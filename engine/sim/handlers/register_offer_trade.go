package handlers

// register_offer_trade.go — ZBBS-HOME-407. Production registration helper
// for the offer_trade tool (the proposer-POV barter front door, see
// offer_trade_handlers.go).
//
// Same opt-in-piecewise pattern as register_pay_with_item.go — the
// entrypoint composes the tool surface it wants (cmd/engine/main.go
// registerTools).

// RegisterOfferTrade adds the offer_trade tool to r as a ClassCommit
// entry. Non-terminal — like pay_with_item, the proposer can chain a
// speak ("here, take these and give me the bread") after placing the
// offer.
//
// The commit handler is HandlePayWithItem, NOT a dedicated offer_trade
// handler: DecodeOfferTradeArgs lowers the proposer-POV args onto a
// PayWithItemArgs, so the offer travels the existing barter flow
// end-to-end. The only offer_trade-specific code is the decoder + schema.
func RegisterOfferTrade(r *Registry) error {
	return r.RegisterCommit(
		"offer_trade",
		offerTradeSchema,
		DecodeOfferTradeArgs,
		HandlePayWithItem,
		false, // non-terminal: offer_trade is a within-tick step
		WithDescription(offerTradeDescription),
	)
}
