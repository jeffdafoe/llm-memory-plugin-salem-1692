package handlers

// register_repair.go — production registration helper for the repair tool
// (LLM-118). Same opt-in-piecewise pattern as register_gather.go.

// RegisterRepair adds the repair tool to r as a ClassCommit entry,
// AvailabilityAvailable. It takes no arguments. The commit handler is
// HandleRepair; the decoder is DecodeRepairArgs; both live in repair.go.
//
// terminalOnSuccess is TRUE, matching gather (LLM-175): a started repair opens a
// timed window, so a second repair this tick bounces "already busy" and a move
// abandons the window — nothing useful chains after starting it. Ending the tick kills
// the within-tick re-fire storm (LLM-443); the stall mend still lands next tick via the
// source-activity completion sweep. A word said over the shoulder rides the narration
// on the repair call — speak is terminal too (LLM-321). (The earlier "matching gather's
// non-terminal policy" rationale went stale when LLM-175 flipped gather to terminal.)
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
		true, // terminal: a started repair ends the tick (LLM-443)
		WithDescription(repairDescription),
	)
}
