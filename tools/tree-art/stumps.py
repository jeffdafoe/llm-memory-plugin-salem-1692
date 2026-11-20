"""Paints salem's stumps and logs (LLM-967): the forest's old and broken
stumps, its fallen logs, the sawn stumps by the village and the camp's seat.
Every pixel is drawn here, no pack art.

    python tools/tree-art/stumps.py
    python tools/tree-art/stumps.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/stumps.py --check     # every frame inside its frame, sheets their size

Writes client/assets/trees/<piece>.png (big-stump, fallen-log, birch-log,
tree-stump, broken-trunk, small-stump, axe-stump, camp-stump). A forest
piece's sheet has one row per look, top to bottom: summer, autumn, winter,
winter-snowy; the sawn stumps by the village and the camp's seat have the one
look. Every piece is one still frame. Rerunning writes the same files. After
a change, run `godot_console --headless --path client --import` (or the Linux
Godot in WSL) so Godot writes the .import sidecars, and commit them.

It imports build.py (the trees) for its scale, colour helpers, bark ramps and
the outline, shadow and snow colours, and fallen.py (the storm damage) for
its deadwood colours, so these match the storm stumps and the broken log:
painted at build.S times size, area-averaged down, every pixel snapped to the
look's own colours.

How each piece is made, from New England woods:
- A standing stump is a short upright trunk seen from above and in front:
  bark lit on its left, dark on its right, furrowed, roots flaring into the
  ground. A sawn one has a pale cut face with growth rings and a darker heart;
  a broken one ends in pale splintered teeth round a dark hollow.
- A log lies on the ground, lit along its upper side, furrowed, its far end
  splintered and its near end showing rings round a rotten hollow.
- Old wood (the big stump, the fallen log) is greyer, with moss.
- Autumn turns the moss rust and drops leaves at the foot; winter dulls it;
  snow lies on the top surfaces only, with nothing heaped at the foot.
"""

import argparse
import math
import os
import random
import sys

from PIL import Image, ImageDraw

import build as B
import fallen as F

S = B.S
OUT_DIR = B.OUT_DIR
SEASONS = ["summer", "autumn", "winter", "winter-snowy"]
ANCHOR_Y = 0.85

MOSS = {"summer": ["34481c", "4c6626", "688630", "88a63e"],
        "autumn": ["5a3a16", "80521e", "a46c28", "c48a36"],
        "winter": ["3e4430", "545c40", "6c7452"],
        "winter-snowy": ["3e4430", "545c40", "6c7452"]}
LEAVES = ["c8702a", "d99a3a", "a8452a", "b8862e"]
IRON = ["2a2a30", "44444c", "6a6a74", "9a9aa4"]
HAFT = ["4a3220", "6e4c30", "946a44", "b48a5c"]


def bark_ramp(kind):
    """Dark-to-light bark as hex. 'storm' is the storm stumps' lifted maple
    bark; 'old' is greyer deadwood; 'birch' the white birch bark."""
    if kind == "birch":
        return B.BIRCH_BARK
    k, desat = (0.18, 0.10) if kind == "storm" else (0.22, 0.38)
    return ["%02x%02x%02x" % F.lift(B.rgb(h)[:3], k, desat) for h in B.BARK]


# ---------------------------------------------------------------- pieces

class Piece:
    def __init__(self, **kw):
        self.looks = SEASONS
        self.__dict__.update(kw)


PIECES = {
    "big-stump": Piece(frame=(48, 32), seed=31, bark="old",
                       stumps=[dict(dx=0.0, w=17.0, h=10.0, top="broken", roots=6, root_len=8.5, moss=4)]),
    "fallen-log": Piece(frame=(48, 32), seed=37, bark="old",
                        logs=[dict(p0=(6.5, 18.5), p1=(40.0, 22.5), r=5.6, far="splinter", near="hollow", moss=4)]),
    "birch-log": Piece(frame=(48, 32), seed=41, bark="birch",
                       logs=[dict(p0=(5.5, 22.0), p1=(41.5, 24.5), r=3.2, far="splinter", near="solid", moss=0)]),
    "tree-stump": Piece(frame=(32, 32), seed=43, bark="storm",
                        stumps=[dict(dx=0.0, w=11.0, h=11.5, top="broken", roots=5, root_len=5.0, moss=2)]),
    "broken-trunk": Piece(frame=(32, 32), seed=47, bark="storm",
                          logs=[dict(p0=(23.5, 9.0), p1=(9.0, 23.0), r=4.6, far="splinter", near="splinter", moss=2)]),
    "small-stump": Piece(frame=(32, 32), seed=53, bark="storm", looks=["default"],
                         stumps=[dict(dx=0.0, w=13.0, h=7.0, top="sawn", roots=5, root_len=4.0, moss=1)]),
    "axe-stump": Piece(frame=(32, 32), seed=53, bark="storm", looks=["default"], axe=True,
                       stumps=[dict(dx=0.0, w=13.0, h=7.0, top="sawn", roots=5, root_len=4.0, moss=1)]),
    "camp-stump": Piece(frame=(16, 16), seed=59, bark="storm", looks=["default"],
                        stumps=[dict(dx=0.0, dy=1.2, w=8.0, h=5.0, top="sawn", roots=0, root_len=0.0, moss=0)]),
}


def use_frame(fw, fh):
    global FW, FH, W, H, BASE_Y
    FW, FH = fw, fh
    W, H = fw * S, fh * S
    BASE_Y = fh * ANCHOR_Y          # frame px


def P(x, y):
    """Frame px to canvas px."""
    return (x * S, y * S)


def ellipse(d, cx, cy, rx, ry, fill):
    d.ellipse(((cx - rx) * S, (cy - ry) * S, (cx + rx) * S, (cy + ry) * S), fill=fill)


def season(look):
    return "summer" if look == "default" else look


# ---------------------------------------------------------------- a stump

def draw_stump(d, rnd, cx, base, w, h, top, roots, root_len, moss_n, bark, look):
    """An upright stump: roots, the bark round it, then its top."""
    rx, ry = w / 2, w * 0.24
    top_y = base - h

    def b(t):
        return B.ramp(bark, t)
    # Roots: tapering wedges from low on the trunk out along the ground, spread
    # round it; the ones behind it are drawn first, the ones in front over its
    # foot. Each is lit along its upper edge, and the left ones are lighter.
    flare = 1.2                          # the foot is this much wider than the top
    # Directions round the trunk (0 right, pi/2 toward the viewer, pi left),
    # the sides and front first, a back one last if there are six.
    dirs = [math.pi, 0.0, 2.35, 0.8, 1.62, 3.75][:roots]
    root_list = []
    for a in dirs:
        a += rnd.uniform(-0.18, 0.18)
        root_list.append((a, root_len * rnd.uniform(0.7, 1.05), rnd.uniform(0.85, 1.15)))

    def draw_root(a, length, girth):
        ca, sa = math.cos(a), math.sin(a)
        # Where it leaves the trunk: high on it, and at the ground; then the tip.
        xs = cx + ca * rx * flare * 0.9
        ys = base + sa * ry * 0.85
        top = (xs - ca * 0.6, ys - h * 0.36 * girth)
        foot = (xs + ca * 0.4 + sa * 1.2, ys + 0.4)
        # Seen from above at a slant, a root toward the viewer runs mostly
        # down the screen only a little: the ground is foreshortened.
        tip = (cx + ca * (rx * flare + length), base + sa * (ry * 0.7 + length * 0.16) + 0.2)
        back = (xs - sa * 1.4 * girth, ys - 0.2)
        lit = 0.6 if ca < -0.2 else 0.42 if ca < 0.3 else 0.3
        d.polygon([P(*back), P(*top), P(*tip), P(*foot)], fill=b(lit))
        d.line([P(*top), P(*tip)], fill=b(lit + 0.18), width=max(1, int(0.45 * S)))
        d.line([P(*foot), P(*tip)], fill=b(lit - 0.2), width=max(1, int(0.45 * S)))
    for a, length, girth in root_list:
        if math.sin(a) < 0:
            draw_root(a, length, girth)
    # The body: an upright trunk, flaring a little to its foot, lit on the left.
    steps = 24
    for i in range(steps):
        u0, u1 = -1 + 2 * i / steps, -1 + 2 * (i + 1) / steps
        um = (u0 + u1) / 2
        yb = base + ry * math.sqrt(max(0.0, 1 - um * um)) * 0.9
        col = b(0.78 - 0.55 * (u0 + 1) / 2)
        d.polygon([P(cx + u0 * rx, top_y), P(cx + u1 * rx + 0.05, top_y),
                   P(cx + u1 * rx * flare + 0.05, yb), P(cx + u0 * rx * flare, yb)], fill=col)
    for a, length, girth in sorted(root_list, key=lambda r: math.sin(r[0])):
        if math.sin(a) >= 0:
            draw_root(a, length, girth)
    # Furrows: broken dark streaks down the bark.
    for _ in range(int(w * 1.1)):
        u = rnd.uniform(-0.92, 0.92)
        y0 = top_y + rnd.uniform(0.3, 0.7) * h * rnd.random()
        y1 = min(base + ry * 0.6, y0 + rnd.uniform(0.25, 0.7) * h)
        x = cx + u * rx
        d.line([P(x, y0), P(cx + u * rx * (1 + (flare - 1) * (y1 - top_y) / h) + rnd.uniform(-0.3, 0.3), y1)], fill=b(0.12 + 0.3 * (0.6 - u * 0.5) / 1.1),
               width=max(1, int(0.45 * S)))
    if top == "sawn":
        # The cut face: bark rim, pale wood with rings, a darker heart and a
        # check (a radial crack) from the heart to the rim.
        ellipse(d, cx, top_y, rx, ry, b(0.45))
        for rr, col in ((0.88, F.PALE[1]), (0.74, F.PALE[0]), (0.6, F.PALE[1]), (0.46, F.PALE[0]),
                        (0.32, F.PALE[2]), (0.16, F.PALE[1])):
            ellipse(d, cx - 0.2, top_y - 0.1, rx * rr, ry * rr, B.rgb(col))
        a = rnd.uniform(0.3, 1.0)
        d.line([P(cx, top_y), P(cx + math.cos(a) * rx * 0.85, top_y + math.sin(a) * ry * 0.85)],
               fill=B.rgb(F.PALE[2]), width=max(1, int(0.4 * S)))
    else:
        # A broken top: the hollow, then pale splintered teeth round it, the
        # back ones taller, every tooth lit on its left.
        ellipse(d, cx, top_y, rx, ry, b(0.35))
        ellipse(d, cx + 0.2, top_y + 0.1, rx * 0.72, ry * 0.7, B.rgb(F.HOLLOW[1]))
        ellipse(d, cx + 0.4, top_y + 0.2, rx * 0.5, ry * 0.5, B.rgb(F.HOLLOW[0]))
        n = max(5, int(w * 0.6))
        teeth = []
        for k in range(n):
            a = math.pi + k * math.pi / (n - 1)          # the back half, left to right
            teeth.append((a, True))
        for k in range(n - 1):
            a = (k + 0.5) * math.pi / (n - 1)            # the front half
            teeth.append((a, False))
        for a, back in sorted(teeth, key=lambda t: math.sin(t[0])):
            bx, by = cx + math.cos(a) * rx * 0.9, top_y + math.sin(a) * ry * 0.9
            tall = rnd.uniform(1.2, 3.6) * (1.25 if back else 0.6) * (h / 10) ** 0.5
            half = rnd.uniform(0.6, 1.1)
            lit = math.cos(a) < 0.2
            col = B.rgb(F.PALE[0 if lit else 2]) if rnd.random() < 0.6 else b(0.6 if lit else 0.3)
            d.polygon([P(bx - half, by + 0.6), P(bx + half, by + 0.6),
                       P(bx + rnd.uniform(-0.5, 0.5), by - tall)], fill=col)
    # Moss on the top rim and low on the shaded side.
    moss = MOSS[season(look)]
    for _ in range(moss_n):
        mx = cx + rnd.uniform(-0.7, 0.8) * rx
        my = rnd.choice((top_y + ry * 0.6, base - h * rnd.uniform(0.1, 0.4)))
        r = rnd.uniform(0.8, 1.4)
        ellipse(d, mx, my, r, r * 0.65, B.ramp(moss, 0.35))
        ellipse(d, mx - 0.2, my - 0.2, r * 0.6, r * 0.4, B.ramp(moss, 0.85))
    if look == "winter-snowy":
        if top == "sawn":
            ellipse(d, cx - 0.2, top_y - 0.15, rx * 0.95, ry * 0.92, B.rgb(B.SNOW[1]))
            ellipse(d, cx - 0.6, top_y - 0.35, rx * 0.6, ry * 0.5, B.rgb(B.SNOW[2]))
        else:
            # Snow in the hollow and on the lower teeth's shoulders.
            ellipse(d, cx + 0.2, top_y + 0.1, rx * 0.7, ry * 0.65, B.rgb(B.SNOW[1]))
            ellipse(d, cx - 0.3, top_y - 0.1, rx * 0.4, ry * 0.35, B.rgb(B.SNOW[2]))
        for k in range(roots):
            side = -1 if k % 2 == 0 else 1
            x = cx + side * (rx + 1.2)
            ellipse(d, x, base - 0.6, 1.1, 0.5, B.rgb(B.SNOW[1]))
    return top_y - 4.0


# ---------------------------------------------------------------- a log

def draw_log(d, rnd, p0, p1, r, far, near, moss_n, bark, look, birch=False):
    """A log lying from p0 (far end) to p1 (near end), lit on its upper side."""
    L = math.hypot(p1[0] - p0[0], p1[1] - p0[1])
    ux, uy = (p1[0] - p0[0]) / L, (p1[1] - p0[1]) / L
    nx, ny = uy, -ux
    if ny > 0:                                   # +n is the upper side
        nx, ny = -nx, -ny

    def at(t, s):
        return (p0[0] + ux * L * t + nx * r * s, p0[1] + uy * L * t + ny * r * s)

    def b(t):
        return B.ramp(bark, t)
    steps = 40
    for i in range(steps):
        s0, s1 = -1 + 2 * i / steps, -1 + 2 * (i + 1) / steps
        d.polygon([P(*at(0, s0)), P(*at(1, s0)), P(*at(1, s1)), P(*at(0, s1))],
                  fill=b(0.2 + 0.6 * ((s0 + s1) / 2 + 1) / 2))
    if birch:
        # Birch: black dashes across the white bark, darker toward the ground.
        for _ in range(int(L * 0.9)):
            t = rnd.uniform(0.04, 0.96)
            s = rnd.uniform(-0.8, 0.9)
            a, c = at(t, s), at(t + rnd.uniform(0.02, 0.05), s + rnd.uniform(-0.1, 0.1))
            d.line([P(*a), P(*c)], fill=B.rgb("1e1c1a"), width=max(1, int(0.5 * S)))
        for _ in range(6):
            t = rnd.uniform(0.1, 0.9)
            ellipse(d, *at(t, -0.6), 1.2, 0.7, b(0.25))
    else:
        for _ in range(int(L * 0.9)):
            s = rnd.uniform(-0.9, 0.9)
            t0 = rnd.uniform(0.03, 0.85)
            t1 = min(0.97, t0 + rnd.uniform(0.08, 0.25))
            d.line([P(*at(t0, s)), P(*at(t1, s))], fill=b(0.1 + 0.3 * (s + 1) / 2), width=max(1, int(0.7 * S)))

    def splinters(t, sign):
        u = (-ux, -uy) if sign < 0 else (ux, uy)
        for k in range(5):
            s = -0.78 + 1.56 * k / 4 + rnd.uniform(-0.08, 0.08)
            hgt = rnd.uniform(1.6, 4.2) * (1.0 - 0.35 * abs(s)) * (r / 5) ** 0.5
            base = at(t, s)
            tip = (base[0] + u[0] * hgt + nx * rnd.uniform(-0.5, 0.5), base[1] + u[1] * hgt + ny * rnd.uniform(-0.5, 0.5))
            w = 0.5 * r / 2.5
            pale = k in (1, 2, 3) or rnd.random() < 0.5
            col = B.rgb(F.PALE[rnd.randrange(2)]) if pale else b(0.4 + 0.3 * (s + 1) / 2)
            d.polygon([P(base[0] - nx * w - u[0], base[1] - ny * w - u[1]), P(base[0] + nx * w - u[0], base[1] + ny * w - u[1]),
                       P(*tip)], fill=col)

    def end_face(t, hollow):
        cx, cy = at(t, 0)
        rings = [(1.0, b(0.5)), (0.86, B.rgb(F.PALE[1])), (0.72, B.rgb(F.PALE[0])), (0.58, B.rgb(F.PALE[2]))]
        rings += [(0.46, B.rgb(F.HOLLOW[1])), (0.32, B.rgb(F.HOLLOW[0]))] if hollow else \
            [(0.44, B.rgb(F.PALE[1])), (0.24, B.rgb(F.PALE[2]))]
        for rr, col in rings:
            a, c = r * rr, r * rr * 0.55
            d.polygon([P(cx + a * math.cos(q) * nx + c * math.sin(q) * ux, cy + a * math.cos(q) * ny + c * math.sin(q) * uy)
                       for q in [i * math.pi / 24 for i in range(48)]], fill=col)
    if far == "splinter":
        splinters(0.0, -1)
    if near == "splinter":
        splinters(1.0, 1)
    else:
        end_face(1.0, near == "hollow")
    moss = MOSS[season(look)]
    for _ in range(moss_n):
        x, y = at(rnd.uniform(0.15, 0.85), rnd.uniform(0.3, 0.8))
        rr = rnd.uniform(0.9, 1.5)
        ellipse(d, x, y, rr * 1.3, rr * 0.7, B.ramp(moss, 0.35))
        ellipse(d, x - 0.2, y - 0.2, rr * 0.8, rr * 0.4, B.ramp(moss, 0.85))
    if look == "winter-snowy":
        # A ridge of snow along the top, not a white tube.
        for i in range(30):
            x, y = at(0.04 + 0.92 * i / 29, 0.78)
            ellipse(d, x, y, max(0.9, r * 0.26), max(0.6, r * 0.16), B.rgb(B.SNOW[1]))
        for i in range(12):
            x, y = at(0.1 + 0.8 * i / 11, 0.86)
            ellipse(d, x, y, max(0.6, r * 0.14), max(0.4, r * 0.08), B.rgb(B.SNOW[2]))
    return at


# ---------------------------------------------------------------- the axe

def draw_axe(d, cx, top_y):
    """An axe left bitten into the cut face: the haft up to the right, the
    iron head sunk in the wood."""
    head = (cx - 0.6, top_y - 0.4)
    end = (cx + 7.5, top_y - 9.5)
    d.line([P(*head), P(*end)], fill=B.rgb(HAFT[0]), width=int(1.3 * S))
    d.line([P(head[0] - 0.25, head[1] - 0.2), P(end[0] - 0.25, end[1] - 0.2)], fill=B.rgb(HAFT[2]), width=int(0.6 * S))
    ellipse(d, end[0], end[1], 0.8, 0.7, B.rgb(HAFT[1]))
    # The head: the poll above the face, the bit buried in it.
    d.polygon([P(head[0] - 2.4, head[1] + 0.3), P(head[0] + 1.4, head[1] - 0.6), P(head[0] + 1.6, head[1] + 0.9),
               P(head[0] - 2.2, head[1] + 1.3)], fill=B.rgb(IRON[1]))
    d.line([P(head[0] - 2.3, head[1] + 0.4), P(head[0] + 1.3, head[1] - 0.5)], fill=B.rgb(IRON[3]), width=max(1, int(0.45 * S)))
    d.line([P(head[0] - 2.4, head[1] + 1.4), P(head[0] + 1.0, head[1] + 1.4)], fill=B.rgb(F.HOLLOW[0]), width=max(1, int(0.4 * S)))


# ---------------------------------------------------------------- one look

def paint(piece, look):
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    rnd = random.Random(piece.seed)
    bark = bark_ramp(piece.bark)
    feet = []
    for st in getattr(piece, "stumps", []):
        # dy lifts a stump whose rounded foot would reach the frame's edge.
        cx, base = FW / 2 + st["dx"], BASE_Y - st.get("dy", 0.0)
        draw_stump(d, rnd, cx, base, st["w"], st["h"], st["top"], st["roots"], st["root_len"], st["moss"], bark, look)
        feet.append((cx, base, st["w"] / 2 + st["root_len"] * 0.7))
        if getattr(piece, "axe", False):
            draw_axe(d, cx, base - st["h"])
    for lg in getattr(piece, "logs", []):
        draw_log(d, rnd, lg["p0"], lg["p1"], lg["r"], lg["far"], lg["near"], lg["moss"], bark, look,
                 birch=piece.bark == "birch")
        mx = (lg["p0"][0] + lg["p1"][0]) / 2
        feet.append((mx, max(lg["p0"][1], lg["p1"][1]) + lg["r"] * 0.6, abs(lg["p1"][0] - lg["p0"][0]) / 2))
    if look == "autumn":
        lrnd = random.Random(piece.seed * 7)
        for fx, fy, spread in feet:
            for _ in range(5 if FW > 16 else 2):
                x = fx + lrnd.uniform(-spread, spread)
                y = fy + lrnd.uniform(-0.4, 1.2)
                r = lrnd.uniform(0.7, 1.0)
                ellipse(d, x, y, r * 1.3, r * 0.75, B.rgb(lrnd.choice(LEAVES)))
    palette = bark + F.PALE + F.HOLLOW + MOSS[season(look)] + (LEAVES if look == "autumn" else []) + \
        (B.SNOW if look == "winter-snowy" else []) + (IRON + HAFT if getattr(piece, "axe", False) else []) + \
        (["1e1c1a"] if piece.bark == "birch" else [])
    return lay, palette


def shrink(lay, palette):
    """To frame size: area average, every covered pixel snapped to the look's
    own colours, every edge a shade darker (the bottom most), and a shadow
    one pixel down and right on the ground."""
    small = lay.resize((FW, FH), Image.BOX)
    sp = small.load()
    pal = [B.rgb(h)[:3] for h in palette]
    covered = {(x, y): B.nearest(sp[x, y][:3], pal) for y in range(FH) for x in range(FW) if sp[x, y][3] >= 128}
    out = Image.new("RGBA", (FW, FH), (0, 0, 0, 0))
    op = out.load()
    for (x, y) in covered:
        sx, sy = x + 1, y + 1
        if 0 < sx < FW - 1 and sy < FH - 1 and (sx, sy) not in covered:
            op[sx, sy] = B.SHADOW
    for (x, y), c in covered.items():
        if (x, y + 1) not in covered:
            c = B.mix(c, B.OUTLINE, 0.45)[:3]
        elif (x - 1, y) not in covered or (x + 1, y) not in covered or (x, y - 1) not in covered:
            c = B.mix(c, B.OUTLINE, 0.3)[:3]
        op[x, y] = c + (255,)
    return out


def build_sheet(piece):
    use_frame(*piece.frame)
    sheet = Image.new("RGBA", (FW, FH * len(piece.looks)), (0, 0, 0, 0))
    for row, look in enumerate(piece.looks):
        sheet.paste(shrink(*paint(piece, look)), (0, row * FH))
    return sheet


def check(out_dir, names):
    """Every frame stays a pixel inside its frame on all four sides, holds
    some art, and the sheet is its size."""
    ok = True
    for name in names:
        piece = PIECES[name]
        use_frame(*piece.frame)
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        want = (FW, FH * len(piece.looks))
        if sheet.size != want:
            print("%s: sheet is %dx%d, want %dx%d" % ((name,) + sheet.size + want))
            ok = False
            continue
        for r, look in enumerate(piece.looks):
            frame = sheet.crop((0, r * FH, FW, (r + 1) * FH))
            px = frame.load()
            edge = any(px[x, y][3] == 255 for y in range(FH) for x in (0, FW - 1)) or \
                any(px[x, y][3] == 255 for x in range(FW) for y in (0, FH - 1))
            bad = edge or frame.getbbox() is None
            ok = ok and not bad
            print("%-13s %-13s%s" % (name, look, "  <- on the frame edge or empty" if bad else ""))
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's stumps and logs.")
    parser.add_argument("--piece", default=",".join(PIECES), help="comma-separated, of: " + ", ".join(PIECES))
    parser.add_argument("--out", default=OUT_DIR, help="where the sheets go (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheets instead of painting them")
    args = parser.parse_args()
    names = args.piece.split(",")
    for name in names:
        if name not in PIECES:
            parser.error("unknown piece %r (have %s)" % (name, ", ".join(PIECES)))
    if args.check:
        sys.exit(0 if check(args.out, names) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name in names:
        build_sheet(PIECES[name]).save(os.path.join(args.out, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
