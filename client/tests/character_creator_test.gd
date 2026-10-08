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
    _creator._picks = {"figure": "straight", "items": {}}
    _creator._name_edit.text = "Tess"
    _creator._npc_id = ""
    _creator._buys = {}


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
