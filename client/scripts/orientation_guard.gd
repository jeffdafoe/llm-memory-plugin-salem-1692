extends CanvasLayer
## Autoloaded singleton — keeps the web client landscape-only on a tablet
## (LLM-702). The UI is laid out for a 1280x720 design scaled by the smaller
## ratio, so in portrait the 1280 design width is squeezed into the short side
## and every panel shrinks to an unreadable size.
##
## Two parts, both only in the web build on a device whose primary pointer is
## coarse (a tablet; a touch-screen laptop has a fine primary pointer and is left
## alone):
##
##   1. A DOM listener requests fullscreen on a tap while the page is not
##      fullscreen — which hides the browser's address bar and tabs — then locks
##      the screen to landscape. Browsers allow the lock only in fullscreen, and
##      fullscreen only inside a real user gesture, which is why this is a DOM
##      listener and not a Godot input handler. iPadOS Safari has no lock; there
##      the user rotates the tablet and the cover below goes away.
##   2. While the window is taller than wide, a cover asks the user to turn the
##      tablet sideways and swallows every pointer event.

## Below Toast (128) so an admin failure message still reads over the cover, and
## above everything else (login_layer is 10).
const LAYER_INDEX: int = 127

const COLOR_BG := Color(0.05, 0.03, 0.02, 1.0)
const COLOR_TITLE := Color(0.93, 0.86, 0.70)
const COLOR_TEXT := Color(0.80, 0.74, 0.62)

## Installed once per page. The flag on window keeps a second autoload instance
## (a scene reload) from stacking a second listener.
const FULLSCREEN_LISTENER_JS := """
(function () {
    if (window.__salemLandscapeGuard) return;
    window.__salemLandscapeGuard = true;
    document.addEventListener('pointerup', function () {
        var root = document.documentElement;
        if (document.fullscreenElement || !root.requestFullscreen) return;
        root.requestFullscreen({ navigationUI: 'hide' }).then(function () {
            if (screen.orientation && screen.orientation.lock) {
                return screen.orientation.lock('landscape');
            }
        }).catch(function () {});
    }, true);
})();
"""

var _enabled := false
var _covering := false
var _cover: ColorRect = null

func _ready() -> void:
    layer = LAYER_INDEX
    _enabled = OS.has_feature("web") and JavaScriptBridge.eval("matchMedia('(pointer: coarse)').matches", true) == true
    if not _enabled:
        return
    JavaScriptBridge.eval(FULLSCREEN_LISTENER_JS)
    _build_cover()
    get_tree().root.size_changed.connect(_refresh)
    # Deferred: the root is still adding its children during an autoload's
    # _ready, and _apply may move this node among them.
    _refresh.call_deferred()

## The whole decision, kept pure so the headless test can drive it.
static func should_cover(coarse_pointer: bool, window_size: Vector2i) -> bool:
    return coarse_pointer and window_size.y > window_size.x

static func is_pointer_event(event: InputEvent) -> bool:
    return event is InputEventMouse \
        or event is InputEventScreenTouch \
        or event is InputEventScreenDrag \
        or event is InputEventGesture

func _refresh() -> void:
    _apply(should_cover(_enabled, get_window().size))

func _apply(covering: bool) -> void:
    _covering = covering
    _cover.visible = covering
    if covering:
        # _input runs last child of the root first, and play-mode scripts (main,
        # camera, the tooltips, the talk panel) read taps in _input — before the
        # GUI, so the cover's own mouse_filter cannot stop them. Moving to the end
        # of the root makes this node see every event first.
        get_parent().move_child(self, -1)

func _input(event: InputEvent) -> void:
    if _covering and is_pointer_event(event):
        get_viewport().set_input_as_handled()

func _build_cover() -> void:
    _cover = ColorRect.new()
    _cover.name = "LandscapeCover"
    _cover.color = COLOR_BG
    _cover.set_anchors_preset(Control.PRESET_FULL_RECT)
    _cover.mouse_filter = Control.MOUSE_FILTER_STOP
    _cover.visible = false
    add_child(_cover)

    var box := VBoxContainer.new()
    box.set_anchors_preset(Control.PRESET_CENTER)
    box.grow_horizontal = Control.GROW_DIRECTION_BOTH
    box.grow_vertical = Control.GROW_DIRECTION_BOTH
    box.alignment = BoxContainer.ALIGNMENT_CENTER
    box.add_theme_constant_override("separation", 16)
    box.mouse_filter = Control.MOUSE_FILTER_IGNORE
    _cover.add_child(box)

    box.add_child(_label("Turn your tablet sideways", 44, COLOR_TITLE))
    box.add_child(_label("Salem is played in landscape.\nTap to play full screen.", 28, COLOR_TEXT))

func _label(text: String, size: int, color: Color) -> Label:
    var label := Label.new()
    label.text = text
    label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    label.add_theme_font_size_override("font_size", size)
    label.add_theme_color_override("font_color", color)
    label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    return label
