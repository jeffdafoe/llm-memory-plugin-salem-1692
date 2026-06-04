package handlers

// register_speak.go — production registration helper for the speak tool.
//
// Composing your own Registry:
//
//	r := handlers.NewRegistry()
//	if err := handlers.RegisterSpeak(r); err != nil {
//	    return err
//	}
//	if err := r.RegisterTerminal("done"); err != nil {
//	    return err
//	}
//	// register other tools as their PRs land...
//
// There is intentionally NO canonical "RegisterAllProductionTools" — the
// rest of Phase 3 (pay / serve / deliver_order) and cutover-time
// concerns (mounting the harness, wiring the worker pool, plumbing
// settings) are independent decisions. PR A just exposes the speak
// helper; downstream composition is the cutover layer's responsibility.

// RegisterSpeak adds the speak tool to r as a ClassCommit entry,
// AvailabilityAvailable. The schema is the narrow PR A form (advertises
// only `text` — mentions/price/state-claim subsystems are deferred). The
// commit handler is HandleSpeak; the decoder is DecodeSpeakArgs; both
// live in speak.go.
//
// terminalOnSuccess is FALSE: speak is non-terminal so the model can
// follow through with a move/chore inside the same tick (the iteration-
// loop behavior v1's executeAgentCommit already had — see the harness
// doc on Multi-tool turns at handlers/harness.go). A speak does NOT end
// the tick: the model ends the turn by calling done() after it has nothing
// new to say. The within-tick repeat that this freedom risks is held by the
// ZBBS-WORK-375 same-tick dedup guard + post-speak continuation steer in the
// harness, NOT by a per-tool cap (HOME-381's cap was removed in WORK-375
// because it also cut legitimate two-beat turns, e.g. greet THEN answer).
//
// Returns an error on registration failure (duplicate name, malformed
// schema bytes — both startup bugs the caller should panic/exit on).
func RegisterSpeak(r *Registry) error {
	return r.RegisterCommit(
		"speak",
		speakSchema,
		DecodeSpeakArgs,
		HandleSpeak,
		false, // non-terminal: speak is a within-tick step, not a tick-ender
		WithDescription(speakDescription),
	)
}
