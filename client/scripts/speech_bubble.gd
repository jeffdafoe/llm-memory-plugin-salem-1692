## Speech bubble — a Node2D that draws a wrapped-text bubble with a tail
## above its parent's origin. Designed to be added as a child of an NPC
## or PC container Node so it auto-follows the sprite as it walks.
##
## Single-instance per parent: the bubble manager (world.gd's _on_npc_spoke
## handler) names this child SpeechBubble and removes any existing one
## before adding a new bubble — so a fresh speak from the same NPC
## replaces the old line immediately rather than queuing.
##
## Drawn with primitives (draw_rect, draw_polygon, draw_string) rather
## than nested Control nodes — Controls render in screen-space, which
## doesn't compose with the Node2D world-space sprite hierarchy.

class_name SpeechBubble
extends Node2D

const PADDING_X := 8.0
const PADDING_Y := 5.0
const MAX_TEXT_WIDTH := 220.0  # bubble wraps text past this many pixels
const TAIL_APEX_Y := -88.0     # tail tip sits this many px above container origin
const TAIL_HALF_W := 6.0
const TAIL_HEIGHT := 8.0
const FONT_SIZE := 14

const BG_COLOR := Color(0.97, 0.94, 0.86, 0.97)
const BORDER_COLOR := Color(0.22, 0.16, 0.10, 1.0)
const TEXT_COLOR := Color(0.10, 0.08, 0.05, 1.0)

# Lifetime is computed by setup() based on text length unless caller overrides.
const MIN_LIFETIME := 3.0
const MAX_LIFETIME := 10.0
const LIFETIME_PER_CHAR := 1.0 / 18.0  # ~18 chars per second reading rate
const LIFETIME_PICKUP := 1.5            # extra buffer so bubbles don't vanish before noticed

var _wrapped_lines: PackedStringArray
var _content_size: Vector2
var _font: Font

## Initialize the bubble with text and start the lifetime timer. Call
## this immediately after add_child — the bubble queue_frees itself
## when the timer fires.
func setup(speak_text: String) -> void:
    _font = ThemeDB.fallback_font
    _wrap_text(speak_text)
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


## Greedy word-wrap to MAX_TEXT_WIDTH. Stores wrapped lines in
## _wrapped_lines and the bounding box (widest line × line count) in
## _content_size for the draw pass.
func _wrap_text(text: String) -> void:
    var max_w: float = MAX_TEXT_WIDTH - 2 * PADDING_X
    var words := text.split(" ", false)
    var lines: Array[String] = []
    var current := ""
    for w in words:
        var trial: String = w if current == "" else current + " " + w
        var trial_size: Vector2 = _font.get_string_size(
            trial, HORIZONTAL_ALIGNMENT_LEFT, -1, FONT_SIZE
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
            line, HORIZONTAL_ALIGNMENT_LEFT, -1, FONT_SIZE
        )
        widest = max(widest, w_size.x)
    var line_height: float = _font.get_height(FONT_SIZE)
    _content_size = Vector2(widest, line_height * _wrapped_lines.size())


func _draw() -> void:
    if _wrapped_lines.is_empty():
        return

    var bubble_w: float = _content_size.x + 2 * PADDING_X
    var bubble_h: float = _content_size.y + 2 * PADDING_Y

    # Bubble centered horizontally on container; bottom edge sits just
    # above where the tail starts. Tail apex (TAIL_APEX_Y) is the
    # bottom-most point of the bubble's visual footprint.
    var bubble_top: float = TAIL_APEX_Y - TAIL_HEIGHT - bubble_h
    var bubble_left: float = -bubble_w * 0.5
    var rect := Rect2(bubble_left, bubble_top, bubble_w, bubble_h)
    draw_rect(rect, BG_COLOR, true)
    draw_rect(rect, BORDER_COLOR, false, 1.0)

    # Tail (downward triangle, fill + stroke).
    var tail_top_y: float = bubble_top + bubble_h
    var tail_left := Vector2(-TAIL_HALF_W, tail_top_y)
    var tail_right := Vector2(TAIL_HALF_W, tail_top_y)
    var tail_apex := Vector2(0, TAIL_APEX_Y)
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
    var line_height: float = _font.get_height(FONT_SIZE)
    var ascent: float = _font.get_ascent(FONT_SIZE)
    var text_x: float = bubble_left + PADDING_X
    var text_y: float = bubble_top + PADDING_Y + ascent
    for line in _wrapped_lines:
        draw_string(
            _font, Vector2(text_x, text_y), line,
            HORIZONTAL_ALIGNMENT_LEFT, -1, FONT_SIZE, TEXT_COLOR
        )
        text_y += line_height


## Z-index above sprites so the bubble doesn't get clipped by overlapping
## NPC bodies. Sprite z is sorted by y for depth-painting; bubbles need
## to win regardless.
func _ready() -> void:
    z_index = 100
    z_as_relative = false
