package handlers

// register_labor.go — LLM-26 production registration helpers for the three
// service-for-pay tools (solicit_work, accept_work, decline_work). Same
// opt-in-piecewise + composite pattern as register_pay_with_item.go.

// RegisterSolicitWork adds the solicit_work tool to r as a ClassCommit
// entry. Non-terminal — a worker can chain speak after offering.
func RegisterSolicitWork(r *Registry) error {
	return r.RegisterCommit(
		"solicit_work",
		solicitWorkSchema,
		DecodeSolicitWorkArgs,
		HandleSolicitWork,
		false, // non-terminal: offering is a within-tick step
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
