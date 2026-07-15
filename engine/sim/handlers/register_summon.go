package handlers

// register_summon.go — production registration helper for the summon tool
// (ZBBS-HOME-311). Same opt-in-piecewise pattern as register_move_to.go —
// the entrypoint composes the tool surface it wants (see cmd/engine/main.go
// registerTools).

// RegisterSummon adds the summon tool to r as a ClassCommit entry,
// AvailabilityAvailable. The commit handler is HandleSummon; the decoder is
// DecodeSummonArgs; both live in summon.go.
//
// terminalOnSuccess is TRUE: summon ends the tick. Sending for someone is a
// "decide, then act" action — the summoner walks to the summon point and has
// nothing left to do this turn. The spoken agreement rides the `say` argument
// (LLM-414): speak is ALSO terminal-on-success (LLM-321), so a speak-first
// ordering would end the tick before the summon ever ran — exactly the live
// failure this rework fixes.
//
// Returns an error on registration failure (duplicate name, malformed schema
// bytes) — a startup wiring bug the caller should fail loudly on.
func RegisterSummon(r *Registry) error {
	return r.RegisterCommit(
		"summon",
		summonSchema,
		DecodeSummonArgs,
		HandleSummon,
		true, // terminal: sending for someone ends the tick (say your piece first)
		WithDescription(summonDescription),
	)
}
