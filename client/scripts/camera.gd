extends Camera2D
## Camera with pan (left/middle-click drag or touch drag) and zoom (scroll wheel or pinch).
## Clamped to the map bounds so grey void is never visible.
##
## When editor_active is true, left-click pan is disabled (editor owns left-click).
## Middle-click pan always works regardless of editor state.

# Zoom limits
const ZOOM_MIN: float = 0.1
const ZOOM_MAX: float = 6.0
const ZOOM_STEP: float = 0.1

# Map bounds in world coordinates (set by Main after terrain is built)
var map_bounds: Rect2 = Rect2(0, 0, 2304, 1664)  # 72*32, 52*32

# When true, left-click is reserved for the editor — only middle/right-click pans.
# Exception: if the editor didn't consume the left-click (empty space hit),
# the editor_ref.left_click_used flag will be false and we allow panning.
var editor_active: bool = false

# Reference to the editor node — set by main.gd so camera can check left_click_used
var editor_ref: CanvasLayer = null

# When true, a modal overlay is open — don't zoom on scroll
var modal_open: bool = false

# Pan state
var _panning: bool = false
var _pan_start: Vector2 = Vector2.ZERO

## UI panel area — clicks here belong to the editor UI, not the camera
const PANEL_WIDTH: float = 240.0
const TOP_BAR_HEIGHT: float = 40.0

## Returns true if the screen position is over the editor UI panel area.
func _is_over_ui(pos: Vector2) -> bool:
    if not editor_active:
        return pos.y < TOP_BAR_HEIGHT
    if pos.y < TOP_BAR_HEIGHT:
        return true
    if pos.x < PANEL_WIDTH:
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
            # Stop any active pan if mouse enters UI
            if not event.pressed:
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
        var delta: Vector2 = event.position - _pan_start
        _pan_start = event.position
        position -= delta / zoom
        _clamp_position()

    # Touch support — single finger pan
    if event is InputEventScreenDrag:
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

## Jump the camera to a world position, clamped to map bounds. Used by the
## panel's People list to focus on the selected villager — especially
## useful when they're indoors and otherwise invisible on the map.
func center_on(world_pos: Vector2) -> void:
    position = world_pos
    _clamp_position()
