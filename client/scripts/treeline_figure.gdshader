shader_type canvas_item;

// The figure at the treeline (LLM-844): a dark silhouette that is never quite
// solid. Only the body's shape is read from the sheet; the colour is `tint`.
//
// Waver: each row of the body slides sideways on a slow wave that climbs the
// body, like heat haze. Its strength drifts on two slow sines that rarely line
// up, so at times it is nearly still and at times plainly wrong.
// Breathing: the opacity drifts between alpha_low and alpha_high on two more
// sines with no common beat.
//
// UV is in the atlas, so rows count from the sheet's top; only the differences
// matter. With nearest filtering the shift steps by whole art pixels.

uniform vec4 tint : source_color = vec4(0.06, 0.06, 0.07, 1.0);
uniform float waver_px = 2.0;
uniform float alpha_low = 0.6;
uniform float alpha_high = 0.9;
// Per showing, so two showings never move alike.
uniform float phase = 0.0;

// The node's modulate, taken in vertex(): in fragment() the incoming COLOR is
// already multiplied by the texture at the unshifted UV, which would tint the
// silhouette with the sheet's colours and clip the waver to the still outline.
varying vec4 figure_modulate;

void vertex() {
    figure_modulate = COLOR;
}

void fragment() {
    float t = TIME + phase;
    float strength = 0.5 + 0.5 * sin(t * 0.37) * sin(t * 0.23 + 1.3);
    float row = UV.y / TEXTURE_PIXEL_SIZE.y;
    float shift = sin(row * 0.45 - t * 2.1) * waver_px * strength;
    float shape = texture(TEXTURE, UV + vec2(shift * TEXTURE_PIXEL_SIZE.x, 0.0)).a;
    float breath = 0.5 + 0.25 * sin(t * 0.9) + 0.25 * sin(t * 1.7 + 2.0);
    // The fade-out rides on the modulate.
    COLOR = vec4(tint.rgb, shape * mix(alpha_low, alpha_high, breath)) * figure_modulate;
}
