package handlers

// register_craft.go — production registration helper for the produce tool
// (LLM-116, one-shot semantics since LLM-319). Same opt-in-piecewise pattern
// as register_gather.go.

// RegisterCraft adds the produce tool to r as a ClassCommit entry,
// AvailabilityAvailable. The schema advertises a required item (the good to
// make one batch of). The commit handler is HandleCraft; the decoder is
// DecodeCraftArgs; both live in craft.go.
//
// terminalOnSuccess is FALSE: starting a batch is a within-tick decision, not
// a tick-ender — the producer can act again in the same tick (a measured 83
// non-speech follow-on actions a day: sell, pay, consume, move). A second
// produce in the same tick is rejected by the harness genericCallKey guard, and
// a mid-cycle produce bounces in the substrate (StartProductionCycle).
//
// LLM-468 makes that CONDITIONAL at dispatch: a produce whose optional `say`
// reached the room ends the tick anyway (producedWithSpeech), because an
// utterance is terminal wherever it happens. The registry flag stays false —
// silence keeps the tick open — and the flip lives in the dispatch branch where
// the result can be inspected.
//
// Advertising is gated at the prompt layer by gateTools (offered exactly when
// the "## Your trade" cue renders: at the workplace, nothing in the works) —
// but it stays AvailabilityAvailable in the registry so a call that does
// arrive is still dispatchable; the sim.StartProductionCycle Command is the
// authoritative gate.
func RegisterCraft(r *Registry) error {
	return r.RegisterCommit(
		craftToolName,
		craftSchema,
		DecodeCraftArgs,
		HandleCraft,
		false, // non-terminal
		WithDescription(craftDescription),
	)
}
