"""Builds salem's ground sheets (LLM-967): every pixel is drawn here, no pack art.

    python tools/terrain-art/build.py

Writes client/assets/terrain/ground-<season>.png, client/assets/terrain/
water-sparkles.png and client/scripts/terrain_pack.gd (the layout the terrain
renderer reads). Rerunning writes the same files. After a change, open the
client project once (godot --headless --editor --quit) so Godot writes the
.import sidecars, and commit them with the PNGs.

The renderer draws the ground as layered corner autotiles: each terrain is a
layer, bottom to top in LAYER_ORDER, and each drawn tile sits on the corner
where four map cells meet. A layer's tile is picked by a 4-bit mask of which
corners are that terrain or a terrain above it (TL=1, TR=2, BR=4, BL=8).

Sheet layout: one row per terrain (terrain id - 1). Columns 1-14 hold the edge
tiles for masks 1-14 (transparent outside the terrain); column 15 and up hold
the full tiles (mask 15), the plain one first, then the ones with detail.

Edges stay seamless between neighbouring tiles because the boundary is a
function of the corner values plus one noise field that repeats every tile:
along a shared tile border both tiles see the same two corners and the same
noise, so the line crosses the border in the same place. Every terrain uses the
same noise, so two layers with the same mask have the same outline and the
upper one hides the lower one exactly.
"""

import math
import os
import random

from PIL import Image

ROOT = os.path.normpath(os.path.join(os.path.dirname(__file__), "..", ".."))
ASSET_DIR = os.path.join(ROOT, "client", "assets", "terrain")
PACK_SCRIPT = os.path.join(ROOT, "client", "scripts", "terrain_pack.gd")

T = 16  # source tile size in pixels
FILL_COL = 15
FILL_VARIANTS = 7  # plain + 6 with detail

# terrain.gd ids
DIRT, LIGHT_GRASS, DARK_GRASS, COBBLESTONE, SHALLOW_WATER, DEEP_WATER = 1, 2, 3, 4, 5, 6
TERRAINS = [DIRT, LIGHT_GRASS, DARK_GRASS, COBBLESTONE, SHALLOW_WATER, DEEP_WATER]
# Bottom to top. Land overlaps water, dark grass grows over light, the cobbled
# paths lie on everything.
LAYER_ORDER = [DEEP_WATER, SHALLOW_WATER, DIRT, LIGHT_GRASS, DARK_GRASS, COBBLESTONE]

SEASONS = ["summer", "autumn", "winter", "snowy"]


def hexrgb(h):
    return (int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16), 255)


def ramp(base, dark, light, outline, extra=None):
    out = {"base": hexrgb(base), "dark": hexrgb(dark), "light": hexrgb(light), "outline": hexrgb(outline)}
    if extra:
        out.update({k: hexrgb(v) for k, v in extra.items()})
    return out


# Each season's colours per terrain. base is the flat ground, dark and light
# draw the detail, outline is the rim where the terrain ends over a lower one.
PALETTES = {
    "summer": {
        DIRT: ramp("8a6643", "6b4d33", "a8825a", "5a3f2a"),
        LIGHT_GRASS: ramp("7fb356", "62963f", "a0cd6e", "557f3a"),
        DARK_GRASS: ramp("559444", "3f7633", "6eab57", "36652c"),
        COBBLESTONE: ramp("9a968e", "77736c", "b8b4ab", "4d4a45", {"mortar": "605c56", "alt": "8c8880"}),
        SHALLOW_WATER: ramp("56a5ca", "4690b6", "93d3e8", "7cc4dc"),
        DEEP_WATER: ramp("32739f", "2a648d", "4f93bd", "2a648d"),
    },
    "autumn": {
        DIRT: ramp("86603f", "684a30", "a37c55", "573c28"),
        LIGHT_GRASS: ramp("a5aa53", "878d3f", "c4c56c", "6f7534"),
        DARK_GRASS: ramp("879244", "6d7735", "a5ae58", "5c652d"),
        COBBLESTONE: ramp("9a968e", "77736c", "b8b4ab", "4d4a45", {"mortar": "605c56", "alt": "8c8880"}),
        SHALLOW_WATER: ramp("549dbf", "468aad", "8ccbe0", "78bcd3"),
        DEEP_WATER: ramp("316e97", "2a6087", "4c8db5", "2a6087"),
    },
    "winter": {
        DIRT: ramp("776150", "5e4b3e", "907865", "4f3f34"),
        LIGHT_GRASS: ramp("92a173", "77875c", "aab58a", "66744f"),
        DARK_GRASS: ramp("768a5f", "60714d", "8d9f74", "53633f"),
        COBBLESTONE: ramp("94928e", "73716d", "aeaca7", "4b4a47", {"mortar": "5d5b58", "alt": "878581"}),
        SHALLOW_WATER: ramp("5c90ad", "4e7f9b", "8dbad0", "7eaec6"),
        DEEP_WATER: ramp("33607e", "2b536e", "4a7896", "2b536e"),
    },
    "snowy": {
        DIRT: ramp("d9e1e8", "bccad6", "f0f4f8", "a3b2c1"),
        LIGHT_GRASS: ramp("edf2f6", "d0dbe4", "ffffff", "b4c3d1"),
        # Snow hides where light grass ends and dark begins: no rim, one white.
        DARK_GRASS: ramp("edf2f6", "d0dbe4", "ffffff", "edf2f6"),
        COBBLESTONE: ramp("c6cbd0", "a3abb3", "e6ebef", "7d868f", {"mortar": "8f98a1", "alt": "b7bec5"}),
        SHALLOW_WATER: ramp("a9cfe0", "93bfd3", "dcedf5", "e8f3f8"),
        DEEP_WATER: ramp("5f8aa6", "537b95", "7ea5be", "537b95"),
    },
}


# ---------------------------------------------------------------- noise

def _lattice(period, seed):
    rnd = random.Random(seed)
    return [[rnd.uniform(-1.0, 1.0) for _ in range(period)] for _ in range(period)]


def _smooth(t):
    return t * t * (3.0 - 2.0 * t)


def _value_noise(lat, x, y):
    """Periodic value noise; x and y in lattice cells, wrapping at the lattice size."""
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


EDGE_LAT_COARSE = _lattice(2, 11)
EDGE_LAT_FINE = _lattice(4, 12)


def edge_noise(px, py):
    """The shared wobble on every terrain edge; px, py in tile pixels, period one tile."""
    return 0.13 * _value_noise(EDGE_LAT_COARSE, px / 8.0, py / 8.0) + 0.06 * _value_noise(EDGE_LAT_FINE, px / 4.0, py / 4.0)


def edge_field(mask, px, py):
    """Above 0 inside the terrain. px, py are tile pixels and may run past the tile."""
    u = px / T
    v = py / T
    tl = 1.0 if mask & 1 else 0.0
    tr = 1.0 if mask & 2 else 0.0
    br = 1.0 if mask & 4 else 0.0
    bl = 1.0 if mask & 8 else 0.0
    f = tl * (1 - u) * (1 - v) + tr * u * (1 - v) + br * u * v + bl * (1 - u) * v
    return f + edge_noise(px, py) - 0.5


# ---------------------------------------------------------------- ground texture

def cobble_cells():
    """Stone centres for the cobbles: a jittered 3x3 grid that repeats every tile."""
    rnd = random.Random(41)
    cells = []
    step = T / 3.0
    for gy in range(3):
        for gx in range(3):
            cells.append(((gx + 0.5) * step + rnd.uniform(-1.2, 1.2), (gy + 0.5) * step + rnd.uniform(-1.2, 1.2), rnd.random()))
    return cells


COBBLE_CELLS = cobble_cells()


def _cobble_at(px, py):
    """(nearest stone, gap to the second nearest) at a pixel centre, wrapping per tile."""
    best = (1e9, None)
    second = 1e9
    for cx, cy, shade in COBBLE_CELLS:
        for ox in (-T, 0, T):
            for oy in (-T, 0, T):
                d = math.hypot(px - (cx + ox), py - (cy + oy))
                if d < best[0]:
                    second = best[0]
                    best = (d, shade)
                elif d < second:
                    second = d
    return best[1], second - best[0]


def ground_pixel(terrain, pal, x, y):
    """The terrain's ground at tile pixel (x, y), the same in every tile so tiles join."""
    if terrain != COBBLESTONE:
        return pal["base"]
    shade, gap = _cobble_at(x + 0.5, y + 0.5)
    if gap < 1.1:
        return pal["mortar"]
    # Light on a stone's top edge, dark on its bottom edge.
    _, gap_up = _cobble_at(x + 0.5, y - 0.5)
    _, gap_down = _cobble_at(x + 0.5, y + 1.5)
    if gap_up < 1.1:
        return pal["light"]
    if gap_down < 1.1:
        return pal["dark"]
    return pal["base"] if shade < 0.55 else pal["alt"]


# ---------------------------------------------------------------- tiles

def edge_tile(terrain, pal, mask):
    img = Image.new("RGBA", (T, T), (0, 0, 0, 0))
    for y in range(T):
        for x in range(T):
            cx = x + 0.5
            cy = y + 0.5
            g = edge_field(mask, cx, cy)
            if g <= 0:
                continue
            # One pixel of rim: the field's rise across one pixel.
            gx = (edge_field(mask, cx + 0.5, cy) - edge_field(mask, cx - 0.5, cy))
            gy = (edge_field(mask, cx, cy + 0.5) - edge_field(mask, cx, cy - 0.5))
            if g < math.hypot(gx, gy):
                img.putpixel((x, y), pal["outline"])
            else:
                img.putpixel((x, y), ground_pixel(terrain, pal, x, y))
    return img


def _put(img, x, y, colour):
    if 0 <= x < T and 0 <= y < T:
        img.putpixel((x, y), colour)


def _grass_detail(img, pal, rnd):
    for _ in range(rnd.randint(2, 4)):
        x = rnd.randint(1, T - 4)
        y = rnd.randint(2, T - 2)
        # A tuft: two dark blades with a light one between.
        _put(img, x, y, pal["dark"])
        _put(img, x + 2, y, pal["dark"])
        _put(img, x + 1, y - 1, pal["light"])
    for _ in range(rnd.randint(1, 3)):
        _put(img, rnd.randint(1, T - 2), rnd.randint(1, T - 2), pal["light"])


def _dirt_detail(img, pal, rnd):
    for _ in range(rnd.randint(2, 4)):
        x = rnd.randint(1, T - 2)
        y = rnd.randint(1, T - 3)
        # A pebble: lit top, shadow under it.
        _put(img, x, y, pal["light"])
        _put(img, x, y + 1, pal["dark"])
    if rnd.random() < 0.6:
        x = rnd.randint(1, T - 4)
        y = rnd.randint(1, T - 2)
        _put(img, x, y, pal["dark"])
        _put(img, x + 1, y, pal["dark"])


def _water_detail(img, pal, rnd):
    for _ in range(rnd.randint(1, 2)):
        x = rnd.randint(1, T - 5)
        y = rnd.randint(2, T - 2)
        # A ripple: a low arc of four pixels.
        _put(img, x, y, pal["light"])
        _put(img, x + 1, y - 1, pal["light"])
        _put(img, x + 2, y - 1, pal["light"])
        _put(img, x + 3, y, pal["light"])


DETAIL = {
    DIRT: _dirt_detail,
    LIGHT_GRASS: _grass_detail,
    DARK_GRASS: _grass_detail,
    SHALLOW_WATER: _water_detail,
    DEEP_WATER: _water_detail,
}


def fill_tile(terrain, pal, variant):
    img = Image.new("RGBA", (T, T))
    for y in range(T):
        for x in range(T):
            img.putpixel((x, y), ground_pixel(terrain, pal, x, y))
    if variant > 0 and terrain in DETAIL:
        # Seeded by terrain and variant only, so a detail sits in the same spot
        # in every season and a season change does not shuffle the ground.
        DETAIL[terrain](img, pal, random.Random(terrain * 100 + variant))
    return img


def fill_variants(terrain):
    # Cobbles have no detail to vary: one tile.
    return 1 if terrain == COBBLESTONE else FILL_VARIANTS


def build_sheet(season):
    cols = FILL_COL + FILL_VARIANTS
    sheet = Image.new("RGBA", (cols * T, len(TERRAINS) * T), (0, 0, 0, 0))
    for terrain in TERRAINS:
        pal = PALETTES[season][terrain]
        row = terrain - 1
        for mask in range(1, 15):
            sheet.paste(edge_tile(terrain, pal, mask), (mask * T, row * T))
        for variant in range(fill_variants(terrain)):
            sheet.paste(fill_tile(terrain, pal, variant), ((FILL_COL + variant) * T, row * T))
    return sheet


# ---------------------------------------------------------------- sparkles

SPARKLE_COLS = 4
SPARKLE_ROWS = 3


def build_sparkles():
    """12 frames, read left to right then down. Each tile starts at its own frame."""
    sheet = Image.new("RGBA", (SPARKLE_COLS * T, SPARKLE_ROWS * T), (0, 0, 0, 0))
    white = (255, 255, 255, 230)
    soft = (220, 240, 250, 150)
    rnd = random.Random(77)
    frames = SPARKLE_COLS * SPARKLE_ROWS
    # Two glints a cycle, each three frames: a dot, a small cross, a dot.
    for start in (1, 7):
        x = rnd.randint(3, T - 4)
        y = rnd.randint(3, T - 4)
        for step, shape in enumerate(("dot", "cross", "dot")):
            frame = (start + step) % frames
            ox = (frame % SPARKLE_COLS) * T
            oy = (frame // SPARKLE_COLS) * T
            sheet.putpixel((ox + x, oy + y), white)
            if shape == "cross":
                for dx, dy in ((1, 0), (-1, 0), (0, 1), (0, -1)):
                    sheet.putpixel((ox + x + dx, oy + y + dy), soft)
    return sheet


# ---------------------------------------------------------------- pack script

def gd_coords(col, row):
    return "Vector2i(%d, %d)" % (col, row)


def write_pack_script():
    lines = [
        "extends RefCounted",
        "## The ground pack the terrain renderer draws (LLM-967). Generated by",
        "## tools/terrain-art/build.py; edit that script and rerun it, not this file.",
        "## A purchased pack gets a file of the same shape, written by hand.",
        "",
        "## Source tile size in the sheets.",
        "const SRC_TILE_SIZE: int = %d" % T,
        "",
        "## terrain.gd ids, drawn bottom to top.",
        "const LAYER_ORDER: Array[int] = [%s]" % ", ".join(str(t) for t in LAYER_ORDER),
        "",
        "## The ground sheet per season, all in one layout. A season not named here",
        "## draws summer. snowy is the ground under light or deep snow, any season.",
        "const SHEETS: Dictionary = {",
    ]
    for season in SEASONS:
        lines.append('    "%s": "res://assets/terrain/ground-%s.png",' % (season, season))
    lines += [
        "}",
        "",
        "## Edge tiles per terrain, indexed by corner mask (TL=1, TR=2, BR=4, BL=8).",
        "## Index 0 and 15 are unused (an empty and a full corner set); a",
        "## Vector2i(-1, -1) entry is a mask the pack does not draw.",
        "const EDGES: Dictionary = {",
    ]
    for terrain in TERRAINS:
        row = terrain - 1
        cells = [gd_coords(-1, -1)] + [gd_coords(mask, row) for mask in range(1, 15)] + [gd_coords(-1, -1)]
        lines.append("    %d: [%s]," % (terrain, ", ".join(cells)))
    lines += [
        "}",
        "",
        "## Full tiles per terrain. The renderer picks one by map position; the plain",
        "## tile is listed twice so it comes up more often than any detailed one.",
        "const FILLS: Dictionary = {",
    ]
    for terrain in TERRAINS:
        row = terrain - 1
        count = fill_variants(terrain)
        cells = [gd_coords(FILL_COL, row)]
        cells += [gd_coords(FILL_COL + v, row) for v in range(count)]
        lines.append("    %d: [%s]," % (terrain, ", ".join(cells)))
    lines += [
        "}",
        "",
        "## Animated glints drawn over water: frames read left to right, then down.",
        "const SPARKLE_SHEET: String = \"res://assets/terrain/water-sparkles.png\"",
        "const SPARKLE_COLS: int = %d" % SPARKLE_COLS,
        "const SPARKLE_ROWS: int = %d" % SPARKLE_ROWS,
        "const SPARKLE_FRAME_SECONDS: float = 0.6",
        "const SPARKLE_TERRAINS: Array[int] = [%d, %d]" % (SHALLOW_WATER, DEEP_WATER),
        "",
    ]
    with open(PACK_SCRIPT, "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(lines))


def main():
    os.makedirs(ASSET_DIR, exist_ok=True)
    for season in SEASONS:
        build_sheet(season).save(os.path.join(ASSET_DIR, "ground-%s.png" % season))
    build_sparkles().save(os.path.join(ASSET_DIR, "water-sparkles.png"))
    write_pack_script()
    print("wrote", ASSET_DIR, "and", PACK_SCRIPT)


if __name__ == "__main__":
    main()
