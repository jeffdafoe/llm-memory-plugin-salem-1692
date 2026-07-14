package sim

// huddle_commerce_guard.go — "is this conversation carrying a live deal?"
//
// A huddle mid-negotiation must never be concluded by a time-based sweep: the
// counterparty is blocked on an answer, and cutting the scene strands a pending
// ledger entry mid-flight. Originally the eco-conclude sweep's guard (LLM-334);
// it now guards the loop sweep's lingering arm (LLM-397), which is the arm that
// can otherwise land on a perfectly healthy, productive conversation — one that
// sold a bowl of porridge and kept talking.
//
// Two signals, OR'd. The LEDGER signal covers a deal still on the books; the
// WARRANT signal covers the beats around a deal that has already resolved (a
// handover, a thanks) which ride commerce-commitment warrants after the ledger
// entry is terminal. Neither is consulted by the lexical or ledger loop arms —
// those conclude BECAUSE commerce is going nowhere, which is a different verdict
// from "leave this deal alone."

// ledgerCommerceHuddles returns the set of huddles carrying a LIVE commitment
// negotiation — a pay-ledger entry that is pending or countered, or a labor
// offer in a non-terminal state (pending / en_route / working) — each stamped
// with the huddle's ID. One O(ledger) pass per ledger, shared by the whole
// scan, mirroring ledgerStandoffHuddles' posture. Terminal states do not hold a
// conversation open: a settled sale's handover beats are covered by the
// member-warrant check (serve_handover, paid, pay_resolved), and a settled hire
// is a terminal labor state (completed/declined/expired/failed_unavailable)
// whose scene is already done.
//
// Labor parity (LLM-348): a hire negotiated over several beats consumes its
// LaborOffer warrant on the first tick, so by the time a sweep's clock elapses
// the remaining beats are social-only and huddleMemberHoldsCommerceWarrant no
// longer fires. The live ledger entry is what keeps the scene from being cut
// mid-hire — exactly as a pending pay entry does for a sale.
func ledgerCommerceHuddles(w *World) map[HuddleID]struct{} {
	var out map[HuddleID]struct{}
	for _, e := range w.PayLedger {
		if e == nil || e.HuddleID == "" {
			continue
		}
		if e.State != PayLedgerStatePending && e.State != PayLedgerStateCountered {
			continue
		}
		if out == nil {
			out = make(map[HuddleID]struct{})
		}
		out[e.HuddleID] = struct{}{}
	}
	for _, o := range w.LaborLedger {
		if o == nil || o.HuddleID == "" {
			continue
		}
		if o.State != LaborStatePending && o.State != LaborStateEnRoute && o.State != LaborStateWorking {
			continue
		}
		if out == nil {
			out = make(map[HuddleID]struct{})
		}
		out[o.HuddleID] = struct{}{}
	}
	return out
}

// huddleMemberHoldsCommerceWarrant reports whether any member's pending warrant
// cycle contains a commerce-commitment kind belonging to THIS conversation — a
// counterparty is blocked on that member's answer, so the conversation is
// commerce-carrying even if the ledger entry has already resolved
// (handover/thanks beats ride paid/pay_resolved/serve_handover warrants).
//
// Scoped, not blanket (code_review): a commerce warrant stamped with a
// DIFFERENT HuddleID belongs to another conversation and must not hold this
// one open — otherwise an actor carrying a stale pay_offer from elsewhere
// would commerce-protect every huddle it joins. A warrant with huddle identity
// counts only on an exact match; a warrant WITHOUT one (not every commerce mint
// site stamps meta.HuddleID) counts only when its counterparty (TriggerActorID /
// SourceActorID) is another member of this huddle — the deal is between people
// in this room. MUST run on the world goroutine.
func huddleMemberHoldsCommerceWarrant(w *World, h *Huddle) bool {
	for memberID := range h.Members {
		a := w.Actors[memberID]
		if a == nil {
			continue
		}
		for _, m := range a.Warrants {
			if !isCommerceCommitmentWarrantKind(m.Kind()) {
				continue
			}
			if m.HuddleID == h.ID {
				return true
			}
			if m.HuddleID != "" {
				continue // scoped to another conversation
			}
			if counterpartyInHuddle(h, memberID, m.TriggerActorID) ||
				counterpartyInHuddle(h, memberID, m.SourceActorID) {
				return true
			}
		}
	}
	return false
}

// counterpartyInHuddle reports whether id names a huddle member OTHER than the
// warrant holder — the "the deal's other side is in this room" test for
// commerce warrants that carry no huddle identity.
func counterpartyInHuddle(h *Huddle, holder, id ActorID) bool {
	if id == "" || id == holder {
		return false
	}
	_, ok := h.Members[id]
	return ok
}

// huddleCarriesLiveCommerce is the guard itself: this huddle is mid-deal, by
// either signal. commerceHuddles is the shared per-scan ledger pass, so a caller
// inside a loop pays one map lookup rather than a ledger walk per huddle.
func huddleCarriesLiveCommerce(w *World, h *Huddle, commerceHuddles map[HuddleID]struct{}) bool {
	if h == nil {
		return false
	}
	if _, ok := commerceHuddles[h.ID]; ok {
		return true
	}
	return huddleMemberHoldsCommerceWarrant(w, h)
}
