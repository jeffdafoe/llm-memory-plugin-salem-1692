"""Paints salem's fish (LLM-967): small schools seen from above, just under
the surface. Every pixel is drawn here, no pack art.

    python tools/tree-art/fish.py
    python tools/tree-art/fish.py --out DIR   # elsewhere (prototypes)
    python tools/tree-art/fish.py --check     # every frame inside its frame, sheets their size

Writes client/assets/trees/<piece>.png. Each sheet is one row: a seamless
FRAMES-long swim at FPS, left to right, at the piece's frame size. The water
does not freeze in the village, so a school has the one look all year.
Rerunning writes the same files. After a change, run
`godot_console --headless --path client --import` (or the Linux Godot) so
Godot writes the .import sidecars, and commit them.

It imports build.py (the trees) for its scale and colour helpers, so the fish
are painted at build.S times size and area-averaged down like everything else.

How a fish moves:
- Each fish swims a closed path of its own (a ring for a milling school, a
  figure eight for a cruising trout), drawn on the water's plane and then
  squashed by SQUASH, the angle the game looks at the water from.
- Its body follows its own path: the spine is the path behind the head, so a
  fish bends as it turns. A tail beat runs down the spine on top of that.
- It swims in bursts and glides: its pace along the path rises and falls, and
  the tail beats harder and faster in a burst and nearly stills in a glide.
  The beats are a whole number per loop, so the loop closes.
- It rises and sinks a little: a deeper fish is fainter and its shadow on the
  bed falls further from it.
- Shiners flash: a shiner turning hard shows its silver side for a moment.
"""

import argparse
import math
import os
import random
import sys

from PIL import Image, ImageDraw

import build as B

S = B.S
OUT_DIR = B.OUT_DIR
FRAMES = 48
FPS = 8
SQUASH = 0.8                   # the water's plane seen from the game's angle
LIGHT = (-0.55, -0.83)         # from the upper left, as the trees
SHADOW_RGB = (8, 26, 34)       # the shadow a fish throws on the bed
SHADOW_ALPHA = 64
RISE = ["a6d6db", "c8e6ea", "e8f6f6"]   # the water's light tones (terrain SHALLOW_WATER)


class Species:
    def __init__(self, length, half_width, back, side, light, glint, fin=None, mark=None,
                 bars=0, spots=0, fins=False, flash=False, tail=0.42):
        self.length = length            # frame px, nose to tail fin
        self.half_width = half_width    # frame px, at the widest
        self.back = back                # the dark back, seen from above
        self.side = side                # the flanks
        self.light = light              # the flank toward the light
        self.glint = glint
        self.fin = fin
        self.mark = mark
        self.bars = bars                # dark bars across the back (perch)
        self.spots = spots              # pale marks on the back (trout)
        self.fins = fins                # pectoral fins show
        self.flash = flash              # the side flashes on a hard turn
        self.tail = tail                # tail-fin spread, a share of the length

    def palette(self):
        return [self.back, self.side, self.light, self.glint] + \
            [c for c in (self.fin, self.mark) if c]


SHINER = Species(9.0, 1.3, back="465e58", side="7a948e", light="a2bab4", glint="dfece8", flash=True)
PERCH = Species(11.0, 1.75, back="2a3418", side="6e7a2c", light="949c44", glint="d2d48a",
                fin="56602a", mark="1c2412", bars=4, fins=True)
TROUT = Species(15.0, 2.0, back="1e2a1e", side="4c5a36", light="6e7a4e", glint="c8cca0",
                fin="3a4630", mark="a2aa76", spots=8, fins=True, tail=0.36)


class Fish:
    def __init__(self, path, loops, centre, radius, phase, burst, bursts, beats, depth, depth_amp,
                 depth_phase, scale=1.0, wobble=0.12, wobble_phase=0.0, direction=1):
        self.path = path                # "ring" or "eight"
        self.loops = loops              # times round its path per loop
        self.centre = centre            # frame px from the frame's middle, on the plane
        self.radius = radius            # frame px
        self.phase = phase              # where on its path it starts, 0..1
        self.burst = burst              # how strongly the pace rises and falls, 0..0.95
        self.bursts = bursts            # bursts per loop
        self.beats = beats              # tail beats per loop
        self.depth = depth              # 0 at the surface, 1 deep
        self.depth_amp = depth_amp
        self.depth_phase = depth_phase
        self.scale = scale              # this fish's size against its kind
        self.wobble = wobble            # how far its ring strays from a circle
        self.wobble_phase = wobble_phase
        self.direction = direction


class Piece:
    def __init__(self, frame, species, fish, rise=None):
        self.frame = frame
        self.species = species
        self.fish = fish
        self.rise = rise                # (fish index, t) a fish dimples the surface


def school(seed, count, loops, r0, r1, depth, burst, beats, spread=2.0, scale=(0.85, 1.1)):
    """A milling school: every fish round the same way, each on its own ring,
    spaced round it, bursting at its own moment."""
    rnd = random.Random(seed)
    out = []
    for i in range(count):
        out.append(Fish("ring", loops,
                        centre=(rnd.uniform(-spread, spread), rnd.uniform(-spread, spread)),
                        radius=rnd.uniform(r0, r1),
                        phase=i / count + rnd.uniform(-0.04, 0.04),
                        burst=rnd.uniform(burst * 0.7, burst),
                        bursts=rnd.choice([2, 3]) * loops,
                        beats=beats * loops + rnd.randint(-1, 1),
                        depth=rnd.uniform(depth[0], depth[1]),
                        depth_amp=rnd.uniform(0.1, 0.25),
                        depth_phase=rnd.random(),
                        scale=rnd.uniform(*scale),
                        wobble=rnd.uniform(0.05, 0.18),
                        wobble_phase=rnd.random()))
    return out


PIECES = {
    "shiners": Piece((48, 48), SHINER, school(11, 5, 2, 9.5, 12.0, (0.0, 0.4), 0.55, 9, spread=1.0), rise=(2, 0.18)),
    "perch": Piece((48, 48), PERCH, school(23, 3, 1, 8.0, 11.5, (0.1, 0.45), 0.6, 8, spread=1.5)),
    "brook-trout": Piece((48, 48), TROUT, [
        Fish("hold", 1, centre=(3.0, 0.5), radius=0.0, phase=0.0, burst=0.0, bursts=1, beats=7,
             depth=0.4, depth_amp=0.08, depth_phase=0.1),
    ], rise=(0, None)),
}

# A holding trout: it faces upstream (HOLD_HEADING, on the plane), sways a
# little and drifts, and once a loop darts DART_DIST forward to the surface
# over DART_OUT of the loop, dimples it, and drops back over DART_BACK.
HOLD_HEADING = math.pi + 0.12
DART_AT, DART_OUT, DART_BACK, DART_DIST = 0.55, 0.07, 0.3, 7.0


# ---------------------------------------------------------------- motion

def use_frame(fw, fh):
    global FW, FH, W, H
    FW, FH = fw, fh
    W, H = fw * S, fh * S


def path_at(f, s):
    """The point at s (0..1 round the path) on the water's plane, frame px from
    the frame's middle."""
    a = 2 * math.pi * s * f.direction
    if f.path == "eight":
        return (f.centre[0] + f.radius * math.sin(a), f.centre[1] + f.radius * 0.42 * math.sin(2 * a))
    wob = 1.0 + f.wobble * math.cos(2 * a + 2 * math.pi * f.wobble_phase)
    return (f.centre[0] + f.radius * wob * math.cos(a), f.centre[1] + f.radius * wob * math.sin(a))


def path_length(f):
    n = 400
    pts = [path_at(f, k / n) for k in range(n + 1)]
    return max(1.0, sum(math.dist(pts[k], pts[k + 1]) for k in range(n)))


def pace(f, t):
    """Where the fish is round its path at t (0..1 through the loop), and how
    fast it is going against its mean pace. The pace rises and falls bursts
    times a loop; it never stops or turns back (burst < 1)."""
    k = f.bursts
    s = f.loops * (t + f.phase + f.burst * math.sin(2 * math.pi * k * t) / (2 * math.pi * k))
    speed = 1.0 + f.burst * math.cos(2 * math.pi * k * t)
    return s, speed


def tail_phase(f, t):
    """The tail beat, a whole number of beats a loop, quicker in a burst."""
    k = f.bursts
    return 2 * math.pi * f.beats * (t + f.burst * math.sin(2 * math.pi * k * t) / (2 * math.pi * k))


def dart(t):
    """A holding fish's dart: how far forward it is (0..1), and how far
    through the dart out (0 before it, 1 from its top on)."""
    u = (t - DART_AT) % 1.0
    if u < DART_OUT:
        k = u / DART_OUT
        return 1.0 - (1.0 - k) ** 2, k
    if u < DART_OUT + DART_BACK:
        k = (u - DART_OUT) / DART_BACK
        return 1.0 - k * k * (3 - 2 * k), 1.0
    return 0.0, 1.0


def depth_at(f, t):
    d = f.depth + f.depth_amp * math.sin(2 * math.pi * (t + f.depth_phase))
    if f.path == "hold":
        d *= 1.0 - dart(t)[0]
    return min(1.0, max(0.0, d))


def turn_rate(f, s, plen):
    """How hard the fish is turning at s: the change of heading over a body
    length, 0 straight on, about 1 on a tight turn."""
    ds = 1.5 / plen
    p0, p1, p2 = path_at(f, s - ds), path_at(f, s), path_at(f, s + ds)
    a0 = math.atan2(p1[1] - p0[1], p1[0] - p0[0])
    a1 = math.atan2(p2[1] - p1[1], p2[0] - p1[0])
    d = (a1 - a0 + math.pi) % (2 * math.pi) - math.pi
    return abs(d) * 4.0


# ---------------------------------------------------------------- a fish

def hold_base(f, length, n, t):
    """A holding fish's spine before the tail beat: head upstream, swaying a
    little, drifting, darting forward and dropping back. Returns the points,
    its speed against a cruise, and the tail beat's phase."""
    fwd, out = dart(t)
    yaw = 0.1 * math.sin(2 * math.pi * 2 * t)
    hx = f.centre[0] + 0.7 * math.sin(2 * math.pi * t) + DART_DIST * fwd * math.cos(HOLD_HEADING)
    hy = f.centre[1] + 0.5 * math.sin(4 * math.pi * t + 1.0) + DART_DIST * fwd * math.sin(HOLD_HEADING)
    pts = []
    for k in range(n + 1):
        u = k / n
        a = HOLD_HEADING + yaw * u
        pts.append((hx - math.cos(a) * u * length * 0.82, hy - math.sin(a) * u * length * 0.82))
    u = (t - DART_AT) % 1.0
    speed = 2.2 if u < DART_OUT else 0.9 if u < DART_OUT + DART_BACK * 0.4 else 0.55
    # whole beats a loop, and two more in the dart, so the loop closes
    ph = 2 * math.pi * (f.beats * t + 2 * out)
    return pts, speed, ph


def spine(f, sp, t, plen):
    """The spine, head to tail, on the plane: the path behind the head, with
    the tail beat running down it. Returns the points and the widths."""
    length = sp.length * f.scale
    n = 14
    if f.path == "hold":
        base, speed, ph = hold_base(f, length, n, t)
    else:
        s, speed = pace(f, t)
        ph = tail_phase(f, t)
        base = [path_at(f, s - (k / n) * length * 0.82 / plen) for k in range(n + 1)]
    amp = length * 0.075 * (0.35 + 0.65 * min(1.6, speed) / 1.6)
    pts = []
    for k in range(n + 1):
        u = k / n
        a, b = base[max(0, k - 1)], base[min(n, k + 1)]
        tx, ty = b[0] - a[0], b[1] - a[1]
        m = math.hypot(tx, ty) or 1.0
        nx, ny = -ty / m, tx / m
        lat = amp * (u ** 1.6) * math.sin(ph - u * 2.4)
        pts.append((base[k][0] + nx * lat, base[k][1] + ny * lat))
    widths = []
    for k in range(n + 1):
        u = k / n
        head = min(1.0, 0.45 + u * 3.2)               # a blunt, rounded head
        body = (1.0 - u) ** 0.75                      # tapering to the tail
        widths.append(sp.half_width * f.scale * max(0.16, head * body * 1.1))
    return pts, widths


def to_big(p):
    """Plane (frame px from the middle) to the painting layer."""
    return ((FW * 0.5 + p[0]) * S, (FH * 0.5 + p[1] * SQUASH) * S)


def normals(pts):
    out = []
    n = len(pts) - 1
    for k in range(n + 1):
        a, b = pts[max(0, k - 1)], pts[min(n, k + 1)]
        tx, ty = b[0] - a[0], b[1] - a[1]
        m = math.hypot(tx, ty) or 1.0
        out.append((-ty / m, tx / m))
    return out


def body_poly(pts, widths, nrm, scale=1.0, shift=(0.0, 0.0)):
    left = [(p[0] + n[0] * w * scale + shift[0], p[1] + n[1] * w * scale + shift[1])
            for p, w, n in zip(pts, widths, nrm)]
    right = [(p[0] - n[0] * w * scale + shift[0], p[1] - n[1] * w * scale + shift[1])
             for p, w, n in zip(pts, widths, nrm)]
    # a rounded nose ahead of the head
    hx, hy = pts[0][0] - pts[1][0], pts[0][1] - pts[1][1]
    m = math.hypot(hx, hy) or 1.0
    nose = (pts[0][0] + hx / m * widths[0] * 0.8 * scale + shift[0],
            pts[0][1] + hy / m * widths[0] * 0.8 * scale + shift[1])
    return [nose] + left + right[::-1]


def paint_fish(f, sp, t, plen, rnd_seed):
    """One fish, on a layer the size of the painting, in its own colours."""
    pts, widths = spine(f, sp, t, plen)
    nrm = normals(pts)
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    big = lambda poly: [to_big(p) for p in poly]
    n = len(pts) - 1
    length = sp.length * f.scale

    # the tail fin: a forked fan off the end of the spine
    tx, ty = pts[n][0] - pts[n - 1][0], pts[n][1] - pts[n - 1][1]
    m = math.hypot(tx, ty) or 1.0
    bx, by = tx / m, ty / m
    nx, ny = nrm[n]
    fl = length * 0.2
    spread = length * sp.tail * 0.5
    root = pts[n - 1]
    tail = [root,
            (pts[n][0] + nx * spread + bx * fl, pts[n][1] + ny * spread + by * fl),
            (pts[n][0] + bx * fl * 0.45, pts[n][1] + by * fl * 0.45),
            (pts[n][0] - nx * spread + bx * fl, pts[n][1] - ny * spread + by * fl)]
    d.polygon(big(tail), fill=B.rgb(sp.fin or sp.side))

    # pectoral fins, swept back from behind the head
    if sp.fins:
        k = 3
        for side in (1, -1):
            px, py = pts[k]
            ex, ey = nrm[k][0] * side, nrm[k][1] * side
            hx, hy = pts[k + 2][0] - px, pts[k + 2][1] - py
            fin = [(px + ex * widths[k] * 0.6, py + ey * widths[k] * 0.6),
                   (px + ex * widths[k] * 1.55 + hx * 0.7, py + ey * widths[k] * 1.55 + hy * 0.7),
                   (px + ex * widths[k] * 0.7 + hx, py + ey * widths[k] * 0.7 + hy)]
            d.polygon(big(fin), fill=B.rgb(sp.fin))

    # the body: flanks, the flank toward the light, then the dark back down
    # the middle, set a touch away from the light
    turn = turn_rate(f, pace(f, t)[0], plen)
    side = sp.side
    if sp.flash and turn > 0.7:
        side = sp.glint if turn > 1.0 else sp.light
    d.polygon(big(body_poly(pts, widths, nrm)), fill=B.rgb(side))
    shade = (-LIGHT[0] * 0.25, -LIGHT[1] * 0.25)
    d.polygon(big(body_poly(pts[:n - 2], widths[:n - 2], nrm[:n - 2], 0.5, shade)), fill=B.rgb(sp.back))

    # bars across the back (perch)
    for b in range(sp.bars):
        k = 3 + b * 2
        if k + 1 >= n:
            break
        bar = [(pts[k][0] + nrm[k][0] * widths[k] * 0.95, pts[k][1] + nrm[k][1] * widths[k] * 0.95),
               (pts[k + 1][0] + nrm[k + 1][0] * widths[k + 1] * 0.95, pts[k + 1][1] + nrm[k + 1][1] * widths[k + 1] * 0.95),
               (pts[k + 1][0] - nrm[k + 1][0] * widths[k + 1] * 0.95, pts[k + 1][1] - nrm[k + 1][1] * widths[k + 1] * 0.95),
               (pts[k][0] - nrm[k][0] * widths[k] * 0.95, pts[k][1] - nrm[k][1] * widths[k] * 0.95)]
        d.polygon(big(bar), fill=B.rgb(sp.mark))

    # pale marks on the back (trout), fixed to the fish
    rnd = random.Random(rnd_seed)
    for _ in range(sp.spots):
        k = rnd.randint(2, n - 3)
        off = rnd.uniform(-0.5, 0.5)
        cx, cy = to_big((pts[k][0] + nrm[k][0] * widths[k] * off, pts[k][1] + nrm[k][1] * widths[k] * off))
        r = S * 0.32
        d.ellipse((cx - r, cy - r * 0.8, cx + r, cy + r * 0.8), fill=B.rgb(sp.mark))

    # a glint on the head, toward the light
    gx, gy = to_big((pts[1][0] + LIGHT[0] * widths[1] * 0.5, pts[1][1] + LIGHT[1] * widths[1] * 0.5))
    r = S * 0.38
    d.ellipse((gx - r, gy - r, gx + r, gy + r), fill=B.rgb(sp.glint))
    return lay


def rise_ring(t0, t, centre):
    """A dimple where a fish touched the surface: a ring that widens and
    fades over a third of the loop."""
    age = (t - t0) % 1.0
    if age > 0.34:
        return None
    k = age / 0.34
    lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cx, cy = to_big(centre)
    rx = (1.2 + k * 7.0) * S
    ry = rx * SQUASH * 0.75
    col = RISE[2] if k < 0.3 else RISE[1] if k < 0.65 else RISE[0]
    d.ellipse((cx - rx, cy - ry, cx + rx, cy + ry), outline=B.rgb(col), width=max(2, int(0.55 * S)))
    return lay, int(230 * (1.0 - k) ** 0.8)


# ---------------------------------------------------------------- to pixels

def to_pixels(lay, palette):
    """To frame size: area average, every covered pixel snapped to the
    species' own colours. Returns {(x, y): rgb}."""
    small = lay.resize((FW, FH), Image.BOX)
    sp = small.load()
    pal = [B.rgb(h)[:3] for h in palette]
    out = {}
    for y in range(FH):
        for x in range(FW):
            if sp[x, y][3] >= 110:
                out[(x, y)] = B.nearest(sp[x, y][:3], pal)
    return out


def paint_frame(piece, col):
    t = col / FRAMES
    sp = piece.species
    out = Image.new("RGBA", (FW, FH), (0, 0, 0, 0))
    lens = [path_length(f) for f in piece.fish]
    fish = []
    for i, f in enumerate(piece.fish):
        px = to_pixels(paint_fish(f, sp, t, lens[i], 1000 + i), sp.palette())
        fish.append((depth_at(f, t), px))
    # the shadows on the bed, under every fish, one shade however they cross
    shadow = set()
    for dep, px in fish:
        ox, oy = round(1.0 + 2.0 * dep), round(1.5 + 2.5 * dep)
        shadow |= {(x + ox, y + oy) for (x, y) in px}
    op = out.load()
    for (x, y) in shadow:
        if 0 <= x < FW and 0 <= y < FH:
            op[x, y] = SHADOW_RGB + (SHADOW_ALPHA,)
    # the rise: a ring on the surface, over the shadows
    if piece.rise:
        i, t0 = piece.rise
        f = piece.fish[i]
        if t0 is None:                        # a holding fish rises at the top of its dart
            t0 = DART_AT + DART_OUT
        r = rise_ring(t0, t, spine(f, sp, t0, lens[i])[0][0])
        if r:
            lay, alpha = r
            for (x, y), c in to_pixels(lay, RISE).items():
                op[x, y] = c + (alpha,)
    # the fish, deepest first: a deeper fish is fainter
    for dep, px in sorted(fish, key=lambda p: -p[0]):
        a = int(255 - 80 * dep)
        for (x, y), c in px.items():
            op[x, y] = c + (a,)
    return out


def build_sheet(piece):
    use_frame(*piece.frame)
    sheet = Image.new("RGBA", (FW * FRAMES, FH), (0, 0, 0, 0))
    for col in range(FRAMES):
        sheet.paste(paint_frame(piece, col), (col * FW, 0))
    return sheet


def check(out_dir, names):
    """Every frame stays a pixel inside its frame on all four sides, holds
    some art, and the sheet is its size."""
    ok = True
    for name in names:
        piece = PIECES[name]
        use_frame(*piece.frame)
        sheet = Image.open(os.path.join(out_dir, name + ".png")).convert("RGBA")
        want = (FW * FRAMES, FH)
        if sheet.size != want:
            print("%s: sheet is %dx%d, want %dx%d" % ((name,) + sheet.size + want))
            ok = False
            continue
        bad = []
        for col in range(FRAMES):
            frame = sheet.crop((col * FW, 0, (col + 1) * FW, FH))
            px = frame.load()
            edge = any(px[x, y][3] > 0 for y in range(FH) for x in (0, FW - 1)) or \
                any(px[x, y][3] > 0 for x in range(FW) for y in (0, FH - 1))
            if edge or frame.getbbox() is None:
                bad.append(col)
        ok = ok and not bad
        print("%-12s %2d frames%s" % (name, FRAMES, "" if not bad else "  <- on the frame edge or empty: %s" % bad))
    return ok


def main():
    parser = argparse.ArgumentParser(description="Paint salem's fish.")
    parser.add_argument("--piece", default=",".join(PIECES), help="comma-separated, of: " + ", ".join(PIECES))
    parser.add_argument("--out", default=OUT_DIR, help="where the sheets go (default client/assets/trees)")
    parser.add_argument("--check", action="store_true", help="check the sheets instead of painting them")
    args = parser.parse_args()
    names = args.piece.split(",")
    for name in names:
        if name not in PIECES:
            parser.error("unknown piece %r (have %s)" % (name, ", ".join(PIECES)))
    if args.check:
        sys.exit(0 if check(args.out, names) else 1)
    os.makedirs(args.out, exist_ok=True)
    for name in names:
        build_sheet(PIECES[name]).save(os.path.join(args.out, name + ".png"))
        print("painted", name)


if __name__ == "__main__":
    main()
