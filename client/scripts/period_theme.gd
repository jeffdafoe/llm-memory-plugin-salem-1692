class_name PeriodTheme
## The brown period look shared by the Pay box and the character creator
## (LLM-728): framed brown buttons and inputs, IM Fell text, a filled main
## button, and the finger-sized tap height. Moved out of pay_panel.gd so the
## two panels cannot drift apart.

const TAP_H := 44.0
## Button text size before OrientationGuard scaling.
const BUTTON_TEXT := 17

const COLOR_TEXT := Color(0.92, 0.84, 0.70)
const COLOR_BORDER := Color(0.55, 0.42, 0.24, 0.95)

static func build(font: Font, font_size: int) -> Theme:
    var theme := Theme.new()
    theme.default_font = font
    theme.default_font_size = font_size

    var st := StyleBoxFlat.new()
    st.bg_color = Color(0.115, 0.085, 0.055, 0.97)
    st.border_color = COLOR_BORDER
    st.set_border_width_all(2)
    st.set_corner_radius_all(10)
    st.shadow_color = Color(0, 0, 0, 0.45)
    st.shadow_size = 18
    st.shadow_offset = Vector2(0, 6)
    theme.set_stylebox("panel", "PanelContainer", st)

    var normal := box(Color(0.20, 0.14, 0.08), Color(0.42, 0.32, 0.19))
    var hover := box(Color(0.26, 0.19, 0.11), Color(0.62, 0.47, 0.26))
    var disabled := box(Color(0.16, 0.12, 0.08, 0.6), Color(0.42, 0.32, 0.19, 0.6))
    theme.set_stylebox("normal", "Button", normal)
    theme.set_stylebox("hover", "Button", hover)
    theme.set_stylebox("pressed", "Button", hover)
    theme.set_stylebox("disabled", "Button", disabled)
    theme.set_stylebox("focus", "Button", StyleBoxEmpty.new())
    theme.set_color("font_color", "Button", COLOR_TEXT)
    theme.set_color("font_hover_color", "Button", COLOR_TEXT)
    theme.set_color("font_pressed_color", "Button", COLOR_TEXT)
    theme.set_color("font_disabled_color", "Button", Color(0.62, 0.54, 0.42))

    theme.set_stylebox("normal", "LineEdit", normal)
    theme.set_stylebox("focus", "LineEdit", box(Color(0.20, 0.14, 0.08), Color(0.78, 0.62, 0.34)))
    theme.set_stylebox("read_only", "LineEdit", normal)
    theme.set_color("font_color", "LineEdit", COLOR_TEXT)
    theme.set_color("font_uneditable_color", "LineEdit", COLOR_TEXT)
    theme.set_color("caret_color", "LineEdit", COLOR_TEXT)
    theme.set_color("selection_color", "LineEdit", Color(0.55, 0.42, 0.25, 0.55))
    theme.set_color("font_color", "Label", COLOR_TEXT)
    return theme

## A button at the tap height every period panel uses. Sizes come in already
## scaled (OrientationGuard.text_size): a static func cannot reach an autoload.
static func button(text: String, font_size: int) -> Button:
    var b := Button.new()
    b.text = text
    b.custom_minimum_size = Vector2(0, TAP_H)
    b.focus_mode = Control.FOCUS_NONE
    b.add_theme_font_size_override("font_size", font_size)
    return b

## The filled main-button look (Buy, Save, Make an offer). A disabled button
## keeps the theme's faded box.
static func make_primary(b: Button) -> void:
    var normal := box(Color(0.48, 0.33, 0.15), Color(0.78, 0.60, 0.33))
    var hover := box(Color(0.58, 0.40, 0.18), Color(0.85, 0.68, 0.38))
    b.add_theme_stylebox_override("normal", normal)
    b.add_theme_stylebox_override("hover", hover)
    b.add_theme_stylebox_override("pressed", hover)
    b.add_theme_color_override("font_color", Color(1.0, 0.95, 0.85))
    b.add_theme_color_override("font_hover_color", Color(1.0, 0.95, 0.85))

static func box(fill: Color, stroke: Color) -> StyleBoxFlat:
    var sb := StyleBoxFlat.new()
    sb.bg_color = fill
    sb.border_color = stroke
    sb.set_border_width_all(1)
    sb.set_corner_radius_all(6)
    sb.content_margin_left = 12
    sb.content_margin_right = 12
    sb.content_margin_top = 4
    sb.content_margin_bottom = 4
    return sb
