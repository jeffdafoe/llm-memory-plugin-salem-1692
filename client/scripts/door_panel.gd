extends CanvasLayer

## The door popup (LLM-785). A click on a house the player may not walk into
## takes them to its door; arriving there, this asks whether to knock: the
## house's name, "You stand at the door.", and [Knock] / [Leave]. Knock emits
## knocked(structure_id) — main.gd plays the knock and POSTs pc/knock; whoever
## is in comes to the door (the talk panel opens) or nobody does (a private
## knock_unanswered line). Leave, Esc or a click on the backdrop just closes
## it; the player stays at the door, and a click on the house brings it back.
##
## After a knock nobody answers (LLM-19), main.gd reopens it as
## open_unanswered: "No one answers." and [Leave a note] / [Leave]. Leave a note
## emits note_requested with the people of the house a note may be for, and
## main.gd opens the note window — unless the player lacks a quill and ink or
## parchment (LLM-799), when it says so instead.
##
## Layer 4, the modal tier, styled like the repair and notice panels; main.gd
## blocks world input while it is open (opened / closed).

const FONT_PATH := "res://assets/fonts/IMFellEnglish-Regular.ttf"
const TAP_SIZE := Vector2(132, 44)
const PROMPT := "You stand at the door."
const NO_ANSWER := "No one answers."

signal opened()
signal closed()
## The player chose to knock on structure_id's door.
signal knocked(structure_id: String)
## After no answer, the player chose to leave a note (LLM-19). recipients are
## the knock response's note_for: [{id, name}].
signal note_requested(structure_id: String, title: String, recipients: Array)

var structure_id := ""

var root: Control = null
var backdrop: ColorRect = null
var sheet: PanelContainer = null
var title_label: Label = null
var prompt_label: Label = null
var knock_button: Button = null
var note_button: Button = null
var leave_button: Button = null

## The people a note may be for, while open after no answer; empty otherwise.
var _recipients: Array = []
## LLM-799: the knock response's note_lack — what the player needs to write a
## note ("" when they hold a quill and ink and parchment). Leave a note shows
## it in place of opening the note window.
var _note_lack := ""

var _font: Font = null


func _ready() -> void:
    layer = 4
    _font = load(FONT_PATH) if ResourceLoader.exists(FONT_PATH) else ThemeDB.fallback_font
    _build_ui()
    visible = false


func _build_ui() -> void:
    root = Control.new()
    root.set_anchors_preset(Control.PRESET_FULL_RECT)
    root.mouse_filter = Control.MOUSE_FILTER_STOP
    add_child(root)

    # The village stays visible around the popup, dimmed.
    backdrop = ColorRect.new()
    backdrop.color = Color(0, 0, 0, 0.4)
    backdrop.set_anchors_preset(Control.PRESET_FULL_RECT)
    backdrop.mouse_filter = Control.MOUSE_FILTER_STOP
    backdrop.gui_input.connect(_on_backdrop_input)
    root.add_child(backdrop)

    var center := CenterContainer.new()
    center.set_anchors_preset(Control.PRESET_FULL_RECT)
    center.mouse_filter = Control.MOUSE_FILTER_IGNORE
    root.add_child(center)

    sheet = PanelContainer.new()
    sheet.mouse_filter = Control.MOUSE_FILTER_STOP
    center.add_child(sheet)

    var pad := MarginContainer.new()
    for side in ["margin_left", "margin_right"]:
        pad.add_theme_constant_override(side, 18)
    pad.add_theme_constant_override("margin_top", 14)
    pad.add_theme_constant_override("margin_bottom", 14)
    sheet.add_child(pad)

    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 10)
    pad.add_child(vb)

    title_label = _label(20, Color(0.95, 0.82, 0.58))
    prompt_label = _label(16, Color(0.92, 0.84, 0.70))
    prompt_label.text = PROMPT
    vb.add_child(title_label)
    vb.add_child(prompt_label)

    var buttons := HBoxContainer.new()
    buttons.alignment = BoxContainer.ALIGNMENT_CENTER
    buttons.add_theme_constant_override("separation", 12)
    vb.add_child(buttons)
    knock_button = _button("Knock", _on_knock_pressed)
    note_button = _button("Leave a note", _on_note_pressed)
    note_button.visible = false
    leave_button = _button("Leave", close)
    buttons.add_child(knock_button)
    buttons.add_child(note_button)
    buttons.add_child(leave_button)
    _apply_theme()


func _label(size: int, color: Color) -> Label:
    var l := Label.new()
    l.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    l.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    l.add_theme_font_override("font", _font)
    l.add_theme_font_size_override("font_size", OrientationGuard.text_size(size))
    l.add_theme_color_override("font_color", color)
    l.custom_minimum_size = Vector2(280, 0)
    return l


func _button(text: String, cb: Callable) -> Button:
    var b := Button.new()
    b.text = text
    b.custom_minimum_size = TAP_SIZE
    b.add_theme_font_override("font", _font)
    b.add_theme_font_size_override("font_size", OrientationGuard.text_size(17))
    b.pressed.connect(cb)
    return b


## Whether `person` (a world container) stands at the door of `house` (a placed
## building): on its loiter pin or one of the eight tiles round it — the
## engine's own "at the door" (AtDoorOf / ActorAtObjectPin), so an arrival
## frame for some other walk cannot open the popup for this house.
static func at_door(person: Node2D, house: Node2D) -> bool:
    if person == null or house == null or not is_instance_valid(person) or not is_instance_valid(house):
        return false
    return StallScenes.at_pin(StallScenes.standing_tile(person), StallScenes.pin_tile(house))


## Ask at the door of `sid`, named `title` (the house's name). Opening again
## while open just retitles it.
func open(sid: String, title: String) -> void:
    _show(sid, title, PROMPT, [])


## After a knock nobody answered at `sid` (LLM-19): "No one answers." and, when
## the house has anyone a note may be for, Leave a note. note_lack is what the
## player needs to write one (LLM-799), "" when they hold it.
func open_unanswered(sid: String, title: String, recipients: Array, note_lack: String = "") -> void:
    _show(sid, title, NO_ANSWER, recipients, note_lack)


func _show(sid: String, title: String, prompt: String, recipients: Array, note_lack: String = "") -> void:
    structure_id = sid
    _recipients = recipients.duplicate()
    _note_lack = note_lack
    title_label.text = title
    title_label.visible = title != ""
    prompt_label.text = prompt
    var unanswered := prompt == NO_ANSWER
    knock_button.visible = not unanswered
    note_button.visible = unanswered and not _recipients.is_empty()
    if visible:
        return
    visible = true
    opened.emit()


func close() -> void:
    if not visible:
        return
    visible = false
    structure_id = ""
    _recipients = []
    _note_lack = ""
    closed.emit()


func is_open() -> bool:
    return visible


func _on_knock_pressed() -> void:
    var sid := structure_id
    close()
    if sid != "":
        knocked.emit(sid)


func _on_note_pressed() -> void:
    if _recipients.is_empty():
        return
    if _note_lack != "":
        prompt_label.text = _note_lack
        note_button.visible = false
        return
    var sid := structure_id
    var title := title_label.text
    var recipients := _recipients
    close()
    if sid != "" and not recipients.is_empty():
        note_requested.emit(sid, title, recipients)


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


# Brown period theme — the repair and notice panels', so the modals match.
func _apply_theme() -> void:
    var st := StyleBoxFlat.new()
    st.bg_color = Color(0.115, 0.085, 0.055, 0.95)
    st.border_color = Color(0.55, 0.42, 0.24, 0.95)
    st.border_width_left = 2
    st.border_width_right = 2
    st.border_width_top = 2
    st.border_width_bottom = 2
    st.corner_radius_top_left = 10
    st.corner_radius_top_right = 10
    st.corner_radius_bottom_left = 10
    st.corner_radius_bottom_right = 10
    st.shadow_color = Color(0, 0, 0, 0.45)
    st.shadow_size = 18
    st.shadow_offset = Vector2(0, 6)
    sheet.add_theme_stylebox_override("panel", st)
    for b in [knock_button, note_button, leave_button]:
        var bs := StyleBoxFlat.new()
        bs.bg_color = Color(0.30, 0.20, 0.11, 1.0)
        bs.border_color = Color(0.62, 0.47, 0.26, 1.0)
        bs.border_width_left = 1
        bs.border_width_right = 1
        bs.border_width_top = 1
        bs.border_width_bottom = 1
        bs.corner_radius_top_left = 6
        bs.corner_radius_top_right = 6
        bs.corner_radius_bottom_left = 6
        bs.corner_radius_bottom_right = 6
        var hover := bs.duplicate()
        hover.bg_color = Color(0.40, 0.27, 0.14, 1.0)
        b.add_theme_stylebox_override("normal", bs)
        b.add_theme_stylebox_override("hover", hover)
        b.add_theme_stylebox_override("pressed", hover)
        b.add_theme_stylebox_override("focus", bs)
        b.add_theme_color_override("font_color", Color(0.95, 0.86, 0.66))
