extends Camera2D
## Camera with pan (left/middle-click drag or touch drag) and zoom (scroll wheel or pinch).
## Clamped to the map bounds so grey void is never visible.
##
## When editor_active is true, left-click pan is disabled (editor owns left-click).
## Middle-click pan always works regardless of editor state.

# Zoom limits
## Minimum zoom floor. Loaded from /api/village/world at startup and
## reapplied on the zoom_settings_changed broadcast so admins can retune
## the floor without a client restart.
var ZOOM_MIN: float = 0.3
const ZOOM_MAX: float = 6.0
const ZOOM_STEP: float = 0.1

## Called from world.gd after the world config is loaded (and from the
## zoom_settings_changed WS handler) with the floor appropriate for the
## viewing user. If the new floor would leave the camera too close, clamp
## down to match. Too-far case is naturally fine.
func set_zoom_floor(value: float) -> void:
    if value <= 0:
        return
    ZOOM_MIN = value
    if zoom.x < ZOOM_MIN:
        zoom = Vector2(ZOOM_MIN, ZOOM_MIN)
        _clamp_position()

# Map bounds in world coordinates (set by Main after terrain is built)
var map_bounds: Rect2 = Rect2(0, 0, 2304, 1664)  # 72*32, 52*32

# When true, left-click is reserved for the editor — only middle/right-click pans.
# Exception: if the editor didn't consume the left-click (empty space hit),
# the editor_ref.left_click_used flag will be false and we allow panning.
var editor_active: bool = false

# Reference to the editor node — set by main.gd so camera can check left_click_used
var editor_ref: CanvasLayer = null

# Registered UI panels — _is_over_ui consults this list before declaring
# a wheel/pan event "in world." Any UI Control that wants to eat input
# (the editor sidebar, the talk panel sheet, future floating windows)
# calls register_ui_panel(self) on _ready and unregister_ui_panel(self)
# on tree_exiting. The camera doesn't need to know about each panel by
# name — drop a panel in, register it, done.
#
# Each entry is a Control whose get_global_rect() defines the input-eating
# zone. Visibility is checked via is_visible_in_tree() so a hidden panel
# doesn't keep blocking input.
var ui_panels: Array[Control] = []

# When true, a modal overlay is open — don't zoom on scroll
var modal_open: bool = false

# Pan state
var _panning: bool = false
var _pan_start: Vector2 = Vector2.ZERO

## Top bar height — the editor's top toolbar is always present and pinned
## to the top of the viewport, so a constant suffices. Could become a
## registered Control later if the toolbar ever becomes optional.
const TOP_BAR_HEIGHT: float = 40.0

## Register a UI panel. Wheel/pan events whose pointer position falls
## inside the panel's global rect (and whose panel is visible in tree)
## are treated as UI input, not world input — _is_over_ui returns true,
## the camera steps aside, and the panel's own controls handle the event.
##
## Idempotent — registering the same panel twice is a no-op.
func register_ui_panel(panel: Control) -> void:
    if panel == null or panel in ui_panels:
        return
    ui_panels.append(panel)


## Unregister a UI panel. Safe to call even if the panel was never
## registered (no-op). Typically called from a panel's tree_exiting
## signal so a freed panel doesn't leave a dangling Control reference.
func unregister_ui_panel(panel: Control) -> void:
    ui_panels.erase(panel)


## Returns true if the screen position is over UI input the camera
## should not steal — checks the top bar (hardcoded constant; always
## present) plus every registered ui_panel that's currently visible.
##
## Panels self-register via register_ui_panel(self); the editor sidebar
## and talk panel both go through that. Adding a new panel requires no
## camera changes.
func _is_over_ui(pos: Vector2) -> bool:
    if pos.y < TOP_BAR_HEIGHT:
        return true
    for panel in ui_panels:
        if not is_instance_valid(panel):
            continue
        if not panel.is_visible_in_tree():
            continue
        if panel.get_global_rect().has_point(pos):
            return true
    return false

## All camera input runs in _input so it works even when editor UI Controls
## (ScrollContainer, PanelContainer) would otherwise consume events in
## _unhandled_input. A position check skips clicks on the UI panel area.
func _input(event: InputEvent) -> void:
    if modal_open:
        return

    if event is InputEventMouseButton and event.pressed:
        # Don't zoom when scrolling over the editor sidebar
        if not _is_over_ui(event.position):
            if event.button_index == MOUSE_BUTTON_WHEEL_UP:
                _zoom_at(event.position, ZOOM_STEP)
                get_viewport().set_input_as_handled()
            if event.button_index == MOUSE_BUTTON_WHEEL_DOWN:
                _zoom_at(event.position, -ZOOM_STEP)
                get_viewport().set_input_as_handled()

    # Pinch zoom also in _input for same reason
    if event is InputEventMagnifyGesture:
        var new_zoom: float = clampf(zoom.x * event.factor, ZOOM_MIN, ZOOM_MAX)
        zoom = Vector2(new_zoom, new_zoom)
        _clamp_position()
        get_viewport().set_input_as_handled()

    # Pan: middle-click and right-click always, left-click only when editor is not active
    if event is InputEventMouseButton:
        if _is_over_ui(event.position):
            # Any mouse interaction with the UI ends any active map pan —
            # both press and release. Without this, _panning could stay
            # wedged true across a UI interaction (e.g. clicking a scrollbar)
            # and subsequent motion events would pan the map.
            _panning = false
            return
        var is_pan_button: bool = false
        if event.button_index == MOUSE_BUTTON_MIDDLE:
            is_pan_button = true
        if event.button_index == MOUSE_BUTTON_RIGHT:
            is_pan_button = true
        if event.button_index == MOUSE_BUTTON_LEFT:
            # In editor mode, only pan if editor didn't use the click (empty space)
            if not editor_active:
                is_pan_button = true
            else:
                if editor_ref != null and not editor_ref.left_click_used:
                    is_pan_button = true
        if is_pan_button:
            _panning = event.pressed
            if _panning:
                _pan_start = event.position

    # Pan motion
    if event is InputEventMouseMotion and _panning:
        # Safeguard: if the release event got eaten by a higher-priority
        # handler (e.g. a UI button press consuming the mouseup) and left
        # _panning wedged true, the button_mask on subsequent motion events
        # is still 0 — meaning no button is actually held. Clear the flag
        # and bail so the map doesn't pan without user input.
        if event.button_mask == 0:
            _panning = false
            return
        # If the cursor moves into the UI panel mid-pan (e.g. user grabs the
        # selection panel's scrollbar while still holding a button), end the
        # pan rather than continuing to translate the map from scrollbar
        # motion. Resuming would need a fresh click on the map.
        if _is_over_ui(event.position):
            _panning = false
            return
        var delta: Vector2 = event.position - _pan_start
        _pan_start = event.position
        position -= delta / zoom
        _clamp_position()

    # Touch support — single finger pan. Same UI-area gate as mouse motion
    # so a touch drag inside the panel doesn't pan the map underneath.
    if event is InputEventScreenDrag:
        if _is_over_ui(event.position):
            return
        position -= event.relative / zoom
        _clamp_position()

func _zoom_at(mouse_pos: Vector2, step: float) -> void:
    var old_zoom: float = zoom.x
    var new_zoom: float = clampf(old_zoom + step, ZOOM_MIN, ZOOM_MAX)
    if new_zoom == old_zoom:
        return

    var viewport_size: Vector2 = get_viewport_rect().size
    var mouse_offset: Vector2 = (mouse_pos - viewport_size / 2.0) / old_zoom
    var new_mouse_offset: Vector2 = (mouse_pos - viewport_size / 2.0) / new_zoom

    position += mouse_offset - new_mouse_offset
    zoom = Vector2(new_zoom, new_zoom)
    _clamp_position()

## Keep the camera within map bounds so grey void is never visible.
## The visible area depends on viewport size and zoom level.
func _clamp_position() -> void:
    var viewport_size: Vector2 = get_viewport_rect().size
    var half_view: Vector2 = viewport_size / (2.0 * zoom)

    # The camera center must stay far enough from the edges
    # that the viewport doesn't extend past the map
    var min_x: float = map_bounds.position.x + half_view.x
    var min_y: float = map_bounds.position.y + half_view.y
    var max_x: float = map_bounds.end.x - half_view.x
    var max_y: float = map_bounds.end.y - half_view.y

    # If the map is smaller than the viewport at this zoom, center it
    if min_x > max_x:
        position.x = map_bounds.position.x + map_bounds.size.x / 2.0
    else:
        position.x = clampf(position.x, min_x, max_x)

    if min_y > max_y:
        position.y = map_bounds.position.y + map_bounds.size.y / 2.0
    else:
        position.y = clampf(position.y, min_y, max_y)

## Smooth tween duration when center_on is called from the sidebar list.
const CENTER_ON_DURATION: float = 0.3

var _center_on_tween: Tween = null

## Smoothly pan the camera to a world position, clamped to map bounds.
## Used by the sidebar Villagers browser and the selection panel's People
## list to focus on a selected villager — especially useful when they're
## indoors and otherwise invisible on the map. Replaces any in-flight
## tween so rapid clicks always target the latest request.
func center_on(world_pos: Vector2) -> void:
    var target: Vector2 = _clamp_position_value(world_pos)
    if _center_on_tween != null and _center_on_tween.is_valid():
        _center_on_tween.kill()
    _center_on_tween = create_tween()
    _center_on_tween.set_trans(Tween.TRANS_SINE).set_ease(Tween.EASE_OUT)
    _center_on_tween.tween_property(self, "position", target, CENTER_ON_DURATION)

## Compute the clamped position for an arbitrary target without mutating
## the camera. Mirrors the logic in _clamp_position so the tween lands
## inside map bounds instead of being clamped mid-flight.
func _clamp_position_value(target: Vector2) -> Vector2:
    var viewport_size: Vector2 = get_viewport_rect().size
    var half_view: Vector2 = viewport_size / (2.0 * zoom)
    var min_x: float = map_bounds.position.x + half_view.x
    var min_y: float = map_bounds.position.y + half_view.y
    var max_x: float = map_bounds.end.x - half_view.x
    var max_y: float = map_bounds.end.y - half_view.y
    var out := Vector2.ZERO
    if min_x > max_x:
        out.x = map_bounds.position.x + map_bounds.size.x / 2.0
    else:
        out.x = clampf(target.x, min_x, max_x)
    if min_y > max_y:
        out.y = map_bounds.position.y + map_bounds.size.y / 2.0
    else:
        out.y = clampf(target.y, min_y, max_y)
    return out
