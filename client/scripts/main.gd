extends Node2D
## Main scene — bootstraps the village viewer.
## Renders terrain immediately, loads objects after catalog is ready.

@onready var world: Node2D = $World

func _ready() -> void:
    # Terrain is procedural — render it immediately, no API needed
    world.build_terrain()

    # Center camera on the map (64x48 tiles at 16px each)
    var cam: Camera2D = $Camera
    cam.position = Vector2(
        world.map_width * 16 / 2.0,
        world.map_height * 16 / 2.0
    )

    # Objects need the asset catalog — load them once the catalog is ready
    if Catalog.loaded:
        world.load_objects()
    else:
        Catalog.catalog_loaded.connect(world.load_objects)
