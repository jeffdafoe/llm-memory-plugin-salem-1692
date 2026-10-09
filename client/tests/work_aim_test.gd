extends SceneTree

## Headless harness for LLM-743 (client/scripts/work_aim.gd, work_chips.gd): a
## repairer turns toward the work and steps in so the axe lands on its visible
## edge, and each blow throws chips coloured by what is being mended.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/work_aim_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_faces_and_steps_toward_the_work",
    "_test_step_is_capped",
    "_test_never_the_north_chop",
    "_test_inside_the_box_faces_its_centre",
    "_test_no_target_no_aim",
    "_test_strike_point",
    "_test_chip_palette",
    "_test_burst_throws_chips",
    "_test_world_wiring",
    "_test_debris_is_the_mark",
]

const SCALE := 2.0
const MAX_STEP := 48.0

var _aim = null
var _chips = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _aim = load("res://scripts/work_aim.gd")
    _chips = load("res://scripts/work_chips.gd")
    for t in TESTS:
        _current = t
        await call(t)
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)
    print("\n[work_aim_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[work_aim_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _done() -> void:
    _completed[_current] = true


## The live complaint: the work off to one side, and the axe swung the other
## way from a tile off. Each side picks its own facing and closes the gap.
func _test_faces_and_steps_toward_the_work() -> void:
    var feet := Vector2.ZERO
    # East strike lands at (63, -24); the box's near edge is 100, bite 4.
    var east: Dictionary = _aim.aim(feet, Rect2(100, -60, 40, 60), SCALE, MAX_STEP)
    _check("work to the east: face east", east.get("facing"), "east")
    _check("work to the east: step in to its edge", east.get("step"), Vector2(41, 0))
    var west: Dictionary = _aim.aim(feet, Rect2(-140, -60, 40, 60), SCALE, MAX_STEP)
    _check("work to the west: face west", west.get("facing"), "west")
    _check("work to the west: step in to its edge", west.get("step"), Vector2(-41, 0))
    # South strike lands at (2, 22); the box starts at 30.
    var south: Dictionary = _aim.aim(feet, Rect2(-20, 30, 40, 30), SCALE, MAX_STEP)
    _check("work below: face south", south.get("facing"), "south")
    _check("work below: step down to it", south.get("step"), Vector2(0, 12))
    # North strike lands at (6, -75); the box ends at -100.
    var north: Dictionary = _aim.aim(feet, Rect2(-20, -140, 40, 40), SCALE, MAX_STEP)
    # LLM-747: never the north chop — a side swing steps up to it. East strike
    # (63,-24) to the box's corner (16,-104) is (-47,-80), capped.
    _check("work above: a side swing, not north", north.get("facing"), "east")
    _check("work above: step up to it, capped", (north.get("step") as Vector2).is_equal_approx(Vector2(-47, -80).limit_length(MAX_STEP)), true)
    _done()


func _test_step_is_capped() -> void:
    var far: Dictionary = _aim.aim(Vector2.ZERO, Rect2(300, -60, 40, 60), SCALE, MAX_STEP)
    _check("far work: still faces it", far.get("facing"), "east")
    _check("far work: step capped", far.get("step"), Vector2(MAX_STEP, 0))
    _done()


## Standing within a big sprite's box (behind a building) every strike lands;
## the facing that points at the box's centre wins and nothing steps.
func _test_inside_the_box_faces_its_centre() -> void:
    var inside: Dictionary = _aim.aim(Vector2.ZERO, Rect2(-200, -300, 400, 320), SCALE, MAX_STEP)
    # Every side strike lands; north is never aimed, so the first side wins.
    _check("inside: a side swing", inside.get("facing"), "east")
    _check("inside: no step", inside.get("step"), Vector2.ZERO)
    _done()


func _test_no_target_no_aim() -> void:
    _check("empty rect: no aim", _aim.aim(Vector2.ZERO, Rect2(), SCALE, MAX_STEP), {})
    _check("flat rect: no aim", _aim.aim(Vector2.ZERO, Rect2(10, 10, 0, 20), SCALE, MAX_STEP), {})
    # A box smaller than the bite aims at its centre.
    var tiny: Dictionary = _aim.aim(Vector2.ZERO, Rect2(98, -26, 6, 6), SCALE, MAX_STEP)
    _check("tiny box: lands on its centre", _aim.strike_point(Vector2.ZERO, tiny["step"], tiny["facing"], SCALE), Vector2(101, -23))
    _done()


func _test_strike_point() -> void:
    _check("strike point: feet + step + strike at scale",
        _aim.strike_point(Vector2(10, 20), Vector2(5, 0), "east", SCALE), Vector2(10 + 5 + 63, 20 - 24))
    _check("strike point: unknown facing is the stepped feet",
        _aim.strike_point(Vector2(10, 20), Vector2(5, 0), "up", SCALE), Vector2(15, 20))
    _done()


func _test_chip_palette() -> void:
    var tree: Array = _chips.palette("Fallen Maple Old maple")
    _check("tree: bark", tree.has(_chips.BARK[0]), true)
    _check("tree: leaf", tree.has(_chips.LEAF[0]), true)
    var well: Array = _chips.palette("Well by the Mill")
    _check("well: stone", well.has(_chips.STONE[0]), true)
    _check("well: wood", well.has(_chips.WOOD[0]), true)
    _check("fence: wood only", _chips.palette("North fence"), _chips.WOOD)
    _check("unknown: wood", _chips.palette(""), _chips.WOOD)
    _done()


func _test_burst_throws_chips() -> void:
    var node = _chips.new()
    node.burst(_chips.WOOD, Vector2(-1, 0))
    _check("burst: COUNT chips", node._chips.size(), _chips.COUNT)
    var all_up := true
    for chip in node._chips:
        if (chip["vel"] as Vector2).y >= 0.0:
            all_up = false
    _check("burst: every chip starts upward", all_up, true)
    node.burst([], Vector2(-1, 0))
    _check("burst: no colours adds nothing", node._chips.size(), _chips.COUNT)
    node.free()
    _done()


const BODY := "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png"


## world.gd end to end. A repairer drawn before the object it works keeps its
## stand and throws no chips, then turns and steps in when the object appears;
## a capped step short of a distant object throws no chips, one that reaches
## does; the end of the work steps back to the exact stand, and a return cut
## short by new work still steps from the original stand.
func _test_world_wiring() -> void:
    var world = load("res://scripts/world.gd").new()
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
    var base := Vector2(-64, -82)
    doll.position = base
    var npc := Node2D.new()
    npc.add_child(doll)
    root.add_child(npc)
    world.placed_npcs["n1"] = npc
    # Frozen: only _strike() reaches the strike frame, so chips are counted exactly.
    doll.speed_scale = 0.0
    npc.set_meta("facing", "south")
    npc.set_meta("source_activity_kind", "repair")
    npc.set_meta("source_activity_object_id", "t1")

    world._apply_activity_animation(npc)
    _check("no target yet: keeps the walk's facing", doll.animation, &"south_chop")
    _check("no target yet: no step", doll.has_meta("work_base"), false)
    npc.set_meta("facing", "north")
    world._apply_activity_animation(npc)
    _check("no target, walked north: a side swing, not the north chop", doll.animation, &"east_chop")
    _strike(doll)
    _check("no target yet: no chips", _chip_count(npc), 0)

    # A 40x40 sprite drawn only in its bottom-right 10x10, anchored at
    # (0.5, 0.85): at (140, 0) the drawn part is (150,-4)-(160,6).
    var img := Image.create(40, 40, false, Image.FORMAT_RGBA8)
    img.fill_rect(Rect2i(30, 30, 10, 10), Color.WHITE)
    # Placed through the real render path, which re-aims whoever works it.
    var catalog: Node = root.get_node("Catalog")
    catalog.sheet_cache["test://work-target"] = ImageTexture.create_from_image(img)
    catalog.assets["test-work-target"] = {"anchor_x": 0.5, "anchor_y": 0.85, "render_scale": 1.0,
        "states": [{"state": "default", "sheet": "test://work-target", "src_x": 0, "src_y": 0, "src_w": 40, "src_h": 40}]}
    world.objects_node = Node2D.new()
    world._place_object({"id": "t1", "asset_id": "test-work-target", "x": 140.0, "y": 0.0})
    var target: Node2D = world.placed_objects.get("t1", null)
    _check("object placed", target != null, true)
    _check("visible rect: cut to the drawn pixels", world.object_visible_rect(target), Rect2(150, -4, 10, 10))
    _check("object appears: turned east", npc.get_meta("facing"), "east")
    _check("object appears: chopping east", doll.animation, &"east_chop")
    _check("stand kept for the step back", doll.get_meta("work_base", Vector2.INF), base)
    await create_timer(0.35).timeout
    _check("far object: step capped", doll.position.is_equal_approx(base + Vector2(91, 24).limit_length(48)), true)
    _strike(doll)
    _check("far object: the capped swing falls short, no chips", _chip_count(npc), 0)

    # Drawn part now (70,-4)-(80,6): the east strike (63,-24) steps (11,24).
    target.position = Vector2(60, 0)
    world._reaim_work_on("t1")
    await create_timer(0.35).timeout
    var near := base + Vector2(11, 24)
    _check("near object: stepped all the way", doll.position.is_equal_approx(near), true)
    _strike(doll)
    _check("near object: the blow throws chips", _chip_count(npc), 1)

    npc.set_meta("source_activity_kind", "")
    world._apply_activity_animation(npc)
    _check("work done: idle", doll.animation, &"east_idle")
    await create_timer(0.35).timeout
    _check("work done: back on the stand", doll.position.is_equal_approx(base), true)
    _check("work done: stand forgotten", doll.has_meta("work_base"), false)

    # Step in, start back, and take the work up again halfway home: the step
    # is measured from the original stand, not from where the sprite was.
    npc.set_meta("source_activity_kind", "repair")
    world._apply_activity_animation(npc)
    await create_timer(0.35).timeout
    npc.set_meta("source_activity_kind", "")
    world._apply_activity_animation(npc)
    await create_timer(0.1).timeout
    npc.set_meta("source_activity_kind", "repair")
    world._apply_activity_animation(npc)
    _check("cut-short return: stand unchanged", doll.get_meta("work_base", Vector2.INF), base)
    await create_timer(0.35).timeout
    _check("cut-short return: no double step", doll.position.is_equal_approx(near), true)

    # A sprite swap while stepped in (the real _swap_npc_sprite path): the new
    # sprite's stand is its own anchor, nothing stale rides over from the old
    # one, and it steps in exactly once.
    world._npc_sheets = sheets
    world._swap_npc_sprite("n1", {"id": "s2", "name": "Farmer", "sheet": BODY, "rig": "farmer_base",
        "frame_width": 64, "frame_height": 64, "render_scale": 2.0,
        "layers": [{"sheet": BODY, "ramps": {}}], "animations": []})
    var swapped: AnimatedSprite2D = npc.get_node("CharacterSprite")
    _check("swap: a new sprite", swapped != doll, true)
    var swap_base := Vector2(-64.0, -128.0 * FarmerDoll.ANCHOR.y)
    _check("swap: stand is the new sprite's anchor", (swapped.get_meta("work_base", Vector2.INF) as Vector2).is_equal_approx(swap_base), true)
    _check("swap: still chopping east", swapped.animation, &"east_chop")
    await create_timer(0.35).timeout
    _check("swap: stepped in once", swapped.position.is_equal_approx(swap_base + Vector2(11, 24)), true)

    npc.queue_free()
    world.objects_node.free()
    catalog.assets.erase("test-work-target")
    catalog.sheet_cache.erase("test://work-target")
    world.free()
    _done()


## Run the chop to its strike frame.
func _strike(doll: AnimatedSprite2D) -> void:
    doll.frame = 1
    doll.frame = _aim.STRIKE_FRAME


func _chip_count(npc: Node2D) -> int:
    var n := 0
    for child in npc.get_children():
        if child.get_script() == _chips and not child.is_queued_for_deletion():
            n += 1
    return n


## A damaged business: the swing's mark is its debris overlay (attached to the
## building, so drawn relative to it), not the building's nearest edge; the
## overlay arriving after the worker re-aims them; chips fly as branches for
## storm debris and only when the blow lands on the debris.
func _test_debris_is_the_mark() -> void:
    var world = load("res://scripts/world.gd").new()
    var catalog: Node = root.get_node("Catalog")
    var solid := func(w: int, h: int) -> ImageTexture:
        var img := Image.create(w, h, false, Image.FORMAT_RGBA8)
        img.fill(Color.WHITE)
        return ImageTexture.create_from_image(img)
    catalog.sheet_cache["test://biz"] = solid.call(200, 100)
    catalog.sheet_cache["test://debris"] = solid.call(20, 20)
    catalog.assets["test-biz"] = {"anchor_x": 0.5, "anchor_y": 0.85, "render_scale": 1.0,
        "states": [{"state": "default", "sheet": "test://biz", "src_x": 0, "src_y": 0, "src_w": 200, "src_h": 100}]}
    catalog.assets["test-debris"] = {"anchor_x": 0.5, "anchor_y": 0.85, "render_scale": 1.0,
        "states": [{"state": "storm", "sheet": "test://debris", "src_x": 0, "src_y": 0, "src_w": 20, "src_h": 20}]}
    world.objects_node = Node2D.new()

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
    var base := Vector2(-64, -82)
    doll.position = base
    doll.speed_scale = 0.0
    var npc := Node2D.new()
    npc.position = Vector2(220, 30)
    npc.add_child(doll)
    root.add_child(npc)
    world.placed_npcs["n1"] = npc
    npc.set_meta("facing", "south")
    npc.set_meta("source_activity_kind", "repair")
    npc.set_meta("source_activity_object_id", "biz")

    # The building: (200,-85)-(400,15). The worker below its front edge.
    world._place_object({"id": "biz", "asset_id": "test-biz", "x": 300.0, "y": 0.0})
    world._apply_activity_animation(npc)
    _check("no debris yet: the building is the mark", world._work_target(npc), world.placed_objects["biz"])

    # The debris, attached at the building's anchor: drawn at (290,-17)-(310,3).
    world._place_object({"id": "deb", "asset_id": "test-debris", "x": 300.0, "y": 0.0,
        "attached_to": "biz", "tags": ["debris"], "current_state": "storm"})
    var debris: Node2D = world.placed_objects["deb"]
    _check("debris: drawn as the building's child", debris.get_parent(), world.placed_objects["biz"])
    _check("debris: map rect adds the building's position", world.object_map_rect(debris), Rect2(290, -17, 20, 20))
    _check("debris arrives: it is the mark", world._work_target(npc), debris)
    _check("debris arrives: faces it", npc.get_meta("facing"), "east")
    await create_timer(0.35).timeout
    # East strike from (220,30) lands at (283,6); the debris inset is (294..306, -13..-1).
    _check("debris arrives: stepped onto it", doll.position.is_equal_approx(base + Vector2(11, -7)), true)
    _strike(doll)
    var chips: Array = []
    for child in npc.get_children():
        if child.get_script() == _chips:
            chips.append(child)
    _check("blow on the debris: chips", chips.size(), 1)
    var has_leaf := false
    for chip in chips[0]._chips if chips.size() > 0 else []:
        if _chips.LEAF.has(chip["color"]) or _chips.BARK.has(chip["color"]):
            has_leaf = true
    _check("storm debris: branch chips", has_leaf, true)
    _check("worn debris reads as boards", world._work_target_name(_named_debris("worn")), "boards")
    _check("storm debris reads as branch", world._work_target_name(_named_debris("storm")), "branch")

    npc.queue_free()
    world.objects_node.free()
    for key in ["test-biz", "test-debris"]:
        catalog.assets.erase(key)
    for key in ["test://biz", "test://debris"]:
        catalog.sheet_cache.erase(key)
    world.free()
    _done()


func _named_debris(state: String) -> Node2D:
    var n := Node2D.new()
    n.set_meta("tags", ["debris"])
    n.set_meta("current_state", state)
    n.queue_free()
    return n


## LLM-747: whatever side the work is on, the aim is never the north chop.
func _test_never_the_north_chop() -> void:
    var north_picks := 0
    for gx in range(-4, 5):
        for gy in range(-4, 5):
            var aim: Dictionary = _aim.aim(Vector2.ZERO, Rect2(gx * 40 - 10, gy * 40 - 10, 20, 20), SCALE, MAX_STEP)
            if aim.get("facing", "") == "north":
                north_picks += 1
    _check("no target position picks the north chop", north_picks, 0)
    _done()
