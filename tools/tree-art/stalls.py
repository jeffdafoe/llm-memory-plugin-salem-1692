"""Paints salem's market stalls (LLM-968): Market Stall (Wood), (Tiled) and
(Fancy), each open and closed. Every pixel is drawn here, no pack art.

    python tools/tree-art/stalls.py
    python tools/tree-art/stalls.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/stalls.py --check     # keeper window, margins, sizes, a fresh paint

Writes client/assets/trees/stall-wood.png, stall-tiled.png and
stall-fancy.png: open, then closed, 128x224 frames (64x112 pack px drawn at
density 2, shown at render_scale 1).

An open market booth, seen corner-on like the buildings: a counter on its
two front sides, four posts, a gable roof with its ridge along the long
front-left counter, and no wall behind the keeper. The client draws a
see-through structure's keeper (visible_when_inside) on the stand tile
BEFORE the stall (y-sort: the stand tile is the anchor tile, the stall's
own row), so the keeper shows only through the art's empty pixels: the open
booth leaves the space between the counters and the roof empty, and the
keeper shows from the waist up behind the counter. Closed (no keeper: the
stall shows closed when nobody is in it), shutters close both fronts from
the counter to the eaves.

The art fits the tiles set by migration LLM-968h: the object snapped to its
anchor tile's centre, which is the stand tile (anchor 0.5 / 152/224: the
keeper's feet); the footprint 3 wide and 2 deep (left 1, right 1, top 1);
the door at the back centre (0, -1), the keeper coming in from behind; the
customers in front of the long counter (loiter (-1, 1)). The back corner
post stands left of the keeper's column so it never crosses them.

Period New England (1692): Wood is weathered grey-brown oak under a roof of
riven boards, Tiled brown oak under cedar shingles (a Salem roof's
"tiles"), Fancy dressed pine under newer shingles with its counter fronts in
Spanish brown (red ochre) and its gable lime-washed. The goods on the
counter (a basket of apples, a crock, a sack) are plain, any trade's.
"""

import argparse
import math
import os
import random
import sys

from PIL import Image, ImageDraw

import build as B
import plants as P
import fences as F

OUT_DIR = B.OUT_DIR

FW, FH = 64, 112
# The keeper's feet: the stand tile's centre, which is the object's position.
ANCHOR = (0.5, 76.0 / 112.0)

# The booth on the ground: near, left, right and far corners (pack px).
A = (-1.0, -0.86)   # along the front-left face, from near to left
BV = (1.0, -0.46)   # along the front-right face, from near to right
N = (39.0, 87.0)
UW, VD = 27.0, 12.0
H = 38.0            # eave height
PEAK = 15.0         # roof rise above the eaves
OVER = 2.6          # eave overhang
COUNTER = 11.0


def gp(u, v, h=0.0):
    """Ground point at u along the front-left face from the LEFT corner (0)
    to the near one (UW), v along the front-right face from the front (0)
    back (VD), lifted h."""
    du = UW - u
    return (N[0] + du * A[0] + v * BV[0], N[1] + du * A[1] + v * BV[1] - h)


def sc(pts):
    return [(x * P.S, y * P.S) for x, y in pts]


# Period New England (1692): unpainted oak and pine, riven boards or cedar
# shingles on the roofs; the finer stall's counter front in "Spanish brown"
# (red ochre) and its gable lime-washed.
WOODS = {
    "wood": ["2a2620", "3e3830", "565046", "706858", "8a8270", "a49a86"],     # weathered grey-brown oak
    "tiled": ["2a1e14", "3e2c1c", "584026", "725432", "8c6a40", "a68452"],    # brown oak
    "fancy": ["2e2216", "48341f", "644a2c", "82623a", "9e7c4c", "b89662"],    # dressed pine
}
ROOFS = {
    "wood": ["262420", "3a3630", "504b44", "68625a", "827b72", "9c958a"],     # riven boards, silvered
    "tiled": ["2e2014", "46301c", "604428", "7c5a36", "967246", "ae8c5c"],    # cedar shingles
    "fancy": ["2c1e14", "44301e", "5e4428", "7a5a34", "947044", "ac8856"],    # cedar shingles, newer
}
SPANISH = ["3a1610", "5a2216", "7a3020", "94402a", "aa5236"]
LIME = ["8e887c", "b0aa9c", "ccc6b8", "e6e0d2"]
CREAM = ["8a7c60", "b4a684", "d8cca8", "eee4c4"]
GOODS = ["3a2a18", "6a4a22", "9a7030", "c49a48", "7a2a1a", "a84a2a", "4a6a28", "6a8a34"]


def palette(kind):
    return WOODS[kind] + ROOFS[kind] + SPANISH + LIME + CREAM + GOODS + F.WOOD


def post(d, u, v, wood, tone):
    x0, y0 = gp(u, v)
    x1, y1 = gp(u, v, H)
    w = 1.4
    d.rectangle(((x0 - w) * P.S, y1 * P.S, (x0 + w) * P.S, y0 * P.S), fill=B.ramp(wood, tone))
    d.rectangle(((x0 - w) * P.S, y1 * P.S, (x0 - w * 0.3) * P.S, y0 * P.S), fill=B.ramp(wood, tone + 0.18))


def boards(d, a, b, h0, h1, wood, tone, n, rnd, horizontal=False):
    """Boards on a face from ground points a to b, heights h0..h1."""
    def at(t, h):
        return (a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t - h)
    d.polygon(sc([at(0, h0), at(1, h0), at(1, h1), at(0, h1)]), fill=B.ramp(wood, tone - 0.25))
    if horizontal:
        for k in range(n):
            ha, hb = h0 + (h1 - h0) * k / n + 0.15, h0 + (h1 - h0) * (k + 1) / n - 0.15
            t = tone + rnd.uniform(-0.06, 0.06)
            d.polygon(sc([at(0, ha), at(1, ha), at(1, hb), at(0, hb)]), fill=B.ramp(wood, t))
            d.line(sc([at(0, hb), at(1, hb)]), fill=B.ramp(wood, t + 0.15), width=max(1, int(0.3 * P.S)))
    else:
        for k in range(n):
            ta, tb = (k + 0.08) / n, (k + 0.92) / n
            t = tone + rnd.uniform(-0.06, 0.06)
            d.polygon(sc([at(ta, h0), at(tb, h0), at(tb, h1), at(ta, h1)]), fill=B.ramp(wood, t))


def counter_top(d, face, wood):
    """The counter's top board along one front face, a little proud of it."""
    if face == "left":
        a, b = gp(0, 0, COUNTER), gp(UW, 0, COUNTER)
        out = (BV[0] * -1.6, BV[1] * -1.6)
        inn = (BV[0] * 2.6, BV[1] * 2.6)
    else:
        a, b = gp(UW, 0, COUNTER), gp(UW, VD, COUNTER)
        out = (-A[0] * 1.6, -A[1] * 1.6)
        inn = (A[0] * 2.6, A[1] * 2.6)
    pts = [(a[0] + out[0], a[1] + out[1]), (b[0] + out[0], b[1] + out[1]),
           (b[0] + inn[0], b[1] + inn[1]), (a[0] + inn[0], a[1] + inn[1])]
    d.polygon(sc(pts), fill=B.ramp(wood, 0.72))
    d.line(sc([pts[0], pts[1]]), fill=B.ramp(wood, 0.45), width=max(1, int(0.6 * P.S)))


def goods(d, rnd):
    """A few things on the counter at its ends, clear of the keeper: a
    basket of apples, a crock, a sack."""
    for u, v, kind in ((4.0, 0.6, "basket"), (UW - 0.4, VD - 3.0, "sack"), (9.0, 0.5, "crock")):
        x, y = gp(u, v, COUNTER)
        if kind == "basket":
            d.ellipse(((x - 3) * P.S, (y - 2.6) * P.S, (x + 3) * P.S, (y + 0.4) * P.S), fill=B.ramp(GOODS[0:4], 0.6))
            for k in range(6):
                ax, ay = x - 2 + (k % 3) * 2, y - 2.4 - (k // 3) * 1.2
                d.ellipse(((ax - 0.9) * P.S, (ay - 0.9) * P.S, (ax + 0.9) * P.S, (ay + 0.9) * P.S), fill=B.ramp(GOODS[4:6], 0.5 + 0.4 * (k % 2)))
        elif kind == "sack":
            d.polygon(sc([(x - 2.4, y), (x + 2.4, y), (x + 1.8, y - 5.0), (x, y - 6.0), (x - 1.8, y - 5.0)]), fill=B.ramp(CREAM, 0.45))
            d.line(sc([(x - 1.2, y - 5.0), (x + 1.2, y - 5.0)]), fill=B.ramp(GOODS[0:4], 0.3), width=max(1, int(0.4 * P.S)))
        else:
            d.rectangle(((x - 1.6) * P.S, (y - 4.2) * P.S, (x + 1.6) * P.S, y * P.S), fill=B.ramp(GOODS[0:4], 0.7))
            d.rectangle(((x - 1.6) * P.S, (y - 4.2) * P.S, (x - 0.6) * P.S, y * P.S), fill=B.ramp(GOODS[0:4], 0.95))


def roof(d, kind, rnd):
    """A gable roof, its ridge along the front-left face: the back slope
    first (mostly hidden), the gable on the front-right face (boards, or
    lime-washed with its timbers showing on the fancy stall), then the front
    slope: riven boards running down it, battened (wood), or rows of cedar
    shingles (tiled, fancy); a ridge board and a bargeboard up the gable."""
    pal = ROOFS[kind]
    wood = WOODS[kind]
    eF0, eF1 = gp(-OVER, -OVER, H), gp(UW + OVER, -OVER, H)          # front eave, far .. near
    r0, r1 = gp(-OVER, VD / 2, H + PEAK), gp(UW + OVER, VD / 2, H + PEAK)
    eB0, eB1 = gp(-OVER, VD + OVER, H), gp(UW + OVER, VD + OVER, H)
    d.polygon(sc([r0, r1, eB1, eB0]), fill=B.ramp(pal, 0.22))
    # The gable.
    g = [gp(UW, 0, H), gp(UW, VD / 2, H + PEAK), gp(UW, VD, H)]
    if kind == "fancy":
        d.polygon(sc(g), fill=B.ramp(LIME, 0.6))
        mid = gp(UW, VD / 2, H)
        d.line(sc([mid, g[1]]), fill=B.ramp(wood, 0.3), width=max(1, int(0.7 * P.S)))
        d.line(sc([g[0], g[2]]), fill=B.ramp(wood, 0.3), width=max(1, int(0.8 * P.S)))
    else:
        d.polygon(sc(g), fill=B.ramp(wood, 0.42))
        for k in range(1, 5):
            t = k / 5
            a = (g[0][0] + (g[2][0] - g[0][0]) * t, g[0][1] + (g[2][1] - g[0][1]) * t)
            d.line(sc([a, (a[0], a[1] - (PEAK * (1 - abs(2 * t - 1))))]), fill=B.ramp(wood, 0.18),
                   width=max(1, int(0.3 * P.S)))

    def at(s_, t):
        """A point on the front slope: s_ 0..1 from the far end to the near
        end, t 0..1 from the eave up to the ridge."""
        e = (eF0[0] + (eF1[0] - eF0[0]) * s_, eF0[1] + (eF1[1] - eF0[1]) * s_)
        r = (r0[0] + (r1[0] - r0[0]) * s_, r0[1] + (r1[1] - r0[1]) * s_)
        return (e[0] + (r[0] - e[0]) * t, e[1] + (r[1] - e[1]) * t)

    d.polygon(sc([at(0, 0), at(1, 0), at(1, 1), at(0, 1)]), fill=B.ramp(pal, 0.2))
    if kind == "wood":
        n = 9
        for k in range(n):
            s0, s1 = (k + 0.06) / n, (k + 0.94) / n
            tone = 0.6 + rnd.uniform(-0.08, 0.08)
            d.polygon(sc([at(s0, 0), at(s1, 0), at(s1, 1), at(s0, 1)]), fill=B.ramp(pal, tone))
            d.line(sc([at(s0, 0), at(s0, 1)]), fill=B.ramp(pal, tone + 0.14), width=max(1, int(0.3 * P.S)))
        for t in (0.3, 0.75):
            d.line(sc([at(0, t), at(1, t)]), fill=B.ramp(pal, 0.4), width=max(1, int(0.6 * P.S)))
    else:
        rows = 6
        for r in range(rows):
            t0, t1 = r / rows, (r + 1) / rows
            n = 11
            for k in range(n + 1):
                s0 = (k - 0.5 * (r % 2)) / n
                s1 = s0 + 1.0 / n - 0.012
                s0, s1 = max(0.0, s0), min(1.0, s1)
                if s1 - s0 < 0.01:
                    continue
                tone = 0.6 + 0.05 * (rows - r) / rows + rnd.uniform(-0.1, 0.1)
                d.polygon(sc([at(s0, t0), at(s1, t0), at(s1, t1 + 0.03), at(s0, t1 + 0.03)]), fill=B.ramp(pal, tone))
            # Each row's butt edge shades the row under it.
            d.line(sc([at(0, t0 + 0.02), at(1, t0 + 0.02)]), fill=B.ramp(pal, 0.15), width=max(1, int(0.4 * P.S)))
    # The ridge board, the eave's shadow line, the bargeboard up the gable.
    d.line(sc([r0, r1]), fill=B.ramp(wood, 0.55), width=int(0.9 * P.S))
    d.line(sc([eF0, eF1]), fill=B.ramp(pal, 0.1), width=int(0.8 * P.S))
    d.line(sc([eF1, r1, eB1]), fill=B.ramp(wood, 0.45), width=int(0.8 * P.S))


def paint(kind, closed, seed):
    P.use_frame(FW, FH, density=2)
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    shade = Image.new("L", (P.W, P.H), 0)
    sd = ImageDraw.Draw(shade)
    rnd = random.Random(seed)
    wood = WOODS[kind]
    g = [gp(0, 0), gp(UW, 0), gp(UW, VD), gp(0, VD)]
    sd.polygon(sc([(x + 4.0, y + 1.6) for x, y in g]), fill=255)
    # The back posts, then the front counters (solid boards up to the
    # counter, the keeper's legs behind them), the front posts, the roof.
    post(d, 0, VD, wood, 0.35)
    post(d, UW, VD, wood, 0.4)
    post(d, 0, 0, wood, 0.55)
    front = SPANISH if kind == "fancy" else wood
    boards(d, gp(0, 0), gp(UW, 0), 0, COUNTER, front, 0.6, 9, rnd)
    boards(d, gp(UW, 0), gp(UW, VD), 0, COUNTER, front, 0.36, 5, rnd)
    if closed:
        # Shutters let down over both counters, up to the eaves.
        boards(d, gp(0, 0), gp(UW, 0), COUNTER, H, wood, 0.56, 6, rnd, horizontal=True)
        boards(d, gp(UW, 0), gp(UW, VD), COUNTER, H, wood, 0.33, 6, rnd, horizontal=True)
    counter_top(d, "left", wood)
    counter_top(d, "right", wood)
    if not closed:
        goods(d, rnd)
    post(d, UW, 0, wood, 0.58)
    roof(d, kind, rnd)
    return F.shrink_shaded(lay, shade, palette(kind))


# ---------------------------------------------------------------- sheets

KINDS = {"wood": 701, "tiled": 711, "fancy": 721}


def build_sheet(kind):
    frames = [paint(kind, False, KINDS[kind]), paint(kind, True, KINDS[kind])]
    fw, fh = frames[0].size
    sheet = Image.new("RGBA", (fw * 2, fh), (0, 0, 0, 0))
    for i, f in enumerate(frames):
        sheet.paste(f, (i * fw, 0))
    return sheet


SHEETS = {"stall-" + k: (lambda k=k: build_sheet(k)) for k in KINDS}

# The keeper's window, in pack px of the frame: where the keeper's head and
# shoulders stand when they are on the stand tile (feet at the anchor
# (32, 76); a villager is about 28 pack px tall). It must be empty in an
# open frame (the keeper shows there) and covered in a closed one (the
# shutters are up).
KEEPER_WINDOW = (28, 50, 36, 60)


def window_alpha(frame):
    x0, y0, x1, y1 = (v * 2 for v in KEEPER_WINDOW)
    px = frame.load()
    return [px[x, y][3] for y in range(y0, y1) for x in range(x0, x1)]


def check(out_dir):
    """Each sheet is exactly what this script paints now, two frames of
    128x224; no frame has art on its outer rows or columns; the keeper's
    window is empty in the open frame and fully covered in the closed one."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun stalls.py)" % name)
            ok = False
            continue
        if sheet.size != (FW * 2 * 2, FH * 2):
            print("%s: sheet %dx%d is not two %dx%d frames" % (name, sheet.width, sheet.height, FW * 2, FH * 2))
            ok = False
            continue
        for i, label in enumerate(("open", "closed")):
            frame = sheet.crop((i * FW * 2, 0, (i + 1) * FW * 2, FH * 2))
            if any(F.edges_touched(frame)):
                print("%s %s: art on the frame edge" % (name, label))
                ok = False
            alpha = window_alpha(frame)
            if label == "open" and any(alpha):
                print("%s open: %d pixels of art in the keeper's window" % (name, sum(1 for a in alpha if a)))
                ok = False
            if label == "closed" and not all(a == 255 for a in alpha):
                print("%s closed: the shutters leave the keeper's window partly open" % name)
                ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's market stalls.")
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
