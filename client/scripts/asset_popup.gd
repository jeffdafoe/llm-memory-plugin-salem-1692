extends Control
## Asset inspect popup — shows a larger preview of an asset with all its
## states, metadata, and a Place button. Opens when clicking an asset
## thumbnail in the editor palette.

signal place_requested(asset_id: String)

const COLOR_BG = Color(0.05, 0.03, 0.02, 0.7)
const COLOR_PANEL_BG = Color(0.12, 0.09, 0.07, 0.98)
const COLOR_BORDER = Color(0.45, 0.35, 0.22, 1.0)
const COLOR_TEXT = Color(0.85, 0.75, 0.55, 1.0)
const COLOR_TEXT_DIM = Color(0.63, 0.56, 0.44, 1.0)
const COLOR_LABEL = Color(0.54, 0.48, 0.31, 1.0)
const COLOR_BTN_BG = Color(0.35, 0.25, 0.12, 1.0)
const COLOR_BTN_BORDER = Color(0.55, 0.42, 0.25, 1.0)
const COLOR_STATE_DEFAULT = Color(0.85, 0.75, 0.35, 0.9)
const COLOR_STATE_BORDER = Color(0.3, 0.24, 0.15, 0.5)

const PREVIEW_SIZE: int = 128
const STATE_THUMB_SIZE: int = 64

var _font: Font = null
var _panel: PanelContainer = null
var _content: VBoxContainer = null
var _current_asset_id: String = ""

func _ready() -> void:
    _font = load("res://assets/fonts/IMFellEnglish-Regular.ttf")

    # Keep the open inspector in sync when another admin (or our own click)
    # mutates tags — Catalog re-emits after applying the WS payload.
    if not Catalog.state_tags_changed.is_connected(_on_catalog_state_tags_changed):
        Catalog.state_tags_changed.connect(_on_catalog_state_tags_changed)

    # Full-screen overlay
    anchors_preset = Control.PRESET_FULL_RECT
    anchor_right = 1.0
    anchor_bottom = 1.0

    # Dim background — click to close
    var bg = ColorRect.new()
    bg.color = COLOR_BG
    bg.anchors_preset = Control.PRESET_FULL_RECT
    bg.anchor_right = 1.0
    bg.anchor_bottom = 1.0
    add_child(bg)

    # Centered panel — smaller than config, just for one asset
    _panel = PanelContainer.new()
    _panel.anchor_left = 0.25
    _panel.anchor_right = 0.75
    _panel.anchor_top = 0.15
    _panel.anchor_bottom = 0.85
    _panel.offset_left = 0
    _panel.offset_right = 0
    _panel.offset_top = 0
    _panel.offset_bottom = 0

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

func _input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
        _close()
        get_viewport().set_input_as_handled()
    # Handle all mouse clicks — close if outside panel, block if inside
    if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
        # Check if click is outside the panel rect (on the dim background)
        var panel_rect: Rect2 = _panel.get_global_rect()
        if not panel_rect.has_point(event.position):
            _close()
        get_viewport().set_input_as_handled()

signal closed

func _close() -> void:
    visible = false
    closed.emit()

## Show the popup for a specific asset.
func show_asset(asset_id: String) -> void:
    _current_asset_id = asset_id
    var asset = Catalog.assets.get(asset_id, {})
    if asset.is_empty():
        return

    # Clear previous content
    for child in _content.get_children():
        child.queue_free()

    var asset_name: String = asset.get("name", asset_id)
    var states: Array = asset.get("states", [])
    var default_state: String = asset.get("defaultState", asset.get("default_state", "default"))
    var anchor_x: float = asset.get("anchorX", asset.get("anchor_x", 0.5))
    var anchor_y: float = asset.get("anchorY", asset.get("anchor_y", 0.85))
    var layer: String = asset.get("layer", "objects")
    var category: String = asset.get("category", "")
    var pack = asset.get("pack", {})
    var pack_name: String = ""
    if pack is Dictionary:
        pack_name = pack.get("name", "")

    # Asset name
    var name_label = Label.new()
    name_label.text = asset_name
    name_label.add_theme_color_override("font_color", COLOR_TEXT)
    name_label.add_theme_font_override("font", _font)
    name_label.add_theme_font_size_override("font_size", 24)
    name_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    _content.add_child(name_label)

    # Large preview — proportionally scaled, animated if multi-frame
    var state_info = Catalog.get_state(asset_id)
    if state_info != null:
        var texture = Catalog.get_sprite_texture(state_info)
        if texture != null:
            var preview_container = CenterContainer.new()
            _content.add_child(preview_container)

            var tex_rect = TextureRect.new()
            tex_rect.texture = texture
            # Scale to fit PREVIEW_SIZE while maintaining aspect ratio
            var native_size: Vector2 = texture.get_size()
            var scale_factor: float = minf(PREVIEW_SIZE / native_size.x, PREVIEW_SIZE / native_size.y)
            # At least 2x for tiny sprites
            if scale_factor < 2.0:
                scale_factor = 2.0
            tex_rect.custom_minimum_size = native_size * scale_factor
            tex_rect.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
            tex_rect.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
            preview_container.add_child(tex_rect)

            # Animate if multi-frame
            _animate_texture_rect(tex_rect, state_info, preview_container)

    # Metadata
    var meta_box = VBoxContainer.new()
    meta_box.add_theme_constant_override("separation", 2)
    _content.add_child(meta_box)

    _add_id_row(meta_box, asset_id)
    _add_meta_line(meta_box, "Category: " + category)
    _add_meta_line(meta_box, "Layer: " + layer + "  Anchor: (" + str(anchor_x) + ", " + str(anchor_y) + ")")
    if pack_name != "":
        _add_meta_line(meta_box, "Pack: " + pack_name)

    # States section
    if states.size() > 0:
        var states_header = Label.new()
        states_header.text = "STATES (" + str(states.size()) + ")"
        states_header.add_theme_color_override("font_color", COLOR_LABEL)
        states_header.add_theme_font_size_override("font_size", 12)
        _content.add_child(states_header)

        var states_flow = HBoxContainer.new()
        states_flow.add_theme_constant_override("separation", 8)
        _content.add_child(states_flow)

        for state in states:
            _add_state_thumb(states_flow, state, state.get("state", "") == default_state)

        # Tag editor — single compact block that targets one state at a time.
        # Simpler than per-thumb editing and scales better when an asset has
        # many states. The state picker defaults to the default state since
        # that's what admins want to tag 95% of the time.
        _add_tag_editor(asset_id, states, default_state)

    # Place button
    var btn_container = CenterContainer.new()
    _content.add_child(btn_container)

    var place_btn = Button.new()
    place_btn.text = "Place on Map"
    place_btn.add_theme_color_override("font_color", COLOR_TEXT)
    place_btn.add_theme_font_override("font", _font)
    place_btn.add_theme_font_size_override("font_size", 18)
    place_btn.custom_minimum_size = Vector2(200, 0)

    var btn_style = StyleBoxFlat.new()
    btn_style.bg_color = COLOR_BTN_BG
    btn_style.border_width_left = 1
    btn_style.border_width_top = 1
    btn_style.border_width_right = 1
    btn_style.border_width_bottom = 1
    btn_style.border_color = COLOR_BTN_BORDER
    btn_style.corner_radius_left_top = 3
    btn_style.corner_radius_right_top = 3
    btn_style.corner_radius_left_bottom = 3
    btn_style.corner_radius_right_bottom = 3
    btn_style.content_margin_top = 8.0
    btn_style.content_margin_bottom = 8.0
    place_btn.add_theme_stylebox_override("normal", btn_style)

    var hover_style = btn_style.duplicate()
    hover_style.bg_color = Color(0.17, 0.17, 0.10, 1.0)
    place_btn.add_theme_stylebox_override("hover", hover_style)

    place_btn.pressed.connect(_on_place_pressed)
    btn_container.add_child(place_btn)

    visible = true

func _add_meta_line(container: VBoxContainer, text: String) -> void:
    var label = Label.new()
    label.text = text
    label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    label.add_theme_font_size_override("font_size", 12)
    container.add_child(label)

## ID row with a small "copy" button. Click copies the UUID to the clipboard.
func _add_id_row(container: VBoxContainer, asset_id: String) -> void:
    var row = HBoxContainer.new()
    row.add_theme_constant_override("separation", 8)
    container.add_child(row)

    var label = Label.new()
    label.text = "ID: " + asset_id
    label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    label.add_theme_font_size_override("font_size", 12)
    row.add_child(label)

    var copy_btn = Button.new()
    copy_btn.text = "copy"
    copy_btn.add_theme_color_override("font_color", COLOR_TEXT)
    copy_btn.add_theme_font_size_override("font_size", 11)

    var btn_style = StyleBoxFlat.new()
    btn_style.bg_color = COLOR_BTN_BG
    btn_style.border_width_left = 1
    btn_style.border_width_top = 1
    btn_style.border_width_right = 1
    btn_style.border_width_bottom = 1
    btn_style.border_color = COLOR_BTN_BORDER
    btn_style.corner_radius_left_top = 2
    btn_style.corner_radius_right_top = 2
    btn_style.corner_radius_left_bottom = 2
    btn_style.corner_radius_right_bottom = 2
    btn_style.content_margin_left = 6.0
    btn_style.content_margin_right = 6.0
    btn_style.content_margin_top = 2.0
    btn_style.content_margin_bottom = 2.0
    copy_btn.add_theme_stylebox_override("normal", btn_style)

    copy_btn.pressed.connect(func():
        DisplayServer.clipboard_set(asset_id)
        copy_btn.text = "copied"
        var t = Timer.new()
        t.wait_time = 1.0
        t.one_shot = true
        t.timeout.connect(func():
            copy_btn.text = "copy"
            t.queue_free()
        )
        copy_btn.add_child(t)
        t.start()
    )
    row.add_child(copy_btn)

func _add_state_thumb(container: HBoxContainer, state: Dictionary, is_default: bool) -> void:
    var state_name: String = state.get("state", "")

    var vbox = VBoxContainer.new()
    vbox.add_theme_constant_override("separation", 2)
    container.add_child(vbox)

    var thumb_panel = PanelContainer.new()
    var thumb_style = StyleBoxFlat.new()
    thumb_style.bg_color = Color(0.1, 0.08, 0.05, 1.0)
    thumb_style.border_width_left = 1
    thumb_style.border_width_top = 1
    thumb_style.border_width_right = 1
    thumb_style.border_width_bottom = 1
    if is_default:
        thumb_style.border_color = COLOR_STATE_DEFAULT
    else:
        thumb_style.border_color = COLOR_STATE_BORDER
    thumb_style.corner_radius_left_top = 2
    thumb_style.corner_radius_right_top = 2
    thumb_style.corner_radius_left_bottom = 2
    thumb_style.corner_radius_right_bottom = 2
    thumb_panel.add_theme_stylebox_override("panel", thumb_style)
    vbox.add_child(thumb_panel)

    var texture = Catalog.get_sprite_texture(state)
    if texture != null:
        var center = CenterContainer.new()
        center.custom_minimum_size = Vector2(STATE_THUMB_SIZE, STATE_THUMB_SIZE)
        thumb_panel.add_child(center)

        var tex_rect = TextureRect.new()
        tex_rect.texture = texture
        var native_size: Vector2 = texture.get_size()
        var max_dim: float = STATE_THUMB_SIZE - 4.0
        var scale_factor: float = minf(max_dim / native_size.x, max_dim / native_size.y)
        if scale_factor > 2.0:
            scale_factor = 2.0
        tex_rect.custom_minimum_size = native_size * scale_factor
        tex_rect.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
        tex_rect.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
        center.add_child(tex_rect)

        # Animate if multi-frame
        _animate_texture_rect(tex_rect, state, thumb_panel)

    var label = Label.new()
    label.text = state_name.to_upper()
    label.add_theme_font_size_override("font_size", 10)
    label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    if is_default:
        label.add_theme_color_override("font_color", COLOR_STATE_DEFAULT)
    else:
        label.add_theme_color_override("font_color", COLOR_TEXT_DIM)
    vbox.add_child(label)

## Add a timer-based animation to a TextureRect for multi-frame states.
func _animate_texture_rect(tex_rect: TextureRect, state_info: Dictionary, parent: Node) -> void:
    var frame_count: int = state_info.get("frame_count", 1)
    var frame_rate: float = state_info.get("frame_rate", 0.0)
    if frame_count <= 1 or frame_rate <= 0:
        return

    var sprite_frames: SpriteFrames = Catalog.get_sprite_frames(state_info)
    if sprite_frames == null:
        return

    var all_frames: Array = []
    for i in range(sprite_frames.get_frame_count("default")):
        all_frames.append(sprite_frames.get_frame_texture("default", i))
    if all_frames.size() <= 1:
        return

    var timer = Timer.new()
    timer.wait_time = 1.0 / frame_rate
    timer.autostart = true
    var frame_idx: Array = [0]
    timer.timeout.connect(func():
        frame_idx[0] = (frame_idx[0] + 1) % all_frames.size()
        tex_rect.texture = all_frames[frame_idx[0]]
    )
    parent.add_child(timer)

func _on_place_pressed() -> void:
    visible = false
    place_requested.emit(_current_asset_id)

# Tag editor state — rebuilt each time show_asset renders. Held as member
# vars so the state picker + apply/remove buttons can read the current
# selection without threading it through signal args.
var _tag_editor_asset_id: String = ""
var _tag_editor_states: Array = []
var _tag_editor_selected_state: String = ""
var _tag_editor_chips_box: HBoxContainer = null
var _tag_editor_add_dropdown: OptionButton = null
var _tag_editor_state_dropdown: OptionButton = null

## Compact state-tag editor. One state picker on top, then the current
## tags for that state as clickable chips (click to remove), then an
## "Add tag" dropdown/button. Uses the existing POST/DELETE endpoints
## and the WS asset_state_tags_updated event to refresh after mutations.
func _add_tag_editor(asset_id: String, states: Array, default_state: String) -> void:
    _tag_editor_asset_id = asset_id
    _tag_editor_states = states
    _tag_editor_selected_state = default_state

    var tags_header = Label.new()
    tags_header.text = "TAGS"
    tags_header.add_theme_color_override("font_color", COLOR_LABEL)
    tags_header.add_theme_font_size_override("font_size", 12)
    _content.add_child(tags_header)

    # State picker — only shown when there are 2+ states; single-state
    # assets render the state name as a label instead.
    if states.size() > 1:
        var picker_row = HBoxContainer.new()
        picker_row.add_theme_constant_override("separation", 6)
        _content.add_child(picker_row)
        var picker_lbl = Label.new()
        picker_lbl.text = "State"
        picker_lbl.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        picker_lbl.add_theme_font_size_override("font_size", 11)
        picker_row.add_child(picker_lbl)
        _tag_editor_state_dropdown = OptionButton.new()
        var default_idx: int = 0
        for i in range(states.size()):
            var sname: String = states[i].get("state", "")
            _tag_editor_state_dropdown.add_item(sname)
            if sname == default_state:
                default_idx = i
        _tag_editor_state_dropdown.select(default_idx)
        _tag_editor_state_dropdown.item_selected.connect(_on_tag_editor_state_selected)
        _tag_editor_state_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        picker_row.add_child(_tag_editor_state_dropdown)
    else:
        var single_lbl = Label.new()
        single_lbl.text = "State: " + default_state
        single_lbl.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        single_lbl.add_theme_font_size_override("font_size", 11)
        _content.add_child(single_lbl)

    _tag_editor_chips_box = HBoxContainer.new()
    _tag_editor_chips_box.add_theme_constant_override("separation", 4)
    _content.add_child(_tag_editor_chips_box)

    var add_row = HBoxContainer.new()
    add_row.add_theme_constant_override("separation", 6)
    _content.add_child(add_row)
    _tag_editor_add_dropdown = OptionButton.new()
    _tag_editor_add_dropdown.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    add_row.add_child(_tag_editor_add_dropdown)
    var add_btn = Button.new()
    add_btn.text = "Add tag"
    add_btn.add_theme_font_size_override("font_size", 11)
    add_btn.pressed.connect(_on_tag_editor_add_pressed)
    add_row.add_child(add_btn)

    _refresh_tag_editor_chips()
    _refresh_tag_editor_add_options()

func _on_tag_editor_state_selected(index: int) -> void:
    if _tag_editor_state_dropdown == null:
        return
    _tag_editor_selected_state = _tag_editor_state_dropdown.get_item_text(index)
    _refresh_tag_editor_chips()
    _refresh_tag_editor_add_options()

## Find the selected state dict in _tag_editor_states and return its tags
## array (empty when no tags or state missing).
func _current_state_tags() -> Array:
    for s in _tag_editor_states:
        if s.get("state", "") == _tag_editor_selected_state:
            var tags = s.get("tags", [])
            if tags is Array:
                return tags
            return []
    return []

func _refresh_tag_editor_chips() -> void:
    if _tag_editor_chips_box == null:
        return
    for child in _tag_editor_chips_box.get_children():
        child.queue_free()
    var current = _current_state_tags()
    if current.size() == 0:
        var empty = Label.new()
        empty.text = "(none)"
        empty.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        empty.add_theme_font_size_override("font_size", 11)
        _tag_editor_chips_box.add_child(empty)
        return
    for tag in current:
        # Build a pill chip with a separate X button — IMFellEnglish doesn't
        # include the ✕ glyph and the fallback renders as garbage, so we
        # use ASCII "x" in a flat button instead.
        var pill = PanelContainer.new()
        var pill_style = StyleBoxFlat.new()
        pill_style.bg_color = Color(0.23, 0.17, 0.08, 1.0)
        pill_style.border_width_left = 1
        pill_style.border_width_top = 1
        pill_style.border_width_right = 1
        pill_style.border_width_bottom = 1
        pill_style.border_color = COLOR_BTN_BORDER
        pill_style.corner_radius_left_top = 8
        pill_style.corner_radius_right_top = 8
        pill_style.corner_radius_left_bottom = 8
        pill_style.corner_radius_right_bottom = 8
        pill_style.content_margin_left = 8.0
        pill_style.content_margin_right = 4.0
        pill_style.content_margin_top = 2.0
        pill_style.content_margin_bottom = 2.0
        pill.add_theme_stylebox_override("panel", pill_style)
        var row = HBoxContainer.new()
        row.add_theme_constant_override("separation", 6)
        pill.add_child(row)
        var label = Label.new()
        label.text = str(tag)
        label.add_theme_color_override("font_color", COLOR_TEXT)
        label.add_theme_font_size_override("font_size", 11)
        row.add_child(label)
        var x_btn = Button.new()
        x_btn.text = "x"
        x_btn.flat = true
        x_btn.add_theme_color_override("font_color", COLOR_TEXT_DIM)
        x_btn.add_theme_color_override("font_hover_color", Color(1, 0.6, 0.5))
        x_btn.add_theme_font_size_override("font_size", 11)
        x_btn.custom_minimum_size = Vector2(14, 14)
        x_btn.focus_mode = Control.FOCUS_NONE
        x_btn.pressed.connect(_on_tag_chip_pressed.bind(str(tag)))
        row.add_child(x_btn)
        _tag_editor_chips_box.add_child(pill)

## Repopulate the "add" dropdown with tags from the Catalog allowlist minus
## the ones already set on the current state.
func _refresh_tag_editor_add_options() -> void:
    if _tag_editor_add_dropdown == null:
        return
    _tag_editor_add_dropdown.clear()
    var current := _current_state_tags()
    var current_set: Dictionary = {}
    for t in current:
        current_set[str(t)] = true
    for tag in Catalog.state_tags:
        if not current_set.has(str(tag)):
            _tag_editor_add_dropdown.add_item(str(tag))

func _on_tag_editor_add_pressed() -> void:
    if _tag_editor_add_dropdown == null or _tag_editor_add_dropdown.selected < 0:
        return
    var tag: String = _tag_editor_add_dropdown.get_item_text(_tag_editor_add_dropdown.selected)
    _post_tag(tag)

func _on_tag_chip_pressed(tag: String) -> void:
    _delete_tag(tag)

## POST /api/assets/{id}/states/{state}/tags — body: {"tag": "xxx"}.
## Response is 204; the WS asset_state_tags_updated event fans out the new
## tag set, and we update our local Catalog copy in the handler below.
func _post_tag(tag: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_tag_mutate_complete.bind(http))
    var headers: PackedStringArray = ["Content-Type: application/json"]
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    var url: String = Auth.api_base + "/api/assets/" + _tag_editor_asset_id + "/states/" + _tag_editor_selected_state + "/tags"
    var body: String = JSON.stringify({"tag": tag})
    var err = http.request(url, headers, HTTPClient.METHOD_POST, body)
    if err != OK:
        push_error("Failed to POST tag: " + str(err))

## DELETE /api/assets/{id}/states/{state}/tags/{tag}.
func _delete_tag(tag: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(_on_tag_mutate_complete.bind(http))
    var headers: PackedStringArray = []
    var auth_header: String = Auth.get_auth_header()
    if auth_header != "":
        headers.append("Authorization: " + auth_header)
    var url: String = Auth.api_base + "/api/assets/" + _tag_editor_asset_id + "/states/" + _tag_editor_selected_state + "/tags/" + tag
    var err = http.request(url, headers, HTTPClient.METHOD_DELETE)
    if err != OK:
        push_error("Failed to DELETE tag: " + str(err))

func _on_tag_mutate_complete(result: int, response_code: int, _headers: PackedStringArray, _body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()
    if not Auth.check_response(response_code):
        return
    if result != HTTPRequest.RESULT_SUCCESS or response_code >= 300:
        push_error("Tag mutation failed: code=" + str(response_code))
        return
    # The WS event asset_state_tags_updated will arrive separately and
    # refresh Catalog's cached tag set, at which point we re-render from
    # fresh state. Nothing to do here beyond error surfacing.

## Called via Catalog.state_tags_changed after the WS event refreshes the
## cached tag list. Catalog already updated its state dict; we just need
## to re-render chips if the popup is open on the affected asset/state.
func _on_catalog_state_tags_changed(asset_id: String, state: String, _tags: Array) -> void:
    if not visible or _tag_editor_asset_id != asset_id:
        return
    if _tag_editor_selected_state == state:
        _refresh_tag_editor_chips()
        _refresh_tag_editor_add_options()
