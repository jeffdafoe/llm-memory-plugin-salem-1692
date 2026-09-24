extends SceneTree

## Headless harness for the broken-well tooltip line (LLM-654) in
## client/scripts/object_tooltip.gd: the pull-on-hover gather read's `damaged`
## flag shows "Broken" in place of the water count, and a sound well still
## shows its count.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/object_tooltip_damage_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The tooltip is instantiated off-tree via .new() so _ready() never fires; the
## test hands it the labels, panel and hovered node the response path touches.

const TESTS := [
    "_test_damaged_well_reads_broken",
    "_test_sound_well_keeps_its_count",
]

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _run_all()
    _check_all_tests_ran()
    print("\n[object_tooltip_damage_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[object_tooltip_damage_test] ALL PASS")
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
        call(t)


func _done() -> void:
    _completed[_current] = true


func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)


## Runs one hover response through _on_count_loaded and returns the berry-line
## text, whether the panel was shown, and whether the line was shown. Panel and
## label start HIDDEN (a new Control is visible by default, which would let a
## missing "show" pass).
func _hover(response: Dictionary) -> Array:
    var tip = load("res://scripts/object_tooltip.gd").new()
    var panel := PanelContainer.new()
    var label := Label.new()
    panel.visible = false
    label.visible = false
    var node := Node2D.new()
    node.set_meta("object_id", "well-1")
    tip._tooltip_panel = panel
    tip._berry_label = label
    tip._hovered_node = node
    var http := HTTPRequest.new()
    var body := JSON.stringify(response).to_utf8_buffer()
    tip._on_count_loaded(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), body, http, "well-1")
    var out := [label.text, panel.visible, label.visible]
    node.free()
    label.free()
    panel.free()
    tip.free()
    return out


func _test_damaged_well_reads_broken() -> void:
    var got := _hover({"gatherable": true, "item": "water", "available": 20, "max": 20, "serves_in_place": true, "damaged": true})
    _check("broken line", got[0], "Broken — the windlass is down")
    _check("panel shown", got[1], true)
    _check("line shown", got[2], true)
    _done()


func _test_sound_well_keeps_its_count() -> void:
    var got := _hover({"gatherable": true, "item": "water", "available": 20, "max": 20, "serves_in_place": true})
    _check("count line", got[0], "20 water")
    _done()
