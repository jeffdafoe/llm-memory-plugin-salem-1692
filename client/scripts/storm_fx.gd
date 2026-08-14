extends CanvasLayer
## Storm FX overlay (LLM-117 Half A; lightning art LLM-630). A full-screen,
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
## Rain is PARTICLES, lightning is ART, and that split is deliberate. LLM-628
## briefly drew the rain with the Mana Seed "weather effects" sheets too, and
## it was reverted: the sheet is one 32px tile repeated across the viewport, so
## a top-down village sits behind a full-screen sheet of art you look AT rather
## than through — visibly banded when zoomed out, and no amount of opacity,
## layering or tile-scattering changed that it covers the thing you are trying
## to watch. Thin procedural streaks read as weather without hiding the
## village. The bolts had the opposite outcome and stayed. Before reaching for
## the rain sheets again, note that three rounds of tuning did not save them.
##
## Renderer note: the project uses the gl_compatibility backend (web export),
## where GPUParticles2D is unreliable — so rain is CPUParticles2D.
##
## Mouse passthrough: every child uses MOUSE_FILTER_IGNORE so clicks fall
## through to the world underneath, exactly like the sleep-fade overlay.

## Storm tint — cool, desaturated slate. Alpha kept < 1.0 so the world bleeds
## through (darken, don't black out), matching the sleep-fade tone choice.
const TINT_COLOR: Color = Color(0.07, 0.09, 0.14, 1.0)
const TINT_TARGET_ALPHA: float = 0.5

## Tween durations — a storm rolls in / clears over a couple of seconds rather
## than snapping, so a forced storm (umbilical) still reads as weather, not a
## hard cut.
const FADE_IN_DURATION: float = 2.0
const FADE_OUT_DURATION: float = 2.0

## Rain look. Tuned for the compatibility renderer's textureless point
## particles: many small, fast, near-vertical light-blue streaks.
const RAIN_AMOUNT: int = 500
const RAIN_LIFETIME: float = 1.4
const RAIN_COLOR: Color = Color(0.70, 0.80, 0.95, 0.65)
const RAIN_VELOCITY_MIN: float = 800.0
const RAIN_VELOCITY_MAX: float = 1100.0

## Lightning — a brief white full-screen flash on a randomized interval while
## the storm is active. Kept sparse so it punctuates rather than strobes.
const LIGHTNING_INTERVAL_MIN: float = 8.0
const LIGHTNING_INTERVAL_MAX: float = 15.0
const LIGHTNING_PEAK_ALPHA: float = 0.55
const LIGHTNING_RISE: float = 0.06
const LIGHTNING_FALL: float = 0.45

## Bolt art: Seliel "weather effects 2.0". The sheet is 64x128 — two
## interchangeable 32x128 bolt variants side by side, not an animation.
const LIGHTNING_SHEET: String = "res://assets/tilesets/mana-seed/weather-effects/weather effects, lightning full 32x128.png"
const BOLT_FRAME_SIZE: Vector2i = Vector2i(32, 128)
const BOLT_VARIANT_COUNT: int = 2

## The bolt is on screen only for the flash's rise — a strike is over long
## before the sky finishes dimming. Alternating the two variants across that
## window is the artist's suggested way to give one bolt a live flicker.
const BOLT_FLICKER_SECONDS: float = 0.045

## The bolt is pixel art on the village's 16px grid, so it is drawn at the
## village's on-screen pixel size or it reads as a different resolution than
## the world: village assets render at render_scale 2.0 (world.gd
## _asset_render_scale) and the camera zooms 0.3-3.0, so one source pixel of
## the world covers 2.0 * camera.zoom.x screen pixels. The particles need none
## of this — a textureless point has no pixel size to mismatch.
const WORLD_RENDER_SCALE: float = 2.0

## ...but the bolt never shrinks below the village's unzoomed size: at the 0.3
## zoom floor a pixel-matched bolt is 77px tall and reads as a scratch rather
## than a strike. The sky is far enough away that a bolt slightly chunkier than
## the world at the zoomed-out extreme costs nothing.
const BOLT_MIN_SCALE: float = WORLD_RENDER_SCALE

## Set by main.gd at wiring time (same injection as actor_tooltip.camera).
## Null until then — _world_pixel_scale falls back to the unzoomed scale, and
## the first _layout after injection corrects it.
var camera: Camera2D = null

var _tint: ColorRect = null
var _lightning: ColorRect = null
var _bolt: TextureRect = null
var _rain: CPUParticles2D = null
var _lightning_timer: Timer = null
var _tint_tween: Tween = null
var _lightning_tween: Tween = null
var _bolt_tween: Tween = null
var _active: bool = false

var _bolt_variants: Array[Texture2D] = []

## Last bolt scale the layout was built for, so _process can notice a zoom
## change without relaying out every frame.
var _applied_bolt_scale: float = 0.0


func _ready() -> void:
    layer = 0

    _tint = _make_fullscreen_rect(TINT_COLOR)
    _tint.modulate = Color(1.0, 1.0, 1.0, 0.0)  # alpha tweened in/out
    add_child(_tint)

    _rain = CPUParticles2D.new()
    _configure_rain()
    add_child(_rain)

    _lightning = _make_fullscreen_rect(Color(1.0, 1.0, 1.0, 1.0))
    _lightning.modulate = Color(1.0, 1.0, 1.0, 0.0)
    add_child(_lightning)

    # Bolt goes ABOVE the white flash: the flash washes the sky out, and a bolt
    # underneath it would wash out with everything else. On top it stays crisp
    # against a blown-out sky, which is what a real strike looks like.
    _bolt_variants = _slice_sheet(LIGHTNING_SHEET, BOLT_FRAME_SIZE, BOLT_VARIANT_COUNT)
    _bolt = TextureRect.new()
    _bolt.mouse_filter = Control.MOUSE_FILTER_IGNORE
    # Size as well as scale: an unparented Control is laid out by nothing here,
    # and its size only reaches the texture's minimum on a later deferred pass
    # — so a bolt read or drawn in the same frame would have zero area.
    _bolt.size = Vector2(BOLT_FRAME_SIZE)
    _bolt.visible = false
    add_child(_bolt)

    _lightning_timer = Timer.new()
    _lightning_timer.one_shot = true
    _lightning_timer.timeout.connect(_on_lightning_timeout)
    add_child(_lightning_timer)

    # Keep the rain emitter sized/positioned to the viewport as the window
    # resizes (the rect anchors handle the ColorRects automatically).
    get_viewport().size_changed.connect(_layout)
    _layout()


## Raise (active=true) or clear (active=false) the storm. Idempotent — calling
## with the current state just refreshes the in-flight tween cleanly. tween
## false applies instantly (used by the connect/reconnect DTO sync so the
## scene doesn't flash clear before the first frame).
func set_storm(active: bool, tween: bool = true) -> void:
    _active = active
    _rain.emitting = active

    if active:
        # Re-layout on the way in: zoom or window size may have moved while the
        # sky was clear, and _process only tracks zoom while the storm runs.
        _layout()

    _kill_tint_tween()
    var target_alpha: float = TINT_TARGET_ALPHA if active else 0.0
    if not tween:
        _tint.modulate.a = target_alpha
    else:
        var duration: float = FADE_IN_DURATION if active else FADE_OUT_DURATION
        _tint_tween = create_tween()
        _tint_tween.tween_property(_tint, "modulate:a", target_alpha, duration)

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


func _process(_delta: float) -> void:
    if not _active:
        return
    # Zoom has no change signal, and polling one float is cheaper than wiring
    # one through camera.gd's several zoom paths (wheel, pinch, floor retune).
    if not is_equal_approx(_bolt_scale(), _applied_bolt_scale):
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


func _configure_rain() -> void:
    _rain.emitting = false
    _rain.amount = RAIN_AMOUNT
    _rain.lifetime = RAIN_LIFETIME
    _rain.preprocess = RAIN_LIFETIME  # start mid-fall so rain fills the screen at once
    _rain.local_coords = false
    _rain.direction = Vector2(0.12, 1.0)  # near-vertical, slight wind lean
    _rain.spread = 4.0
    _rain.gravity = Vector2(40.0, 400.0)
    _rain.initial_velocity_min = RAIN_VELOCITY_MIN
    _rain.initial_velocity_max = RAIN_VELOCITY_MAX
    _rain.scale_amount_min = 1.0
    _rain.scale_amount_max = 2.0
    _rain.color = RAIN_COLOR
    _rain.emission_shape = CPUParticles2D.EMISSION_SHAPE_RECTANGLE


## Load a sheet and cut it into frames. The art is a purchased pack and is
## gitignored (client/.gitignore), so a checkout without it is normal — CI runs
## that way. Returning empty rather than failing degrades the storm to rain,
## tint and flash instead of breaking the client.
func _slice_sheet(path: String, frame_size: Vector2i, count: int) -> Array[Texture2D]:
    var sheet: Texture2D = load(path) if ResourceLoader.exists(path) else null
    if sheet == null:
        push_warning("storm_fx: missing weather sheet " + path)
        return []
    return _slice_texture(sheet, frame_size, count)


## Cut a horizontal strip sheet into its frames as standalone textures.
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


func _bolt_scale() -> float:
    return maxf(_world_pixel_scale(), BOLT_MIN_SCALE)


## Size the rain emission rectangle to span the viewport width and sit just
## above the top edge, so particles fall in from off-screen across the full
## width regardless of window size; and scale the bolt to the village's pixel
## size for the current zoom.
func _layout() -> void:
    var bolt_scale: float = _bolt_scale()
    _applied_bolt_scale = bolt_scale
    var size: Vector2 = get_viewport().get_visible_rect().size
    if _rain != null:
        _rain.position = Vector2(size.x * 0.5, -20.0)
        _rain.emission_rect_extents = Vector2(size.x * 0.5 + 40.0, 8.0)
    if _bolt != null:
        _bolt.size = Vector2(BOLT_FRAME_SIZE)
        _bolt.scale = Vector2(bolt_scale, bolt_scale)


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
