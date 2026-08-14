extends Node2D
## Rain impact splashes (LLM-629, splash half). World-space ground contact for
## the storm: short one-shot splash animations scattered at random uncovered
## points inside the visible map while the weather is "storm".
##
## This is deliberately NOT part of storm_fx.gd. The overlay is screen-space —
## it covers the window and ignores the map, which is right for falling rain
## and a bolt in the sky. A splash belongs to a specific piece of ground: it
## must sit at world coordinates (so it stays put as the camera pans), scale
## with zoom like every other world sprite, and skip ground that is covered.
## Different mechanism, so a different node.
##
## Cover: the artist's note says splashes must not land on tree tops or under
## overhangs. The client already knows enough to answer that — every placed
## object (trees, structures, stalls) has a world-space sprite rectangle via
## world.compute_object_hit_rect, the same rect click hit-testing uses. A
## random point inside any object's rect is rejected. That over-covers a
## little (no splash on the ground beside a trunk, where the canopy rect
## reaches), but a missing splash is invisible; a splash on a roof is not.
## Rects are cached and refreshed on a coarse clock — objects rarely move,
## and a splash landing on a duck's second-old position costs nothing.
##
## The bridge needs no exclusion in a top-down view: its deck IS the surface
## drops land on (the artist's "under bridges" is a side-view concern).
##
## Layering: z_index 2 — over terrain (0) and ground overlays like the bridge
## (1), under objects and NPCs (OBJECT_Z 10). As a child of World it is tinted
## by the day/night + storm CanvasModulate like everything else on the ground.
##
## Art: Seliel "weather effects 2.0" impact sheet — 8 frames of 16x16, drawn
## at the village render scale (2.0) so one art pixel is one world pixel like
## every other village sprite. The pack is purchased and gitignored, so a
## checkout without it is normal — CI runs that way. Missing art degrades to
## no splashes; the storm's rain, tint and lightning are untouched.

const SPLASH_SHEET: String = "res://assets/tilesets/mana-seed/weather-effects/weather effects, rain impact anim 16x16.png"
const FRAME_SIZE: Vector2i = Vector2i(16, 16)
const FRAME_COUNT: int = 8
## Artist-suggested 75ms per frame, so one splash lives 0.6s.
const FRAME_FPS: float = 1000.0 / 75.0

## Matches world.gd's default asset render scale — splash art pixels must be
## the same on-screen size as the village art they land next to.
const WORLD_RENDER_SCALE: float = 2.0

## Over terrain (0) and the bridge (asset z_index 1), under objects/NPCs (10).
const SPLASH_Z: int = 2

## Spawn pacing. Splashes-per-second is over the visible rect regardless of
## zoom: zoomed in they read as steady ground contact, zoomed out they thin
## per tile — which is fine, at 0.3 zoom a splash is ~10 screen px anyway.
const SPLASHES_PER_SECOND: float = 25.0

## Ceiling on banked spawn debt, which is also the per-frame spawn ceiling.
## A stalled frame arrives with its whole stall as one delta — a backgrounded
## web tab or a resumed device can hand _process a minute, which at 25/sec
## would be 1500 placements and nodes in a single frame, a stall exactly on
## resume. Rain that was "missed" while the tab was hidden isn't owed to
## anyone. At a normal 60fps cadence the accumulator stays near 0.4 and this
## clamp is inert.
const MAX_SPAWNS_PER_FRAME: float = 4.0

## Rejection-sampling budget per splash. In a dense patch (many rects) some
## spawns find no uncovered point and are skipped — correct, covered ground
## is exactly where splashes don't belong.
const MAX_PLACEMENT_ATTEMPTS: int = 8

## How stale the cover-rect cache may go. Placed objects move rarely (editor
## drags, ducks), so a coarse clock trades invisible error for not walking
## every placed object 25 times a second.
const COVER_REFRESH_SECONDS: float = 1.0

## Injected by world.gd. Duck-typed: needs .placed_objects (id -> Node2D
## container) and .compute_object_hit_rect(container, sprite) -> Rect2.
var world: Node2D = null

var _frames: SpriteFrames = null
var _active: bool = false
var _spawn_accum: float = 0.0
var _cover_rects: Array[Rect2] = []
var _cover_age: float = COVER_REFRESH_SECONDS  # expired, so first use gathers


func _ready() -> void:
    z_index = SPLASH_Z
    _frames = _load_frames(SPLASH_SHEET)
    set_process(false)


## Start (active=true) or stop (active=false) splash spawning. Idempotent.
## Stopping doesn't clear live splashes — each finishes its 0.6s animation and
## frees itself, the same way clearing the storm lets in-flight rain particles
## finish falling rather than vanishing mid-air.
func set_storm(active: bool) -> void:
    _active = active
    set_process(active and _frames != null)
    if not active:
        _spawn_accum = 0.0
        _cover_age = COVER_REFRESH_SECONDS


func _process(delta: float) -> void:
    _cover_age += delta
    if _cover_age >= COVER_REFRESH_SECONDS:
        _cover_rects = _gather_cover_rects()
        _cover_age = 0.0

    # Capping the bank at MAX_SPAWNS_PER_FRAME bounds the loop below to that
    # many iterations — no separate per-frame counter needed.
    _spawn_accum = minf(_spawn_accum + delta * SPLASHES_PER_SECOND, MAX_SPAWNS_PER_FRAME)
    if _spawn_accum < 1.0:
        return
    # Splashes stop at the map edge, not the camera edge. Camera limits
    # normally keep the two identical, but that is the camera's invariant —
    # don't inherit it here.
    var view: Rect2 = _visible_world_rect().intersection(_map_rect())
    if not view.has_area():
        _spawn_accum = 0.0
        return
    while _spawn_accum >= 1.0:
        _spawn_accum -= 1.0
        var point = _pick_uncovered_point(view, _cover_rects)
        if point != null:
            _spawn_splash(point)


## Load the impact sheet into a one-shot SpriteFrames animation. Missing art
## (gitignored purchased pack — CI's normal state) returns null and the node
## never spawns; the rest of the storm doesn't care.
func _load_frames(path: String) -> SpriteFrames:
    var sheet: Texture2D = load(path) if ResourceLoader.exists(path) else null
    if sheet == null:
        push_warning("rain_splashes: missing impact sheet " + path)
        return null
    return _build_frames(sheet)


## Cut a horizontal strip into a non-looping "default" animation. Stops at the
## last whole frame if the sheet is undersized (the shape a re-exported or
## wrong-sized replacement would have) rather than reading past the edge.
func _build_frames(sheet: Texture2D) -> SpriteFrames:
    var image: Image = sheet.get_image()
    if image == null:
        push_warning("rain_splashes: unreadable impact sheet")
        return null
    var frames := SpriteFrames.new()
    # SpriteFrames ships with a default animation already present.
    frames.set_animation_loop("default", false)
    frames.set_animation_speed("default", FRAME_FPS)
    for i in FRAME_COUNT:
        var region := Rect2i(Vector2i(i * FRAME_SIZE.x, 0), FRAME_SIZE)
        if not Rect2i(Vector2i.ZERO, image.get_size()).encloses(region):
            push_warning("rain_splashes: impact sheet too small for frame %d" % i)
            break
        frames.add_frame("default", ImageTexture.create_from_image(image.get_region(region)))
    if frames.get_frame_count("default") == 0:
        return null
    return frames


## World-space rectangles of every placed object's sprite — the "covered
## ground" a splash must not land on. Containers without a sprite yet (still
## downloading) or with an indeterminate rect are skipped: no rect, no cover.
func _gather_cover_rects() -> Array[Rect2]:
    var rects: Array[Rect2] = []
    if world == null:
        return rects
    for obj_id in world.placed_objects:
        var container: Node2D = world.placed_objects[obj_id]
        if container == null or container.get_child_count() == 0:
            continue
        var sprite_node: Node2D = null
        for child in container.get_children():
            if child is Sprite2D or child is AnimatedSprite2D:
                sprite_node = child
                break
        if sprite_node == null:
            continue
        var rect: Rect2 = world.compute_object_hit_rect(container, sprite_node)
        # has_area, not a zero-compare: a rect degenerate in ONE dimension (or
        # negative) can't cover ground either, and mustn't reach has_point.
        if rect.has_area():
            rects.append(rect)
    return rects


func _is_covered(point: Vector2, rects: Array[Rect2]) -> bool:
    for rect in rects:
        if rect.has_point(point):
            return true
    return false


## A random point in view on uncovered ground, or null when every attempt
## landed on cover (a dense patch — skip the splash, don't force one).
func _pick_uncovered_point(view: Rect2, rects: Array[Rect2]) -> Variant:
    for _attempt in MAX_PLACEMENT_ATTEMPTS:
        var point := Vector2(
            randf_range(view.position.x, view.end.x),
            randf_range(view.position.y, view.end.y),
        )
        if not _is_covered(point, rects):
            return point
    return null


## The world-space rectangle the camera currently shows.
func _visible_world_rect() -> Rect2:
    var viewport: Viewport = get_viewport()
    return viewport.get_canvas_transform().affine_inverse() * viewport.get_visible_rect()


## The world-space rectangle of generated terrain. The grid is map_width x
## map_height tiles of 32 world px, with world (0,0) sitting pad_x/pad_y tiles
## in from the grid corner (world.gd's tile padding) — so terrain starts at
## negative world coords and a splash outside this rect would land on void.
func _map_rect() -> Rect2:
    if world == null:
        return Rect2()
    return Rect2(
        -world.pad_x * 32.0,
        -world.pad_y * 32.0,
        world.map_width * 32.0,
        world.map_height * 32.0,
    )


func _spawn_splash(point: Vector2) -> void:
    var splash := AnimatedSprite2D.new()
    splash.sprite_frames = _frames
    splash.position = point
    splash.scale = Vector2(WORLD_RENDER_SCALE, WORLD_RENDER_SCALE)
    # One-shot: the animation doesn't loop, so finishing frees the node.
    splash.animation_finished.connect(splash.queue_free)
    add_child(splash)
    splash.play("default")
