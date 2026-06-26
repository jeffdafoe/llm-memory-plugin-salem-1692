package handlers

// register_repair.go — production registration helper for the repair tool
// (LLM-118). Same opt-in-piecewise pattern as register_gather.go.

// RegisterRepair adds the repair tool to r as a ClassCommit entry,
// AvailabilityAvailable. It takes no arguments. The commit handler is
// HandleRepair; the decoder is DecodeRepairArgs; both live in repair.go.
//
// terminalOnSuccess is FALSE: starting a repair is a within-tick step — the
// owner can speak a word ("let me see to this stall") in the same tick —
// matching gather's non-terminal policy.
//
// Advertising is gated at the prompt layer by gateTools (offered only when the
// actor owns a co-located stall worn to the repair threshold) — but it stays
// AvailabilityAvailable in the registry so a call that does arrive is still
// dispatchable; the sim.StartRepair Command is the authoritative gate.
func RegisterRepair(r *Registry) error {
	return r.RegisterCommit(
		"repair",
		repairSchema,
		DecodeRepairArgs,
		HandleRepair,
		false, // non-terminal
		WithDescription(repairDescription),
	)
}
