extends Node
## The player's own duck quacks (LLM-800). A duck-pendant wearer's familiar is
## an NPC whose familiar_of meta names their PC. Sounds are not positional, so
## only the player's own duck is heard — another player's would sound as loud.
##
## It quacks once when it first comes to rest beside the player (the walk in
## from the map edge), then each time it sets off after the player (LLM-812):
## the player sets off outdoors with it beside them, and the duck starts walking
## within FOLLOW_WINDOW. A potter round a standing player (the engine's, LLM-812)
## has no set-off to follow and stays quiet. MIN_GAP keeps a burst of clicks
## from chaining quacks.
##
## When the player lies down to sleep (inn or tent) or starts to rest (a
## tiredness dwell, e.g. in a tree's shade), it quacks twice after REST_DELAY:
## the duck keeping watch, which it will one day do in earnest. main.gd calls
## pc_slept() and set_dwelling(); indoors is fine for this one, since the duck
## waits at the door.
## Polls each frame like footsteps.gd: nothing to reconnect when the duck
## comes and goes.

## Set by main.gd: the world, and the player's PC actor id ("" before /pc/me).
var world: Node2D = null
var pc_actor_id := ""

## Sent on each quack, before the sound (tests listen for it): "quack" or
## "watch" (the double quack on rest or sleep).
signal quacked(kind: String)

## How near (tiles) the duck must be to count as beside the player.
const NEAR_TILES := 3
## The fewest seconds between two quacks.
const MIN_GAP := 3.0
## Seconds from lying down or starting to rest to the double quack.
const REST_DELAY := 2.0
## Seconds after the player sets off in which the duck starting to walk counts
## as following them: the engine moves it once they are more than two tiles off,
## on a two-second tick.
const FOLLOW_WINDOW := 6.0

var _duck_id := ""
## The duck has come to rest beside the player at least once.
var _arrived := false
## Seconds since the last quack; starts past MIN_GAP so the first is never held.
var _since_quack := MIN_GAP
## The player was walking on the last frame, so a set-off is the frame it starts.
var _pc_was_walking := false
## The duck was walking on the last frame, so its set-off is the frame it starts.
var _duck_was_walking := false
## Seconds left in which the duck setting off is following the player; 0 when
## the player has not just set off.
var _follow_left := 0.0
## Seconds left until the double quack; below 0 when none is due.
var _watch_in := -1.0
## The last dwelling list from /pc/me, so a rest is the poll it starts.
var _dwelling: PackedStringArray = PackedStringArray()


func _process(delta: float) -> void:
    _since_quack += delta
    _follow_left = maxf(0.0, _follow_left - delta)
    var duck := _own_duck()
    if duck == null:
        _duck_id = ""
        _arrived = false
        _pc_was_walking = false
        _duck_was_walking = false
        _follow_left = 0.0
        _watch_in = -1.0
        return
    if _watch_in >= 0.0:
        _watch_in -= delta
        if _watch_in < 0.0 and _arrived and is_beside(duck.position, world.placed_npcs[pc_actor_id].position):
            _since_quack = 0.0
            quacked.emit("watch")
            print("[familiar_quack] watch")
            Sound.play("quack_watch")
    var duck_id := str(duck.get_meta("npc_id", ""))
    if duck_id != _duck_id:
        _duck_id = duck_id
        _arrived = false
    var pc: Node2D = world.placed_npcs[pc_actor_id]
    var pc_walking := pc.has_meta("walking")
    var set_off := pc_walking and not _pc_was_walking
    _pc_was_walking = pc_walking
    var duck_walking := duck.has_meta("walking")
    var duck_set_off := duck_walking and not _duck_was_walking
    _duck_was_walking = duck_walking
    var outdoors := not bool(pc.get_meta("inside", false))
    if set_off and _arrived and outdoors and is_beside(duck.position, pc.position):
        _follow_left = FOLLOW_WINDOW
    if duck_set_off and _follow_left > 0.0:
        _follow_left = 0.0
        _quack()
        return
    if not _arrived and outdoors and not duck_walking and is_beside(duck.position, pc.position):
        _arrived = true
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
    quacked.emit("quack")
    print("[familiar_quack] quack")
    Sound.play("quack")


## The player lay down to sleep (main.gd, on pc_sleep_started for their own PC).
func pc_slept() -> void:
    _watch_in = REST_DELAY


## The player's dwelling list from /pc/me (main.gd). A rest starts on the poll
## where "tiredness" first appears.
func set_dwelling(attrs: PackedStringArray) -> void:
    if attrs.has("tiredness") and not _dwelling.has("tiredness"):
        _watch_in = REST_DELAY
    _dwelling = attrs


## Whether two world positions are within NEAR_TILES tiles of each other.
static func is_beside(a: Vector2, b: Vector2) -> bool:
    var reach := NEAR_TILES * 32.0
    return absf(a.x - b.x) <= reach and absf(a.y - b.y) <= reach
