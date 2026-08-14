extends Node2D
## Drifting cloud shadows (LLM-633). An always-on, world-space layer of
## sparse cloud-shadow blobs that slides slowly across the village — sky
## movement over the map, visible whenever the ground is lit. Deliberately
## NOT storm-coupled: a shadow needs sunlight to read, and during a storm the
## tint has already darkened the world ~50%, so storm coupling would put the
## effect exactly where it shows least. At night the day/night CanvasModulate
## darkens the shadows along with the ground and they fade naturally.
##
## Art: Seliel "weather effects 2.0" cloud-cover autotile, 3x8 tiles of 16px.
## The sheet is two complete 3x3 rounded blobs (two corner styles) plus six
## inner-corner tiles. We never need the inner corners: the clouds are OUR
## procedural shapes, so instead of autotiling arbitrary blobs we stamp
## whole rounded-rectangle clouds — corners, edges repeated between them,
## center fill — at any size from 2x2 up. The art is baked at 35% alpha in a
## dark blue-purple: the artist pre-tuned "shadow, not lid", so no modulate.
##
## World-space, unlike storm_fx's screen-space overlay: the layer scales and
## pans with the camera exactly like the terrain under it, so the LLM-632
## integer-scale rule does not apply here — there is no independent screen
## scale to snap, and drift is plain world pixels.
##
## Wrap: the cloud field is generated on a fixed grid the size of the map and
## treated as an x-torus — a cloud crossing the right edge continues from the
## left. Two identical TileMapLayer copies sit side by side one period apart,
## and the drift position wraps by one period, so the slide never runs out
## and the seam never shows. Cells never leave the map's rect (the splash
## lesson: weather stops at the map edge, not the camera edge).
##
## Layering: z_index 20 — over objects and NPCs (OBJECT_Z 10), under every
## CanvasLayer (UI, and the storm overlay's rain/tint/lightning, which the
## artist says belong above cloud shadow).

const SHEET: String = "res://assets/tilesets/mana-seed/weather-effects/weather effects, cloud cover autotile 16x16.png"
const TILE: int = 16

## Matches world.gd's asset render scale: one 16px cloud tile covers one 32px
## village tile.
const RENDER_SCALE: float = 2.0

## Over objects/NPCs (world.gd OBJECT_Z = 10), under CanvasLayers.
const CLOUD_Z: int = 20

## Field geometry fallback — world.gd's default map grid, used only when no
## world is injected (the test harness). The real dimensions are read off the
## injected world at _ready, so a map-size change is picked up automatically;
## generation stays deterministic for a given (seed, width, height). A
## mid-session dimension change would need a rebuild — none exists today
## (world.gd never reassigns map_width/map_height), and clouds are cosmetic.
const FIELD_W_FALLBACK: int = 200
const FIELD_H_FALLBACK: int = 180

## Wind. Rightward like the rain's lean, slower than the rain sheets — cloud
## shadow crosses a zoom-1 viewport in a couple of minutes.
const DRIFT_PPS: float = 8.0

## Cloud population — scattered shadows, not an overcast lid. Minimum size is
## 3x3, the artist's own blob: the corner tiles carry cloud only in their
## inner halves, so a 2-tall cloud renders as a thin sliver and a 2x2 as a
## floating speck (seen in the offline preview, not a theory).
const CLOUD_COUNT: int = 90
const CLOUD_MIN_W: int = 3
const CLOUD_MAX_W: int = 9
const CLOUD_MIN_H: int = 3
const CLOUD_MAX_H: int = 6

## Fixed seed: every client sees the same cloud field (drift phase still
## differs by when the client loaded — cosmetic, nothing references clouds).
const FIELD_SEED: int = 16920814

## Injected by world.gd BEFORE add_child (so _ready sees it). Duck-typed:
## reads map_width / map_height once at _ready for the field grid, and
## pad_x / pad_y each frame for where the map's NW corner sits.
var world: Node2D = null

var _drift: float = 0.0
## The generated field: Vector2i cell -> Vector2i atlas coords. Kept for the
## harness; the TileMapLayer copies are built from it once at _ready.
var _cells: Dictionary = {}
var _layers: Array[TileMapLayer] = []

## Field grid + x-torus period, resolved at _ready (see FIELD_W_FALLBACK).
var _field_w: int = FIELD_W_FALLBACK
var _field_h: int = FIELD_H_FALLBACK
var _period: float = FIELD_W_FALLBACK * TILE * RENDER_SCALE


func _ready() -> void:
    z_index = CLOUD_Z
    if world != null:
        _field_w = world.map_width
        _field_h = world.map_height
        _period = _field_w * TILE * RENDER_SCALE
    var sheet: Texture2D = _load_sheet(SHEET)
    if sheet == null:
        # Purchased pack absent (gitignored — CI's normal state): no clouds,
        # no errors, nothing else cares.
        push_warning("cloud_shadows: missing cloud sheet " + SHEET)
        set_process(false)
        return
    _cells = _generate_field(FIELD_SEED)
    _build_layers(sheet)


func _process(delta: float) -> void:
    _drift = fmod(_drift + delta * DRIFT_PPS, _period)
    if world == null:
        return
    # The map's NW corner is pad tiles up-left of world origin (32px village
    # tiles — world.gd world_to_tile).
    position = Vector2(-world.pad_x * 32.0 + _drift, -world.pad_y * 32.0)


## Null when the pack is absent (gitignored purchased art — CI's state).
func _load_sheet(path: String) -> Texture2D:
    return load(path) if ResourceLoader.exists(path) else null


## Scatter CLOUD_COUNT non-overlapping rounded-rectangle clouds on the torus.
## Deterministic for a given seed. Returns cell -> atlas coords.
func _generate_field(seed_value: int) -> Dictionary:
    var rng := RandomNumberGenerator.new()
    rng.seed = seed_value
    var cells: Dictionary = {}
    var placed: int = 0
    # Bounded attempts so a crowded roll can't loop forever; rejection keeps
    # a one-cell margin between clouds, so a failed attempt is just skipped.
    for _attempt in CLOUD_COUNT * 8:
        if placed >= CLOUD_COUNT:
            break
        var w: int = rng.randi_range(CLOUD_MIN_W, CLOUD_MAX_W)
        var h: int = rng.randi_range(CLOUD_MIN_H, CLOUD_MAX_H)
        var x: int = rng.randi_range(0, _field_w - 1)
        var y: int = rng.randi_range(0, _field_h - 1)
        var variant_row: int = rng.randi_range(0, 1) * 3
        if _footprint_is_free(cells, x, y, w, h):
            _stamp_cloud(cells, x, y, w, h, variant_row)
            placed += 1
    return cells


## True when the cloud rect plus a one-cell separation ring holds no cell yet.
## Coordinates wrap on both axes (x for the drift torus, y so placement has
## no edge bias).
func _footprint_is_free(cells: Dictionary, x: int, y: int, w: int, h: int) -> bool:
    for dy in range(-1, h + 1):
        for dx in range(-1, w + 1):
            if cells.has(_wrap(x + dx, y + dy)):
                return false
    return true


## Write one w x h cloud into the field: corner tiles at the corners, edge
## tiles repeated between them, center fill inside. variant_row selects which
## of the two 3x3 blob styles (atlas rows 0-2 or 3-5) supplies the tiles.
func _stamp_cloud(cells: Dictionary, x: int, y: int, w: int, h: int, variant_row: int) -> void:
    for row in h:
        for col in w:
            var cx: int = 0 if col == 0 else (2 if col == w - 1 else 1)
            var cy: int = 0 if row == 0 else (2 if row == h - 1 else 1)
            cells[_wrap(x + col, y + row)] = Vector2i(cx, variant_row + cy)


func _wrap(x: int, y: int) -> Vector2i:
    return Vector2i(posmod(x, _field_w), posmod(y, _field_h))


## Two identical TileMapLayer copies one period apart: as the drift slides
## the pair right, the left copy covers what the right copy exposes, and the
## fmod wrap lands on an identical frame.
func _build_layers(sheet: Texture2D) -> void:
    var atlas := TileSetAtlasSource.new()
    atlas.texture = sheet
    atlas.texture_region_size = Vector2i(TILE, TILE)
    for row in 8:
        for col in 3:
            atlas.create_tile(Vector2i(col, row))
    var tile_set := TileSet.new()
    tile_set.tile_size = Vector2i(TILE, TILE)
    var source_id: int = tile_set.add_source(atlas)

    for copy in 2:
        var layer := TileMapLayer.new()
        layer.tile_set = tile_set
        layer.scale = Vector2(RENDER_SCALE, RENDER_SCALE)
        # Copy 0 at the origin, copy 1 one period to the LEFT (position is in
        # the parent's units, pre-parent-scale — the parent isn't scaled).
        layer.position = Vector2(-copy * _period, 0.0)
        for cell: Vector2i in _cells:
            layer.set_cell(cell, source_id, _cells[cell])
        add_child(layer)
        _layers.append(layer)
