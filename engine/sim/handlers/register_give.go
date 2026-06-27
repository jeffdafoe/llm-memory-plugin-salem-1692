package handlers

// register_give.go — LLM-138. Registration helpers for the one-way gift
// family (give / accept_gift / decline_gift). Same opt-in-piecewise pattern
// as register_pay_with_item.go; cmd/engine composes the surface it wants.

// RegisterGive adds the give tool as a ClassCommit entry. Non-terminal — the
// giver can chain a speak ("here, take these") after offering.
func RegisterGive(r *Registry) error {
	return r.RegisterCommit(
		"give",
		giveSchema,
		DecodeGiveArgs,
		HandleGive,
		false,
		WithDescription(giveDescription),
	)
}

// RegisterAcceptGift adds the accept_gift tool. Non-terminal — the recipient
// can chain a speak ("my thanks") after accepting.
func RegisterAcceptGift(r *Registry) error {
	return r.RegisterCommit(
		"accept_gift",
		acceptGiftSchema,
		DecodeAcceptGiftArgs,
		HandleAcceptGift,
		false,
		WithDescription(acceptGiftDescription),
	)
}

// RegisterDeclineGift adds the decline_gift tool. Non-terminal.
func RegisterDeclineGift(r *Registry) error {
	return r.RegisterCommit(
		"decline_gift",
		declineGiftSchema,
		DecodeDeclineGiftArgs,
		HandleDeclineGift,
		false,
		WithDescription(declineGiftDescription),
	)
}

// RegisterGiveFamily registers all three gift tools in one call. Stops at the
// first registration error so a downstream composer can panic / log on
// startup misconfiguration. Registering give without accept_gift / decline_gift
// would leave the recipient unable to resolve a pending gift.
func RegisterGiveFamily(r *Registry) error {
	if err := RegisterGive(r); err != nil {
		return err
	}
	if err := RegisterAcceptGift(r); err != nil {
		return err
	}
	if err := RegisterDeclineGift(r); err != nil {
		return err
	}
	return nil
}
