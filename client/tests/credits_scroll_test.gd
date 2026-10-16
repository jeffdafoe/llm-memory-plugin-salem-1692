extends SceneTree

## Headless harness for the LLM-777 credits scroll
## (client/scripts/credits_scroll.gd) and the top-bar title that opens it.
##
## Covers: the rod art (transparent outside the rod, opaque across it, an
## outline on the body's edge, different grain per seed); every credit link is
## https; open() unrolls the paper from 0 to its full height and close() ends
## it; the scroll fits the 1280x720 design canvas on desktop; a link opens on
## the release of a tap but not after a drag or a scroll of the paper, and its
## press is left for the ScrollContainer; and a click on the title's text opens
## the scroll while a click in the empty bar beside it does not.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/credits_scroll_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_rod_image_shape",
    "_test_credit_links_are_https",
    "_test_open_unrolls_and_close_ends",
    "_test_fits_design_canvas",
    "_test_link_opens_on_tap_not_drag",
    "_test_title_text_opens_credits",
    "_test_every_freesound_sound_is_credited",
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
    _check("every credit is listed", links, 23)
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


func _button(pressed: bool, at: Vector2) -> InputEventMouseButton:
    var event := InputEventMouseButton.new()
    event.button_index = MOUSE_BUTTON_LEFT
    event.pressed = pressed
    event.position = at
    event.global_position = at
    return event


func _motion(at: Vector2) -> InputEventMouseMotion:
    var event := InputEventMouseMotion.new()
    event.position = at
    event.global_position = at
    return event


## On a touch screen the paper is mostly links, so a drag that starts on one
## must scroll, not open it. The paper is squeezed to force it to overflow.
func _test_link_opens_on_tap_not_drag() -> void:
    var scroll: CanvasLayer = _scroll_script().new()
    root.add_child(scroll)
    var opened := []
    scroll.open_url = func(url: String): opened.append(url)
    scroll.open()
    scroll.body_scroll.custom_minimum_size.y = 120.0
    scroll.body_scroll.size.y = 120.0
    await process_frame
    await process_frame
    var url := "https://kenney.nl/"
    var press := {}

    scroll._on_link_input(_button(true, Vector2(50, 50)), url, press)
    _check("the press opens nothing", opened, [])
    _check("the press is left for the paper to drag", root.is_input_handled(), false)
    scroll._on_link_input(_button(false, Vector2(53, 52)), url, press)
    _check("a tap opens the link on release", opened, [url])

    scroll._on_link_input(_button(true, Vector2(50, 50)), url, press)
    scroll._on_link_input(_button(false, Vector2(50, 90)), url, press)
    _check("a drag opens nothing", opened, [url])

    scroll._on_link_input(_button(true, Vector2(50, 50)), url, press)
    scroll.body_scroll.scroll_vertical = 40
    _check("the paper can scroll", scroll.body_scroll.scroll_vertical, 40)
    scroll._on_link_input(_button(false, Vector2(50, 50)), url, press)
    _check("a press the paper scrolled under opens nothing", opened, [url])

    scroll._on_link_input(_button(false, Vector2(50, 50)), url, press)
    _check("a release with no press opens nothing", opened, [url])

    # Out and back with the paper pinned at its top edge, so the scroll check
    # cannot catch it: only the sticky drag mark does.
    scroll.body_scroll.scroll_vertical = 0
    scroll._on_link_input(_button(true, Vector2(50, 50)), url, press)
    scroll._on_link_input(_motion(Vector2(50, 10)), url, press)
    scroll._on_link_input(_motion(Vector2(50, 50)), url, press)
    scroll._on_link_input(_button(false, Vector2(50, 50)), url, press)
    _check("a drag out and back opens nothing", opened, [url])

    scroll._on_link_input(_button(true, Vector2(50, 50)), url, press)
    scroll._on_link_input(_motion(Vector2(54, 52)), url, press)
    scroll._on_link_input(_button(false, Vector2(54, 52)), url, press)
    _check("a tap after a drag opens again", opened, [url, url])
    scroll.close()
    scroll.queue_free()
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


## Every Freesound recording the village plays (the Freesound section of
## assets/sound/CREDITS.txt: one line per file, `<files> — <id> "<title>" by
## <author>.`) has its author on the scroll, linked to that sound (LLM-20: the
## knock went into CREDITS.txt but not the scroll). A recording line in any
## other shape fails, so a sound cannot slip past by being written differently.
func _test_every_freesound_sound_is_credited() -> void:
    var text := FileAccess.get_file_as_string("res://assets/sound/CREDITS.txt")
    var start := text.find("Freesound (freesound.org)")
    _check("CREDITS.txt has its Freesound section", start >= 0, true)
    var shape := RegEx.new()
    shape.compile("— ([0-9]+) \"[^\"]+\" by ([^.]+)\\.")
    # Each scroll link and the name shown on it.
    var shown := {}
    for section in _scroll_script().CREDITS:
        for entry in section[1]:
            for line in entry[2]:
                for work in line:
                    shown[str(work[1])] = str(work[0])
    var found := 0
    for line in text.substr(maxi(start, 0)).split("\n"):
        # A recording line sits two spaces in and names its .ogg files before
        # the dash; the lines indented further under it say how it was cut.
        var dash := line.find("—")
        if not line.begins_with("  ") or line.begins_with("   ") or dash < 0 or not line.substr(0, dash).contains(".ogg"):
            continue
        found += 1
        var m := shape.search(line)
        _check("credits line has the <id> \"title\" by author shape: %s" % line.strip_edges(), m != null, true)
        if m == null:
            continue
        var url := "https://freesound.org/s/%s/" % m.get_string(1)
        _check("Freesound %s is on the scroll as %s" % [m.get_string(1), m.get_string(2)], shown.get(url, ""), m.get_string(2))
    _check("the Freesound sounds were read", found >= 5, true)
    _done()
