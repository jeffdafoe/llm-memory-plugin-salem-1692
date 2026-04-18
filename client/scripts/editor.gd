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

enum Mode { SELECT, PLACE, MOVE, TERRAIN }

var current_mode: Mode = Mode.SELECT
var selected_asset_id: String = ""
var selected_object: Node2D = null
var selected_npc: Node2D = null
var ghost_sprite: Sprite2D = null
var active: bool = false

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

# Terrain painting state
var _terrain_type: int = 0
var _terrain_painting: bool = false
var _terrain_save_timer: float = 0.0
const TERRAIN_SAVE_DELAY: float = 2.0

# Drag-to-move state
var _dragging: bool = false
var _drag_start_world: Vector2 = Vector2.ZERO
var _drag_start_obj_pos: Vector2 = Vector2.ZERO
var _drag_threshold: float = 4.0
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

        if event.button_index == MOUSE_BUTTON_RIGHT and event.pressed:
            if current_mode == Mode.PLACE:
                set_mode(Mode.SELECT)
                get_viewport().set_input_as_handled()
            elif current_mode == Mode.SELECT and selected_npc != null:
                _deselect_npc()
                get_viewport().set_input_as_handled()

    if event is InputEventMouseMotion:
        if current_mode == Mode.PLACE and ghost_sprite.visible:
            ghost_sprite.global_position = _screen_to_world(event.position)
            _apply_ghost_offset()

    # Keyboard shortcuts
    if event is InputEventKey and event.pressed:
        if event.keycode == KEY_DELETE and selected_object != null:
            _delete_selected()
            get_viewport().set_input_as_handled()
        if event.keycode == KEY_ESCAPE:
            # In SELECT mode with an NPC selected, Esc deselects the NPC
            # rather than re-running set_mode(SELECT), which would no-op.
            if current_mode == Mode.SELECT and selected_npc != null:
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
                    # Re-click on the same NPC — no-op. Use right-click/Esc to deselect.
                    left_click_used = true
                    get_viewport().set_input_as_handled()
                    return
                _walk_selected_npc(screen_pos)
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
    if new_mode != Mode.SELECT:
        _deselect()
        _deselect_npc()
    _dragging = false
    _drag_pending = false
    _terrain_painting = false
    mode_changed.emit(new_mode)

## Called by the editor panel when the user picks an asset from the catalog.
func select_asset_for_placement(asset_id: String) -> void:
    if asset_id == "":
        set_mode(Mode.SELECT)
        return

    selected_asset_id = asset_id
    current_mode = Mode.PLACE
    _deselect()

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

func _apply_ghost_offset() -> void:
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
    if selected_asset_id == "":
        return
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var placed_node: Node2D = world.add_object(selected_asset_id, world_pos)
    set_mode(Mode.SELECT)
    # Auto-select the newly placed object so attachments show immediately
    if placed_node != null:
        _select_object(placed_node)

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
    object_selected.emit({
        "asset_id": node.get_meta("asset_id", ""),
        "placed_by": node.get_meta("placed_by", ""),
        "owner": node.get_meta("owner", ""),
        "display_name": node.get_meta("display_name", ""),
    })

func _deselect() -> void:
    if selected_object != null:
        _remove_selection_border()
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

func _delete_selected() -> void:
    if selected_object == null:
        return
    _remove_selection_border()
    world.remove_object(selected_object)
    selected_object = null
    object_deselected.emit()

func delete_selection() -> void:
    _delete_selected()

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

func _paint_terrain_at(screen_pos: Vector2) -> void:
    var world_pos: Vector2 = _screen_to_world(screen_pos)
    var tile: Vector2i = world.world_to_tile(world_pos)
    world.paint_terrain(tile.x, tile.y, _terrain_type)

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
