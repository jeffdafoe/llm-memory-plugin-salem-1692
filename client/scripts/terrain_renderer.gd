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
# not draw. Built from TerrainPack.LAYER_ORDER.
var _rank_of: PackedInt32Array = PackedInt32Array()

# Map data reference — set by world.gd via _sync_renderer
var map_data: Array = []
var map_width: int = 200
var map_height: int = 180

# Tile offset (array index to world position); overwritten from world.gd
var pad_x: int = 0
var pad_y: int = 0

func _ready() -> void:
    _rank_of = rank_table()
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
## left out: the layer above covers it exactly, so drawing it would only cost
## a draw (or, with a pack whose edges differ per terrain, show a sliver).
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

    var src_size: int = TerrainPack.SRC_TILE_SIZE

    # Draw visible tiles with zoom-adjusted overlap
    for y in range(start_y, end_y + 1):
        for x in range(start_x, end_x + 1):
            var world_x: float = (x - pad_x) * TILE_SIZE
            var world_y: float = (y - pad_y) * TILE_SIZE
            var dst_rect: Rect2 = Rect2(world_x, world_y, TILE_SIZE + overlap, TILE_SIZE + overlap)
            var tl: int = _get_rank(x - 1, y - 1)
            var tr: int = _get_rank(x, y - 1)
            var br: int = _get_rank(x, y)
            var bl: int = _get_rank(x - 1, y)
            # Most corners are one terrain: one full tile, no layer list.
            if tl == tr and tr == br and br == bl:
                var fill: Vector2i = source_cell(TerrainPack.LAYER_ORDER[tl], 15, x, y)
                if fill.x >= 0:
                    draw_texture_rect_region(ground_texture, dst_rect, Rect2(fill.x * src_size, fill.y * src_size, src_size, src_size))
                continue
            for layer in corner_layers(tl, tr, br, bl):
                var terrain: int = TerrainPack.LAYER_ORDER[layer[0]]
                var cell: Vector2i = source_cell(terrain, layer[1], x, y)
                if cell.x < 0:
                    continue
                var src_rect: Rect2 = Rect2(cell.x * src_size, cell.y * src_size, src_size, src_size)
                draw_texture_rect_region(ground_texture, dst_rect, src_rect)

    # Draw water sparkle overlays on water tiles
    if sparkle_texture != null:
        for y in range(start_y, end_y + 1):
            for x in range(start_x, end_x + 1):
                if not TerrainPack.SPARKLE_TERRAINS.has(_get_terrain(x, y)):
                    continue
                # Per-tile frame offset so tiles don't all animate in sync
                var tile_hash: int = absi(((x * 31337) + (y * 65539)) % 2147483647)
                var frame: int = (_sparkle_frame + tile_hash % _sparkle_frames) % _sparkle_frames

                var col: int = frame % TerrainPack.SPARKLE_COLS
                var row: int = frame / TerrainPack.SPARKLE_COLS
                var spark_src: Rect2 = Rect2(col * src_size, row * src_size, src_size, src_size)

                var world_x: float = (x - pad_x) * TILE_SIZE
                var world_y: float = (y - pad_y) * TILE_SIZE
                # No overlap on sparkles — they're transparent overlays,
                # overlap would stretch dots into neighboring grass tiles
                var spark_dst: Rect2 = Rect2(world_x, world_y, TILE_SIZE, TILE_SIZE)

                draw_texture_rect_region(sparkle_texture, spark_dst, spark_src)

## Layer rank of a map cell. A terrain id the pack does not draw takes the
## lowest layer, so the cell still shows ground.
func _get_rank(x: int, y: int) -> int:
    var terrain: int = _get_terrain(x, y)
    if terrain < 0 or terrain >= _rank_of.size() or _rank_of[terrain] < 0:
        return 0
    return _rank_of[terrain]

func _get_terrain(x: int, y: int) -> int:
    x = clampi(x, 0, map_width - 1)
    y = clampi(y, 0, map_height - 1)
    return map_data[y][x]
