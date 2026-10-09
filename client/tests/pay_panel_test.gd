extends SceneTree

## Headless harness for the Pay box (LLM-722, pay_panel.gd): the offer cards,
## the own-offer page, and the pc/pay bodies each button sends.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/pay_panel_test.gd
## Exits 0 when every check passes, 1 if any check fails.

## Loaded at run time, not preloaded: pay_panel.gd names the OrientationGuard
## and Auth autoloads, which exist only once the tree is up.
var PanelScript: GDScript = null

const TESTS := [
    "_test_quote_card_names_the_price",
    "_test_quoted_good_is_on_the_offer_page",
    "_test_take_body_follows_the_good",
    "_test_offer_body_follows_the_good",
    "_test_how_many_scales_the_price",
    "_test_counter_card_answers_the_counter",
    "_test_spoken_goods_skip_quoted_ones",
    "_test_buy_sends_and_closes",
    "_test_one_pay_at_a_time",
    "_test_refused_take_refetches",
    "_test_empty_text",
    "_test_not_now_hides_a_quote",
    "_test_pages_build",
    "_test_seller_gone_hides_cards",
    "_test_counter_stays_gone_when_seller_returns",
    "_test_offer_page_follows_the_roster",
    "_test_reopen_during_pay_keeps_the_new_box",
    "_test_unclear_answer_keeps_the_box_open",
    "_test_cleared_field_survives_refresh",
    "_test_empty_box_buttons",
    "_test_give_body_and_button",
    "_test_give_lists_a_player",
    "_test_give_sends_and_closes",
    "_test_give_refused_stays_open",
    "_test_give_lost_answer_rereads_purse",
    "_test_give_checks_the_purse",
    "_test_give_follows_the_roster",
    "_test_every_glyph_is_in_the_font",
]

var _checks := 0
var _failures := 0
var _current := ""
var _completed := {}


class FakeHost extends Node:
    var huddle_members: Array = []
    var character_name := "Mary Pell"
    var pc_actor_id := "pc-1"
    var pc_coins := 20
    var pc_lodging: Dictionary = {}
    var vendor_mentions: Dictionary = {}
    var vendor_mention_prices: Dictionary = {}
    var vendor_latest_mentions: Dictionary = {}


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    PanelScript = load("res://scripts/pay_panel.gd")
    _check_test_list()
    for t in TESTS:
        _current = t
        call(t)
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)
    print("\n[pay_panel_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[pay_panel_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s — %s: got %s, want %s" % [_current, label, got, want])


func _done() -> void:
    _completed[_current] = true


func _check_test_list() -> void:
    var methods := {}
    for m in get_method_list():
        var n: String = m["name"]
        if n.begins_with("_test_"):
            methods[n] = true
    var seen := {}
    for t in TESTS:
        _check("harness — %s is a method" % t, methods.has(t), true)
        _check("harness — %s listed once" % t, seen.has(t), false)
        seen[t] = true
    for n in methods:
        _check("harness — %s is in TESTS" % n, seen.has(n), true)


# --- fixtures ------------------------------------------------------------------

const STEW_QUOTE := {"quote_id": 7, "seller": "John Ellis", "item": "stew", "display_label": "Stew",
    "qty": 1, "amount": 6, "consume_now": true, "targeted": true,
    "lines": [{"item": "stew", "display_label": "Stew", "qty": 1}]}

const CATALOG := [
    {"name": "stew", "display_label": "Stew", "disposition": "eat_here"},
    {"name": "ale", "display_label": "Ale", "disposition": "choice"},
    {"name": "bread", "display_label": "a loaf of bread", "disposition": "choice"},
    {"name": "nights_stay", "display_label": "a night's stay", "disposition": "tonight"},
]


## A panel in the tree, John Ellis and Hannah Boggs in the huddle, the catalog
## loaded, every request recorded in `sent` instead of made.
func _panel(sent: Array) -> CanvasLayer:
    var host := FakeHost.new()
    host.huddle_members = [{"name": "John Ellis"}, {"name": "Hannah Boggs"}, {"name": "Mary Pell"}]
    root.add_child(host)
    var p: CanvasLayer = PanelScript.new()
    p.host = host
    p.send_hook = func(route: String, body: Dictionary): sent.append([route, body])
    root.add_child(p)
    p.set_catalog(CATALOG)
    return p


func _free(p: CanvasLayer) -> void:
    p.host.free()
    p.free()


func _routes(sent: Array) -> Array:
    return sent.map(func(s): return s[0])


func _ok(state: String) -> PackedByteArray:
    return JSON.stringify({"ledger_id": 9, "state": state, "fast_path": state == "accepted"}).to_utf8_buffer()


# --- tests ---------------------------------------------------------------------

func _test_quote_card_names_the_price() -> void:
    var p := _panel([])
    p.quotes = [STEW_QUOTE]
    _check("one quote card", p.visible_quotes().size(), 1)
    _check("what", p.quote_what(STEW_QUOTE), "Stew")
    _check("sub", p.quote_sub(STEW_QUOTE), "from John Ellis · to eat here · for you")
    _check("button", p.quote_button_text(STEW_QUOTE), "Buy — 6 coins")
    var bread := {"quote_id": 8, "seller": "Hannah Boggs", "item": "bread", "qty": 2, "amount": 6,
        "lines": [{"item": "bread", "display_label": "a loaf of bread", "qty": 2}]}
    _check("two loaves, article dropped", p.quote_what(bread), "2× loaf of bread")
    _check("a choice good names no disposition", p.quote_sub(bread), "from Hannah Boggs")
    var room := {"quote_id": 9, "seller": "Hannah Boggs", "item": "nights_stay", "qty": 1, "amount": 4,
        "lines": [{"item": "nights_stay", "display_label": "a night's stay", "qty": 1}]}
    _check("a room books", p.quote_button_text(room), "Book — 4 coins")
    _check("a room is for tonight", p.quote_sub(room), "from Hannah Boggs · for tonight")
    var bundle := {"quote_id": 10, "seller": "John Ellis", "item": "stew", "qty": 1, "amount": 9, "consume_now": false,
        "lines": [{"item": "stew", "display_label": "Stew", "qty": 1}, {"item": "ale", "display_label": "Ale", "qty": 2}]}
    _check("a bundle lists every line", p.quote_what(bundle), "Stew + 2× Ale")
    _check("a bundle carries its own disposition", p.quote_sub(bundle), "from John Ellis · to take home")
    _free(p)
    _done()


## Jeff's 10-08 screenshot: John posted a stew quote, and the old form under it
## said John "hasn't named anything for sale". A quoted good is on the page.
func _test_quoted_good_is_on_the_offer_page() -> void:
    var p := _panel([])
    p.quotes = [STEW_QUOTE]
    _check("goods", p.goods_of("John Ellis"), ["stew"])
    _check("unit price from the quote", p.unit_price("John Ellis", "stew"), 6)
    p.start_offer("", "")
    _check("seller", p.sel_seller, "John Ellis")
    _check("item", p.sel_item, "stew")
    _check("amount is the asking price", p.amount, 6)
    _check("button", p.send_text(), "Offer John Ellis 6 coins for Stew")
    _check("hint", p.price_hint_text(), "6 coins in all — the asking price.")
    _free(p)
    _done()


func _test_take_body_follows_the_good() -> void:
    var p := _panel([])
    var body: Dictionary = p.take_body(STEW_QUOTE)
    _check("terms verbatim", [body["seller"], body["item"], body["qty"], body["amount"], body["quote_id"]],
        ["John Ellis", "stew", 1, 6, 7])
    var carry := STEW_QUOTE.duplicate(true)
    carry["consume_now"] = false
    _check("an eat_here good is always eaten here", p.take_body(carry)["consume_now"], true)
    var ale := {"quote_id": 3, "seller": "John Ellis", "item": "ale", "qty": 1, "amount": 3, "consume_now": true,
        "lines": [{"item": "ale", "qty": 1}]}
    p.eat_here = false
    _check("a choice good follows the choice", p.take_body(ale)["consume_now"], false)
    p.eat_here = true
    _check("a choice good follows the choice (here)", p.take_body(ale)["consume_now"], true)
    var room := {"quote_id": 4, "seller": "Hannah Boggs", "item": "nights_stay", "qty": 1, "amount": 4, "consume_now": false}
    p.eat_here = true
    _check("a service sends the quote's own", p.take_body(room)["consume_now"], false)
    var bundle := {"quote_id": 5, "seller": "John Ellis", "item": "ale", "qty": 1, "amount": 9, "consume_now": false,
        "lines": [{"item": "ale", "qty": 1}, {"item": "stew", "qty": 1}]}
    _check("a bundle sends the quote's own", p.take_body(bundle)["consume_now"], false)
    _free(p)
    _done()


func _test_offer_body_follows_the_good() -> void:
    var p := _panel([])
    p.sel_seller = "Hannah Boggs"
    p.sel_item = "nights_stay"
    p.qty = 1
    p.amount = 4
    p.days_ahead = 2
    var body: Dictionary = p.offer_body()
    _check("a room is not consumed", body["consume_now"], false)
    _check("booked ahead", body["ready_in_days"], 2)
    p.days_ahead = 0
    _check("tonight sends no offset", p.offer_body().has("ready_in_days"), false)
    p.sel_item = "stew"
    p.eat_here = false
    _check("an eat_here good is eaten here", p.offer_body()["consume_now"], true)
    p.sel_item = "ale"
    _check("a choice good follows the choice", p.offer_body()["consume_now"], false)
    _check("no offset on goods", p.offer_body().has("ready_in_days"), false)
    _free(p)
    _done()


func _test_how_many_scales_the_price() -> void:
    var p := _panel([])
    p.host.vendor_mentions = {"John Ellis": ["ale"]}
    p.host.vendor_mention_prices = {"John Ellis": {"ale": 4}}
    p.open()
    p.start_offer("John Ellis", "ale")
    _check("one ale at the heard price", [p.qty, p.amount], [1, 4])
    p._on_qty(3)
    _check("three ales", p.amount, 12)
    _check("asking price", p.price_hint_text(), "12 coins in all — the asking price.")
    _check("button", p.send_text(), "Offer John Ellis 12 coins for 3× Ale")
    p._on_amount(10)
    _check("less", p.price_hint_text(), "Less than the asking price of 12 coins. They may say no.")
    p._on_amount(25)
    _check("more than the purse", p.price_hint_text(), "You only have 20 coins.")
    p._on_amount(14)
    _check("more", p.price_hint_text(), "More than the asking price of 12 coins.")
    p.host.vendor_mentions = {"John Ellis": ["ale", "cider"]}
    p._on_what("cider")
    _check("no price heard", p.price_hint_text(), "They haven't named a price. They may say no, or ask for more.")
    _check("unknown price starts at 1", p.amount, 1)
    _free(p)
    _done()


func _test_counter_card_answers_the_counter() -> void:
    var p := _panel([])
    p.note_pay_offer({"ledger_id": 41, "buyer_id": "pc-1", "seller_id": "npc-j", "consume_now": false})
    p.note_countered({"ledger_id": 41, "buyer_id": "pc-1", "seller_id": "npc-j", "seller_name": "John Ellis",
        "item": "ale", "qty": 2, "original_amount": 4, "counter_amount": 6})
    p.note_countered({"ledger_id": 42, "buyer_id": "npc-x", "seller_id": "npc-j", "seller_name": "John Ellis",
        "item": "ale", "qty": 1, "original_amount": 1, "counter_amount": 3})
    _check("only the player's counter", p.visible_counters().size(), 1)
    var body: Dictionary = p.counter_body(p.counters[41])
    _check("answers the counter", [body["seller"], body["item"], body["qty"], body["amount"], body["in_response_to"]],
        ["John Ellis", "ale", 2, 6, 41])
    _check("keeps the first disposition", body["consume_now"], false)
    _check("not a quote take", body.has("quote_id"), false)
    p.note_resolved({"ledger_id": 41})
    _check("a resolved counter goes", p.visible_counters().size(), 0)
    _free(p)
    _done()


func _test_spoken_goods_skip_quoted_ones() -> void:
    var p := _panel([])
    p.host.vendor_mentions = {"John Ellis": ["stew", "ale"], "Hannah Boggs": ["bread"]}
    p.quotes = [STEW_QUOTE]
    _check("stew has its quote card", p.spoken_goods(),
        [{"seller": "John Ellis", "items": ["ale"]}, {"seller": "Hannah Boggs", "items": ["bread"]}])
    _check("the offer page lists both", p.goods_of("John Ellis"), ["stew", "ale"])
    _free(p)
    _done()


func _test_buy_sends_and_closes() -> void:
    var sent := []
    var p := _panel(sent)
    var pending := []
    var paid := [0]
    p.offer_pending.connect(func(s: String): pending.append(s))
    p.paid.connect(func(): paid[0] += 1)
    p.open()
    _check("open fetches the quotes", _routes(sent), ["quotes"])
    p._on_quotes_response(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(),
        JSON.stringify({"quotes": [STEW_QUOTE]}).to_utf8_buffer())
    p._on_take(STEW_QUOTE)
    _check("pay sent", _routes(sent), ["quotes", "pay"])
    _check("with the quote", sent[1][1]["quote_id"], 7)
    p._on_pay_response(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _ok("accepted"))
    _check("closed", p.visible, false)
    _check("paid", paid[0], 1)
    _check("a taken quote is not pending", pending, [])
    p.open()
    p.host.vendor_mentions = {"John Ellis": ["stew"]}
    p.start_offer("John Ellis", "stew")
    p._on_send()
    p._on_pay_response(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _ok("pending"))
    _check("an own offer waits on the seller", pending, ["John Ellis"])
    _free(p)
    _done()


func _test_one_pay_at_a_time() -> void:
    var sent := []
    var p := _panel(sent)
    p.open()
    p._on_take(STEW_QUOTE)
    p._on_take(STEW_QUOTE)
    _check("one pay in flight", _routes(sent).count("pay"), 1)
    _check("says so", p.status_label.text, "Your last offer is still on its way.")
    p.host.pc_coins = 3
    p._on_pay_response(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _ok("accepted"))
    p.open()
    p._on_take(STEW_QUOTE)
    _check("not with too few coins", _routes(sent).count("pay"), 1)
    _check("says how many", p.status_label.text, "You only have 3 coins.")
    _free(p)
    _done()


func _test_refused_take_refetches() -> void:
    var sent := []
    var p := _panel(sent)
    p.open()
    p._on_take(STEW_QUOTE)
    p._on_pay_response(HTTPRequest.RESULT_SUCCESS, 409, PackedStringArray(),
        JSON.stringify({"error": "that quote has expired"}).to_utf8_buffer())
    _check("still open", p.visible, true)
    _check("the refusal shows", p.status_label.text, "That quote has expired.")
    _check("the cards refetch", _routes(sent).count("quotes"), 2)
    _free(p)
    _done()


func _test_empty_text() -> void:
    var p := _panel([])
    var help := ["Nobody here has offered you anything yet.", "Ask them what they sell — what they name will show here."]
    _check("nothing offered", p.empty_lines(), help)
    p.host.pc_lodging = {"inn_name": "Tavern", "until_label": "through the day", "keeper_name": "Hannah Boggs"}
    _check("keeper here: the help, then the room", p.empty_lines(), help + ["Your room is paid through the day."])
    p.host.huddle_members = [{"name": "John Ellis"}]
    _check("another seller: no room line", p.empty_lines(), help)
    p.host.huddle_members = []
    _check("nobody here", p.empty_lines(), ["There is nobody here to pay."])
    # The page: no "They offer you" over nothing; the help shows.
    p.host.huddle_members = [{"name": "Hannah Boggs"}]
    p.open()
    _check("no header without offers", p.offers_header.visible, false)
    _check("the help and the room line", p.offers_box.get_child_count(), 3)
    p.quotes = [STEW_QUOTE.duplicate(true)]
    p.quotes[0]["seller"] = "Hannah Boggs"
    p.refresh()
    _check("header over a card", p.offers_header.visible, true)
    _check("just the card", p.offers_box.get_child_count(), 1)
    _free(p)
    _done()


func _test_not_now_hides_a_quote() -> void:
    var p := _panel([])
    p.open()
    p.quotes = [STEW_QUOTE]
    p._on_quote_dismiss(7)
    _check("hidden", p.visible_quotes().size(), 0)
    p.reset_huddle()
    _check("back in a new conversation", p.visible_quotes().size(), 1)
    _free(p)
    _done()


func _test_pages_build() -> void:
    var p := _panel([])
    p.host.vendor_mentions = {"Hannah Boggs": ["bread"]}
    p.open()
    p.quotes = [STEW_QUOTE]
    p.refresh()
    _check("a quote card and a spoken card", p.offers_box.get_child_count(), 2)
    _check("no have-it-here choice for stew alone", p.offers_dispo_row.visible, false)
    _check("own offer offered", p.own_offer_button.visible, true)
    p.start_offer("Hannah Boggs", "bread")
    _check("offer page", [p.offers_page.visible, p.offer_page.visible], [false, true])
    _check("a chip per person", p.who_flow.get_child_count(), 2)
    _check("a chip per good", p.what_flow.get_child_count(), 1)
    _check("have-it-here shows for bread", p.offer_dispo_row.visible, true)
    _check("no night row", p.night_row.visible, false)
    _check("button names the deal", p.send_button.text, "Offer Hannah Boggs 1 coin for a loaf of bread")
    p._on_who("John Ellis")
    _check("John has stew only", p.what_flow.get_child_count(), 1)
    _check("stew needs no choice", p.offer_dispo_row.visible, false)
    p.close()
    p.host.vendor_mentions = {}
    p.quotes = []
    p.open()
    p.start_offer("John Ellis", "")
    _check("nothing named", p.what_empty_label.visible, true)
    _check("the button asks for a pick", p.send_button.text, "Pick what you want")
    _free(p)
    _done()


func _test_seller_gone_hides_cards() -> void:
    var p := _panel([])
    p.quotes = [STEW_QUOTE]
    p.note_countered({"ledger_id": 50, "buyer_id": "pc-1", "seller_id": "npc-j", "seller_name": "John Ellis",
        "item": "ale", "qty": 1, "original_amount": 2, "counter_amount": 3})
    p.host.huddle_members = [{"name": "Hannah Boggs"}]
    _check("no quote card", p.visible_quotes().size(), 0)
    _check("no counter card", p.visible_counters().size(), 0)
    _check("not on the offer page", p.goods_of("John Ellis"), [])
    _free(p)
    _done()


func _test_counter_stays_gone_when_seller_returns() -> void:
    var p := _panel([])
    p.note_countered({"ledger_id": 60, "buyer_id": "pc-1", "seller_id": "npc-j", "seller_name": "John Ellis",
        "item": "ale", "qty": 1, "original_amount": 2, "counter_amount": 3})
    _check("card while John is here", p.visible_counters().size(), 1)
    var everyone: Array = p.host.huddle_members
    p.host.huddle_members = [{"name": "Hannah Boggs"}]
    _check("no card once he leaves", p.visible_counters().size(), 0)
    p.host.huddle_members = everyone
    _check("still none when he comes back", p.visible_counters().size(), 0)
    _free(p)
    _done()


func _test_offer_page_follows_the_roster() -> void:
    var sent := []
    var p := _panel(sent)
    p.host.vendor_mentions = {"John Ellis": ["ale"], "Hannah Boggs": ["bread"]}
    p.open()
    p.start_offer("John Ellis", "ale")
    p._on_qty(2)
    p._on_amount(5)
    p.refresh()
    _check("a valid choice keeps its amounts", [p.sel_seller, p.sel_item, p.qty, p.amount], ["John Ellis", "ale", 2, 5])
    p.host.huddle_members = [{"name": "Hannah Boggs"}]
    p.refresh()
    _check("John left: Hannah and her bread", [p.sel_seller, p.sel_item, p.qty, p.amount], ["Hannah Boggs", "bread", 1, 1])
    p.host.vendor_mentions = {"Hannah Boggs": ["porridge"]}
    p.refresh()
    _check("bread gone: porridge", p.sel_item, "porridge")
    # A stale choice that reaches the button (no refresh in between) sends nothing.
    p.host.vendor_mentions = {"Hannah Boggs": ["cider"]}
    p._on_send()
    _check("nothing sent", _routes(sent).count("pay"), 0)
    _check("the page shows the new choice", p.sel_item, "cider")
    _check("says why", p.status_label.text, "That is no longer on offer. Check your offer and send again.")
    p._on_send()
    _check("sent once checked", _routes(sent).count("pay"), 1)
    _check("with the shown good", sent[-1][1]["item"], "cider")
    _free(p)
    _done()


func _test_reopen_during_pay_keeps_the_new_box() -> void:
    var p := _panel([])
    var paid := [0]
    p.paid.connect(func(): paid[0] += 1)
    p.open()
    p._on_take(STEW_QUOTE)
    p.close()
    p.open()
    p._on_pay_response(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _ok("accepted"))
    _check("the new open stays", p.visible, true)
    _check("the pay still counts", paid[0], 1)
    _free(p)
    _done()


func _test_unclear_answer_keeps_the_box_open() -> void:
    var p := _panel([])
    var paid := [0]
    p.paid.connect(func(): paid[0] += 1)
    for body in [PackedByteArray(), JSON.stringify({"ledger_id": 3}).to_utf8_buffer(),
            JSON.stringify({"state": null}).to_utf8_buffer(), JSON.stringify({"state": 3}).to_utf8_buffer()]:
        p.open()
        p._on_take(STEW_QUOTE)
        p._on_pay_response(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), body)
        _check("open", p.visible, true)
        _check("says so", p.status_label.text, "The answer was unclear. Check the talk log before you pay again.")
        _check("free to try again", p._busy, false)
    _check("no pay claimed", paid[0], 0)
    _free(p)
    _done()


func _test_cleared_field_survives_refresh() -> void:
    var p := _panel([])
    p.host.vendor_mentions = {"John Ellis": ["ale"]}
    p.host.vendor_mention_prices = {"John Ellis": {"ale": 4}}
    p.open()
    p.start_offer("John Ellis", "ale")
    var field: LineEdit = p.amount_stepper["field"]
    field.grab_focus()
    _check("field focused", field.has_focus(), true)
    field.text = ""
    p.refresh()
    _check("a cleared field waits", field.text, "")
    p._on_qty(2)
    _check("a new price still shows", field.text, "8")
    field.release_focus()
    field.text = ""
    p.refresh()
    _check("unfocused: the value is put back", field.text, "8")
    _free(p)
    _done()


## The 10-08 dead end: in the empty box "Make your own offer" led to an empty
## What list (any named or quoted good is already a card). The empty box shows
## the help, Give coins and Close; with a card, the own offer comes back.
func _test_empty_box_buttons() -> void:
    var p := _panel([])
    p.open()
    _check("empty box: no own offer", p.own_offer_button.visible, false)
    _check("empty box: give coins", p.give_button.visible, true)
    p.quotes = [STEW_QUOTE]
    p.refresh()
    _check("with a card: own offer", p.own_offer_button.visible, true)
    _check("with a card: give coins", p.give_button.visible, true)
    p.host.huddle_members = []
    p.refresh()
    _check("nobody here: no own offer", p.own_offer_button.visible, false)
    _check("nobody here: no give", p.give_button.visible, false)
    _free(p)
    _done()


func _test_give_body_and_button() -> void:
    var p := _panel([])
    p.open()
    p._on_give_pressed()
    _check("give page", [p.offers_page.visible, p.offer_page.visible, p.give_page.visible], [false, false, true])
    _check("title", p.title_label.text, "Give coins")
    _check("first person here", p.give_to, "John Ellis")
    _check("button", p.give_send_button.text, "Give John Ellis 1 coin")
    _check("the line under it", p.give_hint.text, "They keep it. Nothing is bought.")
    p._on_give_who("Hannah Boggs")
    p._on_give_amount(5)
    _check("button names the gift", p.give_send_button.text, "Give Hannah Boggs 5 coins")
    _check("no for: none sent", p.give_body(), {"recipient": "Hannah Boggs", "amount": 5})
    p.give_for_field.text = "  the  bread you  lent me  "
    _check("for, tidied", p.give_body(), {"recipient": "Hannah Boggs", "amount": 5, "for": "the bread you lent me"})
    _check("for is capped", p.give_for_field.max_length, 200)
    p._on_give_pressed()
    _check("reopen keeps the person", p.give_to, "Hannah Boggs")
    _check("reopen clears the line", p.give_for_field.text, "")
    _check("reopen starts at 1", p.give_amount, 1)
    _free(p)
    _done()


## Player to player: another PC in the conversation is a chip like anyone.
func _test_give_lists_a_player() -> void:
    var p := _panel([])
    p.host.huddle_members = [{"name": "Tom Gale"}, {"name": "John Ellis"}, {"name": "Mary Pell"}]
    p.open()
    p.start_give("Tom Gale")
    _check("a chip per person, not me", p.give_who_flow.get_child_count(), 2)
    _check("the player picked", p.give_body()["recipient"], "Tom Gale")
    _free(p)
    _done()


func _test_give_sends_and_closes() -> void:
    var sent := []
    var p := _panel(sent)
    var gave := []
    var paid := [0]
    p.gave.connect(func(to: String, n: int): gave.append([to, n]))
    p.paid.connect(func(): paid[0] += 1)
    p.open()
    p.start_give("John Ellis")
    p._on_give_amount(3)
    p._on_give_send()
    _check("give sent", _routes(sent), ["quotes", "give"])
    _check("its body", sent[-1][1], {"recipient": "John Ellis", "amount": 3})
    p._on_give_send()
    _check("one at a time", _routes(sent).count("give"), 1)
    # A pay answer must not settle the gift in flight.
    p._on_pay_response(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _ok("accepted"))
    _check("still busy after a stray pay answer", p._busy, true)
    p._on_give_response(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(),
        JSON.stringify({"recipient": "John Ellis", "amount": 3, "coins": 17}).to_utf8_buffer())
    _check("closed", p.visible, false)
    _check("the talk log hears of it", gave, [["John Ellis", 3]])
    _check("the purse is re-read", paid[0], 1)
    _free(p)
    _done()


func _test_give_refused_stays_open() -> void:
    var p := _panel([])
    var gave := []
    p.gave.connect(func(to: String, n: int): gave.append([to, n]))
    p.open()
    p.start_give("John Ellis")
    p._on_give_send()
    p._on_give_response(HTTPRequest.RESULT_SUCCESS, 422, PackedStringArray(),
        JSON.stringify({"error": "John Ellis is offering Stew for 6 coins — buy it from its offer card. Giving coins does not buy it."}).to_utf8_buffer())
    _check("still open", p.visible, true)
    _check("the refusal shows", p.status_label.text,
        "John Ellis is offering Stew for 6 coins — buy it from its offer card. Giving coins does not buy it.")
    _check("nothing given", gave, [])
    _check("free to try again", p._busy, false)
    _free(p)
    _done()


## A lost answer is ambiguous — the coins may have moved. The box stays open,
## nothing is logged as given, and the purse is re-read before a retry.
func _test_give_lost_answer_rereads_purse() -> void:
    var p := _panel([])
    var gave := []
    var paid := [0]
    p.gave.connect(func(to: String, n: int): gave.append([to, n]))
    p.paid.connect(func(): paid[0] += 1)
    p.open()
    p.start_give("John Ellis")
    p._on_give_send()
    p._on_give_response(HTTPRequest.RESULT_TIMEOUT, 0, PackedStringArray(), PackedByteArray())
    _check("still open", p.visible, true)
    _check("says so", p.status_label.text, "No answer came back. Check your coins and the talk log before you give again.")
    _check("purse re-read", paid[0], 1)
    _check("nothing logged as given", gave, [])
    _check("free to try again", p._busy, false)
    _free(p)
    _done()


func _test_give_checks_the_purse() -> void:
    var sent := []
    var p := _panel(sent)
    p.open()
    p.start_give("John Ellis")
    p._on_give_amount(25)
    _check("hint says the purse", p.give_hint.text, "You only have 20 coins.")
    p._on_give_send()
    _check("nothing sent", _routes(sent).count("give"), 0)
    _check("says so", p.status_label.text, "You only have 20 coins.")
    _free(p)
    _done()


func _test_give_follows_the_roster() -> void:
    var sent := []
    var p := _panel(sent)
    p.open()
    p.start_give("John Ellis")
    p.host.huddle_members = [{"name": "Hannah Boggs"}]
    p._on_give_send()
    _check("nothing sent to a person who left", _routes(sent).count("give"), 0)
    _check("the page shows who is here", p.give_to, "Hannah Boggs")
    _check("says why", p.status_label.text, "John Ellis has gone. Check who you give to and send again.")
    p._on_give_send()
    _check("sent once checked", sent[-1][1]["recipient"], "Hannah Boggs")
    _free(p)
    _done()


## IM Fell has no glyph for some symbols (U+2212 minus drew a box on the web,
## where there is no system font to fall back on — LLM-724). Every string
## literal in pay_panel.gd, and every text the box builds, must be drawable.
func _test_every_glyph_is_in_the_font() -> void:
    var font: Font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")
    _check("font loads", font != null, true)
    if font == null:
        _done()
        return
    # The scanner itself: a glyph written plainly or as an escape is the glyph;
    # a comment is not text.
    var sample := "var a := _button(\"\u2212\")\nvar b := _button(\"\\u2212\") # \"not text\"\nvar c := \"\\U002212 \\\"q\\\"\""
    _check("scanner decodes escapes", _string_literals(sample), ["\u2212", "\u2212", "\u2212 \"q\""])
    var missing := {}
    for s in _string_literals(FileAccess.get_file_as_string("res://scripts/pay_panel.gd")):
        _missing_glyphs(font, s, missing)
    var p := _panel([])
    p.host.vendor_mentions = {"John Ellis": ["ale"], "Hannah Boggs": ["bread"]}
    p.host.vendor_mention_prices = {"John Ellis": {"ale": 3}}
    p.host.pc_lodging = {"until_label": "for about 3 more nights", "keeper_name": "Hannah Boggs"}
    p.open()
    p.quotes = [STEW_QUOTE,
        {"quote_id": 8, "seller": "Hannah Boggs", "item": "bread", "qty": 2, "amount": 6,
            "lines": [{"item": "bread", "display_label": "a loaf of bread", "qty": 2}]},
        {"quote_id": 9, "seller": "Hannah Boggs", "item": "nights_stay", "qty": 1, "amount": 4,
            "lines": [{"item": "nights_stay", "display_label": "a night's stay", "qty": 1}]}]
    p.note_countered({"ledger_id": 70, "buyer_id": "pc-1", "seller_name": "John Ellis",
        "item": "ale", "qty": 2, "original_amount": 4, "counter_amount": 6})
    p.refresh()
    _collect_missing(font, p, missing)
    p.start_offer("John Ellis", "ale")
    for amount in [6, 2, 9, 99]:
        p._on_qty(2)
        p._on_amount(amount)
        _collect_missing(font, p, missing)
    p.start_offer("Hannah Boggs", "nights_stay")
    p._on_night(2)
    _collect_missing(font, p, missing)
    p.start_give("Hannah Boggs")
    for amount in [1, 7, 99]:
        p._on_give_amount(amount)
        _collect_missing(font, p, missing)
    _missing_glyphs(font, p.give_for_field.placeholder_text, missing)
    _check("every character has a glyph", missing.keys(), [])
    _free(p)
    _done()


func _collect_missing(font: Font, node: Node, missing: Dictionary) -> void:
    if node is Label or node is Button or node is LineEdit:
        _missing_glyphs(font, str(node.text), missing)
    for child in node.get_children():
        _collect_missing(font, child, missing)


func _missing_glyphs(font: Font, s: String, missing: Dictionary) -> void:
    for i in s.length():
        var c := s.unicode_at(i)
        if c >= 32 and not font.has_char(c):
            missing["U+%04X %s" % [c, s]] = true


## The double-quoted literals on the code part of each line (comments cut).
static func _string_literals(src: String) -> Array:
    var out: Array = []
    for line in src.split("\n"):
        var in_str := false
        var cur := ""
        var i := 0
        while i < line.length():
            var ch := line[i]
            if in_str:
                if ch == "\\" and i + 1 < line.length():
                    # Decode \uXXXX and \UXXXXXX as GDScript does, so an escaped
                    # glyph is checked as the glyph, not as its hex digits.
                    var esc := line[i + 1]
                    var width := 4 if esc == "u" else (6 if esc == "U" else 0)
                    if width > 0 and i + 2 + width <= line.length() and line.substr(i + 2, width).is_valid_hex_number():
                        cur += char(line.substr(i + 2, width).hex_to_int())
                        i += 2 + width
                        continue
                    cur += esc
                    i += 2
                    continue
                if ch == "\"":
                    out.append(cur)
                    cur = ""
                    in_str = false
                else:
                    cur += ch
            elif ch == "\"":
                in_str = true
            elif ch == "#":
                break
            i += 1
    return out
