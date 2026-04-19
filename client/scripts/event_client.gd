extends Node
## EventClient — connects to the Go engine's WebSocket endpoint and
## dispatches world events (object created/moved/deleted/owner/state changes)
## to the World node so all viewers stay in sync.

## Emitted when the WS reopens after a prior disconnect in the same session.
## Main uses this to trigger a full world resync — any events that fired
## while the socket was closed are gone, so we rebuild state from REST.
signal reconnected

var _socket: WebSocketPeer = null
var _connected: bool = false
var _url: String = ""

# True once we've had at least one successful WS open in this session.
# Used to distinguish initial connect (no resync needed) from reconnect
# after a drop (resync needed).
var _initial_connect_done: bool = false

# Reference to world — set by main.gd after auth
var world: Node2D = null

# IDs of objects we created locally — skip WS events for these
var _local_object_ids: Dictionary = {}

# Reconnect state
var _reconnect_timer: float = 0.0
const RECONNECT_DELAY: float = 3.0

# Token currently bound to the open / opening connection. Used to make
# connect_to_server idempotent — calling it twice with the same token
# (which happens when both auth_ready and logged_in fire on a single
# verify) is a no-op rather than a teardown + reconnect race that
# leaves the server logging spurious 401s.
var _connected_token: String = ""

func connect_to_server() -> void:
    # No token → no point connecting. Auth.session_expired handler will bring
    # us back here once the user re-authenticates.
    if Auth.session_token == "":
        if _socket != null:
            _socket.close()
        _socket = null
        _connected_token = ""
        return

    # Already connecting / connected with the same token — leave alone.
    if _socket != null and _connected_token == Auth.session_token:
        var state = _socket.get_ready_state()
        if state == WebSocketPeer.STATE_OPEN or state == WebSocketPeer.STATE_CONNECTING:
            return

    if _socket != null:
        _socket.close()

    var base: String = ""
    if OS.has_feature("web"):
        base = JavaScriptBridge.eval("window.location.origin", true)
    else:
        base = "http://zbbs.local"

    # Convert http(s) to ws(s). Token rides in the URL because browsers can't
    # set custom headers on WebSocket handshakes; rebuilt on every connect so
    # reconnects after re-login pick up the fresh token.
    _url = base.replace("https://", "wss://").replace("http://", "ws://") \
        + "/api/village/events?token=" + Auth.session_token.uri_encode()

    _connected_token = Auth.session_token
    _socket = WebSocketPeer.new()
    var err = _socket.connect_to_url(_url)
    if err != OK:
        push_error("WebSocket connect failed: " + str(err))
        _socket = null
        _connected_token = ""
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
            if _initial_connect_done:
                # Clear pending echo-suppression — any object IDs we were
                # tracking locally were for operations that completed (or
                # didn't) during the gap; the resync will reconcile.
                _local_object_ids.clear()
                reconnected.emit()
            else:
                _initial_connect_done = true
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
        "npc_created":
            if world != null:
                world.add_npc_from_broadcast(event_data)
        "npc_deleted":
            if world != null:
                world.remove_npc_by_id(event_data.get("id", ""))
        "npc_display_name_changed":
            if world != null:
                world.apply_npc_display_name_change(event_data)
        "npc_behavior_changed":
            if world != null:
                world.apply_npc_behavior_change(event_data)
        "npc_agent_changed":
            if world != null:
                world.apply_npc_agent_change(event_data)
        "npc_home_structure_changed":
            if world != null:
                world.apply_npc_home_structure_change(event_data)
        "npc_work_structure_changed":
            if world != null:
                world.apply_npc_work_structure_change(event_data)
        "npc_inside_changed":
            if world != null:
                world.apply_npc_inside_change(event_data)
        "asset_door_updated":
            _on_asset_door_updated(event_data)
        "session_expired":
            # Server's ping loop noticed our session went bad. Route through
            # the same path as a 401 on a REST request so the UI behavior
            # is consistent (login screen pops up, editor goes inactive).
            Auth.notify_session_expired()
        "asset_footprint_updated":
            _on_asset_footprint_updated(event_data)

## Apply a server-broadcast footprint change to the local catalog and
## redraw the selection border if the affected asset is the one currently
## selected in the editor. The local PATCH path also updates the catalog
## (optimistically), so this handler is mostly for OTHER clients learning
## about the change — but it runs unconditionally; the values are
## idempotent.
## Apply a server-broadcast door offset change. Mirrors the local PATCH path
## (which also updates the catalog optimistically). x/y may be null, meaning
## the door was cleared. Refreshes the door marker if the affected asset is
## the one currently selected in the editor.
func _on_asset_door_updated(data: Dictionary) -> void:
    var asset_id: String = data.get("asset_id", "")
    if asset_id == "" or not Catalog.assets.has(asset_id):
        return
    var asset = Catalog.assets[asset_id]
    var x = data.get("x", null)
    var y = data.get("y", null)
    if x == null or y == null:
        asset["door_offset_x"] = null
        asset["door_offset_y"] = null
    else:
        asset["door_offset_x"] = int(x)
        asset["door_offset_y"] = int(y)
    Catalog.assets[asset_id] = asset
    var editor = get_node_or_null("/root/Main/Editor")
    if editor != null and editor.selected_object != null:
        if editor.selected_object.get_meta("asset_id", "") == asset_id:
            if editor.has_method("refresh_door_marker"):
                editor.refresh_door_marker()

func _on_asset_footprint_updated(data: Dictionary) -> void:
    var asset_id: String = data.get("asset_id", "")
    if asset_id == "" or not Catalog.assets.has(asset_id):
        return
    var asset = Catalog.assets[asset_id]
    asset["footprint_left"]   = int(data.get("left",   0))
    asset["footprint_right"]  = int(data.get("right",  0))
    asset["footprint_top"]    = int(data.get("top",    0))
    asset["footprint_bottom"] = int(data.get("bottom", 0))
    Catalog.assets[asset_id] = asset
    # Refresh the selection border if the change affects the currently
    # selected object. Editor lives at /root/Main/Editor.
    var editor = get_node_or_null("/root/Main/Editor")
    if editor != null and editor.selected_object != null:
        if editor.selected_object.get_meta("asset_id", "") == asset_id:
            editor._add_selection_border(editor.selected_object)

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
