package handlers

// register_labor.go — LLM-26 production registration helpers for the three
// service-for-pay tools (solicit_work, accept_work, decline_work). Same
// opt-in-piecewise + composite pattern as register_pay_with_item.go.

// RegisterSolicitWork adds the solicit_work tool to r as a ClassCommit
// entry.
//
// terminalOnSuccess is TRUE (LLM-180): a placed labor offer ends the tick.
// Posting a solicitation is instant and there is nothing useful to do with it
// this tick — the offer stands until the employer answers on THEIR turn — so a
// second solicit is a guaranteed no-op (the solicitedThisTick guard rejects it)
// and a chained speak is the re-pitch the storm was made of. Ending the tick
// here kills at the source the within-tick re-fire loop a weak model fell into
// (solicit_work x6 to the round budget, observed live) — mirrors gather
// (LLM-175) and move_to. The worker still announces BEFORE offering (speak is
// non-terminal: speak, then solicit ends the tick); only the courtesy word
// AFTER is dropped, and that was the re-pitch vector.
func RegisterSolicitWork(r *Registry) error {
	return r.RegisterCommit(
		"solicit_work",
		solicitWorkSchema,
		DecodeSolicitWorkArgs,
		HandleSolicitWork,
		true, // terminal: a placed labor offer ends the tick (LLM-180)
		WithDescription(solicitWorkDescription),
	)
}

// RegisterAcceptWork adds the accept_work tool to r. Non-terminal — the
// employer can chain a speak ("good, get started") after accepting.
func RegisterAcceptWork(r *Registry) error {
	return r.RegisterCommit(
		"accept_work",
		acceptWorkSchema,
		DecodeAcceptWorkArgs,
		HandleAcceptWork,
		false,
		WithDescription(acceptWorkDescription),
	)
}

// RegisterDeclineWork adds the decline_work tool to r. Non-terminal.
func RegisterDeclineWork(r *Registry) error {
	return r.RegisterCommit(
		"decline_work",
		declineWorkSchema,
		DecodeDeclineWorkArgs,
		HandleDeclineWork,
		false,
		WithDescription(declineWorkDescription),
	)
}

// RegisterLaborFamily registers all three service-for-pay tools in one
// call. Stops at the first registration error so a downstream composer
// fails loud on startup misconfiguration rather than registering a partial
// family (a worker that can solicit but no one that can accept).
func RegisterLaborFamily(r *Registry) error {
	if err := RegisterSolicitWork(r); err != nil {
		return err
	}
	if err := RegisterAcceptWork(r); err != nil {
		return err
	}
	if err := RegisterDeclineWork(r); err != nil {
		return err
	}
	return nil
}
