package handlers

// register_stop.go — production registration for the stop tool
// (ZBBS-HOME-338). Same opt-in-piecewise pattern as register_gather.go.

// RegisterStop adds the stop tool to r as a ClassCommit entry,
// AvailabilityAvailable, TERMINAL on success: a successful stop ends the tick,
// so the next tick re-perceives the actor as stationary and the
// movement-gated tools (consume / speak / …) reappear in its advertised set.
// The decoder is DecodeStopArgs; the handler is HandleStop; the substrate
// command is sim.StopMove.
//
// Advertising is gated at the prompt layer by gateTools — offered ONLY while
// the actor is moving — but the tool stays AvailabilityAvailable so a call
// that does arrive is still dispatchable; sim.StopMove is the authoritative
// gate (rejects when the actor isn't walking).
func RegisterStop(r *Registry) error {
	return r.RegisterCommit(
		"stop",
		stopSchema,
		DecodeStopArgs,
		HandleStop,
		true, // terminal on success — stopping ends the tick
		WithDescription(stopDescription),
	)
}
