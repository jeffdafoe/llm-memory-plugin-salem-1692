package handlers

// register_gather.go — production registration helper for the gather tool
// (ZBBS-WORK-328). Same opt-in-piecewise pattern as register_consume.go.

// RegisterGather adds the gather tool to r as a ClassCommit entry,
// AvailabilityAvailable. The schema advertises only an optional qty (the
// source determines the item). The commit handler is HandleGather; the
// decoder is DecodeGatherArgs; both live in gather.go.
//
// terminalOnSuccess is FALSE: gather is a within-tick step, not a tick-ender —
// the model can follow with a speak or a move in the same tick (e.g. gather
// water then walk to the tavern), matching consume/pay/speak's non-terminal
// policy.
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
		false, // non-terminal
		WithDescription(gatherDescription),
	)
}
