extends SceneTree

## Headless harness for the LLM-750 interior window: InteriorRooms seats the
## people inside a room's spots (sticky as others come and go), and
## interior_panel.gd draws them — keeper behind the bar, the player's own
## character at the door, sleepers left out, "and N more" past the spots.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/interior_panel_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## Sheets are blank synthetic textures; world.gd is instantiated off-tree via
## .new() so _ready() never fires — the panel only uses its sprite builder and
## placed_npcs.

const TESTS := [
    "_test_assign_sticky",
    "_test_assign_overflow",
    "_test_occupants",
    "_test_draws_people",
    "_test_open_close",
    "_test_person_at",
    "_test_pixel_scale",
    "_test_seat_animations",
    "_test_main_sync_interior",
    "_test_under_the_talk_panel",
    "_test_click_to_talk",
    "_test_speech_bubbles",
    "_test_bubble_survives_a_redraw",
    "_test_room_and_village_bubbles_stack_apart",
    "_test_hidden_bubble_takes_no_room",
]

const BODY := "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png"
const PACK := "/tilesets/mana-seed/npc/chef_v02.png"

var _world: Node2D = null
var _panel: CanvasLayer = null
var _sid := ""
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _sid = str(InteriorRooms.ROOMS.keys()[0])
    _world = load("res://scripts/world.gd").new()
    _world._npc_sheets = _sheets()
    _panel = CanvasLayer.new()
    _panel.set_script(load("res://scripts/interior_panel.gd"))
    root.add_child(_panel)
    _panel.world = _world
    _panel.pc_actor_id = "pc"
    _panel.texture_hook = func(_path: String) -> Texture2D: return _blank(128, 112)
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    _panel.free()
    for c in _world.placed_npcs.values():
        c.free()
    _world.free()
    print("\n[interior_panel_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[interior_panel_test] ALL PASS")
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


func _blank(w: int, h: int) -> Texture2D:
    return ImageTexture.create_from_image(Image.create(w, h, false, Image.FORMAT_RGBA8))


func _sheets() -> Dictionary:
    var out := {BODY: _blank(1024, 1024), PACK: _blank(128, 256)}
    for prop in FarmerRig.PROPS.values():
        out[prop["sheet"]] = _blank(512, 512)
    return out


func _farmer() -> Dictionary:
    return {"id": "f", "sheet": BODY, "rig": "farmer_base", "frame_width": 64, "frame_height": 64,
        "render_scale": 2.0, "layers": [{"sheet": BODY, "ramps": {}}], "animations": []}


func _pack() -> Dictionary:
    var anims := []
    var row := 0
    for d in ["south", "east", "north", "west"]:
        anims.append({"direction": d, "animation": "idle", "row_index": row, "frame_count": 1})
        row += 1
    return {"id": "p", "sheet": PACK, "frame_width": 32, "frame_height": 32, "render_scale": 2.0, "animations": anims}


## A villager's container as world.gd leaves it.
func _person(id: String, sprite: Dictionary, inside_sid: String, extra := {}) -> Node2D:
    var c := Node2D.new()
    c.set_meta("inside", inside_sid != "")
    c.set_meta("inside_structure_id", inside_sid)
    c.set_meta("sprite_data", sprite)
    for key in extra:
        c.set_meta(key, extra[key])
    _world.placed_npcs[id] = c
    return c


func _clear_people() -> void:
    for c in _world.placed_npcs.values():
        c.free()
    _world.placed_npcs.clear()


func _test_assign_sticky() -> void:
    var first: Dictionary = InteriorRooms.assign(5, ["a", "b", "c"], {})
    var spots: Dictionary = first["spots"]
    _check("three seated", spots.size(), 3)
    var used := {}
    for id in spots:
        used[spots[id]] = true
    _check("each in a spot of their own", used.size(), 3)
    var second: Dictionary = InteriorRooms.assign(5, ["a", "b", "c", "d"], spots)
    for id in ["a", "b", "c"]:
        _check("%s keeps their spot when d comes in" % id, second["spots"][id], spots[id])
    var third: Dictionary = InteriorRooms.assign(5, ["a", "c", "d"], second["spots"])
    for id in ["a", "c", "d"]:
        _check("%s keeps their spot when b leaves" % id, third["spots"][id], second["spots"][id])
    _check("same people, same spots from scratch", InteriorRooms.assign(5, ["c", "a", "b"], {})["spots"], spots)

    # The case stickiness exists for: a newcomer who sorts first and hashes to
    # the spot someone already holds. Without the memory the newcomer would
    # take it and push the seated person along.
    var seated := ""
    var newcomer := ""
    for i in 200:
        for j in 200:
            var x := "n%03d" % i
            var y := "s%03d" % j
            if x < y and absi(hash(x)) % 5 == absi(hash(y)) % 5:
                newcomer = x
                seated = y
                break
        if seated != "":
            break
    _check("found a colliding pair", seated != "", true)
    var alone: Dictionary = InteriorRooms.assign(5, [seated], {})
    var joined: Dictionary = InteriorRooms.assign(5, [seated, newcomer], alone["spots"])
    _check("the seated person keeps the spot the newcomer wanted", joined["spots"][seated], alone["spots"][seated])
    _check("the newcomer moves along", joined["spots"][newcomer] != alone["spots"][seated], true)
    _done()


func _test_assign_overflow() -> void:
    var r: Dictionary = InteriorRooms.assign(5, ["a", "b", "c", "d", "e", "f", "g"], {})
    _check("five seated", r["spots"].size(), 5)
    _check("two more", r["more"], 2)
    _check("no spots, everyone is more", InteriorRooms.assign(0, ["a"], {})["more"], 1)
    _done()


func _test_occupants() -> void:
    _clear_people()
    _person("keeper", _pack(), _sid, {"work_structure_id": _sid})
    _person("pc", _farmer(), _sid)
    _person("guest", _farmer(), _sid)
    _person("rester", _pack(), _sid, {"dormant": "resting"})
    _person("sleeper", _farmer(), _sid, {"dormant": "sleeping"})
    _person("elsewhere", _farmer(), "other-structure")
    _person("outdoors", _farmer(), "")
    _panel.structure_id = _sid
    _panel.room = InteriorRooms.room_for(_sid)
    _panel._assigned = {}
    var cast: Dictionary = _panel.occupants()
    var ids := []
    for e in cast["placed"]:
        ids.append(e["id"])
    _check("keeper first, then the others, the PC last", ids.slice(0, 1) + ids.slice(-1), ["keeper", "pc"])
    _check("the drawn people", ids.size(), 4)
    _check("sleeper left out (upstairs)", ids.has("sleeper"), false)
    _check("someone in another building left out", ids.has("elsewhere"), false)
    _check("someone outdoors left out", ids.has("outdoors"), false)
    _check("a resting villager is drawn", ids.has("rester"), true)
    _check("keeper behind the bar", cast["placed"][0]["spot"], _panel.room["keeper"])
    _check("PC at the door", cast["placed"][-1]["spot"], _panel.room["door"])
    _check("nobody over", cast["more"], 0)
    for i in 7:
        _person("crowd%d" % i, _farmer(), _sid)
    cast = _panel.occupants()
    _check("past the spots: and N more", cast["more"], 2 + 7 - 5)
    _done()


func _test_draws_people() -> void:
    _clear_people()
    _person("keeper", _pack(), _sid, {"work_structure_id": _sid})
    _person("pc", _farmer(), _sid)
    _person("rester", _pack(), _sid, {"dormant": "resting"})
    _panel.open(_sid)
    var people: Node2D = _panel._people
    _check("three drawn", people.get_child_count(), 3)
    var keeper: Node2D = people.get_node("keeper")
    var spr: AnimatedSprite2D = keeper.get_child(0)
    _check("keeper at the keeper spot", keeper.position, Vector2(_panel.room["keeper"]["x"], _panel.room["keeper"]["y"]))
    _check("drawn at the room's scale", spr.scale, Vector2.ONE)
    _check("keeper faces the spot's way", spr.animation, &"west_idle")
    var pc_spr: AnimatedSprite2D = people.get_node("pc").get_child(0)
    _check("PC at the door, facing in", pc_spr.animation, &"north_idle")
    var rest_spr: AnimatedSprite2D = people.get_node("rester").get_child(0)
    _check("a resting villager is dimmed", rest_spr.modulate, _panel.RESTING_DIM)
    _check("no more line", _panel.more_label.visible, false)

    # A farmer in a seat spot sits in the chair; a pack sprite there stands.
    _clear_people()
    _panel._assigned = {}
    var seat := -1
    var spots: Array = _panel.room["spots"]
    for i in spots.size():
        if spots[i]["pose"] == "seat":
            seat = i
            break
    _person("sitter", _farmer(), _sid)
    _person("stander", _pack(), _sid)
    _panel._assigned = {"sitter": seat, "stander": seat + 1}
    _panel._signature = ""
    _panel.refresh()
    var sit_spr: AnimatedSprite2D = people.get_node("sitter").get_child(0)
    _check("a farmer in a seat sits", sit_spr.animation, StringName(str(spots[seat]["facing"]) + "_seat"))
    _check("redraw only on change", _panel._signature != "", true)
    var before := people.get_child(0)
    _panel.refresh()
    _check("unchanged room is not rebuilt", people.get_child(0), before)

    # Overflow shows the line.
    for i in 8:
        _person("crowd%d" % i, _farmer(), _sid)
    _panel.refresh()
    _check("and N more shown", _panel.more_label.visible, true)
    _check("and N more text", _panel.more_label.text, "and 5 more")
    _panel.close()
    _done()


func _test_open_close() -> void:
    _clear_people()
    var log := []
    var on_open := func(): log.append("opened")
    var on_close := func(): log.append("closed")
    _panel.opened.connect(on_open)
    _panel.closed.connect(on_close)
    _panel.open("no-room-here")
    _check("a building without a room does not open", _panel.is_open(), false)
    _panel.open(_sid)
    _panel.open(_sid)
    _check("opened once", log, ["opened"])
    _check("open", _panel.is_open(), true)
    _check("title", _panel.title_label.text, str(_panel.room["title"]))
    _panel.close()
    _panel.close()
    _check("closed once", log, ["opened", "closed"])
    _panel.opened.disconnect(on_open)
    _panel.closed.disconnect(on_close)
    _done()


func _test_person_at() -> void:
    _clear_people()
    _person("keeper", _pack(), _sid, {"work_structure_id": _sid})
    _panel.open(_sid)
    _panel.k = 4
    _panel._stretch = Vector2.ONE
    _panel._scene.position = Vector2.ZERO
    var feet := Vector2(_panel.room["keeper"]["x"], _panel.room["keeper"]["y"])
    _check("a click on the keeper finds them", _panel.person_at((feet + Vector2(0, -10)) * 4), "keeper")
    _check("a click on bare floor finds nobody", _panel.person_at(Vector2(2, 2) * 4), "")
    _panel.close()
    _done()


func _test_pixel_scale() -> void:
    var art := Vector2(128, 112)
    _check("1280x720 window: 4x", _panel.pixel_scale_for(Vector2(1178, 518), art), 4)
    _check("phone width: 2x", _panel.pixel_scale_for(Vector2(331, 460), art), 2)
    _check("never below 1x", _panel.pixel_scale_for(Vector2(50, 50), art), 1)
    _check("capped at 4x", _panel.pixel_scale_for(Vector2(4000, 4000), art), 4)
    _done()


## main.gd's _sync_interior, driven off-tree: opens on entering, follows a new
## actor id on reconnect while still inside, keeps a ✕ shut, closes on leaving.
func _test_main_sync_interior() -> void:
    _clear_people()
    _panel.close()
    var main: Node2D = load("res://scripts/main.gd").new()
    main.world = _world
    main.interior_panel_layer = _panel
    main._pc_actor_id = "pc"
    var pc := _person("pc", _farmer(), _sid)
    main._sync_interior()
    _check("opens on entering", _panel.is_open(), true)
    _check("knows the character", _panel.pc_actor_id, "pc")

    # Reconnect: a new actor id for the character, same room.
    pc.free()
    _world.placed_npcs.erase("pc")
    _person("pc2", _farmer(), _sid)
    main._pc_actor_id = "pc2"
    main._sync_interior()
    _check("follows the new actor id", _panel.pc_actor_id, "pc2")
    var cast: Dictionary = _panel.occupants()
    _check("the character is at the door, not in a seat", cast["placed"][-1], _panel._entry("pc2", _panel.room["door"]))

    _panel.close()
    main._sync_interior()
    _check("the ✕ stays shut while inside", _panel.is_open(), false)

    _world.placed_npcs["pc2"].set_meta("inside", false)
    _world.placed_npcs["pc2"].set_meta("inside_structure_id", "")
    main._sync_interior()
    _world.placed_npcs["pc2"].set_meta("inside", true)
    _world.placed_npcs["pc2"].set_meta("inside_structure_id", _sid)
    main._sync_interior()
    _check("going back in opens it again", _panel.is_open(), true)
    _world.placed_npcs["pc2"].set_meta("inside", false)
    main._sync_interior()
    _check("closes on leaving", _panel.is_open(), false)
    main.free()
    _done()


## A person click opens the talk panel; it must come up in front of the room.
func _test_under_the_talk_panel() -> void:
    var talk_layer := _ready_layer_of("res://scripts/talk_panel.gd")
    _check("talk panel layer found", talk_layer > 0, true)
    _check("the room sits under the talk panel", _panel.layer < talk_layer, true)
    _done()


## The click-to-talk path through main.gd's own handlers: a real click on the
## stage finds the person, person_clicked reaches main, the talk panel opens,
## it sits above the room, and the room stays open behind it. The talk panel
## is a stand-in (the real one starts its requests when built), on the layer
## the real one sets for itself.
func _test_click_to_talk() -> void:
    _clear_people()
    var stub_script := GDScript.new()
    stub_script.source_code = "extends CanvasLayer\nvar opened := 0\nfunc open() -> void:\n    opened += 1\n"
    stub_script.reload()
    var talk := CanvasLayer.new()
    talk.set_script(stub_script)
    talk.layer = _ready_layer_of("res://scripts/talk_panel.gd")
    var main: Node2D = load("res://scripts/main.gd").new()
    main.world = _world
    main.talk_panel_layer = talk
    main.interior_panel_layer = _panel
    main._place_interior_under_talk()
    var on_click := Callable(main, "_on_interior_person_clicked")
    _panel.person_clicked.connect(on_click)

    _person("keeper", _pack(), _sid, {"work_structure_id": _sid})
    _panel.open(_sid)
    _panel.k = 4
    _panel._stretch = Vector2.ONE
    _panel._scene.position = Vector2.ZERO
    var click := InputEventMouseButton.new()
    click.button_index = MOUSE_BUTTON_LEFT
    click.pressed = true
    click.position = (Vector2(_panel.room["keeper"]["x"], _panel.room["keeper"]["y"]) + Vector2(0, -10)) * 4
    _panel._on_stage_input(click)
    _check("a click on a person opens the talk panel", talk.get("opened"), 1)
    _check("talk sits above the room", talk.layer > _panel.layer, true)
    _check("the room stays open behind the talk", _panel.is_open(), true)
    click.position = Vector2(2, 2) * 4
    _panel._on_stage_input(click)
    _check("a click on bare floor opens nothing", talk.get("opened"), 1)

    _panel.person_clicked.disconnect(on_click)
    _panel.close()
    main.free()
    talk.free()
    _done()


## LLM-751: a line spoken by someone drawn in the room shows over their head.
func _test_speech_bubbles() -> void:
    _clear_people()
    _person("keeper", _pack(), _sid, {"work_structure_id": _sid})
    _person("guest", _farmer(), _sid)
    _panel.open(_sid)
    _panel.say("keeper", "Welcome in.")
    var keeper: Node2D = _panel._people.get_node("keeper")
    var bubble: Node2D = keeper.get_node_or_null(_panel.BUBBLE_NODE)
    _check("a bubble over the keeper", bubble != null, true)
    _check("anchored just above a head in room pixels", bubble.anchor_y, _panel.BUBBLE_ANCHOR_Y)
    _check("the keeper, on the right, gets a bubble that opens left", bubble.opens_left, true)
    _panel.say("keeper", "Stew is three coins.")
    var bubbles := keeper.get_children().filter(func(c): return str(c.name).begins_with(_panel.BUBBLE_NODE))
    _check("a newer line replaces the bubble", bubbles.size(), 1)
    _check("the newer bubble is a new one", bubbles[0] != bubble, true)
    _panel.say("nobody-here", "Hello?")
    _check("no bubble for someone not drawn", _panel._people.get_node_or_null("nobody-here"), null)
    # The real path: world.gd's npc_spoke signal.
    _world.npc_spoke.emit("guest", "Guest", "Evening.", "speak", "", "", [], 0.0, 0.0, "", "", "", {}, true, [])
    _check("npc_spoke puts a bubble over the guest",
        _panel._people.get_node("guest").get_node_or_null(_panel.BUBBLE_NODE) != null, true)
    # The character at the door, in the middle of the room, opens right.
    _panel.pc_actor_id = "pc"
    _person("pc", _farmer(), _sid)
    _panel.refresh()
    _panel.say("pc", "Evening.")
    var pc_bubble: Node2D = _panel._people.get_node("pc").get_node(_panel.BUBBLE_NODE)
    _check("the character at the door opens right", pc_bubble.opens_left, false)
    _panel.close()
    _done()


func _test_bubble_survives_a_redraw() -> void:
    _clear_people()
    _person("keeper", _pack(), _sid, {"work_structure_id": _sid})
    _panel.open(_sid)
    _panel.say("keeper", "Welcome in.")
    var bubble: Node2D = _panel._people.get_node("keeper").get_node(_panel.BUBBLE_NODE)
    _person("newcomer", _farmer(), _sid)
    _panel.refresh()
    _check("the room was redrawn", _panel._people.get_node_or_null("newcomer") != null, true)
    # A freed object compares equal to null, so check both sides are live.
    var carried: Node = _panel._people.get_node("keeper").get_node_or_null(_panel.BUBBLE_NODE)
    _check("the keeper's bubble carried over", carried != null and is_instance_valid(bubble) and carried == bubble, true)
    _panel.say("newcomer", "Is there stew?")
    _world.placed_npcs["newcomer"].set_meta("inside", false)
    _panel.refresh()
    _check("a speaker who left takes their bubble along", _panel._people.get_node_or_null("newcomer"), null)
    _panel.close()
    _done()


## The room's bubbles and the village's never meet in one stack: a village
## bubble at the very spot of a room bubble does not lift it.
func _test_room_and_village_bubbles_stack_apart() -> void:
    _clear_people()
    _person("keeper", _pack(), _sid, {"work_structure_id": _sid})
    _panel.open(_sid)
    _panel.say("keeper", "Welcome in.")
    var room_bubble: Node2D = _panel._people.get_node("keeper").get_node(_panel.BUBBLE_NODE)
    var head: Vector2 = (room_bubble.get_parent() as Node2D).get_global_transform_with_canvas() * Vector2(0, _panel.BUBBLE_ANCHOR_Y)
    var speaker := Node2D.new()
    speaker.position = head
    root.add_child(speaker)
    var village: Node2D = _panel.SpeechBubbleScript.new()
    village.anchor_y = 0.0
    speaker.add_child(village)
    village.setup("Welcome in.")
    var both := [room_bubble, village]
    _panel.SpeechBubbleScript._layout(both, 10.0)
    _check("harness — the two would overlap", room_bubble._canvas_rect().intersects(village._canvas_rect()), true)
    _check("the room bubble stays at its speaker's head", room_bubble._lift_target, 0.0)
    _check("the village bubble stays at its speaker's head", village._lift_target, 0.0)
    speaker.free()
    _panel.close()
    _done()


## A bubble whose speaker is hidden (indoors on the map) takes no room: an
## overlapping visible bubble on the same canvas stays at its head.
func _test_hidden_bubble_takes_no_room() -> void:
    var a := Node2D.new()
    var b := Node2D.new()
    a.position = Vector2(-7000, -7000)
    b.position = Vector2(-7000 + 8, -7000)
    root.add_child(a)
    root.add_child(b)
    # The hidden one is the newer, so visible it would push the other up.
    var shown: Node2D = _panel.SpeechBubbleScript.new()
    b.add_child(shown)
    shown.setup("Out here.")
    var hidden: Node2D = _panel.SpeechBubbleScript.new()
    a.add_child(hidden)
    hidden.setup("Inside, unseen.")
    _panel.SpeechBubbleScript._layout([hidden, shown], 10.0)
    _check("harness — both visible, the older is pushed up", shown._lift_target > 0.0, true)
    a.visible = false
    hidden._lift_target = 0.0
    shown._lift_target = 0.0
    _panel.SpeechBubbleScript._layout([hidden, shown], 10.0)
    _check("with its speaker hidden it does not lift the other", shown._lift_target, 0.0)
    a.free()
    b.free()
    _done()


## The layer a CanvasLayer script sets for itself in _ready, read from its
## source (building the talk panel would start its requests).
func _ready_layer_of(path: String) -> int:
    var src := FileAccess.get_file_as_string(path)
    var at := src.find("func _ready()")
    var re := RegEx.create_from_string("\\n\\s+layer = (\\d+)")
    var m := re.search(src, at)
    return int(m.get_string(1)) if m != null else -1


func _test_seat_animations() -> void:
    var anims := FarmerDoll.rig_animations()
    for f in ["south", "north", "east", "west"]:
        _check("%s_seat exists" % f, anims.has(f + "_seat"), true)
    _done()
