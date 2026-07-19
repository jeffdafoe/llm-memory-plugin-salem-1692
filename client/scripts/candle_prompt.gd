extends CanvasLayer
## Candle prompt overlay (LLM-466). Full-screen, click-to-dismiss. The engine
## raises it when this client has gone an hour without any player input, at
## which point it has stopped counting as an audience and the village has begun
## pacing itself down (eco mode). A single click POSTs /pc/attend and buys
## another hour.
##
## Why a click and not a keypress: play mode stays mobile-portable, and a tap is
## the one input every client has. The whole screen is the target, so there is
## nothing to aim at on a phone.
##
## Layering: layer = 5, ABOVE the top bar and talk panel (layer >= 1) unlike
## sleep_fade (layer 0), because this one is modal — while the candle is up it
## owns every click. The ColorRect uses MOUSE_FILTER_STOP for the same reason:
## sleep_fade deliberately lets clicks fall through, but here a click that fell
## through would walk the PC across the village on the way to answering.
##
## Dismissal is NOT optimistic. The click POSTs and the overlay stays up until
## the engine broadcasts pc_idle_prompt_cleared — the same server-is-the-truth
## contract the sleep overlay keeps with its Wake button, and the reason the ack
## can be trusted as evidence a human is here.

## Emitted when the player clicks the overlay. main.gd POSTs /pc/attend; the
## overlay hides on the engine's cleared broadcast, not on this signal.
signal trimmed

## Guttering candlelight — warm and dark, distinct from sleep_fade's cool
## twilight so the two overlays never read as the same state. Alpha < 1.0 keeps
## the village faintly visible underneath: the point is that it is still there,
## just quieter.
const FADE_COLOR: Color = Color(0.08, 0.05, 0.02, 0.88)

## Slow enough to read as a candle dimming rather than a UI panel appearing.
const FADE_IN_DURATION: float = 1.2
const FADE_OUT_DURATION: float = 0.6

const PROMPT_LINE: String = "Thy candle gutters low."
const INSTRUCTION_LINE: String = "Touch anywhere to trim the wick."

## Warm candle-flame text on the dark tint.
const PROMPT_COLOR: Color = Color(0.96, 0.87, 0.66)
const INSTRUCTION_COLOR: Color = Color(0.78, 0.68, 0.52)

const PROMPT_FONT_SIZE: int = 34
const INSTRUCTION_FONT_SIZE: int = 20

var _rect: ColorRect = null
var _tween: Tween = null
var _shown: bool = false
## True once this raising of the prompt has been answered, until it is lowered.
## Two reasons: with mouse-from-touch emulation a single tap arrives as BOTH an
## InputEventScreenTouch and a synthesized InputEventMouseButton, and dismissal
## is server-driven, so without this an impatient player clicking through the
## round trip would fire a POST per click.
var _answered: bool = false


func _ready() -> void:
    layer = 5
    _rect = ColorRect.new()
    _rect.color = FADE_COLOR
    _rect.modulate = Color(1.0, 1.0, 1.0, 0.0)
    # STOP, not IGNORE: while the candle is up it swallows the click that
    # answers it, so dismissing the prompt never doubles as a move order.
    _rect.mouse_filter = Control.MOUSE_FILTER_STOP
    _rect.anchor_left = 0.0
    _rect.anchor_top = 0.0
    _rect.anchor_right = 1.0
    _rect.anchor_bottom = 1.0
    _rect.offset_left = 0.0
    _rect.offset_top = 0.0
    _rect.offset_right = 0.0
    _rect.offset_bottom = 0.0
    _rect.visible = false
    _rect.gui_input.connect(_on_rect_gui_input)
    add_child(_rect)

    var box := VBoxContainer.new()
    box.alignment = BoxContainer.ALIGNMENT_CENTER
    box.add_theme_constant_override("separation", 18)
    # The label stack must not eat the click — only the ColorRect handles input.
    box.mouse_filter = Control.MOUSE_FILTER_IGNORE
    box.anchor_left = 0.0
    box.anchor_top = 0.0
    box.anchor_right = 1.0
    box.anchor_bottom = 1.0
    box.offset_left = 0.0
    box.offset_top = 0.0
    box.offset_right = 0.0
    box.offset_bottom = 0.0
    _rect.add_child(box)

    box.add_child(_build_label(PROMPT_LINE, PROMPT_FONT_SIZE, PROMPT_COLOR))
    box.add_child(_build_label(INSTRUCTION_LINE, INSTRUCTION_FONT_SIZE, INSTRUCTION_COLOR))


func _build_label(text: String, font_size: int, color: Color) -> Label:
    var label := Label.new()
    label.text = text
    label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    label.add_theme_font_size_override("font_size", font_size)
    label.add_theme_color_override("font_color", color)
    # A dark outline keeps the line legible wherever the village underneath
    # happens to be bright (a lit tavern interior, daytime open ground).
    label.add_theme_color_override("font_shadow_color", Color(0.0, 0.0, 0.0, 0.7))
    label.add_theme_constant_override("shadow_offset_x", 1)
    label.add_theme_constant_override("shadow_offset_y", 1)
    return label


## Raise the candle. Called from main.gd on pc_idle_prompt for the local PC.
## Idempotent — re-firing while it is already up resets the in-flight tween.
func show_prompt() -> void:
    _shown = true
    _answered = false
    _rect.visible = true
    _kill_tween()
    _tween = create_tween()
    _tween.tween_property(_rect, "modulate:a", 1.0, FADE_IN_DURATION)


## Lower the candle. Called on pc_idle_prompt_cleared for the local PC —
## whether it was this client's click or an in-world action that cleared it.
## Hides the rect only after the fade so it stops swallowing clicks at the
## moment it stops being visible, not before.
func hide_prompt() -> void:
    if not _shown:
        return
    _shown = false
    _answered = false
    _kill_tween()
    _tween = create_tween()
    _tween.tween_property(_rect, "modulate:a", 0.0, FADE_OUT_DURATION)
    _tween.tween_callback(func() -> void: _rect.visible = false)


func is_showing() -> bool:
    return _shown


func _on_rect_gui_input(event: InputEvent) -> void:
    if not _shown or _answered:
        return
    if not _is_release(event):
        return
    _answered = true
    _rect.accept_event()
    trimmed.emit()


## True for the release half of a click or a tap. Release rather than press,
## matching the client's click-to-walk semantics — and so a click already begun
## when the candle rose can't answer it.
##
## Both event types are handled deliberately. A native touch produces
## InputEventScreenTouch; mouse-from-touch emulation (on by default) ALSO
## synthesizes an InputEventMouseButton. Handling only the mouse half would make
## the prompt undismissable on any client where that default is off, which is a
## dead end for a player — the overlay is modal. _answered absorbs the duplicate
## when both arrive.
func _is_release(event: InputEvent) -> bool:
    if event is InputEventMouseButton:
        return event.button_index == MOUSE_BUTTON_LEFT and not event.pressed
    if event is InputEventScreenTouch:
        return not event.pressed
    return false


func _kill_tween() -> void:
    if _tween != null and _tween.is_valid():
        _tween.kill()
    _tween = null
