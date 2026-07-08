package handlers

// register_scene_quote.go — production registration helper for the
// scene_quote tool. Phase 3 PR S3.
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
//	if err := handlers.RegisterSceneQuote(r); err != nil {
//	    return err
//	}
//	if err := r.RegisterTerminal("done"); err != nil {
//	    return err
//	}
//	// register other tools as their PRs land...
//
// Same opt-in-piecewise pattern as register_pay.go / register_consume.go.

// RegisterSceneQuote adds the scene_quote tool to r as a ClassCommit
// entry, AvailabilityAvailable. The schema is the narrow PR S3 form
// (advertises item / qty / amount / consume_now /
// target_buyer / consumers — buyer-side pay_with_item fast-path
// references quote_id from the buyer's perspective, which is a
// separate tool landing in PR S4). The commit handler is
// HandleSceneQuote; the decoder is DecodeSceneQuoteArgs; both live
// in scene_quote.go.
//
// terminalOnSuccess is TRUE (LLM-184): a posted quote stands until a buyer
// answers on THEIR turn, so there is nothing useful to chain after it — a
// second sell of the same lot is a no-op (the same-tick quote guard rejects it)
// and the courtesy after-word ("I'm running a special on stew tonight, quote
// #5") is the re-pitch the weak model stormed to the round budget (sell x3,
// observed live). The seller still announces BEFORE quoting (speak is
// non-terminal); only the after-word is dropped. Mirrors solicit_work (LLM-180).
//
// Returns an error on registration failure (duplicate name, malformed
// schema bytes — both startup bugs the caller should panic/exit on).
func RegisterSceneQuote(r *Registry) error {
	return r.RegisterCommit(
		"sell",
		sceneQuoteSchema,
		DecodeSceneQuoteArgs,
		HandleSceneQuote,
		true, // terminal: a posted quote ends the tick (LLM-184)
		WithDescription(sceneQuoteDescription),
	)
}
