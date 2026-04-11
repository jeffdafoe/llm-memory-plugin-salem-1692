extends PanelContainer
## Editor side panel — 240px left sidebar with tool buttons, selection info,
## and an asset catalog grouped by category. Shows when Edit mode is active.

signal asset_selected(asset_id: String)
signal asset_inspect_requested(asset_id: String)
signal delete_requested
signal terrain_mode_toggled(active: bool)
signal terrain_type_selected(terrain_type: int)
signal owner_changed(owner: String)

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
const CELL_SIZE: int = 52  # Grid cell size — sprites render proportionally within

var _font: Font = null
var _select_button: Button = null
var _delete_button: Button = null
var _terrain_button: Button = null
var _selection_info: VBoxContainer = null
var _selection_label: Label = null
var _placed_by_label: Label = null
var _owner_label: Label = null
var _owner_dropdown: OptionButton = null
var _ignoring_dropdown: bool = false
var _catalog_container: VBoxContainer = null
var _terrain_picker: VBoxContainer = null
var _catalog_scroll: ScrollContainer = null
var _selected_item: Control = null
var _selected_asset_id: String = ""
var _terrain_active: bool = false
var _selected_terrain_item: Control = null

# Reference to world for agent name lookups — set by main.gd
var world: Node2D = null

# Track category sections for collapsing
var _category_sections: Dictionary = {}

# Terrain type names and colors for the picker
const TERRAIN_TYPES = [
    {"id": 1, "name": "Dirt", "color": Color(0.55, 0.38, 0.22)},
    {"id": 2, "name": "Light Grass", "color": Color(0.40, 0.55, 0.25)},
    {"id": 3, "name": "Dark Grass", "color": Color(0.25, 0.40, 0.15)},
    {"id": 4, "name": "Cobblestone", "color": Color(0.50, 0.50, 0.48)},
    {"id": 5, "name": "Shallow Water", "color": Color(0.30, 0.50, 0.65)},
    {"id": 6, "name": "Deep Water", "color": Color(0.15, 0.30, 0.55)},
]

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

    # Second row of tools
    var tools_box2 = HBoxContainer.new()
    tools_box2.add_theme_constant_override("separation", 6)
    vbox.add_child(tools_box2)

    _terrain_button = _make_tool_button("Terrain")
    _terrain_button.pressed.connect(_on_terrain_pressed)
    tools_box2.add_child(_terrain_button)

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

    _placed_by_label = Label.new()
    _placed_by_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _placed_by_label.add_theme_font_override("font", _font)
    _placed_by_label.add_theme_font_size_override("font_size", 12)
    _placed_by_label.visible = false
    _selection_info.add_child(_placed_by_label)

    _owner_label = Label.new()
    _owner_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _owner_label.add_theme_font_override("font", _font)
    _owner_label.add_theme_font_size_override("font_size", 12)
    _owner_label.visible = false
    _selection_info.add_child(_owner_label)

    # Owner dropdown — lets editor assign/change object ownership
    var owner_header = Label.new()
    owner_header.text = "OWNER"
    owner_header.add_theme_color_override("font_color", COLOR_LABEL)
    owner_header.add_theme_font_size_override("font_size", 11)
    _selection_info.add_child(owner_header)

    _owner_dropdown = OptionButton.new()
    _owner_dropdown.add_theme_font_override("font", _font)
    _owner_dropdown.add_theme_font_size_override("font_size", 13)
    _owner_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    var dropdown_style = StyleBoxFlat.new()
    dropdown_style.bg_color = COLOR_BTN_BG
    dropdown_style.border_width_left = 1
    dropdown_style.border_width_top = 1
    dropdown_style.border_width_right = 1
    dropdown_style.border_width_bottom = 1
    dropdown_style.border_color = COLOR_BTN_BORDER
    dropdown_style.corner_radius_left_top = 3
    dropdown_style.corner_radius_right_top = 3
    dropdown_style.corner_radius_left_bottom = 3
    dropdown_style.corner_radius_right_bottom = 3
    dropdown_style.content_margin_left = 6.0
    dropdown_style.content_margin_right = 6.0
    dropdown_style.content_margin_top = 4.0
    dropdown_style.content_margin_bottom = 4.0
    _owner_dropdown.add_theme_stylebox_override("normal", dropdown_style)
    _owner_dropdown.item_selected.connect(_on_owner_selected)
    _selection_info.add_child(_owner_dropdown)

    var sel_sep = HSeparator.new()
    sel_sep.add_theme_color_override("separator_color", Color(0.4, 0.32, 0.2, 0.4))
    _selection_info.add_child(sel_sep)

    # Terrain type picker (hidden by default, shown when Terrain tool active)
    _terrain_picker = VBoxContainer.new()
    _terrain_picker.visible = false
    _terrain_picker.add_theme_constant_override("separation", 4)
    vbox.add_child(_terrain_picker)

    var terrain_header = Label.new()
    terrain_header.text = "TERRAIN TYPE"
    terrain_header.add_theme_color_override("font_color", COLOR_LABEL)
    terrain_header.add_theme_font_size_override("font_size", 11)
    _terrain_picker.add_child(terrain_header)

    for t in TERRAIN_TYPES:
        _add_terrain_item(t)

    # Scrollable catalog area
    _catalog_scroll = ScrollContainer.new()
    _catalog_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    _catalog_scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _catalog_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    vbox.add_child(_catalog_scroll)

    _catalog_container = VBoxContainer.new()
    _catalog_container.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _catalog_container.add_theme_constant_override("separation", 4)
    _catalog_scroll.add_child(_catalog_container)

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

    # Container — fixed cell size, sprite renders proportionally inside
    var item = PanelContainer.new()
    item.custom_minimum_size = Vector2(CELL_SIZE, CELL_SIZE)
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

    # Render the sprite at its proportional size within the cell
    var state_info = Catalog.get_state(asset_id)
    if state_info != null:
        var texture = Catalog.get_sprite_texture(state_info)
        if texture != null:
            var center = CenterContainer.new()
            center.custom_minimum_size = Vector2(CELL_SIZE - 4, CELL_SIZE - 4)
            item.add_child(center)

            var tex_rect = TextureRect.new()
            tex_rect.texture = texture
            # Proportional sizing: scale to fit cell while keeping aspect ratio
            # Small sprites stay small, big sprites fill the cell
            var native_size: Vector2 = texture.get_size()
            var max_dim: float = CELL_SIZE - 8.0
            var scale_factor: float = minf(max_dim / native_size.x, max_dim / native_size.y)
            # Cap at 2x (matching world scale) so small items stay small
            if scale_factor > 2.0:
                scale_factor = 2.0
            tex_rect.custom_minimum_size = native_size * scale_factor
            tex_rect.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
            tex_rect.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
            center.add_child(tex_rect)

    # Click opens inspect popup instead of immediately placing
    item.gui_input.connect(_on_item_input.bind(item, asset_id))
    item.mouse_entered.connect(_on_item_hover.bind(item, true))
    item.mouse_exited.connect(_on_item_hover.bind(item, false))

    grid.add_child(item)

func _on_item_input(event: InputEvent, item: Control, asset_id: String) -> void:
    if event is InputEventMouseButton and event.pressed:
        if event.button_index == MOUSE_BUTTON_LEFT:
            _select_catalog_item(item, asset_id)
        if event.button_index == MOUSE_BUTTON_RIGHT:
            asset_inspect_requested.emit(asset_id)

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

func _on_owner_selected(index: int) -> void:
    if _ignoring_dropdown:
        return
    var agent_key: String = _owner_dropdown.get_item_metadata(index)
    owner_changed.emit(agent_key)

    # Update the owner label immediately
    if agent_key != "":
        var display_name: String = agent_key
        if world != null:
            display_name = world.get_owner_display_name(agent_key)
        _owner_label.text = "Owner: " + display_name
        _owner_label.visible = true
    else:
        _owner_label.visible = false

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
func show_selection(info: Dictionary) -> void:
    var asset_id: String = info.get("asset_id", "")
    if asset_id == "":
        _selection_info.visible = false
        _delete_button.disabled = true
        _placed_by_label.visible = false
        _owner_label.visible = false
        return
    _selection_info.visible = true
    _delete_button.disabled = false
    var asset = Catalog.assets.get(asset_id, {})
    var name: String = asset.get("name", asset_id)
    _selection_label.text = name

    var placed_by: String = info.get("placed_by", "")
    if placed_by != "":
        _placed_by_label.text = "Placed by: " + placed_by
        _placed_by_label.visible = true
    else:
        _placed_by_label.visible = false

    var owner: String = info.get("owner", "")
    if owner != "":
        var display_name: String = owner
        if world != null:
            display_name = world.get_owner_display_name(owner)
        _owner_label.text = "Owner: " + display_name
        _owner_label.visible = true
    else:
        _owner_label.visible = false

    # Populate owner dropdown from agent list
    _ignoring_dropdown = true
    _owner_dropdown.clear()
    _owner_dropdown.add_item("No owner", 0)
    _owner_dropdown.set_item_metadata(0, "")
    var selected_index: int = 0
    if world != null:
        var idx: int = 1
        for agent_key in world.agent_list:
            var display: String = world.agent_names.get(agent_key, agent_key)
            _owner_dropdown.add_item(display, idx)
            _owner_dropdown.set_item_metadata(idx, agent_key)
            if agent_key == owner:
                selected_index = idx
            idx += 1
    _owner_dropdown.selected = selected_index
    _ignoring_dropdown = false

## Called when editor exits place mode (right-click cancel, escape, etc.)
func clear_catalog_selection() -> void:
    if _selected_item != null:
        var old_style: StyleBoxFlat = _selected_item.get_meta("style_normal")
        _selected_item.add_theme_stylebox_override("panel", old_style)
        _selected_item = null
        _selected_asset_id = ""
    _set_tool_active(_select_button, true)

# --- Terrain painting ---

func _add_terrain_item(t: Dictionary) -> void:
    var item = PanelContainer.new()
    item.custom_minimum_size = Vector2(0, 32)

    var item_style = StyleBoxFlat.new()
    item_style.bg_color = Color(0.15, 0.12, 0.08, 1.0)
    item_style.border_width_left = 1
    item_style.border_width_top = 1
    item_style.border_width_right = 1
    item_style.border_width_bottom = 1
    item_style.border_color = Color(0.3, 0.24, 0.15, 0.5)
    item_style.corner_radius_left_top = 3
    item_style.corner_radius_right_top = 3
    item_style.corner_radius_left_bottom = 3
    item_style.corner_radius_right_bottom = 3
    item_style.content_margin_left = 8.0
    item_style.content_margin_right = 8.0
    item_style.content_margin_top = 4.0
    item_style.content_margin_bottom = 4.0
    item.add_theme_stylebox_override("panel", item_style)
    item.set_meta("terrain_id", t["id"])
    item.set_meta("style_normal", item_style)

    var hbox = HBoxContainer.new()
    hbox.add_theme_constant_override("separation", 8)
    item.add_child(hbox)

    # Color swatch
    var swatch = ColorRect.new()
    swatch.custom_minimum_size = Vector2(20, 20)
    swatch.color = t["color"]
    hbox.add_child(swatch)

    # Name
    var label = Label.new()
    label.text = t["name"]
    label.add_theme_color_override("font_color", COLOR_TEXT)
    label.add_theme_font_override("font", _font)
    label.add_theme_font_size_override("font_size", 14)
    label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    hbox.add_child(label)

    item.gui_input.connect(_on_terrain_item_input.bind(item, t["id"]))
    _terrain_picker.add_child(item)

func _on_terrain_item_input(event: InputEvent, item: Control, terrain_id: int) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        # Deselect previous
        if _selected_terrain_item != null:
            var old_style: StyleBoxFlat = _selected_terrain_item.get_meta("style_normal")
            _selected_terrain_item.add_theme_stylebox_override("panel", old_style)

        _selected_terrain_item = item
        var sel_style: StyleBoxFlat = item.get_meta("style_normal").duplicate()
        sel_style.bg_color = COLOR_ITEM_SELECTED
        sel_style.border_color = COLOR_ITEM_BORDER
        item.add_theme_stylebox_override("panel", sel_style)

        terrain_type_selected.emit(terrain_id)

func _on_terrain_pressed() -> void:
    _terrain_active = not _terrain_active
    _set_tool_active(_terrain_button, _terrain_active)

    if _terrain_active:
        # Show terrain picker, hide catalog
        _terrain_picker.visible = true
        _catalog_scroll.visible = false
        _set_tool_active(_select_button, false)
        # Deselect any asset
        if _selected_item != null:
            var old_style: StyleBoxFlat = _selected_item.get_meta("style_normal")
            _selected_item.add_theme_stylebox_override("panel", old_style)
            _selected_item = null
            _selected_asset_id = ""
    else:
        # Hide terrain picker, show catalog
        _terrain_picker.visible = false
        _catalog_scroll.visible = true
        _set_tool_active(_select_button, true)
        # Deselect terrain item
        if _selected_terrain_item != null:
            var old_style: StyleBoxFlat = _selected_terrain_item.get_meta("style_normal")
            _selected_terrain_item.add_theme_stylebox_override("panel", old_style)
            _selected_terrain_item = null

    terrain_mode_toggled.emit(_terrain_active)

## Called externally to exit terrain mode (e.g., when switching to select)
func exit_terrain_mode() -> void:
    if _terrain_active:
        _terrain_active = false
        _set_tool_active(_terrain_button, false)
        _terrain_picker.visible = false
        _catalog_scroll.visible = true
        if _selected_terrain_item != null:
            var old_style: StyleBoxFlat = _selected_terrain_item.get_meta("style_normal")
            _selected_terrain_item.add_theme_stylebox_override("panel", old_style)
            _selected_terrain_item = null
