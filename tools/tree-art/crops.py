"""Paints salem's field crops (LLM-968): wheat and carrots, each through the
six growth stages the engine shows. Every pixel is drawn here, no pack art.

    python tools/tree-art/crops.py
    python tools/tree-art/crops.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/crops.py --check     # every frame inside its frame, sheets their size

Writes client/assets/trees/<crop>.png: one row of six 32x64 frames, left to
right growth-1 .. growth-6, shown at render_scale 1 (twice the pack art's
16x32, the same size in the world, as the ground cover). The engine
(berry_state.go cropStageState) shows growth-6 whenever a plot has stock and
walks growth-1 .. growth-5 across the regrow period after a harvest, so
growth-1 is a plot just harvested and growth-6 is ripe. Rerunning writes the
same files. After a change, run `godot_console --headless --path client
--import` so Godot writes the .import sidecars, and commit them.

It paints, shrinks and snaps the way plants.py does (and through its
helpers): at build.S times size, area-averaged down, every pixel snapped to
the crop's own colours, the lower edge darkened toward the outline.

Every stage stands on a small hill of tilled soil, so a plot reads as worked
ground on grass or dirt. From photographs of the crops:
- Wheat: stubble just after the harvest; then a few green blades; a tuft;
  stalks with green ears; the ears turning gold over green leaves; ripe
  golden stalks with heavy bearded ears, a few nodding.
- Carrots: the hill just dug; two seed leaves; small feathery tops; fuller
  tops; tall fern-like tops with the orange shoulder at the soil; full dark
  tops over a bright shoulder.
"""

import argparse
import math
import os
import random
import sys

from PIL import Image, ImageDraw

import build as B
import plants as P

OUT_DIR = B.OUT_DIR
FRAME = (16, 32)          # pack-art pixels; drawn at density 2 (32x64)
DENSITY = 2
STAGES = 6

# ---------------------------------------------------------------- colours

STRAW = ["3e3018", "5a4824", "766232", "907a42", "a89256", "bca86c"]
SOIL = ["24180e", "382616", "4e3620", "64482c", "7a5c3a", "92724c"]
STUBBLE = ["5a4a26", "7e6a38", "a08a4c", "bea866", "d6c286"]
WHEAT_GREEN = ["1a3414", "28481a", "3a6222", "4e7c2a", "669634", "82ae46", "a4c862"]
WHEAT_TURN = ["3a3c14", "56581c", "727426", "8e8e30", "aaa83e", "c4c054", "dad874"]
WHEAT_GOLD = ["4a3410", "6c4e16", "92701e", "b48e28", "d0ac3a", "e4c656", "f2de86"]
CARROT_LEAF = ["10301a", "1a4622", "245e2c", "2e7834", "3e9240", "58ac52", "80c66e"]
CARROT_ORANGE = ["5a2006", "86340a", "b04e10", "d26a18", "ea8a28", "f6ae4a"]


class Crop:
    def __init__(self, **kw):
        self.__dict__.update(kw)


CROPS = {
    # ring: how far a shape's side and top edges are mixed toward the
    # outline (plants.py's), so the ripe wheat's heads stand apart.
    "wheat": Crop(painter="wheat", seed=31, ring=0.22),
    "carrot": Crop(painter="carrot", seed=47, ring=0.0),
}


def frac(seed, *k):
    return (B._hash(seed, *k) & 0xFFFF) / 65535.0


# ---------------------------------------------------------------- soil

def paint_hill(d, rnd, cx, base, w=5.4):
    """A low hill of tilled soil, its top lit, a few clods and a furrow."""
    S = P.S
    d.ellipse((cx - w * S, base - 1.9 * S, cx + w * S, base + 1.5 * S), fill=B.ramp(SOIL, 0.2))
    d.ellipse((cx - (w - 0.6) * S, base - 2.2 * S, cx + (w - 0.9) * S, base + 0.6 * S), fill=B.ramp(SOIL, 0.45))
    d.ellipse((cx - (w - 1.8) * S, base - 2.3 * S, cx + (w - 2.8) * S, base - 0.6 * S), fill=B.ramp(SOIL, 0.62))
    for _ in range(7):
        x = cx + rnd.uniform(-w + 1.0, w - 1.0) * S
        y = base + rnd.uniform(-1.6, 0.6) * S
        r = rnd.uniform(0.45, 0.8) * S
        d.ellipse((x - r, y - r * 0.7, x + r, y + r * 0.7), fill=B.ramp(SOIL, rnd.choice([0.15, 0.75, 0.9])))
    return list(SOIL)


# ---------------------------------------------------------------- wheat

def wheat_ear(d, x, y, ang, length, cols, tone, awns):
    """An ear of wheat: paired grains up a short spike at angle ang (radians
    from straight up), the beard of awns standing off its top."""
    S = P.S
    dx, dy = math.sin(ang), -math.cos(ang)
    n = 5
    for k in range(n):
        t = k / (n - 1)
        gx, gy = x + dx * length * t * S, y + dy * length * t * S
        r = (0.5 - 0.15 * t) * S
        for side in (-1, 1):
            ox, oy = -dy * side * 0.3 * S, dx * side * 0.3 * S
            d.ellipse((gx + ox - r, gy + oy - r * 1.2, gx + ox + r, gy + oy + r * 1.2), fill=B.ramp(cols, tone - (0.3 if side > 0 else 0.1)))
            d.ellipse((gx + ox - r * 0.7, gy + oy - r, gx + ox + r * 0.3, gy + oy + r * 0.2), fill=B.ramp(cols, tone + 0.12))
    if awns:
        tx, ty = x + dx * length * S, y + dy * length * S
        for k in range(3):
            a = ang + (k - 1) * 0.2
            L = (2.0 + 0.5 * (k % 2)) * S
            d.line([(tx - dx * k * 0.4 * S, ty - dy * k * 0.4 * S),
                    (tx + math.sin(a) * L, ty - math.cos(a) * L)], fill=B.ramp(cols, tone - 0.05), width=max(1, int(0.18 * S)))


def paint_wheat(crop, stage):
    S, W, H = P.S, P.W, P.H
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    rnd = random.Random(crop.seed * 10 + stage)
    cx, base = W * 0.5, P.BASE_Y
    pal = paint_hill(d, rnd, cx, base)
    if stage == 1:
        # Stubble: the cut stalks, short and pale, standing out of the hill.
        for k in range(11):
            x = cx + (k - 5) * 0.8 * S + rnd.uniform(-0.3, 0.3) * S
            y0 = base - rnd.uniform(0.6, 1.6) * S
            h = rnd.uniform(1.4, 2.6) * S
            d.line([(x, y0), (x + rnd.uniform(-0.4, 0.4) * S, y0 - h)], fill=B.ramp(STUBBLE, rnd.uniform(0.3, 0.9)),
                   width=max(1, int(0.45 * S)))
        return lay, pal + STUBBLE
    # Stalks: (blade count, height range in frame px, ear colours or None)
    spec = {
        2: (4, (2.5, 4.5), None, WHEAT_GREEN),
        3: (9, (5.0, 9.0), None, WHEAT_GREEN),
        4: (8, (10.0, 14.0), (WHEAT_GREEN, 0.55, False), WHEAT_GREEN),
        5: (9, (14.0, 18.0), (WHEAT_TURN, 0.6, True), WHEAT_GREEN),
        6: (8, (11.0, 20.0), (WHEAT_GOLD, 0.6, True), STRAW),
    }[stage]
    count, (lo, hi), ear, stalk_cols = spec
    pal += stalk_cols + WHEAT_GREEN
    order = sorted(range(count), key=lambda i: frac(crop.seed, stage, i, 1))
    for n, i in enumerate(order):
        f = (i + 0.5) / count
        x0 = cx + (f - 0.5) * (7.0 if ear is None else 5.0) * S
        y0 = base - 0.9 * S - frac(crop.seed, stage, i, 2) * 0.8 * S
        L = (lo + (hi - lo) * frac(crop.seed, stage, i, 3)) * S
        lean = (f - 0.5) * (0.5 if ear is None else 0.26) + (frac(crop.seed, stage, i, 4) - 0.5) * 0.16
        tone = 0.35 + 0.45 * frac(crop.seed, stage, i, 5)
        if ear is None:
            # Young blades: tapering leaves fanning from the hill.
            P.draw_blade(d, x0, y0, -math.pi / 2 + lean, L, lean * 4 * S, 1.2, stalk_cols, tone, 0.0, 0, n % 2 == 0)
            continue
        # A stalk with a leaf or two low on it, and its ear on top. The ripe
        # stage nods: the top of a few stalks bends over with the ear's weight.
        nod = stage == 6 and frac(crop.seed, stage, i, 6) > 0.72
        tx, ty = x0 + math.sin(lean) * L, y0 - math.cos(lean) * L
        mid = (x0 + math.sin(lean) * L * 0.6, y0 - math.cos(lean) * L * 0.6)
        d.line([(x0, y0), mid, (tx, ty)], fill=B.ramp(stalk_cols, tone), width=max(1, int(0.5 * S)))
        for side in (-1, 1) if stage < 6 else ((1,) if i % 3 == 0 else ()):
            lx, ly = x0 + math.sin(lean) * L * 0.3, y0 - math.cos(lean) * L * 0.3
            P.draw_blade(d, lx, ly, -math.pi / 2 + lean + side * 0.55, L * (0.26 if stage < 6 else 0.2), side * 0.8 * S, 1.0,
                         WHEAT_GREEN if stage < 6 else STRAW, tone + (0.2 if stage == 6 else 0.0), 0.0, 0, False)
        ear_cols, etone, awns = ear
        ang = lean + (0.45 if lean >= 0 else -0.45) if nod else lean
        wheat_ear(d, tx, ty, ang, 3.3 if stage == 6 else 3.0, ear_cols, etone + 0.2 * frac(crop.seed, stage, i, 7), awns)
        if ear_cols is not stalk_cols:
            pal += ear_cols
    if stage == 6:
        pal += WHEAT_TURN
    return lay, pal


# ---------------------------------------------------------------- carrots

def frond(d, x0, y0, ang, L, cols, tone, leaflets):
    """A carrot leaf, finely cut like parsley: a thin stem arching out, side
    stalks off its upper two thirds, each ending in a little tuft of tiny
    leaflets."""
    S = P.S
    bend = 0.2 if ang >= 0 else -0.2
    pts = []
    for k in range(13):
        t = k / 12
        a = ang + bend * t
        pts.append((x0 + math.sin(a) * L * t, y0 - math.cos(a) * L * t))
    d.line(pts, fill=B.ramp(cols, tone - 0.25), width=max(1, int(0.3 * S)))
    for k in range(4, 13, 2):
        x, y = pts[k]
        a = ang + bend * k / 12
        reach = (1.6 - 0.8 * k / 12) * S * leaflets
        for side in (-1, 1):
            sa = a + side * 1.0
            ex, ey = x + math.sin(sa) * reach, y - math.cos(sa) * reach
            d.line([(x, y), (ex, ey)], fill=B.ramp(cols, tone - 0.2), width=max(1, int(0.25 * S)))
            for j in range(3):
                r = 0.42 * S * leaflets
                jx = ex + (j - 1) * 0.45 * S * leaflets
                jy = ey - (0.25 if j == 1 else 0.0) * S
                d.ellipse((jx - r, jy - r * 0.8, jx + r, jy + r * 0.6), fill=B.ramp(cols, tone - 0.05 + 0.1 * (j == 1)))
        r = 0.45 * S * leaflets
    tx, ty = pts[-1]
    d.ellipse((tx - r, ty - r, tx + r, ty + r * 0.6), fill=B.ramp(cols, tone + 0.15))


def paint_carrot(crop, stage):
    S, W, H = P.S, P.W, P.H
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    rnd = random.Random(crop.seed * 10 + stage)
    cx, base = W * 0.5, P.BASE_Y
    pal = paint_hill(d, rnd, cx, base)
    if stage == 1:
        # Just dug: the hill freshly turned, a darker hollow where the root came out.
        d.ellipse((cx - 2.2 * S, base - 2.0 * S, cx + 1.6 * S, base - 0.4 * S), fill=B.ramp(SOIL, 0.08))
        d.ellipse((cx - 1.4 * S, base - 1.8 * S, cx + 0.6 * S, base - 1.0 * S), fill=B.ramp(SOIL, 0.0))
        return lay, pal
    pal += CARROT_LEAF
    if stage == 2:
        # Two seed leaves on a short stem.
        top = base - 3.2 * S
        d.line([(cx, base - 1.2 * S), (cx, top)], fill=B.ramp(CARROT_LEAF, 0.4), width=max(1, int(0.4 * S)))
        for side in (-1, 1):
            x = cx + side * 1.1 * S
            d.ellipse((x - 1.1 * S, top - 0.7 * S, x + 1.1 * S, top + 0.5 * S), fill=B.ramp(CARROT_LEAF, 0.5))
            d.ellipse((x - 0.7 * S, top - 0.7 * S, x + 0.4 * S, top), fill=B.ramp(CARROT_LEAF, 0.75))
        return lay, pal
    # Tops: (frond count, length range, leaflet size, shoulder showing)
    count, (lo, hi), leaf, shoulder = {
        3: (4, (3.5, 5.0), 0.7, 0.0),
        4: (5, (5.5, 7.5), 0.8, 0.0),
        5: (6, (7.5, 9.5), 0.9, 0.8),
        6: (7, (8.5, 10.5), 0.95, 1.0),
    }[stage]
    if shoulder:
        # The root's orange shoulder pushing up out of the hill.
        r = 1.9 * S * shoulder
        d.ellipse((cx - r, base - 1.4 * S - r * 0.8, cx + r, base - 1.4 * S + r * 0.6), fill=B.ramp(CARROT_ORANGE, 0.45))
        d.ellipse((cx - r * 0.7, base - 1.4 * S - r * 0.8, cx + r * 0.2, base - 1.4 * S), fill=B.ramp(CARROT_ORANGE, 0.8))
        d.line([(cx - r * 0.6, base - 1.2 * S), (cx + r * 0.6, base - 1.0 * S)], fill=B.ramp(CARROT_ORANGE, 0.2),
               width=max(1, int(0.3 * S)))
        pal += CARROT_ORANGE
    order = sorted(range(count), key=lambda i: frac(crop.seed, stage, i, 1))
    for i in order:
        f = (i + 0.5) / count
        ang = (f - 0.5) * 0.8 + (frac(crop.seed, stage, i, 2) - 0.5) * 0.2
        L = (lo + (hi - lo) * frac(crop.seed, stage, i, 3)) * S
        tone = 0.4 + 0.4 * frac(crop.seed, stage, i, 4)
        frond(d, cx + (f - 0.5) * 1.2 * S, base - 1.8 * S, ang, L, CARROT_LEAF, tone, leaf)
    return lay, pal


PAINTERS = {"wheat": paint_wheat, "carrot": paint_carrot}


def build_sheet(crop):
    P.use_frame(*FRAME, density=DENSITY)
    sheet = Image.new("RGBA", (P.FW * STAGES, P.FH), (0, 0, 0, 0))
    for stage in range(1, STAGES + 1):
        lay, pal = PAINTERS[crop.painter](crop, stage)
        sheet.paste(P.shrink(lay, pal, 0.4, crop.ring), ((stage - 1) * P.FW, 0))
    return sheet


def check(out_dir, names):
    """Every frame holds art and stays a pixel inside its frame, its large
    painting does not touch the canvas edge, and the sheet is its size."""
    ok = True
    P.use_frame(*FRAME, density=DENSITY)
    for name in names:
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        want = (P.FW * STAGES, P.FH)
        if sheet.size != want:
            print("%s: sheet is %dx%d, want %dx%d" % ((name,) + sheet.size + want))
            ok = False
            continue
        for stage in range(1, STAGES + 1):
            frame = sheet.crop(((stage - 1) * P.FW, 0, stage * P.FW, P.FH))
            px = frame.load()
            edge = any(px[x, y][3] == 255 for y in range(P.FH) for x in (0, P.FW - 1)) or \
                any(px[x, y][3] == 255 for x in range(P.FW) for y in (0, P.FH - 1))
            lay, _ = PAINTERS[CROPS[name].painter](CROPS[name], stage)
            box = lay.getbbox()
            clipped = box is not None and (box[0] == 0 or box[1] == 0 or box[2] == P.W or box[3] == P.H)
            bad = edge or frame.getbbox() is None or clipped
            ok = ok and not bad
            print("%-7s growth-%d%s" % (name, stage, "  <- on the frame edge, empty or clipped" if bad else ""))
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's field crops.")
    parser.add_argument("--crop", default=",".join(CROPS), help="comma-separated, of: " + ", ".join(CROPS))
    parser.add_argument("--out", default=OUT_DIR, help="where the sheets go (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheets instead of painting them")
    args = parser.parse_args()
    names = args.crop.split(",")
    for name in names:
        if name not in CROPS:
            parser.error("unknown crop %r (have %s)" % (name, ", ".join(CROPS)))
    if args.check:
        sys.exit(0 if check(args.out, names) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name in names:
        build_sheet(CROPS[name]).save(os.path.join(args.out, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
