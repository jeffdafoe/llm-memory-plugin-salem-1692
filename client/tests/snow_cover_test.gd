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
    "_test_region_top_bounds_the_cell",
    "_test_animated_sprite_region_follows_its_frame",
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

func _region_top(sprite: Node2D) -> float:
    return float(sprite.get_instance_shader_parameter("region_top"))

## A 32x32 sheet cut into four 16x16 cells, as an AtlasTexture on cell (x, y).
func _cell(sheet: Texture2D, x: int, y: int) -> AtlasTexture:
    var atlas := AtlasTexture.new()
    atlas.atlas = sheet
    atlas.region = Rect2(x * 16, y * 16, 16, 16)
    return atlas

func _depth(sprite: Node2D) -> int:
    if sprite.material == null:
        return 0
    return int(sprite.material.get_shader_parameter("depth"))

# --- tests -----------------------------------------------------------------------

## The shader reads the sheet a sprite is cut from, so each snow sprite carries
## the top of its own cell as region_top (code_review, LLM-850): a cell on the
## sheet's second row of two starts at UV 0.5, a whole texture at 0.
func _test_region_top_bounds_the_cell() -> void:
    var sheet := ImageTexture.create_from_image(Image.create(32, 32, false, Image.FORMAT_RGBA8))
    _check("a lower cell starts half way down the sheet", is_equal_approx(_world.snow_region_top(_cell(sheet, 1, 1)), 0.5))
    _check("a top cell starts at 0", is_equal_approx(_world.snow_region_top(_cell(sheet, 0, 0)), 0.0))
    _check("a whole texture starts at 0", is_equal_approx(_world.snow_region_top(sheet), 0.0))

    _world.set_snow_tier("light")
    var sprite: Node2D = _world._create_sprite_node(_static_state(), _cell(sheet, 0, 1), 0.5, 0.85, 2.0, PLAIN_ASSET)
    _world.add_child(sprite)
    _check("a snowy sprite carries its cell's top", is_equal_approx(_region_top(sprite), 0.5))
    _world.set_snow_tier("none")
    sprite.free()
    _done()

## An animated snow sprite whose frames sit on different rows of the sheet
## moves its region_top with the frame.
func _test_animated_sprite_region_follows_its_frame() -> void:
    var sheet := ImageTexture.create_from_image(Image.create(32, 32, false, Image.FORMAT_RGBA8))
    var frames := SpriteFrames.new()
    frames.add_frame("default", _cell(sheet, 0, 1))
    frames.add_frame("default", _cell(sheet, 1, 0))
    var anim := AnimatedSprite2D.new()
    anim.sprite_frames = frames
    anim.add_to_group(_world.SNOW_GROUP)
    anim.frame_changed.connect(_world._on_snow_sprite_frame.bind(anim))
    _world.add_child(anim)

    _world.set_snow_tier("deep")
    _check("frame 0 on the second row: 0.5", is_equal_approx(_region_top(anim), 0.5))
    anim.frame = 1
    _check("frame 1 on the first row: 0", is_equal_approx(_region_top(anim), 0.0))
    anim.frame = 0
    _check("back to frame 0: 0.5", is_equal_approx(_region_top(anim), 0.5))
    _world.set_snow_tier("none")
    anim.free()
    _done()

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
