extends PanelContainer
## Editor side panel — 240px left sidebar with tool buttons, selection info,
## and an asset catalog grouped by category. Shows when Edit mode is active.

signal asset_selected(asset_id: String)
signal asset_inspect_requested(asset_id: String)
signal delete_requested
signal terrain_mode_toggled(active: bool)
signal terrain_type_selected(terrain_type: int)
signal owner_changed(owner: String)
signal display_name_changed(display_name: String, object_id: String)
signal attachment_requested(overlay_asset_id: String)
signal npc_sprite_selected(sprite: Dictionary, sheet: Texture2D, npc_name: String)
signal npc_name_changed(display_name: String)
signal npc_behavior_changed(behavior: String)
signal npc_agent_changed(agent: String)
signal npc_home_structure_changed(structure_id: String)
signal npc_work_structure_changed(structure_id: String)
# Combined schedule change — all six fields sent together.
#   start_min / end_min: absolute work-window minutes-of-day (0–1439). Both
#     -1 means "inherit dawn/dusk" (sent to server as null/null). All-or-none
#     mirrors the schedule_window_all_or_none DB CHECK.
#   interval / start / end: rotation cadence triple. -1 means "no cadence".
#   lateness: lateness_window_minutes (0–180). Always sent.
signal npc_schedule_changed(start_min: int, end_min: int, interval: int, start: int, end: int, lateness: int)
# Social-hour overlay (ZBBS-068, minute-precision since ZBBS-071). Empty tag
# == "clear the schedule" (and start_min/end_min are ignored in that case).
# Applied all-or-none server-side.
signal npc_social_changed(tag: String, start_min: int, end_min: int)
signal npc_home_assign_requested
signal npc_work_assign_requested
signal npc_run_cycle_requested
signal npc_go_home_requested
signal npc_go_to_work_requested
signal npc_sprite_change_requested(npc_id: String, current_sprite_id: String)
signal npc_select_requested(npc_id: String)
signal asset_enterable_toggled(asset_id: String, enterable: bool)
signal asset_visible_when_inside_toggled(asset_id: String, visible: bool)

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
# ScrollContainer wrapping _selection_info. Show/hide this instead of
# _selection_info itself so the scroll viewport actually claims space.
var _selection_info_scroll: ScrollContainer = null
var _selection_label: Label = null
var _placed_by_label: Label = null
var _owner_label: Label = null
var _owner_dropdown: OptionButton = null
var _ignoring_dropdown: bool = false
var _name_input: LineEdit = null
var _ignoring_name_input: bool = false
# Remember which object the name input currently reflects so a later
# focus_exited can save to THAT id — even if the editor's selection has
# already cleared (deselection fires focus_exited as a side effect of
# hiding the panel).
var _name_input_object_id: String = ""
var _catalog_container: VBoxContainer = null
var _terrain_picker: VBoxContainer = null
var _catalog_scroll: ScrollContainer = null
# View tabs — "Catalog" (placement palette) and "Villagers" (browser of
# placed NPCs). Sit between the tool buttons and the scrollable content
# region. Only one of _catalog_scroll / _villagers_scroll is visible at
# a time. The _active_view string drives which one is restored when the
# selection panel or terrain picker releases the content region.
var _view_tabs_row: HBoxContainer = null
var _catalog_tab_button: Button = null
var _villagers_tab_button: Button = null
var _villagers_scroll: ScrollContainer = null
var _villagers_list: VBoxContainer = null
var _villagers_filter_input: LineEdit = null
var _active_view: String = "catalog"
# npc_id → row PanelContainer, so selection sync can scroll to and
# highlight the currently-selected villager without rebuilding.
var _villager_rows: Dictionary = {}
# The npc_id currently highlighted in the Villagers list (empty = none).
var _villagers_selected_id: String = ""
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
var _visible_when_inside_dropdown: OptionButton = null
# The asset_id the two above dropdowns currently reflect, so we know what
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
# SPRITE row — read-only label of current sprite name + Change… button that
# opens the modal picker. NPC's current sprite_id is stashed for the picker.
var _npc_sprite_label: Label = null
var _npc_sprite_change_button: Button = null
var _npc_current_sprite_id: String = ""
var _npc_current_id: String = ""
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
# Schedule section — absolute work-window pair (HH:MM start / HH:MM end)
# + lateness + cadence triple. The start/end pair is nullable: when both
# server values are NULL the worker inherits the global dawn/dusk window,
# and the spinners display those defaults so the admin can see what the
# NPC is actually doing. _npc_schedule_window_is_null tracks whether the
# spinner values came from prepopulation (NULL state) or real overrides;
# the first user-driven value_changed flips the flag and starts emitting
# concrete minutes to the server.
var _npc_schedule_section: VBoxContainer = null
var _npc_start_hour_spin: SpinBox = null
var _npc_start_minute_spin: SpinBox = null
var _npc_end_hour_spin: SpinBox = null
var _npc_end_minute_spin: SpinBox = null
var _npc_schedule_window_is_null: bool = true
var _npc_lateness_spin: SpinBox = null
var _npc_cadence_check: CheckBox = null
var _npc_interval_spin: SpinBox = null
var _npc_start_spin: SpinBox = null
var _npc_end_spin: SpinBox = null
var _npc_schedule_save_button: Button = null
var _npc_cadence_row: HBoxContainer = null
var _npc_cadence_row2: HBoxContainer = null
# Social-hour overlay UI (ZBBS-068). Like cadence, it's gated by a checkbox
# so the panel can express "no social schedule" distinct from "scheduled at
# minute 0." Tag dropdown is populated from GET /api/assets/state-tags.
# HH:MM precision since ZBBS-071.
var _npc_social_check: CheckBox = null
var _npc_social_tag_dropdown: OptionButton = null
var _npc_social_start_hour_spin: SpinBox = null
var _npc_social_start_minute_spin: SpinBox = null
var _npc_social_end_hour_spin: SpinBox = null
var _npc_social_end_minute_spin: SpinBox = null
var _npc_social_row: HBoxContainer = null
var _npc_social_tag_row: HBoxContainer = null

# Per-instance tag editor for placed objects (ZBBS-069). Chips rendered
# into _obj_tags_chips_box as removable buttons; the add dropdown shows
# allowlist minus already-applied tags.
var _obj_tags_chips_box: HBoxContainer = null
var _obj_tags_add_dropdown: OptionButton = null
var _obj_tags_current_id: String = ""
var _obj_tags_current_list: Array = []
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

    # View tabs — Catalog (placement palette) / Villagers (browse placed
    # NPCs). Hidden whenever a selection or the terrain picker takes over
    # the content region, restored alongside whatever tab was active.
    _view_tabs_row = HBoxContainer.new()
    _view_tabs_row.add_theme_constant_override("separation", 2)
    vbox.add_child(_view_tabs_row)

    _catalog_tab_button = _make_view_tab_button("Catalog")
    _catalog_tab_button.pressed.connect(_on_catalog_tab_pressed)
    _view_tabs_row.add_child(_catalog_tab_button)

    _villagers_tab_button = _make_view_tab_button("Villagers")
    _villagers_tab_button.pressed.connect(_on_villagers_tab_pressed)
    _view_tabs_row.add_child(_villagers_tab_button)

    _set_view_tab_active(_catalog_tab_button, true)
    _set_view_tab_active(_villagers_tab_button, false)

    # Selection info (hidden when nothing selected). Wrapped in a
    # ScrollContainer so long selections (NPC with all schedule + social
    # fields expanded, or an asset with many attachments) don't push the
    # bottom of the panel off-screen. The scroll container takes the
    # expand-fill slot so it grows with the panel; the inner VBox holds
    # natural height.
    var _selection_scroll = ScrollContainer.new()
    _selection_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    _selection_scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _selection_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    _selection_scroll.visible = false
    vbox.add_child(_selection_scroll)

    _selection_info = VBoxContainer.new()
    _selection_info.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _selection_info.add_theme_constant_override("separation", 4)
    _selection_scroll.add_child(_selection_info)

    # _selection_info's own `visible` flag drives whether the scroll
    # container above shows. Route show/hide through this setter-like
    # pair of calls so existing call sites (show_selection, etc.) keep
    # working without knowing the container changed.
    _selection_info_scroll = _selection_scroll

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

    # Per-instance tag editor (ZBBS-069). Applies to THIS placed object,
    # not to the asset template. Populated by _populate_object_tags in
    # show_selection. Allowlist comes from Catalog.object_tags, updates
    # via POST/DELETE on /api/village/objects/{id}/tags.
    var obj_tags_header = Label.new()
    obj_tags_header.text = "TAGS"
    obj_tags_header.add_theme_color_override("font_color", COLOR_LABEL)
    obj_tags_header.add_theme_font_size_override("font_size", 11)
    _asset_fields_section.add_child(obj_tags_header)

    _obj_tags_chips_box = HBoxContainer.new()
    _obj_tags_chips_box.add_theme_constant_override("separation", 4)
    _asset_fields_section.add_child(_obj_tags_chips_box)

    # Add-tag row. Dropdown + button use the same styles the rest of the
    # selection panel does (dropdown_style for OptionButtons, behavior_style
    # for action buttons — see shared/notes/codebase/salem-editor-ui-styles).
    var obj_tags_add_row = HBoxContainer.new()
    obj_tags_add_row.add_theme_constant_override("separation", 6)
    _asset_fields_section.add_child(obj_tags_add_row)
    _obj_tags_add_dropdown = OptionButton.new()
    _obj_tags_add_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _obj_tags_add_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    _obj_tags_add_dropdown.add_theme_font_override("font", _font)
    _obj_tags_add_dropdown.add_theme_font_size_override("font_size", 12)
    _obj_tags_add_dropdown.add_theme_stylebox_override("normal", dropdown_style)
    obj_tags_add_row.add_child(_obj_tags_add_dropdown)
    var obj_tags_add_btn = Button.new()
    obj_tags_add_btn.text = "Add tag"
    obj_tags_add_btn.add_theme_color_override("font_color", COLOR_TEXT)
    obj_tags_add_btn.add_theme_font_override("font", _font)
    obj_tags_add_btn.add_theme_font_size_override("font_size", 12)
    obj_tags_add_btn.add_theme_stylebox_override("normal", dropdown_style)
    obj_tags_add_btn.pressed.connect(_on_obj_tag_add_pressed)
    obj_tags_add_row.add_child(obj_tags_add_btn)

    # Marker legend — explains the colored handles that appear on the
    # selected placement in the world. Admins won't immediately know
    # what blue square / orange square / green or gold circle mean, and
    # mouse-hover tooltips on world-space markers would require Area2D
    # plumbing per marker. Static legend in the panel is simpler and
    # always-visible while a placement is selected.
    var markers_header = Label.new()
    markers_header.text = "MARKERS"
    markers_header.add_theme_color_override("font_color", COLOR_LABEL)
    markers_header.add_theme_font_size_override("font_size", 11)
    _asset_fields_section.add_child(markers_header)

    _add_marker_legend_row(Color(0.25, 0.55, 1.0, 0.9), false,
        "Door — entry tile NPCs walk to")
    _add_marker_legend_row(Color(1.0, 0.65, 0.20, 0.9), false,
        "Stand — interior render position")
    _add_marker_legend_row(Color(0.30, 0.85, 0.45, 0.9), true,
        "Loiter — center of the visitor slot ring (NPCs stand around it, not on it)")

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

    # Match the owner dropdown's stylebox exactly — warmer brown bg,
    # rounded corners, slightly larger padding — so the two pickers read
    # as part of the same family.
    var enterable_style = StyleBoxFlat.new()
    enterable_style.bg_color = COLOR_BTN_BG
    enterable_style.border_width_left = 1
    enterable_style.border_width_top = 1
    enterable_style.border_width_right = 1
    enterable_style.border_width_bottom = 1
    enterable_style.border_color = COLOR_BTN_BORDER
    enterable_style.corner_radius_left_top = 3
    enterable_style.corner_radius_right_top = 3
    enterable_style.corner_radius_left_bottom = 3
    enterable_style.corner_radius_right_bottom = 3
    enterable_style.content_margin_left = 6.0
    enterable_style.content_margin_right = 6.0
    enterable_style.content_margin_top = 4.0
    enterable_style.content_margin_bottom = 4.0

    _enterable_dropdown = OptionButton.new()
    _enterable_dropdown.add_theme_font_override("font", _font)
    _enterable_dropdown.add_theme_font_size_override("font_size", 13)
    _enterable_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    _enterable_dropdown.add_theme_stylebox_override("normal", enterable_style)
    _enterable_dropdown.add_item("No", 0)
    _enterable_dropdown.add_item("Yes", 1)
    _enterable_dropdown.item_selected.connect(_on_enterable_selected)
    _asset_fields_section.add_child(_enterable_dropdown)

    # Visible-when-inside — see-through buildings (market stall) keep the
    # villager sprite visible at the door tile. Same style as the
    # enterable picker.
    var visible_header = Label.new()
    visible_header.text = "VISIBLE WHEN INSIDE"
    visible_header.add_theme_color_override("font_color", COLOR_LABEL)
    visible_header.add_theme_font_size_override("font_size", 11)
    _asset_fields_section.add_child(visible_header)

    _visible_when_inside_dropdown = OptionButton.new()
    _visible_when_inside_dropdown.add_theme_font_override("font", _font)
    _visible_when_inside_dropdown.add_theme_font_size_override("font_size", 13)
    _visible_when_inside_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    _visible_when_inside_dropdown.add_theme_stylebox_override("normal", enterable_style)
    _visible_when_inside_dropdown.add_item("No", 0)
    _visible_when_inside_dropdown.add_item("Yes", 1)
    _visible_when_inside_dropdown.item_selected.connect(_on_visible_when_inside_selected)
    _asset_fields_section.add_child(_visible_when_inside_dropdown)

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

    # SPRITE — current sprite name displayed read-only with a Change… button
    # that opens the modal picker. main.gd handles the picker + PATCH; the
    # WS broadcast then drives the visual swap on every connected client.
    var npc_sprite_header = Label.new()
    npc_sprite_header.text = "SPRITE"
    npc_sprite_header.add_theme_color_override("font_color", COLOR_LABEL)
    npc_sprite_header.add_theme_font_size_override("font_size", 11)
    _npc_fields_section.add_child(npc_sprite_header)

    var sprite_row = HBoxContainer.new()
    sprite_row.add_theme_constant_override("separation", 6)
    _npc_fields_section.add_child(sprite_row)

    # Reusable input/button stylebox — same shape the BEHAVIOR/AGENT dropdowns
    # below use, declared inline so this section can be moved/edited
    # independently of those dropdowns.
    var sprite_btn_style = StyleBoxFlat.new()
    sprite_btn_style.bg_color = Color(0.08, 0.07, 0.05, 1.0)
    sprite_btn_style.border_width_left = 1
    sprite_btn_style.border_width_top = 1
    sprite_btn_style.border_width_right = 1
    sprite_btn_style.border_width_bottom = 1
    sprite_btn_style.border_color = Color(0.3, 0.24, 0.15, 0.8)
    sprite_btn_style.content_margin_left = 6.0
    sprite_btn_style.content_margin_right = 6.0
    sprite_btn_style.content_margin_top = 4.0
    sprite_btn_style.content_margin_bottom = 4.0

    _npc_sprite_label = Label.new()
    _npc_sprite_label.add_theme_font_override("font", _font)
    _npc_sprite_label.add_theme_font_size_override("font_size", 13)
    _npc_sprite_label.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_sprite_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _npc_sprite_label.size_flags_vertical = Control.SIZE_SHRINK_CENTER
    _npc_sprite_label.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
    sprite_row.add_child(_npc_sprite_label)

    _npc_sprite_change_button = Button.new()
    _npc_sprite_change_button.text = "Change…"
    _npc_sprite_change_button.add_theme_font_override("font", _font)
    _npc_sprite_change_button.add_theme_font_size_override("font_size", 12)
    _npc_sprite_change_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_sprite_change_button.add_theme_stylebox_override("normal", sprite_btn_style)
    _npc_sprite_change_button.size_flags_vertical = Control.SIZE_SHRINK_CENTER
    _npc_sprite_change_button.pressed.connect(_on_npc_sprite_change_pressed)
    sprite_row.add_child(_npc_sprite_change_button)

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

    # Schedule section — shift offset for workers; cadence window for
    # washerwoman / town_crier. Fields always visible; admin picks what
    # matters for the NPC's behavior. Server ignores fields that don't
    # apply to the chosen behavior.
    _npc_schedule_section = VBoxContainer.new()
    _npc_schedule_section.add_theme_constant_override("separation", 4)
    _npc_fields_section.add_child(_npc_schedule_section)

    var sched_header = Label.new()
    sched_header.text = "SCHEDULE"
    sched_header.add_theme_color_override("font_color", COLOR_LABEL)
    sched_header.add_theme_font_size_override("font_size", 11)
    _npc_schedule_section.add_child(sched_header)

    # Start / End — absolute work-window in HH:MM. Both NULL on the server
    # means "inherit dawn/dusk", in which case the spinners are
    # prepopulated with the current global values so the admin can see and
    # edit from there. _npc_schedule_window_is_null tracks the NULL state;
    # the first real user value_changed flips it false and we start sending
    # concrete minutes.
    var start_row = _build_hm_row("Start",
        _on_schedule_window_field_changed,
        "Worker arrives at this time of day.")
    _npc_schedule_section.add_child(start_row.row)
    _npc_start_hour_spin = start_row.hour_spin
    _npc_start_minute_spin = start_row.minute_spin

    var end_row = _build_hm_row("End",
        _on_schedule_window_field_changed,
        "Worker leaves at this time of day. End < start wraps past midnight (e.g. 17:00–05:00 for a tavernkeeper).")
    _npc_schedule_section.add_child(end_row.row)
    _npc_end_hour_spin = end_row.hour_spin
    _npc_end_minute_spin = end_row.minute_spin

    # Lateness window — fuzzes the actual firing time within [nominal,
    # nominal+window) minutes. Per-NPC, per-boundary offset is
    # deterministic (seeded by NPC id + boundary) so the village feels
    # organic but any single NPC stays predictable across restarts.
    var lateness_row = HBoxContainer.new()
    lateness_row.add_theme_constant_override("separation", 6)
    _npc_schedule_section.add_child(lateness_row)
    var lateness_lbl = Label.new()
    lateness_lbl.text = "Lateness window (min)"
    lateness_lbl.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    lateness_lbl.add_theme_font_size_override("font_size", 11)
    lateness_lbl.tooltip_text = "Fuzzes the actual fire time within [nominal, nominal+window) minutes. 0 = always fires exactly at the nominal boundary. 30 = NPC fires 0-29 min after nominal, always late, never early. Offset is seeded by NPC id + boundary so it's stable across ticks and server restarts."
    lateness_row.add_child(lateness_lbl)
    _npc_lateness_spin = SpinBox.new()
    _npc_lateness_spin.min_value = 0
    _npc_lateness_spin.max_value = 180
    _npc_lateness_spin.step = 1
    _npc_lateness_spin.update_on_text_changed = true
    _npc_lateness_spin.value_changed.connect(_on_schedule_field_changed)
    _npc_lateness_spin.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    lateness_row.add_child(_npc_lateness_spin)

    _npc_cadence_check = CheckBox.new()
    _npc_cadence_check.text = "Use cadence window"
    _npc_cadence_check.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_cadence_check.add_theme_font_size_override("font_size", 11)
    _npc_cadence_check.toggled.connect(_on_cadence_toggled)
    _npc_cadence_check.toggled.connect(func(_v): _schedule_save_debounced())
    _npc_schedule_section.add_child(_npc_cadence_check)

    _npc_cadence_row = HBoxContainer.new()
    _npc_cadence_row.add_theme_constant_override("separation", 6)
    _npc_schedule_section.add_child(_npc_cadence_row)
    var int_lbl = Label.new()
    int_lbl.text = "Every (h)"
    int_lbl.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    int_lbl.add_theme_font_size_override("font_size", 11)
    _npc_cadence_row.add_child(int_lbl)
    _npc_interval_spin = SpinBox.new()
    _npc_interval_spin.min_value = 1
    _npc_interval_spin.max_value = 24
    _npc_interval_spin.step = 1
    _npc_interval_spin.value = 3
    _npc_interval_spin.update_on_text_changed = true
    _npc_interval_spin.value_changed.connect(_on_schedule_field_changed)
    _npc_interval_spin.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _npc_cadence_row.add_child(_npc_interval_spin)

    _npc_cadence_row2 = HBoxContainer.new()
    _npc_cadence_row2.add_theme_constant_override("separation", 6)
    _npc_schedule_section.add_child(_npc_cadence_row2)
    var start_lbl = Label.new()
    start_lbl.text = "Start"
    start_lbl.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    start_lbl.add_theme_font_size_override("font_size", 11)
    _npc_cadence_row2.add_child(start_lbl)
    _npc_start_spin = SpinBox.new()
    _npc_start_spin.min_value = 0
    _npc_start_spin.max_value = 23
    _npc_start_spin.step = 1
    _npc_start_spin.value = 9
    _npc_start_spin.update_on_text_changed = true
    _npc_start_spin.value_changed.connect(_on_schedule_field_changed)
    _npc_start_spin.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _npc_cadence_row2.add_child(_npc_start_spin)
    var end_lbl = Label.new()
    end_lbl.text = "End"
    end_lbl.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    end_lbl.add_theme_font_size_override("font_size", 11)
    _npc_cadence_row2.add_child(end_lbl)
    _npc_end_spin = SpinBox.new()
    _npc_end_spin.min_value = 0
    _npc_end_spin.max_value = 23
    _npc_end_spin.step = 1
    _npc_end_spin.value = 18
    _npc_end_spin.update_on_text_changed = true
    _npc_end_spin.value_changed.connect(_on_schedule_field_changed)
    _npc_end_spin.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _npc_cadence_row2.add_child(_npc_end_spin)

    _npc_schedule_save_button = Button.new()
    _npc_schedule_save_button.text = "Save Schedule"
    _npc_schedule_save_button.add_theme_font_override("font", _font)
    _npc_schedule_save_button.add_theme_font_size_override("font_size", 13)
    _npc_schedule_save_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_schedule_save_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_schedule_save_button.pressed.connect(_on_schedule_save_pressed)
    _npc_schedule_section.add_child(_npc_schedule_save_button)

    # Social-hour section — orthogonal overlay on behavior (ZBBS-068).
    # Any NPC can opt into a daily window where they walk to the nearest
    # structure carrying a named state tag (e.g. the tavern) and head
    # home when the window ends. Checkbox gates the three fields the same
    # way "Use cadence window" gates the rotation triple.
    var social_header = Label.new()
    social_header.text = "SOCIAL HOUR"
    social_header.add_theme_color_override("font_color", COLOR_LABEL)
    social_header.add_theme_font_size_override("font_size", 11)
    _npc_schedule_section.add_child(social_header)

    _npc_social_check = CheckBox.new()
    _npc_social_check.text = "Gathers at tagged structure"
    _npc_social_check.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_social_check.add_theme_font_size_override("font_size", 11)
    _npc_social_check.toggled.connect(_on_social_toggled)
    _npc_social_check.toggled.connect(func(_v): _emit_social_changed())
    _npc_schedule_section.add_child(_npc_social_check)

    _npc_social_tag_row = HBoxContainer.new()
    _npc_social_tag_row.add_theme_constant_override("separation", 6)
    _npc_schedule_section.add_child(_npc_social_tag_row)
    var social_tag_lbl = Label.new()
    social_tag_lbl.text = "Tag"
    social_tag_lbl.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    social_tag_lbl.add_theme_font_size_override("font_size", 11)
    _npc_social_tag_row.add_child(social_tag_lbl)
    _npc_social_tag_dropdown = OptionButton.new()
    _npc_social_tag_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _npc_social_tag_dropdown.item_selected.connect(func(_i): _emit_social_changed())
    _npc_social_tag_row.add_child(_npc_social_tag_dropdown)

    # HH:MM start / end rows. Default 19:00 / 22:00 so a freshly-checked
    # social schedule lands in the typical evening tavern window.
    var social_start_row = _build_hm_row("Start",
        func(_v): _emit_social_changed(),
        "Time of day the NPC walks to the nearest matching structure.")
    _npc_schedule_section.add_child(social_start_row.row)
    _npc_social_start_hour_spin = social_start_row.hour_spin
    _npc_social_start_minute_spin = social_start_row.minute_spin
    _npc_social_start_hour_spin.value = 19

    var social_end_row = _build_hm_row("End",
        func(_v): _emit_social_changed(),
        "Time of day the NPC walks home. End < start wraps past midnight.")
    _npc_schedule_section.add_child(social_end_row.row)
    _npc_social_end_hour_spin = social_end_row.hour_spin
    _npc_social_end_minute_spin = social_end_row.minute_spin
    _npc_social_end_hour_spin.value = 22
    # Hold _npc_social_row at the start row so legacy show/hide code that
    # toggled visibility on the single row still affects the visible UI.
    _npc_social_row = social_start_row.row

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

    # Villagers browser — alphabetical list of all placed NPCs with a
    # name filter. Sits alongside _catalog_scroll; visibility toggled by
    # the Catalog / Villagers tab buttons. See _show_browse_surfaces and
    # rebuild_villagers_list for the state management + content rules.
    _villagers_scroll = ScrollContainer.new()
    _villagers_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    _villagers_scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _villagers_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    _villagers_scroll.visible = false
    vbox.add_child(_villagers_scroll)

    var villagers_body = VBoxContainer.new()
    villagers_body.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    villagers_body.add_theme_constant_override("separation", 6)
    _villagers_scroll.add_child(villagers_body)

    # Name-substring filter. Reuses name_input_style from earlier in
    # _ready so the input visually matches the rest of the panel's text
    # fields without declaring another StyleBoxFlat.
    _villagers_filter_input = LineEdit.new()
    _villagers_filter_input.placeholder_text = "Filter by name"
    _villagers_filter_input.add_theme_font_override("font", _font)
    _villagers_filter_input.add_theme_font_size_override("font_size", 13)
    _villagers_filter_input.add_theme_color_override("font_color", COLOR_TEXT)
    _villagers_filter_input.add_theme_color_override("font_placeholder_color", Color(0.45, 0.40, 0.30, 1.0))
    _villagers_filter_input.add_theme_stylebox_override("normal", name_input_style)
    _villagers_filter_input.add_theme_stylebox_override("focus", name_input_style)
    _villagers_filter_input.text_changed.connect(_on_villagers_filter_changed)
    villagers_body.add_child(_villagers_filter_input)

    _villagers_list = VBoxContainer.new()
    _villagers_list.add_theme_constant_override("separation", 2)
    _villagers_list.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    villagers_body.add_child(_villagers_list)

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
    var headers: PackedStringArray = Auth.auth_headers(false)
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
    if _ignoring_name_input or _name_input_object_id == "":
        return
    _name_input.release_focus()
    display_name_changed.emit(new_text.strip_edges(), _name_input_object_id)

func _on_name_focus_lost() -> void:
    if _ignoring_name_input or _name_input_object_id == "":
        return
    display_name_changed.emit(_name_input.text.strip_edges(), _name_input_object_id)

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

## Toggles the three cadence SpinBoxes enabled/disabled together. Visual
## disable only — the save handler also checks the checkbox state when
## building the payload, so unchecked always sends null regardless of
## the SpinBox values.
func _on_cadence_toggled(enabled: bool) -> void:
    if _npc_interval_spin != null:
        _npc_interval_spin.editable = enabled
    if _npc_start_spin != null:
        _npc_start_spin.editable = enabled
    if _npc_end_spin != null:
        _npc_end_spin.editable = enabled

## Gates the social-hour fields the same way cadence does. Editable state
## follows the checkbox so a disabled schedule can't push stale values
## through the auto-save.
func _on_social_toggled(enabled: bool) -> void:
    if _npc_social_tag_dropdown != null:
        _npc_social_tag_dropdown.disabled = not enabled
    if _npc_social_start_hour_spin != null:
        _npc_social_start_hour_spin.editable = enabled
    if _npc_social_start_minute_spin != null:
        _npc_social_start_minute_spin.editable = enabled
    if _npc_social_end_hour_spin != null:
        _npc_social_end_hour_spin.editable = enabled
    if _npc_social_end_minute_spin != null:
        _npc_social_end_minute_spin.editable = enabled

## Emits npc_social_changed with the current field values. When the
## checkbox is off or the tag dropdown is empty, emit an empty tag — the
## handler converts that into a null-clear payload.
func _emit_social_changed() -> void:
    if _ignoring_npc_inputs:
        return
    if _npc_social_check == null or not _npc_social_check.button_pressed:
        npc_social_changed.emit("", 0, 0)
        return
    var tag: String = ""
    if _npc_social_tag_dropdown != null and _npc_social_tag_dropdown.selected >= 0:
        tag = _npc_social_tag_dropdown.get_item_text(_npc_social_tag_dropdown.selected)
    var start_min: int = int(_npc_social_start_hour_spin.value) * 60 + int(_npc_social_start_minute_spin.value)
    var end_min: int = int(_npc_social_end_hour_spin.value) * 60 + int(_npc_social_end_minute_spin.value)
    npc_social_changed.emit(tag, start_min, end_min)

## Populate the social tag dropdown from the server allowlist. Called by
## main.gd after it fetches the object-tags allowlist (social_tag is
## itself a per-instance object tag, so this dropdown draws from the same
## source as _obj_tags_add_dropdown). Idempotent.
func set_social_tag_options(tags: Array) -> void:
    if _npc_social_tag_dropdown == null:
        return
    _npc_social_tag_dropdown.clear()
    for tag in tags:
        _npc_social_tag_dropdown.add_item(str(tag))

## Refresh the per-instance tag UI for the currently-selected object.
## Chips box is redrawn from _obj_tags_current_list; the add dropdown
## lists any allowlist tag not already applied.
func _refresh_obj_tags_ui() -> void:
    if _obj_tags_chips_box == null:
        return
    for child in _obj_tags_chips_box.get_children():
        child.queue_free()
    if _obj_tags_current_list.size() == 0:
        var empty = Label.new()
        empty.text = "(none)"
        empty.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        empty.add_theme_font_size_override("font_size", 11)
        _obj_tags_chips_box.add_child(empty)
    else:
        for tag in _obj_tags_current_list:
            _obj_tags_chips_box.add_child(_build_tag_chip(str(tag)))

    if _obj_tags_add_dropdown == null:
        return
    _obj_tags_add_dropdown.clear()
    var current_set: Dictionary = {}
    for t in _obj_tags_current_list:
        current_set[str(t)] = true
    for tag in Catalog.object_tags:
        if not current_set.has(str(tag)):
            _obj_tags_add_dropdown.add_item(str(tag))

## Render a removable tag chip. Styled as a pill with the tag name and a
## separate X button so we're not relying on font coverage for the close
## glyph (IMFellEnglish doesn't ship ✕ and falls back to garbage).
func _build_tag_chip(tag: String) -> Control:
    var pill = PanelContainer.new()
    var pill_style = StyleBoxFlat.new()
    pill_style.bg_color = Color(0.23, 0.17, 0.08, 1.0)
    pill_style.border_width_left = 1
    pill_style.border_width_top = 1
    pill_style.border_width_right = 1
    pill_style.border_width_bottom = 1
    pill_style.border_color = COLOR_BTN_BORDER
    pill_style.corner_radius_left_top = 8
    pill_style.corner_radius_right_top = 8
    pill_style.corner_radius_left_bottom = 8
    pill_style.corner_radius_right_bottom = 8
    pill_style.content_margin_left = 8.0
    pill_style.content_margin_right = 4.0
    pill_style.content_margin_top = 2.0
    pill_style.content_margin_bottom = 2.0
    pill.add_theme_stylebox_override("panel", pill_style)

    var row = HBoxContainer.new()
    row.add_theme_constant_override("separation", 6)
    pill.add_child(row)

    var label = Label.new()
    label.text = tag
    label.add_theme_color_override("font_color", COLOR_TEXT)
    label.add_theme_font_size_override("font_size", 11)
    row.add_child(label)

    var x_btn = Button.new()
    x_btn.text = "x"
    x_btn.flat = true
    x_btn.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    x_btn.add_theme_color_override("font_hover_color", Color(1, 0.6, 0.5))
    x_btn.add_theme_font_size_override("font_size", 11)
    x_btn.custom_minimum_size = Vector2(14, 14)
    x_btn.focus_mode = Control.FOCUS_NONE
    x_btn.pressed.connect(_on_obj_tag_chip_pressed.bind(tag))
    row.add_child(x_btn)

    return pill

## Add one row to the marker legend in the placement properties panel.
## Each row is a small colored swatch (square or circle) followed by a
## description. Used so admins can decode the colored handles drawn on
## the selected placement in the world.
func _add_marker_legend_row(swatch_color: Color, is_circle: bool, description: String) -> void:
    var row = HBoxContainer.new()
    row.add_theme_constant_override("separation", 8)
    _asset_fields_section.add_child(row)

    # Swatch — small ColorRect for the square markers, or a Polygon2D
    # circle in a Control for the loiter/gather circle markers.
    var swatch_size: Vector2 = Vector2(12, 12)
    var swatch: Control
    if is_circle:
        swatch = Control.new()
        swatch.custom_minimum_size = swatch_size
        var circle := Polygon2D.new()
        circle.color = swatch_color
        var poly := PackedVector2Array()
        var segments: int = 16
        var radius: float = 5.0
        for i in range(segments):
            var t: float = float(i) / float(segments) * TAU
            poly.append(Vector2(cos(t) * radius + 6.0, sin(t) * radius + 6.0))
        circle.polygon = poly
        swatch.add_child(circle)
    else:
        var rect := ColorRect.new()
        rect.color = swatch_color
        rect.custom_minimum_size = swatch_size
        swatch = rect
    row.add_child(swatch)

    var desc = Label.new()
    desc.text = description
    desc.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    desc.add_theme_font_size_override("font_size", 10)
    desc.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    desc.autowrap_mode = TextServer.AUTOWRAP_WORD
    row.add_child(desc)

func _on_obj_tag_add_pressed() -> void:
    if _obj_tags_add_dropdown == null or _obj_tags_add_dropdown.selected < 0:
        return
    if _obj_tags_current_id == "":
        return
    var tag: String = _obj_tags_add_dropdown.get_item_text(_obj_tags_add_dropdown.selected)
    if world != null:
        world.add_object_tag(_obj_tags_current_id, tag)

func _on_obj_tag_chip_pressed(tag: String) -> void:
    if _obj_tags_current_id == "":
        return
    if world != null:
        world.remove_object_tag(_obj_tags_current_id, tag)

## Called by main.gd when the WS village_object_tags_updated event lands
## (via world.object_tags_updated). Refresh if this is the selected object.
func apply_object_tags_external(object_id: String, tags: Array) -> void:
    if _obj_tags_current_id != object_id:
        return
    _obj_tags_current_list = tags
    _refresh_obj_tags_ui()

## Emits the schedule-changed signal with the current field values.
## start_min/end_min are -1 when the window is NULL-inheriting dawn/dusk;
## interval/start/end are -1 when cadence is unchecked. The handler
## downstream converts -1 to null in the PATCH payload.
func _on_schedule_save_pressed() -> void:
    _emit_schedule_changed()

## Auto-save on any SpinBox value change for fields that don't affect the
## window's NULL state (lateness, cadence). Matches the behavior / home /
## work pickers that also save immediately on change, so admins don't need
## a separate Save button to make the SCHEDULE section stick.
func _on_schedule_field_changed(_value: float) -> void:
    _emit_schedule_changed()

## Auto-save for the four window spinners (start HH/MM, end HH/MM). The
## first user-driven change flips the NPC out of NULL-inherits-dawn/dusk
## and into "concrete window," so subsequent emits send actual minutes.
func _on_schedule_window_field_changed(_value: float) -> void:
    if _ignoring_npc_inputs:
        return
    _npc_schedule_window_is_null = false
    _emit_schedule_changed()

## Alias for the cadence checkbox handler lambda — same semantics as
## any other field change.
func _schedule_save_debounced() -> void:
    _emit_schedule_changed()

## Shared emitter for all three auto-save paths.
func _emit_schedule_changed() -> void:
    if _ignoring_npc_inputs:
        return
    var start_min: int = -1
    var end_min: int = -1
    if not _npc_schedule_window_is_null:
        start_min = int(_npc_start_hour_spin.value) * 60 + int(_npc_start_minute_spin.value)
        end_min = int(_npc_end_hour_spin.value) * 60 + int(_npc_end_minute_spin.value)
    var lateness: int = int(_npc_lateness_spin.value) if _npc_lateness_spin != null else 0
    var interval: int = -1
    var start_h: int = -1
    var end_h: int = -1
    if _npc_cadence_check.button_pressed:
        interval = int(_npc_interval_spin.value)
        start_h = int(_npc_start_spin.value)
        end_h = int(_npc_end_spin.value)
    npc_schedule_changed.emit(start_min, end_min, interval, start_h, end_h, lateness)

## Build a reusable Start/End HH:MM row. Returns a dict with the row
## container plus the two SpinBox children, so the caller can wire them
## into its own state. The change_handler takes a float (Godot's
## value_changed payload) so it matches existing _on_schedule_field_changed
## signatures.
func _build_hm_row(label_text: String, change_handler: Callable, tooltip: String = "") -> Dictionary:
    var row = HBoxContainer.new()
    row.add_theme_constant_override("separation", 6)
    var lbl = Label.new()
    lbl.text = label_text
    lbl.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    lbl.add_theme_font_size_override("font_size", 11)
    if tooltip != "":
        lbl.tooltip_text = tooltip
    row.add_child(lbl)
    var hh = SpinBox.new()
    hh.min_value = 0
    hh.max_value = 23
    hh.step = 1
    hh.update_on_text_changed = true
    hh.value_changed.connect(change_handler)
    hh.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.add_child(hh)
    var colon = Label.new()
    colon.text = ":"
    colon.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    colon.add_theme_font_size_override("font_size", 11)
    row.add_child(colon)
    var mm = SpinBox.new()
    mm.min_value = 0
    mm.max_value = 59
    mm.step = 1
    mm.update_on_text_changed = true
    mm.value_changed.connect(change_handler)
    mm.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.add_child(mm)
    return {"row": row, "hour_spin": hh, "minute_spin": mm}

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

## Forward to main.gd which owns the modal picker. Carries the current
## sprite_id so the picker can highlight it.
func _on_npc_sprite_change_pressed() -> void:
    if _npc_current_id == "":
        return
    npc_sprite_change_requested.emit(_npc_current_id, _npc_current_sprite_id)

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

## Returns true if the villager is currently INSIDE the given structure.
## The inside flag alone isn't sufficient (could be inside their work
## instead of home), so we additionally check that their persisted
## position is near the structure's door tile. Both conditions: greyed
## out only when they're genuinely there.
func _is_npc_at_structure_door(npc_container: Node2D, structure_id: String) -> bool:
    if npc_container == null or structure_id == "" or world == null:
        return false
    if not bool(npc_container.get_meta("inside", false)):
        return false
    var structure: Node2D = world.placed_objects.get(structure_id, null)
    if structure == null:
        return false
    var asset_id: String = structure.get_meta("asset_id", "")
    var asset: Dictionary = Catalog.assets.get(asset_id, {})
    var door_world: Vector2 = structure.position
    var dox = asset.get("door_offset_x", null)
    var doy = asset.get("door_offset_y", null)
    if dox != null and doy != null:
        door_world += Vector2(float(dox) * 32.0, float(doy) * 32.0)
    return npc_container.position.distance_to(door_world) < 48.0

func _on_enterable_selected(index: int) -> void:
    if _ignoring_enterable_toggle or _enterable_asset_id == "":
        return
    asset_enterable_toggled.emit(_enterable_asset_id, index == 1)

func _on_visible_when_inside_selected(index: int) -> void:
    if _ignoring_enterable_toggle or _enterable_asset_id == "":
        return
    asset_visible_when_inside_toggled.emit(_enterable_asset_id, index == 1)

## Build the Catalog/Villagers tab button. Base style is COLOR_BTN_BG,
## active state swaps in the brighter ACTIVE background used by the
## main tool buttons. Both tabs grow to share the sidebar width evenly.
func _make_view_tab_button(label: String) -> Button:
    var btn := Button.new()
    btn.text = label
    btn.add_theme_font_override("font", _font)
    btn.add_theme_font_size_override("font_size", 13)
    btn.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    btn.add_theme_color_override("font_hover_color", COLOR_TEXT)
    btn.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    btn.focus_mode = Control.FOCUS_NONE
    return btn

## Apply inactive/active styling to a tab button. Active tab gets the
## brighter ACTIVE_BG + a bottom border so it reads as the current pane.
func _set_view_tab_active(btn: Button, active: bool) -> void:
    var style := StyleBoxFlat.new()
    style.content_margin_left = 8.0
    style.content_margin_right = 8.0
    style.content_margin_top = 5.0
    style.content_margin_bottom = 5.0
    style.corner_radius_top_left = 3
    style.corner_radius_top_right = 3
    if active:
        style.bg_color = COLOR_BTN_ACTIVE_BG
        style.border_width_bottom = 2
        style.border_color = COLOR_BTN_ACTIVE_BORDER
        btn.add_theme_color_override("font_color", COLOR_TEXT)
    else:
        style.bg_color = COLOR_BTN_BG
        style.border_width_bottom = 1
        style.border_color = COLOR_BTN_BORDER
        btn.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    btn.add_theme_stylebox_override("normal", style)
    btn.add_theme_stylebox_override("hover", style)
    btn.add_theme_stylebox_override("pressed", style)

func _on_catalog_tab_pressed() -> void:
    if _active_view == "catalog":
        return
    _active_view = "catalog"
    _set_view_tab_active(_catalog_tab_button, true)
    _set_view_tab_active(_villagers_tab_button, false)
    _show_browse_surfaces()

func _on_villagers_tab_pressed() -> void:
    if _active_view == "villagers":
        return
    _active_view = "villagers"
    _set_view_tab_active(_catalog_tab_button, false)
    _set_view_tab_active(_villagers_tab_button, true)
    _show_browse_surfaces()

## Render the content region that sits below the tab bar. Exactly one
## of _terrain_picker / _catalog_scroll / _villagers_scroll is shown,
## picked by _terrain_active and _active_view. No-op while the selection
## inspector is visible — _hide_browse_surfaces owns that case.
func _show_browse_surfaces() -> void:
    _view_tabs_row.visible = not _terrain_active
    if _terrain_active:
        _terrain_picker.visible = true
        _catalog_scroll.visible = false
        _villagers_scroll.visible = false
        return
    _terrain_picker.visible = false
    if _active_view == "villagers":
        _catalog_scroll.visible = false
        _villagers_scroll.visible = true
        rebuild_villagers_list()
    else:
        _catalog_scroll.visible = true
        _villagers_scroll.visible = false

## Hide every browse surface — called when a selection panel takes over
## the content region. Tabs hide too; they reappear when selection clears.
func _hide_browse_surfaces() -> void:
    _view_tabs_row.visible = false
    _catalog_scroll.visible = false
    _villagers_scroll.visible = false
    _terrain_picker.visible = false

func _on_villagers_filter_changed(_text: String) -> void:
    rebuild_villagers_list()

## Rebuild the Villagers list from world.placed_npcs. Cheap enough to
## run on tab activation, WS list-changed events, and filter keystrokes.
## Alphabetical by display_name; unnamed NPCs sort to the bottom under
## "(unnamed)".
func rebuild_villagers_list() -> void:
    if _villagers_list == null or world == null:
        return
    for child in _villagers_list.get_children():
        child.queue_free()
    _villager_rows.clear()

    var npc_entries: Array = []
    for npc_id in world.placed_npcs:
        var container: Node2D = world.placed_npcs[npc_id]
        if container == null:
            continue
        var display_name: String = str(container.get_meta("display_name", ""))
        var sort_name: String = display_name if display_name != "" else "(unnamed)"
        npc_entries.append({
            "id": str(npc_id),
            "sort_name": sort_name,
            "display_name": sort_name,
            "container": container,
        })
    npc_entries.sort_custom(func(a, b): return a["sort_name"].to_lower() < b["sort_name"].to_lower())

    var filter_text: String = ""
    if _villagers_filter_input != null:
        filter_text = _villagers_filter_input.text.to_lower()
    for entry in npc_entries:
        if filter_text != "" and not entry["sort_name"].to_lower().contains(filter_text):
            continue
        var row := _make_villager_row(entry["id"], entry["display_name"], entry["container"])
        _villagers_list.add_child(row)
        _villager_rows[entry["id"]] = row

    _refresh_villager_row_highlight()

## Three-line row: name / behavior / landmark-relative location. Row
## uses PanelContainer + gui_input rather than a Button because Buttons
## don't render multi-line text cleanly under this theme.
func _make_villager_row(npc_id: String, display_name: String, container: Node2D) -> Control:
    var row := PanelContainer.new()
    row.mouse_filter = Control.MOUSE_FILTER_STOP
    row.set_meta("npc_id", npc_id)

    var normal_style := StyleBoxFlat.new()
    normal_style.bg_color = Color(0, 0, 0, 0)
    normal_style.content_margin_left = 6.0
    normal_style.content_margin_right = 6.0
    normal_style.content_margin_top = 4.0
    normal_style.content_margin_bottom = 4.0
    row.add_theme_stylebox_override("panel", normal_style)

    var hover_style := normal_style.duplicate()
    hover_style.bg_color = COLOR_ITEM_HOVER
    var selected_style := normal_style.duplicate()
    selected_style.bg_color = COLOR_ITEM_SELECTED
    selected_style.border_width_left = 2
    selected_style.border_color = COLOR_BTN_ACTIVE_BORDER
    row.set_meta("style_normal", normal_style)
    row.set_meta("style_hover", hover_style)
    row.set_meta("style_selected", selected_style)

    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 0)
    vb.mouse_filter = Control.MOUSE_FILTER_IGNORE
    row.add_child(vb)

    var name_label := Label.new()
    name_label.text = display_name
    name_label.add_theme_font_override("font", _font)
    name_label.add_theme_font_size_override("font_size", 13)
    name_label.add_theme_color_override("font_color", COLOR_TEXT)
    name_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    vb.add_child(name_label)

    var behavior_label := Label.new()
    behavior_label.add_theme_font_override("font", _font)
    # Sub-line size is 12, not 11 — IMFellEnglish has irregular widths
    # and renders raggedly at very small sizes (visible gaps inside
    # words). 12 still reads as secondary against the 13 primary line.
    behavior_label.add_theme_font_size_override("font_size", 12)
    behavior_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    behavior_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    vb.add_child(behavior_label)

    var location_label := Label.new()
    location_label.add_theme_font_override("font", _font)
    location_label.add_theme_font_size_override("font_size", 12)
    location_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    location_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    vb.add_child(location_label)

    row.set_meta("name_label", name_label)
    row.set_meta("behavior_label", behavior_label)
    row.set_meta("location_label", location_label)

    _update_villager_row_text(row, container)

    row.mouse_entered.connect(func(): _on_villager_row_hover(row, true))
    row.mouse_exited.connect(func(): _on_villager_row_hover(row, false))
    row.gui_input.connect(func(ev): _on_villager_row_gui_input(ev, npc_id))
    return row

## Format the behavior + location text on a villager row.
func _update_villager_row_text(row: PanelContainer, container: Node2D) -> void:
    var behavior: String = str(container.get_meta("behavior", ""))
    var behavior_text: String = behavior if behavior != "" else "no behavior"
    var behavior_label: Label = row.get_meta("behavior_label")
    if behavior_label != null:
        behavior_label.text = behavior_text
    var location_label: Label = row.get_meta("location_label")
    if location_label != null:
        location_label.text = _format_npc_location(container)

## Build a human-readable location string. Inside a structure → its name.
## Outside → nearest placed_object by world distance, no distance cap
## (nearest landmark is almost always more recognizable than raw coords).
## Only falls back to tile coords when the village has zero placed_objects.
func _format_npc_location(container: Node2D) -> String:
    if world == null:
        return ""
    var inside: bool = bool(container.get_meta("inside", false))
    var inside_id: String = str(container.get_meta("inside_structure_id", ""))
    if inside and inside_id != "" and world.placed_objects.has(inside_id):
        return "inside " + _object_display_name(world.placed_objects[inside_id])
    var pos: Vector2 = container.position
    var best_label: String = ""
    var best_dist_sq: float = INF
    for obj_id in world.placed_objects:
        var obj: Node2D = world.placed_objects[obj_id]
        if obj == null:
            continue
        var d: float = pos.distance_squared_to(obj.position)
        if d < best_dist_sq:
            best_dist_sq = d
            best_label = _object_display_name(obj)
    if best_label != "":
        return "near " + best_label
    var tile_x: int = int(floor(pos.x / 32.0))
    var tile_y: int = int(floor(pos.y / 32.0))
    return "at %d, %d" % [tile_x, tile_y]

## Preferred label for a placed_object: its instance display_name, or
## the asset's catalog name as a fallback.
func _object_display_name(obj: Node2D) -> String:
    var label: String = str(obj.get_meta("display_name", ""))
    if label != "":
        return label
    var asset_id: String = str(obj.get_meta("asset_id", ""))
    var asset = Catalog.assets.get(asset_id, {})
    return asset.get("name", asset_id)

func _on_villager_row_hover(row: PanelContainer, entered: bool) -> void:
    # Don't overwrite the selected-row highlight on hover.
    if row.get_meta("npc_id", "") == _villagers_selected_id:
        return
    var target: StyleBoxFlat
    if entered:
        target = row.get_meta("style_hover")
    else:
        target = row.get_meta("style_normal")
    row.add_theme_stylebox_override("panel", target)

func _on_villager_row_gui_input(ev: InputEvent, npc_id: String) -> void:
    if ev is InputEventMouseButton and ev.pressed and ev.button_index == MOUSE_BUTTON_LEFT:
        # Same signal the in-panel People list uses — main.gd already
        # handles select + camera.center_on in _on_npc_select_requested.
        npc_select_requested.emit(npc_id)

## Apply the selected-row highlight to whichever row matches
## _villagers_selected_id, and scroll it into view. Non-matching rows
## are reset to their normal style (clears any stale hover state too).
func _refresh_villager_row_highlight() -> void:
    for npc_id in _villager_rows:
        var row: PanelContainer = _villager_rows[npc_id]
        if row == null:
            continue
        var style: StyleBoxFlat
        if str(npc_id) == _villagers_selected_id:
            style = row.get_meta("style_selected")
        else:
            style = row.get_meta("style_normal")
        row.add_theme_stylebox_override("panel", style)
    # Scroll the selected row into view. Godot's ScrollContainer has
    # ensure_control_visible for exactly this, but it lives on Container
    # and needs the row's layout to have resolved — defer to next frame.
    if _villagers_selected_id != "" and _villager_rows.has(_villagers_selected_id):
        var row: Control = _villager_rows[_villagers_selected_id]
        if _villagers_scroll != null and row != null:
            _villagers_scroll.call_deferred("ensure_control_visible", row)

## Called by main.gd when an NPC is selected (from map or from the
## Villagers list). Updates _villagers_selected_id and re-styles the
## rows so selection stays in sync across surfaces.
func sync_villager_selection(npc_id: String) -> void:
    _villagers_selected_id = npc_id
    _refresh_villager_row_highlight()

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
        var inside_id: String = str(container.get_meta("inside_structure_id", ""))
        var is_home: bool = home_id == object_id
        var is_work: bool = work_id == object_id
        # Also list NPCs who are CURRENTLY inside this structure even if
        # they don't live or work here — the social scheduler walks NPCs
        # to tagged structures (e.g. a tavern they don't own), and an
        # admin selecting that structure expects to see who's actually
        # there, not just who belongs there.
        var is_inside: bool = inside_id == object_id
        if not is_home and not is_work and not is_inside:
            continue
        any = true
        var label: String = str(container.get_meta("display_name", ""))
        if label == "":
            label = "(unnamed)"
        # Build the role suffix by joining whichever relations apply.
        # Order: home, work, inside — so "(home, currently inside)" reads
        # naturally for a villager standing in their own house.
        var parts: Array = []
        if is_home:
            parts.append("home")
        if is_work:
            parts.append("work")
        if is_inside:
            parts.append("currently inside")
        var tag: String = "  (" + ", ".join(parts) + ")"
        var row := Button.new()
        row.text = label + tag
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
        _selection_info_scroll.visible = false
        _delete_button.disabled = true
        _placed_by_label.visible = false
        _owner_label.visible = false
        _attachments_section.visible = false
        _show_browse_surfaces()  # Restore the active tab's view on deselect
        return
    _selection_info_scroll.visible = true
    _asset_fields_section.visible = true
    _npc_fields_section.visible = false
    _delete_button.disabled = false
    _hide_browse_surfaces()  # Selection inspector takes over the content region
    var asset = Catalog.assets.get(asset_id, {})
    var name: String = asset.get("name", asset_id)
    _selection_label.text = name

    # Show attachments if this asset has slots
    _build_attachments(asset_id)

    # Sync both asset-level dropdowns (enterable + visible-when-inside).
    _ignoring_enterable_toggle = true
    _enterable_asset_id = asset_id
    _enterable_dropdown.selected = 1 if bool(asset.get("enterable", false)) else 0
    if _visible_when_inside_dropdown != null:
        _visible_when_inside_dropdown.selected = 1 if bool(asset.get("visible_when_inside", false)) else 0
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
    _name_input_object_id = info.get("object_id", "")
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

    # Per-instance tags for the selected object.
    _obj_tags_current_id = info.get("object_id", "")
    var tags_raw = info.get("tags", [])
    _obj_tags_current_list = tags_raw if tags_raw is Array else []
    _refresh_obj_tags_ui()

## Called by editor when an NPC is selected/deselected. Reuses the selection
## panel but swaps to NPC-only fields (no owner, no attachments, no delete
## until placement/delete ships in a follow-up).
func show_npc_selection(info: Dictionary) -> void:
    var npc_id: String = info.get("npc_id", "")
    if npc_id == "":
        _selection_info_scroll.visible = false
        _npc_fields_section.visible = false
        sync_villager_selection("")
        _show_browse_surfaces()
        return
    _selection_info_scroll.visible = true
    _asset_fields_section.visible = false
    _npc_fields_section.visible = true
    _delete_button.disabled = false
    sync_villager_selection(npc_id)
    _hide_browse_surfaces()

    var display_name: String = info.get("display_name", "")
    _selection_label.text = display_name if display_name != "" else "(unnamed)"

    _ignoring_npc_inputs = true

    # Drop focus on the name edit before replacing its text — otherwise a
    # focus_exited fires after we've already swapped to a different NPC and
    # emits the old name against the new selection.
    _npc_name_edit.release_focus()
    _npc_name_edit.text = display_name

    # Stash the NPC + sprite identity for the Change… button. Show the
    # sprite name (or its id as a fallback) so admins can tell which sheet
    # is currently in use without opening the picker.
    _npc_current_id = npc_id
    _npc_current_sprite_id = str(info.get("sprite_id", ""))
    var sprite_name: String = str(info.get("sprite_name", ""))
    if sprite_name == "":
        sprite_name = _npc_current_sprite_id if _npc_current_sprite_id != "" else "(unknown)"
    _npc_sprite_label.text = sprite_name
    _npc_sprite_label.tooltip_text = sprite_name

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
    # Go Home / Go to Work disabled when (a) no such structure linked, or
    # (b) the villager is already standing at that structure's door tile.
    # Position-based check rather than the inside flag, because inside can
    # be true for work too.
    var npc_container: Node2D = null
    if world != null and npc_id != "" and world.placed_npcs.has(npc_id):
        npc_container = world.placed_npcs[npc_id]
    if _npc_go_home_button != null:
        var at_home: bool = _is_npc_at_structure_door(npc_container, _npc_home_current_id)
        _npc_go_home_button.disabled = _npc_home_current_id == "" or at_home
    if _npc_go_to_work_button != null:
        var at_work: bool = _is_npc_at_structure_door(npc_container, _npc_work_current_id)
        _npc_go_to_work_button.disabled = _npc_work_current_id == "" or at_work

    # Schedule window — both NULL means "inherit dawn/dusk", in which case
    # we prepopulate the spinners from the global defaults so the admin
    # can see what the NPC is actually doing. The is-null flag stays true
    # until the user actually changes a value (handled by
    # _on_schedule_window_field_changed), keeping NULL as a real persistent
    # state rather than a one-time fill.
    var start_raw_min = info.get("schedule_start_minute", null)
    var end_raw_min = info.get("schedule_end_minute", null)
    _npc_schedule_window_is_null = start_raw_min == null or end_raw_min == null
    var start_min: int
    var end_min: int
    if _npc_schedule_window_is_null:
        # Pull dawn/dusk from world; if the world hasn't loaded the config
        # yet, world.get_*_minute() returns the engine defaults so the
        # spinners still show plausible values.
        if world != null:
            start_min = world.get_dawn_minute()
            end_min = world.get_dusk_minute()
        else:
            start_min = 7 * 60
            end_min = 19 * 60
    else:
        start_min = int(start_raw_min)
        end_min = int(end_raw_min)
    if _npc_start_hour_spin != null:
        _npc_start_hour_spin.value = start_min / 60
        _npc_start_minute_spin.value = start_min % 60
    if _npc_end_hour_spin != null:
        _npc_end_hour_spin.value = end_min / 60
        _npc_end_minute_spin.value = end_min % 60
    if _npc_lateness_spin != null:
        _npc_lateness_spin.value = int(info.get("lateness_window_minutes", 0))
    var interval_raw = info.get("schedule_interval_hours", null)
    var start_raw = info.get("active_start_hour", null)
    var end_raw = info.get("active_end_hour", null)
    var has_cadence: bool = interval_raw != null and start_raw != null and end_raw != null
    if _npc_cadence_check != null:
        _npc_cadence_check.button_pressed = has_cadence
    if has_cadence:
        _npc_interval_spin.value = int(interval_raw)
        _npc_start_spin.value = int(start_raw)
        _npc_end_spin.value = int(end_raw)
    # Mirror the enable/disable state the toggle would set, since setting
    # button_pressed programmatically does fire `toggled` — but do it
    # explicitly in case future Godot changes that.
    _on_cadence_toggled(has_cadence)

    # Social-hour fields — same all-or-none shape as cadence. Minute
    # precision since ZBBS-071.
    var social_tag_raw = info.get("social_tag", null)
    var social_start_raw_min = info.get("social_start_minute", null)
    var social_end_raw_min = info.get("social_end_minute", null)
    var has_social: bool = social_tag_raw != null and social_tag_raw != "" and social_start_raw_min != null and social_end_raw_min != null
    if _npc_social_check != null:
        _npc_social_check.button_pressed = has_social
    if has_social and _npc_social_tag_dropdown != null:
        # Select the matching tag in the dropdown, or leave at index 0 if
        # the tag isn't in the list (e.g. it was removed from the allowlist
        # server-side since this NPC was configured).
        for i in range(_npc_social_tag_dropdown.item_count):
            if _npc_social_tag_dropdown.get_item_text(i) == str(social_tag_raw):
                _npc_social_tag_dropdown.select(i)
                break
        var social_start_min: int = int(social_start_raw_min)
        var social_end_min: int = int(social_end_raw_min)
        _npc_social_start_hour_spin.value = social_start_min / 60
        _npc_social_start_minute_spin.value = social_start_min % 60
        _npc_social_end_hour_spin.value = social_end_min / 60
        _npc_social_end_minute_spin.value = social_end_min % 60
    _on_social_toggled(has_social)

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
        # Terrain picker takes over — hide tab bar + catalog/villagers.
        _show_browse_surfaces()
        _set_tool_active(_select_button, false)
        # Deselect any asset
        if _selected_item != null:
            var old_style: StyleBoxFlat = _selected_item.get_meta("style_normal")
            _selected_item.add_theme_stylebox_override("panel", old_style)
            _selected_item = null
            _selected_asset_id = ""
    else:
        # Hide terrain picker; restore browse view unless something is selected.
        if not _selection_info_scroll.visible:
            _show_browse_surfaces()
        else:
            _terrain_picker.visible = false
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
        # Restore the active tab's view only if nothing is selected
        # (selection hides all browse surfaces).
        if not _selection_info_scroll.visible:
            _show_browse_surfaces()
        else:
            _terrain_picker.visible = false
        if _selected_terrain_item != null:
            var old_style: StyleBoxFlat = _selected_terrain_item.get_meta("style_normal")
            _selected_terrain_item.add_theme_stylebox_override("panel", old_style)
            _selected_terrain_item = null
