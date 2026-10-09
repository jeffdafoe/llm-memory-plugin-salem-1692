extends SceneTree

## Headless harness for the LLM-691 character creator's request handling: the
## creator runs every request on one HTTPRequest, so a request that cannot
## start must leave no callback connected (it would consume a later request's
## completion) and must end the save, and the creator cannot close mid-save.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/character_creator_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The immediate-error path is the reviewer's ERR_BUSY: a raw request is put
## on the creator's HTTPRequest first (_busy), so the creator's own request()
## fails at once. Each test cancels it again; nothing reaches the network
## within the one frame the tests run in.

const TESTS := [
    "_test_post_failure_leaves_nothing_connected",
    "_test_post_refuses_a_second_request",
    "_test_save_that_cannot_start_ends_the_save",
    "_test_no_close_mid_save",
    "_test_villager_mode",
    "_test_store_line_text",
    "_test_buy_button_only_with_a_holder",
    "_test_quote_for",
    "_test_buy_answer_flow",
    "_test_early_answer_is_replayed",
    "_test_pay_refusal_names_the_cause",
    "_test_failed_quote_read_still_offers",
    "_test_buy_section_lists_goods_in_row_order",
    "_test_buy_summary_and_save",
    "_test_villager_mode_has_no_buy_section",
    "_test_village_scale_follows_the_camera",
    "_test_save_waits_for_a_wardrobe_reload",
    "_test_buttons_are_tap_height",
]

var _creator: Control = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    _creator = load("res://scripts/character_creator.gd").new()
    root.add_child(_creator)
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[character_creator_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[character_creator_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _run_all() -> void:
    for t in TESTS:
        _current = t
        _reset()
        call(t)


func _done() -> void:
    _completed[_current] = true


func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)


func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t), true)
        _check("harness — %s exists" % t, has_method(t), true)
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name), true)


func _reset() -> void:
    _creator._http.cancel_request()
    for c in _creator._http.request_completed.get_connections():
        _creator._http.request_completed.disconnect(c["callable"])
    _creator._in_flight = false
    _creator._saving = false
    _creator._cancellable = true
    _creator.visible = true
    _creator._wardrobe = {"categories": [], "items": [], "colours": {}}
    _creator._wardrobe_loaded = true
    _creator._picks = {"figure": "straight", "items": {}}
    _creator._name_edit.text = "Tess"
    _creator._npc_id = ""
    _creator._buys = {}
    _creator._early_frames = {}


## Occupy the creator's HTTPRequest so its next request() returns ERR_BUSY.
func _busy() -> void:
    _creator._http.request("http://127.0.0.1:9/")


func _connections() -> int:
    return _creator._http.request_completed.get_connections().size()


func _noop(_r, _c, _h, _b) -> void:
    pass


func _test_post_failure_leaves_nothing_connected() -> void:
    _busy()
    var before := _connections()
    _check("post — a request that cannot start reports false", _creator._post("/api/village/pc/outfit", "{}", _noop), false)
    _check("post — no callback left connected", _connections(), before)
    _check("post — nothing in flight", _creator._in_flight, false)
    _done()


func _test_post_refuses_a_second_request() -> void:
    _creator._in_flight = true
    var before := _connections()
    _check("post — refused while a request is on _http", _creator._post("/api/village/pc/outfit", "{}", _noop), false)
    _check("post — the refused callback is not connected", _connections(), before)
    _done()


func _test_save_that_cannot_start_ends_the_save() -> void:
    _creator._pc_exists = true
    _creator._current_name = "Tess"
    _busy()
    _creator._on_save()
    _check("save — not left saving", _creator._saving, false)
    _check("save — Save usable again", _creator._save_button.disabled, false)
    _check("save — the player is told", _creator._error.text != "", true)
    _check("save — no callback left connected", _connections(), 0)
    _done()


func _test_no_close_mid_save() -> void:
    _creator._saving = true
    _creator._close()
    _check("close — refused while saving", _creator.visible, true)
    _creator._saving = false
    _creator._close()
    _check("close — allowed once the save ends", _creator.visible, false)
    _done()


func _test_villager_mode() -> void:
    _creator.open_for_npc("hannah", "Hannah Boggs", {})
    _check("villager — titled with the villager", _creator._title.text, "Dress Hannah Boggs")
    _check("villager — no name field", _creator._name_edit.visible, false)
    _check("villager — can cancel", _creator._cancel_button.visible, true)
    _busy()
    _creator._on_save()
    _check("villager — a save that cannot start ends the save", _creator._saving, false)
    _check("villager — no callback left connected", _connections(), 0)
    _creator._http.cancel_request()
    _creator.open(true, "Tess", {})
    _check("player — reopening as the player clears the villager", _creator._npc_id, "")
    _check("player — the name field is back", _creator._name_edit.visible, true)
    _done()


## A wardrobe with one sold hat and the given sellers (LLM-715).
func _hat_wardrobe(sellers: Dictionary) -> Dictionary:
    return {"categories": [], "items": [], "colours": {}, "dyes": [], "held": [],
        "goods": {"felt_hat": {"label": "Felt hat", "price": 6}}, "sellers": sellers}


func _line_buttons(good: String) -> Array:
    var out: Array = []
    var line: Control = _creator._store_line(good)
    for child in line.get_children():
        if child is Button:
            out.append(child.text)
    line.free()
    return out


func _test_store_line_text() -> void:
    _creator._wardrobe = _hat_wardrobe({})
    _check("store — nobody here: where it is sold", _creator._store_text("felt_hat"), "Sold at the Store: Felt hat, about 6 coins.")
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 0}]})
    _check("store — the keeper has none", _creator._store_text("felt_hat"), "Felt hat: Josiah Thorne has none just now.")
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 0}, {"name": "Hannah Boggs", "held": 1}]})
    _check("store — names the one who holds it", _creator._store_text("felt_hat"), "Felt hat: Hannah Boggs has it, about 6 coins.")
    _creator._buys = {"felt_hat": {"state": "countered", "seller": "Josiah Thorne", "amount": 9, "message": "Fine felt."}}
    _check("store — a counter shows the price asked", _creator._store_text("felt_hat"), "Felt hat: Josiah Thorne asks 9 coins. \"Fine felt.\"")
    _done()


func _test_buy_button_only_with_a_holder() -> void:
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 0}]})
    _check("buy — no button when nobody here holds it", _line_buttons("felt_hat"), [])
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 2}]})
    _check("buy — a button when someone here holds it", _line_buttons("felt_hat"), ["Buy"])
    _creator._buys = {"felt_hat": {"state": "waiting", "seller": "Josiah Thorne", "ledger_id": 7}}
    _check("buy — no button while waiting", _line_buttons("felt_hat"), [])
    _creator._buys = {"felt_hat": {"state": "countered", "seller": "Josiah Thorne", "ledger_id": 7, "amount": 9}}
    _check("buy — Accept and No on a counter", _line_buttons("felt_hat"), ["Accept", "No"])
    _creator._buys = {"felt_hat": {"state": "declined", "seller": "Josiah Thorne", "message": "x"}}
    _check("buy — Buy again after a no", _line_buttons("felt_hat"), ["Buy"])
    _creator._buys = {}
    _creator._npc_id = "hannah"
    _check("buy — never while dressing a villager", _line_buttons("felt_hat"), [])
    _done()


func _test_quote_for() -> void:
    var hat := {"quote_id": 3, "seller": "Josiah Thorne", "item": "felt_hat", "qty": 1, "amount": 7, "consume_now": false, "lines": [{}]}
    var two := hat.duplicate()
    two["qty"] = 2
    var bundle := hat.duplicate()
    bundle["lines"] = [{}, {}]
    var other := hat.duplicate()
    other["seller"] = "Hannah Boggs"
    _check("quote — takes the seller's quote for one", _creator._quote_for([two, bundle, other, hat], "Josiah Thorne", "felt_hat").get("quote_id", 0), 3)
    _check("quote — none for another good", _creator._quote_for([hat], "Josiah Thorne", "boots"), {})
    _check("quote — not a quote for two, a bundle, or another seller", _creator._quote_for([two, bundle, other], "Josiah Thorne", "felt_hat"), {})
    _done()


func _test_buy_answer_flow() -> void:
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 1}]})
    _creator._buys = {"felt_hat": {"state": "waiting", "seller": "Josiah Thorne", "ledger_id": 7}}
    _creator._on_pay_resolved({"ledger_id": 99, "terminal_state": "accepted"})
    _check("answer — another offer's answer is ignored", _creator._buys["felt_hat"]["state"], "waiting")
    _creator._on_pay_countered({"ledger_id": 7, "counter_amount": 9, "message": ""})
    _check("answer — a counter", _creator._buys["felt_hat"]["state"], "countered")
    _check("answer — the counter price", _creator._buys["felt_hat"]["amount"], 9)
    _creator._on_refuse_counter("felt_hat")
    _check("answer — No clears the line", _creator._buys.has("felt_hat"), false)
    _creator._buys = {"felt_hat": {"state": "waiting", "seller": "Josiah Thorne", "ledger_id": 8}}
    _creator._on_pay_resolved({"ledger_id": 8, "terminal_state": "declined", "message": "Too low."})
    _check("answer — a no gives the reason", _creator._store_text("felt_hat"), "Felt hat: Josiah Thorne said no: \"Too low.\"")
    _creator._buys = {"felt_hat": {"state": "waiting", "seller": "Josiah Thorne", "ledger_id": 9}}
    _creator._on_pay_resolved({"ledger_id": 9, "terminal_state": "accepted"})
    _check("answer — accepted: the hat is held", _creator._wardrobe["held"].has("felt_hat"), true)
    _check("answer — accepted: press Save", _creator._store_text("felt_hat"), "Felt hat: bought. Press Save to wear it.")
    _creator._show()
    _check("answer — reopening drops the finished line", _creator._buys.has("felt_hat"), false)
    _creator._http.cancel_request()
    _done()


func _pay_body(data: Dictionary) -> PackedByteArray:
    return JSON.stringify(data).to_utf8_buffer()


## The seller's answer can reach the world before the pc/pay response: it is
## held while the offer is sending and applied once the ledger id is known.
func _test_early_answer_is_replayed() -> void:
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 1}]})
    var terms := {"seller": "Josiah Thorne"}
    _creator._on_pay_resolved({"ledger_id": 5, "terminal_state": "accepted"})
    _check("early — nothing held while no offer is sending", _creator._early_frames.is_empty(), true)
    _creator._buys = {"felt_hat": {"state": "sending", "seller": "Josiah Thorne"}}
    _creator._on_pay_resolved({"ledger_id": 5, "terminal_state": "accepted"})
    _creator._on_buy_sent(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _pay_body({"ledger_id": 5, "state": "pending"}), "felt_hat", terms)
    _check("early — an accept that outran the response is applied", _creator._buys["felt_hat"]["state"], "bought")
    _check("early — the hat is held", _creator._wardrobe["held"].has("felt_hat"), true)
    _check("early — nothing left held", _creator._early_frames.is_empty(), true)
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 1}]})
    _creator._buys = {"felt_hat": {"state": "sending", "seller": "Josiah Thorne"}}
    _creator._on_pay_countered({"ledger_id": 6, "counter_amount": 9})
    _creator._on_buy_sent(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _pay_body({"ledger_id": 6, "state": "pending"}), "felt_hat", terms)
    _check("early — a counter that outran the response is applied", _creator._buys["felt_hat"]["state"], "countered")
    _check("early — the counter price", _creator._buys["felt_hat"]["amount"], 9)
    _creator._buys = {"felt_hat": {"state": "sending", "seller": "Josiah Thorne"}}
    _creator._on_pay_resolved({"ledger_id": 40, "terminal_state": "declined"})
    _creator._on_buy_sent(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _pay_body({"ledger_id": 7, "state": "pending"}), "felt_hat", terms)
    _check("early — another offer's answer is not applied", _creator._buys["felt_hat"]["state"], "waiting")
    _check("early — and is dropped", _creator._early_frames.is_empty(), true)
    _done()


func _test_pay_refusal_names_the_cause() -> void:
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 1}]})
    _creator._buys = {"felt_hat": {"state": "sending", "seller": "Josiah Thorne"}}
    _creator._on_buy_sent(HTTPRequest.RESULT_SUCCESS, 422, PackedStringArray(), _pay_body({"error": "not enough coins"}), "felt_hat", {"seller": "Josiah Thorne"})
    _check("refusal — the engine's reason is shown", _creator._store_text("felt_hat"), "Felt hat: not enough coins")
    _check("refusal — Buy again", _line_buttons("felt_hat"), ["Buy"])
    _done()


## A quote read that fails still sends the list-price offer.
func _test_failed_quote_read_still_offers() -> void:
    _creator._wardrobe = _hat_wardrobe({"felt_hat": [{"name": "Josiah Thorne", "held": 1}]})
    _creator._buys = {"felt_hat": {"state": "sending", "seller": "Josiah Thorne"}}
    _creator._in_flight = true
    _creator._on_buy_quotes(HTTPRequest.RESULT_SUCCESS, 500, PackedStringArray(), PackedByteArray(), "felt_hat")
    _check("quote read failed — the offer is on its way", _creator._buys["felt_hat"]["state"], "sending")
    _check("quote read failed — pc/pay is in flight", _creator._in_flight, true)
    _creator._http.cancel_request()
    _done()


## A hat and a shirt; the player holds the shirt but not the blue dye on it
## (LLM-727).
func _outfit_wardrobe() -> Dictionary:
    return {
        "categories": [{"id": "head", "label": "Hat"}, {"id": "shirt", "label": "Shirt"}],
        "items": [
            {"id": "felt", "category": "head", "good": "felt_hat", "slots": ["c4"]},
            {"id": "linen", "category": "shirt", "good": "linen_shirt", "slots": ["c4"]},
        ],
        "colours": {"c4": [0, 1]},
        "dyes": [{"good": "blue_dye", "colours": {"c4": [1]}}],
        "goods": {
            "felt_hat": {"label": "Felt hat", "price": 6},
            "linen_shirt": {"label": "Linen shirt", "price": 3},
            "blue_dye": {"label": "Blue dye", "price": 2},
        },
        "held": ["linen_shirt"],
        "coins": 12,
        "sellers": {},
    }


func _outfit_picks() -> Dictionary:
    return {"figure": "straight", "items": {
        "shirt": {"item": "linen", "ramps": {"c4": 1}},
        "head": {"item": "felt", "ramps": {"c4": 0}},
    }}


func _test_buy_section_lists_goods_in_row_order() -> void:
    _creator._wardrobe = _outfit_wardrobe()
    _creator._picks = _outfit_picks()
    _check("section — the hat row first, then the shirt's dye", _creator._buy_goods(), ["felt_hat", "blue_dye"])
    _creator._buys = {"boots": {"state": "waiting", "seller": "Josiah Thorne", "ledger_id": 4}}
    _check("section — an open offer stays listed after trying something else", _creator._buy_goods(), ["felt_hat", "blue_dye", "boots"])
    _creator._buys = {"boots": {"state": "declined", "seller": "Josiah Thorne", "message": "x"}}
    _check("section — a finished offer not tried on is not listed", _creator._buy_goods(), ["felt_hat", "blue_dye"])
    _creator._rebuild_rows()
    _check("section — shown while goods are missing", _creator._buy_panel.visible, true)
    _creator._wardrobe["held"] = ["linen_shirt", "felt_hat", "blue_dye"]
    _creator._buys = {}
    _creator._rebuild_rows()
    _check("section — hidden when the player holds everything", _creator._buy_panel.visible, false)
    _done()


func _test_buy_summary_and_save() -> void:
    _creator._wardrobe = _outfit_wardrobe()
    _creator._picks = _outfit_picks()
    _creator._rebuild_rows()
    _check("save — no seller here: says Save, not Buy", _creator._save_button.text, "Save")
    _check("save — no seller here: still not usable", _creator._save_button.disabled, true)
    _creator._wardrobe["sellers"] = {"felt_hat": [{"name": "Josiah Thorne", "held": 1}]}
    _creator._rebuild_rows()
    _check("save — only some can be bought here: says Save", _creator._save_button.text, "Save")
    _creator._wardrobe["sellers"]["blue_dye"] = [{"name": "Josiah Thorne", "held": 0}]
    _creator._rebuild_rows()
    _check("save — a stockist with none does not count", _creator._save_button.text, "Save")
    _creator._buys = {"blue_dye": {"state": "countered", "seller": "Josiah Thorne", "ledger_id": 2, "amount": 3}}
    _creator._rebuild_rows()
    _check("save — an open counter counts even with none on the shelf", _creator._save_button.text, "Buy 2 more first")
    _creator._buys = {}
    _creator._wardrobe["sellers"]["blue_dye"] = [{"name": "Josiah Thorne", "held": 2}]
    _creator._rebuild_rows()
    _check("summary — total of what is missing and the purse", _creator._buy_summary(), "About 8 coins in all. You have 12 coins.")
    _check("save — names how many are left", _creator._save_button.text, "Buy 2 more first")
    _check("save — not usable while goods are missing", _creator._save_button.disabled, true)
    _creator._buys = {"felt_hat": {"state": "waiting", "seller": "Josiah Thorne", "ledger_id": 9, "amount": 7}}
    _creator._on_pay_resolved({"ledger_id": 9, "terminal_state": "accepted"})
    _check("bought — the price paid leaves the purse", _creator._wardrobe["coins"], 5)
    _check("bought — the total drops the hat", _creator._buy_summary(), "About 2 coins in all. You have 5 coins.")
    _check("bought — the hat line stays, saying to press Save", _creator._buy_goods(), ["felt_hat", "blue_dye"])
    _check("save — one left", _creator._save_button.text, "Buy 1 more first")
    _creator._buys["blue_dye"] = {"state": "sending", "seller": "Josiah Thorne"}
    _creator._on_buy_sent(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), _pay_body({"ledger_id": 10, "state": "accepted"}), "blue_dye", {"seller": "Josiah Thorne", "amount": 2})
    _check("bought at once — the price leaves the purse", _creator._wardrobe["coins"], 3)
    _creator._on_pay_resolved({"ledger_id": 10, "terminal_state": "accepted"})
    _check("bought at once — a later answer for it takes nothing more", _creator._wardrobe["coins"], 3)
    _check("save — usable once everything is held", _creator._save_button.disabled, false)
    _check("save — says Save", _creator._save_button.text, "Save")
    _creator._wardrobe.erase("coins")
    _check("summary — no purse line without the wardrobe's coins", _creator._buy_summary(), "")
    _done()


## A reload keeps the old wardrobe on screen; Save waits for the new one and
## stays off if it fails.
func _test_save_waits_for_a_wardrobe_reload() -> void:
    _creator._wardrobe = _outfit_wardrobe()
    _creator._wardrobe["held"] = ["linen_shirt", "felt_hat", "blue_dye"]
    _creator._picks = _outfit_picks()
    _creator._rebuild_rows()
    _check("reload — Save usable with everything held", _creator._save_button.disabled, false)
    _creator._load_wardrobe()
    _check("reload — Save off while the reload is out", _creator._save_button.disabled, true)
    _creator._on_wardrobe_loaded(HTTPRequest.RESULT_SUCCESS, 500, PackedStringArray(), PackedByteArray())
    _check("reload — Save stays off after a failed reload", _creator._save_button.disabled, true)
    _check("reload — the old wardrobe is still shown", _creator._wardrobe.is_empty(), false)
    _creator._on_save()
    _check("reload — pressing Save does nothing", _creator._saving, false)
    _creator._http.cancel_request()
    _creator._in_flight = false
    _creator._load_wardrobe()
    _creator._on_wardrobe_loaded(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), JSON.stringify(_creator._wardrobe).to_utf8_buffer())
    _check("reload — Save back once the wardrobe arrives", _creator._save_button.disabled, false)
    _creator._http.cancel_request()
    _done()


func _test_villager_mode_has_no_buy_section() -> void:
    _creator._wardrobe = _outfit_wardrobe()
    _creator._picks = _outfit_picks()
    _creator._npc_id = "hannah"
    _creator._rebuild_rows()
    _check("villager — nothing to buy", _creator._buy_goods(), [])
    _check("villager — no buy section", _creator._buy_panel.visible, false)
    _check("villager — Save usable", _creator._save_button.disabled, false)
    _done()


func _test_village_scale_follows_the_camera() -> void:
    var preview := float(_creator._preview_scale())
    _check("village — 2x with no camera", _creator._village_scale(), 2.0 if preview > 2.0 else 0.0)
    var camera := Camera2D.new()
    root.add_child(camera)
    camera.make_current()
    camera.zoom = Vector2(0.5, 0.5)
    _check("village — farmer scale times the zoom", _creator._village_scale(), 1.0)
    camera.zoom = Vector2(0.25, 0.25)
    _check("village — below 1x when zoomed far out", _creator._village_scale(), 0.5)
    _check("village — the doll is drawn at that scale, not raised to 1x", _creator._village_doll_scale(), 0.5)
    camera.zoom = Vector2(3.0, 3.0)
    _check("village — none when no smaller than the big doll", _creator._village_scale(), 0.0)
    _check("village — a hidden doll keeps a usable scale", _creator._village_doll_scale(), 1.0)
    _creator._size_preview()
    _check("village — its box is hidden then", _creator._village_box.visible, false)
    camera.zoom = Vector2(0.5, 0.5)
    _creator._size_preview()
    _check("village — its box is back when zoomed out", _creator._village_box.visible, true)
    camera.free()
    _done()


## Every creator button is the Pay box's size (LLM-728).
func _test_buttons_are_tap_height() -> void:
    _creator._wardrobe = _outfit_wardrobe()
    _creator._wardrobe["sellers"] = {"felt_hat": [{"name": "Josiah Thorne", "held": 1}]}
    _creator._picks = _outfit_picks()
    _check("theme — the creator carries the period theme", _creator.theme != null, true)
    _check("tap — Save", _creator._save_button.custom_minimum_size.y, PeriodTheme.TAP_H)
    _check("tap — Cancel", _creator._cancel_button.custom_minimum_size.y, PeriodTheme.TAP_H)
    var row: Control = _creator._category_row({"id": "head", "label": "Hat"})
    var heights: Array = []
    for child in row.get_child(0).get_children():
        if child is Button:
            heights.append(child.custom_minimum_size.y)
    row.free()
    _check("tap — the row's < and >", heights, [PeriodTheme.TAP_H, PeriodTheme.TAP_H])
    var line: Control = _creator._store_line("felt_hat")
    var buy: Button = null
    for child in line.get_children():
        if child is Button:
            buy = child
    _check("tap — Buy", buy.custom_minimum_size.y if buy != null else 0.0, PeriodTheme.TAP_H)
    line.free()
    _done()
