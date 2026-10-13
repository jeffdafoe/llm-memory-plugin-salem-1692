extends SceneTree

## Headless regression for LLM-770: the talk panel's line for a settled sale
## names what was bought. A bundle (several goods taken on one quote) comes on
## pay_resolved with no single item and its goods in `lines`; the line read
## "3 coins for an item" (Wendy's raspberries and ale at the Tavern). Calls the
## panel's static TalkPanel.goods_phrase, which _on_pay_resolved words its
## line with.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/pay_narration_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_goods_phrase",
]

var _talk: GDScript = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _talk = load("res://scripts/talk_panel.gd")
    for t in TESTS:
        _current = t
        call(t)
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)
    print("\n[pay_narration_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[pay_narration_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _test_goods_phrase() -> void:
    _check("one good", _talk.goods_phrase({"item": "stew", "qty": 1}), "stew")
    _check("one good, several", _talk.goods_phrase({"item": "stew", "qty": 3}), "3 stew")
    _check("the live bundle", _talk.goods_phrase({"item": "", "qty": 0,
        "lines": [{"item": "raspberries", "qty": 1}, {"item": "ale", "qty": 1}]}), "raspberries and ale")
    _check("three goods", _talk.goods_phrase({"item": "", "qty": 0,
        "lines": [{"item": "bread", "qty": 2}, {"item": "cheese", "qty": 1}, {"item": "ale", "qty": 1}]}), "2 bread, cheese and ale")
    _check("one line", _talk.goods_phrase({"item": "", "lines": [{"item": "ale", "qty": 1}]}), "ale")
    _check("no item, no lines: as before", _talk.goods_phrase({"item": "", "qty": 0}), "an item")
    _check("empty lines: the item", _talk.goods_phrase({"item": "ale", "qty": 1, "lines": []}), "ale")
    _completed[_current] = true
