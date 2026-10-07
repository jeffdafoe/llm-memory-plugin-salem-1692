extends SceneTree

## Headless harness for the town's repair work on the client (LLM-690):
## repair_games.gd (the four mini-games' rules), repair_stage.gd (pixel
## scale, object layout, play input → round signals) and repair_panel.gd (the
## title, the mended state, click matching, the layers built from a placed
## object, text tidy-up).
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/repair_panel_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## No purchased art is needed: the close-up art (client/assets/repair/) is
## drawn for the client and committed with it, village sprites are synthetic
## ImageTextures, and the stage's Title font falls back to the default font
## when the Mana Seed file is absent (as on CI).

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
    "_test_plumb_swings_through_level",
    "_test_plumb_straightens_with_the_work",
    "_test_difficulty_hardens_every_game",
    "_test_saw_binds_and_sticks",
    "_test_saw_stage_bends_the_blade",
    "_test_saw_same_however_time_arrives",
    "_test_pixel_and_object_scale",
    "_test_layers_bbox",
    "_test_title_and_sentence",
    "_test_mended_state_name",
    "_test_click_matches_offer",
    "_test_build_layers_fence_break",
    "_test_stage_round_signal",
    "_test_close_while_start_in_flight",
    "_test_retry_after_close_sends_nothing",
    "_test_old_retry_does_not_count_in_new_work",
    "_test_finish_tween_does_not_close_new_work",
    "_test_event_opens_during_probe",
    "_test_second_click_takes_over_probe",
    "_test_go_on_mending_resumes_without_start",
    "_test_state_change_without_texture_keeps_state",
    "_test_stale_step_answer_skips_new_work",
    "_test_matching_probe_calls_back_true",
    "_test_site_key",
    "_test_closeup_art_is_all_there",
    "_test_closeup_fills_the_area_at_scale_1",
    "_test_closeup_reveal_sweeps_only_the_damage",
    "_test_road_cut_and_rounds",
    "_test_panel_uses_the_closeup_else_village_sprites",
    "_test_hammer_swings_down_onto_the_nail",
    "_test_beats_for",
    "_test_staged_strips_match_their_beats",
    "_test_staged_layers_compose",
    "_test_staged_stage_plays_a_beat",
    "_test_sign_layers_match_the_stage",
    "_test_sign_stage_draws_the_arm_at_its_angle",
]

## Every piece tools/repair-art/build.ps1 writes, and what reads it.
const _ART_PIECES := [
    "fence-broken", "fence-mended", "well-broken", "well-mended", "shop-broken", "shop-mended",
    "crate-broken", "crate-mended", "sign-broken", "sign-mended",
    "sign-base", "sign-arm", "sign-loose", "sign-brace", "stake", "bob",
    "road-ground", "road-trunk", "road-face", "road-round", "road-brush",
    "plank", "nail", "nail-driven", "hammer", "bar", "peg", "wheel",
    "log-back", "log-face", "saw", "saw-bend",
    "fence-states", "fence-between", "fence-in", "fence-out",
    "well-states", "well-between", "well-in", "well-out",
    "shop-states", "shop-between", "shop-in", "shop-out",
    "crate-states", "crate-between", "crate-in", "crate-out",
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
    _check("signpost → plumb", Games.game_kind("minor", "signpost"), "plumb")
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




## Run a plumb game until its arm is level (at most `limit` seconds); true when
## it got there.
func _plumb_to_level(g, limit := 10.0) -> bool:
    var waited := 0.0
    while not g.is_level() and waited < limit:
        g.update(0.005)
        waited += 0.005
    return g.is_level()


## How long the arm stays level as it swings through, seconds.
func _plumb_level_window(g) -> float:
    _plumb_to_level(g)
    var level := 0.0
    while g.is_level():
        g.update(0.002)
        level += 0.002
    return level


## The arm starts at its full sag, swings up past level and back, and a press
## counts only near level; a hit holds it level.
func _test_plumb_swings_through_level() -> void:
    var g = Games.make("plumb", _rng())
    g.set_progress(0, 5)
    _check("starts at the full sag", g.angle, Games.Plumb.SAG)
    _check("sagging: a miss", g.press(null), Games.Result.MISS)
    _check("a miss does not hold", g.holding, false)
    var lo := INF
    var hi := -INF
    for i in 1200:
        g.update(0.01)
        lo = minf(lo, g.angle)
        hi = maxf(hi, g.angle)
    _check("it swings up past level, OVER at most", lo > -Games.Plumb.OVER - 0.01 and lo < -Games.Plumb.OVER + 0.1, true)
    _check("and back down to the sag", hi > Games.Plumb.SAG - 0.1, true)
    # It crosses level at about `speed` degrees a second, so the window is
    # about 2 * tolerance / speed long.
    var window := _plumb_level_window(g)
    var want: float = 2.0 * g.tolerance / g.speed
    _check("the level window is about 2 * tolerance / speed (%.3f s vs %.3f s)" % [window, want],
        absf(window - want) < want * 0.25, true)

    var h = Games.make("plumb", _rng())
    h.set_progress(0, 5)
    _check("the arm comes level", _plumb_to_level(h), true)
    _check("level: a hit", h.press(null), Games.Result.HIT)
    _check("a hit holds", h.holding, true)
    _check("held level", h.angle, 0.0)
    h.update(1.0)
    _check("held: the arm stays level", h.angle, 0.0)
    _check("held: input ignored", h.press(null), Games.Result.NONE)
    _done()


## Each round's swing reaches down a shorter sag, and a reload partway through
## resumes at the sag left.
func _test_plumb_straightens_with_the_work() -> void:
    var sag: float = Games.Plumb.SAG
    var g = Games.make("plumb", _rng())
    g.set_progress(0, 5)
    _plumb_to_level(g)
    g.press(null)
    g.set_progress(1, 5)
    _check("the sag shrinks a fifth", is_equal_approx(g.sag, sag * 0.8), true)
    _check("held level through the step", g.angle, 0.0)
    g.release()
    _check("picks up from level", absf(g.angle) < 0.001, true)
    var hi := -INF
    for i in 1200:
        g.update(0.01)
        hi = maxf(hi, g.angle)
    _check("swings down to the new sag, no further", hi > sag * 0.8 - 0.1 and hi < sag * 0.8 + 0.01, true)
    var r = Games.make("plumb", _rng())
    r.set_progress(3, 5)
    _check("a reload resumes at the sag left", is_equal_approx(r.angle, sag * 0.4), true)
    # The window to stop it level stays the same as the swing shortens.
    var early = Games.make("plumb", _rng())
    early.set_progress(0, 5)
    var late = Games.make("plumb", _rng())
    late.set_progress(4, 5)
    var we := _plumb_level_window(early)
    var wl := _plumb_level_window(late)
    _check("the last round's level window matches the first (%.3f s vs %.3f s)" % [wl, we], absf(wl - we) < we * 0.15, true)
    _done()


## Difficulty 0 plays as before; 1 plays every game at its hardest; past 1 is
## clamped.
func _test_difficulty_hardens_every_game() -> void:
    var w0 = Games.make("windlass", _rng(), 0.0)
    var w1 = Games.make("windlass", _rng(), 1.0)
    _check("windlass easy: speed", w0.speed, Games.Windlass.SPEED)
    _check("windlass easy: zone", w0.zone_w, Games.Windlass.START_ZONE)
    _check("windlass easy: floor", w0.min_zone, Games.Windlass.MIN_ZONE)
    _check("windlass hard: speed", w1.speed, Games.Windlass.HARD_SPEED)
    _check("windlass hard: zone", w1.zone_w, Games.Windlass.HARD_START_ZONE)
    _check("windlass hard: floor", w1.min_zone, Games.Windlass.HARD_MIN_ZONE)
    for i in 20:
        w1.release()
    _check("windlass hard: narrows to its own floor", w1.zone_w, Games.Windlass.HARD_MIN_ZONE)
    var wh = Games.make("windlass", _rng(), 0.5)
    _check("windlass half: between", wh.speed > w0.speed and wh.speed < w1.speed, true)
    _check("past 1 is clamped", Games.make("windlass", _rng(), 5.0).speed, Games.Windlass.HARD_SPEED)

    var h0 = Games.make("hammer", _rng(), 0.0)
    var h1 = Games.make("hammer", _rng(), 1.0)
    _check("hammer easy: stands", h0.up_time, Games.Hammer.UP_TIME)
    _check("hammer hard: stands", h1.up_time, Games.Hammer.HARD_UP_TIME)
    # A tap 4 px past the head's edge: inside the easy slack, outside the hard.
    for g in [h0, h1]:
        g.update(0.5)
    var off0: Vector2 = h0.nail_rect(h0.up).position + Vector2(-4, 5)
    var off1: Vector2 = h1.nail_rect(h1.up).position + Vector2(-4, 5)
    _check("hammer easy: a near tap drives it", h0.press(off0), Games.Result.HIT)
    _check("hammer hard: a near tap misses", h1.press(off1), Games.Result.MISS)
    h1.update(Games.Hammer.HARD_UP_TIME + 0.01)
    _check("hammer hard: a nail sinks sooner", h1.up, -1)

    var p0 = Games.make("plumb", _rng(), 0.0)
    var p1 = Games.make("plumb", _rng(), 1.0)
    _check("plumb easy: speed", p0.speed, Games.Plumb.SPEED)
    _check("plumb hard: tolerance", p1.tolerance, Games.Plumb.HARD_TOLERANCE)
    p0.set_progress(0, 5)
    p1.set_progress(0, 5)
    _check("plumb hard: a shorter level window", _plumb_level_window(p1) < _plumb_level_window(p0) * 0.5, true)

    _check("saw easy: strokes", Games.make("saw", _rng(), 0.0).strokes_needed, Games.Saw.STROKES)
    _check("saw hard: strokes", Games.make("saw", _rng(), 1.0).strokes_needed, Games.Saw.HARD_STROKES)
    var s0 = Games.make("saw", _rng(), 0.0)
    var s1 = Games.make("saw", _rng(), 1.0)
    _check("saw hard: a faster stroke", [s0.period, s1.period], [Games.Saw.PERIOD, Games.Saw.HARD_PERIOD])
    _check("saw hard: a narrower window", [s0.window, s1.window], [Games.Saw.WINDOW, Games.Saw.HARD_WINDOW])
    _check("saw easy still has a real window", s0.window < s0.period / 4.0, true)
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
    _placed(w, "diag", "fence", Vector2(132, 132))
    _placed(w, "crate2", "crate", Vector2(142, 100))
    var fence_offer := {"object_id": "mid", "site_kind": "minor", "form": "fence"}
    _check("the site itself", PanelScript.click_matches_offer(w, "mid", fence_offer), true)
    _check("the neighbour of the break", PanelScript.click_matches_offer(w, "left", fence_offer), true)
    _check("an overlay on the site", PanelScript.click_matches_offer(w, "debris", fence_offer), true)
    _check("a fence far along the run", PanelScript.click_matches_offer(w, "far", fence_offer), false)
    _check("a fence diagonal to the break", PanelScript.click_matches_offer(w, "diag", fence_offer), false)
    _check("another asset nearby", PanelScript.click_matches_offer(w, "crate", fence_offer), false)
    _check("no offer", PanelScript.click_matches_offer(w, "mid", {}), false)
    # Neighbour matching is for a fence break only: a crate's twin beside it
    # is other work.
    var crate_offer := {"object_id": "crate", "site_kind": "minor", "form": "crate"}
    _check("a same-asset crate beside a crate", PanelScript.click_matches_offer(w, "crate2", crate_offer), false)
    w.free()
    _done()


# --- the panel's async transitions (LLM-690 review) -------------------------

## A live panel in the tree, its requests recorded instead of sent.
func _live_panel(sent: Array) -> CanvasLayer:
    var p: CanvasLayer = PanelScript.new()
    root.add_child(p)
    p.world = _fake_world()
    p.send_hook = func(route: String): sent.append(route)
    return p


func _body(d: Dictionary) -> PackedByteArray:
    return JSON.stringify(d).to_utf8_buffer()


const _OFFER := {"object_id": "mid", "site_kind": "minor", "form": "fence", "fact": "A rail is down",
    "bounty": 3, "chest_can_pay": true, "steps": 3, "step_gap_ms": 0, "steps_done": 0}


func _started(p: CanvasLayer, steps_done: int = 0) -> void:
    var o := _OFFER.duplicate()
    o["yours"] = true
    o["steps_done"] = steps_done
    p._on_start_response(0, 200, PackedStringArray(), _body({"repair": o}))


func _free_panel(p: CanvasLayer) -> void:
    p.world.free()
    p.free()


func _test_close_while_start_in_flight() -> void:
    var sent := []
    var p := _live_panel(sent)
    p.show_offer(_OFFER)
    p._on_repair_pressed()
    _check("start sent", sent, ["start"])
    p.close()
    _started(p)
    _check("a late start answer does not reopen play", p.phase, p.Phase.CLOSED)
    _check("the panel stays hidden", p.visible, false)
    _free_panel(p)
    _done()


func _test_retry_after_close_sends_nothing() -> void:
    var sent := []
    var p := _live_panel(sent)
    p.show_offer(_OFFER)
    p._on_repair_pressed()
    _started(p)
    _check("playing", p.phase, p.Phase.PLAYING)
    p._on_round_won(false)
    _check("one step sent", sent.count("step"), 1)
    p._on_step_response(0, 429, PackedStringArray(), PackedByteArray())
    var token: int = p._token
    p.close()
    p._retry_step(token)
    _check("the retry after close sends nothing", sent.count("step"), 1)
    _free_panel(p)
    _done()


func _test_old_retry_does_not_count_in_new_work() -> void:
    var sent := []
    var p := _live_panel(sent)
    p.show_offer(_OFFER)
    p._on_repair_pressed()
    _started(p)
    p._on_round_won(false)
    p._on_step_response(0, 429, PackedStringArray(), PackedByteArray())
    var old: int = p._token
    p.close()
    p.show_offer(_OFFER)
    p._on_repair_pressed()
    _started(p)
    p._retry_step(old)
    _check("an old retry sends nothing in the new work", sent.count("step"), 1)
    p._retry_step(p._token)
    _check("even this work's retry needs a won round", sent.count("step"), 1)
    p._on_round_won(false)
    p._on_round_won(false)
    _check("one step per won round", sent.count("step"), 2)
    _free_panel(p)
    _done()


func _test_finish_tween_does_not_close_new_work() -> void:
    var sent := []
    var p := _live_panel(sent)
    var paid := []
    p.earned.connect(func(n: int): paid.append(n))
    p.show_offer(_OFFER)
    p._on_repair_pressed()
    _started(p, 2)
    p._on_round_won(false)
    p._on_step_response(0, 200, PackedStringArray(), _body({"steps_done": 3, "steps": 3, "done": true, "landed": true, "paid": 3}))
    _check("finishing", p.phase, p.Phase.FINISHING)
    var old: int = p._token
    p.close()
    var other := _OFFER.duplicate()
    other["object_id"] = "other"
    p.show_offer(other)
    p._end_after_pay(3, old)
    _check("the old finish leaves the new offer open", p.phase, p.Phase.OFFER)
    _check("and pays nothing twice", paid.size(), 0)
    _free_panel(p)
    _done()


func _test_event_opens_during_probe() -> void:
    for answer in [{"repair": null}, {"repair": _OFFER}]:
        var sent := []
        var p := _live_panel(sent)
        var calls := []
        p.probe_click("mid", func(opened: bool): calls.append(opened))
        _check("probe sent", sent, ["offer"])
        p.show_offer(_OFFER)  # the arrival thought lands first
        p._on_offer_response(0, 200, PackedStringArray(), _body(answer))
        _check("the probe's answer does not walk (%s)" % str(answer["repair"] != null), calls.size(), 0)
        _check("the panel stays open", p.is_open(), true)
        _free_panel(p)
    _done()


func _test_second_click_takes_over_probe() -> void:
    var sent := []
    var p := _live_panel(sent)
    var first := []
    var second := []
    p.probe_click("a", func(opened: bool): first.append(opened))
    p.probe_click("b", func(opened: bool): second.append(opened))
    _check("one request for both clicks", sent, ["offer"])
    p._on_offer_response(0, 200, PackedStringArray(), _body({"repair": null}))
    _check("the first click never acts", first.size(), 0)
    _check("the latest click gets the answer", second, [false])
    _free_panel(p)
    _done()


func _test_go_on_mending_resumes_without_start() -> void:
    var sent := []
    var p := _live_panel(sent)
    var o := _OFFER.duplicate()
    o["yours"] = true
    o["steps_done"] = 2
    p.show_offer(o)
    p._on_repair_pressed()
    _check("no second start for work under way", sent.count("start"), 0)
    _check("playing on", p.phase, p.Phase.PLAYING)
    _check("from where it was", p.stage.steps_done, 2)
    _free_panel(p)
    _done()


func _test_state_change_without_texture_keeps_state() -> void:
    var catalog := root.get_node("Catalog")
    catalog.assets["ghost"] = {"states": [{"state": "broken", "sheet": "/nowhere.png", "src_x": 0, "src_y": 0, "src_w": 16, "src_h": 16}]}
    var w := _fake_world()
    var n := _placed(w, "obj", "ghost", Vector2.ZERO, "sound")
    var ec = load("res://scripts/event_client.gd").new()
    ec.world = w
    ec._on_object_state_changed({"id": "obj", "state": "broken"})
    _check("the server's state is kept though it cannot be drawn", n.get_meta("current_state"), "broken")
    catalog.assets.erase("ghost")
    ec.free()
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


func _test_stale_step_answer_skips_new_work() -> void:
    var sent := []
    var p := _live_panel(sent)
    var paid := []
    p.earned.connect(func(n: int): paid.append(n))
    p.show_offer(_OFFER)
    p._on_repair_pressed()
    _started(p, 2)
    p._on_round_won(false)
    _check("work A's step out", sent.count("step"), 1)
    p.close()
    p.show_offer(_OFFER)
    p._on_repair_pressed()
    _started(p, 0)
    p._on_round_won(false)
    _check("work B's step waits on A's request", sent.count("step"), 1)
    # A's answer — the last step, paid — lands while B's round is won.
    p._on_step_response(0, 200, PackedStringArray(), _body({"steps_done": 3, "steps": 3, "done": true, "landed": true, "paid": 3}))
    _check("A's answer does not finish B", p.phase, p.Phase.PLAYING)
    _check("nor move B on", p.stage.steps_done, 0)
    _check("B's own step goes out", sent.count("step"), 2)
    p._on_step_response(0, 200, PackedStringArray(), _body({"steps_done": 1, "steps": 3, "done": false}))
    _check("B's own answer counts", p.stage.steps_done, 1)
    _check("nothing paid", paid.size(), 0)
    _free_panel(p)
    _done()


func _test_matching_probe_calls_back_true() -> void:
    var sent := []
    var p := _live_panel(sent)
    _placed(p.world, "mid", "fence", Vector2(100, 100))
    var calls := []
    p.probe_click("mid", func(opened: bool): calls.append(opened))
    p._on_offer_response(0, 200, PackedStringArray(), _body({"repair": _OFFER}))
    _check("the latest click hears the panel opened, once", calls, [true])
    _check("the panel is open", p.is_open(), true)
    _free_panel(p)
    _done()


func _test_site_key() -> void:
    _check("a fence break", StageScript.site_key("minor", "fence"), "fence")
    _check("a crate lid", StageScript.site_key("minor", "crate"), "crate")
    _check("a signpost", StageScript.site_key("minor", "signpost"), "sign")
    _check("a well", StageScript.site_key("well", ""), "well")
    _check("a business", StageScript.site_key("business", ""), "shop")
    _check("a road", StageScript.site_key("road", ""), "road")
    _check("the form wins over the kind", StageScript.site_key("well", "fence"), "fence")
    _check("anything else has none", StageScript.site_key("minor", "barrow"), "")
    _done()


## The art is committed with the client, so every piece is there — CI too.
func _test_closeup_art_is_all_there() -> void:
    for piece in _ART_PIECES:
        _check("piece %s loads" % piece, StageScript.art(piece) != null, true)
    for key in ["fence", "well", "shop", "crate", "sign"]:
        var b := StageScript.art(key + "-broken")
        var m := StageScript.art(key + "-mended")
        if b == null or m == null:
            continue
        _check("%s pictures are 176 wide" % key, b.get_size().x, 176.0)
        _check("%s broken and mended match in size" % key, b.get_size(), m.get_size())
    var wheel := StageScript.art("wheel")
    _check("the wheel is eight 23 px turns", wheel.get_size() if wheel != null else Vector2.ZERO, Vector2(184, 23))
    var hammer := StageScript.art("hammer")
    _check("the hammer is its swing, level to fully raised",
        hammer.get_size() if hammer != null else Vector2.ZERO,
        Vector2(StageScript.HAMMER_FRAME_W * (StageScript.HAMMER_REST + 1), StageScript.HAMMER_FRAME_H))
    var ground := StageScript.art("road-ground")
    var trunk := StageScript.art("road-trunk")
    if ground != null and trunk != null:
        _check("the trunk lies over the whole road picture", trunk.get_size(), ground.get_size())
    _check("no art, no piece", StageScript.art("no-such-piece"), null)
    _done()


func _test_closeup_fills_the_area_at_scale_1() -> void:
    var stage := Control.new()
    stage.set_script(StageScript)
    root.add_child(stage)
    var layers: Dictionary = StageScript.closeup_layers("fence")
    _check("the fence has a close-up", layers.is_empty(), false)
    if not layers.is_empty():
        stage.setup("Mend the Fence", "hammer", layers["broken"], layers["sound"], 5, 0, "fence")
        _check("drawn at the stage's own resolution", stage._obj_scale, 1)
        _check("the area is the picture's height", stage._obj_rect.size.y, 72.0)
        _check("the picture fills it from its top-left", stage._anchor(), stage._obj_rect.position)
        # The village sprites still scale up to fit.
        var broken := [{"tex": _tex(48, 16), "pos": Vector2(-24, -14)}]
        stage.setup("Mend the Fence", "hammer", broken, broken, 5, 0)
        _check("village sprites scale up", stage._obj_scale, 3)
    _check("no close-up for an unknown key", StageScript.closeup_layers(""), {})
    _check("a road's close-up is its ground alone", StageScript.closeup_layers("road")["sound"].size(), 0)
    stage.queue_free()
    _done()


## The broken and mended pictures match pixel for pixel outside the damage
## (each element seeds itself from its coordinates), so a repair changes only
## the damage — for the fence, the right-hand bay, from the nails on the middle
## post's right face (x 89).
func _test_closeup_reveal_sweeps_only_the_damage() -> void:
    var layers: Dictionary = StageScript.closeup_layers("fence")
    if layers.is_empty():
        _check("the fence has a close-up", false, true)
        _done()
        return
    var box := StageScript.layers_bbox(layers["broken"])
    var reveal := StageScript.diff_rect(layers["broken"], layers["sound"], box)
    _check("the pictures differ somewhere", reveal != box, true)
    _check("the left bay and its posts are untouched", reveal.position.x >= 89.0, true)
    _done()


func _test_road_cut_and_rounds() -> void:
    _check("uncut: the whole trunk", StageScript.road_cut_x(0, 10), StageScript.ROAD_TRUNK_R)
    _check("all cut: back to the road's edge", StageScript.road_cut_x(10, 10), StageScript.ROAD_CUT_MIN)
    _check("half way between", StageScript.road_cut_x(5, 10), 101)
    _check("never past the edge", StageScript.road_cut_x(12, 10), StageScript.ROAD_CUT_MIN)
    var shrinks := true
    for i in 10:
        if StageScript.road_cut_x(i + 1, 10) >= StageScript.road_cut_x(i, 10):
            shrinks = false
    _check("each round takes a section off", shrinks, true)
    var ground := StageScript.art("road-ground")
    var rnd := StageScript.art("road-round")
    if ground != null and rnd != null:
        var inside := true
        for at in StageScript.ROAD_ROUNDS:
            if not Rect2(Vector2.ZERO, ground.get_size()).encloses(Rect2(at, rnd.get_size())):
                inside = false
        _check("every stacked round lies inside the picture", inside, true)
    _check("ten places in the pile", StageScript.ROAD_ROUNDS.size(), 10)
    _done()


## A hit is judged at the click, but the hammer has to come down first: the
## nail stands and nothing flies until it lands, then it lifts back to rest.
## A miss swings too and thuds on the board when it lands.
func _test_hammer_swings_down_onto_the_nail() -> void:
    var stage := Control.new()
    stage.set_script(StageScript)
    root.add_child(stage)
    var layers: Dictionary = StageScript.closeup_layers("fence")
    stage.setup("Mend the Fence", "hammer", layers["broken"], layers["sound"], 5, 0, "fence")
    stage.start_game()
    var g = stage.game
    g.up = 2
    g.up_t = 0.5
    _check("at rest the hammer hangs raised", stage._hammer_frame(), StageScript.HAMMER_REST)
    var wins := []
    stage.round_won.connect(func(perfect: bool): wins.append(perfect))
    stage.press_key()
    _check("the hit counts at the click", wins.size(), 1)
    _check("the nail is counted driven at once", g.driven[2], true)
    _check("nothing flies before the hammer lands", stage._particles.size(), 0)
    _check("no jolt yet", stage._shake, 0.0)
    _check("it is swinging down", stage._hammer_frame() < StageScript.HAMMER_REST, true)
    _check("the struck nail still stands while it swings", stage._swing_nail_h, 8.0)
    stage._process(StageScript.SWING_DOWN + 0.01)
    _check("it lands level on the nail", stage._hammer_frame(), 0)
    _check("sparks fly when it lands", stage._particles.size() > 0, true)
    _check("and the panel jolts", stage._shake > 0.0, true)
    stage._process(StageScript.SWING_HOLD + StageScript.SWING_UP)
    _check("it lifts back to rest", stage._hammer_frame(), StageScript.HAMMER_REST)
    _check("the swing is over", stage._swing_t, -1.0)
    # A miss: no nail standing — a swing, and a thud only when it lands.
    g.release()
    g.up = -1
    stage._shake = 0.0
    var misses := []
    stage.missed.connect(func(): misses.append(true))
    stage.press_key()
    _check("a miss is counted at the click", misses.size(), 1)
    _check("a miss swings too", stage._swing_t >= 0.0, true)
    _check("no thud before it lands", stage._shake, 0.0)
    stage._process(StageScript.SWING_DOWN + 0.01)
    _check("it thuds on the board", stage._shake > 0.0, true)
    stage.queue_free()
    _done()


func _test_panel_uses_the_closeup_else_village_sprites() -> void:
    var sent := []
    var p := _live_panel(sent)
    p.show_offer(_OFFER)
    _check("a fence break shows its close-up", p.stage.site, "fence")
    p.close()
    var o := _OFFER.duplicate()
    o["object_id"] = "post"
    o["form"] = "barrow"
    _placed(p.world, "post", "fence", Vector2(100, 100))
    p.show_offer(o)
    _check("no close-up: the village sprites", p.stage.site, "")
    _free_panel(p)
    _done()


## The signpost's layers are drawn to the stage's numbers: the arm strip holds
## one frame per degree over the game's whole swing, and level hangs the bob on
## the stake's gold notch.
func _test_sign_layers_match_the_stage() -> void:
    var arm := StageScript.art("sign-arm")
    _check("the arm strip is SIGN_ARM_FRAMES frames",
        arm.get_size() if arm != null else Vector2.ZERO,
        Vector2(StageScript.SIGN_ARM_SIZE.x * StageScript.SIGN_ARM_FRAMES, StageScript.SIGN_ARM_SIZE.y))
    _check("the first frame is the swing's top", StageScript.SIGN_ARM_MIN_DEG, -int(Games.Plumb.OVER))
    _check("the last frame is the full sag", StageScript.SIGN_ARM_MIN_DEG + StageScript.SIGN_ARM_FRAMES - 1, int(Games.Plumb.SAG))
    var picture := StageScript.art("sign-broken")
    for piece in ["sign-base", "sign-loose", "sign-brace"]:
        var t := StageScript.art(piece)
        if t != null and picture != null:
            _check("%s is the picture's size" % piece, t.get_size(), picture.get_size())
    _check("frame for -10° is the first", StageScript.sign_arm_frame(-10.0), 0)
    _check("frame for level", StageScript.sign_arm_frame(0.4), -StageScript.SIGN_ARM_MIN_DEG)
    _check("frame for 0.6° is the next", StageScript.sign_arm_frame(0.6), 1 - StageScript.SIGN_ARM_MIN_DEG)
    _check("frame past the sag is the last", StageScript.sign_arm_frame(40.0), StageScript.SIGN_ARM_FRAMES - 1)

    var stake := StageScript.art("stake")
    var bob := StageScript.art("bob")
    if stake == null or bob == null:
        _check("the stake and bob load", false, true)
        _done()
        return
    # The notch is the brightest gold on the stake's left column.
    var img := stake.get_image()
    if img.is_compressed():
        img.decompress()
    var mark := -1
    for y in img.get_height():
        if img.get_pixel(0, y).is_equal_approx(Color8(240, 224, 96)):
            mark = y
    _check("the stake has its notch", mark >= 0, true)
    var top := StageScript.sign_cord_top(0).floor()
    var bob_mid := top.y + StageScript.SIGN_CORD_LEN + floorf(bob.get_size().y / 2.0)
    _check("level: the bob hangs on the notch", bob_mid, StageScript.SIGN_STAKE.y + mark)
    _check("the bob hangs beside the stake, not in it", top.x + 2 < StageScript.SIGN_STAKE.x + 1, true)
    _check("a couple of degrees off level, the bob is visibly off the mark",
        StageScript.sign_cord_top(2).y - StageScript.sign_cord_top(0).y >= 2.0, true)
    _check("at the full sag the bob would lie in the grass",
        StageScript.sign_cord_top(int(Games.Plumb.SAG)).y + StageScript.SIGN_CORD_LEN > StageScript.SIGN_BOB_REST_Y, true)
    _done()


## The stage shows the arm at rest, sagging less as the work goes on; plays it
## from the game while playing; and level once mended. The game takes the
## stage's difficulty, which the panel takes from the offer.
func _test_sign_stage_draws_the_arm_at_its_angle() -> void:
    var stage := Control.new()
    stage.set_script(StageScript)
    root.add_child(stage)
    var layers: Dictionary = StageScript.closeup_layers("sign")
    stage.setup("Straighten the Signpost", "plumb", layers["broken"], layers["sound"], 5, 0, "sign")
    _check("the sign close-up", stage.site, "sign")
    _check("the play strip holds only the pips", stage._play_h(), 10)
    _check("at rest: the full sag", stage._sign_angle(false), Games.Plumb.SAG)
    stage.set_progress(2)
    _check("two of five done: the sag shrinks", is_equal_approx(stage._sign_angle(false), Games.Plumb.SAG * 0.6), true)
    stage.difficulty = 1.0
    stage.start_game()
    _check("the game takes the stage's difficulty", stage.game.speed, Games.Plumb.HARD_SPEED)
    _check("and its progress", is_equal_approx(stage.game.sag, Games.Plumb.SAG * 0.6), true)
    stage.game.update(0.3)
    _check("playing: the game's angle", stage._sign_angle(false), stage.game.angle)
    _check("mended: level", stage._sign_angle(true), 0.0)
    stage.queue_free()

    var sent := []
    var p := _live_panel(sent)
    var o := _OFFER.duplicate()
    o["form"] = "signpost"
    o["yours"] = true
    o["difficulty"] = 1.0
    p.show_offer(o)
    p._on_repair_pressed()
    _check("the signpost plays the plumb game", p.stage.game_kind, "plumb")
    _check("at the offer's difficulty", p.stage.game.speed if p.stage.game != null else 0.0, Games.Plumb.HARD_SPEED)
    _check("the hint names the bob and the mark", p.hint_label.text, p.stage.game.hint())
    p.close()
    o.erase("difficulty")
    p.show_offer(o)
    p._on_repair_pressed()
    _check("an offer with no difficulty plays easiest", p.stage.game.speed if p.stage.game != null else 0.0, Games.Plumb.SPEED)
    _free_panel(p)
    _done()


func _test_beats_for() -> void:
    _check("none done, none shown", StageScript.beats_for(0, 5, 5), 0)
    _check("a beat a step", StageScript.beats_for(3, 5, 5), 3)
    _check("all done, all shown", StageScript.beats_for(5, 5, 5), 5)
    _check("more steps than beats: spread", StageScript.beats_for(5, 10, 5), 2)
    _check("more beats than steps: several a step", StageScript.beats_for(1, 2, 5), 2)
    _check("done past the steps ends mended", StageScript.beats_for(9, 4, 12), 12)
    _done()


## The generator's strips agree with STAGED: one frame per beat (and one more
## state), the close-up's size, and broken / mended are the first and last
## states.
func _test_staged_strips_match_their_beats() -> void:
    var w := StageScript.closeup_width()
    for key in StageScript.STAGED:
        var n: int = StageScript.STAGED[key].size()
        var states := StageScript.art(key + "-states")
        var broken := StageScript.art(key + "-broken")
        if states == null or broken == null:
            _check("%s strips load" % key, false, true)
            continue
        var h := broken.get_size().y
        _check("%s states: N+1 frames" % key, states.get_size(), Vector2(w * (n + 1), h))
        for piece in ["between", "in", "out"]:
            var t := StageScript.art("%s-%s" % [key, piece])
            _check("%s %s: N frames" % [key, piece], t.get_size() if t != null else Vector2.ZERO, Vector2(w * n, h))
        var si := _img(states)
        _check("%s broken is the first state" % key, si.get_region(Rect2i(0, 0, w, int(h))).get_data() == _img(broken).get_data(), true)
        var mended := StageScript.art(key + "-mended")
        _check("%s mended is the last state" % key, si.get_region(Rect2i(n * w, 0, w, int(h))).get_data() == _img(mended).get_data(), true)
        for b in n:
            var m: Dictionary = StageScript.STAGED[key][b]
            var changed: bool = StageScript.strip_used_rect(key + "-in", b).has_area() or StageScript.strip_used_rect(key + "-out", b).has_area()
            _check("%s beat %d changes the picture" % [key, b], changed, true)
            _check("%s beat %d has a pop" % [key, b], str(m.get("say", "")) != "", true)
            if m.get("nails", false):
                _check("%s beat %d puts nails in" % [key, b], StageScript.strip_used_rect(key + "-in", b).has_area(), true)
    _done()


func _img(t: Texture2D) -> Image:
    var i := t.get_image()
    if i.is_compressed():
        i.decompress()
    i.convert(Image.FORMAT_RGBA8)
    return i


## Every beat's layers rebuild its pictures exactly: between + in is the state
## after, between + out the state before.
func _test_staged_layers_compose() -> void:
    var w := StageScript.closeup_width()
    for key in StageScript.STAGED:
        var n: int = StageScript.STAGED[key].size()
        var states := StageScript.art(key + "-states")
        if states == null:
            continue
        var si := _img(states)
        var bi := _img(StageScript.art(key + "-between"))
        var ii := _img(StageScript.art(key + "-in"))
        var oi := _img(StageScript.art(key + "-out"))
        var h := si.get_height()
        for b in n:
            var frame := Rect2i(b * w, 0, w, h)
            var after := bi.get_region(frame)
            after.blend_rect(ii, frame, Vector2i.ZERO)
            _check("%s beat %d: between + in is the next state" % [key, b], after.get_data() == si.get_region(Rect2i((b + 1) * w, 0, w, h)).get_data(), true)
            var before := bi.get_region(frame)
            before.blend_rect(oi, frame, Vector2i.ZERO)
            _check("%s beat %d: between + out is the state before" % [key, b], before.get_data() == si.get_region(frame).get_data(), true)
    _done()


## A step plays its beat: queued, the old pieces going and the new flying in,
## a pop when it lands, then the next state at rest. A reload shows the state
## reached with nothing to play.
func _test_staged_stage_plays_a_beat() -> void:
    var stage := Control.new()
    stage.set_script(StageScript)
    root.add_child(stage)
    var layers: Dictionary = StageScript.closeup_layers("fence")
    stage.setup("Mend the Fence", "hammer", layers["broken"], layers["sound"], 5, 0, "fence")
    _check("the fence is staged", stage._is_staged(), true)
    _check("at rest: nothing to play", stage._beat_queue.size(), 0)
    stage.set_progress(1)
    _check("a step queues its beat", stage._beat_queue, [0] as Array[int])
    _check("the beat counts as shown", stage._beats_shown, 1)
    stage._process(0.2)
    _check("part way: still playing", stage._beat_queue.size(), 1)
    stage._process(0.2)
    _check("it landed: a pop", stage._pops.size(), 1)
    _check("…saying what was done", stage._pops[0]["text"], StageScript.STAGED["fence"][0]["say"])
    _check("…with dust", stage._particles.size() > 0, true)
    stage._process(0.3)
    _check("played out", stage._beat_queue.size(), 0)
    stage.set_progress(3)
    _check("two steps at once: two beats in turn", stage._beat_queue, [1, 2] as Array[int])
    for i in 20:
        stage._process(0.1)
    _check("both played", stage._beat_queue.size(), 0)
    stage.finish(3)
    _check("finishing plays the rest", stage._beat_queue, [3, 4] as Array[int])
    # One long frame (a stalled tab) plays through both, each landing once.
    # (_advance_beat alone: _process would also age the pops by the same frame.)
    stage._pops.clear()
    stage._advance_beat(StageScript.BEAT_TIME * 2.0 + 0.05)
    _check("a long frame plays every beat it covers", stage._beat_queue.size(), 0)
    _check("…each landing once", stage._pops.size(), 2)
    # The strips' rects were read at setup, one image read per strip.
    _check("the in strip's rects are cached whole", (StageScript._used_cache.get("fence-in", []) as Array).size(), StageScript.STAGED["fence"].size())
    stage.setup("Mend the Fence", "hammer", layers["broken"], layers["sound"], 5, 3, "fence")
    _check("a reload shows the beats reached", stage._beats_shown, 3)
    _check("…with nothing to play", stage._beat_queue.size(), 0)
    stage.set_progress(3)
    _check("…nor on resuming at the same step", stage._beat_queue.size(), 0)
    stage.queue_free()
    _done()


## Run a saw's blade to end k (k * period on its clock).
func _saw_to_end(g, k: int) -> void:
    g.update(k * g.period - g.blade_t)


## The saw keeps a beat (LLM-716): a stroke counts only on the side the blade
## is reaching, while that end is lit, once per end.
func _test_saw_keeps_the_beat() -> void:
    var g = Games.make("saw", _rng())
    var left := Vector2(10, 20)
    var right := Vector2(Games.PLAY_W - 10, 20)
    _check("at the start nothing is lit", g.lit(), false)
    _check("a tap off the beat binds", g.press(right), Games.Result.MISS)
    _check("a bind is counted", g.binds, 1)
    _saw_to_end(g, 1)
    _check("the right end is lit as the blade reaches it", g.lit(), true)
    _check("tapping it is a stroke", g.press(right), Games.Result.PROGRESS)
    _check("a stroke clears the binds", g.binds, 0)
    _check("a second tap at the same end binds", g.press(right), Games.Result.MISS)
    _check("and loses the stroke", g.strokes, 0)
    g.update(g.period / 2.0)
    _check("mid-stroke nothing is lit", g.lit(), false)
    _saw_to_end(g, 2)
    _check("the wrong side binds", g.press(right), Games.Result.MISS)
    _check("the right side still counts in the window", g.press(left), Games.Result.PROGRESS)
    var r := Games.Result.NONE
    var k := 3
    while g.strokes < g.strokes_needed and k < 20:
        _saw_to_end(g, k)
        r = g.press(null)
        k += 1
    _check("a key press on every beat cuts through", r, Games.Result.HIT)
    _check("held after the cut", g.press(left), Games.Result.NONE)
    g.release()
    _check("a fresh section", g.strokes, 0)
    _done()


## Binds cost a stroke; three in a row stick the saw (the true fail), and once
## a run has started a window let pass binds too.
func _test_saw_binds_and_sticks() -> void:
    var left := Vector2(10, 20)
    var right := Vector2(Games.PLAY_W - 10, 20)
    var g = Games.make("saw", _rng())
    _saw_to_end(g, 1)
    g.press(right)
    _saw_to_end(g, 2)
    g.press(left)
    _check("two strokes in", g.strokes, 2)
    _check("first bind: a stroke lost", [g.press(right), g.strokes], [Games.Result.MISS, 1])
    _check("second bind", [g.press(right), g.strokes], [Games.Result.MISS, 0])
    _check("third bind: stuck", g.press(right), Games.Result.FAIL)
    _check("stuck for its time", g.stuck_left, Games.Saw.STUCK_TIME)
    _check("the cut starts over", g.strokes, 0)
    _check("no tap while stuck", g.press(left), Games.Result.NONE)
    var held_at: float = g.blade_t
    g.update(Games.Saw.STUCK_TIME / 2.0)
    _check("the blade stops while stuck", g.blade_t, held_at)
    g.update(Games.Saw.STUCK_TIME)
    _check("free again", g.stuck_left, 0.0)
    _check("binds start fresh", g.binds, 0)

    var w = Games.make("saw", _rng())
    w.update(w.period * 4.0)
    _check("before a run starts, a window let pass is no bind", w.take_events(), [])
    _saw_to_end(w, 5)
    w.press(null)
    _saw_to_end(w, 6)
    w.update(w.window * 2.0)
    _check("a run started: a window let pass binds", w.take_events(), [Games.Result.MISS])
    _check("and costs the stroke", w.strokes, 0)
    w.update(w.period * 2.0)
    _check("three let pass stick the saw", w.take_events(), [Games.Result.MISS, Games.Result.FAIL])
    _check("stuck", w.stuck_left > 0.0, true)
    _done()


## The stage bows the blade on a bind and springs it back; a stuck blade stays
## bowed.
func _test_saw_stage_bends_the_blade() -> void:
    var g = Games.make("saw", _rng())
    _check("straight before any bind", StageScript.saw_bend(g), 0)
    g.press(null)
    _check("buckled up at the bind", StageScript.saw_bend(g), StageScript.SAW_BEND_MIN)
    g.update(0.12)
    _check("springs past straight", StageScript.saw_bend(g) > 0, true)
    g.update(StageScript.SAW_BEND_TIME)
    _check("settled", StageScript.saw_bend(g), 0)
    g.press(null)
    g.press(null)
    _check("stuck: bowed and held", StageScript.saw_bend(g), StageScript.SAW_STUCK_BEND)
    _done()


## One long update (a stall) ends in the same state as the same time given in
## short frames: three windows let pass, the stuck spell, and on past it.
func _test_saw_same_however_time_arrives() -> void:
    var total := 0.0
    var games: Array = []
    for frames in [1, 7, 400]:
        var g = Games.make("saw", _rng())
        _saw_to_end(g, 1)
        g.press(null)
        total = 3.0 * g.period + Games.Saw.STUCK_TIME + 0.3
        var events: Array = []
        for i in frames:
            g.update(total / frames)
            events.append_array(g.take_events())
        games.append({"g": g, "events": events})
    var a = games[0]["g"]
    _check("one update: three binds, the last sticks", games[0]["events"], [Games.Result.MISS, Games.Result.MISS, Games.Result.FAIL])
    _check("the stuck bind lands when its window closed", absf(a.bound_at - (a.period + 3.0 * a.period + a.window)) < 0.001, true)
    for i in [1, 2]:
        var b = games[i]["g"]
        _check("frames %d: same events" % i, games[i]["events"], games[0]["events"])
        _check("frames %d: same stuck time" % i, absf(b.stuck_left - a.stuck_left) < 0.001, true)
        _check("frames %d: same blade clock" % i, absf(b.blade_t - a.blade_t) < 0.001, true)
        _check("frames %d: same bind time" % i, absf(b.bound_at - a.bound_at) < 0.001, true)
    _check("free of the stuck spell, the blade moved on", a.stuck_left == 0.0 and a.blade_t > 4.0 * a.period + a.window, true)
    _done()
