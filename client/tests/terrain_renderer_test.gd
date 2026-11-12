extends SceneTree

## Headless harness for LLM-967: the terrain renderer's map ranks and shader
## uniforms, and the ground pack it reads. The headless renderer draws nothing,
## so the shader's pixels are checked by hand in a windowed render.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/terrain_renderer_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_rank_table_follows_the_layer_order",
    "_test_rank_bits_mark_the_named_terrains",
    "_test_rebuild_writes_a_rank_per_cell",
    "_test_a_painted_cell_changes_one_rank",
    "_test_a_cell_painted_before_the_tree_still_uploads",
    "_test_a_same_size_map_rebuilds_every_rank",
    "_test_snow_softens_the_land_edges",
    "_test_the_pack_files_fit_its_layout",
]

var _terrain: GDScript = null
var _pack: GDScript = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _terrain = load("res://scripts/terrain_renderer.gd")
    _pack = load("res://scripts/terrain_pack.gd")
    _check_test_list()
    for t in TESTS:
        _current = t
        await call(t)
    _check_all_tests_ran()
    print("\n[terrain_renderer_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[terrain_renderer_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


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


## A renderer on map_data (5 wide, 5 high), its ranks written. Out of the
## tree: _ready never runs. The caller frees it.
func _renderer_on(map_data: Array) -> Node2D:
    var renderer: Node2D = _terrain.new()
    renderer.map_data = map_data
    renderer.map_width = 5
    renderer.map_height = 5
    renderer.rebuild()
    return renderer


func _uniform_map(terrain: int) -> Array:
    var map_data: Array = []
    for y in 5:
        var row: Array = []
        row.resize(5)
        row.fill(terrain)
        map_data.append(row)
    return map_data


func _ranks(renderer: Node2D) -> Array:
    var out: Array = []
    for y in 5:
        for x in 5:
            out.append(renderer._ranks_image.get_pixel(x, y).r8)
    return out


func _rank(terrain: int) -> int:
    return _pack.LAYER_ORDER.find(terrain)


func _test_rank_table_follows_the_layer_order() -> void:
    var table: PackedInt32Array = _terrain.rank_table()
    for rank in _pack.LAYER_ORDER.size():
        _check("rank of terrain %d" % _pack.LAYER_ORDER[rank], table[_pack.LAYER_ORDER[rank]], rank)
    _check("id 0 is not a layer", table[0], -1)
    _done()


func _test_rank_bits_mark_the_named_terrains() -> void:
    var bits: int = _terrain.rank_bits([2, 3] as Array[int])
    for rank in _pack.LAYER_ORDER.size():
        var named: bool = _pack.LAYER_ORDER[rank] in [2, 3]
        _check("rank %d bit" % rank, (bits >> rank) & 1 == 1, named)
    _check("no terrains, no bits", _terrain.rank_bits([] as Array[int]), 0)
    _done()


func _test_rebuild_writes_a_rank_per_cell() -> void:
    var map_data := _uniform_map(2)
    map_data[0][0] = 6
    map_data[1][3] = 4
    map_data[4][4] = 9
    var renderer := _renderer_on(map_data)
    var ranks := _ranks(renderer)
    _check("deep water", ranks[0], _rank(6))
    _check("cobble", ranks[1 * 5 + 3], _rank(4))
    _check("light grass", ranks[2 * 5 + 2], _rank(2))
    _check("an id the pack does not draw takes the lowest layer", ranks[24], 0)
    var material: ShaderMaterial = renderer.material
    _check("the shader reads the ranks", material.get_shader_parameter("ranks"), renderer._ranks_texture)
    _check("map size", material.get_shader_parameter("map_size"), Vector2i(5, 5))
    renderer.free()
    _done()


# A painted cell changes one texel and asks for one upload on the next frame,
# then the renderer stops processing.
func _test_a_painted_cell_changes_one_rank() -> void:
    var map_data := _uniform_map(2)
    var renderer := _renderer_on(map_data)
    _check("idle after rebuild", renderer.is_processing(), false)
    var before := _ranks(renderer)
    map_data[2][3] = 6
    renderer.refresh_cell(3, 2)
    var after := _ranks(renderer)
    _check("the painted cell", after[2 * 5 + 3], _rank(6))
    after[2 * 5 + 3] = before[2 * 5 + 3]
    _check("no other cell changed", after, before)
    _check("an upload waits for the next frame", renderer.is_processing(), true)
    renderer._process(0.0)
    _check("idle after the upload", renderer.is_processing(), false)
    renderer.free()
    _done()


# A cell painted before the renderer enters the tree (code_review, LLM-967):
# entering the tree must keep the pending upload, and the next frame must put
# the edit in the texture the shader reads, not only in the CPU image.
func _test_a_cell_painted_before_the_tree_still_uploads() -> void:
    var map_data := _uniform_map(2)
    var renderer := _renderer_on(map_data)
    map_data[1][1] = 6
    renderer.refresh_cell(1, 1)
    root.add_child(renderer)
    _check("still waiting to upload in the tree", renderer.is_processing(), true)
    await process_frame
    await process_frame
    _check("uploaded", renderer.is_processing(), false)
    # The headless renderer ignores texture updates, so the texture's content
    # is checked only in a windowed run (by hand); CI checks the flag cycle.
    if DisplayServer.get_name() != "headless":
        var uploaded: Image = renderer._ranks_texture.get_image()
        _check("the texture holds the painted rank", uploaded.get_pixel(1, 1).r8, _rank(6))
    renderer.queue_free()
    await process_frame
    _done()


# Another client's terrain save reloads a map of the same size into map_data,
# then world.gd calls rebuild. Every rank must follow the new map.
func _test_a_same_size_map_rebuilds_every_rank() -> void:
    var map_data := _uniform_map(2)
    var renderer := _renderer_on(map_data)
    for y in 5:
        for x in 5:
            map_data[y][x] = 5 if (x + y) % 2 == 0 else 1
    renderer.rebuild()
    var fresh := _renderer_on(map_data)
    _check("rebuild equals a fresh renderer", _ranks(renderer), _ranks(fresh))
    _check("the corner changed", _ranks(renderer)[24], _rank(5))
    renderer.free()
    fresh.free()
    _done()


# Under snow the land types look alike, so dirt joins the grasses in meeting
# with no shadow or rim; the default keeps dirt's edges.
func _test_snow_softens_the_land_edges() -> void:
    var renderer := _renderer_on(_uniform_map(2))
    var material: ShaderMaterial = renderer.material
    var dirt_bit: int = 1 << _rank(1)
    renderer.set_season("summer")
    _check("summer: dirt keeps its edges", material.get_shader_parameter("soft_ranks") & dirt_bit, 0)
    _check("summer atlas", material.get_shader_parameter("ground"), load(_pack.SHEETS["summer"]))
    renderer.set_season("autumn", true)
    _check("snow: dirt edges soft", material.get_shader_parameter("soft_ranks") & dirt_bit, dirt_bit)
    _check("snowy atlas", material.get_shader_parameter("ground"), load(_pack.SHEETS["snowy"]))
    renderer.set_season("winter")
    _check("winter without snow: dirt keeps its edges", material.get_shader_parameter("soft_ranks") & dirt_bit, 0)
    renderer.free()
    _done()


# Every season's atlas holds a texture per layer, the noise wraps with the
# textures, and the sparkle sheet holds every frame.
func _test_the_pack_files_fit_its_layout() -> void:
    var size: int = _pack.TEXTURE_PX
    var rows: int = (_pack.LAYER_ORDER.size() + _pack.ATLAS_COLS - 1) / _pack.ATLAS_COLS
    for season in _pack.SHEETS:
        var atlas: Texture2D = load(_pack.SHEETS[season])
        _check("%s atlas loads" % season, atlas != null, true)
        if atlas != null:
            _check("%s atlas size" % season, Vector2i(atlas.get_width(), atlas.get_height()), Vector2i(_pack.ATLAS_COLS * size, rows * size))
    var noise: Texture2D = load(_pack.EDGE_NOISE)
    _check("edge noise wraps with the textures", Vector2i(noise.get_width(), noise.get_height()), Vector2i(size, size))
    var sparkle: Texture2D = load(_pack.SPARKLE_SHEET)
    _check("sparkle sheet holds every frame", Vector2i(sparkle.get_width(), sparkle.get_height()),
        Vector2i(_pack.SPARKLE_COLS * _pack.TILE_PX, _pack.SPARKLE_ROWS * _pack.TILE_PX))
    _check("tile size matches the renderer", _pack.TILE_PX, _terrain.TILE_SIZE)
    _check("a default soft-edge set", _pack.SOFT_EDGE_TERRAINS.has("default"), true)
    _done()
