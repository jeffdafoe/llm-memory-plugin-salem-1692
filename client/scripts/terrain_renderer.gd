extends Node2D
## Custom terrain renderer — draws wang tiles every frame with 1px overlap
## to prevent sub-pixel seams. Replaces TileMapLayer which caches tiles
## and produces visible gaps at fractional zoom levels.
##
## Only draws tiles visible in the current viewport (culling).
## Each tile is drawn 1px larger than its cell to overlap neighbors.

const WangLookup = preload("res://scripts/wang_lookup.gd")

# Native tile size in the wang atlas (16px Mana Seed tiles)
const SRC_TILE_SIZE: int = 16
# Rendered tile size in world space (2x native)
const TILE_SIZE: int = 32
# Minimum screen pixels of overlap to prevent seams at any zoom level
const MIN_SCREEN_OVERLAP: float = 1.5

# Wang tileset texture — loaded from the atlas
var wang_texture: Texture2D = null

# Water sparkle overlay — 3 rows x 4 columns = 12 total frames, 600ms each.
# Each tile cycles through all 12 frames with a per-tile offset.
var sparkle_texture: Texture2D = null
var _sparkle_frame: int = 0
var _sparkle_timer: float = 0.0
const SPARKLE_FRAME_DURATION: float = 0.6
const SPARKLE_COLS: int = 4
const SPARKLE_ROWS: int = 3
const SPARKLE_TOTAL_FRAMES: int = 12  # 3 rows x 4 cols

# Map data reference — set by world.gd
var map_data: Array = []
var map_width: int = 200
var map_height: int = 90

# Tile offset (array index to world position)
var pad_x: int = 0
var pad_y: int = 0

# Seeded PRNG for wang tile variant selection (must match world.gd)
var _wang_seed_base: int = 7

func _ready() -> void:
    wang_texture = load("res://assets/tilesets/wang.png")
    # Load sparkle sheet if available (commercial asset, may not exist locally)
    var sparkle_path: String = "res://assets/tilesets/mana-seed/summer-forest/summer animations/summer water sparkles B 16x16.png"
    if ResourceLoader.exists(sparkle_path):
        sparkle_texture = load(sparkle_path)

func _process(delta: float) -> void:
    # Advance sparkle animation timer
    _sparkle_timer += delta
    if _sparkle_timer >= SPARKLE_FRAME_DURATION:
        _sparkle_timer -= SPARKLE_FRAME_DURATION
        _sparkle_frame = (_sparkle_frame + 1) % SPARKLE_TOTAL_FRAMES
    # Redraw every frame so tiles are always at correct screen positions
    queue_redraw()

func _draw() -> void:
    if wang_texture == null or map_data.is_empty():
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

    # Draw visible tiles with zoom-adjusted overlap
    for y in range(start_y, end_y + 1):
        for x in range(start_x, end_x + 1):
            var wang_pos: Vector2i = _get_wang_tile(x, y)

            # Source rect in the wang atlas (16x16 native tiles in 1024x512 texture)
            var src_rect: Rect2 = Rect2(
                wang_pos.x * SRC_TILE_SIZE,
                wang_pos.y * SRC_TILE_SIZE,
                SRC_TILE_SIZE,
                SRC_TILE_SIZE
            )

            # Destination in world space — 2x scale + zoom-adjusted overlap
            var world_x: float = (x - pad_x) * TILE_SIZE
            var world_y: float = (y - pad_y) * TILE_SIZE
            var dst_rect: Rect2 = Rect2(
                world_x,
                world_y,
                TILE_SIZE + overlap,
                TILE_SIZE + overlap
            )

            draw_texture_rect_region(wang_texture, dst_rect, src_rect)

    # Draw water sparkle overlays on water tiles
    if sparkle_texture != null:
        for y in range(start_y, end_y + 1):
            for x in range(start_x, end_x + 1):
                var terrain_type: int = _get_terrain(x, y)
                # Terrain types 5 (shallow water) and 6 (deep water)
                if terrain_type == 5 or terrain_type == 6:
                    # Per-tile frame offset so tiles don't all animate in sync
                    var tile_hash: int = ((x * 31337) + (y * 65539)) % 2147483647
                    if tile_hash < 0:
                        tile_hash = -tile_hash
                    var frame_offset: int = tile_hash % SPARKLE_TOTAL_FRAMES
                    var frame: int = (_sparkle_frame + frame_offset) % SPARKLE_TOTAL_FRAMES

                    # Convert linear frame index to row (variant) and column
                    var col: int = frame % SPARKLE_COLS
                    var row: int = frame / SPARKLE_COLS

                    var spark_src: Rect2 = Rect2(
                        col * SRC_TILE_SIZE,
                        row * SRC_TILE_SIZE,
                        SRC_TILE_SIZE,
                        SRC_TILE_SIZE
                    )

                    var world_x: float = (x - pad_x) * TILE_SIZE
                    var world_y: float = (y - pad_y) * TILE_SIZE
                    # No overlap on sparkles — they're transparent overlays,
                    # overlap would stretch dots into neighboring grass tiles
                    var spark_dst: Rect2 = Rect2(
                        world_x, world_y,
                        TILE_SIZE,
                        TILE_SIZE
                    )

                    draw_texture_rect_region(sparkle_texture, spark_dst, spark_src)

## Wang tile lookup — same logic as world.gd but self-contained.
func _get_wang_tile(x: int, y: int) -> Vector2i:
    var tl: int = _get_terrain(x - 1, y - 1)
    var tr: int = _get_terrain(x, y - 1)
    var br: int = _get_terrain(x, y)
    var bl: int = _get_terrain(x - 1, y)

    var key: String = "%d,%d,%d,%d" % [tl, tr, br, bl]

    if WangLookup.WANG_LOOKUP.has(key):
        var options: Array = WangLookup.WANG_LOOKUP[key]
        # Deterministic variant based on position (same seed as world.gd)
        var hash: int = ((x * 16807) + (y * 48271)) % 2147483647
        var idx: int = hash % options.size()
        if idx < 0:
            idx = -idx
        var tile = options[idx]
        return Vector2i(tile[0], tile[1])

    # Fallback — solid light grass
    return Vector2i(1, 2)

func _get_terrain(x: int, y: int) -> int:
    x = clampi(x, 0, map_width - 1)
    y = clampi(y, 0, map_height - 1)
    return map_data[y][x]
