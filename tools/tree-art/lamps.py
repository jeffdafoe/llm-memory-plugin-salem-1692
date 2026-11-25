"""Paints salem's lamps (LLM-968): Lamp Post, Lamp Post 2, Lamp Post (Sign)
and the Torch. Every pixel is drawn here, no pack art.

    python tools/tree-art/lamps.py
    python tools/tree-art/lamps.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/lamps.py --check     # margins, sizes, a fresh paint

Writes client/assets/trees/lamp-post.png, lamp-post-2.png, lamp-post-sign.png
and torch.png, drawn at twice the pack art's size and shown at render_scale 1
(the same size in the world, twice the detail). The feet stay on the catalog
anchor (0.5/0.9 for Lamp Post and Lamp Post (Sign), 0.5/0.85 for Lamp Post 2
and the Torch), so placed lamps keep their place and their lights.

Sheets, one row each, frames left to right:
- lamp-post, lamp-post-2: unlit, lit (48x80 pack px, 96x160 on the sheet).
- lamp-post-sign: one frame.
- torch: unlit, then the four lit flame frames (16x48 pack px, 32x96).

The light pool itself is the state's PointLight2D (asset_state_light); the
art only shows the lantern glass or the flame alight.

From photographs of colonial and early American post lanterns: a hewn post
on a fieldstone footing, a tin-and-glass lantern with a pierced cone cap, a
candle inside.
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

# ---------------------------------------------------------------- colours

WOOD = F.WOOD
CUT = F.CUT
STONE = F.STONE
MORTAR = F.MORTAR
IRON = F.IRON
GLASS = ["161a20", "222830", "323a44", "48525e", "66727e"]
WAX = ["8c8270", "b4aa92", "d8d0b8", "eeead8"]
# Fire, dark to light: the flame's edge, its body, its core.
FIRE = ["6a1806", "a8320a", "d85a12", "f08a22", "f8b83a", "fcdc70", "fff6c8"]
SOOT = ["141210", "26201a", "3a3026", "524436"]
CLOTH = ["2a2018", "403226", "5a4834", "76624a"]
LINEN = ["6e624c", "948668", "b8a886", "d4c6a4"]


def palette():
    return WOOD + CUT + STONE + MORTAR + IRON + GLASS + WAX + FIRE + SOOT + CLOTH + LINEN


# ---------------------------------------------------------------- parts

def footing(d, rnd, cx, top, base, w):
    """A fieldstone footing the post stands in: rough courses, a little
    wider at the foot."""
    F.stone_wall(d, rnd, cx - w / 2, top, cx + w / 2, base, 0.5)


def foot_shadow(sd, cx, base, w):
    """The shadow pooled at the foot, a flat oval a little right of the
    footing (light from the upper left, as every painted piece)."""
    S = P.S
    sd.ellipse(((cx - w / 2 + 1.0) * S, (base - 1.6) * S, (cx + w / 2 + 4.0) * S, (base + 1.4) * S), fill=255)


def lantern(lay, d, cx, top, lit):
    """A tin-and-glass lantern, its ring at y=top, about 17 px tall: ring,
    pierced cone cap, a lip, four glass panes on iron corner posts (the
    front two seen), a base plate and a drip cone. Unlit: dark glass with
    the candle stub inside. Lit: the glass glows, brightest at the flame.
    Returns the lantern's bottom y."""
    S = P.S
    iron = lambda t: B.ramp(IRON, t)
    # The ring it hangs or is lifted by.
    d.ellipse(((cx - 1.3) * S, top * S, (cx + 1.3) * S, (top + 2.4) * S), outline=iron(0.55), width=max(1, int(0.55 * S)))
    # The cone cap, lit on its left, with a row of pierced holes.
    c0, c1 = top + 2.2, top + 6.2
    d.polygon([((cx - 1.2) * S, c0 * S), ((cx + 1.2) * S, c0 * S), ((cx + 4.4) * S, c1 * S), ((cx - 4.4) * S, c1 * S)],
              fill=iron(0.5))
    d.polygon([((cx - 1.2) * S, c0 * S), ((cx - 0.2) * S, c0 * S), ((cx - 1.6) * S, c1 * S), ((cx - 4.4) * S, c1 * S)],
              fill=iron(0.78))
    for k in range(-2, 3):
        hx = cx + k * 1.3
        hy = c0 + 2.4
        d.rectangle(((hx - 0.25) * S, (hy - 0.25) * S, (hx + 0.25) * S, (hy + 0.25) * S),
                    fill=B.ramp(FIRE, 0.7) if lit else iron(0.1))
    # The lip.
    d.rectangle(((cx - 4.8) * S, c1 * S, (cx + 4.8) * S, (c1 + 1.0) * S), fill=iron(0.4))
    d.rectangle(((cx - 4.8) * S, c1 * S, (cx + 4.8) * S, (c1 + 0.4) * S), fill=iron(0.7))
    # The glass.
    g0, g1 = c1 + 1.0, c1 + 9.0
    gx0, gx1 = cx - 3.8, cx + 3.8
    if lit:
        # Brightest at the candle flame, low in the middle; warmer and dimmer
        # toward the frame.
        # Drawn on its own layer and pasted inside the pane, so the glow
        # stays in the glass.
        glow = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
        gd = ImageDraw.Draw(glow)
        steps = 10
        for i in range(steps):
            f = i / steps
            r = 1.0 - f
            fx, fy = cx, g0 + 5.0
            gd.ellipse(((fx - 4.6 * r - 0.6) * S, (fy - 5.6 * r - 0.6) * S, (fx + 4.6 * r + 0.6) * S,
                        (fy + 4.4 * r + 0.6) * S), fill=B.ramp(FIRE, 0.45 + 0.55 * f))
        box = tuple(int(round(v * S)) for v in (gx0, g0, gx1, g1))
        lay.paste(glow.crop(box), box[:2])
    else:
        d.rectangle((gx0 * S, g0 * S, gx1 * S, g1 * S), fill=B.ramp(GLASS, 0.3))
        # The candle stub, cold.
        d.rectangle(((cx - 0.9) * S, (g1 - 3.6) * S, (cx + 0.9) * S, g1 * S), fill=B.ramp(WAX, 0.55))
        d.rectangle(((cx - 0.9) * S, (g1 - 3.6) * S, (cx - 0.3) * S, g1 * S), fill=B.ramp(WAX, 0.85))
        d.line([(cx * S, (g1 - 3.6) * S), (cx * S, (g1 - 4.4) * S)], fill=B.ramp(SOOT, 0.2), width=max(1, int(0.3 * S)))
        # A pale streak of sky on the glass.
        d.line([((gx0 + 1.0) * S, (g0 + 1.0) * S), ((gx0 + 1.0) * S, (g0 + 4.0) * S)], fill=B.ramp(GLASS, 0.9),
               width=max(1, int(0.4 * S)))
    if lit:
        # The candle and its flame, seen through the glass.
        d.rectangle(((cx - 0.9) * S, (g1 - 3.6) * S, (cx + 0.9) * S, g1 * S), fill=B.ramp(WAX, 0.9))
        d.polygon([((cx - 0.7) * S, (g1 - 3.6) * S), ((cx + 0.7) * S, (g1 - 3.6) * S), (cx * S, (g1 - 6.2) * S)],
                  fill=B.ramp(FIRE, 0.98))
    # The iron frame: corner posts and a middle bar.
    for x in (gx0, gx1):
        d.rectangle(((x - 0.5) * S, g0 * S, (x + 0.5) * S, g1 * S), fill=iron(0.45))
    d.rectangle(((cx - 0.3) * S, g0 * S, (cx + 0.3) * S, (g0 + 2.0) * S), fill=iron(0.45))
    # The base plate and drip cone.
    d.rectangle(((cx - 4.6) * S, g1 * S, (cx + 4.6) * S, (g1 + 1.2) * S), fill=iron(0.38))
    d.rectangle(((cx - 4.6) * S, g1 * S, (cx + 4.6) * S, (g1 + 0.4) * S), fill=iron(0.62))
    d.polygon([((cx - 2.8) * S, (g1 + 1.2) * S), ((cx + 2.8) * S, (g1 + 1.2) * S), (cx * S, (g1 + 3.2) * S)],
              fill=iron(0.3))
    return g1 + 3.2


def chain(d, x, y0, y1):
    """A short hanging chain of alternating links."""
    S = P.S
    y, k = y0, 0
    while y < y1 - 0.2:
        h = min(1.6, y1 - y)
        if k % 2 == 0:
            d.ellipse(((x - 0.6) * S, y * S, (x + 0.6) * S, (y + h) * S), outline=B.ramp(IRON, 0.6), width=max(1, int(0.4 * S)))
        else:
            d.rectangle(((x - 0.2) * S, y * S, (x + 0.2) * S, (y + h) * S), fill=B.ramp(IRON, 0.45))
        y += h - 0.2
        k += 1


def bar(d, x0, x1, y, t, seed, tone=0.5):
    """A hewn crossbar, lit on top, with pale cut ends."""
    S = P.S
    F.draw_rail(d, x0, y, x1, y, seed, 0, tone=tone, t=t)
    for x in (x0, x1):
        d.rectangle(((x - 0.4) * S, (y - t / 2) * S, (x + 0.4) * S, (y + t / 2) * S), fill=B.ramp(CUT, 0.5))


def brace(d, x0, y0, x1, y1, seed):
    """A curved knee brace from the post (x0, y0) up to the bar (x1, y1):
    a thick quarter-curve bowed in toward the corner, so a pair of them
    makes an arch under the bar, its upper edge lit."""
    S = P.S
    n = 10
    pts = []
    for i in range(n + 1):
        f = i / n
        a = f * math.pi / 2
        x = x0 + (x1 - x0) * (1 - math.cos(a))
        y = y1 + (y0 - y1) * (1 - math.sin(a))
        pts.append((x * S, y * S))
    d.line(pts, fill=B.ramp(WOOD, 0.45), width=int(1.4 * S))
    d.line([(px, py - 0.4 * S) for px, py in pts], fill=B.ramp(WOOD, 0.62), width=max(1, int(0.4 * S)))


def finish(lay, shade, glow=None):
    """plants.shrink, the foot shadow (a frame pixel 40% in shadow and not
    covered takes B.SHADOW), then the glow layer shrunk on its own with no
    outline or shadow (a flame has no dark rim and casts no shadow)."""
    out = F.shrink_shaded(lay, shade, palette())
    if glow is not None:
        small = glow.resize((P.FW, P.FH), Image.BOX)
        sp, op = small.load(), out.load()
        pal = [B.rgb(h)[:3] for h in palette()]
        for y in range(P.FH):
            for x in range(P.FW):
                if sp[x, y][3] >= 110:
                    op[x, y] = B.nearest(sp[x, y][:3], pal) + (255,)
    return out


# ---------------------------------------------------------------- lamp posts

def new_layers():
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    return lay, ImageDraw.Draw(lay), shade, ImageDraw.Draw(shade)


def paint_lamp_post(lit):
    """One lantern on a tall hewn post, the post standing in a fieldstone
    footing. Frame 48x80 pack px, feet at 0.9."""
    P.use_frame(48, 80, density=DENSITY)
    lay, d, shade, sd = new_layers()
    base, cx = 0.9 * 80, 24.0
    rnd = random.Random(401)
    foot_shadow(sd, cx, base, 11)
    F.draw_post(d, cx, 34.0, base - 8.0, 402, w=3.4)
    # A socket collar where the lantern sits on the post.
    S = P.S
    d.rectangle(((cx - 2.4) * S, 33.0 * S, (cx + 2.4) * S, 35.2 * S), fill=B.ramp(IRON, 0.4))
    d.rectangle(((cx - 2.4) * S, 33.0 * S, (cx + 2.4) * S, 33.6 * S), fill=B.ramp(IRON, 0.7))
    lantern(lay, d, cx, 15.0, lit)
    footing(d, rnd, cx, base - 9.0, base, 11.0)
    return finish(lay, shade)


def paint_lamp_post_2(lit):
    """Two lanterns hung on chains from the ends of a crossbar, curved knee
    braces under the bar. Frame 48x80 pack px, feet at 0.85."""
    P.use_frame(48, 80, density=DENSITY)
    lay, d, shade, sd = new_layers()
    base, cx = 0.85 * 80, 24.0
    rnd = random.Random(411)
    foot_shadow(sd, cx, base, 11)
    bar_y = 15.0
    F.draw_post(d, cx, bar_y - 3.0, base - 8.0, 412, w=3.4)
    brace(d, cx - 1.6, bar_y + 10.0, cx - 10.0, bar_y + 1.2, 413)
    brace(d, cx + 1.6, bar_y + 10.0, cx + 10.0, bar_y + 1.2, 414)
    bar(d, 3.0, 45.0, bar_y, 2.6, 415)
    for lx in (8.0, 40.0):
        chain(d, lx, bar_y + 1.3, bar_y + 6.0)
        lantern(lay, d, lx, bar_y + 5.6, lit)
    footing(d, rnd, cx, base - 9.0, base, 11.0)
    return finish(lay, shade)


def paint_lamp_post_sign():
    """A hewn post with a bracket arm and a plain plank board hung from it
    on two chains. Frame 48x80 pack px, feet at 0.9 (the post on the
    anchor, the board out to the right, as the pack art)."""
    P.use_frame(48, 80, density=DENSITY)
    lay, d, shade, sd = new_layers()
    S = P.S
    base, cx = 0.9 * 80, 24.0
    rnd = random.Random(421)
    foot_shadow(sd, cx, base, 11)
    arm_y = 18.0
    F.draw_post(d, cx, arm_y - 3.0, base - 8.0, 422, w=3.4)
    brace(d, cx + 1.6, arm_y + 11.0, cx + 11.0, arm_y + 1.2, 423)
    bar(d, cx, 46.0, arm_y, 2.4, 424)
    # The board: planks in a frame, hung from two chains.
    bx0, bx1, by0, by1 = 31.0, 45.0, arm_y + 6.0, arm_y + 22.0
    for x in (bx0 + 2.0, bx1 - 2.0):
        chain(d, x, arm_y + 1.2, by0 + 0.6)
    d.rectangle((bx0 * S, by0 * S, bx1 * S, by1 * S), fill=B.ramp(WOOD, 0.3))
    n = 4
    for k in range(n):
        x0 = bx0 + 1.0 + k * (bx1 - bx0 - 2.0) / n
        x1 = bx0 + 1.0 + (k + 1) * (bx1 - bx0 - 2.0) / n - 0.3
        tone = 0.55 + 0.08 * ((k * 7 + 3) % 3 - 1)
        d.rectangle((x0 * S, (by0 + 1.0) * S, x1 * S, (by1 - 1.0) * S), fill=B.ramp(WOOD, tone))
        d.rectangle((x0 * S, (by0 + 1.0) * S, (x0 + 0.6) * S, (by1 - 1.0) * S), fill=B.ramp(WOOD, tone + 0.15))
        gy = by0 + 3.0 + ((k * 5) % 7) * 1.4
        d.line([((x0 + 1.0) * S, gy * S), ((x0 + 1.0) * S, (gy + 2.4) * S)], fill=B.ramp(WOOD, tone - 0.2),
               width=max(1, int(0.25 * S)))
    # The frame's lit top edge and iron straps at the chains.
    d.rectangle((bx0 * S, by0 * S, bx1 * S, (by0 + 0.6) * S), fill=B.ramp(WOOD, 0.7))
    for x in (bx0 + 2.0, bx1 - 2.0):
        d.rectangle(((x - 0.5) * S, by0 * S, (x + 0.5) * S, (by0 + 2.4) * S), fill=B.ramp(IRON, 0.45))
    footing(d, rnd, cx, base - 9.0, base, 11.0)
    return finish(lay, shade)


# ---------------------------------------------------------------- torch

TORCH_FRAMES = 4


def flame(g, cx, bottom, k):
    """Flame frame k of TORCH_FRAMES: a tongue of fire rising from the torch
    head, swaying and stretching from frame to frame, its layers from the
    dark red edge in to a pale core, two side licks and a spark."""
    S = P.S
    ph = 2 * math.pi * k / TORCH_FRAMES
    height = 13.5 + 1.8 * math.sin(ph)
    sway = 1.6 * math.sin(ph + 1.1)

    def tongue(scale, tone, dy=0.0):
        pts = []
        n = 24
        for i in range(n + 1):
            f = i / n                      # 0 at the bottom, 1 at the tip
            y = bottom - dy - f * height * scale
            half = 3.0 * scale * math.sin(math.pi * min(1.0, f * 1.25 + 0.12)) * (1 - f) ** 0.35
            x = cx + sway * f * f
            pts.append(((x - half) * S, y * S, (x + half) * S))
        left = [(a, y) for a, y, _ in pts]
        right = [(b, y) for _, y, b in pts][::-1]
        g.polygon(left + right, fill=B.ramp(FIRE, tone))

    tongue(1.0, 0.25)
    tongue(0.82, 0.5, 0.3)
    tongue(0.6, 0.72, 0.6)
    tongue(0.36, 0.95, 0.8)
    # Side licks, alternating sides.
    for side, off in ((-1, 0.0), (1, math.pi)):
        a = 0.5 + 0.5 * math.sin(ph + off)
        if a > 0.35:
            lx = cx + side * (1.6 + 0.4 * a)
            ly = bottom - 2.0 - 2.5 * a
            g.polygon([((lx - 0.7) * S, (ly + 1.6) * S), ((lx + 0.7) * S, (ly + 1.6) * S),
                       ((lx + side * 0.9) * S, (ly - 1.6 * a) * S)], fill=B.ramp(FIRE, 0.4))
    # A spark above the tip, rising frame by frame.
    sy = bottom - height - 1.5 - 1.2 * k
    sx = cx + sway + (0.8 if k % 2 else -0.6)
    if sy > 0.6:
        g.rectangle(((sx - 0.35) * S, (sy - 0.35) * S, (sx + 0.35) * S, (sy + 0.35) * S), fill=B.ramp(FIRE, 0.8))


def paint_torch(k):
    """A torch: a stave pushed into the ground with a pitch-soaked cloth head
    bound with cord. k is None for unlit (a charred, cold head), else the
    flame frame. Frame 16x48 pack px, feet at 0.85."""
    P.use_frame(16, 48, density=DENSITY)
    S = P.S
    lay, d, shade, sd = new_layers()
    base, cx = 0.85 * 48, 8.0
    sd.ellipse(((cx - 1.2) * S, (base - 0.8) * S, (cx + 3.4) * S, (base + 0.8) * S), fill=255)
    # The stave, a little thicker toward the head, lit on its left.
    top = 21.0
    d.polygon([((cx - 0.7) * S, base * S), ((cx + 0.7) * S, base * S), ((cx + 0.9) * S, top * S), ((cx - 0.9) * S, top * S)],
              fill=B.ramp(WOOD, 0.5))
    d.polygon([((cx - 0.7) * S, base * S), ((cx - 0.2) * S, base * S), ((cx - 0.3) * S, top * S), ((cx - 0.9) * S, top * S)],
              fill=B.ramp(WOOD, 0.72))
    # Soil on the foot where it is pushed into the ground, and grain.
    d.rectangle(((cx - 0.75) * S, (base - 3.0) * S, (cx + 0.75) * S, base * S), fill=B.ramp(WOOD, 0.22))
    for gy in (top + 4.0, top + 9.5):
        d.line([((cx + 0.2) * S, gy * S), ((cx + 0.2) * S, (gy + 2.2) * S)], fill=B.ramp(WOOD, 0.3),
               width=max(1, int(0.25 * S)))
    # The head: wrapped linen, wider in the middle, cross-bound with cord;
    # scorched at the top, black and glowing when alight.
    h0, h1 = 15.0, 22.0
    lit = k is not None
    cols = SOOT if lit else LINEN
    d.polygon([((cx - 1.5) * S, h1 * S), ((cx + 1.5) * S, h1 * S), ((cx + 2.0) * S, (h0 + 3.0) * S),
               ((cx + 1.4) * S, h0 * S), ((cx - 1.4) * S, h0 * S), ((cx - 2.0) * S, (h0 + 3.0) * S)], fill=B.ramp(cols, 0.5))
    d.polygon([((cx - 1.5) * S, h1 * S), ((cx - 0.6) * S, h1 * S), ((cx - 1.0) * S, (h0 + 3.0) * S),
               ((cx - 0.8) * S, h0 * S), ((cx - 1.4) * S, h0 * S), ((cx - 2.0) * S, (h0 + 3.0) * S)], fill=B.ramp(cols, 0.85))
    for y in (h0 + 1.6, h0 + 3.6, h0 + 5.6):
        d.line([((cx - 1.9) * S, y * S), ((cx + 1.9) * S, (y + 1.0) * S)], fill=B.ramp(SOOT if lit else CLOTH, 0.3),
               width=max(1, int(0.4 * S)))
        d.line([((cx - 1.9) * S, (y + 1.0) * S), ((cx + 1.9) * S, y * S)], fill=B.ramp(SOOT if lit else CLOTH, 0.45),
               width=max(1, int(0.3 * S)))
    if not lit:
        d.polygon([((cx - 1.4) * S, h0 * S), ((cx + 1.4) * S, h0 * S), ((cx + 1.8) * S, (h0 + 1.8) * S),
                   ((cx - 1.8) * S, (h0 + 1.8) * S)], fill=B.ramp(SOOT, 0.4))
        out = finish(lay, shade)
    else:
        glow = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
        g = ImageDraw.Draw(glow)
        # Embers glowing in the head where the cloth burns.
        rnd = random.Random(431 + k)
        for _ in range(4):
            ex, ey = cx + rnd.uniform(-1.2, 1.2), h0 + rnd.uniform(0.5, 4.0)
            g.rectangle(((ex - 0.35) * S, (ey - 0.35) * S, (ex + 0.35) * S, (ey + 0.35) * S), fill=B.ramp(FIRE, 0.55))
        flame(g, cx, h0 + 1.5, k)
        out = finish(lay, shade, glow)
    return out


# ---------------------------------------------------------------- sheets

def row(frames):
    fw, fh = frames[0].size
    sheet = Image.new("RGBA", (fw * len(frames), fh), (0, 0, 0, 0))
    for i, f in enumerate(frames):
        sheet.paste(f, (i * fw, 0))
    return sheet


SHEETS = {
    "lamp-post": lambda: row([paint_lamp_post(False), paint_lamp_post(True)]),
    "lamp-post-2": lambda: row([paint_lamp_post_2(False), paint_lamp_post_2(True)]),
    "lamp-post-sign": lambda: row([paint_lamp_post_sign()]),
    "torch": lambda: row([paint_torch(None)] + [paint_torch(k) for k in range(TORCH_FRAMES)]),
}
# Frame size on the sheet (density 2) and feet, by sheet.
FRAMES = {"lamp-post": ((96, 160), 0.9), "lamp-post-2": ((96, 160), 0.85),
          "lamp-post-sign": ((96, 160), 0.9), "torch": ((32, 96), 0.85)}


# ---------------------------------------------------------------- check

def check(out_dir):
    """Each sheet is exactly what this script paints now, its frames are the
    size the catalog rects expect, and no frame has art on its outer rows or
    columns (a margin, so nothing is cut at the frame edge)."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun lamps.py)" % name)
            ok = False
            continue
        (fw, fh), _ = FRAMES[name]
        if sheet.height != fh or sheet.width % fw:
            print("%s: sheet %dx%d is not a row of %dx%d frames" % (name, sheet.width, sheet.height, fw, fh))
            ok = False
            continue
        for i in range(sheet.width // fw):
            frame = sheet.crop((i * fw, 0, (i + 1) * fw, fh))
            if any(F.edges_touched(frame)):
                print("%s frame %d: art on the frame edge" % (name, i))
                ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's lamps and torch.")
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
