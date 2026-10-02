extends SceneTree

## Headless harness for the LLM-704 two-finger pinch (client/scripts/camera.gd).
## Godot's web build delivers a touch pinch as two InputEventScreenDrag
## streams, never as InputEventMagnifyGesture, so the camera tracks fingers by
## index and zooms by the change in their span.
##
## Covers: spreading two fingers zooms in by the span ratio and pinching them
## zooms out; the zoom clamps to ZOOM_MAX; a pinch does not pan; one finger
## still pans; and touch_gesture_was_pinch — the flag main.gd reads so a finger
## from a pinch never walks the player — is set by the second finger and
## cleared by the next first finger.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/camera_pinch_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The camera runs in the tree (zoom math reads the viewport size); every
## event is pushed through the root viewport, as real input would be.

const TESTS := [
    "_test_spread_zooms_in_by_span_ratio",
    "_test_pinch_zooms_out",
    "_test_zoom_clamps_to_max",
    "_test_second_finger_marks_pinch_until_next_gesture",
    "_test_one_finger_still_pans",
    "_test_finger_lifted_under_modal_is_forgotten",
    "_test_third_finger_keeps_first_pair",
]

var _camera: Camera2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[camera_pinch_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[camera_pinch_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _check_near(label: String, got: float, want: float) -> void:
    _check(label, absf(got - want) < 0.001, true)
    if absf(got - want) >= 0.001:
        printerr("       %s: got %f, want %f" % [label, got, want])


func _run_all() -> void:
    for t in TESTS:
        _current = t
        _fresh_camera()
        call(t)
        _camera.free()


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


## A camera at zoom 1, centred on the map, with the ZOOM_MIN floor low enough
## that a pinch-out has room.
func _fresh_camera() -> void:
    _camera = load("res://scripts/camera.gd").new()
    root.add_child(_camera)
    _camera.ZOOM_MIN = 0.3
    _camera.zoom = Vector2.ONE
    _camera.position = _camera.map_bounds.get_center()
    _camera._clamp_position()


func _touch(index: int, pos: Vector2, pressed: bool) -> void:
    var e := InputEventScreenTouch.new()
    e.index = index
    e.position = pos
    e.pressed = pressed
    root.push_input(e)


func _drag(index: int, from: Vector2, to: Vector2) -> void:
    var e := InputEventScreenDrag.new()
    e.index = index
    e.position = to
    e.relative = to - from
    root.push_input(e)


func _test_spread_zooms_in_by_span_ratio() -> void:
    _touch(0, Vector2(300, 300), true)
    _touch(1, Vector2(400, 300), true)
    _drag(1, Vector2(400, 300), Vector2(450, 300))  # span 100 -> 150
    _check_near("span x1.5 zooms to 1.5", _camera.zoom.x, 1.5)
    _done()


func _test_pinch_zooms_out() -> void:
    _touch(0, Vector2(300, 300), true)
    _touch(1, Vector2(500, 300), true)
    _drag(1, Vector2(500, 300), Vector2(400, 300))  # span 200 -> 100
    _check_near("span x0.5 zooms to 0.5", _camera.zoom.x, 0.5)
    _done()


func _test_zoom_clamps_to_max() -> void:
    _touch(0, Vector2(300, 300), true)
    _touch(1, Vector2(310, 300), true)
    _drag(1, Vector2(310, 300), Vector2(700, 300))  # span 10 -> 400
    _check_near("zoom stops at ZOOM_MAX", _camera.zoom.x, _camera.ZOOM_MAX)
    _done()


## The flag main.gd reads: false for a plain tap, true once a second finger
## lands (and stays true through the release), false again at the next tap.
func _test_second_finger_marks_pinch_until_next_gesture() -> void:
    _touch(0, Vector2(300, 300), true)
    _check("one finger is not a pinch", _camera.touch_gesture_was_pinch, false)
    _touch(1, Vector2(400, 300), true)
    _check("second finger marks a pinch", _camera.touch_gesture_was_pinch, true)
    _touch(1, Vector2(400, 300), false)
    _touch(0, Vector2(300, 300), false)
    _check("still a pinch when the last finger lifts", _camera.touch_gesture_was_pinch, true)
    _touch(0, Vector2(300, 300), true)
    _check("next first finger clears it", _camera.touch_gesture_was_pinch, false)
    _done()


## push_input maps event coordinates from the window into the root viewport,
## and the headless window is far smaller than the viewport (a 20 px drag
## arrives as 400), so the expected pan is read off the same transform.
## Distance ratios — all the pinch tests use — are unaffected by it.
func _test_one_finger_still_pans() -> void:
    var start: Vector2 = _camera.position
    var window_to_viewport: float = root.get_final_transform().affine_inverse().basis_xform(Vector2(1, 0)).x
    _touch(0, Vector2(300, 300), true)
    _drag(0, Vector2(300, 300), Vector2(280, 300))
    _check_near("one-finger drag pans right by the drag distance", _camera.position.x - start.x, 20.0 * window_to_viewport)
    _check("one-finger drag leaves zoom alone", _camera.zoom, Vector2.ONE)
    _done()


## code_review (LLM-704): a finger lifted while a modal is open must be
## forgotten, or the next one-finger drag reads as a pinch and never pans.
func _test_finger_lifted_under_modal_is_forgotten() -> void:
    var window_to_viewport: float = root.get_final_transform().affine_inverse().basis_xform(Vector2(1, 0)).x
    _touch(0, Vector2(300, 300), true)
    _camera.modal_open = true
    _touch(0, Vector2(300, 300), false)
    _camera.modal_open = false
    _check("no finger left recorded", _camera._touches.size(), 0)
    var start: Vector2 = _camera.position
    _touch(1, Vector2(300, 300), true)
    _drag(1, Vector2(300, 300), Vector2(280, 300))
    _check_near("next one-finger drag pans", _camera.position.x - start.x, 20.0 * window_to_viewport)
    _check("next one-finger drag leaves zoom alone", _camera.zoom, Vector2.ONE)
    _done()


## A third finger joins without changing the pinch pair (the first two
## fingers down); moving it alone does not zoom.
func _test_third_finger_keeps_first_pair() -> void:
    _touch(0, Vector2(300, 300), true)
    _touch(1, Vector2(400, 300), true)
    _touch(2, Vector2(350, 500), true)
    _drag(2, Vector2(350, 500), Vector2(350, 600))
    _check_near("third finger alone does not zoom", _camera.zoom.x, 1.0)
    _drag(1, Vector2(400, 300), Vector2(500, 300))  # pair span 100 -> 200
    _check_near("first pair still drives the zoom", _camera.zoom.x, 2.0)
    _done()
