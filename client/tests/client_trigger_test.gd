extends SceneTree

## Headless harness for the LLM-881 reverse channel: a client_trigger frame
## (sent by the engine only to this player's sockets) is dispatched by kind to
## the handler a feature registered; a kind with no handler is ignored.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/client_trigger_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## event_client.gd is instantiated off-tree via .new() so _ready() never fires;
## frames go in through _handle_message, the same entry the socket uses.

const TESTS := [
    "_test_known_kind_reaches_handler",
    "_test_unknown_kind_is_ignored",
    "_test_missing_or_bad_detail_is_empty",
    "_test_reregister_replaces_handler",
]

var _events = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""
var _calls: Array = []
var _other_calls: Array = []


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[client_trigger_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[client_trigger_test] ALL PASS")
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
        _events = load("res://scripts/event_client.gd").new()
        _calls = []
        _other_calls = []
        call(t)
        _events.free()


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


func _on_figure(detail: Dictionary) -> void:
    _calls.append(detail)


func _on_other(detail: Dictionary) -> void:
    _other_calls.append(detail)


func _frame(kind: String, detail) -> String:
    var data := {"kind": kind}
    if detail != null:
        data["detail"] = detail
    return JSON.stringify({"type": "client_trigger", "data": data})


func _test_known_kind_reaches_handler() -> void:
    _events.register_client_trigger("treeline_figure", _on_figure)
    _events.register_client_trigger("other_kind", _on_other)
    _events._handle_message(_frame("treeline_figure", {"note": "x"}))
    _check("handler called once", _calls.size(), 1)
    _check("handler gets the detail", _calls[0] if _calls.size() > 0 else null, {"note": "x"})
    _check("other kind's handler not called", _other_calls.size(), 0)
    _done()


func _test_unknown_kind_is_ignored() -> void:
    _events.register_client_trigger("treeline_figure", _on_figure)
    _events._handle_message(_frame("fireworks", {}))
    _check("unknown kind calls no handler", _calls.size(), 0)
    # Still dispatches after an unknown kind.
    _events._handle_message(_frame("treeline_figure", {}))
    _check("known kind after an unknown one still dispatches", _calls.size(), 1)
    _done()


func _test_missing_or_bad_detail_is_empty() -> void:
    _events.register_client_trigger("treeline_figure", _on_figure)
    _events._handle_message(_frame("treeline_figure", null))
    _events._handle_message(_frame("treeline_figure", [1, 2]))
    _check("two calls", _calls.size(), 2)
    _check("missing detail is {}", _calls[0] if _calls.size() > 0 else null, {})
    _check("non-object detail is {}", _calls[1] if _calls.size() > 1 else null, {})
    _done()


func _test_reregister_replaces_handler() -> void:
    _events.register_client_trigger("treeline_figure", _on_other)
    _events.register_client_trigger("treeline_figure", _on_figure)
    _events._handle_message(_frame("treeline_figure", {}))
    _check("latest handler called", _calls.size(), 1)
    _check("replaced handler not called", _other_calls.size(), 0)
    _done()
