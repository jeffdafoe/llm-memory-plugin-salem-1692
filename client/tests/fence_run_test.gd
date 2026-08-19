extends SceneTree

## Headless harness for the LLM-637 fence-run preview layout in
## client/scripts/editor.gd. _fence_run_segments is the client-side MIRROR of
## fenceRunSegments in engine/sim/fence.go: the engine decides the real pieces
## when the run is placed, the client only draws the rubber-band preview from
## the same layout. The two must agree or the preview lies — so the cases here
## are the engine's TestFenceRunSegments_Shapes cases, tile for tile, tag for
## tag, plus the anchor arithmetic shared with sim.fenceSegmentPos.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/fence_run_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The editor script is instantiated off-tree via .new() so _ready() never
## fires (no ghost sprite, no world lookup); the functions under test are pure.

const TESTS := [
    "_test_post",
    "_test_horizontal_line",
    "_test_vertical_line_any_corner_order",
    "_test_ring_any_corner_order",
    "_test_two_by_two_is_four_corners",
    "_test_anchor_pos_fills_tile",
]

var _editor = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _editor = load("res://scripts/editor.gd").new()
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    _editor.free()
    print("\n[fence_run_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[fence_run_test] ALL PASS")
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


func _seg(x: int, y: int, tag: String) -> Array:
    return [Vector2i(x, y), tag]


func _segments(a: Vector2i, b: Vector2i) -> Array:
    return _editor._fence_run_segments(a, b)


func _test_post() -> void:
    _check("1x1 is a lone post", _segments(Vector2i(5, 5), Vector2i(5, 5)), [_seg(5, 5, "fence-post")])
    _done()


func _test_horizontal_line() -> void:
    var want := [_seg(5, 5, "fence-end-left"), _seg(6, 5, "fence-h"), _seg(7, 5, "fence-h"), _seg(8, 5, "fence-end-right")]
    _check("4x1 is end-left / h / h / end-right", _segments(Vector2i(5, 5), Vector2i(8, 5)), want)
    _done()


func _test_vertical_line_any_corner_order() -> void:
    var want := [_seg(5, 5, "fence-v-top"), _seg(5, 6, "fence-v"), _seg(5, 7, "fence-v"), _seg(5, 8, "fence-v-bottom")]
    _check("1x4 dragged bottom-up is v-top / v / v / v-bottom", _segments(Vector2i(5, 8), Vector2i(5, 5)), want)
    _done()


func _test_ring_any_corner_order() -> void:
    var want := [
        _seg(5, 5, "fence-corner-tl"), _seg(6, 5, "fence-h"), _seg(7, 5, "fence-h"), _seg(8, 5, "fence-corner-tr"),
        _seg(5, 6, "fence-v"), _seg(8, 6, "fence-v"),
        _seg(5, 7, "fence-corner-bl"), _seg(6, 7, "fence-h"), _seg(7, 7, "fence-h"), _seg(8, 7, "fence-corner-br"),
    ]
    _check("4x3 dragged from bottom-right is the ring, row-major", _segments(Vector2i(8, 7), Vector2i(5, 5)), want)
    _done()


func _test_two_by_two_is_four_corners() -> void:
    var want := [_seg(5, 5, "fence-corner-tl"), _seg(6, 5, "fence-corner-tr"), _seg(5, 6, "fence-corner-bl"), _seg(6, 6, "fence-corner-br")]
    _check("2x2 is four corners", _segments(Vector2i(5, 5), Vector2i(6, 6)), want)
    _done()


## The sprite's top-left (anchor position minus anchor fraction × 16px × scale 2)
## must be the tile origin, and the position must floor back to the same tile —
## the engine stamps the obstacle on floor(pos / 32).
func _test_anchor_pos_fills_tile() -> void:
    var asset := {"anchor_x": 0.5, "anchor_y": 0.85}
    var pos: Vector2 = _editor._fence_tile_anchor_pos(Vector2i(3, 7), asset)
    var top_left := pos - Vector2(0.5 * 32.0, 0.85 * 32.0)
    _check("sprite top-left is the tile origin", top_left.is_equal_approx(Vector2(96, 224)), true)
    _check("anchor floors to its own tile", Vector2i(int(floor(pos.x / 32.0)), int(floor(pos.y / 32.0))), Vector2i(3, 7))
    _done()
