extends SceneTree

## Headless regression harness for the figure at the treeline (LLM-844).
##
## What this file exists to catch:
##
##  1. The figure stands only beside a TREE that is in view and 10–18 tiles
##     from the player — never by a house, on water, under a roof, or off
##     screen; with no such tree there is no spot. It stands in the open just
##     left of the tree's own pixels at the figure's height, not under the
##     canopy or in the shadow.
##  2. Every "when" condition refuses on its own: no player, indoors, storm,
##     day or late night, no focus, this dusk already rolled, the cooldown.
##  3. It goes for a walk toward it (not away), off screen, and by a fade
##     after its time.
##  4. The cooldown and the dusk roll are written on a real showing (the roll
##     even when it fails); a forced showing writes nothing and skips the
##     dusk, storm and cooldown checks but not indoors.
##  5. The statistics carry the right kinds and reasons.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/treeline_figure_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TreelineFigure = preload("res://scripts/treeline_figure.gd")
const OMENS := "user://treeline_figure_test_omens.cfg"
const NOW := 2_000_000_000.0
const DAY := 86400.0

const TESTS := [
    "_test_spot_beside_a_tree_in_range_and_view",
    "_test_spot_at_the_tree_edge_from_its_pixels",
    "_test_no_spot_without_a_tree_in_range",
    "_test_no_spot_off_screen",
    "_test_no_spot_on_water_or_under_a_roof",
    "_test_fitting_dusk_is_not_refused",
    "_test_each_condition_refuses",
    "_test_forced_keeps_only_player_checks",
    "_test_walk_toward_removes_walk_away_keeps",
    "_test_walk_by_another_actor_is_ignored",
    "_test_off_screen_removes",
    "_test_fades_after_its_time",
    "_test_showing_writes_cooldown_and_roll",
    "_test_failed_roll_spends_the_dusk_only",
    "_test_no_spot_keeps_the_roll",
    "_test_forced_writes_nothing",
    "_test_forced_with_no_tree_reports_skipped",
    "_test_unwritable_omens_still_rolls_once",
    "_test_unwritable_omens_still_cools_down",
    "_test_body_retries_after_a_failed_download",
]

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

## The fields treeline_figure.gd reads off world.gd.
class StubWorld:
    extends Node2D
    var placed_objects: Dictionary = {}
    var placed_npcs: Dictionary = {}
    var pc_actor_id := "pc"
    var objects_node := Node2D.new()
    var current_phase := "night"
    var _phase_pos := 0.2
    var current_weather := "clear"
    var village_date := "1692-10-30"
    var water: Array[Rect2] = []
    var _npc_sheets: Dictionary = {}
    var sheet_requests: Array = []

    func _init() -> void:
        add_child(objects_node)

    func add_object(id: String, asset_id: String, pos: Vector2, size := Vector2(64, 96), sprite := true) -> Node2D:
        var container := Node2D.new()
        container.set_meta("asset_id", asset_id)
        container.set_meta("size", size)
        container.position = pos
        if sprite:
            container.add_child(Sprite2D.new())
        add_child(container)
        placed_objects[id] = container
        return container

    func add_pc(pos: Vector2) -> Node2D:
        var container := Node2D.new()
        container.position = pos
        add_child(container)
        placed_npcs[pc_actor_id] = container
        return container

    ## Anchored at the bottom centre, as the trees and houses are.
    func compute_object_hit_rect(container: Node2D, _sprite: Node2D) -> Rect2:
        var size: Vector2 = container.get_meta("size")
        return Rect2(container.position - Vector2(size.x * 0.5, size.y * 0.95), size)

    ## Records the request; the test completes it by calling one callback.
    func get_or_load_npc_sheet(path: String, loaded: Callable, failed: Callable) -> void:
        sheet_requests.append([path, loaded, failed])

    func _build_character_sprite(_sprite_data: Dictionary) -> AnimatedSprite2D:
        var sprite := AnimatedSprite2D.new()
        sprite.sprite_frames = SpriteFrames.new()
        sprite.sprite_frames.add_animation("south_idle")
        sprite.sprite_frames.add_frame("south_idle", PlaceholderTexture2D.new())
        return sprite

    func terrain_at(pos: Vector2) -> int:
        for r in water:
            if r.has_point(pos):
                return 5
        return 2

func _initialize() -> void:
    pass

func _process(_delta: float) -> bool:
    _check_test_list()
    _run_all()
    DirAccess.remove_absolute(ProjectSettings.globalize_path(OMENS))
    _check_all_tests_ran()
    print("\n[treeline_figure_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[treeline_figure_test] ALL PASS")
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

var _reports: Array = []
var _roll_result := true
var _now := NOW

## The player at the origin, one tree 14 tiles east, the view wide open, a
## fresh omens file, and a clean report log.
func _setup() -> Array:
    DirAccess.remove_absolute(ProjectSettings.globalize_path(OMENS))
    _reports = []
    _roll_result = true
    _now = NOW
    var world := StubWorld.new()
    root.add_child(world)
    world.add_pc(Vector2.ZERO)
    world.add_object("oak", "tree_oak", Vector2(448, 0))
    var figure: Node = TreelineFigure.new()
    figure.world = world
    figure.omens_path = OMENS
    figure.now_unix = func() -> float: return _now
    figure.visible_rect = func() -> Rect2: return Rect2(-1000, -1000, 2000, 2000)
    figure.is_indoors = func() -> bool: return false
    figure.is_tree = func(asset_id: String) -> bool: return asset_id.begins_with("tree")
    figure.roll = func() -> bool: return _roll_result
    figure.report = func(kind: String, detail: Dictionary) -> void: _reports.append([kind, detail])
    figure.build_body = func() -> Node2D: return Node2D.new()
    root.add_child(figure)
    figure.set_process(false)
    return [world, figure]

func _teardown(fixture: Array) -> void:
    fixture[1].free()
    fixture[0].free()

func _write_omens(values: Dictionary) -> void:
    var omens := ConfigFile.new()
    for key in values:
        omens.set_value("treeline_figure", key, values[key])
    omens.save(OMENS)

func _read_omens() -> ConfigFile:
    var omens := ConfigFile.new()
    omens.load(OMENS)
    return omens

func _last_report() -> Array:
    return _reports.back() if not _reports.is_empty() else ["", {}]

# --- the spot ----------------------------------------------------------------------

func _test_spot_beside_a_tree_in_range_and_view() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    world.add_object("near", "tree_birch", Vector2(96, 0))
    world.add_object("far", "tree_maple", Vector2(0, 900))
    world.add_object("barn", "barn", Vector2(-448, 0))
    # No sprite yet, so no roof of its own: only the tree check keeps it out.
    world.add_object("signpost", "signpost", Vector2(0, -448), Vector2(64, 96), false)
    for i in 30:
        var spot = f[1].pick_spot()
        if spot == null or spot.distance_to(Vector2(448, 0)) > 60.0 or spot.x >= 448.0:
            _check("every spot is left of the in-range tree (got %s)" % str(spot), false)
            _teardown(f)
            _done()
            return
        if spot.y >= 0.0:
            _check("the spot stands behind the tree's base (got %s)" % str(spot), false)
            _teardown(f)
            _done()
            return
    _check("30 picks: all just left of the one tree in range, behind its base", true)
    _teardown(f)
    _done()

## A 40x60 tree drawn at scale 2, anchored at its base like a placed object:
## trunk and canopy in columns 12..27, a wide crown in the top rows (above the
## figure's head), and a faint shadow to the right near the base.
func _pixel_tree() -> Sprite2D:
    var img := Image.create(40, 60, false, Image.FORMAT_RGBA8)
    img.fill(Color(0, 0, 0, 0))
    for y in 60:
        for x in range(12, 28):
            img.set_pixel(x, y, Color(0.3, 0.5, 0.2, 1.0))
    for y in 10:
        for x in 40:
            img.set_pixel(x, y, Color(0.3, 0.5, 0.2, 1.0))
    for y in range(50, 60):
        for x in range(28, 40):
            img.set_pixel(x, y, Color(0, 0, 0, 0.3))
    var atlas := AtlasTexture.new()
    atlas.atlas = ImageTexture.create_from_image(img)
    atlas.region = Rect2(0, 0, 40, 60)
    var sprite := Sprite2D.new()
    sprite.texture = atlas
    sprite.centered = false
    sprite.scale = Vector2(2, 2)
    sprite.position = Vector2(-40, -114)
    return sprite

func _test_spot_at_the_tree_edge_from_its_pixels() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    var tree: Node2D = world.placed_objects["oak"]
    for child in tree.get_children():
        child.free()
    tree.add_child(_pixel_tree())
    # Leftmost tree pixel at the figure's height: column 12 -> -40 + 24 = -16.
    _check("the reach is read from the pixels at the figure's height, not the crown",
        is_equal_approx(f[1]._tree_left_reach(tree), -16.0))
    var ok := true
    for i in 30:
        var spot = f[1].pick_spot()
        var right_edge: float = spot.x + TreelineFigure.FIGURE_RECT.end.x - 448.0 if spot != null else 999.0
        if right_edge < -16.0 or right_edge > -10.0:
            ok = false
    _check("30 picks: the figure's right edge at the tree's left edge, reaching in at most 6 px", ok)
    _teardown(f)
    _done()

func _test_no_spot_without_a_tree_in_range() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    world.placed_objects["oak"].position = Vector2(150, 0)
    world.add_object("barn", "barn", Vector2(448, 0))
    _check("a near tree and an in-range house give no spot", f[1].pick_spot() == null)
    world.placed_objects["oak"].position = Vector2(800, 0)
    _check("a tree 25 tiles off gives no spot", f[1].pick_spot() == null)
    _teardown(f)
    _done()

func _test_no_spot_off_screen() -> void:
    var f := _setup()
    f[1].visible_rect = func() -> Rect2: return Rect2(-200, -200, 400, 400)
    _check("a tree in range but out of view gives no spot", f[1].pick_spot() == null)
    f[1].visible_rect = func() -> Rect2: return Rect2(-200, -200, 600, 400)
    _check("the same tree half in view gives no spot", f[1].pick_spot() == null)
    f[1].visible_rect = func() -> Rect2: return Rect2(-200, -200, 800, 400)
    _check("in full view it does", f[1].pick_spot() != null)
    _teardown(f)
    _done()

func _test_no_spot_on_water_or_under_a_roof() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    world.water = [Rect2(380, -100, 140, 200)]
    _check("water beside the tree gives no spot", f[1].pick_spot() == null)
    world.water = []
    world.add_object("barn", "barn", Vector2(448, 40), Vector2(200, 200))
    _check("a roof over the spot gives no spot", f[1].pick_spot() == null)
    _teardown(f)
    _done()

# --- when ------------------------------------------------------------------------------

func _test_fitting_dusk_is_not_refused() -> void:
    var f := _setup()
    _check("early night, outdoors, clear, focused, no history: may show", f[1].refusal(false) == "")
    _write_omens({"last_figure_at": NOW - 22 * DAY, "rolled_dusk": "1692-10-29"})
    _check("22 days on and yesterday's roll: may show", f[1].refusal(false) == "")
    _teardown(f)
    _done()

func _test_each_condition_refuses() -> void:
    var cases := [
        ["no_pc", func(w, _fig): w.placed_npcs.clear()],
        ["indoors", func(_w, fig): fig.is_indoors = func() -> bool: return true],
        ["storm", func(w, _fig): w.current_weather = "storm"],
        ["not_dusk", func(w, _fig): w.current_phase = "day"],
        ["not_dusk", func(w, _fig): w._phase_pos = 0.6],
        ["no_focus", func(_w, fig): fig._notification(NOTIFICATION_APPLICATION_FOCUS_OUT)],
        ["rolled", func(_w, _fig): _write_omens({"rolled_dusk": "1692-10-30"})],
        ["cooldown", func(_w, _fig): _write_omens({"last_figure_at": NOW - 20 * DAY})],
    ]
    for c in cases:
        var f := _setup()
        c[1].call(f[0], f[1])
        var got: String = f[1].refusal(false)
        _check("refuses %s (got %s)" % [c[0], got], got == c[0])
        _teardown(f)
    _done()

func _test_forced_keeps_only_player_checks() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    world.current_phase = "day"
    world.current_weather = "storm"
    _write_omens({"last_figure_at": NOW - DAY, "rolled_dusk": "1692-10-30"})
    _check("forced ignores day, storm, roll and cooldown", f[1].refusal(true) == "")
    f[1].is_indoors = func() -> bool: return true
    _check("forced still refuses indoors", f[1].refusal(true) == "indoors")
    _teardown(f)
    _done()

# --- how it goes -----------------------------------------------------------------

func _show(f: Array) -> Node2D:
    f[1].force_show({})
    return f[1].figure

func _walk(world: StubWorld, goal: Vector2) -> void:
    world.placed_npcs["pc"].set_meta("walking", {"path": [Vector2(32, 0), goal]})

func _test_walk_toward_removes_walk_away_keeps() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    _show(f)
    _walk(world, Vector2(-320, 0))
    f[1].on_npc_walking("pc")
    _check("a walk away keeps the figure", f[1].figure != null)
    _walk(world, Vector2(224, 0))
    f[1].on_npc_walking("pc")
    _check("a walk toward removes it", f[1].figure == null)
    _check("reported as walked", _last_report()[0] == "treeline_figure_gone" and _last_report()[1].reason == "walked")
    _teardown(f)
    _done()

func _test_walk_by_another_actor_is_ignored() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    _show(f)
    var other := Node2D.new()
    other.set_meta("walking", {"path": [Vector2(448, -20)]})
    world.add_child(other)
    world.placed_npcs["josiah"] = other
    f[1].on_npc_walking("josiah")
    _check("a villager walking to it does not remove it", f[1].figure != null)
    _teardown(f)
    _done()

func _test_off_screen_removes() -> void:
    var f := _setup()
    _show(f)
    f[1].tick_figure(1.0)
    _check("in view it stays", f[1].figure != null)
    f[1].visible_rect = func() -> Rect2: return Rect2(-1000, -1000, 1100, 2000)
    f[1].tick_figure(1.0)
    _check("panned away it goes", f[1].figure == null)
    _check("reported as offscreen after 2 s",
        _last_report()[1].get("reason") == "offscreen" and _last_report()[1].get("seconds_shown") == 2)
    _teardown(f)
    _done()

func _test_fades_after_its_time() -> void:
    var f := _setup()
    var fig := _show(f)
    var fade_at: float = f[1]._fade_at
    _check("the fade starts 60–90 s in", fade_at >= 60.0 and fade_at <= 90.0)
    f[1].tick_figure(fade_at - 1.0)
    _check("before the fade it is at full strength", is_equal_approx(fig.modulate.a, 1.0))
    f[1].tick_figure(6.0)
    _check("half way through the fade it is half as strong",
        is_equal_approx(fig.modulate.a, 0.5))
    f[1].tick_figure(5.0)
    _check("after the fade it is gone", f[1].figure == null)
    _check("reported as faded", _last_report()[1].get("reason") == "faded")
    _teardown(f)
    _done()

# --- remembering -----------------------------------------------------------------

func _test_showing_writes_cooldown_and_roll() -> void:
    var f := _setup()
    f[1].try_show()
    _check("a passing roll shows the figure", f[1].figure != null)
    var omens := _read_omens()
    _check("the showing time is written", omens.get_value("treeline_figure", "last_figure_at", 0.0) == NOW)
    _check("the dusk is marked rolled", omens.get_value("treeline_figure", "rolled_dusk", "") == "1692-10-30")
    _check("reported as shown, not forced",
        _last_report()[0] == "treeline_figure_shown" and _last_report()[1].forced == false
        and is_equal_approx(_last_report()[1].distance_tiles, round(_last_report()[1].distance_tiles * 10.0) / 10.0))
    _teardown(f)
    _done()

func _test_failed_roll_spends_the_dusk_only() -> void:
    var f := _setup()
    _roll_result = false
    f[1].try_show()
    _check("a failed roll shows nothing", f[1].figure == null)
    var omens := _read_omens()
    _check("the dusk is marked rolled", omens.get_value("treeline_figure", "rolled_dusk", "") == "1692-10-30")
    _check("the cooldown is not started", not omens.has_section_key("treeline_figure", "last_figure_at"))
    _roll_result = true
    f[1].try_show()
    _check("no second roll this dusk", f[1].figure == null)
    _teardown(f)
    _done()

func _test_no_spot_keeps_the_roll() -> void:
    var f := _setup()
    f[0].placed_objects["oak"].position = Vector2(100, 0)
    f[1].try_show()
    _check("no tree in range: nothing shown", f[1].figure == null)
    _check("and the roll is not spent", not _read_omens().has_section_key("treeline_figure", "rolled_dusk"))
    _teardown(f)
    _done()

func _test_forced_writes_nothing() -> void:
    var f := _setup()
    _write_omens({"last_figure_at": NOW - DAY})
    f[1].force_show({})
    _check("forced shows inside the cooldown", f[1].figure != null)
    var omens := _read_omens()
    _check("the showing time is unchanged", omens.get_value("treeline_figure", "last_figure_at", 0.0) == NOW - DAY)
    _check("the dusk is not marked rolled", not omens.has_section_key("treeline_figure", "rolled_dusk"))
    _check("reported as shown and forced", _last_report()[0] == "treeline_figure_shown" and _last_report()[1].forced == true)
    f[1].remove("walked")
    _check("its going is reported forced", _last_report()[1].forced == true)
    _teardown(f)
    _done()

func _test_forced_with_no_tree_reports_skipped() -> void:
    var f := _setup()
    f[0].placed_objects["oak"].position = Vector2(100, 0)
    f[1].force_show({})
    _check("nothing shown", f[1].figure == null)
    _check("reported skipped, no_treeline_on_screen",
        _last_report()[0] == "treeline_figure_skipped" and _last_report()[1].reason == "no_treeline_on_screen"
        and _last_report()[1].forced == true)
    f[0].placed_objects["oak"].position = Vector2(448, 0)
    f[1].is_indoors = func() -> bool: return true
    f[1].force_show({})
    _check("indoors is reported skipped, indoors", _last_report()[1].reason == "indoors")
    _teardown(f)
    _done()

# --- resilience (code_review) -----------------------------------------------------

const UNWRITABLE := "user://no_such_dir/omens.cfg"

func _test_unwritable_omens_still_rolls_once() -> void:
    var f := _setup()
    f[1].omens_path = UNWRITABLE
    _roll_result = false
    f[1].try_show()
    _roll_result = true
    f[1].try_show()
    _check("a failed roll is remembered when the file cannot be written", f[1].figure == null)
    _check("refused as rolled", f[1].refusal(false) == "rolled")
    _teardown(f)
    _done()

func _test_unwritable_omens_still_cools_down() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    f[1].omens_path = UNWRITABLE
    f[1].try_show()
    _check("it shows", f[1].figure != null)
    f[1].remove("walked")
    world.village_date = "1692-10-31"
    _now = NOW + DAY
    f[1].try_show()
    _check("the next dusk it does not show again", f[1].figure == null)
    _check("refused by the cooldown", f[1].refusal(false) == "cooldown")
    _teardown(f)
    _done()

func _test_body_retries_after_a_failed_download() -> void:
    var f := _setup()
    var world: StubWorld = f[0]
    var fig: Node = f[1]
    _check("no sheet: no body", fig._build_body() == null)
    _check("one download asked for", world.sheet_requests.size() == 1)
    fig._build_body()
    _check("not asked again while in flight", world.sheet_requests.size() == 1)
    world.sheet_requests[0][2].call()
    _now = NOW + 30.0
    fig._build_body()
    _check("not asked again within the retry wait", world.sheet_requests.size() == 1)
    _now = NOW + 61.0
    fig._build_body()
    _check("asked again after it", world.sheet_requests.size() == 2)
    world._npc_sheets[TreelineFigure.BODY_SHEET] = PlaceholderTexture2D.new()
    world.sheet_requests[1][1].call(world._npc_sheets[TreelineFigure.BODY_SHEET])
    var body: Node2D = fig._build_body()
    _check("once loaded, a still body facing the viewer",
        body != null and body.animation == "south_idle" and not body.is_playing())
    _check("drawn by the figure shader",
        body != null and body.material is ShaderMaterial and body.material.shader == TreelineFigure.FIGURE_SHADER)
    if body != null:
        body.free()
    _teardown(f)
    _done()
