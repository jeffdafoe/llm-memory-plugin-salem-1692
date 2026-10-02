## Speech bubble — a Node2D that draws a wrapped-text bubble with a tail
## above its parent's origin. Designed to be added as a child of an NPC
## or PC container Node so it auto-follows the sprite as it walks.
##
## Single-instance per parent: the bubble manager (world.gd's _on_npc_spoke
## handler) names this child SpeechBubble and removes any existing one
## before adding a new bubble — so a fresh speak from the same NPC
## replaces the old line immediately rather than queuing.
##
## Bubbles of DIFFERENT speakers standing close would overlap, so every live
## bubble is laid out together once a frame (_layout): newest first, each
## stays at its speaker's head unless a newer bubble is in the way, and then
## rises just clear of it — a stack reads top to bottom in speaking order. A
## lifted bubble draws a stem down to its speaker. A stack holds MAX_STACK
## bubbles; one that would sit higher closes early (the talk panel keeps
## every line).
##
## Drawn with primitives (draw_texture_rect_region, draw_string) rather
## than nested Control nodes — Controls render in screen-space, which
## doesn't compose with the Node2D world-space sprite hierarchy.
##
## The bubble lives in the world but is drawn at a fixed SCREEN size: every
## frame its scale cancels the camera zoom, so text is never resampled by
## it. Drawn at world scale, the zoom (0.3–3.0 in 0.1 steps, almost never
## whole) resampled the glyphs through the project's nearest filter and
## broke the letter strokes.
##
## Text is always the fallback font at UI size. The bubble around it has two
## looks. With the purchased Mana Seed art on disk: the pack's 9-slice chat
## bubble, one art pixel to ART_PIXEL_SCALE screen pixels at every window
## size, the text on a child layer at UI size inside it. Without it (the art
## is gitignored; CI and fresh checkouts have none): a drawn bubble.

class_name SpeechBubble
extends Node2D

## World px above the container origin where the tail tip points — just
## above the sprite's head. The bubble node sits here; everything else is
## laid out from it in screen-sized units.
const ANCHOR_Y := -88.0

const ART_SHEET := "res://assets/tilesets/mana-seed/fonts/chat bubble, variable 16x16.png"
const ART_PIXEL_SCALE := 2        # screen px per art px, at any zoom and window size
# The sheet is 64x48. Column 40 is uniform top to bottom, so repeating it
# widens the bubble without smearing the outline, the shading or the tail.
# Rows 16-32 are all identical, so the bubble is drawn as the top band
# (rows 0-15), any number of copies of one middle row (none at all for a
# one-line bubble) and the bottom band (rows 33-47, with the tail).
const ART_SLICE_COL := 40
const ART_TOP_ROWS := 16
const ART_MIDDLE_ROW := 24
const ART_BOTTOM_ROW := 33
const ART_TAIL_TIP := Vector2(12, 21)  # the tail's point with no middle rows
const ART_TEXT_ORIGIN := Vector2(15, 14)
const ART_TEXT_ROOM := Vector2(34, 3)  # text room in art px at the sheet's width, with no middle rows

const PADDING_X := 8.0
const PADDING_Y := 5.0
const MAX_TEXT_WIDTH := 340.0  # bubble wraps text past this many pixels
## A long line is shown a page at a time, at most this many lines a page, so
## the bubble never towers over the village. A page with more after it ends
## in MORE_MARK, and the page after it opens with LEAD_MARK, so the reader
## sees the sentence carry on.
const PAGE_LINES := 4
const MORE_MARK := " …"
const LEAD_MARK := "… "
const TAIL_HALF_W := 6.0
const TAIL_HEIGHT := 8.0
const FONT_SIZE := 14

const BG_COLOR := Color(0.97, 0.94, 0.86, 0.97)
const BORDER_COLOR := Color(0.22, 0.16, 0.10, 1.0)
const TEXT_COLOR := Color(0.10, 0.08, 0.05, 1.0)

# Each page stays up for its own reading time, from its length.
const MIN_LIFETIME := 5.0
const MAX_LIFETIME := 10.0
const LIFETIME_PER_CHAR := 1.0 / 18.0  # ~18 chars per second reading rate
const LIFETIME_PICKUP := 1.5            # extra buffer so bubbles don't vanish before noticed

const STACK_GAP := 2.0      # canvas px between stacked bubbles
const MAX_STACK := 3
const LIFT_EASE := 0.035    # seconds; a moving bubble settles in about 0.15 s
const STEM_COLOR := Color(0.094, 0.094, 0.094, 1.0)  # the art's outline

# Shared by every bubble: loaded once, null when the art is absent.
static var _art_sheet: Texture2D = null
static var _art_checked := false

# Every bubble in the tree, for the layout; spawn order decides who is newest.
static var _live: Array = []
static var _next_seq := 0
static var _laid_out_frame := -1

var _seq := 0
## Canvas px this bubble is raised above its anchor, eased toward the layout's target.
var _lift := 0.0
var _lift_target := 0.0
## The line from a lifted bubble's tail down to its speaker, drawn under every bubble.
var _stem: Node2D = null
var _stem_length := 0.0  # local units

var _wrapped_lines: PackedStringArray  # the page on show
var _pages: Array[PackedStringArray] = []
var _page := 0
var _page_timer: Timer = null
var _content_size: Vector2  # UI units; the same for every page, so the bubble doesn't jump
var _font: Font
var _font_size: int = FONT_SIZE
var _use_art := false
## Art px per UI unit: the window stretch over ART_PIXEL_SCALE. Converts the
## text's size into the 9-slice's growth, and scales the text layer.
var _art_per_ui := Vector2(1.0 / ART_PIXEL_SCALE, 1.0 / ART_PIXEL_SCALE)
## The art path's text, on its own canvas item so it can filter smoothly
## while the 9-slice stays nearest-filtered pixel art.
var _text_layer: Node2D = null

## Initialize the bubble with text and start the first page's timer. Call
## this immediately after add_child — the bubble turns its pages and
## queue_frees itself after the last.
func setup(speak_text: String) -> void:
    _load_art()
    _use_art = _art_sheet != null
    _font = ThemeDB.fallback_font
    _font_size = OrientationGuard.text_size(FONT_SIZE, OrientationGuard.BUBBLE_TOUCH_TEXT_SCALE)
    _seq = _next_seq
    _next_seq += 1
    if _use_art:
        _text_layer = Node2D.new()
        _text_layer.texture_filter = CanvasItem.TEXTURE_FILTER_LINEAR
        _text_layer.draw.connect(_draw_text_layer)
        add_child(_text_layer)
    _stem = Node2D.new()
    _stem.z_index = 99
    _stem.z_as_relative = false
    _stem.visible = false
    _stem.draw.connect(_draw_stem)
    add_child(_stem)
    _wrap_text(speak_text)
    if _pages.is_empty():
        # Nothing to say (blank or all spaces).
        queue_free()
        return
    _fit_to_screen()
    queue_redraw()

    _page_timer = Timer.new()
    _page_timer.one_shot = true
    _page_timer.timeout.connect(_next_page)
    add_child(_page_timer)
    _page_timer.start(_page_lifetime(_page))


## How long a page stays up: its reading time, clamped.
func _page_lifetime(page: int) -> float:
    var chars := 0
    for line in _pages[page]:
        chars += line.trim_suffix(MORE_MARK).trim_prefix(LEAD_MARK).length()
    return clampf(chars * LIFETIME_PER_CHAR + LIFETIME_PICKUP, MIN_LIFETIME, MAX_LIFETIME)


## Turn to the next page, or go after the last.
func _next_page() -> void:
    if _page + 1 >= _pages.size():
        queue_free()
        return
    _page += 1
    _wrapped_lines = _pages[_page]
    queue_redraw()
    if _text_layer != null:
        _text_layer.queue_redraw()
    _page_timer.start(_page_lifetime(_page))


static func _load_art() -> void:
    if _art_checked:
        return
    _art_checked = true
    # Typed cast so a failed import or a wrong file at the path reads as
    # "no art" rather than an error.
    if ResourceLoader.exists(ART_SHEET):
        _art_sheet = load(ART_SHEET) as Texture2D


## Greedy word-wrap to MAX_TEXT_WIDTH, cut into pages of PAGE_LINES. Stores
## the pages in _pages, the first in _wrapped_lines, and one bounding box
## for all of them (widest line × the most lines a page holds) in
## _content_size for the draw pass.
func _wrap_text(text: String) -> void:
    var max_w: float = MAX_TEXT_WIDTH - 2 * PADDING_X
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

    _pages.clear()
    var widest: float = 0.0
    for start in range(0, lines.size(), PAGE_LINES):
        var page := PackedStringArray(lines.slice(start, start + PAGE_LINES))
        if start + PAGE_LINES < lines.size():
            page[page.size() - 1] += MORE_MARK
        if start > 0:
            page[0] = LEAD_MARK + page[0]
        for line in page:
            var w_size: Vector2 = _font.get_string_size(
                line, HORIZONTAL_ALIGNMENT_LEFT, -1, _font_size
            )
            widest = max(widest, w_size.x)
        _pages.append(page)
    _page = 0
    _wrapped_lines = _pages[0] if not _pages.is_empty() else PackedStringArray()
    var line_height: float = _font.get_height(_font_size)
    _content_size = Vector2(ceilf(widest), line_height * mini(lines.size(), PAGE_LINES))


## The first bubble to process in a frame lays out all of them. Bubbles are
## spawned at the default priority (the WebSocket poll in event_client.gd,
## through world.gd and the talk panel) and process at 1000, so a new bubble
## is always in _live before this frame's layout runs. A bubble spawned from
## a node processing after 1000 would sit unstacked for one frame.
func _process(delta: float) -> void:
    var frame := Engine.get_process_frames()
    if frame != _laid_out_frame:
        _laid_out_frame = frame
        _layout(_live, delta)


func _enter_tree() -> void:
    _live.append(self)
    # A bubble spawned after this frame's layout gets one of its own.
    _laid_out_frame = -1


func _exit_tree() -> void:
    _live.erase(self)


## Newest first: each bubble keeps its place unless a newer one is in the
## way, and then rises the least that clears it (and whatever that lift
## runs into). A stack is every bubble joined by those collisions — one
## wide bubble can bridge two that never touch — and one that would make
## a stack bigger than MAX_STACK closes. Lifts ease toward their targets,
## and every bubble is then fitted with its lift.
static func _layout(bubbles: Array, delta: float) -> void:
    var order: Array = []
    for b in bubbles:
        if is_instance_valid(b) and b.is_inside_tree() and not b.is_queued_for_deletion() and not b._wrapped_lines.is_empty():
            b._fit_to_screen()
            order.append(b)
    order.sort_custom(func(x, y): return x._seq > y._seq)
    var blend: float = 1.0 - exp(-delta / LIFT_EASE)
    var placed: Array = []  # [Rect2 at its target lift, stack id]
    var stack_size := {}
    for b in order:
        var rect: Rect2 = b._canvas_rect()
        var lift := 0.0
        var moved := true
        while moved:
            moved = false
            for p in placed:
                var lifted := Rect2(rect.position - Vector2(0, lift), rect.size)
                var below: Rect2 = p[0]
                if lifted.intersects(below.grow(STACK_GAP)):
                    # Only a higher lift is a move: rounding can leave a
                    # cleared rect touching, and the same lift again would loop.
                    var clear: float = rect.end.y - below.position.y + STACK_GAP
                    if clear > lift:
                        lift = clear
                        moved = true
        # It joins every stack it rose over: whatever lies between its own
        # place and where it ends up.
        var touched := {}
        var swept := Rect2(rect.position - Vector2(0, lift), rect.size + Vector2(0, lift))
        for p in placed:
            if lift > 0.0 and swept.intersects(p[0].grow(STACK_GAP)):
                touched[p[1]] = true
        var size := 1
        for id in touched:
            size += stack_size[id]
        if size > MAX_STACK:
            b._close_early()
            continue
        var stack_id: int = b._seq
        for p in placed:
            if touched.has(p[1]):
                p[1] = stack_id
        for id in touched:
            stack_size.erase(id)
        stack_size[stack_id] = size
        placed.append([Rect2(rect.position - Vector2(0, lift), rect.size), stack_id])
        b._lift_target = lift
        b._lift = lerpf(b._lift, lift, blend)
        if absf(b._lift - lift) < 1.0:
            b._lift = lift
        b._fit_to_screen()


## This bubble at its anchor (no lift), in canvas px.
func _canvas_rect() -> Rect2:
    var parent_xform: Transform2D = (get_parent() as CanvasItem).get_global_transform_with_canvas()
    var anchor: Vector2 = parent_xform * Vector2(0, ANCHOR_Y)
    var units: Vector2 = parent_xform.get_scale() * scale
    var local: Rect2 = _local_rect()
    return Rect2(anchor + local.position * units, local.size * units)


## What the bubble draws, tail included, in its own units.
func _local_rect() -> Rect2:
    if _use_art:
        var grow: Vector2 = _art_grow()
        # Down to the last tail row; the sheet's rows under it are empty.
        return Rect2(_art_origin(), Vector2(_art_sheet.get_width() + grow.x, ART_TAIL_TIP.y + grow.y + 1.0))
    var w: float = _content_size.x + 2 * PADDING_X
    var h: float = _content_size.y + 2 * PADDING_Y
    return Rect2(-w * 0.5, -TAIL_HEIGHT - h, w, h + TAIL_HEIGHT)


## A fourth bubble in a stack goes before its time.
func _close_early() -> void:
    if _page_timer != null:
        _page_timer.stop()
    queue_free()


## The stem runs from the tail tip down to the anchor above the head: the
## art's one-pixel column under the tip, or a line from the plain tail apex.
func _draw_stem() -> void:
    if _use_art:
        _stem.draw_rect(Rect2(0, 1, 1, maxf(0.0, _stem_length - 1.0)), STEM_COLOR)
    else:
        _stem.draw_line(Vector2.ZERO, Vector2(0, _stem_length), BORDER_COLOR, 1.0)


func _place_stem(canvas_per_unit: float) -> void:
    if _stem == null or canvas_per_unit <= 0.0:
        return
    var length: float = _lift / canvas_per_unit
    _stem.visible = _lift >= 1.0
    if not is_equal_approx(length, _stem_length):
        _stem_length = length
        _stem.queue_redraw()


## Pin the bubble to the anchor above the head and size it for the screen,
## not the world. Two scales sit between this node and the screen: the
## canvas transform (camera zoom) and the viewport's stretch to the window
## (canvas_items stretch — get_final_transform, NOT part of the node's own
## screen transform).
##
## The plain bubble cancels the zoom only: it keeps the stretched size of
## the rest of the UI, and like the UI's own text its font is rasterized at
## the stretched size (font oversampling), so a fractional stretch does not
## resample it the way the zoom did.
##
## The art cancels both, then multiplies back up by ART_PIXEL_SCALE screen
## pixels per art pixel — a fixed whole number, not grown with the window
## stretch: grown, a 1920-wide window drew the bubble at 3x and it crowded
## the village. Its text layer is scaled back to UI units, so the text is
## the same size as the plain bubble's. The stretch differs slightly per
## axis (window 1920x1061 → 1.4747 x 1.4736), so each axis is its own.
func _fit_to_screen() -> void:
    position = Vector2(0, ANCHOR_Y)
    var parent := get_parent() as CanvasItem
    if parent == null or not parent.is_inside_tree():
        return
    var canvas_scale: Vector2 = parent.get_global_transform_with_canvas().get_scale()
    if canvas_scale.x <= 0.0 or canvas_scale.y <= 0.0:
        return
    position.y -= _lift / canvas_scale.y
    if not _use_art:
        scale = Vector2.ONE / canvas_scale
        _place_stem(1.0)
        return
    var stretch: Vector2 = get_viewport().get_final_transform().get_scale()
    if stretch.x <= 0.0 or stretch.y <= 0.0:
        stretch = Vector2.ONE
    scale = Vector2.ONE * float(ART_PIXEL_SCALE) / (canvas_scale * stretch)
    var art_per_ui: Vector2 = stretch / float(ART_PIXEL_SCALE)
    if not art_per_ui.is_equal_approx(_art_per_ui):
        # A window resize changes how many art px the text needs.
        _art_per_ui = art_per_ui
        queue_redraw()
    _place_text_layer()
    _place_stem(canvas_scale.y * scale.y)


func _place_text_layer() -> void:
    if _text_layer == null:
        return
    _text_layer.scale = _art_per_ui
    _text_layer.position = _art_origin() + ART_TEXT_ORIGIN
    _text_layer.queue_redraw()


func _draw() -> void:
    if _wrapped_lines.is_empty():
        return
    if _use_art:
        _draw_art()
    else:
        _draw_plain()


## The Mana Seed 9-slice, in art pixels, laid out so the tail's point sits
## on this node's origin. Grows by repeating the one uniform column and the
## one middle row.
func _draw_art() -> void:
    var grow: Vector2 = _art_grow()
    var origin: Vector2 = _art_origin()
    var sheet_size := Vector2(_art_sheet.get_size())
    var src_x := [0.0, float(ART_SLICE_COL), ART_SLICE_COL + 1.0, sheet_size.x]
    var dst_x := [0.0, float(ART_SLICE_COL), ART_SLICE_COL + 1.0 + grow.x, sheet_size.x + grow.x]
    var bottom_rows: float = sheet_size.y - ART_BOTTOM_ROW
    # [source top, source height, destination height] per band.
    var bands := [
        [0.0, float(ART_TOP_ROWS), float(ART_TOP_ROWS)],
        [float(ART_MIDDLE_ROW), 1.0, grow.y],
        [float(ART_BOTTOM_ROW), bottom_rows, bottom_rows],
    ]
    var dst_top: float = 0.0
    for band in bands:
        if band[2] > 0.0:
            for col in 3:
                var src := Rect2(src_x[col], band[0], src_x[col + 1] - src_x[col], band[1])
                var dst := Rect2(dst_x[col], dst_top, dst_x[col + 1] - dst_x[col], band[2])
                dst.position += origin
                draw_texture_rect_region(_art_sheet, dst, src)
        dst_top += band[2]


## The art path's text, in UI units on the text layer.
func _draw_text_layer() -> void:
    var line_height: float = _font.get_height(_font_size)
    var y: float = _font.get_ascent(_font_size)
    for line in _wrapped_lines:
        _text_layer.draw_string(
            _font, Vector2(0, y), line,
            HORIZONTAL_ALIGNMENT_LEFT, -1, _font_size, TEXT_COLOR
        )
        y += line_height


## The text's size in art px, rounded up to whole art pixels.
func _content_art_size() -> Vector2:
    return (_content_size * _art_per_ui).ceil()


## How far the 9-slice grows, in art px, so the wrapped text fits inside
## the fill: x = extra copies of the slice column past the sheet's width,
## y = copies of the middle row.
func _art_grow() -> Vector2:
    var content: Vector2 = _content_art_size()
    return Vector2(
        maxf(0.0, content.x - ART_TEXT_ROOM.x),
        maxf(0.0, content.y - ART_TEXT_ROOM.y)
    )


## Top-left of the 9-slice in this node's art px, so the tail's point is
## on the origin.
func _art_origin() -> Vector2:
    return -ART_TAIL_TIP - Vector2(0, _art_grow().y)


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
    var line_height: float = _font.get_height(_font_size)
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
    # Fit after the camera has applied this frame's zoom, so a zoom step
    # never draws one frame at the old size.
    process_priority = 1000
