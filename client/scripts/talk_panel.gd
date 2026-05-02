extends CanvasLayer

## Talk panel — Salem 1692 conversation drawer.
##
## Broadcast-only player chat. Nearby members are informational chips, not
## targeting buttons; the LLMs decide who's being addressed from context.
##
## Closed state shows a "Talk (N nearby)" launcher pill. Open state slides up
## a bottom drawer (desktop, anchored bottom-right) or a 60vh bottom sheet
## (mobile, full-width). The user closes/opens it explicitly — the refresh
## timer never forces visibility.
##
## Mounted as a CanvasLayer from main.gd (talk_panel_layer). The script
## owns layer ordering itself.
##
## Runtime dependencies:
##   - /root/Auth (api_base + auth_headers())
##   - /root/Main/World.npc_spoke(name, text, kind) signal
##   - POST /api/village/pc/me  → state
##   - POST /api/village/pc/speak {text}  → broadcast

const REFRESH_INTERVAL := 10.0
const DESKTOP_MIN_WIDTH := 580.0
const DESKTOP_MIN_HEIGHT := 400.0
const MOBILE_BREAKPOINT := 720.0
const MOBILE_SHEET_VH := 0.60
const MOBILE_SHEET_FOCUSED_VH := 0.82
const MAX_LOG_LINES := 80

var root: Control

var launcher_anchor: MarginContainer
var talk_launcher: Button

var sheet_anchor: MarginContainer
var talk_sheet: PanelContainer

var context_label: Label
var subcontext_label: Label
var close_button: Button
var nearby_scroll: ScrollContainer
var nearby_flow: HFlowContainer
var log_scroll: ScrollContainer
var log_vbox: VBoxContainer
var speech_input: TextEdit
var speak_button: Button
## Pay flow — small button next to Speak opens a modal (built lazily)
## with recipient dropdown, amount, optional item / qty / take-home.
## Submit fires POST /api/village/pc/pay and re-polls /pc/me on success
## so the top bar's coin chip refreshes immediately.
var pay_button: Button = null
var pay_modal: CanvasLayer = null
var pay_recipient_option: OptionButton = null
var pay_amount_spin: SpinBox = null
var pay_item_input: LineEdit = null
var pay_qty_spin: SpinBox = null
var pay_take_home_check: CheckBox = null
var pay_status_label: Label = null
var pay_confirm_button: Button = null
var pay_cancel_button: Button = null
var http_pay: HTTPRequest = null
## Cached huddle member list at the moment the modal opened — used so
## the dropdown index maps back to the chosen recipient name without
## re-reading huddle_members during the click.
var pay_modal_recipients: Array = []
## Last /pc/me snapshot of coins + inventory. Reused by the modal to
## show the player what they can afford.
var pc_coins: int = 0
var pc_inventory: Array = []

## Emitted whenever the polled /pc/me reports a fresh coin / inventory
## state. main.gd subscribes and forwards to the top-bar's set_purse.
## Negative coins signals "no PC" (top bar should hide the chip).
signal purse_changed(coins: int, inventory_lines: PackedStringArray)

# ZBBS-087 — Village tab. The panel hosts two tabs: Room (existing room-
# scoped chat) and Village (mechanical village-wide events).
const TAB_ROOM := 0
const TAB_VILLAGE := 1
var tab_bar: HBoxContainer = null
var tab_room_button: Button = null
var tab_village_button: Button = null
var tab_village_unread_dot: Panel = null
var current_tab: int = TAB_ROOM
var village_scroll: ScrollContainer = null
var village_vbox: VBoxContainer = null
# village_log_loading is true between request fire and successful parse;
# village_log_loaded only flips after a 200 response was decoded. Failed
# requests leave both false so the next tab activation retries.
var village_log_loading: bool = false
var village_log_loaded: bool = false
# When true, the scroll container re-pins to bottom every time the
# vbox re-sorts (new label added, autowrap relayout, etc.). This is
# what keeps the "newest at bottom, scrolled to bottom" invariant
# stable — a single deferred call after append races autowrap layout
# and ends up parked partway up. Cleared when the user scrolls up
# manually so they can read history without being yanked back; re-set
# when they scroll back to within a small threshold of the bottom.
var village_stick_bottom: bool = true
const VILLAGE_STICK_BOTTOM_THRESHOLD: int = 24
# Live events that arrive while the backload is in flight get parked
# here and applied after the backload completes — the
# (occurred_at, id) overlap window between SELECT snapshot and WS
# broadcast is handled by the village_seen_ids dedupe.
var village_pending_live: Array = []
# Set of village_event ids already rendered, used to dedupe across the
# backload/WS race. int → true. Bounded implicitly by MAX_LOG_LINES
# trimming + occasional clear if it ever grows unboundedly (it won't
# under realistic load, but worth keeping in mind).
var village_seen_ids: Dictionary = {}
var village_unread: int = 0

var refresh_timer: Timer
var http_me: HTTPRequest
var http_speak: HTTPRequest
var http_village_log: HTTPRequest

var pc_exists := false
var character_name := ""
var structure_name := ""
var home_name := ""
var huddle_members: Array = []

# Tracks which structure's recent_speech we've already loaded so we don't
# duplicate the backload across the 10s polling cycle. When the player walks
# into a new structure (or up to a new booth), this changes and we
# clear+reload the log. Sourced from /pc/me's audience_structure_id, which
# falls back to the huddle's structure when the PC is loitering outdoors at
# a booth — that way a doorstep conversation shows the same room view a
# bar-stool conversation does.
var loaded_structure_id := ""

var is_open := false
var user_closed := false
var has_ever_seen_huddle := false
var first_encounter_pulse_done := false
var unread_count := 0
var is_mobile := false
var input_focused := false
var _pending_focus := false


func _ready() -> void:
    layer = 4

    _build_tree()
    _apply_theme()
    _connect_signals()
    _connect_world_signal()

    get_viewport().size_changed.connect(_on_viewport_size_changed)
    _update_responsive_layout()

    refresh_timer.start()
    _refresh_state()
    _update_visibility_from_state()


func _build_tree() -> void:
    root = Control.new()
    root.name = "TalkRoot"
    root.set_anchors_preset(Control.PRESET_FULL_RECT)
    # The root must let mouse events fall through everywhere except the actual
    # interactive controls; otherwise we eat clicks meant for the world.
    root.mouse_filter = Control.MOUSE_FILTER_IGNORE
    add_child(root)

    _build_launcher()
    _build_sheet()

    http_me = HTTPRequest.new()
    add_child(http_me)

    http_speak = HTTPRequest.new()
    add_child(http_speak)

    http_village_log = HTTPRequest.new()
    add_child(http_village_log)

    http_pay = HTTPRequest.new()
    add_child(http_pay)

    refresh_timer = Timer.new()
    refresh_timer.wait_time = REFRESH_INTERVAL
    refresh_timer.one_shot = false
    add_child(refresh_timer)


func _build_launcher() -> void:
    launcher_anchor = MarginContainer.new()
    launcher_anchor.name = "LauncherAnchor"
    launcher_anchor.set_anchors_preset(Control.PRESET_FULL_RECT)
    launcher_anchor.mouse_filter = Control.MOUSE_FILTER_IGNORE
    root.add_child(launcher_anchor)

    var outer := VBoxContainer.new()
    outer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    outer.size_flags_vertical = Control.SIZE_EXPAND_FILL
    outer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    launcher_anchor.add_child(outer)

    var top_spacer := Control.new()
    top_spacer.size_flags_vertical = Control.SIZE_EXPAND_FILL
    top_spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    outer.add_child(top_spacer)

    var row := HBoxContainer.new()
    row.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.mouse_filter = Control.MOUSE_FILTER_IGNORE
    outer.add_child(row)

    var left_spacer := Control.new()
    left_spacer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    left_spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    row.add_child(left_spacer)

    talk_launcher = Button.new()
    talk_launcher.name = "TalkLauncher"
    talk_launcher.text = "Talk"
    talk_launcher.custom_minimum_size = Vector2(150, 48)
    talk_launcher.focus_mode = Control.FOCUS_ALL
    talk_launcher.mouse_filter = Control.MOUSE_FILTER_STOP
    row.add_child(talk_launcher)


func _build_sheet() -> void:
    sheet_anchor = MarginContainer.new()
    sheet_anchor.name = "SheetAnchor"
    sheet_anchor.set_anchors_preset(Control.PRESET_FULL_RECT)
    sheet_anchor.mouse_filter = Control.MOUSE_FILTER_IGNORE
    sheet_anchor.visible = false
    root.add_child(sheet_anchor)

    var outer := VBoxContainer.new()
    outer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    outer.size_flags_vertical = Control.SIZE_EXPAND_FILL
    outer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    sheet_anchor.add_child(outer)

    var top_spacer := Control.new()
    top_spacer.size_flags_vertical = Control.SIZE_EXPAND_FILL
    top_spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    outer.add_child(top_spacer)

    var row := HBoxContainer.new()
    row.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.mouse_filter = Control.MOUSE_FILTER_IGNORE
    outer.add_child(row)

    var left_spacer := Control.new()
    left_spacer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    left_spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    row.add_child(left_spacer)

    talk_sheet = PanelContainer.new()
    talk_sheet.name = "TalkSheet"
    talk_sheet.mouse_filter = Control.MOUSE_FILTER_STOP
    talk_sheet.size_flags_horizontal = Control.SIZE_SHRINK_END
    talk_sheet.size_flags_vertical = Control.SIZE_SHRINK_END
    talk_sheet.custom_minimum_size = Vector2(DESKTOP_MIN_WIDTH, DESKTOP_MIN_HEIGHT)
    row.add_child(talk_sheet)

    var pad := MarginContainer.new()
    pad.add_theme_constant_override("margin_left", 14)
    pad.add_theme_constant_override("margin_right", 14)
    pad.add_theme_constant_override("margin_top", 8)
    pad.add_theme_constant_override("margin_bottom", 12)
    talk_sheet.add_child(pad)

    var vbox := VBoxContainer.new()
    vbox.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    vbox.size_flags_vertical = Control.SIZE_EXPAND_FILL
    vbox.add_theme_constant_override("separation", 6)
    pad.add_child(vbox)

    _build_header(vbox)
    _build_tab_bar(vbox)
    _build_nearby(vbox)
    _build_log(vbox)
    _build_village_log(vbox)
    _build_input(vbox)
    _set_active_tab(TAB_ROOM)


func _build_header(parent: Control) -> void:
    # Compact one-line header: just the room name, dim and small, with the
    # close button on the right.
    var header := HBoxContainer.new()
    header.custom_minimum_size = Vector2(0, 20)
    header.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    parent.add_child(header)

    context_label = Label.new()
    context_label.clip_text = true
    context_label.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
    context_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    context_label.add_theme_font_size_override("font_size", 11)
    header.add_child(context_label)

    # Kept around so existing _update_context_labels code that touches .text
    # continues to compile, but never added to the layout.
    subcontext_label = Label.new()
    subcontext_label.visible = false

    close_button = Button.new()
    close_button.text = "×"
    close_button.custom_minimum_size = Vector2(28, 20)
    close_button.focus_mode = Control.FOCUS_ALL
    close_button.mouse_filter = Control.MOUSE_FILTER_STOP
    close_button.add_theme_font_size_override("font_size", 14)
    header.add_child(close_button)


func _build_nearby(parent: Control) -> void:
    nearby_scroll = ScrollContainer.new()
    # Just enough for one row of small chips. Was 30 — left 12px of dead
    # space under the chips because the HFlowContainer top-aligned its
    # single row inside the larger ScrollContainer.
    nearby_scroll.custom_minimum_size = Vector2(0, 22)
    nearby_scroll.size_flags_vertical = Control.SIZE_SHRINK_BEGIN
    nearby_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_AUTO
    nearby_scroll.vertical_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    parent.add_child(nearby_scroll)

    nearby_flow = HFlowContainer.new()
    nearby_flow.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    nearby_flow.add_theme_constant_override("h_separation", 6)
    nearby_flow.add_theme_constant_override("v_separation", 6)
    nearby_scroll.add_child(nearby_flow)


func _build_log(parent: Control) -> void:
    log_scroll = ScrollContainer.new()
    log_scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    log_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    log_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    log_scroll.vertical_scroll_mode = ScrollContainer.SCROLL_MODE_AUTO
    parent.add_child(log_scroll)

    log_vbox = VBoxContainer.new()
    log_vbox.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    log_vbox.add_theme_constant_override("separation", 8)
    log_scroll.add_child(log_vbox)


# ZBBS-087 — Tab strip below the header. Two tabs: Room (existing
# room-scoped chat with nearby chips + speech input) and Village (read-
# only feed of arrivals/departures/phase events). Switching tabs swaps
# which content the body shows.
func _build_tab_bar(parent: Control) -> void:
    tab_bar = HBoxContainer.new()
    tab_bar.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    tab_bar.custom_minimum_size = Vector2(0, 22)
    tab_bar.add_theme_constant_override("separation", 4)
    parent.add_child(tab_bar)

    tab_room_button = _make_tab_button("Room")
    tab_bar.add_child(tab_room_button)

    # Village tab carries an unread dot anchored to the upper-right of
    # the button — same affordance as the existing nearby-huddle pulse.
    var village_wrap := Control.new()
    village_wrap.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
    tab_bar.add_child(village_wrap)
    tab_village_button = _make_tab_button("Village")
    village_wrap.add_child(tab_village_button)
    village_wrap.custom_minimum_size = tab_village_button.custom_minimum_size

    tab_village_unread_dot = Panel.new()
    tab_village_unread_dot.custom_minimum_size = Vector2(7, 7)
    tab_village_unread_dot.size = Vector2(7, 7)
    tab_village_unread_dot.position = Vector2(tab_village_button.custom_minimum_size.x - 9, 3)
    tab_village_unread_dot.visible = false
    var dot_style := StyleBoxFlat.new()
    dot_style.bg_color = Color(0.85, 0.45, 0.18, 1.0)
    dot_style.corner_radius_top_left = 4
    dot_style.corner_radius_top_right = 4
    dot_style.corner_radius_bottom_left = 4
    dot_style.corner_radius_bottom_right = 4
    tab_village_unread_dot.add_theme_stylebox_override("panel", dot_style)
    tab_village_unread_dot.mouse_filter = Control.MOUSE_FILTER_IGNORE
    village_wrap.add_child(tab_village_unread_dot)


func _make_tab_button(label: String) -> Button:
    var b := Button.new()
    b.text = label
    b.toggle_mode = true
    b.focus_mode = Control.FOCUS_NONE
    b.mouse_filter = Control.MOUSE_FILTER_STOP
    b.custom_minimum_size = Vector2(72, 22)
    b.add_theme_font_size_override("font_size", 11)
    return b


# ZBBS-087 — Village tab content. Built once, shown/hidden by tab swap.
# Read-only: no speech input, no nearby chips. Backloaded lazily on
# first switch via POST /api/village/log/recent; live updates land via
# the world.village_event_added signal.
func _build_village_log(parent: Control) -> void:
    village_scroll = ScrollContainer.new()
    village_scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    village_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    village_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    village_scroll.vertical_scroll_mode = ScrollContainer.SCROLL_MODE_AUTO
    village_scroll.visible = false
    parent.add_child(village_scroll)

    village_vbox = VBoxContainer.new()
    village_vbox.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    village_vbox.add_theme_constant_override("separation", 6)
    village_scroll.add_child(village_vbox)

    # Re-pin to bottom whenever the vbox re-sorts (new label, autowrap
    # relayout). The deferred scroll on append still fires for the
    # initial paint, but this signal closes the race when the final
    # max_value lands a frame or two later.
    village_vbox.sort_children.connect(_on_village_vbox_sorted)
    # Clear village_stick_bottom when the user scrolls up; re-arm when
    # they scroll back near the bottom. The v_scroll_bar emits "changed"
    # on programmatic scrolls too, so we filter on input_event from the
    # scroll container itself rather than the bar.
    village_scroll.gui_input.connect(_on_village_scroll_gui_input)


# Visibility toggle between the two tabs. Room tab shows nearby chips +
# room log + speech input; Village tab shows the village log (read-only).
# Lazy-loads the village backload on first activation.
func _set_active_tab(idx: int) -> void:
    current_tab = idx
    var room_active := idx == TAB_ROOM
    if tab_room_button != null:
        tab_room_button.button_pressed = room_active
    if tab_village_button != null:
        tab_village_button.button_pressed = not room_active
    if nearby_scroll != null:
        nearby_scroll.visible = room_active
    if log_scroll != null:
        log_scroll.visible = room_active
    if speech_input != null:
        speech_input.get_parent().visible = room_active
    if village_scroll != null:
        village_scroll.visible = not room_active
    if not room_active:
        # Switching INTO Village clears the unread badge and triggers
        # backload on first view.
        village_unread = 0
        if tab_village_unread_dot != null:
            tab_village_unread_dot.visible = false
        if not village_log_loaded:
            _load_village_log_backload()
        _scroll_village_log_to_bottom_deferred()


func _load_village_log_backload() -> void:
    if village_log_loaded or village_log_loading:
        return
    if http_village_log == null:
        return
    var url: String = _api_url("/api/village/log/recent")
    var headers: PackedStringArray = _auth_headers()
    var body := JSON.stringify({"limit": 50})
    village_log_loading = true
    var err := http_village_log.request(url, headers, HTTPClient.METHOD_POST, body)
    if err != OK:
        # request() rejected synchronously — clear the loading flag so
        # the next tab activation retries. WS events accumulated in
        # village_pending_live during this brief window flush below as
        # a courtesy rather than getting stuck.
        village_log_loading = false
        _flush_pending_live()


func _on_village_log_completed(_result: int, code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    village_log_loading = false
    if code != 200:
        # Backload failed — leave village_log_loaded false so the next
        # tab activation retries. Drop pending live rows; without the
        # backload anchor we don't know what's missing in front of them.
        village_pending_live.clear()
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if typeof(json) != TYPE_DICTIONARY:
        return
    var events = json.get("events", [])
    if typeof(events) != TYPE_ARRAY:
        return
    for entry in events:
        if typeof(entry) == TYPE_DICTIONARY:
            _append_village_event(entry, true)
    village_log_loaded = true
    _flush_pending_live()
    _scroll_village_log_to_bottom_deferred()


# Apply WS events that arrived during the backload. Dedupe by id so
# rows that were already in the backload result aren't re-rendered.
func _flush_pending_live() -> void:
    for entry in village_pending_live:
        if typeof(entry) == TYPE_DICTIONARY:
            _append_village_event(entry, false)
    village_pending_live.clear()


# Live update from world.village_event_added signal. When the Village
# tab is the active tab, append the row immediately and scroll to bottom;
# otherwise increment the unread badge so the user knows to look. Events
# arriving during an in-flight backload are buffered and flushed after
# the backload completes (with id-based dedupe handling the race).
func _on_village_event_added(data: Dictionary) -> void:
    if village_log_loading and not village_log_loaded:
        village_pending_live.append(data)
        return
    var appended := _append_village_event(data, false)
    if not appended:
        return
    if current_tab == TAB_VILLAGE and is_open:
        _scroll_village_log_to_bottom_deferred()
    else:
        village_unread += 1
        if tab_village_unread_dot != null:
            tab_village_unread_dot.visible = true


# Append one village_event row to the village log. Returns true if a
# label was actually added so callers can avoid mis-counting unread.
# Styles by event_type: phase events render as centered atmospheric
# accent, arrivals and departures as plain dim text.
func _append_village_event(row: Dictionary, _is_backload: bool) -> bool:
    if village_vbox == null:
        return false
    var text: String = str(row.get("text", ""))
    if text == "":
        return false
    var id_val := int(row.get("id", 0))
    if id_val > 0:
        if village_seen_ids.has(id_val):
            return false
        village_seen_ids[id_val] = true
    var event_type: String = str(row.get("event_type", ""))

    var label := Label.new()
    label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    label.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    label.text = text
    label.add_theme_font_size_override("font_size", 12)
    if event_type.begins_with("phase_"):
        label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
        label.add_theme_color_override("font_color", Color(0.85, 0.72, 0.42, 1.0))
    else:
        label.add_theme_color_override("font_color", Color(0.78, 0.72, 0.58, 1.0))
    village_vbox.add_child(label)

    while village_vbox.get_child_count() > MAX_LOG_LINES:
        village_vbox.get_child(0).queue_free()
    return true


func _scroll_village_log_to_bottom_deferred() -> void:
    if village_scroll == null:
        return
    # Re-arm the stick-bottom invariant — any caller asking to scroll
    # to bottom is implicitly asking to follow new content too. The
    # sort_children handler will land the actual scroll once the
    # vbox finishes laying out.
    village_stick_bottom = true
    call_deferred("_scroll_village_log_to_bottom")
    await get_tree().process_frame
    await get_tree().process_frame
    _scroll_village_log_to_bottom()


func _scroll_village_log_to_bottom() -> void:
    if village_scroll == null:
        return
    var bar := village_scroll.get_v_scroll_bar()
    if bar != null:
        village_scroll.scroll_vertical = int(bar.max_value)


# Inner vbox finished sorting — labels have laid out, max_value is
# current. If we're following the bottom, re-pin. Cheap; no-op when
# the user has scrolled up to read history.
func _on_village_vbox_sorted() -> void:
    if not village_stick_bottom or village_scroll == null:
        return
    var bar := village_scroll.get_v_scroll_bar()
    if bar != null:
        village_scroll.scroll_vertical = int(bar.max_value)


# User-driven scroll input on the village log. Wheel + drag both come
# through here. Decide whether to follow the bottom based on where the
# user has parked the scrollbar.
func _on_village_scroll_gui_input(event: InputEvent) -> void:
    if not (event is InputEventMouseButton or event is InputEventPanGesture or event is InputEventScreenDrag):
        return
    if village_scroll == null:
        return
    var bar := village_scroll.get_v_scroll_bar()
    if bar == null:
        return
    # Defer the read so the scroll has actually moved before we sample.
    await get_tree().process_frame
    var distance_from_bottom: int = int(bar.max_value) - village_scroll.scroll_vertical
    village_stick_bottom = distance_from_bottom <= VILLAGE_STICK_BOTTOM_THRESHOLD


# Public entry point used by the top-bar marquee ticker. Opens the
# panel (bypassing the room-huddle gate that the launcher button uses)
# and switches to the Village tab. Idempotent — safe to call when the
# panel is already open on either tab.
func force_open_to_village_tab() -> void:
    if not pc_exists:
        return
    if sheet_anchor == null or talk_launcher == null:
        # Guard against the rare case where the ticker is clicked
        # before the panel has finished its initial build/refresh.
        return
    is_open = true
    user_closed = false
    sheet_anchor.visible = true
    talk_launcher.visible = false
    _set_active_tab(TAB_VILLAGE)


## Auto-open hook for knock-success (main.gd's pc/move response handler).
## _refresh_state runs an HTTPRequest, so huddle_members isn't populated
## yet at the moment we want to open. Defer to the next _on_me_completed
## by setting a one-shot flag — _apply_pc_state honors it after wiring
## state from the response.
var _open_after_next_refresh := false

func force_open_after_refresh() -> void:
    _open_after_next_refresh = true


func _build_input(parent: Control) -> void:
    var row := HBoxContainer.new()
    row.custom_minimum_size = Vector2(0, 28)
    row.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.add_theme_constant_override("separation", 6)
    parent.add_child(row)

    speech_input = TextEdit.new()
    speech_input.placeholder_text = "Speak to those gathered here…"
    speech_input.wrap_mode = TextEdit.LINE_WRAPPING_BOUNDARY
    speech_input.custom_minimum_size = Vector2(0, 28)
    speech_input.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    speech_input.size_flags_vertical = Control.SIZE_SHRINK_CENTER
    speech_input.focus_mode = Control.FOCUS_ALL
    speech_input.mouse_filter = Control.MOUSE_FILTER_STOP
    speech_input.add_theme_font_size_override("font_size", 12)
    # The TextEdit default stylebox draws a thick bordered box that reads
    # heavy next to the rest of the small UI. Override with a flat fill
    # that matches the panel's tone, plus a 1px subtle border on focus.
    var input_normal := StyleBoxFlat.new()
    input_normal.bg_color = Color(0.18, 0.13, 0.08, 0.95)
    input_normal.border_color = Color(0.42, 0.32, 0.19, 0.55)
    input_normal.border_width_left = 1
    input_normal.border_width_right = 1
    input_normal.border_width_top = 1
    input_normal.border_width_bottom = 1
    input_normal.corner_radius_top_left = 4
    input_normal.corner_radius_top_right = 4
    input_normal.corner_radius_bottom_left = 4
    input_normal.corner_radius_bottom_right = 4
    input_normal.content_margin_left = 8
    input_normal.content_margin_right = 8
    input_normal.content_margin_top = 4
    input_normal.content_margin_bottom = 4
    speech_input.add_theme_stylebox_override("normal", input_normal)
    var input_focus := input_normal.duplicate()
    input_focus.border_color = Color(0.78, 0.62, 0.34, 0.9)
    speech_input.add_theme_stylebox_override("focus", input_focus)
    row.add_child(speech_input)

    pay_button = Button.new()
    pay_button.text = "Pay"
    pay_button.custom_minimum_size = Vector2(48, 28)
    pay_button.focus_mode = Control.FOCUS_ALL
    pay_button.mouse_filter = Control.MOUSE_FILTER_STOP
    pay_button.add_theme_font_size_override("font_size", 12)
    pay_button.tooltip_text = "Pay a villager — opens a confirmation form for amount and optional item."
    row.add_child(pay_button)

    speak_button = Button.new()
    speak_button.text = "Speak"
    speak_button.custom_minimum_size = Vector2(60, 28)
    speak_button.focus_mode = Control.FOCUS_ALL
    speak_button.mouse_filter = Control.MOUSE_FILTER_STOP
    speak_button.add_theme_font_size_override("font_size", 12)
    row.add_child(speak_button)


func _connect_signals() -> void:
    talk_launcher.pressed.connect(open)
    close_button.pressed.connect(close)
    speak_button.pressed.connect(_on_speak_pressed)
    if pay_button != null:
        pay_button.pressed.connect(_on_pay_pressed)

    refresh_timer.timeout.connect(_refresh_state)
    http_me.request_completed.connect(_on_me_completed)
    http_speak.request_completed.connect(_on_speak_completed)
    http_village_log.request_completed.connect(_on_village_log_completed)
    if http_pay != null:
        http_pay.request_completed.connect(_on_pay_completed)

    if tab_room_button != null:
        tab_room_button.pressed.connect(_set_active_tab.bind(TAB_ROOM))
    if tab_village_button != null:
        tab_village_button.pressed.connect(_set_active_tab.bind(TAB_VILLAGE))

    speech_input.focus_entered.connect(_on_input_focus_entered)
    speech_input.focus_exited.connect(_on_input_focus_exited)
    speech_input.gui_input.connect(_on_speech_input_gui_input)


func _connect_world_signal() -> void:
    var world := get_node_or_null("/root/Main/World")
    if world == null:
        return
    if world.has_signal("npc_spoke"):
        world.npc_spoke.connect(_on_npc_spoke)
    if world.has_signal("room_event"):
        world.room_event.connect(_on_room_event)
    if world.has_signal("village_event_added"):
        world.village_event_added.connect(_on_village_event_added)


# talk_sheet is the actual visible chat panel (the bottom-right rounded
# rectangle). main.gd registers it with the camera so wheel scrolling
# over the open sheet scrolls the chat log instead of zooming the map.
# Visibility is gated by sheet_anchor; closing the panel hides
# sheet_anchor which makes is_visible_in_tree() return false on
# talk_sheet, so registration "just works" — we don't have to
# re-register on every open/close.
func get_input_eating_control() -> Control:
    return talk_sheet


func open() -> void:
    if not pc_exists or huddle_members.is_empty():
        return

    is_open = true
    user_closed = false
    unread_count = 0

    sheet_anchor.visible = true
    talk_launcher.visible = false

    _update_launcher_text()
    _focus_input_after_open()
    _scroll_log_to_bottom_deferred()


func close() -> void:
    is_open = false
    user_closed = true
    sheet_anchor.visible = false
    _update_visibility_from_state()


func _focus_input_after_open() -> void:
    _pending_focus = true
    call_deferred("_deferred_focus_input")


func _deferred_focus_input() -> void:
    # First focus pass on the next frame after show().
    await get_tree().process_frame

    if not is_open:
        return

    speech_input.grab_focus()
    call_deferred("_second_deferred_focus_input")


func _second_deferred_focus_input() -> void:
    # Second focus pass — HTML5 sometimes drops the first one.
    if not is_open:
        return

    speech_input.grab_focus()
    _pending_focus = false


func _refresh_state() -> void:
    var err := http_me.request(
        _api_url("/api/village/pc/me"),
        _auth_headers(),
        HTTPClient.METHOD_POST,
        ""
    )

    if err != OK:
        push_warning("TalkPanel pc/me request failed: %s" % err)


func _on_me_completed(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    if result != HTTPRequest.RESULT_SUCCESS or response_code < 200 or response_code >= 300:
        _set_no_pc_state()
        return

    var data = JSON.parse_string(body.get_string_from_utf8())
    if typeof(data) != TYPE_DICTIONARY:
        return

    _apply_pc_state(data)


func _apply_pc_state(data: Dictionary) -> void:
    pc_exists = bool(data.get("exists", false))

    if not pc_exists:
        _set_no_pc_state()
        return

    character_name = str(data.get("character_name", ""))
    structure_name = str(data.get("structure_name", ""))
    home_name = str(data.get("home_name", ""))

    # Coins + inventory — surfaced in the top-bar coin chip and used
    # by the pay modal to validate amount against current balance.
    pc_coins = int(data.get("coins", 0))
    var inv_data = data.get("inventory", [])
    pc_inventory = inv_data if typeof(inv_data) == TYPE_ARRAY else []
    _push_purse_to_top_bar()

    var prev_huddle_size: int = huddle_members.size()
    var members = data.get("huddle_members", [])
    if typeof(members) == TYPE_ARRAY:
        huddle_members = members
    else:
        huddle_members = []

    _maybe_apply_recent_speech(data)
    _update_context_labels()
    _update_nearby_chips()
    _update_launcher_text()
    _update_visibility_from_state()
    _maybe_auto_attention_for_first_encounter()

    # Auto-open on huddle gain. Two paths land here:
    # 1. Knock-success: main.gd sets _open_after_next_refresh and we honor
    #    once huddle_members has populated.
    # 2. Natural entry into a structure with someone there (tavern, smithy):
    #    huddle_members goes from empty -> non-empty on the periodic
    #    refresh after npc_arrived inside-flips the PC. Without auto-open
    #    here, the PC enters the tavern, sprite hides, and the player has
    #    to find the launcher pill — easy to miss.
    # user_closed (set when player explicitly hits Close) suppresses both
    # paths so an explicit close isn't immediately reversed.
    # Reset user_closed when the player leaves a huddle (huddle goes
    # empty) — that's the natural session boundary. Without this, a
    # close from an earlier conversation would suppress auto-open
    # forever afterward, even at a fresh location.
    if prev_huddle_size > 0 and huddle_members.is_empty():
        user_closed = false

    var should_auto_open := false
    if _open_after_next_refresh and not huddle_members.is_empty():
        _open_after_next_refresh = false
        should_auto_open = true
    elif prev_huddle_size == 0 and not huddle_members.is_empty() and not user_closed:
        should_auto_open = true
    if should_auto_open:
        open()


# When the player's inside_structure_id changes (or first arrives), clear
# the log and replay the room's recent speech as a backload. The room
# metaphor: walking into a tavern, you hear what's been said here lately.
# Skipped on subsequent polls of the same structure to avoid duplicates
# layering on top of live npc_spoke events.
func _maybe_apply_recent_speech(data: Dictionary) -> void:
    # Use audience_structure_id (the conversational scope) so a PC
    # loitering at a booth — huddle joined, but not formally inside —
    # gets the same backload as a PC sitting at the bar. Falls back to
    # inside_structure_id for older server builds that don't return the
    # new field.
    var current_structure := str(data.get("audience_structure_id", ""))
    if current_structure.is_empty():
        current_structure = str(data.get("inside_structure_id", ""))
    if current_structure == loaded_structure_id:
        return

    loaded_structure_id = current_structure

    # Wipe whatever's there from the previous room — fresh ears for a new
    # space. Live npc_spoke events that were happening in the old place
    # aren't relevant once the PC has moved.
    for child in log_vbox.get_children():
        child.queue_free()

    if current_structure.is_empty():
        return

    var recent = data.get("recent_speech", [])
    if typeof(recent) != TYPE_ARRAY:
        return

    for entry in recent:
        if typeof(entry) != TYPE_DICTIONARY:
            continue
        var speaker := str(entry.get("speaker_name", ""))
        var text := str(entry.get("text", ""))
        var kind := str(entry.get("kind", "npc"))
        var at := str(entry.get("occurred_at", ""))
        if speaker.is_empty() or text.is_empty():
            continue
        _append_log_line(speaker, text, kind, true, at)


func _set_no_pc_state() -> void:
    pc_exists = false
    huddle_members = []
    pc_coins = 0
    pc_inventory = []
    is_open = false
    sheet_anchor.visible = false
    talk_launcher.visible = false
    loaded_structure_id = ""
    _update_context_labels()
    _clear_nearby_chips()
    _push_purse_to_top_bar()


func _update_visibility_from_state() -> void:
    if not pc_exists or huddle_members.is_empty():
        is_open = false
        sheet_anchor.visible = false
        talk_launcher.visible = false
        return

    if is_open:
        sheet_anchor.visible = true
        talk_launcher.visible = false
    else:
        sheet_anchor.visible = false
        talk_launcher.visible = true


func _update_context_labels() -> void:
    if not pc_exists:
        context_label.text = ""
        subcontext_label.text = ""
        return

    var where := "the village"
    if not structure_name.is_empty():
        where = structure_name

    context_label.text = where
    subcontext_label.text = ""


func _update_nearby_chips() -> void:
    _clear_nearby_chips()

    for member in huddle_members:
        if typeof(member) != TYPE_DICTIONARY:
            continue

        var member_name := str(member.get("name", "Unknown"))
        var role := str(member.get("role", ""))
        var chip_text := member_name
        if not role.is_empty():
            chip_text = "%s · %s" % [member_name, role]
        nearby_flow.add_child(_make_chip(chip_text))


func _clear_nearby_chips() -> void:
    for child in nearby_flow.get_children():
        child.queue_free()


func _make_chip(text: String) -> Control:
    # PanelContainer + Label is more reliable than a Label with a stylebox
    # override, especially across HTML5 themes.
    var panel := PanelContainer.new()
    panel.mouse_filter = Control.MOUSE_FILTER_IGNORE

    var style := StyleBoxFlat.new()
    style.bg_color = Color(0.18, 0.13, 0.08, 0.95)
    style.border_color = Color(0.42, 0.32, 0.19, 0.95)
    style.border_width_left = 1
    style.border_width_right = 1
    style.border_width_top = 1
    style.border_width_bottom = 1
    style.corner_radius_top_left = 999
    style.corner_radius_top_right = 999
    style.corner_radius_bottom_left = 999
    style.corner_radius_bottom_right = 999
    style.content_margin_left = 7
    style.content_margin_right = 7
    style.content_margin_top = 1
    style.content_margin_bottom = 1
    panel.add_theme_stylebox_override("panel", style)

    var label := Label.new()
    label.text = text
    label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    label.add_theme_color_override("font_color", Color(0.78, 0.68, 0.50, 1.0))
    label.add_theme_font_size_override("font_size", 11)
    panel.add_child(label)

    return panel


func _on_speak_pressed() -> void:
    _send_current_text()


func _on_speech_input_gui_input(event: InputEvent) -> void:
    # Enter sends; Shift+Enter inserts a newline.
    if event is InputEventKey:
        var key := event as InputEventKey
        if key.pressed and not key.echo:
            if key.keycode == KEY_ENTER or key.keycode == KEY_KP_ENTER:
                if key.shift_pressed:
                    return

                get_viewport().set_input_as_handled()
                _send_current_text()


func _send_current_text() -> void:
    var text := speech_input.text.strip_edges()
    if text.is_empty():
        _refocus_if_open()
        return

    speech_input.text = ""
    # No local echo — /pc/speak writes an audit_log row that fans out via the
    # npc_spoke WS event, which _on_npc_spoke renders. Local echo here would
    # duplicate the line as "You" and then the player's character name.
    _post_speak(text)
    _refocus_if_open()


func _post_speak(text: String) -> void:
    var body := JSON.stringify({ "text": text })

    speak_button.disabled = true

    var err := http_speak.request(
        _api_url("/api/village/pc/speak"),
        _auth_headers(),
        HTTPClient.METHOD_POST,
        body
    )

    if err != OK:
        speak_button.disabled = false
        push_warning("TalkPanel speak request failed: %s" % err)


## Emit purse_changed so main.gd can update the top bar's coin chip
## and inventory tooltip. Negative coins signals "no PC" — top bar
## hides the chip on that.
func _push_purse_to_top_bar() -> void:
    var lines: PackedStringArray = PackedStringArray()
    if not pc_exists:
        purse_changed.emit(-1, lines)
        return
    for entry in pc_inventory:
        if typeof(entry) != TYPE_DICTIONARY:
            continue
        var label := str(entry.get("display_label", entry.get("item_kind", "")))
        var qty := int(entry.get("quantity", 0))
        if label != "" and qty > 0:
            lines.append("%s × %d" % [label, qty])
    purse_changed.emit(pc_coins, lines)


## Build the pay modal lazily on first use. Lives as a CanvasLayer
## sibling so it overlays the talk sheet and the world view; semi-
## transparent backdrop swallows clicks outside the form so a misclick
## doesn't dismiss it accidentally (Cancel button is the only way out).
func _ensure_pay_modal_built() -> void:
    if pay_modal != null:
        return

    var layer := CanvasLayer.new()
    layer.layer = 30  # above the talk sheet's layer
    layer.visible = false
    add_child(layer)
    pay_modal = layer

    var backdrop := ColorRect.new()
    backdrop.color = Color(0, 0, 0, 0.6)
    backdrop.set_anchors_preset(Control.PRESET_FULL_RECT)
    backdrop.mouse_filter = Control.MOUSE_FILTER_STOP
    layer.add_child(backdrop)

    var center := CenterContainer.new()
    center.set_anchors_preset(Control.PRESET_FULL_RECT)
    layer.add_child(center)

    var panel := PanelContainer.new()
    panel.custom_minimum_size = Vector2(360, 0)
    var panel_style := StyleBoxFlat.new()
    panel_style.bg_color = Color(0.13, 0.10, 0.07, 0.98)
    panel_style.border_color = Color(0.55, 0.42, 0.25, 1.0)
    panel_style.border_width_left = 1
    panel_style.border_width_right = 1
    panel_style.border_width_top = 1
    panel_style.border_width_bottom = 1
    panel_style.corner_radius_top_left = 6
    panel_style.corner_radius_top_right = 6
    panel_style.corner_radius_bottom_left = 6
    panel_style.corner_radius_bottom_right = 6
    panel_style.content_margin_left = 16
    panel_style.content_margin_right = 16
    panel_style.content_margin_top = 14
    panel_style.content_margin_bottom = 14
    panel.add_theme_stylebox_override("panel", panel_style)
    center.add_child(panel)

    var vbox := VBoxContainer.new()
    vbox.add_theme_constant_override("separation", 8)
    panel.add_child(vbox)

    var title := Label.new()
    title.text = "Settle a payment"
    title.add_theme_color_override("font_color", Color(0.92, 0.78, 0.42))
    title.add_theme_font_size_override("font_size", 16)
    vbox.add_child(title)

    pay_recipient_option = OptionButton.new()
    vbox.add_child(_label_with("Recipient:", pay_recipient_option))

    pay_amount_spin = SpinBox.new()
    pay_amount_spin.min_value = 0
    pay_amount_spin.max_value = 999
    pay_amount_spin.step = 1
    pay_amount_spin.value = 1
    vbox.add_child(_label_with("Amount (coins):", pay_amount_spin))

    pay_item_input = LineEdit.new()
    pay_item_input.placeholder_text = "(optional — e.g. ale, tonic, hook)"
    vbox.add_child(_label_with("Item:", pay_item_input))

    pay_qty_spin = SpinBox.new()
    pay_qty_spin.min_value = 1
    pay_qty_spin.max_value = 99
    pay_qty_spin.step = 1
    pay_qty_spin.value = 1
    vbox.add_child(_label_with("Quantity:", pay_qty_spin))

    pay_take_home_check = CheckBox.new()
    pay_take_home_check.text = "Take it home (don't consume now)"
    vbox.add_child(pay_take_home_check)

    pay_status_label = Label.new()
    pay_status_label.text = ""
    pay_status_label.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    pay_status_label.custom_minimum_size = Vector2(320, 0)
    pay_status_label.add_theme_color_override("font_color", Color(0.92, 0.50, 0.42))
    pay_status_label.add_theme_font_size_override("font_size", 12)
    vbox.add_child(pay_status_label)

    var button_row := HBoxContainer.new()
    button_row.alignment = BoxContainer.ALIGNMENT_END
    button_row.add_theme_constant_override("separation", 8)
    vbox.add_child(button_row)

    pay_cancel_button = Button.new()
    pay_cancel_button.text = "Cancel"
    pay_cancel_button.pressed.connect(_close_pay_modal)
    button_row.add_child(pay_cancel_button)

    pay_confirm_button = Button.new()
    pay_confirm_button.text = "Confirm"
    pay_confirm_button.pressed.connect(_on_pay_confirm)
    button_row.add_child(pay_confirm_button)


## Helper: a horizontal row with a small label + the input control.
## Keeps the pay modal layout uniform without hand-tuning each spacing.
func _label_with(text: String, control: Control) -> Control:
    var row := HBoxContainer.new()
    row.add_theme_constant_override("separation", 8)
    var lbl := Label.new()
    lbl.text = text
    lbl.add_theme_color_override("font_color", Color(0.78, 0.68, 0.50))
    lbl.add_theme_font_size_override("font_size", 13)
    lbl.custom_minimum_size = Vector2(120, 0)
    row.add_child(lbl)
    control.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.add_child(control)
    return row


func _on_pay_pressed() -> void:
    _ensure_pay_modal_built()
    # Repopulate the recipient list each open — huddle membership
    # changes (NPCs come and go) so the cached list at modal-build time
    # would go stale fast.
    pay_modal_recipients.clear()
    pay_recipient_option.clear()
    for member in huddle_members:
        if typeof(member) != TYPE_DICTIONARY:
            continue
        var name := str(member.get("name", ""))
        if name.is_empty() or name == character_name:
            continue
        pay_modal_recipients.append(name)
        pay_recipient_option.add_item(name)
    if pay_modal_recipients.is_empty():
        pay_status_label.text = "Nobody here to pay."
    else:
        pay_status_label.text = ""
    pay_amount_spin.value = 1
    pay_item_input.text = ""
    pay_qty_spin.value = 1
    pay_take_home_check.button_pressed = false
    pay_modal.visible = true


func _close_pay_modal() -> void:
    if pay_modal != null:
        pay_modal.visible = false


func _on_pay_confirm() -> void:
    if pay_modal_recipients.is_empty():
        return
    var idx: int = pay_recipient_option.selected
    if idx < 0 or idx >= pay_modal_recipients.size():
        return
    var recipient: String = pay_modal_recipients[idx]
    var amount := int(pay_amount_spin.value)
    var item := pay_item_input.text.strip_edges().to_lower()
    var qty := int(pay_qty_spin.value)
    var consume_now := not pay_take_home_check.button_pressed

    if amount < 0:
        pay_status_label.text = "Amount cannot be negative."
        return
    if amount > pc_coins:
        pay_status_label.text = "You only have %d coins." % pc_coins
        return

    var body := {
        "recipient": recipient,
        "amount": amount,
        "consume_now": consume_now,
    }
    if item != "":
        body["item"] = item
        body["qty"] = qty
    pay_status_label.text = "Sending…"
    pay_confirm_button.disabled = true

    var headers: PackedStringArray = _auth_headers()
    var json := JSON.stringify(body)
    var err := http_pay.request(
        _api_url("/api/village/pc/pay"),
        headers,
        HTTPClient.METHOD_POST,
        json,
    )
    if err != OK:
        pay_status_label.text = "Request failed (%s)." % err
        pay_confirm_button.disabled = false


func _on_pay_completed(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    pay_confirm_button.disabled = false
    if result != HTTPRequest.RESULT_SUCCESS:
        pay_status_label.text = "Network error."
        return
    var raw := body.get_string_from_utf8()
    var parsed = JSON.parse_string(raw)
    if response_code < 200 or response_code >= 300:
        var msg := "Server error %d." % response_code
        if typeof(parsed) == TYPE_DICTIONARY:
            msg = str(parsed.get("error", msg))
        pay_status_label.text = msg
        return
    if typeof(parsed) != TYPE_DICTIONARY:
        pay_status_label.text = "Bad response."
        return
    var status := str(parsed.get("result", ""))
    if status != "ok":
        pay_status_label.text = str(parsed.get("error", "Rejected."))
        return
    # Success — close the modal and re-poll /pc/me so the coin chip
    # and inventory snapshot refresh immediately. The room_event
    # broadcast will surface the line in the room log on its own.
    _close_pay_modal()
    _refresh_state()


func _on_speak_completed(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    speak_button.disabled = false

    if result != HTTPRequest.RESULT_SUCCESS or response_code < 200 or response_code >= 300:
        push_warning("TalkPanel speak failed: code=%s body=%s" % [
            response_code,
            body.get_string_from_utf8()
        ])


func _on_npc_spoke(_npc_id: String, speaker_name: String, text: String, kind: String = "", at: String = "", structure_id: String = "") -> void:
    # WS speech kinds are "npc" | "player"; normalize to the panel's
    # speech_npc / speech_player kinds so render logic is uniform with
    # the backload entries. npc_id is unused here — speech bubbles
    # consume it instead. `at` is an ISO timestamp from the broadcast;
    # _format_timestamp converts to a short clock-time prefix.
    #
    # Structure filter mirrors _on_room_event: speech that happened in
    # a different room than the player's current conversational scope
    # gets dropped. Empty structure_id is from outdoor speech with no
    # structure context — show it only when the player is also outside
    # (loaded_structure_id empty). Older server builds without the
    # field still flow through (treated as outdoor) until everyone's
    # on the structure-stamped version.
    if structure_id != loaded_structure_id:
        return
    var panel_kind := "speech_player" if kind == "player" else "speech_npc"
    _append_log_line(speaker_name, text, panel_kind, false, at)


# Generic room-event handler. Engine emits these for narration-worthy
# things that aren't speech (acts, departures, eventually arrivals/pays).
# Each event scopes itself to a structure_id; we ignore events outside
# the room the player is currently in.
func _on_room_event(data: Dictionary) -> void:
    var event_structure := str(data.get("structure_id", ""))
    if event_structure != loaded_structure_id:
        return
    var actor_name := str(data.get("actor_name", ""))
    var text := str(data.get("text", ""))
    var kind := str(data.get("kind", "act"))
    var at := str(data.get("at", ""))
    if actor_name.is_empty() or text.is_empty():
        return
    _append_log_line(actor_name, text, kind, false, at)


## Public entry for client-only narrations (e.g. ZBBS-101 knock outcomes).
## Renders as a dimmer narration line in the main log without round-
## tripping through the server's room_event broadcast — there's no
## audience for a knock besides the player who issued it. The panel's
## open() requires a huddle, which the player typically does NOT have
## at the moment of a knock; bumps the unread counter and surfaces a
## brief banner so the player notices the response even when the sheet
## is closed.
func append_local_narration(text: String) -> void:
    if text.is_empty():
        return
    _append_log_line("", text, "act")
    if not is_open:
        unread_count += 1
        _update_launcher_text()


func _append_log_line(speaker: String, text: String, kind: String = "", is_backload: bool = false, at: String = "") -> void:
    var was_at_bottom := _is_log_near_bottom()

    # Timestamp prefix — short clock format like "[3:47p]". Dimmed gray so
    # it sits visually behind the speech content. Empty when no `at` was
    # provided (defensive — every server broadcast carries one, but
    # client-only narrations like knock outcomes have no real time).
    var time_prefix := _format_timestamp(at)

    # Narration kinds render as a single dimmer line — text is pre-
    # rendered server-side and embeds the actor's name, so no separate
    # name label. Speech kinds render as name + quoted text, color-coded
    # for player vs NPC. Anything that isn't a known speech kind falls
    # through to narration so future server-side kinds (give, take, etc.)
    # don't need a client patch to render.
    var is_speech := kind == "speech_npc" or kind == "speech_player" or kind == "npc" or kind == "player"
    var is_narration := not is_speech

    var entry: Node
    if is_narration:
        # Narration uses RichTextLabel so the time prefix can carry its
        # own color span (slightly dimmer than the narration text itself).
        var rich := RichTextLabel.new()
        rich.bbcode_enabled = true
        rich.fit_content = true
        rich.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
        rich.scroll_active = false
        rich.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        rich.add_theme_font_size_override("normal_font_size", 13)
        var prefix := ""
        if time_prefix != "":
            prefix = "[color=#7a6f59]%s[/color] " % time_prefix
        rich.text = "%s[color=#a09377]%s[/color]" % [prefix, _bbcode_escape(text)]
        entry = rich
    else:
        # Inline speaker + quote on one line with per-span colors. Plain
        # Label can't carry two colors and HBoxContainer+autowrap is
        # awkward, so RichTextLabel with bbcode is the clean fit.
        # fit_content makes the label size to its content height so the
        # log_vbox layout still flows; scroll_active=false keeps the
        # outer log_scroll as the only scroll surface.
        var rich := RichTextLabel.new()
        rich.bbcode_enabled = true
        rich.fit_content = true
        rich.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
        rich.scroll_active = false
        rich.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        rich.add_theme_font_size_override("normal_font_size", 13)

        var name_color: String = "#b39463"
        var text_color: String = "#d1c2a3"
        if kind == "speech_player" or kind == "player":
            name_color = "#f2c773"
            text_color = "#f2dbad"

        var prefix := ""
        if time_prefix != "":
            prefix = "[color=#7a6f59]%s[/color] " % time_prefix
        rich.text = "%s[color=%s]%s[/color] [color=%s]“%s”[/color]" % [
            prefix,
            name_color,
            _bbcode_escape(speaker),
            text_color,
            _bbcode_escape(text),
        ]
        entry = rich

    log_vbox.add_child(entry)

    while log_vbox.get_child_count() > MAX_LOG_LINES:
        log_vbox.get_child(0).queue_free()

    if is_open:
        if was_at_bottom:
            _scroll_log_to_bottom_deferred()
    elif not is_backload:
        # Backload entries are historical, not new — don't pulse the
        # launcher's "N new" badge as if they just happened.
        unread_count += 1
        _update_launcher_text()


## Escape any opening square brackets in a user-supplied string so they
## don't get interpreted as BBCode start tags. RichTextLabel renders
## [lb] as a literal '[', and a stray ']' on its own is harmless.
func _bbcode_escape(s: String) -> String:
    return s.replace("[", "[lb]")


## Format an ISO timestamp ("2026-05-02T20:08:16Z") as a short clock-time
## prefix in local time ("[3:08p]" / "[11:42a]"). Returns empty string
## for empty / unparseable input so callers can suppress the prefix
## entirely.
##
## get_unix_time_from_datetime_string treats unsuffixed ISO as UTC and
## get_datetime_dict_from_unix_time returns UTC fields, so we offset by
## the system timezone to render local clock time.
func _format_timestamp(at: String) -> String:
    if at.is_empty():
        return ""
    var unix := Time.get_unix_time_from_datetime_string(at)
    if unix <= 0:
        return ""
    var tz: Dictionary = Time.get_time_zone_from_system()
    var bias_minutes: int = tz.get("bias", 0)
    var local_unix: int = int(unix) + bias_minutes * 60
    var local := Time.get_datetime_dict_from_unix_time(local_unix)
    var hour: int = local.get("hour", 0)
    var minute: int = local.get("minute", 0)
    var meridiem := "a" if hour < 12 else "p"
    var display_hour := hour
    if display_hour == 0:
        display_hour = 12
    elif display_hour > 12:
        display_hour -= 12
    return "[%d:%02d%s]" % [display_hour, minute, meridiem]


func _is_log_near_bottom() -> bool:
    var bar := log_scroll.get_v_scroll_bar()
    if bar == null:
        return true

    return bar.value >= bar.max_value - bar.page - 24.0


func _scroll_log_to_bottom_deferred() -> void:
    call_deferred("_scroll_log_to_bottom")


func _scroll_log_to_bottom() -> void:
    # Three frames: one for visibility-change to take effect, one for the
    # PanelContainer layout to settle, one for RichTextLabel fit_content
    # to expand log entries to their rendered heights. With only one
    # await, max_value lags reality on first open() and the scroll lands
    # short of the actual bottom.
    await get_tree().process_frame
    await get_tree().process_frame
    await get_tree().process_frame
    var bar := log_scroll.get_v_scroll_bar()
    if bar != null:
        bar.value = bar.max_value


func _update_launcher_text() -> void:
    var count := huddle_members.size()

    if count <= 0:
        talk_launcher.text = "Talk"
    elif unread_count > 0:
        talk_launcher.text = "Talk (%d nearby) • %d new" % [count, unread_count]
    else:
        talk_launcher.text = "Talk (%d nearby)" % count


func _maybe_auto_attention_for_first_encounter() -> void:
    if huddle_members.is_empty():
        return

    if has_ever_seen_huddle:
        return

    has_ever_seen_huddle = true

    if not first_encounter_pulse_done:
        first_encounter_pulse_done = true
        call_deferred("_pulse_launcher")


func _pulse_launcher() -> void:
    if not is_instance_valid(talk_launcher):
        return

    await get_tree().process_frame

    talk_launcher.pivot_offset = talk_launcher.size * 0.5

    var tween := create_tween()
    tween.set_loops(3)
    tween.tween_property(talk_launcher, "scale", Vector2(1.06, 1.06), 0.22)
    tween.tween_property(talk_launcher, "scale", Vector2.ONE, 0.22)


func _on_input_focus_entered() -> void:
    input_focused = true
    _update_responsive_layout()

    if is_mobile:
        call_deferred("_mobile_refit_after_keyboard")


func _on_input_focus_exited() -> void:
    input_focused = false
    _update_responsive_layout()


func _mobile_refit_after_keyboard() -> void:
    # Two frames: first lets the OS keyboard come up, second lets the resulting
    # viewport size_changed propagate before we re-fit.
    await get_tree().process_frame
    await get_tree().process_frame

    if is_mobile and is_open:
        _update_responsive_layout()


func _refocus_if_open() -> void:
    if is_open:
        call_deferred("_second_deferred_focus_input")


func _on_viewport_size_changed() -> void:
    _update_responsive_layout()


func _update_responsive_layout() -> void:
    var size := get_viewport().get_visible_rect().size
    is_mobile = size.x <= MOBILE_BREAKPOINT

    if is_mobile:
        launcher_anchor.add_theme_constant_override("margin_left", 12)
        launcher_anchor.add_theme_constant_override("margin_right", 12)
        launcher_anchor.add_theme_constant_override("margin_top", 12)
        launcher_anchor.add_theme_constant_override("margin_bottom", 14)

        sheet_anchor.add_theme_constant_override("margin_left", 0)
        sheet_anchor.add_theme_constant_override("margin_right", 0)
        sheet_anchor.add_theme_constant_override("margin_top", 0)
        sheet_anchor.add_theme_constant_override("margin_bottom", 0)

        talk_sheet.size_flags_horizontal = Control.SIZE_EXPAND_FILL

        var vh := MOBILE_SHEET_VH
        if input_focused:
            vh = MOBILE_SHEET_FOCUSED_VH
        talk_sheet.custom_minimum_size = Vector2(0, max(320.0, size.y * vh))

        talk_launcher.custom_minimum_size = Vector2(180, 52)
    else:
        launcher_anchor.add_theme_constant_override("margin_left", 18)
        launcher_anchor.add_theme_constant_override("margin_right", 22)
        launcher_anchor.add_theme_constant_override("margin_top", 18)
        launcher_anchor.add_theme_constant_override("margin_bottom", 22)

        sheet_anchor.add_theme_constant_override("margin_left", 18)
        sheet_anchor.add_theme_constant_override("margin_right", 22)
        sheet_anchor.add_theme_constant_override("margin_top", 18)
        sheet_anchor.add_theme_constant_override("margin_bottom", 22)

        talk_sheet.size_flags_horizontal = Control.SIZE_SHRINK_END
        talk_sheet.custom_minimum_size = Vector2(DESKTOP_MIN_WIDTH, DESKTOP_MIN_HEIGHT)

        talk_launcher.custom_minimum_size = Vector2(150, 48)


func _apply_theme() -> void:
    var sheet_style := StyleBoxFlat.new()
    sheet_style.bg_color = Color(0.115, 0.085, 0.055, 0.94)
    sheet_style.border_color = Color(0.55, 0.42, 0.24, 0.95)
    sheet_style.border_width_left = 1
    sheet_style.border_width_right = 1
    sheet_style.border_width_top = 1
    sheet_style.border_width_bottom = 1
    sheet_style.corner_radius_top_left = 10
    sheet_style.corner_radius_top_right = 10
    sheet_style.corner_radius_bottom_left = 10
    sheet_style.corner_radius_bottom_right = 10
    sheet_style.shadow_color = Color(0, 0, 0, 0.45)
    sheet_style.shadow_size = 18
    sheet_style.shadow_offset = Vector2(0, 6)
    talk_sheet.add_theme_stylebox_override("panel", sheet_style)

    context_label.add_theme_color_override("font_color", Color(0.95, 0.82, 0.58, 1.0))
    subcontext_label.add_theme_color_override("font_color", Color(0.68, 0.58, 0.43, 1.0))
    speech_input.add_theme_color_override("font_color", Color(0.92, 0.84, 0.70, 1.0))
    speech_input.add_theme_color_override("font_placeholder_color", Color(0.58, 0.50, 0.40, 1.0))


func _api_url(path: String) -> String:
    var auth := get_node_or_null("/root/Auth")
    if auth == null:
        return path

    return str(auth.api_base).rstrip("/") + path


func _auth_headers() -> PackedStringArray:
    return Auth.auth_headers()
