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

// laborResponseTools are the employer-side labor-decision tools advertised ONLY
// when the actor's perception carries a pending labor offer staked against them
// (perception.PendingLaborOffers — the standing LaborLedger view). The labor
// analog of payOfferResponseTools; same advertising-only posture (the tools
// stay AvailabilityAvailable so a call that arrives is still dispatchable, and
// sim.AcceptWork/DeclineWork stay authoritative). LLM-26.
var laborResponseTools = map[string]struct{}{
	"accept_work":  {},
	"decline_work": {},
}

// giftResponseTools are the recipient-side gift-decision tools advertised ONLY
// when the actor's perception carries a pending gift offered to them
// (perception.PendingGiftsForMe — the standing IsGift ledger view). The gift
// analog of payOfferResponseTools; same advertising-only posture (the tools
// stay AvailabilityAvailable so a call that arrives is still dispatchable, and
// the reused sim.AcceptPay / sim.DeclinePay stay authoritative). LLM-138.
var giftResponseTools = map[string]struct{}{
	"accept_gift":  {},
	"decline_gift": {},
}

// solicitWorkToolName — the worker's offer-my-labor tool. Advertised ONLY when
// perception offers it (payload.CanSolicitWork: a free AttrWorker carrier with
// an audience). Reading the SAME signal the solicit_work affordance cue renders
// from keeps the tool and its cue from drifting (discussion-109). Also dropped
// while the actor is walking (walkIncompatibleTools — SolicitWork rejects on
// MoveIntent != nil). LLM-26.
const solicitWorkToolName = "solicit_work"

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
// pay_with_item (pay_with_item_commands.go), and bare pay (pay_commands.go,
// which rejects on MoveIntent != nil).
var walkIncompatibleTools = map[string]struct{}{
	"consume":       {},
	"speak":         {},
	"gather":        {},
	"pay_with_item": {},
	"offer_trade":   {}, // ZBBS-HOME-407: same substrate as pay_with_item (walk-in-flight reject)
	"give":          {}, // LLM-138: GiveItems rejects on MoveIntent != nil (offer the gift when stationary)
	"pay":           {}, // LLM-99: bare-coin pay re-registered; same walk-in-flight reject
	"repair":        {}, // LLM-118: StartRepair rejects on MoveIntent != nil (mend at the stall)
	"solicit_work":  {}, // LLM-26: SolicitWork rejects on MoveIntent != nil (offer when stationary)
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

// takeBreakToolName — the keeper's rest-at-post tool. Advertised ONLY when the
// recovery cue offers in-place rest: payload.RecoveryOptions.RestInPlace, set in
// perception build on tired + at-own-post + on-shift (recovery_options.go). Reading
// the SAME field the "Close up and rest where you are — call take_break" prose
// renders from keeps the tool and its cue from drifting — the discussion-109
// "advertise a tool only with its triggering perception" invariant. Before LLM-100
// take_break had no gate and fell through to every actor every tick, so an
// off-shift wanderer standing in its own closed shop was both told to rest in place
// and handed the tool.
const takeBreakToolName = "take_break"

// speakToolName — the conversation tool. Advertised ONLY when the actor has an
// awake, addressable audience (payload.Surroundings.HasAudience() — its huddle
// peers, or co-present actors within earshot). The substrate already rejects a
// speak with no listener ("there is no one here to hear you"), so a lone actor
// handed speak just burns a turn on a doomed greeting — the live Josiah Thorne
// case (LLM-106): alone in his shop, advertised speak, he greeted an empty room
// and the call was rejected at dispatch. Gating off the SAME audience set the
// co-presence line and the dispatch gate read keeps cue, tool, and substrate
// aligned (discussion-109). A walk-in customer lands in CoPresent, so the keeper
// can still greet a newcomer to open a conversation.
const speakToolName = "speak"

// moveToToolName — the locomotion tool. Dropped from a degeneracy-flagged
// actor's advertised set (LLM-94 Stage-1): a flagged actor is in a sustained
// futile loop whose live signature is move_to rejected every tick. Perception
// build already thinned the steering cues that name the unreachable target
// (perception.thinDegenerateSteer); gating the tool removes the matching
// affordance so a lingering place reference plus an advertised move tool can't
// re-drive the walk. Advertising-only, like every other gate here — the
// substrate stays authoritative, and the gate lifts the moment the flag clears.
const moveToToolName = "move_to"

// craftToolName — the model-facing name of the multi-output producer's production-
// choice tool (LLM-116). The internal identifiers keep the "craft" codename to
// avoid colliding with the produce_tick auto-fill machinery (ProduceState etc.).
// Advertised ONLY when the "## Time to produce" cue is present (payload.ForgeChoice
// non-empty), which itself fires only for a >1-produce-entry crafter AT its
// workplace. Reading the SAME signal the cue renders from keeps the tool and its
// cue in lockstep — the discussion-109 "advertise a tool only with its triggering
// perception" invariant. A single-output producer never sees it; the
// sim.SetProductionFocus Command stays the authoritative gate for any call that
// arrives anyway.
const craftToolName = "produce"

// repairToolName — the stall owner's "mend your worn market stall" tool (LLM-118).
// Advertised ONLY when the "## Your stall" cue is present (payload.StallRepair
// non-nil), which itself fires only when the owner stands at their own stall and
// it is mendable (worn to the repair threshold, or degraded). Reading the SAME
// signal the cue renders from keeps the tool and its cue in lockstep (the
// discussion-109 invariant). Deliberately NOT gated on carrying enough nails: the
// affordance stays visible so the model knows mending is the way out, the cue
// steers buy-then-mend when short, and StartRepair errors helpfully if called
// without nails. The sim.StartRepair Command stays the authoritative gate.
const repairToolName = "repair"

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

// actorIsFlaggedDegenerate reports whether the degeneracy observer (LLM-94) has
// the acting actor at Stage 1 or higher (sim.DegeneracyFlagged / …Throttled),
// read off the snapshot projection. The observer is OFF by default, so this is
// false for every actor unless an operator enabled it and the actor sustained a
// futile streak. False when the actor can't be resolved — conservative: don't
// strip locomotion from an actor we can't confirm is stuck.
func actorIsFlaggedDegenerate(actorID sim.ActorID, snap *sim.Snapshot) bool {
	if snap == nil {
		return false
	}
	a, ok := snap.Actors[actorID]
	if !ok || a == nil {
		return false
	}
	return a.DegenStage >= sim.DegeneracyFlagged
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
	offerRestInPlace := payload.RecoveryOptions != nil && payload.RecoveryOptions.RestInPlace
	hasAudience := payload.Surroundings.HasAudience()
	flaggedDegenerate := actorIsFlaggedDegenerate(payload.ActorID, snap)
	offerCraft := payload.ForgeChoice != nil && len(payload.ForgeChoice.Items) > 0
	offerRepair := payload.StallRepair != nil
	hasLaborOffer := len(perception.PendingLaborOffers(payload)) > 0
	canSolicitWork := payload.CanSolicitWork
	hasGift := len(perception.PendingGiftsForMe(payload)) > 0

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
		// take_break consumer (LLM-100): advertise only on the recovery cue's own
		// RestInPlace signal — the same field the "rest where you are" prose renders
		// from — so the tool and its cue can't drift (discussion-109). Keeps
		// take_break out of the prompt for an off-shift actor with no shift to step
		// away from (the LLM-100 phantom-take_break case).
		if spec.Name == takeBreakToolName && !offerRestInPlace {
			continue
		}
		// speak consumer (LLM-106): advertise only when there's an awake audience to
		// address. The dispatch gate already rejects a no-listener speak, so a lone
		// actor offered speak just wastes a turn greeting no one (the Josiah empty-
		// room case). Same audience set as the co-presence cue → cue and tool can't
		// drift; a co-present walk-in re-enables it so a keeper can greet a newcomer.
		if spec.Name == speakToolName && !hasAudience {
			continue
		}
		// degeneracy Stage-1 gate (LLM-94): drop move_to from a flagged actor's
		// set, in lockstep with the steering cues perception build thinned for
		// the same actor. Removes the futile-walk affordance until a productive
		// tick clears the flag.
		if spec.Name == moveToToolName && flaggedDegenerate {
			continue
		}
		// craft consumer (LLM-116): advertise only when the "## Time to produce" cue
		// is present — the same ForgeChoice signal the cue renders from — so a
		// crafter is handed the tool exactly when it has a choice to make, and no
		// other actor ever sees it.
		if spec.Name == craftToolName && !offerCraft {
			continue
		}
		// repair consumer (LLM-118): advertise only when the "## Your stall" cue
		// is present — the same StallRepair signal the cue renders from — so the
		// owner is handed the tool exactly when standing at their own worn stall,
		// and no other actor ever sees it.
		if spec.Name == repairToolName && !offerRepair {
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
		// solicit_work consumer (LLM-26): advertise only to a free worker with an
		// audience — the same CanSolicitWork signal the affordance cue renders
		// from — so the tool and its cue can't drift, and no non-worker ever sees
		// it.
		if spec.Name == solicitWorkToolName && !canSolicitWork {
			continue
		}
		// labor-response consumer (LLM-26): advertise accept_work/decline_work
		// only to an employer with a pending labor offer (PendingLaborOffers, the
		// same standing view the decision section renders the labor_id from), so
		// the tools appear exactly when there's an offer to answer and never as
		// noise + a labor_id-hallucination vector.
		if _, gated := laborResponseTools[spec.Name]; gated && !hasLaborOffer {
			continue
		}
		// gift-response consumer (LLM-138): advertise accept_gift/decline_gift only
		// to a recipient with a pending gift (PendingGiftsForMe, the same standing
		// view the "## Gifts offered to you" section renders the ledger_id from), so
		// the tools appear exactly when there's a gift to answer and never as noise.
		if _, gated := giftResponseTools[spec.Name]; gated && !hasGift {
			continue
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
