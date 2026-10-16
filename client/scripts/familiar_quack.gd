extends Node
## The player's own duck quacks (LLM-800). A duck-pendant wearer's familiar is
## an NPC whose familiar_of meta names their PC. Sounds are not positional, so
## only the player's own duck is heard — another player's would sound as loud.
##
## It quacks once when it first comes to rest beside the player (the walk in
## from the map edge), then now and then while it stays beside them outdoors.
## Polls each frame like footsteps.gd: nothing to reconnect when the duck
## comes and goes.

## Set by main.gd: the world, and the player's PC actor id ("" before /pc/me).
var world: Node2D = null
var pc_actor_id := ""

## Sent on each quack, before the sound (tests listen for it).
signal quacked

## How near (tiles) the duck must be to count as beside the player.
const NEAR_TILES := 3
## Seconds between quacks while it stays beside the player.
const QUACK_GAP := Vector2(30.0, 90.0)

var _duck_id := ""
## The duck has come to rest beside the player at least once.
var _arrived := false
var _next_quack := 0.0


func _process(delta: float) -> void:
    var duck := _own_duck()
    if duck == null:
        _duck_id = ""
        _arrived = false
        return
    var duck_id := str(duck.get_meta("npc_id", ""))
    if duck_id != _duck_id:
        _duck_id = duck_id
        _arrived = false
    var pc: Node2D = world.placed_npcs[pc_actor_id]
    if duck.has_meta("walking") or bool(pc.get_meta("inside", false)) or not is_beside(duck.position, pc.position):
        return
    if not _arrived:
        _arrived = true
        _quack()
        return
    _next_quack -= delta
    if _next_quack <= 0.0:
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
    quacked.emit()
    Sound.play("quack")
    _next_quack = randf_range(QUACK_GAP.x, QUACK_GAP.y)


## Whether two world positions are within NEAR_TILES tiles of each other.
static func is_beside(a: Vector2, b: Vector2) -> bool:
    var reach := NEAR_TILES * 32.0
    return absf(a.x - b.x) <= reach and absf(a.y - b.y) <= reach
