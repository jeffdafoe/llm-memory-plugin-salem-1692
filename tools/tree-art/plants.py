"""Paints salem's ground cover (LLM-967): grasses, a fern, clover, mushrooms
and the small things lying on the forest floor. Every pixel is drawn here, no
pack art.

    python tools/tree-art/plants.py
    python tools/tree-art/plants.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/plants.py --check     # every frame inside its frame, sheets their size

Writes client/assets/trees/<piece>.png. Each sheet has one row per look, top
to bottom: summer, autumn, winter, winter-snowy; a row is the look's frames,
left to right, at the piece's frame size: 32x32, and 32x64 for Tall, Medium,
Thick and Low Grass. That is twice the pack art's frame, drawn at
render_scale 1 instead of 2 (density 2, see Piece): the same size in the
world, with twice the detail. The Piece table gives frames in pack-art
pixels (16x16, 16x32). The grasses and the fern rustle: a build.FRAMES-long loop at build.FPS with the
trees' light-wind puff at its end. Everything else lies still and has one
frame. Rerunning writes the same files. After a change, run
`godot_console --headless --path client --import` so Godot writes the
.import sidecars, and commit them.

It imports build.py (the trees) for its scale, colour helpers, puff and
shadow, so the ground cover is painted, shrunk and snapped the way the trees
are: at build.S times size, area-averaged down, every pixel snapped to the
look's own colours.

Each piece is one kind of plant all year (the pack art swapped species by
season); the season changes its colour, from photographs of New England
woods and fields:
- Grasses: clumps of tapering blades, darker at the root and lit along one
  edge. Green in summer, straw with some green left in autumn, pale
  grey-tan in winter, snow on some bent tips in the snowy look. Tall Grass
  carries timothy heads. In the puff the upper half of a blade leans one
  frame pixel on its own threshold, as the reeds do.
- Fern: fronds of paired leaflets arching out of a crown. Green in summer,
  rust and gold in autumn (as bracken turns), brown and limp in winter.
- Clover: a low patch of three-leaflet leaves with white flower heads in
  summer.
- Mushrooms: brown boletes, grey bell caps, golden chanterelles, white
  puffballs and a bracket fungus on a bit of rotten wood. Winter dulls and
  darkens them; snow caps them in the snowy look.
- Pine cone, a fallen leaf, a twig, a small branch and maple keys (Seeds)
  lying on the ground. Autumn turns the leaf, keys and branch leaves; snow
  lies over them in the snowy look.
"""

import argparse
import math
import os
import random
import sys

from PIL import Image, ImageDraw

import build as B

S = B.S
OUT_DIR = B.OUT_DIR
LOOKS = ["summer", "autumn", "winter", "winter-snowy"]
ANCHOR_Y = 0.85

# ---------------------------------------------------------------- colours

GRASS_GREEN = ["162a12", "22401a", "325a22", "44762a", "5a9232", "78ae40", "9cc85a"]
GRASS_OLIVE = ["262c12", "3a441a", "525e22", "6c782c", "889438", "a6b04a", "c4c866"]
GRASS_STRAW = ["3a2c14", "5a461e", "7c6228", "a08036", "bc9c48", "d4b862", "e6d088"]
GRASS_WINTER = ["3a3020", "5a4c34", "7c6c4c", "9c8c66", "b8aa82", "d0c4a0", "e4dcc0"]
TIMOTHY = ["2c3018", "48502a", "6a7440", "8c9658", "aab676"]

FERN_GREEN = ["14301a", "1e4824", "2a622c", "3a7e34", "50983e", "6cb24e", "94cc6a"]
FERN_RUST = ["3e1c0c", "602c12", "844018", "a85a22", "c47a32", "d89a4a", "e8ba6a"]
FERN_GOLD = ["3e300e", "604a14", "84681c", "a88828", "c4a63a", "d8c058", "e8d67e"]
FERN_DEAD = ["2a1c14", "402c1e", "583e2a", "705236", "886644", "a07e58", "b89870"]

CLOVER = ["142c14", "1e421c", "2c5c26", "3c7830", "52943a", "70ae4c", "98c86a"]
CLOVER_TURN = ["2c2c12", "44441a", "5e5e22", "7a782c", "969438", "b0ac4c", "cac46a"]
CLOVER_WINTER = ["1e2618", "2c3822", "3e4c2e", "52623a", "667848", "7e8e5c", "989e74"]
CLOVER_FLOWER = ["a89c90", "d4cec6", "f2eee8", "fffefa"]
CLOVER_PINK = ["c890a4", "e2b4c4"]

BOLETE = ["2e160c", "4a2814", "6a3c1e", "8a5428", "a86e36", "c48c4c", "dcac6c"]
STEM = ["5e5444", "847a64", "aaa086", "cac2a8", "e2dcc6", "f2eedf"]
GREY_CAP = ["24221f", "3a3733", "54504a", "6e6a62", "8a857c", "a6a196", "c2beb2"]
CHANTERELLE = ["5a3a0a", "86560e", "b07a16", "d29e24", "eac03c", "f6da68", "fcec9a"]
PUFFBALL = ["6c665a", "948d7e", "bab4a4", "d8d4c6", "eeebe0", "fbfaf4"]
BRACKET = ["24160e", "3e2a1c", "5e4630", "84684c", "a88c6c", "cab496", "ece0c4"]
BRACKET_RUST = ["3a1a0c", "5e2a12", "843e18", "a85622", "c47432"]
ROTWOOD = ["221a14", "3a2c22", "54412f", "6e5640", "886e54"]

CONE = ["1e140c", "3a2414", "5a381e", "7a4e2a", "9a6a3a", "b88a52"]
TWIG = ["1e1610", "34261a", "4e3a28", "6a5038", "86694a", "a2865e"]

LEAF_GREEN = ["1e3414", "2e4e1c", "426c26", "588a30", "72a63c", "92c052"]
LEAF_RED = ["4a120c", "7a2210", "aa3a14", "d45a1c", "ec842c", "f6ac48"]
LEAF_GOLD = ["4a300c", "7a5210", "aa7a16", "d4a424", "eec840", "f8e270"]
LEAF_BROWN = ["2a1a10", "443020", "604630", "7a5c40", "947454", "ae8e6c"]
KEY_TAN = ["4a3418", "6a4e24", "8c6a34", "ae8a4a", "c8a866", "dec488"]
ACORN_GREEN = ["2e3412", "4a561c", "687a26", "889a34", "a6b64a", "c6d06c"]
ACORN_BROWN = ["2e1a0c", "4e2e14", "70461e", "92602a", "b07e3c", "cc9e58"]
ACORN_DARK = ["24180e", "3a2818", "543c24", "6e5232", "886a44", "a2845a"]
ACORN_CAP = ["2a2218", "443828", "62523a", "80704e", "9c8c66"]

DULL = (52, 46, 40)          # what winter dulls mushrooms and the forest floor's bits toward


def ramp_dull(cols, t):
    """A winter ramp: the summer colours pulled 35% toward a dull grey-brown."""
    return [("%02x%02x%02x" % tuple(int(c + (d - c) * t) for c, d in zip(B.rgb(h)[:3], DULL))) for h in cols]


# ---------------------------------------------------------------- pieces

class Piece:
    def __init__(self, **kw):
        self.animated = False
        self.outline = 0.4          # how far the down-facing edge is mixed toward the outline
        self.ring = 0.0             # and the other edges (a plant that must stand off the grass)
        # 2 (all of them, Jeff 2026-11-25): a frame twice the pack art's size,
        # shown at render_scale 1 rather than 2, so the same size in the world
        # with twice the detail, as dense as the ground's 32 px tiles. At 16 px
        # the fern and the bracket fungus shrank to a mound and a brick stack.
        self.density = 2
        self.__dict__.update(kw)


PIECES = {
    # Grasses, 16x32. blades: count, length range (frame px), the spread of
    # their roots, how far they fan and bend, their width; heads: timothy.
    "tall-grass": Piece(frame=(16, 32), painter="grass", animated=True, seed=7, outline=0.25,
                        grass=dict(blades=14, blade_len=(13.0, 23.0), spread=8.0, fan=0.6, bend=2.0,
                                   blade_w=1.4, heads=3, head_len=(23.0, 26.0))),
    "medium-grass": Piece(frame=(16, 32), painter="grass", animated=True, seed=13, outline=0.25,
                          grass=dict(blades=13, blade_len=(6.0, 13.0), spread=7.5, fan=0.75, bend=2.0,
                                     blade_w=1.3)),
    "thick-grass": Piece(frame=(16, 32), painter="grass", animated=True, seed=29, outline=0.25,
                         grass=dict(blades=22, blade_len=(9.0, 17.0), spread=9.0, fan=0.8, bend=2.2,
                                    blade_w=1.5)),
    # Low Grass: short tufts scattered over the ground, not one clump.
    "low-grass": Piece(frame=(16, 32), painter="grass", animated=True, seed=41, outline=0.2,
                       grass=dict(blades=6, blade_len=(3.0, 6.0), spread=2.8, fan=1.1, bend=1.2,
                                  blade_w=1.1, tufts=[(-3.0, -7.0), (3.2, -4.6), (-3.4, 0.2), (2.8, 0.8)])),
    "light-grass": Piece(frame=(16, 16), painter="grass", animated=True, seed=53, outline=0.2, light=True,
                         grass=dict(blades=11, blade_len=(3.5, 8.0), spread=5.5, fan=0.9, bend=1.5,
                                    blade_w=1.0)),
    "fern": Piece(frame=(16, 16), painter="fern", animated=True, seed=11, outline=0.4, ring=0.25),
    "clover": Piece(frame=(16, 16), painter="clover", seed=5),
    "brown-mushroom": Piece(frame=(16, 16), painter="mushroom", seed=3, kind="bolete"),
    "grey-mushroom": Piece(frame=(16, 16), painter="mushroom", seed=8, kind="grey"),
    "yellow-mushroom": Piece(frame=(16, 16), painter="mushroom", seed=21, kind="chanterelle"),
    "white-mushroom": Piece(frame=(16, 16), painter="mushroom", seed=34, kind="puffball"),
    "layered-mushroom": Piece(frame=(16, 16), painter="mushroom", seed=55, kind="bracket"),
    "pine-cone": Piece(frame=(16, 16), painter="cone", seed=2),
    "leaf": Piece(frame=(16, 16), painter="leaf", seed=4),
    "twig": Piece(frame=(16, 16), painter="twig", seed=6),
    "small-branch": Piece(frame=(16, 16), painter="branch", seed=9),
    "seeds": Piece(frame=(16, 16), painter="keys", seed=12),
}


def use_frame(fw, fh, density=1):
    """This module's frame globals for one piece. The build.py helpers used
    here (rgb, mix, ramp, nearest, _hash) read no frame, so build's own
    globals are left alone."""
    global S, DENSITY, FW, FH, W, H, BASE_Y
    # density 2 draws the piece on a frame twice the size, shown at
    # render_scale 1 instead of 2: the same size in the world, twice the
    # detail. The painters work in units of the density-1 frame pixel (S
    # canvas px), so they draw the same shapes either way.
    DENSITY = density
    S = B.S * density
    FW, FH = fw * density, fh * density
    W, H = fw * S, fh * S
    BASE_Y = H * ANCHOR_Y


def lean_of(seed, i, gust, base):
    """How far (canvas px) blade i's top leans on this puff frame: one frame
    pixel once the puff passes its own hashed threshold, else none."""
    if gust is None:
        return 0.0
    threshold = base + 0.6 * (B._hash(seed, i, 5) & 0xFFFF) / 65535.0
    return S / DENSITY if gust[2] >= threshold else 0.0


def frac(seed, *k):
    return (B._hash(seed, *k) & 0xFFFF) / 65535.0


def snow_blob(d, x, y, rx, ry):
    d.ellipse((x - rx, y - ry, x + rx, y + ry), fill=B.rgb(B.SNOW[1]))
    d.ellipse((x - rx * 0.8, y - ry, x + rx * 0.5, y + ry * 0.3), fill=B.rgb(B.SNOW[2]))


# ---------------------------------------------------------------- grasses

def grass_ramps(look, light):
    if look == "summer":
        return [GRASS_OLIVE, GRASS_GREEN] if light else [GRASS_GREEN, GRASS_OLIVE]
    if look == "autumn":
        return [GRASS_STRAW, GRASS_STRAW, GRASS_OLIVE]
    return [GRASS_WINTER]


def draw_blade(d, x0, y0, a, L, bend, w0, cols, v, lean, ytop, lit):
    """One tapering blade from (x0, y0) along angle a, its tip bent sideways by
    bend. Drawn in short segments that run from dark at the root to its own
    tone v by a third of the way up, so the clump's foot is shaded and every
    blade above it keeps its tone. A front blade (lit) gets a light edge on
    its upper half. Returns its tip."""
    spine = []
    steps = 12
    for k in range(steps + 1):
        t = k / steps
        x = x0 + math.cos(a) * L * t + bend * t * t + (lean if t > 0.5 else 0.0) * (t - 0.5) * 2
        y = max(ytop, y0 + math.sin(a) * L * t + abs(bend) * 0.3 * t * t)
        spine.append((x, y))
    for k in range(steps):
        t = (k + 0.5) / steps
        w = w0 * S * (1 - t * 0.75)
        tone = v - 0.3 * max(0.0, 1 - t / 0.35)
        (xa, ya), (xb, yb) = spine[k], spine[k + 1]
        d.polygon([(xa - w / 2, ya), (xa + w / 2, ya), (xb + w / 2 * 0.92, yb), (xb - w / 2 * 0.92, yb)], fill=B.ramp(cols, tone))
    if lit:
        d.line(spine[5:11], fill=B.ramp(cols, v + 0.28), width=max(1, int(0.3 * S)))
    return spine[-1]


def paint_grass(piece, seed, look, gust):
    g = piece.grass
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    ramps = grass_ramps(look, getattr(piece, "light", False))
    rnd = random.Random(seed)
    clumps = g.get("tufts") or [(0.0, 0.0)]
    blades = []
    n = 0
    margin = 1.6 * S                          # tips stay this far inside the frame's sides
    for ci, (cdx, cdy) in enumerate(clumps):
        count = g["blades"] - (ci % 2) if g.get("tufts") else g["blades"]
        for i in range(count):
            off = (i + 0.5) / count * 2 - 1 + rnd.uniform(-0.06, 0.06)
            # Depth alternates back and front along the row, so neighbours
            # differ in tone and each blade reads on its own: the back ones
            # dark and a little taller, their roots a little higher.
            back = (i + rnd.randint(0, 1)) % 2 == 0
            z = rnd.uniform(0.55, 1.0) if back else rnd.uniform(0.0, 0.45)
            # a vase: roots close together, tips spreading
            a = -math.pi / 2 + off * g["fan"] * 0.5 + rnd.uniform(-0.08, 0.08)
            L = rnd.uniform(*g["blade_len"]) * S * (1 - 0.3 * abs(off)) * (0.9 + 0.12 * z)
            x0 = W * 0.5 + cdx * S + off * g["spread"] * 0.5 * S + rnd.uniform(-0.25, 0.25) * S
            # The roots lie on the near half of an ellipse: front and middle
            # lowest, the sides and back higher, so the foot is round and
            # ragged rather than a straight cut.
            y0 = BASE_Y + cdy * S - z * 1.4 * S - off * off * g["spread"] * 0.18 * S + rnd.uniform(-0.3, 0.6) * S
            bend = (off * g["bend"] + rnd.uniform(-0.6, 0.6)) * S
            # Keep the whole blade, leaning in the puff, inside the frame: a
            # blade angled out and bent back bulges past its tip, so check
            # the curve and stand it up until it fits.
            for _ in range(40):
                xs = [x0 + math.cos(a) * L * t + bend * t * t + S * max(0.0, t - 0.5) * 2 * sgn
                      for t in (k / 10 for k in range(11)) for sgn in (-1, 1)]
                if min(xs) >= margin and max(xs) <= W - margin:
                    break
                a += 0.04 if math.cos(a) < 0 else -0.04
                bend *= 0.85
            v = (0.3 + rnd.uniform(-0.04, 0.06)) if back else (0.6 + rnd.uniform(-0.06, 0.08))
            cols = ramps[int(frac(seed, n, 9) * len(ramps))]
            blades.append((z, x0, y0, a, L, bend, cols, v, n, not back))
            n += 1
    blades.sort(key=lambda b: -b[0])          # back rows first
    tips = []
    for z, x0, y0, a, L, bend, cols, v, i, lit in blades:
        tip = draw_blade(d, x0, y0, a, L, bend, g["blade_w"], cols, v, lean_of(seed, i, gust, 0.25), 1.5 * S, lit)
        tips.append((tip, bend, i))
    palette = [h for r in ramps for h in r]

    # Timothy: stiff stalks above the blades, each with a long cylindrical head.
    heads = g.get("heads", 0)
    if heads:
        hrnd = random.Random(seed + 3)
        hcols = {"summer": TIMOTHY, "autumn": GRASS_STRAW, "winter": GRASS_WINTER, "winter-snowy": GRASS_WINTER}[look]
        stalk = ramps[0]
        for j in range(heads):
            off = (j + 0.5) / heads * 2 - 1
            x0 = W * 0.5 + off * 1.0 * S
            top_len = (g["head_len"][1] - abs(off) * 3.0 - hrnd.uniform(0, 1.2)) * S
            x1 = x0 + off * 2.6 * S + lean_of(seed, 200 + j, gust, 0.35)
            y1 = max(1.5 * S, BASE_Y - top_len)
            d.line([(x0, BASE_Y - 1.0 * S), (x1, y1)], fill=B.ramp(stalk, 0.5), width=max(2, int(0.4 * S)))
            hl = 3.6 * S
            hx0, hy0 = x1, y1
            hx1 = x1 + (x0 - x1) * hl / (BASE_Y - y1)
            hy1 = y1 + hl
            d.line([(hx0, hy0), (hx1, hy1)], fill=B.ramp(hcols, 0.35), width=int(1.1 * S))
            d.line([(hx0 - 0.25 * S, hy0 + 0.3 * S), (hx1 - 0.25 * S, hy1 - 0.4 * S)], fill=B.ramp(hcols, 0.75), width=max(1, int(0.45 * S)))
            if look == "winter-snowy":
                snow_blob(d, hx0, hy0 + 0.2 * S, 0.8 * S, 0.6 * S)
        palette += hcols

    if look == "winter-snowy":
        # snow on some of the bent tips: the blades that bend furthest catch it
        for (tx, ty), bend, i in tips:
            if abs(bend) > 0.9 * S and frac(seed, i, 17) < 0.55:
                snow_blob(d, tx - bend * 0.12, ty + 0.5 * S, 0.75 * S, 0.55 * S)
        palette += B.SNOW
    return lay, palette


# ---------------------------------------------------------------- fern

def paint_fern(piece, seed, look, gust):
    """Five fronds out of a crown at the foot: the middle one stands up at the
    back, two arch out to the sides and two lie low in front. At this size a
    frond is a solid tapering blade (thin leaflets would vanish in the
    shrink): widest a third of the way out, its edge toothed where the
    leaflets end, dark cuts between the leaflets running forward from a dark
    midrib, and the side facing up lit. Back fronds are drawn first and
    darker."""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    rnd = random.Random(seed)
    if look == "summer":
        ramps = [FERN_GREEN]
    elif look == "autumn":
        ramps = [FERN_RUST, FERN_GOLD]
    else:
        ramps = [FERN_DEAD]
    winter = look.startswith("winter")
    cx, cy = W * 0.5, BASE_Y - 0.6 * S
    # (angle off upright, length, droop, depth 1 = back)
    fronds = [(0.05, 9.0, 0.8, 1.0), (-0.72, 8.2, 1.8, 0.75), (0.78, 8.0, 2.0, 0.75),
              (-1.38, 6.8, 1.6, 0.2), (1.36, 7.0, 1.4, 0.2)]
    snow_spots = []
    for i, (turn, length, droop, back) in enumerate(fronds):
        cols = ramps[int(frac(seed, i, 3) * len(ramps))]
        lean = lean_of(seed, i, gust, 0.25)
        a = -math.pi / 2 + turn + rnd.uniform(-0.05, 0.05)
        L = (length + rnd.uniform(-0.3, 0.3)) * S
        dr = (droop + (1.4 if winter else 0.0)) * S
        v = 0.6 - 0.2 * back
        steps = 14
        spine = []
        for k in range(steps + 1):
            t = k / steps
            x = cx + math.cos(a) * L * t + (lean if t > 0.5 else 0.0) * (t - 0.5) * 2
            # the limp winter fronds would droop off the frame's bottom row
            y = min(BASE_Y + 0.8 * S, cy + math.sin(a) * L * t + dr * t * t * 1.4)
            spine.append((x, y))
        left, right, normals = [], [], []
        for k in range(steps + 1):
            x, y = spine[k]
            nx, ny = spine[min(steps, k + 1)]
            px_, py_ = spine[max(0, k - 1)]
            dx, dy = nx - px_, ny - py_
            ln = math.hypot(dx, dy) or 1.0
            ux, uy = dx / ln, dy / ln
            t = k / steps
            half = 1.15 * S * math.sin(math.pi * min(1.0, t * 1.2 + 0.08)) ** 0.8
            tooth = 0.45 * S * (1 if k % 2 else -0.2) * (1 - t)
            w = max(0.0, half + tooth)
            left.append((x - uy * w, y + ux * w))
            right.append((x + uy * w, y - ux * w))
            normals.append((ux, uy, w))
        # which edge faces up (the lit one)
        up_left = sum(p[1] for p in left) < sum(p[1] for p in right)
        d.polygon(left + right[::-1], fill=B.ramp(cols, v))
        lit_edge = left if up_left else right
        d.polygon([spine[0]] + lit_edge[1:-1] + spine[-2:0:-1], fill=B.ramp(cols, v + 0.16))
        # cuts between leaflets: from the midrib forward and out to each edge
        for k in range(3, steps - 1, 2):
            x, y = spine[k]
            ux, uy, w = normals[k]
            for side in (-1, 1):
                ex = x + (-uy * side) * w * 0.95 + ux * w * 0.55
                ey = y + (ux * side) * w * 0.95 + uy * w * 0.55
                d.line([(x, y), (ex, ey)], fill=B.ramp(cols, v - 0.28), width=max(1, int(0.4 * S)))
            if winter and k % 4 == 1:
                snow_spots.append((x, y))
        d.line(spine[:-2], fill=B.ramp(cols, v - 0.32), width=max(1, int(0.4 * S)))
    if look == "winter-snowy":
        for k, (x, y) in enumerate(snow_spots):
            if frac(seed, k, 19) < 0.6 and y < cy - 1.0 * S:
                snow_blob(d, x, y - 0.3 * S, 1.0 * S, 0.55 * S)
    palette = [h for r in ramps for h in r] + (B.SNOW if look == "winter-snowy" else [])
    return lay, palette


# ---------------------------------------------------------------- clover

def paint_clover(piece, seed, look, gust):
    """A low patch: leaves of three round leaflets on short stalks, a pale
    chevron on some, white flower heads in summer."""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    rnd = random.Random(seed)
    cols_of = {"summer": [CLOVER], "autumn": [CLOVER, CLOVER_TURN], "winter": [CLOVER_WINTER],
               "winter-snowy": [CLOVER_WINTER]}[look]
    leaves = [(-3.6, -1.6), (-0.6, -3.4), (2.8, -2.0), (-2.2, 0.8), (1.2, 0.2), (4.2, 0.9), (-4.6, 1.4), (0.6, 2.2)]
    if look.startswith("winter"):
        leaves = leaves[::2]
    leaves.sort(key=lambda p: p[1])
    cx, cy = W * 0.5, BASE_Y - 2.4 * S
    used = []
    for k, (lx, ly) in enumerate(leaves):
        cols = cols_of[int(frac(seed, k, 2) * len(cols_of))]
        used.append(cols)
        x, y = cx + lx * S, cy + ly * S
        r = rnd.uniform(1.05, 1.3) * S
        turn = rnd.uniform(0, 2 * math.pi)
        d.line([(x, y), (x + 0.3 * S, cy + 3.2 * S)], fill=B.ramp(cols, 0.25), width=max(1, int(0.35 * S)))
        for j in range(3):
            a = turn + j * 2 * math.pi / 3
            fx, fy = x + math.cos(a) * r * 0.85, y + math.sin(a) * r * 0.6
            d.ellipse((fx - r * 0.75, fy - r * 0.55 + 0.35 * S, fx + r * 0.75, fy + r * 0.55 + 0.35 * S), fill=B.ramp(cols, 0.2))
            d.ellipse((fx - r * 0.75, fy - r * 0.55, fx + r * 0.75, fy + r * 0.55), fill=B.ramp(cols, 0.5))
            d.ellipse((fx - r * 0.55, fy - r * 0.45, fx + r * 0.2, fy + r * 0.05), fill=B.ramp(cols, 0.72))
    pal = [h for c in used for h in c]
    if look == "summer":
        for k, (fx, fy) in enumerate([(-1.6, -5.2), (2.4, -4.4)]):
            x, y = cx + fx * S, cy + fy * S
            d.line([(x, y), (x + 0.2 * S, cy + 1.0 * S)], fill=B.ramp(CLOVER, 0.35), width=max(1, int(0.35 * S)))
            r = 1.15 * S
            d.ellipse((x - r, y - r, x + r, y + r), fill=B.rgb(CLOVER_FLOWER[1]))
            d.ellipse((x - r * 0.8, y - r * 0.9, x + r * 0.4, y + r * 0.2), fill=B.rgb(CLOVER_FLOWER[3]))
            d.ellipse((x - r * 0.4, y + r * 0.3, x + r * 0.7, y + r), fill=B.rgb(CLOVER_PINK[0]))
        pal += CLOVER_FLOWER + CLOVER_PINK
    if look == "winter-snowy":
        snow_blob(d, cx - 1.8 * S, cy - 0.6 * S, 2.4 * S, 1.2 * S)
        snow_blob(d, cx + 2.6 * S, cy + 1.4 * S, 1.8 * S, 0.9 * S)
        pal += B.SNOW
    return lay, pal


# ---------------------------------------------------------------- mushrooms

def drop_leaves(d, rnd, cx, base, spread, count):
    """A few of the terrain's autumn leaves at the foot (rocks.py's)."""
    for _ in range(count):
        lx = cx + rnd.uniform(-spread, spread)
        ly = base + rnd.uniform(-0.6, 0.9) * S
        r = rnd.uniform(0.8, 1.1) * S
        d.ellipse((lx - r, ly - r * 0.6, lx + r, ly + r * 0.6), fill=B.rgb(rnd.choice(B.LEAVES)))


def cap_dome(d, x, top, w, h, cols, rim=0.25):
    """A domed cap: dark underside rim, the dome, a lit upper left."""
    d.ellipse((x - w / 2, top + h * 0.55, x + w / 2, top + h * 1.15), fill=B.ramp(cols, rim))
    d.chord((x - w / 2, top, x + w / 2, top + h * 2), 180, 360, fill=B.ramp(cols, 0.5))
    d.chord((x - w * 0.42, top + h * 0.12, x + w * 0.2, top + h * 1.3), 190, 300, fill=B.ramp(cols, 0.75))
    d.ellipse((x - w * 0.28, top + h * 0.22, x - w * 0.08, top + h * 0.45), fill=B.ramp(cols, 0.92))


def stem(d, x, y0, y1, w, cols):
    d.rounded_rectangle((x - w / 2, y1, x + w / 2, y0), radius=w * 0.4, fill=B.ramp(cols, 0.6))
    d.rectangle((x + w * 0.1, y1, x + w / 2, y0 - w * 0.2), fill=B.ramp(cols, 0.35))
    d.ellipse((x - w * 0.62, y0 - w * 0.5, x + w * 0.62, y0 + w * 0.25), fill=B.ramp(cols, 0.45))


def paint_mushroom(piece, seed, look, gust):
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    rnd = random.Random(seed)
    winter = look.startswith("winter")
    tone = (lambda cols: ramp_dull(cols, 0.35)) if winter else (lambda cols: cols)
    cx, base = W * 0.5, BASE_Y
    caps = []            # (x, top, w) for snow
    pal = []
    kind = piece.kind
    if kind == "bolete":
        # two penny buns: a big one and a young one behind and to the left
        cap, st = tone(BOLETE), tone(STEM)
        for (dx, h, w, sw) in ((-3.0, 4.6, 5.6, 2.6), (1.4, 6.6, 8.4, 3.6)):
            x = cx + dx * S
            top = base - h * S
            stem(d, x, base, top + 1.6 * S, sw * S, st)
            cap_dome(d, x, top - 0.4 * S, w * S, 2.6 * S, cap)
            caps.append((x, top - 0.4 * S, w * S))
        pal = cap + st
    elif kind == "grey":
        # three slender bell caps on tall pale stems, leaning apart
        cap, st = tone(GREY_CAP), tone(STEM)
        for (dx, h, w, tilt) in ((-3.2, 6.0, 3.6, -0.8), (0.6, 8.6, 4.2, 0.2), (3.6, 5.0, 3.2, 1.0)):
            x = cx + dx * S
            top = base - h * S
            d.line([(x, base), (x + tilt * S, top + 1.4 * S)], fill=B.ramp(st, 0.6), width=int(1.0 * S))
            d.line([(x + 0.3 * S, base), (x + tilt * S + 0.3 * S, top + 1.4 * S)], fill=B.ramp(st, 0.35), width=max(1, int(0.35 * S)))
            bx = x + tilt * S
            d.chord((bx - w * S / 2, top, bx + w * S / 2, top + 3.6 * S), 180, 360, fill=B.ramp(cap, 0.45))
            d.polygon([(bx - w * S / 2, top + 1.8 * S), (bx, top - 0.9 * S), (bx + w * S / 2, top + 1.8 * S)], fill=B.ramp(cap, 0.45))
            d.line([(bx - w * S / 2, top + 1.9 * S), (bx + w * S / 2, top + 1.9 * S)], fill=B.ramp(cap, 0.2), width=max(1, int(0.5 * S)))
            d.polygon([(bx - w * S * 0.35, top + 1.4 * S), (bx - 0.2 * S, top - 0.4 * S), (bx - 0.1 * S, top + 1.4 * S)], fill=B.ramp(cap, 0.75))
            caps.append((bx, top - 0.6 * S, w * S * 0.8))
        pal = cap + st
    elif kind == "chanterelle":
        # golden funnels with wavy, turned-up rims; the gills run down the stem
        cap = tone(CHANTERELLE)
        for (dx, h, w) in ((-3.4, 4.0, 4.4), (2.8, 4.8, 5.0), (-0.2, 6.6, 5.8)):
            x = cx + dx * S
            top = base - h * S
            d.polygon([(x - w * S / 2, top + 0.8 * S), (x + w * S / 2, top + 0.8 * S), (x + 0.8 * S, base), (x - 0.8 * S, base)], fill=B.ramp(cap, 0.42))
            d.polygon([(x + 0.2 * S, top + 1.2 * S), (x + w * S / 2, top + 0.9 * S), (x + 0.8 * S, base)], fill=B.ramp(cap, 0.25))
            d.ellipse((x - w * S / 2, top, x + w * S / 2, top + 1.8 * S), fill=B.ramp(cap, 0.7))
            d.ellipse((x - w * S * 0.3, top + 0.35 * S, x + w * S * 0.3, top + 1.3 * S), fill=B.ramp(cap, 0.5))
            for k in range(3):
                wx = x - w * S / 2 + (k + 0.5) * w * S / 3
                d.ellipse((wx - 0.45 * S, top - 0.25 * S, wx + 0.45 * S, top + 0.35 * S), fill=B.ramp(cap, 0.88))
            caps.append((x, top, w * S))
        pal = cap
    elif kind == "puffball":
        # three white balls, the biggest at the back, a stub of a base under each
        cap = tone(PUFFBALL)
        for (dx, dy, r) in ((1.0, -1.6, 3.0), (-2.8, 0.0, 2.2), (3.4, 0.6, 1.5)):
            x, y = cx + dx * S, base + dy * S - r * S
            d.ellipse((x - r * S, y - r * S * 0.9 + 0.4 * S, x + r * S, y + r * S * 0.9 + 0.4 * S), fill=B.ramp(cap, 0.2))
            d.ellipse((x - r * S, y - r * S * 0.9, x + r * S, y + r * S * 0.9), fill=B.ramp(cap, 0.55))
            d.ellipse((x - r * S * 0.8, y - r * S * 0.8, x + r * S * 0.4, y + r * S * 0.3), fill=B.ramp(cap, 0.8))
            d.ellipse((x - r * S * 0.5, y - r * S * 0.6, x - r * S * 0.05, y - r * S * 0.2), fill=B.ramp(cap, 0.98))
            for k in range(3):
                sx = x + (frac(seed, int(dx * 10), k) - 0.3) * r * S
                sy = y + (frac(seed, int(dx * 10), k, 1) - 0.2) * r * S * 0.7
                d.point((sx, sy), fill=B.ramp(cap, 0.35))
            caps.append((x, y - r * S * 0.9, r * S * 1.6))
        pal = cap
    else:
        # Turkey tail on a short log lying across the frame: three fans stand
        # up off the log's top, each banded brown, rust and grey-tan out to a
        # cream rim, the bands following the fan's curve.
        wood, br, rust = tone(ROTWOOD), tone(BRACKET), tone(BRACKET_RUST)
        wx0, wx1, wy = cx - 5.6 * S, cx + 5.6 * S, base - 1.4 * S
        d.rounded_rectangle((wx0, wy - 1.4 * S, wx1, wy + 1.3 * S), radius=1.3 * S, fill=B.ramp(wood, 0.4))
        d.rounded_rectangle((wx0 + 0.4 * S, wy - 1.4 * S, wx1 - 1.0 * S, wy - 0.6 * S), radius=0.4 * S, fill=B.ramp(wood, 0.7))
        for k in range(3):
            fx = wx0 + (1.6 + k * 2.6) * S
            d.line([(fx, wy - 0.4 * S), (fx + 1.2 * S, wy + 0.8 * S)], fill=B.ramp(wood, 0.15), width=max(1, int(0.35 * S)))
        d.ellipse((wx1 - 1.6 * S, wy - 1.4 * S, wx1 + 0.3 * S, wy + 1.3 * S), fill=B.ramp(wood, 0.85))
        d.ellipse((wx1 - 1.1 * S, wy - 0.7 * S, wx1 - 0.3 * S, wy + 0.6 * S), fill=B.ramp(wood, 0.35))
        bands = [(1.0, br, 0.05), (0.86, br, 0.97), (0.66, rust, 0.45), (0.46, br, 0.65), (0.26, br, 0.2)]
        for (dx, dy, r) in ((-2.8, -0.8, 3.4), (2.4, -1.4, 3.8), (-0.2, 0.6, 2.6)):
            x, y = cx + dx * S, wy + dy * S
            rx, ry = r * S, r * S * 0.8
            d.pieslice((x - rx, y - ry + 0.4 * S, x + rx, y + ry + 0.4 * S), 180, 360, fill=B.ramp(br, 0.05))
            for t, cols, v in bands:
                d.pieslice((x - rx * t, y - ry * t, x + rx * t, y + ry * t), 180, 360, fill=B.ramp(cols, v))
            d.line([(x - rx, y), (x + rx, y)], fill=B.ramp(br, 0.1), width=max(1, int(0.45 * S)))
            caps.append((x, y - ry, rx * 1.4))
        pal = wood + br + rust
    if look == "autumn":
        drop_leaves(d, random.Random(seed + 5), cx, base, 4.5 * S, 3)
        pal = pal + B.LEAVES
    if look == "winter-snowy":
        for x, top, w in caps:
            snow_blob(d, x - 0.1 * w, top + 0.35 * S, w * 0.42, 0.75 * S)
        pal = pal + B.SNOW
    return lay, pal


# ---------------------------------------------------------------- the forest floor

def floor_ramp(look, summer, autumn, winter):
    return {"summer": summer, "autumn": autumn, "winter": winter, "winter-snowy": winter}[look]


def paint_cone(piece, seed, look, gust):
    """A pine cone lying on its side: rows of scales with dark gaps, lit on
    the upper left."""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cols = ramp_dull(CONE, 0.25) if look.startswith("winter") else CONE
    cx, cy = W * 0.5, BASE_Y - 3.4 * S
    ang = -0.42
    L, R = 10.6 * S, 3.4 * S
    ca, sa = math.cos(ang), math.sin(ang)

    def at(u, v):
        return (cx + u * ca - v * sa, cy + u * sa + v * ca)
    body = [at(L / 2 * math.cos(t), R * math.sin(t) * (0.75 + 0.25 * math.cos(t))) for t in [k * math.pi / 18 for k in range(36)]]
    d.polygon(body, fill=B.ramp(cols, 0.15))
    rows = 6
    for i in range(rows):
        u = -L / 2 + (i + 0.7) * L / rows
        width = R * math.sqrt(max(0.05, 1 - (u / (L / 2)) ** 2)) * 1.05
        for j in range(-2, 3):
            v = j * width / 2.6 + (0.5 if i % 2 else 0.0) * width / 2.6
            if abs(v) > width:
                continue
            x, y = at(u, v)
            s = 1.05 * S * (0.7 + 0.3 * (width / R))
            lit = 0.42 + (0.3 if v < 0 else 0.0) + (0.08 if u < 0 else 0.0)
            d.ellipse((x - s, y - s * 0.75, x + s * 0.8, y + s * 0.6), fill=B.ramp(cols, lit))
            d.ellipse((x - s * 0.6, y - s * 0.65, x + s * 0.1, y - s * 0.1), fill=B.ramp(cols, lit + 0.25))
    # the stalk end
    sx, sy = at(L / 2 + 0.4 * S, 0)
    d.line([at(L / 2 - 0.6 * S, 0), (sx, sy)], fill=B.ramp(TWIG, 0.3), width=int(0.8 * S))
    pal = cols + TWIG
    if look == "autumn":
        drop_leaves(d, random.Random(seed + 5), cx, BASE_Y - 0.5 * S, 4.0 * S, 2)
        pal += B.LEAVES
    if look == "winter-snowy":
        x, y = at(0, -R * 0.7)
        snow_blob(d, x, y, 3.2 * S, 1.0 * S)
        pal += B.SNOW
    return lay, pal


def maple_leaf(d, cx, cy, size, ang, cols, flat=0.7):
    """A fallen maple leaf lying flat: five pointed lobes round a stalk,
    squashed by flat for the view, with darker veins."""
    pts = []
    lobes = [(0.0, 1.0), (0.5, 0.62), (1.0, 0.9), (1.45, 0.62), (1.9, 0.7), (math.pi, 0.35)]
    prof = []
    for a, r in lobes:
        prof.append((a, r))
    full = prof + [(-a, r) for a, r in reversed(prof[1:-1])]
    full.sort()
    for a, r in full:
        aa = a + ang - math.pi / 2
        pts.append((cx + math.cos(aa) * r * size, cy + math.sin(aa) * r * size * flat))
    d.polygon([(x, y + 0.45 * S) for x, y in pts], fill=B.ramp(cols, 0.12))
    d.polygon(pts, fill=B.ramp(cols, 0.5))
    inner = [(cx + (x - cx) * 0.55 - 0.3 * S, cy + (y - cy) * 0.55 - 0.3 * S) for x, y in pts]
    d.polygon(inner, fill=B.ramp(cols, 0.68))
    for a in (0.0, 1.0, -1.0):
        aa = a + ang - math.pi / 2
        d.line([(cx, cy), (cx + math.cos(aa) * 0.8 * size, cy + math.sin(aa) * 0.8 * size * flat)],
               fill=B.ramp(cols, 0.3), width=max(1, int(0.3 * S)))
    aa = math.pi + ang - math.pi / 2
    d.line([(cx, cy), (cx + math.cos(aa) * 0.75 * size, cy + math.sin(aa) * 0.75 * size * flat)],
           fill=B.ramp(cols, 0.2), width=max(1, int(0.4 * S)))


def paint_leaf(piece, seed, look, gust):
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cols = floor_ramp(look, LEAF_GREEN, LEAF_RED, LEAF_BROWN)
    cx, cy = W * 0.5, BASE_Y - 4.0 * S
    maple_leaf(d, cx, cy, 6.0 * S, 0.5, cols, flat=0.8)
    pal = list(cols)
    if look == "winter-snowy":
        snow_blob(d, cx + 1.2 * S, cy - 0.4 * S, 2.6 * S, 1.3 * S)
        pal += B.SNOW
    return lay, pal


def draw_stick(d, pts, w, cols):
    """A stick along pts: shadow side, body, a lit upper edge."""
    d.line([(x, y + 0.35 * S) for x, y in pts], fill=B.ramp(cols, 0.15), width=int(w))
    d.line(pts, fill=B.ramp(cols, 0.5), width=int(w))
    d.line([(x, y - w * 0.3) for x, y in pts], fill=B.ramp(cols, 0.8), width=max(1, int(w * 0.35)))


def paint_twig(piece, seed, look, gust):
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cols = ramp_dull(TWIG, 0.2) if look.startswith("winter") else TWIG
    cy = BASE_Y - 2.4 * S
    main = [(2.2 * S, cy + 2.4 * S), (6.0 * S, cy + 0.8 * S), (9.6 * S, cy - 0.6 * S), (13.6 * S, cy - 2.6 * S)]
    draw_stick(d, main, 1.0 * S, cols)
    draw_stick(d, [(6.4 * S, cy + 0.6 * S), (8.2 * S, cy - 1.8 * S), (9.0 * S, cy - 3.6 * S)], 0.75 * S, cols)
    draw_stick(d, [(10.6 * S, cy - 1.0 * S), (12.8 * S, cy - 0.4 * S)], 0.6 * S, cols)
    pal = list(cols)
    if look == "autumn":
        drop_leaves(d, random.Random(seed + 5), W * 0.5, BASE_Y - 0.4 * S, 3.5 * S, 2)
        pal += B.LEAVES
    if look == "winter-snowy":
        for x, y in main[1:3]:
            snow_blob(d, x + 0.6 * S, y - 0.6 * S, 1.6 * S, 0.6 * S)
        pal += B.SNOW
    return lay, pal


def paint_branch(piece, seed, look, gust):
    """A fallen branch, longer and thicker than the twig, forking once, with
    a few leaves still on it (green in summer, turned in autumn, none in
    winter)."""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cols = ramp_dull(TWIG, 0.2) if look.startswith("winter") else TWIG
    cy = BASE_Y - 2.4 * S
    main = [(1.6 * S, cy + 1.2 * S), (5.0 * S, cy + 0.6 * S), (9.0 * S, cy - 0.6 * S), (14.4 * S, cy - 0.8 * S)]
    draw_stick(d, main, 1.45 * S, cols)
    fork = [(8.0 * S, cy - 0.3 * S), (10.2 * S, cy - 2.6 * S), (12.0 * S, cy - 4.4 * S)]
    draw_stick(d, fork, 0.95 * S, cols)
    # the broken butt: a pale ring at the thick end
    d.ellipse((1.0 * S, cy + 0.4 * S, 2.6 * S, cy + 2.2 * S), fill=B.ramp(KEY_TAN, 0.8))
    pal = list(cols) + KEY_TAN
    if not look.startswith("winter"):
        lc = LEAF_GREEN if look == "summer" else LEAF_GOLD
        for k, (x, y, a) in enumerate(((12.4 * S, cy - 5.0 * S, 0.6), (13.6 * S, cy - 2.2 * S, 2.0), (6.0 * S, cy - 1.4 * S, -0.8))):
            r = 1.3 * S
            ex, ey = x + math.cos(a) * r, y + math.sin(a) * r * 0.6
            d.ellipse((ex - r, ey - r * 0.55 + 0.3 * S, ex + r, ey + r * 0.55 + 0.3 * S), fill=B.ramp(lc, 0.2))
            d.ellipse((ex - r, ey - r * 0.55, ex + r, ey + r * 0.55), fill=B.ramp(lc, 0.55 + 0.1 * (k % 2)))
        pal += lc
    if look == "winter-snowy":
        for x, y in (main[1], main[2], fork[1]):
            snow_blob(d, x + 0.4 * S, y - 0.8 * S, 1.8 * S, 0.65 * S)
        pal += B.SNOW
    return lay, pal


def draw_acorn(d, x, y, ang, nut, cap, cap_on=True):
    """An acorn lying on its side: a smooth nut, lit on the upper left, with a
    pale tip; its scaly cap on the other end (or not)."""
    ca, sa = math.cos(ang), math.sin(ang)

    def at(u, v):
        return (x + u * ca - v * sa, y + u * sa + v * ca)
    L, R = 2.0 * S, 1.45 * S
    body = [at(L * math.cos(t) * (1.0 if math.cos(t) > 0 else 0.8), R * math.sin(t)) for t in [k * math.pi / 12 for k in range(24)]]
    d.polygon([(px, py + 0.4 * S) for px, py in body], fill=B.ramp(nut, 0.1))
    d.polygon(body, fill=B.ramp(nut, 0.5))
    hi = [at(L * 0.55 * math.cos(t) + 0.2 * S, R * 0.5 * math.sin(t) - 0.55 * S) for t in [k * math.pi / 8 for k in range(16)]]
    d.polygon(hi, fill=B.ramp(nut, 0.8))
    tx, ty = at(L + 0.2 * S, 0)
    d.ellipse((tx - 0.4 * S, ty - 0.4 * S, tx + 0.4 * S, ty + 0.4 * S), fill=B.ramp(nut, 0.3))
    if cap_on:
        cx_, cy_ = at(-L * 0.75, 0)
        rc = 1.7 * S
        d.ellipse((cx_ - rc, cy_ - rc, cx_ + rc, cy_ + rc), fill=B.ramp(cap, 0.3))
        for k in range(5):
            a2 = ang + math.pi + (k - 2) * 0.5
            sx, sy = cx_ + math.cos(a2) * rc * 0.55, cy_ + math.sin(a2) * rc * 0.55
            d.ellipse((sx - 0.5 * S, sy - 0.5 * S, sx + 0.5 * S, sy + 0.5 * S), fill=B.ramp(cap, 0.65 if sy < cy_ else 0.45))


def paint_keys(piece, seed, look, gust):
    """Seeds: three acorns lying in the leaf litter, one lost its cap, the cap
    lying by it, cup up."""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    nut = floor_ramp(look, ACORN_GREEN, ACORN_BROWN, ACORN_DARK)
    cap = ramp_dull(ACORN_CAP, 0.25) if look.startswith("winter") else ACORN_CAP
    cx, cy = W * 0.5, BASE_Y - 2.6 * S
    draw_acorn(d, cx - 2.6 * S, cy - 1.6 * S, 0.5, nut, cap)
    draw_acorn(d, cx + 2.8 * S, cy - 0.4 * S, 2.7, nut, cap)
    draw_acorn(d, cx - 0.6 * S, cy + 1.6 * S, -0.3, nut, cap, cap_on=False)
    # the loose cap, cup up: a dark bowl with a lit scaly rim
    x, y = cx + 3.6 * S, cy + 2.2 * S
    d.ellipse((x - 1.5 * S, y - 0.6 * S, x + 1.5 * S, y + 1.2 * S), fill=B.ramp(cap, 0.45))
    d.ellipse((x - 1.1 * S, y - 0.5 * S, x + 1.1 * S, y + 0.4 * S), fill=B.ramp(cap, 0.1))
    pal = list(nut) + list(cap)
    if look == "winter-snowy":
        snow_blob(d, cx + 1.0 * S, cy - 1.6 * S, 2.6 * S, 1.0 * S)
        pal += B.SNOW
    return lay, pal


PAINTERS = {"grass": paint_grass, "fern": paint_fern, "clover": paint_clover, "mushroom": paint_mushroom,
            "cone": paint_cone, "leaf": paint_leaf, "twig": paint_twig, "branch": paint_branch,
            "keys": paint_keys}


# ---------------------------------------------------------------- to pixels

def shrink(lay, palette, outline, ring=0.0):
    """To frame size: area average, every covered pixel snapped to the look's
    own colours, the down-facing edge mixed toward the outline (by outline),
    the other edges by ring, and a shadow on the ground one pixel down and
    right."""
    small = lay.resize((FW, FH), Image.BOX)
    sp = small.load()
    pal = [B.rgb(h)[:3] for h in palette]
    covered = {(x, y): sp[x, y][:3] for y in range(FH) for x in range(FW) if sp[x, y][3] >= 128}
    out = Image.new("RGBA", (FW, FH), (0, 0, 0, 0))
    op = out.load()
    for (x, y) in covered:
        sx, sy = x + 1, y + 1
        if 0 < sx < FW - 1 and sy < FH - 1 and (sx, sy) not in covered:
            op[sx, sy] = B.SHADOW
    snow = [B.rgb(h)[:3] for h in B.SNOW]
    for (x, y), c in covered.items():
        snapped = B.nearest(c, pal)
        if snapped not in snow:
            if outline and (x, y + 1) not in covered:
                snapped = B.mix(snapped, B.OUTLINE, outline)[:3]
            elif ring and any(n not in covered for n in ((x - 1, y), (x + 1, y), (x, y - 1))):
                snapped = B.mix(snapped, B.OUTLINE, ring)[:3]
        op[x, y] = snapped + (255,)
    return out


def look_frames(piece, look):
    paint = PAINTERS[piece.painter]
    lay, palette = paint(piece, piece.seed, look, None)
    still = shrink(lay, palette, piece.outline, piece.ring)
    if not piece.animated:
        return [still]
    frames = [still] * B.FRAMES
    for i, (stir, lean) in enumerate(zip(B.PUFF, B.LEAN)):
        lay, palette = paint(piece, piece.seed, look, (i, stir, lean))
        frames[B.PUFF_AT + i] = shrink(lay, palette, piece.outline, piece.ring)
    return frames


def frame_count(piece):
    return B.FRAMES if piece.animated else 1


def build_sheet(piece):
    use_frame(*piece.frame, density=piece.density)
    n = frame_count(piece)
    sheet = Image.new("RGBA", (FW * n, FH * len(LOOKS)), (0, 0, 0, 0))
    for row, look in enumerate(LOOKS):
        for col, frame in enumerate(look_frames(piece, look)):
            sheet.paste(frame, (col * FW, row * FH))
    return sheet


def clipped_paintings(piece, look):
    """The frames (0 for the still one, else the puff frame's column) whose
    large painting touches its canvas's edge. Pillow clips a drawing at the
    canvas, and the shrink drops a pixel less than half covered, so a sheet
    can pass the frame-edge check below while a shape was cut off. This is
    an edge-contact check: a shape cut by the edge touches it, but a detail
    drawn wholly outside the canvas leaves no pixel and is not seen."""
    paintings = [(0, None)] + [(B.PUFF_AT + i, (i, stir, lean))
                               for i, (stir, lean) in enumerate(zip(B.PUFF, B.LEAN))] if piece.animated \
        else [(0, None)]
    bad = []
    for col, gust in paintings:
        lay, _ = PAINTERS[piece.painter](piece, piece.seed, look, gust)
        box = lay.getbbox()
        if box is not None and (box[0] == 0 or box[1] == 0 or box[2] == W or box[3] == H):
            bad.append(col)
    return bad


def check(out_dir, names):
    """Every frame stays a pixel inside its frame on all four sides (a piece
    on the frame edge is cut off flat), holds some art, its large painting
    does not touch the canvas edge (clipped_paintings), and the sheet is its
    size."""
    ok = True
    for name in names:
        piece = PIECES[name]
        use_frame(*piece.frame, density=piece.density)
        n = frame_count(piece)
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        want = (FW * n, FH * len(LOOKS))
        if sheet.size != want:
            print("%s: sheet is %dx%d, want %dx%d" % ((name,) + sheet.size + want))
            ok = False
            continue
        for r, look in enumerate(LOOKS):
            bad = []
            for col in range(n):
                frame = sheet.crop((col * FW, r * FH, (col + 1) * FW, (r + 1) * FH))
                px = frame.load()
                edge = any(px[x, y][3] == 255 for y in range(FH) for x in (0, FW - 1)) or \
                    any(px[x, y][3] == 255 for x in range(FW) for y in (0, FH - 1))
                if edge or frame.getbbox() is None:
                    bad.append(col)
            clipped = clipped_paintings(piece, look)
            ok = ok and not bad and not clipped
            print("%-16s %-13s %2d frame(s)%s%s" % (name, look, n,
                  "" if not bad else "  <- on the frame edge or empty: %s" % bad,
                  "" if not clipped else "  <- painting clipped at the canvas: %s" % clipped))
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's ground cover.")
    parser.add_argument("--piece", default=",".join(PIECES), help="comma-separated, of: " + ", ".join(PIECES))
    parser.add_argument("--out", default=OUT_DIR, help="where the sheets go (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheets instead of painting them")
    parser.add_argument("--density", type=int, help="override every piece's density (prototypes)")
    args = parser.parse_args()
    if args.density:
        for piece in PIECES.values():
            piece.density = args.density
    names = args.piece.split(",")
    for name in names:
        if name not in PIECES:
            parser.error("unknown piece %r (have %s)" % (name, ", ".join(PIECES)))
    if args.check:
        sys.exit(0 if check(args.out, names) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name in names:
        build_sheet(PIECES[name]).save(os.path.join(args.out, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
