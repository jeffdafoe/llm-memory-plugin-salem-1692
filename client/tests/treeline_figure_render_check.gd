extends SceneTree

## Rendered check of treeline_figure.gdshader (LLM-844). Not a CI suite: the
## headless renderer is a dummy and draws nothing, so this runs windowed, by
## hand, after a shader change:
##   godot --path client --script res://tests/treeline_figure_render_check.gd
## Exits 0 when every check passes.
##
## A bright red 16x16 square with 8 px of transparent padding, drawn through
## the shader with a mid-grey tint at scale 8 on white, sampled over 4 s:
##  - every drawn pixel is grey (the sheet's luminance on the tint), with no
##    red from the sheet;
##  - the sway draws some pixels outside the still square;
##  - modulate alpha 0 draws nothing (the fade-out).
## Both checks on colour and outline failed against the first shader, which
## multiplied by the fragment COLOR (texture colour at the unshifted UV).

const TreelineFigure = preload("res://scripts/treeline_figure.gd")
const SCALE := 8

var _sprite: Sprite2D
var _fails := 0

func _initialize() -> void:
    get_root().size = Vector2i(1280, 720)
    var hard := Timer.new()
    hard.wait_time = 20.0
    hard.autostart = true
    hard.timeout.connect(func(): quit(2))
    get_root().add_child(hard)
    var bg := ColorRect.new()
    bg.color = Color.WHITE
    bg.size = Vector2(1280, 720)
    get_root().add_child(bg)
    var img := Image.create(32, 32, false, Image.FORMAT_RGBA8)
    img.fill(Color(0, 0, 0, 0))
    img.fill_rect(Rect2i(8, 8, 16, 16), Color(1, 0, 0, 1))
    _sprite = Sprite2D.new()
    _sprite.texture = ImageTexture.create_from_image(img)
    _sprite.centered = false
    _sprite.scale = Vector2(SCALE, SCALE)
    _sprite.position = Vector2(100, 100)
    _sprite.texture_filter = CanvasItem.TEXTURE_FILTER_NEAREST
    var mat := ShaderMaterial.new()
    mat.shader = TreelineFigure.FIGURE_SHADER
    mat.set_shader_parameter("sway_px", 1.0)
    # Mid grey, so a red cast from the sheet would show.
    mat.set_shader_parameter("tint", Color(0.5, 0.5, 0.5))
    _sprite.material = mat
    get_root().add_child(_sprite)
    _run()

func _check(label: String, ok: bool) -> void:
    print(("  ok   " if ok else "  FAIL ") + label)
    if not ok:
        _fails += 1

func _run() -> void:
    var outside := 0
    var reddish := 0
    var drawn := 0
    for n in 40:
        await create_timer(0.1).timeout
        await RenderingServer.frame_post_draw
        var shot := get_root().get_texture().get_image()
        for y in range(100, 100 + 32 * SCALE, 4):
            for x in range(100, 100 + 32 * SCALE, 4):
                var c := shot.get_pixel(x, y)
                if c.r > 0.95 and c.g > 0.95 and c.b > 0.95:
                    continue
                drawn += 1
                if absf(c.r - c.g) > 0.05:
                    reddish += 1
                var ax := (x - 100) / SCALE
                var ay := (y - 100) / SCALE
                if ax < 8 or ax >= 24 or ay < 8 or ay >= 24:
                    outside += 1
    _check("something was drawn (%d samples)" % drawn, drawn > 0)
    _check("no drawn pixel keeps the source red (%d)" % reddish, reddish == 0)
    _check("the sway draws outside the still shape (%d samples)" % outside, outside > 0)
    _sprite.modulate.a = 0.0
    await create_timer(0.1).timeout
    await RenderingServer.frame_post_draw
    var shot := get_root().get_texture().get_image()
    var left := 0
    for y in range(100, 100 + 32 * SCALE, 4):
        for x in range(100, 100 + 32 * SCALE, 4):
            var c := shot.get_pixel(x, y)
            if not (c.r > 0.95 and c.g > 0.95 and c.b > 0.95):
                left += 1
    _check("modulate alpha 0 draws nothing (%d)" % left, left == 0)
    print("render check: %d failure(s)" % _fails)
    quit(1 if _fails > 0 else 0)
