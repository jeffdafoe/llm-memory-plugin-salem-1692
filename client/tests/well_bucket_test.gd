extends SceneTree

## Headless harness for the well's shared bucket (well_bucket.gd) and the
## drinkers world.gd counts at it: the first drinker hauls the bucket up and
## then kneels, later drinkers kneel, the bucket stays up while anyone drinks
## and goes down after the last one.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/well_bucket_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The purchased sheets are not in the repo, so every sheet is a blank texture
## of the right size. world.gd is instantiated off-tree via .new() so _ready()
## never fires; tweens are stepped by hand.

const WellBucket = preload("res://scripts/well_bucket.gd")

const TESTS := [
    "_test_is_well",
    "_test_bucket_places",
    "_test_raise_and_lower",
    "_test_check_drinkers",
    "_test_world_shared_bucket",
    "_test_world_removed_drinker",
]

const BODY := "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png"
const PROP_SHEET_SIZES := {"axe": Vector2i(224, 32), "scythe": Vector2i(512, 512), "mug": Vector2i(192, 512),
    "hammer": Vector2i(192, 512)}

var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _world = load("res://scripts/world.gd").new()
    _world._npc_sheets = _sheets()
    _check_test_list()
    for t in TESTS:
        _current = t
        await call(t)
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)
    _world.free()
    print("\n[well_bucket_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[well_bucket_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _done() -> void:
    _completed[_current] = true


func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t), true)
        _check("harness — %s exists" % t, has_method(t), true)
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name), true)


func _blank(w: int, h: int) -> Texture2D:
    return ImageTexture.create_from_image(Image.create(w, h, false, Image.FORMAT_RGBA8))


func _sheets() -> Dictionary:
    var out := {BODY: _blank(1024, 1024), WellBucket.SHEET: _blank(256, 64)}
    for prop_name in FarmerRig.PROPS:
        var size: Vector2i = PROP_SHEET_SIZES[prop_name]
        out[FarmerRig.PROPS[prop_name]["sheet"]] = _blank(size.x, size.y)
    return out


## A well as _place_object draws it: the 48x80 art at 2x, anchored at 0.5/0.85,
## with its drink row and its pail.
func _well() -> Node2D:
    var well := Node2D.new()
    var art := Sprite2D.new()
    art.centered = false
    art.scale = Vector2(2, 2)
    art.position = Vector2(-48 * 0.5 * 2, -80 * 0.85 * 2)
    well.add_child(art)
    well.set_meta("refreshes", [{"attribute": "thirst", "gather_item": ""}, {"attribute": "", "gather_item": "water"}])
    # In the tree, so the bucket's tweens step.
    root.add_child(well)
    return well


## A farmer-base drinker standing below the well, facing east.
func _drinker() -> Node2D:
    var c := Node2D.new()
    var doll: AnimatedSprite2D = _world._build_character_sprite({
        "id": "s1", "name": "Farmer", "sheet": BODY, "rig": "farmer_base",
        "frame_width": 64, "frame_height": 64, "render_scale": 2.0,
        "layers": [{"sheet": BODY, "ramps": {"skin": 0}}], "animations": [],
    })
    c.add_child(doll)
    c.position = Vector2(0, 64)
    c.set_meta("facing", "east")
    doll.play("east_idle")
    return c


func _drink(c: Node2D, well_id: String) -> void:
    c.set_meta("source_activity_kind", "refresh")
    c.set_meta("source_activity_object_id", well_id)
    _world._apply_activity_animation(c)


func _stop(c: Node2D) -> void:
    c.set_meta("source_activity_kind", "")
    c.set_meta("source_activity_object_id", "")
    _world._apply_activity_animation(c)


## Run the bucket's tween to its end.
func _finish(bucket) -> void:
    if bucket.tween != null and bucket.tween.is_valid():
        bucket.tween.custom_step(10.0)


func _test_is_well() -> void:
    var well := _well()
    _check("pail row: a well", WellBucket.is_well(well), true)
    well.set_meta("refreshes", [{"attribute": "thirst", "gather_item": ""}])
    _check("drink row alone: not a well", WellBucket.is_well(well), false)
    well.set_meta("refreshes", "junk")
    _check("malformed rows: not a well", WellBucket.is_well(well), false)
    well.free()
    _done()


func _test_bucket_places() -> void:
    var well := _well()
    var bucket = WellBucket.of(well, func() -> int: return 1)
    _check("made", bucket != null, true)
    _check("the same bucket asked again", WellBucket.of(well, func() -> int: return 1), bucket)
    _check("drawn at the well's scale", bucket.scale, Vector2(2, 2))
    _check("drawn over the well's art", bucket.get_index() > well.get_child(0).get_index(), true)
    _check("down: hidden", bucket.visible, false)
    var art: Sprite2D = well.get_child(0)
    _check("rim place in the well's coordinates", bucket._at(WellBucket.RIM), art.position + WellBucket.RIM * 2.0)
    var bare := Node2D.new()
    root.add_child(bare)
    _check("no art: no bucket", WellBucket.of(bare, func() -> int: return 1), null)
    bare.free()
    var off := _well()
    root.remove_child(off)
    _check("well not on the map: no bucket", WellBucket.of(off, func() -> int: return 1), null)
    off.free()
    well.free()
    _done()


func _test_raise_and_lower() -> void:
    var well := _well()
    var bucket = WellBucket.of(well, func() -> int: return 1)
    _check("first raise starts a haul", bucket.raise(), true)
    _check("rising", bucket.state, WellBucket.State.RISING)
    _check("shown", bucket.visible, true)
    _check("starts down the shaft", bucket.position, bucket._at(WellBucket.MOUTH + Vector2(0, WellBucket.DEPTH)))
    _check("starts unseen", bucket.modulate.a, 0.0)
    _check("raise while rising: no second haul", bucket.raise(), false)
    _finish(bucket)
    _check("up", bucket.state, WellBucket.State.UP)
    _check("on the rim", bucket.position, bucket._at(WellBucket.RIM))
    _check("seen", bucket.modulate.a, 1.0)
    _check("raise while up: no haul", bucket.raise(), false)
    bucket.lower()
    _check("sinking", bucket.state, WellBucket.State.SINKING)
    _check("raise while sinking: hauled again", bucket.raise(), true)
    _check("rising again", bucket.state, WellBucket.State.RISING)
    bucket.lower()
    _finish(bucket)
    _check("down", bucket.state, WellBucket.State.DOWN)
    _check("down: hidden again", bucket.visible, false)
    bucket.lower()
    _check("lower while down: stays down", bucket.state, WellBucket.State.DOWN)
    well.free()
    _done()


func _test_check_drinkers() -> void:
    var well := _well()
    var count := [1]
    var bucket = WellBucket.of(well, func() -> int: return count[0])
    bucket.raise()
    _finish(bucket)
    bucket.check_drinkers()
    _check("a drinker: stays up", bucket.state, WellBucket.State.UP)
    count[0] = 0
    bucket.check_drinkers()
    _check("nobody: goes down", bucket.state, WellBucket.State.SINKING)

    # The bucket's own Timer, unprompted: a drinker who vanishes without a clear
    # (nothing calls check_drinkers) still lets the bucket down.
    count[0] = 1
    bucket.raise()
    _finish(bucket)
    count[0] = 0
    await create_timer(WellBucket.CHECK * 2.0 + 0.2).timeout
    _check("timer: nobody left, goes down unprompted", bucket.state != WellBucket.State.UP, true)
    well.free()
    _done()


func _test_world_shared_bucket() -> void:
    var well := _well()
    _world.placed_objects["w1"] = well
    var a := _drinker()
    var b := _drinker()
    _world.placed_npcs["a"] = a
    _world.placed_npcs["b"] = b
    var doll_a: AnimatedSprite2D = a.get_node("CharacterSprite")
    var doll_b: AnimatedSprite2D = b.get_node("CharacterSprite")

    _drink(a, "w1")
    var bucket = well.get_node_or_null("WellBucket")
    _check("first drinker brings the bucket", bucket != null, true)
    _check("its sheet", bucket.texture, _world._npc_sheets[WellBucket.SHEET])
    _check("coming up", bucket.state, WellBucket.State.RISING)
    _check("first drinker hauls", doll_a.animation, &"north_pull")
    _check("faces the well", a.get_meta("facing"), "north")
    _check("counted at the well", _world._well_drinkers("w1"), 1)

    _drink(b, "w1")
    _check("second drinker kneels, no haul", doll_b.animation, &"north_kneel")
    _check("two at the well", _world._well_drinkers("w1"), 2)

    # The haul is over: the hauler kneels too.
    a.set_meta("well_haul_until_ms", 0)
    _world._apply_activity_animation(a)
    _check("hauler kneels after the haul", doll_a.animation, &"north_kneel")
    _finish(bucket)

    _stop(a)
    _check("first done: back to idle", doll_a.animation, &"north_idle")
    _check("one still drinking: bucket stays up", bucket.state, WellBucket.State.UP)
    _stop(b)
    _check("last done: bucket goes down", bucket.state, WellBucket.State.SINKING)
    _finish(bucket)

    # The next drinker hauls it up again.
    _drink(b, "w1")
    _check("bucket down: the next drinker hauls", doll_b.animation, &"north_pull")
    _stop(b)

    # Eating, or a harvest at the well (carrying water off), brings no bucket up.
    _finish(bucket)
    b.set_meta("source_activity_kind", "harvest")
    b.set_meta("source_activity_object_id", "w1")
    _world._apply_activity_animation(b)
    _check("water carry: no bucket", bucket.state, WellBucket.State.DOWN)
    _stop(b)

    _world.placed_npcs.erase("a")
    _world.placed_npcs.erase("b")
    _world.placed_objects.erase("w1")
    a.free()
    b.free()
    well.free()
    _done()


func _test_world_removed_drinker() -> void:
    var well := _well()
    _world.placed_objects["w1"] = well
    var a := _drinker()
    _world.placed_npcs["a"] = a
    _drink(a, "w1")
    var bucket = well.get_node("WellBucket")
    _finish(bucket)
    _world.remove_npc_by_id("a")
    _check("the only drinker removed: bucket goes down", bucket.state, WellBucket.State.SINKING)
    _world.placed_objects.erase("w1")
    well.free()
    _done()
