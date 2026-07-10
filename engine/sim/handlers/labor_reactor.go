package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_reactor.go — LLM-187. The LaborOfferReceived subscriber that turns a
// labor offer into a reactor warrant on whoever must ANSWER it, the labor analog
// of pay_with_item_reactor.go's handlePayOfferReceivedWarrants.
//
// Why it exists: a mint inserts a pending LaborOffer (labor_commands.go) but the
// offer alone does not wake the other party. The decision section and the
// accept_work/decline_work tool gate are both correct — buildLaborOffersForMe
// scans the ledger and gateTools advertises the tools when a pending offer
// stands — but they only take effect on a tick that party actually takes.
// Without a warrant they are woken only incidentally (e.g. the other speaks
// again, firing the speech reactor), so an offer made into a lull goes unseen
// and expires at LaborLedgerTTLDefault (3 min). Live trace: hud-e8a494cc… —
// Anne Walker solicited twice, Prudence Ward never ticked during either window,
// accept_work never fired, the hire was wholly confabulated. events_labor.go
// documented this subscriber from the start ("a later subscriber stamps the
// employer's warrant"); it was never built.
//
// Since LLM-346 the offer may be minted from either side, so the warrant lands on
// the RESPONDER rather than always on the employer. The failure it guards against
// is identical in the new direction: Prudence asks Lewis to lend a hand, Lewis
// never ticks, and her offer expires against a worker standing at her door.

// handleLaborOfferReceivedWarrants is the LaborOfferReceived subscriber. Stamps
// LaborOfferWarrantReason on the RESPONDER — the party who did not mint the offer
// — so their next reactor tick perceives the pending offer and decides
// accept_work / decline_work. That is the employer on a solicit_work and the
// worker on an offer_work (LLM-346); the initiator needs no wake, they just
// acted. The warrant's DedupDiscriminator is uint64(LaborID), so a duplicate
// registration (or a future restart re-stamp) dedupes cleanly against this stamp.
func handleLaborOfferReceivedWarrants(w *sim.World, evt sim.Event) {
	offer, ok := evt.(*sim.LaborOfferReceived)
	if !ok {
		return
	}
	responderID := offer.Responder()
	initiatorID := offer.InitiatedBy
	if initiatorID == "" {
		initiatorID = offer.WorkerID // legacy solicit-shaped event
	}
	if responderID == "" {
		return
	}
	responder, ok := w.Actors[responderID]
	if !ok || responder == nil {
		return
	}
	if responder.Kind != sim.KindNPCStateful && responder.Kind != sim.KindNPCShared {
		// PC or decorative responder. PCs don't deliberate via the reactor
		// (a player hiring or taking a job acts through the UI surface), and a
		// decorative actor can't deliberate at all. Defensive skip, mirroring the
		// pay reactor's non-NPC-seller skip.
		return
	}

	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: initiatorID,
		Force:          false,
		Reason: sim.LaborOfferWarrantReason{
			LaborID:     offer.LaborID,
			Worker:      offer.WorkerID,
			Employer:    offer.EmployerID,
			InitiatedBy: initiatorID,
			Reward:      offer.Reward,
			RewardItems: offer.RewardItems,
			DurationMin: offer.DurationMin,
			ExpiresAt:   offer.ExpiresAt,
		},
		SourceEventID: offer.EventID(),
		RootEventID:   offer.RootEventID(),
		SourceActorID: initiatorID,
		HuddleID:      offer.HuddleID,
		SceneID:       offer.SceneID,
		OccurredAt:    offer.At,
	}
	if _, err := sim.StampWarrant(responderID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: labor-reactor StampWarrant for responder %q (labor %d, event %d): %v",
			responderID, offer.LaborID, offer.EventID(), err,
		)
	}
}

// RegisterLaborHandlers wires the labor event subscriber(s) into the world.
// Separate from RegisterLaborFamily (which registers the solicit_work /
// accept_work / decline_work TOOLS in the command registry) — this registers
// the world-event SUBSCRIBER, the same split as RegisterPayWithItemHandlers vs
// the pay command registration. Must run on the world goroutine — call before
// World.Run or from inside a Command.Fn.
//
// Idempotency: registering twice would invoke the subscriber twice per event,
// but tryStampWarrant's source-aware dedup catches the duplicate —
// (WarrantKindLaborOffer, LaborID) is the key, identical both times, so the
// second stamp is dropped at the open-cycle dedup gate.
func RegisterLaborHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterLaborHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleLaborOfferReceivedWarrants))
}
