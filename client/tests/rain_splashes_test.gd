extends SceneTree

## Headless regression harness for the rain impact splashes (LLM-629).
##
## What this file exists to catch:
##
##  1. The cover contract. A splash must never land inside a placed object's
##     sprite rectangle — that is a splash on a roof or a tree top, exactly
##     what the art's own notes forbid. The cover query walks the same rects
##     click hit-testing uses, via a stub world here so the suite needs no
##     catalog autoload and no real assets.
##
##  2. The world-scale contract. Splash art is pixel art on the village's 16px
##     grid and spawns in world space, so it draws at the village render scale
##     (2.0) or it reads as a different resolution than the ground it hits.
##
##  3. Degrading without the art. The Mana Seed pack is purchased and
##     gitignored (client/.gitignore), so a checkout without it is normal — CI
##     runs exactly that way. Missing art must mean no splashes and no errors,
##     never a broken storm.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/rain_splashes_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The spawner is added to the tree (like storm_fx_test.gd) because spawning
## reads the viewport's canvas transform for the visible world rect. Frame
## slicing is asserted against synthetic in-memory sheets so the contract is
## checked the same way with and without the purchased art on disk.

const TESTS := [
    "_test_builds_frames_from_a_synthetic_sheet",
    "_test_undersized_sheet_stops_short",
    "_test_sheet_smaller_than_one_frame_yields_null",
    "_test_missing_sheet_yields_null",
    "_test_cover_rejection_is_exact",
    "_test_pick_point_respects_cover",
    "_test_gathers_cover_rects_from_the_world",
    "_test_spawning_follows_the_storm",
    "_test_splashes_avoid_covered_ground",
    "_test_artless_checkout_spawns_nothing",
    "_test_a_stalled_frame_spawns_a_bounded_batch",
    "_test_splashes_stay_on_the_map",
    "_test_world_replays_known_weather_onto_a_fresh_splash_layer",
]

const FRAME := Vector2i(16, 16)
const FRAME_COUNT := 8

var _splashes: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

## A world double: the spawner only reads placed_objects and calls
## compute_object_hit_rect, so a stub carrying a rect per container in node
## meta stands in for the real thing — no Catalog autoload, no textures.
class StubWorld:
    extends Node2D
    var placed_objects: Dictionary = {}
    # Map geometry the spawner clamps to (world.gd's fields). Defaults size the
    # map far past any headless viewport so the clamp is inert unless a test
    # shrinks it.
    var map_width: int = 1000
    var map_height: int = 1000
    var pad_x: int = 100
    var pad_y: int = 100

    func compute_object_hit_rect(container: Node2D, _sprite_node: Node2D) -> Rect2:
        return container.get_meta("rect", Rect2())

    ## A container shaped like world.gd's: a Node2D holding a sprite child.
    func add_object(id: String, rect: Rect2, with_sprite: bool = true) -> void:
        var container := Node2D.new()
        container.set_meta("rect", rect)
        if with_sprite:
            container.add_child(Sprite2D.new())
        add_child(container)
        placed_objects[id] = container

## Setup only. A node added to root during _initialize is NOT in the tree yet
## — _ready has not fired — so the checks cannot run here (same harness shape
## as storm_fx_test.gd).
func _initialize() -> void:
    _splashes = Node2D.new()
    _splashes.set_script(load("res://scripts/rain_splashes.gd"))
    root.add_child(_splashes)

func _process(_delta: float) -> bool:
    _check("harness — spawner entered the tree", _splashes.is_inside_tree())
    _check_test_list()
    _run_all()
    _splashes.queue_free()
    _check_all_tests_ran()
    print("\n[rain_splashes_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[rain_splashes_test] ALL PASS")
    quit(1 if _failures > 0 else 0)
    return true

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

## Same harness contract as storm_fx_test.gd: a runtime error aborts only the
## function it happens in, so every test calls _done() as its last statement
## and the harness asserts each one reached it.
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

## A stand-in for the 128x16 impact sheet: N frames side by side.
func _make_strip(count: int) -> ImageTexture:
    var img := Image.create(maxi(FRAME.x * count, 1), FRAME.y, false, Image.FORMAT_RGBA8)
    return ImageTexture.create_from_image(img)

## Frames + a stub world, the two things spawning needs. Returns the stub so
## tests can shape its cover. Callers restore with _teardown_world.
func _setup_world() -> StubWorld:
    var stub := StubWorld.new()
    root.add_child(stub)
    _splashes.world = stub
    _splashes._frames = _splashes._build_frames(_make_strip(FRAME_COUNT))
    return stub

func _teardown_world(stub: StubWorld) -> void:
    _splashes.set_storm(false)
    _splashes.world = null
    for child in _splashes.get_children():
        child.free()
    stub.queue_free()

## Drive enough process time to attempt roughly `count` spawns.
func _pump_spawns(count: int) -> void:
    var seconds: float = float(count) / _splashes.SPLASHES_PER_SECOND
    # Several small steps rather than one big one — one splash's accumulator
    # carry-over per frame is the shape real frames have.
    for _i in 10:
        _splashes._process(seconds / 10.0)

# --- assertions ------------------------------------------------------------------

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)

# --- tests -----------------------------------------------------------------------

func _test_builds_frames_from_a_synthetic_sheet() -> void:
    var frames: SpriteFrames = _splashes._build_frames(_make_strip(FRAME_COUNT))
    _check("builds one animation frame per sheet frame",
        frames != null and frames.get_frame_count("default") == FRAME_COUNT)
    _check("the splash animation does not loop", not frames.get_animation_loop("default"))
    _check("the splash animation runs at the artist's 75ms cadence",
        is_equal_approx(frames.get_animation_speed("default"), 1000.0 / 75.0))
    for i in frames.get_frame_count("default"):
        var tex: Texture2D = frames.get_frame_texture("default", i)
        _check("frame %d is one splash tile" % i,
            tex.get_width() == FRAME.x and tex.get_height() == FRAME.y)
    _done()

## A sheet shorter than FRAME_COUNT frames must stop at the last whole frame
## rather than reading past the edge — the shape a re-exported or wrong-sized
## replacement sheet would have.
func _test_undersized_sheet_stops_short() -> void:
    var frames: SpriteFrames = _splashes._build_frames(_make_strip(3))
    _check("stops at the frames that actually fit",
        frames != null and frames.get_frame_count("default") == 3)
    _done()

func _test_sheet_smaller_than_one_frame_yields_null() -> void:
    var img := Image.create(FRAME.x / 2, FRAME.y / 2, false, Image.FORMAT_RGBA8)
    var frames: SpriteFrames = _splashes._build_frames(ImageTexture.create_from_image(img))
    _check("a sheet with no whole frame builds nothing", frames == null)
    _done()

func _test_missing_sheet_yields_null() -> void:
    var frames: SpriteFrames = _splashes._load_frames(
        "res://assets/tilesets/mana-seed/weather-effects/no such sheet.png")
    _check("absent sheet loads to nothing", frames == null)
    _done()

func _test_cover_rejection_is_exact() -> void:
    var rects: Array[Rect2] = [Rect2(0, 0, 100, 100), Rect2(300, 300, 50, 50)]
    _check("a point inside the first rect is covered",
        _splashes._is_covered(Vector2(50, 50), rects))
    _check("a point inside the second rect is covered",
        _splashes._is_covered(Vector2(320, 340), rects))
    _check("a point between the rects is open",
        not _splashes._is_covered(Vector2(200, 200), rects))
    _check("no rects covers nothing",
        not _splashes._is_covered(Vector2(50, 50), [] as Array[Rect2]))
    _done()

func _test_pick_point_respects_cover() -> void:
    var view := Rect2(0, 0, 400, 400)
    var open = _splashes._pick_uncovered_point(view, [] as Array[Rect2])
    _check("open ground yields a point", open != null)
    _check("the point is inside the view", open != null and view.has_point(open))

    # Cover the whole view (grown so edge samples can't slip past float edges).
    var full: Array[Rect2] = [view.grow(1.0)]
    _check("fully covered ground yields no point",
        _splashes._pick_uncovered_point(view, full) == null)
    _done()

func _test_gathers_cover_rects_from_the_world() -> void:
    var stub := _setup_world()
    stub.add_object("house", Rect2(10, 10, 64, 96))
    stub.add_object("tree", Rect2(200, 50, 96, 96))
    stub.add_object("still-downloading", Rect2(400, 400, 32, 32), false)  # no sprite child
    stub.add_object("unsized", Rect2(500, 500, 0, 0))  # zero rect

    var rects: Array[Rect2] = _splashes._gather_cover_rects()
    _check("gathers one rect per sprite-bearing object", rects.size() == 2)
    _check("the house rect came through", rects.has(Rect2(10, 10, 64, 96)))
    _check("the tree rect came through", rects.has(Rect2(200, 50, 96, 96)))

    _splashes.world = null
    _check("no world gathers no cover", _splashes._gather_cover_rects().is_empty())
    _teardown_world(stub)
    _done()

func _test_spawning_follows_the_storm() -> void:
    var stub := _setup_world()

    _splashes.set_storm(true)
    _check("a storm turns spawning on", _splashes.is_processing())
    _pump_spawns(10)
    var spawned: int = _splashes.get_child_count()
    _check("a storm spawns splashes over open ground", spawned > 0)
    for child in _splashes.get_children():
        _check("splash draws at the village render scale",
            child.scale.is_equal_approx(Vector2(2.0, 2.0)))
        _check("splash carries the one-shot animation",
            child is AnimatedSprite2D and child.sprite_frames != null)

    _splashes.set_storm(false)
    _check("clearing turns spawning off", not _splashes.is_processing())
    _check("clearing lets live splashes finish rather than culling them",
        _splashes.get_child_count() == spawned)
    _teardown_world(stub)
    _done()

func _test_splashes_avoid_covered_ground() -> void:
    var stub := _setup_world()
    # Cover the left half of the visible world rect. In this harness there is
    # no camera, so the canvas transform is identity and world rect == viewport
    # rect — grow the cover past the seam so edge floats can't slip through.
    var view: Rect2 = _splashes._visible_world_rect()
    var left_half := Rect2(view.position, Vector2(view.size.x / 2.0, view.size.y)).grow(1.0)
    stub.add_object("everything-west", left_half)

    _splashes.set_storm(true)
    _pump_spawns(30)
    _check("splashes still fall on the open half", _splashes.get_child_count() > 0)
    for child in _splashes.get_children():
        _check("no splash on covered ground (%s)" % child.position,
            not left_half.has_point(child.position))
    _teardown_world(stub)
    _done()

## The web export's resume shape: a backgrounded tab hands _process its whole
## stall as one delta. The banked spawn debt must be clamped or a minute of
## "missed" rain becomes ~1500 nodes in a single frame, a stall on resume.
func _test_a_stalled_frame_spawns_a_bounded_batch() -> void:
    var stub := _setup_world()
    _splashes.set_storm(true)
    _splashes._process(60.0)
    _check("a minute-long delta spawns at most the per-frame cap (%d spawned)"
            % _splashes.get_child_count(),
        _splashes.get_child_count() <= int(_splashes.MAX_SPAWNS_PER_FRAME))
    _check("no spawn debt is carried past the cap either",
        _splashes._spawn_accum < _splashes.MAX_SPAWNS_PER_FRAME)
    _teardown_world(stub)
    _done()

## Splashes stop at the map edge, not the camera edge — camera limits normally
## keep the two identical, but that is the camera's invariant. With a map
## smaller than the viewport, every splash must land inside it; with the map
## entirely off-view, none may spawn (and the frame must not error).
func _test_splashes_stay_on_the_map() -> void:
    var stub := _setup_world()
    # 4x4 tiles at 32px with a 2-tile pad: world rect (-64,-64) to (64,64) —
    # far smaller than the headless viewport's world rect.
    stub.map_width = 4
    stub.map_height = 4
    stub.pad_x = 2
    stub.pad_y = 2
    var map_rect := Rect2(-64, -64, 128, 128)
    _check("fixture — the map rect is what the fields describe",
        _splashes._map_rect() == map_rect)

    _splashes.set_storm(true)
    _pump_spawns(30)
    _check("splashes still fall on the tiny map", _splashes.get_child_count() > 0)
    for child in _splashes.get_children():
        _check("splash %s landed on generated terrain" % child.position,
            map_rect.has_point(child.position))

    # Push the map wholly outside the visible rect (viewport world rect starts
    # at 0,0 in this cameraless harness; the map now ends before -32).
    stub.pad_x = 100
    stub.pad_y = 100
    stub.map_width = 99
    stub.map_height = 99
    var before: int = _splashes.get_child_count()
    _splashes._process(1.0)
    _check("a map wholly off-view spawns nothing", _splashes.get_child_count() == before)
    _check("an off-view frame drops its spawn debt", is_zero_approx(_splashes._spawn_accum))
    _teardown_world(stub)
    _done()

## world.gd's side of the contract: a weather frame can land before
## build_terrain creates the splash layer — current_weather records it with no
## node to hear it, so creation must replay the known weather (the same
## early-frame race main.gd replays onto the injected storm overlay).
func _test_world_replays_known_weather_onto_a_fresh_splash_layer() -> void:
    var world: Node2D = load("res://scripts/world.gd").new()
    # Kept OFF the tree (same posture as asset_render_scale_test's world) so
    # _ready and its web/JS probing never run; _create_rain_splashes is the
    # unit under test.
    world.set_weather("storm")
    _check("an early frame records weather with no splash layer to hear it",
        world.current_weather == "storm" and world.rain_splashes == null)
    world._create_rain_splashes()
    _check("creation replays the storm onto the fresh layer",
        world.rain_splashes != null and world.rain_splashes._active)

    world.set_weather("clear")
    _check("later weather keeps driving the layer", not world.rain_splashes._active)
    world.free()
    _done()

## CI's normal state: the purchased pack is absent, frames are null. The storm
## must run without splashes rather than erroring every frame.
func _test_artless_checkout_spawns_nothing() -> void:
    var stub := _setup_world()
    _splashes._frames = null

    _splashes.set_storm(true)
    _check("an artless storm never turns processing on", not _splashes.is_processing())
    _check("an artless storm spawns nothing", _splashes.get_child_count() == 0)
    _splashes.set_storm(false)
    _teardown_world(stub)
    _done()
