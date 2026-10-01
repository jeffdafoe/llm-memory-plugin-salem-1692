extends RefCounted

## The repair mini-games (LLM-690) — one per site kind, the rules only.
##
## A player mends the town's damage by playing: each won round is one
## pc/repair/step. There is no fail state — a miss costs time, nothing else.
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

enum Result { NONE, HIT, MISS, PROGRESS }

## The game for a site: a well and a signpost are timing (windlass), a road is
## sawing, everything else — a business, a fence, a crate — is hammering.
static func game_kind(site_kind: String, form: String) -> String:
    if site_kind == "well" or form == "signpost":
        return "windlass"
    if site_kind == "road":
        return "saw"
    return "hammer"


static func make(kind: String, rng: RandomNumberGenerator) -> Game:
    match kind:
        "windlass":
            return Windlass.new(rng)
        "saw":
            return Saw.new(rng)
    return Hammer.new(rng)


class Game:
    extends RefCounted
    var rng: RandomNumberGenerator
    var holding := false
    var t := 0.0  # seconds since this game started

    func _init(r: RandomNumberGenerator) -> void:
        rng = r

    func update(dt: float) -> void:
        t += dt

    func press(_pos: Variant) -> Result:
        return Result.NONE

    func release() -> void:
        holding = false

    ## A short line telling the player what to do, drawn under the play area.
    func hint() -> String:
        return ""


## WINDLASS — a peg swings along a bar; stop it in the green zone to seat it.
## Each hit turns the windlass a notch. The zone narrows a little as the work
## goes on, never below MIN_ZONE.
class Windlass:
    extends Game
    const BAR_X := 16
    const BAR_W := 144
    const BAR_Y := 30
    const START_ZONE := 30
    const MIN_ZONE := 16
    const SPEED := 96.0  # art px per second
    var marker := 0.0  # 0..BAR_W
    var dir := 1.0
    var zone_x := 0  # zone start, 0..BAR_W - zone_w
    var zone_w := START_ZONE
    var notch := 0

    func _init(r: RandomNumberGenerator) -> void:
        super(r)
        _place_zone()

    func _place_zone() -> void:
        zone_x = rng.randi_range(8, BAR_W - zone_w - 8)

    func update(dt: float) -> void:
        super(dt)
        if holding:
            return
        marker += dir * SPEED * dt
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
        zone_w = maxi(MIN_ZONE, zone_w - 2)
        _place_zone()

    func hint() -> String:
        return "Stop the peg in the green"


## HAMMER — nail heads rise from the board one at a time; tap each before it
## sinks back. Each hit drives one nail.
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
    var driven: Array[bool] = []
    var up := -1  # the slot whose nail stands, or -1
    var up_t := 0.0
    var wait := 0.4

    func _init(r: RandomNumberGenerator) -> void:
        super(r)
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
            if up_t >= UP_TIME:
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
            if not nail_rect(up).grow(HIT_SLACK).has_point(pos):
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


## SAW — keep the beat: tap the side the saw is drawing toward, left, right,
## left. STROKES good strokes cut one section through.
class Saw:
    extends Game
    const STROKES := 6
    const LOG_X := 24
    const LOG_W := 128
    var side := 0  # 0 = left wants the next tap, 1 = right
    var strokes := 0
    var saw_x := 0.0  # -1..1, where the blade is drawn
    var target := -1.0

    func update(dt: float) -> void:
        super(dt)
        saw_x = move_toward(saw_x, target, dt * 8.0)

    ## A tap on the left half is the left side, the right half the right side.
    ## A key press plays whichever side is wanted (keyboard players keep the
    ## beat with one key).
    func press(pos: Variant) -> Result:
        if holding:
            return Result.NONE
        var tapped := side
        if pos is Vector2:
            tapped = 0 if pos.x < PLAY_W / 2.0 else 1
        if tapped != side:
            return Result.MISS
        target = -1.0 if side == 0 else 1.0
        side = 1 - side
        strokes += 1
        if strokes < STROKES:
            return Result.PROGRESS
        holding = true
        return Result.HIT

    func release() -> void:
        super()
        strokes = 0

    func hint() -> String:
        return "Tap left, right, left — keep the beat"
