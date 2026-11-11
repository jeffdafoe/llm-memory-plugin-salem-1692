extends SceneTree

## Headless harness for LLM-967: the layered corner autotiles of the terrain
## renderer, and the ground pack it reads.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/terrain_renderer_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_rank_table_follows_the_layer_order",
    "_test_one_terrain_is_one_full_tile",
    "_test_two_terrains_lay_the_upper_edge",
    "_test_a_covered_layer_is_left_out",
    "_test_three_terrains_nest",
    "_test_full_tiles_vary_by_position",
    "_test_every_pack_cell_is_on_its_sheet",
    "_test_a_painted_cell_redoes_its_four_tiles",
    "_test_a_same_size_map_rebuilds_every_tile",
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


func _test_rank_table_follows_the_layer_order() -> void:
    var table: PackedInt32Array = _terrain.rank_table()
    for rank in _pack.LAYER_ORDER.size():
        _check("rank of terrain %d" % _pack.LAYER_ORDER[rank], table[_pack.LAYER_ORDER[rank]], rank)
    _check("id 0 is not a layer", table[0], -1)
    _done()


func _test_one_terrain_is_one_full_tile() -> void:
    _check("all rank 2", _terrain.corner_layers(2, 2, 2, 2), [[2, 15]])
    _done()


# Masks: TL=1, TR=2, BR=4, BL=8.
func _test_two_terrains_lay_the_upper_edge() -> void:
    _check("top row above", _terrain.corner_layers(3, 3, 1, 1), [[1, 15], [3, 3]])
    _check("one corner above", _terrain.corner_layers(1, 1, 4, 1), [[1, 15], [4, 4]])
    _check("diagonal above", _terrain.corner_layers(5, 0, 5, 0), [[0, 15], [5, 5]])
    _done()


# Ranks 1 to 3 have the same mask as rank 4 (only the rank-4 corners reach
# them), so the rank-4 edge covers them exactly and they are not drawn.
func _test_a_covered_layer_is_left_out() -> void:
    _check("gap of three ranks", _terrain.corner_layers(0, 4, 4, 0), [[0, 15], [4, 6]])
    _done()


# A middle layer reaches more corners than the top one, so it draws under it.
func _test_three_terrains_nest() -> void:
    _check("0, 3, 4, 3", _terrain.corner_layers(0, 3, 4, 3), [[0, 15], [3, 14], [4, 4]])
    _check("lowest not at TL", _terrain.corner_layers(4, 2, 1, 2), [[1, 15], [2, 11], [4, 1]])
    _done()


func _test_full_tiles_vary_by_position() -> void:
    var terrain: int = _pack.LAYER_ORDER[0]
    var fills: Array = _pack.FILLS[terrain]
    var seen := {}
    for y in 20:
        for x in 20:
            var cell: Vector2i = _terrain.source_cell(terrain, 15, x, y)
            _check("fill at %d,%d is a listed fill" % [x, y], fills.has(cell), true)
            seen[cell] = true
    _check("more than one fill in a 20x20 patch", seen.size() > 1, true)
    _check("same cell each call", _terrain.source_cell(terrain, 15, 7, 9), _terrain.source_cell(terrain, 15, 7, 9))
    _done()


# Every season's sheet loads, all share one layout, and every edge and full
# tile the pack names lies inside it.
func _test_every_pack_cell_is_on_its_sheet() -> void:
    var size := Vector2i.ZERO
    for season in _pack.SHEETS:
        var texture: Texture2D = load(_pack.SHEETS[season])
        _check("%s sheet loads" % season, texture != null, true)
        if texture == null:
            continue
        if size == Vector2i.ZERO:
            size = Vector2i(texture.get_width(), texture.get_height())
        _check("%s sheet has the summer layout" % season, Vector2i(texture.get_width(), texture.get_height()), size)
    var cols: int = size.x / _pack.SRC_TILE_SIZE
    var rows: int = size.y / _pack.SRC_TILE_SIZE
    for terrain in _pack.LAYER_ORDER:
        var edges: Array = _pack.EDGES.get(terrain, [])
        _check("terrain %d lists 16 masks" % terrain, edges.size(), 16)
        for mask in range(1, 15):
            var cell: Vector2i = edges[mask]
            _check("terrain %d mask %d on the sheet" % [terrain, mask], cell.x >= 0 and cell.x < cols and cell.y >= 0 and cell.y < rows, true)
        var fills: Array = _pack.FILLS.get(terrain, [])
        _check("terrain %d has a full tile" % terrain, fills.is_empty(), false)
        for cell in fills:
            _check("terrain %d fill %s on the sheet" % [terrain, cell], cell.x >= 0 and cell.x < cols and cell.y >= 0 and cell.y < rows, true)
    var sparkle: Texture2D = load(_pack.SPARKLE_SHEET)
    _check("sparkle sheet loads", sparkle != null, true)
    if sparkle != null:
        _check("sparkle sheet holds every frame", Vector2i(sparkle.get_width(), sparkle.get_height()),
            Vector2i(_pack.SPARKLE_COLS * _pack.SRC_TILE_SIZE, _pack.SPARKLE_ROWS * _pack.SRC_TILE_SIZE))
    _done()


# A renderer with a 5x5 map of one terrain, its tiles built. Out of the tree:
# _ready never runs. The caller frees it.
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


# Every tile's rects and sparkle offset, built on demand.
func _all_tiles(renderer: Node2D) -> Array:
    var tiles: Array = []
    for y in 5:
        for x in 5:
            tiles.append([renderer.tile_rects(x, y), renderer._sparkle_offset[y * 5 + x]])
    return tiles


# Water painted into the middle of a grass field changes the four tiles that
# have that cell as a corner; after refresh_cell every tile must match a fresh
# renderer, and the four tiles must really have changed (or the check proves
# nothing).
func _test_a_painted_cell_redoes_its_four_tiles() -> void:
    var map_data := _uniform_map(2)
    var painted := _renderer_on(map_data)
    var before: Array = _all_tiles(painted)
    map_data[2][2] = 6
    painted.refresh_cell(2, 2)
    var after: Array = _all_tiles(painted)
    var fresh := _renderer_on(map_data)
    _check("refresh equals a fresh renderer", after, _all_tiles(fresh))
    for tile in [Vector2i(2, 2), Vector2i(3, 2), Vector2i(2, 3), Vector2i(3, 3)]:
        var index: int = tile.y * 5 + tile.x
        _check("tile %s changed" % tile, after[index] != before[index], true)
        _check("tile %s has a full tile and an edge" % tile, after[index][0].size(), 2)
    _check("the water corner sparkles", after[2 * 5 + 2][1] >= 0, true)
    _check("a tile off the cell is unchanged", after[0], before[0])
    painted.free()
    fresh.free()
    _done()


# Another client's terrain save reloads a map of the same size into map_data,
# then world.gd calls rebuild (code_review, LLM-967). The cache size still
# matches, so only rebuild can drop the old tiles: after it, every tile must
# match a fresh renderer's, and differ from the old map's.
func _test_a_same_size_map_rebuilds_every_tile() -> void:
    var map_data := _uniform_map(2)
    var renderer := _renderer_on(map_data)
    var before: Array = _all_tiles(renderer)
    for y in 5:
        for x in 5:
            map_data[y][x] = 5 if (x + y) % 2 == 0 else 1
    renderer.rebuild()
    var after: Array = _all_tiles(renderer)
    var fresh := _renderer_on(map_data)
    _check("rebuild equals a fresh renderer", after, _all_tiles(fresh))
    _check("the far corner tile changed", after[24] != before[24], true)
    renderer.free()
    fresh.free()
    _done()
