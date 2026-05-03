extends CanvasLayer

## Notice panel (ZBBS-112) — modal for reading the prose posted on a
## village noticeboard. Shown after the PC walks up to a placement
## tagged `noticeboard_content` whose `content_text` is populated.
##
## Mounted as a CanvasLayer from main.gd. Opens via `show_for_object()`
## with the noticeboard's display name and the cached content fields;
## subscribes to world.object_content_changed so the rendered text
## stays current if the crier rotates the board mid-read.
##
## Layer ordering: 4 — above the editor UI (default 1) and the talk
## panel (3), below the config screen (5) and login (10). The panel
## is a focus-stealing modal — main.gd flips camera.modal_open while
## it's visible so a click on the dim backdrop doesn't bleed into the
## click-to-walk handler.
##
## Dismiss paths:
##   - Escape key
##   - the close (X) button in the panel header
##   - clicking the dim backdrop outside the sheet
##   - main.gd auto-closing on a subsequent /pc/move (see close())
##
## Runtime dependencies:
##   - parent main.gd that listens to opened/closed for camera modal coord
##   - World node that emits object_content_changed so live edits propagate

const PANEL_WIDTH := 420.0
const PANEL_MIN_HEIGHT := 220.0
const PANEL_MAX_HEIGHT_FRAC := 0.75 # cap at 75% of viewport so very long
                                    # notices stay scrollable without
                                    # filling the screen edge to edge

# Public so main.gd / camera can ask "is the panel up right now?".
var open_for_object_id: String = ""

# Layout
var root: Control = null
var backdrop: ColorRect = null
var sheet: PanelContainer = null
var title_label: Label = null
var posted_label: Label = null
var body_scroll: ScrollContainer = null
var body_label: RichTextLabel = null
var close_button: Button = null

# Optional reference so the panel can keep itself in sync as the engine
# broadcasts content edits while the player is reading. Wired by main.gd
# at init via attach_world().
var world: Node2D = null

signal opened()
signal closed()


func _ready() -> void:
    layer = 4
    _build_ui()
    visible = false
    set_process_unhandled_input(true)


## Wire the world reference and connect to its content-changed signal.
## main.gd calls this once after the World node is reachable.
func attach_world(world_ref: Node2D) -> void:
    if world == world_ref:
        return
    if world != null and world.has_signal("object_content_changed"):
        if world.object_content_changed.is_connected(_on_world_content_changed):
            world.object_content_changed.disconnect(_on_world_content_changed)
    world = world_ref
    if world != null and world.has_signal("object_content_changed"):
        world.object_content_changed.connect(_on_world_content_changed)


func _build_ui() -> void:
    root = Control.new()
    root.set_anchors_preset(Control.PRESET_FULL_RECT)
    root.mouse_filter = Control.MOUSE_FILTER_STOP
    add_child(root)

    # Dim backdrop. Click-through to close.
    backdrop = ColorRect.new()
    backdrop.color = Color(0, 0, 0, 0.45)
    backdrop.set_anchors_preset(Control.PRESET_FULL_RECT)
    backdrop.mouse_filter = Control.MOUSE_FILTER_STOP
    backdrop.gui_input.connect(_on_backdrop_input)
    root.add_child(backdrop)

    # Centered sheet.
    var center := CenterContainer.new()
    center.set_anchors_preset(Control.PRESET_FULL_RECT)
    center.mouse_filter = Control.MOUSE_FILTER_IGNORE
    root.add_child(center)

    sheet = PanelContainer.new()
    sheet.custom_minimum_size = Vector2(PANEL_WIDTH, PANEL_MIN_HEIGHT)
    sheet.mouse_filter = Control.MOUSE_FILTER_STOP
    center.add_child(sheet)

    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 8)
    sheet.add_child(vb)

    # Header row: title left, close button right.
    var header := HBoxContainer.new()
    header.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    vb.add_child(header)

    title_label = Label.new()
    title_label.text = "Notice"
    title_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    title_label.add_theme_font_size_override("font_size", 18)
    header.add_child(title_label)

    close_button = Button.new()
    close_button.text = "X"
    close_button.custom_minimum_size = Vector2(32, 32)
    close_button.pressed.connect(close)
    header.add_child(close_button)

    posted_label = Label.new()
    posted_label.text = ""
    posted_label.add_theme_font_size_override("font_size", 11)
    posted_label.modulate = Color(0.65, 0.65, 0.65)
    vb.add_child(posted_label)

    # Body scroller — caps at PANEL_MAX_HEIGHT_FRAC of viewport.
    body_scroll = ScrollContainer.new()
    body_scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    body_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    body_scroll.custom_minimum_size = Vector2(PANEL_WIDTH - 24, 120)
    vb.add_child(body_scroll)

    body_label = RichTextLabel.new()
    body_label.bbcode_enabled = false
    body_label.fit_content = true
    body_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    body_label.size_flags_vertical = Control.SIZE_EXPAND_FILL
    body_label.add_theme_font_size_override("normal_font_size", 14)
    body_scroll.add_child(body_label)


## Open the panel for a placement. content_text and content_posted_at
## may be null when the board has been cleared between hand-off and
## render — render the empty state instead of refusing to open, so the
## walk-up doesn't feel like nothing happened.
func show_for_object(object_id: String, display_name: String, content_text, content_posted_at) -> void:
    if object_id == "":
        return
    open_for_object_id = object_id
    _set_title(display_name)
    _set_content(content_text, content_posted_at)
    _resize_sheet_for_viewport()
    visible = true
    opened.emit()


## Replace title + body in place. Called from the world subscription so
## a crier flip during reading swaps the text live without forcing the
## reader to dismiss + re-open.
func update_for_object(object_id: String, content_text, content_posted_at) -> void:
    if open_for_object_id == "" or object_id != open_for_object_id:
        return
    _set_content(content_text, content_posted_at)


## main.gd calls close() on dismiss key, on a subsequent click-to-walk,
## or on logout. Idempotent.
func close() -> void:
    if not visible:
        return
    visible = false
    open_for_object_id = ""
    closed.emit()


func _set_title(display_name: String) -> void:
    var t := display_name
    if t == "":
        t = "Noticeboard"
    title_label.text = t


func _set_content(content_text, content_posted_at) -> void:
    if content_text == null or str(content_text) == "":
        body_label.text = "(The board is bare.)"
        posted_label.text = ""
        return
    body_label.text = str(content_text)
    if content_posted_at != null and str(content_posted_at) != "":
        posted_label.text = "Posted " + _format_posted_at(str(content_posted_at))
    else:
        posted_label.text = ""


## Light-touch formatter — content_posted_at arrives as RFC3339 from the
## server. Render the date + clock time portion in the player's locale,
## falling back to the raw string on parse failure.
func _format_posted_at(iso: String) -> String:
    # RFC3339 looks like 2026-05-03T17:42:11Z. Drop the fractional second
    # / timezone suffix, then split on T.
    var s := iso
    var z_idx := s.find("Z")
    if z_idx > -1:
        s = s.substr(0, z_idx)
    var plus_idx := s.find("+")
    if plus_idx > -1:
        s = s.substr(0, plus_idx)
    # Trim sub-second precision if present.
    var dot_idx := s.find(".")
    if dot_idx > -1:
        s = s.substr(0, dot_idx)
    var t_idx := s.find("T")
    if t_idx == -1:
        return iso
    var date_part := s.substr(0, t_idx)
    var time_part := s.substr(t_idx + 1, 5) # hh:mm
    if date_part == "" or time_part == "":
        return iso
    return date_part + " " + time_part + " UTC"


func _resize_sheet_for_viewport() -> void:
    if sheet == null:
        return
    var vp_size := get_viewport().get_visible_rect().size
    var max_h := vp_size.y * PANEL_MAX_HEIGHT_FRAC
    if max_h < PANEL_MIN_HEIGHT:
        max_h = PANEL_MIN_HEIGHT
    sheet.custom_minimum_size = Vector2(PANEL_WIDTH, PANEL_MIN_HEIGHT)
    body_scroll.custom_minimum_size = Vector2(PANEL_WIDTH - 24, max_h - 90)


func _on_backdrop_input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
        close()
        # Mark the click consumed so it doesn't propagate to other input
        # handlers — the main.gd click-to-walk handler is the relevant
        # one. MOUSE_FILTER_STOP on the backdrop should already block
        # propagation, but explicit handling guards against an input-
        # ordering change downstream.
        get_viewport().set_input_as_handled()


func _unhandled_input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        close()
        get_viewport().set_input_as_handled()


func _on_world_content_changed(object_id: String, content_text, content_posted_at) -> void:
    update_for_object(object_id, content_text, content_posted_at)
