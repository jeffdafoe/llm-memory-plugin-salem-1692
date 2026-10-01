extends SceneTree

## Headless regression harness for the speech bubble's screen-space sizing.
##
## What this file exists to catch:
##
##  1. Zoom leaking into the text. The bubble hangs off a villager in the
##     world, so the camera zoom (0.3–3.0, almost never whole) scales it
##     unless it cancels that scale itself. Scaled, the glyphs are resampled
##     through the project's nearest filter and the strokes break up — the
##     "hard to read" bubble. At every zoom the plain bubble must sit at one
##     local unit per screen pixel.
##
##  2. Pixel art at a whole scale. With the Mana Seed bubble and font, one
##     art pixel must land on a WHOLE number of screen pixels at every zoom
##     and window stretch, or the pixel font's strokes come out uneven.
##
##  3. The anchor stays in the world. Only the bubble's size is
##     screen-space; its tail must still point at the same spot above the
##     villager's head whatever the zoom.
##
##  4. Degrading without the art. The pack is purchased and gitignored
##     (client/.gitignore), so CI has none: no art must mean the plain
##     bubble, never an error. The art path is exercised with a synthetic
##     sheet and the fallback font, so it is checked the same way with and
##     without the art on disk.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/speech_bubble_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The camera is stood in for by the root viewport's canvas_transform —
## what Camera2D writes — so each zoom applies synchronously.

const TESTS := [
    "_test_plain_bubble_cancels_the_zoom",
    "_test_art_bubble_lands_on_whole_pixels",
    "_test_anchor_stays_above_the_head",
    "_test_no_art_means_the_plain_bubble",
    "_test_art_bubble_grows_to_fit_the_text",
]

const ZOOMS := [0.3, 0.5, 0.7, 1.0, 1.3, 2.0, 3.0]
const LONG_TEXT := "Good morning, neighbor. The well by the Tavern is broken again, and the town is paying twelve coins to mend it."

var _script: GDScript = null
var _actor: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

func _initialize() -> void:
    _script = load("res://scripts/speech_bubble.gd")
    _actor = Node2D.new()
    _actor.position = Vector2(300, 400)
    root.add_child(_actor)

func _process(_delta: float) -> bool:
    _check("harness — actor entered the tree", _actor.is_inside_tree())
    _check_test_list()
    _run_all()
    _set_art(null, null)
    root.canvas_transform = Transform2D.IDENTITY
    _actor.queue_free()
    _check_all_tests_ran()
    print("\n[speech_bubble_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[speech_bubble_test] ALL PASS")
    quit(1 if _failures > 0 else 0)
    return true

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

func _done() -> void:
    _completed[_current] = true

func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t))

func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t))
        _check("harness — %s exists" % t, has_method(t))
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name))

# --- fixtures --------------------------------------------------------------------

## Sets the bubble's shared art cache directly and marks it loaded, so the
## loader does not look at the disk. null, null = the artless checkout.
func _set_art(sheet: Texture2D, font: Font) -> void:
    _script._art_sheet = sheet
    _script._art_font = font
    _script._art_checked = true

func _synthetic_sheet() -> ImageTexture:
    var img := Image.create(64, 48, false, Image.FORMAT_RGBA8)
    return ImageTexture.create_from_image(img)

func _spawn(text: String) -> Node2D:
    var bubble: Node2D = _script.new()
    _actor.add_child(bubble)
    bubble.setup(text)
    return bubble

func _zoom(z: float) -> void:
    root.canvas_transform = Transform2D(0.0, Vector2(z, z), 0.0, Vector2(640, 360))

## Screen pixels per local unit: the canvas transform (zoom) times the
## viewport's stretch to the window.
func _on_screen(bubble: Node2D) -> Vector2:
    return bubble.get_global_transform_with_canvas().get_scale() * root.get_final_transform().get_scale()

func _is_whole(v: float) -> bool:
    return v >= 1.0 and is_equal_approx(v, roundf(v))

# --- assertions ------------------------------------------------------------------

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)

# --- tests -----------------------------------------------------------------------

func _test_plain_bubble_cancels_the_zoom() -> void:
    _set_art(null, null)
    var bubble: Node2D = _spawn(LONG_TEXT)
    for z in ZOOMS:
        _zoom(z)
        bubble._fit_to_screen()
        var s: Vector2 = bubble.get_global_transform_with_canvas().get_scale()
        _check("plain bubble draws one unit per canvas pixel at zoom %.1f (got %s)" % [z, s],
            is_equal_approx(s.x, 1.0) and is_equal_approx(s.y, 1.0))
    bubble.free()
    _done()

## The headless window is 64x64 against the 1280x720 base, so the canvas
## stretch is 0.05; content_scale_factor multiplies it, which sets the
## stretch to 1.0 (the base size) and 1.5 (a 1920-wide browser window).
func _test_art_bubble_lands_on_whole_pixels() -> void:
    _set_art(_synthetic_sheet(), ThemeDB.fallback_font)
    var bubble: Node2D = _spawn(LONG_TEXT)
    _check("the art path is in use with a sheet and a font", bubble._use_art)
    var base_stretch: float = root.get_final_transform().get_scale().x
    var saved_factor: float = root.content_scale_factor
    for case in [[1.0, _script.ART_PIXEL_SCALE], [1.5, 3]]:
        root.content_scale_factor = case[0] / base_stretch
        var stretch: float = root.get_final_transform().get_scale().x
        _check("harness — stretch set to %.1f (got %.3f)" % [case[0], stretch],
            is_equal_approx(stretch, case[0]))
        for z in ZOOMS:
            _zoom(z)
            bubble._fit_to_screen()
            var s: Vector2 = _on_screen(bubble)
            _check("one art pixel is a whole number of screen pixels at zoom %.1f, stretch %.1f (got %s)" % [z, case[0], s],
                _is_whole(s.x) and _is_whole(s.y))
            _check("art pixels are square at zoom %.1f, stretch %.1f" % [z, case[0]],
                is_equal_approx(s.x, s.y))
            _check("art pixels are %d screen pixels at zoom %.1f, stretch %.1f" % [case[1], z, case[0]],
                is_equal_approx(s.x, float(case[1])))
    root.content_scale_factor = saved_factor
    bubble.free()
    _done()

func _test_anchor_stays_above_the_head() -> void:
    for art in [false, true]:
        if art:
            _set_art(_synthetic_sheet(), ThemeDB.fallback_font)
        else:
            _set_art(null, null)
        var bubble: Node2D = _spawn("Aye.")
        for z in ZOOMS:
            _zoom(z)
            bubble._fit_to_screen()
            _check("tail anchor is ANCHOR_Y world px above the villager at zoom %.1f (art %s)" % [z, art],
                bubble.position == Vector2(0, _script.ANCHOR_Y))
        bubble.free()
    _done()

func _test_no_art_means_the_plain_bubble() -> void:
    _set_art(null, ThemeDB.fallback_font)
    var bubble: Node2D = _spawn("Aye.")
    _check("a font without the sheet falls back to the plain bubble", not bubble._use_art)
    bubble.free()
    _set_art(_synthetic_sheet(), null)
    bubble = _spawn("Aye.")
    _check("a sheet without the font falls back to the plain bubble", not bubble._use_art)
    _check("the plain bubble wraps in the fallback font size", bubble._font_size == _script.FONT_SIZE)
    bubble.free()
    _done()

func _test_art_bubble_grows_to_fit_the_text() -> void:
    _set_art(_synthetic_sheet(), ThemeDB.fallback_font)
    var short: Node2D = _spawn("Aye.")
    var long: Node2D = _spawn(LONG_TEXT)
    var room: Vector2 = _script.ART_TEXT_ROOM
    _check("a long line wraps to more than one line", long._wrapped_lines.size() > 1)
    _check("no wrapped line is wider than the wrap width",
        long._content_size.x <= _script.ART_MAX_TEXT_WIDTH)
    for b in [short, long]:
        var fits: Vector2 = room + b._art_grow()
        _check("the bubble fill holds the text (%s)" % b._wrapped_lines[0],
            fits.x >= b._content_size.x and fits.y >= b._content_size.y)
    _check("a short line keeps the sheet's own size", short._art_grow().x == 0.0)
    short.free()
    long.free()
    _done()
