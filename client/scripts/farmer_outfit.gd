class_name FarmerOutfit
## The character creator's outfit model (LLM-691): turns picks from the
## engine's wardrobe (POST /api/village/pc/wardrobe) into a farmer_base layer
## list for FarmerDoll and POST /api/village/pc/outfit, and reads a saved
## layer list back into picks. The engine validates what is allowed; this file
## owns how the picks compose — body first, a cloak's back half behind it,
## then the front layers in the pack's layer order, the curved figure's
## variant sheets, and a hat that covers the head leaving the hair out.
##
## Picks: {"figure": "straight" | "curved",
##         "items": {category: {"item": item_id, "ramps": {slot: index}}}}
## A category missing from "items" is worn as none.

const STRAIGHT := "straight"
const CURVED := "curved"

## Index of the wardrobe's items by id.
static func items_by_id(wardrobe: Dictionary) -> Dictionary:
    var out := {}
    for item in wardrobe.get("items", []):
        if item is Dictionary:
            out[str(item.get("id", ""))] = item
    return out

## The items of one category, in wardrobe order.
static func items_in(wardrobe: Dictionary, category: String) -> Array:
    var out: Array = []
    for item in wardrobe.get("items", []):
        if item is Dictionary and str(item.get("category", "")) == category:
            out.append(item)
    return out

## The colours a slot may take, as ints (JSON parses numbers as floats).
static func colours_for(wardrobe: Dictionary, slot: String) -> Array[int]:
    var out: Array[int] = []
    for c in wardrobe.get("colours", {}).get(slot, []):
        out.append(int(c))
    return out

## Wardrobe goods (LLM-710): an item that names a "good", and a colour a dye
## names, may be worn only while the player holds that good; the wardrobe's
## "held" lists the ones the player holds, and "goods" labels and prices them.

## The dye a colour needs, or "" when the colour is undyed.
static func dye_for(wardrobe: Dictionary, slot: String, index: int) -> String:
    for dye in wardrobe.get("dyes", []):
        for c in dye.get("colours", {}).get(slot, []):
            if int(c) == index:
                return str(dye.get("good", ""))
    return ""

static func holds(wardrobe: Dictionary, good: String) -> bool:
    return good == "" or wardrobe.get("held", []).has(good)

static func item_locked(wardrobe: Dictionary, item: Dictionary) -> bool:
    return not holds(wardrobe, str(item.get("good", "")))

static func colour_locked(wardrobe: Dictionary, slot: String, index: int) -> bool:
    return not holds(wardrobe, dye_for(wardrobe, slot, index))

## The colours of a slot the player may wear: undyed, or dyed with a dye held.
static func open_colours(wardrobe: Dictionary, slot: String) -> Array[int]:
    var out: Array[int] = []
    for c in colours_for(wardrobe, slot):
        if not colour_locked(wardrobe, slot, c):
            out.append(c)
    return out

## The goods picks need that the player does not hold, each named once.
static func missing_goods(wardrobe: Dictionary, picks: Dictionary) -> Array:
    var by_id := items_by_id(wardrobe)
    var out: Array = []
    for category in picks.get("items", {}):
        var pick: Dictionary = picks["items"][category]
        var item: Dictionary = by_id.get(str(pick.get("item", "")), {})
        var needs: Array = [str(item.get("good", ""))]
        var ramps: Dictionary = pick.get("ramps", {})
        for slot in ramps:
            needs.append(dye_for(wardrobe, str(slot), int(ramps[slot])))
        for good in needs:
            if not holds(wardrobe, good) and not out.has(good):
                out.append(good)
    return out

## A good's catalog label, and its list price (0 when unpriced).
static func good_label(wardrobe: Dictionary, good: String) -> String:
    return str(wardrobe.get("goods", {}).get(good, {}).get("label", good))

static func good_price(wardrobe: Dictionary, good: String) -> int:
    return int(wardrobe.get("goods", {}).get(good, {}).get("price", 0))

## picks with every piece the player does not hold taken off and every colour
## whose dye they do not hold set to the first open colour. The body is kept.
static func without_locked(wardrobe: Dictionary, picks: Dictionary) -> Dictionary:
    var by_id := items_by_id(wardrobe)
    var out := {"figure": picks.get("figure", STRAIGHT), "items": {}}
    for category in picks.get("items", {}):
        var pick: Dictionary = picks["items"][category]
        var item: Dictionary = by_id.get(str(pick.get("item", "")), {})
        if item_locked(wardrobe, item):
            continue
        var ramps: Dictionary = pick.get("ramps", {}).duplicate()
        var first := first_colours(wardrobe, item)
        for slot in ramps:
            if colour_locked(wardrobe, str(slot), int(ramps[slot])):
                ramps[slot] = first.get(slot, 0)
        out["items"][category] = {"item": pick.get("item", ""), "ramps": ramps}
    return out

## The farmer_base layer list for picks. A hat that covers the head leaves the
## hair out, unless keep_covered_hair: a player's save keeps it, so the hair
## shows again if the engine takes the hat off a player who no longer holds it
## (LLM-710).
static func compose(wardrobe: Dictionary, picks: Dictionary, keep_covered_hair := false) -> Array:
    var by_id := items_by_id(wardrobe)
    var curved: bool = str(picks.get("figure", STRAIGHT)) == CURVED
    var chosen: Dictionary = picks.get("items", {})
    var hides_hair := false
    if chosen.has("head"):
        var hat: Dictionary = by_id.get(str(chosen["head"].get("item", "")), {})
        hides_hair = bool(hat.get("hides_hair", false))
    var body: Array = []
    var behind: Array = []
    var front: Array = []
    for category in chosen:
        if category == "hair" and hides_hair and not keep_covered_hair:
            continue
        var pick: Dictionary = chosen[category]
        var item: Dictionary = by_id.get(str(pick.get("item", "")), {})
        if item.is_empty():
            continue
        var ramps := {}
        for slot in item.get("slots", []):
            ramps[str(slot)] = int(pick.get("ramps", {}).get(slot, 0))
        for layer in item.get("layers", []):
            var sheet := str(layer.get("sheet", ""))
            var curved_sheet := str(layer.get("curved", ""))
            if curved and curved_sheet != "":
                sheet = curved_sheet
            var entry := {"sheet": sheet, "ramps": ramps.duplicate()}
            if bool(layer.get("behind", false)):
                entry["behind"] = true
                behind.append(entry)
            elif str(item.get("category", "")) == "body":
                body.append(entry)
            else:
                entry["_order"] = int(layer.get("order", 0))
                front.append(entry)
    front.sort_custom(func(a, b): return a["_order"] < b["_order"])
    for entry in front:
        entry.erase("_order")
    return body + behind + front

## Picks read back from a saved layer list; layers the wardrobe does not know
## are dropped. Ramps come from the first layer of each item.
static func decompose(wardrobe: Dictionary, layers: Array) -> Dictionary:
    var by_sheet := {}
    for item in wardrobe.get("items", []):
        if not (item is Dictionary):
            continue
        for layer in item.get("layers", []):
            by_sheet[str(layer.get("sheet", ""))] = {"item": item, "curved": false}
            if str(layer.get("curved", "")) != "":
                by_sheet[str(layer.get("curved", ""))] = {"item": item, "curved": true}
    var picks := {"figure": STRAIGHT, "items": {}}
    for spec in layers:
        if not (spec is Dictionary):
            continue
        var hit: Dictionary = by_sheet.get(str(spec.get("sheet", "")), {})
        if hit.is_empty():
            continue
        var item: Dictionary = hit["item"]
        if hit["curved"]:
            picks["figure"] = CURVED
        var category := str(item.get("category", ""))
        if picks["items"].has(category):
            continue
        var ramps := {}
        var saved = spec.get("ramps", {})
        for slot in item.get("slots", []):
            ramps[str(slot)] = int(saved.get(slot, 0)) if saved is Dictionary else 0
        picks["items"][category] = {"item": str(item.get("id", "")), "ramps": ramps}
    return picks

## A plain first outfit from the free starter pieces: linen shirt, brown
## breeches, dark shoes, brown hair. A colour the player may not wear falls
## back to the first one they may.
const DEFAULT_OUTFIT := [
    ["body", "human", {"skin": 0}],
    ["hair", "dapper", {"hair": 37}],
    ["shirt", "longshirt", {"c3": 0}],
    ["lower", "longpants", {"c3": 32}],
    ["feet", "shoes", {"c3": 3}],
]

static func default_picks(wardrobe: Dictionary) -> Dictionary:
    var picks := {"figure": STRAIGHT, "items": {}}
    var by_id := items_by_id(wardrobe)
    for want in DEFAULT_OUTFIT:
        var item: Dictionary = by_id.get(want[1], {})
        if item.is_empty() or item_locked(wardrobe, item):
            continue
        var ramps := first_colours(wardrobe, item)
        for slot in want[2]:
            if ramps.has(slot) and open_colours(wardrobe, slot).has(int(want[2][slot])):
                ramps[slot] = int(want[2][slot])
        picks["items"][want[0]] = {"item": want[1], "ramps": ramps}
    return picks

## Each slot's first colour the player may wear (else its first offered one).
static func first_colours(wardrobe: Dictionary, item: Dictionary) -> Dictionary:
    var ramps := {}
    for slot in item.get("slots", []):
        var offered := open_colours(wardrobe, str(slot))
        if offered.is_empty():
            offered = colours_for(wardrobe, str(slot))
        ramps[str(slot)] = offered[0] if not offered.is_empty() else 0
    return ramps

## How likely a random outfit wears each optional category.
const WEAR_CHANCE := {
    "hair": 0.9, "head": 0.4, "shirt": 0.9, "over": 0.4, "neck": 0.25, "face": 0.1,
    "lower": 1.0, "legs": 0.5, "feet": 0.9, "hands": 0.15,
}

## A random outfit from the items and colours the player may wear.
static func random_picks(wardrobe: Dictionary, rng: RandomNumberGenerator) -> Dictionary:
    var picks := {"figure": STRAIGHT if rng.randf() < 0.5 else CURVED, "items": {}}
    for category in wardrobe.get("categories", []):
        var id := str(category.get("id", ""))
        var required := bool(category.get("required", false))
        if not required and rng.randf() >= float(WEAR_CHANCE.get(id, 0.5)):
            continue
        var options: Array = items_in(wardrobe, id).filter(func(it): return not item_locked(wardrobe, it))
        if options.is_empty():
            continue
        var item: Dictionary = options[rng.randi_range(0, options.size() - 1)]
        var ramps := {}
        for slot in item.get("slots", []):
            var offered := open_colours(wardrobe, str(slot))
            ramps[str(slot)] = offered[rng.randi_range(0, offered.size() - 1)] if not offered.is_empty() else 0
        picks["items"][id] = {"item": str(item.get("id", "")), "ramps": ramps}
    return picks

## A FarmerDoll sprite payload for a layer list, for the creator's preview.
static func preview_sprite(layers: Array) -> Dictionary:
    return {
        "rig": FarmerDoll.RIG,
        "layers": layers,
        "frame_width": FarmerRig.CELL_SIZE,
        "frame_height": FarmerRig.CELL_SIZE,
    }
