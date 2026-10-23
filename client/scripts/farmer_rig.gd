class_name FarmerRig
## Animation table of the Mana Seed Farmer Sprite System base (LLM-691),
## transcribed from the pack's "farmer base animation guide.png". Every layer
## sheet of the base shares one layout: 1024x1024, sliced into 64x64 cells
## numbered row-major (cell n sits at column n % 16, row n / 16). One table
## therefore animates every farmer-base character, whatever it wears.
##
## Only south, north and east are authored. A west animation is its east
## counterpart mirrored (FarmerDoll derives it), which is how the guide
## draws left-facing frames.
##
## Frame fields: cell, ms (0 = hold forever), flip (mirror this frame),
## prop (a tool drawn with the frame: pose = 32x32 cell index in the prop
## sheet, x/y = the pose cell's top-left in body-cell pixels, flip / flip_v =
## mirror the pose sideways / upside down, behind = draw under the body).
## The sheet draws each pose once, so a pose is flipped wherever the guide
## shows the blade facing the other way — the blade leads the swing.
##
## A prop's cell_size is its sheet's cell (default PROP_CELL_SIZE). The pack's
## two overlay sheets ("farmer animations 64x64" / "32x32", one animation per
## row, column 0 a label) have a fixed layout the guide marks in green: a 64x64
## overlay sits on the body cell at 0,0 and a 32x32 one at 16,16 — measured
## against the guide, every pixel lands.
##
## Work animations (LLM-719): scythe = the guide's backhand strike (overlay
## animations #01-03), pull, pick = the pick-up crouch and back up, drink =
## drink standing (mug #11 south, #12 east; north reuses south, as the guide
## draws no back view), sit = sit on floor. kneel = the pick-up crouch held:
## drinking from a well's bucket on the rim. seat = sit in a chair (LLM-750,
## the interior window), held. doze = the guide's SLEEP standing, eyes shut
## (LLM-782): only its head shows, on a bed's pillow under the cover.
##
## smith (LLM-764, the smithy window) = the guide's SMITH, east only (the guide
## draws no other facing): hammer and hot iron from overlay animation #09.
## The sheet does not draw #09's frames in one place — measured against the
## guide, frames 1-2 sit at 13,10 and frames 3-5 at 25,10 — so the iron
## stays put on the anvil.
##
## milk (LLM-797) = the guide's MILK COW, east only: kneeling, hands at the
## udder (cells 110-111, 180 ms each). The guide draws the cow into the frame;
## here the cow is her own actor beside the milker.
##
## The dictionaries below are kept JSON-compatible (double quotes, no
## trailing commas) so offline tools can read this file as data.

const CELL_SIZE := 64
const SHEET_COLUMNS := 16
const PROP_CELL_SIZE := 32

# BEGIN-JSON PROPS
const PROPS := {
    "axe": {"sheet": "/tilesets/mana-seed/farmer/effects/farmer_tool_001_v00.png", "ramps": {"tool_metal": 0, "tool_wood": 0, "tool_accent": 0}},
    "scythe": {"sheet": "/tilesets/mana-seed/farmer/effects/farmer_animations_64x64_v00.png", "cell_size": 64, "ramps": {"tool_metal": 0}},
    "mug": {"sheet": "/tilesets/mana-seed/farmer/effects/farmer_animations_32x32_v00.png", "ramps": {}},
    "hammer": {"sheet": "/tilesets/mana-seed/farmer/effects/farmer_animations_32x32_v00.png", "ramps": {}}
}
# END-JSON

# BEGIN-JSON ANIMATIONS
const ANIMATIONS := {
    "south_idle": {"frames": [{"cell": 0, "ms": 0}]},
    "north_idle": {"frames": [{"cell": 16, "ms": 0}]},
    "east_idle": {"frames": [{"cell": 32, "ms": 0}]},
    "south_walk": {"frames": [
        {"cell": 48, "ms": 135}, {"cell": 49, "ms": 135}, {"cell": 50, "ms": 135},
        {"cell": 48, "ms": 135, "flip": true}, {"cell": 49, "ms": 135, "flip": true}, {"cell": 50, "ms": 135, "flip": true}
    ]},
    "north_walk": {"frames": [
        {"cell": 52, "ms": 135}, {"cell": 53, "ms": 135}, {"cell": 54, "ms": 135},
        {"cell": 52, "ms": 135, "flip": true}, {"cell": 53, "ms": 135, "flip": true}, {"cell": 54, "ms": 135, "flip": true}
    ]},
    "east_walk": {"frames": [
        {"cell": 64, "ms": 135}, {"cell": 65, "ms": 135}, {"cell": 66, "ms": 135},
        {"cell": 67, "ms": 135}, {"cell": 68, "ms": 135}, {"cell": 69, "ms": 135}
    ]},
    "south_chop": {"prop": "axe", "frames": [
        {"cell": 131, "ms": 180, "prop": {"pose": 3, "x": 27, "y": -2}},
        {"cell": 132, "ms": 80, "prop": {"pose": 6, "x": 38, "y": 15}},
        {"cell": 133, "ms": 80, "prop": {"pose": 3, "x": 16, "y": 38, "flip": true, "flip_v": true}},
        {"cell": 133, "ms": 300, "prop": {"pose": 3, "x": 16, "y": 38, "flip": true, "flip_v": true}}
    ]},
    "north_chop": {"prop": "axe", "frames": [
        {"cell": 147, "ms": 180, "prop": {"pose": 0, "x": -2, "y": 29}},
        {"cell": 148, "ms": 80, "prop": {"pose": 1, "x": -1, "y": 6, "flip": true}},
        {"cell": 149, "ms": 80, "prop": {"pose": 3, "x": 19, "y": -14, "behind": true}},
        {"cell": 149, "ms": 300, "prop": {"pose": 3, "x": 19, "y": -14, "behind": true}}
    ]},
    "east_chop": {"prop": "axe", "frames": [
        {"cell": 163, "ms": 180, "prop": {"pose": 1, "x": 7, "y": 5, "flip": true}},
        {"cell": 164, "ms": 80, "prop": {"pose": 4, "x": 5, "y": 36}},
        {"cell": 165, "ms": 80, "prop": {"pose": 6, "x": 48, "y": 11, "flip_v": true}},
        {"cell": 165, "ms": 300, "prop": {"pose": 6, "x": 48, "y": 11, "flip_v": true}}
    ]},
    "south_scythe": {"prop": "scythe", "frames": [
        {"cell": 132, "ms": 180, "prop": {"pose": 1, "x": 0, "y": 0}},
        {"cell": 133, "ms": 80, "prop": {"pose": 2, "x": 0, "y": 0}},
        {"cell": 12, "ms": 80, "prop": {"pose": 3, "x": 0, "y": 0}},
        {"cell": 12, "ms": 300, "prop": {"pose": 4, "x": 0, "y": 0}}
    ]},
    "north_scythe": {"prop": "scythe", "frames": [
        {"cell": 148, "ms": 180, "prop": {"pose": 9, "x": 0, "y": 0}},
        {"cell": 149, "ms": 80, "prop": {"pose": 10, "x": 0, "y": 0}},
        {"cell": 28, "ms": 80, "prop": {"pose": 11, "x": 0, "y": 0}},
        {"cell": 28, "ms": 300, "prop": {"pose": 12, "x": 0, "y": 0}}
    ]},
    "east_scythe": {"prop": "scythe", "frames": [
        {"cell": 164, "ms": 180, "prop": {"pose": 17, "x": 0, "y": 0}},
        {"cell": 165, "ms": 80, "prop": {"pose": 18, "x": 0, "y": 0}},
        {"cell": 44, "ms": 80, "prop": {"pose": 19, "x": 0, "y": 0}},
        {"cell": 44, "ms": 300, "prop": {"pose": 20, "x": 0, "y": 0}}
    ]},
    "south_pull": {"frames": [{"cell": 10, "ms": 400}, {"cell": 11, "ms": 400}]},
    "north_pull": {"frames": [{"cell": 26, "ms": 400}, {"cell": 27, "ms": 400}]},
    "east_pull": {"frames": [{"cell": 42, "ms": 400}, {"cell": 43, "ms": 400}]},
    "south_pick": {"frames": [{"cell": 130, "ms": 600}, {"cell": 0, "ms": 400}]},
    "north_pick": {"frames": [{"cell": 146, "ms": 600}, {"cell": 16, "ms": 400}]},
    "east_pick": {"frames": [{"cell": 162, "ms": 600}, {"cell": 32, "ms": 400}]},
    "south_drink": {"prop": "mug", "frames": [
        {"cell": 112, "ms": 1200, "prop": {"pose": 61, "x": 16, "y": 16}},
        {"cell": 113, "ms": 800, "prop": {"pose": 62, "x": 16, "y": 16}}
    ]},
    "north_drink": {"prop": "mug", "frames": [
        {"cell": 112, "ms": 1200, "prop": {"pose": 61, "x": 16, "y": 16}},
        {"cell": 113, "ms": 800, "prop": {"pose": 62, "x": 16, "y": 16}}
    ]},
    "east_drink": {"prop": "mug", "frames": [
        {"cell": 114, "ms": 1200, "prop": {"pose": 67, "x": 16, "y": 16}},
        {"cell": 115, "ms": 800, "prop": {"pose": 68, "x": 16, "y": 16}}
    ]},
    "south_kneel": {"frames": [{"cell": 130, "ms": 0}]},
    "north_kneel": {"frames": [{"cell": 146, "ms": 0}]},
    "east_kneel": {"frames": [{"cell": 162, "ms": 0}]},
    "south_sit": {"frames": [{"cell": 197, "ms": 0}]},
    "north_sit": {"frames": [{"cell": 213, "ms": 0}]},
    "east_sit": {"frames": [{"cell": 229, "ms": 0}]},
    "east_smith": {"prop": "hammer", "frames": [
        {"cell": 123, "ms": 400, "prop": {"pose": 49, "x": 13, "y": 10}},
        {"cell": 124, "ms": 400, "prop": {"pose": 50, "x": 13, "y": 10}},
        {"cell": 125, "ms": 100, "prop": {"pose": 51, "x": 25, "y": 10}},
        {"cell": 125, "ms": 100, "prop": {"pose": 52, "x": 25, "y": 10}},
        {"cell": 125, "ms": 100, "prop": {"pose": 53, "x": 25, "y": 10}}
    ]},
    "east_milk": {"frames": [
        {"cell": 110, "ms": 180},
        {"cell": 111, "ms": 180}
    ]},
    "south_seat": {"frames": [{"cell": 195, "ms": 0}]},
    "north_seat": {"frames": [{"cell": 211, "ms": 0}]},
    "east_seat": {"frames": [{"cell": 227, "ms": 0}]},
    "south_doze": {"frames": [{"cell": 199, "ms": 0}]}
}
# END-JSON
