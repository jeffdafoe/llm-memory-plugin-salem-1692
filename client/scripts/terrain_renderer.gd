extends Node2D
## Custom terrain renderer — draws wang tiles every frame with 1px overlap
## to prevent sub-pixel seams. Replaces TileMapLayer which caches tiles
## and produces visible gaps at fractional zoom levels.
##
## Only draws tiles visible in the current viewport (culling).
## Each tile is drawn 1px larger than its cell to overlap neighbors.

const WangLookup = preload("res://scripts/wang_lookup.gd")

const TILE_SIZE: int = 32
const OVERLAP: int = 1

# Wang tileset texture — loaded from the atlas
var wang_texture: Texture2D = null

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

func _process(_delta: float) -> void:
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

    # Convert to tile coordinates (with some margin for overlap)
    var start_x: int = clampi(int(floor(top_left.x / TILE_SIZE)) + pad_x - 1, 0, map_width - 1)
    var start_y: int = clampi(int(floor(top_left.y / TILE_SIZE)) + pad_y - 1, 0, map_height - 1)
    var end_x: int = clampi(int(ceil(bottom_right.x / TILE_SIZE)) + pad_x + 1, 0, map_width - 1)
    var end_y: int = clampi(int(ceil(bottom_right.y / TILE_SIZE)) + pad_y + 1, 0, map_height - 1)

    # Draw visible tiles with 1px overlap
    for y in range(start_y, end_y + 1):
        for x in range(start_x, end_x + 1):
            var wang_pos: Vector2i = _get_wang_tile(x, y)

            # Source rect in the wang atlas (32x32 tiles in 2048x1024 texture)
            var src_rect: Rect2 = Rect2(
                wang_pos.x * TILE_SIZE,
                wang_pos.y * TILE_SIZE,
                TILE_SIZE,
                TILE_SIZE
            )

            # Destination in world space — 1px overlap on each side
            var world_x: float = (x - pad_x) * TILE_SIZE
            var world_y: float = (y - pad_y) * TILE_SIZE
            var dst_rect: Rect2 = Rect2(
                world_x,
                world_y,
                TILE_SIZE + OVERLAP,
                TILE_SIZE + OVERLAP
            )

            draw_texture_rect_region(wang_texture, dst_rect, src_rect)

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
