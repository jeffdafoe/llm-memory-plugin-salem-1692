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
signal npc_sprite_selected(sprite: Dictionary, sheet: Texture2D, npc_name: String)
signal npc_name_changed(display_name: String)
signal npc_behavior_changed(behavior: String)
signal npc_agent_changed(agent: String)
signal npc_home_structure_changed(structure_id: String)
signal npc_work_structure_changed(structure_id: String)
signal npc_home_assign_requested
signal npc_work_assign_requested
signal npc_run_cycle_requested
signal npc_go_home_requested
signal npc_go_to_work_requested
signal npc_select_requested(npc_id: String)
signal asset_enterable_toggled(asset_id: String, enterable: bool)

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
# "Can be entered" per-asset flag. Uses a Yes/No dropdown because the
# built-in CheckBox widget renders empty / vanishes under this project's
# theme, and the dropdown matches the visual language of the surrounding
# owner / behavior pickers.
var _enterable_dropdown: OptionButton = null
# The asset_id the enterable dropdown currently reflects, so we know what
# to PATCH on change. Set in show_selection.
var _enterable_asset_id: String = ""
var _ignoring_enterable_toggle: bool = false
# People section — lists NPCs whose home/work structure is the currently
# selected asset. See _populate_people_section.
var _people_section: VBoxContainer = null
var _people_list: VBoxContainer = null
var _attachments_grid: GridContainer = null

# Asset-specific fields (owner / name input / attachments) live under this
# VBox so the whole block can be hidden when an NPC is selected instead.
var _asset_fields_section: VBoxContainer = null

# NPC-specific fields (behavior, linked llm_memory_agent). Mutually exclusive
# with _asset_fields_section.
var _npc_fields_section: VBoxContainer = null
var _npc_name_edit: LineEdit = null
var _npc_behavior_dropdown: OptionButton = null
var _npc_agent_dropdown: OptionButton = null
# Home / Work are pickers now, not dropdowns — clicking _npc_home_pick_button
# puts the editor into ASSIGN_HOME mode so the user clicks a structure on the
# map. Clear button unlinks. Label shows the current structure name.
var _npc_home_pick_button: Button = null
var _npc_home_clear_button: Button = null
var _npc_work_pick_button: Button = null
var _npc_work_clear_button: Button = null
var _npc_run_cycle_button: Button = null
var _npc_go_home_button: Button = null
var _npc_go_to_work_button: Button = null
# Cached IDs so clear buttons know what's currently assigned (also drives
# whether the clear button is visible).
var _npc_home_current_id: String = ""
var _npc_work_current_id: String = ""
var _ignoring_npc_inputs: bool = false

# NPC placement catalog section — sits at the bottom of _catalog_container
# alongside the asset categories. Lets admins drop new villagers.
var _npc_catalog_section: VBoxContainer = null
var _npc_catalog_grid: GridContainer = null
var _npc_name_input: LineEdit = null
var _npc_sprites_loaded: bool = false

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

    # Enterable per-asset flag — Yes/No dropdown in the same visual style
    # as the owner / behavior pickers. Header label lives above.
    var enterable_header = Label.new()
    enterable_header.text = "CAN BE ENTERED"
    enterable_header.add_theme_color_override("font_color", COLOR_LABEL)
    enterable_header.add_theme_font_size_override("font_size", 11)
    _asset_fields_section.add_child(enterable_header)

    var enterable_style = StyleBoxFlat.new()
    enterable_style.bg_color = Color(0.08, 0.07, 0.05, 1.0)
    enterable_style.border_width_left = 1
    enterable_style.border_width_top = 1
    enterable_style.border_width_right = 1
    enterable_style.border_width_bottom = 1
    enterable_style.border_color = Color(0.3, 0.24, 0.15, 0.8)
    enterable_style.content_margin_left = 6.0
    enterable_style.content_margin_right = 6.0
    enterable_style.content_margin_top = 3.0
    enterable_style.content_margin_bottom = 3.0

    _enterable_dropdown = OptionButton.new()
    _enterable_dropdown.add_theme_font_override("font", _font)
    _enterable_dropdown.add_theme_font_size_override("font_size", 13)
    _enterable_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    _enterable_dropdown.add_theme_stylebox_override("normal", enterable_style)
    _enterable_dropdown.add_item("No", 0)
    _enterable_dropdown.add_item("Yes", 1)
    _enterable_dropdown.item_selected.connect(_on_enterable_selected)
    _asset_fields_section.add_child(_enterable_dropdown)

    # People section — NPCs whose home or work is this structure. Clicking a
    # row selects that villager (and pans the camera to them). Only populated
    # when the selected object is a structure; hidden otherwise. Populated
    # in show_selection from world.placed_npcs.
    _people_section = VBoxContainer.new()
    _people_section.visible = false
    _people_section.add_theme_constant_override("separation", 4)
    _asset_fields_section.add_child(_people_section)

    var people_header = Label.new()
    people_header.text = "PEOPLE"
    people_header.add_theme_color_override("font_color", COLOR_LABEL)
    people_header.add_theme_font_size_override("font_size", 11)
    _people_section.add_child(people_header)

    _people_list = VBoxContainer.new()
    _people_list.add_theme_constant_override("separation", 2)
    _people_section.add_child(_people_list)

    # NPC-only block — shown in place of _asset_fields_section when an NPC is
    # selected. Editable name, behavior picker, and agent picker. Each field
    # commits independently (Enter/focus_exit on name, item_selected on dropdowns).
    _npc_fields_section = VBoxContainer.new()
    _npc_fields_section.visible = false
    _npc_fields_section.add_theme_constant_override("separation", 4)
    _selection_info.add_child(_npc_fields_section)

    var npc_name_header = Label.new()
    npc_name_header.text = "NAME"
    npc_name_header.add_theme_color_override("font_color", COLOR_LABEL)
    npc_name_header.add_theme_font_size_override("font_size", 11)
    _npc_fields_section.add_child(npc_name_header)

    _npc_name_edit = LineEdit.new()
    _npc_name_edit.add_theme_font_override("font", _font)
    _npc_name_edit.add_theme_font_size_override("font_size", 13)
    _npc_name_edit.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_name_edit.placeholder_text = "Villager"
    var npc_name_style = StyleBoxFlat.new()
    npc_name_style.bg_color = Color(0.08, 0.07, 0.05, 1.0)
    npc_name_style.border_width_left = 1
    npc_name_style.border_width_top = 1
    npc_name_style.border_width_right = 1
    npc_name_style.border_width_bottom = 1
    npc_name_style.border_color = Color(0.3, 0.24, 0.15, 0.8)
    npc_name_style.content_margin_left = 6.0
    npc_name_style.content_margin_right = 6.0
    npc_name_style.content_margin_top = 4.0
    npc_name_style.content_margin_bottom = 4.0
    _npc_name_edit.add_theme_stylebox_override("normal", npc_name_style)
    _npc_name_edit.text_submitted.connect(_on_npc_name_submitted)
    _npc_name_edit.focus_exited.connect(_on_npc_name_focus_lost)
    _npc_fields_section.add_child(_npc_name_edit)

    var npc_behavior_header = Label.new()
    npc_behavior_header.text = "BEHAVIOR"
    npc_behavior_header.add_theme_color_override("font_color", COLOR_LABEL)
    npc_behavior_header.add_theme_font_size_override("font_size", 11)
    _npc_fields_section.add_child(npc_behavior_header)

    _npc_behavior_dropdown = OptionButton.new()
    _npc_behavior_dropdown.add_theme_font_override("font", _font)
    _npc_behavior_dropdown.add_theme_font_size_override("font_size", 13)
    _npc_behavior_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    var behavior_style = StyleBoxFlat.new()
    behavior_style.bg_color = Color(0.08, 0.07, 0.05, 1.0)
    behavior_style.border_width_left = 1
    behavior_style.border_width_top = 1
    behavior_style.border_width_right = 1
    behavior_style.border_width_bottom = 1
    behavior_style.border_color = Color(0.3, 0.24, 0.15, 0.8)
    behavior_style.content_margin_left = 6.0
    behavior_style.content_margin_right = 6.0
    behavior_style.content_margin_top = 4.0
    behavior_style.content_margin_bottom = 4.0
    _npc_behavior_dropdown.add_theme_stylebox_override("normal", behavior_style)
    _npc_behavior_dropdown.item_selected.connect(_on_npc_behavior_selected)
    _npc_fields_section.add_child(_npc_behavior_dropdown)

    var npc_agent_header = Label.new()
    npc_agent_header.text = "AGENT"
    npc_agent_header.add_theme_color_override("font_color", COLOR_LABEL)
    npc_agent_header.add_theme_font_size_override("font_size", 11)
    _npc_fields_section.add_child(npc_agent_header)

    _npc_agent_dropdown = OptionButton.new()
    _npc_agent_dropdown.add_theme_font_override("font", _font)
    _npc_agent_dropdown.add_theme_font_size_override("font_size", 13)
    _npc_agent_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    # Same stylebox as behavior dropdown (shared visual language).
    _npc_agent_dropdown.add_theme_stylebox_override("normal", behavior_style)
    _npc_agent_dropdown.item_selected.connect(_on_npc_agent_selected)
    _npc_fields_section.add_child(_npc_agent_dropdown)

    var npc_home_header = Label.new()
    npc_home_header.text = "HOME"
    npc_home_header.add_theme_color_override("font_color", COLOR_LABEL)
    npc_home_header.add_theme_font_size_override("font_size", 11)
    _npc_fields_section.add_child(npc_home_header)

    var home_row := HBoxContainer.new()
    home_row.add_theme_constant_override("separation", 4)
    _npc_fields_section.add_child(home_row)

    _npc_home_pick_button = Button.new()
    _npc_home_pick_button.add_theme_font_override("font", _font)
    _npc_home_pick_button.add_theme_font_size_override("font_size", 13)
    _npc_home_pick_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_home_pick_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_home_pick_button.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _npc_home_pick_button.alignment = HORIZONTAL_ALIGNMENT_LEFT
    _npc_home_pick_button.pressed.connect(func(): npc_home_assign_requested.emit())
    home_row.add_child(_npc_home_pick_button)

    _npc_home_clear_button = Button.new()
    _npc_home_clear_button.text = "×"
    _npc_home_clear_button.add_theme_font_override("font", _font)
    _npc_home_clear_button.add_theme_font_size_override("font_size", 13)
    _npc_home_clear_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_home_clear_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_home_clear_button.custom_minimum_size = Vector2(24, 0)
    _npc_home_clear_button.pressed.connect(func(): npc_home_structure_changed.emit(""))
    home_row.add_child(_npc_home_clear_button)

    var npc_work_header = Label.new()
    npc_work_header.text = "WORK"
    npc_work_header.add_theme_color_override("font_color", COLOR_LABEL)
    npc_work_header.add_theme_font_size_override("font_size", 11)
    _npc_fields_section.add_child(npc_work_header)

    var work_row := HBoxContainer.new()
    work_row.add_theme_constant_override("separation", 4)
    _npc_fields_section.add_child(work_row)

    _npc_work_pick_button = Button.new()
    _npc_work_pick_button.add_theme_font_override("font", _font)
    _npc_work_pick_button.add_theme_font_size_override("font_size", 13)
    _npc_work_pick_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_work_pick_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_work_pick_button.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _npc_work_pick_button.alignment = HORIZONTAL_ALIGNMENT_LEFT
    _npc_work_pick_button.pressed.connect(func(): npc_work_assign_requested.emit())
    work_row.add_child(_npc_work_pick_button)

    _npc_work_clear_button = Button.new()
    _npc_work_clear_button.text = "×"
    _npc_work_clear_button.add_theme_font_override("font", _font)
    _npc_work_clear_button.add_theme_font_size_override("font_size", 13)
    _npc_work_clear_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_work_clear_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_work_clear_button.custom_minimum_size = Vector2(24, 0)
    _npc_work_clear_button.pressed.connect(func(): npc_work_structure_changed.emit(""))
    work_row.add_child(_npc_work_clear_button)

    # Go Home / Go to Work — direct "walk to that building's door and go
    # inside" commands, independent of any behavior. Disabled when the
    # respective structure isn't linked.
    _npc_go_home_button = Button.new()
    _npc_go_home_button.text = "Go Home"
    _npc_go_home_button.add_theme_font_override("font", _font)
    _npc_go_home_button.add_theme_font_size_override("font_size", 13)
    _npc_go_home_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_go_home_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_go_home_button.pressed.connect(func(): npc_go_home_requested.emit())
    _npc_fields_section.add_child(_npc_go_home_button)

    _npc_go_to_work_button = Button.new()
    _npc_go_to_work_button.text = "Go to Work"
    _npc_go_to_work_button.add_theme_font_override("font", _font)
    _npc_go_to_work_button.add_theme_font_size_override("font_size", 13)
    _npc_go_to_work_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_go_to_work_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_go_to_work_button.pressed.connect(func(): npc_go_to_work_requested.emit())
    _npc_fields_section.add_child(_npc_go_to_work_button)

    # Trigger the NPC's behavior cycle on demand — lamplighter rounds,
    # laundry rotation, etc. Enabled only when a behavior is assigned.
    _npc_run_cycle_button = Button.new()
    _npc_run_cycle_button.text = "Run Cycle"
    _npc_run_cycle_button.add_theme_font_override("font", _font)
    _npc_run_cycle_button.add_theme_font_size_override("font_size", 13)
    _npc_run_cycle_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_run_cycle_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_run_cycle_button.pressed.connect(func(): npc_run_cycle_requested.emit())
    _npc_fields_section.add_child(_npc_run_cycle_button)

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
    _npc_catalog_section = null
    _npc_catalog_grid = null
    _npc_name_input = null

    # Sort categories for consistent ordering
    var cat_names: Array = Catalog.categories.keys()
    cat_names.sort()

    for cat_name in cat_names:
        var assets: Array = Catalog.categories[cat_name]
        _add_category_section(cat_name, assets)

    # NPCs catalog — always appears at the bottom. Populated asynchronously
    # from GET /api/village/npc-sprites once thumbnails' sheets download.
    _build_npc_catalog_section()
    _load_npc_sprites()

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

## Append an NPC placement section below the asset categories. Name input
## at the top, sprite thumbnail grid below. Thumbnails are populated once
## sheets download (see _load_npc_sprites).
func _build_npc_catalog_section() -> void:
    _npc_catalog_section = VBoxContainer.new()
    _npc_catalog_section.add_theme_constant_override("separation", 4)
    _catalog_container.add_child(_npc_catalog_section)

    var header = Button.new()
    header.text = "VILLAGERS"
    header.flat = true
    header.add_theme_color_override("font_color", COLOR_LABEL)
    header.add_theme_font_size_override("font_size", 11)
    header.alignment = HORIZONTAL_ALIGNMENT_LEFT
    _npc_catalog_section.add_child(header)

    # Wrap name input + grid so they share the header's collapse state.
    var body = VBoxContainer.new()
    body.add_theme_constant_override("separation", 4)
    body.visible = false
    _npc_catalog_section.add_child(body)
    header.pressed.connect(func(): body.visible = not body.visible)

    var name_label = Label.new()
    name_label.text = "Name (applied on drop)"
    name_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    name_label.add_theme_font_size_override("font_size", 11)
    body.add_child(name_label)

    _npc_name_input = LineEdit.new()
    _npc_name_input.placeholder_text = "e.g. Goody Smith"
    _npc_name_input.add_theme_font_override("font", _font)
    _npc_name_input.add_theme_font_size_override("font_size", 13)
    _npc_name_input.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_name_input.add_theme_color_override("font_placeholder_color", Color(0.45, 0.40, 0.30, 1.0))
    _npc_name_input.max_length = 100
    var input_style = StyleBoxFlat.new()
    input_style.bg_color = COLOR_BTN_BG
    input_style.border_width_left = 1
    input_style.border_width_top = 1
    input_style.border_width_right = 1
    input_style.border_width_bottom = 1
    input_style.border_color = COLOR_BTN_BORDER
    input_style.corner_radius_left_top = 3
    input_style.corner_radius_right_top = 3
    input_style.corner_radius_left_bottom = 3
    input_style.corner_radius_right_bottom = 3
    input_style.content_margin_left = 6.0
    input_style.content_margin_right = 6.0
    input_style.content_margin_top = 4.0
    input_style.content_margin_bottom = 4.0
    _npc_name_input.add_theme_stylebox_override("normal", input_style)
    _npc_name_input.add_theme_stylebox_override("focus", input_style)
    body.add_child(_npc_name_input)

    _npc_catalog_grid = GridContainer.new()
    _npc_catalog_grid.columns = 4
    _npc_catalog_grid.add_theme_constant_override("h_separation", 4)
    _npc_catalog_grid.add_theme_constant_override("v_separation", 4)
    body.add_child(_npc_catalog_grid)

## Fetch the NPC sprite catalog from the server, then ask world.gd to load
## each sheet (using its shared cache). Once a sheet arrives, build that
## thumbnail. Thumbnails appear as sheets download — parallels the async
## asset catalog render.
func _load_npc_sprites() -> void:
    if _npc_sprites_loaded:
        return
    _npc_sprites_loaded = true
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_npc_sprites_loaded.bind(http))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request(Auth.api_base + "/api/village/npc-sprites", headers)

func _on_npc_sprites_loaded(result: int, code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or code != 200:
        push_warning("NPC sprites load failed: " + str(code))
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null or not (json is Array):
        return
    if _npc_catalog_grid == null:
        return  # panel was torn down before response arrived
    if world == null:
        return
    for sprite in json:
        var sheet_path: String = sprite.get("sheet", "")
        if sheet_path == "":
            continue
        # Defer thumbnail creation until the sheet texture is available.
        # world.get_or_load_npc_sheet handles the cache hit/miss; callback
        # fires either synchronously (cache hit) or after the download.
        world.get_or_load_npc_sheet(sheet_path, func(tex: Texture2D):
            _add_npc_catalog_item(sprite, tex)
        )

## Build one NPC sprite thumbnail. Click selects this sprite for placement
## — main.gd routes the signal to editor.select_npc_sprite_for_placement.
func _add_npc_catalog_item(sprite: Dictionary, sheet: Texture2D) -> void:
    if _npc_catalog_grid == null or sheet == null:
        return
    var fw: int = int(sprite.get("frame_width", 32))
    var fh: int = int(sprite.get("frame_height", 32))
    var sprite_name: String = sprite.get("name", "villager")

    var item = PanelContainer.new()
    item.custom_minimum_size = Vector2(CELL_SIZE, CELL_SIZE)
    item.tooltip_text = sprite_name

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
    item.set_meta("style_normal", item_style)

    var center = CenterContainer.new()
    center.custom_minimum_size = Vector2(CELL_SIZE - 4, CELL_SIZE - 4)
    item.add_child(center)

    var atlas := AtlasTexture.new()
    atlas.atlas = sheet
    # Frame 0 of row 0 = south-facing idle (Mana Seed NPC pack convention).
    atlas.region = Rect2(0, 0, fw, fh)

    var tex_rect = TextureRect.new()
    tex_rect.texture = atlas
    var native_size: Vector2 = Vector2(fw, fh)
    var max_dim: float = CELL_SIZE - 8.0
    var scale_factor: float = minf(max_dim / native_size.x, max_dim / native_size.y)
    if scale_factor > 2.0:
        scale_factor = 2.0
    tex_rect.custom_minimum_size = native_size * scale_factor
    tex_rect.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
    tex_rect.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
    center.add_child(tex_rect)

    item.mouse_entered.connect(_on_item_hover.bind(item, true))
    item.mouse_exited.connect(_on_item_hover.bind(item, false))
    item.gui_input.connect(func(event: InputEvent):
        if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
            _on_npc_catalog_item_selected(item, sprite, sheet)
    )

    _npc_catalog_grid.add_child(item)

func _on_npc_catalog_item_selected(item: Control, sprite: Dictionary, sheet: Texture2D) -> void:
    # Visual highlight only — don't route through _select_catalog_item because
    # that emits asset_selected("") which would kick the editor back into
    # SELECT mode and wipe the placement we're about to enter.
    if _selected_item != null:
        var old_style: StyleBoxFlat = _selected_item.get_meta("style_normal")
        _selected_item.add_theme_stylebox_override("panel", old_style)
    _selected_item = item
    _selected_asset_id = ""
    var selected_style: StyleBoxFlat = item.get_meta("style_normal").duplicate()
    selected_style.bg_color = COLOR_ITEM_SELECTED
    selected_style.border_color = COLOR_ITEM_BORDER
    item.add_theme_stylebox_override("panel", selected_style)
    _set_tool_active(_select_button, false)

    var npc_name: String = ""
    if _npc_name_input != null:
        npc_name = _npc_name_input.text.strip_edges()
    npc_sprite_selected.emit(sprite, sheet, npc_name)

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

func _on_npc_name_submitted(new_text: String) -> void:
    if _ignoring_npc_inputs:
        return
    _npc_name_edit.release_focus()
    var trimmed: String = new_text.strip_edges()
    if trimmed != "":
        npc_name_changed.emit(trimmed)

func _on_npc_name_focus_lost() -> void:
    if _ignoring_npc_inputs:
        return
    var trimmed: String = _npc_name_edit.text.strip_edges()
    if trimmed != "":
        npc_name_changed.emit(trimmed)

func _on_npc_behavior_selected(index: int) -> void:
    if _ignoring_npc_inputs:
        return
    var slug: String = _npc_behavior_dropdown.get_item_metadata(index)
    npc_behavior_changed.emit(slug)

func _on_npc_agent_selected(index: int) -> void:
    if _ignoring_npc_inputs:
        return
    var agent_key: String = _npc_agent_dropdown.get_item_metadata(index)
    npc_agent_changed.emit(agent_key)

## Update the Home / Work picker button labels while in structure-picking
## mode. Called by main when editor enters/exits ASSIGN_HOME / ASSIGN_WORK.
## While picking, the row is frozen on "Click a structure (Esc)" to cue the
## user to look at the map rather than the panel.
func set_assigning_home(active: bool) -> void:
    if _npc_home_pick_button == null:
        return
    if active:
        _npc_home_pick_button.text = "Click a structure (Esc)"
        _npc_home_pick_button.disabled = true
        _npc_home_clear_button.disabled = true
    else:
        _npc_home_pick_button.disabled = false
        _npc_home_clear_button.disabled = false
        _refresh_home_button_label()

func set_assigning_work(active: bool) -> void:
    if _npc_work_pick_button == null:
        return
    if active:
        _npc_work_pick_button.text = "Click a structure (Esc)"
        _npc_work_pick_button.disabled = true
        _npc_work_clear_button.disabled = true
    else:
        _npc_work_pick_button.disabled = false
        _npc_work_clear_button.disabled = false
        _refresh_work_button_label()

func _refresh_home_button_label() -> void:
    _npc_home_pick_button.text = _label_for_structure(_npc_home_current_id)
    _npc_home_clear_button.visible = _npc_home_current_id != ""

func _refresh_work_button_label() -> void:
    _npc_work_pick_button.text = _label_for_structure(_npc_work_current_id)
    _npc_work_clear_button.visible = _npc_work_current_id != ""

func _label_for_structure(structure_id: String) -> String:
    if structure_id == "":
        return "(none — click to set)"
    if world == null:
        return structure_id
    for s in world.get_structure_objects():
        if s.get("id", "") == structure_id:
            return s.get("label", structure_id)
    # Structure got deleted or hasn't rendered yet — show the id as a
    # fallback so the user isn't staring at an empty button.
    return structure_id


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

func _on_enterable_selected(index: int) -> void:
    if _ignoring_enterable_toggle or _enterable_asset_id == "":
        return
    asset_enterable_toggled.emit(_enterable_asset_id, index == 1)

## Fill the People section with buttons for each NPC whose home or work is
## the given structure. Hides the section if the selection isn't a
## structure or if nobody lives/works there.
func _populate_people_section(object_id: String, asset_id: String) -> void:
    for child in _people_list.get_children():
        child.queue_free()
    if object_id == "" or world == null:
        _people_section.visible = false
        return
    var asset = Catalog.assets.get(asset_id, {})
    if not bool(asset.get("enterable", false)):
        _people_section.visible = false
        return

    var any: bool = false
    for npc_id in world.placed_npcs:
        var container: Node2D = world.placed_npcs[npc_id]
        if container == null:
            continue
        var home_id: String = str(container.get_meta("home_structure_id", ""))
        var work_id: String = str(container.get_meta("work_structure_id", ""))
        var is_home: bool = home_id == object_id
        var is_work: bool = work_id == object_id
        if not is_home and not is_work:
            continue
        any = true
        var label: String = str(container.get_meta("display_name", ""))
        if label == "":
            label = "(unnamed)"
        var tag: String = ""
        if is_home and is_work:
            tag = "[H+W]"
        elif is_home:
            tag = "[H]"
        else:
            tag = "[W]"
        var row := Button.new()
        row.text = label + "  " + tag
        row.add_theme_font_override("font", _font)
        row.add_theme_font_size_override("font_size", 13)
        row.add_theme_color_override("font_color", COLOR_TEXT)
        row.alignment = HORIZONTAL_ALIGNMENT_LEFT
        var current_id: String = str(npc_id)
        row.pressed.connect(func(): npc_select_requested.emit(current_id))
        _people_list.add_child(row)

    _people_section.visible = any

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

    # Sync the enterable dropdown to this asset's current value.
    _ignoring_enterable_toggle = true
    _enterable_asset_id = asset_id
    _enterable_dropdown.selected = 1 if bool(asset.get("enterable", false)) else 0
    _ignoring_enterable_toggle = false

    # People list — structures only. Shown when someone's home or work is
    # this specific object_id.
    _populate_people_section(info.get("object_id", ""), asset_id)

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
    _delete_button.disabled = false
    _catalog_scroll.visible = false

    var display_name: String = info.get("display_name", "")
    _selection_label.text = display_name if display_name != "" else "(unnamed)"

    _ignoring_npc_inputs = true

    # Drop focus on the name edit before replacing its text — otherwise a
    # focus_exited fires after we've already swapped to a different NPC and
    # emits the old name against the new selection.
    _npc_name_edit.release_focus()
    _npc_name_edit.text = display_name

    # Populate behavior dropdown from Catalog.npc_behaviors. Index 0 is always
    # "(none)" mapped to empty string — represents a null behavior server-side.
    _npc_behavior_dropdown.clear()
    _npc_behavior_dropdown.add_item("(none)", 0)
    _npc_behavior_dropdown.set_item_metadata(0, "")
    var selected_behavior_index: int = 0
    var current_behavior: String = info.get("behavior", "")
    var bi: int = 1
    for b in Catalog.npc_behaviors:
        _npc_behavior_dropdown.add_item(b.get("display_name", b.get("slug", "")), bi)
        _npc_behavior_dropdown.set_item_metadata(bi, b.get("slug", ""))
        if b.get("slug", "") == current_behavior:
            selected_behavior_index = bi
        bi += 1
    _npc_behavior_dropdown.selected = selected_behavior_index

    # Agent dropdown reuses the same village_agent list that powers the owner
    # dropdown on assets. Index 0 is "(none)" → unlink.
    _npc_agent_dropdown.clear()
    _npc_agent_dropdown.add_item("(none)", 0)
    _npc_agent_dropdown.set_item_metadata(0, "")
    var selected_agent_index: int = 0
    var current_agent: String = info.get("llm_memory_agent", "")
    if world != null:
        var ai: int = 1
        for agent_key in world.agent_list:
            var display: String = world.agent_names.get(agent_key, agent_key)
            _npc_agent_dropdown.add_item(display, ai)
            _npc_agent_dropdown.set_item_metadata(ai, agent_key)
            if agent_key == current_agent:
                selected_agent_index = ai
            ai += 1
    _npc_agent_dropdown.selected = selected_agent_index

    # Home / Work are click-to-assign now. The picker button shows the
    # current structure's display name (or an inviting placeholder); the
    # clear × button is only visible when something is actually set.
    _npc_home_current_id = info.get("home_structure_id", "")
    _npc_work_current_id = info.get("work_structure_id", "")
    _refresh_home_button_label()
    _refresh_work_button_label()

    # Run Cycle is only meaningful when a behavior is assigned.
    if _npc_run_cycle_button != null:
        _npc_run_cycle_button.disabled = current_behavior == ""
    if _npc_go_home_button != null:
        _npc_go_home_button.disabled = _npc_home_current_id == ""
    if _npc_go_to_work_button != null:
        _npc_go_to_work_button.disabled = _npc_work_current_id == ""

    _ignoring_npc_inputs = false

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
