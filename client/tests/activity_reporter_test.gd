extends SceneTree

## Headless regression harness for the LLM-470 activity heartbeat
## (client/scripts/activity_reporter.gd) — the state machine that decides when a
## client tells the engine a human is here.
##
## Two invariants carry the whole feature and both are easy to break by accident:
##
##   1. An untouched client NEVER reports. If this breaks, an abandoned tab keeps
##      the village at full cadence forever and LLM-466 is undone.
##   2. A failed report does not swallow the input that prompted it. If this
##      breaks, one dropped request suppresses a watching player for the rest of
##      the hour-long horizon and they get the candle anyway — the LLM-470 bug.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/activity_reporter_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## activity_reporter.gd is instantiated off-tree via .new() so _ready() never
## fires (no Timer child) and no autoload or HTTPRequest is touched: the state
## machine under test (_mark_input / _due / _begin_report / _finish_report) is
## deliberately free of both, so the decision logic is exercised without a
## network or an Auth singleton.

const MINUTE_MS := 60 * 1000

var _reporter: Node = null
var _failures := 0
var _checks := 0


func _initialize() -> void:
    _run_all()
    print("\n[activity_reporter_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[activity_reporter_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _fresh() -> Node:
    if _reporter != null:
        _reporter.free()
    _reporter = load("res://scripts/activity_reporter.gd").new()
    return _reporter


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _run_all() -> void:
    _test_untouched_client_never_reports()
    _test_first_input_reports_immediately()
    _test_throttle_holds_then_releases()
    _test_failed_report_restores_pending_input()
    _test_successful_report_does_not_restore()
    _test_input_during_flight_survives_completion()
    _test_human_input_classification()
    if _reporter != null:
        _reporter.free()
        _reporter = null


## Invariant 1. The whole cost fix rests on this: no input, no report, ever —
## regardless of how much time passes.
func _test_untouched_client_never_reports() -> void:
    var r := _fresh()
    _check("untouched at t=0", r._due(0), false)
    _check("untouched after 5min", r._due(5 * MINUTE_MS), false)
    _check("untouched after 2h", r._due(120 * MINUTE_MS), false)


## A freshly loaded tab counts from its first input, not one interval later.
func _test_first_input_reports_immediately() -> void:
    var r := _fresh()
    r._mark_input()
    _check("first input is due at once", r._due(0), true)


func _test_throttle_holds_then_releases() -> void:
    var r := _fresh()
    r._mark_input()
    r._begin_report(MINUTE_MS)
    _check("cleared by report", r._due(MINUTE_MS), false)

    r._mark_input()
    _check("1 min after report", r._due(2 * MINUTE_MS), false)
    _check("4 min after report", r._due(5 * MINUTE_MS), false)
    _check("5 min after report", r._due(6 * MINUTE_MS), true)


## Invariant 2. A transient failure must cost one 15s check, not the horizon.
func _test_failed_report_restores_pending_input() -> void:
    var r := _fresh()
    r._mark_input()
    r._begin_report(MINUTE_MS)
    _check("not due mid-flight", r._due(MINUTE_MS), false)

    r._finish_report(false)
    _check("pending input restored", r._input_since_report, true)
    _check("due immediately after failure", r._due(MINUTE_MS), true)


func _test_successful_report_does_not_restore() -> void:
    var r := _fresh()
    r._mark_input()
    r._begin_report(MINUTE_MS)
    r._finish_report(true)
    _check("no phantom input after success", r._input_since_report, false)
    _check("throttle intact after success", r._due(2 * MINUTE_MS), false)


## Input arriving while the request is in flight belongs to the NEXT beat — it
## must not be erased by this one's completion. This is the case the optimistic
## clear is most likely to regress if completion handling is ever reworked.
func _test_input_during_flight_survives_completion() -> void:
    var r := _fresh()
    r._mark_input()
    r._begin_report(MINUTE_MS)
    r._mark_input() # player keeps moving while the POST is in flight
    r._finish_report(true)
    _check("in-flight input survives success", r._input_since_report, true)


## Mouse motion must count (a hidden tab produces none — that is the whole
## discrimination), and presses must be recognised as the overlay's own
## dismissal path so the prompt-showing fast path doesn't double-post them.
func _test_human_input_classification() -> void:
    var r := _fresh()
    _check("motion is human input", r._is_human_input(InputEventMouseMotion.new()), true)
    _check("key is human input", r._is_human_input(InputEventKey.new()), true)
    _check("button is human input", r._is_human_input(InputEventMouseButton.new()), true)
    _check("touch is human input", r._is_human_input(InputEventScreenTouch.new()), true)
    _check("action is not human input", r._is_human_input(InputEventAction.new()), false)

    _check("button is a press input", r._is_press_input(InputEventMouseButton.new()), true)
    _check("touch is a press input", r._is_press_input(InputEventScreenTouch.new()), true)
    _check("motion is not a press input", r._is_press_input(InputEventMouseMotion.new()), false)
