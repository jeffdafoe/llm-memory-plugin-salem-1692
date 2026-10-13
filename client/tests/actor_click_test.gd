extends SceneTree

## Headless harness for LLM-755: a click lands on a person only on their
## body, never on their sprite frame's empty margin, and never on the
## player's own character — so walk clicks near people walk.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/actor_click_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## Sheets are blank synthetic textures; world.gd is instantiated off-tree via
## .new() so _ready() never fires — the tooltip only uses its placed_npcs.

const TESTS := [
    "_test_body_not_frame",
    "_test_own_character_never_clicked",
    "_test_small_sprite_kept_in_its_frame",
]

const BODY := "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png"
const PACK := "/tilesets/mana-seed/npc/chef_v02.png"
const TILE := 32.0

var _world: Node2D = null
var _tip: CanvasLayer = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _world = load("res://scripts/world.gd").new()
    var sheets := {BODY: _blank(1024, 1024), PACK: _blank(128, 256)}
    for prop in FarmerRig.PROPS.values():
        sheets[prop["sheet"]] = _blank(512, 512)
    _world._npc_sheets = sheets
    _tip = CanvasLayer.new()
    _tip.set_script(load("res://scripts/actor_tooltip.gd"))
    root.add_child(_tip)
    _tip.world = _world
    _check_test_list()
    for t in TESTS:
        _current = t
        call(t)
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)
    _tip.free()
    for c in _world.placed_npcs.values():
        c.free()
    _world.free()
    print("\n[actor_click_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[actor_click_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _done() -> void:
    _completed[_current] = true


func _check_test_list() -> void:
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, TESTS.has(name), true)


func _blank(w: int, h: int) -> Texture2D:
    return ImageTexture.create_from_image(Image.create(w, h, false, Image.FORMAT_RGBA8))


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


## A person as world.gd places them: a container at the feet holding the sprite.
func _place(id: String, sprite: Dictionary, feet: Vector2) -> Node2D:
    var c := Node2D.new()
    c.position = feet
    c.add_child(_world._build_character_sprite(sprite))
    _world.placed_npcs[id] = c
    return c


func _clear() -> void:
    for c in _world.placed_npcs.values():
        c.free()
    _world.placed_npcs.clear()
    _tip.pc_actor_id = ""


## The canvas is not moved in this harness, so a screen point is a world point.
func _hit(at: Vector2) -> String:
    var node: Node2D = _tip._find_actor_at(at)
    if node == null:
        return ""
    for id in _world.placed_npcs:
        if _world.placed_npcs[id] == node:
            return str(id)
    return "?"


func _test_body_not_frame() -> void:
    _clear()
    var feet := Vector2(20 * TILE, 20 * TILE)
    _place("villager", _farmer(), feet)
    _check("a click on the body finds them", _hit(feet + Vector2(0, -30)), "villager")
    _check("a click at the feet finds them", _hit(feet + Vector2(0, -2)), "villager")
    # Inside the 64 x 64 frame drawn at 2x, outside the body: these walk.
    _check("a tile and a half to the side walks", _hit(feet + Vector2(1.5 * TILE, -20)), "")
    _check("over the head walks", _hit(feet + Vector2(0, -75)), "")
    _check("a tile below the feet walks", _hit(feet + Vector2(0, TILE)), "")
    var pack_feet := Vector2(30 * TILE, 20 * TILE)
    _place("chef", _pack(), pack_feet)
    _check("a pack sprite's body finds them", _hit(pack_feet + Vector2(0, -30)), "chef")
    _check("beside a pack sprite walks", _hit(pack_feet + Vector2(0.8 * TILE, -10)), "")
    _done()


func _test_own_character_never_clicked() -> void:
    _clear()
    var feet := Vector2(20 * TILE, 20 * TILE)
    _place("me", _farmer(), feet)
    _check("before the id is known, it is a person", _hit(feet + Vector2(0, -30)), "me")
    _tip.pc_actor_id = "me"
    _check("your own body walks", _hit(feet + Vector2(0, -30)), "")
    # Someone standing on top of you is still found.
    _place("visitor", _farmer(), feet + Vector2(4, 0))
    _check("someone at your side is found", _hit(feet + Vector2(4, -30)), "visitor")
    _done()


func _test_small_sprite_kept_in_its_frame() -> void:
    # A 16 x 16 frame drawn at 1x, its feet at the frame's bottom middle: the
    # body box is cut to the picture.
    var box: Rect2 = _tip.body_rect(Vector2(100, 100), Vector2(-8, -16), Vector2(16, 16), Vector2.ONE)
    _check("cut to the frame", box, Rect2(Vector2(92, 84), Vector2(16, 16)))
    var farmer: Rect2 = _tip.body_rect(Vector2.ZERO, Vector2(-64, -82), Vector2(64, 64), Vector2(2, 2))
    _check("a farmer at 2x: the body, 28 px above the feet to 2 below", farmer, Rect2(Vector2(-16, -56), Vector2(32, 60)))
    _done()
