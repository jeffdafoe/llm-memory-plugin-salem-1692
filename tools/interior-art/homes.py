"""Paints salem's home interiors (LLM-969): one room per house type, keyed by
the floor plan's `art` (LLM-782), and the player's tent. Every pixel is
drawn here, no pack art.

    python tools/interior-art/homes.py
    python tools/interior-art/homes.py --out DIR   # elsewhere (prototypes)
    python tools/interior-art/homes.py --check     # a fresh paint, sizes, geometry

Writes client/assets/interiors/<art>_common.png and <art>_pieces.png for each
room here, and home_fire.png, the hearth fire (4 frames of 16x16 room px),
all at density 2. See common.py for the view, the sheets and the kit, and
homes_kit.py for the painters only these rooms use.

A 1690s New England house: limewashed plaster between oak framing, a
fieldstone hearth with a trammel and pot, a small leaded casement, wide pine
floorboards; joined oak beds under wool coverlets (indigo overshot, madder
check), a trestle table laid with a trencher, a jug and a loaf, ladder-back
rush-seat chairs, a cupboard with pewter and redware, a blanket chest; herbs
drying, a crockery shelf, a peg rail, a broom, a braided rug.

The houses differ. The red houses keep that plaster and fieldstone; the
medium and small ones have a loft reached by a ladder, a straw pallet on the
floor, Indian corn and onions hung up, a firelock over the hearth (medium) and
a flagstone floor with grain sacks (small). The blue houses have an ochre
limewash over a blue-grey painted wainscot, a brick chimney with a brass
kettle and warming pan, green and butternut coverlets, a great wheel (tiny)
or a pewter shelf, a tin lantern and the loft ladder (small). The tent is the
player's linen wedge tent seen from inside, in the painted camp's materials.

Each room's geometry is declared once here (ROOMS): its size, every piece's
rect and sort line with its painter, every bed's cover rect, every anim's
sheet, frame count and position. The painters draw from it, and --check
requires the room's entry in interior_rooms.gd to say exactly the same, so
the art and the lines that put people on and behind it cannot drift apart.
"""

import argparse
import os
import random
import re
import sys

from PIL import Image

import common as C
import homes_kit as K

# The painted rooms are drawn at twice the room's pixels (interior_rooms.gd
# entry "density": 2).
C.set_density(2)

# Every home's hearth fire: the whole anim record its entry must hold.
FIRE = {"sheet": "res://assets/interiors/home_fire.png", "frames": 4, "fps": 8.0, "x": 12, "y": 36}

# The red houses' room sheets take in the corn; the blue houses' the ochre
# limewash, the painted boards, the brick and the brass.
RED_PALETTE = C.ROOM_PALETTE + K.CORN
BLUE_PALETTE = C.ROOM_PALETTE + K.OCHRE + K.PAINT + K.BRICK + K.LIME + K.BRASS
PIECE_PALETTE = C.PIECE_PALETTE + K.GREEN + K.BUTTERNUT + K.WOOL + K.HEMP + K.CORN + C.LINEN


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
    C.hearth(room, 2, 38, 0, wall, firebox("home_red_tiny"), rnd)
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
    return C.paint_pieces(*spec["size"], [((x, y, w, h), (lambda room, px, py, pw, ph, p=painter: p(room, px, py, pw, ph, rnd)))
                                         for (x, y, w, h, _sort), painter in spec["pieces"]], C.PIECE_PALETTE)


def firebox(key):
    """The hearth's mouth: where the room's fire anim plays, 16 x 16."""
    fire = ROOMS[key]["anims"][0]
    return (fire["x"], fire["y"], 16, 16)


def shadows(room, key, strength=170):
    """Each declared piece's soft shadow on the floor."""
    K.piece_shadows(room, [(x, y, w, h) for (x, y, w, h, _s), _p in ROOMS[key]["pieces"]], strength)


def pieces_sheet(key, seed, palette=None, outline=0.35, ring=0.18):
    """The room's pieces sheet from its ROOMS declaration, the painters
    sharing one random stream in declaration order."""
    rnd = random.Random(seed)
    spec = ROOMS[key]
    return C.paint_pieces(*spec["size"], [((x, y, w, h), (lambda room, px, py, pw, ph, p=painter: p(room, px, py, pw, ph, rnd)))
                                         for (x, y, w, h, _sort), painter in spec["pieces"]],
                          palette or PIECE_PALETTE, outline=outline, ring=ring)


def back_wall(room, w, wall, rnd, posts, blue=False):
    """The back wall common to the houses: plaster (the blue houses' tinted
    with ochre over a painted wainscot), the girt under the ceiling, the
    posts."""
    if blue:
        with K.limewash(K.OCHRE):
            C.plaster_wall(room, 0, w, 0, wall - 2, rnd)
        K.wainscot(room, 0, w, 31, wall - 2, rnd)
    else:
        C.plaster_wall(room, 0, w, 0, wall - 2, rnd)
    C.timber(room, 0, 4, w, 8, 0.42, rnd)
    for px in posts:
        C.timber(room, px, 0, px + 4, wall - 2, 0.4, rnd)


def red_medium_room(seed=971):
    """home_red_medium, 112 x 112: the hearth at the left with a firelock
    over it, braided corn and a shelf, the loft hatch and its ladder, the
    window over the bed."""
    w, h, wall = 112, 112, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd)
    back_wall(room, w, wall, rnd, (40, 86))
    K.loft_hatch(room, 60, 84, 22, rnd)
    K.ladder_rails(room, 64, 16, 16, 28)
    C.casement(room, 93, 14, 106, 28)
    C.wear(room, 50, 100, 18, 10)
    C.light_pool(room, 93, wall + 1, 106, wall + 18, 70)
    C.wall_foot(room, 0, w, wall)
    C.hearth(room, 2, 38, 0, wall, firebox("home_red_medium"), rnd)
    K.musket(room, 5, 35, 24)
    for cx in (48, 56):
        K.corn_braid(room, cx, 8, rnd)
    C.shelf(room, 45, 59, 31, rnd, ("bowl", "jug", "plate"))
    C.broom(room, 42, 47, 18)
    shadows(room, "home_red_medium")
    K.lying_shadow(room, 0, 62, 32, 48, **K.PALLET)
    return C.shrink(room, RED_PALETTE)


def red_small_room(seed=973):
    """home_red_small, 96 x 144: a flagstone floor; the hearth, the window,
    onions and corn hung on the posts, the loft hatch at the right."""
    w, h, wall = 96, 144, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    K.flagstones(room, 0, wall, w, h, rnd)
    back_wall(room, w, wall, rnd, (40, 70))
    K.loft_hatch(room, 78, 96, 22, rnd)
    K.ladder_rails(room, 80, 16, 16, 28)
    C.casement(room, 49, 14, 63, 28)
    C.light_pool(room, 49, wall + 1, 63, wall + 20, 70)
    C.wall_foot(room, 0, w, wall)
    C.hearth(room, 2, 38, 0, wall, firebox("home_red_small"), rnd)
    K.corn_braid(room, 42, 10, rnd, 4)
    K.onion_string(room, 72, 11, rnd)
    C.broom(room, 66, 47, 18)
    shadows(room, "home_red_small")
    K.lying_shadow(room, 0, 60, 32, 48, **K.PALLET)
    return C.shrink(room, RED_PALETTE)


def blue_tiny_room(seed=975):
    """home_blue_tiny, 112 x 144 (the red tiny home's plan): ochre limewash
    over a blue-grey wainscot, a brick hearth with a warming pan, a great
    wheel against the wall, the window, a peg rail over the north bed; no
    rug."""
    w, h, wall = 112, 144, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd)
    back_wall(room, w, wall, rnd, (40, 104), blue=True)
    C.casement(room, 66, 11, 82, 27)
    C.wear(room, 56, 126, 16, 14)
    C.light_pool(room, 66, wall + 1, 82, wall + 24, 80)
    C.wall_foot(room, 0, w, wall)
    K.brick_hearth(room, 2, 38, 0, wall, firebox("home_blue_tiny"), rnd)
    K.warming_pan(room, 34, 14)
    C.herb_bundles(room, 47, 62, 8, rnd, 3)
    K.spinning_wheel(room, 43, 50, rnd)
    C.peg_rail(room, 86, 102, 13, rnd, ("basket", "hat"))
    shadows(room, "home_blue_tiny")
    return C.shrink(room, BLUE_PALETTE)


def blue_small_room(seed=977):
    """home_blue_small, 112 x 160: the blue tiny home one row deeper, the
    loft hatch and its ladder at the right; a shelf of pewter and redware
    and a tin lantern on the post instead of the wheel."""
    w, h, wall = 112, 160, 48
    rnd = random.Random(seed)
    room = C.Room(w, h)
    C.floorboards(room, 0, wall, w, h, rnd)
    back_wall(room, w, wall, rnd, (40, 87), blue=True)
    K.loft_hatch(room, 94, 112, 22, rnd)
    K.ladder_rails(room, 96, 16, 16, 28)
    C.casement(room, 65, 11, 81, 27)
    C.wear(room, 56, 140, 16, 14)
    C.light_pool(room, 65, wall + 1, 81, wall + 24, 80)
    C.wall_foot(room, 0, w, wall)
    K.brick_hearth(room, 2, 38, 0, wall, firebox("home_blue_small"), rnd)
    K.warming_pan(room, 34, 14)
    C.shelf(room, 46, 62, 24, rnd, ("plate", "jug", "bowl"))
    K.lantern(room, 42, 13, lit=False)
    K.corn_braid(room, 89, 12, rnd, 4)
    shadows(room, "home_blue_small")
    return C.shrink(room, BLUE_PALETTE + K.CORN)


def tent_room(seed=979):
    """tent, 96 x 112: the player's linen wedge tent from inside, its back
    end closed, the ground from the back wall (y 48) to the door; a lantern
    on a hook by the back pole, a canteen at its foot."""
    w, h = 96, 112
    rnd = random.Random(seed)
    room = C.Room(w, h)
    K.tent_shell(room, w, h, 48, (48, 4), ((18, 48), (78, 48)), ((0, 104), (96, 104)), rnd)
    K.pole_hook(room, 49.2, 20, 4.6)
    K.lantern(room, 53.8, 20.2)
    room.ellipse(52, 51, 57, 55.5, C.ramp(K.WOOD, 0.5))
    room.ellipse(52.6, 51.4, 55, 53.4, C.ramp(K.WOOD, 0.75))
    room.rect(54, 50, 55, 51.4, C.ramp(K.WOOD, 0.3))
    shadows(room, "tent", 160)
    K.lying_shadow(room, 12, 58, 32, 44, **K.BEDROLL, strength=150)
    return C.shrink(room, K.TENT_PALETTE)


def _bed(cover, pattern):
    return lambda room, x, y, w, h, rnd: C.rope_bed(room, x, y, w, h, cover, rnd, pattern)


def _chair(back_left):
    return lambda room, x, y, w, h, rnd: C.ladder_chair(room, x, y, w, h, back_left=back_left)


def _pallet(cover, stripe):
    return lambda room, x, y, w, h, rnd: K.straw_pallet(room, x, y, w, h, cover, stripe, rnd)


def _sack(open_):
    return lambda room, x, y, w, h, rnd: K.grain_sack(room, x, y, w, h, rnd, open_=open_)


def _table(room, x, y, w, h, rnd):
    C.trestle_table(room, x, y, w, h, rnd)


def _ladder(room, x, y, w, h, rnd):
    K.ladder(room, x, y, w, h)


def _cupboard(room, x, y, w, h, rnd):
    C.cupboard(room, x, y, w, h)


def _chest(room, x, y, w, h, rnd):
    C.blanket_chest(room, x, y, w, h)


# The rects and sort lines are the ones the floor plans' stations were laid
# out against, made disjoint where the pack's overlapped (the client cuts
# every piece by its rect): a table ends where the chair beside it begins, a
# ladder ends where the bed in front of it begins, and in home_red_small the
# chest is a low trunk with the two sacks standing in front of it. The tent's
# bedroll is as wide as its cover.
ROOMS = {
    "home_red_tiny": {
        "sheets": ("home_red_tiny_common", "home_red_tiny_pieces"),
        "size": (112, 144),
        # ((x, y, w, h, sort_y), painter): the rects and sort lines the
        # floor plan's stations were laid out against.
        "pieces": [
            ((80, 44, 32, 44, 71), _bed(C.INDIGO, "overshot")),
            ((80, 98, 32, 44, 125), _bed(C.MADDER, "check")),
            # The table ends where the east chair begins (the pack's rects
            # overlapped by 3 px, which would cut tabletop into the chair).
            ((32, 70, 29, 25, 88), lambda room, x, y, w, h, rnd: C.trestle_table(room, x, y, w, h, rnd)),
            ((16, 68, 14, 27, 89), _chair(True)),
            ((61, 68, 14, 27, 89), _chair(False)),
            ((0, 96, 16, 48, 143), lambda room, x, y, w, h, rnd: C.cupboard(room, x, y, w, h)),
            ((16, 128, 32, 16, 143), lambda room, x, y, w, h, rnd: C.blanket_chest(room, x, y, w, h)),
        ],
        "covers": {"bed_north": (80, 56, 32, 32), "bed_south": (80, 110, 32, 32)},
        "anims": [FIRE],
    },
    "home_red_medium": {
        "sheets": ("home_red_medium_common", "home_red_medium_pieces"),
        "size": (112, 112),
        "pieces": [
            ((64, 28, 16, 37, 65), _ladder),
            ((80, 44, 32, 44, 71), _bed(C.MADDER, "overshot")),
            ((0, 62, 32, 48, 88), _pallet(C.INDIGO, C.MADDER)),
            ((48, 70, 32, 25, 88), _table),
            ((32, 68, 14, 27, 89), _chair(True)),
            ((80, 96, 32, 16, 111), _chest),
        ],
        "covers": {"bed": (80, 56, 32, 32), "pallet": (0, 74, 32, 36)},
        "anims": [FIRE],
    },
    "home_red_small": {
        "sheets": ("home_red_small_common", "home_red_small_pieces"),
        "size": (96, 144),
        "pieces": [
            ((80, 28, 16, 34, 65), _ladder),
            ((64, 62, 32, 44, 89), _bed(C.INDIGO, "check")),
            ((0, 60, 32, 48, 86), _pallet(C.MADDER, C.INDIGO)),
            ((0, 114, 29, 25, 132), _table),
            ((29, 112, 14, 27, 133), _chair(False)),
            ((64, 112, 32, 10, 127), _chest),
            ((68, 122, 12, 17, 139), _sack(False)),
            ((80, 122, 16, 21, 143), _sack(True)),
        ],
        "covers": {"bed": (64, 74, 32, 32), "pallet": (0, 72, 32, 36)},
        "anims": [FIRE],
    },
    "home_blue_tiny": {
        "sheets": ("home_blue_tiny_common", "home_blue_tiny_pieces"),
        "size": (112, 144),
        "pieces": [
            ((80, 44, 32, 44, 71), _bed(K.GREEN, "overshot")),
            ((80, 98, 32, 44, 125), _bed(K.BUTTERNUT, "check")),
            ((32, 70, 29, 25, 88), _table),
            ((16, 68, 14, 27, 89), _chair(True)),
            ((61, 68, 14, 27, 89), _chair(False)),
            ((0, 96, 16, 48, 143), _cupboard),
            ((16, 128, 32, 16, 143), _chest),
        ],
        "covers": {"bed_north": (80, 56, 32, 32), "bed_south": (80, 110, 32, 32)},
        "anims": [FIRE],
    },
    "home_blue_small": {
        "sheets": ("home_blue_small_common", "home_blue_small_pieces"),
        "size": (112, 160),
        "pieces": [
            ((96, 28, 16, 32, 65), _ladder),
            ((80, 60, 32, 44, 87), _bed(K.BUTTERNUT, "overshot")),
            ((80, 114, 32, 44, 141), _bed(K.GREEN, "check")),
            ((32, 86, 29, 25, 104), _table),
            ((16, 84, 14, 27, 105), _chair(True)),
            ((61, 84, 14, 27, 105), _chair(False)),
            ((0, 112, 16, 48, 159), _cupboard),
            ((16, 144, 32, 16, 159), _chest),
        ],
        "covers": {"bed_north": (80, 72, 32, 32), "bed_south": (80, 126, 32, 32)},
        "anims": [FIRE],
    },
    "tent": {
        "sheets": ("tent_common", "tent_pieces"),
        "size": (96, 112),
        "pieces": [
            ((12, 58, 32, 44, 89), lambda room, x, y, w, h, rnd: K.bedroll(room, x, y, w, h, rnd)),
            ((66, 84, 16, 16, 99), lambda room, x, y, w, h, rnd: K.snapsack(room, x, y, w, h)),
        ],
        "covers": {"bedroll": (12, 70, 32, 32)},
        "anims": [],
    },
}


def home_fire():
    return C.fire_strip(16, 16, 4, seed=7)


SHEETS = {
    "home_red_tiny_common": red_tiny_room,
    "home_red_tiny_pieces": red_tiny_pieces,
    "home_red_medium_common": red_medium_room,
    "home_red_medium_pieces": lambda: pieces_sheet("home_red_medium", 972),
    "home_red_small_common": red_small_room,
    "home_red_small_pieces": lambda: pieces_sheet("home_red_small", 974),
    "home_blue_tiny_common": blue_tiny_room,
    "home_blue_tiny_pieces": lambda: pieces_sheet("home_blue_tiny", 976),
    "home_blue_small_common": blue_small_room,
    "home_blue_small_pieces": lambda: pieces_sheet("home_blue_small", 978),
    "tent_common": tent_room,
    "tent_pieces": lambda: pieces_sheet("tent", 980, K.TENT_PIECE_PALETTE, outline=0.3, ring=0.15),
    "home_fire": home_fire,
}
# Art px: each room at twice its room px, the fire 4 frames of 16 x 16 room px.
SIZES = {"home_fire": (128, 32)}
for _key, _spec in ROOMS.items():
    for _sheet in _spec["sheets"]:
        SIZES[_sheet] = (_spec["size"][0] * 2, _spec["size"][1] * 2)


def entry_anims(key, text=None):
    """The room's anim records in interior_rooms.gd (or in text), each as a
    dict of every field it has: a quoted value is a string, a number an int
    or a float, any other value listed under "unread". Unlike
    common.room_entry's anims, nothing is dropped, so an fps, a sort_y or
    any field the declaration does not have is seen."""
    text = text if text is not None else open(C.ROOMS_GD, encoding="utf-8").read()
    m = re.search(r'\n    "%s": \{(.*?)\n    \},' % re.escape(key), text, re.S)
    if not m:
        raise ValueError("no room %s" % key)
    body = m.group(1)
    if '"anims"' not in body:
        return []
    block = body.split('"anims"', 1)[1]
    block = block[:block.index("]")]
    records = []
    for rec in re.findall(r"\{([^{}]*)\}", block):
        fields = {}
        for k, v in re.findall(r'"(\w+)":\s*("[^"]*"|-?\d+(?:\.\d+)?)', rec):
            fields[k] = v[1:-1] if v.startswith('"') else (float(v) if "." in v else int(v))
        # A field whose value is not a quoted string or a number (true, a
        # Vector2i, ...) is kept as unread, so it fails the comparison
        # instead of vanishing.
        unread = sorted(set(re.findall(r'"(\w+)":', rec)) - set(fields))
        if unread:
            fields["unread"] = unread
        records.append(fields)
    return records


def check(out_dir):
    """Each sheet is exactly what this script paints now and of its size;
    each room's entry in interior_rooms.gd names these sheets at this
    density and size, and has exactly the declared pieces (rects and sort
    lines, in order), covers and anims (sheet, frames, position); every cover
    rect lies inside its bed's piece and holds the coverlet; every anim strip
    painted here is whole square frames."""
    ok = True
    # The kit's own guards (common.selftest): a removed guard would leave
    # every sheet unchanged, so the sheets alone cannot show it.
    for f in C.selftest():
        print("common selftest: " + f)
        ok = False
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
                ("anims", entry_anims(key), spec["anims"])):
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
        for anim in spec["anims"]:
            name = os.path.splitext(os.path.basename(anim["sheet"]))[0]
            strip = sheets.get(name)
            if strip is None or strip.width != anim["frames"] * strip.height:
                print("%s: anim %s is not painted here as %d square frames" % (key, anim["sheet"], anim["frames"]))
                ok = False
    # The anim comparison must see every field: an entry whose fire plays
    # at another fps, or gains a sort line, is refused.
    text = open(C.ROOMS_GD, encoding="utf-8").read()
    start = text.index('\n    "home_red_tiny": {')
    end = text.index("\n    },", start)
    entry = text[start:end]
    for label, changed in (("fps 9.0", entry.replace('"fps": 8.0', '"fps": 9.0')),
                           ("a sort_y", entry.replace('"y": 36}', '"y": 36, "sort_y": 40}')),
                           ("an extra true", entry.replace('"y": 36}', '"y": 36, "unexpected": true}'))):
        if changed == entry or entry_anims("home_red_tiny", text[:start] + changed + text[end:]) == ROOMS["home_red_tiny"]["anims"]:
            print("the anim check would pass an entry whose fire has %s" % label)
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
