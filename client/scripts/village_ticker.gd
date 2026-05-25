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
    _label.add_theme_color_override("font_color", Color(0.85, 0.78, 0.55, 1.0))
    _label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    _label.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
    _label.size_flags_vertical = Control.SIZE_FILL
    _clip.add_child(_label)

    _http = HTTPRequest.new()
    add_child(_http)
    _http.request_completed.connect(_on_world_state_completed)

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


# Wired by main.gd once the client is authenticated. Kicks off the first
# world-state fetch so the ticker has atmosphere to display, then starts the
# slow refresh poll.
func begin() -> void:
    _fetch_world_state()
    # Guard against begin() being called before _ready() built the timer (the
    # current call site adds the node first, but don't crash if that changes).
    if _poll_timer != null:
        _poll_timer.start()


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


# Add a raw text line to the marquee. Same line as the active one is a
# no-op (the schedule continues uninterrupted). A different line takes
# over: if a scroll is in flight we let it finish and switch on the
# next pass (player is mid-read), otherwise we cancel any pending
# heartbeat and start the new line immediately. New line resets the
# burst counter so the player gets the 5-scroll attention burst.
func push(text: String) -> void:
    if text == "":
        return
    if text == _active_line:
        return
    if _scrolling:
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
    # Schedule the next re-scroll. First BURST_COUNT scrolls fire at
    # the 3-minute interval; everything after is the slow heartbeat.
    _burst_done += 1
    var delay: float
    if _burst_done < BURST_COUNT:
        delay = BURST_INTERVAL_SEC
    else:
        delay = HEARTBEAT_INTERVAL_SEC
    _repeat_timer.start(delay)


func _on_repeat_timeout() -> void:
    if _active_line == "":
        return
    _start_scroll()
