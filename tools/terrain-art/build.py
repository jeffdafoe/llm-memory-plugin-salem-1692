"""Paints salem's ground (LLM-967): every pixel is drawn here, no pack art.

    python tools/terrain-art/build.py
    python tools/terrain-art/build.py --check   # cobble cells exact, no wrap seams, leaves keep colour

Writes, under client/assets/terrain/:
  ground-<season>.png  one atlas per season (summer, autumn, winter, snowy):
                       a 512x512 texture per terrain, in LAYER_ORDER, 3 across
  edge-noise.png       the wobble on every terrain edge (grey, 128 = none)
  water-sparkles.png   12 glint frames, 32x32, 4 across
and client/scripts/terrain_pack.gd (the layout the renderer reads).

Rerunning writes the same files. After a change, run `godot --headless
--editor --quit` in client/ so Godot writes the .import sidecars, and commit
them with the PNGs.

How a texture is made: each terrain is painted on a 2048x2048 canvas with real
shapes (tapered grass blades, clover, flowers, shaded pebbles, cobbles lit from
the upper left, ripples), shrunk by area average to 512x512, and reduced to 16
colours. Autumn leaves are painted on a layer of their own and laid on after
the reduction, in their own colours. Every shape near a canvas edge is drawn
again across the edge, and the shrink factor is exact, so the texture wraps. It repeats every 16 tiles (32 px
tiles); the shader samples it by world position, so tiles show no grid.
"""

import math
import os
import random
import sys

from PIL import Image, ImageDraw, ImageFilter

ROOT = os.path.normpath(os.path.join(os.path.dirname(__file__), "..", ".."))
ASSET_DIR = os.path.join(ROOT, "client", "assets", "terrain")
PACK_SCRIPT = os.path.join(ROOT, "client", "scripts", "terrain_pack.gd")

W = 2048          # painting canvas
TEX = 512         # texture size: 16 tiles of 32 px
TILE_PX = 32
ATLAS_COLS = 3
COLOURS = 16
# Scale of the cobble pattern: stone size, joints and shading offsets.
STONE = 0.75
COBBLE_SEED = 4

# terrain.gd ids
DIRT, LIGHT_GRASS, DARK_GRASS, COBBLESTONE, SHALLOW_WATER, DEEP_WATER = 1, 2, 3, 4, 5, 6
# Bottom to top: land overlaps water, dark grass grows over light, the cobbled
# paths lie on everything.
LAYER_ORDER = [DEEP_WATER, SHALLOW_WATER, DIRT, LIGHT_GRASS, DARK_GRASS, COBBLESTONE]
SEASONS = ["summer", "autumn", "winter", "snowy"]


def rgb(h, a=255):
    return (int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16), a)


def mix(c1, c2, t):
    return tuple(int(c1[i] + (c2[i] - c1[i]) * t) for i in range(3)) + (255,)


# ---------------------------------------------------------------- canvas

class Canvas:
    def __init__(self, base):
        self.img = Image.new("RGBA", (W, W), rgb(base))

    @staticmethod
    def layer():
        return Image.new("RGBA", (W, W), (0, 0, 0, 0))

    @staticmethod
    def wrap_draw(draw, fn, x, y, r):
        """fn(draw, x, y), plus a copy across every canvas edge the shape touches."""
        xs = [x] + ([x + W] if x - r < 0 else []) + ([x - W] if x + r > W else [])
        ys = [y] + ([y + W] if y - r < 0 else []) + ([y - W] if y + r > W else [])
        for xx in xs:
            for yy in ys:
                fn(draw, xx, yy)

    def blend(self, layer, blur=0):
        if blur:
            layer = wrap_blur(layer, blur)
        self.img = Image.alpha_composite(self.img, layer)


def wrap_blur(layer, radius):
    """Gaussian blur that wraps at the canvas edges."""
    pad = int(radius * 3)
    big = Image.new("RGBA", (W + 2 * pad, W + 2 * pad))
    for dx in (-1, 0, 1):
        for dy in (-1, 0, 1):
            big.paste(layer, (pad + dx * W, pad + dy * W))
    big = big.filter(ImageFilter.GaussianBlur(radius))
    return big.crop((pad, pad, pad + W, pad + W))


def tone_blobs(cv, rnd, colors, count, rmin, rmax, alpha, blur):
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        r = rnd.uniform(rmin, rmax)
        rx = r * rnd.uniform(0.7, 1.3)
        col = rnd.choice(colors)
        cv.wrap_draw(d, lambda dr, xx, yy: dr.ellipse((xx - rx, yy - r, xx + rx, yy + r), fill=rgb(col, alpha)), x, y, max(r, rx))
    cv.blend(lay, blur)


def speckle(cv, rnd, colors, count, rmin, rmax, alpha=255):
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        r = rnd.uniform(rmin, rmax)
        col = rnd.choice(colors)
        cv.wrap_draw(d, lambda dr, xx, yy: dr.ellipse((xx - r, yy - r, xx + r, yy + r), fill=rgb(col, alpha)), x, y, r)
    cv.blend(lay)


def tone_map(rnd):
    """Grey map of where turf is lit (bright) or shaded; wraps."""
    cv = Canvas("808080")
    d = ImageDraw.Draw(cv.img)
    for _ in range(140):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        r = rnd.uniform(90, 260)
        v = rnd.choice((40, 70, 190, 230))
        cv.wrap_draw(d, lambda dr, xx, yy: dr.ellipse((xx - r, yy - r * 0.8, xx + r, yy + r * 0.8), fill=(v, v, v, 90)), x, y, r)
    return wrap_blur(cv.img, 60)


# ---------------------------------------------------------------- details

def blades(cv, rnd, count, length, lean, width, dark, mid, light, tones):
    """Grass blades, back to front so lower blades overlap upper ones. Each is
    a tapered quad: a shaded root, a lit tip. tones brightens blades where the
    turf is lit."""
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    roots = sorted(((rnd.uniform(0, W), rnd.uniform(0, W)) for _ in range(count)), key=lambda p: p[1])
    lit_px = tones.load()
    # Every copy of a blade, wrapped ones included, as (root y, root x, draw).
    # They are drawn in order of their own root y: a copy wrapped across the
    # bottom edge must cover the blades above it, as it would on the next
    # repeat of the texture, or the texture shows a seam where it wraps.
    copies = []
    for x, y in roots:
        L = rnd.uniform(*length)
        a = math.radians(rnd.uniform(-lean, lean))
        w = rnd.uniform(*width)
        lit = lit_px[int(x) % W, int(y) % W][0] / 255.0
        t = min(1.0, max(0.0, lit * 0.8 + rnd.uniform(-0.25, 0.35)))
        root = mix(rgb(dark), rgb(mid), t * 0.6)
        tip = mix(rgb(mid), rgb(light), t)
        tx = math.sin(a) * L
        ty = -math.cos(a) * L

        def draw_blade(dr, bx, by, tx=tx, ty=ty, w=w, root=root, tip=tip):
            mx = tx * 0.5
            my = ty * 0.5
            dr.polygon([(bx - w / 2, by), (bx + w / 2, by), (bx + mx + w * 0.3, by + my), (bx + mx - w * 0.3, by + my)], fill=root)
            dr.polygon([(bx + mx - w * 0.3, by + my), (bx + mx + w * 0.3, by + my), (bx + tx, by + ty)], fill=tip)

        cv.wrap_draw(None, lambda _d, bx, by, fn=draw_blade: copies.append((by, bx, fn)), x, y, L + w)
    for by, bx, fn in sorted(copies, key=lambda c: c[0]):
        fn(d, bx, by)
    cv.blend(lay)


def clover(cv, rnd, count, leaf, dark):
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        cx = rnd.uniform(0, W)
        cy = rnd.uniform(0, W)
        for _ in range(rnd.randint(3, 7)):
            x = cx + rnd.uniform(-40, 40)
            y = cy + rnd.uniform(-30, 30)

            def draw_leaf(dr, bx, by):
                for ang in (90, 210, 330):
                    ox = math.cos(math.radians(ang)) * 7
                    oy = -math.sin(math.radians(ang)) * 7
                    dr.ellipse((bx + ox - 7, by + oy - 7, bx + ox + 7, by + oy + 7), fill=rgb(leaf))
                dr.ellipse((bx - 3, by + 4, bx + 3, by + 10), fill=rgb(dark))

            cv.wrap_draw(d, draw_leaf, x, y, 20)
    cv.blend(lay)


def flowers(cv, rnd, count, petals, centre):
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        cx = rnd.uniform(0, W)
        cy = rnd.uniform(0, W)
        col = rnd.choice(petals)
        for _ in range(rnd.randint(1, 4)):
            x = cx + rnd.uniform(-50, 50)
            y = cy + rnd.uniform(-40, 40)

            def draw_flower(dr, bx, by, col=col):
                dr.ellipse((bx - 9, by - 9, bx + 9, by + 9), fill=rgb(col))
                dr.ellipse((bx - 4, by - 4, bx + 4, by + 4), fill=rgb(centre))

            cv.wrap_draw(d, draw_flower, x, y, 12)
    cv.blend(lay)


def fallen_leaves(rnd, count, colours):
    """Autumn: small leaves lying flat, alone on a transparent layer. shrink()
    lays them over the ground after the ground's colours are reduced; drawn into
    the ground, every leaf shared one muddy brown and read as litter."""
    lay = Canvas.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        r = rnd.uniform(11, 17)
        a = rnd.uniform(0, math.tau)
        col = rnd.choice(colours)
        pts = [(math.cos(a) * r, math.sin(a) * r), (math.cos(a + 1.7) * r * 0.45, math.sin(a + 1.7) * r * 0.45),
               (math.cos(a + math.pi) * r, math.sin(a + math.pi) * r), (math.cos(a - 1.7) * r * 0.45, math.sin(a - 1.7) * r * 0.45)]

        def draw_leaf(dr, bx, by, pts=pts, col=col):
            dr.polygon([(bx + px, by + py) for px, py in pts], fill=rgb(col))

        Canvas.wrap_draw(d, draw_leaf, x, y, r)
    return lay


def tufts(cv, rnd, count, dark, light, shadow):
    """Small clumps of a few short blades, each with a soft shadow at its foot."""
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        shapes = []
        for _ in range(rnd.randint(3, 6)):
            a = math.radians(rnd.uniform(-35, 35))
            L = rnd.uniform(14, 26)
            ox = rnd.uniform(-8, 8)
            shapes.append((ox, math.sin(a) * L, -math.cos(a) * L, rnd.choice((dark, light))))

        def draw_tuft(dr, bx, by, shapes=shapes):
            dr.ellipse((bx - 16, by - 4, bx + 16, by + 6), fill=rgb(shadow, 160))
            for ox, tx, ty, col in shapes:
                dr.polygon([(bx + ox - 3, by), (bx + ox + 3, by), (bx + ox + tx, by + ty)], fill=rgb(col))

        cv.wrap_draw(d, draw_tuft, x, y, 40)
    cv.blend(lay)


def pebbles(cv, rnd, count, rmin, rmax, stones, lit, shadow):
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        r = rnd.uniform(rmin, rmax)
        ry = r * rnd.uniform(0.6, 0.9)
        col = rnd.choice(stones)

        def draw_pebble(dr, bx, by, r=r, ry=ry, col=col):
            dr.ellipse((bx - r + 4, by - ry + 6, bx + r + 4, by + ry + 6), fill=rgb(shadow, 150))
            dr.ellipse((bx - r, by - ry, bx + r, by + ry), fill=rgb(col))
            dr.ellipse((bx - r * 0.7, by - ry * 0.85, bx + r * 0.2, by - ry * 0.1), fill=rgb(lit))

        cv.wrap_draw(d, draw_pebble, x, y, r + 8)
    cv.blend(lay)


def cracks(cv, rnd, count, col):
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        pts = [(0.0, 0.0)]
        ang = rnd.uniform(0, math.tau)
        for _ in range(rnd.randint(3, 7)):
            ang += rnd.uniform(-0.8, 0.8)
            step = rnd.uniform(12, 30)
            pts.append((pts[-1][0] + math.cos(ang) * step, pts[-1][1] + math.sin(ang) * step))
        cv.wrap_draw(d, lambda dr, bx, by, pts=pts: dr.line([(bx + px, by + py) for px, py in pts], fill=rgb(col, 200), width=5), x, y, 200)
    cv.blend(lay)


def ripples(cv, rnd, count, col, alpha, size):
    lay = cv.layer()
    d = ImageDraw.Draw(lay)
    for _ in range(count):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        w = rnd.uniform(*size)
        h = w * rnd.uniform(0.25, 0.4)
        start = rnd.uniform(190, 220)
        end = rnd.uniform(320, 350)
        cv.wrap_draw(d, lambda dr, bx, by, w=w, h=h, s=start, e=end: dr.arc((bx - w, by - h, bx + w, by + h), s, e, fill=rgb(col, alpha), width=5), x, y, w)
    cv.blend(lay)


# ---------------------------------------------------------------- terrains
# Each painter takes the season's palette dict and a seed. The seed is the
# terrain's, not the season's, so shapes sit in the same places in every
# season and a season change only recolours the ground.

def paint_grass(seed, pal, dense):
    rnd = random.Random(seed)
    tones = tone_map(rnd)
    cv = Canvas(pal["base"])
    tone_blobs(cv, rnd, [pal["shade"], pal["lit"]], 160, 80, 240, 70, 50)
    blades(cv, rnd, 52000 if dense else 40000, (26, 54) if dense else (20, 44), 28, (6, 9), pal["dark"], pal["mid"], pal["light"], tones)
    clover(cv, rnd, 18 if dense else 40, pal["clover"], pal["dark"])
    blades(cv, rnd, 9000, (16, 30), 30, (5, 7), pal["dark"], pal["mid"], pal["light"], tones)
    flower_rnd = random.Random(seed + 500)
    if pal.get("petals"):
        flowers(cv, flower_rnd, 20 if dense else 70, pal["petals"], pal["centre"])
    return cv.img


def paint_dirt(seed, pal):
    rnd = random.Random(seed)
    cv = Canvas(pal["base"])
    tone_blobs(cv, rnd, [pal["damp"], pal["dry"], pal["dry"]], 220, 60, 220, 80, 45)
    speckle(cv, rnd, [pal["dark"], pal["light"], pal["damp"]], 26000, 2.5, 5)
    cracks(cv, rnd, 60, pal["dark"])
    pebbles(cv, rnd, 900, 7, 16, pal["stones"], pal["stone_lit"], pal["dark"])
    pebbles(cv, rnd, 90, 16, 28, pal["stones"], pal["stone_lit"], pal["dark"])
    return cv.img


def _clip(poly, nx, ny, limit):
    """Sutherland-Hodgman: keep the part of poly where x*nx + y*ny <= limit."""
    out = []
    for i in range(len(poly)):
        ax, ay = poly[i]
        bx, by = poly[(i + 1) % len(poly)]
        da = ax * nx + ay * ny - limit
        db = bx * nx + by * ny - limit
        if da <= 0:
            out.append((ax, ay))
        if (da <= 0) != (db <= 0):
            t = da / (da - db)
            out.append((ax + (bx - ax) * t, ay + (by - ay) * t))
    return out


def _chaikin(poly, rounds):
    """Round a polygon's corners by corner cutting."""
    for _ in range(rounds):
        out = []
        for i in range(len(poly)):
            ax, ay = poly[i]
            bx, by = poly[(i + 1) % len(poly)]
            out.append((ax * 0.75 + bx * 0.25, ay * 0.75 + by * 0.25))
            out.append((ax * 0.25 + bx * 0.75, ay * 0.25 + by * 0.75))
        poly = out
    return poly


def _stone_sites(rnd, rmin, rmax, tries):
    """Stone centres by dart throwing on the wrapping canvas: each stone has
    its own size, and no two sit closer than their sizes allow, so big and
    small stones pack together with no rows."""
    cell = 2 * rmax
    n = int(W // cell)
    grid = {}
    sites = []
    for _ in range(tries):
        x = rnd.uniform(0, W)
        y = rnd.uniform(0, W)
        r = rnd.uniform(rmin, rmax)
        gx = int(x // cell) % n
        gy = int(y // cell) % n
        ok = True
        for dx in (-1, 0, 1):
            for dy in (-1, 0, 1):
                for j in grid.get(((gx + dx) % n, (gy + dy) % n), ()):
                    sx, sy, sr = sites[j]
                    ddx = (x - sx + W / 2) % W - W / 2
                    ddy = (y - sy + W / 2) % W - W / 2
                    if math.hypot(ddx, ddy) < (r + sr) * 0.92:
                        ok = False
                        break
                if not ok:
                    break
            if not ok:
                break
        if ok:
            grid.setdefault((gx, gy), []).append(len(sites))
            sites.append((x, y, r))
    return sites, grid, cell, n


def _stone_cell(i, sites, grid, cell, n, gap):
    """Stone i's Voronoi cell around its centre, each bisector moved in by
    half the joint. Clips against the 5x5 grid block first; that block holds
    every centre within 2 * cell, and a centre farther than twice the cell's
    reach plus the joint cannot cut it. If the cell reaches farther than the
    block guarantees, it is clipped again against every centre, so the cell
    is exact however sparse the stones."""
    x, y, r = sites[i]

    def clip_against(candidates, reach):
        poly = [(-reach, -reach), (reach, -reach), (reach, reach), (-reach, reach)]
        for j in candidates:
            if j == i or not poly:
                continue
            sx, sy, _ = sites[j]
            vx = (sx - x + W / 2) % W - W / 2
            vy = (sy - y + W / 2) % W - W / 2
            dist = math.hypot(vx, vy)
            if dist == 0:
                continue
            poly = _clip(poly, vx / dist, vy / dist, dist / 2 - gap / 2)
        return poly

    gx = int(x // cell) % n
    gy = int(y // cell) % n
    block = [j for dx in (-2, -1, 0, 1, 2) for dy in (-2, -1, 0, 1, 2) for j in grid.get(((gx + dx) % n, (gy + dy) % n), ())]
    poly = clip_against(block, 4 * r)
    reach = max((math.hypot(px, py) for px, py in poly), default=0.0)
    if 2 * reach + gap > 2 * cell:
        poly = clip_against(range(len(sites)), W / 2)
    return poly


def paint_cobble(seed, pal):
    """Cobbles packed tight: each stone is its centre's Voronoi cell, pulled
    in from its neighbours by the joint and rounded, so stones of mixed sizes
    fit together with thin earthy joints. Three flat tones per stone: a dark
    lower-right edge, the body, a lighter patch up-left (a blur would band
    into rings once cut to a few colours), and a shadow into the joint.
    STONE scales the whole pattern; Jeff, 2026-11-11: a quarter smaller."""
    s = STONE
    rnd = random.Random(seed)
    cv = Canvas(pal["gap"])
    speckle(cv, rnd, [pal["gap_dark"], pal["moss"]], int(9000 / (s * s)), 3 * s, 7 * s)
    gap = 5.0 * s
    # The centres have their own seed, so --check can rebuild them.
    sites, grid, cell, n = _stone_sites(random.Random(seed + 1000), 17 * s, 30 * s, int(60000 / (s * s)))
    shadow = cv.layer()
    body = cv.layer()
    sheen = cv.layer()
    ds = ImageDraw.Draw(shadow)
    db = ImageDraw.Draw(body)
    dh = ImageDraw.Draw(sheen)
    for i, (x, y, r) in enumerate(sites):
        poly = _stone_cell(i, sites, grid, cell, n, gap)
        if len(poly) < 3:
            continue
        poly = _chaikin(poly, 2)
        size = max(math.hypot(px, py) for px, py in poly)
        tone = rnd.uniform(-0.08, 0.08)
        base = mix(rgb(rnd.choice(pal["stones"])), rgb(pal["warm"]), rnd.uniform(0, 0.3))
        base = tuple(max(0, min(255, int(c * (1 + tone)))) for c in base[:3]) + (255,)
        edge = mix(base, rgb(pal["gap_dark"]), 0.35)
        if pal.get("cap"):
            lit, lit_scale, lit_shift = rgb(pal["cap"]), 0.68, 0.18
        else:
            lit, lit_scale, lit_shift = mix(base, rgb(pal["lit"]), 0.22), 0.6, 0.16

        def draw_shadow(dr, bx, by, poly=poly):
            dr.polygon([(bx + px + 4 * s, by + py + 5 * s) for px, py in poly], fill=rgb(pal["gap_dark"], 150))

        def draw_body(dr, bx, by, poly=poly, base=base, edge=edge):
            # The edge shows only along the lower right: the body sits up-left on it.
            dr.polygon([(bx + px, by + py) for px, py in poly], fill=edge)
            dr.polygon([(bx + px * 0.93 - 3 * s, by + py * 0.93 - 3 * s) for px, py in poly], fill=base)

        def draw_sheen(dr, bx, by, poly=poly, lit=lit, sc=lit_scale, k=lit_shift, size=size):
            dr.polygon([(bx + px * sc - size * k, by + py * sc - size * k) for px, py in poly], fill=lit)

        cv.wrap_draw(ds, draw_shadow, x, y, size + 8)
        cv.wrap_draw(db, draw_body, x, y, size + 4)
        cv.wrap_draw(dh, draw_sheen, x, y, size)
    cv.blend(shadow, 3 * s)
    cv.blend(body)
    cv.blend(sheen)
    speckle(cv, rnd, [pal["gap_dark"], pal["lit"]], int(7000 / (s * s)), 1.5 * s, 3 * s, 70)
    return cv.img


def paint_water(seed, pal, shallow):
    rnd = random.Random(seed)
    cv = Canvas(pal["base"])
    tone_blobs(cv, rnd, [pal["deep"], pal["lit"]], 160, 90, 260, 90, 60)
    if shallow:
        # The bed shows through: soft stones under the water.
        pebbles(cv, rnd, 500, 8, 20, [pal["bed"]], pal["bed_lit"], pal["deep"])
        flat = Image.new("RGBA", (W, W), rgb(pal["base"]))
        cv.img = Image.blend(flat, cv.img, 0.55)
    ripples(cv, rnd, 1400, pal["deep"], 150, (24, 60))
    ripples(cv, rnd, 1100, pal["light"], 200, (18, 46))
    speckle(cv, rnd, [pal["glint"]], 160, 3, 5)
    return cv.img


def paint_snow(seed, pal):
    """Snow over any land: drifts with blue shadows, glints, and whatever
    pokes through (grass tips, mud, stones) per pal["through"]."""
    rnd = random.Random(seed)
    cv = Canvas(pal["base"])
    tone_blobs(cv, rnd, [pal["shadow"], pal["light"], pal["light"]], 200, 70, 230, 90, 55)
    # Drifts: soft shadows on the lee side of low mounds.
    tone_blobs(cv, rnd, [pal["deep_shadow"]], 90, 40, 110, 45, 30)
    through = pal.get("through")
    if through == "grass":
        # A few tufts of dead grass stick up through the snow.
        tufts(cv, rnd, 170, pal["poke_dark"], pal["poke"], pal["shadow"])
    elif through == "mud":
        tone_blobs(cv, rnd, [pal["mud"]], 80, 30, 80, 70, 25)
        speckle(cv, rnd, [pal["mud_dark"], pal["shadow"]], 5000, 2.5, 4.5)
        pebbles(cv, rnd, 160, 7, 14, [pal["mud_dark"]], pal["mud"], pal["deep_shadow"])
    speckle(cv, rnd, [pal["glint"]], 2200, 2, 4)
    return cv.img


# ---------------------------------------------------------------- palettes

SUMMER = {
    LIGHT_GRASS: dict(base="5f8a3a", shade="4d7432", lit="7a9f45", dark="3e6129", mid="6a9440", light="9cbf5a",
                      clover="76a64a", petals=["f2eee0", "f4d65a", "e9e4f2"], centre="e0b23a"),
    DARK_GRASS: dict(base="456f31", shade="375c29", lit="5a8139", dark="2c4d23", mid="4f7a35", light="7aa24a",
                     clover="5e9140", petals=["eae6d8"], centre="d4a83a"),
    DIRT: dict(base="7d6044", damp="634a34", dry="94765a", dark="533d2a", light="a3876a",
               stones=["8d8274", "9a8f80", "7e7466"], stone_lit="bdb3a3"),
    COBBLESTONE: dict(gap="4a4036", gap_dark="2f2922", moss="4f5a33",
                      stones=["8b877f", "9a958c", "7f7a72", "a39d92", "7d8288", "8f7e6a", "968a78"],
                      warm="9a8466", lit="c9c3b6"),
    SHALLOW_WATER: dict(base="4b8a98", deep="3c7584", lit="62a2ac", light="a6d6db", glint="e8f6f6", bed="6b8c80", bed_lit="8eab9c"),
    DEEP_WATER: dict(base="336a7d", deep="2c5e70", lit="3d778a", light="76a9b7", glint="cbe5ec"),
}

# Autumn leaf colours: orange, gold, red, tan. No dull brown: a brown leaf
# read as a dropping (Jeff, 2026-11-12).
LEAVES = ["c8702a", "d99a3a", "a8452a", "b8862e"]
# Leaves per texture. A third of the first counts (1100 / 850 / 500 / 310),
# which littered the ground (Jeff, 2026-11-12).
LEAF_COUNTS = {LIGHT_GRASS: 370, DARK_GRASS: 280, DIRT: 170, COBBLESTONE: 100}
LEAF_SEED = 500

AUTUMN = {
    LIGHT_GRASS: dict(base="84863d", shade="6d6f36", lit="9e9c48", dark="595a2c", mid="8e8f42", light="bab25a",
                      clover="909345", leaves=LEAVES),
    DARK_GRASS: dict(base="6b7137", shade="595e2e", lit="81853f", dark="484c26", mid="757a3b", light="9f9d4e",
                     clover="7c823e", leaves=LEAVES),
    DIRT: dict(SUMMER[DIRT], base="76593f", damp="5d4532", dry="8b6d50", dark="4d3826", light="9c7f62", leaves=LEAVES),
    COBBLESTONE: dict(SUMMER[COBBLESTONE], moss="5a5a33", leaves=LEAVES),
    SHALLOW_WATER: dict(SUMMER[SHALLOW_WATER], base="4c8592", deep="3d717e", lit="5f9ba5"),
    DEEP_WATER: dict(SUMMER[DEEP_WATER], base="336473", deep="2c5868", lit="3d6f80"),
}

WINTER = {
    LIGHT_GRASS: dict(base="7c7d57", shade="686a49", lit="908f68", dark="56583d", mid="838561", light="a5a179", clover="7d835e"),
    DARK_GRASS: dict(base="646b48", shade="535a3c", lit="787d56", dark="454b33", mid="6c734e", light="8b8e66", clover="6f7652"),
    DIRT: dict(SUMMER[DIRT], base="6c5644", damp="574536", dry="7f6853", dark="4a3a2d", light="917b66",
               stones=["858078", "918b82", "79746c"], stone_lit="b0aaa0"),
    COBBLESTONE: dict(SUMMER[COBBLESTONE], gap="433b33", moss="4a4e36", stones=["86847f", "94918b", "7a7874", "9c9993"], warm="8a7d6c"),
    SHALLOW_WATER: dict(base="4f7a87", deep="416a77", lit="618b96", light="9cc0c8", glint="dff0f2", bed="627a74", bed_lit="7f9389"),
    DEEP_WATER: dict(base="385e6b", deep="30535f", lit="416977", light="7099a5", glint="c9dde3"),
}

SNOW = dict(base="e6ecf1", shadow="c7d2dd", deep_shadow="a9b8c7", light="f6f9fc", glint="ffffff")

SNOWY = {
    LIGHT_GRASS: dict(SNOW, through="grass", poke="7d8263", poke_dark="5c6149"),
    # The same seed as light grass under snow: the two grasses look alike, so
    # the patches of dark grass disappear under the snow.
    DARK_GRASS: dict(SNOW, through="grass", poke="7d8263", poke_dark="5c6149"),
    DIRT: dict(SNOW, base="d8dfe5", through="mud", mud="9c9188", mud_dark="6f645b"),
    COBBLESTONE: dict(SUMMER[COBBLESTONE], gap="b9c4ce", gap_dark="8d99a5", moss="c9d3dc", cap="eef3f7",
                      stones=["8e8c88", "9c9995", "84827e"], warm="8e857a"),
    SHALLOW_WATER: WINTER[SHALLOW_WATER],
    DEEP_WATER: WINTER[DEEP_WATER],
}

PALETTES = {"summer": SUMMER, "autumn": AUTUMN, "winter": WINTER, "snowy": SNOWY}


def paint(terrain, season):
    """The terrain's ground canvas, and its autumn leaves on a layer of their
    own, or None in a season with no leaves."""
    pal = PALETTES[season][terrain]
    leaves = None
    if pal.get("leaves"):
        leaves = fallen_leaves(random.Random(LEAF_SEED + terrain), LEAF_COUNTS[terrain], pal["leaves"])
    return paint_ground(terrain, season, pal), leaves


def paint_ground(terrain, season, pal):
    if season == "snowy" and terrain in (LIGHT_GRASS, DARK_GRASS):
        return paint_snow(20, pal)
    if season == "snowy" and terrain == DIRT:
        return paint_snow(21, pal)
    if terrain == LIGHT_GRASS:
        return paint_grass(1, pal, False)
    if terrain == DARK_GRASS:
        return paint_grass(2, pal, True)
    if terrain == DIRT:
        return paint_dirt(3, pal)
    if terrain == COBBLESTONE:
        return paint_cobble(COBBLE_SEED, pal)
    if terrain == SHALLOW_WATER:
        return paint_water(5, pal, True)
    return paint_water(6, pal, False)


def shrink(img, leaves=None):
    """Area-average to TEX (an exact factor, so the texture still wraps), then
    reduce to a small palette with no dither. leaves, if given, is shrunk the
    same way and laid on after the reduction: a pixel at least half covered by
    leaf takes the nearest LEAVES colour, so the leaves never share the
    ground's palette."""
    small = img.resize((TEX, TEX), Image.BOX).convert("RGB")
    small = small.quantize(colors=COLOURS, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE).convert("RGB")
    if leaves is None:
        return small
    # Pillow averages an RGBA image with premultiplied alpha, so a part-covered
    # pixel keeps the leaf's colour, not a blend with the transparent black.
    cover = leaves.resize((TEX, TEX), Image.BOX)
    colours = [rgb(h)[:3] for h in LEAVES]
    src = cover.load()
    dst = small.load()
    for y in range(TEX):
        for x in range(TEX):
            r, g, b, a = src[x, y]
            if a >= 128:
                dst[x, y] = min(colours, key=lambda c: (c[0] - r) ** 2 + (c[1] - g) ** 2 + (c[2] - b) ** 2)
    return small


def build_atlas(season):
    rows = (len(LAYER_ORDER) + ATLAS_COLS - 1) // ATLAS_COLS
    atlas = Image.new("RGB", (ATLAS_COLS * TEX, rows * TEX))
    for rank, terrain in enumerate(LAYER_ORDER):
        atlas.paste(shrink(*paint(terrain, season)), ((rank % ATLAS_COLS) * TEX, (rank // ATLAS_COLS) * TEX))
    return atlas


# ---------------------------------------------------------------- edge noise

def _lattice(n, seed):
    rnd = random.Random(seed)
    return [[rnd.random() for _ in range(n)] for _ in range(n)]


def _smooth(t):
    return t * t * (3 - 2 * t)


def _vnoise(lat, x, y):
    n = len(lat)
    x0 = math.floor(x)
    y0 = math.floor(y)
    fx = _smooth(x - x0)
    fy = _smooth(y - y0)
    x0 %= n
    y0 %= n
    x1 = (x0 + 1) % n
    y1 = (y0 + 1) % n
    a = lat[y0][x0] + (lat[y0][x1] - lat[y0][x0]) * fx
    b = lat[y1][x0] + (lat[y1][x1] - lat[y1][x0]) * fx
    return a + (b - a) * fy


def build_edge_noise():
    """fBm in tile units that wraps every 16 tiles (the TEX period), as grey;
    128 is no shift. Smooth, so the PNG stays small."""
    tiles = TEX // TILE_PX
    octaves = [(_lattice(tiles * f, 71 + i), f, w) for i, (f, w) in enumerate(((2, 1.0), (4, 0.55), (8, 0.3)))]
    total = sum(w for _, _, w in octaves)
    img = Image.new("L", (TEX, TEX))
    px = img.load()
    for y in range(TEX):
        for x in range(TEX):
            wx = (x + 0.5) / TILE_PX
            wy = (y + 0.5) / TILE_PX
            v = sum(w * _vnoise(lat, wx * f, wy * f) for lat, f, w in octaves) / total
            px[x, y] = int(round(v * 255))
    return img


# ---------------------------------------------------------------- sparkles

SPARKLE_COLS = 4
SPARKLE_ROWS = 3


def build_sparkles():
    """12 frames of 32x32, read left to right then down. Two glints a cycle,
    each three frames: a dot, a small cross, a dot."""
    sheet = Image.new("RGBA", (SPARKLE_COLS * TILE_PX, SPARKLE_ROWS * TILE_PX), (0, 0, 0, 0))
    white = (255, 255, 255, 230)
    soft = (220, 240, 250, 150)
    rnd = random.Random(77)
    frames = SPARKLE_COLS * SPARKLE_ROWS
    for start in (1, 7):
        x = rnd.randint(5, TILE_PX - 6)
        y = rnd.randint(5, TILE_PX - 6)
        for step, shape in enumerate(("dot", "cross", "dot")):
            frame = (start + step) % frames
            ox = (frame % SPARKLE_COLS) * TILE_PX
            oy = (frame // SPARKLE_COLS) * TILE_PX
            sheet.putpixel((ox + x, oy + y), white)
            if shape == "cross":
                for dx, dy in ((1, 0), (-1, 0), (0, 1), (0, -1), (2, 0), (-2, 0)):
                    sheet.putpixel((ox + x + dx, oy + y + dy), soft)
    return sheet


# ---------------------------------------------------------------- pack script

def write_pack_script():
    lines = [
        "extends RefCounted",
        "## The ground the terrain renderer draws (LLM-967). Generated by",
        "## tools/terrain-art/build.py; edit that script and rerun it, not this file.",
        "",
        "## World pixels per map tile; the textures are drawn one texel per world pixel.",
        "const TILE_PX: int = %d" % TILE_PX,
        "## Each terrain's texture is TEXTURE_PX square and wraps.",
        "const TEXTURE_PX: int = %d" % TEX,
        "## Textures sit in the atlas in LAYER_ORDER, ATLAS_COLS across.",
        "const ATLAS_COLS: int = %d" % ATLAS_COLS,
        "",
        "## terrain.gd ids, drawn bottom to top.",
        "const LAYER_ORDER: Array[int] = [%s]" % ", ".join(str(t) for t in LAYER_ORDER),
        "",
        "## The ground atlas per season. A season not named here draws summer.",
        "## snowy is the ground under light or deep snow, any season.",
        "const SHEETS: Dictionary = {",
    ]
    lines += ['    "%s": "res://assets/terrain/ground-%s.png",' % (s, s) for s in SEASONS]
    lines += [
        "}",
        "",
        "## The wobble on every terrain edge, grey; 128 is no shift.",
        'const EDGE_NOISE: String = "res://assets/terrain/edge-noise.png"',
        "## How far the wobble moves an edge, as a fraction of a tile.",
        "const EDGE_WOBBLE: float = 0.6",
        "",
        "## Terrains whose blade tips reach past their edge.",
        "const FRINGE_TERRAINS: Array[int] = [%d, %d]" % (LIGHT_GRASS, DARK_GRASS),
        "## Terrains that meet with no shadow or dark rim between them, per",
        "## season (default for a season not named). Under snow the land types",
        "## all look alike, so only cobbles and water keep their edges.",
        "const SOFT_EDGE_TERRAINS: Dictionary = {",
        '    "default": [%d, %d],' % (LIGHT_GRASS, DARK_GRASS),
        '    "snowy": [%d, %d, %d],' % (DIRT, LIGHT_GRASS, DARK_GRASS),
        "}",
        "## Water: no edge shading of its own, and a pale line where it meets land.",
        "const WATER_TERRAINS: Array[int] = [%d, %d]" % (SHALLOW_WATER, DEEP_WATER),
        "",
        "## Animated glints drawn over water: frames read left to right, then down.",
        'const SPARKLE_SHEET: String = "res://assets/terrain/water-sparkles.png"',
        "const SPARKLE_COLS: int = %d" % SPARKLE_COLS,
        "const SPARKLE_ROWS: int = %d" % SPARKLE_ROWS,
        "const SPARKLE_FRAME_SECONDS: float = 0.6",
        "const SPARKLE_TERRAINS: Array[int] = [%d, %d]" % (SHALLOW_WATER, DEEP_WATER),
        "",
    ]
    with open(PACK_SCRIPT, "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(lines))


def check_cobble_cells():
    """Every cobble cell from the grid search equals the cell clipped against
    every centre, for the production seed (code_review, LLM-967). Run with
    --check after changing the stone sizes or the search."""
    gap = 5.0 * STONE
    sites, grid, cell, n = _stone_sites(random.Random(COBBLE_SEED + 1000), 17 * STONE, 30 * STONE, int(60000 / (STONE * STONE)))
    worst = 0.0
    for i in range(len(sites)):
        fast = _stone_cell(i, sites, grid, cell, n, gap)
        x, y, _ = sites[i]
        full = [(-W / 2, -W / 2), (W / 2, -W / 2), (W / 2, W / 2), (-W / 2, W / 2)]
        for j in range(len(sites)):
            if j == i or not full:
                continue
            sx, sy, _ = sites[j]
            vx = (sx - x + W / 2) % W - W / 2
            vy = (sy - y + W / 2) % W - W / 2
            dist = math.hypot(vx, vy)
            full = _clip(full, vx / dist, vy / dist, dist / 2 - gap / 2)
        # The two lists may start at different corners: compare the shapes,
        # each corner against the nearest corner of the other cell, both ways,
        # and the areas.
        def apart(a, b):
            return max(min(math.hypot(ax - bx, ay - by) for bx, by in b) for ax, ay in a)

        def area(poly):
            return abs(sum(poly[k][0] * poly[k - 1][1] - poly[k - 1][0] * poly[k][1] for k in range(len(poly)))) / 2

        if not fast or not full:
            if fast or full:
                worst = float("inf")
            continue
        worst = max(worst, apart(fast, full), apart(full, fast), abs(area(fast) - area(full)))
    print("cobble cells: %d stones, largest difference %.6f px" % (len(sites), worst))
    return worst < 1e-6


def check_wrap_seams():
    """The built atlases wrap with no seam: in every texture, the brightness
    step between the last 8 rows (columns) and the first 8 is no larger than
    the largest step between two neighbouring 8-row bands inside the texture.
    A seam fails it: the grass blades drawn out of order across the wrap
    stepped 9.4 against at most 2.5 inside (code_review, LLM-967)."""
    band = 8
    ok = True
    for season in SEASONS:
        atlas = Image.open(os.path.join(ASSET_DIR, "ground-%s.png" % season)).convert("L")
        for rank, terrain in enumerate(LAYER_ORDER):
            tex = atlas.crop(((rank % ATLAS_COLS) * TEX, (rank // ATLAS_COLS) * TEX, (rank % ATLAS_COLS + 1) * TEX, (rank // ATLAS_COLS + 1) * TEX))
            for axis, means in (("rows", tex.resize((1, TEX), Image.BOX)), ("columns", tex.resize((TEX, 1), Image.BOX))):
                line = list(means.tobytes())

                def mean(i):
                    return sum(line[(i + k) % TEX] for k in range(band)) / band

                wrap = abs(mean(0) - mean(TEX - band))
                inside = max(abs(mean(i) - mean(i - band)) for i in range(2 * band, TEX - 2 * band))
                if wrap > inside:
                    print("seam: %s terrain %d %s, step %.1f at the wrap, at most %.1f inside" % (season, terrain, axis, wrap, inside))
                    ok = False
    print("wrap seams: %s" % ("none" if ok else "found"))
    return ok


def check_leaf_cover():
    """A pixel half covered by a leaf keeps that leaf's colour through
    shrink(), which relies on Pillow averaging RGBA with premultiplied alpha.
    Blended toward the transparent black instead, a half-covered orange pixel
    would snap to the red leaf colour (code_review, LLM-967)."""
    ground = Image.new("RGBA", (W, W), rgb(AUTUMN[DARK_GRASS]["base"]))
    leaves = Canvas.layer()
    # The left half of the 4x4 canvas block that shrinks to texture pixel (0, 0).
    ImageDraw.Draw(leaves).rectangle((0, 0, 1, 3), fill=rgb(LEAVES[0]))
    got = shrink(ground, leaves).getpixel((0, 0))
    want = rgb(LEAVES[0])[:3]
    print("leaf cover: half-covered pixel %s, want %s" % ("%02x%02x%02x" % got, LEAVES[0]))
    return got == want


def main():
    if "--check" in sys.argv:
        cobbles_ok = check_cobble_cells()
        seams_ok = check_wrap_seams()
        leaves_ok = check_leaf_cover()
        sys.exit(0 if cobbles_ok and seams_ok and leaves_ok else 1)
    os.makedirs(ASSET_DIR, exist_ok=True)
    for name in os.listdir(ASSET_DIR):
        # The tile sheets of the earlier 16 px layout are replaced by atlases.
        if name.startswith("ground-") and name.split(".")[0][len("ground-"):] not in SEASONS:
            os.remove(os.path.join(ASSET_DIR, name))
    for season in SEASONS:
        build_atlas(season).save(os.path.join(ASSET_DIR, "ground-%s.png" % season))
        print("painted", season)
    build_edge_noise().save(os.path.join(ASSET_DIR, "edge-noise.png"))
    build_sparkles().save(os.path.join(ASSET_DIR, "water-sparkles.png"))
    write_pack_script()
    print("wrote", ASSET_DIR, "and", PACK_SCRIPT)


if __name__ == "__main__":
    main()
