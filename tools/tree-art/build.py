"""Paints salem's maple (LLM-967): every pixel is drawn here, no pack art.

    python tools/tree-art/build.py
    python tools/tree-art/build.py --check   # every look on its anchor, inside its frame

Writes client/assets/trees/maple.png: one 80x112 frame per look, in LOOKS
order, left to right. The catalog's Maple Tree states point at it (migration
LLM-967-painted-maple). After a change, run `godot --headless --editor --quit`
in client/ so Godot writes the .import sidecar, and commit it with the PNG.

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

Rerunning writes the same file (every random draw comes from a fixed seed).
"""

import math
import os
import random
import sys

from PIL import Image, ImageDraw

ROOT = os.path.normpath(os.path.join(os.path.dirname(__file__), "..", ".."))
OUT = os.path.join(ROOT, "client", "assets", "trees", "maple.png")

S = 4                      # painting scale
FW, FH = 80, 112           # frame, the catalog's Maple Tree size
W, H = FW * S, FH * S
LIGHT = (-0.55, -0.83)     # from the upper left
ANCHOR_Y = 0.93            # the catalog anchor: the foot's row, as a share of FH
BASE_Y = H * ANCHOR_Y
CROWN = (W * 0.5, H * 0.37, W * 0.44, H * 0.34)   # upright oval: centre, radii
TRUNK_TOP = H * 0.72
SEED = 21

# The catalog state each frame serves, left to right. Every look is the same
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
            d.line([(b.x1, b.y1), (x1, y1)], fill=col, width=max(2, int(0.9 * S)))
            for s2 in (-1, 1):
                if rnd.random() < 0.5:
                    a2 = a + s2 * rnd.uniform(0.35, 0.6)
                    L2 = L * rnd.uniform(0.4, 0.7)
                    d.line([(x1, y1), (x1 + math.sin(a2) * L2, y1 - math.cos(a2) * L2)], fill=col, width=max(2, int(0.8 * S)))


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


def draw_pad(d, rnd, x, y, ramp_a, ramp_b, hue, light):
    """A pad of leaves, wider than tall: a dark underside, leaf marks lit from
    the top (each with its own small shadow), ragged leaves at its rim."""
    rx = rnd.uniform(6.5, 9.5) * S
    ry = rx * rnd.uniform(0.5, 0.62)
    # Keep the pad, and the ragged leaves at its rim, a pixel inside the
    # frame: a crown cut by the frame edge shows a flat side.
    room = min(x, W - x) - 1 * S - 2.7 * S
    rx = max(2 * S, min(rx, room / 1.05))

    def tone(v):
        return mix(ramp(ramp_a, v), ramp(ramp_b, v), hue)
    d.ellipse((x - rx, y - ry * 0.6, x + rx, y + ry * 1.25), fill=tone(light - 0.4))
    n = int(rx * ry / (S * S) * 0.55)
    marks = []
    for _ in range(n):
        a = rnd.uniform(0, math.tau)
        rr = math.sqrt(rnd.random())
        px, py = x + math.cos(a) * rx * rr, y + math.sin(a) * ry * rr - ry * 0.15
        top = -(py - y) / ry                      # +1 at the top of the pad
        v = light + top * 0.22 + rnd.uniform(-0.12, 0.12)
        marks.append((py, px, v, rnd.uniform(0, math.tau), rnd.uniform(1.8, 2.6) * S))
    for py, px, v, ang, s in sorted(marks):
        pts = [(px + math.cos(ang + k * 2.2) * s * (1 if k == 0 else 0.7),
                py + math.sin(ang + k * 2.2) * s * (0.8 if k == 0 else 0.6)) for k in range(3)]
        d.polygon([(qx + 0.5 * S, qy + 0.8 * S) for qx, qy in pts], fill=tone(v - 0.3))
        d.polygon(pts, fill=tone(v))
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


def draw_crown(body, seed, branches, twigs, look):
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
    for x, y in sorted(pad_points(rnd, twigs), key=lambda p: p[1]):
        gx, gy = (x - cx) / rx, (y - cy) / ry
        # even light: a gentle lift toward the upper left, the lowest pads a
        # little darker where the crown shades itself
        light = 0.62 - (gx * LIGHT[0] + gy * LIGHT[1]) * 0.08 - max(0.0, gy - 0.6) * 0.25 + rnd.uniform(-0.05, 0.05)
        draw_pad(d, rnd, x, y, ramp_a, ramp_b, hue_of(patches(x, y)), light)
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

def paint(look, branches, twigs):
    leafy = look in LEAFY
    body = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(body)
    trunk = Branch(W * 0.5, BASE_Y, W * 0.5 + 1 * S, TRUNK_TOP, 14 * S, 11 * S, 0)
    if not leafy:
        draw_twig_haze(d, random.Random(SEED + 7), branches)
        for b in sorted(branches, key=lambda b: b.w1):   # thin first: limbs over twigs
            if b.depth > 0:
                draw_branch(d, b)
    draw_branch(d, trunk)
    draw_furrows(d, random.Random(SEED + 5), trunk)
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
        body, crown_mask, ramps = draw_crown(body, SEED + 1, branches, twigs, look)
        d = ImageDraw.Draw(body)
    shadow = cast_shadow(body)
    if look in ("autumn-leaves", "autumn-bare"):
        foot_leaves(d, random.Random(SEED + 3), 22 if look == "autumn-leaves" else 14, LEAVES)
    if look == "winter-leaves":
        foot_leaves(d, random.Random(SEED + 4), 16, DRY_LEAVES)
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


def build_sheet():
    branches, twigs = colonize(SEED, 900)
    sheet = Image.new("RGBA", (FW * len(LOOKS), FH), (0, 0, 0, 0))
    for i, look in enumerate(LOOKS):
        sheet.paste(shrink(look, *paint(look, branches, twigs)), (i * FW, 0))
        print("painted", look)
    return sheet


def check_frames():
    """Every look stands on the catalog anchor and fits its frame: the bole's
    last solid row (just above the anchor row, which the bark only partly
    covers) is solid across the middle columns, and nothing opaque touches
    the frame's left, right or top edge, where it would be cut off flat.
    (Roots and fallen leaves reach below the anchor row, as the art it
    replaces does.)"""
    sheet = Image.open(OUT).convert("RGBA")
    row = round(FH * ANCHOR_Y) - 1
    ok = True
    for i, look in enumerate(LOOKS):
        x0 = i * FW
        solid = all(sheet.getpixel((x0 + x, row))[3] == 255 for x in range(FW // 2 - 3, FW // 2 + 4))
        edge = [(x, y) for y in range(FH) for x in (0, FW - 1) if sheet.getpixel((x0 + x, y))[3] == 255] + \
               [(x, 0) for x in range(FW) if sheet.getpixel((x0 + x, 0))[3] == 255]
        good = solid and not edge
        ok = ok and good
        print("%-17s trunk on the anchor: %s, pixels on the frame edge: %d" % (look, "yes" if solid else "NO", len(edge)))
    return ok


def main():
    if "--check" in sys.argv:
        sys.exit(0 if check_frames() else 1)
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    build_sheet().save(OUT)
    print("wrote", OUT)


if __name__ == "__main__":
    main()
