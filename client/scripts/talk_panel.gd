extends PanelContainer

## Talk panel (M6.7) — minimal UI for player conversation.
##
## Always-on panel docked to the right edge in play mode (and edit
## mode for now — gating to play-only is a future polish). Sections:
##   - Header: "You are <character_name> at <location>"
##   - Here: list of huddle members (NPCs + other PCs). Click an NPC
##     to select them as the whisper target; speak goes to the room.
##   - Recent: rolling log of speech around the PC (whispers + room
##     broadcasts via the world.npc_spoke signal).
##   - Input: text field + buttons "Whisper to X" / "Speak Aloud".
##
## State is fetched from POST /api/village/pc/me on a 10-second
## timer; ambient speech updates come immediately via the
## world.npc_spoke WS-derived signal so the recent log feels live.

const REFRESH_INTERVAL: float = 10.0

@onready var world: Node2D = get_node_or_null("/root/Main/World")

var _state: Dictionary = {}
var _selected_target_agent: String = ""
var _selected_target_name: String = ""
var _refresh_timer: float = 0.0

var _header_label: Label = null
var _members_list: VBoxContainer = null
var _replies_box: VBoxContainer = null
var _replies_scroll: ScrollContainer = null
var _input: LineEdit = null
var _whisper_btn: Button = null
var _speak_btn: Button = null
# Floating "Talk" reopen button — sibling Control on the same
# CanvasLayer, only visible when the panel is closed. Created lazily
# on first close so we don't pay the layout cost up front.
var _open_button: Button = null

func _ready() -> void:
    # Wider panel so the input + member buttons aren't cramped.
    custom_minimum_size = Vector2(360, 0)
    set_anchors_preset(Control.PRESET_RIGHT_WIDE)
    offset_left = -370
    offset_right = -10
    offset_top = 50
    offset_bottom = -10

    var bg := StyleBoxFlat.new()
    bg.bg_color = Color(0.10, 0.08, 0.06, 0.88)
    bg.border_color = Color(0.45, 0.35, 0.20, 1.0)
    bg.set_border_width_all(1)
    bg.content_margin_left = 8
    bg.content_margin_right = 8
    bg.content_margin_top = 8
    bg.content_margin_bottom = 8
    add_theme_stylebox_override("panel", bg)

    var box := VBoxContainer.new()
    box.add_theme_constant_override("separation", 6)
    add_child(box)

    # Header row — title label + close (x) button on the right.
    var header_row := HBoxContainer.new()
    header_row.add_theme_constant_override("separation", 6)
    box.add_child(header_row)

    _header_label = Label.new()
    _header_label.text = "Loading..."
    _header_label.add_theme_color_override("font_color", Color(0.95, 0.92, 0.80))
    _header_label.add_theme_font_size_override("font_size", 13)
    _header_label.autowrap_mode = TextServer.AUTOWRAP_WORD
    _header_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    header_row.add_child(_header_label)

    var close_btn := Button.new()
    close_btn.text = "x"
    close_btn.flat = true
    close_btn.add_theme_color_override("font_color", Color(0.78, 0.66, 0.45))
    close_btn.add_theme_color_override("font_hover_color", Color(1.0, 0.6, 0.5))
    close_btn.add_theme_font_size_override("font_size", 14)
    close_btn.custom_minimum_size = Vector2(20, 20)
    close_btn.focus_mode = Control.FOCUS_NONE
    close_btn.pressed.connect(_on_close_pressed)
    header_row.add_child(close_btn)

    var members_header := Label.new()
    members_header.text = "HERE"
    members_header.add_theme_color_override("font_color", Color(0.78, 0.66, 0.45))
    members_header.add_theme_font_size_override("font_size", 10)
    box.add_child(members_header)

    _members_list = VBoxContainer.new()
    _members_list.add_theme_constant_override("separation", 2)
    box.add_child(_members_list)

    var replies_header := Label.new()
    replies_header.text = "RECENT"
    replies_header.add_theme_color_override("font_color", Color(0.78, 0.66, 0.45))
    replies_header.add_theme_font_size_override("font_size", 10)
    box.add_child(replies_header)

    _replies_scroll = ScrollContainer.new()
    _replies_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    _replies_scroll.custom_minimum_size = Vector2(0, 220)
    _replies_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    box.add_child(_replies_scroll)

    _replies_box = VBoxContainer.new()
    _replies_box.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _replies_box.add_theme_constant_override("separation", 4)
    _replies_scroll.add_child(_replies_box)

    _input = LineEdit.new()
    _input.placeholder_text = "Type a line..."
    _input.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _input.text_submitted.connect(_on_text_submitted)
    box.add_child(_input)

    var btn_row := HBoxContainer.new()
    btn_row.add_theme_constant_override("separation", 4)
    box.add_child(btn_row)

    # Single Speak button. Label updates based on selection: addresses
    # the picked NPC if one is highlighted in HERE, otherwise broadcasts
    # to the room. One button replaces the prior Whisper/Speak Aloud
    # split — same backend, consolidated UI.
    _speak_btn = Button.new()
    _speak_btn.text = "Speak (to room)"
    _speak_btn.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    _speak_btn.pressed.connect(_on_speak_pressed)
    btn_row.add_child(_speak_btn)
    _whisper_btn = _speak_btn  # legacy ref retained for state-update code below

    if world != null and not world.npc_spoke.is_connected(_on_world_npc_spoke):
        world.npc_spoke.connect(_on_world_npc_spoke)

    refresh()

func _process(delta: float) -> void:
    _refresh_timer += delta
    if _refresh_timer >= REFRESH_INTERVAL:
        _refresh_timer = 0
        refresh()

## GET-equivalent (POST per project convention) of /api/village/pc/me.
## Updates header + member list. Hides the panel entirely when the
## response says exists=false (no PC seeded yet).
func refresh() -> void:
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, body):
        http.queue_free()
        if c >= 200 and c < 300:
            var parsed = JSON.parse_string(body.get_string_from_utf8())
            if parsed is Dictionary:
                _on_state(parsed)
    )
    var headers: PackedStringArray = ["Content-Type: application/json"]
    var auth: String = Auth.get_auth_header()
    if auth != "":
        headers.append("Authorization: " + auth)
    http.request(Auth.api_base + "/api/village/pc/me", headers, HTTPClient.METHOD_POST, "{}")

func _on_state(state: Dictionary) -> void:
    _state = state
    if not state.get("exists", false):
        _header_label.text = "No character (creation flow not yet wired)"
        return
    visible = true
    var char_name = str(state.get("character_name", "?"))
    var location = str(state.get("structure_name", "outside"))
    var home = str(state.get("home_name", ""))
    var header = "%s @ %s" % [char_name, location]
    if home != "" and home != location:
        header += " (lodging: %s)" % home
    _header_label.text = header

    # Rebuild member list. Whisper toggles persist iff the same NPC
    # is still here; otherwise reset.
    for c in _members_list.get_children():
        c.queue_free()
    var still_here := false
    var members: Array = state.get("huddle_members", [])
    if members.is_empty():
        var none = Label.new()
        none.text = "  (no one nearby)"
        none.add_theme_color_override("font_color", Color(0.65, 0.55, 0.40))
        _members_list.add_child(none)
    for m in members:
        var name_str = str(m.get("name", "?"))
        var target = str(m.get("target_agent", ""))
        var btn = Button.new()
        btn.text = name_str
        btn.toggle_mode = true
        if target != "" and target == _selected_target_agent:
            btn.button_pressed = true
            still_here = true
        if target == "":
            btn.disabled = true
            btn.tooltip_text = "Cannot whisper to other players (yet)"
        var t_target: String = target
        var t_name: String = name_str
        btn.pressed.connect(func():
            _selected_target_agent = t_target
            _selected_target_name = t_name
            refresh()
        )
        _members_list.add_child(btn)
    if not still_here:
        _selected_target_agent = ""
        _selected_target_name = ""
    # Single Speak button: label tracks current selection. Always
    # enabled — broadcast is the fallback when no NPC is targeted.
    _speak_btn.text = ("Speak to " + _selected_target_name) if _selected_target_name != "" else "Speak (to room)"

func _on_text_submitted(text: String) -> void:
    _do_speak_smart(text)

func _on_speak_pressed() -> void:
    _do_speak_smart(_input.text)

## Single entry point — addressed if a target is selected, broadcast
## otherwise. Replaces the prior whisper/speak split.
func _do_speak_smart(text: String) -> void:
    if _selected_target_agent != "":
        _do_whisper(text)
    else:
        _do_speak(text)

func _do_whisper(text: String) -> void:
    if text.strip_edges() == "" or _selected_target_agent == "":
        return
    # No local echo — /pc/say writes an audit_log row that fans out via
    # the npc_spoke WS event. _on_world_npc_spoke renders the line once.
    # Adding a local echo here would duplicate it.
    _input.text = ""
    _post_pc_chat("/api/village/pc/say", JSON.stringify({
        "target": _selected_target_agent,
        "text": text,
    }), true)

func _do_speak(text: String) -> void:
    if text.strip_edges() == "":
        return
    _input.text = ""
    _post_pc_chat("/api/village/pc/speak", JSON.stringify({
        "text": text,
    }), false)

func _post_pc_chat(path: String, body: String, expect_reply: bool) -> void:
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    http.request_completed.connect(func(_r, c, _h, resp_body):
        http.queue_free()
        if c < 200 or c >= 300:
            _append_reply("[server error %d]" % c, Color(1.0, 0.5, 0.5))
            return
        if expect_reply:
            var parsed = JSON.parse_string(resp_body.get_string_from_utf8())
            if parsed is Dictionary:
                var reply = parsed.get("reply", null)
                if reply is Dictionary:
                    var reply_text = str(reply.get("text", "")).strip_edges()
                    if reply_text != "":
                        _append_reply(_selected_target_name + ": " + reply_text, Color(0.95, 0.92, 0.80))
    )
    var headers: PackedStringArray = ["Content-Type: application/json"]
    var auth: String = Auth.get_auth_header()
    if auth != "":
        headers.append("Authorization: " + auth)
    http.request(Auth.api_base + path, headers, HTTPClient.METHOD_POST, body)

## Close button — hide the panel and surface the floating "Talk"
## reopen button. Created lazily so the layout doesn't carry a
## hidden Control until the user actually closes the panel once.
func _on_close_pressed() -> void:
    visible = false
    if _open_button == null:
        _open_button = Button.new()
        _open_button.text = "Talk"
        _open_button.set_anchors_preset(Control.PRESET_TOP_RIGHT)
        _open_button.offset_top = 60
        _open_button.offset_left = -80
        _open_button.offset_right = -10
        _open_button.offset_bottom = 90
        _open_button.add_theme_font_size_override("font_size", 12)
        var bg := StyleBoxFlat.new()
        bg.bg_color = Color(0.10, 0.08, 0.06, 0.88)
        bg.border_color = Color(0.45, 0.35, 0.20, 1.0)
        bg.set_border_width_all(1)
        _open_button.add_theme_stylebox_override("normal", bg)
        _open_button.add_theme_color_override("font_color", Color(0.95, 0.92, 0.80))
        _open_button.pressed.connect(_on_open_pressed)
        get_parent().add_child(_open_button)
    _open_button.visible = true

func _on_open_pressed() -> void:
    visible = true
    if _open_button != null:
        _open_button.visible = false
    refresh()

## WS-derived signal — any speech broadcast in the village. We render
## all of it for now (no proximity filter); when speech bubbles land
## the bubble will handle proximity, this panel keeps the full log.
func _on_world_npc_spoke(name: String, text: String, _kind: String) -> void:
    _append_reply(name + ": " + text, Color(0.95, 0.92, 0.80))

func _append_reply(text: String, color: Color) -> void:
    var label = Label.new()
    label.text = text
    label.autowrap_mode = TextServer.AUTOWRAP_WORD
    label.add_theme_color_override("font_color", color)
    label.add_theme_font_size_override("font_size", 12)
    _replies_box.add_child(label)
    while _replies_box.get_child_count() > 80:
        _replies_box.get_child(0).queue_free()
    await get_tree().process_frame
    var sb = _replies_scroll.get_v_scroll_bar()
    if sb != null:
        _replies_scroll.scroll_vertical = int(sb.max_value)
