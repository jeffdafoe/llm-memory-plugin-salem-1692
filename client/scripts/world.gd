extends Node2D
## World — manages terrain and placed objects.
## Generates terrain procedurally, paints it using Godot's terrain autotiling,
## and loads placed objects from the Go API.

const MapGenerator = preload("res://scripts/map_generator.gd")

@onready var terrain: TileMapLayer = $Terrain
@onready var objects_node: Node2D = $Objects

# The generated map data — 2D array [y][x] of terrain indices (1-based)
var map_data: Array = []
var map_width: int = 64
var map_height: int = 48

# Placed objects keyed by server id
var placed_objects: Dictionary = {}

# API base
var api_base: String = ""

# TileSet atlas source id (the wang sheet)
var atlas_source_id: int = 0

func _ready() -> void:
    if OS.has_feature("web"):
        api_base = ""
    else:
        api_base = "http://zbbs.local"

## Called by Main after catalog is loaded.
func build() -> void:
    _generate_terrain()
    _load_village()

func _generate_terrain() -> void:
    # Generate the logical map
    var gen = MapGenerator.new(map_width, map_height, 42)
    map_data = gen.generate()

    # Paint terrain using Godot's terrain system.
    # Group cells by terrain type, then use set_cells_terrain_connect
    # which automatically picks the right wang tile for each cell.
    # Terrain indices in the map are 1-based (1=dirt..6=deep water),
    # Godot terrain indices are 0-based (0=dirt..5=deep water).

    # Collect cells per terrain type
    var terrain_cells: Dictionary = {}  # terrain_index (0-based) -> Array of Vector2i
    for i in range(6):
        terrain_cells[i] = []

    for y in range(map_height):
        for x in range(map_width):
            var t: int = map_data[y][x] - 1  # Convert 1-based to 0-based
            terrain_cells[t].append(Vector2i(x, y))

    # Paint each terrain type. Order matters — paint base terrain first,
    # then overlay terrains. This lets Godot resolve the corner transitions.
    # Paint from most common (grass) outward so transitions look right.
    var paint_order: Array = [1, 2, 0, 3, 4, 5]  # light grass, dark grass, dirt, cobble, shallow, deep

    for terrain_idx in paint_order:
        var cells: Array = terrain_cells[terrain_idx]
        if cells.size() > 0:
            terrain.set_cells_terrain_connect(cells, 0, terrain_idx)

func _load_village() -> void:
    var http = HTTPRequest.new()
    add_child(http)

    http.request_completed.connect(_on_village_loaded.bind(http))
    var error = http.request(api_base + "/api/village/objects")
    if error != OK:
        push_error("Failed to request village objects: " + str(error))

func _on_village_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()

    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_error("Village load failed: result=" + str(result) + " code=" + str(response_code))
        return

    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null:
        push_error("Failed to parse village JSON")
        return

    if json is Array:
        for obj in json:
            _place_object(obj)

## Create a Sprite2D node for a placed village object.
func _place_object(data: Dictionary) -> void:
    var asset_id: String = data.get("assetId", data.get("asset_id", ""))
    var current_state: String = data.get("currentState", data.get("current_state", ""))
    var obj_x: float = data.get("x", 0.0)
    var obj_y: float = data.get("y", 0.0)
    var obj_id = data.get("id", 0)

    var state_info = Catalog.get_state(asset_id, current_state)
    if state_info == null:
        push_warning("No state found for asset: " + asset_id)
        return

    var texture: AtlasTexture = Catalog.get_sprite_texture(state_info)
    if texture == null:
        push_warning("No texture for asset: " + asset_id)
        return

    var asset = Catalog.assets.get(asset_id, {})
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))

    # Container node at the anchor point for y-sorting
    var container = Node2D.new()
    container.position = Vector2(obj_x, obj_y)
    container.set_meta("object_id", obj_id)
    container.set_meta("asset_id", asset_id)

    var sprite = Sprite2D.new()
    sprite.texture = texture
    sprite.centered = false
    sprite.position = Vector2(
        -texture.region.size.x * anchor_x,
        -texture.region.size.y * anchor_y
    )

    container.add_child(sprite)
    objects_node.add_child(container)
    placed_objects[obj_id] = container

## Add a new object to the world (from the editor).
func add_object(asset_id: String, world_pos: Vector2) -> void:
    var state_info = Catalog.get_state(asset_id)
    if state_info == null:
        return

    var texture: AtlasTexture = Catalog.get_sprite_texture(state_info)
    if texture == null:
        return

    var asset = Catalog.assets.get(asset_id, {})
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))
    var default_state: String = asset.get("defaultState", asset.get("default_state", "default"))

    var container = Node2D.new()
    container.position = world_pos

    var sprite = Sprite2D.new()
    sprite.texture = texture
    sprite.centered = false
    sprite.position = Vector2(
        -texture.region.size.x * anchor_x,
        -texture.region.size.y * anchor_y
    )
    container.add_child(sprite)
    objects_node.add_child(container)

    _save_object(asset_id, default_state, world_pos, container)

func _save_object(asset_id: String, state: String, pos: Vector2, node: Node2D) -> void:
    var http = HTTPRequest.new()
    add_child(http)

    var payload = JSON.stringify({
        "assetId": asset_id,
        "currentState": state,
        "x": pos.x,
        "y": pos.y
    })

    http.request_completed.connect(_on_object_saved.bind(http, node))
    var headers_arr = ["Content-Type: application/json"]
    http.request(api_base + "/api/village/objects", headers_arr, HTTPClient.METHOD_POST, payload)

func _on_object_saved(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, node: Node2D) -> void:
    http.queue_free()

    if result != HTTPRequest.RESULT_SUCCESS or response_code < 200 or response_code >= 300:
        push_error("Failed to save object")
        return

    var json = JSON.parse_string(body.get_string_from_utf8())
    if json != null and json.has("id"):
        var obj_id = json["id"]
        node.set_meta("object_id", obj_id)
        placed_objects[obj_id] = node

## Remove an object from the world and the server.
func remove_object(node: Node2D) -> void:
    var obj_id = node.get_meta("object_id", null)
    if obj_id != null:
        placed_objects.erase(obj_id)
        _delete_object(obj_id)
    node.queue_free()

func _delete_object(obj_id) -> void:
    var http = HTTPRequest.new()
    add_child(http)
    http.request_completed.connect(func(r, c, h, b): http.queue_free())
    http.request(api_base + "/api/village/objects/" + str(obj_id), [], HTTPClient.METHOD_DELETE)
