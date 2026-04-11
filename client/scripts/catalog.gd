extends Node
## Autoloaded singleton — loads the asset catalog from the Go API.
## Other scripts access it via the global `Catalog` name.

signal catalog_loaded

# True once the catalog has been fetched and parsed
var loaded: bool = false

# All assets keyed by asset id
var assets: Dictionary = {}

# Assets grouped by category
var categories: Dictionary = {}

# Tileset packs keyed by pack id
var packs: Dictionary = {}

# Sprite sheet texture cache — keyed by sheet path, value is Texture2D
var sheet_cache: Dictionary = {}

# Base URL for the Go API
var api_base: String = ""

func _ready() -> void:
    # Determine the API base URL.
    # Godot's HTTPRequest needs full URLs, even in the browser.
    if OS.has_feature("web"):
        # Get the page origin via JavaScript (e.g. "https://village.llm-memory.net")
        api_base = JavaScriptBridge.eval("window.location.origin", true)
    else:
        api_base = "http://zbbs.local"

    _load_catalog()

func _load_catalog() -> void:
    var http = HTTPRequest.new()
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
    loaded = true
    catalog_loaded.emit()

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
## Returns a dictionary with sheet, srcX, srcY, srcW, srcH
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
        state = asset.get("defaultState", "default")

    for s in states:
        if s.get("state", "") == state:
            return s

    # Fallback to first state
    return states[0]

## Load and cache a spritesheet texture.
## Sheet paths from the API are like "/assets/tilesets/mana-seed/..."
## In Godot these map to "res://assets/tilesets/mana-seed/..."
func get_sheet_texture(sheet_path: String) -> Texture2D:
    if sheet_cache.has(sheet_path):
        return sheet_cache[sheet_path]

    # Convert API path to Godot resource path
    var res_path: String = "res:/" + sheet_path
    if not ResourceLoader.exists(res_path):
        push_error("Sheet not found: " + res_path)
        return null

    var texture: Texture2D = load(res_path)
    sheet_cache[sheet_path] = texture
    return texture

## Get an AtlasTexture for a specific sprite on a sheet.
## This is the main way to get a drawable texture for an asset state.
func get_sprite_texture(state_info: Dictionary) -> AtlasTexture:
    var sheet_path: String = state_info.get("sheet", "")
    var sheet_texture: Texture2D = get_sheet_texture(sheet_path)
    if sheet_texture == null:
        return null

    var atlas = AtlasTexture.new()
    atlas.atlas = sheet_texture
    atlas.region = Rect2(
        state_info.get("srcX", 0),
        state_info.get("srcY", 0),
        state_info.get("srcW", 0),
        state_info.get("srcH", 0)
    )
    return atlas
