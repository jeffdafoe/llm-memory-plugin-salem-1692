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
## A smithy's work area (LLM-764): now and then a farmer-base hand works the
## bellows, the forge fire flaring with each pull, and between customers a
## farmer-base keeper hammers at the anvil. Behind the counter, a walk between
## the work area and the selling side goes round the counter's end.
##
## PIXEL RULE (as repair_stage.gd): the room and the people are drawn in art
## pixels at a WHOLE number of screen pixels each, `k`; the window stretch is
## cancelled and the origin snapped to a whole screen pixel. People come from
## the world's own sprite builder, brought down from the village's draw scale
## to the room's.

signal opened
signal closed
signal person_clicked(npc_id: String)
## A tap on the floor or a free seat of a room the player's character is in
## (LLM-768): main.gd asks the engine to walk them there.
signal floor_tapped(square: Vector2i)

const FONT_PATH := "res://assets/fonts/IMFellEnglish-Regular.ttf"
const MAX_PIXEL_SCALE := 4
## Never drawn smaller than this (LLM-763): a room too big for the window at
## this scale scrolls instead — drag (or the wheel) to pan.
const MIN_PIXEL_SCALE := 2
## Screen px above a scrolling stage left unclipped, for speech bubbles over
## the back row.
const BUBBLE_HEADROOM := 200.0
## Screen px a press moves before it is a drag (a pan), not a tap — measured
## from the press, over the whole way moved.
const DRAG_START := 6.0
## Art px one wheel notch pans.
const WHEEL_STEP := 16.0
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
## A hand working beside the keeper (LLM-759) stays put and turns between the
## goods and the customers every BUSTLE_MIN..BUSTLE_MAX seconds; the keeper's
## bustle keeps HAND_CLEAR art px from a hand's spot.
const HAND_CLEAR := 20.0
## A smithy's work area (LLM-764). When a hand would turn, BELLOWS_CHANCE of
## the time a farmer-base hand walks to the bellows instead and pulls for
## PULL_MIN..PULL_MAX seconds, one hand at a time; each pull down flares the
## fire, a glow that fades over FLARE_SECONDS. When the keeper would bustle,
## SMITH_CHANCE of the time a farmer-base keeper walks to the anvil and
## hammers for SMITH_MIN..SMITH_MAX seconds. A sale or speech brings the
## keeper back; speech stops a hand's pull.
const BELLOWS_CHANCE := 0.3
const PULL_MIN := 3.0
const PULL_MAX := 6.0
const FLARE_SECONDS := 0.7
const FLARE_ALPHA := 0.6
const FLARE_INNER := Color(1.0, 0.62, 0.22, 1.0)
const FLARE_OUTER := Color(1.0, 0.36, 0.08, 0.0)
const SMITH_CHANCE := 0.25
const SMITH_MIN := 4.0
const SMITH_MAX := 8.0
## Behind the counter, a walk between the work area and the selling side goes
## round the counter's end, GATE_IN art px short of it.
const GATE_IN := 6.0
## A sale over a stall's counter (LLM-756): the good lies on the counter
## HANDOFF_HOLD seconds, then goes to its new owner over HANDOFF_AWAY seconds,
## sliding HANDOFF_SLIDE art px their way as it fades.
const HANDOFF_HOLD := 2.0
const HANDOFF_AWAY := 0.6
const HANDOFF_SLIDE := 14.0
## In a room (LLM-757) a sale is served from the bar at SERVE_SPEED art px a
## second. A good eaten at the table stays there while the meal runs
## (world.meals, LLM-758); a good with no meal that takes time (ale, bread)
## stays TABLE_SECONDS. Then the keeper clears it.
const SERVE_SPEED := 48.0
const TABLE_SECONDS := 30.0

## A name tag (LLM-761): hovering a person shows their name and role (the
## village popup's colours), hovering a good its name; a tap shows it
## TAG_SECONDS. A good's hit box is the lower GOOD_BOX of its cell.
const TAG_SECONDS := 2.5
const GOOD_BOX := Rect2(0, 12, 16, 20)
const TAG_BG := Color(0.10, 0.08, 0.06, 0.92)
const TAG_BORDER := Color(0.45, 0.35, 0.22, 0.8)
const TAG_TEXT := Color(0.85, 0.75, 0.55, 1.0)
const TAG_TEXT_DIM := Color(0.63, 0.56, 0.44, 1.0)

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
## Holds the scene; clips it to the stage only while it scrolls.
var _clip: Control = null
## The part of the room shown, in art px: its top-left (`_pan`) and size
## (`_view`, the room's size when it all fits). `_centre_pan` centres the
## view on the door at the next layout (set on open).
var _pan := Vector2.ZERO
var _view := Vector2.ZERO
var _centre_pan := false
## A press on a scrolling stage, waiting to be a tap or a drag: {at, drag}.
var _press: Dictionary = {}
var _background: Sprite2D = null
## The glow over a smithy's forge fire, and how bright it is now (0..1).
var _fire: Sprite2D = null
var _flare := 0.0
## Stall layers (empty for a room): floor piles, the keeper, the counter, the
## goods on it. A room's people all go in _people, as do a stall's customers.
var _goods_back: Node2D = null
var _staff: Node2D = null
var _counter: Sprite2D = null
var _goods_front: Node2D = null
## A room's animated pieces (InteriorRooms `anims`), over the room and its
## counter, under the people; `_anim_time` runs while the window is open.
var _anims: Node2D = null
var _anim_time := 0.0
## Goods changing hands over the counter, above the goods on it — and in a
## room the dishes on the tables (meta "buyer" and "item").
var _handoff: Node2D = null
## Handoffs (and in a room, dishes to clear: {"clear": dish}) waiting their
## turn, and the one under way ({} when none).
var _handoffs: Array = []
var _handoff_now: Dictionary = {}
var _people: Node2D = null
var _font: Font = null
var _stretch := Vector2.ONE
## A stall's customers' spots, sticky (InteriorRooms.assign).
var _assigned: Dictionary = {}
## In a room, where each drawn person stands this draw (id -> spot), from
## their engine square — the handoffs serve to it.
var _spot_of: Dictionary = {}
## A stall's hands' spots beside the keeper, sticky like _assigned.
var _assigned_hands: Dictionary = {}
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
## The name tag and what it is on: {} when hidden, else {kind: "person" | "good",
## id (a person), node (a good)}. _tag_left > 0 while a tap's tag is up.
var tag: PanelContainer = null
var tag_name: Label = null
var tag_role: Label = null
var _tag_on: Dictionary = {}
var _tag_left := 0.0


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
    stage.mouse_exited.connect(_on_stage_mouse_exited)
    stage.item_rect_changed.connect(_snap)
    vb.add_child(stage)

    _clip = Control.new()
    _clip.name = "Clip"
    _clip.mouse_filter = Control.MOUSE_FILTER_IGNORE
    stage.add_child(_clip)
    _scene = Node2D.new()
    _clip.add_child(_scene)
    _background = Sprite2D.new()
    _background.centered = false
    _scene.add_child(_background)
    _fire = Sprite2D.new()
    _fire.name = "Fire"
    _fire.centered = false
    _fire.texture_filter = CanvasItem.TEXTURE_FILTER_NEAREST
    var add := CanvasItemMaterial.new()
    add.blend_mode = CanvasItemMaterial.BLEND_MODE_ADD
    _fire.material = add
    _fire.visible = false
    _scene.add_child(_fire)
    _goods_back = Node2D.new()
    _scene.add_child(_goods_back)
    _staff = Node2D.new()
    # The keeper at a smithy's anvil stands in front of a hand at the forge.
    _staff.y_sort_enabled = true
    _scene.add_child(_staff)
    _counter = Sprite2D.new()
    _counter.centered = false
    _scene.add_child(_counter)
    _anims = Node2D.new()
    _scene.add_child(_anims)
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

    # Over everything, never in the way of a click.
    tag = PanelContainer.new()
    tag.mouse_filter = Control.MOUSE_FILTER_IGNORE
    tag.visible = false
    var style := StyleBoxFlat.new()
    style.bg_color = TAG_BG
    style.set_border_width_all(1)
    style.border_color = TAG_BORDER
    style.set_corner_radius_all(4)
    style.content_margin_left = 8.0
    style.content_margin_right = 8.0
    style.content_margin_top = 4.0
    style.content_margin_bottom = 4.0
    tag.add_theme_stylebox_override("panel", style)
    var tag_box := VBoxContainer.new()
    tag_box.add_theme_constant_override("separation", 0)
    tag_box.mouse_filter = Control.MOUSE_FILTER_IGNORE
    tag.add_child(tag_box)
    tag_name = _label(15, TAG_TEXT)
    tag_box.add_child(tag_name)
    tag_role = _label(12, TAG_TEXT_DIM)
    tag_box.add_child(tag_role)
    root.add_child(tag)


func _label(font_size: int, color: Color) -> Label:
    var l := Label.new()
    l.add_theme_font_override("font", _font)
    l.add_theme_font_size_override("font_size", font_size)
    l.add_theme_color_override("font_color", color)
    return l


## Show the room — or the stall — of `sid`. Anything else is a no-op.
func open(sid: String) -> void:
    var r := InteriorRooms.room_for(sid)
    if not r.is_empty():
        r = InteriorRooms.with_layout(r, Catalog.rooms.get(sid, {}))
    if r.is_empty():
        r = StallScenes.scene_for(world, Catalog.assets, sid)
    if r.is_empty():
        return
    if world != null and world.has_signal("npc_spoke") and not world.npc_spoke.is_connected(_on_npc_spoke):
        world.npc_spoke.connect(_on_npc_spoke)
    if world != null and world.has_signal("pay_resolved") and not world.pay_resolved.is_connected(on_pay_resolved):
        world.pay_resolved.connect(on_pay_resolved)
    if world != null and world.has_signal("npc_meal_started") and not world.npc_meal_started.is_connected(_on_meal_started):
        world.npc_meal_started.connect(_on_meal_started)
    if world != null and world.has_signal("npc_meal_ended") and not world.npc_meal_ended.is_connected(_on_meal_ended):
        world.npc_meal_ended.connect(_on_meal_ended)
    var was_open := visible
    hide_tag()
    for child in _handoff.get_children():
        # Its tween is bound to it and dies with it; stopped first all the same.
        if child.has_meta("tween"):
            (child.get_meta("tween") as Tween).kill()
        child.free()
    _handoffs = []
    _handoff_now = {}
    if sid != structure_id:
        _assigned = {}
        _assigned_hands = {}
        _spot_of = {}
        _wares = []
    structure_id = sid
    room = r
    # Goods changing hands: at a stall under the customers in front of the
    # counter; in a room over everyone, as they lie on the bar and tables,
    # which now sort among the people (LLM-767).
    _scene.move_child(_handoff, -1)
    if is_stall():
        _scene.move_child(_people, -1)
    _press = {}
    _centre_pan = true
    title_label.text = str(room.get("title", ""))
    _apply_textures()
    _lay_fire()
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
    _lay_running_meals()
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
    hide_tag()
    set_process(false)
    closed.emit()


func is_open() -> bool:
    return visible


func _process(delta: float) -> void:
    _refresh_left -= delta
    if _refresh_left <= 0.0:
        _refresh_left = REFRESH_SECONDS
        refresh()
    _update_tag(delta)
    _animate(delta)
    if is_stall():
        _wares_left -= delta
        if _wares_left <= 0.0:
            _wares_left = WARES_SECONDS
            _request_wares()
        _bustle(delta)
        _hands_turn(delta)
        _flare = maxf(0.0, _flare - delta / FLARE_SECONDS)
        _fire.modulate.a = _flare * FLARE_ALPHA


## A smithy's forge fire gets its glow: drawn in code, an art-pixel radial
## gradient over the fire's rect, added onto it, dark until a pull flares it.
func _lay_fire() -> void:
    _flare = 0.0
    _fire.modulate.a = 0.0
    var rect = room.get("fire", null)
    _fire.visible = rect is Rect2i
    if not _fire.visible:
        return
    var gradient := Gradient.new()
    gradient.set_color(0, FLARE_INNER)
    gradient.set_color(1, FLARE_OUTER)
    var glow := GradientTexture2D.new()
    glow.gradient = gradient
    glow.width = rect.size.x
    glow.height = rect.size.y
    glow.fill = GradientTexture2D.FILL_RADIAL
    # Brightest low in the coals, fading out to the rect's edge.
    glow.fill_from = Vector2(0.5, 0.65)
    glow.fill_to = Vector2(1.0, 0.65)
    _fire.texture = glow
    _fire.position = Vector2(rect.position)


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
## plus the "more" count, from the world's containers. In a room each person
## stands where the engine put them — their `room_square` meta, on the room's
## floor plan (LLM-768); someone with no square (every square taken, or the
## plan not loaded yet) is counted, not drawn. Pure apart from the spot memory
## (`_spot_of`, read by the handoffs), so tests drive it directly.
func occupants() -> Dictionary:
    if is_stall():
        return _stall_occupants()
    # The plan may land after the window opened.
    if not room.has("door") and Catalog.rooms.has(structure_id):
        room = InteriorRooms.with_layout(room, Catalog.rooms[structure_id])
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
    # The keeper first, the player's character last, as they were drawn
    # before (the people's layer sorts by depth all the same).
    var order: Array = ([keeper_id] if keeper_id != "" else []) + others + ([pc_actor_id] if pc_inside else [])
    _spot_of = {}
    var placed: Array = []
    var more := 0
    for id in order:
        var c: Node2D = world.placed_npcs[id]
        var spot := InteriorRooms.spot_at(room, InteriorRooms.square_of(c.get_meta("room_square") if c.has_meta("room_square") else null))
        if spot.is_empty():
            more += 1
            continue
        _spot_of[id] = spot
        var e := _entry(id, spot)
        if id == keeper_id:
            e["keeper"] = true
        placed.append(e)
    return {"placed": placed, "more": more}


## A stall's people: behind the counter, whoever is inside the stall — its
## own keeper in the middle (else a hand tending it), and up to two more
## working beside them at the hands' spots (anyone past those is counted, not
## drawn); in front, whoever stands at its loiter pin, the player's character
## in the middle.
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
    var hand_spots: Array = room.get("hands", [])
    var hands := InteriorRooms.assign(hand_spots.size(), inside, _assigned_hands)
    _assigned_hands = hands["spots"]
    var spots: Array = room.get("spots", [])
    var stood := InteriorRooms.assign(spots.size(), customers, _assigned)
    _assigned = stood["spots"]
    var placed: Array = []
    if keeper_id != "":
        var e := _entry(keeper_id, room.get("keeper", {}))
        e["behind"] = true
        e["keeper"] = true
        placed.append(e)
    for id in inside:
        if _assigned_hands.has(id):
            var e := _entry(id, hand_spots[int(_assigned_hands[id])])
            e["behind"] = true
            e["hand"] = true
            placed.append(e)
    for id in customers:
        if _assigned.has(id):
            placed.append(_entry(id, spots[int(_assigned[id])]))
    if pc_here:
        placed.append(_entry(pc_actor_id, room.get("door", {})))
    return {"placed": placed, "more": int(stood["more"]) + int(hands["more"])}


## One drawn person. `look` stamps the outfit and the rest state so a change
## to either redraws the room.
func _entry(id: String, spot: Dictionary) -> Dictionary:
    var c: Node2D = world.placed_npcs[id]
    var look := "%d|%s" % [hash(JSON.stringify(c.get_meta("sprite_data", {}))), str(c.get_meta("dormant", ""))]
    return {"id": id, "spot": spot, "look": look, "walk": int(c.get_meta("room_walk", 0))}


func _draw_people(cast: Dictionary) -> void:
    # A bubble outlives the redraw (someone came in mid-sentence): lift it off
    # the old person and put it back on the new one below.
    var bubbles := {}
    # Who was drawn where, so a leaver walks off from there, a newcomer cut
    # short carries on walking in, and a stall's keeper carries on from
    # wherever they had bustled to.
    var was := {}
    for child in _people.get_children() + _staff.get_children():
        # A room's furniture shares the people's layer and stays.
        if child.has_meta("piece"):
            continue
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
            "keeper": child.has_meta("keeper"),
            "sprite_data": child.get_meta("sprite_data", {}), "hand": child.has_meta("hand"),
            "turn_left": child.get_meta("turn_left") if child.has_meta("turn_left") else null,
            "facing": child.get_meta("facing") if child.has_meta("facing") else null,
            "smithing": child.get_meta("smithing") if child.has_meta("smithing") else null,
            "anvil_trip": child.has_meta("anvil_trip"), "walk": int(child.get_meta("walk", 0)),
            "route_left": child.get_meta("route_left") if child.has_meta("route_left") else null}
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
        var hand := bool(entry.get("hand", false))
        if hand:
            holder.set_meta("hand", true)
        holder.add_child(spr)
        # A window just opened draws everyone at their spots: nothing carries
        # over from the last time it was open.
        var stays := _entrances and was.has(id) and bool(was[id]["behind"]) == behind
        # Behind a stall's counter, a hand who now keeps the stall (the keeper
        # left) or a keeper who is now a hand (the keeper came back) walks from
        # where they were to their new spot.
        var moves := stays and behind and bool(was[id]["hand"]) != hand and not bool(was[id]["entering"])
        # A hand caught away from their spot (at a smithy's bellows) walks back.
        var back := stays and hand and bool(was[id]["hand"]) and not bool(was[id]["entering"]) \
            and (was[id]["position"] as Vector2).distance_to(home) > 0.5
        # In a room, someone the engine walked to another square (a player's
        # tap) walks the path square by square; one redrawn mid-walk carries on.
        var walk := int(entry.get("walk", 0))
        holder.set_meta("walk", walk)
        var room_route: Array = []
        if stays and not is_stall() and not bool(was[id]["entering"]):
            if walk != int(was[id]["walk"]) and c.has_meta("room_path"):
                room_route = InteriorRooms.route(room, c.get_meta("room_path"))
            elif was[id]["route_left"] is Array and not was[id]["route_left"].is_empty() \
                    and (was[id]["route_left"].back() as Vector2).distance_to(home) <= 0.5:
                room_route = was[id]["route_left"]
        # The keeper carries on from where they bustled to, a newcomer from
        # partway in, a walker from where they had got to; anyone else (a hand
        # too) stands at their (maybe new) spot.
        if stays and ((behind and not hand) or bool(was[id]["entering"]) or moves or back or not room_route.is_empty()):
            holder.position = was[id]["position"]
            holder.modulate.a = was[id]["alpha"]
        # A stall's keeper stands behind the counter, which is drawn over them.
        (_staff if behind else _people).add_child(holder)
        var facing := str(spot.get("facing", "south"))
        # A hand still a hand keeps the way they were looking and their wait.
        if hand and stays and bool(was[id]["hand"]):
            if was[id]["facing"] != null:
                facing = str(was[id]["facing"])
                holder.set_meta("facing", facing)
            if was[id]["turn_left"] != null:
                holder.set_meta("turn_left", was[id]["turn_left"])
        _pose(spr, facing, str(spot.get("pose", "stand")))
        # The keeper hammering at the anvil keeps at it; one on the way there
        # carries on.
        if stays and behind and not hand and not moves and was[id]["smithing"] != null:
            _start_smith(holder, float(was[id]["smithing"]))
        elif stays and behind and not hand and not moves and bool(was[id]["anvil_trip"]):
            _to_anvil(holder)
        if str(c.get_meta("dormant", "")) == "resting":
            spr.modulate = RESTING_DIM
        if _entrances and not stays:
            _walk_in(holder, spot, true)
        elif stays and bool(was[id]["entering"]):
            _walk_in(holder, spot, false)
        elif not room_route.is_empty():
            _walk_along(holder, room_route, str(spot.get("facing", "south")), str(spot.get("pose", "stand")))
        elif moves:
            _walk(holder, home, WALK_SPEED, str(spot.get("facing", "south")))
        elif back:
            _walk(holder, home, WALK_SPEED, facing)
        _hits.append({"id": id, "holder": holder})
        drawn[id] = true
    if _entrances:
        for id in was:
            if not drawn.has(id):
                _walk_out(id, was[id])
    # Someone who left their table leaves their dish to be cleared.
    for dish in _dishes():
        if not drawn.has(str(dish.get_meta("buyer"))):
            _clear_dish(dish)
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
## (the keeper behind the counter too), a room's door — or, for a room's
## keeper, the room's `keeper_door` when it has one (the Inn's stairs).
func _doorway(at: Vector2, keeper := false) -> Vector2:
    if is_stall() or not room.has("door"):
        var size: Vector2i = room.get("size", Vector2i(128, 112))
        var middle := (float(room.get("counter_from", 0)) + size.x) / 2.0
        return at + Vector2(-WALK_OFF if at.x < middle else WALK_OFF, 0)
    var door := _spot_point(room["keeper_door"] if keeper and room.has("keeper_door") else room["door"])
    return door + Vector2(0, 8) if at.distance_to(door) < 4.0 else door


## A newcomer walks in from the doorway to their spot, fading in — from the
## doorway, or (`from_doorway` false) on from wherever a redraw caught them.
## The holder carries an "entering" meta until they are there.
func _walk_in(holder: Node2D, spot: Dictionary, from_doorway: bool) -> void:
    var home: Vector2 = holder.get_meta("home")
    if from_doorway:
        holder.position = _doorway(home, holder.has_meta("keeper"))
        holder.modulate.a = 0.0
    holder.set_meta("entering", true)
    _walk(holder, home, WALK_SPEED, str(spot.get("facing", "south")), str(spot.get("pose", "stand")),
        func(): holder.remove_meta("entering"), 1.0, 0.0, FADE_SECONDS * (1.0 - holder.modulate.a))


## Walk a drawn person through `points` (room art px; the last is where they
## stop) — a path the engine walked them in a room (LLM-768) — taking `facing`
## and `pose` at the end.
func _walk_along(holder: Node2D, points: Array, facing: String, pose: String) -> void:
    holder.set_meta("via", points.slice(0, points.size() - 1))
    _walk(holder, points.back(), WALK_SPEED, facing, pose)


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
    var to := _doorway(ghost.position, bool(was["keeper"]))
    var seconds := ghost.position.distance_to(to) / WALK_SPEED
    _walk(ghost, to, WALK_SPEED, "south", "stand", ghost.queue_free,
        0.0, maxf(0.0, seconds - FADE_SECONDS), minf(FADE_SECONDS, seconds))


## Walk a drawn person to `to` at `speed` art px a second, walking the way
## they go; at the end they take `facing` (and `pose`) and `done` runs. With
## `fade_to` >= 0 they also fade to that alpha over `fade_seconds`, starting
## `fade_delay` into the walk — one tween, so the walk and the fade end
## together. The holder carries a "walking" meta (the tween) while under way.
## Behind the counter the walk may go round its end first (_route).
func _walk(holder: Node2D, to: Vector2, speed: float, facing: String, pose := "stand", done := Callable(),
        fade_to := -1.0, fade_delay := 0.0, fade_seconds := 0.0) -> void:
    var spr := _sprite_of(holder)
    var tween := holder.create_tween()
    holder.set_meta("walking", tween)
    var at := holder.position
    var first := true
    var points: Array = _route(holder, to) if fade_to < 0.0 else [to]
    # The points still ahead, so a redraw mid-walk carries on from there.
    holder.set_meta("route_left", points.duplicate())
    for point in points:
        var dist := at.distance_to(point)
        if spr != null and dist > 0.5 and spr.sprite_frames != null:
            var anim := str(world.facing_from_vec(point - at)) + "_walk"
            if spr.sprite_frames.has_animation(anim):
                # The first leg turns them at once; a later one as it starts.
                if first:
                    spr.play(anim)
                else:
                    tween.tween_callback(spr.play.bind(anim))
        tween.tween_property(holder, "position", point, maxf(dist / speed, 0.01))
        # (Not on a fading walk: its fade runs alongside the last leg.)
        if fade_to < 0.0:
            tween.tween_callback(func(): (holder.get_meta("route_left", []) as Array).pop_front())
        at = point
        first = false
    if fade_to >= 0.0:
        tween.parallel().tween_property(holder, "modulate:a", fade_to, maxf(fade_seconds, 0.01)).set_delay(fade_delay)
    tween.tween_callback(func():
        holder.remove_meta("walking")
        holder.remove_meta("route_left")
        if spr != null:
            _pose(spr, facing, pose)
        if done.is_valid():
            done.call())


## The way a drawn person walks to `to`: through the points a room walk laid
## (_walk_along), straight there, or for someone behind
## a counter that has a work area left of it (a smithy) and a walk from one
## side of the counter's end to the other, round the end first — never
## through the counter.
func _route(holder: Node2D, to: Vector2) -> Array:
    if holder.has_meta("via"):
        var via: Array = holder.get_meta("via")
        holder.remove_meta("via")
        return via + [to]
    var end := float(room.get("counter_from", 0))
    if holder.get_parent() != _staff or end <= 0.0 or (holder.position.x < end) == (to.x < end):
        return [to]
    var row := float(room.get("keeper", {}).get("y", to.y))
    return [Vector2(end - GATE_IN, row), to]


## The one tending a stall bustles behind the counter: now and then a walk to
## another point along it (or back to their spot), then a look at the goods
## behind them or at the customers. At a smithy, now and then a farmer-base
## keeper goes to hammer at the anvil instead.
func _bustle(delta: float) -> void:
    var holder := _tending()
    if holder == null or not _handoff_now.is_empty():
        return
    # On the way to the anvil or at it: a customer at the counter calls them
    # back.
    if holder.has_meta("anvil_trip") or holder.has_meta("smithing"):
        if _customers_waiting():
            _back_to_counter(holder)
        elif holder.has_meta("smithing"):
            _hammer(holder, delta)
        return
    if holder.has_meta("walking"):
        return
    _bustle_left -= delta
    if _bustle_left > 0.0:
        return
    _bustle_left = _bustle_rng.randf_range(BUSTLE_MIN, BUSTLE_MAX)
    var anvil: Dictionary = room.get("anvil", {})
    if _can_work(holder, anvil, "smith") and not _customers_waiting() and _bustle_rng.randf() < SMITH_CHANCE:
        _to_anvil(holder)
        return
    var home: Vector2 = holder.get_meta("home")
    var to := home
    if holder.position.distance_to(home) < 2.0 or _bustle_rng.randf() < 0.5:
        # Along the counter, but never into a hand working beside them.
        var lo := home.x - BUSTLE_REACH
        var hi := home.x + BUSTLE_REACH
        for hand in _hands():
            var x: float = (hand.get_meta("home") as Vector2).x
            if x < home.x:
                lo = maxf(lo, x + HAND_CLEAR)
            else:
                hi = minf(hi, x - HAND_CLEAR)
        to = Vector2(roundf(_bustle_rng.randf_range(lo, maxf(lo, hi))), home.y)
    var facing := "south" if to == home else ("north" if _bustle_rng.randf() < 0.5 else "south")
    _walk(holder, to, BUSTLE_SPEED, facing)


## The one tending: a room's keeper (behind its desk, or not), or in a stall
## the keeper behind the counter (never a hand beside them); null when there
## is none. _draw_people marks them.
func _tending() -> Node2D:
    for child in _staff.get_children() + ([] if is_stall() else _people.get_children()):
        if child.has_meta("keeper") and not child.has_meta("leaving"):
            return child
    return null


## The hands working beside a stall's keeper.
func _hands() -> Array:
    var out: Array = []
    for child in _staff.get_children():
        if child.has_meta("hand") and not child.has_meta("leaving"):
            out.append(child)
    return out


## Each hand now and then turns to the goods behind them or to the customers.
## Each keeps their own wait (meta "turn_left"); one walking in waits. At a
## smithy a farmer-base hand now and then works the bellows instead (one at a
## time, meta "bellows" from setting off until back at their spot); one left
## away from their spot (speech stopped their pull) walks back at their turn.
func _hands_turn(delta: float) -> void:
    var bellows: Dictionary = room.get("bellows", {})
    for hand in _hands():
        if hand.has_meta("walking"):
            continue
        if hand.has_meta("pulling"):
            _pull(hand, delta)
            continue
        if not hand.has_meta("turn_left"):
            hand.set_meta("turn_left", _bustle_rng.randf_range(BUSTLE_MIN, BUSTLE_MAX))
        var left := float(hand.get_meta("turn_left")) - delta
        if left <= 0.0:
            left = _bustle_rng.randf_range(BUSTLE_MIN, BUSTLE_MAX)
            var home: Vector2 = hand.get_meta("home")
            if hand.position.distance_to(home) <= 0.5 and _can_work(hand, bellows, "pull") \
                    and not _bellows_taken() and _bustle_rng.randf() < BELLOWS_CHANCE:
                hand.set_meta("bellows", true)
                _walk(hand, _spot_point(bellows), WALK_SPEED, str(bellows.get("facing", "north")), "stand",
                    func(): _start_pull(hand))
                hand.set_meta("turn_left", left)
                continue
            var facing := "north" if _bustle_rng.randf() < 0.5 else "south"
            hand.set_meta("facing", facing)
            hand.remove_meta("bellows")
            if hand.position.distance_to(home) > 0.5:
                _walk(hand, home, WALK_SPEED, facing)
            else:
                var spr := _sprite_of(hand)
                if spr != null:
                    _pose(spr, facing, "stand")
        hand.set_meta("turn_left", left)


## Whether `who` can do `work` ("pull", "smith") at a work spot of the scene:
## it has one, and their sprite has the animation for its facing — a
## farmer-base sprite does, a one-sheet pack sprite does not.
func _can_work(who: Node2D, spot: Dictionary, work: String) -> bool:
    if spot.is_empty():
        return false
    var spr := _sprite_of(who)
    return spr != null and spr.sprite_frames != null \
        and spr.sprite_frames.has_animation(str(spot.get("facing", "south")) + "_" + work)


## Whether anyone but the player's own character stands at the counter: the
## keeper goes to the anvil only between customers.
func _customers_waiting() -> bool:
    for child in _people.get_children():
        if not child.has_meta("leaving") and str(child.name) != pc_actor_id:
            return true
    return false


## Whether a hand is at the bellows or on the way.
func _bellows_taken() -> bool:
    for hand in _hands():
        if hand.has_meta("bellows"):
            return true
    return false


## At the bellows: pull for a few seconds (meta "pulling", seconds left).
func _start_pull(hand: Node2D) -> void:
    var spr := _sprite_of(hand)
    if spr == null:
        return
    hand.set_meta("pulling", _bustle_rng.randf_range(PULL_MIN, PULL_MAX))
    hand.set_meta("pull_frame", -1)
    spr.play(str(room.get("bellows", {}).get("facing", "north")) + "_pull")


## Pulling: each pull down (the animation's second frame) flares the fire;
## time up, back to their spot.
func _pull(hand: Node2D, delta: float) -> void:
    var spr := _sprite_of(hand)
    if spr != null and str(spr.animation).ends_with("_pull"):
        if spr.frame == 1 and int(hand.get_meta("pull_frame", -1)) != 1:
            _flare = 1.0
        hand.set_meta("pull_frame", spr.frame)
    var left := float(hand.get_meta("pulling")) - delta
    if left > 0.0:
        hand.set_meta("pulling", left)
        return
    _stop_pull(hand)
    hand.set_meta("facing", "south")
    _walk(hand, hand.get_meta("home"), WALK_SPEED, "south")


## Leave the bellows (or the way there) where they stand.
func _stop_pull(hand: Node2D) -> void:
    if hand.has_meta("walking"):
        (hand.get_meta("walking") as Tween).kill()
        hand.remove_meta("walking")
    for key in ["pulling", "pull_frame", "bellows"]:
        hand.remove_meta(key)


## The keeper sets off for the anvil (meta "anvil_trip" until they get there).
func _to_anvil(keeper: Node2D) -> void:
    var anvil: Dictionary = room.get("anvil", {})
    keeper.set_meta("anvil_trip", true)
    _walk(keeper, _spot_point(anvil), WALK_SPEED, str(anvil.get("facing", "west")), "stand",
        func(): _start_smith(keeper, _bustle_rng.randf_range(SMITH_MIN, SMITH_MAX)))


## At the anvil: hammer for `seconds` (meta "smithing", seconds left).
func _start_smith(keeper: Node2D, seconds: float) -> void:
    keeper.remove_meta("anvil_trip")
    var anvil: Dictionary = room.get("anvil", {})
    var spr := _sprite_of(keeper)
    var anim := str(anvil.get("facing", "west")) + "_smith"
    if spr == null or spr.sprite_frames == null or not spr.sprite_frames.has_animation(anim):
        return
    keeper.set_meta("smithing", seconds)
    spr.play(anim)


## Hammering: time up, back to their spot behind the counter.
func _hammer(keeper: Node2D, delta: float) -> void:
    var left := float(keeper.get_meta("smithing")) - delta
    if left > 0.0:
        keeper.set_meta("smithing", left)
        return
    _back_to_counter(keeper)


## The keeper leaves the anvil (or the way there, or the work area) for their
## spot behind the counter, facing the customers.
func _back_to_counter(keeper: Node2D) -> void:
    if keeper.has_meta("walking"):
        (keeper.get_meta("walking") as Tween).kill()
        keeper.remove_meta("walking")
    keeper.remove_meta("smithing")
    keeper.remove_meta("anvil_trip")
    _walk(keeper, keeper.get_meta("home"), WALK_SPEED, "south")


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
    var size := Vector2(room.get("size", Vector2i(128, 112)))
    var avail := get_viewport().get_visible_rect().size * _stretch * Vector2(0.92, 0.72)
    k = maxi(pixel_scale_for(avail, size), MIN_PIXEL_SCALE)
    _view = view_for(avail, size, k)
    var px := Vector2(k / _stretch.x, k / _stretch.y)
    stage.custom_minimum_size = _view * px
    # Clipped only while it scrolls, and then with room above for bubbles
    # over the back row; otherwise nothing clips, as before.
    var headroom := BUBBLE_HEADROOM / _stretch.y if scrolls() else 0.0
    _clip.clip_contents = scrolls()
    _clip.position = Vector2(0, -headroom)
    _clip.size = _view * px + Vector2(0, headroom)
    _scene.scale = px
    if _centre_pan:
        _centre_pan = false
        var door: Dictionary = room.get("door", {})
        _pan = Vector2(int(door.get("x", size.x / 2.0)), int(door.get("y", size.y / 2.0))) - _view / 2.0
    _set_pan(_pan)


## The biggest whole scale at which the room fits, 1..MAX_PIXEL_SCALE.
static func pixel_scale_for(avail_px: Vector2, art: Vector2) -> int:
    if art.x <= 0 or art.y <= 0:
        return 1
    return clampi(mini(floori(avail_px.x / art.x), floori(avail_px.y / art.y)), 1, MAX_PIXEL_SCALE)


## How much of an `art`-sized room shows at `scale` in `avail_px`: all of it,
## or on a side too big, whole art px as far as the window reaches.
static func view_for(avail_px: Vector2, art: Vector2, scale: int) -> Vector2:
    return Vector2(minf(art.x, maxf(1.0, floorf(avail_px.x / scale))), minf(art.y, maxf(1.0, floorf(avail_px.y / scale))))


## Whether the room is bigger than the part shown.
func scrolls() -> bool:
    var size := Vector2(room.get("size", Vector2i(128, 112)))
    return _view.x > 0.0 and (_view.x < size.x or _view.y < size.y)


## Show the room from `pan` (art px), kept inside the room.
func _set_pan(pan: Vector2) -> void:
    var size := Vector2(room.get("size", Vector2i(128, 112)))
    _pan = Vector2(clampf(roundf(pan.x), 0.0, maxf(0.0, size.x - _view.x)),
        clampf(roundf(pan.y), 0.0, maxf(0.0, size.y - _view.y)))
    _snap()


## Put the room's origin on a whole screen pixel so every art pixel is
## exactly k screen pixels.
func _snap() -> void:
    if stage == null or not stage.is_inside_tree():
        return
    var base := Vector2(0, -_clip.position.y) - _pan * Vector2(k / _stretch.x, k / _stretch.y)
    _scene.position = base
    var screen_origin := _scene.get_global_transform_with_canvas().origin * _stretch
    _scene.position = base + (screen_origin.round() - screen_origin) / _stretch


## A stage-local point in room art px.
func _art_at(local: Vector2) -> Vector2:
    return (local - _clip.position - _scene.position) * _stretch / float(k)


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
    # A hand turns to the customers to speak and holds it a while — once
    # there, if still walking in. Speaking stops a pull at the bellows (or the
    # way there) where they stand; they walk back at their next turn.
    if holder.has_meta("hand"):
        if holder.has_meta("bellows"):
            _stop_pull(holder)
            holder.set_meta("bellows", true)
        if not holder.has_meta("walking"):
            holder.set_meta("facing", "south")
            var hand_spr := _sprite_of(holder)
            if hand_spr != null:
                _pose(hand_spr, "south", "stand")
            holder.set_meta("turn_left", SPEAK_HOLD)
    # The one tending a stall stops to speak, facing the customers — but not
    # in the middle of handing something over. From a smithy's anvil (or on
    # the way there) they come back to the counter to speak.
    elif is_stall() and holder.get_parent() == _staff and holder.has_meta("keeper") and not holder.has_meta("leaving") and _handoff_now.is_empty():
        if holder.has_meta("walking"):
            (holder.get_meta("walking") as Tween).kill()
            holder.remove_meta("walking")
        if holder.has_meta("smithing") or holder.has_meta("anvil_trip") \
                or holder.position.x < float(room.get("counter_from", 0)):
            _back_to_counter(holder)
        else:
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
    var view := _view if _view.x > 0.0 else Vector2(room.get("size", Vector2i(128, 112)))
    return (holder as Node2D).position.x - _pan.x > view.x / 2.0


func _on_npc_spoke(npc_id: String, _name: String, text: String, _kind: String, _at: String,
        _structure_id: String, _mentions: Array, _speaker_x: float, _speaker_y: float, _room_id: String,
        _addressee_id: String, _addressee_name: String, _mention_prices: Dictionary, _huddle_scoped: bool,
        _recipient_ids: Array) -> void:
    say(npc_id, text)


## The person under a stage-local point (the nearest one in front wins), or "".
func person_at(local: Vector2) -> String:
    var art := _art_at(local)
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
    if event is InputEventMouseMotion:
        if not _press.is_empty():
            # A press on a scrolling stage that moves far enough (in screen
            # px) pans it — from where it was at the press, by the whole way
            # moved, so small steps are not rounded away.
            var moved: Vector2 = event.position - Vector2(_press["at"])
            if not bool(_press["drag"]) and (moved * _stretch).length() >= DRAG_START:
                _press["drag"] = true
                hide_tag()
            if bool(_press["drag"]):
                _set_pan(Vector2(_press["pan"]) - moved * _stretch / float(k))
            return
        # A tap's tag runs its time out: a touch screen sends motion with a
        # tap, and may send no motion or exit after it to take a hover down.
        if _tag_left <= 0.0:
            _show_tag(thing_at(event.position), 0.0)
    elif event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT:
        stage.accept_event()
        if event.pressed and scrolls():
            # Tap or drag is known on release.
            _press = {"at": event.position, "pan": _pan, "drag": false}
        elif event.pressed:
            _tap(event.position)
        elif not _press.is_empty():
            var dragged := bool(_press["drag"])
            _press = {}
            if not dragged:
                _tap(event.position)
    elif event is InputEventMouseButton and event.pressed and scrolls():
        # The wheel scrolls up and down when the room is too tall, else along
        # (a wide room like the smithy); sideways wheels always along.
        var size := Vector2(room.get("size", Vector2i(128, 112)))
        var roll := Vector2(0, WHEEL_STEP) if _view.y < size.y else Vector2(WHEEL_STEP, 0)
        var step := {MOUSE_BUTTON_WHEEL_UP: -roll, MOUSE_BUTTON_WHEEL_DOWN: roll,
            MOUSE_BUTTON_WHEEL_LEFT: Vector2(-WHEEL_STEP, 0), MOUSE_BUTTON_WHEEL_RIGHT: Vector2(WHEEL_STEP, 0)}
        if step.has(event.button_index):
            stage.accept_event()
            hide_tag()
            _set_pan(_pan + step[event.button_index])


## A tap shows the tag a while — on a touch screen there is no hover — and a
## tap on a person opens talk. In a room the player's character is drawn in,
## a tap on the floor or a free seat walks them there (floor_tapped).
func _tap(local: Vector2) -> void:
    var thing := thing_at(local)
    _show_tag(thing, TAG_SECONDS)
    if str(thing.get("kind", "")) == "person":
        person_clicked.emit(str(thing["id"]))
    elif thing.is_empty() and not is_stall() and pc_actor_id != "" and _holder(pc_actor_id) != null:
        var square := InteriorRooms.square_at(room, _art_at(local))
        if square != Vector2i(-1, -1):
            floor_tapped.emit(square)


func _on_stage_mouse_exited() -> void:
    if _tag_left <= 0.0:
        hide_tag()


## What is under `local` (stage coordinates), front to back: {kind: "person",
## id, holder} or {kind: "good", node, item}, or {}. At a stall the customers
## stand in front of the counter goods (and goods being handed over), those
## in front of the keeper and hands, and they in front of the floor piles; in
## a room what lies on the bar and tables is drawn over the people.
func thing_at(local: Vector2) -> Dictionary:
    var art := _art_at(local)
    var layers: Array = [_people, _goods_front, _handoff, _staff, _goods_back] if is_stall() else [_handoff, _people]
    for layer in layers:
        var found := _person_in(layer, art) if layer == _people or layer == _staff else _good_in(layer, art)
        if not found.is_empty():
            return found
    return {}


## The front-most drawn person in `layer` whose hit box holds `art`.
func _person_in(layer: Node2D, art: Vector2) -> Dictionary:
    var best: Dictionary = {}
    var best_y := -INF
    for h in _hits:
        var holder: Node2D = h["holder"]
        if not is_instance_valid(holder) or holder.get_parent() != layer:
            continue
        var r := Rect2(holder.position + HIT_BOX.position, HIT_BOX.size)
        if r.has_point(art) and r.end.y > best_y:
            best = {"kind": "person", "id": str(h["id"]), "holder": holder}
            best_y = r.end.y
    return best


## The front-most good in `layer` whose box holds `art` — the lower part of
## its cell, widened for a good drawn two or more times.
func _good_in(layer: Node2D, art: Vector2) -> Dictionary:
    var children := layer.get_children()
    children.reverse()
    for node in children:
        if not node.has_meta("item") or node.is_queued_for_deletion() or (node as CanvasItem).modulate.a < 0.5:
            continue
        var r := Rect2((node as Node2D).position + GOOD_BOX.position, GOOD_BOX.size)
        var copies := node.get_child_count()
        if copies > 1:
            r.size.x += StallScenes.FEW_STEP.x * (copies - 1)
        if r.has_point(art):
            return {"kind": "good", "node": node, "item": str(node.get_meta("item"))}
    return {}


## Show the tag on `thing` (from thing_at), or hide it for {}; `seconds` > 0
## keeps it up that long (a tap — and a tap on nothing takes a tag down), 0
## while hovered.
func _show_tag(thing: Dictionary, seconds: float) -> void:
    if thing.is_empty():
        if seconds > 0.0 or _tag_left <= 0.0:
            hide_tag()
        return
    var lines := tag_lines(thing)
    if str(lines[0]) == "":
        hide_tag()
        return
    tag_name.text = str(lines[0])
    tag_role.text = str(lines[1])
    tag_role.visible = str(lines[1]) != ""
    _tag_on = thing
    _tag_left = seconds
    tag.visible = true
    tag.reset_size()
    _place_tag()


func hide_tag() -> void:
    _tag_on = {}
    _tag_left = 0.0
    if tag != null:
        tag.visible = false


## [name, second line]: a person's name and role (a villager's first
## attribute, as the village popup shows it; a player's name alone), or a
## good's name.
func tag_lines(thing: Dictionary) -> Array:
    if str(thing.get("kind", "")) == "good":
        return [good_label(str(thing["item"])), ""]
    var c: Node2D = world.placed_npcs.get(str(thing.get("id", "")), null) if world != null else null
    if c == null:
        return ["", ""]
    var role := ""
    var attrs = c.get_meta("attributes", [])
    if str(c.get_meta("llm_memory_agent", "")) != "" and attrs is Array and not attrs.is_empty():
        var slug := str(attrs[0])
        role = slug.substr(0, 1).to_upper() + slug.substr(1)
    return [str(c.get_meta("display_name", "")), role]


## A good's name: the label the stall's shelf carries, else its kind made
## readable ("fried_meat" → "Fried meat").
func good_label(item: String) -> String:
    for w in _wares:
        if str(w.get("item", "")) == item and str(w.get("display_label", "")) != "":
            return str(w["display_label"])
    var words := item.replace("_", " ")
    return words.substr(0, 1).to_upper() + words.substr(1)


## Keep the tag over what it is on — a person may be walking — and take a
## tap's tag down when its time is up, or when its person or good is gone.
func _update_tag(delta: float) -> void:
    if _tag_on.is_empty():
        return
    if _tag_left > 0.0:
        _tag_left -= delta
        if _tag_left <= 0.0:
            hide_tag()
            return
    _place_tag()


## Put the tag centred above its person's head or its good, inside the window.
func _place_tag() -> void:
    var anchor: Vector2
    if str(_tag_on.get("kind", "")) == "person":
        var holder := _holder(str(_tag_on["id"]))
        if holder == null:
            hide_tag()
            return
        _tag_on["holder"] = holder
        anchor = holder.position + Vector2(0, HIT_BOX.position.y - 2)
    else:
        var node: Node2D = _tag_on.get("node", null)
        if not is_instance_valid(node) or node.is_queued_for_deletion():
            hide_tag()
            return
        anchor = node.position + Vector2(GOOD_BOX.size.x / 2.0, GOOD_BOX.position.y - 2)
    # Canvas coordinates throughout: the scene's point, the clamp to the
    # screen, and the tag's own global position.
    var at: Vector2 = _scene.get_global_transform() * anchor
    var size := tag.size
    var view := get_viewport().get_visible_rect().size if is_inside_tree() else Vector2(1e6, 1e6)
    tag.global_position = Vector2(clampf(at.x - size.x / 2.0, 4.0, maxf(4.0, view.x - size.x - 4.0)),
        clampf(at.y - size.y, 4.0, maxf(4.0, view.y - size.y - 4.0)))


func _on_backdrop_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        close()
        get_viewport().set_input_as_handled()


func _unhandled_input(event: InputEvent) -> void:
    if visible and event.is_action_pressed("ui_cancel"):
        close()
        get_viewport().set_input_as_handled()


## Put the room's pictures on: the backdrop, the counter (a stall's, or a
## room's desk) and the animated pieces (the goods redraw from their own
## sheet). A picture still downloading leaves its layer blank until it lands.
func _apply_textures() -> void:
    _background.texture = _texture(str(room.get("sheet", "")))
    _counter.texture = _texture(str(room.get("counter", ""))) if room.has("counter") else null
    _draw_anims()


## A room's furniture (`pieces`, cut from its `pieces_sheet`) and animated
## pieces (`anims`), once their pictures have landed. A piece goes in the
## people's layer, sorted by its `sort_y` — the line where it stands — so
## someone behind the bar is drawn under it and someone in front over it
## (LLM-767): the sprite sits at (x, sort_y) and its picture is offset up to
## its top. An anim with a `sort_y` is such a piece; one without (the fire in
## the fireplace) is part of the wall, drawn under everyone.
func _draw_anims() -> void:
    for child in _anims.get_children():
        child.free()
    for child in _people.get_children():
        if child.has_meta("piece"):
            child.free()
    var sheet := _texture(str(room.get("pieces_sheet", "")))
    if sheet != null:
        for p in room.get("pieces", []):
            var spr := _piece(sheet, int(p.get("x", 0)), int(p.get("y", 0)), int(p.get("sort_y", 0)))
            spr.region_enabled = true
            spr.region_rect = Rect2(int(p.get("x", 0)), int(p.get("y", 0)), int(p.get("w", 0)), int(p.get("h", 0)))
            _people.add_child(spr)
    for anim in room.get("anims", []):
        var strip := _texture(str(anim.get("sheet", "")))
        if strip == null:
            continue
        var sorted: bool = anim.has("sort_y")
        var spr := _piece(strip, int(anim.get("x", 0)), int(anim.get("y", 0)), int(anim.get("sort_y", anim.get("y", 0))))
        if not sorted:
            spr.remove_meta("piece")
        spr.hframes = maxi(1, int(anim.get("frames", 1)))
        spr.set_meta("fps", float(anim.get("fps", 6.0)))
        (_people if sorted else _anims).add_child(spr)
    _animate(0.0)


## A picture whose top-left is at (x, y), placed at (x, sort_y) for sorting.
func _piece(texture: Texture2D, x: int, y: int, sort_y: int) -> Sprite2D:
    var spr := Sprite2D.new()
    spr.centered = false
    spr.texture = texture
    spr.position = Vector2(x, sort_y)
    spr.offset = Vector2(0, y - sort_y)
    spr.set_meta("piece", true)
    return spr


func _animate(delta: float) -> void:
    _anim_time += delta
    for spr in _anims.get_children() + _people.get_children():
        if spr.has_meta("fps"):
            spr.frame = int(_anim_time * float(spr.get_meta("fps"))) % spr.hframes


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
    for slot in StallScenes.lay_out(_wares, room):
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
            spr.set_meta("item", str(slot["item"]))
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
## it is served — a good eaten here (consume_now) to the table in front of a
## seated buyer, where it stays until they are done and the keeper clears it
## (LLM-758); anything else toward the buyer, standing, seated or the player's
## character at the door. A good the keeper buys lies on the bar until they
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
        "other": str(data.get("buyer_id", "")) if selling else str(data.get("seller_id", "")),
        "eaten": bool(data.get("consume_now", false))})
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
    if h.has("clear"):
        if not is_instance_valid(h["clear"]):
            _next_handoff()
            return
        h["good"] = h["clear"]
        h["stop"] = _spot_point(room.get("reach", room["keeper"]))
        h["facing"] = str(room["keeper"].get("facing", "south"))
        h["keeper"] = str(keeper.name) if keeper != null else ""
        h["arrived"] = false
        _handoff_now = h
        _handoff_walk()
        return
    var at: Vector2
    if is_stall():
        # Where along the counter: in front of the other party, or the middle
        # when they are not drawn.
        var size: Vector2i = room.get("size", StallScenes.SIZE)
        var x := float(room["keeper"].get("x", size.x / 2.0))
        var customer := _people.get_node_or_null(NodePath(str(h["other"]))) as Node2D
        if customer != null:
            x = clampf(customer.position.x, float(room.get("counter_from", 0)) + 10.0, size.x - 10.0)
        at = Vector2(roundf(x), StallScenes.COUNTER_Y)
        h["stop"] = Vector2(at.x, (keeper.get_meta("home") as Vector2).y if keeper != null else 0.0)
        h["facing"] = "south"
    else:
        at = _spot_point(room["bar"])
        h["stop"] = _spot_point(room.get("reach", room["keeper"]))
        h["facing"] = str(room["keeper"].get("facing", "south"))
        h["serve"] = _serve_to(str(h["other"]), at, bool(h["eaten"]))
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
## when it is `eaten` here (it stays); otherwise most of the way toward the
## buyer, standing, seated or the player's character at the door (it fades as
## it goes). {} for a buyer not drawn: it stays on the bar a moment and goes.
func _serve_to(buyer: String, bar: Vector2, eaten: bool) -> Dictionary:
    var spot: Dictionary = _spot_of.get(buyer, {})
    if spot.is_empty():
        return {}
    if eaten and spot.has("serve"):
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
    good.set_meta("item", item)
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
    # A sale calls them from the anvil, or the way there.
    keeper.remove_meta("smithing")
    keeper.remove_meta("anvil_trip")
    _walk(keeper, _handoff_now["stop"], WALK_SPEED, str(_handoff_now["facing"]), "stand", _handoff_arrived)


## A good's cell position (top-left) for a good standing at `at` (its bottom
## middle).
func _cell_at(at: Vector2) -> Vector2:
    return Vector2(at.x - StallScenes.CELL.x / 2.0, at.y - StallScenes.CELL.y)


## The keeper is at the handoff: a good sold is set down and lies a moment;
## at a stall it then goes toward the buyer (down, out of the stall), in a
## room it is served (_serve_to) — a meal to the table, where it stays
## (_on_table) while the next handoff begins. A good bought is taken up toward
## the keeper. In a room the keeper then steps back to their spot.
func _handoff_arrived() -> void:
    if _handoff_now.is_empty() or bool(_handoff_now["arrived"]):
        return
    _handoff_now["arrived"] = true
    var good: Node2D = _handoff_now["good"]
    if not is_instance_valid(good):
        _next_handoff()
        return
    if _handoff_now.has("clear"):
        _clear_arrived(good)
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
            # The bar is freed first: _on_table may clear the dish at once,
            # which stops this tween.
            t.tween_callback(_next_handoff)
            t.tween_callback(_on_table.bind(good, str(_handoff_now["other"]), str(_handoff_now["item"])))
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


## A dish set on `buyer`'s table stays while their meal of `item` runs, until
## world.npc_meal_ended (_on_meal_ended); with no meal running (ale, bread, or
## a meal already over) it stays TABLE_SECONDS. A buyer gone before it lands
## leaves it to be cleared at once. A second helping of the same good: the
## first bowl is cleared.
func _on_table(dish: Node2D, buyer: String, item: String) -> void:
    for old in _dishes():
        if str(old.get_meta("buyer")) == buyer and str(old.get_meta("item")) == item:
            _clear_dish(old)
    dish.set_meta("buyer", buyer)
    dish.set_meta("item", item)
    if _holder(buyer) == null:
        _clear_dish(dish)
        return
    if world != null and world.has_method("meal_of") and world.meal_of(buyer, structure_id) == item:
        dish.remove_meta("tween")
        return
    var t := dish.create_tween()
    t.tween_interval(TABLE_SECONDS)
    t.tween_callback(_clear_dish.bind(dish))
    dish.set_meta("tween", t)


## The dishes on the tables, not yet being cleared.
func _dishes() -> Array:
    var out: Array = []
    for child in _handoff.get_children():
        if child.has_meta("buyer") and not child.has_meta("clearing") and not child.is_queued_for_deletion():
            out.append(child)
    return out


## Clear a dish from its table: the keeper comes to the bar's front and the
## dish goes back across to the bar and away — in turn with the handoffs, so
## the keeper is never on two errands at once.
func _clear_dish(dish: Node2D) -> void:
    if not is_instance_valid(dish) or dish.has_meta("clearing"):
        return
    dish.set_meta("clearing", true)
    if dish.has_meta("tween"):
        (dish.get_meta("tween") as Tween).kill()
        dish.remove_meta("tween")
    _handoffs.append({"clear": dish})
    if _handoff_now.is_empty():
        _next_handoff()


## The keeper is at the bar's front for a dish: it slides back to the bar,
## the keeper steps back, and it fades. Nobody behind the bar: it fades where
## it is.
func _clear_arrived(dish: Node2D) -> void:
    var keeper_id := str(_handoff_now["keeper"])
    var t := dish.create_tween()
    if keeper_id != "":
        var to := _cell_at(_spot_point(room["bar"]))
        t.tween_property(dish, "position", to, maxf(dish.position.distance_to(to) / SERVE_SPEED, 0.2))
        t.tween_callback(_keeper_back.bind(keeper_id))
    t.tween_property(dish, "modulate:a", 0.0, HANDOFF_AWAY)
    t.tween_callback(func():
        dish.queue_free()
        _next_handoff())
    dish.set_meta("tween", t)


## Lay the dish of every running meal (world.meals) at the table of whoever
## is seated eating it — the window opened partway through.
func _lay_running_meals() -> void:
    if not room.has("bar") or world == null or not world.has_method("meal_of"):
        return
    for entry in _hits:
        var id := str(entry["id"])
        var spot: Dictionary = _spot_of.get(id, {})
        var item: String = world.meal_of(id, structure_id)
        if not spot.has("serve") or not StallScenes.GOODS.has(item):
            continue
        var dish := _handoff_good(item, 1, _spot_point(spot["serve"]))
        if dish != null:
            dish.set_meta("buyer", id)
            dish.set_meta("item", item)


## A meal is over (world.npc_meal_ended): its dish is cleared.
func _on_meal_ended(actor_id: String, item: String) -> void:
    for dish in _dishes():
        if str(dish.get_meta("buyer")) == actor_id and str(dish.get_meta("item")) == item:
            _clear_dish(dish)


## A new meal of another good takes the place of the one running: the old
## dish, left waiting on a meal that will now send no end, is cleared.
func _on_meal_started(actor_id: String, item: String) -> void:
    for dish in _dishes():
        if str(dish.get_meta("buyer")) == actor_id and str(dish.get_meta("item")) != item \
                and not dish.has_meta("tween"):
            _clear_dish(dish)


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
