@tool
extends EditorScript
## Run this in the Godot editor (File > Run) to generate the wang TileSet resource.
## Reads the wang lookup data and creates a TileSet with terrain peering bits
## assigned to every tile. Only needs to run once — the output .tres file is saved
## and used at runtime.
##
## This replaces the 1300-line lookup table with Godot's native terrain system.

const WangLookup = preload("res://scripts/wang_lookup.gd")

# The 6 terrain types matching the wang tile indices (1-based in the lookup, 0-based in Godot)
const TERRAIN_NAMES: Array = ["Dirt", "Light Grass", "Dark Grass", "Cobblestone", "Shallow Water", "Deep Water"]

func _run() -> void:
    print("Generating wang TileSet with terrain peering bits...")

    var wang_texture: Texture2D = load("res://assets/tilesets/wang.png")
    if wang_texture == null:
        push_error("Could not load wang.png")
        return

    # Create the TileSet
    var tile_set = TileSet.new()
    tile_set.tile_size = Vector2i(16, 16)
    tile_set.tile_shape = TileSet.TILE_SHAPE_SQUARE
    tile_set.tile_layout = TileSet.TILE_LAYOUT_STACKED

    # Set up terrain set 0 as corner-based (MODE_CORNER)
    # This is the wang tile matching mode — each tile corner belongs to a terrain
    tile_set.add_terrain_set(0)
    tile_set.set_terrain_set_mode(0, TileSet.TERRAIN_MODE_MATCH_CORNERS)

    # Add the 6 terrain types
    var terrain_colors: Array = [
        Color(0.6, 0.45, 0.3),   # Dirt — brown
        Color(0.5, 0.8, 0.3),    # Light Grass — green
        Color(0.2, 0.5, 0.15),   # Dark Grass — dark green
        Color(0.55, 0.55, 0.55), # Cobblestone — grey
        Color(0.3, 0.5, 0.8),    # Shallow Water — light blue
        Color(0.15, 0.25, 0.6),  # Deep Water — dark blue
    ]

    for i in range(6):
        tile_set.add_terrain(0)
        tile_set.set_terrain_name(0, i, TERRAIN_NAMES[i])
        tile_set.set_terrain_color(0, i, terrain_colors[i])

    # Add the wang texture as an atlas source
    var atlas = TileSetAtlasSource.new()
    atlas.texture = wang_texture
    atlas.texture_region_size = Vector2i(16, 16)
    var source_id: int = tile_set.add_source(atlas)

    # Build a reverse lookup: tile position -> corner terrains
    # From the wang lookup: key "TL,TR,BR,BL" -> [[col,row], ...]
    # We need: [col,row] -> [TL,TR,BR,BL] (using the FIRST key that references this tile)
    var tile_terrains: Dictionary = {}  # "col,row" -> [tl, tr, br, bl] (0-based terrain indices)

    for key in WangLookup.WANG_LOOKUP:
        var parts: PackedStringArray = key.split(",")
        var tl: int = int(parts[0]) - 1  # Convert 1-based to 0-based
        var tr: int = int(parts[1]) - 1
        var br: int = int(parts[2]) - 1
        var bl: int = int(parts[3]) - 1

        var positions: Array = WangLookup.WANG_LOOKUP[key]
        for pos in positions:
            var tile_key: String = "%d,%d" % [pos[0], pos[1]]
            if not tile_terrains.has(tile_key):
                tile_terrains[tile_key] = [tl, tr, br, bl]

    print("  Tile positions with terrain data: ", tile_terrains.size())

    # Create tiles and assign peering bits
    var created: int = 0
    for tile_key in tile_terrains:
        var parts: PackedStringArray = tile_key.split(",")
        var col: int = int(parts[0])
        var row: int = int(parts[1])
        var corners: Array = tile_terrains[tile_key]
        var coords = Vector2i(col, row)

        # Create the tile in the atlas
        atlas.create_tile(coords)

        # Get the tile data and set terrain peering bits
        var tile_data: TileData = atlas.get_tile_data(coords, 0)
        tile_data.terrain_set = 0

        # Set the 4 corner peering bits
        # Godot's corner peering bit positions:
        tile_data.set_terrain_peering_bit(TileSet.CELL_NEIGHBOR_TOP_LEFT_CORNER, corners[0])
        tile_data.set_terrain_peering_bit(TileSet.CELL_NEIGHBOR_TOP_RIGHT_CORNER, corners[1])
        tile_data.set_terrain_peering_bit(TileSet.CELL_NEIGHBOR_BOTTOM_RIGHT_CORNER, corners[2])
        tile_data.set_terrain_peering_bit(TileSet.CELL_NEIGHBOR_BOTTOM_LEFT_CORNER, corners[3])

        created += 1

    print("  Tiles created with peering bits: ", created)

    # Save the TileSet resource
    var save_path: String = "res://assets/tilesets/wang_tileset.tres"
    var err = ResourceSaver.save(tile_set, save_path)
    if err != OK:
        push_error("Failed to save TileSet: " + str(err))
    else:
        print("  Saved to: ", save_path)

    print("Done! The TileSet is ready for terrain painting.")
