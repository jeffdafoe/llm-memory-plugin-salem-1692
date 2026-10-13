class_name InteriorRooms
## Building interiors (LLM-750): the room a player sees when their character
## is inside a structure that has one. A room is drawn at the building's own
## size (its footprint, one 16 px art tile per map tile) from Mana Seed pack
## art. This holds the art; where people stand is the engine's (LLM-768): each
## room's floor plan (Catalog.rooms, from /api/village/rooms) — a grid of
## squares, the door, the keeper's place and the stations — and each person's
## square (container meta `room_square`). with_layout() merges the plan into
## the art as the spots the window draws at.
##
## Keyed by structure id. The art lives in the tiles repo (tilesets/interiors/)
## and on the VPS beside the other tilesets.
##
## `sheet` is the room without its furniture (walls, floor, rug). The
## furniture is `pieces` (LLM-767): each a rect {x, y, w, h} of `pieces_sheet`,
## which holds every piece where it stands in the room, drawn among the people
## and sorted by its `sort_y` — whoever's feet are above that line is drawn
## behind the piece, anyone below it in front. A seat's furniture sorts just
## above its sitter's feet, so they sit on it. `anims` are strips of `frames`
## equal frames side by side, top-left at x/y, played at `fps`; with a
## `sort_y` they sort like a piece, without one they are part of the wall.
## `bar` is where a sale is set down (the good's bottom middle) and `reach`
## where the keeper steps to set it there.

const ROOMS := {
    # The Tavern ("Black House (Medium)", footprint 8 x 7 tiles). The keeper
    # stands in the gap inside the C bar: its back arm sorts behind them, its
    # front arm in front.
    "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9": {
        "title": "Tavern",
        "sheet": "/tilesets/interiors/tavern_common.png",
        "pieces_sheet": "/tilesets/interiors/tavern_pieces.png",
        "size": Vector2i(128, 112),
        # A sale is set on the bar's front arm (LLM-757).
        "bar": {"x": 90, "y": 94},
        "reach": {"x": 102, "y": 80},
        "pieces": [
            {"x": 82, "y": 50, "w": 48, "h": 30, "sort_y": 79},
            {"x": 82, "y": 80, "w": 48, "h": 30, "sort_y": 99},
            {"x": 8, "y": 70, "w": 15, "h": 32, "sort_y": 95},
            {"x": 48, "y": 70, "w": 15, "h": 32, "sort_y": 95},
            {"x": 20, "y": 72, "w": 32, "h": 32, "sort_y": 96},
        ],
    },
    # The Inn ("Blue House (Medium)", footprint 8 x 7 tiles): an entry hall.
    # The keeper stands behind the desk at the foot of the stairs and comes
    # and goes by the stairs (the plan's keeper_door). The bench seats face the
    # table, backs to us.
    "019d98af-ac9b-7833-8e03-5a7015bb5b0c": {
        "title": "Inn",
        "sheet": "/tilesets/interiors/inn_common.png",
        "pieces_sheet": "/tilesets/interiors/inn_pieces.png",
        "size": Vector2i(128, 112),
        "bar": {"x": 50, "y": 70},
        "reach": {"x": 48, "y": 66},
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

## One art square: the room art is drawn one 16 px tile per footprint tile.
const SQUARE_PX := 16

## Around a station's point (where the feet go): the box a tap on it falls in.
const STATION_BOX := Rect2(-8, -24, 16, 26)
## How far (room art px, to where the feet go) a tap on furniture looks for a
## place to stand: about two and a half squares.
const TAP_REACH := 40.0

static func room_for(structure_id: String) -> Dictionary:
    return ROOMS.get(structure_id, {})

static func has_room(structure_id: String) -> bool:
    return ROOMS.has(structure_id)

## The room's art with the engine's floor plan merged in as spots — `door`,
## `keeper`, `keeper_door` (when the plan has one) and `spots` (the stations) —
## each {x, y, facing, pose, square[, serve {x, y}]}: x/y where the feet go in
## room art pixels, square the plan's [x, y]. Without a plan the art comes
## back as it is, with no spots: nobody is drawn until the plan lands.
static func with_layout(art: Dictionary, plan: Dictionary) -> Dictionary:
    var room := art.duplicate()
    if plan.is_empty():
        return room
    room["door"] = _spot(plan.get("door", {}))
    room["keeper"] = _spot(plan.get("keeper", {}))
    if plan.get("keeper_door", null) is Dictionary:
        room["keeper_door"] = _spot(plan["keeper_door"])
    var spots: Array = []
    for st in plan.get("stations", []):
        spots.append(_spot(st))
    room["spots"] = spots
    room["grid"] = plan.get("grid", [])
    return room

## Where someone on `square` is drawn: the door, the keeper's place, the
## keeper's way in or a station on that square, else the square's own floor —
## its middle, feet near its bottom, facing south, standing. {} for a room with
## no plan merged or a square off its grid.
static func spot_at(room: Dictionary, square: Vector2i) -> Dictionary:
    if not room.has("door"):
        return {}
    var size: Vector2i = room.get("size", Vector2i(128, 112))
    if square.x < 0 or square.y < 0 or square.x * SQUARE_PX >= size.x or square.y * SQUARE_PX >= size.y:
        return {}
    var candidates: Array = [room["door"], room["keeper"]] + room.get("spots", [])
    if room.has("keeper_door"):
        candidates.append(room["keeper_door"])
    for spot in candidates:
        if spot.get("square", Vector2i(-1, -1)) == square:
            return spot
    return {"x": square.x * SQUARE_PX + SQUARE_PX / 2, "y": square.y * SQUARE_PX + SQUARE_PX - 2,
        "facing": "south", "pose": "stand", "square": square}

## Whether a player may walk on or to `square` (LLM-768): open floor on the
## plan's grid that is not the keeper's place or the keeper's way in — the
## engine's rule, so a tap the engine would refuse is not sent.
static func walkable(room: Dictionary, square: Vector2i) -> bool:
    var grid: Array = room.get("grid", [])
    if square.y < 0 or square.y >= grid.size() or square.x < 0 or square.x >= str(grid[square.y]).length():
        return false
    if str(grid[square.y])[square.x] != ".":
        return false
    if square == room.get("keeper", {}).get("square", Vector2i(-1, -1)):
        return false
    return not room.has("keeper_door") or square != room["keeper_door"].get("square", Vector2i(-1, -1))

## The square a tap at `art` (room art px) asks for: a seat or standing place
## whose person-sized box around its point holds the tap (so a tap on a chair
## is its seat), else the floor square under the tap, else — a tap on furniture
## (the bar counter, the table) — the nearest free place to stand within
## TAP_REACH of it, `held` being the squares others stand on; (-1, -1) when
## there is none (a tap high on the wall).
static func square_at(room: Dictionary, art: Vector2, held := {}) -> Vector2i:
    for spot in [room.get("door", {})] + room.get("spots", []):
        var at := Vector2(int(spot.get("x", -999)), int(spot.get("y", -999)))
        if Rect2(at + STATION_BOX.position, STATION_BOX.size).has_point(art):
            return spot.get("square", Vector2i(-1, -1))
    var square := Vector2i(floori(art.x / SQUARE_PX), floori(art.y / SQUARE_PX))
    if walkable(room, square):
        return square
    # Seats and standing places first (a tap on the table is a chair), plain
    # floor only when none is in reach.
    var places: Array = []
    for spot in [room.get("door", {})] + room.get("spots", []):
        places.append(spot.get("square", Vector2i(-1, -1)))
    var best := _nearest_free(room, art, places, held)
    if best != Vector2i(-1, -1):
        return best
    var floor: Array = []
    var grid: Array = room.get("grid", [])
    for y in grid.size():
        for x in str(grid[y]).length():
            floor.append(Vector2i(x, y))
    return _nearest_free(room, art, floor, held)

## Of `squares`, the walkable one not in `held` drawn nearest `art` within
## TAP_REACH, or (-1, -1).
static func _nearest_free(room: Dictionary, art: Vector2, squares: Array, held: Dictionary) -> Vector2i:
    var best := Vector2i(-1, -1)
    var best_d := TAP_REACH
    for sq in squares:
        if held.has(sq) or not walkable(room, sq):
            continue
        var spot := spot_at(room, sq)
        var d := art.distance_to(Vector2(int(spot.get("x", 0)), int(spot.get("y", 0))))
        if d <= best_d:
            best = sq
            best_d = d
    return best

## The points a person walks through for a walked `path` (wire squares, from
## where they stood to where they stop): each square's middle on the way, then
## where they are drawn on the last.
static func route(room: Dictionary, path: Array) -> Array:
    var points: Array = []
    for i in range(1, path.size()):
        var square := square_of(path[i])
        if i == path.size() - 1:
            var spot := spot_at(room, square)
            points.append(Vector2(int(spot.get("x", 0)), int(spot.get("y", 0))))
        else:
            points.append(Vector2(square.x * SQUARE_PX + SQUARE_PX / 2, square.y * SQUARE_PX + SQUARE_PX - 2))
    return points

## A square from the wire ([x, y]), or (-1, -1) for none.
static func square_of(value) -> Vector2i:
    if value is Array and value.size() == 2:
        return Vector2i(int(value[0]), int(value[1]))
    return Vector2i(-1, -1)

## Whether the room's keeper may walk on `square` (LLM-769): what a player may,
## the keeper's own place, and the way out from behind their counter ('k' on
## the grid, the keeper's alone) — the engine's KeeperPath rule.
static func keeper_walkable(room: Dictionary, square: Vector2i) -> bool:
    return walkable(room, square) or square == room.get("keeper", {}).get("square", Vector2i(-1, -1)) \
        or _keeper_way(room, square)

## Whether `square` is the keeper's way out from behind their counter ('k').
static func _keeper_way(room: Dictionary, square: Vector2i) -> bool:
    var grid: Array = room.get("grid", [])
    return square.y >= 0 and square.y < grid.size() and square.x >= 0 and square.x < str(grid[square.y]).length() \
        and str(grid[square.y])[square.x] == "k"

## The squares the keeper walks from `from` to `to`, both ends included,
## stepping up, down, left and right over keeper_walkable squares; [] when `to`
## is not one or cannot be reached. `from` may be any square (where they are
## drawn mid-walk). In a room with a keeper's way they leave and come back to
## their place only by it, never through the counter (the engine's keeperStep).
static func keeper_path(room: Dictionary, from: Vector2i, to: Vector2i) -> Array:
    if not keeper_walkable(room, to):
        return []
    if from == to:
        return [from]
    var home: Vector2i = room.get("keeper", {}).get("square", Vector2i(-1, -1))
    var has_way := false
    for row in room.get("grid", []):
        has_way = has_way or str(row).contains("k")
    var prev := {from: from}
    var queue: Array = [from]
    while not queue.is_empty():
        var at: Vector2i = queue.pop_front()
        for d in [Vector2i(0, -1), Vector2i(1, 0), Vector2i(0, 1), Vector2i(-1, 0)]:
            var next: Vector2i = at + d
            if prev.has(next) or not keeper_walkable(room, next):
                continue
            if has_way and (at == home or next == home) and not _keeper_way(room, next if at == home else at):
                continue
            prev[next] = at
            if next == to:
                var path: Array = [to]
                while path.front() != from:
                    path.push_front(prev[path.front()])
                return path
            queue.append(next)
    return []

## The square under a point in room art px.
static func square_under(at: Vector2) -> Vector2i:
    return Vector2i(floori(at.x / SQUARE_PX), floori(at.y / SQUARE_PX))

## A square's middle, where someone walking through it goes.
static func middle(square: Vector2i) -> Vector2:
    return Vector2(square.x * SQUARE_PX + SQUARE_PX / 2, square.y * SQUARE_PX + SQUARE_PX - 2)

## Where the keeper stands to set a good down at `toward` (room art px — a
## seat's serve point on the table, or where a standing buyer is): the open
## floor square nearest it that the keeper can walk to from their place, never
## a seat nor one somebody holds (`taken`: square -> true). (-1, -1) when
## there is none — every such square is held, or the room has no way out
## from behind its counter.
static func serve_square(room: Dictionary, toward: Vector2, taken: Dictionary) -> Vector2i:
    var home: Vector2i = room.get("keeper", {}).get("square", Vector2i(-1, -1))
    var seats := {}
    for spot in room.get("spots", []):
        if str(spot.get("pose", "")) == "seat":
            seats[spot.get("square", Vector2i(-1, -1))] = true
    var best := Vector2i(-1, -1)
    var best_cost := INF
    var grid: Array = room.get("grid", [])
    for y in grid.size():
        for x in str(grid[y]).length():
            var sq := Vector2i(x, y)
            if seats.has(sq) or taken.has(sq) or not walkable(room, sq) or keeper_path(room, home, sq).is_empty():
                continue
            var spot := spot_at(room, sq)
            var cost := Vector2(int(spot["x"]), int(spot["y"])).distance_to(toward)
            if cost < best_cost:
                best_cost = cost
                best = sq
    return best

static func _spot(station: Dictionary) -> Dictionary:
    var at := square_of(station.get("at", null))
    var spot := {"x": at.x, "y": at.y, "facing": str(station.get("facing", "south")),
        "pose": str(station.get("pose", "stand")), "square": square_of(station.get("square", null))}
    var serve := square_of(station.get("serve", null))
    if serve != Vector2i(-1, -1):
        spot["serve"] = {"x": serve.x, "y": serve.y}
    return spot

## Spread people over a stall's spots — its customers, the hands beside its
## keeper (a room's people stand on their engine squares). `present` lists the
## actor ids to place (the keeper and the player's own character already taken
## out); `previous` is the last result's id -> spot index. Anyone already
## placed keeps their spot, so people do not shuffle as others come and go; a
## newcomer takes the spot their id hashes to, or the next free one. Returns
## {"spots": {id: index}, "more": n} where n counts the people who did not fit.
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
