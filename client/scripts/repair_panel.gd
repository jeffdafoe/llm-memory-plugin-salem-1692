extends CanvasLayer

## The repair panel (LLM-690) — a player takes the town's repair work.
##
## Opens two ways, both with the engine's offer (sim.PCRepairOffer on the
## wire): on the arrival thought (a private object_condition room_event with a
## `repair` field, forwarded by talk_panel.gd), and on a click on a damaged
## object the player already stands at (probe_click → GET pc/repair/offer).
## The dialog shows the damage line and the pay; [Repair it] POSTs
## pc/repair/start and swaps in the mini-game (repair_stage.gd), which plays
## one round per step: each won round POSTs pc/repair/step. The last step
## lands the repair server-side; the panel shows the object whole, flies the
## coins to the top-bar chip and closes.
##
## Pacing: the engine refuses a step sooner than step_gap_ms after the last
## (429, not counted). The panel holds the game after each won round until the
## step has answered AND step_gap_ms + STEP_MARGIN_MS has passed, so an honest
## player never meets the refusal; a 429 that still comes is retried.
##
## Walking away (pc/move) or going to bed gives the repair up server-side;
## closing the panel without walking leaves the engine's idle timeout to do it.
##
## Layer 4, the modal tier, like the notice panel; main.gd blocks world input
## while it is open (opened / closed).

const Games = preload("res://scripts/repair_games.gd")
const StageScript = preload("res://scripts/repair_stage.gd")

const STEP_MARGIN_MS := 250
const RETRY_429_MS := 400
const FONT_PATH := "res://assets/fonts/IMFellEnglish-Regular.ttf"
const TAP_SIZE := Vector2(132, 44)

signal opened()
signal closed()
## Coins the town paid for a finished repair — main.gd refreshes the purse.
signal earned(amount: int)

enum Phase { CLOSED, OFFER, STARTING, PLAYING, FINISHING }

var phase := Phase.CLOSED
var offer: Dictionary = {}
var world: Node2D = null
## The top-bar coin chip the pay flies to; set by main.gd. May be null.
var coin_target: Control = null

var root: Control = null
var backdrop: ColorRect = null
var sheet: PanelContainer = null
var stage: Control = null
var fact_label: Label = null
var pay_label: Label = null
var hint_label: Label = null
var status_label: Label = null
var repair_button: Button = null
var close_button: Button = null

var _font: Font = null
var _http_offer: HTTPRequest = null
var _http_start: HTTPRequest = null
var _http_step: HTTPRequest = null
var _probe_cb: Callable = Callable()
var _probe_hit_id := ""
var _step_in_flight := false
var _round_won_at := 0
var _round_answered := false
var _release_pending := false
var _paid := 0
## Bumped on every open and every close. A callback carrying an older token —
## an HTTP answer, a retry timer, the coin tween — belongs to work the player
## has put down, and does nothing.
var _token := 0
var _coin_tween: Tween = null
## Test seam: when valid, called as send_hook.call(route) in place of the
## HTTP request, route one of "offer", "start", "step".
var send_hook: Callable = Callable()


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

    # The village stays visible around the panel, dimmed.
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
        pad.add_theme_constant_override(side, 14)
    pad.add_theme_constant_override("margin_top", 12)
    pad.add_theme_constant_override("margin_bottom", 12)
    sheet.add_child(pad)

    var vb := VBoxContainer.new()
    vb.add_theme_constant_override("separation", 8)
    pad.add_child(vb)

    stage = Control.new()
    stage.set_script(StageScript)
    stage.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
    vb.add_child(stage)
    stage.round_won.connect(_on_round_won)
    stage.finish_shown.connect(_on_finish_shown)

    fact_label = _label(16, Color(0.92, 0.84, 0.70))
    pay_label = _label(16, Color(0.95, 0.82, 0.58))
    hint_label = _label(14, Color(0.80, 0.72, 0.56))
    status_label = _label(14, Color(0.93, 0.62, 0.48))
    for l in [fact_label, pay_label, hint_label, status_label]:
        vb.add_child(l)

    var buttons := HBoxContainer.new()
    buttons.alignment = BoxContainer.ALIGNMENT_CENTER
    buttons.add_theme_constant_override("separation", 12)
    vb.add_child(buttons)
    repair_button = _button("Repair it", _on_repair_pressed)
    close_button = _button("Not now", close)
    buttons.add_child(repair_button)
    buttons.add_child(close_button)
    _apply_theme()


func _label(size: int, color: Color) -> Label:
    var l := Label.new()
    l.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
    l.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
    l.add_theme_font_override("font", _font)
    l.add_theme_font_size_override("font_size", size)
    l.add_theme_color_override("font_color", color)
    l.custom_minimum_size = Vector2(260, 0)
    return l


func _button(text: String, cb: Callable) -> Button:
    var b := Button.new()
    b.text = text
    b.custom_minimum_size = TAP_SIZE
    b.add_theme_font_override("font", _font)
    b.add_theme_font_size_override("font_size", 17)
    b.pressed.connect(cb)
    return b


# --- opening ---------------------------------------------------------------

## The title over the stage, by what is being mended.
static func title_for(site_kind: String, form: String) -> String:
    match form:
        "fence":
            return "Mend the Fence"
        "signpost":
            return "Straighten the Signpost"
        "crate":
            return "Nail the Lid Down"
    match site_kind:
        "well":
            return "Mend the Well"
        "business":
            return "Mend the Damage"
        "road":
            return "Clear the Road"
    return "The Town's Work"


## Open on an offer (the engine's PCRepairOffer wire shape). Opening again
## for the same site while open is a no-op, so the arrival thought and a
## click arriving together do not stack.
func show_offer(o: Dictionary) -> void:
    if o.is_empty():
        return
    if phase != Phase.CLOSED and str(offer.get("object_id", "")) == str(o.get("object_id", "")):
        return
    if phase == Phase.PLAYING or phase == Phase.STARTING or phase == Phase.FINISHING:
        return
    # The arrival thought can open the panel while a click's probe is still
    # out: the panel is open now, so that probe is settled — its answer must
    # neither reopen nor walk.
    _drop_probe()
    _token += 1
    offer = o
    phase = Phase.OFFER
    _paid = 0
    _release_pending = false
    _round_answered = false
    var kind := str(o.get("site_kind", ""))
    var form := str(o.get("form", ""))
    var layers := build_layers(world, o)
    stage.setup(title_for(kind, form), Games.game_kind(kind, form), layers["broken"], layers["sound"],
        int(o.get("steps", 1)), int(o.get("steps_done", 0)))
    fact_label.text = _sentence(str(o.get("fact", "")))
    var bounty := int(o.get("bounty", 0))
    pay_label.text = "The town pays %s for the work." % _coins(bounty)
    hint_label.text = ""
    status_label.text = ""
    var mender := str(o.get("mender_name", ""))
    var can_take := true
    if bool(o.get("yours", false)):
        repair_button.text = "Go on mending"
    else:
        repair_button.text = "Repair it"
        if mender != "":
            status_label.text = "%s is already mending it." % mender
            can_take = false
        elif not bool(o.get("chest_can_pay", false)):
            status_label.text = "The town chest cannot pay for this just now."
            can_take = false
    repair_button.visible = can_take
    close_button.text = "Not now"
    _sync_labels()
    _apply_sheet_width()
    visible = true
    set_process(true)
    opened.emit()


## A click on an object: ask the engine what work is here. If it is this
## object (or a piece of the same fence break beside it, or an overlay on it)
## the panel opens and callback(true) runs; otherwise callback(false), and the
## caller walks as usual. The LATEST click wins: a click while a probe is out
## takes the probe over, and the earlier click's callback never runs (it is
## neither work nor a walk — the player has clicked again). With the panel
## open the click is not ours (the panel is modal): callback(false) at once.
func probe_click(hit_id: String, callback: Callable) -> void:
    if hit_id == "" or phase != Phase.CLOSED:
        callback.call(false)
        return
    var pending := _probe_cb.is_valid()
    _probe_hit_id = hit_id
    _probe_cb = callback
    if pending:
        return  # the answer in flight is judged against this click
    if not _send("offer"):
        _finish_probe(false)


## Forget a probe that is out: its answer will find no callback and do nothing.
func _drop_probe() -> void:
    _probe_cb = Callable()
    _probe_hit_id = ""


func _on_offer_response(_r: int, code: int, _h: PackedStringArray, body: PackedByteArray) -> void:
    Auth.check_response(code)
    if not _probe_cb.is_valid():
        return
    var o: Dictionary = {}
    if code == 200:
        var parsed = JSON.parse_string(body.get_string_from_utf8())
        if parsed is Dictionary and parsed.get("repair") is Dictionary:
            o = parsed["repair"]
    if phase == Phase.CLOSED and not o.is_empty() and click_matches_offer(world, _probe_hit_id, o):
        show_offer(o)
        return  # show_offer settled the probe; the click is handled
    _finish_probe(false)


func _finish_probe(opened_panel: bool) -> void:
    var cb := _probe_cb
    _drop_probe()
    if cb.is_valid():
        cb.call(opened_panel)


## Logout / session end: close, and forget a probe still out so its answer
## neither opens the panel nor walks.
func reset() -> void:
    _drop_probe()
    close()


## Whether the panel is up — main.gd checks it before walking on a probe's
## "no work here".
func is_open() -> bool:
    return phase != Phase.CLOSED


## Whether a click on hit_id is a click on the offer's work: the site itself,
## an overlay attached to it (a business's debris), or — for a fence break
## only — one of the two segments that sag with it.
static func click_matches_offer(w: Node2D, hit_id: String, o: Dictionary) -> bool:
    var site_id := str(o.get("object_id", ""))
    if hit_id == "" or site_id == "":
        return false
    if hit_id == site_id:
        return true
    if w == null or not ("placed_objects" in w):
        return false
    var hit: Node2D = w.placed_objects.get(hit_id)
    var site: Node2D = w.placed_objects.get(site_id)
    if hit == null or site == null:
        return false
    if str(hit.get_meta("attached_to", "")) == site_id:
        return true
    if str(o.get("form", "")) != "fence":
        return false
    return fence_neighbours(w, site).has(hit)


# --- the object's layers ---------------------------------------------------

## The object as it stands and as it will be, as stage layers (see
## repair_stage.gd): the site's own sprite, its attached overlays (debris),
## and — for a fence break — the two neighbours that sag with it. The mended
## form comes from the catalog: a state's `minor-of-<state>` tag names the
## state it mends back to; a well mends to its first state without the
## `damaged` tag; overlays go; a road obstacle goes entirely (no sound layers).
static func build_layers(w: Node2D, o: Dictionary) -> Dictionary:
    var out := {"broken": [], "sound": []}
    if w == null or not ("placed_objects" in w):
        return out
    var site_id := str(o.get("object_id", ""))
    var site: Node2D = w.placed_objects.get(site_id)
    if site == null:
        return out
    var kind := str(o.get("site_kind", ""))
    var parts: Array = [site]
    if str(o.get("form", "")) == "fence":
        for n in fence_neighbours(w, site):
            parts.append(n)
    for part in parts:
        var rel: Vector2 = part.position - site.position if part != site else Vector2.ZERO
        var sprite := _sprite_of(part)
        if sprite == null:
            continue
        var rs: float = sprite.scale.x if sprite.scale.x > 0.0 else 2.0
        var tex := _texture_of(sprite)
        var pos: Vector2 = (rel + sprite.position) / rs
        out["broken"].append({"tex": tex, "pos": pos})
        if kind == "road":
            continue
        var mended := _mended_texture(part, kind)
        if mended != null:
            # Placed the way world.gd places a sprite: its anchor on the
            # placement's origin.
            var anchor := _anchor_of(str(part.get_meta("asset_id", "")))
            out["sound"].append({"tex": mended, "pos": rel / rs - mended.get_size() * anchor})
        elif tex != null:
            out["sound"].append({"tex": tex, "pos": pos})
        # Overlays (debris) stand as broken, and are gone when mended.
        for child in part.get_children():
            if child is Node2D and child.has_meta("asset_id"):
                var cs := _sprite_of(child)
                if cs != null:
                    out["broken"].append({"tex": _texture_of(cs), "pos": (rel + child.position + cs.position) / rs})
    return out


## The placements of the site's asset one tile either side of it, in the same
## row — the edges of a fence break.
static func fence_neighbours(w: Node2D, site: Node2D) -> Array:
    var out: Array = []
    var asset_id := str(site.get_meta("asset_id", ""))
    for id in w.placed_objects:
        var n: Node2D = w.placed_objects[id]
        if n == null or n == site or str(n.get_meta("asset_id", "")) != asset_id:
            continue
        var d: Vector2 = n.position - site.position
        if absf(d.y) < 2.0 and absf(absf(d.x) - 32.0) < 2.0:
            out.append(n)
    return out


static func _anchor_of(asset_id: String) -> Vector2:
    var asset: Dictionary = Catalog.assets.get(asset_id, {})
    return Vector2(
        float(asset.get("anchorX", asset.get("anchor_x", 0.5))),
        float(asset.get("anchorY", asset.get("anchor_y", 0.85))))


static func _sprite_of(container: Node) -> Node2D:
    for child in container.get_children():
        if child is Sprite2D or child is AnimatedSprite2D:
            return child
    return null


static func _texture_of(sprite: Node2D) -> Texture2D:
    if sprite is Sprite2D:
        return sprite.texture
    if sprite is AnimatedSprite2D and sprite.sprite_frames != null:
        var anim: StringName = sprite.animation
        if sprite.sprite_frames.get_frame_count(anim) > 0:
            return sprite.sprite_frames.get_frame_texture(anim, 0)
    return null


## The texture of the state a placement mends back to, or null when it does
## not change (a business: only its overlay goes).
static func _mended_texture(part: Node2D, kind: String) -> Texture2D:
    var asset_id := str(part.get_meta("asset_id", ""))
    var asset: Dictionary = Catalog.assets.get(asset_id, {})
    var states: Array = asset.get("states", [])
    var current := str(part.get_meta("current_state", ""))
    var target := mended_state_name(states, current, kind)
    if target == "" or target == current:
        return null
    var info = Catalog.get_state(asset_id, target)
    if info == null:
        return null
    return Catalog.get_sprite_texture(info)


## The state a placement in `current` mends back to: the `minor-of-<state>`
## tag on its current state, else — for a well — the first state without the
## `damaged` tag. "" when there is nothing to change.
static func mended_state_name(states: Array, current: String, kind: String) -> String:
    for s in states:
        if str(s.get("state", "")) != current:
            continue
        for tag in s.get("tags", []):
            var t := str(tag)
            if t.begins_with("minor-of-"):
                return t.substr("minor-of-".length())
    if kind == "well":
        for s in states:
            var tags: Array = s.get("tags", [])
            if not tags.has("damaged"):
                return str(s.get("state", ""))
    return ""


# --- play ------------------------------------------------------------------

## Send one request through the panel's HTTPRequests (or the test seam).
## False when it could not be sent.
func _send(route: String) -> bool:
    if send_hook.is_valid():
        send_hook.call(route)
        return true
    _ensure_http()
    match route:
        "offer":
            return _http_offer.request(Auth.api_base + "/api/village/pc/repair/offer",
                Auth.auth_headers(false), HTTPClient.METHOD_GET) == OK
        "start":
            return _http_start.request(Auth.api_base + "/api/village/pc/repair/start",
                Auth.auth_headers(), HTTPClient.METHOD_POST, "") == OK
        "step":
            return _http_step.request(Auth.api_base + "/api/village/pc/repair/step",
                Auth.auth_headers(), HTTPClient.METHOD_POST, "") == OK
    return false


func _on_repair_pressed() -> void:
    if phase != Phase.OFFER:
        return
    status_label.text = ""
    repair_button.visible = false
    # Work already under way (a reload mid-game): play on from where it is —
    # the engine would refuse a second start as busy.
    if bool(offer.get("yours", false)):
        _begin_play(offer)
        return
    phase = Phase.STARTING
    if not _send("start"):
        _fail("The work could not be started. Try again.")


func _on_start_response(_r: int, code: int, _h: PackedStringArray, body: PackedByteArray) -> void:
    Auth.check_response(code)
    # Closed (or opened on other work) while the start was out: not ours now.
    # The engine gives an unplayed repair up after its idle time.
    if phase != Phase.STARTING:
        return
    var parsed = JSON.parse_string(body.get_string_from_utf8())
    if code != 200 or not (parsed is Dictionary) or not (parsed.get("repair") is Dictionary):
        var msg := "The work could not be started."
        if parsed is Dictionary and parsed.has("error"):
            msg = _sentence(str(parsed["error"]))
        _fail(msg)
        return
    var started: Dictionary = parsed["repair"]
    if str(started.get("object_id", "")) != str(offer.get("object_id", "")):
        _fail("The work could not be started.")
        return
    _begin_play(started)


func _begin_play(o: Dictionary) -> void:
    offer = o
    stage.steps = maxi(1, int(o.get("steps", 1)))
    stage.set_progress(int(o.get("steps_done", 0)))
    stage.start_game()
    phase = Phase.PLAYING
    _release_pending = false
    _round_answered = false
    fact_label.text = ""
    pay_label.text = ""
    hint_label.text = stage.game.hint()
    repair_button.visible = false
    close_button.text = "Stop"
    _sync_labels()


func _on_round_won(_perfect: bool) -> void:
    if phase != Phase.PLAYING or _release_pending:
        return
    _round_won_at = Time.get_ticks_msec()
    _round_answered = false
    _release_pending = true
    _send_step()


## Send the won round's step. Only while a round is won and not yet answered
## — never for work the player has put down, never twice for one round.
func _send_step() -> void:
    if _step_in_flight or phase != Phase.PLAYING or not _release_pending or _round_answered:
        return
    if not _send("step"):
        _retry_later()
        return
    _step_in_flight = true


## Send the step again shortly, if the same work is still in hand by then.
func _retry_later() -> void:
    var token := _token
    get_tree().create_timer(RETRY_429_MS / 1000.0).timeout.connect(_retry_step.bind(token))


func _retry_step(token: int) -> void:
    if token != _token:
        return
    _send_step()


func _on_step_response(_r: int, code: int, _h: PackedStringArray, body: PackedByteArray) -> void:
    _step_in_flight = false
    Auth.check_response(code)
    if phase != Phase.PLAYING or not _release_pending:
        return
    if code == 429:
        # Inside the step gap after all (clock skew, a slow frame): not
        # counted, so send it again shortly.
        _retry_later()
        return
    var parsed = JSON.parse_string(body.get_string_from_utf8())
    if code != 200 or not (parsed is Dictionary):
        var msg := "The work was given up."
        if parsed is Dictionary and parsed.has("error"):
            msg = _sentence(str(parsed["error"]))
        _fail(msg)
        return
    stage.set_progress(int(parsed.get("steps_done", 0)))
    if bool(parsed.get("done", false)):
        if bool(parsed.get("landed", false)):
            _paid = int(parsed.get("paid", 0))
            phase = Phase.FINISHING
            hint_label.text = ""
            close_button.visible = false
            _sync_labels()
            stage.finish(_paid)
        else:
            _fail("Someone else finished it first.")
        return
    _round_answered = true


## Release the next round once the step answered and the gap has passed.
func _process(_delta: float) -> void:
    if phase != Phase.PLAYING or not _release_pending or not _round_answered:
        return
    var gap := int(offer.get("step_gap_ms", 0)) + STEP_MARGIN_MS
    if Time.get_ticks_msec() - _round_won_at < gap:
        return
    _release_pending = false
    _round_answered = false
    stage.release_round()


func _on_finish_shown() -> void:
    if phase == Phase.FINISHING:
        _fly_coins(_paid)


## The pay flies from the stage to the top-bar coin chip, then the panel
## closes.
func _fly_coins(amount: int) -> void:
    var token := _token
    if amount <= 0 or coin_target == null or not is_instance_valid(coin_target) or not coin_target.is_visible_in_tree():
        _end_after_pay(amount, token)
        return
    var chip := Label.new()
    chip.text = "+%d" % amount
    chip.add_theme_font_override("font", _font)
    chip.add_theme_font_size_override("font_size", 22)
    chip.add_theme_color_override("font_color", Color(0.95, 0.82, 0.42))
    chip.add_theme_color_override("font_outline_color", Color(0.12, 0.08, 0.04))
    chip.add_theme_constant_override("outline_size", 4)
    root.add_child(chip)
    chip.global_position = stage.get_global_rect().get_center() - Vector2(12, 12)
    var to := coin_target.get_global_rect().get_center() - Vector2(12, 12)
    _coin_tween = create_tween()
    _coin_tween.tween_property(chip, "global_position", to, 0.7).set_trans(Tween.TRANS_QUAD).set_ease(Tween.EASE_IN)
    _coin_tween.parallel().tween_property(chip, "scale", Vector2(0.6, 0.6), 0.7)
    _coin_tween.tween_callback(chip.queue_free)
    _coin_tween.tween_callback(_end_after_pay.bind(amount, token))


## The pay landed: tell main.gd and close — unless the panel has since been
## closed (and maybe opened on other work), which this must not close.
func _end_after_pay(amount: int, token: int) -> void:
    if token != _token:
        return
    if amount > 0:
        earned.emit(amount)
    close()


func _fail(msg: String) -> void:
    phase = Phase.OFFER
    _release_pending = false
    _round_answered = false
    stage.playing = false
    status_label.text = msg
    hint_label.text = ""
    repair_button.visible = false
    close_button.visible = true
    close_button.text = "Close"
    _sync_labels()


## main.gd calls close() on dismiss, on a new walk and on logout. Idempotent.
func close() -> void:
    if phase == Phase.CLOSED:
        return
    _token += 1
    phase = Phase.CLOSED
    offer = {}
    _release_pending = false
    _round_answered = false
    if _coin_tween != null and _coin_tween.is_valid():
        _coin_tween.kill()
    _coin_tween = null
    for child in root.get_children():
        if child is Label:
            child.queue_free()  # a coin chip caught mid-flight
    stage.playing = false
    stage.set_process(false)
    close_button.visible = true
    visible = false
    set_process(false)
    closed.emit()


func _on_backdrop_input(event: InputEvent) -> void:
    if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
        # Mid-game a stray click on the backdrop must not throw the work away.
        if phase == Phase.OFFER:
            close()
        get_viewport().set_input_as_handled()


func _unhandled_input(event: InputEvent) -> void:
    if phase == Phase.CLOSED:
        return
    if event is InputEventKey and event.pressed and not event.echo:
        if event.keycode == KEY_ESCAPE:
            if phase != Phase.FINISHING:
                close()
            get_viewport().set_input_as_handled()
        elif phase == Phase.PLAYING and (event.keycode == KEY_SPACE or event.keycode == KEY_ENTER):
            stage.press_key()
            get_viewport().set_input_as_handled()


func _ensure_http() -> void:
    if _http_offer == null:
        _http_offer = HTTPRequest.new()
        _http_offer.accept_gzip = false
        add_child(_http_offer)
        _http_offer.request_completed.connect(_on_offer_response)
    if _http_start == null:
        _http_start = HTTPRequest.new()
        _http_start.accept_gzip = false
        add_child(_http_start)
        _http_start.request_completed.connect(_on_start_response)
    if _http_step == null:
        _http_step = HTTPRequest.new()
        _http_step.accept_gzip = false
        add_child(_http_step)
        _http_step.request_completed.connect(_on_step_response)


## An empty line takes no room.
func _sync_labels() -> void:
    for l in [fact_label, pay_label, hint_label, status_label]:
        l.visible = l.text != ""


func _apply_sheet_width() -> void:
    var w := maxf(300.0, stage.custom_minimum_size.x)
    for l in [fact_label, pay_label, hint_label, status_label]:
        l.custom_minimum_size = Vector2(w, 0)


## Engine refusal text arrives lower-case and sometimes without a stop.
static func _sentence(s: String) -> String:
    s = s.strip_edges()
    if s == "":
        return s
    s = s[0].to_upper() + s.substr(1)
    if not (s.ends_with(".") or s.ends_with("!") or s.ends_with("?")):
        s += "."
    return s


static func _coins(n: int) -> String:
    return "1 coin" if n == 1 else "%d coins" % n


# Brown period theme — the notice panel's, so the modals match.
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
    for b in [repair_button, close_button]:
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
