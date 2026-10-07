extends Node
## Autoloaded singleton — the client's one sound player (LLM-703). Any script
## calls Sound.play("hammer_hit") without a node path; the storm drives the rain
## loop through set_rain / thunder; footsteps.gd tells it when the player is
## indoors.
##
## Every sound is a row in SOUNDS: its files (one is picked at random per play),
## its gain, and a random pitch spread. The gains sit far below 0 dB on purpose —
## the Kenney files are normalized hot and sound wrong at full level, but fine
## turned well down (Jeff, 2026-10-02). Tune a sound by its row, never at the
## call site.
##
## Web: Godot's web export plays sounds as Web Audio samples by default, and a
## sample ignores bus effects (a low-pass filter would do nothing in the
## browser). So the indoor rain is a second, pre-muffled recording of the same
## loop (rain_muffled.ogg, low-passed offline) crossfaded against the outdoor
## one, not a filter. Volume and pitch do work on samples.
##
## The browser also refuses to start audio before the first user gesture; the
## login click is that gesture, and nothing here plays before login.

const DIR := "res://assets/sound/"

## name -> {files, db (gain), pitch (± random fraction, 0 = none), base
## (optional fixed pitch scale, default 1.0)}.
const SOUNDS := {
    "step_grass": {
        "files": ["footstep_grass_000.ogg", "footstep_grass_001.ogg", "footstep_grass_002.ogg", "footstep_grass_003.ogg", "footstep_grass_004.ogg"],
        "db": -22.0, "pitch": 0.06,
    },
    "step_dirt": {
        "files": ["footstep_wood_000.ogg", "footstep_wood_001.ogg", "footstep_wood_002.ogg", "footstep_wood_003.ogg", "footstep_wood_004.ogg"],
        "db": -22.0, "pitch": 0.06,
    },
    "step_stone": {
        "files": ["footstep00.ogg", "footstep01.ogg", "footstep02.ogg", "footstep03.ogg", "footstep04.ogg", "footstep05.ogg", "footstep06.ogg", "footstep07.ogg", "footstep08.ogg", "footstep09.ogg"],
        "db": -22.0, "pitch": 0.06,
    },
    "step_wood": {
        "files": ["footstep_concrete_000.ogg", "footstep_concrete_001.ogg", "footstep_concrete_002.ogg", "footstep_concrete_003.ogg", "footstep_concrete_004.ogg"],
        "db": -24.0, "pitch": 0.06,
    },
    "hammer_hit": {
        "files": ["impactMetal_light_000.ogg", "impactMetal_light_001.ogg", "impactMetal_light_002.ogg", "impactMetal_light_003.ogg", "impactMetal_light_004.ogg"],
        "db": -16.0, "pitch": 0.05,
    },
    "hammer_miss": {
        "files": ["impactPlank_medium_000.ogg", "impactPlank_medium_001.ogg", "impactPlank_medium_002.ogg", "impactPlank_medium_003.ogg", "impactPlank_medium_004.ogg"],
        "db": -18.0, "pitch": 0.05,
    },
    "windlass_click": {"files": ["metalClick.ogg"], "db": -16.0, "pitch": 0.04},
    "windlass_turn": {"files": ["creak1.ogg", "creak2.ogg", "creak3.ogg"], "db": -18.0, "pitch": 0.0},
    "saw_stroke": {
        "files": ["saw_0.ogg", "saw_1.ogg", "saw_2.ogg", "saw_3.ogg", "saw_4.ogg", "saw_5.ogg"],
        "db": -14.0, "pitch": 0.04,
    },
    # A cut section of trunk dropping onto the verge — the board thud, lower.
    # The saw binding (LLM-716): the teeth dragging in the cut, and the blade
    # flexing; then, stuck, a clank and the log groaning on it.
    "saw_bind": {"files": ["drawKnife1.ogg", "drawKnife2.ogg", "drawKnife3.ogg"], "db": -12.0, "pitch": 0.05, "base": 0.8},
    "saw_flex": {
        "files": ["impactTin_medium_000.ogg", "impactTin_medium_001.ogg", "impactTin_medium_002.ogg", "impactTin_medium_003.ogg", "impactTin_medium_004.ogg"],
        "db": -16.0, "pitch": 0.06,
    },
    "saw_stuck": {
        "files": ["impactMetal_heavy_000.ogg", "impactMetal_heavy_001.ogg", "impactMetal_heavy_002.ogg", "impactMetal_heavy_003.ogg", "impactMetal_heavy_004.ogg"],
        "db": -12.0, "pitch": 0.03, "base": 0.85,
    },
    "saw_groan": {"files": ["creak1.ogg", "creak2.ogg", "creak3.ogg"], "db": -14.0, "pitch": 0.03, "base": 0.7},
    "section_drop": {
        "files": ["impactPlank_medium_000.ogg", "impactPlank_medium_002.ogg", "impactPlank_medium_004.ogg"],
        "db": -18.0, "pitch": 0.04, "base": 0.75,
    },
    "coins": {"files": ["handleCoins.ogg", "handleCoins2.ogg"], "db": -14.0, "pitch": 0.0},
    "thunder_close": {"files": ["thunder_close.ogg"], "db": -10.0, "pitch": 0.03},
    "thunder_far": {"files": ["thunder_far.ogg"], "db": -12.0, "pitch": 0.03},
}

## The rain loops. Gains in dB at full storm.
const RAIN_FILE := "rain.ogg"
const RAIN_MUFFLED_FILE := "rain_muffled.ogg"
const RAIN_DB := -14.0
const RAIN_MUFFLED_DB := -16.0
## Matches storm_fx's FADE_IN/OUT_DURATION so the rain is heard as the sheets
## are seen.
const RAIN_FADE_SECONDS := 2.0
## Indoor/outdoor crossfade — quicker than the storm fade: a door closing.
const INDOOR_FADE_SECONDS := 0.6

## Thunder lags the flash like real distance: a close strike almost at once, a
## far one seconds later. CLOSE_CHANCE is the share of strikes that are close.
const THUNDER_CLOSE_CHANCE := 0.35
const THUNDER_CLOSE_DELAY := Vector2(0.15, 0.6)
const THUNDER_FAR_DELAY := Vector2(1.2, 3.5)
## Thunder heard from indoors: quieter rather than filtered (see Web above).
const THUNDER_INDOOR_DB := -8.0

## One-shot voices. Round-robin — a ninth overlapping sound cuts the oldest,
## which only a burst of hammer sparks and steps could ever reach.
const VOICES := 8

const SETTINGS_PATH := "user://sound.cfg"
const DEFAULT_VOLUME := 0.8

## 0..1 master volume and the mute switch, kept between visits.
var volume: float = DEFAULT_VOLUME
var muted: bool = false

var _voices: Array[AudioStreamPlayer] = []
var _next_voice := 0
var _streams: Dictionary = {}  # file -> AudioStream (null when missing)
var _rng := RandomNumberGenerator.new()
## Dummy driver only: the players Sound has started (see _start).
var _dummy_running: Dictionary = {}

var _rain: AudioStreamPlayer = null
var _rain_muffled: AudioStreamPlayer = null
## Storm level and indoor mix, each 0..1, eased toward their targets in
## _process. The two loops' volumes derive from them.
var _rain_level := 0.0
var _rain_target := 0.0
var _indoor_mix := 0.0
var _indoor_target := 0.0
var indoors := false


func _ready() -> void:
    process_mode = Node.PROCESS_MODE_ALWAYS
    _rng.randomize()
    for i in VOICES:
        var p := AudioStreamPlayer.new()
        add_child(p)
        _voices.append(p)
    _rain = _make_loop(RAIN_FILE)
    _rain_muffled = _make_loop(RAIN_MUFFLED_FILE)
    _load_settings()
    _apply_master()


## Play a sound from SOUNDS. An unknown name or a missing file is a no-op (with
## a warning once per file), never an error.
func play(sound_name: String, extra_db := 0.0) -> void:
    var row: Dictionary = SOUNDS.get(sound_name, {})
    if row.is_empty():
        push_warning("sound: unknown sound " + sound_name)
        return
    var files: Array = row["files"]
    var stream := _stream(files[_rng.randi_range(0, files.size() - 1)])
    if stream == null:
        return
    var p := _voices[_next_voice]
    _next_voice = (_next_voice + 1) % _voices.size()
    p.stream = stream
    p.volume_db = float(row["db"]) + extra_db
    var spread := float(row["pitch"])
    p.pitch_scale = float(row.get("base", 1.0)) * (1.0 + _rng.randf_range(-spread, spread))
    _start(p)


## Start a player — except under the headless Dummy driver (tests, CI), which
## mixes nothing: a playback started there is never released, and a test that
## quits right after a sound reports it as leaked. Everything up to here still
## runs, so tests still cover the table and the file lookups. There the player
## is only marked running (_dummy_running), so the loop logic reads the same.
func _start(p: AudioStreamPlayer) -> void:
    if AudioServer.get_driver_name() == "Dummy":
        _dummy_running[p] = true
        return
    p.play()


func _stop(p: AudioStreamPlayer) -> void:
    _dummy_running.erase(p)
    p.stop()


func _is_running(p: AudioStreamPlayer) -> bool:
    return p.playing or _dummy_running.has(p)


## Raise (true) or clear (false) the rain. fade false snaps (the connect-time
## weather sync, matching storm_fx.set_storm's tween false).
func set_rain(active: bool, fade := true) -> void:
    _rain_target = 1.0 if active else 0.0
    if not fade:
        _rain_level = _rain_target


## Whether the player is under a roof — the rain and thunder are heard muffled.
func set_indoors(inside: bool) -> void:
    indoors = inside
    _indoor_target = 1.0 if inside else 0.0


## A lightning flash happened: thunder follows after its distance delay.
func thunder() -> void:
    var close := _rng.randf() < THUNDER_CLOSE_CHANCE
    var span: Vector2 = THUNDER_CLOSE_DELAY if close else THUNDER_FAR_DELAY
    var delay := _rng.randf_range(span.x, span.y)
    var sound_name := "thunder_close" if close else "thunder_far"
    get_tree().create_timer(delay).timeout.connect(func() -> void:
        play(sound_name, THUNDER_INDOOR_DB if indoors else 0.0))


func set_volume(v: float) -> void:
    volume = clampf(v, 0.0, 1.0)
    _apply_master()
    _save_settings()


func set_muted(m: bool) -> void:
    muted = m
    _apply_master()
    _save_settings()


func _process(delta: float) -> void:
    _rain_level = move_toward(_rain_level, _rain_target, delta / RAIN_FADE_SECONDS)
    _indoor_mix = move_toward(_indoor_mix, _indoor_target, delta / INDOOR_FADE_SECONDS)
    _drive_loop(_rain, _rain_level * (1.0 - _indoor_mix), RAIN_DB)
    _drive_loop(_rain_muffled, _rain_level * _indoor_mix, RAIN_MUFFLED_DB)


## Set a loop's volume from a 0..1 level. Both loops run whenever it rains at
## all — the silent one at -80 dB — so they start together on the first storm
## frame and the indoor crossfade never jumps between two points of the
## recording. Both stop once the storm has faded out, so a clear sky costs
## nothing.
func _drive_loop(p: AudioStreamPlayer, level: float, db: float) -> void:
    if p.stream == null:
        return
    if _rain_level <= 0.0:
        if _is_running(p):
            _stop(p)
        p.volume_db = -80.0
        return
    if not _is_running(p):
        _start(p)
    p.volume_db = db + linear_to_db(level) if level > 0.0 else -80.0


func _make_loop(file: String) -> AudioStreamPlayer:
    var p := AudioStreamPlayer.new()
    p.volume_db = -80.0
    var stream := _stream(file)
    if stream is AudioStreamOggVorbis:
        stream.loop = true
    p.stream = stream
    add_child(p)
    return p


func _stream(file: String) -> AudioStream:
    if _streams.has(file):
        return _streams[file]
    var path := DIR + file
    var stream: AudioStream = load(path) if ResourceLoader.exists(path) else null
    if stream == null:
        push_warning("sound: missing " + path)
    _streams[file] = stream
    return stream


func _apply_master() -> void:
    AudioServer.set_bus_volume_db(0, linear_to_db(maxf(volume, 0.0001)))
    AudioServer.set_bus_mute(0, muted or volume <= 0.0)


func _load_settings() -> void:
    var cfg := ConfigFile.new()
    if cfg.load(SETTINGS_PATH) != OK:
        return
    volume = clampf(float(cfg.get_value("sound", "volume", DEFAULT_VOLUME)), 0.0, 1.0)
    muted = bool(cfg.get_value("sound", "muted", false))


func _save_settings() -> void:
    var cfg := ConfigFile.new()
    cfg.set_value("sound", "volume", volume)
    cfg.set_value("sound", "muted", muted)
    cfg.save(SETTINGS_PATH)
