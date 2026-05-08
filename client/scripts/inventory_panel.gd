extends Control
## Inventory popover — small panel anchored under the top-bar's pack icon.
##
## Lifecycle: lives on its own CanvasLayer (mounted in main.gd) above the
## talk panel so a player can open it mid-conversation. The top bar emits
## a signal with the icon's global rect; main.gd forwards to show_at().
## Click-outside / Esc / icon re-click closes.
##
## Data shape: items is the raw pcInventoryEntry array from /pc/me — a
## list of dicts with display_label / quantity / category / capabilities.
## The panel renders flat-list (sorted by category then label) since v1
## inventories are short; categories are visible via the existing
## ordering rather than explicit headers.

signal closed

const COLOR_BG = Color(0.05, 0.03, 0.02, 0.7)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_QTY = Color(0.92, 0.78, 0.42, 1.0)  # gold, matches coin chip

const PANEL_WIDTH: float = 240.0
const PANEL_PAD: float = 16.0
const ROW_SEPARATION: int = 6

var _font: Font = null
var _panel: PanelContainer = null
var _content: VBoxContainer = null
var _items: Array = []
var _icon_rect: Rect2 = Rect2()  # icon's global rect, for outside-click detection

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    # Full-screen control so we can capture outside-clicks. The dim
    # background is intentionally transparent (no overlay) — this is a
    # popover, not a modal. The world keeps rendering at full opacity
    # behind. Mouse filter PASS so the world still receives non-popover
    # clicks; we handle close-on-outside in _input instead.
    anchors_preset = Control.PRESET_FULL_RECT
    anchor_right = 1.0
    anchor_bottom = 1.0
    mouse_filter = Control.MOUSE_FILTER_IGNORE

    _panel = PanelContainer.new()
    _panel.custom_minimum_size = Vector2(PANEL_WIDTH, 0)
    _panel.mouse_filter = Control.MOUSE_FILTER_STOP

    var panel_style = StyleBoxFlat.new()
    panel_style.bg_color = COLOR_PANEL_BG
    panel_style.border_width_left = 1
    panel_style.border_width_top = 1
    panel_style.border_width_right = 1
    panel_style.border_width_bottom = 1
    panel_style.border_color = COLOR_BORDER
    panel_style.corner_radius_left_top = 4
    panel_style.corner_radius_right_top = 4
    panel_style.corner_radius_left_bottom = 4
    panel_style.corner_radius_right_bottom = 4
    panel_style.content_margin_left = PANEL_PAD
    panel_style.content_margin_right = PANEL_PAD
    panel_style.content_margin_top = PANEL_PAD - 4
    panel_style.content_margin_bottom = PANEL_PAD - 4
    # A subtle drop shadow so the popover reads as floating above the
    # bar rather than fused with it. Godot's StyleBoxFlat shadow_size
    # gives a soft glow; we color-match the BG for a darkening cast.
    panel_style.shadow_color = Color(0, 0, 0, 0.55)
    panel_style.shadow_size = 8
    panel_style.shadow_offset = Vector2(0, 4)
    _panel.add_theme_stylebox_override("panel", panel_style)
    add_child(_panel)

    _content = VBoxContainer.new()
    _content.add_theme_constant_override("separation", ROW_SEPARATION)
    _panel.add_child(_content)

    visible = false


## Show the panel anchored under the given screen-space rect (the
## inventory icon's global rect). Right edge of the panel aligns with
## the right edge of the icon, top edge sits a few pixels below the
## icon's bottom — so the popover hangs from the icon like a tag.
func show_at(icon_rect: Rect2) -> void:
    _icon_rect = icon_rect
    _rebuild()
    visible = true
    _reposition()


## Update the inventory data. If the panel is open, it re-renders in
## place; if closed, the next show_at() will pick up the latest.
func set_inventory(items: Array) -> void:
    _items = _sorted_items(items)
    if visible:
        _rebuild()
        _reposition()


func close() -> void:
    if not visible:
        return
    visible = false
    closed.emit()


## Sort items by category, then alphabetically by display_label, so the
## render order is stable across polls. Capabilities and item_kind pass
## through unchanged.
func _sorted_items(items: Array) -> Array:
    var copy: Array = []
    for entry in items:
        if typeof(entry) == TYPE_DICTIONARY:
            copy.append(entry)
    copy.sort_custom(func(a, b):
        var ca: String = str(a.get("category", ""))
        var cb: String = str(b.get("category", ""))
        if ca != cb:
            return ca < cb
        var la: String = str(a.get("display_label", a.get("item_kind", "")))
        var lb: String = str(b.get("display_label", b.get("item_kind", "")))
        return la.naturalnocasecmp_to(lb) < 0
    )
    return copy


func _rebuild() -> void:
    for child in _content.get_children():
        child.queue_free()

    var header := Label.new()
    header.text = "Your Pack"
    header.add_theme_color_override("font_color", COLOR_TEXT)
    header.add_theme_font_override("font", _font)
    header.add_theme_font_size_override("font_size", 18)
    header.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    _content.add_child(header)

    # Hairline divider under the header. ColorRect with custom min height
    # reads as a thin gold rule; matches the bar's bottom border weight.
    var rule := ColorRect.new()
    rule.color = COLOR_BORDER
    rule.custom_minimum_size = Vector2(0, 1)
    rule.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    var rule_margin := MarginContainer.new()
    rule_margin.add_theme_constant_override("margin_top", 2)
    rule_margin.add_theme_constant_override("margin_bottom", 6)
    rule_margin.add_child(rule)
    _content.add_child(rule_margin)

    if _items.is_empty():
        var empty := Label.new()
        empty.text = "Your pack is empty."
        empty.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        empty.add_theme_font_override("font", _font)
        empty.add_theme_font_size_override("font_size", 14)
        empty.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
        _content.add_child(empty)
        return

    for entry in _items:
        var label_text := str(entry.get("display_label", entry.get("item_kind", "")))
        var qty := int(entry.get("quantity", 0))
        if label_text == "" or qty <= 0:
            continue
        _content.add_child(_build_row(label_text, qty))


## Each row: display label flush-left, quantity flush-right in the
## coin-chip gold so the eye groups them with the purse below the
## title. Multiplication sign is the Latin-1 × (U+00D7), supported by
## IMFellEnglish.
func _build_row(label_text: String, qty: int) -> HBoxContainer:
    var row := HBoxContainer.new()
    row.add_theme_constant_override("separation", 12)

    var name_label := Label.new()
    name_label.text = label_text
    name_label.add_theme_color_override("font_color", COLOR_TEXT)
    name_label.add_theme_font_override("font", _font)
    name_label.add_theme_font_size_override("font_size", 16)
    name_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    name_label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    row.add_child(name_label)

    var qty_label := Label.new()
    qty_label.text = "× %d" % qty
    qty_label.add_theme_color_override("font_color", COLOR_QTY)
    qty_label.add_theme_font_override("font", _font)
    qty_label.add_theme_font_size_override("font_size", 14)
    qty_label.vertical_alignment = VERTICAL_ALIGNMENT_CENTER
    row.add_child(qty_label)

    return row


## Place _panel so its right edge aligns with _last_anchor.x and its top
## edge sits 4 px below _last_anchor.y. Reads the panel's measured size
## post-rebuild (deferred a frame because Godot's layout pass hasn't
## settled yet at the same frame as show_at()).
func _reposition() -> void:
    await get_tree().process_frame
    var sz: Vector2 = _panel.size
    # Right-align with the icon's right edge; hang 4 px below.
    var x: float = _icon_rect.position.x + _icon_rect.size.x - sz.x
    var y: float = _icon_rect.position.y + _icon_rect.size.y + 4
    # Clamp inside the viewport so the popover never spills off-screen
    # if the player's window is narrow.
    var vp: Vector2 = get_viewport_rect().size
    x = clamp(x, 4, vp.x - sz.x - 4)
    y = clamp(y, 4, vp.y - sz.y - 4)
    _panel.position = Vector2(x, y)


func _input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        close()
        get_viewport().set_input_as_handled()
        return
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        var panel_rect: Rect2 = _panel.get_global_rect()
        # Click inside the panel: let the panel's own children handle it.
        if panel_rect.has_point(event.position):
            return
        # Click on the icon: let the icon's toggle handler run. Without
        # this, _input would close here AND the icon's gui_input would
        # then re-open — net no-op, looks like the click did nothing.
        if _icon_rect.has_point(event.position):
            return
        # Click anywhere else: close and consume so the click doesn't
        # also register as a /pc/move on the world below.
        close()
        get_viewport().set_input_as_handled()
