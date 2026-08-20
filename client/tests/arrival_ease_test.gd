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
## Both scripts are instantiated off-tree via .new() so _ready() never fires;
## the functions under test touch no autoloads (the walking meta the tick reads
## is constructed directly, the way _on_npc_arrived's ease branch builds it).

const TESTS := [
    "_test_ease_decision_matrix",
    "_test_finish_walk_lerps_then_idles",
    "_test_ordinary_walk_still_waits_for_arrival",
]

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
