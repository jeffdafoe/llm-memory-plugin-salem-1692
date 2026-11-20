shader_type canvas_item;

// The village ground (LLM-967), drawn per world pixel over one rect that
// covers the map. terrain_renderer.gd sets every uniform.
//
// Each map tile's corners are the four map cells around it (a dual grid). The
// terrains are layers, ranked bottom to top. At a pixel, the lowest corner
// rank is the base; each higher rank covers the pixel when its field is above
// zero: the bilinear blend of which corners are that rank or above, plus a
// shared wobble. A rank whose corners equal the next rank's is absent from the
// tile and is skipped, so it never draws a band between the terrains there.
//
// Each terrain's texture is sampled by world position and wraps, so tiles
// show no grid. A road that steps diagonally from cell to cell always joins
// across the corner the two cells share (saddle_pull). Edges get blade tips past grass, a shadow cast down and to the
// right by higher ground, and a darker outermost pixel. Water lies below the
// land: under a shore with land above it on screen, the bank's earth face
// shows (banks, a painted strip) and shades the water at its foot; every
// shore gets a darker margin and a broken line of foam a little way out.

uniform sampler2D ranks : filter_nearest;      // R8: layer rank per map cell
uniform sampler2D ground : filter_nearest;     // the season's atlas
uniform sampler2D edge_noise : filter_nearest; // grey wobble, 0.5 = none
uniform sampler2D sparkles : filter_nearest;
uniform sampler2D banks : filter_nearest;      // bank faces, a band per season
uniform int bank_rows = 7;
uniform int bank_band = 0;
uniform ivec2 map_size = ivec2(200, 180);
uniform ivec2 pad = ivec2(0, 0);
uniform int tile_px = 32;
uniform int texture_px = 512;
uniform int atlas_cols = 3;
uniform float edge_wobble = 0.6;
// Bit r set: rank r is grass (blade tips) / soft (no shadow or rim against
// another soft rank) / water / a road (joins on a diagonal) / sparkles.
uniform int grass_ranks = 0;
uniform int soft_ranks = 0;
uniform int water_ranks = 0;
uniform int road_ranks = 0;
uniform int sparkle_ranks = 0;
uniform int sparkle_cols = 4;
uniform int sparkle_frames = 12;
uniform float sparkle_seconds = 0.6;

// World pixels between a pixel and the one that casts its shadow, or the
// land that edges it.
const int STEP = 2;
const float SHADOW = 0.72;
const float RIM = 0.85;
const float TIP = 0.82;
const float FRINGE_REACH = 0.09;
const float FRINGE_CHANCE = 0.45;
// Shallow and deep water fade into each other over this much of a tile either
// side of their boundary. Under 0.5, so the fade is done before a tile edge
// whose corners are one depth, and tiles join.
const float WATER_FADE = 0.45;
const float NO_WATER_BLEND = 9.0;
// The bank face is BANK_MIN to bank_rows world pixels tall, varying along the
// shore. Below its foot the water is in its shadow for BANK_SHADE pixels, then
// foam; beside or above land, the first pixel of water is shaded and the
// foam is FOAM_AT out. SCAN_UP must reach past the tallest face, its shadow
// and its foam.
const int BANK_MIN = 4;
const int BANK_SHADE = 2;
const float BANK_SHADOW = 0.74;
const float SHORE_SHADE = 0.86;
const int FOAM_AT = 2;
const float FOAM_CHANCE = 0.65;
const int SCAN_UP = 12;
// How far saddle_pull moves the field at a saddle tile's centre. More than
// the largest wobble (edge_wobble 0.6 x the noise's reach from 0.5), so the
// road's diagonal holds at every pixel along it.
const float SADDLE_PULL = 0.5;

varying vec2 world;

void vertex() {
    // The node sits at the world origin, so its local space is world space.
    world = VERTEX;
}

bool has_bit(int bits, int r) {
    return r >= 0 && ((bits >> r) & 1) == 1;
}

// GLSL leaves % of a negative number undefined; world pixels left of or
// above the origin are negative.
int wrap(int v, int period) {
    return v - period * int(floor(float(v) / float(period)));
}

int rank_at(ivec2 cell) {
    cell = clamp(cell, ivec2(0), map_size - ivec2(1));
    return int(texelFetch(ranks, cell, 0).r * 255.0 + 0.5);
}

int corner_mask(int r, int tl, int tr, int br, int bl) {
    return (tl >= r ? 1 : 0) | (tr >= r ? 2 : 0) | (br >= r ? 4 : 0) | (bl >= r ? 8 : 0);
}

float corner_blend(int mask, vec2 uv) {
    float tl = float(mask & 1);
    float tr = float((mask >> 1) & 1);
    float br = float((mask >> 2) & 1);
    float bl = float((mask >> 3) & 1);
    return tl * (1.0 - uv.x) * (1.0 - uv.y) + tr * uv.x * (1.0 - uv.y) + br * uv.x * uv.y + bl * (1.0 - uv.x) * uv.y;
}

// A saddle tile has its corners in mask on one diagonal and the other two on
// the other. The blend there is 0.5 at the centre, so the wobble alone would
// decide whether the diagonal joins, and a road stepping diagonally from cell
// to cell would break into islands. own_road: both corners in mask are roads;
// other_road: both of the other two are. A road diagonal joins: the field is
// pulled up at the centre when own_road, else down when other_road. The pull
// is zero on the tile's edges, so the field still meets the next tile's.
float saddle_pull(int mask, bool own_road, bool other_road, vec2 uv) {
    if ((mask != 5 && mask != 10) || !(own_road || other_road)) {
        return 0.0;
    }
    float bump = 16.0 * uv.x * (1.0 - uv.x) * uv.y * (1.0 - uv.y);
    return (own_road ? SADDLE_PULL : -SADDLE_PULL) * bump;
}

uint hash(ivec2 p) {
    uint h = uint(p.x) * 374761393u + uint(p.y) * 668265263u;
    h = (h ^ (h >> 13u)) * 1274126177u;
    return h ^ (h >> 16u);
}

float hash01(ivec2 p) {
    return float(hash(p) & 65535u) / 65535.0;
}

// The ground at world pixel p:
//   x: the rank drawn
//   y: the rank under it, or -1; -2 when the whole tile is one terrain
//   z: how far inside its edge the drawn rank is (its field)
//   w: a grass rank whose blade tip reaches this pixel, or -1
// with_tips false skips the blade tips: the shadow and shore queries need
// only the rank drawn.
// water: when the tile's base is a water rank and the rank above it is water
// too, x is the field of the corners at that upper rank or higher (land
// counts), with no wobble (-0.5 .. 0.5); y is the base rank, z the upper one.
// It is computed whether or not the upper rank is at any corner, so two tiles
// sharing an edge agree on the fade along it (code_review). x is
// NO_WATER_BLEND otherwise.
vec4 ground_at(ivec2 p, bool with_tips, out vec3 water) {
    ivec2 t = ivec2(floor(vec2(p) / float(tile_px)));
    ivec2 tile = t + pad;
    int tl = rank_at(tile + ivec2(-1, -1));
    int tr = rank_at(tile + ivec2(0, -1));
    int br = rank_at(tile);
    int bl = rank_at(tile + ivec2(-1, 0));
    int lo = min(min(tl, tr), min(br, bl));
    int hi = max(max(tl, tr), max(br, bl));
    water = vec3(NO_WATER_BLEND, 0.0, 0.0);
    if (lo == hi) {
        return vec4(float(lo), -2.0, 1.0, -1.0);
    }
    vec2 uv = (vec2(p - t * tile_px) + 0.5) / float(tile_px);
    ivec2 np = ivec2(wrap(p.x, texture_px), wrap(p.y, texture_px));
    float n = (texelFetch(edge_noise, np, 0).r - 0.5) * edge_wobble;
    if (has_bit(water_ranks, lo) && has_bit(water_ranks, lo + 1)) {
        water = vec3(corner_blend(corner_mask(lo + 1, tl, tr, br, bl), uv) - 0.5, float(lo), float(lo + 1));
    }
    // Which diagonal pair is all road: TL and BR, and TR and BL.
    bool road_tlbr = has_bit(road_ranks, tl) && has_bit(road_ranks, br);
    bool road_trbl = has_bit(road_ranks, tr) && has_bit(road_ranks, bl);
    int top = lo;
    int under = -1;
    float field = 1.0;
    int fringe = -1;
    for (int r = lo + 1; r <= hi; r++) {
        int mask = corner_mask(r, tl, tr, br, bl);
        int above = r < hi ? corner_mask(r + 1, tl, tr, br, bl) : 0;
        if (mask == above) {
            continue;
        }
        float pull = mask == 5 ? saddle_pull(mask, road_tlbr, road_trbl, uv) : saddle_pull(mask, road_trbl, road_tlbr, uv);
        float g = corner_blend(mask, uv) + pull + n - 0.5;
        if (g > 0.0) {
            under = top;
            top = r;
            field = g;
        } else if (with_tips && has_bit(grass_ranks, r) && g > -FRINGE_REACH && hash01(p) < FRINGE_CHANCE) {
            fringe = r;
        }
    }
    return vec4(float(top), float(under), field, float(fringe));
}

// Which of the 4x4 map cells around tile t are land: bit (dy + 2) * 4 +
// (dx + 2) for the cell at offset (dx, dy), -2..1, from t's BR corner. Those
// are the corners of t and the eight tiles around it, every tile the bank and
// shore scans reach. Read once per pixel, so each probe of the scans costs one
// noise read, not a full ground_at. Bit 16 + i is set when cell i is a road
// (for saddle_pull).
int land_cells(ivec2 t) {
    ivec2 tile = t + pad;
    int bits = 0;
    for (int dy = -2; dy <= 1; dy++) {
        for (int dx = -2; dx <= 1; dx++) {
            int r = rank_at(tile + ivec2(dx, dy));
            int i = (dy + 2) * 4 + dx + 2;
            if (!has_bit(water_ranks, r)) {
                bits |= 1 << i;
            }
            if (has_bit(road_ranks, r)) {
                bits |= 1 << (16 + i);
            }
        }
    }
    return bits;
}

// Whether land is drawn at world pixel q, which lies in tile t or one of the
// eight around it; cells is land_cells(t). Land ranks all lie above water, and
// a higher rank's corners are a subset of a lower one's, so land is drawn
// exactly where the field of all the land corners is above zero: ground_at's
// rule, without walking the ranks. A land saddle's other two corners are
// water, never road, so its pull hangs on its own two corners alone.
bool land_at(ivec2 q, ivec2 t, int cells) {
    ivec2 qt = ivec2(floor(vec2(q) / float(tile_px)));
    // The TL corner of tile qt, in cells' 4x4 grid.
    int i = (qt.y - t.y + 1) * 4 + (qt.x - t.x + 1);
    int mask = ((cells >> i) & 1) | (((cells >> (i + 1)) & 1) << 1) | (((cells >> (i + 5)) & 1) << 2) | (((cells >> (i + 4)) & 1) << 3);
    if (mask == 0 || mask == 15) {
        return mask == 15;
    }
    vec2 uv = (vec2(q - qt * tile_px) + 0.5) / float(tile_px);
    ivec2 np = ivec2(wrap(q.x, texture_px), wrap(q.y, texture_px));
    float n = (texelFetch(edge_noise, np, 0).r - 0.5) * edge_wobble;
    int roads = cells >> 16;
    bool own_road = mask == 5 ? ((roads >> i) & (roads >> (i + 5)) & 1) == 1 : ((roads >> (i + 1)) & (roads >> (i + 4)) & 1) == 1;
    return corner_blend(mask, uv) + saddle_pull(mask, own_road, false, uv) + n - 0.5 > 0.0;
}

// The bank face's height under column x: the edge noise read along one row,
// so it rises and falls smoothly along the shore.
int bank_height(int x) {
    float n = texelFetch(edge_noise, ivec2(wrap(x, texture_px), 200), 0).r;
    int span = bank_rows - BANK_MIN + 1;
    return clamp(BANK_MIN + int(floor((n - 0.3) / 0.45 * float(span))), BANK_MIN, bank_rows);
}

vec3 texel(int r, ivec2 tp) {
    ivec2 origin = ivec2((r % atlas_cols) * texture_px, (r / atlas_cols) * texture_px);
    return texelFetch(ground, origin + tp, 0).rgb;
}

void fragment() {
    ivec2 p = ivec2(floor(world));
    vec3 water;
    vec4 here = ground_at(p, true, water);
    int top = int(here.x);
    int under = int(here.y);
    int fringe = int(here.w);
    ivec2 tp = ivec2(wrap(p.x, texture_px), wrap(p.y, texture_px));

    vec3 col = texel(top, tp);
    if (fringe > top) {
        // A blade tip past the grass edge; shaded, unless the ground under it
        // is soft too (it would draw a line through snow).
        col = texel(fringe, tp) * (has_bit(soft_ranks, top) ? 1.0 : TIP);
    } else if (has_bit(water_ranks, top) && water.x != NO_WATER_BLEND) {
        // Deep shades into shallow: no line between them. The fade follows the
        // corners only (no wobble), so it is continuous from tile to tile.
        float t = smoothstep(-WATER_FADE, WATER_FADE, water.x);
        col = mix(texel(int(water.y), tp), texel(int(water.z), tp), t);
    }
    float k = 1.0;
    bool bank = false;

    // Inside a one-terrain tile, away from its edge, every pixel STEP away is
    // the same terrain: no shadow and no shore line to look for. Water looks
    // further, up to SCAN_UP above for a bank and FOAM_AT to the sides.
    ivec2 t = ivec2(floor(vec2(p) / float(tile_px)));
    ivec2 local = p - t * tile_px;
    bool water_here = has_bit(water_ranks, top);
    ivec2 reach_lo = water_here ? ivec2(FOAM_AT, SCAN_UP) : ivec2(STEP);
    ivec2 reach_hi = water_here ? ivec2(FOAM_AT) : ivec2(STEP);
    bool interior = under == -2 && all(greaterThanEqual(local, reach_lo)) && all(lessThan(local, ivec2(tile_px) - reach_hi));
    // Water with no land in reach skips the scans, as in a wide lake whose
    // shallows blend into the deep (those tiles are never one terrain).
    int cells = !interior && water_here ? land_cells(t) : 0;
    if (cells != 0) {
        // Land above this water on screen: the bank's face, then its shadow.
        int h = bank_height(p.x);
        int up = 0;
        for (int d = 1; d <= SCAN_UP; d++) {
            if (land_at(p - ivec2(0, d), t, cells)) {
                up = d;
                break;
            }
        }
        if (up > 0 && up <= h) {
            // The face ends on the wet line, however tall it is.
            int row = up < h ? up - 1 : bank_rows - 1;
            col = texelFetch(banks, ivec2(wrap(p.x, texture_px), bank_band * bank_rows + row), 0).rgb;
            bank = true;
        } else {
            int below_bank = up > 0 ? up - h : SCAN_UP;
            int side = FOAM_AT + 1;
            for (int d = 1; d <= FOAM_AT; d++) {
                if (land_at(p + ivec2(-d, 0), t, cells) || land_at(p + ivec2(d, 0), t, cells) || land_at(p + ivec2(0, d), t, cells)) {
                    side = d;
                    break;
                }
            }
            if (below_bank <= BANK_SHADE) {
                k *= BANK_SHADOW;
            } else if (side == 1) {
                k *= SHORE_SHADE;
            }
            // Foam: broken, in short runs, where the water first laps clear
            // of the shore; never on the shaded pixel beside land, where a
            // bank's foam line meets a side shore at an inside corner.
            bool foam_line = side != 1 && (below_bank == BANK_SHADE + 1 || (side == FOAM_AT && below_bank > BANK_SHADE + 1));
            if (foam_line && hash01(p / 3) < FOAM_CHANCE) {
                col = col * 0.6 + vec3(0.9) * 0.4;
            }
        }
    } else if (!interior && !water_here) {
        // Higher ground up and to the left casts a shadow, except soft on soft.
        vec3 unused;
        int caster = int(ground_at(p - ivec2(STEP), false, unused).x);
        bool both_soft = has_bit(soft_ranks, caster) && has_bit(soft_ranks, top);
        if (caster > top && !both_soft) {
            k *= SHADOW;
        }
        // The outermost pixel of raised ground is a shade darker, except soft
        // on soft and land on water: the land's edge there is the top of the
        // bank, not a step down.
        if (here.z < 0.05 && top > 0 && !(has_bit(soft_ranks, top) && has_bit(soft_ranks, under)) && !has_bit(water_ranks, under)) {
            k *= RIM;
        }
    }

    if (has_bit(sparkle_ranks, top) && !bank) {
        int start = int(hash(t + ivec2(7, 13)) % uint(sparkle_frames));
        int frame = (int(TIME / sparkle_seconds) + start) % sparkle_frames;
        ivec2 cell = ivec2((frame % sparkle_cols) * tile_px, (frame / sparkle_cols) * tile_px);
        vec4 glint = texelFetch(sparkles, cell + local, 0);
        col = mix(col, glint.rgb, glint.a);
    }

    COLOR = vec4(col * k, 1.0);
}
