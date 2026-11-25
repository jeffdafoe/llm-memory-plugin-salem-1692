"""Paints salem's wells (LLM-968): Well (Bucket), Well (Roofed) and the pail
that comes up their shafts. Every pixel is drawn here, no pack art.

    python tools/tree-art/wells.py
    python tools/tree-art/wells.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/wells.py --check     # margins, sizes, a fresh paint

Writes client/assets/trees/well-bucket.png and well-roofed.png (192x160: the
default look, then the storm-damaged look, 96x160 frames) and well-pail.png
(32x32), drawn at twice the pack art's size and shown at render_scale 1 (the
same size in the world, twice the detail). Anchor 0.5/0.85 as the pack art.

Both wells share one fieldstone well: a round wall with a coping of dressed
stones round the shaft, standing on an apron of flat stones. The shaft's
opening is where the pack well's was, so the pail (client/scripts/
well_bucket.gd, MOUTH and LIP, in these sheets' pixels: twice the pack's)
comes up in it and a drinker kneels at its rim.

- Well (Bucket): a windlass frame, two posts and a crossbeam, a rope roller
  with a crank, the rope down the shaft.
- Well (Roofed): the same windlass under a shingled roof on taller posts.
- Damaged (the storm's "the windlass is down", damage_minor): the frame is
  broken: a post snapped, the beam and roller down across the rim, the rope
  slack (the roof slumped to one side on the roofed well).

From photographs of New England dug wells: a fieldstone wall, a hewn
windlass, cedar shingles.
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
STONE = F.STONE
MORTAR = F.MORTAR
IRON = F.IRON
ROPE = ["4a3a22", "6e5a38", "94804e", "b8a46c"]
SHINGLE = ["241c16", "3a2e24", "54443a", "6c5a4a", "86735e", "a08c74"]
WATER = ["0e1418", "18242c", "28404c", "4a6c78"]
DARK = ["0a0a0a", "141412", "1e1c1a"]


def palette():
    return WOOD + CUT + STONE + MORTAR + IRON + ROPE + SHINGLE + WATER + DARK


# ---------------------------------------------------------------- the well

CX = 24.0                    # the shaft's middle, pack px
TOP_Y, TOP_RX, TOP_RY = 43.0, 17.0, 8.5     # the coping's outer ellipse
HOLE_Y, HOLE_RX, HOLE_RY = 43.5, 10.5, 6.2  # the shaft's opening
BASE_Y = 63.0                # the wall's foot: the same ellipse, lower
APRON_RX, APRON_RY = 21.5, 11.0


def ell(cx, cy, rx, ry, a):
    return cx + rx * math.cos(a), cy + ry * math.sin(a)


def ring_poly(cx, cy, rx0, ry0, rx1, ry1, a0, a1, n=6):
    """A segment of the ring between two ellipses, angles a0..a1."""
    out = [ell(cx, cy, rx1, ry1, a0 + (a1 - a0) * i / n) for i in range(n + 1)]
    inn = [ell(cx, cy, rx0, ry0, a1 - (a1 - a0) * i / n) for i in range(n + 1)]
    return out + inn


def scaled(pts):
    S = P.S
    return [(x * S, y * S) for x, y in pts]


def apron(d, rnd):
    """Flat stones laid round the wall's foot (the back ones are hidden by the
    wall, drawn over them)."""
    n = 18
    for i in range(n):
        a0 = 2 * math.pi * i / n + rnd.uniform(-0.04, 0.04)
        a1 = 2 * math.pi * (i + 1) / n - 0.05
        tone = 0.45 + rnd.uniform(-0.12, 0.12)
        poly = ring_poly(CX, BASE_Y + 0.5, TOP_RX - 1.0, TOP_RY - 0.5, APRON_RX - rnd.uniform(0, 1.4),
                         APRON_RY - rnd.uniform(0, 0.8), a0, a1)
        d.polygon(scaled(poly), fill=B.ramp(STONE, tone))
        # Its lit back edge (the edge toward the wall, facing up).
        inner = [ell(CX, BASE_Y + 0.5, TOP_RX - 0.6, TOP_RY - 0.2, a0 + (a1 - a0) * k / 4) for k in range(5)]
        if math.sin((a0 + a1) / 2) > 0:
            d.line(scaled(inner), fill=B.ramp(STONE, tone + 0.18), width=max(1, int(0.4 * P.S)))


def wall(lay, rnd):
    """The round wall's front face: courses of fieldstone between the
    coping's front edge and the foot, lit toward the left, the stones' rows
    following the curve."""
    S = P.S
    mask = Image.new("L", (P.W, P.H), 0)
    md = ImageDraw.Draw(mask)
    face = [ell(CX, TOP_Y, TOP_RX, TOP_RY, math.pi * i / 24) for i in range(25)]
    face += [ell(CX, BASE_Y, TOP_RX, TOP_RY, math.pi - math.pi * i / 24) for i in range(25)]
    md.polygon(scaled(face), fill=255)
    layer = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    d.polygon(scaled(face), fill=B.ramp(MORTAR, 0.2))
    rows = 7
    depth = BASE_Y - TOP_Y
    for r in range(rows):
        x = CX - TOP_RX - rnd.uniform(0, 2.0)
        while x < CX + TOP_RX:
            w = rnd.uniform(2.6, 4.4)
            x0, x1 = x + 0.15, min(CX + TOP_RX, x + w - 0.15)
            if x1 - x0 < 0.4:
                x += w
                continue
            xm = (x0 + x1) / 2
            u = max(-1.0, min(1.0, (xm - CX) / TOP_RX))
            arc = TOP_RY * math.sqrt(max(0.0, 1 - u * u))
            top = TOP_Y + arc + r * depth / rows + 0.15
            bot = top + depth / rows - 0.3
            light = 0.62 - 0.32 * (u + 1) / 2 + rnd.uniform(-0.1, 0.1)
            rad = min(0.8, (x1 - x0) / 2) * S
            d.rounded_rectangle((x0 * S, top * S, x1 * S, bot * S), radius=rad, fill=B.ramp(STONE, light))
            d.rounded_rectangle((x0 * S, top * S, x1 * S, (top + 0.6) * S), radius=rad * 0.6,
                                fill=B.ramp(STONE, light + 0.15))
            x += w
    lay.paste(layer, (0, 0), mask)


def coping(d, rnd):
    """Dressed stones round the shaft, the top of the wall: lit on their
    upper faces, a darker front lip."""
    S = P.S
    n = 14
    # The lip: the coping's front edge, a band under its outer front arc.
    lip = [ell(CX, TOP_Y, TOP_RX, TOP_RY, math.pi * i / 24) for i in range(25)]
    lip += [ell(CX, TOP_Y + 1.4, TOP_RX, TOP_RY, math.pi - math.pi * i / 24) for i in range(25)]
    d.polygon(scaled(lip), fill=B.ramp(STONE, 0.3))
    for i in range(n):
        a0 = 2 * math.pi * i / n - 0.3
        a1 = 2 * math.pi * (i + 1) / n - 0.3 - 0.06
        am = (a0 + a1) / 2
        # The light falls from the upper left; the far side's stones face us
        # and catch more of it.
        tone = 0.62 - 0.12 * math.cos(am + 0.8) - 0.08 * math.sin(am) + rnd.uniform(-0.06, 0.06)
        poly = ring_poly(CX, TOP_Y, HOLE_RX + 0.3, HOLE_RY + 0.3, TOP_RX, TOP_RY, a0, a1)
        d.polygon(scaled(poly), fill=B.ramp(STONE, tone))
        outer = [ell(CX, TOP_Y, TOP_RX - 0.4, TOP_RY - 0.3, a0 + (a1 - a0) * k / 4) for k in range(5)]
        d.line(scaled(outer), fill=B.ramp(STONE, tone + 0.15), width=max(1, int(0.35 * S)))


def shaft(d):
    """The opening: the far inner wall's top courses going down into the
    dark, a glint of water at the bottom."""
    S = P.S
    d.ellipse(((CX - HOLE_RX) * S, (HOLE_Y - HOLE_RY) * S, (CX + HOLE_RX) * S, (HOLE_Y + HOLE_RY) * S),
              fill=B.ramp(DARK, 0.2))
    # The far wall: two courses of stone seen inside the near rim, darker as
    # they go down.
    for k, tone in enumerate((0.32, 0.2)):
        y0 = HOLE_Y - HOLE_RY + 0.6 + k * 2.0
        for j in range(5):
            x0 = CX - 7.5 + j * 3.0 + (k % 2) * 1.5
            x1 = x0 + 2.6
            u = (x0 + x1) / 2 - CX
            if abs(u) > HOLE_RX - 2.0 - k * 1.5:
                continue
            d.rounded_rectangle((x0 * S, y0 * S, x1 * S, (y0 + 1.7) * S), radius=0.5 * S, fill=B.ramp(STONE, tone))
    d.ellipse(((CX - 2.6) * S, (HOLE_Y + 2.4) * S, (CX + 2.6) * S, (HOLE_Y + 3.6) * S), fill=B.ramp(WATER, 0.45))
    d.line([((CX - 1.4) * S, (HOLE_Y + 2.8) * S), ((CX + 0.4) * S, (HOLE_Y + 2.8) * S)], fill=B.ramp(WATER, 0.95),
           width=max(1, int(0.35 * S)))


def foot_shadow(sd):
    S = P.S
    # Down and to the right of the apron, kept a pixel inside the frame.
    sd.ellipse(((CX - APRON_RX + 2.5) * S, (BASE_Y + 0.5 - APRON_RY + 4.0) * S,
                (CX + APRON_RX + 1.0) * S, (BASE_Y + 0.5 + APRON_RY + 1.2) * S), fill=255)


def stone_well(lay, d, sd, seed):
    rnd = random.Random(seed)
    foot_shadow(sd)
    apron(d, rnd)
    wall(lay, rnd)
    d = ImageDraw.Draw(lay)
    coping(d, rnd)
    shaft(d)
    return d


# ---------------------------------------------------------------- windlass

def roller(d, x0, x1, y, seed):
    """The windlass roller: a round log between the posts, rope wound round
    its middle, lit along its top."""
    S = P.S
    d.rectangle((x0 * S, (y - 1.3) * S, x1 * S, (y + 1.3) * S), fill=B.ramp(WOOD, 0.45))
    d.rectangle((x0 * S, (y - 1.3) * S, x1 * S, (y - 0.6) * S), fill=B.ramp(WOOD, 0.68))
    d.rectangle((x0 * S, (y + 0.7) * S, x1 * S, (y + 1.3) * S), fill=B.ramp(WOOD, 0.28))
    for k in range(7):
        rx = CX - 4.2 + k * 1.2
        d.line([(rx * S, (y - 1.5) * S), ((rx + 0.7) * S, (y + 1.5) * S)], fill=B.ramp(ROPE, 0.7 - 0.25 * (k % 2)),
               width=max(1, int(0.55 * S)))


def crank(d, x, y):
    """An iron crank on the roller's right end, its handle down."""
    S = P.S
    d.line([(x * S, y * S), ((x + 1.8) * S, y * S)], fill=B.ramp(IRON, 0.5), width=int(0.7 * S))
    d.line([((x + 1.8) * S, y * S), ((x + 1.8) * S, (y + 4.2) * S)], fill=B.ramp(IRON, 0.45), width=int(0.7 * S))
    d.line([((x + 1.8) * S, (y + 4.2) * S), ((x + 3.0) * S, (y + 4.2) * S)], fill=B.ramp(WOOD, 0.55), width=int(0.9 * S))


def rope_down(d, x, y0, y1):
    S = P.S
    d.line([(x * S, y0 * S), (x * S, y1 * S)], fill=B.ramp(ROPE, 0.55), width=max(1, int(0.55 * S)))
    d.line([((x - 0.2) * S, y0 * S), ((x - 0.2) * S, y1 * S)], fill=B.ramp(ROPE, 0.85), width=max(1, int(0.2 * S)))


POST_L, POST_R = 6.4, 41.6


def post_shadow(sd, x, top):
    """A tall post's shadow on the ground, short and to the right (a long
    cast would run out of the frame)."""
    S = P.S
    sd.polygon([((x - 1.2) * S, BASE_Y * S), ((x + 1.2) * S, BASE_Y * S), ((x + 4.0) * S, (BASE_Y + 1.6) * S),
                ((x + 2.0) * S, (BASE_Y + 1.8) * S)], fill=255)


def windlass(lay, d, sd, top, seed, roof=False):
    """Two posts beside the wall, a crossbeam on top, the roller under it and
    the rope down the shaft."""
    for x in (POST_L, POST_R):
        post_shadow(sd, x, top)
    roll_y = top + 7.0
    rope_down(d, CX, roll_y, HOLE_Y + 3.0)
    for i, x in enumerate((POST_L, POST_R)):
        F.draw_post(d, x, top, BASE_Y - 1.0, seed + i, w=3.0)
    roller(d, POST_L + 1.5, POST_R - 1.5, roll_y, seed + 3)
    crank(d, POST_R + 1.5, roll_y)
    if not roof:
        F.draw_rail(d, POST_L - 2.2, top + 0.6, POST_R + 2.2, top + 0.6, seed + 4, 0, tone=0.55, t=2.2)
        for x in (POST_L - 2.2, POST_R + 2.2):
            d.rectangle(((x - 0.3) * P.S, (top - 0.5) * P.S, (x + 0.3) * P.S, (top + 1.7) * P.S), fill=B.ramp(CUT, 0.5))


def shingle_roof(d, rnd, x0, x1, ridge, eave, skew=0.0):
    """A gable roof, its ridge running left to right, seen from the front
    and above: the back slope's top showing over the ridge (in shade), the
    ridge board, the front slope's rows of staggered cedar shingles down to
    the eave (each row's butt edge dark, a gap between shingles), the eave's
    underside in shadow, and the gable ends' bargeboards at both sides.
    skew moves the right end up (negative) or down by that many px against
    the left, for a roof that has slumped."""
    S = P.S

    def drop(x):
        return skew * (x - x0) / (x1 - x0)

    def quad(xa, xb, ya, yb, fill):
        d.polygon([(xa * S, (ya + drop(xa)) * S), (xb * S, (ya + drop(xb)) * S),
                   (xb * S, (yb + drop(xb)) * S), (xa * S, (yb + drop(xa)) * S)], fill=fill)

    back = 4.0
    # The back slope, in shade, with its shingle rows faint.
    quad(x0 + 1.0, x1 - 1.0, ridge - back, ridge, B.ramp(SHINGLE, 0.3))
    for k in range(1, 3):
        quad(x0 + 1.0, x1 - 1.0, ridge - back + k * 1.4 - 0.2, ridge - back + k * 1.4, B.ramp(SHINGLE, 0.16))
    rows = 5
    h = (eave - ridge - 1.0) / rows
    for r in range(rows):
        y0 = ridge + 1.0 + r * h
        x = x0 - (r % 2) * 1.5
        # Lower rows a little lighter: the slope turns toward the light.
        base = 0.5 + 0.08 * r / rows
        while x < x1:
            w = rnd.uniform(2.2, 3.2)
            sx0, sx1 = max(x0, x), min(x1, x + w - 0.35)
            if sx1 - sx0 > 0.5:
                tone = base + rnd.uniform(-0.1, 0.1)
                quad(sx0, sx1, y0, y0 + h + 0.2, B.ramp(SHINGLE, tone))
                quad(sx0, sx0 + 0.5, y0, y0 + h + 0.2, B.ramp(SHINGLE, tone + 0.14))
            x += w
        # The butt edge of this row's shingles casts a dark line on the row
        # under it.
        quad(x0, x1, y0 + h - 0.1, y0 + h + 0.45, B.ramp(SHINGLE, 0.1))
    # The ridge board, lit along its top.
    quad(x0 + 0.6, x1 - 0.6, ridge - 0.6, ridge + 1.0, B.ramp(WOOD, 0.5))
    quad(x0 + 0.6, x1 - 0.6, ridge - 0.6, ridge - 0.1, B.ramp(WOOD, 0.75))
    # The eave's underside.
    quad(x0, x1, eave, eave + 1.0, B.ramp(SHINGLE, 0.08))
    # Bargeboards up the gable ends, from the eave to the ridge and back.
    for xe, xr in ((x0, x0 + 1.0), (x1, x1 - 1.0)):
        d.line([(xe * S, (eave + drop(xe)) * S), (xr * S, (ridge + drop(xr)) * S),
                (xr * S, (ridge - back + drop(xr)) * S)], fill=B.ramp(WOOD, 0.4), width=int(0.8 * S))


# ---------------------------------------------------------------- damaged frames

def broken_windlass(lay, d, sd, top, seed):
    """The storm's work: the left post snapped off above the wall, the beam
    down from the right post's top across the rim to the ground at the left,
    the roller lying on the apron, the rope slack over the coping."""
    S = P.S
    rnd = random.Random(seed)
    post_shadow(sd, POST_R, top)
    F.draw_post(d, POST_L, BASE_Y - 13.0, BASE_Y - 1.0, seed, w=3.0, broken=True)
    F.draw_post(d, POST_R, top, BASE_Y - 1.0, seed + 1, w=3.0)
    # Slack rope from the coping down into the shaft.
    d.line(scaled([(CX + 6.0, TOP_Y - 3.0), (CX + 2.0, HOLE_Y - 1.0), (CX, HOLE_Y + 3.0)]), fill=B.ramp(ROPE, 0.55),
           width=max(1, int(0.55 * S)))
    # The beam, broken from its left post, hangs from the right post's top
    # to the ground left of the well.
    F.draw_rail(d, POST_L - 1.0, BASE_Y + 2.0, POST_R + 1.5, top + 0.6, seed + 4, 0, tone=0.5, t=2.2)
    # The roller on the apron in front, the crank still on it.
    ry = BASE_Y + 6.0
    roller(d, CX - 10.0, CX + 6.0, ry, seed + 3)
    crank(d, CX + 6.0, ry)
    # Splinters by the broken post.
    for _ in range(3):
        x = POST_L + rnd.uniform(-1.0, 4.0)
        y = BASE_Y + rnd.uniform(1.0, 3.0)
        d.line(scaled([(x, y), (x + rnd.uniform(1.0, 2.0), y + rnd.uniform(-0.6, 0.6))]), fill=B.ramp(CUT, 0.5),
               width=max(1, int(0.4 * S)))


# ---------------------------------------------------------------- pieces

def new_layers():
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    return lay, ImageDraw.Draw(lay), shade, ImageDraw.Draw(shade)


def paint_bucket_well(damaged):
    P.use_frame(48, 80, density=DENSITY)
    lay, d, shade, sd = new_layers()
    top = 17.0
    d = stone_well(lay, d, sd, 501)
    if damaged:
        broken_windlass(lay, d, sd, top, 502)
    else:
        windlass(lay, d, sd, top, 502)
    return F.shrink_shaded(lay, shade, palette())


def paint_roofed_well(damaged):
    P.use_frame(48, 80, density=DENSITY)
    lay, d, shade, sd = new_layers()
    rnd = random.Random(511)
    top = 20.0
    d = stone_well(lay, d, sd, 512)
    if damaged:
        broken_windlass(lay, d, sd, top, 513)
        # The roof slumped off its broken left post: its left eave down on
        # the stub, its right still on the right post, a few shingles gone.
        shingle_roof(d, rnd, 1.5, 46.5, 38.0, 51.0, skew=-29.0)
        for _ in range(4):
            x = rnd.uniform(5, 20)
            y = 40.0 - 29.0 * (x - 1.5) / 45.0 + rnd.uniform(2.0, 8.0)
            d.rectangle(((x) * P.S, (y) * P.S, (x + 2.2) * P.S, (y + 1.2) * P.S), fill=B.ramp(DARK, 0.4))
        for _ in range(3):
            x, y = rnd.uniform(28, 44), BASE_Y + rnd.uniform(3.0, 9.0)
            d.polygon(scaled([(x, y), (x + 2.6, y - 0.4), (x + 2.8, y + 0.8), (x + 0.2, y + 1.2)]),
                      fill=B.ramp(SHINGLE, 0.55))
    else:
        windlass(lay, d, sd, top, 513, roof=True)
        shingle_roof(d, rnd, 1.5, 46.5, 7.0, 21.0)
    return F.shrink_shaded(lay, shade, palette())


def paint_pail():
    """The pail on its rope: a small oak bucket seen from above and in front,
    water inside, two iron hoops, an iron bail with the rope up from it.
    16x16 pack px; well_bucket.gd draws it whole (its region is the sheet)."""
    P.use_frame(16, 16, density=DENSITY)
    S = P.S
    lay, d, shade, sd = new_layers()
    cx = 8.0
    # The rope up out of the frame's top margin, to the bail.
    d.line([(cx * S, 0.6 * S), (cx * S, 3.4 * S)], fill=B.ramp(ROPE, 0.6), width=max(1, int(0.6 * S)))
    # The body: staves, wider at the top, lit at the left.
    t0, t1, b = 6.0, 14.6, 14.8
    d.polygon(scaled([(cx - 5.2, t0 + 1.0), (cx + 5.2, t0 + 1.0), (cx + 4.2, b), (cx - 4.2, b)]), fill=B.ramp(WOOD, 0.5))
    for k in range(6):
        x = cx - 4.4 + k * 1.75
        d.line(scaled([(x, t0 + 1.4), (x + (cx - x) * 0.18, b - 0.2)]), fill=B.ramp(WOOD, 0.3), width=max(1, int(0.25 * S)))
    d.polygon(scaled([(cx - 5.2, t0 + 1.0), (cx - 3.2, t0 + 1.0), (cx - 2.6, b), (cx - 4.2, b)]), fill=B.ramp(WOOD, 0.66))
    for y, w in ((t0 + 3.0, 5.0), (b - 1.6, 4.4)):
        d.rectangle(((cx - w) * S, y * S, (cx + w) * S, (y + 0.8) * S), fill=B.ramp(IRON, 0.4))
        d.rectangle(((cx - w) * S, y * S, (cx - w + 1.4) * S, (y + 0.8) * S), fill=B.ramp(IRON, 0.7))
    # The rim and the water in it.
    d.ellipse(((cx - 5.4) * S, (t0 - 0.6) * S, (cx + 5.4) * S, (t0 + 2.4) * S), fill=B.ramp(WOOD, 0.62))
    d.ellipse(((cx - 4.4) * S, (t0 - 0.1) * S, (cx + 4.4) * S, (t0 + 1.9) * S), fill=B.ramp(WATER, 0.6))
    d.line([((cx - 2.6) * S, (t0 + 0.5) * S), ((cx - 0.4) * S, (t0 + 0.5) * S)], fill=B.ramp(WATER, 0.95),
           width=max(1, int(0.3 * S)))
    # The bail: an iron arc from the rim's sides up to the rope.
    pts = [(cx + 5.2 * math.cos(a), t0 + 0.6 - 3.0 * math.sin(a)) for a in [math.pi * i / 12 for i in range(13)]]
    d.line(scaled(pts), fill=B.ramp(IRON, 0.55), width=max(1, int(0.5 * S)))
    return P.shrink(lay, palette(), 0.45, 0.2)


def row(frames):
    fw, fh = frames[0].size
    sheet = Image.new("RGBA", (fw * len(frames), fh), (0, 0, 0, 0))
    for i, f in enumerate(frames):
        sheet.paste(f, (i * fw, 0))
    return sheet


SHEETS = {
    "well-bucket": lambda: row([paint_bucket_well(False), paint_bucket_well(True)]),
    "well-roofed": lambda: row([paint_roofed_well(False), paint_roofed_well(True)]),
    "well-pail": lambda: row([paint_pail()]),
}
FRAMES = {"well-bucket": (96, 160), "well-roofed": (96, 160), "well-pail": (32, 32)}


def check(out_dir):
    """Each sheet is exactly what this script paints now, a row of frames of
    its size, and no frame has art on its outer rows or columns (the pail
    only its sides and bottom: its rope runs out of the top, to the well)."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun wells.py)" % name)
            ok = False
            continue
        fw, fh = FRAMES[name]
        if sheet.height != fh or sheet.width % fw:
            print("%s: sheet %dx%d is not a row of %dx%d frames" % (name, sheet.width, sheet.height, fw, fh))
            ok = False
            continue
        for i in range(sheet.width // fw):
            n, e, s, w = F.edges_touched(sheet.crop((i * fw, 0, (i + 1) * fw, fh)))
            if e or s or w or (n and name != "well-pail"):
                print("%s frame %d: art on the frame edge" % (name, i))
                ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's wells.")
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
