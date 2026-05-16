package handlers

// register_deliver_order.go — production registration helper for the
// deliver_order tool. Phase 3 PR S6.
//
// Composing your own Registry (opt-in piecewise, same as the rest of
// the Phase 3 tool family):
//
//	r := handlers.NewRegistry()
//	if err := handlers.RegisterSpeak(r); err != nil { return err }
//	if err := handlers.RegisterPay(r); err != nil { return err }
//	if err := handlers.RegisterConsume(r); err != nil { return err }
//	if err := handlers.RegisterPayWithItemFamily(r); err != nil { return err }
//	if err := handlers.RegisterDeliverOrder(r); err != nil { return err }
//	if err := r.RegisterTerminal("done"); err != nil { return err }

// RegisterDeliverOrder adds the deliver_order tool to r as a
// ClassCommit entry, AvailabilityAvailable. The schema is the narrow
// PR S6 form (advertises only order_id). The commit handler is
// HandleDeliverOrder; the decoder is DecodeDeliverOrderArgs; both
// live in deliver_order.go.
//
// terminalOnSuccess is FALSE: deliver_order is non-terminal so the
// seller can chain it with a speak ("Here you are, Jefferey.") in the
// same tick — the handover line lands narratively right after the
// engine-side transfer. Matches v1's keeper-deliver flow.
//
// Returns an error on registration failure (duplicate name, malformed
// schema bytes — both startup bugs the caller should panic/exit on).
func RegisterDeliverOrder(r *Registry) error {
	return r.RegisterCommit(
		"deliver_order",
		deliverOrderSchema,
		DecodeDeliverOrderArgs,
		HandleDeliverOrder,
		false, // non-terminal: seller may speak after delivering
		WithDescription(deliverOrderDescription),
	)
}
