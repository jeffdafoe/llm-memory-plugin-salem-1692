extends CanvasLayer
## Storm FX overlay (LLM-117 Half A). A full-screen, screen-space weather
## layer that raises rain + a cool darkening tint + occasional lightning
## flashes when the world weather is "storm", and tweens cleanly back out on
## "clear". Driven from world.gd set_weather (WS weather_changed frame +
## the /api/village/world DTO on connect/reconnect).
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

var _tint: ColorRect = null
var _lightning: ColorRect = null
var _rain: CPUParticles2D = null
var _lightning_timer: Timer = null
var _tint_tween: Tween = null
var _lightning_tween: Tween = null
var _active: bool = false


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

    _lightning_timer = Timer.new()
    _lightning_timer.one_shot = true
    _lightning_timer.timeout.connect(_on_lightning_timeout)
    add_child(_lightning_timer)

    # Keep the rain emitter sized/positioned to the viewport as the window
    # resizes (the rect anchors handle the ColorRects automatically).
    get_viewport().size_changed.connect(_layout_rain)
    _layout_rain()


## Raise (active=true) or clear (active=false) the storm. Idempotent — calling
## with the current state just refreshes the in-flight tween cleanly. tween
## false applies instantly (used by the connect/reconnect DTO sync so the
## scene doesn't flash clear before the first frame).
func set_storm(active: bool, tween: bool = true) -> void:
    _active = active
    _rain.emitting = active

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
        # screen white after the storm is gone.
        _kill_lightning_tween()
        _lightning.modulate.a = 0.0


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


## Size the rain emission rectangle to span the viewport width and sit just
## above the top edge, so particles fall in from off-screen across the full
## width regardless of window size.
func _layout_rain() -> void:
    if _rain == null:
        return
    var size: Vector2 = get_viewport().get_visible_rect().size
    _rain.position = Vector2(size.x * 0.5, -20.0)
    _rain.emission_rect_extents = Vector2(size.x * 0.5 + 40.0, 8.0)


func _schedule_lightning() -> void:
    _lightning_timer.start(randf_range(LIGHTNING_INTERVAL_MIN, LIGHTNING_INTERVAL_MAX))


func _on_lightning_timeout() -> void:
    if not _active:
        return
    _flash()
    _schedule_lightning()


## A quick rise-and-fall of the white overlay's alpha — a lightning flash.
## Tracked so set_storm(false) can kill it mid-flash (otherwise it would flash
## after the storm cleared).
func _flash() -> void:
    _kill_lightning_tween()
    _lightning_tween = create_tween()
    _lightning_tween.tween_property(_lightning, "modulate:a", LIGHTNING_PEAK_ALPHA, LIGHTNING_RISE)
    _lightning_tween.tween_property(_lightning, "modulate:a", 0.0, LIGHTNING_FALL)


func _kill_tint_tween() -> void:
    if _tint_tween != null and _tint_tween.is_valid():
        _tint_tween.kill()
    _tint_tween = null


func _kill_lightning_tween() -> void:
    if _lightning_tween != null and _lightning_tween.is_valid():
        _lightning_tween.kill()
    _lightning_tween = null
