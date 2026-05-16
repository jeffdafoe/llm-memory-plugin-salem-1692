package handlers

// register_consume.go — production registration helper for the consume tool.
//
// Composing your own Registry:
//
//	r := handlers.NewRegistry()
//	if err := handlers.RegisterSpeak(r); err != nil {
//	    return err
//	}
//	if err := handlers.RegisterPay(r); err != nil {
//	    return err
//	}
//	if err := handlers.RegisterConsume(r); err != nil {
//	    return err
//	}
//	if err := r.RegisterTerminal("done"); err != nil {
//	    return err
//	}
//	// register other tools as their PRs land...
//
// Same opt-in-piecewise pattern as register_pay.go / register_speak.go —
// PR S2 exposes the consume helper; downstream composition (mounting
// harness, wiring worker pool, plumbing settings) is the cutover layer's
// responsibility.

// RegisterConsume adds the consume tool to r as a ClassCommit entry,
// AvailabilityAvailable. The schema is the narrow PR S2 form (advertises
// only item + qty — group-feed via consumers[] is deferred alongside the
// buy/serve substrate). The commit handler is HandleConsume; the decoder
// is DecodeConsumeArgs; both live in consume.go.
//
// terminalOnSuccess is FALSE: consume is non-terminal so the model can
// follow with a speak ("Mmm, that hits the spot") or move in the same tick
// — same rationale as pay/speak's non-terminal policy.
//
// Returns an error on registration failure (duplicate name, malformed
// schema bytes — both startup bugs the caller should panic/exit on).
func RegisterConsume(r *Registry) error {
	return r.RegisterCommit(
		"consume",
		consumeSchema,
		DecodeConsumeArgs,
		HandleConsume,
		false, // non-terminal: consume is a within-tick step, not a tick-ender
		WithDescription(consumeDescription),
	)
}
