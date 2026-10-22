extends SceneTree

## Headless harness for LLM-803: a click that dismisses a toast banner does not
## also walk the player.
##
## main.gd._input arms the walk on a left press before the GUI sees it, so it
## must skip any point the camera calls UI. main.gd registers Toast.ui_panel()
## with the camera for that; this harness does the same registration, then
## drives main._input with presses on and off a banner.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/toast_walk_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The Toast autoload is in the tree. main.gd and camera.gd are made with .new()
## off-tree, so neither _ready runs; _input only reads the fields set here.

const TESTS := [
    "_test_no_banner_walks",
    "_test_banner_click_does_not_walk",
    "_test_dismissed_banner_walks_again",
]

var _toast: Node = null
var _camera: Camera2D = null
var _main: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _toast = root.get_node("Toast")
    _camera = load("res://scripts/camera.gd").new()
    _camera.register_ui_panel(_toast.ui_panel())
    _main = load("res://scripts/main.gd").new()
    _main.camera = _camera
    _main._pc_exists = true
    _check_test_list()
    for t in TESTS:
        _current = t
        await call(t)
    _check_all_tests_ran()
    _main.free()
    _camera.free()
    print("\n[toast_walk_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[toast_walk_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


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


## Whether a left press at pos arms the walk. Clears the flag after.
func _press_walks(pos: Vector2) -> bool:
    var press := InputEventMouseButton.new()
    press.button_index = MOUSE_BUTTON_LEFT
    press.pressed = true
    press.position = pos
    _main._pc_walk_pending = false
    _main._input(press)
    var armed: bool = _main._pc_walk_pending
    _main._pc_walk_pending = false
    return armed


func _banner() -> Control:
    var stack: Control = _toast.ui_panel()
    if stack.get_child_count() == 0:
        return null
    return stack.get_child(0)


## Where the first banner of the column sits, top centre, once laid out.
func _banner_point() -> Vector2:
    var size := root.get_visible_rect().size
    return Vector2(size.x / 2.0, _toast.ui_panel().offset_top + 10.0)


func _test_no_banner_walks() -> void:
    _check("no banner up: the column is empty", _banner() == null, true)
    _check("no banner up: a press where one would be walks", _press_walks(_banner_point()), true)
    _done()


func _test_banner_click_does_not_walk() -> void:
    _toast.notice("Walk up to your tent first.")
    await process_frame
    await process_frame
    var banner := _banner()
    _check("the banner is up", banner != null, true)
    if banner == null:
        _done()
        return
    var rect := banner.get_global_rect()
    _check("the banner is laid out at full width", rect.size.x >= _toast.TOAST_WIDTH, true)
    _check("a press on the banner's centre does not walk", _press_walks(rect.get_center()), false)
    _check("a press on the banner's top line does not walk", _press_walks(_banner_point()), false)
    _check("a press below the banner walks", _press_walks(Vector2(rect.get_center().x, rect.end.y + 40.0)), true)
    _check("a press beside the banner walks", _press_walks(Vector2(rect.end.x + 40.0, rect.get_center().y)), true)
    _done()


func _test_dismissed_banner_walks_again() -> void:
    var banner := _banner()
    if banner != null:
        _toast._dismiss(banner)
    await process_frame
    await process_frame
    _check("dismissed: the column is empty", _banner() == null, true)
    _check("dismissed: a press where it was walks", _press_walks(_banner_point()), true)
    _done()
