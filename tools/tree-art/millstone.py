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
PALETTE = GRANITE + WOOD + CUT

FRAME = (32, 32)


def oval(d, cx, cy, rx, ry, col):
    S = P.S
    d.ellipse(((cx - rx) * S, (cy - ry) * S, (cx + rx) * S, (cy + ry) * S), fill=col)


def line(d, pts, col, w):
    d.line([(x * P.S, y * P.S) for x, y in pts], fill=col, width=max(1, int(w * P.S)))


def speckle(d, rnd, cx, cy, rx, ry, n, tones):
    """Granite grain: n flecks inside the face's ellipse (centre cx, cy,
    radii rx, ry), inset so no fleck reaches past the rim."""
    S = P.S
    for _ in range(n):
        # A uniform point in the unit disc, scaled to the inset ellipse.
        a, t = rnd.uniform(0, 2 * math.pi), math.sqrt(rnd.random())
        x, y = cx + (rx - 0.8) * t * math.cos(a), cy + (ry - 0.8) * t * math.sin(a)
        r = rnd.uniform(0.18, 0.32)
        d.ellipse(((x - r) * S, (y - r) * S, (x + r) * S, (y + r) * S), fill=B.ramp(GRANITE, rnd.choice(tones)))


def harps(d, rnd, cx, cy, rx, ry, tone, eye):
    """The dressing: furrows in straight runs from the eye toward the rim, each
    a shallow cut with a lit edge beside it. They stop short of the rim by
    differing amounts and are only a little darker than the face, so they
    read as dressed grooves, not as the spokes of a wheel."""
    for k in range(12):
        a = 2 * math.pi * (k + rnd.uniform(-0.12, 0.12)) / 12
        # Each furrow runs off the eye's edge at a slant (the harp's lean).
        sx, sy = cx + eye * math.cos(a) * rx / ry * 0.9, cy + eye * math.sin(a) * 0.9
        b = a + 0.32
        reach = rnd.uniform(0.55, 0.88)
        ex, ey = cx + rx * reach * math.cos(b), cy + ry * reach * math.sin(b)
        line(d, [(sx, sy), (ex, ey)], B.ramp(GRANITE, tone - 0.14), 0.35)
        line(d, [(sx, sy - 0.25), (ex, ey - 0.25)], B.ramp(GRANITE, tone + 0.08), 0.2)


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
    # Its cast shadow: a low band hugging the ground where it stands,
    # falling down and right.
    sd.ellipse(((rcx - 2) * P.S, (base - 2.6) * P.S, (rcx + 9.5) * P.S, (base + 0.2) * P.S), fill=255)
    oval(d, rcx + 1.8, rcy + 0.3, rr * 0.92, rr, B.ramp(GRANITE, 0.3))           # the thickness, in shade
    oval(d, rcx, rcy, rr * 0.92, rr, B.ramp(GRANITE, 0.56))
    speckle(d, rnd, rcx, rcy, rr * 0.92, rr, 40, (0.35, 0.7, 0.8))
    harps(d, rnd, rcx, rcy, rr * 0.92, rr, 0.56, 2.2)
    # The eye: dark, but stone-dark rather than black.
    oval(d, rcx, rcy, 1.8, 2.0, B.ramp(GRANITE, 0.12))
    oval(d, rcx - 0.4, rcy - 0.6, 1.0, 0.9, B.ramp(GRANITE, 0.04))
    # The rim, lit along the upper left.
    d.arc(((rcx - rr * 0.92) * P.S, (rcy - rr) * P.S, (rcx + rr * 0.92) * P.S, (rcy + rr) * P.S), 160, 290,
          fill=B.ramp(GRANITE, 0.82), width=max(1, int(0.45 * P.S)))

    # The bed stone, lying flat in front: a low drum seen from above, its
    # top face dressed, its side a dark band.
    bcx, bcy, brx, bry, th = 12.5, base - 4.2, 9.5, 4.6, 2.6
    sd.ellipse(((bcx - brx + 1.0) * P.S, (bcy - bry + 3.2) * P.S, (bcx + brx + 1.2) * P.S, (bcy + bry + 3.0) * P.S),
               fill=255)
    oval(d, bcx, bcy + th, brx, bry, B.ramp(GRANITE, 0.24))
    d.rectangle(((bcx - brx) * P.S, bcy * P.S, (bcx + brx) * P.S, (bcy + th) * P.S), fill=B.ramp(GRANITE, 0.34))
    oval(d, bcx, bcy, brx, bry, B.ramp(GRANITE, 0.66))
    speckle(d, rnd, bcx, bcy, brx, bry, 34, (0.45, 0.78, 0.88))
    harps(d, rnd, bcx, bcy, brx, bry, 0.66, 1.3)
    oval(d, bcx, bcy, 1.6, 0.9, B.ramp(GRANITE, 0.12))
    d.arc(((bcx - brx) * P.S, (bcy - bry) * P.S, (bcx + brx) * P.S, (bcy + bry) * P.S), 180, 300,
          fill=B.ramp(GRANITE, 0.9), width=max(1, int(0.45 * P.S)))

    # The chock: a wooden wedge driven under the runner's foot, on the right
    # where the bed stone does not hide it, its top lit.
    foot = rcy + rr
    wedge = [(rcx + 3.6, base - 0.4), (rcx + 8.4, base - 0.6), (rcx + 4.6, foot - 0.6)]
    d.polygon([(x * P.S, y * P.S) for x, y in wedge], fill=B.ramp(WOOD, 0.5))
    line(d, [wedge[2], wedge[1]], B.ramp(WOOD, 0.78), 0.4)
    line(d, [wedge[0], wedge[1]], B.ramp(WOOD, 0.28), 0.35)
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
