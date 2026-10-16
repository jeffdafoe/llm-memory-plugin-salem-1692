extends CanvasLayer

## The camp fire window (LLM-798). A click on the player's own camp fire while
## it is out (unlit, or burnt to ashes) opens it: "Sleeping with a fire will give
## you better rest." with [Light it] / [Leave] — every time, as it is the only
## place the player is told what the fire is for. Light it plays the FIRE game
## (repair_games.gd Fire, drawn by repair_stage.gd): strike a spark, then blow
## the ember into the gold. A win POSTs pc/camp/light; the engine lights the fire
## and its sprite turns on the object frame. A refusal (walked off, the fire lit
## meanwhile) shows the engine's line.
##
## Layer 4, the modal tier, styled like the door and repair panels; main.gd
## blocks world input while it is open (opened / closed).

const FONT_PATH := "res://assets/fonts/IMFellEnglish-Regular.ttf"
const TAP_SIZE := Vector2(132, 44)
const StageScript = preload("res://scripts/repair_stage.gd")

signal opened()
signal closed()
## The engine lit the fire.
signal lit()

## Test seam: when set, called with (on_done: Callable) in place of the POST;
## on_done takes (code: int, body: String).
var send_hook: Callable

var fire_id := ""

var root: Control = null
var backdrop: ColorRect = null
var sheet: PanelContainer = null
var title_label: Label = null
var prompt_label: Label = null
var hint_label: Label = null
var status_label: Label = null
var stage: Control = null
var light_button: Button = null
var leave_button: Button = null

var _font: Font = null
var _http: HTTPRequest = null
var _sending := false


func _ready() -> void:
    layer = 4
    _font = load(FONT_PATH) if ResourceLoader.exists(FONT_PATH) else ThemeDB.fallback_font
    _build_ui()
    visible = false
    set_process(false)


func _build_ui() -> void:
    root = Control.new()
    root.set_anchors_preset(Control.PRESET_FULL_RECT)
    root.mouse_filter = Control.MOUSE_FILTER_STOP
    add_child(root)

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
    for side in ["margin_left", "margin_right"]:
        pad.add_theme_constant_override(side, 18)
    pad.add_theme_constant_override("margin_top", 14)
    pad.add_theme_constant_override("margin_bottom", 14)
    sheet.add_child(pad)

    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 10)
    pad.add_child(vb)

    title_label = _label(20, Color(0.95, 0.82, 0.58))
    title_label.text = "Your camp fire"
    prompt_label = _label(16, Color(0.92, 0.84, 0.70))
    vb.add_child(title_label)
    vb.add_child(prompt_label)

    stage = Control.new()
    stage.set_script(StageScript)
    stage.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
    stage.visible = false
    vb.add_child(stage)
    stage.round_won.connect(_on_round_won)
    stage.finish_shown.connect(_on_finish_shown)

    hint_label = _label(14, Color(0.80, 0.72, 0.56))
    status_label = _label(14, Color(0.93, 0.62, 0.48))
    vb.add_child(hint_label)
    vb.add_child(status_label)

    var buttons := HBoxContainer.new()
    buttons.alignment = BoxContainer.ALIGNMENT_CENTER
    buttons.add_theme_constant_override("separation", 12)
    vb.add_child(buttons)
    light_button = _button("Light it", _on_light_pressed)
    leave_button = _button("Leave", close)
    buttons.add_child(light_button)
    buttons.add_child(leave_button)
    _apply_theme()


func _label(size: int, color: Color) -> Label:
    var l := Label.new()
    l.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    l.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    l.add_theme_font_override("font", _font)
    l.add_theme_font_size_override("font_size", OrientationGuard.text_size(size))
    l.add_theme_color_override("font_color", color)
    l.custom_minimum_size = Vector2(280, 0)
    return l


func _button(text: String, cb: Callable) -> Button:
    var b := Button.new()
    b.text = text
    b.custom_minimum_size = TAP_SIZE
    b.add_theme_font_override("font", _font)
    b.add_theme_font_size_override("font_size", OrientationGuard.text_size(17))
    b.pressed.connect(cb)
    return b


## The fire as it stands (`broken`) and burning (`sound`), as stage layers: the
## fire asset's unlit and burning sprites from the catalog, anchored as world.gd
## anchors a placement. Empty when the catalog does not have them.
static func fire_layers() -> Dictionary:
    var out := {"broken": [], "sound": []}
    var catalog = Engine.get_main_loop().root.get_node_or_null("Catalog") if Engine.get_main_loop() is SceneTree else null
    if catalog == null:
        return out
    var asset: Dictionary = catalog.assets.get(Camp.FIRE_ASSET_ID, {})
    var anchor := Vector2(float(asset.get("anchorX", asset.get("anchor_x", 0.5))), float(asset.get("anchorY", asset.get("anchor_y", 0.85))))
    for pair in [["broken", "unlit"], ["sound", "burning"]]:
        var info = catalog.get_state(Camp.FIRE_ASSET_ID, pair[1])
        if info == null:
            continue
        var tex = catalog.get_sprite_texture(info)
        if tex == null:
            continue
        out[pair[0]].append({"tex": tex, "pos": -tex.get_size() * anchor})
    return out


## Ask whether to light the player's fire, `fid`.
func open(fid: String) -> void:
    fire_id = fid
    prompt_label.text = Camp.FIRE_PROMPT
    prompt_label.visible = true
    stage.visible = false
    hint_label.text = ""
    status_label.text = ""
    light_button.visible = true
    light_button.disabled = false
    _sending = false
    set_process(false)
    if visible:
        return
    visible = true
    opened.emit()


func close() -> void:
    if not visible:
        return
    visible = false
    fire_id = ""
    set_process(false)
    if stage.game != null:
        stage.playing = false
    if _http != null:
        _http.cancel_request()
    _sending = false
    closed.emit()


func is_open() -> bool:
    return visible


func _on_light_pressed() -> void:
    var layers := fire_layers()
    prompt_label.visible = false
    light_button.visible = false
    status_label.text = ""
    stage.visible = true
    stage.setup("Your camp fire", "fire", layers["broken"], layers["sound"], 1, 0, "")
    stage.start_game()
    set_process(true)


func _process(_dt: float) -> void:
    if stage.game != null and stage.playing:
        hint_label.text = stage.game.hint()


## The fire caught: tell the engine. The game holds on the caught ember until
## the answer comes.
func _on_round_won(_perfect: bool) -> void:
    if _sending:
        return
    _sending = true
    hint_label.text = ""
    if send_hook.is_valid():
        send_hook.call(_on_light_response)
        return
    if _http == null:
        _http = HTTPRequest.new()
        _http.accept_gzip = false
        add_child(_http)
        _http.request_completed.connect(func(_result: int, code: int, _headers: PackedStringArray, body: PackedByteArray):
            _on_light_response(code, body.get_string_from_utf8()))
    _http.cancel_request()
    var err := _http.request(Auth.api_base + "/api/village/pc/camp/light", Auth.auth_headers(), HTTPClient.METHOD_POST, "")
    if err != OK:
        _on_light_response(0, "")


func _on_light_response(code: int, body: String) -> void:
    _sending = false
    if not visible:
        return
    if code >= 200 and code < 300:
        stage.finish(0)
        lit.emit()
        return
    if code == 401 or code == 403:
        Auth.check_response(code)
    stage.playing = false
    status_label.text = refusal_line(body)


## The engine's refusal as a line for the player, or a plain one when the body
## carries none.
static func refusal_line(body: String) -> String:
    var parsed = JSON.parse_string(body) if body != "" else null
    if parsed is Dictionary:
        var msg := str(parsed.get("error", "")).strip_edges()
        if msg != "":
            return msg.substr(0, 1).to_upper() + msg.substr(1)
    return "The fire would not take. Try again in a moment."


func _on_finish_shown() -> void:
    close()


func _on_backdrop_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
        close()
        get_viewport().set_input_as_handled()


func _unhandled_input(event: InputEvent) -> void:
    if not visible:
        return
    if event is InputEventKey and event.pressed and not event.echo:
        if event.keycode == KEY_ESCAPE:
            close()
            get_viewport().set_input_as_handled()
        elif stage.visible and (event.keycode == KEY_SPACE or event.keycode == KEY_ENTER):
            stage.press_key()
            get_viewport().set_input_as_handled()


# Brown period theme — the door and repair panels', so the modals match.
func _apply_theme() -> void:
    var st := StyleBoxFlat.new()
    st.bg_color = Color(0.115, 0.085, 0.055, 0.95)
    st.border_color = Color(0.55, 0.42, 0.24, 0.95)
    st.border_width_left = 2
    st.border_width_right = 2
    st.border_width_top = 2
    st.border_width_bottom = 2
    st.corner_radius_top_left = 10
    st.corner_radius_top_right = 10
    st.corner_radius_bottom_left = 10
    st.corner_radius_bottom_right = 10
    st.shadow_color = Color(0, 0, 0, 0.45)
    st.shadow_size = 18
    st.shadow_offset = Vector2(0, 6)
    sheet.add_theme_stylebox_override("panel", st)
    for b in [light_button, leave_button]:
        var bs := StyleBoxFlat.new()
        bs.bg_color = Color(0.30, 0.20, 0.11, 1.0)
        bs.border_color = Color(0.62, 0.47, 0.26, 1.0)
        bs.border_width_left = 1
        bs.border_width_right = 1
        bs.border_width_top = 1
        bs.border_width_bottom = 1
        bs.corner_radius_top_left = 6
        bs.corner_radius_top_right = 6
        bs.corner_radius_bottom_left = 6
        bs.corner_radius_bottom_right = 6
        var hover := bs.duplicate()
        hover.bg_color = Color(0.40, 0.27, 0.14, 1.0)
        b.add_theme_stylebox_override("normal", bs)
        b.add_theme_stylebox_override("hover", hover)
        b.add_theme_stylebox_override("pressed", hover)
        b.add_theme_stylebox_override("focus", bs)
        b.add_theme_color_override("font_color", Color(0.95, 0.86, 0.66))
