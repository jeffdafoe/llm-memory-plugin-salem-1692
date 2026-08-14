extends SceneTree

## Headless regression harness for the drifting cloud shadows (LLM-633).
##
## What this file exists to catch:
##
##  1. The stamp contract. Clouds are rounded rectangles composed from the
##     sheet's two 3x3 blobs — corners at the corners, edges repeated
##     between them, center fill inside. A wrong mapping draws seams or
##     floating edges inside every cloud on the map.
##
##  2. The torus contract. The field wraps on x so the drift never runs out:
##     a cloud crossing the right edge must continue from column zero, the
##     two layer copies must sit exactly one period apart, and the drift
##     phase must wrap at the period.
##
##  3. Determinism and density. A fixed seed gives every client the same
##     field, and the field is scattered shadow — a few percent of the map —
##     not an overcast lid.
##
##  4. Degrading without the art. The pack is purchased and gitignored
##     (client/.gitignore) — CI runs without it. Missing art must mean no
##     clouds and no errors.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/cloud_shadows_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_stamp_composes_a_minimal_cloud",
    "_test_stamp_repeats_edges_on_a_larger_cloud",
    "_test_stamp_wraps_across_the_torus_edge",
    "_test_footprint_rejection_keeps_a_margin",
    "_test_field_is_deterministic_and_sparse",
    "_test_layer_copies_sit_one_period_apart",
    "_test_drift_wraps_at_the_period",
    "_test_position_tracks_the_world_map_corner",
    "_test_clouds_sit_above_objects",
    "_test_artless_checkout_builds_nothing",
]

var _clouds: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""

## World double: cloud_shadows reads map_width / map_height at _ready and
## pad_x / pad_y per frame.
class StubWorld:
    extends Node2D
    var map_width: int = 200
    var map_height: int = 180
    var pad_x: int = 60
    var pad_y: int = 112

func _initialize() -> void:
    _clouds = Node2D.new()
    _clouds.set_script(load("res://scripts/cloud_shadows.gd"))
    root.add_child(_clouds)

func _process(_delta: float) -> bool:
    _check("harness — layer entered the tree", _clouds.is_inside_tree())
    _check_test_list()
    _run_all()
    _clouds.queue_free()
    _check_all_tests_ran()
    print("\n[cloud_shadows_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[cloud_shadows_test] ALL PASS")
    quit(1 if _failures > 0 else 0)
    return true

func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)

## Same harness contract as storm_fx_test.gd: every test calls _done() last,
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

# --- assertions ------------------------------------------------------------------

func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)

# --- tests -----------------------------------------------------------------------

## A 2x2 cloud is four corner tiles and nothing else.
func _test_stamp_composes_a_minimal_cloud() -> void:
    var cells: Dictionary = {}
    _clouds._stamp_cloud(cells, 10, 10, 2, 2, 0)
    _check("a 2x2 cloud is exactly four cells", cells.size() == 4)
    _check("NW corner", cells.get(Vector2i(10, 10)) == Vector2i(0, 0))
    _check("NE corner", cells.get(Vector2i(11, 10)) == Vector2i(2, 0))
    _check("SW corner", cells.get(Vector2i(10, 11)) == Vector2i(0, 2))
    _check("SE corner", cells.get(Vector2i(11, 11)) == Vector2i(2, 2))
    _done()

## A 4x3 cloud repeats the edge tiles between the corners and fills the
## middle with center tiles; the second blob style offsets rows by 3.
func _test_stamp_repeats_edges_on_a_larger_cloud() -> void:
    var cells: Dictionary = {}
    _clouds._stamp_cloud(cells, 0, 0, 4, 3, 3)
    _check("a 4x3 cloud is twelve cells", cells.size() == 12)
    _check("N edge repeats", cells.get(Vector2i(1, 0)) == Vector2i(1, 3)
        and cells.get(Vector2i(2, 0)) == Vector2i(1, 3))
    _check("W edge", cells.get(Vector2i(0, 1)) == Vector2i(0, 4))
    _check("E edge", cells.get(Vector2i(3, 1)) == Vector2i(2, 4))
    _check("center fill", cells.get(Vector2i(1, 1)) == Vector2i(1, 4)
        and cells.get(Vector2i(2, 1)) == Vector2i(1, 4))
    _check("S edge repeats", cells.get(Vector2i(1, 2)) == Vector2i(1, 5)
        and cells.get(Vector2i(2, 2)) == Vector2i(1, 5))
    _check("SE corner in the second style", cells.get(Vector2i(3, 2)) == Vector2i(2, 5))
    _done()

## A cloud stamped at the right edge continues from column zero — the x-torus
## the drift wrap depends on.
func _test_stamp_wraps_across_the_torus_edge() -> void:
    var w: int = _clouds._field_w
    var cells: Dictionary = {}
    _clouds._stamp_cloud(cells, w - 1, 5, 3, 2, 0)
    _check("west column sits at the right edge", cells.has(Vector2i(w - 1, 5)))
    _check("middle column wrapped to zero", cells.get(Vector2i(0, 5)) == Vector2i(1, 0))
    _check("east column wrapped past zero", cells.get(Vector2i(1, 5)) == Vector2i(2, 0))
    _done()

## Placement keeps a one-cell ring clear, so two clouds can never touch —
## adjacent stamps would weld their edge tiles into one malformed shape.
func _test_footprint_rejection_keeps_a_margin() -> void:
    var cells: Dictionary = {}
    _clouds._stamp_cloud(cells, 10, 10, 3, 3, 0)
    _check("footprint overlapping the cloud is taken",
        not _clouds._footprint_is_free(cells, 11, 11, 2, 2))
    _check("footprint touching the cloud is taken",
        not _clouds._footprint_is_free(cells, 13, 10, 2, 2))
    _check("footprint one cell clear of the ring is free",
        _clouds._footprint_is_free(cells, 15, 10, 2, 2))
    _done()

## Same seed, same field; and the field is scattered shadow, not a lid.
func _test_field_is_deterministic_and_sparse() -> void:
    var a: Dictionary = _clouds._generate_field(_clouds.FIELD_SEED)
    var b: Dictionary = _clouds._generate_field(_clouds.FIELD_SEED)
    _check("same seed generates the identical field",
        a.size() == b.size() and a.hash() == b.hash())
    var other: Dictionary = _clouds._generate_field(_clouds.FIELD_SEED + 1)
    _check("a different seed generates a different field", a.hash() != other.hash())

    var coverage: float = float(a.size()) / float(_clouds._field_w * _clouds._field_h)
    _check("coverage is scattered shadow (%.1f%%)" % (coverage * 100.0),
        coverage > 0.02 and coverage < 0.12)
    for cell: Vector2i in a:
        if cell.x < 0 or cell.x >= _clouds._field_w or cell.y < 0 or cell.y >= _clouds._field_h:
            _check("cell %s escaped the field grid" % cell, false)
            break
    _done()

## The wrap illusion needs the two copies exactly one period apart, both at
## the world render scale. On an artless checkout (CI) no copies exist —
## that side of the contract is _test_artless_checkout_builds_nothing's.
func _test_layer_copies_sit_one_period_apart() -> void:
    if not ResourceLoader.exists(_clouds.SHEET):
        _check("artless — no copies to check", _clouds._layers.is_empty())
        _done()
        return
    _check("two layer copies exist", _clouds._layers.size() == 2)
    if _clouds._layers.size() == 2:
        var a: TileMapLayer = _clouds._layers[0]
        var b: TileMapLayer = _clouds._layers[1]
        _check("copies sit one period apart",
            is_equal_approx(a.position.x - b.position.x, _clouds._period))
        _check("copies draw at the world render scale",
            a.scale.is_equal_approx(Vector2(2.0, 2.0)) and b.scale.is_equal_approx(Vector2(2.0, 2.0)))
        _check("copies carry identical cells",
            a.get_used_cells().size() == b.get_used_cells().size()
            and a.get_used_cells().size() == _clouds._cells.size())
    _done()

## Drift accumulates with process time and wraps at one period.
func _test_drift_wraps_at_the_period() -> void:
    _clouds._drift = 0.0
    _clouds._process(1.0)
    _check("drift advances at the wind speed",
        is_equal_approx(_clouds._drift, _clouds.DRIFT_PPS))
    _clouds._drift = _clouds._period - 1.0
    _clouds._process(1.0)
    _check("crossing the period wraps the phase (got %s)" % _clouds._drift,
        is_equal_approx(_clouds._drift, _clouds.DRIFT_PPS - 1.0))
    _clouds._drift = 0.0
    _done()

## With a world injected, the layer anchors to the map's NW corner plus the
## drift; with none, position is left alone (no crash, no NaN).
func _test_position_tracks_the_world_map_corner() -> void:
    var stub := StubWorld.new()
    root.add_child(stub)
    _clouds.world = stub
    _clouds._drift = 0.0
    _clouds._process(1.0)
    _check("x anchors to the map corner plus drift",
        is_equal_approx(_clouds.position.x, -stub.pad_x * 32.0 + _clouds.DRIFT_PPS))
    _check("y anchors to the map corner",
        is_equal_approx(_clouds.position.y, -stub.pad_y * 32.0))
    _clouds.world = null
    _clouds._process(1.0)
    _check("no world still advances the drift without erroring",
        _clouds._drift > _clouds.DRIFT_PPS)
    stub.queue_free()
    _done()

## Cloud shadow paints over objects and NPCs (OBJECT_Z 10) — it is sky, not
## ground decal — while every CanvasLayer (UI, storm overlay) stays above.
func _test_clouds_sit_above_objects() -> void:
    _check("cloud layer z is above OBJECT_Z", _clouds.z_index == 20 and _clouds.z_index > 10)
    _done()

## CI's normal state: the purchased pack is absent. The loader contract is
## deterministic everywhere (a bad path loads to null); the whole-node
## consequence — no layers, no processing — is asserted on whichever side
## this checkout is on, so the suite is meaningful both with and without
## the art on disk.
func _test_artless_checkout_builds_nothing() -> void:
    _check("a missing sheet loads to null",
        _clouds._load_sheet("res://assets/tilesets/mana-seed/weather-effects/no such sheet.png") == null)
    if ResourceLoader.exists(_clouds.SHEET):
        _check("with the art present, the node built its layers",
            _clouds._layers.size() == 2 and _clouds.is_processing())
    else:
        _check("artless node builds no layers", _clouds._layers.is_empty())
        _check("artless node stops processing", not _clouds.is_processing())
        _check("artless node generated no field", _clouds._cells.is_empty())
    _done()
