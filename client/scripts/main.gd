extends Node2D
## Main scene — handles auth flow, bootstraps the village viewer,
## and wires up the editor UI (top bar + side panel).

const TopBarScript = preload("res://scripts/top_bar.gd")
const EditorPanelScript = preload("res://scripts/editor_panel.gd")
const ConfigPanelScript = preload("res://scripts/config_panel.gd")
const AssetPopupScript = preload("res://scripts/asset_popup.gd")
const NPCSpritePickerScript = preload("res://scripts/npc_sprite_picker.gd")
const ObjectTooltipScript = preload("res://scripts/object_tooltip.gd")
const ActorTooltipScript = preload("res://scripts/actor_tooltip.gd")
const EventClientScript = preload("res://scripts/event_client.gd")
const TalkPanelScript = preload("res://scripts/talk_panel.gd")
const NoticePanelScript = preload("res://scripts/notice_panel.gd")
const InventoryPanelScript = preload("res://scripts/inventory_panel.gd")
const VillageTickerScript = preload("res://scripts/village_ticker.gd")
const SleepFadeScript = preload("res://scripts/sleep_fade.gd")

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
var actor_tooltip: CanvasLayer = null
var event_client: Node = null
var talk_panel_layer: CanvasLayer = null
var notice_panel_layer: CanvasLayer = null
var inventory_panel_layer: CanvasLayer = null
var village_ticker: PanelContainer = null
## Sleep-fade overlay (ZBBS-WORK-204 Stage B). CanvasLayer with a
## ColorRect that tweens to twilight while the local PC sleeps and
## back to transparent on wake. See client/scripts/sleep_fade.gd.
var sleep_fade: CanvasLayer = null
## Dream-snippet rotator. While the local PC is sleeping, fires every
## DREAM_SNIPPET_INTERVAL_SEC and pushes one of DREAM_SNIPPETS into
## the village ticker so the top scroller carries flavor instead of
## sitting silent through real-clock-hours of sleep. Stopped on wake.
var _dream_snippet_timer: Timer = null
## /pc/wake POST helper — instantiated lazily on first wake-button
## press so the HTTPRequest node doesn't sit idle when sleep isn't
## in use.
var _pc_wake_http: HTTPRequest = null
## Local-PC actor cache for the structure label shown in the sleep
## marker. Set when the PC's inside_structure_id is known via the
## world container's meta; falls back to "" when the structure isn't
## resolvable. The label is purely for display — empty just renders
## "Sleeping — wake HH:MM" without the location.
var _pc_sleep_structure_label: String = ""

## Period-flavor dream snippets pushed into the village ticker while
## the local PC is sleeping. ZBBS-WORK-204 Stage B picks a static
## list rather than wiring chronicler / dream-pipeline output —
## simpler, ships entirely client-side, and the snippets read fine
## as ambient scoreboard chrome through real-clock sleep hours. A
## future ticket can swap this for engine-pushed lines if the static
## rotation grows stale in observation.
const DREAM_SNIPPETS: Array = [
    "The candle gutters.",
    "A beam settles in the dark.",
    "Zzz...",
    "You dream of horseshoes.",
    "You dream of the harvest.",
    "A floorboard creaks.",
    "The tavern is quiet.",
    "Distant footsteps. Then nothing.",
    "Wind in the eaves.",
    "You shift in the bed.",
    "Embers tick in the hearth.",
    "An owl, somewhere.",
    "You dream of a shoreline you have never seen.",
]
const DREAM_SNIPPET_INTERVAL_SEC: float = 180.0

# Login screen (added as a CanvasLayer so it renders on top of everything)
var login_screen: Control = null
var login_layer: CanvasLayer = null

# PC bootstrap — once we've decided whether the player needs to pick a
# sprite at this login, we don't want to redo the check on every signal
# fire from Auth (auth_ready + logged_in both run on a single verify).
# _pc_bootstrap_done flips after the first /pc/me lands.
var _pc_bootstrap_done: bool = false
var _pc_exists: bool = false
# world_ready watchdog (ZBBS-WORK-333). If world_ready doesn't fire within the
# timeout after auth, the boot stalled (a load-chain fetch or sheet wedged a
# latch); the watchdog beacons the latch summary so the stall surfaces via the
# umbilical instead of a silently-frozen curtain. _world_ready_fired guards it.
const _WORLD_READY_WATCHDOG_SECONDS: float = 20.0
var _world_ready_fired: bool = false
# PC's actor.id from /pc/me — used to recognize the player's own PC in
# WS broadcasts like npc_arrived. Empty until the first /pc/me response.
var _pc_actor_id: String = ""
var _pc_http_me: HTTPRequest = null
var _pc_http_save: HTTPRequest = null
# Set after the camera tweens to the PC's spawn position the first time
# /pc/me reports a placed PC. _ready hardcodes the camera at the village
# crossroads as a pre-PC default; subsequent /pc/me polls don't re-center.
var _pc_initial_camera_centered: bool = false

# Walk-then-read state (ZBBS-112). Click on a noticeboard sets this to
# the placement id; on PC arrival we open the notice panel if the id
# matches. Cleared when the player clicks elsewhere or dismisses the
# panel — a subsequent walk-to-noticeboard arrival isn't auto-opened
# unless they specifically clicked it again.
var _pending_notice_object_id: String = ""

# Discriminator for the in-flight /pc/move so the shared completion
# handler can decide which side-effects to roll back on a non-2xx.
# Today only "noticeboard" needs explicit cleanup (clear the pending
# flag); knock-narration / huddle-join paths self-recover on the
# next /pc/me poll. Empty when no /pc/move is in flight or the most
# recent one was a non-noticeboard click.
var _pc_move_purpose: String = ""

# Modal blocker set. Multiple panels (notice, talk pay-modal, config,
# asset popup, sprite picker) all set camera.modal_open while they're
# up. A raw bool toggle would race — closing one would release the
# world-input lock while another is still open. Track open modals by
# name and OR them together: camera.modal_open is true iff any blocker
# is registered. Every modal owner calls _set_modal_blocker(name, true)
# on open and (name, false) on close — this is the universal mechanism;
# direct `camera.modal_open = bool` assignments are not used anywhere.
var _modal_blockers: Dictionary = {}

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
    # ZBBS-HOME-210: hide just the login form; the dark Background
    # ColorRect inside LoginScreen stays visible as a curtain while
    # the village fetches + renders. Fades in _on_world_ready once
    # world.gd confirms objects + NPCs are placed. Without the
    # curtain, the player watches flat terrain alone for ~1-2s
    # while the catalog and village data load — the same gap the
    # whole 208 / 209 effort failed to address.
    if login_screen != null and login_screen.has_method("hide_form"):
        login_screen.hide_form()

    # Lock world input (clicks, scroll, walk, camera pan/zoom) while
    # the village materializes. Camera, walk-click, and editor
    # handlers bail on camera.modal_open. Released in
    # _on_world_ready. Events arriving during the lock are dropped,
    # not buffered — _input handlers early-return without
    # accept_event() so a click fired during the load doesn't fire
    # when the lock releases.
    _set_modal_blocker("world_loading", true)

    if world != null and not world.world_ready.is_connected(_on_world_ready):
        world.world_ready.connect(_on_world_ready)
        # Arm the stall watchdog once, alongside the (idempotent) world_ready
        # connect, so it starts exactly once per session.
        get_tree().create_timer(_WORLD_READY_WATCHDOG_SECONDS).timeout.connect(_on_world_ready_watchdog)

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
        event_client.npc_arrived.connect(_on_event_npc_arrived)
        event_client.pc_sleep_started.connect(_on_pc_sleep_started)
        event_client.pc_sleep_ended.connect(_on_pc_sleep_ended)
    event_client.world = world
    world.event_client = event_client
    event_client.connect_to_server()

    # Notice panel hooks into world.object_content_changed for live
    # refresh — wire here, after world is reachable. Idempotent.
    if notice_panel_layer != null and notice_panel_layer.has_method("attach_world"):
        notice_panel_layer.attach_world(world)

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
    # ZBBS-HOME-210: re-engage the curtain so the resync rebuild is
    # covered. world.gd's reset_world_state clears the world_ready
    # latches; world_ready re-emits when objects + NPCs are placed
    # again, and _on_world_ready fades + releases the lock.
    if login_screen != null and login_screen.has_method("hide_form"):
        login_screen.modulate = Color(1, 1, 1, 1)
        login_screen.visible = true
        login_screen.hide_form()
    _set_modal_blocker("world_loading", true)
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
        if not world.npc_attributes_changed.is_connected(_on_npc_attributes_changed_from_world):
            world.npc_attributes_changed.connect(_on_npc_attributes_changed_from_world)

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

func _on_npc_attributes_changed_from_world(npc_id: String, attributes: Array) -> void:
    if editor_panel != null:
        editor_panel.apply_npc_attributes_external(npc_id, attributes)

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
    config_panel.closed.connect(func(): _set_modal_blocker("config", false))

    # Editor side panel — also on the editor CanvasLayer, hidden by default
    editor_panel = PanelContainer.new()
    editor_panel.set_script(EditorPanelScript)
    editor.add_child(editor_panel)
    editor_panel.visible = false
    # Camera queries each registered panel's rect at hit-test time. The
    # editor sidebar is wider when an NPC is selected (extra controls), so
    # registering the live Control beats hardcoding a width — and any
    # future panel just calls camera.register_ui_panel(self) the same way.
    # participates_in_clamp=true so opening the sidebar relaxes the map
    # clamp + auto-shifts the camera; without it, the leftmost map column
    # is permanently hidden behind the panel.
    camera.register_ui_panel(editor_panel, true)
    editor.editor_panel_ref = editor_panel

    # Asset inspect popup — on the config layer (above editor)
    asset_popup = Control.new()
    asset_popup.set_script(AssetPopupScript)
    config_layer.add_child(asset_popup)
    asset_popup.visible = false
    asset_popup.place_requested.connect(_on_popup_place_requested)
    asset_popup.closed.connect(func():
        _set_modal_blocker("asset_popup", false)
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
        _set_modal_blocker("sprite_picker", false)
        editor.popup_open = false
    )

    # Object tooltip — shows owner info on hover when not in edit mode
    object_tooltip = CanvasLayer.new()
    object_tooltip.set_script(ObjectTooltipScript)
    object_tooltip.world = world
    object_tooltip.editor = editor
    add_child(object_tooltip)

    # Actor tooltip — click an NPC or PC to see who they are. Click-driven
    # so it works on touch as well as desktop.
    actor_tooltip = CanvasLayer.new()
    actor_tooltip.set_script(ActorTooltipScript)
    actor_tooltip.world = world
    actor_tooltip.editor = editor
    actor_tooltip.camera = camera
    add_child(actor_tooltip)

    # Talk panel (M6.7) — bottom drawer summoned by a "Talk" launcher pill.
    # The script extends CanvasLayer and owns its own layer ordering, mouse
    # filtering, and visibility — main.gd only needs to instantiate it.
    # layer = 3 puts the panel above the editor UI (default 1) and below
    # the config screen (5) and login (10). Without an explicit layer
    # the panel rendered behind the editor's top bar / sidebar in some
    # play-mode layouts, hiding the launcher pill from the player.
    talk_panel_layer = CanvasLayer.new()
    talk_panel_layer.name = "TalkPanelLayer"
    talk_panel_layer.layer = 3
    talk_panel_layer.set_script(TalkPanelScript)
    add_child(talk_panel_layer)

    # Forward the talk panel's purse_changed signal to the top bar's
    # coin chip. Talk panel polls /pc/me and emits this whenever the
    # snapshot includes fresh coin / inventory state; top bar renders
    # them. Decoupled so neither side knows the other's node path.
    if talk_panel_layer.has_signal("purse_changed") and top_bar != null:
        talk_panel_layer.purse_changed.connect(_on_pc_purse_changed)
    # Same wiring for needs_changed → top bar HUD readout (ZBBS-123).
    if talk_panel_layer.has_signal("needs_changed") and top_bar != null:
        talk_panel_layer.needs_changed.connect(_on_pc_needs_changed)
    # ZBBS-HOME-218: forward dwelling_attributes from the talk panel
    # to the top bar so the HUD recovery pulse engages from server
    # state on every poll, not just on detected client-side decreases.
    if talk_panel_layer.has_signal("dwelling_attributes_changed") and top_bar != null:
        talk_panel_layer.dwelling_attributes_changed.connect(_on_pc_dwelling_attributes_changed)
    # Character name → top bar's username slot. Talk panel emits the
    # display_name from /pc/me; top bar shows it instead of the login,
    # falling back to the login when no PC exists (empty payload).
    if talk_panel_layer.has_signal("character_name_changed") and top_bar != null:
        talk_panel_layer.character_name_changed.connect(_on_pc_character_name_changed)
    # Pay modal blocks world input. Without this, a click on the
    # Confirm button propagates into the world's PC-walk handler and
    # the character starts walking under the open modal.
    if talk_panel_layer.has_signal("modal_open_changed") and camera != null:
        talk_panel_layer.modal_open_changed.connect(func(open: bool): _set_modal_blocker("talk_pay", open))

    # Audience scope → world.gd. Talk panel polls /pc/me and tracks the
    # PC's (structure, room) audibility scope; world.gd needs the same
    # scope to gate speech-bubble rendering so private-bedroom speech
    # doesn't leak as a bubble to PCs outside the bedroom. Without this,
    # the talk panel filter shipped earlier still leaves the visual
    # leak open via _spawn_speech_bubble.
    var world_node = get_node_or_null("/root/Main/World")
    if world_node != null and world_node.has_method("set_audience_scope") and talk_panel_layer.has_signal("audience_scope_changed"):
        talk_panel_layer.audience_scope_changed.connect(world_node.set_audience_scope)

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

    # Also register the launcher chip — main.gd._input runs before GUI,
    # so the chip's MOUSE_FILTER_STOP alone doesn't keep clicks out of
    # the click-to-walk handler. Without this, tapping the minimized
    # chip to open the panel also issues a move_to underneath. Chip
    # visibility is dynamic (only shown when panel is minimized and a
    # huddle exists), so _is_over_ui's is_visible_in_tree() gate keeps
    # it from over-blocking when the panel is expanded.
    if talk_panel_layer.has_method("get_launcher_control"):
        var launcher: Control = talk_panel_layer.get_launcher_control()
        if launcher != null:
            camera.register_ui_panel(launcher)

    # Notice panel (ZBBS-112) — modal for reading noticeboard content
    # after the PC walks up to one. Layer = 4: above editor (1) and
    # talk panel (3), below config (5) and login (10). attach_world
    # is deferred until the World node is reachable in
    # _on_authenticated, since main.gd builds UI before the world is
    # populated.
    notice_panel_layer = CanvasLayer.new()
    notice_panel_layer.name = "NoticePanelLayer"
    notice_panel_layer.set_script(NoticePanelScript)
    add_child(notice_panel_layer)
    notice_panel_layer.opened.connect(func(): _set_modal_blocker("notice", true))
    notice_panel_layer.closed.connect(func(): _set_modal_blocker("notice", false))

    # Inventory popover — shows the player's pack when the top-bar
    # icon is clicked. Layer = 4 so it floats above the talk panel
    # (3) and below the modal config screen (5); a player can open
    # their pack mid-conversation without dismissing the chat. Not a
    # modal — outside-clicks close but don't dim the world.
    inventory_panel_layer = CanvasLayer.new()
    inventory_panel_layer.name = "InventoryPanelLayer"
    inventory_panel_layer.layer = 4
    var inventory_panel: Control = Control.new()
    inventory_panel.set_script(InventoryPanelScript)
    inventory_panel_layer.add_child(inventory_panel)
    add_child(inventory_panel_layer)
    if top_bar != null and top_bar.has_signal("inventory_toggle_requested"):
        top_bar.inventory_toggle_requested.connect(func(rect: Rect2):
            if inventory_panel.visible:
                inventory_panel.close()
            else:
                inventory_panel.show_at(rect)
        )
    if talk_panel_layer.has_signal("inventory_changed"):
        talk_panel_layer.inventory_changed.connect(inventory_panel.set_inventory)

    # Village ticker — thin marquee band below the top bar, scrolling the
    # village's current atmosphere prose. Lives on the editor CanvasLayer
    # alongside top_bar so it inherits the same z-order. begin() fetches the
    # world-state DTO (which carries the atmosphere string) and starts the
    # slow refresh poll; Auth is already set by this point.
    village_ticker = PanelContainer.new()
    village_ticker.set_script(VillageTickerScript)
    editor.add_child(village_ticker)
    village_ticker.begin()
    camera.register_ui_panel(village_ticker)

    # Sleep-fade overlay (ZBBS-WORK-204 Stage B). Lives on its own
    # CanvasLayer at layer=0 so it paints over the world (default
    # Node2D layer 0, last-child-wins) but under the editor (layer
    # 1) and higher UI layers — top bar wake button stays clickable.
    sleep_fade = CanvasLayer.new()
    sleep_fade.set_script(SleepFadeScript)
    add_child(sleep_fade)

    # Wake-up button on the top bar emits wake_pressed; route to the
    # /pc/wake endpoint. The engine clears sleeping_until and
    # broadcasts pc_sleep_ended which drives the fade-out + chip
    # restoration on every connected client.
    if top_bar.has_signal("wake_pressed"):
        top_bar.wake_pressed.connect(_on_wake_pressed)

    # Dream-snippet timer. Fires every DREAM_SNIPPET_INTERVAL_SEC
    # (3 min) while the local PC is sleeping, pushing one of the
    # static DREAM_SNIPPETS into the village ticker. Stopped on
    # wake. autostart=false so it sits idle until pc_sleep_started.
    _dream_snippet_timer = Timer.new()
    _dream_snippet_timer.one_shot = false
    _dream_snippet_timer.wait_time = DREAM_SNIPPET_INTERVAL_SEC
    _dream_snippet_timer.autostart = false
    _dream_snippet_timer.timeout.connect(_on_dream_snippet_tick)
    add_child(_dream_snippet_timer)

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
    editor_panel.npc_agent_changed.connect(_on_npc_agent_changed)
    editor_panel.npc_home_structure_changed.connect(_on_npc_home_structure_changed)
    editor_panel.npc_work_structure_changed.connect(_on_npc_work_structure_changed)
    editor_panel.npc_schedule_changed.connect(_on_npc_schedule_changed)
    editor_panel.npc_social_changed.connect(_on_npc_social_changed)
    editor_panel.npc_home_assign_requested.connect(_on_npc_home_assign_requested)
    editor_panel.npc_work_assign_requested.connect(_on_npc_work_assign_requested)
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
    # and per-NPC metadata changes (rename / attributes / inside-flip).
    world.npc_list_changed.connect(_on_villagers_list_should_refresh)
    world.npc_metadata_changed.connect(func(_id): _on_villagers_list_should_refresh())
    world.npc_attributes_changed.connect(func(_id, _attrs): _on_villagers_list_should_refresh())
    editor.mode_changed.connect(_on_editor_mode_changed)
    # Cursor tile readout — editor emits on mouse motion over the map,
    # top bar renders the coords. Hidden whenever edit mode turns off.
    editor.cursor_tile_changed.connect(_on_cursor_tile_changed)

func _on_config_pressed() -> void:
    if config_panel != null:
        config_panel.visible = not config_panel.visible
        _set_modal_blocker("config", config_panel.visible)


## Forward purse updates from the talk panel to the top bar's coin
## chip. Negative coins from the panel signals "no PC" — top bar
## hides the chip on that.
func _on_pc_purse_changed(coins: int, inventory_lines: PackedStringArray) -> void:
    if top_bar == null or not top_bar.has_method("set_purse"):
        return
    var arr: Array = []
    for line in inventory_lines:
        arr.append(line)
    top_bar.set_purse(coins, arr)


## Forward body-need updates from the talk panel to the top bar's HUD
## readout (ZBBS-123). Empty dictionary signals "no PC" — top bar
## hides the readout.
func _on_pc_needs_changed(needs: Dictionary) -> void:
    if top_bar == null or not top_bar.has_method("set_needs"):
        return
    top_bar.set_needs(needs)

## ZBBS-HOME-218: forward server-reported dwelling attributes to top
## bar so the HUD recovery pulse engages immediately on /pc/me, even
## without a client-detected value decrease. Empty array clears any
## active pulse.
func _on_pc_dwelling_attributes_changed(attrs: PackedStringArray) -> void:
    if top_bar == null or not top_bar.has_method("set_dwelling_attributes"):
        return
    top_bar.set_dwelling_attributes(attrs)


## Forward the in-world character name from /pc/me to the top bar.
## Empty string signals "no PC" — top bar reverts to the login
## username. Login fallback is set once at _on_authenticated; the
## override layer happens here on every /pc/me poll.
func _on_pc_character_name_changed(name: String) -> void:
    if top_bar == null or not top_bar.has_method("set_character_name"):
        return
    top_bar.set_character_name(name)

func _on_edit_toggled(active: bool) -> void:
    editor_panel.visible = active
    editor.active = active
    camera.editor_active = active
    if top_bar != null:
        top_bar.set_cursor_tile_visible(active)
    if active:
        # Auto-minimize the talk panel when entering edit mode. Without
        # this, leaving the chat sheet open over the editor's map
        # leaks clicks through and walks the selected NPC to wherever
        # the user clicked (the chat sheet still claims the area but
        # the editor receives the click). State is preserved — the
        # launcher chip stays around so the user can tap it to expand
        # again after leaving edit. No-op when the panel isn't open.
        if talk_panel_layer != null and talk_panel_layer.has_method("minimize"):
            talk_panel_layer.minimize()
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
        login_screen.modulate = Color(1, 1, 1, 1)
        login_screen.visible = true
        # ZBBS-HOME-210: the form may have been hidden by an earlier
        # _on_authenticated. Bring it back so the user can re-enter
        # credentials. set_message paints the "Session expired"
        # error label.
        if login_screen.has_method("show_form"):
            login_screen.show_form()
        if login_screen.has_method("set_message"):
            login_screen.set_message(message)

func _on_asset_inspect_requested(asset_id: String) -> void:
    if asset_popup != null:
        asset_popup.show_asset(asset_id)
        _set_modal_blocker("asset_popup", true)
        editor.popup_open = true

func _on_popup_place_requested(asset_id: String) -> void:
    _set_modal_blocker("asset_popup", false)
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
## when the work-window is NULL-inheriting dawn/dusk — world.set_npc_schedule
## maps the -1 sentinels to null in the JSON payload.
func _on_npc_schedule_changed(start_min: int, end_min: int) -> void:
    if editor.selected_npc != null:
        world.set_npc_schedule(editor.selected_npc, start_min, end_min)

## Social-hour schedule changed (ZBBS-068, minute precision since ZBBS-071).
## Empty tag clears the schedule (start_min/end_min ignored in that case).
func _on_npc_social_changed(tag: String, start_min: int, end_min: int) -> void:
    if editor.selected_npc != null:
        world.set_npc_social(editor.selected_npc, tag, start_min, end_min)

func _on_npc_home_assign_requested() -> void:
    editor.begin_assign_home()

func _on_npc_work_assign_requested() -> void:
    editor.begin_assign_work()

## Admin chose a new entry policy for the selected placement (ZBBS-101).
## PATCH /api/village/objects/{id}/entry-policy → server validates and
## broadcasts object_entry_policy_changed back to every connected client.
## A 400 (e.g. trying to set 'owner' on a structure with no associated
## actor) is surfaced as an alert and the dropdown reverts on the next
## show_selection.
func _on_entry_policy_changed(object_id: String, policy: String) -> void:
    var payload = JSON.stringify({"object_id": object_id, "entry_policy": policy})
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
    http.request(Auth.api_base + "/api/village/admin/object/set-entry-policy",
        headers, HTTPClient.METHOD_POST, payload)

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
    _set_modal_blocker("sprite_picker", true)
    editor.popup_open = true

## Picker emitted a selection. POST the new sprite_id and let the WS
## broadcast (npc_sprite_changed) drive the visual swap on every client,
## including this one. No need to update local state imperatively.
## (ZBBS-HOME-309 rewired this off the dead v1 PATCH /api/village/npcs/{id}/sprite.)
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
    var payload: String = JSON.stringify({"npc_id": npc_id, "sprite_id": sprite_id})
    http.request(Auth.api_base + "/api/village/admin/npc/set-sprite",
        headers, HTTPClient.METHOD_POST, payload)

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
    # Cache actor_id so npc_arrived broadcasts can be matched to "this is
    # the player's PC arriving." Engine populates it once the PC actor row
    # exists; empty before /pc/create completes.
    _pc_actor_id = str(data.get("actor_id", ""))
    # Slide camera to the PC's actual position on the first /pc/me that
    # reports a placed PC. Login default is the village crossroads, so
    # without this the player has to find their own PC on the map after
    # logging in. center_on tweens 0.3s — feels intentional, not jarring.
    if _pc_exists and not _pc_initial_camera_centered:
        # pc/me x/y are TILE coords (the v2 wire contract); convert to a
        # world-pixel position via the one tile->pixel home before centering.
        camera.center_on(VillageApi.tile_to_world(int(data.get("x", 0)), int(data.get("y", 0))))
        _pc_initial_camera_centered = true
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
    _set_modal_blocker("sprite_picker", true)
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
            # Skip clicks that landed on an actor sprite — those are
            # "who is this" identification clicks handled by
            # actor_tooltip.gd, not walk targets.
            if actor_tooltip != null and actor_tooltip.is_press_over_actor(event.position):
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
                # Server rejected the move. Roll back any side-effects
                # the in-flight click set up — today the only one is the
                # ZBBS-112 pending-notice flag. Without this, a 4xx/5xx
                # leaves the flag armed and the next unrelated PC arrival
                # opens the panel for a stale board.
                if _pc_move_purpose == "noticeboard":
                    _pending_notice_object_id = ""
                _pc_move_purpose = ""
                return
            _pc_move_purpose = ""
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
                    # Auto-open the panel: a successful knock means the
                    # player wants to talk to the vendor, and the launcher
                    # pill alone is too easy to miss in the heat of play.
                    # _refresh_state is async (HTTPRequest); defer the
                    # open() so huddle_members has had time to populate
                    # via the /pc/me response.
                    if talk_panel_layer != null and talk_panel_layer.has_method("force_open_after_refresh"):
                        talk_panel_layer.force_open_after_refresh()
                else:
                    var narration: String = str(parsed.get("knock_narration", ""))
                    if narration != "" and talk_panel_layer != null and talk_panel_layer.has_method("append_local_narration"):
                        talk_panel_layer.append_local_narration(narration)
        )
    var headers := Auth.auth_headers()
    var hit: Dictionary = world.find_object_at(screen_pos)
    var payload: String
    var hit_id: String = str(hit.get("id", "")) if hit.has("id") else ""

    # Walk-then-read state machine (ZBBS-112). Any new walk closes a
    # currently-open notice panel — clicking elsewhere mid-read IS the
    # dismiss. A click on a placement tagged `noticeboard_content` with
    # content posted arms a pending-read flag; the npc_arrived handler
    # opens the panel when the PC lands at the loiter slot. The pending
    # flag is repopulated even when re-clicking the same board so a
    # walk-back-to-the-same-board still re-opens the panel.
    if notice_panel_layer != null and notice_panel_layer.has_method("close"):
        notice_panel_layer.close()
    if hit_id != "" and _is_readable_noticeboard(hit_id):
        _pending_notice_object_id = hit_id
        _pc_move_purpose = "noticeboard"
    else:
        _pending_notice_object_id = ""
        _pc_move_purpose = ""

    if hit_id != "":
        # Building/object click. has_interior (ZBBS-WORK-351) discriminates a
        # placement that has a Structure row — a building, or a legacy-shelled
        # prop like a noticeboard — from a bare placement (well, lamp, gather
        # pile). A building's id is a valid structure_id for structure_enter
        # (walk inside, today's behavior); a bare prop has no interior, so we
        # walk to its loiter slot via object_visit. The engine does NOT fall
        # through structure_enter → object_visit; misdispatching on a bare
        # object 404s, so the client must pick the right kind here.
        if bool(hit.get("has_interior", false)):
            payload = JSON.stringify({
                "destination": {"kind": "structure_enter", "structure_id": hit_id},
            })
        else:
            payload = JSON.stringify({
                "destination": {"kind": "object_visit", "object_id": hit_id},
            })
    else:
        # Empty-ground click → walk to that tile. Convert the world-pixel click
        # to a PADDED internal-grid tile (the canonical wire unit) via the single
        # inverse seam; never use world.world_to_tile (unpadded) for wire coords.
        var world_pos: Vector2 = world.get_global_mouse_position()
        var tile: Vector2i = VillageApi.world_to_tile_padded(world_pos)
        payload = JSON.stringify({
            "destination": {"kind": "position", "position": {"x": tile.x, "y": tile.y}},
        })
    var err := _pc_http_move.request(Auth.api_base + "/api/village/pc/move",
        headers, HTTPClient.METHOD_POST, payload)
    if err != OK:
        # Local request failure — never reached the server. Clear the
        # pending notice flag and the purpose discriminator so a later
        # unrelated arrival or completion can't act on this aborted walk.
        _pending_notice_object_id = ""
        _pc_move_purpose = ""
        push_warning("PC move request failed: %s" % err)

## Modal blocker helper — flips camera.modal_open based on the OR of
## currently-open modals. Every modal (config, asset popup, sprite
## picker, talk panel pay modal, notice panel) calls this on open and
## close so concurrent modals (e.g. talk pay modal + notice panel)
## don't release the world-input lock for each other. World input
## stays locked while any blocker is registered.
func _set_modal_blocker(blocker_name: String, enabled: bool) -> void:
    if camera == null:
        return
    if enabled:
        _modal_blockers[blocker_name] = true
    else:
        _modal_blockers.erase(blocker_name)
    camera.modal_open = not _modal_blockers.is_empty()

## Returns true when `object_id` is a placed object the PC can read —
## carries the `noticeboard_content` instance tag AND has content posted
## right now. A tagged board with null content (cycled to empty by the
## crier) walks-up cleanly but doesn't auto-open the panel — there's
## nothing to read yet.
func _is_readable_noticeboard(object_id: String) -> bool:
    if world == null or not world.placed_objects.has(object_id):
        return false
    var node: Node2D = world.placed_objects[object_id]
    if node == null:
        return false
    var tags: Array = node.get_meta("tags", [])
    if not (tags is Array) or not tags.has("noticeboard_content"):
        return false
    var content = node.get_meta("content_text", null)
    return content != null and str(content) != ""

## event_client emits npc_arrived after every walk completion (NPC or
## PC). When it's the player's own PC arriving AND a notice read is
## pending for an object whose content is still readable, open the
## panel. Stale content (board cleared between dispatch and arrival)
## closes the pending state silently — no panel pops on a bare board.
func _on_event_npc_arrived(npc_id: String, _x: float, _y: float, _facing: String) -> void:
    if _pending_notice_object_id == "":
        return
    if _pc_actor_id == "" or npc_id != _pc_actor_id:
        return
    var target_id: String = _pending_notice_object_id
    _pending_notice_object_id = ""
    # Defensive: the 2xx response handler already clears _pc_move_purpose
    # on success, but clearing here too means the discriminator is reset
    # at the actual end of the walk lifecycle. Belt-and-suspenders against
    # a future refactor that drops the 2xx-side clear.
    _pc_move_purpose = ""
    if world == null or not world.placed_objects.has(target_id):
        return
    var node: Node2D = world.placed_objects[target_id]
    if node == null:
        return
    var tags: Array = node.get_meta("tags", [])
    if not (tags is Array) or not tags.has("noticeboard_content"):
        return
    var content = node.get_meta("content_text", null)
    if content == null or str(content) == "":
        return
    var display_name: String = str(node.get_meta("display_name", ""))
    var posted_at = node.get_meta("content_posted_at", null)
    if notice_panel_layer != null and notice_panel_layer.has_method("show_for_object"):
        notice_panel_layer.show_for_object(target_id, display_name, content, posted_at)


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
        "attributes": container.get_meta("attributes", []),
        "llm_memory_agent": container.get_meta("llm_memory_agent", ""),
        "home_structure_id": container.get_meta("home_structure_id", ""),
        "work_structure_id": container.get_meta("work_structure_id", ""),
    }
    # Worker work-window: present only when the NPC has overridden the
    # global dawn/dusk default. Missing keys signal "inherit" to the panel.
    if container.has_meta("schedule_start_minute"):
        info["schedule_start_minute"] = container.get_meta("schedule_start_minute")
        info["schedule_end_minute"] = container.get_meta("schedule_end_minute")
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

## World finished its bootstrap render: village objects placed AND
## NPCs all rendered. Fade the login_screen curtain (its dark
## Background ColorRect has been covering the world since auth) and
## release the input lock. ZBBS-HOME-210.
const _WORLD_READY_FADE_DURATION: float = 0.4
func _on_world_ready() -> void:
    _world_ready_fired = true
    _set_modal_blocker("world_loading", false)
    if login_screen == null:
        return
    var t: Tween = create_tween()
    t.tween_property(login_screen, "modulate", Color(1, 1, 1, 0), _WORLD_READY_FADE_DURATION)
    t.tween_callback(func():
        if login_screen != null:
            login_screen.visible = false
            # Reset modulate so a future _show_login_screen
            # (session-expired path) doesn't surface the screen at
            # alpha 0. _show_login_screen also sets modulate but
            # belt-and-suspenders.
            login_screen.modulate = Color(1, 1, 1, 1)
    )

## Fired _WORLD_READY_WATCHDOG_SECONDS after auth. If world_ready never came,
## the boot stalled behind the curtain — beacon the latch summary so it shows up
## in the umbilical client-error feed instead of being an invisible hang.
func _on_world_ready_watchdog() -> void:
    if _world_ready_fired:
        return
    var summary: String = ""
    if world != null and world.has_method("world_ready_pending_summary"):
        summary = world.world_ready_pending_summary()
    ErrorBeacon.report("world_ready_stalled", summary)


## ZBBS-WORK-204 Stage B — local PC entered sleep. Engine broadcasts
## globally; we filter by _pc_actor_id so a different player's PC
## bedding down at the inn doesn't tint our screen. Fades the
## twilight overlay in, marks the top bar, starts the dream-snippet
## ticker rotation. Pre-/pc/me bootstrap (_pc_actor_id == "") drops
## the event silently — we don't have the local PC yet.
func _on_pc_sleep_started(actor_id: String, wake_at_iso: String) -> void:
    if _pc_actor_id == "" or actor_id != _pc_actor_id:
        return
    _pc_sleep_structure_label = _resolve_local_pc_structure_label()
    if sleep_fade != null and sleep_fade.has_method("fade_to_sleep"):
        sleep_fade.fade_to_sleep()
    if top_bar != null and top_bar.has_method("set_sleep_state"):
        top_bar.set_sleep_state(true, _pc_sleep_structure_label, wake_at_iso)
    # Collapse the talk panel to its launcher chip — no one's going to
    # talk to a sleeping PC, and the panel covers world view the player
    # might want clear during the sleep overlay. minimize() is
    # idempotent, so calling it when the panel is already minimized is
    # a no-op. Wake does NOT auto-expand — the player taps the chip
    # when they want chat back.
    if talk_panel_layer != null and talk_panel_layer.has_method("minimize"):
        talk_panel_layer.minimize()
    # Push an immediate snippet so the ticker carries flavor from the
    # bed-down moment instead of waiting up to DREAM_SNIPPET_INTERVAL_SEC
    # for the first scheduled tick.
    _push_dream_snippet()
    if _dream_snippet_timer != null:
        _dream_snippet_timer.start()


## ZBBS-WORK-204 Stage B — local PC woke. Same actor-id filter as
## sleep_started. Fades the overlay out, clears the top-bar marker,
## stops the snippet ticker. Reason is currently unused but plumbed
## through for future "you woke because X" surfaces.
func _on_pc_sleep_ended(actor_id: String, _reason: String) -> void:
    if _pc_actor_id == "" or actor_id != _pc_actor_id:
        return
    if sleep_fade != null and sleep_fade.has_method("fade_to_awake"):
        sleep_fade.fade_to_awake()
    if top_bar != null and top_bar.has_method("set_sleep_state"):
        top_bar.set_sleep_state(false, "", "")
    if _dream_snippet_timer != null:
        _dream_snippet_timer.stop()
    _pc_sleep_structure_label = ""


## Top bar's Wake-up button → POST /api/village/pc/wake. Engine
## clears sleeping_until and broadcasts pc_sleep_ended (reason
## "manual"), which routes back through _on_pc_sleep_ended. The
## HTTPRequest is instantiated lazily so it doesn't sit idle when
## the player never sleeps in this session.
func _on_wake_pressed() -> void:
    if _pc_wake_http == null:
        _pc_wake_http = HTTPRequest.new()
        add_child(_pc_wake_http)
        _pc_wake_http.request_completed.connect(_on_pc_wake_completed)
    if not Auth.is_authenticated():
        return
    var url: String = Auth.api_base + "/api/village/pc/wake"
    var headers: PackedStringArray = Auth.auth_headers()
    var err := _pc_wake_http.request(url, headers, HTTPClient.METHOD_POST, "")
    if err != OK:
        push_warning("/pc/wake request failed: %s" % err)


func _on_pc_wake_completed(_result: int, code: int, _headers: PackedStringArray, _body: PackedByteArray) -> void:
    if not Auth.check_response(code):
        return
    # Engine broadcasts pc_sleep_ended on success; the UI updates via
    # _on_pc_sleep_ended. Non-2xx is logged but not surfaced — the
    # wake button just looks like it didn't work, which is true.
    if code < 200 or code >= 300:
        push_warning("/pc/wake non-2xx: code=%s" % code)


## Push one randomly-picked dream snippet to the village ticker.
## Called both by the snippet timer and immediately at sleep onset
## (so the ticker carries flavor from minute one). No-op if the
## ticker isn't ready yet.
func _push_dream_snippet() -> void:
    if village_ticker == null or not village_ticker.has_method("push"):
        return
    if DREAM_SNIPPETS.is_empty():
        return
    var idx := randi() % DREAM_SNIPPETS.size()
    village_ticker.push(str(DREAM_SNIPPETS[idx]))


func _on_dream_snippet_tick() -> void:
    _push_dream_snippet()


## Best-effort lookup of the structure the local PC is currently
## inside. Returns the structure's display_name (or asset name
## fallback) when resolvable; "" when the PC isn't inside a
## structure at sleep time, the world hasn't loaded the placement
## yet, or any meta is missing. Empty string degrades the marker
## to a no-location framing — acceptable for the v1 cue.
func _resolve_local_pc_structure_label() -> String:
    if world == null or _pc_actor_id == "":
        return ""
    if not world.placed_npcs.has(_pc_actor_id):
        return ""
    var container: Node2D = world.placed_npcs[_pc_actor_id]
    var inside_id: String = str(container.get_meta("inside_structure_id", ""))
    if inside_id == "" or not world.placed_objects.has(inside_id):
        return ""
    var structure: Node2D = world.placed_objects[inside_id]
    var label: String = str(structure.get_meta("display_name", ""))
    if label != "":
        return label
    # Fallback to the asset's name when the placement has no display
    # override — keeps "Sleeping at the Tavern" reading naturally
    # rather than collapsing to no label.
    var asset_id: String = str(structure.get_meta("asset_id", ""))
    if asset_id != "" and Catalog.assets.has(asset_id):
        var asset_dict: Dictionary = Catalog.assets[asset_id]
        return str(asset_dict.get("name", ""))
    return ""

