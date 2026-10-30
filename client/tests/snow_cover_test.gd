extends SceneTree

## Headless regression harness for LLM-850 — the snow on the top edges of
## placed objects. What it pins:
##
##  1. Which sprites take the snow: an asset with no season art (a house, a
##     fence, a Tiny Swords pine, a crop) does; an asset with season art (a
##     maple, a bush) shows snow through its own snowy states and does not.
##  2. The material follows the tier: none while no snow lies (no shader cost
##     out of snow), one shared material at a depth per tier while it does, on
##     sprites placed before AND after the tier changed.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/snow_cover_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## world.gd is added to the tree (the snow group is a SceneTree group, so
## get_nodes_in_group only sees nodes inside it); its _ready only sets api_base.

const TESTS := [
    "_test_season_art_detection",
    "_test_only_assets_without_season_art_join_the_group",
    "_test_material_follows_the_tier",
]

var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

func _initialize() -> void:
    _world = load("res://scripts/world.gd").new()
    root.add_child(_world)

## First frame: the world is in the tree.
func _process(_delta: float) -> bool:
    _check("harness — world entered the tree", _world.is_inside_tree())
    _check_test_list()
    _run_all()
    _world.queue_free()
    _check_all_tests_ran()
    print("\n[snow_cover_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[snow_cover_test] ALL PASS")
    quit(1 if _failures > 0 else 0)
    return true

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

## Same harness contract as asset_render_scale_test.gd (LLM-480): a runtime
## error aborts only the function it happens in, so every test calls _done()
## as its last statement and the harness asserts each one reached it.
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

# --- fixtures --------------------------------------------------------------------

const PLAIN_ASSET := {"id": "house", "states": [{"state": "default", "tags": []}]}
const SEASON_ASSET := {"id": "maple", "states": [
    {"state": "default", "tags": []},
    {"state": "winter-snowy", "tags": ["season-winter-snowy"]},
]}

func _static_state() -> Dictionary:
    return {"state": "default", "sheet": "test_sheet.png", "src_x": 0, "src_y": 0,
            "src_w": 32, "src_h": 32, "frame_count": 1, "frame_rate": 0}

func _make_atlas() -> AtlasTexture:
    var atlas := AtlasTexture.new()
    atlas.atlas = ImageTexture.create_from_image(Image.create(64, 32, false, Image.FORMAT_RGBA8))
    atlas.region = Rect2(0, 0, 32, 32)
    return atlas

## A placed sprite for asset, parented under the world so it is in the tree.
func _sprite_for(asset: Dictionary) -> Node2D:
    var sprite: Node2D = _world._create_sprite_node(_static_state(), _make_atlas(), 0.5, 0.85, 2.0, asset)
    _world.add_child(sprite)
    return sprite

func _depth(sprite: Node2D) -> int:
    if sprite.material == null:
        return 0
    return int(sprite.material.get_shader_parameter("depth"))

# --- tests -----------------------------------------------------------------------

func _test_season_art_detection() -> void:
    _check("a plain asset has no season art", not _world.asset_has_season_art(PLAIN_ASSET))
    _check("an asset with a season- state tag has season art", _world.asset_has_season_art(SEASON_ASSET))
    _check("an asset with no states has no season art", not _world.asset_has_season_art({}))
    _done()

func _test_only_assets_without_season_art_join_the_group() -> void:
    _world.set_snow_tier("none")
    var plain := _sprite_for(PLAIN_ASSET)
    var season := _sprite_for(SEASON_ASSET)
    var bare: Node2D = _world._create_sprite_node(_static_state(), _make_atlas(), 0.5, 0.85, 2.0)
    _check("a plain asset's sprite takes the snow", plain.is_in_group(_world.SNOW_GROUP))
    _check("a season-art asset's sprite does not", not season.is_in_group(_world.SNOW_GROUP))
    _check("a sprite made with no asset does not", not bare.is_in_group(_world.SNOW_GROUP))
    _check("no snow: no material", plain.material == null)
    plain.free()
    season.free()
    bare.free()
    _done()

func _test_material_follows_the_tier() -> void:
    _world.set_snow_tier("none")
    var before := _sprite_for(PLAIN_ASSET)
    var season := _sprite_for(SEASON_ASSET)

    for c in [["dusting", 1], ["light", 2], ["deep", 3], ["none", 0], ["bogus", 0]]:
        _world.set_snow_tier(c[0])
        _check("tier %s: depth %d on a sprite placed before" % c, _depth(before) == c[1])
        _check("tier %s: a season-art sprite stays bare" % c[0], season.material == null)

    _world.set_snow_tier("light")
    var after := _sprite_for(PLAIN_ASSET)
    _check("a sprite placed under snow takes it at once", _depth(after) == 2)
    _check("both share the one material", after.material == before.material)
    _check("the material is the snow shader",
        after.material is ShaderMaterial and after.material.shader.resource_path.ends_with("snow_cover.gdshader"))

    _world.set_snow_tier("none")
    _check("the snow melting drops the material from every sprite", before.material == null and after.material == null)
    before.free()
    season.free()
    after.free()
    _done()
