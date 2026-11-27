"""The goods sheet (LLM-969): stall_goods.png, every good a villager can sell
or be served, painted for salem. Every pixel is drawn here, no pack art.
stalls.py paints it with the stall close-ups.

The sheet is a grid of COLUMNS cells of 16 x 32 room px (CELL), drawn at
GOODS_DENSITY art px per room px. Good i has two cells: 2i is the good
itself (one of it; the client draws two of it, a little apart, for a few),
2i+1 a pile of it (heaped in its basket, sack, crate or barrel, standing on
the floor behind a stall's counter). Art stands on the cell's bottom edge,
the good's within the lower 20 rows (the client's tap box for a good).

GOODS is in StallScenes.GOODS order: kind -> index there must be the index
of the entry here that lists the kind (stalls.py --check holds the two
together). Add at the end, never reorder, or every good after the change
draws as another.

The view is the rooms': things seen from the front and a little above,
light from the upper left. 1690s New England goods: splint baskets, linen
and tow sacks, joined crates, staved and hooped pails and casks, redware and
grey stoneware, pewter, wrought iron, wool and linen dyed with madder,
indigo, logwood and weld.
"""

import math
import random

from PIL import Image

import common as C

ramp = C.ramp

CELL = (16, 32)
COLUMNS = 16
# Art px per room px: the sheet's own, whatever room it shows in
# (StallScenes.GOODS_DENSITY).
DENSITY = 2

# Produce and goods colours, dark to light.
APPLE = ["3a0e0c", "5e1612", "862218", "aa3220", "c84a2c", "e0704a"]
RASP = ["420c18", "6a1428", "922236", "b83448", "d85868"]
BLUEB = ["14182c", "222a48", "323e66", "4a5a86", "7084aa"]
CARROT = ["5a2208", "86360c", "b05014", "d06c1e", "e88c34"]
CHEESE = ["6a4a10", "946a1c", "bc8e2c", "d8b048", "ecd070", "f6e6a0"]
CREAM = ["7e766a", "a49c88", "c8c0aa", "e4dec8", "f8f4e8"]
MEAT = ["4a1210", "7a2018", "a8342a", "c85448", "e0887a", "f0bca4"]
BREAD = ["4a2a10", "70421a", "986028", "bc8038", "d8a458", "ecc888"]
WATER = ["1e2a2c", "2e4044", "46585a", "6a7c78", "9aaca6"]
BARK = ["241e18", "3a322a", "544a3e", "6e6252", "887c6a"]
LEATHER = ["2a1a10", "40281a", "5a3a24", "764e30", "926844"]
WOOL = ["2e241c", "463628", "5e4a38", "78624c", "927c62"]
GREEN = ["1e2a1a", "2c3c24", "3e5232", "566c46", "70885c"]
LOGWOOD = ["240e22", "3a1636", "562450", "74386c", "94528a"]
WELD = ["5a4c0c", "86721a", "b09a28", "d4bc3c", "ece060"]
AMBER = ["4a2a08", "7a4810", "a86c1a", "cc922c", "e8b850"]
GLASSG = ["1e2e24", "2e4636", "48644e", "6e8c72", "a0bca0"]
BRASS = ["4a3a14", "74602a", "9a8440", "c4ac5c", "e4d08a"]
STONEWARE = ["3a3a38", "5a5a56", "7c7c76", "a0a098", "c4c4ba"]
# Cloth dyed at home, muted: brick (madder), olive (weld over indigo).
BRICK = ["3c1e16", "5a2c20", "763c2c", "8e4e3a", "a2624c"]
OLIVE = ["262818", "3a3c24", "505232", "686a44", "828258"]
LEAF = C.HERB

PALETTE = (C.OAK + C.STRAW + C.LINEN + C.REDWARE + C.PEWTER + C.IRON + C.MADDER + C.INDIGO + C.HERB + APPLE + RASP
           + BLUEB + CARROT + CHEESE + CREAM + MEAT + BREAD + WATER + BARK + LEATHER + WOOL + GREEN + LOGWOOD + WELD
           + AMBER + GLASSG + BRASS + STONEWARE + BRICK + OLIVE)

BOT = 32.0          # the cell's floor line


# ---------------------------------------------------------------- helpers

def ring(r, x0, y0, x1, y1, col, w=0.5):
    r.d.ellipse((x0 * C.S, y0 * C.S, x1 * C.S - 1, y1 * C.S - 1), outline=col, width=max(1, int(w * C.S)))


def arc(r, x0, y0, x1, y1, a0, a1, col, w=0.5):
    r.d.arc((x0 * C.S, y0 * C.S, x1 * C.S - 1, y1 * C.S - 1), a0, a1, fill=col, width=max(1, int(w * C.S)))


def ball(r, cx, cy, rx, ry, cols, tone=0.55):
    """A round thing lit from the upper left: body, shade low right, a
    highlight up left."""
    r.ellipse(cx - rx, cy - ry, cx + rx, cy + ry, ramp(cols, tone - 0.22))
    r.ellipse(cx - rx, cy - ry, cx + rx * 0.7, cy + ry * 0.65, ramp(cols, tone))
    r.ellipse(cx - rx * 0.6, cy - ry * 0.7, cx - rx * 0.05, cy - ry * 0.15, ramp(cols, tone + 0.25))


def shadow(r, x0, x1, y=BOT):
    """The good's shadow on what it stands on, cast a little right."""
    return


# ---------------------------------------------------------------- containers
# Each container is drawn in two parts: its back (the far rim and the dark
# inside), then whatever is heaped in it, then its front (the near rim and
# body), so the heap sits down inside it.

def basket_back(r, x0, x1, rim):
    r.ellipse(x0, rim - 2.0, x1, rim + 2.0, ramp(C.STRAW, 0.42))
    r.ellipse(x0 + 0.8, rim - 1.4, x1 - 0.8, rim + 1.4, ramp(C.STRAW, 0.08))


def basket_front(r, x0, x1, rim, bot=BOT):
    """A splint basket: the body tapering a little to its foot, woven in
    rows of splints, the near rim bound and lit."""
    inset = (x1 - x0) * 0.08
    r.poly([(x0, rim), (x1, rim), (x1 - inset, bot - 0.6), (x0 + inset, bot - 0.6)], ramp(C.STRAW, 0.36))
    r.rect(x0 + inset + 0.6, bot - 1.0, x1 - inset - 0.6, bot, ramp(C.STRAW, 0.22))
    row = 0
    y = rim + 0.8
    while y < bot - 1.0:
        f = (y - rim) / max(1, bot - rim)
        a, b = x0 + inset * f, x1 - inset * f
        x = a + (0.9 if row % 2 else 0)
        while x < b - 0.3:
            r.rect(x, y, min(b, x + 0.9), y + 0.9, ramp(C.STRAW, 0.5 if (x - a) < (b - a) * 0.4 else 0.4))
            x += 1.8
        r.rect(a, y + 0.9, b, y + 1.2, ramp(C.STRAW, 0.18))
        y += 1.3
        row += 1
    # Shade down the right side.
    r.poly([(x1 - (x1 - x0) * 0.25, rim), (x1, rim), (x1 - inset, bot - 0.6), (x1 - inset - (x1 - x0) * 0.2, bot - 0.6)],
           ramp(C.STRAW, 0.36))
    arc(r, x0, rim - 2.0, x1, rim + 2.0, 0, 180, ramp(C.STRAW, 0.82), 0.7)
    arc(r, x0, rim - 1.6, x1, rim + 2.4, 20, 160, ramp(C.STRAW, 0.32), 0.3)


def crate_back(r, x0, x1, rim):
    r.poly([(x0, rim), (x0 + 1.6, rim - 2.6), (x1 - 1.6, rim - 2.6), (x1, rim)], ramp(C.OAK, 0.6))
    r.poly([(x0 + 0.9, rim - 0.2), (x0 + 2.0, rim - 2.0), (x1 - 2.0, rim - 2.0), (x1 - 0.9, rim - 0.2)], ramp(C.OAK, 0.06))


def crate_front(r, x0, x1, rim, bot=BOT):
    """A joined crate of pine boards: two boards across the front, corner
    posts, nail heads, the top edge lit."""
    r.rect(x0, rim, x1, bot, ramp(C.OAK, 0.26))
    mid = (rim + bot) / 2
    for y0, y1 in ((rim, mid), (mid, bot)):
        r.rect(x0 + 1.2, y0 + 0.3, x1 - 1.2, y1 - 0.3, ramp(C.OAK, 0.4))
        r.rect(x0 + 1.2, y0 + 0.3, x1 - 1.2, y0 + 0.7, ramp(C.OAK, 0.56))
        r.rect(x0 + 2.4, y0 + 1.4, x0 + 5.6, y0 + 1.65, ramp(C.OAK, 0.4))
    r.rect(x0, rim, x0 + 1.2, bot, ramp(C.OAK, 0.52))
    r.rect(x1 - 1.2, rim, x1, bot, ramp(C.OAK, 0.2))
    for nx in (x0 + 0.4, x1 - 0.8):
        for ny in (rim + 1.0, bot - 1.6):
            r.rect(nx, ny, nx + 0.5, ny + 0.5, ramp(C.IRON, 0.2))
    r.rect(x0, rim, x1, rim + 0.6, ramp(C.OAK, 0.72))
    r.rect(x0, bot - 0.5, x1, bot, ramp(C.OAK, 0.18))


def sack(r, x0, x1, top, cols, bot=BOT, open_top=True):
    """A sack slumped on the floor, its mouth rolled back (open) or tied."""
    w = x1 - x0
    r.poly([(x0 + w * 0.12, top + 2.0), (x1 - w * 0.12, top + 2.0), (x1, bot - 3.0), (x1 - w * 0.08, bot),
            (x0 + w * 0.08, bot), (x0, bot - 3.0)], ramp(cols, 0.5))
    r.poly([(x0 + w * 0.62, top + 2.0), (x1 - w * 0.12, top + 2.0), (x1, bot - 3.0), (x1 - w * 0.08, bot),
            (x0 + w * 0.6, bot)], ramp(cols, 0.36))
    r.poly([(x0 + w * 0.12, top + 2.0), (x0 + w * 0.3, top + 2.0), (x0 + w * 0.18, bot - 1.0), (x0, bot - 3.0)],
           ramp(cols, 0.66))
    for k in range(3):
        fx = x0 + w * (0.3 + 0.2 * k)
        r.line([(fx, top + 3.4), (fx + 0.6, bot - 1.4)], ramp(cols, 0.28), 0.3)
    if open_top:
        r.ellipse(x0 + w * 0.06, top, x1 - w * 0.06, top + 4.0, ramp(cols, 0.62))
        r.ellipse(x0 + w * 0.16, top + 0.6, x1 - w * 0.16, top + 3.0, ramp(cols, 0.2))


def barrel(r, x0, x1, top, bot=BOT, cols=C.OAK, lid=False):
    """A staved barrel or tub, hooped in iron: bulging staves lit on the
    left, the top's rim (or its lid)."""
    w = x1 - x0
    r.poly([(x0 + 0.6, top + 1.2), (x1 - 0.6, top + 1.2), (x1, (top + bot) / 2), (x1 - 0.6, bot - 0.4),
            (x0 + 0.6, bot - 0.4), (x0, (top + bot) / 2)], ramp(cols, 0.44))
    n = max(4, int(w / 1.6))
    for k in range(n):
        sx = x0 + 0.6 + (w - 1.2) * k / n
        t = 0.6 - 0.3 * k / n
        r.rect(sx + 0.2, top + 1.4, sx + (w - 1.2) / n - 0.1, bot - 0.6, ramp(cols, t))
        r.rect(sx, top + 1.4, sx + 0.2, bot - 0.6, ramp(cols, 0.2))
    for hy in (top + 2.4, bot - 2.6):
        r.rect(x0 + 0.2, hy, x1 - 0.2, hy + 0.9, ramp(C.IRON, 0.32))
        r.rect(x0 + 0.2, hy, x1 - 0.2, hy + 0.3, ramp(C.IRON, 0.62))
    r.ellipse(x0 + 0.4, top, x1 - 0.4, top + 2.6, ramp(cols, 0.7))
    if lid:
        r.ellipse(x0 + 1.0, top + 0.3, x1 - 1.0, top + 2.2, ramp(cols, 0.58))
        r.line([(x0 + 2, top + 1.2), (x1 - 2, top + 1.2)], ramp(cols, 0.3), 0.3)
    else:
        r.ellipse(x0 + 1.0, top + 0.4, x1 - 1.0, top + 2.2, ramp(cols, 0.1))


def bowl(r, cx, bot, w, cols=C.OAK, fill=None, fill_tone=0.5, depth=3.6):
    """A turned wooden (or redware) bowl seen from a little above: the rim
    an oval, its contents inside, the body under it."""
    x0, x1 = cx - w / 2, cx + w / 2
    top = bot - depth
    r.ellipse(x0 + 0.4, top + 0.6, x1 - 0.4, bot + 1.2, ramp(cols, 0.24))
    r.rect(x0 + 0.4, top + 1.6, x1 - 0.4, bot - 0.8, ramp(cols, 0.24))
    r.ellipse(x0 + 0.8, top + 1.2, cx + w * 0.1, bot - 0.2, ramp(cols, 0.44))
    r.rect(cx - w * 0.22, bot - 0.6, cx + w * 0.22, bot, ramp(cols, 0.12))
    r.ellipse(x0, top - 0.6, x1, top + 2.6, ramp(cols, 0.84))
    if fill is None:
        r.ellipse(x0 + 0.7, top - 0.1, x1 - 0.7, top + 1.9, ramp(cols, 0.18))
        r.ellipse(cx - w * 0.05, top + 0.6, x1 - 1.0, top + 1.8, ramp(cols, 0.3))
    else:
        r.ellipse(x0 + 0.7, top - 0.1, x1 - 0.7, top + 1.9, ramp(fill, fill_tone))
        r.ellipse(x0 + 1.4, top + 0.1, cx, top + 1.0, ramp(fill, fill_tone + 0.2))
    return top


def plate(r, cx, bot, w, cols=C.PEWTER):
    """A pewter plate (or wooden trencher) seen from a little above."""
    x0, x1 = cx - w / 2, cx + w / 2
    r.ellipse(x0, bot - 3.4, x1, bot, ramp(cols, 0.18))
    r.ellipse(x0, bot - 4.2, x1, bot - 0.9, ramp(cols, 0.8))
    r.ellipse(x0 + 1.6, bot - 3.6, x1 - 1.6, bot - 1.5, ramp(cols, 0.5))
    r.ellipse(x0 + 0.6, bot - 3.8, cx - 1, bot - 2.8, ramp(cols, 0.92))
    return bot - 2.3


def tankard(r, cx, bot, cols=C.PEWTER, inside=None, foam=False):
    """A lidless pewter tankard, its handle on the right: a straight body
    with a lit band on the left, a fillet near the foot; foam heaped over
    the rim, or the dark inside of an empty one."""
    x0, x1 = cx - 3.0, cx + 3.0
    top = bot - 8.0
    r.line([(x1 - 0.2, top + 1.6), (x1 + 2.4, top + 2.2), (x1 + 2.4, top + 5.6), (x1 - 0.2, top + 6.4)],
           ramp(cols, 0.4), 0.8)
    r.rect(x0, top + 1, x1, bot, ramp(cols, 0.5))
    r.rect(x0, top + 1, x0 + 1.4, bot, ramp(cols, 0.82))
    r.rect(x1 - 1.4, top + 1, x1, bot, ramp(cols, 0.28))
    r.rect(x0 - 0.3, bot - 1.6, x1 + 0.3, bot - 0.8, ramp(cols, 0.62))
    r.rect(x0 - 0.3, bot - 0.8, x1 + 0.3, bot, ramp(cols, 0.3))
    r.ellipse(x0, top, x1, top + 2.2, ramp(cols, 0.72))
    if foam:
        r.ellipse(x0 + 0.4, top + 0.2, x1 - 0.4, top + 1.8, ramp(AMBER, 0.6))
        for fx, fy, fr in ((x0 + 1.4, top - 0.2, 1.6), (cx + 0.2, top - 1.0, 1.9), (x1 - 1.2, top - 0.1, 1.5)):
            r.ellipse(fx - fr, fy - fr * 0.8, fx + fr, fy + fr * 0.8, ramp(CREAM, 0.72))
            r.ellipse(fx - fr * 0.7, fy - fr * 0.8, fx + fr * 0.3, fy, ramp(CREAM, 0.98))
        r.rect(x0 + 0.6, top + 1.4, x0 + 1.2, top + 3.6, ramp(CREAM, 0.85))
    else:
        r.ellipse(x0 + 0.5, top + 0.3, x1 - 0.5, top + 1.9, ramp(cols, 0.1))


# ---------------------------------------------------------------- produce

def apples(r, pts, rnd, size=2.4):
    for cx, cy in pts:
        ball(r, cx, cy, size, size * 0.92, APPLE, rnd.uniform(0.5, 0.66))
        r.line([(cx + 0.2, cy - size * 0.8), (cx + 0.6, cy - size * 1.25)], ramp(C.OAK, 0.3), 0.3)
        if rnd.random() < 0.4:
            r.ellipse(cx + 0.6, cy - size * 1.3, cx + 2.0, cy - size * 0.9, ramp(LEAF, 0.6))


def heap_balls(r, x0, x1, top, base, size, cols, rnd, n=14, stem=False):
    """A heaped mound of round produce from base up to top, the far ones
    first."""
    pts = []
    for k in range(n):
        f = rnd.random()
        x = rnd.uniform(x0 + size, x1 - size)
        hump = top + (base - top) * (abs((x - (x0 + x1) / 2) / ((x1 - x0) / 2)) ** 1.6)
        pts.append((x, rnd.uniform(hump, base)))
    pts.sort(key=lambda p: p[1])
    for x, y in pts:
        ball(r, x, y, size, size * 0.92, cols, rnd.uniform(0.48, 0.68))
        if stem:
            r.line([(x + 0.2, y - size * 0.8), (x + 0.5, y - size * 1.2)], ramp(C.OAK, 0.3), 0.25)


def berries(r, x0, x1, top, base, cols, rnd, n=30, size=0.9):
    pts = []
    for k in range(n):
        x = rnd.uniform(x0 + size, x1 - size)
        hump = top + (base - top) * (abs((x - (x0 + x1) / 2) / ((x1 - x0) / 2)) ** 1.5)
        pts.append((x, rnd.uniform(hump, base)))
    pts.sort(key=lambda p: p[1])
    for x, y in pts:
        r.ellipse(x - size, y - size, x + size, y + size, ramp(cols, rnd.uniform(0.25, 0.5)))
        r.ellipse(x - size * 0.7, y - size * 0.8, x + size * 0.2, y, ramp(cols, rnd.uniform(0.6, 0.85)))
        r.rect(x - 0.4, y - 0.6, x - 0.1, y - 0.3, ramp(cols, 1.0))


def carrot(r, x, y, ang, ln, rnd):
    """A carrot from its crown at (x, y) pointing along ang (radians, 0 =
    right), ln long, its tops feathering back."""
    dx, dy = math.cos(ang), math.sin(ang)
    px, py = -dy, dx
    w = 1.3
    r.poly([(x + px * w, y + py * w), (x + dx * ln, y + dy * ln), (x - px * w, y - py * w)], ramp(CARROT, 0.55))
    r.poly([(x + px * w, y + py * w), (x + dx * ln, y + dy * ln), (x + px * 0.3, y + py * 0.3)], ramp(CARROT, 0.82))
    for k in range(3):
        t = 0.25 + 0.2 * k
        r.line([(x + dx * ln * t + px * 0.7, y + dy * ln * t + py * 0.7), (x + dx * ln * t - px * 0.2, y + dy * ln * t - py * 0.2)],
               ramp(CARROT, 0.3), 0.25)
    for k in range(5):
        a = ang + math.pi + rnd.uniform(-0.6, 0.6)
        l2 = rnd.uniform(2.5, 4.5)
        r.line([(x, y), (x + math.cos(a) * l2, y + math.sin(a) * l2)], ramp(LEAF, rnd.uniform(0.5, 0.9)), 0.45)


def wheat_ears(r, x, y, ang, ln, rnd, n=1):
    """A wheat stalk from (x, y) along ang, the ear at its far end."""
    dx, dy = math.cos(ang), math.sin(ang)
    ex, ey = x + dx * ln, y + dy * ln
    r.line([(x, y), (ex, ey)], ramp(C.STRAW, 0.6), 0.3)
    for k in range(5):
        t = 1.0 + k * 0.6
        cx, cy = ex + dx * t, ey + dy * t
        r.ellipse(cx - 0.7, cy - 0.7, cx + 0.7, cy + 0.7, ramp(C.STRAW, 0.7 + 0.04 * k))
        r.line([(cx, cy), (cx + dx * 2.6 - dy * 0.8, cy + dy * 2.6 + dx * 0.8)], ramp(C.STRAW, 0.85), 0.15)


# ---------------------------------------------------------------- goods: good and pile painters
# Each takes the cell's canvas and a random source.

def g_apples(r, rnd):
    apples(r, [(5.2, 28.6), (10.8, 28.8), (8.0, 25.4)], rnd)
    shadow(r, 2, 13)


def p_apples(r, rnd):
    basket_back(r, 1, 15, 20)
    heap_balls(r, 1.5, 14.5, 14.5, 21, 2.2, APPLE, rnd, 12, stem=True)
    basket_front(r, 1, 15, 20)


def punnet(r, x0, x1, rim, bot=BOT):
    """A small chip punnet, low, of thin woven shavings."""
    r.rect(x0, rim, x1, bot, ramp(C.STRAW, 0.6))
    for k in range(int((x1 - x0) / 1.4)):
        r.rect(x0 + k * 1.4, rim, x0 + k * 1.4 + 0.3, bot, ramp(C.STRAW, 0.4))
    r.rect(x0, rim, x1, rim + 0.5, ramp(C.STRAW, 0.85))
    r.rect(x1 - 1.2, rim, x1, bot, ramp(C.STRAW, 0.35))


def g_rasp(r, rnd):
    berries(r, 3.4, 12.6, 24.6, 28.2, RASP, rnd, 18, 1.0)
    punnet(r, 3, 13, 27.6)
    shadow(r, 3, 13)


def p_rasp(r, rnd):
    basket_back(r, 1, 15, 20)
    berries(r, 1.6, 14.4, 15.6, 21, RASP, rnd, 46, 1.0)
    basket_front(r, 1, 15, 20)


def g_blue(r, rnd):
    berries(r, 3.4, 12.6, 24.8, 28.2, BLUEB, rnd, 20, 0.95)
    punnet(r, 3, 13, 27.6)
    shadow(r, 3, 13)


def p_blue(r, rnd):
    basket_back(r, 1, 15, 20)
    berries(r, 1.6, 14.4, 15.6, 21, BLUEB, rnd, 50, 0.95)
    basket_front(r, 1, 15, 20)


def g_carrots(r, rnd):
    for x, y, a in ((4.0, 26.0, 0.35), (4.6, 28.2, 0.12), (5.0, 30.0, -0.05)):
        carrot(r, x, y, a, 8.5, rnd)
    shadow(r, 2, 14)


def p_carrots(r, rnd):
    basket_back(r, 1, 15, 21)
    for x, a in ((3.5, -1.9), (6.5, -1.65), (9.5, -1.5), (12.5, -1.25), (5.0, -1.75), (11.0, -1.4)):
        carrot(r, x, 20.0, a + math.pi, 6, rnd)
    basket_front(r, 1, 15, 21)


def g_wheat(r, rnd):
    """A small sheaf of wheat lying on its side, tied at the middle."""
    for k in range(14):
        y = 27.4 + rnd.uniform(-2.4, 2.4)
        wheat_ears(r, 0.6, y + 0.8, -0.04 + rnd.uniform(-0.12, 0.12), 8.0, rnd)
    r.rect(4.6, 24.4, 6.2, 30.6, ramp(C.STRAW, 0.28))
    r.rect(4.6, 24.4, 5.1, 30.6, ramp(C.STRAW, 0.6))
    shadow(r, 1, 15)


def p_wheat(r, rnd):
    """A sheaf standing, bound with a twist of its own straw, the ears
    nodding out over the top."""
    r.poly([(4.5, 32), (11.5, 32), (10.0, 18), (6.0, 18)], ramp(C.STRAW, 0.5))
    for k in range(12):
        x = 4.8 + k * 0.6
        r.line([(x, 31.6), (8 + (x - 8) * 0.5, 18)], ramp(C.STRAW, 0.4 + 0.3 * (k % 3 == 0)), 0.3)
    for k in range(13):
        a = -math.pi / 2 + rnd.uniform(-0.9, 0.9)
        wheat_ears(r, 8 + rnd.uniform(-1.2, 1.2), 19, a, rnd.uniform(1.5, 4), rnd)
    r.rect(5.4, 23.6, 10.6, 25.0, ramp(C.STRAW, 0.25))
    r.rect(5.4, 23.6, 10.6, 24.0, ramp(C.STRAW, 0.7))


def g_flour(r, rnd):
    """A small linen bag of flour, tied at the neck, flour dusting it."""
    r.poly([(4, 31.8), (12, 31.8), (12.6, 25), (10.2, 22.4), (5.8, 22.4), (3.4, 25)], ramp(C.LINEN, 0.66))
    r.poly([(9.2, 31.8), (12, 31.8), (12.6, 25), (10.2, 22.4), (9.0, 22.4)], ramp(C.LINEN, 0.46))
    r.poly([(6.2, 22.6), (9.8, 22.6), (9.0, 20.4), (7.0, 20.4)], ramp(C.LINEN, 0.6))
    r.rect(6.0, 21.8, 10.0, 22.6, ramp(C.STRAW, 0.3))
    r.speckle(3.6, 23, 12.4, 31.6, 14, CREAM, (0.85, 1.0), rnd, (0.3, 0.5))
    shadow(r, 3, 13)


def p_flour(r, rnd):
    sack(r, 1, 15, 15, C.LINEN)
    r.ellipse(3, 14.6, 13, 18.6, ramp(CREAM, 0.88))
    r.ellipse(5, 13.4, 11, 16.8, ramp(CREAM, 0.98))
    r.line([(10.5, 15.0), (14.5, 11.0)], ramp(C.OAK, 0.6), 0.6)
    r.ellipse(9.4, 14.6, 11.6, 16.2, ramp(C.OAK, 0.5))


def g_salt(r, rnd):
    """A grey stoneware crock of salt, heaped white."""
    r.rect(4, 24, 12, 31.6, ramp(STONEWARE, 0.62))
    r.rect(4, 24, 5.4, 31.6, ramp(STONEWARE, 0.86))
    r.rect(10.4, 24, 12, 31.6, ramp(STONEWARE, 0.4))
    r.rect(4, 26.4, 12, 27.0, ramp(C.INDIGO, 0.6))
    r.ellipse(3.6, 22.8, 12.4, 25.6, ramp(STONEWARE, 0.78))
    r.ellipse(4.6, 21.8, 11.4, 24.6, ramp(CREAM, 0.96))
    r.speckle(5, 22, 11, 24.2, 8, CREAM, (0.7,), rnd, (0.3, 0.4))
    shadow(r, 4, 12)


def p_salt(r, rnd):
    barrel(r, 2, 14, 17, cols=C.OAK)
    r.ellipse(3.4, 15.6, 12.6, 19.0, ramp(CREAM, 0.9))
    r.ellipse(5.0, 14.6, 10.4, 17.4, ramp(CREAM, 1.0))
    r.speckle(4, 15, 12, 18.4, 14, CREAM + STONEWARE, (0.55, 0.65), rnd, (0.3, 0.4))


def cheese_wheel(r, cx, bot, w, h, cut=False):
    x0, x1 = cx - w / 2, cx + w / 2
    top = bot - h
    r.ellipse(x0, bot - 3, x1, bot, ramp(CHEESE, 0.3))
    r.rect(x0, top + 1.5, x1, bot - 1.5, ramp(CHEESE, 0.5))
    r.rect(x0, top + 1.5, x0 + 1.4, bot - 1.5, ramp(CHEESE, 0.7))
    r.rect(x1 - 1.6, top + 1.5, x1, bot - 1.5, ramp(CHEESE, 0.32))
    r.ellipse(x0, top, x1, top + 3, ramp(CHEESE, 0.74))
    r.ellipse(x0 + 1, top + 0.4, x1 - 1, top + 2.4, ramp(CHEESE, 0.82))
    if cut:
        r.poly([(cx, top + 1.5), (x1, top + 1.0), (x1 - 0.6, top + 2.6)], ramp(CHEESE, 0.2))
        r.poly([(cx, top + 1.5), (x1 - 0.6, top + 2.6), (x1 - 0.6, bot - 1.6), (cx, bot - 1.0)], ramp(CHEESE, 0.95))
        r.ellipse(cx + 1.6, top + 3.6, cx + 2.4, top + 4.3, ramp(CHEESE, 0.7))


def g_cheese(r, rnd):
    cheese_wheel(r, 7.6, 31.6, 11, 6, cut=True)
    shadow(r, 2, 13)


def p_cheese(r, rnd):
    crate_back(r, 1, 15, 23)
    cheese_wheel(r, 6.0, 23.4, 9, 5)
    cheese_wheel(r, 10.4, 23.0, 9, 5)
    cheese_wheel(r, 8.0, 19.4, 10, 5)
    crate_front(r, 1, 15, 23)


def jug(r, cx, bot, cols, h=9.0, milk=False):
    """A bellied redware jug with a pinched lip and a handle on the right."""
    top = bot - h
    r.line([(cx + 2.4, top + 2.4), (cx + 4.4, top + 3.4), (cx + 3.6, top + 6.6), (cx + 2.6, top + 7.0)],
           ramp(cols, 0.36), 0.7)
    r.ellipse(cx - 3.6, top + 2.2, cx + 3.6, bot + 0.4, ramp(cols, 0.46))
    r.ellipse(cx - 3.2, top + 2.6, cx + 1.4, bot - 1.6, ramp(cols, 0.64))
    r.ellipse(cx - 2.4, top + 3.4, cx - 0.6, top + 6.2, ramp(cols, 0.9))
    r.rect(cx - 1.8, top + 0.6, cx + 1.8, top + 3.0, ramp(cols, 0.5))
    r.ellipse(cx - 2.2, top - 0.2, cx + 2.2, top + 1.6, ramp(cols, 0.72))
    r.poly([(cx - 2.2, top + 0.6), (cx - 3.4, top + 0.0), (cx - 2.0, top + 1.4)], ramp(cols, 0.7))
    if milk:
        r.ellipse(cx - 1.6, top + 0.2, cx + 1.6, top + 1.3, ramp(CREAM, 0.92))
    else:
        r.ellipse(cx - 1.6, top + 0.2, cx + 1.6, top + 1.3, ramp(cols, 0.1))
    r.rect(cx - 3.0, bot - 0.6, cx + 3.0, bot, ramp(cols, 0.22))


def g_milk(r, rnd):
    jug(r, 7.0, 31.6, C.REDWARE, milk=True)
    shadow(r, 3, 12)


def p_milk(r, rnd):
    """A staved milk pail full to the brim, a dipper hung on its rim."""
    barrel(r, 2, 14, 18, cols=C.OAK)
    r.ellipse(3.0, 18.4, 13.0, 20.2, ramp(CREAM, 0.88))
    r.ellipse(4.0, 18.6, 8.0, 19.4, ramp(CREAM, 1.0))
    r.line([(2.0, 19.0), (8.0, 9.4), (14.0, 19.0)], ramp(C.OAK, 0.6), 0.6)
    r.line([(12.4, 19.0), (14.4, 23.6)], ramp(C.PEWTER, 0.6), 0.5)


def joint(r, cx, bot, w, cooked=False):
    """A joint of meat: the cut face (lean and a rind of fat), a knuckle of
    bone; browned all over when cooked."""
    cols = BREAD if cooked else MEAT
    x0, x1 = cx - w / 2, cx + w / 2
    r.ellipse(x0, bot - 6.4, x1, bot, ramp(cols, 0.4))
    r.ellipse(x0 + 0.8, bot - 6.0, x1 - 2.0, bot - 1.4, ramp(cols, 0.62))
    if not cooked:
        ring(r, x0 + 0.4, bot - 6.2, x1 - 0.4, bot - 0.2, ramp(CREAM, 0.82), 0.6)
        r.ellipse(x0 + 2.2, bot - 4.8, x0 + 4.6, bot - 3.0, ramp(MEAT, 0.86))
    else:
        r.ellipse(x0 + 1.6, bot - 5.6, x0 + 4.2, bot - 4.2, ramp(BREAD, 0.9))
        r.speckle(x0 + 1, bot - 5.6, x1 - 1, bot - 1, 8, BREAD, (0.2,), random.Random(3), (0.3, 0.5))
    r.ellipse(x1 - 2.4, bot - 5.0, x1 + 0.8, bot - 2.6, ramp(CREAM, 0.8))
    r.ellipse(x1 - 1.8, bot - 4.6, x1 + 0.2, bot - 3.4, ramp(CREAM, 0.98))


def g_meat(r, rnd):
    joint(r, 7.4, 31.4, 11)
    shadow(r, 2, 13)


def p_meat(r, rnd):
    crate_back(r, 1, 15, 23)
    joint(r, 6.0, 23.2, 9)
    joint(r, 10.0, 21.6, 9)
    crate_front(r, 1, 15, 23)


def g_fried(r, rnd):
    plate(r, 8, 31.6, 13, C.OAK)
    joint(r, 7.6, 29.4, 8, cooked=True)


def p_fried(r, rnd):
    crate_back(r, 1, 15, 23)
    plate(r, 8, 23.2, 12, C.OAK)
    joint(r, 7.4, 21.2, 7, cooked=True)
    joint(r, 9.8, 19.6, 6, cooked=True)
    crate_front(r, 1, 15, 23)


def loaf(r, cx, bot, w, h):
    x0, x1 = cx - w / 2, cx + w / 2
    r.ellipse(x0, bot - h, x1, bot, ramp(BREAD, 0.4))
    r.ellipse(x0 + 0.4, bot - h, x1 - 1.0, bot - h * 0.35, ramp(BREAD, 0.66))
    r.ellipse(x0 + 1.4, bot - h + 0.4, cx, bot - h * 0.6, ramp(BREAD, 0.86))
    r.line([(cx - w * 0.25, bot - h * 0.85), (cx + w * 0.1, bot - h * 0.45)], ramp(BREAD, 0.3), 0.4)
    r.line([(cx + w * 0.05, bot - h * 0.9), (cx + w * 0.3, bot - h * 0.5)], ramp(BREAD, 0.3), 0.4)
    r.speckle(x0 + 1, bot - h + 0.5, x1 - 1, bot - 1, 4, CREAM, (0.8,), random.Random(5), (0.3, 0.4))


def g_bread(r, rnd):
    loaf(r, 7.6, 31.6, 11, 6.6)
    shadow(r, 2, 13)


def p_bread(r, rnd):
    basket_back(r, 1, 15, 21)
    loaf(r, 5.4, 21.6, 8, 5)
    loaf(r, 10.6, 21.2, 8, 5)
    loaf(r, 8.0, 18.6, 8, 5)
    basket_front(r, 1, 15, 21)


def g_water(r, rnd):
    """A wooden pail of water, its rope bail."""
    barrel(r, 3, 13, 23.4, cols=C.OAK)
    r.ellipse(4.0, 23.8, 12.0, 25.4, ramp(WATER, 0.45))
    r.ellipse(4.6, 23.9, 7.6, 24.6, ramp(WATER, 0.95))
    arc(r, 3.0, 17.0, 13.0, 29.0, 180, 360, ramp(C.STRAW, 0.45), 0.4)
    shadow(r, 3, 13)


def p_water(r, rnd):
    barrel(r, 1, 15, 14, cols=C.OAK)
    r.ellipse(2.2, 14.4, 13.8, 16.4, ramp(WATER, 0.45))
    r.ellipse(3.0, 14.6, 7.0, 15.4, ramp(WATER, 0.95))
    r.line([(11, 15.0), (15.6, 10.4)], ramp(C.OAK, 0.6), 0.5)
    r.ellipse(9.6, 14.6, 12.2, 16.0, ramp(C.OAK, 0.4))


def log_end(r, cx, cy, rad, split=True):
    """A split billet seen end on: bark round its back, the pale split face,
    rings."""
    if split:
        # A quarter-split billet: bark round its back, two split faces.
        r.ellipse(cx - rad, cy - rad, cx + rad, cy + rad, ramp(BARK, 0.36))
        r.poly([(cx - rad + 0.5, cy + rad * 0.3), (cx + rad * 0.2, cy - rad + 0.5), (cx + rad - 0.5, cy + rad * 0.1),
                (cx + rad * 0.1, cy + rad - 0.5)], ramp(C.STRAW, 0.66))
        r.line([(cx - rad * 0.3, cy + rad * 0.1), (cx + rad * 0.5, cy - rad * 0.2)], ramp(C.STRAW, 0.4), 0.25)
        r.ellipse(cx - rad * 0.9, cy - rad * 0.95, cx - rad * 0.1, cy - rad * 0.3, ramp(BARK, 0.6))
    else:
        r.ellipse(cx - rad, cy - rad, cx + rad, cy + rad, ramp(BARK, 0.36))
        r.ellipse(cx - rad + 0.5, cy - rad + 0.5, cx + rad - 0.5, cy + rad - 0.5, ramp(C.STRAW, 0.6))
        ring(r, cx - rad * 0.55, cy - rad * 0.55, cx + rad * 0.55, cy + rad * 0.55, ramp(C.STRAW, 0.4), 0.25)


def billet(r, x0, x1, y, h, rnd):
    """A split billet lying lengthwise, bark on top, the split face front."""
    r.rect(x0, y, x1, y + h, ramp(C.STRAW, 0.56))
    r.rect(x0, y, x1, y + h * 0.4, ramp(BARK, 0.42))
    r.speckle(x0, y, x1, y + h * 0.4, (x1 - x0), BARK, (0.15, 0.7), rnd, (0.3, 0.6))
    for _ in range(3):
        gy = rnd.uniform(y + h * 0.5, y + h - 0.4)
        r.rect(x0 + 0.5, gy, x1 - 0.5, gy + 0.25, ramp(C.STRAW, 0.38))
    r.rect(x1 - 1.2, y, x1, y + h, ramp(C.STRAW, 0.74))
    r.rect(x0, y + h - 0.4, x1, y + h, ramp(C.STRAW, 0.26))


def g_firewood(r, rnd):
    billet(r, 1.6, 13.4, 28.0, 3.8, rnd)
    billet(r, 2.6, 14.2, 24.4, 3.6, rnd)
    shadow(r, 1, 14)


def p_firewood(r, rnd):
    """Billets stacked end on, a little rick."""
    rows = ((29.0, (3.0, 8.0, 13.0)), (24.6, (5.5, 10.5)), (20.2, (3.0, 8.0, 13.0)), (15.8, (5.5, 10.5)))
    for y, xs in rows:
        for x in xs:
            log_end(r, x + rnd.uniform(-0.3, 0.3), y, 2.5, split=rnd.random() < 0.5)


def herb_bunch(r, x0, y, ln, rnd, tone=0.5, flowers=None):
    """A bunch of herbs lying on its side, tied at the stems on the left."""
    for k in range(9):
        a = rnd.uniform(-0.45, 0.45)
        l2 = ln * rnd.uniform(0.7, 1.0)
        r.line([(x0, y), (x0 + math.cos(a) * l2, y + math.sin(a) * l2 * 0.7)], ramp(LEAF, tone + rnd.uniform(-0.25, 0.2)), 0.6)
    for k in range(6):
        lx, ly = x0 + rnd.uniform(ln * 0.3, ln), y + rnd.uniform(-2.2, 2.2)
        r.ellipse(lx - 1.0, ly - 0.6, lx + 1.0, ly + 0.6, ramp(LEAF, tone + rnd.uniform(-0.1, 0.25)))
    if flowers:
        for k in range(5):
            fx, fy = x0 + ln * rnd.uniform(0.6, 1.0), y + rnd.uniform(-2, 2)
            r.ellipse(fx - 0.5, fy - 0.5, fx + 0.5, fy + 0.5, ramp(flowers, 0.8))
    r.rect(x0 - 0.6, y - 1.0, x0 + 1.0, y + 1.0, ramp(C.STRAW, 0.4))
    r.line([(x0 - 3.0, y - 0.4), (x0, y)], ramp(LEAF, 0.3), 0.6)


def g_herbs(r, rnd):
    herb_bunch(r, 3.4, 27.0, 11, rnd, 0.5, CREAM)
    herb_bunch(r, 4.0, 29.0, 10, rnd, 0.38, None)
    shadow(r, 1, 14)


def p_herbs(r, rnd):
    basket_back(r, 1, 15, 21)
    for k in range(4):
        a = -math.pi / 2 + (k - 1.5) * 0.35
        x, y = 4.4 + k * 2.4, 22
        for j in range(7):
            b = a + rnd.uniform(-0.3, 0.3)
            ln = rnd.uniform(5, 8)
            r.line([(x, y), (x + math.cos(b) * ln, y + math.sin(b) * ln)], ramp(LEAF, rnd.uniform(0.35, 0.85)), 0.6)
        r.ellipse(x - 1.6 + math.cos(a) * 6, y - 1.6 + math.sin(a) * 6, x + 1.6 + math.cos(a) * 6, y + 1.6 + math.sin(a) * 6,
                  ramp(LEAF, 0.6))
    basket_front(r, 1, 15, 21)


def hank(r, x0, x1, y, cols, rnd):
    """A hank of dyed wool yarn, twisted and doubled over: strands along it,
    the twist's ridges."""
    r.ellipse(x0, y - 2.6, x1, y + 2.2, ramp(cols, 0.36))
    r.ellipse(x0 + 0.4, y - 2.4, x1 - 1.4, y + 0.6, ramp(cols, 0.62))
    for k in range(int((x1 - x0) / 1.3)):
        sx = x0 + 1 + k * 1.3
        r.line([(sx, y - 2.2), (sx + 1.0, y + 1.6)], ramp(cols, 0.82 if k % 2 else 0.3), 0.3)
    r.ellipse(x1 - 2.6, y - 1.4, x1 + 0.4, y + 2.6, ramp(cols, 0.5))


def dye_jar(r, cx, bot, cols, rnd):
    """A stoneware jar of dye, tied over with a cloth, a hank of wool dyed
    in it lying over its shoulder: the hank tells the colour."""
    r.ellipse(cx - 3.0, bot - 8, cx + 3.0, bot, ramp(STONEWARE, 0.55))
    r.ellipse(cx - 2.6, bot - 7.4, cx + 0.6, bot - 2.0, ramp(STONEWARE, 0.76))
    r.rect(cx - 2.0, bot - 9.6, cx + 2.0, bot - 7.2, ramp(STONEWARE, 0.5))
    r.ellipse(cx - 2.6, bot - 10.6, cx + 2.6, bot - 8.6, ramp(C.LINEN, 0.7))
    r.rect(cx - 2.2, bot - 9.0, cx + 2.2, bot - 8.4, ramp(C.STRAW, 0.3))
    r.speckle(cx - 1.8, bot - 10.4, cx + 1.8, bot - 8.8, 4, cols, (0.4,), rnd, (0.3, 0.5))
    hank(r, cx - 1.0, cx + 7.4, bot - 2.6, cols, rnd)


def g_dye(cols):
    def paint(r, rnd):
        dye_jar(r, 5.6, 31.6, cols, rnd)
        shadow(r, 2, 14)
    return paint


def p_dye(cols):
    def paint(r, rnd):
        crate_back(r, 1, 15, 23)
        hank(r, 2.0, 10.0, 22.4, cols, rnd)
        hank(r, 6.0, 14.0, 21.0, cols, rnd)
        hank(r, 3.6, 11.6, 19.4, cols, rnd)
        crate_front(r, 1, 15, 23)
    return paint


def g_ale(r, rnd):
    tankard(r, 7.0, 31.6, foam=True)
    shadow(r, 3, 13)


def cask(r, rnd, empty=False):
    """An ale cask on its stand, lying on its side, a spigot in its head."""
    r.rect(1.4, 28, 3.0, 32, ramp(C.OAK, 0.4))
    r.rect(13.0, 28, 14.6, 32, ramp(C.OAK, 0.3))
    r.rect(1.0, 27.4, 15.0, 28.6, ramp(C.OAK, 0.56))
    r.ellipse(1.0, 15.0, 15.0, 29.0, ramp(C.OAK, 0.44))
    for k in range(6):
        y = 16.0 + k * 2.1
        r.rect(2.0, y, 14.0, y + 1.8, ramp(C.OAK, 0.62 - abs(k - 1.5) * 0.06))
    for hx in (3.4, 12.0):
        r.rect(hx, 15.4, hx + 0.9, 28.6, ramp(C.IRON, 0.36))
    r.ellipse(5.4, 18.4, 10.6, 25.4, ramp(C.OAK, 0.34))
    r.rect(7.4, 22.0, 8.8, 23.4, ramp(C.OAK, 0.6))
    r.rect(7.8, 23.4, 8.4, 25.4, ramp(BRASS, 0.6))


def p_ale(r, rnd):
    cask(r, rnd)


def bottle(r, cx, bot, cols, label=True):
    """A small green glass bottle, corked, a paper label tied at its neck."""
    r.ellipse(cx - 2.4, bot - 6.4, cx + 2.4, bot, ramp(cols, 0.4))
    r.ellipse(cx - 2.0, bot - 6.0, cx + 0.4, bot - 1.2, ramp(cols, 0.62))
    r.rect(cx - 1.2, bot - 9.2, cx + 1.2, bot - 5.6, ramp(cols, 0.5))
    r.rect(cx - 1.2, bot - 9.2, cx - 0.5, bot - 5.6, ramp(cols, 0.85))
    r.rect(cx - 1.2, bot - 10.6, cx + 1.2, bot - 9.0, ramp(C.OAK, 0.62))
    r.rect(cx - 1.4, bot - 5.0, cx - 0.9, bot - 2.2, ramp(cols, 0.95))
    if label:
        r.line([(cx + 0.9, bot - 8.0), (cx + 2.4, bot - 6.8)], ramp(C.STRAW, 0.3), 0.25)
        r.rect(cx + 1.6, bot - 7.0, cx + 3.6, bot - 4.8, ramp(CREAM, 0.86))
        r.line([(cx + 2.0, bot - 6.2), (cx + 3.2, bot - 6.2)], ramp(C.OAK, 0.3), 0.2)


def g_draught(r, rnd):
    bottle(r, 7.0, 31.6, GLASSG)
    shadow(r, 4, 11)


def heap_of(good):
    """A pile made of the good itself, heaped in a crate: two of it on top
    of the crate's open mouth (see stall_goods.heap)."""
    def paint(r, rnd):
        heap(r, good, rnd)
    return paint


def p_draught(r, rnd):
    crate_back(r, 1, 15, 23)
    for x in (4.0, 8.0, 12.0):
        bottle(r, x, 23.6, GLASSG, label=False)
    crate_front(r, 1, 15, 23)


def chain(r, pts, cols):
    for (x0, y0), (x1, y1) in zip(pts, pts[1:]):
        n = max(1, int(math.hypot(x1 - x0, y1 - y0) / 0.7))
        for k in range(n):
            x, y = x0 + (x1 - x0) * k / n, y0 + (y1 - y0) * k / n
            r.rect(x, y, x + 0.4, y + 0.4, ramp(cols, 0.6 if k % 2 else 0.9))


def g_locket(r, rnd):
    """A silver locket, closed, on a fine chain coiled beside it."""
    chain(r, [(4, 29), (2.6, 27), (4, 25), (8, 24.4), (11.6, 25.2), (12.4, 27.4), (10.6, 28.6)], C.PEWTER)
    r.ellipse(4.4, 24.8, 11.4, 31.6, ramp(C.PEWTER, 0.42))
    r.ellipse(4.8, 25.2, 10.4, 30.4, ramp(C.PEWTER, 0.74))
    r.ellipse(5.6, 25.8, 7.4, 27.6, ramp(C.PEWTER, 1.0))
    r.rect(7.4, 23.6, 8.4, 25.2, ramp(C.PEWTER, 0.6))
    ring(r, 5.8, 26.6, 9.6, 30.2, ramp(C.PEWTER, 0.4), 0.3)


def g_charm(r, rnd):
    """A charm carved from whalebone in a whale's tail, on a cord."""
    r.line([(4, 24.8), (8, 26.4), (12, 24.8)], ramp(C.OAK, 0.4), 0.35)
    r.poly([(8, 25.6), (6.6, 28.4), (1.8, 30.0), (4.0, 31.8), (8, 30.2), (12.0, 31.8), (14.2, 30.0), (9.4, 28.4)],
           ramp(CREAM, 0.7))
    r.poly([(8, 25.6), (6.6, 28.4), (1.8, 30.0), (4.0, 30.8), (7.6, 29.4)], ramp(CREAM, 0.95))
    r.line([(8, 27.0), (8, 30.2)], ramp(CREAM, 0.38), 0.3)


def g_spectacles(r, rnd):
    """Iron-rimmed spectacles, folded, the lenses catching the light."""
    for cx in (5.0, 11.0):
        r.ellipse(cx - 2.4, 26.6, cx + 2.4, 31.0, ramp(GLASSG, 0.75))
        r.ellipse(cx - 1.8, 27.0, cx - 0.2, 28.6, ramp(CREAM, 0.98))
        ring(r, cx - 2.4, 26.6, cx + 2.4, 31.0, ramp(C.IRON, 0.35), 0.45)
    arc(r, 6.8, 27.4, 9.2, 29.4, 180, 360, ramp(C.IRON, 0.4), 0.4)
    r.line([(2.6, 28.4), (1.0, 26.6)], ramp(C.IRON, 0.4), 0.35)
    r.line([(13.4, 28.4), (15.0, 26.6)], ramp(C.IRON, 0.4), 0.35)


def g_axe(r, rnd):
    """A felling axe lying on its side: the hickory helve, the iron head
    with its bright bit."""
    r.line([(0.8, 31.0), (11.0, 23.8)], ramp(C.OAK, 0.58), 1.5)
    r.line([(0.8, 30.4), (11.0, 23.2)], ramp(C.OAK, 0.86), 0.45)
    r.poly([(8.4, 21.4), (12.4, 19.4), (15.8, 23.6), (15.0, 29.6), (11.6, 26.2)], ramp(C.IRON, 0.44))
    r.poly([(14.2, 22.0), (15.8, 23.6), (15.0, 29.6), (13.8, 27.6)], ramp(C.IRON, 0.92))
    r.poly([(8.4, 21.4), (12.4, 19.4), (12.9, 20.0), (8.9, 22.0)], ramp(C.IRON, 0.7))


def g_hammer(r, rnd):
    r.line([(1.0, 31.0), (11.0, 25.6)], ramp(C.OAK, 0.58), 1.4)
    r.line([(1.0, 30.4), (11.0, 25.0)], ramp(C.OAK, 0.86), 0.4)
    r.poly([(8.4, 21.6), (12.2, 19.8), (15.4, 26.4), (11.8, 28.2)], ramp(C.IRON, 0.42))
    r.poly([(8.4, 21.6), (12.2, 19.8), (12.7, 20.8), (8.9, 22.6)], ramp(C.IRON, 0.8))
    r.poly([(11.8, 28.2), (15.4, 26.4), (15.8, 27.4), (12.2, 29.2)], ramp(C.IRON, 0.18))


def g_shovel(r, rnd):
    """A wooden spade shod with an iron edge, its D handle at the left."""
    r.line([(3.4, 26.4), (9.6, 27.4)], ramp(C.OAK, 0.6), 1.3)
    r.line([(3.4, 26.0), (9.6, 27.0)], ramp(C.OAK, 0.86), 0.4)
    arc(r, 0.4, 24.2, 4.4, 29.0, 90, 270, ramp(C.OAK, 0.6), 0.9)
    r.poly([(9.4, 25.2), (14.6, 25.6), (15.6, 30.4), (9.6, 30.4)], ramp(C.OAK, 0.62))
    r.poly([(9.4, 25.2), (11.0, 25.3), (11.2, 30.4), (9.6, 30.4)], ramp(C.OAK, 0.82))
    r.poly([(14.0, 25.6), (14.6, 25.6), (15.6, 30.4), (14.8, 30.4)], ramp(C.IRON, 0.5))
    r.rect(9.6, 29.8, 15.6, 30.6, ramp(C.IRON, 0.4))


def nail(r, x, y, ln, ang=0.0):
    dx, dy = math.cos(ang), math.sin(ang)
    r.line([(x, y), (x + dx * ln, y + dy * ln)], ramp(C.IRON, 0.5), 0.8)
    r.line([(x, y - 0.25), (x + dx * ln * 0.6, y + dy * ln * 0.6 - 0.25)], ramp(C.IRON, 0.85), 0.3)
    r.rect(x - 1.0, y - 1.0, x + 0.8, y + 0.8, ramp(C.IRON, 0.32))
    r.rect(x - 1.0, y - 1.0, x - 0.2, y - 0.4, ramp(C.IRON, 0.7))


def g_nail(r, rnd):
    for k, (x, y, a) in enumerate(((2.0, 27.6, 0.1), (3.0, 30.0, -0.05), (6.6, 25.8, 0.3), (8.0, 29.0, 0.0))):
        nail(r, x, y, 6.4, a)


def p_nail(r, rnd):
    """A small keg of nails, open."""
    barrel(r, 2, 14, 18)
    r.ellipse(3.0, 17.6, 13.0, 20.6, ramp(C.IRON, 0.3))
    for _ in range(12):
        x, y = rnd.uniform(3.6, 12.4), rnd.uniform(17.4, 19.6)
        nail(r, x, y, rnd.uniform(2, 3.4), rnd.uniform(-2.5, -0.6))


def horseshoe(r, cx, bot, w=7.6):
    x0, x1 = cx - w / 2, cx + w / 2
    arc(r, x0, bot - w * 0.95, x1, bot + w * 0.4, 160, 380, ramp(C.IRON, 0.4), 1.2)
    arc(r, x0, bot - w * 0.95, x1, bot + w * 0.4, 190, 290, ramp(C.IRON, 0.8), 0.35)
    for a in (205, 235, 305, 335):
        ra = math.radians(a)
        hx = cx + math.cos(ra) * (w / 2 - 0.6)
        hy = bot - w * 0.275 + math.sin(ra) * (w * 0.675 - 0.6)
        r.rect(hx - 0.25, hy - 0.25, hx + 0.25, hy + 0.25, ramp(C.IRON, 0.05))


def g_horseshoe(r, rnd):
    horseshoe(r, 5.6, 30.4)
    horseshoe(r, 10.4, 31.2)


def skillet(r, cx, bot):
    """An iron spider: a skillet on three short legs, its long handle out to
    the right."""
    r.line([(cx + 3.6, bot - 4.0), (cx + 8.4, bot - 6.4)], ramp(C.IRON, 0.4), 0.7)
    r.line([(cx + 3.6, bot - 4.4), (cx + 8.4, bot - 6.8)], ramp(C.IRON, 0.7), 0.25)
    for lx in (cx - 3.4, cx + 2.6):
        r.rect(lx, bot - 2.0, lx + 0.8, bot, ramp(C.IRON, 0.2))
    r.ellipse(cx - 4.8, bot - 5.6, cx + 4.8, bot - 0.8, ramp(C.IRON, 0.26))
    r.ellipse(cx - 4.8, bot - 6.0, cx + 4.8, bot - 2.4, ramp(C.IRON, 0.5))
    r.ellipse(cx - 4.0, bot - 5.6, cx + 4.0, bot - 2.8, ramp(C.IRON, 0.14))
    r.ellipse(cx - 3.4, bot - 5.4, cx - 0.4, bot - 4.0, ramp(C.IRON, 0.36))


def g_skillet(r, rnd):
    skillet(r, 6.0, 31.6)


def iron_bar(r, x0, x1, y, h=1.8):
    r.rect(x0, y, x1, y + h, ramp(C.IRON, 0.38))
    r.rect(x0, y, x1, y + 0.5, ramp(C.IRON, 0.72))
    r.rect(x1 - 0.8, y, x1, y + h, ramp(C.IRON, 0.56))
    r.rect(x0, y + h - 0.4, x1, y + h, ramp(C.IRON, 0.14))


def g_iron(r, rnd):
    iron_bar(r, 1.0, 14.0, 29.4)
    iron_bar(r, 2.4, 15.0, 26.8)


def p_iron(r, rnd):
    for k in range(6):
        iron_bar(r, 0.6 + (k % 2) * 0.8, 15.0 - ((k + 1) % 2) * 0.6, 29.8 - k * 2.0, 2.0)
    r.rect(4.0, 18.6, 5.2, 31.8, ramp(C.OAK, 0.36))
    r.rect(11.0, 18.6, 12.2, 31.8, ramp(C.OAK, 0.36))


def g_whetstone(r, rnd):
    r.poly([(2.4, 28.0), (13.0, 26.8), (14.0, 29.6), (3.0, 31.0)], ramp(STONEWARE, 0.5))
    r.poly([(2.4, 28.0), (13.0, 26.8), (13.4, 27.8), (2.6, 29.0)], ramp(STONEWARE, 0.86))
    r.speckle(3, 28, 13, 30.6, 10, STONEWARE, (0.3, 0.7), rnd, (0.3, 0.4))


def spool(r, cx, bot, cols):
    r.rect(cx - 2.6, bot - 1.2, cx + 2.6, bot, ramp(C.OAK, 0.5))
    r.rect(cx - 2.6, bot - 8.0, cx + 2.6, bot - 6.8, ramp(C.OAK, 0.7))
    r.rect(cx - 2.0, bot - 6.8, cx + 2.0, bot - 1.2, ramp(cols, 0.62))
    for k in range(5):
        y = bot - 6.6 + k * 1.1
        r.rect(cx - 2.0, y, cx + 2.0, y + 0.3, ramp(cols, 0.4))
    r.rect(cx - 2.0, bot - 6.8, cx - 1.2, bot - 1.2, ramp(cols, 0.86))


def g_thread(r, rnd):
    spool(r, 5.2, 31.6, C.LINEN)
    hank(r, 7.4, 14.6, 29.6, C.LINEN, rnd)


def bolt(r, x0, x1, y, h, cols):
    """A folded bolt of cloth: the folds' edges toward us."""
    r.rect(x0, y, x1, y + h, ramp(cols, 0.62))
    n = max(2, int(h / 1.3))
    for k in range(n):
        fy = y + h * k / n
        r.rect(x0, fy, x1, fy + 0.3, ramp(cols, 0.84))
        r.rect(x0, fy + h / n - 0.3, x1, fy + h / n, ramp(cols, 0.36))
    r.ellipse(x1 - 1.6, y, x1 + 0.6, y + h, ramp(cols, 0.5))
    r.rect(x0, y, x0 + 0.6, y + h, ramp(cols, 0.9))


def g_linen(r, rnd):
    bolt(r, 2.0, 13.0, 25.6, 6.0, C.LINEN)


def p_linen(r, rnd):
    bolt(r, 1.0, 14.0, 26.0, 6.0, C.LINEN)
    bolt(r, 2.0, 14.6, 20.2, 5.8, C.LINEN)
    bolt(r, 1.4, 13.6, 14.8, 5.4, C.LINEN)


# ---------------------------------------------------------------- clothes

def glove(r, x, y, cols, flip=False):
    s = -1 if flip else 1
    r.poly([(x, y), (x + s * 4.6, y - 0.4), (x + s * 5.4, y + 4.4), (x + s * 0.4, y + 4.6)], ramp(cols, 0.55))
    for k in range(4):
        fx = x + s * (1.0 + k * 1.1)
        r.rect(min(fx, fx + s * 0.9), y - 2.8 + abs(k - 1.5) * 0.6, max(fx, fx + s * 0.9), y + 0.2, ramp(cols, 0.62))
    r.poly([(x + s * 4.6, y + 0.6), (x + s * 6.6, y - 1.0), (x + s * 7.0, y + 0.2), (x + s * 5.2, y + 2.0)], ramp(cols, 0.5))
    r.rect(min(x, x + s * 5.4), y + 3.4, max(x, x + s * 5.4), y + 4.6, ramp(cols, 0.3))


def g_gloves(r, rnd):
    glove(r, 1.0, 26.6, LEATHER)
    glove(r, 15.0, 27.2, LEATHER, flip=True)


def g_suspenders(r, rnd):
    """A pair of leather braces, crossed, their buckles."""
    r.line([(2, 25), (14, 31)], ramp(LEATHER, 0.56), 1.0)
    r.line([(2, 31), (14, 25)], ramp(LEATHER, 0.48), 1.0)
    r.line([(2, 24.6), (14, 30.6)], ramp(LEATHER, 0.8), 0.25)
    for bx, by in ((3.6, 25.4), (11.6, 25.6)):
        r.rect(bx, by, bx + 1.6, by + 1.4, ramp(BRASS, 0.7))
        r.rect(bx + 0.4, by + 0.4, bx + 1.2, by + 1.0, ramp(LEATHER, 0.3))


def garment(r, outline, cols, rnd, folds=(), tone=0.55, trim=None):
    """A garment laid flat (a waistcoat, a gown, a cloak): its outline in
    cols, lit on the left, folds down it, an edge of trim."""
    r.poly(outline, ramp(cols, tone))
    xs = [p[0] for p in outline]
    cx = (min(xs) + max(xs)) / 2
    left = [p for p in outline if p[0] <= cx]
    r.poly(left + [(cx - 1.0, max(p[1] for p in outline)), (cx - 1.0, min(p[1] for p in outline))], ramp(cols, tone + 0.14))
    for a, b in folds:
        r.line([a, b], ramp(cols, tone - 0.22), 0.35)
        r.line([(a[0] - 0.4, a[1]), (b[0] - 0.4, b[1])], ramp(cols, tone + 0.2), 0.25)
    if trim:
        cols2, pts = trim
        r.line(pts, ramp(cols2, 0.8), 0.5)


def g_vest(r, rnd):
    """A wool waistcoat, buttoned down its front."""
    garment(r, [(3.4, 20.6), (6.4, 20.0), (8, 23.6), (9.6, 20.0), (12.6, 20.6), (13.2, 30.4), (8.8, 31.6), (8, 30.6),
                (7.2, 31.6), (2.8, 30.4)], WOOL, rnd, folds=[((5, 24), (4.6, 29)), ((11, 24), (11.4, 29))])
    for k in range(5):
        r.ellipse(7.6, 24 + k * 1.4, 8.4, 24.8 + k * 1.4, ramp(BRASS, 0.75))


def g_scarf(r, rnd):
    """A wool neckcloth folded, its fringed ends hanging."""
    r.poly([(2, 24), (14, 24), (14, 28), (2, 28)], ramp(BRICK, 0.5))
    r.rect(2, 24, 14, 24.6, ramp(BRICK, 0.8))
    for k in range(5):
        r.rect(2 + k * 2.6, 24.6, 2.6 + k * 2.6, 28, ramp(BRICK, 0.3))
    r.poly([(9.6, 27.6), (13.4, 27.6), (13.0, 31.0), (10.0, 31.0)], ramp(BRICK, 0.42))
    for k in range(4):
        r.line([(10.2 + k * 0.9, 31.0), (10.2 + k * 0.9, 32.0)], ramp(BRICK, 0.6), 0.4)


def stocking(r, x, y, cols, ln=11, knee=False):
    """A knitted stocking lying flat, the foot turned right; a garter band
    at the top of a knee stocking."""
    r.rect(x, y, x + 3.2, y + ln - 2.6, ramp(cols, 0.58))
    r.rect(x, y, x + 1.0, y + ln - 2.6, ramp(cols, 0.78))
    r.poly([(x, y + ln - 2.8), (x + 3.2, y + ln - 2.8), (x + 6.4, y + ln - 1.6), (x + 6.4, y + ln), (x, y + ln)],
           ramp(cols, 0.5))
    for k in range(int(ln / 1.2)):
        r.rect(x + 0.3, y + 0.5 + k * 1.2, x + 3.0, y + 0.7 + k * 1.2, ramp(cols, 0.4))
    if knee:
        r.rect(x - 0.2, y, x + 3.4, y + 1.2, ramp(C.MADDER, 0.6))


def g_knee_socks(r, rnd):
    stocking(r, 2.6, 21.4, WOOL, 10, knee=True)
    stocking(r, 7.4, 21.0, WOOL, 10, knee=True)


def g_stockings(r, rnd):
    stocking(r, 2.6, 19.6, C.LINEN, 12)
    stocking(r, 7.4, 19.0, C.LINEN, 12)


def boot(r, x, bot, cols, cuff=False):
    """A tall leather boot seen side on, toe to the right."""
    r.rect(x, bot - 11.0, x + 4.2, bot - 2.0, ramp(cols, 0.5))
    r.rect(x, bot - 11.0, x + 1.2, bot - 2.0, ramp(cols, 0.72))
    r.poly([(x, bot - 3.4), (x + 4.2, bot - 3.4), (x + 7.6, bot - 2.0), (x + 7.8, bot), (x, bot)], ramp(cols, 0.46))
    r.rect(x - 0.2, bot - 0.8, x + 7.8, bot, ramp(cols, 0.18))
    r.rect(x + 0.2, bot - 3.0, x + 2.2, bot - 0.8, ramp(cols, 0.3))
    if cuff:
        r.poly([(x - 0.8, bot - 11.6), (x + 5.0, bot - 11.6), (x + 4.6, bot - 8.4), (x - 0.4, bot - 8.4)], ramp(cols, 0.66))
        r.rect(x - 0.8, bot - 11.6, x + 5.0, bot - 11.0, ramp(cols, 0.88))


def g_boots(r, rnd):
    boot(r, 1.6, 31.4, LEATHER)
    boot(r, 6.4, 31.8, LEATHER)


def g_cuffed_boots(r, rnd):
    boot(r, 1.6, 31.4, LEATHER, cuff=True)
    boot(r, 6.4, 31.8, LEATHER, cuff=True)


def g_skirt(r, rnd):
    """A petticoat laid flat, gathered at its band, a frill at the hem."""
    garment(r, [(5, 20.4), (11, 20.4), (14.4, 30), (1.6, 30)], C.INDIGO, rnd,
            folds=[((6.4, 21.4), (4.6, 29.6)), ((8.4, 21.4), (8.6, 29.6)), ((10.2, 21.4), (11.8, 29.6))])
    r.rect(4.8, 20.0, 11.2, 21.2, ramp(C.LINEN, 0.45))
    for k in range(8):
        r.ellipse(1.4 + k * 1.7, 29.4, 3.4 + k * 1.7, 31.4, ramp(C.LINEN, 0.5 if k % 2 else 0.4))


def g_dress(r, rnd):
    """A gown with a frilled hem and cuffs, laid flat."""
    garment(r, [(6, 18.6), (10, 18.6), (11, 20), (14.6, 22.4), (13.6, 24.4), (11.2, 23), (14.4, 30.2), (1.6, 30.2),
                (4.8, 23), (2.4, 24.4), (1.4, 22.4), (5, 20)], BRICK, rnd,
            folds=[((7, 23), (5.4, 29.6)), ((9, 23), (10.6, 29.6))])
    r.rect(6.0, 18.4, 10.0, 19.4, ramp(C.LINEN, 0.55))
    for k in range(8):
        r.ellipse(1.4 + k * 1.7, 29.6, 3.4 + k * 1.7, 31.6, ramp(C.LINEN, 0.5 if k % 2 else 0.4))


def g_long_dress(r, rnd):
    garment(r, [(6, 17.6), (10, 17.6), (11, 19), (14.4, 21.6), (13.4, 23.6), (11.2, 22), (13.6, 31.4), (2.4, 31.4),
                (4.8, 22), (2.6, 23.6), (1.6, 21.6), (5, 19)], OLIVE, rnd,
            folds=[((7, 22), (5.6, 31)), ((9, 22), (10.4, 31))])
    r.rect(6.0, 17.4, 10.0, 18.4, ramp(C.LINEN, 0.55))
    r.rect(4.8, 22.0, 11.2, 22.8, ramp(OLIVE, 0.25))


def g_shirt(r, rnd):
    """A linen shirt folded: the body, a sleeve folded across, the collar
    band."""
    garment(r, [(2.4, 22), (13.6, 22), (13.6, 31.2), (2.4, 31.2)], C.LINEN, rnd, folds=[((8, 23), (8, 30.6))], tone=0.62)
    r.poly([(2.4, 25.4), (13.6, 23.6), (13.6, 26.0), (2.4, 27.8)], ramp(C.LINEN, 0.52))
    r.rect(5.4, 21.0, 10.6, 22.6, ramp(C.LINEN, 0.85))
    r.poly([(6.6, 22.6), (8.0, 24.4), (9.4, 22.6)], ramp(C.LINEN, 0.3))


def g_cloak(r, rnd, cols=OLIVE, hood=True, mantle=False):
    """A wool cloak laid out: its hood at the top (or a mantle's short cape
    over the shoulders), the fall widening to the hem."""
    garment(r, [(6.2, 19.6), (9.8, 19.6), (14.6, 31.2), (1.4, 31.2)], cols, rnd,
            folds=[((7.2, 21), (4.4, 30.8)), ((8.8, 21), (11.6, 30.8)), ((8, 22), (8, 30.8))], tone=0.5)
    if hood:
        r.ellipse(4.8, 16.4, 11.2, 22.2, ramp(cols, 0.42))
        r.ellipse(6.0, 17.6, 10.0, 21.4, ramp(cols, 0.16))
    if mantle:
        r.poly([(5.0, 20.0), (11.0, 20.0), (13.4, 25.2), (2.6, 25.2)], ramp(cols, 0.64))
        r.rect(2.6, 24.6, 13.4, 25.2, ramp(cols, 0.3))
    r.rect(7.0, 19.4, 9.0, 20.4, ramp(BRASS, 0.7))


def g_mantle(r, rnd):
    """A short shoulder mantle: a cape alone."""
    garment(r, [(5.0, 21.6), (11.0, 21.6), (15.0, 30.6), (1.0, 30.6)], WOOL, rnd,
            folds=[((6.4, 23), (3.6, 30.2)), ((9.6, 23), (12.4, 30.2))], tone=0.56)
    r.rect(5.0, 21.0, 11.0, 22.4, ramp(WOOL, 0.8))
    r.rect(7.4, 21.6, 8.6, 22.8, ramp(BRASS, 0.7))


def g_mantled_cloak(r, rnd):
    g_cloak(r, rnd, C.INDIGO, hood=False, mantle=True)


def hat(r, cx, bot, cols, crown_h=4.6, brim=7.0, band=None, straw=False):
    """A broad-brimmed hat seen from the front and a little above: the brim
    an oval, the crown standing on it, a band."""
    r.ellipse(cx - brim, bot - 3.4, cx + brim, bot, ramp(cols, 0.36))
    r.ellipse(cx - brim, bot - 3.8, cx + brim, bot - 0.6, ramp(cols, 0.56))
    r.rect(cx - 3.6, bot - 2.2 - crown_h, cx + 3.6, bot - 2.2, ramp(cols, 0.5))
    r.rect(cx - 3.6, bot - 2.2 - crown_h, cx - 2.0, bot - 2.2, ramp(cols, 0.72))
    r.ellipse(cx - 3.6, bot - 3.4 - crown_h, cx + 3.6, bot - 1.2 - crown_h, ramp(cols, 0.66))
    if band:
        r.rect(cx - 3.6, bot - 3.6, cx + 3.6, bot - 2.4, ramp(band, 0.4))
    if straw:
        for k in range(5):
            r.line([(cx - brim + 1 + k * 3, bot - 2.8), (cx - brim + 2 + k * 3, bot - 1.0)], ramp(cols, 0.35), 0.25)
        for k in range(3):
            r.rect(cx - 3.6, bot - 3.4 - k * 1.4, cx + 3.6, bot - 3.1 - k * 1.4, ramp(cols, 0.35))


def g_brimmed_hat(r, rnd):
    hat(r, 8, 31.4, WOOL, 4.6, 7.0, band=C.IRON)


def g_felt_hat(r, rnd):
    hat(r, 8, 31.4, C.IRON, 6.4, 6.0, band=BRASS)


def g_headscarf(r, rnd):
    """A linen coif folded: a triangle, its ties."""
    r.poly([(2, 24), (14, 24), (8, 31)], ramp(C.LINEN, 0.66))
    r.poly([(2, 24), (8, 24), (8, 31)], ramp(C.LINEN, 0.82))
    r.rect(2, 23.4, 14, 24.4, ramp(C.LINEN, 0.92))
    r.line([(2, 24), (0.6, 28)], ramp(C.LINEN, 0.6), 0.4)
    r.line([(14, 24), (15.4, 28)], ramp(C.LINEN, 0.5), 0.4)


def g_straw_hat(r, rnd):
    hat(r, 8, 31.4, C.STRAW, 4.0, 7.4, band=C.MADDER, straw=True)


def closed_crate(r, rnd, top=16):
    """A crate nailed shut, a rope round it: a good with no picture of its
    own."""
    r.poly([(1, top), (2.6, top - 2.6), (13.4, top - 2.6), (15, top)], ramp(C.OAK, 0.72))
    for k in range(3):
        x = 2.4 + k * 3.8
        r.line([(x, top - 0.2), (x + 0.8, top - 2.4)], ramp(C.OAK, 0.45), 0.25)
    crate_front(r, 1, 15, top)
    r.rect(7.4, top - 2.6, 8.4, 32, ramp(C.STRAW, 0.5))
    r.rect(7.4, top - 2.6, 7.7, 32, ramp(C.STRAW, 0.8))


def g_other(r, rnd):
    closed_crate(r, rnd, 22)


def p_other(r, rnd):
    closed_crate(r, rnd, 16)


def stew_fill(r, cx, top, w, rnd):
    r.ellipse(cx - w / 2 + 0.8, top, cx + w / 2 - 0.8, top + 1.9, ramp(BREAD, 0.3))
    for col, tone in ((CARROT, 0.6), (MEAT, 0.4), (BREAD, 0.6), (CARROT, 0.7), (LEAF, 0.5)):
        x = rnd.uniform(cx - w / 2 + 1.6, cx + w / 2 - 1.6)
        r.ellipse(x - 0.6, top + 0.4, x + 0.6, top + 1.2, ramp(col, tone))


def g_stew(r, rnd):
    top = bowl(r, 8, 31.0, 11, C.OAK, BREAD, 0.3)
    stew_fill(r, 8, top - 0.1, 11, rnd)
    r.line([(11.8, top + 0.4), (14.6, top - 3.2)], ramp(C.OAK, 0.7), 0.5)


def g_porridge(r, rnd):
    top = bowl(r, 8, 31.0, 11, C.OAK, CREAM, 0.6)
    r.ellipse(6.4, top + 0.2, 8.4, top + 0.9, ramp(CREAM, 0.95))
    r.ellipse(9.4, top + 0.5, 10.2, top + 1.0, ramp(AMBER, 0.6))
    r.line([(11.8, top + 0.4), (14.6, top - 3.2)], ramp(C.OAK, 0.7), 0.5)


def g_porridge_empty(r, rnd):
    top = bowl(r, 8, 31.0, 11, C.OAK)
    r.speckle(4.6, top + 0.3, 10.6, top + 1.4, 4, CREAM, (0.7,), rnd, (0.3, 0.4))
    r.line([(5.0, top + 1.0), (11.0, top - 0.6)], ramp(C.OAK, 0.72), 0.5)


def g_plate_empty(r, rnd):
    top = plate(r, 8, 31.6, 13, C.OAK)
    r.speckle(5, top - 0.8, 11, top + 0.6, 5, BREAD, (0.4, 0.8), rnd, (0.3, 0.5))
    r.line([(4.4, top + 0.6), (11.4, top - 1.0)], ramp(C.PEWTER, 0.8), 0.4)


def g_journeycake(r, rnd):
    """A journeycake: a thick round of cornmeal baked on a board, its top
    golden and scorched in patches, its side a darker band, a wedge cut out
    showing the crumb."""
    # The board it was baked on: its top, then its front edge.
    r.rect(0.4, 25.0, 15.6, 30.6, ramp(C.OAK, 0.5))
    r.rect(0.4, 25.0, 15.6, 25.6, ramp(C.OAK, 0.72))
    r.rect(0.4, 30.6, 15.6, 31.8, ramp(C.OAK, 0.22))
    S = C.S
    top, side = (1.4, 21.6, 11.8, 27.8), (1.4, 24.6, 11.8, 30.8)
    cx, cy = (top[0] + top[2]) / 2, (top[1] + top[3]) / 2
    # The side band (the round's thickness), then its top, both less the
    # wedge cut from the front right (0-80 degrees, clockwise from 3
    # o'clock).
    r.d.pieslice([v * S for v in side], 80, 360, fill=ramp(BREAD, 0.2))
    r.rect(top[0], cy, top[2], cy + 3.0, ramp(BREAD, 0.2))
    r.d.pieslice([v * S for v in top], 80, 360, fill=ramp(CHEESE, 0.62))
    r.d.pieslice([(top[0] + 1.0) * S, (top[1] + 0.6) * S, (cx + 1.0) * S, (cy + 0.6) * S], 180, 300, fill=ramp(CHEESE, 0.84))
    for sx, sy in ((5.0, 25.4), (8.4, 23.2), (3.6, 23.8)):
        r.ellipse(sx - 0.8, sy - 0.4, sx + 0.8, sy + 0.4, ramp(BREAD, 0.46))
    # The cut faces: the crumb, pale, down the thickness.
    ex, ey = cx + math.cos(math.radians(80)) * (top[2] - top[0]) / 2, cy + math.sin(math.radians(80)) * (top[3] - top[1]) / 2
    r.poly([(cx, cy), (ex, ey), (ex, ey + 3.0), (cx, cy + 3.0)], ramp(CHEESE, 0.95))
    r.poly([(cx, cy), (top[2], cy), (top[2], cy + 3.0), (cx, cy + 3.0)], ramp(CHEESE, 0.86))
    # The cut piece, set out to the right: its top, its dark side, its
    # crumb face.
    r.poly([(11.0, 26.2), (15.2, 26.6), (13.6, 28.2)], ramp(CHEESE, 0.64))
    r.poly([(13.6, 28.2), (15.2, 26.6), (15.2, 29.2), (13.6, 30.6)], ramp(BREAD, 0.2))
    r.poly([(11.0, 26.2), (13.6, 28.2), (13.6, 30.6), (11.0, 28.6)], ramp(CHEESE, 0.95))


def g_ale_empty(r, rnd):
    tankard(r, 7.0, 31.6)
    shadow(r, 3, 13)


def pot(r, rnd, fill, fill_cols):
    """An iron pot on its three legs, bail handle up, full."""
    r.ellipse(2, 18, 14, 31, ramp(C.IRON, 0.3))
    r.ellipse(2.6, 18.6, 10, 28, ramp(C.IRON, 0.46))
    r.ellipse(3.6, 19.6, 6.4, 23.4, ramp(C.IRON, 0.7))
    for lx in (3.4, 11.6):
        r.rect(lx, 29.6, lx + 1.0, 32, ramp(C.IRON, 0.2))
    r.ellipse(1.6, 16.6, 14.4, 20.0, ramp(C.IRON, 0.56))
    r.ellipse(2.4, 17.0, 13.6, 19.4, ramp(fill_cols, 0.4))
    fill(r, 8, 17.2, 11.6, rnd)
    arc(r, 1.6, 10.0, 14.4, 22.0, 180, 360, ramp(C.IRON, 0.5), 0.4)


def p_stew(r, rnd):
    pot(r, rnd, stew_fill, BREAD)


def p_porridge(r, rnd):
    pot(r, rnd, lambda r_, cx, top, w, rnd_: r_.ellipse(cx - 4, top + 0.2, cx + 1, top + 1.2, ramp(CREAM, 0.95)), CREAM)


def stack(painter, n=3, step=2.4):
    """A pile of a dish: the dish stacked n high."""
    def paint(r, rnd):
        for k in range(n):
            sub = C.Room(*CELL)
            painter(sub, random.Random(7))
            box = sub.lay.getbbox()
            if box:
                r.lay.alpha_composite(sub.lay, (0, -int(k * step * C.S)))
    return paint


# ---------------------------------------------------------------- the pile of any good

def heap(r, good, rnd, container="crate"):
    """A pile: two of the good laid on an open crate (or basket), shrunk a
    little to sit side by side in its mouth."""
    rim = 23
    (crate_back if container == "crate" else basket_back)(r, 1, 15, rim)
    sub = C.Room(*CELL)
    good(sub, random.Random(11))
    box = sub.lay.getbbox()
    if box:
        art = sub.lay.crop(box)
        k = min(1.0, 9.5 * C.S / art.width)
        art = art.resize((max(1, int(art.width * k)), max(1, int(art.height * k))), Image.LANCZOS)
        for i, (x, dy) in enumerate(((1.4, 0.0), (15 - 1.4, -1.4))):
            ax = int(x * C.S) if i == 0 else int(x * C.S) - art.width
            r.lay.alpha_composite(art, (max(0, ax), int((rim + 1.6 + dy) * C.S) - art.height))
    (crate_front if container == "crate" else basket_front)(r, 1, 15, rim)


def on_crate(good):
    return lambda r, rnd: heap(r, good, rnd, "crate")


def in_basket(good):
    return lambda r, rnd: heap(r, good, rnd, "basket")


# ---------------------------------------------------------------- the table

# One entry per good, in StallScenes.GOODS order: (kinds, good painter,
# pile painter).
GOODS = [
    (["apples"], g_apples, p_apples),
    (["raspberries"], g_rasp, p_rasp),
    (["blueberries"], g_blue, p_blue),
    (["carrots"], g_carrots, p_carrots),
    (["wheat"], g_wheat, p_wheat),
    (["flour"], g_flour, p_flour),
    (["salt"], g_salt, p_salt),
    (["cheese"], g_cheese, p_cheese),
    (["milk"], g_milk, p_milk),
    (["meat"], g_meat, p_meat),
    (["fried_meat"], g_fried, p_fried),
    (["bread", "flatbread"], g_bread, p_bread),
    (["water", "water_from_the_well", "water_from_well", "well_water"], g_water, p_water),
    (["firewood"], g_firewood, p_firewood),
    (["sage", "wormwood", "feverfew", "greenweed", "coca_tea"], g_herbs, p_herbs),
    (["indigo"], g_dye(C.INDIGO), p_dye(C.INDIGO)),
    (["madder"], g_dye(C.MADDER), p_dye(C.MADDER)),
    (["logwood"], g_dye(LOGWOOD), p_dye(LOGWOOD)),
    (["weld"], g_dye(WELD), p_dye(WELD)),
    (["ale", "small_ale", "mulled_ale"], g_ale, p_ale),
    (["calming_draught"], g_draught, p_draught),
    (["locket", "silver_locket", "duck_pendant"], g_locket, on_crate(g_locket)),
    (["whalebone_charm"], g_charm, on_crate(g_charm)),
    (["spectacles"], g_spectacles, on_crate(g_spectacles)),
    (["axe"], g_axe, on_crate(g_axe)),
    (["hammer"], g_hammer, on_crate(g_hammer)),
    (["shovel"], g_shovel, on_crate(g_shovel)),
    (["nail"], g_nail, p_nail),
    (["horseshoe"], g_horseshoe, on_crate(g_horseshoe)),
    (["skillet"], g_skillet, on_crate(g_skillet)),
    (["iron", "iron_bar"], g_iron, p_iron),
    (["whetstone"], g_whetstone, on_crate(g_whetstone)),
    (["thread"], g_thread, in_basket(g_thread)),
    (["linen"], g_linen, p_linen),
    (["gloves"], g_gloves, on_crate(g_gloves)),
    (["suspenders"], g_suspenders, on_crate(g_suspenders)),
    (["vest"], g_vest, on_crate(g_vest)),
    (["scarf"], g_scarf, on_crate(g_scarf)),
    (["knee_socks"], g_knee_socks, on_crate(g_knee_socks)),
    (["stockings"], g_stockings, on_crate(g_stockings)),
    (["boots"], g_boots, on_crate(g_boots)),
    (["cuffed_boots"], g_cuffed_boots, on_crate(g_cuffed_boots)),
    (["frilly_skirt"], g_skirt, on_crate(g_skirt)),
    (["frilly_dress"], g_dress, on_crate(g_dress)),
    (["long_dress"], g_long_dress, on_crate(g_long_dress)),
    (["linen_shirt", "mended_shirt"], g_shirt, on_crate(g_shirt)),
    (["cloak"], g_cloak, on_crate(g_cloak)),
    (["mantle"], g_mantle, on_crate(g_mantle)),
    (["mantled_cloak"], g_mantled_cloak, on_crate(g_mantled_cloak)),
    (["brimmed_hat"], g_brimmed_hat, on_crate(g_brimmed_hat)),
    (["felt_hat"], g_felt_hat, on_crate(g_felt_hat)),
    (["headscarf"], g_headscarf, on_crate(g_headscarf)),
    (["straw_hat"], g_straw_hat, on_crate(g_straw_hat)),
    (["_other"], g_other, p_other),
    (["stew"], g_stew, p_stew),
    (["porridge"], g_porridge, p_porridge),
    (["porridge_empty"], g_porridge_empty, stack(g_porridge_empty)),
    (["stew_empty", "plate_empty"], g_plate_empty, stack(g_plate_empty, 4, 1.2)),
    (["journeycake"], g_journeycake, in_basket(g_journeycake)),
    (["ale_empty"], g_ale_empty, p_ale),
]


def index_table():
    """kind -> goods index, as StallScenes.GOODS has it (OTHER's kind
    _other is the client's OTHER constant, not a table entry)."""
    out = {}
    for i, (kinds, _g, _p) in enumerate(GOODS):
        for k in kinds:
            if k != "_other":
                out[k] = i
    return out


def other_index():
    return [i for i, (kinds, _g, _p) in enumerate(GOODS) if "_other" in kinds][0]


# The painters draw a little small for the cell; each cell's painting is
# grown about its foot by these before the shrink (a good on a counter,
# a pile on the floor), trimmed to the cell.
GOOD_GROW = 1.2
PILE_GROW = 1.14


def paint_cell(painter, seed, grow=1.0):
    r = C.Room(*CELL)
    painter(r, random.Random(seed))
    if grow != 1.0:
        w, h = r.lay.size
        big = r.lay.resize((int(w * grow), int(h * grow)), Image.LANCZOS)
        r.lay = Image.new("RGBA", (w, h), (0, 0, 0, 0))
        r.lay.alpha_composite(big, (0, 0), ((big.width - w) // 2, big.height - h, (big.width - w) // 2 + w, big.height))
    return C.shrink(r, PALETTE, outline=0.5, ring=0.36)


ROWS = math.ceil(len(GOODS) * 2 / COLUMNS)


def sheet():
    """stall_goods.png, painted at DENSITY (the kit's density is put back
    after)."""
    saved = C.DENSITY
    C.set_density(DENSITY)
    try:
        w, h = CELL[0] * DENSITY, CELL[1] * DENSITY
        out = Image.new("RGBA", (COLUMNS * w, ROWS * h), (0, 0, 0, 0))
        for i, (_kinds, good, pile) in enumerate(GOODS):
            for j, painter in enumerate((good, pile)):
                cell = 2 * i + j
                out.alpha_composite(paint_cell(painter, 1000 + cell, PILE_GROW if j else GOOD_GROW),
                                    ((cell % COLUMNS) * w, (cell // COLUMNS) * h))
        return out
    finally:
        C.set_density(saved)
