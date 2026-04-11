extends SceneTree
## Standalone script — run with: godot --headless --script res://scripts/build_tileset.gd
## Generates the wang TileSet resource as a simple atlas grid.
## All 64x32 tiles are created so any position can be referenced at runtime.

func _init() -> void:
    print("Generating wang TileSet...")

    var wang_texture: Texture2D = load("res://assets/tilesets/wang.png")
    if wang_texture == null:
        push_error("Could not load wang.png")
        quit(1)
        return

    var tile_set = TileSet.new()
    tile_set.tile_size = Vector2i(16, 16)

    # Add the wang texture as an atlas source
    var atlas = TileSetAtlasSource.new()
    atlas.texture = wang_texture
    atlas.texture_region_size = Vector2i(16, 16)

    # Create tiles for every cell in the sheet (64 cols x 32 rows)
    var created: int = 0
    for row in range(32):
        for col in range(64):
            var coords = Vector2i(col, row)
            atlas.create_tile(coords)
            created += 1

    var source_id: int = tile_set.add_source(atlas)
    print("  Created ", created, " tiles in atlas (source ", source_id, ")")

    var save_path: String = "res://assets/tilesets/wang_tileset.tres"
    var err = ResourceSaver.save(tile_set, save_path)
    if err != OK:
        push_error("Failed to save TileSet: " + str(err))
        quit(1)
        return

    print("  Saved to: ", save_path)
    print("Done!")
    quit(0)
