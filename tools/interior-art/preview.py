"""A room as the window shows it, for judging the art: the room sheet, the
anims' first frames, then the pieces and people sorted by their feet / sort
lines, each bed's sleeper under its cover, at a whole-pixel zoom. People
stand at the floor plan's stations (engine/sim/roomlayout/layouts.json), so
the preview shows where they stand and how they layer with the furniture:
an anchor and layering view, not a pose check. Everyone is drawn as the same
upright body at their feet point (the client draws a sleeper dozing and a
sitter seated), with a bed's cover drawn again over its sleeper.

    python tools/interior-art/preview.py <room key> <sheet dir> <out.png> [zoom]
        [--villager PNG] [--density D]

Geometry is in room px; the art is at the room's density (its entry's
"density", or --density), so rects and positions are scaled by it and the
people (drawn at one art px per room px) are scaled up to match. The
villager is any 64x64 salem body frame (feet at its bottom middle, y 44);
without one a plain figure stands in.
"""

import argparse
import json
import os

from PIL import Image, ImageDraw

import common as C

FEET = (32, 44)


def figure():
    im = Image.new("RGBA", (64, 64), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    d.ellipse((27, 13, 37, 23), fill=(222, 186, 150, 255))
    d.rectangle((26, 23, 38, 38), fill=(70, 90, 120, 255))
    d.rectangle((27, 38, 37, 44), fill=(50, 50, 60, 255))
    return im


def plan_for(art):
    data = json.load(open(os.path.join(C.ROOT, "engine", "sim", "roomlayout", "layouts.json"), encoding="utf-8"))
    for r in data["rooms"]:
        if r.get("art") == art:
            return r
    return {}


def render(key, sheet_dir, villager=None, density=None):
    entry = C.room_entry(key)
    d = density or entry["density"]

    def sheet(path):
        return Image.open(os.path.join(sheet_dir, os.path.basename(path))).convert("RGBA")

    def box(x, y, w, h):
        return (x * d, y * d, (x + w) * d, (y + h) * d)

    room = sheet(entry["sheets"]["sheet"]).copy()
    pieces = sheet(entry["sheets"]["pieces_sheet"])
    for path, frames, x, y in entry["anims"]:
        strip = sheet(path)
        fw = strip.width // frames
        room.alpha_composite(strip.crop((0, 0, fw, strip.height)), (x * d, y * d))
    body = villager or figure()
    body = body.resize((body.width * d, body.height * d), Image.NEAREST)
    plan = plan_for(key)
    items = [(sy, "piece", (x, y, w, h)) for x, y, w, h, sy in entry["pieces"]]
    for st in plan.get("stations", []):
        ax, ay = st["at"]
        items.append((ay + 0.5, "person", (ax, ay, st.get("id", ""), st.get("pose", ""))))
    for _sy, kind, v in sorted(items, key=lambda t: t[0]):
        if kind == "piece":
            b = box(*v)
            room.alpha_composite(pieces.crop(b), b[:2])
        else:
            ax, ay, sid, pose = v
            room.alpha_composite(body, ((ax - FEET[0]) * d, (ay - FEET[1]) * d))
            cover = entry["covers"].get(sid)
            if pose == "sleep" and cover:
                b = box(*cover)
                room.alpha_composite(pieces.crop(b), b[:2])
    return room


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("key")
    ap.add_argument("sheet_dir")
    ap.add_argument("out")
    ap.add_argument("zoom", nargs="?", type=int, default=2)
    ap.add_argument("--villager")
    ap.add_argument("--density", type=int)
    a = ap.parse_args()
    vil = Image.open(a.villager).convert("RGBA") if a.villager else None
    room = render(a.key, a.sheet_dir, vil, a.density)
    empty = render(a.key, a.sheet_dir, Image.new("RGBA", (64, 64), (0, 0, 0, 0)), a.density)
    out = Image.new("RGBA", (room.width * 2 + 8, room.height), (0, 0, 0, 255))
    out.paste(empty, (0, 0))
    out.paste(room, (room.width + 8, 0))
    out = out.resize((out.width * a.zoom, out.height * a.zoom), Image.NEAREST)
    out.save(a.out)
    print(a.out, out.size)


if __name__ == "__main__":
    main()
