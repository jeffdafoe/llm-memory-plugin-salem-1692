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


func _test_seat_animations() -> void:
    var anims := FarmerDoll.rig_animations()
    for f in ["south", "north", "east", "west"]:
        _check("%s_seat exists" % f, anims.has(f + "_seat"), true)
    _done()
