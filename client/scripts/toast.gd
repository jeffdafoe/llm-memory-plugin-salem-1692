extends CanvasLayer
## Autoloaded singleton — the admin UI's transient user-facing message surface
## (LLM-250). Stacks short-lived banners at the top of the screen; each fades out
## on its own, and a click dismisses it early.
##
## This is NOT ErrorBeacon. ErrorBeacon POSTs runtime failures to the engine for
## an operator to read later; nothing about it ever reaches the person at the
## keyboard. Toast is the opposite direction: it tells the ADMIN that the thing
## they just did failed. Before it existed, the admin routes' HTTPRequest
## callbacks checked only Auth.check_response (401) and dropped every other
## non-2xx on the floor — a rejected NPC work/home assignment looked exactly like
## a successful one (see world.gd _post_npc_admin).
##
## Named "toast" rather than "notice" deliberately: notice_panel / notice_panel_layer
## already mean the in-world NOTICEBOARD in this client, and reusing the word for
## an unrelated UI surface would be a reading hazard.
##
## Autoloaded as a CanvasLayer so any script can call Toast.error(...) without a
## node path, and the banners paint above every other layer (login_layer is 10).

## Above every other CanvasLayer in the client — a failure message the admin needs
## to read must never render behind the editor panel or the login screen.
const LAYER_INDEX: int = 128

## How long a banner stays fully opaque before it begins to fade. Long enough to
## read a sentence, short enough that a burst of failures doesn't wall off the map.
const HOLD_SECONDS: float = 6.0
const FADE_SECONDS: float = 0.6

## Oldest banners are evicted past this many. A tight failure loop (one toast per
## rejected PATCH) must not grow an unbounded column down the screen.
const MAX_VISIBLE: int = 4

const TOAST_WIDTH: float = 460.0
## Clears the top bar so a banner never covers the clock / wake button.
const TOP_MARGIN: float = 64.0

const COLOR_TEXT := Color(0.96, 0.96, 0.96)
const COLOR_ERROR_BG := Color(0.42, 0.11, 0.11, 0.96)
const COLOR_ERROR_BORDER := Color(0.85, 0.28, 0.28)
const COLOR_WARNING_BG := Color(0.40, 0.30, 0.06, 0.96)
const COLOR_WARNING_BORDER := Color(0.90, 0.68, 0.20)

var _stack: VBoxContainer = null

func _ready() -> void:
    layer = LAYER_INDEX
    _stack = VBoxContainer.new()
    _stack.name = "ToastStack"
    # Anchor to top-centre and grow both ways horizontally so the column stays
    # centred as banners of different widths come and go.
    _stack.anchor_left = 0.5
    _stack.anchor_right = 0.5
    _stack.grow_horizontal = Control.GROW_DIRECTION_BOTH
    _stack.grow_vertical = Control.GROW_DIRECTION_END
    _stack.offset_top = TOP_MARGIN
    _stack.add_theme_constant_override("separation", 8)
    # The container itself must never eat clicks aimed at the map behind it; only
    # the individual banners are interactive (click-to-dismiss).
    _stack.mouse_filter = Control.MOUSE_FILTER_IGNORE
    add_child(_stack)

## Report a failed action to the admin — a rejected PATCH, a refused command.
func error(message: String) -> void:
    _post(message, COLOR_ERROR_BG, COLOR_ERROR_BORDER)

## Report something that succeeded but leaves the world in a state the admin
## probably didn't intend.
func warning(message: String) -> void:
    _post(message, COLOR_WARNING_BG, COLOR_WARNING_BORDER)

func _post(message: String, bg: Color, border: Color) -> void:
    if message == "" or _stack == null:
        return
    while _stack.get_child_count() >= MAX_VISIBLE:
        var oldest: Node = _stack.get_child(0)
        _stack.remove_child(oldest)
        oldest.queue_free()

    var panel := PanelContainer.new()
    panel.custom_minimum_size = Vector2(TOAST_WIDTH, 0)
    panel.mouse_filter = Control.MOUSE_FILTER_STOP
    panel.add_theme_stylebox_override("panel", _style(bg, border))

    var margin := MarginContainer.new()
    for side in ["left", "right"]:
        margin.add_theme_constant_override("margin_" + side, 14)
    for side in ["top", "bottom"]:
        margin.add_theme_constant_override("margin_" + side, 10)
    panel.add_child(margin)

    var label := Label.new()
    label.text = message
    label.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    label.add_theme_color_override("font_color", COLOR_TEXT)
    label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    margin.add_child(label)

    _stack.add_child(panel)
    panel.gui_input.connect(func(event: InputEvent):
        if event is InputEventMouseButton and event.pressed:
            _dismiss(panel)
    )

    # Hold, then fade. create_tween() dies with the node, so a banner dismissed by
    # click never fires its queue_free twice.
    var tween := panel.create_tween()
    tween.tween_interval(HOLD_SECONDS)
    tween.tween_property(panel, "modulate:a", 0.0, FADE_SECONDS)
    tween.tween_callback(func(): _dismiss(panel))

func _dismiss(panel: Control) -> void:
    if not is_instance_valid(panel):
        return
    # Remove before freeing so the VBox re-flows this frame rather than holding a
    # gap until the deferred free lands.
    if panel.get_parent() != null:
        panel.get_parent().remove_child(panel)
    panel.queue_free()

func _style(bg: Color, border: Color) -> StyleBoxFlat:
    var style := StyleBoxFlat.new()
    style.bg_color = bg
    style.border_color = border
    style.set_border_width_all(1)
    style.set_corner_radius_all(4)
    return style
