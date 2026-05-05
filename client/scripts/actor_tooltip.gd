extends CanvasLayer
## Actor tooltip — click an NPC or PC sprite in viewer mode and a small
## floating panel appears showing who they are.
##
## Why click and not hover (cf. object_tooltip.gd which is hover-driven):
## play mode is mobile-portable per project guidelines. Hover doesn't exist
## on touch. Click does. Click-to-toggle gives the same affordance on both.
##
## Why a separate node and not piggyback on object_tooltip: hover and click
## are different idioms. Object hover is incidental — you're probably aiming
## for the click-to-walk under the cursor and the tooltip is a bonus.
## Actor identification is intentional — you actively want to know who that
## is. Mixing the two confuses dismissal semantics (does moving the cursor
## off the actor close the panel? what about on touch?).
##
## Click priority: this node uses _input (not _unhandled_input) so it sees
## the press before main.gd's PC walk handler. Main exposes the hit-test
## result via is_press_over_actor() so the walk handler can skip walking
## when the click lands on a sprite.

const COLOR_BG = Color(0.10, 0.08, 0.06, 0.92)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 0.8)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)

var _panel: PanelContainer = null
var _name_label: Label = null
var _role_label: Label = null
var _work_label: Label = null
var _home_label: Label = null
var _font: Font = null

# References set by main.gd
var world: Node2D = null
var editor: CanvasLayer = null
var camera: Node = null

# Currently shown actor's container node, or null. Used to support
# tap-to-toggle: clicking the same actor again dismisses the panel.
var _shown_node: Node2D = null

func _ready() -> void:
    layer = 3  # Same layer as object_tooltip — above world, below editor panels.
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    _panel = PanelContainer.new()
    _panel.visible = false

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
    style.content_margin_left = 12.0
    style.content_margin_right = 12.0
    style.content_margin_top = 8.0
    style.content_margin_bottom = 8.0
    _panel.add_theme_stylebox_override("panel", style)

    var vbox = VBoxContainer.new()
    vbox.add_theme_constant_override("separation", 2)
    _panel.add_child(vbox)

    _name_label = Label.new()
    _name_label.add_theme_color_override("font_color", COLOR_TEXT)
    _name_label.add_theme_font_override("font", _font)
    _name_label.add_theme_font_size_override("font_size", 16)
    vbox.add_child(_name_label)

    _role_label = Label.new()
    _role_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _role_label.add_theme_font_override("font", _font)
    _role_label.add_theme_font_size_override("font_size", 12)
    _role_label.visible = false
    vbox.add_child(_role_label)

    _work_label = Label.new()
    _work_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _work_label.add_theme_font_override("font", _font)
    _work_label.add_theme_font_size_override("font_size", 12)
    _work_label.visible = false
    vbox.add_child(_work_label)

    _home_label = Label.new()
    _home_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _home_label.add_theme_font_override("font", _font)
    _home_label.add_theme_font_size_override("font_size", 12)
    _home_label.visible = false
    vbox.add_child(_home_label)

    add_child(_panel)

func _input(event: InputEvent) -> void:
    if event is InputEventMouseMotion:
        _update_hover(event.position)

func _update_hover(screen_pos: Vector2) -> void:
    if world == null:
        return

    if editor != null and editor.active:
        _hide_panel()
        return
    if camera != null and camera.modal_open:
        _hide_panel()
        return
    if camera != null and camera._is_over_ui(screen_pos):
        _hide_panel()
        return

    var hit: Node2D = _find_actor_at(screen_pos)
    if hit == null:
        _hide_panel()
        return

    if hit != _shown_node:
        _show_panel_for(hit, screen_pos)
    else:
        call_deferred("_position_panel_near", screen_pos)

## Returns true if a position lands on an actor sprite. Kept for the
## main.gd walk-handler integration even though the panel itself is now
## hover-driven — a left-click on an NPC sprite still shouldn't fire a
## walk to the tile underneath.
func is_press_over_actor(screen_pos: Vector2) -> bool:
    return _find_actor_at(screen_pos) != null

func _show_panel_for(node: Node2D, screen_pos: Vector2) -> void:
    var display_name: String = str(node.get_meta("display_name", ""))
    if display_name == "":
        display_name = "(unnamed)"
    _name_label.text = display_name

    # NPC vs PC: NPCs carry a non-empty llm_memory_agent meta. PCs are
    # human players and have no agent link. The role/work/home lines
    # are NPC-specific — for PCs we show the name alone.
    var agent: String = str(node.get_meta("llm_memory_agent", ""))
    if agent == "":
        _role_label.visible = false
        _work_label.visible = false
        _home_label.visible = false
    else:
        var attrs = node.get_meta("attributes", [])
        var role: String = _format_role(attrs)
        if role != "":
            _role_label.text = role
            _role_label.visible = true
        else:
            _role_label.visible = false

        var work_id: String = str(node.get_meta("work_structure_id", ""))
        var work_name: String = _structure_name(work_id)
        if work_name != "":
            _work_label.text = "Works at the " + work_name
            _work_label.visible = true
        else:
            _work_label.visible = false

        var home_id: String = str(node.get_meta("home_structure_id", ""))
        var home_name: String = _structure_name(home_id)
        if home_name != "":
            _home_label.text = "Lives at the " + home_name
            _home_label.visible = true
        else:
            _home_label.visible = false

    _shown_node = node
    _panel.visible = true
    # Defer position calc one frame so the panel has its measured size.
    # Without this, screen-edge clamping reads stale dimensions on the
    # first show after a content change and the panel can run off-screen.
    call_deferred("_position_panel_near", screen_pos)

func _position_panel_near(screen_pos: Vector2) -> void:
    if not _panel.visible:
        return
    var panel_size: Vector2 = _panel.size
    var viewport_size: Vector2 = get_viewport().get_visible_rect().size

    # Anchor above-right of the click so a finger on touch devices
    # doesn't cover the panel. Same offset object_tooltip uses.
    var pos: Vector2 = Vector2(screen_pos.x + 16, screen_pos.y - panel_size.y - 8)
    if pos.x + panel_size.x > viewport_size.x:
        pos.x = screen_pos.x - panel_size.x - 16
    if pos.x < 0:
        pos.x = 8
    if pos.y < 0:
        pos.y = screen_pos.y + 16
    if pos.y + panel_size.y > viewport_size.y:
        pos.y = viewport_size.y - panel_size.y - 8
    _panel.position = pos

func _hide_panel() -> void:
    if _panel.visible:
        _panel.visible = false
    _shown_node = null

## Title-case the first attribute slug. Slugs are role identifiers
## like "tavernkeeper", "merchant", "blacksmith", "herbalist" — render
## the first one as the displayed role; future multi-role NPCs would
## need a richer formatter.
func _format_role(attrs) -> String:
    if not (attrs is Array):
        return ""
    if attrs.is_empty():
        return ""
    var slug: String = str(attrs[0])
    if slug == "":
        return ""
    return slug.substr(0, 1).to_upper() + slug.substr(1)

## Look up a structure's display_name by id. Empty string for unknown
## ids or unnamed structures (decoratives, etc.) — caller hides the
## line when this returns empty.
func _structure_name(structure_id: String) -> String:
    if structure_id == "" or world == null:
        return ""
    if not world.placed_objects.has(structure_id):
        return ""
    var node: Node2D = world.placed_objects[structure_id]
    if node == null:
        return ""
    return str(node.get_meta("display_name", ""))

## Hit-test placed_npcs (which holds both NPCs and PCs — see
## apply_pc_appeared in world.gd). Returns the closest container whose
## sprite bounding box contains the click, or null.
func _find_actor_at(screen_pos: Vector2) -> Node2D:
    if world == null:
        return null
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var best_node: Node2D = null
    var best_dist: float = INF

    for actor_id in world.placed_npcs:
        var container: Node2D = world.placed_npcs[actor_id]
        if container == null or not container.visible:
            continue
        var sprite_node: Node2D = null
        for child in container.get_children():
            if child is AnimatedSprite2D or child is Sprite2D:
                sprite_node = child
                break
        if sprite_node == null:
            continue

        var size: Vector2 = _get_sprite_size(sprite_node)
        if size == Vector2.ZERO:
            continue
        var world_size: Vector2 = size * sprite_node.scale
        var origin: Vector2 = container.position + sprite_node.position
        var rect = Rect2(origin, world_size)
        if rect.has_point(world_pos):
            var dist: float = container.position.distance_to(world_pos)
            if dist < best_dist:
                best_dist = dist
                best_node = container

    return best_node

func _get_sprite_size(sprite_node: Node2D) -> Vector2:
    if sprite_node is Sprite2D:
        var tex = sprite_node.texture
        if tex != null:
            return tex.get_size()
    if sprite_node is AnimatedSprite2D:
        var frames: SpriteFrames = sprite_node.sprite_frames
        if frames == null:
            return Vector2.ZERO
        # SpriteFrames.new() auto-creates an empty "default" animation in
        # Godot 4. NPC sprites never populate "default" — they only add
        # frames to "<direction>_<kind>" animations like "south_idle".
        # Picking alphabetically-first gives us "default" (0 frames) and
        # the lookup degenerates to ZERO. Iterate and use the first
        # animation that actually has frames.
        for anim_name in frames.get_animation_names():
            if frames.get_frame_count(anim_name) > 0:
                var tex = frames.get_frame_texture(anim_name, 0)
                if tex != null:
                    return tex.get_size()
    return Vector2.ZERO

func _screen_to_world(screen_pos: Vector2) -> Vector2:
    var viewport: Viewport = get_viewport()
    var canvas_transform: Transform2D = viewport.get_canvas_transform()
    return canvas_transform.affine_inverse() * screen_pos
