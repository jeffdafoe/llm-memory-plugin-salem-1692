extends CanvasLayer
## Editor — handles object placement, selection, drag-to-move, and deletion.
## Sits on a CanvasLayer so UI elements stay screen-fixed.
## Coordinates with camera.gd (disables left-click pan when active).

signal object_selected(asset_id: String)
signal object_deselected
signal mode_changed(mode: Mode)

enum Mode { SELECT, PLACE, MOVE }

var current_mode: Mode = Mode.SELECT
var selected_asset_id: String = ""
var selected_object: Node2D = null
var ghost_sprite: Sprite2D = null
var active: bool = false

# Drag-to-move state
var _dragging: bool = false
var _drag_start_world: Vector2 = Vector2.ZERO
var _drag_start_obj_pos: Vector2 = Vector2.ZERO
var _drag_threshold: float = 4.0  # Pixels before drag starts
var _drag_pending: bool = false
var _drag_mouse_start: Vector2 = Vector2.ZERO

# References
@onready var world: Node2D = get_node("/root/Main/World")
@onready var camera: Camera2D = get_node("/root/Main/Camera")

func _ready() -> void:
    # Create ghost sprite for placement preview
    # Scale 2x to match placed objects (16px native sprites rendered at 32px)
    ghost_sprite = Sprite2D.new()
    ghost_sprite.centered = false
    ghost_sprite.scale = Vector2(2, 2)
    ghost_sprite.modulate = Color(1, 1, 1, 0.5)
    ghost_sprite.visible = false
    ghost_sprite.z_index = 1000
    # Ghost needs to be in the world space, not canvas layer
    world.add_child(ghost_sprite)

func _unhandled_input(event: InputEvent) -> void:
    if not active:
        return

    if event is InputEventMouseButton:
        if event.button_index == MOUSE_BUTTON_LEFT:
            if event.pressed:
                _on_left_press(event.position)
            else:
                _on_left_release(event.position)

        if event.button_index == MOUSE_BUTTON_RIGHT and event.pressed:
            # Right-click cancels placement mode
            if current_mode == Mode.PLACE:
                set_mode(Mode.SELECT)

    if event is InputEventMouseMotion:
        if current_mode == Mode.PLACE and ghost_sprite.visible:
            ghost_sprite.global_position = _screen_to_world(event.position)
            _apply_ghost_offset()
        if _drag_pending:
            var dist: float = event.position.distance_to(_drag_mouse_start)
            if dist >= _drag_threshold:
                _drag_pending = false
                _dragging = true
        if _dragging:
            _drag_move(event.position)

    # Keyboard shortcuts
    if event is InputEventKey and event.pressed:
        if event.keycode == KEY_DELETE and selected_object != null:
            _delete_selected()
        if event.keycode == KEY_ESCAPE:
            set_mode(Mode.SELECT)

func _on_left_press(screen_pos: Vector2) -> void:
    match current_mode:
        Mode.PLACE:
            _place_at_mouse(screen_pos)
            get_viewport().set_input_as_handled()
        Mode.SELECT:
            var hit = _find_object_at(screen_pos)
            if hit != null:
                _select_object(hit)
                # Start potential drag
                _drag_pending = true
                _drag_mouse_start = screen_pos
                _drag_start_world = _screen_to_world(screen_pos)
                _drag_start_obj_pos = hit.position
                get_viewport().set_input_as_handled()
            else:
                _deselect()
                # Don't consume — let camera handle it for pan via middle-click
                # (left-click pan is disabled when editor is active)

func _on_left_release(screen_pos: Vector2) -> void:
    if _dragging:
        _drag_end(screen_pos)
    _drag_pending = false
    _dragging = false

func set_mode(new_mode: Mode) -> void:
    current_mode = new_mode
    if new_mode != Mode.PLACE:
        ghost_sprite.visible = false
        selected_asset_id = ""
    if new_mode != Mode.SELECT:
        _deselect()
    _dragging = false
    _drag_pending = false
    mode_changed.emit(new_mode)

## Called by the editor panel when the user picks an asset from the catalog.
func select_asset_for_placement(asset_id: String) -> void:
    if asset_id == "":
        set_mode(Mode.SELECT)
        return

    selected_asset_id = asset_id
    current_mode = Mode.PLACE
    _deselect()

    # Set up ghost sprite preview
    var state_info = Catalog.get_state(asset_id)
    if state_info == null:
        return

    var texture = Catalog.get_sprite_texture(state_info)
    if texture == null:
        return

    ghost_sprite.texture = texture
    ghost_sprite.visible = true
    _apply_ghost_offset()
    mode_changed.emit(Mode.PLACE)

func _apply_ghost_offset() -> void:
    if selected_asset_id == "":
        return
    var asset = Catalog.assets.get(selected_asset_id, {})
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))
    var tex = ghost_sprite.texture
    if tex != null:
        # Offset is in local coords (pre-scale), so use raw texture size
        # The 2x scale on the sprite handles world sizing
        ghost_sprite.offset = Vector2(
            -tex.region.size.x * anchor_x,
            -tex.region.size.y * anchor_y
        )

func _place_at_mouse(screen_pos: Vector2) -> void:
    if selected_asset_id == "":
        return
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    world.add_object(selected_asset_id, world_pos)

func _find_object_at(screen_pos: Vector2) -> Node2D:
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var best_node: Node2D = null
    var best_dist: float = 32.0  # Max selection radius in world pixels

    for child in world.get_node("Objects").get_children():
        var dist: float = child.position.distance_to(world_pos)
        if dist < best_dist:
            best_dist = dist
            best_node = child

    return best_node

func _select_object(node: Node2D) -> void:
    _deselect()
    selected_object = node
    # Highlight selected object
    var sprite = node.get_child(0) as Sprite2D
    if sprite != null:
        sprite.modulate = Color(1.2, 1.2, 1.0, 1.0)
    var asset_id: String = node.get_meta("asset_id", "")
    object_selected.emit(asset_id)

func _deselect() -> void:
    if selected_object != null:
        var sprite = selected_object.get_child(0) as Sprite2D
        if sprite != null:
            sprite.modulate = Color(1, 1, 1, 1)
        selected_object = null
        object_deselected.emit()

func _delete_selected() -> void:
    if selected_object == null:
        return
    world.remove_object(selected_object)
    selected_object = null
    object_deselected.emit()

## Delete the currently selected object (called by panel's Delete button).
func delete_selection() -> void:
    _delete_selected()

# --- Drag-to-move ---

func _drag_move(screen_pos: Vector2) -> void:
    if selected_object == null:
        return
    var current_world: Vector2 = _screen_to_world(screen_pos)
    var delta: Vector2 = current_world - _drag_start_world
    selected_object.position = _drag_start_obj_pos + delta

func _drag_end(screen_pos: Vector2) -> void:
    if selected_object == null:
        return
    var current_world: Vector2 = _screen_to_world(screen_pos)
    var delta: Vector2 = current_world - _drag_start_world
    var new_pos: Vector2 = _drag_start_obj_pos + delta
    selected_object.position = new_pos
    # Persist the move to the server
    world.move_object(selected_object, new_pos)

# --- Coordinate conversion ---

func _screen_to_world(screen_pos: Vector2) -> Vector2:
    var viewport: Viewport = get_viewport()
    var canvas_transform: Transform2D = viewport.get_canvas_transform()
    return canvas_transform.affine_inverse() * screen_pos
