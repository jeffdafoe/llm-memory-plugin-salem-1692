extends SceneTree

## Headless harness for the ticker's broken-things band (LLM-654) in
## client/scripts/village_ticker.gd: the world read's `damaged` lines are
## parsed, shown AHEAD of the atmosphere in one band, cleared when mended, and a
## mended well with no atmosphere blanks the band instead of leaving the stale
## line scrolling.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/village_ticker_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The ticker is instantiated off-tree via .new() so _ready() never fires; the
## tests hand it only the nodes the path under test touches.

const TESTS := [
    "_test_band_composition",
    "_test_world_read_parses_damaged",
    "_test_mended_with_no_atmosphere_blanks_the_band",
]

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _run_all()
    _check_all_tests_ran()
    print("\n[village_ticker_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[village_ticker_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)


func _done() -> void:
    _completed[_current] = true


func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)


func _ticker():
    return load("res://scripts/village_ticker.gd").new()


func _body(dict: Dictionary) -> PackedByteArray:
    return JSON.stringify(dict).to_utf8_buffer()


func _test_band_composition() -> void:
    var t = _ticker()
    t._atmosphere_line = "Mist on the green."
    _check("atmosphere alone", t._band_line(), "Mist on the green.")
    t._damage_line = "The windlass at the Well by the Mill is down."
    _check("broken things lead the atmosphere", t._band_line(),
        "The windlass at the Well by the Mill is down.   ~   Mist on the green.")
    t._atmosphere_line = ""
    _check("broken things alone", t._band_line(), "The windlass at the Well by the Mill is down.")
    t.free()
    _done()


## A firing alarm owns the band, so _refresh_band returns before touching the
## label or timer — which lets the parse be checked off-tree.
func _test_world_read_parses_damaged() -> void:
    var t = _ticker()
    t._alarm_line = "*** ENGINE ALARM ***"
    t._on_world_state_completed(0, 200, PackedStringArray(), _body({
        "atmosphere": "Mist on the green.",
        "damaged": [
            {"object_id": "w1", "text": "The windlass at the Well by the Mill is down."},
            {"object_id": "bad", "text": 5},
        ],
    }))
    _check("damaged line parsed, bad entry skipped", t._damage_line, "The windlass at the Well by the Mill is down.")
    _check("atmosphere kept", t._atmosphere_line, "Mist on the green.")
    t._on_world_state_completed(0, 200, PackedStringArray(), _body({"atmosphere": "Mist on the green."}))
    _check("a mended well clears the line", t._damage_line, "")
    t.free()
    _done()


func _test_mended_with_no_atmosphere_blanks_the_band() -> void:
    var t = _ticker()
    var label := Label.new()
    var timer := Timer.new()
    t._label = label
    t._repeat_timer = timer
    t._active_line = "The windlass at the Well by the Mill is down."
    label.text = t._active_line
    t._refresh_band()
    _check("band blanked", t._active_line, "")
    _check("label cleared", label.text, "")
    label.free()
    timer.free()
    t.free()
    _done()
