extends SceneTree

## Headless harness for the pointer that draws a player's eye (pointer.gd,
## LLM-855): where its tip lands, and its pulse.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/pointer_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const Pointer = preload("res://scripts/pointer.gd")

const TESTS := [
    "_test_at_a_point",
    "_test_on_a_box",
    "_test_pulse",
    "_test_scaled_pulse",
    "_test_runs_on_its_own",
]

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _check_test_list()
    for t in TESTS:
        _current = t
        await call(t)
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)
    print("\n[pointer_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[pointer_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _done() -> void:
    _completed[_current] = true


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


func _art() -> Texture2D:
    return ImageTexture.create_from_image(Image.create(11, 15, false, Image.FORMAT_RGBA8))


func _test_at_a_point() -> void:
    var p = Pointer.at(_art(), Vector2(40.6, 12.2))
    _check("tip on whole pixels", p.tip, Vector2(40, 12))
    _check("drawn from the tip (top-left of the art)", p.position, Vector2(40, 12))
    _check("not centred", p.centered, false)
    _check("full strength at rest", p.modulate.a, 1.0)
    p.free()
    _done()


func _test_on_a_box() -> void:
    var p = Pointer.on(_art(), Rect2(62, 14, 36, 34))
    _check("tip at the box's middle", p.tip, Vector2(80, 31))
    var odd = Pointer.on(_art(), Rect2(0, 0, 5, 5))
    _check("an odd box: the middle's pixel", odd.tip, Vector2(2, 2))
    p.free()
    odd.free()
    _done()


func _test_pulse() -> void:
    var p = Pointer.at(_art(), Vector2(10, 10))
    p.pose(Pointer.PERIOD / 2.0)
    _check("half a pulse: drawn back along the diagonal", p.position, Vector2(10, 10) + Vector2.ONE * Pointer.POKE)
    _check("half a pulse: faded", snappedf(p.modulate.a, 0.01), Pointer.ALPHA_MIN)
    p.pose(Pointer.PERIOD)
    _check("a full pulse: back on the tip", p.position, Vector2(10, 10))
    _check("a full pulse: full strength", p.modulate.a, 1.0)
    p.pose(Pointer.PERIOD * 0.1)
    _check("early in the pulse: whole pixels only", p.position, Vector2(10, 10))
    p.free()
    _done()


func _test_scaled_pulse() -> void:
    var p = Pointer.at(_art(), Vector2(10, 10), 2.0)
    _check("drawn at the scale", p.scale, Vector2(2, 2))
    p.pose(Pointer.PERIOD / 2.0)
    _check("the poke is whole pixels of the art", p.position, Vector2(10, 10) + Vector2.ONE * Pointer.POKE * 2.0)
    p.free()
    _done()


func _test_runs_on_its_own() -> void:
    var p = Pointer.at(_art(), Vector2(10, 10))
    root.add_child(p)
    await process_frame
    await process_frame
    _check("on the tree its clock runs by itself", p._time > 0.0, true)
    p.free()
    _done()
