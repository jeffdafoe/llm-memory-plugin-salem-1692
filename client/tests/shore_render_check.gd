extends SceneTree

## Rendered look check for the water's banks and shore (LLM-967; not a CI
## suite: the headless renderer is a dummy and draws nothing). Runs windowed,
## by hand:
##   godot_console --path client --script res://tests/shore_render_check.gd
## Draws a pond (deep water ringed by shallows) in grass, with a dirt path and
## a cobbled path running into it, and a stream across the bottom, through the
## real terrain renderer at 2x zoom, once per ground (summer, autumn, winter,
## snowy). Writes OUT_DIR/shore-<ground>.png. Run the --import step first.

const OUT_DIR := "C:/tmp/llm/"
const VIEW := Vector2i(1280, 720)
const ZOOM := 2.0
const GROUNDS := [["summer", false], ["autumn", false], ["winter", false], ["winter", true]]

# terrain.gd ids
const DIRT := 1
const GRASS := 2
const DARK_GRASS := 3
const COBBLE := 4
const SHALLOW := 5
const DEEP := 6

const W := 24
const H := 14

var _renderer: Node2D = null
var _frame := 0
var _step := 0


func _initialize() -> void:
    get_root().size = VIEW
    var hard := Timer.new()
    hard.wait_time = 30.0
    hard.autostart = true
    hard.timeout.connect(func(): quit(2))
    get_root().add_child(hard)
    var camera := Camera2D.new()
    camera.zoom = Vector2(ZOOM, ZOOM)
    camera.position = Vector2(W * 16, H * 16)
    get_root().add_child(camera)
    _renderer = Node2D.new()
    _renderer.set_script(load("res://scripts/terrain_renderer.gd"))
    _renderer.set("map_data", _map())
    _renderer.set("map_width", W)
    _renderer.set("map_height", H)
    get_root().add_child(_renderer)
    _renderer.call("rebuild")
    _show(0)


func _map() -> Array:
    var rows: Array = []
    for y in H:
        var row: Array = []
        for x in W:
            var t := GRASS
            var dx := (x - 11.5) / 7.0
            var dy := (y - 5.0) / 3.6
            var r := dx * dx + dy * dy
            if r < 0.45:
                t = DEEP
            elif r < 1.0:
                t = SHALLOW
            elif x >= 16 and y <= 3:
                t = DARK_GRASS
            if y >= 11:
                t = SHALLOW
            if x == 3 and y < 11:
                t = DIRT
            if x == 19 and y >= 7 and y < 11:
                t = COBBLE
            row.append(t)
        rows.append(row)
    return rows


func _show(i: int) -> void:
    _renderer.call("set_season", GROUNDS[i][0], GROUNDS[i][1])
    _frame = 0


func _process(_delta: float) -> bool:
    _frame += 1
    if _frame < 4:
        return false
    var name: String = "snowy" if GROUNDS[_step][1] else GROUNDS[_step][0]
    var path := OUT_DIR + "shore-%s.png" % name
    get_root().get_texture().get_image().save_png(path)
    print("wrote ", path)
    _step += 1
    if _step >= GROUNDS.size():
        quit(0)
    else:
        _show(_step)
    return false
