extends SceneTree

## Headless regression harness for carried lantern lights (LLM-846).
##
## What this file exists to catch:
##
##  1. A lantern lit on /agents (a client that connects late) or by an
##     npc_lantern_changed frame puts a light on that actor; an "out" frame
##     takes it off.
##  2. The light goes out at lit_until on the engine's clock, with no frame.
##  3. Its strength follows the night: off by day, full at night.
##  4. It follows a re-rendered character, and a resync drops lanterns the
##     engine no longer reports.
##  5. engine_now_unix runs on from the last world read, not the local clock,
##     and before that read nothing goes out.
##  6. A frame that comes while an /agents read is on its way is not undone
##     by that read's older answer.
##  7. A freed container still in the lookup is treated as absent.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/lantern_lights_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const LanternLights = preload("res://scripts/lantern_lights.gd")

const TESTS := [
    "_test_agents_load_lights_a_lantern",
    "_test_frames_light_and_put_out",
    "_test_light_goes_out_at_lit_until",
    "_test_strength_follows_the_night",
    "_test_light_follows_a_replaced_character",
    "_test_resync_drops_a_lantern_no_longer_lit",
    "_test_a_freed_container_left_in_the_lookup",
    "_test_light_during_an_agents_read_survives_it",
    "_test_put_out_during_an_agents_read_survives_it",
    "_test_a_failed_agents_read_stops_keeping_frames",
    "_test_no_engine_clock_keeps_the_lantern",
    "_test_engine_clock_runs_from_the_last_read",
]

## 2026-11-03T21:00:00Z
const NOW := 1793739600.0
const LIT_ISO := "2026-11-03T22:30:00Z"
const LIT_UNIX := NOW + 5400.0

var _lights: Node = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

## The fields lantern_lights.gd reads off world.gd.
class StubWorld:
    extends Node2D
    var placed_npcs: Dictionary = {}
    var _phase_pos := 1.0
    var _gradient := GradientTexture2D.new()

    func _get_light_gradient() -> Texture2D:
        return _gradient

    static func _iso_to_unix(iso: String) -> float:
        if iso.length() < 19 or not iso.ends_with("Z"):
            return 0.0
        return float(Time.get_unix_time_from_datetime_string(iso))

    func add_npc(id: String) -> Node2D:
        var container := Node2D.new()
        add_child(container)
        placed_npcs[id] = container
        return container

func _initialize() -> void:
    _lights = Node.new()
    _lights.set_script(LanternLights)
    root.add_child(_lights)

func _process(_delta: float) -> bool:
    _check("harness — lights entered the tree", _lights.is_inside_tree())
    _lights.set_process(false)
    _check_test_list()
    _run_all()
    _lights.queue_free()
    _check_all_tests_ran()
    print("\n[lantern_lights_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[lantern_lights_test] ALL PASS")
    quit(1 if _failures > 0 else 0)
    return true

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

func _done() -> void:
    _completed[_current] = true

func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t))

func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t))
        _check("harness — %s exists" % t, has_method(t))
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name))

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if ok:
        print("  ok   " + label)
    else:
        _failures += 1
        print("  FAIL " + label)

# --- fixtures --------------------------------------------------------------------

func _setup_world() -> StubWorld:
    var stub := StubWorld.new()
    root.add_child(stub)
    _lights.world = stub
    _lights.lit_until.clear()
    _lights._lights.clear()
    _lights._loading = false
    _lights._during_load.clear()
    return stub

func _teardown_world(stub: StubWorld) -> void:
    _lights.world = null
    _lights.lit_until.clear()
    _lights._lights.clear()
    stub.free()

## The lights on a container that are not on their way out.
func _live_lights(container: Node2D) -> Array:
    return container.get_children().filter(func(n): return n is PointLight2D and not n.is_queued_for_deletion())

# --- tests -----------------------------------------------------------------------

func _test_agents_load_lights_a_lantern() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    var other := stub.add_npc("villager")
    _lights.load_agents([{"id": "pc", "lantern_lit_until": LIT_ISO}, {"id": "villager"}])
    _lights.refresh(NOW, 1.0)
    var on_pc := _live_lights(pc)
    _check("the lit actor carries one light", on_pc.size() == 1 and on_pc[0].enabled)
    _check("the light is the lantern's warm colour", on_pc.size() == 1 and on_pc[0].color.is_equal_approx(LanternLights.COLOR))
    _check("an actor with no lantern carries none", _live_lights(other).is_empty())
    _teardown_world(stub)
    _done()

func _test_frames_light_and_put_out() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO, "at": "2026-11-03T21:00:00Z"})
    _lights.refresh(NOW, 1.0)
    _check("a lit frame puts a light on", _live_lights(pc).size() == 1)
    _lights.apply_changed({"id": "pc", "at": "2026-11-03T21:10:00Z"})
    _lights.refresh(NOW + 600.0, 1.0)
    _check("an out frame takes it off", _live_lights(pc).is_empty())
    _check("and forgets the lantern", not _lights.lit_until.has("pc"))
    _teardown_world(stub)
    _done()

func _test_light_goes_out_at_lit_until() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO})
    _lights.refresh(LIT_UNIX - 1.0, 1.0)
    _check("a second before lit_until it still burns", _live_lights(pc).size() == 1)
    _lights.refresh(LIT_UNIX, 1.0)
    _check("at lit_until it is out", _live_lights(pc).is_empty())
    _teardown_world(stub)
    _done()

func _test_strength_follows_the_night() -> void:
    var stub := _setup_world()
    stub.add_npc("pc")
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO})
    _lights.refresh(NOW, 0.0)
    var light: PointLight2D = _lights.light_for("pc")
    _check("by day the light is off", light != null and not light.enabled)
    _lights.refresh(NOW, 1.0)
    var top := LanternLights.ENERGY * (1.0 + LanternLights.FLICKER)
    var bottom := LanternLights.ENERGY * (1.0 - LanternLights.FLICKER)
    _check("at night it is on at full strength, give or take the flicker",
        light.enabled and light.energy <= top + 0.001 and light.energy >= bottom - 0.001)
    _lights.refresh(NOW, 0.5)
    _check("half night, about half strength",
        light.energy <= top * 0.5 + 0.001 and light.energy >= bottom * 0.5 - 0.001)
    _teardown_world(stub)
    _done()

func _test_light_follows_a_replaced_character() -> void:
    var stub := _setup_world()
    var old_pc := stub.add_npc("pc")
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO})
    _lights.refresh(NOW, 1.0)
    var new_pc := stub.add_npc("pc")
    _lights.refresh(NOW, 1.0)
    _check("the replacement carries the light", _live_lights(new_pc).size() == 1)
    _check("the old node carries none", _live_lights(old_pc).is_empty())
    _teardown_world(stub)
    _done()

func _test_resync_drops_a_lantern_no_longer_lit() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO})
    _lights.refresh(NOW, 1.0)
    _lights.load_agents([{"id": "pc"}])
    _lights.refresh(NOW, 1.0)
    _check("a resync without the lantern takes the light off", _live_lights(pc).is_empty())
    _teardown_world(stub)
    _done()

func _test_a_freed_container_left_in_the_lookup() -> void:
    var stub := _setup_world()
    var old_pc := stub.add_npc("pc")
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO})
    _lights.refresh(NOW, 1.0)
    old_pc.free()
    _lights.refresh(NOW, 1.0)
    _check("a freed container drops its light from the map", _lights.light_for("pc") == null)
    var new_pc := stub.add_npc("pc")
    _lights.refresh(NOW, 1.0)
    _check("the replacement carries the light", _live_lights(new_pc).size() == 1)
    _teardown_world(stub)
    _done()

func _test_light_during_an_agents_read_survives_it() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    _lights.begin_agents_load()
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO})
    _lights.load_agents([{"id": "pc"}])
    _lights.refresh(NOW, 1.0)
    _check("a light from a frame outlives the older read", _live_lights(pc).size() == 1)
    _lights.load_agents([{"id": "pc"}])
    _lights.refresh(NOW, 1.0)
    _check("a later read with no frame in flight wins", _live_lights(pc).is_empty())
    _teardown_world(stub)
    _done()

func _test_put_out_during_an_agents_read_survives_it() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    _lights.begin_agents_load()
    _lights.apply_changed({"id": "pc"})
    _lights.load_agents([{"id": "pc", "lantern_lit_until": LIT_ISO}])
    _lights.refresh(NOW, 1.0)
    _check("a put-out frame outlives the older read", _live_lights(pc).is_empty())
    _teardown_world(stub)
    _done()

func _test_a_failed_agents_read_stops_keeping_frames() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    _lights.begin_agents_load()
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO})
    _lights.end_agents_load()
    _lights.refresh(NOW, 1.0)
    _check("the frame during the failed read still lit the lantern", _live_lights(pc).size() == 1)
    _lights.apply_changed({"id": "pc"})
    _lights.refresh(NOW, 1.0)
    _check("a later frame puts it out", _live_lights(pc).is_empty())
    _check("and nothing is kept after the failed read", _lights._during_load.is_empty() and not _lights._loading)
    _teardown_world(stub)
    _done()

func _test_no_engine_clock_keeps_the_lantern() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    _lights.apply_changed({"id": "pc", "lit_until": LIT_ISO})
    _lights.refresh(0.0, 1.0)
    _check("with no engine clock the lantern burns on", _live_lights(pc).size() == 1)
    _lights.refresh(LIT_UNIX - 60.0, 1.0)
    _check("the first clock still before lit_until keeps it", _live_lights(pc).size() == 1)
    _teardown_world(stub)
    _done()

func _test_engine_clock_runs_from_the_last_read() -> void:
    var world: Node2D = load("res://scripts/world.gd").new()
    _check("before a read there is no engine clock", world.engine_now_unix() == 0.0)
    world._engine_clock_unix = NOW
    world._engine_clock_read_ms = Time.get_ticks_msec()
    var now: float = world.engine_now_unix()
    _check("after a read it runs from the engine's time", now >= NOW and now < NOW + 5.0)
    world.free()
    _done()
