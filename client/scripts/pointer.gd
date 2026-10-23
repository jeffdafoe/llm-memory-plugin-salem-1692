extends Sprite2D
## A pointer that draws the player's eye to something (LLM-855): the Tiny Swords
## cursor redrawn at half size, its tip at the art's top-left pixel. The tip goes
## on what it points at and the arrow hangs down and to the right of it. Once
## every PERIOD seconds it draws back POKE pixels along the diagonal, fading to
## ALPHA_MIN, and pokes in again.
##
## Make one with on() for the middle of the box of what it points at (a room
## spot's tap rect, a map object's rect, a Control's rect), or with at() for an
## exact point, in the coordinates of the node it is added to. art_scale draws it
## bigger; the poke stays whole pixels of the art.

const ART := "/tilesets/tiny-swords/ui/pointer.png"
const PERIOD := 1.0
const POKE := 1
const ALPHA_MIN := 0.55

## Where the tip touches, in the parent's coordinates.
var tip := Vector2.ZERO
var _time := 0.0


## A pointer whose tip touches point.
static func at(texture: Texture2D, point: Vector2, art_scale := 1.0) -> Sprite2D:
    var pointer = load("res://scripts/pointer.gd").new()
    pointer.texture = texture
    pointer.centered = false
    pointer.scale = Vector2(art_scale, art_scale)
    pointer.tip = point.floor()
    pointer.pose(0.0)
    return pointer


## A pointer whose tip touches the middle of box.
static func on(texture: Texture2D, box: Rect2, art_scale := 1.0) -> Sprite2D:
    return at(texture, box.get_center(), art_scale)


## The pointer t seconds into its pulse.
func pose(t: float) -> void:
    # 0 → 1 → 0 over one period: drawn back by whole pixels and fading, then in.
    var wave := 0.5 - 0.5 * cos(fmod(t, PERIOD) / PERIOD * TAU)
    position = tip + Vector2.ONE * roundf(wave * POKE) * scale.x
    modulate.a = lerpf(1.0, ALPHA_MIN, wave)


func _process(delta: float) -> void:
    _time += delta
    pose(_time)
