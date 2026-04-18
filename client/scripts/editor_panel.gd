extends PanelContainer
## Editor side panel — 240px left sidebar with tool buttons, selection info,
## and an asset catalog grouped by category. Shows when Edit mode is active.

signal asset_selected(asset_id: String)
signal asset_inspect_requested(asset_id: String)
signal delete_requested
signal terrain_mode_toggled(active: bool)
signal terrain_type_selected(terrain_type: int)
signal owner_changed(owner: String)
signal display_name_changed(display_name: String)
signal attachment_requested(overlay_asset_id: String)

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
var _name_input: LineEdit = null
var _ignoring_name_input: bool = false
var _catalog_container: VBoxContainer = null
var _terrain_picker: VBoxContainer = null
var _catalog_scroll: ScrollContainer = null
var _selected_item: Control = null
var _selected_asset_id: String = ""
var _terrain_active: bool = false
var _selected_terrain_item: Control = null
var _attachments_section: VBoxContainer = null
var _attachments_grid: GridContainer = null

# Asset-specific fields (owner / name input / attachments) live under this
# VBox so the whole block can be hidden when an NPC is selected instead.
var _asset_fields_section: VBoxContainer = null

# NPC-specific fields (behavior, linked llm_memory_agent). Mutually exclusive
# with _asset_fields_section.
var _npc_fields_section: VBoxContainer = null
var _npc_behavior_label: Label = null
var _npc_agent_label: Label = null

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
    _selection_info.size_flags_vertical = Control.SIZE_EXPAND_FILL
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

    # Asset-only block — hidden when an NPC is selected so the panel doesn't
    # show inapplicable fields (owner, attachments, etc.)
    _asset_fields_section = VBoxContainer.new()
    _asset_fields_section.add_theme_constant_override("separation", 4)
    _asset_fields_section.size_flags_vertical = Control.SIZE_EXPAND_FILL
    _selection_info.add_child(_asset_fields_section)

    _placed_by_label = Label.new()
    _placed_by_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _placed_by_label.add_theme_font_override("font", _font)
    _placed_by_label.add_theme_font_size_override("font_size", 12)
    _placed_by_label.visible = false
    _asset_fields_section.add_child(_placed_by_label)

    _owner_label = Label.new()
    _owner_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _owner_label.add_theme_font_override("font", _font)
    _owner_label.add_theme_font_size_override("font_size", 12)
    _owner_label.visible = false
    _asset_fields_section.add_child(_owner_label)

    # Display name input — lets editor assign a custom name to the object
    var name_header = Label.new()
    name_header.text = "NAME"
    name_header.add_theme_color_override("font_color", COLOR_LABEL)
    name_header.add_theme_font_size_override("font_size", 11)
    _asset_fields_section.add_child(name_header)

    _name_input = LineEdit.new()
    _name_input.placeholder_text = "e.g. General Store"
    _name_input.add_theme_font_override("font", _font)
    _name_input.add_theme_font_size_override("font_size", 13)
    _name_input.add_theme_color_override("font_color", COLOR_TEXT)
    _name_input.add_theme_color_override("font_placeholder_color", Color(0.45, 0.40, 0.30, 1.0))
    _name_input.max_length = 100
    var name_input_style = StyleBoxFlat.new()
    name_input_style.bg_color = COLOR_BTN_BG
    name_input_style.border_width_left = 1
    name_input_style.border_width_top = 1
    name_input_style.border_width_right = 1
    name_input_style.border_width_bottom = 1
    name_input_style.border_color = COLOR_BTN_BORDER
    name_input_style.corner_radius_left_top = 3
    name_input_style.corner_radius_right_top = 3
    name_input_style.corner_radius_left_bottom = 3
    name_input_style.corner_radius_right_bottom = 3
    name_input_style.content_margin_left = 6.0
    name_input_style.content_margin_right = 6.0
    name_input_style.content_margin_top = 4.0
    name_input_style.content_margin_bottom = 4.0
    _name_input.add_theme_stylebox_override("normal", name_input_style)
    _name_input.add_theme_stylebox_override("focus", name_input_style)
    _name_input.text_submitted.connect(_on_name_submitted)
    _name_input.focus_exited.connect(_on_name_focus_lost)
    _asset_fields_section.add_child(_name_input)

    # Owner dropdown — lets editor assign/change object ownership
    var owner_header = Label.new()
    owner_header.text = "OWNER"
    owner_header.add_theme_color_override("font_color", COLOR_LABEL)
    owner_header.add_theme_font_size_override("font_size", 11)
    _asset_fields_section.add_child(owner_header)

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
    _asset_fields_section.add_child(_owner_dropdown)

    # Attachments section — shown when selected object has slots
    _attachments_section = VBoxContainer.new()
    _attachments_section.visible = false
    _attachments_section.size_flags_vertical = Control.SIZE_EXPAND_FILL
    _attachments_section.add_theme_constant_override("separation", 4)
    _asset_fields_section.add_child(_attachments_section)

    var attach_header = Label.new()
    attach_header.text = "ATTACHMENTS"
    attach_header.add_theme_color_override("font_color", COLOR_LABEL)
    attach_header.add_theme_font_size_override("font_size", 11)
    _attachments_section.add_child(attach_header)

    var attach_scroll = ScrollContainer.new()
    attach_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    attach_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    attach_scroll.mouse_filter = Control.MOUSE_FILTER_PASS
    _attachments_section.add_child(attach_scroll)

    _attachments_grid = GridContainer.new()
    _attachments_grid.columns = 4
    _attachments_grid.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _attachments_grid.add_theme_constant_override("h_separation", 4)
    _attachments_grid.add_theme_constant_override("v_separation", 4)
    attach_scroll.add_child(_attachments_grid)

    # NPC-only block — shown in place of _asset_fields_section when an NPC is
    # selected. Minimal for now: behavior + linked llm_memory_agent.
    _npc_fields_section = VBoxContainer.new()
    _npc_fields_section.visible = false
    _npc_fields_section.add_theme_constant_override("separation", 4)
    _selection_info.add_child(_npc_fields_section)

    _npc_behavior_label = Label.new()
    _npc_behavior_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _npc_behavior_label.add_theme_font_override("font", _font)
    _npc_behavior_label.add_theme_font_size_override("font_size", 12)
    _npc_fields_section.add_child(_npc_behavior_label)

    _npc_agent_label = Label.new()
    _npc_agent_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _npc_agent_label.add_theme_font_override("font", _font)
    _npc_agent_label.add_theme_font_size_override("font_size", 12)
    _npc_agent_label.autowrap_mode = TextServer.AUTOWRAP_WORD
    _npc_fields_section.add_child(_npc_agent_label)

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
    grid.visible = false
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

            # Animate if multi-frame
            var frame_count: int = state_info.get("frame_count", 1)
            var frame_rate: float = state_info.get("frame_rate", 0.0)
            if frame_count > 1 and frame_rate > 0:
                var sprite_frames: SpriteFrames = Catalog.get_sprite_frames(state_info)
                if sprite_frames != null:
                    var all_frames: Array = []
                    for i in range(sprite_frames.get_frame_count("default")):
                        all_frames.append(sprite_frames.get_frame_texture("default", i))
                    if all_frames.size() > 1:
                        var timer = Timer.new()
                        timer.wait_time = 1.0 / frame_rate
                        timer.autostart = true
                        var frame_idx: Array = [0]
                        timer.timeout.connect(func():
                            frame_idx[0] = (frame_idx[0] + 1) % all_frames.size()
                            tex_rect.texture = all_frames[frame_idx[0]]
                        )
                        item.add_child(timer)

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

func _on_name_submitted(new_text: String) -> void:
    if _ignoring_name_input:
        return
    _name_input.release_focus()
    display_name_changed.emit(new_text.strip_edges())

func _on_name_focus_lost() -> void:
    if _ignoring_name_input:
        return
    display_name_changed.emit(_name_input.text.strip_edges())

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
        _attachments_section.visible = false
        _catalog_scroll.visible = true  # Restore catalog when deselected
        return
    _selection_info.visible = true
    _asset_fields_section.visible = true
    _npc_fields_section.visible = false
    _delete_button.disabled = false
    _catalog_scroll.visible = false  # Hide catalog when object is selected
    var asset = Catalog.assets.get(asset_id, {})
    var name: String = asset.get("name", asset_id)
    _selection_label.text = name

    # Show attachments if this asset has slots
    _build_attachments(asset_id)

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

    # Populate display name input
    _ignoring_name_input = true
    _name_input.text = info.get("display_name", "")
    _ignoring_name_input = false

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

## Called by editor when an NPC is selected/deselected. Reuses the selection
## panel but swaps to NPC-only fields (no owner, no attachments, no delete
## until placement/delete ships in a follow-up).
func show_npc_selection(info: Dictionary) -> void:
    var npc_id: String = info.get("npc_id", "")
    if npc_id == "":
        _selection_info.visible = false
        _npc_fields_section.visible = false
        _catalog_scroll.visible = true
        return
    _selection_info.visible = true
    _asset_fields_section.visible = false
    _npc_fields_section.visible = true
    _delete_button.disabled = true  # NPC delete not in this milestone
    _catalog_scroll.visible = false

    var display_name: String = info.get("display_name", "")
    if display_name == "":
        display_name = "(unnamed)"
    _selection_label.text = display_name

    var behavior: String = info.get("behavior", "")
    if behavior == "":
        _npc_behavior_label.text = "Behavior: —"
    else:
        _npc_behavior_label.text = "Behavior: " + behavior

    var agent: String = info.get("llm_memory_agent", "")
    if agent == "":
        _npc_agent_label.text = "Agent: —"
    else:
        _npc_agent_label.text = "Agent: " + agent

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
        # Hide terrain picker, restore catalog only if nothing selected
        _terrain_picker.visible = false
        if not _selection_info.visible:
            _catalog_scroll.visible = true
        _set_tool_active(_select_button, true)
        # Deselect terrain item
        if _selected_terrain_item != null:
            var old_style: StyleBoxFlat = _selected_terrain_item.get_meta("style_normal")
            _selected_terrain_item.add_theme_stylebox_override("panel", old_style)
            _selected_terrain_item = null

    terrain_mode_toggled.emit(_terrain_active)

## Build the attachments grid for a selected object's slots.
func _build_attachments(asset_id: String) -> void:
    # Clear existing attachment items
    for child in _attachments_grid.get_children():
        child.queue_free()

    print("_build_attachments: asset_id=", asset_id)
    var slots: Array = Catalog.get_slots(asset_id)
    print("_build_attachments: slots=", slots)
    if slots.is_empty():
        _attachments_section.visible = false
        return

    # Collect all overlay assets that fit any of this object's slots
    var overlays: Array = []
    for slot in slots:
        var slot_name: String = slot.get("slot_name", "")
        var fitting: Array = Catalog.get_assets_for_slot(slot_name)
        print("_build_attachments: slot=", slot_name, " fitting=", fitting.size())
        for fit_asset in fitting:
            if not overlays.has(fit_asset):
                overlays.append(fit_asset)

    print("_build_attachments: total overlays=", overlays.size())
    if overlays.is_empty():
        _attachments_section.visible = false
        return

    _attachments_section.visible = true

    for overlay in overlays:
        var overlay_id: String = overlay.get("id", "")
        var overlay_name: String = overlay.get("name", overlay_id)

        var btn = Button.new()
        btn.custom_minimum_size = Vector2(CELL_SIZE, CELL_SIZE)
        btn.tooltip_text = overlay_name

        var btn_style = StyleBoxFlat.new()
        btn_style.bg_color = Color(0.15, 0.12, 0.08, 1.0)
        btn_style.border_width_left = 1
        btn_style.border_width_top = 1
        btn_style.border_width_right = 1
        btn_style.border_width_bottom = 1
        btn_style.border_color = Color(0.3, 0.24, 0.15, 0.5)
        btn_style.corner_radius_left_top = 2
        btn_style.corner_radius_right_top = 2
        btn_style.corner_radius_left_bottom = 2
        btn_style.corner_radius_right_bottom = 2
        btn_style.content_margin_left = 2.0
        btn_style.content_margin_right = 2.0
        btn_style.content_margin_top = 2.0
        btn_style.content_margin_bottom = 2.0
        btn.add_theme_stylebox_override("normal", btn_style)

        var hover_style = btn_style.duplicate()
        hover_style.bg_color = COLOR_ITEM_HOVER
        btn.add_theme_stylebox_override("hover", hover_style)

        var pressed_style = btn_style.duplicate()
        pressed_style.bg_color = COLOR_ITEM_SELECTED
        btn.add_theme_stylebox_override("pressed", pressed_style)

        var state_info = Catalog.get_state(overlay_id)
        if state_info != null:
            var texture = Catalog.get_sprite_texture(state_info)
            if texture != null:
                btn.icon = texture
                btn.icon_alignment = HORIZONTAL_ALIGNMENT_CENTER
                btn.expand_icon = true

        btn.pressed.connect(_on_attachment_clicked.bind(overlay_id))
        _attachments_grid.add_child(btn)

func _on_attachment_clicked(overlay_asset_id: String) -> void:
    attachment_requested.emit(overlay_asset_id)
    # Brief flash on the header to confirm attachment was placed
    var attach_label = _attachments_section.get_child(0)
    if attach_label is Label:
        attach_label.text = "ATTACHED!"
        attach_label.add_theme_color_override("font_color", Color(0.5, 0.8, 0.3, 1.0))
        var timer = get_tree().create_timer(1.0)
        timer.timeout.connect(func():
            attach_label.text = "ATTACHMENTS"
            attach_label.add_theme_color_override("font_color", COLOR_LABEL)
        )

## Called externally to exit terrain mode (e.g., when switching to select)
func exit_terrain_mode() -> void:
    if _terrain_active:
        _terrain_active = false
        _set_tool_active(_terrain_button, false)
        _terrain_picker.visible = false
        # Only restore catalog if nothing is selected (selection hides catalog)
        if not _selection_info.visible:
            _catalog_scroll.visible = true
        if _selected_terrain_item != null:
            var old_style: StyleBoxFlat = _selected_terrain_item.get_meta("style_normal")
            _selected_terrain_item.add_theme_stylebox_override("panel", old_style)
            _selected_terrain_item = null
