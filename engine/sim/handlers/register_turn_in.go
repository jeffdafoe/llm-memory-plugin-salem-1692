package handlers

// register_turn_in.go — production registration helper for the turn_in tool
// (LLM-447). Same opt-in-piecewise pattern as register_bake.go /
// register_stoke_repair.go — the entrypoint composes the tool surface it wants
// (see cmd/engine/main.go registerTools).

// RegisterTurnIn adds the turn_in tool to r as a ClassCommit entry,
// AvailabilityAvailable. The commit handler is HandleTurnIn; the decoder is
// DecodeTurnInArgs; both live in turn_in.go.
//
// terminalOnSuccess is TRUE, and here it is not a policy choice but a fact: the
// actor is asleep when this returns, so there is no further action it could take
// this tick. Being terminal is also what lets the goodnight ride the tool's say —
// speak is terminal too, so a goodnight-then-bed pair of calls could never both
// land. Gated to the evening bed-down situation by tool_gating.go
// (payload.TurnInChoice), in lockstep with its perception cue.
func RegisterTurnIn(r *Registry) error {
	return r.RegisterCommit(
		"turn_in",
		turnInSchema,
		DecodeTurnInArgs,
		HandleTurnIn,
		true, // terminal: the actor is asleep — the day is over
		WithDescription(turnInDescription),
	)
}
