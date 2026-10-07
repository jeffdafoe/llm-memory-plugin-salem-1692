class_name FarmerDoll
extends AnimatedSprite2D
## A Mana Seed farmer-base character (LLM-691), dressed at render time from
## its sprite's layer list. Each layer is one AnimatedSprite2D over a shared
## pack sheet, recoloured by the palette-swap shader, and every layer steps
## through the same FarmerRig cell on the same frame clock — the sheets share
## one cell layout, so one frame index keeps them in register.
##
## The body layer is this node itself. Everything that drives a villager's
## CharacterSprite (play, animation, sprite_frames.has_animation) therefore
## works unchanged; the clothing layers and the tool prop are children that
## copy this node's animation, frame and per-frame mirror on every change.
##
## Sprite payload shape (engine npc_sprite.rig / npc_sprite.layers):
##   rig: "farmer_base"
##   layers: [{sheet, ramps: {slot: index}, behind?}, ...] body first, then
##     bottom to top. A slot names a FarmerPalettes.BASE ramp the sheet is
##     drawn in; the index picks the replacement from that slot's family.
##     behind = draw under the body (the back half of a cloak).

const RIG := "farmer_base"
const SWAP_SHADER := preload("res://scripts/farmer_palette_swap.gdshader")
const MAX_SWAPS := 12

## Feet sit on row 43 of a 64px cell; this puts the container's position just
## above them, matching where the 32px NPC-pack sprites (anchor 0.9) stand.
const ANCHOR := Vector2(0.5, 0.64)

## Empty cell rows above a farmer's head (hats reach about row 9), for
## markers that sit just above the head.
const HEAD_ROOM := 8.0

static var _rig_animations: Dictionary = {}

var _layers: Array[AnimatedSprite2D] = []
var _prop: Sprite2D = null
var _prop_textures: Dictionary = {}

static func is_rig_sprite(sprite_data: Dictionary) -> bool:
    return str(sprite_data.get("rig", "")) == RIG

## Every sheet a farmer-base sprite needs before it can render: its layers,
## then the rig's prop sheets (small, and shared by every farmer).
static func sheet_paths(sprite_data: Dictionary) -> Array:
    var out: Array = []
    for spec in _layer_specs(sprite_data):
        # The DB checks only that layers is an array; never trust its members.
        if not (spec is Dictionary):
            continue
        var path := str(spec.get("sheet", ""))
        if path != "" and not out.has(path):
            out.append(path)
    for prop in FarmerRig.PROPS.values():
        var prop_path := str(prop.get("sheet", ""))
        if prop_path != "" and not out.has(prop_path):
            out.append(prop_path)
    return out

## The rig's animations with west derived from east: the guide draws
## left-facing frames as mirrored right-facing ones.
static func rig_animations() -> Dictionary:
    if not _rig_animations.is_empty():
        return _rig_animations
    var out: Dictionary = FarmerRig.ANIMATIONS.duplicate(true)
    for anim_name in FarmerRig.ANIMATIONS:
        if not str(anim_name).begins_with("east_"):
            continue
        var west: Dictionary = (FarmerRig.ANIMATIONS[anim_name] as Dictionary).duplicate(true)
        for f in west.get("frames", []):
            f["flip"] = not bool(f.get("flip", false))
        out["west_" + str(anim_name).substr(5)] = west
    _rig_animations = out
    return out

static func _layer_specs(sprite_data: Dictionary) -> Array:
    var layers = sprite_data.get("layers", [])
    return layers if layers is Array else []

## Build the doll. sheets maps sheet path -> Texture2D and must hold every
## path in sheet_paths(); a missing layer sheet just leaves that layer out.
func setup(sprite_data: Dictionary, sheets: Dictionary) -> void:
    centered = false
    var anims := rig_animations()
    # The first layer is the body and is this node. Without it nothing is
    # built — clothes with no body under them would float — so sprite_frames
    # stays null and the caller skips the doll. The other layers stack above
    # it in list order, each optional.
    var specs := _layer_specs(sprite_data)
    if specs.is_empty() or not (specs[0] is Dictionary):
        return
    var body_tex: Texture2D = sheets.get(str(specs[0].get("sheet", "")), null)
    if body_tex == null:
        return
    sprite_frames = _build_frames(body_tex, anims)
    material = _swap_material(specs[0].get("ramps", {}))
    for spec in specs.slice(1):
        if not (spec is Dictionary):
            continue
        var tex: Texture2D = sheets.get(str(spec.get("sheet", "")), null)
        if tex == null:
            continue
        var layer := AnimatedSprite2D.new()
        layer.centered = false
        layer.show_behind_parent = bool(spec.get("behind", false))
        layer.sprite_frames = _build_frames(tex, anims)
        layer.material = _swap_material(spec.get("ramps", {}))
        add_child(layer)
        _layers.append(layer)

    for prop_name in FarmerRig.PROPS:
        var prop: Dictionary = FarmerRig.PROPS[prop_name]
        var prop_tex: Texture2D = sheets.get(str(prop.get("sheet", "")), null)
        if prop_tex != null:
            _prop_textures[prop_name] = prop_tex
    _prop = Sprite2D.new()
    _prop.name = "Prop"
    _prop.centered = false
    _prop.region_enabled = true
    _prop.visible = false
    add_child(_prop)

    animation_changed.connect(_sync_layers)
    frame_changed.connect(_sync_layers)

## The swap material for one layer or prop: placeholder colours of each named
## slot mapped onto the chosen ramp of that slot's family.
static func _swap_material(ramps) -> ShaderMaterial:
    var from: Array = []
    var to: Array = []
    if ramps is Dictionary:
        for slot in ramps:
            if not FarmerPalettes.BASE.has(slot):
                continue
            var family: Array = FarmerPalettes.RAMPS.get(FarmerPalettes.FAMILY.get(slot, ""), [])
            var index := int(ramps[slot])
            if index < 0 or index >= family.size():
                continue
            var src: Array = FarmerPalettes.BASE[slot]
            var dst: Array = family[index]
            for c in min(src.size(), dst.size()):
                if from.size() < MAX_SWAPS:
                    from.append(_rgb(src[c]))
                    to.append(_rgb(dst[c]))
    var count := from.size()
    while from.size() < MAX_SWAPS:
        from.append(Vector3.ZERO)
        to.append(Vector3.ZERO)
    var mat := ShaderMaterial.new()
    mat.shader = SWAP_SHADER
    mat.set_shader_parameter("swap_count", count)
    mat.set_shader_parameter("swap_from", PackedVector3Array(from))
    mat.set_shader_parameter("swap_to", PackedVector3Array(to))
    return mat

static func _rgb(hex: String) -> Vector3:
    var c := Color.html(hex)
    return Vector3(c.r, c.g, c.b)

## One SpriteFrames per layer sheet with every rig animation. Speed 1000 fps
## makes a frame's relative duration its milliseconds; a held frame (ms 0)
## sits in a one-frame animation, so its duration never matters.
static func _build_frames(sheet: Texture2D, anims: Dictionary) -> SpriteFrames:
    var frames := SpriteFrames.new()
    var cell := FarmerRig.CELL_SIZE
    for anim_name in anims:
        frames.add_animation(anim_name)
        frames.set_animation_speed(anim_name, 1000.0)
        frames.set_animation_loop(anim_name, true)
        for f in anims[anim_name].get("frames", []):
            var index := int(f.get("cell", 0))
            var atlas := AtlasTexture.new()
            atlas.atlas = sheet
            atlas.region = Rect2(
                (index % FarmerRig.SHEET_COLUMNS) * cell,
                (index / FarmerRig.SHEET_COLUMNS) * cell,
                cell, cell)
            frames.add_frame(anim_name, atlas, max(1.0, float(f.get("ms", 0))))
    return frames

## Copy this node's animation and frame onto every layer, apply the frame's
## mirror, and place the frame's prop. Runs on animation_changed as well as
## frame_changed; the frame index is clamped because the switch to a shorter
## animation may signal before the frame resets.
func _sync_layers() -> void:
    var anim: Dictionary = rig_animations().get(animation, {})
    var frames: Array = anim.get("frames", [])
    if frames.is_empty():
        return
    var index: int = clampi(frame, 0, frames.size() - 1)
    var f: Dictionary = frames[index]
    var flip := bool(f.get("flip", false))
    flip_h = flip
    for layer in _layers:
        if layer.animation != animation:
            layer.animation = animation
        layer.frame = index
        layer.flip_h = flip
    _sync_prop(str(anim.get("prop", "")), f, flip)

func _sync_prop(prop_name: String, f: Dictionary, flip: bool) -> void:
    var p = f.get("prop", null)
    var tex: Texture2D = _prop_textures.get(prop_name, null)
    if not (p is Dictionary) or tex == null:
        _prop.visible = false
        return
    var size := FarmerRig.PROP_CELL_SIZE
    var columns: int = max(1, tex.get_width() / size)
    var pose := int(p.get("pose", 0))
    if _prop.texture != tex:
        _prop.texture = tex
        _prop.material = _swap_material(FarmerRig.PROPS[prop_name].get("ramps", {}))
    _prop.region_rect = Rect2((pose % columns) * size, (pose / columns) * size, size, size)
    var x := int(p.get("x", 0))
    if flip:
        x = FarmerRig.CELL_SIZE - x - size
    _prop.position = Vector2(x, int(p.get("y", 0)))
    _prop.flip_h = flip != bool(p.get("flip", false))
    _prop.show_behind_parent = bool(p.get("behind", false))
    _prop.visible = true
