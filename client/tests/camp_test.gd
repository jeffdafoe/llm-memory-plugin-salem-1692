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
    "_test_fire_game",
    "_test_fire_helpers",
    "_test_fire_panel",
    "_test_tent_room",
    "_test_fire_panel_stale_answer",
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


func _fire(d := 0.0):
    var rng := RandomNumberGenerator.new()
    rng.seed = 7
    return load("res://scripts/repair_games.gd").make("fire", rng, d)


## Strike the steel at the flint's edge.
func _strike(g) -> int:
    g.steel = g.EDGE
    return g.press(null)


func _test_fire_game() -> void:
    var G = load("res://scripts/repair_games.gd")
    var g = _fire()
    _check("starts at the strike", g.phase, g.Phase.STRIKE)
    g.steel = 0.0
    _check("a strike off the edge misses", g.press(null), G.Result.MISS)
    _check("a miss keeps striking", g.phase, g.Phase.STRIKE)
    _check("a strike at the edge throws a spark", _strike(g), G.Result.PROGRESS)
    _check("the spark is an ember", g.phase, g.Phase.BLOW)
    _check("the ember starts low", g.glow, g.START_GLOW)

    # Breathe it into the band, letting it fade between breaths.
    var r = g.press(null)  # 0.52 — under the band
    _check("a breath short of the band counts nothing", g.breaths, 0)
    g.update(0.0)
    r = g.press(null)  # 0.74 — in the band
    _check("a breath into the band counts", g.breaths, 1)
    for i in 3:
        g.update(1.0)  # fades 0.22
        r = g.press(null)
    _check("the needed breaths catch it", r, G.Result.HIT)
    _check("caught: holding", g.holding, true)
    _check("clean: no ember lost", g.spent, 0)

    # Blown out: breaths too fast.
    g = _fire()
    _strike(g)
    for i in 4:
        r = g.press(null)
    _check("four quick breaths blow it out", r, G.Result.FAIL)
    _check("blown out, not dark", g.blown_out, true)
    _check("the tinder is spent", g.phase, g.Phase.SPENT)
    _check("an ember lost", g.spent, 1)
    _check("spent: a press does nothing", g.press(null), G.Result.NONE)
    g.update(g.SPENT_TIME + 0.01)
    _check("after the hold: strike again", g.phase, g.Phase.STRIKE)

    # Let go dark: no breaths at all.
    g = _fire()
    _strike(g)
    g.update(5.0)
    _check("left alone it goes dark", g.take_events(), [G.Result.FAIL])
    _check("dark, not blown out", g.blown_out, false)

    # Harder: narrower band, quicker fade, more breaths, faster swing.
    var easy = _fire(0.0)
    var hard = _fire(1.0)
    _check("harder: more breaths", hard.breaths_needed > easy.breaths_needed, true)
    _check("harder: narrower band", hard.band_hi - hard.band_lo < easy.band_hi - easy.band_lo, true)
    _check("harder: quicker fade", hard.fade > easy.fade, true)
    _check("harder: faster swing", hard.swing > easy.swing, true)
    _check("hint names the strike", easy.hint().begins_with("Strike"), true)
    _completed[_current] = true


func _test_fire_helpers() -> void:
    _check("unlit is out", Camp.fire_out("unlit"), true)
    _check("ashes is out", Camp.fire_out("ashes"), true)
    _check("burning is not out", Camp.fire_out("burning"), false)
    _check("dying is not out", Camp.fire_out("dying"), false)
    _check("two tiles off: in reach", Camp.near_fire(Vector2i(2, 2), Vector2i(0, 0)), true)
    _check("three tiles off: out of reach", Camp.near_fire(Vector2i(3, 0), Vector2i(0, 0)), false)
    _completed[_current] = true


func _test_fire_panel() -> void:
    var panel: CanvasLayer = load("res://scripts/fire_panel.gd").new()
    root.add_child(panel)
    var lit := [0]
    panel.lit.connect(func(): lit[0] += 1)
    panel.open("fire-1")
    _check("open: the prompt says why", panel.prompt_label.text, "Sleeping with a fire will give you better rest.")
    _check("open: Light it offered", panel.light_button.visible, true)
    _check("open: no game yet", panel.stage.visible, false)
    panel._on_light_pressed()
    _check("light: the game shows", panel.stage.visible, true)
    _check("light: the fire game", panel.stage.game_kind, "fire")
    _check("light: playing", panel.stage.playing, true)

    # A win the engine refuses: its line shows, play stops.
    panel.send_hook = func(done: Callable): done.call(422, "{\"error\":\"walk to your fire first.\"}")
    panel._on_round_won(false)
    _check("refused: the engine's line", panel.status_label.text, "Walk to your fire first.")
    _check("refused: play stops", panel.stage.playing, false)
    _check("refused: not lit", lit[0], 0)

    # Again, and the engine lights it.
    panel._on_light_pressed()
    panel.send_hook = func(done: Callable): done.call(200, "{\"result\":\"ok\"}")
    panel._on_round_won(true)
    _check("lit: signalled", lit[0], 1)
    panel.close()
    _check("closed", panel.is_open(), false)
    panel.queue_free()
    _completed[_current] = true


## The tent's room: found by its plan's art, the bedroll a bed with its cover.
func _test_tent_room() -> void:
    var plan := {"structure_id": "tent-1", "art": "tent", "name": "Tent",
        "door": {"square": [3, 6], "at": [56, 110], "facing": "north"},
        "keeper": {"square": [4, 3], "at": [72, 62], "facing": "south"},
        "stations": [{"id": "bedroll", "kind": "bed", "square": [1, 5], "at": [28, 90], "facing": "south", "pose": "sleep", "use": ["sleep"]}]}
    _check("the tent has a room", InteriorRooms.has_room("tent-1", plan), true)
    var room := InteriorRooms.with_layout(InteriorRooms.room_for("tent-1", plan), plan)
    _check("titled from the plan", room.get("title", ""), "Tent")
    _check("the bedroll has a cover", room.get("covers", {}).has("bedroll"), true)
    _check("the bedroll is a bed", str(InteriorRooms.spot_at(room, Vector2i(1, 5)).get("pose", "")), "sleep")
    _check("no plan, no tent room", InteriorRooms.has_room("tent-1", {}), false)
    _completed[_current] = true


## An answer to an earlier opening's light is dropped: closed and opened again
## meanwhile, the new window is not lit by it.
func _test_fire_panel_stale_answer() -> void:
    var panel: CanvasLayer = load("res://scripts/fire_panel.gd").new()
    root.add_child(panel)
    var lit := [0]
    panel.lit.connect(func(): lit[0] += 1)
    var held := []
    panel.send_hook = func(done: Callable): held.append(done)
    panel.open("fire-1")
    panel._on_light_pressed()
    panel._on_round_won(false)
    _check("first light sent", held.size(), 1)
    panel.close()
    panel.open("fire-1")
    held[0].call(200, "{\"result\":\"ok\"}")
    _check("stale answer: not lit", lit[0], 0)
    _check("stale answer: the new window still asks", panel.light_button.visible, true)
    panel._on_light_pressed()
    panel._on_round_won(false)
    held[1].call(200, "{\"result\":\"ok\"}")
    _check("this opening's answer lights it", lit[0], 1)
    panel.queue_free()
    _completed[_current] = true
