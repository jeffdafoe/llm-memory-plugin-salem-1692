extends CanvasLayer
## Editor — handles object placement, selection, drag-to-move, and deletion.
## Sits on a CanvasLayer so UI elements stay screen-fixed.
## Coordinates with camera.gd (disables left-click pan when active).
##
## All mouse handling runs in _input (before GUI Controls) to prevent the
## editor panel from swallowing events. A position check skips clicks
## that land on the UI panel area.

signal object_selected(info: Dictionary)
signal object_deselected
signal npc_selected(info: Dictionary)
signal npc_deselected
signal mode_changed(mode: Mode)

enum Mode { SELECT, PLACE, MOVE, TERRAIN, ASSIGN_HOME, ASSIGN_WORK }

var current_mode: Mode = Mode.SELECT
var selected_asset_id: String = ""
var selected_object: Node2D = null
var selected_npc: Node2D = null
var ghost_sprite: Sprite2D = null
var active: bool = false

# NPC placement state — when _placing_npc is true, the PLACE-mode click path
# creates a new NPC via POST /api/village/npcs instead of placing an object.
# Mutually exclusive with selected_asset_id being set.
var _placing_npc: bool = false
var _placing_npc_sprite: Dictionary = {}
var _placing_npc_name: String = ""

# NPC selection border (added as a child of the selected NPC container)
var _npc_selection_border: Node2D = null

# When true, a popup overlay is open — skip all map input
var popup_open: bool = false

# Set to true when the editor consumes a left-click (object hit, placement, etc.)
# Camera checks this to decide whether left-click should pan.
var left_click_used: bool = false

# Selection border node — added as child of selected object. Outlines the
# asset's PATHFIND footprint (per-side tile counts from the catalog), not
# the sprite pixel rect, so what you see is what the pathfinder blocks.
# Each of the 4 edges is grabbable to drag-resize that side. See
# _on_footprint_edge_press / _on_footprint_drag.
var _selection_border: Node2D = null

# Footprint drag state. _footprint_resize_side is one of: "" (no drag),
# "left", "right", "top", "bottom".
const TILE_SIZE: float = 32.0
const FOOTPRINT_EDGE_HIT_PX: float = 8.0  # screen-pixel hit slop on each side
var _footprint_resize_side: String = ""
var _footprint_resize_start_value: int = 0
var _footprint_resize_start_world: Vector2 = Vector2.ZERO

# Door marker drag state — editor for asset.door_offset_{x,y}. Only shown
# when the selected object is a structure. The marker is a child of the
# structure node; _door_marker_asset_id pins which asset it belongs to so
# a mid-drag asset broadcast doesn't repaint over our in-flight change.
# NPC-selected left-click-to-walk is deferred until release so the user
# can left-drag the map to pan. Press sets _npc_walk_pending; motion past
# the drag threshold cancels it (it was a pan, not a walk); release with
# the flag still set fires the walk to the original click position.
var _npc_walk_pending: bool = false
var _npc_walk_start_screen: Vector2 = Vector2.ZERO

var _door_marker: Node2D = null
var _door_marker_asset_id: String = ""
var _door_dragging: bool = false
var _door_drag_start_offset: Vector2 = Vector2.ZERO  # tile offset (could be -Inf,-Inf for "none")

# Terrain painting state
var _terrain_type: int = 0
var _terrain_painting: bool = false
var _terrain_save_timer: float = 0.0
const TERRAIN_SAVE_DELAY: float = 2.0

# Drag-to-move state
var _dragging: bool = false
var _drag_start_world: Vector2 = Vector2.ZERO
var _drag_start_obj_pos: Vector2 = Vector2.ZERO
## 4px was too tight — a normal mouse wobble during a click would cross
## it and either (a) drag a structure slightly (hence the "stuff keeps
## moving north" drift) or (b) cancel an NPC walk-pending so the release
## does nothing. 10px tolerates wobble but still feels like a click.
var _drag_threshold: float = 10.0
var _drag_pending: bool = false
var _drag_mouse_start: Vector2 = Vector2.ZERO

# UI panel area — clicks here belong to the UI, not the map
const PANEL_WIDTH: float = 240.0
const TOP_BAR_HEIGHT: float = 40.0

# References
@onready var world: Node2D = get_node("/root/Main/World")
@onready var camera: Camera2D = get_node("/root/Main/Camera")

func _ready() -> void:
    ghost_sprite = Sprite2D.new()
    ghost_sprite.centered = false
    ghost_sprite.scale = Vector2(2, 2)
    ghost_sprite.modulate = Color(1, 1, 1, 0.5)
    ghost_sprite.visible = false
    ghost_sprite.z_index = 1000
    world.add_child(ghost_sprite)

## Returns true if the screen position is over the editor UI panel area.
func _is_over_ui(pos: Vector2) -> bool:
    if pos.y < TOP_BAR_HEIGHT:
        return true
    if pos.x < PANEL_WIDTH:
        return true
    return false

## All mouse input runs here — before GUI Controls can consume events.
func _input(event: InputEvent) -> void:
    if not active:
        return
    if popup_open:
        return

    # --- Active operations that own all input until done ---

    # Footprint resize in progress — owns mouse motion + release.
    if _footprint_resize_side != "":
        if event is InputEventMouseMotion:
            _on_footprint_drag(event.position)
            get_viewport().set_input_as_handled()
        if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and not event.pressed:
            _commit_footprint_resize()
            get_viewport().set_input_as_handled()
        if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
            _cancel_footprint_resize()
            get_viewport().set_input_as_handled()
        return

    # Door marker drag in progress — same ownership pattern as footprint.
    if _door_dragging:
        if event is InputEventMouseMotion:
            _door_drag_motion(event.position)
            get_viewport().set_input_as_handled()
        if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and not event.pressed:
            _commit_door_drag()
            get_viewport().set_input_as_handled()
        if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
            _cancel_door_drag()
            get_viewport().set_input_as_handled()
        return

    # Terrain painting: hold mouse to paint continuously
    if _terrain_painting:
        if event is InputEventMouseMotion:
            _paint_terrain_at(event.position)
            get_viewport().set_input_as_handled()
        if event is InputEventMouseButton:
            if event.button_index == MOUSE_BUTTON_LEFT and not event.pressed:
                _terrain_painting = false
                _terrain_save_timer = TERRAIN_SAVE_DELAY
                get_viewport().set_input_as_handled()
        return

    # Drag in progress: own all mouse events until release
    if _dragging or _drag_pending:
        if event is InputEventMouseMotion:
            if _drag_pending:
                var dist: float = event.position.distance_to(_drag_mouse_start)
                if dist >= _drag_threshold:
                    _drag_pending = false
                    _dragging = true
            if _dragging:
                _drag_move(event.position)
                get_viewport().set_input_as_handled()
        if event is InputEventMouseButton:
            if event.button_index == MOUSE_BUTTON_LEFT and not event.pressed:
                _on_left_release(event.position)
                get_viewport().set_input_as_handled()
            # Right-click cancels drag and snaps object back
            if event.button_index == MOUSE_BUTTON_RIGHT and event.pressed:
                _cancel_drag()
                get_viewport().set_input_as_handled()
        # Escape also cancels drag
        if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
            _cancel_drag()
            get_viewport().set_input_as_handled()
        return

    # --- New interactions (skip if over UI) ---

    if event is InputEventMouseButton:
        if _is_over_ui(event.position):
            return

        if event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
            left_click_used = false
            _on_left_press(event.position)
        if event.button_index == MOUSE_BUTTON_LEFT and not event.pressed:
            left_click_used = false
            # A still-pending walk means the mouse didn't move far enough
            # during the press to be considered a pan — fire the walk now.
            if _npc_walk_pending:
                _walk_selected_npc(_npc_walk_start_screen)
                _npc_walk_pending = false
                get_viewport().set_input_as_handled()

        if event.button_index == MOUSE_BUTTON_RIGHT and event.pressed:
            if current_mode == Mode.PLACE:
                set_mode(Mode.SELECT)
                get_viewport().set_input_as_handled()
            elif current_mode == Mode.ASSIGN_HOME or current_mode == Mode.ASSIGN_WORK:
                # Cancel structure picking, return to normal select with NPC
                # still selected so the panel stays on the same villager.
                set_mode(Mode.SELECT)
                get_viewport().set_input_as_handled()
            elif current_mode == Mode.SELECT and selected_npc != null:
                _deselect_npc()
                get_viewport().set_input_as_handled()

    if event is InputEventMouseMotion:
        # If an NPC is selected and the mouse moves past the drag threshold
        # before release, treat the gesture as a pan rather than a walk —
        # cancel the pending walk so release is a no-op.
        if _npc_walk_pending:
            if event.position.distance_to(_npc_walk_start_screen) >= _drag_threshold:
                _npc_walk_pending = false
        if current_mode == Mode.PLACE and ghost_sprite.visible:
            ghost_sprite.global_position = _screen_to_world(event.position)
            _apply_ghost_offset()

    # Keyboard shortcuts
    if event is InputEventKey and event.pressed:
        if event.keycode == KEY_DELETE:
            if selected_npc != null:
                _delete_selected_npc()
                get_viewport().set_input_as_handled()
            elif selected_object != null:
                _delete_selected()
                get_viewport().set_input_as_handled()
        if event.keycode == KEY_ESCAPE:
            # In SELECT mode with an NPC selected, Esc deselects the NPC
            # rather than re-running set_mode(SELECT), which would no-op.
            # In ASSIGN_HOME/ASSIGN_WORK, Esc cancels picking but keeps the
            # NPC selected (set_mode(SELECT) preserves selection).
            if current_mode == Mode.ASSIGN_HOME or current_mode == Mode.ASSIGN_WORK:
                set_mode(Mode.SELECT)
            elif current_mode == Mode.SELECT and selected_npc != null:
                _deselect_npc()
            else:
                set_mode(Mode.SELECT)
            get_viewport().set_input_as_handled()

func _on_left_press(screen_pos: Vector2) -> void:
    match current_mode:
        Mode.PLACE:
            _place_at_mouse(screen_pos)
            left_click_used = true
            get_viewport().set_input_as_handled()
        Mode.SELECT:
            # If an NPC is already selected, clicks either switch to a different
            # NPC or command the selected one to walk. The server's
            # findPathToAdjacent handles obstacle targets by routing to the
            # nearest walkable neighbor — same mechanism the lamplighter uses.
            if selected_npc != null:
                var npc_hit_sel: Node2D = _find_npc_at(screen_pos)
                if npc_hit_sel != null and npc_hit_sel != selected_npc:
                    _select_npc(npc_hit_sel)
                    left_click_used = true
                    get_viewport().set_input_as_handled()
                    return
                if npc_hit_sel == selected_npc:
                    # Re-click on the same NPC deselects (matches the intuitive
                    # "click again to toggle off" pattern). Right-click / Esc
                    # still work as alternatives.
                    _deselect_npc()
                    left_click_used = true
                    get_viewport().set_input_as_handled()
                    return
                # With an NPC selected, ALL non-NPC clicks on the map are
                # walk-to commands — including clicks that overlap a
                # structure footprint. This avoids the "I clicked near the
                # house to send them over and got the house selected
                # instead" trap. To switch to object selection, first
                # deselect the NPC (Esc, right-click, re-click the NPC,
                # or press the Select tool button).
                # Defer the walk-to until release so click-vs-drag can be
                # distinguished: if the mouse moves past the drag threshold
                # before release, the gesture is a pan (cancels the walk);
                # otherwise release fires the walk-to. Leave left_click_used
                # false so camera starts pan speculatively.
                _npc_walk_pending = true
                _npc_walk_start_screen = screen_pos
                return

            # If the selected object has a door marker and the click lands on
            # it, start a door drag. This takes priority over footprint edges
            # so the marker stays reachable when it overlaps a border.
            if selected_object != null and _hit_test_door_marker(screen_pos):
                _begin_door_drag(screen_pos)
                left_click_used = true
                get_viewport().set_input_as_handled()
                return

            # If there's already a selected object and the click is on its
            # footprint border edge, start a drag-resize instead of falling
            # through to object selection / drag-move.
            if selected_object != null:
                var side: String = _hit_test_footprint_edge(screen_pos, selected_object)
                if side != "":
                    _begin_footprint_resize(side, screen_pos)
                    left_click_used = true
                    get_viewport().set_input_as_handled()
                    return

            # NPC hit-testing takes priority over objects. NPCs stand on top of
            # obstacles z-wise, and clicking a villager should select the villager,
            # not whatever asset happens to share that screen pixel.
            var npc_hit: Node2D = _find_npc_at(screen_pos)
            if npc_hit != null:
                _select_npc(npc_hit)
                left_click_used = true
                get_viewport().set_input_as_handled()
                return

            var hit = _find_object_at(screen_pos)
            if hit != null:
                _select_object(hit)
                _drag_pending = true
                _drag_mouse_start = screen_pos
                _drag_start_world = _screen_to_world(screen_pos)
                _drag_start_obj_pos = hit.position
                left_click_used = true
                get_viewport().set_input_as_handled()
            else:
                _deselect()
                # Don't set left_click_used — let camera pan on empty space
        Mode.TERRAIN:
            if _terrain_type > 0:
                _terrain_painting = true
                _paint_terrain_at(screen_pos)
                left_click_used = true
                get_viewport().set_input_as_handled()
        Mode.ASSIGN_HOME:
            _try_assign_structure(screen_pos, true)
            left_click_used = true
            get_viewport().set_input_as_handled()
        Mode.ASSIGN_WORK:
            _try_assign_structure(screen_pos, false)
            left_click_used = true
            get_viewport().set_input_as_handled()

func _on_left_release(screen_pos: Vector2) -> void:
    if _dragging:
        _drag_end(screen_pos)
    _drag_pending = false
    _dragging = false

func set_mode(new_mode: Mode) -> void:
    current_mode = new_mode
    if new_mode != Mode.PLACE:
        ghost_sprite.visible = false
        selected_asset_id = ""
        _placing_npc = false
        _placing_npc_sprite = {}
    # ASSIGN_HOME / ASSIGN_WORK intentionally preserve the NPC (and any
    # object) selection — the whole point is to act on the currently
    # selected NPC. Only PLACE/TERRAIN/MOVE force a clean slate.
    if new_mode != Mode.SELECT and new_mode != Mode.ASSIGN_HOME and new_mode != Mode.ASSIGN_WORK:
        _deselect()
        _deselect_npc()
    _dragging = false
    _drag_pending = false
    _terrain_painting = false
    mode_changed.emit(new_mode)

## Enter structure-picking mode for the currently selected NPC. Called from
## main when the user clicks the Set Home / Set Work button in the editor
## panel. If nothing is selected, no-op (the button shouldn't be clickable
## in that state anyway).
func begin_assign_home() -> void:
    if selected_npc == null:
        return
    set_mode(Mode.ASSIGN_HOME)

func begin_assign_work() -> void:
    if selected_npc == null:
        return
    set_mode(Mode.ASSIGN_WORK)

## Hit-test for a structure under the mouse; if found, PATCH the NPC's
## home_structure_id or work_structure_id and return to SELECT mode. Clicks
## that miss or hit a non-structure are ignored so the user can keep trying
## without leaving the mode.
func _try_assign_structure(screen_pos: Vector2, is_home: bool) -> void:
    if selected_npc == null:
        set_mode(Mode.SELECT)
        return
    var hit: Node2D = _find_object_at(screen_pos)
    if hit == null:
        return
    var asset_id: String = hit.get_meta("asset_id", "")
    var asset: Dictionary = Catalog.assets.get(asset_id, {})
    if not bool(asset.get("enterable", false)):
        return
    var structure_id: String = hit.get_meta("object_id", "")
    if structure_id == "":
        return
    if is_home:
        world.set_npc_home_structure(selected_npc, structure_id)
    else:
        world.set_npc_work_structure(selected_npc, structure_id)
    set_mode(Mode.SELECT)

## Called by the editor panel when the user picks an asset from the catalog,
## or when the user presses the Select tool button (asset_id == "").
## Pressing Select with something selected also clears the selection — that's
## the panel-driven deselect.
func select_asset_for_placement(asset_id: String) -> void:
    if asset_id == "":
        _deselect()
        _deselect_npc()
        set_mode(Mode.SELECT)
        return

    selected_asset_id = asset_id
    _placing_npc = false
    _placing_npc_sprite = {}
    current_mode = Mode.PLACE
    _deselect()
    _deselect_npc()

    var state_info = Catalog.get_state(asset_id)
    if state_info == null:
        return

    var texture = Catalog.get_sprite_texture(state_info)
    if texture == null:
        return

    ghost_sprite.texture = texture
    ghost_sprite.visible = true
    _apply_ghost_offset()
    mode_changed.emit(Mode.PLACE)

## Called by the editor panel when the user picks an NPC sprite template.
## Switches the editor into placement mode with a ghost showing the south-
## idle frame of the selected sprite. `sheet` is the preloaded ImageTexture
## so we don't re-download it.
func select_npc_sprite_for_placement(sprite: Dictionary, sheet: Texture2D, npc_name: String) -> void:
    if sprite == null or sprite.is_empty() or sheet == null:
        set_mode(Mode.SELECT)
        return

    selected_asset_id = ""
    _placing_npc = true
    _placing_npc_sprite = sprite
    _placing_npc_name = npc_name
    current_mode = Mode.PLACE
    _deselect()
    _deselect_npc()

    var fw: int = int(sprite.get("frame_width", 32))
    var fh: int = int(sprite.get("frame_height", 32))

    # Frame 0 of row 0 = south-facing idle. Matches the rendering default in
    # world._render_npc (it plays facing + "_idle" with facing default "south").
    var atlas := AtlasTexture.new()
    atlas.atlas = sheet
    atlas.region = Rect2(0, 0, fw, fh)

    ghost_sprite.texture = atlas
    ghost_sprite.visible = true
    # Match world._render_npc's feet-anchoring: sprite top-left is offset
    # (-fw/2, -fh*0.9) from the NPC's position, in texture pixels (the ghost
    # has scale 2, so the world-pixel shift is double).
    ghost_sprite.offset = Vector2(-fw * 0.5, -fh * 0.9)
    mode_changed.emit(Mode.PLACE)

func _apply_ghost_offset() -> void:
    if _placing_npc:
        return  # NPC ghost offset is set once in select_npc_sprite_for_placement
    if selected_asset_id == "":
        return
    var asset = Catalog.assets.get(selected_asset_id, {})
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))
    var tex = ghost_sprite.texture
    if tex != null:
        ghost_sprite.offset = Vector2(
            -tex.region.size.x * anchor_x,
            -tex.region.size.y * anchor_y
        )

func _place_at_mouse(screen_pos: Vector2) -> void:
    if _placing_npc:
        _place_npc_at_mouse(screen_pos)
        return
    if selected_asset_id == "":
        return
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var placed_node: Node2D = world.add_object(selected_asset_id, world_pos)
    set_mode(Mode.SELECT)
    # Auto-select the newly placed object so attachments show immediately
    if placed_node != null:
        _select_object(placed_node)

## POST /api/village/npcs at the click location. Server handles auth (admin
## only), sprite validation, insert, and broadcast. The npc_created WS event
## will arrive and render the new villager — we return to SELECT mode
## immediately without auto-selecting (the broadcast handler creates the
## Node2D; we don't have a local reference to pass to _select_npc).
func _place_npc_at_mouse(screen_pos: Vector2) -> void:
    var sprite_id: String = _placing_npc_sprite.get("id", "")
    if sprite_id == "":
        set_mode(Mode.SELECT)
        return
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var payload = JSON.stringify({
        "name": _placing_npc_name,
        "sprite_id": sprite_id,
        "x": world_pos.x,
        "y": world_pos.y,
    })
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var headers := ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request(Auth.api_base + "/api/village/npcs", headers,
        HTTPClient.METHOD_POST, payload)
    set_mode(Mode.SELECT)

func _find_object_at(screen_pos: Vector2) -> Node2D:
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var best_node: Node2D = null
    var best_dist: float = INF

    for child in world.get_node("Objects").get_children():
        if child.get_child_count() == 0:
            continue
        var sprite_node: Node2D = _get_sprite_child(child)
        if sprite_node == null:
            continue

        var region_size: Vector2 = _get_sprite_size(sprite_node)
        if region_size == Vector2.ZERO:
            continue
        var world_size: Vector2 = region_size * sprite_node.scale
        var rect_origin: Vector2 = child.position + sprite_node.position
        var rect = Rect2(rect_origin, world_size)

        if rect.has_point(world_pos):
            var dist: float = child.position.distance_to(world_pos)
            if dist < best_dist:
                best_dist = dist
                best_node = child

    return best_node

## Find the sprite or animated sprite child of an object container.
func _get_sprite_child(node: Node2D) -> Node2D:
    for child in node.get_children():
        if child is Sprite2D or child is AnimatedSprite2D:
            return child
    return null

## Get the texture size of a sprite node (works for both Sprite2D and AnimatedSprite2D).
## For AnimatedSprite2D, objects use a "default" animation; NPCs use direction-based
## names ("south_idle", etc.). Prefer the currently playing animation, then "default",
## then any available animation as a last resort.
func _get_sprite_size(sprite_node: Node2D) -> Vector2:
    if sprite_node is Sprite2D:
        var tex = sprite_node.texture
        if tex != null:
            return tex.get_size()
    if sprite_node is AnimatedSprite2D:
        var frames: SpriteFrames = sprite_node.sprite_frames
        if frames == null:
            return Vector2.ZERO
        var anim_name: String = String(sprite_node.animation)
        if anim_name == "" or not frames.has_animation(anim_name):
            if frames.has_animation("default"):
                anim_name = "default"
            else:
                var names: PackedStringArray = frames.get_animation_names()
                if names.is_empty():
                    return Vector2.ZERO
                anim_name = names[0]
        if frames.get_frame_count(anim_name) > 0:
            var tex = frames.get_frame_texture(anim_name, 0)
            if tex != null:
                return tex.get_size()
    return Vector2.ZERO

func _select_object(node: Node2D) -> void:
    _deselect()
    _deselect_npc()
    selected_object = node
    _add_selection_border(node)
    _add_door_marker(node)
    object_selected.emit({
        "asset_id": node.get_meta("asset_id", ""),
        "object_id": node.get_meta("object_id", ""),
        "placed_by": node.get_meta("placed_by", ""),
        "owner": node.get_meta("owner", ""),
        "display_name": node.get_meta("display_name", ""),
    })

func _deselect() -> void:
    if selected_object != null:
        _remove_selection_border()
        _remove_door_marker()
        selected_object = null
        object_deselected.emit()

# --- NPC selection ---

## Find an NPC by point-in-sprite-rect, same approach as _find_object_at but
## walks world.placed_npcs (Dictionary<id, Node2D>). Returns the container node
## so callers can read meta for the info panel / walk command.
func _find_npc_at(screen_pos: Vector2) -> Node2D:
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var best_node: Node2D = null
    var best_dist: float = INF

    for npc_id in world.placed_npcs:
        var container: Node2D = world.placed_npcs[npc_id]
        if container == null:
            continue
        var sprite_node: Node2D = _get_sprite_child(container)
        if sprite_node == null:
            continue
        var region_size: Vector2 = _get_sprite_size(sprite_node)
        if region_size == Vector2.ZERO:
            continue
        var world_size: Vector2 = region_size * sprite_node.scale
        var rect_origin: Vector2 = container.position + sprite_node.position
        var rect = Rect2(rect_origin, world_size)
        if rect.has_point(world_pos):
            var dist: float = container.position.distance_to(world_pos)
            if dist < best_dist:
                best_dist = dist
                best_node = container

    return best_node

func _select_npc(container: Node2D) -> void:
    _deselect()
    _deselect_npc()
    selected_npc = container
    _add_npc_selection_border(container)
    npc_selected.emit({
        "npc_id": container.get_meta("npc_id", ""),
        "display_name": container.get_meta("display_name", ""),
        "behavior": container.get_meta("behavior", ""),
        "llm_memory_agent": container.get_meta("llm_memory_agent", ""),
        "home_structure_id": container.get_meta("home_structure_id", ""),
        "work_structure_id": container.get_meta("work_structure_id", ""),
    })

func _deselect_npc() -> void:
    if selected_npc != null:
        _remove_npc_selection_border()
        selected_npc = null
        npc_deselected.emit()

## Simple cyan box around the NPC's sprite bounds. No drag handles — NPCs
## don't have a resizable pathfinding footprint, they walk by A* from the
## server. Different color from the object selection border (gold) so users
## know which kind of thing they've grabbed.
func _add_npc_selection_border(container: Node2D) -> void:
    _remove_npc_selection_border()
    var sprite_node: Node2D = _get_sprite_child(container)
    if sprite_node == null:
        return
    var region_size: Vector2 = _get_sprite_size(sprite_node)
    if region_size == Vector2.ZERO:
        return
    var world_size: Vector2 = region_size * sprite_node.scale
    var origin: Vector2 = sprite_node.position  # local to container

    _npc_selection_border = Node2D.new()
    _npc_selection_border.name = "NPCSelectionBorder"
    _npc_selection_border.z_index = 999
    container.add_child(_npc_selection_border)

    var border := Line2D.new()
    border.width = 2.0
    border.default_color = Color(0.35, 0.85, 0.95, 0.9)
    border.closed = true
    border.add_point(origin)
    border.add_point(origin + Vector2(world_size.x, 0))
    border.add_point(origin + world_size)
    border.add_point(origin + Vector2(0, world_size.y))
    _npc_selection_border.add_child(border)

func _remove_npc_selection_border() -> void:
    if _npc_selection_border != null:
        _npc_selection_border.queue_free()
        _npc_selection_border = null

## Command the currently-selected NPC to walk to the clicked world point.
## The server handles pathfinding, obstacle avoidance, and walk-to-adjacent
## for obstacle targets (findPathToAdjacent). 400 on unreachable is
## silently ignored — the NPC just doesn't move.
func _walk_selected_npc(screen_pos: Vector2) -> void:
    if selected_npc == null:
        return
    var npc_id: String = selected_npc.get_meta("npc_id", "")
    if npc_id == "":
        return
    var target: Vector2 = _screen_to_world(screen_pos)
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var payload = JSON.stringify({"x": target.x, "y": target.y})
    var headers := ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request(Auth.api_base + "/api/village/npcs/" + npc_id + "/walk-to",
        headers, HTTPClient.METHOD_POST, payload)

## Draw the selected object's footprint as a tile-aligned rectangle. Each
## edge is grabbable (see _hit_test_footprint_edge) so the user can drag
## the side in or out and the change persists to asset.footprint_*.
## Falls back to a 1×1 (anchor tile only) outline when the asset has no
## footprint data — still serves as a selection indicator.
func _add_selection_border(node: Node2D) -> void:
    _remove_selection_border()

    var rect: Rect2 = _footprint_rect_local(node)
    if rect.size == Vector2.ZERO:
        return

    _selection_border = Node2D.new()
    _selection_border.name = "SelectionBorder"
    _selection_border.z_index = 999
    node.add_child(_selection_border)

    var border_color := Color(0.85, 0.75, 0.35, 0.9)
    var handle_color := Color(1.0, 0.92, 0.55, 1.0)

    var border = Line2D.new()
    border.width = 2.0
    border.default_color = border_color
    border.closed = true
    border.add_point(rect.position)
    border.add_point(rect.position + Vector2(rect.size.x, 0))
    border.add_point(rect.position + rect.size)
    border.add_point(rect.position + Vector2(0, rect.size.y))
    _selection_border.add_child(border)

    # Drag handles — small filled squares at the midpoint of each side so
    # users can see the rect is interactive. The actual hit zone is the
    # whole edge (see _hit_test_footprint_edge), the handles just signal
    # affordance. World-pixel sized; consistent with the fixed-width border.
    var handle_size: float = 12.0
    var mid_top: Vector2    = rect.position + Vector2(rect.size.x / 2, 0)
    var mid_bottom: Vector2 = rect.position + Vector2(rect.size.x / 2, rect.size.y)
    var mid_left: Vector2   = rect.position + Vector2(0, rect.size.y / 2)
    var mid_right: Vector2  = rect.position + Vector2(rect.size.x, rect.size.y / 2)
    for center in [mid_top, mid_bottom, mid_left, mid_right]:
        _selection_border.add_child(_make_handle(center, handle_size, handle_color))

func _make_handle(center: Vector2, size: float, color: Color) -> Polygon2D:
    var half: float = size / 2.0
    var poly := Polygon2D.new()
    poly.color = color
    poly.polygon = PackedVector2Array([
        center + Vector2(-half, -half),
        center + Vector2( half, -half),
        center + Vector2( half,  half),
        center + Vector2(-half,  half),
    ])
    return poly

## Compute the footprint rect in container-LOCAL coordinates (so the
## border draws correctly when added as a child of the placed object).
## Tile-aligned: anchor tile + footprint_left/right/top/bottom in tile
## counts, all converted to world pixels via TILE_SIZE.
func _footprint_rect_local(node: Node2D) -> Rect2:
    var asset_id: String = node.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    var fL: int = int(asset.get("footprint_left", 0))
    var fR: int = int(asset.get("footprint_right", 0))
    var fT: int = int(asset.get("footprint_top", 0))
    var fB: int = int(asset.get("footprint_bottom", 0))

    # Anchor tile from the container's world position. floor() so the
    # math matches engine/pathfind.go's worldToTile.
    var anchor_tile_x: int = int(floor(node.position.x / TILE_SIZE))
    var anchor_tile_y: int = int(floor(node.position.y / TILE_SIZE))

    var world_left: float = (anchor_tile_x - fL) * TILE_SIZE
    var world_right: float = (anchor_tile_x + fR + 1) * TILE_SIZE
    var world_top: float = (anchor_tile_y - fT) * TILE_SIZE
    var world_bottom: float = (anchor_tile_y + fB + 1) * TILE_SIZE

    return Rect2(
        Vector2(world_left, world_top) - node.position,
        Vector2(world_right - world_left, world_bottom - world_top)
    )

## Hit-test the screen position against the four edges of the selected
## object's footprint border. Returns "left" / "right" / "top" / "bottom"
## or "" if no edge was hit. Slop scales with zoom so the hit area stays
## a consistent size on screen.
func _hit_test_footprint_edge(screen_pos: Vector2, node: Node2D) -> String:
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var rect: Rect2 = _footprint_rect_local(node)
    var origin: Vector2 = node.position
    var left: float = rect.position.x + origin.x
    var right: float = left + rect.size.x
    var top: float = rect.position.y + origin.y
    var bottom: float = top + rect.size.y

    var zoom: float = get_viewport().get_canvas_transform().get_scale().x
    if zoom <= 0.0:
        zoom = 1.0
    var slop: float = FOOTPRINT_EDGE_HIT_PX / zoom

    var on_left: bool = abs(world_pos.x - left) <= slop and world_pos.y >= top - slop and world_pos.y <= bottom + slop
    var on_right: bool = abs(world_pos.x - right) <= slop and world_pos.y >= top - slop and world_pos.y <= bottom + slop
    var on_top: bool = abs(world_pos.y - top) <= slop and world_pos.x >= left - slop and world_pos.x <= right + slop
    var on_bottom: bool = abs(world_pos.y - bottom) <= slop and world_pos.x >= left - slop and world_pos.x <= right + slop

    # Corner overlap is fine — left/right take precedence over top/bottom
    # so corners drag horizontally first. Arbitrary but consistent.
    if on_left:
        return "left"
    if on_right:
        return "right"
    if on_top:
        return "top"
    if on_bottom:
        return "bottom"
    return ""

func _begin_footprint_resize(side: String, screen_pos: Vector2) -> void:
    if selected_object == null:
        return
    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    _footprint_resize_side = side
    _footprint_resize_start_value = int(asset.get("footprint_" + side, 0))
    _footprint_resize_start_world = _screen_to_world(screen_pos)

## Translate the cursor's world-space delta on the dragged axis into a
## tile-count delta, update the local catalog optimistically, and redraw
## the border. PATCH only fires on release so a wiggly drag doesn't
## hammer the server.
func _on_footprint_drag(screen_pos: Vector2) -> void:
    if selected_object == null or _footprint_resize_side == "":
        return
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    # Each side's "outward" direction grows the footprint on that side.
    var delta_world: float
    match _footprint_resize_side:
        "left":   delta_world = _footprint_resize_start_world.x - world_pos.x  # drag west grows
        "right":  delta_world = world_pos.x - _footprint_resize_start_world.x  # drag east grows
        "top":    delta_world = _footprint_resize_start_world.y - world_pos.y  # drag north grows
        "bottom": delta_world = world_pos.y - _footprint_resize_start_world.y  # drag south grows
        _:        return
    var delta_tiles: int = int(round(delta_world / TILE_SIZE))
    var new_value: int = max(0, _footprint_resize_start_value + delta_tiles)

    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    if asset == null or asset.is_empty():
        return
    if int(asset.get("footprint_" + _footprint_resize_side, 0)) == new_value:
        return  # no change since last redraw — skip the work
    asset["footprint_" + _footprint_resize_side] = new_value
    Catalog.assets[asset_id] = asset
    _add_selection_border(selected_object)

func _commit_footprint_resize() -> void:
    if selected_object == null or _footprint_resize_side == "":
        _footprint_resize_side = ""
        return
    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    var payload = JSON.stringify({
        "left":   int(asset.get("footprint_left", 0)),
        "right":  int(asset.get("footprint_right", 0)),
        "top":    int(asset.get("footprint_top", 0)),
        "bottom": int(asset.get("footprint_bottom", 0)),
    })
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    var headers := ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
        # Server broadcast will keep the catalog in sync for everyone else;
        # we already applied the change locally for snappy feedback.
    )
    http.request(Auth.api_base + "/api/assets/" + asset_id + "/footprint",
        headers, HTTPClient.METHOD_PATCH, payload)
    _footprint_resize_side = ""

func _cancel_footprint_resize() -> void:
    if selected_object == null or _footprint_resize_side == "":
        _footprint_resize_side = ""
        return
    # Roll the local catalog back to the value we had at drag start so the
    # border snaps to where the user found it.
    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    asset["footprint_" + _footprint_resize_side] = _footprint_resize_start_value
    Catalog.assets[asset_id] = asset
    _add_selection_border(selected_object)
    _footprint_resize_side = ""

func _remove_selection_border() -> void:
    if _selection_border != null:
        _selection_border.queue_free()
        _selection_border = null

# --- Door marker ---
#
# Per-asset door_offset_x / door_offset_y sets the walkable tile an NPC
# targets when going home. The marker is only shown on structures and only
# while the structure is selected. Dragging it snaps to the tile grid and
# PATCHes /api/assets/{id}/door on release.

## Draw the door marker as a child of the selected structure, or do nothing
## if the asset isn't a structure.
func _add_door_marker(node: Node2D) -> void:
    _remove_door_marker()
    var asset_id: String = node.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    if not bool(asset.get("enterable", false)):
        return

    _door_marker_asset_id = asset_id
    _door_marker = Node2D.new()
    _door_marker.name = "DoorMarker"
    _door_marker.z_index = 1000
    node.add_child(_door_marker)

    var offset_tiles: Vector2 = _current_door_offset_tiles(asset, node)
    _door_marker.position = _door_marker_local_from_tile_offset(node, offset_tiles)
    _draw_door_marker_contents(asset)

## Re-render the door marker's contents. Called after a successful PATCH
## echo so a concurrent admin's change repaints locally. Position is
## driven by the asset catalog — same lookup as the initial draw.
func refresh_door_marker() -> void:
    if selected_object == null or _door_marker == null:
        return
    if _door_dragging:
        # Let the drag finish first; a mid-drag remote change will be
        # reconciled on the next selection anyway.
        return
    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    var offset_tiles: Vector2 = _current_door_offset_tiles(asset, selected_object)
    _door_marker.position = _door_marker_local_from_tile_offset(selected_object, offset_tiles)
    # Clear existing visuals and redraw so the "unset" vs "set" styling updates.
    for child in _door_marker.get_children():
        child.queue_free()
    _draw_door_marker_contents(asset)

## Returns the door offset (in tile units) currently stored on the asset. If
## unset, defaults to (0, +1) — one tile south of anchor — which tends to be
## the "front" for Salem's building sprites. A ghost styling in the marker
## tells the user it's the placeholder, not an intentional value.
func _current_door_offset_tiles(asset: Dictionary, node: Node2D) -> Vector2:
    var ox = asset.get("door_offset_x", null)
    var oy = asset.get("door_offset_y", null)
    if ox == null or oy == null:
        return Vector2(0, int(asset.get("footprint_bottom", 0)) + 1)
    return Vector2(int(ox), int(oy))

## Convert a tile offset from the structure's anchor tile to a LOCAL
## coordinate (relative to the structure node). Positions the marker at
## tile CENTER so the visual dot aligns with the pathfinder's target.
func _door_marker_local_from_tile_offset(node: Node2D, offset_tiles: Vector2) -> Vector2:
    var anchor_tile_x: int = int(floor(node.position.x / TILE_SIZE))
    var anchor_tile_y: int = int(floor(node.position.y / TILE_SIZE))
    var target_tile_x: int = anchor_tile_x + int(offset_tiles.x)
    var target_tile_y: int = anchor_tile_y + int(offset_tiles.y)
    var world_center = Vector2(
        target_tile_x * TILE_SIZE + TILE_SIZE / 2.0,
        target_tile_y * TILE_SIZE + TILE_SIZE / 2.0,
    )
    return world_center - node.position

func _draw_door_marker_contents(_asset: Dictionary) -> void:
    if _door_marker == null:
        return
    var half: float = TILE_SIZE / 2.0 - 2.0
    # Always blue when shown — when enterable=false the marker isn't
    # drawn at all, so a separate "unset" styling is unnecessary. The
    # placeholder position is just a starting point for the admin to
    # drag onto the actual door tile.
    var fill := Polygon2D.new()
    fill.color = Color(0.25, 0.55, 1.0, 0.9)
    fill.polygon = PackedVector2Array([
        Vector2(-half, -half),
        Vector2( half, -half),
        Vector2( half,  half),
        Vector2(-half,  half),
    ])
    _door_marker.add_child(fill)
    var outline := Line2D.new()
    outline.width = 2.0
    outline.default_color = Color(0.15, 0.35, 0.85, 1.0)
    outline.closed = true
    outline.add_point(Vector2(-half, -half))
    outline.add_point(Vector2( half, -half))
    outline.add_point(Vector2( half,  half))
    outline.add_point(Vector2(-half,  half))
    _door_marker.add_child(outline)

func _remove_door_marker() -> void:
    if _door_marker != null:
        _door_marker.queue_free()
        _door_marker = null
    _door_marker_asset_id = ""
    _door_dragging = false

## Hit-test a screen position against the currently visible door marker.
## Returns true if the click landed on the marker so the caller can start
## a drag instead of falling through to normal selection behavior.
func _hit_test_door_marker(screen_pos: Vector2) -> bool:
    if _door_marker == null or selected_object == null:
        return false
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var marker_world: Vector2 = selected_object.position + _door_marker.position
    var half: float = TILE_SIZE / 2.0
    return abs(world_pos.x - marker_world.x) <= half and abs(world_pos.y - marker_world.y) <= half

func _begin_door_drag(screen_pos: Vector2) -> void:
    _door_dragging = true
    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    _door_drag_start_offset = _current_door_offset_tiles(asset, selected_object)

## Snap the dragged marker to the tile under the mouse and update the
## in-memory asset entry so the visual reflects the pending change. The
## actual PATCH fires on release.
func _door_drag_motion(screen_pos: Vector2) -> void:
    if not _door_dragging or selected_object == null or _door_marker == null:
        return
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var anchor_tile_x: int = int(floor(selected_object.position.x / TILE_SIZE))
    var anchor_tile_y: int = int(floor(selected_object.position.y / TILE_SIZE))
    var target_tile_x: int = int(floor(world_pos.x / TILE_SIZE))
    var target_tile_y: int = int(floor(world_pos.y / TILE_SIZE))
    var offset_tiles := Vector2(target_tile_x - anchor_tile_x, target_tile_y - anchor_tile_y)
    _door_marker.position = _door_marker_local_from_tile_offset(selected_object, offset_tiles)
    # Optimistic local update so a redraw renders the "set" styling.
    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    asset["door_offset_x"] = int(offset_tiles.x)
    asset["door_offset_y"] = int(offset_tiles.y)
    Catalog.assets[asset_id] = asset
    for child in _door_marker.get_children():
        child.queue_free()
    _draw_door_marker_contents(asset)

func _commit_door_drag() -> void:
    if not _door_dragging or selected_object == null:
        _door_dragging = false
        return
    _door_dragging = false
    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    var ox = asset.get("door_offset_x", null)
    var oy = asset.get("door_offset_y", null)
    if ox == null or oy == null:
        return
    # No-op if we're back at the starting value (user clicked and released
    # without moving).
    if Vector2(int(ox), int(oy)) == _door_drag_start_offset:
        return
    var payload = JSON.stringify({"x": int(ox), "y": int(oy)})
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var headers := ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request(Auth.api_base + "/api/assets/" + asset_id + "/door",
        headers, HTTPClient.METHOD_PATCH, payload)

func _cancel_door_drag() -> void:
    if not _door_dragging or selected_object == null:
        _door_dragging = false
        return
    var asset_id: String = selected_object.get_meta("asset_id", "")
    var asset = Catalog.assets.get(asset_id, {})
    if _door_drag_start_offset.x == 0 and _door_drag_start_offset.y == int(asset.get("footprint_bottom", 0)) + 1:
        # Started from the placeholder default — restore "unset."
        asset["door_offset_x"] = null
        asset["door_offset_y"] = null
    else:
        asset["door_offset_x"] = int(_door_drag_start_offset.x)
        asset["door_offset_y"] = int(_door_drag_start_offset.y)
    Catalog.assets[asset_id] = asset
    refresh_door_marker()
    _door_dragging = false

func _delete_selected() -> void:
    if selected_object == null:
        return
    _remove_selection_border()
    _remove_door_marker()
    world.remove_object(selected_object)
    selected_object = null
    object_deselected.emit()

func delete_selection() -> void:
    # An NPC takes precedence over an object selection — matches the
    # selection priority in _on_left_press. In practice only one or the
    # other is set at a time.
    if selected_npc != null:
        _delete_selected_npc()
    else:
        _delete_selected()

## DELETE /api/village/npcs/{id}. The npc_deleted WS event handles the
## actual removal from placed_npcs + node cleanup on every client including
## this one, so we just fire the request and clear our local selection.
func _delete_selected_npc() -> void:
    if selected_npc == null:
        return
    var npc_id: String = selected_npc.get_meta("npc_id", "")
    if npc_id == "":
        return
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request(Auth.api_base + "/api/village/npcs/" + npc_id,
        headers, HTTPClient.METHOD_DELETE)
    _deselect_npc()

# --- Drag-to-move ---

func _cancel_drag() -> void:
    if _dragging and selected_object != null:
        # Snap object back to original position
        selected_object.position = _drag_start_obj_pos
    _dragging = false
    _drag_pending = false

func _drag_move(screen_pos: Vector2) -> void:
    if selected_object == null:
        return
    var current_world: Vector2 = _screen_to_world(screen_pos)
    var delta: Vector2 = current_world - _drag_start_world
    var new_pos: Vector2 = _drag_start_obj_pos + delta
    # Force visual refresh — hide, move, show. Without this, the HTML5
    # renderer leaves a ghost of the sprite at the old position.
    selected_object.visible = false
    selected_object.position = new_pos
    selected_object.visible = true

func _drag_end(screen_pos: Vector2) -> void:
    if selected_object == null:
        return
    var current_world: Vector2 = _screen_to_world(screen_pos)
    var delta: Vector2 = current_world - _drag_start_world
    var new_pos: Vector2 = _drag_start_obj_pos + delta
    selected_object.position = new_pos
    world.move_object(selected_object, new_pos)

# --- Terrain painting ---

func set_terrain_type(terrain_type: int) -> void:
    _terrain_type = terrain_type

## Screen-pixel radius of the terrain brush. Kept constant so the cursor
## footprint feels the same physical size no matter how far the camera is
## zoomed — the tile count scales inversely with zoom.
const TERRAIN_BRUSH_SCREEN_RADIUS: float = 18.0

func _paint_terrain_at(screen_pos: Vector2) -> void:
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var zoom: float = camera.zoom.x
    if zoom <= 0:
        zoom = 1.0
    var world_radius: float = TERRAIN_BRUSH_SCREEN_RADIUS / zoom
    var tile_radius: float = world_radius / TILE_SIZE
    # At normal zoom the brush is sub-tile; always paint the center tile so
    # a click at zoom=1 still does something.
    var center_tile: Vector2i = world.world_to_tile(world_pos)
    if tile_radius < 0.5:
        world.paint_terrain(center_tile.x, center_tile.y, _terrain_type)
        return
    # Iterate the square that bounds the circle, keep only tiles whose
    # center is within world_radius of the click. Circle feels more natural
    # than the axis-aligned square wendy would otherwise end up with.
    var tile_reach: int = int(ceil(tile_radius))
    var r2: float = world_radius * world_radius
    for dy in range(-tile_reach, tile_reach + 1):
        for dx in range(-tile_reach, tile_reach + 1):
            var tx: int = center_tile.x + dx
            var ty: int = center_tile.y + dy
            # Distance from click point to the tile's center in world px.
            var tile_center := Vector2(
                float(tx) * TILE_SIZE + TILE_SIZE / 2.0,
                float(ty) * TILE_SIZE + TILE_SIZE / 2.0,
            )
            if tile_center.distance_squared_to(world_pos) <= r2:
                world.paint_terrain(tx, ty, _terrain_type)

func _process(delta: float) -> void:
    if _terrain_save_timer > 0:
        _terrain_save_timer -= delta
        if _terrain_save_timer <= 0:
            world.save_terrain()

# --- Coordinate conversion ---

func _screen_to_world(screen_pos: Vector2) -> Vector2:
    var viewport: Viewport = get_viewport()
    var canvas_transform: Transform2D = viewport.get_canvas_transform()
    return canvas_transform.affine_inverse() * screen_pos
