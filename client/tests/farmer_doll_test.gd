extends SceneTree

## Headless harness for the LLM-691 farmer-base renderer: FarmerDoll stacks one
## AnimatedSprite2D per clothing layer over the shared FarmerRig cell table,
## and world.gd builds it from a sprite payload and swaps it onto work
## animations while a source activity runs.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/farmer_doll_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The purchased sheets are not in the repo, so every sheet here is a blank
## synthetic texture of the right size — the doll only reads sheet sizes and
## cell regions, never pixels. world.gd is instantiated off-tree via .new() so
## _ready() never fires.

const TESTS := [
    "_test_west_mirrors_east",
    "_test_sheet_paths",
    "_test_swap_material",
    "_test_layers_follow_body",
    "_test_prop_follows_frame",
    "_test_overlay_props",
    "_test_missing_layer_sheet",
    "_test_world_builds_doll",
    "_test_world_activity_animation",
    "_test_world_rest_spot_sits",
    "_test_world_swap_partial_sheets",
    "_test_world_load_sheets_settles",
]

const BODY := "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png"
const SHIRT := "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png"
const HAT := "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_boaterhat_00d.png"

var _world: Node2D = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _world = load("res://scripts/world.gd").new()
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    _world.free()
    print("\n[farmer_doll_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[farmer_doll_test] ALL PASS")
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


func _sprite_data(layers: Array) -> Dictionary:
    return {
        "id": "s1", "name": "Farmer", "sheet": BODY, "rig": "farmer_base",
        "frame_width": 64, "frame_height": 64, "render_scale": 2.0,
        "layers": layers, "animations": [],
    }


func _three_layers() -> Array:
    return [
        {"sheet": BODY, "ramps": {"skin": 0}},
        {"sheet": SHIRT, "ramps": {"c3": 19}},
        {"sheet": HAT, "ramps": {"c4": 37, "c3": 3}},
    ]


## Every sheet a farmer needs, as blank textures of the pack's sizes.
func _sheets() -> Dictionary:
    var out := {BODY: _blank(1024, 1024), SHIRT: _blank(1024, 1024), HAT: _blank(1024, 1024)}
    for prop_name in FarmerRig.PROPS:
        var size: Vector2i = PROP_SHEET_SIZES[prop_name]
        out[FarmerRig.PROPS[prop_name]["sheet"]] = _blank(size.x, size.y)
    return out

## The pack's real prop sheet sizes, so pose regions land as they do live.
const PROP_SHEET_SIZES := {"axe": Vector2i(224, 32), "scythe": Vector2i(512, 512), "mug": Vector2i(192, 512),
    "hammer": Vector2i(192, 512)}


func _doll(layers: Array, sheets: Dictionary) -> FarmerDoll:
    var doll := FarmerDoll.new()
    doll.setup(_sprite_data(layers), sheets)
    return doll


func _prop(doll: FarmerDoll) -> Sprite2D:
    return doll.get_node("Prop")


func _layer_children(doll: FarmerDoll) -> Array:
    return doll.get_children().filter(func(c): return c is AnimatedSprite2D)


func _test_west_mirrors_east() -> void:
    var anims := FarmerDoll.rig_animations()
    for kind in ["idle", "walk", "chop"]:
        var east: Array = anims["east_" + kind]["frames"]
        var west: Array = anims["west_" + kind]["frames"]
        _check("west_%s frame count" % kind, west.size(), east.size())
        for i in east.size():
            _check("west_%s[%d] flip is east's inverted" % [kind, i],
                bool(west[i].get("flip", false)), not bool(east[i].get("flip", false)))
            _check("west_%s[%d] cell" % [kind, i], west[i]["cell"], east[i]["cell"])
    _check("west_chop keeps its prop", anims["west_chop"].get("prop", ""), "axe")
    # The table itself is left untouched by the derivation.
    _check("FarmerRig keeps only authored directions", FarmerRig.ANIMATIONS.has("west_walk"), false)
    _done()


func _test_sheet_paths() -> void:
    var layers := _three_layers()
    layers.append({"sheet": SHIRT, "ramps": {"c3": 0}})
    var paths := FarmerDoll.sheet_paths(_sprite_data(layers))
    _check("layers first, in order", paths.slice(0, 3), [BODY, SHIRT, HAT])
    _check("repeated sheet listed once", paths.count(SHIRT), 1)
    _check("prop sheet listed", paths.has(FarmerRig.PROPS["axe"]["sheet"]), true)
    var junk := FarmerDoll.sheet_paths(_sprite_data([null, "bad", 7, {"sheet": BODY}]))
    _check("malformed layer entries skipped", junk.slice(0, 1), [BODY])
    _check("pack sprite is not a rig sprite", FarmerDoll.is_rig_sprite({"sheet": BODY}), false)
    _done()


func _test_swap_material() -> void:
    var mat := FarmerDoll._swap_material({"c3": 19, "nope": 1, "skin": 999})
    _check("only the valid slot swaps", mat.get_shader_parameter("swap_count"), 3)
    var from: PackedVector3Array = mat.get_shader_parameter("swap_from")
    var to: PackedVector3Array = mat.get_shader_parameter("swap_to")
    _check("uniform arrays padded", from.size(), FarmerDoll.MAX_SWAPS)
    var want_from := Color.html(FarmerPalettes.BASE["c3"][0])
    var want_to := Color.html(FarmerPalettes.RAMPS["c3"][19][0])
    _check("placeholder light", from[0].is_equal_approx(Vector3(want_from.r, want_from.g, want_from.b)), true)
    _check("ramp 19 light", to[0].is_equal_approx(Vector3(want_to.r, want_to.g, want_to.b)), true)
    var hat := FarmerDoll._swap_material({"c4": 37, "c3": 3})
    _check("two slots swap 4 + 3 colours", hat.get_shader_parameter("swap_count"), 7)
    _done()


func _test_layers_follow_body() -> void:
    var doll := _doll(_three_layers(), _sheets())
    var layers := _layer_children(doll)
    _check("body is the node, two layers above it", layers.size(), 2)
    _check("body has the rig animations", doll.sprite_frames.has_animation("west_chop"), true)
    doll.play("south_walk")
    doll.frame = 3
    for layer in layers:
        _check("layer follows animation", layer.animation, &"south_walk")
        _check("layer follows frame", layer.frame, 3)
        _check("layer mirrors on a flipped frame", layer.flip_h, true)
    _check("body mirrors on a flipped frame", doll.flip_h, true)
    doll.frame = 1
    _check("unflipped frame clears the mirror", (layers[0] as AnimatedSprite2D).flip_h, false)
    # Per-frame timing: speed 1000 fps turns the relative duration into ms.
    _check("walk frame lasts 135 ms", doll.sprite_frames.get_frame_duration("south_walk", 0), 135.0)
    doll.free()
    _done()


func _test_prop_follows_frame() -> void:
    var doll := _doll(_three_layers(), _sheets())
    var prop := _prop(doll)
    doll.play("east_chop")
    doll.frame = 2
    var f: Dictionary = FarmerRig.ANIMATIONS["east_chop"]["frames"][2]["prop"]
    _check("prop shown on a chop frame", prop.visible, true)
    _check("prop at the frame's offset", prop.position, Vector2(f["x"], f["y"]))
    _check("prop pose region", prop.region_rect, Rect2(int(f["pose"]) * 32, 0, 32, 32))
    _check("prop in front", prop.show_behind_parent, false)
    _check("east strike: blade up", prop.flip_v, true)
    doll.frame = 0
    _check("wind-up: pose upright", prop.flip_v, false)
    doll.play("west_chop")
    doll.frame = 2
    _check("mirrored prop offset", prop.position, Vector2(64 - int(f["x"]) - 32, f["y"]))
    _check("mirrored prop flips", prop.flip_h, true)
    _check("west strike: blade up", prop.flip_v, true)
    doll.play("south_chop")
    doll.frame = 2
    _check("south follow-through: blade turned left", prop.flip_h, true)
    _check("south follow-through: head down", prop.flip_v, true)
    doll.play("north_chop")
    doll.frame = 2
    _check("overhead axe behind the body", prop.show_behind_parent, true)
    _check("north after south: pose upright again", prop.flip_v, false)
    doll.play("south_walk")
    _check("prop hidden off a work animation", prop.visible, false)
    doll.free()
    _done()


## The pack's overlay sheets (LLM-719): a 64x64 overlay covers the body cell
## at 0,0, a 32x32 one sits at 16,16; both stay put when mirrored west.
func _test_overlay_props() -> void:
    var doll := _doll(_three_layers(), _sheets())
    var prop := _prop(doll)
    doll.play("east_scythe")
    doll.frame = 1
    _check("scythe overlay: 64 cell of row #03", prop.region_rect, Rect2(2 * 64, 2 * 64, 64, 64))
    _check("scythe overlay on the body cell", prop.position, Vector2.ZERO)
    doll.play("west_scythe")
    doll.frame = 1
    _check("mirrored scythe stays on the cell", prop.position, Vector2.ZERO)
    _check("mirrored scythe flips", prop.flip_h, true)
    doll.play("south_drink")
    doll.frame = 0
    _check("mug: 32 cell of row #11", prop.region_rect, Rect2(1 * 32, 10 * 32, 32, 32))
    _check("mug at 16,16", prop.position, Vector2(16, 16))
    doll.play("west_drink")
    _check("mirrored mug stays at 16,16", prop.position, Vector2(16, 16))
    # LLM-764: the smith's hammer and iron, overlay #09 — the sheet draws its
    # first two frames 12 px left of the last three.
    doll.play("east_smith")
    doll.frame = 0
    _check("smith: 32 cell of row #09", prop.region_rect, Rect2(1 * 32, 8 * 32, 32, 32))
    _check("smith's first frames at 13,10", prop.position, Vector2(13, 10))
    doll.frame = 3
    _check("smith's strike frames at 25,10", [prop.region_rect, prop.position], [Rect2(4 * 32, 8 * 32, 32, 32), Vector2(25, 10)])
    doll.play("west_smith")
    doll.frame = 3
    _check("mirrored smith: the strike on the west side", [prop.position, prop.flip_h], [Vector2(64 - 25 - 32, 10), true])
    _check("smith faces east or west only", [doll.sprite_frames.has_animation("south_smith"), doll.sprite_frames.has_animation("north_smith")], [false, false])
    doll.play("south_pull")
    _check("pull has no prop", prop.visible, false)
    doll.play("east_sit")
    _check("sit is one held frame", doll.sprite_frames.get_frame_count("east_sit"), 1)
    doll.free()
    _done()


func _test_missing_layer_sheet() -> void:
    var sheets := _sheets()
    sheets.erase(SHIRT)
    var doll := _doll(_three_layers(), sheets)
    _check("missing layer left out", _layer_children(doll).size(), 1)
    _check("body still built", doll.sprite_frames != null, true)
    var headless := _sheets()
    headless.erase(BODY)
    var floating := _doll(_three_layers(), headless)
    _check("no body, no doll (clothes would float)", floating.sprite_frames, null)
    _check("no body, no layers built", _layer_children(floating).size(), 0)
    floating.free()
    var bare := FarmerDoll.new()
    bare.setup(_sprite_data([{"sheet": "/nowhere.png"}]), {})
    _check("no layer at all builds nothing", bare.sprite_frames, null)
    bare.free()
    doll.free()
    _done()


func _test_world_builds_doll() -> void:
    _world._npc_sheets = _sheets()
    var data := _sprite_data(_three_layers())
    _check("all sheets ready", _world._sprite_sheets_ready(data), true)
    var spr: AnimatedSprite2D = _world._build_character_sprite(data)
    _check("rig sprite builds a FarmerDoll", spr is FarmerDoll, true)
    _check("named CharacterSprite", spr.name, &"CharacterSprite")
    _check("anchored on the farmer's feet", spr.position, Vector2(-64 * 2 * FarmerDoll.ANCHOR.x, -64 * 2 * FarmerDoll.ANCHOR.y))
    spr.free()
    _world._npc_sheets.erase(HAT)
    _check("not ready while a layer downloads", _world._sprite_sheets_ready(data), false)
    _check("not resolved while a layer downloads", _world._sprite_sheets_resolved(data), false)
    _world._failed_sheets[HAT] = true
    _check("resolved once the layer failed", _world._sprite_sheets_resolved(data), true)
    _world._failed_sheets.clear()
    _world._npc_sheets.clear()
    _done()


func _test_world_activity_animation() -> void:
    _world._npc_sheets = _sheets()
    var c := Node2D.new()
    var doll: AnimatedSprite2D = _world._build_character_sprite(_sprite_data(_three_layers()))
    c.add_child(doll)
    c.set_meta("facing", "east")
    doll.play("east_idle")
    c.set_meta("source_activity_kind", "repair")
    _world._apply_activity_animation(c)
    _check("repair swings the hatchet", doll.animation, &"east_chop")
    c.set_meta("source_activity_kind", "")
    _world._apply_activity_animation(c)
    _check("clear drops back to idle", doll.animation, &"east_idle")
    doll.play("east_walk")
    _world._apply_activity_animation(c)
    _check("clear leaves a walk alone", doll.animation, &"east_walk")
    c.set_meta("source_activity_kind", "stoke")
    doll.play("east_idle")
    _world._apply_activity_animation(c)
    _check("unmapped kind keeps idle", doll.animation, &"east_idle")

    # LLM-719: harvest picks its motion by the crop, refresh by the need; the
    # worker turns to the object.
    var field := Node2D.new()
    field.position = Vector2(100, 0)
    _world.placed_objects["o1"] = field
    c.set_meta("source_activity_object_id", "o1")
    c.set_meta("source_activity_kind", "harvest")
    c.set_meta("facing", "north")
    field.set_meta("refreshes", [{"attribute": "hunger", "gather_item": "wheat"}])
    _world._apply_activity_animation(c)
    _check("wheat: scythe", doll.animation, &"east_scythe")
    _check("wheat: turned to the field", c.get_meta("facing"), "east")
    field.set_meta("refreshes", [{"attribute": "hunger", "gather_item": "carrots"}])
    _world._apply_activity_animation(c)
    _check("carrots: pull", doll.animation, &"east_pull")
    field.set_meta("refreshes", [{"attribute": "hunger", "gather_item": "blueberries"}])
    _world._apply_activity_animation(c)
    _check("berries: pick", doll.animation, &"east_pick")
    field.position = Vector2(0, -100)
    c.set_meta("source_activity_kind", "refresh")
    field.set_meta("refreshes", [{"attribute": "", "gather_item": "water"}, {"attribute": "thirst", "gather_item": ""}])
    _world._apply_activity_animation(c)
    _check("well: drink (the drink row, not the carry row)", doll.animation, &"north_drink")
    _check("well: need for the tooltip", c.get_meta("source_activity_need"), "thirst")
    field.set_meta("refreshes", [{"attribute": "hunger", "gather_item": "blueberries"}])
    _world._apply_activity_animation(c)
    _check("eating at a bush: pick", doll.animation, &"north_pick")
    _check("bush: need for the tooltip", c.get_meta("source_activity_need"), "hunger")
    _world.placed_objects.erase("o1")
    field.free()
    _world._apply_activity_animation(c)
    _check("refresh, object not drawn: drink", doll.animation, &"north_drink")
    c.set_meta("source_activity_kind", "")
    _world._apply_activity_animation(c)
    _check("refresh clear: idle", doll.animation, &"north_idle")

    # Resting outdoors sits; waking stands up.
    c.set_meta("dormant", "resting")
    _world._apply_activity_animation(c)
    _check("resting: sit", doll.animation, &"north_sit")
    c.set_meta("dormant", "sleeping")
    _world._apply_activity_animation(c)
    _check("sleeping: no sit (sleep is indoors, LLM-750)", doll.animation, &"north_idle")
    c.set_meta("dormant", "resting")
    _world._apply_activity_animation(c)
    c.set_meta("dormant", "")
    _world._apply_activity_animation(c)
    _check("woken: idle", doll.animation, &"north_idle")
    c.set_meta("dormant", "resting")
    doll.play("north_walk")
    _world._apply_activity_animation(c)
    _check("resting flag never sits a walker", doll.animation, &"north_walk")
    c.free()

    # A pack sprite has no work animation and keeps standing.
    var pack := Node2D.new()
    var spr := AnimatedSprite2D.new()
    spr.name = "CharacterSprite"
    spr.sprite_frames = SpriteFrames.new()
    spr.sprite_frames.add_animation("south_idle")
    spr.sprite_frames.add_frame("south_idle", _blank(32, 32))
    pack.add_child(spr)
    spr.play("south_idle")
    pack.set_meta("facing", "south")
    pack.set_meta("source_activity_kind", "repair")
    _world._apply_activity_animation(pack)
    _check("pack sprite keeps idle", spr.animation, &"south_idle")
    pack.free()
    _world._npc_sheets.clear()
    _done()


## LLM-765: standing still at a rest spot's loiter pin (the Picnic Area) sits,
## facing it — through the arrival's refresh and the dwell after it. Walking,
## a nearer pin of another kind, or being inside does not sit.
func _test_world_rest_spot_sits() -> void:
    _world._npc_sheets = _sheets()
    var c := Node2D.new()
    var doll: AnimatedSprite2D = _world._build_character_sprite(_sprite_data(_three_layers()))
    c.add_child(doll)
    c.set_meta("facing", "east")
    doll.play("east_idle")
    # Anchored on tile (10, 10) with its pin a tile north, at (10, 9).
    var picnic := Node2D.new()
    picnic.position = Vector2(10 * 32 + 16, 10 * 32 + 16)
    picnic.set_meta("display_name", "Picnic Area")
    picnic.set_meta("effective_loiter_offset_x", 0)
    picnic.set_meta("effective_loiter_offset_y", -1)
    picnic.set_meta("refreshes", [{"attribute": "tiredness", "gather_item": ""}])
    _world.placed_objects["p1"] = picnic
    c.position = Vector2(10 * 32 + 16, 9 * 32 + 16)

    c.set_meta("source_activity_kind", "refresh")
    c.set_meta("source_activity_object_id", "p1")
    _world._apply_activity_animation(c)
    _check("arrival refresh at a rest spot: sit, not drink", doll.animation, &"south_sit")
    _check("faces the rest spot", c.get_meta("facing"), "south")
    _check("rest spot: need for the tooltip", c.get_meta("source_activity_need"), "tiredness")
    c.set_meta("source_activity_kind", "")
    c.set_meta("source_activity_object_id", "")
    _world._apply_activity_animation(c)
    _check("refresh over, still at the pin: stays seated", doll.animation, &"south_sit")

    c.set_meta("facing", "east")
    doll.play("east_idle")
    c.position = Vector2(11 * 32 + 16, 8 * 32 + 16)
    _world._apply_activity_animation(c)
    _check("one tile off the pin (diagonal): sit", doll.animation, &"south_sit")

    c.set_meta("walking", {"path": [Vector2(0, 0)]})
    doll.play("south_walk")
    _world._apply_activity_animation(c)
    _check("walking off: the walk keeps the sprite", doll.animation, &"south_walk")
    c.remove_meta("walking")
    c.position = Vector2(14 * 32 + 16, 9 * 32 + 16)
    doll.play("south_sit")
    _world._apply_activity_animation(c)
    _check("parked away from the pin: idle", doll.animation, &"south_idle")

    # A well's pin nearer than the picnic's wins the tile, as the engine's
    # resolver does: no sit.
    c.position = Vector2(10 * 32 + 16, 8 * 32 + 16)
    var well := Node2D.new()
    well.position = Vector2(10 * 32 + 16, 7 * 32 + 16)
    well.set_meta("display_name", "Well")
    well.set_meta("effective_loiter_offset_x", 0)
    well.set_meta("effective_loiter_offset_y", 1)
    well.set_meta("refreshes", [{"attribute": "thirst", "gather_item": ""}])
    _world.placed_objects["w1"] = well
    _world._apply_activity_animation(c)
    _check("nearer pin of another kind: no sit", doll.animation, &"south_idle")
    # An unnamed object is never attributed (the engine's Attributable).
    well.set_meta("display_name", "")
    _world._apply_activity_animation(c)
    _check("unnamed nearer pin is skipped: sit", doll.animation, &"south_sit")
    _world.placed_objects.erase("w1")
    well.free()

    # Equal distance: the lower id wins, as the engine compares its string ids.
    # The picnic pin (10, 9) and a well pin (10, 7) are both one tile from (10, 8).
    var tie := Node2D.new()
    tie.position = Vector2(10 * 32 + 16, 6 * 32 + 16)
    tie.set_meta("display_name", "Well")
    tie.set_meta("effective_loiter_offset_x", 0)
    tie.set_meta("effective_loiter_offset_y", 1)
    tie.set_meta("refreshes", [{"attribute": "thirst", "gather_item": ""}])
    _world.placed_objects["a1"] = tie
    doll.play("south_idle")
    _world._apply_activity_animation(c)
    _check("tie, lower id is the well: no sit", doll.animation, &"south_idle")
    _world.placed_objects.erase("a1")
    _world.placed_objects["z1"] = tie
    _world._apply_activity_animation(c)
    _check("tie, lower id is the picnic: sit", doll.animation, &"south_sit")
    _world.placed_objects.erase("z1")
    tie.free()

    c.set_meta("inside", true)
    _world._apply_activity_animation(c)
    _check("inside: no sit", doll.animation, &"south_idle")
    c.set_meta("inside", false)

    _world.placed_objects.erase("p1")
    picnic.free()
    c.free()
    _world._npc_sheets.clear()
    _done()


## A live outfit swap (npc_sprite_changed) with some sheets failed: the doll
## comes up without the failed layer or prop, as a first render does; without
## its body the old sprite stays. Driven at _swap_npc_sprite, the step
## _load_sprite_sheets hands off to once every download has finished.
func _test_world_swap_partial_sheets() -> void:
    var c := Node2D.new()
    var old := AnimatedSprite2D.new()
    old.name = "CharacterSprite"
    old.sprite_frames = SpriteFrames.new()
    c.add_child(old)
    _world.placed_npcs["abe"] = c
    var data := _sprite_data(_three_layers())

    var no_shirt := _sheets()
    no_shirt.erase(SHIRT)
    _world._npc_sheets = no_shirt
    _world._swap_npc_sprite("abe", data)
    var doll: AnimatedSprite2D = c.get_node_or_null("CharacterSprite")
    _check("swap lands without the failed shirt", doll is FarmerDoll, true)
    if doll is FarmerDoll:
        _check("one layer over the body", _layer_children(doll).size(), 1)

    var no_prop := _sheets()
    no_prop.erase(FarmerRig.PROPS["axe"]["sheet"])
    _world._npc_sheets = no_prop
    _world._swap_npc_sprite("abe", data)
    var swung: AnimatedSprite2D = c.get_node_or_null("CharacterSprite")
    _check("swap lands without the prop sheet", swung is FarmerDoll and swung != doll, true)
    if swung is FarmerDoll:
        swung.play("east_chop")
        swung.frame = 2
        _check("no prop drawn without its sheet", (swung.get_node("Prop") as Sprite2D).visible, false)

    var no_body := _sheets()
    no_body.erase(BODY)
    _world._npc_sheets = no_body
    _world._swap_npc_sprite("abe", data)
    _check("no body keeps the current sprite", c.get_node_or_null("CharacterSprite"), swung)

    _world.placed_npcs.erase("abe")
    c.free()
    _world._npc_sheets.clear()
    _done()


## _load_sprite_sheets calls back exactly once after every sheet settles,
## loaded or failed. Off-tree, HTTPRequest.request() cannot start, so every
## uncached sheet fails at once through get_or_load_npc_sheet's request-error
## path — the real loader, driven synchronously.
func _test_world_load_sheets_settles() -> void:
    var calls := {"n": 0}
    var count := func(): calls.n += 1
    var data := _sprite_data(_three_layers())

    _world._npc_sheets = _sheets()
    _world._load_sprite_sheets(data, count)
    _check("all cached: one callback", calls.n, 1)

    calls.n = 0
    var partial := _sheets()
    partial.erase(SHIRT)
    partial.erase(FarmerRig.PROPS["axe"]["sheet"])
    _world._npc_sheets = partial
    _world._load_sprite_sheets(data, count)
    _check("doll with failed shirt and prop: one callback", calls.n, 1)
    _check("failed sheets recorded", _world._failed_sheets.has(SHIRT), true)

    calls.n = 0
    _world._npc_sheets = {}
    _world._load_sprite_sheets({"sheet": "/npc/man.png", "animations": []}, count)
    _check("one-sheet sprite with failed sheet: no callback", calls.n, 0)

    _world._failed_sheets.clear()
    _world._npc_sheets.clear()
    _done()
