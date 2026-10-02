extends SceneTree

## Headless regression harness for LLM-703 — the client's sound.
##
## What this file exists to catch:
##
##  1. A sound row naming a file that is not there. Sound.play degrades a
##     missing file to silence with a warning, so a typo in SOUNDS would ship
##     as a sound that simply never plays. Every file every row names, and the
##     two rain loops, must exist.
##  2. The rain levels: the storm raises the outdoor loop, a roof crossfades to
##     the muffled one, a clear sky fades both out.
##  3. Footsteps: only on a footfall frame of the walk animation, only while
##     the PC walks, the surface from the terrain under it, and the PC's inside
##     flag reaching Sound.
##  4. The volume and mute settings surviving a reload.
##
## Run headless (CI and local):
##   godot --headless --path client --import
##   godot --headless --path client --script res://tests/sound_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## Under the headless Dummy audio driver Sound never starts a player (see
## sound.gd _start), so these checks read what Sound decided — the voice it
## picked, the volume it set — not what was heard. The Sound autoload is
## reached by node path: this script compiles before the tree exists, and the
## autoload's global name does not resolve there.

const TESTS := [
    "_test_every_sound_file_exists",
    "_test_rain_follows_the_storm_and_the_roof",
    "_test_surface_for_terrain",
    "_test_footfall_frames",
    "_test_steps_only_on_footfalls_while_walking",
    "_test_inside_flag_reaches_sound",
    "_test_volume_and_mute_survive_a_reload",
]

## Loaded at run time, not preloaded: footsteps.gd names the Sound autoload,
## which exists only once the tree is up.
var FootstepsScript: GDScript = null

## A stand-in for world.gd: the three things footsteps.gd reads.
const FAKE_WORLD_SOURCE := """
extends Node2D
var placed_npcs := {}
var terrain := 2
func _npc_sprite(container: Node2D) -> AnimatedSprite2D:
    return container.get_node_or_null("CharacterSprite")
func terrain_at(_pos: Vector2) -> int:
    return terrain
"""

var _sound: Node = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    pass


## First frame: the autoloads are in the tree.
func _process(_delta: float) -> bool:
    _sound = root.get_node_or_null("Sound")
    FootstepsScript = load("res://scripts/footsteps.gd")
    _check("harness — Sound autoload present", _sound != null)
    if _sound == null:
        quit(1)
        return true
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[sound_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[sound_test] ALL PASS")
    quit(1 if _failures > 0 else 0)
    return true


func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)


func _done() -> void:
    _completed[_current] = true


func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t))


func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t))
        _check("harness — %s exists" % t, has_method(t))
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name))


func _check(label: String, ok: bool) -> void:
    _checks += 1
    if not ok:
        _failures += 1
        print("  FAIL: ", label)


# --- fixtures --------------------------------------------------------------------

## A fake world with one PC container whose sprite has a 4-frame walk and an
## idle. Returns [world, container, sprite, footsteps].
func _make_walker() -> Array:
    var script := GDScript.new()
    script.source_code = FAKE_WORLD_SOURCE
    script.reload()
    var world := Node2D.new()
    world.set_script(script)
    var frames := SpriteFrames.new()
    for anim in ["south_walk", "south_idle"]:
        frames.add_animation(anim)
        var n := 4 if anim == "south_walk" else 1
        for i in n:
            var tex := PlaceholderTexture2D.new()
            tex.size = Vector2(32, 32)
            frames.add_frame(anim, tex)
    var sprite := AnimatedSprite2D.new()
    sprite.name = "CharacterSprite"
    sprite.sprite_frames = frames
    var container := Node2D.new()
    container.add_child(sprite)
    world.add_child(container)
    world.placed_npcs["pc-1"] = container
    var steps: Node = FootstepsScript.new()
    steps.world = world
    steps.pc_actor_id = "pc-1"
    return [world, container, sprite, steps]


## How many one-shot sounds Sound picked a voice for since `before` (its
## round-robin cursor). Valid for fewer than VOICES plays.
func _plays_since(before: int) -> int:
    return posmod(_sound._next_voice - before, _sound.VOICES)


# --- tests -----------------------------------------------------------------------

func _test_every_sound_file_exists() -> void:
    for sound_name in _sound.SOUNDS:
        for file in _sound.SOUNDS[sound_name]["files"]:
            _check("%s — %s exists" % [sound_name, file], ResourceLoader.exists(_sound.DIR + file))
    for file in [_sound.RAIN_FILE, _sound.RAIN_MUFFLED_FILE]:
        _check("rain — %s exists" % file, ResourceLoader.exists(_sound.DIR + file))
    _done()


func _test_rain_follows_the_storm_and_the_roof() -> void:
    var out: AudioStreamPlayer = _sound._rain
    var muffled: AudioStreamPlayer = _sound._rain_muffled
    _sound.set_indoors(false)
    _sound._indoor_mix = 0.0
    _sound.set_rain(true, false)
    _sound._process(0.0)
    _check("storm outdoors — outdoor loop at full", is_equal_approx(out.volume_db, _sound.RAIN_DB))
    _check("storm outdoors — muffled loop silent", muffled.volume_db <= -80.0)

    _sound.set_indoors(true)
    _sound._process(_sound.INDOOR_FADE_SECONDS)
    _check("storm indoors — outdoor loop silent", out.volume_db <= -80.0)
    _check("storm indoors — muffled loop at full", is_equal_approx(muffled.volume_db, _sound.RAIN_MUFFLED_DB))

    _sound.set_rain(false, true)
    _sound._process(_sound.RAIN_FADE_SECONDS / 2.0)
    _check("clearing — half way down", is_equal_approx(muffled.volume_db, _sound.RAIN_MUFFLED_DB + linear_to_db(0.5)))
    _sound._process(_sound.RAIN_FADE_SECONDS)
    _check("clear — both silent", out.volume_db <= -80.0 and muffled.volume_db <= -80.0)
    _sound.set_indoors(false)
    _sound._indoor_mix = 0.0
    _done()


func _test_surface_for_terrain() -> void:
    _check("dirt", FootstepsScript.surface_for(1) == "dirt")
    _check("light grass", FootstepsScript.surface_for(2) == "grass")
    _check("dark grass", FootstepsScript.surface_for(3) == "grass")
    _check("cobblestone", FootstepsScript.surface_for(4) == "stone")
    _check("shallow water (a bridge)", FootstepsScript.surface_for(5) == "wood")
    _check("deep water (a bridge)", FootstepsScript.surface_for(6) == "wood")
    _check("off the map", FootstepsScript.surface_for(-1) == "grass")
    for surface in ["dirt", "grass", "stone", "wood"]:
        _check("step_%s is a sound" % surface, _sound.SOUNDS.has("step_" + surface))
    _done()


func _test_footfall_frames() -> void:
    _check("4 frames — 0", FootstepsScript.is_footfall(0, 4))
    _check("4 frames — 1 no", not FootstepsScript.is_footfall(1, 4))
    _check("4 frames — 2", FootstepsScript.is_footfall(2, 4))
    _check("4 frames — 3 no", not FootstepsScript.is_footfall(3, 4))
    _check("6 frames — 3", FootstepsScript.is_footfall(3, 6))
    _check("5 frames — 2", FootstepsScript.is_footfall(2, 5))
    _check("1 frame — 0", FootstepsScript.is_footfall(0, 1))
    _done()


func _test_steps_only_on_footfalls_while_walking() -> void:
    var parts := _make_walker()
    var world: Node2D = parts[0]
    var container: Node2D = parts[1]
    var sprite: AnimatedSprite2D = parts[2]
    var steps: Node = parts[3]

    # Idle: no step.
    sprite.play("south_idle")
    var before: int = _sound._next_voice
    steps._process(0.0)
    _check("idle — no step", _plays_since(before) == 0)

    # Walk animation without the walking meta (an arrival snap) — no step.
    sprite.play("south_walk")
    before = _sound._next_voice
    steps._process(0.0)
    _check("walk animation, not walking — no step", _plays_since(before) == 0)

    # Walking: a full cycle of four frames steps twice (frames 0 and 2).
    container.set_meta("walking", {})
    steps._last_anim = ""
    before = _sound._next_voice
    for f in [0, 1, 2, 3]:
        sprite.frame = f
        steps._process(0.0)
    _check("walking — two steps per cycle", _plays_since(before) == 2)

    # The same frame twice is one step, not two.
    sprite.frame = 0
    before = _sound._next_voice
    steps._process(0.0)
    steps._process(0.0)
    _check("held frame — one step", _plays_since(before) == 1)

    # Hidden (inside an opaque building) — no step.
    container.visible = false
    sprite.frame = 2
    before = _sound._next_voice
    steps._process(0.0)
    _check("hidden — no step", _plays_since(before) == 0)
    container.visible = true

    # No PC known (before /pc/me, after logout) — nothing.
    steps.pc_actor_id = ""
    sprite.frame = 0
    before = _sound._next_voice
    steps._process(0.0)
    _check("no PC — no step", _plays_since(before) == 0)

    steps.free()
    world.free()
    _done()


func _test_inside_flag_reaches_sound() -> void:
    var parts := _make_walker()
    var world: Node2D = parts[0]
    var container: Node2D = parts[1]
    var steps: Node = parts[3]
    _sound.set_indoors(false)
    container.set_meta("inside", true)
    steps._process(0.0)
    _check("inside — Sound hears it", _sound.indoors)
    container.set_meta("inside", false)
    steps._process(0.0)
    _check("outside again — Sound hears it", not _sound.indoors)
    steps.free()
    world.free()
    _done()


func _test_volume_and_mute_survive_a_reload() -> void:
    var path: String = _sound.SETTINGS_PATH
    var existed := FileAccess.file_exists(path)
    var old_volume: float = _sound.volume
    var old_muted: bool = _sound.muted

    _sound.set_volume(0.35)
    _sound.set_muted(true)
    _check("muted — master bus muted", AudioServer.is_bus_mute(0))
    var fresh: Node = load("res://scripts/sound.gd").new()
    fresh._load_settings()
    _check("reload — volume kept", is_equal_approx(fresh.volume, 0.35))
    _check("reload — mute kept", fresh.muted)
    fresh.free()

    _sound.set_muted(false)
    _sound.set_volume(0.0)
    _check("volume 0 — master bus muted", AudioServer.is_bus_mute(0))

    _sound.set_volume(old_volume)
    _sound.set_muted(old_muted)
    if not existed:
        DirAccess.remove_absolute(ProjectSettings.globalize_path(path))
    _done()
