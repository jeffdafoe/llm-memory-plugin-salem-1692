extends Node
## Autoloaded singleton — loads the asset catalog from the Go API.
## Downloads spritesheets via HTTP and caches them as textures.
## Other scripts access it via the global `Catalog` name.

signal catalog_loaded
signal npc_behaviors_loaded
signal state_tags_loaded
signal object_tags_loaded
# Fired when the WS asset_state_tags_updated event lands — the asset popup
# subscribes to this so an open inspector refreshes after another admin
# adds or removes a tag.
signal state_tags_changed(asset_id: String, state: String, tags: Array)

# True once the catalog AND all sheets have been fetched
var loaded: bool = false

# All assets keyed by asset id
var assets: Dictionary = {}

# Assets grouped by category
var categories: Dictionary = {}

# Tileset packs keyed by pack id
var packs: Dictionary = {}

# Sprite sheet texture cache — keyed by sheet path, value is ImageTexture
var sheet_cache: Dictionary = {}

# Sheets currently being downloaded
var _pending_sheets: int = 0

# NPC behavior catalog — array of {slug, display_name} dictionaries
# Populated asynchronously after _ready(); editor_panel reads this when the
# NPC behavior dropdown is built. Small list (single digits), fetch in parallel
# with the main asset catalog.
var npc_behaviors: Array = []
var npc_behaviors_loaded_flag: bool = false

# State-tag allowlist — server-side truth for what state tags the editor can
# apply / filter by. Same shape as npc_behaviors: small array loaded once
# after login. Consumers (social-hour dropdown, state-tag editor) subscribe
# to state_tags_loaded.
var state_tags: Array = []
var state_tags_loaded_flag: bool = false

# Per-instance object tag allowlist (ZBBS-069). Different from state_tags
# above — this controls what tags you can apply to individual placed
# objects via the selection panel (e.g. 'tavern').
var object_tags: Array = []
var object_tags_loaded_flag: bool = false

# Base URL for the Go API
var api_base: String = ""

func _ready() -> void:
    if OS.has_feature("web"):
        api_base = JavaScriptBridge.eval("window.location.origin", true)
    else:
        api_base = "http://zbbs.local"

    _load_catalog()

    # npc-behaviors is an authed endpoint. Defer until login completes —
    # autoload _ready() runs before the user has a session token.
    if Auth.authenticated:
        _load_npc_behaviors()
        _load_state_tags()
        _load_object_tags()
    else:
        Auth.logged_in.connect(_load_npc_behaviors)
        Auth.logged_in.connect(_load_state_tags)
        Auth.logged_in.connect(_load_object_tags)

func _load_catalog() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    http.request_completed.connect(_on_catalog_loaded.bind(http))
    var error = http.request(api_base + "/api/assets")
    if error != OK:
        push_error("Failed to request asset catalog: " + str(error))

func _on_catalog_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()

    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_error("Catalog request failed: result=" + str(result) + " code=" + str(response_code))
        return

    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null:
        push_error("Failed to parse catalog JSON")
        return

    _parse_catalog(json)

    # Collect all unique sheet paths and download them
    var unique_sheets: Dictionary = {}
    for asset_id in assets:
        var asset = assets[asset_id]
        for state in asset.get("states", []):
            var sheet: String = state.get("sheet", "")
            if sheet != "" and not unique_sheets.has(sheet):
                unique_sheets[sheet] = true

    _pending_sheets = unique_sheets.size()
    if _pending_sheets == 0:
        loaded = true
        catalog_loaded.emit()
        return

    print("Catalog: downloading ", _pending_sheets, " spritesheets...")
    for sheet_path in unique_sheets:
        _download_sheet(sheet_path)

func _download_sheet(sheet_path: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    # Sheet paths are like "/tilesets/mana-seed/..." — served by nginx
    var url: String = api_base + sheet_path

    http.request_completed.connect(_on_sheet_downloaded.bind(http, sheet_path))
    var err = http.request(url)
    if err != OK:
        push_error("Failed to request sheet: " + sheet_path + " error=" + str(err))
        _pending_sheets -= 1
        _check_all_sheets_loaded()

func _on_sheet_downloaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest, sheet_path: String) -> void:
    http.queue_free()

    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_error("Sheet download failed: " + sheet_path + " code=" + str(response_code))
        _pending_sheets -= 1
        _check_all_sheets_loaded()
        return

    # Create an Image from the PNG data and convert to ImageTexture
    var image = Image.new()
    var err = image.load_png_from_buffer(body)
    if err != OK:
        push_error("Failed to decode sheet PNG: " + sheet_path)
        _pending_sheets -= 1
        _check_all_sheets_loaded()
        return

    var texture = ImageTexture.create_from_image(image)
    sheet_cache[sheet_path] = texture

    _pending_sheets -= 1
    _check_all_sheets_loaded()

func _check_all_sheets_loaded() -> void:
    if _pending_sheets <= 0:
        print("Catalog: all sheets loaded (", sheet_cache.size(), " textures)")
        loaded = true
        catalog_loaded.emit()

func _load_npc_behaviors() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    http.request_completed.connect(_on_npc_behaviors_loaded.bind(http))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    var err = http.request(api_base + "/api/village/npc-behaviors", headers)
    if err != OK:
        push_error("Failed to request npc behaviors: " + str(err))

func _on_npc_behaviors_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()

    if not Auth.check_response(response_code):
        return

    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_error("NPC behaviors request failed: result=" + str(result) + " code=" + str(response_code))
        return

    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null or not (json is Array):
        push_error("Failed to parse npc-behaviors JSON")
        return

    npc_behaviors = json
    npc_behaviors_loaded_flag = true
    npc_behaviors_loaded.emit()

func _load_state_tags() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_state_tags_loaded.bind(http))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    var err = http.request(api_base + "/api/assets/state-tags", headers)
    if err != OK:
        push_error("Failed to request state tags: " + str(err))

func _on_state_tags_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_error("State tags request failed: result=" + str(result) + " code=" + str(response_code))
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null or not (json is Array):
        push_error("Failed to parse state-tags JSON")
        return
    state_tags = json
    state_tags_loaded_flag = true
    state_tags_loaded.emit()

func _load_object_tags() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_object_tags_loaded.bind(http))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    var err = http.request(api_base + "/api/village/object-tags", headers)
    if err != OK:
        push_error("Failed to request object tags: " + str(err))

func _on_object_tags_loaded(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_error("Object tags request failed: result=" + str(result) + " code=" + str(response_code))
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null or not (json is Array):
        push_error("Failed to parse object-tags JSON")
        return
    object_tags = json
    object_tags_loaded_flag = true
    object_tags_loaded.emit()

## Called from event_client when the WS asset_state_tags_updated event
## arrives. Updates our cached copy of the asset's state tags so downstream
## reads (asset popup tag editor, future filters) see fresh data, then
## fans out state_tags_changed for UI subscribers.
func apply_state_tags_updated(asset_id: String, state: String, tags: Array) -> void:
    var asset = assets.get(asset_id, null)
    if asset != null:
        for s in asset.get("states", []):
            if s.get("state", "") == state:
                s["tags"] = tags
                break
    state_tags_changed.emit(asset_id, state, tags)

func _parse_catalog(data: Array) -> void:
    for item in data:
        var asset_id: String = item["id"]
        assets[asset_id] = item

        # Group by category
        var cat: String = item.get("category", "uncategorized")
        if not categories.has(cat):
            categories[cat] = []
        categories[cat].append(item)

        # Track packs
        var pack = item.get("pack", null)
        if pack != null and pack is Dictionary:
            var pack_id: String = pack.get("id", "")
            if pack_id != "" and not packs.has(pack_id):
                packs[pack_id] = pack

## Get the sprite info for an asset in a given state.
## Returns a dictionary with sheet, src_x, src_y, src_w, src_h
## or null if not found.
func get_state(asset_id: String, state: String = "") -> Variant:
    var asset = assets.get(asset_id)
    if asset == null:
        return null

    var states: Array = asset.get("states", [])
    if states.is_empty():
        return null

    # If no state specified, use the asset's default_state or first state
    if state == "":
        state = asset.get("defaultState", asset.get("default_state", "default"))

    for s in states:
        if s.get("state", "") == state:
            return s

    # Fallback to first state
    return states[0]

## Get the cached texture for a spritesheet.
func get_sheet_texture(sheet_path: String) -> Texture2D:
    return sheet_cache.get(sheet_path)

## Returns true if a state has animation frames (frame_count > 1).
func is_animated(state_info: Dictionary) -> bool:
    var frame_count: int = state_info.get("frame_count", 1)
    return frame_count > 1

## Build a SpriteFrames resource for an animated state.
## Returns null if the state is static (frame_count <= 1) or the sheet isn't loaded.
## The animation is named "default" and loops automatically.
func get_sprite_frames(state_info: Dictionary) -> SpriteFrames:
    var frame_count: int = state_info.get("frame_count", 1)
    var frame_rate: float = state_info.get("frame_rate", 0.0)
    if frame_count <= 1:
        return null

    var sheet_path: String = state_info.get("sheet", "")
    var sheet_texture: Texture2D = get_sheet_texture(sheet_path)
    if sheet_texture == null:
        return null

    var src_x: int = state_info.get("src_x", state_info.get("srcX", 0))
    var src_y: int = state_info.get("src_y", state_info.get("srcY", 0))
    var src_w: int = state_info.get("src_w", state_info.get("srcW", 0))
    var src_h: int = state_info.get("src_h", state_info.get("srcH", 0))

    var frames = SpriteFrames.new()
    # SpriteFrames comes with a "default" animation — configure it
    frames.set_animation_speed("default", frame_rate)
    frames.set_animation_loop("default", true)

    # Remove the default empty frame that SpriteFrames starts with
    if frames.get_frame_count("default") > 0:
        frames.remove_frame("default", 0)

    # Build atlas textures for each frame — consecutive horizontally
    for i in range(frame_count):
        var atlas = AtlasTexture.new()
        atlas.atlas = sheet_texture
        atlas.region = Rect2(src_x + (i * src_w), src_y, src_w, src_h)
        frames.add_frame("default", atlas)

    return frames

## Get all assets that fit a given slot name.
## Returns an array of asset dictionaries.
func get_assets_for_slot(slot_name: String) -> Array:
    var result: Array = []
    for asset_id in assets:
        var asset = assets[asset_id]
        var fits = asset.get("fits_slot", null)
        # fits_slot can be null (JSON null) or a string — only match actual strings
        if fits != null and fits is String and fits == slot_name:
            result.append(asset)
    result.sort_custom(func(a, b): return a.get("name", "") < b.get("name", ""))
    return result

## Get the slots defined on an asset.
## Returns an array of slot dictionaries [{slot_name, offset_x, offset_y}].
func get_slots(asset_id: String) -> Array:
    var asset = assets.get(asset_id)
    if asset == null:
        return []
    return asset.get("slots", [])

## Get an AtlasTexture for a specific sprite on a sheet.
## This is the main way to get a drawable texture for an asset state.
func get_sprite_texture(state_info: Dictionary) -> AtlasTexture:
    var sheet_path: String = state_info.get("sheet", "")
    var sheet_texture: Texture2D = get_sheet_texture(sheet_path)
    if sheet_texture == null:
        push_warning("Sheet not loaded: " + sheet_path)
        return null

    var atlas = AtlasTexture.new()
    atlas.atlas = sheet_texture
    atlas.region = Rect2(
        state_info.get("src_x", state_info.get("srcX", 0)),
        state_info.get("src_y", state_info.get("srcY", 0)),
        state_info.get("src_w", state_info.get("srcW", 0)),
        state_info.get("src_h", state_info.get("srcH", 0))
    )
    return atlas
