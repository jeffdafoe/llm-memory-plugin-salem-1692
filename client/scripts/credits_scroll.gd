extends CanvasLayer

## Credits scroll (LLM-777) — opened by a click on the "Salem — 1692" title in
## the top bar. A parchment sheet between two turned wooden rods, which unrolls
## when it opens: the build this client was exported from, then who made the
## art, sound and fonts. Every name is a link to the maker's work.
##
## Layer 4 and the dismiss paths (Escape, a click on the dim backdrop — no X,
## LLM-802: a click anywhere outside the scroll closes it)
## follow notice_panel.gd; main.gd holds camera.modal_open while it is up.

const BuildInfo := preload("res://scripts/build_info.gd")

## Screen pixels per rod-art pixel. The rods are pixel art, drawn at an integer
## scale so they sit with the Mana Seed sprites rather than look like vectors.
const ROD_PIXEL := 2
## Rod art height, and how far each rod's knob runs past the paper, in art pixels.
const ROD_HEIGHT_PX := 16
const ROD_OVERHANG_PX := 12
const PAPER_WIDTH := 420.0
## Kept free between the scroll and the viewport's edges.
const VIEWPORT_MARGIN := 16.0
const SIDE_MARGIN := 16.0
## The shaded band where the paper rolls onto each rod.
const CURL_HEIGHT := 4.0
const PAPER_BORDER := 2
const PAD_X := 24
const PAD_Y := 10
## Vertical gaps in the text: before each section, and between two makers in one.
const SECTION_GAP := 4.0
const ENTRY_GAP := 2.0
const UNROLL_SEC := 0.45
## A press that moves further than this before it lifts is a drag, not a tap.
const LINK_TAP_SLOP := 10.0
## Drawn frames of one full turn of a rod (LLM-802). The rods turn with the
## paper: a frame per (one turn of paper / ROD_TURN_FRAMES) it moves.
const ROD_TURN_FRAMES := 16
## The parchment wound on each rod is drawn at ROLL_STEPS thicknesses: step 0
## is a single wrap over the core, each step after adds one art pixel of radius.
## The top roll fattens as the paper winds up onto it, the bottom one thins.
const ROLL_STEPS := 3
const COLOR_ROLL_EDGE := Color(0.55, 0.45, 0.29)
## The rods are not drawn at runtime (a picture costs ~15 ms): every step and
## turn of each rod is pre-rendered by tools/build_credit_rods.gd from rod_image
## into one sheet per rod, the widest rod the layout makes (PAPER_WIDTH plus the
## overhangs), one ROD_HEIGHT_PX row per picture, row = step * frames + frame.
## A narrower rod is cut from it: its left half from the sheet's left end and
## its right half from the right end, so both knobs survive.
const ROD_SHEET_WIDTH := 234
const ROD_SEEDS := [1692, 1693]
const ROD_SHEET_PATHS := ["res://assets/ui/credits_rod_top.png", "res://assets/ui/credits_rod_bottom.png"]
## The roll on open (LLM-802), only when the credits run taller than the paper:
## after the unroll and ROLL_LEAD, the paper winds up at ROLL_SPEED, rests
## ROLL_PAUSE at each end, and winds back. Any press, wheel, touch or key stops it.
const ROLL_SPEED := 20.0
const ROLL_LEAD := 1.0
const ROLL_PAUSE := 2.0

const COLOR_PAPER := Color(0.925, 0.863, 0.714)
const COLOR_PAPER_EDGE := Color(0.78, 0.67, 0.47)
const COLOR_PAPER_CURL := Color(0.84, 0.757, 0.576)
const COLOR_TITLE := Color(0.23, 0.165, 0.086)
const COLOR_HEADING := Color(0.48, 0.353, 0.196)
const COLOR_ORNAMENT := Color(0.66, 0.53, 0.35)
const COLOR_WHO := Color(0.23, 0.165, 0.086)
const COLOR_WHO_HOVER := Color(0.09, 0.055, 0.025)
const COLOR_WHAT := Color(0.415, 0.32, 0.21)
const COLOR_WHAT_HOVER := Color(0.22, 0.145, 0.07)
const COLOR_WOOD := Color(0.50, 0.33, 0.19)
const COLOR_WOOD_OUTLINE := Color(0.18, 0.11, 0.055)

## [heading, entries]. An entry is [maker, maker_url, work_lines]; a work line
## is a list of [work, work_url] shown on one row, joined by middle dots.
const CREDITS := [
    ["Artwork", [
        ["Seliel the Shaper", "https://seliel-the-shaper.itch.io/", [
            [["the Mana Seed collection", "https://seliel-the-shaper.itch.io/"]],
        ]],
        ["Pixel Frog", "https://pixelfrog-assets.itch.io/", [
            [["Tiny Swords", "https://pixelfrog-assets.itch.io/tiny-swords"]],
        ]],
        # The kit's quill icon is Open Pixel Project's, credited on the same line.
        ["The Bee's Knees", "https://the-bees-knees-games.itch.io/", [
            [["Pixel Parchment UI Kit", "https://the-bees-knees-games.itch.io/pixel-parchment-ui-kit"], ["quill: Open Pixel Project", "https://openpixelproject.com/"]],
        ]],
    ]],
    ["Sound", [
        ["Kenney", "https://kenney.nl/", [
            [["Impact Sounds", "https://kenney.nl/assets/impact-sounds"], ["RPG Audio", "https://kenney.nl/assets/rpg-audio"]],
        ]],
        # Each Freesound name opens the one sound of theirs the village plays
        # (see assets/sound/CREDITS.txt).
        ["Freesound", "https://freesound.org/", [
            [["speakwithanimals", "https://freesound.org/s/525046/"], ["loganzsound", "https://freesound.org/s/840628/"], ["manofham", "https://freesound.org/s/700379/"], ["Mikes-MultiMedia", "https://freesound.org/s/418509/"]],
            [["Emulius", "https://freesound.org/s/744722/"], ["SoundsLikeYukon", "https://freesound.org/s/760611/"], ["Flem0527", "https://freesound.org/s/629987/"], ["WavJunction.com", "https://freesound.org/s/456770/"]],
        ]],
    ]],
    ["Fonts", [
        ["Igino Marini", "https://fonts.google.com/specimen/IM+Fell+English", [
            [["IM Fell English", "https://fonts.google.com/specimen/IM+Fell+English"]],
        ]],
        ["Lucide", "https://lucide.dev/", [
            [["icons", "https://lucide.dev/"]],
        ]],
    ]],
    ["Built with", [
        ["Godot Engine", "https://godotengine.org/", []],
    ]],
    ["Inspired by", [
        ["Brunnfeld agentic village", "https://github.com/marcopatzelt/brunnfeld-agentic-world", [
            [["by Marco Patzelt", "https://github.com/marcopatzelt"]],
        ]],
    ]],
]

var root: Control = null
var backdrop: ColorRect = null
## Holds the paper and grows from 0 to the paper's height to unroll it.
var clipper: Control = null
var paper: PanelContainer = null
var body_scroll: ScrollContainer = null
var content: VBoxContainer = null
var top_rod: TextureRect = null
var bottom_rod: TextureRect = null

var _font: Font = null
var _italic: Font = null
var _heading_font: FontVariation = null
var _rod_width_px := 0
var _paper_height := 0.0
var _tween: Tween = null
## Each rod's pictures at the current width (0 top, 1 bottom), indexed
## step * ROD_TURN_FRAMES + frame: regions of one texture cut from its sheet.
var _rod_frames: Array = [[], []]
## Each rod's turn so far in radians, and the paper travel it was last turned
## to. The turn adds up step by step at the roll's current radius, so a change
## of thickness never jumps the picture.
var _top_angle := 0.0
var _bottom_angle := 0.0
var _top_moved := 0.0
var _bottom_moved := 0.0
## What each rod shows now: its roll step and turn frame.
var top_step := 0
var bottom_step := 0
var top_frame := 0
var bottom_frame := 0

enum Roll { OFF, LEAD, UP, REST_BOTTOM, DOWN, REST_TOP }
var _roll: Roll = Roll.OFF
## The roll's own clock (seconds of _process), its next phase change, and the
## paper's position as a float — scroll_vertical is whole pixels.
var _roll_clock := 0.0
var _roll_until := 0.0
var _roll_pos := 0.0
## How a link is opened; a test swaps it to catch the call without a browser.
var open_url: Callable = Callable(OS, "shell_open")

signal opened()
signal closed()


func _ready() -> void:
    layer = 4
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")
    _italic = load("res://assets/fonts/IMFellEnglish-Italic.ttf")
    _heading_font = FontVariation.new()
    _heading_font.base_font = _font
    _heading_font.spacing_glyph = 3
    _build_ui()
    visible = false
    set_process_unhandled_input(true)
    get_viewport().size_changed.connect(_on_viewport_size_changed)


func _build_ui() -> void:
    root = Control.new()
    root.set_anchors_preset(Control.PRESET_FULL_RECT)
    root.mouse_filter = Control.MOUSE_FILTER_STOP
    add_child(root)

    backdrop = ColorRect.new()
    backdrop.color = Color(0, 0, 0, 0.45)
    backdrop.set_anchors_preset(Control.PRESET_FULL_RECT)
    backdrop.mouse_filter = Control.MOUSE_FILTER_STOP
    backdrop.gui_input.connect(_on_backdrop_input)
    root.add_child(backdrop)

    var center := CenterContainer.new()
    center.set_anchors_preset(Control.PRESET_FULL_RECT)
    center.mouse_filter = Control.MOUSE_FILTER_IGNORE
    root.add_child(center)

    var column := VBoxContainer.new()
    column.add_theme_constant_override("separation", 0)
    column.mouse_filter = Control.MOUSE_FILTER_STOP
    center.add_child(column)

    top_rod = _make_rod_rect()
    column.add_child(top_rod)

    clipper = Control.new()
    clipper.clip_contents = true
    clipper.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
    clipper.mouse_filter = Control.MOUSE_FILTER_PASS
    column.add_child(clipper)

    bottom_rod = _make_rod_rect()
    column.add_child(bottom_rod)

    paper = PanelContainer.new()
    var paper_style := StyleBoxFlat.new()
    paper_style.bg_color = COLOR_PAPER
    paper_style.border_color = COLOR_PAPER_EDGE
    paper_style.border_width_left = PAPER_BORDER
    paper_style.border_width_right = PAPER_BORDER
    paper.add_theme_stylebox_override("panel", paper_style)
    paper.mouse_filter = Control.MOUSE_FILTER_PASS
    clipper.add_child(paper)

    var paper_column := VBoxContainer.new()
    paper_column.add_theme_constant_override("separation", 0)
    paper.add_child(paper_column)
    paper_column.add_child(_make_curl())

    var pad := MarginContainer.new()
    pad.add_theme_constant_override("margin_left", PAD_X)
    pad.add_theme_constant_override("margin_right", PAD_X)
    pad.add_theme_constant_override("margin_top", PAD_Y)
    pad.add_theme_constant_override("margin_bottom", PAD_Y)
    paper_column.add_child(pad)

    body_scroll = ScrollContainer.new()
    body_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    # No bar (LLM-802): the turning rods show the paper moves; wheel and drag
    # still scroll it.
    body_scroll.vertical_scroll_mode = ScrollContainer.SCROLL_MODE_SHOW_NEVER
    body_scroll.get_v_scroll_bar().value_changed.connect(func(_v: float): _turn_rods())
    pad.add_child(body_scroll)

    content = VBoxContainer.new()
    content.add_theme_constant_override("separation", 0)
    content.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    body_scroll.add_child(content)
    _build_contents()

    paper_column.add_child(_make_curl())


func _build_contents() -> void:
    content.add_child(_make_label("Salem — 1692", _font, 28, COLOR_TITLE))
    content.add_child(_make_label("~ build %s ~" % BuildInfo.commit(), _italic, 14, COLOR_HEADING))
    content.add_child(_make_ornament())
    for section in CREDITS:
        _add_space(SECTION_GAP)
        content.add_child(_make_label(str(section[0]).to_upper(), _heading_font, 12, COLOR_HEADING))
        _add_space(2)
        var first := true
        for entry in section[1]:
            if not first:
                _add_space(ENTRY_GAP)
            first = false
            content.add_child(_make_link(entry[0], entry[1], _font, 18, COLOR_WHO, COLOR_WHO_HOVER))
            for work_line in entry[2]:
                var row := HBoxContainer.new()
                row.alignment = BoxContainer.ALIGNMENT_CENTER
                row.add_theme_constant_override("separation", 0)
                for i in work_line.size():
                    if i > 0:
                        row.add_child(_make_label(" · ", _italic, 14, COLOR_WHAT))
                    row.add_child(_make_link(work_line[i][0], work_line[i][1], _italic, 14, COLOR_WHAT, COLOR_WHAT_HOVER))
                content.add_child(row)
    _add_space(SECTION_GAP)
    content.add_child(_make_ornament())


func _make_label(text: String, font: Font, font_size: int, color: Color) -> Label:
    var label := Label.new()
    label.text = text
    label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    label.add_theme_font_override("font", font)
    label.add_theme_font_size_override("font_size", OrientationGuard.text_size(font_size))
    label.add_theme_color_override("font_color", color)
    return label


## A label that opens url in a new tab. PASS, not STOP, so a wheel over a link
## still scrolls the paper. Hover darkens it.
func _make_link(text: String, url: String, font: Font, font_size: int, color: Color, hover_color: Color) -> Label:
    var label := _make_label(text, font, font_size, color)
    label.mouse_filter = Control.MOUSE_FILTER_PASS
    label.mouse_default_cursor_shape = Control.CURSOR_POINTING_HAND
    label.tooltip_text = url
    label.mouse_entered.connect(func(): label.add_theme_color_override("font_color", hover_color))
    label.mouse_exited.connect(func(): label.add_theme_color_override("font_color", color))
    label.gui_input.connect(_on_link_input.bind(url, {}))
    return label


## Opens on the release of a tap, not on the press: on a touch screen most of
## the paper is links, so the press must reach the ScrollContainer to start a
## drag. A press that ever moved past the slop (even if it came back), or that
## the paper scrolled under, is a drag.
func _on_link_input(event: InputEvent, url: String, press: Dictionary) -> void:
    if event is InputEventMouseMotion and press.has("at"):
        if event.global_position.distance_to(press["at"]) > LINK_TAP_SLOP:
            press["dragged"] = true
        return
    if not (event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT):
        return
    if event.pressed:
        press.clear()
        press["at"] = event.global_position
        press["scroll"] = body_scroll.scroll_vertical
        return
    if not press.has("at"):
        return
    var tapped: bool = not press.get("dragged", false) \
            and event.global_position.distance_to(press["at"]) <= LINK_TAP_SLOP \
            and body_scroll.scroll_vertical == press["scroll"]
    press.clear()
    if tapped:
        open_url.call(url)
        get_viewport().set_input_as_handled()


func _add_space(height: float) -> void:
    var spacer := Control.new()
    spacer.custom_minimum_size = Vector2(0, height)
    spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    content.add_child(spacer)


## A printer's rule — two short lines and a diamond — in place of a fleuron,
## which IM Fell English does not have.
func _make_ornament() -> Control:
    var ornament := Control.new()
    ornament.custom_minimum_size = Vector2(0, 14)
    ornament.mouse_filter = Control.MOUSE_FILTER_IGNORE
    ornament.draw.connect(func():
        var c := ornament.size / 2.0
        ornament.draw_line(Vector2(c.x - 44, c.y), Vector2(c.x - 9, c.y), COLOR_ORNAMENT, 1.0)
        ornament.draw_line(Vector2(c.x + 9, c.y), Vector2(c.x + 44, c.y), COLOR_ORNAMENT, 1.0)
        ornament.draw_colored_polygon(PackedVector2Array([
            Vector2(c.x, c.y - 4), Vector2(c.x + 5, c.y), Vector2(c.x, c.y + 4), Vector2(c.x - 5, c.y),
        ]), COLOR_ORNAMENT)
        ornament.draw_circle(Vector2(c.x - 50, c.y), 1.5, COLOR_ORNAMENT)
        ornament.draw_circle(Vector2(c.x + 50, c.y), 1.5, COLOR_ORNAMENT))
    return ornament


func _make_curl() -> ColorRect:
    var curl := ColorRect.new()
    curl.color = COLOR_PAPER_CURL
    curl.custom_minimum_size = Vector2(0, CURL_HEIGHT)
    curl.mouse_filter = Control.MOUSE_FILTER_IGNORE
    return curl


func _make_rod_rect() -> TextureRect:
    var rod := TextureRect.new()
    rod.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
    rod.stretch_mode = TextureRect.STRETCH_SCALE
    rod.texture_filter = CanvasItem.TEXTURE_FILTER_NEAREST
    rod.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
    rod.mouse_filter = Control.MOUSE_FILTER_PASS
    return rod


## Open and unroll, then roll the credits if they run taller than the paper.
## Idempotent while open.
func open() -> void:
    if visible:
        return
    _layout()
    body_scroll.scroll_vertical = 0
    visible = true
    opened.emit()
    if _tween != null:
        _tween.kill()
    clipper.custom_minimum_size.y = 0.0
    _tween = create_tween()
    _tween.tween_property(clipper, "custom_minimum_size:y", _paper_height, UNROLL_SEC) \
        .set_trans(Tween.TRANS_QUAD).set_ease(Tween.EASE_OUT)
    # The bottom rod turns as the paper comes off it.
    _tween.parallel().tween_method(func(_h: float): _turn_rods(), 0.0, _paper_height, UNROLL_SEC)
    _roll_clock = 0.0
    _roll_pos = 0.0
    _roll = Roll.LEAD
    _roll_until = UNROLL_SEC + ROLL_LEAD
    _top_angle = 0.0
    _bottom_angle = 0.0
    _top_moved = 0.0
    _bottom_moved = 0.0
    _turn_rods()


## main.gd also calls this on logout. Idempotent.
func close() -> void:
    if not visible:
        return
    if _tween != null:
        _tween.kill()
        _tween = null
    _roll = Roll.OFF
    visible = false
    closed.emit()


## How far the paper can wind: the text's height past the window, 0 when it fits.
func roll_room() -> float:
    var bar := body_scroll.get_v_scroll_bar()
    return maxf(0.0, bar.max_value - bar.page)


func is_rolling() -> bool:
    return _roll != Roll.OFF


## Step the roll (LLM-802). Phases run on the roll's own clock: the lead after
## the unroll, up, a rest at the bottom, down, a rest at the top, and round.
func _process(delta: float) -> void:
    if not visible:
        return
    if _roll != Roll.OFF:
        _step_roll(delta)
    # The rolls' thickness follows the room left to scroll, which settles a
    # frame or two after open; keep the rods current while the scroll is up.
    _turn_rods()


func _step_roll(delta: float) -> void:
    _roll_clock += delta
    var room := roll_room()
    match _roll:
        Roll.LEAD, Roll.REST_TOP:
            if _roll_clock >= _roll_until:
                # Text that fits has nothing to roll; check once the layout has
                # settled, not at open.
                _roll = Roll.UP if room > 0.0 else Roll.OFF
        Roll.UP:
            _roll_pos = minf(room, _roll_pos + ROLL_SPEED * delta)
            if _roll_pos >= room:
                _roll = Roll.REST_BOTTOM
                _roll_until = _roll_clock + ROLL_PAUSE
        Roll.REST_BOTTOM:
            if _roll_clock >= _roll_until:
                _roll = Roll.DOWN
        Roll.DOWN:
            _roll_pos = maxf(0.0, _roll_pos - ROLL_SPEED * delta)
            if _roll_pos <= 0.0:
                _roll = Roll.REST_TOP
                _roll_until = _roll_clock + ROLL_PAUSE
    body_scroll.scroll_vertical = int(roundf(_roll_pos))


## The player takes the paper: a press, a wheel turn or trackpad pan, a touch,
## a key, or a controller button stops the roll. Only stops it — the event goes
## on to scroll, open a link or close.
func _input(event: InputEvent) -> void:
    if not visible or _roll == Roll.OFF:
        return
    var takes: bool = (event is InputEventMouseButton and event.pressed) \
            or (event is InputEventScreenTouch and event.pressed) \
            or event is InputEventScreenDrag \
            or event is InputEventPanGesture \
            or (event is InputEventKey and event.pressed) \
            or (event is InputEventJoypadButton and event.pressed) \
            or (event is InputEventAction and event.pressed)
    if takes:
        _roll = Roll.OFF


## Dress both rods for where the paper is (LLM-802). Thickness: the paper
## scrolled up is wound on the top roll, the paper still below on the bottom
## one, and in the unroll the bottom roll still holds what has not come off it.
## Turn: paper winding up turns both rods so their faces move up; the bottom rod
## also turns as the paper comes off it in the unroll.
func _turn_rods() -> void:
    if _rod_frames[0].is_empty() or _rod_frames[1].is_empty():
        return
    var room := roll_room()
    var scrolled := float(body_scroll.scroll_vertical)
    var clip := clipper.custom_minimum_size.y
    var up := clampf(scrolled / room, 0.0, 1.0) if room > 0.0 else 0.0
    var still_below := 1.0 - up if room > 0.0 else 0.0
    if _paper_height > 0.0:
        still_below = maxf(still_below, 1.0 - clip / _paper_height)
    top_step = roll_step(up)
    bottom_step = roll_step(still_below)

    _top_angle += (scrolled - _top_moved) / (ROD_PIXEL * roll_radius(top_step))
    _top_moved = scrolled
    var bottom_moved := scrolled - clip
    _bottom_angle += (bottom_moved - _bottom_moved) / (ROD_PIXEL * roll_radius(bottom_step))
    _bottom_moved = bottom_moved
    top_frame = rod_frame_at(_top_angle)
    bottom_frame = rod_frame_at(_bottom_angle)

    top_rod.texture = rod_texture(0, top_step, top_frame)
    bottom_rod.texture = rod_texture(1, bottom_step, bottom_frame)


## The picture of one rod (0 top, 1 bottom) at a roll step and turn frame.
func rod_texture(which: int, step: int, frame: int) -> Texture2D:
    return _rod_frames[which][step * ROD_TURN_FRAMES + frame]


## Cut both rods at width_px (art pixels) out of their sheets: one composed
## image per rod — two native blits — and a region of it per picture.
func _build_rod_frames(width_px: int) -> void:
    for which in 2:
        _rod_frames[which] = []
        var sheet_texture: Texture2D = load(ROD_SHEET_PATHS[which])
        if sheet_texture == null:
            push_error("credits_scroll: rod sheet %s missing — run tools/build_credit_rods.gd" % ROD_SHEET_PATHS[which])
            continue
        var rod := cut_rod_sheet(sheet_texture.get_image(), width_px)
        var atlas := ImageTexture.create_from_image(rod)
        for row in ROLL_STEPS * ROD_TURN_FRAMES:
            var picture := AtlasTexture.new()
            picture.atlas = atlas
            picture.region = Rect2(0, row * ROD_HEIGHT_PX, width_px, ROD_HEIGHT_PX)
            picture.filter_clip = true
            _rod_frames[which].append(picture)


## A rod sheet narrowed to width_px: the left half from the sheet's left end,
## the right half from its right end, every row at once. A width at or over the
## sheet's is the sheet.
static func cut_rod_sheet(sheet: Image, width_px: int) -> Image:
    if sheet.get_format() != Image.FORMAT_RGBA8:
        sheet.convert(Image.FORMAT_RGBA8)
    var full := sheet.get_width()
    if width_px >= full:
        return sheet
    var tall := sheet.get_height()
    var out := Image.create(width_px, tall, false, Image.FORMAT_RGBA8)
    var left := (width_px + 1) / 2
    out.blit_rect(sheet, Rect2i(0, 0, left, tall), Vector2i.ZERO)
    out.blit_rect(sheet, Rect2i(full - (width_px - left), 0, width_px - left, tall), Vector2i(left, 0))
    return out


## The roll step for a share (0..1) of the paper wound on a rod.
static func roll_step(share: float) -> int:
    return clampi(roundi(share * (ROLL_STEPS - 1)), 0, ROLL_STEPS - 1)


## A roll's radius in art pixels at a step: the core wrap plus a pixel a step.
static func roll_radius(step: int) -> float:
    return rod_body_radius(ROD_HEIGHT_PX) + step


## The turn frame for a rod turned `angle` radians.
static func rod_frame_at(angle: float) -> int:
    return posmod(roundi(angle / TAU * ROD_TURN_FRAMES), ROD_TURN_FRAMES)


## The rod body's radius in art pixels (between its outline rows).
static func rod_body_radius(height_px: int) -> float:
    return (float(height_px - 4) - 3.0) / 2.0


## Size the paper to the viewport: full width up to PAPER_WIDTH, and the text
## scrolls inside it when the credits run taller than the room left over.
func _layout() -> void:
    var view := root.get_viewport_rect().size
    var overhang := float(ROD_OVERHANG_PX * ROD_PIXEL)
    var paper_width := minf(PAPER_WIDTH, view.x - 2.0 * SIDE_MARGIN - 2.0 * overhang)
    paper_width = floorf(paper_width / ROD_PIXEL) * ROD_PIXEL

    var rod_width_px := int(paper_width / ROD_PIXEL) + 2 * ROD_OVERHANG_PX
    if rod_width_px != _rod_width_px:
        _rod_width_px = rod_width_px
        var rod_size := Vector2(rod_width_px * ROD_PIXEL, ROD_HEIGHT_PX * ROD_PIXEL)
        _build_rod_frames(rod_width_px)
        top_rod.custom_minimum_size = rod_size
        bottom_rod.custom_minimum_size = rod_size
        _turn_rods()

    var chrome := 2.0 * ROD_HEIGHT_PX * ROD_PIXEL + 2.0 * CURL_HEIGHT + 2.0 * PAD_Y
    var room := maxf(80.0, view.y - 2.0 * VIEWPORT_MARGIN - chrome)
    var content_height := content.get_combined_minimum_size().y
    body_scroll.custom_minimum_size = Vector2(paper_width - 2.0 * (PAD_X + PAPER_BORDER), minf(content_height, room))

    _paper_height = paper.get_combined_minimum_size().y
    paper.position = Vector2.ZERO
    paper.size = Vector2(paper_width, _paper_height)
    clipper.custom_minimum_size = Vector2(paper_width, _paper_height)


func _on_viewport_size_changed() -> void:
    if not visible:
        return
    if _tween != null:
        _tween.kill()
        _tween = null
    _layout()


func _on_backdrop_input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
        close()
        get_viewport().set_input_as_handled()


func _unhandled_input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        close()
        get_viewport().set_input_as_handled()


## One scroll rod as pixel art, lit from the upper left: two round wooden knobs
## and collars at the ends, and between them the parchment wound on the rod —
## a roll of paper, not bare wood (LLM-802). seed_value varies the wood grain
## and the paper's fibres so the two rods are not copies.
##
## turn is how far the rod has turned about its long axis, in radians. The
## grain and fibres live on the surface, the light does not: each pixel's angle
## round the cylinder (from its normal) plus turn picks its texture, sampled on
## a circle so a whole turn comes back to the same picture. A turn moves the
## face of the rod up.
##
## step (0 .. ROLL_STEPS - 1) is how much parchment is wound on: each step adds
## an art pixel of radius to the roll, above and below.
static func rod_image(width_px: int, height_px: int, seed_value: int, turn := 0.0, step := 0) -> Image:
    var image := Image.create(width_px, height_px, false, Image.FORMAT_RGBA8)
    var noise := FastNoiseLite.new()
    noise.seed = seed_value
    noise.noise_type = FastNoiseLite.TYPE_SIMPLEX_SMOOTH
    noise.frequency = 0.035
    var light := Vector3(-0.35, -0.75, 0.56).normalized()

    var radius := height_px / 2.0
    var knob_width := int(ceil(2.0 * radius))
    var collar_width := 3
    var collar_top := 2
    var collar_bottom := height_px - 3
    var wound := clampi(step, 0, ROLL_STEPS - 1)
    var body_top := 3 - wound
    var body_bottom := height_px - 4 + wound
    var body_left := knob_width + collar_width
    var body_right := width_px - 1 - knob_width - collar_width
    var body_radius := rod_body_radius(height_px) + wound

    for y in height_px:
        for x in width_px:
            var px := Vector2(x + 0.5, y + 0.5)
            var color := Color(0, 0, 0, 0)
            var knob_center_x := radius if x * 2 < width_px else width_px - radius
            var d := (px - Vector2(knob_center_x, radius)) / radius
            if d.length_squared() <= 1.0:
                if d.length() > 1.0 - 1.2 / radius:
                    color = COLOR_WOOD_OUTLINE
                else:
                    var normal := Vector3(d.x, d.y, sqrt(maxf(0.0, 1.0 - d.length_squared())))
                    var grain := _grain(noise, x * 0.3, atan2(normal.y, normal.z) + turn, radius, seed_value)
                    color = _lit(COLOR_WOOD, normal, light) * grain
            elif (x < body_left or x > body_right) and x >= knob_width - 1 and x <= width_px - knob_width and y >= collar_top and y <= collar_bottom:
                var on_edge := y == collar_top or y == collar_bottom or x == knob_width - 1 or x == width_px - knob_width or x == body_left - 1 or x == body_right + 1
                if on_edge:
                    color = COLOR_WOOD_OUTLINE
                else:
                    color = _lit(COLOR_WOOD.darkened(0.12), _cylinder_normal(y, collar_top, collar_bottom), light)
            elif x >= body_left and x <= body_right and y >= body_top and y <= body_bottom:
                # The wound parchment.
                if y == body_top or y == body_bottom:
                    color = COLOR_ROLL_EDGE
                else:
                    var normal := _cylinder_normal(y, body_top, body_bottom)
                    var fibres := _fibres(noise, x, atan2(normal.y, normal.z) + turn, body_radius, seed_value)
                    # Paper takes a softer light than the wood: under the hard
                    # wood shading its lower half went as dark as the knobs.
                    var lambert := maxf(0.0, normal.dot(light))
                    var shade := (0.68 + 0.42 * lambert) * fibres
                    color = Color(COLOR_PAPER.r * shade, COLOR_PAPER.g * shade, COLOR_PAPER.b * shade, 1.0)
            color.a = 1.0 if color.a > 0.0 else 0.0
            image.set_pixel(x, y, color)
    return image


static func _cylinder_normal(y: int, top: int, bottom: int) -> Vector3:
    var t := (y + 0.5 - top) / float(bottom - top + 1)
    var ny := clampf(t * 2.0 - 1.0, -1.0, 1.0)
    return Vector3(0.0, ny, sqrt(maxf(0.0, 1.0 - ny * ny)))


static func _lit(base: Color, normal: Vector3, light: Vector3) -> Color:
    var lambert := maxf(0.0, normal.dot(light))
    var shade := 0.45 + 0.75 * lambert
    if lambert > 0.93:
        shade += 0.12
    return Color(base.r * shade, base.g * shade, base.b * shade, 1.0)


## A parchment multiplier at angle phi round a roll of radius r (art pixels):
## a soft mottle and sparse darker flecks, read on the circle like the grain so
## the turn frames loop. The flecks are what show the roll turning.
static func _fibres(noise: FastNoiseLite, x: float, phi: float, r: float, seed_value: int) -> float:
    var around := Vector2(cos(phi), sin(phi)) * r
    var tone := 1.0 + noise.get_noise_3d(x * 0.25, around.x * 0.6, around.y * 0.6) * 0.07
    if noise.get_noise_3d(x * 1.7 + seed_value, around.x * 2.2, around.y * 2.2) > 0.42:
        tone *= 0.88
    return tone


## A grain multiplier at angle phi round a cylinder of radius r (art pixels):
## dark bands every ~3 px of surface, warped along the rod by low-frequency
## noise, with a faint speckle. The noise is read on the circle of radius r and
## the band count is whole, so phi and phi + TAU give the same grain — the turn
## frames loop.
static func _grain(noise: FastNoiseLite, x: float, phi: float, r: float, seed_value: int) -> float:
    var around := Vector2(cos(phi), sin(phi)) * r
    var bands := maxf(1.0, roundf(TAU * r * 0.36))
    var phase := phi / TAU * bands + noise.get_noise_3d(x * 0.6, around.x * 0.9, around.y * 0.9) * 2.2
    var band := 0.78 if fposmod(phase, 1.0) < 0.28 else 1.0
    var speckle := 1.0 + noise.get_noise_3d(x * 7.0 + seed_value, around.x * 7.0, around.y * 7.0) * 0.05
    return band * speckle
