package handlers

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// tool_gating.go — ZBBS-HOME-306. The deliberation-prompt / tool-gating
// seam: the per-tick advertised tool set is a function of (registry,
// payload, snapshot), not a static list.
//
// Built as a general seam with a single wired consumer (pay-offer
// deliberation), per discussion 109. Future consumers (e.g. shift-based
// speak gating for prompt adherence) slot in here without changing the
// callsite or signature.

// payOfferResponseTools are the seller-side pay-deliberation tools that are
// advertised ONLY when the actor's perception carries a pending pay offer
// (a PayOfferWarrantReason). Mapped to a set for O(1) membership.
//
// These stay AvailabilityAvailable in the registry — gating is an
// *advertising* decision, not a *dispatch* one — so a call that does arrive
// is still dispatchable and the substrate stays authoritative. We do NOT use
// AvailabilityDisabled: that flag also makes a tool non-dispatchable
// (tool_validate.go's Validate rejects any non-Available entry), which would
// reject the seller's accept_pay the instant they tried to use it. Gating
// purely at the advertising layer (here) gives "only offered when relevant"
// without breaking dispatch.
//
// withdraw_pay is intentionally NOT gated here: it is buyer-side, and the
// buyer holds no PayOfferWarrantReason (the offer warrant lands on the seller
// only — engine/sim/handlers/pay_with_item_reactor.go), so there is no
// seller-side signal to gate it on. It stays unconditionally advertised.
// Gating it correctly needs a buyer-side "your outstanding offers" perception
// view that does not exist yet (out of scope; discussion 109 point 4 as
// amended).
var payOfferResponseTools = map[string]struct{}{
	"accept_pay":  {},
	"decline_pay": {},
	"counter_pay": {},
}

// gateTools computes the per-tick advertised tool set from the registry's
// Available tools, conditioned on the actor's perception.
//
// Pay-offer consumer: the seller-response tools (accept/decline/counter) are
// advertised only when a pending pay offer is present in the payload. The
// same predicate (perception.PayOfferWarrants) drives the perception
// offer-decision section, so the rendered offer and the advertised tools
// cannot drift (discussion 109 invariant).
//
// Registration order is preserved (the gated tools, when included, stay in
// their registered positions) for provider prompt-cache stability.
//
// snap is part of the general signature for future consumers that need world
// state the warrant batch does not carry (e.g. shift state for speak gating);
// the pay consumer reads only the payload.
func gateTools(r *Registry, payload perception.Payload, snap *sim.Snapshot) []llm.ToolSpec {
	all := r.AdvertisedSpecs()
	hasPayOffer := len(perception.PayOfferWarrants(payload)) > 0

	// Single pass over the Available set so each gated group is evaluated
	// against its OWN condition. We deliberately avoid a "pending offer →
	// return all" fast path: that would re-enable every future gated tool
	// whenever a pay offer happened to be present, silently bypassing that
	// tool's own gate. Today only the pay-response group is gated; this shape
	// keeps the general seam composable as more groups are added.
	out := make([]llm.ToolSpec, 0, len(all))
	for _, spec := range all {
		if _, gated := payOfferResponseTools[spec.Name]; gated && !hasPayOffer {
			// Seller-response tool with no pending offer: drop it (noise +
			// ledger_id hallucination vector).
			continue
		}
		out = append(out, spec)
	}
	return out
}
