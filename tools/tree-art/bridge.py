"""Paints salem's bridge (LLM-968). Every pixel is drawn here, no pack art.

    python tools/tree-art/bridge.py
    python tools/tree-art/bridge.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/bridge.py --check     # margins, size, a fresh paint

Writes client/assets/trees/bridge.png (one 192x128 frame), drawn at twice
the pack art's size and shown at render_scale 1.

A timber bridge of the kind New England towns built: a deck of planks laid
across log stringers, carried over the stream on trestle bents (posts driven
into the bed, braced), a post-and-rail side on each edge, and fieldstone
abutments where it meets the banks. Seen as the ground is drawn, straight
on: the deck runs east-west over the two road rows, its south face and the
bents below it toward the viewer.

The deck covers exactly DECK_COLS x DECK_ROWS tiles; the anchor is the
middle column of its south row, so the footprint (left 2, right 2, top 1,
bottom 0) is the deck. From photographs and drawings of early timber
trestle bridges.
"""

import argparse
import os
import random
import re
import sys

from PIL import Image, ImageDraw

import build as B
import plants as P
import fences as F

OUT_DIR = B.OUT_DIR
DENSITY = 2

WOOD = F.WOOD
CUT = F.CUT
IRON = F.IRON
STONE = F.STONE
# Weathered timber, grey-brown.
TIMBER = ["1c1814", "2e2620", "443a30", "5c5042", "766856", "90826c", "a89a82"]
PALETTE = TIMBER + WOOD + CUT + IRON + STONE

TILE = 16                  # pack px per tile
DECK_COLS, DECK_ROWS = 5, 2
FRAME = (96, 64)           # pack px
DECK_X0 = (FRAME[0] - DECK_COLS * TILE) / 2     # the deck's west edge
DECK_Y0 = 12.0                                  # its north edge
DECK_X1 = DECK_X0 + DECK_COLS * TILE
DECK_Y1 = DECK_Y0 + DECK_ROWS * TILE
# The anchor: the middle column of the deck's south row, at its centre.
ANCHOR = (DECK_X0 + (DECK_COLS // 2) * TILE + TILE / 2, DECK_Y1 - TILE / 2)
FOOTPRINT = (DECK_COLS // 2, DECK_COLS // 2, DECK_ROWS - 1, 0)

FASCIA = 4.0               # the south stringer's face, below the deck
LEG = 9.0                  # how far the bents show below the stringer
RAIL = 6.0                 # the side rails' height


def rect(d, x0, y0, x1, y1, col):
    S = P.S
    d.rectangle((x0 * S, y0 * S, x1 * S - 1, y1 * S - 1), fill=col)


def line(d, pts, col, w):
    d.line([(x * P.S, y * P.S) for x, y in pts], fill=col, width=max(1, int(w * P.S)))


def abutment(d, rnd, x0, x1, top, bottom):
    """A fieldstone abutment under one end of the deck: stones laid in
    rough courses, lit on top."""
    y = bottom
    while y > top:
        h = rnd.uniform(2.4, 3.4)
        x = x0 - rnd.uniform(0, 1.5)
        while x < x1:
            w = rnd.uniform(3.0, 5.0)
            tone = rnd.uniform(0.35, 0.6)
            d.ellipse((x * P.S, (y - h) * P.S, (x + w) * P.S, y * P.S), fill=B.ramp(STONE, tone))
            d.ellipse(((x + 0.6) * P.S, (y - h) * P.S, (x + w * 0.6) * P.S, (y - h + 1.0) * P.S),
                      fill=B.ramp(STONE, tone + 0.25))
            x += w - 0.4
        y -= h - 0.5


def bent(d, x):
    """A trestle bent seen end-on below the south stringer: two posts into
    the water and a diagonal brace."""
    top, foot = DECK_Y1 + FASCIA, DECK_Y1 + FASCIA + LEG
    for px, tone in ((x - 2.2, 0.42), (x + 2.2, 0.3)):
        rect(d, px - 0.9, top, px + 0.9, foot, B.ramp(TIMBER, tone))
        line(d, [(px - 0.9, top), (px - 0.9, foot)], B.ramp(TIMBER, tone + 0.2), 0.3)
    line(d, [(x - 2.2, top + 1.0), (x + 2.2, foot - 2.0)], B.ramp(TIMBER, 0.36), 0.8)
    # Where the posts meet the water: a pale ripple either side.
    for px in (x - 2.2, x + 2.2):
        line(d, [(px - 1.8, foot + 0.2), (px - 0.9, foot - 0.2)], B.ramp(STONE, 0.92), 0.35)
        line(d, [(px + 0.9, foot - 0.2), (px + 1.8, foot + 0.2)], B.ramp(STONE, 0.92), 0.35)


def rail(d, y, posts, tone, height):
    """A post-and-rail side standing on the deck edge at y."""
    for x in posts:
        rect(d, x - 0.8, y - height, x + 0.8, y + 0.4, B.ramp(TIMBER, tone - 0.1))
        line(d, [(x - 0.8, y - height), (x - 0.8, y + 0.4)], B.ramp(TIMBER, tone + 0.15), 0.3)
    rect(d, DECK_X0 + 0.5, y - height, DECK_X1 - 0.5, y - height + 1.3, B.ramp(TIMBER, tone))
    line(d, [(DECK_X0 + 0.5, y - height), (DECK_X1 - 0.5, y - height)], B.ramp(TIMBER, tone + 0.22), 0.35)


def paint_bridge(seed=968):
    P.use_frame(*FRAME, density=DENSITY)
    lay = Image.new("RGBA", (P.W, P.H), (0, 0, 0, 0))
    shade = Image.new("L", (P.W, P.H), 0)
    d, sd = ImageDraw.Draw(lay), ImageDraw.Draw(shade)
    rnd = random.Random(seed)
    # The cast shadow on the water, south-east of the deck.
    sd.rectangle(((DECK_X0 + 3) * P.S, (DECK_Y1 + FASCIA) * P.S, (DECK_X1 + 2) * P.S,
                  (DECK_Y1 + FASCIA + LEG + 2.5) * P.S), fill=255)
    # The abutments under each end, then the bents across the stream.
    abutment(d, rnd, DECK_X0 - 1.0, DECK_X0 + TILE - 3, DECK_Y1 - 3, DECK_Y1 + FASCIA + LEG - 1)
    abutment(d, rnd, DECK_X1 - TILE + 3, DECK_X1 + 1.0, DECK_Y1 - 3, DECK_Y1 + FASCIA + LEG - 1)
    for k in (1.5, 2.5, 3.5):
        bent(d, DECK_X0 + k * TILE)
    # The north rail, behind the deck.
    posts = [DECK_X0 + 1.2 + i * (DECK_X1 - DECK_X0 - 2.4) / 6 for i in range(7)]
    rail(d, DECK_Y0 + 0.5, posts, 0.5, RAIL)
    # The deck: planks laid north-south across the stringers, each its own
    # shade, with grain, a gap between, and nail heads over the stringers.
    x = DECK_X0
    while x < DECK_X1 - 0.1:
        w = min(rnd.uniform(3.2, 4.2), DECK_X1 - x)
        tone = rnd.uniform(0.58, 0.78)
        rect(d, x, DECK_Y0, x + w, DECK_Y1, B.ramp(TIMBER, tone))
        for _ in range(4):
            gx = x + rnd.uniform(0.6, w - 0.6)
            y0 = rnd.uniform(DECK_Y0, DECK_Y1 - 8)
            line(d, [(gx, y0), (gx, y0 + rnd.uniform(4, 10))], B.ramp(TIMBER, tone + rnd.choice((-0.12, 0.08))), 0.3)
        line(d, [(x + w - 0.2, DECK_Y0), (x + w - 0.2, DECK_Y1)], B.ramp(TIMBER, 0.12), 0.45)
        line(d, [(x + 0.25, DECK_Y0), (x + 0.25, DECK_Y1)], B.ramp(TIMBER, tone + 0.14), 0.3)
        # Two nails into each of the outer stringers.
        for sy in (DECK_Y0 + 2.5, DECK_Y1 - 2.5):
            rect(d, x + w / 2 - 0.3, sy - 0.3, x + w / 2 + 0.3, sy + 0.3, B.ramp(IRON, 0.35))
        x += w
    # The south stringer's face under the deck's edge.
    rect(d, DECK_X0, DECK_Y1, DECK_X1, DECK_Y1 + FASCIA, B.ramp(TIMBER, 0.3))
    line(d, [(DECK_X0, DECK_Y1 + 0.3), (DECK_X1, DECK_Y1 + 0.3)], B.ramp(TIMBER, 0.55), 0.4)
    for gx in range(int(DECK_X0) + 3, int(DECK_X1) - 2, 7):
        line(d, [(gx, DECK_Y1 + 1.5), (gx + 3.5, DECK_Y1 + 1.5)], B.ramp(TIMBER, 0.22), 0.3)
    # The south rail, on the near edge: lower, and only a post at each end
    # and the middle, so it reads as a rail in front and not as a fence
    # across the planks.
    rail(d, DECK_Y1 - 0.5, [posts[0], posts[3], posts[-1]], 0.7, RAIL - 2.5)
    return F.shrink_shaded(lay, shade, PALETTE)


SHEETS = {"bridge": lambda: paint_bridge()}
FRAMES = {"bridge": (FRAME[0] * DENSITY, FRAME[1] * DENSITY, 1)}


MIGRATION = os.path.join(B.ROOT, "migrations", "LLM-968k-painted-bridge_up.sql")


def migration_problems(path=MIGRATION):
    """The migration must write this script's anchor fraction, footprint and
    frame rect: in its asset and state updates, and in the postcondition
    checks after them. Comments are dropped first, so a stale comment can't
    stand in for the SQL."""
    sql = re.sub(r"--[^\n]*", "", open(path, encoding="utf-8").read())
    sql = " ".join(sql.split())
    fw, fh, _ = FRAMES["bridge"]
    asset_update = re.search(r"UPDATE asset SET (.*?) WHERE id = bridge;", sql)
    state_update = re.search(r"UPDATE asset_state SET (.*?) WHERE asset_id = bridge AND state = 'default';", sql)
    if not asset_update or not state_update:
        return ["the migration's asset or state update is not where expected"]
    checks = sql[state_update.end():]
    want = [
        (asset_update.group(1), "anchor_x = %.1f::double precision / %d, anchor_y = %.1f::double precision / %d"
         % (ANCHOR[0], FRAME[0], ANCHOR[1], FRAME[1])),
        (asset_update.group(1), "footprint_left = %d, footprint_right = %d, footprint_top = %d, footprint_bottom = %d"
         % FOOTPRINT),
        (state_update.group(1), "sheet = 'res://assets/trees/bridge.png', src_x = 0, src_y = 0, src_w = %d, src_h = %d"
         % (fw, fh)),
        (checks, "%.1f::double precision / %d, %.1f::double precision / %d, %d, %d, %d, %d,"
         % ((ANCHOR[0], FRAME[0], ANCHOR[1], FRAME[1]) + FOOTPRINT)),
        (checks, "'res://assets/trees/bridge.png'::text, 0, 0, %d, %d, 1," % (fw, fh)),
    ]
    # Each value must end where its assignment or tuple element ends (at a
    # comma, or the end of the SET body), so src_h = 1280 can't pass for 128.
    return ["the migration does not write: %s" % w for scope, w in want
            if w.rstrip(",") + "," not in scope + ","]


def check(out_dir):
    """The sheet is exactly what this script paints now, exactly one frame of
    its size, and has no art on its outer rows or columns; and the migration
    writes this script's anchor, footprint and rect."""
    ok = True
    for problem in migration_problems():
        print(problem)
        ok = False
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun bridge.py)" % name)
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
    parser = argparse.ArgumentParser(description="Paint salem's bridge.")
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
