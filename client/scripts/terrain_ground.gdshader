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
// show no grid. Edges get blade tips past grass, a shadow cast down and to the
// right by higher ground, a darker outermost pixel, and a pale line where
// water meets land.

uniform sampler2D ranks : filter_nearest;      // R8: layer rank per map cell
uniform sampler2D ground : filter_nearest;     // the season's atlas
uniform sampler2D edge_noise : filter_nearest; // grey wobble, 0.5 = none
uniform sampler2D sparkles : filter_nearest;
uniform ivec2 map_size = ivec2(200, 180);
uniform ivec2 pad = ivec2(0, 0);
uniform int tile_px = 32;
uniform int texture_px = 512;
uniform int atlas_cols = 3;
uniform float edge_wobble = 0.6;
// Bit r set: rank r is grass (blade tips) / soft (no shadow or rim against
// another soft rank) / water / sparkles.
uniform int grass_ranks = 0;
uniform int soft_ranks = 0;
uniform int water_ranks = 0;
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
// water: where a water rank lies over the base water rank in this tile, x is
// that upper rank's field with no wobble (-0.5 .. 0.5), y the base rank, z the
// upper rank; x is NO_WATER_BLEND otherwise.
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
        float blend = corner_blend(mask, uv);
        if (water.x == NO_WATER_BLEND && has_bit(water_ranks, lo) && has_bit(water_ranks, r)) {
            water = vec3(blend - 0.5, float(lo), float(r));
        }
        float g = blend + n - 0.5;
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

    // Inside a one-terrain tile, away from its edge, every pixel STEP away is
    // the same terrain: no shadow and no shore line to look for.
    ivec2 local = p - ivec2(floor(vec2(p) / float(tile_px))) * tile_px;
    bool interior = under == -2 && all(greaterThanEqual(local, ivec2(STEP))) && all(lessThan(local, ivec2(tile_px - STEP)));
    if (!interior) {
        // Higher ground up and to the left casts a shadow, except soft on soft
        // and water on water (shallows over the deep are not raised).
        vec3 unused;
        int caster = int(ground_at(p - ivec2(STEP), false, unused).x);
        bool both_soft = has_bit(soft_ranks, caster) && has_bit(soft_ranks, top);
        bool both_water = has_bit(water_ranks, caster) && has_bit(water_ranks, top);
        if (caster > top && !both_soft && !both_water) {
            k *= SHADOW;
        }
        if (has_bit(water_ranks, top)) {
            // Water meeting land: a pale line on the water.
            ivec2 sides[4] = ivec2[](ivec2(0, -STEP), ivec2(-STEP, 0), ivec2(STEP, 0), ivec2(0, STEP));
            for (int i = 0; i < 4; i++) {
                if (!has_bit(water_ranks, int(ground_at(p + sides[i], false, unused).x))) {
                    col = col * 0.55 + vec3(0.9) * 0.45;
                    break;
                }
            }
        } else if (here.z < 0.05 && top > 0 && !(has_bit(soft_ranks, top) && has_bit(soft_ranks, under))) {
            // The outermost pixel of raised ground is a shade darker.
            k *= RIM;
        }
    }

    if (has_bit(sparkle_ranks, top)) {
        ivec2 t = ivec2(floor(vec2(p) / float(tile_px)));
        int start = int(hash(t + ivec2(7, 13)) % uint(sparkle_frames));
        int frame = (int(TIME / sparkle_seconds) + start) % sparkle_frames;
        ivec2 cell = ivec2((frame % sparkle_cols) * tile_px, (frame / sparkle_cols) * tile_px);
        vec4 glint = texelFetch(sparkles, cell + local, 0);
        col = mix(col, glint.rgb, glint.a);
    }

    COLOR = vec4(col * k, 1.0);
}
