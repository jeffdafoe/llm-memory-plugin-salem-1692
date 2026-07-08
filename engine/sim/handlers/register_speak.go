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
// terminalOnSuccess is TRUE: a successful speak ends the tick (LLM-321).
// The turn prompt previously told the model to call done() after speaking,
// costing a second full LLM round whose only job was to emit done() —
// ~22% of all reactor-tick calls were that trailing termination. Ending on
// the speak commit itself kills that round: one social action per tick, the
// accept-one-success-per-tick shape already used by the LLM-184 commit verbs
// and the LLM-201 produce flip. A model that wants to speak AND then act is
// split across two ticks; the second beat re-fires for free on the next scan,
// and multi-actor conversation still advances because each utterance spawns
// the other party's own reply tick. The same-tick repeat that non-terminal
// speak risked no longer exists — the first successful speak ends the tick,
// so a second speak (in the same batch or a later round) is unreachable.
//
// Returns an error on registration failure (duplicate name, malformed
// schema bytes — both startup bugs the caller should panic/exit on).
func RegisterSpeak(r *Registry) error {
	return r.RegisterCommit(
		"speak",
		speakSchema,
		DecodeSpeakArgs,
		HandleSpeak,
		true, // terminal-on-success: a successful speak ends the tick (LLM-321)
		WithDescription(speakDescription),
	)
}
