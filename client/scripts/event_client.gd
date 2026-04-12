extends Node
## EventClient — connects to the Go engine's WebSocket endpoint and
## dispatches world events (object created/moved/deleted/owner/state changes)
## to the World node so all viewers stay in sync.

var _socket: WebSocketPeer = null
var _connected: bool = false
var _url: String = ""

# Reference to world — set by main.gd after auth
var world: Node2D = null

# IDs of objects we created locally — skip WS events for these
var _local_object_ids: Dictionary = {}

# Reconnect state
var _reconnect_timer: float = 0.0
const RECONNECT_DELAY: float = 3.0

func connect_to_server() -> void:
    if _socket != null:
        _socket.close()

    var base: String = ""
    if OS.has_feature("web"):
        base = JavaScriptBridge.eval("window.location.origin", true)
    else:
        base = "http://zbbs.local"

    # Convert http(s) to ws(s)
    _url = base.replace("https://", "wss://").replace("http://", "ws://") + "/api/village/events"

    _socket = WebSocketPeer.new()
    var err = _socket.connect_to_url(_url)
    if err != OK:
        push_error("WebSocket connect failed: " + str(err))
        _socket = null
        _reconnect_timer = RECONNECT_DELAY

func _process(delta: float) -> void:
    # Handle terrain reload debounce
    if _terrain_reload_timer > 0:
        _terrain_reload_timer -= delta
        if _terrain_reload_timer <= 0 and world != null:
            world.reload_terrain()

    # Handle reconnect timer
    if _socket == null:
        if _reconnect_timer > 0:
            _reconnect_timer -= delta
            if _reconnect_timer <= 0:
                connect_to_server()
        return

    _socket.poll()
    var state = _socket.get_ready_state()

    if state == WebSocketPeer.STATE_OPEN:
        if not _connected:
            _connected = true
        # Read all available messages
        while _socket.get_available_packet_count() > 0:
            var data = _socket.get_packet().get_string_from_utf8()
            _handle_message(data)

    if state == WebSocketPeer.STATE_CLOSED:
        _connected = false
        _socket = null
        _reconnect_timer = RECONNECT_DELAY

func _handle_message(data: String) -> void:
    var json = JSON.parse_string(data)
    if json == null:
        return

    var event_type: String = json.get("type", "")
    var event_data = json.get("data", {})

    match event_type:
        "object_created":
            _on_object_created(event_data)
        "object_deleted":
            _on_object_deleted(event_data)
        "object_moved":
            _on_object_moved(event_data)
        "object_owner_changed":
            _on_object_owner_changed(event_data)
        "object_state_changed":
            _on_object_state_changed(event_data)
        "terrain_updated":
            _on_terrain_updated()

## Call this after placing an object locally so we ignore the WS echo.
func mark_local_object(obj_id: String) -> void:
    _local_object_ids[obj_id] = true

func _on_object_created(data: Dictionary) -> void:
    if world == null:
        return
    var obj_id = data.get("id", "")
    # Skip if we already have this object (we placed it ourselves)
    if world.placed_objects.has(obj_id):
        return
    # Skip if this is our own creation echoed back via WS
    if _local_object_ids.has(obj_id):
        _local_object_ids.erase(obj_id)
        return
    # Place it using the same method as initial load
    world._place_object(data)

func _on_object_deleted(data: Dictionary) -> void:
    if world == null:
        return
    var obj_id = data.get("id", "")
    if not world.placed_objects.has(obj_id):
        return
    var node: Node2D = world.placed_objects[obj_id]
    world.placed_objects.erase(obj_id)
    node.queue_free()

func _on_object_moved(data: Dictionary) -> void:
    if world == null:
        return
    var obj_id = data.get("id", "")
    if not world.placed_objects.has(obj_id):
        return
    var node: Node2D = world.placed_objects[obj_id]
    var new_pos = Vector2(data.get("x", 0.0), data.get("y", 0.0))
    # Visibility toggle to prevent HTML5 y-sort ghost
    node.visible = false
    node.position = new_pos
    node.visible = true

func _on_object_owner_changed(data: Dictionary) -> void:
    if world == null:
        return
    var obj_id = data.get("id", "")
    if not world.placed_objects.has(obj_id):
        return
    var node: Node2D = world.placed_objects[obj_id]
    var owner = data.get("owner", "")
    if owner == null:
        owner = ""
    node.set_meta("owner", owner)

# Debounce terrain reloads — painting triggers frequent saves
var _terrain_reload_timer: float = 0.0
const TERRAIN_RELOAD_DELAY: float = 1.0

func _on_terrain_updated() -> void:
    # Debounce: reset timer on each event, only reload after a pause
    _terrain_reload_timer = TERRAIN_RELOAD_DELAY

func _on_object_state_changed(data: Dictionary) -> void:
    if world == null:
        return
    var obj_id = data.get("id", "")
    if not world.placed_objects.has(obj_id):
        return
    var node: Node2D = world.placed_objects[obj_id]
    var new_state: String = data.get("state", "")
    var asset_id: String = node.get_meta("asset_id", "")

    # Update the sprite texture to reflect the new state
    var state_info = Catalog.get_state(asset_id, new_state)
    if state_info == null:
        return
    var texture = Catalog.get_sprite_texture(state_info)
    if texture == null:
        return

    # Find the sprite child and update it
    for child in node.get_children():
        if child is Sprite2D:
            child.texture = texture
            break
