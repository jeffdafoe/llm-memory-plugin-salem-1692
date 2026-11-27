"""Paints salem's signs (LLM-968): the Wood Sign Post (straight and crooked)
and the Sign Board. Every pixel is drawn here, no pack art.

    python tools/tree-art/signs.py
    python tools/tree-art/signs.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/signs.py --check     # margins, sizes, a fresh paint

Writes client/assets/trees/sign-post.png and sign-board.png, drawn at twice
the pack art's size and shown at render_scale 1 (the same size in the world,
twice the detail). The feet stay on the catalog anchor (0.5/0.85), so the
placed signs keep their place.

Sheets, one row each, frames left to right:
- sign-post: default, crooked (48x64 pack px, 96x128 on the sheet). The
  crooked frame is the minor-damage state (damage_minor.go, form
  "signpost"): the arm has slipped on its peg and hangs down at a slant.
- sign-board: one frame (16x16 pack px, 32x32).

From drawings of early New England way-marks: a fingerpost, a hewn post with
a pointed arm pegged through it, lettered in paint; and a small plank board
on two stakes, of the kind set by a door or a road with a notice painted on
it. The post stands in the same fieldstone footing as the lamp posts.
"""

import argparse
import math
import os
import random
import re
import sys

from PIL import Image, ImageDraw

import build as B
import plants as P
import fences as F
import lamps as L

OUT_DIR = B.OUT_DIR
DENSITY = 2

WOOD = F.WOOD
CUT = F.CUT
IRON = F.IRON


def palette():
    return WOOD + CUT + F.STONE + F.MORTAR + IRON


# ---------------------------------------------------------------- parts

def rotated(pts, px, py, a):
    """pts turned by a radians (positive = clockwise on screen) about
    (px, py), in canvas px."""
    S = P.S
    c, s = math.cos(a), math.sin(a)
    return [((px + (x - px) * c - (y - py) * s) * S, (py + (x - px) * s + (y - py) * c) * S) for x, y in pts]


def arm(d, px, py, x0, x1, y, h, a, seed):
    """The fingerpost's arm: a plank from x0 to x1 (frame px) centred on y,
    h tall, its right end cut to a point, turned by a about the peg (px, py).
    Lit along its top edge, shaded along its bottom; the lettering is a line
    of short strokes in worn paint, too small to read."""
    rnd = random.Random(seed)
    tip = x1 - h * 0.7
    body = [(x0, y - h / 2), (tip, y - h / 2), (x1, y), (tip, y + h / 2), (x0, y + h / 2)]
    d.polygon(rotated(body, px, py, a), fill=B.ramp(WOOD, 0.42))
    # The lit top edge and the shaded underside, as thin bands.
    d.polygon(rotated([(x0, y - h / 2), (tip, y - h / 2), (tip + 0.5, y - h / 2 + 0.35),
                       (x0, y - h / 2 + 0.6)], px, py, a), fill=B.ramp(WOOD, 0.8))
    d.polygon(rotated([(x0, y + h / 2 - 0.7), (tip + 0.4, y + h / 2 - 0.5), (tip, y + h / 2),
                       (x0, y + h / 2)], px, py, a), fill=B.ramp(WOOD, 0.3))
    # The pale end grain where the arm is cut square on the left.
    d.polygon(rotated([(x0, y - h / 2), (x0 + 0.5, y - h / 2), (x0 + 0.5, y + h / 2), (x0, y + h / 2)], px, py, a),
              fill=B.ramp(CUT, 0.55))
    # The lettering: pale paint on the dark board, two short words of a few
    # strokes each, spaced wide enough to stay apart at game zoom.
    x = px + 3.0
    end = tip - 1.2
    for word in (3, 4):
        for _ in range(word):
            if x >= end:
                break
            top, bot = y - h * 0.22, y + h * 0.22
            lean = rnd.uniform(-0.25, 0.25)
            d.line(rotated([(x, top), (x + lean, bot)], px, py, a), fill=B.ramp(CUT, 0.8), width=max(1, int(0.5 * P.S)))
            x += 1.5
        x += 2.0


def peg(d, x, y):
    """The wooden peg the arm is fixed by: a pale round end proud of the
    face, ringed dark."""
    S = P.S
    d.ellipse(((x - 1.1) * S, (y - 1.1) * S, (x + 1.1) * S, (y + 1.1) * S), fill=B.ramp(WOOD, 0.12))
    d.ellipse(((x - 0.75) * S, (y - 0.75) * S, (x + 0.75) * S, (y + 0.75) * S), fill=B.ramp(CUT, 0.6))
    d.ellipse(((x - 0.55) * S, (y - 0.6) * S, (x + 0.1) * S, (y + 0.05) * S), fill=B.ramp(CUT, 0.95))


def post_edges(d, x, top, bottom, w):
    """A firmer lit edge on the post's upper-left face and a dark edge on its
    right, so the hewn timber reads against the stone at game zoom."""
    S = P.S
    d.rectangle(((x - w / 2) * S, top * S, (x - w / 2 + 0.5) * S, bottom * S), fill=B.ramp(WOOD, 0.85))
    d.rectangle(((x + w / 2 - 0.5) * S, top * S, (x + w / 2) * S, bottom * S), fill=B.ramp(WOOD, 0.1))


# ---------------------------------------------------------------- sign post

POST_FRAME = (48, 64)
POST_FEET = 0.85


def paint_sign_post(crooked):
    """A hewn post in a fieldstone footing, with one pointed arm pegged
    across its face at the top, pointing right (as the pack art). Crooked:
    the arm has slipped on its peg and hangs down at a slant, its square end
    kicked up behind the post."""
    P.use_frame(*POST_FRAME, density=DENSITY)
    lay, d, shade, sd = L.new_layers()
    base, cx = POST_FEET * POST_FRAME[1], 24.0
    rnd = random.Random(681)
    # The footing's shadow, a little fuller than the lamp posts' and falling
    # further down and right, so the post sits on the ground at game zoom.
    sd.ellipse(((cx - 4.0) * P.S, (base - 2.2) * P.S, (cx + 10.5) * P.S, (base + 2.4) * P.S), fill=255)
    F.draw_post(d, cx, 2.2, base - 8.0, 682, w=3.4)
    post_edges(d, cx, 2.2, base - 8.0, 3.4)
    arm_y, h = 6.4, 4.4
    px, py = cx, arm_y
    a = math.radians(38) if crooked else 0.0
    x0 = cx - 3.6
    if crooked:
        # The upper peg has worked out: the arm has dropped and swung down on
        # the lower one. Where it sat shows above it as a band of fresh,
        # unweathered wood on the post, with the upper peg's empty hole.
        S = P.S
        d.rectangle(((cx - 1.7) * S, (arm_y - h / 2) * S, (cx + 1.7) * S, (arm_y + h / 2) * S), fill=B.ramp(CUT, 0.7))
        d.ellipse(((cx - 0.7) * S, (arm_y - 1.4) * S, (cx + 0.7) * S, (arm_y - 0.0) * S), fill=B.ramp(WOOD, 0.05))
        py = arm_y + 2.8
        # A short stub behind the post, so its kicked-up end stays off the scar.
        x0 = cx - 2.0
    arm(d, px, py, x0, 44.0, py, h, a, 683)
    peg(d, px, py)
    L.footing(d, rnd, cx, base - 9.0, base, 11.0)
    return L.finish(lay, shade)


# ---------------------------------------------------------------- sign board

BOARD_FRAME = (16, 16)
BOARD_FEET = 0.85


def paint_sign_board():
    """A small board of two planks on two stakes driven into the ground,
    standing straight on, a notice painted on it."""
    P.use_frame(*BOARD_FRAME, density=DENSITY)
    lay, d, shade, sd = L.new_layers()
    S = P.S
    base = BOARD_FEET * BOARD_FRAME[1]
    # A compact shadow under the legs, falling down and right.
    sd.ellipse((4.0 * S, (base - 1.2) * S, 14.8 * S, (base + 1.3) * S), fill=255)
    # The board stands tall on short legs: most of the frame is its face.
    bx0, bx1, by0, by1 = 2.4, 13.6, 1.2, 10.6
    # The stakes, narrow and in shade behind the board, showing only below
    # it; dark where they go into the ground.
    for x in (5.2, 10.8):
        d.rectangle(((x - 0.45) * S, (by1 - 0.5) * S, (x + 0.45) * S, base * S), fill=B.ramp(WOOD, 0.28))
        d.rectangle(((x - 0.45) * S, (by1 - 0.5) * S, (x - 0.1) * S, base * S), fill=B.ramp(WOOD, 0.45))
        d.rectangle(((x - 0.45) * S, (base - 0.9) * S, (x + 0.45) * S, base * S), fill=B.ramp(WOOD, 0.12))
    # The board: two planks, the upper a little lighter, a dark seam between.
    mid = (by0 + by1) / 2
    for y0, y1, tone in ((by0, mid, 0.5), (mid, by1, 0.42)):
        d.rectangle((bx0 * S, y0 * S, bx1 * S, y1 * S), fill=B.ramp(WOOD, tone))
        d.rectangle((bx0 * S, y0 * S, bx1 * S, (y0 + 0.4) * S), fill=B.ramp(WOOD, tone + 0.25))
    d.rectangle((bx0 * S, (mid - 0.2) * S, bx1 * S, (mid + 0.2) * S), fill=B.ramp(WOOD, 0.12))
    # The pale end grain on the left, the shaded underside.
    d.rectangle((bx0 * S, by0 * S, (bx0 + 0.45) * S, by1 * S), fill=B.ramp(CUT, 0.55))
    d.rectangle((bx0 * S, (by1 - 0.5) * S, bx1 * S, by1 * S), fill=B.ramp(WOOD, 0.2))
    # The notice: lines of pale painted writing, each a row of short
    # horizontal words, two rows on the upper plank and one on the lower.
    rnd = random.Random(691)
    for ly, right in ((by0 + 1.7, bx1 - 1.4), (by0 + 3.4, bx1 - 3.0), (mid + 1.6, bx1 - 2.0)):
        x = bx0 + 1.5
        while x < right - 0.8:
            w = min(rnd.uniform(1.6, 3.2), right - x)
            d.rectangle((x * S, (ly - 0.3) * S, (x + w) * S, (ly + 0.3) * S), fill=B.ramp(CUT, 0.9))
            x += w + 0.9
    # Nail heads where the planks meet the stakes.
    for x in (5.2, 10.8):
        for y in (mid - 1.4, by1 - 1.4):
            d.rectangle(((x - 0.3) * S, (y - 0.3) * S, (x + 0.3) * S, (y + 0.3) * S), fill=B.ramp(IRON, 0.5))
    return L.finish(lay, shade)


# ---------------------------------------------------------------- sheets

# Each sheet's frames, left to right, as (catalog asset id, state). The order
# is the sheet's layout: the migration points each state at its frame's
# offset, and --check holds the two together. A frame's picture is chosen by
# its state's name (paint_frame), so the layout cannot name one state and
# paint another.
POST_ID = "796d76b4-9a3b-4541-9852-98d389c1906a"    # Wood Sign Post
BOARD_ID = "a1f9f3e6-d790-48f9-a169-4c26186e113c"   # Sign Board
LAYOUT = {
    "sign-post": [(POST_ID, "default"), (POST_ID, "crooked")],
    "sign-board": [(BOARD_ID, "default")],
}


def paint_frame(asset_id, state):
    if asset_id == POST_ID and state in ("default", "crooked"):
        return paint_sign_post(state == "crooked")
    if asset_id == BOARD_ID and state == "default":
        return paint_sign_board()
    raise ValueError("no painter for %s %s" % (asset_id, state))


SHEETS = {name: (lambda frames=frames: L.row([paint_frame(a, s) for a, s in frames])) for name, frames in LAYOUT.items()}
# Frame size on the sheet (density 2), by sheet.
FRAMES = {"sign-post": (POST_FRAME[0] * DENSITY, POST_FRAME[1] * DENSITY),
          "sign-board": (BOARD_FRAME[0] * DENSITY, BOARD_FRAME[1] * DENSITY)}
MIGRATION = os.path.join(B.ROOT, "migrations", "LLM-968n-painted-signs_up.sql")


def migration_states(path=MIGRATION):
    """The migration's llm968n_state rows, as {(asset id, state): (sheet,
    src_x, src_y, src_w, src_h)}."""
    text = open(path, encoding="utf-8").read()
    m = re.search(r"INSERT INTO llm968n_state VALUES(.*?);", text, re.S)
    if not m:
        return {}
    rows = {}
    for r in re.finditer(r"\('([0-9a-f-]{36})', '([^']+)', '([^']+)', (\d+), (\d+), (\d+), (\d+), \d+, [\d.]+\)", m.group(1)):
        rows[(r.group(1), r.group(2))] = (r.group(3), int(r.group(4)), int(r.group(5)), int(r.group(6)), int(r.group(7)))
    return rows


def migration_problems(path=MIGRATION):
    """The migration must point every state at its frame in LAYOUT (the
    sheet, the frame's offset in the row, the frame size), and at nothing
    else."""
    rows = migration_states(path)
    want = {}
    for name, frames in LAYOUT.items():
        fw, fh = FRAMES[name]
        for i, (asset_id, state) in enumerate(frames):
            want[(asset_id, state)] = ("res://assets/trees/%s.png" % name, i * fw, 0, fw, fh)
    out = []
    for key in sorted(set(want) | set(rows)):
        if rows.get(key) != want.get(key):
            out.append("migration state %s %s is %s, the layout says %s" % (key[0], key[1], rows.get(key), want.get(key)))
    return out


def check(out_dir):
    """Each sheet is exactly what this script paints now, a row of exactly as
    many frames as LAYOUT names, of the size the catalog rects expect, with
    no art on a frame's outer rows or columns; and the migration points each
    state at its own frame."""
    ok = True
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun signs.py)" % name)
            ok = False
            continue
        fw, fh = FRAMES[name]
        n = len(LAYOUT[name])
        if sheet.size != (fw * n, fh):
            print("%s: sheet %dx%d is not a row of %d %dx%d frames" % (name, sheet.width, sheet.height, n, fw, fh))
            ok = False
            continue
        for i in range(n):
            if any(F.edges_touched(sheet.crop((i * fw, 0, (i + 1) * fw, fh)))):
                print("%s frame %d: art on the frame edge" % (name, i))
                ok = False
    # The crooked frame is the damaged one: it must differ from the default,
    # and the migration must point the crooked state at it.
    post = Image.open(os.path.join(out_dir, "sign-post.png")).convert("RGBA")
    fw, fh = FRAMES["sign-post"]
    # The arm's tip is the art right of the post (frame x 64 on, above the
    # footing); crooked, it hangs well below where it points straight.
    tips = []
    for i in range(2):
        box = post.crop((i * fw + 64, 0, (i + 1) * fw, fh // 2)).getbbox()
        tips.append(box[3] if box else 0)
    if tips[1] < tips[0] + 16:
        print("sign-post: the crooked frame's arm (tip down to row %d) does not hang below the default's (row %d)"
              % (tips[1], tips[0]))
        ok = False
    for problem in migration_problems():
        print(problem)
        ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's signs.")
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
