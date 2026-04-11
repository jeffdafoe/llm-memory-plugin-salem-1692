extends Control
## Config panel — full-screen overlay showing all assets with their state
## variants. Opens when Config button is pressed. Click anywhere outside
## or press Escape to close.

signal closed

# Theme colors
const COLOR_BG = Color(0.05, 0.03, 0.02, 0.85)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_LABEL = Color(0.54, 0.48, 0.31, 1.0)
const COLOR_ITEM_BG = Color(0.15, 0.12, 0.08, 1.0)
const COLOR_ITEM_BORDER = Color(0.3, 0.24, 0.15, 0.5)
const COLOR_STATE_ACTIVE = Color(0.85, 0.75, 0.35, 0.9)

const THUMB_SIZE: int = 48
const TOP_BAR_HEIGHT: float = 40.0

var _font: Font = null
var _content: VBoxContainer = null

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
    panel.anchor_left = 0.1
    panel.anchor_right = 0.9
    panel.anchor_top = 0.0
    panel.anchor_bottom = 1.0
    panel.offset_top = TOP_BAR_HEIGHT + 8
    panel.offset_bottom = -8
    panel.offset_left = 0
    panel.offset_right = 0

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
    panel_style.content_margin_top = 16.0
    panel_style.content_margin_bottom = 16.0
    panel.add_theme_stylebox_override("panel", panel_style)
    add_child(panel)

    # Scrollable content inside the panel
    var margin = MarginContainer.new()
    margin.size_flags_vertical = Control.SIZE_EXPAND_FILL
    margin.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    panel.add_child(margin)

    var scroll = ScrollContainer.new()
    scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    margin.add_child(scroll)

    _content = VBoxContainer.new()
    _content.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _content.add_theme_constant_override("separation", 16)
    scroll.add_child(_content)

    # Header
    var header = Label.new()
    header.text = "Asset Reference"
    header.add_theme_color_override("font_color", COLOR_TEXT)
    header.add_theme_font_override("font", _font)
    header.add_theme_font_size_override("font_size", 24)
    header.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    _content.add_child(header)

    var sep = HSeparator.new()
    sep.add_theme_color_override("separator_color", Color(0.4, 0.32, 0.2, 0.4))
    _content.add_child(sep)

func _unhandled_input(event: InputEvent) -> void:
    if not visible:
        return
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
    # Clear existing content (keep header and separator)
    var children = _content.get_children()
    for i in range(2, children.size()):
        children[i].queue_free()

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

    # Sort assets by name
    var sorted_assets = assets.duplicate()
    sorted_assets.sort_custom(func(a, b): return a.get("name", a.get("id", "")) < b.get("name", b.get("id", "")))

    for asset in sorted_assets:
        _add_asset_row(asset)

func _add_asset_row(asset: Dictionary) -> void:
    var asset_id: String = asset.get("id", "")
    var asset_name: String = asset.get("name", asset_id)
    var states: Array = asset.get("states", [])
    var default_state: String = asset.get("defaultState", asset.get("default_state", "default"))
    var pack = asset.get("pack", {})
    var pack_name: String = ""
    if pack is Dictionary:
        pack_name = pack.get("name", "")

    # Row container with background
    var row = PanelContainer.new()
    var row_style = StyleBoxFlat.new()
    row_style.bg_color = COLOR_ITEM_BG
    row_style.border_width_left = 1
    row_style.border_width_top = 1
    row_style.border_width_right = 1
    row_style.border_width_bottom = 1
    row_style.border_color = COLOR_ITEM_BORDER
    row_style.corner_radius_left_top = 3
    row_style.corner_radius_right_top = 3
    row_style.corner_radius_left_bottom = 3
    row_style.corner_radius_right_bottom = 3
    row_style.content_margin_left = 8.0
    row_style.content_margin_right = 8.0
    row_style.content_margin_top = 6.0
    row_style.content_margin_bottom = 6.0
    row.add_theme_stylebox_override("panel", row_style)
    _content.add_child(row)

    var hbox = HBoxContainer.new()
    hbox.add_theme_constant_override("separation", 12)
    row.add_child(hbox)

    # Asset info (name + pack)
    var info_box = VBoxContainer.new()
    info_box.custom_minimum_size = Vector2(160, 0)
    info_box.add_theme_constant_override("separation", 2)
    hbox.add_child(info_box)

    var name_label = Label.new()
    name_label.text = asset_name
    name_label.add_theme_color_override("font_color", COLOR_TEXT)
    name_label.add_theme_font_override("font", _font)
    name_label.add_theme_font_size_override("font_size", 14)
    info_box.add_child(name_label)

    if pack_name != "":
        var pack_label = Label.new()
        pack_label.text = pack_name
        pack_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        pack_label.add_theme_font_size_override("font_size", 11)
        info_box.add_child(pack_label)

    # State thumbnails
    var states_box = HBoxContainer.new()
    states_box.add_theme_constant_override("separation", 8)
    hbox.add_child(states_box)

    for state in states:
        _add_state_thumb(states_box, asset_id, state, state.get("state", "") == default_state)

func _add_state_thumb(container: HBoxContainer, asset_id: String, state: Dictionary, is_default: bool) -> void:
    var state_name: String = state.get("state", "")

    var vbox = VBoxContainer.new()
    vbox.add_theme_constant_override("separation", 2)
    container.add_child(vbox)

    # Thumbnail with border if default state
    var thumb_panel = PanelContainer.new()
    var thumb_style = StyleBoxFlat.new()
    thumb_style.bg_color = Color(0.1, 0.08, 0.05, 1.0)
    thumb_style.border_width_left = 1
    thumb_style.border_width_top = 1
    thumb_style.border_width_right = 1
    thumb_style.border_width_bottom = 1
    if is_default:
        thumb_style.border_color = COLOR_STATE_ACTIVE
    else:
        thumb_style.border_color = COLOR_ITEM_BORDER
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
    else:
        var placeholder = ColorRect.new()
        placeholder.custom_minimum_size = Vector2(THUMB_SIZE, THUMB_SIZE)
        placeholder.color = Color(0.2, 0.15, 0.1, 1.0)
        thumb_panel.add_child(placeholder)

    # State name label
    var label = Label.new()
    label.text = state_name
    label.add_theme_font_size_override("font_size", 10)
    label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    if is_default:
        label.add_theme_color_override("font_color", COLOR_STATE_ACTIVE)
    else:
        label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    vbox.add_child(label)
