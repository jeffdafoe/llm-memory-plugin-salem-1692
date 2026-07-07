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
// a tick-ender — the producer can speak its social beat or act in the same
// tick (matching gather/consume/speak's non-terminal policy). A second produce
// in the same tick is rejected by the harness genericCallKey guard, and a
// mid-cycle produce bounces in the substrate (StartProductionCycle).
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
