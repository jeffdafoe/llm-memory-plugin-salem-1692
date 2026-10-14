extends SceneTree

## Headless harness for the LLM-777 credits scroll
## (client/scripts/credits_scroll.gd) and the top-bar title that opens it.
##
## Covers: the rod art (transparent outside the rod, opaque across it, an
## outline on the body's edge, different grain per seed); every credit link is
## https; open() unrolls the paper from 0 to its full height and close() ends
## it; the scroll fits the 1280x720 design canvas on desktop; and a click on
## the title's text opens the scroll while a click in the empty bar beside it
## does not.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/credits_scroll_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_rod_image_shape",
    "_test_credit_links_are_https",
    "_test_open_unrolls_and_close_ends",
    "_test_fits_design_canvas",
    "_test_title_text_opens_credits",
]

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    root.get_node("OrientationGuard")._touch_text = false
    _check_test_list()
    for t in TESTS:
        _current = t
        await call(t)
    _check_all_tests_ran()
    print("\n[credits_scroll_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[credits_scroll_test] ALL PASS")
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


func _scroll_script() -> GDScript:
    return load("res://scripts/credits_scroll.gd")


func _test_rod_image_shape() -> void:
    var script := _scroll_script()
    var image: Image = script.rod_image(234, 16, 1692)
    _check("rod width", image.get_width(), 234)
    _check("rod height", image.get_height(), 16)
    _check("corner outside the knob is clear", image.get_pixel(0, 0).a, 0.0)
    _check("above the body is clear", image.get_pixel(117, 0).a, 0.0)
    _check("knob centre is solid", image.get_pixel(8, 8).a, 1.0)
    _check("collar is solid", image.get_pixel(17, 8).a, 1.0)
    _check("body middle is solid", image.get_pixel(117, 8).a, 1.0)
    # Within one step of 8-bit storage, which truncates.
    var edge: Color = image.get_pixel(60, 3)
    var outline: Color = script.COLOR_WOOD_OUTLINE
    var off := maxf(maxf(absf(edge.r - outline.r), absf(edge.g - outline.g)), absf(edge.b - outline.b))
    _check("body top edge is the outline", off < 1.5 / 255.0, true)
    var other: Image = script.rod_image(234, 16, 1693)
    _check("a second seed grains differently", image.get_data() != other.get_data(), true)
    _done()


func _test_credit_links_are_https() -> void:
    var links := 0
    for section in _scroll_script().CREDITS:
        for entry in section[1]:
            _check("%s link is https" % entry[0], str(entry[1]).begins_with("https://"), true)
            links += 1
            for line in entry[2]:
                _check("%s work line is not empty" % entry[0], line.size() > 0, true)
                for work in line:
                    _check("%s link is https" % work[0], str(work[1]).begins_with("https://"), true)
                    links += 1
    _check("every credit is listed", links, 19)
    _done()


func _test_open_unrolls_and_close_ends() -> void:
    var scroll: CanvasLayer = _scroll_script().new()
    root.add_child(scroll)
    var events := []
    scroll.opened.connect(func(): events.append("opened"))
    scroll.closed.connect(func(): events.append("closed"))
    _check("starts hidden", scroll.visible, false)
    scroll.open()
    _check("open shows it", scroll.visible, true)
    _check("unroll starts flat", scroll.clipper.custom_minimum_size.y, 0.0)
    _check("paper has height", scroll._paper_height > 100.0, true)
    scroll.open()
    _check("open while open is a no-op", events, ["opened"])
    await create_timer(scroll.UNROLL_SEC + 0.2).timeout
    _check("unrolled to the paper's height", scroll.clipper.custom_minimum_size.y, scroll._paper_height)
    scroll.close()
    scroll.close()
    _check("close hides it", scroll.visible, false)
    _check("close is idempotent", events, ["opened", "closed"])
    scroll.queue_free()
    await process_frame
    _done()


## The design canvas is 1280x720 (canvas_items / expand; a wider window only
## widens it), so on desktop every credit must show without scrolling, with
## the viewport margin to spare. A headless window is not 720 tall on its own.
func _test_fits_design_canvas() -> void:
    var old_size := root.size
    root.size = Vector2i(1280, 720)
    await process_frame
    var scroll: CanvasLayer = _scroll_script().new()
    root.add_child(scroll)
    scroll.open()
    await create_timer(scroll.UNROLL_SEC + 0.2).timeout
    var column: Control = scroll.clipper.get_parent()
    var view: Vector2 = scroll.root.get_viewport_rect().size
    var content_height: float = scroll.content.get_combined_minimum_size().y
    print("[credits_scroll_test] view %s, scroll %s, content %s, room for text %s" % [
        view, column.size, content_height, scroll.body_scroll.size.y])
    _check("canvas is the design height", view.y, 720.0)
    _check("every credit shows without scrolling", content_height <= scroll.body_scroll.size.y, true)
    _check("scroll fits the canvas height", column.size.y <= view.y - 2.0 * scroll.VIEWPORT_MARGIN, true)
    _check("scroll fits the canvas width", column.size.x <= view.x, true)
    _check("rods are pixel art at an integer scale", scroll.top_rod.size, Vector2(scroll._rod_width_px, scroll.ROD_HEIGHT_PX) * scroll.ROD_PIXEL)
    _check("paper sits between the rods", scroll.clipper.size.x, scroll.paper.size.x)
    scroll.close()
    scroll.queue_free()
    root.size = old_size
    await process_frame
    _done()


func _test_title_text_opens_credits() -> void:
    var bar: Control = load("res://scripts/top_bar.gd").new()
    root.add_child(bar)
    await process_frame
    var pressed := [0]
    bar.title_pressed.connect(func(): pressed[0] += 1)
    var click := InputEventMouseButton.new()
    click.button_index = MOUSE_BUTTON_LEFT
    click.pressed = true
    click.position = Vector2(10, 10)
    bar._on_title_input(click)
    _check("a click on the title text opens the credits", pressed[0], 1)
    _check("the title shows a hand over its text", bar.title_label.mouse_default_cursor_shape, Control.CURSOR_POINTING_HAND)
    click.position = Vector2(bar.title_label.size.x - 2, 10)
    var beside := InputEventMouseMotion.new()
    beside.position = click.position
    bar._on_title_input(beside)
    _check("no hand in the empty bar beside the title", bar.title_label.mouse_default_cursor_shape, Control.CURSOR_ARROW)
    bar._on_title_input(click)
    _check("a click beside the title does nothing", pressed[0], 1)
    bar.free()
    _done()
