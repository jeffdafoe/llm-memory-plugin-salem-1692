extends SceneTree

## Headless harness for LLM-967: catalog sheets packed into the client. A
## catalog state whose sheet is a res://assets/ path loads from the package
## with no request; anything else under res:// is refused; either way the
## catalog still finishes loading. And the twelve tree sheets the migrations
## point at (three maples, chestnuts, birches and apple trees) are packed, at the size its rects assume, and a leafy look
## slices into its animated loop the way the catalog serves it.
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
    "_test_a_path_climbing_out_of_assets_is_refused_before_loading",
    "_test_only_a_png_inside_assets_is_a_packed_sheet",
    "_test_the_tree_sheets_fit_the_migration_rects",
    "_test_a_leafy_look_slices_into_its_loop",
]

const MAPLE := "res://assets/trees/maple-1.png"
const MAPLES := ["res://assets/trees/maple-1.png", "res://assets/trees/maple-2.png", "res://assets/trees/maple-3.png"]
const TREES := MAPLES + [
    "res://assets/trees/chestnut-1.png", "res://assets/trees/chestnut-2.png", "res://assets/trees/chestnut-3.png",
    "res://assets/trees/birch-1.png", "res://assets/trees/birch-2.png", "res://assets/trees/birch-3.png",
]
# migrations/LLM-967b-maple-variants_up.sql and LLM-967c-chestnut-birch_up.sql:
# eight looks, one 112-high row each; the leafy rows 36 frames of 80 at
# 6 fps, the bare rows one frame.
const LOOKS := 8
const LOOP_FRAMES := 36
const LOOP_FPS := 6.0
const FRAME_W := 80
const FRAME_H := 112
# migrations/LLM-967d-apple-tree_up.sql and LLM-967e-apple-winter_up.sql: four
# leafy looks of 36 frames, then four one-frame winter looks, all 48x64.
const APPLES := ["res://assets/trees/apple-1.png", "res://assets/trees/apple-2.png", "res://assets/trees/apple-3.png"]
const APPLE_LOOKS := 8  # four leafy fruit looks, four bare winter looks (LLM-967e)
const APPLE_W := 48
const APPLE_H := 64

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


## "assets/../" passes a raw prefix check. The target must be refused before
## load(): a loaded script would also fail the texture cast and leave the
## cache empty, so the check is that the resource was never loaded at all.
## terrain_pack.gd is a script nothing in this suite loads.
func _test_a_path_climbing_out_of_assets_is_refused_before_loading() -> void:
    var target := "res://scripts/terrain_pack.gd"
    _check("the target is not loaded before the test", not ResourceLoader.has_cached(target))
    var fired := [0]
    var catalog := _catalog(1, fired)
    catalog.call("_download_sheet", "res://assets/../scripts/terrain_pack.gd")
    _check("the climbing path never loads its target", not ResourceLoader.has_cached(target))
    _check("nothing is cached for it", catalog.sheet_cache.is_empty())
    _check("the catalog still finishes loading", catalog.loaded and fired[0] == 1)
    catalog.free()
    _done()


## The guard itself, path by path. The climbing PNG is the case only the
## normalising catches: it ends in .png and its raw text starts with
## res://assets/, but it resolves outside. (No PNG sits outside assets/ in
## the package today, so a load-based test cannot see this.)
func _test_only_a_png_inside_assets_is_a_packed_sheet() -> void:
    var catalog_script: GDScript = load("res://scripts/catalog.gd")
    _check("the maple sheet is one", catalog_script.packed_sheet_path(MAPLE) == MAPLE)
    _check("a dotted path inside assets normalises", catalog_script.packed_sheet_path("res://assets/trees/./maple-1.png") == MAPLE)
    _check("a PNG climbing out of assets is not", catalog_script.packed_sheet_path("res://assets/../icon.png") == "")
    _check("a script is not", catalog_script.packed_sheet_path("res://assets/trees/build.gd") == "")
    _check("a PNG outside assets is not", catalog_script.packed_sheet_path("res://scripts/x.png") == "")
    _done()


func _test_the_tree_sheets_fit_the_migration_rects() -> void:
    for path in TREES:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is eight rows of up to 36 frames" % path,
                texture.get_width() == LOOP_FRAMES * FRAME_W and texture.get_height() == LOOKS * FRAME_H)
    for path in APPLES:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is four rows of 36 frames" % path,
                texture.get_width() == LOOP_FRAMES * APPLE_W and texture.get_height() == APPLE_LOOKS * APPLE_H)
    _done()


## The autumn-leaves row of maple-2 as the catalog serves it (the migration's
## rect): get_sprite_frames slices it into the 36-frame loop at 6 fps, every
## frame the next 80 px to the right on that row.
func _test_a_leafy_look_slices_into_its_loop() -> void:
    var catalog: Node = load("res://scripts/catalog.gd").new()
    var path: String = MAPLES[1]
    catalog.set("_pending_sheets", 1)
    catalog.call("_download_sheet", path)
    var state := {"sheet": path, "src_x": 0, "src_y": 2 * FRAME_H, "src_w": FRAME_W, "src_h": FRAME_H,
                  "frame_count": LOOP_FRAMES, "frame_rate": LOOP_FPS}
    var frames: SpriteFrames = catalog.get_sprite_frames(state)
    _check("the look animates", frames != null)
    if frames != null:
        _check("36 frames", frames.get_frame_count("default") == LOOP_FRAMES)
        _check("at 6 fps, looping", is_equal_approx(frames.get_animation_speed("default"), LOOP_FPS) and frames.get_animation_loop("default"))
        var last := frames.get_frame_texture("default", LOOP_FRAMES - 1) as AtlasTexture
        _check("the last frame is the row's 36th", last != null and last.region == Rect2((LOOP_FRAMES - 1) * FRAME_W, 2 * FRAME_H, FRAME_W, FRAME_H))
    catalog.free()
    _done()
