extends Node2D
## World — manages terrain and placed objects.
## Loads village data from the Go API and renders everything.

@onready var terrain: TileMapLayer = $Terrain
@onready var objects_node: Node2D = $Objects

# Placed objects keyed by server id
var placed_objects: Dictionary = {}

# API base — same as catalog
var api_base: String = ""

func _ready() -> void:
    if OS.has_feature("web"):
        api_base = ""
    else:
        api_base = "http://zbbs.local"

## Called by Main after catalog is loaded.
## Fetches the village layout from the API and renders it.
func build() -> void:
    _load_village()

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
        # Still render an empty world so the editor works
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

    var sprite = Sprite2D.new()
    sprite.texture = texture
    sprite.centered = false

    # Position the sprite using anchor point.
    # The anchor defines where the "foot" of the object is relative to its size.
    # The object's world position (obj_x, obj_y) is the anchor point in world space.
    sprite.position = Vector2(
        obj_x - texture.region.size.x * anchor_x,
        obj_y - texture.region.size.y * anchor_y
    )

    # Y-sort uses the object's world y position (the anchor point / foot position)
    # We set the sprite's y_sort position relative to the sprite node position
    sprite.y_sort_enabled = false  # Parent handles y_sort

    # Store metadata on the node for the editor
    sprite.set_meta("object_id", obj_id)
    sprite.set_meta("asset_id", asset_id)
    sprite.set_meta("current_state", current_state)
    sprite.set_meta("anchor_x", anchor_x)
    sprite.set_meta("anchor_y", anchor_y)
    sprite.set_meta("world_x", obj_x)
    sprite.set_meta("world_y", obj_y)

    # For y-sorting to work correctly, the node's position.y must be the sort point.
    # We place the node at the object's y position and offset the sprite rendering.
    var container = Node2D.new()
    container.position = Vector2(obj_x, obj_y)
    container.set_meta("object_id", obj_id)
    container.set_meta("asset_id", asset_id)

    # Re-attach sprite as child, offset from the container's origin
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

    # Create the visual node immediately
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

    # Save to server
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
    var headers = ["Content-Type: application/json"]
    http.request(api_base + "/api/village/objects", headers, HTTPClient.METHOD_POST, payload)

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
