"""Paints salem's storm weather (LLM-970). Every pixel is drawn here, no pack art.

    python tools/tree-art/weather.py
    python tools/tree-art/weather.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/weather.py --check     # sizes, seams, a fresh paint

Writes client/assets/weather/:

    rain-light.png, rain-heavy.png   8 frames of a 64x256 tile, side by side
    snow-light.png, snow-heavy.png   the same layout
    lightning.png                    2 bolt variants of 64x256, side by side
    rain-splash.png, snow-splash.png 8 frames of 32x32, side by side

Density 2, like the painted props: twice the old pack art's size, drawn at
scale 1 (storm_fx.gd, rain_splashes.gd), so the same size in the world with
twice the detail. Unlike the props, weather is drawn straight onto the pixel
grid, not painted large and shrunk: rain is one-pixel streaks and snow is
single flakes, which a shrink would smear into haze.

The falling sheets are tiles that the overlay repeats across the screen and
slides sideways for the wind, so each one must join to itself on all four
sides and loop: every drop and flake is drawn with wrap-around, and each one
moves a whole number of tiles over the 8 frames, so frame 8 is frame 0.
"""

import argparse
import math
import os
import random
import sys

from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_DIR = os.path.normpath(os.path.join(HERE, "..", "..", "client", "assets", "weather"))

FRAMES = 8
TILE = (64, 256)     # one falling-weather frame
BOLT = (64, 256)     # one lightning variant
SPLASH = (32, 32)    # one impact frame; the drop lands on the centre


def rgb(h):
    return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))


def mix(a, b, t):
    return tuple(round(a[i] + (b[i] - a[i]) * t) for i in range(3))


class Canvas:
    """An RGBA pixel grid with alpha-over blending. `wrap` makes every write
    land modulo the size, which is what keeps a tile seamless."""

    def __init__(self, w, h, wrap=False):
        self.w, self.h, self.wrap = w, h, wrap
        self.px = [[(0, 0, 0, 0)] * w for _ in range(h)]

    def put(self, x, y, col, a):
        x, y = int(math.floor(x)), int(math.floor(y))
        if self.wrap:
            x, y = x % self.w, y % self.h
        elif not (0 <= x < self.w and 0 <= y < self.h):
            return
        if a <= 0:
            return
        # Porter-Duff "over" in straight alpha.
        r0, g0, b0, a0 = self.px[y][x]
        sa, da = a / 255.0, a0 / 255.0
        oa = sa + da * (1 - sa)
        if oa <= 0:
            return
        out = tuple(round((col[i] * sa + (r0, g0, b0)[i] * da * (1 - sa)) / oa) for i in range(3))
        self.px[y][x] = out + (round(oa * 255),)

    def image(self):
        im = Image.new("RGBA", (self.w, self.h))
        im.putdata([p for row in self.px for p in row])
        return im


def strip(frames):
    w, h = frames[0].size
    out = Image.new("RGBA", (w * len(frames), h))
    for i, f in enumerate(frames):
        out.paste(f, (i * w, 0))
    return out


# --- rain -------------------------------------------------------------------
#
# A drop is a streak two pixels wide, dim at its tail (top) and brightest at
# its head (bottom), the way a falling drop catches the light; its left pixel
# is the lit edge, a step brighter than its right. Two speeds in a layer —
# one or two tile heights over the 8 frames — so nearer drops fall faster and
# longer than farther ones, and the layer has depth of its own. The streaks
# fall straight down: the overlay's sideways drift is the wind.

RAIN = {
    # The heavy sheet is the back layer: many drops, darker and a little
    # shorter, run at 100 ms a frame.
    "heavy": dict(count=17, tail="3b4f69", head="9fb4ca", alpha=(170, 225), length=(34, 54), fast=0.35),
    # The light sheet is the front layer over it: fewer drops, paler and
    # longer, run at 50 ms a frame.
    "light": dict(count=9, tail="62758c", head="e4edf5", alpha=(190, 240), length=(44, 66), fast=0.5),
}


# A streak is two columns wide, and two streaks of a layer never share or
# touch a column (that reads as a seam when the tile repeats), so their start
# columns are at least this far apart, across the tile's wrap too.
RAIN_MIN_GAP = 3


def rain_columns(n, W, rnd):
    """n start columns, every two at least RAIN_MIN_GAP apart around the
    circle of W columns. Column i sits at its even share i*W/n plus a jitter
    of at most (W//n - RAIN_MIN_GAP), so any neighbour gap — the last-to-first
    one across the wrap included — is at least W//n minus that jitter."""
    if n * RAIN_MIN_GAP > W:
        raise ValueError(f"{n} streaks do not fit {W} columns {RAIN_MIN_GAP} apart")
    jitter = W // n - RAIN_MIN_GAP
    offset = rnd.randrange(W)
    return [(i * W // n + rnd.randint(0, jitter) + offset) % W for i in range(n)]


def circular_gaps(columns, W):
    cols = sorted(columns)
    return [(cols[(i + 1) % len(cols)] - cols[i]) % W for i in range(len(cols))]


def rain_drops(kind, seed):
    spec = RAIN[kind]
    rnd = random.Random(seed)
    W, H = TILE
    drops = []
    for _ in range(spec["count"]):
        drops.append(dict(
            y=rnd.uniform(0, H),
            # Tiles per loop: 1 tile = 32 px a frame, 2 = 64 px a frame.
            laps=2 if rnd.random() < spec["fast"] else 1,
            length=rnd.randint(*spec["length"]),
            alpha=rnd.randint(*spec["alpha"]),
        ))
    # Columns are dealt out shuffled, so a drop's speed and length have
    # nothing to do with where it falls.
    columns = rain_columns(len(drops), W, rnd)
    rnd.shuffle(columns)
    for d, x in zip(drops, columns):
        d["x"] = x
    return drops


def rain_frames(kind, seed, frame_indices):
    """The rain frames at the given indices. Index FRAMES is one whole loop
    on from 0, which --check uses to prove the loop closes."""
    spec = RAIN[kind]
    W, H = TILE
    drops = rain_drops(kind, seed)
    tail, head = rgb(spec["tail"]), rgb(spec["head"])
    frames = []
    for f in frame_indices:
        c = Canvas(W, H, wrap=True)
        for d in drops:
            y_head = d["y"] + f * d["laps"] * H / FRAMES
            # Fast drops are a little longer: the eye smears what moves more.
            n = d["length"] + (10 if d["laps"] == 2 else 0)
            for k in range(n):
                t = k / (n - 1)               # 0 at the tail, 1 at the head
                col = mix(tail, head, t ** 1.4)
                a = d["alpha"] * (0.3 + 0.7 * t)
                y = y_head - (n - 1) + k
                c.put(d["x"], y, mix(col, head, 0.25), a)
                c.put(d["x"] + 1, y, col, a * 0.85)
        frames.append(c.image())
    return frames


def paint_rain(kind, seed):
    return strip(rain_frames(kind, seed, range(FRAMES)))


# --- snow -------------------------------------------------------------------
#
# Flakes drift down one tile over the 8 frames and sway side to side once per
# loop. The front (light) layer has the nearer, bigger flakes; the back (heavy)
# layer many small ones. Each flake has a bright core and a soft cool rim.
# Most are plain dots and uneven flecks; a cross or a symmetric shape reads as
# a sparkle at game zoom, not as falling snow.

SNOW = {
    "heavy": dict(count=44, sizes=(2, 2, 3), sway=(0.5, 2.0)),
    "light": dict(count=16, sizes=(3, 3, 3, 4), sway=(1.5, 3.5)),
}
# The pixel a fleck adds to its 2x2 dot, by the flake's own draw.
FLECK_TAILS = ((2, 0), (2, 1), (0, 2), (1, 2), (-1, 0), (-1, 1), (0, -1), (1, -1))
FLAKE_CORE = rgb("f6f8f4")
FLAKE_RIM = rgb("b7c4cc")


def flake(c, x, y, size, alpha, tail=0):
    x, y = round(x), round(y)
    if size == 1:
        c.put(x, y, FLAKE_CORE, alpha * 0.85)
    elif size == 2:
        for dx, dy in ((0, 0), (1, 0), (0, 1), (1, 1)):
            c.put(x + dx, y + dy, FLAKE_CORE if (dx, dy) == (0, 0) else FLAKE_RIM, alpha * (1.0 if (dx, dy) == (0, 0) else 0.7))
    elif size == 4:
        # A 4x4 flake with its corners clipped: round, bright in the middle.
        for dx in range(4):
            for dy in range(4):
                if (dx in (0, 3)) and (dy in (0, 3)):
                    continue
                inner = 1 <= dx <= 2 and 1 <= dy <= 2
                c.put(x + dx - 1, y + dy - 1, FLAKE_CORE if inner else FLAKE_RIM, alpha * (1.0 if inner else 0.8))
    else:
        # A fleck: a 2x2 dot with one dimmer pixel off one side, so it is
        # uneven, three pixels long one way and two the other.
        for dx, dy in ((0, 0), (1, 0), (0, 1), (1, 1)):
            c.put(x + dx, y + dy, FLAKE_CORE if (dx, dy) == (0, 0) else FLAKE_RIM, alpha * (1.0 if (dx, dy) == (0, 0) else 0.75))
        tx, ty = FLECK_TAILS[tail % len(FLECK_TAILS)]
        c.put(x + tx, y + ty, FLAKE_RIM, alpha * 0.55)


def snow_frames(kind, seed, frame_indices):
    """The snow frames at the given indices; index FRAMES is one loop on."""
    spec = SNOW[kind]
    rnd = random.Random(seed)
    W, H = TILE
    flakes = [dict(
        x=rnd.uniform(0, W), y=rnd.uniform(0, H),
        size=rnd.choice(spec["sizes"]),
        sway=rnd.uniform(*spec["sway"]),
        phase=rnd.uniform(0, 2 * math.pi),
        alpha=rnd.randint(190, 255),
        tail=rnd.randrange(len(FLECK_TAILS)),
    ) for _ in range(spec["count"])]
    frames = []
    for f in frame_indices:
        c = Canvas(W, H, wrap=True)
        for fl in flakes:
            # One tile down per loop; one full sway per loop. Both close at
            # frame 8, so the loop has no jump.
            y = fl["y"] + f * H / FRAMES
            x = fl["x"] + fl["sway"] * math.sin(fl["phase"] + 2 * math.pi * f / FRAMES)
            flake(c, x, y, fl["size"], fl["alpha"], fl["tail"])
        frames.append(c.image())
    return frames


def paint_snow(kind, seed):
    return strip(snow_frames(kind, seed, range(FRAMES)))


# --- lightning --------------------------------------------------------------
#
# A jagged channel from the top of the frame to the bottom, with a few forks
# that die out. A white core two pixels wide, a pale blue glow beside it and a
# faint violet haze outside that. The overlay draws it over the full-screen
# white flash, so the bolt needs a strong, saturated edge to hold its shape
# against the bright sky; the haze is that edge.

BOLT_CORE = rgb("fbfdff")
BOLT_GLOW = rgb("b0c6ff")
BOLT_HAZE = rgb("4c5be8")


def channel(rnd, x, y, y_end, step, jitter, lo, hi):
    pts = [(x, y)]
    while y < y_end:
        y = min(y_end, y + rnd.randint(*step))
        x = max(lo, min(hi, x + rnd.randint(-jitter, jitter)))
        pts.append((x, y))
    return pts


def draw_path(c, pts, core_w, glow_a, core_a=255, swell=False):
    # Walk each segment pixel by pixel; at each point draw the haze, the glow
    # and the core as horizontal runs, widest first. With swell, every other
    # segment is a pixel wider in core and edge, so a long stem pulses thick
    # and thin like a real channel instead of reading as one thin line.
    def width(k):
        return core_w + (1 if swell and k % 2 == 0 else 0)

    for k, ((x0, y0), (x1, y1)) in enumerate(zip(pts, pts[1:])):
        w = width(k)
        n = max(abs(x1 - x0), abs(y1 - y0), 1)
        for i in range(n + 1):
            x = x0 + (x1 - x0) * i / n
            y = y0 + (y1 - y0) * i / n
            for dx in range(-w - 3, w + 3):
                # The haze thins toward its edges.
                edge = abs(dx + 0.5) / (w + 3)
                c.put(x + dx, y, BOLT_HAZE, glow_a * 0.85 * (1 - edge * edge))
            for dx in range(-w - 1, w + 1):
                c.put(x + dx, y, BOLT_GLOW, glow_a)
    for k, ((x0, y0), (x1, y1)) in enumerate(zip(pts, pts[1:])):
        w = width(k)
        n = max(abs(x1 - x0), abs(y1 - y0), 1)
        for i in range(n + 1):
            x = x0 + (x1 - x0) * i / n
            y = y0 + (y1 - y0) * i / n
            for dx in range(w):
                c.put(x + dx - w // 2, y, BOLT_CORE, core_a)


def paint_bolt(seed):
    rnd = random.Random(seed)
    W, H = BOLT
    c = Canvas(W, H)
    trunk = channel(rnd, rnd.randint(26, 38), 0, H - 1, (5, 12), 6, 10, W - 11)
    draw_path(c, trunk, 2, 210, swell=True)
    # Forks leave the trunk downward and fade by drawing thinner and dimmer.
    for _ in range(rnd.randint(2, 3)):
        i = rnd.randint(2, len(trunk) - 6)
        x, y = trunk[i]
        side = rnd.choice((-1, 1))
        length = rnd.randint(30, 70)
        pts = [(x, y)]
        while pts[-1][1] < min(H - 8, y + length):
            px, py = pts[-1]
            pts.append((max(5, min(W - 6, px + side * rnd.randint(1, 5) + rnd.randint(-2, 2))),
                        py + rnd.randint(4, 9)))
        draw_path(c, pts, 1, 150, 230)
    return c.image()


def paint_lightning(seed):
    return strip([paint_bolt(seed), paint_bolt(seed + 1)])


# --- splashes ---------------------------------------------------------------
#
# One drop landing on the centre of the frame (the spawn point): the drop's
# tail, a crown of droplets thrown up and out, and a ripple ring that widens
# and fades. The ring is an ellipse, flattened for the game's slanted view of
# the ground.

SPLASH_WATER = rgb("a3c1dc")
SPLASH_BRIGHT = rgb("eaf2f8")
SPLASH_DARK = rgb("6d8299")
# The first instant of an impact is the brightest thing in the splash.
SPLASH_FLASH = rgb("ffffff")


def dot(c, x, y, col, a):
    """A 2x2 droplet: one art pixel of the old 16 px grid."""
    x, y = round(x), round(y)
    for dx, dy in ((0, 0), (1, 0), (0, 1), (1, 1)):
        c.put(x + dx, y + dy, col, a)


def ring(c, cx, cy, rx, ry, col, a):
    steps = max(24, int(8 * (rx + ry)))
    seen = set()
    for i in range(steps):
        t = 2 * math.pi * i / steps
        p = (round(cx + rx * math.cos(t)), round(cy + ry * math.sin(t)))
        if p in seen:
            continue
        seen.add(p)
        # A thin line; the near (lower) half catches a little more light.
        c.put(p[0], p[1], col, a * (1.0 if math.sin(t) > 0 else 0.6))


def paint_rain_splash():
    W, H = SPLASH
    cx, cy = W // 2, H // 2
    frames = []
    # Crown droplets: (angle from vertical, speed), thrown up and out.
    drops = [(-1.0, 1.0), (-0.5, 1.25), (0.05, 1.35), (0.55, 1.2), (1.0, 1.0)]
    for f in range(FRAMES):
        c = Canvas(W, H)
        if f == 0:
            # The drop arriving: its last streak, head on the ground.
            for k in range(10):
                col = mix(SPLASH_WATER, SPLASH_BRIGHT, k / 9)
                c.put(cx, cy - 9 + k, col, 150 + 11 * k)
                c.put(cx + 1, cy - 9 + k, col, 130 + 11 * k)
            # Where it strikes: a bright bead on the ground, a little wider
            # than the drop.
            dot(c, cx, cy, SPLASH_FLASH, 255)
            c.put(cx - 1, cy + 1, SPLASH_BRIGHT, 220)
            c.put(cx + 2, cy + 1, SPLASH_BRIGHT, 220)
        if 1 <= f <= 5:
            t = f / 5.0
            for ang, sp in drops:
                # A short ballistic arc: up, then back down by frame 5.
                x = cx + math.sin(ang) * sp * 9 * t
                y = cy - math.cos(ang) * sp * 12 * t + 14 * t * t
                if y <= cy:
                    # Fresh droplets (the first two frames) catch the most light.
                    col = SPLASH_FLASH if f <= 2 else SPLASH_BRIGHT
                    dot(c, x, y - 1, col, 255 * min(1.0, 1.35 - t))
        if 1 <= f <= 2:
            # The crown's foot: a cup of water standing up off the ground.
            for dx in range(-3, 5):
                rim = dx in (-3, 4)
                c.put(cx + dx, cy - (2 if rim else 0), SPLASH_BRIGHT if rim else SPLASH_WATER, 235)
                c.put(cx + dx, cy - (1 if rim else -1), SPLASH_WATER, 210)
        if 2 <= f <= 7:
            t = (f - 2) / 5.0
            ring(c, cx, cy, 3 + 10 * t, 1.2 + 3.6 * t, SPLASH_WATER, 170 * (1 - t) ** 1.1)
        frames.append(c.image())
    return strip(frames)


def paint_snow_splash():
    W, H = SPLASH
    cx, cy = W // 2, H // 2
    frames = []
    # Specks knocked loose, sliding low and outward, never high.
    specks = [(-6, -1), (-4, -2), (-1, -3), (2, -3), (5, -2), (7, -1)]
    for f in range(FRAMES):
        c = Canvas(W, H)
        if f <= 1:
            flake(c, cx, cy - 6 + 5 * f, 4, 255)
        if 1 <= f <= 6:
            t = (f - 1) / 5.0
            for sx, sy in specks:
                x = cx + sx * (0.4 + 0.9 * t)
                y = cy + sy * math.sin(math.pi * t) * 1.5
                dot(c, x, y, FLAKE_CORE, 240 * (1 - t) ** 0.8)
        if 2 <= f <= 7:
            # The little mound the flake leaves, melting into the ground.
            t = (f - 2) / 5.0
            for dx in range(-2, 4):
                c.put(cx + dx, cy + 1, FLAKE_RIM, 230 * (1 - t))
            for dx in range(-1, 3):
                c.put(cx + dx, cy, FLAKE_CORE, 240 * (1 - t))
        frames.append(c.image())
    return strip(frames)


# --- output -----------------------------------------------------------------

SEEDS = {"rain-light": 9701, "rain-heavy": 9702, "snow-light": 9703, "snow-heavy": 9704}


def paint_all():
    return {
        "rain-light.png": paint_rain("light", SEEDS["rain-light"]),
        "rain-heavy.png": paint_rain("heavy", SEEDS["rain-heavy"]),
        "snow-light.png": paint_snow("light", SEEDS["snow-light"]),
        "snow-heavy.png": paint_snow("heavy", SEEDS["snow-heavy"]),
        "lightning.png": paint_lightning(9705),
        "rain-splash.png": paint_rain_splash(),
        "snow-splash.png": paint_snow_splash(),
    }


SIZES = {
    "rain-light.png": (TILE[0] * FRAMES, TILE[1]),
    "rain-heavy.png": (TILE[0] * FRAMES, TILE[1]),
    "snow-light.png": (TILE[0] * FRAMES, TILE[1]),
    "snow-heavy.png": (TILE[0] * FRAMES, TILE[1]),
    "lightning.png": (BOLT[0] * 2, BOLT[1]),
    "rain-splash.png": (SPLASH[0] * FRAMES, SPLASH[1]),
    "snow-splash.png": (SPLASH[0] * FRAMES, SPLASH[1]),
}


def check_wrap_canvas():
    """A wrapping canvas lands a write past any edge on the opposite edge,
    which is what makes a streak or flake crossing the tile's border continue
    on the far side of the next tile."""
    ok = True
    W, H = TILE
    for (x, y), want in (((W, 5), (0, 5)), ((-1, 5), (W - 1, 5)),
                         ((7, H), (7, 0)), ((7, -1), (7, H - 1)), ((-1, -1), (W - 1, H - 1))):
        c = Canvas(W, H, wrap=True)
        c.put(x, y, (255, 255, 255), 255)
        if c.px[want[1]][want[0]][3] != 255:
            print(f"wrap: a write at {(x, y)} did not land at {want}")
            ok = False
    return ok


def check_loops():
    """One whole loop on (frame FRAMES), every falling sheet must draw
    exactly its frame 0: the motion closes, so the animation has no jump.
    Also some rain frame must have a streak on both its top and its bottom
    row (one streak crossing the border, drawn on both sides by the wrap),
    so the shipped rain really exercises the wrap. Snow is not held to this:
    a flake moves 32 px a frame and may never sit on the border in any frame;
    check_wrap_canvas covers the wrap itself."""
    ok = True
    W, H = TILE
    for kind in ("light", "heavy"):
        for name, frames_of in (("rain", rain_frames), ("snow", snow_frames)):
            seed = SEEDS[f"{name}-{kind}"]
            frames = frames_of(kind, seed, list(range(FRAMES + 1)))
            if frames[0].tobytes() != frames[FRAMES].tobytes():
                print(f"{name}-{kind}: frame {FRAMES} is not frame 0 (the loop jumps)")
                ok = False
            if name == "rain" and not any(crosses_bottom(f) for f in frames[:FRAMES]):
                print(f"{name}-{kind}: no frame has a shape crossing the tile's top/bottom edge")
                ok = False
    return ok


def crosses_bottom(frame):
    """True when some column has paint on both the top and the bottom row.
    Streak columns are kept apart (RAIN_MIN_GAP), so paint at the same x on
    both rows is one streak running across the seam, not two streaks."""
    W, H = frame.size
    px = frame.load()
    return any(px[x, 0][3] and px[x, H - 1][3] for x in range(W))


def check_rain_spacing():
    ok = True
    W = TILE[0]
    for kind in ("light", "heavy"):
        cols = [d["x"] for d in rain_drops(kind, SEEDS[f"rain-{kind}"])]
        gaps = circular_gaps(cols, W)
        if len(set(cols)) != len(cols) or min(gaps) < RAIN_MIN_GAP:
            print(f"rain-{kind}: streak columns closer than {RAIN_MIN_GAP} (gaps {sorted(gaps)})")
            ok = False
    return ok


def check(images, out_dir):
    ok = True
    for name, im in images.items():
        if im.size != SIZES[name]:
            print(f"{name}: size {im.size}, want {SIZES[name]}")
            ok = False
        on_disk = os.path.join(out_dir, name)
        if not os.path.exists(on_disk):
            print(f"{name}: not written")
            ok = False
            continue
        disk = Image.open(on_disk).convert("RGBA")
        if disk.size != SIZES[name]:
            print(f"{name}: on disk {disk.size}, want {SIZES[name]}")
            ok = False
        elif disk.tobytes() != im.tobytes():
            print(f"{name}: differs from a fresh paint (run without --check)")
            ok = False
    # A splash or bolt touching its frame's edge would be cut off on screen.
    # A bolt runs the full height on purpose; a splash must clear all four.
    for name in ("rain-splash.png", "snow-splash.png", "lightning.png"):
        im = images[name]
        splash = "splash" in name
        fw = SPLASH[0] if splash else BOLT[0]
        for i in range(im.size[0] // fw):
            box = im.crop((i * fw, 0, (i + 1) * fw, im.size[1])).getbbox()
            if box and (box[0] == 0 or box[2] == fw or (splash and (box[1] == 0 or box[3] == im.size[1]))):
                print(f"{name}: frame {i} touches its edge {box}")
                ok = False
    return check_wrap_canvas() and check_loops() and check_rain_spacing() and ok


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--out", default=OUT_DIR)
    ap.add_argument("--check", action="store_true")
    args = ap.parse_args()
    images = paint_all()
    if args.check:
        sys.exit(0 if check(images, args.out) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name, im in images.items():
        im.save(os.path.join(args.out, name))
        print(os.path.join(args.out, name), im.size)


if __name__ == "__main__":
    main()
