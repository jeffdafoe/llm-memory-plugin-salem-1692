extends CanvasLayer
## Building interior window (LLM-750). While the player's character is inside
## a structure that has a room (InteriorRooms), a framed window over the
## dimmed village shows the room and the people in it: the keeper (whoever
## works there) behind the bar, the others in the room's spots, the player's
## own character at the door, and "and N more" for anyone past the spots.
## Sleepers are upstairs and not drawn.
##
## main.gd opens it when the character goes in and closes it when they walk
## out; the ✕ (or a click on the dimmed village, or Esc) only hides it, and a
## click on the building reopens it. A click on a person opens the talk panel,
## which sits on the layer above, so the room stays in view behind the talk.
## A line spoken by someone drawn in the room shows as the village's speech
## bubble over their head (LLM-751); it survives a redraw of the room. The window reads the world each half
## second and rebuilds only when who is where has changed.
##
## A market stall (StallScenes, LLM-753) opens the same window while the
## character stands at the stall's counter — nobody but the keeper goes in.
## The keeper (whoever is inside the stall) stands behind the counter, the
## people at its loiter pin stand outside in front, and the goods the keeper
## sells (engine read pc/stall, refetched every few seconds) lie in piles on
## the floor behind and on the counter top. Layers, back to front: the stall,
## the floor piles, the keeper, the counter, the counter goods, the customers.
##
## People come and go on foot (LLM-754): once the window is open, someone who
## leaves turns and walks out — a stall's nearer side, a room's door — fading
## as they go, and a newcomer walks in the same way. The one tending a stall
## bustles along the counter every few seconds and stops, facing the
## customers, to speak. Clicks find people where they stand now.
##
## A sale at a stall is handed over the counter (LLM-756): the keeper walks
## along the counter to the buyer, sets the good down in front of them, and
## it goes to them; a good the keeper buys lies in front of the seller until
## the keeper comes for it.
##
## PIXEL RULE (as repair_stage.gd): the room and the people are drawn in art
## pixels at a WHOLE number of screen pixels each, `k`; the window stretch is
## cancelled and the origin snapped to a whole screen pixel. People come from
## the world's own sprite builder, brought down from the village's draw scale
## to the room's.

signal opened
signal closed
signal person_clicked(npc_id: String)

const FONT_PATH := "res://assets/fonts/IMFellEnglish-Regular.ttf"
const MAX_PIXEL_SCALE := 4
const REFRESH_SECONDS := 0.5
## The dimming world.gd gives a resting villager.
const RESTING_DIM := Color(0.58, 0.62, 0.75, 1.0)
## A person's click box around their feet, in art pixels.
const HIT_BOX := Rect2(-8, -28, 16, 28)
## Speech bubbles (LLM-751): the village's bubble over a speaker drawn in the
## room, its tail just above the head — room art px above the feet (a farmer's
## hat reaches about 34 above the feet, a pack sprite's head about 29).
const SpeechBubbleScript = preload("res://scripts/speech_bubble.gd")
const BUBBLE_NODE := "SpeechBubble"
const BUBBLE_ANCHOR_Y := -36.0
## How often an open stall re-reads its shelf.
const WARES_SECONDS := 5.0
## Coming and going (LLM-754): someone who leaves turns and walks off this far
## (art px) toward the way out, fading over the last FADE_SECONDS; a newcomer
## walks in the same way. WALK_SPEED is art px a second.
const WALK_OFF := 40.0
const WALK_SPEED := 24.0
const FADE_SECONDS := 0.5
const LEAVING_PREFIX := "leaving_"
## The one tending a stall bustles behind the counter (LLM-754): every
## BUSTLE_MIN..BUSTLE_MAX seconds a walk to a point up to BUSTLE_REACH either
## side of their spot (or back to it) at BUSTLE_SPEED, then a look at the
## goods or the customers. Speaking stops them, facing the customers, for
## SPEAK_HOLD seconds.
const BUSTLE_MIN := 2.5
const BUSTLE_MAX := 6.0
const BUSTLE_REACH := 34.0
const BUSTLE_SPEED := 16.0
const SPEAK_HOLD := 4.0
## A sale over a stall's counter (LLM-756): the good lies on the counter
## HANDOFF_HOLD seconds, then goes to its new owner over HANDOFF_AWAY seconds,
## sliding HANDOFF_SLIDE art px their way as it fades.
const HANDOFF_HOLD := 2.0
const HANDOFF_AWAY := 0.6
const HANDOFF_SLIDE := 14.0
## In a room (LLM-757) a sale is served from the bar at SERVE_SPEED art px a
## second; a meal set on a table stays MEAL_SECONDS before it is gone.
const SERVE_SPEED := 48.0
const MEAL_SECONDS := 6.0

var world: Node2D = null
var pc_actor_id := ""
var structure_id := ""
var room: Dictionary = {}
var k := 1

var root: Control = null
var backdrop: ColorRect = null
var sheet: PanelContainer = null
var title_label: Label = null
var close_button: Button = null
var stage: Control = null
var more_label: Label = null

## Test seam: path -> Texture2D, used in place of a download.
var texture_hook: Callable = Callable()
## Test seam: structure id -> the pc/stall wares array, used in place of the
## request.
var wares_hook: Callable = Callable()

static var _textures: Dictionary = {}

var _scene: Node2D = null
var _background: Sprite2D = null
## Stall layers (empty for a room): floor piles, the keeper, the counter, the
## goods on it. A room's people all go in _people, as do a stall's customers.
var _goods_back: Node2D = null
var _staff: Node2D = null
var _counter: Sprite2D = null
var _goods_front: Node2D = null
## Goods changing hands over the counter, above the goods on it.
var _handoff: Node2D = null
## Handoffs waiting their turn, and the one under way ({} when none).
var _handoffs: Array = []
var _handoff_now: Dictionary = {}
var _people: Node2D = null
var _font: Font = null
var _stretch := Vector2.ONE
var _assigned: Dictionary = {}
var _signature := ""
var _refresh_left := 0.0
var _hits: Array = []
var _fetching: Dictionary = {}
var _wares: Array = []
var _wares_left := 0.0
var _http_wares: HTTPRequest = null
## False while open() draws the room as it is; true after, when a change in
## who is here is someone coming or going.
var _entrances := false
var _bustle_left := 0.0
var _bustle_rng := RandomNumberGenerator.new()


func _ready() -> void:
    # Under the talk panel (4): a conversation opened from the room shows in
    # front of it. main.gd sets this from the talk panel's layer.
    layer = 3
    _font = load(FONT_PATH) if ResourceLoader.exists(FONT_PATH) else ThemeDB.fallback_font
    _build_ui()
    visible = false
    set_process(false)
    get_viewport().size_changed.connect(_relayout)


func _build_ui() -> void:
    root = Control.new()
    root.set_anchors_preset(Control.PRESET_FULL_RECT)
    root.mouse_filter = Control.MOUSE_FILTER_STOP
    add_child(root)

    # The village stays visible around the window, dimmed.
    backdrop = ColorRect.new()
    backdrop.color = Color(0, 0, 0, 0.4)
    backdrop.set_anchors_preset(Control.PRESET_FULL_RECT)
    backdrop.mouse_filter = Control.MOUSE_FILTER_STOP
    backdrop.gui_input.connect(_on_backdrop_input)
    root.add_child(backdrop)

    var center := CenterContainer.new()
    center.set_anchors_preset(Control.PRESET_FULL_RECT)
    center.mouse_filter = Control.MOUSE_FILTER_IGNORE
    root.add_child(center)

    sheet = PanelContainer.new()
    sheet.mouse_filter = Control.MOUSE_FILTER_STOP
    center.add_child(sheet)

    var pad := MarginContainer.new()
    for side in ["margin_left", "margin_right", "margin_bottom"]:
        pad.add_theme_constant_override(side, 10)
    pad.add_theme_constant_override("margin_top", 6)
    sheet.add_child(pad)

    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 6)
    pad.add_child(vb)

    var head := HBoxContainer.new()
    vb.add_child(head)
    title_label = _label(18, Color(0.95, 0.86, 0.66))
    title_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    head.add_child(title_label)
    close_button = Button.new()
    # "×", not "✕": the default font has no U+2715, and a browser shows the
    # missing-glyph box (a desktop run hides it behind a system font).
    close_button.text = "×"
    close_button.add_theme_font_size_override("font_size", 24)
    close_button.custom_minimum_size = Vector2(44, 44)
    close_button.focus_mode = Control.FOCUS_NONE
    close_button.pressed.connect(close)
    head.add_child(close_button)

    stage = Control.new()
    stage.name = "Stage"
    # Not clipped: a speech bubble over the back row rises past the room's top.
    stage.clip_contents = false
    stage.mouse_filter = Control.MOUSE_FILTER_STOP
    stage.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
    stage.gui_input.connect(_on_stage_input)
    stage.item_rect_changed.connect(_snap)
    vb.add_child(stage)

    _scene = Node2D.new()
    stage.add_child(_scene)
    _background = Sprite2D.new()
    _background.centered = false
    _scene.add_child(_background)
    _goods_back = Node2D.new()
    _scene.add_child(_goods_back)
    _staff = Node2D.new()
    _scene.add_child(_staff)
    _counter = Sprite2D.new()
    _counter.centered = false
    _scene.add_child(_counter)
    _goods_front = Node2D.new()
    _scene.add_child(_goods_front)
    _handoff = Node2D.new()
    _scene.add_child(_handoff)
    _people = Node2D.new()
    _people.y_sort_enabled = true
    _scene.add_child(_people)

    more_label = _label(15, Color(0.85, 0.78, 0.62))
    more_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    more_label.visible = false
    vb.add_child(more_label)


func _label(font_size: int, color: Color) -> Label:
    var l := Label.new()
    l.add_theme_font_override("font", _font)
    l.add_theme_font_size_override("font_size", font_size)
    l.add_theme_color_override("font_color", color)
    return l


## Show the room — or the stall — of `sid`. Anything else is a no-op.
func open(sid: String) -> void:
    var r := InteriorRooms.room_for(sid)
    if r.is_empty():
        r = StallScenes.scene_for(world, Catalog.assets, sid)
    if r.is_empty():
        return
    if world != null and world.has_signal("npc_spoke") and not world.npc_spoke.is_connected(_on_npc_spoke):
        world.npc_spoke.connect(_on_npc_spoke)
    if world != null and world.has_signal("pay_resolved") and not world.pay_resolved.is_connected(on_pay_resolved):
        world.pay_resolved.connect(on_pay_resolved)
    var was_open := visible
    for child in _handoff.get_children():
        # Its tween is bound to it and dies with it; stopped first all the same.
        if child.has_meta("tween"):
            (child.get_meta("tween") as Tween).kill()
        child.free()
    _handoffs = []
    _handoff_now = {}
    if sid != structure_id:
        _assigned = {}
        _wares = []
    structure_id = sid
    room = r
    title_label.text = str(room.get("title", ""))
    _apply_textures()
    visible = true
    _signature = ""
    _relayout()
    # Drawn as it is: nobody walks in or out of a window just opened.
    _entrances = false
    for layer in [_people, _staff]:
        for child in layer.get_children():
            if child.has_meta("leaving"):
                child.free()
    refresh()
    _entrances = true
    _bustle_left = _bustle_rng.randf_range(BUSTLE_MIN, BUSTLE_MAX)
    _draw_goods()
    _refresh_left = REFRESH_SECONDS
    _wares_left = 0.0
    if is_stall():
        _request_wares()
        _wares_left = WARES_SECONDS
    set_process(true)
    if not was_open:
        opened.emit()


func is_stall() -> bool:
    return str(room.get("kind", "")) == "stall"


## Hide the window. Idempotent.
func close() -> void:
    if not visible:
        return
    visible = false
    set_process(false)
    closed.emit()


func is_open() -> bool:
    return visible


func _process(delta: float) -> void:
    _refresh_left -= delta
    if _refresh_left <= 0.0:
        _refresh_left = REFRESH_SECONDS
        refresh()
    if is_stall():
        _wares_left -= delta
        if _wares_left <= 0.0:
            _wares_left = WARES_SECONDS
            _request_wares()
        _bustle(delta)


## Read who is inside and redraw the people when that has changed.
func refresh() -> void:
    if world == null or room.is_empty():
        return
    var cast := occupants()
    var sig := JSON.stringify(cast) + "|%d" % k
    if sig == _signature:
        return
    _signature = sig
    _draw_people(cast)


## Who is drawn where: [{id, spot: {x, y, facing, pose}}] in drawing order
## plus the "more" count, from the world's containers. Pure apart from the
## sticky spot memory, so tests drive it directly.
func occupants() -> Dictionary:
    if is_stall():
        return _stall_occupants()
    var keeper_id := ""
    var others: Array = []
    var pc_inside := false
    var ids: Array = world.placed_npcs.keys()
    ids.sort()
    for id in ids:
        var c: Node2D = world.placed_npcs[id]
        if not is_instance_valid(c):
            continue
        if not bool(c.get_meta("inside", false)) or str(c.get_meta("inside_structure_id", "")) != structure_id:
            continue
        # Asleep means upstairs in a bedroom, not in this room.
        if str(c.get_meta("dormant", "")) == "sleeping":
            continue
        if id == pc_actor_id:
            pc_inside = true
        elif keeper_id == "" and str(c.get_meta("work_structure_id", "")) == structure_id:
            keeper_id = id
        else:
            others.append(id)
    var spots: Array = room.get("spots", [])
    var seated := InteriorRooms.assign(spots.size(), others, _assigned)
    _assigned = seated["spots"]
    var placed: Array = []
    if keeper_id != "":
        var k := _entry(keeper_id, room.get("keeper", {}))
        k["keeper"] = true
        placed.append(k)
    for id in others:
        if _assigned.has(id):
            placed.append(_entry(id, spots[int(_assigned[id])]))
    if pc_inside:
        placed.append(_entry(pc_actor_id, room.get("door", {})))
    return {"placed": placed, "more": int(seated["more"])}


## A stall's people: behind the counter, whoever is inside the stall — its
## own keeper first, else a hand tending it (anyone else inside is counted,
## not drawn); in front, whoever stands at its loiter pin, the player's
## character in the middle.
func _stall_occupants() -> Dictionary:
    var keeper_id := ""
    var inside: Array = []
    var customers: Array = []
    var pc_here := false
    var pin: Vector2i = room.get("pin", Vector2i.ZERO)
    var ids: Array = world.placed_npcs.keys()
    ids.sort()
    for id in ids:
        var c: Node2D = world.placed_npcs[id]
        if not is_instance_valid(c) or str(c.get_meta("dormant", "")) == "sleeping":
            continue
        if bool(c.get_meta("inside", false)):
            if str(c.get_meta("inside_structure_id", "")) != structure_id:
                continue
            if keeper_id == "" and str(c.get_meta("work_structure_id", "")) == structure_id:
                keeper_id = id
            else:
                inside.append(id)
        elif StallScenes.at_pin(StallScenes.standing_tile(c), pin):
            if id == pc_actor_id:
                pc_here = true
            else:
                customers.append(id)
    if keeper_id == "" and not inside.is_empty():
        keeper_id = inside.pop_front()
    var spots: Array = room.get("spots", [])
    var stood := InteriorRooms.assign(spots.size(), customers, _assigned)
    _assigned = stood["spots"]
    var placed: Array = []
    if keeper_id != "":
        var e := _entry(keeper_id, room.get("keeper", {}))
        e["behind"] = true
        placed.append(e)
    for id in customers:
        if _assigned.has(id):
            placed.append(_entry(id, spots[int(_assigned[id])]))
    if pc_here:
        placed.append(_entry(pc_actor_id, room.get("door", {})))
    return {"placed": placed, "more": int(stood["more"]) + inside.size()}


## One drawn person. `look` stamps the outfit and the rest state so a change
## to either redraws the room.
func _entry(id: String, spot: Dictionary) -> Dictionary:
    var c: Node2D = world.placed_npcs[id]
    var look := "%d|%s" % [hash(JSON.stringify(c.get_meta("sprite_data", {}))), str(c.get_meta("dormant", ""))]
    return {"id": id, "spot": spot, "look": look}


func _draw_people(cast: Dictionary) -> void:
    # A bubble outlives the redraw (someone came in mid-sentence): lift it off
    # the old person and put it back on the new one below.
    var bubbles := {}
    # Who was drawn where, so a leaver walks off from there, a newcomer cut
    # short carries on walking in, and a stall's keeper carries on from
    # wherever they had bustled to.
    var was := {}
    for child in _people.get_children() + _staff.get_children():
        # Someone walking out keeps walking, but their line is lifted too: if
        # they come back it goes on their new holder, else back on them.
        var id := str(child.get_meta("leaving")) if child.has_meta("leaving") else str(child.name)
        var bubble := child.get_node_or_null(BUBBLE_NODE)
        if bubble != null:
            child.remove_child(bubble)
            bubbles[id] = bubble
        if child.has_meta("leaving"):
            continue
        was[id] = {"position": child.position, "alpha": child.modulate.a,
            "entering": child.has_meta("entering"), "behind": child.get_parent() == _staff,
            "sprite_data": child.get_meta("sprite_data", {})}
        child.free()
    _hits.clear()
    var drawn := {}
    for entry in cast["placed"]:
        var id := str(entry["id"])
        var spot: Dictionary = entry["spot"]
        var c: Node2D = world.placed_npcs.get(id, null)
        if c == null:
            continue
        var sprite_data: Dictionary = c.get_meta("sprite_data", {})
        var spr := _sprite(sprite_data)
        if spr == null:
            continue
        var behind := bool(entry.get("behind", false))
        var home := Vector2(int(spot.get("x", 0)), int(spot.get("y", 0)))
        # Back before their way out finished: the one walking out goes.
        _stop_leaving(id)
        var holder := Node2D.new()
        holder.name = id
        holder.position = home
        holder.set_meta("home", home)
        holder.set_meta("sprite_data", sprite_data)
        if bool(entry.get("keeper", false)):
            holder.set_meta("keeper", true)
        holder.add_child(spr)
        # A window just opened draws everyone at their spots: nothing carries
        # over from the last time it was open.
        var stays := _entrances and was.has(id) and bool(was[id]["behind"]) == behind
        # The keeper carries on from where they bustled to, a newcomer from
        # partway in; anyone else stands at their (maybe new) spot.
        if stays and (behind or bool(was[id]["entering"])):
            holder.position = was[id]["position"]
            holder.modulate.a = was[id]["alpha"]
        # A stall's keeper stands behind the counter, which is drawn over them.
        (_staff if behind else _people).add_child(holder)
        _pose(spr, str(spot.get("facing", "south")), str(spot.get("pose", "stand")))
        if str(c.get_meta("dormant", "")) == "resting":
            spr.modulate = RESTING_DIM
        if _entrances and not stays:
            _walk_in(holder, spot, behind, true)
        elif stays and bool(was[id]["entering"]):
            _walk_in(holder, spot, behind, false)
        _hits.append({"id": id, "holder": holder})
        drawn[id] = true
    if _entrances:
        for id in was:
            if not drawn.has(id):
                _walk_out(id, was[id])
    # The keeper was rebuilt mid-handoff: their walk to it goes on.
    if not _handoff_now.is_empty() and not bool(_handoff_now["arrived"]):
        _handoff_walk()
    for id in bubbles:
        # A leaver takes their last line out with them.
        var holder := _holder(id)
        if holder == null:
            holder = _leaving(id)
        if holder != null:
            # The speaker may have moved to the other side of the room.
            var carried: Node2D = bubbles[id]
            if carried.opens_left != _opens_left_at(holder):
                carried.opens_left = _opens_left_at(holder)
                carried.queue_redraw()
            holder.add_child(carried)
        else:
            bubbles[id].free()
    var more := int(cast["more"])
    more_label.text = "and %d more" % more
    more_label.visible = more > 0


## A person's sprite at the room's scale: from the village's draw scale down
## to the room's art pixels.
func _sprite(sprite_data: Dictionary) -> AnimatedSprite2D:
    var spr: AnimatedSprite2D = world._build_character_sprite(sprite_data)
    if spr == null:
        return null
    var s := spr.scale.x
    if s > 0.0:
        spr.scale /= s
        spr.position /= s
    return spr


func _sprite_of(holder: Node2D) -> AnimatedSprite2D:
    for child in holder.get_children():
        if child is AnimatedSprite2D:
            return child
    return null


## Where someone at `at` comes in from and goes out to: a stall's nearer side
## (the keeper behind the counter too), a room's door.
func _doorway(at: Vector2, behind: bool) -> Vector2:
    if is_stall() or behind or not room.has("door"):
        var size: Vector2i = room.get("size", Vector2i(128, 112))
        return at + Vector2(-WALK_OFF if at.x < size.x / 2.0 else WALK_OFF, 0)
    var door := Vector2(int(room["door"].get("x", 0)), int(room["door"].get("y", 0)))
    return door + Vector2(0, 8) if at.distance_to(door) < 4.0 else door


## A newcomer walks in from the doorway to their spot, fading in — from the
## doorway, or (`from_doorway` false) on from wherever a redraw caught them.
## The holder carries an "entering" meta until they are there.
func _walk_in(holder: Node2D, spot: Dictionary, behind: bool, from_doorway: bool) -> void:
    var home: Vector2 = holder.get_meta("home")
    if from_doorway:
        holder.position = _doorway(home, behind)
        holder.modulate.a = 0.0
    holder.set_meta("entering", true)
    _walk(holder, home, WALK_SPEED, str(spot.get("facing", "south")), str(spot.get("pose", "stand")),
        func(): holder.remove_meta("entering"), 1.0, 0.0, FADE_SECONDS * (1.0 - holder.modulate.a))


## The one walking out as `id`, or null.
func _leaving(id: String) -> Node2D:
    for layer in [_people, _staff]:
        for child in layer.get_children():
            if str(child.get_meta("leaving", "")) == id:
                return child
    return null


## Drop whoever is walking out as `id` (they came back, or leave again).
func _stop_leaving(id: String) -> void:
    var ghost := _leaving(id)
    while ghost != null:
        ghost.free()
        ghost = _leaving(id)


## Someone no longer here turns and walks from where they were drawn out
## through the doorway, fading as they go, then is gone. Not clickable.
func _walk_out(id: String, was: Dictionary) -> void:
    _stop_leaving(id)
    var spr := _sprite(was["sprite_data"])
    if spr == null:
        return
    var ghost := Node2D.new()
    ghost.name = LEAVING_PREFIX + id
    ghost.set_meta("leaving", id)
    ghost.modulate.a = was["alpha"]
    ghost.position = was["position"]
    ghost.add_child(spr)
    (_staff if bool(was["behind"]) else _people).add_child(ghost)
    var to := _doorway(ghost.position, bool(was["behind"]))
    var seconds := ghost.position.distance_to(to) / WALK_SPEED
    _walk(ghost, to, WALK_SPEED, "south", "stand", ghost.queue_free,
        0.0, maxf(0.0, seconds - FADE_SECONDS), minf(FADE_SECONDS, seconds))


## Walk a drawn person to `to` at `speed` art px a second, walking the way
## they go; at the end they take `facing` (and `pose`) and `done` runs. With
## `fade_to` >= 0 they also fade to that alpha over `fade_seconds`, starting
## `fade_delay` into the walk — one tween, so the walk and the fade end
## together. The holder carries a "walking" meta (the tween) while under way.
func _walk(holder: Node2D, to: Vector2, speed: float, facing: String, pose := "stand", done := Callable(),
        fade_to := -1.0, fade_delay := 0.0, fade_seconds := 0.0) -> void:
    var spr := _sprite_of(holder)
    var dist := holder.position.distance_to(to)
    if spr != null and dist > 0.5 and spr.sprite_frames != null:
        var anim := str(world.facing_from_vec(to - holder.position)) + "_walk"
        if spr.sprite_frames.has_animation(anim):
            spr.play(anim)
    var tween := holder.create_tween()
    holder.set_meta("walking", tween)
    tween.tween_property(holder, "position", to, maxf(dist / speed, 0.01))
    if fade_to >= 0.0:
        tween.parallel().tween_property(holder, "modulate:a", fade_to, maxf(fade_seconds, 0.01)).set_delay(fade_delay)
    tween.tween_callback(func():
        holder.remove_meta("walking")
        if spr != null:
            _pose(spr, facing, pose)
        if done.is_valid():
            done.call())


## The one tending a stall bustles behind the counter: now and then a walk to
## another point along it (or back to their spot), then a look at the goods
## behind them or at the customers.
func _bustle(delta: float) -> void:
    var holder := _tending()
    if holder == null or holder.has_meta("walking") or not _handoff_now.is_empty():
        return
    _bustle_left -= delta
    if _bustle_left > 0.0:
        return
    _bustle_left = _bustle_rng.randf_range(BUSTLE_MIN, BUSTLE_MAX)
    var home: Vector2 = holder.get_meta("home")
    var to := home
    if holder.position.distance_to(home) < 2.0 or _bustle_rng.randf() < 0.5:
        to = Vector2(roundf(home.x + _bustle_rng.randf_range(-BUSTLE_REACH, BUSTLE_REACH)), home.y)
    var facing := "south" if to == home else ("north" if _bustle_rng.randf() < 0.5 else "south")
    _walk(holder, to, BUSTLE_SPEED, facing)


## The person behind a stall's counter, or null.
func _tending() -> Node2D:
    if not is_stall():
        # A room's keeper stands among the others, marked by _draw_people.
        for child in _people.get_children():
            if child.has_meta("keeper") and not child.has_meta("leaving"):
                return child
        return null
    for child in _staff.get_children():
        if not child.has_meta("leaving"):
            return child
    return null


## Seat a farmer-base villager in a seat spot; everyone else stands, facing
## the spot's way when their sprite has that facing.
func _pose(spr: AnimatedSprite2D, facing: String, pose: String) -> void:
    if spr.sprite_frames == null:
        return
    for anim in [facing + "_" + ("seat" if pose == "seat" else "idle"), facing + "_idle", "south_idle"]:
        if spr.sprite_frames.has_animation(anim):
            spr.play(anim)
            return


func _relayout() -> void:
    if not visible or not is_inside_tree() or room.is_empty():
        return
    _stretch = get_viewport().get_final_transform().get_scale()
    if _stretch.x <= 0.0 or _stretch.y <= 0.0:
        _stretch = Vector2.ONE
    var size: Vector2i = room.get("size", Vector2i(128, 112))
    var avail := get_viewport().get_visible_rect().size * _stretch * Vector2(0.92, 0.72)
    k = pixel_scale_for(avail, Vector2(size))
    stage.custom_minimum_size = Vector2(size.x * k / _stretch.x, size.y * k / _stretch.y)
    _scene.scale = Vector2(k / _stretch.x, k / _stretch.y)
    _snap()


## The biggest whole scale at which the room fits, 1..MAX_PIXEL_SCALE.
static func pixel_scale_for(avail_px: Vector2, art: Vector2) -> int:
    if art.x <= 0 or art.y <= 0:
        return 1
    return clampi(mini(floori(avail_px.x / art.x), floori(avail_px.y / art.y)), 1, MAX_PIXEL_SCALE)


## Put the room's origin on a whole screen pixel so every art pixel is
## exactly k screen pixels.
func _snap() -> void:
    if stage == null or not stage.is_inside_tree():
        return
    var screen_origin := stage.get_global_transform_with_canvas().origin * _stretch
    _scene.position = (screen_origin.round() - screen_origin) / _stretch


## A line spoken by someone drawn in the room shows as a bubble over their
## head; a newer line replaces theirs. Someone not drawn (outside, upstairs, or
## past the spots) gets none — the talk panel keeps every line.
func say(npc_id: String, text: String) -> void:
    if npc_id == "" or text == "":
        return
    var holder := _holder(npc_id)
    if holder == null:
        # Someone walking out says it over their shoulder.
        holder = _leaving(npc_id)
    if holder == null:
        return
    # The one tending a stall stops to speak, facing the customers — but not
    # in the middle of handing something over.
    if holder.get_parent() == _staff and not holder.has_meta("leaving") and _handoff_now.is_empty():
        if holder.has_meta("walking"):
            (holder.get_meta("walking") as Tween).kill()
            holder.remove_meta("walking")
        var spr := _sprite_of(holder)
        if spr != null:
            _pose(spr, "south", "stand")
        _bustle_left = SPEAK_HOLD
    var existing := holder.get_node_or_null(BUBBLE_NODE)
    if existing != null:
        # Out of the tree now, or the new bubble would be renamed (as world.gd).
        holder.remove_child(existing)
        existing.queue_free()
    var bubble: Node2D = SpeechBubbleScript.new()
    bubble.name = BUBBLE_NODE
    bubble.anchor_y = BUBBLE_ANCHOR_Y
    bubble.opens_left = _opens_left_at(holder)
    holder.add_child(bubble)
    bubble.setup(text)


## The drawn person `id`, in front of the counter or behind it, or null.
func _holder(id: String) -> Node2D:
    var holder := _people.get_node_or_null(NodePath(id)) as Node2D
    if holder == null:
        holder = _staff.get_node_or_null(NodePath(id)) as Node2D
    return holder


## On the right side of the room a bubble opens left, over the room rather
## than past the window's edge (LLM-752).
func _opens_left_at(holder: Node) -> bool:
    var size: Vector2i = room.get("size", Vector2i(128, 112))
    return (holder as Node2D).position.x > size.x / 2.0


func _on_npc_spoke(npc_id: String, _name: String, text: String, _kind: String, _at: String,
        _structure_id: String, _mentions: Array, _speaker_x: float, _speaker_y: float, _room_id: String,
        _addressee_id: String, _addressee_name: String, _mention_prices: Dictionary, _huddle_scoped: bool,
        _recipient_ids: Array) -> void:
    say(npc_id, text)


## The person under a stage-local point (the nearest one in front wins), or "".
func person_at(local: Vector2) -> String:
    var art := local * _stretch / float(k) - _scene.position * _stretch / float(k)
    var best := ""
    var best_y := -INF
    for h in _hits:
        var holder: Node2D = h["holder"]
        if not is_instance_valid(holder):
            continue
        # Where they stand now: the keeper bustles, newcomers walk in.
        var r := Rect2(holder.position + HIT_BOX.position, HIT_BOX.size)
        if r.has_point(art) and r.end.y > best_y:
            best = str(h["id"])
            best_y = r.end.y
    return best


func _on_stage_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        stage.accept_event()
        var id := person_at(event.position)
        if id != "":
            person_clicked.emit(id)


func _on_backdrop_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        close()
        get_viewport().set_input_as_handled()


func _unhandled_input(event: InputEvent) -> void:
    if visible and event.is_action_pressed("ui_cancel"):
        close()
        get_viewport().set_input_as_handled()


## Put the room's pictures on: the backdrop, and for a stall the counter (the
## goods redraw from their own sheet). A picture still downloading leaves its
## layer blank until it lands.
func _apply_textures() -> void:
    _background.texture = _texture(str(room.get("sheet", "")))
    _counter.texture = _texture(str(room.get("counter", ""))) if is_stall() else null


## The goods on the stall's shelf changed (or their sheet arrived): lay them
## out again. Each is a cell of the goods sheet — a pile on the floor, or one
## or two of the good on the counter (StallScenes.lay_out).
func set_wares(wares: Array) -> void:
    _wares = wares
    _draw_goods()


func _draw_goods() -> void:
    for layer in [_goods_back, _goods_front]:
        for child in layer.get_children():
            child.free()
    if not is_stall():
        return
    var sheet := _texture(StallScenes.GOODS_SHEET)
    if sheet == null:
        return
    for slot in StallScenes.lay_out(_wares):
        var cell := AtlasTexture.new()
        cell.atlas = sheet
        cell.region = StallScenes.cell_rect(int(slot["cell"]))
        var layer: Node2D = _goods_back if int(slot["cell"]) % 2 == 1 else _goods_front
        for i in int(slot["count"]):
            var spr := Sprite2D.new()
            spr.centered = false
            spr.texture = cell
            spr.position = Vector2(slot["at"] + StallScenes.FEW_STEP * i)
            spr.name = "%s_%d" % [slot["item"], i]
            layer.add_child(spr)


## A sale settled (the world's pay_resolved, LLM-756). Over a stall's counter
## it is handed across, one handoff at a time (queued in _handoffs): when the
## one tending the stall sells, they walk along the counter to the buyer, set
## the good down in front of them when they get there, and after a moment it
## goes to the buyer; when they buy from someone at the counter, the good lies
## in front of the seller until they come and take it up. Only goods that can
## be drawn: a goods picture, or for a sale anything on the shelf (a service
## has neither). Trades between customers, and offers that fell through, pass
## the counter by. The shelf is re-read at once.
##
## A room with a bar (the Tavern, LLM-757) hands a sale across its bar the
## same way: the keeper steps to the bar's front and sets the good down, and
## it is served — to the table in front of a seated buyer, where it stays a
## while (a meal is eaten), or toward a standing buyer or the player's
## character at the door; a good the keeper buys lies on the bar until they
## take it.
func on_pay_resolved(data: Dictionary) -> void:
    if not visible or not (is_stall() or room.has("bar")) or str(data.get("terminal_state", "")) != "accepted":
        return
    var keeper := _tending()
    if keeper == null:
        return
    var keeper_id := str(keeper.name)
    var item := str(data.get("item", ""))
    var selling := str(data.get("seller_id", "")) == keeper_id
    if not selling and str(data.get("buyer_id", "")) != keeper_id:
        return
    if not (StallScenes.GOODS.has(item) or (selling and _on_shelf(item))):
        return
    if is_stall():
        _request_wares(true)
    # The goods sheet still downloading: nothing to hand over.
    if _texture(StallScenes.GOODS_SHEET) == null:
        return
    _handoffs.append({"item": item, "qty": int(data.get("qty", 1)), "selling": selling,
        "other": str(data.get("buyer_id", "")) if selling else str(data.get("seller_id", ""))})
    if _handoff_now.is_empty():
        _next_handoff()


## Start the next handoff in line; with none left the keeper bustles again.
func _next_handoff() -> void:
    _handoff_now = {}
    if _handoffs.is_empty() or not visible:
        _bustle_left = _bustle_rng.randf_range(BUSTLE_MIN, BUSTLE_MAX)
        return
    var h: Dictionary = _handoffs.pop_front()
    var keeper := _tending()
    var at: Vector2
    if is_stall():
        # Where along the counter: in front of the other party, or the middle
        # when they are not drawn.
        var size: Vector2i = room.get("size", StallScenes.SIZE)
        var x := float(StallScenes.KEEPER["x"])
        var customer := _people.get_node_or_null(NodePath(str(h["other"]))) as Node2D
        if customer != null:
            x = clampf(customer.position.x, 10.0, size.x - 10.0)
        at = Vector2(roundf(x), StallScenes.COUNTER_Y)
        h["stop"] = Vector2(at.x, (keeper.get_meta("home") as Vector2).y if keeper != null else 0.0)
        h["facing"] = "south"
    else:
        at = _spot_point(room["bar"])
        h["stop"] = _spot_point(room.get("reach", room["keeper"]))
        h["facing"] = str(room["keeper"].get("facing", "south"))
        h["serve"] = _serve_to(str(h["other"]), at)
    h["keeper"] = str(keeper.name) if keeper != null else ""
    var good := _handoff_good(str(h["item"]), int(h["qty"]), at)
    if good == null:
        _next_handoff()
        return
    # A good the keeper buys lies there from the start; one they sell
    # appears when they set it down.
    good.modulate.a = 0.0 if bool(h["selling"]) else 1.0
    h["good"] = good
    h["arrived"] = false
    _handoff_now = h
    _handoff_walk()


## A spot's {x, y} as a point.
func _spot_point(spot: Dictionary) -> Vector2:
    return Vector2(int(spot.get("x", 0)), int(spot.get("y", 0)))


## Where a sale on a room's bar is served to `buyer` (the good's bottom
## middle), and whether it stays there: the table in front of a seated buyer
## (it stays — a meal is eaten); most of the way toward a standing buyer or
## the player's character at the door (it fades as it goes). {} for a buyer
## not drawn: it stays on the bar a moment and goes.
func _serve_to(buyer: String, bar: Vector2) -> Dictionary:
    var spot: Dictionary = {}
    if buyer != "" and buyer == pc_actor_id:
        spot = room.get("door", {})
    elif _assigned.has(buyer):
        spot = room["spots"][int(_assigned[buyer])]
    if spot.is_empty():
        return {}
    if spot.has("serve"):
        return {"to": _spot_point(spot["serve"]), "stay": true}
    var person := _spot_point(spot) + Vector2(0, -6)
    return {"to": bar.lerp(person, 0.6).round(), "stay": false}


## The good for a handoff: its goods picture, one of it or two for more,
## standing at `at` (its bottom middle) — hidden until it is shown. Null
## without the goods sheet.
func _handoff_good(item: String, qty: int, at: Vector2) -> Node2D:
    var sheet := _texture(StallScenes.GOODS_SHEET)
    if sheet == null:
        return null
    var good := Node2D.new()
    good.name = "handoff_" + item
    good.position = _cell_at(at)
    var cell := AtlasTexture.new()
    cell.atlas = sheet
    cell.region = StallScenes.cell_rect(2 * StallScenes.goods_index(item))
    for i in (1 if qty <= 1 else 2):
        var spr := Sprite2D.new()
        spr.centered = false
        spr.texture = cell
        spr.position = Vector2(StallScenes.FEW_STEP * i)
        good.add_child(spr)
    _handoff.add_child(good)
    return good


## Send the keeper along the counter to the handoff in hand; there (at once
## when nobody is behind the counter) the good changes hands. A redraw of the
## keeper calls this again, so the walk resumes from where they had got to.
func _handoff_walk() -> void:
    var keeper := _tending()
    if keeper == null:
        _handoff_arrived()
        return
    if keeper.has_meta("walking"):
        (keeper.get_meta("walking") as Tween).kill()
        keeper.remove_meta("walking")
    _walk(keeper, _handoff_now["stop"], WALK_SPEED, str(_handoff_now["facing"]), "stand", _handoff_arrived)


## A good's cell position (top-left) for a good standing at `at` (its bottom
## middle).
func _cell_at(at: Vector2) -> Vector2:
    return Vector2(at.x - StallScenes.CELL.x / 2.0, at.y - StallScenes.CELL.y)


## The keeper is at the handoff: a good sold is set down and lies a moment;
## at a stall it then goes toward the buyer (down, out of the stall), in a
## room it is served (_serve_to) — a meal to the table, where it stays while
## the next handoff begins. A good bought is taken up toward the keeper. In a
## room the keeper then steps back to their spot.
func _handoff_arrived() -> void:
    if _handoff_now.is_empty() or bool(_handoff_now["arrived"]):
        return
    _handoff_now["arrived"] = true
    var good: Node2D = _handoff_now["good"]
    if not is_instance_valid(good):
        _next_handoff()
        return
    var selling := bool(_handoff_now["selling"])
    var t := good.create_tween()
    if selling:
        t.tween_property(good, "modulate:a", 1.0, 0.15)
        t.tween_interval(HANDOFF_HOLD)
    else:
        t.tween_interval(0.3)
    if not is_stall():
        t.tween_callback(_keeper_back.bind(str(_handoff_now["keeper"])))
    var serve: Dictionary = _handoff_now.get("serve", {})
    if selling and serve.has("to"):
        var to := _cell_at(serve["to"])
        var seconds := maxf(good.position.distance_to(to) / SERVE_SPEED, 0.2)
        t.tween_property(good, "position", to, seconds)
        if bool(serve["stay"]):
            # On the table: the counter is free for the next one meanwhile.
            t.tween_callback(_next_handoff)
            t.tween_interval(MEAL_SECONDS)
            t.tween_property(good, "modulate:a", 0.0, HANDOFF_AWAY)
            t.tween_callback(good.queue_free)
            good.set_meta("tween", t)
            return
        t.parallel().tween_property(good, "modulate:a", 0.0, seconds)
    elif selling and not is_stall():
        # Sold to someone not drawn: it fades where it lies on the bar.
        t.tween_property(good, "modulate:a", 0.0, HANDOFF_AWAY)
    else:
        var away := Vector2(0, HANDOFF_SLIDE) if selling else Vector2(0, -HANDOFF_SLIDE)
        if not is_stall():
            away = Vector2(HANDOFF_SLIDE, 0)
        t.tween_property(good, "position", good.position + away, HANDOFF_AWAY)
        t.parallel().tween_property(good, "modulate:a", 0.0, HANDOFF_AWAY)
    t.tween_callback(func():
        good.queue_free()
        _next_handoff())
    good.set_meta("tween", t)


## A room's keeper steps back to their spot after a handoff — the keeper
## who made it (`keeper_id`), redrawn or not; never someone who has taken
## their place since. A walk they are on is stopped for it.
func _keeper_back(keeper_id: String) -> void:
    var keeper := _tending()
    if keeper == null or str(keeper.name) != keeper_id:
        return
    if keeper.has_meta("walking"):
        (keeper.get_meta("walking") as Tween).kill()
        keeper.remove_meta("walking")
    var spot: Dictionary = room.get("keeper", {})
    _walk(keeper, keeper.get_meta("home"), WALK_SPEED, str(spot.get("facing", "south")), str(spot.get("pose", "stand")))


func _on_shelf(item: String) -> bool:
    for w in _wares:
        if str(w.get("item", "")) == item:
            return true
    return false


## Ask the engine what is on this stall's shelf. `fresh`: a sale just
## settled, so a request already under way (asked before the sale) is
## restarted rather than reused.
func _request_wares(fresh := false) -> void:
    var sid := structure_id
    if wares_hook.is_valid():
        set_wares(wares_hook.call(sid))
        return
    if _http_wares == null:
        _http_wares = HTTPRequest.new()
        add_child(_http_wares)
        _http_wares.request_completed.connect(_on_wares)
    elif _http_wares.get_http_client_status() != HTTPClient.STATUS_DISCONNECTED:
        # Already asking about this stall: that answer will do, unless a sale
        # has settled since. A request for the stall the window just left is
        # dropped for this one.
        if str(_http_wares.get_meta("structure_id", "")) == sid and not fresh:
            return
        _http_wares.cancel_request()
    _http_wares.set_meta("structure_id", sid)
    _http_wares.request(Auth.api_base + "/api/village/pc/stall?structure_id=" + sid.uri_encode(),
        Auth.auth_headers(false), HTTPClient.METHOD_GET)


func _on_wares(result: int, code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    # An answer for a stall the window has since left is dropped.
    if str(_http_wares.get_meta("structure_id", "")) != structure_id or not is_stall():
        return
    if result != HTTPRequest.RESULT_SUCCESS or code != 200:
        return
    var data = JSON.parse_string(body.get_string_from_utf8())
    if data is Dictionary and data.get("wares", null) is Array:
        set_wares(data["wares"])


## A picture by its served path: cached, else fetched from the server's
## tileset folder (null until it lands; _apply_textures puts it on then).
func _texture(path: String) -> Texture2D:
    if path == "":
        return null
    if texture_hook.is_valid():
        return texture_hook.call(path)
    if _textures.has(path):
        return _textures[path]
    _fetch(path)
    return null


func _fetch(path: String) -> void:
    if _fetching.has(path):
        return
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    _fetching[path] = http
    http.request_completed.connect(func(result: int, code: int, _h, body: PackedByteArray):
        http.queue_free()
        _fetching.erase(path)
        if result != HTTPRequest.RESULT_SUCCESS or code != 200:
            push_warning("Interior sheet download failed: %s code=%d" % [path, code])
            return
        var img := Image.new()
        if img.load_png_from_buffer(body) != OK:
            return
        _textures[path] = ImageTexture.create_from_image(img)
        if visible:
            _apply_textures()
            _draw_goods())
    if http.request(Auth.api_base + path) != OK:
        http.queue_free()
        _fetching.erase(path)
