extends CanvasLayer
## Editor — handles object placement, selection, and movement.
## Sits on a CanvasLayer so UI elements stay screen-fixed.

enum Mode { SELECT, PLACE, MOVE }

var current_mode: Mode = Mode.SELECT
var selected_asset_id: String = ""
var selected_object: Node2D = null
var ghost_sprite: Sprite2D = null

# Reference to the world node
@onready var world: Node2D = get_node("/root/Main/World")
@onready var camera: Camera2D = get_node("/root/Main/Camera")

func _ready() -> void:
    # Create ghost sprite for placement preview
    ghost_sprite = Sprite2D.new()
    ghost_sprite.centered = false
    ghost_sprite.modulate = Color(1, 1, 1, 0.5)
    ghost_sprite.visible = false
    ghost_sprite.z_index = 1000
    # Ghost needs to be in the world space, not canvas layer
    world.add_child(ghost_sprite)

func _unhandled_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed:
        if event.button_index == MOUSE_BUTTON_LEFT:
            match current_mode:
                Mode.PLACE:
                    _place_at_mouse(event.position)
                Mode.SELECT:
                    _select_at_mouse(event.position)

        if event.button_index == MOUSE_BUTTON_RIGHT:
            # Right click cancels placement mode
            if current_mode == Mode.PLACE:
                set_mode(Mode.SELECT)

    if event is InputEventMouseMotion:
        if current_mode == Mode.PLACE and ghost_sprite.visible:
            ghost_sprite.global_position = _screen_to_world(event.position)
            _apply_ghost_offset()

    # Keyboard shortcuts
    if event is InputEventKey and event.pressed:
        if event.keycode == KEY_DELETE and selected_object != null:
            world.remove_object(selected_object)
            selected_object = null
        if event.keycode == KEY_ESCAPE:
            set_mode(Mode.SELECT)

func set_mode(mode: Mode) -> void:
    current_mode = mode
    if mode != Mode.PLACE:
        ghost_sprite.visible = false
        selected_asset_id = ""
    if mode != Mode.SELECT:
        _deselect()

## Called by the UI when the user picks an asset from the palette.
func select_asset_for_placement(asset_id: String) -> void:
    selected_asset_id = asset_id
    current_mode = Mode.PLACE

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

func _apply_ghost_offset() -> void:
    if selected_asset_id == "":
        return
    var asset = Catalog.assets.get(selected_asset_id, {})
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))
    var tex = ghost_sprite.texture
    if tex != null:
        ghost_sprite.offset = Vector2(
            -tex.region.size.x * anchor_x,
            -tex.region.size.y * anchor_y
        )

func _place_at_mouse(screen_pos: Vector2) -> void:
    if selected_asset_id == "":
        return
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    world.add_object(selected_asset_id, world_pos)

func _select_at_mouse(screen_pos: Vector2) -> void:
    _deselect()
    var world_pos: Vector2 = _screen_to_world(screen_pos)

    # Find the closest object within a reasonable radius
    var best_node: Node2D = null
    var best_dist: float = 32.0  # Max selection radius in world pixels

    for child in world.get_node("Objects").get_children():
        var dist: float = child.position.distance_to(world_pos)
        if dist < best_dist:
            best_dist = dist
            best_node = child

    if best_node != null:
        selected_object = best_node
        # Highlight selected object
        var sprite = best_node.get_child(0) as Sprite2D
        if sprite != null:
            sprite.modulate = Color(1.2, 1.2, 1.0, 1.0)

func _deselect() -> void:
    if selected_object != null:
        var sprite = selected_object.get_child(0) as Sprite2D
        if sprite != null:
            sprite.modulate = Color(1, 1, 1, 1)
        selected_object = null

func _screen_to_world(screen_pos: Vector2) -> Vector2:
    # Convert screen coordinates to world coordinates using the camera
    var viewport: Viewport = get_viewport()
    var canvas_transform: Transform2D = viewport.get_canvas_transform()
    return canvas_transform.affine_inverse() * screen_pos
