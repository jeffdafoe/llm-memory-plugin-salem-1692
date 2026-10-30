class_name StallScenes
## Market stalls (LLM-753): the close-up a player sees while their character
## stands at a stall's counter. Nobody goes into a stall but its keeper, so the
## picture is the stall from the customers' side: the keeper behind the
## counter, the customers outside in front, and the goods the keeper sells —
## piles on the floor behind, a few of a good on the counter top.
##
## A stall is a placed object whose asset is one of SHEETS. The customers are
## the people standing at its loiter pin (the engine's effective loiter offset,
## Chebyshev 1 — where buyers wait), the keeper whoever is inside it. The art
## is built by the tiles repo's tilesets/interiors/build_stalls.gd and lives
## on the VPS beside the room art. Spots are art pixels, where the feet go.

const SIZE := Vector2i(128, 112)
const SHEETS := {
    "Market Stall (Wood)": "/tilesets/interiors/stall_wood.png",
    "Market Stall (Tiled)": "/tilesets/interiors/stall_tiled.png",
    "Market Stall (Fancy)": "/tilesets/interiors/stall_fancy.png",
}
## Trade backdrops (LLM-762): a stall tagged with a trade in the editor draws
## its roof's stall dressed for that trade — stall_<roof>_<trade>.png, built
## beside the plain one by build_stalls.gd — so a second smithy needs only the
## tag. A trade's entry replaces any of the scene's layout fields (LLM-763):
## its size, counter picture, `counter_from` (the counter's left edge — the
## selling side), spots and slots. The smithy is 192 wide: a work area with
## the forge on the left, the plain stall's layout moved 64 right, and a hand's
## spot at the forge.
##
## Work spots in the work area (LLM-764), feet where the art puts them: a
## farmer-base hand pulls the bellows' right rope at `bellows`, the bellows
## (`bellows_art`) press down and the forge fire in `fire` flares with each
## pull; a farmer-base keeper hammers at the anvil from `anvil`, facing it.
const TRADES := {
    "smithy": {
        "size": Vector2i(192, 112),
        "bellows": {"x": 57, "y": 57, "facing": "north"},
        "anvil": {"x": 36, "y": 84, "facing": "west"},
        "fire": Rect2i(1, 32, 26, 19),
        # The bellows, open and pressed (LLM-795), over the backdrop's own
        # at the place build_stalls.gd cut them from.
        "bellows_art": {"sheet": "/tilesets/interiors/stall_smithy_bellows.png", "x": 30, "y": 24},
        "counter": "/tilesets/interiors/stall_counter_smithy.png",
        "counter_from": 64,
        "keeper": {"x": 128, "y": 66, "facing": "south"},
        "hands": [
            {"x": 44, "y": 66, "facing": "south"},
            {"x": 162, "y": 66, "facing": "south"},
        ],
        "door": {"x": 128, "y": 108, "facing": "north"},
        "spots": [
            {"x": 82, "y": 106, "facing": "north"},
            {"x": 174, "y": 106, "facing": "north"},
            {"x": 104, "y": 109, "facing": "north"},
            {"x": 152, "y": 109, "facing": "north"},
        ],
        "floor_slots": [66, 82, 98, 142, 158, 174],
        "counter_slots": [68, 84, 100, 140, 156, 172],
    },
}
const COUNTER_SHEET := "/tilesets/interiors/stall_counter.png"
const GOODS_SHEET := "/tilesets/interiors/stall_goods.png"

const KEEPER := {"x": 64, "y": 66, "facing": "south"}
## Hands working in the stall beside the keeper (LLM-759), either side of
## them behind the counter.
const HANDS := [
    {"x": 30, "y": 66, "facing": "south"},
    {"x": 98, "y": 66, "facing": "south"},
]
## The player's own character, front and centre.
const DOOR := {"x": 64, "y": 108, "facing": "north"}
const SPOTS := [
    {"x": 18, "y": 106, "facing": "north"},
    {"x": 110, "y": 106, "facing": "north"},
    {"x": 40, "y": 109, "facing": "north"},
    {"x": 88, "y": 109, "facing": "north"},
]

## Goods slots: a pile stands on the floor behind the counter, either side of
## the keeper; a few of a good (or one) lie on the counter top. x is the left
## edge of a 16 x 32 goods cell, y its bottom.
const FLOOR_SLOTS := [2, 18, 34, 78, 94, 110]
const FLOOR_Y := 62
const COUNTER_SLOTS := [4, 20, 36, 76, 92, 108]
const COUNTER_Y := 76
## The second of a few sits this far right and down of the first.
const FEW_STEP := Vector2i(4, 1)
const CELL := Vector2i(16, 32)
const COLUMNS := 16

## kind -> goods index in stall_goods.png: cell 2i is the good, 2i+1 the pile.
## Printed by build_stalls.gd; its GOODS order and this table must agree.
const GOODS := {"apples": 0, "raspberries": 1, "blueberries": 2, "carrots": 3, "wheat": 4, "flour": 5, "salt": 6, "cheese": 7, "milk": 8, "meat": 9, "fried_meat": 10, "bread": 11, "flatbread": 11, "water": 12, "water_from_the_well": 12, "water_from_well": 12, "well_water": 12, "firewood": 13, "sage": 14, "wormwood": 14, "feverfew": 14, "greenweed": 14, "coca_tea": 14, "indigo": 15, "madder": 16, "logwood": 17, "weld": 18, "ale": 19, "small_ale": 19, "mulled_ale": 19, "calming_draught": 20, "locket": 21, "silver_locket": 21, "duck_pendant": 21, "whalebone_charm": 22, "spectacles": 23, "axe": 24, "hammer": 25, "shovel": 26, "nail": 27, "horseshoe": 28, "skillet": 29, "iron": 30, "iron_bar": 30, "whetstone": 31, "thread": 32, "linen": 33, "gloves": 34, "suspenders": 35, "vest": 36, "scarf": 37, "knee_socks": 38, "stockings": 39, "boots": 40, "cuffed_boots": 41, "frilly_skirt": 42, "frilly_dress": 43, "long_dress": 44, "linen_shirt": 45, "mended_shirt": 45, "cloak": 46, "mantle": 47, "mantled_cloak": 48, "brimmed_hat": 49, "felt_hat": 50, "headscarf": 51, "straw_hat": 52, "stew": 54, "porridge": 55, "porridge_empty": 56, "stew_empty": 57, "plate_empty": 57, "journeycake": 58, "ale_empty": 59}
## A good with no picture of its own draws as a closed crate.
const OTHER := 53

const TILE := 32.0
## A tile no pin is near: where someone mid-route stands, for stall purposes.
const NOWHERE := Vector2i(-1000000, -1000000)


## The stall scene for structure `sid`, or {} when it is not a stall.
static func scene_for(world: Node, assets: Dictionary, sid: String) -> Dictionary:
    var node: Node2D = world.placed_objects.get(sid, null) if world != null else null
    if node == null:
        return {}
    var sheet := sheet_for(assets, node)
    if sheet == "":
        return {}
    var title := str(node.get_meta("display_name", ""))
    var scene := {
        "kind": "stall",
        "title": title if title != "" else "Market Stall",
        "sheet": sheet,
        "floor_slots": FLOOR_SLOTS,
        "counter_slots": COUNTER_SLOTS,
        "counter": COUNTER_SHEET,
        "counter_from": 0,
        "size": SIZE,
        "keeper": KEEPER,
        "hands": HANDS,
        "door": DOOR,
        "spots": SPOTS,
        "pin": pin_tile(node),
    }
    var trade := trade_of(node)
    if trade != "":
        scene["sheet"] = sheet.replace(".png", "_%s.png" % trade)
        scene.merge(TRADES[trade], true)
    return scene


## The backdrop for a placed object's asset, or "" when it is not a stall.
static func sheet_for(assets: Dictionary, node: Node2D) -> String:
    var asset: Dictionary = assets.get(str(node.get_meta("asset_id", "")), {})
    return str(SHEETS.get(str(asset.get("name", "")), ""))


## Whether the stall is shut: the map draws it in its `unoccupied` state, the
## one the engine's occupancy puts it in when nobody stands in it. A shut stall
## has no window.
static func shut(assets: Dictionary, node: Node2D) -> bool:
    var asset: Dictionary = assets.get(str(node.get_meta("asset_id", "")), {})
    var state := str(node.get_meta("current_state", ""))
    for s in asset.get("states", []):
        if s is Dictionary and str(s.get("state", "")) == state:
            var tags = s.get("tags", [])
            return tags is Array and tags.has("unoccupied")
    return false


## The stall's trade, from the tags set on it in the editor: the first of
## TRADES it carries, or "".
static func trade_of(node: Node2D) -> String:
    var tags = node.get_meta("tags", [])
    if not tags is Array:
        return ""
    for trade in TRADES:
        if tags.has(trade):
            return trade
    return ""


## The stall's loiter pin in (unpadded) map tiles: the object's anchor tile
## plus the effective loiter offset the engine sends (as the editor's marker).
static func pin_tile(node: Node2D) -> Vector2i:
    var ox = node.get_meta("effective_loiter_offset_x", null)
    var oy = node.get_meta("effective_loiter_offset_y", null)
    var offset := Vector2i(int(ox), int(oy)) if ox != null and oy != null else Vector2i.ZERO
    return Vector2i(int(floor(node.position.x / TILE)), int(floor(node.position.y / TILE))) + offset


## Where a person is for the stall's purposes, in map tiles: where they stand,
## or where an arrival's last few steps are taking them. Someone still walking
## a route is on the way, not here (NOWHERE).
static func standing_tile(c: Node2D) -> Vector2i:
    var walk = c.get_meta("walking") if c.has_meta("walking") else null
    if walk is Dictionary:
        var path: Array = walk.get("path", [])
        if not bool(walk.get("finish_idle", false)) or path.is_empty():
            return NOWHERE
        var end: Vector2 = path[path.size() - 1]
        return Vector2i(int(floor(end.x / TILE)), int(floor(end.y / TILE)))
    return Vector2i(int(floor(c.position.x / TILE)), int(floor(c.position.y / TILE)))


## Whether a map tile is at a pin: on it or one of the eight around it.
static func at_pin(tile: Vector2i, pin: Vector2i) -> bool:
    return maxi(absi(tile.x - pin.x), absi(tile.y - pin.y)) <= 1


## The stall whose counter a person standing on `tile` is at, or "". The
## nearest pin wins, the lower id on a tie (as the engine's loiter resolver).
static func stall_at(world: Node, assets: Dictionary, tile: Vector2i) -> String:
    var best := ""
    var best_d := 2
    for id in world.placed_objects:
        var node: Node2D = world.placed_objects[id]
        if not is_instance_valid(node) or sheet_for(assets, node) == "":
            continue
        var pin := pin_tile(node)
        var d := maxi(absi(tile.x - pin.x), absi(tile.y - pin.y))
        if d < best_d or (d == best_d and best != "" and str(id) < best):
            best = str(id)
            best_d = d
    return best


## Where each ware is drawn: [{item, cell, at, count}], `at` the cell's
## top-left in art pixels. Piles take the scene's floor slots (in the order
## given); the rest lie on its counter slots — piles past the floor first, as
## a few — two of a good for a few, one for one. Goods past the last slot are
## not drawn. A scene without slots takes the plain stall's.
static func lay_out(wares: Array, scene: Dictionary = {}) -> Array:
    var floor_slots: Array = scene.get("floor_slots", FLOOR_SLOTS)
    var counter_slots: Array = scene.get("counter_slots", COUNTER_SLOTS)
    var piles: Array = []
    var loose: Array = []
    for w in wares:
        if str(w.get("stock", "")) == "plenty":
            piles.append(w)
        else:
            loose.append(w)
    var out: Array = []
    var counter: Array = []
    for w in piles:
        var index := goods_index(str(w.get("item", "")))
        if out.size() < floor_slots.size():
            out.append({"item": str(w["item"]), "cell": 2 * index + 1,
                "at": Vector2i(floor_slots[out.size()], FLOOR_Y - CELL.y), "count": 1})
        else:
            counter.append({"item": w["item"], "stock": "few"})
    var counter_used := 0
    for w in counter + loose:
        if counter_used >= counter_slots.size():
            break
        var index := goods_index(str(w.get("item", "")))
        out.append({"item": str(w["item"]), "cell": 2 * index,
            "at": Vector2i(counter_slots[counter_used], COUNTER_Y - CELL.y),
            "count": 1 if str(w.get("stock", "")) == "one" else 2})
        counter_used += 1
    return out


static func goods_index(item: String) -> int:
    return int(GOODS.get(item, OTHER))


## A goods cell's region in stall_goods.png.
static func cell_rect(cell: int) -> Rect2:
    return Rect2((cell % COLUMNS) * CELL.x, (cell / COLUMNS) * CELL.y, CELL.x, CELL.y)
