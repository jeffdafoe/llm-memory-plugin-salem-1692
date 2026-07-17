package handlers

// register_disperse.go — production registration helper for the disperse tool
// (LLM-453). Same opt-in-piecewise pattern as register_move_to.go /
// register_take_break.go — the entrypoint composes the tool surface it wants (see
// cmd/engine/main.go registerTools).

// RegisterDisperse adds the disperse tool to r as a ClassCommit entry,
// AvailabilityAvailable. The commit handler is HandleDisperse; the decoder is
// DecodeDisperseArgs; both live in disperse.go.
//
// terminalOnSuccess is TRUE: taking your leave ends the tick. The parting line
// rides the tool's `say` (folded in per the terminal-verb rule), so — unlike
// take_break — the model does not pair it with a separate speak. Gated to
// wound-down huddles by tool_gating.go (payload.OffersDisperse), in lockstep with
// its perception cue.
//
// Returns an error on registration failure (duplicate name, malformed schema
// bytes) — a startup wiring bug the caller should fail loudly on.
func RegisterDisperse(r *Registry) error {
	return r.RegisterCommit(
		"disperse",
		disperseSchema,
		DecodeDisperseArgs,
		HandleDisperse,
		true, // terminal: taking your leave ends the tick (the farewell rides `say`)
		WithDescription(disperseDescription),
	)
}
