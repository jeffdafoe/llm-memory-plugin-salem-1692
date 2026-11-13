"""Paints salem's trees (LLM-967): every pixel is drawn here, no pack art.

    python tools/tree-art/build.py
    python tools/tree-art/build.py --species birch --out DIR   # one species, elsewhere
    python tools/tree-art/build.py --check   # every frame on its anchor, inside its frame

Writes client/assets/trees/<species>-1.png, -2, -3 for every species in
SPECIES (maple, chestnut, birch, apple): three trees each, grown from
different seeds, so no two neighbours share a fork. Each sheet has one row per
look, in the species' looks order (LOOKS for the big trees), top to bottom; a
row is the look's frames, left to right, at the species' frame size (80x112;
the apple tree 48x64). The leafy looks are a FRAMES-long loop at FPS with one
light-wind puff in it; the bare looks are one frame. The catalog's trees point
at them (migrations LLM-967b-maple-variants, LLM-967c-chestnut-birch,
LLM-967d-apple-tree). After a change, run
`godot --headless --editor --quit` in client/ so Godot writes the .import
sidecars, and commit them.

How a tree is made, from photographs (sugar maple; American and sweet
chestnut; paper and gray birch; orchard apple trees):
- The branches are grown by space colonization: target points fill the
  species' crown, and from the trunk top each node grows a step toward the
  targets nearest it, using them up as it reaches them. Limbs fork and reach
  outward at the crown's edge as real ones do. A limb's width follows the
  share of the twigs it carries, so the trunk is thickest and a twig is one
  pixel.
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

What makes each species its own (Species):
- Maple: an upright oval crown of mid-sized pads, brown bark.
- Chestnut: a broad dome built of big rounded lobes, each lit on top and
  shaded under; dense, coarse leaf marks in a deep glossy green; a short,
  stout trunk with grey-brown ridges that spiral; limbs that spread wide.
  Autumn is yellow going golden-brown and russet, never red.
- Birch: a slender white stem that runs up into a narrow, airy crown of small
  light-green pads with sky between them; black dashes and dark chevrons on
  the bark, a darker foot; dark fine twigs that hang at their tips. Autumn is
  clear gold.
- Apple: a short leaning trunk that forks low into crooked, spreading limbs;
  a low, broad, rounded crown of dull mid-green with grey-green undersides,
  turning yellow-olive in patches in autumn. Its looks are the catalog's
  fruit states: with apples (red and red-gold, hung under the leaf pads) and
  picked, the same tree leaf for leaf; the same fallen leaves lie under both
  in autumn.

Rerunning writes the same files (every random draw comes from a fixed seed).
"""

import argparse
import math
import os
import random
import sys

from PIL import Image, ImageChops, ImageDraw

ROOT = os.path.normpath(os.path.join(os.path.dirname(__file__), "..", ".."))
OUT_DIR = os.path.join(ROOT, "client", "assets", "trees")

S = 4                      # painting scale
FW, FH = 80, 112           # frame, the catalog's tree size
W, H = FW * S, FH * S
LIGHT = (-0.55, -0.83)     # from the upper left
ANCHOR_Y = 0.93            # the catalog anchor: the foot's row, as a share of FH
BASE_Y = H * ANCHOR_Y
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


def use_frame(sp):
    """Point the frame globals (FW, FH, W, H, ANCHOR_Y, BASE_Y) at a species'
    frame before painting or checking it: the big trees are 80x112 on anchor
    0.93, the apple tree 48x64 on 0.85. Everything below reads them at call
    time."""
    global FW, FH, W, H, ANCHOR_Y, BASE_Y
    FW, FH, ANCHOR_Y = sp.frame
    W, H = FW * S, FH * S
    BASE_Y = H * ANCHOR_Y


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
# Chestnut: grey-brown ridged bark; a deep, slightly blue glossy green and an
# olive; in autumn a golden brown going russet.
CHESTNUT_BARK = ["1c1814", "3a3229", "5e5244", "847462", "a8977e"]
CHESTNUT_GREEN = ["12261a", "1c3a24", "28502e", "366838", "4a8242", "669c52", "8eb86a"]
CHESTNUT_OLIVE = ["1e2c14", "2e421c", "405a24", "56722e", "70903a", "90aa4c", "b4c468"]
CHESTNUT_GOLD = ["44300e", "6e4e14", "9a701c", "c0922a", "daae40", "eccb62"]
RUSSET = ["3a200e", "5e3416", "844c20", "a6662c", "c4823c", "d89e56"]
# Birch: chalk-white bark shaded grey, black marks, dark red-brown twigs;
# small light-green leaves, clear gold in autumn.
BIRCH_BARK = ["4a4642", "7c7872", "aeaaa2", "d6d2c8", "eeebe2"]
BIRCH_MARK = ["1e1c1a", "2e2a27"]
BIRCH_TWIG = ["3a2626", "5a3e3a", "7a5852", "9a7a72"]
BIRCH_GREEN = ["22381a", "32521f", "467026", "5e8e2c", "7caa36", "a0c44a", "c6de6a"]
BIRCH_LIME = ["2c3a14", "42561a", "5c7420", "789228", "98b034", "bccc4a", "dce670"]
BIRCH_GOLD = ["4e3a0c", "806010", "b08a16", "d8b024", "f0d03c", "fae678"]
BIRCH_AMBER = ["50300c", "845010", "b4741a", "d89628", "eeb63c", "f8d066"]
# Apple: flaky grey-brown bark; a dull mid-green and a greyer green (the
# leaves are downy, paler beneath); red apples and red-flushed gold ones.
APPLE_BARK = ["241e18", "40362c", "5e5042", "7c6c5a", "9a8a74"]
APPLE_GREEN = ["1e3418", "2c4c24", "3e682e", "548638", "6ea240", "90bc56", "b6d478"]
APPLE_GREY = ["20341f", "30482e", "44623e", "5c7e4c", "789a5c", "98b674", "bcd094"]
APPLE_TURNING = ["3a3412", "5a5018", "7e7020", "a08c2c", "bea840", "d6c460", "e8da84"]
APPLE_RED = ["4e100c", "9a1e14", "c8341e", "f08a5c"]
APPLE_GOLD = ["6a3a10", "b8481c", "d8862c", "f4cc70"]
# Fallen leaves: the ground's autumn leaf colours (terrain-art LEAVES), and
# the browner dry leaves of December.
LEAVES = ["c8702a", "d99a3a", "a8452a", "b8862e"]
DRY_LEAVES = ["6e5234", "8a6a42", "a08454"]
SNOW = ["c9d4de", "e2e9ef", "f4f8fb"]
OUTLINE = (16, 18, 14, 255)
SHADOW = (18, 22, 14, 110)


# ---------------------------------------------------------------- species

class Species:
    """Everything that makes one kind of tree its own. The maple's values are
    the ones the painter was first written with; the code paths below take
    the same random draws for it as before, so its sheets do not change."""

    def __init__(self, **kw):
        # The big trees' frame and looks; a species may give its own.
        self.frame = (80, 112, 0.93)
        self.looks, self.leafy = LOOKS, LEAFY
        # Fruit hung under the leaf pads, in fruit_looks only (fruit, below).
        self.fruit, self.fruit_looks = None, ()
        # Fallen leaves at the foot, by look: how many.
        self.foot = {"autumn-leaves": 22, "autumn-bare": 14}
        self.__dict__.update(kw)


def chestnut_lobes(sp, seed):
    """A chestnut crown is a dome of big rounded lobes, one over each main
    limb: a core, a ring of lobes round its top, and two lower ones at the
    sides."""
    rnd = random.Random(seed * 31 + 7)
    cx, cy, rx, ry = sp.crown
    lobes = [(cx, cy + ry * 0.15, rx * 0.55, ry * 0.6)]
    n = 7
    for i in range(n):
        a = math.pi + math.pi * (i + 0.5) / n + rnd.uniform(-0.12, 0.12)
        dist = rnd.uniform(0.5, 0.62)
        size = rnd.uniform(0.3, 0.4)
        lobes.append((cx + math.cos(a) * rx * dist, cy + math.sin(a) * ry * dist, rx * size, ry * size * 1.1))
    for side in (-1, 1):
        lobes.append((cx + side * rx * 0.6, cy + ry * 0.32, rx * 0.36, ry * 0.36))
    return lobes


SPECIES = {
    "maple": Species(
        # Seed 21 is the one Jeff first approved; 8 and 55 were picked from
        # candidates as the least like it.
        variants=[("maple-1", 21), ("maple-2", 8), ("maple-3", 55)],
        crown=(W * 0.5, H * 0.37, W * 0.44, H * 0.34),   # upright oval: centre, radii
        lobes=None,
        trunk_top=H * 0.72, trunk_w=(14 * S, 11 * S), lean=1 * S,
        trunk_nodes=6, branch_nodes=1, target_floor=H * 0.72 - 6 * S,
        n_targets=900, bias=0.22, wander=0.12, limb_w=14 * S,
        bark=BARK, twig=BARK, crown_bark=BARK[:3], extra_bark=[],
        furrows="straight", root_scale=1.0,
        haze=dict(tries=2, skip=0.45, turn=(0.45, 0.9), length=(2.5, 5.5), colour=BARK, shade=(0.35, 0.6), droop=0.0),
        deep_fill=True, light=0.62, pad_spacing=4.2,
        pad_rx=(6.5, 9.5), pad_ry=(0.5, 0.62), marks=0.55, mark_size=(1.8, 2.6), mark_shape=(1, 0.7),
        rim_size=(1.5, 2.2), rim_reach=2.7,
        ramps={"default": (GREEN, GREEN_WARM, lambda p: p * 0.6),
               # September: mostly green, a third of the crown already turning
               "autumn-clean": (GREEN, YELLOW, lambda p: max(0.0, min(1.0, (p - 0.55) * 2.5))),
               "autumn-leaves": (YELLOW, RED, lambda p: p)},
        # Leaf undersides: paler than the tops. In a light wind leaves turn
        # over and flash these, a small cluster at a time (flipper).
        underside={"default": ["7ea46a", "93b47c", "a6c38e"],
                   "autumn-clean": ["93b07a", "c8bf72", "d8cf8a"],
                   "autumn-leaves": ["e0a85a", "e8bc72", "eecb8c"]},
        fallen=LEAVES,
    ),
    "chestnut": Species(
        variants=[("chestnut-1", 3), ("chestnut-2", 14), ("chestnut-3", 40)],
        crown=(W * 0.5, H * 0.43, W * 0.42, H * 0.28),   # a broad dome
        lobes=chestnut_lobes,
        # The ridged bole is drawn on up under the crown (bole_top), where
        # a chestnut's stem carries on between its first great limbs.
        trunk_top=H * 0.70, bole_top=H * 0.58, bole_over_limbs=True, trunk_w=(17 * S, 12 * S), lean=0,
        trunk_nodes=6, branch_nodes=1, target_floor=H * 0.70 - 6 * S,
        n_targets=1100, bias=0.1, wander=0.15, limb_w=17 * S,
        bark=CHESTNUT_BARK, twig=CHESTNUT_BARK, crown_bark=CHESTNUT_BARK, extra_bark=[],
        furrows="spiral", root_scale=1.15,
        haze=dict(tries=2, skip=0.5, turn=(0.5, 1.0), length=(2.5, 5.0), colour=CHESTNUT_BARK, shade=(0.3, 0.55), droop=0.0),
        deep_fill=True, light=0.6, pad_spacing=4.0,
        pad_rx=(8.0, 11.0), pad_ry=(0.55, 0.68), marks=0.5, mark_size=(2.2, 3.0), mark_shape=(1.35, 0.55),
        rim_size=(1.8, 2.6), rim_reach=3.2,
        lobe_shade=0.3,
        ramps={"default": (CHESTNUT_GREEN, CHESTNUT_OLIVE, lambda p: p * 0.5),
               "autumn-clean": (CHESTNUT_GREEN, CHESTNUT_GOLD, lambda p: max(0.0, min(1.0, (p - 0.5) * 2.5))),
               "autumn-leaves": (CHESTNUT_GOLD, RUSSET, lambda p: p)},
        underside={"default": ["6e9a74", "84aa86", "9cbc98"],
                   "autumn-clean": ["a0aa70", "c4b46a", "d6c888"],
                   "autumn-leaves": ["d6aa66", "e0bc7c", "e8cc96"]},
        fallen=["d99a3a", "b8862e", "c8702a"],
    ),
    "birch": Species(
        variants=[("birch-1", 5), ("birch-2", 17), ("birch-3", 33)],
        crown=(W * 0.5, H * 0.37, W * 0.29, H * 0.33),   # narrow and upright
        lobes=None,
        # The white stem runs up through the crown; limbs leave it all along
        # its upper part.
        trunk_top=H * 0.14, trunk_w=(8 * S, 2.5 * S), lean=1.5 * S,
        trunk_nodes=10, branch_nodes=6, target_floor=H * 0.66,
        n_targets=600, bias=0.32, wander=0.14, limb_w=20 * S,
        bark=BIRCH_BARK, twig=BIRCH_TWIG, crown_bark=BIRCH_BARK + BIRCH_TWIG, extra_bark=BIRCH_TWIG + BIRCH_MARK,
        furrows="lenticels", root_scale=0.55,
        haze=dict(tries=2, skip=0.45, turn=(0.4, 0.8), length=(2.0, 4.0), colour=BIRCH_TWIG, shade=(0.45, 0.95), droop=0.2),
        deep_fill=False, light=0.62, pad_spacing=3.6,
        pad_rx=(3.5, 5.5), pad_ry=(0.55, 0.7), marks=0.75, mark_size=(1.1, 1.6), mark_shape=(1, 0.7),
        rim_size=(1.0, 1.5), rim_reach=1.8,
        ramps={"default": (BIRCH_GREEN, BIRCH_LIME, lambda p: p * 0.6),
               "autumn-clean": (BIRCH_GREEN, BIRCH_GOLD, lambda p: max(0.0, min(1.0, (p - 0.5) * 2.5))),
               "autumn-leaves": (BIRCH_GOLD, BIRCH_AMBER, lambda p: p * 0.55)},
        underside={"default": ["9cbc7a", "b0cc8c", "c2d8a0"],
                   "autumn-clean": ["b8c47a", "dccf7a", "e8dc96"],
                   "autumn-leaves": ["f0d878", "f4e28e", "f8eaa8"]},
        fallen=["d99a3a", "b8862e"],
    ),
    "apple": Species(
        # The catalog's Apple Tree: a 48x64 frame on anchor 0.85, four looks
        # (with apples / picked, summer / autumn), all leafy.
        frame=(48, 64, 0.85),
        looks=["berries", "bare", "autumn-berries", "autumn-bare"],
        leafy=("berries", "bare", "autumn-berries", "autumn-bare"),
        variants=[("apple-1", 2), ("apple-2", 12), ("apple-3", 27)],
        # A low, broad, rounded crown on a short trunk that forks low.
        crown=(48 * S * 0.5, 64 * S * 0.40, 48 * S * 0.42, 64 * S * 0.27),
        lobes=None,
        trunk_top=64 * S * 0.62, trunk_w=(8 * S, 6 * S), lean=1.5 * S,
        trunk_nodes=6, branch_nodes=2, target_floor=64 * S * 0.62 - 2 * S,
        n_targets=450, bias=0.05, wander=0.25, limb_w=9 * S,
        bark=APPLE_BARK, twig=APPLE_BARK, crown_bark=APPLE_BARK, extra_bark=[],
        furrows="straight", root_scale=0.6,
        haze=dict(tries=2, skip=0.45, turn=(0.5, 1.0), length=(2.0, 4.0), colour=APPLE_BARK, shade=(0.3, 0.6), droop=0.0),
        deep_fill=True, light=0.66, pad_spacing=3.0,
        pad_rx=(3.5, 5.0), pad_ry=(0.55, 0.68), marks=0.7, mark_size=(1.1, 1.5), mark_shape=(1.1, 0.65),
        rim_size=(1.0, 1.4), rim_reach=2.4,
        ramps={"berries": (APPLE_GREEN, APPLE_GREY, lambda p: p * 0.6),
               "bare": (APPLE_GREEN, APPLE_GREY, lambda p: p * 0.6),
               "autumn-berries": (APPLE_GREEN, APPLE_TURNING, lambda p: max(0.0, min(1.0, (p - 0.15) * 1.6))),
               "autumn-bare": (APPLE_GREEN, APPLE_TURNING, lambda p: max(0.0, min(1.0, (p - 0.15) * 1.6)))},
        underside={"berries": ["98a88a", "acba9e", "c0cab2"],
                   "bare": ["98a88a", "acba9e", "c0cab2"],
                   "autumn-berries": ["aeae86", "c2bf98", "d2ceac"],
                   "autumn-bare": ["aeae86", "c2bf98", "d2ceac"]},
        fallen=["d99a3a", "b8862e"],
        fruit=dict(per_pad=1, share=0.42, spread=2.0, drop=(0.8, 2.4), r=1.6, ramps=[APPLE_RED, APPLE_RED, APPLE_GOLD]),
        fruit_looks=("berries", "autumn-berries"),
        # each fruit look's picked look: the same frames without the fruit
        picked={"berries": "bare", "autumn-berries": "autumn-bare"},
        # picked or not, the same leaves lie under it in autumn
        foot={"autumn-berries": 10, "autumn-bare": 10},
    ),
}


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


def crown_targets(sp, rnd, lobes):
    """The points the branches grow toward. An oval crown samples its oval;
    a lobed crown samples the union of its lobes, inside the frame."""
    cx, cy, rx, ry = sp.crown
    targets = []
    while len(targets) < sp.n_targets:
        if lobes is None:
            a = rnd.uniform(0, math.tau)
            r = math.sqrt(rnd.random())
            x, y = cx + math.cos(a) * rx * r, cy + math.sin(a) * ry * r
        else:
            x, y = rnd.uniform(cx - rx * 1.1, cx + rx * 1.1), rnd.uniform(cy - ry * 1.1, cy + ry * 1.1)
            if not (4 * S <= x <= W - 4 * S and y >= 5 * S):
                continue
            if not any(((x - lx) / lrx) ** 2 + ((y - ly) / lry) ** 2 <= 1 for lx, ly, lrx, lry in lobes):
                continue
        if y < sp.target_floor:
            targets.append((x, y))
    return targets


def colonize(sp, seed):
    """Space colonization. Returns (branches, twig points for leaf pads)."""
    rnd = random.Random(seed)
    targets = crown_targets(sp, rnd, sp.lobes(sp, seed) if sp.lobes else None)
    step, reach, kill = 2.4 * S, 13 * S, 3.6 * S
    # the trunk: a short chain of nodes from the foot to the trunk top
    nodes, parent = [], []
    grid = Grid(reach)
    for k in range(sp.trunk_nodes):
        t = k / (sp.trunk_nodes - 1)
        nodes.append((W * 0.5 + sp.lean * t, BASE_Y + (sp.trunk_top - BASE_Y) * t))
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
            if ni < 0 or d >= reach or ni < trunk_n - sp.branch_nodes:
                continue          # only the trunk's top branches, not the bole
            vx, vy = tx - nodes[ni][0], ty - nodes[ni][1]
            n = math.hypot(vx, vy) + 1e-6
            px, py = pull.get(ni, (0.0, 0.0))
            pull[ni] = (px + vx / n, py + vy / n)
        grew = False
        for ni, (vx, vy) in pull.items():
            n = math.hypot(vx, vy) + 1e-6
            # a little upward lean and a little wander, as limbs climb
            vx, vy = vx / n + rnd.uniform(-sp.wander, sp.wander), vy / n - sp.bias
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
    w = [max(1.0 * S, sp.limb_w * (carried[i] / carried[0]) ** 0.8) for i in range(len(nodes))]
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

def draw_branch(d, sp, b):
    """A limb in bands across its width, lit on the upper-left side; a twig
    is one dark stroke."""
    dx, dy = b.x1 - b.x0, b.y1 - b.y0
    L = math.hypot(dx, dy) or 1
    nx, ny = -dy / L, dx / L
    lit_side = 1 if (nx * LIGHT[0] + ny * LIGHT[1]) > 0 else -1
    if b.w0 < 2.2 * S:
        d.line([(b.x0, b.y0), (b.x1, b.y1)], fill=ramp(sp.twig, 0.25), width=max(2, int(b.w0)))
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
        d.polygon(pts, fill=ramp(sp.bark, 0.12 + 0.8 * (t0 + t1) / 2))
    r = b.w1 / 2
    d.ellipse((b.x1 - r, b.y1 - r, b.x1 + r, b.y1 + r), fill=ramp(sp.bark, 0.45))


def draw_twig_haze(d, sp, rnd, branches):
    """Short side twigs off the thin branches, forking once, in a greyer bark,
    so a bare outer crown reads as a haze rather than black lines. A birch's
    hang down at their tips (droop)."""
    hz = sp.haze
    for b in branches:
        if b.w1 > 2.5 * S or b.depth == 0:
            continue
        base = math.atan2(b.x1 - b.x0, -(b.y1 - b.y0))      # 0 = straight up
        for _ in range(hz["tries"]):
            if rnd.random() < hz["skip"]:
                continue
            a = base + rnd.choice((-1, 1)) * rnd.uniform(*hz["turn"])
            if hz["droop"]:
                # bend toward straight down, on the side the twig points
                a += hz["droop"] * (math.copysign(math.pi, math.sin(a) or 1.0) - a)
            L = rnd.uniform(*hz["length"]) * S
            x1, y1 = b.x1 + math.sin(a) * L, b.y1 - math.cos(a) * L
            col = ramp(hz["colour"], rnd.uniform(*hz["shade"]))
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


def draw_roots(d, sp, trunk):
    bx, by = trunk.x0, trunk.y0
    hw = trunk.w0 / 2
    k = sp.root_scale
    for sx, reach, drop, t in ((-1, 1.7, 1.0, 0.72), (1, 1.55, 1.2, 0.3), (-0.45, 1.05, 2.2, 0.58), (0.5, 1.1, 2.4, 0.24)):
        root = (bx + sx * hw * 0.55, by - 7 * S * k)
        tip = (bx + sx * hw * reach, by + drop * S)
        knee = (bx + sx * hw * (reach * 0.8), by - 1.5 * S)
        d.polygon([(root[0] - 3.2 * S * k, root[1]), (root[0] + 3.2 * S * k, root[1]), knee, tip,
                   (tip[0] - sx * 3 * S * k, tip[1] + 0.6 * S)], fill=ramp(sp.bark, t))


def draw_furrows(d, sp, rnd, trunk):
    """The bark's texture on the trunk: a maple's straight furrows, a
    chestnut's long ridges winding up round the bole, or a birch's marks."""
    if sp.furrows == "straight":
        for _ in range(14):
            t = rnd.uniform(-0.4, 0.4)
            d.line([(trunk.x0 + t * trunk.w0, trunk.y0 - rnd.uniform(1, 5) * S),
                    (trunk.x1 + t * trunk.w1, trunk.y1 + rnd.uniform(2, 10) * S)],
                   fill=ramp(sp.bark, 0.04 if t > -0.1 else 0.3), width=S)
    elif sp.furrows == "spiral":
        # Each ridge runs from the foot toward the top, drifting `slant` of
        # the width to the right; it is drawn only where it is on the front
        # of the bole (a ridge winding round leaves sight at the edge), and
        # a pale ridge rides beside each dark furrow.
        slant = 0.35
        y0, y1 = trunk.y0 - 1 * S, trunk.y1
        for _ in range(26):
            t = rnd.uniform(-0.45 - slant, 0.45)
            u0, u1 = max(0.0, (-0.45 - t) / slant), min(1.0, (0.45 - t) / slant)
            shade = rnd.uniform(0.0, 0.25)
            for off, tone in ((0.0, shade), (0.07, 0.75)):
                pts = []
                for u in (u0, u1):
                    w = trunk.w0 + (trunk.w1 - trunk.w0) * u
                    cx = trunk.x0 + (trunk.x1 - trunk.x0) * u
                    pts.append((cx + (t + off + slant * u) * w, y0 + (y1 - y0) * u))
                d.line(pts, fill=ramp(sp.bark, tone), width=S)
    elif sp.furrows == "lenticels":
        draw_birch_marks(d, rnd, trunk)


def draw_birch_marks(d, rnd, trunk):
    """A birch's bark: short black dashes across the stem, wider dark patches
    here and there, and a darker, rougher foot."""
    def at(t):
        """Centre x and width of the stem at t (0 = foot, 1 = top)."""
        return trunk.x0 + (trunk.x1 - trunk.x0) * t, trunk.w0 + (trunk.w1 - trunk.w0) * t
    span = trunk.y0 - trunk.y1
    # the foot: grey-black, rough, reaching a little way up
    for _ in range(26):
        t = rnd.uniform(0, 0.09)
        cx, w = at(t)
        y = trunk.y0 - t * span
        u = rnd.uniform(-0.5, 0.35)
        d.line([(cx + u * w, y), (cx + (u + rnd.uniform(0.2, 0.5)) * w, y - rnd.uniform(0.5, 2) * S)],
               fill=ramp(BIRCH_MARK + BIRCH_BARK[:2], rnd.uniform(0, 0.7)), width=S)
    # dashes up the stem, thinning out toward the top
    for _ in range(60):
        t = rnd.uniform(0.06, 0.9) ** 1.3
        cx, w = at(t)
        if w < 2.5 * S:
            continue
        y = trunk.y0 - t * span
        u = rnd.uniform(-0.5, 0.3)
        L = rnd.uniform(0.15, 0.45)
        d.line([(cx + u * w, y), (cx + min(0.5, u + L) * w, y)], fill=rgb(rnd.choice(BIRCH_MARK)), width=S)
    # a few broad dark patches
    for _ in range(4):
        t = rnd.uniform(0.15, 0.6)
        cx, w = at(t)
        y = trunk.y0 - t * span
        u = rnd.uniform(-0.5, 0.0)
        d.polygon([(cx + u * w, y - 1.2 * S), (cx + (u + 0.5) * w, y), (cx + u * w, y + 1.2 * S)], fill=rgb(BIRCH_MARK[0]))


def draw_limb_marks(d, rnd, branches):
    """On a birch's white limbs: a dark chevron where each leaves the stem
    or its parent, and a dash or two along it."""
    for b in branches:
        if b.depth == 0 or b.w0 < 2.4 * S:
            continue
        dx, dy = b.x1 - b.x0, b.y1 - b.y0
        L = math.hypot(dx, dy) or 1
        nx, ny = -dy / L, dx / L
        if rnd.random() < 0.35:
            # a dash across the limb
            t = rnd.uniform(0.2, 0.8)
            px, py = b.x0 + dx * t, b.y0 + dy * t
            hw = (b.w0 + (b.w1 - b.w0) * t) * 0.4
            d.line([(px - nx * hw, py - ny * hw), (px + nx * hw * 0.3, py + ny * hw * 0.3)], fill=rgb(BIRCH_MARK[1]), width=S)


# ---------------------------------------------------------------- foliage

def pad_points(sp, rnd, twigs):
    """Pads hang along the fine twigs, which fill the crown: a sample of them,
    spread so no two pads sit on top of each other."""
    pts = []
    for x, y in sorted(twigs, key=lambda p: rnd.random()):
        if all((x - px) ** 2 + (y - py) ** 2 > (sp.pad_spacing * S) ** 2 for px, py in pts):
            pts.append((x + rnd.uniform(-1, 1) * S, y + rnd.uniform(-1, 1) * S))
    return pts


def draw_pad(d, sp, rnd, x, y, ramp_a, ramp_b, hue, light, still_x=None, flip=None, pad=0):
    """A pad of leaves, wider than tall: a dark underside, leaf marks lit from
    the top (each with its own small shadow), ragged leaves at its rim."""
    rx = rnd.uniform(*sp.pad_rx) * S
    ry = rx * rnd.uniform(*sp.pad_ry)
    # Keep the pad, and the ragged leaves at its rim, a pixel inside the
    # frame: a crown cut by the frame edge shows a flat side. Measured from
    # the pad's still position, so a gust moves the pad without changing its
    # size or any random draw after it.
    sx = x if still_x is None else still_x
    room = min(sx, W - sx) - 1 * S - sp.rim_reach * S - MAX_GUST * S
    rx = max(2 * S, min(rx, room / 1.05))
    n = int(rx * ry / (S * S) * sp.marks)
    # And a pixel below the frame's top: the pad's leaf marks reach up about
    # 1.15 ry plus a leaf. Scaled after n is counted, so a pad squeezed
    # under the top takes the same number of draws and no later pad changes.
    room_up = y - 1 * S - sp.rim_reach * S
    if ry * 1.15 > room_up:
        squeeze = max(2 * S, room_up / 1.15) / ry
        rx, ry = rx * squeeze, ry * squeeze
        # a pad on a twig too near the top for even its smallest size sits
        # a little lower instead
        y = max(y, 1 * S + sp.rim_reach * S + ry * 1.15)

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
        marks.append((py, px, v, rnd.uniform(0, math.tau), rnd.uniform(*sp.mark_size) * S, int(a / (math.tau / 3))))
    long_, short = sp.mark_shape
    for py, px, v, ang, s, sector in sorted(marks):
        pts = [(px + math.cos(ang + k * 2.2) * s * (long_ if k == 0 else short),
                py + math.sin(ang + k * 2.2) * s * (0.8 if k == 0 else 0.6)) for k in range(3)]
        d.polygon([(qx + 0.5 * S, qy + 0.8 * S) for qx, qy in pts], fill=tone(v - 0.3))
        under = flip(pad, sector, px, py) if flip is not None else None
        d.polygon(pts, fill=under if under is not None else tone(v))
    for _ in range(5):
        a = rnd.uniform(math.pi * 1.05, math.pi * 1.95) if rnd.random() < 0.7 else rnd.uniform(0, math.pi)
        px, py = x + math.cos(a) * rx * 1.05, y + math.sin(a) * ry * 1.05
        s = rnd.uniform(*sp.rim_size) * S
        d.polygon([(px + math.cos(a) * s * 1.2, py + math.sin(a) * s * 1.2),
                   (px + math.cos(a + 1.8) * s * 0.6, py + math.sin(a + 1.8) * s * 0.6),
                   (px + math.cos(a - 1.8) * s * 0.6, py + math.sin(a - 1.8) * s * 0.6)],
                  fill=tone(light + (0.1 if math.sin(a) < 0 else -0.15)))


def _hash(*parts):
    h = 2166136261
    for p in parts:
        h = ((h ^ (int(p) & 0xFFFFFFFF)) * 16777619) & 0xFFFFFFFF
    return h


def flipper(sp, gust, look, seed):
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
    cols = [rgb(h) for h in sp.underside[look]]

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


def draw_crown(sp, body, seed, branches, twigs, look, gust=None, bole=None, fruit=True):
    """Deep foliage inside the crown, the limbs showing through it, then the
    pads, higher first so lower pads overlap them. bole, if given, draws the
    trunk again over the limbs and under the pads: a thick first limb would
    otherwise lay its smooth bark over the trunk's own texture below the
    crown, as the bare looks avoid by drawing the trunk after the limbs."""
    rnd = random.Random(seed)
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    ramp_a, ramp_b, hue_of = sp.ramps[look]
    patches = value_noise(seed + 9, 4)
    cx, cy, rx, ry = sp.crown
    # The lobes come from the tree's own seed (draw_crown is given seed + 1).
    lobes = sp.lobes(sp, seed - 1) if sp.lobes else None
    if lobes:
        for lx, ly, lrx, lry in lobes:
            d.ellipse((lx - lrx * 0.75, ly - lry * 0.7, lx + lrx * 0.75, ly + lry * 0.75), fill=ramp(ramp_a, 0.1))
    elif sp.deep_fill:
        d.ellipse((cx - rx * 0.7, cy - ry * 0.7, cx + rx * 0.7, cy + ry * 0.75), fill=ramp(ramp_a, 0.1))
    for b in sorted(branches, key=lambda b: b.w1):
        if b.depth >= 1 and b.w1 > 2 * S:
            draw_branch(d, sp, b)
    if bole is not None:
        bole(d)
    for k, (x, y) in enumerate(sorted(pad_points(sp, rnd, twigs), key=lambda p: p[1])):
        gx, gy = (x - cx) / rx, (y - cy) / ry
        # even light: a gentle lift toward the upper left, the lowest pads a
        # little darker where the crown shades itself
        light = sp.light - (gx * LIGHT[0] + gy * LIGHT[1]) * 0.08 - max(0.0, gy - 0.6) * 0.25 + rnd.uniform(-0.05, 0.05)
        if lobes:
            # each lobe is lit on its top and shaded under, so the crown
            # reads as a heap of rounded masses
            lx, ly, lrx, lry = min(lobes, key=lambda l: ((x - l[0]) / l[2]) ** 2 + ((y - l[1]) / l[3]) ** 2)
            light -= sp.lobe_shade * max(-1.0, min(1.0, (y - ly) / lry))
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
        draw_pad(d, sp, rnd, x + dx, y, ramp_a, ramp_b, hue_of(patches(x, y)), light, still_x=x,
                 flip=None if gust is None else flipper(sp, gust[:2], look, seed), pad=k)
        if fruit and look in sp.fruit_looks:
            draw_fruit(d, sp, seed, k, x + dx, y)
    return Image.alpha_composite(body, lay), lay.split()[3], (ramp_a, ramp_b)


def draw_fruit(d, sp, seed, k, x, y):
    """Fruit hanging under leaf pad k. Where and which colour come from
    hashes of seed and pad, not the crown's generator, so the picked look
    (no fruit) is the same tree leaf for leaf, and a pad's fruit leans with
    it in the puff. A fruit is a round of its ramp's middle with a shaded
    lower right and a lit spot at the upper left."""
    fr = sp.fruit
    for j in range(fr["per_pad"]):
        h = _hash(seed, k, j, 41)
        if (h & 0xFF) >= fr["share"] * 256:
            continue
        fx = x + (((h >> 8) & 0xFF) / 255.0 * 2 - 1) * fr["spread"] * S
        fy = y + (fr["drop"][0] + ((h >> 16) & 0xFF) / 255.0 * (fr["drop"][1] - fr["drop"][0])) * S
        cols = fr["ramps"][(h >> 24) % len(fr["ramps"])]
        r = fr["r"] * S
        fx = max(r + 1.5 * S, min(W - r - 1.5 * S, fx))     # inside the frame
        d.ellipse((fx - r, fy - r, fx + r, fy + r), fill=rgb(cols[1]))
        d.ellipse((fx - r, fy - r, fx + r * 0.55, fy + r * 0.55), fill=rgb(cols[2]))
        d.ellipse((fx - r * 0.7, fy - r * 0.75, fx - r * 0.05, fy - r * 0.1), fill=rgb(cols[3]))


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


def cast_shadow(sp, body):
    """The tree's alpha squashed to about a quarter of its height, centred a
    little above the foot, the higher parts pushed right: a solid patch under
    a leafy crown, a hatch of branches under a bare one."""
    alpha = body.split()[3]
    y0 = BASE_Y - 6 * S          # where the crown centre's shadow lands
    sq, lean, dx = 0.26, 0.1, 7 * S
    # source y = cy + (y - y0) / sq ; source x = x - dx - lean * (BASE_Y - source y)
    e, f = 1 / sq, sp.crown[1] - y0 / sq
    return alpha.transform((W, H), Image.AFFINE, (1, lean * e, -dx - lean * BASE_Y + lean * f, 0, e, f),
                           resample=Image.BILINEAR)


# ---------------------------------------------------------------- one look

def paint(sp, look, seed, branches, twigs, gust=None, fruit=True):
    """One frame of one look. gust = (frame within the puff, how many leaf
    clusters stir 0..1, how far the crown leans), or None for the still
    tree. fruit=False paints a fruit look without its fruit."""
    leafy = look in sp.leafy
    body = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(body)
    trunk = Branch(W * 0.5, BASE_Y, W * 0.5 + sp.lean, getattr(sp, "bole_top", sp.trunk_top), sp.trunk_w[0], sp.trunk_w[1], 0)
    if not leafy:
        draw_twig_haze(d, sp, random.Random(seed + 7), branches)
        for b in sorted(branches, key=lambda b: b.w1):   # thin first: limbs over twigs
            if b.depth > 0:
                draw_branch(d, sp, b)
        if sp.furrows == "lenticels":
            draw_limb_marks(d, random.Random(seed + 6), branches)
    draw_branch(d, sp, trunk)
    draw_furrows(d, sp, random.Random(seed + 5), trunk)
    draw_roots(d, sp, trunk)

    def bole(dd):
        # the same trunk and furrows (same seed), over the crown's limbs
        draw_branch(dd, sp, trunk)
        draw_furrows(dd, sp, random.Random(seed + 5), trunk)
    if look == "winter-snowy":
        for b in branches:
            draw_snow_on(d, b)
    if look == "winter-snowy-top":
        # a dusting settles only on the upper crown, thinly
        for b in branches:
            if min(b.y0, b.y1) < sp.crown[1]:
                draw_snow_on(d, b, 0.6)
    crown_mask, ramps = None, None
    if leafy:
        body, crown_mask, ramps = draw_crown(sp, body, seed + 1, branches, twigs, look, gust,
                                             bole=bole if getattr(sp, "bole_over_limbs", False) else None,
                                             fruit=fruit)
        d = ImageDraw.Draw(body)
    shadow = cast_shadow(sp, body)
    if look in sp.foot:
        foot_leaves(d, random.Random(seed + 3), sp.foot[look], sp.fallen)
    if look == "winter-leaves":
        foot_leaves(d, random.Random(seed + 4), 16, DRY_LEAVES)
    if look == "winter-snowy":
        d.ellipse((W * 0.5 - 15 * S, BASE_Y - 3 * S, W * 0.5 + 17 * S, BASE_Y + 3.5 * S), fill=rgb("eef3f7"))
    return body, shadow, crown_mask, ramps


def nearest(c, pal):
    # weighted RGB distance: the eye is most sensitive to green, least to blue
    return min(pal, key=lambda p: 2 * (p[0] - c[0]) ** 2 + 4 * (p[1] - c[1]) ** 2 + 3 * (p[2] - c[2]) ** 2)


def shrink(sp, look, body, shadow, crown_mask, ramps):
    """To frame size: area average, snap to the look's colours, outline, and
    the shadow under it all at SHADOW's alpha."""
    small = body.resize((FW, FH), Image.BOX)
    sh = shadow.resize((FW, FH), Image.BOX).load()
    cm = crown_mask.resize((FW, FH), Image.BOX).load() if crown_mask is not None else None
    sp_ = small.load()
    covered = {(x, y): sp_[x, y][:3] for y in range(FH) for x in range(FW) if sp_[x, y][3] >= 128}
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
    ground = (LEAVES if look in sp.foot else []) + \
             (DRY_LEAVES if look == "winter-leaves" else []) + \
             (SNOW if look.startswith("winter-snowy") else [])
    bark_pal = [rgb(h)[:3] for h in sp.bark + sp.extra_bark + ground]
    snow_pal = {rgb(h)[:3] for h in SNOW}
    crown_pal = None
    if ramps:
        a, b = ramps
        crown_pal = [rgb(h)[:3] for h in a + b] + [mix(rgb(a[i]), rgb(b[i]), 0.5)[:3] for i in range(min(len(a), len(b)))]
        crown_pal += [rgb(h)[:3] for h in sp.crown_bark]
        crown_pal += [rgb(h)[:3] for h in sp.underside.get(look, [])]
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


def frame_count(sp, look):
    return FRAMES if look in sp.leafy else 1


def add_fruit(frame, bare, fruited, sp):
    """Lay a fruit look's fruit on its frame, which was shrunk from the same
    tree painted without fruit, so every other pixel is the picked look's
    own. The fruit is what the two paintings differ by (an apple behind a
    later pad is hidden in both); a frame pixel at least half fruit takes
    the fruit's average colour there, snapped to the fruit colours."""
    # any channel differing, alpha included (a grey conversion would round
    # a one-level difference away)
    bands = ImageChops.difference(bare, fruited).split()
    diff = bands[0]
    for band in bands[1:]:
        diff = ImageChops.lighter(diff, band)
    diff = diff.point(lambda v: 255 if v else 0)
    only = Image.new("RGBA", fruited.size, (0, 0, 0, 0))
    only.paste(fruited, (0, 0), diff)
    # Pillow averages RGBA with premultiplied alpha: colour is the fruit's
    # own mean, alpha its share of the frame pixel.
    small = only.resize((FW, FH), Image.BOX).load()
    pal = [rgb(h)[:3] for cols in sp.fruit["ramps"] for h in cols]
    out = frame.copy()
    op = out.load()
    for y in range(FH):
        for x in range(FW):
            c = small[x, y]
            if c[3] >= 128:
                op[x, y] = nearest(c[:3], pal) + (255,)
    return out


def look_frames(sp, look, seed, branches, twigs):
    """A look's frames. A leafy look is the still tree, then the puff at the
    end of the loop; the shadow stays the still tree's, so the ground under
    it does not flicker. A fruit look is its picked look's frames with the
    fruit laid on (add_fruit), so picking changes no other pixel."""
    fruity = look in sp.fruit_looks

    def frame(gust, shadow=None):
        body, sh, mask, ramps = paint(sp, look, seed, branches, twigs, gust=gust, fruit=not fruity)
        out = shrink(sp, look, body, shadow if shadow is not None else sh, mask, ramps)
        if fruity:
            fruited = paint(sp, look, seed, branches, twigs, gust=gust)[0]
            out = add_fruit(out, body, fruited, sp)
        return out, sh
    still, shadow = frame(None)
    if look not in sp.leafy:
        return [still]
    frames = [still] * FRAMES
    for i, (stir, lean) in enumerate(zip(PUFF, LEAN)):
        frames[PUFF_AT + i] = frame((i, stir, lean), shadow)[0]
    return frames


def build_sheet(sp, seed):
    use_frame(sp)
    branches, twigs = colonize(sp, seed)
    sheet = Image.new("RGBA", (FW * FRAMES, FH * len(sp.looks)), (0, 0, 0, 0))
    for row, look in enumerate(sp.looks):
        for col, frame in enumerate(look_frames(sp, look, seed, branches, twigs)):
            sheet.paste(frame, (col * FW, row * FH))
    return sheet


def check_frames(out_dir, species):
    """Every frame of every look stands on the catalog anchor and fits its
    frame: the bole's last solid row (just above the anchor row, which the
    bark only partly covers) is solid across the middle columns, and nothing
    opaque touches the frame's left, right or top edge, where it would be
    cut off flat. (Roots and fallen leaves reach below the anchor row, as
    the art it replaces does.) A leafy loop starts still: its first frame is
    the look's still tree, as the bare looks' only frame is."""
    ok = True
    for sp_name in species:
        sp = SPECIES[sp_name]
        use_frame(sp)
        for name, _seed in sp.variants:
            sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
            if sheet.size != (FW * FRAMES, FH * len(sp.looks)):
                print("%s: sheet is %dx%d, want %dx%d" % (name, sheet.size[0], sheet.size[1], FW * FRAMES, FH * len(sp.looks)))
                ok = False
                continue
            row = round(FH * ANCHOR_Y) - 1
            for r, look in enumerate(sp.looks):
                bad = []
                for col in range(frame_count(sp, look)):
                    x0, y0 = col * FW, r * FH
                    solid = all(sheet.getpixel((x0 + x, y0 + row))[3] == 255 for x in range(FW // 2 - 3, FW // 2 + 4))
                    edge = any(sheet.getpixel((x0 + x, y0 + y))[3] == 255 for y in range(FH) for x in (0, FW - 1)) or \
                        any(sheet.getpixel((x0 + x, y0))[3] == 255 for x in range(FW))
                    if not solid or edge:
                        bad.append(col)
                ok = ok and not bad
                print("%-10s %-17s %2d frame(s)%s" % (name, look, frame_count(sp, look),
                                                     "" if not bad else "  <- off the anchor or on the frame edge: %s" % bad))
            # Picking changes nothing but the fruit: a fruit look's frames
            # differ from its picked look's only where they show fruit.
            fruit_pal = {rgb(h) for cols in sp.fruit["ramps"] for h in cols} if sp.fruit else set()
            for look, picked in getattr(sp, "picked", {}).items():
                r1, r2 = sp.looks.index(look), sp.looks.index(picked)
                stray = sum(1 for y in range(FH) for x in range(FW * FRAMES)
                            if sheet.getpixel((x, r1 * FH + y)) != sheet.getpixel((x, r2 * FH + y))
                            and sheet.getpixel((x, r1 * FH + y)) not in fruit_pal)
                ok = ok and not stray
                print("%-10s %-17s picked is %s%s" % (name, look, picked,
                                                     "" if not stray else "  <- %d pixels other than fruit differ" % stray))
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's trees.")
    parser.add_argument("--species", default=",".join(SPECIES), help="comma-separated, of: " + ", ".join(SPECIES))
    parser.add_argument("--out", default=OUT_DIR, help="where the sheets go (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheets instead of painting them")
    args = parser.parse_args()
    out_dir = args.out
    species = args.species.split(",")
    for name in species:
        if name not in SPECIES:
            parser.error("unknown species %r (have %s)" % (name, ", ".join(SPECIES)))
    if args.check:
        sys.exit(0 if check_frames(out_dir, species) else 1)
    os.makedirs(out_dir, exist_ok=True)
    for sp_name in species:
        sp = SPECIES[sp_name]
        for name, seed in sp.variants:
            build_sheet(sp, seed).save(os.path.join(out_dir, name + ".png"))
            print("painted", name)


if __name__ == "__main__":
    main()
