extends CanvasLayer

## The Pay box (LLM-722) — the player buys from, or pays, someone they are
## talking with. Opened from the talk sheet's Pay button (talk_panel.gd).
##
## Two pages:
##   OFFERS — what is offered to the player right now, one card each: a
##     seller's counter to the player's own offer (answered with
##     in_response_to), a posted quote (GET pc/quotes; Buy takes it by
##     quote_id, the fast path), and per seller the goods they have spoken of
##     (speak.mentions, gathered by talk_panel.gd). A remark is not a binding
##     offer, so a spoken good opens the OFFER page filled in instead of paying.
##   OFFER — the player's own offer: who, what, how many, how much. The goods
##     listed are the seller's spoken mentions plus anything they have a posted
##     quote for. The send button names the whole deal.
##   GIVE — coins handed to anyone here, nothing bought (LLM-725): a gift, a
##     tip, a debt paid back. Who, how much, an optional "what for". POST
##     pc/give; the recipient may be another player.
##
## Every pay is POST pc/pay, every gift POST pc/give. The engine owns every
## rule (co-presence, stock, funds, counter chains, a gift that is really a
## purchase); a refusal shows on the status line.
##
## The panel reads the talk panel's live state through `host`: huddle_members,
## character_name, pc_actor_id, pc_coins, pc_lodging, vendor_mentions,
## vendor_mention_prices, vendor_latest_mentions.
##
## Layer 30, above the talk sheet. talk_panel.gd forwards open_changed as its
## modal_open_changed, so main.gd blocks world input while the box is open.

const FONT_PATH := "res://assets/fonts/IMFellEnglish-Regular.ttf"
const TAP_H := 44.0
## The engine's PayLedgerInResponseToWindow: a counter older than this can no
## longer be answered, so its card is dropped.
const COUNTER_WINDOW_MS := 60 * 60 * 1000
## The engine's cap on a gift's "what for" line (pc/give, maxPayForChars).
const GIVE_FOR_MAX := 200

const COLOR_TITLE := Color(0.92, 0.78, 0.42)
const COLOR_TEXT := Color(0.92, 0.84, 0.70)
const COLOR_DIM := Color(0.78, 0.68, 0.50)
const COLOR_ERROR := Color(0.93, 0.62, 0.48)
const COLOR_BORDER := Color(0.55, 0.42, 0.24, 0.95)

signal open_changed(open: bool)
## A slow-path offer was minted; the seller answers on a later tick.
signal offer_pending(seller: String)
## Any pay the engine took (2xx) — the host re-polls pc/me for the purse.
signal paid()
## A gift the engine took — the host writes it in the talk log.
signal gave(recipient: String, amount: int)

enum Page { OFFERS, OFFER, GIVE }

var host: Node = null
## Test seam, like repair_panel.gd's: when set, called as
## send_hook.call(route, body) instead of making the request.
var send_hook: Callable = Callable()

var page := Page.OFFERS
var quotes: Array = []
var dismissed_quotes: Dictionary = {}
## Counters to the player's own offers, keyed by the countered ledger id:
## {ledger_id, seller, item, qty, amount, original, consume_now, at_ms}.
var counters: Dictionary = {}
## consume_now of the player's own pending offers, keyed by ledger id, so a
## counter's answer keeps the disposition the player first asked for.
var _offer_consume_now: Dictionary = {}

var catalog_labels: Dictionary = {}  # lowercase item name -> display label
var catalog_dispo: Dictionary = {}   # lowercase item name -> choice | eat_here | tonight
var _catalog_requested := false

## Have it here (true) or take it home. One choice for both pages; kept for
## the session. Have-it-here is the default (LLM-415).
var eat_here := true

## The player's own offer.
var sel_seller := ""
var sel_item := ""
var qty := 1
var amount := 1
var days_ahead := 0

## The player's gift.
var give_to := ""
var give_amount := 1

var _busy := false
var _busy_kind := ""
var _busy_seller := ""
var _busy_counter := 0
## Counts opens. A pay answer closes the box only if it is still the same
## open it was sent from — not a box the player closed and opened again.
var _open_gen := 0
var _busy_gen := 0
## True while a background refresh rebuilds the offer page — the one time a
## field the player is typing in must not be rewritten.
var _refreshing := false

var _font: Font = null
var _http_pay: HTTPRequest = null
var _http_quotes: HTTPRequest = null
var _http_items: HTTPRequest = null
var _http_give: HTTPRequest = null

var root: Control = null
var sheet: PanelContainer = null
var coins_label: Label = null
var title_label: Label = null
var status_label: Label = null
var offers_page: VBoxContainer = null
var offers_header: Label = null
var offers_scroll: ScrollContainer = null
var offers_box: VBoxContainer = null
var offers_dispo_row: Control = null
var own_offer_button: Button = null
var give_button: Button = null
var offer_page: VBoxContainer = null
var who_flow: HFlowContainer = null
var what_flow: HFlowContainer = null
var what_empty_label: Label = null
var qty_stepper: Dictionary = {}
var amount_stepper: Dictionary = {}
var price_hint: Label = null
var offer_dispo_row: Control = null
var night_row: Control = null
var night_stepper: Dictionary = {}
var send_button: Button = null
var give_page: VBoxContainer = null
var give_who_flow: HFlowContainer = null
var give_amount_stepper: Dictionary = {}
var give_for_field: LineEdit = null
var give_hint: Label = null
var give_send_button: Button = null
var _dispo_buttons: Array = []  # [eat, home] pairs, one per page


func _ready() -> void:
    layer = 30
    _font = load(FONT_PATH) if ResourceLoader.exists(FONT_PATH) else ThemeDB.fallback_font
    _build_ui()
    visible = false


# --- public ------------------------------------------------------------------

func open() -> void:
    _open_gen += 1
    _ensure_catalog()
    quotes = []
    _send("quotes")
    _set_status("")
    _show_page(Page.OFFERS)
    visible = true
    open_changed.emit(true)


func close() -> void:
    if not visible:
        return
    visible = false
    open_changed.emit(false)


func is_open() -> bool:
    return visible


## Rebuild what is showing — new mentions, a purse change, a roster change.
func refresh() -> void:
    if not visible or sheet == null:
        return
    _rebuild_coins()
    if page == Page.OFFERS:
        _rebuild_offers()
    else:
        _refreshing = true
        if page == Page.OFFER:
            _rebuild_offer_page()
        else:
            _rebuild_give_page()
        _refreshing = false


## The player left the conversation: hidden quotes and the bookkeeping of
## their own offers do not carry over.
func reset_huddle() -> void:
    dismissed_quotes.clear()
    _offer_consume_now.clear()


func note_pay_offer(data: Dictionary) -> void:
    if _pc_id() == "" or str(data.get("buyer_id", "")) != _pc_id():
        return
    _offer_consume_now[int(data.get("ledger_id", 0))] = bool(data.get("consume_now", true))


func note_countered(data: Dictionary) -> void:
    if _pc_id() == "" or str(data.get("buyer_id", "")) != _pc_id():
        return
    var id := int(data.get("ledger_id", 0))
    if id == 0:
        return
    var item := str(data.get("item", ""))
    var consume_now: bool = _offer_consume_now.get(id, _default_consume_now(item))
    counters[id] = {
        "ledger_id": id,
        "seller": str(data.get("seller_name", "")),
        "item": item,
        "qty": int(data.get("qty", 1)),
        "amount": int(data.get("counter_amount", 0)),
        "original": int(data.get("original_amount", 0)),
        "consume_now": consume_now,
        "at_ms": Time.get_ticks_msec(),
    }
    _offer_consume_now.erase(id)
    refresh()


func note_resolved(data: Dictionary) -> void:
    var id := int(data.get("ledger_id", 0))
    _offer_consume_now.erase(id)
    if counters.has(id):
        counters.erase(id)
        refresh()


## The catalog label for an item name, else the name itself.
func item_label(item_name: String) -> String:
    return str(catalog_labels.get(item_name.to_lower(), item_name))


# --- state the host owns -----------------------------------------------------

func _pc_id() -> String:
    return str(host.pc_actor_id) if host != null else ""


func _coins_held() -> int:
    return int(host.pc_coins) if host != null else 0


func recipients() -> Array:
    var out: Array = []
    if host == null:
        return out
    for m in host.huddle_members:
        if typeof(m) != TYPE_DICTIONARY:
            continue
        var n := str(m.get("name", ""))
        if n != "" and n != str(host.character_name) and not out.has(n):
            out.append(n)
    return out


func _mentions_of(seller: String) -> Array:
    var out: Array = []
    if host == null:
        return out
    var m = host.vendor_mentions.get(seller, [])
    if typeof(m) == TYPE_ARRAY or typeof(m) == TYPE_PACKED_STRING_ARRAY:
        for k in m:
            var s := str(k)
            if s != "" and not out.has(s):
                out.append(s)
    return out


func _heard_price(seller: String, item: String) -> int:
    if host == null:
        return 0
    var prices = host.vendor_mention_prices.get(seller, {})
    if typeof(prices) != TYPE_DICTIONARY:
        return 0
    return maxi(int(prices.get(item.to_lower(), 0)), 0)


func _dispo(item: String) -> String:
    return str(catalog_dispo.get(item.to_lower(), ""))


func _default_consume_now(item: String) -> bool:
    if item.to_lower() == "nights_stay":
        return false
    match _dispo(item):
        "eat_here":
            return true
        "take_home":
            return false
    return eat_here


static func _same(a: String, b: String) -> bool:
    return a.to_lower() == b.to_lower()


# --- what goes on the wire ---------------------------------------------------

## Take a quote: its terms verbatim (the fast path matches them exactly), with
## the player's have-it-here choice for a choice-class good. An eat_here-class
## good always settles eaten here for a player, whatever the quote proposed;
## a service or an unknown class sends the quote's own consume_now.
func take_body(q: Dictionary) -> Dictionary:
    var item := str(q.get("item", ""))
    var consume_now := bool(q.get("consume_now", false))
    var lines = q.get("lines", [])
    var bundle: bool = typeof(lines) == TYPE_ARRAY and lines.size() > 1
    if not bundle:
        match _dispo(item):
            "choice":
                consume_now = eat_here
            "eat_here":
                consume_now = true
            "take_home":
                consume_now = false
    return {
        "seller": str(q.get("seller", "")),
        "item": item,
        "qty": int(q.get("qty", 1)),
        "amount": int(q.get("amount", 0)),
        "consume_now": consume_now,
        "quote_id": int(q.get("quote_id", 0)),
    }


## Answer a counter at the seller's price. Still a slow-path offer: the seller
## answers it on a later tick.
func counter_body(c: Dictionary) -> Dictionary:
    return {
        "seller": str(c.get("seller", "")),
        "item": str(c.get("item", "")),
        "qty": int(c.get("qty", 1)),
        "amount": int(c.get("amount", 0)),
        "consume_now": bool(c.get("consume_now", true)),
        "in_response_to": int(c.get("ledger_id", 0)),
    }


## The player's own offer. A room is never consumed on the spot; an
## eat_here-class good always is; otherwise the have-it-here choice.
func offer_body() -> Dictionary:
    var body := {
        "seller": sel_seller,
        "item": sel_item,
        "qty": qty,
        "amount": amount,
        "consume_now": _default_consume_now(sel_item),
    }
    if sel_item.to_lower() == "nights_stay" and days_ahead > 0:
        body["ready_in_days"] = days_ahead
    return body


# --- offers page ---------------------------------------------------------------

## The quotes and counters with a card now: seller still here, not hidden,
## counter not too old to answer.
func visible_quotes() -> Array:
    var here := recipients()
    var out: Array = []
    for q in quotes:
        if typeof(q) != TYPE_DICTIONARY:
            continue
        if dismissed_quotes.has(int(q.get("quote_id", 0))):
            continue
        if not _has_name(here, str(q.get("seller", ""))):
            continue
        out.append(q)
    return out


func visible_counters() -> Array:
    var here := recipients()
    var now := Time.get_ticks_msec()
    var out: Array = []
    for id in counters.keys():
        var c: Dictionary = counters[id]
        # Too old to answer, or the seller has gone: the counter is over.
        if now - int(c.get("at_ms", 0)) > COUNTER_WINDOW_MS or not _has_name(here, str(c.get("seller", ""))):
            counters.erase(id)
            continue
        out.append(c)
    return out


static func _has_name(names: Array, n: String) -> bool:
    for x in names:
        if _same(str(x), n):
            return true
    return false


## Spoken goods per seller, leaving out any the seller has a quote card for.
func spoken_goods() -> Array:
    var quoted := {}
    for q in visible_quotes():
        for item in _quote_items(q):
            quoted["%s|%s" % [str(q.get("seller", "")).to_lower(), item.to_lower()]] = true
    var out: Array = []
    for seller in recipients():
        var goods: Array = []
        for item in _mentions_of(seller):
            if not quoted.has("%s|%s" % [seller.to_lower(), item.to_lower()]):
                goods.append(item)
        if not goods.is_empty():
            out.append({"seller": seller, "items": goods})
    return out


func _rebuild_offers() -> void:
    for child in offers_box.get_children():
        offers_box.remove_child(child)
        child.queue_free()
    var any_choice := false
    for c in visible_counters():
        offers_box.add_child(_counter_card(c))
    for q in visible_quotes():
        offers_box.add_child(_quote_card(q))
        if _quote_items(q).size() == 1 and _dispo(str(q.get("item", ""))) == "choice":
            any_choice = true
    for g in spoken_goods():
        offers_box.add_child(_spoken_card(g))
    var any_card := offers_box.get_child_count() > 0
    offers_header.visible = any_card
    if not any_card:
        var lines := empty_lines()
        for i in lines.size():
            offers_box.add_child(_label(lines[i], 16 if i == 0 else 15, COLOR_TEXT if i == 0 else COLOR_DIM))
    offers_dispo_row.visible = any_choice
    # The empty box has no own-offer button: every good someone has named or
    # quoted is already a card here, so its What list would be empty (10-08).
    var anyone := not recipients().is_empty()
    own_offer_button.visible = anyone and any_card
    give_button.visible = anyone
    _fit_scroll.call_deferred()


## What the box says when nothing is on offer: the main line first, then the
## help. When the player's own innkeeper is here, a quiet last line says the
## room is already paid (LLM-38: the keeper "offers" a room the player holds,
## and the player looks here for it). The inn is not named — the keeper is
## right there, and the bare structure name reads "at Tavern".
func empty_lines() -> Array:
    var here := recipients()
    if here.is_empty():
        return ["There is nobody here to pay."]
    var lines: Array = ["Nobody here has offered you anything yet.", "Ask them what they sell — what they name will show here."]
    if host != null and typeof(host.pc_lodging) == TYPE_DICTIONARY and not host.pc_lodging.is_empty():
        var keeper := str(host.pc_lodging.get("keeper_name", ""))
        if keeper != "" and _has_name(here, keeper):
            var until := str(host.pc_lodging.get("until_label", ""))
            lines.append("Your room is paid %s." % until if until != "" else "Your room is paid.")
    return lines


func _counter_card(c: Dictionary) -> Control:
    var card := _card()
    var vb: VBoxContainer = card.get_child(0)
    vb.add_child(_label(_what(str(c.get("item", "")), int(c.get("qty", 1))), 17, COLOR_TEXT))
    vb.add_child(_label("%s asks %s. You offered %s." % [
        str(c.get("seller", "")), _coins(int(c.get("amount", 0))), _coins(int(c.get("original", 0)))],
        14, COLOR_DIM))
    vb.add_child(_card_buttons(
        "Pay %s" % _coins(int(c.get("amount", 0))), _on_counter_pay.bind(int(c.get("ledger_id", 0))),
        _on_counter_dismiss.bind(int(c.get("ledger_id", 0)))))
    return card


func _quote_card(q: Dictionary) -> Control:
    var card := _card()
    var vb: VBoxContainer = card.get_child(0)
    vb.add_child(_label(quote_what(q), 17, COLOR_TEXT))
    vb.add_child(_label(quote_sub(q), 14, COLOR_DIM))
    vb.add_child(_card_buttons(quote_button_text(q), _on_take.bind(q),
        _on_quote_dismiss.bind(int(q.get("quote_id", 0)))))
    return card


func _spoken_card(g: Dictionary) -> Control:
    var seller := str(g.get("seller", ""))
    var card := _card()
    var vb: VBoxContainer = card.get_child(0)
    vb.add_child(_label("%s spoke of" % seller, 17, COLOR_TEXT))
    vb.add_child(_label("Tap one to make an offer.", 14, COLOR_DIM))
    var flow := HFlowContainer.new()
    flow.add_theme_constant_override("h_separation", 6)
    flow.add_theme_constant_override("v_separation", 6)
    for item in g.get("items", []):
        var b := _chip(_good_chip_text(seller, str(item)))
        b.toggle_mode = false
        b.pressed.connect(_on_spoken_pressed.bind(seller, str(item)))
        flow.add_child(b)
    vb.add_child(flow)
    return card


func _card_buttons(primary_text: String, on_primary: Callable, on_dismiss: Callable) -> Control:
    var row := HBoxContainer.new()
    row.add_theme_constant_override("separation", 8)
    var buy := _button(primary_text, on_primary, true)
    buy.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.add_child(buy)
    row.add_child(_button("Not now", on_dismiss))
    return row


## The goods of a quote — every line of a bundle (LLM-101).
func _quote_items(q: Dictionary) -> Array:
    var out: Array = []
    var lines = q.get("lines", [])
    if typeof(lines) == TYPE_ARRAY:
        for ln in lines:
            if typeof(ln) == TYPE_DICTIONARY and str(ln.get("item", "")) != "":
                out.append(str(ln.get("item", "")))
    if out.is_empty() and str(q.get("item", "")) != "":
        out.append(str(q.get("item", "")))
    return out


## "Stew", "2× bowl of stew", "2× Blueberries + 2× Raspberries".
func quote_what(q: Dictionary) -> String:
    var parts: Array = []
    var lines = q.get("lines", [])
    if typeof(lines) == TYPE_ARRAY:
        for ln in lines:
            if typeof(ln) != TYPE_DICTIONARY:
                continue
            var label := str(ln.get("display_label", ""))
            if label == "":
                label = item_label(str(ln.get("item", "")))
            parts.append(_qty_phrase(label, int(ln.get("qty", 1))))
    if parts.is_empty():
        var label := str(q.get("display_label", ""))
        if label == "":
            label = item_label(str(q.get("item", "")))
        parts.append(_qty_phrase(label, int(q.get("qty", 1))))
    return " + ".join(parts)


## "from John Ellis · to eat here · for you". A choice-class good carries no
## disposition — the have-it-here choice below the cards governs it.
func quote_sub(q: Dictionary) -> String:
    var parts: Array = ["from %s" % str(q.get("seller", ""))]
    var item := str(q.get("item", ""))
    var bundle := _quote_items(q).size() > 1
    var dispo := _dispo(item)
    if bundle:
        parts.append("to eat here" if bool(q.get("consume_now", false)) else "to take home")
    elif item.to_lower() == "nights_stay" or dispo == "tonight":
        parts.append("for tonight")
    elif dispo == "eat_here":
        parts.append("to eat here")
    elif dispo == "take_home":
        parts.append("to take home")
    elif dispo == "":
        parts.append("to eat here" if bool(q.get("consume_now", false)) else "to take home")
    if bool(q.get("targeted", false)):
        parts.append("for you")
    return " · ".join(parts)


func quote_button_text(q: Dictionary) -> String:
    var verb := "Book" if str(q.get("item", "")).to_lower() == "nights_stay" else "Buy"
    return "%s — %s" % [verb, _coins(int(q.get("amount", 0)))]


func _good_chip_text(seller: String, item: String) -> String:
    var unit := unit_price(seller, item)
    if unit > 0:
        return "%s · %d each" % [item_label(item), unit]
    return item_label(item)


func _on_take(q: Dictionary) -> void:
    if int(q.get("amount", 0)) > _coins_held():
        _set_status("You only have %s." % _coins(_coins_held()))
        return
    _post_pay(take_body(q), "take", str(q.get("seller", "")), 0)


func _on_counter_pay(ledger_id: int) -> void:
    var c: Dictionary = counters.get(ledger_id, {})
    if c.is_empty():
        return
    if int(c.get("amount", 0)) > _coins_held():
        _set_status("You only have %s." % _coins(_coins_held()))
        return
    _post_pay(counter_body(c), "counter", str(c.get("seller", "")), ledger_id)


func _on_quote_dismiss(quote_id: int) -> void:
    if quote_id != 0:
        dismissed_quotes[quote_id] = true
    _rebuild_offers()


func _on_counter_dismiss(ledger_id: int) -> void:
    counters.erase(ledger_id)
    _rebuild_offers()


func _on_spoken_pressed(seller: String, item: String) -> void:
    start_offer(seller, item)


func _on_own_offer_pressed() -> void:
    start_offer("", "")


func _on_give_pressed() -> void:
    start_give("")


# --- offer page ----------------------------------------------------------------

## Open the offer page. An empty seller keeps the last one if still here, else
## the first person here; an empty item takes the seller's latest single
## mention, else their first good.
func start_offer(seller: String, item: String) -> void:
    var here := recipients()
    if seller == "" or not _has_name(here, seller):
        seller = sel_seller if _has_name(here, sel_seller) else (str(here[0]) if not here.is_empty() else "")
    _set_status("")
    _select_seller(seller, item)
    _show_page(Page.OFFER)


func _select_seller(seller: String, item: String = "") -> void:
    sel_seller = seller
    var goods := goods_of(seller)
    if item == "":
        item = _latest_single_mention(seller)
    if item == "" or not _has_name(goods, item):
        item = str(goods[0]) if not goods.is_empty() else ""
    _select_item(_name_in(goods, item))


func _select_item(item: String) -> void:
    sel_item = item
    qty = 1
    var unit := unit_price(sel_seller, item)
    amount = unit if unit > 0 else 1
    days_ahead = 0


static func _name_in(names: Array, n: String) -> String:
    for x in names:
        if _same(str(x), n):
            return str(x)
    return n


func _latest_single_mention(seller: String) -> String:
    if host == null:
        return ""
    var m = host.vendor_latest_mentions.get(seller, [])
    if (typeof(m) == TYPE_ARRAY or typeof(m) == TYPE_PACKED_STRING_ARRAY) and m.size() == 1:
        return str(m[0])
    return ""


## What the seller can be offered for: what they have spoken of, plus what
## they have a posted quote for (a quote is an offer too — the old box left
## those out and said "hasn't named anything for sale").
func goods_of(seller: String) -> Array:
    var out: Array = []
    for item in _mentions_of(seller):
        out.append(item)
    for q in visible_quotes():
        if not _same(str(q.get("seller", "")), seller):
            continue
        for item in _quote_items(q):
            if not _has_name(out, item):
                out.append(item)
    return out


## The seller's price for one unit: a single-good quote's price per unit when
## it divides evenly, else the price heard in conversation, else 0 (unknown).
func unit_price(seller: String, item: String) -> int:
    for q in visible_quotes():
        if not _same(str(q.get("seller", "")), seller):
            continue
        var items := _quote_items(q)
        if items.size() != 1 or not _same(str(items[0]), item):
            continue
        var n := maxi(int(q.get("qty", 1)), 1)
        var total := int(q.get("amount", 0))
        if total > 0 and total % n == 0:
            return total / n
    return _heard_price(seller, item)


func send_text() -> String:
    if sel_seller == "":
        return "Make an offer"
    if sel_item == "":
        return "Pick what you want"
    return "Offer %s %s for %s" % [sel_seller, _coins(amount), _what(sel_item, qty)]


func price_hint_text() -> String:
    if sel_item == "":
        return ""
    if amount > _coins_held():
        return "You only have %s." % _coins(_coins_held())
    var unit := unit_price(sel_seller, sel_item)
    if unit <= 0:
        return "They haven't named a price. They may say no, or ask for more."
    var ask := unit * qty
    if amount == ask:
        return "%s in all — the asking price." % _coins(amount)
    if amount < ask:
        return "Less than the asking price of %s. They may say no." % _coins(ask)
    return "More than the asking price of %s." % _coins(ask)


func _rebuild_offer_page() -> void:
    _reconcile_offer()
    _fill_chips(who_flow, recipients(), sel_seller, func(n: String) -> String: return n, _on_who)
    var goods := goods_of(sel_seller)
    _fill_chips(what_flow, goods, sel_item, func(n: String) -> String: return _good_chip_text(sel_seller, n), _on_what)
    what_flow.visible = not goods.is_empty()
    what_empty_label.visible = goods.is_empty() and sel_seller != ""
    what_empty_label.text = "%s hasn't named anything for sale yet. Ask them in the talk box." % sel_seller
    _sync_offer_values()


## The chosen seller left, or the chosen good is no longer offered (a room or
## roster change, a quote gone): choose again rather than keep a stale deal.
## A still-valid choice is left as it is, with its amounts.
func _reconcile_offer() -> void:
    var here := recipients()
    if not _has_name(here, sel_seller):
        _select_seller(str(here[0]) if not here.is_empty() else "")
        return
    var goods := goods_of(sel_seller)
    if sel_item == "" and goods.is_empty():
        return
    if not _has_name(goods, sel_item):
        _select_seller(sel_seller)


## The parts that change on every tap, without rebuilding the chips.
func _sync_offer_values() -> void:
    _set_stepper(qty_stepper, qty)
    _set_stepper(amount_stepper, amount)
    _set_stepper(night_stepper, days_ahead)
    var booking := sel_item.to_lower() == "nights_stay"
    night_row.visible = booking
    offer_dispo_row.visible = sel_item != "" and not booking and not (_dispo(sel_item) in ["eat_here", "take_home"])
    price_hint.text = price_hint_text()
    price_hint.add_theme_color_override("font_color", COLOR_ERROR if amount > _coins_held() else COLOR_DIM)
    price_hint.visible = price_hint.text != ""
    send_button.text = send_text()


func _on_who(n: String) -> void:
    _select_seller(n)
    _rebuild_offer_page()


func _on_what(n: String) -> void:
    _select_item(n)
    _rebuild_offer_page()


func _on_qty(v: int) -> void:
    qty = v
    var unit := unit_price(sel_seller, sel_item)
    if unit > 0:
        amount = clampi(unit * qty, 1, 999)
    _sync_offer_values()


func _on_amount(v: int) -> void:
    amount = v
    _sync_offer_values()


func _on_night(v: int) -> void:
    days_ahead = v
    _sync_offer_values()


func _on_send() -> void:
    var chosen := [sel_seller, sel_item]
    _reconcile_offer()
    if [sel_seller, sel_item] != chosen:
        # Changed under the player: show the new choice, send nothing yet.
        _rebuild_offer_page()
        _set_status("That is no longer on offer. Check your offer and send again.")
        return
    if sel_seller == "":
        _set_status("There is nobody here to pay.")
        return
    if sel_item == "":
        _set_status("Pick what you want first.")
        return
    if amount > _coins_held():
        _set_status("You only have %s." % _coins(_coins_held()))
        return
    _post_pay(offer_body(), "offer", sel_seller, 0)


# --- give page -----------------------------------------------------------------

## Open the give page. An empty name keeps the last one if still here, else the
## first person here. The "what for" line starts empty on each open.
func start_give(to: String) -> void:
    var here := recipients()
    if to == "" or not _has_name(here, to):
        to = give_to if _has_name(here, give_to) else (str(here[0]) if not here.is_empty() else "")
    give_to = _name_in(here, to)
    give_amount = 1
    give_for_field.text = ""
    _set_status("")
    _show_page(Page.GIVE)


## {recipient, amount} plus "for" when the player wrote one.
func give_body() -> Dictionary:
    var body := {"recipient": give_to, "amount": give_amount}
    var why := " ".join(give_for_field.text.split(" ", false)).strip_edges()
    if why != "":
        body["for"] = why
    return body


func give_text() -> String:
    if give_to == "":
        return "Give coins"
    return "Give %s %s" % [give_to, _coins(give_amount)]


func _rebuild_give_page() -> void:
    var here := recipients()
    if not _has_name(here, give_to):
        give_to = str(here[0]) if not here.is_empty() else ""
    _fill_chips(give_who_flow, here, give_to, func(n: String) -> String: return n, _on_give_who)
    _sync_give_values()


func _sync_give_values() -> void:
    _set_stepper(give_amount_stepper, give_amount)
    var short := give_amount > _coins_held()
    give_hint.text = "You only have %s." % _coins(_coins_held()) if short else "They keep it. Nothing is bought."
    give_hint.add_theme_color_override("font_color", COLOR_ERROR if short else COLOR_DIM)
    give_send_button.text = give_text()


func _on_give_who(n: String) -> void:
    give_to = n
    _rebuild_give_page()


func _on_give_amount(v: int) -> void:
    give_amount = v
    _sync_give_values()


func _on_give_send() -> void:
    var chosen := give_to
    _rebuild_give_page()
    if give_to != chosen:
        _set_status("%s has gone. Check who you give to and send again." % chosen if chosen != "" else "There is nobody here to give to.")
        return
    if give_to == "":
        _set_status("There is nobody here to give to.")
        return
    if give_amount > _coins_held():
        _set_status("You only have %s." % _coins(_coins_held()))
        return
    if _busy:
        _set_status("Your last offer is still on its way.")
        return
    _busy = true
    _busy_kind = "give"
    _busy_seller = give_to
    _busy_counter = give_amount
    _busy_gen = _open_gen
    _set_status("Sending…")
    if not _send("give", give_body()):
        _busy = false
        _set_status("The coins could not be sent. Try again.")


# --- sending -------------------------------------------------------------------

func _post_pay(body: Dictionary, kind: String, seller: String, counter_id: int) -> void:
    if _busy:
        _set_status("Your last offer is still on its way.")
        return
    _busy = true
    _busy_kind = kind
    _busy_seller = seller
    _busy_counter = counter_id
    _busy_gen = _open_gen
    _set_status("Sending…")
    if not _send("pay", body):
        _busy = false
        _set_status("The offer could not be sent. Try again.")


func _send(route: String, body: Dictionary = {}) -> bool:
    if send_hook.is_valid():
        send_hook.call(route, body)
        return true
    _ensure_http()
    match route:
        "pay":
            return _http_pay.request(Auth.api_base + "/api/village/pc/pay",
                Auth.auth_headers(), HTTPClient.METHOD_POST, JSON.stringify(body)) == OK
        "give":
            return _http_give.request(Auth.api_base + "/api/village/pc/give",
                Auth.auth_headers(), HTTPClient.METHOD_POST, JSON.stringify(body)) == OK
        "quotes":
            # An older answer must never fill a newer open's cards.
            if _http_quotes.get_http_client_status() != HTTPClient.STATUS_DISCONNECTED:
                _http_quotes.cancel_request()
            return _http_quotes.request(Auth.api_base + "/api/village/pc/quotes",
                Auth.auth_headers(false), HTTPClient.METHOD_GET) == OK
        "items":
            return _http_items.request(Auth.api_base + "/api/village/items",
                Auth.auth_headers(false), HTTPClient.METHOD_GET) == OK
    return false


func _ensure_http() -> void:
    if _http_pay != null:
        return
    _http_pay = HTTPRequest.new()
    _http_pay.timeout = 15.0
    add_child(_http_pay)
    _http_pay.request_completed.connect(_on_pay_response)
    _http_quotes = HTTPRequest.new()
    _http_quotes.timeout = 15.0
    add_child(_http_quotes)
    _http_quotes.request_completed.connect(_on_quotes_response)
    _http_items = HTTPRequest.new()
    _http_items.timeout = 15.0
    add_child(_http_items)
    _http_items.request_completed.connect(_on_items_response)
    _http_give = HTTPRequest.new()
    _http_give.timeout = 15.0
    add_child(_http_give)
    _http_give.request_completed.connect(_on_give_response)


## Fetched once: names, labels and disposition classes are boot-fixed server
## side. A failed fetch retries on the next open.
func _ensure_catalog() -> void:
    if _catalog_requested:
        return
    _catalog_requested = _send("items")


## The body as JSON, or null. A JSON instance parses quietly; the static
## JSON.parse_string logs an engine error for an empty or broken body.
static func _parse(body: PackedByteArray) -> Variant:
    var json := JSON.new()
    if json.parse(body.get_string_from_utf8()) != OK:
        return null
    return json.data


func _on_pay_response(result: int, code: int, _h: PackedStringArray, body: PackedByteArray) -> void:
    # Only the pay this box sent; a late answer after a reset is dropped.
    if not _busy or _busy_kind == "give":
        return
    var kind := _busy_kind
    _busy = false
    _busy_kind = ""
    if result != HTTPRequest.RESULT_SUCCESS:
        _set_status("The offer did not reach them. Try again.")
        return
    var parsed = _parse(body)
    if code < 200 or code >= 300:
        var msg := "Something went wrong (%d)." % code
        if typeof(parsed) == TYPE_DICTIONARY and str(parsed.get("error", "")) != "":
            msg = _sentence(str(parsed.get("error", "")))
        _set_status(msg)
        # A refused take means the card went stale (taken, expired, moved on).
        if kind == "take":
            _send("quotes")
        return
    # pc/pay answers {ledger_id, state, fast_path}; without a state the
    # outcome is unknown, so the box stays open rather than claim a pay.
    var state := ""
    if typeof(parsed) == TYPE_DICTIONARY:
        var raw_state = parsed.get("state", null)
        if typeof(raw_state) == TYPE_STRING:
            state = raw_state.strip_edges()
    if state == "":
        _set_status("The answer was unclear. Check the talk log before you pay again.")
        return
    if kind == "counter":
        counters.erase(_busy_counter)
    if state == "pending":
        offer_pending.emit(_busy_seller)
    # The player may have closed the box and opened it again since; that
    # newer open stays.
    if _busy_gen == _open_gen:
        close()
    paid.emit()


## pc/give answers {recipient, amount, coins}. The gift is final on any 2xx —
## coins move at once, nothing waits on an answer.
func _on_give_response(result: int, code: int, _h: PackedStringArray, body: PackedByteArray) -> void:
    if not _busy or _busy_kind != "give":
        return
    var to := _busy_seller
    var amount_given := _busy_counter
    _busy = false
    _busy_kind = ""
    if result != HTTPRequest.RESULT_SUCCESS:
        # The engine may have moved the coins before the answer was lost: re-read
        # the purse so the count is right before the player tries again.
        _set_status("No answer came back. Check your coins and the talk log before you give again.")
        paid.emit()
        return
    if code < 200 or code >= 300:
        var parsed = _parse(body)
        var msg := "Something went wrong (%d)." % code
        if typeof(parsed) == TYPE_DICTIONARY and str(parsed.get("error", "")) != "":
            msg = _sentence(str(parsed.get("error", "")))
        _set_status(msg)
        return
    if _busy_gen == _open_gen:
        close()
    gave.emit(to, amount_given)
    paid.emit()


func _on_quotes_response(result: int, code: int, _h: PackedStringArray, body: PackedByteArray) -> void:
    if result != HTTPRequest.RESULT_SUCCESS or code != 200:
        return
    var parsed = _parse(body)
    if typeof(parsed) != TYPE_DICTIONARY or typeof(parsed.get("quotes", null)) != TYPE_ARRAY:
        return
    quotes = parsed["quotes"]
    refresh()


func _on_items_response(result: int, code: int, _h: PackedStringArray, body: PackedByteArray) -> void:
    var parsed = _parse(body) if result == HTTPRequest.RESULT_SUCCESS and code == 200 else null
    if typeof(parsed) != TYPE_ARRAY:
        _catalog_requested = false
        return
    set_catalog(parsed)


func set_catalog(entries: Array) -> void:
    _catalog_requested = true
    catalog_labels.clear()
    catalog_dispo.clear()
    for e in entries:
        if typeof(e) != TYPE_DICTIONARY:
            continue
        var n := str(e.get("name", "")).strip_edges()
        if n == "":
            continue
        var label := str(e.get("display_label", "")).strip_edges()
        catalog_labels[n.to_lower()] = label if label != "" else n
        var d := str(e.get("disposition", "")).strip_edges()
        if d != "":
            catalog_dispo[n.to_lower()] = d
    refresh()


# --- building ------------------------------------------------------------------

func _build_ui() -> void:
    root = Control.new()
    root.set_anchors_preset(Control.PRESET_FULL_RECT)
    root.mouse_filter = Control.MOUSE_FILTER_STOP
    add_child(root)

    var backdrop := ColorRect.new()
    backdrop.color = Color(0, 0, 0, 0.5)
    backdrop.set_anchors_preset(Control.PRESET_FULL_RECT)
    backdrop.mouse_filter = Control.MOUSE_FILTER_STOP
    root.add_child(backdrop)

    var center := CenterContainer.new()
    center.set_anchors_preset(Control.PRESET_FULL_RECT)
    center.mouse_filter = Control.MOUSE_FILTER_IGNORE
    root.add_child(center)

    sheet = PanelContainer.new()
    sheet.custom_minimum_size = Vector2(360, 0)
    sheet.theme = _build_theme()
    center.add_child(sheet)

    var pad := MarginContainer.new()
    for side in ["margin_left", "margin_right"]:
        pad.add_theme_constant_override(side, 16)
    pad.add_theme_constant_override("margin_top", 14)
    pad.add_theme_constant_override("margin_bottom", 14)
    sheet.add_child(pad)

    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 10)
    pad.add_child(vb)

    var header := HBoxContainer.new()
    title_label = _label("Pay", 22, COLOR_TITLE)
    title_label.autowrap_mode = TextServer.AUTOWRAP_OFF
    title_label.custom_minimum_size = Vector2.ZERO
    title_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    header.add_child(title_label)
    coins_label = _label("", 15, COLOR_DIM)
    coins_label.autowrap_mode = TextServer.AUTOWRAP_OFF
    coins_label.custom_minimum_size = Vector2.ZERO
    coins_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
    header.add_child(coins_label)
    vb.add_child(header)

    _build_offers_page(vb)
    _build_offer_page(vb)
    _build_give_page(vb)

    status_label = _label("", 15, COLOR_ERROR)
    vb.add_child(status_label)
    status_label.visible = false


## The status line takes no room while empty.
func _set_status(text: String) -> void:
    status_label.text = text
    status_label.visible = text != ""


func _build_offers_page(parent: Control) -> void:
    offers_page = VBoxContainer.new()
    offers_page.add_theme_constant_override("separation", 10)
    parent.add_child(offers_page)
    offers_header = _label("They offer you", 15, COLOR_DIM)
    offers_page.add_child(offers_header)

    offers_scroll = ScrollContainer.new()
    offers_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    offers_page.add_child(offers_scroll)
    offers_box = VBoxContainer.new()
    offers_box.add_theme_constant_override("separation", 8)
    offers_box.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    offers_scroll.add_child(offers_box)
    offers_box.minimum_size_changed.connect(func(): _fit_scroll.call_deferred())

    offers_dispo_row = _dispo_row()
    offers_page.add_child(offers_dispo_row)

    # Own offer on its own row: hidden in the empty box, which then shows just
    # Give coins and Close.
    own_offer_button = _button("Make your own offer", _on_own_offer_pressed)
    offers_page.add_child(own_offer_button)
    var buttons := HBoxContainer.new()
    buttons.add_theme_constant_override("separation", 8)
    offers_page.add_child(buttons)
    give_button = _button("Give coins", _on_give_pressed)
    give_button.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    buttons.add_child(give_button)
    buttons.add_child(_button("Close", close))


func _build_offer_page(parent: Control) -> void:
    offer_page = VBoxContainer.new()
    offer_page.add_theme_constant_override("separation", 8)
    parent.add_child(offer_page)

    offer_page.add_child(_label("Who", 15, COLOR_DIM))
    who_flow = _flow()
    offer_page.add_child(who_flow)

    offer_page.add_child(_label("What", 15, COLOR_DIM))
    what_flow = _flow()
    offer_page.add_child(what_flow)
    what_empty_label = _label("", 15, COLOR_ERROR)
    offer_page.add_child(what_empty_label)

    var nums := HBoxContainer.new()
    nums.add_theme_constant_override("separation", 12)
    offer_page.add_child(nums)
    qty_stepper = _stepper(1, 99, _on_qty)
    nums.add_child(_titled("How many", qty_stepper["row"]))
    amount_stepper = _stepper(1, 999, _on_amount)
    nums.add_child(_titled("You pay (coins)", amount_stepper["row"]))

    price_hint = _label("", 14, COLOR_DIM)
    offer_page.add_child(price_hint)

    offer_dispo_row = _dispo_row()
    offer_page.add_child(offer_dispo_row)

    night_stepper = _stepper(0, 30, _on_night, func(v: int) -> String:
        return "Tonight" if v == 0 else ("In 1 day" if v == 1 else "In %d days" % v))
    night_row = _titled("Which night", night_stepper["row"])
    offer_page.add_child(night_row)

    send_button = _button("Make an offer", _on_send, true)
    send_button.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    offer_page.add_child(send_button)
    offer_page.add_child(_label("They answer in a moment. They may say no, or ask for more.", 14, COLOR_DIM))

    var back := _button("Back", func(): _show_page(Page.OFFERS))
    back.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
    offer_page.add_child(back)


func _build_give_page(parent: Control) -> void:
    give_page = VBoxContainer.new()
    give_page.add_theme_constant_override("separation", 8)
    parent.add_child(give_page)

    give_page.add_child(_label("Who", 15, COLOR_DIM))
    give_who_flow = _flow()
    give_page.add_child(give_who_flow)

    give_amount_stepper = _stepper(1, 999, _on_give_amount)
    give_page.add_child(_titled("How much (coins)", give_amount_stepper["row"]))

    give_for_field = LineEdit.new()
    give_for_field.max_length = GIVE_FOR_MAX
    give_for_field.placeholder_text = "A gift, a debt paid back…"
    give_for_field.custom_minimum_size = Vector2(0, TAP_H)
    give_page.add_child(_titled("What for? (if you like)", give_for_field))

    give_send_button = _button("Give coins", _on_give_send, true)
    give_send_button.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    give_page.add_child(give_send_button)
    give_hint = _label("They keep it. Nothing is bought.", 14, COLOR_DIM)
    give_page.add_child(give_hint)

    var back := _button("Back", func(): _show_page(Page.OFFERS))
    back.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
    give_page.add_child(back)


func _show_page(p: Page) -> void:
    page = p
    offers_page.visible = p == Page.OFFERS
    offer_page.visible = p == Page.OFFER
    give_page.visible = p == Page.GIVE
    match p:
        Page.OFFERS:
            title_label.text = "Pay"
        Page.OFFER:
            title_label.text = "Make an offer"
        Page.GIVE:
            title_label.text = "Give coins"
    _sync_dispo_buttons()
    _rebuild_coins()
    match p:
        Page.OFFERS:
            _rebuild_offers()
        Page.OFFER:
            _rebuild_offer_page()
        Page.GIVE:
            _rebuild_give_page()


func _rebuild_coins() -> void:
    coins_label.text = "You have %s" % _coins(_coins_held())


## The cards scroll once they would pass about half the screen.
func _fit_scroll() -> void:
    if offers_scroll == null or offers_box == null:
        return
    var cap := 330.0
    var vp := get_viewport()
    if vp != null:
        cap = maxf(160.0, vp.get_visible_rect().size.y * 0.55)
    offers_scroll.custom_minimum_size.y = minf(offers_box.get_combined_minimum_size().y, cap)


## "Have it here, or take it home?" — one choice, shown on both pages.
func _dispo_row() -> Control:
    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 4)
    vb.add_child(_label("Have it here, or take it home?", 15, COLOR_DIM))
    var row := HBoxContainer.new()
    row.add_theme_constant_override("separation", 0)
    var group := ButtonGroup.new()
    group.allow_unpress = false
    var here := _chip("Have it here")
    var home := _chip("Take it home")
    for b in [here, home]:
        b.button_group = group
        b.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        row.add_child(b)
    here.pressed.connect(_set_eat_here.bind(true))
    home.pressed.connect(_set_eat_here.bind(false))
    vb.add_child(row)
    _dispo_buttons.append([here, home])
    return vb


func _set_eat_here(v: bool) -> void:
    eat_here = v
    _sync_dispo_buttons()


func _sync_dispo_buttons() -> void:
    for pair in _dispo_buttons:
        pair[0].set_pressed_no_signal(eat_here)
        pair[1].set_pressed_no_signal(not eat_here)


## One chip per name; the selected one shows pressed. Rebuilt on each change.
func _fill_chips(flow: HFlowContainer, names: Array, selected: String, text_of: Callable, on_pick: Callable) -> void:
    for child in flow.get_children():
        flow.remove_child(child)
        child.queue_free()
    var group := ButtonGroup.new()
    group.allow_unpress = false
    for n in names:
        var b := _chip(text_of.call(str(n)))
        b.button_group = group
        b.set_pressed_no_signal(_same(str(n), selected))
        b.pressed.connect(on_pick.bind(str(n)))
        flow.add_child(b)


func _stepper(lo: int, hi: int, on_change: Callable, fmt: Callable = Callable()) -> Dictionary:
    var row := HBoxContainer.new()
    row.add_theme_constant_override("separation", 4)
    # An en dash, not U+2212: IM Fell has no minus sign, and the web build
    # has no system font to fall back on (it drew a box).
    var minus := _button("–", Callable())
    var plus := _button("+", Callable())
    for b in [minus, plus]:
        b.custom_minimum_size = Vector2(TAP_H, TAP_H)
    var field := LineEdit.new()
    field.alignment = HORIZONTAL_ALIGNMENT_CENTER
    field.custom_minimum_size = Vector2(56, TAP_H)
    field.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    field.editable = not fmt.is_valid()
    field.virtual_keyboard_type = LineEdit.KEYBOARD_TYPE_NUMBER
    row.add_child(minus)
    row.add_child(field)
    row.add_child(plus)
    var s := {"row": row, "field": field, "lo": lo, "hi": hi, "fmt": fmt, "value": lo}
    minus.pressed.connect(func(): on_change.call(clampi(int(s["value"]) - 1, lo, hi)))
    plus.pressed.connect(func(): on_change.call(clampi(int(s["value"]) + 1, lo, hi)))
    # Typed numbers apply as they are typed; a cleared field waits, and
    # leaving the field puts the current value back.
    field.text_changed.connect(func(t: String):
        if t.is_valid_int():
            var v := clampi(t.to_int(), lo, hi)
            s["value"] = v
            on_change.call(v))
    field.focus_exited.connect(func(): _set_stepper(s, int(s["value"])))
    return s


func _set_stepper(s: Dictionary, v: int) -> void:
    if s.is_empty():
        return
    s["value"] = v
    var field: LineEdit = s["field"]
    var fmt: Callable = s["fmt"]
    var text := str(fmt.call(v)) if fmt.is_valid() else str(v)
    # Don't fight the player while they type: a focused field that holds the
    # same number is left alone, and so is any focused field during a
    # background refresh (it may be cleared mid-edit) — focus leaving puts the
    # value back. A tap (− / +, How many re-pricing) always shows its number.
    var editing := field.has_focus() and (_refreshing or (field.text.is_valid_int() and field.text.to_int() == v))
    if field.text != text and not editing:
        field.text = text


func _titled(text: String, control: Control) -> Control:
    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 4)
    vb.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    var l := _label(text, 15, COLOR_DIM)
    l.custom_minimum_size = Vector2.ZERO
    vb.add_child(l)
    vb.add_child(control)
    return vb


func _flow() -> HFlowContainer:
    var f := HFlowContainer.new()
    f.add_theme_constant_override("h_separation", 6)
    f.add_theme_constant_override("v_separation", 6)
    return f


func _card() -> PanelContainer:
    var card := PanelContainer.new()
    var st := StyleBoxFlat.new()
    st.bg_color = Color(0.16, 0.12, 0.08, 1.0)
    st.border_color = Color(0.42, 0.32, 0.19, 1.0)
    st.set_border_width_all(1)
    st.set_corner_radius_all(6)
    st.content_margin_left = 10
    st.content_margin_right = 10
    st.content_margin_top = 8
    st.content_margin_bottom = 10
    card.add_theme_stylebox_override("panel", st)
    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 4)
    card.add_child(vb)
    return card


func _label(text: String, size: int, color: Color) -> Label:
    var l := Label.new()
    l.text = text
    l.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    l.custom_minimum_size = Vector2(300, 0)
    l.add_theme_font_size_override("font_size", OrientationGuard.text_size(size))
    l.add_theme_color_override("font_color", color)
    return l


func _button(text: String, cb: Callable, primary: bool = false) -> Button:
    var b := PeriodTheme.button(text, OrientationGuard.text_size(PeriodTheme.BUTTON_TEXT))
    if primary:
        PeriodTheme.make_primary(b)
    if cb.is_valid():
        b.pressed.connect(cb)
    return b


func _chip(text: String) -> Button:
    var b := Button.new()
    b.text = text
    b.toggle_mode = true
    b.focus_mode = Control.FOCUS_NONE
    b.custom_minimum_size = Vector2(0, 38)
    b.add_theme_font_size_override("font_size", OrientationGuard.text_size(16))
    var on := _box(Color(0.30, 0.21, 0.10), Color(0.77, 0.60, 0.33))
    b.add_theme_stylebox_override("pressed", on)
    b.add_theme_stylebox_override("hover_pressed", on)
    b.add_theme_color_override("font_pressed_color", Color(1.0, 0.95, 0.85))
    b.add_theme_color_override("font_hover_pressed_color", Color(1.0, 0.95, 0.85))
    return b


static func _box(fill: Color, stroke: Color) -> StyleBoxFlat:
    return PeriodTheme.box(fill, stroke)


## Brown period theme (PeriodTheme) — the repair and notice panels' palette,
## IM Fell for all text, so the modals match.
func _build_theme() -> Theme:
    return PeriodTheme.build(_font, OrientationGuard.text_size(16))


func _unhandled_input(event: InputEvent) -> void:
    if not visible or not event.is_action_pressed("ui_cancel"):
        return
    get_viewport().set_input_as_handled()
    if page != Page.OFFERS:
        _show_page(Page.OFFERS)
    else:
        close()


# --- words ---------------------------------------------------------------------

static func _coins(n: int) -> String:
    return "1 coin" if n == 1 else "%d coins" % n


## "stew" / "2× bowl of stew".
func _what(item: String, n: int) -> String:
    return _qty_phrase(item_label(item), n)


static func _qty_phrase(label: String, n: int) -> String:
    if n > 1:
        return "%d× %s" % [n, _strip_article(label)]
    return label


## "2× bowl of stew", not "2× a bowl of stew".
static func _strip_article(label: String) -> String:
    var lower := label.to_lower()
    for a in ["a ", "an ", "the "]:
        if lower.begins_with(a):
            return label.substr(a.length())
    return label


## Engine refusal text arrives lower-case and sometimes without a stop.
static func _sentence(s: String) -> String:
    s = s.strip_edges()
    if s == "":
        return s
    s = s[0].to_upper() + s.substr(1)
    if not (s.ends_with(".") or s.ends_with("!") or s.ends_with("?")):
        s += "."
    return s
