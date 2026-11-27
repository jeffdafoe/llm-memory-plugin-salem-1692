"""Paints salem's Inn interiors (LLM-969): the common room downstairs, the
upstairs hall and the guest room. Every pixel is drawn here, no pack art.

    python tools/interior-art/inn.py
    python tools/interior-art/inn.py --out DIR   # elsewhere (prototypes)
    python tools/interior-art/inn.py --check     # a fresh paint, sizes, rects

Writes to client/assets/interiors/:
  inn_common.png, inn_pieces.png          the common room (the Inn's own
                                          structure id in interior_rooms.gd)
  inn_fire.png                            its hearth fire, 4 frames of 16x16
  inn_pot.png                             the pot on its brazier, 4 of 32x32
  inn_candle.png                          the desk candle, 4 frames of 16x32
  inn_hall_common.png, inn_hall_pieces.png, inn_hall_door_open.png
                                          the upstairs hall, and the open door
                                          drawn over a vacant room's door
  inn_guest_common.png, inn_guest_pieces.png
                                          a guest room
all at density 2. See common.py for the view, the sheets and the kit.

A respectable 1690s ordinary, better finished than a home: the same oak
frame and limewashed plaster, but a painted board wainscot below a chair
rail, a stair with turned balusters, joined furniture with turned legs and
ebonised split spindles, pewter and brass rather than wood and redware, the
county's rates posted by the door of the keeper's desk.

The pieces stand exactly where interior_rooms.gd's entries say (rects, sort
lines, covers, the anims' x/y, the desk's `bar`), and --check holds the two
together.
"""

import argparse
import math
import os
import random
import re
import sys

from PIL import Image, ImageChops, ImageDraw, ImageFilter

import common as C
from common import ramp

# Painted at twice the room's pixels (interior_rooms.gd entry "density": 2).
C.set_density(2)

INN_KEY = "019d98af-ac9b-7833-8e03-5a7015bb5b0c"

# ---------------------------------------------------------------- palettes
# The Inn's woodwork paint: a soft blue-grey (the house is the Blue House),
# rubbed thin on the edges.
PAINT = ["1c262c", "2a3840", "3a4c56", "4e646e", "647c86", "7e969e", "9cb0b4"]
# Brass candlesticks and drawer pulls.
BRASS = ["3e2a0e", "624416", "8a6220", "b08430", "d0a84a", "ecd07a"]
# Salt-glazed stoneware (a Rhenish jug), mottled brown.
STONEWARE = ["2e261c", "4a3e2e", "6a5a42", "8c7a5a", "ae9c78", "ccbc96"]
# A traveller's trunk, hide over pine.
LEATHER = ["1e120a", "321e10", "4a2e18", "623e22", "7c5230"]
# The guest bed's coverlet: madder gone soft and brown with washing.
FADED = ["3a201a", "4e2c22", "62382a", "744634", "86563e"]

ROOM_PALETTE = C.ROOM_PALETTE + PAINT + BRASS
PIECE_PALETTE = C.PIECE_PALETTE + PAINT + BRASS + STONEWARE + LEATHER + C.HERB + FADED
POT_PALETTE = C.IRON + C.FIRE + C.SOOT + C.STONE + C.OAK + C.LINEN + C.PEWTER
CANDLE_PALETTE = BRASS + C.LINEN + C.FIRE + C.SOOT


# ---------------------------------------------------------------- shared painters

def turned(room, cx, y0, y1, r, tone=0.55, cols=None, profile=None, lit=0.24):
    """A turned post seen from the side, centre cx from y0 to y1: its width
    follows `profile`, (t, fraction of r) points down its length (a vase and
    rings by default), lit on the left, shaded on the right."""
    cols = cols or C.OAK
    profile = profile or [(0.0, 0.8), (0.06, 1.0), (0.1, 0.55), (0.18, 0.6), (0.52, 1.0), (0.68, 0.72),
                          (0.78, 0.5), (0.84, 0.95), (0.9, 0.95), (0.93, 0.6), (1.0, 0.75)]
    n = max(1, int((y1 - y0) * 4))
    for i in range(n):
        t = i / max(1, n - 1)
        for (t0, f0), (t1, f1) in zip(profile, profile[1:]):
            if t0 <= t <= t1:
                f = f0 + (f1 - f0) * (t - t0) / max(1e-6, t1 - t0)
                break
        hw = max(0.3, r * f)
        y = y0 + i / 4
        room.rect(cx - hw, y, cx + hw, y + 0.25, ramp(cols, tone))
        room.rect(cx - hw, y, cx - hw * 0.35, y + 0.25, ramp(cols, tone + lit))
        room.rect(cx + hw * 0.45, y, cx + hw, y + 0.25, ramp(cols, tone - 0.22))


def wainscot(room, x0, x1, top, bottom, rnd, board=5.0):
    """Feather-edged boards up the lower wall, painted, under a moulded chair
    rail: each board's bevel lit on its left, the joint dark on its right,
    brush streaks, the paint rubbed through to the wood low down."""
    x = x0
    while x < x1:
        b = min(x1, x + board + rnd.uniform(-0.6, 0.6))
        t = 0.5 + rnd.uniform(-0.05, 0.05)
        room.rect(x, top, b, bottom, ramp(PAINT, t))
        for _ in range(int((bottom - top) / 3)):
            gy, gx = rnd.uniform(top, bottom - 2), rnd.uniform(x + 0.8, max(x + 0.9, b - 0.8))
            room.rect(gx, gy, gx + 0.25, gy + rnd.uniform(1.5, 4), ramp(PAINT, t + rnd.choice((-0.08, 0.07))))
        if rnd.random() < 0.6:
            cy, cx = rnd.uniform(bottom - 4, bottom - 1), rnd.uniform(x + 1, max(x + 1.1, b - 2))
            room.ellipse(cx, cy, cx + rnd.uniform(0.8, 1.8), cy + rnd.uniform(0.5, 1.0), ramp(C.OAK, 0.5))
        room.rect(x, top, x + 0.7, bottom, ramp(PAINT, t + 0.22))
        room.rect(b - 0.45, top, b, bottom, ramp(PAINT, 0.1))
        x = b
    room.rect(x0, top - 2.0, x1, top + 0.4, ramp(PAINT, 0.58))
    room.rect(x0, top - 2.0, x1, top - 1.3, ramp(PAINT, 0.9))
    room.rect(x0, top - 0.6, x1, top - 0.2, ramp(PAINT, 0.3))
    room.rect(x0, top + 0.1, x1, top + 0.6, ramp(PAINT, 0.12))


def girt_and_joists(room, x0, x1, rnd, gaps=()):
    """The ceiling: joist ends showing above a girt across the top of the
    back wall; `gaps` are (x0, x1) spans left open (a stairwell)."""
    room.rect(x0, 0, x1, 4, ramp(C.TIMBER, 0.12))
    x = x0 + 2
    while x < x1:
        if not any(a - 3 < x < b for a, b in gaps):
            C.timber(room, x, 0, x + 3, 4, 0.38)
        x += 9
    spans = [(x0, x1)]
    for a, b in gaps:
        spans = [s for sp in spans for s in ((sp[0], min(sp[1], a)), (max(sp[0], b), sp[1])) if s[1] - s[0] > 1]
    for a, b in spans:
        C.timber(room, a, 4, b, 8, 0.42, rnd)


def sconce(room, cx, y):
    """An iron candle sconce on a post: a back plate, an arm, a pan and a
    tallow candle."""
    room.rect(cx - 0.9, y - 3, cx + 0.9, y + 2, ramp(C.IRON, 0.4))
    room.rect(cx - 0.9, y - 3, cx - 0.3, y + 2, ramp(C.IRON, 0.7))
    room.line([(cx, y + 1.0), (cx + 2.4, y + 0.6)], ramp(C.IRON, 0.3), 0.4)
    room.ellipse(cx + 1.0, y - 0.2, cx + 4.0, y + 1.0, ramp(C.IRON, 0.5))
    room.rect(cx + 2.0, y - 3.6, cx + 3.0, y + 0.2, ramp(C.LINEN, 0.85))
    room.rect(cx + 2.0, y - 3.6, cx + 2.4, y + 0.2, ramp(C.LINEN, 0.97))
    room.rect(cx + 2.4, y - 4.3, cx + 2.6, y - 3.6, ramp(C.SOOT, 0.3))


def rates_paper(room, x0, y0, x1, y1):
    """The county's rates for the ordinary, as the law had them posted: a
    sheet of paper tacked to the wall, a heading and lines of a hand."""
    room.rect(x0 + 0.4, y0 + 0.5, x1 + 0.4, y1 + 0.5, ramp(C.PLASTER, 0.3))
    room.rect(x0, y0, x1, y1, ramp(C.LINEN, 0.82))
    room.rect(x0, y0, x0 + 0.5, y1, ramp(C.LINEN, 0.95))
    room.rect(x1 - 0.5, y0 + 1, x1, y1, ramp(C.LINEN, 0.6))
    room.rect(x0 + 1.5, y0 + 1.3, x1 - 1.5, y0 + 2.3, ramp(C.TIMBER, 0.1))
    y = y0 + 3.6
    k = 0
    while y < y1 - 1.2:
        room.rect(x0 + 1.0, y, x1 - 1.0 - (k % 3) * 1.0, y + 0.55, ramp(C.TIMBER, 0.2))
        y += 1.7
        k += 1
    for tx in (x0 + 0.6, x1 - 1.1):
        room.rect(tx, y0 + 0.4, tx + 0.6, y0 + 1.0, ramp(C.IRON, 0.3))


def numeral(room, x, y, digit, col):
    """A painted figure, 3 x 5 room px, top left at (x, y)."""
    glyphs = {
        "1": (".#.", "##.", ".#.", ".#.", "###"),
        "2": ("##.", "..#", ".#.", "#..", "###"),
        "3": ("##.", "..#", ".#.", "..#", "##."),
        "4": ("#.#", "#.#", "###", "..#", "..#"),
    }
    for j, row in enumerate(glyphs[digit]):
        for i, c in enumerate(row):
            if c == "#":
                room.rect(x + i, y + j, x + i + 1, y + j + 1, col)


# ---------------------------------------------------------------- the common room

def stair_up(room, x0, x1, foot, top, steps, rnd):
    """A straight stair up to the chamber floor, rising from its foot at the
    left (x0, on the floor at `foot`) to the right through the ceiling, seen
    side on: each tread's lit nosing, the step ends, the string along its
    underside; the space under it boarded and painted; turned balusters on
    the treads, the handrail, and a newel post at the foot."""
    run = (x1 - x0) / steps
    rise = (foot - top) / steps
    under = 5.5   # how far the string's underside lies below the pitch line
    # The boarded spandrel under the stair.
    pts = [(x0 + under * run / rise, foot), (x1, top + under), (x1, foot)]
    room.poly(pts, ramp(PAINT, 0.42))
    bx = x0 + 2
    while bx < x1:
        room.rect(bx, top, bx + 0.5, foot, ramp(PAINT, 0.62))
        room.rect(bx + 0.5, top, bx + 0.8, foot, ramp(PAINT, 0.2))
        bx += 4.2
    # Trim the boards back to the spandrel: paint the area above the
    # underside afresh with the stair's side.
    side = [(x0, foot)]
    for i in range(steps):
        xa, xb = x0 + i * run, x0 + (i + 1) * run
        yt = foot - (i + 1) * rise
        side += [(xa, yt), (xb, yt)]
    side += [(x1, top + under), (x0 + under * run / rise, foot)]
    room.poly(side, ramp(C.OAK, 0.46))
    # The string: a band along the underside, darker.
    room.line([(x0 + under * run / rise, foot - 0.6), (x1, top + under - 0.6)], ramp(C.OAK, 0.26), 1.6)
    room.line([(x0 + under * run / rise - 0.6, foot - 2.4), (x1, top + under - 2.4)], ramp(C.OAK, 0.62), 0.4)
    for i in range(steps):
        xa, xb = x0 + i * run, x0 + (i + 1) * run
        yt = foot - (i + 1) * rise
        # The step's end grain, a riser line at its left, the tread's lit
        # top and its nosing just proud of the riser.
        room.rect(xa, yt, xa + 0.5, yt + rise, ramp(C.OAK, 0.22))
        room.rect(xa - 0.5, yt - 0.9, xb, yt, ramp(C.OAK, 0.88))
        room.rect(xa - 0.5, yt - 0.9, xa, yt + 0.3, ramp(C.OAK, 0.98))
        if rnd.random() < 0.5:
            gx = rnd.uniform(xa + 0.6, xb - 0.8)
            room.rect(gx, yt + 1.0, gx + 0.3, yt + rise - 0.5, ramp(C.OAK, 0.36))
    # Balusters, one on each tread, up to the rail; the rail follows the
    # pitch 13 px above the nosings.
    rail = 13.0
    for i in range(1, steps):
        cx = x0 + (i + 0.5) * run
        yt = foot - (i + 1) * rise
        ytop = foot - rail - (cx - x0) * rise / run
        turned(room, cx, max(0, ytop), yt - 0.9, 0.75, 0.58)
    # The newel at the foot and the handrail from it.
    nx = x0 + 1.6
    ny = foot - rail - 3.5
    room.line([(nx, ny + 1.2), (x1 + 2, foot - rail - (x1 + 2 - x0) * rise / run)], ramp(C.OAK, 0.4), 1.5)
    room.line([(nx, ny + 0.6), (x1 + 2, foot - rail - 0.6 - (x1 + 2 - x0) * rise / run)], ramp(C.OAK, 0.85), 0.5)
    turned(room, nx, ny, foot, 1.7, 0.56, profile=[(0.0, 0.7), (0.08, 1.0), (0.14, 0.6), (0.3, 0.85), (0.55, 0.85),
                                                   (0.6, 1.1), (0.66, 1.1), (0.7, 1.0), (1.0, 1.0)])
    room.ellipse(nx - 1.4, ny - 2.6, nx + 1.4, ny + 0.4, ramp(C.OAK, 0.66))
    room.ellipse(nx - 0.8, ny - 2.2, nx + 0.2, ny - 1.2, ramp(C.OAK, 0.98))


def inn_hearth(room, x0, x1, top, wall_bottom, fire_box, rnd):
    """The common room's hearth, wider than a home's: a breast of fieldstone
    laid up rough, stones of every size with uneven seams; a deep mouth
    under an oak lintel, its cheeks splaying in to the sooty back (the right
    cheek lit by the fire); a trammel and pot; a stone apron and fire irons.
    fire_box (x, y, w, h) is where the fire strip plays."""
    fx, fy, fw, fh = fire_box
    room.rect(x0, top, x1, wall_bottom, ramp(C.MORTAR, 0.45))
    y = wall_bottom
    while y > top + 0.5:
        row = rnd.uniform(3.0, 6.5)
        x = x0 - rnd.uniform(0, 4)
        while x < x1:
            sw = rnd.choice((rnd.uniform(3, 5), rnd.uniform(5, 8), rnd.uniform(8, 12)))
            sh = row * rnd.uniform(0.7, 1.0) + (rnd.uniform(1.5, 3.5) if rnd.random() < 0.2 else 0)
            a, b = max(x0, x + 0.35), min(x1, x + sw - 0.35)
            s1 = y - 0.35 - rnd.uniform(0, 0.5)
            s0 = max(top, s1 - sh + 0.4)
            if b - a > 1.0 and s1 - s0 > 1.0:
                tone = 0.5 + rnd.uniform(-0.2, 0.18)
                r = int(min(b - a, s1 - s0) * 0.35 * C.S)
                room.d.rounded_rectangle((a * C.S, s0 * C.S, b * C.S, s1 * C.S), radius=r, fill=ramp(C.STONE, tone))
                room.speckle(a + 0.5, s0 + 0.6, b - 0.5, s1 - 0.6, (b - a) * (s1 - s0) / 2.5, C.STONE,
                             (tone - 0.14, tone + 0.12), rnd, (0.25, 0.5))
                room.d.rounded_rectangle((a * C.S, s0 * C.S, b * C.S, (s0 + 0.8) * C.S), radius=int(0.4 * C.S),
                                         fill=ramp(C.STONE, tone + 0.26))
                room.rect(a, s0 + 0.5, a + 0.5, s1 - 0.6, ramp(C.STONE, tone + 0.16))
                room.rect(b - 0.6, s0 + 0.8, b, s1, ramp(C.STONE, tone - 0.26))
                room.rect(a + 0.4, s1 - 0.7, b - 0.2, s1, ramp(C.STONE, tone - 0.24))
            x += sw
        y -= row
    soot = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(soot).ellipse(((fx - 3) * C.S, (top - 4) * C.S, (fx + fw + 3) * C.S, (fy + 2) * C.S), fill=110)
    room.lay.paste(Image.new("RGBA", room.lay.size, C.B.rgb(C.SOOT[1])), (0, 0), soot.filter(ImageFilter.GaussianBlur(3 * C.S)))
    # The mouth: splayed cheeks in to the sooty back.
    room.rect(fx - 1.5, fy, fx + fw + 1.5, fy + fh, ramp(C.SOOT, 0.1))
    room.poly([(fx - 1.5, fy), (fx + 2.2, fy + 2.6), (fx + 2.2, fy + fh - 1.6), (fx - 1.5, fy + fh)], ramp(C.STONE, 0.24))
    room.poly([(fx + fw + 1.5, fy), (fx + fw - 2.2, fy + 2.6), (fx + fw - 2.2, fy + fh - 1.6), (fx + fw + 1.5, fy + fh)],
              ramp(C.STONE, 0.4))
    room.rect(fx + 2.2, fy + 2.6, fx + fw - 2.2, fy + fh - 1.6, ramp(C.SOOT, 0.02))
    room.rect(fx - 1.5, fy + fh - 1.6, fx + fw + 1.5, fy + fh, ramp(C.STONE, 0.18))
    C.timber(room, fx - 4.0, fy - 3.8, fx + fw + 4.0, fy, 0.45, rnd)
    room.rect(fx - 1.5, fy, fx + fw + 1.5, fy + 0.8, ramp(C.SOOT, 0.0))
    tx = fx + fw / 2
    room.rect(tx - 0.35, fy, tx + 0.35, fy + 4.2, ramp(C.IRON, 0.45))
    for k in range(4):
        room.rect(tx + 0.35, fy + 0.6 + k, tx + 0.8, fy + 1.0 + k, ramp(C.IRON, 0.6))
    room.ellipse(tx - 2.8, fy + 4.0, tx + 2.8, fy + 8.2, ramp(C.IRON, 0.2))
    room.ellipse(tx - 2.2, fy + 4.4, tx - 0.4, fy + 6.0, ramp(C.IRON, 0.6))
    room.line([(tx - 2.6, fy + 4.6), (tx, fy + 3.4), (tx + 2.6, fy + 4.6)], ramp(C.IRON, 0.5), 0.3)
    # The stone apron: big flat slabs on the floor.
    ax0, ax1 = fx - 6, fx + fw + 6
    room.rect(ax0 - 0.6, wall_bottom, ax1 + 0.6, wall_bottom + 6.4, ramp(C.STONE, 0.12))
    sx = ax0
    while sx < ax1:
        sw = min(ax1 - sx, rnd.uniform(6, 10))
        tone = 0.6 + rnd.uniform(-0.08, 0.08)
        room.rect(sx + 0.2, wall_bottom, sx + sw - 0.2, wall_bottom + 5.8, ramp(C.STONE, tone))
        room.rect(sx + 0.2, wall_bottom, sx + sw - 0.2, wall_bottom + 0.9, ramp(C.STONE, tone + 0.22))
        room.speckle(sx + 0.6, wall_bottom + 1.2, sx + sw - 0.6, wall_bottom + 5.2, sw * 2, C.STONE,
                     (tone - 0.12, tone + 0.1), rnd)
        sx += sw
    room.line([(ax1 - 1.0, wall_bottom - 10), (ax1 + 0.4, wall_bottom + 1.0)], ramp(C.IRON, 0.45), 0.4)
    room.line([(ax1 + 0.6, wall_bottom - 9), (ax1 + 1.4, wall_bottom + 1.0)], ramp(C.IRON, 0.35), 0.4)
    room.ellipse(ax1 - 2.0, wall_bottom - 10.8, ax1 + 0.2, wall_bottom - 9.4, ramp(C.IRON, 0.6))


def common_room(seed=1692):
    """The Inn's entry hall and common room, 128 x 112: the back wall (rows
    0-2), the stair up at the left, the casement, the hearth at the right;
    the floor below; the street door at the bottom."""
    w, h, wall = 128, 112, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd)
    C.plaster_wall(room, 0, w, 0, wall - 2, rnd)
    wainscot(room, 0, w, 33, wall - 2, rnd)
    girt_and_joists(room, 0, w, rnd, gaps=((26, 44),))
    # The stairwell the stair climbs into.
    room.rect(26, 0, 44, 8, ramp(C.SOOT, 0.2))
    room.rect(26, 7, 44, 8, ramp(C.TIMBER, 0.3))
    for px in (44, 80):
        C.timber(room, px, 0, px + 4, wall - 2, 0.4, rnd)
    C.casement(room, 54, 13, 70, 27)
    rates_paper(room, 71.4, 11.5, 79.4, 27)
    C.wear(room, 50, 100, 18, 10, 46)
    C.wear(room, 66, 104, 10, 8, 30)
    C.light_pool(room, 54, wall + 1, 70, wall + 20, 70)
    C.wall_foot(room, 0, w, wall)
    # The firebox is where the fire anim plays (ROOMS anims).
    fire = ROOMS[INN_KEY]["anims"][0]
    inn_hearth(room, 84, 127, 0, wall, (fire[2], fire[3], 16, 16), rnd)
    # A mantel board on the breast with the house's pewter, its shadow on
    # the stone under it.
    room.rect(87, 32.6, 124, 34.4, ramp(C.SOOT, 0.3))
    C.shelf(room, 87, 124, 31, rnd, ("plate", "jug", "plate", "plate", "jug"))
    room.rect(87, 31, 124, 31.5, ramp(C.OAK, 0.98))
    stair_up(room, 0, 40, 58, 0, 11, rnd)
    # The hearthstone the brazier stands on, clear of the desk.
    room.rect(74.4, 74.4, 99.4, 84.4, ramp(C.STONE, 0.05))
    room.rect(74, 74, 99, 83.6, ramp(C.STONE, 0.5))
    room.rect(74, 74, 99, 74.8, ramp(C.STONE, 0.74))
    room.speckle(74.6, 75.2, 98.4, 83, 60, C.STONE, (0.38, 0.6), rnd)
    room.line([(85, 74.4), (86.4, 83.4)], ramp(C.STONE, 0.25), 0.3)
    # Shadows, cast down and right: the desk, table, form, chair, basket.
    for x0, y0, x1, y1 in ((23, 86, 72, 92), (77, 94, 125, 99.5), (77, 109, 126, 113), (2, 102, 22, 108),
                           (30, 107, 45, 112)):
        room.shadow(x0, y0, x1, y1, 235)
    return C.shrink(room, ROOM_PALETTE)


def bellarmine(room, cx, base):
    """A salt-glazed stoneware jug, its foot at (cx, base): a round belly,
    the narrow neck, a loop handle, the bearded mask on its shoulder."""
    room.ellipse(cx - 3.8, base - 7.8, cx + 3.8, base + 0.6, ramp(STONEWARE, 0.0))
    room.rect(cx - 1.6, base - 11.2, cx + 1.6, base - 6.0, ramp(STONEWARE, 0.0))
    room.ellipse(cx - 3.2, base - 7.2, cx + 3.2, base, ramp(STONEWARE, 0.42))
    room.ellipse(cx - 2.6, base - 6.6, cx - 0.4, base - 3.2, ramp(STONEWARE, 0.72))
    room.ellipse(cx + 1.2, base - 5.6, cx + 3.0, base - 0.8, ramp(STONEWARE, 0.22))
    room.rect(cx - 1.0, base - 10.4, cx + 1.0, base - 6.4, ramp(STONEWARE, 0.4))
    room.rect(cx - 1.4, base - 10.8, cx + 1.4, base - 9.8, ramp(STONEWARE, 0.6))
    room.rect(cx - 1.0, base - 10.4, cx - 0.4, base - 6.6, ramp(STONEWARE, 0.7))
    room.line([(cx + 1.0, base - 9.8), (cx + 2.6, base - 8.6), (cx + 2.4, base - 6.0)], ramp(STONEWARE, 0.3), 0.5)
    room.ellipse(cx - 1.3, base - 6.8, cx + 0.9, base - 4.4, ramp(STONEWARE, 0.6))
    room.rect(cx - 0.8, base - 5.4, cx + 0.4, base - 4.6, ramp(STONEWARE, 0.25))
    room.ellipse(cx - 2.8, base - 0.9, cx + 2.8, base + 0.3, ramp(STONEWARE, 0.3))


def tankard(room, cx, base):
    """A lidded pewter tankard, its foot at (cx, base)."""
    room.rect(cx - 1.6, base - 4.6, cx + 1.6, base, ramp(C.PEWTER, 0.55))
    room.rect(cx - 1.6, base - 4.6, cx - 0.8, base, ramp(C.PEWTER, 0.92))
    room.rect(cx + 0.9, base - 4.6, cx + 1.6, base, ramp(C.PEWTER, 0.3))
    room.ellipse(cx - 1.9, base - 5.6, cx + 1.9, base - 4.2, ramp(C.PEWTER, 0.78))
    room.line([(cx + 1.6, base - 4.0), (cx + 2.8, base - 3.2), (cx + 1.6, base - 1.2)], ramp(C.PEWTER, 0.45), 0.4)
    room.rect(cx - 1.8, base - 0.7, cx + 1.8, base, ramp(C.PEWTER, 0.4))


def ledger(room, x0, y0, x1, y1):
    """An open account book lying on the desk: calf boards, two pages of
    reckonings, the gutter."""
    room.rect(x0 - 0.8, y0 - 0.5, x1 + 0.8, y1 + 0.9, ramp(LEATHER, 0.15))
    mid = (x0 + x1) / 2
    room.rect(x0, y0, mid - 0.2, y1, ramp(C.LINEN, 0.95))
    room.rect(mid + 0.2, y0, x1, y1, ramp(C.LINEN, 0.84))
    room.rect(mid - 0.3, y0, mid + 0.3, y1, ramp(C.LINEN, 0.4))
    for k in range(int((y1 - y0 - 1.2) / 1.2)):
        yy = y0 + 1.0 + k * 1.2
        room.rect(x0 + 0.7, yy, mid - 1.0 - (k % 2) * 1.2, yy + 0.5, ramp(C.TIMBER, 0.3))
        room.rect(mid + 0.8, yy, x1 - 0.8 - ((k + 1) % 3) * 0.8, yy + 0.5, ramp(C.TIMBER, 0.3))


def top_light(room, x0, y0, x1, y1, rnd):
    """A board top's timber: the light pooling toward its upper left, a few
    short uneven grain marks."""
    glow = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(glow).ellipse(((x0 - 6) * C.S, (y0 - 4) * C.S, (x0 + (x1 - x0) * 0.55) * C.S, (y1 - 2) * C.S), fill=60)
    mask = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(mask).rectangle((x0 * C.S, y0 * C.S, x1 * C.S - 1, y1 * C.S - 1), fill=255)
    glow = ImageChops.multiply(glow.filter(ImageFilter.GaussianBlur(3 * C.S)), mask)
    room.lay.paste(Image.new("RGBA", room.lay.size, C.B.rgb(C.OAK[-1])), (0, 0), glow)
    for _ in range(int((x1 - x0) / 5)):
        gx, gy = rnd.uniform(x0 + 1, x1 - 4), rnd.uniform(y0 + 1, y1 - 1)
        room.line([(gx, gy), (gx + rnd.uniform(1.5, 3.5), gy + rnd.uniform(-0.3, 0.3))], ramp(C.OAK, 0.6), 0.3)


def keeper_desk(room, x, y, w, h, rnd):
    """The keeper's desk, a joined oak counter seen from the front at a
    slant: a two-board top with a moulded front edge; the front framed in
    three fielded panels with ebonised split spindles between them; a plinth
    on bun feet. On it the account book with an inkhorn and quill, and a
    stoneware jug at the right end; the middle is left clear for a sale
    (the entry's `bar`) and the left end for the candle."""
    top0, top1 = y + 4, y + 18
    fy0, fy1 = top1 + 2.4, y + h - 3.2
    # The top, two boards front and back.
    room.rect(x, top0, x + w, top1, ramp(C.OAK, 0.84))
    room.rect(x, (top0 + top1) / 2, x + w, top1, ramp(C.OAK, 0.8))
    for _ in range(6):
        gy, gx = rnd.uniform(top0 + 0.6, top1 - 0.6), rnd.uniform(x, x + w - 10)
        room.rect(gx, gy, gx + rnd.uniform(6, 14), gy + 0.25, ramp(C.OAK, 0.72))
    room.rect(x, (top0 + top1) / 2 - 0.2, x + w, (top0 + top1) / 2 + 0.3, ramp(C.OAK, 0.5))
    room.rect(x, top0, x + w, top0 + 0.7, ramp(C.OAK, 0.98))
    room.rect(x, top0, x + 0.7, top1, ramp(C.OAK, 0.94))
    room.rect(x + w - 2.0, top0, x + w, top1, ramp(C.OAK, 0.6))
    top_light(room, x, top0, x + w, top1, rnd)
    # The moulded front edge: a lit round, a dark cove under it.
    room.rect(x, top1, x + w, top1 + 1.2, ramp(C.OAK, 0.84))
    room.rect(x, top1 + 1.2, x + w, top1 + 2.4, ramp(C.OAK, 0.3))
    # The front: stiles and rails, three fielded panels.
    room.rect(x + 0.6, fy0, x + w - 0.6, fy1, ramp(C.OAK, 0.44))
    room.rect(x + 0.6, fy0, x + 1.4, fy1, ramp(C.OAK, 0.66))
    pw = (w - 2 * 3.0 - 2 * 4.0) / 3
    for i in range(3):
        px0 = x + 3.0 + i * (pw + 4.0)
        px1 = px0 + pw
        py0, py1 = fy0 + 1.8, fy1 - 1.8
        room.rect(px0, py0, px1, py1, ramp(C.OAK, 0.36))
        room.rect(px0 + 1.4, py0 + 1.4, px1 - 1.4, py1 - 1.4, ramp(C.OAK, 0.5))
        room.rect(px0, py0, px1, py0 + 0.7, ramp(C.OAK, 0.12))
        room.rect(px0, py0, px0 + 0.7, py1, ramp(C.OAK, 0.16))
        room.rect(px0, py1 - 0.6, px1, py1, ramp(C.OAK, 0.7))
        room.rect(px0 + 1.4, py0 + 1.4, px1 - 1.4, py0 + 2.0, ramp(C.OAK, 0.72))
    # Ebonised split spindles between the panels and at the ends.
    for sx in (x + 1.6, x + 3.0 + pw + 2.0, x + 3.0 + 2 * pw + 6.0, x + w - 1.6):
        turned(room, sx, fy0 + 1.2, fy1 - 1.2, 0.85, 0.2, cols=C.IRON)
    # The right end in shade.
    room.rect(x + w - 2.0, fy0 - 2.4, x + w - 0.2, fy1 + 1.6, ramp(C.OAK, 0.2))
    # Plinth and bun feet.
    room.rect(x + 0.3, fy1, x + w - 0.3, fy1 + 1.6, ramp(C.OAK, 0.4))
    room.rect(x + 0.3, fy1, x + w - 0.3, fy1 + 0.5, ramp(C.OAK, 0.66))
    for fx in (x + 1.0, x + w / 2 - 2.0, x + w - 5.0):
        room.ellipse(fx, fy1 + 1.0, fx + 4.0, y + h, ramp(C.OAK, 0.34))
        room.ellipse(fx + 0.5, fy1 + 1.2, fx + 2.0, fy1 + 2.2, ramp(C.OAK, 0.62))
    # On the top: the book in front of the candle, an inkhorn and quill, the
    # jug at the right end.
    ledger(room, x + 2.6, top1 - 7.4, x + 15.4, top1 - 1.4)
    room.ellipse(x + 16, top1 - 6.6, x + 18.6, top1 - 4.4, ramp(C.IRON, 0.25))
    room.line([(x + 17.2, top1 - 5.8), (x + 19.6, top1 - 11.0)], ramp(C.LINEN, 0.9), 0.5)
    room.line([(x + 17.8, top1 - 7.4), (x + 19.6, top1 - 11.0)], ramp(C.LINEN, 0.6), 0.35)
    bellarmine(room, x + w - 6, top0 + 7.6)


def joined_table(room, x, y, w, h, rnd):
    """A joined oak table seen from the front at a slant: a three-board top
    with a moulded edge, a frieze, turned legs on a low stretcher. On it two
    pewter tankards and a clay pipe toward the back; the places in front of
    the form are left clear for what is served. The form stands in front of
    it, so the legs end where the form's rect begins."""
    top0, top1 = y, y + 14
    room.rect(x + 3, y + h - 3.0, x + w - 3, y + h - 1.8, ramp(C.OAK, 0.34))
    room.rect(x + 3, y + h - 3.0, x + w - 3, y + h - 2.5, ramp(C.OAK, 0.58))
    for lx in (x + 3.4, x + w - 3.4):
        turned(room, lx, top1 + 2.0, y + h - 0.3, 1.5, 0.5)
    room.rect(x + 1.5, top1 + 1.8, x + w - 1.5, top1 + 4.8, ramp(C.OAK, 0.46))
    room.rect(x + 1.5, top1 + 3.0, x + w - 1.5, top1 + 3.4, ramp(C.OAK, 0.26))
    room.rect(x + 1.5, top1 + 4.4, x + w - 1.5, top1 + 4.8, ramp(C.OAK, 0.22))
    for k, (b0, b1) in enumerate(((top0, top0 + 5), (top0 + 5, top0 + 10), (top0 + 10, top1))):
        room.rect(x, b0, x + w, b1, ramp(C.OAK, (0.86, 0.8, 0.83)[k]))
        if k:
            room.rect(x, b0 - 0.2, x + w, b0 + 0.25, ramp(C.OAK, 0.42))
    for _ in range(7):
        gy, gx = rnd.uniform(top0 + 0.6, top1 - 0.6), rnd.uniform(x, x + w - 14)
        room.rect(gx, gy, gx + rnd.uniform(6, 14), gy + 0.25, ramp(C.OAK, 0.74))
    # Rings left by wet tankards.
    for _ in range(3):
        rx, ry = rnd.uniform(x + 6, x + w - 8), rnd.uniform(top0 + 6, top1 - 4)
        room.ellipse(rx, ry, rx + 3.2, ry + 2.0, ramp(C.OAK, 0.7))
        room.ellipse(rx + 0.4, ry + 0.3, rx + 2.8, ry + 1.7, ramp(C.OAK, 0.82))
    room.rect(x, top0, x + w, top0 + 0.7, ramp(C.OAK, 0.98))
    room.rect(x, top0, x + 0.7, top1, ramp(C.OAK, 0.9))
    room.rect(x + w - 2.0, top0, x + w, top1, ramp(C.OAK, 0.6))
    top_light(room, x, top0, x + w, top1, rnd)
    room.rect(x, top1, x + w, top1 + 1.0, ramp(C.OAK, 0.9))
    room.rect(x, top1 + 1.0, x + w, top1 + 1.8, ramp(C.OAK, 0.2))
    room.rect(x + w - 1.5, top1 + 1.8, x + w - 0.2, top1 + 4.8, ramp(C.OAK, 0.18))
    tankard(room, x + 23, top0 + 6.5)
    tankard(room, x + 28, top0 + 5.6)
    room.line([(x + 39, top0 + 4.5), (x + 45, top0 + 3.2)], ramp(C.LINEN, 0.9), 0.5)
    room.ellipse(x + 38, top0 + 3.8, x + 40.2, top0 + 5.6, ramp(C.LINEN, 0.75))


def form(room, x, y, w, h, rnd):
    """A joined form (a long bench) seen from the front at a slant: the seat
    board's top, its moulded edge and the rail under it, splayed turned legs."""
    for lx in (x + 4.0, x + w - 4.0):
        turned(room, lx, y + 8.6, y + h - 0.5, 1.3, 0.5)
    room.rect(x + 4, y + h - 4.5, x + w - 4, y + h - 3.4, ramp(C.OAK, 0.36))
    room.rect(x + 0.5, y + 1, x + w - 0.5, y + 6, ramp(C.OAK, 0.74))
    for _ in range(6):
        gy, gx = rnd.uniform(y + 1.4, y + 5.6), rnd.uniform(x + 0.5, x + w - 12.5)
        room.rect(gx, gy, gx + rnd.uniform(4, 12), gy + 0.25, ramp(C.OAK, rnd.choice((0.6, 0.88))))
    room.rect(x + 0.5, y + 1, x + w - 0.5, y + 1.6, ramp(C.OAK, 0.98))
    room.rect(x + 0.2, y + 6, x + w - 0.2, y + 7.1, ramp(C.OAK, 0.86))
    room.rect(x + 0.2, y + 7.1, x + w - 0.2, y + 7.9, ramp(C.OAK, 0.24))
    room.rect(x + 2.2, y + 7.9, x + w - 2.2, y + 9.6, ramp(C.OAK, 0.4))


def great_chair(room, x, y, w, h, rnd):
    """A turned great chair, the master's chair by the stair, seen side on
    facing right (its sitter faces east): two tall back posts with finials
    and spindles between them, a rush seat with a madder cushion, an arm to
    a front post, turned legs and stretchers."""
    seat = y + 15.2               # the seat's top
    legs = y + 27.5               # the feet on the floor
    bx = x + 3.4                  # the back posts
    fx = x + 16.2                 # the front posts
    # Stretchers low down, then the far legs.
    room.rect(bx, legs - 5.0, fx, legs - 4.2, ramp(C.OAK, 0.34))
    room.rect(bx, legs - 2.4, fx, legs - 1.6, ramp(C.OAK, 0.3))
    turned(room, fx, seat + 1.8, legs, 1.1, 0.48)
    # The back: two posts (the far one darker) and three turned spindles.
    turned(room, bx + 2.6, y + 1.2, legs - 0.5, 1.0, 0.36)
    for k in range(3):
        sy = y + 4.0 + k * 3.4
        room.rect(bx - 0.6, sy, bx + 3.6, sy + 1.2, ramp(C.OAK, 0.72))
        room.rect(bx - 0.6, sy, bx + 3.6, sy + 0.4, ramp(C.OAK, 0.92))
    turned(room, bx, y + 1.5, legs, 1.2, 0.55, profile=[(0, 0.9), (0.05, 1.0), (0.08, 0.6), (0.12, 0.9), (1, 0.9)])
    room.ellipse(bx - 1.3, y + 0.2, bx + 1.3, y + 2.6, ramp(C.OAK, 0.72))
    room.ellipse(bx - 0.8, y + 0.5, bx + 0.2, y + 1.4, ramp(C.OAK, 0.98))
    # The rush seat and its cushion.
    room.rect(bx + 0.8, seat - 2.6, fx + 0.8, seat + 0.6, ramp(C.STRAW, 0.6))
    for k in range(int((fx - bx) * 2)):
        room.rect(bx + 0.8 + k * 0.5, seat - 2.6, bx + 1.05 + k * 0.5, seat + 0.6, ramp(C.STRAW, 0.32 if k % 2 else 0.74))
    room.d.rounded_rectangle(((bx + 1.4) * C.S, (seat - 4.4) * C.S, (fx - 0.6) * C.S, (seat - 1.2) * C.S),
                             radius=int(1.2 * C.S), fill=ramp(C.MADDER, 0.42))
    room.rect(bx + 2.0, seat - 4.2, fx - 1.4, seat - 3.6, ramp(C.MADDER, 0.75))
    room.rect(bx + 2.0, seat - 1.8, fx - 1.0, seat - 1.2, ramp(C.MADDER, 0.15))
    room.rect(bx + 0.8, seat + 0.6, fx + 0.8, seat + 1.9, ramp(C.OAK, 0.5))
    room.rect(bx + 0.8, seat + 1.5, fx + 0.8, seat + 1.9, ramp(C.OAK, 0.22))
    # The arm, from the back post to the near front post.
    room.rect(bx, seat - 8.4, fx + 1.6, seat - 7.0, ramp(C.OAK, 0.7))
    room.rect(bx, seat - 8.4, fx + 1.6, seat - 7.9, ramp(C.OAK, 0.95))
    room.ellipse(fx - 0.4, seat - 9.0, fx + 2.6, seat - 6.6, ramp(C.OAK, 0.78))
    turned(room, fx + 1.0, seat - 7.0, legs, 1.15, 0.56)


def work_basket(room, x, y, w, h, rnd):
    """A splint work basket on the floor by the chair, its bail handle up,
    a skein of undyed yarn and a ball of indigo in it, needles stuck through."""
    cx = x + w / 2
    b0, b1 = y + 10, y + h - 0.8
    room.ellipse(cx - 6.4, b1 - 2.2, cx + 6.4, b1 + 0.6, ramp(C.STRAW, 0.2))
    room.poly([(cx - 6.6, b0 + 1.0), (cx + 6.6, b0 + 1.0), (cx + 5.6, b1), (cx - 5.6, b1)], ramp(C.STRAW, 0.5))
    for k in range(int((b1 - b0 - 2.4) / 1.6)):
        yy = b0 + 2.0 + k * 1.6
        room.rect(cx - 6.4 + k * 0.17, yy, cx + 6.4 - k * 0.17, yy + 0.5, ramp(C.STRAW, 0.28))
    for k in range(7):
        sx = cx - 5.6 + k * 1.9
        room.rect(sx, b0 + 1.4, sx + 0.6, b1 - 0.4, ramp(C.STRAW, 0.72))
    room.poly([(cx - 6.6, b0 + 1.0), (cx - 5.8, b0 + 1.0), (cx - 4.8, b1), (cx - 5.6, b1)], ramp(C.STRAW, 0.82))
    room.poly([(cx + 5.8, b0 + 1.0), (cx + 6.6, b0 + 1.0), (cx + 5.6, b1), (cx + 4.8, b1)], ramp(C.STRAW, 0.22))
    # The rim, the contents, the bail.
    room.ellipse(cx - 6.8, b0 - 1.2, cx + 6.8, b0 + 2.2, ramp(C.STRAW, 0.7))
    room.ellipse(cx - 5.8, b0 - 0.6, cx + 5.8, b0 + 1.6, ramp(C.STRAW, 0.15))
    room.ellipse(cx - 5.2, b0 - 2.6, cx + 0.4, b0 + 1.4, ramp(C.LINEN, 0.7))
    for k in range(4):
        room.line([(cx - 4.6 + k * 1.2, b0 - 2.0), (cx - 4.0 + k * 1.2, b0 + 1.0)], ramp(C.LINEN, 0.45), 0.3)
    room.ellipse(cx - 0.6, b0 - 3.4, cx + 4.6, b0 + 1.2, ramp(C.INDIGO, 0.45))
    room.ellipse(cx + 0.2, b0 - 3.0, cx + 2.4, b0 - 1.2, ramp(C.INDIGO, 0.8))
    room.line([(cx + 1.2, b0 - 1.0), (cx + 4.8, b0 - 6.0)], ramp(C.IRON, 0.6), 0.3)
    room.line([(cx + 2.4, b0 - 0.8), (cx + 3.6, b0 - 6.4)], ramp(C.IRON, 0.5), 0.3)
    room.line([(cx - 6.2, b0 + 0.6), (cx - 5.0, y + 2.0), (cx + 5.0, y + 2.0), (cx + 6.2, b0 + 0.6)],
              ramp(C.STRAW, 0.62), 0.8)
    room.line([(cx - 5.0, y + 1.8), (cx + 5.0, y + 1.8)], ramp(C.STRAW, 0.85), 0.3)


def pieces_sheet(key, seed, outline=0.35, ring=0.18):
    """The room's pieces sheet, each piece painted into its declared rect
    (ROOMS) by C.paint_pieces, which refuses art outside a rect and rects
    that overlap."""
    rnd = random.Random(seed)
    spec = ROOMS[key]
    return C.paint_pieces(*spec["size"], [((x, y, w, h), (lambda room, px, py, pw, ph, p=painter: p(room, px, py, pw, ph, rnd)))
                                         for (x, y, w, h, _sort), painter in spec["pieces"]],
                          PIECE_PALETTE, outline=outline, ring=ring)


def common_pieces():
    return pieces_sheet(INN_KEY, 1693, outline=0.45, ring=0.26)


def inn_fire():
    return C.fire_strip(16, 16, 4, seed=11)


def pot_strip(frames=4):
    """The pot on the brazier by the hearth, `frames` frames of 32 x 32 room
    px: an iron fire pan on three legs, its coals breathing; a three-legged
    pot on it, the stew in it bubbling; steam curling up."""
    w, h = 32, 32
    out = Image.new("RGBA", (w * C.DENSITY * frames, h * C.DENSITY), (0, 0, 0, 0))
    for k in range(frames):
        room = C.Room(w, h)
        rnd = random.Random(500 + k)
        ph = 2 * math.pi * k / frames
        cx = 20.0
        # The brazier's legs, its pan with the coals, its rim.
        for lx, top, bot, t in ((cx - 7.5, 25.0, 31.0, 0.45), (cx + 7.5, 25.0, 31.0, 0.3), (cx + 0.5, 25.0, 29.4, 0.2)):
            room.line([(lx, top), (lx + (lx - cx) * 0.12, bot)], ramp(C.IRON, t), 0.9)
            room.ellipse(lx - 1.0 + (lx - cx) * 0.12, bot - 0.6, lx + 1.0 + (lx - cx) * 0.12, bot + 0.4, ramp(C.IRON, t))
        room.ellipse(cx - 9.5, 20.6, cx + 9.5, 27.4, ramp(C.IRON, 0.3))
        room.ellipse(cx - 8.7, 20.8, cx + 8.7, 25.6, ramp(C.SOOT, 0.4))
        # Coals: dark lumps with their glowing seams breathing between them.
        glow = 0.32 + 0.1 * math.sin(ph)
        room.speckle(cx - 7.6, 21.4, cx + 7.6, 24.8, 14, C.FIRE, (glow, glow + 0.14, glow + 0.28), rnd, (0.4, 0.8))
        room.speckle(cx - 7.6, 21.2, cx + 7.6, 25.0, 22, C.IRON, (0.08, 0.16, 0.24), rnd, (0.8, 1.5))
        room.rect(cx - 9.5, 24.6, cx + 9.5, 25.2, ramp(C.IRON, 0.55))
        room.rect(cx - 9.0, 25.2, cx + 9.0, 25.8, ramp(C.IRON, 0.12))
        # The pot: a round iron belly lit from below by the coals, its rim,
        # the stew, the bail handle.
        # The trivet the pot stands on in the pan.
        for tx in (cx - 4.0, cx + 0.6, cx + 4.4):
            room.rect(tx - 0.5, 21.0, tx + 0.5, 24.2, ramp(C.IRON, 0.05))
        room.rect(cx - 5.2, 20.6, cx + 5.2, 21.4, ramp(C.IRON, 0.12))
        room.ellipse(cx - 5.4, 12.6, cx + 5.4, 23.0, ramp(C.IRON, 0.3))
        room.ellipse(cx - 4.6, 13.4, cx - 1.0, 18.6, ramp(C.IRON, 0.58))
        room.ellipse(cx - 3.8, 14.0, cx - 2.2, 16.2, ramp(C.IRON, 0.85))
        room.ellipse(cx + 2.0, 14.4, cx + 5.0, 20.6, ramp(C.IRON, 0.18))
        room.ellipse(cx - 4.4, 20.4, cx + 4.4, 23.0, ramp(C.FIRE, 0.22 + 0.05 * math.sin(ph + 1.0)))
        room.ellipse(cx - 3.8, 20.0, cx + 3.8, 22.4, ramp(C.IRON, 0.3))
        room.ellipse(cx - 5.4, 11.2, cx + 5.4, 14.4, ramp(C.IRON, 0.62))
        room.ellipse(cx - 4.5, 11.7, cx + 4.5, 13.8, ramp(C.OAK, 0.3))
        for b in range(3):
            bx = cx - 2.4 + b * 2.2 + 0.7 * math.sin(ph + b * 2.1)
            by = 12.6 + 0.3 * math.cos(ph * 2 + b)
            if (k + b) % 2 == 0:
                room.ellipse(bx - 0.6, by - 0.35, bx + 0.6, by + 0.45, ramp(C.OAK, 0.62))
        room.line([(cx - 5.6, 12.8), (cx - 4.2, 8.4), (cx + 4.2, 8.4), (cx + 5.6, 12.8)], ramp(C.IRON, 0.5), 0.45)
        # Steam: soft wisps rising and leaning, each frame a step higher.
        for s in range(3):
            phase = (k + s * 1.4) / frames
            sy = 10.0 - phase * 8.0
            sx = cx - 2.0 + s * 2.2 + 1.4 * math.sin(2 * math.pi * phase + s)
            size = 1.1 + phase * 1.0
            room.ellipse(sx - size, sy - size * 0.7, sx + size, sy + size * 0.7, ramp(C.PEWTER, 0.95 - 0.12 * phase))
        out.paste(C.shrink(room, POT_PALETTE, outline=0.3), (k * w * C.DENSITY, 0))
    return out


def candle_strip(frames=4):
    """The desk candle, `frames` frames of 16 x 32 room px: a brass
    candlestick (its foot on the desk top, room y 64, at the strip's y 20)
    with a tallow candle; the flame stretching and leaning each frame."""
    w, h = 16, 32
    out = Image.new("RGBA", (w * C.DENSITY * frames, h * C.DENSITY), (0, 0, 0, 0))
    for k in range(frames):
        room = C.Room(w, h)
        cx, base = 8.0, 20.0
        room.ellipse(cx - 4.2, base - 2.6, cx + 4.2, base + 0.4, ramp(BRASS, 0.4))
        room.ellipse(cx - 3.8, base - 2.6, cx + 3.4, base - 0.8, ramp(BRASS, 0.72))
        room.ellipse(cx - 3.0, base - 2.4, cx - 0.6, base - 1.4, ramp(BRASS, 0.98))
        turned(room, cx, base - 7.0, base - 1.8, 1.0, 0.6, cols=BRASS,
               profile=[(0, 0.8), (0.2, 0.7), (0.4, 1.3), (0.55, 0.7), (0.8, 0.8), (1.0, 1.2)])
        room.ellipse(cx - 2.8, base - 8.4, cx + 2.8, base - 6.6, ramp(BRASS, 0.62))
        room.ellipse(cx - 2.4, base - 8.3, cx + 0.4, base - 7.4, ramp(BRASS, 0.95))
        room.rect(cx - 1.2, base - 9.6, cx + 1.2, base - 7.8, ramp(BRASS, 0.5))
        room.rect(cx - 1.2, base - 9.6, cx - 0.5, base - 7.8, ramp(BRASS, 0.82))
        # The candle, burnt half down, a drip down its side.
        room.rect(cx - 1.0, base - 14.6, cx + 1.0, base - 9.4, ramp(C.LINEN, 0.8))
        room.rect(cx - 1.0, base - 14.6, cx - 0.4, base - 9.4, ramp(C.LINEN, 0.97))
        room.rect(cx + 0.5, base - 14.6, cx + 1.0, base - 9.4, ramp(C.LINEN, 0.55))
        room.rect(cx + 0.6, base - 14.2, cx + 1.3, base - 11.6, ramp(C.LINEN, 0.88))
        room.rect(cx - 0.15, base - 15.6, cx + 0.2, base - 14.4, ramp(C.SOOT, 0.3))
        # The flame.
        lean = (0.0, 0.1, 1.5, -0.7)[k % 4]
        tall = (5.2, 3.4, 4.8, 4.4)[k % 4]
        width = (1.6, 2.4, 1.7, 1.9)[k % 4]
        fb = base - 14.8
        for scale, tone in ((1.0, 0.5), (0.74, 0.72), (0.5, 0.9), (0.26, 1.0)):
            half = width * scale
            room.poly([(cx - half, fb), (cx - half * 0.7, fb - tall * scale * 0.5),
                       (cx + lean * scale, fb - tall * scale), (cx + half * 0.7, fb - tall * scale * 0.5),
                       (cx + half, fb)], ramp(C.FIRE, tone))
        room.ellipse(cx - 1.4, fb - 1.4, cx + 1.4, fb + 0.4, ramp(C.FIRE, 0.6))
        out.paste(C.shrink(room, CANDLE_PALETTE, outline=0.3), (k * w * C.DENSITY, 0))
    return out


# ---------------------------------------------------------------- the upstairs hall

DOORS = (16, 64, 112, 160, 208)        # each door's left edge; 32 x 32 from y 16
DOOR_NUMBERS = ("1", "2", None, "3", "4")


def door_frame(room, x0, y0, x1, y1, rnd):
    """A door's oak casing: two jambs and a head, inside x0..x1, y0..y1."""
    C.timber(room, x0, y0, x0 + 2.4, y1, 0.46, rnd)
    C.timber(room, x1 - 2.4, y0, x1, y1, 0.4, rnd)
    C.timber(room, x0, y0, x1, y0 + 2.6, 0.5, rnd)


def hall_door(room, x0, y0, rnd):
    """A shut chamber door, 32 x 32 at (x0, y0): its casing, a leaf of
    beaded boards, strap hinges on the left with their nails, a thumb latch
    on the right, a sill worn in the middle."""
    x1, y1 = x0 + 32, y0 + 32
    door_frame(room, x0, y0, x1, y1, rnd)
    lx0, lx1, ly0 = x0 + 2.4, x1 - 2.4, y0 + 2.6
    n = 4
    bw = (lx1 - lx0) / n
    for i in range(n):
        a, b = lx0 + i * bw, lx0 + (i + 1) * bw
        t = 0.5 + rnd.uniform(-0.06, 0.06)
        room.rect(a, ly0, b, y1, ramp(C.OAK, t))
        for _ in range(2):
            gx, gy = rnd.uniform(a + 0.8, b - 0.8), rnd.uniform(ly0, y1 - 5)
            room.rect(gx, gy, gx + 0.25, gy + rnd.uniform(3, 9), ramp(C.OAK, t + rnd.choice((-0.1, 0.08))))
        room.rect(a, ly0, a + 0.6, y1, ramp(C.OAK, t + 0.24))
        room.rect(a + 0.6, ly0, a + 0.9, y1, ramp(C.OAK, 0.22))
        room.rect(b - 0.4, ly0, b, y1, ramp(C.OAK, 0.16))
    # The leaf is set back in its casing: the reveal shows dark along the
    # head and the left jamb, the casing's inner edge catches the light.
    room.rect(lx0, ly0, lx1, ly0 + 1.4, ramp(C.SOOT, 0.4))
    room.rect(lx0, ly0, lx0 + 1.2, y1, ramp(C.SOOT, 0.5))
    room.rect(lx1 - 0.5, ly0, lx1, y1, ramp(C.OAK, 0.66))
    for hy in (ly0 + 4.0, y1 - 6.5):
        room.rect(lx0, hy, lx0 + 13, hy + 2.0, ramp(C.IRON, 0.12))
        room.rect(lx0, hy, lx0 + 13, hy + 0.5, ramp(C.IRON, 0.5))
        room.ellipse(lx0 + 11.4, hy - 0.6, lx0 + 14.6, hy + 2.6, ramp(C.IRON, 0.12))
        for nx in (lx0 + 2.5, lx0 + 7.0):
            room.rect(nx, hy + 0.7, nx + 0.7, hy + 1.4, ramp(C.IRON, 0.6))
    lx = lx1 - 5.2
    ly = y0 + 17.0
    room.rect(lx - 1.0, ly - 3.6, lx + 1.0, ly + 4.0, ramp(C.IRON, 0.14))
    room.rect(lx - 3.0, ly - 0.6, lx + 3.6, ly + 0.8, ramp(C.IRON, 0.3))
    room.ellipse(lx + 2.4, ly - 1.8, lx + 4.8, ly + 1.4, ramp(C.IRON, 0.1))
    room.line([(lx - 0.2, ly + 1.0), (lx + 0.6, ly + 5.0)], ramp(C.IRON, 0.45), 0.7)


def hall_room(seed=801):
    """The upstairs hall, 256 x 128: the chamber doors along the back wall
    (rows 0-2), the walkway (rows 3-4) at the banister's foot, and the
    stairwell below it, the stair going down through the gap."""
    w, h, wall = 256, 128, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    # Below the walkway: the dark stairwell, the room below just showing
    # under it, its floorboards dim.
    room.rect(0, 80, w, h, ramp(C.SOOT, 0.25))
    C.floorboards(room, 0, 112, w, h, random.Random(seed + 1), board=5.0)
    shade = Image.new("L", room.lay.size, 0)
    sd = ImageDraw.Draw(shade)
    for i in range(16):
        y0 = 112 + i
        sd.rectangle((0, y0 * C.S, w * C.S, (y0 + 1) * C.S), fill=int(235 - i * 7))
    room.lay.paste(Image.new("RGBA", room.lay.size, C.B.rgb(C.SOOT[0])), (0, 0), shade)
    C.floorboards(room, 0, wall, w, 81, rnd)
    C.plaster_wall(room, 0, w, 0, wall - 2, rnd)
    wainscot(room, 0, w, 33, wall - 2, rnd)
    girt_and_joists(room, 0, w, rnd)
    for px in (0, 54, 102, 150, 198, 252):
        C.timber(room, px, 8, px + 4, wall - 2, 0.4, rnd)
        if 0 < px < 252:
            sconce(room, px + 2, 26)
    for x0, num in zip(DOORS, DOOR_NUMBERS):
        hall_door(room, x0, 16, rnd)
        if num:
            # The chamber's figure, painted on a little board over the door.
            bx = x0 + 13
            room.rect(bx - 0.6, 8.6, bx + 6.6, 15.6, ramp(C.OAK, 0.25))
            room.rect(bx, 9.2, bx + 6, 15.0, ramp(PAINT, 0.38))
            room.rect(bx, 9.2, bx + 6, 9.7, ramp(PAINT, 0.62))
            numeral(room, bx + 1.5, 9.8, num, ramp(C.LINEN, 0.9))
    C.wall_foot(room, 0, w, wall)
    for cx in (32, 80, 128, 176, 224):
        C.wear(room, cx, 62, 10, 7, 36)
    # The walkway's edge: the floor's thickness, a beam face across, broken
    # by the stair's gap.
    for a, b in ((0, 115), (141, w)):
        C.timber(room, a, 80, b, 84.5, 0.36, rnd)
    # The stair down: treads and risers stacked, darker as they go down,
    # its strings at each side.
    y = 80.0
    k = 0
    while y < h:
        t = max(0.08, 0.62 - k * 0.07)
        room.rect(116, y, 140, y + 3.0, ramp(C.OAK, t))
        room.rect(116, y, 140, y + 0.6, ramp(C.OAK, t + 0.22))
        room.rect(116, y + 3.0, 140, y + 6.0, ramp(C.OAK, t - 0.18))
        room.rect(116, y + 5.6, 140, y + 6.0, ramp(C.OAK, max(0.0, t - 0.34)))
        gx = rnd.uniform(119, 133)
        room.rect(gx, y + 1.0, gx + rnd.uniform(3, 6), y + 1.3, ramp(C.OAK, t - 0.08))
        y += 6.0
        k += 1
    C.wear(room, 128, 92, 6, 10, 20)
    for sx, tone in ((112.5, 0.4), (139.5, 0.3)):
        room.rect(sx, 80, sx + 4, h, ramp(C.OAK, tone))
        room.rect(sx, 80, sx + 0.8, h, ramp(C.OAK, tone + 0.3))
        room.rect(sx + 3.3, 80, sx + 4, h, ramp(C.OAK, tone - 0.2))
    # Shadows under the banister along the walkway's edge.
    for a, b in ((0, 116), (140, w)):
        room.shadow(a, 76, b, 81, 140)
    return C.shrink(room, ROOM_PALETTE)


def banister(room, x, y, w, h, rnd):
    """A run of banister along the walkway's edge, seen from the front: a
    moulded handrail on turned balusters over a bottom rail, a square newel
    post at each end."""
    top, foot = y, y + h - 1
    room.rect(x + 2, foot - 2.6, x + w - 2, foot - 0.6, ramp(C.OAK, 0.42))
    room.rect(x + 2, foot - 2.6, x + w - 2, foot - 2.0, ramp(C.OAK, 0.72))
    n = int((w - 6) / 7.0)
    step = (w - 6) / n
    for i in range(n):
        turned(room, x + 3 + (i + 0.5) * step, top + 2.6, foot - 2.4, 1.4, 0.48, lit=0.14,
               profile=[(0, 0.55), (0.12, 0.55), (0.22, 1.0), (0.55, 1.0), (0.72, 0.55), (0.88, 0.55), (1.0, 0.8)])
    room.rect(x + 1, top + 0.8, x + w - 1, top + 3.0, ramp(C.OAK, 0.56))
    room.rect(x + 1, top + 0.8, x + w - 1, top + 1.4, ramp(C.OAK, 0.78))
    room.rect(x + 1, top + 2.5, x + w - 1, top + 3.0, ramp(C.OAK, 0.24))
    for px in (x, x + w - 3.2):
        room.rect(px, top, px + 3.2, foot + 1, ramp(C.OAK, 0.5))
        room.rect(px, top, px + 1.0, foot + 1, ramp(C.OAK, 0.7))
        room.rect(px + 2.4, top, px + 3.2, foot + 1, ramp(C.OAK, 0.26))
        room.rect(px, top, px + 3.2, top + 1.0, ramp(C.OAK, 0.8))
        room.rect(px, top + 4.0, px + 3.2, top + 4.6, ramp(C.OAK, 0.3))


def hall_pieces():
    return pieces_sheet("inn_hall", 802, outline=0.3, ring=0.12)


def hall_door_open(seed=803):
    """A vacant chamber's door standing open, 32 x 32, drawn over the shut
    door: the same casing; through it the dim chamber, its floorboards and a
    patch of window light; the leaf swung in against the left jamb."""
    rnd = random.Random(seed)
    room = C.Room(32, 32)
    room.rect(2.4, 2.6, 29.6, 32, ramp(C.SOOT, 0.4))
    room.rect(2.4, 2.6, 29.6, 20, ramp(C.PLASTER, 0.12))
    room.rect(2.4, 19, 29.6, 20, ramp(C.TIMBER, 0.2))
    for k, yy in enumerate((20, 23, 26.5, 30)):
        room.rect(2.4, yy, 29.6, yy + 3.2, ramp(C.FLOOR, 0.22 + k * 0.06))
        room.rect(2.4, yy + 3.0, 29.6, yy + 3.4, ramp(C.FLOOR, 0.08))
    # Window light from the chamber's casement lying across its floor.
    room.poly([(16, 20.5), (24, 20.5), (29.6, 31.6), (20, 31.6)], ramp(C.FLOOR, 0.4))
    room.poly([(17.5, 21), (22.5, 21), (26.5, 30.5), (21, 30.5)], ramp(C.FLOOR, 0.46))
    room.rect(17, 6, 24, 15, ramp(C.GLASS, 0.42))
    room.rect(17, 6, 24, 9, ramp(C.GLASS, 0.55))
    room.rect(20.2, 6, 20.8, 15, ramp(C.GLASS, 0.12))
    room.rect(16.4, 5.4, 24.6, 6.0, ramp(C.TIMBER, 0.3))
    room.rect(16.4, 15, 24.6, 15.8, ramp(C.TIMBER, 0.35))
    # The leaf, swung in on its hinges: its face foreshortened, darker
    # toward its far edge, a hinge strap showing.
    room.poly([(2.4, 2.6), (8.6, 5.0), (8.6, 29.4), (2.4, 32)], ramp(C.OAK, 0.48))
    room.poly([(2.4, 2.6), (4.2, 3.3), (4.2, 32), (2.4, 32)], ramp(C.OAK, 0.56))
    room.rect(8.0, 5.0, 8.6, 29.4, ramp(C.OAK, 0.2))
    room.rect(8.6, 5.0, 9.4, 29.4, ramp(C.SOOT, 0.0))
    for hy in (7.5, 24.5):
        room.poly([(2.4, hy), (8.0, hy + 1.0), (8.0, hy + 2.0), (2.4, hy + 1.4)], ramp(C.IRON, 0.35))
    door_frame(room, 0, 0, 32, 32, rnd)
    return C.shrink(room, ROOM_PALETTE)


# ---------------------------------------------------------------- the guest room

def looking_glass(room, x0, y0, x1, y1):
    """A small looking glass in a walnut frame with a cresting, on a nail."""
    cx = (x0 + x1) / 2
    room.line([(cx, y0 - 5), (cx - 3, y0 - 1)], ramp(C.SOOT, 0.6), 0.25)
    room.line([(cx, y0 - 5), (cx + 3, y0 - 1)], ramp(C.SOOT, 0.6), 0.25)
    room.poly([(x0 + 1, y0), (cx - 2, y0 - 2.4), (cx, y0 - 3.6), (cx + 2, y0 - 2.4), (x1 - 1, y0)], ramp(C.OAK, 0.4))
    room.rect(x0 + 0.5, y0 + 0.6, x1 + 0.6, y1 + 0.8, ramp(C.PLASTER, 0.2))
    room.rect(x0, y0, x1, y1, ramp(C.OAK, 0.2))
    room.rect(x0, y0, x1, y0 + 0.6, ramp(C.OAK, 0.5))
    room.rect(x0, y0, x0 + 0.6, y1, ramp(C.OAK, 0.45))
    room.rect(x0 + 1.6, y0 + 1.6, x1 - 1.6, y1 - 1.6, ramp(C.GLASS, 0.4))
    room.poly([(x0 + 1.6, y0 + 1.6), (x0 + 4.6, y0 + 1.6), (x0 + 1.6, y0 + 6.4)], ramp(C.GLASS, 0.95))
    room.line([(x0 + 4.0, y1 - 1.8), (x1 - 1.8, y0 + 4.4)], ramp(C.GLASS, 0.78), 0.5)
    room.rect(x0 + 1.6, y1 - 3.0, x1 - 1.6, y1 - 1.6, ramp(C.GLASS, 0.2))


def braided_rug(room, x0, y0, x1, y1, rnd):
    """A braided rug of old cloth by the bed, worn and close to the floor in
    tone: rings of dull wool and linen, each a little out of true, the braid
    a run of slanted ticks."""
    cols = ((FADED, 0.42), (C.STRAW, 0.32), (C.INDIGO, 0.3), (C.LINEN, 0.3), (FADED, 0.5), (C.STRAW, 0.38))
    cx, cy, rx, ry = (x0 + x1) / 2, (y0 + y1) / 2, (x1 - x0) / 2, (y1 - y0) / 2
    room.ellipse(x0 + 0.6, y0 + 0.9, x1 + 0.6, y1 + 0.9, ramp(C.FLOOR, 0.14))
    rings = 6
    for i in range(rings):
        f = 1 - i / rings
        col, tone = cols[i % len(cols)]
        n = 28
        wob = [rnd.uniform(-0.04, 0.04) for _ in range(n)]
        pts = [(cx + rx * (f + wob[k]) * math.cos(2 * math.pi * k / n), cy + ry * (f + wob[k]) * math.sin(2 * math.pi * k / n))
               for k in range(n)]
        room.poly(pts, ramp(col, tone))
        m = int(40 * f) + 8
        for k in range(m):
            a = 2 * math.pi * (k + 0.5 * (i % 2)) / m
            px, py = cx + rx * (f - 0.07) * math.cos(a), cy + ry * (f - 0.09) * math.sin(a)
            room.line([(px - 0.4, py - 0.3), (px + 0.4, py + 0.3)], ramp(col, tone - 0.14), 0.3)


def guest_room(seed=847):
    """A guest chamber, 112 x 128: the back wall (rows 0-2) with the
    casement over the candle stand, the looking glass; the bed to the left,
    the chest of drawers in the right corner; the door at the bottom."""
    w, h, wall = 112, 128, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd)
    C.plaster_wall(room, 0, w, 0, wall - 2, rnd)
    wainscot(room, 0, w, 33, wall - 2, rnd)
    girt_and_joists(room, 0, w, rnd)
    for px in (0, 66, 108):
        C.timber(room, px, 8, px + 4, wall - 2, 0.4, rnd)
    C.casement(room, 48, 14, 62, 26)
    looking_glass(room, 78, 15, 88, 27)
    C.peg_rail(room, 14, 44, 12, rnd, ("hat", "cloak", "hat"))
    C.wall_foot(room, 0, w, wall)
    C.light_pool(room, 48, wall + 1, 62, wall + 22, 74)
    C.wear(room, 60, 100, 16, 12, 34)
    braided_rug(room, 50, 76, 78, 92, rnd)
    # The threshold of the door to the hall.
    room.rect(62, 124.5, 82, 128, ramp(C.TIMBER, 0.45))
    room.rect(62, 124.5, 82, 125.2, ramp(C.TIMBER, 0.75))
    for x0, y0, x1, y1 in ((18, 86, 51, 91), (18, 115, 50, 120), (50, 65, 66, 70), (98, 69, 112, 74),
                           (83, 103, 97, 108)):
        room.shadow(x0, y0, x1, y1, 240)
    return C.shrink(room, ROOM_PALETTE)


def candle_stand(room, x, y, w, h, rnd):
    """A small joined stand by the bed: a top with a pewter candlestick and
    a little book on it, a drawer with a brass drop, turned legs and a
    stretcher."""
    top0, top1 = y + 18, y + 24
    for lx in (x + 1.8, x + w - 1.8):
        turned(room, lx, top1 + 5.5, y + h - 0.5, 1.0, 0.5)
    room.rect(x + 2, y + h - 5, x + w - 2, y + h - 4, ramp(C.OAK, 0.34))
    room.rect(x + 0.6, top1, x + w - 0.6, top1 + 5.5, ramp(C.OAK, 0.48))
    room.rect(x + 1.6, top1 + 1.0, x + w - 1.6, top1 + 4.6, ramp(C.OAK, 0.4))
    room.rect(x + 1.6, top1 + 1.0, x + w - 1.6, top1 + 1.4, ramp(C.OAK, 0.2))
    room.ellipse(x + w / 2 - 0.9, top1 + 2.2, x + w / 2 + 0.9, top1 + 4.2, ramp(BRASS, 0.45))
    room.rect(x, top0, x + w, top1, ramp(C.OAK, 0.76))
    room.rect(x, top0, x + w, top0 + 0.6, ramp(C.OAK, 0.98))
    room.rect(x, top1 - 0.8, x + w, top1 + 0.2, ramp(C.OAK, 0.3))
    # A little book, and the candlestick with its candle.
    room.rect(x + 1.6, top0 + 1.6, x + 6.6, top0 + 4.6, ramp(LEATHER, 0.3))
    room.rect(x + 1.6, top0 + 3.8, x + 6.6, top0 + 4.6, ramp(C.LINEN, 0.75))
    cx = x + w - 5
    room.ellipse(cx - 2.6, top0 + 2.6, cx + 2.6, top0 + 4.6, ramp(C.PEWTER, 0.5))
    room.ellipse(cx - 2.2, top0 + 2.6, cx + 1.6, top0 + 3.8, ramp(C.PEWTER, 0.85))
    turned(room, cx, top0 - 4.0, top0 + 3.2, 0.7, 0.55, cols=C.PEWTER)
    room.ellipse(cx - 1.6, top0 - 4.8, cx + 1.6, top0 - 3.6, ramp(C.PEWTER, 0.75))
    room.rect(cx - 0.8, top0 - 11.0, cx + 0.8, top0 - 4.4, ramp(C.LINEN, 0.85))
    room.rect(cx - 0.8, top0 - 11.0, cx - 0.3, top0 - 4.4, ramp(C.LINEN, 0.97))
    room.rect(cx - 0.12, top0 - 12.0, cx + 0.2, top0 - 10.8, ramp(C.SOOT, 0.3))


def chest_of_drawers(room, x, y, w, h, rnd):
    """A tall chest of drawers in the corner, seen from the front: a pewter
    basin and ewer on its top; a moulded cornice; four graduated drawers
    with brass drops; a base moulding on turned feet."""
    top = y + 9
    # The ewer and basin.
    room.ellipse(x + 1.4, top - 2.6, x + 11.2, top + 0.8, ramp(C.PEWTER, 0.5))
    room.ellipse(x + 2.2, top - 2.4, x + 10.4, top - 0.6, ramp(C.PEWTER, 0.3))
    ex = x + w - 5.6
    room.ellipse(ex - 2.4, top - 6.4, ex + 2.4, top + 0.2, ramp(C.PEWTER, 0.58))
    room.ellipse(ex - 1.9, top - 5.6, ex - 0.5, top - 2.4, ramp(C.PEWTER, 0.95))
    room.poly([(ex - 1.6, top - 6.0), (ex + 1.4, top - 6.0), (ex + 2.8, top - 8.4), (ex - 1.2, top - 8.0)],
              ramp(C.PEWTER, 0.66))
    room.line([(ex + 2.0, top - 5.6), (ex + 3.4, top - 4.4), (ex + 2.2, top - 1.6)], ramp(C.PEWTER, 0.4), 0.45)
    # The case.
    room.rect(x, top, x + w, top + 2.6, ramp(C.OAK, 0.78))
    room.rect(x, top, x + w, top + 0.7, ramp(C.OAK, 0.98))
    room.rect(x, top + 1.8, x + w, top + 2.6, ramp(C.OAK, 0.3))
    feet = y + h - 4.0
    room.rect(x + 0.6, top + 2.6, x + w - 0.6, feet - 2.0, ramp(C.OAK, 0.22))
    room.rect(x + 0.6, top + 2.6, x + 1.4, feet - 2.0, ramp(C.OAK, 0.6))
    dy = top + 3.6
    for k, dh in enumerate((6.0, 7.0, 8.0, 9.0)):
        room.rect(x + 1.8, dy, x + w - 2.6, dy + dh - 1.0, ramp(C.OAK, 0.56))
        room.rect(x + 1.8, dy, x + w - 2.6, dy + 0.5, ramp(C.OAK, 0.8))
        room.rect(x + 1.8, dy + dh - 1.5, x + w - 2.6, dy + dh - 1.0, ramp(C.OAK, 0.36))
        for px in (x + 4.6, x + w - 5.4):
            room.rect(px - 0.9, dy + dh / 2 - 1.0, px + 0.9, dy + dh / 2 - 0.4, ramp(BRASS, 0.32))
            room.ellipse(px - 0.5, dy + dh / 2 - 0.6, px + 0.5, dy + dh / 2 + 1.6, ramp(BRASS, 0.5))
        dy += dh
    # The case's right side in shade.
    room.rect(x + w - 2.0, top + 2.6, x + w - 0.4, feet - 2.0, ramp(C.OAK, 0.16))
    room.rect(x, feet - 2.0, x + w, feet, ramp(C.OAK, 0.66))
    room.rect(x, feet - 0.6, x + w, feet, ramp(C.OAK, 0.26))
    for fx in (x + 2.0, x + w - 2.0):
        room.ellipse(fx - 1.8, feet - 0.4, fx + 1.8, y + h, ramp(C.OAK, 0.4))
        room.ellipse(fx - 1.2, feet - 0.2, fx - 0.2, feet + 1.2, ramp(C.OAK, 0.66))


def trunk(room, x, y, w, h, rnd):
    """A traveller's trunk at the bed's foot, seen from the front at a slant:
    hide over pine, the domed lid's top, brass nails along its edges, iron
    bands, a lock plate and a hasp, a leather handle at the end."""
    lid = y + 6.5
    room.d.rounded_rectangle((x * C.S, (y + 0.6) * C.S, (x + w) * C.S, (lid + 1.0) * C.S),
                             radius=int(2.8 * C.S), fill=ramp(LEATHER, 0.62))
    room.rect(x + 1.0, y + 1.2, x + w - 1.0, y + 2.4, ramp(LEATHER, 0.88))
    room.rect(x, lid, x + w, y + h - 0.6, ramp(LEATHER, 0.42))
    room.rect(x, lid, x + w, lid + 0.6, ramp(LEATHER, 0.15))
    room.rect(x, y + h - 1.6, x + w, y + h - 0.6, ramp(LEATHER, 0.2))
    for k in range(int(w / 1.6)):
        nx = x + 0.8 + k * 1.6
        room.rect(nx, lid - 1.2, nx + 0.5, lid - 0.7, ramp(BRASS, 0.85))
        room.rect(nx, lid + 1.0, nx + 0.5, lid + 1.5, ramp(BRASS, 0.7))
        room.rect(nx, y + h - 2.6, nx + 0.5, y + h - 2.1, ramp(BRASS, 0.6))
    for bx in (x + 5.0, x + w - 7.0):
        room.rect(bx, y + 0.8, bx + 2.0, y + h - 0.6, ramp(C.IRON, 0.32))
        room.rect(bx, y + 0.8, bx + 0.6, y + h - 0.6, ramp(C.IRON, 0.6))
    cx = x + w / 2
    room.rect(cx - 2.0, lid + 0.8, cx + 2.0, lid + 5.0, ramp(BRASS, 0.4))
    room.rect(cx - 2.0, lid + 0.8, cx - 1.2, lid + 5.0, ramp(BRASS, 0.62))
    room.rect(cx - 0.3, lid + 2.2, cx + 0.4, lid + 3.4, ramp(C.SOOT, 0.2))
    room.rect(cx - 0.8, lid - 2.4, cx + 0.8, lid + 1.6, ramp(C.IRON, 0.45))
    room.line([(x + w - 1.4, lid + 1.6), (x + w - 0.7, lid + 3.6), (x + w - 1.4, lid + 5.4)], ramp(LEATHER, 0.3), 0.6)


def weave(room, x0, y0, x1, y1, rnd):
    """Break the guest coverlet's even check into homespun: its pale cross
    threads (the kit's undyed linen) dyed down toward the madder-brown in
    uneven runs, and short weft slubs over the cloth."""
    pale = ramp(C.LINEN, 0.45)
    box = tuple(int(round(v * C.S)) for v in (x0, y0, x1, y1))
    px = room.lay.load()
    run = 3 * C.S
    for y in range(box[1], box[3]):
        for x in range(box[0], box[2]):
            c = px[x, y]
            # The thread's colour, or the thread brightened where the light
            # lies on the mattress.
            if c[3] and sum(abs(c[i] - pale[i]) for i in range(3)) < 60:
                seg = random.Random(hash((x // run, y // run, 969)) & 0xffff).uniform(0.46, 0.7)
                px[x, y] = ramp(FADED, seg)
    for _ in range(int((x1 - x0) * (y1 - y0) / 6)):
        sx, sy = rnd.uniform(x0 + 3.8, x1 - 5.8), rnd.uniform(y0 + 0.5, y1 - 0.3)
        room.rect(sx, sy, sx + rnd.uniform(0.8, 2.2), sy + 0.25, ramp(FADED, rnd.choice((0.3, 0.52, 0.62))))


def guest_bed(room, x, y, w, h, rnd):
    """The guest bed under its faded check coverlet, the check woven down."""
    C.rope_bed(room, x, y, w, h, FADED, rnd, "check")
    weave(room, x + 1.6, y + 12, x + w - 1.6, y + h - 12.5, rnd)


def guest_pieces():
    return pieces_sheet("inn_guest", 848)


# ---------------------------------------------------------------- sheets and checks

SHEETS = {
    "inn_common": common_room,
    "inn_pieces": common_pieces,
    "inn_fire": inn_fire,
    "inn_pot": pot_strip,
    "inn_candle": candle_strip,
    "inn_hall_common": hall_room,
    "inn_hall_pieces": hall_pieces,
    "inn_hall_door_open": hall_door_open,
    "inn_guest_common": guest_room,
    "inn_guest_pieces": guest_pieces,
}
FIRE = "res://assets/interiors/inn_fire.png"
POT = "res://assets/interiors/inn_pot.png"
CANDLE = "res://assets/interiors/inn_candle.png"


def _ladder_chair(back_left):
    return lambda room, x, y, w, h, rnd: C.ladder_chair(room, x, y, w, h, back_left=back_left)


# Each room's geometry, declared once: the painters draw from it and --check
# requires its interior_rooms.gd entry to say exactly the same. pieces are
# ((x, y, w, h, sort_y), painter), the rects and sort lines the engine's
# floor plans were laid out against, disjoint because the client cuts each
# sprite from its own pixels; anims (sheet, frames, x, y, sort_y or None);
# bar/reach where a sale is set on the desk and where the keeper steps to
# set it there.
ROOMS = {
    INN_KEY: {
        "sheets": ("inn_common", "inn_pieces"),
        "size": (128, 112),
        "bar": (50, 70),
        "reach": (48, 66),
        "pieces": [
            ((20, 54, 48, 36, 89), keeper_desk),
            # The table stops where the form begins (the pack's rects
            # overlapped by 8 px).
            ((74, 74, 48, 24, 104), joined_table),
            ((74, 98, 48, 16, 109), form),
            # The great chair stops where the desk begins and the basket
            # starts below the desk (the pack's chair and basket overlapped
            # the desk and each other).
            ((0, 78, 20, 32, 101), great_chair),
            ((26, 90, 16, 22, 111), work_basket),
        ],
        "covers": {},
        "anims": [(FIRE, 4, 96, 38, None), (POT, 4, 66, 50, 81), (CANDLE, 4, 24, 44, 90)],
    },
    "inn_hall": {
        "sheets": ("inn_hall_common", "inn_hall_pieces"),
        "door_open": "inn_hall_door_open",
        "size": (256, 128),
        "pieces": [((0, 63, 115, 19, 80), banister), ((141, 63, 115, 19, 80), banister)],
        "covers": {},
        "anims": [],
    },
    "inn_guest": {
        "sheets": ("inn_guest_common", "inn_guest_pieces"),
        "size": (112, 128),
        "pieces": [
            ((16, 44, 32, 44, 71), guest_bed),
            ((16, 102, 32, 16, 117), trunk),
            ((48, 32, 16, 36, 67), candle_stand),
            ((96, 24, 16, 48, 71), chest_of_drawers),
            ((81, 80, 14, 27, 101), _ladder_chair(False)),
        ],
        "covers": {"bed": (16, 56, 32, 32)},
        "anims": [],
    },
}
# Art px: the rooms at twice their room px; each anim strip its frames of
# the frame's room px at twice; the open door 32 x 32 room px at twice.
SIZES = {
    "inn_common": (256, 224), "inn_pieces": (256, 224),
    "inn_fire": (4 * 32, 32), "inn_pot": (4 * 64, 64), "inn_candle": (4 * 32, 64),
    "inn_hall_common": (512, 256), "inn_hall_pieces": (512, 256), "inn_hall_door_open": (64, 64),
    "inn_guest_common": (224, 256), "inn_guest_pieces": (224, 256),
}
# Each anim's frame in room px.
FRAMES = {"inn_fire": (16, 16), "inn_pot": (32, 32), "inn_candle": (16, 32)}
# The open door is drawn at its way's tap rect + (2, 2): the closed door's
# art, 32 x 32 room px.
DOOR_OPEN_PX = (32, 32)


def entry_extras(key, path=C.ROOMS_GD):
    """What C.room_entry does not read from the room's entry: bar, reach,
    door_open, and each anim's sort line (None for a wall anim)."""
    text = open(path, encoding="utf-8").read()
    body = re.search(r'\n    "%s": \{(.*?)\n    \},' % re.escape(key), text, re.S).group(1)

    def point(field):
        m = re.search(r'"%s": \{"x": (-?\d+), "y": (-?\d+)\}' % field, body)
        return (int(m.group(1)), int(m.group(2))) if m else None

    door = re.search(r'"door_open": "([^"]+)"', body)
    sorts = [int(v) if v else None for v in re.findall(r'\{"sheet": "[^"]+", [^}]*?(?:"sort_y": (\d+))?\}', body)]
    return {"bar": point("bar"), "reach": point("reach"), "door_open": door.group(1) if door else None, "anim_sorts": sorts}


def check(out_dir):
    """Each sheet is exactly what this script paints now and of its size;
    each room's entry in interior_rooms.gd names these sheets at this
    density and says exactly what ROOMS declares: size, pieces (rects and
    sort lines, in order), covers, anims (sheet, frames, position, sort
    line), bar, reach and door_open; every cover rect lies inside its bed's
    piece and holds the coverlet; every anim strip is whole frames of its
    declared size."""
    ok = True
    sheets = {}
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun inn.py)" % name)
            ok = False
        if sheet.size != SIZES[name]:
            print("%s: %dx%d, not %dx%d" % ((name,) + sheet.size + SIZES[name]))
            ok = False
        sheets[name] = sheet
    d = C.DENSITY
    res = "res://assets/interiors/%s.png"
    for key, spec in ROOMS.items():
        entry = C.room_entry(key)
        extra = entry_extras(key)
        common, pieces = spec["sheets"]
        door = spec.get("door_open")
        for label, got, expect in (
                ("sheets", entry["sheets"], {"sheet": res % common, "pieces_sheet": res % pieces}),
                ("density", entry["density"], d),
                ("size", entry["size"], spec["size"]),
                ("pieces", entry["pieces"], [r for r, _p in spec["pieces"]]),
                ("covers", entry["covers"], spec["covers"]),
                ("anims", entry["anims"], [a[:4] for a in spec["anims"]]),
                ("anim sort lines", extra["anim_sorts"], [a[4] for a in spec["anims"]]),
                ("bar", extra["bar"], spec.get("bar")),
                ("reach", extra["reach"], spec.get("reach")),
                ("door_open", extra["door_open"], res % door if door else None)):
            if got != expect:
                print("%s: interior_rooms.gd %s are %s, the painted %s" % (key, label, got, expect))
                ok = False
        for name in (common, pieces):
            if sheets[name].size != (spec["size"][0] * d, spec["size"][1] * d):
                print("%s: %s is %s, not the room's size at density %d" % (key, name, sheets[name].size, d))
                ok = False
        if door and sheets[door].size != (DOOR_OPEN_PX[0] * d, DOOR_OPEN_PX[1] * d):
            print("%s: %s is not %s room px at density %d" % (key, door, DOOR_OPEN_PX, d))
            ok = False
        alpha = sheets[pieces].split()[3]
        for bed, (x, y, w, h) in spec["covers"].items():
            inside = any(px <= x and py <= y and x + w <= px + pw and y + h <= py + ph
                         for (px, py, pw, ph, _s), _p in spec["pieces"])
            filled = alpha.crop(((x + 4) * d, (y + 2) * d, (x + w - 4) * d, (y + h - 8) * d)).getextrema()[0] == 255
            if not inside or not filled:
                print("%s: cover %s %s is not a covered part of a bed piece" % (key, bed, (x, y, w, h)))
                ok = False
        for path, frames, _x, _y, _sort in spec["anims"]:
            name = os.path.splitext(os.path.basename(path))[0]
            fw, fh = FRAMES[name]
            if path != res % name or sheets[name].size != (frames * fw * d, fh * d):
                print("%s: anim %s is not %d frames of %dx%d room px painted here" % (key, path, frames, fw, fh))
                ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's Inn interiors.")
    parser.add_argument("--out", default=C.OUT_DIR, help="where the sheets go (default client/assets/interiors)")
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
