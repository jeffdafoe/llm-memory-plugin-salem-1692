extends CanvasLayer
## Editor — handles object placement, selection, drag-to-move, and deletion.
## Sits on a CanvasLayer so UI elements stay screen-fixed.
## Coordinates with camera.gd (disables left-click pan when active).
##
## Drag tracking uses _input (not _unhandled_input) so UI Controls
## on the CanvasLayer can't swallow mouse motion during a drag.

signal object_selected(asset_id: String)
signal object_deselected
signal mode_changed(mode: Mode)

enum Mode { SELECT, PLACE, MOVE, TERRAIN }

var current_mode: Mode = Mode.SELECT
var selected_asset_id: String = ""
var selected_object: Node2D = null
var ghost_sprite: Sprite2D = null
var active: bool = false

# Terrain painting state
var _terrain_type: int = 0  # Currently selected terrain type (1-6)
var _terrain_painting: bool = false  # True while mouse is held down painting
var _terrain_save_timer: float = 0.0  # Debounce save after painting
const TERRAIN_SAVE_DELAY: float = 2.0  # Seconds after last paint to auto-save

# Selection border node — added as child of selected object
var _selection_border: Node2D = null

# Drag-to-move state
var _dragging: bool = false
var _drag_start_world: Vector2 = Vector2.ZERO
var _drag_start_obj_pos: Vector2 = Vector2.ZERO
var _drag_threshold: float = 4.0  # Screen pixels before drag starts
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

## _input runs BEFORE GUI Controls process events, so drag motion
## and terrain painting can't be swallowed by the editor panel or top bar.
func _input(event: InputEvent) -> void:
    if not active:
        return

    # Terrain painting: hold mouse to paint continuously
    if _terrain_painting:
        if event is InputEventMouseMotion:
            _paint_terrain_at(event.position)
            get_viewport().set_input_as_handled()
        if event is InputEventMouseButton:
            if event.button_index == MOUSE_BUTTON_LEFT and not event.pressed:
                _terrain_painting = false
                _terrain_save_timer = TERRAIN_SAVE_DELAY
                get_viewport().set_input_as_handled()
        return

    # Once a drag is active, we own all mouse events until release
    if _dragging or _drag_pending:
        if event is InputEventMouseMotion:
            if _drag_pending:
                var dist: float = event.position.distance_to(_drag_mouse_start)
                if dist >= _drag_threshold:
                    _drag_pending = false
                    _dragging = true
            if _dragging:
                _drag_move(event.position)
                # Consume so nothing else (camera, UI) reacts
                get_viewport().set_input_as_handled()

        if event is InputEventMouseButton:
            if event.button_index == MOUSE_BUTTON_LEFT and not event.pressed:
                _on_left_release(event.position)
                get_viewport().set_input_as_handled()

func _unhandled_input(event: InputEvent) -> void:
    if not active:
        return

    if event is InputEventMouseButton:
        if event.button_index == MOUSE_BUTTON_LEFT:
            if event.pressed:
                _on_left_press(event.position)

        if event.button_index == MOUSE_BUTTON_RIGHT and event.pressed:
            if current_mode == Mode.PLACE:
                set_mode(Mode.SELECT)

    if event is InputEventMouseMotion:
        if current_mode == Mode.PLACE and ghost_sprite.visible:
            ghost_sprite.global_position = _screen_to_world(event.position)
            _apply_ghost_offset()

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
        Mode.TERRAIN:
            if _terrain_type > 0:
                _terrain_painting = true
                _paint_terrain_at(screen_pos)
                get_viewport().set_input_as_handled()

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
    var best_dist: float = INF

    for child in world.get_node("Objects").get_children():
        if child.get_child_count() == 0:
            continue
        # First child is always the Sprite2D; skip SelectionBorder nodes
        var sprite: Sprite2D = null
        for grandchild in child.get_children():
            if grandchild is Sprite2D:
                sprite = grandchild
                break
        if sprite == null:
            continue

        # Build the sprite's bounding rect in world coordinates
        var tex = sprite.texture
        if tex == null:
            continue
        var region_size: Vector2 = tex.get_size()
        var world_size: Vector2 = region_size * sprite.scale
        var rect_origin: Vector2 = child.position + sprite.position
        var rect = Rect2(rect_origin, world_size)

        if rect.has_point(world_pos):
            # Among overlapping sprites, prefer the one whose anchor is closest
            var dist: float = child.position.distance_to(world_pos)
            if dist < best_dist:
                best_dist = dist
                best_node = child

    return best_node

func _select_object(node: Node2D) -> void:
    _deselect()
    selected_object = node
    _add_selection_border(node)
    var asset_id: String = node.get_meta("asset_id", "")
    object_selected.emit(asset_id)

func _deselect() -> void:
    if selected_object != null:
        _remove_selection_border()
        selected_object = null
        object_deselected.emit()

func _add_selection_border(node: Node2D) -> void:
    _remove_selection_border()
    var sprite: Sprite2D = null
    for child in node.get_children():
        if child is Sprite2D:
            sprite = child
            break
    if sprite == null:
        return
    var tex = sprite.texture
    if tex == null:
        return

    # Calculate the sprite's rect relative to the container
    var region_size: Vector2 = tex.get_size()
    var world_size: Vector2 = region_size * sprite.scale
    var rect_pos: Vector2 = sprite.position
    var padding: float = 3.0

    _selection_border = Node2D.new()
    _selection_border.name = "SelectionBorder"
    _selection_border.z_index = 999
    node.add_child(_selection_border)

    # Draw the border using a Line2D rectangle
    var border = Line2D.new()
    border.width = 2.0
    border.default_color = Color(0.85, 0.75, 0.35, 0.9)
    border.closed = true

    var x0: float = rect_pos.x - padding
    var y0: float = rect_pos.y - padding
    var x1: float = rect_pos.x + world_size.x + padding
    var y1: float = rect_pos.y + world_size.y + padding

    border.add_point(Vector2(x0, y0))
    border.add_point(Vector2(x1, y0))
    border.add_point(Vector2(x1, y1))
    border.add_point(Vector2(x0, y1))

    _selection_border.add_child(border)

func _remove_selection_border() -> void:
    if _selection_border != null:
        _selection_border.queue_free()
        _selection_border = null

func _delete_selected() -> void:
    if selected_object == null:
        return
    _remove_selection_border()
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
    # Update the selection border position (it moves with the container
    # since it's a child, so no action needed)

# --- Terrain painting ---

func set_terrain_type(terrain_type: int) -> void:
    _terrain_type = terrain_type

func _paint_terrain_at(screen_pos: Vector2) -> void:
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var tile: Vector2i = world.world_to_tile(world_pos)
    world.paint_terrain(tile.x, tile.y, _terrain_type)

func _process(delta: float) -> void:
    # Debounced terrain save — saves 2 seconds after the last paint stroke
    if _terrain_save_timer > 0:
        _terrain_save_timer -= delta
        if _terrain_save_timer <= 0:
            world.save_terrain()

# --- Coordinate conversion ---

func _screen_to_world(screen_pos: Vector2) -> Vector2:
    var viewport: Viewport = get_viewport()
    var canvas_transform: Transform2D = viewport.get_canvas_transform()
    return canvas_transform.affine_inverse() * screen_pos
