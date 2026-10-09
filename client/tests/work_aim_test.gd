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
    "_test_inside_the_box_faces_its_centre",
    "_test_no_target_no_aim",
    "_test_strike_point",
    "_test_chip_palette",
    "_test_burst_throws_chips",
    "_test_world_wiring",
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
        call(t)
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
    _check("work above: face north", north.get("facing"), "north")
    _check("work above: step up to it", north.get("step"), Vector2(0, -29))
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
    _check("inside: face the centre", inside.get("facing"), "north")
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


## world.gd end to end: the target's visible rect is cut to its drawn pixels,
## a repairer with the work to the east turns east and steps in, a chop's
## strike frame throws chips, and the end of the work steps back and idles.
func _test_world_wiring() -> void:
    var world = load("res://scripts/world.gd").new()
    # A 40x40 sprite drawn only in its bottom-right 10x10, anchored at
    # (0.5, 0.85): hit rect (80,-34)-(120,6), drawn part (110,-4)-(120,6).
    var img := Image.create(40, 40, false, Image.FORMAT_RGBA8)
    img.fill_rect(Rect2i(30, 30, 10, 10), Color.WHITE)
    var target := Node2D.new()
    target.position = Vector2(100, 0)
    var tex_sprite := Sprite2D.new()
    tex_sprite.texture = ImageTexture.create_from_image(img)
    target.add_child(tex_sprite)
    world.placed_objects["t1"] = target
    _check("visible rect: cut to the drawn pixels", world.object_visible_rect(target), Rect2(110, -4, 10, 10))

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
    doll.position = Vector2(-64, -82)
    var npc := Node2D.new()
    npc.add_child(doll)
    root.add_child(npc)
    npc.set_meta("facing", "south")
    npc.set_meta("source_activity_kind", "repair")
    npc.set_meta("source_activity_object_id", "t1")
    world._apply_activity_animation(npc)
    _check("work to the east: turned east", npc.get_meta("facing"), "east")
    _check("work to the east: chopping east", doll.animation, &"east_chop")
    _check("stand position kept for the step back", doll.get_meta("work_base", Vector2.INF), Vector2(-64, -82))

    doll.frame = 1
    doll.frame = _aim.STRIKE_FRAME
    var chips := 0
    for child in npc.get_children():
        if child.get_script() == _chips:
            chips += 1
    _check("strike frame: chips thrown", chips, 1)

    npc.set_meta("source_activity_kind", "")
    world._apply_activity_animation(npc)
    _check("work done: idle", doll.animation, &"east_idle")
    npc.queue_free()
    target.free()
    world.free()
    _done()
