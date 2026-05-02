extends PanelContainer
## Village ticker (ZBBS-087) — thin band below the top bar that scrolls
## chronicler atmosphere prose right-to-left in marquee fashion.
##
## Subscribes to world.world_environment_added for live chronicler
## fires; backloads the single latest row on init via
## POST /api/village/environment/recent. Click → opens the talk panel
## to the Village tab (mechanical events live there; the ticker is
## chronicler-curated atmosphere only — they don't repeat content).
##
## Always-latest model: at most one active line. A new chronicler fire
## replaces it (politely — the current scroll, if any, finishes first
## so the player isn't yanked mid-read). After each scroll the line
## re-scrolls on a schedule: a 5-scroll burst at 3-minute intervals,
## then a 15-minute heartbeat. A new line resets the burst counter.

signal clicked

const SCROLL_SPEED: float = 40.0
const TICKER_HEIGHT: float = 24.0
const SIDE_PADDING: float = 12.0
const BURST_COUNT: int = 5
const BURST_INTERVAL_SEC: float = 180.0
const HEARTBEAT_INTERVAL_SEC: float = 900.0

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
# Dedupe by world_environment.id so a row that's in both the backload
# response and a near-simultaneous WS broadcast only scrolls once.
var _seen_ids: Dictionary = {}


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
    _http.request_completed.connect(_on_environment_recent_completed)

    _repeat_timer = Timer.new()
    _repeat_timer.one_shot = true
    _repeat_timer.timeout.connect(_on_repeat_timeout)
    add_child(_repeat_timer)


# Wired by main.gd after the world is ready. Subscribes to the live
# chronicler-atmosphere signal and kicks off the initial backload so
# the ticker has something to display before the next chronicler fire.
func attach_world(world: Node) -> void:
    if world == null:
        return
    if world.has_signal("world_environment_added"):
        world.world_environment_added.connect(_on_world_environment_added)
    _load_recent()


func _load_recent() -> void:
    if _http == null:
        return
    if not Auth.is_authenticated():
        return
    var url: String = Auth.api_base + "/api/village/environment/recent"
    var headers: PackedStringArray = Auth.auth_headers()
    # Just the latest atmosphere line — older prose feels stale once
    # the world clock has moved on (morning prose at evening was the
    # specific complaint that motivated this).
    var body := JSON.stringify({"limit": 1})
    var err := _http.request(url, headers, HTTPClient.METHOD_POST, body)
    if err != OK:
        return


func _on_environment_recent_completed(_result: int, code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    if code != 200:
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if typeof(json) != TYPE_DICTIONARY:
        return
    var rows = json.get("rows", [])
    if typeof(rows) != TYPE_ARRAY or rows.is_empty():
        return
    # Backload comes newest-first; push in array order so the marquee
    # surfaces the most recent prose first as the user starts watching.
    for entry in rows:
        if typeof(entry) == TYPE_DICTIONARY:
            push_row(entry)


func _on_world_environment_added(data: Dictionary) -> void:
    push_row(data)


# Add one world_environment row to the marquee, deduping by id so the
# backload-WS race window can't queue the same prose twice. Falls back
# to a plain text push if the row has no id (defensive — backend always
# stamps id from RETURNING, but be lenient in case a future caller
# constructs a row without one).
func push_row(row: Dictionary) -> void:
    var text: String = str(row.get("text", ""))
    if text == "":
        return
    var id_val := int(row.get("id", 0))
    if id_val > 0:
        if _seen_ids.has(id_val):
            return
        _seen_ids[id_val] = true
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


# Click anywhere on the ticker → emit clicked. main.gd routes this to
# the talk_panel's force_open_to_village_tab().
func _gui_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        clicked.emit()
        accept_event()
