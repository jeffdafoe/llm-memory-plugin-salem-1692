package handlers

// register_labor.go — LLM-26 production registration helpers for the four
// service-for-pay tools (solicit_work, offer_work, accept_work, decline_work).
// Same opt-in-piecewise + composite pattern as register_pay_with_item.go.
//
// Every one of them is terminal-on-success, and so is speak (LLM-321). Any cue
// that instructs one of these AND a speak in the same turn is therefore
// instructing something impossible — the first call to land ends the tick. That
// is the LLM-343 defect; offer_work avoids it by carrying its own `say`.

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
// (LLM-175) and move_to.
//
// A worker announcing BEFORE offering no longer works: LLM-180 was written when
// speak was non-terminal, and LLM-321 made speak end the tick too, so a worker who
// speaks first never reaches solicit_work. No cue instructs that order
// (renderLaborAffordance names only the tool), so there is no live failure — but
// nothing prevents one either, and the fix if it appears is offer_work's: fold the
// utterance into the tool.
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

// RegisterOfferWork adds the offer_work tool to r (LLM-346).
//
// terminalOnSuccess is TRUE, for solicit_work's reason with the roles reversed: a
// placed offer of work is instant, the worker answers on THEIR turn, and a second
// offer this tick is a guaranteed no-op the duplicate-offer gate rejects.
//
// The keeper's spoken request rides on the tool's own `say` rather than a chained
// speak, because both verbs are terminal and whichever lands first ends the tick
// (LLM-343). Nothing is lost: the offer is minted before the words go out, so a
// refused offer is a silent one.
func RegisterOfferWork(r *Registry) error {
	return r.RegisterCommit(
		"offer_work",
		offerWorkSchema,
		DecodeOfferWorkArgs,
		HandleOfferWork,
		true, // terminal: a placed offer of work ends the tick (LLM-346)
		WithDescription(offerWorkDescription),
	)
}

// RegisterAcceptWork adds the accept_work tool to r.
//
// terminalOnSuccess is TRUE (LLM-184): accepting hires the worker atomically,
// so there is nothing mechanical left to do this tick. The courtesy after-word
// ("good, get started") is the re-fire vector the weak model stormed
// (accept_work x6, observed live on pooled AND stateful NPCs). The already_answered
// guard (LLM-164) stays a backstop. (The "before-speak is preserved" rationale this
// comment once carried died with LLM-321, which made speak terminal as well.)
//
// The acceptor agrees aloud through accept_work's own `say` (LLM-350), spoken
// from INSIDE sim.AcceptWorkSaying rather than a handler-level wrapper: a
// relocating accept sets the worker walking and drops them from the huddle before
// any wrapper could speak. See HandleAcceptWork.
func RegisterAcceptWork(r *Registry) error {
	return r.RegisterCommit(
		"accept_work",
		acceptWorkSchema,
		DecodeAcceptWorkArgs,
		HandleAcceptWork,
		true, // terminal: an atomic hire ends the tick (LLM-184)
		WithDescription(acceptWorkDescription),
	)
}

// RegisterDeclineWork adds the decline_work tool to r.
//
// terminalOnSuccess is TRUE (LLM-184): a decline is instant and final for this
// tick — nothing to chain — so the decline_work storm cannot recur.
//
// The refusal is spoken through decline_work's own `say` (LLM-350). Its old
// description — "if you want to explain or propose different terms, just say so
// in conversation" — asked for a speak the terminal decline had already made
// unreachable.
func RegisterDeclineWork(r *Registry) error {
	return r.RegisterCommit(
		"decline_work",
		declineWorkSchema,
		DecodeDeclineWorkArgs,
		HandleDeclineWork,
		true, // terminal: an instant decline ends the tick (LLM-184)
		WithDescription(declineWorkDescription),
	)
}

// RegisterLaborFamily registers all four service-for-pay tools in one
// call. Stops at the first registration error so a downstream composer
// fails loud on startup misconfiguration rather than registering a partial
// family (a worker that can solicit but no one that can accept, or a keeper who
// can offer work no one can take).
func RegisterLaborFamily(r *Registry) error {
	if err := RegisterSolicitWork(r); err != nil {
		return err
	}
	if err := RegisterOfferWork(r); err != nil {
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
