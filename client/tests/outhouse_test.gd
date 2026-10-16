extends SceneTree

## Headless regression for LLM-806 — a player in an outhouse, and sound heard
## by distance.
##
## What this file exists to catch:
##
##  1. The falloff: full gain near, quieter further off, silent at and past a
##     sound's reach.
##  2. play_at: a sound beside the listener plays at its row's gain, one across
##     the village picks no voice, one indoors is quieter.
##  3. The stay: the player is hidden for the stay and shown again after, an
##     inside-change frame during the stay does not show them, the end of an
##     earlier stay does not show a player who went back in, and the door is
##     heard on going in.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/outhouse_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## Under the headless Dummy audio driver Sound never starts a player, so these
## checks read the voice Sound picked and the gain it set (see sound_test.gd).
## world.gd is put in the tree for its timers; its _ready only sets api_base.

const TESTS := [
    "_test_distance_falloff",
    "_test_play_at_by_distance",
    "_test_outhouse_stay",
]

## A stay short enough to wait out, long enough to read between.
const STAY := 0.3

var _sound: Node = null
var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _sound = root.get_node_or_null("Sound")
    _check("harness — Sound autoload present", _sound != null)
    if _sound == null:
        quit(1)
        return
    _world = load("res://scripts/world.gd").new()
    var objects := Node2D.new()
    objects.name = "Objects"
    _world.add_child(objects)
    root.add_child(_world)
    _check_test_list()
    for t in TESTS:
        _current = t
        await call(t)
    _world.queue_free()
    _check_all_tests_ran()
    print("\n[outhouse_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[outhouse_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


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
    if not ok:
        _failures += 1
        print("  FAIL: ", label)


## How many one-shot sounds Sound picked a voice for since `before`.
func _plays_since(before: int) -> int:
    return posmod(_sound._next_voice - before, _sound.VOICES)


## The voice Sound picked last.
func _last_voice() -> AudioStreamPlayer:
    return _sound._voices[posmod(_sound._next_voice - 1, _sound.VOICES)]


# --- tests -----------------------------------------------------------------------

func _test_distance_falloff() -> void:
    var near: float = _sound.NEAR_TILES
    _check("beside — full gain", _sound.distance_db(0.0, 20.0) == 0.0)
    _check("at the near edge — full gain", _sound.distance_db(near, 20.0) == 0.0)
    var mid: float = _sound.distance_db(10.0, 20.0)
    var further: float = _sound.distance_db(15.0, 20.0)
    _check("past near — quieter", mid < 0.0)
    _check("further — quieter still", further < mid)
    _check("at reach — silent", is_inf(_sound.distance_db(20.0, 20.0)))
    _check("past reach — silent", is_inf(_sound.distance_db(40.0, 20.0)))
    for sound_name in ["fart", "outhouse_door", "cow_moo"]:
        var reach := float(_sound.SOUNDS[sound_name].get("reach", 0.0))
        _check("%s — reach past the near ring" % sound_name, reach > near)
    _done()


func _test_play_at_by_distance() -> void:
    var tile: float = _sound.TILE_PX
    var row_db := float(_sound.SOUNDS["fart"]["db"])
    _sound.set_indoors(false)

    var before: int = _sound._next_voice
    var length: float = _sound.play_at("fart", Vector2(tile, 0), Vector2.ZERO)
    _check("beside — a voice picked", _plays_since(before) == 1)
    _check("beside — the row's gain", is_equal_approx(_last_voice().volume_db, row_db))
    _check("beside — the length played", length > 0.0)

    _sound.play_at("fart", Vector2(tile * 12.0, 0), Vector2.ZERO)
    _check("across the green — quieter", _last_voice().volume_db < row_db)

    before = _sound._next_voice
    length = _sound.play_at("fart", Vector2(tile * 200.0, 0), Vector2.ZERO)
    _check("across the village — no voice", _plays_since(before) == 0)
    _check("across the village — nothing played", length == 0.0)

    _sound.set_indoors(true)
    _sound.play_at("fart", Vector2(tile, 0), Vector2.ZERO)
    _check("indoors — quieter", is_equal_approx(_last_voice().volume_db, row_db + _sound.WORLD_INDOOR_DB))
    _sound.set_indoors(false)
    _done()


func _test_outhouse_stay() -> void:
    var house := Node2D.new()
    _world.add_child(house)
    _world.placed_objects["oh"] = house
    var me := Node2D.new()
    me.position = Vector2(_sound.TILE_PX * 2.0, 0)
    _world.add_child(me)
    _world.placed_npcs["pc-me"] = me
    _world.pc_actor_id = "pc-me"
    var them := Node2D.new()
    _world.add_child(them)
    _world.placed_npcs["pc-them"] = them

    var before: int = _sound._next_voice
    _world.apply_outhouse_used({"object_id": "oh", "actor_id": "pc-them", "seconds": STAY})
    _check("going in — hidden", not them.visible)
    _check("going in — in the outhouse", _world.in_outhouse(them))
    _check("going in — the door heard", _plays_since(before) == 1)
    _check("the listener — still shown", me.visible)

    _world.apply_npc_inside_change({"id": "pc-them", "inside": false})
    _check("an inside-change frame — still hidden", not them.visible)

    await create_timer(STAY + 0.15).timeout
    _check("the stay over — shown", them.visible)
    _check("the stay over — out of the outhouse", not _world.in_outhouse(them))

    # Back in before an earlier stay ends: that end must not show them.
    _world.apply_outhouse_used({"object_id": "oh", "actor_id": "pc-them", "seconds": STAY})
    await create_timer(STAY * 0.5).timeout
    _world.apply_outhouse_used({"object_id": "oh", "actor_id": "pc-them", "seconds": STAY})
    before = _sound._next_voice
    await create_timer(STAY * 0.5 + 0.05).timeout
    _check("the first stay's end — still hidden for the second", not them.visible)
    _check("the first stay's end — no door heard", _plays_since(before) == 0)
    await create_timer(STAY).timeout
    _check("the second stay over — shown", them.visible)
    _check("the second stay over — the door heard once", _plays_since(before) == 1)

    # A broadcast for an outhouse this client has not placed changes nothing.
    _world.apply_outhouse_used({"object_id": "missing", "actor_id": "pc-them", "seconds": STAY})
    _check("unknown outhouse — still shown", them.visible)

    _world.placed_objects.erase("oh")
    _world.placed_npcs.erase("pc-me")
    _world.placed_npcs.erase("pc-them")
    _done()
