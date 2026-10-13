extends SceneTree

## Headless regression for LLM-768: an agent row's room_square reaches the
## villager. VillageApi.normalize_agent is a whitelist — a field it does not
## copy never reaches world._render_npc — and room_square was missed, so after
## the deploy every room window counted everyone inside as "and N more" until
## their next inside change. Runs the real seam: a raw /api/village/agents row
## through normalize_agent, then _render_npc, then reads the container meta.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/agent_room_square_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## world.gd is instantiated off-tree via .new() so _ready()/@onready never fire.

const TESTS := [
    "_test_room_square_survives_the_seam",
]

var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _world = load("res://scripts/world.gd").new()
    _world._npc_sheets = {SHEET: ImageTexture.create_from_image(Image.create(128, 128, false, Image.FORMAT_RGBA8))}
    _world.objects_node = Node2D.new()
    _check_test_list()
    _run_all()
    _world.objects_node.free()
    _world.free()
    _check_all_tests_ran()
    print("\n[agent_room_square_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[agent_room_square_test] ALL PASS")
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


const SHEET := "/tilesets/test/villager.png"


## A one-sheet sprite in the render subset, its sheet seeded so it builds.
func _sprite() -> Dictionary:
    var anims := []
    var row := 0
    for d in ["south", "east", "north", "west"]:
        anims.append({"direction": d, "animation": "idle", "row_index": row, "frame_count": 1})
        row += 1
    return {"id": "p", "sheet": SHEET, "frame_width": 32, "frame_height": 32, "render_scale": 2.0, "animations": anims}


## The VillageApi autoload (its global name does not compile in a --script run).
func _api() -> Node:
    return root.get_node("VillageApi")


func _row(id: String, inside: String, square) -> Dictionary:
    var row := {"id": id, "display_name": id, "kind": "npc_stateful", "state": "idle", "x": 10, "y": 12,
        "facing": "south", "inside_structure_id": inside, "sprite": _sprite()}
    if square != null:
        row["room_square"] = square
    return row


func _test_room_square_survives_the_seam() -> void:
    var tavern := "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9"
    var row := _row("john", tavern, [7, 5])
    var normalized: Dictionary = _api().normalize_agent(JSON.parse_string(JSON.stringify(row)))
    _check("normalize_agent forwards room_square", InteriorRooms.square_of(normalized.get("room_square", null)), Vector2i(7, 5))
    _world._render_npc(normalized)
    var john: Node2D = _world.placed_npcs.get("john", null)
    _check("the villager is placed", john != null, true)
    if john != null:
        _check("and carries the square", john.get_meta("room_square") if john.has_meta("room_square") else null, [7, 5])
    # A row without one (outdoors, asleep) leaves none.
    _world._render_npc(_api().normalize_agent(_row("ruth", "", null)))
    var ruth: Node2D = _world.placed_npcs.get("ruth", null)
    _check("no square on the row: none on the villager", ruth != null and not ruth.has_meta("room_square"), true)
    _done()
