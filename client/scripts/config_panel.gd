extends Control
## Config panel — admin controls for world-level settings.
## Currently: day/night phase display + force-toggle buttons.
## Fetches state from GET /api/village/world; force-phase via
## POST /api/village/world/force-phase (admin only).

signal closed

const COLOR_BG = Color(0.05, 0.03, 0.02, 0.85)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_LABEL = Color(0.54, 0.48, 0.31, 1.0)
const COLOR_VALUE = Color(0.92, 0.82, 0.55, 1.0)
const COLOR_BTN_BG = Color(0.35, 0.25, 0.12, 1.0)
const COLOR_BTN_BORDER = Color(0.55, 0.42, 0.25, 1.0)
const COLOR_BTN_HOVER_BG = Color(0.45, 0.32, 0.15, 1.0)
const COLOR_STATUS_OK = Color(0.55, 0.78, 0.45, 0.9)
const COLOR_STATUS_ERR = Color(0.85, 0.45, 0.40, 0.9)

const TOP_BAR_HEIGHT: float = 40.0
const REFRESH_INTERVAL: float = 1.0  # countdown ticker
const REFETCH_INTERVAL: float = 10.0  # poll server state

var _font: Font = null
var _panel: PanelContainer = null
var _content: VBoxContainer = null

# Live values (most recent from server)
var _phase: String = ""
var _last_transition_at: String = ""   # RFC3339
var _next_transition_at: String = ""   # RFC3339
var _next_transition_phase: String = ""
var _dawn_time: String = ""
var _dusk_time: String = ""
var _timezone: String = ""
var _server_time: String = ""
var _rotation_time: String = ""
var _last_rotation_at: String = ""
var _next_rotation_at: String = ""

# UI elements that update without a full rebuild
var _phase_value: Label = null
var _next_countdown: Label = null
var _last_transition_value: Label = null
var _server_time_value: Label = null
var _next_rotation_countdown: Label = null
var _last_rotation_value: Label = null
var _status_label: Label = null

var _refresh_timer: Timer = null
var _refetch_timer: Timer = null

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    # Full-screen overlay
    anchors_preset = Control.PRESET_FULL_RECT
    anchor_right = 1.0
    anchor_bottom = 1.0

    # Dim background — click to close
    var bg = ColorRect.new()
    bg.color = COLOR_BG
    bg.anchors_preset = Control.PRESET_FULL_RECT
    bg.anchor_right = 1.0
    bg.anchor_bottom = 1.0
    bg.gui_input.connect(_on_bg_input)
    add_child(bg)

    # Centered panel — doesn't need the full height; give it a readable width
    _panel = PanelContainer.new()
    _panel.anchor_left = 0.25
    _panel.anchor_right = 0.75
    _panel.anchor_top = 0.15
    _panel.anchor_bottom = 0.85

    var panel_style = StyleBoxFlat.new()
    panel_style.bg_color = COLOR_PANEL_BG
    panel_style.border_width_left = 2
    panel_style.border_width_top = 2
    panel_style.border_width_right = 2
    panel_style.border_width_bottom = 2
    panel_style.border_color = COLOR_BORDER
    panel_style.corner_radius_left_top = 4
    panel_style.corner_radius_right_top = 4
    panel_style.corner_radius_left_bottom = 4
    panel_style.corner_radius_right_bottom = 4
    panel_style.content_margin_left = 28.0
    panel_style.content_margin_right = 28.0
    panel_style.content_margin_top = 24.0
    panel_style.content_margin_bottom = 24.0
    _panel.add_theme_stylebox_override("panel", panel_style)
    add_child(_panel)

    _content = VBoxContainer.new()
    _content.add_theme_constant_override("separation", 14)
    _panel.add_child(_content)

    _build_layout()

    # Timers for live UI updates. Refresh ticks the countdown every second;
    # refetch pulls fresh server state every 10s so the Config screen stays
    # honest about the phase even without user interaction.
    _refresh_timer = Timer.new()
    _refresh_timer.wait_time = REFRESH_INTERVAL
    _refresh_timer.autostart = false
    _refresh_timer.timeout.connect(_update_countdown)
    add_child(_refresh_timer)

    _refetch_timer = Timer.new()
    _refetch_timer.wait_time = REFETCH_INTERVAL
    _refetch_timer.autostart = false
    _refetch_timer.timeout.connect(fetch_state)
    add_child(_refetch_timer)

    visibility_changed.connect(_on_visibility_changed)

func _build_layout() -> void:
    var title = Label.new()
    title.text = "World Controls"
    title.add_theme_color_override("font_color", COLOR_TEXT)
    title.add_theme_font_override("font", _font)
    title.add_theme_font_size_override("font_size", 28)
    title.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    _content.add_child(title)

    _add_separator()

    # Phase row — big, readable
    var phase_row = HBoxContainer.new()
    phase_row.add_theme_constant_override("separation", 12)
    _content.add_child(phase_row)

    var phase_label = _make_label("Current phase:", COLOR_LABEL, 18)
    phase_row.add_child(phase_label)

    _phase_value = _make_label("—", COLOR_VALUE, 22)
    phase_row.add_child(_phase_value)

    # Countdown row
    var count_row = HBoxContainer.new()
    count_row.add_theme_constant_override("separation", 12)
    _content.add_child(count_row)

    count_row.add_child(_make_label("Next transition:", COLOR_LABEL, 14))
    _next_countdown = _make_label("—", COLOR_TEXT, 14)
    count_row.add_child(_next_countdown)

    # Last-transition row
    var last_row = HBoxContainer.new()
    last_row.add_theme_constant_override("separation", 12)
    _content.add_child(last_row)

    last_row.add_child(_make_label("Last transition:", COLOR_LABEL, 14))
    _last_transition_value = _make_label("—", COLOR_TEXT_DIM, 14)
    last_row.add_child(_last_transition_value)

    # Server time row
    var stime_row = HBoxContainer.new()
    stime_row.add_theme_constant_override("separation", 12)
    _content.add_child(stime_row)

    stime_row.add_child(_make_label("World clock:", COLOR_LABEL, 14))
    _server_time_value = _make_label("—", COLOR_TEXT_DIM, 14)
    stime_row.add_child(_server_time_value)

    _add_separator()

    # Daily rotation (rotatable assets — notice boards, laundry)
    var rotation_header = _make_label("Daily rotation", COLOR_LABEL, 14)
    _content.add_child(rotation_header)

    var rot_next_row = HBoxContainer.new()
    rot_next_row.add_theme_constant_override("separation", 12)
    _content.add_child(rot_next_row)
    rot_next_row.add_child(_make_label("Next rotation:", COLOR_LABEL, 14))
    _next_rotation_countdown = _make_label("—", COLOR_TEXT, 14)
    rot_next_row.add_child(_next_rotation_countdown)

    var rot_last_row = HBoxContainer.new()
    rot_last_row.add_theme_constant_override("separation", 12)
    _content.add_child(rot_last_row)
    rot_last_row.add_child(_make_label("Last rotation:", COLOR_LABEL, 14))
    _last_rotation_value = _make_label("—", COLOR_TEXT_DIM, 14)
    rot_last_row.add_child(_last_rotation_value)

    _add_separator()

    # Force-phase controls
    var force_header = _make_label("Force phase (dev/admin)", COLOR_LABEL, 14)
    _content.add_child(force_header)

    var btn_row = HBoxContainer.new()
    btn_row.add_theme_constant_override("separation", 8)
    _content.add_child(btn_row)

    btn_row.add_child(_make_button("Force Day", func(): _send_force("day")))
    btn_row.add_child(_make_button("Force Night", func(): _send_force("night")))
    btn_row.add_child(_make_button("Force Rotate", _send_force_rotate))

    _status_label = _make_label("", COLOR_TEXT_DIM, 12)
    _content.add_child(_status_label)

func _make_label(text: String, color: Color, size: int) -> Label:
    var lbl = Label.new()
    lbl.text = text
    lbl.add_theme_color_override("font_color", color)
    lbl.add_theme_font_size_override("font_size", size)
    return lbl

func _make_button(text: String, cb: Callable) -> Button:
    var btn = Button.new()
    btn.text = text
    btn.add_theme_color_override("font_color", COLOR_TEXT)
    btn.add_theme_color_override("font_hover_color", COLOR_VALUE)
    btn.add_theme_font_override("font", _font)
    btn.add_theme_font_size_override("font_size", 16)

    var normal = StyleBoxFlat.new()
    normal.bg_color = COLOR_BTN_BG
    normal.border_width_left = 1
    normal.border_width_top = 1
    normal.border_width_right = 1
    normal.border_width_bottom = 1
    normal.border_color = COLOR_BTN_BORDER
    normal.corner_radius_left_top = 3
    normal.corner_radius_right_top = 3
    normal.corner_radius_left_bottom = 3
    normal.corner_radius_right_bottom = 3
    normal.content_margin_left = 14.0
    normal.content_margin_right = 14.0
    normal.content_margin_top = 6.0
    normal.content_margin_bottom = 6.0
    btn.add_theme_stylebox_override("normal", normal)

    var hover = normal.duplicate()
    hover.bg_color = COLOR_BTN_HOVER_BG
    btn.add_theme_stylebox_override("hover", hover)

    btn.pressed.connect(cb)
    return btn

func _add_separator() -> void:
    var sep = HSeparator.new()
    sep.add_theme_constant_override("separation", 2)
    _content.add_child(sep)

## Input handling — close on ESC or left-click outside the panel. Matches the
## asset popup's "click outside to dismiss" feel.
func _input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        _close()
        get_viewport().set_input_as_handled()

func _on_bg_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        var panel_rect: Rect2 = _panel.get_global_rect()
        if not panel_rect.has_point(event.position):
            _close()

func _close() -> void:
    visible = false
    closed.emit()

func _on_visibility_changed() -> void:
    if visible:
        fetch_state()
        _refresh_timer.start()
        _refetch_timer.start()
    else:
        _refresh_timer.stop()
        _refetch_timer.stop()
        _set_status("", false)

## Fetch current world state from the server and refresh the UI.
func fetch_state() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_state_response.bind(http))
    var headers = ["Authorization: " + Auth.get_auth_header()]
    http.request(Auth.api_base + "/api/village/world", headers)

func _on_state_response(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        _set_status("Failed to load world state (" + str(response_code) + ")", true)
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if typeof(json) != TYPE_DICTIONARY:
        _set_status("Malformed world state response", true)
        return

    _phase = json.get("phase", "")
    _last_transition_at = json.get("last_transition_at", "")
    _next_transition_at = json.get("next_transition_at", "")
    _next_transition_phase = json.get("next_transition_phase", "")
    _dawn_time = json.get("dawn_time", "")
    _dusk_time = json.get("dusk_time", "")
    _timezone = json.get("timezone", "")
    _server_time = json.get("server_time", "")
    _rotation_time = json.get("rotation_time", "")
    _last_rotation_at = json.get("last_rotation_at", "")
    _next_rotation_at = json.get("next_rotation_at", "")

    _refresh_labels()

func _refresh_labels() -> void:
    _phase_value.text = _phase.to_upper() if _phase != "" else "—"
    _phase_value.add_theme_color_override("font_color",
        COLOR_VALUE if _phase == "day" else Color(0.55, 0.62, 0.95, 1.0) if _phase == "night" else COLOR_TEXT_DIM
    )
    _last_transition_value.text = _format_iso_local(_last_transition_at)
    _server_time_value.text = _format_iso_local(_server_time) + "  (" + _timezone + ")"
    if _last_rotation_value != null:
        _last_rotation_value.text = _format_iso_local(_last_rotation_at)
    _update_countdown()

## Ticks once per second when the panel is visible — updates only the
## countdown labels so the rest of the UI doesn't flicker.
func _update_countdown() -> void:
    if _next_transition_at == "":
        _next_countdown.text = "—"
    else:
        var phase_countdown: String = _format_countdown_until(_next_transition_at)
        if phase_countdown == "":
            _next_countdown.text = "(any moment)"
        else:
            _next_countdown.text = phase_countdown + " → " + _next_transition_phase.to_upper()

    if _next_rotation_countdown != null:
        if _next_rotation_at == "":
            _next_rotation_countdown.text = "—"
        else:
            var rot_countdown: String = _format_countdown_until(_next_rotation_at)
            _next_rotation_countdown.text = rot_countdown if rot_countdown != "" else "(any moment)"

## Format the "h m s" countdown from now to the given RFC3339 timestamp.
## Returns an empty string if the timestamp is already in the past.
func _format_countdown_until(iso: String) -> String:
    var delta_s: int = _iso_seconds_until(iso)
    if delta_s <= 0:
        return ""
    var h: int = int(delta_s / 3600)
    var m: int = int((delta_s % 3600) / 60)
    var s: int = delta_s % 60
    if h > 0:
        return "%dh %dm %ds" % [h, m, s]
    if m > 0:
        return "%dm %ds" % [m, s]
    return "%ds" % s

## Parse an RFC3339 string into seconds-from-now using local clock. Godot's
## Time APIs handle "YYYY-MM-DDTHH:MM:SSZ" via Time.get_unix_time_from_datetime_string.
func _iso_seconds_until(iso: String) -> int:
    var ts := Time.get_unix_time_from_datetime_string(iso)
    if ts == 0:
        return 0
    var now_ts := Time.get_unix_time_from_system()
    return int(ts - now_ts)

## Convert an RFC3339 UTC timestamp into "YYYY-MM-DD HH:MM:SS" local time for
## display. Keeps things readable without a heavy date-formatting dep.
func _format_iso_local(iso: String) -> String:
    if iso == "":
        return "—"
    var ts := Time.get_unix_time_from_datetime_string(iso)
    if ts == 0:
        return iso
    var dt: Dictionary = Time.get_datetime_dict_from_unix_time(int(ts))
    return "%04d-%02d-%02d %02d:%02d:%02d" % [dt.year, dt.month, dt.day, dt.hour, dt.minute, dt.second]

## POST the force-phase action. Refetches state on success so the UI reflects
## the new phase + new last_transition_at.
func _send_force(phase: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_force_response.bind(http, phase))
    var headers = [
        "Authorization: " + Auth.get_auth_header(),
        "Content-Type: application/json",
    ]
    var payload = JSON.stringify({"phase": phase})
    http.request(Auth.api_base + "/api/village/world/force-phase", headers, HTTPClient.METHOD_POST, payload)
    _set_status("Forcing " + phase + "...", false)

func _on_force_response(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, requested: String) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS:
        _set_status("Force " + requested + " failed: network error", true)
        return
    if response_code == 403:
        _set_status("Admin access required", true)
        return
    if response_code != 200:
        _set_status("Force " + requested + " failed (" + str(response_code) + ")", true)
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    var applied: String = requested
    var affected: int = 0
    if typeof(json) == TYPE_DICTIONARY:
        applied = json.get("phase", requested)
        affected = int(json.get("objects_affected", 0))
    _set_status("Forced " + applied.to_upper() + " — " + str(affected) + " objects updated", false)
    fetch_state()

## POST /api/village/world/force-rotate — kicks the daily rotation pass
## immediately. Affected objects flip over each asset's transition_spread_seconds
## window so updates trickle in rather than all landing at once.
func _send_force_rotate() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_force_rotate_response.bind(http))
    var headers = [
        "Authorization: " + Auth.get_auth_header(),
        "Content-Type: application/json",
    ]
    http.request(Auth.api_base + "/api/village/world/force-rotate", headers, HTTPClient.METHOD_POST, "{}")
    _set_status("Forcing rotation...", false)

func _on_force_rotate_response(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS:
        _set_status("Force rotate failed: network error", true)
        return
    if response_code == 403:
        _set_status("Admin access required", true)
        return
    if response_code != 200:
        _set_status("Force rotate failed (" + str(response_code) + ")", true)
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    var affected: int = 0
    if typeof(json) == TYPE_DICTIONARY:
        affected = int(json.get("objects_affected", 0))
    _set_status("Rotation scheduled — " + str(affected) + " objects over their spread windows", false)
    fetch_state()

func _set_status(text: String, is_error: bool) -> void:
    if _status_label == null:
        return
    _status_label.text = text
    _status_label.add_theme_color_override("font_color", COLOR_STATUS_ERR if is_error else COLOR_STATUS_OK)
