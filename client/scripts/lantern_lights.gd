extends Node

## Carried lantern lights (LLM-846). A warm light on every actor whose horn
## lantern is lit, drawn by every client. The engine says when a lantern is lit
## and when it burns out (lantern_lit_until on /agents for a client that
## connects late, npc_lantern_changed when it is lit or put out); burning out on
## the clock sends nothing, so the light is taken off here at that time, on
## the engine's clock (world.engine_now_unix); until the first world read gives
## that clock, nothing is taken off.
##
## Like the night-vision light (night_shade.gd), each light is a child of the
## actor's container, looked up every frame: it moves with the actor, hides
## with it indoors, and finds the new node after a re-render. Its strength
## follows the night, so by day it is all but gone.

const COLOR := Color(1.0, 0.72, 0.38)
const ENERGY := 0.9
const RADIUS_PX := 96.0
## Flicker: a slow wobble of the energy, out of step between actors.
const FLICKER := 0.06
const FLICKER_HZ := 1.3

const NightShadeScript = preload("res://scripts/night_shade.gd")

var world: Node2D = null
## Actor id -> engine unix time the lantern burns out.
var lit_until: Dictionary = {}
## Actor id -> the PointLight2D on that actor's container.
var _lights: Dictionary = {}
var _time := 0.0
## Frames that came while an /agents read was on its way. The read may predate
## them, so they are applied again, in order, over its answer.
var _loading := false
var _during_load: Array = []

## An /agents read was sent (world._load_npcs).
func begin_agents_load() -> void:
    _loading = true
    _during_load.clear()

## Replace every lit lantern from an /agents read (a load or a resync), then
## replay the frames that came while it was on its way.
func load_agents(agents: Array) -> void:
    lit_until.clear()
    for dto in agents:
        if dto is Dictionary:
            _set_lit(str(dto.get("id", "")), str(dto.get("lantern_lit_until", "")))
    for data in _during_load:
        _set_lit(str(data.get("id", "")), str(data.get("lit_until", "")))
    _loading = false
    _during_load.clear()

## An /agents read failed or brought nothing usable: stop keeping frames. The
## lanterns stay as the frames have already set them.
func end_agents_load() -> void:
    _loading = false
    _during_load.clear()

## An npc_lantern_changed frame: lit until lit_until, or out when it is absent.
func apply_changed(data: Dictionary) -> void:
    _set_lit(str(data.get("id", "")), str(data.get("lit_until", "")))
    if _loading:
        _during_load.append(data)

func _set_lit(id: String, until_iso: String) -> void:
    if id == "":
        return
    var until_unix: float = world._iso_to_unix(until_iso) if world != null else 0.0
    if until_unix > 0.0:
        lit_until[id] = until_unix
    else:
        lit_until.erase(id)

func _process(delta: float) -> void:
    if world == null:
        return
    _time += delta
    refresh(world.engine_now_unix(), NightShadeScript.night_factor(world._phase_pos))

## Drop lanterns that have burned out (now_unix 0: no engine clock yet, keep
## them), put a light on every actor still carrying a lit one, and set each
## light's strength for the night.
func refresh(now_unix: float, night: float) -> void:
    if now_unix > 0.0:
        for id in lit_until.keys():
            if now_unix >= float(lit_until[id]):
                lit_until.erase(id)
    for id in _lights.keys():
        # Untyped, light and container both: a container's own queue_free can
        # free them before this runs, and a typed variable refuses a freed
        # instance.
        var light = _lights[id]
        var container = _container(id)
        if not lit_until.has(id) or not is_instance_valid(light) or light.get_parent() != container:
            if is_instance_valid(light):
                light.queue_free()
            _lights.erase(id)
    for id in lit_until:
        var container = _container(id)
        if container == null:
            continue
        var light = _lights.get(id, null)
        if light == null:
            light = PointLight2D.new()
            light.name = "Lantern"
            light.texture = world._get_light_gradient()
            light.texture_scale = RADIUS_PX / 128.0
            light.color = COLOR
            container.add_child(light)
            _lights[id] = light
        var wobble := 1.0 + FLICKER * sin(TAU * FLICKER_HZ * _time + float(hash(id) % 628) / 100.0)
        light.energy = ENERGY * night * wobble
        light.enabled = night > 0.0

## The actor's container, or null when it is absent or freed.
func _container(id: String):
    var container = world.placed_npcs.get(id, null)
    if container == null or not is_instance_valid(container):
        return null
    return container

## The light on an actor, or null when it carries none.
func light_for(id: String) -> PointLight2D:
    var light = _lights.get(id, null)
    if light == null or not is_instance_valid(light):
        return null
    return light
