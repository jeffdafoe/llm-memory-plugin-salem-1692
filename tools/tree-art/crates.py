"""Paints salem's crates and notice board (LLM-968): Crate / Crate (Dark),
Crate (Light), each shut and with its lid loose, and the Notice Board's five
variants. Every pixel is drawn here, no pack art.

    python tools/tree-art/crates.py
    python tools/tree-art/crates.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/crates.py --check     # margins, sizes, a fresh paint

Writes client/assets/trees/crate-dark.png and crate-light.png (160x80: shut,
lid loose; 80x80 frames) and notice-board.png (480x128: variant-1 .. 5, 96x128
frames), drawn at twice the pack art's size and shown at render_scale 1 (the
same size in the world, twice the detail).

The crates stand corner-on, in the angled view of the buildings (LLM-699): a
box whose lid's corners are far, left, near and right, the left face lit and
the right face in shade. The box is where the LLM-699 crate was in its 40x40
frame: its near bottom corner at (18, 35.2), on the anchor's row (0.88) and
2 px left of the anchor's column (x 20), as that crate stood, so every
placed crate keeps its spot. The loose lid is slid back off the open
box (the minor-works "lid loose", damage_minor), the box's top open: its rim
and the straw inside.

The notice board: two hewn posts, a shingled hood over a plank board, and
paper notices pinned to it. Each variant shows exactly as many notices as
its content-capacity-N tag says it holds (engine/sim/noticeboard.go: the
crier's posting picks the variant by how many notices she authored, so the
notices drawn are the ones read): variant-1 five, variant-2 three, variant-3
two, variant-4 none, variant-5 four. Anchor 0.5/0.85 as the pack art.

From photographs of colonial packing crates (nailed boards on a frame) and
village notice boards.
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
import wells as WL

OUT_DIR = B.OUT_DIR
DENSITY = 2

WOOD = F.WOOD
CUT = F.CUT
IRON = F.IRON
SHINGLE = WL.SHINGLE
# The dark crate: old oak boards, grey-brown. The light crate: fresh pine.
OAK = ["1e1612", "302218", "46321f", "5e4428", "785833", "906c40"]
PINE = ["3a2410", "6a4418", "98621e", "c2862c", "dca848", "ecc66c"]
STRAW = ["5a4818", "8a7026", "b4963a", "d4b858"]
PAPER = ["8a7e66", "b4a888", "d6ccb0", "ece4cc", "f8f2e0"]
INK = ["2a241e", "4a4034"]
DARK = WL.DARK


def palette(wood):
    return wood + WOOD + CUT + IRON + SHINGLE + STRAW + PAPER + INK + DARK


def scaled(pts):
    S = P.S
    return [(x * S, y * S) for x, y in pts]


def lerp(a, b, t):
    return (a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t)


# ---------------------------------------------------------------- crates

# In the 40x40 pack frame (LLM-699): the lid's corners when shut, and how
# tall the box stands. The near bottom corner, (18, 35.2), is on the
# anchor's row and 2 px left of its column, as the LLM-699 crate's was.
FAR, LEFT, NEAR, RIGHT = (21.0, 7.5), (7.5, 14.5), (18.0, 23.5), (31.0, 17.5)
TALL = 11.7
LID_SLIDE = (4.0, -4.0)


def down(p, h=TALL):
    return (p[0], p[1] + h)


def boarded_face(d, a, b, tone, wood, seed):
    """One side of the box, from top edge a..b down TALL: a frame of boards
    round its edge, vertical slats inside with dark gaps between, nail
    heads at the frame's corners. Light falls from the upper left: tone
    sets how lit this face is."""
    rnd = random.Random(seed)
    face = [a, b, down(b), down(a)]
    d.polygon(scaled(face), fill=B.ramp(wood, tone - 0.28))
    # Slats: four boards between the frame's top and bottom rails.
    n = 4
    for k in range(n):
        t0, t1 = (k + 0.08) / n, (k + 0.92) / n
        top0, top1 = lerp(a, b, t0), lerp(a, b, t1)
        st = tone + rnd.uniform(-0.07, 0.07)
        d.polygon(scaled([down(top0, 1.6), down(top1, 1.6), down(top1, TALL - 1.6), down(top0, TALL - 1.6)]),
                  fill=B.ramp(wood, st))
        # Grain: a short dark line along the slat.
        gx = lerp(top0, top1, rnd.uniform(0.3, 0.7))
        y0 = rnd.uniform(3.0, 6.0)
        d.line(scaled([down(gx, y0), down(gx, y0 + rnd.uniform(1.5, 3.0))]), fill=B.ramp(wood, st - 0.18),
               width=max(1, int(0.25 * P.S)))
    # The frame: top and bottom rails, and the two corner posts.
    for h0, h1 in ((0.0, 1.7), (TALL - 1.7, TALL)):
        d.polygon(scaled([down(a, h0), down(b, h0), down(b, h1), down(a, h1)]), fill=B.ramp(wood, tone + 0.08))
        d.line(scaled([down(a, h0 + 0.15), down(b, h0 + 0.15)]), fill=B.ramp(wood, tone + 0.25),
               width=max(1, int(0.3 * P.S)))
    for p, q in ((a, lerp(a, b, 0.12)), (lerp(a, b, 0.88), b)):
        d.polygon(scaled([p, q, down(q), down(p)]), fill=B.ramp(wood, tone + 0.05))
    for p in (lerp(a, b, 0.06), lerp(a, b, 0.94)):
        for h in (0.85, TALL - 0.85):
            x, y = down(p, h)
            d.rectangle(((x - 0.3) * P.S, (y - 0.3) * P.S, (x + 0.3) * P.S, (y + 0.3) * P.S), fill=B.ramp(IRON, 0.3))


def lid(d, far, left, near, right, wood, seed, tone=0.68):
    """The lid: boards running from the left edge to the right, two cleats
    across them, the near edges showing the boards' thickness."""
    rnd = random.Random(seed)
    # Thickness along its two near edges.
    d.polygon(scaled([left, near, down(near, 1.1), down(left, 1.1)]), fill=B.ramp(wood, tone - 0.15))
    d.polygon(scaled([near, right, down(right, 1.1), down(near, 1.1)]), fill=B.ramp(wood, tone - 0.35))
    d.polygon(scaled([far, left, near, right]), fill=B.ramp(wood, tone - 0.3))
    n = 4
    for k in range(n):
        t0, t1 = (k + 0.12) / n, (k + 0.88) / n
        # A board from the far-left edge to the near-right edge.
        p0, p1 = lerp(far, left, t0), lerp(far, left, t1)
        q0, q1 = lerp(right, near, t0), lerp(right, near, t1)
        bt = tone + rnd.uniform(-0.08, 0.06)
        d.polygon(scaled([p0, q0, q1, p1]), fill=B.ramp(wood, bt))
        d.line(scaled([p0, q0]), fill=B.ramp(wood, bt + 0.14), width=max(1, int(0.25 * P.S)))
    for t in (0.2, 0.8):
        a, b = lerp(far, right, t), lerp(left, near, t)
        d.line(scaled([a, b]), fill=B.ramp(wood, tone - 0.05), width=int(1.1 * P.S))
        d.line(scaled([lerp(a, b, 0.0), lerp(a, b, 1.0)]), fill=B.ramp(wood, tone + 0.15), width=max(1, int(0.3 * P.S)))


def open_top(d, wood, seed):
    """The open box seen from above: the walls' top edges round a dark
    inside, straw heaped in it."""
    rnd = random.Random(seed)
    d.polygon(scaled([FAR, LEFT, NEAR, RIGHT]), fill=B.ramp(wood, 0.55))
    inset = 0.18
    c = ((FAR[0] + NEAR[0]) / 2, (FAR[1] + NEAR[1]) / 2)
    inner = [lerp(p, c, inset) for p in (FAR, LEFT, NEAR, RIGHT)]
    d.polygon(scaled(inner), fill=B.ramp(DARK, 0.5))
    # The far inside walls catch a little light.
    d.polygon(scaled([inner[0], inner[1], lerp(inner[1], c, 0.25), lerp(inner[0], c, 0.25)]), fill=B.ramp(wood, 0.25))
    d.polygon(scaled([inner[0], inner[3], lerp(inner[3], c, 0.25), lerp(inner[0], c, 0.25)]), fill=B.ramp(wood, 0.15))
    # Straw packed in it: short strokes over most of the opening, the far
    # part left dark (the walls' shadow).
    for _ in range(70):
        u, v = rnd.uniform(0.1, 0.9), rnd.uniform(0.25, 0.95)
        p = lerp(lerp(inner[1], inner[2], v), lerp(inner[0], inner[3], v), u)
        a = rnd.uniform(-0.6, 0.6)
        L = rnd.uniform(1.0, 2.2)
        d.line(scaled([p, (p[0] + L * math.cos(a), p[1] + L * math.sin(a) * 0.5)]),
               fill=B.ramp(STRAW, rnd.uniform(0.35, 0.99)), width=max(1, int(0.4 * P.S)))


def crate_shadow(sd, loose):
    """The box's shadow on the ground, down and to the right of its two near
    bottom edges; the loose lid's falls on the ground behind."""
    shape = [down(LEFT), down(NEAR), down(RIGHT), (down(RIGHT)[0] + 3.0, down(RIGHT)[1] + 1.2),
             (down(NEAR)[0] + 2.4, down(NEAR)[1] + 1.4)]
    sd.polygon(scaled(shape), fill=255)


def paint_crate(wood, loose, seed):
    P.use_frame(40, 40, density=DENSITY)
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    shade = Image.new("L", (P.W, P.H), 0)
    sd = ImageDraw.Draw(shade)
    crate_shadow(sd, loose)
    if loose:
        # The open box, then the lid slid back up and to the right lying on
        # its rim over part of the opening. The near faces are drawn after:
        # they hang below the rim, so they cover only the lid's far overhang
        # behind the box, never the part resting on it.
        open_top(d, wood, seed + 5)
        dx, dy = LID_SLIDE
        mv = lambda p: (p[0] + dx, p[1] + dy)
        lid(d, mv(FAR), mv(LEFT), mv(NEAR), mv(RIGHT), wood, seed + 9, tone=0.64)
    boarded_face(d, LEFT, NEAR, 0.62, wood, seed + 1)
    boarded_face(d, NEAR, RIGHT, 0.34, wood, seed + 2)
    if not loose:
        lid(d, FAR, LEFT, NEAR, RIGHT, wood, seed + 3)
    return F.shrink_shaded(lay, shade, palette(wood))


# ---------------------------------------------------------------- notice board

# Each variant's notice count, by the content-capacity-N tag on its state.
BOARD_NOTICES = [5, 3, 2, 0, 4]
# Where notices go, in pinning order, as (x, y, w, h, tilt) in pack px.
NOTICE_SLOTS = [(15.0, 22.5, 7.5, 8.5, -0.06), (28.0, 21.5, 7.0, 9.0, 0.05), (21.5, 32.5, 8.0, 7.0, 0.04),
                (11.5, 33.5, 6.5, 7.5, 0.08), (31.0, 33.0, 6.5, 8.0, -0.07)]


def notice(d, rnd, x, y, w, h, tilt):
    """A paper notice pinned at its top: a pale sheet tilted a little, its
    shadow on the board, a few lines of writing, a pin."""
    cx, cy = x + w / 2, y + h / 2

    def rot(px, py):
        dx, dy = px - cx, py - cy
        return (cx + dx * math.cos(tilt) - dy * math.sin(tilt), cy + dx * math.sin(tilt) + dy * math.cos(tilt))

    corners = [rot(x, y), rot(x + w, y), rot(x + w, y + h), rot(x, y + h)]
    d.polygon(scaled([(px + 0.6, py + 0.7) for px, py in corners]), fill=B.ramp(WOOD, 0.12))
    d.polygon(scaled(corners), fill=B.ramp(PAPER, 0.72 + rnd.uniform(-0.1, 0.1)))
    # A curled lower corner.
    c = corners[2]
    d.polygon(scaled([c, rot(x + w - 1.6, y + h), rot(x + w, y + h - 1.6)]), fill=B.ramp(PAPER, 0.35))
    # Writing: a heading line and short lines under it.
    lines = max(2, int((h - 3.0) / 1.4))
    for k in range(lines):
        ly = y + 1.6 + k * 1.4 + (0.3 if k else 0.0)
        lw = (w - 2.2) * (0.6 if k == 0 else rnd.uniform(0.55, 0.95))
        lx = x + 1.1 + ((w - 2.2 - lw) / 2 if k == 0 else 0.0)
        d.line(scaled([rot(lx, ly), rot(lx + lw, ly)]), fill=B.ramp(INK, 0.3 if k == 0 else 0.7),
               width=max(1, int((0.45 if k == 0 else 0.3) * P.S)))
    px, py = rot(cx, y + 0.6)
    d.rectangle(((px - 0.35) * P.S, (py - 0.35) * P.S, (px + 0.35) * P.S, (py + 0.35) * P.S), fill=B.ramp(IRON, 0.6))


def paint_board(count, seed):
    """The notice board with count notices pinned to it."""
    P.use_frame(48, 64, density=DENSITY)
    S = P.S
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    shade = Image.new("L", (P.W, P.H), 0)
    sd = ImageDraw.Draw(shade)
    rnd = random.Random(seed)
    base = 0.85 * 64
    lx, rx = 7.0, 41.0
    for x in (lx, rx):
        sd.polygon(scaled([(x - 1.3, base), (x + 1.3, base), (x + 4.0, base + 1.6), (x + 1.8, base + 1.8)]), fill=255)
    sd.polygon(scaled([(lx, base - 0.2), (rx, base - 0.2), (rx + 3.0, base + 1.4), (lx + 3.0, base + 1.4)]), fill=255)
    # The board: vertical planks between the posts, a rail across top and
    # bottom.
    bx0, bx1, by0, by1 = lx + 1.2, rx - 1.2, 17.0, 45.0
    d.rectangle((bx0 * S, by0 * S, bx1 * S, by1 * S), fill=B.ramp(WOOD, 0.25))
    planks = 7
    for k in range(planks):
        x0 = bx0 + k * (bx1 - bx0) / planks + 0.15
        x1 = bx0 + (k + 1) * (bx1 - bx0) / planks - 0.15
        t = 0.5 + rnd.uniform(-0.08, 0.08)
        d.rectangle((x0 * S, by0 * S, x1 * S, by1 * S), fill=B.ramp(WOOD, t))
        d.rectangle((x0 * S, by0 * S, (x0 + 0.5) * S, by1 * S), fill=B.ramp(WOOD, t + 0.14))
        for _ in range(2):
            gy = rnd.uniform(by0 + 2, by1 - 4)
            gx = rnd.uniform(x0 + 1.0, x1 - 0.6)
            d.line([(gx * S, gy * S), (gx * S, (gy + rnd.uniform(1.5, 3.5)) * S)], fill=B.ramp(WOOD, t - 0.2),
                   width=max(1, int(0.25 * S)))
    for y in (by0, by1 - 1.6):
        d.rectangle(((bx0 - 0.4) * S, y * S, (bx1 + 0.4) * S, (y + 1.6) * S), fill=B.ramp(WOOD, 0.45))
        d.rectangle(((bx0 - 0.4) * S, y * S, (bx1 + 0.4) * S, (y + 0.5) * S), fill=B.ramp(WOOD, 0.65))
    for x in (lx, rx):
        F.draw_post(d, x, 12.0, base, seed + int(x), w=3.0)
    for k in range(count):
        notice(d, rnd, *NOTICE_SLOTS[k])
    # A small shingled hood over it all, to keep the rain off the notices.
    WL.shingle_roof(d, rnd, 2.0, 46.0, 8.0, 16.5)
    return F.shrink_shaded(lay, shade, palette(OAK))


# ---------------------------------------------------------------- sheets

def row(frames):
    fw, fh = frames[0].size
    sheet = Image.new("RGBA", (fw * len(frames), fh), (0, 0, 0, 0))
    for i, f in enumerate(frames):
        sheet.paste(f, (i * fw, 0))
    return sheet


SHEETS = {
    "crate-dark": lambda: row([paint_crate(OAK, False, 601), paint_crate(OAK, True, 601)]),
    "crate-light": lambda: row([paint_crate(PINE, False, 611), paint_crate(PINE, True, 611)]),
    "notice-board": lambda: row([paint_board(n, 621 + i) for i, n in enumerate(BOARD_NOTICES)]),
}
FRAMES = {"crate-dark": (80, 80), "crate-light": (80, 80), "notice-board": (96, 128)}


MIGRATION = os.path.join(B.ROOT, "migrations", "LLM-968g-painted-crates_up.sql")
BOARD_ID = "df06e1c7-3912-4846-b828-6c0435af8c01"


def board_count_problems(path=MIGRATION):
    """Each notice-board frame's notice count (BOARD_NOTICES) against the
    capacity tag of the state the migration points at that frame: the
    state's src_x / 96 is its frame, its content-capacity-N tag (none: 0)
    the notices it holds. A reordered list, or a migration edit, that puts a
    frame with the wrong count under a state shows here."""
    import re
    sql = open(path, encoding="utf-8").read()
    frame_of = {m.group(1): int(m.group(2)) // 96 for m in re.finditer(
        r"'%s', '(variant-\d)', 'res://assets/trees/notice-board.png', (\d+), 0, 96, 128" % BOARD_ID, sql)}
    capacity = {m.group(1): int(m.group(2)) for m in re.finditer(
        r"'%s'::uuid, '(variant-\d)', 'content-capacity-(\d+)'" % BOARD_ID, sql)}
    problems = []
    if len(frame_of) != len(BOARD_NOTICES) or sorted(frame_of.values()) != list(range(len(BOARD_NOTICES))):
        problems.append("the migration's notice-board states do not cover the %d frames once each: %s"
                        % (len(BOARD_NOTICES), frame_of))
    for state, frame in sorted(frame_of.items()):
        if frame < len(BOARD_NOTICES) and BOARD_NOTICES[frame] != capacity.get(state, 0):
            problems.append("%s draws frame %d with %d notices but its tag says %d"
                            % (state, frame, BOARD_NOTICES[frame], capacity.get(state, 0)))
    return problems


def check(out_dir):
    """Each sheet is exactly what this script paints now, a row of frames of
    its size, and no frame has art on its outer rows or columns; and each
    notice-board frame shows as many notices as the capacity tag of the
    state the migration points at it (board_count_problems)."""
    ok = True
    for problem in board_count_problems():
        print(problem)
        ok = False
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun crates.py)" % name)
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
    parser = argparse.ArgumentParser(description="Paint salem's crates and notice board.")
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
