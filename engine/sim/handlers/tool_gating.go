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
// withdraw_pay (buyer-side) is NOT in this seller-response set — it is the
// buyer retracting an offer THEY placed, gated separately on the buyer's own
// standing offers (see withdrawPayToolName / gateTools, LLM-322).
var payOfferResponseTools = map[string]struct{}{
	"accept_pay":  {},
	"decline_pay": {},
	"counter_pay": {},
}

// laborResponseTools are the labor-decision tools advertised ONLY when the actor's
// perception carries a pending labor offer AWAITING THEIR ANSWER
// (perception.PendingLaborOffers — the standing LaborLedger view). That is a
// worker's solicitation when the actor is the employer, and an employer's offer of
// work when the actor is the worker (LLM-346). The labor analog of
// payOfferResponseTools; same advertising-only posture (the tools stay
// AvailabilityAvailable so a call that arrives is still dispatchable, and
// sim.AcceptWork/DeclineWork stay authoritative). LLM-26.
//
// Note what is NOT here: no comfort gate. A seek-work coin ceiling silences a
// worker's HUSTLE (CanSolicitWork), never their answer to a direct question. These
// tools ride the standing offer view alone, so a worker who has been asked to lend
// a hand can always say yes or no — however full their purse, however late the
// hour, whatever the seek-work backstop thinks of them.
var laborResponseTools = map[string]struct{}{
	"accept_work":  {},
	"decline_work": {},
}

// laborAbandonTools are the commerce/trade tools stripped from a LABORING
// worker's advertised set (LLM-230): each would walk her off the job she
// committed to, and none serves a survival need (a starving worker eats via
// consume, not a trade). Leaving speak (+ consume/done) makes the reply a
// "can't stop just now, I'm minding the shelves," anchored by the standing
// renderLaborSelfState line, instead of silence or job abandonment. move_to is
// handled separately (it stays when a red hunger/thirst need needs walking to
// food — see gateTools). solicit_work / accept_work / the pay-offer group are
// already gated by their own conditions (a busy worker is not a free solicitor
// and holds no offer), so they need no entry here.
//
// offer_work IS listed (LLM-346). Its own gate would not stop a laboring worker
// from hiring a co-present peer — HireableWorkers only asks about the TARGET — and
// a hand mid-job subcontracting the shelves to a passer-by is exactly the kind of
// commerce this set exists to strip. The employer's own commitment, not the
// target's, is what disqualifies the call.
var laborAbandonTools = map[string]struct{}{
	"pay":           {},
	"pay_with_item": {},
	"offer_trade":   {},
	"sell":          {}, // the seller quote tool's model-facing name (scene_quote, renamed in LLM-184)
	"offer_work":    {}, // LLM-346: a worker mid-job does not take on hired help of her own
}

// payVerbTools are the buyer-initiated payment tools advertised ONLY when the
// actor has a co-present huddle peer to transact with (Surroundings.HuddleMembers
// non-empty). Both hard-require CurrentHuddleID != "" at the substrate — sim.Pay
// resolves the recipient among huddle peers, and sim.PayWithItem (the barter/offer
// slow path AND the quote fast-path) rejects a non-huddled buyer — so an actor
// with no huddle peer storms a doomed call up to the per-tick iteration cap
// (→ budget_forced) and, carrying no memory of the failure, re-storms it next tick
// (LLM-329: Hannah Boggs fired pay_with_item at an absent seller 23× / 4 min while
// cued to restock but not co-present with any seller). HuddleMembers is the huddle
// subset of the speak gate's audience set — a not-yet-huddled walk-in (CoPresent)
// can be greeted but not paid until a conversation forms — and the same
// co-presence the restock/satiation buy cues read, so tool and cue can't drift
// (the discussion-109 invariant). A necessary-condition gate: no huddle peer means
// a guaranteed substrate reject, so there are no false drops. Advertising-only:
// the tools stay AvailabilityAvailable and sim.Pay / sim.PayWithItem stay
// authoritative for any call that arrives.
var payVerbTools = map[string]struct{}{
	"pay":           {},
	"pay_with_item": {},
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

// offerWorkToolName — the employer's hire-someone tool (LLM-346). Advertised ONLY
// when perception names at least one co-present hireable worker
// (payload.HireableWorkers), the SAME slice renderOfferWorkAffordance names them
// from — so the tool and its cue surface together or not at all (discussion-109),
// and the model is never handed a hiring verb with no one in the room to hire.
// Also dropped while the actor is walking (walkIncompatibleTools — OfferWork
// rejects on MoveIntent != nil) and while the actor is laboring
// (laborAbandonTools).
const offerWorkToolName = "offer_work"

// counterPayToolName is the seller's counter tool — gated more tightly than
// the rest of the seller-response group (see gateTools / scar #4).
const counterPayToolName = "counter_pay"

// withdrawPayToolName is the buyer's retract-my-offer tool. Advertised ONLY
// when the buyer holds an own still-pending pay-with-item offer to withdraw —
// payload.PendingOffersFromMe, the per-tick PayLedger scan that also drives the
// "## Offers you have standing" cue (buildPendingOffersFromMe, ZBBS-HOME-413).
// Reading the SAME view the cue renders from keeps the tool and its cue from
// drifting (the discussion-109 invariant), mirroring the seller-side
// accept/decline/counter gate. Before LLM-322 it was advertised to every actor
// every tick, because this buyer-side standing view did not yet exist.
const withdrawPayToolName = "withdraw_pay"

// summonToolName is the messenger-errand tool (send a courier to fetch someone,
// ZBBS-HOME-311). Advertised to every actor again as of LLM-323: a messenger is
// provisioned in the live village (a non-VA NPC carrying AttrMessenger), target
// name resolution works (DispatchSummon resolves a display name → actor id), and
// the summon_point is reachable whether or not it backs a structure. The LLM-322
// advertising gate that dropped it — a dead affordance while no messenger was
// thought to exist — is removed. Registered/dispatchable via register_summon.go.
const summonToolName = "summon"

// recallToolName / memorizeToolName are the two memory observation tools. Both
// are advertised to any actor with a private memory partition — every NPC,
// stateful or shared-VA (LLM-356). Before LLM-356 recall was gated to
// dedicated-VA NPCs only, because a shared-VA NPC had no per-NPC memory to
// search; sectioning shared-VA memory by slug prefix removed that limit.
const (
	recallToolName   = "recall"
	memorizeToolName = "memorize"
)

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
	"stoke":         {}, // LLM-412: StartStoke rejects on MoveIntent != nil (tend the fire on site)
	"solicit_work":  {}, // LLM-26: SolicitWork rejects on MoveIntent != nil (offer when stationary)
	"offer_work":    {}, // LLM-346: OfferWork rejects on MoveIntent != nil (hire when stationary)
}

// stopToolName — the voluntary-halt tool (ZBBS-HOME-338). The inverse of the
// walking gate: advertised ONLY while the actor is moving (a stationary actor
// has nothing to stop). It is the escape hatch that lets a walking NPC abandon
// a route so the walkIncompatibleTools become usable next tick.
const stopToolName = "stop"

// deliverOrderToolName — the seller's order check-in/handover tool. Advertised
// ONLY when the keeper has a Ready order that is deliverable RIGHT NOW — the good
// on hand and the recipient co-present (OrderView.DeliverableNow). Ready orders
// are lodging check-ins (ZBBS-HOME-398) and made-to-order commissions (LLM-338);
// an unforged commission (AwaitingMake) or an order whose recipient stepped away
// would bounce DeliverOrder's gate 5 / gate 6, so the tool is withheld until it
// can actually be used — matching the "## Orders to deliver" instruction, which
// reads the same DeliverableNow predicate. This is the discussion-109 "advertise
// a tool only with its triggering perception" invariant: tool and cue surface
// together or not at all.
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

// craftToolName — the model-facing name of the start-one-batch production tool
// (LLM-116, one-shot semantics since LLM-319). The internal identifiers keep
// the "craft" codename for file/handler continuity. Advertised ONLY when the
// "## Your trade" cue is present (payload.ForgeChoice non-empty), which itself
// fires for ANY producer AT its workplace with nothing already in the works —
// so the tool disappears mid-batch along with the cue. Reading the SAME signal
// the cue renders from keeps the tool and its cue in lockstep — the
// discussion-109 "advertise a tool only with its triggering perception"
// invariant. The sim.StartProductionCycle Command stays the authoritative gate
// for any call that arrives anyway.
const craftToolName = "produce"

// repairToolName — the business owner's "mend your worn premises" tool (LLM-118,
// generalized LLM-247). Advertised ONLY when the "## Your business" cue is present
// (payload.StallRepair non-nil), which itself fires only when the owner stands at
// their own business and it is mendable (worn to the repair threshold, or
// degraded). Reading the SAME
// signal the cue renders from keeps the tool and its cue in lockstep (the
// discussion-109 invariant). Deliberately NOT gated on carrying enough nails: the
// affordance stays visible so the model knows mending is the way out, the cue
// steers buy-then-mend when short, and StartRepair errors helpfully if called
// without nails. The sim.StartRepair Command stays the authoritative gate.
const repairToolName = "repair"

// stokeToolName — the hearth keeper's "feed the fire" tool (LLM-412).
// Advertised ONLY when the hearth cue is present (payload.Hearth non-nil),
// which itself fires only when the actor is responsible for the hearth
// (owner, or Working a hired job for its owner), stands inside its structure,
// and the fire is out or low. Deliberately NOT in laborAbandonTools: a hired
// hand stoking the employer's fire is doing the job, not leaving it — the
// work-vs-leaving principle. Like repair, not gated on carrying enough
// firewood; sim.StartStoke stays the authoritative gate.
const stokeToolName = "stoke"

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

// actorHasMemory reports whether the acting actor has a private memory
// partition — i.e. whether the memory tools (recall / memorize) apply to it.
// True for both stateful NPCs (own namespace) and shared-VA NPCs (sectioned by
// slug prefix inside a pooled namespace); false for PCs, decoratives, and a
// shared-VA actor whose name won't slugify. Derives from sim.MemoryPartition,
// the single source of truth the memory tools themselves use, so what's
// advertised can't drift from what's searchable/writable (LLM-356). Returns
// false when the actor can't be resolved — conservative: don't advertise a
// memory tool to an actor we can't confirm has memory.
func actorHasMemory(actorID sim.ActorID, snap *sim.Snapshot) bool {
	if snap == nil {
		return false
	}
	a, ok := snap.Actors[actorID]
	if !ok || a == nil {
		return false
	}
	_, hasMemory := sim.MemoryPartition(a.Kind, a.DisplayName)
	return hasMemory
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

// laboringMayBreakOffToEat reports whether the subject has a red-tier hunger or
// thirst need — the one situation in which a laboring worker keeps move_to, so
// she can walk off to eat/drink (LLM-230). It mirrors the reactor's
// hasBreakInterruptingNeedWarrant carve-out that ticks her in the first place:
// tiredness is excluded on purpose (a break cures tiredness, so it never
// justifies abandoning the job, and the shift-end clamp keeps a job from running
// into the worker's own bedtime). Reads the payload's own ActorView needs, the
// same values render classifies into felt tiers, so tool and cue can't drift.
func laboringMayBreakOffToEat(a perception.ActorView) bool {
	for need, level := range a.Needs {
		if need == "tiredness" {
			// Non-tiredness only, in lockstep with the reactor's
			// hasBreakInterruptingNeedWarrant (a break cures tiredness, so it never
			// justifies leaving the job). If a need is ever added, update BOTH
			// predicates together — the tool surface must agree with the reactor tick
			// that woke her, or she'd tick for a need but be denied move_to (or vice
			// versa).
			continue
		}
		// >= NeedRed catches both red and the maxed-out NeedPeak tier (the same
		// idiom shift_duty.go uses); == NeedRed would miss a need pinned at NeedMax.
		if sim.NeedLabelTier(level, a.NeedThresholds.Get(need)) >= sim.NeedRed {
			return true
		}
	}
	return false
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
// recall (ZBBS-WORK-321) is the first consumer to use snap: with memorize
// (LLM-356) it advertises the memory tools only to actors with a private memory
// partition (actorHasMemory → sim.MemoryPartition). snap remains the
// channel for future consumers needing world state the warrant batch doesn't
// carry (e.g. shift state for speak gating); the pay consumer reads only the
// payload.
// hasDeliverableOrder reports whether the keeper holds at least one Ready order
// that can be handed over this tick (OrderView.DeliverableNow: good on hand +
// recipient co-present). Drives the deliver_order advertising gate off the SAME
// predicate the "## Orders to deliver" instruction uses, so tool and cue stay in
// lockstep (LLM-338).
func hasDeliverableOrder(orders []perception.OrderView) bool {
	for _, o := range orders {
		if o.DeliverableNow() {
			return true
		}
	}
	return false
}

func gateTools(r *Registry, payload perception.Payload, snap *sim.Snapshot) []llm.ToolSpec {
	all := r.AdvertisedSpecs()
	offers := perception.PendingPayOffers(payload)
	hasPayOffer := len(offers) > 0
	canCounter := anyOfferCounterable(offers)
	hasOwnPendingOffer := len(payload.PendingOffersFromMe) > 0
	hasMemory := actorHasMemory(payload.ActorID, snap)
	atGatherableSource := payload.Surroundings.GatherableItem != ""
	moving := actorIsMoving(payload.ActorID, snap)
	offerStayOpen := payload.DutySteer != nil && payload.DutySteer.OfferStayOpen
	offerTakeBreak := payload.RecoveryOptions.OffersTakeBreak()
	hasAudience := payload.Surroundings.HasAudience()
	// hasHuddlePeer gates the pay verbs (LLM-329): both require the actor to be in
	// a huddle at the substrate, and HuddleMembers is populated only then. The
	// huddle subset of hasAudience — a not-yet-huddled CoPresent walk-in enables
	// speak but not pay (start the conversation first).
	hasHuddlePeer := len(payload.Surroundings.HuddleMembers) > 0
	flaggedDegenerate := actorIsFlaggedDegenerate(payload.ActorID, snap)
	offerCraft := payload.ForgeChoice != nil && len(payload.ForgeChoice.Items) > 0
	offerRepair := payload.StallRepair != nil
	offerStoke := payload.Hearth != nil
	hasLaborOffer := len(perception.PendingLaborOffers(payload)) > 0
	canSolicitWork := payload.CanSolicitWork
	canOfferWork := len(payload.HireableWorkers) > 0
	hasGift := len(perception.PendingGiftsForMe(payload)) > 0
	laboring := payload.Laboring != nil
	// LLM-230 strips a laboring worker's move_to to keep her committed, EXCEPT when
	// she has a red hunger/thirst need (reach food), OR (LLM-268) she has wandered
	// off the post (walk back) or her employer has left it (follow along). The
	// off-post/employer-away flags come from her own LaboringView — the same
	// predicate renderLaborSelfState renders the head-back / accompany cue from, so
	// the tool and its cue can't drift.
	laboringMayMove := laboring && (laboringMayBreakOffToEat(payload.Actor) ||
		payload.Laboring.OffPost || payload.Laboring.EmployerAway)

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
		// memory consumers (LLM-356): advertise recall + memorize only to
		// actors with a private memory partition (every NPC — stateful or
		// shared-VA). PCs and decoratives have none, so both tools are dropped.
		if (spec.Name == recallToolName || spec.Name == memorizeToolName) && !hasMemory {
			continue
		}
		// gather consumer (ZBBS-WORK-328): advertise only when loitering at a
		// gatherable source — keeps the tool out of the prompt everywhere else.
		if spec.Name == gatherToolName && !atGatherableSource {
			continue
		}
		// deliver_order consumer (ZBBS-HOME-398, LLM-338): advertise only to a
		// keeper who has a Ready order deliverable RIGHT NOW — the good on hand and
		// the recipient co-present (OrderView.DeliverableNow). An unforged
		// commission (AwaitingMake) or an absent-recipient order would bounce
		// DeliverOrder's gate 5 / gate 6, so the tool stays out of the prompt until
		// it can be used. DeliverableNow is the SAME predicate the "## Orders to
		// deliver" instruction reads, so the tool and its cue can't drift.
		if spec.Name == deliverOrderToolName && !hasDeliverableOrder(payload.PendingDeliveriesFromMe) {
			continue
		}
		// stay_open consumer (LLM-66): advertise only on the off-shift wind-down
		// cue's own OfferStayOpen signal — the same field the stay-open prose
		// renders from — so the tool and its cue can't drift (discussion-109).
		if spec.Name == stayOpenToolName && !offerStayOpen {
			continue
		}
		// take_break consumer (LLM-100 + LLM-214): advertise only on the recovery
		// cue's own in-place-rest signal — OffersTakeBreak = RestInPlace (tired at
		// own post, on shift) OR RestAtHome (tired inside own home) — the same signal
		// the in-place-rest prose renders from, so the tool and its cue can't drift
		// (discussion-109). Keeps take_break out of the prompt for an actor away from
		// any post/bed with no shift to step away from (the LLM-100 phantom case).
		if spec.Name == takeBreakToolName && !offerTakeBreak {
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
		// pay-verb consumer (LLM-329): advertise pay / pay_with_item only when the
		// actor has a co-present huddle peer — see payVerbTools for the rationale.
		if _, gated := payVerbTools[spec.Name]; gated && !hasHuddlePeer {
			continue
		}
		// degeneracy Stage-1 gate (LLM-94): drop move_to from a flagged actor's
		// set, in lockstep with the steering cues perception build thinned for
		// the same actor. Removes the futile-walk affordance until a productive
		// tick clears the flag.
		if spec.Name == moveToToolName && flaggedDegenerate {
			continue
		}
		// produce consumer (LLM-116/LLM-319): advertise only when the "## Your
		// trade" cue is present — the same ForgeChoice signal the cue renders
		// from — so a producer is handed the tool exactly when idle at its post
		// with a batch to consider, never mid-batch, and no other actor ever
		// sees it.
		if spec.Name == craftToolName && !offerCraft {
			continue
		}
		// repair consumer (LLM-118): advertise only when the "## Your business" cue
		// is present — the same StallRepair signal the cue renders from — so the
		// owner is handed the tool exactly when standing at their own worn business,
		// and no other actor ever sees it.
		if spec.Name == repairToolName && !offerRepair {
			continue
		}
		// stoke consumer (LLM-412): advertise only when the hearth cue is present
		// (payload.Hearth non-nil: responsible for the hearth, inside its structure,
		// fire out/low) — the same signal the cue renders from, so tool and cue
		// can't drift. Like repair, NOT gated on carrying enough firewood: the cue
		// steers buy-then-stoke when short, and sim.StartStoke errors helpfully.
		if spec.Name == stokeToolName && !offerStoke {
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
		// withdraw_pay consumer (LLM-322): advertise only when the buyer holds an
		// own still-pending offer to retract (payload.PendingOffersFromMe — the
		// same standing view the "## Offers you have standing" cue renders from).
		// No own pending offer → nothing to withdraw.
		if spec.Name == withdrawPayToolName && !hasOwnPendingOffer {
			continue
		}
		// solicit_work consumer (LLM-26): advertise only to a free worker with an
		// audience — the same CanSolicitWork signal the affordance cue renders
		// from — so the tool and its cue can't drift, and no non-worker ever sees
		// it.
		if spec.Name == solicitWorkToolName && !canSolicitWork {
			continue
		}
		// offer_work consumer (LLM-346): advertise only when perception names a
		// co-present worker this actor could hire — the same HireableWorkers slice
		// the affordance cue names them from, so the tool and its cue can't drift.
		if spec.Name == offerWorkToolName && !canOfferWork {
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
		// laboring speak-only surface (LLM-230): a worker mid-job stays committed.
		// Strip the commerce tools that would walk her off the job (laborAbandonTools),
		// and move_to too — EXCEPT the three laboringMayMove cases (LLM-268): a red
		// hunger/thirst need (reach food), being off the post (walk back), or the
		// employer having left it (follow along). speak / consume / done stay, so she
		// answers "can't stop just now" — or, when one of those holds, walks. The
		// commerce tools (laborAbandonTools) never serve any of those, so they go
		// unconditionally.
		if laboring {
			if _, gated := laborAbandonTools[spec.Name]; gated {
				continue
			}
			if spec.Name == moveToToolName && !laboringMayMove {
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
