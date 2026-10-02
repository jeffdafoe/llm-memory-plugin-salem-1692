extends Node
## The player's own footsteps, and whether the player is under a roof
## (LLM-703). Only the player's PC — never a villager.
##
## Polls the PC's container each frame rather than wiring a signal: the sprite
## node is rebuilt on a sprite swap, and a poll needs nothing reconnected. A
## step sounds when the walk animation reaches a footfall frame (see
## is_footfall), so the steps keep the animation's pace whatever its frame rate,
## and stop the moment the walk animation does.
##
## The surface is the terrain under the PC. Water under a WALKING player can
## only be a bridge (nobody walks on water), so it sounds as wood.

## Set by main.gd: the world, and the player's PC actor id ("" before /pc/me
## answers and after logout — then nothing sounds).
var world: Node2D = null
var pc_actor_id := ""

var _last_anim := ""
var _last_frame := -1


func _process(_delta: float) -> void:
    if world == null or pc_actor_id == "" or not world.placed_npcs.has(pc_actor_id):
        _last_anim = ""
        _last_frame = -1
        # No PC, no roof: a PC removed while inside must not leave the rain
        # muffled for good.
        if Sound.indoors:
            Sound.set_indoors(false)
        return
    var container: Node2D = world.placed_npcs[pc_actor_id]
    var inside := bool(container.get_meta("inside", false))
    if inside != Sound.indoors:
        Sound.set_indoors(inside)
    var sprite: AnimatedSprite2D = world._npc_sprite(container)
    if sprite == null or sprite.sprite_frames == null:
        return
    var anim := String(sprite.animation)
    var frame := sprite.frame
    var walking := container.visible and container.has_meta("walking") and anim.ends_with("_walk") and sprite.is_playing()
    var stepped := frame != _last_frame or anim != _last_anim
    _last_anim = anim
    _last_frame = frame
    if not walking or not stepped:
        return
    if is_footfall(frame, sprite.sprite_frames.get_frame_count(anim)):
        Sound.play("step_" + surface_for(world.terrain_at(container.position)))


## A foot lands twice per walk cycle: on the first frame and halfway through.
static func is_footfall(frame: int, frame_count: int) -> bool:
    if frame_count < 2:
        return frame == 0
    return frame == 0 or frame == int(frame_count / 2.0)


## The step sound for a terrain type (terrain.gd).
static func surface_for(terrain: int) -> String:
    match terrain:
        1:
            return "dirt"
        4:
            return "stone"
        5, 6:
            return "wood"
    return "grass"
