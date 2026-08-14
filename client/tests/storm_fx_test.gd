extends SceneTree

## Headless regression harness for LLM-628 — the sprite-based storm overlay.
##
## Two things this file exists to catch:
##
##  1. The zoom contract. The weather art is pixel art on the village's 16px
##     grid, and the village draws at render_scale 2.0, so the overlay must
##     draw its tiles at 2.0 * camera.zoom.x or the rain reads as a different
##     resolution than the world under it. A Control's size is in PRE-scale
##     units, so screen coverage has to divide by that scale — getting the
##     two to move together is the regression here.
##
##  2. Degrading without the art. The Mana Seed pack is purchased and
##     gitignored (client/.gitignore), so a checkout without it is normal —
##     CI runs exactly that way. Missing sheets must leave a working storm
##     (tint + full-screen flash) rather than erroring every frame.
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
    "_test_layout_covers_viewport_at_every_zoom",
    "_test_bolt_scale_floors_only_when_zoomed_out",
    "_test_storm_cycle_without_art",
    "_test_rain_hidden_after_instant_clear",
    "_test_clearing_mid_strike_leaves_nothing_running",
    "_test_wind_drift_slides_and_wraps",
    "_test_artless_counters_stay_wrapped",
    "_test_a_long_delta_advances_all_its_frames",
]

const FRAME := Vector2i(32, 128)
const FRAME_COUNT := 8
const BOLT_FRAME := Vector2i(32, 128)

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

## A stand-in for one of the 256x128 rain sheets: 8 frames of 32x128.
func _make_strip(frame: Vector2i, count: int) -> ImageTexture:
    var img := Image.create(frame.x * count, frame.y, false, Image.FORMAT_RGBA8)
    return ImageTexture.create_from_image(img)

## Camera2D fixture — the overlay reads only zoom off it.
func _camera_at(zoom: float) -> Camera2D:
    var cam := Camera2D.new()
    cam.zoom = Vector2(zoom, zoom)
    return cam

func _set_zoom(zoom: float) -> Camera2D:
    var cam := _camera_at(zoom)
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
    var frames: Array[Texture2D] = _fx._slice_texture(_make_strip(FRAME, FRAME_COUNT), FRAME, FRAME_COUNT)
    _check("slices one frame per animation frame", frames.size() == FRAME_COUNT)
    for i in frames.size():
        _check("frame %d is one tile wide" % i, frames[i].get_width() == FRAME.x)
        _check("frame %d is one tile tall" % i, frames[i].get_height() == FRAME.y)
    _done()

## A sheet shorter than count * frame width must stop at the last whole frame
## rather than reading past the edge — the shape a re-exported or wrong-sized
## replacement sheet would have.
func _test_slice_stops_short_on_an_undersized_sheet() -> void:
    var frames: Array[Texture2D] = _fx._slice_texture(_make_strip(FRAME, 3), FRAME, FRAME_COUNT)
    _check("stops at the frames that actually fit", frames.size() == 3)
    _done()

func _test_missing_sheet_yields_no_frames() -> void:
    var frames: Array[Texture2D] = _fx._slice_sheet("res://assets/tilesets/mana-seed/weather-effects/no such sheet.png", FRAME, FRAME_COUNT)
    _check("absent sheet slices to nothing", frames.is_empty())
    _done()

## One source pixel of village art covers 2.0 * zoom screen pixels — the
## overlay has to agree with world.gd's render_scale or the rain is the wrong
## size against the world. No camera yet (main.gd injects it after _ready)
## falls back to the unzoomed scale.
func _test_pixel_scale_tracks_camera_zoom() -> void:
    _fx.camera = null
    _check("no camera falls back to the world render scale", is_equal_approx(_fx._world_pixel_scale(), 2.0))
    for zoom in [0.3, 1.0, 3.0]:
        var cam := _set_zoom(zoom)
        _check("zoom %s scales to %s" % [zoom, 2.0 * zoom], is_equal_approx(_fx._world_pixel_scale(), 2.0 * zoom))
        _clear_zoom(cam)
    _done()

## The rain rects are scaled, and a Control's size is in pre-scale units — so
## size * scale must still span the viewport at both zoom extremes. This is the
## pairing that breaks if someone "fixes" one half of it.
func _test_layout_covers_viewport_at_every_zoom() -> void:
    var view: Vector2 = _fx.get_viewport().get_visible_rect().size
    _check("harness — viewport has area", view.x > 0.0 and view.y > 0.0)
    for zoom in [0.3, 1.0, 3.0]:
        var cam := _set_zoom(zoom)
        _fx._layout()
        var scale: float = 2.0 * zoom
        for rect: TextureRect in [_fx._rain_heavy, _fx._rain_light]:
            _check("zoom %s — tiles drawn at the village pixel size" % zoom,
                rect.scale.is_equal_approx(Vector2(scale, scale)))
            var covered: Vector2 = rect.size * rect.scale
            _check("zoom %s — rain still spans the viewport (%s vs %s)" % [zoom, covered, view],
                covered.x >= view.x and covered.y >= view.y)
        # The bolt tracks the same scale but never below the unzoomed village
        # size, or it reads as a scratch at the 0.3 zoom floor.
        var bolt: float = maxf(scale, 2.0)
        _check("zoom %s — bolt drawn at %s" % [zoom, bolt],
            _fx._bolt.scale.is_equal_approx(Vector2(bolt, bolt)))
        _check("zoom %s — bolt never shrinks below the village's own size" % zoom,
            _fx._bolt.scale.x >= 2.0)
        # Scale alone does not make a Control drawable — a zero-size bolt would
        # satisfy every scale check above and still render nothing.
        _check("zoom %s — bolt is one frame in unscaled units" % zoom,
            _fx._bolt.size.is_equal_approx(Vector2(BOLT_FRAME)))
        var bolt_rect: Vector2 = _fx._bolt.size * _fx._bolt.scale
        _check("zoom %s — bolt covers real screen area (%s)" % [zoom, bolt_rect],
            bolt_rect.x > 0.0 and bolt_rect.y > 0.0)
        _clear_zoom(cam)
    _done()

## The bolt floor must engage only when zoomed out past 1:1 — above it the bolt
## has to keep tracking the world, or it stops matching the rain beside it.
func _test_bolt_scale_floors_only_when_zoomed_out() -> void:
    for zoom in [0.3, 0.5, 1.0, 2.0]:
        var cam := _set_zoom(zoom)
        _check("zoom %s — bolt is the larger of world scale and the floor" % zoom,
            is_equal_approx(_fx._bolt_scale(), maxf(2.0 * zoom, 2.0)))
        _clear_zoom(cam)
    _done()

## The whole raise/strike/clear cycle with no art loaded (CI's state). Nothing
## here may error, and the flash — the part that carries the effect — must
## still run.
func _test_storm_cycle_without_art() -> void:
    var light: Array[Texture2D] = _fx._rain_light_frames
    var heavy: Array[Texture2D] = _fx._rain_heavy_frames
    var bolts: Array[Texture2D] = _fx._bolt_variants
    # An untyped [] cannot be assigned to an Array[Texture2D] field.
    var none: Array[Texture2D] = []
    _fx._rain_light_frames = none.duplicate()
    _fx._rain_heavy_frames = none.duplicate()
    _fx._bolt_variants = none.duplicate()

    _fx.set_storm(true)
    _check("artless storm still raises the tint", _fx._tint_tween != null)
    _fx._advance_rain(1.0)
    _fx._flash()
    _check("artless storm still flashes", _fx._lightning_tween != null)
    _fx._strike()
    _check("artless storm draws no bolt", not _fx._bolt.visible)
    _fx.set_storm(false, false)
    _check("artless storm clears the flash", is_equal_approx(_fx._lightning.modulate.a, 0.0))

    _fx._rain_light_frames = light
    _fx._rain_heavy_frames = heavy
    _fx._bolt_variants = bolts
    _done()

## An instant clear (the connect/reconnect DTO sync path) must leave the rain
## hidden, not merely transparent — two full-screen tiled draws every frame
## under clear skies is the cost this guards.
func _test_rain_hidden_after_instant_clear() -> void:
    _fx.set_storm(true, false)
    _check("instant storm shows the rain", _fx._rain_light.visible and _fx._rain_heavy.visible)
    _fx.set_storm(false, false)
    _check("instant clear hides the rain", not _fx._rain_light.visible and not _fx._rain_heavy.visible)
    _check("instant clear hides the bolt", not _fx._bolt.visible)
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
    _fx._bolt_variants = _fx._slice_texture(_make_strip(BOLT_FRAME, 2), BOLT_FRAME, 2)

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

## Wind drift: the sheets must slide rightward at their two speeds (the shear
## between them is the depth cue), wrap invisibly at one tile, and keep the
## viewport covered at every phase — a sheet that drifts without the extra
## left-side tile opens a bare strip along the screen edge.
func _test_wind_drift_slides_and_wraps() -> void:
    var cam := _set_zoom(1.0)
    _fx._layout()
    _fx._rain_light_drift = 0.0
    _fx._rain_heavy_drift = 0.0

    _fx._advance_rain(0.5)
    _check("light layer drifts at its wind speed",
        is_equal_approx(_fx._rain_light_drift, 0.5 * _fx.RAIN_DRIFT_LIGHT_PPS))
    _check("heavy layer drifts slower — the layers shear",
        _fx._rain_heavy_drift < _fx._rain_light_drift)
    _check("sheet position follows the phase, one tile left of the edge",
        is_equal_approx(_fx._rain_light.position.x, (_fx._rain_light_drift - 32.0) * 2.0))

    for _i in 20:
        _fx._advance_rain(0.9)
    _check("light drift stays wrapped inside one tile",
        _fx._rain_light_drift >= 0.0 and _fx._rain_light_drift < 32.0)
    _check("heavy drift stays wrapped inside one tile",
        _fx._rain_heavy_drift >= 0.0 and _fx._rain_heavy_drift < 32.0)

    var view: Vector2 = _fx.get_viewport().get_visible_rect().size
    for phase in [0.0, 15.9, 31.9]:
        _fx._rain_light_drift = phase
        _fx._rain_heavy_drift = phase
        _fx._apply_rain_drift()
        for rect: TextureRect in [_fx._rain_heavy, _fx._rain_light]:
            _check("phase %s — sheet starts at or left of the screen edge" % phase,
                rect.position.x <= 0.0)
            _check("phase %s — sheet still reaches the right screen edge" % phase,
                rect.position.x + rect.size.x * rect.scale.x >= view.x)

    _fx._rain_light_drift = 0.0
    _fx._rain_heavy_drift = 0.0
    _fx._apply_rain_drift()
    _clear_zoom(cam)
    _done()

## The frame-timing counters must wrap even with no art loaded (CI's state) —
## unwrapped they accumulate for the life of the client and bleed float
## precision (code_review, LLM-632). Fails on the pre-fix code, where the
## fmod sat inside the frames-exist branch.
func _test_artless_counters_stay_wrapped() -> void:
    var light: Array[Texture2D] = _fx._rain_light_frames
    var heavy: Array[Texture2D] = _fx._rain_heavy_frames
    var none: Array[Texture2D] = []
    _fx._rain_light_frames = none
    _fx._rain_heavy_frames = none

    for _i in 50:
        _fx._advance_rain(0.4)
    _check("artless light counter stays wrapped",
        _fx._rain_light_elapsed < _fx.RAIN_LIGHT_FRAME_SECONDS)
    _check("artless heavy counter stays wrapped",
        _fx._rain_heavy_elapsed < _fx.RAIN_HEAVY_FRAME_SECONDS)

    _fx._rain_light_frames = light
    _fx._rain_heavy_frames = heavy
    _done()

## A delta spanning many frame intervals must advance the animation phase by
## ALL of them (modulo the loop), not one — a resumed background tab hands
## _process a large delta, and a one-frame advance leaves the displayed phase
## disagreeing with the wrapped counter. Needs synthetic frames present: the
## artless test above only checks the counter remainder and passes either way.
func _test_a_long_delta_advances_all_its_frames() -> void:
    var light: Array[Texture2D] = _fx._rain_light_frames
    var heavy: Array[Texture2D] = _fx._rain_heavy_frames
    _fx._rain_light_frames = _fx._slice_texture(_make_strip(FRAME, FRAME_COUNT), FRAME, FRAME_COUNT)
    _fx._rain_heavy_frames = _fx._slice_texture(_make_strip(FRAME, FRAME_COUNT), FRAME, FRAME_COUNT)
    _fx._rain_light_frame = 0
    _fx._rain_heavy_frame = 0
    _fx._rain_light_elapsed = 0.0
    _fx._rain_heavy_elapsed = 0.0

    # 1.0s = 20 light intervals (0.05) and 10 heavy (0.10): 20 % 8 = 4, 10 % 8 = 2.
    _fx._advance_rain(1.0)
    _check("light advanced by every interval the delta spans (20 %% 8)",
        _fx._rain_light_frame == 4)
    _check("heavy advanced by every interval the delta spans (10 %% 8)",
        _fx._rain_heavy_frame == 2)
    _check("light counter kept only the remainder", _fx._rain_light_elapsed < _fx.RAIN_LIGHT_FRAME_SECONDS)
    _check("heavy counter kept only the remainder", _fx._rain_heavy_elapsed < _fx.RAIN_HEAVY_FRAME_SECONDS)

    _fx._rain_light_frames = light
    _fx._rain_heavy_frames = heavy
    _fx._rain_light_frame = 0
    _fx._rain_heavy_frame = 0
    _done()
