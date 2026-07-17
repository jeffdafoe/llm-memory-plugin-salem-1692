package handlers

// register_bake.go — production registration helper for the bake tool (LLM-454).
// Same opt-in-piecewise pattern as register_stoke_repair.go / register_take_break.go
// — the entrypoint composes the tool surface it wants (see cmd/engine/main.go
// registerTools).

// RegisterBake adds the bake tool to r as a ClassCommit entry,
// AvailabilityAvailable. The commit handler is HandleBake; the decoder is
// DecodeBakeArgs; both live in bake.go.
//
// terminalOnSuccess is TRUE: baking occupies the actor for the whole evening (the
// SourceActivity window shelves its tick until bedtime), so the tick ends here — the
// same terminal-source-activity policy as stoke/gather. Gated to the evening-at-home
// state by tool_gating.go (payload.BakeChoice), in lockstep with its perception cue.
func RegisterBake(r *Registry) error {
	return r.RegisterCommit(
		"bake",
		bakeSchema,
		DecodeBakeArgs,
		HandleBake,
		true, // terminal: baking fills the evening — the tick ends here
		WithDescription(bakeDescription),
	)
}
