extends Node
## The player's own duck quacks (LLM-800). A duck-pendant wearer's familiar is
## an NPC whose familiar_of meta names their PC. Sounds are not positional, so
## only the player's own duck is heard — another player's would sound as loud.
##
## It quacks once when it first comes to rest beside the player (the walk in
## from the map edge), then each time the player sets off with it beside them
## outdoors (LLM-804). MIN_GAP keeps a burst of clicks from chaining quacks.
## Polls each frame like footsteps.gd: nothing to reconnect when the duck
## comes and goes.

## Set by main.gd: the world, and the player's PC actor id ("" before /pc/me).
var world: Node2D = null
var pc_actor_id := ""

## Sent on each quack, before the sound (tests listen for it).
signal quacked

## How near (tiles) the duck must be to count as beside the player.
const NEAR_TILES := 3
## The fewest seconds between two quacks.
const MIN_GAP := 3.0

var _duck_id := ""
## The duck has come to rest beside the player at least once.
var _arrived := false
## Seconds since the last quack; starts past MIN_GAP so the first is never held.
var _since_quack := MIN_GAP
## The player was walking on the last frame, so a set-off is the frame it starts.
var _pc_was_walking := false


func _process(delta: float) -> void:
    _since_quack += delta
    var duck := _own_duck()
    if duck == null:
        _duck_id = ""
        _arrived = false
        _pc_was_walking = false
        return
    var duck_id := str(duck.get_meta("npc_id", ""))
    if duck_id != _duck_id:
        _duck_id = duck_id
        _arrived = false
    var pc: Node2D = world.placed_npcs[pc_actor_id]
    var pc_walking := pc.has_meta("walking")
    var set_off := pc_walking and not _pc_was_walking
    _pc_was_walking = pc_walking
    if bool(pc.get_meta("inside", false)) or not is_beside(duck.position, pc.position):
        return
    if not _arrived:
        if duck.has_meta("walking"):
            return
        _arrived = true
        _quack()
    elif set_off:
        _quack()


## The player's familiar, or null.
func _own_duck() -> Node2D:
    if world == null or pc_actor_id == "" or not world.placed_npcs.has(pc_actor_id):
        return null
    for id in world.placed_npcs:
        var c: Node2D = world.placed_npcs[id]
        if is_instance_valid(c) and str(c.get_meta("familiar_of", "")) == pc_actor_id:
            return c
    return null


func _quack() -> void:
    if _since_quack < MIN_GAP:
        return
    _since_quack = 0.0
    quacked.emit()
    Sound.play("quack")


## Whether two world positions are within NEAR_TILES tiles of each other.
static func is_beside(a: Vector2, b: Vector2) -> bool:
    var reach := NEAR_TILES * 32.0
    return absf(a.x - b.x) <= reach and absf(a.y - b.y) <= reach
