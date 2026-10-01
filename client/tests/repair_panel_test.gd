extends SceneTree

## Headless harness for the town's repair work on the client (LLM-690):
## repair_games.gd (the three mini-games' rules), repair_stage.gd (pixel
## scale, object layout, play input → round signals) and repair_panel.gd (the
## title, the mended state, click matching, the layers built from a placed
## object, text tidy-up).
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/repair_panel_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## No purchased art is needed: textures are synthetic ImageTextures, and the
## stage's Title font falls back to the default font when the Mana Seed file
## is absent (as on CI).

const Games = preload("res://scripts/repair_games.gd")
const StageScript = preload("res://scripts/repair_stage.gd")
## Loaded at run time, not preloaded: repair_panel.gd names the Auth and
## Catalog autoloads, which exist only once the tree is up.
var PanelScript: GDScript = null

const TESTS := [
    "_test_game_kind",
    "_test_windlass_hits_only_in_the_zone_and_holds",
    "_test_hammer_hits_the_standing_nail",
    "_test_saw_keeps_the_beat",
    "_test_pixel_and_object_scale",
    "_test_layers_bbox",
    "_test_title_and_sentence",
    "_test_mended_state_name",
    "_test_click_matches_offer",
    "_test_build_layers_fence_break",
    "_test_stage_round_signal",
]

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    PanelScript = load("res://scripts/repair_panel.gd")
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[repair_panel_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[repair_panel_test] ALL PASS")
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


## Every _test_ method is listed in TESTS once, and every name has a method.
func _check_test_list() -> void:
    var methods := {}
    for m in get_method_list():
        var n: String = m["name"]
        if n.begins_with("_test_"):
            methods[n] = true
    var seen := {}
    for t in TESTS:
        _check("harness — %s is a method" % t, methods.has(t), true)
        _check("harness — %s listed once" % t, seen.has(t), false)
        seen[t] = true
    for n in methods:
        _check("harness — %s is in TESTS" % n, seen.has(n), true)


func _rng() -> RandomNumberGenerator:
    var r := RandomNumberGenerator.new()
    r.seed = 690
    return r


func _tex(w: int, h: int) -> ImageTexture:
    var img := Image.create(w, h, false, Image.FORMAT_RGBA8)
    img.fill(Color(0.5, 0.3, 0.1, 1.0))
    return ImageTexture.create_from_image(img)


func _test_game_kind() -> void:
    _check("well → windlass", Games.game_kind("well", ""), "windlass")
    _check("signpost → windlass", Games.game_kind("minor", "signpost"), "windlass")
    _check("road → saw", Games.game_kind("road", ""), "saw")
    _check("business → hammer", Games.game_kind("business", ""), "hammer")
    _check("fence → hammer", Games.game_kind("minor", "fence"), "hammer")
    _check("crate → hammer", Games.game_kind("minor", "crate"), "hammer")
    _done()


func _test_windlass_hits_only_in_the_zone_and_holds() -> void:
    var g = Games.make("windlass", _rng())
    g.zone_x = 60
    g.marker = 10.0
    _check("outside the zone misses", g.press(null), Games.Result.MISS)
    _check("a miss does not hold", g.holding, false)
    g.marker = 70.0
    _check("inside the zone hits", g.press(null), Games.Result.HIT)
    _check("a hit holds", g.holding, true)
    _check("the windlass turns a notch", g.notch, 1)
    _check("held: input ignored", g.press(null), Games.Result.NONE)
    var before: float = g.marker
    g.update(0.5)
    _check("held: the peg stops", g.marker, before)
    var w0: int = g.zone_w
    g.release()
    _check("released", g.holding, false)
    _check("the zone narrows", g.zone_w, w0 - 2)
    for i in 20:
        g.release()
    _check("never below the floor", g.zone_w, g.MIN_ZONE)
    _done()


func _test_hammer_hits_the_standing_nail() -> void:
    var g = Games.make("hammer", _rng())
    _check("no nail up: miss", g.press(Vector2(10, 10)), Games.Result.MISS)
    g.update(0.5)  # past the first wait
    _check("a nail rises", g.up >= 0, true)
    var slot: int = g.up
    var far := Vector2(g.slot_x(slot) + 40, 0) if slot < g.SLOTS / 2 else Vector2(g.slot_x(slot) - 40, 0)
    _check("tap away from the nail: miss", g.press(far), Games.Result.MISS)
    var on: Vector2 = g.nail_rect(slot).get_center()
    _check("tap on the nail: hit", g.press(on), Games.Result.HIT)
    _check("the nail is driven", g.driven[slot], true)
    g.release()
    _check("released: no nail stands", g.up, -1)
    # A nail left standing sinks back: a miss costs time only.
    g.update(g.GAP_TIME + 0.01)
    _check("the next nail rises", g.up >= 0, true)
    g.update(g.UP_TIME + 0.01)
    _check("it sinks untapped", g.up, -1)
    _done()


func _test_saw_keeps_the_beat() -> void:
    var g = Games.make("saw", _rng())
    var left := Vector2(10, 20)
    var right := Vector2(Games.PLAY_W - 10, 20)
    _check("right first: off the beat", g.press(right), Games.Result.MISS)
    var r := Games.Result.NONE
    for i in g.STROKES:
        r = g.press(left if i % 2 == 0 else right)
        if i < g.STROKES - 1:
            _check("stroke %d is progress" % i, r, Games.Result.PROGRESS)
    _check("the last stroke cuts through", r, Games.Result.HIT)
    _check("held after the cut", g.press(left), Games.Result.NONE)
    g.release()
    _check("a fresh section", g.strokes, 0)
    _check("a key press plays the wanted side", g.press(null), Games.Result.PROGRESS)
    _done()


func _test_pixel_and_object_scale() -> void:
    _check("fits the narrow side", StageScript.pixel_scale_for(Vector2(800, 400), Vector2(192, 160)), 2)
    _check("at least 1", StageScript.pixel_scale_for(Vector2(100, 100), Vector2(192, 160)), 1)
    _check("at most the cap", StageScript.pixel_scale_for(Vector2(9000, 9000), Vector2(192, 160)), StageScript.MAX_PIXEL_SCALE)
    _check("a small fence draws at 3", StageScript.object_scale_for(Vector2(48, 16), Vector2(176, 112)), 3)
    _check("a building draws at 1", StageScript.object_scale_for(Vector2(160, 150), Vector2(176, 112)), 1)
    _check("a well draws at 2", StageScript.object_scale_for(Vector2(48, 48), Vector2(176, 112)), 2)
    _done()


func _test_layers_bbox() -> void:
    var layers := [
        {"tex": _tex(16, 16), "pos": Vector2(-24, -14)},
        {"tex": _tex(16, 16), "pos": Vector2(-8, -14)},
        {"tex": _tex(16, 16), "pos": Vector2(8, -14)},
    ]
    _check("three fence cells span 48 x 16", StageScript.layers_bbox(layers), Rect2(-24, -14, 48, 16))
    _check("no layers: empty", StageScript.layers_bbox([]), Rect2())
    _done()


func _test_title_and_sentence() -> void:
    _check("fence title", PanelScript.title_for("minor", "fence"), "Mend the Fence")
    _check("road title", PanelScript.title_for("road", ""), "Clear the Road")
    _check("refusal gets a capital and a stop", PanelScript._sentence("someone is already mending it"), "Someone is already mending it.")
    _check("kept as is", PanelScript._sentence("The chest is empty."), "The chest is empty.")
    _check("one coin", PanelScript._coins(1), "1 coin")
    _done()


func _test_mended_state_name() -> void:
    var fence := [
        {"state": "h", "tags": ["fence-h"]},
        {"state": "h-broken-1", "tags": ["minor-of-h", "minor-form-fence"]},
        {"state": "h-broken-1-l", "tags": ["minor-of-h"]},
    ]
    _check("site mends to h", PanelScript.mended_state_name(fence, "h-broken-1", "minor"), "h")
    _check("edge mends to h", PanelScript.mended_state_name(fence, "h-broken-1-l", "minor"), "h")
    _check("a sound piece: nothing", PanelScript.mended_state_name(fence, "h", "minor"), "")
    var well := [
        {"state": "default", "tags": []},
        {"state": "damaged", "tags": ["damaged"]},
    ]
    _check("a well mends to its sound state", PanelScript.mended_state_name(well, "damaged", "well"), "default")
    _check("a business has no mended state", PanelScript.mended_state_name(well, "damaged", "business"), "")
    _done()


func _fake_world() -> Node2D:
    var s := GDScript.new()
    s.source_code = "extends Node2D\nvar placed_objects := {}\n"
    s.reload()
    var w := Node2D.new()
    w.set_script(s)
    return w


func _placed(w: Node2D, id: String, asset_id: String, pos: Vector2, state: String = "h") -> Node2D:
    var n := Node2D.new()
    n.set_meta("object_id", id)
    n.set_meta("asset_id", asset_id)
    n.set_meta("current_state", state)
    n.position = pos
    var sp := Sprite2D.new()
    sp.texture = _tex(16, 16)
    sp.centered = false
    sp.scale = Vector2(2, 2)
    sp.position = Vector2(-16 * 2 * 0.5, -16 * 2 * 0.85)
    n.add_child(sp)
    w.placed_objects[id] = n
    w.add_child(n)
    return n


func _test_click_matches_offer() -> void:
    var w := _fake_world()
    _placed(w, "mid", "fence", Vector2(100, 100))
    _placed(w, "left", "fence", Vector2(68, 100))
    _placed(w, "far", "fence", Vector2(300, 100))
    _placed(w, "crate", "crate", Vector2(110, 100))
    var debris := _placed(w, "debris", "debris", Vector2(0, 0))
    debris.set_meta("attached_to", "mid")
    _check("the site itself", PanelScript.click_matches_offer(w, "mid", "mid"), true)
    _check("the neighbour of the break", PanelScript.click_matches_offer(w, "left", "mid"), true)
    _check("an overlay on the site", PanelScript.click_matches_offer(w, "debris", "mid"), true)
    _check("a fence far along the run", PanelScript.click_matches_offer(w, "far", "mid"), false)
    _check("another asset nearby", PanelScript.click_matches_offer(w, "crate", "mid"), false)
    _check("no offer", PanelScript.click_matches_offer(w, "mid", ""), false)
    w.free()
    _done()


func _test_build_layers_fence_break() -> void:
    var w := _fake_world()
    _placed(w, "mid", "fence", Vector2(100, 100), "h-broken-1")
    _placed(w, "left", "fence", Vector2(68, 100), "h-broken-1-l")
    _placed(w, "right", "fence", Vector2(132, 100), "h-broken-1-r")
    _placed(w, "far", "fence", Vector2(300, 100))
    var offer := {"object_id": "mid", "site_kind": "minor", "form": "fence"}
    var layers: Dictionary = PanelScript.build_layers(w, offer)
    _check("three broken cells (site + both edges)", layers["broken"].size(), 3)
    _check("three mended cells", layers["sound"].size(), 3)
    _check("the broken run spans 48 art px", StageScript.layers_bbox(layers["broken"]).size, Vector2(48, 16))
    # A road obstacle has no mended form: it is cut away.
    var road: Dictionary = PanelScript.build_layers(w, {"object_id": "far", "site_kind": "road"})
    _check("road: one broken layer", road["broken"].size(), 1)
    _check("road: nothing mended", road["sound"].size(), 0)
    _check("unknown site: nothing", PanelScript.build_layers(w, {"object_id": "nope"})["broken"].size(), 0)
    w.free()
    _done()


func _test_stage_round_signal() -> void:
    var stage := Control.new()
    stage.set_script(StageScript)
    root.add_child(stage)
    var broken := [{"tex": _tex(32, 32), "pos": Vector2(-16, -27)}]
    var sound := [{"tex": _tex(32, 32), "pos": Vector2(-16, -27)}]
    stage.setup("Mend the Well", "windlass", broken, sound, 3, 1)
    _check("the stage takes room", stage.custom_minimum_size.x > 0.0 and stage.custom_minimum_size.y > 0.0, true)
    _check("a whole pixel scale", stage.k >= 1, true)
    var wins := []
    stage.round_won.connect(func(perfect: bool): wins.append(perfect))
    stage.press_key()
    _check("no game yet: nothing", wins.size(), 0)
    stage.start_game()
    var g = stage.game
    g.zone_x = 40
    g.marker = g.zone_x + g.zone_w / 2.0
    stage.press_key()
    _check("a won round signals once", wins.size(), 1)
    _check("dead centre is perfect", wins[0] if wins.size() > 0 else false, true)
    stage.set_progress(2)
    _check("progress taken", stage.steps_done, 2)
    stage.set_progress(9)
    _check("progress clamps to the steps", stage.steps_done, 3)
    stage.queue_free()
    _done()
