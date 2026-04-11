extends Node2D
## Main scene — bootstraps the village viewer.
## Loads the asset catalog from the Go API, then tells World to render.

@onready var world: Node2D = $World

func _ready() -> void:
    # Wait for the catalog autoload to finish loading from the API
    if not Catalog.loaded:
        await Catalog.catalog_loaded
    world.build()
