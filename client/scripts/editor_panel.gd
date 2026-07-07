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
signal npc_agent_changed(agent: String)
signal npc_home_structure_changed(structure_id: String)
signal npc_work_structure_changed(structure_id: String)
# Combined schedule change — all six fields sent together.
#   start_min / end_min: absolute work-window minutes-of-day (0–1439). Both
#     -1 means "inherit dawn/dusk" (sent to server as null/null). All-or-none
#     mirrors the schedule_window_all_or_none DB CHECK.
#   interval / start / end: rotation cadence triple. -1 means "no cadence".
signal npc_schedule_changed(start_min: int, end_min: int)
signal npc_home_assign_requested
signal npc_work_assign_requested
signal npc_sprite_change_requested(npc_id: String, current_sprite_id: String)
signal npc_select_requested(npc_id: String)
signal entry_policy_changed(object_id: String, policy: String)
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
const COLOR_COIN = Color(0.92, 0.78, 0.42, 1.0)  # gold, matches the coin chip

const PANEL_WIDTH: float = 240.0
const TOP_BAR_HEIGHT: float = 40.0
# Offset from the top bar to where this panel's top edge sits, sized so
# the panel butts up flush against the ticker's bottom border with no
# visible gap. Empirically 23 — close to TICKER_HEIGHT (24) in
# village_ticker.gd minus a one-pixel overlap that masks a sub-pixel
# rounding artifact in the marquee band's bottom edge.
const TICKER_HEIGHT: float = 23.0
const CELL_SIZE: int = 52  # Grid cell size — sprites render proportionally within

var _font: Font = null
# Lucide icon font — loaded alongside the body font so any control that
# wants an icon glyph (heart, X, gear, …) can swap fonts on a single
# Label without falling back to IMFellEnglish's missing-glyph tofu.
# Codepoints come from lucide-static/font/codepoints.json. Stored as
# ints + materialized via String.chr at use to dodge any source-file
# encoding wobble around private-use-area characters.
var _icon_font: Font = null
const ICON_CODEPOINT_HEART: int = 0xE0F2
const ICON_CODEPOINT_X: int = 0xE1B2
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
# Re-renders villager-row text (location + needs) in place while the list
# is visible (ZBBS-HOME-374). Movement has no per-step broadcast to hook,
# so the rows' "inside <structure>" line went stale once built.
var _villager_text_refresh_timer: Timer = null
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
var _entry_policy_dropdown: OptionButton = null
var _visible_when_inside_dropdown: OptionButton = null
# The asset_id the two above dropdowns currently reflect, so we know what
# to PATCH on change. Set in show_selection.
var _entry_policy_object_id: String = ""
# visible_when_inside is per-asset (it's a rendering property of the
# glyph), so the visible-when-inside dropdown still PATCHes by asset id.
# entry_policy is per-village_object (gameplay), so its dropdown PATCHes
# by object id. The two ids are tracked separately so the wrong endpoint
# can't be hit by accident.
var _visible_when_inside_asset_id: String = ""
var _ignoring_policy_dropdowns: bool = false
# People section — lists NPCs whose home/work structure is the currently
# selected asset. See _populate_people_section.
var _people_section: VBoxContainer = null
var _people_list: VBoxContainer = null
var _attachments_grid: GridContainer = null

# Asset-specific fields (owner / name input / attachments) live under this
# VBox so the whole block can be hidden when an NPC is selected instead.
var _asset_fields_section: VBoxContainer = null

# REFRESHES section (ZBBS-090) — per-instance attribute refresh rows on
# the selected village_object. Header + dynamic row stack + add/save
# controls. The lookup-table list is fetched lazily on first object
# selection; row state is fetched per-selection from
# GET /api/village/objects/{id}/refresh.
var _refreshes_section: VBoxContainer = null
var _refreshes_rows_box: VBoxContainer = null
var _refreshes_add_button: Button = null
var _refreshes_save_button: Button = null
var _refreshes_status: Label = null
var _refreshes_current_id: String = ""
# {name, display_label, sort_order} from /api/refresh-attributes. Loaded
# once and cached for the editor session — adding a new attribute is rare
# enough that a refresh-on-startup is fine.
var _refresh_attributes: Array = []
var _refresh_attributes_loaded: bool = false
# Per-row edit state. Each entry is a Dictionary with keys:
#   attribute (String), amount (int, restores-per-use, positive MAGNITUDE),
#   gather_item (String — the harvestable item this source yields; "" = not
#     a gather source), infinite (bool), available (int), max (int),
#   mode (String "continuous"|"periodic"), period (int hours)
# `amount` is held here as a positive magnitude ("restores per use") but the
# wire contract is NEGATIVE (the on-arrival decrement) — load negates the
# incoming amount into a magnitude, save negates it back. A yield-only gather
# source (attribute "" + gather_item set) carries magnitude 0 (wire amount 0).
# When `infinite` is true, available/max/mode/period are still tracked so
# unchecking the box restores the prior values, but the saved payload
# encodes them as null.
var _refresh_rows_state: Array = []

# Per-row edit dialog (modal). One reusable ConfirmationDialog (OK = Save,
# Cancel = discard) following editor.gd's _delete_dialog convention. The row
# being edited is _refresh_dialog_idx; its live form controls are held in _rd
# so _on_refresh_dialog_confirmed can read them back on OK.
var _refresh_dialog: ConfirmationDialog = null
var _refresh_dialog_content: VBoxContainer = null
var _refresh_dialog_idx: int = -1
var _rd: Dictionary = {}

# INVENTORY section (ZBBS-091) — per-NPC item rows in the NPC selection
# panel. Mirrors the Refreshes pattern: lookup-table list cached for
# the editor session, per-selection row state, whole-set save.
var _inventory_section: VBoxContainer = null
var _inventory_rows_box: VBoxContainer = null
var _inventory_add_button: Button = null
var _inventory_save_button: Button = null
var _inventory_status: Label = null
var _inventory_current_id: String = ""
# {name, display_label, category, satisfies: [{attribute, amount}, ...],
#  capabilities: [...], sort_order} from /api/items. Loaded once and
# cached. ZBBS-125 changed satisfies_* from a single pair of fields to
# an array; if you find an old reference to satisfies_attribute, it's
# stale.
var _items_catalog: Array = []
var _items_loaded: bool = false
# Per-row state. Each entry: {item_kind: String, quantity: int}.
var _inventory_rows_state: Array = []

# NPC-specific fields (behavior, linked llm_memory_agent). Mutually exclusive
# with _asset_fields_section.
var _npc_fields_section: VBoxContainer = null
var _npc_name_edit: LineEdit = null
# Attributes section — replaces the single-behavior dropdown (ZBBS-105).
# Multi-select chip list backed by actor_attribute. Add dropdown lists
# allowlist slugs not already on the NPC. Chips render with an X to
# remove. Add/remove call the new POST/DELETE attribute endpoints
# directly via world.gd; the WS event npc_attributes_changed refreshes
# the chips after the round-trip lands.
var _npc_attributes_chips_box: HBoxContainer = null
var _npc_attributes_add_dropdown: OptionButton = null
var _npc_attributes_current_id: String = ""
var _npc_attributes_current_list: Array = []
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
# Needs readout (ZBBS-082) — three labels showing current value / max
# (hunger, thirst, tiredness).
var _npc_hunger_label: Label = null
var _npc_thirst_label: Label = null
var _npc_tiredness_label: Label = null
# Schedule section — absolute work-window pair (HH:MM start / HH:MM end)
# + cadence triple. The start/end pair is nullable: when both
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
var _npc_schedule_save_button: Button = null

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
    _icon_font = load("res://assets/fonts/lucide.ttf")

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
    offset_top = TOP_BAR_HEIGHT + TICKER_HEIGHT
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
    # ScrollContainer so long selections (NPC with all schedule
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

    # REFRESHES section (ZBBS-090). Per-instance refresh rows for the
    # selected object. Empty by default for new placements; admin adds
    # rows as needed. Save sends the whole set in one PUT.
    _refreshes_section = VBoxContainer.new()
    _refreshes_section.add_theme_constant_override("separation", 4)
    _asset_fields_section.add_child(_refreshes_section)

    var refreshes_header := Label.new()
    refreshes_header.text = "REFRESHES"
    refreshes_header.add_theme_color_override("font_color", COLOR_LABEL)
    refreshes_header.add_theme_font_size_override("font_size", 11)
    _refreshes_section.add_child(refreshes_header)

    _refreshes_rows_box = VBoxContainer.new()
    _refreshes_rows_box.add_theme_constant_override("separation", 6)
    _refreshes_section.add_child(_refreshes_rows_box)

    var refreshes_btn_row := HBoxContainer.new()
    refreshes_btn_row.add_theme_constant_override("separation", 6)
    _refreshes_section.add_child(refreshes_btn_row)

    _refreshes_add_button = _make_refreshes_button("+ Add refresh")
    _refreshes_add_button.pressed.connect(_on_refresh_add_pressed)
    refreshes_btn_row.add_child(_refreshes_add_button)

    _refreshes_save_button = _make_refreshes_button("Save")
    _refreshes_save_button.pressed.connect(_on_refreshes_save_pressed)
    refreshes_btn_row.add_child(_refreshes_save_button)

    _refreshes_status = Label.new()
    _refreshes_status.text = ""
    _refreshes_status.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _refreshes_status.add_theme_font_size_override("font_size", 11)
    _refreshes_section.add_child(_refreshes_status)

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

    # Per-instance entry_policy (ZBBS-101). Four states matching the engine
    # EntryPolicy enum: 'closed' (no entry, used for decoratives), 'owner-only'
    # (only NPCs whose home or work is this structure), 'open' (public access),
    # and '' (the type-driven default). Same visual style as the owner /
    # behavior pickers.
    var entry_policy_header = Label.new()
    entry_policy_header.text = "ENTRY POLICY"
    entry_policy_header.add_theme_color_override("font_color", COLOR_LABEL)
    entry_policy_header.add_theme_font_size_override("font_size", 11)
    _asset_fields_section.add_child(entry_policy_header)

    # Match the owner dropdown's stylebox exactly — warmer brown bg,
    # rounded corners, slightly larger padding — so the two pickers read
    # as part of the same family.
    var entry_policy_style = StyleBoxFlat.new()
    entry_policy_style.bg_color = COLOR_BTN_BG
    entry_policy_style.border_width_left = 1
    entry_policy_style.border_width_top = 1
    entry_policy_style.border_width_right = 1
    entry_policy_style.border_width_bottom = 1
    entry_policy_style.border_color = COLOR_BTN_BORDER
    entry_policy_style.corner_radius_left_top = 3
    entry_policy_style.corner_radius_right_top = 3
    entry_policy_style.corner_radius_left_bottom = 3
    entry_policy_style.corner_radius_right_bottom = 3
    entry_policy_style.content_margin_left = 6.0
    entry_policy_style.content_margin_right = 6.0
    entry_policy_style.content_margin_top = 4.0
    entry_policy_style.content_margin_bottom = 4.0

    _entry_policy_dropdown = OptionButton.new()
    _entry_policy_dropdown.add_theme_font_override("font", _font)
    _entry_policy_dropdown.add_theme_font_size_override("font_size", 13)
    _entry_policy_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    _entry_policy_dropdown.add_theme_stylebox_override("normal", entry_policy_style)
    # item_id matches the slot order so we can index by selected; the
    # actual policy string is set as item metadata for type safety.
    _entry_policy_dropdown.add_item("Cannot be entered", 0)
    _entry_policy_dropdown.set_item_metadata(0, "closed")
    _entry_policy_dropdown.add_item("Owner only", 1)
    _entry_policy_dropdown.set_item_metadata(1, "owner-only")
    _entry_policy_dropdown.add_item("Anyone", 2)
    _entry_policy_dropdown.set_item_metadata(2, "open")
    # "" is the engine's type-driven default — enterable like "open" at
    # runtime, but kept distinct so a placement can be left unset rather
    # than pinned to an explicit policy. Newly-placed objects arrive here.
    _entry_policy_dropdown.add_item("Default", 3)
    _entry_policy_dropdown.set_item_metadata(3, "")
    _entry_policy_dropdown.item_selected.connect(_on_entry_policy_selected)
    _asset_fields_section.add_child(_entry_policy_dropdown)

    # Visible-when-inside — see-through buildings (market stall) keep the
    # villager sprite visible at the door tile. Per-asset since rendering
    # is a property of the glyph; entry_policy is the per-instance
    # gameplay knob that lives next to it.
    var visible_header = Label.new()
    visible_header.text = "VISIBLE WHEN INSIDE"
    visible_header.add_theme_color_override("font_color", COLOR_LABEL)
    visible_header.add_theme_font_size_override("font_size", 11)
    _asset_fields_section.add_child(visible_header)

    _visible_when_inside_dropdown = OptionButton.new()
    _visible_when_inside_dropdown.add_theme_font_override("font", _font)
    _visible_when_inside_dropdown.add_theme_font_size_override("font_size", 13)
    _visible_when_inside_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    _visible_when_inside_dropdown.add_theme_stylebox_override("normal", entry_policy_style)
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

    # Shared stylebox for the agent dropdown + the attribute add-row's
    # dropdown and button below. Declared once here, reused everywhere
    # this NPC fields section needs the standard dark-on-tan picker look.
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

    var npc_attributes_header = Label.new()
    npc_attributes_header.text = "ATTRIBUTES"
    npc_attributes_header.add_theme_color_override("font_color", COLOR_LABEL)
    npc_attributes_header.add_theme_font_size_override("font_size", 11)
    _npc_fields_section.add_child(npc_attributes_header)

    _npc_attributes_chips_box = HBoxContainer.new()
    _npc_attributes_chips_box.add_theme_constant_override("separation", 4)
    _npc_fields_section.add_child(_npc_attributes_chips_box)

    var attr_add_row = HBoxContainer.new()
    attr_add_row.add_theme_constant_override("separation", 6)
    _npc_fields_section.add_child(attr_add_row)

    _npc_attributes_add_dropdown = OptionButton.new()
    _npc_attributes_add_dropdown.add_theme_font_override("font", _font)
    _npc_attributes_add_dropdown.add_theme_font_size_override("font_size", 12)
    _npc_attributes_add_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_attributes_add_dropdown.add_theme_stylebox_override("normal", behavior_style)
    _npc_attributes_add_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    attr_add_row.add_child(_npc_attributes_add_dropdown)

    var attr_add_btn = Button.new()
    attr_add_btn.text = "Add"
    attr_add_btn.add_theme_font_override("font", _font)
    attr_add_btn.add_theme_font_size_override("font_size", 12)
    attr_add_btn.add_theme_color_override("font_color", COLOR_TEXT)
    attr_add_btn.add_theme_stylebox_override("normal", behavior_style)
    attr_add_btn.pressed.connect(_on_npc_attribute_add_pressed)
    attr_add_row.add_child(attr_add_btn)

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

    # Needs readout (ZBBS-082) — current hunger/thirst/tiredness in [0, 24].
    var npc_needs_header = Label.new()
    npc_needs_header.text = "NEEDS"
    npc_needs_header.add_theme_color_override("font_color", COLOR_LABEL)
    npc_needs_header.add_theme_font_size_override("font_size", 11)
    _npc_fields_section.add_child(npc_needs_header)

    _npc_hunger_label = Label.new()
    _npc_hunger_label.add_theme_font_override("font", _font)
    _npc_hunger_label.add_theme_font_size_override("font_size", 12)
    _npc_hunger_label.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_fields_section.add_child(_npc_hunger_label)

    _npc_thirst_label = Label.new()
    _npc_thirst_label.add_theme_font_override("font", _font)
    _npc_thirst_label.add_theme_font_size_override("font_size", 12)
    _npc_thirst_label.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_fields_section.add_child(_npc_thirst_label)

    _npc_tiredness_label = Label.new()
    _npc_tiredness_label.add_theme_font_override("font", _font)
    _npc_tiredness_label.add_theme_font_size_override("font_size", 12)
    _npc_tiredness_label.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_fields_section.add_child(_npc_tiredness_label)

    # INVENTORY section (ZBBS-091). Per-actor item rows with quantity
    # spinners. Empty by default; admin adds rows from the item picker.
    # Save sends the whole set in one PUT (whole-set replace, mirrors
    # the Refreshes panel pattern).
    _inventory_section = VBoxContainer.new()
    _inventory_section.add_theme_constant_override("separation", 4)
    _npc_fields_section.add_child(_inventory_section)

    var inventory_header := Label.new()
    inventory_header.text = "INVENTORY"
    inventory_header.add_theme_color_override("font_color", COLOR_LABEL)
    inventory_header.add_theme_font_size_override("font_size", 11)
    _inventory_section.add_child(inventory_header)

    _inventory_rows_box = VBoxContainer.new()
    _inventory_rows_box.add_theme_constant_override("separation", 4)
    _inventory_section.add_child(_inventory_rows_box)

    var inv_btn_row := HBoxContainer.new()
    inv_btn_row.add_theme_constant_override("separation", 6)
    _inventory_section.add_child(inv_btn_row)

    _inventory_add_button = _make_refreshes_button("+ Add item")
    _inventory_add_button.pressed.connect(_on_inventory_add_pressed)
    inv_btn_row.add_child(_inventory_add_button)

    _inventory_save_button = _make_refreshes_button("Save")
    _inventory_save_button.pressed.connect(_on_inventory_save_pressed)
    inv_btn_row.add_child(_inventory_save_button)

    _inventory_status = Label.new()
    _inventory_status.text = ""
    _inventory_status.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _inventory_status.add_theme_font_size_override("font_size", 11)
    _inventory_section.add_child(_inventory_status)

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

    _npc_schedule_save_button = Button.new()
    _npc_schedule_save_button.text = "Save Schedule"
    _npc_schedule_save_button.add_theme_font_override("font", _font)
    _npc_schedule_save_button.add_theme_font_size_override("font_size", 13)
    _npc_schedule_save_button.add_theme_color_override("font_color", COLOR_TEXT)
    _npc_schedule_save_button.add_theme_stylebox_override("normal", behavior_style)
    _npc_schedule_save_button.pressed.connect(_on_schedule_save_pressed)
    _npc_schedule_section.add_child(_npc_schedule_save_button)

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

    # ZBBS-HOME-374: the rows' location/needs text is rendered once at
    # build time, and NPC movement emits no metadata broadcast to rebuild
    # on — so a row showed "inside the Tavern" long after the NPC walked
    # out. A 2s in-place text refresh while the list is visible keeps the
    # rows honest without the scroll-position reset a full rebuild causes.
    _villager_text_refresh_timer = Timer.new()
    _villager_text_refresh_timer.wait_time = 2.0
    _villager_text_refresh_timer.autostart = true
    _villager_text_refresh_timer.timeout.connect(_refresh_villager_rows_text)
    add_child(_villager_text_refresh_timer)

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

## The explicit Save button commits the displayed window as a concrete
## schedule even when the admin never touched a spinner (the fields were
## prepopulated from the dawn/dusk defaults). Flipping the NULL flag false
## first is what makes the emit send real minutes. Without it, saving an
## untouched NULL-inheriting NPC re-sent null/null — a silent no-op that
## left a keeper unscheduled (and, for a non-worker keeper, permanently
## off-shift and asleep at home).
func _on_schedule_save_pressed() -> void:
    if _ignoring_npc_inputs:
        return
    _npc_schedule_window_is_null = false
    _emit_schedule_changed()

## Auto-save on any SpinBox value change for fields that don't affect the
## window's NULL state (cadence). Matches the behavior / home /
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

## Shared emitter for all three auto-save paths. Sentinel contract: emits
## -1/-1 to clear the schedule (NULL-inherit dawn/dusk); otherwise concrete
## minutes. The downstream handler converts -1 to null in the PATCH payload.
func _emit_schedule_changed() -> void:
    if _ignoring_npc_inputs:
        return
    var start_min: int = -1
    var end_min: int = -1
    if not _npc_schedule_window_is_null:
        start_min = int(_npc_start_hour_spin.value) * 60 + int(_npc_start_minute_spin.value)
        end_min = int(_npc_end_hour_spin.value) * 60 + int(_npc_end_minute_spin.value)
    npc_schedule_changed.emit(start_min, end_min)

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

## Refresh the chip list + add-dropdown for the currently-selected NPC's
## attributes. Mirrors _refresh_obj_tags_ui — empty list shows a "(none)"
## placeholder; the add dropdown lists allowlist slugs not already on
## the NPC, sorted by display name from Catalog.npc_behaviors.
func _refresh_npc_attributes_ui() -> void:
    if _npc_attributes_chips_box == null:
        return
    for child in _npc_attributes_chips_box.get_children():
        child.queue_free()
    if _npc_attributes_current_list.size() == 0:
        var empty = Label.new()
        empty.text = "(none)"
        empty.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        empty.add_theme_font_size_override("font_size", 11)
        _npc_attributes_chips_box.add_child(empty)
    else:
        for slug in _npc_attributes_current_list:
            var display: String = _attribute_display_name(str(slug))
            _npc_attributes_chips_box.add_child(_build_npc_attribute_chip(str(slug), display))

    if _npc_attributes_add_dropdown == null:
        return
    _npc_attributes_add_dropdown.clear()
    var current_set: Dictionary = {}
    for s in _npc_attributes_current_list:
        current_set[str(s)] = true
    var idx: int = 0
    for entry in Catalog.npc_behaviors:
        var slug: String = str(entry.get("slug", ""))
        if slug == "" or current_set.has(slug):
            continue
        _npc_attributes_add_dropdown.add_item(str(entry.get("display_name", slug)), idx)
        _npc_attributes_add_dropdown.set_item_metadata(idx, slug)
        idx += 1

## Look up the display name for an attribute slug from Catalog.npc_behaviors.
## Falls back to the slug itself if the attribute isn't in the loaded list
## (race during first login, or the catalog hasn't refreshed yet).
func _attribute_display_name(slug: String) -> String:
    for entry in Catalog.npc_behaviors:
        if str(entry.get("slug", "")) == slug:
            return str(entry.get("display_name", slug))
    return slug

## Build a removable chip for a single attribute. Same pill shape as
## _build_tag_chip (object tags); the X is an ASCII button rather than a
## unicode glyph because IMFellEnglish lacks U+2715.
func _build_npc_attribute_chip(slug: String, display: String) -> Control:
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
    label.text = display
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
    x_btn.pressed.connect(_on_npc_attribute_chip_pressed.bind(slug))
    row.add_child(x_btn)

    return pill

func _on_npc_attribute_add_pressed() -> void:
    if _npc_attributes_add_dropdown == null or _npc_attributes_add_dropdown.selected < 0:
        return
    if _npc_attributes_current_id == "":
        return
    var slug: String = _npc_attributes_add_dropdown.get_item_metadata(_npc_attributes_add_dropdown.selected)
    if slug == "" or world == null:
        return
    world.add_npc_attribute(_npc_attributes_current_id, slug)

func _on_npc_attribute_chip_pressed(slug: String) -> void:
    if _npc_attributes_current_id == "" or world == null:
        return
    world.remove_npc_attribute(_npc_attributes_current_id, slug)

## Called by main.gd when the WS npc_attributes_changed event lands (via
## world.npc_attributes_changed). Refresh the chips if this is the
## currently-selected NPC.
func apply_npc_attributes_external(npc_id: String, attributes: Array) -> void:
    if _npc_attributes_current_id != npc_id:
        return
    _npc_attributes_current_list = attributes
    _refresh_npc_attributes_ui()

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
    # Item metadata is the owner actor GUID (or "" for "No owner") — LLM-122.
    var owner_id: String = _owner_dropdown.get_item_metadata(index)
    owner_changed.emit(owner_id)

    # Update the owner label immediately
    if owner_id != "":
        var display_name: String = owner_id
        if world != null:
            display_name = world.get_owner_display_name(owner_id)
        _owner_label.text = "Owner: " + display_name
        _owner_label.visible = true
    else:
        _owner_label.visible = false

func _on_entry_policy_selected(index: int) -> void:
    if _ignoring_policy_dropdowns or _entry_policy_object_id == "":
        return
    var policy: String = str(_entry_policy_dropdown.get_item_metadata(index))
    entry_policy_changed.emit(_entry_policy_object_id, policy)

func _on_visible_when_inside_selected(index: int) -> void:
    # _visible_when_inside_asset_id holds the asset id for the (still
    # per-asset) visible-when-inside PATCH; show_selection assigns it
    # alongside _entry_policy_object_id when a placement is selected.
    if _ignoring_policy_dropdowns or _visible_when_inside_asset_id == "":
        return
    asset_visible_when_inside_toggled.emit(_visible_when_inside_asset_id, index == 1)

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
## LLM-attached villagers sort first (they're the ones we actually care
## about during a debugging pass); within each group, alphabetical by
## display_name with unnamed NPCs at the bottom under "(unnamed)".
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
        var has_llm: bool = str(container.get_meta("llm_memory_agent", "")) != ""
        npc_entries.append({
            "id": str(npc_id),
            "sort_name": sort_name,
            "display_name": sort_name,
            "container": container,
            "has_llm": has_llm,
        })
    # Two-key sort: LLM-attached first (true > false), then alphabetical
    # within each group. The compare returns true when a should come
    # before b.
    npc_entries.sort_custom(func(a, b):
        if a["has_llm"] != b["has_llm"]:
            return a["has_llm"]
        return a["sort_name"].to_lower() < b["sort_name"].to_lower()
    )

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

    # Top line: name plus an inline (behavior) suffix at the smaller
    # secondary font/color. Two Labels in an HBox so the two segments
    # can carry different sizes and colors. The behavior segment hides
    # when the NPC has no behavior — no parens, no empty space.
    var name_row := HBoxContainer.new()
    name_row.add_theme_constant_override("separation", 4)
    name_row.mouse_filter = Control.MOUSE_FILTER_IGNORE
    vb.add_child(name_row)

    var name_label := Label.new()
    name_label.text = display_name
    name_label.add_theme_font_override("font", _font)
    name_label.add_theme_font_size_override("font_size", 13)
    name_label.add_theme_color_override("font_color", COLOR_TEXT)
    name_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    name_row.add_child(name_label)

    var behavior_inline_label := Label.new()
    behavior_inline_label.add_theme_font_override("font", _font)
    # Sub-line size is 12, not 11 — IMFellEnglish has irregular widths
    # and renders raggedly at very small sizes (visible gaps inside
    # words). 12 still reads as secondary against the 13 primary line.
    behavior_inline_label.add_theme_font_size_override("font_size", 12)
    behavior_inline_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    behavior_inline_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    name_row.add_child(behavior_inline_label)

    var location_label := Label.new()
    location_label.add_theme_font_override("font", _font)
    location_label.add_theme_font_size_override("font_size", 12)
    location_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    location_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    vb.add_child(location_label)

    # Needs line: three Labels in an HBox so each can color red
    # independently when its value crosses the engine's red threshold.
    # Format is "Hunger N" / "Thirst N" / "Tiredness N" — full words
    # at the request of the operator; if it ever overflows the panel
    # width we'll switch to abbreviations.
    var needs_row := HBoxContainer.new()
    needs_row.add_theme_constant_override("separation", 8)
    needs_row.mouse_filter = Control.MOUSE_FILTER_IGNORE
    vb.add_child(needs_row)

    var needs_hunger_label := Label.new()
    needs_hunger_label.add_theme_font_override("font", _font)
    needs_hunger_label.add_theme_font_size_override("font_size", 12)
    needs_hunger_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    needs_row.add_child(needs_hunger_label)

    var needs_thirst_label := Label.new()
    needs_thirst_label.add_theme_font_override("font", _font)
    needs_thirst_label.add_theme_font_size_override("font_size", 12)
    needs_thirst_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    needs_row.add_child(needs_thirst_label)

    var needs_tiredness_label := Label.new()
    needs_tiredness_label.add_theme_font_override("font", _font)
    needs_tiredness_label.add_theme_font_size_override("font_size", 12)
    needs_tiredness_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    needs_row.add_child(needs_tiredness_label)

    var needs_coins_label := Label.new()
    needs_coins_label.add_theme_font_override("font", _font)
    needs_coins_label.add_theme_font_size_override("font_size", 12)
    needs_coins_label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    needs_row.add_child(needs_coins_label)

    row.set_meta("name_label", name_label)
    row.set_meta("behavior_inline_label", behavior_inline_label)
    row.set_meta("location_label", location_label)
    row.set_meta("needs_hunger_label", needs_hunger_label)
    row.set_meta("needs_thirst_label", needs_thirst_label)
    row.set_meta("needs_tiredness_label", needs_tiredness_label)
    row.set_meta("needs_coins_label", needs_coins_label)

    _update_villager_row_text(row, container)

    row.mouse_entered.connect(func(): _on_villager_row_hover(row, true))
    row.mouse_exited.connect(func(): _on_villager_row_hover(row, false))
    row.gui_input.connect(func(ev): _on_villager_row_gui_input(ev, npc_id))
    return row

## Format the role suffix, location, and needs readout on a villager
## row. Role suffix joins every attribute slug the NPC carries —
## "(town_crier, worker)" rather than just the first — so the list
## reflects the full chip set the inspector shows on click. Slugs are
## str()-coerced and empties dropped before the join so a JSON-decoded
## Variant array with nulls or non-strings doesn't crash the
## formatter.
func _update_villager_row_text(row: PanelContainer, container: Node2D) -> void:
    var attrs_raw = container.get_meta("attributes", [])
    var attrs: Array = attrs_raw if attrs_raw is Array else []
    var attr_labels: Array[String] = []
    for attr in attrs:
        var slug := str(attr).strip_edges()
        if slug != "":
            attr_labels.append(slug)
    var role_label: Label = row.get_meta("behavior_inline_label")
    if role_label != null:
        if attr_labels.size() > 0:
            role_label.text = "(" + ", ".join(attr_labels) + ")"
            role_label.visible = true
        else:
            role_label.text = ""
            role_label.visible = false
    var location_label: Label = row.get_meta("location_label")
    if location_label != null:
        location_label.text = _format_npc_location(container)
    # Needs readout — same red thresholds the NPC selection panel uses
    # (see _format_need_label). Each label colors independently so
    # only the actually-distressed needs draw the eye.
    var hunger_val: int = int(container.get_meta("hunger", 0))
    var thirst_val: int = int(container.get_meta("thirst", 0))
    var tiredness_val: int = int(container.get_meta("tiredness", 0))
    _set_villager_need_label(row.get_meta("needs_hunger_label"), "Hunger", hunger_val, 18)
    _set_villager_need_label(row.get_meta("needs_thirst_label"), "Thirst", thirst_val, 12)
    _set_villager_need_label(row.get_meta("needs_tiredness_label"), "Tiredness", tiredness_val, 20)
    var coins_val: int = int(container.get_meta("coins", 0))
    _set_villager_coins_label(row.get_meta("needs_coins_label"), coins_val)

## Render one need on the villager-list row. Same red-threshold cue as
## the selection panel — the value crossing red recolors the whole "Name N"
## segment so distress reads at a glance during a list scan.
func _set_villager_need_label(label: Label, name: String, value: int, red_threshold: int) -> void:
    if label == null:
        return
    label.text = "%s %d" % [name, value]
    if value >= red_threshold:
        label.add_theme_color_override("font_color", Color(0.95, 0.45, 0.45))
    else:
        label.add_theme_color_override("font_color", COLOR_TEXT_DIM)

## Render the coin balance on the villager-list row. Coins is not a need, so
## there's no distress threshold — it always draws in coin-chip gold so the
## value reads as money rather than something to worry about.
func _set_villager_coins_label(label: Label, value: int) -> void:
    if label == null:
        return
    label.text = "Coins %d" % value
    label.add_theme_color_override("font_color", COLOR_COIN)

## Timer-driven in-place refresh of every villager row's location + needs
## text (ZBBS-HOME-374). Structural changes (rename / attribute / create /
## delete) still rebuild the whole list via npc_metadata_changed; this pass
## covers what that signal never fires for — plain movement — and writes
## only label text, so selection highlight and scroll position survive.
## No-op while the Villagers tab (or the whole editor panel) is hidden.
func _refresh_villager_rows_text() -> void:
    if world == null or _villagers_scroll == null or not _villagers_scroll.is_visible_in_tree():
        return
    for npc_id in _villager_rows:
        var row = _villager_rows[npc_id]
        if row == null or not is_instance_valid(row):
            continue
        var container = world.placed_npcs.get(npc_id, null)
        if container == null or not is_instance_valid(container):
            continue
        _update_villager_row_text(row, container)

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
    if str(asset.get("category", "")) != "structure":
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
        # they don't live or work here — an admin selecting that structure
        # expects to see who's actually there, not just who belongs there.
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
        _clear_refreshes_panel()
        _clear_inventory_panel()
        _show_browse_surfaces()  # Restore the active tab's view on deselect
        return
    _selection_info_scroll.visible = true
    _asset_fields_section.visible = true
    _npc_fields_section.visible = false
    _clear_inventory_panel()
    _delete_button.disabled = false
    _hide_browse_surfaces()  # Selection inspector takes over the content region
    var asset = Catalog.assets.get(asset_id, {})
    var name: String = asset.get("name", asset_id)
    _selection_label.text = name

    # Show attachments if this asset has slots
    _build_attachments(asset_id)

    # Sync the entry-policy dropdown (per-instance) and the
    # visible-when-inside dropdown (per-asset, retained on the same panel
    # because the two settings are conceptually adjacent).
    _ignoring_policy_dropdowns = true
    _entry_policy_object_id = info.get("object_id", "")
    # entry_policy arrives as the engine EntryPolicy enum. The default
    # policy ("") is omitted from the snapshot (omitempty) and surfaces
    # here as the "none" sentinel from world.gd; both map to the "Default"
    # item (index 3). Any other unmatched value falls through to index 0.
    var policy: String = str(info.get("entry_policy", "none"))
    var policy_index: int = 0
    match policy:
        "closed":
            policy_index = 0
        "owner-only":
            policy_index = 1
        "open":
            policy_index = 2
        "", "none":
            policy_index = 3
    _entry_policy_dropdown.selected = policy_index
    _visible_when_inside_asset_id = asset_id
    if _visible_when_inside_dropdown != null:
        _visible_when_inside_dropdown.selected = 1 if bool(asset.get("visible_when_inside", false)) else 0
    _ignoring_policy_dropdowns = false

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

    # Populate owner dropdown from the full actor list. Any actor can own an object
    # (owner_actor_id is a per-actor GUID), so list every actor by id — not the
    # VA-slug agent_list, which collapsed shared-VA actors and hid all but one
    # (e.g. Moses James was unselectable behind Elizabeth Ellis). LLM-122.
    _ignoring_dropdown = true
    _owner_dropdown.clear()
    _owner_dropdown.add_item("No owner", 0)
    _owner_dropdown.set_item_metadata(0, "")
    var selected_index: int = 0
    if world != null:
        var idx: int = 1
        for actor_id in world.actor_list:
            var display: String = world.actor_names.get(actor_id, actor_id)
            _owner_dropdown.add_item(display, idx)
            _owner_dropdown.set_item_metadata(idx, actor_id)
            if actor_id == owner:
                selected_index = idx
            idx += 1
    _owner_dropdown.selected = selected_index
    _ignoring_dropdown = false

    # Per-instance tags for the selected object.
    _obj_tags_current_id = info.get("object_id", "")
    var tags_raw = info.get("tags", [])
    _obj_tags_current_list = tags_raw if tags_raw is Array else []
    _refresh_obj_tags_ui()

    # Per-instance refresh rows (ZBBS-090). v2 has no standalone GET — the
    # rows ride the ObjectDTO (set on the object node's "refreshes" meta), so
    # read them straight off the selection info rather than fetching.
    var refreshes_raw = info.get("refreshes", [])
    _show_refreshes_for_object(info.get("object_id", ""), refreshes_raw if refreshes_raw is Array else [])

## Called by editor when an NPC is selected/deselected. Reuses the selection
## panel but swaps to NPC-only fields (no owner, no attachments, no delete
## until placement/delete ships in a follow-up).
func show_npc_selection(info: Dictionary) -> void:
    var npc_id: String = info.get("npc_id", "")
    if npc_id == "":
        _selection_info_scroll.visible = false
        _npc_fields_section.visible = false
        _clear_refreshes_panel()
        _clear_inventory_panel()
        sync_villager_selection("")
        _show_browse_surfaces()
        return
    _selection_info_scroll.visible = true
    _asset_fields_section.visible = false
    _clear_refreshes_panel()
    _npc_fields_section.visible = true
    _show_inventory_for_actor(npc_id)
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

    # Populate the attribute chip list from info["attributes"] (post-ZBBS-105
    # multi-attribute shape). The legacy behavior field is kept on the wire
    # for compatibility but ignored here; the chip list is the source of
    # truth in the UI. _refresh_npc_attributes_ui rebuilds chips and the
    # add-dropdown together.
    _npc_attributes_current_id = npc_id
    var attrs_raw = info.get("attributes", [])
    _npc_attributes_current_list = attrs_raw if attrs_raw is Array else []
    _refresh_npc_attributes_ui()

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

    # Needs readout — current values from the actor row, refreshed on
    # selection and via npc_metadata_changed when the WS broadcasts a
    # reset. _format_need_label colors the value red when it crosses
    # the engine's default red threshold for that need. Operator-tuned
    # thresholds (hunger_red_threshold, etc.) are NOT consulted here —
    # the panel can drift from the chronicler's actual distress filter
    # if those settings are edited. Visual hint only; the source of
    # truth for "in distress" stays server-side.
    var hunger_val: int = int(info.get("hunger", 0))
    var thirst_val: int = int(info.get("thirst", 0))
    var tiredness_val: int = int(info.get("tiredness", 0))
    if _npc_hunger_label != null:
        _format_need_label(_npc_hunger_label, "Hunger", hunger_val, 18)
    if _npc_thirst_label != null:
        _format_need_label(_npc_thirst_label, "Thirst", thirst_val, 12)
    if _npc_tiredness_label != null:
        _format_need_label(_npc_tiredness_label, "Tiredness", tiredness_val, 20)

    _ignoring_npc_inputs = false

## Render a single need's label as "Name: value / 24" with a red font
## color when value is at or above the red threshold. Mirrors the
## chronicler's visual cue for "in distress" without making a server
## roundtrip per selection.
func _format_need_label(label: Label, name: String, value: int, red_threshold: int) -> void:
    label.text = "  %s: %d / 24" % [name, value]
    if value >= red_threshold:
        label.add_theme_color_override("font_color", Color(0.95, 0.45, 0.45))
    else:
        label.add_theme_color_override("font_color", COLOR_TEXT)

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


# =====================================================================
# REFRESHES (ZBBS-090)
# =====================================================================
#
# Per-instance refresh rows on the selected village_object. The section
# UI is built in _build_panel; state lives in _refresh_rows_state and the
# attribute lookup-table list in _refresh_attributes (fetched lazily).
#
# Lifecycle:
#   - Editor selects an object → show_for_object calls _show_refreshes_for_object.
#   - _show_refreshes_for_object ensures the attribute list is loaded, then
#     fetches the row set for the selected object and renders it.
#   - Operator edits rows in-place; changes mutate _refresh_rows_state.
#     Save sends the whole set to PUT /api/village/objects/{id}/refresh.
#   - Selection cleared → _clear_refreshes_panel empties state and DOM.

func _make_refreshes_button(text: String) -> Button:
    var btn := Button.new()
    btn.text = text
    btn.add_theme_color_override("font_color", COLOR_TEXT)
    btn.add_theme_font_override("font", _font)
    btn.add_theme_font_size_override("font_size", 12)
    var s := StyleBoxFlat.new()
    s.bg_color = COLOR_BTN_BG
    s.border_width_left = 1
    s.border_width_top = 1
    s.border_width_right = 1
    s.border_width_bottom = 1
    s.border_color = COLOR_BTN_BORDER
    s.corner_radius_left_top = 3
    s.corner_radius_right_top = 3
    s.corner_radius_left_bottom = 3
    s.corner_radius_right_bottom = 3
    s.content_margin_left = 8.0
    s.content_margin_right = 8.0
    s.content_margin_top = 3.0
    s.content_margin_bottom = 3.0
    btn.add_theme_stylebox_override("normal", s)
    return btn

## Show the Refreshes section for the given object_id. The rows come straight
## from the ObjectDTO (passed in by show_selection); v2 has no standalone GET.
## The attribute catalog (dropdown labels for the edit dialog) is lazy-loaded
## once on first selection.
func _show_refreshes_for_object(object_id: String, refreshes: Array) -> void:
    _refreshes_current_id = object_id
    _load_refresh_rows_from(refreshes)
    _set_refreshes_status("", false)
    _render_refresh_rows()
    if object_id == "":
        return
    if not _refresh_attributes_loaded:
        _fetch_refresh_attributes()
    # The refresh edit dialog's gather-item dropdown is built from the item
    # catalog (same source as the NPC inventory dropdown). It's fetched lazily
    # on NPC selection today; an object selection needs it too. Passing "" means
    # "just load the catalog" — no inventory follow-up (see _fetch_items_catalog).
    if not _items_loaded:
        _fetch_items_catalog("")

## Parse a refresh set (the ObjectDTO's "refreshes" array, or the echo from a
## set-refresh save) into _refresh_rows_state. dwell_delta/dwell_period_minutes
## travel together — both null means "no dwell recovery"; available/max travel
## together — null means infinite supply.
func _load_refresh_rows_from(refreshes: Array) -> void:
    _refresh_rows_state.clear()
    for entry in refreshes:
        if not (entry is Dictionary):
            continue
        var available_q = entry.get("available_quantity", null)
        var max_q = entry.get("max_quantity", null)
        var period = entry.get("refresh_period_hours", null)
        var infinite := available_q == null
        var dwell_delta = entry.get("dwell_delta", null)
        var dwell_period = entry.get("dwell_period_minutes", null)
        var has_dwell := dwell_delta != null
        # Wire amount is NEGATIVE (on-arrival decrement) or 0 (yield-only). The
        # panel edits a positive magnitude, so negate on the way in: -8 -> 8,
        # 0 -> 0. A pre-negative-contract row that somehow carried a positive
        # amount would flip to negative here, but the server only ever emits <= 0.
        _refresh_rows_state.append({
            "attribute":    str(entry.get("attribute", "")),
            "amount":       -int(entry.get("amount", 0)),
            "gather_item":  str(entry.get("gather_item", "")),
            "infinite":     infinite,
            "available":    (int(available_q) if not infinite else 1),
            "max":          (int(max_q) if not infinite else 10),
            "mode":         str(entry.get("refresh_mode", "continuous")),
            "period":       (int(period) if period != null else 24),
            "has_dwell":    has_dwell,
            "dwell_delta":  (int(dwell_delta) if has_dwell else -1),
            "dwell_period": (int(dwell_period) if (has_dwell and dwell_period != null) else 10),
        })

func _clear_refreshes_panel() -> void:
    _reset_refresh_dialog()
    _refreshes_current_id = ""
    _refresh_rows_state.clear()
    _set_refreshes_status("", false)
    _render_refresh_rows()

## Close + reset the reusable edit dialog. Call whenever _refresh_rows_state is
## replaced out from under an open dialog (deselect, save-echo reload) so a
## later confirm can't write stale cached controls onto a fresh row.
func _reset_refresh_dialog() -> void:
    if _refresh_dialog != null and _refresh_dialog.visible:
        _refresh_dialog.hide()
    _refresh_dialog_idx = -1
    _rd = {}

func _fetch_refresh_attributes() -> void:
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_refresh_attributes_response.bind(http))
    var headers := Auth.auth_headers(false)
    http.request(Auth.api_base + "/api/village/refresh-attributes", headers)

func _on_refresh_attributes_response(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        _set_refreshes_status("Failed to load attribute list (" + str(response_code) + ")", true)
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if not (json is Array):
        _set_refreshes_status("Malformed attribute list response", true)
        return
    _refresh_attributes = json
    _refresh_attributes_loaded = true

## Rebuild the row stack from _refresh_rows_state. O(n) in row count which
## is small (one to a few). Disconnects all child signals by virtue of
## queue_free, so per-row Callable.bind on idx stays consistent.
func _render_refresh_rows() -> void:
    if _refreshes_rows_box == null:
        return
    for child in _refreshes_rows_box.get_children():
        child.queue_free()
    for idx in range(_refresh_rows_state.size()):
        var row_panel := _make_refresh_row_ui(idx)
        _refreshes_rows_box.add_child(row_panel)

## Build the UI for one refresh row. Reads from _refresh_rows_state[idx]
## so the layout reflects current state (e.g. the supply controls hide
## when "Infinite supply" is checked).
## A refresh row in the sidebar is now a single compact summary line that
## opens the per-row edit dialog on click, plus a remove button. The deep
## tuning (supply, refill, dwell recovery) lives in the modal so the sidebar
## stays readable as rows and fields grow.
func _make_refresh_row_ui(idx: int) -> Control:
    var row: Dictionary = _refresh_rows_state[idx]

    var panel := PanelContainer.new()
    var panel_style := StyleBoxFlat.new()
    panel_style.bg_color = Color(0.10, 0.08, 0.05, 1.0)
    panel_style.border_width_left = 1
    panel_style.border_width_top = 1
    panel_style.border_width_right = 1
    panel_style.border_width_bottom = 1
    panel_style.border_color = COLOR_ITEM_BORDER
    panel_style.content_margin_left = 6.0
    panel_style.content_margin_right = 6.0
    panel_style.content_margin_top = 4.0
    panel_style.content_margin_bottom = 4.0
    panel.add_theme_stylebox_override("panel", panel_style)

    var hbox := HBoxContainer.new()
    hbox.add_theme_constant_override("separation", 6)
    panel.add_child(hbox)

    # The whole summary line is the click target that opens the edit dialog.
    var summary_btn := Button.new()
    summary_btn.text = _refresh_summary_text(row)
    summary_btn.tooltip_text = "Edit this refresh"
    summary_btn.flat = true
    summary_btn.alignment = HORIZONTAL_ALIGNMENT_LEFT
    summary_btn.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    summary_btn.add_theme_color_override("font_color", COLOR_TEXT)
    summary_btn.add_theme_font_override("font", _font)
    summary_btn.add_theme_font_size_override("font_size", 12)
    summary_btn.pressed.connect(_open_refresh_dialog.bind(idx))
    hbox.add_child(summary_btn)

    # Close X comes from Lucide — IMFellEnglish doesn't ship the U+2715
    # multiplication-X this used to use. Override the font on the
    # already-built button rather than threading an icon-font path through
    # _make_refreshes_button (shared with text buttons like "+ Add refresh").
    var remove_btn := _make_refreshes_button(String.chr(ICON_CODEPOINT_X))
    remove_btn.add_theme_font_override("font", _icon_font)
    remove_btn.tooltip_text = "Remove this refresh row"
    remove_btn.pressed.connect(_on_refresh_remove_pressed.bind(idx))
    hbox.add_child(remove_btn)

    return panel

## One-line summary of a refresh row for the compact sidebar list, e.g.
## "tiredness · -1/use · dwell -1/10min · 10/10 continuous".
func _refresh_summary_text(row: Dictionary) -> String:
    var attr := str(row.get("attribute", ""))
    var gather := str(row.get("gather_item", ""))
    var parts: Array = []
    if attr != "":
        parts.append(attr)
        parts.append(str(int(row.get("amount", 0))) + "/use")
    elif gather != "":
        parts.append("gather-only")
    else:
        parts.append("(no attribute)")
    if gather != "":
        parts.append("yields " + gather)
    if bool(row.get("has_dwell", false)):
        parts.append("dwell " + str(int(row.get("dwell_delta", -1))) + "/" + str(int(row.get("dwell_period", 10))) + "min")
    if bool(row.get("infinite", false)):
        parts.append("unlimited supply")
    else:
        var mode_label := "periodic" if str(row.get("mode", "continuous")) == "periodic" else "continuous"
        parts.append(str(int(row.get("available", 0))) + "/" + str(int(row.get("max", 0))) + " " + mode_label)
    return " · ".join(parts)

func _make_refresh_label(text: String) -> Label:
    var l := Label.new()
    l.text = text
    l.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    l.add_theme_font_size_override("font_size", 12)
    return l

func _make_refresh_spinbox(value: int, min_val: int, max_val: int) -> SpinBox:
    var sb := SpinBox.new()
    sb.min_value = min_val
    sb.max_value = max_val
    sb.step = 1
    sb.value = value
    sb.custom_minimum_size = Vector2(80, 0)
    return sb

# --- per-row state mutators ---

# --- per-row edit dialog ---

## Lazily create the single reusable edit dialog (mirrors editor.gd's
## _delete_dialog). The form controls are rebuilt per-open inside _content.
func _ensure_refresh_dialog() -> void:
    if _refresh_dialog != null:
        return
    _refresh_dialog = ConfirmationDialog.new()
    _refresh_dialog.title = "Edit refresh"
    _refresh_dialog.ok_button_text = "Save"
    _refresh_dialog.min_size = Vector2i(360, 0)
    _refresh_dialog_content = VBoxContainer.new()
    _refresh_dialog_content.add_theme_constant_override("separation", 6)
    _refresh_dialog.add_child(_refresh_dialog_content)
    _refresh_dialog.confirmed.connect(_on_refresh_dialog_confirmed)
    add_child(_refresh_dialog)

## Open the edit dialog for one refresh row, building its controls from the
## row's current state. Reads are committed to _refresh_rows_state only on OK
## (Cancel discards), so the spinboxes can hold transient values freely.
func _open_refresh_dialog(idx: int) -> void:
    if idx < 0 or idx >= _refresh_rows_state.size():
        return
    # The dialog's dropdowns are built from _refresh_attributes and _items_catalog;
    # if either async fetch hasn't returned yet the dropdown would be empty (and OK
    # would commit a blank attribute, or a new gather row would be unpickable).
    # Refuse to open until both are loaded.
    if not _refresh_attributes_loaded or not _items_loaded:
        _set_refreshes_status("Loading refresh metadata...", false)
        return
    _ensure_refresh_dialog()
    _refresh_dialog_idx = idx
    var row: Dictionary = _refresh_rows_state[idx]

    for c in _refresh_dialog_content.get_children():
        _refresh_dialog_content.remove_child(c)
        c.queue_free()
    _rd = {}

    # Whether this row is (or starts as) a yield-only gather source: no
    # attribute, so no per-use restore amount and no dwell recovery. Drives the
    # initial visibility of the amount row and the dwell controls below; the
    # attribute dropdown's change handler keeps them in sync as the operator edits.
    var attr_is_none := str(row.get("attribute", "")) == ""

    # Attribute dropdown. A leading "(none — gather-only)" entry (metadata "")
    # models the yield-only shape; the remaining entries are the need catalog.
    var attr_row := HBoxContainer.new()
    attr_row.add_theme_constant_override("separation", 6)
    attr_row.add_child(_make_refresh_label("Attribute:"))
    var attr_dropdown := OptionButton.new()
    attr_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    attr_dropdown.add_item("(none — gather-only)")
    attr_dropdown.set_item_metadata(0, "")
    var selected_attr_idx := -1
    for ai in range(_refresh_attributes.size()):
        var a: Dictionary = _refresh_attributes[ai]
        var attr_opt_idx := ai + 1
        attr_dropdown.add_item(str(a.get("display_label", a.get("name", ""))))
        attr_dropdown.set_item_metadata(attr_opt_idx, str(a.get("name", "")))
        if str(a.get("name", "")) == str(row.get("attribute", "")):
            selected_attr_idx = attr_opt_idx
    # A matched attribute selects it; a blank or unmatched attribute falls back
    # to "(none)" rather than silently picking the first need in the catalog.
    if selected_attr_idx >= 0:
        attr_dropdown.selected = selected_attr_idx
    else:
        attr_dropdown.selected = 0
    attr_dropdown.item_selected.connect(_on_refresh_dialog_attr_changed)
    attr_row.add_child(attr_dropdown)
    _refresh_dialog_content.add_child(attr_row)
    _rd["attr"] = attr_dropdown

    # Gather item dropdown — the harvestable item this source yields. "(none)"
    # for a pure need row; a real item makes the row a gather source (a need row
    # WITH a gather item is an "eat+pick" source like the well). Sourced from the
    # item catalog (same as the NPC inventory dropdown). The row's current
    # gather_item is always kept as an option even if the catalog lacks it, so a
    # hand-authored / out-of-catalog value round-trips instead of being dropped.
    var gather_row := HBoxContainer.new()
    gather_row.add_theme_constant_override("separation", 6)
    gather_row.add_child(_make_refresh_label("Gather item:"))
    var gather_dropdown := OptionButton.new()
    gather_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    var current_gather := str(row.get("gather_item", ""))
    gather_dropdown.add_item("(none)")
    gather_dropdown.set_item_metadata(0, "")
    var selected_gather_idx := 0
    var found_gather := false
    for gi in range(_items_catalog.size()):
        var it: Dictionary = _items_catalog[gi]
        var item_name := str(it.get("name", ""))
        gather_dropdown.add_item(str(it.get("display_label", item_name)))
        var gather_opt_idx := gather_dropdown.get_item_count() - 1
        gather_dropdown.set_item_metadata(gather_opt_idx, item_name)
        if current_gather != "" and item_name == current_gather:
            selected_gather_idx = gather_opt_idx
            found_gather = true
    if current_gather != "" and not found_gather:
        gather_dropdown.add_item(current_gather + " (custom)")
        var custom_gather_idx := gather_dropdown.get_item_count() - 1
        gather_dropdown.set_item_metadata(custom_gather_idx, current_gather)
        selected_gather_idx = custom_gather_idx
    gather_dropdown.selected = selected_gather_idx
    gather_row.add_child(gather_dropdown)
    _refresh_dialog_content.add_child(gather_row)
    _rd["gather"] = gather_dropdown

    # Restores-per-use — only meaningful for a need row; hidden for a yield-only
    # source (amount 0). Kept in _rd either way so the confirm handler can read it.
    var amt_row := HBoxContainer.new()
    amt_row.add_theme_constant_override("separation", 6)
    amt_row.add_child(_make_refresh_label("Restores per use:"))
    var amt_spin := _make_refresh_spinbox(max(1, int(row.get("amount", 1))), 1, 24)
    amt_row.add_child(amt_spin)
    amt_row.visible = not attr_is_none
    _refresh_dialog_content.add_child(amt_row)
    _rd["amount"] = amt_spin
    _rd["amount_row"] = amt_row

    # Infinite-supply toggle + supply block
    var inf_check := CheckBox.new()
    inf_check.text = "Infinite supply"
    inf_check.button_pressed = bool(row.get("infinite", false))
    inf_check.toggled.connect(_on_refresh_dialog_infinite_toggled)
    _refresh_dialog_content.add_child(inf_check)
    _rd["infinite"] = inf_check

    var supply_block := VBoxContainer.new()
    supply_block.add_theme_constant_override("separation", 4)
    supply_block.visible = not bool(row.get("infinite", false))
    _refresh_dialog_content.add_child(supply_block)
    _rd["supply_block"] = supply_block

    var avail_row := HBoxContainer.new()
    avail_row.add_theme_constant_override("separation", 6)
    avail_row.add_child(_make_refresh_label("Available:"))
    var avail_spin := _make_refresh_spinbox(int(row.get("available", 1)), 0, 32000)
    avail_row.add_child(avail_spin)
    avail_row.add_child(_make_refresh_label("Max:"))
    var max_spin := _make_refresh_spinbox(int(row.get("max", 10)), 1, 32000)
    avail_row.add_child(max_spin)
    supply_block.add_child(avail_row)
    _rd["available"] = avail_spin
    _rd["max"] = max_spin

    var mode_row := HBoxContainer.new()
    mode_row.add_theme_constant_override("separation", 6)
    mode_row.add_child(_make_refresh_label("Refill:"))
    var mode_dropdown := OptionButton.new()
    mode_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    mode_dropdown.add_item("Continuous (gradual)", 0)
    mode_dropdown.set_item_metadata(0, "continuous")
    mode_dropdown.add_item("Periodic (one-shot)", 1)
    mode_dropdown.set_item_metadata(1, "periodic")
    mode_dropdown.selected = (1 if str(row.get("mode", "continuous")) == "periodic" else 0)
    mode_row.add_child(mode_dropdown)
    supply_block.add_child(mode_row)
    _rd["mode"] = mode_dropdown

    var period_row := HBoxContainer.new()
    period_row.add_theme_constant_override("separation", 6)
    period_row.add_child(_make_refresh_label("Period (hours):"))
    var period_spin := _make_refresh_spinbox(int(row.get("period", 24)), 1, 100000)
    period_row.add_child(period_spin)
    supply_block.add_child(period_row)
    _rd["period"] = period_spin

    # Dwell recovery toggle + block. dwell_delta is the per-period DECREMENT
    # applied while the actor dwells (negative — needs fall toward 0/sated),
    # so the spinbox range is negative-only to keep it server-valid.
    var dwell_check := CheckBox.new()
    dwell_check.text = "Has dwell recovery"
    dwell_check.button_pressed = bool(row.get("has_dwell", false))
    # A yield-only source can't carry dwell (the server rejects it), so the
    # toggle is disabled while the row has no attribute.
    dwell_check.disabled = attr_is_none
    dwell_check.toggled.connect(_on_refresh_dialog_dwell_toggled)
    _refresh_dialog_content.add_child(dwell_check)
    _rd["dwell_check"] = dwell_check

    var dwell_block := VBoxContainer.new()
    dwell_block.add_theme_constant_override("separation", 4)
    dwell_block.visible = bool(row.get("has_dwell", false))
    _refresh_dialog_content.add_child(dwell_block)
    _rd["dwell_block"] = dwell_block

    var dwell_amt_row := HBoxContainer.new()
    dwell_amt_row.add_theme_constant_override("separation", 6)
    dwell_amt_row.add_child(_make_refresh_label("Dwell amount (per period):"))
    var dwell_delta_spin := _make_refresh_spinbox(int(row.get("dwell_delta", -1)), -100, -1)
    dwell_amt_row.add_child(dwell_delta_spin)
    dwell_block.add_child(dwell_amt_row)
    _rd["dwell_delta"] = dwell_delta_spin

    var dwell_period_row := HBoxContainer.new()
    dwell_period_row.add_theme_constant_override("separation", 6)
    dwell_period_row.add_child(_make_refresh_label("Dwell period (minutes):"))
    var dwell_period_spin := _make_refresh_spinbox(int(row.get("dwell_period", 10)), 1, 1440)
    dwell_period_row.add_child(dwell_period_spin)
    dwell_block.add_child(dwell_period_row)
    _rd["dwell_period"] = dwell_period_spin

    _refresh_dialog.reset_size()
    _refresh_dialog.popup_centered()

func _on_refresh_dialog_infinite_toggled(pressed: bool) -> void:
    if _rd.has("supply_block") and is_instance_valid(_rd["supply_block"]):
        _rd["supply_block"].visible = not pressed
        _refresh_dialog.reset_size()

func _on_refresh_dialog_dwell_toggled(pressed: bool) -> void:
    if _rd.has("dwell_block") and is_instance_valid(_rd["dwell_block"]):
        _rd["dwell_block"].visible = pressed
        _refresh_dialog.reset_size()

## When the attribute dropdown changes, reflect the row type. Selecting "(none)"
## makes the row a yield-only gather source: no per-use restore amount and no
## dwell recovery (the server rejects both on a yield-only row), so hide the
## amount row and force dwell off + disabled. Selecting a real attribute restores
## them. Keeps the dialog in the same shape the save serializer expects.
func _on_refresh_dialog_attr_changed(_selected: int) -> void:
    if not (_rd.has("attr") and is_instance_valid(_rd["attr"])):
        return
    var meta = _rd["attr"].get_item_metadata(_rd["attr"].selected)
    var is_none := (meta == null) or (str(meta) == "")
    if _rd.has("amount_row") and is_instance_valid(_rd["amount_row"]):
        _rd["amount_row"].visible = not is_none
    if _rd.has("dwell_check") and is_instance_valid(_rd["dwell_check"]):
        var dwell_check: CheckBox = _rd["dwell_check"]
        dwell_check.disabled = is_none
        if is_none:
            dwell_check.button_pressed = false
            if _rd.has("dwell_block") and is_instance_valid(_rd["dwell_block"]):
                _rd["dwell_block"].visible = false
    _refresh_dialog.reset_size()

## Commit the dialog's controls back to the row state on Save (OK), then
## re-render the summary list.
func _on_refresh_dialog_confirmed() -> void:
    var idx := _refresh_dialog_idx
    if idx < 0 or idx >= _refresh_rows_state.size():
        return
    var attr_meta = _rd["attr"].get_item_metadata(_rd["attr"].selected) if _rd.has("attr") and _rd["attr"].selected >= 0 else null
    var attr_str := str(attr_meta) if attr_meta != null else ""
    var gather_meta = _rd["gather"].get_item_metadata(_rd["gather"].selected) if _rd.has("gather") and _rd["gather"].selected >= 0 else null
    var gather_str := str(gather_meta) if gather_meta != null else ""
    # A yield-only gather source (no attribute) carries magnitude 0 and no dwell;
    # a need row keeps its restore magnitude and dwell toggle. The amount/dwell
    # controls are hidden/disabled for yield-only, so read them only for a need row.
    var amount_mag := 0
    var has_dwell := false
    if attr_str != "":
        amount_mag = int(_rd["amount"].value)
        has_dwell = bool(_rd["dwell_check"].button_pressed)
    var row := {
        "attribute":    attr_str,
        "amount":       amount_mag,
        "gather_item":  gather_str,
        "infinite":     bool(_rd["infinite"].button_pressed),
        "available":    int(_rd["available"].value),
        "max":          int(_rd["max"].value),
        "mode":         str(_rd["mode"].get_item_metadata(_rd["mode"].selected)),
        "period":       int(_rd["period"].value),
        "has_dwell":    has_dwell,
        "dwell_delta":  int(_rd["dwell_delta"].value),
        "dwell_period": int(_rd["dwell_period"].value),
    }
    _refresh_rows_state[idx] = row
    _render_refresh_rows()

## Add a new row with sensible defaults. First-available attribute,
## restores-per-use 1, infinite supply off, capped supply 10/10,
## continuous mode, 24-hour refill period. Operator picks the attribute
## and tunes from there.
func _on_refresh_add_pressed() -> void:
    if _refreshes_current_id == "":
        return
    # Don't add until both catalogs are loaded — a new row immediately opens its
    # edit dialog, whose attribute AND gather-item dropdowns need to be populated
    # (otherwise a fresh gather/yield-only row can't pick an item until reopened).
    if not _refresh_attributes_loaded or not _items_loaded:
        _set_refreshes_status("Loading refresh metadata...", false)
        return
    var default_attr := ""
    if _refresh_attributes.size() > 0:
        default_attr = str(_refresh_attributes[0].get("name", ""))
    _refresh_rows_state.append({
        "attribute":    default_attr,
        "amount":       1,
        "gather_item":  "",
        "infinite":     false,
        "available":    10,
        "max":          10,
        "mode":         "continuous",
        "period":       24,
        "has_dwell":    false,
        "dwell_delta":  -1,
        "dwell_period": 10,
    })
    _render_refresh_rows()
    # Open the new row straight into its edit dialog so the operator can pick
    # the attribute and tune it rather than hunting for the summary line.
    _open_refresh_dialog(_refresh_rows_state.size() - 1)

func _on_refresh_remove_pressed(idx: int) -> void:
    if idx < 0 or idx >= _refresh_rows_state.size():
        return
    _refresh_rows_state.remove_at(idx)
    _render_refresh_rows()

## Send the whole row set to the server. Validation mirrors the API's
## checks so the operator gets immediate feedback rather than a 400 round
## trip; the server still validates because the client is untrusted.
func _on_refreshes_save_pressed() -> void:
    if _refreshes_current_id == "":
        return
    # Duplicate keys mirror the server's two conflict targets: need rows are
    # unique per attribute, yield-only gather rows per gather_item.
    var seen_attrs := {}
    var seen_gather := {}
    var rows := []
    for state in _refresh_rows_state:
        var attr := str(state.get("attribute", ""))
        var gather := str(state.get("gather_item", ""))
        # A yield-only gather source is a blank attribute WITH a gather item. A
        # blank attribute and no gather item is neither shape — reject it (mirrors
        # ValidateObjectRefreshes: attribute required on a need-bearing row).
        var is_yield_only := attr == "" and gather != ""
        if attr == "" and gather == "":
            _set_refreshes_status("A row needs an attribute or a gather item", true)
            return
        # A label for validation messages: yield-only rows have no attribute, so
        # name them by their gather item (matches the server's "gather:<item>").
        var label := attr if attr != "" else ("gather:" + gather)
        if is_yield_only:
            if seen_gather.has(gather):
                _set_refreshes_status("Duplicate gather item: " + gather, true)
                return
            seen_gather[gather] = true
        else:
            if seen_attrs.has(attr):
                _set_refreshes_status("Duplicate attribute: " + attr, true)
                return
            seen_attrs[attr] = true
        # Wire amount is NEGATIVE for a need row (the on-arrival decrement) and 0
        # for a yield-only source. The panel holds a positive magnitude, so negate.
        var amount := 0
        if not is_yield_only:
            var magnitude := int(state.get("amount", 1))
            if magnitude <= 0:
                _set_refreshes_status("Restores-per-use must be positive (" + label + ")", true)
                return
            amount = -magnitude
        var infinite := bool(state.get("infinite", false))
        var avail_value: Variant = null
        var max_value: Variant = null
        var period_value: Variant = null
        var mode := str(state.get("mode", "continuous"))
        if not infinite:
            var avail := int(state.get("available", 0))
            var max_q := int(state.get("max", 0))
            if max_q <= 0:
                _set_refreshes_status("Max must be > 0 (" + label + ")", true)
                return
            if avail < 0 or avail > max_q:
                _set_refreshes_status("Available must be between 0 and Max (" + label + ")", true)
                return
            var period := int(state.get("period", 24))
            if period <= 0:
                _set_refreshes_status("Period must be > 0 (" + label + ")", true)
                return
            avail_value = avail
            max_value = max_q
            period_value = period
        # A yield-only row can't carry dwell (the server rejects it). The dialog
        # forces dwell off when a row becomes yield-only, so this only fires on
        # stale/corrupt state — reject it rather than silently dropping the fields,
        # to strictly mirror ValidateObjectRefreshes.
        if is_yield_only and bool(state.get("has_dwell", false)):
            _set_refreshes_status("Yield-only rows cannot have dwell recovery (" + label + ")", true)
            return
        # Dwell recovery (optional, need rows only). dwell_delta + dwell_period_minutes
        # travel together — null for both when off. Mirrors the server CHECKs
        # (dwell_delta < 0, dwell_period_minutes > 0) for immediate feedback.
        var dwell_delta_value: Variant = null
        var dwell_period_value: Variant = null
        if bool(state.get("has_dwell", false)):
            var dd := int(state.get("dwell_delta", -1))
            var dp := int(state.get("dwell_period", 10))
            if dd >= 0:
                _set_refreshes_status("Dwell amount must be negative (" + label + ")", true)
                return
            if dp <= 0:
                _set_refreshes_status("Dwell period must be > 0 (" + label + ")", true)
                return
            dwell_delta_value = dd
            dwell_period_value = dp
        # refresh_mode/refresh_period_hours are only valid on a finite (tracked-
        # supply) row — the server rejects a non-empty mode on an infinite row.
        # Emit an empty mode when infinite so the row round-trips server-valid
        # (period is already null via period_value). This normalizes a vestigial
        # mode that seed data can carry on an infinite row (e.g. the well's
        # infinite drink row ships refresh_mode="continuous").
        var mode_value := mode if not infinite else ""
        rows.append({
            "attribute":            attr,
            "amount":               amount,
            "gather_item":          gather,
            "available_quantity":   avail_value,
            "max_quantity":         max_value,
            "refresh_mode":         mode_value,
            "refresh_period_hours": period_value,
            "dwell_delta":          dwell_delta_value,
            "dwell_period_minutes": dwell_period_value,
        })

    # v2 set-refresh: POST the whole set with the object id in the body
    # (replaces the v1 PUT /objects/{id}/refresh, which the v2 server doesn't
    # serve). The admin gate is enforced server-side in the command handler.
    var payload := JSON.stringify({"object_id": _refreshes_current_id, "rows": rows})
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_refreshes_save_response.bind(http, _refreshes_current_id))
    var headers := Auth.auth_headers()
    http.request(Auth.api_base + "/api/village/admin/object/set-refresh",
        headers, HTTPClient.METHOD_POST, payload)
    _set_refreshes_status("Saving...", false)

func _on_refreshes_save_response(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, saved_id: String) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    # The operator may have selected a different object while the save was in
    # flight. Node meta (that object's data) is still safe to update, but UI
    # status / visible rows must only change if we're still on saved_id.
    var still_selected := saved_id == _refreshes_current_id
    if response_code == 200:
        # set-refresh echoes the canonical applied set ({id, rows}). Persist it
        # onto the object node's meta so a later reselection reads the saved
        # state (v2 has no refresh GET).
        var json = JSON.parse_string(body.get_string_from_utf8())
        var echoed: Array = []
        if json is Dictionary and json.get("rows", null) is Array:
            echoed = json["rows"]
        if world != null and world.placed_objects.has(saved_id):
            world.placed_objects[saved_id].set_meta("refreshes", echoed)
        if still_selected:
            # An external reload invalidates any open dialog's cached row refs.
            _reset_refresh_dialog()
            _load_refresh_rows_from(echoed)
            _render_refresh_rows()
            _set_refreshes_status("Saved", false)
        return
    if not still_selected:
        return
    if result != HTTPRequest.RESULT_SUCCESS:
        _set_refreshes_status("Network error", true)
        return
    if response_code == 403:
        _set_refreshes_status("Admin access required", true)
        return
    # 400 from server includes the validation message in the body's "error"
    # field — surface it directly instead of a generic "save failed."
    var detail := ""
    var ejson = JSON.parse_string(body.get_string_from_utf8())
    if ejson is Dictionary:
        detail = str(ejson.get("error", ""))
    var label := "Save failed (" + str(response_code) + ")"
    if detail != "":
        label += ": " + detail
    _set_refreshes_status(label, true)

func _set_refreshes_status(text: String, is_error: bool) -> void:
    if _refreshes_status == null:
        return
    _refreshes_status.text = text
    _refreshes_status.add_theme_color_override("font_color",
        Color(0.85, 0.45, 0.40, 0.9) if is_error else COLOR_TEXT_DIM)


# =====================================================================
# INVENTORY (ZBBS-091)
# =====================================================================
#
# Per-NPC item rows in the NPC selection panel. The section UI is built
# in _build_panel under _npc_fields_section; state lives in
# _inventory_rows_state and the lookup catalog in _items_catalog.

func _show_inventory_for_actor(actor_id: String) -> void:
    _inventory_current_id = actor_id
    _inventory_rows_state.clear()
    _set_inventory_status("", false)
    _render_inventory_rows()
    if actor_id == "":
        return
    if not _items_loaded:
        _fetch_items_catalog(actor_id)
    else:
        _fetch_inventory_for_actor(actor_id)

func _clear_inventory_panel() -> void:
    _inventory_current_id = ""
    _inventory_rows_state.clear()
    _set_inventory_status("", false)
    _render_inventory_rows()

func _fetch_items_catalog(then_load_actor_id: String) -> void:
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_items_catalog_response.bind(http, then_load_actor_id))
    var headers := Auth.auth_headers(false)
    http.request(Auth.api_base + "/api/village/items", headers)

func _on_items_catalog_response(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, then_load_actor_id: String) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        _set_inventory_status("Failed to load items (" + str(response_code) + ")", true)
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if not (json is Array):
        _set_inventory_status("Malformed items response", true)
        return
    _items_catalog = json
    _items_loaded = true
    if then_load_actor_id != "" and then_load_actor_id == _inventory_current_id:
        _fetch_inventory_for_actor(then_load_actor_id)

func _fetch_inventory_for_actor(actor_id: String) -> void:
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_inventory_response.bind(http, actor_id))
    # ZBBS-HOME-309: rewired off the dead v1 GET /api/village/npcs/{id}/inventory
    # to the v2 admin read route (POST, id in body). Response is still a bare
    # array of {item_kind, quantity}, so the parse below is unchanged.
    var headers := Auth.auth_headers()
    var body: String = JSON.stringify({"npc_id": actor_id})
    http.request(Auth.api_base + "/api/village/admin/npc/inventory",
        headers, HTTPClient.METHOD_POST, body)

func _on_inventory_response(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, fetched_id: String) -> void:
    http.queue_free()
    if fetched_id != _inventory_current_id:
        return
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        _set_inventory_status("Failed to load inventory (" + str(response_code) + ")", true)
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if not (json is Array):
        _set_inventory_status("Malformed inventory response", true)
        return
    _inventory_rows_state.clear()
    for entry in json:
        if not (entry is Dictionary):
            continue
        _inventory_rows_state.append({
            "item_kind": str(entry.get("item_kind", "")),
            "quantity":  int(entry.get("quantity", 1)),
        })
    _render_inventory_rows()

func _render_inventory_rows() -> void:
    if _inventory_rows_box == null:
        return
    for child in _inventory_rows_box.get_children():
        child.queue_free()
    for idx in range(_inventory_rows_state.size()):
        var row_panel := _make_inventory_row_ui(idx)
        _inventory_rows_box.add_child(row_panel)

func _make_inventory_row_ui(idx: int) -> Control:
    var row: Dictionary = _inventory_rows_state[idx]

    var hbox := HBoxContainer.new()
    hbox.add_theme_constant_override("separation", 6)

    var item_dropdown := OptionButton.new()
    item_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    item_dropdown.add_theme_color_override("font_color", COLOR_TEXT)
    item_dropdown.add_theme_font_override("font", _font)
    item_dropdown.add_theme_font_size_override("font_size", 12)
    var selected := -1
    for ii in range(_items_catalog.size()):
        var it: Dictionary = _items_catalog[ii]
        item_dropdown.add_item(str(it.get("display_label", it.get("name", ""))), ii)
        item_dropdown.set_item_metadata(ii, str(it.get("name", "")))
        if str(it.get("name", "")) == str(row.get("item_kind", "")):
            selected = ii
    if selected >= 0:
        item_dropdown.selected = selected
    elif _items_catalog.size() > 0:
        item_dropdown.selected = 0
        _inventory_rows_state[idx]["item_kind"] = str(_items_catalog[0].get("name", ""))
    item_dropdown.item_selected.connect(_on_inventory_item_changed.bind(idx, item_dropdown))
    hbox.add_child(item_dropdown)

    var qty_spin := SpinBox.new()
    qty_spin.min_value = 1
    qty_spin.max_value = 32000
    qty_spin.step = 1
    qty_spin.value = int(row.get("quantity", 1))
    qty_spin.custom_minimum_size = Vector2(80, 0)
    qty_spin.value_changed.connect(_on_inventory_qty_changed.bind(idx))
    hbox.add_child(qty_spin)

    var remove_btn := _make_refreshes_button("✕")
    remove_btn.tooltip_text = "Remove this item row"
    remove_btn.pressed.connect(_on_inventory_remove_pressed.bind(idx))
    hbox.add_child(remove_btn)

    return hbox

func _on_inventory_item_changed(_dropdown_idx: int, idx: int, dropdown: OptionButton) -> void:
    if idx < 0 or idx >= _inventory_rows_state.size():
        return
    var meta = dropdown.get_item_metadata(dropdown.selected)
    _inventory_rows_state[idx]["item_kind"] = str(meta) if meta != null else ""

func _on_inventory_qty_changed(value: float, idx: int) -> void:
    if idx < 0 or idx >= _inventory_rows_state.size():
        return
    _inventory_rows_state[idx]["quantity"] = int(value)

func _on_inventory_add_pressed() -> void:
    if _inventory_current_id == "":
        return
    var default_item := ""
    if _items_catalog.size() > 0:
        default_item = str(_items_catalog[0].get("name", ""))
    _inventory_rows_state.append({
        "item_kind": default_item,
        "quantity":  1,
    })
    _render_inventory_rows()

func _on_inventory_remove_pressed(idx: int) -> void:
    if idx < 0 or idx >= _inventory_rows_state.size():
        return
    _inventory_rows_state.remove_at(idx)
    _render_inventory_rows()

func _on_inventory_save_pressed() -> void:
    if _inventory_current_id == "":
        return
    var seen := {}
    var rows := []
    for state in _inventory_rows_state:
        var item := str(state.get("item_kind", ""))
        if item == "":
            _set_inventory_status("A row is missing its item", true)
            return
        if seen.has(item):
            _set_inventory_status("Duplicate item: " + item, true)
            return
        seen[item] = true
        var qty := int(state.get("quantity", 1))
        if qty <= 0:
            _set_inventory_status("Quantity must be positive (" + item + ")", true)
            return
        rows.append({"item_kind": item, "quantity": qty})

    # ZBBS-HOME-309: rewired off the dead v1 PUT /api/village/npcs/{id}/inventory
    # to the v2 admin whole-set write (POST, id in body). Still responds 204 on
    # success, so _on_inventory_save_response is unchanged.
    var payload := JSON.stringify({"npc_id": _inventory_current_id, "rows": rows})
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_inventory_save_response.bind(http, _inventory_current_id))
    var headers := Auth.auth_headers()
    http.request(Auth.api_base + "/api/village/admin/npc/set-inventory",
        headers, HTTPClient.METHOD_POST, payload)
    _set_inventory_status("Saving...", false)

func _on_inventory_save_response(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, _saved_id: String) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS:
        _set_inventory_status("Network error", true)
        return
    if response_code == 204:
        _set_inventory_status("Saved", false)
        return
    if response_code == 403:
        _set_inventory_status("Admin access required", true)
        return
    var detail := ""
    var json = JSON.parse_string(body.get_string_from_utf8())
    if json is Dictionary:
        detail = str(json.get("error", ""))
    var label := "Save failed (" + str(response_code) + ")"
    if detail != "":
        label += ": " + detail
    _set_inventory_status(label, true)

func _set_inventory_status(text: String, is_error: bool) -> void:
    if _inventory_status == null:
        return
    _inventory_status.text = text
    _inventory_status.add_theme_color_override("font_color",
        Color(0.85, 0.45, 0.40, 0.9) if is_error else COLOR_TEXT_DIM)
