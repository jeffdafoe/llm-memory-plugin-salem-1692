extends Node
## Player-activity heartbeat (LLM-470). Tells the engine a human is at the
## client, so watching the village counts as being present.
##
## LLM-466 made eco-mode audience require a fresh server-side activity stamp,
## but only the write routes (move / speak / pay / gather) reach the server. A
## player who scrolls the map, opens the talk box and reads — the exact
## watching-without-playing the candle prompt was designed to permit — went
## activity-idle at the same rate as an abandoned tab and got prompted every
## hour. This node closes that: it watches for real input locally and reports it.
##
## Why its own node: main.gd's `_input` early-returns on `_pc_exists`, on the
## editor being active, and on `camera.modal_open` — which the candle prompt
## itself sets. A tracker hosted there would go deaf at exactly the moment it
## matters. `_input` fires on every node in the tree, so this one sees
## everything, and it never calls `accept_event`, so it cannot interfere with
## click-to-walk, camera pan, or any panel.
##
## What it does NOT do: fire on a timer alone. Every POST is gated on a real
## InputEvent having arrived since the last report, so an abandoned tab reports
## nothing and still gutters its candle on schedule (LLM-466 design point 6 —
## the client must not auto-answer).

## Minimum spacing between heartbeat POSTs. The horizon is an hour, so this is
## an order of magnitude finer than it needs to be; the cost is at most 12
## trivial POSTs an hour for an actively-watched client, and zero for an
## unattended one.
const HEARTBEAT_INTERVAL_SEC: float = 300.0

## How often to check whether a heartbeat is due. Decoupled from the interval so
## the check is cheap and the reported staleness is bounded by this, not by the
## interval.
const CHECK_INTERVAL_SEC: float = 15.0

## Set when any real input arrives; cleared when a heartbeat reports it.
var _input_since_report: bool = false
var _last_report_ms: int = 0
## Set by main.gd while the candle prompt is up. Suppresses the throttle: with
## the prompt showing, any input reports at once, so a scroll or a mouse-wiggle
## dismisses the candle in a round-trip instead of waiting out the interval.
## (A click needs none of this — it hits the overlay directly.)
var _prompt_showing: bool = false

var _http: HTTPRequest = null
var _in_flight: bool = false


func _ready() -> void:
    # Report at startup so the tab that just loaded doesn't wait an interval to
    # count. The WS connect stamps activity too; this is belt-and-braces for a
    # client whose socket attaches late.
    _last_report_ms = Time.get_ticks_msec()
    var timer := Timer.new()
    timer.wait_time = CHECK_INTERVAL_SEC
    timer.autostart = true
    timer.timeout.connect(_on_check_timer)
    add_child(timer)


func _input(event: InputEvent) -> void:
    if not _is_human_input(event):
        return
    _input_since_report = true
    # With the candle up, don't make the player wait out the throttle. Excluding
    # taps and clicks: those land on the modal overlay, which POSTs /pc/attend
    # itself, so reporting them here too would double-post every dismissal. What
    # this covers is the input the overlay does NOT see as an answer — motion,
    # scroll, keys, gestures.
    if _prompt_showing and not _is_press_input(event):
        _report()


func _is_press_input(event: InputEvent) -> bool:
    return event is InputEventMouseButton or event is InputEventScreenTouch


## Whether an event counts as evidence of a human. Mouse MOTION counts
## deliberately: it is the strongest "someone is at the machine" signal
## available, and a hidden or backgrounded tab produces none — which is exactly
## the discrimination this feature rests on. Everything here is passive
## observation; nothing is consumed.
##
## Known limit (accepted, see the ticket): on web, motion only arrives while the
## pointer is over the focused canvas, so watching on a second monitor with the
## mouse parked elsewhere still goes idle and gets the candle after an hour.
func _is_human_input(event: InputEvent) -> bool:
    return (event is InputEventMouseMotion
        or event is InputEventMouseButton
        or event is InputEventKey
        or event is InputEventScreenTouch
        or event is InputEventScreenDrag
        or event is InputEventPanGesture
        or event is InputEventMagnifyGesture)


## Called by main.gd when the candle prompt is raised or lowered.
func set_prompt_showing(showing: bool) -> void:
    _prompt_showing = showing


func _on_check_timer() -> void:
    if not _input_since_report:
        return # nobody has touched the client — let the candle gutter
    if Time.get_ticks_msec() - _last_report_ms < int(HEARTBEAT_INTERVAL_SEC * 1000.0):
        return
    _report()


## POST /pc/attend — the same route the candle's click uses. It means "a human
## is here": it stamps the activity cursor only, never the in-world input cursor
## (LastPCInputAt), so a heartbeat can never defer a lodger's idle auto-bed the
## way a fake move would.
func _report() -> void:
    if _in_flight or not Auth.is_authenticated():
        return
    if _http == null:
        _http = HTTPRequest.new()
        add_child(_http)
        _http.request_completed.connect(_on_report_completed)
    var url: String = Auth.api_base + "/api/village/pc/attend"
    var err := _http.request(url, Auth.auth_headers(), HTTPClient.METHOD_POST, "")
    if err != OK:
        push_warning("activity heartbeat request failed: %s" % err)
        return
    _in_flight = true
    # Clear optimistically: a dropped heartbeat costs at most one interval of
    # staleness against an hour-long horizon, whereas holding the flag until the
    # response would re-report the same input on every check while in flight.
    _input_since_report = false
    _last_report_ms = Time.get_ticks_msec()


func _on_report_completed(_result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray) -> void:
    _in_flight = false
    if not Auth.check_response(code):
        return
    if code < 200 or code >= 300:
        push_warning("activity heartbeat non-2xx: code=%s" % code)
