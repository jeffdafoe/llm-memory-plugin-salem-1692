extends RefCounted
## Where a repairer's axe lands (LLM-743). The farmer chop (farmer_rig.gd
## *_chop) puts the axe head at a fixed point from the feet on its strike
## frame, a different point for each facing. aim() picks the facing and the
## step that put that point on the target's visible edge. The step moves only
## the drawn sprite; the engine position, pathing and reach are untouched.

## The axe head on the strike frame, in body-cell px from the feet
## (FarmerDoll.ANCHOR), measured from the axe sheet (farmer_tool_001): south
## cell 133 pose 2, east cell 165 pose 6, west its mirror, north cell 149
## pose 3.
const STRIKE := {
    "south": Vector2(1.0, 11.0),
    "east": Vector2(31.5, -12.0),
    "west": Vector2(-31.5, -12.0),
    "north": Vector2(3.0, -37.5),
}
const FACING_DIR := {
    "south": Vector2(0, 1),
    "east": Vector2(1, 0),
    "west": Vector2(-1, 0),
    "north": Vector2(0, -1),
}
## The strike frame's index in every *_chop animation.
const STRIKE_FRAME := 2
## The facings a swing is aimed with. The pack's north chop is a back view that
## rises from low-left to overhead — it reads as a swing the wrong way (Jeff,
## LLM-747) — so work to the north takes a side swing and a step instead.
const AIM_FACINGS := ["south", "east", "west"]
## How far inside the target's visible box the blade lands, world px.
const BITE := 4.0


## The facing and drawn step (world px) that land the axe head on rect, for a
## sprite drawn at draw_scale with its feet at feet. Each facing's step moves
## its strike point to the nearest point of rect (inset by BITE); the shortest
## step wins, a tie going to the facing that points at rect's centre. The step
## is capped at max_step. {} for a rect with no area.
static func aim(feet: Vector2, rect: Rect2, draw_scale: float, max_step: float) -> Dictionary:
    if not rect.has_area():
        return {}
    var inner := rect.grow(-BITE)
    if not inner.has_area():
        inner = Rect2(rect.get_center(), Vector2.ZERO)
    var to_center := rect.get_center() - feet
    var best := {}
    var best_len := INF
    var best_dot := -INF
    for facing in AIM_FACINGS:
        var strike: Vector2 = feet + STRIKE[facing] * draw_scale
        var landed := Vector2(clampf(strike.x, inner.position.x, inner.end.x), clampf(strike.y, inner.position.y, inner.end.y))
        var step := landed - strike
        var dot: float = FACING_DIR[facing].dot(to_center)
        var length := step.length()
        if length < best_len - 0.5 or (absf(length - best_len) <= 0.5 and dot > best_dot):
            best = {"facing": facing, "step": step}
            best_len = length
            best_dot = dot
    best["step"] = (best["step"] as Vector2).limit_length(max_step)
    return best


## Where the axe head lands, world px: the feet, the drawn step, and the
## facing's strike point at draw_scale.
static func strike_point(feet: Vector2, step: Vector2, facing: String, draw_scale: float) -> Vector2:
    return feet + step + STRIKE.get(facing, Vector2.ZERO) * draw_scale
