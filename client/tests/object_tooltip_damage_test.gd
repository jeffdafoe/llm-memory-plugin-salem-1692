extends SceneTree

## Headless harness for the repair lines in the hover tooltip (LLM-654,
## LLM-698), client/scripts/object_tooltip.gd: the pull-on-hover gather read's
## `repair` shows what is broken and what the town pays — or who is mending it,
## or that the chest cannot pay — in place of any count, for named and unnamed
## objects alike; a sound well still shows its count.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/object_tooltip_damage_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The tooltip is instantiated off-tree via .new() so _ready() never fires; the
## test hands it the labels, panel and hovered node the response path touches.

const TESTS := [
    "_test_broken_well_says_what_and_the_pay",
    "_test_road_is_cleared_not_mended",
    "_test_someone_mending_it",
    "_test_chest_cannot_pay",
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



## Runs one hover response through _on_count_loaded and returns the repair
## text, the count text, whether the panel was shown, and whether the repair
## line was shown. Panel and labels start HIDDEN (a new Control is visible by
## default, which would let a missing "show" pass).
func _hover(response: Dictionary) -> Array:
    var tip = load("res://scripts/object_tooltip.gd").new()
    var panel := PanelContainer.new()
    var count := Label.new()
    var repair := Label.new()
    panel.visible = false
    count.visible = false
    repair.visible = false
    var node := Node2D.new()
    node.set_meta("object_id", "obj-1")
    tip._tooltip_panel = panel
    tip._berry_label = count
    tip._repair_label = repair
    tip._hovered_node = node
    var http := HTTPRequest.new()
    var body := JSON.stringify(response).to_utf8_buffer()
    tip._on_count_loaded(HTTPRequest.RESULT_SUCCESS, 200, PackedStringArray(), body, http, "obj-1")
    var out := [repair.text, count.text, panel.visible, repair.visible]
    node.free()
    count.free()
    repair.free()
    panel.free()
    tip.free()
    return out


func _test_broken_well_says_what_and_the_pay() -> void:
    var got := _hover({"gatherable": true, "item": "water", "available": 20, "max": 20, "serves_in_place": true,
        "repair": {"site_id": "obj-1", "site_kind": "well", "fact": "The windlass at the Well by the Mill is down",
            "bounty": 12, "chest_can_pay": true}})
    _check("repair lines", got[0], "The windlass at the Well by the Mill is down.\nThe town pays 12 coins to mend it.")
    _check("no count beside it", got[1], "")
    _check("panel shown", got[2], true)
    _check("repair line shown", got[3], true)
    _done()


## A road is cleared, not mended; an unnamed, non-gatherable object still
## shows the tooltip.
func _test_road_is_cleared_not_mended() -> void:
    var got := _hover({"gatherable": false,
        "repair": {"site_id": "obj-1", "site_kind": "road", "fact": "A fallen maple lies across the road by the Inn",
            "bounty": 1, "chest_can_pay": true}})
    _check("road lines", got[0], "A fallen maple lies across the road by the Inn.\nThe town pays 1 coin to clear it.")
    _check("panel shown", got[2], true)
    _done()


func _test_someone_mending_it() -> void:
    var got := _hover({"gatherable": false,
        "repair": {"site_id": "obj-1", "site_kind": "minor", "fact": "A rail has come down on the fence by the Mansion",
            "bounty": 3, "chest_can_pay": true, "mender_name": "Lewis Walker"}})
    _check("mender line", got[0], "A rail has come down on the fence by the Mansion.\nLewis Walker is mending it.")
    _done()


func _test_chest_cannot_pay() -> void:
    var got := _hover({"gatherable": false,
        "repair": {"site_id": "obj-1", "site_kind": "business", "fact": "The storm has torn at the Tavern",
            "bounty": 25, "chest_can_pay": false}})
    _check("cannot-pay line", got[0], "The storm has torn at the Tavern.\nThe town cannot pay for the mending just now.")
    _done()


func _test_sound_well_keeps_its_count() -> void:
    var got := _hover({"gatherable": true, "item": "water", "available": 20, "max": 20, "serves_in_place": true})
    _check("count line", got[1], "20 water")
    _check("no repair line", got[3], false)
    _done()
