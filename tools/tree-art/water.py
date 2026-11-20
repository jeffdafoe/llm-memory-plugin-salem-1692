"""Paints salem's water edge (LLM-967): reeds, lily pads, a floating vine and a
sunk barrel. Every pixel is drawn here, no pack art.

    python tools/tree-art/water.py
    python tools/tree-art/water.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/water.py --check     # every frame inside its frame, sheets their size

Writes client/assets/trees/<piece>.png (cattails-1..3, water-plant-1..3,
lily-pad, light-lily-pad, small-lily-pad, tiny-lily-pad, water-vine,
sunk-barrel). Each sheet has one row per look, top to bottom: summer, autumn,
winter, winter-snowy (the sunk barrel has the one look); a row is the look's
frames, left to right, at the piece's frame size. The reeds rustle: a
build.FRAMES-long loop at build.FPS with the trees' light-wind puff at its end.
Everything else floats still (the water's own glints move round it) and has
one frame. Rerunning writes the same files. After a change, run
`godot_console --headless --path client --import` so Godot writes the .import
sidecars, and commit them.

It imports build.py (the trees) for its scale, colour helpers, puff and
shadow, so the water edge is painted, shrunk and snapped the way the trees
are: at build.S times size, area-averaged down, every pixel snapped to the
look's own colours.

How each piece is made, from photographs of New England ponds:
- Reeds (Cattails, Water Plant): a clump of long tapering blades arching out
  of the water, with stalks carrying brown seed heads. Green in summer, straw
  in autumn, pale and broken in winter, when the heads burst into fluff; snow
  caps the heads in the snowy look. A pale ripple ring lies round the foot.
  In the puff the upper half of a blade or stalk leans one frame pixel, each
  on its own threshold, as the trees' leaf pads do.
- Lily pads: round leaves with a notch to the middle, lying flat (an ellipse
  in the top-down view), lit along the upper-left rim, a darker lip where the
  leaf's edge meets the water and a pale waterline under it. Green in summer
  (the big pad with a white water lily), yellow, orange and red in autumn,
  brown in winter; the water does not freeze, so they stay, and snow lies on
  them in the snowy look.
- Water vine: a trailing stem lying on the water with small oval leaves.
- Sunk barrel: an oak barrel sunk to its middle staves, its lid and iron
  hoops above the water, a ripple ring round it.
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

# The water these pieces sit on (tools/terrain-art SHALLOW_WATER, summer): the
# ripple rings and waterlines are its light tones.
RIPPLE = ["a6d6db", "c8e6ea", "e8f6f6"]

# ---------------------------------------------------------------- colours

REED_GREEN = ["1c3416", "2a4c1e", "3c6826", "52862e", "6ea23a", "92bc52", "b8d470"]
REED_STRAW = ["3e3216", "5e4c20", "806a2c", "a28a3e", "bea656", "d6c276", "e8da9e"]
REED_WINTER = ["3a3428", "565040", "746c58", "908870", "aaa28a", "c2bca6", "d8d4c2"]
HEAD = ["1e120c", "3a2216", "5a3620", "7a4c2c", "966440"]
HEAD_FLUFF = ["6e6252", "948a78", "b8b0a0", "dcd6ca", "f0ece4"]

PAD_GREEN = ["17361c", "245226", "336e30", "468a3a", "5ea446", "80bc5a", "a8d474"]
PAD_LIGHT = ["24401c", "345c24", "48782c", "609634", "7eb240", "a2ca58", "c6e07a"]
PAD_YELLOW = ["4a3410", "745218", "9e7220", "c49430", "deb446", "eed06a"]
PAD_ORANGE = ["4e2410", "7c3a16", "a8561c", "d07426", "ea9636", "f6b858"]
PAD_RED = ["4a1410", "761e16", "a02c1c", "c84426", "e2643a", "f08a58"]
PAD_BROWN = ["2e2014", "4a321e", "664628", "7e5a34", "967044", "ae8a5a"]
PAD_OLIVE = ["2e2c16", "48441e", "625e28", "7c7634", "968e44", "aea85c"]
LILY_WHITE = ["b8b4a8", "d8d6cc", "f2f0e8", "fffef8"]
LILY_GOLD = ["b08a20", "e0b83a", "f6dc6a"]

OAK = ["3a2416", "5a3a22", "7c5430", "9e7040", "bc8e56", "d6ae78"]
IRON = ["2a2a30", "44444c", "5e5e68", "82828c"]

REED_RAMPS = {"summer": REED_GREEN, "autumn": REED_STRAW, "winter": REED_WINTER, "winter-snowy": REED_WINTER}
PAD_RAMPS = {"summer": [PAD_GREEN], "autumn": [PAD_YELLOW, PAD_ORANGE, PAD_RED],
             "winter": [PAD_BROWN, PAD_OLIVE], "winter-snowy": [PAD_BROWN, PAD_OLIVE]}


# ---------------------------------------------------------------- pieces

class Piece:
    def __init__(self, **kw):
        self.looks = LOOKS
        self.animated = False
        self.variants = None        # [(name, seed)]; None = one sheet named after the piece
        self.__dict__.update(kw)


PIECES = {
    # Cattails: tall blades and two or three stalks with the long brown heads.
    "cattails": Piece(frame=(16, 16), painter="reeds", animated=True,
                      variants=[("cattails-1", 3), ("cattails-2", 17), ("cattails-3", 42)],
                      reeds=dict(blades=7, blade_len=(7.0, 10.5), fan=0.8, bend=1.4, blade_w=0.7,
                                 stalks=3, stalk_len=(9.0, 12.0), head=(1.3, 3.2), spread=3.4)),
    # Water plant: a lower, fuller clump of shorter blades with three stubby
    # heads standing just above it (bur-reed by the pond edge).
    "water-plant": Piece(frame=(16, 16), painter="reeds", animated=True,
                         variants=[("water-plant-1", 5), ("water-plant-2", 26), ("water-plant-3", 58)],
                         reeds=dict(blades=10, blade_len=(5.0, 7.5), fan=1.25, bend=2.0, blade_w=0.9,
                                    stalks=3, stalk_len=(7.5, 9.5), head=(1.3, 2.0), spread=3.0, burs=True)),
    # The big lily pad: three pads touching, one with a white water lily.
    "lily-pad": Piece(frame=(32, 32), painter="pads", ramps_extra=[],
                      pads=[(-6.0, -1.5, 6.6, 0.35), (5.5, -3.5, 6.0, 2.4), (3.0, 3.0, 5.2, 4.3)],
                      flower=(-5.0, -2.5, 2.6)),
    "light-lily-pad": Piece(frame=(16, 16), painter="pads", light=True,
                            pads=[(-2.8, -2.0, 3.4, 0.5), (2.8, -1.3, 3.2, 2.6), (0.0, 2.0, 3.0, 4.6)]),
    "small-lily-pad": Piece(frame=(16, 16), painter="pads",
                            pads=[(-2.0, -1.8, 3.9, 0.8), (2.2, 1.2, 3.6, 3.6)]),
    "tiny-lily-pad": Piece(frame=(16, 16), painter="pads", pads=[(0.0, 0.0, 3.3, 1.2)]),
    "water-vine": Piece(frame=(16, 16), painter="vine"),
    "sunk-barrel": Piece(frame=(16, 16), painter="barrel", looks=["default"]),
}


def use_frame(fw, fh):
    """build.py's painters read the frame from its module globals."""
    global FW, FH, W, H, BASE_Y
    FW, FH = fw, fh
    W, H = fw * S, fh * S
    BASE_Y = H * ANCHOR_Y
    B.FW, B.FH, B.W, B.H = FW, FH, W, H


def seeds(piece, name):
    return piece.variants if piece.variants else [(name, 1)]


# ---------------------------------------------------------------- reeds

def ripple_ring(d, cx, cy, rx, ry, front):
    """A pale ring on the water round a foot: the back half under the plant
    (front=False), the front half over its foot (front=True)."""
    a0, a1 = (0, 180) if front else (180, 360)
    d.arc((cx - rx, cy - ry, cx + rx, cy + ry), a0, a1, fill=B.rgb(RIPPLE[1]), width=max(2, int(0.55 * S)))


def reed_lean(seed, i, gust, base):
    if gust is None:
        return 0.0
    threshold = base + 0.6 * (B._hash(seed, i, 5) & 0xFFFF) / 65535.0
    return S if gust[2] >= threshold else 0.0


def paint_reeds(piece, seed, look, gust):
    rd = piece.reeds
    rnd = random.Random(seed)
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cols = REED_RAMPS[look]
    winter = look.startswith("winter")
    ripple_ring(d, W * 0.5, BASE_Y, 4.2 * S, 1.3 * S, front=False)

    # Blades: each a tapering strip along a curve that leans out from the
    # middle, darker at the water, lit along one edge. The highest tips go
    # first so the lower, outer blades lie over them.
    blades = []
    for i in range(rd["blades"]):
        off = (i + 0.5) / rd["blades"] * 2 - 1
        a = -math.pi / 2 + off * rd["fan"] * 0.5 + rnd.uniform(-0.12, 0.12)
        L = rnd.uniform(*rd["blade_len"]) * S * (1 - 0.3 * abs(off))
        x0 = W * 0.5 + off * 2.2 * S + rnd.uniform(-0.4, 0.4) * S
        bend = off * rd["bend"] * S + rnd.uniform(-0.5, 0.5) * S
        shade = rnd.uniform(-0.08, 0.08)
        blades.append((x0, a, L, bend, shade, i))
    blades.sort(key=lambda b: math.sin(b[1]) * b[2])
    for x0, a, L, bend, shade, i in blades:
        lean = reed_lean(seed, i, gust, 0.3)
        pts_l, pts_r = [], []
        steps = 8
        for k in range(steps + 1):
            t = k / steps
            x = x0 + math.cos(a) * L * t + bend * t * t + (lean if t > 0.5 else 0.0) * (t - 0.5) * 2
            y = BASE_Y + math.sin(a) * L * t
            y = max(1.2 * S, y)
            w = rd["blade_w"] * S * (1 - t) * 0.5 + 0.12 * S
            pts_l.append((x - w, y))
            pts_r.append((x + w, y))
        v = 0.45 + shade
        d.polygon(pts_l + pts_r[::-1], fill=B.ramp(cols, v))
        d.line(pts_l[2:], fill=B.ramp(cols, v + 0.25), width=max(1, int(0.4 * S)))
        d.line(pts_r[:3], fill=B.ramp(cols, v - 0.3), width=max(1, int(0.5 * S)))

    # Stalks and their heads, spread across the clump.
    srnd = random.Random(seed + 11)
    for j in range(rd["stalks"]):
        off = (j + 0.5) / rd["stalks"] * 2 - 1
        x0 = W * 0.5 + off * 1.2 * S + srnd.uniform(-0.3, 0.3) * S
        # staggered: the middle stalk tallest, so the heads stand apart
        lo, hi = rd["stalk_len"]
        top = BASE_Y - (hi - abs(off) * (hi - lo) * 0.9 - srnd.uniform(0, 0.6)) * S
        top = max(top, 2.2 * S)
        x1 = W * 0.5 + off * rd["spread"] * S + reed_lean(seed, 100 + j, gust, 0.45)
        d.line([(x0, BASE_Y), (x1, top)], fill=B.ramp(cols, 0.55), width=max(2, int(0.45 * S)))
        hw, hh = rd["head"][0] * S * 0.5, rd["head"][1] * S
        hx, hy0 = x1, top + 0.9 * S
        if not rd.get("burs"):
            d.line([(hx, top - 0.9 * S), (hx, hy0)], fill=B.ramp(cols, 0.4), width=max(1, int(0.3 * S)))
        if rd.get("burs"):
            # bur-reed: two round spiky burs up the stalk's top
            for b, (by, br) in enumerate(((top + 0.6 * S, 0.95), (top + 2.6 * S, 0.8))):
                bx = x1 + (x0 - x1) * (by - top) / (BASE_Y - top)
                r = br * S
                d.ellipse((bx - r, by - r, bx + r, by + r), fill=B.ramp(HEAD_FLUFF if winter else REED_GREEN if look == "summer" else HEAD, 0.4))
                d.ellipse((bx - r * 0.8, by - r * 0.8, bx + r * 0.1, by + r * 0.1), fill=B.ramp(HEAD_FLUFF if winter else REED_GREEN if look == "summer" else HEAD, 0.8))
                if look == "winter-snowy" and b == 0:
                    d.ellipse((bx - r, by - r * 1.2, bx + r, by - r * 0.1), fill=B.rgb(B.SNOW[2]))
        elif winter:
            # a weathered head, splitting at the top into a little pale fluff
            d.rounded_rectangle((hx - hw, hy0, hx + hw, hy0 + hh), radius=hw, fill=B.ramp(HEAD, 0.55))
            d.rounded_rectangle((hx - hw, hy0 + 0.2 * S, hx - hw * 0.1, hy0 + hh - 0.4 * S), radius=hw * 0.5, fill=B.ramp(HEAD, 0.85))
            frnd = random.Random(seed * 7 + j)
            for _ in range(2):
                fx = hx + frnd.uniform(-0.6, 0.9) * hw
                fy = hy0 + frnd.uniform(0.0, 0.35) * hh
                r = frnd.uniform(0.4, 0.6) * S
                d.ellipse((fx - r, fy - r, fx + r, fy + r), fill=B.ramp(HEAD_FLUFF, frnd.uniform(0.6, 0.95)))
        else:
            d.rounded_rectangle((hx - hw, hy0, hx + hw, hy0 + hh), radius=hw, fill=B.ramp(HEAD, 0.35))
            d.rounded_rectangle((hx - hw, hy0 + 0.2 * S, hx - hw * 0.1, hy0 + hh - 0.4 * S), radius=hw * 0.5, fill=B.ramp(HEAD, 0.75))
        if look == "winter-snowy" and not rd.get("burs"):
            d.ellipse((hx - hw * 1.1, hy0 - 0.6 * S, hx + hw * 1.1, hy0 + 0.7 * S), fill=B.rgb(B.SNOW[2]))
    ripple_ring(d, W * 0.5, BASE_Y, 4.2 * S, 1.3 * S, front=True)
    palette = cols + HEAD + RIPPLE + (HEAD_FLUFF if winter else []) + (B.SNOW if look == "winter-snowy" else [])
    return lay, palette, False


# ---------------------------------------------------------------- lily pads

def draw_pad(lay, cx, cy, r, notch, cols, tone, snow):
    """One floating leaf, on its own layer laid over the others: an ellipse
    with a narrow wedge cut to the middle (cut from this leaf only), a dark
    lip on its lower edge, a lit upper-left rim and faint veins."""
    rx, ry = r * S, r * S * 0.72
    own = Image.new("RGBA", lay.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(own)

    def ell(scale, dy=0.0):
        return (cx - rx * scale, cy - ry * scale + dy, cx + rx * scale, cy + ry * scale + dy)

    def wedge():
        w = 0.2
        return [(cx, cy), (cx + math.cos(notch - w) * rx * 1.3, cy + math.sin(notch - w) * ry * 1.3),
                (cx + math.cos(notch + w) * rx * 1.3, cy + math.sin(notch + w) * ry * 1.3)]
    # the waterline: pale, a little below and wider than the leaf
    d.ellipse((cx - rx * 1.05, cy - ry * 0.9 + 0.7 * S, cx + rx * 1.05, cy + ry * 1.05 + 0.7 * S), fill=B.rgb(RIPPLE[0]))
    d.ellipse(ell(1.0, 0.45 * S), fill=B.ramp(cols, tone - 0.35))      # the lip
    d.ellipse(ell(1.0), fill=B.ramp(cols, tone))
    d.chord(ell(0.92), 150, 300, fill=B.ramp(cols, tone + 0.2))       # lit upper left
    d.ellipse(ell(0.62, 0.15 * S), fill=B.ramp(cols, tone + 0.05))
    for k in range(6 if r >= 5 else 0):
        a = notch + math.pi / 6 + k * (2 * math.pi - math.pi / 3) / 6
        d.line([(cx, cy), (cx + math.cos(a) * rx * 0.85, cy + math.sin(a) * ry * 0.85)], fill=B.ramp(cols, tone - 0.12), width=max(1, int(0.3 * S)))
    if snow:
        # on the half of the leaf away from the notch, so the cut never splits it
        sx, sy = cx - math.cos(notch) * rx * 0.35, cy - math.sin(notch) * ry * 0.35
        d.ellipse((sx - rx * 0.5, sy - ry * 0.45, sx + rx * 0.45, sy + ry * 0.35), fill=B.rgb(B.SNOW[1]))
        d.ellipse((sx - rx * 0.42, sy - ry * 0.42, sx + rx * 0.3, sy + ry * 0.15), fill=B.rgb(B.SNOW[2]))
    # the notch: the water shows through to the middle
    d.polygon(wedge(), fill=(0, 0, 0, 0))
    lay.alpha_composite(own)


def draw_lily(d, cx, cy, r):
    """A white water lily: two rings of pointed petals round a gold heart."""
    for ring, (n, rr, col) in enumerate(((8, 1.0, LILY_WHITE[1]), (6, 0.7, LILY_WHITE[3]))):
        for k in range(n):
            a = k * 2 * math.pi / n + ring * 0.4
            tip = (cx + math.cos(a) * r * rr * S, cy + math.sin(a) * r * rr * S * 0.75)
            side = 0.45
            p1 = (cx + math.cos(a - side) * r * rr * 0.45 * S, cy + math.sin(a - side) * r * rr * 0.45 * S * 0.75)
            p2 = (cx + math.cos(a + side) * r * rr * 0.45 * S, cy + math.sin(a + side) * r * rr * 0.45 * S * 0.75)
            d.polygon([(cx, cy), p1, tip, p2], fill=B.rgb(col))
    g = r * 0.32 * S
    d.ellipse((cx - g, cy - g * 0.8, cx + g, cy + g * 0.8), fill=B.rgb(LILY_GOLD[1]))
    d.ellipse((cx - g * 0.5, cy - g * 0.6, cx + g * 0.3, cy), fill=B.rgb(LILY_GOLD[2]))


def paint_pads(piece, seed, look, gust):
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cx0, cy0 = W * 0.5, BASE_Y - (piece.frame[1] * 0.24) * S
    ramps = PAD_RAMPS[look]
    if look == "summer" and getattr(piece, "light", False):
        ramps = [PAD_LIGHT]
    used = []
    for k, (dx, dy, r, notch) in enumerate(sorted(piece.pads, key=lambda p: p[1])):
        cols = ramps[B._hash(seed, k, 3) % len(ramps)]
        used.append(cols)
        draw_pad(lay, cx0 + dx * S, cy0 + dy * S, r, notch, cols, 0.55, look == "winter-snowy" and k % 2 == 0)
    d = ImageDraw.Draw(lay)
    flower = getattr(piece, "flower", None)
    if flower and look == "summer":
        draw_lily(d, cx0 + flower[0] * S, cy0 + flower[1] * S, flower[2])
    palette = [h for cols in used for h in cols] + RIPPLE + (B.SNOW if look == "winter-snowy" else []) + \
        (LILY_WHITE + LILY_GOLD if flower and look == "summer" else [])
    return lay, palette, True


# ---------------------------------------------------------------- vine, barrel

def paint_vine(piece, seed, look, gust):
    """A trailing stem lying on the water, small oval leaves along it."""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cols = PAD_RAMPS[look][0] if look != "autumn" else PAD_RED
    y0 = BASE_Y - 2.0 * S
    pts = [(2.0 * S + t * (W - 4.0 * S), y0 + math.sin(t * math.pi * 1.6 + 0.4) * 1.4 * S) for t in [k / 12 for k in range(13)]]
    d.line([(x, y + 0.7 * S) for x, y in pts], fill=B.rgb(RIPPLE[0]), width=int(1.0 * S))
    d.line(pts, fill=B.ramp(cols, 0.3), width=max(2, int(0.5 * S)))
    leaves = [(0.12, -1), (0.3, 1), (0.48, -1), (0.66, 1), (0.84, -1)]
    for k, (t, side) in enumerate(leaves):
        i = int(t * 12)
        x, y = pts[i]
        a = -math.pi / 2 * side + (0.5 if k % 2 else -0.5)
        lx, ly = x + math.cos(a) * 1.6 * S, y + math.sin(a) * 1.0 * S
        r = (1.9 if k in (1, 3) else 1.6) * S
        tone = 0.5 + (0.08 if k % 2 else -0.04)
        d.ellipse((lx - r, ly - r * 0.7 + 0.4 * S, lx + r, ly + r * 0.7 + 0.4 * S), fill=B.ramp(cols, tone - 0.35))
        d.ellipse((lx - r, ly - r * 0.7, lx + r, ly + r * 0.7), fill=B.ramp(cols, tone))
        d.chord((lx - r * 0.85, ly - r * 0.6, lx + r * 0.85, ly + r * 0.6), 160, 300, fill=B.ramp(cols, tone + 0.22))
        if look == "winter-snowy" and k % 2 == 0:
            d.ellipse((lx - r * 0.6, ly - r * 0.5, lx + r * 0.4, ly + r * 0.15), fill=B.rgb(B.SNOW[2]))
    palette = cols + RIPPLE + (B.SNOW if look == "winter-snowy" else [])
    return lay, palette, True


def paint_barrel(piece, seed, look, gust):
    """An oak barrel sunk to its middle: the lid, two hoops and the staves
    above the water, a ripple ring where it goes under."""
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cx, wl = W * 0.5, BASE_Y - 1.2 * S       # waterline
    rx, ry = 3.8 * S, 1.8 * S                # the lid
    top = wl - 5.0 * S
    ripple_ring(d, cx, wl, rx + 1.2 * S, ry + 0.6 * S, front=False)
    # staves: a band of vertical planks, rounded at the waterline
    d.rectangle((cx - rx, top, cx + rx, wl), fill=B.ramp(OAK, 0.45))
    d.ellipse((cx - rx, wl - ry, cx + rx, wl + ry), fill=B.ramp(OAK, 0.4))
    for k in range(-3, 4):
        x = cx + k * rx / 3.6
        d.line([(x, top + ry * 0.6), (x, wl + ry * 0.7 * math.cos(k / 3.6 * math.pi / 2))], fill=B.ramp(OAK, 0.22), width=max(1, int(0.35 * S)))
    d.rectangle((cx - rx, top, cx - rx * 0.55, wl), fill=B.ramp(OAK, 0.62))                 # lit left
    d.rectangle((cx + rx * 0.6, top, cx + rx, wl), fill=B.ramp(OAK, 0.25))                  # shaded right
    for hy in (top + 1.4 * S, wl - 1.1 * S):
        d.line([(cx - rx, hy), (cx + rx, hy)], fill=B.ramp(IRON, 0.35), width=int(0.8 * S))
        d.line([(cx - rx, hy - 0.3 * S), (cx - rx * 0.4, hy - 0.3 * S)], fill=B.ramp(IRON, 0.85), width=max(1, int(0.3 * S)))
    # the lid: an ellipse of boards inside a rim
    d.ellipse((cx - rx, top - ry, cx + rx, top + ry), fill=B.ramp(IRON, 0.3))
    d.ellipse((cx - rx * 0.82, top - ry * 0.75, cx + rx * 0.82, top + ry * 0.75), fill=B.ramp(OAK, 0.85))
    for k in (-1, 1):
        d.line([(cx + k * rx * 0.3, top - ry * 0.75), (cx + k * rx * 0.3, top + ry * 0.75)], fill=B.ramp(OAK, 0.5), width=max(1, int(0.3 * S)))
    ripple_ring(d, cx, wl, rx + 1.2 * S, ry + 0.6 * S, front=True)
    return lay, OAK + IRON + RIPPLE, True


PAINTERS = {"reeds": paint_reeds, "pads": paint_pads, "vine": paint_vine, "barrel": paint_barrel}


# ---------------------------------------------------------------- to pixels

def shrink(lay, palette, outline):
    """To frame size: area average, every covered pixel snapped to the look's
    own colours, a faint outline along the down-facing edge of floating
    pieces, and a shadow on the water one pixel down and right."""
    small = lay.resize((FW, FH), Image.BOX)
    sp = small.load()
    pal = [B.rgb(h)[:3] for h in palette]
    covered = {(x, y): sp[x, y][:3] for y in range(FH) for x in range(FW) if sp[x, y][3] >= 128}
    out = Image.new("RGBA", (FW, FH), (0, 0, 0, 0))
    op = out.load()
    if outline:
        for (x, y) in covered:
            sx, sy = x + 1, y + 1
            if 0 < sx < FW - 1 and sy < FH and (sx, sy) not in covered:
                op[sx, sy] = B.SHADOW
    for (x, y), c in covered.items():
        snapped = B.nearest(c, pal)
        if outline and (x, y + 1) not in covered and snapped not in [B.rgb(h)[:3] for h in RIPPLE]:
            snapped = B.mix(snapped, B.OUTLINE, 0.4)[:3]
        op[x, y] = snapped + (255,)
    return out


def look_frames(piece, seed, look):
    paint = PAINTERS[piece.painter]
    lay, palette, outline = paint(piece, seed, look, None)
    still = shrink(lay, palette, outline)
    if not piece.animated:
        return [still]
    frames = [still] * B.FRAMES
    for i, (stir, lean) in enumerate(zip(B.PUFF, B.LEAN)):
        lay, palette, outline = paint(piece, seed, look, (i, stir, lean))
        frames[B.PUFF_AT + i] = shrink(lay, palette, outline)
    return frames


def frame_count(piece):
    return B.FRAMES if piece.animated else 1


def build_sheet(piece, seed):
    use_frame(*piece.frame)
    n = frame_count(piece)
    sheet = Image.new("RGBA", (FW * n, FH * len(piece.looks)), (0, 0, 0, 0))
    for row, look in enumerate(piece.looks):
        for col, frame in enumerate(look_frames(piece, seed, look)):
            sheet.paste(frame, (col * FW, row * FH))
    return sheet


def check(out_dir, names):
    """Every frame stays a pixel inside its frame on all four sides (a piece
    on the frame edge is cut off flat), holds some art, and the sheet is its
    size."""
    ok = True
    for name in names:
        piece = PIECES[name]
        use_frame(*piece.frame)
        n = frame_count(piece)
        for sheet_name, _ in seeds(piece, name):
            sheet = Image.open(os.path.join(out_dir, sheet_name + ".png")).convert("RGBA")
            want = (FW * n, FH * len(piece.looks))
            if sheet.size != want:
                print("%s: sheet is %dx%d, want %dx%d" % ((sheet_name,) + sheet.size + want))
                ok = False
                continue
            for r, look in enumerate(piece.looks):
                bad = []
                for col in range(n):
                    frame = sheet.crop((col * FW, r * FH, (col + 1) * FW, (r + 1) * FH))
                    px = frame.load()
                    edge = any(px[x, y][3] == 255 for y in range(FH) for x in (0, FW - 1)) or \
                        any(px[x, y][3] == 255 for x in range(FW) for y in (0, FH - 1))
                    empty = frame.getbbox() is None
                    if edge or empty:
                        bad.append(col)
                ok = ok and not bad
                print("%-16s %-13s %2d frame(s)%s" % (sheet_name, look, n, "" if not bad else "  <- on the frame edge or empty: %s" % bad))
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's water edge.")
    parser.add_argument("--piece", default=",".join(PIECES), help="comma-separated, of: " + ", ".join(PIECES))
    parser.add_argument("--out", default=OUT_DIR, help="where the sheets go (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheets instead of painting them")
    args = parser.parse_args()
    names = args.piece.split(",")
    for name in names:
        if name not in PIECES:
            parser.error("unknown piece %r (have %s)" % (name, ", ".join(PIECES)))
    if args.check:
        sys.exit(0 if check(args.out, names) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name in names:
        piece = PIECES[name]
        for sheet_name, seed in seeds(piece, name):
            build_sheet(piece, seed).save(os.path.join(args.out, sheet_name + ".png"))
            print("painted", sheet_name)


if __name__ == "__main__":
    main()
