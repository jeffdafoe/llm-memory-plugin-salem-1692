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

# Event client reference — set by main.gd for marking local objects
var event_client: Node = null

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

## Reload terrain from the server. Called on initial load and
## when another client saves terrain changes.
func reload_terrain() -> void:
    _load_terrain()

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
        # Two-pass load: base objects first, then attachments.
        # Attachments need their parent to already exist in placed_objects.
        var base_objects: Array = []
        var attachments: Array = []
        for obj in json:
            var attached_to = obj.get("attached_to", null)
            if attached_to != null and attached_to != "":
                attachments.append(obj)
            else:
                base_objects.append(obj)
        for obj in base_objects:
            _place_object(obj)
        for obj in attachments:
            _place_object(obj)

## Create a sprite node for a placed village object.
## Uses AnimatedSprite2D for multi-frame assets, Sprite2D for static ones.
## Handles attached objects — if attached_to is set, the object renders as
## a child of the parent object at the slot's offset position.
func _place_object(data: Dictionary) -> void:
    var asset_id: String = data.get("assetId", data.get("asset_id", ""))
    var current_state: String = data.get("currentState", data.get("current_state", ""))
    var obj_x: float = data.get("x", 0.0)
    var obj_y: float = data.get("y", 0.0)
    var obj_id = data.get("id", 0)
    var attached_to = data.get("attached_to", null)

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
    container.set_meta("object_id", obj_id)
    container.set_meta("asset_id", asset_id)
    container.set_meta("placed_by", data.get("placed_by", ""))
    container.set_meta("owner", data.get("owner", ""))
    container.set_meta("display_name", data.get("display_name", ""))
    container.set_meta("attached_to", attached_to if attached_to != null else "")

    var sprite_node: Node2D = _create_sprite_node(state_info, texture, anchor_x, anchor_y)
    container.add_child(sprite_node)

    # If attached to a parent, add as child of parent node at slot offset
    if attached_to != null and attached_to != "" and placed_objects.has(attached_to):
        var parent_node: Node2D = placed_objects[attached_to]
        var parent_asset_id: String = parent_node.get_meta("asset_id", "")
        var slots: Array = Catalog.get_slots(parent_asset_id)
        var fits_slot: String = asset.get("fits_slot", "")

        # Find the matching slot offset
        var slot_offset: Vector2 = Vector2.ZERO
        for slot in slots:
            if slot.get("slot_name", "") == fits_slot:
                slot_offset = Vector2(slot.get("offset_x", 0), slot.get("offset_y", 0))
                break

        container.position = slot_offset
        container.z_index = 1  # Render on top of parent
        parent_node.add_child(container)
    else:
        container.position = Vector2(obj_x, obj_y)
        objects_node.add_child(container)

    placed_objects[obj_id] = container

## Create the appropriate sprite node for an asset state.
## Returns AnimatedSprite2D for multi-frame states, Sprite2D for static ones.
func _create_sprite_node(state_info: Dictionary, texture: AtlasTexture, anchor_x: float, anchor_y: float) -> Node2D:
    var sprite_frames: SpriteFrames = Catalog.get_sprite_frames(state_info)

    if sprite_frames != null:
        # Animated — use AnimatedSprite2D
        var anim_sprite = AnimatedSprite2D.new()
        anim_sprite.sprite_frames = sprite_frames
        anim_sprite.centered = false
        anim_sprite.scale = Vector2(2, 2)
        anim_sprite.position = Vector2(
            -texture.region.size.x * 2 * anchor_x,
            -texture.region.size.y * 2 * anchor_y
        )
        anim_sprite.play("default")
        return anim_sprite
    else:
        # Static — use Sprite2D
        var sprite = Sprite2D.new()
        sprite.texture = texture
        sprite.centered = false
        sprite.scale = Vector2(2, 2)
        sprite.position = Vector2(
            -texture.region.size.x * 2 * anchor_x,
            -texture.region.size.y * 2 * anchor_y
        )
        return sprite

## Attach an overlay asset to a placed parent object (from the editor).
## The overlay renders as a child node at the parent's slot offset.
func add_attachment(overlay_asset_id: String, parent_node: Node2D) -> void:
    var parent_id = parent_node.get_meta("object_id", null)
    if parent_id == null:
        return

    var state_info = Catalog.get_state(overlay_asset_id)
    if state_info == null:
        return

    var texture: AtlasTexture = Catalog.get_sprite_texture(state_info)
    if texture == null:
        return

    var asset = Catalog.assets.get(overlay_asset_id, {})
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))
    var fits_slot: String = asset.get("fits_slot", "")

    # Find the slot offset on the parent
    var parent_asset_id: String = parent_node.get_meta("asset_id", "")
    var slots: Array = Catalog.get_slots(parent_asset_id)
    var slot_offset: Vector2 = Vector2.ZERO
    for slot in slots:
        if slot.get("slot_name", "") == fits_slot:
            slot_offset = Vector2(slot.get("offset_x", 0), slot.get("offset_y", 0))
            break

    var container = Node2D.new()
    container.position = slot_offset
    container.z_index = 1
    container.set_meta("asset_id", overlay_asset_id)
    container.set_meta("placed_by", Auth.username)
    container.set_meta("owner", "")
    container.set_meta("display_name", "")
    container.set_meta("attached_to", str(parent_id))

    var sprite_node: Node2D = _create_sprite_node(state_info, texture, anchor_x, anchor_y)
    container.add_child(sprite_node)
    parent_node.add_child(container)
    # Force visual refresh
    container.visible = false
    container.visible = true

    _save_attachment(overlay_asset_id, parent_node.position, str(parent_id), container)

func _save_attachment(asset_id: String, pos: Vector2, parent_id: String, node: Node2D) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    var payload = JSON.stringify({
        "asset_id": asset_id,
        "x": pos.x,
        "y": pos.y,
        "attached_to": parent_id
    })

    http.request_completed.connect(_on_object_saved.bind(http, node))
    var headers_arr = ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers_arr.append("Authorization: " + auth_header)
    http.request(api_base + "/api/village/objects", headers_arr, HTTPClient.METHOD_POST, payload)

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
    container.set_meta("display_name", "")

    var sprite_node: Node2D = _create_sprite_node(state_info, texture, anchor_x, anchor_y)
    container.add_child(sprite_node)
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
        "asset_id": asset_id,
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
        # If the WS echo arrived first, it created a duplicate — remove it
        if placed_objects.has(obj_id):
            var ws_node = placed_objects[obj_id]
            if ws_node != node:
                ws_node.queue_free()
        node.set_meta("object_id", obj_id)
        node.set_meta("placed_by", json.get("placed_by", ""))
        node.set_meta("owner", json.get("owner", ""))
        node.set_meta("display_name", json.get("display_name", ""))
        placed_objects[obj_id] = node
        # Mark as locally created so any late WS echo is ignored
        if event_client != null:
            event_client.mark_local_object(obj_id)

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
        push_error("Failed to load agents: result=" + str(result) + " code=" + str(response_code))
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

## Set the display name of an object and persist to the server.
func set_object_display_name(node: Node2D, display_name: String) -> void:
    var obj_id = node.get_meta("object_id", null)
    if obj_id == null:
        return
    node.set_meta("display_name", display_name)
    _update_object_display_name(obj_id, display_name)

func _update_object_display_name(obj_id, display_name: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    var name_value = null
    if display_name != "":
        name_value = display_name
    var payload = JSON.stringify({"display_name": name_value})

    var headers_arr = ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers_arr.append("Authorization: " + auth_header)
    http.request_completed.connect(func(r, c, h, b): http.queue_free())
    http.request(api_base + "/api/village/objects/" + str(obj_id) + "/name", headers_arr, HTTPClient.METHOD_PATCH, payload)

## Resolve an owner identifier to a display name.
## Returns the display name if found, otherwise the raw owner string.
func get_owner_display_name(owner: String) -> String:
    if owner == "":
        return ""
    return agent_names.get(owner, owner)
