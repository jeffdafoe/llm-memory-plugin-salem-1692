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
    close_button.text = "✕"
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
    var was_open := visible
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
    refresh()
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
        placed.append(_entry(keeper_id, room.get("keeper", {})))
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
    for child in _people.get_children() + _staff.get_children():
        var bubble := child.get_node_or_null(BUBBLE_NODE)
        if bubble != null:
            child.remove_child(bubble)
            bubbles[str(child.name)] = bubble
        child.free()
    _hits.clear()
    for entry in cast["placed"]:
        var id := str(entry["id"])
        var spot: Dictionary = entry["spot"]
        var c: Node2D = world.placed_npcs.get(id, null)
        if c == null:
            continue
        var spr: AnimatedSprite2D = world._build_character_sprite(c.get_meta("sprite_data", {}))
        if spr == null:
            continue
        # From the village's draw scale down to the room's art pixels.
        var s := spr.scale.x
        if s > 0.0:
            spr.scale /= s
            spr.position /= s
        var holder := Node2D.new()
        holder.name = id
        holder.position = Vector2(int(spot.get("x", 0)), int(spot.get("y", 0)))
        holder.add_child(spr)
        # A stall's keeper stands behind the counter, which is drawn over them.
        (_staff if bool(entry.get("behind", false)) else _people).add_child(holder)
        _pose(spr, str(spot.get("facing", "south")), str(spot.get("pose", "stand")))
        if str(c.get_meta("dormant", "")) == "resting":
            spr.modulate = RESTING_DIM
        _hits.append({"id": id, "rect": Rect2(holder.position + HIT_BOX.position, HIT_BOX.size)})
    for id in bubbles:
        var holder := _holder(id)
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
        return
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
        var r: Rect2 = h["rect"]
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


## Ask the engine what is on this stall's shelf.
func _request_wares() -> void:
    var sid := structure_id
    if wares_hook.is_valid():
        set_wares(wares_hook.call(sid))
        return
    if _http_wares == null:
        _http_wares = HTTPRequest.new()
        add_child(_http_wares)
        _http_wares.request_completed.connect(_on_wares)
    elif _http_wares.get_http_client_status() != HTTPClient.STATUS_DISCONNECTED:
        # Already asking about this stall: that answer will do. A request for
        # the stall the window just left is dropped for this one.
        if str(_http_wares.get_meta("structure_id", "")) == sid:
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
