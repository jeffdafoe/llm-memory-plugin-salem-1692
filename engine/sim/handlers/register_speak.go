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
// doc on Multi-tool turns at handlers/harness.go).
//
// WithOneUtterancePerTick caps speech at a single utterance per tick and
// ends the tick after the round it commits in (ZBBS-HOME-381). This is the
// live-path home for HOME-379's one-speak cap, which only ever landed in the
// dead v1 agent_tick.go. Without it, a non-terminal speak loops back to the
// model each round and — with no new input — re-pitches the same line until
// the iteration budget force-ends the tick (the observed speak×6 ramble).
// Speak alongside a terminal (move_to/done) in the SAME response still ends
// the tick via that terminal; the cap only stops the cross-round re-speak.
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
		WithOneUtterancePerTick(),
	)
}
