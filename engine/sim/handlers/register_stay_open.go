package handlers

// register_stay_open.go — production registration helper for the stay_open tool
// (ZBBS-WORK-387). Same opt-in-piecewise pattern as register_take_break.go — the
// entrypoint composes the tool surface it wants (see cmd/engine/main.go
// registerTools).

// RegisterStayOpen adds the stay_open tool to r as a ClassCommit entry,
// AvailabilityAvailable. The commit handler is HandleStayOpen; the decoder is
// DecodeStayOpenArgs; both live in stay_open.go.
//
// terminalOnSuccess is FALSE: stay_open is non-terminal so the model can pair it
// with a speak in the same tick — the in-world social beat that announces the
// keeper is staying open ("I'll keep the forge lit a while yet"). Same
// non-terminal policy as take_break. The already-committed reject in
// sim.StayOpen guards against a within-tick repeat.
//
// Returns an error on registration failure (duplicate name, malformed schema
// bytes) — a startup wiring bug the caller should fail loudly on.
func RegisterStayOpen(r *Registry) error {
	return r.RegisterCommit(
		"stay_open",
		stayOpenSchema,
		DecodeStayOpenArgs,
		HandleStayOpen,
		false, // non-terminal: a within-tick step (model may add a speak), not a tick-ender
		WithDescription(stayOpenDescription),
	)
}
