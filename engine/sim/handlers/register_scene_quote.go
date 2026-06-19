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
// (advertises item_kind / qty / amount / consume_now /
// target_buyer / consumers — buyer-side pay_with_item fast-path
// references quote_id from the buyer's perspective, which is a
// separate tool landing in PR S4). The commit handler is
// HandleSceneQuote; the decoder is DecodeSceneQuoteArgs; both live
// in scene_quote.go.
//
// terminalOnSuccess is FALSE: scene_quote is non-terminal so the
// seller can follow with a speak ("I'm running a special on stew
// tonight, quote #5") or other tool in the same tick — same
// rationale as pay/speak/consume.
//
// Returns an error on registration failure (duplicate name, malformed
// schema bytes — both startup bugs the caller should panic/exit on).
func RegisterSceneQuote(r *Registry) error {
	return r.RegisterCommit(
		"sell",
		sceneQuoteSchema,
		DecodeSceneQuoteArgs,
		HandleSceneQuote,
		false, // non-terminal: scene_quote is a within-tick step, not a tick-ender
		WithDescription(sceneQuoteDescription),
	)
}
