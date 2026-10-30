extends SceneTree

## Headless regression harness for the night dark (LLM-842): the woods mask
## built from tree objects, the night-vision circle on the player's own
## character, and the live night colour.
##
## What this file exists to catch:
##
##  1. The woods come from TREES, not from dark-grass terrain. On the live map
##     dark grass is a third of the map, village included; a mask built from it
##     would darken the village. The mask must mark the ground near trees and
##     nothing else.
##  2. Both lights are gone by day and full at night.
##  3. The night-vision light sits on the player's own character only — never
##     on another player or a villager.
##  4. The night colour from the engine replaces the last stop of the sunset
##     curve, and a bad value is ignored.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/night_shade_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const NightShade = preload("res://scripts/night_shade.gd")

const TESTS := [
    "_test_mask_marks_ground_near_a_tree_only",
    "_test_mask_takes_the_strongest_of_overlapping_trees",
    "_test_mask_clips_at_the_map_edge",
    "_test_night_factor_is_zero_by_day_and_full_at_night",
    "_test_tree_tiles_reads_only_trees_on_the_grid",
    "_test_refresh_builds_and_places_the_mask",
    "_test_refresh_with_no_trees_turns_the_woods_off",
    "_test_a_moved_tree_rebuilds_the_mask",
    "_test_woods_light_subtracts_a_share_of_the_tint",
    "_test_vision_light_on_own_character_only",
    "_test_vision_light_follows_a_replaced_character",
    "_test_vision_light_off_by_day",
    "_test_night_color_replaces_the_last_curve_stop",
    "_test_bad_night_color_is_ignored",
]

var _shade: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

## The fields night_shade.gd reads off world.gd.
class StubWorld:
    extends Node2D
    var placed_objects: Dictionary = {}
    var placed_npcs: Dictionary = {}
    var pc_actor_id := "pc"
    var map_width: int = 40
    var map_height: int = 30
    var pad_x: int = 10
    var pad_y: int = 5
    var _phase_pos := 1.0
    var canvas_modulate: CanvasModulate = null
    var _gradient := GradientTexture2D.new()

    func _get_light_gradient() -> Texture2D:
        return _gradient

    func add_object(id: String, asset_id: String, pos: Vector2) -> Node2D:
        var container := Node2D.new()
        container.set_meta("asset_id", asset_id)
        container.position = pos
        add_child(container)
        placed_objects[id] = container
        return container

    func add_npc(id: String) -> Node2D:
        var container := Node2D.new()
        add_child(container)
        placed_npcs[id] = container
        return container

func _initialize() -> void:
    _shade = Node2D.new()
    _shade.set_script(NightShade)
    root.add_child(_shade)

func _process(_delta: float) -> bool:
    _check("harness — shade entered the tree", _shade.is_inside_tree())
    _shade.set_process(false)
    _check_test_list()
    _run_all()
    _shade.queue_free()
    _check_all_tests_ran()
    print("\n[night_shade_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[night_shade_test] ALL PASS")
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

## A stub world wired to the shade, with "oak" counted as a tree.
func _setup_world() -> StubWorld:
    var stub := StubWorld.new()
    stub.canvas_modulate = CanvasModulate.new()
    stub.canvas_modulate.color = Color(0.2, 0.3, 0.4)
    stub.add_child(stub.canvas_modulate)
    root.add_child(stub)
    _shade.world = stub
    _shade.is_tree = func(asset_id: String) -> bool: return asset_id == "oak"
    _shade._trees_signature = ""
    _shade.woods_light.enabled = false
    _shade.woods_light.texture = null
    return stub

func _teardown_world(stub: StubWorld) -> void:
    if _shade.vision_light != null and is_instance_valid(_shade.vision_light):
        _shade.vision_light.free()
    _shade.vision_light = null
    _shade.world = null
    stub.free()

func _alpha(img: Image, x: int, y: int) -> float:
    return img.get_pixel(x, y).a

func _tiles(list: Array) -> Array[Vector2i]:
    var out: Array[Vector2i] = []
    for t in list:
        out.append(t)
    return out

# --- tests -------------------------------------------------------------------------

func _test_mask_marks_ground_near_a_tree_only() -> void:
    var img: Image = NightShade.build_woods_mask(_tiles([Vector2i(10, 10)]), 30, 30)
    _check("mask is one pixel per tile", img.get_width() == 30 and img.get_height() == 30)
    _check("the tree's own tile is full dark", is_equal_approx(_alpha(img, 10, 10), 1.0))
    _check("a tile inside the core is full dark", is_equal_approx(_alpha(img, 11, 10), 1.0))
    var mid := _alpha(img, 12, 10)
    _check("the edge fades (between 0 and 1 two tiles out)", mid > 0.0 and mid < 1.0)
    _check("the radius is clear", is_zero_approx(_alpha(img, 13, 10)))
    _check("past the radius is clear", is_zero_approx(_alpha(img, 15, 10)))
    _check("far ground is clear", is_zero_approx(_alpha(img, 25, 25)))
    _check("mask is white, so the light's colour sets what it takes away",
        img.get_pixel(10, 10).r == 1.0 and img.get_pixel(10, 10).g == 1.0)
    _done()

func _test_mask_takes_the_strongest_of_overlapping_trees() -> void:
    var one: Image = NightShade.build_woods_mask(_tiles([Vector2i(10, 10)]), 30, 30)
    var two: Image = NightShade.build_woods_mask(_tiles([Vector2i(10, 10), Vector2i(13, 10)]), 30, 30)
    _check("a second tree two tiles from a point raises it to full dark",
        _alpha(two, 12, 10) > _alpha(one, 12, 10) and is_equal_approx(_alpha(two, 12, 10), 1.0))
    _check("overlap never lowers a value", _alpha(two, 10, 10) >= _alpha(one, 10, 10))
    _done()

func _test_mask_clips_at_the_map_edge() -> void:
    var img: Image = NightShade.build_woods_mask(_tiles([Vector2i(0, 0), Vector2i(29, 29)]), 30, 30)
    _check("a corner tree marks its tile", is_equal_approx(_alpha(img, 0, 0), 1.0))
    _check("the far corner tree marks its tile", is_equal_approx(_alpha(img, 29, 29), 1.0))
    _done()

func _test_night_factor_is_zero_by_day_and_full_at_night() -> void:
    _check("day is 0", is_zero_approx(NightShade.night_factor(0.0)))
    _check("mid-dusk is still 0", is_zero_approx(NightShade.night_factor(0.5)))
    _check("late dusk is part way", is_equal_approx(NightShade.night_factor(0.75), 0.5))
    _check("night is 1", is_equal_approx(NightShade.night_factor(1.0), 1.0))
    _done()

func _test_tree_tiles_reads_only_trees_on_the_grid() -> void:
    var stub := _setup_world()
    stub.add_object("t1", "oak", Vector2(64.0, -32.0))
    stub.add_object("w1", "well", Vector2(0.0, 0.0))
    var tiles: Array[Vector2i] = _shade.tree_tiles()
    _check("only the tree is read", tiles.size() == 1)
    # world (64, -32) is tile (2, -1) from the origin; the origin sits at (pad_x, pad_y).
    _check("its tile is offset by the grid padding", tiles.size() == 1 and tiles[0] == Vector2i(12, 4))
    _teardown_world(stub)
    _done()

func _test_refresh_builds_and_places_the_mask() -> void:
    var stub := _setup_world()
    stub.add_object("t1", "oak", Vector2(0.0, 0.0))
    _shade.refresh_woods()
    _shade._apply_woods(1.0)
    var light: PointLight2D = _shade.woods_light
    _check("the woods light is on at night", light.enabled)
    _check("its texture is the map, one pixel per tile",
        light.texture != null and light.texture.get_width() == stub.map_width and light.texture.get_height() == stub.map_height)
    _check("each pixel covers one tile", is_equal_approx(light.texture_scale, NightShade.TILE))
    # The grid's centre under the light puts pixel (x, y) over tile (x, y).
    _check("the light sits on the grid's centre",
        light.position.is_equal_approx(Vector2((20 - 10) * 32.0, (15 - 5) * 32.0)))
    _check("it subtracts", light.blend_mode == Light2D.BLEND_MODE_SUB)
    var img: Image = light.texture.get_image()
    _check("the tree's tile (the grid origin) is dark", is_equal_approx(img.get_pixel(10, 5).a, 1.0))
    _teardown_world(stub)
    _done()

func _test_refresh_with_no_trees_turns_the_woods_off() -> void:
    var stub := _setup_world()
    stub.add_object("w1", "well", Vector2(0.0, 0.0))
    _shade.refresh_woods()
    _shade._apply_woods(1.0)
    _check("no trees means no woods light, even at night", not _shade.woods_light.enabled)
    _teardown_world(stub)
    _done()

func _test_a_moved_tree_rebuilds_the_mask() -> void:
    var stub := _setup_world()
    var tree := stub.add_object("t1", "oak", Vector2(0.0, 0.0))
    _shade.refresh_woods()
    var first: Texture2D = _shade.woods_light.texture
    _shade.refresh_woods()
    _check("an unchanged map keeps the same mask", _shade.woods_light.texture == first)
    tree.position = Vector2(320.0, 0.0)
    _shade.refresh_woods()
    var img: Image = _shade.woods_light.texture.get_image()
    _check("a moved tree builds a new mask", _shade.woods_light.texture != first)
    _check("the dark follows the tree", is_equal_approx(img.get_pixel(20, 5).a, 1.0) and is_zero_approx(img.get_pixel(10, 5).a))
    _teardown_world(stub)
    _done()

func _test_woods_light_subtracts_a_share_of_the_tint() -> void:
    var stub := _setup_world()
    stub.add_object("t1", "oak", Vector2(0.0, 0.0))
    _shade.refresh_woods()
    _shade._apply_woods(1.0)
    var want := Color(0.2, 0.3, 0.4) * NightShade.WOODS_DARKEN
    _check("at night it takes WOODS_DARKEN of the tint",
        _shade.woods_light.color.is_equal_approx(Color(want.r, want.g, want.b)) and is_equal_approx(_shade.woods_light.energy, 1.0))
    _shade._apply_woods(0.0)
    _check("by day the light is off, not just at zero energy", not _shade.woods_light.enabled)
    _shade._apply_woods(1.0)
    _check("and it comes back on at night", _shade.woods_light.enabled)
    _teardown_world(stub)
    _done()

func _test_vision_light_on_own_character_only() -> void:
    var stub := _setup_world()
    var pc := stub.add_npc("pc")
    var other := stub.add_npc("villager")
    _shade._apply_vision(1.0)
    var on_pc := pc.get_children().filter(func(n): return n is PointLight2D)
    var on_other := other.get_children().filter(func(n): return n is PointLight2D)
    _check("the player's own character carries one light", on_pc.size() == 1)
    _check("no other character does", on_other.is_empty())
    _check("full night gives the full energy",
        on_pc.size() == 1 and on_pc[0].enabled and is_equal_approx(on_pc[0].energy, NightShade.VISION_ENERGY))
    _shade._apply_vision(1.0)
    _check("a second frame does not add a second light",
        pc.get_children().filter(func(n): return n is PointLight2D).size() == 1)
    _teardown_world(stub)
    _done()

## code_review: the character leaves the lookup while its old node stands
## (a resync), then a new node replaces it.
func _test_vision_light_follows_a_replaced_character() -> void:
    var stub := _setup_world()
    var old_pc := stub.add_npc("pc")
    _shade._apply_vision(1.0)
    stub.placed_npcs.erase("pc")
    _shade._apply_vision(1.0)
    var on_old := old_pc.get_children().filter(func(n): return n is PointLight2D)
    _check("while the character is out of the lookup, its old light is off",
        on_old.size() == 1 and not on_old[0].enabled)
    var new_pc := stub.add_npc("pc")
    _shade._apply_vision(1.0)
    var lit_new := new_pc.get_children().filter(func(n): return n is PointLight2D and n.enabled)
    var lit_old := old_pc.get_children().filter(func(n): return n is PointLight2D and is_instance_valid(n) and not n.is_queued_for_deletion() and n.enabled)
    _check("the replacement carries the one lit light", lit_new.size() == 1)
    _check("the old node carries no lit light", lit_old.is_empty())
    _teardown_world(stub)
    _done()

func _test_vision_light_off_by_day() -> void:
    var stub := _setup_world()
    stub.add_npc("pc")
    _shade._apply_vision(0.0)
    _check("by day the light is off", _shade.vision_light != null and not _shade.vision_light.enabled)
    _teardown_world(stub)
    _done()

func _test_night_color_replaces_the_last_curve_stop() -> void:
    var world: Node2D = load("res://scripts/world.gd").new()
    world.apply_night_color({"night_color": "#101820"})
    _check("night samples the setting", world._sample_phase_curve(1.0).is_equal_approx(Color.html("#101820")))
    _check("day is unchanged", world._sample_phase_curve(0.0).is_equal_approx(world.DAY_COLOR))
    var last_segment: Color = world._sample_phase_curve(0.9)
    _check("the last segment blends toward the setting, not the old pole",
        last_segment.is_equal_approx(world.BLUE_HOUR_COLOR.lerp(Color.html("#101820"), 0.6)))
    world.free()
    _done()

func _test_bad_night_color_is_ignored() -> void:
    var world: Node2D = load("res://scripts/world.gd").new()
    world.apply_night_color({"night_color": "#101820"})
    world.apply_night_color({"night_color": "midnight"})
    world.apply_night_color({})
    _check("a bad or missing value keeps the last good one",
        world._sample_phase_curve(1.0).is_equal_approx(Color.html("#101820")))
    world.free()
    _done()
