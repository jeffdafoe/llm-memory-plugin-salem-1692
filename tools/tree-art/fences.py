"""Paints salem's fences and gates (LLM-968): the ranch fence's pieces, its
four three-cell breaks, the ranch fence gate and the high stone gate. Every
pixel is drawn here, no pack art.

    python tools/tree-art/fences.py
    python tools/tree-art/fences.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/fences.py --check     # joins, margins, sizes, a fresh paint

Writes client/assets/trees/ranch-fence.png, ranch-fence-gate.png and
stone-gate.png, drawn at twice the pack art's size and shown at
render_scale 1 (the same size in the world, twice the detail), anchor as the
pack art (0.5/0.85; the stone gate 0.5/0.7).

The ranch fence is tiled. Its sheet is one row of 32x32 cells (one world
tile each), one per catalog state in PIECES order. The engine picks a
segment's state by the tags of its four neighbours (fence.go fencePieceFor),
so each cell is painted from the same four flags: a hewn post in the middle,
two split rails running to each side that has a neighbour, and, where a
neighbour is north or south, the rails seen from above along the posts' line
running to that edge. Every rail and post casts a shadow on the ground, which
is what shows a north-south rail is in the air. The rails pass through the
posts (mortised), so every side of a pen looks the same. Rails and their
shadows meet the cell edge on the same rows in every cell, so neighbours
join. The breaks (damage_minor.go) span three cells: the middle
segment loses its post and its rails break and sag into both neighbours, so
each break is painted as one three-cell scene and cut into its -l, middle and
-r cells.

From photographs of New England post-and-rail fences: weathered oak and
chestnut, grey-brown, rough split rails, square hewn posts with a pale cut
top.
"""

import argparse
import math
import os
import random
import sys

from PIL import Image, ImageDraw

import build as B
import plants as P

OUT_DIR = B.OUT_DIR
DENSITY = 2

# ---------------------------------------------------------------- colours

WOOD = ["1e1610", "33271c", "4a3a2a", "625038", "7a6648", "94805e", "ae9a78"]
CUT = ["6a5a40", "8c7a58", "ae9c78", "cab896"]
STONE = ["2a2824", "403c36", "58534a", "716b60", "8a8478", "a49e92", "bfbaae"]
MORTAR = ["3a3630", "4e4a42"]
IRON = ["141414", "26262a", "3a3a40", "54545c", "70707a"]

# ---------------------------------------------------------------- ranch fence

# One cell per catalog state, in sheet order: (state, neighbours n e s w).
# end-left, end-right and post are their own cells (in the pack sheet the
# bottom corners doubled as line ends and v-bottom as the lone post, so an
# end showed a post reaching for a neighbour that was not there).
PIECES = [
    ("corner-tl", (0, 1, 1, 0)), ("h", (0, 1, 0, 1)), ("corner-tr", (0, 0, 1, 1)),
    ("v-top", (0, 0, 1, 0)), ("v", (1, 0, 1, 0)), ("v-bottom", (1, 0, 0, 0)),
    ("corner-bl", (1, 1, 0, 0)), ("corner-br", (1, 0, 0, 1)),
    ("end-left", (0, 1, 0, 0)), ("end-right", (0, 0, 0, 1)), ("post", (0, 0, 0, 0)),
]
BREAKS = 4
CELL = 16                 # pack-art pixels; drawn at density 2 (32x32)
POST_X = 8.0              # the post's middle, in cell px
POST_TOP = 2.2            # its cut top
RAILS = (5.0, 9.0)        # rail heights (their middles), the same in every cell
RAIL_T = 1.3              # rail thickness


def piece_names():
    names = [p[0] for p in PIECES]
    for k in range(1, BREAKS + 1):
        names += ["h-broken-%d-l" % k, "h-broken-%d" % k, "h-broken-%d-r" % k]
    return names


LEVEL_END = 1.5           # cell px at each rail end held level and unmarked


def wobble(seed, k, t, length):
    """A rough split rail's edge: a little up and down along it, but level
    over the last LEVEL_END px at both ends, so a rail meets its neighbour's
    on exactly the same rows."""
    dist = min(t, 1 - t) * length
    env = max(0.0, min(1.0, (dist - LEVEL_END) / LEVEL_END))
    return 0.25 * env * math.sin(3.1 * t * math.pi + seed * 1.7 + k)


def draw_rail(d, x0, y0, x1, y1, seed, k, tone=0.5, t=RAIL_T):
    """A split rail from (x0, y0) to (x1, y1) in cell px: lit top edge, a
    darker underside, a few grain marks."""
    S = P.S
    n = max(2, int(abs(x1 - x0) * 2))
    top, bot = [], []
    for i in range(n + 1):
        f = i / n
        x = (x0 + (x1 - x0) * f) * S
        y = (y0 + (y1 - y0) * f + wobble(seed, k, f, abs(x1 - x0))) * S
        top.append((x, y - t / 2 * S))
        bot.append((x, y + t / 2 * S))
    d.polygon(top + bot[::-1], fill=B.ramp(WOOD, tone - 0.1))
    mid = [(x, (a[1] + b[1]) / 2 - 0.1 * S) for (x, a), b in zip([(p[0], p) for p in top], bot)]
    d.line(top, fill=B.ramp(WOOD, tone + 0.25), width=max(1, int(0.35 * S)))
    d.line(bot, fill=B.ramp(WOOD, tone - 0.3), width=max(1, int(0.3 * S)))
    rnd = random.Random(seed * 31 + k)
    lo, hi = min(x0, x1) + LEVEL_END, max(x0, x1) - LEVEL_END
    for _ in range(max(1, n // 6)):
        i = rnd.randrange(1, n)
        x, y = mid[i]
        mark = rnd.uniform(1.0, 2.2)
        # Grain marks stay off the level ends.
        if lo * S <= x and x + mark * S <= hi * S:
            d.line([(x, y), (x + mark * S, y)], fill=B.ramp(WOOD, tone - 0.22), width=max(1, int(0.2 * S)))


def draw_post(d, x, top, bottom, seed, w=2.4, tone=0.5, broken=False):
    """A square hewn post: lit left face, shaded right face, a pale cut top
    (or a splintered top when broken)."""
    S = P.S
    d.rectangle(((x - w / 2) * S, top * S, (x + w / 2) * S, bottom * S), fill=B.ramp(WOOD, tone))
    d.rectangle(((x + w * 0.1) * S, top * S, (x + w / 2) * S, bottom * S), fill=B.ramp(WOOD, tone - 0.22))
    d.rectangle(((x - w / 2) * S, top * S, (x - w * 0.25) * S, bottom * S), fill=B.ramp(WOOD, tone + 0.2))
    rnd = random.Random(seed)
    for _ in range(3):
        gx = x + rnd.uniform(-0.7, 0.3) * w / 2
        gy = rnd.uniform(top + 1.0, bottom - 1.5)
        d.line([(gx * S, gy * S), (gx * S, (gy + rnd.uniform(1.2, 2.6)) * S)], fill=B.ramp(WOOD, tone - 0.3),
               width=max(1, int(0.2 * S)))
    if broken:
        for k in range(4):
            tx = x - w / 2 + (k + 0.5) * w / 4
            d.polygon([((tx - w / 8) * S, top * S), ((tx + w / 8) * S, top * S),
                       (tx * S, (top - rnd.uniform(0.6, 1.6)) * S)], fill=B.ramp(CUT, 0.5))
    else:
        d.rectangle(((x - w / 2) * S, (top - 0.7) * S, (x + w / 2) * S, (top + 0.5) * S), fill=B.ramp(CUT, 0.45))
        d.rectangle(((x - w / 2) * S, (top - 0.7) * S, (x - w * 0.1) * S, (top - 0.1) * S), fill=B.ramp(CUT, 0.85))


# The rails pass through the posts' middle, as on a mortised post-and-rail
# fence, in both directions. Nailed to one face instead, they would sit
# inside the pen on some sides and outside on others, since a segment's piece
# comes from its neighbours alone and cannot know which side is the pen's
# inside (Jeff chose through the posts, 2026-11-25).
SIDE_X = POST_X


def draw_stringer(d, y0, y1, seed, tone=0.55):
    """Rails running north or south, seen from above: the top face of a rail
    along the posts' line from y0 to y1 (cell px), lit on its left side. Its
    shadow on the ground (stringer_shadow), apart from it, is what shows it is
    in the air."""
    S = P.S
    x = SIDE_X
    d.rectangle(((x - 0.7) * S, y0 * S, (x + 0.7) * S, y1 * S), fill=B.ramp(WOOD, tone))
    d.rectangle(((x - 0.7) * S, y0 * S, (x - 0.2) * S, y1 * S), fill=B.ramp(WOOD, tone + 0.2))
    d.rectangle(((x + 0.35) * S, y0 * S, (x + 0.7) * S, y1 * S), fill=B.ramp(WOOD, tone - 0.2))


# Cast shadows on the ground, light from the upper left: a point at height h
# above the ground line throws its shadow KX * h right and KY * h down of
# the ground under it. The offset is what shows a rail is in the air (a
# north-south rail with no shadow apart from it reads as a stripe on the
# ground).
KX, KY = 0.5, 0.1


def shadow_point(x, y, base):
    h = max(0.0, base - y)
    return x + h * KX, base + h * KY


def rail_shadow(sd, x0, y0, x1, y1, base, span=CELL):
    """The shadow of a rail from (x0, y0) to (x1, y1). An end on the scene's
    left or right edge keeps its x, so the shadow runs on unbroken into the
    neighbour's (whose own end there is also at the edge)."""
    S = P.S
    a, b = shadow_point(x0, y0, base), shadow_point(x1, y1, base)
    if x0 in (0, span):
        a = (x0, a[1])
    if x1 in (0, span):
        b = (x1, b[1])
    sd.line([(a[0] * S, a[1] * S), (b[0] * S, b[1] * S)], fill=255, width=max(1, int(1.1 * S)))


def post_shadow(sd, x, top, base, w=2.4):
    S = P.S
    tx, ty = shadow_point(x, top, base)
    sd.polygon([((x - w / 2) * S, base * S), ((x + w / 2) * S, base * S),
                ((tx + w / 2) * S, ty * S), ((tx - w / 2) * S, ty * S)], fill=255)


def stringer_shadow(sd, n, s, base):
    """The two north-south rails' shadows: each a line down the ground,
    right of the stringer by its own rail's height."""
    S = P.S
    for ry in RAILS:
        x, y = shadow_point(SIDE_X, ry, base)
        if n:
            sd.line([(x * S, 0), (x * S, y * S)], fill=255, width=max(1, int(1.2 * S)))
        if s:
            sd.line([(x * S, y * S), (x * S, CELL * S)], fill=255, width=max(1, int(1.2 * S)))


def paint_cell(flags, seed):
    """One fence cell from its neighbour flags (n, e, s, w): the art and its
    ground shadow (an L mask)."""
    n, e, s, w = flags
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    shade = Image.new("L", (P.W, P.H), 0)
    sd = ImageDraw.Draw(shade)
    base = P.BASE_Y / P.S
    stringer_shadow(sd, n, s, base)
    for ry in RAILS:
        if w:
            rail_shadow(sd, 0, ry, POST_X, ry, base)
        if e:
            rail_shadow(sd, POST_X, ry, CELL, ry, base)
    post_shadow(sd, POST_X, POST_TOP, base)
    for k, ry in enumerate(RAILS):
        if w:
            draw_rail(d, 0, ry, POST_X, ry, seed, k)
        if e:
            draw_rail(d, POST_X, ry, CELL, ry, seed + 7, k)
    # The rails from the north end in this post (behind it: the post is
    # nearer the viewer), the lower one lower; the rails to the south come
    # out of its front from the top rail down, so they are drawn over it.
    if n:
        draw_stringer(d, 0, RAILS[1], seed)
    draw_post(d, POST_X, POST_TOP, base, seed)
    if s:
        draw_stringer(d, RAILS[0], CELL, seed + 1)
    return lay, shade


def paint_break(k):
    """A three-cell break scene, 48 cell px wide: posts in the outer cells,
    the middle post gone, its rails broken. Returns the scene layer and its
    ground shadow."""
    S = P.S
    W3 = P.W * 3
    lay = Image.new("RGBA", (W3, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    base = P.BASE_Y / S
    lx, rx, mx = POST_X, 2 * CELL + POST_X, CELL + POST_X
    shade = Image.new("L", (W3, P.H), 0)
    sd = ImageDraw.Draw(shade)

    def rail(x0, y0, x1, y1, *args, **kw):
        rail_shadow(sd, x0, y0, x1, y1, base, span=3 * CELL)
        draw_rail(d, x0, y0, x1, y1, *args, **kw)

    def post(x, top, bottom, *args, **kw):
        post_shadow(sd, x, top, bottom)
        draw_post(d, x, top, bottom, *args, **kw)

    r0, r1 = RAILS
    # The outer sides stay whole: the rails run on into the fence.
    for kk, ry in enumerate(RAILS):
        rail(0, ry, lx, ry, 100 + k, kk)
        rail(rx, ry, 3 * CELL, ry, 110 + k, kk)
    if k == 1:
        # The post snapped off at the foot and lies in the grass; both rails
        # sag from the outer posts to a low middle.
        post(mx, base - 2.2, base, 120, broken=True)
        sd.polygon([((mx - 5) * S, (base + 0.4) * S), ((mx + 4.6) * S, (base - 0.8) * S),
                    ((mx + 5.0) * S, (base + 0.4) * S), ((mx - 4.2) * S, (base + 1.6) * S)], fill=255)
        d.polygon([((mx - 5) * S, (base - 0.2) * S), ((mx + 4) * S, (base - 1.6) * S),
                   ((mx + 4.4) * S, (base - 0.4) * S), ((mx - 4.6) * S, (base + 1.0) * S)], fill=B.ramp(WOOD, 0.45))
        for kk, ry in enumerate(RAILS):
            rail(lx, ry, mx - 0.5, ry + 3.2, 130, kk, tone=0.45)
            rail(mx + 0.5, ry + 3.2, rx, ry, 131, kk, tone=0.45)
    elif k == 2:
        # The post leans hard to the right; the top rail broke at it, the low
        # rail still holds, bowed.
        top_x = mx + 3.5
        post_shadow(sd, mx + 1.6, POST_TOP + 2.3, base)
        d.polygon([((mx - 1.2) * S, base * S), ((mx + 1.2) * S, base * S),
                   ((top_x + 1.2) * S, (POST_TOP + 2.6) * S), ((top_x - 1.2) * S, (POST_TOP + 2.0) * S)],
                  fill=B.ramp(WOOD, 0.5))
        d.line([((mx - 1.2) * S, base * S), ((top_x - 1.2) * S, (POST_TOP + 2.0) * S)], fill=B.ramp(WOOD, 0.72),
               width=max(1, int(0.4 * S)))
        rail(lx, r0, mx - 1.5, r0 + 2.4, 140, 0, tone=0.45)
        rail(mx + 2.5, r0 + 4.5, rx, r0, 141, 0, tone=0.45)
        rail(lx, r1, rx, r1, 142, 1, tone=0.5)
    elif k == 3:
        # The post is gone; the top rails hang from the outer posts, their
        # broken ends on the ground, the low rails lie across the gap.
        rail(lx, r0, mx - 1.0, base - 0.4, 150, 0, tone=0.45)
        rail(mx + 1.5, base - 0.2, rx, r0, 151, 0, tone=0.45)
        rail(lx + 1.0, base - 0.6, rx - 2.0, base + 0.4, 152, 1, tone=0.42)
    else:
        # A stump; one top rail still spans the gap, sagging, the others lie
        # broken at the foot.
        post(mx, base - 3.0, base, 160, broken=True)
        rail(lx, r0, rx, r0, 161, 0, tone=0.5)
        rail(lx, r1, mx - 2.0, base - 0.3, 162, 1, tone=0.45)
        rail(mx + 3.0, base - 0.2, rx, r1, 163, 1, tone=0.45)
    post(lx, POST_TOP, base, 170 + k)
    post(rx, POST_TOP, base, 180 + k)
    return lay, shade


def fence_palette():
    return WOOD + CUT


def shrink_shaded(lay, shade, palette):
    """plants.shrink (snap, the bottom edge outlined), then the cast shadow:
    a frame pixel at least 40% in shadow and not covered by the art takes
    B.SHADOW (the translucent ground shadow every painted piece uses)."""
    out = P.shrink(lay, palette, 0.45)
    small = shade.resize((P.FW, P.FH), Image.BOX).load()
    op = out.load()
    for y in range(P.FH):
        for x in range(P.FW):
            if op[x, y][3] == 0 and small[x, y] >= 102:
                op[x, y] = B.SHADOW
    return out


def fence_cells():
    """Every ranch-fence cell, in piece_names() order: (name, frame, flags).
    flags (n, e, s, w) say which edges the cell must reach; a break cell
    reaches its left and right edges."""
    P.use_frame(CELL, CELL, density=DENSITY)
    out = []
    for i, (name, flags) in enumerate(PIECES):
        lay, shade = paint_cell(flags, 11 + i)
        out.append((name, shrink_shaded(lay, shade, fence_palette()), flags))
    for k in range(1, BREAKS + 1):
        scene, shade = paint_break(k)
        for j, suffix in enumerate(("-l", "", "-r")):
            box = (j * P.W, 0, (j + 1) * P.W, P.H)
            out.append(("h-broken-%d%s" % (k, suffix),
                        shrink_shaded(scene.crop(box), shade.crop(box), fence_palette()), (0, 1, 0, 1)))
    return out


def build_fence():
    cells = fence_cells()
    sheet = Image.new("RGBA", (P.FW * len(cells), P.FH), (0, 0, 0, 0))
    for i, (_, frame, _) in enumerate(cells):
        sheet.paste(frame, (i * P.FW, 0))
    return sheet


# ---------------------------------------------------------------- gates

def paint_ranch_gate():
    """An open gateway: two tall posts with a lintel across their tops and
    short braces, rail stubs running on into the fence at both sides."""
    P.use_frame(64, 48, density=DENSITY)
    S = P.S
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    base = P.BASE_Y / S
    lx, rx = 5.0, 59.0
    for kk, ry in enumerate((base - 8.6, base - 4.6)):
        draw_rail(d, 0.4, ry, lx, ry, 200, kk)
        draw_rail(d, rx, ry, 63.6, ry, 201, kk)
    draw_post(d, lx, 6.0, base, 202, w=3.2)
    draw_post(d, rx, 6.0, base, 203, w=3.2)
    draw_rail(d, 1.5, 6.2, 62.5, 6.2, 204, 0, tone=0.55, t=2.4)
    for x0, x1 in ((lx + 1.6, lx + 6.0), (rx - 1.6, rx - 6.0)):
        draw_rail(d, x0, 11.5, x1, 7.4, 205, 1, tone=0.45, t=1.1)
    return lay, WOOD + CUT


def stone_wall(d, rnd, x0, y0, x1, y1, base_tone=0.5):
    """Fieldstone laid in rough courses between (x0, y0) and (x1, y1), frame
    px: rounded stones of mixed width, lit tops, dark mortar between."""
    S = P.S
    d.rectangle((x0 * S, y0 * S, x1 * S, y1 * S), fill=B.ramp(MORTAR, 0.3))
    y = y1
    row = 0
    while y > y0 + 0.5:
        h = rnd.uniform(2.2, 3.0)
        top = max(y0, y - h)
        x = x0 - rnd.uniform(0.0, 1.5) * (row % 2)
        while x < x1:
            w = rnd.uniform(2.6, 4.6)
            sx0, sx1 = max(x0, x + 0.2), min(x1, x + w - 0.2)
            if sx1 - sx0 > 0.6:
                tone = base_tone + rnd.uniform(-0.18, 0.18)
                r = min(0.9, (sx1 - sx0) / 2) * S
                d.rounded_rectangle((sx0 * S, (top + 0.2) * S, sx1 * S, (y - 0.2) * S), radius=r, fill=B.ramp(STONE, tone))
                d.rounded_rectangle((sx0 * S, (top + 0.2) * S, sx1 * S, (top + 1.0) * S), radius=r * 0.6,
                                    fill=B.ramp(STONE, tone + 0.2))
            x += w
        y = top
        row += 1


STONE_GATE_FEET = 62.4   # frame px of the 64x64 frame: 8 below 0.85, a whole-pixel shift


def paint_stone_gate(state):
    """A fieldstone gateway: two square pillars with flat cap stones, a stone
    arch between, and a pair of iron gate leaves, shut or swung back."""
    P.use_frame(64, 64, density=DENSITY)
    S = P.S
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    rnd = random.Random(301)
    # The feet stand near the frame's bottom, below the catalog anchor (0.7),
    # as the pack art's did: the placed gate keeps its place on the map.
    base = STONE_GATE_FEET
    # The opening and the arch.
    # The gate as designed stands on 0.85 of the frame; dy moves all of it
    # down to its feet, so its proportions stay the approved ones.
    dy = base - 0.85 * 64
    ox0, ox1, spring, crown = 15.0, 49.0, 20.0 + dy, 8.0 + dy
    stone_wall(d, rnd, ox0 - 1.0, crown - 2.5, ox1 + 1.0, spring + 1.0, 0.45)
    # Cut the arch's opening back out, then line it with voussoirs.
    cx, ry = (ox0 + ox1) / 2, spring - crown
    hole = Image.new("L", (P.W, P.H), 0)
    hd = ImageDraw.Draw(hole)
    hd.rectangle((ox0 * S, spring * S, ox1 * S, base * S), fill=255)
    hd.chord((ox0 * S, (spring - ry) * S, ox1 * S, (spring + ry) * S), 180, 360, fill=255)
    lay.paste((0, 0, 0, 0), (0, 0), hole)
    d = ImageDraw.Draw(lay)
    for i in range(11):
        a0 = math.pi + math.pi * i / 11
        a1 = math.pi + math.pi * (i + 1) / 11
        rx0, rx1 = (ox1 - ox0) / 2, (ox1 - ox0) / 2 + 2.6
        ry0, ry1 = ry, ry + 2.4
        pts = [(cx + math.cos(a0) * rx0, spring + math.sin(a0) * ry0), (cx + math.cos(a1) * rx0, spring + math.sin(a1) * ry0),
               (cx + math.cos(a1) * rx1, spring + math.sin(a1) * ry1), (cx + math.cos(a0) * rx1, spring + math.sin(a0) * ry1)]
        tone = 0.55 + 0.12 * ((i % 3) - 1)
        d.polygon([(x * S, y * S) for x, y in pts], fill=B.ramp(STONE, tone))
        d.line([(pts[0][0] * S, pts[0][1] * S), (pts[3][0] * S, pts[3][1] * S)], fill=B.ramp(MORTAR, 0.2),
               width=max(1, int(0.3 * S)))
    # The gate leaves inside the opening: shut, or swung back to the pillars.
    def leaf(x0, x1, top):
        d.rectangle((x0 * S, top * S, x1 * S, (top + 1.0) * S), fill=B.ramp(IRON, 0.5))
        d.rectangle((x0 * S, (base - 3.0) * S, x1 * S, (base - 2.2) * S), fill=B.ramp(IRON, 0.45))
        n = max(2, int(abs(x1 - x0) / 1.6))
        for j in range(n + 1):
            bx = x0 + (x1 - x0) * j / n
            d.rectangle(((bx - 0.3) * S, (top - 0.8) * S, (bx + 0.3) * S, (base - 0.6) * S), fill=B.ramp(IRON, 0.35 + 0.2 * (j % 2)))
            d.polygon([((bx - 0.55) * S, (top - 0.8) * S), ((bx + 0.55) * S, (top - 0.8) * S), (bx * S, (top - 1.9) * S)],
                      fill=B.ramp(IRON, 0.6))
    if state == "closed":
        leaf(ox0 + 0.6, cx - 0.2, spring - 2.0)
        leaf(cx + 0.2, ox1 - 0.6, spring - 2.0)
        d.rectangle(((cx - 0.5) * S, (spring + 6) * S, (cx + 0.5) * S, (spring + 8.5) * S), fill=B.ramp(IRON, 0.8))
    else:
        leaf(ox0 + 0.4, ox0 + 3.2, spring - 2.0)
        leaf(ox1 - 3.2, ox1 - 0.4, spring - 2.0)
    # Pillars, over the arch's ends, with cap stones.
    for px0, px1 in ((5.0, ox0 + 0.5), (ox1 - 0.5, 59.0)):
        stone_wall(d, rnd, px0, 6.0 + dy, px1, base, 0.5)
        d.rectangle(((px0 - 0.8) * S, (4.4 + dy) * S, (px1 + 0.8) * S, (6.6 + dy) * S), fill=B.ramp(STONE, 0.62))
        d.rectangle(((px0 - 0.8) * S, (4.4 + dy) * S, (px1 + 0.8) * S, (5.1 + dy) * S), fill=B.ramp(STONE, 0.85))
        d.rectangle(((px0 - 0.8) * S, (6.1 + dy) * S, (px1 + 0.8) * S, (6.6 + dy) * S), fill=B.ramp(STONE, 0.25))
    return lay, STONE + MORTAR + IRON


def build_ranch_gate():
    lay, pal = paint_ranch_gate()
    return P.shrink(lay, pal, 0.45, 0.25)


def build_stone_gate():
    frames = []
    for state in ("closed", "open"):
        lay, pal = paint_stone_gate(state)
        frames.append(P.shrink(lay, pal, 0.45, 0.2))
    sheet = Image.new("RGBA", (P.FW * 2, P.FH), (0, 0, 0, 0))
    for i, f in enumerate(frames):
        sheet.paste(f, (i * P.FW, 0))
    return sheet


SHEETS = {
    "ranch-fence": build_fence,
    "ranch-fence-gate": build_ranch_gate,
    "stone-gate": build_stone_gate,
}


# ---------------------------------------------------------------- check

def edges_touched(frame):
    """Which of the frame's outer rows/columns hold any pixel: (n, e, s, w)."""
    px = frame.load()
    fw, fh = frame.size
    return (any(px[x, 0][3] for x in range(fw)), any(px[fw - 1, y][3] for y in range(fh)),
            any(px[x, fh - 1][3] for x in range(fw)), any(px[0, y][3] for y in range(fh)))


def edge_profile(frame, side):
    """What lies along one outer row or column of a frame, pixel by pixel:
    'w' wood (opaque), 's' shadow (translucent), '.' nothing."""
    px = frame.load()
    fw, fh = frame.size
    coords = {"n": [(x, 0) for x in range(fw)], "s": [(x, fh - 1) for x in range(fw)],
              "e": [(fw - 1, y) for y in range(fh)], "w": [(0, y) for y in range(fh)]}[side]
    return "".join("." if px[c][3] == 0 else ("w" if px[c][3] == 255 else "s") for c in coords)


def runs(profile, kind):
    """The contiguous runs of one kind ('w' or 's') along a profile, as
    (first, last) index pairs."""
    out, start = [], None
    for i, c in enumerate(profile + "."):
        if c == kind and start is None:
            start = i
        elif c != kind and start is not None:
            out.append((start, i - 1))
            start = None
    return out


def runs_meet(a, b, slack=1):
    """Every run of a has a run of b within slack pixels of it, and back."""
    def near(r, rs):
        return any(s[0] <= r[1] + slack and s[1] >= r[0] - slack for s in rs)
    return all(near(r, b) for r in a) and all(near(r, a) for r in b)


def join_problems(cells):
    """How neighbouring cells fail to join, wood and shadow compared apart.

    - Ordinary joins (every edge of an ordinary cell, a break's outer side):
      every east edge shows exactly the same wood and shadow pixels as every
      west edge, and every north edge as every south edge, so any two
      neighbours meet pixel for pixel.
    - A break's inner seams (-l to middle, middle to -r): its rails slope
      across them, so each wood or shadow run on one side must meet a run of
      the same kind on the other within a pixel."""
    problems = []
    by_name = {name: frame for name, frame, _ in cells}
    seen = {"e": set(), "w": set(), "n": set(), "s": set()}
    for name, frame, flags in cells:
        broken = name.startswith("h-broken")
        for side, flag in zip("nesw", flags):
            if not flag:
                continue
            if broken and not ((side == "w" and name.endswith("-l")) or (side == "e" and name.endswith("-r"))):
                continue
            seen[side].add(edge_profile(frame, side))
    for a, b in (("e", "w"), ("n", "s")):
        if len(seen[a]) > 1 or len(seen[b]) > 1 or seen[a] != seen[b]:
            problems.append("cells meet their %s/%s neighbours with different edges: %s %s, %s %s"
                            % (a, b, a, sorted(seen[a]), b, sorted(seen[b])))
    for k in range(1, BREAKS + 1):
        names = ["h-broken-%d-l" % k, "h-broken-%d" % k, "h-broken-%d-r" % k]
        for left, right in zip(names, names[1:]):
            pe, pw = edge_profile(by_name[left], "e"), edge_profile(by_name[right], "w")
            # Some rail crosses every inner seam (a break keeps rails to both
            # outer posts), so each side must show wood: with none on either
            # side the run comparison below would pass vacuously.
            if not runs(pe, "w") or not runs(pw, "w"):
                problems.append("break %d: no rail crosses the seam between %s and %s: %s / %s"
                                % (k, left, right, pe, pw))
                continue
            for kind in "ws":
                if not runs_meet(runs(pe, kind), runs(pw, kind)):
                    problems.append("break %d: %s's east edge and %s's west edge do not meet (%s): %s / %s"
                                    % (k, left, right, {"w": "wood", "s": "shadow"}[kind], pe, pw))
    return problems


def check(out_dir):
    """Each sheet is exactly what this script paints now. A fence cell
    reaches exactly the edges its neighbour flags name and stays off the
    others, and neighbouring cells join (join_problems). The gates stay off
    their top and bottom edges."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun fences.py)" % name)
            ok = False
    cells = fence_cells()
    for name, frame, flags in cells:
        got = tuple(bool(v) for v in edges_touched(frame))
        want = tuple(bool(v) for v in flags)
        bad = got != want
        ok = ok and not bad
        print("%-15s edges n/e/s/w %s%s" % (name, "".join("x" if g else "." for g in got),
                                            "" if not bad else "  <- want %s" % "".join("x" if w else "." for w in want)))
    for problem in join_problems(cells):
        print(problem)
        ok = False
    for name in ("ranch-fence-gate", "stone-gate"):
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        n, _, s, _ = edges_touched(sheet)
        if n or s:
            print("%s: art on the top or bottom edge" % name)
            ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's fences and gates.")
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
