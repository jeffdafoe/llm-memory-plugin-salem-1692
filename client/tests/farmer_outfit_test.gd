extends SceneTree

## Headless harness for the LLM-691 character creator's outfit model
## (FarmerOutfit): picks from the engine's wardrobe compose into the
## farmer_base layer list the engine's ValidateFarmerOutfit accepts — body
## first, a cloak's back half behind it, front layers in the pack's layer
## order, the curved figure's sheets, a covering hat leaving the hair out —
## and a saved list reads back into the same picks.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/farmer_outfit_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The wardrobe here is a small fixture in the shape POST /pc/wardrobe
## returns (sim.PlayerWardrobe); no network, no art.

const TESTS := [
    "_test_compose_orders_layers",
    "_test_compose_curved_figure",
    "_test_covering_hat_drops_hair",
    "_test_decompose_round_trip",
    "_test_decompose_drops_unknown_sheets",
    "_test_default_picks",
    "_test_random_picks_offered_only",
    "_test_locks",
    "_test_missing_goods",
    "_test_without_locked",
    "_test_save_keeps_covered_hair",
]

const ROOT := "/tilesets/mana-seed/farmer/sheets/"

var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[farmer_outfit_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[farmer_outfit_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)


func _done() -> void:
    _completed[_current] = true


func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)


func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t), true)
        _check("harness — %s exists" % t, has_method(t), true)
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name), true)


## JSON numbers arrive as floats; the fixture goes through JSON like the
## real payload does.
func _wardrobe() -> Dictionary:
    var w := {
        "categories": [
            {"id": "body", "label": "Body", "required": true},
            {"id": "hair", "label": "Hair"},
            {"id": "head", "label": "Hat"},
            {"id": "shirt", "label": "Shirt"},
            {"id": "neck", "label": "Cloak"},
            {"id": "lower", "label": "Breeches and skirts"},
            {"id": "feet", "label": "Shoes"},
        ],
        "items": [
            _item("body", "human", ["skin"], [{"sheet": "01body/body.png", "order": 1}]),
            _item("hair", "dapper", ["hair"], [{"sheet": "13hair/dapper.png", "order": 13}]),
            _item("head", "strawhat", ["c4", "c3"], [{"sheet": "14head/straw.png", "order": 14}], false, "straw_hat"),
            _item("head", "headscarf", ["c4"], [{"sheet": "14head/scarf.png", "order": 14}], true),
            _item("shirt", "longshirt", ["c3"], [{"sheet": "05shrt/shirt.png", "curved": "05shrt/shirtc.png", "order": 5}]),
            _item("neck", "cloakplain", ["c4", "c3"], [
                {"sheet": "00undr/cloak.png", "behind": true, "order": 0},
                {"sheet": "11neck/cloak.png", "order": 11},
            ]),
            _item("lower", "longpants", ["c3"], [{"sheet": "04lwr1/pants.png", "order": 4}]),
            _item("feet", "boots", ["c3"], [{"sheet": "03fot1/boots.png", "order": 3}], false, "boots"),
            _item("feet", "shoes", ["c3"], [{"sheet": "03fot1/shoes.png", "order": 3}]),
        ],
        "colours": {"skin": [0, 1, 2], "hair": [5, 37], "c3": [0, 32, 34], "c4": [2, 30]},
        "dyes": [{"good": "madder", "colours": {"c3": [34]}}],
        "goods": {"straw_hat": {"label": "Straw hat", "price": 4}, "boots": {"label": "Boots", "price": 10},
            "madder": {"label": "Madder", "price": 6}},
        "held": [],
    }
    return JSON.parse_string(JSON.stringify(w))


func _item(category: String, id: String, slots: Array, layers: Array, hides_hair := false, good := "") -> Dictionary:
    for layer in layers:
        layer["sheet"] = ROOT + layer["sheet"]
        if layer.has("curved"):
            layer["curved"] = ROOT + layer["curved"]
    var item := {"id": id, "category": category, "label": id, "slots": slots, "layers": layers}
    if hides_hair:
        item["hides_hair"] = true
    if good != "":
        item["good"] = good
    return item


func _picks(figure: String, items: Dictionary) -> Dictionary:
    return {"figure": figure, "items": items}


func _sheets(layers: Array) -> Array:
    var out: Array = []
    for l in layers:
        out.append(str(l["sheet"]).trim_prefix(ROOT))
    return out


func _dressed() -> Dictionary:
    return _picks("straight", {
        "feet": {"item": "boots", "ramps": {"c3": 34}},
        "hair": {"item": "dapper", "ramps": {"hair": 37}},
        "neck": {"item": "cloakplain", "ramps": {"c4": 30, "c3": 0}},
        "body": {"item": "human", "ramps": {"skin": 2}},
        "shirt": {"item": "longshirt", "ramps": {"c3": 0}},
        "head": {"item": "strawhat", "ramps": {"c4": 2, "c3": 32}},
        "lower": {"item": "longpants", "ramps": {"c3": 32}},
    })


func _test_compose_orders_layers() -> void:
    var layers := FarmerOutfit.compose(_wardrobe(), _dressed())
    _check("compose — body first, cloak back behind it, then the pack's layer order",
        _sheets(layers), ["01body/body.png", "00undr/cloak.png", "03fot1/boots.png", "04lwr1/pants.png",
            "05shrt/shirt.png", "11neck/cloak.png", "13hair/dapper.png", "14head/straw.png"])
    _check("compose — only the cloak back is behind", layers[1].get("behind", false), true)
    _check("compose — front layers carry no behind key", layers[2].has("behind"), false)
    _check("compose — ramps name the item's slots", layers[7]["ramps"], {"c4": 2, "c3": 32})
    _check("compose — ramps are ints, as the engine decodes them",
        JSON.stringify(layers[0]["ramps"]), '{"skin":2}')
    _done()


func _test_compose_curved_figure() -> void:
    var picks := _dressed()
    picks["figure"] = FarmerOutfit.CURVED
    var sheets := _sheets(FarmerOutfit.compose(_wardrobe(), picks))
    _check("curved — the shirt uses its curved sheet", sheets.has("05shrt/shirtc.png"), true)
    _check("curved — an item with no curved sheet keeps its own", sheets.has("04lwr1/pants.png"), true)
    _done()


func _test_covering_hat_drops_hair() -> void:
    var picks := _dressed()
    picks["items"]["head"] = {"item": "headscarf", "ramps": {"c4": 2}}
    var sheets := _sheets(FarmerOutfit.compose(_wardrobe(), picks))
    _check("headscarf — hair is left out", sheets.has("13hair/dapper.png"), false)
    _check("headscarf — the scarf is worn", sheets.has("14head/scarf.png"), true)
    _done()


func _test_decompose_round_trip() -> void:
    var wardrobe := _wardrobe()
    for figure in [FarmerOutfit.STRAIGHT, FarmerOutfit.CURVED]:
        var picks := _dressed()
        picks["figure"] = figure
        var layers := FarmerOutfit.compose(wardrobe, picks)
        var back := FarmerOutfit.decompose(wardrobe, layers)
        _check("round trip (%s) — figure" % figure, back["figure"], figure)
        _check("round trip (%s) — picks" % figure, back["items"], picks["items"])
        _check("round trip (%s) — layers" % figure,
            JSON.stringify(FarmerOutfit.compose(wardrobe, back)), JSON.stringify(layers))
    _done()


func _test_decompose_drops_unknown_sheets() -> void:
    # Abraham's boater is not in the fixture wardrobe; a stale or villager
    # outfit opens with what the wardrobe still knows.
    var back := FarmerOutfit.decompose(_wardrobe(), [
        {"sheet": ROOT + "01body/body.png", "ramps": {"skin": 1}},
        {"sheet": ROOT + "14head/boater.png", "ramps": {"c4": 37, "c3": 3}},
    ])
    _check("decompose — only the known body is picked", back["items"].keys(), ["body"])
    _done()


func _test_default_picks() -> void:
    var picks := FarmerOutfit.default_picks(_wardrobe())
    _check("default — wears body, hair, shirt, breeches, shoes",
        _sorted(picks["items"].keys()), ["body", "feet", "hair", "lower", "shirt"])
    _check("default — brown breeches", picks["items"]["lower"]["ramps"], {"c3": 32})
    _check("default — linen shirt", picks["items"]["shirt"]["ramps"], {"c3": 0})
    _check("default — free shoes, not boots", picks["items"]["feet"]["item"], "shoes")
    # A default colour the wardrobe stops offering falls back to its first.
    var wardrobe := _wardrobe()
    wardrobe["colours"]["c3"] = [0]
    _check("default — withdrawn colour falls back",
        FarmerOutfit.default_picks(wardrobe)["items"]["feet"]["ramps"], {"c3": 0})
    _done()


func _test_random_picks_offered_only() -> void:
    var wardrobe := _wardrobe()
    var rng := RandomNumberGenerator.new()
    rng.seed = 691
    var locked_seen := false
    var ok := true
    for i in 200:
        var picks := FarmerOutfit.random_picks(wardrobe, rng)
        if not FarmerOutfit.missing_goods(wardrobe, picks).is_empty():
            locked_seen = true
        if not picks["items"].has("body"):
            ok = false
        for category in picks["items"]:
            var ramps: Dictionary = picks["items"][category]["ramps"]
            for slot in ramps:
                if not FarmerOutfit.open_colours(wardrobe, slot).has(ramps[slot]):
                    ok = false
    _check("random — always a body, only open colours", ok, true)
    _check("random — never a piece or dye the player lacks", locked_seen, false)
    _done()


func _sorted(a: Array) -> Array:
    var out := a.duplicate()
    out.sort()
    return out


func _test_locks() -> void:
    var wardrobe := _wardrobe()
    var by_id := FarmerOutfit.items_by_id(wardrobe)
    _check("locks — boots are sold", FarmerOutfit.item_locked(wardrobe, by_id["boots"]), true)
    _check("locks — shoes are free", FarmerOutfit.item_locked(wardrobe, by_id["shoes"]), false)
    _check("locks — a madder colour needs madder", FarmerOutfit.dye_for(wardrobe, "c3", 34), "madder")
    _check("locks — undyed colours are open", FarmerOutfit.open_colours(wardrobe, "c3"), [0, 32])
    wardrobe["held"] = ["boots", "madder"]
    _check("locks — held boots open", FarmerOutfit.item_locked(wardrobe, by_id["boots"]), false)
    _check("locks — held madder opens its colour", FarmerOutfit.open_colours(wardrobe, "c3"), [0, 32, 34])
    _done()


func _test_missing_goods() -> void:
    var wardrobe := _wardrobe()
    _check("missing — the dressed fixture needs boots, madder and a straw hat",
        _sorted(FarmerOutfit.missing_goods(wardrobe, _dressed())), ["boots", "madder", "straw_hat"])
    wardrobe["held"] = ["boots", "madder", "straw_hat"]
    _check("missing — nothing once all are held", FarmerOutfit.missing_goods(wardrobe, _dressed()), [])
    _check("missing — labels and prices", [FarmerOutfit.good_label(wardrobe, "madder"), FarmerOutfit.good_price(wardrobe, "madder")], ["Madder", 6])
    _done()


func _test_without_locked() -> void:
    var wardrobe := _wardrobe()
    var picks := _dressed()
    picks["items"]["lower"]["ramps"] = {"c3": 34}
    var open := FarmerOutfit.without_locked(wardrobe, picks)
    _check("without locked — the boots and straw hat come off",
        _sorted(open["items"].keys()), ["body", "hair", "lower", "neck", "shirt"])
    _check("without locked — a madder colour falls back to the first open one",
        open["items"]["lower"]["ramps"], {"c3": 0})
    _check("without locked — open colours are kept", open["items"]["neck"]["ramps"], {"c4": 30, "c3": 0})
    _done()


func _test_save_keeps_covered_hair() -> void:
    var picks := _dressed()
    picks["items"]["head"] = {"item": "headscarf", "ramps": {"c4": 2}}
    var sheets := _sheets(FarmerOutfit.compose(_wardrobe(), picks, true))
    _check("save — the hair under a headscarf is kept", sheets.has("13hair/dapper.png"), true)
    _check("save — the headscarf is worn", sheets.has("14head/scarf.png"), true)
    _done()
