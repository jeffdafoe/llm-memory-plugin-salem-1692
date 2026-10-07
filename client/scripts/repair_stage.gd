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
## THE OBJECT. The panel hands over the object twice: as it stands (`broken`)
## and as it will be (`sound`). Each layer is {tex: Texture2D, pos: Vector2
## (art px, top-left, relative to the object's anchor)}. For village sprites,
## while the work goes on the sound layers rise from the foot of the object over
## the broken ones.
##
## CLOSE-UPS (LLM-696). Every site kind the town pays for has a close-up drawn
## at the stage's own resolution (client/assets/repair/, built by
## tools/repair-art/build.ps1): a whole picture, broken and mended, drawn at
## scale 1 filling the object area. A road has no mended picture — its trunk is
## CUT a section per round, the section falling away and a sawn round stacking
## on the verge, and the cleared road shows the crown dragged aside. The fence,
## well, shop and crate (LLM-713) mend a piece at a time: each round plays a
## beat that takes broken pieces away and flies mended ones in (STAGED). A site
## with no close-up (`site` "") falls back to the village sprites the panel
## collects, scaled up to fit — a road there is cut the same way.
##
## THE SIGNPOST (LLM-712) is drawn in layers, not swept: its arm swings on its
## bolt in the close-up itself (the plumb game), a plumb bob hanging from it
## over a marked stake, and between rounds it rests a little truer each time.
##
## THE GAMES draw from the same art: a plank, nails and a hammer; a bar with a
## gold zone, a peg and a windlass wheel; a log end-on with a saw across it; and
## for the signpost only the progress pips, the game being in the picture.

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

# Colours for what is drawn in code (sparks, pips, the header, the zone).
const C_WOOD_DARK := Color8(59, 36, 23)
const C_WOOD_LIGHT := Color8(160, 101, 47)
const C_WOOD_HI := Color8(211, 155, 74)
const C_IRON_LIGHT := Color8(182, 188, 196)
const C_GOLD := Color8(242, 210, 120)
const C_TEXT := Color8(242, 222, 170)
const C_SHADE := Color(0, 0, 0, 0.28)

# The gold inlay that marks the windlass zone and the saw's wanted side.
const C_ZONE := Color8(192, 160, 64)
const C_ZONE_HI := Color8(240, 224, 96)
const C_ZONE_LO := Color8(126, 94, 38)
const C_OUTLINE := Color8(42, 34, 34)

const ART_DIR := "res://assets/repair/"

# The road close-up's geometry, in picture px — tools/repair-art/build.ps1
# draws the trunk to these numbers. Each round cuts a section off the trunk's
# right end, stopping at the road's left edge.
const ROAD_TRUNK_R := 146
const ROAD_CUT_MIN := 56
const ROAD_LOG_Y := 34
const ROAD_FACE_HALF := 5
## Where the sawn rounds stack on the right verge (top-left of each round), a
## pile of four, three, two and one; rounds past ten are not drawn.
const ROAD_ROUNDS := [
    Vector2(118, 72), Vector2(131, 72), Vector2(144, 72), Vector2(157, 72),
    Vector2(124, 64), Vector2(137, 64), Vector2(150, 64),
    Vector2(131, 56), Vector2(144, 56),
    Vector2(137, 48),
]

# The signpost close-up's geometry, in picture px — tools/repair-art/build.ps1
# draws the layers to these numbers (SIGN_* there). `sign-arm` is a strip of
# SIGN_ARM_FRAMES frames, one per whole degree from SIGN_ARM_MIN_DEG, each with
# the hinge at SIGN_ARM_PIVOT; the hinge sits at SIGN_HINGE on the post.
## The staged close-ups (LLM-713) mend a piece at a time. Each has strips from
## tools/repair-art/build.ps1 — `<site>-states` (the picture after 0..N beats)
## and per beat `-between` (the pieces taken away gone, none put in yet), `-in`
## (what the beat puts in) and `-out` (what it takes away) — and here one entry
## per beat, as many as the generator's: `in`, the offset (picture px) the new
## pieces fly in from; `out`, where the old ones go as they fade; `nails`, the
## new pieces are nails (they pop in with sparks); `say`, the pop.
const STAGED := {
    "fence": [
        {"in": Vector2(0, 26), "out": Vector2(0, 4), "say": "Up she goes!"},
        {"nails": true, "say": "Nailed!"},
        {"out": Vector2(0, 24), "say": "Out with it!"},
        {"in": Vector2(0, -22), "say": "New rail!"},
        {"nails": true, "say": "Nailed fast!"},
    ],
    "well": [
        {"out": Vector2(-6, -20), "say": "Cleared!"},
        {"in": Vector2(0, 40), "out": Vector2(0, 6), "say": "New post!"},
        {"out": Vector2(0, -28), "say": "Heave!"},
        {"in": Vector2(0, -18), "say": "Beam up!"},
        {"out": Vector2(-8, -10), "say": "Coiled!"},
        {"out": Vector2(-30, 0), "say": "Roll it!"},
        {"in": Vector2(0, -20), "say": "Drum in!"},
        {"in": Vector2(14, 0), "say": "Crank on!"},
        {"in": Vector2(0, -16), "say": "Rope down!"},
        {"in": Vector2(0, 18), "out": Vector2(10, -8), "say": "Bucket up!"},
    ],
    "shop": [
        {"out": Vector2(-20, -14), "say": "Heave!"},
        {"out": Vector2(10, -16), "say": "Shingles!"},
        {"out": Vector2(24, -12), "say": "Heave!"},
        {"out": Vector2(-12, -16), "say": "Shingles!"},
        {"out": Vector2(-16, -14), "say": "More shingles!"},
        {"out": Vector2(-24, -10), "say": "Heave!"},
        {"out": Vector2(0, -26), "say": "Last board!"},
        {"out": Vector2(30, 0), "say": "Swept!"},
        {"say": "Patched!"},
        {"in": Vector2(0, -6), "out": Vector2(0, 4), "say": "Shutter hung!"},
        {"nails": true, "out": Vector2(0, 2), "say": "Strap fixed!"},
        {"in": Vector2(0, -8), "out": Vector2(0, 6), "say": "Sign up!"},
    ],
    "crate": [
        {"out": Vector2(0, -10), "say": "Nails out!"},
        {"out": Vector2(-26, -8), "say": "Tidied!"},
        {"in": Vector2(22, -10), "out": Vector2(4, 0), "say": "Lid on!"},
        {"nails": true, "say": "Nailed!"},
        {"nails": true, "say": "Nailed down!"},
    ],
}
## A beat's timing, seconds: the old pieces go over BEAT_OUT; the new ones fly
## in from BEAT_IN_FROM to the end with a little overshoot, landing (dust, a
## jolt, the pop) where the ease first reaches them home.
const BEAT_TIME := 0.6
const BEAT_OUT := 0.3
const BEAT_IN_FROM := 0.12
const BEAT_LAND_W := 0.37  # ease_out_back(w) first reaches 1 here
const NAIL_FROM := Vector2(0, -4)
const PIECE_FROM := Vector2(0, -16)

const SIGN_HINGE := Vector2(48, 12)
const SIGN_ARM_MIN_DEG := -5
const SIGN_ARM_FRAMES := 30
const SIGN_ARM_SIZE := Vector2(112, 68)
const SIGN_ARM_PIVOT := Vector2(4, 10)
## The cord hangs from SIGN_CORD_AT on the arm (along it, across it, from the
## hinge), SIGN_CORD_LEN long; a bob that would hang lower lies in the grass
## with its top at SIGN_BOB_REST_Y. The stake's gold notch is where the bob
## hangs when the arm is level.
const SIGN_CORD_AT := Vector2(90, 16)
const SIGN_CORD_LEN := 24
const SIGN_BOB_REST_Y := 73
const SIGN_STAKE := Vector2(140, 46)
const C_CORD := Color8(225, 205, 165)

# The hammer's swing: `hammer` is a strip of HAMMER_FRAME_W x HAMMER_FRAME_H
# frames turned about the grip — level (the strike), then raised 20°, 40°,
# 60° — with the grip at the same place in each (tools/repair-art/build.ps1).
# At rest it hangs raised; a swing comes down in SWING_DOWN, lands, holds on
# the nail for SWING_HOLD and lifts back over SWING_UP.
const HAMMER_FRAME_W := 28
const HAMMER_FRAME_H := 32
const HAMMER_REST := 3
const SWING_DOWN := 0.06
const SWING_HOLD := 0.06
const SWING_UP := 0.21

static var _art_cache := {}

var title := ""
var game_kind := "hammer"
var game = null  # a repair_games.gd Game, or null before play
var broken: Array = []
var sound: Array = []
var steps := 1
var steps_done := 0
var playing := false
## The engine's game difficulty for this repair, 0..1 (LLM-712).
var difficulty := 0.0
## The close-up drawn ("fence", "well", "shop", "crate", "sign", "road"), or
## "" for the village sprites.
var site := ""

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
## The hammer's swing: seconds since it started (-1 at rest), whether it is
## coming down on a nail, and which slot it swings at.
var _swing_t := -1.0
var _swing_hit := false
var _swing_perfect := false
var _swing_slot := 0
## How far the struck nail stood when it was hit — it stands until the hammer
## lands, though the game has already counted it driven.
var _swing_nail_h := 0.0
var _particles: Array = []  # {pos, vel, life, max_life, color, size}
var _chunks: Array = []  # road sections falling away: {tex, region, pos, vel, life}
var _pops: Array = []  # {text, pos, life}
var _finishing := false
var _finish_t := 0.0
## The staged close-up's beats (LLM-713): how many are drawn as done, those
## still to play in order, and how far into the first of them.
var _beats_shown := 0
var _beat_queue: Array[int] = []
var _beat_t := 0.0
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


## The close-up for a site: its form first (a minor work), then its kind. ""
## when there is none.
static func site_key(site_kind: String, form: String) -> String:
    match form:
        "fence":
            return "fence"
        "crate":
            return "crate"
        "signpost":
            return "sign"
    match site_kind:
        "well":
            return "well"
        "business":
            return "shop"
        "road":
            return "road"
    return ""


## A piece of the close-up art by name, or null when it is not there.
static func art(piece: String) -> Texture2D:
    if not _art_cache.has(piece):
        var path := ART_DIR + piece + ".png"
        _art_cache[piece] = load(path) if ResourceLoader.exists(path) else null
    return _art_cache[piece]


## The close-up's layers for setup(): the broken and the mended picture whole,
## or for a road the ground alone (the trunk is drawn by progress). Empty when
## the site has no close-up.
static func closeup_layers(key: String) -> Dictionary:
    if key == "road":
        var ground := art("road-ground")
        return {} if ground == null else {"broken": [{"tex": ground, "pos": Vector2.ZERO}], "sound": []}
    if key == "":
        return {}
    var b := art(key + "-broken")
    var m := art(key + "-mended")
    if b == null or m == null:
        return {}
    return {"broken": [{"tex": b, "pos": Vector2.ZERO}], "sound": [{"tex": m, "pos": Vector2.ZERO}]}


## Where the road's trunk ends after `done` of `total` rounds, picture px.
static func road_cut_x(done: int, total: int) -> int:
    var t := clampf(float(done) / maxi(1, total), 0.0, 1.0)
    return roundi(lerpf(ROAD_TRUNK_R, ROAD_CUT_MIN, t))


## `closeup` is the site_key() of art the layers came from ("" for village
## sprites).
func setup(title_text: String, kind: String, broken_layers: Array, sound_layers: Array, total_steps: int, done: int, closeup := "") -> void:
    title = title_text
    game_kind = kind
    site = closeup
    broken = broken_layers
    sound = sound_layers
    steps = maxi(1, total_steps)
    steps_done = clampi(done, 0, steps)
    _shown_progress = float(steps_done) / steps
    _beat_queue.clear()
    _beat_t = 0.0
    _beats_shown = beats_for(steps_done, steps, STAGED.get(closeup, []).size())
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
    game = Games.make(game_kind, _rng, difficulty)
    game.set_progress(steps_done, steps)
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
    if site != "":
        # A close-up is drawn at the stage's own resolution and fills the area.
        _obj_scale = 1
        obj_h = int(_bbox.size.y)
    _obj_rect = Rect2(8, HEADER_H, ART_W - 16, obj_h)
    _art_h = HEADER_H + obj_h + GAP + _play_h() + GAP
    var room := get_viewport().get_visible_rect().size * _stretch * Vector2(0.92, 0.6)
    k = pixel_scale_for(room, Vector2(ART_W, _art_h))
    custom_minimum_size = Vector2(ART_W * k / _stretch.x, _art_h * k / _stretch.y)
    queue_redraw()


func set_progress(done: int) -> void:
    var before := steps_done
    steps_done = clampi(done, 0, steps)
    if steps_done > before and sound.is_empty():
        _drop_section(before)
    if game != null:
        game.set_progress(steps_done, steps)
    _queue_beats()


func release_round() -> void:
    if game != null:
        game.release()


## Show the object whole, flash, pop the pay; finish_shown fires when the
## beat is over so the panel can fly the coins.
func finish(paid: int) -> void:
    playing = false
    steps_done = steps
    _finishing = true
    _queue_beats()
    _finish_t = 0.0
    _flash = 1.0
    _burst(_obj_center(), 18, [C_GOLD, C_WOOD_HI, Color.WHITE])
    if paid > 0:
        pop("+%d" % paid, _obj_center() + Vector2(0, -12))
        _sound("coins")


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


## The play area's height: the plumb game is played in the close-up, so its
## strip holds only the progress pips.
func _play_h() -> int:
    return 10 if game_kind == "plumb" else Games.PLAY_H


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
            elif game_kind == "plumb":
                perfect = absf(game.hit_off) <= game.tolerance * 0.35
            _on_hit(perfect)
            round_won.emit(perfect)
        Games.Result.PROGRESS:
            _on_stroke()
            stroke.emit()
        Games.Result.MISS:
            if game_kind == "hammer":
                _start_swing(false, false)
            else:
                _shake = maxf(_shake, 0.12)
            missed.emit()


func _on_hit(perfect: bool) -> void:
    if game_kind == "hammer":
        # The strike lands when the swing comes down (_land_swing).
        _start_swing(true, perfect)
        return
    _shake = 0.18
    var at := _play_origin()
    match game_kind:
        "windlass":
            var wl = game
            at += Vector2(wl.BAR_X + wl.marker, wl.BAR_Y)
            _burst(at, 6, [C_IRON_LIGHT, C_WOOD_HI])
            _sound("windlass_click")
            _sound("windlass_turn")
        "plumb":
            # The arm is nailed home at its bolt.
            at = _anchor() + SIGN_HINGE + Vector2(2, 8)
            _burst(at, 7, [C_GOLD, Color.WHITE, C_IRON_LIGHT])
            _sound("hammer_hit")
        _:
            at += Vector2(Games.PLAY_W / 2.0, 20)
            _burst(at, 10, [C_WOOD_HI, C_WOOD_LIGHT])
            # The saw's last stroke of a round — the earlier ones sound in
            # _on_stroke.
            _sound("saw_stroke")
    if perfect:
        _flash = 0.6
        pop("Perfect!", at + Vector2(0, -10))


## Swing the hammer down onto the nail standing (a hit) or the board (a
## miss). A miss while a swing is under way leaves it be; a hit restarts it.
func _start_swing(hit: bool, perfect: bool) -> void:
    if _swing_t >= 0.0 and not hit:
        return
    var g = game
    _swing_t = 0.0
    _swing_hit = hit
    _swing_perfect = perfect
    _swing_slot = maxi(g.up, 0)
    _swing_nail_h = _nail_height(g) if hit else 0.0


## Where the swing is: the hammer frame to draw (0 = level, the strike;
## HAMMER_REST = raised at rest).
func _hammer_frame() -> int:
    if _swing_t < 0.0:
        return HAMMER_REST
    if _swing_t < SWING_DOWN:
        return maxi(1, HAMMER_REST - 1 - int(_swing_t / SWING_DOWN * (HAMMER_REST - 1)))
    if _swing_t < SWING_DOWN + SWING_HOLD:
        return 0
    var u := (_swing_t - SWING_DOWN - SWING_HOLD) / SWING_UP
    return mini(HAMMER_REST, 1 + int(u * HAMMER_REST))


## The hammer comes down: sparks and a jolt on a nail, a thud on the board.
func _land_swing() -> void:
    if game == null:
        return
    var at := _play_origin() + Vector2(game.slot_x(_swing_slot), game.BOARD_Y - 2)
    if _swing_hit:
        _sound("hammer_hit")
        _shake = 0.18
        _burst(at, 7, [C_GOLD, Color.WHITE, C_IRON_LIGHT])
        _burst(at + Vector2(0, 3), 4, [C_WOOD_LIGHT, C_WOOD_HI])
        if _swing_perfect:
            _flash = 0.6
            pop("Perfect!", at + Vector2(0, -10))
    else:
        _sound("hammer_miss")
        _shake = maxf(_shake, 0.12)
        _burst(at + Vector2(0, 2), 3, [C_WOOD_LIGHT, C_WOOD_HI])


## How far the standing nail stands proud: it rises fast and sinks over the
## last half second.
static func _nail_height(g) -> float:
    var rise: float = clampf(g.up_t / 0.12, 0.0, 1.0)
    var sink: float = clampf((g.up_t - (g.up_time - 0.5)) / 0.5, 0.0, 1.0)
    return roundf(8.0 * rise * (1.0 - sink))


func _on_stroke() -> void:
    var at := _play_origin() + Vector2(Games.PLAY_W / 2.0, 18)
    _burst(at, 3, [C_WOOD_HI, C_WOOD_LIGHT])
    _sound("saw_stroke")


## Play a sound through the Sound autoload (LLM-703). Looked up by node path,
## not by its global name: the repair tests preload this script before the
## tree — and so the autoload — exists, and the global name does not compile
## there.
func _sound(sound_name: String) -> void:
    if not is_inside_tree():
        return
    var s := get_node_or_null("/root/Sound")
    if s != null:
        s.play(sound_name)


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
    if not _beat_queue.is_empty():
        _advance_beat(delta)
    if _swing_t >= 0.0:
        var before := _swing_t
        _swing_t += delta
        if before < SWING_DOWN and _swing_t >= SWING_DOWN:
            _land_swing()
        if _swing_t >= SWING_DOWN + SWING_HOLD + SWING_UP:
            _swing_t = -1.0
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


## Where the object's anchor sits: centred, its foot on the area's floor. A
## close-up's picture fills the area from its top-left.
func _anchor() -> Vector2:
    if site != "":
        return _obj_rect.position
    var s := float(_obj_scale)
    var floor_y := _obj_rect.end.y - 4
    var x := _obj_rect.get_center().x - (_bbox.position.x + _bbox.size.x / 2.0) * s
    var y := floor_y - _bbox.end.y * s
    return Vector2(x, y).floor()


func _draw_object() -> void:
    var anchor := _anchor()
    var s := float(_obj_scale)
    if site == "":
        draw_rect(_obj_rect, Color(0, 0, 0, 0.18))
        # A soft ground shadow under the object.
        var shadow_w := _bbox.size.x * s * 0.8
        draw_rect(Rect2(_obj_rect.get_center().x - shadow_w / 2.0, _obj_rect.end.y - 5, shadow_w, 2), C_SHADE)
    var p := 1.0 if _finishing or steps_done >= steps and not playing else _shown_progress
    if site == "road":
        _draw_road(anchor, p >= 1.0)
        return
    if site == "sign" and art("sign-base") != null:
        _draw_sign(anchor, p >= 1.0)
        return
    if sound.is_empty():
        _draw_cut(anchor, s, p)
        return
    if _is_staged():
        _draw_staged(anchor)
        return
    # The village sprites' sweep, in art px: below the line the mended
    # sprites, above it the broken ones. It rises through the rows that change,
    # so outside them the two are the same picture and the seam never shows.
    var line_y := anchor.y + (_reveal.position.y + _reveal.size.y * (1.0 - p)) * s
    if p >= 1.0:
        line_y = -INF
    elif p <= 0.0:
        line_y = INF
    for l in broken:
        _draw_layer_clipped(l, anchor, s, -INF, line_y)
    for l in sound:
        _draw_layer_clipped(l, anchor, s, line_y, INF)


## Draw a layer, only the rows between y0 and y1 (art px), and only inside
## the object area.
func _draw_layer_clipped(l: Dictionary, anchor: Vector2, s: float, y0: float, y1: float) -> void:
    var tex: Texture2D = l.get("tex")
    if tex == null:
        return
    var size := tex.get_size()
    var dst := Rect2(anchor + l.get("pos", Vector2.ZERO) * s, size * s)
    # The rows between y0 and y1 inside the area. At either end of the work
    # the line is off the area (±INF) and one side has no rows at all — a
    # Rect2 with a negative height would log an error every frame.
    var top := maxf(y0, _obj_rect.position.y)
    var bottom := minf(y1, _obj_rect.end.y)
    if bottom <= top:
        return
    var clip := Rect2(_obj_rect.position.x, top, _obj_rect.size.x, bottom - top)
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


## The road close-up: the ground, the trunk cut back a section per round with
## a sawn face on its end, and the sawn rounds stacked on the verge. Cleared,
## the crown lies dragged aside.
func _draw_road(anchor: Vector2, cleared: bool) -> void:
    var ground := art("road-ground")
    if ground != null:
        draw_texture(ground, anchor)
    if cleared:
        _tex("road-brush", anchor)
    else:
        var trunk := art("road-trunk")
        if trunk != null:
            if steps_done == 0:
                draw_texture(trunk, anchor)
            else:
                var cut := road_cut_x(steps_done, steps)
                draw_texture_rect_region(trunk, Rect2(anchor, Vector2(cut, trunk.get_size().y)), Rect2(0, 0, cut, trunk.get_size().y))
                _tex("road-face", anchor + Vector2(cut - ROAD_FACE_HALF, ROAD_LOG_Y))
    var rounds := steps if cleared else steps_done
    for i in mini(rounds, ROAD_ROUNDS.size()):
        _tex("road-round", anchor + ROAD_ROUNDS[i])


## The signpost frame drawn for an arm `angle_deg` below level (negative above).
static func sign_arm_frame(angle_deg: float) -> int:
    return clampi(roundi(angle_deg) - SIGN_ARM_MIN_DEG, 0, SIGN_ARM_FRAMES - 1)


## Where the plumb cord hangs from on an arm turned `deg` below level, picture
## px — the same turn tools/repair-art/build.ps1 gives the arm's pixels.
static func sign_cord_top(deg: int) -> Vector2:
    var a := deg_to_rad(deg)
    var u := SIGN_CORD_AT.x
    var v := SIGN_CORD_AT.y
    return SIGN_HINGE + Vector2(u * cos(a) - v * sin(a), u * sin(a) + v * cos(a))


## How far below level the signpost's arm hangs: swinging while it is played,
## else at rest, its droop shrinking with the work done; level once mended.
func _sign_angle(mended: bool) -> float:
    if mended:
        return 0.0
    if game != null and game_kind == "plumb":
        return game.angle
    return Games.Plumb.SAG * (1.0 - float(steps_done) / steps)


## The signpost close-up: the base, the arm at its angle, the loose brace and
## nail until it is mended (the brace set under the arm after), and while it is
## played the stake and the plumb bob hanging from the arm.
func _draw_sign(anchor: Vector2, mended: bool) -> void:
    _tex("sign-base", anchor)
    var gear := playing and game != null and game_kind == "plumb"
    if gear:
        _tex("stake", anchor + SIGN_STAKE)
    var frame := sign_arm_frame(_sign_angle(mended))
    var arm := art("sign-arm")
    if arm != null:
        draw_texture_rect_region(arm, Rect2(anchor + SIGN_HINGE - SIGN_ARM_PIVOT, SIGN_ARM_SIZE),
            Rect2(frame * SIGN_ARM_SIZE.x, 0, SIGN_ARM_SIZE.x, SIGN_ARM_SIZE.y))
    _tex("sign-brace" if mended else "sign-loose", anchor)
    if not gear:
        return
    # The cord is drawn from the frame shown, not the exact angle, so the bob
    # always hangs from the arm's pixels. It shows gold while the arm is level.
    var top := (anchor + sign_cord_top(frame + SIGN_ARM_MIN_DEG)).floor()
    var bob_y := minf(top.y + SIGN_CORD_LEN, anchor.y + SIGN_BOB_REST_Y)
    draw_rect(Rect2(top.x, top.y, 1, bob_y - top.y), C_GOLD if game.is_level() else C_CORD)
    draw_rect(Rect2(top.x, top.y, 1, 1), C_IRON_LIGHT)
    _tex("bob", Vector2(top.x - 2, bob_y))


## Beats done after `done` of `total` steps, for a close-up of `n` beats:
## evenly spread, and all of them once the work is done.
static func beats_for(done: int, total: int, n: int) -> int:
    if done >= total:
        return n
    return clampi(floori(float(done) * n / maxi(1, total)), 0, n)


## 0 → 1 with an overshoot past 1 before it settles: a piece lands a touch
## past its place and bounces back.
static func ease_out_back(w: float) -> float:
    var c1 := 1.70158
    var c3 := c1 + 1.0
    var x := w - 1.0
    return 1.0 + c3 * x * x * x + c1 * x * x


## Where a strip frame's pixels are (frame-local), cached; empty when the frame
## is clear.
static var _used_cache := {}

static func strip_used_rect(piece: String, frame: int) -> Rect2i:
    var key := "%s#%d" % [piece, frame]
    if _used_cache.has(key):
        return _used_cache[key]
    var r := Rect2i()
    var tex := art(piece)
    if tex != null:
        var img := tex.get_image()
        if img != null:
            if img.is_compressed():
                img.decompress()
            var h := img.get_height()
            var w := closeup_width()
            if w > 0:
                r = img.get_region(Rect2i(frame * w, 0, w, h)).get_used_rect()
    _used_cache[key] = r
    return r


## A staged strip's frame width: every close-up is the object area's width.
static func closeup_width() -> int:
    return ART_W - 16


func _is_staged() -> bool:
    return STAGED.has(site) and art(site + "-states") != null


## Queue the beats the steps done have reached and not yet played.
func _queue_beats() -> void:
    if not STAGED.has(site):
        return
    var want := beats_for(steps_done, steps, STAGED[site].size())
    while _beats_shown < want:
        _beat_queue.append(_beats_shown)
        _beats_shown += 1


## The close-up at rest, or the beat under way: the old pieces lifting away
## and fading over the picture between, the new ones flying in.
func _draw_staged(anchor: Vector2) -> void:
    if _beat_queue.is_empty():
        _draw_frame(site + "-states", _beats_shown, anchor, 1.0)
        return
    var b: int = _beat_queue[0]
    var m: Dictionary = STAGED[site][b]
    _draw_frame(site + "-between", b, anchor, 1.0)
    var u := clampf(_beat_t / BEAT_OUT, 0.0, 1.0)
    if u < 1.0:
        var away: Vector2 = m.get("out", Vector2.ZERO)
        _draw_frame(site + "-out", b, anchor + away * u * u, 1.0 - u)
    var w := (_beat_t - BEAT_IN_FROM) / (BEAT_TIME - BEAT_IN_FROM)
    if w > 0.0:
        var from: Vector2 = m.get("in", NAIL_FROM if m.get("nails", false) else PIECE_FROM)
        var e := ease_out_back(minf(w, 1.0))
        _draw_frame(site + "-in", b, anchor + from * (1.0 - e), clampf(w * 5.0, 0.0, 1.0))


## One frame of a strip at `at` (whole art px), clipped to the object area.
func _draw_frame(piece: String, frame: int, at: Vector2, alpha: float) -> void:
    var tex := art(piece)
    if tex == null or alpha <= 0.0:
        return
    var size := Vector2(closeup_width(), tex.get_size().y)
    var dst := Rect2(at.round(), size)
    var vis := dst.intersection(_obj_rect)
    if vis.size.x <= 0 or vis.size.y <= 0:
        return
    var src := Rect2(vis.position - dst.position + Vector2(frame * size.x, 0), vis.size)
    draw_texture_rect_region(tex, vis, src, Color(1, 1, 1, alpha))


## When a beat lands: with new pieces, when they first reach their place;
## with only old ones going, halfway through their going.
func _beat_land_t(b: int) -> float:
    if strip_used_rect(site + "-in", b).has_area():
        return BEAT_IN_FROM + BEAT_LAND_W * (BEAT_TIME - BEAT_IN_FROM)
    return BEAT_OUT * 0.5


func _advance_beat(delta: float) -> void:
    var b: int = _beat_queue[0]
    var before := _beat_t
    if before == 0.0:
        var gone := strip_used_rect(site + "-out", b)
        if gone.has_area():
            _burst(_anchor() + Vector2(gone.get_center()), 5, [C_WOOD_HI, C_WOOD_LIGHT])
    _beat_t += delta
    var land := _beat_land_t(b)
    if before < land and _beat_t >= land:
        _beat_landed(b)
    if _beat_t >= BEAT_TIME:
        _beat_queue.pop_front()
        _beat_t = 0.0


## The beat lands: sparks for nails, dust and a thud for a piece set down, a
## puff where pieces were cleared away — and its pop.
func _beat_landed(b: int) -> void:
    var m: Dictionary = STAGED[site][b]
    var anchor := _anchor()
    var put := strip_used_rect(site + "-in", b)
    var gone := strip_used_rect(site + "-out", b)
    var r := put if put.has_area() else gone
    var at := anchor + Vector2(r.get_center())
    if put.has_area() and m.get("nails", false):
        _burst(at, 8, [C_GOLD, Color.WHITE, C_IRON_LIGHT])
        _shake = maxf(_shake, 0.1)
    elif put.has_area():
        _burst(anchor + Vector2(r.get_center().x, r.end.y - 1), 10, [C_WOOD_HI, C_WOOD_LIGHT, C_TEXT])
        _shake = maxf(_shake, 0.14)
        _sound("section_drop")
    else:
        _burst(at, 8, [C_WOOD_HI, C_TEXT])
    var say: String = m.get("say", "")
    if say != "":
        pop(say, anchor + Vector2(r.get_center().x, maxf(r.position.y - 4, 10.0)))


## The section a road round cut off falls away.
func _drop_section(from_step: int) -> void:
    var anchor := _anchor()
    var s := float(_obj_scale)
    if site == "road":
        var trunk := art("road-trunk")
        if trunk == null:
            return
        var x0 := road_cut_x(from_step + 1, steps)
        # The first cut takes the splintered end with it.
        var x1 := int(trunk.get_size().x) if from_step == 0 else road_cut_x(from_step, steps)
        if x1 <= x0:
            return
        _chunks.append({
            "tex": trunk, "region": Rect2(x0, ROAD_LOG_Y, x1 - x0, 18),
            "pos": anchor + Vector2(x0, ROAD_LOG_Y),
            "vel": Vector2(_rng.randf_range(10, 30), -30), "life": 0.8,
        })
        _burst(anchor + Vector2(x0, ROAD_LOG_Y + 8), 10, [C_WOOD_HI, C_WOOD_LIGHT])
        _sound("section_drop")
        return
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
    var play_h := _play_h()
    draw_rect(Rect2(o, Vector2(Games.PLAY_W, play_h)), Color(0, 0, 0, 0.22))
    # Progress pips: one per step, filled as the work lands.
    var pip_w := 6
    var total_w := steps * (pip_w + 2) - 2
    var px := o.x + (Games.PLAY_W - total_w) / 2.0
    for i in steps:
        var col := C_GOLD if i < steps_done else Color(C_WOOD_DARK, 0.8)
        draw_rect(Rect2(floorf(px + i * (pip_w + 2)), o.y + play_h - 5, pip_w, 3), col)
    if game == null:
        return
    match game_kind:
        "windlass":
            _draw_windlass(o)
        "saw":
            _draw_saw(o)
        "plumb":
            pass
        _:
            _draw_hammer(o)


## A texture at a whole art pixel, or nothing when the art is not there.
func _tex(piece: String, at: Vector2) -> void:
    var t := art(piece)
    if t != null:
        draw_texture(t, at.floor())


func _draw_windlass(o: Vector2) -> void:
    var g = game
    var bx: float = o.x + g.BAR_X
    var by: float = o.y + g.BAR_Y
    # The bar: a beam with iron caps; the zone is a gold inlay set into it.
    _tex("bar", Vector2(bx - 2, by - 2))
    var zx: float = bx + g.zone_x
    draw_rect(Rect2(zx, by + 1, g.zone_w, 5), C_ZONE)
    draw_rect(Rect2(zx, by + 1, g.zone_w, 1), C_ZONE_HI)
    draw_rect(Rect2(zx, by + 5, g.zone_w, 1), C_ZONE_LO)
    draw_rect(Rect2(zx - 1, by, 1, 7), C_OUTLINE)
    draw_rect(Rect2(zx + g.zone_w, by, 1, 7), C_OUTLINE)
    # The peg: an iron pin with a ring head, riding the bar.
    _tex("peg", Vector2(floorf(bx + g.marker) - 2, by - 11))
    # The windlass wheel above the bar's left end, a notch (an eighth of a
    # turn) per hit.
    var wheel := art("wheel")
    if wheel != null:
        var frame := posmod(int(g.notch), 8)
        draw_texture_rect_region(wheel, Rect2(Vector2(bx + 4 - 11, by - 16 - 11), Vector2(23, 23)), Rect2(frame * 23, 0, 23, 23))


func _draw_hammer(o: Vector2) -> void:
    var g = game
    var top: float = o.y + g.BOARD_Y
    _tex("plank", Vector2(o.x + g.BOARD_X - 4, top))
    var nail := art("nail")
    # The nail just hit still stands until the hammer comes down on it.
    var struck := _swing_hit and _swing_t >= 0.0 and _swing_t < SWING_DOWN
    for i in g.SLOTS:
        var x: float = o.x + g.slot_x(i)
        var standing := -1.0
        if struck and i == _swing_slot:
            standing = _swing_nail_h
        elif not g.driven[i] and i == g.up:
            standing = _nail_height(g)
        if standing >= 0.0 and nail != null:
            # Standing proud: the head over as much shaft as stands.
            draw_texture_rect_region(nail, Rect2(x - 3, top - standing - 3, 7, 3 + standing), Rect2(0, 0, 7, 3 + standing))
            draw_rect(Rect2(x - 2, top, 5, 1), C_SHADE)
        elif g.driven[i]:
            # Driven flush: just the head.
            _tex("nail-driven", Vector2(x - 3, top))
            draw_rect(Rect2(x - 3, top + 2, 7, 1), C_SHADE)
    # The hammer hangs raised over the nail standing (or the last struck) and
    # swings down about its grip. Its grip sits at the same place in every
    # frame, so one top-left serves them all: level, its face lands on the
    # board's top at the slot.
    var hammer := art("hammer")
    if hammer != null:
        var slot := _swing_slot if _swing_t >= 0.0 else maxi(g.up, 0)
        var at := Vector2(o.x + g.slot_x(slot) - 5, top - 30).floor()
        var frame := _hammer_frame()
        draw_texture_rect_region(hammer, Rect2(at, Vector2(HAMMER_FRAME_W, HAMMER_FRAME_H)),
            Rect2(frame * HAMMER_FRAME_W, 0, HAMMER_FRAME_W, HAMMER_FRAME_H))


func _draw_saw(o: Vector2) -> void:
    var g = game
    # Tap halves: the side wanted glows gold, its arrow pointing outward.
    var half := Games.PLAY_W / 2.0
    for side in 2:
        var r := Rect2(o + Vector2(side * half + 2, 4), Vector2(half - 4, 30))
        var want: bool = side == g.side
        draw_rect(r, Color(C_ZONE, 0.16) if want else Color(0, 0, 0, 0.12))
        var cy := floorf(r.get_center().y)
        var col := C_ZONE_HI if want else Color(C_TEXT, 0.3)
        var edge := C_ZONE_LO if want else Color(0, 0, 0, 0.24)
        for i in 7:
            var ax := r.position.x + (8.0 + i if side == 0 else r.size.x - 12.0 - i)
            draw_rect(Rect2(ax, cy - i, 2, i * 2 + 1), col)
            draw_rect(Rect2(ax, cy - i, 1, 1), edge)
            draw_rect(Rect2(ax, cy + i, 1, 1), edge)
    # The log end-on, pointing at the player: the body recedes up and right,
    # the blade runs in the cut behind the sawn face and shows past the log's
    # sides, sinking a stroke at a time.
    var face := (o + Vector2(half, 28)).floor()
    _tex("log-back", face - Vector2(19, 27))
    var depth := floorf(float(g.strokes) / g.strokes_needed * 38.0)
    _tex("saw", Vector2(face.x - 34 + roundf(g.saw_x * 12.0), face.y - 27 + depth))
    _tex("log-face", face - Vector2(19, 19))
    # Sawdust where the blade comes out at both sides.
    var by := face.y - 19 + depth
    var w := floorf(sqrt(maxf(0.0, 361.0 - (by - face.y) * (by - face.y))))
    for d in [Vector2(1, 1), Vector2(2, 3), Vector2(1, 5), Vector2(3, 6)]:
        draw_rect(Rect2(face.x - w - d.x, by + d.y, 1, 1), C_WOOD_HI)
        draw_rect(Rect2(face.x + w + d.x, by + d.y - 1, 1, 1), C_WOOD_HI)
