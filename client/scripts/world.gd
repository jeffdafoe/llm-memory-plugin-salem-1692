extends Node2D
## World — manages terrain and placed objects.
## Generates terrain procedurally, renders using custom wang tile renderer
## (with 1px overlap to prevent seams), and loads placed objects from the Go API.

const MapGenerator = preload("res://scripts/map_generator.gd")
const WangLookup = preload("res://scripts/wang_lookup.gd")
const TerrainRendererScript = preload("res://scripts/terrain_renderer.gd")

var terrain_renderer: Node2D = null
@onready var objects_node: Node2D = $Objects

# The generated map data — 2D array [y][x] of terrain indices (1-based)
var map_data: Array = []
var map_width: int = 200
var map_height: int = 90

# Placed objects keyed by server id
var placed_objects: Dictionary = {}

# Agent lookup: llm_memory_agent name → display name
var agent_names: Dictionary = {}
# Ordered list of agent keys for dropdowns
var agent_list: Array = []

# API base
var api_base: String = ""

func _ready() -> void:
    if OS.has_feature("web"):
        api_base = JavaScriptBridge.eval("window.location.origin", true)
    else:
        api_base = "http://zbbs.local"

## Build terrain — create custom renderer, generate terrain data,
## then try loading saved data from API (overwrites if found).
func build_terrain() -> void:
    # Create custom terrain renderer (replaces TileMapLayer)
    terrain_renderer = Node2D.new()
    terrain_renderer.set_script(TerrainRendererScript)
    # Insert before Objects so terrain draws underneath
    add_child(terrain_renderer)
    move_child(terrain_renderer, 0)

    _generate_terrain()  # Generate first so something is visible immediately
    _load_terrain()      # Then try to load saved terrain (overwrites if found)

## Load placed objects from the API — called after catalog is ready.
## Guards against duplicate calls (auth flow can trigger this twice).
var _objects_loaded: bool = false

func load_objects() -> void:
    if _objects_loaded:
        return
    _objects_loaded = true
    _load_village()
    _load_agents()

## Paint a terrain cell. The custom renderer reads map_data directly
## and redraws every frame, so we just update the data.
func paint_terrain(tile_x: int, tile_y: int, terrain_type: int) -> void:
    var pad_x: int = (map_width - 80) / 2
    var pad_y: int = (map_height - 45) / 2
    var ax: int = tile_x + pad_x
    var ay: int = tile_y + pad_y

    if ax < 0 or ax >= map_width or ay < 0 or ay >= map_height:
        return

    map_data[ay][ax] = terrain_type

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

    # Sync renderer with updated map data
    _sync_renderer()

func _generate_terrain() -> void:
    # Generate the logical map
    var gen = MapGenerator.new(map_width, map_height, 42)
    map_data = gen.generate()
    _sync_renderer()

## Convert a world position to tilemap tile coordinates.
## Returns the tile coordinate (accounting for offset and 2x scale).
func world_to_tile(world_pos: Vector2) -> Vector2i:
    # Terrain is scaled 2x so each tile is 32 world pixels
    return Vector2i(
        int(floor(world_pos.x / 32.0)),
        int(floor(world_pos.y / 32.0))
    )

## Sync the terrain renderer with current map_data.
## The renderer draws tiles each frame with 1px overlap.
func _sync_renderer() -> void:
    if terrain_renderer == null:
        return
    terrain_renderer.map_data = map_data
    terrain_renderer.map_width = map_width
    terrain_renderer.map_height = map_height
    terrain_renderer.pad_x = (map_width - 80) / 2
    terrain_renderer.pad_y = (map_height - 45) / 2

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
    container.set_meta("placed_by", data.get("placed_by", ""))
    container.set_meta("owner", data.get("owner", ""))

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
    container.set_meta("asset_id", asset_id)
    container.set_meta("placed_by", Auth.username)
    container.set_meta("owner", "")

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
    # Force visual refresh — same HTML5 y-sort renderer bug as drag-to-move
    container.visible = false
    container.visible = true

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
        node.set_meta("placed_by", json.get("placed_by", ""))
        node.set_meta("owner", json.get("owner", ""))
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

## Fetch village agents to build the owner display name lookup.
func _load_agents() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    http.request_completed.connect(_on_agents_loaded.bind(http))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request(api_base + "/api/village/agents", headers)

func _on_agents_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()

    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        return

    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null or not (json is Array):
        return

    for agent in json:
        var llm_name: String = agent.get("llm_memory_agent", "")
        var display_name: String = agent.get("name", "")
        if llm_name != "" and display_name != "":
            agent_names[llm_name] = display_name
            agent_list.append(llm_name)
    agent_list.sort()

## Set the owner of an object and persist to the server.
func set_object_owner(node: Node2D, owner: String) -> void:
    var obj_id = node.get_meta("object_id", null)
    if obj_id == null:
        return
    node.set_meta("owner", owner)
    _update_object_owner(obj_id, owner)

func _update_object_owner(obj_id, owner: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    var owner_value = null
    if owner != "":
        owner_value = owner
    var payload = JSON.stringify({"owner": owner_value})

    var headers_arr = ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers_arr.append("Authorization: " + auth_header)
    http.request_completed.connect(func(r, c, h, b): http.queue_free())
    http.request(api_base + "/api/village/objects/" + str(obj_id) + "/owner", headers_arr, HTTPClient.METHOD_PATCH, payload)

## Resolve an owner identifier to a display name.
## Returns the display name if found, otherwise the raw owner string.
func get_owner_display_name(owner: String) -> String:
    if owner == "":
        return ""
    return agent_names.get(owner, owner)
