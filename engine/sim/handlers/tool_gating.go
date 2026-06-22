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
// (the standing PayOffersForMe ledger view, ZBBS-HOME-453). Mapped to a set
// for O(1) membership.
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

// gatherToolName is the gather tool — advertised ONLY when the actor is
// loitering at a gatherable source (ZBBS-WORK-328). The signal is the
// perception payload's SurroundingsView.GatherableItem, computed once in
// perception build (findGatherableCue); reading the SAME field the "gatherable"
// perception line renders from means the cue and the offered tool can't drift.
// Advertising-only: the tool stays AvailabilityAvailable so a call that arrives
// is still dispatchable, and sim.Gather is the authoritative resolver.
const gatherToolName = "gather"

// walkIncompatibleTools are the action tools the substrate rejects while the
// actor has an in-flight MoveIntent ("you are walking — finish your move
// before …"). gateTools drops them from the advertised set while the actor is
// moving (ZBBS-HOME-337): the model can't use them mid-walk, so advertising
// them only burns within-tick iterations and floods the reject log. They
// reappear once the actor is stationary (arrived, or halted via the stop
// tool). Kept in sync with the command-side gates — consume
// (item_commands.go), speak (speak_commands.go), gather (gather_commands.go),
// pay_with_item (pay_with_item_commands.go). The bare `pay` tool is no longer
// registered in production (ZBBS-HOME-430) so it has no entry here; its
// command-side gate in pay_commands.go remains for composed registries.
var walkIncompatibleTools = map[string]struct{}{
	"consume":       {},
	"speak":         {},
	"gather":        {},
	"pay_with_item": {},
	"offer_trade":   {}, // ZBBS-HOME-407: same substrate as pay_with_item (walk-in-flight reject)
}

// stopToolName — the voluntary-halt tool (ZBBS-HOME-338). The inverse of the
// walking gate: advertised ONLY while the actor is moving (a stationary actor
// has nothing to stop). It is the escape hatch that lets a walking NPC abandon
// a route so the walkIncompatibleTools become usable next tick.
const stopToolName = "stop"

// deliverOrderToolName — the seller's order check-in/handover tool. Advertised
// ONLY when the keeper actually has a Ready order to fulfill (a pending
// delivery in the perception payload). After ZBBS-HOME-398 the only Ready
// orders are lodging bookings awaiting check-in (physical takeaway hands over
// immediately at accept, so it never mints a Ready order), so this keeps
// deliver_order out of every other NPC's prompt instead of advertising it
// every tick to everyone — the discussion-109 "advertise a tool only with its
// triggering perception" invariant.
const deliverOrderToolName = "deliver_order"

// stayOpenToolName — the keeper's keep-shop-open-past-close tool. Advertised
// ONLY when the off-shift wind-down cue offers it: payload.DutySteer.OfferStayOpen,
// set in perception build on `!ToWork && !AtPost && AtOwnBusiness`
// (engine/sim/perception/build.go). Reading the SAME signal the stay-open prose
// renders from keeps the tool and its cue from drifting — the discussion-109
// "advertise a tool only with its triggering perception" invariant. Before
// LLM-66 stay_open had no gate and fell through to every actor every tick,
// offered off-post and to non-keepers.
const stayOpenToolName = "stay_open"

// actorIsMoving reports whether the subject has an in-flight move at snapshot
// time, read from the ZBBS-HOME-336 read-path projection (MoveDestKind is
// empty when the actor is not moving). False when the actor can't be resolved
// — conservative: don't hide the action tools for an actor we can't confirm is
// walking.
func actorIsMoving(actorID sim.ActorID, snap *sim.Snapshot) bool {
	if snap == nil {
		return false
	}
	a, ok := snap.Actors[actorID]
	if !ok || a == nil {
		return false
	}
	return a.MoveDestKind != ""
}

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
// same predicate (perception.PendingPayOffers — the standing ledger view,
// not the one-shot warrant batch, ZBBS-HOME-453) drives the perception
// offer-decision section, so the rendered offer and the advertised tools
// cannot drift (discussion 109 invariant), and both persist across ticks
// until the offer resolves or expires.
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
	offers := perception.PendingPayOffers(payload)
	hasPayOffer := len(offers) > 0
	canCounter := anyOfferCounterable(offers)
	dedicatedVA := actorHasDedicatedVA(payload.ActorID, snap)
	atGatherableSource := payload.Surroundings.GatherableItem != ""
	moving := actorIsMoving(payload.ActorID, snap)
	offerStayOpen := payload.DutySteer != nil && payload.DutySteer.OfferStayOpen

	// Single pass over the Available set so each gated group is evaluated
	// against its OWN condition. We deliberately avoid a "pending offer →
	// return all" fast path: that would re-enable every future gated tool
	// whenever a pay offer happened to be present, silently bypassing that
	// tool's own gate. The shape keeps the general seam composable as more
	// consumers are added (pay-response group, recall, …).
	out := make([]llm.ToolSpec, 0, len(all))
	for _, spec := range all {
		// walking gate (ZBBS-HOME-337): while the actor is mid-walk, drop the
		// action tools the substrate rejects on MoveIntent != nil — the model
		// can't use them until it arrives or stops, so advertising them only
		// wastes iterations and floods rejects.
		if moving {
			if _, gated := walkIncompatibleTools[spec.Name]; gated {
				continue
			}
		}
		// stop consumer (ZBBS-HOME-338): the inverse — advertise the voluntary
		// halt tool ONLY while moving (a stationary actor has nothing to stop).
		if spec.Name == stopToolName && !moving {
			continue
		}
		// recall consumer (ZBBS-WORK-321): advertise only to dedicated-VA
		// agents — a shared-VA NPC has no personal memory to search.
		if spec.Name == recallToolName && !dedicatedVA {
			continue
		}
		// gather consumer (ZBBS-WORK-328): advertise only when loitering at a
		// gatherable source — keeps the tool out of the prompt everywhere else.
		if spec.Name == gatherToolName && !atGatherableSource {
			continue
		}
		// deliver_order consumer (ZBBS-HOME-398): advertise only to a keeper
		// who has a Ready order to fulfill. Post-397 that means a lodging
		// booking awaiting check-in; physical takeaway delivers at accept and
		// never sits Ready, so this stops advertising the tool every tick to
		// every NPC. PendingDeliveriesFromMe is the seller-side Ready-order
		// view that also drives the "Orders to deliver" perception section, so
		// the tool and its cue can't drift.
		if spec.Name == deliverOrderToolName && len(payload.PendingDeliveriesFromMe) == 0 {
			continue
		}
		// stay_open consumer (LLM-66): advertise only on the off-shift wind-down
		// cue's own OfferStayOpen signal — the same field the stay-open prose
		// renders from — so the tool and its cue can't drift (discussion-109).
		if spec.Name == stayOpenToolName && !offerStayOpen {
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
