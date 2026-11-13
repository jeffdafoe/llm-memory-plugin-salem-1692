extends SceneTree

## Headless harness for LLM-967: catalog sheets packed into the client. A
## catalog state whose sheet is a res://assets/ path loads from the package
## with no request; anything else under res:// is refused; either way the
## catalog still finishes loading. And the maple sheet the migration points
## at is packed, at the size its rects assume.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/catalog_packed_sheet_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## catalog.gd is instantiated off-tree via .new(), so _ready() never fires and
## no request is made: a packed sheet never reaches the HTTP branch.

const TESTS := [
    "_test_a_packed_sheet_loads_without_a_request",
    "_test_a_missing_packed_sheet_still_finishes_the_load",
    "_test_a_res_path_outside_assets_is_refused",
    "_test_the_maple_sheet_fits_the_migration_rects",
]

const MAPLE := "res://assets/trees/maple.png"
# migrations/LLM-967-painted-maple_up.sql: eight 80x112 frames in a row.
const MAPLE_FRAMES := 8
const FRAME_W := 80
const FRAME_H := 112

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    _check_test_list()
    for t in TESTS:
        _current = t
        call(t)
    _check_all_tests_ran()
    print("\n[catalog_packed_sheet_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[catalog_packed_sheet_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


## A runtime error aborts only the function it happens in, so every test
## calls _done() last and the harness asserts each one reached it.
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


func _check(label: String, ok: bool) -> void:
    _checks += 1
    if ok:
        print("  ok   ", label)
    else:
        _failures += 1
        print("  FAIL ", label)


# --- fixtures --------------------------------------------------------------------

## A fresh catalog off the tree, waiting on `pending` sheets, counting how
## often catalog_loaded fires.
func _catalog(pending: int, fired: Array) -> Node:
    var catalog: Node = load("res://scripts/catalog.gd").new()
    catalog.set("_pending_sheets", pending)
    catalog.catalog_loaded.connect(func() -> void: fired[0] += 1)
    return catalog


# --- tests -----------------------------------------------------------------------

func _test_a_packed_sheet_loads_without_a_request() -> void:
    var fired := [0]
    var catalog := _catalog(1, fired)
    catalog.call("_download_sheet", MAPLE)
    var texture: Texture2D = catalog.sheet_cache.get(MAPLE)
    _check("the sheet is cached under its path", texture != null)
    _check("no HTTPRequest was made", catalog.get_child_count() == 0)
    _check("the catalog finished loading", catalog.loaded and fired[0] == 1)
    if texture != null:
        # hit-testing and the treeline figure read the pixels back
        var image := texture.get_image()
        _check("its pixels read back", image != null and not image.is_empty())
        _check("its pixels are not compressed", image != null and not image.is_compressed())
    catalog.free()
    _done()


func _test_a_missing_packed_sheet_still_finishes_the_load() -> void:
    var fired := [0]
    var catalog := _catalog(2, fired)
    catalog.call("_download_sheet", "res://assets/trees/no-such-tree.png")
    _check("nothing is cached for a missing sheet", catalog.sheet_cache.is_empty())
    _check("the catalog still waits on the other sheet", not catalog.loaded and fired[0] == 0)
    catalog.call("_download_sheet", MAPLE)
    _check("the catalog finishes once the other sheet loads", catalog.loaded and fired[0] == 1)
    catalog.free()
    _done()


func _test_a_res_path_outside_assets_is_refused() -> void:
    var fired := [0]
    var catalog := _catalog(1, fired)
    catalog.call("_download_sheet", "res://scripts/catalog.gd")
    _check("a script path is not loaded as a sheet", catalog.sheet_cache.is_empty())
    _check("no HTTPRequest was made for it", catalog.get_child_count() == 0)
    _check("the catalog still finishes loading", catalog.loaded and fired[0] == 1)
    catalog.free()
    _done()


func _test_the_maple_sheet_fits_the_migration_rects() -> void:
    var texture := load(MAPLE) as Texture2D
    _check("the maple sheet is packed", texture != null)
    if texture != null:
        _check("eight 80x112 frames in a row", texture.get_width() == MAPLE_FRAMES * FRAME_W and texture.get_height() == FRAME_H)
    _done()
