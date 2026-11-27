"""Paints salem's wagons (LLM-968): the Wagon, the Covered Wagon and the
Cart. Every pixel is drawn here, no pack art.

    python tools/tree-art/wagons.py
    python tools/tree-art/wagons.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/wagons.py --check     # margins, sizes, a fresh paint

Writes client/assets/trees/wagons.png (wagon, covered wagon; 192x192
frames) and cart.png (128x96), drawn at twice the pack art's size and shown
at render_scale 1 (the same size in the world, twice the detail).

Seen corner-on like the buildings: the wagon's long side runs back up-left
along the lit face, its front end faces down-right in shade, and the tongue
runs out of the front and rests on the ground. The anchor is the middle of
the box's ground, so the footprint sits round it.

THE WAGON: a plain oak box wagon, its sides two boards high between
standards, on four iron-tyred spoked wheels, the rear pair taller than the
front; the bed's floor boards show inside.

THE COVERED WAGON: the same wagon under a linen tilt stretched over four
hoops and roped to the sides, its front drawn in round a dark opening.

THE CART: a two-wheeled handcart, a small box on one axle, two handles out
of the front resting on the ground and a leg under its front.

From period farm wagons and carts of England and New England (box wagons,
tilts of linen or canvas over bows, handcarts on two wheels).
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
DENSITY = 2

WOOD = F.WOOD
CUT = F.CUT
IRON = F.IRON
# Weathered oak, grey-brown.
OAK = ["1e1612", "302218", "46321f", "5e4428", "785833", "906c40", "a8845a"]
LINEN = ["4e4636", "6a604c", "877c64", "a49880", "bfb49a", "d6ccb2", "e8e0c8"]
ROPE = ["4a3a22", "6e5a38", "94804e", "b8a46c"]
# The wheels: ash, paler and yellower than the oak box, so they stand off it.
WHEELWOOD = ["3a2e1e", "5a4830", "7e6844", "a08a5e", "bea87a", "d6c296"]
DARK = ["0e0c0a", "1a1612", "262018"]
PALETTE = OAK + WOOD + CUT + IRON + LINEN + ROPE + DARK + WHEELWOOD

# The angled view's ground axes, as the stalls and the camp: A runs from the
# near corner to the left one, BV from the near corner to the right one
# (pack px each).
A = (-1.0, -0.86)
BV = (1.0, -0.46)
AL = math.hypot(*A)
AHAT = (A[0] / AL, A[1] / AL)


def scaled(pts):
    return [(x * P.S, y * P.S) for x, y in pts]


def lerp(a, b, t):
    return (a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t)


class Box:
    """A wagon box: u runs from the front end (0) back to the rear (L)
    along A, v across from the near side (0) to the far one (Wd) along BV;
    the box's ground middle sits on the anchor."""

    def __init__(self, anchor, L, Wd):
        self.L, self.Wd = L, Wd
        self.near = (anchor[0] - (L / 2) * A[0] - (Wd / 2) * BV[0],
                     anchor[1] - (L / 2) * A[1] - (Wd / 2) * BV[1])

    def p(self, u, v, h=0.0):
        n = self.near
        return (n[0] + u * A[0] + v * BV[0], n[1] + u * A[1] + v * BV[1] - h)


def quad(d, pts, col):
    d.polygon(scaled(pts), fill=col)


def wheel(d, box, u, v, r, tone, spokes=8):
    """A spoked wheel standing in the plane of the long side: an iron tyre,
    the wooden felloes, spokes and a hub, open between the spokes."""
    S = P.S
    c = box.p(u, v, r)

    def ring(rad, n=36):
        return [(c[0] + rad * math.cos(t) * AHAT[0], c[1] + rad * math.cos(t) * AHAT[1] - rad * math.sin(t))
                for t in (2 * math.pi * k / n for k in range(n))]
    # The tyre and the felloes as rings: between the spokes the wheel is
    # open, and whatever stands behind it shows through. A dark edge round
    # the tyre keeps the wheel off the boards behind it.
    edge = ring(r + 0.15)
    d.line(scaled(edge + edge[:1]), fill=B.ramp(DARK, 0.4), width=max(1, int(0.6 * P.S)))
    tyre = ring(r - 0.45)
    d.line(scaled(tyre + tyre[:1]), fill=B.ramp(IRON, 0.3 * tone + 0.15), width=max(1, int(0.9 * P.S)))
    fel = ring(r - 1.6)
    d.line(scaled(fel + fel[:1]), fill=B.ramp(WHEELWOOD, 0.42 * tone + 0.1), width=max(1, int(1.6 * P.S)))
    inner = ring(r - 2.45)
    d.line(scaled(inner + inner[:1]), fill=B.ramp(WHEELWOOD, 0.3 * tone + 0.05), width=max(1, int(0.35 * P.S)))
    for k in range(spokes):
        t = 2 * math.pi * (k + 0.25) / spokes
        e = (c[0] + (r - 1.6) * math.cos(t) * AHAT[0], c[1] + (r - 1.6) * math.cos(t) * AHAT[1] - (r - 1.6) * math.sin(t))
        d.line(scaled([c, e]), fill=B.ramp(WHEELWOOD, 0.48 * tone + 0.16), width=max(1, int(0.95 * S)))
    hr = r * 0.22
    d.ellipse(((c[0] - hr) * S, (c[1] - hr) * S, (c[0] + hr) * S, (c[1] + hr) * S), fill=B.ramp(WHEELWOOD, 0.36 * tone + 0.16))
    d.ellipse(((c[0] - hr * 0.45) * S, (c[1] - hr * 0.45) * S, (c[0] + hr * 0.45) * S, (c[1] + hr * 0.45) * S),
              fill=B.ramp(IRON, 0.5))
    # The lit rim along the tyre's upper left.
    top = [p for p in ring(r - 0.3, 48)][12:30]
    d.line(scaled(top), fill=B.ramp(IRON, 0.8 * tone + 0.15), width=max(1, int(0.4 * S)))


def boards(d, a0, a1, b0, b1, n, tone, lines, seed=0, nails=False):
    """A boarded face from edge a0-a1 (bottom) to b0-b1 (top): each of the n
    boards its own shade with grain and a knot or two, a lit bevel along its
    top edge and a dark seam under it, then the standards (upright posts)
    at each t in lines, nailed through each board when nails."""
    rnd = random.Random(seed)
    for k in range(n):
        t0, t1 = k / n, (k + 1) / n
        bt = tone + rnd.uniform(-0.07, 0.07)
        quad(d, [lerp(a0, b0, t0), lerp(a1, b1, t0), lerp(a1, b1, t1), lerp(a0, b0, t1)], B.ramp(OAK, bt))
        for _ in range(9):
            t = t0 + (t1 - t0) * rnd.uniform(0.15, 0.85)
            s0 = rnd.uniform(0.0, 0.75)
            s1 = min(1.0, s0 + rnd.uniform(0.12, 0.4))
            row0, row1 = lerp(a0, b0, t), lerp(a1, b1, t)
            d.line(scaled([lerp(row0, row1, s0), lerp(row0, row1, s1)]),
                   fill=B.ramp(OAK, bt + rnd.choice((-0.16, -0.12, 0.1))), width=max(1, int(0.3 * P.S)))
        for _ in range(rnd.choice((0, 1, 1, 2))):
            t = t0 + (t1 - t0) * rnd.uniform(0.35, 0.65)
            q = lerp(lerp(a0, b0, t), lerp(a1, b1, t), rnd.uniform(0.1, 0.9))
            d.ellipse(((q[0] - 0.55) * P.S, (q[1] - 0.35) * P.S, (q[0] + 0.55) * P.S, (q[1] + 0.35) * P.S),
                      fill=B.ramp(OAK, bt - 0.26))
        if k > 0:
            d.line(scaled([lerp(a0, b0, t0), lerp(a1, b1, t0)]), fill=B.ramp(OAK, tone - 0.34),
                   width=max(1, int(0.45 * P.S)))
        d.line(scaled([lerp(a0, b0, t1 - 0.06), lerp(a1, b1, t1 - 0.06)]), fill=B.ramp(OAK, bt + 0.14),
               width=max(1, int(0.3 * P.S)))
    for t in lines:
        d.line(scaled([lerp(a0, a1, t), lerp(b0, b1, t)]), fill=B.ramp(OAK, tone - 0.28),
               width=max(1, int(1.3 * P.S)))
        d.line(scaled([lerp(a0, a1, t + 0.014), lerp(b0, b1, t + 0.014)]), fill=B.ramp(OAK, tone + 0.16),
               width=max(1, int(0.35 * P.S)))
        if nails:
            for k in range(n):
                q = lerp(lerp(a0, a1, t + 0.006), lerp(b0, b1, t + 0.006), (k + 0.5) / n)
                d.rectangle(((q[0] - 0.3) * P.S, (q[1] - 0.3) * P.S, (q[0] + 0.3) * P.S, (q[1] + 0.3) * P.S),
                            fill=B.ramp(IRON, 0.55))


def box_shadow(sd, box, h):
    """The cast shadow, the light from the upper left: the box's ground and
    its top pushed down-right."""
    g = [box.p(0, 0), box.p(box.L, 0), box.p(box.L, box.Wd), box.p(0, box.Wd)]
    off = (h * 0.42, h * 0.12)
    for poly in (g, [(x + off[0], y + off[1]) for x, y in g]):
        sd.polygon(scaled(poly), fill=255)
    for i in range(4):
        a, b = g[i], g[(i + 1) % 4]
        sd.polygon(scaled([a, b, (b[0] + off[0], b[1] + off[1]), (a[0] + off[0], a[1] + off[1])]), fill=255)


def paint_running_gear(d, box, bed, wheels, tongue):
    """The far wheels and the tongue, all behind the box."""
    for u, r in wheels:
        wheel(d, box, u, box.Wd + 1.2, r, 0.7)
    if tongue:
        # The tongue: out of the front, under the bed, its tip on the ground.
        root = box.p(3.0, box.Wd / 2, bed - 1.2)
        tip = box.p(-tongue, box.Wd / 2, 0.8)
        d.line(scaled([root, tip]), fill=B.ramp(OAK, 0.42), width=int(1.8 * P.S))
        d.line(scaled([(root[0], root[1] - 0.6), (tip[0], tip[1] - 0.5)]), fill=B.ramp(OAK, 0.62),
               width=max(1, int(0.45 * P.S)))
        # The iron ring at its end, for the yoke.
        d.ellipse(((tip[0] - 1.0) * P.S, (tip[1] - 1.4) * P.S, (tip[0] + 1.0) * P.S, (tip[1] + 0.2) * P.S),
                  outline=B.ramp(IRON, 0.6), width=max(1, int(0.4 * P.S)))


def paint_body(d, box, bed, side):
    """The open box: its floor inside, the far inner walls, then the two
    near outer faces and the rim."""
    L, Wd = box.L, box.Wd
    top = bed + side
    p = box.p
    # Inside: the floor boards, then the far side's and the tailboard's
    # inner faces (lit: they face the viewer).
    boards(d, p(0, 0, bed + 0.6), p(L, 0, bed + 0.6), p(0, Wd, bed + 0.6), p(L, Wd, bed + 0.6), 5, 0.4, [], 5)
    boards(d, p(0, Wd, bed + 0.6), p(L, Wd, bed + 0.6), p(0, Wd, top), p(L, Wd, top), 3, 0.5, [0.03, 0.36, 0.66, 0.97], 1)
    boards(d, p(L, 0, bed + 0.6), p(L, Wd, bed + 0.6), p(L, 0, top), p(L, Wd, top), 3, 0.42, [], 2)
    # The outer faces: the front end in shade, the near long side lit.
    boards(d, p(0, Wd, bed), p(0, 0, bed), p(0, Wd, top), p(0, 0, top), 3, 0.4, [0.5], 3, True)
    boards(d, p(0, 0, bed), p(L, 0, bed), p(0, 0, top), p(L, 0, top), 3, 0.66, [0.03, 0.36, 0.66, 0.97], 4, True)
    # The bed's edge under the side, dark.
    d.line(scaled([p(0, 0, bed), p(L, 0, bed)]), fill=B.ramp(OAK, 0.25), width=max(1, int(0.8 * P.S)))
    d.line(scaled([p(0, Wd, bed), p(0, 0, bed)]), fill=B.ramp(OAK, 0.15), width=max(1, int(0.8 * P.S)))
    # The corner posts, the near one catching the light on its left edge,
    # and the iron straps that bind the corners.
    for (u, v), tone in (((0, 0), 0.3), ((L, 0), 0.4), ((0, Wd), 0.22)):
        d.line(scaled([p(u, v, bed - 0.6), p(u, v, top + 0.5)]), fill=B.ramp(OAK, tone), width=max(1, int(1.1 * P.S)))
    d.line(scaled([p(0.4, 0, bed - 0.6), p(0.4, 0, top + 0.5)]), fill=B.ramp(OAK, 0.8), width=max(1, int(0.35 * P.S)))
    for h in (bed + 1.2, top - 1.0):
        d.line(scaled([p(4.0, 0, h), p(0, 0, h), p(0, 3.5, h)]), fill=B.ramp(IRON, 0.3), width=max(1, int(0.75 * P.S)))
        d.line(scaled([p(L - 4.0, 0, h), p(L, 0, h)]), fill=B.ramp(IRON, 0.35), width=max(1, int(0.75 * P.S)))
    # The rim: the side boards' top edges, lit.
    d.line(scaled([p(0, 0, top), p(L, 0, top), p(L, Wd, top), p(0, Wd, top), p(0, 0, top)]),
           fill=B.ramp(OAK, 0.85), width=max(1, int(0.45 * P.S)))


def paint_tilt(d, box, top, rise):
    """The linen tilt over the hoops: a half round along the box from the
    front hoop to the rear one, shaded by how each strip faces the light,
    the hoops as ribs, ropes down to the side, then the front drawn in round
    a dark opening."""
    L, Wd = box.L, box.Wd
    u0, u1 = 1.2, L - 0.6
    half = Wd / 2 + 0.6

    def at(u, phi):
        return box.p(u, Wd / 2 + half * math.cos(phi), top - 0.8 + rise * math.sin(phi))
    n = 24
    for k in range(n):
        a, b = math.pi * k / n, math.pi * (k + 1) / n
        m = (a + b) / 2
        tone = 0.5 + 0.3 * math.sin(m) - 0.16 * math.cos(m)
        quad(d, [at(u0, a), at(u1, a), at(u1, b), at(u0, b)], B.ramp(LINEN, tone))
    # The hoops showing through as ribs, a sag between them.
    for u in (u0 + 0.2, u0 + (u1 - u0) / 3, u0 + 2 * (u1 - u0) / 3, u1 - 0.3):
        d.line(scaled([at(u, math.pi * k / 20) for k in range(6, 21)]), fill=B.ramp(LINEN, 0.42),
               width=max(1, int(0.45 * P.S)))
    # Folds in the linen between the hoops, down the lit side.
    for k in range(9):
        u = u0 + (u1 - u0) * (k + 0.5) / 9
        d.line(scaled([at(u + 0.6, math.pi * 0.62), at(u - 0.4, math.pi * 0.97)]), fill=B.ramp(LINEN, 0.55),
               width=max(1, int(0.3 * P.S)))
    # The hem along the near side and its ropes down to the box.
    d.line(scaled([at(u0, math.pi), at(u1, math.pi)]), fill=B.ramp(LINEN, 0.35), width=max(1, int(0.6 * P.S)))
    for t in (0.12, 0.38, 0.62, 0.88):
        u = u0 + (u1 - u0) * t
        d.line(scaled([at(u, math.pi), box.p(u, -0.2, top - 2.2)]), fill=B.ramp(ROPE, 0.55),
               width=max(1, int(0.4 * P.S)))
    # The front: the end hoop's arch in linen, drawn in on its cord round a
    # dark opening.
    arch = [at(u0, math.pi * k / 24) for k in range(25)]
    d.polygon(scaled(arch), fill=B.ramp(LINEN, 0.46))
    c = box.p(u0, Wd / 2, top + rise * 0.3)
    hole = [(c[0] + 4.0 * math.cos(t) * BV[0] / math.hypot(*BV), c[1] + 4.0 * math.cos(t) * BV[1] / math.hypot(*BV)
             - 5.6 * math.sin(t)) for t in (2 * math.pi * k / 24 for k in range(24))]
    d.polygon(scaled(hole), fill=B.ramp(LINEN, 0.08))
    d.polygon(scaled([lerp(c, q, 0.8) for q in hole]), fill=B.ramp(DARK, 0.9))
    # The gathers: creases from the cord out to the arch.
    for k in range(2, 23, 3):
        e = arch[k]
        d.line(scaled([lerp(c, e, 0.42), lerp(c, e, 0.92)]), fill=B.ramp(LINEN, 0.3), width=max(1, int(0.3 * P.S)))
    d.line(scaled(arch), fill=B.ramp(LINEN, 0.7), width=max(1, int(0.4 * P.S)))


# ---------------------------------------------------------------- pieces

WAGON_FRAME = (96, 96)
WAGON_ANCHOR = (44.0, 58.0)     # the box's ground middle, in the 96x96 frame
WAGON_BOX = (40.0, 17.0)        # the box's length (along A) and width (along BV)
CART_FRAME = (64, 48)
CART_ANCHOR = (26.0, 27.0)


def paint_wagon(covered):
    P.use_frame(*WAGON_FRAME, density=DENSITY)
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    d, sd = ImageDraw.Draw(lay), ImageDraw.Draw(shade)
    box = Box(WAGON_ANCHOR, *WAGON_BOX)
    bed, side = 9.5, 8.5
    wheels = [(8.0, 8.0), (box.L - 8.5, 9.5)]
    box_shadow(sd, box, bed + side + (17.0 if covered else 0.0))
    paint_running_gear(d, box, bed, wheels, 14.0)
    paint_body(d, box, bed, side)
    if covered:
        paint_tilt(d, box, bed + side, 17.0)
    for u, r in wheels:
        wheel(d, box, u, -1.4, r, 1.0)
    return F.shrink_shaded(lay, shade, PALETTE)


def paint_cart():
    P.use_frame(*CART_FRAME, density=DENSITY)
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    d, sd = ImageDraw.Draw(lay), ImageDraw.Draw(shade)
    box = Box(CART_ANCHOR, 14.0, 11.0)
    bed, side = 6.5, 4.5
    box_shadow(sd, box, bed + side)
    p = box.p
    # The far wheel, the far handle, the front leg.
    wheel(d, box, box.L / 2, box.Wd + 1.0, 6.0, 0.45, 6)
    d.line(scaled([p(2.0, box.Wd - 1.2, bed + 0.4), p(-7.0, box.Wd - 1.2, 0.8)]), fill=B.ramp(OAK, 0.4),
           width=int(1.0 * P.S))
    d.line(scaled([p(1.2, box.Wd / 2, bed), p(1.2, box.Wd / 2, 0.0)]), fill=B.ramp(OAK, 0.35), width=int(0.9 * P.S))
    paint_body(d, box, bed, side)
    wheel(d, box, box.L / 2, -1.2, 6.0, 1.0, 6)
    # The near handle over everything, out of the front to the ground.
    a, b = p(2.0, 1.2, bed + 0.4), p(-7.0, 1.2, 0.8)
    d.line(scaled([a, b]), fill=B.ramp(OAK, 0.55), width=int(1.0 * P.S))
    d.line(scaled([(a[0], a[1] - 0.35), (b[0], b[1] - 0.35)]), fill=B.ramp(OAK, 0.8), width=max(1, int(0.35 * P.S)))
    return F.shrink_shaded(lay, shade, PALETTE)


# ---------------------------------------------------------------- sheets

def row(frames):
    fw, fh = frames[0].size
    sheet = Image.new("RGBA", (fw * len(frames), fh), (0, 0, 0, 0))
    for i, f in enumerate(frames):
        sheet.paste(f, (i * fw, 0))
    return sheet


SHEETS = {
    "wagons": lambda: row([paint_wagon(False), paint_wagon(True)]),
    "cart": lambda: row([paint_cart()]),
}
# Frame size and the exact number of frames each sheet holds (the migration's
# rects index into these).
FRAMES = {"wagons": (192, 192, 2), "cart": (128, 96, 1)}

MIGRATION = os.path.join(B.ROOT, "migrations", "LLM-968j-painted-wagons_up.sql")
# The wagons' footprint in the migration (left, right, top, bottom tiles round
# the anchor tile).
WAGON_FOOTPRINT = (2, 2, 1, 1)


def migration_problems(path=MIGRATION):
    """The migration's anchors and footprint must be this script's: each
    anchor as the fraction it writes, and the wagon box's ground (its four
    corners, out to the wheels) inside the footprint with the anchor at its
    tile's centre, where the migration puts the placed covered wagon."""
    text = open(path, encoding="utf-8").read()
    out = []
    for name, (ax, ay), (fw, fh) in (("wagon", WAGON_ANCHOR, WAGON_FRAME), ("cart", CART_ANCHOR, CART_FRAME)):
        frac = "%.1f::double precision / %d, %.1f::double precision / %d" % (ax, fw, ay, fh)
        if frac not in text:
            out.append("%s: the migration does not write the anchor %s" % (name, frac))
    if "2, 2, 1, 1, true)" not in text:
        out.append("wagon: the migration's footprint is not %s" % (WAGON_FOOTPRINT,))
    box = Box(WAGON_ANCHOR, *WAGON_BOX)
    ground = [box.p(u, v) for u in (0, box.L) for v in (-1.4, box.Wd + 1.2)]
    fl, fr, ft, fb = WAGON_FOOTPRINT
    for x, y in ground:
        # Pack px from the anchor, 16 to a tile; from the tile's centre the
        # footprint reaches half a tile past its outer tiles' count.
        dx, dy = (x - WAGON_ANCHOR[0]) / 16, (y - WAGON_ANCHOR[1]) / 16
        if not (-fl - 0.5 <= dx <= fr + 0.5 and -ft - 0.5 <= dy <= fb + 0.5):
            out.append("wagon: ground point (%.1f, %.1f) tiles from the anchor is outside the footprint" % (dx, dy))
    return out


def check(out_dir):
    """Each sheet is exactly what this script paints now, exactly its row of
    frames (size and count), and no frame has art on its outer rows or
    columns; and the migration's anchors and footprint fit the art."""
    ok = True
    for problem in migration_problems():
        print(problem)
        ok = False
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun wagons.py)" % name)
            ok = False
            continue
        fw, fh, n = FRAMES[name]
        if sheet.size != (fw * n, fh):
            print("%s: sheet %dx%d is not a row of %d frames of %dx%d" % (name, sheet.width, sheet.height, n, fw, fh))
            ok = False
            continue
        for i in range(n):
            if any(F.edges_touched(sheet.crop((i * fw, 0, (i + 1) * fw, fh)))):
                print("%s frame %d: art on the frame edge" % (name, i))
                ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's wagons.")
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
