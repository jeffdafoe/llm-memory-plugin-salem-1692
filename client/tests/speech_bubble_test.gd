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
##  2. Pixel art at a whole scale, text at UI size. With the Mana Seed
##     bubble, one art pixel must land on a WHOLE number of screen pixels at
##     every zoom and window stretch, or its outline comes out uneven; the
##     text inside it is a smooth font on its own layer at the UI's size.
##
##  3. The anchor stays in the world. Only the bubble's size is
##     screen-space; its tail must still point at the same spot above the
##     villager's head whatever the zoom.
##
##  4. Degrading without the art. The pack is purchased and gitignored
##     (client/.gitignore), so CI has none: no art must mean the plain
##     bubble, never an error. The art path is exercised with a synthetic
##     sheet, so it is checked the same way with and without the art on
##     disk.
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
    "_test_plain_bubble_keeps_the_window_stretch",
    "_test_art_bubble_lands_on_whole_pixels",
    "_test_anchor_stays_above_the_head",
    "_test_no_art_means_the_plain_bubble",
    "_test_art_bubble_grows_to_fit_the_text",
    "_test_live_camera_zoom_is_cancelled_the_same_frame",
    "_test_long_speech_shows_a_page_at_a_time",
    "_test_blank_speech_draws_no_bubble",
    "_test_art_text_is_ui_sized_and_smooth",
    "_test_a_speaker_has_one_bubble",
    "_test_side_by_side_speakers_stack_apart",
    "_test_far_apart_speakers_keep_their_place",
    "_test_a_stack_holds_three",
    "_test_a_lift_eases_into_place",
    "_test_a_wide_bubble_joins_two_stacks",
    "_test_touch_text_is_bigger_and_still_fits",
]
## One village tile, in world px.
const TILE := 32.0

const ZOOMS := [0.3, 0.5, 0.7, 1.0, 1.3, 2.0, 3.0]
const LONG_TEXT := "Good morning, neighbor. The well by the Tavern is broken again, and the town is paying twelve coins to mend it."
## A merchant's haggle as seen live (2026-10-01): ten lines at the old wrap.
const HAGGLE_TEXT := "Twenty-six and twelve, thirty-eight coins all told. I can manage that if you'll take thirty-seven and a sack of flour or two wedges of cheese to make up the difference. Or if you'd rather have goods than haggle over the last coin, I've flour, wheat, cheese, and a fine silver locket that would fetch a pretty penny in Boston. What say you to that? And mind, the roads to Boston are long this time of year, so I'd as soon settle it here and now."
## Zoom steps the live-camera test walks, one per frame.
const LIVE_ZOOMS := [0.7, 1.3, 0.5, 2.0]

var _script: GDScript = null
var _actor: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""
var _frame := 0

# Live-camera test state, stepped one frame at a time from _process.
var _camera: Camera2D = null
var _zoomer: Zoomer = null
var _live_bubble: Node2D = null
var _live_step := -1

## Sets the camera zoom from inside node processing at the default
## priority, the way the client's camera.gd does on a scroll — so a bubble
## that fitted itself BEFORE the camera moved would be caught one zoom stale.
class Zoomer:
    extends Node
    var camera: Camera2D = null
    var pending := 0.0
    func _process(_delta: float) -> void:
        if pending > 0.0:
            camera.zoom = Vector2(pending, pending)
            pending = 0.0

func _initialize() -> void:
    _script = load("res://scripts/speech_bubble.gd")
    _actor = Node2D.new()
    _actor.position = Vector2(300, 400)
    root.add_child(_actor)

## SceneTree._process runs before the nodes' own processing each frame, so
## a zoom scheduled here lands in the Zoomer this frame, and its effect on
## the bubble is read at the start of the next.
func _process(_delta: float) -> bool:
    _frame += 1
    if _frame == 1:
        _check("harness — actor entered the tree", _actor.is_inside_tree())
        _check_test_list()
        _run_all()
        return false
    if _live_step >= 0 and _live_step <= LIVE_ZOOMS.size():
        _step_live_camera()
        return false
    _set_art(null)
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
## loader does not look at the disk. null = the artless checkout.
func _set_art(sheet: Texture2D) -> void:
    _script._art_sheet = sheet
    _script._art_checked = true

## Sets the window stretch (see _test_art_bubble_lands_on_whole_pixels) and
## returns the factor to restore.
func _set_stretch(stretch: float) -> float:
    var saved: float = root.content_scale_factor
    var base: float = root.get_final_transform().get_scale().x / saved
    root.content_scale_factor = stretch / base
    return saved

func _synthetic_sheet() -> ImageTexture:
    var img := Image.create(64, 48, false, Image.FORMAT_RGBA8)
    return ImageTexture.create_from_image(img)

func _spawn(text: String) -> Node2D:
    var bubble: Node2D = _script.new()
    _actor.add_child(bubble)
    bubble.setup(text)
    return bubble

## Speakers in a row, TILE apart, far from the shared _actor so the bubbles
## of other tests never meet them.
func _speakers(count: int, gap: float = TILE) -> Array:
    var out := []
    for i in count:
        var n := Node2D.new()
        n.position = Vector2(-6000 + i * gap, -6000)
        root.add_child(n)
        out.append(n)
    return out

func _say(speaker: Node2D, text: String) -> Node2D:
    var bubble: Node2D = _script.new()
    speaker.add_child(bubble)
    bubble.setup(text)
    return bubble

## Lays the bubbles out settled (a long delta ends the easing), then fits
## each the way its own _process does.
func _settle(bubbles: Array) -> void:
    _script._layout(bubbles, 10.0)
    for b in bubbles:
        if not b.is_queued_for_deletion():
            b._fit_to_screen()

## Where the bubble draws now, lift included, in canvas px.
func _drawn_rect(bubble: Node2D) -> Rect2:
    var t: Transform2D = bubble.get_global_transform_with_canvas()
    var r: Rect2 = bubble._local_rect()
    return Rect2(t * r.position, r.size * t.get_scale())

func _free_all(nodes: Array) -> void:
    for n in nodes:
        n.free()

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
    _set_art(null)
    var bubble: Node2D = _spawn(LONG_TEXT)
    for z in ZOOMS:
        _zoom(z)
        bubble._fit_to_screen()
        var s: Vector2 = bubble.get_global_transform_with_canvas().get_scale()
        _check("plain bubble draws one unit per canvas pixel at zoom %.1f (got %s)" % [z, s],
            is_equal_approx(s.x, 1.0) and is_equal_approx(s.y, 1.0))
    bubble.free()
    _done()

## The plain bubble keeps the window stretch like the rest of the UI (its
## smooth font is rasterized at that size), so on screen it is exactly the
## stretch, whatever the zoom.
func _test_plain_bubble_keeps_the_window_stretch() -> void:
    _set_art(null)
    var bubble: Node2D = _spawn(LONG_TEXT)
    var stretch: Vector2 = root.get_final_transform().get_scale()
    for z in ZOOMS:
        _zoom(z)
        bubble._fit_to_screen()
        var s: Vector2 = _on_screen(bubble)
        _check("plain bubble is the window stretch on screen at zoom %.1f (got %s, stretch %s)" % [z, s, stretch],
            s.is_equal_approx(stretch))
    bubble.free()
    _done()

## The headless window is 64x64 against the 1280x720 base, so the canvas
## stretch is 0.05; content_scale_factor multiplies it, which sets the
## stretch to 1.0 (the base size) and 1.5 (a 1920-wide browser window).
## The art stays ART_PIXEL_SCALE screen pixels per art pixel at both: it
## does not grow with the window (grown, it crowded the village at 3x).
func _test_art_bubble_lands_on_whole_pixels() -> void:
    _set_art(_synthetic_sheet())
    var bubble: Node2D = _spawn(LONG_TEXT)
    _check("the art path is in use with a sheet and a font", bubble._use_art)
    var base_stretch: float = root.get_final_transform().get_scale().x
    var saved_factor: float = root.content_scale_factor
    for case in [[1.0, _script.ART_PIXEL_SCALE], [1.5, _script.ART_PIXEL_SCALE]]:
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
            _set_art(_synthetic_sheet())
        else:
            _set_art(null)
        var bubble: Node2D = _spawn("Aye.")
        for z in ZOOMS:
            _zoom(z)
            bubble._fit_to_screen()
            _check("tail anchor is ANCHOR_Y world px above the villager at zoom %.1f (art %s)" % [z, art],
                bubble.position == Vector2(0, _script.ANCHOR_Y))
        bubble.free()
    _done()

func _test_no_art_means_the_plain_bubble() -> void:
    _set_art(null)
    var bubble: Node2D = _spawn("Aye.")
    _check("no sheet falls back to the plain bubble", not bubble._use_art)
    _check("the plain bubble has no separate text layer", bubble._text_layer == null)
    bubble.free()
    _set_art(_synthetic_sheet())
    bubble = _spawn("Aye.")
    _check("a sheet means the art bubble", bubble._use_art)
    bubble.free()
    _done()

## The text is UI-sized on both paths, so the 9-slice is sized from the
## text's UI size times the window stretch, in art px. Checked at the base
## size and a 1920-wide window.
func _test_art_bubble_grows_to_fit_the_text() -> void:
    _set_art(_synthetic_sheet())
    for stretch in [1.0, 1.5]:
        var saved: float = _set_stretch(stretch)
        _zoom(1.0)
        var short: Node2D = _spawn("Aye.")
        var long: Node2D = _spawn(LONG_TEXT)
        var room: Vector2 = _script.ART_TEXT_ROOM
        _check("a long line wraps to more than one line", long._wrapped_lines.size() > 1)
        _check("no wrapped line is wider than the wrap width",
            long._content_size.x <= _script.MAX_TEXT_WIDTH - 2 * _script.PADDING_X)
        for b in [short, long]:
            var fits: Vector2 = room + b._art_grow()
            var need: Vector2 = b._content_size * stretch / _script.ART_PIXEL_SCALE
            _check("the bubble fill holds the text at stretch %.1f (%s)" % [stretch, b._wrapped_lines[0]],
                fits.x >= need.x and fits.y >= need.y)
        _check("a short line keeps the sheet's own width at stretch %.1f" % stretch, short._art_grow().x == 0.0)
        _check("a longer text grows the bubble taller at stretch %.1f" % stretch,
            long._art_grow().y > short._art_grow().y)
        # Checked against the sheet itself, not ART_TEXT_ROOM: in the Mana
        # Seed bubble the last cream fill rows are sheet rows 33-34 (row 35
        # starts the bottom shading, row 36 the outline), and the fill's
        # right edge is column 50. The text box must end inside the fill.
        var sheet_last_fill_row := 34
        var sheet_last_fill_col := 50
        for b in [short, long]:
            var grow: Vector2 = b._art_grow()
            var text_art: Vector2 = b._content_size * stretch / _script.ART_PIXEL_SCALE
            var last_fill_row: float = _script.ART_TOP_ROWS + grow.y + (sheet_last_fill_row - _script.ART_BOTTOM_ROW)
            var last_fill_col: float = sheet_last_fill_col + grow.x
            var text_bottom: float = _script.ART_TEXT_ORIGIN.y + text_art.y
            var text_right: float = _script.ART_TEXT_ORIGIN.x + text_art.x
            _check("text ends above the bottom shading at stretch %.1f (%s: bottom %.1f, last fill row %.0f)" % [stretch, b._wrapped_lines[0], text_bottom, last_fill_row],
                text_bottom <= last_fill_row + 1.0)
            _check("text ends inside the right edge at stretch %.1f (%s: right %.1f, last fill col %.0f)" % [stretch, b._wrapped_lines[0], text_right, last_fill_col],
                text_right <= last_fill_col + 1.0)
        short.free()
        long.free()
        root.content_scale_factor = saved
    _done()

## The text is not pixel art: inside the art bubble it sits on its own
## layer, smoothly filtered, at the UI's size on screen (the window stretch,
## like the plain bubble), at every zoom and window size.
func _test_art_text_is_ui_sized_and_smooth() -> void:
    _set_art(_synthetic_sheet())
    var bubble: Node2D = _spawn(LONG_TEXT)
    var layer: Node2D = bubble._text_layer
    _check("the art bubble has a text layer", layer != null)
    _check("the text layer filters smoothly", layer.texture_filter == CanvasItem.TEXTURE_FILTER_LINEAR)
    for stretch in [1.0, 1.5]:
        var saved: float = _set_stretch(stretch)
        for z in ZOOMS:
            _zoom(z)
            bubble._fit_to_screen()
            var s: Vector2 = _on_screen(layer)
            var want: Vector2 = root.get_final_transform().get_scale()
            _check("text is UI-sized on screen at zoom %.1f, stretch %.1f (got %s, want %s)" % [z, stretch, s, want],
                s.is_equal_approx(want))
        root.content_scale_factor = saved
    bubble.free()
    _done()

## A long line is shown PAGE_LINES at a time so the bubble never towers;
## nothing is lost, every page gets its own reading time, and the bubble
## keeps one size across pages. Pages are turned by calling the timer's
## handler directly.
func _test_long_speech_shows_a_page_at_a_time() -> void:
    for art in [false, true]:
        _set_art(_synthetic_sheet() if art else null)
        var bubble: Node2D = _spawn(HAGGLE_TEXT)
        var pages: Array = bubble._pages
        var mark: String = _script.MORE_MARK
        var lead: String = _script.LEAD_MARK
        var lead_w: float = bubble._font.get_string_size(lead, HORIZONTAL_ALIGNMENT_LEFT, -1, bubble._font_size).x
        var max_w: float = _script.MAX_TEXT_WIDTH - 2 * _script.PADDING_X
        var mark_w: float = bubble._font.get_string_size(mark, HORIZONTAL_ALIGNMENT_LEFT, -1, bubble._font_size).x
        _check("the haggle runs to more than one page (art %s, %d pages)" % [art, pages.size()], pages.size() > 1)
        var words: Array[String] = []
        for p in pages.size():
            var page: PackedStringArray = pages[p]
            _check("page %d holds at most PAGE_LINES lines (art %s)" % [p, art], page.size() <= _script.PAGE_LINES)
            var last: bool = p == pages.size() - 1
            _check("page %d ends in the more-mark only if more follows (art %s)" % [p, art],
                page[page.size() - 1].ends_with(mark) != last)
            _check("page %d opens with the lead-mark only if it continues a page (art %s)" % [p, art],
                page[0].begins_with(lead) == (p > 0))
            for line in page:
                var plain: String = line.trim_suffix(mark).trim_prefix(lead)
                var w: float = bubble._font.get_string_size(line, HORIZONTAL_ALIGNMENT_LEFT, -1, bubble._font_size).x
                _check("a line fits the wrap width, plus the mark on a page end (art %s): %s" % [art, line],
                    w <= max_w + (mark_w if line.ends_with(mark) else 0.0) + (lead_w if line.begins_with(lead) else 0.0) + 0.5)
                words.append_array(Array(plain.split(" ", false)))
            var life: float = bubble._page_lifetime(p)
            _check("page %d stays up between MIN and MAX (art %s, %.1fs)" % [p, art, life],
                life >= _script.MIN_LIFETIME and life <= _script.MAX_LIFETIME)
        _check("every word survives, in order (art %s)" % art,
            " ".join(PackedStringArray(words)) == " ".join(HAGGLE_TEXT.split(" ", false)))
        var size: Vector2 = bubble._content_size
        _check("the bubble is PAGE_LINES lines tall (art %s)" % art,
            is_equal_approx(size.y, bubble._font.get_height(bubble._font_size) * _script.PAGE_LINES))
        for p in range(1, pages.size()):
            bubble._next_page()
            _check("turning shows page %d (art %s)" % [p, art], bubble._wrapped_lines == pages[p])
            _check("the bubble keeps its size on page %d (art %s)" % [p, art], bubble._content_size == size)
            _check("the timer restarts for page %d (art %s)" % [p, art],
                is_equal_approx(bubble._page_timer.wait_time, bubble._page_lifetime(p)))
        _check("the bubble is still up on its last page (art %s)" % art, not bubble.is_queued_for_deletion())
        bubble._next_page()
        _check("turning past the last page removes the bubble (art %s)" % art, bubble.is_queued_for_deletion())
        bubble.free()
    _set_art(null)
    var short: Node2D = _spawn("Aye, I'll see to it.")
    _check("a short line is one page", short._pages.size() == 1)
    _check("a short line carries no more-mark", not short._wrapped_lines[0].ends_with(_script.MORE_MARK))
    _check("a short line carries no lead-mark", not short._wrapped_lines[0].begins_with(_script.LEAD_MARK))
    _check("a short line's bubble is one line tall",
        is_equal_approx(short._content_size.y, short._font.get_height(short._font_size)))
    short.free()
    _done()

func _test_blank_speech_draws_no_bubble() -> void:
    _set_art(null)
    var bubble: Node2D = _spawn("   ")
    _check("all-space speech removes the bubble", bubble.is_queued_for_deletion())
    _check("all-space speech starts no page timer", bubble._page_timer == null)
    bubble.free()
    _done()

## Starts the live-camera test; _step_live_camera finishes it over the next
## frames. A real Camera2D, zoomed from node processing.
func _test_live_camera_zoom_is_cancelled_the_same_frame() -> void:
    root.canvas_transform = Transform2D.IDENTITY
    _set_art(_synthetic_sheet())
    _live_bubble = _spawn(LONG_TEXT)
    # Added after the actor, so at equal priority it would process after
    # the bubble — the order that leaves a bubble one zoom stale.
    _camera = Camera2D.new()
    root.add_child(_camera)
    _camera.make_current()
    _zoomer = Zoomer.new()
    _zoomer.camera = _camera
    root.add_child(_zoomer)
    _live_step = 0

func _step_live_camera() -> void:
    if _live_step > 0:
        var z: float = LIVE_ZOOMS[_live_step - 1]
        var canvas: Vector2 = root.canvas_transform.get_scale()
        _check("harness — the camera applied zoom %.1f (canvas %s)" % [z, canvas],
            canvas.is_equal_approx(Vector2(z, z)))
        var units := float(_script.ART_PIXEL_SCALE)
        var s: Vector2 = _on_screen(_live_bubble)
        _check("the bubble matched zoom %.1f in the frame the camera zoomed (on screen %s, want %.0f)" % [z, s, units],
            s.is_equal_approx(Vector2(units, units)))
    if _live_step < LIVE_ZOOMS.size():
        _zoomer.pending = LIVE_ZOOMS[_live_step]
        _live_step += 1
        return
    _live_bubble.free()
    _zoomer.free()
    _camera.free()
    _live_step += 1
    _current = "_test_live_camera_zoom_is_cancelled_the_same_frame"
    _done()

## LLM-693: a speaker who talks again replaces their bubble — never a second
## one beside it. The old bubble used to stay a child until the end of the
## frame, so the new one was renamed and the next line could not find it to
## replace: bubbles stacked. Checked for a villager and for a structure.
## Runs within one frame, the way the bug did.
func _test_a_speaker_has_one_bubble() -> void:
    var world = load("res://scripts/world.gd").new()
    var npc := Node2D.new()
    var shop := Node2D.new()
    root.add_child(npc)
    root.add_child(shop)
    world.placed_npcs["josiah"] = npc
    world.placed_objects["store"] = shop
    for line in ["First line.", "Second line.", "Third line."]:
        world._spawn_speech_bubble("josiah", line)
        world.spawn_structure_bubble("store", line)
    for pair in [["villager", npc], ["structure", shop]]:
        var holder: Node2D = pair[1]
        var live := []
        for c in holder.get_children():
            if not c.is_queued_for_deletion():
                live.append(c)
        _check("one bubble after three lines (%s, got %d)" % [pair[0], live.size()], live.size() == 1)
        _check("it is the last line's bubble (%s)" % pair[0],
            live.size() == 1 and live[0].name == world.SPEECH_BUBBLE_NODE_NAME and live[0]._wrapped_lines[0] == "Third line.")
    npc.queue_free()
    shop.queue_free()
    world.free()
    _done()

## LLM-694: two speakers one tile apart both talk. The newer line stays at
## its speaker's head; the older rises just clear of it, so neither hides
## the other, and its stem reaches down to its own speaker. Every zoom,
## both looks, at the base window size and a 1920-wide one.
func _test_side_by_side_speakers_stack_apart() -> void:
    var art_before: Texture2D = _script._art_sheet  # the live-camera bubble still draws with it
    for art in [false, true]:
        _set_art(_synthetic_sheet() if art else null)
        for stretch in [1.0, 1.5]:
            var saved: float = _set_stretch(stretch)
            for z in ZOOMS:
                _zoom(z)
                var who: Array = _speakers(2)
                var first: Node2D = _say(who[0], LONG_TEXT)
                var second: Node2D = _say(who[1], "Aye, I heard it was the windlass.")
                var label := "zoom %.1f, stretch %.1f, art %s" % [z, stretch, art]
                _settle([first, second])
                _check("harness — the bubbles would overlap unlifted (%s)" % label,
                    first._canvas_rect().intersects(second._canvas_rect()))
                var top: Rect2 = _drawn_rect(first)
                var low: Rect2 = _drawn_rect(second)
                _check("the two bubbles do not overlap (%s)" % label, not top.intersects(low))
                _check("the older line sits above the newer (%s)" % label, top.end.y <= low.position.y + 0.01)
                _check("the newer line stays at its speaker's head (%s)" % label,
                    second._lift == 0.0 and second.position == Vector2(0, _script.ANCHOR_Y))
                _check("the newer line has no stem (%s)" % label, not second._stem.visible)
                _check("the older line has a stem (%s)" % label, first._stem.visible)
                var tip_to_head: float = first._stem_length * first.get_global_transform_with_canvas().get_scale().y
                var head: Vector2 = who[0].get_global_transform_with_canvas() * Vector2(0, _script.ANCHOR_Y)
                var tip: Vector2 = first.get_global_transform_with_canvas() * Vector2.ZERO
                _check("the stem reaches the older speaker's head (%s: tip %s + %.1f, head %s)" % [label, tip, tip_to_head, head],
                    absf(tip.y + tip_to_head - head.y) < 0.01 and absf(tip.x - head.x) < 0.01)
                _free_all(who)
            root.content_scale_factor = saved
    _zoom(1.0)
    _set_art(art_before)
    _done()

## Speakers far apart keep their bubbles where they are.
func _test_far_apart_speakers_keep_their_place() -> void:
    var art_before: Texture2D = _script._art_sheet  # the live-camera bubble still draws with it
    _set_art(null)
    _zoom(1.0)
    var who: Array = _speakers(2, 2000.0)
    var a: Node2D = _say(who[0], LONG_TEXT)
    var b: Node2D = _say(who[1], LONG_TEXT)
    _settle([a, b])
    for x in [a, b]:
        _check("a bubble with no one near stays at its anchor", x._lift == 0.0 and not x._stem.visible)
    _free_all(who)
    _set_art(art_before)
    _done()

## Four speakers side by side, four lines: the stack keeps the newest
## three, apart and in order; the oldest closes early.
func _test_a_stack_holds_three() -> void:
    var art_before: Texture2D = _script._art_sheet  # the live-camera bubble still draws with it
    _set_art(null)
    _zoom(1.0)
    var who: Array = _speakers(4)
    var bubbles := []
    for i in 4:
        bubbles.append(_say(who[i], "Line %d of the talk at the well." % i))
    _settle(bubbles)
    _check("the oldest line closes early", bubbles[0].is_queued_for_deletion())
    for i in range(1, 4):
        _check("line %d stays up" % i, not bubbles[i].is_queued_for_deletion())
    for i in range(1, 3):
        _check("line %d sits above line %d" % [i, i + 1],
            _drawn_rect(bubbles[i]).end.y <= _drawn_rect(bubbles[i + 1]).position.y + 0.01)
    _check("harness — MAX_STACK is three", _script.MAX_STACK == 3)
    _free_all(who)
    _set_art(art_before)
    _done()

## A bubble pushed up glides there over a few frames rather than jumping,
## and lands exactly on its place.
func _test_a_lift_eases_into_place() -> void:
    var art_before: Texture2D = _script._art_sheet  # the live-camera bubble still draws with it
    _set_art(null)
    _zoom(1.0)
    var who: Array = _speakers(2)
    var first: Node2D = _say(who[0], LONG_TEXT)
    _script._layout([first], 1.0 / 60.0)
    _check("one bubble alone is not lifted", first._lift == 0.0)
    var second: Node2D = _say(who[1], LONG_TEXT)
    _script._layout([first, second], 1.0 / 60.0)
    var target: float = first._lift_target
    _check("the older bubble has somewhere to go (target %.1f)" % target, target > 0.0)
    _check("one frame in, it has moved part of the way (%.1f of %.1f)" % [first._lift, target],
        first._lift > 0.0 and first._lift < target)
    for i in 12:
        _script._layout([first, second], 1.0 / 60.0)
    _check("after 0.2 s it is in place (%.1f of %.1f)" % [first._lift, target], first._lift == target)
    _free_all(who)
    _set_art(art_before)
    _done()

## A stack is every bubble joined by overlaps, not the longest chain: two
## short new lines that do not touch, one wide older line over both, and
## an oldest line over that make four in one stack — the oldest closes.
func _test_a_wide_bubble_joins_two_stacks() -> void:
    var art_before: Texture2D = _script._art_sheet  # the live-camera bubble still draws with it
    _set_art(null)
    _zoom(1.0)
    var who: Array = _speakers(3, 120.0)
    var left: Node2D = _say(who[0], "Aye.")
    var right: Node2D = _say(who[2], "Nay.")
    # Spawned after, so newer: re-number the two short lines newest.
    var wide: Node2D = _say(who[1], LONG_TEXT)
    var oldest: Node2D = _say(who[1], LONG_TEXT)
    oldest._seq = 0
    wide._seq = 1
    left._seq = 2
    right._seq = 3
    var all := [left, right, wide, oldest]
    _check("harness — the two short lines do not touch",
        not left._canvas_rect().grow(_script.STACK_GAP).intersects(right._canvas_rect()))
    _check("harness — the wide line spans both",
        wide._canvas_rect().intersects(left._canvas_rect()) and wide._canvas_rect().intersects(right._canvas_rect()))
    _settle(all)
    _check("the wide line rises over both short lines", wide._lift > 0.0 and left._lift == 0.0 and right._lift == 0.0)
    _check("the fourth bubble of the joined stack closes", oldest.is_queued_for_deletion())
    _check("the three newest stay up",
        not left.is_queued_for_deletion() and not right.is_queued_for_deletion() and not wide.is_queued_for_deletion())
    _free_all(who)
    _set_art(art_before)
    _done()

## LLM-705: on a touch screen the bubble text is bigger (14 -> 17 via
## OrientationGuard.text_size with BUBBLE_TOUCH_TEXT_SCALE); paging, wrapping
## and the art fill must all still hold. Touch mode is the guard autoload's
## flag, set before setup() reads it. The art cache is restored, not cleared:
## the live-camera test keeps drawing its bubble for frames after this.
func _test_touch_text_is_bigger_and_still_fits() -> void:
    var guard: Node = root.get_node("OrientationGuard")
    var prev_sheet: Texture2D = _script._art_sheet
    var prev_touch_text: bool = guard._touch_text
    guard._touch_text = true
    var want_size: int = roundi(_script.FONT_SIZE * guard.BUBBLE_TOUCH_TEXT_SCALE)
    var max_w: float = _script.MAX_TEXT_WIDTH - 2 * _script.PADDING_X
    for art in [false, true]:
        _set_art(_synthetic_sheet() if art else null)
        _zoom(1.0)
        var paged: Node2D = _spawn(HAGGLE_TEXT)
        _check("touch font is %d (art %s, got %d)" % [want_size, art, paged._font_size], paged._font_size == want_size)
        _check("touch text still pages (art %s)" % art, paged._pages.size() > 1)
        for p in paged._pages.size():
            var page: PackedStringArray = paged._pages[p]
            _check("touch page %d holds at most PAGE_LINES lines (art %s)" % [p, art], page.size() <= _script.PAGE_LINES)
        _check("touch bubble is PAGE_LINES lines of the bigger font (art %s)" % art,
            is_equal_approx(paged._content_size.y, paged._font.get_height(want_size) * _script.PAGE_LINES))
        paged.free()
        var long: Node2D = _spawn(LONG_TEXT)
        _check("touch text wraps inside the wrap width (art %s)" % art, long._content_size.x <= max_w)
        long.free()
    _set_art(_synthetic_sheet())
    for stretch in [1.0, 1.5]:
        var saved: float = _set_stretch(stretch)
        _zoom(1.0)
        var long: Node2D = _spawn(LONG_TEXT)
        var fits: Vector2 = _script.ART_TEXT_ROOM + long._art_grow()
        var need: Vector2 = long._content_size * stretch / _script.ART_PIXEL_SCALE
        _check("the art fill holds the bigger text at stretch %.1f" % stretch, fits.x >= need.x and fits.y >= need.y)
        long.free()
        root.content_scale_factor = saved
    guard._touch_text = prev_touch_text
    _script._art_sheet = prev_sheet
    _done()
