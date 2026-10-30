extends Node2D

## The dark at night beyond the day/night tint (LLM-842). Two lights, both in
## the World subtree so they work with the CanvasModulate and with every other
## light:
##
##  - The woods: one SUBTRACT light whose texture is a one-pixel-per-tile mask
##    of the ground near trees. Subtracting, not drawing a dark sprite on top,
##    is what lets a lamp, the night-vision circle or a carried lantern
##    (LLM-846) still light the ground inside the woods: lights add, this one
##    takes away, and they sum.
##  - Night vision: a dim, cool, short light on the player's own character.
##    Only this client draws it, so other players and villagers get no glow
##    and are hard to see in the dark. The lantern widens it.
##
## Both follow the night position (world._phase_pos), so they are gone by day
## and come in through dusk.

const WOODS_RADIUS_TILES := 3.0
## Full darkness inside this many tiles of a tree; fades out to WOODS_RADIUS_TILES.
const WOODS_CORE_TILES := 1.0
## How much of the night tint the woods take away (0.45 = 45% darker).
const WOODS_DARKEN := 0.45
## Trees are re-read this often; a moved, added or deleted tree rebuilds the mask.
const WOODS_RECHECK_SECONDS := 2.0

const VISION_COLOR := Color(0.62, 0.70, 0.85)
const VISION_ENERGY := 0.35
const VISION_RADIUS_PX := 80.0

const TILE := 32.0

var world: Node2D = null
## Asset id -> true when it is a tree. Defaults to the Catalog autoload, looked
## up by path so the script also loads where no autoload runs (headless
## tests); tests replace it.
var is_tree: Callable = _catalog_is_tree

var woods_light: PointLight2D = null
var vision_light: PointLight2D = null
var _trees_signature := ""
var _recheck_left := 0.0

func _ready() -> void:
    woods_light = PointLight2D.new()
    woods_light.name = "WoodsDark"
    woods_light.blend_mode = Light2D.BLEND_MODE_SUB
    woods_light.energy = 0.0
    woods_light.enabled = false
    add_child(woods_light)

func _process(delta: float) -> void:
    if world == null:
        return
    _recheck_left -= delta
    if _recheck_left <= 0.0:
        _recheck_left = WOODS_RECHECK_SECONDS
        refresh_woods()
    var night := night_factor(world._phase_pos)
    _apply_woods(night)
    _apply_vision(night)

func _catalog_is_tree(asset_id: String) -> bool:
    var catalog := get_node_or_null("/root/Catalog")
    if catalog == null:
        return false
    return str(catalog.assets.get(asset_id, {}).get("category", "")) == "tree"

## 0 by day and through most of dusk, rising to 1 at full night. The tint
## itself carries the early dusk; these lights only matter once it is dark.
static func night_factor(phase_pos: float) -> float:
    return clampf((phase_pos - 0.5) * 2.0, 0.0, 1.0)

## Internal grid tile (x, y) of every placed tree.
func tree_tiles() -> Array[Vector2i]:
    var out: Array[Vector2i] = []
    for node in world.placed_objects.values():
        if not is_instance_valid(node):
            continue
        if not is_tree.call(str(node.get_meta("asset_id", ""))):
            continue
        out.append(Vector2i(int(floor(node.position.x / TILE)) + world.pad_x,
                int(floor(node.position.y / TILE)) + world.pad_y))
    return out

## Rebuild the woods mask when the set of tree tiles changed.
func refresh_woods() -> void:
    var tiles := tree_tiles()
    var signature := str(tiles.size()) + ":" + str(tiles)
    if signature == _trees_signature:
        return
    _trees_signature = signature
    if tiles.is_empty():
        woods_light.texture = null
        woods_light.enabled = false
        return
    var mask := build_woods_mask(tiles, world.map_width, world.map_height)
    woods_light.texture = ImageTexture.create_from_image(mask)
    woods_light.texture_scale = TILE
    # A light's texture is centred on the light; put the centre of the grid
    # under it so pixel (x, y) lies over internal tile (x, y).
    woods_light.position = Vector2(
        (world.map_width * 0.5 - world.pad_x) * TILE,
        (world.map_height * 0.5 - world.pad_y) * TILE)
    woods_light.enabled = true

## One pixel per tile: white, alpha 1 within WOODS_CORE_TILES of a tree,
## fading to 0 at WOODS_RADIUS_TILES. Overlapping trees take the strongest
## value, so a group reads as one wood.
static func build_woods_mask(tiles: Array[Vector2i], width: int, height: int) -> Image:
    var img := Image.create(width, height, false, Image.FORMAT_RGBA8)
    img.fill(Color(1, 1, 1, 0))
    var r := int(ceil(WOODS_RADIUS_TILES))
    for t in tiles:
        for dy in range(-r, r + 1):
            for dx in range(-r, r + 1):
                var x := t.x + dx
                var y := t.y + dy
                if x < 0 or y < 0 or x >= width or y >= height:
                    continue
                var d := Vector2(dx, dy).length()
                if d > WOODS_RADIUS_TILES:
                    continue
                var a := 1.0 - smoothstep(WOODS_CORE_TILES, WOODS_RADIUS_TILES, d)
                if a > img.get_pixel(x, y).a:
                    img.set_pixel(x, y, Color(1, 1, 1, a))
    return img

## The woods take away a share of the current tint, so they darken in step
## with the night instead of by a fixed amount that would be black at night
## and nothing at dusk.
func _apply_woods(night: float) -> void:
    if not woods_light.enabled:
        return
    var tint: Color = world.canvas_modulate.color if world.canvas_modulate != null else Color.WHITE
    woods_light.color = Color(tint.r * WOODS_DARKEN, tint.g * WOODS_DARKEN, tint.b * WOODS_DARKEN)
    woods_light.energy = night

## Keep the night-vision light on the player's own character. The character
## node can be replaced (a re-render, a resync), so it is looked up each frame.
func _apply_vision(night: float) -> void:
    var pc: Node2D = world.placed_npcs.get(world.pc_actor_id, null)
    if pc == null or not is_instance_valid(pc):
        return
    if vision_light == null or not is_instance_valid(vision_light) or vision_light.get_parent() != pc:
        if vision_light != null and is_instance_valid(vision_light):
            vision_light.queue_free()
        vision_light = PointLight2D.new()
        vision_light.name = "NightVision"
        vision_light.texture = world._get_light_gradient()
        vision_light.texture_scale = VISION_RADIUS_PX / 128.0
        vision_light.color = VISION_COLOR
        pc.add_child(vision_light)
    vision_light.energy = VISION_ENERGY * night
    vision_light.enabled = night > 0.0
