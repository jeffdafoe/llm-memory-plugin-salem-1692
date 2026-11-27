"""Paints salem's market-stall close-ups (LLM-969): the stall a player sees
while their character stands at its counter, and the goods sheet every room
shares. Every pixel is drawn here, no pack art.

    python tools/interior-art/stalls.py
    python tools/interior-art/stalls.py --out DIR   # elsewhere (prototypes)
    python tools/interior-art/stalls.py --check     # a fresh paint, sizes, paths, the goods grid
    python tools/interior-art/stalls.py --preview OUT [--pack DIR]

Writes client/assets/interiors/:
  stall_<roof>.png              the stall behind the people, roof wood, tiled
                                or fancy, 128 x 112 room px
  stall_<roof>_<trade>.png      the stall dressed for a trade (the smithy: 192
                                wide, a work area with the forge, the bellows
                                and the anvil left of the counter)
  stall_counter_<roof>.png      the counter across the front, drawn over the
  stall_counter_<roof>_<trade>.png  keeper and under the customers
  stall_smithy_bellows.png      the forge's bellows, open and pressed, side by
                                side; drawn over the smithy at the place
                                StallScenes.TRADES gives it
  stall_goods.png               16 x 32 room px cells, two per good in GOODS
                                order: the good itself, then a pile of it

The scenes and counters are painted at density 2 (stall_scenes.gd's scene
dicts say "density": 2), the goods at GOODS_DENSITY (StallScenes.
GOODS_DENSITY): the goods sheet is shared by every room that shows goods (a
stall's counter, the Inn's desk, the Tavern's bar), so it has a density of
its own, not a room's.

The close-up is the inside of the booths the map draws (tools/tree-art/
stalls.py), seen from the customers' side: an open booth with no back wall,
so the market shows through its back under the eave; the roof's underside
across the top (riven boards on Wood, cedar shingles on Tiled and Fancy);
the booth's own timber (grey-brown oak, brown oak, dressed pine); the
counter's front in that timber, or Spanish brown on the Fancy stall;
trodden earth under the booth and in front of it. Spots, slots and the
smithy's work places are StallScenes' (keeper feet y 66, floor piles
standing at y 62, the counter's goods at y 76, customers at y 106-109).
"""

import argparse
import importlib.util
import json
import math
import os
import random
import re
import sys

from PIL import Image, ImageDraw, ImageFilter

import common as C
import stall_goods as G

C.set_density(2)

B = C.B
ramp = C.ramp

# The booths' own palettes, from the map art's painter. It is loaded under
# another name: this module is stalls.py too.
_spec = importlib.util.spec_from_file_location("booth_art", os.path.join(C.ROOT, "tools", "tree-art", "stalls.py"))
BOOTH = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(BOOTH)

STALL_GD = os.path.join(C.ROOT, "client", "scripts", "stall_scenes.gd")
ROOFS = ("wood", "tiled", "fancy")


def res(name):
    return "res://assets/interiors/%s.png" % name


def spot(x, y, facing):
    return {"x": x, "y": y, "facing": facing}


# ---------------------------------------------------------------- the geometry
# Every stall scene's layout, in room px (spots are where the feet go),
# declared once: the sheets are painted from it, and --check requires
# stall_scenes.gd's constants to say exactly this.
DENSITY = 2
SIZE = (128, 112)
SCENES = {"Market Stall (%s)" % k.capitalize(): res("stall_%s" % k) for k in ROOFS}
KEEPER = spot(64, 66, "south")
HANDS = [spot(30, 66, "south"), spot(98, 66, "south")]
DOOR = spot(64, 108, "north")
SPOTS = [spot(18, 106, "north"), spot(110, 106, "north"), spot(40, 109, "north"), spot(88, 109, "north")]
FLOOR_SLOTS = [2, 18, 34, 78, 94, 110]
FLOOR_Y = 62                    # where the floor piles stand
COUNTER_SLOTS = [4, 20, 36, 76, 92, 108]
COUNTER_Y = 76                  # where the goods on the counter stand
FEW_STEP = (4, 1)
BELLOWS_FRAME = (32, 28)        # one of stall_smithy_bellows.png's two frames
TRADES = {
    "smithy": {
        "size": (192, 112),
        "bellows": spot(57, 57, "north"),
        "anvil": spot(36, 84, "west"),
        "fire": (1, 32, 26, 19),
        "bellows_art": {"sheet": res("stall_smithy_bellows"), "x": 30, "y": 24},
        "counter_from": 64,
        "keeper": spot(128, 66, "south"),
        "hands": [spot(44, 66, "south"), spot(162, 66, "south")],
        "door": spot(128, 108, "north"),
        "spots": [spot(82, 106, "north"), spot(174, 106, "north"), spot(104, 109, "north"), spot(152, 109, "north")],
        "floor_slots": [66, 82, 98, 142, 158, 174],
        "counter_slots": [68, 84, 100, 140, 156, 172],
    },
}
W, H = SIZE
SMITHY = TRADES["smithy"]
SMITHY_W = SMITHY["size"][0]
FIRE = SMITHY["fire"]
BELLOWS_AT = (SMITHY["bellows_art"]["x"], SMITHY["bellows_art"]["y"])
W, H = 128, 112

# Trodden earth and the grass at the market's edges; the far side of the
# market seen through the booth's open back.
EARTH = ["241a12", "34281c", "463626", "5a4630", "6e583c", "846c4a", "9a825c", "b09a70"]
GRASS = ["222c16", "30401e", "425628", "566c32", "6c823e", "86984e"]
HAZE = ["4c4a3e", "666252", "807a66", "9a947c", "b4ae94"]
WATER = ["1e2a2c", "2e4044", "46585a", "6a7c78"]
# The Fancy stall's counter front: the booth's Spanish brown (red ochre)
# seen in the booth's shade, earthier.
OCHRE = ["321e18", "462a22", "5a372c", "6a4537", "7a5444"]
# The bellows' leather, black with oil and soot, its folds catching light.
LEATHER = ["1c1410", "2e2218", "463426", "62503c", "7e6a52"]


def wood_of(kind):
    return BOOTH.WOODS[kind]


def roof_of(kind):
    return BOOTH.ROOFS[kind]


def scene_palette(kind):
    return (wood_of(kind) + roof_of(kind) + OCHRE + BOOTH.LIME + EARTH + GRASS + HAZE + WATER + C.STONE
            + C.MORTAR + C.SOOT + C.IRON + C.FIRE + C.STRAW + C.HERB + C.LINEN + C.REDWARE + C.PEWTER + C.OAK
            + C.TIMBER + C.MADDER + C.INDIGO)


def ring(room, x0, y0, x1, y1, col, w=0.5):
    """An oval outline, w room px thick."""
    room.d.ellipse((x0 * C.S, y0 * C.S, x1 * C.S - 1, y1 * C.S - 1), outline=col, width=max(1, int(w * C.S)))


def arc(room, x0, y0, x1, y1, start, end, col, w=0.5):
    """Part of an oval outline, from start to end degrees (clockwise from
    3 o'clock)."""
    room.d.arc((x0 * C.S, y0 * C.S, x1 * C.S - 1, y1 * C.S - 1), start, end, fill=col, width=max(1, int(w * C.S)))


# ---------------------------------------------------------------- the booth

def beam(room, x0, y0, x1, y1, wood, tone, rnd, dressed=False):
    """A squared timber in the booth's wood: lit top and left edges, shaded
    lower and right; grain along it, adze scoops unless it is dressed (the
    Fancy stall's planed pine), a check."""
    room.rect(x0, y0, x1, y1, ramp(wood, tone))
    horizontal = (x1 - x0) > (y1 - y0)
    length = max(x1 - x0, y1 - y0)
    for _ in range(int(length / 2.5)):
        t = tone + rnd.choice((-0.12, -0.07, 0.08))
        if horizontal:
            gy, gx = rnd.uniform(y0 + 0.5, y1 - 0.5), rnd.uniform(x0, x1 - 3)
            room.rect(gx, gy, gx + rnd.uniform(3, 12), gy + 0.25, ramp(wood, t))
        else:
            gx, gy = rnd.uniform(x0 + 0.5, x1 - 0.5), rnd.uniform(y0, y1 - 3)
            room.rect(gx, gy, gx + 0.25, gy + rnd.uniform(3, 12), ramp(wood, t))
    if not dressed:
        for _ in range(int(length / 6)):
            if horizontal:
                ax = rnd.uniform(x0 + 1, x1 - 2)
                room.line([(ax, y0 + 0.7), (ax + 0.8, y1 - 0.7)], ramp(wood, tone - 0.14), 0.3)
            else:
                ay = rnd.uniform(y0 + 1, y1 - 2)
                room.line([(x0 + 0.7, ay), (x1 - 0.7, ay + 0.8)], ramp(wood, tone - 0.14), 0.3)
    if length > 14:
        if horizontal:
            cy, cx = (y0 + y1) / 2 + rnd.uniform(-0.5, 0.5), rnd.uniform(x0, max(x0 + 1, x1 - 14))
            room.rect(cx, cy, cx + rnd.uniform(6, 14), cy + 0.3, ramp(wood, 0.04))
        else:
            cx, cy = (x0 + x1) / 2 + rnd.uniform(-0.3, 0.3), rnd.uniform(y0, max(y0 + 1, y1 - 14))
            room.rect(cx, cy, cx + 0.3, cy + rnd.uniform(6, 14), ramp(wood, 0.04))
    room.rect(x0, y0, x1, min(y1, y0 + 0.6), ramp(wood, tone + 0.34))
    room.rect(x0, y0, min(x1, x0 + 0.6), y1, ramp(wood, tone + 0.26))
    room.rect(x0, max(y0, y1 - 0.6), x1, y1, ramp(wood, tone - 0.3))
    room.rect(max(x0, x1 - 0.6), y0, x1, y1, ramp(wood, tone - 0.22))


def fieldstone(room, x0, y0, x1, y1, rnd, tone=0.42, size=(3.5, 6.5)):
    """Rough fieldstone laid in courses: irregular rounded stones, each lit on
    its upper left and shaded on its lower right, dark mortar between."""
    room.rect(x0, y0, x1, y1, ramp(C.MORTAR, 0.3))
    y = y1
    while y > y0 + 0.3:
        h = rnd.uniform(size[0] * 0.7, size[0] * 1.15)
        t0 = max(y0, y - h)
        x = x0 - rnd.uniform(0, size[1])
        while x < x1:
            sw = rnd.uniform(*size)
            a, b = max(x0, x + 0.25), min(x1, x + sw - 0.25)
            if b - a > 0.8:
                t = tone + rnd.uniform(-0.14, 0.14)
                cx, cy = (a + b) / 2, (t0 + y) / 2
                pts = []
                for k in range(10):
                    ang = 2 * math.pi * k / 10
                    rx, ry = (b - a) / 2 * rnd.uniform(0.86, 1.0), (y - t0) / 2 * rnd.uniform(0.84, 1.0)
                    pts.append((cx + math.cos(ang) * rx, cy + math.sin(ang) * ry))
                room.poly(pts, ramp(C.STONE, t))
                # Lit upper left (the points from 9 o'clock round to 12),
                # shaded lower right.
                room.poly([(cx + (px - cx) * 0.8 - 0.35, cy + (py - cy) * 0.8 - 0.35) for px, py in pts[4:9]] + [(cx - 0.3, cy - 0.3)],
                          ramp(C.STONE, t + 0.16))
                room.poly([(cx + (px - cx) * 0.86 + 0.35, cy + (py - cy) * 0.86 + 0.35) for px, py in pts[9:] + pts[:4]] + [(cx, cy)],
                          ramp(C.STONE, t - 0.16))
                room.speckle(a + 0.4, t0 + 0.5, b - 0.4, y - 0.5, (b - a) * (y - t0) / 3, C.STONE, (t - 0.12, t + 0.1), rnd,
                             (0.25, 0.5))
            x += sw
        y = t0


def market_beyond(room, x0, x1, top, bottom, rnd):
    """The market through the booth's open back, kept quieter than the
    keeper and the goods: the clapboard flank of a house across the lane in
    the haze, one door and two shuttered windows; a split-rail fence of a
    few wide posts and grass before it; the lane coming up to the booth."""
    house_bot = top + 13
    room.rect(x0, top, x1, house_bot, ramp(HAZE, 0.34))
    y = top + 0.6
    while y < house_bot - 2.0:
        room.rect(x0, y, x1, y + 0.4, ramp(HAZE, 0.46))
        room.rect(x0, y + 1.8, x1, y + 2.2, ramp(HAZE, 0.24))
        y += 2.2
    room.rect(x0, house_bot - 1.4, x1, house_bot, ramp(HAZE, 0.18))
    # One door and two windows, spread along the house, each a plain
    # dark shape in a light frame.
    w = x1 - x0
    door = x0 + w * 0.62
    room.rect(door - 0.8, top + 1.2, door + 7.8, house_bot - 1.4, ramp(HAZE, 0.56))
    room.rect(door, top + 2.0, door + 7.0, house_bot - 1.4, ramp(HAZE, 0.1))
    room.rect(door + 3.3, top + 2.0, door + 3.7, house_bot - 1.4, ramp(HAZE, 0.2))
    for wx in (x0 + w * 0.2, x0 + w * 0.86):
        room.rect(wx - 0.8, top + 2.2, wx + 8.8, top + 9.0, ramp(HAZE, 0.56))
        room.rect(wx, top + 3.0, wx + 8.0, top + 8.2, ramp(HAZE, 0.08))
        room.rect(wx + 3.8, top + 3.0, wx + 4.2, top + 8.2, ramp(HAZE, 0.3))
        room.rect(wx + 9.2, top + 2.2, wx + 13.0, top + 9.0, ramp(HAZE, 0.4))
    # Grass at its foot, the fence, the lane.
    gy = house_bot
    room.rect(x0, gy, x1, gy + 6, ramp(GRASS, 0.42))
    room.speckle(x0, gy, x1, gy + 6, (x1 - x0), GRASS, (0.3, 0.55), rnd, (0.4, 0.9))
    for k, ry in enumerate((gy + 1.0, gy + 3.4)):
        room.rect(x0, ry, x1, ry + 1.0, ramp(HAZE, 0.3))
        room.rect(x0, ry, x1, ry + 0.35, ramp(HAZE, 0.5))
    for sx in range(int(x0) + 6, int(x1), 16):
        room.rect(sx, gy - 1.0, sx + 1.6, gy + 6.0, ramp(HAZE, 0.22))
        room.rect(sx, gy - 1.0, sx + 0.5, gy + 6.0, ramp(HAZE, 0.42))
    ey = gy + 6
    for y in range(int(ey * 4), int(bottom * 4)):
        yy = y / 4
        room.rect(x0, yy, x1, yy + 0.25, ramp(EARTH, 0.7 - 0.08 * (yy - ey) / max(1, bottom - ey)))
    room.speckle(x0, ey, x1, bottom, (x1 - x0) * (bottom - ey) / 8, EARTH, (0.6, 0.78), rnd)


def roof_underside(room, kind, x0, x1, rnd):
    """The booth's roof seen from under its front eave, in shade: a quiet
    dark field of the roof's underside (riven boards along the rafters on
    Wood, a chink of daylight; shingles on laths across on Tiled and Fancy),
    a few broad rafters running back (converging a little: they recede),
    and the main timbers lighter than the field: the front eave plate under
    the eave's edge, the ridge beam, the back eave plate at y 18-22."""
    wood, roof = wood_of(kind), roof_of(kind)
    dressed = kind == "fancy"
    vx = (x0 + x1) / 2
    top, bot = 6.0, 18.4

    def at(x, y):
        f = (y - top) / (bot - top)
        return vx + (x - vx) * (1 - 0.16 * f), y

    room.rect(x0, 0, x1, 22, ramp(roof, 0.1))
    for y0, y1, tone in ((top, 10.4, 0.2), (13.2, bot, 0.12)):
        if kind == "wood":
            x = x0 - 30 - rnd.uniform(0, 3)
            while x < x1 + 30:
                bw = rnd.uniform(3.5, 5.5)
                t = tone + rnd.uniform(-0.03, 0.04)
                p = [at(x + 0.2, y0), at(x + bw - 0.2, y0), at(x + bw - 0.2, y1), at(x + 0.2, y1)]
                room.poly(p, ramp(roof, t))
                if rnd.random() < 0.06:
                    ly = rnd.uniform(y0 + 0.5, y1 - 1)
                    lx, _ = at(x + bw - 0.3, ly)
                    room.rect(lx, ly, lx + 0.5, ly + 0.5, ramp(C.DAYLIGHT, 0.4))
                x += bw
        else:
            y = y0
            while y < y1:
                gap = 1.9 - 0.6 * (y - top) / (bot - top)
                room.rect(x0, y, x1, y + gap, ramp(roof, tone + rnd.uniform(-0.02, 0.03)))
                room.rect(x0, y + gap - 0.45, x1, y + gap, ramp(roof, tone - 0.08))
                y += gap
    # A few broad rafters, lighter than the field.
    for rx in range(int(x0) + 14, int(x1), 34):
        a, b = at(rx, top), at(rx, bot)
        room.poly([a, (a[0] + 3.0, a[1]), (b[0] + 2.6, b[1]), b], ramp(wood, 0.36))
        room.line([a, b], ramp(wood, 0.56), 0.6)
        room.line([(a[0] + 3.0, a[1]), (b[0] + 2.6, b[1])], ramp(wood, 0.14), 0.4)
    beam(room, x0, 10.4, x1, 13.2, wood, 0.42, rnd, dressed)
    # The front eave's edge, lit from the front, then its plate.
    x = x0 - rnd.uniform(0, 3)
    while x < x1:
        if kind == "wood":
            bw, drop = rnd.uniform(3.5, 5.5), rnd.uniform(1.6, 2.4)
        else:
            bw, drop = rnd.uniform(2.6, 4.0), rnd.uniform(1.8, 2.3)
        t = 0.62 + rnd.uniform(-0.08, 0.08)
        room.rect(x + 0.2, 0, x + bw - 0.2, drop, ramp(roof, t))
        room.rect(x + 0.2, drop - 0.5, x + bw - 0.2, drop, ramp(roof, 0.25))
        x += bw
    beam(room, x0, 2.0, x1, 6.0, wood, 0.52, rnd, dressed)
    beam(room, x0, 18.4, x1, 22.4, wood, 0.48, rnd, dressed)


def gable(room, kind, x, rnd):
    """The booth's gable end at the right, seen from inside and edge on in
    the corner between the back post (x) and the front post: boards (Wood,
    Tiled) or lime-washed (Fancy), under the roof's slope."""
    wood = wood_of(kind)
    pts = [(x, 22.4), (x, 12), (x + 6.4, 2.0), (x + 6.4, 22.4)]
    if kind == "fancy":
        room.poly(pts, ramp(BOOTH.LIME, 0.62))
        room.speckle(x, 3, x + 6.4, 22, 18, BOOTH.LIME, (0.4, 0.8), rnd, (0.3, 0.6))
        room.line([(x + 3.2, 7.0), (x + 3.2, 22.4)], ramp(wood, 0.36), 0.9)
    else:
        room.poly(pts, ramp(wood, 0.3))
        for k in range(1, 4):
            bx = x + k * 1.6
            room.line([(bx, 22.4), (bx, 12 - k * 2.6)], ramp(wood, 0.16), 0.3)
    room.line([(x, 12), (x + 6.4, 2.0)], ramp(wood, 0.5), 0.9)


def back_post(room, kind, x, rnd, top=19, foot=47):
    """A back post, standing clear of the market behind it: a dark edge down
    each side, the post lit on its left."""
    wood = wood_of(kind)
    room.rect(x - 0.5, top, x + 4.5, foot, ramp(wood, 0.04))
    beam(room, x, top, x + 4.0, foot, wood, 0.5, rnd, kind == "fancy")
    room.rect(x - 0.5, foot - 0.6, x + 4.5, foot + 0.4, ramp(EARTH, 0.1))


def knee_brace(room, kind, x, side, rnd):
    """A brace from a back post up to the back plate."""
    wood = wood_of(kind)
    sx = x + (4.0 if side > 0 else 0)
    pts = [(sx, 30.4), (sx + side * 8.6, 22.4), (sx + side * 11.0, 22.4), (sx, 33.0)]
    room.poly([(px + side * 0.5, py + 0.5) for px, py in pts], ramp(wood, 0.04))
    room.poly(pts, ramp(wood, 0.44))
    room.line([(sx, 30.4), (sx + side * 8.6, 22.4)], ramp(wood, 0.68), 0.5)


def back_rail(room, kind, x0, x1, y, rnd):
    wood = wood_of(kind)
    beam(room, x0, y, x1, y + 1.8, wood, 0.48, rnd, kind == "fancy")


def booth_floor(room, x0, x1, top, bottom, rnd, cinders=False):
    """The trodden earth under the booth, in the roof's shade: darker toward
    the back, scuffed paler where the keeper walks, straw (or a smith's
    cinders and scale) trodden into it."""
    for y in range(int(top * 4), int(bottom * 4)):
        yy = y / 4
        f = (yy - top) / max(1, bottom - top)
        room.rect(x0, yy, x1, yy + 0.25, ramp(EARTH, 0.36 + 0.1 * f))
    # Soft scuffs rather than grit: broad patches a shade either way.
    for _ in range(int((x1 - x0) * (bottom - top) / 40)):
        px, py = rnd.uniform(x0 - 2, x1), rnd.uniform(top, bottom)
        room.ellipse(px, py, px + rnd.uniform(3, 7), py + rnd.uniform(1.0, 2.0), ramp(EARTH, rnd.choice((0.3, 0.42, 0.46))))
    if cinders:
        for _ in range(int((x1 - x0) * (bottom - top) / 30)):
            px, py = rnd.uniform(x0, x1), rnd.uniform(top, bottom)
            room.ellipse(px, py, px + rnd.uniform(1.2, 3), py + rnd.uniform(0.5, 1.0), ramp(EARTH, rnd.choice((0.14, 0.22))))
    else:
        for _ in range(int((x1 - x0) * (bottom - top) / 90)):
            px, py = rnd.uniform(x0, x1), rnd.uniform(top + 2, bottom)
            a = rnd.uniform(-0.6, 0.6)
            ln = rnd.uniform(1.4, 3.2)
            room.line([(px, py), (px + math.cos(a) * ln, py + math.sin(a) * ln * 0.4)],
                      ramp(C.STRAW, rnd.uniform(0.35, 0.75)), 0.3)


def front_ground(room, x0, x1, top, bottom, rnd):
    """The trodden earth in front of the counter where the customers stand,
    in daylight: paler, scuffed, stones and a few tufts of grass at the
    edges."""
    for y in range(int(top * 4), int(bottom * 4)):
        yy = y / 4
        f = (yy - top) / max(1, bottom - top)
        room.rect(x0, yy, x1, yy + 0.25, ramp(EARTH, 0.6 + 0.08 * f))
    for _ in range(int((x1 - x0) * (bottom - top) / 30)):
        px, py = rnd.uniform(x0 - 2, x1), rnd.uniform(top, bottom)
        room.ellipse(px, py, px + rnd.uniform(3, 8), py + rnd.uniform(1.0, 2.2), ramp(EARTH, rnd.choice((0.54, 0.7, 0.74))))
    room.speckle(x0, top, x1, bottom, (x1 - x0) * (bottom - top) / 24, EARTH, (0.56, 0.66), rnd, (0.4, 0.8))
    for _ in range(int((x1 - x0) / 16)):
        px, py = rnd.uniform(x0, x1), rnd.uniform(top + 3, bottom - 1)
        room.ellipse(px, py, px + rnd.uniform(1.4, 2.6), py + rnd.uniform(0.8, 1.4), ramp(C.STONE, rnd.uniform(0.4, 0.55)))
        room.rect(px + 0.3, py + 0.1, px + 1.2, py + 0.5, ramp(C.STONE, 0.62))
    # Ruts and footprints, scuffed darker.
    for _ in range(int((x1 - x0) / 10)):
        px, py = rnd.uniform(x0, x1), rnd.uniform(top + 4, bottom - 2)
        room.ellipse(px, py, px + 2.4, py + 1.0, ramp(EARTH, 0.46))
    for gx in list(range(int(x0), int(x0) + 14, 2)) + list(range(int(x1) - 14, int(x1), 2)):
        if rnd.random() < 0.6:
            gy = rnd.uniform(top + 6, bottom)
            for _k in range(3):
                a = rnd.uniform(-0.5, 0.5)
                room.line([(gx, gy), (gx + math.sin(a) * 2.5, gy - math.cos(a) * 3)],
                          ramp(GRASS, rnd.uniform(0.4, 0.9)), 0.4)


def hanging(room, x, y, what, rnd):
    """Something hung from a peg on the back rail at (x, y)."""
    room.rect(x - 0.4, y - 0.6, x + 0.4, y + 1.0, ramp(C.OAK, 0.7))
    if what == "basket":
        room.line([(x - 2.6, y + 3.2), (x, y + 0.6), (x + 2.6, y + 3.2)], ramp(C.STRAW, 0.6), 0.45)
        room.ellipse(x - 3.4, y + 2.6, x + 3.4, y + 8.6, ramp(C.STRAW, 0.48))
        for k in range(4):
            room.rect(x - 3.0, y + 3.6 + k * 1.2, x + 3.0, y + 3.9 + k * 1.2, ramp(C.STRAW, 0.25))
        room.rect(x - 3.4, y + 2.6, x + 3.4, y + 3.2, ramp(C.STRAW, 0.8))
    elif what == "rope":
        for k in range(4):
            ring(room, x - 2.6 + k * 0.2, y + 0.6 + k * 0.25, x + 2.6 - k * 0.2, y + 7.6 - k * 0.15,
                 ramp(C.STRAW, 0.62 - k * 0.08), 0.5)
        room.line([(x + 1.4, y + 7.0), (x + 1.8, y + 10.0)], ramp(C.STRAW, 0.45), 0.4)
    elif what == "herbs":
        room.line([(x, y + 0.6), (x, y + 2.6)], ramp(C.STRAW, 0.4), 0.3)
        tone = rnd.uniform(0.35, 0.65)
        for _k in range(8):
            a = math.radians(rnd.uniform(-30, 30))
            ln = rnd.uniform(3, 6)
            room.line([(x, y + 2.6), (x + math.sin(a) * ln, y + 2.6 + math.cos(a) * ln)],
                      ramp(C.HERB, tone + rnd.uniform(-0.2, 0.2)), 0.5)
    elif what == "scale":
        # A balance: a cord to the beam, the iron arm, two brass pans on
        # cords.
        room.line([(x, y + 0.6), (x, y + 5.0)], ramp(C.STRAW, 0.4), 0.3)
        room.rect(x - 0.7, y + 4.6, x + 0.7, y + 6.0, ramp(C.IRON, 0.5))
        room.rect(x - 7.0, y + 5.4, x + 7.0, y + 6.1, ramp(C.IRON, 0.42))
        room.rect(x - 7.0, y + 5.4, x + 7.0, y + 5.7, ramp(C.IRON, 0.7))
        for px in (x - 6.4, x + 6.4):
            room.line([(px, y + 6.0), (px - 2.2, y + 11.6)], ramp(C.STRAW, 0.35), 0.25)
            room.line([(px, y + 6.0), (px + 2.2, y + 11.6)], ramp(C.STRAW, 0.35), 0.25)
            room.ellipse(px - 3.0, y + 11.0, px + 3.0, y + 13.0, ramp(C.STRAW, 0.72))
            room.rect(px - 2.6, y + 11.0, px + 2.6, y + 11.5, ramp(C.STRAW, 0.95))
    elif what == "lantern":
        # A pierced tin lantern on a hook, unlit by day.
        room.line([(x, y + 0.6), (x, y + 3.0)], ramp(C.IRON, 0.4), 0.3)
        room.ellipse(x - 1.0, y + 2.4, x + 1.0, y + 3.8, ramp(C.PEWTER, 0.5))
        room.poly([(x - 2.0, y + 5.0), (x, y + 3.4), (x + 2.0, y + 5.0)], ramp(C.PEWTER, 0.6))
        room.rect(x - 2.0, y + 5.0, x + 2.0, y + 10.4, ramp(C.PEWTER, 0.45))
        room.rect(x - 2.0, y + 5.0, x - 1.3, y + 10.4, ramp(C.PEWTER, 0.75))
        for k in range(6):
            room.rect(x - 1.0 + (k % 2) * 1.2, y + 5.8 + (k // 2) * 1.5, x - 0.6 + (k % 2) * 1.2, y + 6.2 + (k // 2) * 1.5,
                      ramp(C.PEWTER, 0.15))
        room.rect(x - 2.4, y + 10.4, x + 2.4, y + 11.2, ramp(C.PEWTER, 0.3))
    elif what in ("onions", "apples"):
        # A string of onions plaited by their tops, or dried apple rings on
        # a cord.
        room.line([(x, y), (x + 0.4, y + 13.0)], ramp(C.STRAW, 0.45), 0.35)
        for k in range(7 if what == "onions" else 9):
            oy = y + 2.0 + k * (1.5 if what == "onions" else 1.2)
            ox = x + (0.9 if k % 2 else -0.9) * (what == "onions")
            if what == "onions":
                room.ellipse(ox - 1.4, oy, ox + 1.4, oy + 2.2, ramp(C.REDWARE, 0.62 + rnd.uniform(-0.1, 0.1)))
                room.ellipse(ox - 0.9, oy + 0.3, ox - 0.1, oy + 1.2, ramp(C.STRAW, 0.85))
            else:
                room.ellipse(x - 1.0, oy, x + 1.4, oy + 1.1, ramp(C.STRAW, 0.7 + rnd.uniform(-0.1, 0.1)))
                room.rect(x - 0.1, oy + 0.3, x + 0.5, oy + 0.7, ramp(C.REDWARE, 0.4))
    elif what == "tongs":
        room.line([(x - 0.4, y + 0.6), (x - 1.6, y + 9.6)], ramp(C.IRON, 0.5), 0.45)
        room.line([(x + 0.4, y + 0.6), (x + 1.6, y + 9.6)], ramp(C.IRON, 0.35), 0.45)
        room.ellipse(x - 0.9, y + 0.2, x + 0.9, y + 1.6, ramp(C.IRON, 0.6))
    elif what == "shoes":
        for k, dx in enumerate((-1.6, 1.6)):
            hx, hy = x + dx, y + 2.2 + k * 0.6
            arc(room, hx - 1.6, hy, hx + 1.6, hy + 4.4, 150, 390, ramp(C.IRON, 0.45), 0.7)
        room.line([(x, y + 0.6), (x - 1.6, y + 2.2)], ramp(C.STRAW, 0.4), 0.25)
        room.line([(x, y + 0.6), (x + 1.6, y + 2.8)], ramp(C.STRAW, 0.4), 0.25)


def counter_shadow(room, x0, x1, y):
    """The counter's shadow cast forward and right on the lit ground."""
    room.shadow(x0 + 2, y - 1, x1 + 4, y + 4, 170)


def booth(kind, w, seed, sell_from=0, rails=True):
    """The plain stall's backdrop, `w` wide, its selling side (the counter,
    the floor piles) from x `sell_from`; returns the canvas so a trade can
    dress it before the shrink. Under the roof is in shade, deepest at the
    back of the booth's floor; the market beyond and the ground in front of
    the counter are in daylight."""
    rnd = random.Random(seed)
    room = C.Room(w, H)
    market_beyond(room, 0, w, 22, 48, rnd)
    booth_floor(room, 0, w, 47, 92, rnd)
    front_ground(room, 0, w, 92, H, rnd)
    roof_underside(room, kind, 0, w, rnd)
    gable(room, kind, w - 6.4, rnd)
    # The back posts at the stall's ends, a brace at each, the sill along
    # the back where the booth's floor ends.
    for x, side in ((sell_from + 6.6, 1), (w - 10.0, -1)):
        knee_brace(room, kind, x, side, rnd)
        back_post(room, kind, x, rnd)
    beam(room, sell_from, 45.4, w, 47.4, wood_of(kind), 0.36, rnd, kind == "fancy")
    if rails:
        back_rail(room, kind, sell_from + 3, w - 3, 31, rnd)
    for y in range(47 * 4, 92 * 4):
        f = (y / 4 - 47) / 45
        room.sd.rectangle((0, y * C.S // 4, w * C.S, (y + 1) * C.S // 4), fill=int(170 - 90 * f))
    room.sd.rectangle((0, 0, w * C.S, 22 * C.S), fill=70)
    counter_shadow(room, sell_from, w, 93)
    return room, rnd


def dress_plain(room, kind, w, rnd, sell_from=0):
    """The plain stall's dressing, clear of the keeper (x 52-76) and above
    the floor piles: a balance hung from the ridge beam beside the keeper, a
    lantern from a rafter, strings of onions and dried apples from the front
    eave plate at the ends; a market basket, herbs and a coil of rope on the
    back rail by the posts; a besom leaning on the right back post."""
    hanging(room, sell_from + 44, 12.4, "scale", rnd)
    hanging(room, sell_from + 88, 12.4, "lantern", rnd)
    hanging(room, sell_from + 9, 5.6, "onions", rnd)
    hanging(room, sell_from + 14, 5.6, "apples", rnd)
    hanging(room, w - 12, 5.6, "onions", rnd)
    hanging(room, sell_from + 8, 32.6, "basket", rnd)
    hanging(room, sell_from + 16, 32.6, "herbs", rnd)
    hanging(room, w - 20, 32.6, "herbs", rnd)
    hanging(room, w - 10, 32.6, "rope", rnd)
    C.broom(room, w - 7, 47, 26)


def stall_sheet(kind):
    room, rnd = booth(kind, W, 9690 + ROOFS.index(kind))
    dress_plain(room, kind, W, rnd)
    return C.shrink(room, scene_palette(kind))


# ---------------------------------------------------------------- the smithy



def forge(room, rnd):
    """The smith's forge against the back at the left: a fieldstone hearth
    waist high, the fire on its bed in FIRE, a stone hood over it and the
    chimney rising through the roof; a quench tub before it."""
    fx, fy, fw, fh = FIRE
    top = fy + fh - 1
    # The chimney and hood: fieldstone from the roof down to the hearth
    # bed, sooted above the fire; the hood's mouth over the firebed.
    fieldstone(room, 0, 0, 30, top, rnd, 0.36)
    soot = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(soot).ellipse(((fx - 2) * C.S, (fy - 22) * C.S, (fx + fw + 2) * C.S, (fy + 4) * C.S), fill=150)
    room.lay.paste(Image.new("RGBA", room.lay.size, B.rgb(C.SOOT[1])), (0, 0),
                   soot.filter(ImageFilter.GaussianBlur(3 * C.S)))
    # The hood's mouth: a rim of big dressed stones round it, lighter than
    # the rubble, an iron lintel bar; inside, the sooted back warmed by the
    # fire's glow, dark toward the top.
    rim = [(fx - 1.6, top), (fx - 1.6, fy + 1.0), (fx + fw + 1.6, fy + 1.0), (fx + fw + 1.6, top)]
    room.poly(rim, ramp(C.MORTAR, 0.2))
    for k, (sx0, sy0, sx1, sy1) in enumerate(((fx - 1.4, fy + 3.6, fx + 1.2, fy + 9.6), (fx - 1.4, fy + 9.8, fx + 1.2, top),
                                              (fx + fw - 1.2, fy + 3.6, fx + fw + 1.4, fy + 9.6),
                                              (fx + fw - 1.2, fy + 9.8, fx + fw + 1.4, top))):
        room.d.rounded_rectangle((sx0 * C.S, sy0 * C.S, sx1 * C.S, sy1 * C.S), radius=int(0.6 * C.S), fill=ramp(C.STONE, 0.66))
        room.rect(sx0, sy0, sx1, sy0 + 0.5, ramp(C.STONE, 0.86))
        room.rect(sx1 - 0.5, sy0, sx1, sy1, ramp(C.STONE, 0.38))
    for k in range(6):
        t = k / 6
        room.rect(fx + 1.2, fy + 3.6 + (top - fy - 3.6) * t, fx + fw - 1.2, fy + 3.6 + (top - fy - 3.6) * (t + 1 / 6),
                  ramp(C.SOOT, 0.05) if t < 0.34 else ramp(C.FIRE, 0.02 + 0.2 * (t - 0.34)))
    room.rect(fx - 1.6, fy + 1.0, fx + fw + 1.6, fy + 3.6, ramp(C.IRON, 0.3))
    room.rect(fx - 1.6, fy + 1.0, fx + fw + 1.6, fy + 1.6, ramp(C.IRON, 0.6))
    # The hearth bed: a stone table under the fire, its front face to y 66.
    fieldstone(room, 0, top, 30, 66, rnd, 0.44, (3.0, 6.0))
    room.rect(0, top - 0.6, 30.6, top + 1.0, ramp(C.STONE, 0.66))
    room.rect(0, top - 0.6, 30.6, top - 0.1, ramp(C.STONE, 0.85))
    room.rect(0, top + 1.0, 30.6, top + 1.5, ramp(C.STONE, 0.16))
    # The firebed: a heap of coal and glowing coke in the hood's mouth.
    cx, cy = fx + fw / 2, fy + fh - 2.2
    room.ellipse(cx - 11, cy - 4.2, cx + 11, cy + 1.6, ramp(C.SOOT, 0.4))
    room.ellipse(cx - 7, cy - 3.6, cx + 7, cy + 0.6, ramp(C.FIRE, 0.3))
    room.ellipse(cx - 4.5, cy - 3.0, cx + 4.5, cy - 0.2, ramp(C.FIRE, 0.55))
    room.ellipse(cx - 2.2, cy - 2.4, cx + 2.4, cy - 0.8, ramp(C.FIRE, 0.82))
    room.speckle(cx - 10, cy - 3.6, cx + 10, cy + 0.8, 70, C.SOOT + C.FIRE, (0.05, 0.2, 0.55, 0.7), rnd, (0.3, 0.7))
    for k in range(3):
        px = cx - 4 + k * 4 + rnd.uniform(-1, 1)
        room.poly([(px - 1.2, cy - 2), (px + 1.2, cy - 2), (px + 0.3, cy - 6 - k % 2 * 2)], ramp(C.FIRE, 0.7))
        room.poly([(px - 0.5, cy - 2), (px + 0.5, cy - 2), (px + 0.1, cy - 4.2)], ramp(C.FIRE, 0.95))
    # A tuyere: the bellows' iron pipe into the fire from the right.
    room.rect(fx + fw - 2, cy - 3.0, 31, cy - 1.6, ramp(C.IRON, 0.35))
    room.rect(fx + fw - 2, cy - 3.0, 31, cy - 2.6, ramp(C.IRON, 0.6))
    # The quench tub, staved and hooped, at the hearth's foot.
    quench_tub(room, 3, 58, 17, 70, rnd)


def quench_tub(room, x0, y0, x1, y1, rnd):
    oak = C.OAK
    room.rect(x0, y0 + 1.6, x1, y1, ramp(oak, 0.42))
    for k in range(int((x1 - x0) / 1.8)):
        sx = x0 + k * 1.8
        room.rect(sx, y0 + 1.6, sx + 0.3, y1, ramp(oak, 0.26))
        room.rect(sx + 0.3, y0 + 1.6, sx + 0.9, y1, ramp(oak, 0.5 + rnd.uniform(-0.05, 0.05)))
    for hy in (y0 + 3.2, y1 - 2.4):
        room.rect(x0 - 0.2, hy, x1 + 0.2, hy + 0.9, ramp(C.IRON, 0.32))
        room.rect(x0 - 0.2, hy, x1 + 0.2, hy + 0.3, ramp(C.IRON, 0.6))
    room.ellipse(x0, y0, x1, y0 + 3.4, ramp(oak, 0.66))
    room.ellipse(x0 + 0.7, y0 + 0.5, x1 - 0.7, y0 + 2.9, ramp(WATER, 0.35))
    room.ellipse(x0 + 2, y0 + 0.9, x0 + 6, y0 + 1.7, ramp(WATER, 0.9))
    room.shadow(x0 + 1, y1 - 1, x1 + 3, y1 + 1.6, 200)


def anvil(room, rnd):
    """The anvil on its oak stump, its horn toward the keeper at
    StallScenes' anvil spot (36, 84) who hammers facing west."""
    # The stump: a section of trunk, its cut top under the anvil.
    sx0, sx1, top, foot = 13, 29, 79, 89
    room.shadow(sx0 + 1, foot - 2, sx1 + 5, foot + 2, 210)
    room.rect(sx0, top, sx1, foot, ramp(C.OAK, 0.4))
    for k in range(7):
        bx = sx0 + 0.8 + k * 2.2 + rnd.uniform(-0.3, 0.3)
        room.line([(bx, top + 1), (bx + rnd.uniform(-0.3, 0.3), foot - 0.3)], ramp(C.OAK, 0.24), 0.35)
    room.rect(sx0, top, sx0 + 1.2, foot, ramp(C.OAK, 0.6))
    room.rect(sx1 - 1.2, top, sx1, foot, ramp(C.OAK, 0.22))
    room.ellipse(sx0, top - 2.0, sx1, top + 2.0, ramp(C.OAK, 0.7))
    room.ellipse(sx0 + 2, top - 1.2, sx1 - 2, top + 1.2, ramp(C.OAK, 0.56))
    room.ellipse(sx0 + 5, top - 0.5, sx1 - 5, top + 0.5, ramp(C.OAK, 0.66))
    # The anvil: waist, face, horn to the right, heel to the left.
    room.poly([(15, top - 1.0), (27, top - 1.0), (25, top - 4.0), (17, top - 4.0)], ramp(C.IRON, 0.3))
    room.rect(17.4, top - 7.0, 24.6, top - 3.6, ramp(C.IRON, 0.28))
    room.poly([(11, top - 10.0), (27, top - 10.0), (33.6, top - 8.6), (27, top - 7.0), (12, top - 7.0)],
              ramp(C.IRON, 0.36))
    room.rect(11, top - 10.0, 27, top - 9.0, ramp(C.IRON, 0.85))
    room.poly([(27, top - 10.0), (33.6, top - 8.6), (27, top - 9.0)], ramp(C.IRON, 0.7))
    room.rect(11, top - 7.6, 27, top - 7.0, ramp(C.IRON, 0.12))
    room.rect(13, top - 9.6, 15, top - 9.2, ramp(C.IRON, 0.05))
    # A hammer resting on the stump, tongs leaning on it.
    room.rect(sx0 + 1, top - 0.6, sx0 + 4, top + 0.4, ramp(C.IRON, 0.4))
    room.line([(sx0 + 2.5, top), (sx0 + 7.5, top + 1.2)], ramp(C.OAK, 0.6), 0.5)
    room.line([(sx1 + 1.2, foot), (sx1 - 0.4, top - 2)], ramp(C.IRON, 0.4), 0.4)
    room.line([(sx1 + 2.2, foot), (sx1 + 0.2, top - 2)], ramp(C.IRON, 0.3), 0.4)


def bellows(room, lift, rnd):
    """The great bellows (frame-local room px, 32 x 28; the sheet draws them
    at bellows_art's x, y, under the beam they hang from): a wedge of two
    thick oak boards hinged at an iron-banded nozzle on the left, brown
    leather pleated between them, the bottom board hung from the beam on
    two ropes. `lift` is how far the top board's back end stands above the
    bottom board; the hand's rope hangs from that end to a toggle at the
    hand's reach (frame-local y 16, above StallScenes' bellows spot), so a
    pull down presses the bellows."""
    oak, iron = C.OAK, C.IRON
    hinge, back = 6.0, 30.0
    base = 23.6                        # the bottom board's top
    top_back = base - lift             # the top board's back end
    rope_x, toggle_y = 28.6, 16.4

    def top_at(x):
        return base - 1.6 - (base - 1.6 - top_back) * (x - hinge) / (back - hinge)

    # The ropes from the beam to the bottom board.
    for cx in (11.0, 25.0):
        room.line([(cx, 0), (cx, base + 0.6)], ramp(C.STRAW, 0.34), 0.7)
        room.line([(cx - 0.25, 0), (cx - 0.25, base + 0.6)], ramp(C.STRAW, 0.62), 0.25)
    # The leather: a wedge from the hinge, pleated in folds that fan out,
    # lit on its upper folds.
    pts = [(hinge, base)] + [(x, top_at(x) + 1.0) for x in [hinge + (back - hinge) * k / 12 for k in range(13)]] + [(back - 0.6, base)]
    # The bag bulges a little past the boards' back ends.
    pts = pts[:-1] + [(back + 1.4, top_at(back) + 2.0), (back + 1.6, (top_at(back) + base) / 2), (back + 1.2, base)]
    room.poly(pts, ramp(LEATHER, 0.5))
    for k in range(1, 4):
        f = k / 4
        fold = [(x, base - (base - top_at(x) - 1.0) * f) for x in [hinge + 1.5 + (back - hinge) * j / 10 for j in range(11)]]
        room.line(fold, ramp(LEATHER, 0.08), 0.7)
        room.line([(x, y - 0.7) for x, y in fold], ramp(LEATHER, 0.95), 0.5)
    # The bottom board: thick, its lit top edge.
    room.rect(hinge - 0.6, base, back + 0.8, base + 2.6, ramp(oak, 0.5))
    room.rect(hinge - 0.6, base, back + 0.8, base + 0.6, ramp(oak, 0.9))
    room.rect(hinge - 0.6, base + 2.0, back + 0.8, base + 2.6, ramp(oak, 0.16))
    room.rect(back + 0.2, base, back + 0.8, base + 2.6, ramp(oak, 0.3))
    # The top board: as thick, lit along its upper edge, iron-strapped.
    room.poly([(hinge - 0.6, base - 2.2), (back + 0.8, top_back - 1.6), (back + 0.8, top_back + 0.2), (hinge - 0.6, base - 0.4)],
              ramp(oak, 0.62))
    room.line([(hinge - 0.6, base - 2.2), (back + 0.8, top_back - 1.6)], ramp(oak, 0.95), 0.5)
    room.line([(hinge - 0.6, base - 0.5), (back + 0.8, top_back + 0.1)], ramp(oak, 0.2), 0.35)
    room.poly([(back + 0.2, top_back - 1.6), (back + 0.8, top_back - 1.6), (back + 0.8, top_back + 0.2), (back + 0.2, top_back + 0.2)],
              ramp(oak, 0.34))
    for sx in (hinge + 8, hinge + 16):
        room.line([(sx, top_at(sx) - 2.4), (sx, top_at(sx) + 1.0)], ramp(iron, 0.36), 0.8)
    # The rope from the top board's back end down to the toggle.
    room.line([(rope_x, top_back - 0.5), (rope_x, toggle_y)], ramp(C.STRAW, 0.4), 0.7)
    room.line([(rope_x - 0.25, top_back - 0.5), (rope_x - 0.25, toggle_y)], ramp(C.STRAW, 0.7), 0.25)
    room.rect(rope_x - 1.6, toggle_y, rope_x + 1.6, toggle_y + 1.4, ramp(oak, 0.6))
    room.rect(rope_x - 1.6, toggle_y, rope_x + 1.6, toggle_y + 0.5, ramp(oak, 0.92))
    # The hinge: an iron band round both boards' ends; the nozzle, a
    # wooden cone shod with iron, into the forge's tuyere.
    room.poly([(0.4, base - 2.4), (hinge - 1, base - 2.8), (hinge - 1, base + 2.4), (0.4, base - 0.4)], ramp(oak, 0.48))
    room.poly([(0.0, base - 2.2), (2.4, base - 2.4), (2.4, base - 0.2), (0.0, base - 0.6)], ramp(iron, 0.45))
    room.rect(hinge - 2.0, base - 3.6, hinge + 0.4, base + 2.8, ramp(iron, 0.3))
    room.rect(hinge - 2.0, base - 3.6, hinge - 1.2, base + 2.8, ramp(iron, 0.68))
    for ny in (base - 2.4, base + 1.4):
        room.rect(hinge - 0.8, ny, hinge - 0.2, ny + 0.6, ramp(iron, 0.85))


def bellows_sheet():
    """stall_smithy_bellows.png: open, then pressed, each frame painted
    guarded within its BELLOWS_FRAME rect. The frames are drawn over the
    smithy, which has no bellows of its own, so they are the bellows alone,
    transparent around them."""
    fw, fh = BELLOWS_FRAME
    frames = Image.new("RGBA", (fw * 2 * C.DENSITY, fh * C.DENSITY), (0, 0, 0, 0))
    for k, lift in enumerate((17.0, 9.0)):
        art, _c = G.guarded(fw, fh, lambda r, lift=lift: bellows(r, lift, random.Random(795)), C.PIECE_PALETTE + LEATHER,
                            "bellows frame %d" % k, outline=0.3, ring=0.15)
        frames.paste(art, (k * fw * C.DENSITY, 0))
    return frames


def dress_smithy(room, kind, rnd):
    """The smithy's work area left of the counter (x 0-64): the forge, the
    anvil, cinders on the floor; the bellows hang where the bellows sheet
    draws (not painted here); on the selling side's rail, the smith's
    tongs and horseshoes."""
    booth_floor(room, 0, 64, 47, 92, rnd, cinders=True)
    beam(room, 0, 45.4, 64, 47.4, wood_of(kind), 0.36, rnd, kind == "fancy")
    # The beam the bellows hang from, out from the selling side's post.
    wood = wood_of(kind)
    beam(room, 26, 22.4, 64, 24.4, wood, 0.44, rnd, kind == "fancy")
    forge(room, rnd)
    anvil(room, rnd)
    # A basket of charcoal by the forge, a sledge leaning on it.
    room.ellipse(31, 59, 41, 63, ramp(C.STRAW, 0.3))
    room.rect(31, 61, 41, 66, ramp(C.STRAW, 0.42))
    for k in range(5):
        room.rect(31.5 + k * 2, 61, 31.9 + k * 2, 66, ramp(C.STRAW, 0.22))
    room.speckle(31.5, 58.6, 40.5, 61.6, 30, C.SOOT + C.IRON, (0.1, 0.4, 0.7), rnd, (0.5, 1.0))
    room.shadow(32, 64, 44, 67, 200)
    room.line([(30.6, 66), (28.4, 54)], ramp(C.OAK, 0.6), 0.6)
    room.rect(26.6, 52.6, 30.6, 55.0, ramp(C.IRON, 0.35))
    hanging(room, 70, 32.6, "tongs", rnd)
    hanging(room, 78, 32.6, "shoes", rnd)
    hanging(room, 112, 12.4, "lantern", rnd)
    hanging(room, 184, 32.6, "tongs", rnd)
    hanging(room, 176, 32.6, "shoes", rnd)


def smithy_sheet(kind):
    room, rnd = booth(kind, SMITHY_W, 9790 + ROOFS.index(kind), sell_from=SMITHY["counter_from"])
    dress_smithy(room, kind, rnd)
    return C.shrink(room, scene_palette(kind))


# ---------------------------------------------------------------- the counter

def counter(kind, w, start, seed):
    """The counter across the booth's front from x `start`, `w` wide sheet:
    a thick top board seen from a little above (two planks, grain, worn
    paler along the middle) whose near edge is just below COUNTER_Y, where
    the goods on it stand; its lit front edge; the front face of upright
    boards down to the ground (ochre on the Fancy stall); the booth's front
    posts at its ends. Painted guarded: nothing may fall off the sheet."""
    def paint(room):
        rnd = random.Random(seed)
        wood = wood_of(kind)
        face = OCHRE if kind == "fancy" else wood
        dressed = kind == "fancy"
        top, split, edge = COUNTER_Y - 9.0, COUNTER_Y - 3.6, COUNTER_Y + 1.6
        front, foot = edge + 1.0, 93.0
        # The front face: upright boards.
        x = start
        while x < w:
            bw = rnd.uniform(4.5, 6.5) if not dressed else 5.5
            x1 = min(w, x + bw)
            t = 0.5 + rnd.uniform(-0.07, 0.07)
            room.rect(x, front, x1, foot, ramp(face, t))
            for _ in range(4):
                gx = rnd.uniform(x + 0.6, max(x + 0.7, x1 - 0.6))
                room.rect(gx, rnd.uniform(front + 0.4, front + 5.4), gx + 0.25, rnd.uniform(front + 7.4, foot), ramp(face, t - 0.1))
            room.rect(x, front, x + 0.5, foot, ramp(face, t + 0.16))
            room.rect(x1 - 0.4, front, x1, foot, ramp(face, 0.1))
            if rnd.random() < 0.35 and not dressed:
                kx, ky = (x + x1) / 2, rnd.uniform(front + 3.4, foot - 3)
                room.ellipse(kx - 0.7, ky - 1.0, kx + 0.7, ky + 1.0, ramp(face, 0.2))
            for ny in (front + 1.8, foot - 2.4):
                room.rect((x + x1) / 2 - 0.4, ny, (x + x1) / 2 + 0.3, ny + 0.6, ramp(C.IRON, 0.3))
            x += bw
        # The kick board at the foot.
        room.rect(start, foot - 1.4, w, foot, ramp(face, 0.2))
        # The top: two planks seen from above, each worn paler along its
        # middle, then the thick front edge.
        for y0, y1 in ((top, split), (split, edge)):
            x = start - rnd.uniform(0, 10)
            while x < w:
                ln = rnd.uniform(30, 60)
                a, b = max(start, x), min(w, x + ln)
                t = 0.78 + rnd.uniform(-0.05, 0.05)
                room.rect(a, y0, b, y1, ramp(wood, t))
                room.rect(a, y0 + 1.4, b, y1 - 1.4, ramp(wood, t + 0.05))
                for _ in range(int((b - a) / 3)):
                    gy, gx = rnd.uniform(y0 + 0.4, y1 - 0.4), rnd.uniform(a, max(a + 0.1, b - 4))
                    room.rect(gx, gy, min(b, gx + rnd.uniform(3, 12)), gy + 0.25, ramp(wood, t + rnd.choice((-0.1, 0.08))))
                if rnd.random() < 0.5 and not dressed and b - a > 8:
                    kx, ky = rnd.uniform(a + 3, b - 3), (y0 + y1) / 2
                    room.ellipse(kx - 1.0, ky - 0.6, kx + 1.0, ky + 0.6, ramp(wood, t - 0.24))
                if b < w:
                    room.rect(b - 0.35, y0, b, y1, ramp(wood, 0.25))
                x += ln
            room.rect(start, y0, w, y0 + 0.35, ramp(wood, 0.9))
        room.rect(start, split - 0.3, w, split, ramp(wood, 0.3))
        room.rect(start, edge, w, front, ramp(wood, 0.82))
        room.rect(start, front - 0.3, w, front, ramp(wood, 0.3))
        room.rect(start, front, w, front + 0.8, ramp(face, 0.12))
        # The front posts at the ends, the full height of the booth (not at
        # a trade's counter_from: a smithy's hand pulls the bellows there,
        # in front of the counter's end).
        posts = [w - 4.2] if start > 0 else [0.2, w - 4.2]
        if start > 0:
            # The counter's open end: the top board's end grain over its
            # end frame.
            room.rect(start - 1.4, top, start, foot, ramp(face, 0.3))
            room.rect(start - 1.4, top, start - 0.8, foot, ramp(face, 0.55))
            room.rect(start - 1.6, top, start, front, ramp(wood, 0.5))
            room.rect(start - 1.6, top, start, top + 0.5, ramp(wood, 0.9))
        for px in posts:
            beam(room, px, 0, px + 4.0, foot + 1, wood, 0.5, rnd, dressed)
            room.rect(px - 0.2, foot + 0.2, px + 4.0, foot + 1.4, ramp(EARTH, 0.12))
    art, _c = G.guarded(w, H, paint, scene_palette(kind), "stall_counter_%s (%d wide)" % (kind, w), outline=0.3)
    return art


def counter_sheet(kind):
    return counter(kind, W, 0, 9890 + ROOFS.index(kind))


def counter_smithy_sheet(kind):
    return counter(kind, SMITHY_W, SMITHY["counter_from"], 9990 + ROOFS.index(kind))


# ---------------------------------------------------------------- sheets

SHEETS = {}
for _k in ROOFS:
    SHEETS["stall_%s" % _k] = (lambda k: lambda: stall_sheet(k))(_k)
    SHEETS["stall_%s_smithy" % _k] = (lambda k: lambda: smithy_sheet(k))(_k)
    SHEETS["stall_counter_%s" % _k] = (lambda k: lambda: counter_sheet(k))(_k)
    SHEETS["stall_counter_%s_smithy" % _k] = (lambda k: lambda: counter_smithy_sheet(k))(_k)
SHEETS["stall_smithy_bellows"] = bellows_sheet
SHEETS["stall_goods"] = G.sheet

# Art px: the scenes and counters at twice their room px, the bellows two
# frames of BELLOWS_FRAME, the goods sheet its grid at the goods density.
SIZES = {}
for _k in ROOFS:
    SIZES["stall_%s" % _k] = (W * 2, H * 2)
    SIZES["stall_%s_smithy" % _k] = (SMITHY_W * 2, H * 2)
    SIZES["stall_counter_%s" % _k] = (W * 2, H * 2)
    SIZES["stall_counter_%s_smithy" % _k] = (SMITHY_W * 2, H * 2)
SIZES["stall_smithy_bellows"] = (BELLOWS_FRAME[0] * 2 * 2, BELLOWS_FRAME[1] * 2)
SIZES["stall_goods"] = (G.COLUMNS * G.CELL[0] * G.DENSITY, G.ROWS * G.CELL[1] * G.DENSITY)


def gd_const(text, name):
    """A constant of stall_scenes.gd as Python data: its literal, comments
    dropped, Vector2i and Rect2i as lists, read as JSON."""
    m = re.search(r"\nconst %s := " % name, text)
    if not m:
        raise ValueError("stall_scenes.gd has no const %s" % name)
    i = j = m.end()
    if text[i] in "{[":
        depth = 0
        while True:
            depth += text[j] in "{[("
            depth -= text[j] in "}])"
            if depth == 0:
                break
            j += 1
        lit = text[i:j + 1]
    else:
        lit = text[i:text.index("\n", i)]
    lit = re.sub(r"#[^\n]*", "", lit)
    lit = re.sub(r"(?:Vector2i|Rect2i)\(([^)]*)\)", r"[\1]", lit)
    lit = re.sub(r",(\s*[}\]])", r"\1", lit)
    return json.loads(lit)


def declared():
    """The declaration, as stall_scenes.gd's constants should read (tuples
    as lists)."""
    want = {
        "SHEETS": SCENES, "DENSITY": DENSITY, "SIZE": SIZE, "TRADES": TRADES, "KEEPER": KEEPER, "HANDS": HANDS,
        "DOOR": DOOR, "SPOTS": SPOTS, "FLOOR_SLOTS": FLOOR_SLOTS, "FLOOR_Y": FLOOR_Y, "COUNTER_SLOTS": COUNTER_SLOTS,
        "COUNTER_Y": COUNTER_Y, "FEW_STEP": FEW_STEP, "GOODS_SHEET": res("stall_goods"), "GOODS_DENSITY": G.DENSITY,
        "CELL": G.CELL, "COLUMNS": G.COLUMNS, "GOODS": G.index_table(), "OTHER": G.other_index(),
    }
    return json.loads(json.dumps(want))


def check(out_dir):
    """Each sheet is exactly what this script paints now and of its size;
    stall_scenes.gd's constants equal the declaration exactly (the scene
    sheets, density and size, the people's spots, the goods slots and lines,
    the smithy's whole layout with its fire and bellows, the goods sheet,
    its density, cell grid, GOODS table and OTHER); every scene's counter
    (counter_for's rule) and the bellows are painted here; the goods sheet
    holds every good's two cells, each good within the tap box's rows."""
    ok = True
    sheets = {}
    for name, build in SHEETS.items():
        path = os.path.join(out_dir, name + ".png")
        sheet = Image.open(path).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun stalls.py)" % name)
            ok = False
        if sheet.size != SIZES[name]:
            print("%s: %dx%d, not %dx%d" % ((name,) + sheet.size + SIZES[name]))
            ok = False
        sheets[name] = sheet
    text = open(STALL_GD, encoding="utf-8").read()
    for name, want in declared().items():
        got = gd_const(text, name)
        if got != want:
            print("stall_scenes.gd %s is %s, the declared %s" % (name, got, want))
            ok = False
    for path in list(SCENES.values()) + [p.replace(".png", "_%s.png" % t) for p in SCENES.values() for t in TRADES]:
        name = os.path.splitext(os.path.basename(path))[0]
        counter_name = "stall_counter_" + name[len("stall_"):]
        for n in (name, counter_name):
            if n not in sheets:
                print("%s is not painted here" % n)
                ok = False
    goods = sheets["stall_goods"]
    alpha = goods.split()[3]
    d = G.DENSITY
    cw, ch = G.CELL[0] * d, G.CELL[1] * d
    for i in range(len(G.GOODS)):
        for cell in (2 * i, 2 * i + 1):
            x, y = (cell % G.COLUMNS) * cw, (cell // G.COLUMNS) * ch
            box = alpha.crop((x, y, x + cw, y + ch)).getbbox()
            if y + ch > goods.height or box is None:
                print("stall_goods: cell %d (%s) is empty or off the sheet" % (cell, G.GOODS[i][0][0]))
                ok = False
            elif cell % 2 == 0 and box[1] < (G.CELL[1] - G.GOOD_ROWS) * d:
                print("stall_goods: the good %s rises above the tap box's %d rows" % (G.GOODS[i][0][0], G.GOOD_ROWS))
                ok = False
    return ok


# ---------------------------------------------------------------- preview

# The goods laid out in the preview: (goods index, stock).
WARES = [(0, "plenty"), (13, "plenty"), (5, "plenty"), (11, "few"), (7, "one"), (19, "few"), (3, "plenty"), (29, "few")]
SMITHY_WARES = [(28, "plenty"), (24, "plenty"), (27, "few"), (29, "one"), (25, "few"), (30, "plenty")]


def compose(sheet, counter, goods, d, gd, wares, villager, smithy=False, bellows=None):
    """A stall as the window shows it, at 2 art px per room px: the
    backdrop, the bellows (open), the floor piles, the keeper (and a smithy's
    smith at the anvil and hand at the bellows), the counter, the goods on
    it, three customers. d and gd are the sheets' densities (1 for the pack
    art)."""
    out = sheet.resize((sheet.width * 2 // d, sheet.height * 2 // d), Image.NEAREST)
    lay = SMITHY if smithy else {"keeper": KEEPER, "spots": SPOTS, "floor_slots": FLOOR_SLOTS, "counter_slots": COUNTER_SLOTS}
    body = villager.resize((villager.width * 2, villager.height * 2), Image.NEAREST)

    def put(img, x, y, density):
        img = img.resize((img.width * 2 // density, img.height * 2 // density), Image.NEAREST)
        out.alpha_composite(img, (int(x * 2), int(y * 2)))

    def cell(i):
        cw, ch = 16 * gd, 32 * gd
        return goods.crop(((i % 16) * cw, (i // 16) * ch, (i % 16 + 1) * cw, (i // 16 + 1) * ch))

    if bellows is not None:
        put(bellows.crop((0, 0, bellows.width // 2, bellows.height)), BELLOWS_AT[0], BELLOWS_AT[1], d)
    piles = [g for g, s in wares if s == "plenty"]
    loose = [(g, s) for g, s in wares if s != "plenty"]
    for k, g in enumerate(piles[:len(lay["floor_slots"])]):
        put(cell(2 * g + 1), lay["floor_slots"][k], FLOOR_Y - 32, gd)
    # The keeper (and at a smithy the smith at the anvil and a hand at
    # their spot by the forge), feet at 44 in the body's frame.
    for p in ([SMITHY["anvil"], SMITHY["hands"][0]] if smithy else []) + [lay["keeper"]]:
        out.alpha_composite(body, ((p["x"] - 32) * 2, (p["y"] - 44) * 2))
    put(counter, 0, 0, d)
    for k, (g, s) in enumerate(loose[:len(lay["counter_slots"])]):
        for n in range(1 if s == "one" else 2):
            put(cell(2 * g), lay["counter_slots"][k] + FEW_STEP[0] * n, COUNTER_Y - 32 + FEW_STEP[1] * n, gd)
    for p in lay["spots"][:3]:
        out.alpha_composite(body, ((p["x"] - 32) * 2, (p["y"] - 44) * 2))
    return out


def preview(out_path, sheet_dir, pack_dir=None, villager=None, zoom=2):
    """Every scene (plain, then smithy, each roof) with its people and
    goods, the pack's version beside it at the same scale when pack_dir is
    given; then the goods sheet, the pack's above it."""
    vil = Image.open(villager).convert("RGBA") if villager else Image.new("RGBA", (64, 64), (0, 0, 0, 0))

    def load(d, name):
        return Image.open(os.path.join(d, name + ".png")).convert("RGBA")

    goods = load(sheet_dir, "stall_goods")
    rows = []
    for smithy in (False, True):
        for k in ROOFS:
            name = "stall_%s%s" % (k, "_smithy" if smithy else "")
            wares = SMITHY_WARES if smithy else WARES
            bel = load(sheet_dir, "stall_smithy_bellows") if smithy else None
            new = compose(load(sheet_dir, name), load(sheet_dir, "stall_counter_%s%s" % (k, "_smithy" if smithy else "")),
                          goods, 2, G.DENSITY, wares, vil, smithy, bel)
            old = None
            if pack_dir:
                old = compose(load(pack_dir, name), load(pack_dir, "stall_counter%s" % ("_smithy" if smithy else "")),
                              load(pack_dir, "stall_goods"), 1, 1, wares, vil, smithy,
                              load(pack_dir, "stall_smithy_bellows") if smithy else None)
            rows.append((old, new))
    width = max((o.width + 8 if o else 0) + n.width for o, n in rows)
    height = sum(n.height + 8 for _o, n in rows)
    sheet = Image.new("RGBA", (width, height), (0, 0, 0, 255))
    y = 0
    for old, new in rows:
        x = 0
        if old:
            sheet.paste(old, (0, y))
            x = old.width + 8
        sheet.paste(new, (x, y))
        y += new.height + 8
    sheet = sheet.resize((sheet.width * zoom, sheet.height * zoom), Image.NEAREST)
    sheet.save(out_path)
    print(out_path, sheet.size)


def main():
    parser = argparse.ArgumentParser(description="Paint salem's market-stall close-ups and the goods sheet.")
    parser.add_argument("--out", default=C.OUT_DIR, help="where the sheets go (default client/assets/interiors)")
    parser.add_argument("--only", help="paint only the sheets whose names contain this")
    parser.add_argument("--check", action="store_true", help="check the sheets instead of painting them")
    parser.add_argument("--preview", metavar="PNG", help="render the scenes from --out's sheets into PNG")
    parser.add_argument("--pack", help="with --preview: the pack sheets' folder, shown beside ours")
    parser.add_argument("--villager", help="with --preview: a 64x64 salem body frame for the people")
    args = parser.parse_args()
    if args.check:
        sys.exit(0 if check(args.out) else 1)
    if args.preview:
        preview(args.preview, args.out, args.pack, args.villager)
        return
    os.makedirs(args.out, exist_ok=True)
    for name, build in SHEETS.items():
        if args.only and args.only not in name:
            continue
        build().save(os.path.join(args.out, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
