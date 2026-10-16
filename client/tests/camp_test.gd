extends SceneTree

## Headless harness for LLM-798 (client/scripts/camp.gd and its world.gd
## wiring): a player pitching or striking a camp plays the beat's motion and
## faces the piece being set down or taken up.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/camp_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_motion",
    "_test_tent_raised",
    "_test_world_faces_the_piece",
    "_test_beat_from_frame_and_dto",
    "_test_home_chip",
]

const BODY := "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png"

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    for t in TESTS:
        _current = t
        await call(t)
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)
    print("\n[camp_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[camp_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _test_motion() -> void:
    _check("pitch, a prop: crouch", Camp.motion("pitch", "fire", 0.5), "pick")
    _check("strike, a prop: crouch", Camp.motion("strike", "stool", 0.5), "pick")
    _check("pitch, the tent, early: lay out the canvas", Camp.motion("pitch", "tent", 0.5), "pick")
    _check("pitch, the tent, late: drive the pegs", Camp.motion("pitch", "tent", Camp.TENT_SWITCH + 0.1), "chop")
    _check("strike, the tent, early: pull the pegs", Camp.motion("strike", "tent", 0.5), "pull")
    _check("strike, the tent, late: roll the canvas", Camp.motion("strike", "tent", Camp.TENT_SWITCH + 0.1), "pick")
    _check("camp work: pitch", Camp.is_camp_work("pitch"), true)
    _check("camp work: strike", Camp.is_camp_work("strike"), true)
    _check("camp work: not a repair", Camp.is_camp_work("repair"), false)
    _completed[_current] = true


func _test_tent_raised() -> void:
    _check("bundle to standing", Camp.tent_raised(Camp.TENT_ASSET_ID, "packed", "closed"), true)
    _check("flap opening is not raising", Camp.tent_raised(Camp.TENT_ASSET_ID, "closed", "open"), false)
    _check("dropping to the bundle is not raising", Camp.tent_raised(Camp.TENT_ASSET_ID, "closed", "packed"), false)
    _check("another asset never", Camp.tent_raised("some-house", "packed", "closed"), false)
    _completed[_current] = true


## A farmer doll in a container, as world.gd draws a villager.
func _farmer(world) -> Array:
    var blank := ImageTexture.create_from_image(Image.create(1024, 1024, false, Image.FORMAT_RGBA8))
    var sheets := {BODY: blank}
    for prop in FarmerRig.PROPS.values():
        sheets[prop["sheet"]] = ImageTexture.create_from_image(Image.create(224, 32, false, Image.FORMAT_RGBA8))
    var doll := FarmerDoll.new()
    doll.setup({"id": "s1", "name": "Farmer", "sheet": BODY, "rig": "farmer_base",
        "frame_width": 64, "frame_height": 64, "render_scale": 2.0,
        "layers": [{"sheet": BODY, "ramps": {}}], "animations": []}, sheets)
    doll.name = "CharacterSprite"
    doll.scale = Vector2(2, 2)
    doll.speed_scale = 0.0
    var npc := Node2D.new()
    npc.add_child(doll)
    root.add_child(npc)
    world.placed_npcs["pc"] = npc
    npc.set_meta("facing", "south")
    return [npc, doll]


func _test_world_faces_the_piece() -> void:
    var world = load("res://scripts/world.gd").new()
    var pair := _farmer(world)
    var npc: Node2D = pair[0]
    var doll: AnimatedSprite2D = pair[1]

    # Setting the fire ring down, east of the player: a crouch facing east.
    npc.set_meta("source_activity_kind", "pitch")
    npc.set_meta("source_activity_object_id", "tent")
    world._set_camp_beat(npc, "fire", {"x": 96.0, "y": 0.0})
    world._apply_activity_animation(npc)
    _check("pitch the fire: crouch toward it", doll.animation, &"east_pick")

    # Taking the woodpile up to the west.
    npc.set_meta("source_activity_kind", "strike")
    world._set_camp_beat(npc, "woodpile", {"x": -96.0, "y": 0.0})
    world._apply_activity_animation(npc)
    _check("strike the woodpile: crouch toward it", doll.animation, &"west_pick")

    # The tent's beat, late: rolling the canvas, facing the tent to the north.
    world._set_camp_beat(npc, "tent", {"x": 0.0, "y": -64.0})
    npc.set_meta("source_activity_beat_msec", Time.get_ticks_msec() - int((Camp.TENT_SWITCH + 1.0) * 1000))
    world._apply_activity_animation(npc)
    _check("strike the tent, late: roll the canvas", str(doll.animation).ends_with("_pick"), true)

    # The work ends: back to idle.
    npc.set_meta("source_activity_kind", "")
    world._set_camp_beat(npc, "", null)
    world._apply_activity_animation(npc)
    _check("work done: idle", str(doll.animation).ends_with("_idle"), true)
    npc.queue_free()
    world.free()
    _completed[_current] = true


func _test_beat_from_frame_and_dto() -> void:
    var world = load("res://scripts/world.gd").new()
    var pair := _farmer(world)
    var npc: Node2D = pair[0]
    world.apply_npc_source_activity_changed({"id": "pc", "kind": "pitch", "object_id": "tent",
        "beat": "stool", "target": {"x": 10.0, "y": 20.0}})
    _check("frame: beat kept", npc.get_meta("source_activity_beat", ""), "stool")
    _check("frame: target kept", npc.get_meta("source_activity_target", Vector2.INF), Vector2(10, 20))
    world.apply_npc_source_activity_changed({"id": "pc"})
    _check("clear: no beat", npc.get_meta("source_activity_beat", "x"), "")
    _check("clear: no target", npc.has_meta("source_activity_target"), false)

    var agent: Dictionary = root.get_node("VillageApi").normalize_agent({"id": "pc", "source_activity_kind": "strike",
        "source_activity_beat": "fire", "source_activity_target": {"x": 1.0, "y": 2.0}})
    _check("dto: beat forwarded", agent.get("source_activity_beat", ""), "fire")
    _check("dto: target forwarded", agent.get("source_activity_target", null), {"x": 1.0, "y": 2.0})
    npc.queue_free()
    world.free()
    _completed[_current] = true


## The top bar's home chip: a paid room shows first, else a pitched tent.
func _test_home_chip() -> void:
    var bar: Control = load("res://scripts/top_bar.gd").new()
    root.add_child(bar)
    bar.set_camp(true, "by the Inn")
    _check("tent pitched: home chip", bar.lodging_label.text, "Home: your tent")
    _check("tent pitched: says where", bar.lodging_label.tooltip_text, "Your tent is pitched by the Inn.")
    _check("tent pitched: shown", bar.lodging_label.visible, true)
    bar.set_lodging("Inn", "")
    _check("a room as well: the room shows", bar.lodging_label.text, "Lodged at Inn")
    bar.set_lodging("", "")
    _check("room gone: back to the tent", bar.lodging_label.text, "Home: your tent")
    bar.set_camp(false, "")
    _check("tent struck: hidden", bar.lodging_label.visible, false)
    bar.queue_free()
    _completed[_current] = true
