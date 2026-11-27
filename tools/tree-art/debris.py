"""Paints salem's debris heap (LLM-968): the Debris overlay a damaged
business carries by its door. Every pixel is drawn here, no pack art.

    python tools/tree-art/debris.py
    python tools/tree-art/debris.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/debris.py --check     # margins, sizes, a fresh paint

Writes client/assets/trees/debris-heap.png: one row of two frames, each
64x40 pack px drawn at density 2 (128x80 on the sheet) and shown at
render_scale 1 (the same size in the world, twice the detail). Anchor
0.5/0.85 as the pack art, so the heap sits where it did by each door
(the "debris" slots on the business buildings, LLM-675b).

Frames, left to right, by state (damage.go placeDebris picks the state from
the damage trigger):
- worn: the shop has worn out. Boards come off it: a battened shutter fallen
  flat with a corner split off, loose clapboards and planks across it, two
  boards leaning, their broken ends splintered.
- storm: a storm has struck. A limb torn off a tree lies across the door,
  its torn butt splintered, its side branches and twigs still in leaf, with
  a smaller broken branch by it.

From period New England practice: riven clapboards and board-and-batten
shutters of sawn or split oak and pine, unpainted and silvered with weather.
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
import lamps as L
import wood as WD

OUT_DIR = B.OUT_DIR
DENSITY = 2

WOOD = F.WOOD
CUT = F.CUT
# Fresh-broken wood: the warm split faces of the woodpiles.
SPLIT = WD.SPLIT
BARK = B.BARK
# Leaves still on a torn-off limb: a dull summer green.
LEAF = ["2c3824", "3e4c2e", "546438", "6c7c46", "88965a"]
PALETTE = WOOD + CUT + SPLIT + BARK + LEAF

FRAME = (64, 40)
FEET = 0.85
GROUND = FEET * FRAME[1]
CX = FRAME[0] / 2


def new_layers():
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    return lay, ImageDraw.Draw(lay), shade, ImageDraw.Draw(shade)


def sc(pts):
    return [(x * P.S, y * P.S) for x, y in pts]


def width(px):
    return max(1, int(px * P.S))


# ---------------------------------------------------------------- boards

def board(d, rnd, p0, p1, w, tone, broken=False, thick=0.8, side=(0.0, 1.0), edge=False):
    """A board from p0 to p1, w across, in frame px: a dark edge face thick
    px out along side (below a board lying flat, to the right of a leaning
    one), the weathered top face lit along its upper edge with a grain line
    or two, and at p1, when broken, a jagged end of pale splinters."""
    (x0, y0), (x1, y1) = p0, p1
    length = math.hypot(x1 - x0, y1 - y0)
    ux, uy = (x1 - x0) / length, (y1 - y0) / length
    nx, ny = -uy, ux
    if ny > 0 or (ny == 0 and nx > 0):
        nx, ny = -nx, -ny
    hw = w / 2
    a = (x0 + nx * hw, y0 + ny * hw)
    b = (x1 + nx * hw, y1 + ny * hw)
    c = (x1 - nx * hw, y1 - ny * hw)
    e = (x0 - nx * hw, y0 - ny * hw)
    if broken:
        # The broken end: teeth of uneven length along the board, across its
        # width from the lit edge to the dark one.
        teeth = []
        for k in range(5):
            t = k / 4
            reach = rnd.uniform(1.0, 2.6) if k % 2 else rnd.uniform(-0.6, 0.3)
            teeth.append((b[0] + (c[0] - b[0]) * t + ux * reach, b[1] + (c[1] - b[1]) * t + uy * reach))
        top = [a] + teeth + [e]
    else:
        top = [a, b, c, e]
    dx, dy = side[0] * thick, side[1] * thick
    if edge:
        # A dark rim all round, to part it from a board it crosses.
        rim = [(x + dx * 0.5, y + dy * 0.5) for x, y in top]
        d.line(sc(rim + rim[:1]), fill=B.ramp(WOOD, 0.04), width=width(thick + 1.0), joint="curve")
    d.polygon(sc([(x + dx, y + dy) for x, y in top]), fill=B.ramp(WOOD, 0.1))
    d.polygon(sc(top), fill=B.ramp(WOOD, tone))
    d.line(sc([a, b]), fill=B.ramp(WOOD, tone + 0.25), width=width(0.4))
    for _ in range(2):
        f = rnd.uniform(-0.25, 0.25) * w
        s0, s1 = rnd.uniform(0.05, 0.3), rnd.uniform(0.55, 0.9)
        d.line(sc([(x0 + ux * length * s0 + nx * f, y0 + uy * length * s0 + ny * f),
                   (x0 + ux * length * s1 + nx * f, y0 + uy * length * s1 + ny * f)]),
               fill=B.ramp(WOOD, tone - 0.2), width=width(0.3))
    if broken:
        # The splinters: pale fresh wood a little way back from each tooth.
        for (tx, ty) in teeth:
            back = rnd.uniform(1.2, 2.2)
            d.line(sc([(tx, ty), (tx - ux * back, ty - uy * back)]), fill=B.ramp(SPLIT, 0.75), width=width(0.45))
        d.line(sc(teeth), fill=B.ramp(SPLIT, 0.55), width=width(0.4))
    else:
        # A sawn end: the end grain, pale, across the board.
        d.line(sc([b, c]), fill=B.ramp(CUT, 0.75), width=width(0.7))


def board_shadow(sd, p0, p1, w):
    (x0, y0), (x1, y1) = p0, p1
    sd.line(sc([(x0 + 0.6, y0 + 0.8), (x1 + 0.6, y1 + 0.8)]), fill=255, width=width(w + 0.6))


def shutter(d, rnd, corner, u, v, boards=4, tone=0.5):
    """A board-and-batten shutter lying flat: a panel from corner along u
    (the boards' run) and v (across them), a dark edge below and dark gaps
    between its boards, two battens across them, and its far corner split
    off along a jagged line, each board's broken end splintered pale.
    Returns the panel's front edge, for its contact shadow."""
    ox, oy = corner
    (ux, uy), (vx, vy) = u, v

    def at(s, t):
        return (ox + ux * s + vx * t, oy + uy * s + vy * t)

    # Where the split runs: s along the boards for t across them. The panel
    # is whole up to s = 1 for t under CUT_T, then broken back to CUT_S at
    # the far edge.
    cut_t, cut_s = 0.3, 0.52

    def end_s(t):
        return 1.0 if t <= cut_t else 1.0 - (1.0 - cut_s) * (t - cut_t) / (1.0 - cut_t)

    whole = [at(0, 0), at(1, 0), at(1, cut_t), at(cut_s, 1), at(0, 1)]
    d.polygon(sc([(x, y + 0.9) for x, y in whole]), fill=B.ramp(WOOD, 0.08))
    d.polygon(sc(whole), fill=B.ramp(WOOD, 0.04))
    for k in range(boards):
        t0, t1 = (k + 0.17) / boards, (k + 0.83) / boards
        tt = tone + (0.08 if k % 2 else -0.06) + rnd.uniform(-0.03, 0.03)
        s0, s1 = end_s(t0), end_s(t1)
        pts = [at(0, t0), at(s0, t0), at(s1, t1), at(0, t1)]
        d.polygon(sc(pts), fill=B.ramp(WOOD, tt))
        d.line(sc(pts[:2]), fill=B.ramp(WOOD, tt + 0.22), width=width(0.4))
        if s1 < 1.0:
            # The broken end: pale splinters standing out past the split.
            d.line(sc([pts[1], pts[2]]), fill=B.ramp(SPLIT, 0.7), width=width(0.6))
            for j in range(3):
                t = t0 + (t1 - t0) * (j + 0.5) / 3
                s = end_s(t)
                reach = rnd.uniform(0.05, 0.12)
                a, b = at(s - 0.02, t - 0.04), at(s - 0.02, t + 0.04)
                d.polygon(sc([a, b, at(s + reach, t + rnd.uniform(-0.05, 0.05))]),
                          fill=B.ramp(SPLIT, rnd.uniform(0.6, 0.95)))
    for s in (0.18, 0.62):
        tb = 1.02 if s <= cut_s else min(1.02, cut_t + (1.0 - s) / (1.0 - cut_s) * (1.0 - cut_t) - 0.04)
        d.polygon(sc([at(s, -0.04), at(s + 0.06, -0.04), at(s + 0.06, tb), at(s, tb)]),
                  fill=B.ramp(WOOD, tone - 0.14))
        d.line(sc([at(s, -0.04), at(s, tb)]), fill=B.ramp(WOOD, tone + 0.12), width=width(0.4))
    return at(0, 1), at(cut_s, 1)


def paint_worn(seed=811):
    """The worn heap: two boards leaning back against the wall, splayed, the
    fallen shutter, loose planks across it, small broken pieces at the
    sides; each piece with its own small contact shadow."""
    P.use_frame(*FRAME, density=DENSITY)
    lay, d, shade, sd = new_layers()
    rnd = random.Random(seed)
    # The leaning boards, propped against the wall behind the heap, splayed
    # out to the sides.
    for p0, p1, w, tone, broken in (((22.0, GROUND - 4.0), (11.0, 9.0), 3.6, 0.62, True),
                                    ((42.0, GROUND - 4.5), (53.0, 11.0), 3.4, 0.54, False)):
        sd.ellipse(sc([(p0[0] - 2.0, p0[1] - 0.6), (p0[0] + 2.6, p0[1] + 1.2)]), fill=255)
        board(d, rnd, p0, p1, w, tone, broken=broken, thick=1.0, side=(0.9, 0.2))
    # The shutter lying flat, a little askew.
    front = shutter(d, rnd, (12.0, GROUND - 11.0), (25.0, -2.8), (3.4, 9.0), boards=4, tone=0.72)
    board_shadow(sd, front[0], front[1], 1.0)
    # Loose planks across the shutter and the ground, two snapped.
    for p0, p1, w, tone, broken in (((25.0, GROUND - 1.6), (55.0, GROUND - 6.6), 2.6, 0.7, True),
                                    ((34.0, GROUND - 0.2), (51.5, GROUND + 0.2), 2.2, 0.46, False),
                                    ((9.0, GROUND - 2.4), (23.0, GROUND - 4.4), 2.0, 0.58, True)):
        board_shadow(sd, p0, p1, w * 0.5)
        board(d, rnd, p0, p1, w, tone, broken=broken, edge=True)
    # Small broken pieces: a split-off shingle or two.
    for p0, p1 in (((6.5, GROUND - 0.4), (10.0, GROUND + 0.4)), ((54.5, GROUND - 1.6), (58.0, GROUND - 0.6))):
        board(d, rnd, p0, p1, 1.5, 0.55, broken=True, thick=0.6)
    return F.shrink_shaded(lay, shade, PALETTE)


# ---------------------------------------------------------------- limbs

def limb(d, pts, r0, r1, tone=0.45):
    """A bark-covered limb lying along pts, r0 thick at the first point
    tapering to r1 at the last: a dark underside, the bark, a lit stripe
    along its upper side."""
    n = len(pts) - 1
    rads = [r0 + (r1 - r0) * i / n for i in range(n + 1)]
    for dy, t, k in ((0.45, 0.08, 1.0), (0.0, tone, 1.0), (-0.42, tone + 0.28, 0.32)):
        for i in range(n):
            (xa, ya), (xb, yb) = pts[i], pts[i + 1]
            ra, rb = rads[i] * k, rads[i + 1] * k
            ln = math.hypot(xb - xa, yb - ya)
            nx, ny = -(yb - ya) / ln, (xb - xa) / ln
            oa, ob = dy * rads[i], dy * rads[i + 1]
            d.polygon(sc([(xa + nx * ra, ya + ny * ra + oa), (xb + nx * rb, yb + ny * rb + ob),
                          (xb - nx * rb, yb - ny * rb + ob), (xa - nx * ra, ya - ny * ra + oa)]),
                      fill=B.ramp(BARK, t))
            d.ellipse(sc([(xb - rb, yb - rb + ob), (xb + rb, yb + rb + ob)]), fill=B.ramp(BARK, t))


def bark_marks(d, rnd, pts, r, tone=0.45, n=8):
    """Short dark fissures along a limb."""
    for _ in range(n):
        i = rnd.randrange(len(pts) - 1)
        f = rnd.random()
        (xa, ya), (xb, yb) = pts[i], pts[i + 1]
        x, y = xa + (xb - xa) * f, ya + (yb - ya) * f + rnd.uniform(-0.4, 0.3) * r
        ln = math.hypot(xb - xa, yb - ya)
        m = rnd.uniform(1.0, 2.2)
        d.line(sc([(x, y), (x + (xb - xa) / ln * m, y + (yb - ya) / ln * m)]), fill=B.ramp(BARK, tone - 0.25),
               width=width(0.3))


def torn_butt(d, rnd, x, y, r, toward):
    """The torn end of a limb at (x, y), the limb running toward the given
    direction: a pale splintered face with long splinters standing out of
    it, away from the limb."""
    ux, uy = toward
    # The torn face: ragged, not a sawn round.
    face = []
    for k in range(14):
        t = 2 * math.pi * k / 14
        rr = r * rnd.uniform(0.5, 1.0)
        face.append((x + rr * 0.55 * math.cos(t), y + rr * math.sin(t)))
    d.polygon(sc(face), fill=B.ramp(SPLIT, 0.45))
    # Long splinters standing out of the face, away from the limb, fanned a
    # little; strips of torn wood peeled back along the limb.
    for k in range(7):
        t = (k + 0.5) / 7
        sx, sy = x - uy * r * (t * 2 - 1) * 0.95, y + ux * r * (t * 2 - 1) * 0.95
        reach = rnd.uniform(3.5, 6.5) if k % 2 else rnd.uniform(1.6, 3.2)
        tip = (sx - ux * reach + rnd.uniform(-0.6, 0.6), sy - uy * reach + (t - 0.5) * 2.0)
        d.polygon(sc([(sx - uy * 0.8, sy + ux * 0.8), (sx + uy * 0.8, sy - ux * 0.8), tip]),
                  fill=B.ramp(SPLIT, rnd.uniform(0.5, 0.95)))
    for k in range(3):
        sy = y + (k - 1) * r * 0.6
        ln = rnd.uniform(2.5, 4.5)
        d.line(sc([(x, sy), (x + ux * ln, sy + uy * ln + rnd.uniform(-0.3, 0.3))]), fill=B.ramp(SPLIT, 0.7),
               width=width(0.4))
    d.line(sc(face[9:13]), fill=B.ramp(SPLIT, 0.95), width=width(0.4))


def twig(d, rnd, x, y, a, length, depth, leaves):
    """A twig from (x, y) at angle a (radians, screen), forking depth times;
    the ends gather leaves (a list the caller draws on top)."""
    x1, y1 = x + math.cos(a) * length, y + math.sin(a) * length
    d.line(sc([(x, y), (x1, y1)]), fill=B.ramp(BARK, 0.35 if depth else 0.5), width=width(0.4 + 0.15 * depth))
    if depth == 0:
        leaves.append((x1, y1))
        return
    for da in (-0.55, 0.45):
        twig(d, rnd, x1, y1, a + da + rnd.uniform(-0.2, 0.2), length * rnd.uniform(0.6, 0.8), depth - 1, leaves)


def leaf_cluster(d, rnd, x, y, n=5):
    for _ in range(n):
        lx, ly = x + rnd.uniform(-1.8, 1.8), y + rnd.uniform(-1.4, 1.1)
        w, h = rnd.uniform(1.2, 1.7), rnd.uniform(0.8, 1.1)
        d.ellipse(sc([(lx - w, ly - h), (lx + w, ly + h)]), fill=B.ramp(LEAF, rnd.uniform(0.4, 0.85)))
        d.point(sc([(lx - w * 0.4, ly - h * 0.4)]), fill=B.ramp(LEAF, 0.95))


def paint_storm(seed=823):
    """The storm heap: a limb torn off a tree lying across the door, its
    torn butt at the right, two side branches forking off it with twigs in
    leaf, and a smaller snapped branch in front."""
    P.use_frame(*FRAME, density=DENSITY)
    lay, d, shade, sd = new_layers()
    rnd = random.Random(seed)
    main = [(54.0, GROUND - 4.4), (45.0, GROUND - 3.6), (36.0, GROUND - 4.4), (26.0, GROUND - 6.0),
            (16.0, GROUND - 8.6)]
    leaves = []
    # The branches behind the limb first, so it lies over their forks.
    up = [(41.0, GROUND - 5.0), (36.0, GROUND - 12.0), (31.0, GROUND - 20.0)]
    limb(d, up, 1.7, 0.9, tone=0.42)
    twig(d, rnd, 31.0, GROUND - 20.0, -2.0, 4.6, 2, leaves)
    twig(d, rnd, 34.5, GROUND - 15.0, -0.8, 4.2, 1, leaves)
    back = [(25.0, GROUND - 6.6), (19.0, GROUND - 13.5), (13.5, GROUND - 18.0)]
    limb(d, back, 1.4, 0.8, tone=0.4)
    twig(d, rnd, 13.5, GROUND - 18.0, -2.6, 4.2, 2, leaves)
    twig(d, rnd, 17.0, GROUND - 15.5, -1.4, 3.6, 1, leaves)
    twig(d, rnd, 16.0, GROUND - 8.6, 3.0, 4.5, 1, leaves)
    for x, y in leaves:
        leaf_cluster(d, rnd, x, y, n=rnd.randint(4, 6))
    for (x0, y0), (x1, y1) in zip(main, main[1:]):
        sd.line(sc([(x0 + 0.6, y0 + 2.4), (x1 + 0.6, y1 + 2.4)]), fill=255, width=width(2.6))
    limb(d, main, 3.4, 1.4, tone=0.48)
    bark_marks(d, rnd, main, 2.4, n=12)
    torn_butt(d, rnd, 54.0, GROUND - 4.4, 3.4, (-1.0, 0.08))
    # A twig or two off the near side of the limb, in leaf, lying on the
    # ground in front.
    front = []
    twig(d, rnd, 30.0, GROUND - 3.0, 2.75, 3.0, 1, front)
    for x, y in front:
        leaf_cluster(d, rnd, x, y, n=3)
    # The smaller snapped branch in front, its broken end pale.
    small = [(43.0, GROUND + 0.2), (50.0, GROUND + 0.8), (58.5, GROUND - 0.2)]
    for (x0, y0), (x1, y1) in zip(small, small[1:]):
        sd.line(sc([(x0 + 0.5, y0 + 1.0), (x1 + 0.5, y1 + 1.0)]), fill=255, width=width(1.2))
    limb(d, small, 1.0, 0.65, tone=0.5)
    d.ellipse(sc([(42.4, GROUND - 0.7), (43.8, GROUND + 1.1)]), fill=B.ramp(SPLIT, 0.8))
    return F.shrink_shaded(lay, shade, PALETTE)


# ---------------------------------------------------------------- sheet

# The sheet's frames, left to right, as (catalog asset id, state). The order
# is the sheet's layout: the migration points each state at its frame's
# offset, and --check holds the two together. A frame's picture is chosen by
# its state's name (paint_frame), so the layout cannot name one state and
# paint another.
DEBRIS_ID = "019e5f00-c401-7a10-9e00-000000675001"   # Debris
LAYOUT = {"debris-heap": [(DEBRIS_ID, "worn"), (DEBRIS_ID, "storm")]}


def paint_frame(asset_id, state):
    if asset_id == DEBRIS_ID and state == "worn":
        return paint_worn()
    if asset_id == DEBRIS_ID and state == "storm":
        return paint_storm()
    raise ValueError("no painter for %s %s" % (asset_id, state))


SHEETS = {name: (lambda frames=frames: L.row([paint_frame(a, s) for a, s in frames])) for name, frames in LAYOUT.items()}
FRAMES = {name: (FRAME[0] * DENSITY, FRAME[1] * DENSITY) for name in LAYOUT}
MIGRATION = os.path.join(B.ROOT, "migrations", "LLM-968q-painted-debris_up.sql")
STATE_ROW = re.compile(r"\('([0-9a-f-]{36})', '([^']+)', '([^']+)', (\d+), (\d+), (\d+), (\d+), (\d+), ([\d.]+)\)")


def migration_states(path=MIGRATION):
    """The migration's llm968q_state rows, as ({(asset id, state): (sheet,
    src_x, src_y, src_w, src_h, frame_count, frame_rate)}, problems). Every
    row of the VALUES block must parse (comments and the commas between rows
    aside), and no (asset, state) may appear twice."""
    if not os.path.exists(path):
        return {}, ["the migration %s is missing" % os.path.basename(path)]
    text = open(path, encoding="utf-8").read()
    m = re.search(r"INSERT INTO llm968q_state VALUES(.*?);", text, re.S)
    if not m:
        return {}, ["the migration has no llm968q_state VALUES block"]
    body = re.sub(r"--[^\n]*", "", m.group(1))
    rows, problems = {}, []
    for r in STATE_ROW.finditer(body):
        key = (r.group(1), r.group(2))
        if key in rows:
            problems.append("migration state %s %s is written twice" % key)
        rows[key] = (r.group(3), int(r.group(4)), int(r.group(5)), int(r.group(6)), int(r.group(7)),
                     int(r.group(8)), float(r.group(9)))
    # The whole block must be rows separated by single commas, as PostgreSQL
    # needs: no text that is not a row, no missing or extra comma.
    block = r"\s*%s(?:\s*,\s*%s)*\s*" % (STATE_ROW.pattern, STATE_ROW.pattern)
    if not re.fullmatch(block, body):
        left = " ".join(STATE_ROW.sub(" ROW ", body).split())
        problems.append("migration llm968q_state is not rows separated by commas: %s" % left[:120])
    return rows, problems


def migration_problems(path=MIGRATION):
    """The migration must point every state at its frame in LAYOUT (the
    sheet, the frame's offset in the row, the frame size, one still frame),
    and at nothing else."""
    rows, out = migration_states(path)
    want = {}
    for name, frames in LAYOUT.items():
        fw, fh = FRAMES[name]
        for i, (asset_id, state) in enumerate(frames):
            # Every state is one still frame: frame_count 1, frame_rate 0.
            want[(asset_id, state)] = ("res://assets/trees/%s.png" % name, i * fw, 0, fw, fh, 1, 0.0)
    for key in sorted(set(want) | set(rows)):
        if rows.get(key) != want.get(key):
            out.append("migration state %s %s is %s, the layout says %s" % (key[0], key[1], rows.get(key), want.get(key)))
    return out


def leaf_pixels(frame):
    """How many of a frame's pixels are leaf green (LEAF, snapped)."""
    leaf = {B.rgb(h)[:3] for h in LEAF}
    px = frame.load()
    return sum(1 for y in range(frame.height) for x in range(frame.width)
               if px[x, y][3] == 255 and px[x, y][:3] in leaf)


def picture_problems(sheet):
    """The two pictures by state: storm is the limb in leaf, worn the boards
    (no leaves); both stand on the anchor row and spread across the door."""
    out = []
    fw, fh = FRAMES["debris-heap"]
    feet = round(FEET * fh)
    frames = {state: sheet.crop((i * fw, 0, (i + 1) * fw, fh)) for i, (_, state) in enumerate(LAYOUT["debris-heap"])}
    leaves = {state: leaf_pixels(f) for state, f in frames.items()}
    if leaves.get("storm", 0) < 40:
        out.append("debris-heap: the storm frame has %d leaf pixels, under 40 (is it the limb?)" % leaves.get("storm", 0))
    if leaves.get("worn", 0) != 0:
        out.append("debris-heap: the worn frame has %d leaf pixels (boards carry none)" % leaves["worn"])
    for state, f in frames.items():
        box = f.getbbox()
        if box is None:
            out.append("debris-heap %s: an empty frame" % state)
            continue
        # The art's lowest row (its cast shadow included) a little below
        # the feet, as the heap lies on the ground there. getbbox's bottom
        # is exclusive: the last row with art is one above it.
        bottom = box[3] - 1
        if not feet <= bottom <= feet + 8:
            out.append("debris-heap %s: art ends on row %d, not just below the feet (row %d)" % (state, bottom, feet))
        if box[2] - box[0] < fw * 0.7:
            out.append("debris-heap %s: art only %d px wide, under 70%% of the frame" % (state, box[2] - box[0]))
    return out


def check(out_dir):
    """The sheet is exactly what this script paints now, a row of exactly as
    many frames as LAYOUT names, of the size the catalog rects expect, with
    no art on a frame's outer rows or columns; the pictures tell the states
    apart; and the migration points each state at its own frame."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun debris.py)" % name)
            ok = False
            continue
        fw, fh = FRAMES[name]
        n = len(LAYOUT[name])
        if sheet.size != (fw * n, fh):
            print("%s: sheet %dx%d is not a row of %d %dx%d frames" % (name, sheet.width, sheet.height, n, fw, fh))
            ok = False
            continue
        for i in range(n):
            if any(F.edges_touched(sheet.crop((i * fw, 0, (i + 1) * fw, fh)))):
                print("%s frame %d: art on the frame edge" % (name, i))
                ok = False
        for problem in picture_problems(sheet):
            print(problem)
            ok = False
    for problem in migration_problems():
        print(problem)
        ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's debris heap.")
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
