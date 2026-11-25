extends SceneTree

## Headless harness for the painted market stalls (LLM-968h): a keeper inside a
## stall is seen behind its counter. Places each painted stall the way
## world.gd does (_place_object, the catalog values migration
## LLM-968h-painted-stalls sets) with a keeper inside (_apply_stand_offset_if_applicable)
## and checks the three things the look depends on:
##   - the keeper stands on the stand tile, a row behind the stall's anchor;
##   - the keeper's y is less than the stall's, so the y-sorted layer they
##     share (z_index 10 both) draws the keeper first, behind the stall (on
##     the anchor's own row the two would tie, and a tie goes to tree order,
##     which draws villagers last);
##   - over the keeper's head and shoulders the stall's own pixels are empty
##     when it is open (the keeper shows) and solid when it is closed (the
##     shutters are up).
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/stall_keeper_view_test.gd
## Exits 0 when every check passes, 1 if any check fails.

const TESTS := [
    "_test_keeper_behind_the_counter",
]

const KINDS := ["wood", "tiled", "fancy"]
## As migration LLM-968h-painted-stalls sets them.
const ANCHOR_Y := 92.0 / 112.0
const STAND := Vector2i(0, -1)
## The keeper's head and shoulders, world px from their feet: a villager is
## about 56 px tall and 16 wide at the head.
const HEAD := Rect2i(-8, -56, 16, 24)
## The stall's anchor tile.
const TILE := Vector2i(3, 5)

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    for t in TESTS:
        _current = t
        await call(t)
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)
    print("\n[stall_keeper_view_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[stall_keeper_view_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _done() -> void:
    _completed[_current] = true


func _test_keeper_behind_the_counter() -> void:
    var catalog: Node = root.get_node("Catalog")
    for kind in KINDS:
        var sheet: String = "res://assets/trees/stall-%s.png" % kind
        catalog.sheet_cache[sheet] = load(sheet)
        var asset_id: String = "stall-" + str(kind)
        catalog.assets[asset_id] = {"anchor_x": 0.5, "anchor_y": ANCHOR_Y, "render_scale": 1.0,
            "visible_when_inside": true, "stand_offset_x": STAND.x, "stand_offset_y": STAND.y,
            "states": [{"state": "open", "sheet": sheet, "src_x": 0, "src_y": 0, "src_w": 128, "src_h": 224},
                       {"state": "closed", "sheet": sheet, "src_x": 128, "src_y": 0, "src_w": 128, "src_h": 224}]}
        for state in ["open", "closed"]:
            var world = load("res://scripts/world.gd").new()
            world.objects_node = Node2D.new()
            # The placement on its anchor tile's centre, as the migration
            # snaps it.
            var at := Vector2(TILE.x * 32 + 16, TILE.y * 32 + 16)
            world._place_object({"id": "s1", "asset_id": asset_id, "x": at.x, "y": at.y,
                "current_state": state, "display_name": "Stall"})
            var stall: Node2D = world.placed_objects["s1"]
            var keeper := Node2D.new()
            world._apply_stand_offset_if_applicable(keeper, true, "s1")
            var label: String = "%s %s" % [kind, state]
            _check("%s: keeper on the stand tile" % label, keeper.position,
                Vector2((TILE.x + STAND.x) * 32 + 16, (TILE.y + STAND.y) * 32 + 16))
            _check("%s: keeper drawn before the stall (less y)" % label, keeper.position.y < stall.position.y, true)
            # The stall's pixels over the keeper's head, read from its sheet.
            var sprite: Sprite2D = stall.get_child(0)
            var tex: AtlasTexture = sprite.texture
            var img: Image = tex.atlas.get_image()
            var empty := 0
            var solid := 0
            for dy in range(HEAD.position.y, HEAD.end.y):
                for dx in range(HEAD.position.x, HEAD.end.x):
                    var p: Vector2 = keeper.position + Vector2(dx, dy) - stall.position - sprite.position
                    var px := Vector2i(p) + Vector2i(tex.region.position)
                    var a: float = img.get_pixelv(px).a
                    if a == 0.0:
                        empty += 1
                    elif a == 1.0:
                        solid += 1
            var area := HEAD.size.x * HEAD.size.y
            if state == "open":
                _check("%s: the keeper's head and shoulders are clear of the stall" % label, empty, area)
            else:
                _check("%s: the shutters cover where the keeper would be" % label, solid, area)
            keeper.free()
            world.objects_node.free()
            world.free()
    _done()
