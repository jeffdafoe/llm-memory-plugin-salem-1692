extends PanelContainer
## Top bar — title on the left, Edit/Logout buttons on the right.
## Built programmatically to match the dark brown/gold theme.

signal edit_toggled(active: bool)
signal config_pressed
signal logout_pressed
## Inventory icon toggled — payload is the icon's global rect so the
## inventory panel can anchor itself relative to it. main.gd forwards
## to the inventory panel's show_at() / close().
signal inventory_toggle_requested(icon_rect: Rect2)

var edit_button: Button = null
var config_button: Button = null
var logout_button: Button = null
var username_label: Label = null
## Backing fields for the username label so login_username and
## character_name can layer cleanly. set_username sets the login
## fallback; set_character_name overrides while a PC exists. Clearing
## the character name (PC stops existing, /pc/me returns exists=false)
## reverts to the login.
var _login_username: String = ""
var _character_name: String = ""
## Inventory icon — clickable Lucide glyph between coin chip and
## username. Hidden until /pc/me reports an existing PC; toggles a
## floating panel of the player's pack.
var inventory_icon: Label = null
## Cursor tile readout — only visible in edit mode. Shows the tile the
## mouse is hovering over so admins can place things at specific
## coordinates and interpret list-view "at X,Y" fallbacks.
var cursor_tile_label: Label = null
## Coin chip — displays the player's purse with a tooltip listing
## inventory. Hidden until /pc/me reports an existing PC; talk panel
## calls set_purse() each time it polls.
var coins_label: Label = null
## Needs chip — displays the PC's hunger / thirst / tiredness as a
## spelled-out "Hunger 8  Thirst 12  Weariness 4" readout where each
## word's leading letter is rendered larger than the rest. Each segment
## is tier-tinted by its own value (so a fine W stays dim even when H
## peaks). Hidden until /pc/me reports an existing PC. Updated alongside
## the coin chip via the talk panel's needs_changed signal (ZBBS-123).
##
## Implemented as a horizontal container whose children are rebuilt on
## every set_needs() call. Avoids RichTextLabel because its fit_content
## sizing inside an HBoxContainer interacts poorly with the surrounding
## layout when the chip starts hidden then becomes visible later.
var needs_label: HBoxContainer = null
var _editor_active: bool = false

# Persistent-segment state for the needs HUD (ZBBS-HOME-215). Pre-215
# set_needs rebuilt the chip's children every poll, killing in-flight
# tweens and snapping numbers to their new value with no transition.
# Now segments are built once in _build_needs_chip and updated in
# place — value changes "gas-pump" tween from old to new, recoveries
# (new < old) flash a warm brightening on the segment that fades back
# over ~1.6s. Polling cadence is 10s (talk_panel REFRESH_INTERVAL),
# well outside the longest animation, so consecutive polls don't
# overlap their effects.
const _NEED_KEYS: Array = ["hunger", "thirst", "tiredness"]
const _NEED_DISPLAY: Dictionary = {
    "hunger":    ["H", "unger "],
    "thirst":    ["T", "hirst "],
    "tiredness": ["W", "eariness "],
}
const RECOVERY_FLASH_COLOR := Color(1.35, 1.25, 1.05, 1.0)
const VALUE_TWEEN_DURATION: float = 0.6

# ZBBS-HOME-216: pulse window. Each time a need decreases the segment
# enters a "recovering" state for RECOVERING_WINDOW_MS milliseconds
# during which container.modulate oscillates between white and
# RECOVERY_FLASH_COLOR via a sin wave driven from _process. A fresh
# decrease (next dwell tick or consume) refreshes the window. After
# the window expires with no new decrease, the pulse eases back to
# white over RECOVERING_FADE_DURATION and stops.
#
# 15 min covers the gap between dwell ticks (10 min) plus a 5 min
# grace period for jitter and slow networks. Anyone actively
# dwelling sees a continuous pulse; anyone who walked away sees
# their pulse fade out within ~15 min and the segment goes still.
const RECOVERING_PULSE_PERIOD: float = 1.8        # full sin cycle, seconds
const RECOVERING_WINDOW_MS: int = 15 * 60 * 1000  # 15 minutes
const RECOVERING_FADE_DURATION: float = 1.0       # post-window settle, seconds

# Per-need segment record: container + the three labels + the
# in-flight value tween + the currently-rendered integer value (used
# as the start point for the next gas-pump tween, since the label
# text is already showing it).
var _need_segments: Dictionary = {}
# Last value seen per need. -1 sentinel = "no value yet" — first set
# snaps without animation so the chip shows the right number on the
# first /pc/me response without a 0→24 roll-up.
var _prior_needs: Dictionary = {"hunger": -1, "thirst": -1, "tiredness": -1}
# Per-need pulse-window expiry timestamp (Time.get_ticks_msec()
# value, NOT a Unix timestamp). 0 = not currently in a pulse window.
var _recovering_until: Dictionary = {"hunger": 0, "thirst": 0, "tiredness": 0}

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
var _icon_font: Font = null

# Lucide glyph for "package" — a tied bundle that reads as period-
# neutral well enough for a 1692 setting (vs. shopping-bag/backpack
# which feel modern). See `notes/codebase/salem/icon-fonts` for the
# loading + materialization pattern.
const ICON_CODEPOINT_PACKAGE: int = 0xE129

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")
    _icon_font = load("res://assets/fonts/lucide.ttf")

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

    # Body-needs chip (ZBBS-123). Spells out "Hunger 24  Thirst 24
    # Weariness 0" with the leading letter at font_size 18 and the rest
    # of each word + value at font_size 10. Each need is tier-tinted by
    # its own value (default → dim, mild → amber, red → orange, peak →
    # bright red), so a fine W stays dim even when H peaks. Sits before
    # the coin chip so personal stats (body, then purse) read together
    # left-to-right. Built as nested HBoxContainers so each segment can
    # combine two Label children of different sizes; the chip's children
    # are rebuilt by set_needs() on each update. Hidden until set_needs
    # is called with non-empty data.
    needs_label = HBoxContainer.new()
    needs_label.add_theme_constant_override("separation", 12)
    needs_label.visible = false
    needs_label.mouse_filter = Control.MOUSE_FILTER_PASS
    right_box.add_child(needs_label)

    # ZBBS-HOME-215: build the per-need segments once, here, so set_needs
    # can update them in place (preserving in-flight tweens). The chip
    # stays hidden until set_needs receives a non-empty dictionary, but
    # the segment children sit ready under it.
    _build_needs_segments()

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

    # Inventory icon — Lucide "package" glyph, clickable Label rather
    # than a Button so it sits flush in the bar (Buttons drag in their
    # own border / padding / focus rect; Labels look like part of the
    # text flow). Hidden until set_inventory() reports a non-empty
    # state; visibility tracks the coin chip.
    inventory_icon = Label.new()
    inventory_icon.text = String.chr(ICON_CODEPOINT_PACKAGE)
    inventory_icon.add_theme_font_override("font", _icon_font)
    inventory_icon.add_theme_font_size_override("font_size", 18)
    inventory_icon.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    inventory_icon.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    inventory_icon.visible = false
    inventory_icon.tooltip_text = "Your pack"
    inventory_icon.mouse_filter = Control.MOUSE_FILTER_STOP
    inventory_icon.mouse_default_cursor_shape = Control.CURSOR_POINTING_HAND
    inventory_icon.gui_input.connect(_on_inventory_icon_input)
    inventory_icon.mouse_entered.connect(func(): inventory_icon.add_theme_color_override("font_color", COLOR_TEXT))
    inventory_icon.mouse_exited.connect(func(): inventory_icon.add_theme_color_override("font_color", COLOR_TEXT_DIM))
    right_box.add_child(inventory_icon)

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

## Set the login username — the fallback label shown before /pc/me lands
## or when the player has no PC yet. set_character_name overrides this
## while a PC exists; clearing the character name (empty string) reverts
## to whatever was stashed here.
func set_username(name: String) -> void:
    _login_username = name
    _refresh_name_label()


## Set the in-world character name for the active PC. Empty string
## clears the override and reverts to the login username — used when
## /pc/me reports the PC no longer exists (deletion, mid-session
## logout-elsewhere). Called by main.gd whenever the talk panel emits
## character_name_changed from a fresh /pc/me snapshot.
func set_character_name(name: String) -> void:
    _character_name = name
    _refresh_name_label()


func _refresh_name_label() -> void:
    if username_label == null:
        return
    if _character_name != "":
        username_label.text = _character_name
        username_label.add_theme_color_override("font_color", COLOR_TEXT)
    else:
        username_label.text = _login_username
        username_label.add_theme_color_override("font_color", COLOR_TEXT_DIM)

## Update the coins chip. Hidden when the player has no PC yet (called
## with -1). Inventory rendering moved to the dedicated panel post the
## pack-icon ship; this method now only owns the coin chip itself, and
## inventory_lines is preserved as a parameter for backward compat with
## the existing main.gd wiring (ignored here).
func set_purse(coins: int, _inventory_lines: Array) -> void:
    if coins_label == null:
        return
    if coins < 0:
        coins_label.visible = false
        if inventory_icon != null:
            inventory_icon.visible = false
        return
    coins_label.text = "%d c" % coins
    coins_label.tooltip_text = "Your purse: %d coins." % coins
    coins_label.visible = true
    if inventory_icon != null:
        inventory_icon.visible = true


## gui_input handler on the inventory icon. Treat any left-click as a
## toggle request and emit upward; main.gd routes to the inventory
## panel's show_at() / close() based on its current visibility. The
## global rect of the icon goes along so the panel can anchor itself.
func _on_inventory_icon_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        inventory_toggle_requested.emit(inventory_icon.get_global_rect())
        get_viewport().set_input_as_handled()

## Update the body-needs chip (ZBBS-123, animated ZBBS-HOME-215).
## `needs` is a Dictionary keyed by 'hunger' / 'thirst' / 'tiredness'
## with int values 0..24. Empty dictionary hides the chip.
##
## Animation behavior:
##   - First non-empty call snaps each value into place (no tween),
##     so the chip shows the right number on the first /pc/me response
##     without a 0→24 roll-up.
##   - Subsequent calls tween the displayed integer from the prior
##     value to the new one over VALUE_TWEEN_DURATION ("gas-pump"
##     style — the digits roll instead of snap).
##   - When a need decreases (recovery, e.g. dwelling at a Shade Tree
##     pulls tiredness down), the segment flashes a warm brightening
##     and fades back over RECOVERY_FLASH_DURATION. Player gets a
##     visible signal that resting is doing something.
##
## Tier thresholds are hardcoded to engine defaults (mild ≥ 8, red ≥
## 18, peak = 24). If an admin tunes the server thresholds the HUD
## coloring drifts slightly from the in-prompt felt language —
## acceptable for v1; a future refresh can wire thresholds into
## /pc/me alongside the values.
func set_needs(needs: Dictionary) -> void:
    if needs_label == null:
        return
    if needs.is_empty():
        needs_label.visible = false
        return
    needs_label.visible = true
    var h := int(needs.get("hunger", 0))
    var t := int(needs.get("thirst", 0))
    var w := int(needs.get("tiredness", 0))
    needs_label.tooltip_text = "Hunger: %d / 24\nThirst: %d / 24\nTiredness: %d / 24" % [h, t, w]
    _update_need_segment("hunger", h)
    _update_need_segment("thirst", t)
    _update_need_segment("tiredness", w)

## Build the persistent per-need segments under needs_label. Called
## once from _ready. Each segment is a horizontal triplet —
## cap (16pt) + lowercase rest (10pt) + value (16pt) — kept alive
## across set_needs calls so tweens land on stable nodes.
func _build_needs_segments() -> void:
    for key in _NEED_KEYS:
        var meta: Array = _NEED_DISPLAY[key]
        var segment := HBoxContainer.new()
        segment.add_theme_constant_override("separation", 0)
        var cap_label := _make_need_label(meta[0], 16, COLOR_TEXT_DIM)
        var rest_label := _make_need_label(meta[1], 10, COLOR_TEXT_DIM)
        var value_label := _make_need_label("0", 16, COLOR_TEXT_DIM)
        segment.add_child(cap_label)
        segment.add_child(rest_label)
        segment.add_child(value_label)
        needs_label.add_child(segment)
        _need_segments[key] = {
            "container":      segment,
            "cap_label":       cap_label,
            "rest_label":      rest_label,
            "value_label":     value_label,
            "value_tween":     null,
            "displayed_value": 0,
        }

## Apply a need value update to a single segment. Color always
## refreshes (so a tier crossover paints immediately). The displayed
## number tweens from the segment's currently-rendered value to the
## new one — using `displayed_value` instead of `_prior_needs[key]`
## so a poll arriving mid-tween picks up where the last animation
## was, no jump. Recovery (new < old) triggers the warm flash on the
## container's modulate.
func _update_need_segment(key: String, new_val: int) -> void:
    var seg = _need_segments.get(key)
    if seg == null:
        return
    var color := _tier_color(new_val)
    seg.cap_label.add_theme_color_override("font_color", color)
    seg.rest_label.add_theme_color_override("font_color", color)
    seg.value_label.add_theme_color_override("font_color", color)

    var old_val: int = _prior_needs.get(key, -1)
    _prior_needs[key] = new_val

    # First-time set: snap. Avoids the visually-jarring 0→N rollup
    # when the chip first appears.
    if old_val < 0:
        seg.value_label.text = "%d" % new_val
        seg.displayed_value = new_val
        return

    if new_val == old_val:
        return  # No change — no tween, no flash.

    # Gas-pump: animate the displayed integer from where it currently
    # reads (displayed_value, may be mid-tween) to the new value.
    if seg.value_tween != null and seg.value_tween.is_valid():
        seg.value_tween.kill()
    var start_val: float = float(seg.displayed_value)
    seg.value_tween = create_tween()
    seg.value_tween.tween_method(
        Callable(self, "_set_segment_displayed_value").bind(key),
        start_val,
        float(new_val),
        VALUE_TWEEN_DURATION
    )

    # ZBBS-HOME-216: continuous pulse on recovery. Each decrease
    # refreshes the pulse window; _process drives the actual modulate
    # oscillation. Pre-216 was a one-shot 1.6s flash on each
    # decrease which the player couldn't see between 10-minute dwell
    # ticks. Now the segment glows continuously while dwelling and
    # fades after walking away.
    if new_val < old_val:
        _recovering_until[key] = Time.get_ticks_msec() + RECOVERING_WINDOW_MS

## Tween callback — drives the gas-pump value display. Rounds the
## tweened float to the nearest int so the digit reads cleanly.
## Bound to the per-segment key so a single function serves all three.
func _set_segment_displayed_value(key: String, v: float) -> void:
    var seg = _need_segments.get(key)
    if seg == null:
        return
    var rounded: int = int(round(v))
    if rounded != seg.displayed_value:
        seg.value_label.text = "%d" % rounded
        seg.displayed_value = rounded

## Construct a single Label inside a need segment with the given size
## and color. Vertically centered so labels of different sizes line up
## around the middle of the bar — matches the coin chip's alignment.
func _make_need_label(text: String, size: int, color: Color) -> Label:
    var label := Label.new()
    label.text = text
    label.add_theme_color_override("font_color", color)
    label.add_theme_font_override("font", _font)
    label.add_theme_font_size_override("font_size", size)
    label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    return label

## Tier color for a single need value. Mirrors the engine's mild/red/peak
## thresholds (8 / 18 / 24). Falls back to the dim chrome text color
## when the need is below the mild threshold so the segment recedes.
func _tier_color(value: int) -> Color:
    if value >= 24:
        return Color(0.95, 0.35, 0.30, 1.0)
    if value >= 18:
        return Color(0.90, 0.55, 0.25, 1.0)
    if value >= 8:
        return Color(0.88, 0.74, 0.35, 1.0)
    return COLOR_TEXT_DIM


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

## ZBBS-HOME-216: drive the per-need recovery pulse. Each segment
## that's within its RECOVERING_WINDOW_MS gets its container
## modulate set to a sin-wave oscillation between Color(1,1,1,1)
## and RECOVERY_FLASH_COLOR. Outside the window, the modulate
## eases back to white over RECOVERING_FADE_DURATION and stays
## there until the next decrease re-arms.
##
## Cheap: three segments × one sin call + a lerp per frame. Runs
## even when the chip is hidden (early-out below) so a poll that
## arrives during a fade settles cleanly instead of getting frozen
## mid-pulse.
func _process(delta: float) -> void:
    if needs_label == null or not needs_label.visible:
        return
    var now_ms: int = Time.get_ticks_msec()
    var seconds: float = float(now_ms) / 1000.0
    for key in _NEED_KEYS:
        var seg = _need_segments.get(key)
        if seg == null:
            continue
        var until_ms: int = _recovering_until.get(key, 0)
        if now_ms < until_ms:
            # In window — sin wave 0..1, lerp white → flash color.
            var t: float = sin(seconds * TAU / RECOVERING_PULSE_PERIOD) * 0.5 + 0.5
            seg.container.modulate = Color(1, 1, 1, 1).lerp(RECOVERY_FLASH_COLOR, t)
        elif seg.container.modulate != Color(1, 1, 1, 1):
            # Window expired — settle modulate back to white.
            var step: float = delta / RECOVERING_FADE_DURATION
            seg.container.modulate = seg.container.modulate.lerp(
                Color(1, 1, 1, 1), clamp(step, 0.0, 1.0)
            )
