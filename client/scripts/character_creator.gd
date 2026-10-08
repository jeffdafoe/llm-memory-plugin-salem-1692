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
##
## Buy (LLM-715): a piece or dye the player tries on but does not hold gets a
## Buy button while someone in the player's huddle holds it (the wardrobe's
## "sellers"). Buy takes that seller's live quote for one, if there is one, or
## offers the list price through POST /pc/pay; the seller's answer arrives as
## the world's pay_resolved / pay_countered. A bought good is held at once
## (take-home goods move at accept), so the player only has to press Save.

signal saved(character_name: String)
signal closed

const COLOR_BG = Color(0.05, 0.03, 0.02, 0.7)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_ERROR = Color(0.85, 0.45, 0.35, 1.0)
const COLOR_SWATCH_RING = Color(0.95, 0.85, 0.55, 1.0)
## A swatch whose dye the player does not hold (LLM-710): still clickable, to
## try the colour on, but faded.
const LOCKED_SWATCH_ALPHA := 0.3

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
## Set while dressing a villager from the editor (open_for_npc); empty while
## dressing the player.
var _npc_id := ""
var _name_label: Label = null
var _current_name := ""
var _current_layers: Array = []
var _cancellable := true
var _saving := false
## A request is on _http. Every completion handler clears it first; _post
## refuses a second request while it is set, so a callback can never consume
## another request's completion.
var _in_flight := false
## Bumped on every preview rebuild; a sheet callback from an older rebuild is
## dropped.
var _preview_gen := 0
var _preview_sheets: Dictionary = {}
## Buys started from the creator, by good: {state, seller, ledger_id, amount,
## message}. state is "sending" (a request is out), "waiting" (the offer is
## before the seller), "countered", "declined" (or failed) or "bought".
var _buys: Dictionary = {}

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
    _name_label = _label("Name", 13, COLOR_TEXT_DIM)
    left.add_child(_name_label)
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

    if world != null and world.has_signal("pay_resolved"):
        world.pay_resolved.connect(_on_pay_resolved)
        world.pay_countered.connect(_on_pay_countered)

    get_viewport().size_changed.connect(_size_preview)
    _size_preview()

## Open the creator. pc_exists: the player already has a PC (otherwise Save
## creates it and there is no Cancel). current_sprite: the PC's sprite payload
## from /pc/me; a farmer-base one is read back into the rows.
func open(pc_exists: bool, character_name: String, current_sprite: Dictionary) -> void:
    _npc_id = ""
    _pc_exists = pc_exists
    _current_name = character_name
    _cancellable = pc_exists
    _current_layers = current_sprite.get("layers", []) if FarmerDoll.is_rig_sprite(current_sprite) else []
    _title.text = "Dress your character" if pc_exists else "Make your character"
    _cancel_button.visible = _cancellable
    _name_edit.text = character_name if character_name != "" else str(Auth.username)
    _name_label.visible = true
    _name_edit.visible = true
    _show()

## Open the creator on a villager from the editor's Dress… button. No name
## field (the editor renames villagers); Save posts /admin/npc/outfit.
func open_for_npc(npc_id: String, display_name: String, current_sprite: Dictionary) -> void:
    _npc_id = npc_id
    _cancellable = true
    _current_layers = current_sprite.get("layers", []) if FarmerDoll.is_rig_sprite(current_sprite) else []
    _title.text = "Dress " + display_name
    _cancel_button.visible = true
    _name_label.visible = false
    _name_edit.visible = false
    _show()

## A player's open always reloads the wardrobe: what the player holds changes
## as they trade (LLM-710). Dressing a villager needs no holdings.
func _show() -> void:
    _error.text = ""
    visible = true
    # An offer still before a seller keeps its line; a finished one does not.
    for good in _buys.keys():
        if not ["sending", "waiting", "countered"].has(str(_buys[good].get("state", ""))):
            _buys.erase(good)
    if _wardrobe.is_empty() or _npc_id == "":
        if not _in_flight:
            _load_wardrobe()
    else:
        _wardrobe["held"] = _wardrobe.get("goods", {}).keys()
        _start_from_current()

func _close() -> void:
    # No closing mid-save: the save would land with the creator shut, and a
    # reopen could start a second one on the same request.
    if not _cancellable or _saving:
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
    if not _post("/api/village/pc/wardrobe", "", _on_wardrobe_loaded):
        _error.text = "The wardrobe could not be opened. Close and try again."

## Start one request on _http with callback as its one-shot completion.
## Returns false, leaving nothing connected, when a request is already on
## _http or the request cannot start (no completion would ever arrive).
func _post(path: String, body: String, callback: Callable, method := HTTPClient.METHOD_POST) -> bool:
    if _in_flight:
        return false
    _http.request_completed.connect(callback, CONNECT_ONE_SHOT)
    var err := _http.request(Auth.api_base + path, Auth.auth_headers(), method, body)
    if err != OK:
        _http.request_completed.disconnect(callback)
        return false
    _in_flight = true
    return true

func _on_wardrobe_loaded(result: int, code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    _in_flight = false
    if not Auth.check_response(code):
        return
    var data = JSON.parse_string(body.get_string_from_utf8())
    if result != HTTPRequest.RESULT_SUCCESS or code != 200 or not (data is Dictionary):
        _error.text = "The wardrobe could not be opened. Close and try again."
        return
    _wardrobe = data
    # A villager holds no wardrobe goods; the editor may dress one in anything.
    if _npc_id != "":
        _wardrobe["held"] = _wardrobe.get("goods", {}).keys()
    _error.text = ""
    _start_from_current()

## A player starts from the outfit they chose (the wardrobe's "outfit"), less
## what they no longer hold; a villager from what it wears.
func _start_from_current() -> void:
    var layers: Array = _current_layers
    var chosen = _wardrobe.get("outfit", null)
    if _npc_id == "" and chosen is Array and not chosen.is_empty():
        layers = chosen
    _picks = FarmerOutfit.decompose(_wardrobe, layers) if not layers.is_empty() else {}
    _picks = FarmerOutfit.without_locked(_wardrobe, _picks)
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
    for good in _store_goods(item, pick):
        row.add_child(_store_line(good))
    return row

## The goods a row is trying on that the player does not hold, plus any just
## bought (their line says to press Save).
func _store_goods(item: Dictionary, pick: Dictionary) -> Array:
    var goods: Array = []
    var wanted: Array = [str(item.get("good", ""))]
    var ramps: Dictionary = pick.get("ramps", {})
    for slot in ramps:
        wanted.append(FarmerOutfit.dye_for(_wardrobe, str(slot), int(ramps[slot])))
    for good in wanted:
        if good == "" or goods.has(good):
            continue
        if not FarmerOutfit.holds(_wardrobe, good) or str(_buys.get(good, {}).get("state", "")) == "bought":
            goods.append(good)
    return goods

## One line under a row per good: where it is sold or who here has it, or how
## a buy is going, with the Buy / Accept / No buttons that apply.
func _store_line(good: String) -> Control:
    var line := HBoxContainer.new()
    line.add_theme_constant_override("separation", 8)
    var text := _label(_store_text(good), 12, COLOR_TEXT_DIM)
    text.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    text.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    line.add_child(text)
    match str(_buys.get(good, {}).get("state", "")):
        "countered":
            line.add_child(_line_button("Accept", _on_accept_counter.bind(good)))
            line.add_child(_line_button("No", _on_refuse_counter.bind(good)))
        "sending", "waiting", "bought":
            pass
        _:
            if _npc_id == "" and _seller_for(good) != "":
                line.add_child(_line_button("Buy", _on_buy.bind(good)))
    return line

func _store_text(good: String) -> String:
    var label := FarmerOutfit.good_label(_wardrobe, good)
    var price := FarmerOutfit.good_price(_wardrobe, good)
    var about := ", about %d coins" % price if price > 0 else ""
    var buy: Dictionary = _buys.get(good, {})
    var seller := str(buy.get("seller", ""))
    match str(buy.get("state", "")):
        "sending":
            return "%s: asking %s…" % [label, seller]
        "waiting":
            return "%s: waiting for %s…" % [label, seller]
        "countered":
            var asks := "%s: %s asks %d coins." % [label, seller, int(buy.get("amount", 0))]
            var note := str(buy.get("message", ""))
            return asks + (" \"%s\"" % note if note != "" else "")
        "declined":
            return "%s: %s" % [label, str(buy.get("message", ""))]
        "bought":
            return "%s: bought. Press Save to wear it." % label
    var holder := _seller_for(good)
    if holder != "":
        return "%s: %s has it%s." % [label, holder, about]
    var sellers: Array = _wardrobe.get("sellers", {}).get(good, [])
    if not sellers.is_empty():
        return "%s: %s has none just now." % [label, str(sellers[0].get("name", ""))]
    return "Sold at the Store: %s%s." % [label, about]

## Someone in the player's huddle who holds the good, by name; "" if nobody.
func _seller_for(good: String) -> String:
    for seller in _wardrobe.get("sellers", {}).get(good, []):
        if int(seller.get("held", 0)) > 0:
            return str(seller.get("name", ""))
    return ""

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
    # The colours the player may wear first, then the dyed ones to try on.
    var open := FarmerOutfit.open_colours(_wardrobe, slot)
    var order: Array[int] = open.duplicate()
    for index in FarmerOutfit.colours_for(_wardrobe, slot):
        if not open.has(index):
            order.append(index)
    for index in order:
        if index < 0 or index >= family.size():
            continue
        var ramp: Array = family[index]
        var tone: int = mini(int(SWATCH_TONE.get(slot, 1)), ramp.size() - 1)
        var swatch := Panel.new()
        if not open.has(index):
            var dye := FarmerOutfit.dye_for(_wardrobe, slot, index)
            swatch.modulate.a = LOCKED_SWATCH_ALPHA
            swatch.tooltip_text = FarmerOutfit.good_label(_wardrobe, dye) + " — sold at the Store"
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

# --- buying -----------------------------------------------------------------

## Buy one of a good from whoever here holds it: first look for that seller's
## live quote (it settles at once), else offer the list price.
func _on_buy(good: String) -> void:
    var seller := _seller_for(good)
    if seller == "" or _npc_id != "":
        return
    if not _post("/api/village/pc/quotes", "", _on_buy_quotes.bind(good), HTTPClient.METHOD_GET):
        _error.text = "One moment, the village has not answered yet."
        return
    _buys[good] = {"state": "sending", "seller": seller}
    _rebuild_rows()

func _on_buy_quotes(result: int, code: int, _headers: PackedStringArray, body: PackedByteArray, good: String) -> void:
    _in_flight = false
    if not Auth.check_response(code):
        _buy_failed(good, "")
        return
    var seller := str(_buys.get(good, {}).get("seller", ""))
    var terms := {"seller": seller, "item": good, "qty": 1,
        "amount": maxi(1, FarmerOutfit.good_price(_wardrobe, good)), "consume_now": false}
    # A failed quote read is no reason to stop: the list-price offer still works.
    var data = JSON.parse_string(body.get_string_from_utf8())
    if result == HTTPRequest.RESULT_SUCCESS and code == 200 and data is Dictionary:
        var quote := _quote_for(data.get("quotes", []), seller, good)
        if not quote.is_empty():
            terms["amount"] = int(quote.get("amount", 0))
            terms["consume_now"] = bool(quote.get("consume_now", false))
            terms["quote_id"] = int(quote.get("quote_id", 0))
    _send_buy(good, terms)

## The seller's live quote for exactly one of the good, taken home; {} if none.
## A take must echo the quote's terms verbatim (pc/quotes).
static func _quote_for(quotes: Array, seller: String, good: String) -> Dictionary:
    for q in quotes:
        if not (q is Dictionary):
            continue
        if str(q.get("seller", "")) != seller or q.get("lines", []).size() != 1:
            continue
        if str(q.get("item", "")) == good and int(q.get("qty", 0)) == 1 and not bool(q.get("consume_now", false)):
            return q
    return {}

func _send_buy(good: String, terms: Dictionary) -> void:
    if not _post("/api/village/pc/pay", JSON.stringify(terms), _on_buy_sent.bind(good, terms)):
        _buy_failed(good, "")
        return
    _buys[good] = {"state": "sending", "seller": str(terms.get("seller", ""))}
    _rebuild_rows()

func _on_buy_sent(result: int, code: int, _headers: PackedStringArray, body: PackedByteArray, good: String, terms: Dictionary) -> void:
    _in_flight = false
    if not Auth.check_response(code):
        _buy_failed(good, "")
        return
    var data = JSON.parse_string(body.get_string_from_utf8())
    if result != HTTPRequest.RESULT_SUCCESS or code < 200 or code >= 300 or not (data is Dictionary):
        # The engine's refusal names the cause (too few coins, the seller left).
        _buy_failed(good, str(data.get("error", "")) if data is Dictionary else "")
        return
    var seller := str(terms.get("seller", ""))
    if str(data.get("state", "")) == "accepted":
        _bought(good, seller)
        return
    _buys[good] = {"state": "waiting", "seller": seller, "ledger_id": int(data.get("ledger_id", 0))}
    _rebuild_rows()

func _on_accept_counter(good: String) -> void:
    var buy: Dictionary = _buys.get(good, {})
    if str(buy.get("state", "")) != "countered":
        return
    var terms := {"seller": str(buy.get("seller", "")), "item": good, "qty": 1,
        "amount": int(buy.get("amount", 0)), "consume_now": false,
        "in_response_to": int(buy.get("ledger_id", 0))}
    if _in_flight:
        _error.text = "One moment, the village has not answered yet."
        return
    _send_buy(good, terms)

## The countered offer is already closed; saying no needs no request.
func _on_refuse_counter(good: String) -> void:
    _buys.erase(good)
    _rebuild_rows()

## The world's pay_resolved, for an offer this creator is waiting on.
func _on_pay_resolved(data: Dictionary) -> void:
    var good := _good_for_ledger(int(data.get("ledger_id", 0)))
    if good == "":
        return
    var seller := str(_buys[good].get("seller", ""))
    var state := str(data.get("terminal_state", ""))
    if state == "accepted":
        _bought(good, seller)
        return
    var reason := str(data.get("message", ""))
    var text := ""
    match state:
        "declined":
            text = "%s said no%s" % [seller, ": \"%s\"" % reason if reason != "" else "."]
        "expired":
            text = "%s did not answer." % seller
        "failed_insufficient_funds":
            text = "You did not have the coins."
        "failed_insufficient_stock":
            text = "%s had none left." % seller
        _:
            text = "The sale fell through."
    _buys[good] = {"state": "declined", "seller": seller, "message": text}
    if visible:
        _rebuild_rows()

## The world's pay_countered: the seller names another price.
func _on_pay_countered(data: Dictionary) -> void:
    var good := _good_for_ledger(int(data.get("ledger_id", 0)))
    if good == "":
        return
    _buys[good] = {"state": "countered", "seller": str(_buys[good].get("seller", "")),
        "ledger_id": int(data.get("ledger_id", 0)), "amount": int(data.get("counter_amount", 0)),
        "message": str(data.get("message", ""))}
    if visible:
        _rebuild_rows()

func _good_for_ledger(ledger_id: int) -> String:
    if ledger_id == 0:
        return ""
    for good in _buys:
        var buy: Dictionary = _buys[good]
        if str(buy.get("state", "")) == "waiting" and int(buy.get("ledger_id", 0)) == ledger_id:
            return good
    return ""

## The good is the player's now: unlock it here without reloading the wardrobe.
## Save still checks holdings, so a stale unlock cannot dress anyone.
func _bought(good: String, seller: String) -> void:
    var held: Array = _wardrobe.get("held", [])
    if not held.has(good):
        held.append(good)
    _wardrobe["held"] = held
    _buys[good] = {"state": "bought", "seller": seller}
    if visible:
        _rebuild_rows()

func _buy_failed(good: String, message: String) -> void:
    _buys[good] = {"state": "declined", "seller": str(_buys.get(good, {}).get("seller", "")),
        "message": message if message != "" else "The village did not answer. Try again."}
    _rebuild_rows()

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
    if _npc_id != "":
        _save_npc()
        return
    var character_name := _name_edit.text.strip_edges()
    if character_name == "":
        _error.text = "Enter a name."
        return
    var missing := FarmerOutfit.missing_goods(_wardrobe, _picks)
    if not missing.is_empty():
        var labels: Array = []
        for good in missing:
            labels.append(FarmerOutfit.good_label(_wardrobe, good))
        _error.text = "You do not have: %s. Buy them at the Store first." % ", ".join(labels)
        return
    _saving = true
    _save_button.disabled = true
    _error.text = ""
    if not _pc_exists or character_name != _current_name:
        if not _post("/api/village/pc/create", JSON.stringify({"character_name": character_name}),
                _on_create_done.bind(character_name)):
            _fail("The village did not answer. Try again.")
    else:
        _send_outfit(character_name)

func _on_create_done(result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray, character_name: String) -> void:
    _in_flight = false
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
    var layers := FarmerOutfit.compose(_wardrobe, _picks, true)
    if not _post("/api/village/pc/outfit", JSON.stringify({"layers": layers}), _on_outfit_done.bind(character_name, layers)):
        _fail("The village did not answer. Try again.")

func _on_outfit_done(result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray, character_name: String, layers: Array) -> void:
    _in_flight = false
    if not Auth.check_response(code):
        _fail("")
        return
    if code == 409:
        # Something was sold or worn out since the wardrobe loaded.
        _fail("You no longer have some of these clothes. Close and open the wardrobe again.")
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

## Villager mode: one admin request. The new outfit reaches every client as
## npc_sprite_changed; the creator just closes.
func _save_npc() -> void:
    _saving = true
    _save_button.disabled = true
    _error.text = ""
    var layers := FarmerOutfit.compose(_wardrobe, _picks)
    if not _post("/api/village/admin/npc/outfit", JSON.stringify({"npc_id": _npc_id, "layers": layers}), _on_npc_outfit_done.bind(layers)):
        _fail("The village did not answer. Try again.")

func _on_npc_outfit_done(result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray, layers: Array) -> void:
    _in_flight = false
    if not Auth.check_response(code):
        _fail("")
        return
    if code == 403:
        _fail("Only an admin can dress villagers.")
        return
    if code == 422:
        _fail("This one cannot be dressed.")
        return
    if result != HTTPRequest.RESULT_SUCCESS or code < 200 or code >= 300:
        _fail("The clothes could not be saved. Try again.")
        return
    _saving = false
    _save_button.disabled = false
    _current_layers = layers
    visible = false
    _turn_timer.stop()
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

## A small button on a row's store line.
func _line_button(text: String, on_pressed: Callable) -> Button:
    var button := _button(text)
    button.add_theme_font_size_override("font_size", OrientationGuard.text_size(12))
    button.pressed.connect(on_pressed)
    return button
