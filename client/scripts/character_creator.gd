extends Control
## The player character creator (LLM-691): a live farmer-base doll on the left,
## one row per wardrobe category on the right (a style cycler and the slot's
## colour swatches), the character's name, Randomize and Save. Opened by main
## on first login (no way out until saved) and from the top bar's shirt icon.
##
## Save: POST /pc/create when the PC does not exist yet or the name changed
## (idempotent — an existing PC is renamed, its sprite untouched), then
## POST /pc/outfit with the composed layer list. The outfit reaches every
## client, this one included, as an npc_sprite_changed frame.

signal saved(character_name: String)
signal closed

const COLOR_BG = Color(0.05, 0.03, 0.02, 0.7)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_ERROR = Color(0.85, 0.45, 0.35, 1.0)
const COLOR_SWATCH_RING = Color(0.95, 0.85, 0.55, 1.0)

## Which ramp colour a swatch shows per slot: the main tone of the ramp.
const SWATCH_TONE := {"skin": 0, "hair": 2, "c3": 1, "c4": 1}
## Swatch-row labels when an item has more than one colour.
const SLOT_LABELS := {"skin": "Skin", "hair": "Hair", "c3": "Trim", "c4": "Cloth"}
## The preview turns through the four facings while walking.
const PREVIEW_FACINGS := ["south", "west", "north", "east"]
const PREVIEW_TURN_SECONDS := 2.0

var world: Node = null

var _font: Font = null
var _panel: PanelContainer = null
var _title: Label = null
var _rows: VBoxContainer = null
var _name_edit: LineEdit = null
var _error: Label = null
var _save_button: Button = null
var _cancel_button: Button = null
var _preview_box: Control = null
var _doll: FarmerDoll = null
var _turn_timer: Timer = null
var _facing_index := 0
var _http: HTTPRequest = null
var _rng := RandomNumberGenerator.new()

var _wardrobe: Dictionary = {}
var _picks: Dictionary = {}
## Colours remembered per category and slot, so cycling styles keeps them.
var _remembered: Dictionary = {}
var _pc_exists := false
var _current_name := ""
var _current_layers: Array = []
var _cancellable := true
var _saving := false
## Bumped on every preview rebuild; a sheet callback from an older rebuild is
## dropped.
var _preview_gen := 0
var _preview_sheets: Dictionary = {}

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")
    _rng.randomize()
    anchors_preset = Control.PRESET_FULL_RECT
    anchor_right = 1.0
    anchor_bottom = 1.0

    var bg := ColorRect.new()
    bg.color = COLOR_BG
    bg.anchors_preset = Control.PRESET_FULL_RECT
    bg.anchor_right = 1.0
    bg.anchor_bottom = 1.0
    add_child(bg)

    _panel = PanelContainer.new()
    _panel.anchor_left = 0.08
    _panel.anchor_right = 0.92
    _panel.anchor_top = 0.05
    _panel.anchor_bottom = 0.95
    var panel_style := StyleBoxFlat.new()
    panel_style.bg_color = COLOR_PANEL_BG
    panel_style.set_border_width_all(2)
    panel_style.border_color = COLOR_BORDER
    panel_style.set_corner_radius_all(4)
    panel_style.content_margin_left = 20.0
    panel_style.content_margin_right = 20.0
    panel_style.content_margin_top = 14.0
    panel_style.content_margin_bottom = 14.0
    _panel.add_theme_stylebox_override("panel", panel_style)
    add_child(_panel)

    var content := VBoxContainer.new()
    content.add_theme_constant_override("separation", 10)
    _panel.add_child(content)

    _title = _label("Your Character", 18, COLOR_TEXT)
    content.add_child(_title)

    var body := HBoxContainer.new()
    body.size_flags_vertical = Control.SIZE_EXPAND_FILL
    body.add_theme_constant_override("separation", 18)
    content.add_child(body)

    var left := VBoxContainer.new()
    left.add_theme_constant_override("separation", 8)
    body.add_child(left)
    _preview_box = Control.new()
    _preview_box.clip_contents = true
    _preview_box.resized.connect(_place_doll)
    left.add_child(_preview_box)
    left.add_child(_label("Name", 13, COLOR_TEXT_DIM))
    _name_edit = LineEdit.new()
    _name_edit.max_length = 100
    _name_edit.add_theme_font_override("font", _font)
    _name_edit.add_theme_font_size_override("font_size", OrientationGuard.text_size(15))
    _name_edit.text_changed.connect(func(_t): _error.text = "")
    left.add_child(_name_edit)
    var randomize_button := _button("Randomize")
    randomize_button.pressed.connect(_on_randomize)
    left.add_child(randomize_button)

    var scroll := ScrollContainer.new()
    scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    body.add_child(scroll)
    _rows = VBoxContainer.new()
    _rows.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _rows.add_theme_constant_override("separation", 10)
    scroll.add_child(_rows)

    var footer := HBoxContainer.new()
    footer.add_theme_constant_override("separation", 10)
    content.add_child(footer)
    _error = _label("", 13, COLOR_ERROR)
    _error.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _error.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    footer.add_child(_error)
    _cancel_button = _button("Cancel")
    _cancel_button.pressed.connect(_close)
    footer.add_child(_cancel_button)
    _save_button = _button("Save")
    _save_button.pressed.connect(_on_save)
    footer.add_child(_save_button)

    _turn_timer = Timer.new()
    _turn_timer.wait_time = PREVIEW_TURN_SECONDS
    _turn_timer.timeout.connect(_turn_preview)
    add_child(_turn_timer)

    _http = HTTPRequest.new()
    _http.accept_gzip = false
    add_child(_http)

    get_viewport().size_changed.connect(_size_preview)
    _size_preview()

## Open the creator. pc_exists: the player already has a PC (otherwise Save
## creates it and there is no Cancel). current_sprite: the PC's sprite payload
## from /pc/me; a farmer-base one is read back into the rows.
func open(pc_exists: bool, character_name: String, current_sprite: Dictionary) -> void:
    _pc_exists = pc_exists
    _current_name = character_name
    _cancellable = pc_exists
    _current_layers = current_sprite.get("layers", []) if FarmerDoll.is_rig_sprite(current_sprite) else []
    _title.text = "Dress your character" if pc_exists else "Make your character"
    _cancel_button.visible = _cancellable
    _name_edit.text = character_name if character_name != "" else str(Auth.username)
    _error.text = ""
    _saving = false
    _save_button.disabled = false
    visible = true
    if _wardrobe.is_empty():
        _load_wardrobe()
    else:
        _start_from_current()

func _close() -> void:
    if not _cancellable:
        return
    visible = false
    _turn_timer.stop()
    closed.emit()

func _input(event: InputEvent) -> void:
    if visible and event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        _close()
        get_viewport().set_input_as_handled()

func _load_wardrobe() -> void:
    _error.text = "Opening the wardrobe…"
    _http.request_completed.connect(_on_wardrobe_loaded, CONNECT_ONE_SHOT)
    _http.request(Auth.api_base + "/api/village/pc/wardrobe", Auth.auth_headers(), HTTPClient.METHOD_POST, "")

func _on_wardrobe_loaded(result: int, code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    if not Auth.check_response(code):
        return
    var data = JSON.parse_string(body.get_string_from_utf8())
    if result != HTTPRequest.RESULT_SUCCESS or code != 200 or not (data is Dictionary):
        _error.text = "The wardrobe could not be opened. Close and try again."
        return
    _wardrobe = data
    _error.text = ""
    _start_from_current()

func _start_from_current() -> void:
    _picks = FarmerOutfit.decompose(_wardrobe, _current_layers) if not _current_layers.is_empty() else {}
    if not _picks.get("items", {}).has("body"):
        _picks = FarmerOutfit.default_picks(_wardrobe)
    _remembered = {}
    _rebuild_rows()
    _rebuild_preview()

func _on_randomize() -> void:
    _picks = FarmerOutfit.random_picks(_wardrobe, _rng)
    _rebuild_rows()
    _rebuild_preview()

# --- rows -------------------------------------------------------------------

func _rebuild_rows() -> void:
    for child in _rows.get_children():
        child.queue_free()
    for category in _wardrobe.get("categories", []):
        _rows.add_child(_category_row(category))

func _category_row(category: Dictionary) -> Control:
    var id := str(category.get("id", ""))
    var row := VBoxContainer.new()
    row.add_theme_constant_override("separation", 4)
    var head := HBoxContainer.new()
    head.add_theme_constant_override("separation", 8)
    row.add_child(head)
    var title := _label(str(category.get("label", id)), 15, COLOR_TEXT)
    title.custom_minimum_size.x = 150
    head.add_child(title)
    var prev := _button("<")
    var next := _button(">")
    var current := _label(_choice_label(id), 15, COLOR_TEXT_DIM)
    current.custom_minimum_size.x = 140
    current.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    prev.pressed.connect(func(): _cycle(id, -1))
    next.pressed.connect(func(): _cycle(id, 1))
    head.add_child(prev)
    head.add_child(current)
    head.add_child(next)

    var pick: Dictionary = _picks.get("items", {}).get(id, {})
    var item: Dictionary = FarmerOutfit.items_by_id(_wardrobe).get(str(pick.get("item", "")), {})
    var slots: Array = item.get("slots", [])
    for slot in slots:
        if slots.size() > 1:
            row.add_child(_label(str(SLOT_LABELS.get(slot, slot)), 12, COLOR_TEXT_DIM))
        row.add_child(_swatch_row(id, str(slot), int(pick.get("ramps", {}).get(slot, 0))))
    return row

## The body row cycles the figure; every other row cycles its items plus none.
func _choice_label(category: String) -> String:
    if category == "body":
        return "Curved" if _picks.get("figure", "") == FarmerOutfit.CURVED else "Straight"
    var pick: Dictionary = _picks.get("items", {}).get(category, {})
    var item: Dictionary = FarmerOutfit.items_by_id(_wardrobe).get(str(pick.get("item", "")), {})
    return str(item.get("label", "None")) if not item.is_empty() else "None"

func _cycle(category: String, step: int) -> void:
    if category == "body":
        _picks["figure"] = FarmerOutfit.STRAIGHT if _picks.get("figure", "") == FarmerOutfit.CURVED else FarmerOutfit.CURVED
    else:
        var options := FarmerOutfit.items_in(_wardrobe, category)
        var ids: Array = [""]
        for item in options:
            ids.append(str(item.get("id", "")))
        var items: Dictionary = _picks.get("items", {})
        var at := ids.find(str(items.get(category, {}).get("item", "")))
        var next_id: String = ids[posmod(at + step, ids.size())]
        if items.has(category):
            _remembered[category] = items[category].get("ramps", {})
            items.erase(category)
        if next_id != "":
            items[category] = {"item": next_id, "ramps": _colours_for_switch(category, next_id)}
    _rebuild_rows()
    _rebuild_preview()

## Colours for an item switched to: what the category last wore, slot by slot,
## else the first offered colour.
func _colours_for_switch(category: String, item_id: String) -> Dictionary:
    var item: Dictionary = FarmerOutfit.items_by_id(_wardrobe).get(item_id, {})
    var ramps := FarmerOutfit.first_colours(_wardrobe, item)
    var before: Dictionary = _remembered.get(category, {})
    for slot in ramps:
        if before.has(slot):
            ramps[slot] = int(before[slot])
    return ramps

func _swatch_row(category: String, slot: String, selected: int) -> Control:
    var flow := HFlowContainer.new()
    flow.add_theme_constant_override("h_separation", 4)
    flow.add_theme_constant_override("v_separation", 4)
    var family: Array = FarmerPalettes.RAMPS.get(FarmerPalettes.FAMILY.get(slot, ""), [])
    var size := float(OrientationGuard.text_size(18))
    for index in FarmerOutfit.colours_for(_wardrobe, slot):
        if index < 0 or index >= family.size():
            continue
        var ramp: Array = family[index]
        var tone: int = mini(int(SWATCH_TONE.get(slot, 1)), ramp.size() - 1)
        var swatch := Panel.new()
        swatch.custom_minimum_size = Vector2(size, size)
        swatch.mouse_default_cursor_shape = Control.CURSOR_POINTING_HAND
        var style := StyleBoxFlat.new()
        style.bg_color = Color.html(str(ramp[tone]))
        style.set_corner_radius_all(2)
        if index == selected:
            style.set_border_width_all(2)
            style.border_color = COLOR_SWATCH_RING
        swatch.add_theme_stylebox_override("panel", style)
        var chosen := index
        swatch.gui_input.connect(func(event: InputEvent):
            if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
                _set_colour(category, slot, chosen)
        )
        flow.add_child(swatch)
    return flow

func _set_colour(category: String, slot: String, index: int) -> void:
    var pick: Dictionary = _picks.get("items", {}).get(category, {})
    if pick.is_empty():
        return
    pick["ramps"][slot] = index
    _rebuild_rows()
    _rebuild_preview()

# --- preview ----------------------------------------------------------------

## Integer preview scale (pixel art): 4 on a tall window, 3 on a short one.
func _preview_scale() -> int:
    return 4 if get_viewport_rect().size.y >= 640 else 3

func _size_preview() -> void:
    var cell := FarmerRig.CELL_SIZE * _preview_scale()
    _preview_box.custom_minimum_size = Vector2(cell * 0.75, cell * 0.75)
    if _doll != null:
        _doll.scale = Vector2.ONE * _preview_scale()
    _place_doll()

## The figure stands in the middle of the cell; crop the empty rows around it.
func _place_doll() -> void:
    if _doll == null:
        return
    var s := float(_preview_scale())
    var cell := FarmerRig.CELL_SIZE * s
    _doll.position = Vector2((_preview_box.size.x - cell) / 2.0, (_preview_box.size.y - cell) / 2.0 - 2.0 * s)

func _rebuild_preview() -> void:
    _preview_gen += 1
    var gen := _preview_gen
    var sprite := FarmerOutfit.preview_sprite(FarmerOutfit.compose(_wardrobe, _picks))
    var paths := FarmerDoll.sheet_paths(sprite)
    var remaining := {"count": paths.size()}
    var settle := func():
        remaining.count -= 1
        if remaining.count == 0 and gen == _preview_gen:
            _show_doll(sprite)
    for path in paths:
        if world == null:
            settle.call()
            continue
        world.get_or_load_npc_sheet(path, func(tex: Texture2D):
            _preview_sheets[path] = tex
            settle.call()
        , settle)

func _show_doll(sprite: Dictionary) -> void:
    if _doll != null:
        _doll.queue_free()
        _doll = null
    var doll := FarmerDoll.new()
    doll.setup(sprite, _preview_sheets)
    if doll.sprite_frames == null:
        doll.free()
        return
    doll.centered = false
    doll.scale = Vector2.ONE * _preview_scale()
    _preview_box.add_child(doll)
    _doll = doll
    _place_doll()
    _play_preview()
    _turn_timer.start()

func _turn_preview() -> void:
    _facing_index = (_facing_index + 1) % PREVIEW_FACINGS.size()
    _play_preview()

func _play_preview() -> void:
    if _doll == null:
        return
    var anim := str(PREVIEW_FACINGS[_facing_index]) + "_walk"
    if _doll.sprite_frames.has_animation(anim):
        _doll.play(anim)

# --- save -------------------------------------------------------------------

func _on_save() -> void:
    if _saving or _wardrobe.is_empty():
        return
    var character_name := _name_edit.text.strip_edges()
    if character_name == "":
        _error.text = "Enter a name."
        return
    _saving = true
    _save_button.disabled = true
    _error.text = ""
    if not _pc_exists or character_name != _current_name:
        _http.request_completed.connect(_on_create_done.bind(character_name), CONNECT_ONE_SHOT)
        _http.request(Auth.api_base + "/api/village/pc/create", Auth.auth_headers(),
            HTTPClient.METHOD_POST, JSON.stringify({"character_name": character_name}))
    else:
        _send_outfit(character_name)

func _on_create_done(result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray, character_name: String) -> void:
    if not Auth.check_response(code):
        _fail("")
        return
    if code == 409:
        _fail("That name is already taken. Try another.")
        return
    if result != HTTPRequest.RESULT_SUCCESS or code < 200 or code >= 300:
        _fail("That name cannot be used. Try another." if code == 400 else "The village did not answer. Try again.")
        return
    _pc_exists = true
    _current_name = character_name
    _send_outfit(character_name)

func _send_outfit(character_name: String) -> void:
    var layers := FarmerOutfit.compose(_wardrobe, _picks)
    _http.request_completed.connect(_on_outfit_done.bind(character_name, layers), CONNECT_ONE_SHOT)
    _http.request(Auth.api_base + "/api/village/pc/outfit", Auth.auth_headers(),
        HTTPClient.METHOD_POST, JSON.stringify({"layers": layers}))

func _on_outfit_done(result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray, character_name: String, layers: Array) -> void:
    if not Auth.check_response(code):
        _fail("")
        return
    if result != HTTPRequest.RESULT_SUCCESS or code < 200 or code >= 300:
        _fail("Your clothes could not be saved. Try again.")
        return
    _saving = false
    _save_button.disabled = false
    _current_layers = layers
    _cancellable = true
    visible = false
    _turn_timer.stop()
    saved.emit(character_name)
    closed.emit()

func _fail(message: String) -> void:
    _saving = false
    _save_button.disabled = false
    _error.text = message

# --- widgets ----------------------------------------------------------------

func _label(text: String, size: int, color: Color) -> Label:
    var label := Label.new()
    label.text = text
    label.add_theme_font_override("font", _font)
    label.add_theme_font_size_override("font_size", OrientationGuard.text_size(size))
    label.add_theme_color_override("font_color", color)
    return label

func _button(text: String) -> Button:
    var button := Button.new()
    button.text = text
    button.add_theme_font_override("font", _font)
    button.add_theme_font_size_override("font_size", OrientationGuard.text_size(14))
    button.focus_mode = Control.FOCUS_NONE
    return button
