"""Paints salem's rocks (LLM-967): pebbles, stones, boulders and the river
rock. Every pixel is drawn here, no pack art.

    python tools/tree-art/rocks.py
    python tools/tree-art/rocks.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/rocks.py --check     # every frame inside its frame, sheets their size

Writes client/assets/trees/<piece>.png (pebble, tiny-rock, medium-rocks,
large-rocks, boulder-pair, flat-rock, small-rock, river-rock). Each sheet has
one row per look, top to bottom: summer, autumn, winter, winter-snowy; a row
is the look's frames, left to right, at the piece's frame size. Every rock is
one still frame except the river rock, whose rings of ripples spread across
the water round it, a RIPPLE_FRAMES loop at RIPPLE_FPS. Rerunning writes the
same files. After a change, run `godot_console --headless --path client
--import` so Godot writes the .import sidecars, and commit them.

It imports build.py (the trees) for its scale, colour helpers and the
outline and shadow colours, so the rocks are painted, shrunk and snapped the
way the trees and the water edge are: at build.S times size, area-averaged
down, every pixel snapped to the look's own colours.

How a stone is made, from New England fieldstone: a rounded, lumpy outline
(flat where it sits on the ground), dark on its lower right, a lit top face
with a bright patch to the upper left, a ridge where the top turns down, a
crack or two, granite flecks, and moss on the top. Autumn turns the moss
rust and drops a few leaves at the foot; winter cools the stone and dulls the
moss; snow caps the top face only, with nothing heaped at the foot. The river
rock stands in the water: wet and dark at the waterline, a pale ring round
its foot, and two rings spreading out from it in turn.
"""

import argparse
import math
import os
import random
import sys

from PIL import Image, ImageChops, ImageDraw

import build as B

S = B.S
OUT_DIR = B.OUT_DIR
LOOKS = ["summer", "autumn", "winter", "winter-snowy"]
ANCHOR_Y = 0.85
RIPPLE_FRAMES = 12
RIPPLE_FPS = 6

# ---------------------------------------------------------------- colours

STONE = ["2a2624", "433d38", "5e5650", "7a7068", "978c82", "b3a99e", "cfc6bb", "e6dfd5"]
STONE_COLD = ["282a2e", "404247", "5a5c61", "75777b", "919296", "aeaeb1", "cac9cb", "e0dfe0"]
MOSS = {"summer": ["34481c", "4c6626", "688630", "88a63e"],
        "autumn": ["5a3a16", "80521e", "a46c28", "c48a36"],
        "winter": ["3e4430", "545c40", "6c7452"],
        "winter-snowy": ["3e4430", "545c40", "6c7452"]}
# The terrain's autumn leaves (tools/terrain-art LEAVES).
LEAVES = ["c8702a", "d99a3a", "a8452a", "b8862e"]
# The water round the river rock (tools/terrain-art SHALLOW_WATER light tones).
WATER = {"summer": ["a6d6db", "c8e6ea", "e8f6f6"], "autumn": ["a6d6db", "c8e6ea", "e8f6f6"],
         "winter": ["9cc0c8", "c0d8de", "dff0f2"], "winter-snowy": ["9cc0c8", "c0d8de", "dff0f2"]}


def stone_ramp(look):
    return STONE_COLD if look.startswith("winter") else STONE


# ---------------------------------------------------------------- pieces

class Piece:
    def __init__(self, **kw):
        self.animated = False
        self.water = False
        self.__dict__.update(kw)


# stones: (dx, dy, w, h) in frame pixels; dx from the frame's middle, dy up
# from the foot line (positive is further back), w and h the stone's width
# and height on screen. Drawn back to front.
PIECES = {
    "pebble": Piece(frame=(16, 16), seed=3, stones=[(3.6, 3.6, 4.0, 3.2), (-3.8, 2.0, 4.6, 3.6), (0.4, -0.4, 5.0, 3.8)]),
    "tiny-rock": Piece(frame=(16, 16), seed=7, stones=[(-1.6, 1.6, 7.0, 5.6), (3.2, -0.2, 4.6, 3.6)]),
    "medium-rocks": Piece(frame=(16, 16), seed=11, stones=[(-0.8, 1.2, 10.0, 8.6), (4.0, -0.6, 5.0, 3.8)]),
    "large-rocks": Piece(frame=(16, 16), seed=13, stones=[(-2.2, 2.4, 9.0, 8.6), (3.4, 1.4, 7.0, 6.6), (-0.6, -0.8, 6.0, 4.4)]),
    "boulder-pair": Piece(frame=(16, 16), seed=17, stones=[(-2.8, 1.0, 8.6, 8.2), (3.2, 0.0, 7.6, 6.8)]),
    "flat-rock": Piece(frame=(16, 16), seed=19, flat=True, stones=[(0.0, 0.2, 13.0, 6.4)]),
    "small-rock": Piece(frame=(32, 32), seed=23, stones=[(-0.5, 1.0, 21.0, 18.5), (8.0, -1.2, 6.4, 4.4)]),
    "river-rock": Piece(frame=(32, 32), seed=29, water=True, animated=True, stones=[(0.0, 0.8, 17.0, 14.5)]),
}


def use_frame(fw, fh):
    global FW, FH, W, H, BASE_Y
    FW, FH = fw, fh
    W, H = fw * S, fh * S
    BASE_Y = H * ANCHOR_Y


# ---------------------------------------------------------------- a stone

def outline(rnd, cx, base, w, h, flat):
    """A lumpy closed outline, round on top and flat along the ground."""
    n = 16
    top_r = h * 0.62
    low_r = h * 0.38
    cy = base - low_r
    pts = []
    for k in range(n):
        a = k * 2 * math.pi / n
        wobble = 1.0 + rnd.uniform(-0.09, 0.09)
        ry = low_r if math.sin(a) > 0 else top_r
        x = cx + math.cos(a) * w * 0.5 * wobble
        y = cy + math.sin(a) * ry * wobble
        if flat and math.sin(a) < 0:
            # a slab: the top is a broad, nearly level face
            y = cy + math.sin(a) * ry * (0.55 + 0.45 * abs(math.cos(a)) ** 3) * wobble
        pts.append((x, min(y, base)))
    return chaikin(pts, 2)


def chaikin(poly, rounds):
    for _ in range(rounds):
        out = []
        for i in range(len(poly)):
            (x0, y0), (x1, y1) = poly[i], poly[(i + 1) % len(poly)]
            out.append((0.75 * x0 + 0.25 * x1, 0.75 * y0 + 0.25 * y1))
            out.append((0.25 * x0 + 0.75 * x1, 0.25 * y0 + 0.75 * y1))
        poly = out
    return poly


def scaled(poly, sx, sy, ox, oy, cx, cy):
    return [(cx + (x - cx) * sx + ox, cy + (y - cy) * sy + oy) for x, y in poly]


def draw_stone(lay, rnd, cx, base, w, h, look, flat=False, wet=False):
    """One stone on its own layer, cut to its outline, then laid on lay."""
    cols = stone_ramp(look)
    moss = MOSS[look]
    poly = outline(rnd, cx, base, w, h, flat)
    xs = [p[0] for p in poly]
    ys = [p[1] for p in poly]
    top = min(ys)
    mcx, mcy = (min(xs) + max(xs)) / 2, (top + base) / 2
    own = Image.new("RGBA", lay.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(own)
    tone = rnd.uniform(-0.05, 0.05)
    # Body: dark all over, then the lit mass up and to the left of it, so a
    # dark band stays along the lower right.
    d.polygon(poly, fill=B.ramp(cols, 0.18 + tone))
    d.polygon(scaled(poly, 0.94, 0.92, -0.05 * w, -0.07 * h, mcx, mcy), fill=B.ramp(cols, 0.4 + tone))
    # The top face: a lumpy cap over the upper part, lit.
    face_h = (base - top) * (0.62 if flat else 0.42)
    face = [(x, y) for x, y in scaled(poly, 0.86, 0.6 if not flat else 0.8, -0.04 * w, 0, mcx, top)]
    face = [(x, min(y, top + face_h)) for x, y in face]
    d.polygon(face, fill=B.ramp(cols, 0.66 + tone))
    # The ridge where the top turns down: a darker line under the face.
    ridge = sorted([p for p in face if p[1] >= top + face_h - 0.5], key=lambda p: p[0])
    if len(ridge) >= 2:
        d.line(ridge, fill=B.ramp(cols, 0.36 + tone), width=max(1, int(0.5 * S)))
    # The bright patch, upper left.
    hx, hy = mcx - w * 0.16, top + face_h * 0.45
    d.ellipse((hx - w * 0.16, hy - face_h * 0.28, hx + w * 0.1, hy + face_h * 0.22), fill=B.ramp(cols, 0.86 + tone))
    # Granite flecks, cracks and moss only where a stone is big enough to
    # hold them: on a small one they shrink to noise.
    small = w < 7 * S
    for _ in range(0 if small else int(w * h / (S * S) * 0.12)):
        fx = rnd.uniform(min(xs), max(xs))
        fy = rnd.uniform(top, base)
        r = rnd.uniform(0.25, 0.45) * S
        d.ellipse((fx - r, fy - r, fx + r, fy + r), fill=B.ramp(cols, rnd.choice((0.15, 0.3, 0.75))))
    # Cracks: down from the top face, a dark line with a lit lip beside it.
    for _ in range(0 if small else 1 if w < 12 * S else 2):
        x0 = rnd.uniform(mcx - w * 0.25, mcx + w * 0.2)
        y0 = top + face_h * rnd.uniform(0.1, 0.5)
        pts = [(x0, y0)]
        for _ in range(3):
            px, py = pts[-1]
            pts.append((px + rnd.uniform(-0.6, 0.9) * S, py + rnd.uniform(0.8, 1.4) * S * (h / (6 * S)) ** 0.5))
        d.line(pts, fill=B.ramp(cols, 0.1), width=max(1, int(0.4 * S)))
        d.line([(x - 0.45 * S, y) for x, y in pts[:-1]], fill=B.ramp(cols, 0.7 + tone), width=max(1, int(0.3 * S)))
    # Moss and lichen on the top and down the shaded side.
    for _ in range(0 if small else int(w / (5 * S))):
        mx = rnd.uniform(mcx - w * 0.3, mcx + w * 0.3)
        my = rnd.uniform(top + face_h * 0.5, top + face_h * 1.3)
        r = rnd.uniform(0.6, 0.9) * S
        d.ellipse((mx - r, my - r * 0.7, mx + r, my + r * 0.7), fill=B.ramp(moss, 0.35))
        d.ellipse((mx - r * 0.7, my - r * 0.6, mx + r * 0.2, my), fill=B.ramp(moss, 0.85))
    if wet:
        # Wet and dark where the water laps it.
        d.rectangle((min(xs), base - 1.6 * S, max(xs), base), fill=B.ramp(cols, 0.12))
        d.rectangle((min(xs), base - 2.4 * S, max(xs), base - 1.6 * S), fill=B.ramp(cols, 0.28))
    if look == "winter-snowy":
        # Snow on the top face only: a cap a little inside it, its lower
        # edge in shadow, and a bright crest.
        cap = scaled(face, 0.92, 0.9, 0.02 * w, -0.03 * h, mcx, top)
        d.polygon([(x, y + 0.5 * S) for x, y in cap], fill=B.rgb(B.SNOW[0]))
        d.polygon(cap, fill=B.rgb(B.SNOW[1]))
        d.ellipse((hx - w * 0.2, hy - face_h * 0.35, hx + w * 0.12, hy + face_h * 0.1), fill=B.rgb(B.SNOW[2]))
    mask = Image.new("L", lay.size, 0)
    ImageDraw.Draw(mask).polygon(poly, fill=255)
    own.putalpha(ImageChops.multiply(own.getchannel("A"), mask))
    lay.alpha_composite(own)


def drop_leaves(d, rnd, cx, base, spread, count):
    for _ in range(count):
        lx = cx + rnd.uniform(-spread, spread)
        ly = base + rnd.uniform(-0.2, 1.0) * S
        r = rnd.uniform(0.7, 1.0) * S
        d.ellipse((lx - r, ly - r * 0.6, lx + r, ly + r * 0.6), fill=B.rgb(rnd.choice(LEAVES)))


# ---------------------------------------------------------------- the water

def ring(d, cx, cy, rx, ry, cols, t, front):
    """A ripple ring, half of it: the back half (front False) lies behind the
    rock, the front half over its foot. t in 0..1 is how far it has spread:
    it widens, pales and breaks up as it goes."""
    a0, a1 = (0, 180) if front else (180, 360)
    if t >= 0.92:
        return
    col = B.rgb(cols[2] if t < 0.3 else cols[1] if t < 0.65 else cols[0])
    gap = 6 + int(t * 34)                     # degrees of break between dashes
    dash = 34 - int(t * 14)
    a = a0 + (7 if front else 0)
    while a < a1:
        d.arc((cx - rx, cy - ry, cx + rx, cy + ry), a, min(a + dash, a1), fill=col, width=max(2, int(0.5 * S)))
        a += dash + gap


def paint(piece, look, frame):
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    rnd = random.Random(piece.seed)
    cx = W * 0.5
    stones = sorted(piece.stones, key=lambda s: -s[1])
    if piece.water:
        cols = WATER[look]
        d = ImageDraw.Draw(lay)
        w = stones[0][2] * S
        rx0, ry0 = w * 0.56, w * 0.1
        span = 2.8 * S
        for k in range(2):
            t = ((frame / RIPPLE_FRAMES) + k * 0.5) % 1.0
            ring(d, cx, BASE_Y, rx0 + t * span * 1.6, ry0 + t * span * 0.25, cols, t, front=False)
        ring(d, cx, BASE_Y, rx0 * 0.86, ry0 * 0.9, cols, 0.0, front=False)
    for dx, dy, w, h in stones:
        draw_stone(lay, rnd, cx + dx * S, BASE_Y - dy * S, w * S, h * S, look,
                   flat=getattr(piece, "flat", False), wet=piece.water)
    d = ImageDraw.Draw(lay)
    if piece.water:
        ring(d, cx, BASE_Y, rx0 * 0.86, ry0 * 0.9, cols, 0.0, front=True)
        for k in range(2):
            t = ((frame / RIPPLE_FRAMES) + k * 0.5) % 1.0
            ring(d, cx, BASE_Y, rx0 + t * span * 1.6, ry0 + t * span * 0.25, cols, t, front=True)
    elif look == "autumn":
        lrnd = random.Random(piece.seed * 5)
        width = max(s[2] for s in piece.stones) * S
        drop_leaves(d, lrnd, cx, BASE_Y, width * 0.55, 2 if piece.frame[0] == 16 else 5)
    palette = stone_ramp(look) + MOSS[look] + (B.SNOW if look == "winter-snowy" else []) + \
        (LEAVES if look == "autumn" and not piece.water else []) + (WATER[look] if piece.water else [])
    return lay, palette


# ---------------------------------------------------------------- to pixels

def shrink(lay, palette, water_cols):
    """To frame size: area average, every covered pixel snapped to the look's
    own colours, the down-facing edge of the stone darkened, and a shadow one
    pixel down and right on the ground. The water's own rings get neither."""
    small = lay.resize((FW, FH), Image.BOX)
    sp = small.load()
    pal = [B.rgb(h)[:3] for h in palette]
    water = [B.rgb(h)[:3] for h in water_cols]
    covered = {}
    for y in range(FH):
        for x in range(FW):
            if sp[x, y][3] >= 128:
                covered[(x, y)] = B.nearest(sp[x, y][:3], pal)
    stone = {p for p, c in covered.items() if c not in water}
    out = Image.new("RGBA", (FW, FH), (0, 0, 0, 0))
    op = out.load()
    for (x, y) in stone:
        sx, sy = x + 1, y + 1
        if 0 < sx < FW - 1 and sy < FH - 1 and (sx, sy) not in covered:
            op[sx, sy] = B.SHADOW
    for (x, y), c in covered.items():
        if (x, y) in stone:
            # Every edge of the stone a shade darker, the bottom most.
            if (x, y + 1) not in stone:
                c = B.mix(c, B.OUTLINE, 0.45)[:3]
            elif (x - 1, y) not in stone or (x + 1, y) not in stone or (x, y - 1) not in stone:
                c = B.mix(c, B.OUTLINE, 0.3)[:3]
        op[x, y] = c + (255,)
    return out


def frame_count(piece):
    return RIPPLE_FRAMES if piece.animated else 1


def build_sheet(piece):
    use_frame(*piece.frame)
    n = frame_count(piece)
    sheet = Image.new("RGBA", (FW * n, FH * len(LOOKS)), (0, 0, 0, 0))
    for row, look in enumerate(LOOKS):
        for col in range(n):
            lay, palette = paint(piece, look, col)
            sheet.paste(shrink(lay, palette, WATER[look] if piece.water else []), (col * FW, row * FH))
    return sheet


def check(out_dir, names):
    """Every frame stays a pixel inside its frame on all four sides, holds
    some art, and the sheet is its size."""
    ok = True
    for name in names:
        piece = PIECES[name]
        use_frame(*piece.frame)
        n = frame_count(piece)
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        want = (FW * n, FH * len(LOOKS))
        if sheet.size != want:
            print("%s: sheet is %dx%d, want %dx%d" % ((name,) + sheet.size + want))
            ok = False
            continue
        for r, look in enumerate(LOOKS):
            bad = []
            for col in range(n):
                frame = sheet.crop((col * FW, r * FH, (col + 1) * FW, (r + 1) * FH))
                px = frame.load()
                edge = any(px[x, y][3] == 255 for y in range(FH) for x in (0, FW - 1)) or \
                    any(px[x, y][3] == 255 for x in range(FW) for y in (0, FH - 1))
                if edge or frame.getbbox() is None:
                    bad.append(col)
            ok = ok and not bad
            print("%-13s %-13s %2d frame(s)%s" % (name, look, n, "" if not bad else "  <- on the frame edge or empty: %s" % bad))
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's rocks.")
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
