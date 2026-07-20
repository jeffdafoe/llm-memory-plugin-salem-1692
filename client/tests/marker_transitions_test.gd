extends SceneTree

## Headless regression harness for the above-head status markers in world.gd — the
## sleep "Zzz" marker (_apply_dormant_visual) and the LLM-448 source-activity marker
## (_apply_activity_marker). Covers LLM-449: the queue_free lifecycle race (the markers
## are now persistent, visibility-toggled nodes) and the sleep/activity mutual exclusion
## (at most one marker visible, a masked-but-still-active marker restored from stored
## state when its masker clears, and the sprite dim staying owned by dormancy).
##
## Run headless (CI and local) — the import step must run once to populate the asset
## cache the activity marker's font load needs; a --script run does not build it:
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/marker_transitions_test.gd
## Exits 0 when every check passes, 1 if any check fails (or a script error aborts).
##
## world.gd is instantiated off-tree via .new() so _ready()/@onready never fire: the
## marker methods only touch the container passed in plus the cached lucide font, none
## of the network / terrain / autoload state _ready() would otherwise set up. The two
## feeds are simulated exactly as world.gd drives them — dormancy writes its own
## "dormant" meta; the activity feed writes "source_activity_kind" before the apply call.

const ZZZ := "ZzzMarker"
const ACT := "ActivityMarker"

## Every test, by name. _run_all dispatches through this and _check_all_tests_ran
## asserts each one reached its end — see _done().
const TESTS := [
    "_test_dormant_toggle",
    "_test_same_frame_wake_sleep_no_duplicate",
    "_test_repeated_dormant_no_duplicate",
    "_test_mutual_exclusion_both_orders",
    "_test_forward_restore_activity_after_sleep",
    "_test_reverse_restore_zzz_after_activity_clear",
    "_test_activity_toggle_no_sleep",
    "_test_position_self_heal_marker_before_sprite",
    "_test_activity_marker_sizing_and_placement",
]

var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

func _initialize() -> void:
    _world = load("res://scripts/world.gd").new()
    _run_all()
    _world.free()
    _check_test_list()
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
## it runs twice but leaves only one completion mark.
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

# --- fixtures / feed simulation -------------------------------------------------

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

## Mirror the npc_source_activity_changed feed. world.gd writes source_activity_kind
## BEFORE calling _apply_activity_marker (see world.gd lines 610/766), and the wake
## restore path reads that same meta — so the ordering matters and is replicated here.
func _set_activity(c: Node2D, kind: String) -> void:
    c.set_meta("source_activity_kind", kind)
    _world._apply_activity_marker(c, kind)

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

func _sprite_modulate(c: Node2D) -> Color:
    var spr: AnimatedSprite2D = _world._npc_sprite(c)
    return spr.modulate if spr != null else Color.WHITE

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)

## Assert exactly the expected marker is visible ("zzz", "activity", or "" for none),
## that the shown marker is a single persistent node, and that neither marker node is
## ever duplicated in the shared above-head slot.
func _expect_only(c: Node2D, who: String, ctx: String) -> void:
    _check("%s — zzz visible is %s" % [ctx, who == "zzz"], _vis(c, ZZZ) == (who == "zzz"))
    _check("%s — activity visible is %s" % [ctx, who == "activity"], _vis(c, ACT) == (who == "activity"))
    _check("%s — ZzzMarker not duplicated" % ctx, _count(c, ZZZ) <= 1)
    _check("%s — ActivityMarker not duplicated" % ctx, _count(c, ACT) <= 1)
    if who == "zzz":
        _check("%s — exactly one ZzzMarker node" % ctx, _count(c, ZZZ) == 1)
    elif who == "activity":
        _check("%s — exactly one ActivityMarker node" % ctx, _count(c, ACT) == 1)

## Assert the sprite dim state — dormancy owns it (DORMANT_DIM while dormant, WHITE
## otherwise); the activity path must never touch modulation.
func _expect_dim(c: Node2D, dimmed: bool, ctx: String) -> void:
    var expected: Color = _world.DORMANT_DIM if dimmed else Color.WHITE
    _check("%s — sprite modulate %s" % [ctx, "dimmed" if dimmed else "normal"], _sprite_modulate(c) == expected)

# --- cases ----------------------------------------------------------------------

func _test_dormant_toggle() -> void:
    var c := _make_container()
    _set_dormant(c, "sleeping")
    _expect_only(c, "zzz", "dormant_toggle: after sleep")
    _expect_dim(c, true, "dormant_toggle: after sleep")
    _set_dormant(c, "")
    _expect_only(c, "", "dormant_toggle: after wake")
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
    _expect_only(c, "zzz", "same_frame_wake_sleep")
    c.free()
    _done()

func _test_repeated_dormant_no_duplicate() -> void:
    var c := _make_container()
    _set_dormant(c, "sleeping")
    _set_dormant(c, "sleeping")
    _set_dormant(c, "resting")
    _expect_only(c, "zzz", "repeated_dormant")
    c.free()
    _done()

func _test_mutual_exclusion_both_orders() -> void:
    # sleep then activity: activity wins the slot; the sprite stays dimmed because the
    # NPC is still flagged dormant and the activity path never touches modulation.
    var c1 := _make_container()
    _set_dormant(c1, "sleeping")
    _set_activity(c1, "repair")
    _expect_only(c1, "activity", "mutual_excl: sleep then activity")
    _expect_dim(c1, true, "mutual_excl: sleep then activity")
    c1.free()

    var c2 := _make_container()
    _set_activity(c2, "harvest")
    _expect_dim(c2, false, "mutual_excl: activity before sleep")
    _set_dormant(c2, "sleeping")
    _expect_only(c2, "zzz", "mutual_excl: activity then sleep")
    _expect_dim(c2, true, "mutual_excl: activity then sleep")
    c2.free()
    _done()

## Reviewer's primary bug: activity active -> sleep masks it -> waking must restore the
## activity marker from the stored kind (not leave both hidden) and undim the sprite.
func _test_forward_restore_activity_after_sleep() -> void:
    var c := _make_container()
    _set_activity(c, "repair")
    _expect_only(c, "activity", "forward_restore: activity set")
    _expect_dim(c, false, "forward_restore: activity set")
    _set_dormant(c, "sleeping")
    _expect_only(c, "zzz", "forward_restore: dormant masks activity")
    _expect_dim(c, true, "forward_restore: dormant masks activity")
    _set_dormant(c, "")
    _expect_only(c, "activity", "forward_restore: wake restores activity")
    _expect_dim(c, false, "forward_restore: wake restores activity")
    c.free()
    _done()

## Symmetric case: dormant -> activity (out of step) masks Zzz -> clearing the activity
## must restore the Zzz while still dormant. The sprite stays dimmed throughout — the
## activity path must not undim a dormant NPC.
func _test_reverse_restore_zzz_after_activity_clear() -> void:
    var c := _make_container()
    _set_dormant(c, "sleeping")
    _expect_only(c, "zzz", "reverse_restore: dormant set")
    _expect_dim(c, true, "reverse_restore: dormant set")
    _set_activity(c, "stoke")
    _expect_only(c, "activity", "reverse_restore: activity masks zzz")
    _expect_dim(c, true, "reverse_restore: activity masks zzz")
    _set_activity(c, "")
    _expect_only(c, "zzz", "reverse_restore: activity clear restores zzz")
    _expect_dim(c, true, "reverse_restore: activity clear restores zzz")
    c.free()
    _done()

func _test_activity_toggle_no_sleep() -> void:
    var c := _make_container()
    _set_activity(c, "repair")
    _expect_only(c, "activity", "activity_toggle: set")
    _expect_dim(c, false, "activity_toggle: set")
    _set_activity(c, "")
    _expect_only(c, "", "activity_toggle: clear leaves nothing when not dormant")
    _expect_dim(c, false, "activity_toggle: clear")
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
    _expect_only(c, "zzz", "position_self_heal")
    c.free()
    _done()

## The activity glyph carries its OWN size and placement: the label renders at
## ACTIVITY_MARKER_FONT_SIZE and is positioned by _activity_marker_position, which derives
## both the horizontal centering and the head clearance from that size (using the font's
## real line height). The Zzz's _zzz_marker_position is tuned for its 3-character text, so
## reusing it would drift the glyph right and down into the head as the size changes.
##
## Asserts the CONTRACT — glyph centred on the sprite, label sitting FULLY above it —
## rather than "some negative offset", so a future resize or font swap that reintroduces
## overlap fails here. Covers the real rendered-width path, not just the fallback.
func _test_activity_marker_sizing_and_placement() -> void:
    var size_px: float = float(_world.ACTIVITY_MARKER_FONT_SIZE)
    # Guard the font lookup through _check rather than letting a null deref abort here:
    # a desynced .godot import cache makes _get_icon_font() return null, and this names
    # that cause directly instead of leaving the reader with the harness's generic
    # "did not run to completion" (LLM-479 hit exactly this).
    var font: Font = _world._get_icon_font()
    _check("activity_placement — icon font loaded", font != null)
    if font == null:
        _done()
        return
    var line_h: float = font.get_height(_world.ACTIVITY_MARKER_FONT_SIZE)

    # Real sprite frames (24px frame at 2x) — exercises the rendered-width branch.
    var c := Node2D.new()
    var spr := AnimatedSprite2D.new()
    spr.name = "Sprite"
    spr.centered = false
    spr.scale = Vector2(2, 2)
    var tex := PlaceholderTexture2D.new()
    tex.size = Vector2(24, 24)
    var frames := SpriteFrames.new()
    frames.add_animation("south_idle")
    frames.add_frame("south_idle", tex)
    spr.sprite_frames = frames
    spr.animation = "south_idle"
    spr.position = Vector2(40, 40)
    c.add_child(spr)

    _set_activity(c, "repair")
    var m: Label = c.get_node_or_null(ACT)
    _check("activity_placement — marker created", m != null)
    if m == null:
        c.free()
        _done()
        return
    _check("activity_placement — font size override is ACTIVITY_MARKER_FONT_SIZE", m.has_theme_font_size_override("font_size") and m.get_theme_font_size("font_size") == int(size_px))
    _check("activity_placement — uses the activity positioner", m.position == _world._activity_marker_position(spr))
    # Contract 1: centred on the sprite's RENDERED width (frame width x scale).
    var sprite_centre_x: float = spr.position.x + 24.0 * spr.scale.x * 0.5
    _check("activity_placement — glyph centred on the rendered sprite", is_equal_approx(m.position.x + size_px * 0.5, sprite_centre_x))
    # Contract 2: the label's BOTTOM (origin + real line height) clears the sprite's top.
    _check("activity_placement — label bottom clears the sprite top", m.position.y + line_h <= spr.position.y)
    _expect_only(c, "activity", "activity_placement")
    c.free()

    # No sprite_frames — the fallback half-width branch still centres and clears.
    var c2 := _make_container()
    var spr2: AnimatedSprite2D = _world._npc_sprite(c2)
    spr2.position = Vector2(10, 10)
    _set_activity(c2, "harvest")
    var m2: Label = c2.get_node_or_null(ACT)
    _check("activity_placement — fallback marker created", m2 != null)
    if m2 != null:
        _check("activity_placement — centred on the fallback half-width", is_equal_approx(m2.position.x + size_px * 0.5, spr2.position.x + 16.0))
        _check("activity_placement — fallback label bottom clears the sprite top", m2.position.y + line_h <= spr2.position.y)
    c2.free()

    # No sprite at all — the null fallback still derives its centring from the size.
    _check("activity_placement — null-sprite fallback derives from the size", is_equal_approx(_world._activity_marker_position(null).x, -size_px * 0.5))
    _done()
