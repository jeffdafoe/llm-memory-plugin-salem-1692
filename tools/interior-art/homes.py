"""Paints salem's home interiors (LLM-969): one room per house type, keyed by
the floor plan's `art` (LLM-782). Every pixel is drawn here, no pack art.

    python tools/interior-art/homes.py
    python tools/interior-art/homes.py --out DIR   # elsewhere (prototypes)
    python tools/interior-art/homes.py --check     # a fresh paint, sizes, geometry

Writes client/assets/interiors/<art>_common.png and <art>_pieces.png for each
home here, and home_fire.png, the hearth fire (4 frames of 16x16 room px),
all at density 2. See common.py for the view, the sheets and the kit.

A 1690s New England house: limewashed plaster between oak framing, a
fieldstone hearth with a trammel and pot, a small leaded casement, wide pine
floorboards; joined oak beds under wool coverlets (indigo overshot, madder
check), a trestle table laid with a trencher, a jug and a loaf, ladder-back
rush-seat chairs, a cupboard with pewter and redware, a blanket chest; herbs
drying, a crockery shelf, a peg rail, a broom, a braided rug.

Each room's geometry is declared once here (ROOMS): its size, every piece's
rect and sort line with its painter, every bed's cover rect, every anim's
sheet, frame count and position. The painters draw from it, and --check
requires the room's entry in interior_rooms.gd to say exactly the same, so
the art and the lines that put people on and behind it cannot drift apart.
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

FIRE = "res://assets/interiors/home_fire.png"


def red_tiny_room(seed=969):
    """home_red_tiny, 112 x 144: the back wall (rows 0-2) with the hearth at
    the left and the window, the floor below; the door at the bottom."""
    w, h, wall = 112, 144, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd)
    C.plaster_wall(room, 0, w, 0, wall - 2, rnd)
    # The frame: a girt across under the ceiling, posts between the hearth
    # and the window and at the right.
    C.timber(room, 0, 4, w, 8, 0.42, rnd)
    for px in (44, 104):
        C.timber(room, px, 0, px + 4, wall - 2, 0.4, rnd)
    C.casement(room, 66, 16, 84, 30)
    C.wear(room, 56, 126, 16, 14)
    C.light_pool(room, 66, wall + 1, 84, wall + 24, 80)
    C.wall_foot(room, 0, w, wall)
    # The hearth's firebox is where the fire anim plays (ROOMS anims).
    fire = ROOMS["home_red_tiny"]["anims"][0]
    C.hearth(room, 2, 38, 0, wall, (fire[2], fire[3], 16, 16), rnd)
    # What hangs on the walls: herbs drying from the girt, a shelf of
    # crockery, a peg rail with a hat and a cloak over the north bed, a broom.
    C.herb_bundles(room, 50, 63, 8, rnd, 3)
    C.shelf(room, 49, 63, 31, rnd, ("jug", "plate", "bowl"))
    C.peg_rail(room, 86, 102, 13, rnd, ("hat", "cloak"))
    C.broom(room, 62, 47, 20)
    # A braided rag rug between the table and the beds.
    C.rag_rug(room, 26, 106, 72, 120, rnd)
    # Each piece's shadow on the floor, compact and dark under its feet and
    # lower edge.
    for (x, y, pw, ph, _sort), _painter in ROOMS["home_red_tiny"]["pieces"]:
        room.shadow(x + 1, y + ph - 3, x + pw + 1, y + ph + 0.5, 220)
    return C.shrink(room, C.ROOM_PALETTE)


def red_tiny_pieces(seed=970):
    rnd = random.Random(seed)
    spec = ROOMS["home_red_tiny"]
    return C.paint_pieces(*spec["size"], [((x, y, w, h), (lambda room, p=painter, r=(x, y, w, h): p(room, *r, rnd)))
                                         for (x, y, w, h, _sort), painter in spec["pieces"]], C.PIECE_PALETTE)


def _bed(cover, pattern):
    return lambda room, x, y, w, h, rnd: C.rope_bed(room, x, y, w, h, cover, rnd, pattern)


def _chair(back_left):
    return lambda room, x, y, w, h, rnd: C.ladder_chair(room, x, y, w, h, back_left=back_left)


ROOMS = {
    "home_red_tiny": {
        "sheets": ("home_red_tiny_common", "home_red_tiny_pieces"),
        "size": (112, 144),
        # ((x, y, w, h, sort_y), painter): the rects and sort lines the
        # floor plan's stations were laid out against.
        "pieces": [
            ((80, 44, 32, 44, 71), _bed(C.INDIGO, "overshot")),
            ((80, 98, 32, 44, 125), _bed(C.MADDER, "check")),
            ((32, 70, 32, 25, 88), lambda room, x, y, w, h, rnd: C.trestle_table(room, x, y, w, h, rnd)),
            ((16, 68, 14, 27, 89), _chair(True)),
            ((61, 68, 14, 27, 89), _chair(False)),
            ((0, 96, 16, 48, 143), lambda room, x, y, w, h, rnd: C.cupboard(room, x, y, w, h)),
            ((16, 128, 32, 16, 143), lambda room, x, y, w, h, rnd: C.blanket_chest(room, x, y, w, h)),
        ],
        "covers": {"bed_north": (80, 56, 32, 32), "bed_south": (80, 110, 32, 32)},
        "anims": [(FIRE, 4, 12, 36)],
    },
}


def home_fire():
    return C.fire_strip(16, 16, 4, seed=7)


SHEETS = {
    "home_red_tiny_common": red_tiny_room,
    "home_red_tiny_pieces": red_tiny_pieces,
    "home_fire": home_fire,
}
# Art px: the rooms at twice their room px, the fire 4 frames of 16 x 16
# room px.
SIZES = {"home_red_tiny_common": (224, 288), "home_red_tiny_pieces": (224, 288), "home_fire": (128, 32)}


def check(out_dir):
    """Each sheet is exactly what this script paints now and of its size;
    each room's entry in interior_rooms.gd names these sheets at this
    density and size, and has exactly the declared pieces (rects and sort
    lines, in order), covers and anims (sheet, frames, position); every cover
    rect lies inside its bed's piece and holds the coverlet; every anim strip
    painted here is whole square frames."""
    ok = True
    sheets = {}
    for name, build in SHEETS.items():
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        fresh = build()
        if sheet.size != fresh.size or sheet.tobytes() != fresh.tobytes():
            print("%s: the saved sheet differs from a fresh paint (rerun homes.py)" % name)
            ok = False
        if sheet.size != SIZES[name]:
            print("%s: %dx%d, not %dx%d" % ((name,) + sheet.size + SIZES[name]))
            ok = False
        sheets[name] = sheet
    for key, spec in ROOMS.items():
        entry = C.room_entry(key)
        common, pieces = spec["sheets"]
        want = {"sheet": "res://assets/interiors/%s.png" % common, "pieces_sheet": "res://assets/interiors/%s.png" % pieces}
        d = C.DENSITY
        for label, got, expect in (
                ("sheets", entry["sheets"], want),
                ("density", entry["density"], d),
                ("size", entry["size"], spec["size"]),
                ("pieces", entry["pieces"], [r for r, _p in spec["pieces"]]),
                ("covers", entry["covers"], spec["covers"]),
                ("anims", entry["anims"], spec["anims"])):
            if got != expect:
                print("%s: interior_rooms.gd %s are %s, the painted %s" % (key, label, got, expect))
                ok = False
        for name in (common, pieces):
            if sheets[name].size != (spec["size"][0] * d, spec["size"][1] * d):
                print("%s: %s is %s, not the room's size at density %d" % (key, name, sheets[name].size, d))
                ok = False
        alpha = sheets[pieces].split()[3]
        for bed, (x, y, w, h) in spec["covers"].items():
            inside = any(px <= x and py <= y and x + w <= px + pw and y + h <= py + ph
                         for (px, py, pw, ph, _s), _p in spec["pieces"])
            filled = alpha.crop(((x + 4) * d, (y + 2) * d, (x + w - 4) * d, (y + h - 8) * d)).getextrema()[0] == 255
            if not inside or not filled:
                print("%s: cover %s %s is not a covered part of a bed piece" % (key, bed, (x, y, w, h)))
                ok = False
        for path, frames, _x, _y in spec["anims"]:
            name = os.path.splitext(os.path.basename(path))[0]
            strip = sheets.get(name)
            if strip is None or strip.width != frames * strip.height:
                print("%s: anim %s is not painted here as %d square frames" % (key, path, frames))
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
