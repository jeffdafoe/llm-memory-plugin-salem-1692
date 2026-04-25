extends Control
## Modal NPC sprite picker — opens over the editor when an admin wants to
## change which sprite an existing villager renders as. Fetches the same
## /api/village/npc-sprites catalog the placement palette uses, lays out
## thumbnails in a scrollable grid, and emits sprite_selected with the
## chosen sprite_id. The caller (main.gd) PATCHes the NPC and the WS
## broadcast handles the visual swap on every connected client.

signal sprite_selected(npc_id: String, sprite_id: String)
signal closed

const COLOR_BG = Color(0.05, 0.03, 0.02, 0.7)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_LABEL = Color(0.54, 0.48, 0.31, 1.0)
const COLOR_ITEM_BG = Color(0.15, 0.12, 0.08, 1.0)
const COLOR_ITEM_BORDER = Color(0.3, 0.24, 0.15, 0.5)
const COLOR_ITEM_HOVER = Color(0.25, 0.20, 0.10, 1.0)
const COLOR_ITEM_CURRENT = Color(0.45, 0.35, 0.18, 1.0)

const CELL_SIZE: float = 56.0
const GRID_COLUMNS: int = 6

var world: Node = null

var _font: Font = null
var _panel: PanelContainer = null
var _content: VBoxContainer = null
var _grid: GridContainer = null
var _title: Label = null
var _hint: Label = null
var _npc_id: String = ""
var _current_sprite_id: String = ""
var _sprites_loaded: bool = false
var _sprites: Array = []

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    anchors_preset = Control.PRESET_FULL_RECT
    anchor_right = 1.0
    anchor_bottom = 1.0

    var bg = ColorRect.new()
    bg.color = COLOR_BG
    bg.anchors_preset = Control.PRESET_FULL_RECT
    bg.anchor_right = 1.0
    bg.anchor_bottom = 1.0
    add_child(bg)

    _panel = PanelContainer.new()
    _panel.anchor_left = 0.20
    _panel.anchor_right = 0.80
    _panel.anchor_top = 0.12
    _panel.anchor_bottom = 0.88

    var panel_style = StyleBoxFlat.new()
    panel_style.bg_color = COLOR_PANEL_BG
    panel_style.border_width_left = 2
    panel_style.border_width_top = 2
    panel_style.border_width_right = 2
    panel_style.border_width_bottom = 2
    panel_style.border_color = COLOR_BORDER
    panel_style.corner_radius_left_top = 4
    panel_style.corner_radius_right_top = 4
    panel_style.corner_radius_left_bottom = 4
    panel_style.corner_radius_right_bottom = 4
    panel_style.content_margin_left = 24.0
    panel_style.content_margin_right = 24.0
    panel_style.content_margin_top = 20.0
    panel_style.content_margin_bottom = 20.0
    _panel.add_theme_stylebox_override("panel", panel_style)
    add_child(_panel)

    _content = VBoxContainer.new()
    _content.add_theme_constant_override("separation", 12)
    _panel.add_child(_content)

    _title = Label.new()
    _title.add_theme_font_override("font", _font)
    _title.add_theme_font_size_override("font_size", 18)
    _title.add_theme_color_override("font_color", COLOR_TEXT)
    _title.text = "Change Villager Sprite"
    _content.add_child(_title)

    _hint = Label.new()
    _hint.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    _hint.add_theme_font_size_override("font_size", 12)
    _hint.text = "Click a sprite to swap. Esc or click outside to cancel."
    _content.add_child(_hint)

    var scroll = ScrollContainer.new()
    scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    _content.add_child(scroll)

    _grid = GridContainer.new()
    _grid.columns = GRID_COLUMNS
    _grid.add_theme_constant_override("h_separation", 6)
    _grid.add_theme_constant_override("v_separation", 6)
    _grid.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    scroll.add_child(_grid)

func _input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        _close()
        get_viewport().set_input_as_handled()
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        var panel_rect: Rect2 = _panel.get_global_rect()
        if not panel_rect.has_point(event.position):
            _close()
            get_viewport().set_input_as_handled()

## Open the picker for a specific NPC. current_sprite_id highlights the
## currently selected sprite in the grid so the admin can see what's
## already in use. Lazily fetches the catalog on first open.
func show_for_npc(npc_id: String, current_sprite_id: String) -> void:
    _npc_id = npc_id
    _current_sprite_id = current_sprite_id
    visible = true
    if _sprites_loaded:
        _rebuild_grid()
    else:
        _load_sprites()

func _close() -> void:
    visible = false
    _npc_id = ""
    closed.emit()

func _load_sprites() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_sprites_loaded.bind(http))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    http.request(Auth.api_base + "/api/village/npc-sprites", headers)

func _on_sprites_loaded(result: int, code: int, _headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or code != 200:
        push_warning("NPC sprite picker: catalog load failed code=" + str(code))
        return
    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null or not (json is Array):
        return
    _sprites = json
    _sprites_loaded = true
    _rebuild_grid()

func _rebuild_grid() -> void:
    if _grid == null:
        return
    for child in _grid.get_children():
        child.queue_free()
    if world == null:
        return
    for sprite in _sprites:
        var sheet_path: String = sprite.get("sheet", "")
        if sheet_path == "":
            continue
        # Capture sprite by-value into the closure — Godot's lambda capture
        # would otherwise share the loop variable across iterations.
        var sprite_local: Dictionary = sprite
        world.get_or_load_npc_sheet(sheet_path, func(tex: Texture2D):
            _add_item(sprite_local, tex)
        )

func _add_item(sprite: Dictionary, sheet: Texture2D) -> void:
    if _grid == null or sheet == null:
        return
    var fw: int = int(sprite.get("frame_width", 32))
    var fh: int = int(sprite.get("frame_height", 32))
    var sprite_id: String = str(sprite.get("id", ""))
    var sprite_name: String = sprite.get("name", "villager")
    var is_current: bool = sprite_id != "" and sprite_id == _current_sprite_id

    var item = PanelContainer.new()
    item.custom_minimum_size = Vector2(CELL_SIZE, CELL_SIZE)
    item.tooltip_text = sprite_name + ("  (current)" if is_current else "")

    var item_style = StyleBoxFlat.new()
    item_style.bg_color = (COLOR_ITEM_CURRENT if is_current else COLOR_ITEM_BG)
    item_style.border_width_left = 1
    item_style.border_width_top = 1
    item_style.border_width_right = 1
    item_style.border_width_bottom = 1
    item_style.border_color = COLOR_ITEM_BORDER
    item_style.corner_radius_left_top = 2
    item_style.corner_radius_right_top = 2
    item_style.corner_radius_left_bottom = 2
    item_style.corner_radius_right_bottom = 2
    item.add_theme_stylebox_override("panel", item_style)

    var center = CenterContainer.new()
    center.custom_minimum_size = Vector2(CELL_SIZE - 4, CELL_SIZE - 4)
    item.add_child(center)

    # Frame 0 of row 0 = south-facing idle (Mana Seed NPC pack convention),
    # same convention as the placement palette.
    var atlas := AtlasTexture.new()
    atlas.atlas = sheet
    atlas.region = Rect2(0, 0, fw, fh)

    var tex_rect = TextureRect.new()
    tex_rect.texture = atlas
    var native_size: Vector2 = Vector2(fw, fh)
    var max_dim: float = CELL_SIZE - 8.0
    var scale_factor: float = minf(max_dim / native_size.x, max_dim / native_size.y)
    if scale_factor > 2.0:
        scale_factor = 2.0
    tex_rect.custom_minimum_size = native_size * scale_factor
    tex_rect.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
    tex_rect.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
    center.add_child(tex_rect)

    item.gui_input.connect(func(event: InputEvent):
        if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
            _on_item_clicked(sprite_id)
    )

    _grid.add_child(item)

func _on_item_clicked(sprite_id: String) -> void:
    if sprite_id == "" or _npc_id == "":
        return
    # Re-clicking the current sprite is a no-op close — admin signaled "no change."
    if sprite_id == _current_sprite_id:
        _close()
        return
    sprite_selected.emit(_npc_id, sprite_id)
    _close()
