"""Paints salem's outhouse (LLM-968): Outhouse 1. Every pixel is drawn here,
no pack art.

    python tools/tree-art/outhouse.py
    python tools/tree-art/outhouse.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/outhouse.py --check     # margins, size, a fresh paint

Writes client/assets/trees/outhouse.png, one 64x128 frame (32x64 pack px
drawn at density 2, shown at render_scale 1: the same size in the world,
twice the detail). The art fills the frame as the pack art did, so with the
anchor (0.5/0.85), footprint and door unchanged a player still walks to the
same door and goes in. Its foot sits 2.5 pack px above the frame's bottom,
leaving room for the cast shadow (and --check keeps the outer rows clear).

A "necessary house" of the 1690s: a small board-and-batten privy seen from
the front, a little of its right side showing, under a shed roof of riven
boards that rises to the right. A plank door braced with a Z of battens,
hung on iron strap hinges, a wooden latch and a pull, a small diamond cut
high in the door for air. Weathered oak; no crescent moon (a much later
custom).
"""

import argparse
import os
import random
import re
import sys

from PIL import Image, ImageDraw

import build as B
import plants as P
import fences as F

OUT_DIR = B.OUT_DIR
DENSITY = 2

# Weathered grey-brown oak (the Wood market stall's), colder than the fences'.
WOOD = ["2a2620", "3e3830", "565046", "706858", "8a8270", "a49a86", "bcb3a0"]
CUT = F.CUT
IRON = F.IRON
# Weathered roof boards, silvered (as the woodshed's and the Wood stall's).
ROOF = ["262420", "3a3630", "504b44", "68625a", "827b72", "9c958a"]
LEATHER = ["4a2e1a", "6e4428", "8e5a34", "a87244"]
PALETTE = WOOD + CUT + IRON + ROOF + LEATHER

FRAME = (32, 64)

# The front face and the side, frame px.
FX0, FX1 = 1.5, 24.5          # the front wall's left and right
SX1 = 29.6                    # the side's far edge
BASE = 60.5                   # the front's foot, room under it for the cast shadow
SIDE_RISE = 5.0               # the side recedes back and up toward its far edge
TOP_L, TOP_R = 18.0, 13.0      # the front wall's top, left and right (the shed roof's slope)


def sc(pts):
    return [(x * P.S, y * P.S) for x, y in pts]


def top_at(x):
    """The front wall's top at x: the roof line rising to the right."""
    return TOP_L + (TOP_R - TOP_L) * (x - FX0) / (FX1 - FX0)


def boards(d, rnd, x0, x1, n, tone):
    """Upright boards across the front from x0 to x1, each from the roof line
    to the foot, with a dark seam and a lit left edge, and a batten over
    each seam (board-and-batten)."""
    w = (x1 - x0) / n
    for k in range(n):
        a, b = x0 + k * w, x0 + (k + 1) * w
        t = tone + rnd.uniform(-0.07, 0.07)
        d.polygon(sc([(a, top_at(a)), (b, top_at(b)), (b, BASE), (a, BASE)]), fill=B.ramp(WOOD, t))
        d.line(sc([(a + 0.35, top_at(a) + 0.5), (a + 0.35, BASE)]), fill=B.ramp(WOOD, t + 0.15), width=max(1, int(0.3 * P.S)))
        # Grain.
        for _ in range(2):
            gx, gy = rnd.uniform(a + 0.8, b - 0.6), rnd.uniform(top_at(a) + 4, BASE - 6)
            d.line(sc([(gx, gy), (gx, gy + rnd.uniform(2.5, 5.0))]), fill=B.ramp(WOOD, t - 0.18), width=max(1, int(0.22 * P.S)))
    for k in range(1, n):
        x = x0 + k * w
        d.rectangle(sc([(x - 0.55, top_at(x) + 0.3), (x + 0.55, BASE)]), fill=B.ramp(WOOD, tone - 0.06))
        d.rectangle(sc([(x - 0.55, top_at(x) + 0.3), (x - 0.15, BASE)]), fill=B.ramp(WOOD, tone + 0.14))


def paint_outhouse(seed=751):
    P.use_frame(*FRAME, density=DENSITY)
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    shade = Image.new("L", (P.W, P.H), 0)
    rnd = random.Random(seed)
    S = P.S

    # A compact cast shadow at the foot, falling down and right.
    # Concentric ovals, strongest at the foot, so finish() can dither the
    # outer edge.
    sd = ImageDraw.Draw(shade)
    for k, v in ((0.0, 110), (0.8, 170), (1.6, 255)):
        sd.ellipse(sc([(FX0 + 2.0 + k, BASE - 1.6 + k * 0.4), (SX1 + 0.7 - k, BASE + 2.0 - k * 0.6)]), fill=v)
    # The side wall, in shade: boards receding to the right.
    side = [(FX1, top_at(FX1)), (SX1, top_at(FX1) - SIDE_RISE), (SX1, BASE - SIDE_RISE), (FX1, BASE)]
    d.polygon(sc(side), fill=B.ramp(WOOD, 0.34))
    for k in range(1, 3):
        t = k / 3
        x = FX1 + (SX1 - FX1) * t
        d.line(sc([(x, top_at(FX1) - SIDE_RISE * t + 0.5), (x, BASE - SIDE_RISE * t)]), fill=B.ramp(WOOD, 0.16),
               width=max(1, int(0.4 * S)))
    # The front wall's boards and battens.
    boards(d, rnd, FX0, FX1, 6, 0.5)
    # The corner boards, the front-left one lit.
    d.rectangle(sc([(FX0, top_at(FX0)), (FX0 + 1.3, BASE)]), fill=B.ramp(WOOD, 0.7))
    d.rectangle(sc([(FX1 - 1.0, top_at(FX1)), (FX1 + 0.2, BASE)]), fill=B.ramp(WOOD, 0.36))

    # The door: planks in a frame, braced with a Z of battens.
    dx0, dx1, dy0 = 6.5, 19.5, 28.0
    # The casing round the door: lit boards, then a dark gap inside them, so
    # the door stands apart from the wall.
    d.rectangle(sc([(dx0 - 2.0, dy0 - 2.0), (dx1 + 2.0, BASE)]), fill=B.ramp(WOOD, 0.74))
    d.rectangle(sc([(dx1 + 0.9, dy0 - 1.0), (dx1 + 2.0, BASE)]), fill=B.ramp(WOOD, 0.5))
    d.rectangle(sc([(dx0 - 0.7, dy0 - 0.7), (dx1 + 0.7, BASE)]), fill=B.ramp(WOOD, 0.04))
    n = 4
    pw = (dx1 - dx0) / n
    for k in range(n):
        a, b = dx0 + k * pw + 0.1, dx0 + (k + 1) * pw - 0.1
        t = 0.6 + rnd.uniform(-0.05, 0.05)
        d.rectangle(sc([(a, dy0), (b, BASE - 0.4)]), fill=B.ramp(WOOD, t))
        d.rectangle(sc([(a, dy0), (a + 0.4, BASE - 0.4)]), fill=B.ramp(WOOD, t + 0.14))
    # The diamond cut for air, high in the door.
    vx, vy = (dx0 + dx1) / 2, dy0 + 4.2
    d.polygon(sc([(vx, vy - 2.2), (vx + 1.8, vy), (vx, vy + 2.2), (vx - 1.8, vy)]), fill=B.ramp(WOOD, 0.14))
    # The light catches the cut's lower-right edges.
    d.line(sc([(vx + 1.8, vy), (vx, vy + 2.2), (vx - 1.8, vy)]), fill=B.ramp(CUT, 0.75), width=max(1, int(0.4 * S)))
    # The Z: a ledge across the top and the bottom, a brace between.
    for y in (dy0 + 8.5, BASE - 6.0):
        d.rectangle(sc([(dx0 + 0.3, y - 1.1), (dx1 - 0.3, y + 1.1)]), fill=B.ramp(WOOD, 0.48))
        d.rectangle(sc([(dx0 + 0.3, y - 1.1), (dx1 - 0.3, y - 0.6)]), fill=B.ramp(WOOD, 0.72))
        d.rectangle(sc([(dx0 + 0.3, y + 0.7), (dx1 - 0.3, y + 1.1)]), fill=B.ramp(WOOD, 0.22))
    d.line(sc([(dx0 + 1.4, BASE - 6.2), (dx1 - 1.4, dy0 + 10.2)]), fill=B.ramp(WOOD, 0.2), width=int(2.8 * S))
    d.line(sc([(dx0 + 1.4, BASE - 7.0), (dx1 - 1.4, dy0 + 9.4)]), fill=B.ramp(WOOD, 0.66), width=int(2.2 * S))
    d.line(sc([(dx0 + 1.4, BASE - 7.9), (dx1 - 1.4, dy0 + 8.5)]), fill=B.ramp(WOOD, 0.88), width=max(1, int(0.45 * S)))
    # Strap hinges on the left, over the ledges, iron with a lit top.
    # Strap hinges: short dark iron bands on the ledges, each ending in a
    # round hinge knuckle on its pintle, out on the casing at the door's edge.
    for y in (dy0 + 8.5, BASE - 6.0):
        d.polygon(sc([(dx0 - 0.4, y - 0.8), (dx0 + 5.2, y - 0.45), (dx0 + 5.2, y + 0.45), (dx0 - 0.4, y + 0.8)]),
                  fill=B.ramp(IRON, 0.32))
        d.line(sc([(dx0 - 0.4, y - 0.7), (dx0 + 5.2, y - 0.4)]), fill=B.ramp(IRON, 0.8), width=max(1, int(0.3 * S)))
        d.ellipse(sc([(dx0 - 2.1, y - 1.2), (dx0 - 0.1, y + 1.2)]), fill=B.ramp(IRON, 0.25))
        d.ellipse(sc([(dx0 - 1.8, y - 1.0), (dx0 - 0.7, y + 0.0)]), fill=B.ramp(IRON, 1.0))
    # The latch: a wooden bar on the right with a leather pull.
    ly = (dy0 + BASE) / 2 + 1.5
    d.rectangle(sc([(dx1 - 6.0, ly - 0.8), (dx1 + 1.8, ly + 0.8)]), fill=B.ramp(WOOD, 0.15))
    d.rectangle(sc([(dx1 - 5.7, ly - 0.6), (dx1 + 1.5, ly + 0.5)]), fill=B.ramp(CUT, 0.75))
    d.rectangle(sc([(dx1 - 5.7, ly - 0.6), (dx1 + 1.5, ly - 0.2)]), fill=B.ramp(CUT, 1.0))
    # Its keeper on the casing, and the leather pull hanging from the bar.
    d.rectangle(sc([(dx1 + 0.6, ly - 1.4), (dx1 + 1.9, ly + 1.4)]), fill=B.ramp(WOOD, 0.3))
    # The pull: a loop of tanned leather hanging from the bar.
    d.line(sc([(dx1 - 4.8, ly + 0.6), (dx1 - 4.9, ly + 4.0), (dx1 - 3.8, ly + 4.9), (dx1 - 2.7, ly + 4.0),
               (dx1 - 2.8, ly + 0.6)]), fill=B.ramp(LEATHER, 0.45), width=max(1, int(0.9 * S)))
    # The sill: a log the walls stand on, dark where it meets the ground.
    d.rectangle(sc([(FX0, BASE - 1.3), (FX1, BASE)]), fill=B.ramp(WOOD, 0.42))
    d.rectangle(sc([(FX0, BASE - 1.3), (FX1, BASE - 0.8)]), fill=B.ramp(WOOD, 0.66))

    # The shed roof, a slab seen from a little above. Its top is the
    # parallelogram spanned by the front edge (along the wall's top, rising
    # to the right) and the side wall's depth (back and up, as the side
    # recedes), so its far end sits on the side wall and nothing shows
    # between them. Riven boards lie down the slope in courses running along
    # it, each course's lower edge in shadow; a lit fascia on the front edge
    # and the slab's thickness at its high right end.
    over_l, over_r, thick = 1.0, 0.4, 1.6

    def eave(x):
        """The roof's front edge at x: just above the wall's top."""
        return (x, top_at(x) - 0.2)

    f0, f1 = eave(FX0 - over_l), eave(FX1 + over_r)
    dv = (SX1 - FX1 + 0.5, -SIDE_RISE - 0.4)
    b0, b1 = (f0[0] + dv[0], f0[1] + dv[1]), (f1[0] + dv[0], f1[1] + dv[1])
    # The slab's thickness: under the front edge and down the high end.
    d.polygon(sc([f0, f1, (f1[0], f1[1] + thick), (f0[0], f0[1] + thick)]), fill=B.ramp(WOOD, 0.3))
    d.polygon(sc([f1, b1, (b1[0], b1[1] + thick), (f1[0], f1[1] + thick)]), fill=B.ramp(WOOD, 0.22))
    # The top, then the courses.
    d.polygon(sc([f0, f1, b1, b0]), fill=B.ramp(ROOF, 0.55))
    courses = 3
    for k in range(courses):
        t0, t1 = k / courses, (k + 1) / courses

        def at(s_, t):
            return (f0[0] + (f1[0] - f0[0]) * s_ + dv[0] * t, f0[1] + (f1[1] - f0[1]) * s_ + dv[1] * t)

        tone = 0.74 + 0.06 * (courses - k) / courses
        d.polygon(sc([at(0, t0), at(1, t0), at(1, t1), at(0, t1)]), fill=B.ramp(ROOF, tone))
        # The butt of the course above casts a thin shadow on this one.
        d.line(sc([at(0, t1 - 0.05), at(1, t1 - 0.05)]), fill=B.ramp(ROOF, 0.12), width=max(1, int(0.5 * S)))
        # Board joints, staggered course to course.
        n = 5
        for j in range(1, n):
            s_ = (j + 0.5 * (k % 2)) / n + rnd.uniform(-0.03, 0.03)
            if 0.03 < s_ < 0.97:
                d.line(sc([at(s_, t0 + 0.08), at(s_, t1 - 0.12)]), fill=B.ramp(ROOF, 0.45), width=max(1, int(0.22 * S)))
    # The fascia: lit along its top, the eave's shadow line under it on the wall.
    d.line(sc([(f0[0], f0[1] + thick + 0.4), (f1[0], f1[1] + thick + 0.4)]), fill=B.ramp(WOOD, 0.1),
           width=max(1, int(0.6 * S)))
    d.line(sc([(f0[0], f0[1] + 0.35), (f1[0], f1[1] + 0.35)]), fill=B.ramp(WOOD, 0.7), width=max(1, int(0.5 * S)))
    d.line(sc([f0, f1]), fill=B.ramp(WOOD, 1.0), width=max(1, int(0.45 * S)))
    return finish(lay, shade)


def finish(lay, shade):
    """plants.shrink, then the cast shadow with a dithered outer edge: a
    frame pixel the art does not cover takes B.SHADOW where the shadow is
    strong, and every other one (a checker) where it is only partly in it."""
    out = P.shrink(lay, PALETTE, 0.45)
    small = shade.resize((P.FW, P.FH), Image.BOX).load()
    op = out.load()
    for y in range(P.FH):
        for x in range(P.FW):
            if op[x, y][3]:
                continue
            v = small[x, y]
            if v >= 150 or (v >= 70 and (x + y) % 2 == 0):
                op[x, y] = B.SHADOW
    return out


# One frame, the default state of the catalog asset.
OUTHOUSE_ID = "053ec0f2-79bd-4db6-a9df-fec642792244"   # Outhouse 1
LAYOUT = {"outhouse": (OUTHOUSE_ID, "default", paint_outhouse)}
SHEETS = {name: (lambda paint=paint: paint()) for name, (_, _, paint) in LAYOUT.items()}
FRAMES = {name: (FRAME[0] * DENSITY, FRAME[1] * DENSITY) for name in LAYOUT}
MIGRATION = os.path.join(B.ROOT, "migrations", "LLM-968p-painted-outhouse_up.sql")
STATE_ROW = re.compile(r"\('([0-9a-f-]{36})', '([^']+)', '([^']+)', (\d+), (\d+), (\d+), (\d+), (\d+), ([\d.]+)\)")


def migration_states(path=MIGRATION):
    """The migration's llm968p_state rows, as ({(asset id, state): (sheet,
    src_x, src_y, src_w, src_h, frame_count, frame_rate)}, problems). The
    VALUES block (comments aside) must be exactly a comma-separated list of
    rows that parse, and no (asset, state) may appear twice."""
    text = open(path, encoding="utf-8").read()
    m = re.search(r"INSERT INTO llm968p_state VALUES(.*?);", text, re.S)
    if not m:
        return {}, ["the migration has no llm968p_state VALUES block"]
    body = re.sub(r"--[^\n]*", "", m.group(1))
    rows, problems = {}, []
    for r in STATE_ROW.finditer(body):
        key = (r.group(1), r.group(2))
        if key in rows:
            problems.append("migration state %s %s is written twice" % key)
        rows[key] = (r.group(3), int(r.group(4)), int(r.group(5)), int(r.group(6)), int(r.group(7)),
                     int(r.group(8)), float(r.group(9)))
    # The block must be exactly a comma-separated list of state rows: no
    # other text, and no stray, doubled or trailing comma (PostgreSQL would
    # reject one, but only when the migration runs).
    row = STATE_ROW.pattern
    if not re.fullmatch(r"\s*%s(?:\s*,\s*%s)*\s*" % (row, row), body):
        problems.append("migration llm968p_state VALUES is not a comma-separated list of state rows: %s"
                        % " ".join(body.split())[:120])
    return rows, problems


def migration_problems(path=MIGRATION):
    """The migration must point the state at the sheet's one frame, still
    (frame_count 1, frame_rate 0), and write no other state."""
    rows, out = migration_states(path)
    want = {(asset_id, state): ("res://assets/trees/%s.png" % name, 0, 0) + FRAMES[name] + (1, 0.0)
            for name, (asset_id, state, _) in LAYOUT.items()}
    for key in sorted(set(want) | set(rows)):
        if rows.get(key) != want.get(key):
            out.append("migration state %s %s is %s, the layout says %s" % (key[0], key[1], rows.get(key), want.get(key)))
    return out


def check(out_dir):
    """The sheet is exactly what this script paints now, one frame of its
    size, with no art on its outer rows or columns; and the migration points
    the state at it."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun outhouse.py)" % name)
            ok = False
            continue
        if sheet.size != FRAMES[name]:
            print("%s: sheet %dx%d is not one %dx%d frame" % ((name,) + sheet.size + FRAMES[name]))
            ok = False
            continue
        if any(F.edges_touched(sheet)):
            print("%s: art on the frame edge" % name)
            ok = False
    for problem in migration_problems():
        print(problem)
        ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's outhouse.")
    parser.add_argument("--out", default=OUT_DIR, help="where the sheet goes (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheet instead of painting it")
    args = parser.parse_args()
    if args.check:
        sys.exit(0 if check(args.out) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name, build in SHEETS.items():
        build().save(os.path.join(args.out, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
