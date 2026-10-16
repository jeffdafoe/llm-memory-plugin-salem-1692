extends CanvasLayer
## Feed prompt (LLM-797). A player holding the feed clicks a milking animal and
## is asked "Feed Cow 2 some sage?". Yes feeds her (main.gd posts /pc/feed, after
## walking over if she is not beside the player); No closes it. Modal while
## open: the panel stops clicks, so an answer never also walks the player.
##
## The engine owns every rule (she gives milk, she is not fed already, the player
## holds the feed and stands beside her); a refusal comes back as a toast.

signal answered(animal_id: String, feed: bool)

## Room between the frame and its contents, and the buttons' least width so
## the two sit level.
const PAD_X := 24
const PAD_Y := 20
const BUTTON_MIN_WIDTH := 120.0

var _panel: PanelContainer = null
var _label: Label = null
var _animal_id := ""

func _ready() -> void:
    layer = 4
    var font: Font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")
    var size := OrientationGuard.text_size(18)

    var shade := ColorRect.new()
    shade.color = Color(0, 0, 0, 0.25)
    shade.set_anchors_preset(Control.PRESET_FULL_RECT)
    shade.mouse_filter = Control.MOUSE_FILTER_STOP
    shade.gui_input.connect(func(e: InputEvent):
        if e is InputEventMouseButton and e.pressed:
            _answer(false))
    add_child(shade)

    var center := CenterContainer.new()
    center.set_anchors_preset(Control.PRESET_FULL_RECT)
    center.mouse_filter = Control.MOUSE_FILTER_IGNORE
    add_child(center)

    _panel = PanelContainer.new()
    _panel.theme = PeriodTheme.build(font, size)
    center.add_child(_panel)

    # Room inside the frame, as the Pay box has (a touch more: this panel is
    # small and the frame sat right on the words and buttons).
    var pad := MarginContainer.new()
    for side in ["margin_left", "margin_right"]:
        pad.add_theme_constant_override(side, PAD_X)
    for side in ["margin_top", "margin_bottom"]:
        pad.add_theme_constant_override(side, PAD_Y)
    _panel.add_child(pad)

    var box := VBoxContainer.new()
    box.add_theme_constant_override("separation", 18)
    pad.add_child(box)

    _label = Label.new()
    _label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    box.add_child(_label)

    var row := HBoxContainer.new()
    row.alignment = BoxContainer.ALIGNMENT_CENTER
    row.add_theme_constant_override("separation", 16)
    box.add_child(row)

    var button_size := OrientationGuard.text_size(PeriodTheme.BUTTON_TEXT)
    var yes := PeriodTheme.button("Feed her", button_size)
    yes.custom_minimum_size.x = BUTTON_MIN_WIDTH
    PeriodTheme.make_primary(yes)
    yes.pressed.connect(func(): _answer(true))
    row.add_child(yes)
    var no := PeriodTheme.button("Not now", button_size)
    no.custom_minimum_size.x = BUTTON_MIN_WIDTH
    no.pressed.connect(func(): _answer(false))
    row.add_child(no)

    visible = false

## Ask the player whether to feed this animal.
func open(animal_id: String, animal_name: String, feed_label: String) -> void:
    _animal_id = animal_id
    _label.text = prompt_text(animal_name, feed_label)
    visible = true

func is_open() -> bool:
    return visible

func close() -> void:
    visible = false
    _animal_id = ""

## "Feed Cow 2 some sage?" — the question the prompt asks.
static func prompt_text(animal_name: String, feed_label: String) -> String:
    var who := animal_name if animal_name != "" else "the cow"
    return "Feed %s some %s?" % [who, feed_label]

func _answer(feed: bool) -> void:
    if not visible:
        return
    var id := _animal_id
    close()
    answered.emit(id, feed)
