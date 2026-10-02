extends SceneTree

## Headless harness for the LLM-705 top bar on a touch screen
## (client/scripts/top_bar.gd): bigger text, a taller bar, no editor buttons,
## and a row that still fits the 1280-wide design.
##
## Covers: the bar is 40 tall on desktop and 60 on touch; the camera's
## top-bar tap-blocking strip follows it; Edit and Config never show on touch
## even when an admin turns them on; and, with realistic values filled in, the
## touch row's minimum width stays inside the 1280 design width (measured
## 1179 when the ticket was written, 1381 with the admin buttons and an
## unclipped title) — and still does when the row is crowded, because the
## title clips.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/top_bar_touch_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The OrientationGuard autoload is in the tree; each test flips its touch
## flag before building the bar (the bar reads it in _ready).

const DESIGN_WIDTH := 1280.0

const TESTS := [
    "_test_desktop_bar_unchanged",
    "_test_touch_bar_is_taller",
    "_test_touch_hides_editor_buttons",
    "_test_touch_row_fits_design_width",
]

var _guard: Node = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _guard = root.get_node("OrientationGuard")
    _check_test_list()
    for t in TESTS:
        _current = t
        await call(t)
    _guard._touch_text = false
    _check_all_tests_ran()
    print("\n[top_bar_touch_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[top_bar_touch_test] ALL PASS")
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


func _make_bar(touch: bool) -> Control:
    _guard._touch_text = touch
    var bar: Control = load("res://scripts/top_bar.gd").new()
    root.add_child(bar)
    return bar


func _test_desktop_bar_unchanged() -> void:
    var bar := _make_bar(false)
    _check("desktop bar min height 40", bar.custom_minimum_size.y, 40.0)
    _check("desktop bar bottom at 40", bar.offset_bottom, 40.0)
    bar.set_edit_visible(true)
    bar.set_config_visible(true)
    _check("desktop admin sees Edit", bar.edit_button.visible, true)
    _check("desktop admin sees Config", bar.config_button.visible, true)
    var cam: Camera2D = load("res://scripts/camera.gd").new()
    _check("desktop camera blocks taps above 40", cam._top_bar_height(), 40.0)
    cam.free()
    bar.free()
    _done()


func _test_touch_bar_is_taller() -> void:
    var bar := _make_bar(true)
    _check("touch bar min height 60", bar.custom_minimum_size.y, 60.0)
    _check("touch bar bottom at 60", bar.offset_bottom, 60.0)
    var cam: Camera2D = load("res://scripts/camera.gd").new()
    _check("touch camera blocks taps above 60", cam._top_bar_height(), 60.0)
    _check("tap at y=50 is on the touch bar", cam._is_over_ui(Vector2(400, 50)), true)
    cam.free()
    bar.free()
    _done()


func _test_touch_hides_editor_buttons() -> void:
    var bar := _make_bar(true)
    bar.set_edit_visible(true)
    bar.set_config_visible(true)
    _check("touch admin: Edit hidden", bar.edit_button.visible, false)
    _check("touch admin: Config hidden", bar.config_button.visible, false)
    bar.free()
    _done()


## The values the ticket's measurement used, with the admin buttons requested.
func _test_touch_row_fits_design_width() -> void:
    var bar := _make_bar(true)
    bar.set_username("jeff")
    bar.set_character_name("Thomas Prescott")
    bar.set_purse(1234, [{"kind": "bread", "qty": 2}])
    bar.set_lodging("the Blue Anchor Inn", "through tonight")
    bar.set_needs({"hunger": 14, "thirst": 9, "tiredness": 20})
    bar.set_edit_visible(true)
    bar.set_config_visible(true)
    await process_frame
    var width: float = bar.get_combined_minimum_size().x
    _check("touch row fits the 1280 design width", width <= DESIGN_WIDTH, true)
    if width > DESIGN_WIDTH:
        printerr("       touch row min width %.0f" % width)
    # Crowd the row past what its items need: the title must give way.
    bar.edit_button.visible = true
    bar.config_button.visible = true
    await process_frame
    var crowded: float = bar.get_combined_minimum_size().x
    _check("crowded touch row still fits (title clips)", crowded <= DESIGN_WIDTH, true)
    if crowded > DESIGN_WIDTH:
        printerr("       crowded touch row min width %.0f" % crowded)
    bar.free()
    _done()
