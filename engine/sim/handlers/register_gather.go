package handlers

// register_gather.go — production registration helper for the gather tool
// (ZBBS-WORK-328). Same opt-in-piecewise pattern as register_consume.go.

// RegisterGather adds the gather tool to r as a ClassCommit entry,
// AvailabilityAvailable. The schema advertises only an optional qty (the
// source determines the item). The commit handler is HandleGather; the
// decoder is DecodeGatherArgs; both live in gather.go.
//
// terminalOnSuccess is TRUE (LLM-175): a started pick ends the tick. Gather is a
// timed harvest (LLM-54) that occupies the actor until the window completes and
// picks the source CLEAN in one call (LLM-87) — so there is nothing useful to do
// after it this tick: a second gather hits "already gathering", and a move
// abandons the in-flight pick. Ending the tick here mirrors move_to and matches
// the post-gather steer's own "wait / done" intent (the engine now does the
// done). This kills at the source the within-tick re-fire loop a weak model fell
// into (gather x6 to the round budget); the harvest yield still lands next tick
// via the completion sweep, and the eat-loop stays move_to -> gather -> consume
// across ticks. (The old rationale — "gather water then walk to the tavern" —
// predates the timed harvest: walking now abandons the pick.)
//
// Advertising is gated at the prompt layer by gateTools (the tool is offered
// only when the actor is loitering at a gatherable source) — but it stays
// AvailabilityAvailable in the registry so a call that does arrive is still
// dispatchable; the sim.Gather Command is the authoritative gate.
func RegisterGather(r *Registry) error {
	return r.RegisterCommit(
		"gather",
		gatherSchema,
		DecodeGatherArgs,
		HandleGather,
		true, // terminal: a started pick ends the tick (LLM-175)
		WithDescription(gatherDescription),
	)
}
