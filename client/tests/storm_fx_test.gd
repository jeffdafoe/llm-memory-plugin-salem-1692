extends SceneTree

## Headless regression harness for the storm overlay — particle rain plus
## sprite lightning (LLM-117, LLM-630).
##
## What this file exists to catch:
##
##  1. The bolt's zoom contract. The bolt is pixel art on the village's 16px
##     grid, and the village draws at render_scale 2.0, so it must be drawn at
##     2.0 * camera.zoom.x or it reads as a different resolution than the world
##     — with a floor, because a pixel-matched bolt at the 0.3 zoom limit is a
##     scratch. The particles need none of this; a textureless point has no
##     pixel size to mismatch.
##
##  2. Nothing outliving a storm. A flash tween or a pending strike that
##     survives "clear" lights the screen under an open sky.
##
##  3. Degrading without the art. The Mana Seed pack is purchased and
##     gitignored (client/.gitignore), so a checkout without it is normal —
##     CI runs exactly that way. Missing sheets must leave a working storm
##     (rain, tint, full-screen flash) rather than erroring every frame.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/storm_fx_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The overlay is added to the tree (unlike the world.gd suites, which use
## .new() off-tree) because _ready builds every node and _layout needs a real
## viewport. Sheet loading is therefore exercised for real — and asserted only
## on what holds BOTH with the art present (local) and absent (CI). Frame
## slicing is asserted against a synthetic in-memory sheet instead, so the
## count contract is checked the same way in both places.

const TESTS := [
    "_test_slices_a_synthetic_sheet",
    "_test_slice_stops_short_on_an_undersized_sheet",
    "_test_missing_sheet_yields_no_frames",
    "_test_pixel_scale_tracks_camera_zoom",
    "_test_bolt_scale_floors_only_when_zoomed_out",
    "_test_bolt_is_drawable_at_every_zoom",
    "_test_rain_particles_follow_the_storm",
    "_test_storm_cycle_without_art",
    "_test_clearing_mid_strike_leaves_nothing_running",
]

const BOLT_FRAME := Vector2i(32, 128)
const BOLT_VARIANTS := 2

var _fx: CanvasLayer = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

## Setup only. A node added to root during _initialize is NOT in the tree yet
## — is_inside_tree() is false and _ready has not fired — so the checks cannot
## run here or they would test an unbuilt overlay against a null viewport.
func _initialize() -> void:
    _fx = CanvasLayer.new()
    _fx.set_script(load("res://scripts/storm_fx.gd"))
    root.add_child(_fx)

## First frame: the overlay is in the tree and _ready has built it. Run
## everything and quit. Returning true would also quit, but quit(code) is what
## carries the pass/fail exit status the CI loop reads.
func _process(_delta: float) -> bool:
    _check("harness — overlay entered the tree", _fx.is_inside_tree())
    _check_test_list()
    _run_all()
    _fx.queue_free()
    _check_all_tests_ran()
    print("\n[storm_fx_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[storm_fx_test] ALL PASS")
    quit(1 if _failures > 0 else 0)
    return true

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

## Same harness contract as asset_render_scale_test.gd (LLM-480): a runtime
## error aborts only the function it happens in, so every test calls _done()
## as its last statement and the harness asserts each one reached it.
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

## A stand-in for the 64x128 bolt sheet: N frames side by side.
func _make_strip(frame: Vector2i, count: int) -> ImageTexture:
    var img := Image.create(frame.x * count, frame.y, false, Image.FORMAT_RGBA8)
    return ImageTexture.create_from_image(img)

## Camera2D fixture — the overlay reads only zoom off it.
func _set_zoom(zoom: float) -> Camera2D:
    var cam := Camera2D.new()
    cam.zoom = Vector2(zoom, zoom)
    _fx.camera = cam
    return cam

## Free the fixture camera AND drop the overlay's reference to it. Leaving the
## reference behind leaves _fx.camera pointing at a freed instance, which the
## next test inherits and cannot even read back.
func _clear_zoom(cam: Camera2D) -> void:
    _fx.camera = null
    cam.free()

# --- assertions ------------------------------------------------------------------

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)

# --- tests -----------------------------------------------------------------------

func _test_slices_a_synthetic_sheet() -> void:
    var frames: Array[Texture2D] = _fx._slice_texture(_make_strip(BOLT_FRAME, BOLT_VARIANTS), BOLT_FRAME, BOLT_VARIANTS)
    _check("slices one frame per variant", frames.size() == BOLT_VARIANTS)
    for i in frames.size():
        _check("frame %d is one bolt wide" % i, frames[i].get_width() == BOLT_FRAME.x)
        _check("frame %d is one bolt tall" % i, frames[i].get_height() == BOLT_FRAME.y)
    _done()

## A sheet shorter than count * frame width must stop at the last whole frame
## rather than reading past the edge — the shape a re-exported or wrong-sized
## replacement sheet would have.
func _test_slice_stops_short_on_an_undersized_sheet() -> void:
    var frames: Array[Texture2D] = _fx._slice_texture(_make_strip(BOLT_FRAME, 1), BOLT_FRAME, BOLT_VARIANTS)
    _check("stops at the frames that actually fit", frames.size() == 1)
    _done()

func _test_missing_sheet_yields_no_frames() -> void:
    var frames: Array[Texture2D] = _fx._slice_sheet("res://assets/tilesets/mana-seed/weather-effects/no such sheet.png", BOLT_FRAME, BOLT_VARIANTS)
    _check("absent sheet slices to nothing", frames.is_empty())
    _done()

## One source pixel of village art covers 2.0 * zoom screen pixels — the bolt
## has to agree with world.gd's render_scale or it is the wrong size against
## the world. No camera yet (main.gd injects it after _ready) falls back to the
## unzoomed scale.
func _test_pixel_scale_tracks_camera_zoom() -> void:
    _fx.camera = null
    _check("no camera falls back to the world render scale", is_equal_approx(_fx._world_pixel_scale(), 2.0))
    for zoom in [0.3, 1.0, 3.0]:
        var cam := _set_zoom(zoom)
        _check("zoom %s scales to %s" % [zoom, 2.0 * zoom], is_equal_approx(_fx._world_pixel_scale(), 2.0 * zoom))
        _clear_zoom(cam)
    _done()

## The bolt floor must engage only when zoomed out past 1:1 — above it the bolt
## keeps tracking the world.
func _test_bolt_scale_floors_only_when_zoomed_out() -> void:
    for zoom in [0.3, 0.5, 1.0, 2.0]:
        var cam := _set_zoom(zoom)
        _check("zoom %s — bolt is the larger of world scale and the floor" % zoom,
            is_equal_approx(_fx._bolt_scale(), maxf(2.0 * zoom, 2.0)))
        _clear_zoom(cam)
    _done()

## Scale alone does not make a Control drawable: an unparented Control's size
## reaches the texture's minimum only on a later deferred pass, so a bolt read
## or drawn in the same frame would have zero area. That shipped as a real bug
## once — and every scale assertion above passes with a zero-size bolt.
func _test_bolt_is_drawable_at_every_zoom() -> void:
    for zoom in [0.3, 1.0, 3.0]:
        var cam := _set_zoom(zoom)
        _fx._layout()
        var scale: float = maxf(2.0 * zoom, 2.0)
        _check("zoom %s — bolt drawn at %s" % [zoom, scale],
            _fx._bolt.scale.is_equal_approx(Vector2(scale, scale)))
        _check("zoom %s — bolt is one frame in unscaled units" % zoom,
            _fx._bolt.size.is_equal_approx(Vector2(BOLT_FRAME)))
        var rect: Vector2 = _fx._bolt.size * _fx._bolt.scale
        _check("zoom %s — bolt covers real screen area (%s)" % [zoom, rect],
            rect.x > 0.0 and rect.y > 0.0)
        _clear_zoom(cam)
    _done()

## Rain is particles, and the emitter is the whole of its on/off state — there
## is no fade, so a storm that forgets to stop emitting rains under a clear sky.
func _test_rain_particles_follow_the_storm() -> void:
    _fx.set_storm(true, false)
    _check("a storm emits rain", _fx._rain.emitting)
    _fx.set_storm(false, false)
    _check("clearing stops the rain", not _fx._rain.emitting)
    _done()

## The whole raise/strike/clear cycle with no art loaded (CI's state). Nothing
## here may error, and the rain and flash — which need no art at all — must
## still run.
func _test_storm_cycle_without_art() -> void:
    var bolts: Array[Texture2D] = _fx._bolt_variants
    # An untyped [] cannot be assigned to an Array[Texture2D] field.
    var none: Array[Texture2D] = []
    _fx._bolt_variants = none

    _fx.set_storm(true)
    _check("artless storm still raises the tint", _fx._tint_tween != null)
    _check("artless storm still rains", _fx._rain.emitting)
    _fx._flash()
    _check("artless storm still flashes", _fx._lightning_tween != null)
    _fx._strike()
    _check("artless storm draws no bolt", not _fx._bolt.visible)
    _fx.set_storm(false, false)
    _check("artless storm clears the flash", is_equal_approx(_fx._lightning.modulate.a, 0.0))

    _fx._bolt_variants = bolts
    _done()

## Weather can clear mid-strike: the sky goes calm between the flash starting
## and the bolt finishing. Nothing the storm scheduled may survive that — a
## live flash tween would light the screen under a clear sky, and a pending
## strike callback would draw a bolt out of nowhere. Resetting the two
## properties is not sufficient on its own, so this asserts the schedulers are
## dead too, not merely that this frame looks right.
func _test_clearing_mid_strike_leaves_nothing_running() -> void:
    # Seed synthetic bolts rather than relying on the purchased pack. Without
    # them _strike() correctly no-ops, so on an art-less checkout (CI) every
    # assertion below would be vacuous — and the preconditions would fail
    # outright. What is under test is the teardown, not the art.
    var bolts: Array[Texture2D] = _fx._bolt_variants
    _fx._bolt_variants = _fx._slice_texture(_make_strip(BOLT_FRAME, BOLT_VARIANTS), BOLT_FRAME, BOLT_VARIANTS)

    _fx.set_storm(true, false)
    _fx._flash()
    _fx._strike()
    _check("precondition — a strike is in flight", _fx._bolt.visible)
    _check("precondition — the flash tween is live",
        _fx._lightning_tween != null and _fx._lightning_tween.is_valid())
    _check("precondition — the bolt tween is live",
        _fx._bolt_tween != null and _fx._bolt_tween.is_valid())

    _fx.set_storm(false, false)

    _check("clearing hides the bolt", not _fx._bolt.visible)
    _check("clearing resets the flash to dark", is_equal_approx(_fx._lightning.modulate.a, 0.0))
    # Killed, not merely finished — a live tween would keep driving both of
    # those properties after the storm is gone.
    _check("clearing kills the flash tween", _fx._lightning_tween == null)
    _check("clearing kills the bolt tween", _fx._bolt_tween == null)
    # The repeat timer is the third way a bolt could come back on its own.
    _check("clearing stops the strike timer", _fx._lightning_timer.is_stopped())

    # Belt and braces: if that timer ever did fire anyway, the handler has to
    # refuse it rather than rely on having been stopped.
    _fx._on_lightning_timeout()
    _check("a late timeout draws no bolt", not _fx._bolt.visible)
    _check("a late timeout does not flash", is_equal_approx(_fx._lightning.modulate.a, 0.0))
    _check("a late timeout schedules no further strike", _fx._lightning_timer.is_stopped())

    _fx._bolt_variants = bolts
    _done()
