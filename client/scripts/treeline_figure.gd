extends Node

## Someone at the treeline (LLM-844, omens slice 3). Rarely, at dusk, a
## colourless grey figure stands beside a tree some way off. Walk toward it and it is
## gone; pan away and it is gone; leave it and it fades.
##
## Client-only and private: this client decides, places, removes and
## remembers. The engine, the action log and the villagers know nothing; the
## only trace is the statistics rows sent through ClientEvents (LLM-880).
##
## Where it stands: in the open just left of a tree, its edge against the
## tree's own pixels at the figure's height. Left, because the tree shadows
## fall to the right; in the open, because under a canopy or in a shadow a
## dark figure reads as more shadow and nobody sees it. Trees, not dark-grass terrain:
## on the live map dark grass is scattered over a third of the map, village
## included, so a dark-grass edge can be an open field (the woods dark of
## LLM-842 is built from trees for the same reason).
##
## When it may show: the first half of the hour-long dusk sweep, the player
## outdoors with no storm and the window focused, 21 days since the last
## showing on this device, and one 1-in-4 roll per dusk. The roll is spent
## only when a spot is in view, so a dusk spent indoors keeps its chance.
##
## An operator trigger (client_trigger kind treeline_figure, LLM-881) skips
## the dusk, storm, focus, cooldown and roll checks but still needs the
## player outdoors and a tree in view, and leaves the cooldown untouched.

const Terrain = preload("res://scripts/terrain.gd")

const BODY_SHEET := "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png"
const BODY_SPRITE := {
    "rig": "farmer_base",
    "frame_width": 64,
    "frame_height": 64,
    "layers": [{"sheet": BODY_SHEET, "ramps": {"skin": 0}}],
}
## The figure shader (waver and breathing alpha) colours the body; the
## container's modulate alpha carries only the final fade.
const FIGURE_SHADER := preload("res://scripts/treeline_figure.gdshader")
## The body's drawn pixels around its feet point (rig rows 9..43 at scale 2).
const FIGURE_RECT := Rect2(-16, -64, 32, 68)

const TILE := 32.0
const MIN_TILES := 10.0
const MAX_TILES := 18.0
## Feet this many px above the tree's base, so the tree sorts in front.
const BEHIND_PX := Vector2(4, 12)
## How far the figure's edge may reach in behind the tree's edge, in px.
const OVERLAP_PX := Vector2(0, 6)
## A tree pixel at least this opaque is tree (or its shadow): not open ground.
const TREE_ALPHA := 0.1
## The tree's left reach when its pixels cannot be read.
const FALLBACK_REACH_PX := 40.0

## _phase_pos runs 0..1 over the 60-minute sweep; 0.5 is 30 minutes after dusk.
const DUSK_WINDOW := 0.5
const COOLDOWN_SECONDS := 21 * 86400.0
const ROLL_CHANCE := 0.25
const CHECK_SECONDS := 5.0
const SHOW_SECONDS := Vector2(60.0, 90.0)
const FADE_SECONDS := 10.0
## After a failed body download, wait this long before asking again.
const BODY_RETRY_SECONDS := 60.0

const OMENS_PATH := "user://omens.cfg"
const SECTION := "treeline_figure"

## Duck-typed world.gd: placed_objects, placed_npcs, pc_actor_id,
## objects_node, current_phase, _phase_pos, current_weather, village_date,
## compute_object_hit_rect, terrain_at, _npc_sheets, get_or_load_npc_sheet,
## _build_character_sprite.
var world: Node = null
var omens_path := OMENS_PATH

## Seams for the tests; the defaults read the running game.
var now_unix: Callable = Time.get_unix_time_from_system
var visible_rect: Callable = _viewport_world_rect
var is_indoors: Callable = _sound_indoors
var is_tree: Callable = _catalog_is_tree
var roll: Callable = func() -> bool: return _rng.randf() < ROLL_CHANCE
var report: Callable = _report
var build_body: Callable = _build_body

var figure: Node2D = null
var _forced := false
var _shown_for := 0.0
var _fade_at := 0.0
var _check_left := CHECK_SECONDS
var _focused := true
var _body_requested := false
var _body_retry_at := 0.0
var _rolled_dusk := ""
var _last_figure_at := 0.0
var _reach_cache: Dictionary = {}
var _sheet_images: Dictionary = {}
var _rng := RandomNumberGenerator.new()

func _ready() -> void:
    _rng.randomize()

func _notification(what: int) -> void:
    if what == NOTIFICATION_APPLICATION_FOCUS_OUT:
        _focused = false
    elif what == NOTIFICATION_APPLICATION_FOCUS_IN:
        _focused = true

func _process(delta: float) -> void:
    if world == null:
        return
    if figure != null:
        tick_figure(delta)
        return
    _check_left -= delta
    if _check_left <= 0.0:
        _check_left = CHECK_SECONDS
        try_show()

## Why the figure may not show now, or "" when it may. forced keeps only the
## checks an operator trigger still needs.
func refusal(forced: bool) -> String:
    if figure != null:
        return "already_showing"
    var pc: Node2D = world.placed_npcs.get(world.pc_actor_id, null)
    if pc == null or not is_instance_valid(pc):
        return "no_pc"
    if is_indoors.call():
        return "indoors"
    if forced:
        return ""
    if world.current_weather == "storm":
        return "storm"
    if world.current_phase != "night" or world._phase_pos >= DUSK_WINDOW:
        return "not_dusk"
    if not _focused:
        return "no_focus"
    if str(world.village_date) == "":
        return "no_date"
    var omens := _load_omens()
    var dusk := str(world.village_date)
    if _rolled_dusk == dusk or str(omens.get_value(SECTION, "rolled_dusk", "")) == dusk:
        return "rolled"
    var last := maxf(_last_figure_at, float(omens.get_value(SECTION, "last_figure_at", 0.0)))
    if last > 0.0 and now_unix.call() - last < COOLDOWN_SECONDS:
        return "cooldown"
    return ""

## A spot beside a tree in view and in range of the player, or null.
func pick_spot() -> Variant:
    var pc: Node2D = world.placed_npcs.get(world.pc_actor_id, null)
    if pc == null:
        return null
    var view: Rect2 = visible_rect.call()
    var cover := _cover_rects()
    var spots: Array[Vector2] = []
    for node in world.placed_objects.values():
        if not is_instance_valid(node) or not is_tree.call(str(node.get_meta("asset_id", ""))):
            continue
        var spot := _spot_beside(node)
        var tiles := spot.distance_to(pc.position) / TILE
        if tiles < MIN_TILES or tiles > MAX_TILES:
            continue
        if not view.encloses(Rect2(spot + FIGURE_RECT.position, FIGURE_RECT.size)):
            continue
        var terrain: int = world.terrain_at(spot)
        if terrain < 0 or terrain == Terrain.SHALLOW_WATER or terrain == Terrain.DEEP_WATER:
            continue
        if _covered(spot, cover):
            continue
        spots.append(spot)
    if spots.is_empty():
        return null
    return spots[_rng.randi_range(0, spots.size() - 1)]

## The periodic chance. The roll for this dusk is spent only once a spot is
## in view and the body can be drawn.
func try_show() -> void:
    if refusal(false) != "":
        return
    var spot = pick_spot()
    if spot == null:
        return
    var body: Node2D = build_body.call()
    if body == null:
        return
    # Kept in memory as well as in the file, so a file that cannot be written
    # still gives one roll per dusk and one showing per cooldown this session.
    _rolled_dusk = str(world.village_date)
    var omens := _load_omens()
    omens.set_value(SECTION, "rolled_dusk", _rolled_dusk)
    if not roll.call():
        _save_omens(omens)
        body.free()
        return
    _last_figure_at = now_unix.call()
    omens.set_value(SECTION, "last_figure_at", _last_figure_at)
    _save_omens(omens)
    _show(spot, body, false)

## The operator trigger (client_trigger kind treeline_figure).
func force_show(_detail: Dictionary = {}) -> void:
    var reason := refusal(true)
    var spot = null
    if reason == "":
        spot = pick_spot()
        if spot == null:
            reason = "no_treeline_on_screen"
    var body: Node2D = null
    if reason == "":
        body = build_body.call()
        if body == null:
            reason = "no_body"
    if reason != "":
        report.call("treeline_figure_skipped", {"reason": reason, "forced": true})
        return
    _show(spot, body, true)

func _show(spot: Vector2, body: Node2D, forced: bool) -> void:
    figure = Node2D.new()
    figure.name = "TreelineFigure"
    figure.position = spot
    figure.z_index = 10
    figure.add_child(body)
    world.objects_node.add_child(figure)
    _forced = forced
    _shown_for = 0.0
    _fade_at = _rng.randf_range(SHOW_SECONDS.x, SHOW_SECONDS.y)
    var pc: Node2D = world.placed_npcs.get(world.pc_actor_id, null)
    report.call("treeline_figure_shown", {
        "distance_tiles": snappedf(spot.distance_to(pc.position) / TILE, 0.1),
        "zoom": snappedf(_zoom(), 0.01),
        "forced": forced,
    })

## Advance a showing figure: gone off screen, faded, or fading.
func tick_figure(delta: float) -> void:
    _shown_for += delta
    var rect := Rect2(figure.position + FIGURE_RECT.position, FIGURE_RECT.size)
    if not (visible_rect.call() as Rect2).intersects(rect):
        remove("offscreen")
    elif _shown_for >= _fade_at + FADE_SECONDS:
        remove("faded")
    elif _shown_for >= _fade_at:
        figure.modulate.a = 1.0 - (_shown_for - _fade_at) / FADE_SECONDS

## The player started a walk: gone if it ends closer to the figure than the
## player stands now. event_client has snapped the character to the walk's
## start and stored the path in its "walking" meta.
func on_npc_walking(npc_id: String) -> void:
    if figure == null or world == null or npc_id != world.pc_actor_id:
        return
    var pc: Node2D = world.placed_npcs.get(npc_id, null)
    if pc == null:
        return
    var path: Array = pc.get_meta("walking", {}).get("path", [])
    if path.is_empty():
        return
    var goal: Vector2 = path.back()
    if goal.distance_to(figure.position) < pc.position.distance_to(figure.position):
        remove("walked")

func remove(reason: String) -> void:
    if figure == null:
        return
    report.call("treeline_figure_gone", {
        "reason": reason,
        "seconds_shown": int(round(_shown_for)),
        "forced": _forced,
    })
    figure.queue_free()
    figure = null
    _check_left = CHECK_SECONDS

## Just left of the tree: the figure's right edge at the tree's leftmost
## pixel, reaching in by up to OVERLAP_PX.
func _spot_beside(tree: Node2D) -> Vector2:
    var x := _tree_left_reach(tree) - FIGURE_RECT.end.x + _rng.randf_range(OVERLAP_PX.x, OVERLAP_PX.y)
    return tree.position + Vector2(x, -_rng.randf_range(BEHIND_PX.x, BEHIND_PX.y))

## The tree's leftmost drawn pixel over the rows the figure's body can cover,
## as an x offset from the tree's base. Cached per art and placement shape.
func _tree_left_reach(tree: Node2D) -> float:
    var sprite := _sprite_of(tree)
    var tex: Texture2D = null
    if sprite is Sprite2D:
        tex = sprite.texture
    elif sprite is AnimatedSprite2D and sprite.sprite_frames != null:
        tex = sprite.sprite_frames.get_frame_texture(sprite.animation, sprite.frame)
    if tex == null:
        return -FALLBACK_REACH_PX
    var source: Texture2D = tex
    var region := Rect2(Vector2.ZERO, tex.get_size())
    if tex is AtlasTexture and tex.atlas != null:
        source = tex.atlas
        region = tex.region
    var key := "%d|%s|%s|%s" % [source.get_instance_id(), region, sprite.position, sprite.scale]
    if _reach_cache.has(key):
        return _reach_cache[key]
    var reach := -FALLBACK_REACH_PX
    var image := _sheet_image(source)
    if image != null and sprite.scale.y > 0.0:
        var band_top := -BEHIND_PX.y + FIGURE_RECT.position.y
        var band_bottom := -BEHIND_PX.x + FIGURE_RECT.end.y
        var row_from := maxi(int(floor((band_top - sprite.position.y) / sprite.scale.y)), 0)
        var row_to := mini(int(ceil((band_bottom - sprite.position.y) / sprite.scale.y)), int(region.size.y))
        var left := int(region.size.x)
        for row in range(row_from, row_to):
            for col in left:
                if image.get_pixel(int(region.position.x) + col, int(region.position.y) + row).a >= TREE_ALPHA:
                    left = col
                    break
        # Nothing drawn at the figure's height: stand at the base.
        reach = sprite.position.x + left * sprite.scale.x if left < int(region.size.x) else 0.0
    _reach_cache[key] = reach
    return reach

## One CPU copy per sheet, shared by every tree cut from it.
func _sheet_image(source: Texture2D) -> Image:
    var id := source.get_instance_id()
    if not _sheet_images.has(id):
        var image := source.get_image()
        if image != null and image.is_compressed():
            image.decompress()
        _sheet_images[id] = image
    return _sheet_images[id]

## Rects of everything placed that is not a tree: no figure under a roof or
## inside a stall.
func _cover_rects() -> Array[Rect2]:
    var out: Array[Rect2] = []
    for node in world.placed_objects.values():
        if not is_instance_valid(node) or is_tree.call(str(node.get_meta("asset_id", ""))):
            continue
        var sprite := _sprite_of(node)
        if sprite == null:
            continue
        var rect: Rect2 = world.compute_object_hit_rect(node, sprite)
        if rect.has_area():
            out.append(rect)
    return out

func _covered(spot: Vector2, cover: Array[Rect2]) -> bool:
    for rect in cover:
        if rect.has_point(spot):
            return true
    return false

func _sprite_of(container: Node2D) -> Node2D:
    for child in container.get_children():
        if child is Sprite2D or child is AnimatedSprite2D:
            return child
    return null

func _load_omens() -> ConfigFile:
    var omens := ConfigFile.new()
    omens.load(omens_path)  # a missing file leaves it empty: never shown
    return omens

func _save_omens(omens: ConfigFile) -> void:
    var err := omens.save(omens_path)
    if err != OK:
        push_warning("treeline_figure: cannot save %s (error %d)" % [omens_path, err])

## A bare human body, still and facing the viewer. Null until its sheet is
## cached; a call starts the download unless one is in flight or a failed one
## is still within BODY_RETRY_SECONDS.
func _build_body() -> Node2D:
    if not world._npc_sheets.has(BODY_SHEET):
        if not _body_requested and now_unix.call() >= _body_retry_at:
            _body_requested = true
            world.get_or_load_npc_sheet(BODY_SHEET, _on_body_loaded, _on_body_failed)
        return null
    var body: AnimatedSprite2D = world._build_character_sprite(BODY_SPRITE)
    if body == null:
        return null
    body.animation = "south_idle"
    body.frame = 0
    body.stop()
    # Replaces the doll's palette swap: the figure is the body's own shading in grey.
    var material := ShaderMaterial.new()
    material.shader = FIGURE_SHADER
    material.set_shader_parameter("phase", _rng.randf_range(0.0, 100.0))
    body.material = material
    return body

func _on_body_loaded(_tex: Texture2D) -> void:
    _body_requested = false

func _on_body_failed() -> void:
    _body_requested = false
    _body_retry_at = now_unix.call() + BODY_RETRY_SECONDS

func _viewport_world_rect() -> Rect2:
    var viewport: Viewport = world.get_viewport()
    return viewport.get_canvas_transform().affine_inverse() * viewport.get_visible_rect()

func _zoom() -> float:
    var camera: Camera2D = world.get_viewport().get_camera_2d() if world.is_inside_tree() else null
    return camera.zoom.x if camera != null else 1.0

## Looked up by path so the script also loads where no autoload runs.
func _sound_indoors() -> bool:
    var sound := get_node_or_null("/root/Sound")
    return sound != null and bool(sound.indoors)

func _catalog_is_tree(asset_id: String) -> bool:
    var catalog := get_node_or_null("/root/Catalog")
    if catalog == null:
        return false
    return str(catalog.assets.get(asset_id, {}).get("category", "")) == "tree"

func _report(kind: String, detail: Dictionary) -> void:
    var events := get_node_or_null("/root/ClientEvents")
    if events != null:
        events.report(kind, detail)
