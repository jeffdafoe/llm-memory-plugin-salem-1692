extends Camera2D
## Camera with pan (middle-click drag or touch drag) and zoom (scroll wheel or pinch).

# Zoom limits
const ZOOM_MIN: float = 0.5
const ZOOM_MAX: float = 6.0
const ZOOM_STEP: float = 0.1

# Pan state
var _panning: bool = false
var _pan_start: Vector2 = Vector2.ZERO

func _unhandled_input(event: InputEvent) -> void:
    # Middle-click pan
    if event is InputEventMouseButton:
        if event.button_index == MOUSE_BUTTON_MIDDLE:
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
        # Move camera opposite to mouse direction, scaled by zoom
        position -= delta / zoom

    # Touch support — single finger pan
    if event is InputEventScreenDrag:
        position -= event.relative / zoom

    # Touch support — pinch zoom handled via InputEventMagnifyGesture
    if event is InputEventMagnifyGesture:
        var new_zoom: float = clampf(zoom.x * event.factor, ZOOM_MIN, ZOOM_MAX)
        zoom = Vector2(new_zoom, new_zoom)

func _zoom_at(mouse_pos: Vector2, step: float) -> void:
    # Zoom toward the mouse position so the point under the cursor stays put
    var old_zoom: float = zoom.x
    var new_zoom: float = clampf(old_zoom + step, ZOOM_MIN, ZOOM_MAX)
    if new_zoom == old_zoom:
        return

    # Convert mouse position to world coordinates before and after zoom
    var viewport_size: Vector2 = get_viewport_rect().size
    var mouse_offset: Vector2 = (mouse_pos - viewport_size / 2.0) / old_zoom
    var new_mouse_offset: Vector2 = (mouse_pos - viewport_size / 2.0) / new_zoom

    position += mouse_offset - new_mouse_offset
    zoom = Vector2(new_zoom, new_zoom)
