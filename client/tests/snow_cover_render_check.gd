extends SceneTree

## Rendered look check for the LLM-850 snow (not a CI suite: the headless
## renderer is a dummy and draws nothing). Runs windowed, by hand, with the
## purchased art from the private tiles repo (TILES below):
##   godot --path client --script res://tests/snow_cover_render_check.gd
## Draws a scene once per snow tier (none / dusting / light / deep): the winter
## ground for the tier (clean, or the snowy sheet at light and deep) and a
## house, a pine, a fence and a crop through snow_cover.gdshader at that tier's
## depth (world.gd SNOW_DEPTH), at the village's 2x art scale. Then the deep
## scene again under a winter storm as storm_fx.gd draws it (tint + snowfall).
## Writes OUT_DIR/snow-tiers.png (the four tiers, 2x2) and
## OUT_DIR/snow-storm.png, then quits.

const TILES := "C:/dev/llm-memory-village-tiles/tilesets/"
const OUT_DIR := "C:/tmp/llm/"
const SnowCoverShader = preload("res://scripts/snow_cover.gdshader")
# storm_fx.gd names the Sound autoload, so it is loaded at run time (as
# storm_fx_test.gd does), not preloaded.

const VIEW := Vector2i(1280, 720)
const TIERS := ["none", "dusting", "light", "deep"]
# world.gd SNOW_DEPTH, restated: preloading world.gd here drags in its autoloads.
const SNOW_DEPTH := {"dusting": 1, "light": 2, "deep": 3}
const ART_SCALE := 2.0

var _frame := 0
var _step := 0
var _scene: Node2D = null
var _shots: Array[Image] = []
var _storm: CanvasLayer = null
var _art := {}

func _initialize() -> void:
    get_root().size = VIEW
    var hard := Timer.new()
    hard.wait_time = 30.0
    hard.autostart = true
    hard.timeout.connect(func(): quit(2))
    get_root().add_child(hard)
    _art = {
        "clean": _tex("winter-forest/winter sheets/winter forest wang tiles (clean).png"),
        "snowy": _tex("winter-forest/winter sheets/winter forest wang tiles (snowy).png"),
        "house": _tex("tiny-swords/buildings/blue/House1.png"),
        "pine": _region(_tex("tiny-swords/resources/trees/tree-1.png"), Rect2(0, 0, 192, 256)),
        "fence": _tex("fences-walls/picket fence 16x16 v1.png"),
        "crop": _region(_tex("farming crops 1/farming crops 1-A 16x32.png"), Rect2(64, 0, 16, 32)),
    }
    _build(TIERS[0])

## Every tier gets three frames to draw before its shot; the storm gets more,
## so the snowfall has started moving.
func _process(_delta: float) -> bool:
    _frame += 1
    if _step < TIERS.size():
        if _frame % 3 == 0:
            _shots.append(get_root().get_texture().get_image())
            _step += 1
            if _step < TIERS.size():
                _build(TIERS[_step])
            else:
                _save_grid()
                _build("deep")
                _storm = CanvasLayer.new()
                _storm.set_script(load("res://scripts/storm_fx.gd"))
                get_root().add_child(_storm)
    elif _storm != null and _frame == TIERS.size() * 3 + 2:
        _storm.set("_snow_frames_light", _sheet_frames("snow light"))
        _storm.set("_snow_frames_heavy", _sheet_frames("snow heavy"))
        _storm.call("set_storm", true, false, true)
    elif _frame == TIERS.size() * 3 + 20:
        _save(get_root().get_texture().get_image(), "snow-storm.png")
        quit(0)
    return false

func _build(tier: String) -> void:
    if _scene != null:
        _scene.free()
    _scene = Node2D.new()
    get_root().add_child(_scene)
    # The wang fallback tile (1,2) is plain ground, what most of the village is.
    var sheet: Texture2D = _art["snowy"] if tier == "light" or tier == "deep" else _art["clean"]
    var ground := TextureRect.new()
    ground.texture = _region(sheet, Rect2(16, 32, 16, 16))
    ground.stretch_mode = TextureRect.STRETCH_TILE
    ground.texture_filter = CanvasItem.TEXTURE_FILTER_NEAREST
    ground.texture_repeat = CanvasItem.TEXTURE_REPEAT_ENABLED
    ground.size = Vector2(VIEW) / ART_SCALE
    ground.scale = Vector2(ART_SCALE, ART_SCALE)
    _scene.add_child(ground)

    var material: ShaderMaterial = null
    var depth: int = SNOW_DEPTH.get(tier, 0)
    if depth > 0:
        material = ShaderMaterial.new()
        material.shader = SnowCoverShader
        material.set_shader_parameter("depth", depth)
    _place(_art["house"], Vector2(60, 110), material)
    _place(_art["pine"], Vector2(420, 40), material, 1.5)
    _place(_art["fence"], Vector2(760, 300), material)
    _place(_art["fence"], Vector2(888, 300), material)
    _place(_art["crop"], Vector2(800, 500), material)
    _place(_art["crop"], Vector2(850, 500), material)
    _place(_art["crop"], Vector2(900, 500), material)

    var label := Label.new()
    label.text = tier
    label.position = Vector2(16, 8)
    label.add_theme_font_size_override("font_size", 40)
    label.add_theme_color_override("font_color", Color.BLACK)
    _scene.add_child(label)

func _place(texture: Texture2D, at: Vector2, material: ShaderMaterial, scale: float = ART_SCALE) -> void:
    var sprite := Sprite2D.new()
    sprite.texture = texture
    sprite.centered = false
    sprite.position = at
    sprite.scale = Vector2(scale, scale)
    sprite.texture_filter = CanvasItem.TEXTURE_FILTER_NEAREST
    sprite.material = material
    _scene.add_child(sprite)

func _save_grid() -> void:
    var grid := Image.create(VIEW.x * 2, VIEW.y * 2, false, _shots[0].get_format())
    for i in _shots.size():
        grid.blit_rect(_shots[i], Rect2i(Vector2i.ZERO, VIEW), Vector2i((i % 2) * VIEW.x, (i / 2) * VIEW.y))
    _save(grid, "snow-tiers.png")

func _save(img: Image, name: String) -> void:
    img.save_png(OUT_DIR + name)
    print("wrote " + OUT_DIR + name)

func _tex(rel: String) -> Texture2D:
    var img := Image.load_from_file(TILES + rel)
    if img == null:
        push_error("missing art: " + rel)
        quit(3)
    return ImageTexture.create_from_image(img)

func _region(sheet: Texture2D, rect: Rect2) -> AtlasTexture:
    var atlas := AtlasTexture.new()
    atlas.atlas = sheet
    atlas.region = rect
    return atlas

func _sheet_frames(kind: String) -> Array[Texture2D]:
    var sheet := _tex("weather effects 2.0/weather effects, %s anim 32x128.png" % kind)
    var frames: Array[Texture2D] = []
    for i in 8:
        frames.append(ImageTexture.create_from_image(sheet.get_image().get_region(Rect2i(i * 32, 0, 32, 128))))
    return frames
