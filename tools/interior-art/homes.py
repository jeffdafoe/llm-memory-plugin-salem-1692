"""Paints salem's home interiors (LLM-969): one room per house type, keyed by
the floor plan's `art` (LLM-782). Every pixel is drawn here, no pack art.

    python tools/interior-art/homes.py
    python tools/interior-art/homes.py --out DIR   # elsewhere (prototypes)
    python tools/interior-art/homes.py --check     # a fresh paint, sizes, rects

Writes client/assets/interiors/<art>_common.png and <art>_pieces.png for each
home here, and home_fire.png, the hearth fire (4 frames of 16x16, played at
the room's anim x/y). See common.py for the view, the sheets and the kit.

A 1690s New England house: whitewashed plaster between oak framing, a
fieldstone hearth, a small leaded casement, wide pine floorboards; joined oak
beds under wool coverlets (madder red, indigo), a trestle table, ladder-back
chairs, a cupboard with pewter, a blanket chest.

The pieces stand exactly where interior_rooms.gd's entry for the room says
(its rects and sort lines, which put people on and behind them), and --check
holds the two together: every piece's rect must hold art, and every cover
rect must hold the coverlet.
"""

import argparse
import os
import random
import sys

from PIL import Image

import common as C

# The painted rooms are drawn at twice the room's pixels (interior_rooms.gd
# entry "density": 2).
C.set_density(2)


def red_tiny_room(seed=969):
    """home_red_tiny, 112 x 144: the back wall (rows 0-2) with the hearth at
    the left and the window, the floor below; the door at the bottom."""
    w, h, wall = 112, 144, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd)
    C.plaster_wall(room, 0, w, 0, wall - 2, rnd)
    # The frame: a girt across under the ceiling, posts at the corners and
    # between the hearth and the window, a brace.
    C.timber(room, 0, 4, w, 8, 0.42, rnd)
    for px in (44, 104):
        C.timber(room, px, 0, px + 4, wall - 2, 0.4, rnd)
    C.casement(room, 66, 16, 84, 30)
    C.wear(room, 56, 126, 16, 14)
    C.light_pool(room, 66, wall + 1, 84, wall + 24, 80)
    C.wall_foot(room, 0, w, wall)
    C.hearth(room, 2, 38, 0, wall, (12, 36, 16, 16), rnd)
    # What hangs on the walls: herbs drying from the girt, a shelf of
    # crockery, a peg rail with a hat and a cloak over the north bed, a broom.
    C.herb_bundles(room, 50, 63, 8, rnd, 3)
    C.shelf(room, 49, 63, 31, rnd, ("jug", "plate", "bowl"))
    C.peg_rail(room, 86, 102, 13, rnd, ("hat", "cloak"))
    C.broom(room, 62, 47, 20)
    # A braided rag rug between the table and the beds.
    C.rag_rug(room, 26, 106, 72, 120, rnd)
    # Where the furniture stands, its shadow on the floor (down and right).
    # Compact and dark, right under each piece's feet and lower edge.
    for x0, y0, x1, y1 in ((81, 85, 113, 90), (81, 139, 113, 144), (34, 92, 66, 96), (17, 93, 30, 96), (62, 93, 75, 96),
                           (1, 141, 17, 144), (17, 142, 49, 144)):
        room.shadow(x0, y0, x1, y1, 220)
    return C.shrink(room, C.ROOM_PALETTE)


def red_tiny_pieces(seed=970):
    w, h = 112, 144
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.rope_bed(room, 80, 44, 32, 44, C.INDIGO, rnd, "overshot")
    C.rope_bed(room, 80, 98, 32, 44, C.MADDER, rnd, "check")
    C.trestle_table(room, 32, 70, 32, 25, rnd)
    C.ladder_chair(room, 16, 68, 14, 27, back_left=True)
    C.ladder_chair(room, 61, 68, 14, 27, back_left=False)
    C.cupboard(room, 0, 96, 16, 48)
    C.blanket_chest(room, 16, 128, 32, 16)
    return C.shrink(room, C.PIECE_PALETTE, outline=0.35, ring=0.18)


def home_fire():
    return C.fire_strip(16, 16, 4, seed=7)


SHEETS = {
    "home_red_tiny_common": red_tiny_room,
    "home_red_tiny_pieces": red_tiny_pieces,
    "home_fire": home_fire,
}
# The interior_rooms.gd entries this module paints, by key.
ROOMS = {"home_red_tiny": ("home_red_tiny_common", "home_red_tiny_pieces")}
# Art px: the rooms at twice their room px (112 x 144), the fire 4 frames of
# 16 x 16 room px.
SIZES = {"home_red_tiny_common": (224, 288), "home_red_tiny_pieces": (224, 288), "home_fire": (128, 32)}


def check(out_dir):
    """Each sheet is exactly what this script paints now and of its size; the
    room entry points at these sheets; every piece's rect holds art; every
    cover rect lies inside its bed's piece and holds the coverlet; every anim
    this module paints is a strip of the entry's frame count."""
    ok = True
    sheets = {}
    for name, build in SHEETS.items():
        path = os.path.join(out_dir, name + ".png")
        sheet = Image.open(path).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun homes.py)" % name)
            ok = False
        if sheet.size != SIZES[name]:
            print("%s: %dx%d, not %dx%d" % ((name,) + sheet.size + SIZES[name]))
            ok = False
        sheets[name] = sheet
    for key, (common, pieces) in ROOMS.items():
        entry = C.room_entry(key)
        want = {"sheet": "res://assets/interiors/%s.png" % common, "pieces_sheet": "res://assets/interiors/%s.png" % pieces}
        if entry["sheets"] != want:
            print("%s: interior_rooms.gd sheets are %s, not %s" % (key, entry["sheets"], want))
            ok = False
        d = entry["density"]
        if d != C.DENSITY:
            print("%s: the entry's density %d is not the painted %d" % (key, d, C.DENSITY))
            ok = False
        for name in (common, pieces):
            if sheets[name].size != (entry["size"][0] * d, entry["size"][1] * d):
                print("%s: %s is %s, not the entry's size %s at density %d" % (key, name, sheets[name].size, entry["size"], d))
                ok = False
        alpha = sheets[pieces].split()[3]
        for x, y, w, h, _sort in entry["pieces"]:
            box = alpha.crop((x * d, y * d, (x + w) * d, (y + h) * d)).getbbox()
            if box is None:
                print("%s: no art in the piece rect %s" % (key, (x, y, w, h)))
                ok = False
        for bed, (x, y, w, h) in entry["covers"].items():
            inside = any(px <= x and py <= y and x + w <= px + pw and y + h <= py + ph for px, py, pw, ph, _ in entry["pieces"])
            filled = alpha.crop(((x + 4) * d, (y + 2) * d, (x + w - 4) * d, (y + h - 8) * d)).getextrema()[0] == 255
            if not inside or not filled:
                print("%s: cover %s %s is not a covered part of a bed piece" % (key, bed, (x, y, w, h)))
                ok = False
        for sheet, frames, _x, _y in entry["anims"]:
            name = os.path.splitext(os.path.basename(sheet))[0]
            if sheet.startswith("res://") and name in sheets and sheets[name].width != frames * sheets[name].height:
                print("%s: anim %s is not %d square frames" % (key, name, frames))
                ok = False
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's home interiors.")
    parser.add_argument("--out", default=C.OUT_DIR, help="where the sheets go (default client/assets/interiors)")
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
