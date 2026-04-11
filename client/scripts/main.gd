extends Node2D
## Main scene — bootstraps the village viewer.
## Renders terrain immediately, loads objects after catalog is ready.

@onready var world: Node2D = $World

func _ready() -> void:
    # Terrain is procedural — render it immediately, no API needed
    world.build_terrain()

    # Objects need the asset catalog — load them once the catalog is ready
    if Catalog.loaded:
        world.load_objects()
    else:
        Catalog.catalog_loaded.connect(world.load_objects)
