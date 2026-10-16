extends CanvasLayer

## The note window (LLM-19). After a knock nobody answers, the door popup's
## Leave a note opens this: a sheet of worn parchment with who the note is for,
## the note itself (at most MAX_CHARS), and [Pin it to the door] / [Never mind].
## Pin emits pinned(structure_id, recipient_id, text); main.gd POSTs
## pc/leave-note and closes it on success, or calls show_error with the reason.
##
## Who it is for: the people of the house, from the knock's note_for. One name
## is already chosen; with more, none is, and Pin waits until the player picks.
##
## The parchment, button and quill are operator-managed art served from
## /tilesets/parchment-ui/ ("Pixel Parchment UI Kit" by The Bee's Knees — see
## the credits scroll). Until a picture lands, or if it never does, the sheet
## and buttons are drawn in plain parchment colours, so the window always works.
##
## Layer 4, the modal tier; it sits near the top of the screen so a tablet's
## on-screen keyboard does not cover the note. main.gd blocks world input while
## it is open (opened / closed).

const FONT_PATH := "res://assets/fonts/IMFellEnglish-Regular.ttf"
const ITALIC_PATH := "res://assets/fonts/IMFellEnglish-Italic.ttf"
const MAX_CHARS := 200

const SHEET_PATH := "/tilesets/parchment-ui/worn-parchment.png"
const BUTTON_PATH := "/tilesets/parchment-ui/brown-button.png"
const QUILL_PATH := "/tilesets/parchment-ui/quill.png"

## The sheet's torn edges and curled corners, in art pixels, kept whole when it
## stretches to fit the note (9-slice): the roll at the top left is about 75 x
## 50, the turned-up corner at the bottom right about 30 x 55.
const SHEET_MARGIN_LEFT := 78
const SHEET_MARGIN_TOP := 52
const SHEET_MARGIN_RIGHT := 36
const SHEET_MARGIN_BOTTOM := 58
## Where the writing starts inside the sheet — within the slice margins, clear
## of the torn edge; the centred title and buttons pass the curls.
const SHEET_PAD_X := 32
const SHEET_PAD_Y := 30
const BUTTON_MARGIN := 10

const SHEET_WIDTH := 480.0
const SCREEN_MARGIN := 12.0
const TOP_MARGIN := 24.0
const TAP_HEIGHT := 44.0

const COLOR_INK := Color(0.23, 0.165, 0.086)
const COLOR_INK_SOFT := Color(0.42, 0.32, 0.21)
const COLOR_INK_FAINT := Color(0.42, 0.32, 0.21, 0.45)
const COLOR_ERROR := Color(0.55, 0.14, 0.08)
const COLOR_PAPER := Color(0.86, 0.76, 0.60)
const COLOR_PAPER_EDGE := Color(0.62, 0.50, 0.33)
const COLOR_BUTTON := Color(0.50, 0.34, 0.18)
const COLOR_BUTTON_TEXT := Color(0.98, 0.92, 0.80)

signal opened()
signal closed()
## The player pinned the note: POST it.
signal pinned(structure_id: String, recipient_id: String, text: String)

## Test seam: when set, returns the Texture2D for a served path (or null)
## instead of fetching it.
var texture_hook: Callable

var structure_id := ""
var recipient_id := ""
## Counts each opening, so an answer to a note sent from an earlier opening is
## not applied to this one (answers_session).
var session := 0

var root: Control = null
var backdrop: ColorRect = null
var sheet: PanelContainer = null
var title_label: Label = null
var house_label: Label = null
var for_label: Label = null
var only_name: Label = null
var names_row: HFlowContainer = null
var note_edit: TextEdit = null
var counter_label: Label = null
var error_label: Label = null
var pin_button: Button = null
var cancel_button: Button = null

var _font: Font = null
var _italic: Font = null
var _names_group: ButtonGroup = null
var _sending := false

## Pictures by served path, shared by every window (static: fetched once).
static var _textures: Dictionary = {}
var _fetching: Dictionary = {}


func _ready() -> void:
    layer = 4
    _font = load(FONT_PATH) if ResourceLoader.exists(FONT_PATH) else ThemeDB.fallback_font
    _italic = load(ITALIC_PATH) if ResourceLoader.exists(ITALIC_PATH) else _font
    _build_ui()
    visible = false


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

    # Top-anchored, centred across: a tablet's keyboard rises from the bottom.
    var holder := MarginContainer.new()
    holder.set_anchors_preset(Control.PRESET_TOP_WIDE)
    holder.add_theme_constant_override("margin_top", int(TOP_MARGIN))
    holder.mouse_filter = Control.MOUSE_FILTER_IGNORE
    root.add_child(holder)
    var across := HBoxContainer.new()
    across.alignment = BoxContainer.ALIGNMENT_CENTER
    across.mouse_filter = Control.MOUSE_FILTER_IGNORE
    holder.add_child(across)

    sheet = PanelContainer.new()
    sheet.mouse_filter = Control.MOUSE_FILTER_STOP
    across.add_child(sheet)

    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 8)
    sheet.add_child(vb)

    title_label = _label(_font, 21, COLOR_INK, HORIZONTAL_ALIGNMENT_CENTER)
    title_label.text = "A note for the door"
    vb.add_child(title_label)
    house_label = _label(_italic, 14, COLOR_INK_SOFT, HORIZONTAL_ALIGNMENT_CENTER)
    vb.add_child(house_label)

    var for_row := HBoxContainer.new()
    for_row.add_theme_constant_override("separation", 8)
    vb.add_child(for_row)
    for_label = _label(_italic, 16, COLOR_INK_SOFT, HORIZONTAL_ALIGNMENT_LEFT)
    for_label.text = "For"
    for_label.autowrap_mode = TextServer.AUTOWRAP_OFF
    for_label.size_flags_vertical = Control.SIZE_SHRINK_CENTER
    for_row.add_child(for_label)
    only_name = _label(_font, 17, COLOR_INK, HORIZONTAL_ALIGNMENT_LEFT)
    only_name.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    only_name.size_flags_vertical = Control.SIZE_SHRINK_CENTER
    for_row.add_child(only_name)
    names_row = HFlowContainer.new()
    names_row.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    names_row.add_theme_constant_override("h_separation", 6)
    names_row.add_theme_constant_override("v_separation", 6)
    for_row.add_child(names_row)

    note_edit = TextEdit.new()
    note_edit.placeholder_text = "Write your note…"
    note_edit.wrap_mode = TextEdit.LINE_WRAPPING_BOUNDARY
    note_edit.custom_minimum_size = Vector2(0, OrientationGuard.text_size(104))
    note_edit.add_theme_font_override("font", _font)
    note_edit.add_theme_font_size_override("font_size", OrientationGuard.text_size(17))
    note_edit.add_theme_color_override("font_color", COLOR_INK)
    note_edit.add_theme_color_override("font_placeholder_color", COLOR_INK_FAINT)
    note_edit.add_theme_color_override("caret_color", COLOR_INK)
    note_edit.add_theme_color_override("selection_color", Color(0.62, 0.50, 0.33, 0.35))
    var writing := StyleBoxFlat.new()
    writing.bg_color = Color(1, 1, 1, 0.0)
    writing.border_color = COLOR_INK_FAINT
    writing.border_width_bottom = 1
    writing.content_margin_left = 4
    writing.content_margin_right = 4
    writing.content_margin_top = 4
    writing.content_margin_bottom = 4
    note_edit.add_theme_stylebox_override("normal", writing)
    var writing_focus := writing.duplicate()
    writing_focus.border_color = COLOR_INK_SOFT
    note_edit.add_theme_stylebox_override("focus", writing_focus)
    note_edit.text_changed.connect(_on_text_changed)
    vb.add_child(note_edit)

    counter_label = _label(_italic, 13, COLOR_INK_SOFT, HORIZONTAL_ALIGNMENT_RIGHT)
    vb.add_child(counter_label)
    error_label = _label(_font, 14, COLOR_ERROR, HORIZONTAL_ALIGNMENT_CENTER)
    error_label.visible = false
    vb.add_child(error_label)

    var buttons := HBoxContainer.new()
    buttons.alignment = BoxContainer.ALIGNMENT_CENTER
    buttons.add_theme_constant_override("separation", 14)
    vb.add_child(buttons)
    pin_button = _button("Pin it to the door", _on_pin_pressed)
    cancel_button = _button("Never mind", close)
    buttons.add_child(pin_button)
    buttons.add_child(cancel_button)
    _apply_art()


func _label(font: Font, size: int, color: Color, align: HorizontalAlignment) -> Label:
    var l := Label.new()
    l.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    l.horizontal_alignment = align
    l.add_theme_font_override("font", font)
    l.add_theme_font_size_override("font_size", OrientationGuard.text_size(size))
    l.add_theme_color_override("font_color", color)
    return l


func _button(text: String, cb: Callable) -> Button:
    var b := Button.new()
    b.text = text
    b.custom_minimum_size = Vector2(0, TAP_HEIGHT)
    b.add_theme_font_override("font", _font)
    b.add_theme_font_size_override("font_size", OrientationGuard.text_size(16))
    b.add_theme_color_override("font_color", COLOR_BUTTON_TEXT)
    b.add_theme_color_override("font_hover_color", Color(1, 0.97, 0.88))
    b.add_theme_color_override("font_disabled_color", Color(0.85, 0.78, 0.66, 0.6))
    b.pressed.connect(cb)
    return b


## Open for structure `sid`, named `house`, with `recipients` ([{id, name}])
## the people a note may be for. Opening again while open starts afresh.
func open(sid: String, house: String, recipients: Array) -> void:
    structure_id = sid
    session += 1
    recipient_id = ""
    _sending = false
    house_label.text = house
    house_label.visible = house != ""
    note_edit.text = ""
    error_label.visible = false
    _fill_names(recipients)
    _refresh()
    _fit_width()
    if not visible:
        visible = true
        opened.emit()
    note_edit.grab_focus.call_deferred()


func close() -> void:
    if not visible:
        return
    visible = false
    structure_id = ""
    recipient_id = ""
    _sending = false
    closed.emit()


func is_open() -> bool:
    return visible


## Whether an answer to a note sent from opening `sent_session` still belongs
## here: the same opening, still open.
func answers_session(sent_session: int) -> bool:
    return visible and sent_session == session


## The engine's answer to the note sent from opening `sent_session` (HTTP code
## and body): pinned → close; refused → say why. An answer for an opening since
## closed or replaced is dropped — it is not this window's.
func take_answer(sent_session: int, code: int, body: String) -> void:
    if not answers_session(sent_session):
        return
    if code >= 200 and code < 300:
        close()
        return
    var parsed = JSON.parse_string(body)
    show_error(refusal_line(str(parsed.get("error", "")) if parsed is Dictionary else ""))


## The engine refused the note (or the request failed): say why and let the
## player try again.
func show_error(message: String) -> void:
    _sending = false
    error_label.text = message if message != "" else "The note could not be pinned."
    error_label.visible = true
    _refresh()


## The engine's refusal as a line for the window: its last reason, capitalised
## and closed with a full stop ("you cannot leave a note here: you are not at
## that door" → "You are not at that door.").
static func refusal_line(message: String) -> String:
    var reason := message.strip_edges()
    var cut := reason.rfind(": ")
    if cut >= 0:
        reason = reason.substr(cut + 2)
    if reason == "":
        return "The note could not be pinned."
    reason = reason.left(1).to_upper() + reason.substr(1)
    if not reason.ends_with(".") and not reason.ends_with("!") and not reason.ends_with("?"):
        reason += "."
    return reason


## One name: shown and chosen. More: a toggle per name, none chosen.
func _fill_names(recipients: Array) -> void:
    for c in names_row.get_children():
        names_row.remove_child(c)
        c.queue_free()
    _names_group = ButtonGroup.new()
    var people := recipients.filter(func(r): return r is Dictionary and str(r.get("id", "")) != "")
    only_name.visible = people.size() == 1
    names_row.visible = people.size() > 1
    if people.size() == 1:
        only_name.text = str(people[0].get("name", ""))
        recipient_id = str(people[0]["id"])
        return
    for r in people:
        var b := Button.new()
        b.text = str(r.get("name", ""))
        b.toggle_mode = true
        b.button_group = _names_group
        b.custom_minimum_size = Vector2(0, TAP_HEIGHT - 6)
        b.add_theme_font_override("font", _font)
        b.add_theme_font_size_override("font_size", OrientationGuard.text_size(16))
        b.add_theme_color_override("font_color", COLOR_INK_SOFT)
        b.add_theme_color_override("font_hover_color", COLOR_INK)
        b.add_theme_color_override("font_pressed_color", COLOR_INK)
        b.add_theme_color_override("font_hover_pressed_color", COLOR_INK)
        var id := str(r["id"])
        b.toggled.connect(func(on: bool):
            if on:
                recipient_id = id
                _refresh())
        _style_name_button(b)
        names_row.add_child(b)


func _style_name_button(b: Button) -> void:
    var plain := StyleBoxFlat.new()
    plain.bg_color = Color(1, 1, 1, 0.0)
    plain.border_color = COLOR_INK_FAINT
    plain.border_width_bottom = 1
    plain.content_margin_left = 8
    plain.content_margin_right = 8
    var chosen := plain.duplicate()
    chosen.bg_color = Color(0.62, 0.50, 0.33, 0.28)
    chosen.border_color = COLOR_INK
    chosen.border_width_bottom = 2
    chosen.corner_radius_top_left = 4
    chosen.corner_radius_top_right = 4
    b.add_theme_stylebox_override("normal", plain)
    b.add_theme_stylebox_override("hover", plain)
    b.add_theme_stylebox_override("focus", plain)
    b.add_theme_stylebox_override("pressed", chosen)
    b.add_theme_stylebox_override("hover_pressed", chosen)


## TextEdit has no length cap: cut anything past MAX_CHARS, keeping the caret.
func _on_text_changed() -> void:
    if note_edit.text.length() > MAX_CHARS:
        var line := note_edit.get_caret_line()
        var col := note_edit.get_caret_column()
        note_edit.text = note_edit.text.substr(0, MAX_CHARS)
        note_edit.set_caret_line(mini(line, note_edit.get_line_count() - 1))
        note_edit.set_caret_column(col)
    error_label.visible = false
    _refresh()


func _refresh() -> void:
    var n := note_edit.text.strip_edges().length()
    counter_label.text = "%d / %d" % [note_edit.text.length(), MAX_CHARS]
    pin_button.disabled = _sending or recipient_id == "" or n == 0


## Whether the note may be pinned now (a recipient and some text).
func can_pin() -> bool:
    return not pin_button.disabled


func _on_pin_pressed() -> void:
    if not can_pin() or structure_id == "":
        return
    _sending = true
    _refresh()
    pinned.emit(structure_id, recipient_id, note_edit.text.strip_edges())


## The sheet's width: SHEET_WIDTH, narrowed to the screen with a margin.
func _fit_width() -> void:
    var vw := SHEET_WIDTH
    var vp := get_viewport()
    if vp != null:
        vw = minf(SHEET_WIDTH, vp.get_visible_rect().size.x - SCREEN_MARGIN * 2.0)
    sheet.custom_minimum_size = Vector2(maxf(vw, 280.0), 0)


func _on_backdrop_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
        close()
        get_viewport().set_input_as_handled()


func _unhandled_input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and not event.echo and event.keycode == KEY_ESCAPE:
        close()
        get_viewport().set_input_as_handled()


# --- art ---------------------------------------------------------------------

## Put the parchment, button and quill on, or the plain-colour stand-ins for
## any picture not yet here.
func _apply_art() -> void:
    var paper := _texture(SHEET_PATH)
    if paper != null:
        var st := StyleBoxTexture.new()
        st.texture = paper
        st.texture_margin_left = SHEET_MARGIN_LEFT
        st.texture_margin_top = SHEET_MARGIN_TOP
        st.texture_margin_right = SHEET_MARGIN_RIGHT
        st.texture_margin_bottom = SHEET_MARGIN_BOTTOM
        st.content_margin_left = SHEET_PAD_X
        st.content_margin_top = SHEET_PAD_Y
        st.content_margin_right = SHEET_PAD_X
        st.content_margin_bottom = SHEET_PAD_Y + 6
        sheet.add_theme_stylebox_override("panel", st)
    else:
        var flat := StyleBoxFlat.new()
        flat.bg_color = COLOR_PAPER
        flat.border_color = COLOR_PAPER_EDGE
        for side in ["border_width_left", "border_width_right", "border_width_top", "border_width_bottom"]:
            flat.set(side, 2)
        flat.content_margin_left = 28
        flat.content_margin_right = 28
        flat.content_margin_top = 22
        flat.content_margin_bottom = 22
        flat.shadow_color = Color(0, 0, 0, 0.4)
        flat.shadow_size = 14
        flat.shadow_offset = Vector2(0, 5)
        sheet.add_theme_stylebox_override("panel", flat)
    var button_art := _texture(BUTTON_PATH)
    for b in [pin_button, cancel_button]:
        var normal: StyleBox
        if button_art != null:
            var tb := StyleBoxTexture.new()
            tb.texture = button_art
            for side in ["texture_margin_left", "texture_margin_right", "texture_margin_top", "texture_margin_bottom"]:
                tb.set(side, BUTTON_MARGIN)
            tb.content_margin_left = 16
            tb.content_margin_right = 16
            normal = tb
        else:
            var fb := StyleBoxFlat.new()
            fb.bg_color = COLOR_BUTTON
            fb.corner_radius_top_left = 6
            fb.corner_radius_top_right = 6
            fb.corner_radius_bottom_left = 6
            fb.corner_radius_bottom_right = 6
            fb.content_margin_left = 16
            fb.content_margin_right = 16
            normal = fb
        var hover := normal.duplicate()
        hover.set("modulate_color", Color(1.12, 1.08, 1.0))
        var down := normal.duplicate()
        down.set("modulate_color", Color(0.85, 0.82, 0.78))
        var off := normal.duplicate()
        off.set("modulate_color", Color(1, 1, 1, 0.55))
        if normal is StyleBoxFlat:
            hover.set("bg_color", COLOR_BUTTON.lightened(0.12))
            down.set("bg_color", COLOR_BUTTON.darkened(0.15))
            off.set("bg_color", Color(COLOR_BUTTON, 0.5))
        # The kit's button is a light parchment tab: ink on it, not cream.
        var text_color := COLOR_INK if button_art != null else COLOR_BUTTON_TEXT
        b.add_theme_color_override("font_color", text_color)
        b.add_theme_color_override("font_hover_color", text_color.darkened(0.25) if button_art != null else Color(1, 0.97, 0.88))
        b.add_theme_color_override("font_pressed_color", text_color)
        b.add_theme_color_override("font_focus_color", text_color)
        b.add_theme_color_override("font_disabled_color", Color(text_color, 0.45))
        b.add_theme_stylebox_override("normal", normal)
        b.add_theme_stylebox_override("hover", hover)
        b.add_theme_stylebox_override("pressed", down)
        b.add_theme_stylebox_override("focus", normal)
        b.add_theme_stylebox_override("disabled", off)
    var quill := _texture(QUILL_PATH)
    pin_button.icon = quill
    pin_button.expand_icon = false


## A picture by its served path: cached, else fetched (null until it lands;
## _apply_art puts it on then).
func _texture(path: String) -> Texture2D:
    if texture_hook.is_valid():
        return texture_hook.call(path)
    if _textures.has(path):
        return _textures[path]
    _fetch(path)
    return null


func _fetch(path: String) -> void:
    if _fetching.has(path) or not is_inside_tree():
        return
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    _fetching[path] = http
    http.request_completed.connect(func(result: int, code: int, _h, body: PackedByteArray):
        http.queue_free()
        _on_fetch_done(path, result, code, body))
    if http.request(Auth.api_base + path) != OK:
        _fetching.erase(path)
        http.queue_free()


func _on_fetch_done(path: String, result: int, code: int, body: PackedByteArray) -> void:
    _fetching.erase(path)
    if result != HTTPRequest.RESULT_SUCCESS or code != 200:
        push_warning("Note art download failed: %s code=%d" % [path, code])
        return
    var img := Image.new()
    if img.load_png_from_buffer(body) != OK:
        push_warning("Note art is not a PNG: %s" % path)
        return
    _textures[path] = ImageTexture.create_from_image(img)
    _apply_art()
