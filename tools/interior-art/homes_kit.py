"""Painters only the home and tent interiors use (LLM-969), on top of the
shared kit in common.py: the same canvas, view and light (the back wall
head-on, the floor and furniture from above at a slant, light from the upper
left), in room px.

  The loft:        loft_hatch, ladder_rails (on the room sheet), ladder (a piece)
  Floors:          flagstones, earth (the tent's ground)
  The blue houses: limewash (another tint for common.plaster_wall), wainscot,
                   brick_hearth, warming_pan, spinning_wheel
  Set dressing:    corn_braid, onion_string, musket, lantern
  Furniture:       blanket, straw_pallet, grain_sack, piece_shadows
  The tent:        tent_shell (canvas, poles, ground), bedroll, snapsack

The tent's canvas, rope, wool, leather and hemp are the painted camp's
(tools/tree-art/camp.py), so the tent seen from inside is the tent outside.
"""

import contextlib
import math

from PIL import Image, ImageChops, ImageDraw, ImageFilter

import common as C
from common import ramp
import camp as K  # tools/tree-art (common puts it on the path)

# ---------------------------------------------------------------- palettes
# Limewash tinted with yellow ochre, the blue houses' walls.
OCHRE = ["5a4e32", "746642", "8e8054", "a89868", "bcac7a", "cebe8e", "ddd0a4"]
# Boards painted a muted blue-grey (smalt and white lead in linseed oil),
# worn and smoked.
PAINT = ["262a2c", "323a3e", "404a4e", "505c60", "626e70", "788284", "8e9898"]
# Soft, smoked red brick and the pale lime mortar between.
BRICK = ["3a2018", "50291e", "663626", "7a4430", "8c543e", "9c6650"]
LIME = ["7e7664", "9c947e", "b8b098"]
# Coverlets: wool dyed green (weld over indigo) and butternut brown.
GREEN = ["1c2a1a", "2a3e24", "3c5430", "546c40", "6e8656"]
BUTTERNUT = ["3a2410", "583818", "784e20", "966a2c", "b0883e"]
BRASS = ["4a3410", "6e5018", "967022", "bc9434", "dcb85a"]
# Indian corn, and the trodden ground in the tent.
CORN = ["5a3010", "8a5216", "b87a1e", "dca236", "eec462"]
EARTH = ["2e2418", "42341f", "56462c", "6c5a3a", "82704a", "98865c"]
# Oiled linen (the tent's ground cloth), yellowed and darkened by the oil.
OILCLOTH = ["2e2614", "443a1e", "5c5028", "766836", "8e8046"]
# The camp's materials.
CANVAS, ROPE, WOOL, LEATHER, HEMP, WOOD = K.LINEN, K.ROPE, K.WOOL, K.LEATHER, K.HEMP, K.WOOD

TENT_PALETTE = CANVAS + ROPE + WOOD + EARTH + C.HERB + C.STRAW + C.SOOT + C.IRON + C.PEWTER + C.DAYLIGHT + C.FIRE[3:]
TENT_PIECE_PALETTE = (WOOL + LEATHER + HEMP + CANVAS + ROPE + OILCLOTH + C.MADDER + C.INDIGO + C.IRON + C.PEWTER + C.STRAW
                      + EARTH + C.SOOT)


def lerp(a, b, t):
    return (a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t)


@contextlib.contextmanager
def limewash(cols):
    """common.plaster_wall in another limewash: it reads common.PLASTER when
    it is called, so swap it for the call and put it back."""
    saved = C.PLASTER
    C.PLASTER = cols
    try:
        yield
    finally:
        C.PLASTER = saved


def piece_shadows(room, rects, strength=170):
    """Each piece's shadow on the floor: soft, under its feet and lower
    edge, a little to the right (light from the upper left)."""
    for x, y, w, h in rects:
        room.shadow(x + 1, y + h - 3, x + w + 1, y + h + 1.5, strength)


def silhouette(room, pts, col, grow=0.45):
    """A dark outline round a shape on the room sheet, so it reads against a
    busy ground: the polygon drawn a little larger, before the shape."""
    cx = sum(p[0] for p in pts) / len(pts)
    cy = sum(p[1] for p in pts) / len(pts)
    out = []
    for px, py in pts:
        dx, dy = px - cx, py - cy
        n = math.hypot(dx, dy) or 1.0
        out.append((px + dx / n * grow, py + dy / n * grow))
    room.poly(out, col)


# ---------------------------------------------------------------- floors

def flagstones(room, x0, y0, x1, y1, rnd):
    """A floor of flat fieldstone slabs, big and small, laid roughly in
    rows with dark earth in the joints: each slab its own quiet tone, a
    faint lit top edge and shaded foot, its corners knocked about."""
    room.rect(x0, y0, x1, y1, ramp(EARTH, 0.3))
    y = y0
    while y < y1:
        row_h = rnd.uniform(6.0, 10.0)
        x = x0 - rnd.uniform(0, 8)
        while x < x1:
            w = rnd.choice((rnd.uniform(5, 8), rnd.uniform(9, 17), rnd.uniform(9, 17)))
            # A slab may fall short of the row, with a small stone under it.
            h = row_h if rnd.random() < 0.7 else row_h * rnd.uniform(0.5, 0.65)
            a, b = max(x0, x + 0.3), min(x1, x + w - 0.3)
            if b - a > 1.5:
                for top, hh in ((y, h),) + (((y + h, row_h - h),) if h < row_h else ()):
                    if hh < 2:
                        continue
                    tone = 0.38 + rnd.uniform(-0.05, 0.05)
                    pts = [(a + rnd.uniform(0, 1.4), top + 0.4 + rnd.uniform(0, 0.8)),
                           (b - rnd.uniform(0, 1.4), top + 0.4 + rnd.uniform(0, 0.8)),
                           (b, top + hh * rnd.uniform(0.3, 0.7)),
                           (b - rnd.uniform(0, 1.4), top + hh - 0.4 - rnd.uniform(0, 0.8)),
                           (a + rnd.uniform(0, 1.4), top + hh - 0.4 - rnd.uniform(0, 0.8)),
                           (a, top + hh * rnd.uniform(0.3, 0.7))]
                    room.poly(pts, ramp(C.STONE, tone))
                    room.speckle(a + 0.8, top + 1.0, b - 0.8, top + hh - 1.0, (b - a) * hh / 9, C.STONE,
                                 (tone - 0.05, tone + 0.05), rnd, (0.25, 0.5))
                    room.line([pts[0], pts[1]], ramp(C.STONE, tone + 0.07), 0.35)
                    room.line([pts[4], pts[3]], ramp(C.STONE, tone - 0.09), 0.4)
            x += w
        y += row_h


def earth(room, x0, y0, x1, y1, rnd):
    """Trodden ground: earth with a little grit and a few straws."""
    room.rect(x0, y0, x1, y1, ramp(EARTH, 0.45))
    room.speckle(x0, y0, x1, y1, (x1 - x0) * (y1 - y0) / 10, EARTH, (0.36, 0.55), rnd, (0.3, 0.8))
    for _ in range(int((x1 - x0) * (y1 - y0) / 90)):
        px, py = rnd.uniform(x0, x1), rnd.uniform(y0, y1)
        a = rnd.uniform(0, math.pi)
        ln = rnd.uniform(1.5, 3.5)
        room.line([(px, py), (px + math.cos(a) * ln, py + math.sin(a) * ln * 0.5)],
                  ramp(C.STRAW, rnd.choice((0.4, 0.6))), 0.25)


# ---------------------------------------------------------------- the loft

def loft_hatch(room, x0, x1, y1, rnd):
    """The way up to the loft: an opening at the top of the back wall from
    the ceiling down to y1, framed in oak, the loft dim beyond it (rafters'
    feet, hay, a basket), the loft floor's edge at its foot with hay hanging
    over it."""
    for yy in range(0, int(y1 * 4)):
        room.rect(x0, yy / 4, x1, yy / 4 + 0.25, ramp(C.TIMBER, 0.02 + 0.16 * yy / (y1 * 4)))
    for rx in (x0 + 4, x0 + (x1 - x0) / 2 + 1, x1 - 4):
        room.rect(rx - 0.8, 0, rx + 0.8, y1 - 9, ramp(C.TIMBER, 0.12))
    room.ellipse(x0 + 1, y1 - 9, x0 + (x1 - x0) * 0.62, y1 - 1, ramp(C.STRAW, 0.18))
    room.speckle(x0 + 2, y1 - 8, x0 + (x1 - x0) * 0.55, y1 - 2, 30, C.STRAW, (0.1, 0.3), rnd)
    room.ellipse(x1 - 9, y1 - 7.5, x1 - 2, y1 - 2, ramp(C.STRAW, 0.12))
    room.rect(x1 - 9, y1 - 7.5, x1 - 2, y1 - 6.7, ramp(C.STRAW, 0.32))
    room.rect(x0, y1 - 2.4, x1, y1, ramp(C.TIMBER, 0.3))
    room.rect(x0, y1 - 2.4, x1, y1 - 1.8, ramp(C.TIMBER, 0.55))
    for _ in range(int((x1 - x0) * 1.2)):
        hx = rnd.uniform(x0 + 0.5, x1 - 0.5)
        room.line([(hx, y1 - 2.6), (hx + rnd.uniform(-1.2, 1.2), y1 - 2.2 + rnd.uniform(0.6, 3.2))],
                  ramp(C.STRAW, rnd.choice((0.3, 0.45, 0.6, 0.75))), 0.25)
    C.timber(room, x0 - 2, 0, x0, y1 + 1.6, 0.4, rnd)
    C.timber(room, x1, 0, x1 + 2, y1 + 1.6, 0.4, rnd)


def ladder_rails(room, x, w, y0, y1):
    """The ladder's top on the room sheet: its rails and a rung going up
    from the piece's top (y1) into the loft hatch (to y0), in the hatch's
    shadow."""
    for rx in (x + 2.5, x + w - 4.5):
        room.rect(rx, y0, rx + 2, y1, ramp(C.OAK, 0.3))
        room.rect(rx, y0, rx + 0.6, y1, ramp(C.OAK, 0.45))
    for ry in range(int(y1) - 5, int(y0), -5):
        room.rect(x + 4.5, ry, x + w - 4.5, ry + 1.1, ramp(C.OAK, 0.36))


def ladder(room, x, y, w, h):
    """A ladder leaning on the back wall up to the loft, seen from the
    front: two rails, a little closer together at the top as it leans away,
    round rungs lit on top; its top meets ladder_rails on the room sheet."""
    foot = y + h
    for rx, lit in ((x + 1.5, True), (x + w - 3.5, False)):
        lean = 1.0 if lit else -1.0
        rail = [(rx, foot), (rx + 2, foot), (rx + 2 + lean, y), (rx + lean, y)]
        room.poly(rail, ramp(C.OAK, 0.5))
        room.poly([(rx, foot), (rx + 0.7, foot), (rx + 0.7 + lean, y), (rx + lean, y)], ramp(C.OAK, 0.8))
        room.poly([(rx + 1.5, foot), (rx + 2, foot), (rx + 2 + lean, y), (rx + 1.5 + lean, y)], ramp(C.OAK, 0.26))
        room.rect(rx - 0.2, foot - 1.0, rx + 2.2, foot, ramp(C.OAK, 0.2))
    ry = foot - 5
    while ry > y + 1:
        t = (foot - ry) / h
        a, b = x + 3.5 + t, x + w - 3.5 - t
        room.rect(a, ry, b, ry + 1.3, ramp(C.OAK, 0.6))
        room.rect(a, ry, b, ry + 0.45, ramp(C.OAK, 0.92))
        room.rect(a, ry + 0.9, b, ry + 1.3, ramp(C.OAK, 0.24))
        ry -= 5


# ---------------------------------------------------------------- the blue houses

def wainscot(room, x0, x1, top, bottom, rnd):
    """Upright pine boards painted a muted blue-grey along the foot of the
    wall, top to bottom: a quiet bead down each board's edge, the paint worn
    through to the wood here and there low down, a moulded cap rail along
    the top."""
    x = x0
    while x < x1:
        b = min(x1, x + rnd.uniform(5.0, 7.5))
        t = 0.5 + rnd.uniform(-0.04, 0.04)
        room.rect(x, top, b, bottom, ramp(PAINT, t))
        room.rect(x, top, x + 0.4, bottom, ramp(PAINT, t - 0.2))
        room.rect(x + 0.4, top, x + 0.8, bottom, ramp(PAINT, t + 0.14))
        room.speckle(x + 1, top + 1, b - 0.5, bottom, (b - x) * (bottom - top) / 16, PAINT, (t - 0.06, t + 0.06), rnd,
                     (0.25, 0.5))
        for _ in range(int((b - x) * (bottom - top) / 70)):
            wx, wy = rnd.uniform(x + 1, b - 1.5), rnd.uniform(top + (bottom - top) * 0.55, bottom - 0.6)
            room.ellipse(wx, wy, wx + rnd.uniform(0.7, 1.8), wy + rnd.uniform(0.4, 0.8), ramp(C.OAK, rnd.uniform(0.4, 0.5)))
        x = b
    room.rect(x0, top - 1.8, x1, top + 0.4, ramp(PAINT, 0.6))
    room.rect(x0, top - 1.8, x1, top - 1.2, ramp(PAINT, 0.85))
    room.rect(x0, top - 0.4, x1, top + 0.4, ramp(PAINT, 0.36))
    room.rect(x0, top + 0.4, x1, top + 0.9, ramp(PAINT, 0.18))


def brick_hearth(room, x0, x1, top, wall_bottom, fire_box, rnd):
    """A brick chimney breast on the back wall, from x0 to x1 up to top, in
    soft smoked brick laid in uneven courses with pale lime joints, worn
    limewash clinging here and there; fire_box (x, y, w, h) is the mouth the
    fire strip plays in. An oak lintel over the mouth, a trammel and brass
    kettle above the fire, a hearth of bricks laid flat in front, fire irons
    by it."""
    fx, fy, fw, fh = fire_box
    room.rect(x0, top, x1, wall_bottom, ramp(LIME, 0.4))
    y, row = wall_bottom, 0
    while y > top + 0.2:
        bh = rnd.uniform(1.8, 2.3)
        t0 = max(top, y - bh)
        x = x0 - rnd.uniform(0.5, 3.5)
        while x < x1:
            bw = rnd.uniform(3.8, 5.0)
            a, b = max(x0, x + 0.25), min(x1, x + bw - 0.25)
            if b - a > 0.5 and y - t0 > 0.6:
                tone = 0.5 + rnd.uniform(-0.1, 0.1)
                if rnd.random() < 0.08:
                    tone -= 0.22
                room.rect(a, t0 + 0.3, b, y - 0.2, ramp(BRICK, tone))
                room.rect(a, t0 + 0.3, b, t0 + 0.6, ramp(BRICK, tone + 0.1))
            x += bw
        y, row = t0, row + 1
    # Old limewash still clinging to the brick, in patches.
    for _ in range(int((x1 - x0) * (wall_bottom - top) / 160)):
        px, py = rnd.uniform(x0, x1 - 4), rnd.uniform(top, wall_bottom - 14)
        room.ellipse(px, py, px + rnd.uniform(3, 7), py + rnd.uniform(1.5, 3.5), ramp(LIME, rnd.uniform(0.2, 0.5)))
    # Soot up the breast over the mouth.
    soot = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(soot).ellipse(((fx - 2) * C.S, (top - 6) * C.S, (fx + fw + 2) * C.S, (fy + 2) * C.S), fill=130)
    room.lay.paste(Image.new("RGBA", room.lay.size, C.B.rgb(C.SOOT[1])), (0, 0),
                   soot.filter(ImageFilter.GaussianBlur(3 * C.S)))
    room.rect(fx, fy, fx + fw, fy + fh, ramp(C.SOOT, 0.3))
    room.rect(fx + 1, fy + 1, fx + fw - 1, fy + fh, ramp(C.SOOT, 0.05))
    C.timber(room, fx - 2.5, fy - 3.4, fx + fw + 2.5, fy, 0.45, rnd)
    # The trammel and a brass kettle over the fire.
    tx = fx + fw / 2
    room.rect(tx - 0.35, fy, tx + 0.35, fy + 3.6, ramp(C.IRON, 0.45))
    for k in range(3):
        room.rect(tx + 0.35, fy + 0.6 + k, tx + 0.8, fy + 1.0 + k, ramp(C.IRON, 0.6))
    room.ellipse(tx - 3.0, fy + 3.8, tx + 3.0, fy + 8.2, ramp(BRASS, 0.3))
    room.ellipse(tx - 2.4, fy + 4.2, tx - 0.6, fy + 5.8, ramp(BRASS, 0.75))
    room.rect(tx - 1.6, fy + 3.2, tx + 1.6, fy + 4.0, ramp(BRASS, 0.5))
    room.line([(tx + 2.8, fy + 5.0), (tx + 4.6, fy + 3.6)], ramp(BRASS, 0.4), 0.45)
    room.line([(tx - 2.6, fy + 4.2), (tx, fy + 2.8), (tx + 2.6, fy + 4.2)], ramp(C.IRON, 0.5), 0.3)
    # The hearth: bricks laid flat on the floor in front, seen from above.
    ax0, ax1 = fx - 5, fx + fw + 5
    room.rect(ax0 - 0.6, wall_bottom, ax1 + 0.6, wall_bottom + 6.4, ramp(LIME, 0.2))
    for r in range(3):
        yy = wall_bottom + r * 2.1
        xx = ax0 - (r % 2) * 2.2
        while xx < ax1:
            a, b = max(ax0, xx + 0.25), min(ax1, xx + 4.4 - 0.25)
            if b - a > 0.5:
                tone = 0.6 + rnd.uniform(-0.08, 0.08)
                room.rect(a, yy + 0.25, b, yy + 1.9, ramp(BRICK, tone))
                room.rect(a, yy + 0.25, b, yy + 0.6, ramp(BRICK, tone + 0.12))
            xx += 4.4
    room.line([(ax1 - 1.0, wall_bottom - 10), (ax1 + 0.4, wall_bottom + 1.0)], ramp(C.IRON, 0.45), 0.4)
    room.line([(ax1 + 0.6, wall_bottom - 9), (ax1 + 1.4, wall_bottom + 1.0)], ramp(C.IRON, 0.35), 0.4)
    room.ellipse(ax1 - 2.0, wall_bottom - 10.8, ax1 + 0.2, wall_bottom - 9.4, ramp(C.IRON, 0.6))


def warming_pan(room, cx, y):
    """A brass warming pan hung by its long oak handle from a nail at
    (cx, y): the round pan at the foot, its pierced lid lit at the upper
    left."""
    room.rect(cx - 0.4, y - 0.4, cx + 0.4, y + 0.4, ramp(C.IRON, 0.3))
    room.line([(cx, y), (cx + 0.6, y + 13)], ramp(C.OAK, 0.55), 0.8)
    room.line([(cx - 0.3, y + 1), (cx + 0.2, y + 12)], ramp(C.OAK, 0.8), 0.3)
    room.ellipse(cx - 3.6, y + 12.4, cx + 4.6, y + 19.4, ramp(BRASS, 0.42))
    room.ellipse(cx - 3.0, y + 12.8, cx + 4.0, y + 18.6, ramp(BRASS, 0.62))
    room.ellipse(cx - 2.6, y + 13.2, cx - 0.2, y + 15.2, ramp(BRASS, 0.95))
    for k in range(5):
        a = 2 * math.pi * k / 5 + 0.4
        px, py = cx + 0.5 + 1.8 * math.cos(a), y + 15.8 + 1.4 * math.sin(a)
        room.rect(px - 0.25, py - 0.25, px + 0.25, py + 0.25, ramp(BRASS, 0.12))


def spinning_wheel(room, x, foot, rnd):
    """A great wheel for wool standing against the back wall, side on, its
    feet on the floor at foot: a stout bench on three legs, the big wheel on
    its post at the right (a heavy rim, a few stout spokes), the spindle head
    on its post at the left with a hank of yarn."""
    cx, cy, r = x + 15, foot - 18, 8.4
    bench_y = foot - 7.4
    for lx0, lx1 in ((x + 2.4, x + 1.2), (x + 19.5, x + 20.8), (x + 11, x + 11.6)):
        room.line([(lx0, bench_y + 1), (lx1, foot)], ramp(C.OAK, 0.36), 1.0)
        room.line([(lx0 - 0.3, bench_y + 1), (lx1 - 0.3, foot)], ramp(C.OAK, 0.6), 0.35)
    room.rect(x, bench_y, x + 22, bench_y + 1.8, ramp(C.OAK, 0.5))
    room.rect(x, bench_y, x + 22, bench_y + 0.5, ramp(C.OAK, 0.82))
    room.rect(x, bench_y + 1.4, x + 22, bench_y + 1.8, ramp(C.OAK, 0.22))
    # The wheel's post and the wheel.
    room.rect(cx + 2.4, cy, cx + 3.8, bench_y, ramp(C.OAK, 0.42))
    d, s = room.d, C.S
    for k in range(6):
        a = 2 * math.pi * k / 6 + 0.3
        room.line([(cx, cy), (cx + (r - 0.6) * math.cos(a), cy + (r - 0.6) * math.sin(a))], ramp(C.OAK, 0.4), 0.45)
    d.ellipse(((cx - r) * s, (cy - r) * s, (cx + r) * s, (cy + r) * s), outline=ramp(C.OAK, 0.36), width=int(1.1 * s))
    d.arc(((cx - r) * s, (cy - r) * s, (cx + r) * s, (cy + r) * s), 150, 290, fill=ramp(C.OAK, 0.78), width=int(0.45 * s))
    room.ellipse(cx - 1.2, cy - 1.2, cx + 1.2, cy + 1.2, ramp(C.OAK, 0.62))
    # The spindle head: a post with a block, the spindle pointing left, yarn,
    # and the drive band from the wheel.
    hx = x + 2.4
    room.rect(hx, foot - 15, hx + 1.2, bench_y, ramp(C.OAK, 0.48))
    room.rect(hx - 1.2, foot - 16.8, hx + 2.6, foot - 14.6, ramp(C.OAK, 0.6))
    room.rect(hx - 1.2, foot - 16.8, hx + 2.6, foot - 16.2, ramp(C.OAK, 0.85))
    room.line([(hx - 3.6, foot - 15.8), (hx + 1.4, foot - 15.6)], ramp(C.IRON, 0.5), 0.3)
    room.ellipse(hx - 3.4, foot - 17.2, hx - 0.8, foot - 14.4, ramp(C.LINEN, 0.75))
    room.line([(hx + 1.6, foot - 16.4), (cx - 0.6, cy - r + 0.4)], ramp(C.LINEN, 0.5), 0.2)


# ---------------------------------------------------------------- set dressing

def corn_braid(room, cx, y, rnd, n=5):
    """Ears of Indian corn braided by their husks and hung from a nail at
    (cx, y): the husks folded back up over the knot, the ears fanning down,
    one of them red flint corn."""
    room.line([(cx, y), (cx, y + 1.6)], ramp(C.STRAW, 0.4), 0.4)
    for k in range(7):
        a = math.radians(-66 + k * 22 + rnd.uniform(-6, 6))
        ln = rnd.uniform(2.4, 3.8)
        room.line([(cx, y + 2.4), (cx + math.sin(a) * ln, y + 2.4 - abs(math.cos(a)) * ln * 0.5)],
                  ramp(C.STRAW, rnd.choice((0.55, 0.75, 0.9))), 0.5)
    red = rnd.randrange(n)
    for i in range(n):
        a = math.radians(-26 + 52 * i / (n - 1) + rnd.uniform(-4, 4))
        ln = rnd.uniform(6.5, 8.5)
        x0, y0 = cx, y + 2.6
        x1, y1 = x0 + math.sin(a) * ln, y0 + math.cos(a) * ln
        cols = C.MADDER if i == red else CORN
        room.line([(x0, y0), (x1, y1)], ramp(cols, 0.4), 1.9)
        for k in range(1, 7):
            px, py = lerp((x0, y0), (x1, y1), k / 7)
            room.rect(px - 0.75, py - 0.3, px - 0.1, py + 0.2, ramp(cols, 0.8))
            room.rect(px + 0.2, py, px + 0.7, py + 0.4, ramp(cols, 0.2))
        room.ellipse(x1 - 0.9, y1 - 0.9, x1 + 0.9, y1 + 0.9, ramp(cols, 0.35))
    room.rect(cx - 0.9, y + 1.6, cx + 0.9, y + 3.0, ramp(C.STRAW, 0.25))


def onion_string(room, cx, y, rnd, n=8):
    """Onions plaited by their dry tops into a rope, hung from a nail at
    (cx, y): golden-brown bulbs staggered down it, each lit on its left."""
    room.line([(cx, y), (cx + 0.3, y + 3 + n * 1.6)], ramp(C.STRAW, 0.5), 0.6)
    for k in range(n):
        side = -1 if k % 2 else 1
        ox, oy = cx + side * rnd.uniform(0.8, 1.6), y + 2.6 + k * 1.6
        room.ellipse(ox - 1.5, oy - 1.0, ox + 1.5, oy + 1.4, ramp(C.REDWARE, 0.7))
        room.ellipse(ox - 1.1, oy - 0.7, ox - 0.1, oy + 0.4, ramp(C.STRAW, 0.85))
        room.rect(ox - 0.2, oy + 1.2, ox + 0.2, oy + 1.8, ramp(C.STRAW, 0.6))


def musket(room, x0, x1, y):
    """A firelock laid on two iron hooks across the chimney breast at y,
    its butt at x0, drawn in a dark outline so it reads on the stone: the
    stock, the lock and cock, the long barrel to x1 with the ramrod under
    it."""
    stock = [(x0, y - 1.4), (x0 + 3.0, y - 1.0), (x0 + 8.5, y - 0.2), (x0 + 11, y - 0.4), (x1 - 7, y - 0.2),
             (x1 - 7, y + 0.8), (x0 + 11, y + 1.0), (x0 + 8.5, y + 1.2), (x0 + 3.0, y + 2.6), (x0, y + 2.8)]
    barrel = [(x0 + 10.5, y - 1.0), (x1, y - 1.0), (x1, y - 0.1), (x0 + 10.5, y - 0.1)]
    room.poly([(x0 - 0.6, y - 2.0), (x0 + 10.5, y - 1.7), (x1 + 0.6, y - 1.7), (x1 + 0.6, y + 0.6), (x1 - 6.4, y + 1.6),
               (x0 + 8.5, y + 2.0), (x0 + 3.0, y + 3.4), (x0 - 0.6, y + 3.6)], ramp(C.SOOT, 0.2))
    room.poly(stock, ramp(C.OAK, 0.55))
    room.line([(x0, y - 1.2), (x0 + 8.5, y - 0.1)], ramp(C.OAK, 0.85), 0.35)
    room.poly(barrel, ramp(C.PEWTER, 0.35))
    room.rect(x0 + 10.5, y - 1.0, x1, y - 0.7, ramp(C.PEWTER, 0.8))
    room.rect(x0 + 12, y + 0.9, x1 - 8, y + 1.3, ramp(C.OAK, 0.7))
    room.rect(x0 + 8.6, y - 0.6, x0 + 12, y + 0.8, ramp(C.IRON, 0.55))
    room.line([(x0 + 9.2, y - 0.6), (x0 + 9.0, y - 2.2), (x0 + 10.2, y - 2.6)], ramp(C.IRON, 0.25), 0.45)
    room.rect(x0, y - 1.4, x0 + 0.6, y + 2.8, ramp(C.IRON, 0.5))
    for hx in (x0 + 13, x1 - 9):
        room.rect(hx - 0.35, y - 2.2, hx + 0.35, y + 2.0, ramp(C.IRON, 0.15))
        room.rect(hx - 0.35, y + 1.4, hx + 1.2, y + 2.0, ramp(C.IRON, 0.15))


def lantern(room, cx, y, lit=True):
    """A pierced tin lantern hung by its ring from a nail at (cx, y), in a
    dark outline: the ring handle, the conical cap, the tall punched body
    with its door, the base; candlelight through the holes when lit."""
    outline = ramp(C.SOOT, 0.2)
    room.ellipse(cx - 1.3, y - 0.6, cx + 1.3, y + 2.0, outline)
    room.poly([(cx - 1.0, y + 1.4), (cx + 1.0, y + 1.4), (cx + 3.2, y + 4.4), (cx + 3.2, y + 12.6),
               (cx - 3.2, y + 12.6), (cx - 3.2, y + 4.4)], outline)
    d, s = room.d, C.S
    d.ellipse(((cx - 0.9) * s, (y - 0.2) * s, (cx + 0.9) * s, (y + 1.6) * s), outline=ramp(C.PEWTER, 0.7), width=int(0.35 * s))
    room.poly([(cx - 0.7, y + 1.6), (cx + 0.7, y + 1.6), (cx + 2.6, y + 4.4), (cx - 2.6, y + 4.4)], ramp(C.PEWTER, 0.6))
    room.poly([(cx - 0.7, y + 1.6), (cx - 0.1, y + 1.6), (cx - 1.4, y + 4.4), (cx - 2.6, y + 4.4)], ramp(C.PEWTER, 0.9))
    room.rect(cx - 2.6, y + 4.4, cx + 2.6, y + 11.6, ramp(C.PEWTER, 0.45))
    room.rect(cx - 2.6, y + 4.4, cx - 1.6, y + 11.6, ramp(C.PEWTER, 0.8))
    room.rect(cx + 1.8, y + 4.4, cx + 2.6, y + 11.6, ramp(C.PEWTER, 0.25))
    room.rect(cx - 0.4, y + 5.2, cx + 1.4, y + 10.6, ramp(C.PEWTER, 0.35))
    room.rect(cx - 3.0, y + 11.4, cx + 3.0, y + 12.4, ramp(C.PEWTER, 0.55))
    for k in range(6):
        px, py = cx - 1.6 + (k % 2) * 2.6, y + 5.6 + (k // 2) * 2.0
        room.rect(px - 0.25, py - 0.25, px + 0.25, py + 0.25,
                  ramp(C.FIRE, 0.85) if lit else ramp(C.SOOT, 0.3))


# ---------------------------------------------------------------- furniture

def blanket(room, x0, y0, x1, y1, cols, stripe, rnd):
    """A plain homespun wool blanket lying flat: soft corners, a faint nap,
    one band of stripe woven across near each end, soft rumples slanting
    across it, its left edge lit and its right in shade."""
    S = C.S
    room.d.rounded_rectangle((x0 * S, y0 * S, x1 * S, y1 * S), radius=int(1.4 * S), fill=ramp(cols, 0.5))
    room.speckle(x0 + 0.5, y0 + 0.5, x1 - 0.5, y1 - 0.5, (x1 - x0) * (y1 - y0) / 6, cols, (0.42, 0.58), rnd, (0.25, 0.5))
    for yy in (y0 + 3.4, y1 - 4.6):
        room.rect(x0 + 0.3, yy, x1 - 0.3, yy + 1.2, ramp(stripe, 0.4))
    mask = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(mask).rounded_rectangle((x0 * S, y0 * S, x1 * S, y1 * S), radius=int(1.4 * S), fill=255)
    dark, lit = Image.new("L", room.lay.size, 0), Image.new("L", room.lay.size, 0)
    dd, ldr = ImageDraw.Draw(dark), ImageDraw.Draw(lit)
    for k in range(3):
        fy = y0 + (y1 - y0) * (0.3 + 0.24 * k) + rnd.uniform(-1.5, 1.5)
        a, b = (x0, fy - rnd.uniform(0.5, 2.5)), (x1, fy + rnd.uniform(0.5, 2.5))
        mid = ((a[0] + b[0]) / 2 + rnd.uniform(-3, 3), (a[1] + b[1]) / 2 + rnd.uniform(-1.2, 1.2))
        dd.line([(px * S, py * S) for px, py in (a, mid, b)], fill=85, width=int(1.4 * S))
        ldr.line([(px * S, (py - 1.3) * S) for px, py in (a, mid, b)], fill=60, width=int(0.7 * S))
    for layer, col in ((dark, (16, 10, 6, 255)), (lit, (250, 238, 214, 255))):
        room.lay.paste(Image.new("RGBA", room.lay.size, col), (0, 0),
                       ImageChops.multiply(layer.filter(ImageFilter.GaussianBlur(0.5 * S)), mask))
    room.rect(x0 + 0.4, y0 + 1, x0 + 1.0, y1 - 1, ramp(cols, 0.82))
    room.rect(x1 - 1.0, y0 + 1, x1 - 0.4, y1 - 1, ramp(cols, 0.18))


def clamp(p, box):
    """p moved inside box (x0, y0, x1, y1)."""
    return (min(max(p[0], box[0]), box[2]), min(max(p[1], box[1]), box[3]))


def straw(room, box, n, rnd):
    """n loose straws strewn inside box (x0, y0, x1, y1)."""
    for _ in range(n):
        px, py = rnd.uniform(box[0], box[2]), rnd.uniform(box[1], box[3])
        a = rnd.uniform(0, math.pi)
        ln = rnd.uniform(1.4, 3.6)
        room.line([(px, py), clamp((px + math.cos(a) * ln, py + math.sin(a) * ln * 0.6), box)],
                  ramp(C.STRAW, rnd.choice((0.25, 0.45, 0.65, 0.85))), 0.3)


def straw_wisps(room, x0, y0, x1, y1, n, rnd, out=2.0, box=None):
    """Straw poking out from under the edges of the rect: n wisps, kept
    inside box when given."""
    for _ in range(n):
        side = rnd.randrange(4)
        if side < 2:
            px, py = rnd.uniform(x0 + 1, x1 - 1), (y0 if side == 0 else y1)
            dx, dy = rnd.uniform(-1.2, 1.2), (-1 if side == 0 else 1) * rnd.uniform(0.8, out)
        else:
            px, py = (x0 if side == 2 else x1), rnd.uniform(y0 + 1, y1 - 1)
            dx, dy = (-1 if side == 2 else 1) * rnd.uniform(0.8, out), rnd.uniform(-1.2, 1.2)
        end = (px + dx, py + dy) if box is None else clamp((px + dx, py + dy), box)
        room.line([(px - dx * 0.4, py - dy * 0.4), end], ramp(C.STRAW, rnd.choice((0.55, 0.75, 0.9))), 0.3)


def straw_pallet(room, x, y, w, h, cover, stripe, rnd):
    """A bed on the floor: a plump bag of plain ticking stuffed with straw,
    a dark seam all round it, a few wisps of straw poking out, a linen
    bolster at the head, a homespun blanket over it from 12 px below the top
    (the room's covers rect). Seen from above at a slant like the beds."""
    S = C.S
    tx0, ty0, tx1, ty1 = x + 1.6, y + 2.6, x + w - 1.6, y + h - 1.6
    straw_wisps(room, tx0 + 0.5, ty0 + 0.5, tx1 - 0.5, ty1 - 0.5, int(w + h) // 2, rnd,
                box=(x + 0.4, y + 0.4, x + w - 0.4, y + h - 0.4))
    room.d.rounded_rectangle(((tx0 - 0.6) * S, (ty0 - 0.6) * S, (tx1 + 0.6) * S, (ty1 + 0.6) * S), radius=int(3.6 * S),
                             fill=ramp(C.OAK, 0.08))
    room.d.rounded_rectangle((tx0 * S, ty0 * S, tx1 * S, ty1 * S), radius=int(3.4 * S), fill=ramp(HEMP, 0.42))
    k = tx0 + 2.0
    while k < tx1 - 1.5:
        room.rect(k, ty0 + 1.4, k + 0.5, ty1 - 1.4, ramp(HEMP, 0.32))
        k += 3.2
    room.rect(tx0 + 2, ty1 - 1.6, tx1 - 2, ty1 - 0.4, ramp(HEMP, 0.22))
    # The bolster.
    room.d.rounded_rectangle(((tx0 + 1.0) * S, (y + 4) * S, (tx1 - 1.0) * S, (y + 10.6) * S),
                             radius=int(2.6 * S), fill=ramp(C.LINEN, 0.75))
    room.ellipse(tx0 + 3, y + 4.8, tx1 - 7, y + 7.4, ramp(C.LINEN, 0.95))
    room.rect(tx0 + 2, y + 9.8, tx1 - 2, y + 10.6, ramp(C.LINEN, 0.4))
    # The blanket, the sheet turned down over its top edge.
    blanket(room, x + 4.0, y + 12, x + w - 4.0, y + h - 5.6, cover, stripe, rnd)
    room.rect(x + 4.0, y + 12, x + w - 4.0, y + 13.6, ramp(C.LINEN, 0.88))
    room.rect(x + 4.0, y + 13.1, x + w - 4.0, y + 13.6, ramp(C.LINEN, 0.48))


def grain_sack(room, x, y, w, h, rnd, open_=True):
    """A full hemp sack standing on the floor, slumped: broad at the foot,
    tapering to the neck, creased where it sags. Open with its mouth rolled
    down on a little shelled corn and a wooden scoop in it, or tied at the
    neck with its ears standing up. Drawn for a 16 px wide rect and
    narrowed to fit a narrower one."""
    cx, base = x + w / 2, y + h - 0.6
    k = w / 16.0
    top = y + 7.5 if open_ else y + 4.5
    body = [(cx - 6.8 * k, base), (cx + 6.8 * k, base), (cx + 7.4 * k, base - 3.2), (cx + 6.6 * k, base - 8.5), (cx + 4.4 * k, top + 4.5),
            (cx + 3.0 * k, top + 1.2), (cx - 2.8 * k, top + 1.2), (cx - 4.6 * k, top + 4.5), (cx - 6.8 * k, base - 8.5),
            (cx - 7.6 * k, base - 3.2)]
    room.poly(body, ramp(HEMP, 0.5))
    room.poly([(cx - 7.6 * k, base - 3.2), (cx - 6.8 * k, base - 8.5), (cx - 4.6 * k, top + 4.5), (cx - 2.8 * k, top + 1.2),
               (cx - 1.6 * k, top + 1.2), (cx - 3.6 * k, base - 7), (cx - 4.4 * k, base - 1), (cx - 6.6 * k, base)], ramp(HEMP, 0.7))
    room.poly([(cx + 3.6 * k, top + 3), (cx + 4.4 * k, top + 4.5), (cx + 6.6 * k, base - 8.5), (cx + 7.4 * k, base - 3.2),
               (cx + 6.8 * k, base), (cx + 4.6 * k, base), (cx + 5.4 * k, base - 6)], ramp(HEMP, 0.3))
    # Creases where the cloth sags from the neck over the load.
    for (ax, ay), (bx, by) in (((cx - 1.0 * k, top + 2.5), (cx - 3.6 * k, base - 5.5)), ((cx + 1.4 * k, top + 2.5), (cx + 2.6 * k, base - 8)),
                               ((cx - 5.0 * k, base - 4.5), (cx - 1.0 * k, base - 2.5))):
        room.line([(ax, ay), (bx, by)], ramp(HEMP, 0.26), 0.5)
        room.line([(ax - 0.6, ay), (bx - 0.6, by)], ramp(HEMP, 0.78), 0.3)
    room.rect(cx - 6.2 * k, base - 0.8, cx + 6.4 * k, base, ramp(HEMP, 0.16))
    if open_:
        room.ellipse(cx - 4.6 * k, top - 1.6, cx + 4.6 * k, top + 2.8, ramp(HEMP, 0.8))
        room.ellipse(cx - 3.6 * k, top - 0.9, cx + 3.6 * k, top + 2.0, ramp(HEMP, 0.2))
        room.ellipse(cx - 3.0 * k, top - 0.6, cx + 3.0 * k, top + 1.6, ramp(CORN, 0.62))
        for n in range(7):
            px, py = cx - 2.2 * k + (n % 4) * 1.4 * k, top - 0.2 + (n // 4) * 0.9
            room.rect(px, py, px + 0.5, py + 0.4, ramp(CORN, 0.9 if n % 2 else 0.35))
        room.line([(cx + 0.4 * k, top + 0.6), (cx + 4.2 * k, top - 4.6)], ramp(C.OAK, 0.2), 1.0)
        room.line([(cx + 0.4 * k, top + 0.6), (cx + 4.2 * k, top - 4.6)], ramp(C.OAK, 0.72), 0.55)
        room.ellipse(cx - 1.8 * k, top - 0.6, cx + 1.4 * k, top + 1.6, ramp(C.OAK, 0.25))
        room.ellipse(cx - 1.4 * k, top - 0.4, cx + 1.0 * k, top + 1.2, ramp(C.OAK, 0.6))
    else:
        room.poly([(cx - 2.8 * k, top + 1.4), (cx + 3.0 * k, top + 1.4), (cx + 1.4 * k, top - 1.4), (cx - 1.2 * k, top - 1.4)],
                  ramp(HEMP, 0.58))
        room.rect(cx - 1.6 * k, top - 1.6, cx + 1.8 * k, top - 0.6, ramp(ROPE, 0.3))
        room.poly([(cx - 1.2 * k, top - 1.4), (cx + 0.2 * k, top - 1.4), (cx - 0.8 * k, top - 4.4), (cx - 2.6 * k, top - 3.8)], ramp(HEMP, 0.74))
        room.poly([(cx + 0.2 * k, top - 1.4), (cx + 1.6 * k, top - 1.4), (cx + 3.0 * k, top - 3.6), (cx + 1.0 * k, top - 4.4)], ramp(HEMP, 0.42))


# ---------------------------------------------------------------- the tent

def tent_shell(room, w, h, back, apex, shoulders, eaves, rnd):
    """The player's wedge tent seen from inside, looking at its closed back
    end: the back gable (apex down to the shoulders on the ground, y back)
    in the shade of the canvas, made of two flaps, the right one lapped
    over the left beside the pole and tied shut in four places, a narrow crack of daylight
    at the very top; the back pole; the two roof slopes coming toward us on
    either side, the left one in the sun, with their sewn seams and the
    canvas sagging between them; the ridge pole overhead; trodden grass and
    earth between the eaves (the shoulders out to eaves, at the room's
    foot)."""
    S = C.S
    (lx, ly), (rx, ry) = shoulders
    (lex, ley), (rex, rey) = eaves
    ax, ay = apex
    earth(room, 0, back - 2, w, h, rnd)
    # Flattened grass along the walls, worn to earth down the middle.
    for _ in range(int(w * (h - back) / 5)):
        px, py = rnd.uniform(0, w), rnd.uniform(back, h)
        half = (lx - ax) + (lex - lx) * (py - back) / (h - back)
        if rnd.random() < min(1.0, abs((px - ax) / half) ** 2):
            a = math.radians(rnd.uniform(-70, 70))
            room.line([(px, py), (px + math.sin(a) * 2.2, py - math.cos(a) * 1.0)], ramp(C.HERB, rnd.uniform(0.3, 0.6)), 0.3)

    def canvas(poly, base, fall):
        """Fill poly with canvas, base at the top darkening by fall toward
        the foot of the room, with a faint weave; returns the poly's mask."""
        mask = Image.new("L", room.lay.size, 0)
        ImageDraw.Draw(mask).polygon([(px * S, py * S) for px, py in poly], fill=255)
        layer = Image.new("RGBA", room.lay.size, (0, 0, 0, 0))
        ld = ImageDraw.Draw(layer)
        for yy in range(0, h * 4):
            ld.rectangle((0, yy * S / 4, room.lay.size[0], (yy + 1) * S / 4), fill=ramp(CANVAS, base - fall * yy / (h * 4)))
        for _ in range(int(w * h * 0.5)):
            px, py = rnd.uniform(0, w), rnd.uniform(0, h)
            t = base - fall * py / h
            ld.rectangle((px * S, py * S, (px + 0.6) * S, (py + 0.25) * S), fill=ramp(CANVAS, t + rnd.choice((-0.05, 0.04))))
        room.lay.paste(layer, (0, 0), mask)
        return mask

    left = [(0, 0), (ax, 0), (ax, ay), (lx, ly), (lex, ley), (0, h)]
    right = [(ax, 0), (w, 0), (w, h), (rex, rey), (rx, ry), (ax, ay)]
    lmask = canvas(left, 0.74, 0.26)
    rmask = canvas(right, 0.5, 0.2)
    # Seams down each slope, fanning from the ridge to the eaves, and the
    # canvas sagging a little darker between them.
    for side, (sx, sy), (ex, ey), tone, mask in ((-1, (lx, ly), (lex, ley), 0.56, lmask),
                                                 (1, (rx, ry), (rex, rey), 0.34, rmask)):
        sag = Image.new("L", room.lay.size, 0)
        sd = ImageDraw.Draw(sag)
        prev = None
        for k in range(0, 7):
            t = k / 5.2
            top = (ax + side * 0.5, ay - 34 * t * t - 4)
            foot = lerp((sx, sy), (ex, ey), t * 1.6)
            if prev:
                mt, mf = lerp(prev[0], top, 0.5), lerp(prev[1], foot, 0.5)
                sd.line([(mt[0] * S, mt[1] * S), (mf[0] * S, mf[1] * S)], fill=60, width=int(3.0 * S))
            if k:
                room.line([top, foot], ramp(CANVAS, tone - 0.16), 0.45)
                room.line([(top[0] - side * 0.6, top[1]), (foot[0] - side * 0.6, foot[1])], ramp(CANVAS, tone + 0.14), 0.3)
            prev = (top, foot)
        sag = ImageChops.multiply(sag.filter(ImageFilter.GaussianBlur(2 * S)), mask)
        room.lay.paste(Image.new("RGBA", room.lay.size, C.B.rgb(CANVAS[1])), (0, 0), sag)
        # The eave, where the slope meets the ground: a dark hem.
        room.line([(sx, sy), lerp((sx, sy), (ex, ey), 1.4)], ramp(CANVAS, 0.12), 0.9)
    # The back gable: two flaps in the shade, the light from the left slope
    # falling across the left one.
    lap = ax + 6.0          # where the right flap's edge lies over the left
    gmask = Image.new("L", room.lay.size, 0)
    ImageDraw.Draw(gmask).polygon([(ax * S, ay * S), (rx * S, ry * S), (lx * S, ly * S)], fill=255)
    layer = Image.new("RGBA", room.lay.size, (0, 0, 0, 0))
    ld = ImageDraw.Draw(layer)
    for yy in range(int(ay * 4), int(ly * 4)):
        f = (yy / 4 - ay) / (ly - ay)
        ld.rectangle((0, yy * S / 4, lap * S, (yy + 1) * S / 4), fill=ramp(CANVAS, 0.56 - 0.18 * f))
        ld.rectangle((lap * S, yy * S / 4, room.lay.size[0], (yy + 1) * S / 4), fill=ramp(CANVAS, 0.66 - 0.18 * f))
    for _ in range(int((rx - lx) * (ly - ay) / 10)):
        px, py = rnd.uniform(lx, rx), rnd.uniform(ay, ly)
        f = (py - ay) / (ly - ay)
        base = 0.56 if px < lap else 0.66
        ld.rectangle((px * S, py * S, (px + 0.6) * S, (py + 0.25) * S), fill=ramp(CANVAS, base - 0.17 * f + rnd.choice((-0.05, 0.04))))
    room.lay.paste(layer, (0, 0), gmask)
    room.line([(ax, ay), (lx, ly)], ramp(CANVAS, 0.85), 0.4)
    room.line([(ax, ay), (rx, ry)], ramp(CANVAS, 0.25), 0.5)
    # The lapped edge of the right flap: a lit hem, its shadow on the left
    # flap, and the ties across it in pairs.
    room.rect(lap - 2.0, ay + 7, lap - 0.3, ly, ramp(CANVAS, 0.12))
    room.rect(lap - 0.3, ay + 7, lap + 0.7, ly, ramp(CANVAS, 0.9))
    room.line([(ax + 1.4, ay + 2), (lap - 0.3, ay + 7)], ramp(CANVAS, 0.9), 0.5)
    for k in range(4):
        ty = ay + 13 + k * 8.6
        if ty < ly - 4:
            room.line([(lap - 5.0, ty + 0.4), (lap + 4.8, ty - 0.4)], ramp(C.SOOT, 0.3), 1.5)
            room.line([(lap - 4.6, ty + 0.4), (lap + 4.4, ty - 0.4)], ramp(ROPE, 0.5), 0.7)
            for ex in (-2.2, 2.0):
                room.line([(lap, ty + 0.6), (lap + ex, ty + 4.8)], ramp(C.SOOT, 0.3), 0.9)
                room.line([(lap, ty + 0.6), (lap + ex * 0.9, ty + 4.4)], ramp(ROPE, 0.45), 0.35)
            room.ellipse(lap - 1.7, ty - 1.6, lap + 1.7, ty + 1.6, ramp(C.SOOT, 0.3))
            room.ellipse(lap - 1.2, ty - 1.1, lap + 1.2, ty + 1.1, ramp(ROPE, 0.5))
            room.rect(lap - 0.7, ty - 0.8, lap + 0.2, ty - 0.2, ramp(ROPE, 0.95))
    # Daylight through the crack at the very top, where the flaps part.
    room.poly([(ax + 1.4, ay + 1.6), (ax + 3.0, ay + 3.0), (ax + 2.0, ay + 6.0)], ramp(C.DAYLIGHT, 0.85))
    room.line([(lx, ly), (rx, ry)], ramp(CANVAS, 0.2), 0.7)
    # The back pole, from the ground up to the apex, and the ridge pole
    # coming toward us overhead, each with a dark edge.
    room.rect(ax - 1.7, ay - 1, ax + 1.7, back + 1, ramp(C.SOOT, 0.3))
    room.rect(ax - 1.2, ay - 1, ax + 1.2, back + 1, ramp(WOOD, 0.45))
    room.rect(ax - 1.2, ay - 1, ax - 0.4, back + 1, ramp(WOOD, 0.75))
    room.rect(ax + 0.6, ay - 1, ax + 1.2, back + 1, ramp(WOOD, 0.22))
    room.poly([(ax - 1.5, ay), (ax + 1.5, ay), (ax + 3.1, 0), (ax - 3.1, 0)], ramp(C.SOOT, 0.3))
    room.poly([(ax - 1.0, ay), (ax + 1.0, ay), (ax + 2.6, 0), (ax - 2.6, 0)], ramp(WOOD, 0.4))
    room.poly([(ax - 1.0, ay), (ax - 0.3, ay), (ax - 1.2, 0), (ax - 2.6, 0)], ramp(WOOD, 0.68))
    room.ellipse(ax - 2.2, back - 0.6, ax + 3.4, back + 1.8, ramp(EARTH, 0.15))
    # Where the ground meets the back wall: a dark line, tufts at the corners.
    room.rect(lx, back - 0.6, rx, back + 0.6, ramp(EARTH, 0.1))
    for ex, ey in ((lx, ly), (rx, ry)):
        for _ in range(4):
            tx, ty = ex + rnd.uniform(-2, 2), ey + rnd.uniform(-0.5, 1)
            for _k in range(4):
                a = math.radians(rnd.uniform(-40, 40))
                room.line([(tx, ty), (tx + math.sin(a) * 2.4, ty - math.cos(a) * 2.4)],
                          ramp(C.HERB, rnd.uniform(0.3, 0.6)), 0.3)


def pole_hook(room, px, y, reach):
    """An iron hook driven into a pole at (px, y), reaching out to the
    right: what the tent's lantern hangs from."""
    room.line([(px, y), (px + reach, y - 0.4), (px + reach, y + 0.8)], ramp(C.SOOT, 0.25), 0.6)
    room.line([(px, y - 0.2), (px + reach, y - 0.6)], ramp(C.IRON, 0.6), 0.25)


def bedroll(room, x, y, w, h, rnd):
    """The camp bed laid along the tent's left wall: a ground cloth of
    oiled linen, dark and yellowed, showing all round; a red coat rolled up
    at the head for a pillow, its rolled end and a cuff showing; a grey wool
    blanket over it from 12 px below the top (the room's covers rect), its
    foot corner turned back to show the paler underside."""
    S = C.S
    gx0, gy0, gx1, gy1 = x + 0.6, y + 1.4, x + w - 0.4, y + h - 0.4
    room.poly([(gx0 + 0.8, gy0), (gx1, gy0 + 0.6), (gx1 - 0.3, gy1), (gx0, gy1 - 0.4)], ramp(OILCLOTH, 0.5))
    room.line([(gx0 + 0.8, gy0), (gx1, gy0 + 0.6)], ramp(OILCLOTH, 0.85), 0.4)
    room.line([(gx0 + 0.8, gy0), (gx0, gy1 - 0.4)], ramp(OILCLOTH, 0.75), 0.4)
    room.line([(gx0, gy1 - 0.4), (gx1 - 0.3, gy1)], ramp(OILCLOTH, 0.15), 0.6)
    straw_wisps(room, gx0 + 0.5, gy0 + 0.5, gx1 - 0.5, gy1 - 0.5, 18, rnd, 1.6,
                box=(x + 0.4, y + 0.4, x + w - 0.4, y + h - 0.4))
    # The rolled coat: a cylinder lying across, its rolled end at the left
    # showing the spiral of the cloth, a cuff and a pewter button at the right.
    c0, c1, ct, cb = x + 2.6, x + w - 2.6, y + 3.0, y + 11.0
    room.d.rounded_rectangle((c0 * S, ct * S, c1 * S, cb * S), radius=int(3.0 * S), fill=ramp(C.MADDER, 0.45))
    room.rect(c0 + 3, ct + 0.6, c1 - 3, ct + 2.4, ramp(C.MADDER, 0.72))
    room.rect(c0 + 3, cb - 1.6, c1 - 2, cb - 0.6, ramp(C.MADDER, 0.2))
    room.ellipse(c0, ct, c0 + 5.6, cb, ramp(C.MADDER, 0.6))
    room.ellipse(c0 + 1.0, ct + 1.2, c0 + 4.6, cb - 1.2, ramp(C.MADDER, 0.3))
    room.ellipse(c0 + 1.9, ct + 2.4, c0 + 3.7, cb - 2.4, ramp(C.MADDER, 0.6))
    room.rect(c1 - 6, ct + 1.6, c1 - 2.2, cb - 1.6, ramp(C.MADDER, 0.28))
    room.rect(c1 - 5.4, ct + 3.0, c1 - 4.2, ct + 4.2, ramp(C.PEWTER, 0.85))
    # The blanket.
    b0, b1, bt, bb = x + 1.6, x + w - 1.4, y + 12.4, y + h - 2.2
    blanket(room, b0, bt, b1, bb, WOOL, C.INDIGO, rnd)
    room.rect(b0, bt, b1, bt + 2.0, ramp(WOOL, 0.82))
    room.rect(b0, bt + 1.6, b1, bt + 2.0, ramp(WOOL, 0.3))
    # The foot corner turned back: the ground cloth where it was, the
    # underside folded over, its fold line in shade.
    fold = 9.0
    room.poly([(b1 + 0.1, bb - fold), (b1 + 0.1, bb + 0.1), (b1 - fold, bb + 0.1)], ramp(OILCLOTH, 0.5))
    room.poly([(b1 - 0.2, bb - fold), (b1 - fold, bb - 0.2), (b1 - fold * 0.9, bb - fold * 0.9)], ramp(WOOL, 0.86))
    room.line([(b1 - 0.2, bb - fold), (b1 - fold, bb - 0.2)], ramp(WOOL, 0.12), 0.45)
    room.line([(b1 - fold * 0.9, bb - fold * 0.9), (b1 - 0.2, bb - fold)], ramp(WOOL, 0.95), 0.3)


def snapsack(room, x, y, w, h):
    """A traveller's snapsack slumped on the ground, in a dark outline: a
    soft linen bag, its brown leather flap with a rounded foot buckled down
    over the front, a tin cup with a handle tied beside it."""
    S = C.S
    cx, base = x + 5.8, y + h - 1
    room.ellipse(cx - 5.6, base - 6.9, cx + 5.6, base + 0.4, ramp(C.SOOT, 0.3))
    room.d.rounded_rectangle(((cx - 5.1) * S, (base - 12.2) * S, (cx + 5.1) * S, (base - 2.0) * S), radius=int(3.4 * S),
                             fill=ramp(C.SOOT, 0.3))
    room.ellipse(cx - 5.3, base - 6.5, cx + 5.3, base, ramp(CANVAS, 0.5))
    room.d.rounded_rectangle(((cx - 4.8) * S, (base - 11.6) * S, (cx + 4.8) * S, (base - 2.4) * S), radius=int(3.0 * S),
                             fill=ramp(CANVAS, 0.58))
    room.ellipse(cx - 4.8, base - 10.4, cx - 1.9, base - 1.0, ramp(CANVAS, 0.78))
    room.ellipse(cx + 2.5, base - 9.0, cx + 5.1, base - 0.6, ramp(CANVAS, 0.34))
    room.rect(cx - 3.9, base - 0.8, cx + 4.1, base, ramp(CANVAS, 0.2))
    room.ellipse(cx - 4.6, base - 12.0, cx + 4.6, base - 4.6, ramp(LEATHER, 0.45))
    room.rect(cx - 4.6, base - 12.0, cx + 4.6, base - 8.6, ramp(LEATHER, 0.45))
    room.rect(cx - 4.2, base - 12.0, cx + 4.2, base - 11.2, ramp(LEATHER, 0.95))
    room.ellipse(cx - 3.9, base - 11.2, cx - 1.2, base - 7.6, ramp(LEATHER, 0.7))
    room.rect(cx - 0.5, base - 5.8, cx + 0.5, base - 2.6, ramp(LEATHER, 0.25))
    room.rect(cx - 0.9, base - 5.0, cx + 0.9, base - 3.8, ramp(C.IRON, 0.75))
    # The tin cup, on its side of the bag.
    kx = cx + 5.4
    room.rect(kx - 0.4, base - 5.4, kx + 3.6, base + 0.2, ramp(C.SOOT, 0.3))
    room.rect(kx, base - 5.0, kx + 3.2, base - 0.2, ramp(C.PEWTER, 0.55))
    room.rect(kx, base - 5.0, kx + 1.0, base - 0.2, ramp(C.PEWTER, 0.9))
    room.rect(kx + 2.4, base - 5.0, kx + 3.2, base - 0.2, ramp(C.PEWTER, 0.3))
    room.ellipse(kx, base - 5.6, kx + 3.2, base - 4.4, ramp(C.PEWTER, 0.2))
    room.d.arc(((kx + 2.4) * S, (base - 4.4) * S, (kx + 4.6) * S, (base - 1.4) * S), 270, 90, fill=ramp(C.PEWTER, 0.6),
               width=int(0.5 * S))
