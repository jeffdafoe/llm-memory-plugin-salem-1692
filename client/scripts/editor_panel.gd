extends PanelContainer
## Editor side panel — 240px left sidebar with tool buttons, selection info,
## and an asset catalog grouped by category. Shows when Edit mode is active.

signal asset_selected(asset_id: String)
signal delete_requested

# Theme colors (matching top bar / login screen)
const COLOR_BG = Color(0.12, 0.09, 0.07, 0.95)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_LABEL = Color(0.54, 0.48, 0.31, 1.0)
const COLOR_BTN_BG = Color(0.35, 0.25, 0.12, 1.0)
const COLOR_BTN_BORDER = Color(0.55, 0.42, 0.25, 1.0)
const COLOR_BTN_ACTIVE_BG = Color(0.29, 0.29, 0.19, 1.0)
const COLOR_BTN_ACTIVE_BORDER = Color(0.54, 0.48, 0.31, 1.0)
const COLOR_ITEM_HOVER = Color(0.17, 0.17, 0.10, 1.0)
const COLOR_ITEM_SELECTED = Color(0.23, 0.23, 0.16, 1.0)
const COLOR_ITEM_BORDER = Color(0.42, 0.42, 0.25, 1.0)

const PANEL_WIDTH: float = 240.0
const TOP_BAR_HEIGHT: float = 40.0
const THUMB_SIZE: int = 48

var _font: Font = null
var _select_button: Button = null
var _delete_button: Button = null
var _selection_info: VBoxContainer = null
var _selection_label: Label = null
var _catalog_container: VBoxContainer = null
var _selected_item: Control = null
var _selected_asset_id: String = ""

# Track category sections for collapsing
var _category_sections: Dictionary = {}

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    # Style the panel
    var panel_style = StyleBoxFlat.new()
    panel_style.bg_color = COLOR_BG
    panel_style.border_width_right = 1
    panel_style.border_color = COLOR_BORDER
    panel_style.content_margin_left = 8.0
    panel_style.content_margin_right = 8.0
    panel_style.content_margin_top = 8.0
    panel_style.content_margin_bottom = 8.0
    add_theme_stylebox_override("panel", panel_style)

    # Position: left side, below top bar, full height
    anchor_left = 0.0
    anchor_top = 0.0
    anchor_right = 0.0
    anchor_bottom = 1.0
    offset_left = 0
    offset_right = PANEL_WIDTH
    offset_top = TOP_BAR_HEIGHT
    offset_bottom = 0

    # Main vertical layout
    var vbox = VBoxContainer.new()
    vbox.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    vbox.size_flags_vertical = Control.SIZE_EXPAND_FILL
    vbox.add_theme_constant_override("separation", 8)
    add_child(vbox)

    # Tool buttons row
    var tools_box = HBoxContainer.new()
    tools_box.add_theme_constant_override("separation", 6)
    vbox.add_child(tools_box)

    _select_button = _make_tool_button("Select")
    _select_button.pressed.connect(_on_select_pressed)
    tools_box.add_child(_select_button)

    _delete_button = _make_tool_button("Delete")
    _delete_button.pressed.connect(_on_delete_pressed)
    _delete_button.disabled = true
    tools_box.add_child(_delete_button)

    # Set select as active by default
    _set_tool_active(_select_button, true)

    # Separator
    var sep = HSeparator.new()
    sep.add_theme_color_override("separator_color", Color(0.4, 0.32, 0.2, 0.4))
    vbox.add_child(sep)

    # Selection info (hidden when nothing selected)
    _selection_info = VBoxContainer.new()
    _selection_info.visible = false
    _selection_info.add_theme_constant_override("separation", 4)
    vbox.add_child(_selection_info)

    var sel_header = Label.new()
    sel_header.text = "SELECTED"
    sel_header.add_theme_color_override("font_color", COLOR_LABEL)
    sel_header.add_theme_font_size_override("font_size", 11)
    _selection_info.add_child(sel_header)

    _selection_label = Label.new()
    _selection_label.add_theme_color_override("font_color", COLOR_TEXT)
    _selection_label.add_theme_font_override("font", _font)
    _selection_label.add_theme_font_size_override("font_size", 14)
    _selection_label.autowrap_mode = TextServer.AUTOWRAP_WORD
    _selection_info.add_child(_selection_label)

    var sel_sep = HSeparator.new()
    sel_sep.add_theme_color_override("separator_color", Color(0.4, 0.32, 0.2, 0.4))
    _selection_info.add_child(sel_sep)

    # Scrollable catalog area
    var scroll = ScrollContainer.new()
    scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    vbox.add_child(scroll)

    _catalog_container = VBoxContainer.new()
    _catalog_container.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _catalog_container.add_theme_constant_override("separation", 4)
    scroll.add_child(_catalog_container)

## Build the catalog UI from the loaded asset data.
## Called after Catalog.catalog_loaded.
func build_catalog() -> void:
    # Clear existing items
    for child in _catalog_container.get_children():
        child.queue_free()
    _category_sections.clear()

    # Sort categories for consistent ordering
    var cat_names: Array = Catalog.categories.keys()
    cat_names.sort()

    for cat_name in cat_names:
        var assets: Array = Catalog.categories[cat_name]
        _add_category_section(cat_name, assets)

func _add_category_section(cat_name: String, assets: Array) -> void:
    var section = VBoxContainer.new()
    section.add_theme_constant_override("separation", 4)
    _catalog_container.add_child(section)

    # Category header (clickable to collapse)
    var header = Button.new()
    header.text = cat_name.to_upper()
    header.flat = true
    header.add_theme_color_override("font_color", COLOR_LABEL)
    header.add_theme_font_size_override("font_size", 11)
    header.alignment = HORIZONTAL_ALIGNMENT_LEFT
    section.add_child(header)

    # Grid of asset thumbnails
    var grid = GridContainer.new()
    # Fit items within 224px (240 - 16 padding), each item is ~56px (48 thumb + 8 gap)
    grid.columns = 4
    grid.add_theme_constant_override("h_separation", 4)
    grid.add_theme_constant_override("v_separation", 4)
    section.add_child(grid)

    # Toggle collapse on header click
    header.pressed.connect(func(): grid.visible = not grid.visible)

    _category_sections[cat_name] = {"section": section, "grid": grid}

    # Sort assets by name for consistent ordering
    var sorted_assets = assets.duplicate()
    sorted_assets.sort_custom(func(a, b): return a.get("name", a.get("id", "")) < b.get("name", b.get("id", "")))

    for asset in sorted_assets:
        _add_catalog_item(grid, asset)

func _add_catalog_item(grid: GridContainer, asset: Dictionary) -> void:
    var asset_id: String = asset.get("id", "")
    var asset_name: String = asset.get("name", asset_id)

    # Container for the thumbnail
    var item = PanelContainer.new()
    item.custom_minimum_size = Vector2(THUMB_SIZE + 4, THUMB_SIZE + 4)
    item.tooltip_text = asset_name

    var item_style = StyleBoxFlat.new()
    item_style.bg_color = Color(0.15, 0.12, 0.08, 1.0)
    item_style.border_width_left = 1
    item_style.border_width_top = 1
    item_style.border_width_right = 1
    item_style.border_width_bottom = 1
    item_style.border_color = Color(0.3, 0.24, 0.15, 0.5)
    item_style.corner_radius_left_top = 2
    item_style.corner_radius_right_top = 2
    item_style.corner_radius_left_bottom = 2
    item_style.corner_radius_right_bottom = 2
    item.add_theme_stylebox_override("panel", item_style)
    item.set_meta("asset_id", asset_id)
    item.set_meta("style_normal", item_style)

    # Render the sprite thumbnail
    var state_info = Catalog.get_state(asset_id)
    if state_info != null:
        var texture = Catalog.get_sprite_texture(state_info)
        if texture != null:
            var tex_rect = TextureRect.new()
            tex_rect.texture = texture
            tex_rect.expand_mode = TextureRect.EXPAND_FIT_WIDTH_PROPORTIONAL
            tex_rect.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
            tex_rect.custom_minimum_size = Vector2(THUMB_SIZE, THUMB_SIZE)
            item.add_child(tex_rect)

    # Mouse interaction
    item.gui_input.connect(_on_item_input.bind(item, asset_id))
    item.mouse_entered.connect(_on_item_hover.bind(item, true))
    item.mouse_exited.connect(_on_item_hover.bind(item, false))

    grid.add_child(item)

func _on_item_input(event: InputEvent, item: Control, asset_id: String) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        _select_catalog_item(item, asset_id)

func _on_item_hover(item: Control, hovering: bool) -> void:
    if item == _selected_item:
        return
    var style: StyleBoxFlat = item.get_meta("style_normal")
    if hovering:
        var hover = style.duplicate()
        hover.bg_color = COLOR_ITEM_HOVER
        item.add_theme_stylebox_override("panel", hover)
    else:
        item.add_theme_stylebox_override("panel", style)

func _select_catalog_item(item: Control, asset_id: String) -> void:
    # Deselect previous
    if _selected_item != null:
        var old_style: StyleBoxFlat = _selected_item.get_meta("style_normal")
        _selected_item.add_theme_stylebox_override("panel", old_style)

    # Select new
    _selected_item = item
    _selected_asset_id = asset_id
    var selected_style: StyleBoxFlat = item.get_meta("style_normal").duplicate()
    selected_style.bg_color = COLOR_ITEM_SELECTED
    selected_style.border_color = COLOR_ITEM_BORDER
    item.add_theme_stylebox_override("panel", selected_style)

    # Switch select button to inactive (we're in place mode now)
    _set_tool_active(_select_button, false)

    asset_selected.emit(asset_id)

func _make_tool_button(label: String) -> Button:
    var btn = Button.new()
    btn.text = label
    btn.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    btn.add_theme_color_override("font_color", COLOR_TEXT)
    btn.add_theme_font_override("font", _font)
    btn.add_theme_font_size_override("font_size", 14)

    var style = StyleBoxFlat.new()
    style.bg_color = COLOR_BTN_BG
    style.border_width_left = 1
    style.border_width_top = 1
    style.border_width_right = 1
    style.border_width_bottom = 1
    style.border_color = COLOR_BTN_BORDER
    style.corner_radius_left_top = 3
    style.corner_radius_right_top = 3
    style.corner_radius_left_bottom = 3
    style.corner_radius_right_bottom = 3
    style.content_margin_left = 8.0
    style.content_margin_right = 8.0
    style.content_margin_top = 4.0
    style.content_margin_bottom = 4.0
    btn.add_theme_stylebox_override("normal", style)

    var hover = style.duplicate()
    hover.bg_color = Color(0.23, 0.23, 0.16, 1.0)
    btn.add_theme_stylebox_override("hover", hover)

    var pressed = style.duplicate()
    pressed.bg_color = Color(0.25, 0.18, 0.08, 1.0)
    btn.add_theme_stylebox_override("pressed", pressed)

    var disabled_style = style.duplicate()
    disabled_style.bg_color = Color(0.20, 0.15, 0.08, 0.5)
    disabled_style.border_color = Color(0.3, 0.24, 0.15, 0.3)
    btn.add_theme_stylebox_override("disabled", disabled_style)
    btn.add_theme_color_override("font_disabled_color", Color(0.5, 0.43, 0.32, 0.4))

    return btn

func _set_tool_active(btn: Button, active: bool) -> void:
    if active:
        var active_style = StyleBoxFlat.new()
        active_style.bg_color = COLOR_BTN_ACTIVE_BG
        active_style.border_width_left = 1
        active_style.border_width_top = 1
        active_style.border_width_right = 1
        active_style.border_width_bottom = 1
        active_style.border_color = COLOR_BTN_ACTIVE_BORDER
        active_style.corner_radius_left_top = 3
        active_style.corner_radius_right_top = 3
        active_style.corner_radius_left_bottom = 3
        active_style.corner_radius_right_bottom = 3
        active_style.content_margin_left = 8.0
        active_style.content_margin_right = 8.0
        active_style.content_margin_top = 4.0
        active_style.content_margin_bottom = 4.0
        btn.add_theme_stylebox_override("normal", active_style)
        btn.add_theme_color_override("font_color", Color(0.78, 0.72, 0.48, 1.0))
    else:
        var style = StyleBoxFlat.new()
        style.bg_color = COLOR_BTN_BG
        style.border_width_left = 1
        style.border_width_top = 1
        style.border_width_right = 1
        style.border_width_bottom = 1
        style.border_color = COLOR_BTN_BORDER
        style.corner_radius_left_top = 3
        style.corner_radius_right_top = 3
        style.corner_radius_left_bottom = 3
        style.corner_radius_right_bottom = 3
        style.content_margin_left = 8.0
        style.content_margin_right = 8.0
        style.content_margin_top = 4.0
        style.content_margin_bottom = 4.0
        btn.add_theme_stylebox_override("normal", style)
        btn.add_theme_color_override("font_color", COLOR_TEXT)

func _on_select_pressed() -> void:
    # Deselect catalog item
    if _selected_item != null:
        var old_style: StyleBoxFlat = _selected_item.get_meta("style_normal")
        _selected_item.add_theme_stylebox_override("panel", old_style)
        _selected_item = null
        _selected_asset_id = ""
    _set_tool_active(_select_button, true)
    # Signal empty asset to return editor to select mode
    asset_selected.emit("")

func _on_delete_pressed() -> void:
    delete_requested.emit()

## Called by editor when an object is selected/deselected on the map.
func show_selection(asset_id: String) -> void:
    if asset_id == "":
        _selection_info.visible = false
        _delete_button.disabled = true
        return
    _selection_info.visible = true
    _delete_button.disabled = false
    var asset = Catalog.assets.get(asset_id, {})
    var name: String = asset.get("name", asset_id)
    _selection_label.text = name

## Called when editor exits place mode (right-click cancel, escape, etc.)
func clear_catalog_selection() -> void:
    if _selected_item != null:
        var old_style: StyleBoxFlat = _selected_item.get_meta("style_normal")
        _selected_item.add_theme_stylebox_override("panel", old_style)
        _selected_item = null
        _selected_asset_id = ""
    _set_tool_active(_select_button, true)
