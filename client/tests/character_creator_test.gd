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
