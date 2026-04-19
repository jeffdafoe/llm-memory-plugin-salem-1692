extends Node2D
## Main scene — handles auth flow, bootstraps the village viewer,
## and wires up the editor UI (top bar + side panel).

const TopBarScript = preload("res://scripts/top_bar.gd")
const EditorPanelScript = preload("res://scripts/editor_panel.gd")
const ConfigPanelScript = preload("res://scripts/config_panel.gd")
const AssetPopupScript = preload("res://scripts/asset_popup.gd")
const ObjectTooltipScript = preload("res://scripts/object_tooltip.gd")
const EventClientScript = preload("res://scripts/event_client.gd")

@onready var world: Node2D = $World
@onready var camera: Camera2D = $Camera
@onready var editor: CanvasLayer = $Editor

# UI elements (created after auth)
var top_bar: PanelContainer = null
var editor_panel: PanelContainer = null
var config_panel: Control = null
var asset_popup: Control = null
var object_tooltip: CanvasLayer = null
var event_client: Node = null

# Login screen (added as a CanvasLayer so it renders on top of everything)
var login_screen: Control = null
var login_layer: CanvasLayer = null

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

    # Object tooltip — shows owner info on hover when not in edit mode
    object_tooltip = CanvasLayer.new()
    object_tooltip.set_script(ObjectTooltipScript)
    object_tooltip.world = world
    object_tooltip.editor = editor
    add_child(object_tooltip)

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
    editor_panel.npc_home_assign_requested.connect(_on_npc_home_assign_requested)
    editor_panel.npc_work_assign_requested.connect(_on_npc_work_assign_requested)
    editor_panel.npc_run_cycle_requested.connect(_on_npc_run_cycle_requested)
    editor_panel.npc_go_home_requested.connect(_on_npc_go_home_requested)
    editor_panel.npc_go_to_work_requested.connect(_on_npc_go_to_work_requested)
    editor_panel.world = world

    # Wire editor signals to panel
    editor.object_selected.connect(_on_editor_object_selected)
    editor.object_deselected.connect(_on_editor_object_deselected)
    editor.npc_selected.connect(_on_editor_npc_selected)
    editor.npc_deselected.connect(_on_editor_npc_deselected)
    world.npc_metadata_changed.connect(_on_npc_metadata_changed)
    editor.mode_changed.connect(_on_editor_mode_changed)

func _on_config_pressed() -> void:
    if config_panel != null:
        config_panel.visible = not config_panel.visible
        camera.modal_open = config_panel.visible

func _on_edit_toggled(active: bool) -> void:
    editor_panel.visible = active
    editor.active = active
    camera.editor_active = active
    if not active:
        editor._deselect()
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

func _on_display_name_changed(display_name: String) -> void:
    if editor.selected_object != null:
        world.set_object_display_name(editor.selected_object, display_name)

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
    var headers := ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request(Auth.api_base + "/api/village/npcs/" + npc_id + "/run-cycle",
        headers, HTTPClient.METHOD_POST, "{}")

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
    var headers := ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
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
    var info := {
        "npc_id": npc_id,
        "display_name": editor.selected_npc.get_meta("display_name", ""),
        "behavior": editor.selected_npc.get_meta("behavior", ""),
        "llm_memory_agent": editor.selected_npc.get_meta("llm_memory_agent", ""),
        "home_structure_id": editor.selected_npc.get_meta("home_structure_id", ""),
        "work_structure_id": editor.selected_npc.get_meta("work_structure_id", ""),
    }
    editor_panel.show_npc_selection(info)

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

