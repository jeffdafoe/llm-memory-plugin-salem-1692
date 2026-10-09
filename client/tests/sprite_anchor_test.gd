extends SceneTree

## Headless regression harness for LLM-742 — the per-sprite feet line. The
## livestock sheets leave empty rows under the hooves, so drawn at the villager
## 0.9 an animal floats north of where it stands (a cow on the top row of a pen
## sat on the north fence). Covers _sprite_anchor_y's fallback contract and
## that _build_character_sprite offsets a one-sheet sprite by its own feet line
## at its own scale.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/sprite_anchor_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## world.gd is instantiated off-tree via .new() so _ready()/@onready never fire;
## the sheet is seeded straight into _npc_sheets, the cache
## _build_character_sprite reads.

const TESTS := [
    "_test_anchor_fallbacks",
    "_test_livestock_sprite_stands_on_its_feet_line",
    "_test_villager_sprite_keeps_0_9",
]

const SHEET := "/tilesets/test/sheep.png"

var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

func _initialize() -> void:
    _world = load("res://scripts/world.gd").new()
    _check_test_list()
    _run_all()
    _world.free()
    _check_all_tests_ran()
    print("\n[sprite_anchor_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[sprite_anchor_test] ALL PASS")
    quit(1 if _failures > 0 else 0)

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

## Same harness contract as asset_render_scale_test.gd: a runtime error aborts
## only the function it happens in, so every test calls _done() last and the
## harness asserts each one reached it.
func _done() -> void:
    _completed[_current] = true

func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t))

func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t))
        _check("harness — %s exists" % t, has_method(t))
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name))

# --- fixtures --------------------------------------------------------------------

## A one-sheet sprite payload shaped like the engine's AgentSpriteDTO. anchor_y
## is left out when null, as the engine omits a zero value.
func _sprite_data(frame: int, render_scale: float, anchor_y) -> Dictionary:
    var d := {
        "id": "s1", "name": "Sheep", "sheet": SHEET,
        "frame_width": frame, "frame_height": frame,
        "render_scale": render_scale,
        "animations": [{"direction": "south", "animation": "idle", "row_index": 0, "frame_count": 1, "frame_rate": 5.0}],
    }
    if anchor_y != null:
        d["anchor_y"] = anchor_y
    return d

func _seed_sheet(frame: int) -> void:
    var img := Image.create(frame * 4, frame * 4, false, Image.FORMAT_RGBA8)
    _world._npc_sheets = {SHEET: ImageTexture.create_from_image(img)}

# --- tests -----------------------------------------------------------------------

func _test_anchor_fallbacks() -> void:
    _check("absent → 0.9", is_equal_approx(_world._sprite_anchor_y({}), 0.9))
    _check("zero → 0.9", is_equal_approx(_world._sprite_anchor_y({"anchor_y": 0.0}), 0.9))
    _check("negative → 0.9", is_equal_approx(_world._sprite_anchor_y({"anchor_y": -0.2}), 0.9))
    _check("above 1 → 0.9", is_equal_approx(_world._sprite_anchor_y({"anchor_y": 1.5}), 0.9))
    _check("1.0 kept", is_equal_approx(_world._sprite_anchor_y({"anchor_y": 1.0}), 1.0))
    _check("measured value kept", is_equal_approx(_world._sprite_anchor_y({"anchor_y": 0.65625}), 0.65625))
    _done()

## The sheep: a 32px frame at 2x whose hooves end on row 20, so its feet line is
## 21/32. The sprite's top-left sits 42 px above the container — the hooves land
## on the container's position, not 14 px north of it.
func _test_livestock_sprite_stands_on_its_feet_line() -> void:
    _seed_sheet(32)
    var spr: AnimatedSprite2D = _world._build_character_sprite(_sprite_data(32, 2.0, 0.65625))
    _check("sprite built", spr != null)
    if spr != null:
        _check("scale 2x", spr.scale.is_equal_approx(Vector2(2, 2)))
        _check("x centred: %s" % spr.position, is_equal_approx(spr.position.x, -32.0))
        _check("feet on the position: %s" % spr.position, is_equal_approx(spr.position.y, -42.0))
        spr.free()
    # A cow: 64px frame at 2x, feet line 46/64 → 92 px.
    _seed_sheet(64)
    var cow: AnimatedSprite2D = _world._build_character_sprite(_sprite_data(64, 2.0, 0.71875))
    _check("cow built", cow != null)
    if cow != null:
        _check("cow feet on the position: %s" % cow.position, is_equal_approx(cow.position.y, -92.0))
        cow.free()
    _world._npc_sheets.clear()
    _done()

## A sprite with no anchor_y (every villager sheet) keeps the historical 0.9.
func _test_villager_sprite_keeps_0_9() -> void:
    _seed_sheet(32)
    var spr: AnimatedSprite2D = _world._build_character_sprite(_sprite_data(32, 2.0, null))
    _check("sprite built", spr != null)
    if spr != null:
        _check("0.9 feet line: %s" % spr.position, is_equal_approx(spr.position.y, -57.6))
        spr.free()
    _world._npc_sheets.clear()
    _done()

# --- assertions ------------------------------------------------------------------

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)
