extends Control
## Asset inspect popup — shows a larger preview of an asset with all its
## states, metadata, and a Place button. Opens when clicking an asset
## thumbnail in the editor palette.

signal place_requested(asset_id: String)

const COLOR_BG = Color(0.05, 0.03, 0.02, 0.7)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_LABEL = Color(0.54, 0.48, 0.31, 1.0)
const COLOR_BTN_BG = Color(0.35, 0.25, 0.12, 1.0)
const COLOR_BTN_BORDER = Color(0.55, 0.42, 0.25, 1.0)
const COLOR_STATE_DEFAULT = Color(0.85, 0.75, 0.35, 0.9)
const COLOR_STATE_BORDER = Color(0.3, 0.24, 0.15, 0.5)

const PREVIEW_SIZE: int = 128
const STATE_THUMB_SIZE: int = 64

var _font: Font = null
var _panel: PanelContainer = null
var _content: VBoxContainer = null
var _current_asset_id: String = ""

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

    # Centered panel — smaller than config, just for one asset
    _panel = PanelContainer.new()
    _panel.anchor_left = 0.25
    _panel.anchor_right = 0.75
    _panel.anchor_top = 0.15
    _panel.anchor_bottom = 0.85
    _panel.offset_left = 0
    _panel.offset_right = 0
    _panel.offset_top = 0
    _panel.offset_bottom = 0

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
    panel_style.content_margin_left = 24.0
    panel_style.content_margin_right = 24.0
    panel_style.content_margin_top = 20.0
    panel_style.content_margin_bottom = 20.0
    _panel.add_theme_stylebox_override("panel", panel_style)
    add_child(_panel)

    _content = VBoxContainer.new()
    _content.add_theme_constant_override("separation", 12)
    _panel.add_child(_content)

func _input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        _close()
        get_viewport().set_input_as_handled()
    # Consume all mouse clicks when popup is visible so editor doesn't process them
    if event is InputEventMouseButton and event.pressed:
        get_viewport().set_input_as_handled()

func _on_bg_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        _close()

signal closed

func _close() -> void:
    visible = false
    closed.emit()

## Show the popup for a specific asset.
func show_asset(asset_id: String) -> void:
    _current_asset_id = asset_id
    var asset = Catalog.assets.get(asset_id, {})
    if asset.is_empty():
        return

    # Clear previous content
    for child in _content.get_children():
        child.queue_free()

    var asset_name: String = asset.get("name", asset_id)
    var states: Array = asset.get("states", [])
    var default_state: String = asset.get("defaultState", asset.get("default_state", "default"))
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))
    var layer: String = asset.get("layer", "objects")
    var category: String = asset.get("category", "")
    var pack = asset.get("pack", {})
    var pack_name: String = ""
    if pack is Dictionary:
        pack_name = pack.get("name", "")

    # Asset name
    var name_label = Label.new()
    name_label.text = asset_name
    name_label.add_theme_color_override("font_color", COLOR_TEXT)
    name_label.add_theme_font_override("font", _font)
    name_label.add_theme_font_size_override("font_size", 24)
    name_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    _content.add_child(name_label)

    # Large preview — proportionally scaled
    var state_info = Catalog.get_state(asset_id)
    if state_info != null:
        var texture = Catalog.get_sprite_texture(state_info)
        if texture != null:
            var preview_container = CenterContainer.new()
            _content.add_child(preview_container)

            var tex_rect = TextureRect.new()
            tex_rect.texture = texture
            # Scale to fit PREVIEW_SIZE while maintaining aspect ratio
            var native_size: Vector2 = texture.get_size()
            var scale_factor: float = minf(PREVIEW_SIZE / native_size.x, PREVIEW_SIZE / native_size.y)
            # At least 2x for tiny sprites
            if scale_factor < 2.0:
                scale_factor = 2.0
            tex_rect.custom_minimum_size = native_size * scale_factor
            tex_rect.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
            tex_rect.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
            preview_container.add_child(tex_rect)

    # Metadata
    var meta_box = VBoxContainer.new()
    meta_box.add_theme_constant_override("separation", 2)
    _content.add_child(meta_box)

    _add_meta_line(meta_box, "ID: " + asset_id)
    _add_meta_line(meta_box, "Category: " + category)
    _add_meta_line(meta_box, "Layer: " + layer + "  Anchor: (" + str(anchor_x) + ", " + str(anchor_y) + ")")
    if pack_name != "":
        _add_meta_line(meta_box, "Pack: " + pack_name)

    # States section
    if states.size() > 0:
        var states_header = Label.new()
        states_header.text = "STATES (" + str(states.size()) + ")"
        states_header.add_theme_color_override("font_color", COLOR_LABEL)
        states_header.add_theme_font_size_override("font_size", 12)
        _content.add_child(states_header)

        var states_flow = HBoxContainer.new()
        states_flow.add_theme_constant_override("separation", 8)
        _content.add_child(states_flow)

        for state in states:
            _add_state_thumb(states_flow, state, state.get("state", "") == default_state)

    # Place button
    var btn_container = CenterContainer.new()
    _content.add_child(btn_container)

    var place_btn = Button.new()
    place_btn.text = "Place on Map"
    place_btn.add_theme_color_override("font_color", COLOR_TEXT)
    place_btn.add_theme_font_override("font", _font)
    place_btn.add_theme_font_size_override("font_size", 18)
    place_btn.custom_minimum_size = Vector2(200, 0)

    var btn_style = StyleBoxFlat.new()
    btn_style.bg_color = COLOR_BTN_BG
    btn_style.border_width_left = 1
    btn_style.border_width_top = 1
    btn_style.border_width_right = 1
    btn_style.border_width_bottom = 1
    btn_style.border_color = COLOR_BTN_BORDER
    btn_style.corner_radius_left_top = 3
    btn_style.corner_radius_right_top = 3
    btn_style.corner_radius_left_bottom = 3
    btn_style.corner_radius_right_bottom = 3
    btn_style.content_margin_top = 8.0
    btn_style.content_margin_bottom = 8.0
    place_btn.add_theme_stylebox_override("normal", btn_style)

    var hover_style = btn_style.duplicate()
    hover_style.bg_color = Color(0.17, 0.17, 0.10, 1.0)
    place_btn.add_theme_stylebox_override("hover", hover_style)

    place_btn.pressed.connect(_on_place_pressed)
    btn_container.add_child(place_btn)

    visible = true

func _add_meta_line(container: VBoxContainer, text: String) -> void:
    var label = Label.new()
    label.text = text
    label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    label.add_theme_font_size_override("font_size", 12)
    container.add_child(label)

func _add_state_thumb(container: HBoxContainer, state: Dictionary, is_default: bool) -> void:
    var state_name: String = state.get("state", "")

    var vbox = VBoxContainer.new()
    vbox.add_theme_constant_override("separation", 2)
    container.add_child(vbox)

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
        tex_rect.custom_minimum_size = Vector2(STATE_THUMB_SIZE, STATE_THUMB_SIZE)
        thumb_panel.add_child(tex_rect)

    var label = Label.new()
    label.text = state_name.to_upper()
    label.add_theme_font_size_override("font_size", 10)
    label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    if is_default:
        label.add_theme_color_override("font_color", COLOR_STATE_DEFAULT)
    else:
        label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    vbox.add_child(label)

func _on_place_pressed() -> void:
    visible = false
    place_requested.emit(_current_asset_id)
