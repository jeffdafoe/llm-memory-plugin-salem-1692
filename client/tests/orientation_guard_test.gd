extends SceneTree

## Headless harness for the LLM-702 landscape guard
## (client/scripts/orientation_guard.gd): on a tablet the client covers itself
## while the window is portrait and swallows taps under the cover.
##
## Covers the cover decision (only a coarse-pointer device, only when taller
## than wide), which events the cover swallows, and that a covering guard
## really eats a pointer event before a node that reads _input — the reason the
## guard moves itself to the end of the root.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/orientation_guard_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The guard's _ready never enables itself here (not the web build), so the
## swallow test builds the cover and calls _apply directly.

const GUARD_PATH := "res://scripts/orientation_guard.gd"

const TESTS := [
    "_test_text_size_scales_only_on_touch",
    "_test_js_flag_reads_every_yes_shape",
    "_test_cover_decision_matrix",
    "_test_pointer_event_classification",
    "_test_covering_guard_eats_taps_first",
    "_test_guard_stays_first_when_root_child_added",
]

var _guard_script: GDScript = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _guard_script = load(GUARD_PATH)
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[orientation_guard_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[orientation_guard_test] ALL PASS")
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


func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t), true)
        _check("harness — %s exists" % t, has_method(t), true)
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name), true)


## LLM-704: play-mode font sizes pass through text_size — unchanged off touch,
## times TOUCH_TEXT_SCALE (rounded) on a touch screen.
func _test_text_size_scales_only_on_touch() -> void:
    var guard: CanvasLayer = _guard_script.new()
    _check("desktop: 12 stays 12", guard.text_size(12), 12)
    _check("desktop: 13 stays 13", guard.text_size(13), 13)
    guard._touch_text = true
    _check("touch: 12 becomes 18", guard.text_size(12), 18)
    _check("touch: 13 rounds to 20", guard.text_size(13), 20)
    _check("touch: 52 (talk input height) becomes 78", guard.text_size(52), 78)
    _check("touch: speech bubble 14 becomes 17", guard.text_size(14, _guard_script.BUBBLE_TOUCH_TEXT_SCALE), 17)
    _check("touch: is_touch", guard.is_touch(), true)
    guard._touch_text = false
    _check("desktop: bubble scale ignored", guard.text_size(14, _guard_script.BUBBLE_TOUCH_TEXT_SCALE), 14)
    _check("desktop: not is_touch", guard.is_touch(), false)
    guard.free()
    _done()


## On Jeff's Alldocube the browser said yes to pointer:coarse but Godot received
## the int 1, so `== true` kept the guard off.
func _test_js_flag_reads_every_yes_shape() -> void:
    _check("bool true is yes", _guard_script.js_flag(true), true)
    _check("int 1 is yes (Android Chrome)", _guard_script.js_flag(1), true)
    _check("float 1.0 is yes", _guard_script.js_flag(1.0), true)
    _check("bool false is no", _guard_script.js_flag(false), false)
    _check("int 0 is no", _guard_script.js_flag(0), false)
    _check("null (eval failed) is no", _guard_script.js_flag(null), false)
    _check("string is no", _guard_script.js_flag("true"), false)
    _done()


func _test_cover_decision_matrix() -> void:
    _check("tablet portrait covers", _guard_script.should_cover(true, Vector2i(1200, 2000)), true)
    _check("tablet landscape does not cover", _guard_script.should_cover(true, Vector2i(2000, 1200)), false)
    _check("square tablet does not cover", _guard_script.should_cover(true, Vector2i(1500, 1500)), false)
    _check("narrow desktop window does not cover", _guard_script.should_cover(false, Vector2i(600, 900)), false)
    _check("desktop landscape does not cover", _guard_script.should_cover(false, Vector2i(1920, 1080)), false)
    _done()


func _test_pointer_event_classification() -> void:
    _check("mouse button swallowed", _guard_script.is_pointer_event(InputEventMouseButton.new()), true)
    _check("mouse motion swallowed", _guard_script.is_pointer_event(InputEventMouseMotion.new()), true)
    _check("screen touch swallowed", _guard_script.is_pointer_event(InputEventScreenTouch.new()), true)
    _check("screen drag swallowed", _guard_script.is_pointer_event(InputEventScreenDrag.new()), true)
    _check("pinch swallowed", _guard_script.is_pointer_event(InputEventMagnifyGesture.new()), true)
    _check("pan gesture swallowed", _guard_script.is_pointer_event(InputEventPanGesture.new()), true)
    _check("key passes", _guard_script.is_pointer_event(InputEventKey.new()), false)
    _done()


## The guard sits BEFORE the _input reader in the root, the way an autoload sits
## before the main scene. Covering must move it to the end so it eats the tap
## before the reader sees it; uncovering lets taps through again.
func _test_covering_guard_eats_taps_first() -> void:
    var guard: CanvasLayer = _guard_script.new()
    root.add_child(guard)
    var reader := _InputReader.new()
    root.add_child(reader)
    guard._build_cover()

    guard._apply(true)
    _check("covered: guard moved to the end of the root", guard.get_index(), root.get_child_count() - 1)
    _check("covered: cover shown", guard._cover.visible, true)
    root.push_input(_tap())
    _check("covered: reader never saw the tap", reader.seen, 0)

    guard._apply(false)
    _check("uncovered: cover hidden", guard._cover.visible, false)
    root.push_input(_tap())
    _check("uncovered: reader saw the tap", reader.seen, 1)

    guard.free()
    reader.free()
    _done()


## A root child added AFTER the cover went up (a scene change, a popup) must not
## get ahead of the guard: the guard moves back to the end and still eats the tap.
func _test_guard_stays_first_when_root_child_added() -> void:
    var guard: CanvasLayer = _guard_script.new()
    root.add_child(guard)
    guard._build_cover()
    guard._watch_root()
    guard._apply(true)

    var late := _InputReader.new()
    root.add_child(late)
    _check("guard moved back to the end of the root", guard.get_index(), root.get_child_count() - 1)
    root.push_input(_tap())
    _check("late reader never saw the tap", late.seen, 0)

    guard._apply(false)
    var later := _InputReader.new()
    root.add_child(later)
    _check("uncovered: guard stays put when a child is added", later.get_index(), root.get_child_count() - 1)

    guard.free()
    late.free()
    later.free()
    _done()


func _tap() -> InputEventMouseButton:
    var e := InputEventMouseButton.new()
    e.button_index = MOUSE_BUTTON_LEFT
    e.pressed = true
    e.position = Vector2(10, 10)
    return e


class _InputReader extends Node:
    var seen := 0

    func _input(event: InputEvent) -> void:
        if event is InputEventMouseButton:
            seen += 1
