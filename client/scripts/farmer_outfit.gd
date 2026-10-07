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

## The farmer_base layer list for picks.
static func compose(wardrobe: Dictionary, picks: Dictionary) -> Array:
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
        if category == "hair" and hides_hair:
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

## A plain first outfit: linen shirt, brown breeches and boots, brown hair.
## A colour the wardrobe no longer offers falls back to its first offered one.
const DEFAULT_OUTFIT := [
    ["body", "human", {"skin": 0}],
    ["hair", "dapper", {"hair": 37}],
    ["shirt", "longshirt", {"c3": 0}],
    ["lower", "longpants", {"c3": 32}],
    ["feet", "boots", {"c3": 34}],
]

static func default_picks(wardrobe: Dictionary) -> Dictionary:
    var picks := {"figure": STRAIGHT, "items": {}}
    var by_id := items_by_id(wardrobe)
    for want in DEFAULT_OUTFIT:
        var item: Dictionary = by_id.get(want[1], {})
        if item.is_empty():
            continue
        var ramps := first_colours(wardrobe, item)
        for slot in want[2]:
            if ramps.has(slot) and colours_for(wardrobe, slot).has(int(want[2][slot])):
                ramps[slot] = int(want[2][slot])
        picks["items"][want[0]] = {"item": want[1], "ramps": ramps}
    return picks

static func first_colours(wardrobe: Dictionary, item: Dictionary) -> Dictionary:
    var ramps := {}
    for slot in item.get("slots", []):
        var offered := colours_for(wardrobe, str(slot))
        ramps[str(slot)] = offered[0] if not offered.is_empty() else 0
    return ramps

## How likely a random outfit wears each optional category.
const WEAR_CHANCE := {
    "hair": 0.9, "head": 0.4, "shirt": 0.9, "over": 0.4, "neck": 0.25, "face": 0.1,
    "lower": 1.0, "legs": 0.5, "feet": 0.9, "hands": 0.15,
}

## A random outfit from the offered items and colours.
static func random_picks(wardrobe: Dictionary, rng: RandomNumberGenerator) -> Dictionary:
    var picks := {"figure": STRAIGHT if rng.randf() < 0.5 else CURVED, "items": {}}
    for category in wardrobe.get("categories", []):
        var id := str(category.get("id", ""))
        var required := bool(category.get("required", false))
        if not required and rng.randf() >= float(WEAR_CHANCE.get(id, 0.5)):
            continue
        var options := items_in(wardrobe, id)
        if options.is_empty():
            continue
        var item: Dictionary = options[rng.randi_range(0, options.size() - 1)]
        var ramps := {}
        for slot in item.get("slots", []):
            var offered := colours_for(wardrobe, str(slot))
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
