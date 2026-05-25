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

// counterPayToolName is the seller's counter tool — gated more tightly than
// the rest of the seller-response group (see gateTools / scar #4).
const counterPayToolName = "counter_pay"

// recallToolName is the recall observation tool — advertised ONLY to agents
// with a dedicated VA / own memory namespace (ZBBS-WORK-321). A shared-VA NPC
// (vendor/visitor-backed) has no personal memory to recall.
const recallToolName = "recall"

// actorHasDedicatedVA reports whether the acting actor is a stateful NPC
// (KindNPCStateful = "own VA with memory", per actor.go). Shared-VA NPCs have
// no personal memory, so recall is not advertised to them. Returns false when
// the actor can't be resolved (nil snapshot / missing actor) — conservative:
// don't advertise a memory tool to an actor we can't confirm has memory.
func actorHasDedicatedVA(actorID sim.ActorID, snap *sim.Snapshot) bool {
	if snap == nil {
		return false
	}
	a, ok := snap.Actors[actorID]
	if !ok || a == nil {
		return false
	}
	return a.Kind == sim.KindNPCStateful
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
// counter_pay carries an ADDITIONAL gate (ZBBS-WORK-320, pc/pay scar #4):
// it is dropped when every pending offer is already at the counter-chain
// depth cap (sim.MaxPayCounterChainDepth). A seller can still counter an
// offer at the cap, but the buyer can no longer answer it — validateInResponseTo
// rejects a response when parent.Depth >= cap — so the counter is a wasted,
// unanswerable round. The buyer-side guard already bounds the chain; this just
// removes the noise + ledger_id-hallucination vector of advertising a dead
// counter. accept_pay / decline_pay stay advertised regardless of depth (an
// offer at the cap is still acceptable / declinable). When offers of mixed
// depth are present, counter_pay stays advertised as long as AT LEAST ONE is
// below the cap (the seller's counter_pay(ledger_id=N) targets a specific
// offer, so a useful target existing is sufficient).
//
// Registration order is preserved (the gated tools, when included, stay in
// their registered positions) for provider prompt-cache stability.
//
// recall (ZBBS-WORK-321) is the first consumer to use snap: it advertises
// recall only to dedicated-VA agents (`snap.Actors[id].Kind == KindNPCStateful`)
// — a shared-VA NPC has no own memory namespace to search. snap remains the
// channel for future consumers needing world state the warrant batch doesn't
// carry (e.g. shift state for speak gating); the pay consumer reads only the
// payload.
func gateTools(r *Registry, payload perception.Payload, snap *sim.Snapshot) []llm.ToolSpec {
	all := r.AdvertisedSpecs()
	offers := perception.PayOfferWarrants(payload)
	hasPayOffer := len(offers) > 0
	canCounter := anyOfferCounterable(offers)
	dedicatedVA := actorHasDedicatedVA(payload.ActorID, snap)

	// Single pass over the Available set so each gated group is evaluated
	// against its OWN condition. We deliberately avoid a "pending offer →
	// return all" fast path: that would re-enable every future gated tool
	// whenever a pay offer happened to be present, silently bypassing that
	// tool's own gate. The shape keeps the general seam composable as more
	// consumers are added (pay-response group, recall, …).
	out := make([]llm.ToolSpec, 0, len(all))
	for _, spec := range all {
		// recall consumer (ZBBS-WORK-321): advertise only to dedicated-VA
		// agents — a shared-VA NPC has no personal memory to search.
		if spec.Name == recallToolName && !dedicatedVA {
			continue
		}
		if _, gated := payOfferResponseTools[spec.Name]; gated {
			if !hasPayOffer {
				// Seller-response tool with no pending offer: drop it (noise +
				// ledger_id hallucination vector).
				continue
			}
			if spec.Name == counterPayToolName && !canCounter {
				// Every pending offer is at the depth cap — a counter can't be
				// answered. Drop counter_pay (keep accept_pay / decline_pay).
				continue
			}
		}
		out = append(out, spec)
	}
	return out
}

// anyOfferCounterable reports whether at least one offer is below the
// counter-chain depth cap, i.e. a counter_pay on it could still be answered
// by the buyer (validateInResponseTo allows a response only while
// parent.Depth < sim.MaxPayCounterChainDepth). False for an empty slice.
func anyOfferCounterable(offers []sim.PayOfferWarrantReason) bool {
	for _, o := range offers {
		if o.Depth < sim.MaxPayCounterChainDepth {
			return true
		}
	}
	return false
}
