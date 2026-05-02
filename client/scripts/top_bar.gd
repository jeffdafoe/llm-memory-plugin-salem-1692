extends PanelContainer
## Top bar — title on the left, Edit/Logout buttons on the right.
## Built programmatically to match the dark brown/gold theme.

signal edit_toggled(active: bool)
signal config_pressed
signal logout_pressed

var edit_button: Button = null
var config_button: Button = null
var logout_button: Button = null
var username_label: Label = null
## Cursor tile readout — only visible in edit mode. Shows the tile the
## mouse is hovering over so admins can place things at specific
## coordinates and interpret list-view "at X,Y" fallbacks.
var cursor_tile_label: Label = null
## Coin chip — displays the player's purse with a tooltip listing
## inventory. Hidden until /pc/me reports an existing PC; talk panel
## calls set_purse() each time it polls.
var coins_label: Label = null
var _editor_active: bool = false

# Theme colors (matching login screen)
const COLOR_BG = Color(0.12, 0.09, 0.07, 0.95)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_BTN_BG = Color(0.35, 0.25, 0.12, 1.0)
const COLOR_BTN_BORDER = Color(0.55, 0.42, 0.25, 1.0)
const COLOR_BTN_ACTIVE_BG = Color(0.29, 0.29, 0.19, 1.0)
const COLOR_BTN_ACTIVE_BORDER = Color(0.54, 0.48, 0.31, 1.0)

var _font: Font = null

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    # Style the panel container
    var panel_style = StyleBoxFlat.new()
    panel_style.bg_color = COLOR_BG
    panel_style.border_width_bottom = 1
    panel_style.border_color = COLOR_BORDER
    panel_style.content_margin_left = 12.0
    panel_style.content_margin_right = 12.0
    panel_style.content_margin_top = 0.0
    panel_style.content_margin_bottom = 0.0
    add_theme_stylebox_override("panel", panel_style)

    # Size: full width, 40px tall, anchored to top
    custom_minimum_size = Vector2(0, 40)
    anchor_left = 0.0
    anchor_right = 1.0
    anchor_top = 0.0
    anchor_bottom = 0.0
    offset_bottom = 40

    # HBox: title on left, buttons on right
    var hbox = HBoxContainer.new()
    hbox.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    add_child(hbox)

    # Title
    var title = Label.new()
    title.text = "Salem \u2014 1692"
    title.add_theme_color_override("font_color", COLOR_TEXT)
    title.add_theme_font_override("font", _font)
    title.add_theme_font_size_override("font_size", 22)
    title.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    title.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    hbox.add_child(title)

    # Right side container
    var right_box = HBoxContainer.new()
    right_box.add_theme_constant_override("separation", 8)
    right_box.alignment = BoxContainer.ALIGNMENT_END
    hbox.add_child(right_box)

    # Cursor tile readout. Placed before the username so it reads
    # "Tile: 42, 17   jeff   Edit  Config  Logout" from left to right.
    # Hidden outside edit mode — this is admin-only information.
    cursor_tile_label = Label.new()
    cursor_tile_label.text = ""
    cursor_tile_label.visible = false
    cursor_tile_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    cursor_tile_label.add_theme_font_override("font", _font)
    cursor_tile_label.add_theme_font_size_override("font_size", 14)
    cursor_tile_label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    right_box.add_child(cursor_tile_label)

    # Coins chip — period-flavored "P 25" (silver pence). Hidden until
    # the talk panel reports the player has a PC with coins. Tooltip
    # spelled out via mouse hover handler since Label.tooltip_text only
    # renders on a delay; for now the label-only readout is enough.
    coins_label = Label.new()
    coins_label.text = ""
    coins_label.visible = false
    coins_label.add_theme_color_override("font_color", Color(0.92, 0.78, 0.42, 1.0))
    coins_label.add_theme_font_override("font", _font)
    coins_label.add_theme_font_size_override("font_size", 16)
    coins_label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    coins_label.mouse_filter = Control.MOUSE_FILTER_PASS
    right_box.add_child(coins_label)

    # Username label
    username_label = Label.new()
    username_label.text = ""
    username_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    username_label.add_theme_font_override("font", _font)
    username_label.add_theme_font_size_override("font_size", 16)
    username_label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    right_box.add_child(username_label)

    # Edit button (hidden until auth confirms can_edit)
    edit_button = _make_button("Edit")
    edit_button.visible = false
    edit_button.pressed.connect(_on_edit_pressed)
    right_box.add_child(edit_button)

    # Config button — hidden until auth confirms can_edit, since the panel now
    # contains admin-only world controls instead of the old asset reference.
    config_button = _make_button("Config")
    config_button.visible = false
    config_button.pressed.connect(_on_config_pressed)
    right_box.add_child(config_button)

    # Logout button
    logout_button = _make_button("Logout")
    logout_button.pressed.connect(_on_logout_pressed)
    right_box.add_child(logout_button)

func _make_button(label: String) -> Button:
    var btn = Button.new()
    btn.text = label
    btn.flat = false
    btn.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    btn.add_theme_color_override("font_hover_color", COLOR_TEXT)
    btn.add_theme_font_override("font", _font)
    btn.add_theme_font_size_override("font_size", 16)

    var normal_style = StyleBoxFlat.new()
    normal_style.bg_color = Color(0.17, 0.14, 0.10, 1.0)
    normal_style.border_width_left = 1
    normal_style.border_width_top = 1
    normal_style.border_width_right = 1
    normal_style.border_width_bottom = 1
    normal_style.border_color = Color(0.35, 0.28, 0.18, 1.0)
    normal_style.corner_radius_left_top = 3
    normal_style.corner_radius_right_top = 3
    normal_style.corner_radius_left_bottom = 3
    normal_style.corner_radius_right_bottom = 3
    normal_style.content_margin_left = 12.0
    normal_style.content_margin_right = 12.0
    normal_style.content_margin_top = 4.0
    normal_style.content_margin_bottom = 4.0
    btn.add_theme_stylebox_override("normal", normal_style)

    var hover_style = normal_style.duplicate()
    hover_style.bg_color = COLOR_BTN_BG
    hover_style.border_color = COLOR_BTN_BORDER
    btn.add_theme_stylebox_override("hover", hover_style)

    var pressed_style = normal_style.duplicate()
    pressed_style.bg_color = Color(0.25, 0.18, 0.08, 1.0)
    btn.add_theme_stylebox_override("pressed", pressed_style)

    return btn

func set_username(name: String) -> void:
    username_label.text = name

## Update the coins chip + inventory tooltip. Hidden when the player has
## no PC yet (called with -1) or with an empty inventory + zero coins.
## inventory_lines is a list of "Item x N" strings already formatted by
## the caller — top bar doesn't know item display labels.
func set_purse(coins: int, inventory_lines: Array) -> void:
    if coins_label == null:
        return
    if coins < 0:
        coins_label.visible = false
        return
    coins_label.text = "%d c" % coins
    coins_label.visible = true
    if inventory_lines.is_empty():
        coins_label.tooltip_text = "Your purse: %d coins.\nNothing in your pack." % coins
    else:
        coins_label.tooltip_text = "Your purse: %d coins.\n\nIn your pack:\n  %s" % [coins, "\n  ".join(inventory_lines)]

## Update the cursor tile readout. Called from main.gd when the editor
## emits a cursor_tile_changed signal.
func set_cursor_tile(x: int, y: int) -> void:
    if cursor_tile_label == null:
        return
    cursor_tile_label.text = "Tile: %d, %d" % [x, y]

## Show or hide the cursor tile readout. Called when edit mode is toggled
## and when the mouse leaves the map area.
func set_cursor_tile_visible(show: bool) -> void:
    if cursor_tile_label == null:
        return
    cursor_tile_label.visible = show
    if not show:
        cursor_tile_label.text = ""

func set_edit_visible(show: bool) -> void:
    edit_button.visible = show

func set_config_visible(show: bool) -> void:
    config_button.visible = show

func _on_edit_pressed() -> void:
    _editor_active = not _editor_active
    _update_edit_button_style()
    edit_toggled.emit(_editor_active)

func _update_edit_button_style() -> void:
    if _editor_active:
        var active_style = StyleBoxFlat.new()
        active_style.bg_color = COLOR_BTN_ACTIVE_BG
        active_style.border_width_left = 1
        active_style.border_width_top = 1
        active_style.border_width_right = 1
        active_style.border_width_bottom = 1
        active_style.border_color = COLOR_BTN_ACTIVE_BORDER
        active_style.corner_radius_left_top = 3
        active_style.corner_radius_right_top = 3
        active_style.corner_radius_left_bottom = 3
        active_style.corner_radius_right_bottom = 3
        active_style.content_margin_left = 12.0
        active_style.content_margin_right = 12.0
        active_style.content_margin_top = 4.0
        active_style.content_margin_bottom = 4.0
        edit_button.add_theme_stylebox_override("normal", active_style)
        edit_button.add_theme_color_override("font_color", Color(0.78, 0.72, 0.48, 1.0))
    else:
        # Reset to default style (must match _make_button's normal style)
        var normal_style = StyleBoxFlat.new()
        normal_style.bg_color = Color(0.17, 0.14, 0.10, 1.0)
        normal_style.border_width_left = 1
        normal_style.border_width_top = 1
        normal_style.border_width_right = 1
        normal_style.border_width_bottom = 1
        normal_style.border_color = Color(0.35, 0.28, 0.18, 1.0)
        normal_style.corner_radius_left_top = 3
        normal_style.corner_radius_right_top = 3
        normal_style.corner_radius_left_bottom = 3
        normal_style.corner_radius_right_bottom = 3
        normal_style.content_margin_left = 12.0
        normal_style.content_margin_right = 12.0
        normal_style.content_margin_top = 4.0
        normal_style.content_margin_bottom = 4.0
        edit_button.add_theme_stylebox_override("normal", normal_style)
        edit_button.add_theme_color_override("font_color", COLOR_TEXT_DIM)

## Force the edit button to a specific state (called externally when edit
## mode is closed by something other than the button, e.g., ESC key).
func set_edit_active(active: bool) -> void:
    _editor_active = active
    _update_edit_button_style()

func _on_config_pressed() -> void:
    config_pressed.emit()

func _on_logout_pressed() -> void:
    _editor_active = false
    _update_edit_button_style()
    logout_pressed.emit()
