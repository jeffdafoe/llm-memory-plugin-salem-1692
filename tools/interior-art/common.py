"""Shared painting kit for salem's room interiors (LLM-969). Every pixel is
drawn here, no pack art.

A room's geometry is in room pixels, one 16 px square per map tile: every
rect, sort line, cover, animation offset and goods cell in client/scripts/
interior_rooms.gd and stall_scenes.gd, and the engine's floor plans. The art
is painted at DENSITY art pixels per room pixel (set_density; 2 for the
painted rooms, twice the detail in the same room), on a canvas S = 4 x
DENSITY canvas px per room px, then shrink() boxes it down to the art size
and snaps every pixel to the room's palette. Painters always work in room
pixels; the density only changes how finely they come out.

The view is the rooms' own: the back wall seen head-on across the top of the
room, the floor and the furniture seen from above at a slant, light from the
upper left (as all salem art).

Each room writes three kinds of sheet to client/assets/interiors/:
  <room>_common.png   the room without its furniture: walls, floor, the
                      things on them that nobody walks behind, the shadows
                      the furniture casts on the floor
  <room>_pieces.png   every piece of furniture where it stands in the room
                      (the room's size, transparent between pieces); the
                      client cuts each piece and each bed's cover out of it
  <anim>.png          an animation strip, frames side by side

The kit: palettes (1690s New England materials), the canvas (Room), wall and
floor painters, set dressing (things on the walls and floor), furniture
painters that draw a piece into its rect, the hearth and its fire strip, and
the shrink. A room module (homes.py, ...) composes them and lists its pieces'
rects, which must equal the room's entry in interior_rooms.gd (room_entry).
"""

import math
import os
import random
import re
import sys

from PIL import Image, ImageChops, ImageDraw, ImageFilter

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))
OUT_DIR = os.path.join(ROOT, "client", "assets", "interiors")
ROOMS_GD = os.path.join(ROOT, "client", "scripts", "interior_rooms.gd")
sys.path.insert(0, os.path.join(ROOT, "tools", "tree-art"))
import build as B  # noqa: E402  (the tree art's colour helpers: rgb, ramp, mix, nearest)

DENSITY = 1                 # art px per room px
S = 4 * DENSITY             # canvas px per room px


def set_density(d):
    """Paint at d art px per room px from here on."""
    global DENSITY, S
    DENSITY, S = d, 4 * d


# ---------------------------------------------------------------- palettes
# Lime plaster, whitewashed, warm, smoked darker toward the ceiling and
# dirtier low down.
PLASTER = ["5e5444", "786c58", "92866e", "aa9e84", "bfb49a", "d2c8ae", "e2dac2"]
# Oak framing timbers, dark with age and smoke.
TIMBER = ["1c140c", "2c2014", "3e2e1e", "523e28", "684f34", "7e6242", "947652"]
# Wide pine floorboards, honey-brown, worn paler where people walk.
FLOOR = ["32221a", "463020", "5c4028", "725232", "88643c", "9c7848", "b08c58"]
# Fieldstone, grey-brown; mortar between.
STONE = ["2a2622", "3e3830", "544c42", "6c6254", "847866", "9c907a", "b4a88e", "c8bea4"]
MORTAR = ["38302a", "4e4438"]
SOOT = ["0e0c0a", "1a1612", "28221c"]
# Furniture: joined oak and pine, oiled.
OAK = ["24180c", "382614", "4e3620", "664a2c", "7e5e38", "967446", "ae8a56", "c4a068"]
# Wool coverlets dyed with madder, indigo; undyed linen; redware; pewter.
MADDER = ["3e1610", "5a2018", "782e22", "963e2c", "b0543a"]
INDIGO = ["18202e", "243246", "34465e", "4a5e7a", "647a96"]
LINEN = ["6e6650", "928a70", "b2aa8c", "ccc4a6", "e2dcc0"]
REDWARE = ["4a2012", "6e3018", "924422", "b05a30", "c87a48"]
PEWTER = ["3a3c40", "56585c", "74767a", "929498", "b0b2b4"]
IRON = ["141414", "26262a", "3a3a40", "54545c", "70707a"]
HERB = ["2c3418", "424c22", "5a6630", "76804a", "929a62"]
STRAW = ["5a4a24", "7a6630", "9a8440", "b8a058", "d0bc78"]
# Leaded casement glass, greenish, and the daylight beyond it.
GLASS = ["2e3a36", "46564e", "6a7e72", "9aae9e", "c8d6c4"]
DAYLIGHT = ["d8d0ae", "ece4c2", "f8f2d8"]
FIRE = ["5a1404", "8e2608", "c2440e", "e86e1a", "f6a032", "fcd468", "fff4c4"]

ROOM_PALETTE = (PLASTER + TIMBER + FLOOR + STONE + MORTAR + SOOT + GLASS + DAYLIGHT + OAK + LINEN + REDWARE
                + PEWTER + IRON + HERB + STRAW + MADDER + INDIGO)
PIECE_PALETTE = OAK + MADDER + INDIGO + LINEN + IRON + TIMBER + REDWARE + PEWTER + STRAW
FIRE_PALETTE = FIRE + SOOT + STONE + OAK


def ramp(cols, t):
    return B.ramp(cols, t)


# ---------------------------------------------------------------- canvas

class Room:
    """A canvas the size of a room (w x h room px), painted at S, with a
    shade layer for cast shadows (on the floor sheet)."""

    def __init__(self, w, h):
        self.w, self.h = w, h
        self.lay = Image.new("RGBA", (w * S, h * S), (0, 0, 0, 0))
        self.d = ImageDraw.Draw(self.lay)
        self.shade = Image.new("L", (w * S, h * S), 0)
        self.sd = ImageDraw.Draw(self.shade)

    def rect(self, x0, y0, x1, y1, col):
        # Canvas px, inclusive; a sliver thinner than a canvas px still
        # draws one row or column rather than inverting.
        a, b = int(round(x0 * S)), int(round(y0 * S))
        self.d.rectangle((a, b, max(a, int(round(x1 * S)) - 1), max(b, int(round(y1 * S)) - 1)), fill=col)

    def poly(self, pts, col):
        self.d.polygon([(x * S, y * S) for x, y in pts], fill=col)

    def line(self, pts, col, w=1.0):
        self.d.line([(x * S, y * S) for x, y in pts], fill=col, width=max(1, int(w * S)))

    def ellipse(self, x0, y0, x1, y1, col):
        a, b = x0 * S, y0 * S
        self.d.ellipse((a, b, max(a, x1 * S - 1), max(b, y1 * S - 1)), fill=col)

    def shadow(self, x0, y0, x1, y1, strength=150):
        """A soft cast shadow on the floor (an oval), folded into the floor
        sheet by shrink()."""
        self.sd.ellipse((x0 * S, y0 * S, x1 * S - 1, y1 * S - 1), fill=strength)

    def speckle(self, x0, y0, x1, y1, n, cols, tones, rnd, size=(0.25, 0.6)):
        """n flecks of cols at tones, inside the rect: texture (plaster grit,
        stone grain, wear)."""
        for _ in range(int(n)):
            x, y = rnd.uniform(x0, x1), rnd.uniform(y0, y1)
            r = rnd.uniform(*size)
            self.rect(x, y, x + r, y + r * rnd.uniform(0.6, 1.4), ramp(cols, rnd.choice(tones)))


def shrink(room, palette, outline=0.0, shadow_dark=0.45, ring=0.0):
    """The canvas to art size (room px x DENSITY): box-average, snap to the
    palette. With outline, a covered pixel whose lower neighbour is uncovered
    is darkened that much, and with ring, one with any other uncovered
    neighbour by ring, so the pieces stand apart from the floor; the shade
    layer darkens the covered floor under it by up to shadow_dark."""
    w, h = room.w * DENSITY, room.h * DENSITY
    small = room.lay.resize((w, h), Image.BOX)
    shade = room.shade.resize((w, h), Image.BOX)
    sp, hp = small.load(), shade.load()
    pal = [B.rgb(c)[:3] for c in palette]
    out = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    op = out.load()

    def bare(nx, ny):
        return not (0 <= nx < w and 0 <= ny < h) or sp[nx, ny][3] < 128

    for y in range(h):
        for x in range(w):
            r, g, b, a = sp[x, y]
            if a < 128:
                continue
            # Box averaging with transparent pixels darkens the colour by
            # the alpha it took in: undo it before snapping.
            k = 255.0 / a
            c = (min(255, int(r * k)), min(255, int(g * k)), min(255, int(b * k)))
            if hp[x, y]:
                f = 1.0 - shadow_dark * hp[x, y] / 255.0
                c = (int(c[0] * f), int(c[1] * f), int(c[2] * f))
            c = B.nearest(c, pal)
            if outline and bare(x, y + 1):
                c = B.nearest(tuple(int(v * (1.0 - outline)) for v in c), pal)
            elif ring and (bare(x - 1, y) or bare(x + 1, y) or bare(x, y - 1)):
                c = B.nearest(tuple(int(v * (1.0 - ring)) for v in c), pal)
            op[x, y] = c + (255,)
    return out


PIECE_PAD = 8                # room px of margin round a piece while it is painted


def paint_pieces(w, h, pieces, palette, outline=0.35, ring=0.18):
    """The pieces sheet (w x h room px). pieces: [((x, y, pw, ph), painter)],
    painter(room, x, y, pw, ph) drawing the piece into that rect in room px.

    The client cuts every piece (and every bed's cover) out of this sheet by
    its rect, so:
      - no two rects may overlap: the client would cut both sprites from the
        same pixels and one piece's art would ride in the other's sprite;
      - a piece must paint nothing outside its rect, which would be clipped;
      - a piece must paint something.
    Each piece is painted alone on a canvas PIECE_PAD room px bigger than the
    sheet on every side (so a stroke that runs off the sheet's edge is still
    seen), shrunk, checked, and pasted into the sheet cropped to its rect.
    Any breach raises ValueError naming the piece."""
    rects = [r for r, _p in pieces]
    for i, (ax, ay, aw, ah) in enumerate(rects):
        for bx, by, bw, bh in rects[i + 1:]:
            if ax < bx + bw and bx < ax + aw and ay < by + bh and by < ay + ah:
                raise ValueError("pieces %s and %s overlap" % ((ax, ay, aw, ah), (bx, by, bw, bh)))
    pad = PIECE_PAD
    sheet = Image.new("RGBA", (w * DENSITY, h * DENSITY), (0, 0, 0, 0))
    for (x, y, pw, ph), painter in pieces:
        room = Room(w + 2 * pad, h + 2 * pad)
        painter(room, x + pad, y + pad, pw, ph)
        art = shrink(room, palette, outline=outline, ring=ring)
        box = ((x + pad) * DENSITY, (y + pad) * DENSITY, (x + pad + pw) * DENSITY, (y + pad + ph) * DENSITY)
        outside = art.copy()
        outside.paste((0, 0, 0, 0), box)
        if outside.getbbox() is not None:
            b = outside.getbbox()
            raise ValueError("piece %s paints outside its rect, at %s (art px, sheet origin)"
                             % ((x, y, pw, ph), tuple(v - pad * DENSITY for v in b)))
        cut = art.crop(box)
        if cut.getbbox() is None:
            raise ValueError("piece %s paints nothing" % ((x, y, pw, ph),))
        sheet.alpha_composite(cut, (x * DENSITY, y * DENSITY))
    return sheet


# ---------------------------------------------------------------- walls and floors

def plaster_wall(room, x0, x1, top, bottom, rnd):
    """Limewashed plaster from top to bottom: smoked a little darker under
    the ceiling, dirtier toward the floor, grit and trowel marks, a few
    patches and cracks."""
    for y in range(int(top * 4), int(bottom * 4)):
        yy = y / 4
        f = (yy - top) / max(1, bottom - top)
        t = 0.66 + 0.08 * math.sin(f * math.pi) - 0.12 * f * f - 0.08 * (1 - f) ** 4
        room.rect(x0, yy, x1, yy + 0.25, ramp(PLASTER, t))
    # Trowel strokes: broad faint sweeps.
    for _ in range(int((x1 - x0) * (bottom - top) / 60)):
        px, py = rnd.uniform(x0, x1 - 6), rnd.uniform(top + 1, bottom - 2)
        room.ellipse(px, py, px + rnd.uniform(5, 11), py + rnd.uniform(1.0, 2.2), ramp(PLASTER, rnd.uniform(0.58, 0.78)))
    room.speckle(x0, top, x1, bottom, (x1 - x0) * (bottom - top) / 9, PLASTER, (0.35, 0.45, 0.8), rnd)
    # Where the limewash has worn off: a patch of the brown daub under it.
    for _ in range(max(1, int((x1 - x0) / 70))):
        px, py = rnd.uniform(x0 + 4, x1 - 10), rnd.uniform(top + (bottom - top) * 0.5, bottom - 5)
        room.ellipse(px, py, px + rnd.uniform(4, 7), py + rnd.uniform(2, 3.5), ramp(PLASTER, 0.22))
        room.ellipse(px + 0.6, py + 0.5, px + rnd.uniform(3, 5), py + 2.0, ramp(PLASTER, 0.32))
    for _ in range(max(1, int((x1 - x0) / 45))):
        cx, cy = rnd.uniform(x0 + 4, x1 - 4), rnd.uniform(top + 4, bottom - 8)
        pts = [(cx, cy)]
        for _k in range(4):
            cx, cy = cx + rnd.uniform(-1.6, 1.6), cy + rnd.uniform(0.8, 2.2)
            pts.append((cx, cy))
        room.line(pts, ramp(PLASTER, 0.3), 0.25)


def timber(room, x0, y0, x1, y1, tone=0.45, rnd=None):
    """A hewn oak timber (a post, a girt, a beam): lit along its top and left
    edges, shaded on its right and lower edges, adze marks, checks (the long
    cracks in old oak), a peg head at its ends."""
    room.rect(x0, y0, x1, y1, ramp(TIMBER, tone))
    horizontal = (x1 - x0) > (y1 - y0)
    if rnd is not None:
        # Grain along the timber.
        for _ in range(int(max(x1 - x0, y1 - y0) / 3)):
            if horizontal:
                gy, gx = rnd.uniform(y0 + 0.6, y1 - 0.6), rnd.uniform(x0, x1 - 4)
                room.rect(gx, gy, gx + rnd.uniform(3, 10), gy + 0.25, ramp(TIMBER, tone + rnd.choice((-0.12, 0.1))))
            else:
                gx, gy = rnd.uniform(x0 + 0.6, x1 - 0.6), rnd.uniform(y0, y1 - 4)
                room.rect(gx, gy, gx + 0.25, gy + rnd.uniform(3, 10), ramp(TIMBER, tone + rnd.choice((-0.12, 0.1))))
        # Adze marks: short scoops across the grain.
        for _ in range(int(max(x1 - x0, y1 - y0) / 7)):
            if horizontal:
                ax = rnd.uniform(x0 + 1, x1 - 2)
                room.line([(ax, y0 + 0.8), (ax + 0.9, y1 - 0.8)], ramp(TIMBER, tone - 0.15), 0.3)
            else:
                ay = rnd.uniform(y0 + 1, y1 - 2)
                room.line([(x0 + 0.8, ay), (x1 - 0.8, ay + 0.9)], ramp(TIMBER, tone - 0.15), 0.3)
        # A long check.
        if horizontal:
            cy, cx = (y0 + y1) / 2 + rnd.uniform(-0.6, 0.6), rnd.uniform(x0, x1 - 20)
            room.rect(cx, cy, cx + rnd.uniform(10, 20), cy + 0.3, ramp(TIMBER, 0.05))
        else:
            cx, cy = (x0 + x1) / 2 + rnd.uniform(-0.4, 0.4), rnd.uniform(y0, max(y0 + 1, y1 - 18))
            room.rect(cx, cy, cx + 0.3, cy + rnd.uniform(8, 16), ramp(TIMBER, 0.05))
    room.rect(x0, y0, x1, min(y1, y0 + 0.7), ramp(TIMBER, tone + 0.38))
    room.rect(x0, y0, min(x1, x0 + 0.7), y1, ramp(TIMBER, tone + 0.3))
    room.rect(x0, max(y0, y1 - 0.7), x1, y1, ramp(TIMBER, tone - 0.3))
    room.rect(max(x0, x1 - 0.7), y0, x1, y1, ramp(TIMBER, tone - 0.24))


def floorboards(room, x0, y0, x1, y1, rnd, board=6.5):
    """Wide pine boards running left to right, joints staggered, each board
    its own shade: grain lines, knots, a few nail heads, wear in the middle
    of the room."""
    y = y0
    while y < y1:
        h = min(board + rnd.uniform(-0.8, 0.8), y1 - y)
        x = x0 - rnd.uniform(0, 40)
        while x < x1:
            length = rnd.uniform(36, 80)
            a, b = max(x0, x), min(x1, x + length)
            if b - a > 0.5:
                t = 0.5 + rnd.uniform(-0.09, 0.09)
                room.rect(a, y, b, y + h, ramp(FLOOR, t))
                # Grain: long thin streaks, a little wavy.
                for _ in range(int((b - a) * h / 14)):
                    gy = y + rnd.uniform(0.6, h - 0.6)
                    gx = rnd.uniform(a, b - 4)
                    gl = rnd.uniform(4, 16)
                    room.line([(gx, gy), (gx + gl / 2, gy + rnd.uniform(-0.35, 0.35)), (min(b, gx + gl), gy)],
                              ramp(FLOOR, t + rnd.choice((-0.1, -0.06, 0.08))), 0.25)
                # A knot now and then, the grain bent round it.
                if rnd.random() < 0.35:
                    kx, ky = rnd.uniform(a + 3, max(a + 3.1, b - 3)), y + rnd.uniform(1.6, h - 1.6)
                    room.ellipse(kx - 1.0, ky - 0.6, kx + 1.0, ky + 0.6, ramp(FLOOR, t - 0.2))
                    room.ellipse(kx - 0.5, ky - 0.3, kx + 0.4, ky + 0.25, ramp(FLOOR, t - 0.32))
                    room.line([(kx - 3.5, ky - 1.2), (kx, ky - 1.0), (kx + 3.5, ky - 1.2)], ramp(FLOOR, t - 0.1), 0.25)
                # The lit upper edge of the board, faint.
                room.rect(a, y, b, y + 0.3, ramp(FLOOR, t + 0.1))
                # The butt joint, with its nails.
                if b < x1:
                    room.rect(b - 0.4, y, b, y + h, ramp(FLOOR, 0.2))
                    for ny in (y + 1.2, y + h - 1.6):
                        room.rect(b - 1.5, ny, b - 0.9, ny + 0.6, ramp(IRON, 0.3))
            x += length
        # The seam between boards.
        room.rect(x0, y + h - 0.4, x1, y + h, ramp(FLOOR, 0.16))
        y += h


def wear(room, cx, cy, rx, ry, strength=40):
    """Floor worn paler and smoother where people walk: a soft patch."""
    glow = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(glow).ellipse(((cx - rx) * S, (cy - ry) * S, (cx + rx) * S, (cy + ry) * S), fill=strength)
    glow = glow.filter(ImageFilter.GaussianBlur(4 * S))
    room.lay.paste(Image.new("RGBA", room.lay.size, B.rgb(FLOOR[-2])), (0, 0), glow)


def wall_foot(room, x0, x1, y):
    """The sill timber where the back wall meets the floor, and the shadow
    line under it."""
    room.rect(x0, y - 2.2, x1, y, ramp(TIMBER, 0.42))
    room.rect(x0, y - 2.2, x1, y - 1.6, ramp(TIMBER, 0.72))
    room.rect(x0, y, x1, y + 1.2, ramp(FLOOR, 0.06))


def casement(room, x0, y0, x1, y1):
    """A small casement window with diamond leading in an oak frame;
    daylight through it, brighter in the upper panes."""
    timber(room, x0 - 1.6, y0 - 1.6, x1 + 1.6, y1 + 1.6, 0.42)
    for y in range(int(y0 * 4), int(y1 * 4)):
        yy = y / 4
        room.rect(x0, yy, x1, yy + 0.25, ramp(DAYLIGHT, 0.9 - 0.7 * (yy - y0) / (y1 - y0)))
    # The diamond quarrels: leading at 45 degrees both ways.
    step = 2.6
    k = x0 - (y1 - y0)
    while k < x1:
        room.line([(max(x0, k), y0 + max(0, x0 - k)), (min(x1, k + (y1 - y0)), y0 + min(y1 - y0, x1 - k))],
                  ramp(GLASS, 0.15), 0.3)
        room.line([(max(x0, k), y1 - max(0, x0 - k)), (min(x1, k + (y1 - y0)), y1 - min(y1 - y0, x1 - k))],
                  ramp(GLASS, 0.15), 0.3)
        k += step
    # The mullion, the sill, a candlestick on it.
    timber(room, (x0 + x1) / 2 - 0.7, y0, (x0 + x1) / 2 + 0.7, y1, 0.42)
    room.rect(x0 - 2.4, y1 + 1.0, x1 + 2.4, y1 + 2.6, ramp(TIMBER, 0.55))
    room.rect(x0 - 2.4, y1 + 1.0, x1 + 2.4, y1 + 1.5, ramp(TIMBER, 0.85))
    room.rect(x0 - 2.4, y1 + 2.2, x1 + 2.4, y1 + 2.6, ramp(TIMBER, 0.2))
    cx = x1 - 3
    room.rect(cx - 1.4, y1 + 0.2, cx + 1.4, y1 + 1.0, ramp(PEWTER, 0.6))
    room.rect(cx - 0.5, y1 - 2.6, cx + 0.5, y1 + 0.2, ramp(PEWTER, 0.5))
    room.rect(cx - 0.45, y1 - 6.0, cx + 0.45, y1 - 2.6, ramp(LINEN, 0.9))


def light_pool(room, x0, y0, x1, y1, strength=70):
    """Daylight from a window lying on the floor: a pale patch angled down
    and right, soft at its edges, fading toward its far end."""
    glow = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(glow).polygon([(x0 * S, y0 * S), (x1 * S, y0 * S), ((x1 + 10) * S, y1 * S), ((x0 + 10) * S, y1 * S)],
                                 fill=strength)
    fade = Image.new("L", room.lay.size, 0)
    fd = ImageDraw.Draw(fade)
    for i in range(16):
        t = i / 16
        fd.rectangle((0, (y0 + (y1 - y0) * t) * S, room.lay.size[0], (y0 + (y1 - y0) * (t + 1 / 16)) * S),
                     fill=int(255 * (1 - 0.7 * t)))
    glow = ImageChops.multiply(glow.filter(ImageFilter.GaussianBlur(2.5 * S)), fade)
    room.lay.paste(Image.new("RGBA", room.lay.size, B.rgb(DAYLIGHT[0])), (0, 0), glow)


# ---------------------------------------------------------------- set dressing (on the room sheet)

def herb_bundles(room, x0, x1, y, rnd, n=4):
    """Bunches of herbs hung head-down to dry from a girt at y: stems tied
    with twine, leaves fanning out below."""
    for i in range(n):
        cx = x0 + (i + 0.5) * (x1 - x0) / n + rnd.uniform(-1, 1)
        drop = rnd.uniform(3.0, 5.0)
        room.line([(cx, y), (cx, y + drop)], ramp(STRAW, 0.4), 0.3)
        room.rect(cx - 0.7, y + drop, cx + 0.7, y + drop + 1.0, ramp(STRAW, 0.6))
        tone = rnd.uniform(0.3, 0.7)
        for k in range(9):
            a = math.radians(rnd.uniform(-35, 35))
            ln = rnd.uniform(3.5, 7.0)
            room.line([(cx, y + drop + 0.8), (cx + math.sin(a) * ln, y + drop + 0.8 + math.cos(a) * ln)],
                      ramp(HERB, tone + rnd.uniform(-0.2, 0.2)), 0.5)
        room.ellipse(cx - 1.4, y + drop + 5.5, cx + 1.4, y + drop + 8.0, ramp(HERB, tone - 0.15))


def peg_rail(room, x0, x1, y, rnd, things=("hat", "cloak")):
    """A board with turned pegs, and what hangs on them: a felt hat, a wool
    cloak, a basket."""
    room.rect(x0, y, x1, y + 1.6, ramp(TIMBER, 0.55))
    room.rect(x0, y, x1, y + 0.5, ramp(TIMBER, 0.85))
    pegs = [x0 + (i + 0.5) * (x1 - x0) / len(things) for i in range(len(things))]
    for px, thing in zip(pegs, things):
        room.rect(px - 0.5, y + 0.4, px + 0.5, y + 1.8, ramp(OAK, 0.7))
        if thing == "hat":
            room.ellipse(px - 4.0, y + 1.8, px + 4.0, y + 3.4, ramp(IRON, 0.25))
            room.rect(px - 2.2, y - 1.6, px + 2.2, y + 2.4, ramp(IRON, 0.3))
            room.rect(px - 2.2, y - 1.6, px + 2.2, y - 1.0, ramp(IRON, 0.55))
            room.rect(px - 2.2, y + 1.2, px + 2.2, y + 1.8, ramp(STRAW, 0.3))
        elif thing == "cloak":
            room.poly([(px - 1.4, y + 1.4), (px + 1.4, y + 1.4), (px + 4.4, y + 17), (px - 4.6, y + 17)], ramp(MADDER, 0.35))
            room.poly([(px - 1.4, y + 1.4), (px - 0.2, y + 1.4), (px - 2.4, y + 17), (px - 4.6, y + 17)], ramp(MADDER, 0.55))
            for fx in (px - 1.8, px + 1.0):
                room.line([(fx, y + 4), (fx + rnd.uniform(-0.6, 0.6), y + 16.5)], ramp(MADDER, 0.15), 0.35)
        elif thing == "basket":
            room.ellipse(px - 4, y + 2.5, px + 4, y + 9, ramp(STRAW, 0.45))
            room.line([(px - 3, y + 4), (px, y + 1.4), (px + 3, y + 4)], ramp(STRAW, 0.6), 0.5)
            for k in range(4):
                room.rect(px - 3.5, y + 4.4 + k * 1.2, px + 3.5, y + 4.7 + k * 1.2, ramp(STRAW, 0.25))


def shelf(room, x0, x1, y, rnd, wares=("jug", "plate", "bowl", "plate", "jug")):
    """A plank shelf on two brackets and the crockery on it: redware jugs
    and bowls, pewter plates stood on edge."""
    room.rect(x0, y, x1, y + 1.3, ramp(OAK, 0.7))
    room.rect(x0, y, x1, y + 0.4, ramp(OAK, 0.92))
    room.rect(x0, y + 1.3, x1, y + 1.7, ramp(PLASTER, 0.25))
    for bx in (x0 + 2, x1 - 3):
        room.poly([(bx, y + 1.3), (bx + 1.2, y + 1.3), (bx + 1.2, y + 4.5)], ramp(OAK, 0.5))
    step = (x1 - x0) / len(wares)
    for i, ware in enumerate(wares):
        cx = x0 + (i + 0.5) * step
        if ware == "jug":
            room.ellipse(cx - 1.9, y - 4.6, cx + 1.9, y + 0.2, ramp(REDWARE, 0.5))
            room.rect(cx - 1.0, y - 6.0, cx + 1.0, y - 4.2, ramp(REDWARE, 0.45))
            room.ellipse(cx - 1.4, y - 4.2, cx - 0.2, y - 2.0, ramp(REDWARE, 0.85))
            room.line([(cx + 1.6, y - 5.0), (cx + 2.8, y - 3.6), (cx + 1.8, y - 1.6)], ramp(REDWARE, 0.35), 0.45)
        elif ware == "bowl":
            room.ellipse(cx - 2.6, y - 2.6, cx + 2.6, y + 0.2, ramp(REDWARE, 0.55))
            room.ellipse(cx - 2.3, y - 2.6, cx + 2.3, y - 1.4, ramp(REDWARE, 0.25))
        else:
            room.ellipse(cx - 2.8, y - 5.6, cx + 2.8, y + 0.1, ramp(PEWTER, 0.55))
            room.ellipse(cx - 1.8, y - 4.6, cx + 1.8, y - 0.9, ramp(PEWTER, 0.42))
            room.ellipse(cx - 2.2, y - 5.2, cx - 0.4, y - 3.4, ramp(PEWTER, 0.95))


def broom(room, x, y, h):
    """A besom broom leaning against the wall, its foot at (x, y)."""
    room.line([(x + 3.0, y - h), (x + 0.6, y - 5.5)], ramp(OAK, 0.6), 0.6)
    room.poly([(x - 1.0, y - 6.0), (x + 2.0, y - 6.4), (x + 3.0, y), (x - 2.4, y)], ramp(STRAW, 0.5))
    for k in range(5):
        bx = x - 1.6 + k * 1.1
        room.line([(bx + 0.6, y - 5.8), (bx, y - 0.2)], ramp(STRAW, 0.3 if k % 2 else 0.75), 0.25)
    room.rect(x - 1.0, y - 6.0, x + 2.2, y - 5.2, ramp(STRAW, 0.15))


def rag_rug(room, x0, y0, x1, y1, rnd):
    """A braided rag rug on the floor: an oval of braided rings in old,
    faded cloth colours, close in tone, each ring a run of slanted braid
    ticks."""
    cols = (MADDER, STRAW, INDIGO, LINEN, MADDER, STRAW, INDIGO)
    rings = 6
    cx, cy, rx, ry = (x0 + x1) / 2, (y0 + y1) / 2, (x1 - x0) / 2, (y1 - y0) / 2
    room.ellipse(x0 + 0.8, y0 + 1.0, x1 + 0.8, y1 + 1.0, ramp(FLOOR, 0.12))
    for i in range(rings):
        f = 1 - i / rings
        col = cols[i % len(cols)]
        room.ellipse(cx - rx * f, cy - ry * f, cx + rx * f, cy + ry * f, ramp(col, 0.4))
        n = int(40 * f) + 8
        for k in range(n):
            a = 2 * math.pi * (k + 0.5 * (i % 2)) / n
            px, py = cx + rx * (f - 0.06) * math.cos(a), cy + ry * (f - 0.08) * math.sin(a)
            room.line([(px - 0.4, py - 0.3), (px + 0.4, py + 0.3)], ramp(col, 0.22), 0.3)
            room.line([(px - 0.4, py - 0.7), (px + 0.2, py - 0.3)], ramp(col, 0.6), 0.25)


# ---------------------------------------------------------------- the hearth

def hearth(room, x0, x1, top, wall_bottom, fire_box, rnd):
    """A fieldstone hearth and chimney breast on the back wall, from x0 to x1,
    the breast rising to top; fire_box (x, y, w, h) is the opening the fire
    strip plays in. An oak lintel over the mouth, a trammel hook with a pot
    above the fire, tools by it, a stone apron on the floor."""
    fx, fy, fw, fh = fire_box
    room.rect(x0, top, x1, wall_bottom, ramp(MORTAR, 0.4))
    y = wall_bottom
    row = 0
    while y > top + 0.5:
        h = rnd.uniform(3.2, 4.6)
        t0 = max(top, y - h)
        x = x0 - (row % 2) * rnd.uniform(1.5, 3.0)
        while x < x1:
            w = rnd.uniform(4.5, 8.0)
            a, b = max(x0, x + 0.3), min(x1, x + w - 0.3)
            # A last course under 0.6 px tall would draw an inverted stone.
            if b - a > 1.0 and y - t0 > 0.6:
                tone = 0.5 + rnd.uniform(-0.18, 0.16)
                room.d.rounded_rectangle((a * S, (t0 + 0.3) * S, b * S, (y - 0.3) * S), radius=int(1.2 * S),
                                         fill=ramp(STONE, tone))
                room.speckle(a + 0.4, t0 + 0.8, b - 0.4, y - 0.8, (b - a) * h / 2.5, STONE,
                             (tone - 0.14, tone + 0.12), rnd, (0.25, 0.5))
                room.d.rounded_rectangle((a * S, (t0 + 0.3) * S, b * S, (t0 + 1.0) * S), radius=int(0.5 * S),
                                         fill=ramp(STONE, tone + 0.26))
                room.rect(a, t0 + 0.6, a + 0.5, y - 0.6, ramp(STONE, tone + 0.18))
                room.rect(b - 0.7, t0 + 1.0, b - 0.3, y - 0.3, ramp(STONE, tone - 0.24))
                room.rect(a + 0.4, y - 1.0, b - 0.3, y - 0.3, ramp(STONE, tone - 0.2))
            x += w
        y = t0
        row += 1
    # Soot staining up the breast above the mouth.
    soot = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(soot).ellipse(((fx - 2) * S, (top - 4) * S, (fx + fw + 2) * S, (fy + 2) * S), fill=120)
    soot = soot.filter(ImageFilter.GaussianBlur(3 * S))
    room.lay.paste(Image.new("RGBA", room.lay.size, B.rgb(SOOT[1])), (0, 0), soot)
    # The mouth under an oak lintel.
    room.rect(fx, fy, fx + fw, fy + fh, ramp(SOOT, 0.3))
    room.rect(fx + 1, fy + 1, fx + fw - 1, fy + fh, ramp(SOOT, 0.05))
    timber(room, fx - 2.5, fy - 3.4, fx + fw + 2.5, fy, 0.45, rnd)
    # The trammel: a ratcheted iron bar hanging from inside the flue, an S
    # hook and a black pot over the fire.
    tx = fx + fw / 2
    room.rect(tx - 0.35, fy, tx + 0.35, fy + 4.2, ramp(IRON, 0.45))
    for k in range(4):
        room.rect(tx + 0.35, fy + 0.6 + k, tx + 0.8, fy + 1.0 + k, ramp(IRON, 0.6))
    room.ellipse(tx - 2.8, fy + 4.0, tx + 2.8, fy + 8.2, ramp(IRON, 0.2))
    room.ellipse(tx - 2.2, fy + 4.4, tx - 0.4, fy + 6.0, ramp(IRON, 0.6))
    room.line([(tx - 2.6, fy + 4.6), (tx, fy + 3.4), (tx + 2.6, fy + 4.6)], ramp(IRON, 0.5), 0.3)
    # The stone apron on the floor in front of it: big flat slabs.
    ax0, ax1 = fx - 5, fx + fw + 5
    room.rect(ax0 - 0.6, wall_bottom, ax1 + 0.6, wall_bottom + 6.4, ramp(STONE, 0.15))
    sx = ax0
    while sx < ax1:
        sw = min(ax1 - sx, rnd.uniform(6, 10))
        tone = 0.62 + rnd.uniform(-0.08, 0.08)
        room.rect(sx + 0.2, wall_bottom, sx + sw - 0.2, wall_bottom + 5.8, ramp(STONE, tone))
        room.rect(sx + 0.2, wall_bottom, sx + sw - 0.2, wall_bottom + 0.9, ramp(STONE, tone + 0.22))
        room.speckle(sx + 0.6, wall_bottom + 1.2, sx + sw - 0.6, wall_bottom + 5.2, sw * 2, STONE, (tone - 0.12, tone + 0.1), rnd)
        sx += sw
    # Fire irons and a pail of kindling by the hearth.
    room.line([(ax1 - 1.0, wall_bottom - 10), (ax1 + 0.4, wall_bottom + 1.0)], ramp(IRON, 0.45), 0.4)
    room.line([(ax1 + 0.6, wall_bottom - 9), (ax1 + 1.4, wall_bottom + 1.0)], ramp(IRON, 0.35), 0.4)
    room.ellipse(ax1 - 2.0, wall_bottom - 10.8, ax1 + 0.2, wall_bottom - 9.4, ramp(IRON, 0.6))


def fire_strip(w, h, frames=4, seed=0):
    """The fire in the firebox, `frames` frames of w x h room px side by side
    (w x h x DENSITY art px each): logs on andirons, flames rising and
    leaning a little differently each frame, embers under them, a spark.
    The strip plays over the hearth's dark mouth."""
    out = Image.new("RGBA", (w * DENSITY * frames, h * DENSITY), (0, 0, 0, 0))
    for k in range(frames):
        room = Room(w, h)
        rnd = random.Random(seed * 31 + k)
        # Andirons, embers, the logs (bark and their glowing ends).
        for ax in (2.5, w - 3.5):
            room.rect(ax, h - 4.5, ax + 1.0, h - 0.5, ramp(IRON, 0.35))
            room.rect(ax - 0.4, h - 5.2, ax + 1.4, h - 4.4, ramp(IRON, 0.6))
        room.rect(1, h - 2.8, w - 1, h - 0.6, ramp(FIRE, 0.22))
        room.speckle(1.5, h - 2.6, w - 1.5, h - 0.8, 18, FIRE, (0.45, 0.7, 0.85), rnd, (0.3, 0.6))
        room.line([(2, h - 2.6), (w - 3, h - 4.2)], ramp(OAK, 0.22), 1.8)
        room.line([(3, h - 4.4), (w - 2, h - 2.8)], ramp(OAK, 0.3), 1.8)
        room.ellipse(w - 3.6, h - 4.0, w - 1.6, h - 2.0, ramp(FIRE, 0.55))
        # Flames: overlapping tongues from dark red at the edge to pale.
        ph = 2 * math.pi * k / frames
        for i, (cx, height) in enumerate(((w * 0.3, h * 0.6), (w * 0.52, h * 0.82), (w * 0.74, h * 0.52))):
            sway = 1.0 * math.sin(ph + i * 1.7)
            hh = height * (0.86 + 0.14 * math.sin(ph * 1.3 + i))
            for scale, tone in ((1.0, 0.3), (0.74, 0.5), (0.5, 0.72), (0.28, 0.9), (0.12, 1.0)):
                base = h - 3.2
                half = 2.5 * scale
                room.poly([(cx - half, base), (cx + half, base),
                           (cx + sway * 0.6 + half * 0.35, base - hh * scale * 0.6),
                           (cx + sway, base - hh * scale)], ramp(FIRE, tone))
        sx, sy = rnd.uniform(3, w - 4), rnd.uniform(1, h * 0.3)
        room.rect(sx, sy, sx + 0.6, sy + 0.6, ramp(FIRE, 0.95))
        out.paste(shrink(room, FIRE_PALETTE), (k * w * DENSITY, 0))
    return out


# ---------------------------------------------------------------- furniture

def coverlet(room, x0, y0, x1, y1, cols, rnd, pattern="check"):
    """A woven wool coverlet over a bed, kept inside its rect: a small
    two-tone check of cols crossed by thin undyed threads (pattern "check",
    a linsey-woolsey), or overshot diamonds in cols on undyed linen
    ("overshot"); soft folds down its length, its left edge lit and its right
    in shade."""
    # Woven on a layer of its own and pasted inside the rect, so a pattern
    # that runs past the edges is trimmed there.
    saved_lay, saved_d = room.lay, room.d
    room.lay = Image.new("RGBA", saved_lay.size, (0, 0, 0, 0))
    room.d = ImageDraw.Draw(room.lay)
    if pattern == "check":
        room.rect(x0, y0, x1, y1, ramp(cols, 0.42))
        cell = 2.0
        j = 0
        y = y0
        while y < y1:
            i = 0
            x = x0
            while x < x1:
                if (i + j) % 2 == 0:
                    room.rect(x, y, x + cell, y + cell, ramp(cols, 0.58))
                x += cell
                i += 1
            y += cell
            j += 1
        for k in range(int((x1 - x0) / 6) + 1):
            room.rect(x0 + 3 + k * 6, y0, x0 + 3.35 + k * 6, y1, ramp(LINEN, 0.45))
        for k in range(int((y1 - y0) / 6) + 1):
            room.rect(x0, y0 + 3 + k * 6, x1, y0 + 3.35 + k * 6, ramp(LINEN, 0.45))
    else:
        room.rect(x0, y0, x1, y1, ramp(LINEN, 0.5))
        size = 5.0
        cy = y0 + size / 2
        r = 0
        while cy < y1 + size:
            cx = x0 + (size / 2 if r % 2 else 0)
            while cx < x1 + size:
                room.poly([(cx, cy - size / 2), (cx + size / 2, cy), (cx, cy + size / 2), (cx - size / 2, cy)],
                          ramp(cols, 0.42))
                room.poly([(cx, cy - size / 4), (cx + size / 4, cy), (cx, cy + size / 4), (cx - size / 4, cy)],
                          ramp(LINEN, 0.42))
                cx += size
            cy += size / 2
            r += 1
    woven = room.lay
    room.lay, room.d = saved_lay, saved_d
    box = tuple(int(round(v * S)) for v in (x0, y0, x1, y1))
    room.lay.alpha_composite(woven.crop(box), box[:2])
    # Folds: a darker run down the length with a lit crest beside it.
    for fx in (x0 + (x1 - x0) * 0.33, x0 + (x1 - x0) * 0.7):
        drift = rnd.uniform(-0.8, 0.8)
        shade = Image.new("L", room.lay.size, 0)
        ImageDraw.Draw(shade).line([(fx * S, (y0 + 2) * S), ((fx + drift) * S, (y1 - 0.5) * S)], fill=90,
                                   width=int(0.9 * S))
        room.lay.paste(Image.new("RGBA", room.lay.size, (20, 14, 10, 255)), (0, 0), shade)
        lit = Image.new("L", room.lay.size, 0)
        ImageDraw.Draw(lit).line([((fx - 1.0) * S, (y0 + 2) * S), ((fx - 1.0 + drift) * S, (y1 - 0.5) * S)], fill=60,
                                 width=int(0.5 * S))
        room.lay.paste(Image.new("RGBA", room.lay.size, (250, 240, 220, 255)), (0, 0), lit)
    room.rect(x0, y0, x0 + 0.8, y1, ramp(cols, 0.75))
    room.rect(x1 - 0.8, y0, x1, y1, ramp(cols, 0.2))


def rope_bed(room, x, y, w, h, cover, rnd, pattern="check"):
    """A joined oak bed in the rooms' view, head to the back wall, seen from
    above at a slant: the tall panelled headboard against the wall; the
    mattress's top under the coverlet, a little narrower toward the head as
    it recedes; the coverlet hanging over the foot as a darker front face;
    the low footboard showing its lit top edge and its front, on short legs;
    side rails along both sides, the right one in shade (light from the
    upper left). The coverlet's top starts 12 px below the bed's top, where
    the room's `covers` rect starts, so a sleeper's head shows on the pillow
    above it, and everything down to the footboard is inside that rect."""
    head_bot = y + 10            # the headboard's foot, where the mattress begins
    top_end = y + h - 12.5       # the far edge of the drape: the mattress top ends here
    drape_end = y + h - 7        # the coverlet's hem, over the footboard
    foot_top = y + h - 7.5       # the footboard's top edge
    # The far (head) end is narrower than the near (foot) end: the bed
    # recedes toward the wall.
    inset_head, inset_foot = 5.4, 1.6
    head_l, head_r = x + 2.6, x + w - 2.6

    # The side rails and the frame under the mattress: a dark trapezoid,
    # the right side's face in shade.
    room.poly([(x + inset_head - 1.4, head_bot), (x + w - inset_head + 1.4, head_bot),
               (x + w - inset_foot + 1.4, foot_top), (x + inset_foot - 1.4, foot_top)], ramp(OAK, 0.3))
    room.poly([(x + w - inset_head + 0.2, head_bot), (x + w - inset_head + 1.4, head_bot),
               (x + w - inset_foot + 1.4, foot_top), (x + w - inset_foot + 0.2, foot_top)], ramp(OAK, 0.16))
    room.poly([(x + inset_head - 1.4, head_bot), (x + inset_head - 0.6, head_bot),
               (x + inset_foot - 0.6, foot_top), (x + inset_foot - 1.4, foot_top)], ramp(OAK, 0.6))

    # The headboard: a frame with two panels, a lit top rail, tall turned
    # posts with finials.
    # Everything stays inside the bed's rect (the client cuts the piece by
    # it): the finials' tops at the rect's top edge.
    room.rect(head_l + 1, y + 2.4, head_r - 1, head_bot, ramp(OAK, 0.5))
    room.rect(head_l + 1, y + 2.4, head_r - 1, y + 3.4, ramp(OAK, 0.92))
    for px0, px1 in ((head_l + 3.6, x + w / 2 - 1), (x + w / 2 + 1, head_r - 3.6)):
        room.rect(px0, y + 4.4, px1, y + 8.6, ramp(OAK, 0.34))
        room.rect(px0, y + 4.4, px1, y + 4.9, ramp(OAK, 0.16))
        room.rect(px0, y + 8.0, px1, y + 8.6, ramp(OAK, 0.66))
    for px in (head_l - 0.2, head_r - 2.6):
        room.rect(px, y + 1.8, px + 3, head_bot + 1.5, ramp(OAK, 0.56))
        room.rect(px, y + 1.8, px + 1, head_bot + 1.5, ramp(OAK, 0.82))
        room.rect(px + 2.2, y + 1.8, px + 3, head_bot + 1.5, ramp(OAK, 0.32))
        room.ellipse(px - 0.2, y, px + 3.2, y + 3.0, ramp(OAK, 0.72))
        room.ellipse(px + 0.3, y + 0.4, px + 1.5, y + 1.6, ramp(OAK, 0.98))
        room.rect(px, y + 5.4, px + 3, y + 6.0, ramp(OAK, 0.3))

    # The coverlet's top surface: woven, then trimmed to the receding
    # trapezoid by the frame drawn back over its corners.
    coverlet(room, x + inset_foot, head_bot + 2, x + w - inset_foot, top_end, cover, rnd, pattern)
    for side in (-1, 1):
        if side < 0:
            pts = [(x + inset_foot - 0.1, head_bot + 2), (x + inset_head, head_bot + 2), (x + inset_foot, top_end),
                   (x + inset_foot - 0.1, top_end)]
        else:
            pts = [(x + w - inset_head, head_bot + 2), (x + w - inset_foot + 0.1, head_bot + 2),
                   (x + w - inset_foot + 0.1, top_end), (x + w - inset_foot, top_end)]
        room.poly(pts, ramp(OAK, 0.3 if side < 0 else 0.16))
    # The mattress top lit toward the head and the left, a little shade
    # toward the foot.
    light = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(light).polygon([((x + inset_head) * S, (head_bot + 2) * S), ((x + w * 0.55) * S, (head_bot + 2) * S),
                                   ((x + w * 0.35) * S, top_end * S), ((x + inset_foot) * S, top_end * S)], fill=34)
    room.lay.paste(Image.new("RGBA", room.lay.size, (255, 246, 222, 255)), (0, 0),
                   light.filter(ImageFilter.GaussianBlur(2 * S)))
    # The coverlet over the foot: its front face hanging down, darker, the
    # hem a little uneven, wider than the top as it falls.
    front = [(x + inset_foot, top_end), (x + w - inset_foot, top_end),
             (x + w - inset_foot + 0.8, drape_end), (x + inset_foot - 0.8, drape_end)]
    room.poly(front, ramp(cover, 0.22))
    for k in range(5):
        fx = x + inset_foot + (k + 0.5) * (w - 2 * inset_foot) / 5
        room.line([(fx, top_end + 0.6), (fx + rnd.uniform(-0.4, 0.4), drape_end - 0.3)], ramp(cover, 0.1), 0.45)
    room.rect(x + inset_foot, top_end, x + w - inset_foot, top_end + 0.6, ramp(cover, 0.6))

    # The pillow and bolster on the mattress's head end, linen, seen from
    # above: lit tops, a shadow along their foot side.
    room.d.rounded_rectangle(((x + inset_head + 0.2) * S, (head_bot - 0.8) * S, (x + w - inset_head - 0.2) * S,
                              (head_bot + 2.6) * S), radius=int(1.7 * S), fill=ramp(LINEN, 0.55))
    room.ellipse(x + 7.5, head_bot - 0.4, x + w - 7.5, head_bot + 5.0, ramp(LINEN, 0.78))
    room.ellipse(x + 9, head_bot + 0.1, x + w - 11, head_bot + 2.2, ramp(LINEN, 0.97))
    room.ellipse(x + 9, head_bot + 3.6, x + w - 9, head_bot + 5.0, ramp(LINEN, 0.42))
    # The sheet turned down over the coverlet's top edge.
    room.rect(x + inset_head - 0.2, y + 12, x + w - inset_head + 0.2, y + 14, ramp(LINEN, 0.88))
    room.rect(x + inset_head - 0.2, y + 13.4, x + w - inset_head + 0.2, y + 14, ramp(LINEN, 0.48))

    # The footboard: its lit top edge, its front face with a sunk panel,
    # short turned posts, the legs on the floor.
    room.rect(x + 0.5, foot_top, x + w - 0.5, foot_top + 1.1, ramp(OAK, 0.94))
    room.rect(x + 0.5, foot_top + 1.1, x + w - 0.5, y + h - 2.2, ramp(OAK, 0.5))
    room.rect(x + 5, foot_top + 2.0, x + w - 5, y + h - 3.2, ramp(OAK, 0.38))
    room.rect(x + 5, foot_top + 2.0, x + w - 5, foot_top + 2.5, ramp(OAK, 0.22))
    room.rect(x + 1, y + h - 2.8, x + w - 1, y + h - 2.2, ramp(OAK, 0.2))
    for px in (x, x + w - 3):
        room.rect(px, foot_top - 1.2, px + 3, y + h, ramp(OAK, 0.6))
        room.rect(px, foot_top - 1.2, px + 1, y + h, ramp(OAK, 0.84))
        room.rect(px + 2.2, foot_top - 1.2, px + 3, y + h, ramp(OAK, 0.3))
        room.ellipse(px, foot_top - 2.8, px + 3, foot_top - 0.4, ramp(OAK, 0.78))
        room.ellipse(px + 0.3, foot_top - 2.4, px + 1.5, foot_top - 1.4, ramp(OAK, 0.98))


def trestle_table(room, x, y, w, h, rnd=None, things=True):
    """A trestle table seen from above at a slant: a board top of two planks
    with grain, the front edge's thickness, two trestle legs and the
    stretcher below; on it a wooden trencher and spoon, a redware jug, a
    loaf."""
    top_h = h * 0.58
    rnd = rnd or random.Random(5)
    for lx in (x + 4, x + w - 7):
        room.rect(lx, y + top_h - 1, lx + 3, y + h - 1, ramp(OAK, 0.4))
        room.rect(lx, y + top_h - 1, lx + 1, y + h - 1, ramp(OAK, 0.64))
        room.rect(lx - 1.8, y + h - 2.4, lx + 4.8, y + h, ramp(OAK, 0.36))
        room.rect(lx - 1.8, y + h - 2.4, lx + 4.8, y + h - 1.9, ramp(OAK, 0.6))
    room.rect(x + 6, y + top_h + 3, x + w - 6, y + top_h + 4.8, ramp(OAK, 0.34))
    room.rect(x + 6, y + top_h + 3, x + w - 6, y + top_h + 3.5, ramp(OAK, 0.55))
    room.rect(x, y, x + w, y + top_h / 2, ramp(OAK, 0.78))
    room.rect(x, y + top_h / 2, x + w, y + top_h, ramp(OAK, 0.72))
    for _ in range(10):
        gy, gx = rnd.uniform(y + 0.6, y + top_h - 0.6), rnd.uniform(x, x + w - 6)
        room.rect(gx, gy, min(x + w, gx + rnd.uniform(4, 12)), gy + 0.25, ramp(OAK, rnd.choice((0.6, 0.66, 0.88))))
    room.rect(x, y + top_h / 2 - 0.3, x + w, y + top_h / 2 + 0.3, ramp(OAK, 0.3))
    room.rect(x, y, x + w, y + 0.7, ramp(OAK, 0.98))
    room.rect(x, y, x + 0.7, y + top_h, ramp(OAK, 0.9))
    room.rect(x, y + top_h, x + w, y + top_h + 2.4, ramp(OAK, 0.32))
    room.rect(x, y + top_h + 1.8, x + w, y + top_h + 2.4, ramp(OAK, 0.12))
    if things:
        # A trencher and spoon at the left, a jug at the right end, a loaf
        # between them ending clear of the jug, so none covers another at
        # any table width.
        room.ellipse(x + 4, y + 3.5, x + 12, y + 8.5, ramp(OAK, 0.5))
        room.ellipse(x + 5, y + 4.2, x + 11, y + 7.6, ramp(OAK, 0.62))
        room.line([(x + 9, y + 9.2), (x + 13, y + 7.6)], ramp(OAK, 0.4), 0.5)
        jx = x + w - 5
        lx1 = jx - 3.0
        lx0 = max(x + 13.6, lx1 - 7)
        room.ellipse(lx0, y + 2.2, lx1, y + 6.4, ramp(STRAW, 0.55))
        room.ellipse(lx0 + 0.6, y + 2.4, lx1 - 2, y + 4.4, ramp(STRAW, 0.85))
        room.line([(lx0 + 2, y + 3.0), (lx0 + 3, y + 5.4)], ramp(STRAW, 0.3), 0.3)
        room.ellipse(jx - 2.2, y + 1.0, jx + 2.2, y + 7.0, ramp(REDWARE, 0.5))
        room.ellipse(jx - 1.6, y + 1.4, jx - 0.2, y + 4.0, ramp(REDWARE, 0.85))
        room.rect(jx - 1.1, y + 0.2, jx + 1.1, y + 1.4, ramp(REDWARE, 0.42))


def ladder_chair(room, x, y, w, h, back_left=True):
    """A ladder-back chair seen from the side at a slant: the tall back posts
    with three arched slats on the back side (left when back_left), the rush
    seat across, the turned legs and stretchers. A sitter sits on the seat in
    front of the back."""
    seat_y = y + h * 0.55
    bx = x + 1 if back_left else x + w - 3
    room.rect(bx - 0.4, y + 1.4, bx + 2.4, y + h - 1, ramp(OAK, 0.32))
    room.rect(bx - 0.4, y + 1.4, bx + 0.8, y + h - 1, ramp(OAK, 0.66))
    room.ellipse(bx - 0.5, y, bx + 2.5, y + 2.6, ramp(OAK, 0.8))
    room.ellipse(bx - 0.1, y + 0.3, bx + 1.1, y + 1.3, ramp(OAK, 0.98))
    for sy in (y + 4, y + 7, y + 10):
        sl0, sl1 = max(x, bx - 1.3), min(x + w, bx + 3.3)
        room.rect(sl0, sy, sl1, sy + 1.9, ramp(OAK, 0.74))
        room.rect(sl0, sy, sl1, sy + 0.5, ramp(OAK, 0.95))
        room.rect(sl0, sy + 1.4, sl1, sy + 1.9, ramp(OAK, 0.28))
    sx0, sx1 = (bx + 2, x + w - 0.4) if back_left else (x + 0.4, bx)
    room.rect(sx0, seat_y - 3.2, sx1, seat_y + 0.5, ramp(STRAW, 0.6))
    for k in range(int((sx1 - sx0) * 2)):
        room.rect(sx0 + k * 0.5, seat_y - 3.2, sx0 + k * 0.5 + 0.25, seat_y + 0.5, ramp(STRAW, 0.32 if k % 2 else 0.75))
    room.rect(sx0, seat_y - 3.2, sx1, seat_y - 2.6, ramp(STRAW, 0.92))
    room.rect(sx0, seat_y + 0.5, sx1, seat_y + 2.0, ramp(OAK, 0.56))
    room.rect(sx0, seat_y + 1.5, sx1, seat_y + 2.0, ramp(OAK, 0.24))
    fx = sx1 - 1.8 if back_left else sx0
    room.rect(fx, seat_y + 2.0, fx + 1.8, y + h - 1, ramp(OAK, 0.46))
    room.rect(fx, seat_y + 2.0, fx + 0.6, y + h - 1, ramp(OAK, 0.74))
    for ty in (seat_y + 4.5, seat_y + 7.0):
        room.rect(fx - 0.2, ty, fx + 2.0, ty + 0.6, ramp(OAK, 0.3))
    room.rect(min(fx, bx), y + h - 4.6, max(fx, bx) + 2, y + h - 3.6, ramp(OAK, 0.38))


def blanket_chest(room, x, y, w, h):
    """A pine blanket chest seen from the front at a slant: the lid on top
    with its moulded edge, the front with two sunk panels, iron strap hinges
    over the lid's back edge, a lock plate and hasp, bracket feet."""
    lid_h = h * 0.36
    room.rect(x, y, x + w, y + lid_h, ramp(OAK, 0.8))
    room.rect(x, y, x + w, y + 0.7, ramp(OAK, 0.98))
    for hx in (x + 5, x + w - 9):
        room.rect(hx, y + 0.3, hx + 4, y + 1.3, ramp(IRON, 0.35))
        room.rect(hx, y + 0.3, hx + 4, y + 0.6, ramp(IRON, 0.7))
    room.rect(x, y + lid_h - 1.2, x + w, y + lid_h - 0.5, ramp(OAK, 0.62))
    room.rect(x, y + lid_h - 0.5, x + w, y + lid_h, ramp(OAK, 0.14))
    room.rect(x + 0.5, y + lid_h, x + w - 0.5, y + h - 1.6, ramp(OAK, 0.46))
    for px0, px1 in ((x + 2.5, x + w / 2 - 1.5), (x + w / 2 + 1.5, x + w - 2.5)):
        room.rect(px0, y + lid_h + 1.5, px1, y + h - 3.4, ramp(OAK, 0.34))
        room.rect(px0, y + lid_h + 1.5, px1, y + lid_h + 2.0, ramp(OAK, 0.18))
        room.rect(px0, y + h - 3.9, px1, y + h - 3.4, ramp(OAK, 0.6))
    room.rect(x + w / 2 - 1.4, y + lid_h + 0.4, x + w / 2 + 1.4, y + lid_h + 4.0, ramp(IRON, 0.45))
    room.rect(x + w / 2 - 0.3, y + lid_h + 1.4, x + w / 2 + 0.3, y + lid_h + 2.6, ramp(IRON, 0.05))
    room.rect(x + 0.5, y + h - 1.6, x + 3.5, y + h, ramp(OAK, 0.3))
    room.rect(x + w - 3.5, y + h - 1.6, x + w - 0.5, y + h, ramp(OAK, 0.3))
    room.rect(x + 3.5, y + h - 1.6, x + w - 3.5, y + h - 1.0, ramp(OAK, 0.22))


def cupboard(room, x, y, w, h):
    """A tall joined cupboard against the side wall, seen from the front: a
    moulded cornice, an open shelf of pewter and redware, two panelled doors,
    a drawer, a plinth on two feet."""
    room.rect(x + 0.5, y + 2, x + w - 0.5, y + h - 2, ramp(OAK, 0.5))
    room.rect(x + 0.5, y + 2, x + 1.2, y + h - 2, ramp(OAK, 0.72))
    room.rect(x, y, x + w, y + 2.6, ramp(OAK, 0.78))
    room.rect(x, y, x + w, y + 0.7, ramp(OAK, 0.98))
    room.rect(x, y + 1.4, x + w, y + 1.8, ramp(OAK, 0.5))
    room.rect(x, y + 2.6, x + w, y + 3.2, ramp(OAK, 0.18))
    # The open shelf: a pewter plate stood on edge, a redware cup.
    room.rect(x + 1.5, y + 4, x + w - 1.5, y + 14, ramp(OAK, 0.1))
    px = x + w * 0.38
    room.ellipse(px - 3.2, y + 5, px + 3.2, y + 12.6, ramp(PEWTER, 0.55))
    room.ellipse(px - 2.0, y + 6.2, px + 2.0, y + 11.4, ramp(PEWTER, 0.42))
    room.ellipse(px - 2.6, y + 5.6, px - 0.6, y + 8.4, ramp(PEWTER, 0.95))
    cx = x + w - 3.6
    room.rect(cx - 1.4, y + 9.4, cx + 1.4, y + 13.4, ramp(REDWARE, 0.5))
    room.rect(cx - 1.4, y + 9.4, cx - 0.6, y + 13.4, ramp(REDWARE, 0.8))
    room.rect(x + 1, y + 13.4, x + w - 1, y + 15.2, ramp(OAK, 0.74))
    room.rect(x + 1, y + 13.4, x + w - 1, y + 13.9, ramp(OAK, 0.95))
    for dy0, dy1 in ((y + 17, y + 30), (y + 31, y + h - 10)):
        room.rect(x + 1.5, dy0, x + w - 1.5, dy1, ramp(OAK, 0.56))
        room.rect(x + 3, dy0 + 1.6, x + w - 3, dy1 - 1.6, ramp(OAK, 0.4))
        room.rect(x + 3, dy0 + 1.6, x + w - 3, dy0 + 2.1, ramp(OAK, 0.22))
        room.rect(x + 3, dy1 - 2.1, x + w - 3, dy1 - 1.6, ramp(OAK, 0.7))
        room.ellipse(x + w - 3.8, (dy0 + dy1) / 2 - 0.7, x + w - 2.4, (dy0 + dy1) / 2 + 0.7, ramp(IRON, 0.65))
    room.rect(x + 1.5, y + h - 9, x + w - 1.5, y + h - 3.4, ramp(OAK, 0.54))
    room.rect(x + 1.5, y + h - 9, x + w - 1.5, y + h - 8.5, ramp(OAK, 0.82))
    room.ellipse(x + w / 2 - 1, y + h - 6.8, x + w / 2 + 1, y + h - 5.0, ramp(IRON, 0.6))
    room.rect(x + 0.5, y + h - 3.4, x + w - 0.5, y + h - 1.6, ramp(OAK, 0.32))
    room.rect(x + 0.5, y + h - 1.6, x + 3, y + h, ramp(OAK, 0.3))
    room.rect(x + w - 3, y + h - 1.6, x + w - 0.5, y + h, ramp(OAK, 0.3))


# ---------------------------------------------------------------- checks

def room_entry(key, path=ROOMS_GD):
    """The room's pieces, covers and anims from interior_rooms.gd, as
    {"pieces": [(x, y, w, h, sort_y)], "covers": {bed: (x, y, w, h)},
    "anims": [(sheet, frames, x, y)], "sheets": {sheet, pieces_sheet},
    "size": (w, h), "density": d}."""
    text = open(path, encoding="utf-8").read()
    m = re.search(r'\n    "%s": \{(.*?)\n    \},' % re.escape(key), text, re.S)
    if not m:
        raise ValueError("no room %s in %s" % (key, path))
    body = m.group(1)
    num = r"(-?\d+)"
    pieces = [tuple(int(v) for v in p) for p in re.findall(
        r'\{"x": %s, "y": %s, "w": %s, "h": %s, "sort_y": %s\}' % (num, num, num, num, num),
        body.split('"covers"')[0].split('"anims"')[0])]
    covers = {k: tuple(int(v) for v in r) for k, *r in re.findall(
        r'"(\w+)": \{"x": %s, "y": %s, "w": %s, "h": %s\}' % (num, num, num, num),
        body.split('"covers"')[1].split('"anims"')[0])} if '"covers"' in body else {}
    anims = [(s, int(f), int(x), int(y)) for s, f, x, y in re.findall(
        r'\{"sheet": "([^"]+)", "frames": (\d+), "fps": [\d.]+, "x": (-?\d+), "y": (-?\d+)', body)]
    # The room's own sheets: the first of each key (an anim's "sheet" comes
    # later and is not the room's).
    sheets = {}
    for k, v in re.findall(r'"(sheet|pieces_sheet)": "([^"]+)"', body):
        sheets.setdefault(k, v)
    size = tuple(int(v) for v in re.search(r'"size": Vector2i\((\d+), (\d+)\)', body).groups())
    dm = re.search(r'"density": (\d+)', body)
    return {"pieces": pieces, "covers": covers, "anims": anims, "sheets": sheets, "size": size,
            "density": int(dm.group(1)) if dm else 1}
