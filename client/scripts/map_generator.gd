extends RefCounted
## Procedural village map generator.
## Produces a 2D array of terrain indices matching the wang tile system.

const T = preload("res://scripts/terrain.gd")

# Map size in tiles (at 16px native = 32px rendered with 2x scale)
var width: int = 64
var height: int = 48

# Seeded PRNG for deterministic generation
var _seed: int = 0

func _init(map_width: int = 64, map_height: int = 48, seed: int = 42) -> void:
    width = map_width
    height = map_height
    _seed = seed

func _rand() -> float:
    _seed = (_seed * 16807 + 0) % 2147483647
    return float(_seed) / 2147483647.0

## Generate the terrain map as a 2D array [y][x] of terrain indices.
func generate() -> Array:
    var map_data: Array = []

    # Fill with light grass, sprinkle dark grass
    for y in range(height):
        var row: Array = []
        for x in range(width):
            if _rand() < 0.12:
                row.append(T.GRASS_DARK)
            else:
                row.append(T.GRASS)
        map_data.append(row)

    # Horizontal road — 3 tiles wide with sine curve
    var mid_y: int = height / 2
    var mid_x: int = width / 2

    for x in range(width):
        var curve: int = int(sin(x * 0.1) * 1)
        for dy in range(-1, 2):
            var ry: int = mid_y + curve + dy
            if ry >= 0 and ry < height:
                map_data[ry][x] = T.DIRT_PATH

    # Vertical road
    for y in range(height):
        var curve: int = int(sin(y * 0.08) * 1)
        for dx in range(-1, 2):
            var rx: int = mid_x + curve + dx
            if rx >= 0 and rx < width:
                map_data[y][rx] = T.DIRT_PATH

    # River along the eastern side
    var river_base_x: int = int(width * 0.78)
    var bridge_road_y: int = mid_y + int(sin(river_base_x * 0.1) * 1)

    for y in range(height):
        var river_x: int = river_base_x + int(sin(y * 0.15) * 2)
        var dist_from_bridge: int = abs(y - bridge_road_y)
        var river_width: int = 3
        if dist_from_bridge == 0:
            river_width = 1
        elif dist_from_bridge == 1:
            river_width = 2

        var offset: int = (3 - river_width) / 2
        for dx in range(river_width):
            var rx: int = river_x + offset + dx
            if rx >= 0 and rx < width:
                if river_width >= 3 and dx == 1:
                    map_data[y][rx] = T.DEEP_WATER
                else:
                    map_data[y][rx] = T.WATER

    # Taper road near bridge
    var bridge_river_x: int = river_base_x + int(sin(bridge_road_y * 0.15) * 2)
    var taper_len: int = 10
    for dist in range(1, taper_len + 1):
        var t: float = float(dist) / float(taper_len)
        var half_width: int = 0 if t < 0.5 else 1

        for side in [-1, 1]:
            var bx: int
            if side == -1:
                bx = bridge_river_x - dist
            else:
                bx = bridge_river_x + 2 + dist
            if bx < 0 or bx >= width:
                continue

            for dy in range(-1, 2):
                var ry: int = bridge_road_y + dy
                if ry >= 0 and ry < height:
                    if abs(dy) > half_width:
                        map_data[ry][bx] = T.GRASS
                    else:
                        map_data[ry][bx] = T.DIRT_PATH

    # Forest clusters
    var forest_areas: Array = [
        {"cx": 8, "cy": 6, "r": 6},
        {"cx": width - 8, "cy": 6, "r": 5},
        {"cx": 6, "cy": height - 8, "r": 5},
        {"cx": int(width * 0.6), "cy": int(height * 0.72), "r": 4},
        {"cx": 4, "cy": int(height * 0.4), "r": 3},
    ]
    for area in forest_areas:
        for dy in range(-area["r"], area["r"] + 1):
            for dx in range(-area["r"], area["r"] + 1):
                var tx: int = area["cx"] + dx
                var ty: int = area["cy"] + dy
                var d: float = sqrt(dx * dx + dy * dy)
                var noise: float = _rand() * 2
                if tx >= 0 and tx < width and ty >= 0 and ty < height and d < area["r"] + noise - 1:
                    if map_data[ty][tx] == T.GRASS or map_data[ty][tx] == T.GRASS_DARK:
                        map_data[ty][tx] = T.GRASS_DARK

    # Cobblestone town square at the crossroads
    for dy in range(-2, 3):
        for dx in range(-2, 3):
            if dx * dx + dy * dy <= 5:
                var sx: int = mid_x + dx
                var sy: int = mid_y + dy
                if sx >= 0 and sx < width and sy >= 0 and sy < height:
                    map_data[sy][sx] = T.STONE

    # Dense dark grass border around map edges
    var border_seed: int = 99
    var border_depth: int = 8
    for y in range(height):
        for x in range(width):
            var dist_edge: int = mini(mini(x, width - 1 - x), mini(y, height - 1 - y))
            if dist_edge < border_depth:
                var prob: float = 1.0 - (float(dist_edge) / float(border_depth))
                # Inline PRNG for border (separate seed)
                border_seed = (border_seed * 16807 + 0) % 2147483647
                var r: float = float(border_seed) / 2147483647.0
                if r < prob * 0.9:
                    map_data[y][x] = T.GRASS_DARK

    return map_data
