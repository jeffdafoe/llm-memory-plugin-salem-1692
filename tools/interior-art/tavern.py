"""Paints salem's Tavern interiors (LLM-969): the common room, the upstairs
hall and the guest room. Every pixel is drawn here, no pack art.

    python tools/interior-art/tavern.py
    python tools/interior-art/tavern.py --out DIR   # elsewhere (prototypes)
    python tools/interior-art/tavern.py --check     # a fresh paint, sizes, entries

Writes client/assets/interiors/tavern_*.png: for each room its _common and
_pieces sheets, the hall's _door_open (a vacant room's door standing open,
drawn over the closed door at its tap rect) and tavern_fire.png, the hearth
fire (4 frames of 24 x 18 room px). See common.py for the view, the sheets
and the kit.

The Tavern is the Black House: a rougher public house than the Inn. Walls of
hewn logs, smoke-dark, chinked with daub; a big fieldstone hearth; a plank
bar in a C with casks behind it, pewter tankards and green onion bottles; a
round table worn by elbows; sanded boards stained with ale. Upstairs, a dark
log hall of plank doors with chalked numbers, and a shabby guest room: a
plank bed with a patched blanket over a straw tick, crates for a chest, a
barrel and a sack, the shutters closed, cobwebs.

The pieces stand exactly where interior_rooms.gd's entries say (rects, sort
lines, covers, and the common room's bar and reach), which the floor plans'
stations, the keeper's place in the C bar and the sale are laid out against;
--check holds each entry to what is declared here.
"""

import argparse
import math
import os
import random
import re
import sys

from PIL import Image, ImageDraw, ImageFilter

import common as C
from common import ramp

C.set_density(2)

# Hewn logs smoked grey-brown; a wool blanket gone dingy with use. The rooms'
# palette takes in the logs and the fire's glow, the pieces' the glass of the
# bottles and a candle's flame.
LOG = ["14110e", "1e1a15", "2a241d", "373027", "463d32", "574c3f", "6a5e4e", "807260"]
DINGY = ["2e2620", "443a30", "5c4e40", "746452", "8c7a64", "a08e74"]
# Spanish brown door paint, weathered toward the wood.
DOOR = ["24160f", "341f15", "462a1c", "583626", "6a4430", "7c533c"]
ROOM_PALETTE = C.ROOM_PALETTE + LOG + DINGY + DOOR + C.FIRE[2:]
PIECE_PALETTE = C.PIECE_PALETTE + C.GLASS + C.SOOT + C.HERB + DINGY + C.FIRE[2:]

# The hearth fire: its anim entry in interior_rooms.gd exactly (a wall anim,
# so no sort_y), and the size of one frame in room px. tavern_fire.png is
# painted from it and --check holds the entry to it.
FIRE = {"sheet": "res://assets/interiors/tavern_fire.png", "frames": 4, "fps": 8.0, "x": 40, "y": 46}
FIRE_FRAME = (24, 18)
COMMON_KEY = "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9"
STAIR_W = 28                # the stair passage at the common room's left (build_rooms.gd TAVERN_STAIR_W)
HALL_PLATES = ("I", "II", "", "III", "IV")   # doors x = 16 + 48 i; the keeper's quarters in the middle
HALL_EDGE = 80              # the walkway's front edge
HALL_GAP = (112, 144)       # the head of the stairs, between the banisters


# ---------------------------------------------------------------- shared tavern painters

def wash(room, shape, box, col, alpha, blur=0.0):
    """Lay col over the canvas through a soft mask (an "ellipse" or a "rect",
    box in room px): grime, firelight, spilled ale. Only on a room sheet,
    which is opaque everywhere."""
    mask = Image.new("L", room.lay.size, 0)
    b = tuple(v * C.S for v in box)
    getattr(ImageDraw.Draw(mask), "ellipse" if shape == "ellipse" else "rectangle")(b, fill=alpha)
    if blur:
        mask = mask.filter(ImageFilter.GaussianBlur(blur * C.S))
    room.lay.paste(Image.new("RGBA", room.lay.size, C.B.rgb(col)), (0, 0), mask)


def log_wall(room, x0, x1, top, bottom, rnd, tone=0.42):
    """Hewn logs laid one on another, smoked grey-brown: each course a run of
    logs of uneven length and height, butted end to end; each face lit along
    its top and shaded under it, with long grain, a few adze marks, a check,
    now and then a knot; dark seams between the courses, the daub left in
    them only here and there."""
    y = top
    while y < bottom - 0.5:
        h = min(rnd.uniform(5.4, 8.2), bottom - y)
        room.rect(x0, y, x1, y + 0.9, ramp(LOG, 0.03))
        ly = y + 0.9
        x = x0 - rnd.uniform(0, 60)
        while x < x1:
            length = rnd.uniform(44, 120)
            a, b = max(x0, x), min(x1, x + length)
            if b - a > 0.5:
                t = tone + rnd.uniform(-0.07, 0.07)
                room.rect(a, ly, b, y + h, ramp(LOG, t))
                room.rect(a, ly, b, ly + 0.8, ramp(LOG, t + 0.2))
                room.rect(a, ly + 0.8, b, ly + 1.8, ramp(LOG, t + 0.08))
                room.rect(a, y + h - 1.3, b, y + h, ramp(LOG, t - 0.2))
                for _ in range(int((b - a) / 7)):
                    gy, gx = rnd.uniform(ly + 1.8, y + h - 1.4), rnd.uniform(a, max(a, b - 8))
                    room.rect(gx, gy, min(b, gx + rnd.uniform(6, 18)), gy + 0.25, ramp(LOG, t + rnd.choice((-0.08, 0.06))))
                for _ in range(int((b - a) / 16)):
                    ax = rnd.uniform(a + 1, max(a + 1, b - 2))
                    room.line([(ax, ly + 1.8), (ax + 0.7, y + h - 1.4)], ramp(LOG, t - 0.1), 0.3)
                if b - a > 30 and rnd.random() < 0.6:
                    cx = rnd.uniform(a + 2, b - 24)
                    cy = ly + (y + h - ly) * rnd.uniform(0.45, 0.6)
                    room.rect(cx, cy, cx + rnd.uniform(10, 22), cy + 0.3, ramp(LOG, 0.02))
                if rnd.random() < 0.3:
                    kx, ky = rnd.uniform(a + 3, max(a + 3, b - 3)), ly + (y + h - ly) * 0.5
                    room.ellipse(kx - 1.1, ky - 0.8, kx + 1.1, ky + 0.8, ramp(LOG, t - 0.16))
                    room.ellipse(kx - 0.5, ky - 0.4, kx + 0.4, ky + 0.3, ramp(LOG, t + 0.1))
                if b < x1:
                    # The butt joint: a dark seam, the next log's end grain.
                    room.rect(b - 0.5, ly, b, y + h, ramp(LOG, 0.02))
                    room.rect(b, ly, b + 0.8, y + h, ramp(LOG, t + 0.16))
            x += length
        for _ in range(max(1, int((x1 - x0) / 45))):
            dx = rnd.uniform(x0, x1 - 4)
            room.rect(dx, y + 0.1, dx + rnd.uniform(2, 5), y + 0.9, ramp(C.PLASTER, rnd.uniform(0.22, 0.34)))
        y += h


def grime(room, x0, y0, x1, y1, alpha):
    wash(room, "rect", (x0, y0, x1, y1), C.SOOT[1], alpha)


def stains(room, spots, rnd):
    """Old ale and water stains on the boards: dark, soft-edged, irregular."""
    for cx, cy, rx, ry in spots:
        for _ in range(3):
            ox, oy = rnd.uniform(-rx * 0.3, rx * 0.3), rnd.uniform(-ry * 0.3, ry * 0.3)
            wash(room, "ellipse", (cx + ox - rx, cy + oy - ry, cx + ox + rx, cy + oy + ry), C.FLOOR[0], 46, 0.6)


def straw_bits(room, x0, y0, x1, y1, n, rnd, keep_out=None):
    """Straw and sawdust trodden into the boards, sparse."""
    for _ in range(n):
        sx, sy = rnd.uniform(x0, x1 - 3), rnd.uniform(y0, y1)
        if keep_out and keep_out[0] < sx < keep_out[2] and keep_out[1] < sy < keep_out[3] and rnd.random() < 0.7:
            continue
        a = rnd.uniform(-0.8, 0.8)
        room.line([(sx, sy), (sx + 2.4 * math.cos(a), sy + 2.4 * math.sin(a))], ramp(C.STRAW, rnd.choice((0.45, 0.75))), 0.3)


def cobweb(room, x, y, side):
    """A cobweb in a top corner: threads fanning from the corner, joined by
    sagging rings."""
    col = ramp(C.LINEN, 0.8)
    spokes = [(side * 11, 0.6), (side * 10.4, 4.2), (side * 8, 8), (side * 4.2, 10.4), (side * 0.6, 11)]
    for dx, dy in spokes:
        room.line([(x, y), (x + dx, y + dy)], col, 0.25)
    for f in (0.35, 0.62, 0.9):
        room.line([(x + dx * f, y + dy * f) for dx, dy in spokes], ramp(C.LINEN, 0.65), 0.25)


def lantern(room, cx, y):
    """An unlit lantern hung from a nail: a tin cone top with a ring, panes
    of horn in an iron frame, a candle stub behind them."""
    room.rect(cx - 0.3, y, cx + 0.3, y + 1.0, ramp(C.IRON, 0.6))
    room.ellipse(cx - 1.4, y + 0.6, cx + 1.4, y + 3.0, ramp(C.IRON, 0.6))
    room.ellipse(cx - 0.7, y + 1.2, cx + 0.7, y + 2.4, ramp(C.SOOT, 0.2))
    room.poly([(cx, y + 2.6), (cx + 4.0, y + 7.0), (cx - 4.0, y + 7.0)], ramp(C.IRON, 0.5))
    room.poly([(cx, y + 2.6), (cx - 0.5, y + 7.0), (cx - 4.0, y + 7.0)], ramp(C.IRON, 0.74))
    room.rect(cx - 3.6, y + 7.0, cx + 3.6, y + 16.4, ramp(C.IRON, 0.3))
    room.rect(cx - 2.8, y + 7.8, cx + 2.8, y + 15.4, ramp(C.STRAW, 0.6))
    room.rect(cx - 2.8, y + 7.8, cx - 1.4, y + 15.4, ramp(C.STRAW, 0.78))
    room.rect(cx - 0.3, y + 7.0, cx + 0.3, y + 16.4, ramp(C.IRON, 0.3))
    room.rect(cx - 0.6, y + 11.6, cx + 0.6, y + 15.4, ramp(C.LINEN, 0.5))
    room.rect(cx - 4.0, y + 16.0, cx + 4.0, y + 17.2, ramp(C.IRON, 0.35))
    room.rect(cx - 4.0, y + 16.0, cx + 4.0, y + 16.4, ramp(C.IRON, 0.62))


def tankard(room, cx, by, s=1.0):
    """A pewter tankard standing with its foot at (cx, by): a drum lit on its
    left, a domed lid, the handle on the right."""
    w2, ht = 1.7 * s, 4.2 * s
    room.rect(cx - w2, by - ht, cx + w2, by, ramp(C.PEWTER, 0.45))
    room.rect(cx - w2, by - ht, cx - w2 + 0.8 * s, by, ramp(C.PEWTER, 0.88))
    room.rect(cx + w2 - 0.7 * s, by - ht, cx + w2, by, ramp(C.PEWTER, 0.2))
    room.rect(cx - w2, by - ht * 0.55, cx + w2, by - ht * 0.55 + 0.4 * s, ramp(C.PEWTER, 0.25))
    room.rect(cx - w2 - 0.2, by - 0.7 * s, cx + w2 + 0.2, by, ramp(C.PEWTER, 0.3))
    room.ellipse(cx - w2 - 0.2, by - ht - 1.0 * s, cx + w2 + 0.2, by - ht + 0.8 * s, ramp(C.PEWTER, 0.66))
    room.ellipse(cx - w2 + 0.4, by - ht - 0.8 * s, cx, by - ht + 0.1 * s, ramp(C.PEWTER, 0.95))
    room.line([(cx + w2, by - ht + 0.6 * s), (cx + w2 + 1.4 * s, by - ht + 1.4 * s), (cx + w2 + 1.2 * s, by - 1.4 * s),
               (cx + w2, by - 1.1 * s)], ramp(C.PEWTER, 0.32), 0.45 * s)


def bottle(room, cx, by, s=1.0):
    """A green onion bottle: a squat round body lit on its upper left, a
    short thick neck, a string rim."""
    room.ellipse(cx - 2.0 * s, by - 3.4 * s, cx + 2.0 * s, by, ramp(C.GLASS, 0.14))
    room.ellipse(cx - 1.4 * s, by - 3.0 * s, cx - 0.2 * s, by - 1.6 * s, ramp(C.GLASS, 0.62))
    room.rect(cx - 0.6 * s, by - 5.6 * s, cx + 0.6 * s, by - 3.0 * s, ramp(C.GLASS, 0.24))
    room.rect(cx - 0.6 * s, by - 5.6 * s, cx - 0.1 * s, by - 3.2 * s, ramp(C.GLASS, 0.5))
    room.rect(cx - 0.8 * s, by - 6.0 * s, cx + 0.8 * s, by - 5.3 * s, ramp(C.GLASS, 0.4))


def jug(room, cx, by, s=1.0):
    """A redware jug, its glaze lit on the left, a strap handle."""
    room.ellipse(cx - 1.9 * s, by - 4.6 * s, cx + 1.9 * s, by, ramp(C.REDWARE, 0.5))
    room.rect(cx - 1.0 * s, by - 6.0 * s, cx + 1.0 * s, by - 4.2 * s, ramp(C.REDWARE, 0.42))
    room.ellipse(cx - 1.4 * s, by - 4.2 * s, cx - 0.2 * s, by - 2.0 * s, ramp(C.REDWARE, 0.86))
    room.line([(cx + 1.0 * s, by - 5.4 * s), (cx + 2.6 * s, by - 4.0 * s), (cx + 1.7 * s, by - 1.8 * s)],
              ramp(C.REDWARE, 0.3), 0.45 * s)


def candle(room, cx, by, lit=True, h=4.0):
    """A tallow candle in a pewter stick, its foot at (cx, by), lit: a small
    flame with a pale heart."""
    room.ellipse(cx - 1.8, by - 1.1, cx + 1.8, by + 0.2, ramp(C.PEWTER, 0.55))
    room.ellipse(cx - 1.6, by - 1.0, cx - 0.2, by - 0.3, ramp(C.PEWTER, 0.9))
    room.rect(cx - 0.45, by - 2.6, cx + 0.45, by - 0.6, ramp(C.PEWTER, 0.66))
    room.rect(cx - 1.0, by - 3.0, cx + 1.0, by - 2.4, ramp(C.PEWTER, 0.5))
    room.rect(cx - 0.55, by - 3.0 - h, cx + 0.55, by - 3.0, ramp(C.LINEN, 0.88))
    room.rect(cx + 0.15, by - 3.0 - h, cx + 0.55, by - 3.0, ramp(C.LINEN, 0.6))
    if lit:
        room.ellipse(cx - 0.75, by - 5.6 - h, cx + 0.75, by - 2.8 - h, ramp(C.FIRE, 0.7))
        room.ellipse(cx - 0.35, by - 4.4 - h, cx + 0.35, by - 3.0 - h, ramp(C.FIRE, 0.97))


def casement(room, x0, y0, x1, y1, step=3.6):
    """A leaded casement in a heavy oak frame, its diamond quarrels larger
    than the home's: daylight through it, brighter in the upper panes, a
    mullion and a sill."""
    C.timber(room, x0 - 2.0, y0 - 2.0, x1 + 2.0, y1 + 2.0, 0.42)
    room.rect(x0 - 0.6, y0 - 0.6, x1 + 0.6, y1 + 0.6, ramp(C.TIMBER, 0.08))
    for k in range(int((y1 - y0) * 4)):
        yy = y0 + k / 4
        room.rect(x0, yy, x1, yy + 0.25, ramp(C.DAYLIGHT, 0.92 - 0.75 * (yy - y0) / (y1 - y0)))
    k = x0 - (y1 - y0)
    while k < x1:
        room.line([(max(x0, k), y0 + max(0, x0 - k)), (min(x1, k + (y1 - y0)), y0 + min(y1 - y0, x1 - k))],
                  ramp(C.GLASS, 0.1), 0.35)
        room.line([(max(x0, k), y1 - max(0, x0 - k)), (min(x1, k + (y1 - y0)), y1 - min(y1 - y0, x1 - k))],
                  ramp(C.GLASS, 0.1), 0.35)
        k += step
    C.timber(room, (x0 + x1) / 2 - 0.8, y0, (x0 + x1) / 2 + 0.8, y1, 0.4)
    room.rect(x0 - 2.8, y1 + 2.0, x1 + 2.8, y1 + 3.8, ramp(C.TIMBER, 0.5))
    room.rect(x0 - 2.8, y1 + 2.0, x1 + 2.8, y1 + 2.6, ramp(C.TIMBER, 0.85))
    room.rect(x0 - 2.8, y1 + 3.3, x1 + 2.8, y1 + 3.8, ramp(C.TIMBER, 0.15))


def fire_strip(w, h, frames=4, seed=0):
    """The hearth fire, `frames` frames of w x h room px side by side: logs
    on andirons over a bed of embers, and broad rounded tongues of flame,
    each layered from dark red at its edge to a pale heart, swaying a little
    differently each frame; a compact bright core low in the middle."""
    out = Image.new("RGBA", (w * C.DENSITY * frames, h * C.DENSITY), (0, 0, 0, 0))
    tongues = ((0.27, 0.5), (0.5, 0.82), (0.73, 0.56), (0.39, 0.66), (0.62, 0.62))
    for k in range(frames):
        room = C.Room(w, h)
        rnd = random.Random(seed * 31 + k)
        for ax in (2.5, w - 3.5):
            room.rect(ax, h - 4.5, ax + 1.2, h - 0.5, ramp(C.IRON, 0.35))
            room.rect(ax - 0.4, h - 5.2, ax + 1.6, h - 4.4, ramp(C.IRON, 0.6))
        room.rect(1, h - 2.8, w - 1, h - 0.6, ramp(C.FIRE, 0.22))
        room.speckle(1.5, h - 2.6, w - 1.5, h - 0.8, 26, C.FIRE, (0.45, 0.7, 0.85), rnd, (0.3, 0.7))
        room.line([(2, h - 2.4), (w - 4, h - 4.6)], ramp(C.OAK, 0.2), 2.0)
        room.line([(4, h - 4.6), (w - 2, h - 2.6)], ramp(C.OAK, 0.28), 2.0)
        room.ellipse(w - 4.2, h - 4.2, w - 1.8, h - 1.8, ramp(C.FIRE, 0.55))
        room.ellipse(1.6, h - 3.6, 3.6, h - 1.6, ramp(C.FIRE, 0.5))
        ph = 2 * math.pi * k / frames
        base = h - 3.6
        # All the tongues' outer layers first, then each brighter layer over
        # them, so a neighbour's dark edge never covers a bright heart.
        for scale, tone in ((1.0, 0.3), (0.74, 0.52), (0.5, 0.74), (0.28, 0.92)):
            for i, (fx, fh) in enumerate(tongues):
                sway = 1.1 * math.sin(ph + i * 1.9)
                hh = h * fh * (0.86 + 0.14 * math.sin(ph + i * 2.3)) * scale
                cx = w * fx + sway * 0.3
                half = (3.4 + (0.8 if i == 1 else 0)) * scale + 0.4
                room.ellipse(cx - half, base - hh * 0.62, cx + half, base + 0.8, ramp(C.FIRE, tone))
                room.poly([(cx - half * 0.72, base - hh * 0.4), (cx + half * 0.72, base - hh * 0.4),
                           (cx + sway * 0.8, base - hh)], ramp(C.FIRE, tone))
        room.ellipse(w * 0.36, base - 3.2, w * 0.64, base + 0.6, ramp(C.FIRE, 1.0))
        sx, sy = rnd.uniform(4, w - 5), rnd.uniform(1, h * 0.25)
        room.rect(sx, sy, sx + 0.6, sy + 0.6, ramp(C.FIRE, 0.95))
        out.paste(C.shrink(room, C.FIRE_PALETTE), (k * w * C.DENSITY, 0))
    return out


# ---------------------------------------------------------------- the common room (room sheet)

def stair_passage(room, x0, x1, top, bottom, rnd):
    """The stairs up, seen head-on through the back wall: two hewn posts and
    a dark well between them, steps climbing away and up, each riser and
    tread a little narrower and dimmer than the one below until they go into
    the dark; plank walls either side; a rope handrail on the left."""
    room.rect(x0, top, x1, bottom, ramp(C.SOOT, 0.25))
    for k in range(int((bottom - top) * 2)):
        yy = top + k * 0.5
        f = (yy - top) / (bottom - top)
        room.rect(x0 + 3, yy, x0 + 5.6, yy + 0.5, ramp(C.TIMBER, 0.05 + 0.3 * f))
        room.rect(x1 - 5.6, yy, x1 - 3, yy + 0.5, ramp(C.TIMBER, 0.03 + 0.16 * f))
    step, n = 5.4, 11
    for k in range(n):
        f = k / (n - 1)
        front = bottom - k * step
        inset = 4.2 + 1.6 * f
        a, b = x0 + inset, x1 - inset
        light = max(0.0, 1.0 - f * 1.0)
        riser = (front - 3.4, front)
        tread = (front - step, front - 3.4)
        room.rect(a, riser[0], b, riser[1], ramp(C.OAK, 0.06 + 0.4 * light))
        room.rect(a, riser[1] - 0.5, b, riser[1], ramp(C.OAK, 0.04 + 0.12 * light))
        room.rect(a, tread[0], b, tread[1], ramp(C.OAK, 0.12 + 0.7 * light))
        room.rect(a, tread[1] - 0.6, b, tread[1], ramp(C.OAK, 0.16 + 0.82 * light))
        if light > 0.2:
            # The tread worn hollow in the middle, the boots' path.
            room.rect((a + b) / 2 - 3, tread[0] + 0.5, (a + b) / 2 + 3, tread[1] - 0.6, ramp(C.OAK, 0.14 + 0.72 * light))
            room.rect(b - 0.8, riser[0], b, riser[1], ramp(C.OAK, 0.05 + 0.2 * light))
    fade = Image.new("L", room.lay.size, 0)
    fd = ImageDraw.Draw(fade)
    for i in range(24):
        t = i / 24
        fd.rectangle((x0 * C.S, (top + (bottom - top) * 0.4 * t) * C.S, x1 * C.S, (top + (bottom - top) * 0.4 * (t + 1 / 24)) * C.S),
                     fill=int(220 * (1 - t)))
    room.lay.paste(Image.new("RGBA", room.lay.size, C.B.rgb(C.SOOT[0])), (0, 0), fade)
    pts = [(x0 + 4.6, bottom - 4), (x0 + 5.0, bottom - 22), (x0 + 5.4, bottom - 40)]
    room.line(pts, ramp(C.STRAW, 0.45), 0.5)
    room.line([(x0 + 4.9, bottom - 4), (x0 + 5.3, bottom - 22)], ramp(C.STRAW, 0.75), 0.25)
    for _px, py in pts:
        room.rect(x0 + 3.6, py - 0.4, x0 + 4.6, py + 0.4, ramp(C.IRON, 0.5))
    C.timber(room, x0, top, x0 + 3.4, bottom, 0.34, rnd)
    C.timber(room, x1 - 3.4, top, x1, bottom, 0.3, rnd)


def cask(room, cx, cy, r, rnd):
    """A cask on its side on the stillage, its head to us: its bulging body
    behind the head seen from above, the staves and two iron hoops round it;
    the head ringed by its chime, its boards, a wooden tap, the light on its
    upper left, the keeper's chalked count."""
    bt = cy - r - 5.0
    room.ellipse(cx - r * 1.08, bt, cx + r * 1.08, cy + 1.0, ramp(C.OAK, 0.34))
    for sx in (-0.7, -0.35, 0.0, 0.35, 0.7):
        room.line([(cx + sx * r * 1.02, bt + 0.8), (cx + sx * r * 1.06, cy - r * 0.4)], ramp(C.OAK, 0.2), 0.25)
    room.ellipse(cx - r * 0.95, bt + 0.6, cx - r * 0.2, cy - r * 0.6, ramp(C.OAK, 0.56))
    for hy in (bt + 1.6, cy - r - 1.2):
        room.rect(cx - r * 1.04, hy, cx + r * 1.04, hy + 1.0, ramp(C.IRON, 0.28))
        room.rect(cx - r * 1.04, hy, cx + r * 1.04, hy + 0.35, ramp(C.IRON, 0.6))
    room.ellipse(cx - r - 0.6, cy - r - 0.6, cx + r + 0.6, cy + r + 0.6, ramp(C.IRON, 0.25))
    room.ellipse(cx - r, cy - r, cx + r, cy + r, ramp(C.OAK, 0.3))
    room.ellipse(cx - r + 1.0, cy - r + 1.0, cx + r - 1.0, cy + r - 1.0, ramp(C.OAK, 0.5))
    for bx in (-r * 0.5, 0.0, r * 0.5):
        half = math.sqrt(max(0.0, (r - 1.0) ** 2 - bx * bx))
        room.line([(cx + bx, cy - half), (cx + bx, cy + half)], ramp(C.OAK, 0.28), 0.25)
    room.ellipse(cx - r + 1.4, cy - r + 1.4, cx - 0.6, cy - 0.6, ramp(C.OAK, 0.66))
    room.rect(cx - 0.8, cy + r * 0.45, cx + 0.8, cy + r * 0.45 + 2.6, ramp(C.OAK, 0.72))
    room.rect(cx - 0.4, cy + r * 0.45 + 2.6, cx + 0.4, cy + r * 0.45 + 3.6, ramp(C.OAK, 0.3))
    room.rect(cx - 1.6, cy + r * 0.45 - 0.2, cx + 1.6, cy + r * 0.45 + 0.6, ramp(C.OAK, 0.85))
    for k in range(rnd.randint(2, 4)):
        sx = cx + r * 0.2 + k * 0.9
        room.line([(sx, cy - r * 0.6), (sx, cy - r * 0.2)], ramp(C.LINEN, 0.85), 0.22)


def wall_shelf(room, x0, x1, y, wares, s=1.0):
    """A plank shelf on two brackets and what stands on it."""
    room.rect(x0, y, x1, y + 1.4, ramp(C.OAK, 0.6))
    room.rect(x0, y, x1, y + 0.45, ramp(C.OAK, 0.88))
    room.rect(x0, y + 1.4, x1, y + 1.9, ramp(C.TIMBER, 0.05))
    for bx in (x0 + 2, x1 - 3):
        room.poly([(bx, y + 1.4), (bx + 1.2, y + 1.4), (bx + 1.2, y + 4.6)], ramp(C.OAK, 0.42))
    step = (x1 - x0) / len(wares)
    for i, ware in enumerate(wares):
        cx = x0 + (i + 0.5) * step
        {"bottle": bottle, "tankard": tankard, "jug": jug}[ware](room, cx, y + 0.2, s)


def rubble_breast(room, x0, x1, top, wall_bottom, fire_box, rnd):
    """The chimney breast laid up again in rough fieldstone over the kit
    hearth's coursed stones: rounded stones of uneven size, set in thick
    mortar, the courses broken; each stone lit on its upper left and shaded
    on its lower right; soot up the breast. The kit hearth's mouth, lintel,
    trammel and apron are kept (only the breast round them is repainted),
    and its fire irons drawn again."""
    fx, fy, fw, fh = fire_box
    saved_lay, saved_d = room.lay, room.d
    room.lay = Image.new("RGBA", saved_lay.size, (0, 0, 0, 0))
    room.d = ImageDraw.Draw(room.lay)
    room.rect(x0, top, x1, wall_bottom, ramp(C.MORTAR, 0.3))
    y = wall_bottom
    while y > top + 0.4:
        rh = rnd.uniform(2.8, 5.6)
        x = x0 - rnd.uniform(0, 4)
        while x < x1:
            sw = rnd.uniform(3.2, 8.8)
            sh = rh * rnd.uniform(0.78, 1.08)
            oy = rnd.uniform(-0.8, 0.8)
            a, b = x + 0.4, x + sw - 0.4
            t0, t1 = y - sh + oy, y - 0.4 + oy * 0.3
            tone = 0.5 + rnd.uniform(-0.2, 0.16)
            r = int(min(sw, sh) * rnd.uniform(0.3, 0.5) * C.S)
            room.d.rounded_rectangle((a * C.S, t0 * C.S, b * C.S, t1 * C.S), radius=r, fill=ramp(C.STONE, tone - 0.22))
            room.d.rounded_rectangle((a * C.S, t0 * C.S, (b - 0.6) * C.S, (t1 - 0.6) * C.S), radius=r, fill=ramp(C.STONE, tone))
            room.d.rounded_rectangle(((a + 0.3) * C.S, (t0 + 0.3) * C.S, (a + (b - a) * 0.6) * C.S, (t0 + 1.0) * C.S),
                                     radius=int(0.4 * C.S), fill=ramp(C.STONE, tone + 0.24))
            room.speckle(a + 0.6, t0 + 1.0, b - 0.8, t1 - 0.8, (b - a) * sh / 3, C.STONE, (tone - 0.12, tone + 0.1), rnd, (0.25, 0.45))
            x += sw
        y -= rh
    stones = room.lay
    room.lay, room.d = saved_lay, saved_d
    mask = Image.new("L", room.lay.size, 0)
    md = ImageDraw.Draw(mask)
    lintel_top = fy - 3.4
    for bx0, by0, bx1, by1 in ((x0, top, x1, lintel_top), (x0, lintel_top, fx - 2.5, wall_bottom),
                               (fx + fw + 2.5, lintel_top, x1, wall_bottom)):
        md.rectangle((bx0 * C.S, by0 * C.S, bx1 * C.S - 1, by1 * C.S - 1), fill=255)
    room.lay.paste(stones, (0, 0), mask)
    room.d = ImageDraw.Draw(room.lay)
    wash(room, "ellipse", (fx - 3, top - 6, fx + fw + 3, fy + 1), C.SOOT[1], 120, 3)
    ax1 = fx + fw + 5
    room.line([(ax1 - 1.0, wall_bottom - 10), (ax1 + 0.4, wall_bottom + 1.0)], ramp(C.IRON, 0.45), 0.4)
    room.line([(ax1 + 0.6, wall_bottom - 9), (ax1 + 1.4, wall_bottom + 1.0)], ramp(C.IRON, 0.35), 0.4)
    room.ellipse(ax1 - 2.0, wall_bottom - 10.8, ax1 + 0.2, wall_bottom - 9.4, ramp(C.IRON, 0.6))


def fowling_piece(room, x0, x1, y):
    """A long fowling piece on two pegs over the hearth: the stock at the
    left, the lock, the long barrel catching the firelight."""
    for px in (x0 + 9, x1 - 7):
        room.rect(px - 0.6, y - 0.4, px + 0.6, y + 2.2, ramp(C.OAK, 0.78))
    room.poly([(x0, y + 0.6), (x0 + 2.2, y - 0.6), (x0 + 11, y - 0.4), (x0 + 11, y + 1.2), (x0 + 3.0, y + 2.6), (x0, y + 2.8)],
              ramp(C.OAK, 0.6))
    room.line([(x0 + 0.6, y + 0.4), (x0 + 10.6, y - 0.2)], ramp(C.OAK, 0.85), 0.35)
    room.rect(x0 + 9, y - 0.8, x0 + 12.4, y + 1.0, ramp(C.IRON, 0.55))
    room.rect(x0 + 11, y - 0.2, x1, y + 0.8, ramp(C.IRON, 0.5))
    room.rect(x0 + 11, y - 0.2, x1, y + 0.2, ramp(C.PEWTER, 0.8))
    room.rect(x0 + 11, y + 0.8, x1 - 5, y + 1.5, ramp(C.OAK, 0.56))


def common_room(seed=1692):
    """The common room, 128 x 112: the back wall (to y 64) with the stairs up
    at the left, the hearth, a small window, and behind the bar the casks and
    a shelf of bottles; the floor below, the door at the bottom middle."""
    w, h, wall = 128, 112, 64
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd, board=6.0)
    grime(room, 0, wall, w, h, 64)
    C.wear(room, 74, 92, 9, 16, 30)
    C.wear(room, 64, 104, 12, 8, 26)
    # Firelight on the boards before the hearth.
    wash(room, "ellipse", (30, wall - 2, 74, wall + 22), C.FIRE[4], 46, 5)
    stains(room, ((72, 98, 3.5, 1.8), (40, 106, 3.0, 1.5), (10, 86, 2.5, 1.3), (76, 80, 2.2, 1.2), (100, 110, 3.0, 1.2)), rnd)
    straw_bits(room, 0, wall + 3, w, h - 1, 40, rnd)
    room.speckle(0, wall + 2, w, h, 120, C.STRAW, (0.55, 0.75), rnd, (0.25, 0.4))
    log_wall(room, 0, w, 4, wall - 2, rnd)
    C.timber(room, 0, 0, w, 4.6, 0.3, rnd)
    stair_passage(room, 0, STAIR_W, 0, wall + 1, rnd)
    fire_box = (FIRE["x"], FIRE["y"]) + FIRE_FRAME
    C.hearth(room, 30, 72, 0, wall, fire_box, rnd)
    rubble_breast(room, 30, 72, 0, wall, fire_box, rnd)
    fowling_piece(room, 33, 69, 31)
    for cx in (41, 63):
        candle(room, cx, 42.8, lit=True, h=4.4)
    casement(room, 78, 17, 92, 33)
    C.light_pool(room, 78, wall + 1, 92, wall + 18, 50)
    # Behind the bar: a shelf of bottles, tankards and a jug, and two casks
    # on their stillage (the counter hides the stillage's feet).
    wall_shelf(room, 97, 127, 21, ("bottle", "tankard", "bottle", "jug", "bottle"), 1.3)
    room.rect(97, 47.5, 127, 50, ramp(C.OAK, 0.38))
    room.rect(97, 47.5, 127, 48.2, ramp(C.OAK, 0.62))
    cask(room, 104.5, 41, 6.2, rnd)
    cask(room, 119.5, 41, 6.2, rnd)
    C.wall_foot(room, STAIR_W, w, wall)
    # The doorstep: a worn stone slab where the door opens below.
    room.rect(54, 105, 74, h, ramp(C.STONE, 0.15))
    room.rect(54.5, 105.5, 73.5, h, ramp(C.STONE, 0.58))
    room.rect(54.5, 105.5, 73.5, 106.4, ramp(C.STONE, 0.8))
    room.speckle(55, 106.5, 73, h - 0.5, 60, C.STONE, (0.45, 0.7), rnd)
    # The furniture's shadows on the floor, down and right; the well inside
    # the bar, where the keeper stands, in the counters' shade.
    for x0, y0, x1, y1 in ((21, 99, 54, 105), (9, 99, 21, 103), (53, 99, 64, 103), (83, 105, 129, 111)):
        room.shadow(x0, y0, x1, y1, 220)
    room.sd.rectangle((96 * C.S, 71 * C.S, w * C.S, 82 * C.S), fill=150)
    room.shadow(96, 70, 129, 76, 230)
    return C.shrink(room, ROOM_PALETTE)


# ---------------------------------------------------------------- the common room (pieces)

def counter_top(room, x0, y0, x1, y1, rnd, along="x"):
    """A counter's top: thick oak planks along the counter, each its own
    shade, dark joints, grain."""
    room.rect(x0, y0, x1, y1, ramp(C.OAK, 0.5))
    long, short = (x1 - x0, y1 - y0) if along == "x" else (y1 - y0, x1 - x0)
    n = max(1, int(round(short / 4.6)))
    for k in range(n):
        t = 0.5 + (-0.05, 0.05, -0.02)[k % 3] + rnd.uniform(-0.03, 0.03)
        if along == "x":
            py = y0 + k * short / n
            room.rect(x0, py, x1, py + short / n, ramp(C.OAK, t))
            room.rect(x0, py, x1, py + 0.4, ramp(C.OAK, 0.16))
            for _ in range(int(long / 5)):
                gx, gy = rnd.uniform(x0, x1 - 5), rnd.uniform(py + 0.6, py + short / n - 0.4)
                room.rect(gx, gy, min(x1, gx + rnd.uniform(3, 9)), gy + 0.25, ramp(C.OAK, t + rnd.choice((-0.1, 0.12))))
        else:
            px = x0 + k * short / n
            room.rect(px, y0, px + short / n, y1, ramp(C.OAK, t))
            room.rect(px, y0, px + 0.4, y1, ramp(C.OAK, 0.16))
            for _ in range(int(long / 5)):
                gy, gx = rnd.uniform(y0, y1 - 5), rnd.uniform(px + 0.6, px + short / n - 0.4)
                room.rect(gx, gy, gx + 0.25, min(y1, gy + rnd.uniform(3, 9)), ramp(C.OAK, t + rnd.choice((-0.1, 0.12))))


def scores(room, x0, y0, x1, y1, n, rnd):
    """Knife cuts and scratches in a worn top."""
    for _ in range(n):
        sx, sy = rnd.uniform(x0 + 1, x1 - 4), rnd.uniform(y0 + 1, y1 - 1)
        room.line([(sx, sy), (sx + rnd.uniform(1.5, 3.5), sy + rnd.uniform(-0.6, 0.6))], ramp(C.OAK, 0.26), 0.25)


def ring_stain(room, cx, cy, r=1.6):
    room.ellipse(cx - r, cy - r * 0.6, cx + r, cy + r * 0.6, ramp(C.OAK, 0.38))
    room.ellipse(cx - r + 0.35, cy - r * 0.6 + 0.3, cx + r - 0.35, cy + r * 0.6 - 0.3, ramp(C.OAK, 0.52))


def board_face(room, x0, y0, x1, y1, rnd, board=4.6, tone=0.36):
    """A front of upright boards: each its own shade, lit on its left edge,
    the gaps dark, nail heads at top and bottom."""
    room.rect(x0, y0, x1, y1, ramp(C.OAK, 0.06))
    x = x0
    while x < x1 - 0.3:
        bw = min(board + rnd.uniform(-0.6, 0.6), x1 - x)
        t = tone + rnd.uniform(-0.06, 0.06)
        room.rect(x + 0.3, y0, x + bw - 0.3, y1, ramp(C.OAK, t))
        room.rect(x + 0.3, y0, x + 0.9, y1, ramp(C.OAK, t + 0.2))
        for _ in range(2):
            gx = rnd.uniform(x + 1.2, x + bw - 1)
            room.rect(gx, y0 + rnd.uniform(0, 3), gx + 0.25, y1 - rnd.uniform(0, 3), ramp(C.OAK, t - 0.1))
        for ny in (y0 + 1.2, y1 - 1.6):
            room.rect(x + bw / 2 - 0.3, ny, x + bw / 2 + 0.3, ny + 0.6, ramp(C.IRON, 0.25))
        x += bw


def c_bar(room, x, y, w, h, rnd):
    """The C-shaped bar over its two arms' rects (painted as one counter; the
    client cuts it in two at y + 30, where the back arm's sort line puts it
    behind the keeper and the front arm's in front). It opens toward the
    right wall. The back arm is a narrow counter with shelves under it, its
    face to the keeper (a keg, bottles, tankards); below it the floor of the
    well where the keeper stands shows between the arms. The end piece on
    the left joins the back arm to the front arm, whose face of upright
    boards on a kick rail is to the room. A sale is set on the front arm's
    left end (the entry's bar point), which is kept clear."""
    L, T, R = x, y, x + w - 1          # the counter's right end runs into the wall
    inner = L + 14                     # the end piece's inner edge
    # The back arm's face under its counter, to the keeper: two shelves.
    face0, face1 = T + 11, T + 22
    room.rect(inner, face0, R, face1, ramp(C.OAK, 0.06))
    for sy in (T + 16.4, T + 21.2):
        room.rect(inner, sy, R, sy + 0.9, ramp(C.OAK, 0.5))
        room.rect(inner, sy, R, sy + 0.4, ramp(C.OAK, 0.75))
    for px in (inner, L + 30, R - 1.6):
        room.rect(px, face0, px + 1.6, face1, ramp(C.OAK, 0.42))
        room.rect(px, face0, px + 0.5, face1, ramp(C.OAK, 0.66))
    room.ellipse(inner + 2.4, face0 + 0.6, inner + 7.0, face0 + 5.2, ramp(C.OAK, 0.34))
    room.ellipse(inner + 3.1, face0 + 1.3, inner + 6.3, face0 + 4.5, ramp(C.OAK, 0.56))
    room.rect(inner + 2.4, face0 + 2.6, inner + 7.0, face0 + 3.2, ramp(C.IRON, 0.3))
    for bx in (inner + 10, inner + 13):
        bottle(room, bx, T + 16.4, 0.8)
    for tx in (L + 34, L + 38.5, L + 43):
        tankard(room, tx, T + 16.4, 0.75)
    for tx in (inner + 3, inner + 7, inner + 11):
        tankard(room, tx, T + 21.2, 0.7)
    for bx in (L + 35, L + 38.5, L + 42):
        bottle(room, bx, T + 21.2, 0.75)
    # The front arm's face to the room, and its kick rail.
    board_face(room, L, T + 46.6, R, T + 56, rnd)
    room.rect(L, T + 56, R, T + 58.6, ramp(C.OAK, 0.2))
    room.rect(L, T + 56, R, T + 56.5, ramp(C.OAK, 0.42))
    # The counter: back arm and front arm along their length, the end piece
    # down the left joining them; a lit far edge, a lit front lip over a
    # shadow line.
    counter_top(room, L, T + 1, R, T + 11, rnd, "x")
    counter_top(room, L, T + 32, R, T + 46, rnd, "x")
    counter_top(room, L, T + 1, inner, T + 46, rnd, "y")
    room.rect(L, T + 1, R, T + 1.7, ramp(C.OAK, 0.96))
    room.rect(L, T + 1, L + 0.8, T + 46, ramp(C.OAK, 0.9))
    room.rect(inner, T + 32, R, T + 32.7, ramp(C.OAK, 0.9))
    for a, b, ly in ((inner, R, T + 10.2), (L, R, T + 45.4)):
        room.rect(a, ly, b, ly + 0.9, ramp(C.OAK, 0.78))
        room.rect(a, ly + 0.9, b, ly + 1.4, ramp(C.OAK, 0.18))
    # The end piece's inner edge over the well.
    room.rect(inner - 0.8, T + 11, inner, T + 32, ramp(C.OAK, 0.3))
    for cx, cy in ((L + 22, T + 6), (L + 36, T + 8), (L + 6, T + 18), (L + 26, T + 38), (L + 42, T + 36)):
        ring_stain(room, cx, cy)
    scores(room, L, T + 1, R, T + 10, 8, rnd)
    scores(room, L + 16, T + 33, R, T + 45, 8, rnd)
    # On the counter: a lit candle at the back arm's far end, a flagon;
    # tankards along the front arm away from the sale's place.
    candle(room, L + 42, T + 9.4, lit=True, h=2.8)
    jug(room, L + 26, T + 8.6, 0.9)
    tankard(room, L + 32, T + 41, 1.0)
    tankard(room, L + 39, T + 43, 1.0)


def round_table(room, x, y, w, h, rnd):
    """A round table on four splayed legs, seen from above at a slant: an
    oval top of three planks, its lit far edge and dark front edge, the legs
    and a stretcher under it; on it two tankards, a jug, a clay pipe."""
    top_h = h * 0.58
    for lx, ly in ((x + 5, y + h - 1), (x + w - 7, y + h - 1), (x + 9, y + h - 3.6), (x + w - 11, y + h - 3.6)):
        room.rect(lx, y + top_h - 1, lx + 2.2, ly, ramp(C.OAK, 0.32))
        room.rect(lx, y + top_h - 1, lx + 0.7, ly, ramp(C.OAK, 0.56))
    room.rect(x + 6, y + h - 7.4, x + w - 6, y + h - 6, ramp(C.OAK, 0.3))
    room.rect(x + 6, y + h - 7.4, x + w - 6, y + h - 7.0, ramp(C.OAK, 0.5))
    room.ellipse(x, y + 2.0, x + w, y + top_h + 2.6, ramp(C.OAK, 0.2))
    room.ellipse(x, y + 1.6, x + w, y + top_h + 1.6, ramp(C.OAK, 0.42))
    # The planks, painted on a layer of their own and laid through the oval.
    saved_lay, saved_d = room.lay, room.d
    room.lay = Image.new("RGBA", saved_lay.size, (0, 0, 0, 0))
    room.d = ImageDraw.Draw(room.lay)
    for k in range(3):
        py0 = y + 0.6 + k * top_h / 3
        t = 0.62 + (0.06, -0.04, 0.0)[k]
        room.rect(x, py0, x + w, py0 + top_h / 3, ramp(C.OAK, t))
        room.rect(x, py0, x + w, py0 + 0.35, ramp(C.OAK, 0.3))
        for _ in range(8):
            gx, gy = rnd.uniform(x, x + w - 6), rnd.uniform(py0 + 0.6, py0 + top_h / 3 - 0.3)
            room.rect(gx, gy, gx + rnd.uniform(3, 9), gy + 0.25, ramp(C.OAK, t + rnd.choice((-0.1, 0.12))))
    room.rect(x, y + 0.6, x + w, y + 1.4, ramp(C.OAK, 0.9))
    planks = room.lay
    room.lay, room.d = saved_lay, saved_d
    mask = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(mask).ellipse((x * C.S, (y + 0.6) * C.S, (x + w) * C.S - 1, (y + top_h + 0.6) * C.S - 1), fill=255)
    room.lay.paste(planks, (0, 0), mask)
    room.d = ImageDraw.Draw(room.lay)
    ring_stain(room, x + 22, y + 13)
    tankard(room, x + 9, y + 11.4, 1.0)
    tankard(room, x + 24, y + 9.0, 1.0)
    jug(room, x + 16.5, y + 8.0, 0.9)
    room.line([(x + 8, y + 14.6), (x + 17, y + 13.2)], ramp(C.LINEN, 0.9), 0.35)
    room.ellipse(x + 16.6, y + 12.2, x + 18.4, y + 14.0, ramp(C.LINEN, 0.75))
    room.rect(x + 17.0, y + 12.4, x + 18.0, y + 12.9, ramp(C.SOOT, 0.3))


def _chair(back_left):
    # The kit's chair, its finial 4 px below the rect's top as the pack's was.
    return lambda room, x, y, w, h, rnd: C.ladder_chair(room, x, y + 4, w, h - 4, back_left=back_left)


# ---------------------------------------------------------------- the hall

def plank_door(room, x, y, w, h, rnd):
    """A plank door in Spanish brown weathered toward the wood, rubbed to the
    oak at its edges and low down: upright boards, the nails of the battens
    behind them in two rows, long iron strap hinges on the left, a thumb
    latch on the right."""

    room.rect(x, y, x + w, y + h, ramp(C.TIMBER, 0.04))
    n = 5
    for k in range(n):
        a = x + k * w / n
        t = 0.42 + rnd.uniform(-0.06, 0.06)
        room.rect(a + 0.25, y, a + w / n - 0.25, y + h, ramp(DOOR, t))
        room.rect(a + 0.25, y, a + 0.9, y + h, ramp(DOOR, t + 0.12))
        for _ in range(3):
            gx = rnd.uniform(a + 1.2, a + w / n - 1)
            room.rect(gx, y + rnd.uniform(0, 6), gx + 0.25, y + h - rnd.uniform(0, 6), ramp(DOOR, t - 0.12))
        for _ in range(4):
            px, py = rnd.uniform(a + 0.3, a + w / n - 1.6), rnd.uniform(y + h * 0.55, y + h - 1)
            room.rect(px, py, px + rnd.uniform(0.6, 1.6), py + rnd.uniform(0.4, 1.4), ramp(C.OAK, rnd.uniform(0.35, 0.5)))
    for by in (y + 4.5, y + h - 6.5):
        for k in range(n):
            nx = x + (k + 0.5) * w / n
            room.rect(nx - 0.3, by, nx + 0.3, by + 0.6, ramp(C.IRON, 0.3))
        room.rect(x + 0.3, by - 0.8, x + w * 0.6, by + 0.6, ramp(C.IRON, 0.28))
        room.rect(x + 0.3, by - 0.8, x + w * 0.6, by - 0.4, ramp(C.IRON, 0.55))
        room.ellipse(x + w * 0.6 - 1.6, by - 1.2, x + w * 0.6 + 0.6, by + 1.0, ramp(C.IRON, 0.32))
    lx, ly = x + w - 4.5, y + h * 0.55
    room.rect(lx - 0.8, ly - 2.6, lx + 0.8, ly + 2.6, ramp(C.IRON, 0.35))
    room.rect(lx - 2.4, ly - 0.4, lx + 1.6, ly + 0.4, ramp(C.IRON, 0.6))
    room.rect(lx - 0.4, ly - 4.0, lx + 0.4, ly - 2.4, ramp(C.IRON, 0.55))
    room.rect(x, y + h - 1.0, x + w, y + h, ramp(C.TIMBER, 0.02))


def plaque(room, x, y, numeral):
    """A board nailed over a door, its number chalked on in Roman strokes."""
    room.rect(x, y, x + 12, y + 9.6, ramp(C.TIMBER, 0.1))
    room.rect(x + 0.5, y + 0.5, x + 11.5, y + 9.1, ramp(C.OAK, 0.26))
    room.rect(x + 0.5, y + 0.5, x + 11.5, y + 1.0, ramp(C.OAK, 0.46))
    for nx in (x + 1.2, x + 10.2):
        room.rect(nx, y + 1.4, nx + 0.6, y + 2.0, ramp(C.IRON, 0.5))
    chalk = ramp(C.LINEN, 0.98)
    cx, t0, t1 = x + 6, y + 2.2, y + 8.0
    if numeral == "IV":
        strokes = [[(cx - 3.0, t0), (cx - 3.0, t1)], [(cx - 1.2, t0), (cx + 0.7, t1), (cx + 2.6, t0)]]
    else:
        k = len(numeral)
        strokes = [[(cx + (i - (k - 1) / 2) * 2.2, t0), (cx + (i - (k - 1) / 2) * 2.2 + 0.15, t1)] for i in range(k)]
    for s in strokes:
        room.line(s, chalk, 0.55)


def onion_string(room, cx, y, rnd):
    """A string of onions hung from a nail."""
    room.line([(cx, y), (cx, y + 3)], ramp(C.STRAW, 0.5), 0.35)
    for k in range(6):
        ox = cx + (k % 2 - 0.5) * 2.6 + rnd.uniform(-0.3, 0.3)
        oy = y + 3.4 + k * 2.1
        room.ellipse(ox - 1.8, oy - 1.4, ox + 1.8, oy + 1.6, ramp(C.STRAW, 0.6))
        room.ellipse(ox - 1.2, oy - 1.0, ox, oy + 0.2, ramp(C.STRAW, 0.9))
        room.line([(ox, oy - 1.4), (ox + 0.2, oy - 2.1)], ramp(C.STRAW, 0.35), 0.25)


def stairs_down(room, top, bottom, rnd):
    """The stairs down from the gap between the banisters toward us: a tread
    every 7 px, each wider and darker than the last, its nosing lit and its
    riser dark, between two stringers."""
    a0, b0 = HALL_GAP
    k = 0
    y = top
    while y < bottom:
        grow = k * 2.0
        a, b = a0 - grow, b0 + grow
        dim = min(0.5, k / 11)
        room.rect(a, y, b, y + 7, ramp(C.OAK, 0.06))
        room.rect(a + 1.4, y, b - 1.4, y + 2.4, ramp(C.OAK, 0.68 - dim))
        room.rect(a + 1.4, y, b - 1.4, y + 0.6, ramp(C.OAK, 0.88 - dim))
        room.rect(a + 1.4, y + 2.4, b - 1.4, y + 6.4, ramp(C.OAK, 0.34 - dim * 0.5))
        room.rect((a + b) / 2 - 5, y + 0.6, (a + b) / 2 + 5, y + 2.2, ramp(C.OAK, 0.78 - dim))
        for gx in (a + rnd.uniform(4, 10), b - rnd.uniform(6, 12)):
            room.rect(gx, y + 0.8, gx + rnd.uniform(3, 6), y + 1.05, ramp(C.OAK, 0.5 - dim))
        room.rect(a, y, a + 1.4, y + 7, ramp(C.TIMBER, 0.3 - dim * 0.4))
        room.rect(b - 1.4, y, b, y + 7, ramp(C.TIMBER, 0.2 - dim * 0.3))
        k += 1
        y += 7


def hall_room(seed=807):
    """The upstairs hall, 256 x 128: five plank doors in the log wall (to y
    48), the walkway, its front edge at y 80, the dark stairwell below with
    the stairs coming down out of the gap between the banisters."""
    w, h, wall = 256, 128, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    for k in range(int((h - HALL_EDGE) * 4)):
        yy = HALL_EDGE + k / 4
        room.rect(0, yy, w, yy + 0.25, ramp(C.SOOT, 0.7 - 0.6 * min(1.0, (yy - HALL_EDGE) / 44)))
    C.floorboards(room, 0, wall, w, HALL_EDGE, rnd, board=6.0)
    grime(room, 0, wall, w, HALL_EDGE, 56)
    C.wear(room, 128, 62, 110, 5, 28)
    stains(room, ((40, 70, 3, 1.4), (182, 74, 2.6, 1.2), (226, 56, 2.2, 1.0)), rnd)
    straw_bits(room, 0, wall + 3, w, HALL_EDGE - 2, 30, rnd)
    log_wall(room, 0, w, 3.6, wall - 2, rnd)
    C.timber(room, 0, 0, w, 3.8, 0.3, rnd)
    for i, numeral in enumerate(HALL_PLATES):
        x = 16 + 48 * i
        C.timber(room, x - 2, 14, x + 34, 16, 0.36, rnd)
        C.timber(room, x - 2, 14, x, wall, 0.36, rnd)
        C.timber(room, x + 32, 14, x + 34, wall, 0.3, rnd)
        plank_door(room, x, 16, 32, 32, rnd)
        if numeral:
            plaque(room, x + 10, 3.8, numeral)
    # Between the doors: herbs drying, a lantern, a string of onions, a cloak.
    C.herb_bundles(room, 51, 61, 17, rnd, 2)
    lantern(room, 104, 17)
    onion_string(room, 152, 17, rnd)
    C.peg_rail(room, 195, 205, 19, rnd, ("cloak",))
    cobweb(room, 0, 3.8, 1)
    cobweb(room, w, 3.8, -1)
    C.wall_foot(room, 0, w, wall)
    # The walkway's front edge, the boards' ends and the beam under them,
    # broken at the head of the stairs, where the top tread is the floor's
    # edge.
    for a, b in ((0, HALL_GAP[0]), (HALL_GAP[1], w)):
        room.rect(a, HALL_EDGE - 0.6, b, HALL_EDGE, ramp(C.OAK, 0.82))
        C.timber(room, a, HALL_EDGE, b, HALL_EDGE + 4.6, 0.3, rnd)
    stairs_down(room, HALL_EDGE, h, rnd)
    for a, b in ((0, HALL_GAP[0]), (HALL_GAP[1], w)):
        room.shadow(a, HALL_EDGE - 3.4, b, HALL_EDGE, 120)
    return C.shrink(room, ROOM_PALETTE)


def banister(room, x, y, w, h, rnd, newel_left=True, newel_right=True):
    """A banister along the walkway's edge, in its rect: a handrail lit along
    its top, square balusters to a bottom rail, square newel posts with a
    ball cap at its ends ("stair": the stouter newel at the head of the
    stairs)."""
    top, bottom, x1 = y + 3.0, y + h, x + w
    rail_b = top + 3.0
    room.rect(x, bottom - 2.2, x1, bottom, ramp(C.OAK, 0.3))
    room.rect(x, bottom - 2.2, x1, bottom - 1.6, ramp(C.OAK, 0.55))
    bx = x + 2.6
    while bx < x1 - 4.2:
        room.rect(bx, rail_b, bx + 1.6, bottom - 2.2, ramp(C.OAK, 0.3))
        room.rect(bx, rail_b, bx + 0.6, bottom - 2.2, ramp(C.OAK, 0.56))
        room.rect(bx + 1.1, rail_b, bx + 1.6, bottom - 2.2, ramp(C.OAK, 0.16))
        bx += 4.2 + rnd.uniform(-0.15, 0.15)
    room.rect(x, top + 0.6, x1, rail_b, ramp(C.OAK, 0.5))
    room.rect(x, top + 0.6, x1, top + 1.4, ramp(C.OAK, 0.86))
    room.rect(x, rail_b - 0.5, x1, rail_b, ramp(C.OAK, 0.2))
    for at_left, on in ((True, newel_left), (False, newel_right)):
        if not on:
            continue
        pw = 4.0 if on == "stair" else 3.2
        px = x if at_left else x1 - pw
        room.rect(px, top, px + pw, bottom, ramp(C.OAK, 0.44))
        room.rect(px, top, px + 1.0, bottom, ramp(C.OAK, 0.74))
        room.rect(px + pw - 0.8, top, px + pw, bottom, ramp(C.OAK, 0.2))
        room.rect(px - (0 if at_left else 0.3), top - 0.2, px + pw + (0.3 if at_left else 0), top + 0.8, ramp(C.OAK, 0.6))
        room.ellipse(px, y, px + pw, top + 0.4, ramp(C.OAK, 0.66))
        room.ellipse(px + pw / 2 - 1.1, y + 0.4, px + pw / 2, y + 1.6, ramp(C.OAK, 0.95))


def hall_door_open(seed=809):
    """A vacant room's door standing open, 32 x 32 over the closed door: the
    leaf swung in against the left of the doorway, its battens and hinge
    straps toward us; beyond it the room, dim, its log wall and boards, the
    hall's light lying in a wedge on the floor."""
    w, h = 32, 32
    rnd = random.Random(seed)
    room = C.Room(w, h)
    # The room's far wall, logs in the dark, and its floor running away.
    room.rect(0, 0, w, 19, ramp(LOG, 0.2))
    for k in range(4):
        room.rect(0, 4.4 + k * 4.6, w, 5.2 + k * 4.6, ramp(LOG, 0.04))
        room.rect(0, 5.2 + k * 4.6, w, 5.8 + k * 4.6, ramp(LOG, 0.3))
    room.rect(0, 18.6, w, 20, ramp(C.TIMBER, 0.1))
    for k in range(7):
        y0 = 20 + k * 1.75
        room.rect(0, y0, w, y0 + 1.75, ramp(C.FLOOR, 0.2 + 0.03 * k + rnd.uniform(-0.02, 0.02)))
        room.rect(0, y0, w, y0 + 0.25, ramp(C.FLOOR, 0.08))
    glow = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(glow).polygon([(12 * C.S, 20 * C.S), (26 * C.S, 20 * C.S), (31 * C.S, 32 * C.S), (8 * C.S, 32 * C.S)], fill=130)
    room.lay.paste(Image.new("RGBA", room.lay.size, C.B.rgb(C.FLOOR[5])), (0, 0), glow.filter(ImageFilter.GaussianBlur(1.6 * C.S)))
    # A glimpse of the bed's foot and the blanket in the dark.
    room.rect(16, 13.6, 27, 19.4, ramp(DINGY, 0.24))
    room.rect(16, 17.2, 27, 19.4, ramp(C.OAK, 0.22))
    room.rect(16, 17.2, 27, 17.7, ramp(C.OAK, 0.4))
    # The leaf, swung in against the left: its near edge tall, its far edge
    # shorter; the battens and straps across it.
    far = 9.0
    room.poly([(0, 0), (far, 3.4), (far, 28.4), (0, 32)], ramp(C.OAK, 0.34))
    for k in range(1, 4):
        fx = far * k / 4
        room.line([(fx, 3.4 * k / 4 + 0.2), (fx, 32 - 3.6 * k / 4 - 0.2)], ramp(C.OAK, 0.22), 0.25)
    for by in (5.0, 23.6):
        room.poly([(0, by - 0.4), (far, by + 1.4), (far, by + 3.2), (0, by + 1.8)], ramp(C.OAK, 0.5))
        room.poly([(0, by - 0.4), (far, by + 1.4), (far, by + 1.8), (0, by + 0.1)], ramp(C.OAK, 0.7))
    room.poly([(far - 1.0, 3.0), (far, 3.4), (far, 28.4), (far - 1.0, 28.8)], ramp(DOOR, 0.42))
    room.rect(w - 1.4, 0, w, h, ramp(C.TIMBER, 0.24))
    room.rect(0, 31.0, w, h, ramp(C.OAK, 0.5))
    room.rect(0, 31.0, w, 31.4, ramp(C.OAK, 0.8))
    return C.shrink(room, ROOM_PALETTE)


# ---------------------------------------------------------------- the guest room

def shutters(room, x0, y0, x1, y1, rnd):
    """A window shut with two board shutters on a bar: a frame, the boards
    with Z braces, a thin line of daylight between the leaves."""
    C.timber(room, x0 - 1.6, y0 - 1.6, x1 + 1.6, y1 + 1.6, 0.4)
    mid = (x0 + x1) / 2
    for a, b in ((x0, mid - 0.2), (mid + 0.2, x1)):
        bw = (b - a) / 3
        for k in range(3):
            t = 0.36 + rnd.uniform(-0.05, 0.05)
            room.rect(a + k * bw + 0.2, y0, a + (k + 1) * bw - 0.2, y1, ramp(C.OAK, t))
            room.rect(a + k * bw + 0.2, y0, a + k * bw + 0.7, y1, ramp(C.OAK, t + 0.14))
        for by in (y0 + 2, y1 - 3.4):
            room.rect(a, by, b, by + 1.4, ramp(C.OAK, 0.48))
            room.rect(a, by, b, by + 0.4, ramp(C.OAK, 0.66))
        room.line([(a + 0.6, y1 - 2.2), (b - 0.6, y0 + 3.2)], ramp(C.OAK, 0.46), 0.9)
    room.rect(mid - 0.2, y0 + 0.6, mid + 0.2, y1 - 0.6, ramp(C.DAYLIGHT, 0.7))
    room.rect(x0 - 1, (y0 + y1) / 2 - 0.6, x1 + 1, (y0 + y1) / 2 + 0.6, ramp(C.IRON, 0.35))
    room.rect(x0 - 1, (y0 + y1) / 2 - 0.6, x1 + 1, (y0 + y1) / 2 - 0.2, ramp(C.IRON, 0.6))
    room.rect(x0 - 2.4, y1 + 1.0, x1 + 2.4, y1 + 2.6, ramp(C.TIMBER, 0.5))
    room.rect(x0 - 2.4, y1 + 1.0, x1 + 2.4, y1 + 1.5, ramp(C.TIMBER, 0.8))


def guest_room(seed=1690):
    """The guest room, 112 x 128: the log wall (to y 48) with the shuttered
    window and an unlit lantern, the bare boards below with their stains and
    straw, the door at the bottom."""
    w, h, wall = 112, 128, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd, board=6.0)
    grime(room, 0, wall, w, h, 64)
    C.wear(room, 66, 108, 12, 16, 22)
    stains(room, ((30, 98, 3.0, 1.2), (70, 70, 2.6, 1.0), (88, 114, 3.4, 1.3), (52, 119, 2.0, 1.0), (8, 66, 2.0, 1.0),
                  (60, 92, 1.6, 0.8)), rnd)
    straw_bits(room, 3, wall + 3, w - 3, h - 3, 42, rnd, keep_out=(18, 58, 46, 86))
    log_wall(room, 0, w, 3.6, wall - 2, rnd, tone=0.38)
    C.timber(room, 0, 0, w, 3.8, 0.28, rnd)
    shutters(room, 60, 14, 80, 32, rnd)
    lantern(room, 90, 7)
    # Where the daub has come away: dark gaps between two logs.
    for cx, cy, cw in ((20, 23.6, 5), (98, 36.6, 4), (38, 30.4, 3.5)):
        room.rect(cx, cy, cx + cw, cy + 0.9, ramp(C.SOOT, 0.05))
    cobweb(room, 0, 3.8, 1)
    cobweb(room, w, 3.8, -1)
    C.wall_foot(room, 0, w, wall)
    room.rect(62, 125, 82, h, ramp(C.OAK, 0.4))
    room.rect(62, 125, 82, 125.6, ramp(C.OAK, 0.7))
    for x0, y0, x1, y1 in ((17, 85, 49, 91), (17, 117, 49, 121), (49, 66, 64, 71), (97, 76, 112, 81), (82, 103, 96, 107),
                           (4, 112, 16, 116)):
        room.shadow(x0, y0, x1, y1, 220)
    return C.shrink(room, ROOM_PALETTE)


def patched_blanket(room, x0, y0, x1, y1, rnd):
    """A coarse wool blanket gone dingy, kept inside its rect: mottled and
    pilled, two patches of other grey-brown cloth sewn on with pale
    stitches, a darn; soft folds down its length."""
    saved_lay, saved_d = room.lay, room.d
    room.lay = Image.new("RGBA", saved_lay.size, (0, 0, 0, 0))
    room.d = ImageDraw.Draw(room.lay)
    room.rect(x0, y0, x1, y1, ramp(DINGY, 0.5))
    room.speckle(x0, y0, x1, y1, (x1 - x0) * (y1 - y0) / 4, DINGY, (0.38, 0.6), rnd, (0.3, 0.7))
    for k in range(int((y1 - y0) / 4) + 1):
        room.rect(x0, y0 + 2 + k * 4, x1, y0 + 2.3 + k * 4, ramp(DINGY, 0.4))
    for px, py, pw, ph, tone in ((x0 + 3, y0 + 4, 8, 6, 0.24), (x1 - 10.5, y1 - 9.5, 7.5, 7, 0.8)):
        room.rect(px, py, px + pw, py + ph, ramp(DINGY, tone))
        room.rect(px, py, px + pw, py + 0.5, ramp(DINGY, tone + 0.14))
        room.rect(px, py + ph - 0.5, px + pw, py + ph, ramp(DINGY, tone - 0.14))
        for k in range(int(pw * 1.6)):
            sx = px + k / 1.6
            room.rect(sx, py - 0.2, sx + 0.25, py + 0.3, ramp(C.LINEN, 0.85))
            room.rect(sx, py + ph - 0.3, sx + 0.25, py + ph + 0.2, ramp(C.LINEN, 0.85))
        for k in range(int(ph * 1.6)):
            sy = py + k / 1.6
            room.rect(px - 0.2, sy, px + 0.3, sy + 0.25, ramp(C.LINEN, 0.85))
            room.rect(px + pw - 0.3, sy, px + pw + 0.2, sy + 0.25, ramp(C.LINEN, 0.85))
    dx, dy = x0 + (x1 - x0) * 0.6, y0 + 3
    for k in range(5):
        room.line([(dx + k * 0.6, dy), (dx + k * 0.6 + 0.3, dy + 2.4)], ramp(C.LINEN, 0.45), 0.2)
    woven = room.lay
    room.lay, room.d = saved_lay, saved_d
    box = tuple(int(round(v * C.S)) for v in (x0, y0, x1, y1))
    room.lay.alpha_composite(woven.crop(box), box[:2])
    for fx in (x0 + (x1 - x0) * 0.3, x0 + (x1 - x0) * 0.72):
        drift = rnd.uniform(-1.0, 1.0)
        room.line([(fx, y0 + 2), (fx + drift, y1 - 0.5)], ramp(DINGY, 0.22), 0.8)
        room.line([(fx - 1.0, y0 + 2), (fx - 1.0 + drift, y1 - 0.5)], ramp(DINGY, 0.7), 0.4)
    room.rect(x0, y0, x0 + 0.8, y1, ramp(DINGY, 0.7))
    room.rect(x1 - 0.8, y0, x1, y1, ramp(DINGY, 0.2))


def plank_bed(room, x, y, w, h, rnd):
    """A rough bed of planks in the rooms' view, head to the back wall,
    inside its rect: a plain dark headboard of two boards (one split)
    between square posts; a straw tick in striped ticking, its edge showing
    along the sides, under a patched blanket, narrower toward the head as it
    recedes; the blanket hanging ragged over the foot; one plank for a
    footboard. The kit's rope_bed geometry: the blanket's top starts 12 px
    below the bed's top, where the room's `covers` rect starts, so a
    sleeper's head shows on the pillow above it, and everything down to the
    footboard is inside that rect."""
    head_bot = y + 10
    top_end = y + h - 12.5
    drape_end = y + h - 7
    foot_top = y + h - 7.5
    inset_head, inset_foot = 5.4, 1.6
    head_l, head_r = x + 2.6, x + w - 2.6
    room.poly([(x + inset_head - 1.4, head_bot), (x + w - inset_head + 1.4, head_bot),
               (x + w - inset_foot + 1.4, foot_top), (x + inset_foot - 1.4, foot_top)], ramp(C.OAK, 0.26))
    room.poly([(x + w - inset_head + 0.2, head_bot), (x + w - inset_head + 1.4, head_bot),
               (x + w - inset_foot + 1.4, foot_top), (x + w - inset_foot + 0.2, foot_top)], ramp(C.OAK, 0.14))
    room.poly([(x + inset_head - 1.4, head_bot), (x + inset_head - 0.6, head_bot),
               (x + inset_foot - 0.6, foot_top), (x + inset_foot - 1.4, foot_top)], ramp(C.OAK, 0.52))
    for by0, by1 in ((y + 1.6, y + 5.4), (y + 5.8, y + 9.6)):
        room.rect(head_l + 2, by0, head_r - 2, by1, ramp(C.OAK, 0.3))
        room.rect(head_l + 2, by0, head_r - 2, by0 + 0.7, ramp(C.OAK, 0.56))
        room.rect(head_l + 2, by1 - 0.5, head_r - 2, by1, ramp(C.OAK, 0.12))
    room.line([(x + w * 0.55, y + 5.8), (x + w * 0.62, y + 7.4), (x + w * 0.7, y + 9.6)], ramp(C.OAK, 0.08), 0.3)
    for px in (head_l - 0.4, head_r - 2.8):
        room.rect(px, y, px + 3.2, head_bot + 1.5, ramp(C.OAK, 0.4))
        room.rect(px, y, px + 1.0, head_bot + 1.5, ramp(C.OAK, 0.66))
        room.rect(px + 2.4, y, px + 3.2, head_bot + 1.5, ramp(C.OAK, 0.2))
        room.rect(px, y, px + 3.2, y + 0.6, ramp(C.OAK, 0.82))

    def tick_edges(top):
        # The tick's edge along both sides, a pale strip with blue stripes.
        for side in (-1, 1):
            if side < 0:
                pts = [(x + inset_head - 1.2, top), (x + inset_head, top), (x + inset_foot + 1.2, top_end),
                       (x + inset_foot, top_end)]
            else:
                pts = [(x + w - inset_head, top), (x + w - inset_head + 1.2, top), (x + w - inset_foot, top_end),
                       (x + w - inset_foot - 1.2, top_end)]
            room.poly(pts, ramp(C.LINEN, 0.6 if side < 0 else 0.34))
            for k in range(int((top_end - top) / 2.2)):
                ty = top + 0.8 + k * 2.2
                f = (ty - top) / (top_end - top)
                ex = (x + inset_head - 1.2 + (inset_foot + 1.2 - inset_head) * f) if side < 0 \
                    else (x + w - inset_head + (inset_head - inset_foot - 1.2) * f)
                room.rect(ex, ty, ex + 1.2, ty + 0.5, ramp(C.INDIGO, 0.4))

    patched_blanket(room, x + inset_foot, head_bot + 2, x + w - inset_foot, top_end, rnd)
    tick_edges(head_bot + 2)
    front = [(x + inset_foot, top_end), (x + w - inset_foot, top_end),
             (x + w - inset_foot + 0.8, drape_end), (x + inset_foot - 0.8, drape_end)]
    room.poly(front, ramp(DINGY, 0.24))
    for k in range(5):
        fx = x + inset_foot + (k + 0.5) * (w - 2 * inset_foot) / 5
        room.line([(fx, top_end + 0.6), (fx + rnd.uniform(-0.4, 0.4), drape_end - 0.3)], ramp(DINGY, 0.1), 0.45)
    room.rect(x + inset_foot, top_end, x + w - inset_foot, top_end + 0.6, ramp(DINGY, 0.62))
    for k in range(6):
        hx = x + inset_foot + k * (w - 2 * inset_foot) / 6 + rnd.uniform(0, 2)
        room.rect(hx, drape_end - 0.2, hx + rnd.uniform(0.8, 1.6), drape_end + 0.6, ramp(DINGY, 0.2))
    # The tick at the head, straw poking out; a lumpy pillow of sacking.
    room.rect(x + inset_head - 0.4, head_bot - 0.8, x + w - inset_head + 0.4, head_bot + 2.2, ramp(C.LINEN, 0.5))
    for k in range(int((w - 2 * inset_head) / 2.2) + 1):
        sx = x + inset_head + k * 2.2
        room.rect(sx, head_bot - 0.8, sx + 0.5, head_bot + 2.2, ramp(C.INDIGO, 0.45))
    for k in range(6):
        sx = x + inset_head + rnd.uniform(0, w - 2 * inset_head)
        room.line([(sx, head_bot - 0.6), (sx + rnd.uniform(-1.2, 1.2), head_bot - 2.0)], ramp(C.STRAW, 0.8), 0.3)
    room.ellipse(x + 8, head_bot - 0.6, x + w - 9, head_bot + 4.6, ramp(C.LINEN, 0.44))
    room.ellipse(x + 9.5, head_bot - 0.1, x + w - 13, head_bot + 2.4, ramp(C.LINEN, 0.66))
    room.ellipse(x + 9, head_bot + 3.4, x + w - 10, head_bot + 4.6, ramp(C.LINEN, 0.24))
    room.rect(x + inset_head - 0.2, y + 12, x + w - inset_head + 0.2, y + 13.2, ramp(DINGY, 0.66))
    room.rect(x + inset_head - 0.2, y + 12.8, x + w - inset_head + 0.2, y + 13.4, ramp(DINGY, 0.3))
    room.rect(x + 0.5, foot_top, x + w - 0.5, foot_top + 1.0, ramp(C.OAK, 0.8))
    room.rect(x + 0.5, foot_top + 1.0, x + w - 0.5, y + h - 2.2, ramp(C.OAK, 0.38))
    room.rect(x + 0.5, y + h - 2.8, x + w - 0.5, y + h - 2.2, ramp(C.OAK, 0.16))
    for _ in range(4):
        gx = rnd.uniform(x + 4, x + w - 10)
        room.rect(gx, foot_top + rnd.uniform(1.6, 3.8), gx + rnd.uniform(3, 7), foot_top + 2.0, ramp(C.OAK, 0.28))
    for px in (x, x + w - 3):
        room.rect(px, foot_top - 1.2, px + 3, y + h, ramp(C.OAK, 0.48))
        room.rect(px, foot_top - 1.2, px + 1, y + h, ramp(C.OAK, 0.74))
        room.rect(px + 2.2, foot_top - 1.2, px + 3, y + h, ramp(C.OAK, 0.24))
        room.rect(px, foot_top - 1.2, px + 3, foot_top - 0.6, ramp(C.OAK, 0.88))


def crate(room, x, y, w, h, rnd, top=True):
    """A slatted crate seen from the front at a slant: its top of boards,
    the front of three slats on corner posts, a diagonal brace, a chalk
    mark."""
    lid = 6.0 if top else 0.0
    if top:
        for k in range(3):
            room.rect(x, y + k * lid / 3, x + w, y + (k + 1) * lid / 3 - 0.3, ramp(C.OAK, 0.5 + rnd.uniform(-0.05, 0.05)))
        room.rect(x, y, x + w, y + 0.5, ramp(C.OAK, 0.74))
    room.rect(x, y + lid, x + w, y + h, ramp(C.OAK, 0.08))
    sh = (h - lid) / 3
    for k in range(3):
        t = 0.42 + rnd.uniform(-0.05, 0.05)
        room.rect(x + 0.3, y + lid + k * sh + 0.4, x + w - 0.3, y + lid + (k + 1) * sh - 0.4, ramp(C.OAK, t))
        room.rect(x + 0.3, y + lid + k * sh + 0.4, x + w - 0.3, y + lid + k * sh + 0.9, ramp(C.OAK, t + 0.18))
    for px in (x, x + w - 2.2):
        room.rect(px, y + lid, px + 2.2, y + h, ramp(C.OAK, 0.52))
        room.rect(px, y + lid, px + 0.7, y + h, ramp(C.OAK, 0.76))
    room.line([(x + 2.2, y + h - 1), (x + w - 2.2, y + lid + 1)], ramp(C.OAK, 0.58), 0.7)
    room.rect(x + w / 2 - 1, y + lid + 1.2, x + w / 2 + 1, y + lid + 1.8, ramp(C.LINEN, 0.4))


def crates(room, x, y, w, h, rnd):
    """Two crates side by side at the bed's foot, for a chest."""
    crate(room, x + 0.5, y + 6, w / 2 - 0.5, h - 7, rnd)
    crate(room, x + w / 2 + 0.5, y + 8, w / 2 - 1.0, h - 9, rnd)


def nightstand(room, x, y, w, h, rnd):
    """An upturned crate for a nightstand, and on it a candle stub burning in
    a redware dish, a tinderbox."""
    crate(room, x + 1, y + 27, w - 2, h - 28, rnd)
    room.ellipse(x + 4, y + 25.6, x + 10.4, y + 28.4, ramp(C.REDWARE, 0.4))
    room.ellipse(x + 4.6, y + 25.8, x + 9.6, y + 27.6, ramp(C.REDWARE, 0.7))
    room.rect(x + 6.6, y + 22.0, x + 8.0, y + 27.0, ramp(C.LINEN, 0.8))
    room.rect(x + 7.4, y + 22.0, x + 8.0, y + 27.0, ramp(C.LINEN, 0.55))
    room.ellipse(x + 6.5, y + 19.2, x + 8.1, y + 22.4, ramp(C.FIRE, 0.75))
    room.ellipse(x + 6.9, y + 20.2, x + 7.7, y + 22.0, ramp(C.FIRE, 0.97))
    room.rect(x + 11.4, y + 26.6, x + 13.4, y + 27.6, ramp(C.IRON, 0.5))


def barrel(room, x, y, w, h):
    """A barrel standing on its end: the bulging staves lit on the left, two
    iron hoops at each end, its head seen from above."""
    cx = x + w / 2
    room.ellipse(x, y + 2, x + w, y + h, ramp(C.OAK, 0.4))
    room.rect(x + 0.6, y + 2.5, x + w - 0.6, y + h - 1.5, ramp(C.OAK, 0.42))
    for k in range(5):
        sx = x + 1.2 + k * (w - 2.4) / 5
        room.rect(sx, y + 3, sx + 0.25, y + h - 1, ramp(C.OAK, 0.24))
    room.rect(x + 0.8, y + 3, x + 3.0, y + h - 1.5, ramp(C.OAK, 0.66))
    room.rect(x + w - 2.4, y + 3, x + w - 0.6, y + h - 1.5, ramp(C.OAK, 0.2))
    for hy in (y + 4.5, y + 6.6, y + h - 6.0, y + h - 3.6):
        room.rect(x + 0.3, hy, x + w - 0.3, hy + 1.0, ramp(C.IRON, 0.3))
        room.rect(x + 0.3, hy, x + w - 0.3, hy + 0.35, ramp(C.IRON, 0.6))
    room.ellipse(x + 0.2, y, x + w - 0.2, y + 5, ramp(C.OAK, 0.72))
    room.ellipse(x + 1.0, y + 0.6, x + w - 1.0, y + 4.4, ramp(C.OAK, 0.52))
    room.line([(cx - 3, y + 2.5), (cx + 3, y + 2.5)], ramp(C.OAK, 0.34), 0.25)


def sack(room, x, y, w, h, rnd):
    """A grain sack slumped against the wall, tied at the neck, its ear
    flopped over."""
    body = [(x + 0.6, y + h), (x, y + h * 0.62), (x + 1.4, y + h * 0.36), (x + w * 0.34, y + h * 0.2),
            (x + w * 0.7, y + h * 0.22), (x + w - 0.6, y + h * 0.42), (x + w, y + h * 0.7), (x + w - 0.4, y + h)]
    room.poly(body, ramp(C.STRAW, 0.34))
    room.poly([(x + 0.6, y + h), (x, y + h * 0.62), (x + 1.4, y + h * 0.36), (x + w * 0.34, y + h * 0.2),
               (x + w * 0.42, y + h * 0.3), (x + w * 0.3, y + h)], ramp(C.STRAW, 0.5))
    room.poly([(x + w - 3, y + h), (x + w - 1.2, y + h * 0.5), (x + w, y + h * 0.7), (x + w - 0.4, y + h)], ramp(C.STRAW, 0.18))
    room.poly([(x + w * 0.36, y + h * 0.24), (x + w * 0.42, y + 3), (x + w * 0.6, y + 3), (x + w * 0.66, y + h * 0.24)],
              ramp(C.STRAW, 0.46))
    room.poly([(x + w * 0.5, y + 3), (x + w * 0.62, y + 0.4), (x + w * 0.9, y + 2.6), (x + w * 0.72, y + 5)], ramp(C.STRAW, 0.62))
    room.rect(x + w * 0.36, y + 4.4, x + w * 0.66, y + 5.6, ramp(C.TIMBER, 0.2))
    for k in range(3):
        fy = y + h * 0.42 + k * 4 + rnd.uniform(-0.5, 0.5)
        room.line([(x + 1.6, fy), (x + w * 0.45, fy + 1.6), (x + w - 1.6, fy + 0.4)], ramp(C.STRAW, 0.18), 0.3)
    room.speckle(x + 1, y + h * 0.3, x + w - 1, y + h - 1, 40, C.STRAW, (0.18, 0.52), rnd)


def store(room, x, y, w, h, rnd):
    """A sack leaning on the wall behind a barrel."""
    sack(room, x + 1, y + 3, w - 2, 24, rnd)
    barrel(room, x + 1, y + 26, w - 2, h - 27)


def stool(room, x, y, w, h, rnd):
    """A three-legged stool: a thick round seat, splayed legs, a stretcher."""
    room.line([(x + 2.5, y + 4), (x + 0.8, y + h - 0.6)], ramp(C.OAK, 0.4), 1.1)
    room.line([(x + w - 2.5, y + 4), (x + w - 0.8, y + h - 0.6)], ramp(C.OAK, 0.3), 1.1)
    room.line([(x + w / 2, y + 4), (x + w / 2 + 0.4, y + h - 2.0)], ramp(C.OAK, 0.22), 1.0)
    room.rect(x + 2.0, y + h - 6, x + w - 2.0, y + h - 5.2, ramp(C.OAK, 0.3))
    room.ellipse(x, y + 2.2, x + w, y + 6.4, ramp(C.OAK, 0.3))
    room.ellipse(x, y + 1.4, x + w, y + 5.4, ramp(C.OAK, 0.66))
    room.ellipse(x + 1.4, y + 1.8, x + w * 0.6, y + 3.6, ramp(C.OAK, 0.84))


def bucket(room, x, y, w, h, rnd):
    """A wooden slop bucket: staves narrowing to the foot, two iron hoops, its
    open rim lit with the dark inside, a rope handle arched over it."""
    rim, foot = y + 6.0, y + h - 0.2   # a polygon's edge is inclusive: the foot just inside the rect
    room.poly([(x + 0.6, rim), (x + w - 0.6, rim), (x + w - 2.0, foot), (x + 2.0, foot)], ramp(C.OAK, 0.4))
    room.poly([(x + 0.6, rim), (x + 3.0, rim), (x + 3.6, foot), (x + 2.0, foot)], ramp(C.OAK, 0.64))
    for k in range(3):
        sx = x + 4.2 + k * 2.4
        room.line([(sx, rim + 0.5), (sx + (k - 1) * 0.15, y + h - 0.5)], ramp(C.OAK, 0.24), 0.25)
    for hy, inset in ((rim + 2.0, 0.9), (y + h - 2.6, 1.8)):
        room.rect(x + inset, hy, x + w - inset, hy + 1.0, ramp(C.IRON, 0.3))
        room.rect(x + inset, hy, x + w - inset, hy + 0.35, ramp(C.IRON, 0.6))
    room.ellipse(x + 0.2, rim - 2.4, x + w - 0.2, rim + 1.6, ramp(C.OAK, 0.78))
    room.ellipse(x + 1.3, rim - 1.6, x + w - 1.3, rim + 0.9, ramp(C.SOOT, 0.35))
    room.ellipse(x + 2.4, rim - 0.6, x + w - 3.4, rim + 0.6, ramp(C.SOOT, 0.12))
    room.line([(x + 0.8, rim - 0.4), (x + 2.0, rim - 4.6), (x + w / 2, rim - 5.8), (x + w - 2.0, rim - 4.6), (x + w - 0.8, rim - 0.4)],
              ramp(C.STRAW, 0.62), 0.45)


# ---------------------------------------------------------------- the rooms

def _paint(painter):
    return lambda room, x, y, w, h, rnd: painter(room, x, y, w, h, rnd)


_BAR = _paint(c_bar)

# Each room: its sheets (room, pieces, open door or None), size in room px,
# and the pieces as ((x, y, w, h, sort_y), painter): the rects and sort lines
# the floor plan's stations, the keeper's place and the sale are laid out
# against, which interior_rooms.gd's entry must equal. Pieces that share a
# painter are painted as one over their joined rect (the C bar: one counter,
# which the client cuts in two by its arms' rects and sort lines). The pack's
# chairs overlapped the table and its bucket the crates; here those rects end
# where the neighbour's begin, as the kit's paint_pieces requires: the table
# 28 px (its serve points stay on it), the chairs 14 and 13, the bucket 13.
ROOMS = {
    COMMON_KEY: {
        "sheets": ("tavern_common", "tavern_pieces", None),
        "size": (128, 112),
        "pieces": [
            ((82, 50, 48, 30, 79), _BAR),
            ((82, 80, 48, 30, 99), _BAR),
            ((8, 70, 14, 32, 95), _chair(True)),
            ((50, 70, 13, 32, 95), _chair(False)),
            ((22, 72, 28, 32, 96), _paint(round_table)),
        ],
        "covers": {},
        "anims": [FIRE],
        "bar": (90, 94),
        "reach": (102, 80),
    },
    "tavern_hall": {
        "sheets": ("tavern_hall_common", "tavern_hall_pieces", "tavern_hall_door_open"),
        "size": (256, 128),
        "pieces": [
            ((0, 63, 115, 19, 80), lambda room, x, y, w, h, rnd: banister(room, x, y, w, h, rnd, True, "stair")),
            ((141, 63, 115, 19, 80), lambda room, x, y, w, h, rnd: banister(room, x, y, w, h, rnd, "stair", True)),
        ],
        "covers": {},
        "anims": [],
    },
    "tavern_guest": {
        "sheets": ("tavern_guest_common", "tavern_guest_pieces", None),
        "size": (112, 128),
        "pieces": [
            ((16, 44, 32, 44, 71), _paint(plank_bed)),
            ((16, 88, 32, 32, 119), _paint(crates)),
            ((48, 25, 16, 45, 69), _paint(nightstand)),
            ((96, 30, 16, 50, 79), _paint(store)),
            ((81, 91, 14, 16, 101), _paint(stool)),
            ((3, 100, 13, 16, 115), _paint(bucket)),
        ],
        "covers": {"bed": (16, 56, 32, 32)},
        "anims": [],
    },
}


def piece_groups(pieces):
    """The pieces as paint_pieces is given them: [(rect, painter)], pieces
    that share a painter object joined into one rect. The declared rects are
    held to paint_pieces' rule first (no two overlap, the members of a group
    included), and a group's rects must tile their joined rect exactly, so a
    split moved by a pixel (a gap or an overlap between the bar's arms) is
    refused rather than painted over. Raises ValueError."""
    rects = [r[:4] for r, _p in pieces]
    for r in rects:
        if r[2] <= 0 or r[3] <= 0:
            raise ValueError("piece %s has no area" % (r,))
    for i, (ax, ay, aw, ah) in enumerate(rects):
        for bx, by, bw, bh in rects[i + 1:]:
            if ax < bx + bw and bx < ax + aw and ay < by + bh and by < ay + ah:
                raise ValueError("pieces %s and %s overlap" % ((ax, ay, aw, ah), (bx, by, bw, bh)))
    groups = []
    for (x, y, w, h, _sort), painter in pieces:
        for g in groups:
            if g[1] is painter:
                g[0].append((x, y, w, h))
                break
        else:
            groups.append([[(x, y, w, h)], painter])
    out = []
    for members, painter in groups:
        x0, y0 = min(r[0] for r in members), min(r[1] for r in members)
        x1, y1 = max(r[0] + r[2] for r in members), max(r[1] + r[3] for r in members)
        if sum(r[2] * r[3] for r in members) != (x1 - x0) * (y1 - y0):
            raise ValueError("pieces %s painted as one do not fill %s" % (members, (x0, y0, x1 - x0, y1 - y0)))
        out.append(((x0, y0, x1 - x0, y1 - y0), painter))
    return out


def pieces_sheet(key, seed):
    """A room's pieces through the kit's paint_pieces, pieces that share a
    painter painted once over their joined rect."""
    spec = ROOMS[key]
    rnd = random.Random(seed)
    return C.paint_pieces(*spec["size"], [(r, (lambda room, px, py, pw, ph, p=p: p(room, px, py, pw, ph, rnd)))
                                         for r, p in piece_groups(spec["pieces"])], PIECE_PALETTE)


SHEETS = {
    "tavern_common": common_room,
    "tavern_pieces": lambda: pieces_sheet(COMMON_KEY, 1693),
    "tavern_fire": lambda: fire_strip(*FIRE_FRAME, FIRE["frames"], seed=17),
    "tavern_hall_common": hall_room,
    "tavern_hall_pieces": lambda: pieces_sheet("tavern_hall", 808),
    "tavern_hall_door_open": hall_door_open,
    "tavern_guest_common": guest_room,
    "tavern_guest_pieces": lambda: pieces_sheet("tavern_guest", 1691),
}
# Art px: the rooms at twice their room px, the open door 32 x 32 room px (a
# hall door), the fire 4 frames of 24 x 18 room px.
SIZES = {
    "tavern_common": (256, 224), "tavern_pieces": (256, 224), "tavern_fire": (192, 36),
    "tavern_hall_common": (512, 256), "tavern_hall_pieces": (512, 256), "tavern_hall_door_open": (64, 64),
    "tavern_guest_common": (224, 256), "tavern_guest_pieces": (224, 256),
}


# Each anim strip painted here: its frame in room px.
ANIM_FRAMES = {"tavern_fire": FIRE_FRAME}


def _entry_body(key):
    text = open(C.ROOMS_GD, encoding="utf-8").read()
    return re.search(r'\n    "%s": \{(.*?)\n    \},' % re.escape(key), text, re.S).group(1)


_FIELD = r'\s*"(\w+)"\s*:\s*("[^"\\]*"|-?\d+\.\d+|-?\d+)\s*'


def _entry_anims(body):
    """The entry's anims, each as a dict of all its fields, so a changed fps
    or an added sort_y shows. The whole array is read wherever its line
    breaks fall, and every field must be a "key": value of a plain string,
    int or decimal; anything else (an exponent, a bool, a nested value) raises
    ValueError rather than being skipped."""
    m = re.search(r'"anims"\s*:\s*\[', body)
    if not m:
        return []
    depth, i = 1, m.end()
    while depth:
        if i >= len(body):
            raise ValueError("the anims array is not closed")
        depth += {"[": 1, "]": -1}.get(body[i], 0)
        i += 1
    inner = body[m.end():i - 1]
    anims = []
    for item in re.findall(r'\{([^{}]*)\}', inner):
        if not re.fullmatch(r'%s(,%s)*,?\s*' % (_FIELD, _FIELD), item):
            raise ValueError("an anim this check cannot read whole: {%s}" % item.strip())
        fields = {}
        for k, v in re.findall(_FIELD, item):
            fields[k] = v[1:-1] if v.startswith('"') else (float(v) if "." in v else int(v))
        anims.append(fields)
    if re.sub(r'\{[^{}]*\}|[\s,]', "", inner):
        raise ValueError("the anims array holds something other than anims: [%s]" % inner.strip())
    return anims


def check(out_dir):
    """Each sheet is exactly what this script paints now and of its size;
    each room's entry in interior_rooms.gd names these sheets (and the
    hall's open door) at this density and size, and has exactly the
    declared pieces (rects and sort lines, in order), covers, anims (every
    field: sheet, frames, fps, position, and no sort_y unless declared) and
    bar / reach points; the bar's arms tile its joined rect (a split moved
    by a pixel is refused); every piece rect holds art; every cover rect
    lies inside its bed's piece and holds the blanket; the open door covers
    the closed one whole; each anim strip is its declared frames of its
    declared size; the bar's sale point is on the counter."""
    ok = True
    # The kit's own guards (common.selftest): a removed guard would leave
    # every sheet unchanged, so the sheets alone cannot show it.
    for f in C.selftest():
        print("common selftest: " + f)
        ok = False
    # The grouping refuses the bar's split moved a pixel either way (an
    # overlap, a gap between the arms) and a collapsed arm.
    bar = ROOMS[COMMON_KEY]["pieces"]
    (x, y, w, h, s), p = bar[1]
    for label, arm in (("moved up a px", (x, y - 1, w, h + 1, s)), ("moved down a px", (x, y + 1, w, h - 1, s)),
                       ("collapsed", (x, y, w, 0, s))):
        try:
            piece_groups([bar[0], (arm, p)] + bar[2:])
            print("the bar's front arm %s was not refused" % label)
            ok = False
        except ValueError:
            pass
    sheets = {}
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun tavern.py)" % name)
            ok = False
        if sheet.size != SIZES[name]:
            print("%s: %dx%d, not %dx%d" % ((name,) + sheet.size + SIZES[name]))
            ok = False
        sheets[name] = sheet
    d = C.DENSITY
    for key, spec in ROOMS.items():
        entry = C.room_entry(key)
        body = _entry_body(key)
        common, pieces, door = spec["sheets"]
        want = {"sheet": "res://assets/interiors/%s.png" % common, "pieces_sheet": "res://assets/interiors/%s.png" % pieces}
        door_m = re.search(r'"door_open": "([^"]+)"', body)
        try:
            anims = _entry_anims(body)
        except ValueError as e:
            anims = "unreadable (%s)" % e
        points = {}
        for k in ("bar", "reach"):
            m = re.search(r'"%s": \{"x": (-?\d+), "y": (-?\d+)\}' % k, body)
            if m:
                points[k] = tuple(int(v) for v in m.groups())
        for label, got, expect in (
                ("sheets", entry["sheets"], want),
                ("door_open", door_m.group(1) if door_m else None, "res://assets/interiors/%s.png" % door if door else None),
                ("density", entry["density"], d),
                ("size", entry["size"], spec["size"]),
                ("pieces", entry["pieces"], [r for r, _p in spec["pieces"]]),
                ("covers", entry["covers"], spec["covers"]),
                ("anims", anims, spec["anims"]),
                ("bar and reach", points, {k: spec[k] for k in ("bar", "reach") if k in spec})):
            if got != expect:
                print("%s: interior_rooms.gd %s are %s, the painted %s" % (key, label, got, expect))
                ok = False
        for name in (common, pieces):
            if sheets[name].size != (spec["size"][0] * d, spec["size"][1] * d):
                print("%s: %s is %s, not the room's size at density %d" % (key, name, sheets[name].size, d))
                ok = False
        alpha = sheets[pieces].split()[3]
        for (x, y, w, h, _s), _p in spec["pieces"]:
            if alpha.crop((x * d, y * d, (x + w) * d, (y + h) * d)).getbbox() is None:
                print("%s: no art in the piece rect %s" % (key, (x, y, w, h)))
                ok = False
        for bed, (x, y, w, h) in spec["covers"].items():
            inside = any(px <= x and py <= y and x + w <= px + pw and y + h <= py + ph
                         for (px, py, pw, ph, _s), _p in spec["pieces"])
            filled = alpha.crop(((x + 4) * d, (y + 2) * d, (x + w - 4) * d, (y + h - 8) * d)).getextrema()[0] == 255
            if not inside or not filled:
                print("%s: cover %s %s is not a covered part of a bed piece" % (key, bed, (x, y, w, h)))
                ok = False
        if door and sheets[door].split()[3].getextrema()[0] != 255:
            print("%s: the open door does not cover the closed one whole" % key)
            ok = False
        for a in spec["anims"]:
            name = os.path.splitext(os.path.basename(a["sheet"]))[0]
            strip, frame = sheets.get(name), ANIM_FRAMES.get(name)
            if strip is None or frame is None or strip.size != (a["frames"] * frame[0] * d, frame[1] * d):
                print("%s: anim %s is not painted here as %d frames of %s" % (key, a["sheet"], a["frames"], frame))
                ok = False
        if "bar" in spec:
            bx, by = spec["bar"]
            if alpha.getpixel((bx * d, (by - 1) * d)) != 255:
                print("%s: the bar's sale point %s is not on the counter" % (key, spec["bar"]))
                ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's Tavern interiors.")
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
