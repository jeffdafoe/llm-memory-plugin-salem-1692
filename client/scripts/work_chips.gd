extends Node2D
## A few chips knocked off by a repairer's blow (LLM-743): they burst from where
## the axe lands, arc up and back toward the swinger, fall and fade, then the
## node frees itself.

const LIFE := 0.5
const GRAVITY := 420.0
const COUNT := 5

const BARK := [Color8(92, 64, 40), Color8(124, 88, 52)]
const LEAF := [Color8(86, 128, 52)]
const STONE := [Color8(140, 140, 150), Color8(108, 108, 120)]
const WOOD := [Color8(168, 124, 78), Color8(130, 92, 56), Color8(196, 160, 110)]

var _chips: Array = []  # {pos, vel, color, size}
var _age := 0.0


## The chip colours for the thing being mended, read off its name: bark and
## leaf for a fallen tree, stone and wood for a well, wood for the rest.
static func palette(name: String) -> Array:
    var n := name.to_lower()
    for word in ["tree", "maple", "oak", "elm", "pine", "log", "branch", "bough"]:
        if n.contains(word):
            return BARK + BARK + LEAF
    if n.contains("well"):
        return STONE + STONE + WOOD
    return WOOD


## Throw the chips: back the way the blade came (`back`, a unit vector toward
## the swinger) and up.
func burst(colors: Array, back: Vector2) -> void:
    if colors.is_empty():
        return
    for i in COUNT:
        var vel := Vector2(back.x * randf_range(20.0, 70.0) + randf_range(-30.0, 30.0),
            back.y * randf_range(10.0, 40.0) - randf_range(60.0, 130.0))
        _chips.append({
            "pos": Vector2(randf_range(-2.0, 2.0), randf_range(-2.0, 2.0)),
            "vel": vel,
            "color": colors[randi() % colors.size()],
            "size": 2.0 if randf() < 0.6 else 3.0,
        })


func _process(delta: float) -> void:
    _age += delta
    if _age >= LIFE:
        queue_free()
        return
    for chip in _chips:
        chip["vel"] += Vector2(0, GRAVITY * delta)
        chip["pos"] += chip["vel"] * delta
    queue_redraw()


func _draw() -> void:
    var alpha := clampf(1.0 - _age / LIFE, 0.0, 1.0)
    for chip in _chips:
        var color: Color = chip["color"]
        color.a = alpha
        var size: float = chip["size"]
        draw_rect(Rect2((chip["pos"] as Vector2).round(), Vector2(size, size)), color)
