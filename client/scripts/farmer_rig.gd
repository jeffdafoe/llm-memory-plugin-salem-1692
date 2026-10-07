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
## sheet, x/y = the pose cell's top-left in body-cell pixels, behind = draw
## under the body).
##
## The dictionaries below are kept JSON-compatible (double quotes, no
## trailing commas) so offline tools can read this file as data.

const CELL_SIZE := 64
const SHEET_COLUMNS := 16
const PROP_CELL_SIZE := 32

# BEGIN-JSON PROPS
const PROPS := {
    "axe": {"sheet": "/tilesets/mana-seed/farmer/effects/farmer_tool_001_v00.png", "ramps": {"tool_metal": 0, "tool_wood": 0, "tool_accent": 0}}
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
        {"cell": 133, "ms": 80, "prop": {"pose": 2, "x": 17, "y": 29}},
        {"cell": 133, "ms": 300, "prop": {"pose": 2, "x": 17, "y": 29}}
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
        {"cell": 165, "ms": 80, "prop": {"pose": 6, "x": 48, "y": 11}},
        {"cell": 165, "ms": 300, "prop": {"pose": 6, "x": 48, "y": 11}}
    ]}
}
# END-JSON
