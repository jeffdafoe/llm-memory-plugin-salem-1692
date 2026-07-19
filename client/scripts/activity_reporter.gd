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
## matters. This node never calls `accept_event`, so it is a pure observer and
## cannot interfere with click-to-walk, camera pan, or any panel.
##
## Scope of what it observes: `_input` runs BEFORE Control `gui_input` in
## Godot's pipeline, so events the UI later consumes are still seen here. It is
## not a guarantee of seeing every event, though — an earlier `_input` handler
## that called `set_input_as_handled()` would stop propagation. Nothing in this
## client does that today, and the signal that carries the feature is mouse
## motion, which nothing consumes.
##
## What it does NOT do: fire on a timer alone. Every POST is gated on a real
## InputEvent having arrived since the last report, so an abandoned tab reports
## nothing and still gutters its candle on schedule (LLM-466 design point 6 —
## the client must not auto-answer).
##
## The state machine (_mark_input / _due / _begin_report / _finish_report) is
## kept free of HTTP and autoload access so tests/activity_reporter_test.gd can
## drive it headless off-tree.

## Minimum spacing between heartbeat POSTs. The horizon is an hour, so this is
## an order of magnitude finer than it needs to be; the cost is at most 12
## trivial POSTs an hour for an actively-watched client, and zero for an
## unattended one.
const HEARTBEAT_INTERVAL_SEC: float = 300.0

## How often to check whether a heartbeat is due. Decoupled from the interval so
## the check is cheap and the reported staleness is bounded by this, not by the
## interval.
const CHECK_INTERVAL_SEC: float = 15.0

## Set when any real input arrives; cleared when a report is dispatched, and
## restored if that report fails.
var _input_since_report: bool = false
## Timestamp of the last dispatched report. 0 means "no report yet, so the next
## input is due immediately" — that is the startup state (a freshly loaded tab
## should count from its first input, not five minutes later) and also the state
## a failed report rolls back to, so a retry rides the next 15s check.
var _last_report_ms: int = 0
## Set by main.gd while the candle prompt is up. Suppresses the throttle: with
## the prompt showing, any input reports at once, so a scroll or a mouse-wiggle
## dismisses the candle in a round-trip instead of waiting out the interval.
## (A click needs none of this — it hits the overlay directly.)
var _prompt_showing: bool = false

var _http: HTTPRequest = null
var _in_flight: bool = false


func _ready() -> void:
    var timer := Timer.new()
    timer.wait_time = CHECK_INTERVAL_SEC
    timer.autostart = true
    timer.timeout.connect(_on_check_timer)
    add_child(timer)


func _input(event: InputEvent) -> void:
    if not _is_human_input(event):
        return
    _mark_input()
    # With the candle up, don't make the player wait out the throttle. Excluding
    # taps and clicks: those land on the modal overlay, which POSTs /pc/attend
    # itself, so reporting them here too would double-post every dismissal. What
    # this covers is the input the overlay does NOT see as an answer — motion,
    # scroll, keys, gestures. (The overlay answers on mouse-button and screen-
    # touch releases only; it has no keyboard or gamepad dismissal path, so
    # nothing else is double-counted.)
    if _prompt_showing and not _is_press_input(event):
        _report()


## Whether an event counts as evidence of a human. Mouse MOTION counts
## deliberately: it is the strongest "someone is at the machine" signal
## available, and a hidden or backgrounded tab produces none — which is exactly
## the discrimination this feature rests on.
##
## This is presence EVIDENCE, not proof of intentional interaction: a trackpad
## bump or a pointer crossing the window renews the horizon for another hour.
## Accepted deliberately (Jeff, 2026-07-19) because the case being caught is a
## tab nobody touches at all. If false positives ever start costing real money,
## the lever is to require motion over the game viewport, or to pair it with
## focus/visibility — not to drop motion, which would put watchers back in the
## abandoned-tab bucket.
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


func _is_press_input(event: InputEvent) -> bool:
    return event is InputEventMouseButton or event is InputEventScreenTouch


## Called by main.gd when the candle prompt is raised or lowered.
func set_prompt_showing(showing: bool) -> void:
    _prompt_showing = showing


func _mark_input() -> void:
    _input_since_report = true


## Whether a throttled heartbeat should go out now. Gated on real input having
## arrived — an untouched client is never due, which is the invariant that keeps
## an abandoned tab pacing down.
func _due(now_ms: int) -> bool:
    if not _input_since_report:
        return false
    if _last_report_ms == 0:
        return true
    return now_ms - _last_report_ms >= int(HEARTBEAT_INTERVAL_SEC * 1000.0)


## Consume the pending input and start the throttle window. Clearing before the
## response lands is deliberate: input arriving while the request is in flight
## re-sets the flag and is reported by the NEXT beat rather than being swallowed
## by this one's completion.
func _begin_report(now_ms: int) -> void:
    _input_since_report = false
    _last_report_ms = now_ms


## Settle a dispatched report. On failure the pending input is restored and the
## throttle window is rolled back to "due immediately", so a transient network
## blip costs one 15s check rather than suppressing activity for the rest of the
## hour-long horizon. Never clears _input_since_report — input that arrived
## while the request was in flight must survive completion.
func _finish_report(success: bool) -> void:
    _in_flight = false
    if success:
        return
    _input_since_report = true
    _last_report_ms = 0


func _on_check_timer() -> void:
    if not _due(Time.get_ticks_msec()):
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
    _begin_report(Time.get_ticks_msec())


func _on_report_completed(_result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray) -> void:
    var ok := code >= 200 and code < 300
    _finish_report(ok)
    if not Auth.check_response(code):
        return
    if not ok:
        push_warning("activity heartbeat non-2xx: code=%s" % code)
