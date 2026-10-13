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
##
## `sheet` is the room without its furniture (walls, floor, rug). The
## furniture is `pieces` (LLM-767): each a rect {x, y, w, h} of `pieces_sheet`,
## which holds every piece where it stands in the room, drawn among the people
## and sorted by its `sort_y` — whoever's feet are above that line is drawn
## behind the piece, anyone below it in front. A seat's furniture sorts just
## above its sitter's feet, so they sit on it. `anims` are strips of `frames`
## equal frames side by side, top-left at x/y, played at `fps`; with a
## `sort_y` they sort like a piece, without one they are part of the wall.

const ROOMS := {
    # The Tavern ("Black House (Medium)", footprint 8 x 7 tiles).
    "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9": {
        "title": "Tavern",
        "sheet": "/tilesets/interiors/tavern_common.png",
        "pieces_sheet": "/tilesets/interiors/tavern_pieces.png",
        "size": Vector2i(128, 112),
        # The keeper stands in the gap inside the C bar: its back arm sorts
        # behind them, its front arm in front.
        "keeper": {"x": 112, "y": 80, "facing": "west"},
        "door": {"x": 64, "y": 111, "facing": "north"},
        # A sale is set on the bar's front arm (LLM-757): `bar` is where the
        # good stands (its bottom middle), `reach` where the keeper steps to
        # set it down; a seat's `serve` is where a meal lands on the table.
        "bar": {"x": 90, "y": 94},
        "reach": {"x": 102, "y": 80},
        "spots": [
            {"x": 16, "y": 98, "facing": "east", "pose": "seat", "serve": {"x": 31, "y": 87}},
            {"x": 56, "y": 98, "facing": "west", "pose": "seat", "serve": {"x": 39, "y": 87}},
            {"x": 76, "y": 100, "facing": "east", "pose": "stand"},
            {"x": 52, "y": 68, "facing": "south", "pose": "stand"},
            {"x": 68, "y": 74, "facing": "south", "pose": "stand"},
        ],
        "pieces": [
            {"x": 82, "y": 50, "w": 48, "h": 30, "sort_y": 79},
            {"x": 82, "y": 80, "w": 48, "h": 30, "sort_y": 99},
            {"x": 8, "y": 70, "w": 15, "h": 32, "sort_y": 95},
            {"x": 48, "y": 70, "w": 15, "h": 32, "sort_y": 95},
            {"x": 20, "y": 72, "w": 32, "h": 32, "sort_y": 96},
        ],
    },
    # The Inn ("Blue House (Medium)", footprint 8 x 7 tiles): an entry hall.
    # The keeper stands behind the desk at the foot of the stairs, and
    # `keeper_door` is the way they come and go (down the stairs). The bench
    # seats face the table, backs to us.
    "019d98af-ac9b-7833-8e03-5a7015bb5b0c": {
        "title": "Inn",
        "sheet": "/tilesets/interiors/inn_common.png",
        "pieces_sheet": "/tilesets/interiors/inn_pieces.png",
        "size": Vector2i(128, 112),
        "keeper": {"x": 44, "y": 66, "facing": "south"},
        "keeper_door": {"x": 10, "y": 60},
        "door": {"x": 64, "y": 111, "facing": "north"},
        "bar": {"x": 50, "y": 70},
        "reach": {"x": 48, "y": 66},
        "spots": [
            {"x": 86, "y": 110, "facing": "north", "pose": "seat", "serve": {"x": 86, "y": 86}},
            {"x": 110, "y": 110, "facing": "north", "pose": "seat", "serve": {"x": 110, "y": 86}},
            {"x": 14, "y": 102, "facing": "east", "pose": "seat"},
            {"x": 48, "y": 100, "facing": "north", "pose": "stand"},
            {"x": 112, "y": 70, "facing": "west", "pose": "stand"},
            {"x": 12, "y": 80, "facing": "east", "pose": "stand"},
        ],
        "pieces": [
            # The desk with its bottle; the keeper stands behind it.
            {"x": 20, "y": 54, "w": 48, "h": 36, "sort_y": 89},
            # Table and bench: the bench sitters sort in front of both.
            {"x": 74, "y": 74, "w": 48, "h": 32, "sort_y": 104},
            {"x": 74, "y": 98, "w": 48, "h": 16, "sort_y": 109},
            # Rocking chair and basket.
            {"x": 0, "y": 78, "w": 32, "h": 32, "sort_y": 101},
            {"x": 26, "y": 86, "w": 16, "h": 26, "sort_y": 111},
        ],
        "anims": [
            {"sheet": "/tilesets/interiors/inn_fire.png", "frames": 4, "fps": 8.0, "x": 96, "y": 38},
            {"sheet": "/tilesets/interiors/inn_pot.png", "frames": 4, "fps": 5.0, "x": 66, "y": 50, "sort_y": 81},
            {"sheet": "/tilesets/interiors/inn_candle.png", "frames": 4, "fps": 6.0, "x": 24, "y": 44, "sort_y": 90},
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
