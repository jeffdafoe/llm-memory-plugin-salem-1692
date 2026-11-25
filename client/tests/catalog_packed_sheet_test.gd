extends SceneTree

## Headless harness for LLM-967: catalog sheets packed into the client. A
## catalog state whose sheet is a res://assets/ path loads from the package
## with no request; anything else under res:// is refused; either way the
## catalog still finishes loading. And the tree and bush sheets the migrations
## point at (three maples, chestnuts, birches, apple trees, blueberry,
## raspberry and sage bushes, and the water edge) are packed, at the size their rects assume, and a leafy look
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
# migrations/LLM-967g-berry-bushes_up.sql: every look a leafy row of 36 frames;
# the berry bushes have eight looks (summer, autumn, winter and winter-snowy,
# each with berries and picked), the sage four (default, autumn, winter,
# winter-snowy). Sheet path -> [frame width, frame height, looks].
const BUSHES := {
    "res://assets/trees/blueberry-1.png": [16, 32, 8],
    "res://assets/trees/blueberry-2.png": [16, 32, 8],
    "res://assets/trees/blueberry-3.png": [16, 32, 8],
    "res://assets/trees/raspberry-1.png": [32, 32, 8],
    "res://assets/trees/raspberry-2.png": [32, 32, 8],
    "res://assets/trees/raspberry-3.png": [32, 32, 8],
    "res://assets/trees/sage-1.png": [16, 16, 4],
    "res://assets/trees/sage-2.png": [16, 16, 4],
    "res://assets/trees/sage-3.png": [16, 16, 4],
}
# migrations/LLM-967h-water-edge_up.sql: four looks (summer, autumn, winter,
# winter-snowy; the sunk barrel one), the reeds 36 frames, the rest one.
# Sheet path -> [frame width, frame height, looks, frames].
const WATER_EDGE := {
    "res://assets/trees/cattails-1.png": [16, 16, 4, 36],
    "res://assets/trees/cattails-2.png": [16, 16, 4, 36],
    "res://assets/trees/cattails-3.png": [16, 16, 4, 36],
    "res://assets/trees/water-plant-1.png": [16, 16, 4, 36],
    "res://assets/trees/water-plant-2.png": [16, 16, 4, 36],
    "res://assets/trees/water-plant-3.png": [16, 16, 4, 36],
    "res://assets/trees/lily-pad.png": [32, 32, 4, 1],
    "res://assets/trees/light-lily-pad.png": [16, 16, 4, 1],
    "res://assets/trees/small-lily-pad.png": [16, 16, 4, 1],
    "res://assets/trees/tiny-lily-pad.png": [16, 16, 4, 1],
    "res://assets/trees/water-vine.png": [16, 16, 4, 1],
    "res://assets/trees/sunk-barrel.png": [16, 16, 1, 1],
}
# migrations/LLM-967i-rocks_up.sql (tools/tree-art/rocks.py): four looks
# (summer, autumn, winter, winter-snowy), the river rock 12 frames (its
# ripples), the rest one. Sheet path -> [frame size, frames].
const ROCKS := {
    "res://assets/trees/pebble.png": [16, 1],
    "res://assets/trees/tiny-rock.png": [16, 1],
    "res://assets/trees/medium-rocks.png": [16, 1],
    "res://assets/trees/large-rocks.png": [16, 1],
    "res://assets/trees/boulder-pair.png": [16, 1],
    "res://assets/trees/flat-rock.png": [16, 1],
    "res://assets/trees/small-rock.png": [32, 1],
    "res://assets/trees/river-rock.png": [32, 12],
}
# migrations/LLM-967j-stumps_up.sql (tools/tree-art/stumps.py): one still
# frame per look, four looks for the forest pieces, one for the sawn stumps
# and the camp's seat. Sheet path -> [frame width, frame height, looks].
const STUMPS := {
    "res://assets/trees/big-stump.png": [48, 32, 4],
    "res://assets/trees/fallen-log.png": [48, 32, 4],
    "res://assets/trees/birch-log.png": [48, 32, 4],
    "res://assets/trees/tree-stump.png": [32, 32, 4],
    "res://assets/trees/broken-trunk.png": [32, 32, 4],
    "res://assets/trees/small-stump.png": [32, 32, 1],
    "res://assets/trees/axe-stump.png": [32, 32, 1],
    "res://assets/trees/camp-stump.png": [16, 16, 1],
}
# migrations/LLM-967k-fish_up.sql (tools/tree-art/fish.py): one row, the
# swim loop along it. Sheet path -> [frame size, frames].
const FISH := {
    "res://assets/trees/shiners.png": [48, 48],
    "res://assets/trees/perch.png": [48, 48],
    "res://assets/trees/brook-trout.png": [48, 48],
}
# migrations/LLM-967l-ground-cover_up.sql (tools/tree-art/plants.py): four
# looks (summer, autumn, winter, winter-snowy) at twice the pack art's size,
# drawn at render_scale 1; the grasses and the fern rustle (36 frames), the
# rest lie still. Sheet path -> [frame width, frame height, frames].
const GROUND_COVER := {
    "res://assets/trees/tall-grass.png": [32, 64, 36],
    "res://assets/trees/medium-grass.png": [32, 64, 36],
    "res://assets/trees/thick-grass.png": [32, 64, 36],
    "res://assets/trees/low-grass.png": [32, 64, 36],
    "res://assets/trees/light-grass.png": [32, 32, 36],
    "res://assets/trees/fern.png": [32, 32, 36],
    "res://assets/trees/clover.png": [32, 32, 1],
    "res://assets/trees/brown-mushroom.png": [32, 32, 1],
    "res://assets/trees/grey-mushroom.png": [32, 32, 1],
    "res://assets/trees/yellow-mushroom.png": [32, 32, 1],
    "res://assets/trees/white-mushroom.png": [32, 32, 1],
    "res://assets/trees/layered-mushroom.png": [32, 32, 1],
    "res://assets/trees/pine-cone.png": [32, 32, 1],
    "res://assets/trees/leaf.png": [32, 32, 1],
    "res://assets/trees/twig.png": [32, 32, 1],
    "res://assets/trees/small-branch.png": [32, 32, 1],
    "res://assets/trees/seeds.png": [32, 32, 1],
}
# migrations/LLM-967f-fallen-trees_up.sql (tools/tree-art/fallen.py): the
# storm damage's sheets, each the size its last rect ends at.
const FALLEN := {
    "res://assets/trees/fallen-tops.png": Vector2i(160, 464),
    "res://assets/trees/storm-stumps.png": Vector2i(174, 24),
    "res://assets/trees/fallen-trunk.png": Vector2i(192, 32),
}
# migrations/LLM-968c-painted-crops_up.sql (tools/tree-art/crops.py): one row
# of six 32x64 still frames, growth-1 .. growth-6, at render_scale 1.
const CROPS := {
    "res://assets/trees/wheat.png": Vector2i(192, 64),
    "res://assets/trees/carrot.png": Vector2i(192, 64),
}
# migrations/LLM-968d-painted-fences_up.sql (tools/tree-art/fences.py): the
# ranch fence's 23 cells of 32x32 in one row, the ranch gate's one 128x96
# frame, the stone gate's closed and open 128x128 frames.
const FENCES := {
    "res://assets/trees/ranch-fence.png": Vector2i(736, 32),
    "res://assets/trees/ranch-fence-gate.png": Vector2i(128, 96),
    "res://assets/trees/stone-gate.png": Vector2i(256, 128),
}
# migrations/LLM-968e-painted-lamps_up.sql (tools/tree-art/lamps.py): the two
# lamp posts unlit and lit (96x160 frames), the sign post's one frame, the
# torch unlit then its four lit flame frames (32x96).
const LAMPS := {
    "res://assets/trees/lamp-post.png": Vector2i(192, 160),
    "res://assets/trees/lamp-post-2.png": Vector2i(192, 160),
    "res://assets/trees/lamp-post-sign.png": Vector2i(96, 160),
    "res://assets/trees/torch.png": Vector2i(160, 96),
}
# migrations/LLM-968f-painted-wells_up.sql (tools/tree-art/wells.py): each
# well's default and damaged looks (96x160 frames), and the pail that
# well_bucket.gd draws whole (32x32).
const WELLS := {
    "res://assets/trees/well-bucket.png": Vector2i(192, 160),
    "res://assets/trees/well-roofed.png": Vector2i(192, 160),
    "res://assets/trees/well-pail.png": Vector2i(32, 32),
}
# migrations/LLM-968g-painted-crates_up.sql (tools/tree-art/crates.py): the
# dark and light crates shut and lid loose (80x80 frames), the notice board's
# five variants (96x128 frames).
const CRATES := {
    "res://assets/trees/crate-dark.png": Vector2i(160, 80),
    "res://assets/trees/crate-light.png": Vector2i(160, 80),
    "res://assets/trees/notice-board.png": Vector2i(480, 128),
}
# migrations/LLM-968h-painted-stalls_up.sql (tools/tree-art/stalls.py): each
# market stall open then closed (128x224 frames).
const STALLS := {
    "res://assets/trees/stall-wood.png": Vector2i(256, 224),
    "res://assets/trees/stall-tiled.png": Vector2i(256, 224),
    "res://assets/trees/stall-fancy.png": Vector2i(256, 224),
}
# migrations/LLM-968i-painted-camp_up.sql (tools/tree-art/camp.py): the tent
# (closed, open, packed; 128x128), the fire (unlit, burning x4, dying x4,
# ashes; 64x64), the stool, woodpile and sack (32x32), the bedroll, spit and
# tripod (64x64).
const CAMP := {
    "res://assets/trees/tent.png": Vector2i(384, 128),
    "res://assets/trees/camp-fire.png": Vector2i(640, 64),
    "res://assets/trees/camp-small.png": Vector2i(96, 32),
    "res://assets/trees/camp-gear.png": Vector2i(320, 64),
}

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
            _check("%s is eight rows of up to 36 frames" % path,
                texture.get_width() == LOOP_FRAMES * APPLE_W and texture.get_height() == APPLE_LOOKS * APPLE_H)
    for path in BUSHES:
        var texture := load(path) as Texture2D
        var size: Array = BUSHES[path]
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %d rows of 36 frames of %dx%d" % [path, size[2], size[0], size[1]],
                texture.get_width() == LOOP_FRAMES * size[0] and texture.get_height() == size[2] * size[1])
    for path in WATER_EDGE:
        var texture := load(path) as Texture2D
        var size: Array = WATER_EDGE[path]
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %d rows of %d frames of %dx%d" % [path, size[2], size[3], size[0], size[1]],
                texture.get_width() == size[3] * size[0] and texture.get_height() == size[2] * size[1])
    for path in ROCKS:
        var texture := load(path) as Texture2D
        var size: Array = ROCKS[path]
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is 4 rows of %d frames of %dx%d" % [path, size[1], size[0], size[0]],
                texture.get_width() == size[1] * size[0] and texture.get_height() == 4 * size[0])
    for path in STUMPS:
        var texture := load(path) as Texture2D
        var size: Array = STUMPS[path]
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %d rows of one %dx%d frame" % [path, size[2], size[0], size[1]],
                texture.get_width() == size[0] and texture.get_height() == size[2] * size[1])
    for path in FISH:
        var texture := load(path) as Texture2D
        var size: Array = FISH[path]
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is one row of %d frames of %dx%d" % [path, size[1], size[0], size[0]],
                texture.get_width() == size[0] * size[1] and texture.get_height() == size[0])
    for path in GROUND_COVER:
        var texture := load(path) as Texture2D
        var size: Array = GROUND_COVER[path]
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is 4 rows of %d frames of %dx%d" % [path, size[2], size[0], size[1]],
                texture.get_width() == size[2] * size[0] and texture.get_height() == 4 * size[1])
    for path in FALLEN:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %s" % [path, FALLEN[path]],
                Vector2i(texture.get_width(), texture.get_height()) == FALLEN[path])
    for path in CROPS:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %s" % [path, CROPS[path]],
                Vector2i(texture.get_width(), texture.get_height()) == CROPS[path])
    for path in FENCES:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %s" % [path, FENCES[path]],
                Vector2i(texture.get_width(), texture.get_height()) == FENCES[path])
    for path in LAMPS:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %s" % [path, LAMPS[path]],
                Vector2i(texture.get_width(), texture.get_height()) == LAMPS[path])
    for path in WELLS:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %s" % [path, WELLS[path]],
                Vector2i(texture.get_width(), texture.get_height()) == WELLS[path])
    for path in CRATES:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %s" % [path, CRATES[path]],
                Vector2i(texture.get_width(), texture.get_height()) == CRATES[path])
    for path in STALLS:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %s" % [path, STALLS[path]],
                Vector2i(texture.get_width(), texture.get_height()) == STALLS[path])
    for path in CAMP:
        var texture := load(path) as Texture2D
        _check("%s is packed" % path, texture != null)
        if texture != null:
            _check("%s is %s" % [path, CAMP[path]],
                Vector2i(texture.get_width(), texture.get_height()) == CAMP[path])
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
