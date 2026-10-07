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
    "_test_missing_layer_sheet",
    "_test_world_builds_doll",
    "_test_world_activity_animation",
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
    for prop in FarmerRig.PROPS.values():
        out[prop["sheet"]] = _blank(224, 32)
    return out


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
    doll.play("west_chop")
    doll.frame = 2
    _check("mirrored prop offset", prop.position, Vector2(64 - int(f["x"]) - 32, f["y"]))
    _check("mirrored prop flips", prop.flip_h, true)
    doll.play("north_chop")
    doll.frame = 2
    _check("overhead axe behind the body", prop.show_behind_parent, true)
    doll.play("south_walk")
    _check("prop hidden off a work animation", prop.visible, false)
    doll.free()
    _done()


func _test_missing_layer_sheet() -> void:
    var sheets := _sheets()
    sheets.erase(SHIRT)
    var doll := _doll(_three_layers(), sheets)
    _check("missing layer left out", _layer_children(doll).size(), 1)
    _check("body still built", doll.sprite_frames != null, true)
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
