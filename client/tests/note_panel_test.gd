extends SceneTree

## Headless harness for the note window (LLM-19, client/scripts/note_panel.gd):
## after a knock nobody answered, a note for one of the people of the house.
##
## Covers who it is for (one name chosen; more, none until picked), the
## MAX_CHARS cap and counter, Pin's readiness and what it sends, a refusal
## shown back to the player (refusal_line), closing paths, the parchment art
## when it is there and the plain stand-in when it is not, and the pinned
## paper's place on a door (world.gd door_note_local_position).
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/note_panel_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const PANEL_PATH := "res://scripts/note_panel.gd"
const WORLD_PATH := "res://scripts/world.gd"

const TESTS := [
    "_test_one_recipient_is_chosen",
    "_test_several_recipients_wait_for_a_pick",
    "_test_cap_and_counter",
    "_test_pin_sends_and_waits",
    "_test_refusal_shows_and_allows_retry",
    "_test_refusal_line",
    "_test_close_paths",
    "_test_art_and_stand_in",
    "_test_door_note_sits_on_the_door",
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
    print("\n[note_panel_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[note_panel_test] ALL PASS")
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


## A window in the tree with no art (the hook answers null), every signal it
## sent recorded in `log`.
func _panel(log: Array, art: Callable = Callable()) -> CanvasLayer:
    var p: CanvasLayer = load(PANEL_PATH).new()
    p.texture_hook = art if art.is_valid() else func(_path: String): return null
    root.add_child(p)
    p.opened.connect(func(): log.append("opened"))
    p.closed.connect(func(): log.append("closed"))
    p.pinned.connect(func(sid: String, rid: String, text: String): log.append("pinned:%s:%s:%s" % [sid, rid, text]))
    return p


const WALKERS := [
    {"id": "anne", "name": "Anne Walker"},
    {"id": "lewis", "name": "Lewis Walker"},
    {"id": "patience", "name": "Patience Walker"},
    {"id": "silence", "name": "Silence Walker"},
]


func _type(p: CanvasLayer, text: String) -> void:
    p.note_edit.text = text
    p.note_edit.text_changed.emit()


func _test_one_recipient_is_chosen() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("ward-home", "Ward Residence", [{"id": "prudence", "name": "Prudence Ward"}])
    _check("open shows it", [p.is_open(), log], [true, ["opened"]])
    _check("the house", p.house_label.text, "Ward Residence")
    _check("one name: shown", [p.only_name.visible, p.only_name.text], [true, "Prudence Ward"])
    _check("one name: no picker", p.names_row.visible, false)
    _check("one name: chosen", p.recipient_id, "prudence")
    _check("no text yet: cannot pin", p.can_pin(), false)
    _type(p, "The tincture, if you please.")
    _check("text and a name: can pin", p.can_pin(), true)
    p.free()
    _done()


func _test_several_recipients_wait_for_a_pick() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("walker-home", "Walker Residence", WALKERS)
    _check("several: a picker", [p.names_row.visible, p.only_name.visible], [true, false])
    _check("several: one button each", p.names_row.get_child_count(), 4)
    _check("several: none chosen", p.recipient_id, "")
    _type(p, "I came about the bread.")
    _check("text but no name: cannot pin", p.can_pin(), false)
    var silence: Button = p.names_row.get_child(3)
    _check("the fourth is Silence", silence.text, "Silence Walker")
    silence.button_pressed = true
    _check("picked: Silence", p.recipient_id, "silence")
    _check("picked: can pin", p.can_pin(), true)
    var anne: Button = p.names_row.get_child(0)
    anne.button_pressed = true
    _check("picking another: one at a time", [p.recipient_id, silence.button_pressed], ["anne", false])
    # Reopening starts afresh: no name, no text.
    p.open("walker-home", "Walker Residence", WALKERS)
    _check("reopened: none chosen, empty", [p.recipient_id, p.note_edit.text, p.names_row.get_child_count()], ["", "", 4])
    _check("reopened: opened only once", log, ["opened"])
    p.free()
    _done()


func _test_cap_and_counter() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("ward-home", "Ward Residence", [{"id": "prudence", "name": "Prudence Ward"}])
    _check("empty counter", p.counter_label.text, "0 / 200")
    _type(p, "x".repeat(p.MAX_CHARS + 25))
    _check("cut to the cap", p.note_edit.text.length(), p.MAX_CHARS)
    _check("full counter", p.counter_label.text, "200 / 200")
    _type(p, "   ")
    _check("only spaces: cannot pin", p.can_pin(), false)
    p.free()
    _done()


func _test_pin_sends_and_waits() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("walker-home", "Walker Residence", WALKERS)
    (p.names_row.get_child(3) as Button).button_pressed = true
    _type(p, "  I came about the bread.  ")
    p.pin_button.pressed.emit()
    _check("pin: sends the door, the name and the trimmed note", log.back(), "pinned:walker-home:silence:I came about the bread.")
    _check("pin: waits for the answer (no second send)", p.can_pin(), false)
    p.pin_button.pressed.emit()
    _check("a second press sends nothing", log.size(), 2)
    _check("still open until main closes it", p.is_open(), true)
    p.close()
    _check("closed", [p.is_open(), log.back()], [false, "closed"])
    p.free()
    _done()


func _test_refusal_shows_and_allows_retry() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("ward-home", "Ward Residence", [{"id": "prudence", "name": "Prudence Ward"}])
    _type(p, "hello")
    p.pin_button.pressed.emit()
    p.show_error("You are not at that door.")
    _check("refusal shown", [p.error_label.visible, p.error_label.text], [true, "You are not at that door."])
    _check("refusal: may try again", p.can_pin(), true)
    _type(p, "hello again")
    _check("typing clears the refusal", p.error_label.visible, false)
    p.free()
    _done()


func _test_refusal_line() -> void:
    var script: GDScript = load(PANEL_PATH)
    _check("last reason, capitalised", script.refusal_line("you cannot leave a note here: you cannot knock here: you are not at that door"), "You are not at that door.")
    _check("one part", script.refusal_line("the note must have between 1 and 200 characters"), "The note must have between 1 and 200 characters.")
    _check("empty", script.refusal_line(""), "The note could not be pinned.")
    _check("keeps its own stop", script.refusal_line("they do not live or keep here."), "They do not live or keep here.")
    _done()


func _test_close_paths() -> void:
    var log: Array = []
    var p := _panel(log)
    p.open("ward-home", "Ward Residence", [{"id": "prudence", "name": "Prudence Ward"}])
    p.cancel_button.pressed.emit()
    _check("never mind: closes, sends nothing", log, ["opened", "closed"])
    p.open("ward-home", "Ward Residence", [{"id": "prudence", "name": "Prudence Ward"}])
    var esc := InputEventKey.new()
    esc.keycode = KEY_ESCAPE
    esc.pressed = true
    p._unhandled_input(esc)
    _check("esc: closes", p.is_open(), false)
    p.open("ward-home", "Ward Residence", [{"id": "prudence", "name": "Prudence Ward"}])
    var click := InputEventMouseButton.new()
    click.button_index = MOUSE_BUTTON_LEFT
    click.pressed = true
    p._on_backdrop_input(click)
    _check("backdrop: closes", p.is_open(), false)
    _check("nothing was ever pinned", log.any(func(e): return str(e).begins_with("pinned")), false)
    p.free()
    _done()


func _test_art_and_stand_in() -> void:
    var log: Array = []
    var bare := _panel(log)
    _check("no art: plain parchment stand-in", bare.sheet.get_theme_stylebox("panel") is StyleBoxFlat, true)
    _check("no art: no quill", bare.pin_button.icon, null)
    bare.free()
    var img := Image.create(64, 64, false, Image.FORMAT_RGBA8)
    img.fill(Color(0.8, 0.7, 0.5))
    var tex := ImageTexture.create_from_image(img)
    var dressed := _panel(log, func(_path: String): return tex)
    var st = dressed.sheet.get_theme_stylebox("panel")
    _check("art: the parchment sheet", st is StyleBoxTexture and st.texture == tex, true)
    _check("art: torn edges kept whole", st.texture_margin_left if st is StyleBoxTexture else -1.0, float(dressed.SHEET_MARGIN_LEFT))
    _check("art: the quill on Pin", dressed.pin_button.icon, tex)
    _check("art: parchment buttons", dressed.pin_button.get_theme_stylebox("normal") is StyleBoxTexture, true)
    dressed.free()
    _done()


## The pinned paper sits on the door tile (the asset's door offset from the
## anchor tile), raised onto the door's face; a building with no door gets none.
func _test_door_note_sits_on_the_door() -> void:
    var world_script: GDScript = load(WORLD_PATH)
    var tile: float = world_script.DOOR_NOTE_TILE
    var house_at := Vector2(20 * tile + 5, 20 * tile + 7)
    var at = world_script.door_note_local_position(house_at, {"door_offset_x": 0, "door_offset_y": 1})
    var want := Vector2(20 * tile + tile / 2.0, 21 * tile + tile / 2.0 - world_script.DOOR_NOTE_LIFT) - house_at
    _check("on the door tile, raised", at, want)
    _check("no door: no paper", world_script.door_note_local_position(house_at, {}), null)
    _done()
