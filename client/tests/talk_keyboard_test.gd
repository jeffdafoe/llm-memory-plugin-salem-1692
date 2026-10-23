extends SceneTree

## Headless harness for LLM-861: the talk panel's own on-screen keyboard types
## into its target field, and a tap on the village while a field has focus only
## takes the focus away (LLM-828).
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/talk_keyboard_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The keyboard and a TextEdit target are added to the root; keys are fired
## through press() and through the key Buttons' gui_input, as a touch would.

const TalkKeyboardScript = preload("res://scripts/talk_keyboard.gd")

const TESTS := [
    "_test_letters_capitalise_a_sentence",
    "_test_shift_is_one_letter",
    "_test_backspace_and_its_repeat",
    "_test_symbols_page",
    "_test_send_and_system_signals",
    "_test_emulated_mouse_then_first_touch_types_once",
    "_test_mouse_only_device_types",
    "_test_touch_then_emulated_mouse_types_once",
    "_test_second_finger_types",
    "_test_icons_are_drawn",
    "_test_world_tap_releases_text_focus",
    "_test_talk_field_touch_opens_own_keyboard",
    "_test_system_keyboard_round_trip",
]

var _keyboard: VBoxContainer = null
var _field: TextEdit = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _field = TextEdit.new()
    root.add_child(_field)
    _keyboard = TalkKeyboardScript.new()
    _keyboard.target = _field
    root.add_child(_keyboard)
    _keyboard.set_key_size(48.0, 18)
    _check_test_list()
    for t in TESTS:
        _current = t
        _reset()
        await call(t)
    _check_all_tests_ran()
    print("\n[talk_keyboard_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[talk_keyboard_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _done() -> void:
    _completed[_current] = true


func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)


func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t), true)
        _check("harness — %s exists" % t, has_method(t), true)
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name), true)


func _reset() -> void:
    _field.text = ""
    _keyboard.stop_repeat()
    if _keyboard.symbols_on:
        _keyboard.press("page")
    _keyboard.refresh_auto_shift()


func _type(ids: Array) -> void:
    for id in ids:
        _keyboard.press(id)
        _keyboard.release(id)


func _key(id: String) -> Button:
    return _keyboard._keys.get(id)


func _touch(id: String, index: int, pressed: bool) -> void:
    var event := InputEventScreenTouch.new()
    event.index = index
    event.pressed = pressed
    _key(id).gui_input.emit(event)


func _emulated_mouse(id: String, pressed: bool) -> void:
    var event := InputEventMouseButton.new()
    event.button_index = MOUSE_BUTTON_LEFT
    event.pressed = pressed
    event.device = InputEvent.DEVICE_ID_EMULATION
    _key(id).gui_input.emit(event)


func _test_letters_capitalise_a_sentence() -> void:
    _check("empty field: shift is on", _keyboard.shift_on, true)
    _type(["h", "i", ".", "space", "y", "o"])
    _check("a sentence start takes a capital", _field.text, "Hi. Yo")
    _check("mid-word: shift is off", _keyboard.shift_on, false)
    _done()


func _test_shift_is_one_letter() -> void:
    _field.text = "x"
    _field.set_caret_column(1)
    _keyboard.refresh_auto_shift()
    _type(["a", "shift", "b", "c"])
    _check("shift capitalises the next letter only", _field.text, "xaBc")
    _done()


func _test_backspace_and_its_repeat() -> void:
    _type(["a", "b", "c", "back"])
    _check("backspace removes one letter", _field.text, "Ab")
    _keyboard.press("back")
    _check("held backspace: the repeat is running", _keyboard._back_timer.is_stopped(), false)
    _keyboard._on_back_repeat()
    _check("a repeat removes one more", _field.text, "")
    _keyboard.release("back")
    _check("let go: the repeat stops", _keyboard._back_timer.is_stopped(), true)
    _check("emptied field: shift is back on", _keyboard.shift_on, true)
    _done()


func _test_symbols_page() -> void:
    _keyboard.press("page")
    _check("123: the symbols page", _keyboard.symbols_on, true)
    _check("123: a digit key exists", _key("1") != null, true)
    _check("123: no shift key", _key("shift") == null, true)
    _type(["1", "?"])
    _check("symbols are not capitalised", _field.text, "1?")
    _keyboard.press("page")
    _check("ABC: back to letters", _key("q") != null, true)
    _done()


func _test_send_and_system_signals() -> void:
    var sent := [0]
    var switched := [0]
    var on_send := func(): sent[0] += 1
    var on_system := func(): switched[0] += 1
    _keyboard.send_pressed.connect(on_send)
    _keyboard.system_pressed.connect(on_system)
    _type(["send", "system"])
    _check("Speak key sends", sent[0], 1)
    _check("keyboard key asks for the system keyboard", switched[0], 1)
    _check("neither types", _field.text, "")
    _keyboard.send_pressed.disconnect(on_send)
    _keyboard.system_pressed.disconnect(on_system)
    _done()


func _test_emulated_mouse_then_first_touch_types_once() -> void:
    _keyboard._touch_seen = false
    _emulated_mouse("a", true)
    _touch("a", 0, true)
    _emulated_mouse("a", false)
    _touch("a", 0, false)
    _check("a first tap whose emulated mouse comes first types once", _field.text, "A")
    _check("the first touch marks touches as seen", _keyboard._touch_seen, true)
    _done()


func _test_mouse_only_device_types() -> void:
    _keyboard._touch_seen = false
    _emulated_mouse("a", true)
    _emulated_mouse("a", false)
    _emulated_mouse("b", true)
    _emulated_mouse("b", false)
    _check("with no touch reaching the keys, the emulated mouse types", _field.text, "Ab")
    _done()


func _test_touch_then_emulated_mouse_types_once() -> void:
    _touch("a", 0, true)
    _emulated_mouse("a", true)
    _touch("a", 0, false)
    _emulated_mouse("a", false)
    _check("a tap types once, not again for its emulated mouse", _field.text, "A")
    _done()


func _test_second_finger_types() -> void:
    _touch("a", 0, true)
    _touch("b", 1, true)
    _touch("a", 0, false)
    _touch("b", 1, false)
    _check("a key pressed while another finger is down types", _field.text, "Ab")
    _done()


func _test_icons_are_drawn() -> void:
    var icon: ImageTexture = TalkKeyboardScript.make_icon("back", 2)
    _check("the backspace icon is 24x18 at scale 2", icon.get_size(), Vector2(24, 18))
    _check("the backspace key shows an icon, no text", _key("back").icon != null and _key("back").text == "", true)
    _done()


func _test_world_tap_releases_text_focus() -> void:
    _field.grab_focus()
    _check("field focused", _field.has_focus(), true)
    _check("a focused field: the tap takes the focus", load("res://scripts/main.gd").release_text_focus(root), true)
    _check("the field lost focus", _field.has_focus(), false)
    _check("nothing focused: the tap walks", load("res://scripts/main.gd").release_text_focus(root), false)
    _done()


## A talk panel built as on a touch device, so it has its own keyboard.
func _touch_talk_panel() -> CanvasLayer:
    var guard: Node = root.get_node("OrientationGuard")
    var was_touch: bool = guard._touch_text
    guard._touch_text = true
    var talk: CanvasLayer = load("res://scripts/talk_panel.gd").new()
    root.add_child(talk)
    guard._touch_text = was_touch
    return talk


func _test_talk_field_touch_opens_own_keyboard() -> void:
    var talk := _touch_talk_panel()
    _check("touch device: the panel has its own keyboard", talk.own_keyboard != null, true)
    _check("touch device: the field opens no system keyboard", talk.speech_input.virtual_keyboard_enabled, false)
    _check("before a tap: ours is down", talk.own_keyboard.visible, false)
    var touch := InputEventScreenTouch.new()
    touch.pressed = true
    talk.speech_input.gui_input.emit(touch)
    _check("a touch on the field opens ours", talk.own_keyboard.visible, true)
    talk._hide_own_keyboard()
    var click := InputEventMouseButton.new()
    click.button_index = MOUSE_BUTTON_LEFT
    click.pressed = true
    talk.speech_input.gui_input.emit(click)
    _check("an emulated mouse press on the field opens ours", talk.own_keyboard.visible, true)
    talk.free()
    _done()


func _test_system_keyboard_round_trip() -> void:
    var talk := _touch_talk_panel()
    talk._show_own_keyboard()
    talk.own_keyboard.press("system")
    _check("switch key: ours goes down", talk.own_keyboard.visible, false)
    _check("switch key: system mode on", talk.system_keyboard, true)
    _check("switch key: the field may open the device keyboard", talk.speech_input.virtual_keyboard_enabled, true)
    talk._on_keyboard_button_pressed()
    _check("Speak-row key: ours comes back", talk.own_keyboard.visible, true)
    _check("Speak-row key: system mode ends", talk.system_keyboard, false)
    _check("Speak-row key: the field opens no system keyboard", talk.speech_input.virtual_keyboard_enabled, false)
    talk._on_keyboard_button_pressed()
    _check("Speak-row key again: ours goes down", talk.own_keyboard.visible, false)
    talk.free()
    _done()
