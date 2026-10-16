extends SceneTree

## Headless harness for the Your Pack item actions (LLM-800,
## client/scripts/inventory_panel.gd) and the player's own duck quacking
## (client/scripts/familiar_quack.gd).
##
## Covers: a row with actions draws one button per action with its label and a
## row without draws none; a press emits item_action_requested and disables the
## buttons until action_finished; a refusal's line shows under the list and goes
## on the next press; the duck quacks once on coming to rest beside its owner,
## not while walking, not for someone else's duck, not when the owner is indoors.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/inventory_panel_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const PANEL_PATH := "res://scripts/inventory_panel.gd"
const QUACK_PATH := "res://scripts/familiar_quack.gd"

const TESTS := [
    "_test_rows_draw_their_action_buttons",
    "_test_press_requests_and_waits",
    "_test_refusal_shows_under_the_list",
    "_test_success_redraws_from_the_answer",
    "_test_older_poll_does_not_undo_the_answer",
    "_test_ui_panel_covers_the_popover",
    "_test_own_duck_quacks_on_arrival",
    "_test_own_duck_quacks_when_you_set_off",
    "_test_duck_keeps_watch_when_you_rest_or_sleep",
    "_test_agents_load_tags_the_own_duck",
    "_test_no_quack_walking_indoors_or_for_another",
    "_test_is_beside",
    "_test_tent_rows",
]

const ITEMS := [
    {"item_kind": "bread", "display_label": "Bread", "quantity": 2, "category": "food", "actions": []},
    {"item_kind": "duck_pendant", "display_label": "Duck pendant", "quantity": 1, "category": "charm", "actions": ["wear"]},
    {"item_kind": "silver_locket", "display_label": "Silver locket", "quantity": 1, "category": "charm", "actions": ["remove"]},
    {"item_kind": "parchment", "display_label": "Parchment", "quantity": 3, "category": "writing"},
]

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _check_test_list()
    await _run_all()
    _check_all_tests_ran()
    print("\n[inventory_panel_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[inventory_panel_test] ALL PASS")
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
        await call(t)


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


func _panel(log: Array) -> Control:
    var p: Control = load(PANEL_PATH).new()
    root.add_child(p)
    p.item_action_requested.connect(func(kind: String, action: String): log.append("%s:%s" % [kind, action]))
    p.set_inventory(ITEMS)
    p.show_at(Rect2(600, 0, 32, 32))
    return p


## label text -> [button texts] for every item row on the panel.
func _rows(p: Control) -> Dictionary:
    var out := {}
    for child in p._content.get_children():
        if child.is_queued_for_deletion() or not (child is HBoxContainer):
            continue
        var label := ""
        var buttons: Array = []
        for c in child.get_children():
            if c is Button:
                buttons.append(c.text)
            elif c is Label and label == "":
                label = c.text
        out[label] = buttons
    return out


func _button(p: Control, row_label: String) -> Button:
    for child in p._content.get_children():
        if child.is_queued_for_deletion() or not (child is HBoxContainer):
            continue
        var children: Array = child.get_children()
        if children.size() > 0 and children[0] is Label and children[0].text == row_label:
            for c in children:
                if c is Button:
                    return c
    return null


func _note_text(p: Control) -> String:
    for child in p._content.get_children():
        if not child.is_queued_for_deletion() and child is Label and child.autowrap_mode != TextServer.AUTOWRAP_OFF:
            return child.text
    return ""


func _test_rows_draw_their_action_buttons() -> void:
    var p := _panel([])
    var rows := _rows(p)
    _check("bread: no buttons", rows.get("Bread"), [])
    _check("duck pendant: Wear", rows.get("Duck pendant"), ["Wear"])
    _check("silver locket worn: Remove", rows.get("Silver locket"), ["Remove"])
    _check("no actions key: no buttons", rows.get("Parchment"), [])
    p.free()
    _done()


func _test_press_requests_and_waits() -> void:
    var log: Array = []
    var p := _panel(log)
    _button(p, "Duck pendant").pressed.emit()
    _check("press asks to wear the duck pendant", log, ["duck_pendant:wear"])
    _check("buttons wait while it is asked", _button(p, "Silver locket").disabled, true)
    _button(p, "Silver locket").pressed.emit()
    _check("a press while waiting asks nothing", log.size(), 1)
    p.action_finished("")
    _check("answered: buttons back", _button(p, "Silver locket").disabled, false)
    _check("success: no line", _note_text(p), "")
    p.free()
    _done()


func _test_refusal_shows_under_the_list() -> void:
    var p := _panel([])
    _button(p, "Duck pendant").pressed.emit()
    p.action_finished("You are not carrying that.")
    _check("refusal line shows", _note_text(p), "You are not carrying that.")
    _button(p, "Duck pendant").pressed.emit()
    _check("next press clears it", _note_text(p), "")
    p.action_finished("")
    p.close()
    p.free()
    _done()


func _test_success_redraws_from_the_answer() -> void:
    var log: Array = []
    var p := _panel(log)
    _button(p, "Duck pendant").pressed.emit()
    var after: Array = ITEMS.duplicate(true)
    after[1]["actions"] = ["remove"]
    p.action_finished("", after)
    _check("the row shows Remove at once", _rows(p).get("Duck pendant"), ["Remove"])
    _check("and it is pressable", _button(p, "Duck pendant").disabled, false)
    _button(p, "Duck pendant").pressed.emit()
    _check("a second press asks to remove, not to wear again", log, ["duck_pendant:wear", "duck_pendant:remove"])
    p.action_finished("", after)
    p.free()
    _done()


## /pc/me sent before the Wear, answered after it: the rows stay as the Wear's
## answer left them. A poll sent later still lands.
func _test_older_poll_does_not_undo_the_answer() -> void:
    var p := _panel([])
    # /pc/me number 5 is in flight when the Wear's answer arrives.
    _button(p, "Duck pendant").pressed.emit()
    var after: Array = ITEMS.duplicate(true)
    after[1]["actions"] = ["remove"]
    p.action_finished("", after, 5)
    p.set_inventory(ITEMS, 5)
    _check("the poll already in flight is dropped", _rows(p).get("Duck pendant"), ["Remove"])
    p.set_inventory(ITEMS, 4)
    _check("an even older one too", _rows(p).get("Duck pendant"), ["Remove"])
    p.set_inventory(ITEMS, 6)
    _check("a later poll lands", _rows(p).get("Duck pendant"), ["Wear"])
    p.set_inventory(after)
    _check("an unstamped update lands", _rows(p).get("Duck pendant"), ["Remove"])
    p.free()
    _done()


## main.gd registers ui_panel() with the camera so a press on a row button is
## not also a walk: it must cover the buttons while open and drop out closed.
func _test_ui_panel_covers_the_popover() -> void:
    var p := _panel([])
    await process_frame
    await process_frame
    var ui: Control = p.ui_panel()
    var b := _button(p, "Duck pendant")
    _check("open: visible to the camera", ui.is_visible_in_tree(), true)
    _check("open: covers a row button", ui.get_global_rect().has_point(b.get_global_rect().get_center()), true)
    p.close()
    _check("closed: out of the camera's way", ui.is_visible_in_tree(), false)
    p.free()
    _done()


## A stand-in world: just the placed_npcs map familiar_quack reads.
func _world() -> Node2D:
    var s := GDScript.new()
    s.source_code = "extends Node2D\nvar placed_npcs := {}\n"
    s.reload()
    var w := Node2D.new()
    w.set_script(s)
    root.add_child(w)
    return w


func _actor(w: Node2D, id: String, pos: Vector2, familiar_of := "") -> Node2D:
    var c := Node2D.new()
    c.position = pos
    c.set_meta("npc_id", id)
    if familiar_of != "":
        c.set_meta("familiar_of", familiar_of)
    w.add_child(c)
    w.placed_npcs[id] = c
    return c


func _quacker(w: Node2D, pc_id: String, log: Array) -> Node:
    var q: Node = load(QUACK_PATH).new()
    q.world = w
    q.pc_actor_id = pc_id
    q.quacked.connect(func(kind: String): log.append(kind))
    return q


func _test_own_duck_quacks_on_arrival() -> void:
    var w := _world()
    _actor(w, "pc", Vector2(1000, 1000))
    var duck := _actor(w, "fam-1", Vector2(0, 1000), "pc")
    var log: Array = []
    var q := _quacker(w, "pc", log)
    q._process(0.1)
    _check("far off: quiet", log.size(), 0)
    duck.set_meta("walking", {})
    duck.position = Vector2(1040, 1000)
    q._process(0.1)
    _check("beside but still walking: quiet", log.size(), 0)
    duck.remove_meta("walking")
    q._process(0.1)
    _check("comes to rest beside: one quack", log.size(), 1)
    q._process(100.0)
    _check("standing together: no more quacks (LLM-804, no timer)", log.size(), 1)
    q.free()
    w.free()
    _done()


## LLM-804: the duck quacks when the player sets off with it beside them, once
## per set-off, never twice inside MIN_GAP, and not when it is out of reach.
func _test_own_duck_quacks_when_you_set_off() -> void:
    var w := _world()
    var pc := _actor(w, "pc", Vector2(1000, 1000))
    var duck := _actor(w, "fam-1", Vector2(1040, 1000), "pc")
    var log: Array = []
    var q := _quacker(w, "pc", log)
    q._process(0.1)
    _check("arrival quack", log.size(), 1)
    q._process(5.0)
    pc.set_meta("walking", {})
    q._process(0.1)
    _check("sets off: a quack", log.size(), 2)
    q._process(0.5)
    _check("still walking: no more", log.size(), 2)
    pc.remove_meta("walking")
    q._process(0.5)
    pc.set_meta("walking", {})
    q._process(0.1)
    _check("sets off again within MIN_GAP: quiet", log.size(), 2)
    pc.remove_meta("walking")
    q._process(5.0)
    duck.position = Vector2(2000, 1000)
    pc.set_meta("walking", {})
    q._process(0.1)
    _check("sets off with the duck far behind: quiet", log.size(), 2)
    duck.position = Vector2(1032, 1000)
    q._process(0.1)
    _check("the duck catches up mid-walk: still quiet (that set-off is spent)", log.size(), 2)
    q.free()
    w.free()
    _done()


## LLM-804: lying down (inn or tent) or starting to rest brings the double
## quack after REST_DELAY, indoors too (the duck waits at the door); a rest
## already under way does not repeat it, and a duck far off stays quiet.
func _test_duck_keeps_watch_when_you_rest_or_sleep() -> void:
    var w := _world()
    var pc := _actor(w, "pc", Vector2(1000, 1000))
    var duck := _actor(w, "fam-1", Vector2(1040, 1000), "pc")
    var log: Array = []
    var q := _quacker(w, "pc", log)
    q._process(0.1)
    _check("arrival", log, ["quack"])
    pc.set_meta("inside", true)
    q.pc_slept()
    q._process(1.0)
    _check("lying down: not yet", log, ["quack"])
    q._process(1.5)
    _check("lying down: the watch quack, indoors too", log, ["quack", "watch"])
    q._process(10.0)
    _check("only once", log.size(), 2)
    pc.set_meta("inside", false)
    q.set_dwelling(PackedStringArray(["tiredness"]))
    q._process(2.5)
    _check("starts to rest: the watch quack", log, ["quack", "watch", "watch"])
    q.set_dwelling(PackedStringArray(["tiredness"]))
    q._process(2.5)
    _check("a rest under way: no repeat", log.size(), 3)
    q.set_dwelling(PackedStringArray(["thirst"]))
    q._process(2.5)
    _check("drinking is not resting", log.size(), 3)
    duck.position = Vector2(2000, 1000)
    q.set_dwelling(PackedStringArray(["tiredness"]))
    q._process(2.5)
    _check("duck far off: quiet", log.size(), 3)
    q.free()
    w.free()
    _done()


## LLM-810: the duck as the client really loads it — an /api/village/agents row
## through VillageApi.normalize_agent and world._render_npc — keeps its owner, so
## the player's own duck quacks. normalize_agent forwards a fixed list of fields,
## and familiar_of was missing from it: every loaded duck had no owner.
func _test_agents_load_tags_the_own_duck() -> void:
    var world = load("res://scripts/world.gd").new()
    var objects := Node2D.new()
    objects.name = "Objects"
    world.add_child(objects)
    root.add_child(world)
    var sheet := "tilesets/test/duck.png"
    world._npc_sheets[sheet] = ImageTexture.create_from_image(Image.create(192, 352, false, Image.FORMAT_RGBA8))
    var row := {"id": "fam-1", "display_name": "Jefferey's duck", "kind": "decorative", "state": "idle",
        "x": 80, "y": 120, "facing": "south", "familiar_of": "pc",
        "sprite": {"id": "s", "name": "Duck (mallard)", "sheet": sheet, "frame_width": 32, "frame_height": 32,
            "render_scale": 1.0, "behaviors": ["waterfowl", "ambient"], "animations": []}}
    world._render_npc(root.get_node("VillageApi").normalize_agent(row))
    var duck: Node2D = world.placed_npcs.get("fam-1")
    _check("the loaded duck knows its owner", str(duck.get_meta("familiar_of", "")) if duck != null else "<not rendered>", "pc")
    var pc := Node2D.new()
    pc.position = duck.position + Vector2(32, 0) if duck != null else Vector2.ZERO
    world.add_child(pc)
    world.placed_npcs["pc"] = pc
    var log: Array = []
    var q := _quacker(world, "pc", log)
    q._process(0.1)
    _check("and quacks beside its owner", log, ["quack"])
    q.free()
    world.free()
    _done()


func _test_no_quack_walking_indoors_or_for_another() -> void:
    var w := _world()
    var pc := _actor(w, "pc", Vector2(1000, 1000))
    _actor(w, "fam-2", Vector2(1040, 1000), "someone-else")
    var log: Array = []
    var q := _quacker(w, "pc", log)
    q._process(0.1)
    _check("another's duck beside: quiet", log.size(), 0)
    _actor(w, "fam-1", Vector2(1032, 1000), "pc")
    pc.set_meta("inside", true)
    q._process(0.1)
    _check("owner indoors: quiet", log.size(), 0)
    pc.set_meta("inside", false)
    q._process(0.1)
    _check("owner out: quacks", log.size(), 1)
    q.free()
    w.free()
    _done()


func _test_is_beside() -> void:
    var Q = load(QUACK_PATH)
    _check("3 tiles: beside", Q.is_beside(Vector2(0, 0), Vector2(96, 96)), true)
    _check("4 tiles: not", Q.is_beside(Vector2(0, 0), Vector2(128, 0)), false)
    _done()


## LLM-798: the carried tent offers Pitch; a pitched tent is a count-less row
## that offers Strike and asks for it.
func _test_tent_rows() -> void:
    var log: Array = []
    var p: Control = load(PANEL_PATH).new()
    root.add_child(p)
    p.item_action_requested.connect(func(kind: String, action: String): log.append("%s:%s" % [kind, action]))
    p.set_inventory([{"item_kind": "tent", "display_label": "Tent", "quantity": 1, "capabilities": ["shelter"], "actions": ["pitch"]}])
    p.show_at(Rect2(600, 0, 32, 32))
    _check("carried tent: Pitch", _rows(p).get("Tent"), ["Pitch"])
    p.set_inventory([{"item_kind": "tent", "display_label": "Tent — pitched by the Inn", "quantity": 0, "pitched": true, "actions": ["strike"]}])
    _check("pitched tent: a row with Strike", _rows(p).get("Tent — pitched by the Inn"), ["Strike"])
    var counts := 0
    for child in p._content.get_children():
        if child is HBoxContainer and not child.is_queued_for_deletion():
            for c in child.get_children():
                if c is Label and c.text.begins_with("×"):
                    counts += 1
    _check("pitched tent: no count", counts, 0)
    _button(p, "Tent — pitched by the Inn").pressed.emit()
    _check("Strike asks for strike", log, ["tent:strike"])
    p.set_inventory([{"item_kind": "bread", "display_label": "Bread", "quantity": 0, "actions": []}])
    _check("an empty row that is not a tent stays hidden", _rows(p).has("Bread"), false)
    p.free()
    _done()
