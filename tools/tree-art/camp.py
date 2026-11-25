"""Paints salem's camp (LLM-968): the player's pitched camp (Tent, Camp Fire,
Camp Stool, Camp Woodpile) and the scenery set. Every pixel is drawn here,
no pack art.

    python tools/tree-art/camp.py
    python tools/tree-art/camp.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/camp.py --check     # margins, sizes, a fresh paint

Drawn at twice the pack art's size and shown at render_scale 1 (the same
size in the world, twice the detail).

THE TENT (tent.png: closed, open, packed; 128x128 frames, anchor 0.5/0.85
as the pack art). A linen wedge tent seen corner-on like the buildings: the
ridge runs back up-right, the entrance gable faces down-left, onto the
anchor tile, which is the tent's door (the engine walks the owner to it and
the camp layout puts the player's stand tile in front of it, camp.go). Two
poles hold the ridge, guy ropes run fore and aft to stakes, and each corner
is staked down. Closed, the entrance flaps are laced shut; open (someone
inside and awake), one flap is tied back on the dark inside. Packed (the
bundle on the ground while the camp goes up or comes down) is the canvas
rolled and roped, the poles beside it, on the anchor.

THE FIRE (camp-fire.png: unlit, burning x4, dying x4, ashes; 64x64 frames,
anchor 0.5/0.85): split logs laid in a ring of fieldstones. Burning: flames
over the logs with sparks rising; dying (the clock runs burning -> dying ->
ashes, camp.go UpdateCampFires): no flame, embers glowing, smoke curling up;
ashes: grey ash and charred ends in the ring.

THE SMALL THINGS (camp-small.png: stool, woodpile, sack; 32x32 frames) and
THE GEAR (camp-gear.png: bedroll rolled, bedroll laid out, spit with a fowl,
bare spit, tripod with kettle; 64x64 frames), anchor 0.5/0.85 as the pack
art: a three-legged stool, split firewood stacked three and two, a hemp
sack tied at the neck, a wool blanket rolled and strapped or laid out with
its roll for a pillow, two forked sticks with a spit and its crank, three
poles lashed over an iron kettle on a chain.

The scenery pieces share these sheets: Tent (Open) and Tent (Closed) the
tent's frames, Campfire the fire's (burning, unlit), Camp Firewood the
woodpile.

From period accounts of soldiers' and travellers' camps: linen or duck
wedge tents on wooden poles and stakes, fires in a ring of stones.
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
import lamps as LP

OUT_DIR = B.OUT_DIR
DENSITY = 2

WOOD = F.WOOD
CUT = F.CUT
STONE = F.STONE
LINEN = ["4e4636", "6a604c", "877c64", "a49880", "bfb49a", "d6ccb2", "e8e0c8"]
ROPE = ["4a3a22", "6e5a38", "94804e", "b8a46c"]
DARK = ["0e0c0a", "1a1612", "262018"]
FIRE = LP.FIRE
EMBER = ["3a1206", "6a1e08", "a8320a", "e0601a"]
ASH = ["2a2724", "403c38", "5a5550", "77716a", "948e86", "b2aca2"]
SMOKE = ["5e5a56", "7a7670", "96928c", "b2aea8"]


def scaled(pts):
    return [(x * P.S, y * P.S) for x, y in pts]


def lerp(a, b, t):
    return (a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t)


def new_layers():
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    return lay, ImageDraw.Draw(lay), shade, ImageDraw.Draw(shade)


# ---------------------------------------------------------------- the tent

# The angled view's ground axes, as the stalls: A runs from the near corner
# to the left one, BV from the near corner to the right one (pack px each).
A = (-1.0, -0.86)
BV = (1.0, -0.46)
NEAR = (37.0, 60.0)      # the tent's near ground corner, in the 64x64 frame
TW, TD = 22.0, 19.0      # across the gable (along A), along the ridge (BV)
RIDGE = 26.0             # ridge height


def tp(u, v, h=0.0):
    """A point of the tent: u across the entrance gable from the left corner
    (0) to the near one (TW), v back along the ridge from the front (0) to
    the back (TD), lifted h."""
    du = TW - u
    return (NEAR[0] + du * A[0] + v * BV[0], NEAR[1] + du * A[1] + v * BV[1] - h)


def stake(d, p, lean=0.6):
    """A wooden stake driven at ground point p, leaning back a little."""
    S = P.S
    x, y = p
    d.polygon(scaled([(x - 0.5, y), (x + 0.5, y), (x + 0.5 + lean, y - 2.6), (x - 0.3 + lean, y - 2.8)]),
              fill=B.ramp(WOOD, 0.55))
    d.line(scaled([(x - 0.5, y), (x - 0.3 + lean, y - 2.8)]), fill=B.ramp(WOOD, 0.75), width=max(1, int(0.3 * S)))


def rope(d, a, b, sag=0.6):
    """A rope from a to b, sagging a little."""
    m = lerp(a, b, 0.5)
    m = (m[0], m[1] + sag)
    d.line(scaled([a, m, b]), fill=B.ramp(ROPE, 0.55), width=max(1, int(0.35 * P.S)))


def tent_shadow(sd):
    g = [tp(0, 0), tp(TW, 0), tp(TW, TD), tp(0, TD)]
    sd.polygon(scaled([(x + 6.0, y + 1.6) for x, y in g]), fill=255)


def paint_tent(state, seed):
    """state: closed, open or packed."""
    P.use_frame(64, 64, density=DENSITY)
    S = P.S
    lay, d, shade, sd = new_layers()
    rnd = random.Random(seed)
    if state == "packed":
        return paint_packed(lay, d, shade, sd)
    tent_shadow(sd)
    front, back = tp(TW / 2, 0, RIDGE), tp(TW / 2, TD, RIDGE)
    # The back guy rope and stake, and the far corner's, behind the tent.
    bstake = tp(TW / 2, TD + 7)
    stake(d, bstake)
    rope(d, (back[0], back[1] - 1.0), (bstake[0], bstake[1] - 2.0), 1.2)
    stake(d, tp(-2.0, TD + 1.5))
    # The left slope, lit (it faces up and to the left), then the right
    # slope in shade, then the entrance gable.
    L, N, R, Fr = tp(0, 0), tp(TW, 0), tp(TW, TD), tp(0, TD)
    d.polygon(scaled([L, Fr, back, front]), fill=B.ramp(LINEN, 0.78))
    d.polygon(scaled([N, R, back, front]), fill=B.ramp(LINEN, 0.5))
    # Folds in the canvas: lines down each slope from the ridge.
    for k in range(1, 6):
        t = k / 6
        top = lerp(front, back, t)
        d.line(scaled([top, lerp(L, Fr, t)]), fill=B.ramp(LINEN, 0.64), width=max(1, int(0.3 * S)))
        d.line(scaled([top, lerp(N, R, t)]), fill=B.ramp(LINEN, 0.36), width=max(1, int(0.3 * S)))
    # The hem along the bottom edges, a darker band where the canvas meets
    # the ground.
    d.line(scaled([N, R]), fill=B.ramp(LINEN, 0.28), width=max(1, int(0.7 * S)))
    d.line(scaled([L, Fr]), fill=B.ramp(LINEN, 0.55), width=max(1, int(0.6 * S)))
    # The ridge seam.
    d.line(scaled([front, back]), fill=B.ramp(LINEN, 0.9), width=max(1, int(0.5 * S)))
    # The entrance gable.
    d.polygon(scaled([L, N, front]), fill=B.ramp(LINEN, 0.64))
    mid = lerp(L, N, 0.5)
    if state == "open":
        # One flap tied back to the left: the dark inside shows in the right
        # half of the doorway, the rolled flap a bulge on the left.
        d.polygon(scaled([mid, lerp(L, N, 0.9), lerp(front, N, 0.25)]), fill=B.ramp(DARK, 0.5))
        d.polygon(scaled([lerp(L, N, 0.25), mid, lerp(front, mid, 0.35)]), fill=B.ramp(DARK, 0.6))
        roll = lerp(lerp(L, N, 0.3), front, 0.45)
        d.ellipse(((roll[0] - 1.6) * S, (roll[1] - 3.2) * S, (roll[0] + 1.6) * S, (roll[1] + 3.2) * S),
                  fill=B.ramp(LINEN, 0.82))
        d.line(scaled([(roll[0] - 1.6, roll[1]), (roll[0] + 1.6, roll[1])]), fill=B.ramp(ROPE, 0.4),
               width=max(1, int(0.4 * S)))
    else:
        # The two flaps laced shut down the middle.
        d.line(scaled([front, mid]), fill=B.ramp(LINEN, 0.38), width=max(1, int(0.5 * S)))
        for k in range(1, 6):
            p = lerp(front, mid, k / 6)
            d.line(scaled([(p[0] - 0.7, p[1] - 0.3), (p[0] + 0.7, p[1] + 0.3)]), fill=B.ramp(ROPE, 0.5),
                   width=max(1, int(0.3 * S)))
    # Gable edges, lit along the left one.
    d.line(scaled([L, front]), fill=B.ramp(LINEN, 0.92), width=max(1, int(0.4 * S)))
    d.line(scaled([N, front]), fill=B.ramp(LINEN, 0.45), width=max(1, int(0.4 * S)))
    # The poles' tops above the ridge ends, the front pole's foot showing at
    # the doorway's top.
    for top in (front, back):
        d.line(scaled([(top[0], top[1] + 0.5), (top[0], top[1] - 2.2)]), fill=B.ramp(WOOD, 0.55),
               width=max(1, int(0.8 * S)))
    # The front guy rope to its stake, and the near corners' stakes.
    fstake = tp(TW / 2 - 1.0, -7.0)
    rope(d, (front[0], front[1] - 1.5), (fstake[0], fstake[1] - 2.0), 1.5)
    stake(d, fstake)
    for g, out in ((L, (-1.6, 0.6)), (N, (1.0, 1.6)), (R, (1.8, 0.2))):
        s = (g[0] + out[0], g[1] + out[1])
        rope(d, g, (s[0], s[1] - 2.0), 0.2)
        stake(d, s)
    return F.shrink_shaded(lay, shade, LINEN + ROPE + WOOD + CUT + DARK)


def paint_packed(lay, d, shade, sd):
    """The struck tent on the anchor: the canvas rolled and tied with rope,
    the two poles and the stakes lying beside it."""
    S = P.S
    cx, base = 32.0, 0.85 * 64
    sd.ellipse(((cx - 7) * S, (base - 1.2) * S, (cx + 10) * S, (base + 1.4) * S), fill=255)
    # The poles first, under the roll.
    for dy, x0, x1 in ((-0.8, cx - 9.0, cx + 8.0), (0.4, cx - 7.0, cx + 9.5)):
        d.line([(x0 * S, (base + dy) * S), (x1 * S, (base + dy - 1.2) * S)], fill=B.ramp(WOOD, 0.55),
               width=max(1, int(0.8 * S)))
    # The roll: a lying cylinder, its round end toward the viewer at the left.
    x0, x1, top = cx - 6.0, cx + 6.0, base - 5.0
    d.rectangle((x0 * S, top * S, x1 * S, (base - 0.5) * S), fill=B.ramp(LINEN, 0.55))
    d.rectangle((x0 * S, top * S, x1 * S, (top + 1.4) * S), fill=B.ramp(LINEN, 0.8))
    d.ellipse(((x0 - 1.6) * S, top * S, (x0 + 1.6) * S, (base - 0.5) * S), fill=B.ramp(LINEN, 0.7))
    d.ellipse(((x0 - 0.8) * S, (top + 1.4) * S, (x0 + 0.8) * S, (base - 2.0) * S), fill=B.ramp(LINEN, 0.45))
    for rx in (x0 + 3.0, x1 - 3.0):
        d.line([(rx * S, top * S), (rx * S, (base - 0.5) * S)], fill=B.ramp(ROPE, 0.45), width=max(1, int(0.6 * S)))
    return F.shrink_shaded(lay, shade, LINEN + ROPE + WOOD + CUT + DARK)


# ---------------------------------------------------------------- the fire

FIRE_FRAMES = 4


def ring(d, rnd, cx, cy, front):
    """The ring of fieldstones: the back half (front=False) or the front
    half, each stone a lumpy lit oval."""
    S = P.S
    n = 11
    for k in range(n):
        a = 2 * math.pi * (k + 0.5) / n
        if (math.sin(a) > 0) != front:
            continue
        x, y = cx + 9.0 * math.cos(a), cy + 4.2 * math.sin(a)
        w, h = rnd.uniform(2.6, 3.4), rnd.uniform(1.8, 2.4)
        tone = 0.45 + 0.15 * (-math.cos(a + 0.8)) + rnd.uniform(-0.08, 0.08)
        d.ellipse(((x - w / 2) * S, (y - h) * S, (x + w / 2) * S, (y + 0.4) * S), fill=B.ramp(STONE, tone))
        d.ellipse(((x - w / 2 + 0.4) * S, (y - h) * S, (x + w / 4) * S, (y - h + 0.9) * S), fill=B.ramp(STONE, tone + 0.25))


def logs(d, cx, cy, charred):
    """Split logs laid across each other in the ring; charred when burnt."""
    S = P.S
    for (x0, y0, x1, y1), tone in (((cx - 6, cy + 0.6, cx + 4, cy - 2.4), 0.55), ((cx - 4, cy - 2.2, cx + 6, cy + 0.8), 0.45),
                                   ((cx - 1, cy + 1.6, cx + 1.4, cy - 4.0), 0.62)):
        cols = ASH if charred else WOOD
        d.line([(x0 * S, y0 * S), (x1 * S, y1 * S)], fill=B.ramp(cols, tone + (0.1 if not charred else -0.2)), width=int(2.0 * S))
        d.line([(x0 * S, (y0 - 0.5) * S), (x1 * S, (y1 - 0.5) * S)], fill=B.ramp(cols, tone + 0.18), width=max(1, int(0.4 * S)))
        if not charred:
            # The split face at each log's near end.
            d.ellipse(((x0 - 0.8) * S, (y0 - 0.8) * S, (x0 + 0.8) * S, (y0 + 0.8) * S), fill=B.ramp(CUT, 0.6))


def paint_fire(state, k, seed):
    """state: unlit, burning, dying or ashes; k the frame for burning and
    dying."""
    P.use_frame(32, 32, density=DENSITY)
    S = P.S
    lay, d, shade, sd = new_layers()
    rnd = random.Random(seed)
    cx, cy = 16.0, 0.85 * 32 - 2.6
    sd.ellipse(((cx - 8) * S, (cy + 1.0) * S, (cx + 12) * S, (cy + 4.4) * S), fill=255)
    ring(d, random.Random(seed), cx, cy, front=False)
    if state == "ashes":
        d.ellipse(((cx - 6.5) * S, (cy - 2.6) * S, (cx + 6.5) * S, (cy + 2.6) * S), fill=B.ramp(ASH, 0.55))
        for _ in range(14):
            x, y = cx + rnd.uniform(-5.5, 5.5), cy + rnd.uniform(-1.8, 1.8)
            d.rectangle(((x - 0.4) * S, (y - 0.3) * S, (x + 0.4) * S, (y + 0.3) * S), fill=B.ramp(ASH, rnd.uniform(0.2, 0.9)))
        for x0, y0, x1, y1 in ((cx - 5, cy + 0.5, cx - 2, cy - 0.8), (cx + 2.5, cy - 1.2, cx + 5.5, cy + 0.4)):
            d.line([(x0 * S, y0 * S), (x1 * S, y1 * S)], fill=B.ramp(ASH, 0.12), width=int(1.3 * S))
    else:
        if state != "unlit":
            d.ellipse(((cx - 6) * S, (cy - 2.4) * S, (cx + 6) * S, (cy + 2.4) * S), fill=B.ramp(ASH, 0.35))
        logs(d, cx, cy, charred=state == "dying")
    ring(d, random.Random(seed), cx, cy, front=True)
    glow = None
    if state in ("burning", "dying"):
        glow = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
        g = ImageDraw.Draw(glow)
        er = random.Random(seed * 7 + k)
        for _ in range(9 if state == "burning" else 6):
            ex, ey = cx + er.uniform(-4.5, 4.5), cy + er.uniform(-1.6, 1.2)
            col = B.ramp(EMBER, er.uniform(0.5, 1.0) if state == "burning" else er.uniform(0.2, 0.7))
            g.rectangle(((ex - 0.45) * S, (ey - 0.45) * S, (ex + 0.45) * S, (ey + 0.45) * S), fill=col)
        if state == "burning":
            # Two tongues of fire, the torch's flame grown, out of step.
            LP.flame(g, cx - 1.6, cy, k)
            LP.flame(g, cx + 1.8, cy + 0.4, (k + 2) % FIRE_FRAMES)
        else:
            # Smoke: a curl rising and drifting right, a little higher each
            # frame.
            ph = 2 * math.pi * k / FIRE_FRAMES
            for j in range(12):
                t = j / 11
                # A broken wisp: every third puff left out, thinner and
                # paler as it rises.
                if (j + k) % 3 == 2:
                    continue
                x = cx + 0.5 + 1.8 * math.sin(ph + t * 4.5) + t * 2.5
                y = cy - 3.0 - t * 16.0
                r = 0.45 + t * 0.5
                g.ellipse(((x - r) * S, (y - r) * S, (x + r) * S, (y + r) * S), fill=B.ramp(SMOKE, 0.95 - 0.35 * t))
    out = F.shrink_shaded(lay, shade, STONE + WOOD + CUT + ASH)
    if glow is not None:
        small = glow.resize((P.FW, P.FH), Image.BOX)
        sp, op = small.load(), out.load()
        pal = [B.rgb(h)[:3] for h in FIRE + EMBER + SMOKE]
        for y in range(P.FH):
            for x in range(P.FW):
                if sp[x, y][3] >= 110:
                    op[x, y] = B.nearest(sp[x, y][:3], pal) + (255,)
    return out


# ---------------------------------------------------------------- small things

WOOL = ["2e2a26", "443e36", "5e564a", "786e5e", "928876", "aca290"]
LEATHER = ["2a1a10", "442a18", "5e3c22", "7a5030"]
HEMP = ["4a3e2a", "66583c", "847452", "a2926c", "bcae88", "d2c6a2"]
IRON = F.IRON
FOWL = ["4a2410", "6e3816", "92521e", "b0702c", "c8903e", "dcae5c"]


def small_frame(fw, fh):
    P.use_frame(fw, fh, density=DENSITY)
    return new_layers()


def foot_oval(sd, cx, base, rx, ry=1.2, dx=1.2):
    S = P.S
    sd.ellipse(((cx - rx + dx) * S, (base - ry) * S, (cx + rx + dx) * S, (base + ry) * S), fill=255)


def paint_stool():
    """A three-legged stool: a round seat of one board, its edge lit, and
    three splayed legs, the back one half hidden."""
    lay, d, shade, sd = small_frame(16, 16)
    S = P.S
    cx, base = 8.0, 0.85 * 16
    foot_oval(sd, cx, base, 4.2)
    seat_y = base - 6.0
    for fx, tone in ((cx + 0.6, 0.3), (cx - 3.4, 0.55), (cx + 3.6, 0.45)):
        d.line([((cx + (fx - cx) * 0.4) * S, (seat_y + 0.6) * S), (fx * S, (base - (0.8 if fx == cx + 0.6 else 0)) * S)],
               fill=B.ramp(WOOD, tone), width=int(0.9 * S))
    d.ellipse(((cx - 4.2) * S, (seat_y - 1.6) * S, (cx + 4.2) * S, (seat_y + 1.8) * S), fill=B.ramp(WOOD, 0.4))
    d.ellipse(((cx - 4.2) * S, (seat_y - 2.0) * S, (cx + 4.2) * S, (seat_y + 1.0) * S), fill=B.ramp(WOOD, 0.65))
    d.arc(((cx - 3.0) * S, (seat_y - 1.4) * S, (cx + 2.0) * S, (seat_y + 0.4) * S), 200, 320, fill=B.ramp(WOOD, 0.82),
          width=max(1, int(0.4 * S)))
    return F.shrink_shaded(lay, shade, WOOD + CUT)


def split_log(d, x0, y0, x1, y1, w, tone, face=True):
    """A split log lying from (x0, y0) to (x1, y1): bark on top, the pale
    split face at its near end (x0, y0)."""
    S = P.S
    d.line([(x0 * S, y0 * S), (x1 * S, y1 * S)], fill=B.ramp(WOOD, tone), width=int(w * S))
    d.line([(x0 * S, (y0 - w * 0.35) * S), (x1 * S, (y1 - w * 0.35) * S)], fill=B.ramp(WOOD, tone + 0.18),
           width=max(1, int(w * 0.3 * S)))
    if face:
        d.polygon([((x0 - w * 0.55) * S, (y0 + w * 0.45) * S), ((x0 + w * 0.55) * S, (y0 + w * 0.45) * S),
                   (x0 * S, (y0 - w * 0.55) * S)], fill=B.ramp(CUT, 0.7))
        d.line([((x0 - w * 0.2) * S, (y0 + w * 0.2) * S), ((x0 + w * 0.2) * S, (y0 + w * 0.2) * S)],
               fill=B.ramp(CUT, 0.35), width=max(1, int(0.25 * S)))


def paint_woodpile():
    """Split firewood stacked three and two, the split faces toward us."""
    lay, d, shade, sd = small_frame(16, 16)
    cx, base = 8.0, 0.85 * 16
    foot_oval(sd, cx, base, 5.6)
    for i, (x, y) in enumerate(((cx - 3.6, base - 1.2), (cx + 0.2, base - 1.0), (cx + 4.0, base - 1.3))):
        split_log(d, x, y, x + 2.6, y - 2.4, 2.4, 0.45 + 0.06 * (i % 2))
    for i, (x, y) in enumerate(((cx - 1.8, base - 3.4), (cx + 2.2, base - 3.5))):
        split_log(d, x, y, x + 2.6, y - 2.4, 2.4, 0.52 + 0.06 * i)
    return F.shrink_shaded(lay, shade, WOOD + CUT)


def paint_sack():
    """A hemp sack, full and slumped, its neck tied with cord."""
    lay, d, shade, sd = small_frame(16, 16)
    S = P.S
    cx, base = 8.0, 0.85 * 16
    foot_oval(sd, cx, base, 4.6)
    body = [(cx - 4.4, base), (cx + 4.6, base), (cx + 4.0, base - 4.0), (cx + 2.0, base - 6.6), (cx + 1.0, base - 7.4),
            (cx - 1.4, base - 7.2), (cx - 3.2, base - 5.6), (cx - 4.6, base - 2.6)]
    d.polygon(scaled(body), fill=B.ramp(HEMP, 0.5))
    d.polygon(scaled([(cx - 4.6, base - 2.6), (cx - 3.2, base - 5.6), (cx - 1.4, base - 7.2), (cx - 0.6, base - 4.0),
                      (cx - 2.4, base - 0.6)]), fill=B.ramp(HEMP, 0.72))
    # The weave: a few faint cross lines.
    for k in range(3):
        y = base - 1.6 - k * 1.8
        d.line(scaled([(cx - 3.6 + k * 0.4, y), (cx + 3.6 - k * 0.6, y)]), fill=B.ramp(HEMP, 0.4),
               width=max(1, int(0.25 * S)))
    # The tied neck and its ear of cloth.
    d.line(scaled([(cx - 1.6, base - 6.8), (cx + 1.4, base - 7.0)]), fill=B.ramp(ROPE, 0.35), width=max(1, int(0.6 * S)))
    d.polygon(scaled([(cx - 1.0, base - 7.2), (cx + 0.8, base - 7.4), (cx + 1.6, base - 9.0), (cx - 0.4, base - 9.4)]),
              fill=B.ramp(HEMP, 0.62))
    return F.shrink_shaded(lay, shade, HEMP + ROPE)


def paint_bedroll(open_):
    """A wool blanket: rolled tight and strapped (closed), or laid out on
    the ground with the roll at its head for a pillow (open)."""
    lay, d, shade, sd = small_frame(32, 32)
    S = P.S
    cx, base = 16.0, 0.85 * 32
    if open_:
        # The blanket laid flat on the ground, corner-on: a parallelogram,
        # a turned-back corner, a stripe woven near each end.
        q = [(cx - 11.0, base - 7.5), (cx + 1.0, base - 13.5), (cx + 11.0, base - 5.0), (cx - 1.0, base + 1.0)]
        sd.polygon(scaled([(x + 1.0, y + 1.0) for x, y in q]), fill=255)
        d.polygon(scaled(q), fill=B.ramp(WOOL, 0.6))
        for t in (0.12, 0.86):
            a, b = lerp(q[0], q[3], t), lerp(q[1], q[2], t)
            d.line(scaled([a, b]), fill=B.ramp(WOOL, 0.3), width=max(1, int(0.7 * S)))
        d.polygon(scaled([q[3], lerp(q[3], q[2], 0.35), lerp(q[3], q[0], 0.3)]), fill=B.ramp(WOOL, 0.82))
        # The pillow roll across the head end.
        h0, h1 = lerp(q[0], q[1], 0.15), lerp(q[0], q[1], 0.85)
        d.line(scaled([(h0[0] + 1.6, h0[1] + 0.6), (h1[0] + 1.6, h1[1] + 0.6)]), fill=B.ramp(WOOL, 0.42), width=int(2.6 * S))
        d.line(scaled([(h0[0] + 1.6, h0[1] - 0.3), (h1[0] + 1.6, h1[1] - 0.3)]), fill=B.ramp(WOOL, 0.72),
               width=max(1, int(0.8 * S)))
    else:
        foot_oval(sd, cx, base, 8.0, 1.6, 1.6)
        x0, x1, top = cx - 7.0, cx + 7.0, base - 6.0
        d.rectangle((x0 * S, top * S, x1 * S, (base - 0.4) * S), fill=B.ramp(WOOL, 0.5))
        d.rectangle((x0 * S, top * S, x1 * S, (top + 1.8) * S), fill=B.ramp(WOOL, 0.74))
        d.ellipse(((x0 - 2.0) * S, top * S, (x0 + 2.0) * S, (base - 0.4) * S), fill=B.ramp(WOOL, 0.62))
        d.arc(((x0 - 1.2) * S, (top + 1.0) * S, (x0 + 1.2) * S, (base - 1.4) * S), 0, 300, fill=B.ramp(WOOL, 0.32),
              width=max(1, int(0.4 * S)))
        for sx in (x0 + 3.5, x1 - 3.0):
            d.line([(sx * S, (top - 0.2) * S), (sx * S, (base - 0.2) * S)], fill=B.ramp(LEATHER, 0.45), width=int(0.9 * S))
            d.rectangle(((sx - 0.5) * S, (top + 1.2) * S, (sx + 0.5) * S, (top + 2.0) * S), fill=B.ramp(IRON, 0.6))
    return F.shrink_shaded(lay, shade, WOOL + LEATHER + IRON)


def forked_stick(d, x, base, top):
    """A green forked stick pushed into the ground, its fork at the top."""
    S = P.S
    d.line([(x * S, base * S), (x * S, (top + 1.4) * S)], fill=B.ramp(WOOD, 0.5), width=int(0.9 * S))
    d.line([(x * S, (top + 1.6) * S), ((x - 1.2) * S, top * S)], fill=B.ramp(WOOD, 0.55), width=max(1, int(0.7 * S)))
    d.line([(x * S, (top + 1.6) * S), ((x + 1.2) * S, top * S)], fill=B.ramp(WOOD, 0.45), width=max(1, int(0.7 * S)))


def paint_spit(roast):
    """Two forked sticks with a spit across them, a fowl roasting on it
    (roast) or the bare spit."""
    lay, d, shade, sd = small_frame(32, 32)
    S = P.S
    cx, base = 16.0, 0.85 * 32
    foot_oval(sd, cx, base, 9.0, 1.0, 1.0)
    lx, rx, fork = cx - 8.0, cx + 8.0, base - 12.0
    forked_stick(d, lx, base, fork)
    if roast:
        # The fowl, trussed on the spit: a browned body, the near drumstick.
        fx, fy = cx, fork + 2.4
        d.ellipse(((fx - 4.6) * S, (fy - 2.6) * S, (fx + 4.6) * S, (fy + 3.0) * S), fill=B.ramp(FOWL, 0.55))
        d.ellipse(((fx - 3.6) * S, (fy - 2.4) * S, (fx + 2.6) * S, (fy + 0.6) * S), fill=B.ramp(FOWL, 0.82))
        d.ellipse(((fx + 2.4) * S, (fy + 0.6) * S, (fx + 5.2) * S, (fy + 3.4) * S), fill=B.ramp(FOWL, 0.6))
        d.line([((fx + 4.6) * S, (fy + 2.8) * S), ((fx + 6.0) * S, (fy + 4.0) * S)], fill=B.ramp(CUT, 0.85),
               width=max(1, int(0.6 * S)))
    d.line([((lx - 1.0) * S, (fork + 0.6) * S), ((rx + 2.4) * S, (fork + 0.4) * S)], fill=B.ramp(WOOD, 0.4),
           width=max(1, int(0.7 * S)))
    # The spit's crank, bent at the right end.
    d.line([((rx + 2.4) * S, (fork + 0.4) * S), ((rx + 2.4) * S, (fork + 2.6) * S)], fill=B.ramp(WOOD, 0.4),
           width=max(1, int(0.6 * S)))
    forked_stick(d, rx, base, fork)
    return F.shrink_shaded(lay, shade, WOOD + CUT + FOWL)


def paint_tripod():
    """Three poles lashed at the top, an iron kettle hanging from a chain
    under the lashing."""
    lay, d, shade, sd = small_frame(32, 32)
    S = P.S
    cx, base = 16.0, 0.85 * 32
    top = (cx, base - 22.0)
    feet = [(cx + 1.0, base - 3.0), (cx - 8.0, base), (cx + 8.5, base - 0.4)]
    foot_oval(sd, cx, base - 1.0, 8.0, 1.4, 2.0)
    # The back leg first, then the kettle, then the two front legs.
    d.line(scaled([feet[0], (top[0] + 0.4, top[1] + 1.0)]), fill=B.ramp(WOOD, 0.35), width=int(0.9 * S))
    kx, ky = cx, base - 7.0
    d.line(scaled([(top[0], top[1] + 1.6), (kx, ky - 4.4)]), fill=B.ramp(IRON, 0.55), width=max(1, int(0.4 * S)))
    d.arc(((kx - 3.0) * S, (ky - 6.4) * S, (kx + 3.0) * S, (ky - 1.6) * S), 180, 360, fill=B.ramp(IRON, 0.6),
          width=max(1, int(0.4 * S)))
    d.ellipse(((kx - 3.8) * S, (ky - 3.8) * S, (kx + 3.8) * S, (ky + 2.4) * S), fill=B.ramp(IRON, 0.32))
    d.ellipse(((kx - 3.2) * S, (ky - 3.4) * S, (kx + 1.0) * S, (ky - 0.6) * S), fill=B.ramp(IRON, 0.55))
    d.ellipse(((kx - 3.6) * S, (ky - 4.4) * S, (kx + 3.6) * S, (ky - 2.6) * S), fill=B.ramp(IRON, 0.15))
    for f, tone in ((feet[1], 0.6), (feet[2], 0.45)):
        d.line(scaled([f, top]), fill=B.ramp(WOOD, tone), width=int(0.9 * S))
    # The lashing.
    d.ellipse(((top[0] - 1.2) * S, (top[1] + 0.2) * S, (top[0] + 1.2) * S, (top[1] + 2.4) * S), fill=B.ramp(ROPE, 0.5))
    return F.shrink_shaded(lay, shade, WOOD + IRON + ROPE)


# ---------------------------------------------------------------- sheets

def row(frames):
    fw, fh = frames[0].size
    sheet = Image.new("RGBA", (fw * len(frames), fh), (0, 0, 0, 0))
    for i, f in enumerate(frames):
        sheet.paste(f, (i * fw, 0))
    return sheet


SHEETS = {
    "tent": lambda: row([paint_tent(s, 801) for s in ("closed", "open", "packed")]),
    "camp-fire": lambda: row([paint_fire("unlit", 0, 811)]
                             + [paint_fire("burning", k, 811) for k in range(FIRE_FRAMES)]
                             + [paint_fire("dying", k, 811) for k in range(FIRE_FRAMES)]
                             + [paint_fire("ashes", 0, 811)]),
    "camp-small": lambda: row([paint_stool(), paint_woodpile(), paint_sack()]),
    "camp-gear": lambda: row([paint_bedroll(False), paint_bedroll(True), paint_spit(True), paint_spit(False),
                              paint_tripod()]),
}
FRAMES = {"tent": (128, 128), "camp-fire": (64, 64), "camp-small": (32, 32), "camp-gear": (64, 64)}


def check(out_dir):
    """Each sheet is exactly what this script paints now, a row of frames of
    its size, and no frame has art on its outer rows or columns."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun camp.py)" % name)
            ok = False
            continue
        fw, fh = FRAMES[name]
        if sheet.height != fh or sheet.width % fw:
            print("%s: sheet %dx%d is not a row of %dx%d frames" % (name, sheet.width, sheet.height, fw, fh))
            ok = False
            continue
        for i in range(sheet.width // fw):
            if any(F.edges_touched(sheet.crop((i * fw, 0, (i + 1) * fw, fh)))):
                print("%s frame %d: art on the frame edge" % (name, i))
                ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's camp.")
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
