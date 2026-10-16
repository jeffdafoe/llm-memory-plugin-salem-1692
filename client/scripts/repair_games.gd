extends RefCounted

## The repair mini-games (LLM-690) — one per site kind, the rules only.
##
## A player mends the town's damage by playing: each won round is one
## pc/repair/step. A miss costs time; the saw, the crate's lid, the shop's
## ladder and the fence's board can fail a round outright (Result.FAIL: the saw
## sticks and the cut starts over, LLM-716; the lid springs, the ladder falls,
## LLM-721; the board splits, LLM-786).
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
## arm level (plumb), a road is sawing, a crate is nailing its lid crosswise, a
## business is worked from a ladder kept steady, and everything else — a fence —
## is hammering.
static func game_kind(site_kind: String, form: String) -> String:
    if form == "signpost":
        return "plumb"
    if form == "crate":
        return "crosswise"
    if site_kind == "well":
        return "windlass"
    if site_kind == "road":
        return "saw"
    if site_kind == "business":
        return "ladder"
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
        "crosswise":
            return Crosswise.new(rng, d)
        "ladder":
            return Ladder.new(rng, d)
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

    ## Results the game found on its own in update() (a moment let pass, a
    ## fall, a round worked through), for the stage to show. Only the saw and
    ## the ladder have any.
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


## HAMMER — the fence's board (LLM-786). Nails rise from the board one at a
## time, leaning; each rocks upright, stands TRUE for a moment, then leans the
## other way and sinks. Strike its HEAD while it stands true and it drives. A
## strike while it leans, or a glancing one on its shank, BENDS it (the nail is
## lost); a tap off every nail, or with none standing, DENTS the board. A bend
## or a dent is a bad strike, and three in a row SPLIT the board
## (Result.FAIL): its nails come out, the round starts over, and the hammer is
## held SPLIT_TIME. A nail let sink costs only time. `nails_needed` driven
## nails win the round. Harder: more nails, a shorter true moment, a tighter
## head.
class Hammer:
    extends Game
    const SLOTS := 6
    const BOARD_X := 16
    const BOARD_Y := 32
    const BOARD_W := 144
    const BOARD_H := 14
    const NAILS := 3
    const HARD_NAILS := 5
    const STAND := 8  # art px of shank a nail stands proud
    const HEAD_H := 3  # the head's rows at the top of nail.png
    const LEAN := 3  # art px a leaning nail's head sits off upright
    const RISE := 0.12
    # The rock upright takes a random time in this span, so it cannot be
    # counted.
    const APPROACH_MIN := 0.35
    const APPROACH_MAX := 0.9
    const TRUE_TIME := 0.3
    const HARD_TRUE_TIME := 0.12
    const AWAY := 0.3  # seconds it takes to lean over the other way
    const SINK := 0.35
    const GAP_TIME := 0.35  # seconds between one nail going and the next rising
    const HEAD_SLACK := 2  # art px around the head that still counts
    const HARD_HEAD_SLACK := 1
    const SPLIT_STRIKES := 3
    const SPLIT_TIME := 1.0
    var driven: Array[bool] = []
    var up := -1  # the slot whose nail stands, or -1
    var up_t := 0.0
    var wait := 0.4
    var side := 1  # the way the standing nail leans first, -1 or 1
    var approach := APPROACH_MIN
    var up_time := 0.0  # the standing nail's whole stand, rise to sunk
    var nails := 0  # nails driven this round
    var nails_needed := NAILS
    var true_time := TRUE_TIME
    var head_slack := HEAD_SLACK
    var bad := 0  # bad strikes in a row
    var clean := true  # no bad strike yet this round
    var split_left := 0.0
    var split_at := -1.0
    ## The last strike, for the stage to swing at and show: its kind
    ## ("drive", "bend" or "dent"), slot (-1 for a dent), board x, and the
    ## nail's height and lean when struck.
    var last_kind := ""
    var last_slot := -1
    var last_x := 0.0
    var last_h := 0.0
    var last_lean := 0
    ## The last bent nail: {slot, h, lean, at}; empty for none.
    var bent := {}
    ## Dents in the board this round: Vector2(board x, game time).
    var dents: Array[Vector2] = []
    ## The slots whose nails the last split knocked out, for the stage to
    ## show coming out when the hammer lands.
    var popped: Array[int] = []

    func _init(r: RandomNumberGenerator, d := 0.0) -> void:
        super(r, d)
        nails_needed = roundi(lerpf(NAILS, HARD_NAILS, d))
        true_time = lerpf(TRUE_TIME, HARD_TRUE_TIME, d)
        head_slack = roundi(lerpf(HEAD_SLACK, HARD_HEAD_SLACK, d))
        for i in SLOTS:
            driven.append(false)

    func slot_x(i: int) -> int:
        return BOARD_X + 12 + i * ((BOARD_W - 24) / (SLOTS - 1))

    ## When the standing nail comes true and stops being true, in up_t.
    func true_from() -> float:
        return RISE + approach

    func true_until() -> float:
        return RISE + approach + true_time

    func is_true() -> bool:
        return up >= 0 and up_t >= true_from() and up_t < true_until()

    ## How far the standing nail stands proud, whole art px: it rises fast,
    ## stands, and sinks at the end.
    func height() -> float:
        if up < 0:
            return 0.0
        var rise := clampf(up_t / RISE, 0.0, 1.0)
        var sink := clampf((up_t - (up_time - SINK)) / SINK, 0.0, 1.0)
        return roundf(STAND * rise * (1.0 - sink))

    ## How far the standing nail's head sits off upright, whole art px:
    ## LEAN to 1 as it rocks upright, 0 while true, then 1 to LEAN the other
    ## way.
    func lean() -> int:
        if up < 0 or is_true():
            return 0
        if up_t < true_from():
            var u := clampf((up_t - RISE) / approach, 0.0, 1.0)
            return side * clampi(ceili(LEAN * (1.0 - u)), 1, LEAN)
        var v := (up_t - true_until()) / AWAY
        return -side * clampi(1 + floori(v * LEAN), 1, LEAN)

    ## The standing nail's head, play px, as drawn.
    func head_rect() -> Rect2:
        return Rect2(slot_x(up) - 3 + lean(), BOARD_Y - height() - HEAD_H, 7, HEAD_H)

    ## The whole standing nail, head to board, leaning shank and all.
    func nail_rect() -> Rect2:
        var l := lean()
        var h := height()
        return Rect2(slot_x(up) - 3 + mini(l, 0), BOARD_Y - h - HEAD_H, 7 + absi(l), h + HEAD_H)

    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        if split_left > 0.0:
            split_left -= dt
            if split_left > 0.0:
                return
            split_left = 0.0
            # The split board is set right.
            dents.clear()
            wait = GAP_TIME
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
        up = free[rng.randi_range(0, free.size() - 1)]
        up_t = 0.0
        side = -1 if rng.randf() < 0.5 else 1
        approach = rng.randf_range(APPROACH_MIN, APPROACH_MAX)
        up_time = RISE + approach + true_time + AWAY + SINK

    ## pos is a tap in play px, or null for a key press — a key strikes the
    ## standing nail's head, so only its timing counts. A tap outside the play
    ## area does nothing.
    func press(pos: Variant) -> Result:
        if holding or split_left > 0.0:
            return Result.NONE
        if pos is Vector2 and not Rect2(0, 0, PLAY_W, PLAY_H).has_point(pos):
            return Result.NONE
        if up < 0:
            var x: float = pos.x if pos is Vector2 else BOARD_X + BOARD_W / 2.0
            return _dent(x)
        if pos is Vector2:
            if not nail_rect().grow(head_slack).has_point(pos):
                return _dent(pos.x)
            if not head_rect().grow(head_slack).has_point(pos):
                return _bend()
        if not is_true():
            return _bend()
        _strike("drive", up, slot_x(up))
        driven[up] = true
        up = -1
        wait = GAP_TIME
        bad = 0
        nails += 1
        if nails < nails_needed:
            return Result.PROGRESS
        holding = true
        return Result.HIT

    func _strike(kind: String, slot: int, x: float) -> void:
        last_kind = kind
        last_slot = slot
        last_x = x
        last_h = height()
        last_lean = lean()

    ## A strike on the leaning nail, or a glancing one on its shank: it
    ## bends and is lost.
    func _bend() -> Result:
        _strike("bend", up, slot_x(up))
        bent = {"slot": up, "h": last_h, "lean": last_lean if last_lean != 0 else side, "at": t}
        up = -1
        wait = GAP_TIME
        return _bad()

    ## A strike on the bare board.
    func _dent(x: float) -> Result:
        x = clampf(x, BOARD_X + 2, BOARD_X + BOARD_W - 2)
        _strike("dent", -1, x)
        dents.append(Vector2(x, t))
        return _bad()

    ## A bad strike; the third in a row splits the board.
    func _bad() -> Result:
        clean = false
        bad += 1
        if bad < SPLIT_STRIKES:
            return Result.MISS
        bad = 0
        nails = 0
        up = -1
        split_left = SPLIT_TIME
        split_at = t
        # The nails come out at once; `popped` keeps which, so the stage can
        # show them until the hammer lands however the time arrives.
        popped.clear()
        for i in SLOTS:
            if driven[i]:
                popped.append(i)
            driven[i] = false
        return Result.FAIL

    func release() -> void:
        super()
        up = -1
        wait = GAP_TIME
        nails = 0
        bad = 0
        clean = true
        split_left = 0.0
        split_at = -1.0
        last_kind = ""
        last_slot = -1
        bent.clear()
        popped.clear()
        for i in SLOTS:
            driven[i] = false
        dents.clear()

    func hint() -> String:
        return "Strike each nail on the head as it stands straight"


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

    ## The elapsed time is spent in segments — up to the next window's close,
    ## or the end of a stuck spell — so each bind lands at the moment it
    ## happened, and the result is the same however the time arrives (one
    ## long frame after a stall, or many short ones).
    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        var now := t - dt
        var left := dt
        while left > 0.0:
            if stuck_left > 0.0:
                var spent := minf(left, stuck_left)
                stuck_left -= spent
                left -= spent
                now += spent
                if stuck_left <= 0.0:
                    stuck_left = 0.0
                    next_end = floori(blade_t / period) + 1
                continue
            var to_close := maxf(0.0, next_end * period + window - blade_t)
            if left <= to_close:
                blade_t += left
                break
            blade_t += to_close
            left -= to_close
            now += to_close
            next_end += 1
            if started:
                _events.append(_bind(now))
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
            return _bind(t)
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
    func _bind(at: float) -> Result:
        bound_at = at
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


## CROSSWISE — the crate's lid (LLM-721). Nails stand at the lid's corners, a
## back row and a front row. A round opens by showing the order to drive them:
## one number at a time lights on its nail and goes dark before the next, so
## the whole order has to be remembered. Strike the nails in that order; there
## is no hurry. A wrong nail springs the lid (Result.FAIL) when the hammer lands
## on it, SPRING_AT later: the nails pop out, and after SPRUNG_TIME the same
## order shows again at the same pace. A tap off every nail, or on one already
## driven, does nothing.
## The show quickens by SPEEDUP with every round won (the mend's steps done, so
## a mend taken up again keeps its pace), never below MIN_SHOW_STEP.
## Harder: a quicker show from the first round.
class Crosswise:
    extends Game
    const ROWS := 2
    const COLS := 3
    const SHOW_STEP := 0.8  # seconds from one number lighting to the next, first round
    const HARD_SHOW_STEP := 0.45
    const SHOW_ON := 0.7  # of a step, the part its number stays lit; then dark
    const SPEEDUP := 0.85
    const MIN_SHOW_STEP := 0.25
    const SPRUNG_TIME := 1.0
    const SPRING_AT := 0.06  # the hammer's fall: repair_stage.gd's SWING_DOWN
    const STAND := 4  # art px a nail stands proud before it is driven
    const REACH := 12.0  # art px from a nail's head that a tap still picks it
    # The lid seen from the front and a little above: its back edge is the
    # narrower one. Each row of nails goes in at ROW_Y, and a nail sits
    # INSET of the way in from the lid's edges at its row.
    const BACK_Y := 26
    const FRONT_Y := 46
    const BACK_X0 := 38
    const BACK_X1 := 138
    const FRONT_X0 := 20
    const FRONT_X1 := 156
    const ROW_Y := [29, 42]
    const INSET := 0.12
    var base_step := SHOW_STEP
    var rounds_done := 0
    var order: Array[int] = []  # nail indices, in the order to drive them
    var driven: Array[bool] = []
    var next := 0  # how many of the order are driven
    var showing := true
    var show_t := 0.0
    var spring_in := 0.0  # a wrong nail struck: seconds until the lid springs
    var sprung_left := 0.0
    var springs := 0  # this round
    var last := -1  # the nail last struck

    func _init(r: RandomNumberGenerator, d := 0.0) -> void:
        super(r, d)
        base_step = lerpf(SHOW_STEP, HARD_SHOW_STEP, d)
        for i in count():
            driven.append(false)
        _new_order()
        _begin_show()

    func count() -> int:
        return ROWS * COLS

    func set_progress(done: int, _total: int) -> void:
        rounds_done = maxi(done, 0)

    ## Seconds from one number lighting to the next, this round.
    func show_step() -> float:
        return maxf(MIN_SHOW_STEP, base_step * pow(SPEEDUP, rounds_done))

    ## Where a nail goes into the lid, play px. Nails are numbered in reading
    ## order: the back row left to right, then the front row.
    func nail_pos(i: int) -> Vector2:
        var y: int = ROW_Y[i / COLS]
        var f := INSET + (1.0 - 2.0 * INSET) * float(i % COLS) / (COLS - 1)
        var e := edge_x(y)
        return Vector2(roundf(lerpf(e.x, e.y, f)), y)

    ## The lid's left and right edge on row y.
    static func edge_x(y: float) -> Vector2:
        var u := clampf((y - BACK_Y) / float(FRONT_Y - BACK_Y), 0.0, 1.0)
        return Vector2(roundf(lerpf(BACK_X0, FRONT_X0, u)), roundf(lerpf(BACK_X1, FRONT_X1, u)))

    ## The nail whose head is nearest pos, within REACH; -1 for none.
    func nail_at(pos: Vector2) -> int:
        var best := -1
        var best_d := REACH
        for i in count():
            var head := nail_pos(i) - Vector2(0, STAND)
            var dist := head.distance_to(pos)
            if dist <= best_d:
                best = i
                best_d = dist
        return best

    func show_len() -> float:
        return show_step() * count()

    ## Which of the order's numbers is lit now (0 for the first), or -1 while
    ## none is: between two numbers, and once the show is over.
    func number_lit() -> int:
        if not showing:
            return -1
        var step := show_step()
        var n := floori(show_t / step)
        if n >= count() or show_t - n * step >= step * SHOW_ON:
            return -1
        return n

    ## A fresh order, never the same as the one before when another exists.
    func _new_order() -> void:
        var before := order.duplicate()
        for attempt in 8:
            order.clear()
            for i in count():
                order.append(i)
            for i in range(order.size() - 1, 0, -1):
                var j := rng.randi_range(0, i)
                var tmp := order[i]
                order[i] = order[j]
                order[j] = tmp
            if order != before:
                return

    func _begin_show() -> void:
        showing = true
        show_t = 0.0

    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        var left := dt
        if spring_in > 0.0:
            if left < spring_in:
                spring_in -= left
                return
            left -= spring_in
            spring_in = 0.0
            for k in count():
                driven[k] = false
            next = 0
            sprung_left = SPRUNG_TIME
        if sprung_left > 0.0:
            sprung_left -= left
            if sprung_left <= 0.0:
                sprung_left = 0.0
                _begin_show()
            return
        if showing:
            show_t += left
            if show_t >= show_len():
                showing = false

    ## pos is a tap (Vector2), a nail picked by its key (int), or a bare key
    ## press (null), which picks no nail.
    func press(pos: Variant) -> Result:
        if holding or showing or spring_in > 0.0 or sprung_left > 0.0:
            return Result.NONE
        var i := -1
        if pos is int:
            i = pos if pos >= 0 and pos < count() else -1
        elif pos is Vector2:
            i = nail_at(pos)
        if i < 0 or driven[i]:
            return Result.NONE
        last = i
        if i != order[next]:
            springs += 1
            spring_in = SPRING_AT
            return Result.FAIL
        driven[i] = true
        next += 1
        if next < count():
            return Result.PROGRESS
        holding = true
        return Result.HIT

    func release() -> void:
        super()
        for k in count():
            driven[k] = false
        next = 0
        springs = 0
        spring_in = 0.0
        sprung_left = 0.0
        _new_order()
        _begin_show()

    func hint() -> String:
        return "Watch the order, then drive the nails in it"


## LADDER — the shop is mended from a ladder (LLM-721). It tips: the further it
## leans the faster it goes, and it drifts a little of its own. A tap on a half
## of the play area nudges it toward that side, so the player taps the side it
## leans away from; a key press nudges it back toward upright. The round's work
## goes on only while it stands steady, and the round is won (a HIT from
## update) once the work is done. Let it lean past the edge and it falls
## (Result.FAIL): the round's work is lost, and it takes FALL_TIME to set up
## again. Harder: it tips faster and drifts more, the steady zone is narrower,
## and the round needs more work.
class Ladder:
    extends Game
    const STEP := 1.0 / 120.0  # the fixed step the lean is worked out in
    const TIP := 4.0  # how hard the lean feeds on itself, per second squared
    const HARD_TIP := 7.0
    const DRIFT := 0.5  # the drift's push at most, lean per second squared
    const HARD_DRIFT := 1.0
    const DRIFT_EVERY := 0.4  # seconds between one drift and the next
    const DAMP := 1.2  # how fast the swing dies away, per second
    const NUDGE := 0.6  # lean per second a tap adds
    const STEADY := 0.45  # the lean inside which the work goes on
    const HARD_STEADY := 0.3
    const START_LEAN := 0.12
    const WORK_TIME := 2.5  # seconds of steady work a round needs
    const HARD_WORK_TIME := 3.5
    const FALL_TIME := 1.2
    var tip := TIP
    var drift_max := DRIFT
    var steady := STEADY
    var work_time := WORK_TIME
    var lean := 0.0  # -1..1, positive to the right; past either end it falls
    var lean_v := 0.0
    var drift := 0.0
    var work := 0.0  # seconds of steady work this round
    var fallen_left := 0.0
    var fell_to := 0.0  # the side it last fell to, -1 or 1
    var falls := 0  # this round
    var fell_at := -1.0  # game time of the last fall, -1 for none
    var nudged_at := -1.0  # game time of the last nudge, -1 for none
    var _acc := 0.0
    var _drift_t := 0.0
    var _events: Array = []

    func _init(r: RandomNumberGenerator, d := 0.0) -> void:
        super(r, d)
        tip = lerpf(TIP, HARD_TIP, d)
        drift_max = lerpf(DRIFT, HARD_DRIFT, d)
        steady = lerpf(STEADY, HARD_STEADY, d)
        work_time = lerpf(WORK_TIME, HARD_WORK_TIME, d)
        _set_up()

    ## Stood up again, leaning a little to one side.
    func _set_up() -> void:
        lean = START_LEAN if rng.randf() < 0.5 else -START_LEAN
        lean_v = 0.0
        drift = 0.0
        _drift_t = 0.0

    func is_steady() -> bool:
        return fallen_left == 0.0 and absf(lean) < steady

    ## The side to tap to bring it back: 0 the left, 1 the right.
    func wanted_side() -> int:
        return 1 if lean < 0.0 else 0

    ## The lean is worked out in fixed steps, so the result is the same
    ## however the time arrives (one long frame after a stall, or many short
    ## ones).
    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        _acc += dt
        while _acc >= STEP and not holding:
            _acc -= STEP
            _step(t - _acc)

    func _step(now: float) -> void:
        if fallen_left > 0.0:
            fallen_left -= STEP
            if fallen_left <= 0.0:
                fallen_left = 0.0
                _set_up()
            return
        _drift_t += STEP
        if _drift_t >= DRIFT_EVERY:
            _drift_t -= DRIFT_EVERY
            drift = rng.randf_range(-drift_max, drift_max)
        lean_v += (lean * tip + drift) * STEP
        lean_v -= lean_v * DAMP * STEP
        lean += lean_v * STEP
        if absf(lean) >= 1.0:
            fell_to = signf(lean)
            lean = fell_to
            lean_v = 0.0
            falls += 1
            work = 0.0
            fallen_left = FALL_TIME
            fell_at = now
            _events.append(Result.FAIL)
            return
        if absf(lean) < steady:
            work += STEP
            if work >= work_time:
                work = work_time
                holding = true
                _events.append(Result.HIT)

    ## A tap on the left half nudges it left, the right half right. A key
    ## press nudges it back toward upright; an int key picks a side (0 left).
    func press(pos: Variant) -> Result:
        if holding or fallen_left > 0.0:
            return Result.NONE
        var dir := -1.0 if lean > 0.0 else 1.0
        if pos is Vector2:
            dir = -1.0 if pos.x < PLAY_W / 2.0 else 1.0
        elif pos is int:
            dir = -1.0 if pos == 0 else 1.0
        lean_v += dir * NUDGE
        nudged_at = t
        return Result.PROGRESS

    func take_events() -> Array:
        var out := _events
        _events = []
        return out

    ## The next round starts from now: time left over from a long frame that
    ## ended the last one is dropped, not played into this one.
    func release() -> void:
        super()
        _acc = 0.0
        _events.clear()
        work = 0.0
        falls = 0
        fallen_left = 0.0
        _set_up()

    func hint() -> String:
        return "Keep the ladder upright — tap the other side"
