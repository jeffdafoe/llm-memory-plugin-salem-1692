"""Paints salem's maples (LLM-967): every pixel is drawn here, no pack art.

    python tools/tree-art/build.py
    python tools/tree-art/build.py --check   # every frame on its anchor, inside its frame

Writes client/assets/trees/maple-1.png, -2, -3: three maples grown from
different seeds (VARIANTS), so no two neighbours share a fork. Each sheet has
one row per look, in LOOKS order, top to bottom; a row is the look's frames,
80x112 each, left to right. The leafy looks are a FRAMES-long loop at FPS
with one light-wind puff in it; the bare looks are one frame. The catalog's
Maple Tree, Maple Tree 2 and Maple Tree 3 states point at them (migration
LLM-967b-maple-variants). After a change, run `godot --headless --editor
--quit` in client/ so Godot writes the .import sidecars, and commit them.

How the tree is made, from photographs of sugar maples:
- The branches are grown by space colonization: target points fill an upright
  oval crown, and from the trunk top each node grows a step toward the targets
  nearest it, using them up as it reaches them. Limbs fork and reach outward
  at the crown's edge as real ones do. A limb's width follows the share of the
  twigs it carries, so the trunk is thickest and a twig is one pixel.
- Leafy looks hang pads of leaves along the fine twigs, each pad lit along
  its top with dark gaps under it. The light is even over the crown; autumn
  colour varies in patches, not as a gradient.
- Bare looks add short side twigs, so the outer crown reads as a haze.
- Painted at S times size, shrunk by area average, and every pixel snapped to
  the look's own colours (a median-cut reduction merged the crown's shade
  into the bark). A faint near-black outline, heavier along the bottom, keeps
  the crown off the grass. The shadow is the tree's own shape squashed onto
  the ground under it, pushed right: light comes from the upper left, as on
  the ground.
- The puff (iterated with Jeff, 2026-11-13; a whole-tree wind tunnel study
  and game practice agree that light-wind foliage motion is individual leaf
  flutter, not a front crossing the crown): small leaf clusters all over the
  crown turn to show paler undersides, each on its own beat; and the crown
  bends by leaf pads leaning one whole pixel, the top first and longest. The
  client starts every placed tree at a random frame and speed, so no two
  trees rustle together.

Rerunning writes the same files (every random draw comes from a fixed seed).
"""

import math
import os
import random
import sys

from PIL import Image, ImageDraw

ROOT = os.path.normpath(os.path.join(os.path.dirname(__file__), "..", ".."))
OUT_DIR = os.path.join(ROOT, "client", "assets", "trees")
# The maples: sheet name and seed. Seed 21 is the one Jeff first approved;
# 8 and 55 were picked from candidates as the least like it.
VARIANTS = [("maple-1", 21), ("maple-2", 8), ("maple-3", 55)]

S = 4                      # painting scale
FW, FH = 80, 112           # frame, the catalog's Maple Tree size
W, H = FW * S, FH * S
LIGHT = (-0.55, -0.83)     # from the upper left
ANCHOR_Y = 0.93            # the catalog anchor: the foot's row, as a share of FH
BASE_Y = H * ANCHOR_Y
CROWN = (W * 0.5, H * 0.37, W * 0.44, H * 0.34)   # upright oval: centre, radii
TRUNK_TOP = H * 0.72
MAX_GUST = 1.0     # frame px a leaf pad may lean in a puff

# The leafy looks' loop: FRAMES at FPS, still but for one puff at its end.
# PUFF is how many leaf clusters stir, frame by frame; LEAN how far the
# crown bends (times a pad's height, against the pad's own threshold).
FPS = 6
FRAMES = 36
PUFF = [0.35, 0.6, 0.85, 1.0, 1.0, 1.0, 0.85, 0.6, 0.35]
LEAN = [0.2, 0.45, 0.75, 1.0, 1.2, 0.9, 0.5, -0.15, -0.1]
PUFF_AT = FRAMES - len(PUFF)

# The catalog state each row serves, top to bottom. Every look is the same
# tree: one skeleton, one seed.
LOOKS = [
    "default",           # summer
    "autumn-clean",      # September: turning, green with yellow patches
    "autumn-leaves",     # October: full colour, leaves at the foot
    "autumn-bare",       # November: bare, leaves at the foot
    "winter-clean",      # January to March: bare
    "winter-leaves",     # December: bare, dry leaves at the foot
    "winter-snowy-top",  # a dusting: snow on the top twigs
    "winter-snowy",      # snow lying on the limbs and at the foot
]
LEAFY = ("default", "autumn-clean", "autumn-leaves")


def rgb(h, a=255):
    return (int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16), a)


def mix(c1, c2, t):
    t = max(0.0, min(1.0, t))
    return tuple(int(c1[i] + (c2[i] - c1[i]) * t) for i in range(3)) + (255,)


def ramp(cols, t):
    """A colour along a dark-to-light list of hex colours, t in 0..1."""
    t = max(0.0, min(0.999, t))
    f = t * (len(cols) - 1)
    i = int(f)
    return mix(rgb(cols[i]), rgb(cols[i + 1]), f - i)


# ---------------------------------------------------------------- colours

BARK = ["241a14", "3e2e24", "5a4434", "7a5e46", "9c8060"]
# Leaf ramps, dark to light. Each leafy look mixes two in patches.
GREEN = ["1a3018", "28482a", "386430", "4c8236", "66a03e", "8cbc50", "b6d86c"]
GREEN_WARM = ["243414", "3a5420", "527428", "6c9230", "8aae3a", "acc850", "d0e070"]
YELLOW = ["4a300c", "7a5210", "aa7a16", "d4a424", "eec840", "f8e270"]
RED = ["4a120c", "7a2210", "aa3a14", "d45a1c", "ec842c", "f6ac48"]
# Fallen leaves: the ground's autumn leaf colours (terrain-art LEAVES), and
# the browner dry leaves of December.
LEAVES = ["c8702a", "d99a3a", "a8452a", "b8862e"]
DRY_LEAVES = ["6e5234", "8a6a42", "a08454"]
SNOW = ["c9d4de", "e2e9ef", "f4f8fb"]
OUTLINE = (16, 18, 14, 255)
SHADOW = (18, 22, 14, 110)


# ---------------------------------------------------------------- noise

def value_noise(seed, cells):
    """Smooth value noise over the canvas, `cells` lattice cells across: the
    patches of autumn colour, and a little hue drift in summer."""
    rnd = random.Random(seed)
    lat = [[rnd.random() for _ in range(cells + 2)] for _ in range(cells + 2)]

    def at(x, y):
        fx, fy = x / W * cells, y / H * cells
        ix, iy = int(fx), int(fy)
        tx, ty = fx - ix, fy - iy
        tx, ty = tx * tx * (3 - 2 * tx), ty * ty * (3 - 2 * ty)
        a = lat[iy][ix] + (lat[iy][ix + 1] - lat[iy][ix]) * tx
        b = lat[iy + 1][ix] + (lat[iy + 1][ix + 1] - lat[iy + 1][ix]) * tx
        return a + (b - a) * ty
    return at


# ---------------------------------------------------------------- skeleton

class Branch:
    def __init__(self, x0, y0, x1, y1, w0, w1, depth):
        self.x0, self.y0, self.x1, self.y1 = x0, y0, x1, y1
        self.w0, self.w1, self.depth = w0, w1, depth


class Grid:
    """Nodes bucketed by cell, so 'the nearest node within reach' looks at
    nine cells instead of every node."""

    def __init__(self, cell):
        self.cell = cell
        self.cells = {}

    def key(self, x, y):
        return int(x // self.cell), int(y // self.cell)

    def add(self, i, x, y):
        self.cells.setdefault(self.key(x, y), []).append(i)

    def near(self, x, y):
        kx, ky = self.key(x, y)
        for dx in (-1, 0, 1):
            for dy in (-1, 0, 1):
                for i in self.cells.get((kx + dx, ky + dy), ()):
                    yield i


def colonize(seed, n_targets, bias=0.22):
    """Space colonization. Returns (branches, twig points for leaf pads)."""
    rnd = random.Random(seed)
    cx, cy, rx, ry = CROWN
    targets = []
    while len(targets) < n_targets:
        a = rnd.uniform(0, math.tau)
        r = math.sqrt(rnd.random())
        x, y = cx + math.cos(a) * rx * r, cy + math.sin(a) * ry * r
        if y < TRUNK_TOP - 6 * S:
            targets.append((x, y))
    step, reach, kill = 2.4 * S, 13 * S, 3.6 * S
    # the trunk: a short chain of nodes from the foot to the trunk top
    nodes, parent = [], []
    grid = Grid(reach)
    for k in range(6):
        t = k / 5
        nodes.append((W * 0.5 + 1 * S * t, BASE_Y + (TRUNK_TOP - BASE_Y) * t))
        parent.append(k - 1)
        grid.add(k, *nodes[-1])
    trunk_n = len(nodes)

    def nearest(x, y):
        best, best_d = -1, None
        for i in grid.near(x, y):
            d = math.hypot(nodes[i][0] - x, nodes[i][1] - y)
            if best_d is None or d < best_d or (d == best_d and i < best):
                best, best_d = i, d
        return best, best_d

    stall = 0
    for _ in range(700):
        if not targets:
            break
        # each target pulls the node nearest it, if within reach
        pull = {}
        for tx, ty in targets:
            ni, d = nearest(tx, ty)
            if ni < 0 or d >= reach or ni < trunk_n - 1:
                continue          # only the trunk top branches, not the bole
            vx, vy = tx - nodes[ni][0], ty - nodes[ni][1]
            n = math.hypot(vx, vy) + 1e-6
            px, py = pull.get(ni, (0.0, 0.0))
            pull[ni] = (px + vx / n, py + vy / n)
        grew = False
        for ni, (vx, vy) in pull.items():
            n = math.hypot(vx, vy) + 1e-6
            # a little upward lean and a little wander, as maple limbs climb
            vx, vy = vx / n + rnd.uniform(-0.12, 0.12), vy / n - bias
            n = math.hypot(vx, vy) + 1e-6
            x, y = nodes[ni][0] + vx / n * step, nodes[ni][1] + vy / n * step
            nodes.append((x, y))
            parent.append(ni)
            grid.add(len(nodes) - 1, x, y)
            grew = True
        targets = [(tx, ty) for tx, ty in targets if nearest(tx, ty)[1] is None or nearest(tx, ty)[1] > kill]
        stall = 0 if grew else stall + 1
        if stall > 3:
            break
    kids = [[] for _ in nodes]
    for i, p in enumerate(parent):
        if p >= 0:
            kids[p].append(i)
    # Width grows with the share of the twigs a node carries, slowly enough
    # that a main limb is about a third of the trunk and a twig is one pixel.
    carried = [0] * len(nodes)
    for i in range(len(nodes) - 1, -1, -1):
        carried[i] = 1 if not kids[i] else sum(carried[k] for k in kids[i])
    w = [max(1.0 * S, 14 * S * (carried[i] / carried[0]) ** 0.8) for i in range(len(nodes))]
    branches, twigs = [], []
    for i, p in enumerate(parent):
        if p < 0:
            continue
        depth = 0 if i < trunk_n else (1 if w[i] > 6 * S else (2 if w[i] > 2.5 * S else 4))
        branches.append(Branch(nodes[p][0], nodes[p][1], nodes[i][0], nodes[i][1], w[p] if depth == 0 else w[i], w[i], depth))
        if w[i] <= 1.6 * S:
            twigs.append(nodes[i])
    return branches, twigs


# ---------------------------------------------------------------- bark

def draw_branch(d, b):
    """A limb in bands across its width, lit on the upper-left side; a twig
    is one dark stroke."""
    dx, dy = b.x1 - b.x0, b.y1 - b.y0
    L = math.hypot(dx, dy) or 1
    nx, ny = -dy / L, dx / L
    lit_side = 1 if (nx * LIGHT[0] + ny * LIGHT[1]) > 0 else -1
    if b.w0 < 2.2 * S:
        d.line([(b.x0, b.y0), (b.x1, b.y1)], fill=ramp(BARK, 0.25), width=max(2, int(b.w0)))
        return
    steps = 4
    for i in range(steps):
        t0, t1 = i / steps, (i + 1) / steps

        def e(t, w):
            return (t - 0.5) * w * lit_side
        pts = [(b.x0 + nx * e(t0, b.w0), b.y0 + ny * e(t0, b.w0)),
               (b.x0 + nx * e(t1, b.w0), b.y0 + ny * e(t1, b.w0)),
               (b.x1 + nx * e(t1, b.w1), b.y1 + ny * e(t1, b.w1)),
               (b.x1 + nx * e(t0, b.w1), b.y1 + ny * e(t0, b.w1))]
        d.polygon(pts, fill=ramp(BARK, 0.12 + 0.8 * (t0 + t1) / 2))
    r = b.w1 / 2
    d.ellipse((b.x1 - r, b.y1 - r, b.x1 + r, b.y1 + r), fill=ramp(BARK, 0.45))


def draw_twig_haze(d, rnd, branches):
    """Short side twigs off the thin branches, forking once, in a greyer bark,
    so a bare outer crown reads as a haze rather than black lines."""
    for b in branches:
        if b.w1 > 2.5 * S or b.depth == 0:
            continue
        base = math.atan2(b.x1 - b.x0, -(b.y1 - b.y0))      # 0 = straight up
        for _ in range(2):
            if rnd.random() < 0.45:
                continue
            a = base + rnd.choice((-1, 1)) * rnd.uniform(0.45, 0.9)
            L = rnd.uniform(2.5, 5.5) * S
            x1, y1 = b.x1 + math.sin(a) * L, b.y1 - math.cos(a) * L
            col = ramp(BARK, rnd.uniform(0.35, 0.6))
            # A twig that would cross the frame's side or top is not drawn
            # (its draws are still taken, so the rest of the haze is the same).
            inside = lambda px, py: 1.5 * S <= px <= W - 1.5 * S and py >= 1.5 * S
            if inside(x1, y1):
                d.line([(b.x1, b.y1), (x1, y1)], fill=col, width=max(2, int(0.9 * S)))
            for s2 in (-1, 1):
                if rnd.random() < 0.5:
                    a2 = a + s2 * rnd.uniform(0.35, 0.6)
                    L2 = L * rnd.uniform(0.4, 0.7)
                    x2, y2 = x1 + math.sin(a2) * L2, y1 - math.cos(a2) * L2
                    if inside(x1, y1) and inside(x2, y2):
                        d.line([(x1, y1), (x2, y2)], fill=col, width=max(2, int(0.8 * S)))


def draw_snow_on(d, b, scale=1.0):
    """Snow along the top of a limb, wider on a level limb than a steep one."""
    dx, dy = b.x1 - b.x0, b.y1 - b.y0
    L = math.hypot(dx, dy) or 1
    level = 1 - abs(dy) / L
    if b.depth == 0 or level < 0.08:
        return
    w = max(1.3 * S, (b.w0 + b.w1) * 0.35 * (0.6 + level)) * scale
    d.line([(b.x0, b.y0 - b.w0 * 0.45), (b.x1, b.y1 - b.w1 * 0.45)], fill=rgb("f2f6f9"), width=max(2, int(w)))


def draw_roots(d, trunk):
    bx, by = trunk.x0, trunk.y0
    hw = trunk.w0 / 2
    for sx, reach, drop, t in ((-1, 1.7, 1.0, 0.72), (1, 1.55, 1.2, 0.3), (-0.45, 1.05, 2.2, 0.58), (0.5, 1.1, 2.4, 0.24)):
        root = (bx + sx * hw * 0.55, by - 7 * S)
        tip = (bx + sx * hw * reach, by + drop * S)
        knee = (bx + sx * hw * (reach * 0.8), by - 1.5 * S)
        d.polygon([(root[0] - 3.2 * S, root[1]), (root[0] + 3.2 * S, root[1]), knee, tip,
                   (tip[0] - sx * 3 * S, tip[1] + 0.6 * S)], fill=ramp(BARK, t))


def draw_furrows(d, rnd, trunk):
    for _ in range(14):
        t = rnd.uniform(-0.4, 0.4)
        d.line([(trunk.x0 + t * trunk.w0, trunk.y0 - rnd.uniform(1, 5) * S),
                (trunk.x1 + t * trunk.w1, trunk.y1 + rnd.uniform(2, 10) * S)],
               fill=ramp(BARK, 0.04 if t > -0.1 else 0.3), width=S)


# ---------------------------------------------------------------- foliage

def pad_points(rnd, twigs):
    """Pads hang along the fine twigs, which fill the crown: a sample of them,
    spread so no two pads sit on top of each other."""
    pts = []
    for x, y in sorted(twigs, key=lambda p: rnd.random()):
        if all((x - px) ** 2 + (y - py) ** 2 > (4.2 * S) ** 2 for px, py in pts):
            pts.append((x + rnd.uniform(-1, 1) * S, y + rnd.uniform(-1, 1) * S))
    return pts


def draw_pad(d, rnd, x, y, ramp_a, ramp_b, hue, light, still_x=None, flip=None, pad=0):
    """A pad of leaves, wider than tall: a dark underside, leaf marks lit from
    the top (each with its own small shadow), ragged leaves at its rim."""
    rx = rnd.uniform(6.5, 9.5) * S
    ry = rx * rnd.uniform(0.5, 0.62)
    # Keep the pad, and the ragged leaves at its rim, a pixel inside the
    # frame: a crown cut by the frame edge shows a flat side. Measured from
    # the pad's still position, so a gust moves the pad without changing its
    # size or any random draw after it.
    sx = x if still_x is None else still_x
    room = min(sx, W - sx) - 1 * S - 2.7 * S - MAX_GUST * S
    rx = max(2 * S, min(rx, room / 1.05))
    n = int(rx * ry / (S * S) * 0.55)
    # And a pixel below the frame's top: the pad's leaf marks reach up about
    # 1.15 ry plus a leaf. Scaled after n is counted, so a pad squeezed
    # under the top takes the same number of draws and no later pad changes.
    room_up = y - 1 * S - 2.7 * S
    if ry * 1.15 > room_up:
        squeeze = max(2 * S, room_up / 1.15) / ry
        rx, ry = rx * squeeze, ry * squeeze
        # a pad on a twig too near the top for even its smallest size sits
        # a little lower instead
        y = max(y, 1 * S + 2.7 * S + ry * 1.15)

    def tone(v):
        return mix(ramp(ramp_a, v), ramp(ramp_b, v), hue)
    d.ellipse((x - rx, y - ry * 0.6, x + rx, y + ry * 1.25), fill=tone(light - 0.4))
    marks = []
    for _ in range(n):
        a = rnd.uniform(0, math.tau)
        rr = math.sqrt(rnd.random())
        px, py = x + math.cos(a) * rx * rr, y + math.sin(a) * ry * rr - ry * 0.15
        top = -(py - y) / ry                      # +1 at the top of the pad
        v = light + top * 0.22 + rnd.uniform(-0.12, 0.12)
        marks.append((py, px, v, rnd.uniform(0, math.tau), rnd.uniform(1.8, 2.6) * S, int(a / (math.tau / 3))))
    for py, px, v, ang, s, sector in sorted(marks):
        pts = [(px + math.cos(ang + k * 2.2) * s * (1 if k == 0 else 0.7),
                py + math.sin(ang + k * 2.2) * s * (0.8 if k == 0 else 0.6)) for k in range(3)]
        d.polygon([(qx + 0.5 * S, qy + 0.8 * S) for qx, qy in pts], fill=tone(v - 0.3))
        under = flip(pad, sector, px, py) if flip is not None else None
        d.polygon(pts, fill=under if under is not None else tone(v))
    for _ in range(5):
        a = rnd.uniform(math.pi * 1.05, math.pi * 1.95) if rnd.random() < 0.7 else rnd.uniform(0, math.pi)
        px, py = x + math.cos(a) * rx * 1.05, y + math.sin(a) * ry * 1.05
        s = rnd.uniform(1.5, 2.2) * S
        d.polygon([(px + math.cos(a) * s * 1.2, py + math.sin(a) * s * 1.2),
                   (px + math.cos(a + 1.8) * s * 0.6, py + math.sin(a + 1.8) * s * 0.6),
                   (px + math.cos(a - 1.8) * s * 0.6, py + math.sin(a - 1.8) * s * 0.6)],
                  fill=tone(light + (0.1 if math.sin(a) < 0 else -0.15)))


def crown_ramps(look):
    """The two leaf ramps a leafy look mixes, and how its patches mix them."""
    if look == "default":
        return GREEN, GREEN_WARM, lambda p: p * 0.6
    if look == "autumn-clean":
        # September: mostly green, a third of the crown already turning
        return GREEN, YELLOW, lambda p: max(0.0, min(1.0, (p - 0.55) * 2.5))
    return YELLOW, RED, lambda p: p


# Leaf undersides: paler than the tops. In a light wind leaves turn over and
# flash these, a small cluster at a time (flipper).
UNDERSIDE = {"default": ["7ea46a", "93b47c", "a6c38e"],
             "autumn-clean": ["93b07a", "c8bf72", "d8cf8a"],
             "autumn-leaves": ["e0a85a", "e8bc72", "eecb8c"]}


def _hash(*parts):
    h = 2166136261
    for p in parts:
        h = ((h ^ (int(p) & 0xFFFFFFFF)) * 16777619) & 0xFFFFFFFF
    return h


def flipper(gust, look, seed):
    """Which leaf marks show their undersides in this frame. In a light wind
    each small cluster of leaves flutters on its own (a whole-tree wind
    tunnel study: low-speed foliage motion is individual leaf flutter;
    Crytek gives every leaf its own phase), so there is no front crossing
    the crown. A cluster is a third of a pad (by the mark's angle in it).
    Each cluster has its own period (4-6 frames, about a second) and phase,
    and shows its undersides for one frame of its period; only some
    clusters are stirring at all, more as the puff strengthens, and only
    part of a turned cluster turns. gust = (frame within the puff,
    strength 0..1). Hashes, not the crown's generator, so the crown's own
    draws are the same in every frame."""
    t, strength = gust
    cols = [rgb(h) for h in UNDERSIDE[look]]

    def flip(pad, sector, px, py):
        h = _hash(seed, pad, sector, 17)
        if (h & 0xFFFF) / 65535.0 > strength * 0.5:
            return None                          # this cluster is not stirring
        period = 4 + (h >> 16) % 3
        phase = (h >> 20) % period
        if (t + phase) % period != 0:
            return None
        m = _hash(int(px), int(py), 31)
        if (m & 0xFF) > 150:
            return None                          # not every leaf in it turns
        return cols[(m >> 8) % len(cols)]
    return flip


def draw_crown(body, seed, branches, twigs, look, gust=None):
    """Deep foliage inside the crown, the limbs showing through it, then the
    pads, higher first so lower pads overlap them."""
    rnd = random.Random(seed)
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    ramp_a, ramp_b, hue_of = crown_ramps(look)
    patches = value_noise(seed + 9, 4)
    cx, cy, rx, ry = CROWN
    d.ellipse((cx - rx * 0.7, cy - ry * 0.7, cx + rx * 0.7, cy + ry * 0.75), fill=ramp(ramp_a, 0.1))
    for b in sorted(branches, key=lambda b: b.w1):
        if b.depth >= 1 and b.w1 > 2 * S:
            draw_branch(d, b)
    for k, (x, y) in enumerate(sorted(pad_points(rnd, twigs), key=lambda p: p[1])):
        gx, gy = (x - cx) / rx, (y - cy) / ry
        # even light: a gentle lift toward the upper left, the lowest pads a
        # little darker where the crown shades itself
        light = 0.62 - (gx * LIGHT[0] + gy * LIGHT[1]) * 0.08 - max(0.0, gy - 0.6) * 0.25 + rnd.uniform(-0.05, 0.05)
        # The crown bends: leaf pads lean one whole frame pixel (exactly S
        # canvas px, so a pad's texture survives the shrink unchanged), the
        # way pixel artists move balls of leaves. A pad leans once the lean
        # times its height in the crown passes its own threshold, so the
        # top leans first and longest, pads lower down join only at the
        # peak, and the line between moved and still is the ragged edge of
        # the clusters, never a row.
        dx = 0.0
        if gust is not None:
            h = max(0.0, min(1.0, (cy + ry - y) / (2 * ry)))
            threshold = 0.35 + 0.55 * (_hash(seed, k, 5) & 0xFFFF) / 65535.0
            if gust[2] * h >= threshold:
                dx = S
        draw_pad(d, rnd, x + dx, y, ramp_a, ramp_b, hue_of(patches(x, y)), light, still_x=x,
                 flip=None if gust is None else flipper(gust[:2], look, seed), pad=k)
    return Image.alpha_composite(body, lay), lay.split()[3], (ramp_a, ramp_b)


# ---------------------------------------------------------------- ground bits

def foot_leaves(d, rnd, count, colours):
    for _ in range(count):
        x = W * 0.5 + rnd.gauss(0, 12 * S)
        y = BASE_Y + rnd.gauss(0.5, 1.8) * S
        r = rnd.uniform(2.6, 3.8) * S
        x = max(r + S, min(W - r - S, x))      # inside the frame
        a = rnd.uniform(-0.5, 0.5)
        pts = [(x + math.cos(a) * r, y + math.sin(a) * r * 0.55), (x + math.cos(a + 1.7) * r * 0.45, y + math.sin(a + 1.7) * r * 0.3),
               (x - math.cos(a) * r, y - math.sin(a) * r * 0.55), (x + math.cos(a - 1.7) * r * 0.45, y + math.sin(a - 1.7) * r * 0.3)]
        d.polygon(pts, fill=rgb(rnd.choice(colours)))


def cast_shadow(body):
    """The tree's alpha squashed to about a quarter of its height, centred a
    little above the foot, the higher parts pushed right: a solid patch under
    a leafy crown, a hatch of branches under a bare one."""
    alpha = body.split()[3]
    y0 = BASE_Y - 6 * S          # where the crown centre's shadow lands
    sq, lean, dx = 0.26, 0.1, 7 * S
    # source y = cy + (y - y0) / sq ; source x = x - dx - lean * (BASE_Y - source y)
    e, f = 1 / sq, CROWN[1] - y0 / sq
    return alpha.transform((W, H), Image.AFFINE, (1, lean * e, -dx - lean * BASE_Y + lean * f, 0, e, f),
                           resample=Image.BILINEAR)


# ---------------------------------------------------------------- one look

def paint(look, seed, branches, twigs, gust=None):
    """One frame of one look. gust = (frame within the puff, how many leaf
    clusters stir 0..1, how far the crown leans), or None for the still
    tree."""
    leafy = look in LEAFY
    body = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(body)
    trunk = Branch(W * 0.5, BASE_Y, W * 0.5 + 1 * S, TRUNK_TOP, 14 * S, 11 * S, 0)
    if not leafy:
        draw_twig_haze(d, random.Random(seed + 7), branches)
        for b in sorted(branches, key=lambda b: b.w1):   # thin first: limbs over twigs
            if b.depth > 0:
                draw_branch(d, b)
    draw_branch(d, trunk)
    draw_furrows(d, random.Random(seed + 5), trunk)
    draw_roots(d, trunk)
    if look == "winter-snowy":
        for b in branches:
            draw_snow_on(d, b)
    if look == "winter-snowy-top":
        # a dusting settles only on the upper crown, thinly
        for b in branches:
            if min(b.y0, b.y1) < CROWN[1]:
                draw_snow_on(d, b, 0.6)
    crown_mask, ramps = None, None
    if leafy:
        body, crown_mask, ramps = draw_crown(body, seed + 1, branches, twigs, look, gust)
        d = ImageDraw.Draw(body)
    shadow = cast_shadow(body)
    if look in ("autumn-leaves", "autumn-bare"):
        foot_leaves(d, random.Random(seed + 3), 22 if look == "autumn-leaves" else 14, LEAVES)
    if look == "winter-leaves":
        foot_leaves(d, random.Random(seed + 4), 16, DRY_LEAVES)
    if look == "winter-snowy":
        d.ellipse((W * 0.5 - 15 * S, BASE_Y - 3 * S, W * 0.5 + 17 * S, BASE_Y + 3.5 * S), fill=rgb("eef3f7"))
    return body, shadow, crown_mask, ramps


def nearest(c, pal):
    # weighted RGB distance: the eye is most sensitive to green, least to blue
    return min(pal, key=lambda p: 2 * (p[0] - c[0]) ** 2 + 4 * (p[1] - c[1]) ** 2 + 3 * (p[2] - c[2]) ** 2)


def shrink(look, body, shadow, crown_mask, ramps):
    """To frame size: area average, snap to the look's colours, outline, and
    the shadow under it all at SHADOW's alpha."""
    small = body.resize((FW, FH), Image.BOX)
    sh = shadow.resize((FW, FH), Image.BOX).load()
    cm = crown_mask.resize((FW, FH), Image.BOX).load() if crown_mask is not None else None
    sp = small.load()
    covered = {(x, y): sp[x, y][:3] for y in range(FH) for x in range(FW) if sp[x, y][3] >= 128}
    # A faint near-black outline all round, stronger where the edge faces
    # down, with a second fainter row under the crown. Twigs (few covered
    # neighbours) keep their colour, or a bare tree turns to black wire.
    outline = {}
    for (x, y) in covered:
        if sum((x + dx, y + dy) in covered for dx in (-1, 0, 1) for dy in (-1, 0, 1)) < 6:
            continue
        if (x, y + 1) not in covered:
            outline[(x, y)] = 0.72
        elif any((x + dx, y + dy) not in covered for dx, dy in ((1, 0), (-1, 0), (0, -1))):
            outline[(x, y)] = 0.55
        elif (x, y + 2) not in covered and cm is not None and cm[x, y] >= 128:
            outline[(x, y)] = 0.32
    ground = (LEAVES if look in ("autumn-leaves", "autumn-bare") else []) + \
             (DRY_LEAVES if look == "winter-leaves" else []) + \
             (SNOW if look.startswith("winter-snowy") else [])
    bark_pal = [rgb(h)[:3] for h in BARK + ground]
    snow_pal = {rgb(h)[:3] for h in SNOW}
    crown_pal = None
    if ramps:
        a, b = ramps
        crown_pal = [rgb(h)[:3] for h in a + b] + [mix(rgb(a[i]), rgb(b[i]), 0.5)[:3] for i in range(min(len(a), len(b)))]
        crown_pal += [rgb(h)[:3] for h in BARK[:3]]
        crown_pal += [rgb(h)[:3] for h in UNDERSIDE.get(look, [])]
    out = Image.new("RGBA", (FW, FH), (0, 0, 0, 0))
    op = out.load()
    for y in range(FH):
        for x in range(FW):
            if sh[x, y] >= 110:
                op[x, y] = SHADOW
    for (x, y), c in covered.items():
        snapped = nearest(c, crown_pal if cm is not None and cm[x, y] >= 128 else bark_pal)
        k = outline.get((x, y), 0.0)
        if k and snapped not in snow_pal:      # an outline round snow reads as a hole
            snapped = mix(snapped, OUTLINE, k)[:3]
        op[x, y] = snapped + (255,)
    return out


def frame_count(look):
    return FRAMES if look in LEAFY else 1


def look_frames(look, seed, branches, twigs):
    """A look's frames. A leafy look is the still tree, then the puff at the
    end of the loop; the shadow stays the still tree's, so the ground under
    it does not flicker."""
    body, shadow, mask, ramps = paint(look, seed, branches, twigs)
    still = shrink(look, body, shadow, mask, ramps)
    if look not in LEAFY:
        return [still]
    frames = [still] * FRAMES
    for i, (stir, lean) in enumerate(zip(PUFF, LEAN)):
        g_body, _, g_mask, g_ramps = paint(look, seed, branches, twigs, gust=(i, stir, lean))
        frames[PUFF_AT + i] = shrink(look, g_body, shadow, g_mask, g_ramps)
    return frames


def build_sheet(seed):
    branches, twigs = colonize(seed, 900)
    sheet = Image.new("RGBA", (FW * FRAMES, FH * len(LOOKS)), (0, 0, 0, 0))
    for row, look in enumerate(LOOKS):
        for col, frame in enumerate(look_frames(look, seed, branches, twigs)):
            sheet.paste(frame, (col * FW, row * FH))
    return sheet


def check_frames():
    """Every frame of every look stands on the catalog anchor and fits its
    frame: the bole's last solid row (just above the anchor row, which the
    bark only partly covers) is solid across the middle columns, and nothing
    opaque touches the frame's left, right or top edge, where it would be
    cut off flat. (Roots and fallen leaves reach below the anchor row, as
    the art it replaces does.) A leafy loop starts still: its first frame is
    the look's still tree, as the bare looks' only frame is."""
    ok = True
    for name, _seed in VARIANTS:
        sheet = Image.open(os.path.join(OUT_DIR, name + ".png")).convert("RGBA")
        if sheet.size != (FW * FRAMES, FH * len(LOOKS)):
            print("%s: sheet is %dx%d, want %dx%d" % (name, sheet.size[0], sheet.size[1], FW * FRAMES, FH * len(LOOKS)))
            ok = False
            continue
        row = round(FH * ANCHOR_Y) - 1
        for r, look in enumerate(LOOKS):
            bad = []
            for col in range(frame_count(look)):
                x0, y0 = col * FW, r * FH
                solid = all(sheet.getpixel((x0 + x, y0 + row))[3] == 255 for x in range(FW // 2 - 3, FW // 2 + 4))
                edge = any(sheet.getpixel((x0 + x, y0 + y))[3] == 255 for y in range(FH) for x in (0, FW - 1)) or \
                    any(sheet.getpixel((x0 + x, y0))[3] == 255 for x in range(FW))
                if not solid or edge:
                    bad.append(col)
            ok = ok and not bad
            print("%s %-17s %2d frame(s)%s" % (name, look, frame_count(look),
                                              "" if not bad else "  <- off the anchor or on the frame edge: %s" % bad))
    return ok


def main():
    if "--check" in sys.argv:
        sys.exit(0 if check_frames() else 1)
    os.makedirs(OUT_DIR, exist_ok=True)
    for name, seed in VARIANTS:
        build_sheet(seed).save(os.path.join(OUT_DIR, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
