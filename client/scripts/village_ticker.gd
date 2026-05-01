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
## Always-latest model: at most one line is in flight, at most one
## queued. A new chronicler fire arriving mid-scroll replaces whatever
## was queued, so when the current scroll finishes the marquee jumps
## straight to the latest atmosphere — never a backlog of stale prose
## from earlier in the day.

signal clicked

const SCROLL_SPEED: float = 40.0
const TICKER_HEIGHT: float = 24.0
const SIDE_PADDING: float = 12.0

var _label: Label = null
var _clip: Control = null
var _queue: Array[String] = []
var _current_text: String = ""
var _label_width: float = 0.0
var _label_x: float = 0.0
var _http: HTTPRequest = null
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


# Add a raw text line to the marquee. If idle, starts immediately;
# otherwise replaces the queued slot so the next scroll jumps to the
# latest line — anything that was queued in between gets skipped. We
# don't cut the current scroll short because the player is actively
# reading it; we just make sure they never have to wait through a
# backlog to see what just happened.
func push(text: String) -> void:
    if text == "":
        return
    if _current_text == "":
        _start_text(text)
        return
    _queue.clear()
    _queue.append(text)


func _start_text(text: String) -> void:
    _current_text = text
    # Reset size and re-snap to the label's intrinsic size before
    # measuring — without this, custom_minimum_size from a previous
    # text or stale layout state can leave _label_width wrong on the
    # first frame.
    _label.custom_minimum_size = Vector2.ZERO
    _label.text = text
    _label.reset_size()
    _label.size = _label.get_minimum_size()
    _label_width = _label.size.x
    _label_x = size.x + SIDE_PADDING
    _label.position = Vector2(_label_x, 0)


func _process(delta: float) -> void:
    if _current_text == "":
        return
    _label_x -= SCROLL_SPEED * delta
    _label.position.x = _label_x
    if _label_x + _label_width + SIDE_PADDING < 0:
        _advance()


func _advance() -> void:
    if _queue.is_empty():
        _current_text = ""
        _label.text = ""
        _label_x = 0
        return
    _start_text(_queue.pop_front())


# Click anywhere on the ticker → emit clicked. main.gd routes this to
# the talk_panel's force_open_to_village_tab().
func _gui_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        clicked.emit()
        accept_event()
