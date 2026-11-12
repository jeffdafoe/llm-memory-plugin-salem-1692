extends Node2D
## The village ground (LLM-967). One rect covers the map; terrain_ground.gdshader
## draws every pixel of it from the map's layer ranks and the season's ground
## atlas. The shader works per pixel, so there are no tile seams at any zoom,
## and the CPU does nothing per frame.
##
## The map reaches the shader as a small R8 texture of layer ranks, one texel
## per map cell. rebuild() writes it after the map changes; refresh_cell()
## changes one texel after a cell is painted, and the texture uploads once on
## the next frame.

const TerrainPack = preload("res://scripts/terrain_pack.gd")
const GroundShader = preload("res://scripts/terrain_ground.gdshader")

# Rendered tile size in world space. The ground textures are drawn one texel
# per world pixel, so this must equal TerrainPack.TILE_PX.
const TILE_SIZE: int = 32

# The ground atlas being drawn — the season's (LLM-834) or the snowy one.
var ground_texture: Texture2D = null
var _sheet_cache: Dictionary = {}

var _material: ShaderMaterial = null
# Layer rank per terrain id (index = terrain id); -1 for an id the pack does
# not draw. Built from TerrainPack.LAYER_ORDER.
var _rank_of: PackedInt32Array = PackedInt32Array()
# Layer rank per map cell, and the texture the shader reads it from.
var _ranks_image: Image = null
var _ranks_texture: ImageTexture = null
# A painted cell is in _ranks_image but not yet in _ranks_texture.
var _upload_pending: bool = false

# Map data reference — set by world.gd via _sync_renderer
var map_data: Array = []
var map_width: int = 200
var map_height: int = 180

# Tile offset (array index to world position); overwritten from world.gd
var pad_x: int = 0
var pad_y: int = 0

func _ready() -> void:
    # A cell painted before the renderer entered the tree still needs its upload.
    set_process(_upload_pending)
    _ensure_material()
    if ground_texture == null:
        _set_ground(_sheet(ground_sheet_path("")))
    if _ranks_image == null and not map_data.is_empty():
        rebuild()

## Layer rank per terrain id, from the pack's LAYER_ORDER.
static func rank_table() -> PackedInt32Array:
    var top_id: int = 0
    for terrain in TerrainPack.LAYER_ORDER:
        top_id = maxi(top_id, terrain)
    var table := PackedInt32Array()
    table.resize(top_id + 1)
    table.fill(-1)
    for rank in TerrainPack.LAYER_ORDER.size():
        table[TerrainPack.LAYER_ORDER[rank]] = rank
    return table

## Bit r set for each rank whose terrain is in terrains.
static func rank_bits(terrains: Array[int]) -> int:
    var bits: int = 0
    for rank in TerrainPack.LAYER_ORDER.size():
        if terrains.has(TerrainPack.LAYER_ORDER[rank]):
            bits |= 1 << rank
    return bits

## The soft-edge terrains for season (see TerrainPack.SOFT_EDGE_TERRAINS).
static func soft_edge_terrains(season: String, snowy: bool = false) -> Array[int]:
    var key: String = "snowy" if snowy else season
    var terrains: Array[int] = []
    terrains.assign(TerrainPack.SOFT_EDGE_TERRAINS.get(key, TerrainPack.SOFT_EDGE_TERRAINS["default"]))
    return terrains

## The ground atlas for season. snowy (snow lies light or deep) is the snowy
## atlas whatever the season; a season the pack does not name draws summer.
static func ground_sheet_path(season: String, snowy: bool = false) -> String:
    if snowy and TerrainPack.SHEETS.has("snowy"):
        return TerrainPack.SHEETS["snowy"]
    return TerrainPack.SHEETS.get(season, TerrainPack.SHEETS["summer"])

## Draw the ground for season (the world read's "autumn"), or snowy.
func set_season(season: String, snowy: bool = false) -> void:
    var texture := _sheet(ground_sheet_path(season, snowy))
    if texture != null:
        _set_ground(texture)
        _material.set_shader_parameter("soft_ranks", rank_bits(soft_edge_terrains(season, snowy)))

func _set_ground(texture: Texture2D) -> void:
    ground_texture = texture
    _ensure_material()
    _material.set_shader_parameter("ground", texture)

func _sheet(path: String) -> Texture2D:
    if not _sheet_cache.has(path):
        _sheet_cache[path] = load(path)
    return _sheet_cache[path]

## The shader and every uniform that comes from the pack. Built once; the
## renderer may be used before it enters the tree (tests, the world's setup).
func _ensure_material() -> void:
    if _material != null:
        return
    assert(TILE_SIZE == TerrainPack.TILE_PX, "terrain pack tiles must be TILE_SIZE world pixels")
    _rank_of = rank_table()
    _material = ShaderMaterial.new()
    _material.shader = GroundShader
    _material.set_shader_parameter("edge_noise", load(TerrainPack.EDGE_NOISE))
    _material.set_shader_parameter("sparkles", load(TerrainPack.SPARKLE_SHEET))
    _material.set_shader_parameter("tile_px", TerrainPack.TILE_PX)
    _material.set_shader_parameter("texture_px", TerrainPack.TEXTURE_PX)
    _material.set_shader_parameter("atlas_cols", TerrainPack.ATLAS_COLS)
    _material.set_shader_parameter("edge_wobble", TerrainPack.EDGE_WOBBLE)
    _material.set_shader_parameter("grass_ranks", rank_bits(TerrainPack.FRINGE_TERRAINS))
    _material.set_shader_parameter("soft_ranks", rank_bits(soft_edge_terrains("")))
    _material.set_shader_parameter("water_ranks", rank_bits(TerrainPack.WATER_TERRAINS))
    _material.set_shader_parameter("sparkle_ranks", rank_bits(TerrainPack.SPARKLE_TERRAINS))
    _material.set_shader_parameter("sparkle_cols", TerrainPack.SPARKLE_COLS)
    _material.set_shader_parameter("sparkle_frames", TerrainPack.SPARKLE_COLS * TerrainPack.SPARKLE_ROWS)
    _material.set_shader_parameter("sparkle_seconds", TerrainPack.SPARKLE_FRAME_SECONDS)
    material = _material

## Write the rank texture from map_data. Call after the map, its size or the
## pad changes.
func rebuild() -> void:
    _ensure_material()
    var bytes := PackedByteArray()
    bytes.resize(map_width * map_height)
    for y in map_height:
        var row: Array = map_data[y]
        for x in map_width:
            bytes[y * map_width + x] = _rank_for(row[x])
    _ranks_image = Image.create_from_data(map_width, map_height, false, Image.FORMAT_R8, bytes)
    _ranks_texture = ImageTexture.create_from_image(_ranks_image)
    _material.set_shader_parameter("ranks", _ranks_texture)
    _material.set_shader_parameter("map_size", Vector2i(map_width, map_height))
    _material.set_shader_parameter("pad", Vector2i(pad_x, pad_y))
    _upload_pending = false
    set_process(false)
    queue_redraw()

## Map cell (x, y) was painted. The rank texture uploads on the next frame,
## once however many cells a brush stroke paints.
func refresh_cell(x: int, y: int) -> void:
    if _ranks_image == null or _ranks_image.get_width() != map_width or _ranks_image.get_height() != map_height:
        rebuild()
        return
    _ranks_image.set_pixel(x, y, Color8(_rank_for(map_data[y][x]), 0, 0))
    _upload_pending = true
    set_process(true)

func _process(_delta: float) -> void:
    _ranks_texture.update(_ranks_image)
    _upload_pending = false
    set_process(false)

## Layer rank of a terrain id. An id the pack does not draw takes the lowest
## layer, so the cell still shows ground.
func _rank_for(terrain: int) -> int:
    if terrain < 0 or terrain >= _rank_of.size() or _rank_of[terrain] < 0:
        return 0
    return _rank_of[terrain]

func _draw() -> void:
    if _ranks_texture == null or ground_texture == null:
        return
    draw_rect(Rect2(-pad_x * TILE_SIZE, -pad_y * TILE_SIZE, map_width * TILE_SIZE, map_height * TILE_SIZE), Color.WHITE)
