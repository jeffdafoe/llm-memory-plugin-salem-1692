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
// ClassCommit entry.
//
// terminalOnSuccess is TRUE (LLM-184): a placed buy offer ends the tick. The
// offer is instant and stands until the seller answers on THEIR turn, so there
// is nothing useful to chain after it — a second pay_with_item is a guaranteed
// no-op (the already_offered guard rejects it) and the courtesy after-word is
// the re-pitch the weak model stormed to the round budget (pay_with_item x6,
// observed live). The buyer still announces BEFORE offering (speak is
// non-terminal: speak, then pay_with_item ends the tick); only the after-word is
// dropped. Mirrors solicit_work (LLM-180) / gather (LLM-175).
func RegisterPayWithItem(r *Registry) error {
	return r.RegisterCommit(
		"pay_with_item",
		payWithItemSchema,
		DecodePayWithItemArgs,
		HandlePayWithItem,
		true, // terminal: a placed buy offer ends the tick (LLM-184)
		WithDescription(payWithItemDescription),
	)
}

// RegisterAcceptPay adds the accept_pay tool to r.
//
// terminalOnSuccess is TRUE (LLM-184): accepting settles the sale atomically,
// so there is nothing mechanical left to do this tick. The courtesy after-word
// ("thank you, here's your stew") is exactly the re-fire vector the weak model
// stormed. The already_answered guard stays a backstop.
//
// The seller answers through accept_pay's own `say`, NOT a speak before or after
// it (LLM-350). The comment here used to claim "the seller's before-speak is
// preserved (speak is non-terminal)" — true when LLM-184 landed, false since
// LLM-321 made speak terminal, and nothing back-propagated the change. Either
// order lost something: a speak first ended the tick and the offer went
// unanswered; an accept first had the speak skipped as post_terminal and the
// sale settled in silence. The same rot LLM-343 found in register_scene_quote.go
// and LLM-346 found in register_labor.go.
func RegisterAcceptPay(r *Registry) error {
	return r.RegisterCommit(
		"accept_pay",
		acceptPaySchema,
		DecodeAcceptPayArgs,
		HandleAcceptPay,
		true, // terminal: an atomic settle ends the tick (LLM-184)
		WithDescription(acceptPayDescription),
	)
}

// RegisterDeclinePay adds the decline_pay tool to r.
//
// terminalOnSuccess is TRUE (LLM-184): a decline is instant and final for this
// tick — nothing to chain — so ending the tick here kills the decline_pay x6
// storm (observed live) at the source. The already_X guard stays a backstop.
//
// The refusal is spoken through decline_pay's own `say` (LLM-350), which also
// subsumes the old silent `reason` field — see DeclinePayArgs.
func RegisterDeclinePay(r *Registry) error {
	return r.RegisterCommit(
		"decline_pay",
		declinePaySchema,
		DecodeDeclinePayArgs,
		HandleDeclinePay,
		true, // terminal: an instant decline ends the tick (LLM-184)
		WithDescription(declinePayDescription),
	)
}

// RegisterCounterPay adds the counter_pay tool to r.
//
// terminalOnSuccess is TRUE (LLM-184): a counter places a fresh offer that
// stands until the other party answers on THEIR turn — same shape as
// pay_with_item — so there is nothing to chain after it this tick.
//
// The counter's terms are spoken through its own `say` (LLM-350), which also
// subsumes the old silent `message` field — see CounterPayArgs.
func RegisterCounterPay(r *Registry) error {
	return r.RegisterCommit(
		"counter_pay",
		counterPaySchema,
		DecodeCounterPayArgs,
		HandleCounterPay,
		true, // terminal: a placed counter-offer ends the tick (LLM-184)
		WithDescription(counterPayDescription),
	)
}

// RegisterWithdrawPay adds the withdraw_pay tool to r.
//
// terminalOnSuccess is TRUE (LLM-184): retracting an offer is instant and final
// for this tick — nothing to chain — so the withdraw_pay x6 storm (observed
// live) cannot recur.
func RegisterWithdrawPay(r *Registry) error {
	return r.RegisterCommit(
		"withdraw_pay",
		withdrawPaySchema,
		DecodeWithdrawPayArgs,
		HandleWithdrawPay,
		true, // terminal: an instant withdrawal ends the tick (LLM-184)
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
