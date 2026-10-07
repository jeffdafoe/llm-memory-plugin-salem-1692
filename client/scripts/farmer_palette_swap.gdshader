shader_type canvas_item;

// Palette swap for Mana Seed farmer-base sheets (LLM-691). Each layer sheet is
// drawn in placeholder ramps; FarmerDoll fills swap_from with the placeholder
// colours the layer uses and swap_to with the chosen ramp, so one shared sheet
// renders in any colourway. Pixels matching no entry (outline, eye white) pass
// through unchanged.

const int MAX_SWAPS = 12;

uniform int swap_count = 0;
uniform vec3 swap_from[MAX_SWAPS];
uniform vec3 swap_to[MAX_SWAPS];

// COLOR in vertex() carries modulate * self_modulate. Overwriting COLOR in
// fragment() would drop it (the sleep dimming tints villagers through
// modulate), so it is carried across and re-applied.
varying vec4 modulate_color;

void vertex() {
    modulate_color = COLOR;
}

void fragment() {
    vec4 texel = texture(TEXTURE, UV);
    vec3 rgb = texel.rgb;
    for (int i = 0; i < MAX_SWAPS; i++) {
        if (i >= swap_count) {
            break;
        }
        if (distance(texel.rgb, swap_from[i]) < 0.01) {
            rgb = swap_to[i];
            break;
        }
    }
    COLOR = vec4(rgb, texel.a) * modulate_color;
}
