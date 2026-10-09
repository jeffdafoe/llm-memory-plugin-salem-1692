extends SceneTree

## Headless regression harness for the above-head sleep "Zzz" marker in world.gd
## (_apply_dormant_visual). Covers LLM-449: the queue_free lifecycle race (the marker
## is a persistent, visibility-toggled node). Also pins that a source activity draws
## no above-head marker — the work animation and the hover line show it.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/marker_transitions_test.gd
## Exits 0 when every check passes, 1 if any check fails (or a script error aborts).
##
## world.gd is instantiated off-tree via .new() so _ready()/@onready never fire: the
## marker methods only touch the container passed in, none of the network / terrain /
## autoload state _ready() would otherwise set up.

const ZZZ := "ZzzMarker"

## Every test, by name. _run_all dispatches through this and _check_all_tests_ran
## asserts each one reached its end — see _done().
const TESTS := [
    "_test_dormant_toggle",
    "_test_same_frame_wake_sleep_no_duplicate",
    "_test_repeated_dormant_no_duplicate",
    "_test_position_self_heal_marker_before_sprite",
    "_test_activity_draws_no_marker",
]

var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

func _initialize() -> void:
    _world = load("res://scripts/world.gd").new()
    _check_test_list()
    _run_all()
    _world.free()
    _check_all_tests_ran()
    print("\n[marker_transitions_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[marker_transitions_test] ALL PASS")
    quit(1 if _failures > 0 else 0)

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

## LLM-480. A GDScript runtime error aborts ONLY the function it happens in — the
## caller resumes at the very next statement and the process still exits 0 — so an
## aborted test is invisible both to _run_all and to the failure tally, and the suite
## prints ALL PASS having silently skipped it. Every test therefore calls _done() as
## its last statement (and before any early return); a name missing here ran partially.
func _done() -> void:
    _completed[_current] = true

func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t))

## TESTS is the dispatch list, so an unregistered test is simply never called and
## _check_all_tests_ran would never miss it — the same silent coverage loss this file
## exists to prevent, one level up. Enumerating the script's own _test_ methods closes
## it: adding a case without listing it fails here. A duplicate entry is caught too, as
## it runs twice but leaves only one completion mark. Runs BEFORE _run_all so a bad name
## is a normal harness failure rather than something call() hits first.
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

# --- fixtures -------------------------------------------------------------------

## A fresh NPC container with an AnimatedSprite2D child, as a placed NPC node has.
func _make_container() -> Node2D:
    var c := Node2D.new()
    var spr := AnimatedSprite2D.new()
    spr.name = "Sprite"
    c.add_child(spr)
    return c

## Mirror the dormancy feed (dormancy delta / initial render). _apply_dormant_visual
## writes the "dormant" meta itself, so nothing extra to set here.
func _set_dormant(c: Node2D, token: String) -> void:
    _world._apply_dormant_visual(c, token)

# --- assertions -----------------------------------------------------------------

func _vis(c: Node2D, marker_name: String) -> bool:
    var m: Label = c.get_node_or_null(marker_name)
    return m != null and m.visible

func _count(c: Node2D, marker_name: String) -> int:
    var n := 0
    for child in c.get_children():
        if child.name == marker_name:
            n += 1
    return n

func _labels(c: Node2D) -> int:
    var n := 0
    for child in c.get_children():
        if child is Label:
            n += 1
    return n

func _sprite_modulate(c: Node2D) -> Color:
    var spr: AnimatedSprite2D = _world._npc_sprite(c)
    return spr.modulate if spr != null else Color.WHITE

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)

## Assert the Zzz visibility, and that the marker is a single persistent node.
func _expect_zzz(c: Node2D, shown: bool, ctx: String) -> void:
    _check("%s — zzz visible is %s" % [ctx, shown], _vis(c, ZZZ) == shown)
    _check("%s — ZzzMarker not duplicated" % ctx, _count(c, ZZZ) <= 1)
    if shown:
        _check("%s — exactly one ZzzMarker node" % ctx, _count(c, ZZZ) == 1)

## Assert the sprite dim state — dormancy owns it (DORMANT_DIM while dormant, WHITE
## otherwise); the activity path must never touch modulation.
func _expect_dim(c: Node2D, dimmed: bool, ctx: String) -> void:
    var expected: Color = _world.DORMANT_DIM if dimmed else Color.WHITE
    _check("%s — sprite modulate %s" % [ctx, "dimmed" if dimmed else "normal"], _sprite_modulate(c) == expected)

# --- cases ----------------------------------------------------------------------

func _test_dormant_toggle() -> void:
    var c := _make_container()
    _set_dormant(c, "sleeping")
    _expect_zzz(c, true, "dormant_toggle: after sleep")
    _expect_dim(c, true, "dormant_toggle: after sleep")
    _set_dormant(c, "")
    _expect_zzz(c, false, "dormant_toggle: after wake")
    _expect_dim(c, false, "dormant_toggle: after wake")
    c.free()
    _done()

## LLM-449 core: a same-frame clear -> set (wake then immediately sleep, repeated) must
## reuse the one persistent node — never queue_free-and-recreate, which could reuse the
## queued-for-deletion node or leave two ZzzMarker children.
func _test_same_frame_wake_sleep_no_duplicate() -> void:
    var c := _make_container()
    _set_dormant(c, "sleeping")
    _set_dormant(c, "")
    _set_dormant(c, "sleeping")
    _set_dormant(c, "")
    _set_dormant(c, "sleeping")
    _expect_zzz(c, true, "same_frame_wake_sleep")
    c.free()
    _done()

func _test_repeated_dormant_no_duplicate() -> void:
    var c := _make_container()
    _set_dormant(c, "sleeping")
    _set_dormant(c, "sleeping")
    _set_dormant(c, "resting")
    _expect_zzz(c, true, "repeated_dormant")
    c.free()
    _done()

## A marker first created before the sprite frames resolve uses the fallback position;
## a later dormant apply repositions it off the now-present sprite. Assert the exact
## invariant — position equals _zzz_marker_position for the current sprite — rather than
## only that it changed.
func _test_position_self_heal_marker_before_sprite() -> void:
    var c := Node2D.new()
    _set_dormant(c, "sleeping")
    var m: Label = c.get_node_or_null(ZZZ)
    _check("position_self_heal — marker created without a sprite", m != null)
    if m == null:
        c.free()
        _done()
        return
    var fallback_pos: Vector2 = m.position
    _check("position_self_heal — fallback position with no sprite", fallback_pos == _world._zzz_marker_position(null))
    var spr := AnimatedSprite2D.new()
    spr.name = "Sprite"
    spr.position = Vector2(40, 40)
    c.add_child(spr)
    _set_dormant(c, "sleeping")
    _check("position_self_heal — repositioned off the sprite", m.position == _world._zzz_marker_position(spr))
    _check("position_self_heal — position actually changed from fallback", m.position != fallback_pos)
    _expect_zzz(c, true, "position_self_heal")
    c.free()
    _done()

## The source-activity feed (apply_npc_source_activity_changed) stores the kind for the
## hover line and draws nothing above the head, for every kind, and leaves a dormant
## actor's Zzz and dim alone.
func _test_activity_draws_no_marker() -> void:
    var c := _make_container()
    _world.placed_npcs["worker"] = c
    for kind in ["repair", "stoke", "harvest"]:
        _world.apply_npc_source_activity_changed({"id": "worker", "kind": kind, "source_name": "the Well"})
        _check("activity_no_marker: %s — kind stored for the hover line" % kind, str(c.get_meta("source_activity_kind", "")) == kind)
        _check("activity_no_marker: %s — no label above the head" % kind, _labels(c) == 0)
    _world.apply_npc_source_activity_changed({"id": "worker"})
    _check("activity_no_marker: clear — kind cleared", str(c.get_meta("source_activity_kind", "x")) == "")
    _check("activity_no_marker: clear — no label above the head", _labels(c) == 0)
    _set_dormant(c, "sleeping")
    _world.apply_npc_source_activity_changed({"id": "worker", "kind": "repair"})
    _expect_zzz(c, true, "activity_no_marker: dormant then activity")
    _expect_dim(c, true, "activity_no_marker: dormant then activity")
    _world.placed_npcs.erase("worker")
    c.free()
    _done()
