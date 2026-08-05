extends SceneTree

## Headless regression harness for LLM-599 — per-asset render scale on object
## sprites. Covers _asset_render_scale's fallback contract (absent / zero /
## negative / non-finite all collapse to the historical 2x) and, for BOTH
## _create_sprite_node branches (static Sprite2D and animated AnimatedSprite2D),
## that a non-default scale lands on the sprite's scale AND its anchor-offset
## position together — the drift between those two is the regression this file
## exists to catch.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/asset_render_scale_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## world.gd is instantiated off-tree via .new() so _ready()/@onready never fire.
## _create_sprite_node reaches the Catalog autoload (available in --script runs)
## only through get_sprite_frames; the animated branch is driven by seeding
## Catalog.sheet_cache with an in-memory texture, exactly the lookup
## get_sheet_texture performs for a downloaded sheet.

const TESTS := [
    "_test_render_scale_fallbacks",
    "_test_static_branch_scale_and_position",
    "_test_animated_branch_scale_and_position",
    "_test_default_scale_matches_historical_2x",
]

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
    print("\n[asset_render_scale_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[asset_render_scale_test] ALL PASS")
    quit(1 if _failures > 0 else 0)

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

## Same harness contract as marker_transitions_test.gd (LLM-480): a runtime
## error aborts only the function it happens in, so every test calls _done()
## as its last statement and the harness asserts each one reached it.
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

const SHEET_W := 128
const SHEET_H := 32
const FRAME_W := 32
const FRAME_H := 32

## An in-memory sheet texture — no file, no network.
func _make_sheet() -> ImageTexture:
    var img := Image.create(SHEET_W, SHEET_H, false, Image.FORMAT_RGBA8)
    return ImageTexture.create_from_image(img)

## The AtlasTexture callers hand _create_sprite_node (frame 0 of the state).
func _make_atlas(sheet: Texture2D) -> AtlasTexture:
    var atlas := AtlasTexture.new()
    atlas.atlas = sheet
    atlas.region = Rect2(0, 0, FRAME_W, FRAME_H)
    return atlas

## state_info for a static (single-frame) state — get_sprite_frames returns
## null for it without touching the sheet cache.
func _static_state() -> Dictionary:
    return {"state": "default", "sheet": "test_sheet.png", "src_x": 0, "src_y": 0,
            "src_w": FRAME_W, "src_h": FRAME_H, "frame_count": 1, "frame_rate": 0}

## state_info for a 4-frame animated state whose sheet is seeded into
## Catalog.sheet_cache under the given path.
func _animated_state(sheet_path: String) -> Dictionary:
    return {"state": "default", "sheet": sheet_path, "src_x": 0, "src_y": 0,
            "src_w": FRAME_W, "src_h": FRAME_H, "frame_count": 4, "frame_rate": 8}

# --- assertions ------------------------------------------------------------------

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)

## The scale/position contract for one created sprite: uniform scale s, and the
## anchor offset scaled by the SAME s (position = -frame_size * s * anchor).
func _check_sprite_transform(sprite: Node2D, s: float, anchor_x: float, anchor_y: float, ctx: String) -> void:
    _check("%s — sprite created" % ctx, sprite != null)
    if sprite == null:
        return
    _check("%s — scale is %s" % [ctx, s], sprite.scale.is_equal_approx(Vector2(s, s)))
    var want_pos := Vector2(-FRAME_W * s * anchor_x, -FRAME_H * s * anchor_y)
    _check("%s — anchor offset scales with it (%s)" % [ctx, want_pos],
        sprite.position.is_equal_approx(want_pos))
    sprite.free()

# --- tests -----------------------------------------------------------------------

func _test_render_scale_fallbacks() -> void:
    _check("absent falls back to 2.0", _world._asset_render_scale({}) == 2.0)
    _check("explicit value passes through", _world._asset_render_scale({"render_scale": 3.5}) == 3.5)
    _check("sub-1 value passes through", _world._asset_render_scale({"render_scale": 0.5}) == 0.5)
    _check("zero falls back to 2.0", _world._asset_render_scale({"render_scale": 0.0}) == 2.0)
    _check("negative falls back to 2.0", _world._asset_render_scale({"render_scale": -1.5}) == 2.0)
    _check("INF falls back to 2.0", _world._asset_render_scale({"render_scale": INF}) == 2.0)
    _check("NaN falls back to 2.0", _world._asset_render_scale({"render_scale": NAN}) == 2.0)
    _done()

func _test_static_branch_scale_and_position() -> void:
    var atlas := _make_atlas(_make_sheet())
    var sprite: Node2D = _world._create_sprite_node(_static_state(), atlas, 0.5, 0.85, 3.5)
    _check("static — Sprite2D branch taken", sprite is Sprite2D)
    _check_sprite_transform(sprite, 3.5, 0.5, 0.85, "static 3.5x")
    _done()

func _test_animated_branch_scale_and_position() -> void:
    # The Catalog autoload NODE is present in --script runs, but the compile-time
    # global name is not — reach it through the tree root.
    var catalog: Node = root.get_node("Catalog")
    var sheet := _make_sheet()
    var sheet_path := "res://test_asset_render_scale_sheet.png"
    catalog.sheet_cache[sheet_path] = sheet
    var atlas := _make_atlas(sheet)
    var sprite: Node2D = _world._create_sprite_node(_animated_state(sheet_path), atlas, 0.5, 0.85, 3.5)
    catalog.sheet_cache.erase(sheet_path)
    _check("animated — AnimatedSprite2D branch taken", sprite is AnimatedSprite2D)
    if sprite is AnimatedSprite2D:
        _check("animated — 4 frames built", sprite.sprite_frames.get_frame_count("default") == 4)
    _check_sprite_transform(sprite, 3.5, 0.5, 0.85, "animated 3.5x")
    _done()

## The default argument must reproduce the pre-LLM-599 hardcoded 2x, so a call
## site that misses the new parameter renders exactly as before.
func _test_default_scale_matches_historical_2x() -> void:
    var atlas := _make_atlas(_make_sheet())
    var sprite: Node2D = _world._create_sprite_node(_static_state(), atlas, 0.5, 0.85)
    _check_sprite_transform(sprite, 2.0, 0.5, 0.85, "default 2x")
    _done()
