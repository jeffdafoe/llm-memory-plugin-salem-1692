extends CanvasLayer
## Object tooltip — shows a small floating label when hovering over objects
## in viewer mode (editor not active). Displays the object name and owner.
##
## Uses _input (not _unhandled_input) for the same reason as editor.gd:
## UI Controls on CanvasLayers consume events between _input and
## _unhandled_input, so hover detection must happen in _input.

const COLOR_BG = Color(0.10, 0.08, 0.06, 0.92)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 0.8)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)

var _tooltip_panel: PanelContainer = null
var _name_label: Label = null
var _owner_label: Label = null
var _font: Font = null

# References set by main.gd
var world: Node2D = null
var editor: CanvasLayer = null

# Track which object we're hovering to avoid redundant updates
var _hovered_node: Node2D = null

func _ready() -> void:
    layer = 3  # Above world, below editor panels
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    _tooltip_panel = PanelContainer.new()
    _tooltip_panel.visible = false

    var style = StyleBoxFlat.new()
    style.bg_color = COLOR_BG
    style.border_width_left = 1
    style.border_width_top = 1
    style.border_width_right = 1
    style.border_width_bottom = 1
    style.border_color = COLOR_BORDER
    style.corner_radius_left_top = 4
    style.corner_radius_right_top = 4
    style.corner_radius_left_bottom = 4
    style.corner_radius_right_bottom = 4
    style.content_margin_left = 10.0
    style.content_margin_right = 10.0
    style.content_margin_top = 6.0
    style.content_margin_bottom = 6.0
    _tooltip_panel.add_theme_stylebox_override("panel", style)

    var vbox = VBoxContainer.new()
    vbox.add_theme_constant_override("separation", 2)
    _tooltip_panel.add_child(vbox)

    _name_label = Label.new()
    _name_label.add_theme_color_override("font_color", COLOR_TEXT)
    _name_label.add_theme_font_override("font", _font)
    _name_label.add_theme_font_size_override("font_size", 14)
    vbox.add_child(_name_label)

    _owner_label = Label.new()
    _owner_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _owner_label.add_theme_font_override("font", _font)
    _owner_label.add_theme_font_size_override("font_size", 12)
    _owner_label.visible = false
    vbox.add_child(_owner_label)

    add_child(_tooltip_panel)

func _input(event: InputEvent) -> void:
    if event is InputEventMouseMotion:
        _update_hover(event.position)

func _update_hover(screen_pos: Vector2) -> void:
    if world == null:
        return

    # Skip if over top bar area
    if screen_pos.y < 40.0:
        _hide_tooltip()
        return

    # Skip if over editor panel area
    if editor != null and editor.active and screen_pos.x < 240.0:
        _hide_tooltip()
        return

    var hit = _find_object_at(screen_pos)
    if hit == null:
        _hide_tooltip()
        return

    if hit != _hovered_node:
        _hovered_node = hit
        _show_tooltip(hit)

    if not _tooltip_panel.visible:
        return

    # Position tooltip near cursor, offset so it doesn't overlap
    _tooltip_panel.position = Vector2(screen_pos.x + 16, screen_pos.y - 8)

    # Keep tooltip on screen
    var viewport_size: Vector2 = get_viewport().get_visible_rect().size
    var tooltip_size: Vector2 = _tooltip_panel.size
    if _tooltip_panel.position.x + tooltip_size.x > viewport_size.x:
        _tooltip_panel.position.x = screen_pos.x - tooltip_size.x - 8
    if _tooltip_panel.position.y + tooltip_size.y > viewport_size.y:
        _tooltip_panel.position.y = screen_pos.y - tooltip_size.y - 8
    if _tooltip_panel.position.y < 0:
        _tooltip_panel.position.y = screen_pos.y + 20

func _show_tooltip(node: Node2D) -> void:
    var asset_id: String = node.get_meta("asset_id", "")
    var owner: String = node.get_meta("owner", "")
    var display_name_meta: String = node.get_meta("display_name", "")
    var is_editing: bool = editor != null and editor.active

    # In viewer mode, only show tooltip if object has an owner or display name
    if not is_editing and owner == "" and display_name_meta == "":
        _hide_tooltip()
        return

    # Show display_name if set, otherwise fall back to catalog asset name
    if display_name_meta != "":
        _name_label.text = display_name_meta
    else:
        var asset = Catalog.assets.get(asset_id, {})
        _name_label.text = asset.get("name", asset_id)

    if owner != "":
        var display_name: String = world.get_owner_display_name(owner)
        _owner_label.text = display_name
        _owner_label.visible = true
    else:
        _owner_label.visible = false

    _tooltip_panel.visible = true

func _hide_tooltip() -> void:
    if _tooltip_panel.visible:
        _tooltip_panel.visible = false
        _hovered_node = null

## Find the object at a screen position using bounding box hit detection.
## Same logic as editor.gd:_find_object_at.
func _find_object_at(screen_pos: Vector2) -> Node2D:
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var best_node: Node2D = null
    var best_dist: float = INF

    for child in world.get_node("Objects").get_children():
        if child.get_child_count() == 0:
            continue
        var sprite_node: Node2D = null
        for grandchild in child.get_children():
            if grandchild is Sprite2D or grandchild is AnimatedSprite2D:
                sprite_node = grandchild
                break
        if sprite_node == null:
            continue

        var region_size: Vector2 = _get_sprite_size(sprite_node)
        if region_size == Vector2.ZERO:
            continue
        var world_size: Vector2 = region_size * sprite_node.scale
        var rect_origin: Vector2 = child.position + sprite_node.position
        var rect = Rect2(rect_origin, world_size)

        if rect.has_point(world_pos):
            var dist: float = child.position.distance_to(world_pos)
            if dist < best_dist:
                best_dist = dist
                best_node = child

    return best_node

## Get texture size from either Sprite2D or AnimatedSprite2D.
func _get_sprite_size(sprite_node: Node2D) -> Vector2:
    if sprite_node is Sprite2D:
        var tex = sprite_node.texture
        if tex != null:
            return tex.get_size()
    if sprite_node is AnimatedSprite2D:
        var frames: SpriteFrames = sprite_node.sprite_frames
        if frames != null and frames.get_frame_count("default") > 0:
            var tex = frames.get_frame_texture("default", 0)
            if tex != null:
                return tex.get_size()
    return Vector2.ZERO

func _screen_to_world(screen_pos: Vector2) -> Vector2:
    var viewport: Viewport = get_viewport()
    var canvas_transform: Transform2D = viewport.get_canvas_transform()
    return canvas_transform.affine_inverse() * screen_pos
