extends SceneTree

## Headless regression harness for LLM-689: the editor lists animals apart
## from villagers (client/scripts/editor_panel.gd).
##
##   - the placement palette routes a sprite the engine flags `animal` into the
##     ANIMALS grid, everything else into VILLAGERS;
##   - picking an animal places it unnamed (the engine names it for its
##     species) even when the VILLAGERS name field holds text;
##   - the Villagers browser lists people first and the animals after them,
##     under one ANIMALS heading, which a filter with no animal match drops.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/editor_animal_split_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The panel script is instantiated off-tree via .new() so _ready() never fires;
## each test wires only the pieces it touches (the grids, the name field, the
## list, a stub world holding placed_npcs).

const TESTS := [
    "_test_palette_routes_animals_to_their_grid",
    "_test_animal_pick_ignores_villager_name",
    "_test_browser_lists_animals_after_people",
    "_test_browser_filter_drops_empty_animal_heading",
]

var _failures := 0
var _checks := 0
var _completed := {}


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    for name in TESTS:
        call(name)
        _completed[name] = true
    for name in TESTS:
        _check(_completed.has(name), "%s ran" % name)
    print("\n[editor_animal_split_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[editor_animal_split_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(ok: bool, what: String) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("FAIL: " + what)


func _panel() -> PanelContainer:
    var panel: PanelContainer = load("res://scripts/editor_panel.gd").new()
    panel._font = ThemeDB.fallback_font
    return panel



func _stub_world() -> Node2D:
    var stub := GDScript.new()
    stub.source_code = "extends Node2D\nvar placed_npcs: Dictionary = {}\nvar placed_objects: Dictionary = {}\n"
    stub.reload()
    var world := Node2D.new()
    world.set_script(stub)
    return world


func _npc(display_name: String, animal: bool) -> Node2D:
    var c := Node2D.new()
    c.set_meta("display_name", display_name)
    c.set_meta("animal", animal)
    return c


func _test_palette_routes_animals_to_their_grid() -> void:
    # _add_npc_catalog_item itself can't run in a debug build: its (pre-existing)
    # StyleBoxFlat corner_radius_left_top assignment is an invalid property that
    # release exports ignore and debug aborts on. The routing decision is its own
    # helper, tested here.
    var panel := _panel()
    panel._npc_catalog_grid = GridContainer.new()
    panel._animal_catalog_grid = GridContainer.new()
    var person_grid: GridContainer = panel._npc_catalog_grid_for({"id": "p", "name": "Woman A v00"})
    var sheep_grid: GridContainer = panel._npc_catalog_grid_for({"id": "s", "name": "Sheep", "animal": true})
    var cow_grid: GridContainer = panel._npc_catalog_grid_for({"id": "c", "name": "Cow (grey)", "animal": true})
    _check(person_grid == panel._npc_catalog_grid, "person goes to VILLAGERS")
    _check(sheep_grid == panel._animal_catalog_grid, "sheep goes to ANIMALS")
    _check(cow_grid == panel._animal_catalog_grid, "cow goes to ANIMALS")
    _check(panel._npc_catalog_grid != panel._animal_catalog_grid, "the two grids are distinct")


func _test_animal_pick_ignores_villager_name() -> void:
    var panel := _panel()
    panel._npc_name_input = LineEdit.new()
    panel._npc_name_input.text = "  Goody Smith  "
    _check(panel._placement_name_for({"id": "p", "name": "Woman A v00"}) == "Goody Smith", "villager keeps the typed name")
    _check(panel._placement_name_for({"id": "s", "name": "Sheep", "animal": true}) == "", "animal is placed unnamed")
    panel._npc_name_input = null
    _check(panel._placement_name_for({"id": "p", "name": "Woman A v00"}) == "", "no name field -> unnamed")


func _browser_labels(panel: PanelContainer) -> Array:
    var out := []
    for child in panel._villagers_list.get_children():
        if child.is_queued_for_deletion():
            continue
        if child is Label:
            out.append("#" + child.text)
        else:
            for npc_id in panel._villager_rows:
                if panel._villager_rows[npc_id] == child:
                    out.append(npc_id)
    return out


func _browser_panel() -> PanelContainer:
    var panel := _panel()
    panel._villagers_list = VBoxContainer.new()
    var world := _stub_world()
    world.placed_npcs = {
        "cow": _npc("Cow", true),
        "hannah": _npc("Hannah Boggs", false),
        "sheep": _npc("Sheep", true),
        "abe": _npc("Abraham Warren", false),
    }
    panel.world = world
    return panel


func _test_browser_lists_animals_after_people() -> void:
    var panel := _browser_panel()
    panel.rebuild_villagers_list()
    var labels := _browser_labels(panel)
    _check(labels == ["abe", "hannah", "#ANIMALS", "cow", "sheep"], "people, then ANIMALS heading, then animals (got %s)" % str(labels))


func _test_browser_filter_drops_empty_animal_heading() -> void:
    var panel := _browser_panel()
    panel._villagers_filter_input = LineEdit.new()
    panel._villagers_filter_input.text = "hann"
    panel.rebuild_villagers_list()
    var labels := _browser_labels(panel)
    _check(labels == ["hannah"], "filter with no animal match shows no ANIMALS heading (got %s)" % str(labels))
