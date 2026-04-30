extends CanvasLayer

## Talk panel — Salem 1692 conversation drawer.
##
## Broadcast-only player chat. Nearby members are informational chips, not
## targeting buttons; the LLMs decide who's being addressed from context.
##
## Closed state shows a "Talk (N nearby)" launcher pill. Open state slides up
## a bottom drawer (desktop, anchored bottom-right) or a 60vh bottom sheet
## (mobile, full-width). The user closes/opens it explicitly — the refresh
## timer never forces visibility.
##
## Mounted as a CanvasLayer from main.gd (talk_panel_layer). The script
## owns layer ordering itself.
##
## Runtime dependencies:
##   - /root/Auth (api_base + get_auth_header())
##   - /root/Main/World.npc_spoke(name, text, kind) signal
##   - POST /api/village/pc/me  → state
##   - POST /api/village/pc/speak {text}  → broadcast

const REFRESH_INTERVAL := 10.0
const DESKTOP_MIN_WIDTH := 580.0
const DESKTOP_MIN_HEIGHT := 400.0
const MOBILE_BREAKPOINT := 720.0
const MOBILE_SHEET_VH := 0.60
const MOBILE_SHEET_FOCUSED_VH := 0.82
const MAX_LOG_LINES := 80

var root: Control

var launcher_anchor: MarginContainer
var talk_launcher: Button

var sheet_anchor: MarginContainer
var talk_sheet: PanelContainer

var context_label: Label
var subcontext_label: Label
var close_button: Button
var nearby_scroll: ScrollContainer
var nearby_flow: HFlowContainer
var log_scroll: ScrollContainer
var log_vbox: VBoxContainer
var speech_input: TextEdit
var speak_button: Button

var refresh_timer: Timer
var http_me: HTTPRequest
var http_speak: HTTPRequest

var pc_exists := false
var character_name := ""
var structure_name := ""
var home_name := ""
var huddle_members: Array = []

# Tracks which structure's recent_speech we've already loaded so we don't
# duplicate the backload across the 10s polling cycle. When the player walks
# into a new structure, this changes and we clear+reload the log.
var loaded_structure_id := ""

var is_open := false
var user_closed := false
var has_ever_seen_huddle := false
var first_encounter_pulse_done := false
var unread_count := 0
var is_mobile := false
var input_focused := false
var _pending_focus := false


func _ready() -> void:
    layer = 4

    _build_tree()
    _apply_theme()
    _connect_signals()
    _connect_world_signal()

    get_viewport().size_changed.connect(_on_viewport_size_changed)
    _update_responsive_layout()

    refresh_timer.start()
    _refresh_state()
    _update_visibility_from_state()


func _build_tree() -> void:
    root = Control.new()
    root.name = "TalkRoot"
    root.set_anchors_preset(Control.PRESET_FULL_RECT)
    # The root must let mouse events fall through everywhere except the actual
    # interactive controls; otherwise we eat clicks meant for the world.
    root.mouse_filter = Control.MOUSE_FILTER_IGNORE
    add_child(root)

    _build_launcher()
    _build_sheet()

    http_me = HTTPRequest.new()
    add_child(http_me)

    http_speak = HTTPRequest.new()
    add_child(http_speak)

    refresh_timer = Timer.new()
    refresh_timer.wait_time = REFRESH_INTERVAL
    refresh_timer.one_shot = false
    add_child(refresh_timer)


func _build_launcher() -> void:
    launcher_anchor = MarginContainer.new()
    launcher_anchor.name = "LauncherAnchor"
    launcher_anchor.set_anchors_preset(Control.PRESET_FULL_RECT)
    launcher_anchor.mouse_filter = Control.MOUSE_FILTER_IGNORE
    root.add_child(launcher_anchor)

    var outer := VBoxContainer.new()
    outer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    outer.size_flags_vertical = Control.SIZE_EXPAND_FILL
    outer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    launcher_anchor.add_child(outer)

    var top_spacer := Control.new()
    top_spacer.size_flags_vertical = Control.SIZE_EXPAND_FILL
    top_spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    outer.add_child(top_spacer)

    var row := HBoxContainer.new()
    row.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.mouse_filter = Control.MOUSE_FILTER_IGNORE
    outer.add_child(row)

    var left_spacer := Control.new()
    left_spacer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    left_spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    row.add_child(left_spacer)

    talk_launcher = Button.new()
    talk_launcher.name = "TalkLauncher"
    talk_launcher.text = "Talk"
    talk_launcher.custom_minimum_size = Vector2(150, 48)
    talk_launcher.focus_mode = Control.FOCUS_ALL
    talk_launcher.mouse_filter = Control.MOUSE_FILTER_STOP
    row.add_child(talk_launcher)


func _build_sheet() -> void:
    sheet_anchor = MarginContainer.new()
    sheet_anchor.name = "SheetAnchor"
    sheet_anchor.set_anchors_preset(Control.PRESET_FULL_RECT)
    sheet_anchor.mouse_filter = Control.MOUSE_FILTER_IGNORE
    sheet_anchor.visible = false
    root.add_child(sheet_anchor)

    var outer := VBoxContainer.new()
    outer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    outer.size_flags_vertical = Control.SIZE_EXPAND_FILL
    outer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    sheet_anchor.add_child(outer)

    var top_spacer := Control.new()
    top_spacer.size_flags_vertical = Control.SIZE_EXPAND_FILL
    top_spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    outer.add_child(top_spacer)

    var row := HBoxContainer.new()
    row.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.mouse_filter = Control.MOUSE_FILTER_IGNORE
    outer.add_child(row)

    var left_spacer := Control.new()
    left_spacer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    left_spacer.mouse_filter = Control.MOUSE_FILTER_IGNORE
    row.add_child(left_spacer)

    talk_sheet = PanelContainer.new()
    talk_sheet.name = "TalkSheet"
    talk_sheet.mouse_filter = Control.MOUSE_FILTER_STOP
    talk_sheet.size_flags_horizontal = Control.SIZE_SHRINK_END
    talk_sheet.size_flags_vertical = Control.SIZE_SHRINK_END
    talk_sheet.custom_minimum_size = Vector2(DESKTOP_MIN_WIDTH, DESKTOP_MIN_HEIGHT)
    row.add_child(talk_sheet)

    var pad := MarginContainer.new()
    pad.add_theme_constant_override("margin_left", 14)
    pad.add_theme_constant_override("margin_right", 14)
    pad.add_theme_constant_override("margin_top", 8)
    pad.add_theme_constant_override("margin_bottom", 12)
    talk_sheet.add_child(pad)

    var vbox := VBoxContainer.new()
    vbox.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    vbox.size_flags_vertical = Control.SIZE_EXPAND_FILL
    vbox.add_theme_constant_override("separation", 6)
    pad.add_child(vbox)

    _build_header(vbox)
    _build_nearby(vbox)
    _build_log(vbox)
    _build_input(vbox)


func _build_header(parent: Control) -> void:
    # Compact one-line header: just the room name, dim and small, with the
    # close button on the right. Player name dropped (the user knows who
    # they are), lodging dropped (low value). Sits tight against the top
    # border so the chips effectively become the visual top of the panel.
    var header := HBoxContainer.new()
    header.custom_minimum_size = Vector2(0, 20)
    header.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    parent.add_child(header)

    context_label = Label.new()
    context_label.clip_text = true
    context_label.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
    context_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    context_label.add_theme_font_size_override("font_size", 11)
    header.add_child(context_label)

    # Kept around so existing _update_context_labels code that touches .text
    # continues to compile, but never added to the layout.
    subcontext_label = Label.new()
    subcontext_label.visible = false

    close_button = Button.new()
    close_button.text = "×"
    close_button.custom_minimum_size = Vector2(28, 20)
    close_button.focus_mode = Control.FOCUS_ALL
    close_button.mouse_filter = Control.MOUSE_FILTER_STOP
    close_button.add_theme_font_size_override("font_size", 14)
    header.add_child(close_button)


func _build_nearby(parent: Control) -> void:
    nearby_scroll = ScrollContainer.new()
    nearby_scroll.custom_minimum_size = Vector2(0, 30)
    nearby_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_AUTO
    nearby_scroll.vertical_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    parent.add_child(nearby_scroll)

    nearby_flow = HFlowContainer.new()
    nearby_flow.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    nearby_flow.add_theme_constant_override("h_separation", 6)
    nearby_flow.add_theme_constant_override("v_separation", 6)
    nearby_scroll.add_child(nearby_flow)


func _build_log(parent: Control) -> void:
    log_scroll = ScrollContainer.new()
    log_scroll.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    log_scroll.size_flags_vertical = Control.SIZE_EXPAND_FILL
    log_scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
    log_scroll.vertical_scroll_mode = ScrollContainer.SCROLL_MODE_AUTO
    parent.add_child(log_scroll)

    log_vbox = VBoxContainer.new()
    log_vbox.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    log_vbox.add_theme_constant_override("separation", 8)
    log_scroll.add_child(log_vbox)


func _build_input(parent: Control) -> void:
    var row := HBoxContainer.new()
    row.custom_minimum_size = Vector2(0, 48)
    row.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    row.add_theme_constant_override("separation", 8)
    parent.add_child(row)

    speech_input = TextEdit.new()
    speech_input.placeholder_text = "Speak to those gathered here…"
    speech_input.wrap_mode = TextEdit.LINE_WRAPPING_BOUNDARY
    speech_input.custom_minimum_size = Vector2(0, 44)
    speech_input.size_flags_horizontal = Control.SIZE_EXPAND_FILL
    speech_input.size_flags_vertical = Control.SIZE_EXPAND_FILL
    speech_input.focus_mode = Control.FOCUS_ALL
    speech_input.mouse_filter = Control.MOUSE_FILTER_STOP
    speech_input.add_theme_font_size_override("font_size", 13)
    row.add_child(speech_input)

    speak_button = Button.new()
    speak_button.text = "Speak"
    speak_button.custom_minimum_size = Vector2(88, 44)
    speak_button.focus_mode = Control.FOCUS_ALL
    speak_button.mouse_filter = Control.MOUSE_FILTER_STOP
    row.add_child(speak_button)


func _connect_signals() -> void:
    talk_launcher.pressed.connect(open)
    close_button.pressed.connect(close)
    speak_button.pressed.connect(_on_speak_pressed)

    refresh_timer.timeout.connect(_refresh_state)
    http_me.request_completed.connect(_on_me_completed)
    http_speak.request_completed.connect(_on_speak_completed)

    speech_input.focus_entered.connect(_on_input_focus_entered)
    speech_input.focus_exited.connect(_on_input_focus_exited)
    speech_input.gui_input.connect(_on_speech_input_gui_input)


func _connect_world_signal() -> void:
    var world := get_node_or_null("/root/Main/World")
    if world == null:
        return
    if world.has_signal("npc_spoke"):
        world.npc_spoke.connect(_on_npc_spoke)
    if world.has_signal("room_event"):
        world.room_event.connect(_on_room_event)


# talk_sheet is the actual visible chat panel (the bottom-right rounded
# rectangle). main.gd registers it with the camera so wheel scrolling
# over the open sheet scrolls the chat log instead of zooming the map.
# Visibility is gated by sheet_anchor; closing the panel hides
# sheet_anchor which makes is_visible_in_tree() return false on
# talk_sheet, so registration "just works" — we don't have to
# re-register on every open/close.
func get_input_eating_control() -> Control:
    return talk_sheet


func open() -> void:
    if not pc_exists or huddle_members.is_empty():
        return

    is_open = true
    user_closed = false
    unread_count = 0

    sheet_anchor.visible = true
    talk_launcher.visible = false

    _update_launcher_text()
    _focus_input_after_open()
    _scroll_log_to_bottom_deferred()


func close() -> void:
    is_open = false
    user_closed = true
    sheet_anchor.visible = false
    _update_visibility_from_state()


func _focus_input_after_open() -> void:
    _pending_focus = true
    call_deferred("_deferred_focus_input")


func _deferred_focus_input() -> void:
    # First focus pass on the next frame after show().
    await get_tree().process_frame

    if not is_open:
        return

    speech_input.grab_focus()
    call_deferred("_second_deferred_focus_input")


func _second_deferred_focus_input() -> void:
    # Second focus pass — HTML5 sometimes drops the first one.
    if not is_open:
        return

    speech_input.grab_focus()
    _pending_focus = false


func _refresh_state() -> void:
    var err := http_me.request(
        _api_url("/api/village/pc/me"),
        _auth_headers(),
        HTTPClient.METHOD_POST,
        ""
    )

    if err != OK:
        push_warning("TalkPanel pc/me request failed: %s" % err)


func _on_me_completed(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    if result != HTTPRequest.RESULT_SUCCESS or response_code < 200 or response_code >= 300:
        _set_no_pc_state()
        return

    var data = JSON.parse_string(body.get_string_from_utf8())
    if typeof(data) != TYPE_DICTIONARY:
        return

    _apply_pc_state(data)


func _apply_pc_state(data: Dictionary) -> void:
    pc_exists = bool(data.get("exists", false))

    if not pc_exists:
        _set_no_pc_state()
        return

    character_name = str(data.get("character_name", ""))
    structure_name = str(data.get("structure_name", ""))
    home_name = str(data.get("home_name", ""))

    var members = data.get("huddle_members", [])
    if typeof(members) == TYPE_ARRAY:
        huddle_members = members
    else:
        huddle_members = []

    _maybe_apply_recent_speech(data)
    _update_context_labels()
    _update_nearby_chips()
    _update_launcher_text()
    _update_visibility_from_state()
    _maybe_auto_attention_for_first_encounter()


# When the player's inside_structure_id changes (or first arrives), clear
# the log and replay the room's recent speech as a backload. The room
# metaphor: walking into a tavern, you hear what's been said here lately.
# Skipped on subsequent polls of the same structure to avoid duplicates
# layering on top of live npc_spoke events.
func _maybe_apply_recent_speech(data: Dictionary) -> void:
    var current_structure := str(data.get("inside_structure_id", ""))
    if current_structure == loaded_structure_id:
        return

    loaded_structure_id = current_structure

    # Wipe whatever's there from the previous room — fresh ears for a new
    # space. Live npc_spoke events that were happening in the old place
    # aren't relevant once the PC has moved.
    for child in log_vbox.get_children():
        child.queue_free()

    if current_structure.is_empty():
        return

    var recent = data.get("recent_speech", [])
    if typeof(recent) != TYPE_ARRAY:
        return

    for entry in recent:
        if typeof(entry) != TYPE_DICTIONARY:
            continue
        var speaker := str(entry.get("speaker_name", ""))
        var text := str(entry.get("text", ""))
        var kind := str(entry.get("kind", "npc"))
        if speaker.is_empty() or text.is_empty():
            continue
        _append_log_line(speaker, text, kind, true)


func _set_no_pc_state() -> void:
    pc_exists = false
    huddle_members = []
    is_open = false
    sheet_anchor.visible = false
    talk_launcher.visible = false
    loaded_structure_id = ""
    _update_context_labels()
    _clear_nearby_chips()


func _update_visibility_from_state() -> void:
    if not pc_exists or huddle_members.is_empty():
        is_open = false
        sheet_anchor.visible = false
        talk_launcher.visible = false
        return

    if is_open:
        sheet_anchor.visible = true
        talk_launcher.visible = false
    else:
        sheet_anchor.visible = false
        talk_launcher.visible = true


func _update_context_labels() -> void:
    if not pc_exists:
        context_label.text = ""
        subcontext_label.text = ""
        return

    var where := "the village"
    if not structure_name.is_empty():
        where = structure_name

    context_label.text = where
    subcontext_label.text = ""


func _update_nearby_chips() -> void:
    _clear_nearby_chips()

    for member in huddle_members:
        if typeof(member) != TYPE_DICTIONARY:
            continue

        var member_name := str(member.get("name", "Unknown"))
        var role := str(member.get("role", ""))
        var chip_text := member_name
        if not role.is_empty():
            chip_text = "%s · %s" % [member_name, role]
        nearby_flow.add_child(_make_chip(chip_text))


func _clear_nearby_chips() -> void:
    for child in nearby_flow.get_children():
        child.queue_free()


func _make_chip(text: String) -> Control:
    # PanelContainer + Label is more reliable than a Label with a stylebox
    # override, especially across HTML5 themes.
    var panel := PanelContainer.new()
    panel.mouse_filter = Control.MOUSE_FILTER_IGNORE

    var style := StyleBoxFlat.new()
    style.bg_color = Color(0.18, 0.13, 0.08, 0.95)
    style.border_color = Color(0.42, 0.32, 0.19, 0.95)
    style.border_width_left = 1
    style.border_width_right = 1
    style.border_width_top = 1
    style.border_width_bottom = 1
    style.corner_radius_top_left = 999
    style.corner_radius_top_right = 999
    style.corner_radius_bottom_left = 999
    style.corner_radius_bottom_right = 999
    style.content_margin_left = 7
    style.content_margin_right = 7
    style.content_margin_top = 1
    style.content_margin_bottom = 1
    panel.add_theme_stylebox_override("panel", style)

    var label := Label.new()
    label.text = text
    label.mouse_filter = Control.MOUSE_FILTER_IGNORE
    label.add_theme_color_override("font_color", Color(0.78, 0.68, 0.50, 1.0))
    label.add_theme_font_size_override("font_size", 11)
    panel.add_child(label)

    return panel


func _on_speak_pressed() -> void:
    _send_current_text()


func _on_speech_input_gui_input(event: InputEvent) -> void:
    # Enter sends; Shift+Enter inserts a newline.
    if event is InputEventKey:
        var key := event as InputEventKey
        if key.pressed and not key.echo:
            if key.keycode == KEY_ENTER or key.keycode == KEY_KP_ENTER:
                if key.shift_pressed:
                    return

                get_viewport().set_input_as_handled()
                _send_current_text()


func _send_current_text() -> void:
    var text := speech_input.text.strip_edges()
    if text.is_empty():
        _refocus_if_open()
        return

    speech_input.text = ""
    # No local echo — /pc/speak writes an audit_log row that fans out via the
    # npc_spoke WS event, which _on_npc_spoke renders. Local echo here would
    # duplicate the line as "You" and then the player's character name.
    _post_speak(text)
    _refocus_if_open()


func _post_speak(text: String) -> void:
    var body := JSON.stringify({ "text": text })

    speak_button.disabled = true

    var err := http_speak.request(
        _api_url("/api/village/pc/speak"),
        _auth_headers(),
        HTTPClient.METHOD_POST,
        body
    )

    if err != OK:
        speak_button.disabled = false
        push_warning("TalkPanel speak request failed: %s" % err)


func _on_speak_completed(result: int, response_code: int, _headers: PackedStringArray, body: PackedByteArray) -> void:
    speak_button.disabled = false

    if result != HTTPRequest.RESULT_SUCCESS or response_code < 200 or response_code >= 300:
        push_warning("TalkPanel speak failed: code=%s body=%s" % [
            response_code,
            body.get_string_from_utf8()
        ])


func _on_npc_spoke(speaker_name: String, text: String, kind: String = "") -> void:
    # WS speech kinds are "npc" | "player"; normalize to the panel's
    # speech_npc / speech_player kinds so render logic is uniform with
    # the backload entries.
    var panel_kind := "speech_player" if kind == "player" else "speech_npc"
    _append_log_line(speaker_name, text, panel_kind)


# Generic room-event handler. Engine emits these for narration-worthy
# things that aren't speech (acts, departures, eventually arrivals/pays).
# Each event scopes itself to a structure_id; we ignore events outside
# the room the player is currently in.
func _on_room_event(data: Dictionary) -> void:
    var event_structure := str(data.get("structure_id", ""))
    if event_structure != loaded_structure_id:
        return
    var actor_name := str(data.get("actor_name", ""))
    var text := str(data.get("text", ""))
    var kind := str(data.get("kind", "act"))
    if actor_name.is_empty() or text.is_empty():
        return
    _append_log_line(actor_name, text, kind)


func _append_log_line(speaker: String, text: String, kind: String = "", is_backload: bool = false) -> void:
    var was_at_bottom := _is_log_near_bottom()

    # Narration kinds (act, departure, eventually arrival/pay) render as a
    # single dimmer line — text is pre-rendered server-side and embeds the
    # actor's name, so no separate name label. Speech kinds render as
    # name + quoted text, color-coded for player vs NPC.
    var is_narration := kind == "act" or kind == "departure" or kind == "arrival"

    var entry: Node
    if is_narration:
        var narr := Label.new()
        narr.text = text
        narr.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
        narr.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        narr.add_theme_color_override("font_color", Color(0.58, 0.51, 0.39, 1.0))
        narr.add_theme_font_size_override("font_size", 13)
        entry = narr
    else:
        var vbox := VBoxContainer.new()
        vbox.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        vbox.add_theme_constant_override("separation", 2)

        var name_label := Label.new()
        name_label.text = speaker
        name_label.clip_text = true
        name_label.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
        name_label.add_theme_font_size_override("font_size", 13)

        var text_label := Label.new()
        text_label.text = "“%s”" % text
        text_label.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
        text_label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
        text_label.add_theme_font_size_override("font_size", 13)

        if kind == "speech_player" or kind == "player":
            name_label.add_theme_color_override("font_color", Color(0.95, 0.78, 0.45, 1.0))
            text_label.add_theme_color_override("font_color", Color(0.95, 0.86, 0.68, 1.0))
        else:
            name_label.add_theme_color_override("font_color", Color(0.70, 0.58, 0.39, 1.0))
            text_label.add_theme_color_override("font_color", Color(0.82, 0.76, 0.64, 1.0))

        vbox.add_child(name_label)
        vbox.add_child(text_label)
        entry = vbox

    log_vbox.add_child(entry)

    while log_vbox.get_child_count() > MAX_LOG_LINES:
        log_vbox.get_child(0).queue_free()

    if is_open:
        if was_at_bottom:
            _scroll_log_to_bottom_deferred()
    elif not is_backload:
        # Backload entries are historical, not new — don't pulse the
        # launcher's "N new" badge as if they just happened.
        unread_count += 1
        _update_launcher_text()


func _is_log_near_bottom() -> bool:
    var bar := log_scroll.get_v_scroll_bar()
    if bar == null:
        return true

    return bar.value >= bar.max_value - bar.page - 24.0


func _scroll_log_to_bottom_deferred() -> void:
    call_deferred("_scroll_log_to_bottom")


func _scroll_log_to_bottom() -> void:
    await get_tree().process_frame
    var bar := log_scroll.get_v_scroll_bar()
    if bar != null:
        bar.value = bar.max_value


func _update_launcher_text() -> void:
    var count := huddle_members.size()

    if count <= 0:
        talk_launcher.text = "Talk"
    elif unread_count > 0:
        talk_launcher.text = "Talk (%d nearby) • %d new" % [count, unread_count]
    else:
        talk_launcher.text = "Talk (%d nearby)" % count


func _maybe_auto_attention_for_first_encounter() -> void:
    if huddle_members.is_empty():
        return

    if has_ever_seen_huddle:
        return

    has_ever_seen_huddle = true

    if not first_encounter_pulse_done:
        first_encounter_pulse_done = true
        call_deferred("_pulse_launcher")


func _pulse_launcher() -> void:
    if not is_instance_valid(talk_launcher):
        return

    await get_tree().process_frame

    talk_launcher.pivot_offset = talk_launcher.size * 0.5

    var tween := create_tween()
    tween.set_loops(3)
    tween.tween_property(talk_launcher, "scale", Vector2(1.06, 1.06), 0.22)
    tween.tween_property(talk_launcher, "scale", Vector2.ONE, 0.22)


func _on_input_focus_entered() -> void:
    input_focused = true
    _update_responsive_layout()

    if is_mobile:
        call_deferred("_mobile_refit_after_keyboard")


func _on_input_focus_exited() -> void:
    input_focused = false
    _update_responsive_layout()


func _mobile_refit_after_keyboard() -> void:
    # Two frames: first lets the OS keyboard come up, second lets the resulting
    # viewport size_changed propagate before we re-fit.
    await get_tree().process_frame
    await get_tree().process_frame

    if is_mobile and is_open:
        _update_responsive_layout()


func _refocus_if_open() -> void:
    if is_open:
        call_deferred("_second_deferred_focus_input")


func _on_viewport_size_changed() -> void:
    _update_responsive_layout()


func _update_responsive_layout() -> void:
    var size := get_viewport().get_visible_rect().size
    is_mobile = size.x <= MOBILE_BREAKPOINT

    if is_mobile:
        launcher_anchor.add_theme_constant_override("margin_left", 12)
        launcher_anchor.add_theme_constant_override("margin_right", 12)
        launcher_anchor.add_theme_constant_override("margin_top", 12)
        launcher_anchor.add_theme_constant_override("margin_bottom", 14)

        sheet_anchor.add_theme_constant_override("margin_left", 0)
        sheet_anchor.add_theme_constant_override("margin_right", 0)
        sheet_anchor.add_theme_constant_override("margin_top", 0)
        sheet_anchor.add_theme_constant_override("margin_bottom", 0)

        talk_sheet.size_flags_horizontal = Control.SIZE_EXPAND_FILL

        var vh := MOBILE_SHEET_VH
        if input_focused:
            vh = MOBILE_SHEET_FOCUSED_VH
        talk_sheet.custom_minimum_size = Vector2(0, max(320.0, size.y * vh))

        talk_launcher.custom_minimum_size = Vector2(180, 52)
    else:
        launcher_anchor.add_theme_constant_override("margin_left", 18)
        launcher_anchor.add_theme_constant_override("margin_right", 22)
        launcher_anchor.add_theme_constant_override("margin_top", 18)
        launcher_anchor.add_theme_constant_override("margin_bottom", 22)

        sheet_anchor.add_theme_constant_override("margin_left", 18)
        sheet_anchor.add_theme_constant_override("margin_right", 22)
        sheet_anchor.add_theme_constant_override("margin_top", 18)
        sheet_anchor.add_theme_constant_override("margin_bottom", 22)

        talk_sheet.size_flags_horizontal = Control.SIZE_SHRINK_END
        talk_sheet.custom_minimum_size = Vector2(DESKTOP_MIN_WIDTH, DESKTOP_MIN_HEIGHT)

        talk_launcher.custom_minimum_size = Vector2(150, 48)


func _apply_theme() -> void:
    var sheet_style := StyleBoxFlat.new()
    sheet_style.bg_color = Color(0.115, 0.085, 0.055, 0.94)
    sheet_style.border_color = Color(0.55, 0.42, 0.24, 0.95)
    sheet_style.border_width_left = 1
    sheet_style.border_width_right = 1
    sheet_style.border_width_top = 1
    sheet_style.border_width_bottom = 1
    sheet_style.corner_radius_top_left = 10
    sheet_style.corner_radius_top_right = 10
    sheet_style.corner_radius_bottom_left = 10
    sheet_style.corner_radius_bottom_right = 10
    sheet_style.shadow_color = Color(0, 0, 0, 0.45)
    sheet_style.shadow_size = 18
    sheet_style.shadow_offset = Vector2(0, 6)
    talk_sheet.add_theme_stylebox_override("panel", sheet_style)

    context_label.add_theme_color_override("font_color", Color(0.95, 0.82, 0.58, 1.0))
    subcontext_label.add_theme_color_override("font_color", Color(0.68, 0.58, 0.43, 1.0))
    speech_input.add_theme_color_override("font_color", Color(0.92, 0.84, 0.70, 1.0))
    speech_input.add_theme_color_override("font_placeholder_color", Color(0.58, 0.50, 0.40, 1.0))


func _api_url(path: String) -> String:
    var auth := get_node_or_null("/root/Auth")
    if auth == null:
        return path

    return str(auth.api_base).rstrip("/") + path


func _auth_headers() -> PackedStringArray:
    var headers := PackedStringArray()
    headers.append("Content-Type: application/json")

    var auth := get_node_or_null("/root/Auth")
    if auth != null:
        var value := str(auth.get_auth_header())
        if not value.is_empty():
            headers.append("Authorization: " + value)

    return headers
