extends Camera2D
## Camera with pan (left/middle-click drag or touch drag) and zoom (scroll wheel or pinch).
## Clamped to the map bounds so grey void is never visible.

# Zoom limits
const ZOOM_MIN: float = 0.5
const ZOOM_MAX: float = 6.0
const ZOOM_STEP: float = 0.1

# Map bounds in world coordinates (set by Main after terrain is built)
var map_bounds: Rect2 = Rect2(0, 0, 2304, 1664)  # 72*32, 52*32

# Pan state
var _panning: bool = false
var _pan_start: Vector2 = Vector2.ZERO

func _unhandled_input(event: InputEvent) -> void:
    # Left-click or middle-click pan
    if event is InputEventMouseButton:
        if event.button_index == MOUSE_BUTTON_LEFT or event.button_index == MOUSE_BUTTON_MIDDLE:
            _panning = event.pressed
            if _panning:
                _pan_start = event.position

        # Scroll wheel zoom
        if event.button_index == MOUSE_BUTTON_WHEEL_UP and event.pressed:
            _zoom_at(event.position, ZOOM_STEP)
        if event.button_index == MOUSE_BUTTON_WHEEL_DOWN and event.pressed:
            _zoom_at(event.position, -ZOOM_STEP)

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

    # Touch support — pinch zoom handled via InputEventMagnifyGesture
    if event is InputEventMagnifyGesture:
        var new_zoom: float = clampf(zoom.x * event.factor, ZOOM_MIN, ZOOM_MAX)
        zoom = Vector2(new_zoom, new_zoom)
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
