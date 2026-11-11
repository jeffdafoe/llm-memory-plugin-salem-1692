extends Node2D
## Custom terrain renderer — draws the ground every frame as layered corner
## autotiles, with a little overlap to prevent sub-pixel seams. Replaces
## TileMapLayer, which caches tiles and produces visible gaps at fractional
## zoom levels.
##
## Each drawn tile sits on the corner where four map cells meet (a dual grid),
## so its four corners are those cells' terrains. The terrains are layers,
## bottom to top in the pack's LAYER_ORDER. A tile draws the full tile of its
## lowest corner terrain, then one edge tile for each higher layer, picked by a
## 4-bit mask of which corners are that layer or above (TL=1, TR=2, BR=4, BL=8).
## Any mix of terrains draws with one edge set per terrain, so a pack does not
## need a tile for every pair of terrains (LLM-967).
##
## Only draws tiles visible in the current viewport (culling).

const TerrainPack = preload("res://scripts/terrain_pack.gd")

# Rendered tile size in world space
const TILE_SIZE: int = 32
# Minimum screen pixels of overlap to prevent seams at any zoom level
const MIN_SCREEN_OVERLAP: float = 1.5

# The ground sheet being drawn — the season's (LLM-834) or the snowy one.
var ground_texture: Texture2D = null
var _sheet_cache: Dictionary = {}

# Water sparkle overlay. Each water tile cycles through every frame with a
# per-tile offset, so the tiles do not glint in step.
var sparkle_texture: Texture2D = null
var _sparkle_frame: int = 0
var _sparkle_timer: float = 0.0
var _sparkle_frames: int = TerrainPack.SPARKLE_COLS * TerrainPack.SPARKLE_ROWS

# Layer rank per terrain id (index = terrain id); -1 for an id the pack does
# not draw. Built from TerrainPack.LAYER_ORDER by _prepare_pack.
var _rank_of: PackedInt32Array = PackedInt32Array()

# Sheet rects per drawn tile (index y * map_width + x), bottom to top; null
# until the tile is first drawn. They depend only on the map, so they are
# worked out once, not each frame: rebuild() after a new map, refresh_cell()
# after one cell is painted.
var _tile_rects: Array = []
# Sparkle frame offset per drawn tile; -1 where the tile does not sparkle.
# Set with the tile's rects.
var _sparkle_offset: PackedInt32Array = PackedInt32Array()
# Layer rank per map cell (index y * map_width + x), read from map_data.
var _ranks: PackedInt32Array = PackedInt32Array()
# Per rank, from the pack: the full-tile rects, the 16 edge rects by mask (null
# where the pack draws none), and 1 where the terrain sparkles.
var _fill_rects: Array = []
var _edge_rects: Array = []
var _rank_sparkles: PackedByteArray = PackedByteArray()

# Map data reference — set by world.gd via _sync_renderer
var map_data: Array = []
var map_width: int = 200
var map_height: int = 180

# Tile offset (array index to world position); overwritten from world.gd
var pad_x: int = 0
var pad_y: int = 0

func _ready() -> void:
    if ground_texture == null:
        ground_texture = _sheet(ground_sheet_path(""))
    if ResourceLoader.exists(TerrainPack.SPARKLE_SHEET):
        sparkle_texture = load(TerrainPack.SPARKLE_SHEET)

## Layer rank per terrain id, from the pack's LAYER_ORDER.
static func rank_table() -> PackedInt32Array:
    var top_id: int = 0
    for terrain in TerrainPack.LAYER_ORDER:
        top_id = maxi(top_id, terrain)
    var table := PackedInt32Array()
    table.resize(top_id + 1)
    table.fill(-1)
    for rank in TerrainPack.LAYER_ORDER.size():
        table[TerrainPack.LAYER_ORDER[rank]] = rank
    return table

## The ground sheet for season. snowy (snow lies light or deep) is the snowy
## sheet whatever the season; a season the pack does not name draws summer.
static func ground_sheet_path(season: String, snowy: bool = false) -> String:
    if snowy and TerrainPack.SHEETS.has("snowy"):
        return TerrainPack.SHEETS["snowy"]
    return TerrainPack.SHEETS.get(season, TerrainPack.SHEETS["summer"])

## Draw the ground for season (the world read's "autumn"), or snowy.
func set_season(season: String, snowy: bool = false) -> void:
    var texture := _sheet(ground_sheet_path(season, snowy))
    if texture != null:
        ground_texture = texture

func _sheet(path: String) -> Texture2D:
    if not _sheet_cache.has(path):
        _sheet_cache[path] = load(path)
    return _sheet_cache[path]

## The tiles that draw one corner, bottom to top, from the layer ranks of its
## four corner cells. Each entry is [rank, mask]; the first is the full tile of
## the lowest rank (mask 15). A layer whose mask equals the next layer's is
## left out: then no corner is that terrain, and an absent terrain must not
## draw a band between the ones that are there.
static func corner_layers(tl: int, tr: int, br: int, bl: int) -> Array:
    var low: int = mini(mini(tl, tr), mini(br, bl))
    var high: int = maxi(maxi(tl, tr), maxi(br, bl))
    var layers: Array = [[low, 15]]
    for rank in range(low + 1, high + 1):
        var mask: int = corner_mask(rank, tl, tr, br, bl)
        var above: int = corner_mask(rank + 1, tl, tr, br, bl) if rank < high else 0
        if mask != above:
            layers.append([rank, mask])
    return layers

## Which corners are at rank or above (TL=1, TR=2, BR=4, BL=8).
static func corner_mask(rank: int, tl: int, tr: int, br: int, bl: int) -> int:
    var mask: int = 0
    if tl >= rank:
        mask |= 1
    if tr >= rank:
        mask |= 2
    if br >= rank:
        mask |= 4
    if bl >= rank:
        mask |= 8
    return mask

## Source cell in the sheet for a layer at a corner, or (-1, -1) when the pack
## has no tile for it. Full tiles vary by map position, the same each frame.
static func source_cell(terrain: int, mask: int, x: int, y: int) -> Vector2i:
    if mask == 15:
        var fills: Array = TerrainPack.FILLS.get(terrain, [])
        if fills.is_empty():
            return Vector2i(-1, -1)
        var hash: int = absi(((x * 16807) + (y * 48271)) % 2147483647)
        return fills[hash % fills.size()]
    var edges: Array = TerrainPack.EDGES.get(terrain, [])
    if mask >= edges.size():
        return Vector2i(-1, -1)
    return edges[mask]

## Read map_data afresh. Call after the map or its size changes. Each tile is
## worked out the first time it is drawn, so a load does not stall on the
## whole map.
func rebuild() -> void:
    _prepare_pack()
    var count: int = map_width * map_height
    _ranks.resize(count)
    _tile_rects.resize(count)
    _tile_rects.fill(null)
    _sparkle_offset.resize(count)
    for y in map_height:
        var row: Array = map_data[y]
        for x in map_width:
            _ranks[y * map_width + x] = _rank_for(row[x])

## Redo the four tiles that have map cell (x, y) as a corner. Call after the
## cell is painted.
func refresh_cell(x: int, y: int) -> void:
    if _tile_rects.size() != map_width * map_height:
        rebuild()
        return
    _ranks[y * map_width + x] = _rank_for(map_data[y][x])
    for ty in range(y, mini(y + 2, map_height)):
        for tx in range(x, mini(x + 2, map_width)):
            _tile_rects[ty * map_width + tx] = null

## The sheet rects of tile (x, y), bottom to top, worked out on first use.
func tile_rects(x: int, y: int) -> Array:
    var index: int = y * map_width + x
    if _tile_rects[index] == null:
        _build_tile(x, y)
    return _tile_rects[index]

## The pack's tiles as sheet rects per rank, so building a tile does no
## dictionary lookups. Built once.
func _prepare_pack() -> void:
    if not _fill_rects.is_empty():
        return
    _rank_of = rank_table()
    var src_size: int = TerrainPack.SRC_TILE_SIZE
    _rank_sparkles.resize(TerrainPack.LAYER_ORDER.size())
    for terrain in TerrainPack.LAYER_ORDER:
        var fills: Array = []
        for cell in TerrainPack.FILLS.get(terrain, []):
            fills.append(Rect2(cell.x * src_size, cell.y * src_size, src_size, src_size))
        _fill_rects.append(fills)
        var edges: Array = []
        edges.resize(16)
        for mask in range(1, 15):
            var cell: Vector2i = source_cell(terrain, mask, 0, 0)
            if cell.x >= 0:
                edges[mask] = Rect2(cell.x * src_size, cell.y * src_size, src_size, src_size)
        _edge_rects.append(edges)
        _rank_sparkles[_rank_of[terrain]] = 1 if TerrainPack.SPARKLE_TERRAINS.has(terrain) else 0

## Layer rank of a terrain id. An id the pack does not draw takes the lowest
## layer, so the cell still shows ground.
func _rank_for(terrain: int) -> int:
    if terrain < 0 or terrain >= _rank_of.size() or _rank_of[terrain] < 0:
        return 0
    return _rank_of[terrain]

func _build_tile(x: int, y: int) -> void:
    # The corners are the cells up and left of the tile; the map edge repeats.
    var left: int = maxi(x - 1, 0)
    var up_row: int = maxi(y - 1, 0) * map_width
    var row: int = y * map_width
    var tl: int = _ranks[up_row + left]
    var tr: int = _ranks[up_row + x]
    var br: int = _ranks[row + x]
    var bl: int = _ranks[row + left]
    var rects: Array = []
    # Most corners are one terrain: one full tile, no layer list.
    if tl == tr and tr == br and br == bl:
        var fills: Array = _fill_rects[tl]
        if not fills.is_empty():
            # Same position hash as source_cell.
            rects.append(fills[absi(((x * 16807) + (y * 48271)) % 2147483647) % fills.size()])
    else:
        for layer in corner_layers(tl, tr, br, bl):
            if layer[1] == 15:
                var fills: Array = _fill_rects[layer[0]]
                if not fills.is_empty():
                    rects.append(fills[absi(((x * 16807) + (y * 48271)) % 2147483647) % fills.size()])
            elif _edge_rects[layer[0]][layer[1]] != null:
                rects.append(_edge_rects[layer[0]][layer[1]])
    _tile_rects[row + x] = rects
    if _rank_sparkles[br] == 1:
        # Per-tile frame offset so tiles don't all animate in sync
        _sparkle_offset[row + x] = absi(((x * 31337) + (y * 65539)) % 2147483647) % _sparkle_frames
    else:
        _sparkle_offset[row + x] = -1

func _process(delta: float) -> void:
    # Advance sparkle animation timer
    _sparkle_timer += delta
    if _sparkle_timer >= TerrainPack.SPARKLE_FRAME_SECONDS:
        _sparkle_timer -= TerrainPack.SPARKLE_FRAME_SECONDS
        _sparkle_frame = (_sparkle_frame + 1) % _sparkle_frames
    # Redraw every frame so tiles are always at correct screen positions
    queue_redraw()

func _draw() -> void:
    if ground_texture == null or map_data.is_empty():
        return

    # Get the visible rect in world coordinates
    var viewport: Viewport = get_viewport()
    var canvas_transform: Transform2D = viewport.get_canvas_transform()
    var viewport_size: Vector2 = viewport.get_visible_rect().size
    var inv: Transform2D = canvas_transform.affine_inverse()

    var top_left: Vector2 = inv * Vector2.ZERO
    var bottom_right: Vector2 = inv * viewport_size

    # Calculate overlap in world pixels based on current zoom
    # At low zoom, we need more world pixels to achieve 1+ screen pixel overlap
    var zoom: float = canvas_transform.get_scale().x
    var overlap: float = MIN_SCREEN_OVERLAP / zoom

    # Convert to tile coordinates (with some margin for overlap)
    var start_x: int = clampi(int(floor(top_left.x / TILE_SIZE)) + pad_x - 1, 0, map_width - 1)
    var start_y: int = clampi(int(floor(top_left.y / TILE_SIZE)) + pad_y - 1, 0, map_height - 1)
    var end_x: int = clampi(int(ceil(bottom_right.x / TILE_SIZE)) + pad_x + 1, 0, map_width - 1)
    var end_y: int = clampi(int(ceil(bottom_right.y / TILE_SIZE)) + pad_y + 1, 0, map_height - 1)

    if _tile_rects.size() != map_width * map_height:
        rebuild()

    var src_size: int = TerrainPack.SRC_TILE_SIZE

    # Draw visible tiles with zoom-adjusted overlap. This also builds any
    # tile not yet worked out, so the sparkle offsets below are set.
    for y in range(start_y, end_y + 1):
        var world_y: float = (y - pad_y) * TILE_SIZE
        for x in range(start_x, end_x + 1):
            var dst_rect: Rect2 = Rect2((x - pad_x) * TILE_SIZE, world_y, TILE_SIZE + overlap, TILE_SIZE + overlap)
            var index: int = y * map_width + x
            # tile_rects(x, y) inlined: a call per tile costs real frame time.
            if _tile_rects[index] == null:
                _build_tile(x, y)
            for src_rect in _tile_rects[index]:
                draw_texture_rect_region(ground_texture, dst_rect, src_rect)

    # Draw water sparkle overlays on water tiles
    if sparkle_texture != null:
        for y in range(start_y, end_y + 1):
            var world_y: float = (y - pad_y) * TILE_SIZE
            for x in range(start_x, end_x + 1):
                var offset: int = _sparkle_offset[y * map_width + x]
                if offset < 0:
                    continue
                var frame: int = (_sparkle_frame + offset) % _sparkle_frames
                var col: int = frame % TerrainPack.SPARKLE_COLS
                var row: int = frame / TerrainPack.SPARKLE_COLS
                var spark_src: Rect2 = Rect2(col * src_size, row * src_size, src_size, src_size)
                # No overlap on sparkles — they're transparent overlays,
                # overlap would stretch dots into neighboring grass tiles
                var spark_dst: Rect2 = Rect2((x - pad_x) * TILE_SIZE, world_y, TILE_SIZE, TILE_SIZE)
                draw_texture_rect_region(sparkle_texture, spark_dst, spark_src)
