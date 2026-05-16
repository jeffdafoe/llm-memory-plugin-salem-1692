package handlers

// register_pay_with_item.go — Phase 3 PR S4 step 6. Production
// registration helpers for the five pay-with-item tools.
//
// Composing your own Registry:
//
//	r := handlers.NewRegistry()
//	if err := handlers.RegisterSpeak(r); err != nil { return err }
//	if err := handlers.RegisterPay(r); err != nil { return err }
//	if err := handlers.RegisterConsume(r); err != nil { return err }
//	if err := handlers.RegisterSceneQuote(r); err != nil { return err }
//	if err := handlers.RegisterPayWithItemFamily(r); err != nil { return err }
//	if err := r.RegisterTerminal("done"); err != nil { return err }
//
// RegisterPayWithItemFamily is the convenience composite — it wires up
// all five tools (pay_with_item, accept_pay, decline_pay, counter_pay,
// withdraw_pay) in one call. The individual RegisterX functions are
// also exported in case a downstream composer needs to register a
// subset (e.g. an integration test that only exercises pay_with_item
// without the resolution surface).
//
// Same opt-in-piecewise pattern as register_pay.go / register_consume.go
// / register_scene_quote.go.

// RegisterPayWithItem adds the pay_with_item tool to r as a
// ClassCommit entry. Non-terminal — the buyer can chain speak / move
// after offering.
func RegisterPayWithItem(r *Registry) error {
	return r.RegisterCommit(
		"pay_with_item",
		payWithItemSchema,
		DecodePayWithItemArgs,
		HandlePayWithItem,
		false, // non-terminal: pay_with_item is a within-tick step
		WithDescription(payWithItemDescription),
	)
}

// RegisterAcceptPay adds the accept_pay tool to r. Non-terminal — the
// seller can chain a speak ("thank you, here's your stew") after
// accepting.
func RegisterAcceptPay(r *Registry) error {
	return r.RegisterCommit(
		"accept_pay",
		acceptPaySchema,
		DecodeAcceptPayArgs,
		HandleAcceptPay,
		false,
		WithDescription(acceptPayDescription),
	)
}

// RegisterDeclinePay adds the decline_pay tool to r. Non-terminal.
func RegisterDeclinePay(r *Registry) error {
	return r.RegisterCommit(
		"decline_pay",
		declinePaySchema,
		DecodeDeclinePayArgs,
		HandleDeclinePay,
		false,
		WithDescription(declinePayDescription),
	)
}

// RegisterCounterPay adds the counter_pay tool to r. Non-terminal.
func RegisterCounterPay(r *Registry) error {
	return r.RegisterCommit(
		"counter_pay",
		counterPaySchema,
		DecodeCounterPayArgs,
		HandleCounterPay,
		false,
		WithDescription(counterPayDescription),
	)
}

// RegisterWithdrawPay adds the withdraw_pay tool to r. Non-terminal.
func RegisterWithdrawPay(r *Registry) error {
	return r.RegisterCommit(
		"withdraw_pay",
		withdrawPaySchema,
		DecodeWithdrawPayArgs,
		HandleWithdrawPay,
		false,
		WithDescription(withdrawPayDescription),
	)
}

// RegisterPayWithItemFamily registers all five pay-with-item tools in
// one call. Stops at the first registration error so a downstream
// composer can panic / log on startup misconfiguration without
// silently registering a partial family.
//
// Equivalent to calling each RegisterX in sequence; provided as a
// composite because the family is a unit and registering 4 of 5 leaves
// the LLM with a dangling offer it can't resolve.
func RegisterPayWithItemFamily(r *Registry) error {
	if err := RegisterPayWithItem(r); err != nil {
		return err
	}
	if err := RegisterAcceptPay(r); err != nil {
		return err
	}
	if err := RegisterDeclinePay(r); err != nil {
		return err
	}
	if err := RegisterCounterPay(r); err != nil {
		return err
	}
	if err := RegisterWithdrawPay(r); err != nil {
		return err
	}
	return nil
}
