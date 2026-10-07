extends SceneTree

## Headless harness for the LLM-640 arrival ease: instead of snapping an NPC to
## the authoritative endpoint when npc_arrived lands mid-lerp, the client walks
## the remainder as a "finishing" leg and idles when it parks. Covers the ease
## decision matrix (event_client._arrival_should_ease) and the finish_idle
## completion contract in world._tick_npc_walk — including the non-regression
## that an ORDINARY walk past its end still waits for npc_arrived to clean up.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/arrival_ease_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## Both scripts are instantiated off-tree via .new() so _ready() never fires.
## The ease tests touch no autoloads (the walking meta the tick reads is
## constructed directly, the way _on_npc_arrived's ease branch builds it); the
## arrival tests drive _on_npc_arrived itself, seeding the Catalog autoload with
## a synthetic asset, and cover the inside render it applies after the snap.

const TESTS := [
    "_test_ease_decision_matrix",
    "_test_finish_walk_lerps_then_idles",
    "_test_ordinary_walk_still_waits_for_arrival",
    "_test_arrival_into_stall_lands_on_stand_point",
    "_test_arrival_into_opaque_building_stays_hidden",
    "_test_walking_out_of_stall_is_visible",
]

const STALL_ASSET := "test-arrival-stall-asset"
const HOUSE_ASSET := "test-arrival-house-asset"
const STRUCTURE_ID := "test-arrival-structure"
const NPC_ID := "test-arrival-npc"

var _events = null
var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _events = load("res://scripts/event_client.gd").new()
    _world = load("res://scripts/world.gd").new()
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    _events.free()
    _world.free()
    print("\n[arrival_ease_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[arrival_ease_test] ALL PASS")
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


## A container the walk tick can drive: Node2D with the named CharacterSprite
## child play_npc_animation resolves (empty frames — the has_animation guard
## keeps it inert, which is all these tests need).
func _make_container(pos: Vector2) -> Node2D:
    var c := Node2D.new()
    c.position = pos
    var sprite := AnimatedSprite2D.new()
    sprite.name = "CharacterSprite"
    sprite.sprite_frames = SpriteFrames.new()
    c.add_child(sprite)
    return c


func _test_ease_decision_matrix() -> void:
    var c := _make_container(Vector2(100, 100))
    _check("inside arrival never eases", _events._arrival_should_ease(c, Vector2(150, 100), true), false)
    _check("already-there (1px) snaps", _events._arrival_should_ease(c, Vector2(101, 100), false), false)
    _check("half-tile remainder eases", _events._arrival_should_ease(c, Vector2(116, 100), false), true)
    _check("three-tile remainder eases", _events._arrival_should_ease(c, Vector2(196, 100), false), true)
    _check("huge drift (200px) snaps", _events._arrival_should_ease(c, Vector2(300, 100), false), false)
    c.free()
    _done()


## A finish_idle walk lerps toward the endpoint mid-flight, then parks, clears
## its own meta and stays parked — the cleanup npc_arrived would otherwise do.
func _test_finish_walk_lerps_then_idles() -> void:
    var c := _make_container(Vector2(0, 0))
    # Deterministic clock: the tick reads the _walk_clock_override_s seam, so
    # the interpolation point is exact instead of racing the wall clock
    # (code_review, LLM-640). Walk starts at t=100; 64px leg at 32px/s.
    c.set_meta("walking", {
        "start_pos": Vector2(0, 0),
        "path": [Vector2(64, 0)],
        "speed": 32.0,
        "started_at_s": 100.0,
        "attempt_id": 7,
        "finish_idle": true,
        "finish_facing": "north",
    })
    c.set_meta("facing", "east")
    _world._walk_clock_override_s = 101.0
    _world._tick_npc_walk(c)
    _check("mid-flight position is the exact half-way point", c.position, Vector2(32, 0))
    _check("mid-flight meta kept", c.has_meta("walking"), true)

    _world._walk_clock_override_s = 103.0
    _world._tick_npc_walk(c)
    _check("parked on the endpoint", c.position, Vector2(64, 0))
    _check("finish walk cleaned its own meta", c.has_meta("walking"), false)
    _check("final idle applied the arrival's authoritative facing", String(c.get_meta("facing", "")), "north")
    _world._walk_clock_override_s = -1.0
    c.free()
    _done()


## Non-regression: an ordinary walk (no finish_idle) that runs past its end
## parks but KEEPS its meta — npc_arrived owns the cleanup, exactly as before.
func _test_ordinary_walk_still_waits_for_arrival() -> void:
    var c := _make_container(Vector2(0, 0))
    _world._walk_clock_override_s = 110.0
    c.set_meta("walking", {
        "start_pos": Vector2(0, 0),
        "path": [Vector2(64, 0)],
        "speed": 32.0,
        "started_at_s": 100.0,
        "attempt_id": 8,
    })
    c.set_meta("facing", "east")
    _world._tick_npc_walk(c)
    _check("ordinary walk parked on the endpoint", c.position, Vector2(64, 0))
    _check("ordinary walk meta kept for npc_arrived", c.has_meta("walking"), true)
    _world._walk_clock_override_s = -1.0
    c.free()
    _done()


## Drive _on_npc_arrived end to end: an NPC container registered on the world,
## a structure placed with its anchor at tile (10, 10), and its asset in the
## Catalog autoload (reached through the tree root — the compile-time global
## name is not bound in a --script run). The arrival lands on the door tile,
## anchor tile + (1, 1). Returns the container; _teardown_arrival undoes all of it.
func _arrive(start_pos: Vector2, asset_id: String, asset: Dictionary, structure_id: String) -> Node2D:
    var catalog: Node = root.get_node("Catalog")
    catalog.assets[asset_id] = asset
    var structure := Node2D.new()
    structure.position = Vector2(320, 320)
    structure.set_meta("asset_id", asset_id)
    _world.placed_objects[STRUCTURE_ID] = structure
    var c := _make_container(start_pos)
    _world.placed_npcs[NPC_ID] = c
    _events.world = _world
    var api: Node = root.get_node("VillageApi")
    _events._on_npc_arrived({
        "id": NPC_ID,
        "x": api.pad_x + 11,
        "y": api.pad_y + 11,
        "structure_id": structure_id,
    })
    return c


func _teardown_arrival(asset_id: String) -> void:
    root.get_node("Catalog").assets.erase(asset_id)
    _world.placed_objects[STRUCTURE_ID].free()
    _world.placed_objects.erase(STRUCTURE_ID)
    _world.placed_npcs[NPC_ID].free()
    _world.placed_npcs.erase(NPC_ID)
    _events.world = null


## The live bug: a keeper or hired hand who walks into a see-through stall must
## end behind the counter (anchor + stand_offset), not on the door tile the
## arrival snap writes. Both a near start (the snap re-seats in place) and a far
## one (the snap's over-a-tile ghost toggle) must land on the stand point.
func _test_arrival_into_stall_lands_on_stand_point() -> void:
    var stall := {"visible_when_inside": true, "stand_offset_x": -1, "stand_offset_y": -1}
    # Anchor tile (10, 10) + (-1, -1) = tile (9, 9), drawn at its center.
    var stand_point := Vector2(9 * 32 + 16, 9 * 32 + 16)
    var door := Vector2(11 * 32 + 16, 11 * 32 + 16)
    for start in [door + Vector2(8, 0), door + Vector2(96, 0)]:
        var c := _arrive(start, STALL_ASSET, stall, STRUCTURE_ID)
        _check("stall arrival from %s lands on the stand point" % start, c.position, stand_point)
        _check("stall arrival from %s stays visible" % start, c.visible, true)
        _check("stall arrival from %s records inside" % start, c.get_meta("inside", false), true)
        _check("stall arrival from %s leaves no walk" % start, c.has_meta("walking"), false)
        _teardown_arrival(STALL_ASSET)
    _done()


## An opaque building hides its occupant. A start more than a tile from the
## door makes the snap force visible=true for its ghost toggle; the hide must
## still win.
func _test_arrival_into_opaque_building_stays_hidden() -> void:
    var house := {"visible_when_inside": false}
    var door := Vector2(11 * 32 + 16, 11 * 32 + 16)
    for start in [door + Vector2(8, 0), door + Vector2(96, 0)]:
        var c := _arrive(start, HOUSE_ASSET, house, STRUCTURE_ID)
        _check("house arrival from %s is hidden" % start, c.visible, false)
        _check("house arrival from %s sits on the door tile" % start, c.position, door)
        _teardown_arrival(HOUSE_ASSET)
    _done()


## Non-regression: an outdoor arrival is visible, heads for its own tile, and
## gets no stand-offset reposition.
func _test_walking_out_of_stall_is_visible() -> void:
    var door := Vector2(11 * 32 + 16, 11 * 32 + 16)
    var stall := {"visible_when_inside": true, "stand_offset_x": -1, "stand_offset_y": -1}
    # Arrive inside first, then walk back out: the outdoor arrival must undo
    # both the stand-offset reposition and any hide.
    var c := _arrive(door, STALL_ASSET, stall, STRUCTURE_ID)
    c.visible = false
    var api: Node = root.get_node("VillageApi")
    _events._on_npc_arrived({"id": NPC_ID, "x": api.pad_x + 11, "y": api.pad_y + 11, "structure_id": ""})
    _check("outdoor arrival is visible", c.visible, true)
    # From the stand point the door is ~90px off — inside the ease band — so the
    # sprite walks the remainder out to its tile rather than snapping.
    var walk: Dictionary = c.get_meta("walking", {})
    _check("outdoor arrival eases out toward its tile", walk.get("path", []), [door])
    _check("outdoor arrival records outside", c.get_meta("inside", true), false)
    _teardown_arrival(STALL_ASSET)
    _done()
