shader_type canvas_item;

// Snow on the top edges of a placed object (LLM-850): roofs, fences, props,
// pines and crops — everything with no season art of its own. world.gd sets
// one shared material on every such sprite while snow lies, and drops it again
// when the snow is gone, so the village pays nothing for this shader out of
// snow.
//
// A cap of `depth` texel rows is laid under every upward-facing edge of the
// sprite (an opaque texel with clear texels above it). A dark outline texel on
// the very edge is kept, so the cap sits inside the art's own outline the way
// the pack's snowy art draws it. The cap is the pack's snow palette — the
// colours of the snowy ground sheet, the snowy trees and the snow-only roof
// layers — with the bottom row one shade down as a lip.
//
// The test reads the texels above in the sheet the sprite is cut from. At the
// top row of a cell that is the cell above it on the sheet; where that cell is
// opaque there, the row gets no snow. Only a missing line of snow, never a
// stray one.

uniform int depth : hint_range(0, 4) = 0;
uniform vec4 snow_color : source_color = vec4(0.816, 0.847, 0.722, 1.0);   // #d0d8b8
uniform vec4 snow_shade : source_color = vec4(0.722, 0.753, 0.69, 1.0);    // #b8c0b0
// An edge texel darker than this is outline, and stays.
uniform float outline_luma = 0.25;

// The node's own modulate (a fade, a highlight) is kept from vertex() so it
// still applies to a snowy sprite. Night and storm tint the whole canvas after
// this, snow included.
varying vec4 modulate_color;

void vertex() {
    modulate_color = COLOR;
}

vec4 above(sampler2D tex, vec2 uv, int d, vec2 px) {
    return texture(tex, uv - vec2(0.0, px.y * float(d)));
}

// How far up the walk looks for the clear texel, outline included.
const int MAX_WALK = 10;

bool is_outline(vec4 c, float limit) {
    return dot(c.rgb, vec3(0.299, 0.587, 0.114)) < limit;
}

void fragment() {
    vec4 c = texture(TEXTURE, UV);
    // Outline texels are never snow: the snow sits inside the art's own
    // outline, which is what keeps a snowy roof apart from the snowy ground.
    if (depth > 0 && c.a > 0.5 && !is_outline(c, outline_luma)) {
        // Walk up to the clear texel above. The texels passed on the way that
        // are outline do not count, so `row` is this texel's row in the cap
        // under the outline (1 = the top row of snow). Read straight up, the
        // outline of a shallow slope is several texels tall, so the walk goes
        // through any run of outline (up to MAX_WALK) but stops once it has
        // passed more than `depth` texels of art.
        int row = 0;
        int outline_rows = 0;
        for (int d = 1; d <= MAX_WALK; d++) {
            vec4 a = above(TEXTURE, UV, d, TEXTURE_PIXEL_SIZE);
            if (a.a < 0.5) {
                row = d - outline_rows;
                break;
            }
            if (is_outline(a, outline_luma)) {
                outline_rows++;
            } else if (d - outline_rows > depth) {
                break;
            }
        }
        // Art with no dark outline on top keeps its own top texel as the rim:
        // a cap in the ground's snow colour right on the silhouette would read
        // as the object shrinking into the snowy ground, not as snow on it.
        int rim = outline_rows > 0 ? 0 : 1;
        if (row > rim && row <= depth + rim) {
            c.rgb = row == depth + rim && depth > 1 ? snow_shade.rgb : snow_color.rgb;
        }
    }
    COLOR = c * modulate_color;
}
