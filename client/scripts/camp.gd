class_name Camp
## The player's camp (LLM-798), the client's half: the camp's asset ids, and the
## motion a player plays on each beat of pitching or striking it. The engine runs
## the beats and sends each one on npc_source_activity_changed (kind pitch /
## strike, beat, target); the tent goes up and comes down on the ordinary object
## frames.

const TENT_ASSET_ID := "019e5f00-c401-7a10-9e00-000000798001"
const FIRE_ASSET_ID := "019e5f00-c401-7a10-9e00-000000798002"

## Seconds into the tent's beat its motion turns over: pitching crouches to lay
## out the canvas, then drives the pegs with the back of the hatchet; striking
## pulls the pegs, then rolls the canvas up.
const TENT_SWITCH := 2.0


## Whether kind is camp work.
static func is_camp_work(kind: String) -> bool:
    return kind == "pitch" or kind == "strike"


## The work animation for a beat of pitching or striking, `into` seconds after
## the beat began. Each prop is set down, or taken up, with a crouch.
static func motion(kind: String, beat: String, into: float) -> String:
    if beat != "tent":
        return "pick"
    if kind == "pitch":
        return "pick" if into < TENT_SWITCH else "chop"
    return "pull" if into < TENT_SWITCH else "pick"


## Whether an object's state change is a tent standing up from its bundle — the
## moment for the dust puff.
static func tent_raised(asset_id: String, from_state: String, to_state: String) -> bool:
    return asset_id == TENT_ASSET_ID and from_state == "packed" and to_state != "packed"


## How near the fire, in tiles (king's move), the player stands to light it —
## the engine's own reach (sim.LightCampFire).
const FIRE_REACH := 2

## The line the fire prompt opens with: why the player would light it.
const FIRE_PROMPT := "Sleeping with a fire will give you better rest."


## Whether a camp fire in `state` is out — unlit, or burnt to ashes — the only
## times a click on it offers the fire game.
static func fire_out(state: String) -> bool:
    return state == "" or state == "unlit" or state == "ashes"


## Whether a player standing on `tile` is near enough the fire anchored on
## `fire_tile` to light it.
static func near_fire(tile: Vector2i, fire_tile: Vector2i) -> bool:
    return maxi(absi(tile.x - fire_tile.x), absi(tile.y - fire_tile.y)) <= FIRE_REACH
