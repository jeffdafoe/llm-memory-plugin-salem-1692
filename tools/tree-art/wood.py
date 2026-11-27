"""Paints salem's woodpiles (LLM-968): Wood Pile (Large) and the Wood
Shelter. Every pixel is drawn here, no pack art.

    python tools/tree-art/wood.py
    python tools/tree-art/wood.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/wood.py --check     # margins, sizes, a fresh paint

Writes client/assets/trees/wood-pile.png and wood-shelter.png, one 64x64
frame each (32x32 pack px drawn at density 2, shown at render_scale 1: the
same size in the world, twice the detail). Anchor 0.5/0.85 as the pack art,
so the placed piles keep their place.

Wood Pile (Large): a rick of split cordwood seen end-on, the courses laid
between two driven stakes and domed on top, the split faces toward us (as
the camp's woodpile). Wood Shelter: an open woodshed seen corner-on like the
buildings, a riven-board roof on four posts over cordwood stacked to the
eaves, its far end and back boarded.

From period New England practice: firewood cut and split in the fall and
ricked to dry, under a shed roof where there was one.
"""

import argparse
import math
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

WOOD = F.WOOD
CUT = F.CUT
# Weathered roof boards, silvered (as the Wood market stall's roof).
ROOF = ["262420", "3a3630", "504b44", "68625a", "827b72", "9c958a"]
# Fresh-split wood, warmer than the weathered cut ends of fence posts.
SPLIT = ["6a4a2c", "8e663c", "b0864e", "cca468", "e0c08a"]
PALETTE = WOOD + CUT + ROOF + SPLIT

FRAME = (32, 32)
FEET = 0.85


def new_layers():
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    return lay, ImageDraw.Draw(lay), shade, ImageDraw.Draw(shade)


def sc(pts):
    return [(x * P.S, y * P.S) for x, y in pts]


def split_end(d, rnd, x, y, r, tone=0.6):
    """The end of a split billet, centred (x, y), r across: a round or a
    half-round with its split side flat, a thin dark rim of bark, the warm
    cut face, a lit crescent on its upper left. Kept simple so a course of
    them reads as stacked ends at game zoom."""
    pts = [(x + r * math.cos(t), y + r * 0.92 * math.sin(t)) for t in (2 * math.pi * k / 16 for k in range(16))]
    if rnd.random() < 0.55:
        # A split piece: the round cut flat along one side (the split), at a
        # slant, keeping most of its size.
        a0 = rnd.uniform(0, 2 * math.pi)
        nx, ny = math.cos(a0), math.sin(a0)
        pts = [(px, py) for px, py in pts if (px - x) * nx + (py - y) * ny <= 0.45 * r]
    d.polygon(sc(pts), fill=B.ramp(WOOD, 0.3))
    inner = [(x + (px - x) * 0.78, y + (py - y) * 0.78) for px, py in pts]
    d.polygon(sc(inner), fill=B.ramp(SPLIT, tone + rnd.uniform(-0.08, 0.08)))
    lit = [(x + (px - x) * 0.78, y + (py - y) * 0.78) for px, py in pts if px - x + py - y < -0.3 * r]
    if len(lit) > 1:
        d.line(sc(lit), fill=B.ramp(SPLIT, 0.95), width=max(1, int(0.5 * P.S)))


# ---------------------------------------------------------------- wood pile

def paint_wood_pile(seed=731):
    """A rick of split cordwood end-on: courses of billets between two
    driven stakes, domed on top."""
    P.use_frame(*FRAME, density=DENSITY)
    lay, d, shade, sd = new_layers()
    rnd = random.Random(seed)
    cx, base = 16.0, FEET * FRAME[1]
    sd.ellipse(sc([(cx - 12.0, base - 1.8), (cx + 14.0, base + 1.8)]), fill=255)
    # The courses, bottom up, each a whole number of billets on a fixed
    # pitch, each course one fewer and sitting in the hollows of the one
    # below, so the rick steps in at the shoulders to a domed top; a dark
    # gap shows round every end.
    r, pitch, rise = 2.3, 5.1, 4.2
    counts = (5, 4, 3, 2)
    rows = [(cx, base - r - 0.4 - i * rise, n) for i, n in enumerate(counts)]
    # The stakes, driven behind the bottom course's outer ends: only their
    # tops show, at the shoulders.
    for sx in (cx - (counts[0] - 1) * pitch / 2, cx + (counts[0] - 1) * pitch / 2):
        F.draw_post(d, sx, base - 2 * rise - 1.6, base - 2.0, rnd.randint(0, 999), w=1.4, tone=0.4)
    # The dark behind the billets: one block per course, inset to the ends
    # so the silhouette follows the billets, not a box.
    for x, y, n in rows:
        w = n * pitch
        d.rounded_rectangle(sc([(x - w / 2 + 0.4, y - r + 0.2), (x + w / 2 - 0.4, y + r)]), radius=r * P.S * 0.8,
                            fill=B.ramp(WOOD, 0.08))
    for x, y, n in rows:
        for k in range(n):
            ex = x - n * pitch / 2 + pitch * (k + 0.5) + rnd.uniform(-0.2, 0.2)
            split_end(d, rnd, ex, y, r * rnd.uniform(0.94, 1.04), tone=0.62)
    return F.shrink_shaded(lay, shade, PALETTE)


# ---------------------------------------------------------------- wood shelter

# The shed on the ground, corner-on like the buildings and the market stalls:
# the near corner N, the front-left face running up-left from it (the open
# front), the front-right face running up-right (the boarded end).
A = (-1.0, -0.86)
BV = (1.0, -0.46)
N = (19.5, 29.5)
UW, VD = 13.0, 6.0
H = 12.0            # eave height
PEAK = 3.5
OVER = 0.8


def gp(u, v, h=0.0):
    """Ground point at u along the front from the LEFT corner (0) to the near
    one (UW), v back from the front (0) to the back (VD), lifted h."""
    du = UW - u
    return (N[0] + du * A[0] + v * BV[0], N[1] + du * A[1] + v * BV[1] - h)


def face_point(a, b, t, h):
    return (a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t - h)


def shelter_post(d, u, v, tone, edge=False, w=0.8):
    """A post at ground point (u, v) up to the eaves, lit on its left; with
    edge, a dark line down each side to part it from what is behind."""
    x0, y0 = gp(u, v)
    _, y1 = gp(u, v, H)
    if edge:
        d.rectangle(sc([(x0 - w - 0.35, y1), (x0 + w + 0.35, y0)]), fill=B.ramp(WOOD, 0.06))
    d.rectangle(sc([(x0 - w, y1), (x0 + w, y0)]), fill=B.ramp(WOOD, tone))
    d.rectangle(sc([(x0 - w, y1), (x0 - w * 0.2, y0)]), fill=B.ramp(WOOD, tone + 0.2))


def board_face(d, a, b, h0, h1, tone, n, rnd):
    """Upright boards on a face from ground points a to b, heights h0..h1."""
    d.polygon(sc([face_point(a, b, 0, h0), face_point(a, b, 1, h0), face_point(a, b, 1, h1), face_point(a, b, 0, h1)]),
              fill=B.ramp(WOOD, tone - 0.22))
    for k in range(n):
        ta, tb = (k + 0.1) / n, (k + 0.9) / n
        t = tone + rnd.uniform(-0.06, 0.06)
        d.polygon(sc([face_point(a, b, ta, h0), face_point(a, b, tb, h0), face_point(a, b, tb, h1),
                      face_point(a, b, ta, h1)]), fill=B.ramp(WOOD, t))


def paint_wood_shelter(seed=741):
    """The woodshed: the back wall and back posts, the cordwood seen through
    the open front in three courses of large split ends, the front posts
    standing clear of it, the boarded gable end, then the roof."""
    P.use_frame(*FRAME, density=DENSITY)
    lay, d, shade, sd = new_layers()
    rnd = random.Random(seed)
    # A tight shadow under the shed, a little down and right of its feet.
    g = [gp(0, 0), gp(UW, 0), gp(UW, VD), gp(0, VD)]
    sd.polygon(sc([(x + 1.4, y + 0.6) for x, y in g]), fill=255)
    # The back wall, seen over the wood (dark: inside the shed).
    board_face(d, gp(0, VD), gp(UW, VD), 0, H, 0.28, 7, rnd)
    shelter_post(d, 0, VD, 0.3)
    # The cordwood on a plane just inside the open front: a dark backing,
    # then three courses of ends on a fixed pitch, clear of the posts.
    front_a, front_b = gp(0, 0.9), gp(UW, 0.9)
    r, rise = 1.75, 3.8
    top_h = r + 0.3 + 2 * rise + r + 0.3
    d.polygon(sc([face_point(front_a, front_b, 0.04, 0), face_point(front_a, front_b, 0.96, 0),
                  face_point(front_a, front_b, 0.96, top_h), face_point(front_a, front_b, 0.04, top_h)]),
              fill=B.ramp(WOOD, 0.08))
    for i in range(3):
        h = r + 0.3 + i * rise
        n = 4
        for k in range(n):
            t = 0.14 + 0.72 * (k + 0.5) / n
            x, y = face_point(front_a, front_b, t, h)
            split_end(d, rnd, x, y, r * rnd.uniform(0.95, 1.03), tone=0.6)
    # A dark contact line along the open front and under the end, so the
    # shed sits on the ground (the cast shadow alone is faint at game zoom).
    for a, b in ((gp(0, 0), gp(UW, 0)), (gp(UW, 0), gp(UW, VD))):
        d.line(sc([(a[0] + 0.3, a[1] + 0.35), (b[0] + 0.3, b[1] + 0.35)]), fill=B.ramp(WOOD, 0.05),
               width=max(1, int(0.7 * P.S)))
    # The open front's posts, each with a dark edge so it stands clear of
    # the wood behind it.
    shelter_post(d, 0, 0, 0.55, edge=True)
    # The boarded end (front-right face), in shade, and its back post.
    board_face(d, gp(UW, 0), gp(UW, VD), 0, H, 0.4, 4, rnd)
    shelter_post(d, UW, VD, 0.3)
    # The near corner post: narrower, lit, with a dark edge on its right
    # against the boards of the end.
    shelter_post(d, UW, 0, 0.72, edge=True, w=0.65)
    # The roof: a low gable, its ridge along the front, riven boards down
    # the front slope; the gable end boarded with a clear bargeboard.
    eF0, eF1 = gp(-OVER, -OVER, H), gp(UW + OVER, -OVER, H)
    r0, r1 = gp(-OVER, VD / 2, H + PEAK), gp(UW + OVER, VD / 2, H + PEAK)
    eB1 = gp(UW + OVER, VD + OVER, H)
    gab = [gp(UW, 0, H), gp(UW, VD / 2, H + PEAK), gp(UW, VD, H)]
    d.polygon(sc(gab), fill=B.ramp(WOOD, 0.4))

    def q(s_, t):
        e = (eF0[0] + (eF1[0] - eF0[0]) * s_, eF0[1] + (eF1[1] - eF0[1]) * s_)
        rr = (r0[0] + (r1[0] - r0[0]) * s_, r0[1] + (r1[1] - r0[1]) * s_)
        return (e[0] + (rr[0] - e[0]) * t, e[1] + (rr[1] - e[1]) * t)

    d.polygon(sc([q(0, 0), q(1, 0), q(1, 1), q(0, 1)]), fill=B.ramp(ROOF, 0.15))
    n = 6
    for k in range(n):
        s0, s1 = (k + 0.08) / n, (k + 0.92) / n
        tone = 0.62 + rnd.uniform(-0.06, 0.06)
        d.polygon(sc([q(s0, 0), q(s1, 0), q(s1, 1), q(s0, 1)]), fill=B.ramp(ROOF, tone))
        d.line(sc([q(s0, 0), q(s0, 1)]), fill=B.ramp(ROOF, tone + 0.2), width=max(1, int(0.45 * P.S)))
    d.line(sc([r0, r1]), fill=B.ramp(WOOD, 0.6), width=int(0.6 * P.S))
    d.line(sc([eF0, eF1]), fill=B.ramp(ROOF, 0.08), width=int(0.6 * P.S))
    # The bargeboard up and down the gable: dark, with a lit upper edge.
    d.line(sc([eF1, r1, eB1]), fill=B.ramp(WOOD, 0.15), width=int(0.8 * P.S))
    d.line(sc([(eF1[0], eF1[1] - 0.4), (r1[0], r1[1] - 0.4)]), fill=B.ramp(WOOD, 0.75), width=max(1, int(0.3 * P.S)))
    return F.shrink_shaded(lay, shade, PALETTE)


# Each sheet is one frame, the default state of one catalog asset.
PILE_ID = "74881601-00fd-4c07-898b-a9d3c155b4cc"      # Wood Pile (Large)
SHELTER_ID = "57229387-1773-4bf8-89f0-3dc0b090ed4d"   # Wood Shelter
LAYOUT = {"wood-pile": (PILE_ID, "default", paint_wood_pile),
          "wood-shelter": (SHELTER_ID, "default", paint_wood_shelter)}
SHEETS = {name: (lambda paint=paint: paint()) for name, (_, _, paint) in LAYOUT.items()}
FRAMES = {name: (FRAME[0] * DENSITY, FRAME[1] * DENSITY) for name in LAYOUT}
MIGRATION = os.path.join(B.ROOT, "migrations", "LLM-968o-painted-wood_up.sql")
STATE_ROW = re.compile(r"\('([0-9a-f-]{36})', '([^']+)', '([^']+)', (\d+), (\d+), (\d+), (\d+), (\d+), ([\d.]+)\)")


def migration_states(path=MIGRATION):
    """The migration's llm968o_state rows, as ({(asset id, state): (sheet,
    src_x, src_y, src_w, src_h, frame_count, frame_rate)}, problems). Every row of the VALUES block
    must parse (comments and the commas between rows aside), and no
    (asset, state) may appear twice."""
    text = open(path, encoding="utf-8").read()
    m = re.search(r"INSERT INTO llm968o_state VALUES(.*?);", text, re.S)
    if not m:
        return {}, ["the migration has no llm968o_state VALUES block"]
    body = re.sub(r"--[^\n]*", "", m.group(1))
    rows, problems = {}, []
    for r in STATE_ROW.finditer(body):
        key = (r.group(1), r.group(2))
        if key in rows:
            problems.append("migration state %s %s is written twice" % key)
        rows[key] = (r.group(3), int(r.group(4)), int(r.group(5)), int(r.group(6)), int(r.group(7)),
                     int(r.group(8)), float(r.group(9)))
    left = STATE_ROW.sub("", body).replace(",", "").split()
    if left:
        problems.append("migration llm968o_state has text that is not a state row: %s" % " ".join(left)[:80])
    return rows, problems


def migration_problems(path=MIGRATION):
    """The migration must point each asset's state at its sheet's one frame,
    still (frame_count 1, frame_rate 0), and write no other state."""
    rows, out = migration_states(path)
    # One still frame each: frame_count 1, frame_rate 0.
    want = {(asset_id, state): ("res://assets/trees/%s.png" % name, 0, 0) + FRAMES[name] + (1, 0.0)
            for name, (asset_id, state, _) in LAYOUT.items()}
    for key in sorted(set(want) | set(rows)):
        if rows.get(key) != want.get(key):
            out.append("migration state %s %s is %s, the layout says %s" % (key[0], key[1], rows.get(key), want.get(key)))
    return out


def check(out_dir):
    """Each sheet is exactly what this script paints now, one frame of its
    size, with no art on its outer rows or columns; and the migration points
    each state at its sheet."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun wood.py)" % name)
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
    parser = argparse.ArgumentParser(description="Paint salem's woodpiles.")
    parser.add_argument("--out", default=OUT_DIR, help="where the sheets go (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheets instead of painting them")
    args = parser.parse_args()
    if args.check:
        sys.exit(0 if check(args.out) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name, build in SHEETS.items():
        build().save(os.path.join(args.out, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
