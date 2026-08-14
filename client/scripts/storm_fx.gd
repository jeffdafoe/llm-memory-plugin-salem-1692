extends CanvasLayer
## Storm FX overlay (LLM-117 Half A; art pass LLM-628). A full-screen,
## screen-space weather layer that raises rain + a cool darkening tint +
## occasional lightning when the world weather is "storm", and tweens cleanly
## back out on "clear". Driven from world.gd set_weather (WS weather_changed
## frame + the /api/village/world DTO on connect/reconnect).
##
## Layering: sits on a dedicated CanvasLayer with layer=0 — same posture as
## sleep_fade.gd — so it paints over the world (the default Node2D layer 0,
## last-child-wins) but UNDER the editor / talk_panel / login layers
## (layer >= 1). UI panels stay bright; only the world darkens.
##
## Composition with day/night: the tint is a translucent ColorRect painted
## OVER whatever the phase CanvasModulate already produced — it does NOT
## touch world.gd's phase color. So a day-storm darkens from bright and a
## night-storm darkens further from the already-cold night tint, both
## reading correctly, without two systems fighting over one CanvasModulate.
##
## Art: Seliel "weather effects 2.0" (assets/tilesets/mana-seed/
## weather-effects/), the same artist and resolution as the rest of the
## village. Rain is the two 8-frame sheets tiled across the screen, light
## over heavy at the artist's own cadences. Lightning is a bolt sprite drawn
## ON TOP of the full-screen white flash — the flash is the part that sells
## it, so the bolt adds to it rather than replacing it.
##
## Mouse passthrough: every child uses MOUSE_FILTER_IGNORE so clicks fall
## through to the world underneath, exactly like the sleep-fade overlay.

## Storm tint — cool, desaturated slate. Alpha kept < 1.0 so the world bleeds
## through (darken, don't black out), matching the sleep-fade tone choice.
const TINT_COLOR: Color = Color(0.07, 0.09, 0.14, 1.0)
const TINT_TARGET_ALPHA: float = 0.5

## Rain opacity at full storm. Separate from the tint's alpha even though they
## currently match — they are two different judgements (how dark the sky goes
## vs. how heavily it rains), and sharing one constant means retuning the gloom
## silently retunes the downpour.
const RAIN_TARGET_ALPHA: float = 0.5

## Tween durations — a storm rolls in / clears over a couple of seconds rather
## than snapping, so a forced storm (umbilical) still reads as weather, not a
## hard cut.
const FADE_IN_DURATION: float = 2.0
const FADE_OUT_DURATION: float = 2.0

const WEATHER_DIR: String = "res://assets/tilesets/mana-seed/weather-effects/"
const RAIN_LIGHT_SHEET: String = WEATHER_DIR + "weather effects, rain light anim 32x128.png"
const RAIN_HEAVY_SHEET: String = WEATHER_DIR + "weather effects, rain heavy anim 32x128.png"
const LIGHTNING_SHEET: String = WEATHER_DIR + "weather effects, lightning full 32x128.png"

## Both rain sheets are 256x128 — 8 frames of one 32x128 tile, built to tile
## seamlessly in both axes. The bolt sheet is 64x128: two interchangeable
## 32x128 bolt variants side by side, not an animation.
const RAIN_FRAME_SIZE: Vector2i = Vector2i(32, 128)
const RAIN_FRAME_COUNT: int = 8
const BOLT_FRAME_SIZE: Vector2i = Vector2i(32, 128)
const BOLT_VARIANT_COUNT: int = 2

## Per-frame hold times from the pack's own "using these effects.txt" — the
## two rain layers must run at DIFFERENT speeds or they beat against each
## other and read as one flat sheet instead of depth.
const RAIN_LIGHT_FRAME_SECONDS: float = 0.05
const RAIN_HEAVY_FRAME_SECONDS: float = 0.10

## Wind drift (LLM-632 follow-up). The frame animation moves the droplets but
## the tiled sheet itself is pinned, so without translation the rain reads as
## a static curtain hanging in front of the village. A slow rightward slide of
## the whole sheet is the missing wind. Speeds are in SOURCE pixels/second
## (they scale with the on-screen pixel size like everything else), light
## faster than heavy so the two layers shear against each other — near sheet
## moves more than far sheet, which is what gives the curtain depth. One tile
## is 32 source px, so the light layer crosses a tile in ~3s.
const RAIN_DRIFT_LIGHT_PPS: float = 10.0
const RAIN_DRIFT_HEAVY_PPS: float = 6.0

## Village art is drawn at render_scale 2.0 by default (world.gd
## _asset_render_scale), so one source pixel of village art covers
## 2.0 * camera.zoom.x screen pixels. The weather art is pixel art at the same
## 16px grid, so it has to be drawn at that same size or it reads as a
## different resolution laid over the village — thin sharp rain over a chunky
## world when zoomed in, and the reverse when zoomed out. The old procedural
## particles had no pixel size, which is why nothing mismatched before.
const WORLD_RENDER_SCALE: float = 2.0

## Lightning — a brief white full-screen flash on a randomized interval while
## the storm is active. Kept sparse so it punctuates rather than strobes.
const LIGHTNING_INTERVAL_MIN: float = 8.0
const LIGHTNING_INTERVAL_MAX: float = 15.0
const LIGHTNING_PEAK_ALPHA: float = 0.55
const LIGHTNING_RISE: float = 0.06
const LIGHTNING_FALL: float = 0.45

## The bolt is on screen only for the flash's rise — a strike is over long
## before the sky finishes dimming. Alternating the two variants across that
## window is the artist's suggested way to give one bolt a live flicker.
const BOLT_FLICKER_SECONDS: float = 0.045

## The bolt tracks the world pixel scale like the rain, but never shrinks below
## the village's own unzoomed size: at the 0.3 zoom floor a pixel-matched bolt
## is 77px tall and reads as a scratch rather than a strike. Rain has no such
## floor — it tiles the whole screen, so it stays legible however small the
## drops get. The sky is far enough away that a bolt slightly chunkier than the
## world at the zoomed-out extreme costs nothing.
const BOLT_MIN_SCALE: float = WORLD_RENDER_SCALE

## Set by main.gd at wiring time (same injection as actor_tooltip.camera).
## Null until then — _world_pixel_scale falls back to the unzoomed scale, and
## the first _layout after injection corrects it.
var camera: Camera2D = null

var _tint: ColorRect = null
var _lightning: ColorRect = null
var _bolt: TextureRect = null
var _rain_light: TextureRect = null
var _rain_heavy: TextureRect = null
var _lightning_timer: Timer = null
var _tint_tween: Tween = null
var _lightning_tween: Tween = null
var _bolt_tween: Tween = null
var _active: bool = false

var _rain_light_frames: Array[Texture2D] = []
var _rain_heavy_frames: Array[Texture2D] = []
var _bolt_variants: Array[Texture2D] = []
var _rain_light_frame: int = 0
var _rain_heavy_frame: int = 0
var _rain_light_elapsed: float = 0.0
var _rain_heavy_elapsed: float = 0.0

## Wind drift phase per layer, in source pixels, wrapped to one 32px tile —
## the sheets tile seamlessly, so a full tile of travel lands back on the
## identical image and the phase never has to grow.
var _rain_light_drift: float = 0.0
var _rain_heavy_drift: float = 0.0

## The snapped scale the sheets are CURRENTLY drawn at — set only by _layout.
## Drift positioning must use this, not a fresh _rain_scale(): between a zoom
## change and the next layout the two disagree, and moving the sheet at a
## scale it isn't drawn at reopens the coverage gap at the screen edge.
var _applied_rain_scale: float = 1.0

## Last pixel scale the layout was built for, so _process can notice a zoom
## change without relaying out every frame.
var _applied_scale: float = 0.0


func _ready() -> void:
    layer = 0

    _tint = _make_fullscreen_rect(TINT_COLOR)
    _tint.modulate = Color(1.0, 1.0, 1.0, 0.0)  # alpha tweened in/out
    add_child(_tint)

    # Heavy behind light — the artist's layering. Child order is draw order.
    _rain_heavy_frames = _slice_sheet(RAIN_HEAVY_SHEET, RAIN_FRAME_SIZE, RAIN_FRAME_COUNT)
    _rain_light_frames = _slice_sheet(RAIN_LIGHT_SHEET, RAIN_FRAME_SIZE, RAIN_FRAME_COUNT)
    _bolt_variants = _slice_sheet(LIGHTNING_SHEET, BOLT_FRAME_SIZE, BOLT_VARIANT_COUNT)

    _rain_heavy = _make_rain_rect(_rain_heavy_frames)
    add_child(_rain_heavy)
    _rain_light = _make_rain_rect(_rain_light_frames)
    add_child(_rain_light)

    _lightning = _make_fullscreen_rect(Color(1.0, 1.0, 1.0, 1.0))
    _lightning.modulate = Color(1.0, 1.0, 1.0, 0.0)
    add_child(_lightning)

    # Bolt goes ABOVE the white flash: the flash washes the sky out, and a bolt
    # underneath it would wash out with everything else. On top it stays crisp
    # against a blown-out sky, which is what a real strike looks like.
    _bolt = TextureRect.new()
    _bolt.mouse_filter = Control.MOUSE_FILTER_IGNORE
    _bolt.size = Vector2(BOLT_FRAME_SIZE)
    _bolt.visible = false
    add_child(_bolt)

    _lightning_timer = Timer.new()
    _lightning_timer.one_shot = true
    _lightning_timer.timeout.connect(_on_lightning_timeout)
    add_child(_lightning_timer)

    get_viewport().size_changed.connect(_layout)
    _layout()


## Raise (active=true) or clear (active=false) the storm. Idempotent — calling
## with the current state just refreshes the in-flight tween cleanly. tween
## false applies instantly (used by the connect/reconnect DTO sync so the
## scene doesn't flash clear before the first frame).
func set_storm(active: bool, tween: bool = true) -> void:
    _active = active

    if active:
        # Re-layout on the way in: zoom or window size may have moved while the
        # sky was clear, and _process only tracks zoom while the storm runs.
        _layout()
        _rain_heavy.visible = true
        _rain_light.visible = true

    _kill_tint_tween()
    var tint_alpha: float = TINT_TARGET_ALPHA if active else 0.0
    var rain_alpha: float = RAIN_TARGET_ALPHA if active else 0.0
    if not tween:
        _tint.modulate.a = tint_alpha
        _rain_heavy.modulate.a = rain_alpha
        _rain_light.modulate.a = rain_alpha
        if not active:
            _hide_rain()
    else:
        var duration: float = FADE_IN_DURATION if active else FADE_OUT_DURATION
        _tint_tween = create_tween()
        _tint_tween.set_parallel(true)
        _tint_tween.tween_property(_tint, "modulate:a", tint_alpha, duration)
        _tint_tween.tween_property(_rain_heavy, "modulate:a", rain_alpha, duration)
        _tint_tween.tween_property(_rain_light, "modulate:a", rain_alpha, duration)
        if not active:
            # Stop paying for two full-screen tiled draws once the rain is gone.
            _tint_tween.chain().tween_callback(_hide_rain)

    if active:
        _schedule_lightning()
    else:
        _lightning_timer.stop()
        # Kill an in-flight flash before forcing dark — otherwise a tween that
        # was mid-rise when the storm cleared keeps running and flashes the
        # screen white after the storm is gone. Same for the bolt.
        _kill_lightning_tween()
        _lightning.modulate.a = 0.0
        _kill_bolt_tween()
        _bolt.visible = false


func _process(delta: float) -> void:
    if not _active:
        return
    _advance_rain(delta)
    # Zoom has no change signal, and polling one float is cheaper than wiring
    # one through camera.gd's several zoom paths (wheel, pinch, floor retune).
    if not is_equal_approx(_world_pixel_scale(), _applied_scale):
        _layout()


func _make_fullscreen_rect(color: Color) -> ColorRect:
    var rect := ColorRect.new()
    rect.color = color
    rect.mouse_filter = Control.MOUSE_FILTER_IGNORE
    rect.anchor_left = 0.0
    rect.anchor_top = 0.0
    rect.anchor_right = 1.0
    rect.anchor_bottom = 1.0
    rect.offset_left = 0.0
    rect.offset_top = 0.0
    rect.offset_right = 0.0
    rect.offset_bottom = 0.0
    return rect


## One rain layer: a tiled TextureRect sized by _layout. Anchors are left at
## the default top-left corner on purpose — a full-rect anchor would pin size
## to the viewport and fight the scaling _layout does.
func _make_rain_rect(frames: Array[Texture2D]) -> TextureRect:
    var rect := TextureRect.new()
    rect.mouse_filter = Control.MOUSE_FILTER_IGNORE
    rect.stretch_mode = TextureRect.STRETCH_TILE
    # The project default repeat is Mirror (project.godot
    # default_texture_repeat=2), which flips every other tile and leaves the
    # rain leaning both ways in alternating columns. Tiling rain needs plain
    # repeat.
    rect.texture_repeat = CanvasItem.TEXTURE_REPEAT_ENABLED
    rect.visible = false
    rect.modulate = Color(1.0, 1.0, 1.0, 0.0)
    if not frames.is_empty():
        rect.texture = frames[0]
    return rect


## Load a weather sheet and cut it into frames. The art is a purchased pack and
## is gitignored (client/.gitignore), so a checkout without it is normal — CI
## runs that way. Returning empty rather than failing degrades the storm to
## tint + flash instead of breaking the client.
func _slice_sheet(path: String, frame_size: Vector2i, count: int) -> Array[Texture2D]:
    var sheet: Texture2D = load(path) if ResourceLoader.exists(path) else null
    if sheet == null:
        push_warning("storm_fx: missing weather sheet " + path)
        return []
    return _slice_texture(sheet, frame_size, count)


## Cut a horizontal strip sheet into its frames as standalone textures.
## AtlasTexture would be the obvious choice, but it does not tile reliably
## under STRETCH_TILE — separate textures do.
func _slice_texture(sheet: Texture2D, frame_size: Vector2i, count: int) -> Array[Texture2D]:
    var frames: Array[Texture2D] = []
    var image: Image = sheet.get_image()
    if image == null:
        push_warning("storm_fx: unreadable weather sheet")
        return frames
    for i in count:
        var region := Rect2i(Vector2i(i * frame_size.x, 0), frame_size)
        if not Rect2i(Vector2i.ZERO, image.get_size()).encloses(region):
            push_warning("storm_fx: weather sheet too small for frame %d" % i)
            break
        frames.append(ImageTexture.create_from_image(image.get_region(region)))
    return frames


## On-screen size of one source pixel of village art. See WORLD_RENDER_SCALE.
func _world_pixel_scale() -> float:
    if camera == null:
        return WORLD_RENDER_SCALE
    return WORLD_RENDER_SCALE * camera.zoom.x


## The rain's draw scale: the village pixel size snapped to a whole number.
## Nearest-neighbour sampling at a fractional scale gives some source pixels
## one screen pixel and some two, and on a repeating field of identical 1px
## drops that quantization lines up into visible columns of fatter/longer
## drops. Worse, the wind drift wraps at the 32px TEXTURE period while the
## fat-column beat has its own incommensurate period, so at every wrap the
## bands jump back — they read as caught in place while the rain slides. An
## integer scale has no fractional sampling, so there is no beat to see; the
## cost is rain slightly chunkier or finer than the world between integer
## zoom products, which the bolt already accepts with its own floor.
func _rain_scale() -> float:
    return maxf(1.0, roundf(_world_pixel_scale()))


## Size and scale the rain layers to cover the viewport with tiles drawn at
## the rain's snapped pixel size (see _rain_scale). A Control's size is in
## pre-scale units, so the coverage divides by the scale as the tiles grow;
## one extra tile of slack absorbs the fractional remainder at the right and
## bottom edges.
func _layout() -> void:
    var pixel_scale: float = _world_pixel_scale()
    _applied_scale = pixel_scale
    if _rain_heavy == null or _rain_light == null or _bolt == null:
        return
    _applied_rain_scale = _rain_scale()
    var view: Vector2 = get_viewport().get_visible_rect().size
    # One tile of slack for the fractional remainder at the right and bottom,
    # plus a second horizontal tile because the wind drift starts the sheet up
    # to one tile left of the viewport edge.
    var covered: Vector2 = view / _applied_rain_scale + Vector2(RAIN_FRAME_SIZE) + Vector2(RAIN_FRAME_SIZE.x, 0.0)
    for rect: TextureRect in [_rain_heavy, _rain_light]:
        rect.scale = Vector2(_applied_rain_scale, _applied_rain_scale)
        rect.size = covered
    _apply_rain_drift()
    # Size as well as scale: an unparented Control is not laid out by anything
    # here, and its size only reaches the texture's minimum on a later deferred
    # pass — so a bolt read or drawn in the same frame would have zero area.
    var bolt_scale: float = _bolt_scale()
    _bolt.size = Vector2(BOLT_FRAME_SIZE)
    _bolt.scale = Vector2(bolt_scale, bolt_scale)


func _bolt_scale() -> float:
    return maxf(_world_pixel_scale(), BOLT_MIN_SCALE)


func _advance_rain(delta: float) -> void:
    # Wrap the elapsed counters whether or not frames exist — on an artless
    # checkout they would otherwise accumulate for the life of the client and
    # bleed float precision (code_review, LLM-632).
    # Advance by ALL the intervals the delta spans, not one — a slow frame or
    # a resumed background tab hands a delta worth many frames, and advancing
    # one leaves the displayed phase behind the wrapped counter.
    _rain_light_elapsed += delta
    if _rain_light_elapsed >= RAIN_LIGHT_FRAME_SECONDS:
        var light_steps: int = floori(_rain_light_elapsed / RAIN_LIGHT_FRAME_SECONDS)
        _rain_light_elapsed = fmod(_rain_light_elapsed, RAIN_LIGHT_FRAME_SECONDS)
        if not _rain_light_frames.is_empty():
            _rain_light_frame = (_rain_light_frame + light_steps) % _rain_light_frames.size()
            _rain_light.texture = _rain_light_frames[_rain_light_frame]

    _rain_heavy_elapsed += delta
    if _rain_heavy_elapsed >= RAIN_HEAVY_FRAME_SECONDS:
        var heavy_steps: int = floori(_rain_heavy_elapsed / RAIN_HEAVY_FRAME_SECONDS)
        _rain_heavy_elapsed = fmod(_rain_heavy_elapsed, RAIN_HEAVY_FRAME_SECONDS)
        if not _rain_heavy_frames.is_empty():
            _rain_heavy_frame = (_rain_heavy_frame + heavy_steps) % _rain_heavy_frames.size()
            _rain_heavy.texture = _rain_heavy_frames[_rain_heavy_frame]

    # Wind: slide each sheet rightward, wrapping at one tile — the art tiles
    # seamlessly, so the wrap is invisible and the phase stays tiny.
    _rain_light_drift = fmod(_rain_light_drift + delta * RAIN_DRIFT_LIGHT_PPS, float(RAIN_FRAME_SIZE.x))
    _rain_heavy_drift = fmod(_rain_heavy_drift + delta * RAIN_DRIFT_HEAVY_PPS, float(RAIN_FRAME_SIZE.x))
    _apply_rain_drift()


## Position each sheet one tile left of the viewport edge plus its current
## drift phase, in screen units at the scale the sheets are DRAWN at
## (_applied_rain_scale — never a fresh _rain_scale(), see its comment).
## Called from _advance_rain every frame and from _layout so a re-layout
## doesn't snap the sheets back to phase zero.
func _apply_rain_drift() -> void:
    var tile: float = float(RAIN_FRAME_SIZE.x)
    _rain_light.position.x = (_rain_light_drift - tile) * _applied_rain_scale
    _rain_heavy.position.x = (_rain_heavy_drift - tile) * _applied_rain_scale


func _hide_rain() -> void:
    _rain_heavy.visible = false
    _rain_light.visible = false


func _schedule_lightning() -> void:
    _lightning_timer.start(randf_range(LIGHTNING_INTERVAL_MIN, LIGHTNING_INTERVAL_MAX))


func _on_lightning_timeout() -> void:
    if not _active:
        return
    _flash()
    _strike()
    _schedule_lightning()


## A quick rise-and-fall of the white overlay's alpha — a lightning flash.
## Tracked so set_storm(false) can kill it mid-flash (otherwise it would flash
## after the storm cleared).
func _flash() -> void:
    _kill_lightning_tween()
    _lightning_tween = create_tween()
    _lightning_tween.tween_property(_lightning, "modulate:a", LIGHTNING_PEAK_ALPHA, LIGHTNING_RISE)
    _lightning_tween.tween_property(_lightning, "modulate:a", 0.0, LIGHTNING_FALL)


## Drop a bolt somewhere across the sky for the length of the flash's rise,
## alternating the two variants once so the strike flickers rather than sitting
## there as a still image.
func _strike() -> void:
    if _bolt_variants.size() < BOLT_VARIANT_COUNT:
        return
    _kill_bolt_tween()

    var view: Vector2 = get_viewport().get_visible_rect().size
    var bolt_width: float = float(BOLT_FRAME_SIZE.x) * _bolt_scale()
    _bolt.position = Vector2(randf_range(0.0, maxf(0.0, view.x - bolt_width)), 0.0)

    var first: int = randi() % BOLT_VARIANT_COUNT
    var second: int = (first + 1) % BOLT_VARIANT_COUNT
    _bolt.texture = _bolt_variants[first]
    _bolt.visible = true

    _bolt_tween = create_tween()
    _bolt_tween.tween_interval(BOLT_FLICKER_SECONDS)
    _bolt_tween.tween_callback(func() -> void: _bolt.texture = _bolt_variants[second])
    _bolt_tween.tween_interval(BOLT_FLICKER_SECONDS)
    _bolt_tween.tween_callback(func() -> void: _bolt.visible = false)


func _kill_tint_tween() -> void:
    if _tint_tween != null and _tint_tween.is_valid():
        _tint_tween.kill()
    _tint_tween = null


func _kill_lightning_tween() -> void:
    if _lightning_tween != null and _lightning_tween.is_valid():
        _lightning_tween.kill()
    _lightning_tween = null


func _kill_bolt_tween() -> void:
    if _bolt_tween != null and _bolt_tween.is_valid():
        _bolt_tween.kill()
    _bolt_tween = null
