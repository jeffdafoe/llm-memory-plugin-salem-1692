extends Node2D
## World — manages terrain and placed objects.
## Generates terrain procedurally, paints it using Godot's terrain autotiling,
## and loads placed objects from the Go API.

const MapGenerator = preload("res://scripts/map_generator.gd")
const WangLookup = preload("res://scripts/wang_lookup.gd")

@onready var terrain: TileMapLayer = $Terrain
@onready var objects_node: Node2D = $Objects

# The generated map data — 2D array [y][x] of terrain indices (1-based)
var map_data: Array = []
var map_width: int = 200
var map_height: int = 90

# Placed objects keyed by server id
var placed_objects: Dictionary = {}

# API base
var api_base: String = ""

# Seeded PRNG for wang tile variant selection
var _wang_seed: int = 7

func _wang_rand() -> int:
    _wang_seed = (_wang_seed * 16807 + 0) % 2147483647
    return _wang_seed

func _ready() -> void:
    if OS.has_feature("web"):
        api_base = JavaScriptBridge.eval("window.location.origin", true)
    else:
        api_base = "http://zbbs.local"

## Build terrain — try loading saved data from API first,
## fall back to procedural generation if none exists.
func build_terrain() -> void:
    _generate_terrain()  # Generate first so something is visible immediately
    _load_terrain()      # Then try to load saved terrain (overwrites if found)

## Load placed objects from the API — called after catalog is ready.
func load_objects() -> void:
    _load_village()

## Paint a terrain cell and update the wang tiles for it and neighbors.
func paint_terrain(tile_x: int, tile_y: int, terrain_type: int) -> void:
    # Convert tile coords to array indices
    var pad_x: int = (map_width - 80) / 2
    var pad_y: int = (map_height - 45) / 2
    var ax: int = tile_x + pad_x
    var ay: int = tile_y + pad_y

    if ax < 0 or ax >= map_width or ay < 0 or ay >= map_height:
        return

    map_data[ay][ax] = terrain_type

    # Repaint this cell and its neighbors (wang tiles depend on neighbors)
    for dy in range(-1, 2):
        for dx in range(-1, 2):
            var nx: int = ax + dx
            var ny: int = ay + dy
            if nx >= 0 and nx < map_width and ny >= 0 and ny < map_height:
                var wang_pos: Vector2i = _get_wang_tile(nx, ny)
                var source_id: int = terrain.tile_set.get_source_id(0)
                terrain.set_cell(Vector2i(nx - pad_x, ny - pad_y), source_id, wang_pos)

## Save the current terrain to the server.
func save_terrain() -> void:
    # Flatten map_data to a byte array
    var bytes: PackedByteArray = PackedByteArray()
    bytes.resize(map_width * map_height)
    var idx: int = 0
    for y in range(map_height):
        for x in range(map_width):
            bytes[idx] = map_data[y][x]
            idx += 1

    var b64: String = Marshalls.raw_to_base64(bytes)

    var http_req = HTTPRequest.new()
    http_req.accept_gzip = false
    add_child(http_req)

    var payload = JSON.stringify({
        "width": map_width,
        "height": map_height,
        "data": b64
    })

    var headers_arr = ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers_arr.append("Authorization: " + auth_header)
    http_req.request_completed.connect(func(r, c, h, b): http_req.queue_free())
    http_req.request(api_base + "/api/village/terrain", headers_arr, HTTPClient.METHOD_PUT, payload)

func _load_terrain() -> void:
    var http_req = HTTPRequest.new()
    http_req.accept_gzip = false
    add_child(http_req)

    http_req.request_completed.connect(_on_terrain_loaded.bind(http_req))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http_req.request(api_base + "/api/village/terrain", headers)

func _on_terrain_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http_req: HTTPRequest) -> void:
    http_req.queue_free()

    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        # No saved terrain — keep the procedurally generated one
        return

    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null:
        return

    var saved_width: int = json.get("width", 0)
    var saved_height: int = json.get("height", 0)
    var data_b64: String = json.get("data", "")

    if saved_width != map_width or saved_height != map_height:
        push_warning("Saved terrain size mismatch, ignoring")
        return

    var bytes: PackedByteArray = Marshalls.base64_to_raw(data_b64)
    if bytes.size() != map_width * map_height:
        push_warning("Terrain data size mismatch")
        return

    # Overwrite map_data with saved terrain
    var idx: int = 0
    for y in range(map_height):
        for x in range(map_width):
            map_data[y][x] = bytes[idx]
            idx += 1

    # Repaint the entire tilemap
    _paint_tilemap()

func _generate_terrain() -> void:
    # Generate the logical map
    var gen = MapGenerator.new(map_width, map_height, 42)
    map_data = gen.generate()
    _paint_tilemap()

## Convert a world position to tilemap tile coordinates.
## Returns the tile coordinate (accounting for offset and 2x scale).
func world_to_tile(world_pos: Vector2) -> Vector2i:
    # Terrain is scaled 2x so each tile is 32 world pixels
    return Vector2i(
        int(floor(world_pos.x / 32.0)),
        int(floor(world_pos.y / 32.0))
    )

## Repaint the entire tilemap from map_data. Called after generation or
## after loading saved terrain from the server.
func _paint_tilemap() -> void:
    var pad_x: int = (map_width - 80) / 2
    var pad_y: int = (map_height - 45) / 2

    var tile_set: TileSet = terrain.tile_set
    var source_id: int = tile_set.get_source_id(0)

    for y in range(map_height):
        for x in range(map_width):
            var wang_pos: Vector2i = _get_wang_tile(x, y)
            terrain.set_cell(Vector2i(x - pad_x, y - pad_y), source_id, wang_pos)

## Look up the correct wang tile for a map position based on corner terrains.
func _get_wang_tile(x: int, y: int) -> Vector2i:
    # Each tile's appearance depends on the terrain at its 4 corners.
    # A corner is shared between 4 tiles. The corner terrain is the
    # terrain of the tile at that diagonal position.
    var tl: int = _get_terrain(x - 1, y - 1)
    var tr: int = _get_terrain(x, y - 1)
    var br: int = _get_terrain(x, y)
    var bl: int = _get_terrain(x - 1, y)

    var key: String = "%d,%d,%d,%d" % [tl, tr, br, bl]

    if WangLookup.WANG_LOOKUP.has(key):
        var options: Array = WangLookup.WANG_LOOKUP[key]
        # Pick a random variant for visual variety
        var idx: int = _wang_rand() % options.size()
        var tile = options[idx]
        return Vector2i(tile[0], tile[1])

    # Fallback — solid light grass
    return Vector2i(1, 2)

## Get the terrain index at a map position, clamping at edges.
func _get_terrain(x: int, y: int) -> int:
    x = clampi(x, 0, map_width - 1)
    y = clampi(y, 0, map_height - 1)
    return map_data[y][x]

func _load_village() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    http.request_completed.connect(_on_village_loaded.bind(http))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    var error = http.request(api_base + "/api/village/objects", headers)
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

    # Sprites are 16px native, world is 32px scale — render at 2x
    var sprite = Sprite2D.new()
    sprite.texture = texture
    sprite.centered = false
    sprite.scale = Vector2(2, 2)
    sprite.position = Vector2(
        -texture.region.size.x * 2 * anchor_x,
        -texture.region.size.y * 2 * anchor_y
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
    sprite.scale = Vector2(2, 2)
    sprite.position = Vector2(
        -texture.region.size.x * 2 * anchor_x,
        -texture.region.size.y * 2 * anchor_y
    )
    container.add_child(sprite)
    objects_node.add_child(container)

    _save_object(asset_id, default_state, world_pos, container)

func _save_object(asset_id: String, state: String, pos: Vector2, node: Node2D) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    var payload = JSON.stringify({
        "assetId": asset_id,
        "currentState": state,
        "x": pos.x,
        "y": pos.y
    })

    http.request_completed.connect(_on_object_saved.bind(http, node))
    var headers_arr = ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers_arr.append("Authorization: " + auth_header)
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

## Move an object to a new position and persist it to the server.
func move_object(node: Node2D, new_pos: Vector2) -> void:
    var obj_id = node.get_meta("object_id", null)
    if obj_id == null:
        return
    _update_object_position(obj_id, new_pos)

func _update_object_position(obj_id, pos: Vector2) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    var payload = JSON.stringify({
        "x": pos.x,
        "y": pos.y
    })

    var headers_arr = ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers_arr.append("Authorization: " + auth_header)
    http.request_completed.connect(func(r, c, h, b): http.queue_free())
    http.request(api_base + "/api/village/objects/" + str(obj_id) + "/position", headers_arr, HTTPClient.METHOD_PATCH, payload)

func _delete_object(obj_id) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request_completed.connect(func(r, c, h, b): http.queue_free())
    http.request(api_base + "/api/village/objects/" + str(obj_id), headers, HTTPClient.METHOD_DELETE)
