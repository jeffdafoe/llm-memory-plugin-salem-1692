package handlers

// register_take_break.go — production registration helper for the take_break
// tool (ZBBS-HOME-284 #4). Same opt-in-piecewise pattern as
// register_consume.go / register_pay.go — the entrypoint composes the tool
// surface it wants (see cmd/engine/main.go registerTools).

// RegisterTakeBreak adds the take_break tool to r as a ClassCommit entry,
// AvailabilityAvailable. The commit handler is HandleTakeBreak; the decoder is
// DecodeTakeBreakArgs; both live in take_break.go.
//
// terminalOnSuccess is FALSE: take_break is non-terminal so the model can pair
// it with a speak in the same tick ("I must close up — back at 1pm"). The
// already-on-break reject in sim.TakeBreak guards against a within-tick repeat
// (the first call stamps BreakUntil, the second rejects). Same non-terminal
// policy as consume/pay/speak.
//
// Returns an error on registration failure (duplicate name, malformed schema
// bytes) — a startup wiring bug the caller should fail loudly on.
func RegisterTakeBreak(r *Registry) error {
	return r.RegisterCommit(
		"take_break",
		takeBreakSchema,
		DecodeTakeBreakArgs,
		HandleTakeBreak,
		false, // non-terminal: a within-tick step (model may add a speak), not a tick-ender
		WithDescription(takeBreakDescription),
	)
}
