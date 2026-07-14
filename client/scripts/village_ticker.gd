extends PanelContainer
## Village ticker — thin band below the top bar that scrolls the village's
## current atmosphere prose right-to-left in marquee fashion.
##
## v2 sourcing: the atmosphere is a single world-level string the engine's
## atmosphere cascade (engine/sim/cascade/atmosphere.go) refreshes every few
## hours. It rides the WorldStateDTO `atmosphere` field of
## GET /api/village/world (engine/sim/httpapi/dto.go). v1's per-fire feed
## (POST /api/village/environment/recent) and its world_environment_added WS
## broadcast were not ported, so there is nothing to subscribe to — the ticker
## fetches the world DTO on start and re-polls on a slow interval so a
## long-lived session still picks up the periodic refresh.
##
## Always-latest model: at most one active line. A fetched atmosphere that
## differs from the active line replaces it (politely — the current scroll, if
## any, finishes first so the player isn't yanked mid-read); an identical line
## is a no-op and the existing re-scroll schedule continues. After each scroll
## the line re-scrolls on a schedule: a 5-scroll burst at 3-minute intervals,
## then a 15-minute heartbeat. A new line resets the burst counter.

const SCROLL_SPEED: float = 40.0
const TICKER_HEIGHT: float = 24.0
const SIDE_PADDING: float = 12.0
const BURST_COUNT: int = 5
const BURST_INTERVAL_SEC: float = 180.0
const HEARTBEAT_INTERVAL_SEC: float = 900.0
## How often to re-fetch GET /api/village/world to pick up a server-side
## atmosphere refresh. The cascade refreshes on the order of hours, so a slow
## poll is plenty; identical prose is deduped by push().
const WORLD_POLL_INTERVAL_SEC: float = 600.0

## Operator alarm banner (LLM-394). A critical engine-health failure (today:
## durable checkpointing broken) is stamped onto every umbilical response so an
## operator hitting the API trips over it — but Jeff watches the village in the
## client, not curl, so the same alarm rides the ticker. Operator-only: gated on
## Auth.can_edit, which mirrors the plugins/administer capability the umbilical's
## requireOperator gate uses, so an ordinary player never polls this and never
## sees it. A non-200 (umbilical disabled → 404, non-operator → 403) reads as
## "no alarm" — this must fail SILENT, never wedge a player's ticker.
const ALARM_POLL_INTERVAL_SEC: float = 60.0
## An alarm re-scrolls on this fixed interval for as long as it is firing — it
## never decays into the atmosphere line's slow heartbeat. It is an emergency;
## it keeps shouting until someone fixes it.
const ALARM_REPEAT_SEC: float = 30.0
const COLOR_ATMOSPHERE: Color = Color(0.85, 0.78, 0.55, 1.0)
const COLOR_ALARM: Color = Color(1.0, 0.35, 0.30, 1.0)

var _label: Label = null
var _clip: Control = null
var _active_line: String = ""
var _pending_line: String = ""
var _scrolling: bool = false
var _burst_done: int = 0
var _label_width: float = 0.0
var _label_x: float = 0.0
var _http: HTTPRequest = null
var _repeat_timer: Timer = null
var _poll_timer: Timer = null
## The two sources that compete for the band. _alarm_line wins whenever it is
## non-empty; _atmosphere_line is remembered underneath and restored when the
## alarm clears, so an operator doesn't lose the village's prose to an incident.
var _atmosphere_line: String = ""
var _alarm_line: String = ""
var _alarm_http: HTTPRequest = null
var _alarm_timer: Timer = null


func _ready() -> void:
    custom_minimum_size = Vector2(0, TICKER_HEIGHT)
    anchor_left = 0.0
    anchor_right = 1.0
    anchor_top = 0.0
    anchor_bottom = 0.0
    # Sit just below the 40px top bar (TopBar offset_bottom = 40).
    offset_top = 40.0
    offset_bottom = 40.0 + TICKER_HEIGHT
    mouse_filter = Control.MOUSE_FILTER_STOP

    var panel_style := StyleBoxFlat.new()
    panel_style.bg_color = Color(0.10, 0.07, 0.05, 0.92)
    panel_style.border_width_bottom = 1
    panel_style.border_color = Color(0.40, 0.30, 0.18, 0.6)
    add_theme_stylebox_override("panel", panel_style)

    # _clip is a Control with clip_contents=true that holds _label so
    # the label's animated position can extend beyond the visible band
    # without being painted past the panel edges. PanelContainer itself
    # does not clip children — _clip.clip_contents is what does the work.
    _clip = Control.new()
    _clip.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _clip.size_flags_vertical = Control.SIZE_EXPAND_FILL
    _clip.mouse_filter = Control.MOUSE_FILTER_IGNORE
    _clip.clip_contents = true
    add_child(_clip)

    _label = Label.new()
    _label.text = ""
    _label.add_theme_font_size_override("font_size", 12)
    _label.add_theme_color_override("font_color", COLOR_ATMOSPHERE)
    _label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    _label.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
    _label.size_flags_vertical = Control.SIZE_FILL
    _clip.add_child(_label)

    _http = HTTPRequest.new()
    add_child(_http)
    _http.request_completed.connect(_on_world_state_completed)

    # Separate HTTPRequest for the alarm poll: one HTTPRequest serves one request
    # at a time, so sharing _http would make the two polls collide with ERR_BUSY.
    _alarm_http = HTTPRequest.new()
    add_child(_alarm_http)
    _alarm_http.request_completed.connect(_on_alarms_completed)

    _repeat_timer = Timer.new()
    _repeat_timer.one_shot = true
    _repeat_timer.timeout.connect(_on_repeat_timeout)
    add_child(_repeat_timer)

    # Slow poll for atmosphere refreshes (no WS frame in v2 — see header).
    _poll_timer = Timer.new()
    _poll_timer.one_shot = false
    _poll_timer.wait_time = WORLD_POLL_INTERVAL_SEC
    _poll_timer.timeout.connect(_fetch_world_state)
    add_child(_poll_timer)

    _alarm_timer = Timer.new()
    _alarm_timer.one_shot = false
    _alarm_timer.wait_time = ALARM_POLL_INTERVAL_SEC
    _alarm_timer.timeout.connect(_fetch_alarms)
    add_child(_alarm_timer)


# Wired by main.gd once the client is authenticated. Kicks off the first
# world-state fetch so the ticker has atmosphere to display, then starts the
# slow refresh poll. Operators additionally start the alarm poll.
func begin() -> void:
    _fetch_world_state()
    # Guard against begin() being called before _ready() built the timer (the
    # current call site adds the node first, but don't crash if that changes).
    if _poll_timer != null:
        _poll_timer.start()
    # Operator-only. can_edit is resolved by the pc/me token-verify before
    # main.gd calls begin(), so it is trustworthy here.
    if Auth.can_edit:
        _fetch_alarms()
        if _alarm_timer != null:
            _alarm_timer.start()


func _fetch_world_state() -> void:
    if _http == null:
        return
    if not Auth.is_authenticated():
        return
    var headers: PackedStringArray = Auth.auth_headers(false)
    var err := _http.request(Auth.api_base + "/api/village/world", headers)
    if err != OK:
        # ERR_BUSY just means the previous slow poll is still in flight;
        # atmosphere changes rarely, so skipping this cycle is harmless.
        return


func _on_world_state_completed(_result: int, code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    if code != 200:
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if typeof(json) != TYPE_DICTIONARY:
        return
    # WorldStateDTO.atmosphere — a single world-level string, or "" before the
    # cascade's first sweep populates it. Type-check defends the contract path
    # (a JSON null would make str() scroll "<null>"); push() dedupes an
    # unchanged line, so re-polling the same prose is a no-op.
    var raw = json.get("atmosphere", "")
    if typeof(raw) != TYPE_STRING:
        return
    var text: String = raw.strip_edges()
    if text == "":
        return
    push(text)


# Add a raw atmosphere line to the marquee. Same line as the active one is a
# no-op (the schedule continues uninterrupted).
func push(text: String) -> void:
    if text == "":
        return
    _atmosphere_line = text
    # A firing alarm owns the band. The prose is remembered underneath and
    # restored the moment the alarm clears.
    if _alarm_line != "":
        return
    _show(text, false)


# Take over the band with a line.
#
# immediate=false is the courteous path (atmosphere): if a scroll is in flight we
# let it finish and switch on the next pass, so the player is never yanked
# mid-read. immediate=true interrupts (alarms) — a durability outage does not
# wait politely behind a paragraph of village prose.
#
# A new line resets the burst counter so it gets the 5-scroll attention burst.
#
# INVARIANT — a queued atmosphere line can never resurface over a firing alarm.
# Two things hold it, and a future caller must not break either: (1) the immediate
# path below CLEARS _pending_line as it takes over, so an atmosphere line queued
# before the alarm is dropped, and (2) push() — the only non-alarm caller — bails
# out before reaching here while _alarm_line is set, so nothing can queue behind
# an alarm afterwards. _atmosphere_line still holds the prose, so it is restored
# (not lost) when the alarm clears.
func _show(text: String, immediate: bool) -> void:
    if text == _active_line:
        return
    if _scrolling and not immediate:
        _pending_line = text
        return
    _active_line = text
    _pending_line = ""
    _burst_done = 0
    _repeat_timer.stop()
    _start_scroll()


func _start_scroll() -> void:
    _scrolling = true
    # Reset size and re-snap to the label's intrinsic size before
    # measuring — without this, custom_minimum_size from a previous
    # text or stale layout state can leave _label_width wrong on the
    # first frame.
    _label.custom_minimum_size = Vector2.ZERO
    _label.text = _active_line
    _label.reset_size()
    _label.size = _label.get_minimum_size()
    _label_width = _label.size.x
    _label_x = size.x + SIDE_PADDING
    _label.position = Vector2(_label_x, 0)


func _process(delta: float) -> void:
    if not _scrolling:
        return
    _label_x -= SCROLL_SPEED * delta
    _label.position.x = _label_x
    if _label_x + _label_width + SIDE_PADDING < 0:
        _on_scroll_finished()


func _on_scroll_finished() -> void:
    _scrolling = false
    # A line came in mid-scroll; switch to it now and reset the burst.
    if _pending_line != "":
        _active_line = _pending_line
        _pending_line = ""
        _burst_done = 0
        _repeat_timer.stop()
        _start_scroll()
        return
    if _active_line == "":
        _label.text = ""
        return
    # Schedule the next re-scroll. An alarm re-scrolls on its own fixed interval
    # for as long as it fires — it must never decay into the 15-minute heartbeat,
    # which is a fine cadence for weather prose and a terrible one for "the world
    # is not being saved". Otherwise: first BURST_COUNT scrolls at the 3-minute
    # interval, everything after on the slow heartbeat.
    _burst_done += 1
    var delay: float
    if _alarm_line != "" and _active_line == _alarm_line:
        delay = ALARM_REPEAT_SEC
    elif _burst_done < BURST_COUNT:
        delay = BURST_INTERVAL_SEC
    else:
        delay = HEARTBEAT_INTERVAL_SEC
    _repeat_timer.start(delay)


func _on_repeat_timeout() -> void:
    if _active_line == "":
        return
    _start_scroll()


# Poll the operator-gated alarm read. Operator-only (Auth.can_edit mirrors the
# plugins/administer capability the umbilical gate requires), so an ordinary
# player never issues this request.
func _fetch_alarms() -> void:
    if _alarm_http == null:
        return
    if not Auth.is_authenticated() or not Auth.can_edit:
        return
    var headers: PackedStringArray = Auth.auth_headers(false)
    var err := _alarm_http.request(Auth.api_base + "/api/village/umbilical/alarms", headers)
    if err != OK:
        # ERR_BUSY just means the previous poll is still in flight — skip this
        # cycle; the next tick picks it up.
        return


func _on_alarms_completed(_result: int, code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    # Fail SILENT on anything but a clean 200: the umbilical may be disabled
    # (404), the session may not be an operator (403), or the engine may be
    # briefly unreachable. None of those is an alarm, and none of them should be
    # allowed to disturb the atmosphere line.
    if code != 200:
        _apply_alarms([])
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if typeof(json) != TYPE_DICTIONARY:
        _apply_alarms([])
        return
    var raw = json.get("alarms", [])
    if typeof(raw) != TYPE_ARRAY:
        _apply_alarms([])
        return
    _apply_alarms(raw)


# Fold the firing alarms into one red band line, or hand the band back to the
# atmosphere when nothing is firing. The engine already renders each alarm as a
# plain-English sentence (Alarm.detail) — the client does not restate the
# diagnosis, it just carries it.
func _apply_alarms(alarms: Array) -> void:
    if _label == null:
        return

    var parts: PackedStringArray = PackedStringArray()
    for entry in alarms:
        if typeof(entry) != TYPE_DICTIONARY:
            continue
        var detail: String = ""
        var raw_detail = entry.get("detail", "")
        if typeof(raw_detail) == TYPE_STRING:
            detail = raw_detail
        if detail == "":
            var raw_kind = entry.get("kind", "")
            if typeof(raw_kind) == TYPE_STRING:
                detail = raw_kind
        if detail == "":
            continue
        parts.append(detail)

    if parts.is_empty():
        _clear_alarm()
        return

    var line: String = "*** ENGINE ALARM — " + " | ".join(parts) + " ***"
    if line == _alarm_line:
        return
    _alarm_line = line
    _label.add_theme_color_override("font_color", COLOR_ALARM)
    _show(line, true)


func _clear_alarm() -> void:
    if _alarm_line == "":
        return
    _alarm_line = ""
    _label.add_theme_color_override("font_color", COLOR_ATMOSPHERE)
    if _atmosphere_line != "":
        # Immediate, not courteous: the colour has already flipped back to the
        # atmosphere tone, so letting the resolved alarm finish its scroll would
        # paint red text in amber. Nothing is on fire — swap now.
        _show(_atmosphere_line, true)
        return
    # Nothing to fall back to (the atmosphere cascade hasn't produced prose yet).
    # Blank the band rather than leaving a resolved alarm scrolling forever.
    _active_line = ""
    _pending_line = ""
    _scrolling = false
    _repeat_timer.stop()
    _label.text = ""
