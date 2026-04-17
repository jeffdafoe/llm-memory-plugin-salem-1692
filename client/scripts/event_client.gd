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
        "object_display_name_changed":
            _on_object_display_name_changed(event_data)
        "object_state_changed":
            _on_object_state_changed(event_data)
        "terrain_updated":
            _on_terrain_updated()
        "world_phase_changed":
            _on_world_phase_changed(event_data)
        "npc_walking":
            _on_npc_walking(event_data)
        "npc_arrived":
            _on_npc_arrived(event_data)

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
    # Skip if this is an attachment we just created locally —
    # the parent already has it as a child but we don't have the server ID yet
    var attached_to = data.get("attached_to", null)
    if attached_to != null and attached_to != "":
        if world.placed_objects.has(attached_to):
            var parent_node: Node2D = world.placed_objects[attached_to]
            var asset_id: String = data.get("asset_id", "")
            for child in parent_node.get_children():
                if child.has_meta("asset_id") and child.get_meta("asset_id") == asset_id:
                    # Already have this attachment locally — just record the server ID
                    child.set_meta("object_id", obj_id)
                    world.placed_objects[obj_id] = child
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

func _on_object_display_name_changed(data: Dictionary) -> void:
    if world == null:
        return
    var obj_id = data.get("id", "")
    if not world.placed_objects.has(obj_id):
        return
    var node: Node2D = world.placed_objects[obj_id]
    var display_name = data.get("display_name", "")
    if display_name == null:
        display_name = ""
    node.set_meta("display_name", display_name)

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

    var state_info = Catalog.get_state(asset_id, new_state)
    if state_info == null:
        return
    var texture = Catalog.get_sprite_texture(state_info)
    if texture == null:
        return

    var asset = Catalog.assets.get(asset_id, {})
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))

    # Remove old sprite child and replace with new one (handles Sprite2D <-> AnimatedSprite2D swap)
    for child in node.get_children():
        if child is Sprite2D or child is AnimatedSprite2D:
            child.queue_free()
            break

    var new_sprite: Node2D = world._create_sprite_node(state_info, texture, anchor_x, anchor_y)
    node.add_child(new_sprite)

    # State flipped — update the light to match. Lit states get a glow, unlit
    # states strip it. This also drives the day/night lamp cycle via the bulk
    # UPDATE in the engine's phase transition.
    world.attach_state_light(node, state_info)

func _on_world_phase_changed(data: Dictionary) -> void:
    if world == null:
        return
    var phase: String = data.get("phase", "day")
    world.set_phase(phase, true)

## Server says an NPC is starting a waypoint walk. Store the path + start
## time on the container's "walking" meta; world._process will tick it every
## frame until npc_arrived lands. Picks the initial facing from the first leg
## so the walk animation starts correctly.
func _on_npc_walking(data: Dictionary) -> void:
    if world == null:
        return
    var npc_id: String = data.get("id", "")
    if not world.placed_npcs.has(npc_id):
        return
    var container: Node2D = world.placed_npcs[npc_id]

    var start_pos := Vector2(
        float(data.get("start_x", 0.0)),
        float(data.get("start_y", 0.0))
    )
    var path: Array = []
    for p in data.get("path", []):
        path.append(Vector2(float(p.get("x", 0.0)), float(p.get("y", 0.0))))
    if path.is_empty():
        return

    var walk := {
        "start_pos": start_pos,
        "path": path,
        "speed": float(data.get("speed", 48.0)),
        "started_at_s": Time.get_ticks_msec() / 1000.0,
    }
    container.set_meta("walking", walk)

    # Kick off walk animation in the first leg's direction.
    var first_dir: Vector2 = path[0] - start_pos
    var facing: String = world.facing_from_vec(first_dir)
    container.set_meta("facing", facing)
    world.play_npc_animation(container, facing, "walk")

## Server says the NPC arrived at its destination. Snap position + facing,
## clear walking meta, switch to idle animation.
func _on_npc_arrived(data: Dictionary) -> void:
    if world == null:
        return
    var npc_id: String = data.get("id", "")
    if not world.placed_npcs.has(npc_id):
        return
    var container: Node2D = world.placed_npcs[npc_id]
    var final_x: float = float(data.get("x", 0.0))
    var final_y: float = float(data.get("y", 0.0))
    var facing: String = data.get("facing", "south")

    container.position = Vector2(final_x, final_y)
    container.set_meta("facing", facing)
    container.remove_meta("walking")
    world.play_npc_animation(container, facing, "idle")
