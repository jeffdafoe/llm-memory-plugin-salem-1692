"""Paints salem's millstone (LLM-968). Every pixel is drawn here, no pack art.

    python tools/tree-art/millstone.py
    python tools/tree-art/millstone.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/millstone.py --check     # margins, size, a fresh paint

Writes client/assets/trees/millstone.png (one 64x64 frame), drawn at twice
the pack art's size and shown at render_scale 1 (the same size in the world,
twice the detail). Anchor 0.5/0.85 as the pack art.

A pair of spare millstones by the mill: the bed stone lying flat, and the
runner stood on its edge behind it, propped on a wooden chock. Both are
grey granite dressed for grinding: the eye in the middle, and the furrows
(the "harps") cut in straight runs from the eye toward the rim. From
drawings of seventeenth-century English and New England mill stones.
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

WOOD = F.WOOD
CUT = F.CUT
# Grey granite, a little warm.
GRANITE = ["24221f", "3a3733", "524e48", "6c675f", "868077", "a09a90", "bab4aa", "d0cbc2"]
DARK = ["0e0c0a", "1a1714", "282420"]
PALETTE = GRANITE + WOOD + CUT + DARK

FRAME = (32, 32)


def oval(d, cx, cy, rx, ry, col):
    S = P.S
    d.ellipse(((cx - rx) * S, (cy - ry) * S, (cx + rx) * S, (cy + ry) * S), fill=col)


def line(d, pts, col, w):
    d.line([(x * P.S, y * P.S) for x, y in pts], fill=col, width=max(1, int(w * P.S)))


def speckle(d, rnd, inside, n, tones):
    """Granite grain: n flecks at points chosen by inside()."""
    S = P.S
    for _ in range(n):
        x, y = inside(rnd)
        r = rnd.uniform(0.18, 0.32)
        d.ellipse(((x - r) * S, (y - r) * S, (x + r) * S, (y + r) * S), fill=B.ramp(GRANITE, rnd.choice(tones)))


def harps(d, cx, cy, rx, ry, tone, eye):
    """The dressing: furrows in straight runs from the eye toward the rim, in
    quarters, each a dark cut with a lit edge beside it."""
    for k in range(12):
        a = 2 * math.pi * k / 12
        # Each furrow runs off the eye's edge at a slant (the harp's lean).
        sx, sy = cx + eye * math.cos(a) * rx / ry * 0.9, cy + eye * math.sin(a) * 0.9
        b = a + 0.32
        ex, ey = cx + rx * 0.9 * math.cos(b), cy + ry * 0.9 * math.sin(b)
        line(d, [(sx, sy), (ex, ey)], B.ramp(GRANITE, tone - 0.22), 0.4)
        line(d, [(sx, sy - 0.25), (ex, ey - 0.25)], B.ramp(GRANITE, tone + 0.14), 0.22)


def paint_millstone(seed=968):
    P.use_frame(*FRAME, density=DENSITY)
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    d, sd = ImageDraw.Draw(lay), ImageDraw.Draw(shade)
    rnd = random.Random(seed)
    base = 0.85 * FRAME[1]

    # The runner, stood on edge behind: a disc seen almost face-on, leaning
    # back, its edge (the thickness) showing on the right.
    rcx, rcy, rr = 18.5, base - 11.5, 8.8
    sd.ellipse(((rcx - 4) * P.S, (base - 3.5) * P.S, (rcx + 13) * P.S, (base + 0.5) * P.S), fill=255)
    oval(d, rcx + 1.8, rcy + 0.3, rr * 0.92, rr, B.ramp(GRANITE, 0.3))           # the thickness, in shade
    oval(d, rcx, rcy, rr * 0.92, rr, B.ramp(GRANITE, 0.56))
    speckle(d, rnd, lambda r: (rcx + r.uniform(-6.5, 6.5), rcy + r.uniform(-7, 7)), 40, (0.35, 0.7, 0.8))
    harps(d, rcx, rcy, rr * 0.92, rr, 0.56, 2.2)
    oval(d, rcx, rcy, 1.8, 2.0, B.ramp(DARK, 0.6))                                # the eye
    oval(d, rcx - 0.4, rcy - 0.6, 1.0, 0.9, B.ramp(DARK, 0.2))
    # The rim, lit along the upper left.
    d.arc(((rcx - rr * 0.92) * P.S, (rcy - rr) * P.S, (rcx + rr * 0.92) * P.S, (rcy + rr) * P.S), 160, 290,
          fill=B.ramp(GRANITE, 0.82), width=max(1, int(0.45 * P.S)))
    # The chock under its foot.
    d.polygon([((rcx - 3.5) * P.S, (base - 1.6) * P.S), ((rcx + 1.5) * P.S, (base - 1.4) * P.S),
               ((rcx + 1.0) * P.S, (base - 3.4) * P.S)], fill=B.ramp(WOOD, 0.55))

    # The bed stone, lying flat in front: a low drum seen from above, its
    # top face dressed, its side a dark band.
    bcx, bcy, brx, bry, th = 12.5, base - 4.2, 9.5, 4.6, 2.6
    sd.ellipse(((bcx - brx + 1.5) * P.S, (bcy - bry + 2.0) * P.S, (bcx + brx + 2.5) * P.S, (bcy + bry + 2.4) * P.S),
               fill=255)
    oval(d, bcx, bcy + th, brx, bry, B.ramp(GRANITE, 0.24))
    d.rectangle(((bcx - brx) * P.S, bcy * P.S, (bcx + brx) * P.S, (bcy + th) * P.S), fill=B.ramp(GRANITE, 0.34))
    oval(d, bcx, bcy, brx, bry, B.ramp(GRANITE, 0.66))
    speckle(d, rnd, lambda r: (bcx + r.uniform(-7.5, 7.5), bcy + r.uniform(-3.2, 3.2)), 34, (0.45, 0.78, 0.88))
    harps(d, bcx, bcy, brx, bry, 0.66, 1.3)
    oval(d, bcx, bcy, 1.6, 0.9, B.ramp(DARK, 0.5))
    d.arc(((bcx - brx) * P.S, (bcy - bry) * P.S, (bcx + brx) * P.S, (bcy + bry) * P.S), 180, 300,
          fill=B.ramp(GRANITE, 0.9), width=max(1, int(0.45 * P.S)))
    return F.shrink_shaded(lay, shade, PALETTE)


SHEETS = {"millstone": lambda: paint_millstone()}
FRAMES = {"millstone": (FRAME[0] * DENSITY, FRAME[1] * DENSITY, 1)}


def check(out_dir):
    """The sheet is exactly what this script paints now, one frame of its
    size, with no art on its outer rows or columns."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun millstone.py)" % name)
            ok = False
            continue
        fw, fh, n = FRAMES[name]
        if sheet.size != (fw * n, fh):
            print("%s: sheet %dx%d is not %d frame(s) of %dx%d" % (name, sheet.width, sheet.height, n, fw, fh))
            ok = False
            continue
        if any(F.edges_touched(sheet)):
            print("%s: art on the frame edge" % name)
            ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's millstone.")
    parser.add_argument("--out", default=OUT_DIR, help="where the sheet goes (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheet instead of painting it")
    args = parser.parse_args()
    if args.check:
        sys.exit(0 if check(args.out) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name, build in SHEETS.items():
        build().save(os.path.join(args.out, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
