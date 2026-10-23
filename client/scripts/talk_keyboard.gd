extends VBoxContainer

## The talk panel's own on-screen keyboard (LLM-861), for touch devices.
##
## A system keyboard covers about half of a landscape tablet, and a web page
## cannot size it. This one docks under the talk panel's Speak row, so its
## height is ours. It types straight into `target` (the talk panel's
## speech_input); the talk panel owns showing, hiding and sending.
##
## Keys are Buttons with button_mask 0: their own click handling is off and
## _on_key_input fires a key on the touch PRESS. A Button only hears the
## emulated mouse, and the emulated mouse follows the first finger alone, so a
## key pressed while another finger is still down (fast two-thumb typing) would
## be lost. Once a touch has reached a key, emulated mouse presses are dropped.

signal send_pressed
signal system_pressed

const LETTER_ROWS := [
    ["q", "w", "e", "r", "t", "y", "u", "i", "o", "p"],
    ["a", "s", "d", "f", "g", "h", "j", "k", "l", "'"],
    ["shift", "z", "x", "c", "v", "b", "n", "m", ",", "."],
]
const SYMBOL_ROWS := [
    ["1", "2", "3", "4", "5", "6", "7", "8", "9", "0"],
    ["!", "?", "'", "\"", "-", ":", ";", "(", ")", "&"],
    ["@", "#", "$", "%", "*", "/", "+", "=", ",", "."],
]
const BOTTOM_ROW := ["page", "system", "space", "back", "send"]
# Every row sums to 10, so key columns line up.
const STRETCH := {"shift": 1.5, "page": 1.5, "system": 1.0, "space": 4.0, "back": 1.5, "send": 2.0}
const NARROW := {",": 0.75, ".": 0.75}

const BACK_REPEAT_DELAY := 0.45
const BACK_REPEAT_INTERVAL := 0.06
const FLASH_SEC := 0.09
# A first touch this soon after an emulated mouse press on the same key is the
# same tap.
const EMULATED_MOUSE_WINDOW_MSEC := 300

const KEY_COLOR := Color(0.30, 0.22, 0.13, 0.98)
const SPECIAL_COLOR := Color(0.21, 0.15, 0.09, 0.98)
const FLASH_COLOR := Color(0.58, 0.44, 0.25, 1.0)
const ON_COLOR := Color(0.46, 0.35, 0.20, 1.0)
const TEXT_COLOR := Color(0.94, 0.88, 0.75)

# Pixel icons, drawn here rather than font glyphs: a pixel font may lack
# ⌨ / ⌫ / ⇧, and a missing glyph only shows on the device.
const ICONS := {
    "system": [
        "############",
        "#..........#",
        "#.#.#.#.#..#",
        "#..........#",
        "#..#.#.#.#.#",
        "#..........#",
        "#..######..#",
        "#..........#",
        "############",
    ],
    "back": [
        "...#########",
        "..#........#",
        ".#..#...#..#",
        "#....#.#...#",
        "#.....#....#",
        "#....#.#...#",
        ".#..#...#..#",
        "..#........#",
        "...#########",
    ],
    "shift": [
        "....##....",
        "...#..#...",
        "..#....#..",
        ".#......#.",
        "###....###",
        "..#....#..",
        "..#....#..",
        "..#....#..",
        "..######..",
    ],
    "hide": [
        "#..........#",
        ".#........#.",
        "..#......#..",
        "...#....#...",
        "....#..#....",
        ".....##.....",
    ],
}

var target: TextEdit = null
var shift_on := false
var symbols_on := false

var _rows: Array[HBoxContainer] = []
var _keys: Dictionary = {}  # key id -> Button, for the current page
var _key_height := 48.0
var _font_size := 18
var _touch_seen := false
var _emulated_press_id := ""
var _emulated_press_msec := -100000
var _back_timer: Timer = null


func _init() -> void:
    add_theme_constant_override("separation", 4)
    mouse_filter = Control.MOUSE_FILTER_STOP
    _back_timer = Timer.new()
    _back_timer.one_shot = false
    _back_timer.timeout.connect(_on_back_repeat)
    add_child(_back_timer)


## Size the keys: one row's height and the label font size.
func set_key_size(key_height: float, font_size: int) -> void:
    if not _rows.is_empty() and is_equal_approx(key_height, _key_height) and font_size == _font_size:
        return
    _key_height = key_height
    _font_size = font_size
    _rebuild()


## The keyboard's height for a row height, separation included.
static func height_for(key_height: float) -> float:
    return key_height * 4.0 + 4.0 * 3.0


## Turn shift on at the start of a sentence, as phone keyboards do.
func refresh_auto_shift() -> void:
    if symbols_on:
        return
    var before := text_before_caret()
    var at_start := before.strip_edges().is_empty() \
            or before.ends_with(". ") or before.ends_with("! ") or before.ends_with("? ")
    if at_start != shift_on:
        shift_on = at_start
        _relabel()


## Fire a key, as a press on it does. Ids: a character, or one of shift,
## page, system, space, back, send.
func press(id: String) -> void:
    _flash(id)
    match id:
        "shift":
            shift_on = not shift_on
            _relabel()
        "page":
            symbols_on = not symbols_on
            shift_on = false
            _rebuild()
            refresh_auto_shift()
        "system":
            system_pressed.emit()
        "send":
            send_pressed.emit()
        "space":
            _type(" ")
        "back":
            _backspace()
            _back_timer.start(BACK_REPEAT_DELAY)
        _:
            _type(id.to_upper() if shift_on and not symbols_on else id)
            if shift_on and not symbols_on:
                shift_on = false
                _relabel()


## A finger or the mouse came off a key: ends a held backspace.
func release(id: String) -> void:
    if id == "back":
        _back_timer.stop()


func stop_repeat() -> void:
    _back_timer.stop()


func _type(s: String) -> void:
    if target == null:
        return
    target.insert_text_at_caret(s)
    if s == " ":
        refresh_auto_shift()


func _backspace() -> void:
    if target == null:
        return
    target.backspace()
    refresh_auto_shift()


func _on_back_repeat() -> void:
    _backspace()
    _back_timer.wait_time = BACK_REPEAT_INTERVAL


func text_before_caret() -> String:
    if target == null:
        return ""
    var line := target.get_caret_line()
    var before := target.get_line(line).substr(0, target.get_caret_column())
    for i in range(line - 1, -1, -1):
        before = target.get_line(i) + "\n" + before
    return before


func _on_key_input(event: InputEvent, id: String) -> void:
    if event is InputEventScreenTouch:
        if event.pressed:
            # The first touch may come after the emulated mouse press of the
            # same tap, which has typed it already.
            var same_tap := not _touch_seen and id == _emulated_press_id \
                    and Time.get_ticks_msec() - _emulated_press_msec <= EMULATED_MOUSE_WINDOW_MSEC
            _touch_seen = true
            if not same_tap:
                press(id)
        else:
            release(id)
        accept_event()
    elif event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT:
        var emulated: bool = event.device == InputEvent.DEVICE_ID_EMULATION
        if event.pressed:
            # Once touches reach the keys, the emulated mouse only repeats them.
            if emulated and _touch_seen:
                pass
            else:
                if emulated:
                    _emulated_press_id = id
                    _emulated_press_msec = Time.get_ticks_msec()
                press(id)
        else:
            release(id)
        accept_event()


func _rebuild() -> void:
    _back_timer.stop()
    for row in _rows:
        remove_child(row)
        row.queue_free()
    _rows.clear()
    _keys.clear()
    var rows: Array = (SYMBOL_ROWS if symbols_on else LETTER_ROWS).duplicate()
    rows.append(BOTTOM_ROW)
    for ids in rows:
        var row := HBoxContainer.new()
        row.add_theme_constant_override("separation", 4)
        row.mouse_filter = Control.MOUSE_FILTER_IGNORE
        add_child(row)
        _rows.append(row)
        for id in ids:
            row.add_child(_make_key(id))
    _relabel()


func _make_key(id: String) -> Button:
    var key := Button.new()
    key.name = "Key_" + id
    key.focus_mode = Control.FOCUS_NONE
    key.button_mask = 0
    key.mouse_filter = Control.MOUSE_FILTER_STOP
    key.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    key.size_flags_stretch_ratio = STRETCH.get(id, NARROW.get(id, 1.0))
    key.custom_minimum_size = Vector2(0, _key_height)
    key.add_theme_font_size_override("font_size", _font_size)
    for state in ["font_color", "font_hover_color", "font_pressed_color", "font_focus_color"]:
        key.add_theme_color_override(state, TEXT_COLOR)
    var special: bool = STRETCH.has(id)
    _set_key_color(key, SPECIAL_COLOR if special else KEY_COLOR)
    if ICONS.has(id):
        key.icon = make_icon(id, maxi(1, roundi(_key_height / 24.0)))
        key.icon_alignment = HORIZONTAL_ALIGNMENT_CENTER
    elif id == "page":
        key.text = "ABC" if symbols_on else "123"
    elif id == "send":
        key.text = "Speak"
    elif id == "space":
        key.text = ""
    else:
        key.text = id
    key.gui_input.connect(_on_key_input.bind(id))
    _keys[id] = key
    return key


func _relabel() -> void:
    for id in _keys:
        var key: Button = _keys[id]
        if id.length() == 1 and not symbols_on:
            key.text = id.to_upper() if shift_on else id
    if _keys.has("shift"):
        _set_key_color(_keys["shift"], ON_COLOR if shift_on else SPECIAL_COLOR)


func _flash(id: String) -> void:
    if not _keys.has(id) or not is_inside_tree():
        return
    var key: Button = _keys[id]
    var rest := SPECIAL_COLOR if STRETCH.has(id) else KEY_COLOR
    _set_key_color(key, FLASH_COLOR)
    get_tree().create_timer(FLASH_SEC).timeout.connect(func():
        if is_instance_valid(key):
            _set_key_color(key, ON_COLOR if id == "shift" and shift_on else rest))


func _set_key_color(key: Button, color: Color) -> void:
    var box := StyleBoxFlat.new()
    box.bg_color = color
    box.border_color = Color(0.55, 0.42, 0.24, 0.6)
    box.set_border_width_all(1)
    box.set_corner_radius_all(5)
    for state in ["normal", "hover", "pressed", "focus", "disabled"]:
        key.add_theme_stylebox_override(state, box)


## One of the ICONS as a texture, each grid cell `scale` pixels square.
static func make_icon(kind: String, scale: int) -> ImageTexture:
    var grid: Array = ICONS[kind]
    var w: int = grid[0].length()
    var h: int = grid.size()
    var image := Image.create(w * scale, h * scale, false, Image.FORMAT_RGBA8)
    image.fill(Color(0, 0, 0, 0))
    for y in h:
        for x in w:
            if grid[y][x] == "#":
                image.fill_rect(Rect2i(x * scale, y * scale, scale, scale), TEXT_COLOR)
    return ImageTexture.create_from_image(image)
