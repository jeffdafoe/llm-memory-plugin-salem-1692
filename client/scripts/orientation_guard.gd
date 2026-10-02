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

## TEMPORARY (LLM-702 tablet debugging): shown in the debug panel so a stale
## build is obvious — if the panel is missing, the tab runs an older build.
const DEBUG_BUILD := "LLM-702 debug 1"

## The landscape request plus a short log of every attempt, defined once per
## page on web whether or not the guard is enabled, so the debug panel's button
## can run it too. The flag on window keeps a second autoload instance (a scene
## reload) from redefining it.
const LANDSCAPE_SUPPORT_JS := """
(function () {
    if (window.__salemGoLandscape) return;
    window.__salemGuardLog = [];
    window.__salemLog = function (line) {
        var log = window.__salemGuardLog;
        log.push(new Date().toISOString().substr(11, 8) + ' ' + line);
        if (log.length > 6) log.shift();
    };
    // One request at a time: a button tap also reaches the tap listener, and a
    // second request off the same gesture would be refused and muddy the log.
    var busy = false;
    function lock(source) {
        if (!(screen.orientation && screen.orientation.lock)) { window.__salemLog(source + ': no lock API'); return Promise.resolve(); }
        return screen.orientation.lock('landscape').then(function () {
            window.__salemLog(source + ': lock ok');
        });
    }
    window.__salemGoLandscape = function (source) {
        var root = document.documentElement;
        if (busy) return;
        if (!root.requestFullscreen) { window.__salemLog(source + ': no fullscreen API'); return; }
        busy = true;
        var step = document.fullscreenElement
            ? lock(source)
            : root.requestFullscreen({ navigationUI: 'hide' }).then(function () {
                window.__salemLog(source + ': fullscreen ok');
                return lock(source);
            });
        step.catch(function (e) {
            window.__salemLog(source + ': refused ' + (e && e.name ? e.name + ' ' + e.message : String(e)));
        }).then(function () { busy = false; });
    };
})();
"""

## Installed only when the guard is enabled: a tap while not fullscreen asks for
## fullscreen + landscape.
const FULLSCREEN_LISTENER_JS := """
(function () {
    if (window.__salemLandscapeGuard) return;
    window.__salemLandscapeGuard = true;
    document.addEventListener('pointerup', function (e) {
        if (document.fullscreenElement) return;
        window.__salemGoLandscape('tap ' + e.pointerType);
    }, true);
    window.__salemLog('listener installed');
})();
"""

const DEBUG_READINGS_JS := "'coarse=' + matchMedia('(pointer: coarse)').matches + ' anyCoarse=' + matchMedia('(any-pointer: coarse)').matches + ' fine=' + matchMedia('(pointer: fine)').matches + ' hover=' + matchMedia('(hover: hover)').matches + ' touchPoints=' + navigator.maxTouchPoints + ' dpr=' + devicePixelRatio + ' css=' + innerWidth + 'x' + innerHeight + ' fsApi=' + !!document.documentElement.requestFullscreen + ' fsOn=' + !!document.fullscreenElement + ' lockApi=' + !!(screen.orientation && screen.orientation.lock) + ' orient=' + (screen.orientation ? screen.orientation.type : '?')"

var _enabled := false
var _covering := false
var _cover: ColorRect = null
var _coarse_raw: Variant = null

func _ready() -> void:
    layer = LAYER_INDEX
    if OS.has_feature("web"):
        JavaScriptBridge.eval(LANDSCAPE_SUPPORT_JS)
        _coarse_raw = JavaScriptBridge.eval("matchMedia('(pointer: coarse)').matches", true)
    _enabled = OS.has_feature("web") and _coarse_raw == true
    if not _enabled:
        return
    JavaScriptBridge.eval(FULLSCREEN_LISTENER_JS)
    _build_cover()
    get_tree().root.size_changed.connect(_refresh)
    _watch_root()
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

## A root child added while covering (a scene change, a popup) would land after
## this node and read _input first, so the guard moves back to the end.
func _watch_root() -> void:
    get_tree().root.child_entered_tree.connect(_on_root_child_entered)

func _on_root_child_entered(node: Node) -> void:
    if _covering and node != self:
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
    # No button here: the guard swallows pointer input while covering, and a tap
    # on the cover already runs the request through the tap listener.
    var debug := build_debug_panel(false)
    debug.set_anchors_preset(Control.PRESET_BOTTOM_WIDE)
    debug.grow_vertical = Control.GROW_DIRECTION_BEGIN
    debug.offset_left = 16
    debug.offset_right = -16
    debug.offset_bottom = -16
    _cover.add_child(debug)

## TEMPORARY (LLM-702 tablet debugging): what the guard saw and every fullscreen
## attempt, refreshed twice a second, plus a button that runs the same request.
## Shown on the login screen and on the portrait cover.
func debug_lines() -> PackedStringArray:
    var lines := PackedStringArray()
    lines.append("%s | web=%s enabled=%s covering=%s window=%s" % [DEBUG_BUILD, OS.has_feature("web"), _enabled, _covering, get_window().size])
    lines.append("coarse as Godot saw it: %s (type %d)" % [_coarse_raw, typeof(_coarse_raw)])
    if OS.has_feature("web"):
        lines.append(str(JavaScriptBridge.eval(DEBUG_READINGS_JS, true)))
        lines.append("log: " + str(JavaScriptBridge.eval("(window.__salemGuardLog || []).join(' | ')", true)))
        lines.append(str(JavaScriptBridge.eval("navigator.userAgent", true)))
    return lines

func build_debug_panel(with_button: bool = true) -> Control:
    var box := VBoxContainer.new()
    box.add_theme_constant_override("separation", 8)
    box.mouse_filter = Control.MOUSE_FILTER_IGNORE
    var text := Label.new()
    text.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    text.add_theme_font_size_override("font_size", 18)
    text.add_theme_color_override("font_color", COLOR_TEXT)
    text.mouse_filter = Control.MOUSE_FILTER_IGNORE
    box.add_child(text)
    if with_button:
        var button := Button.new()
        button.text = "Test fullscreen"
        button.add_theme_font_size_override("font_size", 22)
        button.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
        button.pressed.connect(func():
            if OS.has_feature("web"):
                JavaScriptBridge.eval("window.__salemGoLandscape && window.__salemGoLandscape('button')")
        )
        box.add_child(button)
    var timer := Timer.new()
    timer.wait_time = 0.5
    timer.autostart = true
    timer.timeout.connect(func(): text.text = "\n".join(debug_lines()))
    box.add_child(timer)
    text.text = "\n".join(debug_lines())
    return box

func _label(text: String, size: int, color: Color) -> Label:
    var label := Label.new()
    label.text = text
    label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    label.add_theme_font_size_override("font_size", size)
    label.add_theme_color_override("font_color", color)
    label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    return label
