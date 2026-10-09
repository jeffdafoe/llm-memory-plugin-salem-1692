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
##   - /root/Main/World.npc_spoke(npc_id, name, text, kind, at, structure_id, mentions, speaker_x, speaker_y, room_id, addressee_id, addressee_name) signal
##   - POST /api/village/pc/me  → state (includes self x/y, used to filter outdoor speech by distance)
##   - POST /api/village/pc/speak {text}  → broadcast (indoor: huddle scope; outdoor: proximity broadcast with speaker_x/speaker_y)

const PayPanelScript = preload("res://scripts/pay_panel.gd")

const REFRESH_INTERVAL := 10.0
# ZBBS-WORK-399: Village tab poll cadence. Runs only while the tab is the
# active view; each poll is an incremental ?since= fetch, so the steady-state
# response is tiny.
const VILLAGE_POLL_INTERVAL := 5.0
# Default panel size on first open / when no persisted size exists.
const DESKTOP_MIN_WIDTH := 580.0
const DESKTOP_MIN_HEIGHT := 400.0
# Lower floor the user can drag the panel down to via the resize grip.
# Smaller than the default opening size so the user can actually shrink
# the panel — clamping resize to DESKTOP_MIN_* would prevent that.
const DESKTOP_RESIZE_MIN_WIDTH := 360.0
const DESKTOP_RESIZE_MIN_HEIGHT := 240.0
const MOBILE_BREAKPOINT := 720.0
const MOBILE_SHEET_VH := 0.60
const MOBILE_SHEET_FOCUSED_VH := 0.82
const MAX_LOG_LINES := 80

var root: Control

var launcher_anchor: MarginContainer
var talk_launcher: Button

var sheet_anchor: MarginContainer
var talk_sheet: PanelContainer

# Top-left resize grip (desktop only). 14×14 Control with the
# diagonal-arrow cursor; drag mutates talk_sheet.custom_minimum_size
# clamped to (DESKTOP_RESIZE_MIN_WIDTH × DESKTOP_RESIZE_MIN_HEIGHT)
# floor and the viewport ceiling. Top-level positioned so it tracks
# the sheet's top-left corner regardless of layout reflows.
var resize_grip: Control = null
var _resize_dragging: bool = false
var _resize_start_mouse: Vector2 = Vector2.ZERO
var _resize_start_size: Vector2 = Vector2.ZERO
const RESIZE_GRIP_SIZE: float = 14.0
# Persisted user-chosen panel size (desktop only). Loaded from
# user://talk_panel_size.json on _build_sheet, saved on each drag-end.
# Vector2.ZERO means "no override; use defaults from
# _update_responsive_layout".
var _persisted_panel_size: Vector2 = Vector2.ZERO
const TALK_PANEL_SIZE_PATH := "user://talk_panel_size.json"

var context_label: Label
var subcontext_label: Label
var close_button: Button
var nearby_scroll: ScrollContainer
var nearby_flow: HFlowContainer
var log_scroll: ScrollContainer
var log_vbox: VBoxContainer
var speech_input: TextEdit
var speak_button: Button

# ZBBS-WORK-399: admin-only Village tab — a village-wide action-log feed for
# troubleshooting, fed by GET /api/village/activity/recent. The route is
# operator-gated server-side (the same plugins/administer capability that
# drives Auth.can_edit), so the tab buttons are purely presentation: hidden
# for non-admins, but a modified client still gets 403s. Read-only view —
# no nearby chips, no speech input. Distinct from the dead v1 Village tab
# (ZBBS-HOME-313 removed it): this one polls, no WS frames involved.
var village_tab_active := false
var room_tab_button: Button = null
var village_tab_button: Button = null
var village_scroll: ScrollContainer = null
var village_vbox: VBoxContainer = null
var village_poll_timer: Timer = null
var http_village: HTTPRequest = null
var village_loading := false
# Newest seq rendered, echoed back as ?since_seq= so each poll only returns
# what's new. Seq (not occurred_at): timestamps can collide within an engine
# batch, and the server's strictly-greater filter would drop same-instant
# stragglers. 0 = no cursor yet → the server sends a newest-tail backload.
var village_since_seq := 0
var input_row: HBoxContainer = null
## Pay flow — the Pay button next to Speak opens the Pay box (pay_panel.gd,
## LLM-722). The box reads this panel's huddle roster, purse, lodging and the
## vendor mention accumulators below through its `host`.
var pay_button: Button = null
var pay_panel: CanvasLayer = null

# Phase C of sales-and-gifts: per-vendor accumulator of item_kinds
# they've mentioned in this huddle session. Sourced from npc_spoke's
# mentions field. Lives across the talk panel's open state but resets
# when the huddle changes — fresh conversation, fresh dropdown.
# Shape: { speaker_name: PackedStringArray of unique lowercase item_kinds }
var vendor_mentions: Dictionary = {}
# Companion to vendor_mentions: per-vendor map of item_kind → unit_price
# from the speaker's scene_quote rows in this huddle. Sourced from
# npc_spoke's mention_prices field. Used to pre-fill the pay-modal
# amount when the vendor has quoted a price for the discussed item.
# Resets on huddle change alongside vendor_mentions.
# Shape: { speaker_name: { item_kind: int unit_price } }
var vendor_mention_prices: Dictionary = {}
# ZBBS-HOME-238: separate cache for the LATEST speak's mentions (not
# accumulated). The pay-modal pre-fill wants "is the vendor currently
# narrowed to a single item?" — the accumulated vendor_mentions set
# above stays stuck wide once the vendor's first speak listed several
# options ("I have ale, water, bread, cheese"), even after a follow-up
# narrows to one ("the cheese is 5 coins"). Updated only on speaks
# with non-empty mentions so a chatter speak ("how's business?") doesn't
# clobber the latest quote context. Resets on huddle change.
# Shape: { speaker_name: PackedStringArray of lowercase item_kinds from
# the most recent mention-bearing speak }
var vendor_latest_mentions: Dictionary = {}
## Last /pc/me snapshot of coins + inventory. Reused by the modal to
## show the player what they can afford.
var pc_coins: int = 0
var pc_inventory: Array = []
## Last /pc/me snapshot of the PC's lodging (LLM-38): {inn_name, until_label,
## expires_at}, or empty when the PC holds no room. Drives the top-bar chip and
## the pay modal's empty-state copy.
var pc_lodging: Dictionary = {}
## Last /pc/me snapshot of needs (hunger / thirst / tiredness, each 0..24).
## Forwarded to the top-bar's HUD readout. Empty when no PC.
var pc_needs: Dictionary = {}

## Emitted whenever the polled /pc/me reports a fresh character_name.
## Empty string signals "no PC" — the top bar reverts the name label
## to the login username on that. main.gd is the subscriber.
signal character_name_changed(name: String)

## Emitted with the structured pcInventoryEntry array on every /pc/me
## poll where pc_exists. Empty array signals "no PC" or empty pack.
## Powers the top-bar inventory panel (richer than the formatted-line
## payload of purse_changed).
signal inventory_changed(items: Array)

## Emitted whenever the polled /pc/me reports a fresh coin / inventory
## state. main.gd subscribes and forwards to the top-bar's set_purse.
## Negative coins signals "no PC" (top bar should hide the chip).
signal purse_changed(coins: int, inventory_lines: PackedStringArray)

## Emitted whenever /pc/me reports a new audience scope (structure or
## room change). World.gd listens to gate world-view speech bubbles by
## the same room scope the talk panel uses for its log filter — without
## this, bubbles for NPCs in private bedrooms still leak to PCs outside
## the bedroom even though the talk panel correctly drops the line.
signal audience_scope_changed(structure_id: String, room_id: String)
## Emitted whenever /pc/me reports fresh body-need values. main.gd
## forwards to top_bar.set_needs. Empty dictionary signals "no PC"
## (top bar should hide the readout).
signal needs_changed(needs: Dictionary)
## Emitted whenever /pc/me reports a fresh dwelling_attributes list —
## the attributes the PC is currently recovering via dwell. ZBBS-HOME-218.
## Top bar's HUD uses this to engage a continuous pulse on the matching
## need segment without waiting for a value-change to be detected
## client-side, so the visual signal is present immediately on a fresh
## page load.
signal dwelling_attributes_changed(attrs: PackedStringArray)

## Emitted whenever /pc/me reports the PC's lodging (LLM-38). inn_name +
## until_label are empty when the PC holds no room. main.gd forwards to the top
## bar's "Lodged at …" chip.
signal lodging_changed(inn_name: String, until_label: String)

## Emitted when the pay modal opens/closes. main.gd forwards to
## camera.modal_open so world clicks (PC walk, pan, zoom) don't fire
## while the modal is up — without this, a click on the Confirm
## button bleeds into the world handler and walks the PC underneath.
signal modal_open_changed(open: bool)

## Emitted when the PC's arrival thought at a damaged site carries the town's
## repair offer (LLM-690: object_condition's `repair` field). main.gd opens the
## repair panel on it.
signal repair_offered(offer: Dictionary)

# Same stick-bottom invariant for the room log. Cleared when the user
# scrolls up to read history; re-armed when they scroll back down or
# when an explicit follow-the-bottom path runs (e.g. open(), or a new
# speech entry while was_at_bottom).
var log_stick_bottom: bool = true
const LOG_STICK_BOTTOM_THRESHOLD: int = 24

var refresh_timer: Timer
var http_me: HTTPRequest
var http_speak: HTTPRequest

var pc_exists := false
var character_name := ""
# PC's actor_id from /pc/me — used to compare against deliberation
# broadcasts' addressee_id so the panel knows when speech is directed
# at the player vs another NPC. Compared as id, not display_name, since
# display names can collide and casing/normalization may differ.
var pc_actor_id := ""
var structure_name := ""
var home_name := ""
var huddle_members: Array = []
# Co-present sleepers (pc/me dormant_members) rendered as passive "(asleep)" chips
# so an indoor sleeper with no visible map sprite is still legible. Kept SEPARATE
# from huddle_members on purpose — these are NOT talk/pay targets (a sleeper is
# out of the audience, server pcDormantRoster), so they must never feed
# the Pay box's recipients.
var dormant_members: Array = []
# Self position from the latest /pc/me snapshot — used to filter outdoor
# npc_spoke broadcasts by Chebyshev distance (drop speech from PCs more
# than OUTDOOR_SPEECH_RANGE tiles away when we're both outside).
var pc_x: float = 0.0
var pc_y: float = 0.0
const OUTDOOR_SPEECH_RANGE: int = 6

# Tracks which structure's recent_speech we've already loaded so we don't
# duplicate the backload across the 10s polling cycle. When the player walks
# into a new structure (or up to a new booth), this changes and we
# clear+reload the log. Sourced from /pc/me's audience_structure_id, which
# falls back to the huddle's structure when the PC is loitering outdoors at
# a booth — that way a doorstep conversation shows the same room view a
# bar-stool conversation does.
var loaded_structure_id := ""

# Subspace-aware scope. Empty when the PC is in a common room or outdoors;
# set to the PC's inside_room_id when the PC is in a private/staff room.
# Pairs with loaded_structure_id: a private-room PC only hears speech with
# matching room_id, a common/outdoor PC only hears speech with empty
# room_id. Without this, a PC in Tavern→bedroom_1 would still hear the
# tavern common room (structure_id matches; no further scope filter).
# Sourced from /pc/me's audience_room_id.
var loaded_room_id := ""

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

    # Timeouts (LLM-361): a request that hangs (server restart mid-flight,
    # stalled proxy) otherwise wedges its HTTPRequest node forever — every
    # later poll bounces off ERR_BUSY. Polled requests time out under their
    # own cadence so the next tick gets a free node; user-action requests
    # get a looser bound.
    http_me = HTTPRequest.new()
    http_me.timeout = 8.0
    add_child(http_me)

    http_speak = HTTPRequest.new()
    http_speak.timeout = 15.0
    add_child(http_speak)

    pay_panel = PayPanelScript.new()
    pay_panel.host = self
    add_child(pay_panel)
    pay_panel.open_changed.connect(func(open: bool): modal_open_changed.emit(open))
    pay_panel.offer_pending.connect(_on_pay_offer_pending)
    pay_panel.paid.connect(_refresh_state)
    pay_panel.gave.connect(_on_pay_gave)

    http_village = HTTPRequest.new()
    http_village.timeout = 4.0
    add_child(http_village)

    refresh_timer = Timer.new()
    refresh_timer.wait_time = REFRESH_INTERVAL
    refresh_timer.one_shot = false
    add_child(refresh_timer)

    village_poll_timer = Timer.new()
    village_poll_timer.wait_time = VILLAGE_POLL_INTERVAL
    village_poll_timer.one_shot = false
    add_child(village_poll_timer)


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
    _build_nearby(vbox)
    _build_log(vbox)
    _build_village_log(vbox)
    _build_input(vbox)
    _build_resize_grip()
    _restore_persisted_panel_size()


func _build_header(parent: Control) -> void:
    # Single-row header: room/place label on the left, Room/Village tab
    # toggle in the middle, close button on the right. Replaces the
    # earlier two-row arrangement (header line + standalone tab bar
    # underneath) so the chips row sits directly under the header and
    # leaves more vertical space for the log + 2-line input.
    var header := HBoxContainer.new()
    header.custom_minimum_size = Vector2(0, 22)
    header.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    header.add_theme_constant_override("separation", 8)
    parent.add_child(header)

    context_label = Label.new()
    context_label.clip_text = true
    context_label.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
    # Expand-fill so a long room name has room to render and ellipsizes
    # gracefully when squeezed by the inline tabs/close-button. With
    # SIZE_SHRINK_BEGIN the label collapses to its minimum (which is
    # near zero with clip_text) and the room name disappears.
    context_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    context_label.add_theme_font_size_override("font_size", OrientationGuard.text_size(11))
    header.add_child(context_label)

    # Kept around so existing _update_context_labels code that touches
    # .text continues to compile, but never added to the layout.
    subcontext_label = Label.new()
    subcontext_label.visible = false

    # ZBBS-WORK-399: Room/Village toggle, admin-only (see _update_tab_buttons).
    # Hidden by default; visibility re-evaluates on every open(). The active
    # tab's button is DISABLED (click-block) and restyled as the highlight —
    # bright on an accent plate (see _apply_theme, ZBBS-HOME-438); the
    # clickable inactive tab renders muted.
    room_tab_button = Button.new()
    room_tab_button.text = "Room"
    room_tab_button.custom_minimum_size = Vector2(0, 20)
    room_tab_button.focus_mode = Control.FOCUS_NONE
    room_tab_button.add_theme_font_size_override("font_size", OrientationGuard.text_size(11))
    room_tab_button.visible = false
    header.add_child(room_tab_button)

    village_tab_button = Button.new()
    village_tab_button.text = "Village"
    village_tab_button.custom_minimum_size = Vector2(0, 20)
    village_tab_button.focus_mode = Control.FOCUS_NONE
    village_tab_button.add_theme_font_size_override("font_size", OrientationGuard.text_size(11))
    village_tab_button.visible = false
    header.add_child(village_tab_button)

    close_button = Button.new()
    close_button.text = "×"
    close_button.custom_minimum_size = Vector2(28, 20)
    close_button.focus_mode = Control.FOCUS_ALL
    close_button.mouse_filter = Control.MOUSE_FILTER_STOP
    close_button.add_theme_font_size_override("font_size", OrientationGuard.text_size(14))
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

    # Re-pin on sort_children: when the layout reflows — a new entry
    # appended, autowrap relayout, or the input row growing as the
    # player types into the 2-line speech input below — re-pin to the
    # bottom if we were already there. Without this, typing a wrapping
    # message visibly clips the most recent log entries from view
    # because log_scroll.scroll_vertical is an absolute pixel value
    # against the now-shorter visible area.
    log_vbox.sort_children.connect(_on_log_vbox_sorted)
    # User-driven scroll arms / disarms the stick-bottom flag, mirroring
    # the village pattern. Wheel and drag over the ScrollContainer come
    # through log_scroll.gui_input; thumb drags on the scrollbar itself
    # come through bar.gui_input — without the second hookup, dragging
    # the thumb upward would leave log_stick_bottom armed and the next
    # entry append would yank the user back to the bottom.
    log_scroll.gui_input.connect(_on_log_scroll_gui_input)
    var log_bar := log_scroll.get_v_scroll_bar()
    if log_bar != null:
        log_bar.gui_input.connect(_on_log_scroll_gui_input)


## ZBBS-WORK-399: the Village tab's scrollback — structurally a sibling of the
## room log, swapped in/out by _set_active_tab. Always follows the bottom (no
## stick-bottom tracking like the room log; a troubleshooting feed wants the
## newest line in view).
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
    village_vbox.add_theme_constant_override("separation", 2)
    village_scroll.add_child(village_vbox)


## Auto-open hook for knock-success (main.gd's pc/move response handler).
## _refresh_state runs an HTTPRequest, so huddle_members isn't populated
## yet at the moment we want to open. Defer to the next _on_me_completed
## by setting a one-shot flag — _apply_pc_state honors it after wiring
## state from the response.
var _open_after_next_refresh := false

func force_open_after_refresh() -> void:
    _open_after_next_refresh = true


# --- ZBBS-WORK-399: Village activity tab -----------------------------------


func _on_room_tab_pressed() -> void:
    _set_active_tab(false)


func _on_village_tab_pressed() -> void:
    _set_active_tab(true)


## Swap the panel between the Room view (log + chips + input) and the
## read-only Village feed. The poll runs only while Village is the active
## view — switching away stops it, switching back resumes from the since
## cursor, so the accumulated scrollback survives tab flips for free.
func _set_active_tab(village: bool) -> void:
    village_tab_active = village
    log_scroll.visible = not village
    nearby_scroll.visible = not village
    if input_row != null:
        input_row.visible = not village
    village_scroll.visible = village
    _update_tab_buttons()
    # Poll lifecycle only reacts when the panel is actually open — open()
    # selects the initial tab BEFORE setting is_open, and starts the poll
    # itself afterwards. Without the guard the pre-open tab selection fires
    # a doomed request (code_review round 1).
    if village:
        if is_open:
            _start_village_poll()
        _scroll_village_to_bottom_deferred()
    else:
        _stop_village_poll()
        if is_open:
            _focus_input_after_open()
            _scroll_log_to_bottom_deferred()


## Tab buttons are admin-only chrome: visible only when Auth.can_edit. The
## active tab's button renders disabled — that's the current-view marker. If
## the capability vanished (token re-verify demoted), force the Room view so
## a non-admin can never sit on the Village tab.
func _update_tab_buttons() -> void:
    if room_tab_button == null or village_tab_button == null:
        return
    var show: bool = Auth.can_edit
    room_tab_button.visible = show
    village_tab_button.visible = show
    if not show and village_tab_active:
        _set_active_tab(false)
        return
    room_tab_button.disabled = not village_tab_active
    village_tab_button.disabled = village_tab_active


func _start_village_poll() -> void:
    village_poll_timer.start()
    _poll_village_activity()


func _stop_village_poll() -> void:
    village_poll_timer.stop()


func _poll_village_activity() -> void:
    if not is_open or not village_tab_active:
        return
    if village_loading:
        return
    village_loading = true
    var path := "/api/village/activity/recent?limit=200"
    if village_since_seq > 0:
        path += "&since_seq=%d" % village_since_seq
    var err := http_village.request(_api_url(path), _auth_headers(), HTTPClient.METHOD_GET, "")
    if err != OK:
        # Next timer tick retries; the flag must not stay latched.
        village_loading = false


func _on_village_activity_completed(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    village_loading = false
    if not is_open or not village_tab_active:
        # Stale response after a tab flip or close — drop it. The since
        # cursor wasn't advanced, so nothing is lost on resume.
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code < 200 or response_code >= 300:
        # Transient failure — the next poll retries from the same since
        # cursor. (403 can't normally happen: the tab only shows for
        # can_edit and the route checks the same capability.)
        return
    var json: Variant = JSON.parse_string(body.get_string_from_utf8())
    if typeof(json) != TYPE_DICTIONARY:
        return
    # Cursor ahead of the server's newest seq means the engine restarted
    # (the seq counter and the log reset together). Drop the cursor; the
    # next poll backloads the fresh log's tail.
    var latest_seq := int(json.get("latest_seq", 0))
    if village_since_seq > latest_seq:
        village_since_seq = 0
        return
    var entries: Variant = json.get("entries", [])
    if typeof(entries) != TYPE_ARRAY:
        return
    var appended := 0
    for e in entries:
        if typeof(e) != TYPE_DICTIONARY:
            continue
        if _append_village_line(e):
            appended += 1
        var seq := int(e.get("seq", 0))
        if seq > village_since_seq:
            village_since_seq = seq
    if appended > 0:
        _scroll_village_to_bottom_deferred()


## One rendered feed row. The server pre-renders `line` (names embedded), so
## the client only styles by kind: speech in the room log's speech tones, act
## narration in its muted tan, and `raw` rows (renderer-less ActionTypes,
## orphan actor ids) in a cool telemetry gray that visually flags them as
## mechanical — those are the rows an admin is usually hunting.
func _append_village_line(e: Dictionary) -> bool:
    var line := str(e.get("line", ""))
    if line == "":
        return false
    var kind := str(e.get("kind", ""))
    var color := "#a09377"
    if kind == "speech_player":
        color = "#f2dbad"
    elif kind == "speech_npc":
        color = "#d1c2a3"
    elif kind == "raw":
        color = "#8d97a8"

    var rich := RichTextLabel.new()
    rich.bbcode_enabled = true
    rich.fit_content = true
    rich.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    rich.scroll_active = false
    rich.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    rich.add_theme_font_size_override("normal_font_size", OrientationGuard.text_size(13))
    var prefix := ""
    var time_prefix := _format_timestamp(str(e.get("occurred_at", "")))
    if time_prefix != "":
        prefix = "[color=#7a6f59]%s[/color] " % time_prefix
    rich.text = "%s[color=%s]%s[/color]" % [prefix, color, _bbcode_escape(line)]
    village_vbox.add_child(rich)

    # remove_child before queue_free: a queued node stays in the tree (and
    # in get_child_count()) until end-of-frame, so queue_free alone never
    # shrinks the count and this loop spins forever — the ZBBS-HOME-429
    # village lockup.
    while village_vbox.get_child_count() > MAX_LOG_LINES:
        var old := village_vbox.get_child(0)
        village_vbox.remove_child(old)
        old.queue_free()
    return true


func _scroll_village_to_bottom_deferred() -> void:
    call_deferred("_scroll_village_to_bottom")


func _scroll_village_to_bottom() -> void:
    if village_scroll == null:
        return
    # Two frames: visibility/layout settle, then RichTextLabel fit_content
    # expansion — same dance as the room log's scroll helper.
    await get_tree().process_frame
    await get_tree().process_frame
    var bar := village_scroll.get_v_scroll_bar()
    if bar != null:
        village_scroll.scroll_vertical = int(bar.max_value)


func _build_input(parent: Control) -> void:
    var row := HBoxContainer.new()
    # ZBBS-WORK-399: held so the Village tab can hide the speech input —
    # the village view is read-only.
    input_row = row
    # Two-line input: ~52px fits 2 visual lines at font_size=12 plus the
    # 4px top/bottom content margins on the input stylebox. The TextEdit
    # itself handles internal scroll past 2 lines, so a long message
    # doesn't blow up the panel.
    row.custom_minimum_size = Vector2(0, OrientationGuard.text_size(52))
    row.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.add_theme_constant_override("separation", 6)
    parent.add_child(row)

    speech_input = TextEdit.new()
    speech_input.placeholder_text = "Speak to those gathered here…"
    speech_input.wrap_mode = TextEdit.LINE_WRAPPING_BOUNDARY
    speech_input.custom_minimum_size = Vector2(0, OrientationGuard.text_size(52))
    speech_input.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    speech_input.size_flags_vertical = Control.SIZE_FILL
    speech_input.focus_mode = Control.FOCUS_ALL
    speech_input.mouse_filter = Control.MOUSE_FILTER_STOP
    speech_input.add_theme_font_size_override("font_size", OrientationGuard.text_size(12))
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
    pay_button.size_flags_vertical = Control.SIZE_FILL
    pay_button.focus_mode = Control.FOCUS_ALL
    pay_button.mouse_filter = Control.MOUSE_FILTER_STOP
    pay_button.add_theme_font_size_override("font_size", OrientationGuard.text_size(12))
    pay_button.tooltip_text = "Buy from, or pay, someone you are talking with."
    row.add_child(pay_button)

    speak_button = Button.new()
    speak_button.text = "Speak"
    speak_button.custom_minimum_size = Vector2(60, 28)
    speak_button.size_flags_vertical = Control.SIZE_FILL
    speak_button.focus_mode = Control.FOCUS_ALL
    speak_button.mouse_filter = Control.MOUSE_FILTER_STOP
    speak_button.add_theme_font_size_override("font_size", OrientationGuard.text_size(12))
    row.add_child(speak_button)


# Top-left corner resize grip. Parented under `root` (a plain Control,
# not a Container) so PanelContainer / MarginContainer layout passes
# never see it as a layout participant. `top_level = true` puts the
# grip in viewport coords; talk_sheet.resized + sheet_anchor.visibility
# keep it anchored to the sheet's top-left corner regardless of
# layout reflows. Mobile mode hides it (the sheet is already viewport-
# sized; user-resize doesn't apply).
func _build_resize_grip() -> void:
    if talk_sheet == null or root == null:
        return
    resize_grip = ColorRect.new()
    resize_grip.name = "ResizeGrip"
    resize_grip.color = Color(0.55, 0.42, 0.24, 0.55)
    resize_grip.size = Vector2(RESIZE_GRIP_SIZE, RESIZE_GRIP_SIZE)
    resize_grip.custom_minimum_size = Vector2(RESIZE_GRIP_SIZE, RESIZE_GRIP_SIZE)
    resize_grip.mouse_filter = Control.MOUSE_FILTER_STOP
    resize_grip.mouse_default_cursor_shape = Control.CURSOR_FDIAGSIZE
    resize_grip.tooltip_text = "Drag to resize the talk panel"
    resize_grip.top_level = true
    resize_grip.visible = not is_mobile
    root.add_child(resize_grip)
    resize_grip.gui_input.connect(_on_resize_grip_input)
    # item_rect_changed (not resized) so position-only changes also retrigger
    # — the panel is bottom-right anchored, so a viewport resize moves it
    # without changing its size and the grip needs to follow.
    talk_sheet.item_rect_changed.connect(_position_resize_grip)
    sheet_anchor.visibility_changed.connect(_position_resize_grip)
    # Defer the initial read — Godot lays out children after the build
    # frame, and reading talk_sheet.global_position synchronously here
    # returns (0, 0) before the first layout pass.
    call_deferred("_position_resize_grip")


# Re-anchor the grip to the top-left corner of talk_sheet. Called on
# sheet resize, panel open/close, and after a user-driven drag.
# global_position rather than position because the grip is top_level
# and lives in viewport coords; using `position` here resolves
# against the parent's transform and would land in the wrong place
# under non-trivial canvas transforms.
func _position_resize_grip() -> void:
    if resize_grip == null or talk_sheet == null:
        return
    resize_grip.global_position = talk_sheet.global_position
    resize_grip.visible = sheet_anchor.visible and not is_mobile


# Drag start hook. The 14×14 grip only stays under the cursor while
# the user holds steady; once they start moving fast the pointer can
# leave the grip's rect mid-drag and gui_input stops delivering motion
# / release. Press captures start state; motion + release are handled
# globally in _input below so the drag survives the pointer leaving
# the grip.
func _on_resize_grip_input(event: InputEvent) -> void:
    if talk_sheet == null:
        return
    if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
        _resize_dragging = true
        _resize_start_mouse = event.global_position
        # Capture custom_minimum_size (the field we mutate) rather than
        # actual rendered size — keeps the math consistent if a Container
        # constraint had inflated the rendered size beyond the minimum.
        _resize_start_size = talk_sheet.custom_minimum_size


# Global drag motion + release. Active only while _resize_dragging is
# true; otherwise this is a no-op for every input event in the scene.
# Catches the case where the cursor leaves the grip mid-drag — without
# this, _resize_dragging would stay armed and the next motion over the
# grip would resume from stale start state.
func _input(event: InputEvent) -> void:
    if not _resize_dragging:
        return
    if event is InputEventMouseMotion:
        var motion := event as InputEventMouseMotion
        var delta: Vector2 = motion.global_position - _resize_start_mouse
        # Top-left grip: drag up-left (negative delta) grows the panel.
        var new_w: float = _resize_start_size.x - delta.x
        var new_h: float = _resize_start_size.y - delta.y
        var vp := get_viewport().get_visible_rect().size
        # Min: never below the desktop floor. Max: leave 24px margin
        # from the viewport edge so the grip stays clickable.
        new_w = clamp(new_w, DESKTOP_RESIZE_MIN_WIDTH, max(DESKTOP_RESIZE_MIN_WIDTH, vp.x - 24.0))
        new_h = clamp(new_h, DESKTOP_RESIZE_MIN_HEIGHT, max(DESKTOP_RESIZE_MIN_HEIGHT, vp.y - 24.0))
        talk_sheet.custom_minimum_size = Vector2(new_w, new_h)
        _position_resize_grip()
        get_viewport().set_input_as_handled()
    elif event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and not event.pressed:
        _resize_dragging = false
        _persisted_panel_size = talk_sheet.custom_minimum_size
        _save_persisted_panel_size()
        get_viewport().set_input_as_handled()


# Load any persisted size from user://talk_panel_size.json. Only sets
# `_persisted_panel_size` — `_update_responsive_layout` is the single
# place that decides whether to apply it (desktop only, re-clamped
# against the current viewport). Missing/corrupt file is a silent
# no-op; the panel falls back to DESKTOP_MIN_WIDTH × DESKTOP_MIN_HEIGHT
# via the responsive-layout default branch.
func _restore_persisted_panel_size() -> void:
    if talk_sheet == null:
        return
    if not FileAccess.file_exists(TALK_PANEL_SIZE_PATH):
        return
    var f := FileAccess.open(TALK_PANEL_SIZE_PATH, FileAccess.READ)
    if f == null:
        return
    var data = JSON.parse_string(f.get_as_text())
    if not (data is Dictionary):
        return
    var w := float(data.get("width", 0.0))
    var h := float(data.get("height", 0.0))
    if w < DESKTOP_RESIZE_MIN_WIDTH or h < DESKTOP_RESIZE_MIN_HEIGHT:
        return
    _persisted_panel_size = Vector2(w, h)


func _save_persisted_panel_size() -> void:
    var f := FileAccess.open(TALK_PANEL_SIZE_PATH, FileAccess.WRITE)
    if f == null:
        return
    f.store_string(JSON.stringify({
        "width": _persisted_panel_size.x,
        "height": _persisted_panel_size.y,
    }))


func _connect_signals() -> void:
    talk_launcher.pressed.connect(open)
    close_button.pressed.connect(close)
    speak_button.pressed.connect(_on_speak_pressed)
    if pay_button != null:
        pay_button.pressed.connect(_on_pay_pressed)

    refresh_timer.timeout.connect(_refresh_state)
    http_me.request_completed.connect(_on_me_completed)
    http_speak.request_completed.connect(_on_speak_completed)

    if room_tab_button != null:
        room_tab_button.pressed.connect(_on_room_tab_pressed)
    if village_tab_button != null:
        village_tab_button.pressed.connect(_on_village_tab_pressed)
    if village_poll_timer != null:
        village_poll_timer.timeout.connect(_poll_village_activity)
    if http_village != null:
        http_village.request_completed.connect(_on_village_activity_completed)

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
    # Pay-with-item lifecycle narration (ZBBS-WORK-296). world resolves the
    # buyer/seller display names; we scope to the PC's own transactions and
    # render "You"-framed narration lines into the log.
    if world.has_signal("pay_offer"):
        world.pay_offer.connect(_on_pay_offer)
    if world.has_signal("pay_countered"):
        world.pay_countered.connect(_on_pay_countered)
    if world.has_signal("pay_resolved"):
        world.pay_resolved.connect(_on_pay_resolved)


# talk_sheet is the actual visible chat panel (the bottom-right rounded
# rectangle). main.gd registers it with the camera so wheel scrolling
# over the open sheet scrolls the chat log instead of zooming the map.
# Visibility is gated by sheet_anchor; closing the panel hides
# sheet_anchor which makes is_visible_in_tree() return false on
# talk_sheet, so registration "just works" — we don't have to
# re-register on every open/close.
func get_input_eating_control() -> Control:
    return talk_sheet


# Launcher chip (the "Talk" pill that appears when the panel is
# minimized). Registered with camera._is_over_ui so main.gd's
# click-to-walk handler skips clicks that land on the chip — without
# this, tapping the chip to open the panel also issues a move_to.
# Visibility flips with sheet_anchor, so _is_over_ui's
# is_visible_in_tree() check naturally gates the block to the
# minimized state.
func get_launcher_control() -> Control:
    return talk_launcher


func open() -> void:
    if not pc_exists:
        return
    if huddle_members.is_empty() and dormant_members.is_empty():
        # ZBBS-WORK-399: alone in the village there's no room conversation
        # to show, but an admin still gets the panel — the Village activity
        # tab is its troubleshooting purpose. Land directly on Village.
        # A co-present sleeper (dormant_members, ZBBS-WORK-427) counts as
        # not-alone: fall through so the panel opens on the conversation tab
        # and shows the "(asleep)" chip even with no one awake to address.
        if not Auth.can_edit:
            return
        _set_active_tab(true)

    is_open = true
    user_closed = false
    unread_count = 0

    sheet_anchor.visible = true
    talk_launcher.visible = false

    _update_tab_buttons()
    _update_launcher_text()
    if village_tab_active:
        _start_village_poll()
    else:
        _focus_input_after_open()
    _scroll_log_to_bottom_deferred()


func close() -> void:
    is_open = false
    user_closed = true
    _stop_village_poll()
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
    pc_actor_id = str(data.get("actor_id", ""))
    structure_name = str(data.get("structure_name", ""))
    home_name = str(data.get("home_name", ""))
    pc_x = float(data.get("x", 0.0))
    pc_y = float(data.get("y", 0.0))

    # Coins + inventory — surfaced in the top-bar coin chip and used
    # by the Pay box to check an amount against the purse.
    pc_coins = int(data.get("coins", 0))
    var inv_data = data.get("inventory", [])
    pc_inventory = inv_data if typeof(inv_data) == TYPE_ARRAY else []
    _push_purse_to_top_bar()

    # Body needs — surfaced in the top-bar HUD readout (ZBBS-123).
    var needs_data = data.get("needs", {})
    pc_needs = needs_data if typeof(needs_data) == TYPE_DICTIONARY else {}
    _push_needs_to_top_bar()

    # Dwelling attributes (ZBBS-HOME-218). Forwarded as-is to top_bar's
    # HUD so the recovery pulse engages from server state.
    var dwelling_raw = data.get("dwelling_attributes", [])
    var dwelling: PackedStringArray = PackedStringArray()
    if dwelling_raw is Array:
        for entry in dwelling_raw:
            dwelling.append(str(entry))
    dwelling_attributes_changed.emit(dwelling)

    # Active lodging (LLM-38) — surfaced as the top-bar "Lodged at …" chip and
    # used for the Pay box's empty-state copy. Absent when the PC holds no room.
    var lodging_data = data.get("lodging", {})
    pc_lodging = lodging_data if typeof(lodging_data) == TYPE_DICTIONARY else {}
    _push_lodging_to_top_bar()

    var prev_huddle_size: int = huddle_members.size()
    var members = data.get("huddle_members", [])
    if typeof(members) == TYPE_ARRAY:
        huddle_members = members
    else:
        huddle_members = []
    var dormant = data.get("dormant_members", [])
    if typeof(dormant) == TYPE_ARRAY:
        dormant_members = dormant
    else:
        dormant_members = []

    _maybe_apply_recent_speech(data)
    _update_context_labels()
    _update_nearby_chips()
    _update_launcher_text()
    _update_visibility_from_state()
    _maybe_auto_attention_for_first_encounter()
    # The open Pay box follows the purse and who is here.
    if pay_panel != null:
        pay_panel.refresh()

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
    var current_room := str(data.get("audience_room_id", ""))
    if current_structure == loaded_structure_id and current_room == loaded_room_id:
        return

    loaded_structure_id = current_structure
    loaded_room_id = current_room
    # Notify world.gd so its speech-bubble spawn path uses the same scope.
    audience_scope_changed.emit(current_structure, current_room)

    # Wipe whatever's there from the previous room — fresh ears for a new
    # space. Live npc_spoke events that were happening in the old place
    # aren't relevant once the PC has moved. Phase C: also reset the
    # vendor mentions accumulator since "what the vendor said they had"
    # is a per-conversation, per-huddle fact.
    for child in log_vbox.get_children():
        child.queue_free()
    vendor_mentions.clear()
    vendor_mention_prices.clear()
    vendor_latest_mentions.clear()
    # Dismissals share the accumulators' lifecycle: walk away and come
    # back = clean slate. (ZBBS-WORK-401)
    if pay_panel != null:
        pay_panel.reset_huddle()

    if current_structure.is_empty():
        return

    var recent = data.get("recent_speech", [])
    if typeof(recent) != TYPE_ARRAY:
        return

    # Find the proprietor's arrival greeting — the LAST entry in the
    # backload that's an NPC speech AND happens to be the most recent
    # entry overall. Common case: player walks in, arrival cascade
    # fires the proprietor's tick, the resulting `speak` row is the
    # freshest thing in agent_action_log when the next /pc/me poll
    # lands, so it backloads alongside the older history. The visual
    # gap should go BEFORE it so the room reads as "older history →
    # greeting → your live conversation" rather than burying the
    # greeting in the wall of older acts.
    #
    # Edge case guarded against: a stale NPC speech from earlier in
    # the day with subsequent acts AFTER it. That's history, not a
    # greeting, so we don't anchor on it. We only anchor when the
    # latest valid entry in the backload IS the speech_npc.
    var greeting_index := -1
    for idx in range(recent.size() - 1, -1, -1):
        var pre = recent[idx]
        if typeof(pre) != TYPE_DICTIONARY:
            continue
        if str(pre.get("speaker_name", "")).is_empty() or str(pre.get("text", "")).is_empty():
            continue
        if str(pre.get("kind", "")) == "speech_npc":
            greeting_index = idx
        break

    var backload_count := 0
    var inserted_gap := false
    for idx in range(recent.size()):
        var entry = recent[idx]
        if typeof(entry) != TYPE_DICTIONARY:
            continue
        var speaker := str(entry.get("speaker_name", ""))
        var text := str(entry.get("text", ""))
        var kind := str(entry.get("kind", "npc"))
        var at := str(entry.get("occurred_at", ""))
        if speaker.is_empty() or text.is_empty():
            continue
        if idx == greeting_index and backload_count > 0:
            for s in range(2):
                var spacer := Control.new()
                spacer.custom_minimum_size = Vector2(0, 13)
                log_vbox.add_child(spacer)
            inserted_gap = true
        _append_log_line(speaker, text, kind, true, at)
        backload_count += 1

    # Fallback: no arrival greeting to anchor before, so the gap goes
    # at the end of the backload. The first live event that arrives
    # after walking in still gets visually separated from whatever
    # historical acts were here.
    if backload_count > 0 and not inserted_gap:
        for s in range(2):
            var spacer := Control.new()
            spacer.custom_minimum_size = Vector2(0, 13)
            log_vbox.add_child(spacer)


func _set_no_pc_state() -> void:
    pc_exists = false
    pc_actor_id = ""
    huddle_members = []
    dormant_members = []
    pc_coins = 0
    pc_inventory = []
    pc_needs = {}
    pc_lodging = {}
    is_open = false
    sheet_anchor.visible = false
    talk_launcher.visible = false
    loaded_structure_id = ""
    loaded_room_id = ""
    audience_scope_changed.emit("", "")
    _update_context_labels()
    _clear_nearby_chips()
    _push_purse_to_top_bar()
    _push_needs_to_top_bar()
    _push_lodging_to_top_bar()


## Public: collapse the talk panel to its launcher chip without
## otherwise touching state (PC, huddle, log). Called by main.gd when
## the user enters edit mode so the editor's map clicks don't have to
## negotiate with an open chat sheet, and so the user's chat context
## (selected partner, scrollback, drafted speech) survives the edit
## excursion. No-op when the panel isn't currently open. Re-opening
## is left to the user — they tap the launcher chip when ready.
func minimize() -> void:
    if is_open:
        is_open = false
        _update_visibility_from_state()


func _update_visibility_from_state() -> void:
    # No PC → nothing to show, ever. Force everything down.
    if not pc_exists:
        is_open = false
        sheet_anchor.visible = false
        talk_launcher.visible = false
        return

    # PC exists. Preserve the panel's open state across huddle
    # transitions so a refresh narration that lands the moment the
    # player walks out of a structure (tavern → well, etc.) still
    # surfaces in the brown box. The auto-close on empty huddle was
    # snapping the panel shut between the walk-out and the arrival,
    # eating any private narration that fired in the gap.
    if is_open:
        sheet_anchor.visible = true
        talk_launcher.visible = false
    else:
        sheet_anchor.visible = false
        # Launcher chip is useful when there's someone to see — a huddle to
        # enter, OR a co-present sleeper to mark (ZBBS-WORK-427: an indoor
        # sleeper has no map sprite, so the (asleep) chip is the only sign a
        # lone sleeper is even there). Otherwise hide it rather than offer a
        # no-op tap target. Exception (ZBBS-WORK-399): admins keep the chip
        # while alone, because open() lands them on the Village activity tab.
        talk_launcher.visible = not huddle_members.is_empty() or not dormant_members.is_empty() or Auth.can_edit


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

    # Addressable members first, then the passive dormant (asleep) chips
    # (ZBBS-WORK-427). Both render identically; only dormant_members carries a
    # sleeper, and it is deliberately NOT in the talk/pay roster.
    for member in huddle_members:
        _append_member_chip(member)
    for member in dormant_members:
        _append_member_chip(member)


## Render one roster member as a nearby chip, with a rest-state suffix
## ((on break) / (asleep)) when the server tagged a status. Shared by the
## addressable huddle_members and the passive dormant_members lists.
func _append_member_chip(member) -> void:
    if typeof(member) != TYPE_DICTIONARY:
        return

    var member_name := str(member.get("name", "Unknown"))
    var role := str(member.get("role", ""))
    var chip_text := member_name
    if not role.is_empty():
        chip_text = "%s · %s" % [member_name, role]
    # Rest-state suffix so the player can see a keeper is winding down
    # (on break — still answers) or has turned in (asleep) instead of
    # typing into silence. Server sends status on pc/me members.
    var status := str(member.get("status", ""))
    if status == "on_break":
        chip_text = "%s (on break)" % chip_text
    elif status == "asleep":
        chip_text = "%s (asleep)" % chip_text
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
    label.add_theme_font_size_override("font_size", OrientationGuard.text_size(11))
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
    # Local echo (ZBBS-WORK-360): render the player's own line immediately as
    # "You" instead of waiting for /pc/speak to round-trip and fan back via the
    # npc_spoke WS event — that delay read as "I typed and it vanished."
    # _on_npc_spoke drops the echoed self-line (speaker_id == pc_actor_id) so it
    # isn't double-rendered. Client-stamped UTC time → same [h:mma] prefix as
    # server-sourced lines.
    _append_log_line("You", text, "speech_player", false, Time.get_datetime_string_from_system(true))
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


## Emit purse_changed (coins + formatted lines for the chip) and
## inventory_changed (raw structured items for the pack panel) so main.gd
## can update the top bar. Also emit character_name_changed so the
## name label tracks the in-world identity. Negative coins signals
## "no PC" — top bar hides the chip on that. Empty character_name
## signals the same — top bar reverts to login.
func _push_purse_to_top_bar() -> void:
    var lines: PackedStringArray = PackedStringArray()
    if not pc_exists:
        purse_changed.emit(-1, lines)
        inventory_changed.emit([])
        character_name_changed.emit("")
        return
    var structured: Array = []
    for entry in pc_inventory:
        if typeof(entry) != TYPE_DICTIONARY:
            continue
        var label := str(entry.get("display_label", entry.get("item_kind", "")))
        var qty := int(entry.get("quantity", 0))
        if label != "" and qty > 0:
            lines.append("%s × %d" % [label, qty])
            structured.append(entry)
    purse_changed.emit(pc_coins, lines)
    inventory_changed.emit(structured)
    character_name_changed.emit(character_name)


## Emit lodging_changed so main.gd can update the top bar's "Lodged at …" chip
## (LLM-38). Empty strings (no PC, or an empty pc_lodging) hide the chip.
func _push_lodging_to_top_bar() -> void:
    lodging_changed.emit(str(pc_lodging.get("inn_name", "")), str(pc_lodging.get("until_label", "")))


## Emit needs_changed so main.gd can update the top bar's HUD readout.
## Empty dictionary signals "no PC" — top bar hides the readout.
func _push_needs_to_top_bar() -> void:
    if not pc_exists:
        needs_changed.emit({})
        return
    needs_changed.emit(pc_needs)


func _on_pay_pressed() -> void:
    if pay_panel != null:
        pay_panel.open()


## A slow-path offer is before the seller; their answer lands later as a
## pay_resolved or pay_countered line.
func _on_pay_offer_pending(seller: String) -> void:
    if seller.is_empty():
        seller = "the seller"
    _append_log_line("", "Your offer is before %s — awaiting their answer." % seller, "act", false, Time.get_datetime_string_from_system(true))


## A gift (pc/give) is final at once — no answer to wait on (LLM-725).
func _on_pay_gave(recipient: String, amount: int) -> void:
    _append_log_line("", "You gave %s %s." % [recipient, "1 coin" if amount == 1 else "%d coins" % amount], "act", false, Time.get_datetime_string_from_system(true))


func _on_speak_completed(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    speak_button.disabled = false

    if result != HTTPRequest.RESULT_SUCCESS or response_code < 200 or response_code >= 300:
        var raw := body.get_string_from_utf8().strip_edges()
        push_warning("TalkPanel speak failed: code=%s body=%s" % [response_code, raw])
        # ZBBS-WORK-360: the engine exempts PCs from sim.Speak's gates, so a
        # rejected PC speak is a malfunction, not a normal condition. Show a
        # generic, benign line — never the raw engine reason (internal jargon /
        # a false claim) — and beacon the real code+reason for diagnosis.
        _append_system_warning("Your words go unheard.")
        ErrorBeacon.report("pc_speak_rejected", "code=%d body=%s" % [response_code, raw])


func _on_npc_spoke(speaker_id: String, speaker_name: String, text: String, kind: String = "", at: String = "", structure_id: String = "", mentions: Array = [], speaker_x: float = 0.0, speaker_y: float = 0.0, room_id: String = "", addressee_id: String = "", addressee_name: String = "", mention_prices: Dictionary = {}, huddle_scoped: bool = false, recipient_ids: Array = []) -> void:
    # WS speech kinds are "npc" | "player"; normalize to the panel's
    # speech_npc / speech_player kinds so render logic is uniform with
    # the backload entries. speaker_id scopes the v2 huddle-audience check
    # below (world.gd's bubble path consumes it too). `at` is an ISO timestamp from the broadcast;
    # _format_timestamp converts to a short clock-time prefix.
    #
    # Scope filter. v2 npc_spoke frames are HUDDLE-scoped: the engine already
    # chose the audience (recipient_ids) and the frame carries no structure/room
    # geometry, so scope the panel log by huddle membership — accept our own
    # line (we are the speaker, so it echoes) or any utterance we were an
    # audience member of. Without this the v1 filter below dropped every indoor
    # v2 frame (empty structure_id != the PC's loaded_structure_id), so live
    # speech never appeared in the panel even though the backload did
    # (ZBBS-HOME-372).
    # ZBBS-WORK-360: our own speak is echoed locally on send (shown immediately
    # as "You"), so drop the WS echo of it to avoid a duplicate line. Skip the
    # guard until pc_actor_id is known (first /pc/me not yet back) — the WS echo
    # is the only render then, so there is nothing to double.
    if pc_actor_id != "" and speaker_id == pc_actor_id:
        return

    if huddle_scoped:
        if speaker_id != pc_actor_id and not recipient_ids.has(pc_actor_id):
            return
    else:
        # Legacy v1 frame carries explicit structure/room scope. Structure
        # filter mirrors _on_room_event; empty structure_id is outdoor speech,
        # accepted only when we are also outside AND the speaker is within
        # OUTDOOR_SPEECH_RANGE (Chebyshev). The subspace room_id must also match
        # the PC's loaded_room_id (post-ZBBS-149) so common-room speech does not
        # leak into private bedrooms.
        if structure_id != loaded_structure_id:
            return
        if room_id != loaded_room_id:
            return
        if structure_id.is_empty():
            var dx: float = abs(speaker_x - pc_x)
            var dy: float = abs(speaker_y - pc_y)
            if max(dx, dy) > OUTDOOR_SPEECH_RANGE:
                return
    var panel_kind := "speech_player" if kind == "player" else "speech_npc"
    var rendered_speaker := speaker_name
    # Addressee disambiguation for deliberation speech (counter / decline).
    # When the engine knows who the speaker is responding to, it stamps
    # addressee_id/name onto the broadcast. The panel renders "John (to
    # Ezekiel)" so an NPC-NPC haggle overheard from across the room
    # doesn't read as if the merchant is talking to the player. Suppress
    # the parenthetical when the listener IS the addressee (their own
    # offer being countered) — "John (to me): ..." reads awkwardly.
    # Match on actor_id, not display name — names can collide between
    # actors and casing/normalization may differ between addressee_name
    # and character_name. Fall back to display-name comparison only when
    # the panel doesn't have its own actor_id yet (early bootstrap, /pc/me
    # not yet returned).
    var is_self_addressed: bool
    if not pc_actor_id.is_empty():
        is_self_addressed = (addressee_id == pc_actor_id)
    else:
        is_self_addressed = (addressee_name == character_name)
    if not addressee_name.is_empty() and not is_self_addressed:
        rendered_speaker = "%s (to %s)" % [speaker_name, addressee_name]
    _append_log_line(rendered_speaker, text, panel_kind, false, at)
    # Phase C of sales-and-gifts: accumulate this speaker's mentions for
    # the customer's pay-modal item dropdown. Per-speaker, deduped,
    # lowercase. Resets when the player leaves the huddle (see
    # _maybe_apply_recent_speech). Refresh the live dropdown if it's
    # currently showing this same speaker.
    if not speaker_name.is_empty() and not mentions.is_empty():
        var existing = vendor_mentions.get(speaker_name, [])
        var seen := {}
        for s in existing:
            seen[str(s)] = true
        var updated: Array = []
        for s in existing:
            updated.append(str(s))
        # Build a normalized list of THIS speak's mentions (lowercase,
        # deduped within the speak). Drives both the accumulated
        # vendor_mentions union and the per-speak vendor_latest_mentions
        # snapshot the pay-modal pre-fill reads.
        var this_speak: Array = []
        var this_seen := {}
        for m in mentions:
            var k := str(m).strip_edges().to_lower()
            if k.is_empty() or this_seen.has(k):
                continue
            this_seen[k] = true
            this_speak.append(k)
            if not seen.has(k):
                seen[k] = true
                updated.append(k)
        vendor_mentions[speaker_name] = updated
        # Pre-fill (vendor_latest_mentions) only follows lines aimed at
        # US — an addressee-stamped line to someone else (the vendor
        # countering or quoting another buyer) must not overwrite what
        # this player is about to pay for. The union + prices still
        # accumulate from every line so the dropdown stays complete.
        # (ZBBS-HOME-423: John's "1 ale for 6 coins" to Hannah was
        # clobbering the PC's 4-coin room context.)
        if addressee_id.is_empty() or pc_actor_id.is_empty() or addressee_id == pc_actor_id:
            vendor_latest_mentions[speaker_name] = this_speak
        # Merge mention_prices into per-speaker price cache. Newer
        # quotes override older — vendor's most recent price wins.
        if not mention_prices.is_empty():
            var existing_prices = vendor_mention_prices.get(speaker_name, {})
            if typeof(existing_prices) != TYPE_DICTIONARY:
                existing_prices = {}
            for k in mention_prices.keys():
                var price = int(mention_prices[k])
                if price > 0:
                    existing_prices[str(k).strip_edges().to_lower()] = price
            vendor_mention_prices[speaker_name] = existing_prices
        # The open Pay box shows a new spoken good straight away.
        if pay_panel != null:
            pay_panel.refresh()


# Generic room-event handler. Engine emits these for narration-worthy
# things that aren't speech — acts, departures, and peer arrivals
# (ZBBS-WORK-422: "X arrives at Y" surfaced to co-present PCs).
# Each event scopes itself to a structure_id; we ignore events outside
# the room the player is currently in.
func _on_room_event(data: Dictionary) -> void:
    var event_structure := str(data.get("structure_id", ""))
    var actor_name := str(data.get("actor_name", ""))
    var text := str(data.get("text", ""))
    var kind := str(data.get("kind", "act"))
    var at := str(data.get("at", ""))
    var private_event := bool(data.get("private", false))
    # Private events are felt-language narrations meant only for the
    # acting player — refresh effects ("the parching ebbs"), future
    # consume reactions, etc. Render only when this client's PC is the
    # actor; drop for any other audience even if the room scope would
    # otherwise match. Prefer matching on actor_id (since /pc/me added
    # it 2026-05-08 alongside Phase 1.5) — name matching breaks when
    # the engine sends actor_name="" with an actor_id (the convention
    # used by sleep.go and order_fulfillment.go's consume narration).
    # Fall back to name match for events that don't carry actor_id, and
    # during early bootstrap before /pc/me has populated pc_actor_id.
    if private_event:
        var event_actor_id := str(data.get("actor_id", ""))
        var matches := false
        if not pc_actor_id.is_empty() and not event_actor_id.is_empty():
            matches = (event_actor_id == pc_actor_id)
        elif not actor_name.is_empty() and not character_name.is_empty():
            matches = (actor_name == character_name)
        if not matches:
            return
        # Private events bypass the room scope filter — your felt
        # experience follows you regardless of which structure you're
        # currently parked at, since by the time the broadcast lands
        # the structure_id reflects where you WERE, which may not
        # match the panel's loaded scope after a quick walk.
    elif event_structure != loaded_structure_id:
        return
    else:
        # Subspace filter (Phase 1.5): event room_id must match the PC's
        # loaded_room_id. Both empty = public scope (common room or
        # outdoor); either set = private/staff scope, only same-room
        # listeners receive. Skipped for private events (already passed
        # through the actor-name guard above and bypass the room scope).
        var event_room := str(data.get("room_id", ""))
        if event_room != loaded_room_id:
            return
    # Empty-actor drop applies only to public events. Private events
    # have already passed the actor_id-or-name match above and are
    # second-person narrations like "You settle into your bed and
    # drift off." (sleep), "You feel rested." (consume floor-hit), and
    # "You arrive at General Store. It is closed." (closed-business
    # arrival, ZBBS-179) — the engine convention for those is
    # actor_name="" with actor_id=<pc>. Dropping them here was a
    # latent bug that swallowed all such narrations after the
    # ZBBS-128 empty-check landed; lifted in ZBBS-181.
    if text.is_empty() or (not private_event and actor_name.is_empty()):
        return
    _append_log_line(actor_name, text, kind, false, at)
    # Private events bump the unread counter on the launcher pill when
    # the panel is closed, mirroring append_local_narration. Without
    # this, second-person narrations land in the log but the player
    # never sees them — the panel only auto-opens via a huddle, which
    # most narration paths (sleep, consume, closed-business arrival)
    # don't have.
    if private_event and not is_open:
        unread_count += 1
        _update_launcher_text()
    # Closed-business arrival (ZBBS-179) also pops a transient
    # SpeechBubble at the structure so the player sees the message
    # at the location, not just in the brown panel. Other private
    # narration kinds (sleep, consume) stay panel-only — they're
    # about the PC's body, not a place. Filter via kind + structure_id.
    # A broken well (LLM-654, object_condition) floats its thought over the
    # well the same way — structure_id carries the well's object id, and
    # spawn_structure_bubble looks up placed objects by that id.
    if private_event and (kind == "closed_business_arrival" or kind == "object_condition"):
        var event_structure_id := str(data.get("structure_id", ""))
        if event_structure_id != "":
            var world_node := get_node_or_null("/root/Main/World")
            if world_node != null and world_node.has_method("spawn_structure_bubble"):
                world_node.spawn_structure_bubble(event_structure_id, text)
    # The town pays to mend it (LLM-690): hand the offer to the repair panel.
    if private_event and kind == "object_condition" and data.get("repair") is Dictionary:
        repair_offered.emit(data["repair"])


## Public entry for client-only narrations (e.g. ZBBS-101 knock outcomes).
## Renders as a dimmer narration line in the main log without round-
## tripping through the server's room_event broadcast — there's no
## audience for a knock besides the player who issued it. The panel's
## open() requires a huddle, which the player typically does NOT have
## at the moment of a knock; bumps the unread counter and surfaces a
## brief banner so the player notices the response even when the sheet
## is closed.
func append_local_narration(text: String, at: String = "") -> void:
    if text.is_empty():
        return
    _append_log_line("", text, "act", false, at)
    if not is_open:
        unread_count += 1
        _update_launcher_text()


## --- Pay-with-item lifecycle narration (ZBBS-WORK-296) ---
##
## The engine broadcasts pay_offer / pay_countered / pay_resolved to every
## connected client; world resolves the buyer/seller display names and
## re-emits. We render only the PC's OWN transactions (PC is buyer or
## seller) — the precise filter for the documented use case (the player
## drives an offer via pc/pay and watches it resolve) and the source of
## the "You" framing. Overheard NPC-NPC haggling already arrives via
## npc_spoke counter/decline broadcasts, so this isn't the channel for it.

## True when the PC is a party to the transaction. pc_actor_id is empty
## until the first /pc/me returns — render nothing rather than mis-attribute.
func _pc_is_party(buyer_id: String, seller_id: String) -> bool:
    if pc_actor_id.is_empty():
        return false
    return pc_actor_id == buyer_id or pc_actor_id == seller_id


## "stew" / "3 stew". The wire carries the raw item kind, not a display
## label; good enough for a narration line.
func _item_phrase(item: String, qty: int) -> String:
    if item.is_empty():
        return "an item"
    if qty > 1:
        return "%d %s" % [qty, item]
    return item


func _coins_phrase(n: int) -> String:
    if n == 1:
        return "1 coin"
    return "%d coins" % n


func _on_pay_offer(data: Dictionary) -> void:
    var buyer_id := str(data.get("buyer_id", ""))
    var seller_id := str(data.get("seller_id", ""))
    if pay_panel != null:
        pay_panel.note_pay_offer(data)
    if not _pc_is_party(buyer_id, seller_id):
        return
    var item_phrase := _item_phrase(str(data.get("item", "")), int(data.get("qty", 1)))
    var coins := _coins_phrase(int(data.get("amount", 0)))
    var at := str(data.get("at", ""))
    var text: String
    if buyer_id == pc_actor_id:
        text = "You offered %s %s for %s." % [str(data.get("seller_name", "")), coins, item_phrase]
    else:
        text = "%s offered you %s for %s." % [str(data.get("buyer_name", "")), coins, item_phrase]
    append_local_narration(text, at)


func _on_pay_countered(data: Dictionary) -> void:
    var buyer_id := str(data.get("buyer_id", ""))
    var seller_id := str(data.get("seller_id", ""))
    if pay_panel != null:
        pay_panel.note_countered(data)
    if not _pc_is_party(buyer_id, seller_id):
        return
    var counter := _coins_phrase(int(data.get("counter_amount", 0)))
    var original := _coins_phrase(int(data.get("original_amount", 0)))
    var msg := str(data.get("message", ""))
    var at := str(data.get("at", ""))
    var text: String
    if buyer_id == pc_actor_id:
        text = "%s countered: %s (you offered %s)." % [str(data.get("seller_name", "")), counter, original]
    else:
        text = "You countered %s: %s." % [str(data.get("buyer_name", "")), counter]
    if not msg.is_empty():
        text += " \"%s\"" % msg
    # The counter waits in the Pay box as a card with one Pay button.
    if buyer_id == pc_actor_id:
        text += " Press Pay to answer."
    append_local_narration(text, at)


func _on_pay_resolved(data: Dictionary) -> void:
    var buyer_id := str(data.get("buyer_id", ""))
    var seller_id := str(data.get("seller_id", ""))
    if pay_panel != null:
        pay_panel.note_resolved(data)
    if not _pc_is_party(buyer_id, seller_id):
        return
    var pc_is_buyer := buyer_id == pc_actor_id
    var seller_name := str(data.get("seller_name", ""))
    var buyer_name := str(data.get("buyer_name", ""))
    var item_phrase := _item_phrase(str(data.get("item", "")), int(data.get("qty", 1)))
    var coins := _coins_phrase(int(data.get("amount", 0)))
    var state := str(data.get("terminal_state", ""))
    var msg := str(data.get("message", ""))
    var at := str(data.get("at", ""))
    # ZBBS-WORK-420: true only when this resolved via the instant quote-take
    # fast-path (the seller posted a scene_quote and the PC took it). Lets the
    # "accepted" copy say "you took their offer" instead of the backwards "they
    # accepted your offer". Absent/false for a buyer-initiated offer the seller
    # later accepts.
    var buyer_took_quote := bool(data.get("buyer_took_quote", false))
    var text := ""
    match state:
        "accepted":
            if pc_is_buyer:
                if buyer_took_quote:
                    text = "You took %s's offer — %s for %s." % [seller_name, coins, item_phrase]
                else:
                    text = "%s accepted your offer — %s for %s." % [seller_name, coins, item_phrase]
            else:
                # Seller side: the PC accepted a buyer's pending offer. A
                # PC-posted quote an NPC took would also land here with
                # buyer_took_quote true, but PCs don't post scene_quotes today,
                # so that case can't occur — revisit this copy if they ever do.
                text = "You accepted %s's offer — %s for %s." % [buyer_name, coins, item_phrase]
        "declined":
            if pc_is_buyer:
                text = "%s declined your offer." % seller_name
            else:
                text = "You declined %s's offer." % buyer_name
        "withdrawn_by_buyer":
            if pc_is_buyer:
                text = "You withdrew your offer to %s." % seller_name
            else:
                text = "%s withdrew their offer." % buyer_name
        "expired":
            if pc_is_buyer:
                text = "Your offer to %s expired." % seller_name
            else:
                text = "%s's offer expired." % buyer_name
        "failed_insufficient_funds":
            text = "The offer failed — not enough coins."
        "failed_insufficient_stock":
            text = "The offer failed — %s is out of stock." % seller_name
        "failed_unavailable":
            text = "The offer failed — %s is unavailable." % item_phrase
        _:
            text = "The offer ended (%s)." % state
    # Seller's decline note / buyer's withdraw note rides the resolved frame.
    if not msg.is_empty() and (state == "declined" or state == "withdrawn_by_buyer"):
        text += " \"%s\"" % msg
    append_local_narration(text, at)


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
    if kind == "system_warning":
        # ZBBS-WORK-360: rejected-action feedback — its own warning-amber
        # dialect, distinct from speech (quote styling) and world narration
        # (muted tan). No glyph; the tint carries it. Timestamp dim like the
        # others.
        var rich := RichTextLabel.new()
        rich.bbcode_enabled = true
        rich.fit_content = true
        rich.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
        rich.scroll_active = false
        rich.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        rich.add_theme_font_size_override("normal_font_size", OrientationGuard.text_size(13))
        var prefix := ""
        if time_prefix != "":
            prefix = "[color=#7a6f59]%s[/color] " % time_prefix
        rich.text = "%s[color=#d6ad7a]%s[/color]" % [prefix, _bbcode_escape(text)]
        entry = rich
    elif is_narration:
        # Narration uses RichTextLabel so the time prefix can carry its
        # own color span (slightly dimmer than the narration text itself).
        var rich := RichTextLabel.new()
        rich.bbcode_enabled = true
        rich.fit_content = true
        rich.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
        rich.scroll_active = false
        rich.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        rich.add_theme_font_size_override("normal_font_size", OrientationGuard.text_size(13))
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
        rich.add_theme_font_size_override("normal_font_size", OrientationGuard.text_size(13))

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

    # remove_child before queue_free — same end-of-frame trap as the village
    # feed's prune loop (ZBBS-HOME-429); without it this spins once the room
    # log crosses MAX_LOG_LINES in a session.
    while log_vbox.get_child_count() > MAX_LOG_LINES:
        var old := log_vbox.get_child(0)
        log_vbox.remove_child(old)
        old.queue_free()

    if is_open:
        if was_at_bottom:
            _scroll_log_to_bottom_deferred()
    elif not is_backload:
        # Backload entries are historical, not new — don't pulse the
        # launcher's "N new" badge as if they just happened.
        unread_count += 1
        _update_launcher_text()


## ZBBS-WORK-360: append a warning-amber system line — feedback about the
## player's own rejected action. A rejected PC speak is a malfunction (the
## engine exempts PCs from speak rejection), so this is rare; the copy stays
## generic and benign, never the raw engine reason. Client-stamped time so the
## line carries the same [h:mma] prefix as the rest of the log.
func _append_system_warning(text: String) -> void:
    _append_log_line("", text, "system_warning", false, Time.get_datetime_string_from_system(true))


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
    # Re-arm the follow-bottom invariant — any caller asking to
    # scroll to bottom is implicitly asking to follow new content too.
    log_stick_bottom = true
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


# Inner vbox finished sorting — entries have laid out, max_value is
# current. Re-pin to the bottom when log_stick_bottom is armed, no-op
# otherwise. Cheap; runs on every layout reflow including the input
# row growing as the player types into the 2-line speech_input.
func _on_log_vbox_sorted() -> void:
    if not log_stick_bottom or log_scroll == null:
        return
    var bar := log_scroll.get_v_scroll_bar()
    if bar != null:
        log_scroll.scroll_vertical = int(bar.max_value)


# User-driven scroll input on the room log. Connected to both
# log_scroll.gui_input (wheel + drag over the ScrollContainer) AND
# log_scroll.get_v_scroll_bar().gui_input (thumb drags on the
# scrollbar itself, which would otherwise bypass the parent's
# gui_input). Decides whether to follow the bottom based on where
# the user has parked the scrollbar.
func _on_log_scroll_gui_input(event: InputEvent) -> void:
    if not (event is InputEventMouseButton or event is InputEventPanGesture or event is InputEventScreenDrag):
        return
    if not is_instance_valid(log_scroll):
        return
    var bar := log_scroll.get_v_scroll_bar()
    if not is_instance_valid(bar):
        return
    # Defer the read so the scroll has actually moved before we sample.
    await get_tree().process_frame
    if not is_instance_valid(log_scroll):
        return
    bar = log_scroll.get_v_scroll_bar()
    if not is_instance_valid(bar):
        return
    var distance_from_bottom: int = int(bar.max_value) - log_scroll.scroll_vertical
    log_stick_bottom = distance_from_bottom <= LOG_STICK_BOTTOM_THRESHOLD


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
        # Use the persisted user-chosen size when set, otherwise the
        # baseline desktop floor. _persisted_panel_size is loaded from
        # user://talk_panel_size.json on _build_sheet and refreshed on
        # each drag-end. Re-clamp against the current viewport in case
        # the window was resized between sessions.
        var sheet_size: Vector2
        if _persisted_panel_size.x >= DESKTOP_RESIZE_MIN_WIDTH and _persisted_panel_size.y >= DESKTOP_RESIZE_MIN_HEIGHT:
            sheet_size = _persisted_panel_size
        else:
            sheet_size = Vector2(DESKTOP_MIN_WIDTH, DESKTOP_MIN_HEIGHT)
        sheet_size.x = clamp(sheet_size.x, DESKTOP_RESIZE_MIN_WIDTH, max(DESKTOP_RESIZE_MIN_WIDTH, size.x - 24.0))
        sheet_size.y = clamp(sheet_size.y, DESKTOP_RESIZE_MIN_HEIGHT, max(DESKTOP_RESIZE_MIN_HEIGHT, size.y - 24.0))
        talk_sheet.custom_minimum_size = sheet_size

        talk_launcher.custom_minimum_size = Vector2(150, 48)

    if resize_grip != null:
        resize_grip.visible = sheet_anchor.visible and not is_mobile
        # Defer so the position read happens after the layout pass that
        # this responsive update has just queued — viewport resize, mobile/
        # desktop flip, and persisted-size apply all change talk_sheet's
        # rect, but the new global_position isn't visible until next frame.
        call_deferred("_position_resize_grip")


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

    # ZBBS-HOME-438: the Room/Village toggle marks the ACTIVE tab by disabling
    # its button (click-block), but Godot's default disabled look is DIM — so
    # the tab you were on read as off and the clickable one read as lit,
    # exactly backwards. Restyle the disabled state as the highlight (bright
    # gold on an accent plate) and mute the enabled/normal state, so the
    # "you are here" marker actually looks like it.
    var active_tab_style := StyleBoxFlat.new()
    active_tab_style.bg_color = Color(0.34, 0.25, 0.13, 0.95)
    active_tab_style.border_color = Color(0.55, 0.42, 0.24, 0.95)
    active_tab_style.border_width_bottom = 2
    active_tab_style.corner_radius_top_left = 4
    active_tab_style.corner_radius_top_right = 4
    active_tab_style.corner_radius_bottom_left = 4
    active_tab_style.corner_radius_bottom_right = 4
    for tab_button in [room_tab_button, village_tab_button]:
        if tab_button == null:
            continue
        tab_button.add_theme_stylebox_override("disabled", active_tab_style)
        tab_button.add_theme_color_override("font_disabled_color", Color(0.95, 0.82, 0.58, 1.0))
        tab_button.add_theme_color_override("font_color", Color(0.58, 0.50, 0.40, 1.0))
        tab_button.add_theme_color_override("font_hover_color", Color(0.85, 0.74, 0.58, 1.0))


func _api_url(path: String) -> String:
    var auth := get_node_or_null("/root/Auth")
    if auth == null:
        return path

    return str(auth.api_base).rstrip("/") + path


func _auth_headers() -> PackedStringArray:
    return Auth.auth_headers()
