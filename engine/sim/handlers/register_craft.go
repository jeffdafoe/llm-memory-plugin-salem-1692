package handlers

// register_craft.go — production registration helper for the craft tool
// (LLM-116). Same opt-in-piecewise pattern as register_gather.go.

// RegisterCraft adds the craft tool to r as a ClassCommit entry,
// AvailabilityAvailable. The schema advertises a required item (the good to
// forge next). The commit handler is HandleCraft; the decoder is DecodeCraftArgs;
// both live in craft.go.
//
// terminalOnSuccess is FALSE: choosing what to forge is a within-tick decision,
// not a tick-ender — the crafter can speak or move in the same tick (matching
// gather/consume/speak's non-terminal policy).
//
// Advertising is gated at the prompt layer by gateTools (offered only to a
// crafter with more than one produce entry, at its workplace) — but it stays
// AvailabilityAvailable in the registry so a call that does arrive is still
// dispatchable; the sim.SetProductionFocus Command is the authoritative gate.
func RegisterCraft(r *Registry) error {
	return r.RegisterCommit(
		"produce",
		craftSchema,
		DecodeCraftArgs,
		HandleCraft,
		false, // non-terminal
		WithDescription(craftDescription),
	)
}
