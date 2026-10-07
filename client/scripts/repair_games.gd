extends RefCounted

## The repair mini-games (LLM-690) — one per site kind, the rules only.
##
## A player mends the town's damage by playing: each won round is one
## pc/repair/step. A miss costs time; only the saw can fail a round outright
## (Result.FAIL: the saw sticks and the cut starts over, LLM-716).
## These classes hold the game state and answer input; repair_stage.gd draws
## them in art pixels, and repair_panel.gd sends the steps. Kept free of nodes
## so tests can drive them with plain numbers.
##
## Every game works in ART PIXELS inside a play area of PLAY_W x PLAY_H, with
## (0, 0) at its top-left. press() takes a point in that space (or null for a
## key press) and answers with a Result. After a HIT the game holds — it shows
## the work landing and takes no input — until the panel calls release() for
## the next round (the step answered and the step gap has passed).

const PLAY_W := 176
const PLAY_H := 56

enum Result { NONE, HIT, MISS, PROGRESS, FAIL }

## The game for a site: a well is timing (windlass), a signpost is setting its
## arm level (plumb), a road is sawing, everything else — a business, a fence, a
## crate — is hammering.
static func game_kind(site_kind: String, form: String) -> String:
    if form == "signpost":
        return "plumb"
    if site_kind == "well":
        return "windlass"
    if site_kind == "road":
        return "saw"
    return "hammer"


## `difficulty` 0..1 is the engine's, from the player's purse (LLM-712): 0 plays
## easiest, 1 hardest; each game lerps its speed and margins between the two.
static func make(kind: String, rng: RandomNumberGenerator, difficulty := 0.0) -> Game:
    var d := clampf(difficulty, 0.0, 1.0)
    match kind:
        "windlass":
            return Windlass.new(rng, d)
        "plumb":
            return Plumb.new(rng, d)
        "saw":
            return Saw.new(rng, d)
    return Hammer.new(rng, d)


class Game:
    extends RefCounted
    var rng: RandomNumberGenerator
    var holding := false
    var t := 0.0  # seconds since this game started
    var difficulty := 0.0

    func _init(r: RandomNumberGenerator, d := 0.0) -> void:
        rng = r
        difficulty = d

    func update(dt: float) -> void:
        t += dt

    func press(_pos: Variant) -> Result:
        return Result.NONE

    func release() -> void:
        holding = false

    ## How far the work has gone. Only the plumb game shows it.
    func set_progress(_done: int, _total: int) -> void:
        pass

    ## Misses or fails the game found on its own in update() (a moment let
    ## pass), for the stage to show. Only the saw has any.
    func take_events() -> Array:
        return []

    ## A short line telling the player what to do, drawn under the play area.
    func hint() -> String:
        return ""


## WINDLASS — a peg swings along a bar; stop it in the gold zone to seat it.
## Each hit turns the windlass a notch. The zone narrows a little as the work
## goes on, never below min_zone. Harder: a faster peg, a narrower zone.
class Windlass:
    extends Game
    const BAR_X := 16
    const BAR_W := 144
    const BAR_Y := 30
    const START_ZONE := 30
    const MIN_ZONE := 16
    const SPEED := 96.0  # art px per second
    const HARD_START_ZONE := 18
    const HARD_MIN_ZONE := 10
    const HARD_SPEED := 170.0
    var marker := 0.0  # 0..BAR_W
    var dir := 1.0
    var speed := SPEED
    var min_zone := MIN_ZONE
    var zone_x := 0  # zone start, 0..BAR_W - zone_w
    var zone_w := START_ZONE
    var notch := 0

    func _init(r: RandomNumberGenerator, d := 0.0) -> void:
        super(r, d)
        speed = lerpf(SPEED, HARD_SPEED, d)
        zone_w = roundi(lerpf(START_ZONE, HARD_START_ZONE, d))
        min_zone = roundi(lerpf(MIN_ZONE, HARD_MIN_ZONE, d))
        _place_zone()

    func _place_zone() -> void:
        zone_x = rng.randi_range(8, BAR_W - zone_w - 8)

    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        marker += dir * speed * dt
        if marker >= BAR_W:
            marker = BAR_W
            dir = -1.0
        elif marker <= 0.0:
            marker = 0.0
            dir = 1.0

    func in_zone() -> bool:
        return marker >= zone_x and marker <= zone_x + zone_w

    func press(_pos: Variant) -> Result:
        if holding:
            return Result.NONE
        if not in_zone():
            return Result.MISS
        holding = true
        notch += 1
        return Result.HIT

    func release() -> void:
        super()
        zone_w = maxi(min_zone, zone_w - 2)
        _place_zone()

    func hint() -> String:
        return "Stop the peg in the gold"


## HAMMER — nail heads rise from the board one at a time; tap each before it
## sinks back. Each hit drives one nail. Harder: a nail stands for less time,
## and a tap must land closer to its head.
class Hammer:
    extends Game
    const SLOTS := 6
    const BOARD_X := 16
    const BOARD_Y := 32
    const BOARD_W := 144
    const BOARD_H := 14
    const UP_TIME := 1.5  # seconds a nail stands before it sinks
    const GAP_TIME := 0.35  # seconds between one nail sinking and the next rising
    const HIT_SLACK := 6  # art px around a nail head that still counts
    const HARD_UP_TIME := 0.85
    const HARD_HIT_SLACK := 2
    var driven: Array[bool] = []
    var up := -1  # the slot whose nail stands, or -1
    var up_t := 0.0
    var wait := 0.4
    var up_time := UP_TIME
    var hit_slack := HIT_SLACK

    func _init(r: RandomNumberGenerator, d := 0.0) -> void:
        super(r, d)
        up_time = lerpf(UP_TIME, HARD_UP_TIME, d)
        hit_slack = roundi(lerpf(HIT_SLACK, HARD_HIT_SLACK, d))
        for i in SLOTS:
            driven.append(false)

    func slot_x(i: int) -> int:
        return BOARD_X + 12 + i * ((BOARD_W - 24) / (SLOTS - 1))

    func nail_rect(i: int) -> Rect2:
        return Rect2(slot_x(i) - 3, BOARD_Y - 10, 7, 10)

    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        if up >= 0:
            up_t += dt
            if up_t >= up_time:
                up = -1
                wait = GAP_TIME
            return
        wait -= dt
        if wait <= 0.0:
            _raise()

    func _raise() -> void:
        var free: Array[int] = []
        for i in SLOTS:
            if not driven[i]:
                free.append(i)
        if free.is_empty():
            # Every slot driven: a fresh board's worth of nails.
            for i in SLOTS:
                driven[i] = false
                free.append(i)
        up = free[rng.randi_range(0, free.size() - 1)]
        up_t = 0.0

    func press(pos: Variant) -> Result:
        if holding:
            return Result.NONE
        if up < 0:
            return Result.MISS
        if pos is Vector2:
            if not nail_rect(up).grow(hit_slack).has_point(pos):
                return Result.MISS
        holding = true
        driven[up] = true
        return Result.HIT

    func release() -> void:
        super()
        up = -1
        wait = GAP_TIME

    func hint() -> String:
        return "Strike each nail before it sinks"


## SAW — keep the beat (LLM-716). The blade strokes across the log on its own,
## end to end every `period` seconds; as it reaches an end, that end's window
## lights for `window` seconds either side. Tap that side while it is lit and
## the cut sinks a stroke. A tap off the window or on the wrong side BINDS the
## saw — the blade buckles, the stroke is lost and the cut rises back one — and
## once a run has started, letting a window pass untouched binds it too. Three
## binds in a row stick the saw (Result.FAIL): the cut starts over, and the
## blade stays fast in the log for STUCK_TIME. `strokes_needed` good strokes
## cut one section through. Harder: more strokes, a faster stroke, a narrower
## window.
class Saw:
    extends Game
    const STROKES := 6
    const HARD_STROKES := 10
    const PERIOD := 0.9
    const HARD_PERIOD := 0.45
    const WINDOW := 0.16
    const HARD_WINDOW := 0.07
    const STUCK_BINDS := 3
    const STUCK_TIME := 1.2
    const LOG_X := 24
    const LOG_W := 128
    var side := 0  # 0 = the blade is heading for the left end, 1 = the right
    var strokes := 0
    var strokes_needed := STROKES
    var period := PERIOD
    var window := WINDOW
    var binds := 0  # binds in a row
    var stuck_left := 0.0
    var bound_at := -1.0  # game time of the last bind, -1 for none
    var started := false  # a good stroke has been made this run
    var blade_t := 0.0  # the blade's own clock; stops while stuck or holding
    var next_end := 1  # the next end to be judged; end k is reached at k * period
    var saw_x := -1.0  # -1..1, where the blade is drawn
    var _events: Array = []

    func _init(r: RandomNumberGenerator, d := 0.0) -> void:
        super(r, d)
        strokes_needed = roundi(lerpf(STROKES, HARD_STROKES, d))
        period = lerpf(PERIOD, HARD_PERIOD, d)
        window = lerpf(WINDOW, HARD_WINDOW, d)

    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        if stuck_left > 0.0:
            stuck_left = maxf(0.0, stuck_left - dt)
            if stuck_left == 0.0:
                next_end = floori(blade_t / period) + 1
            return
        blade_t += dt
        while blade_t > next_end * period + window:
            next_end += 1
            if started:
                _events.append(_bind())
                if stuck_left > 0.0:
                    return
        saw_x = -cos(PI * blade_t / period)
        # The side to tap: the end lit now, else the end the blade is heading for.
        var k := roundi(blade_t / period)
        side = posmod(k, 2) if absf(blade_t - k * period) <= window else posmod(floori(blade_t / period) + 1, 2)

    ## A tap on the left half is the left side, the right half the right side.
    ## A key press plays the side the blade is reaching (keyboard players keep
    ## the beat with one key).
    func press(pos: Variant) -> Result:
        if holding or stuck_left > 0.0:
            return Result.NONE
        var k := roundi(blade_t / period)
        var wanted := posmod(k, 2)
        var tapped := wanted
        if pos is Vector2:
            tapped = 0 if pos.x < PLAY_W / 2.0 else 1
        if k < next_end or absf(blade_t - k * period) > window or tapped != wanted:
            return _bind()
        started = true
        binds = 0
        next_end = k + 1
        strokes += 1
        if strokes < strokes_needed:
            return Result.PROGRESS
        holding = true
        return Result.HIT

    ## The blade binds: the stroke is lost, the cut rises back one, and the
    ## third in a row sticks the saw.
    func _bind() -> Result:
        bound_at = t
        binds += 1
        strokes = maxi(0, strokes - 1)
        if binds < STUCK_BINDS:
            return Result.MISS
        binds = 0
        strokes = 0
        started = false
        stuck_left = STUCK_TIME
        return Result.FAIL

    ## Whether the end the blade is reaching is lit now.
    func lit() -> bool:
        var k := roundi(blade_t / period)
        return k >= next_end and absf(blade_t - k * period) <= window and stuck_left == 0.0 and not holding

    ## The binds update() found (a window let pass), for the stage to show.
    func take_events() -> Array:
        var out := _events
        _events = []
        return out

    func release() -> void:
        super()
        strokes = 0
        binds = 0
        started = false
        next_end = floori(blade_t / period) + 1

    func hint() -> String:
        return "Tap each side as it lights — keep the beat"


## PLUMB — the signpost's arm swings on its bolt, from its sag up past level
## and back, easing at each end. Stop it level: a plumb bob hangs from the arm,
## and level is the bob on the mark of the stake beside it. Each hit sets the
## arm truer — the sag it swings down to shrinks with the work done — and holds
## it level until the next round. Harder: a faster swing, a finer level.
class Plumb:
    extends Game
    const SAG := 24.0  # degrees the broken arm droops below level
    const OVER := 5.0  # degrees it swings up past level
    const SPEED := 16.0  # degrees per second through level
    const TOLERANCE := 2.5  # degrees either side of level that count
    const HARD_SPEED := 30.0
    const HARD_TOLERANCE := 1.75
    var speed := SPEED
    var tolerance := TOLERANCE
    var sag := SAG  # how far down this round's swing reaches
    var angle := SAG  # degrees below level; negative is above
    var phase := 0.0  # radians along the swing; 0 is the bottom
    var hit_off := 0.0  # how far off level the last hit was, degrees

    func _init(r: RandomNumberGenerator, d := 0.0) -> void:
        super(r, d)
        speed = lerpf(SPEED, HARD_SPEED, d)
        tolerance = lerpf(TOLERANCE, HARD_TOLERANCE, d)

    ## The swing runs between sag (below) and OVER (above), as angle =
    ## middle + half * cos(phase).
    func _middle() -> float:
        return (sag - OVER) / 2.0

    func _half() -> float:
        return (sag + OVER) / 2.0

    ## Pick the swing up again from where the arm is now.
    func _rephase() -> void:
        phase = acos(clampf((angle - _middle()) / _half(), -1.0, 1.0))

    func set_progress(done: int, total: int) -> void:
        sag = SAG * (1.0 - clampf(float(done) / maxi(1, total), 0.0, 1.0))
        if not holding:
            angle = clampf(angle, -OVER, sag)
            _rephase()

    ## How fast the phase turns so the arm crosses LEVEL at `speed` degrees a
    ## second, wherever level falls in the swing — the window to stop it is the
    ## same every round as the sag shrinks. (Level is not the swing's middle,
    ## where it moves fastest: there the rate would be speed / half.)
    func _rate() -> float:
        var c := clampf(_middle() / _half(), -1.0, 1.0)
        return speed / (_half() * maxf(sqrt(1.0 - c * c), 0.3))

    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        phase = fmod(phase + dt * _rate(), TAU)
        angle = _middle() + _half() * cos(phase)

    func is_level() -> bool:
        return absf(angle) <= tolerance

    func press(_pos: Variant) -> Result:
        if holding:
            return Result.NONE
        if not is_level():
            return Result.MISS
        holding = true
        hit_off = angle
        angle = 0.0
        return Result.HIT

    func release() -> void:
        super()
        angle = 0.0
        _rephase()

    func hint() -> String:
        return "Stop the arm level — the bob on the mark"
