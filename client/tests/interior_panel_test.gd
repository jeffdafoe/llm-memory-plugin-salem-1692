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
    "_test_carried_bubble_turns_with_its_speaker",
    "_test_room_and_village_bubbles_stack_apart",
    "_test_hidden_bubble_takes_no_room",
    "_test_stall_lay_out",
    "_test_stall_at",
    "_test_standing_tile",
    "_test_stall_occupants",
    "_test_stall_draws",
    "_test_smithy_layout",
    "_test_smithy_bellows",
    "_test_smithy_anvil",
    "_test_smithy_anvil_interrupted",
    "_test_scrolling_stage",
    "_test_main_sync_stall",
    "_test_close_glyph_in_font",
    "_test_walk_out",
    "_test_walk_in",
    "_test_keeper_bustles",
    "_test_name_tags",
    "_test_stall_hands",
    "_test_walk_in_survives_a_redraw",
    "_test_leave_return_leave",
    "_test_leaver_takes_their_bubble",
    "_test_sale_handed_over_the_counter",
    "_test_sales_handed_over_in_turn",
    "_test_handoff_survives_a_redraw",
    "_test_purchase_over_the_counter",
    "_test_no_handoff",
    "_test_tavern_serves_a_meal_to_the_table",
    "_test_tavern_serves_standing_and_at_the_door",
    "_test_tavern_keeper_buys",
    "_test_tavern_keeper_back_is_theirs",
    "_test_reopen_stops_a_handoff",
    "_test_world_meals",
    "_test_tavern_second_helping_one_dish",
    "_test_tavern_guest_leaving_clears_the_dish",
    "_test_tavern_ale_lingers_then_cleared",
    "_test_tavern_take_home_handed_over",
    "_test_tavern_reopen_lays_a_running_meal",
    "_test_inn_keeper_behind_the_desk",
    "_test_inn_animations",
    "_test_inn_serves_porridge_to_the_bench",
    "_test_room_pieces",
    "_test_room_squares",
]

const STALL := "stall-1"
const STALL_ASSET := "asset-stall"

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
    # A desktop window: the headless one is tiny, and every room would scroll
    # (LLM-763).
    root.size = Vector2i(1280, 720)
    _sid = str(InteriorRooms.ROOMS.keys()[0])
    _world = load("res://scripts/world.gd").new()
    _world._npc_sheets = _sheets()
    _panel = CanvasLayer.new()
    _panel.set_script(load("res://scripts/interior_panel.gd"))
    root.add_child(_panel)
    _panel.world = _world
    _panel.pc_actor_id = "pc"
    _panel.texture_hook = func(_path: String) -> Texture2D: return _blank(128, 112)
    _load_room_plans()
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    _panel.free()
    for c in _world.placed_npcs.values():
        c.free()
    for o in _world.placed_objects.values():
        o.free()
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


## A villager's container as world.gd leaves it — inside a room, with the
## square the engine would deal them (_deal_square).
func _person(id: String, sprite: Dictionary, inside_sid: String, extra := {}) -> Node2D:
    var c := Node2D.new()
    c.set_meta("inside", inside_sid != "")
    c.set_meta("inside_structure_id", inside_sid)
    c.set_meta("sprite_data", sprite)
    for key in extra:
        c.set_meta(key, extra[key])
    _world.placed_npcs[id] = c
    _deal_square(id, c, inside_sid)
    return c


## The Catalog autoload (its global name does not compile in a --script run).
func _catalog() -> Node:
    return root.get_node("Catalog")


## The engine's own floor plans (engine/sim/roomlayout/layouts.json), as
## /api/village/rooms serves them, so the client is tested against the plans
## the engine places people on.
func _load_room_plans() -> void:
    var path := ProjectSettings.globalize_path("res://").path_join("../engine/sim/roomlayout/layouts.json")
    var parsed = JSON.parse_string(FileAccess.get_file_as_string(path))
    _check("harness — the engine's room plans load", parsed is Dictionary, true)
    if parsed is Dictionary:
        _catalog().set_rooms(parsed["rooms"])


## A square as the engine deals one (sim/room_square.go): the keeper to the
## keeper's place, the player's character to the door, anyone else to the first
## free station, then the first free open square that is no station; nothing
## past those, and nothing asleep. (The engine starts guests at a hashed
## station; the order does not matter to the window.)
func _deal_square(id: String, c: Node2D, sid: String) -> void:
    var plan: Dictionary = _catalog().rooms.get(sid, {})
    if plan.is_empty() or str(c.get_meta("dormant", "")) == "sleeping":
        return
    var taken := {}
    for other in _world.placed_npcs:
        var o: Node2D = _world.placed_npcs[other]
        if other != id and str(o.get_meta("inside_structure_id", "")) == sid and o.has_meta("room_square"):
            taken[InteriorRooms.square_of(o.get_meta("room_square"))] = true
    var first: Array = []
    if id.begins_with("pc"):
        first.append(plan["door"]["square"])
    elif str(c.get_meta("work_structure_id", "")) == sid:
        first.append(plan["keeper"]["square"])
    var reserved := {InteriorRooms.square_of(plan["door"]["square"]): true, InteriorRooms.square_of(plan["keeper"]["square"]): true}
    if plan.get("keeper_door", null) is Dictionary:
        reserved[InteriorRooms.square_of(plan["keeper_door"]["square"])] = true
    for st in plan["stations"]:
        first.append(st["square"])
        reserved[InteriorRooms.square_of(st["square"])] = true
    for y in plan["grid"].size():
        var row: String = plan["grid"][y]
        for x in row.length():
            if row[x] == "." and not reserved.has(Vector2i(x, y)):
                first.append([x, y])
    for sq in first:
        if not taken.has(InteriorRooms.square_of(sq)):
            c.set_meta("room_square", [int(sq[0]), int(sq[1])])
            return


## Put `id` on the room's station `i` (in plan order), as if the engine had.
func _seat(id: String, i: int) -> void:
    var c: Node2D = _world.placed_npcs[id]
    var plan: Dictionary = _catalog().rooms[str(c.get_meta("inside_structure_id"))]
    var sq: Array = plan["stations"][i]["square"]
    c.set_meta("room_square", [int(sq[0]), int(sq[1])])


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
    _panel.room = InteriorRooms.with_layout(InteriorRooms.room_for(_sid), _catalog().rooms[_sid])
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
    # The Tavern has 5 stations and 4 open squares besides: 14 others, 5 over.
    for i in 12:
        _person("crowd%d" % i, _farmer(), _sid)
    cast = _panel.occupants()
    _check("past the squares: and N more", cast["more"], 2 + 12 - 9)
    _done()


func _test_draws_people() -> void:
    _clear_people()
    _person("keeper", _pack(), _sid, {"work_structure_id": _sid})
    _person("pc", _farmer(), _sid)
    _person("rester", _pack(), _sid, {"dormant": "resting"})
    _panel.open(_sid)
    var people: Node2D = _panel._people
    _check("three drawn", _drawn(people).size(), 3)
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
    _seat("sitter", seat)
    _seat("stander", seat + 1)
    _panel._signature = ""
    _panel.refresh()
    # Newcomers walk in (LLM-754); once there, the farmer sits.
    _finish_walk(people.get_node("sitter"))
    var sit_spr: AnimatedSprite2D = people.get_node("sitter").get_child(0)
    _check("a farmer in a seat sits", sit_spr.animation, StringName(str(spots[seat]["facing"]) + "_seat"))
    _check("redraw only on change", _panel._signature != "", true)
    var before: Node = _drawn(people)[0]
    _panel.refresh()
    _check("unchanged room is not rebuilt", _drawn(people)[0], before)

    # Overflow shows the line.
    for i in 8:
        _person("crowd%d" % i, _farmer(), _sid)
    _panel.refresh()
    _check("and N more shown", _panel.more_label.visible, true)
    _check("and N more text", _panel.more_label.text, "and 1 more")
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
    # LLM-763: how much of a room shows at a scale — all of it when it fits,
    # else whole art px as far as the window reaches.
    var wide := Vector2(192, 112)
    _check("desktop shows all of a wide room", _panel.view_for(Vector2(1178, 518), wide, 4), wide)
    _check("a phone shows part of it at 2x", _panel.view_for(Vector2(331, 460), wide, 2), Vector2(165, 112))
    _check("never less than one art px", _panel.view_for(Vector2(0, 0), wide, 2), Vector2(1, 1))
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


## LLM-752: a bubble carried over a redraw that moved its speaker to the other
## side of the room turns to open the other way.
func _test_carried_bubble_turns_with_its_speaker() -> void:
    _clear_people()
    var spots: Array = InteriorRooms.with_layout(InteriorRooms.room_for(_sid), _catalog().rooms[_sid])["spots"]
    var west := -1
    var east := -1
    var half: float = InteriorRooms.room_for(_sid)["size"].x / 2.0
    for i in spots.size():
        if west < 0 and float(spots[i]["x"]) < half:
            west = i
        if east < 0 and float(spots[i]["x"]) > half:
            east = i
    _check("harness — a spot on each side", west >= 0 and east >= 0, true)
    _person("guest", _farmer(), _sid)
    _panel.open(_sid)
    _seat("guest", west)
    _panel._signature = ""
    _panel.refresh()
    _panel.say("guest", "Over here.")
    var bubble: Node2D = _panel._people.get_node("guest").get_node(_panel.BUBBLE_NODE)
    _check("on the left it opens right", bubble.opens_left, false)
    _seat("guest", east)
    _panel._signature = ""
    _panel.refresh()
    var carried: Node = _panel._people.get_node("guest").get_node_or_null(_panel.BUBBLE_NODE)
    _check("the bubble carried over", carried != null and is_instance_valid(bubble) and carried == bubble, true)
    _check("moved to the right, it opens left", bubble.opens_left, true)
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


## A stall placed at tile (10, 20) with its loiter pin at (8, 21), and a
## plain object (a well) at a pin of its own: the Catalog knows the stall's
## asset by name.
func _place_stall() -> Node2D:
    var catalog: Node = root.get_node("Catalog")
    catalog.assets[STALL_ASSET] = {"name": "Market Stall (Tiled)"}
    catalog.assets["asset-well"] = {"name": "Well"}
    for id in _world.placed_objects.keys():
        _world.placed_objects[id].free()
    _world.placed_objects.clear()
    var stall := Node2D.new()
    stall.position = Vector2(10 * 32 + 5, 20 * 32 + 30)
    stall.set_meta("asset_id", STALL_ASSET)
    stall.set_meta("display_name", "General Store")
    stall.set_meta("effective_loiter_offset_x", -2)
    stall.set_meta("effective_loiter_offset_y", 1)
    _world.placed_objects[STALL] = stall
    var well := Node2D.new()
    well.position = Vector2(30 * 32, 20 * 32)
    well.set_meta("asset_id", "asset-well")
    well.set_meta("effective_loiter_offset_x", 0)
    well.set_meta("effective_loiter_offset_y", 1)
    _world.placed_objects["well"] = well
    return stall


## The Catalog autoload's asset table (the autoload's global name does not
## compile in a --script run; the node is there).
func _assets() -> Dictionary:
    return root.get_node("Catalog").assets


## A person standing on map tile `tile`.
func _at_tile(id: String, sprite: Dictionary, tile: Vector2i, extra := {}) -> Node2D:
    var c := _person(id, sprite, "", extra)
    c.position = Vector2(tile.x * 32 + 16, tile.y * 32 + 16)
    return c


func _test_stall_lay_out() -> void:
    var wares := [
        {"item": "apples", "stock": "plenty"},
        {"item": "nail", "stock": "few"},
        {"item": "skillet", "stock": "one"},
        {"item": "no_such_good", "stock": "one"},
    ]
    var slots := StallScenes.lay_out(wares)
    _check("every ware gets a slot", slots.size(), 4)
    _check("a pile is the good's pile cell", slots[0]["cell"], 2 * StallScenes.GOODS["apples"] + 1)
    _check("a pile stands on the first floor slot", slots[0]["at"], Vector2i(StallScenes.FLOOR_SLOTS[0], StallScenes.FLOOR_Y - 32))
    _check("a few: the good, twice, on the counter", [slots[1]["cell"], slots[1]["count"]], [2 * StallScenes.GOODS["nail"], 2])
    _check("on the first counter slot", slots[1]["at"], Vector2i(StallScenes.COUNTER_SLOTS[0], StallScenes.COUNTER_Y - 32))
    _check("one: the good once", slots[2]["count"], 1)
    _check("a good with no picture is a crate", slots[3]["cell"], 2 * StallScenes.OTHER)

    # More piles than floor: the rest lie on the counter as a few; past the
    # counter nothing more is drawn.
    var many := []
    for i in 10:
        many.append({"item": "apples", "stock": "plenty"})
    for i in 5:
        many.append({"item": "nail", "stock": "one"})
    slots = StallScenes.lay_out(many)
    _check("every slot used, no more", slots.size(), StallScenes.FLOOR_SLOTS.size() + StallScenes.COUNTER_SLOTS.size())
    _check("the seventh pile lies on the counter", [slots[6]["cell"], slots[6]["count"]], [2 * StallScenes.GOODS["apples"], 2])
    _check("nothing empty", StallScenes.lay_out([]), [])

    # A trade's own floor slots (the smithy keeps the forge's side clear):
    # piles past them go to the counter.
    var scene := {"floor_slots": [78, 94, 110], "counter_slots": [140, 156]}
    slots = StallScenes.lay_out(many, scene)
    _check("piles on the scene's slots", [slots[0]["at"].x, slots[2]["at"].x], [78, 110])
    _check("the fourth pile lies on the scene's counter", [slots[3]["cell"], slots[3]["at"]], [2 * StallScenes.GOODS["apples"], Vector2i(140, StallScenes.COUNTER_Y - 32)])
    _check("no more than the scene's slots", slots.size(), 5)
    _done()


func _test_stall_at() -> void:
    _place_stall()
    var pin := Vector2i(8, 21)
    _check("the pin from the anchor tile and the offset", StallScenes.pin_tile(_world.placed_objects[STALL]), pin)
    _check("on the pin", StallScenes.stall_at(_world, _assets(), pin), STALL)
    _check("beside the pin", StallScenes.stall_at(_world, _assets(), pin + Vector2i(1, 1)), STALL)
    _check("two away is not at the counter", StallScenes.stall_at(_world, _assets(), pin + Vector2i(2, 0)), "")
    _check("at a well is at no stall", StallScenes.stall_at(_world, _assets(), Vector2i(30, 21)), "")
    _check("a stall has a scene", StallScenes.scene_for(_world, _assets(), STALL)["sheet"], StallScenes.SHEETS["Market Stall (Tiled)"])
    _check("titled by its name", StallScenes.scene_for(_world, _assets(), STALL)["title"], "General Store")
    _check("a well has none", StallScenes.scene_for(_world, _assets(), "well"), {})
    _check("an untagged stall lays piles on both sides", StallScenes.scene_for(_world, _assets(), STALL)["floor_slots"], StallScenes.FLOOR_SLOTS)

    # LLM-762: a stall tagged with a trade draws its roof's stall dressed for
    # the trade, whatever its id; other tags change nothing.
    var stall: Node2D = _world.placed_objects[STALL]
    stall.set_meta("tags", ["business", "shop", "market_stall"])
    _check("a shop tag keeps the plain stall", StallScenes.scene_for(_world, _assets(), STALL)["sheet"], "/tilesets/interiors/stall_tiled.png")
    var plain := StallScenes.scene_for(_world, _assets(), STALL)
    _check("a plain stall's layout", [plain["size"], plain["counter"], plain["counter_from"], plain["counter_slots"], plain["keeper"]],
        [StallScenes.SIZE, StallScenes.COUNTER_SHEET, 0, StallScenes.COUNTER_SLOTS, StallScenes.KEEPER])
    stall.set_meta("tags", ["business", "smithy", "market_stall"])
    var smithy := StallScenes.scene_for(_world, _assets(), STALL)
    _check("a smithy tag dresses the roof's stall", smithy["sheet"], "/tilesets/interiors/stall_tiled_smithy.png")
    _check("a smithy lays piles clear of the forge", smithy["floor_slots"], StallScenes.TRADES["smithy"]["floor_slots"])
    # LLM-763: the trade's layout replaces the plain stall's, field by field.
    _check("the smithy is 192 wide", smithy["size"], Vector2i(192, 112))
    _check("with its own counter on the selling side", [smithy["counter"], smithy["counter_from"]], ["/tilesets/interiors/stall_counter_smithy.png", 64])
    _check("the keeper in the selling side's middle", smithy["keeper"]["x"], 128)
    _check("a hand's spot at the forge", smithy["hands"][0]["x"] < 64, true)
    _check("still its own title and pin", [smithy["title"], smithy["pin"]], ["General Store", pin])
    _check("still found as a stall at its pin", StallScenes.stall_at(_world, _assets(), pin), STALL)
    stall.set_meta("tags", "smithy")
    _check("tags that are not a list are no trade", StallScenes.trade_of(stall), "")
    stall.remove_meta("tags")
    _done()


func _test_standing_tile() -> void:
    _clear_people()
    var c := _at_tile("walker", _farmer(), Vector2i(3, 4))
    _check("standing still: where they are", StallScenes.standing_tile(c), Vector2i(3, 4))
    c.set_meta("walking", {"path": [Vector2(9 * 32 + 16, 9 * 32 + 16)]})
    _check("walking a route: nowhere yet", StallScenes.standing_tile(c).x < -1000, true)
    c.set_meta("walking", {"path": [Vector2(5 * 32 + 16, 6 * 32 + 16)], "finish_idle": true})
    _check("an arrival's last steps: where they will stand", StallScenes.standing_tile(c), Vector2i(5, 6))
    _done()


func _test_stall_occupants() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return []
    _panel.open(STALL)
    _check("a stall opens the window", _panel.is_stall(), true)
    _person("keeper", _pack(), STALL, {"work_structure_id": STALL})
    _at_tile("pc", _farmer(), pin)
    _at_tile("buyer", _farmer(), pin + Vector2i(-1, 1))
    _at_tile("sleeper", _farmer(), pin + Vector2i(1, 0), {"dormant": "sleeping"})
    # Walking a route that runs through the pin: on the way, not at the counter.
    _at_tile("passer", _farmer(), pin + Vector2i(0, 1), {"walking": {"path": [Vector2(pin * 32) + Vector2(16, 16)]}})
    _at_tile("faraway", _farmer(), pin + Vector2i(3, 0))
    _person("indoors", _farmer(), "tavern")
    _panel._assigned = {}
    var cast: Dictionary = _panel.occupants()
    var ids := []
    for e in cast["placed"]:
        ids.append(e["id"])
    _check("keeper, the customer, the player's character", ids, ["keeper", "buyer", "pc"])
    _check("the keeper stands behind the counter", cast["placed"][0].get("behind", false), true)
    _check("at the keeper's spot", cast["placed"][0]["spot"], StallScenes.KEEPER)
    _check("a customer stands in front", cast["placed"][1].get("behind", false), false)
    _check("the player's character front and centre", cast["placed"][2]["spot"], StallScenes.DOOR)
    _check("nobody over", cast["more"], 0)

    # Hands working beside the keeper (LLM-759): two at the hands' spots, a
    # third counted.
    _person("worker-a", _farmer(), STALL)
    _person("worker-b", _farmer(), STALL, {"work_structure_id": "elsewhere"})
    _person("worker-c", _farmer(), STALL)
    cast = _panel.occupants()
    var behind := {}
    for e in cast["placed"]:
        if e.get("behind", false):
            behind[e["id"]] = e
    _check("the keeper and two hands behind the counter", behind.size(), 3)
    _check("the keeper marked, at the keeper's spot", [behind["keeper"].get("keeper", false), behind["keeper"]["spot"]], [true, StallScenes.KEEPER])
    var hand_spots := []
    for id in behind:
        if id != "keeper":
            _check("%s is a hand, not the keeper" % id, [behind[id].get("hand", false), behind[id].get("keeper", false)], [true, false])
            hand_spots.append(behind[id]["spot"])
    _check("the hands at the two hands' spots", hand_spots.has(StallScenes.HANDS[0]) and hand_spots.has(StallScenes.HANDS[1]), true)
    _check("the third hand: and 1 more", cast["more"], 1)
    var kept: Dictionary = _panel._assigned_hands.duplicate()
    _person("worker-0", _farmer(), STALL)
    cast = _panel.occupants()
    var still := {}
    for id in kept:
        still[id] = _panel._assigned_hands.get(id, -1)
    _check("a hand who comes later does not move the others", still, kept)
    for id in ["worker-a", "worker-b", "worker-c", "worker-0"]:
        _world.placed_npcs[id].free()
        _world.placed_npcs.erase(id)

    # The keeper away: a hand tending the stall stands behind the counter.
    _world.placed_npcs["keeper"].free()
    _world.placed_npcs.erase("keeper")
    _person("hand", _farmer(), STALL, {"work_structure_id": "elsewhere"})
    cast = _panel.occupants()
    _check("a hand behind the counter", [cast["placed"][0]["id"], cast["placed"][0].get("behind", false)], ["hand", true])

    # More at the counter than spots: counted.
    for i in 5:
        _at_tile("crowd%d" % i, _farmer(), pin + Vector2i(1, -1))
    cast = _panel.occupants()
    _check("past the spots: and N more", cast["more"], 1 + 5 - StallScenes.SPOTS.size())
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


func _test_stall_draws() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    var asked := []
    _panel.wares_hook = func(sid: String) -> Array:
        asked.append(sid)
        return [{"item": "apples", "stock": "plenty"}, {"item": "nail", "stock": "few"}]
    _person("keeper", _pack(), STALL, {"work_structure_id": STALL})
    _at_tile("buyer", _farmer(), pin + Vector2i(1, 1))
    _panel.close()
    _panel.open(STALL)
    _check("the shelf is asked for on opening", asked, [STALL])
    _check("title", _panel.title_label.text, "General Store")
    _check("the counter is drawn", _panel._counter.texture != null, true)
    _check("the keeper behind the counter", _panel._staff.get_node_or_null("keeper") != null, true)
    _check("the customer in front of it", _panel._people.get_node_or_null("buyer") != null, true)
    _check("the counter between them", _panel._counter.get_index() > _panel._staff.get_index()
        and _panel._counter.get_index() < _panel._people.get_index(), true)
    _check("a pile on the floor", _panel._goods_back.get_child_count(), 1)
    _check("a few on the counter: two", _panel._goods_front.get_child_count(), 2)
    _check("the floor behind the keeper", _panel._goods_back.get_index() < _panel._staff.get_index(), true)
    var pile: Sprite2D = _panel._goods_back.get_child(0)
    _check("the pile's cell", (pile.texture as AtlasTexture).region, StallScenes.cell_rect(2 * StallScenes.GOODS["apples"] + 1))
    _check("the pile on the first floor slot", pile.position.x, float(StallScenes.FLOOR_SLOTS[0]))

    # LLM-762: a smithy draws its own backdrop and keeps its piles off the
    # forge's side.
    _world.placed_objects[STALL].set_meta("tags", ["smithy"])
    _panel.close()
    _panel.open(STALL)
    _check("the smithy backdrop", _panel.room["sheet"], "/tilesets/interiors/stall_tiled_smithy.png")
    pile = _panel._goods_back.get_child(0)
    _check("the smithy's pile on its first slot", pile.position.x, float(StallScenes.TRADES["smithy"]["floor_slots"][0]))
    _world.placed_objects[STALL].remove_meta("tags")
    _panel.close()
    _panel.open(STALL)

    # The keeper's line shows over their head behind the counter.
    _panel.say("keeper", "Fresh apples today.")
    _check("the keeper's bubble", _panel._staff.get_node("keeper").get_node_or_null(_panel.BUBBLE_NODE) != null, true)

    # A newcomer at the counter redraws everyone: the keeper is rebuilt, not
    # doubled, and keeps the bubble.
    _at_tile("newcomer", _farmer(), pin + Vector2i(-1, 0))
    _panel.refresh()
    _check("one keeper after a redraw", _panel._staff.get_child_count(), 1)
    _check("the newcomer drawn", _panel._people.get_node_or_null("newcomer") != null, true)
    _check("the keeper's bubble carried over", _panel._staff.get_node("keeper").get_node_or_null(_panel.BUBBLE_NODE) != null, true)

    # The shelf sells out.
    _panel.set_wares([])
    _check("a bare shelf", _panel._goods_back.get_child_count() + _panel._goods_front.get_child_count(), 0)

    # A room draws no goods and no counter.
    _panel.open(_sid)
    _check("a room has no counter", _panel._counter.texture, null)
    _panel.set_wares([{"item": "apples", "stock": "plenty"}])
    _check("a room has no goods", _panel._goods_back.get_child_count(), 0)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## main.gd's _sync_interior for a stall: arriving at the counter opens it,
## walking away closes it, the ✕ stays shut while there.
func _test_main_sync_stall() -> void:
    _clear_people()
    _place_stall()
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return []
    var main: Node2D = load("res://scripts/main.gd").new()
    main.world = _world
    main.interior_panel_layer = _panel
    main._pc_actor_id = "pc"
    var pc := _at_tile("pc", _farmer(), Vector2i(8, 22))
    main._sync_interior()
    _check("opens at the counter", [_panel.is_open(), _panel.structure_id], [true, STALL])
    _panel.close()
    main._sync_interior()
    _check("the ✕ stays shut while there", _panel.is_open(), false)
    pc.set_meta("walking", {"path": [Vector2(40 * 32, 40 * 32)]})
    main._sync_interior()
    _check("walking away keeps it shut", _panel.is_open(), false)
    pc.set_meta("walking", {"path": [Vector2(8 * 32 + 16, 20 * 32 + 16)], "finish_idle": true})
    main._sync_interior()
    _check("coming back opens it again", _panel.is_open(), true)
    pc.set_meta("walking", {"path": [Vector2(40 * 32, 40 * 32)]})
    main._sync_interior()
    _check("closes on walking away", _panel.is_open(), false)
    main.free()
    _panel.wares_hook = Callable()
    for id in _world.placed_objects.keys():
        _world.placed_objects[id].free()
    _world.placed_objects.clear()
    _done()


## Run a drawn person's walk to its end (LLM-754 walks are tweens).
func _finish_walk(holder: Node) -> void:
    if holder != null and holder.has_meta("walking"):
        (holder.get_meta("walking") as Tween).custom_step(600.0)


func _leavers() -> Array:
    var out := []
    for layer in [_panel._people, _panel._staff]:
        for child in layer.get_children():
            if str(child.name).begins_with(_panel.LEAVING_PREFIX):
                out.append(str(child.name))
    return out


## The close button's glyph is in the font the button draws with — a browser
## has no system font to hide a missing one behind.
func _test_close_glyph_in_font() -> void:
    var text: String = _panel.close_button.text
    _check("the close button is ×", text, "×")
    var font: Font = _panel.close_button.get_theme_font("font")
    _check("its font has the glyph", font.has_char(text.unicode_at(0)), true)
    _done()


func _test_walk_out() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return []
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    var buyer := _at_tile("buyer", _farmer(), pin)
    _panel.open(STALL)
    _check("opening walks nobody in or out", [_leavers(), _panel._people.get_node("buyer").has_meta("walking")], [[], false])
    var was: Vector2 = _panel._people.get_node("buyer").position

    # The buyer sets off: they turn and walk away toward the nearer side.
    buyer.set_meta("walking", {"path": [Vector2(40 * 32, 40 * 32)]})
    _panel.refresh()
    _check("the buyer is no longer drawn standing", _panel._people.get_node_or_null("buyer"), null)
    var ghost: Node2D = _panel._people.get_node_or_null(_panel.LEAVING_PREFIX + "buyer")
    _check("the buyer walks away", ghost != null and ghost.has_meta("walking"), true)
    if ghost != null:
        _check("from where they stood", ghost.position, was)
        var side := "west" if was.x < StallScenes.SIZE.x / 2.0 else "east"
        _check("toward the nearer side", (ghost.get_child(0) as AnimatedSprite2D).animation, StringName(side + "_walk"))
        _check("not clickable on the way out", _panel.person_at(was * _panel.k), "")
        _finish_walk(ghost)
        _check("gone at the end", ghost.is_queued_for_deletion(), true)

    # The keeper walks out of the stall too, behind the counter.
    _world.placed_npcs["keeper"].set_meta("inside", false)
    _world.placed_npcs["keeper"].set_meta("inside_structure_id", "")
    _panel.refresh()
    _check("the keeper walks out behind the counter", _panel._staff.get_node_or_null(_panel.LEAVING_PREFIX + "keeper") != null, true)
    _panel.close()

    # A room's guest walks to the door.
    _clear_people()
    _person("guest", _farmer(), _sid)
    _seat("guest", 3)
    _panel.open(_sid)
    _world.placed_npcs["guest"].set_meta("inside", false)
    _panel.refresh()
    var out: Node2D = _panel._people.get_node_or_null(_panel.LEAVING_PREFIX + "guest")
    _check("a guest leaves toward the door", out != null and (out.get_child(0) as AnimatedSprite2D).animation == &"south_walk", true)
    _panel.close()
    _panel.open(_sid)
    _check("reopening clears the leavers", _leavers(), [])
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


func _test_walk_in() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return []
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    _panel.open(STALL)
    _at_tile("newcomer", _farmer(), pin + Vector2i(1, 1))
    _panel.refresh()
    var holder: Node2D = _panel._people.get_node_or_null("newcomer")
    _check("a newcomer is drawn", holder != null, true)
    if holder != null:
        var home: Vector2 = holder.get_meta("home")
        _check("walking in", holder.has_meta("walking"), true)
        _check("from the side, not at their spot", holder.position != home, true)
        _check("fading in", holder.modulate.a < 1.0, true)
        _finish_walk(holder)
        _check("at their spot at the end", holder.position, home)
        _check("facing the counter", (holder.get_child(0) as AnimatedSprite2D).animation, &"north_idle")
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


func _test_keeper_bustles() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return []
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    _panel.open(STALL)
    var keeper: Node2D = _panel._staff.get_node("keeper")
    var home: Vector2 = keeper.get_meta("home")
    _check("the keeper starts at their spot", keeper.position, home)
    _panel._bustle_rng.seed = 7
    _panel._bustle(0.01)
    _check("not before the wait is up", keeper.has_meta("walking"), _panel._bustle_left <= 0.0)
    _panel._bustle_left = 0.0
    _panel._bustle(0.01)
    _check("off along the counter", keeper.has_meta("walking"), true)
    _finish_walk(keeper)
    _check("somewhere else behind the counter", keeper.position != home, true)
    _check("within reach of their spot", absf(keeper.position.x - home.x) <= _panel.BUSTLE_REACH, true)
    _check("on the same line", keeper.position.y, home.y)
    _check("a next wait", _panel._bustle_left >= _panel.BUSTLE_MIN, true)

    # A redraw (someone arrives) keeps the keeper where they bustled to.
    var there := keeper.position
    _at_tile("buyer", _farmer(), pin)
    _panel.refresh()
    keeper = _panel._staff.get_node("keeper")
    _check("still there after a redraw", keeper.position, there)

    # Speaking stops them, facing the customers.
    _panel._bustle_left = 0.0
    _panel._bustle(0.01)
    _panel.say("keeper", "Mind the step.")
    _check("stopped to speak", keeper.has_meta("walking"), false)
    _check("facing the customers", (keeper.get_child(0) as AnimatedSprite2D).animation, &"south_idle")
    _check("held a while", _panel._bustle_left, _panel.SPEAK_HOLD)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## A hand working beside the keeper (LLM-759) is drawn behind the counter at
## a hand's spot; the keeper alone bustles (never into the hand) and hands
## goods over; the hand turns now and then, and to the customers to speak.
func _test_stall_hands() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return [{"item": "apples", "stock": "plenty"}]
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    _person("hand", _farmer(), STALL)
    _at_tile("buyer", _farmer(), pin + Vector2i(1, 1))
    _panel.open(STALL)
    var keeper: Node2D = _panel._staff.get_node_or_null("keeper")
    var hand: Node2D = _panel._staff.get_node_or_null("hand")
    _check("the hand is behind the counter with the keeper", hand != null and keeper != null, true)
    if hand == null or keeper == null:
        _done()
        return
    var hand_home: Vector2 = hand.get_meta("home")
    _check("at a hand's spot", StallScenes.HANDS.map(func(s): return Vector2(s["x"], s["y"])).has(hand_home), true)
    _check("the one tending is the keeper", _panel._tending(), keeper)

    # The keeper's bustle stays clear of the hand.
    _panel._bustle_rng.seed = 3
    var clear := true
    for i in 40:
        _panel._bustle_left = 0.0
        _panel._bustle(0.01)
        _finish_walk(keeper)
        if absf(keeper.position.x - hand_home.x) < _panel.HAND_CLEAR:
            clear = false
    _check("the keeper never bustles into the hand", clear, true)
    _check("the hand stays at their spot", [hand.position, hand.has_meta("walking")], [hand_home, false])

    # The hand turns now and then.
    var facings := {}
    for i in 30:
        _panel._hands_turn(_panel.BUSTLE_MAX + 0.1)
        facings[str((hand.get_child(0) as AnimatedSprite2D).animation)] = true
    _check("the hand looks at the goods and at the customers", [facings.has("north_idle"), facings.has("south_idle")], [true, true])
    _panel._hands_turn(0.01)
    _check("and waits between turns", float(hand.get_meta("turn_left")) > 0.0, true)

    # Speaking turns the hand to the customers; the keeper's bustle is not touched.
    _pose_north(hand)
    _panel._bustle_left = 1.5
    _panel.say("hand", "Mind the sparks.")
    _check("the hand faces the customers to speak", (hand.get_child(0) as AnimatedSprite2D).animation, &"south_idle")
    _check("and holds it", [float(hand.get_meta("turn_left")), _panel._bustle_left], [_panel.SPEAK_HOLD, 1.5])

    # The keeper hands a sale over; a sale by the hand passes the counter by.
    _panel.on_pay_resolved(_sale("hand", "buyer"))
    _check("the hand selling: no handoff", [_panel._handoff_now, hand.has_meta("walking")], [{}, false])
    _panel.on_pay_resolved(_sale("keeper", "buyer"))
    _check("the keeper selling: the keeper walks to it", [keeper.has_meta("walking"), hand.has_meta("walking")], [true, false])
    _finish_handoff()

    # A redraw keeps the hand at their spot and still a hand.
    _at_tile("newcomer", _farmer(), pin + Vector2i(-1, 1))
    _panel.refresh()
    hand = _panel._staff.get_node("hand")
    _check("after a redraw: the hand at their spot, still a hand", [hand.position, hand.has_meta("hand"), hand.has_meta("keeper")], [hand_home, true, false])

    # A speaking hand keeps facing the customers, and their hold, over a
    # redraw; a hand looking at the goods keeps looking there.
    _panel.say("hand", "One moment.")
    _at_tile("another", _farmer(), pin + Vector2i(0, 1))
    _panel.refresh()
    hand = _panel._staff.get_node("hand")
    _check("a redraw mid-speech: still facing the customers, still holding", [(hand.get_child(0) as AnimatedSprite2D).animation, float(hand.get_meta("turn_left", 0.0))], [&"south_idle", _panel.SPEAK_HOLD])
    hand.set_meta("facing", "north")
    _pose_north(hand)
    _world.placed_npcs["another"].free()
    _world.placed_npcs.erase("another")
    _panel.refresh()
    hand = _panel._staff.get_node("hand")
    _check("a redraw: a hand looking at the goods still does", (hand.get_child(0) as AnimatedSprite2D).animation, &"north_idle")
    var rng_state: int = _panel._bustle_rng.state
    _panel._hands_turn(0.01)
    _check("a hand's wait counting down draws nothing from the bustle's dice", _panel._bustle_rng.state, rng_state)

    # A hand who comes in while the window is open: speaking does not stop
    # their walk in.
    _person("late-hand", _farmer(), STALL)
    _panel.refresh()
    var late: Node2D = _panel._staff.get_node_or_null("late-hand")
    _check("a second hand walks in", late != null and late.has_meta("walking") and late.has_meta("hand"), true)
    if late != null:
        _panel.say("late-hand", "Coming through.")
        _check("speaking on the way in: still walking in", [late.has_meta("walking"), late.has_meta("entering")], [true, true])
        _finish_walk(late)
        _check("then at their spot", late.position, late.get_meta("home"))
    _world.placed_npcs["late-hand"].free()
    _world.placed_npcs.erase("late-hand")
    _panel.refresh()
    _finish_walk(_panel._staff.get_node_or_null(_panel.LEAVING_PREFIX + "late-hand"))

    # A hand minding the stall alone bustles like a keeper; when the keeper
    # comes back the hand goes to a hand's spot, not where they had got to.
    keeper = _panel._staff.get_node("keeper")
    _finish_walk(keeper)
    _world.placed_npcs["keeper"].free()
    _world.placed_npcs.erase("keeper")
    _panel.refresh()
    var minding: Node2D = _panel._tending()
    _check("the hand minds the stall", minding != null and str(minding.name) == "hand", true)
    if minding != null:
        _check("the keeper gone: the hand walks to the keeper's spot", [minding.has_meta("walking"), minding.position], [true, hand_home])
        _finish_walk(minding)
        _check("…and stands there", minding.position, Vector2(StallScenes.KEEPER["x"], StallScenes.KEEPER["y"]))
        minding.position = Vector2(StallScenes.KEEPER["x"] + 9, StallScenes.KEEPER["y"])
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    _panel.refresh()
    hand = _panel._staff.get_node("hand")
    _finish_walk(hand)
    _check("the keeper back: the hand at a hand's spot", hand.position, hand.get_meta("home"))
    _check("…which is a hand's spot", StallScenes.HANDS.map(func(s): return Vector2(s["x"], s["y"])).has(hand.position), true)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


func _pose_north(holder: Node2D) -> void:
    (holder.get_child(0) as AnimatedSprite2D).play("north_idle")


## Stage coordinates for a point in room art pixels, at the test's 4x scale.
func _at_art(art: Vector2) -> Vector2:
    return art * 4.0


func _tag_text() -> Array:
    return [_panel.tag.visible, _panel.tag_name.text, _panel.tag_role.text if _panel.tag_role.visible else ""]


## LLM-761: hovering a person shows their name and role, a good its name; a
## tap shows the tag a while (a touch screen has no hover) and still opens talk.
func _test_name_tags() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array:
        return [{"item": "horseshoe", "display_label": "Horseshoe", "stock": "plenty"},
            {"item": "nail", "display_label": "Nails", "stock": "few"}]
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL, "display_name": "Ezekiel Crane",
        "llm_memory_agent": "salem-vendor", "attributes": ["blacksmith", "businessowner"]})
    _person("hand", _farmer(), STALL, {"display_name": "Abraham Warren", "llm_memory_agent": "salem-vendor",
        "attributes": ["worker"]})
    _at_tile("pc", _farmer(), pin, {"display_name": "Jefferey"})
    _panel.open(STALL)
    _panel.k = 4
    _panel._stretch = Vector2.ONE
    _panel._scene.position = Vector2.ZERO
    _panel._scene.scale = Vector2(4, 4)
    var keeper: Node2D = _panel._staff.get_node("keeper")
    var hand: Node2D = _panel._staff.get_node("hand")
    var pc: Node2D = _panel._people.get_node("pc")

    _panel._on_stage_input(_motion(_at_art(keeper.position + Vector2(0, -10))))
    _check("hovering the keeper: name and role", _tag_text(), [true, "Ezekiel Crane", "Blacksmith"])
    _panel._on_stage_input(_motion(_at_art(hand.position + Vector2(0, -10))))
    _check("hovering the hand: name and role", _tag_text(), [true, "Abraham Warren", "Worker"])
    _panel._on_stage_input(_motion(_at_art(pc.position + Vector2(0, -10))))
    _check("hovering a player: the name alone", _tag_text(), [true, "Jefferey", ""])
    var pile: Node2D = _panel._goods_back.get_child(0)
    _panel._on_stage_input(_motion(_at_art(pile.position + Vector2(8, 20))))
    _check("hovering a pile on the floor: its name", _tag_text(), [true, "Horseshoe", ""])
    var nails: Node2D = _panel._goods_front.get_child(0)
    var on_nails := nails.position + Vector2(8, 18)
    _panel._on_stage_input(_motion(_at_art(on_nails)))
    _check("hovering goods on the counter: their name", _tag_text(), [true, "Nails", ""])
    _panel._on_stage_input(_motion(_at_art(Vector2(64, 4))))
    _check("hovering nothing: the tag goes", _panel.tag.visible, false)

    # Front to back: the counter's goods in front of the keeper behind them.
    keeper.position = Vector2(on_nails.x, keeper.position.y)
    _panel._on_stage_input(_motion(_at_art(on_nails)))
    _check("goods on the counter over the keeper behind", _panel.tag_name.text, "Nails")

    # The tag follows the keeper along the counter.
    keeper.position = Vector2(64, keeper.position.y)
    _panel._on_stage_input(_motion(_at_art(keeper.position + Vector2(0, -10))))
    var was_at: Vector2 = _panel.tag.position
    keeper.position.x += 10
    _panel._update_tag(0.01)
    _check("the tag follows a walking keeper", _panel.tag.position.x > was_at.x, true)

    # A tap shows the tag a while, and a tap on a person still opens talk.
    var clicked := []
    var on_click := func(id: String): clicked.append(id)
    _panel.person_clicked.connect(on_click)
    _panel._on_stage_input(_motion(_at_art(Vector2(64, 4))))
    _panel._on_stage_input(_press(_at_art(hand.position + Vector2(0, -10))))
    _check("a tap on the hand: the tag, and talk opens", [_tag_text(), clicked], [[true, "Abraham Warren", "Worker"], ["hand"]])
    _panel._on_stage_input(_motion(_at_art(Vector2(64, 4))))
    _check("a tap's tag stays as the pointer moves off", _panel.tag.visible, true)
    _panel._update_tag(_panel.TAG_SECONDS + 0.1)
    _check("then goes", _panel.tag.visible, false)
    _panel._on_stage_input(_press(_at_art(on_nails)))
    _check("a tap on a good: its tag, no talk", [_panel.tag_name.text, clicked.size()], ["Nails", 1])
    _panel._on_stage_input(_press(_at_art(Vector2(64, 4))))
    _check("a tap on nothing takes a tapped tag down", _panel.tag.visible, false)
    # A touch screen sends motion with the tap: the tag still runs its time out.
    _panel._on_stage_input(_press(_at_art(hand.position + Vector2(0, -10))))
    _panel._on_stage_input(_motion(_at_art(hand.position + Vector2(0, -10))))
    _check("motion after a tap keeps the tap's time", _panel._tag_left > 0.0, true)
    _panel._update_tag(_panel.TAG_SECONDS + 0.1)
    _check("and the tag goes when it is up", _panel.tag.visible, false)
    _panel.person_clicked.disconnect(on_click)

    # The tag sits over its person wherever the window is.
    var root_at: Vector2 = _panel.root.position
    _panel.root.position = Vector2(50, 30)
    _panel._on_stage_input(_motion(_at_art(keeper.position + Vector2(0, -10))))
    var head: Vector2 = _panel._scene.get_global_transform() * (keeper.position + Vector2(0, _panel.HIT_BOX.position.y - 2))
    var box: Rect2 = _panel.tag.get_global_rect()
    _check("the tag centred just above the head, the window moved", [roundf(box.get_center().x), roundf(box.end.y)], [roundf(head.x), roundf(head.y)])
    _panel.root.position = root_at

    # The person it is on leaves: the tag goes.
    _panel._on_stage_input(_motion(_at_art(hand.position + Vector2(0, -10))))
    _world.placed_npcs["hand"].free()
    _world.placed_npcs.erase("hand")
    _panel.refresh()
    _panel._update_tag(0.01)
    _check("its person gone: the tag goes", _panel.tag.visible, false)
    _panel.close()
    _check("closing hides it", [_panel.tag.visible, _panel._tag_on], [false, {}])

    # In a room: a dish on the table; a good not yet set down is not there.
    _tavern_with_guest(0)
    _panel.k = 4
    _panel._stretch = Vector2.ONE
    _panel._scene.position = Vector2.ZERO
    var table := Vector2(_panel.room["spots"][0]["serve"]["x"], _panel.room["spots"][0]["serve"]["y"])
    var dish: Node2D = _panel._handoff_good("stew", 1, table)
    _panel._on_stage_input(_motion(_at_art(dish.position + Vector2(8, 22))))
    _check("hovering a dish: its name", _tag_text(), [true, "Stew", ""])
    dish.modulate.a = 0.0
    _panel._on_stage_input(_motion(_at_art(dish.position + Vector2(8, 22))))
    _check("a good not set down yet: nothing", _panel.tag.visible, false)
    _check("a good's name from its kind", _panel.good_label("fried_meat"), "Fried meat")
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


func _motion(at: Vector2) -> InputEventMouseMotion:
    var e := InputEventMouseMotion.new()
    e.position = at
    return e


func _press(at: Vector2) -> InputEventMouseButton:
    var e := InputEventMouseButton.new()
    e.position = at
    e.pressed = true
    e.button_index = MOUSE_BUTTON_LEFT
    return e


## Someone else coming or going mid-entrance redraws everyone: a newcomer
## partway in carries on walking in from there — in front of the counter
## and behind it.
func _test_walk_in_survives_a_redraw() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return []
    _panel.open(STALL)
    _at_tile("first", _farmer(), pin + Vector2i(1, 1))
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    _panel.refresh()
    for id in ["first", "keeper"]:
        var h: Node2D = _panel._holder(id)
        (h.get_meta("walking") as Tween).custom_step(0.4)
    var part_way := {"first": _panel._holder("first").position, "keeper": _panel._holder("keeper").position}
    _at_tile("second", _farmer(), pin + Vector2i(-1, 1))
    _panel.refresh()
    for id in ["first", "keeper"]:
        var h: Node2D = _panel._holder(id)
        _check("%s: picked up where the redraw found them" % id, h.position, part_way[id])
        _check("%s: still walking in" % id, [h.has_meta("walking"), h.has_meta("entering")], [true, true])
        _check("%s: still fading in" % id, h.modulate.a < 1.0, true)
        _finish_walk(h)
        _check("%s: at their spot" % id, h.position, h.get_meta("home"))
        _check("%s: in" % id, [h.modulate.a, h.has_meta("entering")], [1.0, false])
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## Leave, come back before the way out is over, leave again: one person
## walking out, never two.
func _test_leave_return_leave() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return []
    var buyer := _at_tile("buyer", _farmer(), pin)
    _panel.open(STALL)
    buyer.set_meta("walking", {"path": [Vector2(40 * 32, 40 * 32)]})
    _panel.refresh()
    _check("walking out", _leavers(), [_panel.LEAVING_PREFIX + "buyer"])
    _panel.say("buyer", "Oh — one more thing.")
    _check("a line on the way out", _panel._leaving("buyer").get_node_or_null(_panel.BUBBLE_NODE) != null, true)
    _at_tile("other", _farmer(), pin + Vector2i(1, 1))
    _panel.refresh()
    _check("a redraw leaves the line on them", _panel._leaving("buyer").get_node_or_null(_panel.BUBBLE_NODE) != null, true)
    buyer.remove_meta("walking")
    _panel.refresh()
    _check("back: nobody walking out", _leavers(), [])
    _check("back: drawn again", _panel._holder("buyer") != null, true)
    _check("back: with their line", _panel._holder("buyer").get_node_or_null(_panel.BUBBLE_NODE) != null, true)
    buyer.set_meta("walking", {"path": [Vector2(40 * 32, 40 * 32)]})
    _panel.refresh()
    _check("gone again: one walking out", _leavers(), [_panel.LEAVING_PREFIX + "buyer"])
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


func _test_leaver_takes_their_bubble() -> void:
    _clear_people()
    _place_stall()
    var pin := Vector2i(8, 21)
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return []
    var buyer := _at_tile("buyer", _farmer(), pin)
    _panel.open(STALL)
    _panel.say("buyer", "Good day to you.")
    buyer.set_meta("walking", {"path": [Vector2(40 * 32, 40 * 32)]})
    _panel.refresh()
    var ghost: Node2D = _panel._leaving("buyer")
    _check("the line goes with them", ghost != null and ghost.get_node_or_null(_panel.BUBBLE_NODE) != null, true)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## A stall open with its keeper behind the counter and `buyer` at it; the
## shelf (asked for through the hook) holds apples. Returns the shelf-asks log.
func _stall_with_buyer(pin_offset := Vector2i(1, 1)) -> Array:
    _clear_people()
    _place_stall()
    var asked := []
    _panel.close()
    _panel.wares_hook = func(sid: String) -> Array:
        asked.append(sid)
        return [{"item": "apples", "stock": "plenty"}]
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    _at_tile("buyer", _farmer(), Vector2i(8, 21) + pin_offset)
    _panel.open(STALL)
    return asked


func _sale(seller: String, buyer: String, item := "apples", state := "accepted", qty := 3, eaten := false) -> Dictionary:
    return {"seller_id": seller, "buyer_id": buyer, "item": item, "qty": qty, "terminal_state": state,
        "consume_now": eaten}


## A smithy-tagged stall open with the keeper behind the counter and a buyer
## at the pin, selling a pile and a few.
func _open_smithy() -> void:
    _clear_people()
    _place_stall()
    _world.placed_objects[STALL].set_meta("tags", ["smithy"])
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array:
        return [{"item": "nail", "stock": "plenty"}, {"item": "skillet", "stock": "few"}]
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    _at_tile("buyer", _farmer(), Vector2i(8, 21) + Vector2i(1, 1))
    _panel.open(STALL)


## LLM-763: everything in the window follows the scene's layout — the
## smithy's keeper, customers, goods and counter sit on its selling side.
func _test_smithy_layout() -> void:
    _open_smithy()
    var layout: Dictionary = StallScenes.TRADES["smithy"]
    var keeper: Node2D = _panel._tending()
    _check("the keeper at the smithy's spot", keeper.position, Vector2(layout["keeper"]["x"], layout["keeper"]["y"]))
    var buyer: Node2D = _panel._people.get_node("buyer")
    var spot_xs := []
    for s in layout["spots"]:
        spot_xs.append(float(s["x"]))
    _check("the buyer at one of the smithy's spots", spot_xs.has(buyer.position.x), true)
    var pile: Node2D = _panel._goods_back.get_child(0)
    _check("the pile on the smithy's first floor slot", pile.position.x, float(layout["floor_slots"][0]))
    var few: Node2D = _panel._goods_front.get_child(0)
    _check("the few on the smithy's first counter slot", few.position.x, float(layout["counter_slots"][0]))
    _check("the smithy's counter picture", _panel.room["counter"], layout["counter"])
    # A sale to someone not drawn: set down at the keeper's spot, not the
    # plain stall's.
    _panel.on_pay_resolved(_sale("keeper", "somebody-past-the-spots", "nail"))
    _check("not drawn: at the smithy keeper's spot", float((_panel._handoff_now["stop"] as Vector2).x), float(layout["keeper"]["x"]))
    _finish_handoff()
    # A customer walking away leaves by the selling side's nearer edge.
    var middle: float = (float(layout["counter_from"]) + layout["size"].x) / 2.0
    _check("the doorway from the left of the selling side", _panel._doorway(Vector2(middle - 10, 106), false).x < middle - 10, true)
    _check("the doorway from the right of it", _panel._doorway(Vector2(middle + 10, 106), false).x > middle + 10, true)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## A smithy open with a farmer-base keeper and the given hands behind the
## counter, the given customers at the pin, and its shelf.
func _open_smithy_crew(hands: Array, customers: Array) -> void:
    _clear_people()
    _place_stall()
    _world.placed_objects[STALL].set_meta("tags", ["smithy"])
    _panel.close()
    _panel.wares_hook = func(_s: String) -> Array: return [{"item": "nail", "stock": "plenty"}]
    _person("keeper", _farmer(), STALL, {"work_structure_id": STALL})
    for id in hands:
        _person(id, _farmer(), STALL)
    var i := 0
    for id in customers:
        _at_tile(id, _farmer(), Vector2i(8, 21) + Vector2i(i - 1, 1))
        i += 1
    _panel.open(STALL)


func _anim(holder: Node2D) -> StringName:
    return _panel._sprite_of(holder).animation


## Run a hand's turns until they set off for the bellows (bounded).
func _send_to_bellows(hand: Node2D) -> bool:
    for i in 60:
        hand.set_meta("turn_left", 0.0)
        _panel._hands_turn(0.01)
        if hand.has_meta("bellows"):
            return true
        _finish_walk(hand)
    return false


## LLM-764: now and then a farmer-base hand works the bellows; each pull down
## flares the forge fire; one hand at a time; speech stops the pull.
func _test_smithy_bellows() -> void:
    _open_smithy_crew(["hand", "hand2"], [])
    var layout: Dictionary = StallScenes.TRADES["smithy"]
    var bellows := Vector2(layout["bellows"]["x"], layout["bellows"]["y"])
    _check("the fire's glow laid over the fire, dark", [_panel._fire.visible, _panel._fire.position, _panel._fire.modulate.a], [true, Vector2(layout["fire"].position), 0.0])
    _check("the glow is the fire's size in art px", _panel._fire.texture.get_size(), Vector2(layout["fire"].size))
    _check("the staff are drawn in depth order", _panel._staff.y_sort_enabled, true)
    var hand: Node2D = _panel._staff.get_node("hand")
    var other: Node2D = _panel._staff.get_node("hand2")
    var home: Vector2 = hand.get_meta("home")
    _panel._bustle_rng.seed = 7
    _check("a hand sets off for the bellows", _send_to_bellows(hand), true)
    _check("walking there", hand.has_meta("walking"), true)
    _finish_walk(hand)
    _check("at the bellows, pulling, facing them", [hand.position, hand.has_meta("pulling"), _anim(hand)], [bellows, true, &"north_pull"])
    var seconds := float(hand.get_meta("pulling"))
    _check("for a few seconds", seconds >= _panel.PULL_MIN and seconds <= _panel.PULL_MAX, true)

    # Each pull down flares the fire, once per stroke; it dies down after.
    _panel._sprite_of(hand).frame = 1
    _panel._hands_turn(0.01)
    _check("the pull down flares the fire", _panel._flare, 1.0)
    _panel._flare = 0.5
    _panel._hands_turn(0.01)
    _check("once per stroke", _panel._flare, 0.5)
    _panel._sprite_of(hand).frame = 0
    _panel._hands_turn(0.01)
    _panel._sprite_of(hand).frame = 1
    _panel._hands_turn(0.01)
    _check("the next stroke flares it again", _panel._flare, 1.0)
    _panel._process(_panel.FLARE_SECONDS / 2.0)
    _check("the glow fading", [is_equal_approx(_panel._flare, 0.5), is_equal_approx(_panel._fire.modulate.a, 0.5 * _panel.FLARE_ALPHA)], [true, true])

    # One hand at the bellows at a time.
    var went := false
    for i in 30:
        other.set_meta("turn_left", 0.0)
        _panel._hands_turn(0.01)
        went = went or other.has_meta("bellows")
        _finish_walk(other)
    _check("the other hand never goes while one pulls", went, false)

    # Time up: back to their spot, facing the customers.
    _panel._hands_turn(_panel.PULL_MAX + 1.0)
    _check("time up: walking back", [hand.has_meta("pulling"), hand.has_meta("bellows"), hand.has_meta("walking")], [false, false, true])
    _finish_walk(hand)
    _check("back at their spot facing the customers", [hand.position, _anim(hand)], [home, &"south_idle"])

    # Speech stops a pull where they stand; they walk back at their next turn.
    _check("off again", _send_to_bellows(hand), true)
    _finish_walk(hand)
    _panel.say("hand", "Hot work, this.")
    _check("speaking stops the pull, facing the customers", [hand.has_meta("pulling"), _anim(hand), hand.position], [false, &"south_idle", bellows])
    _check("holding it, the bellows still theirs", [float(hand.get_meta("turn_left")), hand.has_meta("bellows")], [_panel.SPEAK_HOLD, true])
    _panel._hands_turn(_panel.SPEAK_HOLD + 0.1)
    _check("then walks back", [hand.has_meta("walking"), hand.has_meta("bellows")], [true, false])
    _finish_walk(hand)
    _check("to their spot", hand.position, home)

    # Speech on the way there stops them too.
    _check("off once more", _send_to_bellows(hand), true)
    _panel.say("hand", "Coming.")
    _check("speech on the way: stopped, facing the customers", [hand.has_meta("walking"), _anim(hand)], [false, &"south_idle"])

    # A redraw mid-pull: they walk back from the bellows.
    _panel._hands_turn(_panel.SPEAK_HOLD + 0.1)
    _finish_walk(hand)
    _check("and once more", _send_to_bellows(hand), true)
    _finish_walk(hand)
    _at_tile("newcomer", _farmer(), Vector2i(8, 21) + Vector2i(0, 1))
    _panel.refresh()
    hand = _panel._staff.get_node("hand")
    _check("a redraw mid-pull: from the bellows, walking back", [hand.position, hand.has_meta("walking"), hand.has_meta("hand")], [bellows, true, true])
    _finish_walk(hand)
    _check("to their spot", hand.position, home)

    # A one-sheet pack sprite never pulls; a stall without bellows has no glow.
    var pack := _pack_holder()
    _check("a pack hand cannot pull", _panel._can_work(pack, layout["bellows"], "pull"), false)
    pack.free()
    _open_smithy_crew([], [])
    _check("nobody pulls: the fire stays still", [_panel._fire.visible, _panel._fire.modulate.a], [true, 0.0])
    _world.placed_objects[STALL].set_meta("tags", [])
    _panel.close()
    _panel.open(STALL)
    _check("a plain stall: no glow", _panel._fire.visible, false)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## A pack-sprite person's holder, off the stage.
func _pack_holder() -> Node2D:
    var holder := Node2D.new()
    holder.add_child(_panel._sprite(_pack()))
    return holder


## LLM-764: between customers a farmer-base keeper now and then walks round
## the counter's end to the anvil and hammers; a sale or speech brings them
## back.
func _test_smithy_anvil() -> void:
    _open_smithy_crew([], [])
    var layout: Dictionary = StallScenes.TRADES["smithy"]
    var anvil := Vector2(layout["anvil"]["x"], layout["anvil"]["y"])
    var keeper: Node2D = _panel._tending()
    var home: Vector2 = keeper.get_meta("home")
    var gate := Vector2(float(layout["counter_from"]) - _panel.GATE_IN, home.y)
    _check("to the anvil: round the counter's end", _panel._route(keeper, anvil), [gate, anvil])
    _check("along the counter: straight", _panel._route(keeper, home + Vector2(10, 0)), [home + Vector2(10, 0)])
    _panel._bustle_rng.seed = 11
    _check("the keeper goes to the anvil", _send_to_anvil(keeper), true)
    _check("hammering at the anvil, facing it", [keeper.position, _anim(keeper)], [anvil, &"west_smith"])
    var seconds := float(keeper.get_meta("smithing"))
    _check("for a few seconds", seconds >= _panel.SMITH_MIN and seconds <= _panel.SMITH_MAX, true)
    _panel._bustle(1.0)
    _check("still at it, no bustle", [keeper.has_meta("smithing"), keeper.has_meta("walking")], [true, false])

    # A redraw keeps them at it.
    _at_tile("pc", _farmer(), Vector2i(8, 21))
    _panel.refresh()
    keeper = _panel._tending()
    _check("a redraw: still at the anvil, hammering", [keeper.position, keeper.has_meta("smithing"), _anim(keeper)], [anvil, true, &"west_smith"])

    # Time up: back round the counter's end to their spot.
    _panel._bustle(_panel.SMITH_MAX + 1.0)
    _check("time up: walking back", [keeper.has_meta("smithing"), keeper.has_meta("walking")], [false, true])
    _finish_walk(keeper)
    _check("at their spot, facing the customers", [keeper.position, _anim(keeper)], [home, &"south_idle"])

    # Speech brings them back to the counter.
    _check("off to the anvil again", _send_to_anvil(keeper), true)
    _panel.say("keeper", "With you directly.")
    _check("speaking: walking back to the counter", [keeper.has_meta("smithing"), keeper.has_meta("walking")], [false, true])
    _finish_walk(keeper)
    _check("there", keeper.position, home)

    # A sale brings them back to hand it over.
    _check("and again", _send_to_anvil(keeper), true)
    _panel.on_pay_resolved(_sale("keeper", "pc", "nail"))
    _check("a sale: off the anvil, walking to the counter", [keeper.has_meta("smithing"), keeper.has_meta("walking")], [false, true])
    _finish_handoff()
    _check("over the counter", keeper.position.x >= float(layout["counter_from"]), true)

    # Not while a customer waits at the counter.
    _open_smithy_crew([], ["buyer"])
    keeper = _panel._tending()
    _panel._bustle_rng.seed = 11
    var went := false
    for i in 60:
        _panel._bustle_left = 0.0
        _panel._bustle(0.01)
        went = went or keeper.has_meta("smithing") or keeper.position.x < float(layout["counter_from"])
        _finish_walk(keeper)
        went = went or keeper.has_meta("smithing")
    _check("a customer waiting: the keeper stays at the counter", went, false)
    var pack := _pack_holder()
    _check("a pack keeper cannot smith", _panel._can_work(pack, layout["anvil"], "smith"), false)
    pack.free()
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## LLM-764 (code_review): a redraw on either leg of the way to the anvil
## carries the trip on; a customer arriving on the way or while hammering calls
## the keeper back to the counter.
func _test_smithy_anvil_interrupted() -> void:
    _open_smithy_crew([], [])
    var layout: Dictionary = StallScenes.TRADES["smithy"]
    var anvil := Vector2(layout["anvil"]["x"], layout["anvil"]["y"])
    var end := float(layout["counter_from"])
    var keeper: Node2D = _panel._tending()
    var home: Vector2 = keeper.get_meta("home")
    _panel._bustle_rng.seed = 11

    # A redraw on the first leg, still along the counter.
    _check("off to the anvil", _start_anvil_trip(keeper), true)
    (keeper.get_meta("walking") as Tween).custom_step(1.0)
    var was_at := keeper.position
    _check("partway along the counter", was_at.x > end and was_at.x < home.x, true)
    _toggle_pc()
    keeper = _panel._tending()
    _check("a redraw on the first leg: on from there, still bound for the anvil", [keeper.position, keeper.has_meta("anvil_trip"), keeper.has_meta("walking")], [was_at, true, true])
    _finish_walk(keeper)
    _check("there, hammering", [keeper.position, keeper.has_meta("smithing"), keeper.has_meta("anvil_trip")], [anvil, true, false])
    _panel._bustle(_panel.SMITH_MAX + 1.0)
    _finish_walk(keeper)

    # A redraw on the second leg, in the work area.
    _check("off again", _start_anvil_trip(keeper), true)
    (keeper.get_meta("walking") as Tween).custom_step(4.0)
    was_at = keeper.position
    _check("past the counter's end", was_at.x < end, true)
    _toggle_pc()
    keeper = _panel._tending()
    _check("a redraw on the second leg: on from there, still bound for the anvil", [keeper.position, keeper.has_meta("anvil_trip"), keeper.has_meta("walking")], [was_at, true, true])
    _finish_walk(keeper)
    _check("there, hammering", [keeper.position, keeper.has_meta("smithing")], [anvil, true])

    # A customer arrives while they hammer.
    _at_tile("buyer", _farmer(), Vector2i(8, 21) + Vector2i(1, 1))
    _panel.refresh()
    keeper = _panel._tending()
    _check("still hammering after the redraw", keeper.has_meta("smithing"), true)
    _panel._bustle(0.01)
    _check("a customer: back to the counter", [keeper.has_meta("smithing"), keeper.has_meta("walking")], [false, true])
    _finish_walk(keeper)
    _check("at their spot", keeper.position, home)

    # A customer arrives while they are on the way.
    _world.placed_npcs["buyer"].free()
    _world.placed_npcs.erase("buyer")
    _panel.refresh()
    keeper = _panel._tending()
    _finish_walk(keeper)
    _check("off once more", _start_anvil_trip(keeper), true)
    (keeper.get_meta("walking") as Tween).custom_step(4.0)
    _at_tile("buyer", _farmer(), Vector2i(8, 21) + Vector2i(1, 1))
    _panel.refresh()
    keeper = _panel._tending()
    _panel._bustle(0.01)
    _check("a customer on the way: turned back", [keeper.has_meta("anvil_trip"), keeper.has_meta("walking")], [false, true])
    _finish_walk(keeper)
    _check("at their spot, not hammering", [keeper.position, keeper.has_meta("smithing")], [home, false])
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## Run the keeper's bustle until they set off for the anvil (bounded); other
## bustle walks are run to their end.
func _start_anvil_trip(keeper: Node2D) -> bool:
    for i in 60:
        _panel._bustle_left = 0.0
        _panel._bustle(0.01)
        if keeper.has_meta("anvil_trip"):
            return true
        _finish_walk(keeper)
    return false


## Redraw the window: the player's character steps up to the counter, or away.
func _toggle_pc() -> void:
    if _world.placed_npcs.has("pc"):
        _world.placed_npcs["pc"].free()
        _world.placed_npcs.erase("pc")
    else:
        _at_tile("pc", _farmer(), Vector2i(8, 21))
    _panel.refresh()


## Run the keeper's bustle until they are at the anvil, hammering (bounded).
func _send_to_anvil(keeper: Node2D) -> bool:
    for i in 60:
        _panel._bustle_left = 0.0
        _panel._bustle(0.01)
        _finish_walk(keeper)
        if keeper.has_meta("smithing"):
            return true
    return false


## LLM-763: a room too big for the window at 2x scrolls — clipped, panned by
## a drag or the wheel, a tap still a tap.
func _test_scrolling_stage() -> void:
    _open_smithy()
    var size := Vector2(_panel.room["size"])
    _check("a window that fits shows it all", [_panel.scrolls(), _panel._view], [false, size])
    _check("unclipped while it fits", _panel._clip.clip_contents, false)

    # A phone: never below 2x, the rest scrolls, opening on the door.
    var was_size := root.size
    root.size = Vector2i(360, 640)
    _panel._stretch = Vector2.ONE
    _panel._centre_pan = true
    _panel._relayout()
    if root.size == Vector2i(360, 640):
        _check("a phone: 2x", _panel.k, 2)
        _check("a phone: part of it shown", _panel.scrolls(), true)
        _check("clipped, with room above for bubbles", [_panel._clip.clip_contents, _panel._clip.position.y < 0.0], [true, true])
        var door := float(_panel.room["door"]["x"])
        var centred := clampf(roundf(door - _panel._view.x / 2.0), 0.0, size.x - _panel._view.x)
        _check("opens centred on the door, as far as the edge allows", _panel._pan.x, centred)
        var head: Vector2 = _panel._tending().position + Vector2(0, -10)
        var at: Vector2 = head * float(_panel.k) / _panel._stretch + _panel._clip.position + _panel._scene.position
        _check("a person found under the headroom and the pan", _panel.person_at(at), "keeper")
    root.size = was_size

    # The pan math, on a view set by hand.
    _panel.k = 2
    _panel._stretch = Vector2.ONE
    _panel._view = Vector2(100, size.y)
    _panel._clip.position = Vector2.ZERO
    _panel._set_pan(Vector2(1000, 50))
    _check("panned no further than the room's edge", _panel._pan, Vector2(size.x - 100, 0))
    _panel._set_pan(Vector2(-5, 0))
    _check("nor before its start", _panel._pan, Vector2.ZERO)
    _check("it scrolls", _panel.scrolls(), true)
    _panel._set_pan(Vector2(50, 0))
    var keeper: Node2D = _panel._tending()
    var feet := keeper.position + Vector2(0, -10)
    var local: Vector2 = feet * 2.0 + _panel._clip.position + _panel._scene.position
    _check("the scene moved with the pan", absf(_panel._scene.position.x + 100.0) <= 1.0, true)
    _check("a point maps back to the room", _panel._art_at(local), feet)
    _check("the keeper found where the pan puts them", _panel.person_at(local), "keeper")

    var clicks := []
    var on_click := func(id: String): clicks.append(id)
    _panel.person_clicked.connect(on_click)
    var press := InputEventMouseButton.new()
    press.button_index = MOUSE_BUTTON_LEFT
    press.pressed = true
    press.position = local
    _panel._on_stage_input(press)
    _check("a press waits to be a tap or a drag", clicks, [])
    var release := press.duplicate() as InputEventMouseButton
    release.pressed = false
    _panel._on_stage_input(release)
    _check("released where pressed: a tap", clicks, ["keeper"])

    _panel._on_stage_input(press)
    # Twenty one-px steps, each half an art px at 2x: the whole way counts,
    # the steps before the drag began included.
    var move := InputEventMouseMotion.new()
    for i in range(1, 21):
        move.position = local + Vector2(-i, 0)
        move.relative = Vector2(-1, 0)
        _panel._on_stage_input(move)
    _check("a drag pans by the whole way moved", _panel._pan.x, 60.0)
    release.position = move.position
    _panel._on_stage_input(release)
    _check("a drag is no tap", clicks, ["keeper"])

    # The drag threshold is screen px: at a stretch of 2, four canvas units
    # are eight screen px — a drag.
    _panel._stretch = Vector2(2, 2)
    _panel._set_pan(Vector2(60, 0))
    press.position = local
    _panel._on_stage_input(press)
    move.position = local + Vector2(-4, 0)
    move.relative = Vector2(-4, 0)
    _panel._on_stage_input(move)
    _check("past the threshold in screen px: a drag", bool(_panel._press["drag"]), true)
    release.position = move.position
    _panel._on_stage_input(release)
    _check("and no tap", clicks, ["keeper"])
    _panel._stretch = Vector2.ONE
    _panel._set_pan(Vector2(60, 0))

    var wheel := InputEventMouseButton.new()
    wheel.button_index = MOUSE_BUTTON_WHEEL_DOWN
    wheel.pressed = true
    _panel._on_stage_input(wheel)
    _check("the wheel pans a wide room along", _panel._pan, Vector2(60.0 + _panel.WHEEL_STEP, 0))
    # A room too tall for the window: the wheel scrolls it up and down, a
    # sideways wheel still along.
    _panel._view = Vector2(size.x, 60)
    _panel._set_pan(Vector2.ZERO)
    _panel._on_stage_input(wheel)
    _check("the wheel scrolls a tall room down", _panel._pan, Vector2(0, _panel.WHEEL_STEP))
    wheel.button_index = MOUSE_BUTTON_WHEEL_RIGHT
    _panel._on_stage_input(wheel)
    _check("a sideways wheel goes along (none to go here)", _panel._pan, Vector2(0, _panel.WHEEL_STEP))
    _panel.person_clicked.disconnect(on_click)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## Run the handoff under way to its end: the keeper's walk, then the good.
func _finish_handoff() -> void:
    var keeper: Node2D = _panel._tending()
    if keeper != null:
        _finish_walk(keeper)
    var now: Dictionary = _panel._handoff_now
    if not now.is_empty() and is_instance_valid(now["good"]) and now["good"].has_meta("tween"):
        (now["good"].get_meta("tween") as Tween).custom_step(60.0)


func _test_sale_handed_over_the_counter() -> void:
    var asked := _stall_with_buyer()
    var buyer: Node2D = _panel._people.get_node("buyer")
    var keeper: Node2D = _panel._staff.get_node("keeper")
    _panel.on_pay_resolved(_sale("keeper", "buyer"))
    _check("the shelf is read again", asked.size(), 2)
    _check("the keeper walks to the buyer", keeper.has_meta("walking"), true)
    var good: Node2D = _panel._handoff.get_node_or_null("handoff_apples")
    _check("the apples go on the counter", good != null, true)
    if good != null:
        _check("in front of the buyer", good.position.x + StallScenes.CELL.x / 2.0, roundf(buyer.position.x))
        _check("on the counter", good.position.y, float(StallScenes.COUNTER_Y - StallScenes.CELL.y))
        _check("a few: two of them", good.get_child_count(), 2)
        _check("not down until the keeper gets there", [good.modulate.a, good.has_meta("tween")], [0.0, false])
        _check("the apples' picture", (good.get_child(0).texture as AtlasTexture).region, StallScenes.cell_rect(2 * StallScenes.GOODS["apples"]))
        _check("the keeper does not stop to talk mid-handoff", [_panel.say("keeper", "Here you are."), keeper.has_meta("walking")], [null, true])
        _finish_walk(keeper)
        _check("the keeper stands at the buyer", keeper.position.x, roundf(buyer.position.x))
        var t: Tween = good.get_meta("tween")
        t.custom_step(1.0)
        _check("set down when they get there", good.modulate.a, 1.0)
        var on_counter := good.position
        t.custom_step(60.0)
        _check("then toward the buyer", good.position.y > on_counter.y, true)
        _check("and gone", good.is_queued_for_deletion(), true)
        _check("the counter is free", _panel._handoff_now, {})
    _panel.on_pay_resolved(_sale("keeper", "buyer", "apples", "accepted", 1))
    _check("one bought: one on the counter", (_panel._handoff_now["good"] as Node2D).get_child_count(), 1)
    _finish_handoff()

    # Someone not drawn gets it on the counter's middle.
    _panel.on_pay_resolved(_sale("keeper", "somebody-past-the-spots"))
    _check("not drawn: the counter's middle", float((_panel._handoff_now["stop"] as Vector2).x), float(StallScenes.KEEPER["x"]))
    _panel.open(STALL)
    _check("reopening clears the counter", [_panel._handoff.get_child_count(), _panel._handoff_now, _panel._handoffs], [0, {}, []])
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## Two sales close together are handed over one after the other.
func _test_sales_handed_over_in_turn() -> void:
    _stall_with_buyer()
    _check("a window opened afresh: the keeper at their spot", _panel._tending().position, Vector2(StallScenes.KEEPER["x"], StallScenes.KEEPER["y"]))
    _panel.on_pay_resolved(_sale("keeper", "buyer", "apples"))
    _panel.on_pay_resolved(_sale("keeper", "buyer", "nail"))
    _check("the first under way", str(_panel._handoff_now["item"]), "apples")
    _check("the second waits its turn", [_panel._handoffs.size(), _panel._handoff.get_node_or_null("handoff_nail") == null], [1, true])
    _finish_handoff()
    _check("then the second", str(_panel._handoff_now.get("item", "")), "nail")
    _check("the keeper walks for it", _panel._tending().has_meta("walking") or _panel._tending().position.x == float((_panel._handoff_now["stop"] as Vector2).x), true)
    _finish_handoff()
    _check("both done", [_panel._handoff_now, _panel._handoffs], [{}, []])
    _check("the bustle resumes", _panel._bustle_left >= _panel.BUSTLE_MIN, true)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## Someone arrives while the keeper walks to a handoff: the redraw rebuilds
## the keeper, whose walk goes on, and the good is set down when they get
## there — never before.
func _test_handoff_survives_a_redraw() -> void:
    _stall_with_buyer()
    var buyer_x := roundf((_panel._people.get_node("buyer") as Node2D).position.x)
    _panel.on_pay_resolved(_sale("keeper", "buyer"))
    (_panel._tending().get_meta("walking") as Tween).custom_step(0.3)
    var part_way: Vector2 = _panel._tending().position
    _at_tile("newcomer", _farmer(), Vector2i(8, 21) + Vector2i(-1, 0))
    _panel.refresh()
    var keeper: Node2D = _panel._tending()
    var good: Node2D = _panel._handoff_now["good"]
    _check("rebuilt where they had got to", keeper.position, part_way)
    _check("still on their way", keeper.has_meta("walking"), true)
    _check("the good still waits for them", [good.modulate.a, good.has_meta("tween")], [0.0, false])
    _finish_walk(keeper)
    _check("there", keeper.position.x, buyer_x)
    _check("then set down", good.has_meta("tween"), true)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


func _test_purchase_over_the_counter() -> void:
    _stall_with_buyer(Vector2i(-1, 1))
    var seller: Node2D = _panel._people.get_node("buyer")
    var keeper: Node2D = _panel._staff.get_node("keeper")
    # The keeper buys a bale the customer brought — not a good the shelf has.
    _panel.on_pay_resolved(_sale("buyer", "keeper", "iron"))
    var good: Node2D = _panel._handoff.get_node_or_null("handoff_iron")
    _check("the iron lies in front of the seller", good != null and good.position.x + StallScenes.CELL.x / 2.0 == roundf(seller.position.x), true)
    _check("there at once", good != null and good.modulate.a == 1.0, true)
    _check("the keeper comes for it", keeper.has_meta("walking"), true)
    if good != null:
        var on_counter := good.position
        _finish_walk(keeper)
        (good.get_meta("tween") as Tween).custom_step(60.0)
        _check("then up to the keeper", good.position.y < on_counter.y, true)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


func _test_no_handoff() -> void:
    var asked := _stall_with_buyer()
    var keeper: Node2D = _panel._staff.get_node("keeper")
    _panel.on_pay_resolved(_sale("keeper", "buyer", "apples", "declined"))
    _check("a declined offer: nothing", [_panel._handoff.get_child_count(), keeper.has_meta("walking"), asked.size()], [0, false, 1])
    _panel.on_pay_resolved(_sale("keeper", "buyer", "mending"))
    _check("selling a service: nothing over the counter", _panel._handoff.get_child_count(), 0)
    _panel.on_pay_resolved(_sale("buyer", "keeper", "mending"))
    _check("buying a service: nothing over the counter", [_panel._handoff.get_child_count(), keeper.has_meta("walking")], [0, false])
    _at_tile("other", _farmer(), Vector2i(8, 21) + Vector2i(-1, 1))
    _panel.refresh()
    _panel.on_pay_resolved(_sale("other", "buyer"))
    _check("customers trading: nothing", _panel._handoff.get_child_count(), 0)
    keeper = _panel._tending()
    # The goods sheet still downloading: the keeper stays put.
    var hook: Callable = _panel.texture_hook
    _panel.texture_hook = func(path: String) -> Texture2D:
        return null if path == StallScenes.GOODS_SHEET else _blank(128, 112)
    _panel.on_pay_resolved(_sale("keeper", "buyer"))
    _check("no goods sheet: nothing, and the keeper stays", [_panel._handoff.get_child_count(), keeper.has_meta("walking")], [0, false])
    _panel.texture_hook = hook
    _panel.close()
    _panel.on_pay_resolved(_sale("keeper", "buyer"))
    _check("a closed window: nothing", _panel._handoff.get_child_count(), 0)
    _panel.close()
    _panel.wares_hook = Callable()
    _done()


## The Tavern with its keeper behind the bar and `guest` at spot `spot`.
func _tavern_with_guest(spot: int) -> void:
    _clear_people()
    _panel.close()
    _world.meals = {}
    _person("keeper", _farmer(), _sid, {"work_structure_id": _sid})
    _person("guest", _farmer(), _sid)
    _panel.open(_sid)
    _seat("guest", spot)
    _panel._signature = ""
    _panel.refresh()


func _test_tavern_serves_a_meal_to_the_table() -> void:
    _tavern_with_guest(0)
    var keeper: Node2D = _panel._tending()
    _check("the Tavern's keeper is the one behind the bar", keeper != null and str(keeper.name) == "keeper", true)
    # The engine's npc_meal_started comes before the sale's pay_resolved.
    _world.apply_npc_meal_started({"id": "guest", "item": "stew", "structure_id": _sid, "credits": 1})
    _panel.on_pay_resolved(_sale("keeper", "guest", "stew", "accepted", 1, true))
    var good: Node2D = _panel._handoff.get_node_or_null("handoff_stew")
    var bar := Vector2(_panel.room["bar"]["x"], _panel.room["bar"]["y"])
    _check("the stew goes on the bar", good != null and good.position == _panel._cell_at(bar), true)
    _check("the stew's picture", good != null and (good.get_child(0).texture as AtlasTexture).region == StallScenes.cell_rect(2 * StallScenes.GOODS["stew"]), true)
    _check("the keeper steps to the bar's front", [keeper.has_meta("walking"), _panel._handoff_now["stop"]], [true, Vector2(_panel.room["reach"]["x"], _panel.room["reach"]["y"])])
    _check("not down until the keeper is there", good != null and good.modulate.a == 0.0, true)
    if good == null:
        _done()
        return
    _finish_walk(keeper)
    var t: Tween = good.get_meta("tween")
    t.custom_step(1.0)
    _check("set down", good.modulate.a, 1.0)
    t.custom_step(_panel.HANDOFF_HOLD)
    _check("the keeper steps back to their spot", keeper.has_meta("walking"), true)
    t.custom_step(3.0)
    var table := Vector2(_panel.room["spots"][0]["serve"]["x"], _panel.room["spots"][0]["serve"]["y"])
    _check("served to the table in front of the guest", good.position, _panel._cell_at(table))
    _check("eaten there, still to be seen", [good.modulate.a, good.is_queued_for_deletion()], [1.0, false])
    _check("the bar is free for the next", _panel._handoff_now, {})
    t.custom_step(60.0)
    _finish_walk(keeper)
    _check("the meal runs: it stays, nothing timed", [good.is_queued_for_deletion(), good.has_meta("tween"), good.has_meta("clearing")], [false, false, false])
    _world.apply_npc_meal_ended({"id": "guest", "item": "porridge", "structure_id": _sid})
    _check("another good's end leaves it", good.has_meta("clearing"), false)
    _world.apply_npc_meal_ended({"id": "guest", "item": "stew", "structure_id": _sid})
    _check("the meal over: the keeper comes to clear it", [good.has_meta("clearing"), _panel._handoff_now.get("clear") == good, keeper.has_meta("walking")], [true, true, true])
    _finish_walk(keeper)
    var c: Tween = good.get_meta("tween")
    c.custom_step(0.3)
    _check("it goes back toward the bar", good.position.x > _panel._cell_at(table).x, true)
    c.custom_step(60.0)
    _check("onto the bar and gone", [good.position, good.is_queued_for_deletion()], [_panel._cell_at(bar), true])
    _check("the keeper steps back", keeper.has_meta("walking"), true)
    _check("the bar is free", _panel._handoff_now, {})
    _panel.close()
    _done()


## The world's meal record: a meal ends when its last credit does; a second
## helping sets the count afresh; it is kept by where it is eaten.
func _test_world_meals() -> void:
    var ended := []
    var on_end := func(id: String, item: String): ended.append([id, item])
    _world.npc_meal_ended.connect(on_end)
    _world.meals = {}
    _world.apply_npc_meal_started({"id": "a", "item": "stew", "structure_id": _sid, "credits": 2})
    _check("eating stew here", _world.meal_of("a", _sid), "stew")
    _check("not somewhere else", _world.meal_of("a", "elsewhere"), "")
    _world.apply_npc_meal_ended({"id": "a", "item": "stew", "structure_id": _sid})
    _check("one credit of two ended: still eating", [_world.meal_of("a", _sid), ended], ["stew", []])
    _world.apply_npc_meal_started({"id": "a", "item": "stew", "structure_id": _sid, "credits": 2})
    _world.apply_npc_meal_ended({"id": "a", "item": "stew", "structure_id": _sid})
    _check("a second helping sets the count afresh", _world.meal_of("a", _sid), "stew")
    _world.apply_npc_meal_ended({"id": "a", "item": "porridge", "structure_id": _sid})
    _check("another good's end: still eating", _world.meal_of("a", _sid), "stew")
    _world.apply_npc_meal_ended({"id": "a", "item": "stew", "structure_id": "elsewhere"})
    _check("a stew ended somewhere else: still eating here", [_world.meal_of("a", _sid), ended], ["stew", []])
    _world.apply_npc_meal_ended({"id": "a", "item": "stew", "structure_id": _sid})
    _check("the last credit ended: done, and said so", [_world.meal_of("a", _sid), ended], ["", [["a", "stew"]]])
    _world.apply_npc_meal_ended({"id": "a", "item": "stew", "structure_id": _sid})
    _check("an end with no meal: nothing", ended.size(), 1)
    _world.apply_npc_meal_started({"id": "gone", "item": "stew", "structure_id": _sid, "credits": 1})
    _world.remove_npc_by_id("gone")
    _check("an actor removed, drawn or not: their meal goes too", _world.meals.has("gone"), false)
    _world.npc_meal_ended.disconnect(on_end)
    _done()


## A second bowl of stew to the same guest: the first bowl is cleared, one
## dish stays.
func _test_tavern_second_helping_one_dish() -> void:
    _tavern_with_guest(0)
    for i in 2:
        _world.apply_npc_meal_started({"id": "guest", "item": "stew", "structure_id": _sid, "credits": 1})
        _panel.on_pay_resolved(_sale("keeper", "guest", "stew", "accepted", 1, true))
        var bowl: Node2D = _panel._handoff_now["good"]
        _finish_handoff()
        _finish_walk(_panel._tending())
        if i == 0:
            _check("the first bowl waits on the meal", [_panel._dishes().size(), _panel._handoff_now], [1, {}])
            continue
        _check("the second bowl on the table, the only dish", _panel._dishes(), [bowl])
        _check("the first bowl is being cleared, once", [_panel._handoff_now.has("clear"), _panel._handoffs.size()], [true, 0])
    _finish_handoff()
    _check("then just the one", [_panel._dishes().size(), _panel._handoff_now], [1, {}])
    _panel.close()
    _done()


## A guest who gets up and goes leaves their dish to be cleared.
func _test_tavern_guest_leaving_clears_the_dish() -> void:
    _tavern_with_guest(0)
    _world.apply_npc_meal_started({"id": "guest", "item": "stew", "structure_id": _sid, "credits": 1})
    _panel.on_pay_resolved(_sale("keeper", "guest", "stew", "accepted", 1, true))
    var dish: Node2D = _panel._handoff_now["good"]
    _finish_handoff()
    _finish_walk(_panel._tending())
    _check("on the table", [dish.has_meta("buyer"), dish.has_meta("clearing")], [true, false])
    _world.placed_npcs["guest"].free()
    _world.placed_npcs.erase("guest")
    _panel.refresh()
    _check("the guest gone: the keeper clears it", [dish.has_meta("clearing"), _panel._handoff_now.get("clear") == dish], [true, true])
    _finish_handoff()
    _check("cleared", dish.is_queued_for_deletion(), true)

    # Gone while it is carried over: it lands, is cleared, and the bar is
    # free after — the queue does not stall on it.
    _tavern_with_guest(0)
    _world.apply_npc_meal_started({"id": "guest", "item": "stew", "structure_id": _sid, "credits": 1})
    _panel.on_pay_resolved(_sale("keeper", "guest", "stew", "accepted", 1, true))
    dish = _panel._handoff_now["good"]
    _world.placed_npcs["guest"].free()
    _world.placed_npcs.erase("guest")
    _panel.refresh()
    _finish_handoff()
    _check("landed with nobody there: cleared at once", [dish.has_meta("clearing"), _panel._handoff_now.get("clear") == dish], [true, true])
    _finish_handoff()
    _check("then gone, the bar free", [dish.is_queued_for_deletion(), _panel._handoff_now, _panel._handoffs], [true, {}, []])
    _panel.close()
    _done()


## Ale drunk at the table has no meal that takes time: it stays a while, then
## the keeper clears it.
func _test_tavern_ale_lingers_then_cleared() -> void:
    _tavern_with_guest(0)
    _panel.on_pay_resolved(_sale("keeper", "guest", "ale", "accepted", 1, true))
    var ale: Node2D = _panel._handoff_now["good"]
    _finish_handoff()
    var table := Vector2(_panel.room["spots"][0]["serve"]["x"], _panel.room["spots"][0]["serve"]["y"])
    _check("the ale on the table", [ale.position, ale.is_queued_for_deletion()], [_panel._cell_at(table), false])
    var linger: Tween = ale.get_meta("tween")
    linger.custom_step(_panel.TABLE_SECONDS - 1.0)
    _check("still there just before its time", ale.has_meta("clearing"), false)
    linger.custom_step(2.0)
    _check("then cleared", [ale.has_meta("clearing"), _panel._handoff_now.get("clear") == ale], [true, true])
    _finish_handoff()
    _check("gone", ale.is_queued_for_deletion(), true)
    _panel.close()
    _done()


## A seated guest buying to take home: handed over toward them, never left
## on the table.
func _test_tavern_take_home_handed_over() -> void:
    _tavern_with_guest(0)
    var bar := Vector2(_panel.room["bar"]["x"], _panel.room["bar"]["y"])
    var seated := Vector2(_panel.room["spots"][0]["x"], _panel.room["spots"][0]["y"] - 6)
    _check("take-home to a seated guest: toward them", _panel._serve_to("guest", bar, false), {"to": bar.lerp(seated, 0.6).round(), "stay": false})
    _panel.on_pay_resolved(_sale("keeper", "guest", "bread", "accepted", 1, false))
    var bread: Node2D = _panel._handoff_now["good"]
    _finish_handoff()
    _check("handed over and gone, not on the table", [bread.is_queued_for_deletion(), bread.has_meta("buyer")], [true, false])
    _panel.close()
    _done()


## The window opened partway through a meal shows its dish; a new meal of
## another good clears the old dish.
func _test_tavern_reopen_lays_a_running_meal() -> void:
    _tavern_with_guest(0)
    _world.apply_npc_meal_started({"id": "guest", "item": "stew", "structure_id": _sid, "credits": 1})
    _world.apply_npc_meal_started({"id": "keeper", "item": "porridge", "structure_id": _sid, "credits": 1})
    _panel.open(_sid)
    var dishes: Array = _panel._dishes()
    var table := Vector2(_panel.room["spots"][0]["serve"]["x"], _panel.room["spots"][0]["serve"]["y"])
    _check("the guest's stew on their table, nothing for the keeper", dishes.size(), 1)
    if dishes.size() == 1:
        var dish: Node2D = dishes[0]
        _check("the stew, in front of the guest", [str(dish.get_meta("buyer")), str(dish.get_meta("item")), dish.position], ["guest", "stew", _panel._cell_at(table)])
        _world.apply_npc_meal_started({"id": "guest", "item": "porridge", "structure_id": _sid, "credits": 1})
        _check("porridge in place of the stew: the stew is cleared", dish.has_meta("clearing"), true)
    _world.meals = {"guest": {"item": "stew", "structure_id": "another-place", "left": 1}}
    _panel.open(_sid)
    _check("a meal somewhere else: no dish here", _panel._dishes().size(), 0)
    _panel.close()
    _done()


func _test_tavern_serves_standing_and_at_the_door() -> void:
    _tavern_with_guest(2)
    var bar := Vector2(_panel.room["bar"]["x"], _panel.room["bar"]["y"])
    var standing := Vector2(_panel.room["spots"][2]["x"], _panel.room["spots"][2]["y"] - 6)
    var serve: Dictionary = _panel._serve_to("guest", bar, true)
    _check("a standing guest: most of the way to them, then gone", serve, {"to": bar.lerp(standing, 0.6).round(), "stay": false})
    _person("pc", _farmer(), _sid)
    _panel.refresh()
    var door := Vector2(_panel.room["door"]["x"], _panel.room["door"]["y"] - 6)
    _check("the player's character at the door", _panel._serve_to("pc", bar, true), {"to": bar.lerp(door, 0.6).round(), "stay": false})
    _check("someone not drawn: it stays on the bar", _panel._serve_to("nobody", bar, true), {})
    _panel.on_pay_resolved(_sale("keeper", "nobody", "bread", "accepted", 1))
    var bread: Node2D = _panel._handoff_now["good"]
    _finish_handoff()
    _check("…and fades there", [bread.position, bread.is_queued_for_deletion()], [_panel._cell_at(bar), true])
    _panel.on_pay_resolved(_sale("keeper", "guest", "nights_stay", "accepted", 1))
    _check("a night's stay: nothing on the bar", _panel._handoff.get_children().filter(func(n): return not n.is_queued_for_deletion()).size(), 0)
    _panel.on_pay_resolved(_sale("keeper", "guest", "ale", "accepted", 1))
    _finish_handoff()
    var good: Node2D = _panel._handoff.get_node_or_null("handoff_ale")
    _check("the ale went toward them and is gone", good != null and good.is_queued_for_deletion() and good.position.distance_to(_panel._cell_at(serve["to"])) < 1.0, true)
    _panel.close()
    _done()


func _test_tavern_keeper_buys() -> void:
    _tavern_with_guest(2)
    var keeper: Node2D = _panel._tending()
    _panel.on_pay_resolved(_sale("guest", "keeper", "carrots"))
    var good: Node2D = _panel._handoff.get_node_or_null("handoff_carrots")
    _check("the carrots lie on the bar at once", good != null and good.modulate.a == 1.0, true)
    _check("the keeper comes for them", keeper.has_meta("walking"), true)
    if good != null:
        var on_bar := good.position
        _finish_handoff()
        _check("taken toward the keeper", good.position.x > on_bar.x, true)
    _panel.on_pay_resolved(_sale("guest", "keeper", "mending"))
    _check("a service bought: nothing", _panel._handoff_now, {})
    _panel.close()
    _done()


## The keeper's step back is for the keeper who made the sale: someone who
## has taken the bar since is left alone; the same keeper, redrawn and on a
## walk, is brought back all the same.
func _test_tavern_keeper_back_is_theirs() -> void:
    _tavern_with_guest(2)
    var keeper: Node2D = _panel._tending()
    keeper.position = Vector2(90, 86)
    _panel._keeper_back("someone-else")
    _check("another's step back leaves this keeper be", keeper.has_meta("walking"), false)
    _panel._walk(keeper, Vector2(95, 86), _panel.WALK_SPEED, "west")
    _panel._keeper_back("keeper")
    _finish_walk(keeper)
    _check("their own: brought back to their spot, a walk stopped for it", keeper.position, keeper.get_meta("home"))
    _panel.close()
    _done()


## Reopening the window mid-handoff — the good on the bar, or a meal on the
## table — stops it: nothing from the old handoff moves anyone afterwards.
func _test_reopen_stops_a_handoff() -> void:
    for meal in [false, true]:
        var where := "at the table" if meal else "on the bar"
        _tavern_with_guest(0)
        if meal:
            _world.apply_npc_meal_started({"id": "guest", "item": "stew", "structure_id": _sid, "credits": 1})
        _panel.on_pay_resolved(_sale("keeper", "guest", "stew", "accepted", 1, true))
        var keeper: Node2D = _panel._tending()
        _finish_walk(keeper)
        var old: Node2D = _panel._handoff_now["good"]
        var t: Tween = old.get_meta("tween")
        t.custom_step(1.0 if not meal else 6.0)
        _panel.open(_sid)
        _check("reopened (%s): the old good and its tween are gone" % where, [is_instance_valid(old), t.is_running()], [false, false])
        # A meal still running is laid afresh on the table; nothing else.
        _check("reopened (%s): nothing handed or queued" % where, [_panel._handoff.get_child_count(), _panel._handoff_now, _panel._handoffs], [1 if meal else 0, {}, []])
        keeper = _panel._tending()
        _check("reopened (%s): the keeper stands still at their spot" % where, [keeper.has_meta("walking"), keeper.position], [false, keeper.get_meta("home")])
    _world.meals = {}
    _panel.close()
    _done()


const INN := "019d98af-ac9b-7833-8e03-5a7015bb5b0c"


## The Inn with its keeper behind the desk, the player's character at the
## door and `guest` at spot `spot`.
func _inn_with_guest(spot: int) -> void:
    _clear_people()
    _panel.close()
    _world.meals = {}
    _person("keeper", _farmer(), INN, {"work_structure_id": INN})
    _person("pc", _farmer(), INN)
    _person("guest", _farmer(), INN)
    _panel.open(INN)
    _seat("guest", spot)
    _panel._signature = ""
    _panel.refresh()


## The people drawn in `layer` — a room's furniture shares the people's layer.
func _drawn(layer: Node2D) -> Array:
    var out: Array = []
    for child in layer.get_children():
        if not child.has_meta("piece"):
            out.append(child)
    return out


## The room's furniture and sorted animations, in the people's layer.
func _pieces() -> Array:
    var out: Array = []
    for child in _panel._people.get_children():
        if child.has_meta("piece"):
            out.append(child)
    return out


## The piece whose top-left is at (x, y), or null.
func _piece_at(x: int, y: int) -> Sprite2D:
    for p in _pieces():
        if p.position.x == x and p.position.y + p.offset.y == y:
            return p
    return null


## The Inn's keeper stands behind the desk — the desk sorts in front of them
## — and comes and goes by the stairs; everyone else uses the door.
func _test_inn_keeper_behind_the_desk() -> void:
    _inn_with_guest(3)
    _check("the Inn has a room", _panel.is_open(), true)
    var desk := _piece_at(20, 54)
    _check("the desk is a piece", desk != null, true)
    var keeper: Node2D = _panel._people.get_node_or_null("keeper")
    _check("the keeper is drawn among the people", keeper != null, true)
    _check("the guest and the player's character too", [_panel._people.get_node_or_null("guest") != null, _panel._people.get_node_or_null("pc") != null], [true, true])
    _check("the keeper is the one tending", _panel._tending() == keeper, true)
    if keeper == null or desk == null:
        _done()
        return
    _check("the desk sorts in front of the keeper", keeper.position.y < desk.position.y, true)
    var guest: Node2D = _panel._people.get_node("guest")
    _check("a guest at the desk sorts in front of it", guest.position.y > desk.position.y, true)
    var stairs := Vector2(_panel.room["keeper_door"]["x"], _panel.room["keeper_door"]["y"])
    var door := Vector2(_panel.room["door"]["x"], _panel.room["door"]["y"])
    _check("the keeper's way is the stairs", _panel._doorway(keeper.position, true), stairs)
    _check("a guest's way is the door", _panel._doorway(Vector2(48, 100), false), door)
    _panel.k = 4
    _panel._stretch = Vector2.ONE
    _panel._scene.position = Vector2.ZERO
    _panel._clip.position = Vector2.ZERO
    _check("a tap on the keeper finds them", str(_panel.thing_at((keeper.position + Vector2(0, -16)) * 4).get("id", "")), "keeper")
    # The keeper walks in down the stairs, behind the desk.
    _clear_people()
    _panel._signature = ""
    _panel.refresh()
    _person("keeper", _farmer(), INN, {"work_structure_id": INN})
    _panel.refresh()
    keeper = _panel._people.get_node_or_null("keeper")
    _check("coming in from the stairs", keeper != null and keeper.position == stairs and keeper.has_meta("entering"), true)
    _finish_walk(keeper)
    _check("then at the desk", keeper.position, keeper.get_meta("home"))
    # Speaking does not stop the keeper as it stops a stall's bustle.
    _panel._walk(keeper, Vector2(30, 66), _panel.WALK_SPEED, "south")
    _panel.say("keeper", "Welcome to the Inn.")
    _check("a room's keeper keeps walking while speaking", keeper.has_meta("walking"), true)
    _finish_walk(keeper)
    # Leaving, the keeper goes back up the stairs, behind the desk.
    _clear_people()
    _panel.refresh()
    var ghost: Node2D = _panel._leaving("keeper")
    _check("the keeper walks out", ghost != null, true)
    if ghost != null:
        _finish_walk(ghost)
        _check("up the stairs", ghost.position, stairs)
        _check("behind the desk", ghost.position.y < desk.position.y, true)
    _panel.close()
    _done()


## The fire, the pot and the candle play their strips: the fire as part of the
## wall, the pot and the candle sorted with the furniture; a room without
## `anims` has none.
func _test_inn_animations() -> void:
    _inn_with_guest(0)
    var anims: Array = _panel.room["anims"]
    var pot_spec: Dictionary = {}
    var wall := 0
    for a in anims:
        if str(a["sheet"]).ends_with("inn_pot.png"):
            pot_spec = a
        if not a.has("sort_y"):
            wall += 1
    _check("the fire is part of the wall", _panel._anims.get_child_count(), wall)
    var pot := _piece_at(int(pot_spec["x"]), int(pot_spec["y"]))
    _check("the pot is drawn among the furniture", pot != null, true)
    if pot != null:
        _check("sorted at its line", pot.position.y, float(pot_spec["sort_y"]))
        _check("its strip in frames", pot.hframes, int(pot_spec["frames"]))
        var step := 1.0 / float(pot_spec["fps"])
        # From the start of a frame, so a step crosses exactly one boundary.
        _panel._anim_time = 0.0
        _panel._animate(0.0)
        _check("the first frame at the start", pot.frame, 0)
        _panel._animate(step * 1.5)
        _check("the next frame after a frame's time", pot.frame, 1)
        _panel._animate(step * float(pot.hframes))
        _check("round again after the whole strip", pot.frame, 1)
    _panel._process(0.5)
    _check("the window's clock runs them", _panel._anim_time > 0.5, true)
    _panel.open(_sid)
    var playing := 0
    for p in _pieces():
        if p.has_meta("fps"):
            playing += 1
    _check("the Tavern has none", [_panel._anims.get_child_count(), playing], [0, 0])
    _panel.close()
    _done()


## A bowl of porridge sold at the desk is set on it, then served to the table
## in front of the bench where the guest sits.
func _test_inn_serves_porridge_to_the_bench() -> void:
    _inn_with_guest(0)
    var keeper: Node2D = _panel._tending()
    _world.apply_npc_meal_started({"id": "guest", "item": "porridge", "structure_id": INN, "credits": 1})
    _panel.on_pay_resolved(_sale("keeper", "guest", "porridge", "accepted", 1, true))
    var good: Node2D = _panel._handoff.get_node_or_null("handoff_porridge")
    var desk := Vector2(_panel.room["bar"]["x"], _panel.room["bar"]["y"])
    _check("the porridge goes on the desk", good != null and good.position == _panel._cell_at(desk), true)
    var desk_piece := _piece_at(20, 54)
    _check("the keeper steps to it, behind the desk", [keeper.has_meta("walking"), desk_piece != null and (_panel._handoff_now["stop"] as Vector2).y < desk_piece.position.y], [true, true])
    if good == null:
        _done()
        return
    _finish_walk(keeper)
    (good.get_meta("tween") as Tween).custom_step(60.0)
    var table := Vector2(_panel.room["spots"][0]["serve"]["x"], _panel.room["spots"][0]["serve"]["y"])
    _check("served to the table in front of the bench", good.position, _panel._cell_at(table))
    _check("and stays while the meal runs", good.is_queued_for_deletion(), false)
    _world.apply_npc_meal_ended({"id": "guest", "item": "porridge", "structure_id": INN})
    _check("cleared when it ends", good.has_meta("clearing"), true)
    _finish_handoff()
    _world.meals = {}
    _panel.close()
    _done()


## LLM-767: a room's furniture is drawn among the people, each piece sorted
## by its own line — the Tavern's keeper stands inside the bar, its back arm
## behind them and its front arm in front; a diner sits in front of their
## chair and the table. The pieces outlive a redraw of the people, wait for
## their sheet, and what lies on the bar and tables is drawn over everyone
## (at a stall, still under the customers).
func _test_room_pieces() -> void:
    _tavern_with_guest(0)
    var pieces: Array = _panel.room["pieces"]
    _check("one sprite per piece", _pieces().size(), pieces.size())
    var back := _piece_at(82, 50)
    var front := _piece_at(82, 80)
    _check("the bar is two pieces", back != null and front != null, true)
    if back == null or front == null:
        _done()
        return
    var spec: Dictionary = pieces[0]
    _check("a piece is cut from the sheet where it stands", back.region_rect, Rect2(spec["x"], spec["y"], spec["w"], spec["h"]))
    _check("and stands at its sort line", back.position, Vector2(spec["x"], spec["sort_y"]))
    var keeper: Node2D = _panel._tending()
    _check("the keeper stands between the bar's arms", [keeper.position.y > back.position.y, keeper.position.y < front.position.y], [true, true])
    var guest: Node2D = _panel._people.get_node("guest")
    var chair := _piece_at(8, 70)
    var table := _piece_at(20, 72)
    _check("the diner sits in front of their chair and the table", [guest.position.y > chair.position.y, guest.position.y > table.position.y], [true, true])
    # A tap on furniture finds nobody; a person over furniture is still found.
    _panel.k = 4
    _panel._stretch = Vector2.ONE
    _panel._scene.position = Vector2.ZERO
    _panel._clip.position = Vector2.ZERO
    _check("a tap on the bar's bare end finds nothing", _panel.thing_at(Vector2(126, 104) * 4), {})
    _check("a tap on the table's far side finds nothing", _panel.thing_at(Vector2(36, 74) * 4), {})
    _check("a tap on the keeper over the bar finds them", str(_panel.thing_at((keeper.position + Vector2(0, -16)) * 4).get("id", "")), "keeper")
    # A newcomer redraws the people; the furniture stays as it was.
    var before := _pieces()
    _person("newcomer", _farmer(), _sid)
    _panel.refresh()
    _check("someone came in", _panel._people.get_node_or_null("newcomer") != null, true)
    _check("the furniture is not rebuilt", _pieces(), before)
    # What lies on the bar and tables over the people in a room; under the
    # customers at a stall.
    _check("in a room goods are over the people", _panel._handoff.get_index() > _panel._people.get_index(), true)
    _place_stall()
    _panel.wares_hook = func(_s: String) -> Array: return []
    _panel.open(STALL)
    _check("at a stall under the customers", _panel._handoff.get_index() < _panel._people.get_index(), true)
    _check("a stall has no pieces", _pieces().size(), 0)
    _panel.wares_hook = Callable()
    _panel.open(_sid)
    _check("back in a room, over them again", _panel._handoff.get_index() > _panel._people.get_index(), true)
    # The pieces' sheet still downloading: none until it lands.
    var hook: Callable = _panel.texture_hook
    _panel.texture_hook = func(path: String) -> Texture2D:
        return null if path.ends_with("_pieces.png") else _blank(128, 112)
    _panel._apply_textures()
    _check("no sheet yet, no pieces", _pieces().size(), 0)
    _panel.texture_hook = hook
    _panel._apply_textures()
    _check("then the pieces", _pieces().size(), pieces.size())
    _panel.close()
    _done()


## LLM-768: people stand where the engine put them. A square on a station is
## drawn at the station; any other open square at its middle; no square (every
## square taken, or the plan not loaded) is counted, not drawn. The frames move
## a person's square, and a plan that lands after the window opened is used.
func _test_room_squares() -> void:
    var room := InteriorRooms.with_layout(InteriorRooms.room_for(_sid), _catalog().rooms[_sid])
    var plan: Dictionary = _catalog().rooms[_sid]
    var st: Dictionary = plan["stations"][0]
    var spot := InteriorRooms.spot_at(room, InteriorRooms.square_of(st["square"]))
    _check("a station's square is drawn at the station", [spot["x"], spot["y"], spot["pose"]], [int(st["at"][0]), int(st["at"][1]), st["pose"]])
    _check("the door's square at the door", InteriorRooms.spot_at(room, InteriorRooms.square_of(plan["door"]["square"])), room["door"])
    var floor := InteriorRooms.spot_at(room, Vector2i(0, 4))
    _check("a plain square at its middle, standing", [floor["x"], floor["y"], floor["pose"]], [8, 78, "stand"])
    _check("off the grid: nowhere", InteriorRooms.spot_at(room, Vector2i(9, 9)), {})
    _check("no square: nowhere", InteriorRooms.spot_at(room, Vector2i(-1, -1)), {})
    _check("no plan merged: nowhere", InteriorRooms.spot_at(InteriorRooms.room_for(_sid), Vector2i(0, 4)), {})

    _clear_people()
    _person("keeper", _farmer(), _sid, {"work_structure_id": _sid})
    _person("guest", _farmer(), _sid)
    var unplaced := _person("unplaced", _farmer(), _sid)
    unplaced.remove_meta("room_square")
    _panel.open(_sid)
    var guest: Node2D = _panel._people.get_node_or_null("guest")
    _finish_walk(guest)
    var guest_spot := InteriorRooms.spot_at(_panel.room, InteriorRooms.square_of(_world.placed_npcs["guest"].get_meta("room_square")))
    _check("the guest stands on their square", guest != null and guest.position == Vector2(guest_spot["x"], guest_spot["y"]), true)
    _check("no square: not drawn, counted", [_panel._people.get_node_or_null("unplaced") == null, _panel.more_label.text], [true, "and 1 more"])

    # The engine moves the guest: npc_room_square_changed.
    _world.apply_npc_room_square_change({"id": "guest", "structure_id": _sid, "square": [0, 4]})
    _check("the frame sets the square", _world.placed_npcs["guest"].get_meta("room_square"), [0, 4])
    _panel.refresh()
    guest = _panel._people.get_node("guest")
    _finish_walk(guest)
    _check("drawn on the new square", guest.position, Vector2(8, 78))
    _world.apply_npc_room_square_change({"id": "guest", "structure_id": _sid})
    _check("a frame with no square takes it away", _world.placed_npcs["guest"].has_meta("room_square"), false)
    _world.apply_npc_inside_change({"id": "guest", "inside": true, "inside_structure_id": _sid, "room_square": [2, 4]})
    _check("npc_inside_changed carries the square", _world.placed_npcs["guest"].get_meta("room_square"), [2, 4])
    _world.apply_npc_inside_change({"id": "guest", "inside": false})
    _check("leaving drops it", _world.placed_npcs["guest"].has_meta("room_square"), false)
    _panel.close()

    # The plans not loaded yet: nobody drawn; once they land, everyone is.
    var plans: Dictionary = _catalog().rooms
    _catalog().rooms = {}
    _clear_people()
    _person("keeper", _farmer(), _sid, {"work_structure_id": _sid})
    _world.placed_npcs["keeper"].set_meta("room_square", plan["keeper"]["square"])
    _panel.open(_sid)
    _check("no plan: nobody drawn", _drawn(_panel._people).size(), 0)
    _catalog().rooms = plans
    _panel._signature = ""
    _panel.refresh()
    _check("the plan lands: the keeper is drawn", _panel._people.get_node_or_null("keeper") != null, true)
    _panel.close()
    _done()
