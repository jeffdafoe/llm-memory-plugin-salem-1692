"""Paints salem's storm damage (LLM-967): the snapped trees that fall across a
road, the stumps they leave, and the broken log. Every pixel is drawn here.

    python tools/tree-art/fallen.py
    python tools/tree-art/fallen.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/fallen.py --check     # sheets match the geometry, stumps clear of the logs

Writes, under client/assets/trees/:
  fallen-tops.png   the snapped tops: a row per species and pose (maple,
                    chestnut, birch; up, down), the "bare" and "winter" tops
                    side by side, each pose on its own frame
  storm-stumps.png  the six stumps (<species>-bare, <species>-winter), one frame
  fallen-trunk.png  the broken log, 32x32, its six season looks left to right
and engine/sim/testdata/fallen-trees-snapped.json, the geometry the engine and
migration LLM-967f-fallen-trees carry: each frame's rect, each pose's anchor
and footprint, and where its stump stands. TestRoadVariantsMatchTheArt holds
the engine's road variants to that file.

Rerunning writes the same files. It imports build.py (the standing trees), so
a fallen tree is one of our standing trees: the "bare" top is the species'
first standing variant, the "winter" top its second (two trees, not two
seasons: the engine picks either at random, all year, as it did with the pack
art these replace). After a change, run `godot --headless --editor --quit` in
client/ so Godot writes the .import sidecars, and commit them.

How a snapped tree is made (approved by Jeff 2026-11-15):
- The standing tree's bare look is painted at S times size (build.paint), the
  same skeleton and bark, and cut low on the trunk (STUMP_H) along a ragged
  zigzag. Everything under the cut, roots included, is the stump.
- Both break faces get pale splintered heartwood: a band along the cut and a
  few splinters, up from the stump and down from the butt.
- The top falls flat. In the top-down view that is its picture turned 45
  degrees (crown up-left, "up") or 135 (crown down-left, "down") about the
  break, and laid with its butt GAP px from the stump's foot.
- To pixels: area average, snapped to the standing tree's own colours, then
  maple and chestnut bark lifted toward white and greyed (LIFT): dark bare
  tops blended into a dirt road's ruts with the pack art too (LLM-789). The
  standing trees' outline (stronger on down-facing edges) and a (0, 3) drop
  shadow; the stump gets a small cast shadow like the standing trees'.

Engine geometry, measured off the result, in sheet px with the stump's foot
at the origin (render scale 2 doubles it in the world):
- anchor: the centre of the 16-px cell holding the centroid of the wood of
  both tops of a pose, on a grid with a cell centred on the stump's foot (so
  the stump stands mid-tile); every frame starts and ends on that cell grid,
  so the footprint follows the tile grid.
- footprint: the box of cells round the anchor cell holding at least
  FOOTPRINT_MIN_PX of wood in either top, trimmed so the stump's cell is off it.
- stump: the foot's offset from the anchor, x2 (stump_world_dx/dy), and the
  tile that lands in (stump_tile_dx/dy), as the engine's stumpOffset finds it.

The broken log (the catalog's Fallen Trunk, the road's one look with no stump)
is a short length of the fallen maple's trunk on the diagonal: splintered at
its far end, a broken face with a hollow heart at the near end, with the
season's fallen leaves or snow on it. Its anchor and footprint are the
catalog's (0.75, 0.75; one tile left and up), so it keeps its place.
"""

import argparse
import json
import math
import os
import random
import sys

from PIL import Image, ImageDraw

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import build as B  # noqa: E402  (the standing trees)

ROOT = B.ROOT
TREE_DIR = os.path.join(ROOT, "client", "assets", "trees")
GEOMETRY = os.path.join(ROOT, "engine", "sim", "testdata", "fallen-trees-snapped.json")
S = B.S

SPECIES = ("maple", "chestnut", "birch")
STATES = (("bare", 0), ("winter", 1))         # top state -> the species' standing variant it is
POSES = (("up", 45.0, (-1, -1)), ("down", 135.0, (-1, 1)))   # degrees turned CCW, fall direction
STUMP_H = {"maple": 13, "chestnut": 11, "birch": 12}         # px from the foot to the break
GAP = 7                  # px from the stump's foot to the butt, along the fall
TOP_SCALE = 0.75         # a top is drawn this size of its standing tree: whole, a lying tree
                         # would block a 5x5-tile square, too big for most road sites
CELL = 16                # sheet px per tile
RENDER_SCALE = 2
FOOTPRINT_MIN_PX = 24
LIFT = {"maple": (0.18, 0.10), "chestnut": (0.18, 0.10), "birch": (0.0, 0.0)}
PALE = ["eadbb4", "d6be8e", "b8996a"]         # splintered heartwood, light to dark
HOLLOW = ["2a1f1c", "3d2c25"]                 # the log's rotten heart

TRUNK_FRAME = 32
TRUNK_LOOKS = ["default", "autumn", "winter-clean", "winter-leaves", "winter-snowy-top", "winter-snowy"]


def lift(c, k, desat):
    """Old deadwood: k of the way toward white, then desat toward grey."""
    r, g, b = (v + (255 - v) * k for v in c)
    m = (r + g + b) / 3
    return tuple(int(round(v + (m - v) * desat)) for v in (r, g, b))


def trunk_at(sp, y):
    """The standing trunk's centre x and half width at canvas y."""
    top = getattr(sp, "bole_top", sp.trunk_top)
    t = (B.BASE_Y - y) / (B.BASE_Y - top)
    return B.W * 0.5 + sp.lean * t, (sp.trunk_w[0] + (sp.trunk_w[1] - sp.trunk_w[0]) * t) / 2


def standing_colours(sp, seed, branches, twigs):
    """Every opaque colour of the standing tree's winter-clean frame, so the
    fallen tree is snapped to that tree's own bark."""
    frame = B.look_frames(sp, "winter-clean", seed, branches, twigs)[0]
    return sorted({c[:3] for c in frame.getdata() if c[3] == 255})


def snap_tree(name, sp, seed, branches, twigs, rnd):
    """The bare tree at S, cut in two: (top, stump, break centre)."""
    body = B.paint(sp, "winter-clean", seed, branches, twigs)[0]
    hb = B.BASE_Y - STUMP_H[name] * S
    bx, hw = trunk_at(sp, hb)
    # the ragged break: a zigzag across the trunk, deepest mid-trunk
    xs = [bx - hw - 3 * S + i * 1.5 * S for i in range(int((2 * hw + 6 * S) / (1.5 * S)) + 2)]
    cut = [(x, hb + rnd.uniform(-2.6, 2.6) * S * (1.2 - abs(x - bx) / (hw + 3 * S))) for x in xs]
    below = Image.new("L", body.size, 0)
    # everything under the cut across the whole canvas: the roots spread wider than the trunk
    edge = [(0, cut[0][1])] + cut + [(body.size[0], cut[-1][1])]
    ImageDraw.Draw(below).polygon(edge + [(body.size[0], body.size[1]), (0, body.size[1])], fill=255)
    stump = Image.new("RGBA", body.size, (0, 0, 0, 0))
    stump.paste(body, (0, 0), below)
    top = Image.new("RGBA", body.size, (0, 0, 0, 0))
    top.paste(body, (0, 0), below.point(lambda v: 255 - v))
    # pale broken wood: a band each side of the cut, and splinters up from the
    # stump and down from the butt
    ds, dt = ImageDraw.Draw(stump), ImageDraw.Draw(top)
    inside = [(x, y) for (x, y) in cut if abs(x - bx) < hw - 0.6 * S]
    for i in range(len(inside) - 1):
        (x0, y0), (x1, y1) = inside[i], inside[i + 1]
        ds.polygon([(x0, y0), (x1, y1), (x1, y1 + 2.2 * S), (x0, y0 + 2.2 * S)], fill=B.rgb(PALE[rnd.randrange(3)]))
        dt.polygon([(x0, y0), (x1, y1), (x1, y1 - 2.0 * S), (x0, y0 - 2.0 * S)], fill=B.rgb(PALE[rnd.randrange(3)]))
    for _ in range(3):
        x = rnd.uniform(bx - hw * 0.7, bx + hw * 0.7)
        near = [c[1] for c in cut if abs(c[0] - x) < 1.6 * S]
        y = min(near) if near else hb
        h = rnd.uniform(2.5, 5.5) * S
        ds.polygon([(x - 0.9 * S, y + 0.5 * S), (x + 0.9 * S, y + 0.5 * S), (x + rnd.uniform(-0.6, 0.6) * S, y - h)],
                   fill=B.rgb(PALE[rnd.randrange(2)]))
    for _ in range(2):
        x = rnd.uniform(bx - hw * 0.7, bx + hw * 0.7)
        h = rnd.uniform(2.0, 4.5) * S
        dt.polygon([(x - 0.8 * S, hb - 1.0 * S), (x + 0.8 * S, hb - 1.0 * S), (x + rnd.uniform(-0.6, 0.6) * S, hb + h)],
                   fill=B.rgb(PALE[rnd.randrange(2)]))
    return top, stump, (bx, hb)


def to_pixels(img, colours, recolour, drop=True, bare_edge=()):
    """Area average to 1x; every half-covered pixel snapped to colours and
    then mapped through recolour; the standing trees' outline, none round
    the colours in bare_edge (an outline round snow reads as a hole); and,
    with drop, the (0, 3) drop shadow."""
    small = img.resize((img.width // S, img.height // S), Image.BOX)
    px = small.load()
    w, h = small.size
    covered = {(x, y): px[x, y][:3] for y in range(h) for x in range(w) if px[x, y][3] >= 128}
    out = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    op = out.load()
    if drop:
        for (x, y) in covered:
            if y + 3 < h and (x, y + 3) not in covered:
                op[x, y + 3] = B.SHADOW
    for (x, y), c in covered.items():
        snapped = recolour[B.nearest(c, colours)]
        k = 0.0
        if sum((x + dx, y + dy) in covered for dx in (-1, 0, 1) for dy in (-1, 0, 1)) >= 5 and snapped not in bare_edge:
            if (x, y + 1) not in covered:
                k = 0.72
            elif any((x + dx, y + dy) not in covered for dx, dy in ((1, 0), (-1, 0), (0, -1))):
                k = 0.5
        if k:
            snapped = B.mix(snapped, B.OUTLINE, k)[:3]
        op[x, y] = snapped + (255,)
    return out


def wood(img):
    """The opaque pixels: wood and its outline, not the half-clear shadow."""
    a = img.getchannel("A").load()
    return [(x, y) for y in range(img.height) for x in range(img.width) if a[x, y] == 255]


def floor_cell(v):
    """The cell holding v, on a grid whose cell CENTRES are the multiples of
    CELL: the stump's foot (the origin) is then a cell centre, so it stands
    in the middle of the tile it blocks, wherever the top lands."""
    return int(math.floor((v + CELL / 2) / CELL))


def snapped_trees():
    """Per species: the two trees (stump images, and per pose the 1x top
    with its world origin), at 1x, stump foot at the world origin."""
    out = {}
    for name in SPECIES:
        sp = B.SPECIES[name]
        B.use_frame(sp)
        foot = (B.W / S / 2, B.BASE_Y / S)       # the stump's foot in its own image
        trees = {}
        for state, vi in STATES:
            seed = sp.variants[vi][1]
            branches, twigs = B.colonize(sp, seed)
            top, stump, (bx, hb) = snap_tree(name, sp, seed, branches, twigs, random.Random(seed * 31 + 7))
            base = standing_colours(sp, seed, branches, twigs)
            k, ds = LIFT[name]
            colours = base + [B.rgb(c)[:3] for c in PALE]
            recolour = {c: lift(c, k, ds) for c in base}
            recolour.update({B.rgb(c)[:3]: B.rgb(c)[:3] for c in PALE})
            small = to_pixels(stump, colours, recolour, drop=False)
            st = Image.new("RGBA", small.size, (0, 0, 0, 0))
            ImageDraw.Draw(st).ellipse((foot[0] - 4, foot[1] - 2, foot[0] + 9, foot[1] + 2), fill=B.SHADOW)
            st.alpha_composite(small)
            poses = {}
            for pose, angle, (dx, dy) in POSES:
                big = Image.new("RGBA", (top.width * 2, top.width * 2), (0, 0, 0, 0))
                c = big.width // 2               # the break lands on the centre and stays there
                big.paste(top, (int(c - bx), int(c - hb)))
                turned = big.rotate(angle, resample=Image.NEAREST, center=(c, c))
                # shrink about the break (the centre), keeping the canvas a multiple of S
                side = int(round(big.width * TOP_SCALE / S)) * S
                turned = turned.resize((side, side), Image.BOX)
                c = side // 2
                turned = to_pixels(turned, colours, recolour)
                # the butt lies GAP px from the foot along the fall
                origin = (int(round(dx * GAP / math.sqrt(2))) - c // S, int(round(dy * GAP / math.sqrt(2))) - c // S)
                poses[pose] = (turned, origin)
            trees[state] = (st, foot, poses)
        out[name] = trees
    return out


def top_geometry(name, pose, tops):
    """Anchor, frame and footprint for one pose's two tops; tops maps state
    -> (image, world origin)."""
    pts = {s: [(x + o[0], y + o[1]) for (x, y) in wood(img)] for s, (img, o) in tops.items()}
    every = [p for s in pts for p in pts[s]]
    cx = sum(p[0] for p in every) / len(every)
    cy = sum(p[1] for p in every) / len(every)
    acx, acy = floor_cell(cx), floor_cell(cy)             # the anchor cell
    anchor = (acx * CELL, acy * CELL)
    drawn = []
    for img, o in tops.values():
        bb = img.getbbox()
        # a clear pixel round the art, then out to the cell grid
        drawn += [(bb[0] - 1 + o[0], bb[1] - 1 + o[1]), (bb[2] + o[0], bb[3] + o[1])]
    x0 = floor_cell(min(p[0] for p in drawn)) * CELL - CELL // 2
    y0 = floor_cell(min(p[1] for p in drawn)) * CELL - CELL // 2
    x1 = (floor_cell(max(p[0] for p in drawn)) + 1) * CELL - CELL // 2
    y1 = (floor_cell(max(p[1] for p in drawn)) + 1) * CELL - CELL // 2
    # wood per cell, relative to the anchor cell, in either top
    count = {}
    for s in pts:
        per = {}
        for (x, y) in pts[s]:
            key = (floor_cell(x) - acx, floor_cell(y) - acy)
            per[key] = per.get(key, 0) + 1
        for key, n in per.items():
            count[key] = max(count.get(key, 0), n)
    cells = [k for k, n in count.items() if n >= FOOTPRINT_MIN_PX] + [(0, 0)]
    box = [-min(c[0] for c in cells), max(c[0] for c in cells), -min(c[1] for c in cells), max(c[1] for c in cells)]
    stump_world = (-anchor[0] * RENDER_SCALE, -anchor[1] * RENDER_SCALE)
    # the engine's stumpOffset: a top sits at its tile's centre (16 world px in)
    sdx = int(math.floor((CELL * RENDER_SCALE / 2 + stump_world[0]) / (CELL * RENDER_SCALE)))
    sdy = int(math.floor((CELL * RENDER_SCALE / 2 + stump_world[1]) / (CELL * RENDER_SCALE)))
    left, right, top, bottom = box
    if -left <= sdx <= right and -top <= sdy <= bottom:
        # trim the side that gives up the least wood
        options = []
        if sdx > 0:
            options.append(("right", sdx - 1))
        if sdx < 0:
            options.append(("left", -sdx - 1))
        if sdy > 0:
            options.append(("bottom", sdy - 1))
        if sdy < 0:
            options.append(("top", -sdy - 1))

        def kept(opt):
            side, v = opt
            b = dict(left=left, right=right, top=top, bottom=bottom)
            b[side] = v
            return sum(n for (kx, ky), n in count.items() if -b["left"] <= kx <= b["right"] and -b["top"] <= ky <= b["bottom"])
        side, v = max(options, key=kept)
        b = dict(left=left, right=right, top=top, bottom=bottom)
        b[side] = v
        left, right, top, bottom = b["left"], b["right"], b["top"], b["bottom"]
    return dict(anchor=anchor, frame=(x0, y0, x1, y1), footprint=(left, right, top, bottom),
                stump_world=stump_world, stump_tile=(sdx, sdy))


def paint_log(look, seed=5):
    """The broken log at S on its 32x32 frame."""
    rnd = random.Random(seed)
    F = TRUNK_FRAME
    img = Image.new("RGBA", (F * S, F * S), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    p0, p1, r = (9.0, 11.0), (22.5, 23.0), 5.2     # far end, near end, radius (frame px)
    L = math.hypot(p1[0] - p0[0], p1[1] - p0[1])
    ux, uy = (p1[0] - p0[0]) / L, (p1[1] - p0[1]) / L      # along, far -> near
    nx, ny = uy, -ux                                        # across; +n the upper side

    def at(t, s):
        return (p0[0] + ux * L * t + nx * r * s, p0[1] + uy * L * t + ny * r * s)

    def bark(t):
        return lift(B.ramp(B.BARK, t)[:3], *LIFT["maple"]) + (255,)
    steps = 60
    for i in range(steps):                     # the body, lit on its upper side
        s0, s1 = -1 + 2 * i / steps, -1 + 2 * (i + 1) / steps
        d.polygon([(x * S, y * S) for x, y in (at(0, s0), at(1, s0), at(1, s1), at(0, s1))],
                  fill=bark(0.25 + 0.55 * (s0 + 1) / 2))
    for _ in range(26):                        # furrows: broken dark streaks along it
        s = rnd.uniform(-0.9, 0.9)
        t0 = rnd.uniform(0.05, 0.8)
        t1 = min(0.97, t0 + rnd.uniform(0.12, 0.35))
        a, b = at(t0, s), at(t1, s)
        d.line([(a[0] * S, a[1] * S), (b[0] * S, b[1] * S)], fill=bark(0.12 + 0.3 * (s + 1) / 2), width=int(0.8 * S))
    for k in range(4):                         # far end: splinter teeth
        s = -0.72 + 1.44 * k / 3 + rnd.uniform(-0.08, 0.08)
        h = rnd.uniform(3.0, 7.0) * (1.0 - 0.35 * abs(s))
        base = at(0, s)
        tip = (base[0] - ux * h + nx * rnd.uniform(-0.6, 0.6), base[1] - uy * h + ny * rnd.uniform(-0.6, 0.6))
        w = 1.55
        pale = k in (1, 2) or rnd.random() < 0.5
        col = B.rgb(PALE[rnd.randrange(2)]) if pale else bark(0.4 + 0.3 * (s + 1) / 2)
        d.polygon([((base[0] - nx * w) * S + ux * S, (base[1] - ny * w) * S + uy * S),
                   ((base[0] + nx * w) * S + ux * S, (base[1] + ny * w) * S + uy * S),
                   (tip[0] * S, tip[1] * S)], fill=col)
    face = Image.new("RGBA", img.size, (0, 0, 0, 0))
    fd = ImageDraw.Draw(face)                  # near end: rings round a hollow heart
    for rr, col in ((1.0, bark(0.5)), (0.86, B.rgb(PALE[1])), (0.72, B.rgb(PALE[0])), (0.62, B.rgb(PALE[2])),
                    (0.52, B.rgb(PALE[1])), (0.42, B.rgb(HOLLOW[1])), (0.30, B.rgb(HOLLOW[0]))):
        a, b = r * rr * S, r * rr * 0.55 * S   # an ellipse, long across the log
        fd.polygon([(p1[0] * S + a * math.cos(t) * nx + b * math.sin(t) * ux,
                     p1[1] * S + a * math.cos(t) * ny + b * math.sin(t) * uy)
                    for t in [i * math.pi / 24 for i in range(48)]], fill=col)
    img.alpha_composite(face)
    if look in ("autumn", "winter-leaves"):
        cols = B.SPECIES["maple"].fallen if look == "autumn" else B.DRY_LEAVES
        for _ in range(14 if look == "autumn" else 9):
            x, y = at(rnd.uniform(0.0, 1.0), rnd.uniform(-1.7, 1.2))
            rr = rnd.uniform(0.7, 1.1)
            if 2 < x < F - 2 and 2 < y < F - 2:
                d.ellipse(((x - rr * 1.3) * S, (y - rr) * S, (x + rr * 1.3) * S, (y + rr) * S), fill=B.rgb(rnd.choice(cols)))
    if look.startswith("winter-snowy"):
        deep = look == "winter-snowy"
        for i in range(40):                    # snow along the top
            x, y = at(i / 39, 0.15 if deep else 0.55)
            rr = 2.6 if deep else 1.4
            d.ellipse(((x - rr) * S, (y - rr * 0.8) * S, (x + rr) * S, (y + rr * 0.8) * S), fill=B.rgb("eef3f7"))
        if deep:
            for i in range(30):                # banked along the lower side
                x, y = at(i / 29, -0.9)
                d.ellipse(((x - 2.2) * S, (y - 1.2) * S, (x + 2.2) * S, (y + 1.6) * S), fill=B.rgb("e2e9ef"))
    return img


def log_frame(look):
    colours = [lift(B.ramp(B.BARK, k / 10)[:3], *LIFT["maple"]) for k in range(11)]
    colours += [B.rgb(h)[:3] for h in PALE + HOLLOW + B.SPECIES["maple"].fallen + B.DRY_LEAVES + B.SNOW]
    colours = list(dict.fromkeys(colours))
    snow = {B.rgb(h)[:3] for h in B.SNOW}
    out = to_pixels(paint_log(look), colours, {c: c for c in colours}, drop=False, bare_edge=snow)
    shadow = Image.new("RGBA", out.size, (0, 0, 0, 0))
    a, sp_ = out.getchannel("A").load(), shadow.load()
    for y in range(out.height):
        for x in range(out.width):
            if a[x, y] and y + 2 < out.height and not a[x, y + 2]:
                sp_[x, y + 2] = B.SHADOW
    shadow.alpha_composite(out)
    return shadow


def build_all():
    """(sheets by file name, geometry dict)."""
    trees = snapped_trees()
    geo = {"source": "tools/tree-art/fallen.py", "stumps": [], "tops": [], "trunk": {}}
    # stumps: one shared frame round every stump, anchored at the foot
    stumps = [(name + "-" + state, trees[name][state][0]) for name in SPECIES for state, _ in STATES]
    foot = trees[SPECIES[0]][STATES[0][0]][1]
    boxes = [img.getbbox() for _, img in stumps]
    x0, y0 = min(b[0] for b in boxes) - 1, min(b[1] for b in boxes) - 1
    x1, y1 = max(b[2] for b in boxes) + 1, max(b[3] for b in boxes) + 1
    sw, sh = x1 - x0, y1 - y0
    stump_sheet = Image.new("RGBA", (sw * len(stumps), sh), (0, 0, 0, 0))
    for i, (state, img) in enumerate(stumps):
        stump_sheet.alpha_composite(img.crop((x0, y0, x1, y1)), (i * sw, 0))
        geo["stumps"].append({"state": state, "rect": [i * sw, 0, sw, sh],
                              "anchor_x": round((foot[0] - x0) / sw, 6), "anchor_y": round((foot[1] - y0) / sh, 6)})
    # tops: a row per species and pose, the two states side by side
    rows, width, height = [], 0, 0
    for name in SPECIES:
        for pose, _, _ in POSES:
            tops = {state: trees[name][state][2][pose] for state, _ in STATES}
            g = top_geometry(name, pose, tops)
            fx0, fy0, fx1, fy1 = g["frame"]
            fw, fh = fx1 - fx0, fy1 - fy0
            frames = []
            for i, (state, _) in enumerate(STATES):
                img, o = tops[state]
                cell = Image.new("RGBA", (fw, fh), (0, 0, 0, 0))
                cell.alpha_composite(img, (o[0] - fx0, o[1] - fy0)) if o[0] >= fx0 and o[1] >= fy0 else \
                    cell.paste(img, (o[0] - fx0, o[1] - fy0), img)
                frames.append((state, cell))
            rows.append((name, pose, g, fw, fh, frames))
            width, height = max(width, fw * len(frames)), height + fh
    tops_sheet = Image.new("RGBA", (width, height), (0, 0, 0, 0))
    y = 0
    for name, pose, g, fw, fh, frames in rows:
        left, right, top, bottom = g["footprint"]
        entry = {"species": name, "pose": pose,
                 "anchor_x": round((g["anchor"][0] - g["frame"][0]) / fw, 6),
                 "anchor_y": round((g["anchor"][1] - g["frame"][1]) / fh, 6),
                 "footprint_left": left, "footprint_right": right, "footprint_top": top, "footprint_bottom": bottom,
                 "stump_tile_dx": g["stump_tile"][0], "stump_tile_dy": g["stump_tile"][1], "frames": []}
        for i, (state, cell) in enumerate(frames):
            tops_sheet.alpha_composite(cell, (i * fw, y))
            entry["frames"].append({"state": state, "rect": [i * fw, y, fw, fh], "stump_state": name + "-" + state,
                                    "stump_world_dx": g["stump_world"][0], "stump_world_dy": g["stump_world"][1]})
        geo["tops"].append(entry)
        y += fh
    # the log: six looks in a row
    trunk_sheet = Image.new("RGBA", (TRUNK_FRAME * len(TRUNK_LOOKS), TRUNK_FRAME), (0, 0, 0, 0))
    for i, look in enumerate(TRUNK_LOOKS):
        trunk_sheet.alpha_composite(log_frame(look), (i * TRUNK_FRAME, 0))
    geo["trunk"] = {"looks": [{"state": look, "rect": [i * TRUNK_FRAME, 0, TRUNK_FRAME, TRUNK_FRAME]}
                              for i, look in enumerate(TRUNK_LOOKS)]}
    return {"fallen-tops.png": tops_sheet, "storm-stumps.png": stump_sheet, "fallen-trunk.png": trunk_sheet}, geo


def check(tree_dir, geometry):
    """The sheets hold every rect, every frame has wood and a clear edge, and
    every stump stands off its log and off the anchor's row and column."""
    geo = json.load(open(geometry, encoding="utf-8"))
    bad = 0
    sheets = {n: Image.open(os.path.join(tree_dir, n)).convert("RGBA")
              for n in ("fallen-tops.png", "storm-stumps.png", "fallen-trunk.png")}

    def frame_ok(sheet, rect, what):
        x, y, w, h = rect
        if x + w > sheet.width or y + h > sheet.height:
            print("%s: rect %s is off the sheet" % (what, rect))
            return False
        cell = sheet.crop((x, y, x + w, y + h))
        if not wood(cell):
            print("%s: no wood in its frame" % what)
            return False
        a = cell.getchannel("A").load()
        if any(a[i, 0] or a[i, h - 1] for i in range(w)) or any(a[0, j] or a[w - 1, j] for j in range(h)):
            print("%s: art touches the frame's edge" % what)
            return False
        return True
    for s in geo["stumps"]:
        bad += not frame_ok(sheets["storm-stumps.png"], s["rect"], "stump " + s["state"])
    for t in geo["tops"]:
        sdx, sdy = t["stump_tile_dx"], t["stump_tile_dy"]
        if sdx == 0 or sdy == 0:
            print("%s %s: stump on the anchor's row or column (%d, %d)" % (t["species"], t["pose"], sdx, sdy)); bad += 1
        if -t["footprint_left"] <= sdx <= t["footprint_right"] and -t["footprint_top"] <= sdy <= t["footprint_bottom"]:
            print("%s %s: stump tile (%d, %d) is under the log" % (t["species"], t["pose"], sdx, sdy)); bad += 1
        for f in t["frames"]:
            bad += not frame_ok(sheets["fallen-tops.png"], f["rect"], "%s %s %s" % (t["species"], t["pose"], f["state"]))
        print("%-8s %-4s anchor (%.3f, %.3f) footprint L%d R%d T%d B%d stump tile (%d, %d) world (%g, %g)" % (
            t["species"], t["pose"], t["anchor_x"], t["anchor_y"], t["footprint_left"], t["footprint_right"],
            t["footprint_top"], t["footprint_bottom"], sdx, sdy, t["frames"][0]["stump_world_dx"], t["frames"][0]["stump_world_dy"]))
    for lk in geo["trunk"]["looks"]:
        bad += not frame_ok(sheets["fallen-trunk.png"], lk["rect"], "log " + lk["state"])
    if bad:
        print("%d problem(s)" % bad)
    return bad == 0


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--out", help="write the sheets and geometry here instead")
    ap.add_argument("--check", action="store_true", help="check the written sheets against the geometry")
    args = ap.parse_args()
    tree_dir = args.out or TREE_DIR
    geometry = os.path.join(args.out, "fallen-trees-snapped.json") if args.out else GEOMETRY
    if args.check:
        sys.exit(0 if check(tree_dir, geometry) else 1)
    sheets, geo = build_all()
    os.makedirs(tree_dir, exist_ok=True)
    for name, img in sheets.items():
        img.save(os.path.join(tree_dir, name))
        print("painted", name, img.size)
    with open(geometry, "w", encoding="utf-8", newline="\n") as f:
        json.dump(geo, f, indent=2)
        f.write("\n")
    print("geometry", geometry)


if __name__ == "__main__":
    main()
