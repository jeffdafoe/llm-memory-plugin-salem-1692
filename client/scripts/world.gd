extends Node2D
## World — manages terrain and placed objects.
## Generates terrain procedurally, renders using custom wang tile renderer
## (with 1px overlap to prevent seams), and loads placed objects from the Go API.

## Emitted after an NPC's metadata is updated from a WS broadcast. Listeners
## (main.gd) refresh the editor panel if the changed NPC is currently selected.
signal npc_metadata_changed(npc_id: String)

## Emitted when the placed_npcs dictionary gains or loses an entry — NPC
## created or deleted from anywhere (local placement or WS broadcast).
## The sidebar Villagers browser rebuilds its list on this signal.
signal npc_list_changed

const MapGenerator = preload("res://scripts/map_generator.gd")
const WangLookup = preload("res://scripts/wang_lookup.gd")
const TerrainRendererScript = preload("res://scripts/terrain_renderer.gd")
const SpeechBubbleScript = preload("res://scripts/speech_bubble.gd")

const SPEECH_BUBBLE_NODE_NAME := "SpeechBubble"

var terrain_renderer: Node2D = null
@onready var objects_node: Node2D = $Objects

# Day/night atmosphere — CanvasModulate tints the whole World subtree at night.
# PointLight2D children on lit objects carve warm pools out of that tint.
var canvas_modulate: CanvasModulate = null
var current_phase: String = "day"
var _light_gradient_texture: Texture2D = null

const DAY_COLOR := Color(1.0, 1.0, 1.0, 1.0)
const NIGHT_COLOR := Color(0.42, 0.46, 0.68, 1.0)
const PHASE_TRANSITION_DURATION := 1.5  # seconds — tween from day to night color

# Layer baseline — terrain renders at z=0 (default), ground overlays sit at
# asset.z_index = 1 (bridges, future road decals), everything else (objects,
# NPCs) sits at OBJECT_Z. Migration ZBBS-052 sets asset.z_index = 10 for
# everything except passages so the catalog already returns the right value;
# OBJECT_Z is the in-code default for NPCs and the legacy fallback.
const OBJECT_Z: int = 10

# The generated map data — 2D array [y][x] of terrain indices (1-based)
var map_data: Array = []
var map_width: int = 200
var map_height: int = 180

# World origin offset — where world (0,0) sits inside the grid.
# Horizontally centered (pad_x = (200-80)/2 = 60). Vertically biased so the
# existing village sits in the southern half of the grid and there's 90 rows
# of space north of origin for building. ZBBS-041 grew the map northward;
# pad_y = 22 + 90 to keep world (0,0) anchored to the same tile as before.
var pad_x: int = 60
var pad_y: int = 112

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

# Cached "HH:MM" dawn/dusk strings from /api/village/world. The editor
# panel reads these (via get_dawn_minute / get_dusk_minute) so it can
# prepopulate empty NPC schedule windows from the global defaults.
# Initialized to the engine's defaults (defaultDawn / defaultDusk in
# engine/world_phase.go) so the panel still has sane numbers if the
# panel renders before _on_world_phase_loaded completes.
var dawn_time_str: String = "07:00"
var dusk_time_str: String = "19:00"

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
    # Terrain at default z = 0 (the ground). Ground overlays (bridges, etc)
    # sit just above at asset.z_index = 1; characters and regular objects
    # sit at z = OBJECT_Z = 10 (see _place_object / _place_npc).
    add_child(terrain_renderer)
    move_child(terrain_renderer, 0)

    # CanvasModulate tints everything under World (terrain + objects) for the
    # day/night cycle. UI lives on separate CanvasLayers and stays bright.
    canvas_modulate = CanvasModulate.new()
    canvas_modulate.color = DAY_COLOR
    add_child(canvas_modulate)

    _generate_terrain()  # Generate first so something is visible immediately
    _load_terrain()      # Then try to load saved terrain (overwrites if found)
    _load_world_phase()  # Then sync modulate to the server's current phase

## Load placed objects from the API — called after catalog is ready.
## Guards against duplicate calls (auth flow can trigger this twice).
var _objects_loaded: bool = false

func load_objects() -> void:
    if _objects_loaded:
        return
    _objects_loaded = true
    _load_village()
    _load_agents()
    _load_npcs()

## Tear down all placed objects and NPCs so load_objects can run cleanly again.
## Used by the WS reconnect resync path — any events that fired while the
## socket was closed are gone forever, so we rebuild from REST rather than
## trying to reconcile. Keeps the NPC sheet texture cache since those files
## don't change mid-session.
func reset_world_state() -> void:
    for obj_id in placed_objects:
        var node: Node2D = placed_objects[obj_id]
        if is_instance_valid(node):
            node.queue_free()
    placed_objects.clear()
    for npc_id in placed_npcs:
        var container: Node2D = placed_npcs[npc_id]
        if is_instance_valid(container):
            container.queue_free()
    placed_npcs.clear()
    _pending_npcs.clear()
    _objects_loaded = false

# NPC rendering — static for milestone 1a, no movement or animation.
# Sprite sheets are cached per path so multiple NPCs sharing a sheet share
# one texture.
var placed_npcs: Dictionary = {}    # id -> Node2D (sprite container)
var _npc_sheets: Dictionary = {}    # sheet_path -> ImageTexture
var _pending_npcs: Array = []       # NPC dicts whose sheet is still downloading

func _load_npcs() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_npcs_loaded.bind(http))
    var headers: PackedStringArray = Auth.auth_headers(false)
    http.request(api_base + "/api/village/npcs", headers)

func _on_npcs_loaded(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_warning("NPC load failed: " + str(response_code))
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null or not (json is Array):
        return

    # Collect unique sheet paths across all NPCs, kick off one download per sheet.
    var unique_sheets: Dictionary = {}
    for npc in json:
        var sprite = npc.get("sprite", null)
        if sprite == null:
            continue
        var sheet: String = sprite.get("sheet", "")
        if sheet != "" and not _npc_sheets.has(sheet) and not unique_sheets.has(sheet):
            unique_sheets[sheet] = true
    _pending_npcs = json
    if unique_sheets.is_empty():
        _render_pending_npcs()
        return
    for sheet_path in unique_sheets:
        _download_npc_sheet(sheet_path)

func _download_npc_sheet(sheet_path: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_npc_sheet_downloaded.bind(http, sheet_path))
    http.request(api_base + sheet_path)

## Get a cached NPC sheet texture, or download it and invoke the callback
## when ready. Used by editor_panel's NPC placement thumbnails so it doesn't
## need its own sheet cache. Callback receives one ImageTexture argument
## (or is called with null on failure — currently we just don't call it,
## keeping the pattern the same as _on_npc_sheet_downloaded).
func get_or_load_npc_sheet(sheet_path: String, callback: Callable) -> void:
    if sheet_path == "":
        return
    if _npc_sheets.has(sheet_path):
        callback.call(_npc_sheets[sheet_path])
        return
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(result: int, code: int, _hdrs: PackedStringArray, body: PackedByteArray):
        http.queue_free()
        if result != HTTPRequest.RESULT_SUCCESS or code != 200:
            push_warning("NPC sheet download failed: " + sheet_path + " code=" + str(code))
            return
        var image = Image.new()
        if image.load_png_from_buffer(body) != OK:
            push_warning("NPC sheet decode failed: " + sheet_path)
            return
        var tex = ImageTexture.create_from_image(image)
        _npc_sheets[sheet_path] = tex
        callback.call(tex)
    )
    http.request(api_base + sheet_path)

## Apply a server-broadcast display name change to the local NPC. Idempotent —
## our own PATCH triggered the broadcast too, so this runs for both the admin
## who made the change and any other connected clients.
func apply_npc_display_name_change(data: Dictionary) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    container.set_meta("display_name", data.get("display_name", ""))
    npc_metadata_changed.emit(npc_id)

## Apply a server-broadcast sprite swap. Rebuilds the AnimatedSprite2D with
## new SpriteFrames. The container, position, and meta (other than sprite
## fields) are preserved — sprite swap is purely visual.
##
## Same async-sheet pattern as add_npc_from_broadcast: if the new sheet
## isn't cached, kick off a download and re-apply once it lands.
func apply_npc_sprite_change(data: Dictionary) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    var sprite_data = data.get("sprite", null)
    if sprite_data == null:
        return
    var sheet_path: String = sprite_data.get("sheet", "")
    if sheet_path == "":
        return

    # Defer to the shared sheet cache. If cached, callback fires
    # synchronously; otherwise after the download completes.
    get_or_load_npc_sheet(sheet_path, func(sheet: Texture2D):
        _swap_npc_sprite(npc_id, sprite_data, sheet)
    )

func _swap_npc_sprite(npc_id: String, sprite_data: Dictionary, sheet: Texture2D) -> void:
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null or sheet == null:
        return

    var fw: int = int(sprite_data.get("frame_width", 32))
    var fh: int = int(sprite_data.get("frame_height", 32))
    var sprite_frames := _build_npc_sprite_frames(sprite_data, sheet)

    # Preserve current facing + animation kind so the new sprite picks up
    # mid-walk seamlessly. Default to facing meta (or south) if no anim.
    var facing: String = str(container.get_meta("facing", "south"))
    var current_anim: String = ""
    var existing_sprite: AnimatedSprite2D = null
    for child in container.get_children():
        if child is AnimatedSprite2D:
            existing_sprite = child
            current_anim = child.animation
            break

    var new_sprite := AnimatedSprite2D.new()
    new_sprite.sprite_frames = sprite_frames
    new_sprite.centered = false
    new_sprite.scale = Vector2(2, 2)
    new_sprite.position = Vector2(-fw * 2 * 0.5, -fh * 2 * 0.9)

    # Replay the same animation if the new sheet has it; otherwise fall back
    # to facing_idle or the first available animation. Avoids a frozen sprite.
    var play_name: String = ""
    if current_anim != "" and sprite_frames.has_animation(current_anim):
        play_name = current_anim
    else:
        var idle_name: String = facing + "_idle"
        if sprite_frames.has_animation(idle_name):
            play_name = idle_name
        else:
            var anims: PackedStringArray = sprite_frames.get_animation_names()
            if anims.size() > 0:
                play_name = anims[0]
    if play_name != "":
        new_sprite.play(play_name)

    if existing_sprite != null:
        existing_sprite.queue_free()
    container.add_child(new_sprite)

    container.set_meta("sprite_id", sprite_data.get("id", ""))
    container.set_meta("sprite_name", sprite_data.get("name", ""))
    npc_metadata_changed.emit(npc_id)

## Build a SpriteFrames from a sprite catalog entry. Shared by initial NPC
## render and live sprite swaps. One animation per (direction, kind) pair —
## "south_walk", "east_idle", etc. Frames are AtlasTexture regions on the
## sheet, laid out horizontally per row.
func _build_npc_sprite_frames(sprite_data: Dictionary, sheet: Texture2D) -> SpriteFrames:
    var fw: int = int(sprite_data.get("frame_width", 32))
    var fh: int = int(sprite_data.get("frame_height", 32))
    var animations: Array = sprite_data.get("animations", [])
    var sprite_frames := SpriteFrames.new()
    for anim in animations:
        var direction: String = anim.get("direction", "")
        var kind: String = anim.get("animation", "")
        var anim_name: String = direction + "_" + kind
        if direction == "" or kind == "":
            continue
        sprite_frames.add_animation(anim_name)
        sprite_frames.set_animation_speed(anim_name, float(anim.get("frame_rate", 6.0)))
        sprite_frames.set_animation_loop(anim_name, true)
        var row_index: int = int(anim.get("row_index", 0))
        var frame_count: int = int(anim.get("frame_count", 1))
        for i in range(frame_count):
            var atlas := AtlasTexture.new()
            atlas.atlas = sheet
            atlas.region = Rect2(i * fw, row_index * fh, fw, fh)
            sprite_frames.add_frame(anim_name, atlas)
    return sprite_frames

## Apply a server-broadcast behavior change. data.behavior may be null for
## behavior cleared — the JSON null decodes to Godot null.
func apply_npc_behavior_change(data: Dictionary) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    var behavior = data.get("behavior", null)
    if behavior == null:
        container.remove_meta("behavior")
    else:
        container.set_meta("behavior", str(behavior))
    npc_metadata_changed.emit(npc_id)

## Apply a server-broadcast agent link change. data.llm_memory_agent may be
## null for unlinked.
func apply_npc_agent_change(data: Dictionary) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    var agent = data.get("llm_memory_agent", null)
    if agent == null:
        container.remove_meta("llm_memory_agent")
    else:
        container.set_meta("llm_memory_agent", str(agent))
    npc_metadata_changed.emit(npc_id)

## Apply a server-broadcast inside flip. Visibility depends on the asset
## of the structure the villager is inside — plain houses hide the sprite,
## market stall / see-through buildings keep them visible.
func apply_npc_inside_change(data: Dictionary) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    var inside: bool = bool(data.get("inside", false))
    var inside_structure_id_val = data.get("inside_structure_id", null)
    var inside_structure_id: String = str(inside_structure_id_val) if inside_structure_id_val != null else ""
    container.set_meta("inside", inside)
    container.set_meta("inside_structure_id", inside_structure_id)
    container.visible = _compute_npc_visible(inside, inside_structure_id)
    _apply_stand_offset_if_applicable(container, inside, inside_structure_id)
    # Buttons that depend on location (Go Home / Go to Work) refresh via
    # the metadata-changed path, so nudge the panel when inside flips.
    npc_metadata_changed.emit(npc_id)

## Resolve whether an NPC should be rendered given the inside flag and
## which structure they're inside. For structures whose asset has
## visible_when_inside=true (see-through buildings like market stalls),
## the villager stays on screen at the door tile.
func _compute_npc_visible(inside: bool, inside_structure_id: String) -> bool:
    if not inside:
        return true
    if inside_structure_id == "" or not placed_objects.has(inside_structure_id):
        return false
    var structure: Node2D = placed_objects[inside_structure_id]
    var asset_id: String = structure.get_meta("asset_id", "")
    var asset: Dictionary = Catalog.assets.get(asset_id, {})
    return bool(asset.get("visible_when_inside", false))

## Reposition a visible-inside NPC to the structure's stand offset tile
## when one is configured. Called on inside=true transitions and on
## initial load so the NPC renders behind the counter rather than at
## the doorway. Silently returns without moving the NPC when the
## structure doesn't have a stand offset set (fall back to the arrival
## position, which is the door tile).
func _apply_stand_offset_if_applicable(container: Node2D, inside: bool, inside_structure_id: String) -> void:
    if not inside or inside_structure_id == "":
        return
    if not placed_objects.has(inside_structure_id):
        return
    var structure: Node2D = placed_objects[inside_structure_id]
    var asset_id: String = structure.get_meta("asset_id", "")
    var asset: Dictionary = Catalog.assets.get(asset_id, {})
    if not bool(asset.get("visible_when_inside", false)):
        return
    var sx = asset.get("stand_offset_x", null)
    var sy = asset.get("stand_offset_y", null)
    if sx == null or sy == null:
        return
    # Snap to tile center so the sprite sits visually aligned with the
    # structure tiles rather than at the anchor corner.
    const TILE: float = 32.0
    var anchor_tile_x: int = int(floor(structure.position.x / TILE))
    var anchor_tile_y: int = int(floor(structure.position.y / TILE))
    container.position = Vector2(
        (anchor_tile_x + int(sx)) * TILE + TILE / 2.0,
        (anchor_tile_y + int(sy)) * TILE + TILE / 2.0,
    )

## Apply a server-broadcast home structure change. data.home_structure_id
## may be null for unlinked.
func apply_npc_home_structure_change(data: Dictionary) -> void:
    _apply_npc_structure_meta(data, "home_structure_id")

## Apply a server-broadcast work structure change. data.work_structure_id
## may be null for unlinked.
func apply_npc_work_structure_change(data: Dictionary) -> void:
    _apply_npc_structure_meta(data, "work_structure_id")

## Apply a server-broadcast schedule update. Mirrors the PATCH payload:
## start_minute/end_minute may be null (worker inherits dawn/dusk),
## interval/start/end are null when cadence is off, lateness is always
## present. Emits npc_metadata_changed so the editor panel re-populates
## when this NPC is the current selection on another client.
func apply_npc_schedule_change(data: Dictionary) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    container.set_meta("lateness_window_minutes", int(data.get("lateness_window_minutes", 0)))
    var start_min = data.get("schedule_start_minute", null)
    var end_min = data.get("schedule_end_minute", null)
    if start_min == null or end_min == null:
        container.remove_meta("schedule_start_minute")
        container.remove_meta("schedule_end_minute")
    else:
        container.set_meta("schedule_start_minute", int(start_min))
        container.set_meta("schedule_end_minute", int(end_min))
    var interval = data.get("schedule_interval_hours", null)
    var start_h = data.get("active_start_hour", null)
    var end_h = data.get("active_end_hour", null)
    if interval == null or start_h == null or end_h == null:
        container.remove_meta("schedule_interval_hours")
        container.remove_meta("active_start_hour")
        container.remove_meta("active_end_hour")
    else:
        container.set_meta("schedule_interval_hours", int(interval))
        container.set_meta("active_start_hour", int(start_h))
        container.set_meta("active_end_hour", int(end_h))
    npc_metadata_changed.emit(npc_id)

func _apply_npc_structure_meta(data: Dictionary, field: String) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    var value = data.get(field, null)
    if value == null:
        container.set_meta(field, "")
    else:
        container.set_meta(field, str(value))
    npc_metadata_changed.emit(npc_id)

## Remove an NPC from the world by id. Called by the npc_deleted WS handler
## on all connected clients, not just the one that initiated the delete.
func remove_npc_by_id(npc_id: String) -> void:
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    placed_npcs.erase(npc_id)
    container.queue_free()
    npc_list_changed.emit()

## Handle an npc_created broadcast — adds the new villager to placed_npcs
## and renders it (downloading the sheet first if it's an unseen sprite).
## Same flow as _on_npcs_loaded for a single NPC.
func add_npc_from_broadcast(data: Dictionary) -> void:
    if not (data is Dictionary):
        return
    var npc_id: String = data.get("id", "")
    if npc_id == "" or placed_npcs.has(npc_id):
        return
    var sprite = data.get("sprite", null)
    if sprite == null:
        return
    var sheet_path: String = sprite.get("sheet", "")
    if sheet_path == "":
        return
    _pending_npcs.append(data)
    if _npc_sheets.has(sheet_path):
        _render_pending_npcs()
    else:
        _download_npc_sheet(sheet_path)

## Handle a pc_appeared broadcast (M6.7). Same payload shape as
## npc_created — the engine's broadcastPCAppeared inlines the sprite,
## position, facing, and inside flag so the client can render in one
## hop. Branches:
##
##   - PC already in placed_npcs (sprite swap or re-broadcast): defer
##     to apply_npc_sprite_change. Position is server-canonical via
##     /api/village/npcs and pc_walk_started events; we don't move
##     them here.
##   - PC not yet rendered (first appearance, or this client connected
##     after the PC entered the world): defer to add_npc_from_broadcast,
##     which builds the AnimatedSprite2D from the same payload that
##     drives NPC rendering.
##
## Single dispatch point so future PC events (needs, sprite swaps, etc.)
## that share the broadcast shape can route through here without a new
## handler.
func apply_pc_appeared(data: Dictionary) -> void:
    if not (data is Dictionary):
        return
    var pc_id: String = data.get("id", "")
    if pc_id == "":
        return
    if placed_npcs.has(pc_id):
        apply_npc_sprite_change(data)
        return
    add_npc_from_broadcast(data)

func _on_npc_sheet_downloaded(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, sheet_path: String) -> void:
    http.queue_free()
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_warning("NPC sheet download failed: " + sheet_path + " code=" + str(response_code))
        return
    var image = Image.new()
    if image.load_png_from_buffer(body) != OK:
        push_warning("NPC sheet decode failed: " + sheet_path)
        return
    _npc_sheets[sheet_path] = ImageTexture.create_from_image(image)
    # Render everyone whose sheet is now ready.
    _render_pending_npcs()

## Render any pending NPCs whose sprite sheet is loaded. Called per sheet
## completion so NPCs appear as their sheet arrives.
func _render_pending_npcs() -> void:
    var still_pending: Array = []
    for npc in _pending_npcs:
        var sprite = npc.get("sprite", null)
        if sprite == null:
            continue
        var sheet_path: String = sprite.get("sheet", "")
        if not _npc_sheets.has(sheet_path):
            still_pending.append(npc)
            continue
        _render_npc(npc)
    _pending_npcs = still_pending

## Build an AnimatedSprite2D for an NPC with all directions × (idle, walk)
## animations loaded from the catalog. Starts in idle for the NPC's facing.
func _render_npc(npc: Dictionary) -> void:
    var npc_id: String = npc.get("id", "")
    if npc_id == "" or placed_npcs.has(npc_id):
        return
    var sprite_data: Dictionary = npc.get("sprite", {})
    var sheet_path: String = sprite_data.get("sheet", "")
    var sheet: ImageTexture = _npc_sheets.get(sheet_path)
    if sheet == null:
        return

    var fw: int = int(sprite_data.get("frame_width", 32))
    var fh: int = int(sprite_data.get("frame_height", 32))
    var sprite_frames := _build_npc_sprite_frames(sprite_data, sheet)

    var facing: String = npc.get("facing", "south")

    var container := Node2D.new()
    container.set_meta("npc_id", npc_id)
    container.set_meta("sprite_id", sprite_data.get("id", ""))
    container.set_meta("sprite_name", sprite_data.get("name", ""))
    container.set_meta("display_name", npc.get("display_name", ""))
    container.set_meta("facing", facing)
    container.set_meta("behavior", npc.get("behavior", ""))
    container.set_meta("llm_memory_agent", npc.get("llm_memory_agent", ""))
    container.set_meta("home_structure_id", npc.get("home_structure_id", ""))
    container.set_meta("work_structure_id", npc.get("work_structure_id", ""))
    container.set_meta("lateness_window_minutes", int(npc.get("lateness_window_minutes", 0)))
    # Worker work-window (ZBBS-071) — carry only when both are set so the
    # editor panel can distinguish "NULL inherits dawn/dusk" from the
    # 0/0 minute literal.
    var _sched_start = npc.get("schedule_start_minute", null)
    var _sched_end = npc.get("schedule_end_minute", null)
    if _sched_start != null and _sched_end != null:
        container.set_meta("schedule_start_minute", int(_sched_start))
        container.set_meta("schedule_end_minute", int(_sched_end))
    # interval/start/end are optional (all-or-none). Only set meta when the
    # server actually sent values so the editor panel can distinguish
    # "null cadence" from 0-valued cadence.
    var _interval = npc.get("schedule_interval_hours", null)
    var _start_h = npc.get("active_start_hour", null)
    var _end_h = npc.get("active_end_hour", null)
    if _interval != null and _start_h != null and _end_h != null:
        container.set_meta("schedule_interval_hours", int(_interval))
        container.set_meta("active_start_hour", int(_start_h))
        container.set_meta("active_end_hour", int(_end_h))
    # Social-hour overlay (ZBBS-068, minute-precision since ZBBS-071) —
    # carry only when all three are set.
    var _social_tag = npc.get("social_tag", null)
    var _social_start = npc.get("social_start_minute", null)
    var _social_end = npc.get("social_end_minute", null)
    if _social_tag != null and _social_tag != "" and _social_start != null and _social_end != null:
        container.set_meta("social_tag", str(_social_tag))
        container.set_meta("social_start_minute", int(_social_start))
        container.set_meta("social_end_minute", int(_social_end))
    # Needs (ZBBS-082) — current hunger/thirst/tiredness in [0, 24].
    # Always set, default to 0 so the editor panel can read them without
    # null-checks. Updates arrive via apply_npc_needs_changed (admin
    # reset) or on the next /api/village/npcs refresh.
    container.set_meta("hunger", int(npc.get("hunger", 0)))
    container.set_meta("thirst", int(npc.get("thirst", 0)))
    container.set_meta("tiredness", int(npc.get("tiredness", 0)))
    var inside: bool = bool(npc.get("inside", false))
    var inside_structure_id_val = npc.get("inside_structure_id", null)
    var inside_structure_id: String = str(inside_structure_id_val) if inside_structure_id_val != null else ""
    container.set_meta("inside", inside)
    container.set_meta("inside_structure_id", inside_structure_id)
    container.visible = _compute_npc_visible(inside, inside_structure_id)
    container.position = Vector2(npc.get("current_x", 0.0), npc.get("current_y", 0.0))
    # Stand offset overrides the server-provided current_x/y when inside
    # a visible_when_inside structure — NPCs render behind the counter
    # rather than at the door tile they actually walked to.
    _apply_stand_offset_if_applicable(container, inside, inside_structure_id)
    container.z_index = OBJECT_Z

    var anim_sprite := AnimatedSprite2D.new()
    anim_sprite.sprite_frames = sprite_frames
    anim_sprite.centered = false
    anim_sprite.scale = Vector2(2, 2)
    # Anchor the sprite so its feet sit at the container's position.
    anim_sprite.position = Vector2(-fw * 2 * 0.5, -fh * 2 * 0.9)
    container.add_child(anim_sprite)

    var idle_name := facing + "_idle"
    if sprite_frames.has_animation(idle_name):
        anim_sprite.play(idle_name)

    objects_node.add_child(container)
    placed_npcs[npc_id] = container
    npc_list_changed.emit()

## Facing direction from a movement vector. |dx| vs |dy| picks the dominant
## axis, sign picks N/S/E/W. Matches the server's deriveFacing so server and
## client always agree on the animation to play.
func facing_from_vec(v: Vector2) -> String:
    if abs(v.x) > abs(v.y):
        return "east" if v.x > 0 else "west"
    return "south" if v.y > 0 else "north"

## Play (direction + "_" + kind) on an NPC's sprite. No-op if the container
## is missing an AnimatedSprite2D child or the animation doesn't exist.
func play_npc_animation(container: Node2D, facing: String, kind: String) -> void:
    for child in container.get_children():
        if child is AnimatedSprite2D:
            var anim_name := facing + "_" + kind
            if child.sprite_frames != null and child.sprite_frames.has_animation(anim_name):
                if child.animation != anim_name:
                    child.play(anim_name)
            return

## Each frame, tick any NPC whose "walking" meta is set. Walks along the path
## stored at walk_start time and interpolates position; swaps facing when the
## current leg's direction of travel changes. Arrival cleanup happens in the
## npc_arrived WS handler, not here.
func _process(delta: float) -> void:
    for npc_id in placed_npcs:
        var container: Node2D = placed_npcs[npc_id]
        if container.has_meta("walking"):
            _tick_npc_walk(container)

func _tick_npc_walk(container: Node2D) -> void:
    var walk = container.get_meta("walking")
    var now_s: float = Time.get_ticks_msec() / 1000.0
    var elapsed: float = now_s - walk["started_at_s"]
    var remaining: float = elapsed * walk["speed"]

    var prev: Vector2 = walk["start_pos"]
    var path: Array = walk["path"]
    for wp in path:
        var leg_dist: float = prev.distance_to(wp)
        if leg_dist <= 0.01:
            prev = wp
            continue
        if remaining <= leg_dist:
            var t: float = remaining / leg_dist
            container.position = prev.lerp(wp, t)
            var new_facing: String = facing_from_vec(wp - prev)
            if container.get_meta("facing", "") != new_facing:
                container.set_meta("facing", new_facing)
                play_npc_animation(container, new_facing, "walk")
            return
        remaining -= leg_dist
        prev = wp
    # Past the end — snap to final waypoint. Real cleanup waits for npc_arrived.
    container.position = path[path.size() - 1]

## Paint a terrain cell. The custom renderer reads map_data directly
## and redraws every frame, so we just update the data.
func paint_terrain(tile_x: int, tile_y: int, terrain_type: int) -> void:
    var ax: int = tile_x + pad_x
    var ay: int = tile_y + pad_y

    if ax < 0 or ax >= map_width or ay < 0 or ay >= map_height:
        return

    map_data[ay][ax] = terrain_type

## Save the current terrain to the server. Caches the payload to localStorage
## first so an unsent / failed save survives a refresh — main.gd flushes the
## cache after re-auth (see _flush_unsaved_terrain).
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

    # Persist before send. If the request is rejected, dies on the wire, or the
    # tab is closed before the response, the bytes are still recoverable on the
    # next authenticated session.
    _cache_unsaved_terrain(b64)

    # No token → no point in sending. The cache will be flushed after re-auth.
    # This stops the silent retry storm that masked the original wendy-paint bug.
    if not Auth.is_authenticated():
        return

    var http_req = HTTPRequest.new()
    http_req.accept_gzip = false
    add_child(http_req)

    var payload = JSON.stringify({
        "width": map_width,
        "height": map_height,
        "data": b64
    })

    var headers_arr = Auth.auth_headers()
    http_req.request_completed.connect(func(r, c, h, b):
        http_req.queue_free()
        if c == 204:
            _clear_unsaved_terrain()
        Auth.check_response(c)
    )
    http_req.request(api_base + "/api/village/terrain", headers_arr, HTTPClient.METHOD_PUT, payload)

# --- Unsaved-terrain localStorage cache ---
#
# Painted bytes that haven't been confirmed by the server live here. Web-only
# (no localStorage off-web); main.gd flushes after _on_authenticated.

const UNSAVED_TERRAIN_KEY: String = "salem_terrain_unsaved"

func _cache_unsaved_terrain(b64: String) -> void:
    if not OS.has_feature("web"):
        return
    var payload: String = JSON.stringify({
        "width": map_width,
        "height": map_height,
        "data": b64,
        "saved_at": Time.get_unix_time_from_system(),
    })
    # Wrap in JSON.stringify on the JS side too so embedded quotes survive.
    JavaScriptBridge.eval("localStorage.setItem('%s', %s)" % [UNSAVED_TERRAIN_KEY, JSON.stringify(payload)])

func _clear_unsaved_terrain() -> void:
    if not OS.has_feature("web"):
        return
    JavaScriptBridge.eval("localStorage.removeItem('%s')" % UNSAVED_TERRAIN_KEY)

## Returns the cached payload (width/height/data/saved_at dict) or null if
## nothing is pending. Called from main.gd after re-auth.
func get_unsaved_terrain() -> Variant:
    if not OS.has_feature("web"):
        return null
    var raw = JavaScriptBridge.eval("localStorage.getItem('%s') || ''" % UNSAVED_TERRAIN_KEY, true)
    if not (raw is String) or raw == "":
        return null
    return JSON.parse_string(raw)

## Restore a cached payload into map_data and immediately re-attempt the save.
## Used by main.gd after re-auth to recover paints the user did while offline
## or against a stale token.
func restore_unsaved_terrain(payload: Dictionary) -> void:
    var saved_width: int = int(payload.get("width", 0))
    var saved_height: int = int(payload.get("height", 0))
    var data_b64: String = payload.get("data", "")
    if saved_width != map_width or saved_height != map_height:
        # Map dimensions changed under us — safer to discard than corrupt.
        _clear_unsaved_terrain()
        return
    var bytes: PackedByteArray = Marshalls.base64_to_raw(data_b64)
    if bytes.size() != map_width * map_height:
        _clear_unsaved_terrain()
        return
    var idx: int = 0
    for y in range(map_height):
        for x in range(map_width):
            map_data[y][x] = bytes[idx]
            idx += 1
    _sync_renderer()
    save_terrain()

## Reload terrain from the server. Called on initial load and
## when another client saves terrain changes.
func reload_terrain() -> void:
    _load_terrain()

## Apply a world phase change. Tweens CanvasModulate toward the target color
## so darken/brighten happens smoothly. Pass tween=false for the initial load
## so the scene doesn't briefly flash the wrong color.
func set_phase(phase: String, tween: bool = true) -> void:
    current_phase = phase
    if canvas_modulate == null:
        return
    var target_color: Color = NIGHT_COLOR if phase == "night" else DAY_COLOR
    if tween:
        var t = create_tween()
        t.tween_property(canvas_modulate, "color", target_color, PHASE_TRANSITION_DURATION)
    else:
        canvas_modulate.color = target_color

## Fetch the server's current world phase and sync CanvasModulate to match.
## Called once at build_terrain so clients opening the page at night don't
## start in bright day before the first WS event lands.
func _load_world_phase() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_world_phase_loaded.bind(http))
    var headers: PackedStringArray = Auth.auth_headers(false)
    http.request(api_base + "/api/village/world", headers)

func _on_world_phase_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null:
        return
    var phase: String = json.get("phase", "day")
    set_phase(phase, false)  # instant — no tween on first load

    # Cache dawn/dusk so the editor panel can prepopulate empty NPC
    # schedule windows from the global defaults. Format is "HH:MM".
    dawn_time_str = json.get("dawn_time", dawn_time_str)
    dusk_time_str = json.get("dusk_time", dusk_time_str)

    # Apply the zoom floor appropriate for this user's role. Admins get
    # the "admin" floor (typically lower, so they can see further out).
    apply_zoom_floor_from_config(json)

## Parse "HH:MM" into minutes-of-day. Returns fallback when the cached
## string is empty or malformed (e.g. world-config request hasn't completed
## yet, or the server returned a bad value).
func _parse_hm_to_minute(s: String, fallback: int) -> int:
    var parts := s.split(":")
    if parts.size() != 2:
        return fallback
    var h := int(parts[0])
    var m := int(parts[1])
    if h < 0 or h > 23 or m < 0 or m > 59:
        return fallback
    return h * 60 + m

## Dawn/dusk in minutes-of-day from the cached world-config strings.
## Editor panel uses these to prepopulate the SCHEDULE start/end spinners
## when the NPC's per-NPC window is NULL (inheriting global defaults).
func get_dawn_minute() -> int:
    return _parse_hm_to_minute(dawn_time_str, 7 * 60)

func get_dusk_minute() -> int:
    return _parse_hm_to_minute(dusk_time_str, 19 * 60)

## Pick the right zoom floor from a world config dict and push it to the
## camera. Used by the initial load and by the zoom_settings_changed WS
## event when an admin retunes the values.
func apply_zoom_floor_from_config(cfg: Dictionary) -> void:
    var key: String = "zoom_min_admin" if Auth.can_edit else "zoom_min_regular"
    var value = cfg.get(key, null)
    if value == null:
        return
    var camera = get_node_or_null("/root/Main/Camera")
    if camera != null and camera.has_method("set_zoom_floor"):
        camera.set_zoom_floor(float(value))

## Lazily build the shared soft-radial gradient texture used by every
## PointLight2D. One texture, reused across all lit objects.
func _get_light_gradient() -> Texture2D:
    if _light_gradient_texture != null:
        return _light_gradient_texture
    var gradient = Gradient.new()
    gradient.set_color(0, Color(1.0, 1.0, 1.0, 1.0))
    gradient.set_color(1, Color(1.0, 1.0, 1.0, 0.0))
    var tex = GradientTexture2D.new()
    tex.gradient = gradient
    tex.fill = GradientTexture2D.FILL_RADIAL
    tex.fill_from = Vector2(0.5, 0.5)
    tex.fill_to = Vector2(1.0, 0.5)
    tex.width = 256
    tex.height = 256
    _light_gradient_texture = tex
    return tex

## Attach (or re-attach) a PointLight2D to a container based on its current
## state's light params. Removes any existing light first. No-op when the
## state has no light data (most states). Called on object placement and
## on object_state_changed.
func attach_state_light(container: Node2D, state_info: Dictionary) -> void:
    for child in container.get_children():
        if child is PointLight2D:
            child.queue_free()

    var light_data = state_info.get("light", null)
    if light_data == null or not (light_data is Dictionary):
        return

    var light = PointLight2D.new()
    light.texture = _get_light_gradient()
    var color_str: String = light_data.get("color", "#FFAA55")
    light.color = Color.html(color_str)
    light.energy = light_data.get("energy", 1.0)
    # The gradient's transparent edge sits at 128px from center (256px texture,
    # fill_from=center, fill_to=edge). Scale so the outer edge lands at the
    # configured world-pixel radius.
    var radius: float = light_data.get("radius", 96)
    light.texture_scale = radius / 128.0
    light.offset = Vector2(
        light_data.get("offset_x", 0),
        light_data.get("offset_y", 0)
    )
    # Optional flicker: sinusoidal-ish tween on energy. Two-leg tween per cycle.
    var amp: float = light_data.get("flicker_amplitude", 0.0)
    var period_ms: int = light_data.get("flicker_period_ms", 0)
    if amp > 0.0 and period_ms > 0:
        var base_energy: float = light.energy
        var half_period: float = (period_ms / 1000.0) / 2.0
        var flicker_tween = light.create_tween()
        flicker_tween.set_loops()
        flicker_tween.tween_property(light, "energy", base_energy * (1.0 + amp), half_period)
        flicker_tween.tween_property(light, "energy", base_energy * (1.0 - amp), half_period)

    container.add_child(light)

func _load_terrain() -> void:
    var http_req = HTTPRequest.new()
    http_req.accept_gzip = false
    add_child(http_req)

    http_req.request_completed.connect(_on_terrain_loaded.bind(http_req))
    var headers: PackedStringArray = Auth.auth_headers(false)
    http_req.request(api_base + "/api/village/terrain", headers)

func _on_terrain_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http_req: HTTPRequest) -> void:
    http_req.queue_free()

    if not Auth.check_response(response_code):
        return
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

## Hit-test a screen position against rendered structures.
## Returns {id, asset_id} for the nearest structure whose sprite bounding
## box contains the click, or empty Dictionary if no hit.
##
## Used by play-mode click-to-walk so a click on a building routes the
## PC to that building's door (server-side resolution) instead of the
## raw click coords. Mirrors the bounding-box logic in editor.gd /
## object_tooltip.gd's _find_object_at, plus a reverse lookup in
## placed_objects to recover the object id.
func find_object_at(screen_pos: Vector2) -> Dictionary:
    var viewport: Viewport = get_viewport()
    var canvas_transform: Transform2D = viewport.get_canvas_transform()
    var world_pos: Vector2 = canvas_transform.affine_inverse() * screen_pos

    var best_id: String = ""
    var best_asset_id: String = ""
    var best_dist: float = INF

    for obj_id in placed_objects:
        var node: Node2D = placed_objects[obj_id]
        if node == null or node.get_child_count() == 0:
            continue
        var sprite_node: Node2D = null
        for child in node.get_children():
            if child is Sprite2D or child is AnimatedSprite2D:
                sprite_node = child
                break
        if sprite_node == null:
            continue

        var region_size: Vector2 = _sprite_size_for_hit(sprite_node)
        if region_size == Vector2.ZERO:
            continue
        var world_size: Vector2 = region_size * sprite_node.scale
        var rect_origin: Vector2 = node.position + sprite_node.position
        var rect = Rect2(rect_origin, world_size)
        if not rect.has_point(world_pos):
            continue
        var dist: float = node.position.distance_to(world_pos)
        if dist < best_dist:
            best_dist = dist
            best_id = obj_id
            best_asset_id = str(node.get_meta("asset_id", ""))

    if best_id == "":
        return {}
    return {"id": best_id, "asset_id": best_asset_id}

## Texture size from either Sprite2D or AnimatedSprite2D — same logic
## the editor / tooltip helpers use for click hit-testing.
func _sprite_size_for_hit(sprite_node: Node2D) -> Vector2:
    if sprite_node is Sprite2D:
        var tex = sprite_node.texture
        if tex != null:
            return tex.get_size()
    if sprite_node is AnimatedSprite2D:
        var frames: SpriteFrames = sprite_node.sprite_frames
        if frames != null and frames.get_frame_count("default") > 0:
            var tex = frames.get_frame_texture("default", 0)
            if tex != null:
                return tex.get_size()
    return Vector2.ZERO

## Sync the terrain renderer with current map_data.
## The renderer draws tiles each frame with 1px overlap.
func _sync_renderer() -> void:
    if terrain_renderer == null:
        return
    terrain_renderer.map_data = map_data
    terrain_renderer.map_width = map_width
    terrain_renderer.map_height = map_height
    terrain_renderer.pad_x = pad_x
    terrain_renderer.pad_y = pad_y

func _load_village() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    http.request_completed.connect(_on_village_loaded.bind(http))
    var headers: PackedStringArray = Auth.auth_headers(false)
    var error = http.request(api_base + "/api/village/objects", headers)
    if error != OK:
        push_error("Failed to request village objects: " + str(error))

func _on_village_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()

    if not Auth.check_response(response_code):
        return
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

    # NPCs and objects load in parallel (two independent HTTP requests).
    # If the NPC response arrived first, any NPCs currently "inside" a
    # visible_when_inside structure were rendered at the raw door tile
    # because _apply_stand_offset_if_applicable couldn't find their
    # structure in placed_objects yet. Now that objects are loaded,
    # re-run the stand-offset pass so those NPCs snap to their proper
    # behind-the-counter positions.
    _reapply_stand_offsets_for_inside_npcs()

    # Same load-order race for the Villagers list: location labels read from
    # placed_objects to resolve "near Meeting House"-style strings, so if the
    # list rebuilt before objects loaded everyone shows "at X,Y". Re-emit so
    # the panel rebuilds with proper landmark labels now that objects exist.
    npc_list_changed.emit()

## Re-run the stand-offset adjustment for every NPC whose inside flag is
## true and whose inside_structure_id now resolves. Safe to call multiple
## times — _apply_stand_offset_if_applicable is a no-op for NPCs whose
## structure lacks visible_when_inside + stand offset.
func _reapply_stand_offsets_for_inside_npcs() -> void:
    for npc_id in placed_npcs:
        var container: Node2D = placed_npcs[npc_id]
        if container == null:
            continue
        var inside: bool = bool(container.get_meta("inside", false))
        if not inside:
            continue
        var inside_structure_id: String = str(container.get_meta("inside_structure_id", ""))
        if inside_structure_id == "":
            continue
        # Refresh visibility too — an NPC marked inside with no resolved
        # structure earlier would have been hidden; with the structure
        # now loaded, visible_when_inside may flip them back on.
        container.visible = _compute_npc_visible(inside, inside_structure_id)
        _apply_stand_offset_if_applicable(container, inside, inside_structure_id)

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
    var asset_z_index: int = int(asset.get("z_index", OBJECT_Z))

    # Container node at the anchor point for y-sorting
    var container = Node2D.new()
    container.set_meta("object_id", obj_id)
    container.set_meta("asset_id", asset_id)
    container.set_meta("placed_by", data.get("placed_by", ""))
    container.set_meta("owner", data.get("owner", ""))
    container.set_meta("display_name", data.get("display_name", ""))
    container.set_meta("attached_to", attached_to if attached_to != null else "")
    # Per-instance tags (ZBBS-069). Server always sends an array (possibly
    # empty); accept either an Array or omit-as-null and normalize.
    var tags_raw = data.get("tags", [])
    container.set_meta("tags", tags_raw if tags_raw is Array else [])
    # Per-instance entry policy (ZBBS-101). 'none' / 'owner' / 'anyone'.
    # Drives the editor's structure detail dropdown and the PC click
    # handler's enter-vs-knock decision (server-side, this duplicates).
    container.set_meta("entry_policy", str(data.get("entry_policy", "none")))
    # Per-instance loiter offset (ZBBS-075). Tile-unit ints, both nullable.
    # Stored as variants so the editor can distinguish "not set" (null)
    # from "set to (0, 0)" (legitimate origin offset). The fill state of
    # the green marker (placeholder vs. configured) reads these.
    container.set_meta("loiter_offset_x", data.get("loiter_offset_x", null))
    container.set_meta("loiter_offset_y", data.get("loiter_offset_y", null))
    # Effective loiter — the canonical position the marker renders at AND
    # the engine's visitor walk-resolver targets. Computed server-side via
    # effectiveLoiterTile (engine/village_objects.go). Single source of
    # truth — never recompute the placeholder formula on the client.
    container.set_meta("effective_loiter_offset_x", data.get("effective_loiter_offset_x", null))
    container.set_meta("effective_loiter_offset_y", data.get("effective_loiter_offset_y", null))

    var sprite_node: Node2D = _create_sprite_node(state_info, texture, anchor_x, anchor_y)
    container.add_child(sprite_node)
    attach_state_light(container, state_info)

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
        # asset.z_index lets ground-layer overlays (bridges, decals) render
        # below NPCs regardless of where the NPC's feet are along the y axis.
        # Default 0 keeps the previous y-sort behavior for everything else.
        container.z_index = asset_z_index
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
    attach_state_light(container, state_info)
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
    var headers_arr = Auth.auth_headers()
    http.request(api_base + "/api/village/objects", headers_arr, HTTPClient.METHOD_POST, payload)

## Add a new object to the world (from the editor).
## Returns the created container node so the editor can auto-select it.
func add_object(asset_id: String, world_pos: Vector2) -> Node2D:
    var state_info = Catalog.get_state(asset_id)
    if state_info == null:
        return null

    var texture: AtlasTexture = Catalog.get_sprite_texture(state_info)
    if texture == null:
        return null

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
    attach_state_light(container, state_info)
    objects_node.add_child(container)
    # Force visual refresh — same HTML5 y-sort renderer bug as drag-to-move
    container.visible = false
    container.visible = true

    _save_object(asset_id, default_state, world_pos, container)
    return container

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
    var headers_arr = Auth.auth_headers()
    http.request(api_base + "/api/village/objects", headers_arr, HTTPClient.METHOD_POST, payload)

func _on_object_saved(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, node: Node2D) -> void:
    http.queue_free()

    if not Auth.check_response(response_code):
        return
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

## Remove an object from the world and the server. Single chokepoint for
## every delete path (keyboard shortcut, sidebar Delete button, anything
## else) — the confirmation dialog lives here so every caller is gated
## without per-call dialog wiring. The actual destructive work runs only
## after the user confirms.
func remove_object(node: Node2D) -> void:
    var obj_id = node.get_meta("object_id", null)
    if obj_id == null:
        node.queue_free()
        return
    var label: String = node.get_meta("display_name", "")
    if label == "":
        label = "this object"
    var dialog := ConfirmationDialog.new()
    dialog.title = "Delete object"
    dialog.dialog_text = "Delete \"" + label + "\"? This cannot be undone."
    dialog.dialog_hide_on_ok = true
    add_child(dialog)
    dialog.confirmed.connect(func():
        placed_objects.erase(obj_id)
        _delete_object(obj_id)
        if is_instance_valid(node):
            node.queue_free()
        dialog.queue_free()
    )
    dialog.canceled.connect(func(): dialog.queue_free())
    dialog.popup_centered()

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

    var headers_arr = Auth.auth_headers()
    http.request_completed.connect(func(r, c, h, b):
        http.queue_free()
        Auth.check_response(c)
    )
    http.request(api_base + "/api/village/objects/" + str(obj_id) + "/position", headers_arr, HTTPClient.METHOD_PATCH, payload)

func _delete_object(obj_id) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    var headers: PackedStringArray = Auth.auth_headers(false)
    http.request_completed.connect(func(r, c, h, b):
        http.queue_free()
        Auth.check_response(c)
    )
    http.request(api_base + "/api/village/objects/" + str(obj_id), headers, HTTPClient.METHOD_DELETE)

## Fetch village agents to build the owner display name lookup.
func _load_agents() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    http.request_completed.connect(_on_agents_loaded.bind(http))
    var headers: PackedStringArray = Auth.auth_headers(false)
    http.request(api_base + "/api/village/agents", headers)

func _on_agents_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()

    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_error("Failed to load agents: result=" + str(result) + " code=" + str(response_code))
        return

    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null or not (json is Array):
        return

    # Reset before refilling — load_agents can run again (WS reconnect
    # resync, re-auth flow) and appending would leave duplicates in the
    # editor panel's agent dropdown.
    agent_list.clear()
    agent_names.clear()
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

    var headers_arr = Auth.auth_headers()
    http.request_completed.connect(func(r, c, h, b):
        http.queue_free()
        Auth.check_response(c)
    )
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

    var headers_arr = Auth.auth_headers()
    http.request_completed.connect(func(r, c, h, b):
        http.queue_free()
        Auth.check_response(c)
    )
    http.request(api_base + "/api/village/objects/" + str(obj_id) + "/name", headers_arr, HTTPClient.METHOD_PATCH, payload)

## Set the display name of an NPC and persist to the server.
## Stashes the name on the container immediately for optimistic UI; the
## npc_display_name_changed WS broadcast echoes it back and is idempotent.
func set_npc_display_name(container: Node2D, display_name: String) -> void:
    var npc_id = container.get_meta("npc_id", null)
    if npc_id == null:
        return
    container.set_meta("display_name", display_name)
    _patch_npc(npc_id, "display-name", {"display_name": display_name})

## Set the behavior of an NPC (or clear it if behavior == ""). Persists to
## the server. Empty string is normalized to null in the payload.
func set_npc_behavior(container: Node2D, behavior: String) -> void:
    var npc_id = container.get_meta("npc_id", null)
    if npc_id == null:
        return
    var value = null
    if behavior != "":
        value = behavior
        container.set_meta("behavior", behavior)
    else:
        container.remove_meta("behavior")
    _patch_npc(npc_id, "behavior", {"behavior": value})

## Link or unlink the llm_memory_agent for an NPC. Empty string unlinks.
func set_npc_agent(container: Node2D, agent: String) -> void:
    var npc_id = container.get_meta("npc_id", null)
    if npc_id == null:
        return
    var value = null
    if agent != "":
        value = agent
        container.set_meta("llm_memory_agent", agent)
    else:
        container.remove_meta("llm_memory_agent")
    _patch_npc(npc_id, "agent", {"llm_memory_agent": value})

## Link or unlink the home structure for an NPC. Empty string unlinks.
func set_npc_home_structure(container: Node2D, structure_id: String) -> void:
    _set_npc_structure(container, "home-structure", "home_structure_id", structure_id)

## Link or unlink the work structure for an NPC. Empty string unlinks.
func set_npc_work_structure(container: Node2D, structure_id: String) -> void:
    _set_npc_structure(container, "work-structure", "work_structure_id", structure_id)

## Update the NPC's schedule in one atomic PATCH. start_min/end_min are
## sent as null when -1 (worker inherits dawn/dusk); otherwise as
## integers. interval/start/end are likewise null when -1 (cadence
## disabled). The server's all-or-none CHECKs guarantee consistent state.
## lateness is the per-NPC lateness_window_minutes (ZBBS-067).
func set_npc_schedule(container: Node2D, start_min: int, end_min: int, interval: int, start_h: int, end_h: int, lateness: int) -> void:
    var npc_id = container.get_meta("npc_id", null)
    if npc_id == null:
        return
    var payload: Dictionary = {
        "lateness_window_minutes": lateness,
    }
    if start_min >= 0 and end_min >= 0:
        payload["schedule_start_minute"] = start_min
        payload["schedule_end_minute"] = end_min
    else:
        payload["schedule_start_minute"] = null
        payload["schedule_end_minute"] = null
    if interval >= 0:
        payload["schedule_interval_hours"] = interval
        payload["active_start_hour"] = start_h
        payload["active_end_hour"] = end_h
    else:
        payload["schedule_interval_hours"] = null
        payload["active_start_hour"] = null
        payload["active_end_hour"] = null
    container.set_meta("lateness_window_minutes", lateness)
    if start_min >= 0 and end_min >= 0:
        container.set_meta("schedule_start_minute", start_min)
        container.set_meta("schedule_end_minute", end_min)
    else:
        container.remove_meta("schedule_start_minute")
        container.remove_meta("schedule_end_minute")
    if interval >= 0:
        container.set_meta("schedule_interval_hours", interval)
        container.set_meta("active_start_hour", start_h)
        container.set_meta("active_end_hour", end_h)
    else:
        container.remove_meta("schedule_interval_hours")
        container.remove_meta("active_start_hour")
        container.remove_meta("active_end_hour")
    _patch_npc(npc_id, "schedule", payload)

## Update the NPC's social-hour overlay (ZBBS-068, minute-precision since
## ZBBS-071). Empty tag clears the schedule server-side (payload sends
## nulls for all three fields). Otherwise tag + start_min + end_min are
## committed atomically.
func set_npc_social(container: Node2D, tag: String, start_min: int, end_min: int) -> void:
    var npc_id = container.get_meta("npc_id", null)
    if npc_id == null:
        return
    var payload: Dictionary = {}
    if tag == "":
        payload["social_tag"] = null
        payload["social_start_minute"] = null
        payload["social_end_minute"] = null
        container.remove_meta("social_tag")
        container.remove_meta("social_start_minute")
        container.remove_meta("social_end_minute")
    else:
        payload["social_tag"] = tag
        payload["social_start_minute"] = start_min
        payload["social_end_minute"] = end_min
        container.set_meta("social_tag", tag)
        container.set_meta("social_start_minute", start_min)
        container.set_meta("social_end_minute", end_min)
    _patch_npc(npc_id, "social", payload)

## POST /api/village/objects/{id}/tags — add a per-instance tag. The WS
## event village_object_tags_updated comes back with the authoritative set
## and refreshes the selection panel if it's showing this object.
func add_object_tag(object_id: String, tag: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, code, _h, _b):
        http.queue_free()
        if code >= 300:
            push_error("Add object tag failed: " + str(code))
    )
    var headers: PackedStringArray = Auth.auth_headers()
    var url: String = Auth.api_base + "/api/village/objects/" + object_id + "/tags"
    var body: String = JSON.stringify({"tag": tag})
    http.request(url, headers, HTTPClient.METHOD_POST, body)

## DELETE /api/village/objects/{id}/tags/{tag}.
func remove_object_tag(object_id: String, tag: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, code, _h, _b):
        http.queue_free()
        if code >= 300:
            push_error("Remove object tag failed: " + str(code))
    )
    var headers: PackedStringArray = Auth.auth_headers(false)
    var url: String = Auth.api_base + "/api/village/objects/" + object_id + "/tags/" + tag
    http.request(url, headers, HTTPClient.METHOD_DELETE)

## PATCH /api/village/objects/{id}/loiter-offset — set the per-instance
## loiter offset where visiting NPCs stand. Both values are tile-unit ints,
## or both null to clear the override (engine falls back to door_offset).
func set_object_loiter_offset(object_id: String, ox, oy) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, code, _h, _b):
        http.queue_free()
        if code >= 300:
            push_error("Set loiter offset failed: " + str(code))
    )
    var headers: PackedStringArray = Auth.auth_headers()
    var url: String = Auth.api_base + "/api/village/objects/" + object_id + "/loiter-offset"
    var body_dict: Dictionary = {
        "loiter_offset_x": ox,
        "loiter_offset_y": oy,
    }
    http.request(url, headers, HTTPClient.METHOD_PATCH, JSON.stringify(body_dict))

## WS event — loiter offset changed (us or another admin). Update meta
## and emit a signal so the marker can repaint if this object is selected.
signal object_loiter_offset_changed(object_id: String, ox, oy)

func apply_object_loiter_offset_changed(data: Dictionary) -> void:
    var object_id: String = str(data.get("id", ""))
    if object_id == "" or not placed_objects.has(object_id):
        return
    var node: Node2D = placed_objects[object_id]
    var ox = data.get("loiter_offset_x", null)
    var oy = data.get("loiter_offset_y", null)
    node.set_meta("loiter_offset_x", ox)
    node.set_meta("loiter_offset_y", oy)
    # Server recomputes effective on every change and broadcasts both;
    # store both so the marker stays in sync without recomputing.
    node.set_meta("effective_loiter_offset_x", data.get("effective_loiter_offset_x", null))
    node.set_meta("effective_loiter_offset_y", data.get("effective_loiter_offset_y", null))
    object_loiter_offset_changed.emit(object_id, ox, oy)

## WS event — another admin (or ourselves) added or removed a tag on a
## placed object. Update our container meta, then fan out a local signal
## so the selection panel re-renders its tag chips if this object is open.
signal object_tags_updated(object_id: String, tags: Array)

## WS event — an NPC (or PC) spoke. Carries id/name/text/kind so the
## talk panel and the speech bubble manager can both subscribe. id is the
## actor.id (NPC or PC) so the bubble manager can attach a bubble Node as
## a child of the right container in placed_npcs / placed_pcs. Emitted by
## event_client when an `npc_spoke` event lands.
signal npc_spoke(npc_id: String, name: String, text: String, kind: String)

func apply_npc_spoke(data: Dictionary) -> void:
    var npc_id: String = str(data.get("npc_id", ""))
    var name: String = str(data.get("name", ""))
    var text: String = str(data.get("text", ""))
    var kind: String = str(data.get("kind", "npc"))
    if name == "" or text == "":
        return
    _spawn_speech_bubble(npc_id, text)
    npc_spoke.emit(npc_id, name, text, kind)


## Spawn a SpeechBubble child on the speaker's container (NPC or PC).
## Replaces an existing bubble on the same speaker — same NPC speaking
## twice in quick succession should show the latest line, not stack.
## No-op when the speaker isn't currently rendered (off-village PC,
## NPC not yet bootstrapped) — the speech still surfaces in the talk
## panel's room log via the npc_spoke signal.
func _spawn_speech_bubble(npc_id: String, text: String) -> void:
    if npc_id == "" or text == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    var existing: Node = container.get_node_or_null(SPEECH_BUBBLE_NODE_NAME)
    if existing != null:
        existing.queue_free()
    var bubble: Node2D = SpeechBubbleScript.new()
    bubble.name = SPEECH_BUBBLE_NODE_NAME
    container.add_child(bubble)
    bubble.setup(text)

## WS event — generic narration-worthy thing happened in a room.
## Covers act/departure/(future arrival, pay, ...). Subscribers filter
## by structure_id; the data dict carries actor_name, kind, pre-rendered
## text, and the structure_id where it happened. Emitted by event_client
## when a `room_event` event lands.
##
## Speech is intentionally still on its own npc_spoke channel — different
## visual treatment (quoted dialogue vs italic narration) and different
## subscribers, so folding it under room_event would require migrating
## existing consumers (editor's loiter-marker repaint hook, etc.). Worth
## doing as a follow-up; out of scope for the room-events landing.
signal room_event(data: Dictionary)

func apply_room_event(data: Dictionary) -> void:
    var actor_name: String = str(data.get("actor_name", ""))
    var text: String = str(data.get("text", ""))
    if actor_name == "" or text == "":
        return
    room_event.emit(data)

## ZBBS-087 — village-wide log feed (talk_panel Village tab subscribes).
signal village_event_added(data: Dictionary)

func apply_village_event_added(data: Dictionary) -> void:
    village_event_added.emit(data)

## ZBBS-087 — chronicler atmosphere prose for the top-bar marquee ticker.
signal world_environment_added(data: Dictionary)

func apply_world_environment_added(data: Dictionary) -> void:
    world_environment_added.emit(data)

func apply_object_tags_updated(data: Dictionary) -> void:
    var object_id: String = str(data.get("object_id", ""))
    if object_id == "" or not placed_objects.has(object_id):
        return
    var tags_raw = data.get("tags", [])
    var tags: Array = tags_raw if tags_raw is Array else []
    var node: Node2D = placed_objects[object_id]
    node.set_meta("tags", tags)
    object_tags_updated.emit(object_id, tags)

## WS event — admin reset-needs (or the future well mechanic) changed
## an NPC's hunger/thirst/tiredness. Patch the container metas and emit
## npc_metadata_changed so the editor panel refreshes its readout if
## this NPC is the current selection.
func apply_npc_needs_changed(data: Dictionary) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    if data.has("hunger"):
        container.set_meta("hunger", int(data.get("hunger", 0)))
    if data.has("thirst"):
        container.set_meta("thirst", int(data.get("thirst", 0)))
    if data.has("tiredness"):
        container.set_meta("tiredness", int(data.get("tiredness", 0)))
    npc_metadata_changed.emit(npc_id)

## WS event — another admin edited the social-hour schedule. Update our
## container meta and tell the panel to refresh if it's the selected NPC.
func apply_npc_social_updated(data: Dictionary) -> void:
    var npc_id: String = data.get("id", "")
    if npc_id == "":
        return
    var container: Node2D = placed_npcs.get(npc_id, null)
    if container == null:
        return
    var tag = data.get("social_tag", null)
    var start_min = data.get("social_start_minute", null)
    var end_min = data.get("social_end_minute", null)
    if tag == null or tag == "" or start_min == null or end_min == null:
        container.remove_meta("social_tag")
        container.remove_meta("social_start_minute")
        container.remove_meta("social_end_minute")
    else:
        container.set_meta("social_tag", str(tag))
        container.set_meta("social_start_minute", int(start_min))
        container.set_meta("social_end_minute", int(end_min))
    npc_metadata_changed.emit(npc_id)

func _set_npc_structure(container: Node2D, suffix: String, field: String, structure_id: String) -> void:
    var npc_id = container.get_meta("npc_id", null)
    if npc_id == null:
        return
    var value = null
    if structure_id != "":
        value = structure_id
    container.set_meta(field, structure_id)
    _patch_npc(npc_id, suffix, {field: value})

## Returns all placed objects in the 'structure' asset category as
## [{id, label}] dicts, sorted by label. Used to populate the editor's
## Home / Work dropdowns.
func get_structure_objects() -> Array:
    var out: Array = []
    for obj_id in placed_objects:
        var node: Node2D = placed_objects[obj_id]
        var asset_id: String = node.get_meta("asset_id", "")
        if asset_id == "":
            continue
        var asset: Dictionary = Catalog.assets.get(asset_id, {})
        # Eligibility for home/work dropdown: physically a building.
        # Per-instance entry_policy gates runtime entry but doesn't
        # exclude a structure from being SOMEONE'S home or work — in
        # fact, having an associated NPC is what unlocks the 'owner'
        # policy (server validates).
        if str(asset.get("category", "")) != "structure":
            continue
        var display_name: String = node.get_meta("display_name", "")
        var label: String = display_name if display_name != "" else asset.get("name", asset_id)
        out.append({"id": str(obj_id), "label": label})
    out.sort_custom(func(a, b): return a.label < b.label)
    return out

func _patch_npc(npc_id, suffix: String, payload_dict: Dictionary) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    var headers_arr = Auth.auth_headers()
    http.request_completed.connect(func(r, c, h, b):
        http.queue_free()
        Auth.check_response(c)
    )
    http.request(
        api_base + "/api/village/npcs/" + str(npc_id) + "/" + suffix,
        headers_arr,
        HTTPClient.METHOD_PATCH,
        JSON.stringify(payload_dict)
    )

## Resolve an owner identifier to a display name.
## Returns the display name if found, otherwise the raw owner string.
func get_owner_display_name(owner: String) -> String:
    if owner == "":
        return ""
    return agent_names.get(owner, owner)
