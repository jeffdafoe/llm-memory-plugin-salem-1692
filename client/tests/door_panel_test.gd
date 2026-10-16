extends SceneTree

## Headless harness for the door popup (LLM-785, client/scripts/door_panel.gd):
## at the door of a house the player may not walk into, the house's name,
## "You stand at the door." and Knock / Leave.
##
## Covers opening (title, prompt, the opened signal, retitling while open),
## Knock (closes, then knocked(structure_id)), Leave / Esc / a backdrop click
## (close, no knock), and that closed or empty it never knocks.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/door_panel_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const PANEL_PATH := "res://scripts/door_panel.gd"
const WORLD_PATH := "res://scripts/world.gd"

const TESTS := [
    "_test_open_shows_the_house_and_the_question",
    "_test_knock_closes_then_knocks",
    "_test_leave_esc_and_backdrop_close_without_knocking",
    "_test_reopen_retitles",
    "_test_at_door_is_the_engines_door",
    "_test_no_answer_offers_a_note",
    "_test_no_answer_without_anyone_offers_no_note",
    "_test_no_answer_without_the_kit_says_what_is_needed",
    "_test_no_answer_waits_for_the_knock",
]

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[door_panel_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[door_panel_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)


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


## A popup in the tree, with every signal it sent recorded in `log`.
func _panel(log: Array) -> CanvasLayer:
    var p: CanvasLayer = load(PANEL_PATH).new()
    root.add_child(p)
    p.opened.connect(func(): log.append("opened"))
    p.closed.connect(func(): log.append("closed"))
    p.knocked.connect(func(sid: String): log.append("knocked:" + sid))
    p.note_requested.connect(func(sid: String, title: String, recipients: Array): log.append("note:%s:%s:%d" % [sid, title, recipients.size()]))
    return p


func _test_open_shows_the_house_and_the_question() -> void:
    var log: Array = []
    var p := _panel(log)
    _check("starts hidden", p.is_open(), false)
    p.open("walker-home", "Walker Residence")
    _check("open shows it", p.is_open(), true)
    _check("the house's name", p.title_label.text, "Walker Residence")
    _check("the question", p.prompt_label.text, "You stand at the door.")
    _check("the buttons", [p.knock_button.text, p.leave_button.text], ["Knock", "Leave"])
    _check("no note offered before a knock", p.note_button.visible, false)
    _check("opened once", log, ["opened"])
    p.open("walker-home", "")
    _check("no name: no title line", p.title_label.visible, false)
    p.free()
    _done()


func _test_knock_closes_then_knocks() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("walker-home", "Walker Residence")
    p.knock_button.pressed.emit()
    _check("knock: closes, then knocks on that door", log, ["opened", "closed", "knocked:walker-home"])
    _check("closed after the knock", p.is_open(), false)
    # A stray press while closed knocks on nothing.
    p.knock_button.pressed.emit()
    _check("closed: a knock press sends nothing", log.size(), 3)
    p.free()
    _done()


func _test_leave_esc_and_backdrop_close_without_knocking() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("walker-home", "Walker Residence")
    p.leave_button.pressed.emit()
    _check("leave: closes, no knock", log, ["opened", "closed"])

    p.open("walker-home", "Walker Residence")
    var esc := InputEventKey.new()
    esc.keycode = KEY_ESCAPE
    esc.pressed = true
    p._unhandled_input(esc)
    _check("esc: closes, no knock", log, ["opened", "closed", "opened", "closed"])

    p.open("walker-home", "Walker Residence")
    var click := InputEventMouseButton.new()
    click.button_index = MOUSE_BUTTON_LEFT
    click.pressed = true
    p._on_backdrop_input(click)
    _check("backdrop: closes, no knock", log.has("knocked:walker-home"), false)
    _check("backdrop: closed", p.is_open(), false)
    p.free()
    _done()


func _test_reopen_retitles() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("walker-home", "Walker Residence")
    p.open("cole-home", "Cole Residence")
    _check("retitled for the new door", [p.structure_id, p.title_label.text], ["cole-home", "Cole Residence"])
    _check("opened only once", log, ["opened"])
    p.knock_button.pressed.emit()
    _check("knocks on the new door", log.back(), "knocked:cole-home")
    p.free()
    _done()


## at_door: the person stands on the house's loiter pin or one of the eight
## tiles round it (the engine's AtDoorOf); anywhere else, or still walking a
## route, is not at the door — so an arrival from some other walk cannot open
## the popup for this house.
func _test_at_door_is_the_engines_door() -> void:
    var script: GDScript = load(PANEL_PATH)
    var tile := float(StallScenes.TILE)
    var house := Node2D.new()
    house.position = Vector2(10 * tile, 10 * tile)
    house.set_meta("effective_loiter_offset_x", 0)
    house.set_meta("effective_loiter_offset_y", 2)
    var person := Node2D.new()
    person.position = Vector2(10 * tile + 4, 12 * tile + 4)
    _check("on the pin", script.at_door(person, house), true)
    person.position = Vector2(11 * tile + 4, 13 * tile + 4)
    _check("a tile off, diagonally", script.at_door(person, house), true)
    person.position = Vector2(13 * tile + 4, 12 * tile + 4)
    _check("three tiles off", script.at_door(person, house), false)
    person.position = Vector2(10 * tile + 4, 12 * tile + 4)
    person.set_meta("walking", {"path": [Vector2(40 * tile, 40 * tile)], "finish_idle": false})
    _check("walking a route through the door", script.at_door(person, house), false)
    _check("no house", script.at_door(person, null), false)
    house.free()
    person.free()
    _done()


## LLM-19: after a knock nobody answered, the popup comes back saying so, with
## Leave a note (no Knock); Leave a note closes, then asks for the note window
## with the house's people. A later plain open is the knock question again.
func _test_no_answer_offers_a_note() -> void:
    var log: Array = []
    var p := _panel(log)
    var people := [{"id": "silence", "name": "Silence Walker"}, {"id": "anne", "name": "Anne Walker"}]
    p.open_unanswered("walker-home", "Walker Residence", people)
    _check("no answer: says so", p.prompt_label.text, "No one answers.")
    _check("no answer: knock hidden", p.knock_button.visible, false)
    _check("no answer: note offered", [p.note_button.visible, p.note_button.text], [true, "Leave a note"])
    p.note_button.pressed.emit()
    _check("leave a note: closes, then asks for the note", log, ["opened", "closed", "note:walker-home:Walker Residence:2"])
    p.open("walker-home", "Walker Residence")
    _check("reopened: the knock question", [p.prompt_label.text, p.knock_button.visible, p.note_button.visible],
        ["You stand at the door.", true, false])
    p.free()
    _done()


## LLM-799: without a quill and ink and parchment, Leave a note still shows, but
## pressing it puts the engine's line in the popup and asks for no note window.
## The next opening forgets the lack.
func _test_no_answer_without_the_kit_says_what_is_needed() -> void:
    var log: Array = []
    var p := _panel(log)
    var people := [{"id": "anne", "name": "Anne Walker"}]
    var lack := "You need a sheet of parchment to leave a note. Josiah Thorne has it for sale."
    p.open_unanswered("walker-home", "Walker Residence", people, lack)
    _check("lacking: note still offered", p.note_button.visible, true)
    p.note_button.pressed.emit()
    _check("lacking: says what is needed", [p.prompt_label.text, p.note_button.visible, p.is_open()], [lack, false, true])
    _check("lacking: no note window asked for", log, ["opened"])
    p.close()
    p.open_unanswered("walker-home", "Walker Residence", people)
    p.note_button.pressed.emit()
    _check("with the kit: asks for the note", log, ["opened", "closed", "opened", "closed", "note:walker-home:Walker Residence:1"])
    p.free()
    _done()


func _test_no_answer_without_anyone_offers_no_note() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open_unanswered("walker-home", "Walker Residence", [])
    _check("nobody to write to: no note button", p.note_button.visible, false)
    _check("nobody to write to: leave stays", p.leave_button.visible, true)
    p.note_button.pressed.emit()
    _check("a stray press asks for nothing", log, ["opened"])
    p.free()
    _done()


## LLM-805: "No one answers." waits for the knock. The popup is held for what
## is left of ANSWER_WAIT, longer than the knock itself; the engine's
## knock_unanswered line is held the whole wait, and nothing else is held.
func _test_no_answer_waits_for_the_knock() -> void:
    var P = load(PANEL_PATH)
    _check("the wait outlasts the knock (0.6 s)", P.ANSWER_WAIT > 0.61, true)
    _check("just knocked: the whole wait", P.answer_wait_left(1000, 1000), P.ANSWER_WAIT)
    _check("half a second on", is_equal_approx(P.answer_wait_left(1000, 1500), P.ANSWER_WAIT - 0.5), true)
    _check("a slow answer: nothing left to wait", P.answer_wait_left(1000, 9000), 0.0)
    var W = load(WORLD_PATH)
    _check("knock_unanswered is held", W.room_event_delay({"kind": "knock_unanswered"}), P.ANSWER_WAIT)
    _check("a note pinned is not", W.room_event_delay({"kind": "note_left"}), 0.0)
    _check("an ordinary event is not", W.room_event_delay({"kind": "act"}), 0.0)
    _done()
