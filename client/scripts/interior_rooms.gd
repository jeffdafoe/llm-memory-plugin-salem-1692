class_name InteriorRooms
## Building interiors (LLM-750): the room a player sees when their character
## is inside a structure that has one. A room is drawn at the building's own
## size (its footprint, one 16 px art tile per map tile) from Mana Seed pack
## art, and holds a fixed set of spots where the people inside are drawn —
## the keeper behind the bar, a few others, and the door where the player's
## own character stands. People past the spots are counted, not drawn.
##
## Keyed by structure id. A spot's x/y is where the person's feet go, in room
## art pixels; pose "seat" sits a farmer-base villager in a chair (others
## stand). The art lives in the tiles repo (tilesets/interiors/) and on the VPS
## beside the other tilesets.

const ROOMS := {
    # The Tavern ("Black House (Medium)", footprint 8 x 7 tiles).
    "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9": {
        "title": "Tavern",
        "sheet": "/tilesets/interiors/tavern_common.png",
        "size": Vector2i(128, 112),
        "keeper": {"x": 112, "y": 86, "facing": "west"},
        "door": {"x": 64, "y": 111, "facing": "north"},
        "spots": [
            {"x": 16, "y": 98, "facing": "east", "pose": "seat"},
            {"x": 56, "y": 98, "facing": "west", "pose": "seat"},
            {"x": 76, "y": 100, "facing": "east", "pose": "stand"},
            {"x": 52, "y": 68, "facing": "south", "pose": "stand"},
            {"x": 68, "y": 74, "facing": "south", "pose": "stand"},
        ],
    },
}

static func room_for(structure_id: String) -> Dictionary:
    return ROOMS.get(structure_id, {})

static func has_room(structure_id: String) -> bool:
    return ROOMS.has(structure_id)

## Seat the people inside. `present` lists the actor ids to place (the keeper
## and the player's own character already taken out); `previous` is the last
## result's id -> spot index. Anyone already seated keeps their spot, so people
## do not shuffle as others come and go; a newcomer takes the spot their id
## hashes to, or the next free one. Returns {"spots": {id: index}, "more": n}
## where n counts the people who did not fit.
static func assign(spot_count: int, present: Array, previous: Dictionary) -> Dictionary:
    var taken := {}
    var out := {}
    var ids := present.duplicate()
    ids.sort()
    for id in ids:
        if previous.has(id):
            var keep := int(previous[id])
            if keep >= 0 and keep < spot_count and not taken.has(keep):
                taken[keep] = true
                out[id] = keep
    var more := 0
    for id in ids:
        if out.has(id):
            continue
        if taken.size() >= spot_count:
            more += 1
            continue
        var at := absi(hash(str(id))) % spot_count
        while taken.has(at):
            at = (at + 1) % spot_count
        taken[at] = true
        out[id] = at
    return {"spots": out, "more": more}
