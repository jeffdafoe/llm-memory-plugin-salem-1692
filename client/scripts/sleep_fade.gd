extends CanvasLayer
## Sleep-fade overlay (ZBBS-WORK-204 Stage B). A full-screen ColorRect
## that tweens to a deep twilight tint while the local PC is sleeping
## and back to transparent on wake. Sits on a dedicated CanvasLayer
## with layer=0 so it paints over the world (default Node2D layer 0,
## last-child-wins) but under the editor / talk_panel / login layers
## (layer >= 1), letting the top bar stay readable for the wake button.
##
## Mouse passthrough: the ColorRect uses MOUSE_FILTER_IGNORE so clicks
## fall through to whatever's underneath (mostly the world, which
## ignores them while sleeping anyway). The wake button in the top
## bar is on a higher CanvasLayer and remains clickable regardless.
##
## Tone: a desaturated cool blue (0.05, 0.05, 0.10) at ~0.85 alpha
## reads as twilight without going fully black. The world is still
## faintly visible underneath — better than a hard cut to black
## because it preserves the player's spatial sense of where they
## bedded down.

## Fade durations chosen to feel like settling into sleep / waking up
## rather than instant cuts. 1.5s sleep-in is gentle; 1.0s wake-out is
## slightly snappier so the player's first input doesn't sit behind a
## long fade.
const FADE_IN_DURATION: float = 1.5
const FADE_OUT_DURATION: float = 1.0

## Twilight color for the fade. Cool deep blue with slight purple
## bias — reads as nighttime without going pure black, leaves the
## world faintly visible. Tunable; keep alpha < 1.0 so the world
## bleeds through.
const FADE_COLOR: Color = Color(0.05, 0.05, 0.10, 0.85)

var _rect: ColorRect = null
var _tween: Tween = null


func _ready() -> void:
    layer = 0
    _rect = ColorRect.new()
    _rect.color = FADE_COLOR
    _rect.modulate = Color(1.0, 1.0, 1.0, 0.0)
    _rect.mouse_filter = Control.MOUSE_FILTER_IGNORE
    _rect.anchor_left = 0.0
    _rect.anchor_top = 0.0
    _rect.anchor_right = 1.0
    _rect.anchor_bottom = 1.0
    _rect.offset_left = 0.0
    _rect.offset_top = 0.0
    _rect.offset_right = 0.0
    _rect.offset_bottom = 0.0
    add_child(_rect)


## Fade in to twilight. Called from main.gd on pc_sleep_started for
## the local PC. Idempotent — re-firing while the fade is already
## up just resets the in-flight tween cleanly.
func fade_to_sleep() -> void:
    _kill_tween()
    _tween = create_tween()
    _tween.tween_property(_rect, "modulate:a", 1.0, FADE_IN_DURATION)


## Fade back to transparent. Called on pc_sleep_ended for the local
## PC. Idempotent.
func fade_to_awake() -> void:
    _kill_tween()
    _tween = create_tween()
    _tween.tween_property(_rect, "modulate:a", 0.0, FADE_OUT_DURATION)


func _kill_tween() -> void:
    if _tween != null and _tween.is_valid():
        _tween.kill()
    _tween = null
