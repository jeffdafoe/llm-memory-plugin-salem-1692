extends Control
## Config panel — full-screen overlay showing all assets in a card grid
## with metadata and state variant thumbnails. Matches the old TypeScript
## layout: category headers, multi-column cards, asset ID, layer info,
## anchor, state count, pack name, and state thumbnails with labels.

signal closed

# Theme colors
const COLOR_BG = Color(0.05, 0.03, 0.02, 0.85)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_LABEL = Color(0.54, 0.48, 0.31, 1.0)
const COLOR_CARD_BG = Color(0.15, 0.12, 0.08, 1.0)
const COLOR_CARD_BORDER = Color(0.35, 0.28, 0.17, 0.7)
const COLOR_STATE_DEFAULT = Color(0.85, 0.75, 0.35, 0.9)
const COLOR_STATE_BORDER = Color(0.3, 0.24, 0.15, 0.5)

const THUMB_SIZE: int = 64
const TOP_BAR_HEIGHT: float = 40.0
const CARD_WIDTH: float = 200.0

var _font: Font = null
var _scroll: ScrollContainer = null
var _content: VBoxContainer = null
var _summary_label: Label = null
const SCROLL_SPEED: float = 60.0

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    # Full-screen overlay
    anchors_preset = Control.PRESET_FULL_RECT
    anchor_right = 1.0
    anchor_bottom = 1.0

    # Dim background — click to close
    var bg = ColorRect.new()
    bg.color = COLOR_BG
    bg.anchors_preset = Control.PRESET_FULL_RECT
    bg.anchor_right = 1.0
    bg.anchor_bottom = 1.0
    bg.gui_input.connect(_on_bg_input)
    add_child(bg)

    # Center panel
    var panel = PanelContainer.new()
    panel.anchor_left = 0.02
    panel.anchor_right = 0.98
    panel.anchor_top = 0.0
    panel.anchor_bottom = 1.0
    panel.offset_top = TOP_BAR_HEIGHT + 4
    panel.offset_bottom = -4

    var panel_style = StyleBoxFlat.new()
    panel_style.bg_color = COLOR_PANEL_BG
    panel_style.border_width_left = 2
    panel_style.border_width_top = 2
    panel_style.border_width_right = 2
    panel_style.border_width_bottom = 2
    panel_style.border_color = COLOR_BORDER
    panel_style.corner_radius_left_top = 4
    panel_style.corner_radius_right_top = 4
    panel_style.corner_radius_left_bottom = 4
    panel_style.corner_radius_right_bottom = 4
    panel_style.content_margin_left = 16.0
    panel_style.content_margin_right = 16.0
    panel_style.content_margin_top = 12.0
    panel_style.content_margin_bottom = 12.0
    panel.add_theme_stylebox_override("panel", panel_style)
    add_child(panel)

    # Scrollable content
    var scroll = ScrollContainer.new()
    _scroll = scroll
    scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_AUTO
    panel.add_child(scroll)

    _content = VBoxContainer.new()
    _content.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _content.add_theme_constant_override("separation", 12)
    scroll.add_child(_content)

    # Summary line
    _summary_label = Label.new()
    _summary_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _summary_label.add_theme_font_size_override("font_size", 12)
    _content.add_child(_summary_label)

## Handle scroll manually — the camera and editor _input handlers
## run before GUI Controls, so the ScrollContainer never gets scroll events.
func _input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventMouseButton and event.pressed:
        if event.button_index == MOUSE_BUTTON_WHEEL_UP:
            _scroll.scroll_vertical -= int(SCROLL_SPEED)
            get_viewport().set_input_as_handled()
        if event.button_index == MOUSE_BUTTON_WHEEL_DOWN:
            _scroll.scroll_vertical += int(SCROLL_SPEED)
            get_viewport().set_input_as_handled()
    if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        _close()
        get_viewport().set_input_as_handled()

func _on_bg_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        _close()

func _close() -> void:
    visible = false
    closed.emit()

## Build the asset reference from the loaded catalog.
func build_reference() -> void:
    # Clear existing content (keep summary label)
    var children = _content.get_children()
    for i in range(1, children.size()):
        children[i].queue_free()

    # Count totals
    var total_assets: int = Catalog.assets.size()
    var total_states: int = 0
    for asset_id in Catalog.assets:
        var asset = Catalog.assets[asset_id]
        total_states += asset.get("states", []).size()
    _summary_label.text = str(total_assets) + " assets, " + str(total_states) + " states"

    # Group assets by category
    var cat_names: Array = Catalog.categories.keys()
    cat_names.sort()

    for cat_name in cat_names:
        var assets: Array = Catalog.categories[cat_name]
        _add_category_section(cat_name, assets)

func _add_category_section(cat_name: String, assets: Array) -> void:
    # Category header
    var header = Label.new()
    header.text = cat_name.to_upper()
    header.add_theme_color_override("font_color", COLOR_LABEL)
    header.add_theme_font_override("font", _font)
    header.add_theme_font_size_override("font_size", 16)
    _content.add_child(header)

    # Wrapping grid — use a FlowContainer-style layout via GridContainer
    # with enough columns to fill the width
    var grid = GridContainer.new()
    grid.columns = 6
    grid.add_theme_constant_override("h_separation", 8)
    grid.add_theme_constant_override("v_separation", 8)
    grid.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _content.add_child(grid)

    # Sort assets by name
    var sorted_assets = assets.duplicate()
    sorted_assets.sort_custom(func(a, b): return a.get("name", a.get("id", "")) < b.get("name", b.get("id", "")))

    for asset in sorted_assets:
        _add_asset_card(grid, asset)

func _add_asset_card(grid: GridContainer, asset: Dictionary) -> void:
    var asset_id: String = asset.get("id", "")
    var asset_name: String = asset.get("name", asset_id)
    var states: Array = asset.get("states", [])
    var default_state: String = asset.get("defaultState", asset.get("default_state", "default"))
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))
    var layer: String = asset.get("layer", "objects")
    var pack = asset.get("pack", {})
    var pack_name: String = ""
    if pack is Dictionary:
        pack_name = pack.get("name", "")

    # Card container
    var card = PanelContainer.new()
    card.custom_minimum_size = Vector2(CARD_WIDTH, 0)
    card.size_flags_horizontal = Control.SIZE_EXPAND_FILL

    var card_style = StyleBoxFlat.new()
    card_style.bg_color = COLOR_CARD_BG
    card_style.border_width_left = 1
    card_style.border_width_top = 1
    card_style.border_width_right = 1
    card_style.border_width_bottom = 1
    card_style.border_color = COLOR_CARD_BORDER
    card_style.corner_radius_left_top = 3
    card_style.corner_radius_right_top = 3
    card_style.corner_radius_left_bottom = 3
    card_style.corner_radius_right_bottom = 3
    card_style.content_margin_left = 8.0
    card_style.content_margin_right = 8.0
    card_style.content_margin_top = 6.0
    card_style.content_margin_bottom = 6.0
    card.add_theme_stylebox_override("panel", card_style)
    grid.add_child(card)

    var vbox = VBoxContainer.new()
    vbox.add_theme_constant_override("separation", 2)
    card.add_child(vbox)

    # Asset name (bold)
    var name_label = Label.new()
    name_label.text = asset_name
    name_label.add_theme_color_override("font_color", COLOR_TEXT)
    name_label.add_theme_font_override("font", _font)
    name_label.add_theme_font_size_override("font_size", 14)
    vbox.add_child(name_label)

    # Asset ID
    var id_label = Label.new()
    id_label.text = asset_id
    id_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    id_label.add_theme_font_size_override("font_size", 10)
    vbox.add_child(id_label)

    # Metadata line: layer, anchor, states count
    var meta_label = Label.new()
    meta_label.text = "layer: " + layer + " anchor: (" + str(anchor_x) + ", " + str(anchor_y) + ") states: " + str(states.size())
    meta_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    meta_label.add_theme_font_size_override("font_size", 10)
    vbox.add_child(meta_label)

    # Pack name
    if pack_name != "":
        var pack_label = Label.new()
        pack_label.text = pack_name
        pack_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        pack_label.add_theme_font_size_override("font_size", 10)
        vbox.add_child(pack_label)

    # State thumbnails row
    var states_box = HBoxContainer.new()
    states_box.add_theme_constant_override("separation", 6)
    vbox.add_child(states_box)

    for state in states:
        _add_state_thumb(states_box, state, state.get("state", "") == default_state)

func _add_state_thumb(container: HBoxContainer, state: Dictionary, is_default: bool) -> void:
    var state_name: String = state.get("state", "")
    var frame_count: int = state.get("frame_count", 1)
    var frame_rate: float = state.get("frame_rate", 0.0)

    var vbox = VBoxContainer.new()
    vbox.add_theme_constant_override("separation", 2)
    container.add_child(vbox)

    # Thumbnail with border
    var thumb_panel = PanelContainer.new()
    var thumb_style = StyleBoxFlat.new()
    thumb_style.bg_color = Color(0.1, 0.08, 0.05, 1.0)
    thumb_style.border_width_left = 1
    thumb_style.border_width_top = 1
    thumb_style.border_width_right = 1
    thumb_style.border_width_bottom = 1
    if is_default:
        thumb_style.border_color = COLOR_STATE_DEFAULT
    else:
        thumb_style.border_color = COLOR_STATE_BORDER
    thumb_style.corner_radius_left_top = 2
    thumb_style.corner_radius_right_top = 2
    thumb_style.corner_radius_left_bottom = 2
    thumb_style.corner_radius_right_bottom = 2
    thumb_panel.add_theme_stylebox_override("panel", thumb_style)
    vbox.add_child(thumb_panel)

    var texture = Catalog.get_sprite_texture(state)
    if texture != null:
        var tex_rect = TextureRect.new()
        tex_rect.texture = texture
        tex_rect.expand_mode = TextureRect.EXPAND_FIT_WIDTH_PROPORTIONAL
        tex_rect.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
        tex_rect.custom_minimum_size = Vector2(THUMB_SIZE, THUMB_SIZE)
        thumb_panel.add_child(tex_rect)

        # Animate multi-frame states by cycling the texture
        if frame_count > 1 and frame_rate > 0:
            var all_frames: Array = []
            var sprite_frames: SpriteFrames = Catalog.get_sprite_frames(state)
            if sprite_frames != null:
                for i in range(sprite_frames.get_frame_count("default")):
                    all_frames.append(sprite_frames.get_frame_texture("default", i))
            if all_frames.size() > 1:
                var timer = Timer.new()
                timer.wait_time = 1.0 / frame_rate
                timer.autostart = true
                var frame_idx: Array = [0]  # wrapped in array for closure capture
                timer.timeout.connect(func():
                    frame_idx[0] = (frame_idx[0] + 1) % all_frames.size()
                    tex_rect.texture = all_frames[frame_idx[0]]
                )
                thumb_panel.add_child(timer)
    else:
        var placeholder = ColorRect.new()
        placeholder.custom_minimum_size = Vector2(THUMB_SIZE, THUMB_SIZE)
        placeholder.color = Color(0.2, 0.15, 0.1, 1.0)
        thumb_panel.add_child(placeholder)

    # State name label
    var label = Label.new()
    label.text = state_name.to_upper()
    label.add_theme_font_size_override("font_size", 10)
    label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    if is_default:
        label.add_theme_color_override("font_color", COLOR_STATE_DEFAULT)
    else:
        label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    vbox.add_child(label)
