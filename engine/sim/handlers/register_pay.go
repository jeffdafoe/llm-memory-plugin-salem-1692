package handlers

// register_pay.go — production registration helper for the pay tool.
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
//	if err := r.RegisterTerminal("done"); err != nil {
//	    return err
//	}
//	// register other tools as their PRs land...
//
// Same opt-in-piecewise pattern as register_speak.go — PR B exposes the
// pay helper; downstream composition (mounting harness, wiring worker
// pool, plumbing settings) is the cutover layer's responsibility.

// RegisterPay adds the pay tool to r as a ClassCommit entry,
// AvailabilityAvailable. The schema is the narrow PR B form (advertises
// only recipient/amount/for — item / qty / consume_now / consumers /
// in_response_to are deferred). The commit handler is HandlePay; the
// decoder is DecodePayArgs; both live in pay.go.
//
// terminalOnSuccess is FALSE: pay is non-terminal so the model can follow
// through with a speak ("Thank you, here's the news...") or move in the
// same tick — same rationale as speak's non-terminal policy.
//
// Returns an error on registration failure (duplicate name, malformed
// schema bytes — both startup bugs the caller should panic/exit on).
func RegisterPay(r *Registry) error {
	return r.RegisterCommit(
		"pay",
		paySchema,
		DecodePayArgs,
		HandlePay,
		false, // non-terminal: pay is a within-tick step, not a tick-ender
		WithDescription(payDescription),
	)
}
