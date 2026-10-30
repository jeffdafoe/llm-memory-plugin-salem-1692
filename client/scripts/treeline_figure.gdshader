shader_type canvas_item;

// The figure at the treeline (LLM-844): a colourless grey person who is never
// quite solid.
//
// Colour: the body sheet's own shading turned to grey (tint + luminance *
// lift), so the outline of head, arms and legs still reads as a person. A
// flat fill read as a dark blob, and near black sank into the dusk grass.
// The luminance is of the sheet as drawn, in the palette-swap placeholder
// colours (the swap material is replaced): a change to those placeholders
// changes the figure's shading.
// Sway: the whole body bends sideways by up to sway_px on a long, slow wave.
// A short wave that moved each row on its own broke the outline apart.
// Breathing: the opacity drifts between alpha_low and alpha_high on two sines
// with no common beat.
//
// UV is in the atlas, so rows count from the sheet's top; only the differences
// matter. With nearest filtering the shift steps by whole art pixels.

uniform vec4 tint : source_color = vec4(0.20, 0.20, 0.23, 1.0);
uniform float lift = 0.40;
uniform float sway_px = 1.0;
uniform float alpha_low = 0.8;
uniform float alpha_high = 0.95;
// Per showing, so two showings never move alike.
uniform float phase = 0.0;

// The node's modulate, taken in vertex(): in fragment() the incoming COLOR is
// already multiplied by the texture at the unshifted UV, which would clip the
// sway to the still outline.
varying vec4 figure_modulate;

void vertex() {
    figure_modulate = COLOR;
}

void fragment() {
    float t = TIME + phase;
    float row = UV.y / TEXTURE_PIXEL_SIZE.y;
    float shift = sin(row * 0.12 - t * 1.3) * sway_px;
    vec4 source = texture(TEXTURE, UV + vec2(shift * TEXTURE_PIXEL_SIZE.x, 0.0));
    float luminance = dot(source.rgb, vec3(0.299, 0.587, 0.114));
    float breath = 0.5 + 0.25 * sin(t * 0.9) + 0.25 * sin(t * 1.7 + 2.0);
    // The fade-out rides on the modulate.
    COLOR = vec4(tint.rgb + luminance * lift, source.a * mix(alpha_low, alpha_high, breath)) * figure_modulate;
}
