extends Node2D
## Main scene — handles auth flow, bootstraps the village viewer,
## and wires up the editor UI (top bar + side panel).

const TopBarScript = preload("res://scripts/top_bar.gd")
const EditorPanelScript = preload("res://scripts/editor_panel.gd")
const ConfigPanelScript = preload("res://scripts/config_panel.gd")
const AssetPopupScript = preload("res://scripts/asset_popup.gd")
const NPCSpritePickerScript = preload("res://scripts/npc_sprite_picker.gd")
const ObjectTooltipScript = preload("res://scripts/object_tooltip.gd")
const EventClientScript = preload("res://scripts/event_client.gd")
const TalkPanelScript = preload("res://scripts/talk_panel.gd")
const VillageTickerScript = preload("res://scripts/village_ticker.gd")

@onready var world: Node2D = $World
@onready var camera: Camera2D = $Camera
@onready var editor: CanvasLayer = $Editor

# UI elements (created after auth)
var top_bar: PanelContainer = null
var editor_panel: PanelContainer = null
var config_panel: Control = null
var asset_popup: Control = null
var npc_sprite_picker: Control = null
var object_tooltip: CanvasLayer = null
var event_client: Node = null
var talk_panel_layer: CanvasLayer = null
var village_ticker: PanelContainer = null

# Login screen (added as a CanvasLayer so it renders on top of everything)
var login_screen: Control = null
var login_layer: CanvasLayer = null

# PC bootstrap — once we've decided whether the player needs to pick a
# sprite at this login, we don't want to redo the check on every signal
# fire from Auth (auth_ready + logged_in both run on a single verify).
# _pc_bootstrap_done flips after the first /pc/me lands.
var _pc_bootstrap_done: bool = false
var _pc_exists: bool = false
var _pc_http_me: HTTPRequest = null
var _pc_http_save: HTTPRequest = null

# Click-to-walk state. Camera owns left-click pan in play mode, so the
# walk handler can't simply consume the press — it would steal the pan
# gesture. Instead: arm a "walk pending" flag on press, clear it on
# motion past a small threshold (the user was panning), and fire the
# walk on release if the flag's still set (the user clicked without
# dragging). Threshold matches editor.gd's existing _drag_threshold so
# the click feel is uniform.
const _PC_CLICK_DRAG_THRESHOLD: float = 10.0
var _pc_walk_pending: bool = false
var _pc_walk_press_screen: Vector2 = Vector2.ZERO
var _pc_http_move: HTTPRequest = null

func _ready() -> void:
    # Always generate terrain — it's visible behind the login screen
    world.build_terrain()

    # Give camera a reference to the editor for left-click pan coordination
    camera.editor_ref = editor

    # Set camera bounds to match the terrain (2x scaled = 32px per tile)
    # Terrain extends into negative tile coordinates, so bounds start negative.
    # pad_x/pad_y live on World — asymmetric after ZBBS-041 (more space north).
    camera.map_bounds = Rect2(
        -world.pad_x * 32, -world.pad_y * 32,
        world.map_width * 32, world.map_height * 32
    )

    # Center camera on the village crossroads (same position as always)
    camera.position = Vector2(40 * 32.0, 22 * 32.0)

    # Show login screen while checking auth
    var login_scene = load("res://scenes/login_screen.tscn")
    login_layer = CanvasLayer.new()
    login_layer.name = "LoginLayer"
    login_layer.layer = 10  # Above editor UI
    login_screen = login_scene.instantiate()
    login_layer.add_child(login_screen)
    add_child(login_layer)

    # Wait for auth check to complete
    if Auth.authenticated:
        _on_authenticated()
    else:
        Auth.auth_ready.connect(_on_auth_ready)

    # React to mid-session 401s — drop the user back on the login screen.
    Auth.session_expired.connect(_on_session_expired)

func _on_auth_ready() -> void:
    if Auth.authenticated:
        _on_authenticated()
    # If not authenticated, the login screen is already visible

func _on_authenticated() -> void:
    # Hide login screen
    if login_screen != null:
        login_screen.visible = false

    # Connect for future login events (in case token was saved)
    if not Auth.logged_in.is_connected(_on_authenticated):
        Auth.logged_in.connect(_on_authenticated)

    # Build UI if not already created
    if top_bar == null:
        _build_ui()

    # Update username display
    top_bar.set_username(Auth.username)
    top_bar.set_edit_visible(Auth.can_edit)
    top_bar.set_config_visible(Auth.can_edit)
    top_bar.visible = true

    # Connect WebSocket event stream for real-time sync
    if event_client == null:
        event_client = Node.new()
        event_client.set_script(EventClientScript)
        add_child(event_client)
        event_client.reconnected.connect(_on_ws_reconnected)
    event_client.world = world
    world.event_client = event_client
    event_client.connect_to_server()

    # Recover terrain edits that didn't make it to the server (e.g. paints
    # done after a silent session expiry). If there's nothing buffered,
    # re-pull the saved terrain — covers the case where the boot-time
    # _load_terrain ran before the user authenticated.
    _flush_unsaved_terrain_or_reload()

    # PC bootstrap (M6.7): check whether the player has a sprite assigned.
    # If not, pop the sprite picker so they enter the world with a chosen
    # character. Guarded so the duplicate _on_authenticated fires (auth_ready
    # + logged_in both signal on a single verify) don't double-trigger.
    if not _pc_bootstrap_done:
        _pc_bootstrap_done = true
        _bootstrap_pc()

    # Load objects now that we're authenticated. Guard against the duplicate
    # connect — _on_authenticated runs twice on a single verify (auth_ready
    # + logged_in both fire from Auth._on_verify_response).
    if Catalog.loaded:
        _on_catalog_ready()
    elif not Catalog.catalog_loaded.is_connected(_on_catalog_ready):
        Catalog.catalog_loaded.connect(_on_catalog_ready)

## WebSocket reopened after a disconnect (browser tab backgrounded overnight,
## network blip, etc). Any events that fired during the gap — most visibly
## world_phase_changed at dawn/dusk — are gone. Tear down the rendered world
## and refetch everything from REST to match server truth.
func _on_ws_reconnected() -> void:
    if editor != null:
        editor._deselect()
        editor._deselect_npc()
    world.reset_world_state()
    world.reload_terrain()
    world._load_world_phase()
    if Catalog.loaded:
        world.load_objects()

func _flush_unsaved_terrain_or_reload() -> void:
    var pending = world.get_unsaved_terrain()
    if pending is Dictionary:
        world.restore_unsaved_terrain(pending)
    else:
        world.reload_terrain()

func _on_catalog_ready() -> void:
    world.load_objects()
    # Build catalog in editor panel now that assets are loaded
    if editor_panel != null:
        editor_panel.build_catalog()
        # Push the object-tag allowlist — drives both the social-hour tag
        # dropdown and the per-instance tag editor. If tags are already
        # loaded, push immediately; otherwise subscribe for the one-shot.
        if Catalog.object_tags_loaded_flag:
            editor_panel.set_social_tag_options(Catalog.object_tags)
        elif not Catalog.object_tags_loaded.is_connected(_on_object_tags_loaded):
            Catalog.object_tags_loaded.connect(_on_object_tags_loaded)
    # Tag mutations broadcast back via this world signal — forward to the
    # panel so the chips refresh in place.
    if world != null and editor_panel != null:
        if not world.object_tags_updated.is_connected(_on_object_tags_updated_from_world):
            world.object_tags_updated.connect(_on_object_tags_updated_from_world)

func _on_object_tags_loaded() -> void:
    if editor_panel != null:
        editor_panel.set_social_tag_options(Catalog.object_tags)

func _on_object_tags_updated_from_world(object_id: String, tags: Array) -> void:
    if editor_panel != null:
        editor_panel.apply_object_tags_external(object_id, tags)
    # Loiter marker styling no longer depends on tags, but repaint anyway
    # so any future tag-driven decoration stays in sync.
    if editor != null and editor.selected_object != null:
        if editor.selected_object.get_meta("object_id", "") == object_id:
            if editor.has_method("refresh_loiter_marker"):
                editor.refresh_loiter_marker()

func _build_ui() -> void:
    # Top bar — lives on the editor CanvasLayer
    # Set script before adding to tree so _ready() fires correctly
    top_bar = PanelContainer.new()
    top_bar.set_script(TopBarScript)
    editor.add_child(top_bar)

    # Wire top bar signals after it's in the tree and _ready has run
    top_bar.edit_toggled.connect(_on_edit_toggled)
    top_bar.config_pressed.connect(_on_config_pressed)
    top_bar.logout_pressed.connect(_on_logout)

    # Config panel — full-screen overlay on a higher CanvasLayer
    var config_layer = CanvasLayer.new()
    config_layer.name = "ConfigLayer"
    config_layer.layer = 5  # Above editor, below login
    get_parent().add_child(config_layer)

    config_panel = Control.new()
    config_panel.set_script(ConfigPanelScript)
    config_layer.add_child(config_panel)
    config_panel.visible = false
    config_panel.closed.connect(func(): camera.modal_open = false)

    # Editor side panel — also on the editor CanvasLayer, hidden by default
    editor_panel = PanelContainer.new()
    editor_panel.set_script(EditorPanelScript)
    editor.add_child(editor_panel)
    editor_panel.visible = false
    # Camera queries each registered panel's rect at hit-test time. The
    # editor sidebar is wider when an NPC is selected (extra controls), so
    # registering the live Control beats hardcoding a width — and any
    # future panel just calls camera.register_ui_panel(self) the same way.
    camera.register_ui_panel(editor_panel)
    editor.editor_panel_ref = editor_panel

    # Asset inspect popup — on the config layer (above editor)
    asset_popup = Control.new()
    asset_popup.set_script(AssetPopupScript)
    config_layer.add_child(asset_popup)
    asset_popup.visible = false
    asset_popup.place_requested.connect(_on_popup_place_requested)
    asset_popup.closed.connect(func():
        camera.modal_open = false
        editor.popup_open = false
    )

    # NPC sprite picker — modal overlay above the editor. Same layer as the
    # asset popup; ownership of camera/editor input flags handled symmetrically.
    # Doubles as the PC sprite picker via show_for_pc — see pc_sprite_selected
    # signal wired below.
    npc_sprite_picker = Control.new()
    npc_sprite_picker.set_script(NPCSpritePickerScript)
    npc_sprite_picker.world = world
    config_layer.add_child(npc_sprite_picker)
    npc_sprite_picker.visible = false
    npc_sprite_picker.sprite_selected.connect(_on_npc_sprite_picker_selected)
    npc_sprite_picker.pc_sprite_selected.connect(_on_pc_sprite_picker_selected)
    npc_sprite_picker.closed.connect(func():
        camera.modal_open = false
        editor.popup_open = false
    )

    # Object tooltip — shows owner info on hover when not in edit mode
    object_tooltip = CanvasLayer.new()
    object_tooltip.set_script(ObjectTooltipScript)
    object_tooltip.world = world
    object_tooltip.editor = editor
    add_child(object_tooltip)

    # Talk panel (M6.7) — bottom drawer summoned by a "Talk" launcher pill.
    # The script extends CanvasLayer and owns its own layer ordering, mouse
    # filtering, and visibility — main.gd only needs to instantiate it.
    talk_panel_layer = CanvasLayer.new()
    talk_panel_layer.name = "TalkPanelLayer"
    talk_panel_layer.set_script(TalkPanelScript)
    add_child(talk_panel_layer)

    # Register the talk panel's sheet (the actual rounded-rect chat
    # surface, not the full-screen anchor) so wheel-scroll over the
    # open chat scrolls the log instead of zooming the map. Sheet
    # visibility is parent-driven (sheet_anchor.visible toggles); the
    # camera's is_visible_in_tree() check handles the open/closed state
    # automatically without re-registering.
    if talk_panel_layer.has_method("get_input_eating_control"):
        var sheet: Control = talk_panel_layer.get_input_eating_control()
        if sheet != null:
            camera.register_ui_panel(sheet)

    # Village ticker — thin marquee band below the top bar, scrolling
    # chronicler atmosphere prose. Lives on the editor CanvasLayer
    # alongside top_bar so it inherits the same z-order. Click → opens
    # the talk panel to its Village tab. attach_world() must run after
    # event_client.world is set so the ticker can hook the
    # world_environment_added signal that event_client emits via world.
    village_ticker = PanelContainer.new()
    village_ticker.set_script(VillageTickerScript)
    editor.add_child(village_ticker)
    village_ticker.attach_world(world)
    village_ticker.clicked.connect(_on_village_ticker_clicked)
    camera.register_ui_panel(village_ticker)

    # Wire panel signals to editor
    editor_panel.asset_selected.connect(_on_panel_asset_selected)
    editor_panel.asset_inspect_requested.connect(_on_asset_inspect_requested)
    editor_panel.delete_requested.connect(_on_panel_delete)
    editor_panel.terrain_mode_toggled.connect(_on_terrain_mode_toggled)
    editor_panel.terrain_type_selected.connect(_on_terrain_type_selected)
    editor_panel.owner_changed.connect(_on_owner_changed)
    editor_panel.display_name_changed.connect(_on_display_name_changed)
    editor_panel.attachment_requested.connect(_on_attachment_requested)
    editor_panel.npc_sprite_selected.connect(_on_panel_npc_sprite_selected)
    editor_panel.npc_name_changed.connect(_on_npc_name_changed)
    editor_panel.npc_behavior_changed.connect(_on_npc_behavior_changed)
    editor_panel.npc_agent_changed.connect(_on_npc_agent_changed)
    editor_panel.npc_home_structure_changed.connect(_on_npc_home_structure_changed)
    editor_panel.npc_work_structure_changed.connect(_on_npc_work_structure_changed)
    editor_panel.npc_schedule_changed.connect(_on_npc_schedule_changed)
    editor_panel.npc_social_changed.connect(_on_npc_social_changed)
    editor_panel.npc_home_assign_requested.connect(_on_npc_home_assign_requested)
    editor_panel.npc_work_assign_requested.connect(_on_npc_work_assign_requested)
    editor_panel.npc_run_cycle_requested.connect(_on_npc_run_cycle_requested)
    editor_panel.npc_reset_needs_requested.connect(_on_npc_reset_needs_requested)
    editor_panel.npc_heal_requested.connect(_on_npc_heal_requested)
    editor_panel.npc_go_home_requested.connect(_on_npc_go_home_requested)
    editor_panel.npc_go_to_work_requested.connect(_on_npc_go_to_work_requested)
    editor_panel.npc_select_requested.connect(_on_npc_select_requested)
    editor_panel.npc_sprite_change_requested.connect(_on_npc_sprite_change_requested)
    editor_panel.entry_policy_changed.connect(_on_entry_policy_changed)
    editor_panel.asset_visible_when_inside_toggled.connect(_on_asset_visible_when_inside_toggled)
    editor_panel.world = world

    # Wire editor signals to panel
    editor.object_selected.connect(_on_editor_object_selected)
    editor.object_deselected.connect(_on_editor_object_deselected)
    editor.npc_selected.connect(_on_editor_npc_selected)
    editor.npc_deselected.connect(_on_editor_npc_deselected)
    world.npc_metadata_changed.connect(_on_npc_metadata_changed)
    # Villagers browser refreshes on both list changes (create/delete)
    # and per-NPC metadata changes (rename / behavior / inside-flip).
    world.npc_list_changed.connect(_on_villagers_list_should_refresh)
    world.npc_metadata_changed.connect(func(_id): _on_villagers_list_should_refresh())
    editor.mode_changed.connect(_on_editor_mode_changed)
    # Cursor tile readout — editor emits on mouse motion over the map,
    # top bar renders the coords. Hidden whenever edit mode turns off.
    editor.cursor_tile_changed.connect(_on_cursor_tile_changed)

func _on_config_pressed() -> void:
    if config_panel != null:
        config_panel.visible = not config_panel.visible
        camera.modal_open = config_panel.visible

# Click on the village ticker → open the talk panel to its Village tab.
# Bypasses the room-huddle gate the talk_launcher uses since the Village
# tab is universally available.
func _on_village_ticker_clicked() -> void:
    if talk_panel_layer != null and talk_panel_layer.has_method("force_open_to_village_tab"):
        talk_panel_layer.force_open_to_village_tab()

func _on_edit_toggled(active: bool) -> void:
    editor_panel.visible = active
    editor.active = active
    camera.editor_active = active
    if top_bar != null:
        top_bar.set_cursor_tile_visible(active)
    if not active:
        # Clear both kinds of selection — object AND NPC — so
        # re-entering edit mode lands on the default browse view
        # (Catalog/Villagers tab) rather than the inspector for
        # whatever was last selected.
        editor._deselect()
        editor._deselect_npc()
        editor.set_mode(editor.Mode.SELECT)

func _on_logout() -> void:
    Auth.logout()
    _show_login_screen("")

## Called when the server rejects our token mid-session. Reuses the logout
## UI flip and surfaces a message so the user knows why they're back here.
func _on_session_expired() -> void:
    _show_login_screen("Session expired — please log in again.")

func _show_login_screen(message: String) -> void:
    if top_bar != null:
        top_bar.set_edit_visible(false)
        top_bar.set_config_visible(false)
        top_bar.visible = false
    if editor_panel != null:
        editor_panel.visible = false
    if config_panel != null:
        config_panel.visible = false
    editor.active = false
    camera.editor_active = false
    if login_screen != null:
        login_screen.visible = true
        if login_screen.has_method("set_message"):
            login_screen.set_message(message)

func _on_asset_inspect_requested(asset_id: String) -> void:
    if asset_popup != null:
        asset_popup.show_asset(asset_id)
        camera.modal_open = true
        editor.popup_open = true

func _on_popup_place_requested(asset_id: String) -> void:
    camera.modal_open = false
    editor.popup_open = false
    editor.select_asset_for_placement(asset_id)

func _on_panel_asset_selected(asset_id: String) -> void:
    editor.select_asset_for_placement(asset_id)

func _on_panel_npc_sprite_selected(sprite: Dictionary, sheet: Texture2D, npc_name: String) -> void:
    editor.select_npc_sprite_for_placement(sprite, sheet, npc_name)

func _on_panel_delete() -> void:
    editor.delete_selection()

func _on_editor_object_selected(info: Dictionary) -> void:
    if editor_panel != null:
        # Selecting a non-NPC object clears the Villagers list highlight.
        editor_panel.sync_villager_selection("")
        editor_panel.show_selection(info)

func _on_editor_object_deselected() -> void:
    if editor_panel != null:
        editor_panel.show_selection({})

func _on_editor_npc_selected(info: Dictionary) -> void:
    if editor_panel != null:
        editor_panel.show_npc_selection(info)

func _on_editor_npc_deselected() -> void:
    if editor_panel != null:
        editor_panel.show_npc_selection({})

func _on_owner_changed(owner: String) -> void:
    if editor.selected_object != null:
        world.set_object_owner(editor.selected_object, owner)

func _on_display_name_changed(display_name: String, object_id: String) -> void:
    # Route via the id rather than editor.selected_object: deselection
    # hides the panel which triggers focus_exited on the name input, and
    # by then selected_object is already null. The id keeps the save
    # pointed at the right object.
    if object_id == "" or not world.placed_objects.has(object_id):
        return
    var node: Node2D = world.placed_objects[object_id]
    world.set_object_display_name(node, display_name)

func _on_npc_name_changed(display_name: String) -> void:
    if editor.selected_npc != null:
        world.set_npc_display_name(editor.selected_npc, display_name)

func _on_npc_behavior_changed(behavior: String) -> void:
    if editor.selected_npc != null:
        world.set_npc_behavior(editor.selected_npc, behavior)

func _on_npc_agent_changed(agent: String) -> void:
    if editor.selected_npc != null:
        world.set_npc_agent(editor.selected_npc, agent)

func _on_npc_home_structure_changed(structure_id: String) -> void:
    if editor.selected_npc != null:
        world.set_npc_home_structure(editor.selected_npc, structure_id)

func _on_npc_work_structure_changed(structure_id: String) -> void:
    if editor.selected_npc != null:
        world.set_npc_work_structure(editor.selected_npc, structure_id)

## Admin edited the schedule fields and hit Save. start_min/end_min are -1
## when the work-window is NULL-inheriting dawn/dusk; interval/start/end are
## -1 when the cadence checkbox is unchecked — world.set_npc_schedule maps
## both -1 sentinels to null in the JSON payload.
func _on_npc_schedule_changed(start_min: int, end_min: int, interval: int, start_h: int, end_h: int, lateness: int) -> void:
    if editor.selected_npc != null:
        world.set_npc_schedule(editor.selected_npc, start_min, end_min, interval, start_h, end_h, lateness)

## Social-hour schedule changed (ZBBS-068, minute precision since ZBBS-071).
## Empty tag clears the schedule (start_min/end_min ignored in that case).
func _on_npc_social_changed(tag: String, start_min: int, end_min: int) -> void:
    if editor.selected_npc != null:
        world.set_npc_social(editor.selected_npc, tag, start_min, end_min)

func _on_npc_home_assign_requested() -> void:
    editor.begin_assign_home()

func _on_npc_work_assign_requested() -> void:
    editor.begin_assign_work()

## Admin clicked "Run Cycle" on the selected villager. Fires the behavior
## route on demand, bypassing the time-of-day schedule. The server decides
## what a "cycle" means per behavior (lamplighter uses current world phase;
## washerwoman/town_crier trigger their rotation walk).
func _on_npc_run_cycle_requested() -> void:
    if editor.selected_npc == null:
        return
    var npc_id: String = editor.selected_npc.get_meta("npc_id", "")
    if npc_id == "":
        return
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var headers := Auth.auth_headers()
    http.request(Auth.api_base + "/api/village/npcs/" + npc_id + "/run-cycle",
        headers, HTTPClient.METHOD_POST, "{}")
    # Once a cycle is kicked off, the admin is typically done with this
    # villager for a while — deselect so the map / structures are clickable
    # again without right-clicking first.
    editor._deselect_npc()

## Admin pressed "Top up needs" on the selected NPC. Zeroes hunger,
## thirst, tiredness via the engine's reset-needs route. The server
## broadcasts npc_needs_changed which world.apply_npc_needs_changed
## picks up, refreshing the panel readout — no need to refresh here.
## Selection is preserved so the admin can verify the values dropped.
func _on_npc_reset_needs_requested() -> void:
    if editor.selected_npc == null:
        return
    var npc_id: String = editor.selected_npc.get_meta("npc_id", "")
    if npc_id == "":
        return
    _post_reset_needs(npc_id)

## Per-row heal click from the villager browser. Same endpoint as the
## selection panel's "Top up needs" button — kept separate so the row
## click can carry its own npc_id without touching selection state.
func _on_npc_heal_requested(npc_id: String) -> void:
    if npc_id == "":
        return
    _post_reset_needs(npc_id)

func _post_reset_needs(npc_id: String) -> void:
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var headers := Auth.auth_headers()
    http.request(Auth.api_base + "/api/village/npcs/" + npc_id + "/reset-needs",
        headers, HTTPClient.METHOD_POST, "{}")

## Admin chose a new entry policy for the selected placement (ZBBS-101).
## PATCH /api/village/objects/{id}/entry-policy → server validates and
## broadcasts object_entry_policy_changed back to every connected client.
## A 400 (e.g. trying to set 'owner' on a structure with no associated
## actor) is surfaced as an alert and the dropdown reverts on the next
## show_selection.
func _on_entry_policy_changed(object_id: String, policy: String) -> void:
    var payload = JSON.stringify({"entry_policy": policy})
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, body):
        http.queue_free()
        if c >= 200 and c < 300:
            return
        Auth.check_response(c)
        # Server refused — likely the no-associated-actor guard. Surface
        # the message so the admin understands why nothing changed.
        var msg: String = "Entry policy change failed."
        var parsed = JSON.parse_string(body.get_string_from_utf8())
        if parsed is Dictionary and parsed.has("error"):
            msg = str(parsed.get("error"))
        OS.alert(msg, "Entry policy")
    )
    var headers := Auth.auth_headers()
    http.request(Auth.api_base + "/api/village/objects/" + object_id + "/entry-policy",
        headers, HTTPClient.METHOD_PATCH, payload)

## Admin toggled the "Visible when inside" dropdown — controls whether
## an NPC's sprite hides on inside=true or stays rendered at the door.
func _on_asset_visible_when_inside_toggled(asset_id: String, visible: bool) -> void:
    _patch_asset_flag(asset_id, "visible-when-inside", "visible_when_inside", visible)

func _patch_asset_flag(asset_id: String, path_suffix: String, field: String, value: bool) -> void:
    var payload = JSON.stringify({field: value})
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var headers := Auth.auth_headers()
    http.request(Auth.api_base + "/api/assets/" + asset_id + "/" + path_suffix,
        headers, HTTPClient.METHOD_PATCH, payload)

## Panel People list clicked. Selects the villager (even when hidden
## indoors) and pans the camera to them so the admin can see where the
## villager lives/works.
func _on_npc_select_requested(npc_id: String) -> void:
    if npc_id == "" or not world.placed_npcs.has(npc_id):
        return
    var container: Node2D = world.placed_npcs[npc_id]
    editor._select_npc(container)
    # global_position rather than position so we don't miss any parent
    # transform (Objects node, y-sort, etc) that would make raw position
    # local-only.
    camera.center_on(container.global_position)

## Editor panel's "Change…" button on the SPRITE row was clicked. Open the
## modal picker for this NPC, highlighting the current sprite.
func _on_npc_sprite_change_requested(npc_id: String, current_sprite_id: String) -> void:
    if npc_sprite_picker == null:
        return
    npc_sprite_picker.show_for_npc(npc_id, current_sprite_id)
    camera.modal_open = true
    editor.popup_open = true

## Picker emitted a selection. PATCH the NPC's sprite_id and let the WS
## broadcast (npc_sprite_changed) drive the visual swap on every client,
## including this one. No need to update local state imperatively.
func _on_npc_sprite_picker_selected(npc_id: String, sprite_id: String) -> void:
    if npc_id == "" or sprite_id == "":
        return
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var headers := Auth.auth_headers()
    var payload: String = JSON.stringify({"sprite_id": sprite_id})
    http.request(Auth.api_base + "/api/village/npcs/" + npc_id + "/sprite",
        headers, HTTPClient.METHOD_PATCH, payload)

## PC bootstrap (M6.7): kick off /pc/me. The response either tells us
## the PC has a sprite (nothing to do) or that they need to pick one
## (open the picker in PC mode). _pc_http_me lives on this node so the
## response handler closes over the right reference.
func _bootstrap_pc() -> void:
    if _pc_http_me == null:
        _pc_http_me = HTTPRequest.new()
        _pc_http_me.accept_gzip = false
        add_child(_pc_http_me)
        _pc_http_me.request_completed.connect(_on_pc_me_completed)
    var headers := Auth.auth_headers()
    var err := _pc_http_me.request(Auth.api_base + "/api/village/pc/me",
        headers, HTTPClient.METHOD_POST, "")
    if err != OK:
        push_warning("PC bootstrap /pc/me request failed: %s" % err)

## /pc/me response. Branch on whether the PC exists at all and whether
## a sprite has been chosen. Either gap → open the picker. The
## picker's pc_sprite_selected signal drives the right save call
## (create vs sprite-only) based on _pc_exists.
func _on_pc_me_completed(result: int, code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    if not Auth.check_response(code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or code < 200 or code >= 300:
        push_warning("PC bootstrap /pc/me failed: code=%s" % code)
        return
    var data = JSON.parse_string(body.get_string_from_utf8())
    if typeof(data) != TYPE_DICTIONARY:
        return
    _pc_exists = bool(data.get("exists", false))
    var current_sprite_id := str(data.get("sprite_id", ""))
    if current_sprite_id != "":
        # PC already has a sprite — nothing to bootstrap. They'll see
        # themselves on the map (A2) and can change the sprite later via
        # a future settings affordance.
        return
    if npc_sprite_picker == null:
        # Picker hasn't been instantiated yet (UI build raced ahead of
        # bootstrap). Defer until the picker exists. In practice
        # _build_ui runs synchronously before _bootstrap_pc, so this is
        # a defensive guard rather than an expected branch.
        return
    npc_sprite_picker.show_for_pc(current_sprite_id)
    camera.modal_open = true
    editor.popup_open = true

## PC mode picker emitted a selection. Branch on _pc_exists:
##   - false: POST /pc/create with character_name + sprite_id (one-shot
##            creation; default character_name to the auth username
##            until a name-input UI lands).
##   - true:  POST /pc/sprite with just sprite_id.
## Either way, the WS broadcast (pc_sprite_changed) drives the visual
## update on every connected client once A2 wires rendering. After the
## save completes, re-fetch /pc/me so any local PC state we cache is
## current.
func _on_pc_sprite_picker_selected(sprite_id: String) -> void:
    if sprite_id == "":
        return
    if _pc_http_save == null:
        _pc_http_save = HTTPRequest.new()
        _pc_http_save.accept_gzip = false
        add_child(_pc_http_save)
        _pc_http_save.request_completed.connect(_on_pc_save_completed)
    var headers := Auth.auth_headers()
    var url: String
    var payload: String
    if _pc_exists:
        url = Auth.api_base + "/api/village/pc/sprite"
        payload = JSON.stringify({"sprite_id": sprite_id})
    else:
        url = Auth.api_base + "/api/village/pc/create"
        payload = JSON.stringify({
            "character_name": Auth.username,
            "sprite_id": sprite_id,
        })
    var err := _pc_http_save.request(url, headers, HTTPClient.METHOD_POST, payload)
    if err != OK:
        push_warning("PC sprite save request failed: %s" % err)

func _on_pc_save_completed(result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray) -> void:
    if not Auth.check_response(code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or code < 200 or code >= 300:
        push_warning("PC sprite save failed: code=%s" % code)
        return
    # _pc_exists flips true post-create so a subsequent picker open
    # (e.g., user re-opens to change sprite) routes through /pc/sprite
    # instead of re-creating.
    _pc_exists = true
    # Re-fetch /pc/me so any cached state (A2 rendering will need it)
    # picks up the new sprite. Cheap; one round-trip.
    _bootstrap_pc_refetch()

## Re-fire /pc/me after a save, without re-running the bootstrap-done
## guard. _bootstrap_pc itself is idempotent (lazy-creates _pc_http_me),
## so this is just a clean alias call.
func _bootstrap_pc_refetch() -> void:
    if _pc_http_me == null:
        _bootstrap_pc()
        return
    var headers := Auth.auth_headers()
    var err := _pc_http_me.request(Auth.api_base + "/api/village/pc/me",
        headers, HTTPClient.METHOD_POST, "")
    if err != OK:
        push_warning("PC bootstrap /pc/me refetch failed: %s" % err)

## Click-to-walk in play mode. Edit mode and modal-open states are
## owned by editor.gd / config_panel / sprite_picker respectively, so
## this handler steps aside in those cases. The walk-pending state
## machine handles the camera-pan vs walk-click ambiguity; see the
## _PC_CLICK_DRAG_THRESHOLD comment block above.
func _input(event: InputEvent) -> void:
    if not _pc_exists:
        return
    if editor != null and editor.active:
        return
    if camera != null and camera.modal_open:
        return
    if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT:
        if event.pressed:
            # Skip clicks over UI (sidebar, top bar, talk panel sheet).
            # Camera's _is_over_ui maintains the panel registry; we
            # piggyback on it rather than duplicating the rect math.
            if camera != null and camera._is_over_ui(event.position):
                return
            _pc_walk_pending = true
            _pc_walk_press_screen = event.position
        else:
            # Release. If the press wasn't cancelled by a drag, fire walk.
            if _pc_walk_pending:
                _pc_walk_pending = false
                _post_pc_move_to_screen(event.position)
    elif event is InputEventMouseMotion:
        if _pc_walk_pending:
            if event.position.distance_to(_pc_walk_press_screen) > _PC_CLICK_DRAG_THRESHOLD:
                _pc_walk_pending = false

## Convert the screen-space click into a walk request. Two payloads:
##
##   - Click landed on a structure → send {target_structure_id}. Server
##     resolves the door tile (entry allowed) or loiter slot (knock or none)
##     and flips inside_structure_id on arrival, which is what hooks
##     the talk panel's huddle gate. Without this branch the PC slides
##     up to the side of a building and never registers as inside.
##
##   - Click landed on open ground → send {target_x, target_y}. Walk to
##     the tile, no inside flip. Same as before structure routing.
##
## World coordinates come from world.get_global_mouse_position so camera
## pan / zoom are baked in. Lazy-create the HTTPRequest so we don't
## allocate one until the player actually clicks.
func _post_pc_move_to_screen(screen_pos: Vector2) -> void:
    if world == null:
        return
    if _pc_http_move == null:
        _pc_http_move = HTTPRequest.new()
        _pc_http_move.accept_gzip = false
        add_child(_pc_http_move)
        _pc_http_move.request_completed.connect(func(_r, c, _h, b):
            Auth.check_response(c)
            if c < 200 or c >= 300:
                return
            # Knock outcome (ZBBS-101): the server resolved the structure
            # click as a knock and either (a) joined the PC into a service
            # huddle with the vendor inside, or (b) reported the structure
            # as unattended. Case (a) needs an immediate /pc/me poll so the
            # talk panel pops open right away with the vendor as an
            # addressee — without it the player waits up to 10s for the
            # next refresh tick. Case (b) renders narration in the panel
            # log so the player understands why the click went nowhere.
            var parsed = JSON.parse_string(b.get_string_from_utf8())
            if parsed is Dictionary and bool(parsed.get("knocked", false)):
                if bool(parsed.get("huddle_joined", false)):
                    if talk_panel_layer != null and talk_panel_layer.has_method("_refresh_state"):
                        talk_panel_layer._refresh_state()
                else:
                    var narration: String = str(parsed.get("knock_narration", ""))
                    if narration != "" and talk_panel_layer != null and talk_panel_layer.has_method("append_local_narration"):
                        talk_panel_layer.append_local_narration(narration)
        )
    var headers := Auth.auth_headers()
    var hit: Dictionary = world.find_object_at(screen_pos)
    var payload: String
    if hit.has("id") and str(hit.get("id", "")) != "":
        payload = JSON.stringify({"target_structure_id": str(hit["id"])})
    else:
        var world_pos: Vector2 = world.get_global_mouse_position()
        payload = JSON.stringify({
            "target_x": world_pos.x,
            "target_y": world_pos.y,
        })
    var err := _pc_http_move.request(Auth.api_base + "/api/village/pc/move",
        headers, HTTPClient.METHOD_POST, payload)
    if err != OK:
        push_warning("PC move request failed: %s" % err)

func _on_npc_go_home_requested() -> void:
    _post_npc_action("go-home")

func _on_npc_go_to_work_requested() -> void:
    _post_npc_action("go-to-work")

## Shared POST body for the three selected-NPC action buttons (go-home,
## go-to-work, run-cycle). Reads the selected NPC's id, fires a fire-and-
## forget POST to /api/village/npcs/{id}/{action}.
func _post_npc_action(action: String) -> void:
    if editor.selected_npc == null:
        return
    var npc_id: String = editor.selected_npc.get_meta("npc_id", "")
    if npc_id == "":
        return
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, _b):
        http.queue_free()
        Auth.check_response(c)
    )
    var headers := Auth.auth_headers()
    http.request(Auth.api_base + "/api/village/npcs/" + npc_id + "/" + action,
        headers, HTTPClient.METHOD_POST, "{}")

# Refresh the selection panel if the changed NPC is the one we have selected.
# Handles the cross-admin case: another admin edits the NPC while we have it
# open. Our own PATCH also comes through this path (idempotent).
func _on_npc_metadata_changed(npc_id: String) -> void:
    if editor.selected_npc == null or editor_panel == null:
        return
    var selected_id: String = editor.selected_npc.get_meta("npc_id", "")
    if selected_id != npc_id:
        return
    # Keep this in sync with editor._select_npc — show_npc_selection reads
    # whatever keys are present and defaults missing ones to 0 / null, so
    # forgetting a field here made the SpinBox reset to 0 after every
    # successful Save Schedule PATCH. Any new NPC field the panel renders
    # MUST be included here too.
    var container: Node2D = editor.selected_npc
    var info := {
        "npc_id": npc_id,
        "sprite_id": container.get_meta("sprite_id", ""),
        "sprite_name": container.get_meta("sprite_name", ""),
        "display_name": container.get_meta("display_name", ""),
        "behavior": container.get_meta("behavior", ""),
        "llm_memory_agent": container.get_meta("llm_memory_agent", ""),
        "home_structure_id": container.get_meta("home_structure_id", ""),
        "work_structure_id": container.get_meta("work_structure_id", ""),
        "lateness_window_minutes": container.get_meta("lateness_window_minutes", 0),
    }
    # Worker work-window: present only when the NPC has overridden the
    # global dawn/dusk default. Missing keys signal "inherit" to the panel.
    if container.has_meta("schedule_start_minute"):
        info["schedule_start_minute"] = container.get_meta("schedule_start_minute")
        info["schedule_end_minute"] = container.get_meta("schedule_end_minute")
    if container.has_meta("schedule_interval_hours"):
        info["schedule_interval_hours"] = container.get_meta("schedule_interval_hours")
        info["active_start_hour"] = container.get_meta("active_start_hour")
        info["active_end_hour"] = container.get_meta("active_end_hour")
    if container.has_meta("social_tag"):
        info["social_tag"] = container.get_meta("social_tag")
        info["social_start_minute"] = container.get_meta("social_start_minute")
        info["social_end_minute"] = container.get_meta("social_end_minute")
    info["hunger"] = int(container.get_meta("hunger", 0))
    info["thirst"] = int(container.get_meta("thirst", 0))
    info["tiredness"] = int(container.get_meta("tiredness", 0))
    editor_panel.show_npc_selection(info)

## Rebuild the Villagers list when it's actually visible. Wired to
## world.npc_list_changed (create/delete) and npc_metadata_changed
## (rename / behavior / inside-flip). No-op when the tab isn't open —
## the list rebuilds on activation anyway.
func _on_villagers_list_should_refresh(_ignored = null) -> void:
    if editor_panel == null:
        return
    if editor_panel._villagers_scroll != null and editor_panel._villagers_scroll.visible:
        editor_panel.rebuild_villagers_list()

## Editor reports the tile under the cursor; top bar renders it.
func _on_cursor_tile_changed(tile_x: int, tile_y: int) -> void:
    if top_bar != null:
        top_bar.set_cursor_tile(tile_x, tile_y)

func _on_attachment_requested(overlay_asset_id: String) -> void:
    if editor.selected_object != null:
        world.add_attachment(overlay_asset_id, editor.selected_object)

func _on_terrain_mode_toggled(active: bool) -> void:
    if active:
        editor.set_mode(editor.Mode.TERRAIN)
    else:
        editor.set_mode(editor.Mode.SELECT)

func _on_terrain_type_selected(terrain_type: int) -> void:
    editor.set_terrain_type(terrain_type)

func _on_editor_mode_changed(mode) -> void:
    if editor_panel == null:
        return
    # When editor exits place mode (escape, right-click), clear catalog selection
    if mode == editor.Mode.SELECT:
        editor_panel.clear_catalog_selection()
        editor_panel.exit_terrain_mode()
    # Drive the home/work picker button labels so the user sees the hint
    # "Click a structure (Esc)" while in assign mode and snaps back to the
    # current structure name when they exit.
    editor_panel.set_assigning_home(mode == editor.Mode.ASSIGN_HOME)
    editor_panel.set_assigning_work(mode == editor.Mode.ASSIGN_WORK)

