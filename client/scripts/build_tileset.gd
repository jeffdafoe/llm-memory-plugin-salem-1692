extends SceneTree
## Standalone script — run with: godot --headless --script res://scripts/build_tileset.gd
## Generates the wang TileSet resource with terrain peering bits.

const WangLookup = preload("res://scripts/wang_lookup.gd")
const TERRAIN_NAMES: Array = ["Dirt", "Light Grass", "Dark Grass", "Cobblestone", "Shallow Water", "Deep Water"]

func _init() -> void:
    print("Generating wang TileSet with terrain peering bits...")

    var wang_texture: Texture2D = load("res://assets/tilesets/wang.png")
    if wang_texture == null:
        push_error("Could not load wang.png")
        quit(1)
        return

    var tile_set = TileSet.new()
    tile_set.tile_size = Vector2i(16, 16)
    tile_set.tile_shape = TileSet.TILE_SHAPE_SQUARE
    tile_set.tile_layout = TileSet.TILE_LAYOUT_STACKED

    # Corner-based terrain matching (wang tiles)
    tile_set.add_terrain_set(0)
    tile_set.set_terrain_set_mode(0, TileSet.TERRAIN_MODE_MATCH_CORNERS)

    var terrain_colors: Array = [
        Color(0.6, 0.45, 0.3),
        Color(0.5, 0.8, 0.3),
        Color(0.2, 0.5, 0.15),
        Color(0.55, 0.55, 0.55),
        Color(0.3, 0.5, 0.8),
        Color(0.15, 0.25, 0.6),
    ]

    for i in range(6):
        tile_set.add_terrain(0)
        tile_set.set_terrain_name(0, i, TERRAIN_NAMES[i])
        tile_set.set_terrain_color(0, i, terrain_colors[i])

    var atlas = TileSetAtlasSource.new()
    atlas.texture = wang_texture
    atlas.texture_region_size = Vector2i(16, 16)
    var source_id: int = tile_set.add_source(atlas)

    # Reverse lookup: tile position -> corner terrains
    var tile_terrains: Dictionary = {}

    for key in WangLookup.WANG_LOOKUP:
        var parts: PackedStringArray = key.split(",")
        var tl: int = int(parts[0]) - 1
        var tr: int = int(parts[1]) - 1
        var br: int = int(parts[2]) - 1
        var bl: int = int(parts[3]) - 1

        var positions: Array = WangLookup.WANG_LOOKUP[key]
        for pos in positions:
            var tile_key: String = "%d,%d" % [pos[0], pos[1]]
            if not tile_terrains.has(tile_key):
                tile_terrains[tile_key] = [tl, tr, br, bl]

    print("  Tile positions with terrain data: ", tile_terrains.size())

    var created: int = 0
    for tile_key in tile_terrains:
        var parts: PackedStringArray = tile_key.split(",")
        var col: int = int(parts[0])
        var row: int = int(parts[1])
        var corners: Array = tile_terrains[tile_key]
        var coords = Vector2i(col, row)

        atlas.create_tile(coords)
        var tile_data: TileData = atlas.get_tile_data(coords, 0)
        tile_data.terrain_set = 0

        # Set the center terrain — Godot needs this for set_cells_terrain_connect.
        # Use the most common corner as the center terrain.
        var corner_counts: Dictionary = {}
        for c in corners:
            corner_counts[c] = corner_counts.get(c, 0) + 1
        var best_terrain: int = corners[0]
        var best_count: int = 0
        for c in corner_counts:
            if corner_counts[c] > best_count:
                best_count = corner_counts[c]
                best_terrain = c
        tile_data.terrain = best_terrain

        tile_data.set_terrain_peering_bit(TileSet.CELL_NEIGHBOR_TOP_LEFT_CORNER, corners[0])
        tile_data.set_terrain_peering_bit(TileSet.CELL_NEIGHBOR_TOP_RIGHT_CORNER, corners[1])
        tile_data.set_terrain_peering_bit(TileSet.CELL_NEIGHBOR_BOTTOM_RIGHT_CORNER, corners[2])
        tile_data.set_terrain_peering_bit(TileSet.CELL_NEIGHBOR_BOTTOM_LEFT_CORNER, corners[3])
        created += 1

    print("  Tiles created with peering bits: ", created)

    var save_path: String = "res://assets/tilesets/wang_tileset.tres"
    var err = ResourceSaver.save(tile_set, save_path)
    if err != OK:
        push_error("Failed to save TileSet: " + str(err))
        quit(1)
        return

    print("  Saved to: ", save_path)
    print("Done!")
    quit(0)
