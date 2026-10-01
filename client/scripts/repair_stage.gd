extends Control

## The repair mini-game's pixel canvas (LLM-690) — the damaged object mending
## round by round, the game under it, and the juice. Owned by repair_panel.gd.
##
## PIXEL RULE. Everything here is drawn in ART PIXELS (one source pixel of
## Mana Seed art) at a WHOLE number of screen pixels each, `k`. The window
## stretch is fractional (canvas_items stretch), so _draw cancels it — the
## transform is k / stretch per axis — and snaps the origin to a whole screen
## pixel, the same reasoning as speech_bubble.gd. A pixel font is only crisp
## at its native size times a whole number: the Mana Seed Title font is
## loaded at a fixed 8 px with no antialiasing and no oversampling, and
## scaled by the same transform.
##
## THE OBJECT. The panel hands over the object's sprites twice: as it stands
## (`broken` — the damaged state, a business with its debris, a fence break
## with both sagging neighbours) and as it will be (`sound`). Each layer is
## {tex: Texture2D, pos: Vector2 (art px, top-left, relative to the object's
## anchor)}. While the work goes on, a reveal line rises from the foot of the
## object: below it the sound sprites, above it the broken ones. A road has no
## sound sprites — the tree is CUT instead, a section falling away per round.

const Games = preload("res://scripts/repair_games.gd")

signal round_won(perfect: bool)
signal stroke()
signal missed()
signal finish_shown()

const TITLE_FONT_PATH := "res://assets/tilesets/mana-seed/fonts/ManaSeedTitle.ttf"
const TITLE_PX := 8  # the Title font's native size

const ART_W := 192
const HEADER_H := 16
const OBJ_MAX_H := 112
const OBJ_MIN_H := 40
const GAP := 4
const PLAY_X := (ART_W - Games.PLAY_W) / 2
const MAX_PIXEL_SCALE := 5

# Mana Seed-like palette for the drawn pieces (tools, nails, bar, sparks).
const C_WOOD_DARK := Color8(59, 36, 23)
const C_WOOD := Color8(107, 63, 35)
const C_WOOD_LIGHT := Color8(160, 101, 47)
const C_WOOD_HI := Color8(211, 155, 74)
const C_IRON_DARK := Color8(46, 46, 54)
const C_IRON := Color8(106, 111, 122)
const C_IRON_LIGHT := Color8(182, 188, 196)
const C_GREEN := Color8(98, 160, 74)
const C_GREEN_LIGHT := Color8(150, 204, 102)
const C_GOLD := Color8(242, 210, 120)
const C_TEXT := Color8(242, 222, 170)
const C_SHADE := Color(0, 0, 0, 0.28)

## Tools, nails and the wheel draw at this many art px per map pixel, so they
## read beside an object drawn at 2–3x.
const TOOL_SCALE := 2

# Tool sprites, drawn from these maps: one char per map pixel.
# I/i/h = iron dark/mid/light, W/w = wood dark/mid, '.' = clear.
const HAMMER := [
    ".IIIIII.",
    "IiiiihhI",
    ".IIIIII.",
    "...Ww...",
    "...Ww...",
    "...Ww...",
    "...Ww...",
    "...WW...",
]
const SAW := [
    "WWW.............",
    "WwwIIIIIIIIIIIII",
    "WwwiiiiiiiihhhhI",
    "WWW.I.I.I.I.I.I.",
]

var title := ""
var game_kind := "hammer"
var game = null  # a repair_games.gd Game, or null before play
var broken: Array = []
var sound: Array = []
var steps := 1
var steps_done := 0
var playing := false

var k := 2  # screen px per art px
var _stretch := Vector2.ONE
var _title_font: Font = null
var _art_h := 0
var _obj_rect := Rect2()  # the object area, art px
var _obj_scale := 1
var _bbox := Rect2()  # union of the object's layers, art px around its anchor
var _reveal := Rect2()  # where broken and mended differ, art px around the anchor
var _shown_progress := 0.0  # 0..1, eased toward steps_done / steps
var _shake := 0.0
var _flash := 0.0
var _swing := 0.0  # 1 at a strike, decays — the hammer's drop
var _particles: Array = []  # {pos, vel, life, max_life, color, size}
var _chunks: Array = []  # road sections falling away: {tex, region, pos, vel, life}
var _pops: Array = []  # {text, pos, life}
var _finishing := false
var _finish_t := 0.0
var _rng := RandomNumberGenerator.new()


func _ready() -> void:
    texture_filter = CanvasItem.TEXTURE_FILTER_NEAREST
    mouse_filter = Control.MOUSE_FILTER_STOP
    _title_font = load_title_font()
    get_viewport().size_changed.connect(relayout)
    set_process(false)


## The Title font as a pixel font: native size, no smoothing, no
## oversampling. Falls back to the default font when the purchased art is
## absent (CI, fresh checkouts).
static func load_title_font() -> Font:
    if not ResourceLoader.exists(TITLE_FONT_PATH):
        return ThemeDB.fallback_font
    var loaded = load(TITLE_FONT_PATH)
    if not (loaded is FontFile):
        return ThemeDB.fallback_font
    var f: FontFile = (loaded as FontFile).duplicate()
    f.antialiasing = TextServer.FONT_ANTIALIASING_NONE
    f.hinting = TextServer.HINTING_NONE
    f.subpixel_positioning = TextServer.SUBPIXEL_POSITIONING_DISABLED
    f.oversampling = 1.0
    f.fixed_size = TITLE_PX
    f.fixed_size_scale_mode = TextServer.FIXED_SIZE_SCALE_INTEGER_ONLY
    return f


## The largest whole-number screen px per art px that fits the art in the
## room given (screen px), at least 1 and at most MAX_PIXEL_SCALE.
static func pixel_scale_for(room_px: Vector2, art: Vector2) -> int:
    if art.x <= 0 or art.y <= 0:
        return 1
    var fit := mini(floori(room_px.x / art.x), floori(room_px.y / art.y))
    return clampi(fit, 1, MAX_PIXEL_SCALE)


## The union of the layers' rects, art px around the anchor.
static func layers_bbox(layers: Array) -> Rect2:
    var box := Rect2()
    var first := true
    for l in layers:
        var tex: Texture2D = l.get("tex")
        if tex == null:
            continue
        var r := Rect2(l.get("pos", Vector2.ZERO), tex.get_size())
        box = r if first else box.merge(r)
        first = false
    return box


## Where the broken and the mended object differ — the rows the reveal sweeps,
## so every round visibly changes something even when the damage is a small
## part of a tall sprite (a signpost's arm, a well's windlass). The whole box
## when the images cannot be read or nothing differs.
static func diff_rect(broken_layers: Array, sound_layers: Array, box: Rect2) -> Rect2:
    if sound_layers.is_empty() or box.size.x < 1 or box.size.y < 1:
        return box
    var a := _compose(broken_layers, box)
    var b := _compose(sound_layers, box)
    if a == null or b == null:
        return box
    var w := a.get_width()
    var h := a.get_height()
    var x0 := w
    var y0 := h
    var x1 := -1
    var y1 := -1
    for y in h:
        for x in w:
            if not a.get_pixel(x, y).is_equal_approx(b.get_pixel(x, y)):
                x0 = mini(x0, x)
                y0 = mini(y0, y)
                x1 = maxi(x1, x)
                y1 = maxi(y1, y)
    if x1 < 0:
        return box
    return Rect2(box.position + Vector2(x0, y0), Vector2(x1 - x0 + 1, y1 - y0 + 1))


## The layers flattened into one image the size of box, or null when a layer's
## pixels cannot be read.
static func _compose(layers: Array, box: Rect2) -> Image:
    var out := Image.create(int(box.size.x), int(box.size.y), false, Image.FORMAT_RGBA8)
    for l in layers:
        var tex: Texture2D = l.get("tex")
        if tex == null:
            continue
        var img := tex.get_image()
        if img == null:
            return null
        if img.is_compressed():
            if img.decompress() != OK:
                return null
        img.convert(Image.FORMAT_RGBA8)
        var at := Vector2i((l.get("pos", Vector2.ZERO) - box.position).round())
        out.blend_rect(img, Rect2i(Vector2i.ZERO, img.get_size()), at)
    return out


## The whole-number scale the object draws at inside the object area: as big
## as fits, 1 to 3.
static func object_scale_for(bbox_size: Vector2, area: Vector2) -> int:
    if bbox_size.x <= 0 or bbox_size.y <= 0:
        return 1
    return clampi(mini(floori(area.x / bbox_size.x), floori(area.y / bbox_size.y)), 1, 3)


func setup(title_text: String, kind: String, broken_layers: Array, sound_layers: Array, total_steps: int, done: int) -> void:
    title = title_text
    game_kind = kind
    broken = broken_layers
    sound = sound_layers
    steps = maxi(1, total_steps)
    steps_done = clampi(done, 0, steps)
    _shown_progress = float(steps_done) / steps
    _particles.clear()
    _chunks.clear()
    _pops.clear()
    _finishing = false
    playing = false
    game = null
    _bbox = layers_bbox(broken)
    if _bbox.size == Vector2.ZERO:
        _bbox = layers_bbox(sound)
    else:
        _bbox = _bbox.merge(layers_bbox(sound)) if not sound.is_empty() else _bbox
    _reveal = diff_rect(broken, sound, _bbox)
    relayout()
    set_process(true)
    queue_redraw()


## Begin play: the game for this kind, from a fresh state.
func start_game() -> void:
    _rng.randomize()
    game = Games.make(game_kind, _rng)
    playing = true


func relayout() -> void:
    if not is_inside_tree():
        return
    _stretch = get_viewport().get_final_transform().get_scale()
    if _stretch.x <= 0.0 or _stretch.y <= 0.0:
        _stretch = Vector2.ONE
    var area := Vector2(ART_W - 16, OBJ_MAX_H)
    _obj_scale = object_scale_for(_bbox.size, area)
    var obj_h := clampi(int(_bbox.size.y) * _obj_scale + 8, OBJ_MIN_H, OBJ_MAX_H)
    _obj_rect = Rect2(8, HEADER_H, ART_W - 16, obj_h)
    _art_h = HEADER_H + obj_h + GAP + Games.PLAY_H + GAP
    var room := get_viewport().get_visible_rect().size * _stretch * Vector2(0.92, 0.6)
    k = pixel_scale_for(room, Vector2(ART_W, _art_h))
    custom_minimum_size = Vector2(ART_W * k / _stretch.x, _art_h * k / _stretch.y)
    queue_redraw()


func set_progress(done: int) -> void:
    var before := steps_done
    steps_done = clampi(done, 0, steps)
    if steps_done > before and sound.is_empty():
        _drop_section(before)


func release_round() -> void:
    if game != null:
        game.release()


## Show the object whole, flash, pop the pay; finish_shown fires when the
## beat is over so the panel can fly the coins.
func finish(paid: int) -> void:
    playing = false
    steps_done = steps
    _finishing = true
    _finish_t = 0.0
    _flash = 1.0
    _burst(_obj_center(), 18, [C_GOLD, C_WOOD_HI, Color.WHITE])
    if paid > 0:
        pop("+%d" % paid, _obj_center() + Vector2(0, -12))


func pop(text: String, at: Vector2) -> void:
    _pops.append({"text": text, "pos": at, "life": 1.1})


## A key press plays the round (keyboard and accessibility).
func press_key() -> void:
    _handle_press(null)


func _gui_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        accept_event()
        _handle_press(_to_play(event.position))


## A local (UI unit) point to play-area art px.
func _to_play(local: Vector2) -> Vector2:
    var art := local * _stretch / float(k)
    return art - _play_origin()


func _play_origin() -> Vector2:
    return Vector2(PLAY_X, _obj_rect.end.y + GAP)


func _handle_press(pos: Variant) -> void:
    if not playing or game == null:
        return
    var r: int = game.press(pos)
    match r:
        Games.Result.HIT:
            var perfect := false
            if game_kind == "windlass":
                var center: float = game.zone_x + game.zone_w / 2.0
                perfect = absf(game.marker - center) <= game.zone_w * 0.18
            _on_hit(perfect)
            round_won.emit(perfect)
        Games.Result.PROGRESS:
            _on_stroke()
            stroke.emit()
        Games.Result.MISS:
            _shake = maxf(_shake, 0.12)
            missed.emit()


func _on_hit(perfect: bool) -> void:
    _shake = 0.18
    _swing = 1.0
    var at := _play_origin()
    match game_kind:
        "hammer":
            var h = game
            at += Vector2(h.slot_x(h.up), h.BOARD_Y - 2)
            _burst(at, 7, [C_GOLD, Color.WHITE, C_IRON_LIGHT])
            _burst(at + Vector2(0, 3), 4, [C_WOOD_LIGHT, C_WOOD_HI])
        "windlass":
            var wl = game
            at += Vector2(wl.BAR_X + wl.marker, wl.BAR_Y)
            _burst(at, 6, [C_IRON_LIGHT, C_WOOD_HI])
        _:
            at += Vector2(Games.PLAY_W / 2.0, 20)
            _burst(at, 10, [C_WOOD_HI, C_WOOD_LIGHT])
    if perfect:
        _flash = 0.6
        pop("Perfect!", at + Vector2(0, -10))


func _on_stroke() -> void:
    var at := _play_origin() + Vector2(Games.PLAY_W / 2.0, 18)
    _burst(at, 3, [C_WOOD_HI, C_WOOD_LIGHT])


func _obj_center() -> Vector2:
    return _obj_rect.get_center()


func _burst(at: Vector2, n: int, colors: Array) -> void:
    for i in n:
        var ang := _rng.randf_range(-PI, 0.0)
        var spd := _rng.randf_range(20.0, 60.0)
        var life := _rng.randf_range(0.35, 0.7)
        _particles.append({
            "pos": at,
            "vel": Vector2(cos(ang), sin(ang)) * spd,
            "life": life,
            "max_life": life,
            "color": colors[_rng.randi_range(0, colors.size() - 1)],
            "size": 1 if _rng.randf() < 0.7 else 2,
        })


func _process(delta: float) -> void:
    if game != null and playing:
        game.update(delta)
    var target := float(steps_done) / steps
    _shown_progress = move_toward(_shown_progress, target, delta * 1.6)
    _shake = maxf(0.0, _shake - delta)
    _flash = maxf(0.0, _flash - delta * 2.5)
    _swing = maxf(0.0, _swing - delta * 6.0)
    for p in _particles:
        p["vel"] += Vector2(0, 140) * delta
        p["pos"] += p["vel"] * delta
        p["life"] -= delta
    _particles = _particles.filter(func(p): return p["life"] > 0.0)
    for c in _chunks:
        c["vel"] += Vector2(0, 180) * delta
        c["pos"] += c["vel"] * delta
        c["life"] -= delta
    _chunks = _chunks.filter(func(c): return c["life"] > 0.0)
    for p in _pops:
        p["pos"] += Vector2(0, -14) * delta
        p["life"] -= delta
    _pops = _pops.filter(func(p): return p["life"] > 0.0)
    if _finishing:
        _finish_t += delta
        if _finish_t >= 1.1:
            _finishing = false
            finish_shown.emit()
    queue_redraw()


# --- drawing ---------------------------------------------------------------

func _draw() -> void:
    if _art_h == 0:
        return
    var px := float(k)
    var to_ui := Vector2(px / _stretch.x, px / _stretch.y)
    # Snap the origin to a whole screen pixel so every art pixel is exactly k
    # screen pixels wide, never k±1.
    var screen_origin := get_global_transform_with_canvas().origin * _stretch
    var snap := (screen_origin.round() - screen_origin) / _stretch
    var shake := Vector2.ZERO
    if _shake > 0.0:
        shake = Vector2(_rng.randi_range(-1, 1), _rng.randi_range(-1, 1)) * (1.0 / px)
    draw_set_transform(snap + shake * to_ui, 0.0, to_ui)

    _draw_header()
    _draw_object()
    _draw_play()
    for c in _chunks:
        var a: float = clampf(c["life"] / 0.8, 0.0, 1.0)
        draw_texture_rect_region(c["tex"], Rect2(c["pos"], c["region"].size * _obj_scale), c["region"], Color(1, 1, 1, a))
    for p in _particles:
        var a: float = clampf(p["life"] / p["max_life"], 0.0, 1.0)
        var col: Color = p["color"]
        col.a = a
        draw_rect(Rect2(p["pos"].floor(), Vector2.ONE * p["size"]), col)
    for p in _pops:
        _draw_text_centered(p["text"], p["pos"].floor(), Color(C_GOLD, clampf(p["life"] / 0.5, 0.0, 1.0)))
    if _flash > 0.0:
        draw_rect(Rect2(0, 0, ART_W, _art_h), Color(1, 0.96, 0.85, _flash * 0.35))
    draw_set_transform(Vector2.ZERO, 0.0, Vector2.ONE)


func _draw_header() -> void:
    draw_rect(Rect2(0, 0, ART_W, HEADER_H - 2), Color(C_WOOD_DARK, 0.55))
    draw_rect(Rect2(0, HEADER_H - 2, ART_W, 1), Color(C_WOOD_LIGHT, 0.6))
    _draw_text_centered(title, Vector2(ART_W / 2.0, 11), C_TEXT)


func _draw_text_centered(text: String, at: Vector2, color: Color) -> void:
    if _title_font == null or text == "":
        return
    var w := _title_font.get_string_size(text, HORIZONTAL_ALIGNMENT_LEFT, -1, TITLE_PX).x
    var pos := Vector2(floorf(at.x - w / 2.0), at.y)
    draw_string(_title_font, pos + Vector2(1, 1), text, HORIZONTAL_ALIGNMENT_LEFT, -1, TITLE_PX, Color(0, 0, 0, color.a * 0.6))
    draw_string(_title_font, pos, text, HORIZONTAL_ALIGNMENT_LEFT, -1, TITLE_PX, color)


## Where the object's anchor sits: centred, its foot on the area's floor.
func _anchor() -> Vector2:
    var s := float(_obj_scale)
    var floor_y := _obj_rect.end.y - 4
    var x := _obj_rect.get_center().x - (_bbox.position.x + _bbox.size.x / 2.0) * s
    var y := floor_y - _bbox.end.y * s
    return Vector2(x, y).floor()


func _draw_object() -> void:
    draw_rect(_obj_rect, Color(0, 0, 0, 0.18))
    var anchor := _anchor()
    var s := float(_obj_scale)
    # A soft ground shadow under the object.
    var shadow_w := _bbox.size.x * s * 0.8
    draw_rect(Rect2(_obj_rect.get_center().x - shadow_w / 2.0, _obj_rect.end.y - 5, shadow_w, 2), C_SHADE)
    var p := 1.0 if _finishing or steps_done >= steps and not playing else _shown_progress
    if sound.is_empty():
        _draw_cut(anchor, s, p)
        return
    # The reveal line, in art px: below it the mended sprites, above it the
    # broken ones. It rises through the rows that change, so outside them the
    # two are the same picture and the seam never shows.
    var line_y := anchor.y + (_reveal.position.y + _reveal.size.y * (1.0 - p)) * s
    if p >= 1.0:
        line_y = -INF
    elif p <= 0.0:
        line_y = INF
    for l in broken:
        _draw_layer_clipped(l, anchor, s, -INF, line_y)
    for l in sound:
        _draw_layer_clipped(l, anchor, s, line_y, INF)
    if p > 0.0 and p < 1.0:
        var lx := anchor.x + _reveal.position.x * s
        draw_rect(Rect2(lx, floorf(line_y), _reveal.size.x * s, 1), Color(C_GOLD, 0.55))


## Draw a layer, only the rows between y0 and y1 (art px), and only inside
## the object area.
func _draw_layer_clipped(l: Dictionary, anchor: Vector2, s: float, y0: float, y1: float) -> void:
    var tex: Texture2D = l.get("tex")
    if tex == null:
        return
    var size := tex.get_size()
    var dst := Rect2(anchor + l.get("pos", Vector2.ZERO) * s, size * s)
    var clip := _obj_rect
    clip = clip.intersection(Rect2(clip.position.x, maxf(y0, clip.position.y), clip.size.x, minf(y1, clip.end.y) - maxf(y0, clip.position.y)))
    var vis := dst.intersection(clip)
    if vis.size.x <= 0 or vis.size.y <= 0:
        return
    var src := Rect2((vis.position - dst.position) / s, vis.size / s)
    draw_texture_rect_region(tex, vis, src)


## A road: the tree shortens from the right as sections are cut away.
func _draw_cut(anchor: Vector2, s: float, p: float) -> void:
    for l in broken:
        var tex: Texture2D = l.get("tex")
        if tex == null:
            continue
        var size := tex.get_size()
        var keep := Vector2(roundf(size.x * (1.0 - p)), size.y)
        if keep.x <= 0:
            continue
        var dst := Rect2(anchor + l.get("pos", Vector2.ZERO) * s, keep * s)
        var vis := dst.intersection(_obj_rect)
        if vis.size.x <= 0 or vis.size.y <= 0:
            continue
        draw_texture_rect_region(tex, vis, Rect2((vis.position - dst.position) / s, vis.size / s))


## The section a road round cut off falls away.
func _drop_section(from_step: int) -> void:
    var anchor := _anchor()
    var s := float(_obj_scale)
    for l in broken:
        var tex: Texture2D = l.get("tex")
        if tex == null:
            continue
        var size := tex.get_size()
        var x0 := roundf(size.x * (1.0 - float(from_step + 1) / steps))
        var x1 := roundf(size.x * (1.0 - float(from_step) / steps))
        if x1 <= x0:
            continue
        var region := Rect2(x0, 0, x1 - x0, size.y)
        _chunks.append({
            "tex": tex, "region": region,
            "pos": anchor + (l.get("pos", Vector2.ZERO) + Vector2(x0, 0)) * s,
            "vel": Vector2(_rng.randf_range(10, 30), -30), "life": 0.8,
        })
        _burst(anchor + (l.get("pos", Vector2.ZERO) + Vector2(x0, size.y / 2.0)) * s, 10, [C_WOOD_HI, C_WOOD_LIGHT])


func _draw_play() -> void:
    var o := _play_origin()
    draw_rect(Rect2(o, Vector2(Games.PLAY_W, Games.PLAY_H)), Color(0, 0, 0, 0.22))
    # Progress pips: one per step, filled as the work lands.
    var pip_w := 6
    var total_w := steps * (pip_w + 2) - 2
    var px := o.x + (Games.PLAY_W - total_w) / 2.0
    for i in steps:
        var col := C_GOLD if i < steps_done else Color(C_WOOD_DARK, 0.8)
        draw_rect(Rect2(floorf(px + i * (pip_w + 2)), o.y + Games.PLAY_H - 5, pip_w, 3), col)
    if game == null:
        return
    match game_kind:
        "windlass":
            _draw_windlass(o)
        "saw":
            _draw_saw(o)
        _:
            _draw_hammer(o)


func _draw_windlass(o: Vector2) -> void:
    var g = game
    var bar := Rect2(o + Vector2(g.BAR_X, g.BAR_Y), Vector2(g.BAR_W, 6))
    draw_rect(bar.grow(1), C_WOOD_DARK)
    draw_rect(bar, C_WOOD)
    draw_rect(Rect2(bar.position.x, bar.position.y, bar.size.x, 1), C_WOOD_LIGHT)
    var zone := Rect2(bar.position.x + g.zone_x, bar.position.y, g.zone_w, bar.size.y)
    draw_rect(zone, C_GREEN)
    draw_rect(Rect2(zone.position, Vector2(zone.size.x, 1)), C_GREEN_LIGHT)
    # The peg: an iron pin riding the bar.
    var mx := floorf(bar.position.x + g.marker)
    draw_rect(Rect2(mx - 1, bar.position.y - 4, 3, bar.size.y + 8), C_IRON_DARK)
    draw_rect(Rect2(mx, bar.position.y - 3, 1, bar.size.y + 6), C_IRON_LIGHT)
    # The windlass wheel above the bar's left end, a notch turned per hit.
    var c := (o + Vector2(g.BAR_X + 2, g.BAR_Y - 9)).floor()
    var ang: float = g.notch * PI / 4.0 + _swing * 0.6
    draw_arc(c, 8.0, 0.0, TAU, 24, C_WOOD_DARK, 2.0)
    draw_arc(c, 7.0, 0.0, TAU, 24, C_WOOD_LIGHT, 1.0)
    for i in 4:
        var a := ang + i * PI / 2.0
        var tip := c + Vector2(cos(a), sin(a)) * 7.0
        draw_line(c, tip.floor(), C_WOOD_HI, 2.0)
    draw_rect(Rect2(c - Vector2(2, 2), Vector2(4, 4)), C_IRON)


func _draw_hammer(o: Vector2) -> void:
    var g = game
    var board := Rect2(o + Vector2(g.BOARD_X, g.BOARD_Y), Vector2(g.BOARD_W, g.BOARD_H))
    draw_rect(board.grow(1), C_WOOD_DARK)
    draw_rect(board, C_WOOD)
    for y in [board.position.y + 4, board.position.y + 9]:
        draw_rect(Rect2(board.position.x, y, board.size.x, 1), C_WOOD_DARK)
    draw_rect(Rect2(board.position.x, board.position.y, board.size.x, 1), C_WOOD_LIGHT)
    for i in g.SLOTS:
        var x: float = o.x + g.slot_x(i)
        var top := board.position.y
        if g.driven[i]:
            # Driven flush: just the head.
            draw_rect(Rect2(x - 3, top, 7, 2), C_IRON_LIGHT)
            draw_rect(Rect2(x - 3, top + 1, 7, 1), C_IRON)
        elif i == g.up:
            # Standing proud — rises fast, sinks over the last half second.
            var rise: float = clampf(g.up_t / 0.12, 0.0, 1.0)
            var sink: float = clampf((g.up_t - (g.UP_TIME - 0.5)) / 0.5, 0.0, 1.0)
            var h := roundf(8.0 * rise * (1.0 - sink))
            draw_rect(Rect2(x - 1, top - h, 2, h), C_IRON)
            draw_rect(Rect2(x, top - h, 1, h), C_IRON_LIGHT)
            draw_rect(Rect2(x - 3, top - h - 2, 7, 2), C_IRON_LIGHT)
            draw_rect(Rect2(x - 3, top - h - 1, 7, 1), C_IRON_DARK)
    # The hammer hangs over the nail standing (or the last struck), dropping
    # on a strike.
    var hx: float = o.x + g.slot_x(maxi(g.up, 0)) - 8
    var hy := board.position.y - 30 + roundf(_swing * 12.0)
    _draw_map(HAMMER, Vector2(hx, hy))


func _draw_saw(o: Vector2) -> void:
    var g = game
    # Tap halves: the side wanted glows.
    var half := Games.PLAY_W / 2.0
    for side in 2:
        var r := Rect2(o + Vector2(side * half + 2, 4), Vector2(half - 4, 30))
        var want: bool = side == g.side
        draw_rect(r, Color(C_GREEN, 0.28) if want else Color(0, 0, 0, 0.12))
        var cy := floorf(r.get_center().y)
        var col := C_GREEN_LIGHT if want else Color(C_TEXT, 0.35)
        # A chevron pointing outward, 6 px tall: the side to draw toward.
        for i in 6:
            var dx := i if side == 0 else -i
            var ax := r.position.x + (8.0 if side == 0 else r.size.x - 10.0) + dx
            draw_rect(Rect2(ax, cy - i, 2, i * 2 + 1), col)
    # The log section under the saw, and the saw riding back and forth.
    var log := Rect2(o + Vector2(g.LOG_X + 16, 16), Vector2(g.LOG_W - 32, 14))
    draw_rect(log.grow(1), C_WOOD_DARK)
    draw_rect(log, C_WOOD)
    draw_rect(Rect2(log.position.x, log.position.y + 2, log.size.x, 1), C_WOOD_LIGHT)
    draw_rect(Rect2(log.position.x, log.end.y - 2, log.size.x, 1), C_WOOD_DARK)
    var cut := floorf(log.get_center().x)
    var depth := floorf(float(g.strokes) / g.STROKES * log.size.y)
    draw_rect(Rect2(cut - 1, log.position.y, 2, depth), C_WOOD_DARK)
    var sx := cut - 16 + roundf(g.saw_x * 12.0)
    _draw_map(SAW, Vector2(sx, log.position.y - 8 + depth))


## Draw a tool map, each map pixel `scale` art px square.
func _draw_map(rows: Array, at: Vector2, scale: int = TOOL_SCALE) -> void:
    for y in rows.size():
        var row: String = rows[y]
        for x in row.length():
            var col := _map_color(row[x])
            if col.a > 0.0:
                draw_rect(Rect2(at.floor() + Vector2(x, y) * scale, Vector2.ONE * scale), col)


func _map_color(ch: String) -> Color:
    match ch:
        "I":
            return C_IRON_DARK
        "i":
            return C_IRON
        "h":
            return C_IRON_LIGHT
        "W":
            return C_WOOD_DARK
        "w":
            return C_WOOD_LIGHT
    return Color(0, 0, 0, 0)
