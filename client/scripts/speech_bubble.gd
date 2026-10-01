## Speech bubble — a Node2D that draws a wrapped-text bubble with a tail
## above its parent's origin. Designed to be added as a child of an NPC
## or PC container Node so it auto-follows the sprite as it walks.
##
## Single-instance per parent: the bubble manager (world.gd's _on_npc_spoke
## handler) names this child SpeechBubble and removes any existing one
## before adding a new bubble — so a fresh speak from the same NPC
## replaces the old line immediately rather than queuing.
##
## Drawn with primitives (draw_texture_rect_region, draw_string) rather
## than nested Control nodes — Controls render in screen-space, which
## doesn't compose with the Node2D world-space sprite hierarchy.
##
## The bubble lives in the world but is drawn at a fixed SCREEN size: every
## frame its scale cancels the camera zoom (and the window stretch), so text
## lands on whole screen pixels at any zoom. Drawn at world scale, the zoom
## (0.3–3.0 in 0.1 steps, almost never whole) resampled the glyphs through
## the project's nearest filter and broke the letter strokes.
##
## Two looks. With the purchased Mana Seed art on disk: the pack's 9-slice
## chat bubble and its Body pixel font, one art pixel to ART_PIXEL_SCALE
## screen pixels (the village art's own size at zoom 1). Without it (the
## art is gitignored; CI and fresh checkouts have none): a drawn bubble in
## the fallback font, one local unit to one screen pixel.

class_name SpeechBubble
extends Node2D

## World px above the container origin where the tail tip points — just
## above the sprite's head. The bubble node sits here; everything else is
## laid out from it in screen-sized units.
const ANCHOR_Y := -88.0

const ART_DIR := "res://assets/tilesets/mana-seed/fonts/"
const ART_SHEET := ART_DIR + "chat bubble, variable 16x16.png"
const ART_FONT := ART_DIR + "ManaSeedBody.ttf"
const ART_FONT_SIZE := 8          # Body's native pixel height
const ART_PIXEL_SCALE := 2        # screen px per art px at the 1280x720 base size
const ART_MAX_TEXT_WIDTH := 120.0 # art px; the bubble wraps past this
const ART_LINE_GAP := 1.0         # art px between lines
# The sheet is 64x48. Column 40 and row 24 are uniform across the whole
# bubble, so stretching that one column / row grows the bubble without
# smearing the outline, the shading or the tail.
const ART_SLICE_COL := 40
const ART_SLICE_ROW := 24
const ART_TAIL_TIP := Vector2(12, 38)  # the tail's point, bottom-left
const ART_TEXT_ORIGIN := Vector2(15, 14)
const ART_TEXT_ROOM := Vector2(34, 19) # text fits this at the sheet's own size

const PADDING_X := 8.0
const PADDING_Y := 5.0
const MAX_TEXT_WIDTH := 220.0  # bubble wraps text past this many pixels
const TAIL_HALF_W := 6.0
const TAIL_HEIGHT := 8.0
const FONT_SIZE := 14

const BG_COLOR := Color(0.97, 0.94, 0.86, 0.97)
const BORDER_COLOR := Color(0.22, 0.16, 0.10, 1.0)
const TEXT_COLOR := Color(0.10, 0.08, 0.05, 1.0)

# Lifetime is computed by setup() based on text length unless caller overrides.
const MIN_LIFETIME := 5.0
const MAX_LIFETIME := 10.0
const LIFETIME_PER_CHAR := 1.0 / 18.0  # ~18 chars per second reading rate
const LIFETIME_PICKUP := 1.5            # extra buffer so bubbles don't vanish before noticed

# Shared by every bubble: loaded once, null when the art is absent.
static var _art_sheet: Texture2D = null
static var _art_font: Font = null
static var _art_checked := false

var _wrapped_lines: PackedStringArray
var _content_size: Vector2
var _font: Font
var _font_size: int = FONT_SIZE
var _use_art := false

## Initialize the bubble with text and start the lifetime timer. Call
## this immediately after add_child — the bubble queue_frees itself
## when the timer fires.
func setup(speak_text: String) -> void:
    _load_art()
    _use_art = _art_sheet != null and _art_font != null
    _font = _art_font if _use_art else ThemeDB.fallback_font
    _font_size = ART_FONT_SIZE if _use_art else FONT_SIZE
    _wrap_text(speak_text)
    _fit_to_screen()
    queue_redraw()

    var lifetime: float = clamp(
        speak_text.length() * LIFETIME_PER_CHAR + LIFETIME_PICKUP,
        MIN_LIFETIME,
        MAX_LIFETIME
    )
    var timer := Timer.new()
    timer.wait_time = lifetime
    timer.one_shot = true
    timer.timeout.connect(queue_free)
    add_child(timer)
    timer.start()


static func _load_art() -> void:
    if _art_checked:
        return
    _art_checked = true
    if ResourceLoader.exists(ART_SHEET):
        _art_sheet = load(ART_SHEET)
    if ResourceLoader.exists(ART_FONT):
        var font: FontFile = load(ART_FONT)
        # A pixel font is 1-bit art: any smoothing or sub-pixel placement
        # blurs it.
        font.antialiasing = TextServer.FONT_ANTIALIASING_NONE
        font.hinting = TextServer.HINTING_NONE
        font.subpixel_positioning = TextServer.SUBPIXEL_POSITIONING_DISABLED
        _art_font = font


## Greedy word-wrap. Stores wrapped lines in _wrapped_lines and the
## bounding box (widest line × line count) in _content_size for the
## draw pass.
func _wrap_text(text: String) -> void:
    var max_w: float = ART_MAX_TEXT_WIDTH if _use_art else MAX_TEXT_WIDTH - 2 * PADDING_X
    var words := text.split(" ", false)
    var lines: Array[String] = []
    var current := ""
    for w in words:
        var trial: String = w if current == "" else current + " " + w
        var trial_size: Vector2 = _font.get_string_size(
            trial, HORIZONTAL_ALIGNMENT_LEFT, -1, _font_size
        )
        if trial_size.x > max_w and current != "":
            lines.append(current)
            current = w
        else:
            current = trial
    if current != "":
        lines.append(current)
    _wrapped_lines = PackedStringArray(lines)

    var widest: float = 0.0
    for line in _wrapped_lines:
        var w_size: Vector2 = _font.get_string_size(
            line, HORIZONTAL_ALIGNMENT_LEFT, -1, _font_size
        )
        widest = max(widest, w_size.x)
    _content_size = Vector2(ceilf(widest), _line_height() * _wrapped_lines.size())


func _line_height() -> float:
    var h: float = _font.get_height(_font_size)
    return h + ART_LINE_GAP if _use_art else h


func _process(_delta: float) -> void:
    _fit_to_screen()


## Pin the bubble to the anchor above the head and size it for the screen,
## not the world. Two scales sit between this node and the screen: the
## canvas transform (camera zoom) and the viewport's stretch to the window
## (canvas_items stretch — get_final_transform, NOT part of the node's own
## screen transform). The plain bubble cancels the zoom only and keeps the
## UI's stretched size; the art cancels both, then multiplies back up by a
## WHOLE number of screen pixels per art pixel. The stretch differs slightly
## per axis (window 1920x1061 → 1.4747 x 1.4736), so each axis is its own.
func _fit_to_screen() -> void:
    position = Vector2(0, ANCHOR_Y)
    var parent := get_parent() as CanvasItem
    if parent == null or not parent.is_inside_tree():
        return
    var canvas_scale: Vector2 = parent.get_global_transform_with_canvas().get_scale()
    if canvas_scale.x <= 0.0 or canvas_scale.y <= 0.0:
        return
    if not _use_art:
        scale = Vector2.ONE / canvas_scale
        return
    var stretch: Vector2 = get_viewport().get_final_transform().get_scale()
    if stretch.x <= 0.0 or stretch.y <= 0.0:
        stretch = Vector2.ONE
    var units := float(maxi(1, roundi(ART_PIXEL_SCALE * stretch.x)))
    scale = Vector2.ONE * units / (canvas_scale * stretch)


func _draw() -> void:
    if _wrapped_lines.is_empty():
        return
    if _use_art:
        _draw_art()
    else:
        _draw_plain()


## The Mana Seed 9-slice, in art pixels, laid out so the tail's point sits
## on this node's origin. Grows by stretching the one uniform column and row.
func _draw_art() -> void:
    var grow: Vector2 = _art_grow()
    var origin := -ART_TAIL_TIP - Vector2(0, grow.y)
    var sheet_size := Vector2(_art_sheet.get_size())
    var src_x := [0.0, float(ART_SLICE_COL), ART_SLICE_COL + 1.0, sheet_size.x]
    var src_y := [0.0, float(ART_SLICE_ROW), ART_SLICE_ROW + 1.0, sheet_size.y]
    var dst_x := [0.0, float(ART_SLICE_COL), ART_SLICE_COL + 1.0 + grow.x, sheet_size.x + grow.x]
    var dst_y := [0.0, float(ART_SLICE_ROW), ART_SLICE_ROW + 1.0 + grow.y, sheet_size.y + grow.y]
    for row in 3:
        for col in 3:
            var src := Rect2(src_x[col], src_y[row], src_x[col + 1] - src_x[col], src_y[row + 1] - src_y[row])
            var dst := Rect2(dst_x[col], dst_y[row], dst_x[col + 1] - dst_x[col], dst_y[row + 1] - dst_y[row])
            dst.position += origin
            draw_texture_rect_region(_art_sheet, dst, src)

    var line_height: float = _line_height()
    var ascent: float = _art_font.get_ascent(_font_size)
    var text_pos := origin + ART_TEXT_ORIGIN + Vector2(0, ascent)
    for line in _wrapped_lines:
        draw_string(
            _art_font, text_pos, line,
            HORIZONTAL_ALIGNMENT_LEFT, -1, _font_size, TEXT_COLOR
        )
        text_pos.y += line_height


## How far the 9-slice grows past the sheet's own size, in art px, so the
## wrapped text fits inside the fill.
func _art_grow() -> Vector2:
    return Vector2(
        maxf(0.0, _content_size.x - ART_TEXT_ROOM.x),
        maxf(0.0, _content_size.y - ART_TEXT_ROOM.y)
    )


## The drawn bubble for checkouts without the art, in screen pixels, with
## the tail apex on this node's origin.
func _draw_plain() -> void:
    var bubble_w: float = _content_size.x + 2 * PADDING_X
    var bubble_h: float = _content_size.y + 2 * PADDING_Y

    # Bubble centered horizontally on the anchor; bottom edge sits just
    # above where the tail starts.
    var bubble_top: float = -TAIL_HEIGHT - bubble_h
    var bubble_left: float = -bubble_w * 0.5
    var rect := Rect2(bubble_left, bubble_top, bubble_w, bubble_h)
    draw_rect(rect, BG_COLOR, true)
    draw_rect(rect, BORDER_COLOR, false, 1.0)

    # Tail (downward triangle, fill + stroke).
    var tail_top_y: float = bubble_top + bubble_h
    var tail_left := Vector2(-TAIL_HALF_W, tail_top_y)
    var tail_right := Vector2(TAIL_HALF_W, tail_top_y)
    var tail_apex := Vector2.ZERO
    draw_polygon(
        PackedVector2Array([tail_left, tail_apex, tail_right]),
        PackedColorArray([BG_COLOR])
    )
    draw_line(tail_left, tail_apex, BORDER_COLOR, 1.0)
    draw_line(tail_apex, tail_right, BORDER_COLOR, 1.0)
    # Cover the bubble's bottom border between the tail's top corners
    # so the rectangle's underline doesn't bisect the tail's interior.
    draw_line(
        Vector2(bubble_left, tail_top_y),
        tail_left,
        BORDER_COLOR,
        1.0
    )
    draw_line(
        tail_right,
        Vector2(bubble_left + bubble_w, tail_top_y),
        BORDER_COLOR,
        1.0
    )

    # Text. draw_string baselines text — offset by font ascent so the
    # first line sits nicely at the top of the padded area.
    var line_height: float = _line_height()
    var ascent: float = _font.get_ascent(_font_size)
    var text_x: float = bubble_left + PADDING_X
    var text_y: float = bubble_top + PADDING_Y + ascent
    for line in _wrapped_lines:
        draw_string(
            _font, Vector2(text_x, text_y), line,
            HORIZONTAL_ALIGNMENT_LEFT, -1, _font_size, TEXT_COLOR
        )
        text_y += line_height


## Z-index above sprites so the bubble doesn't get clipped by overlapping
## NPC bodies. Sprite z is sorted by y for depth-painting; bubbles need
## to win regardless.
func _ready() -> void:
    z_index = 100
    z_as_relative = false
    texture_filter = CanvasItem.TEXTURE_FILTER_NEAREST
