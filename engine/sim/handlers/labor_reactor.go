package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_reactor.go — LLM-187. The LaborOfferReceived subscriber that turns a
// worker's solicit_work into a reactor warrant on the EMPLOYER, the labor
// analog of pay_with_item_reactor.go's handlePayOfferReceivedWarrants.
//
// Why it exists: solicit_work mints a pending LaborOffer (labor_commands.go)
// but the offer alone does not wake the employer. The decision section and the
// accept_work/decline_work tool gate are both correct — buildLaborOffersForMe
// scans the ledger and gateTools advertises the tools when a pending offer
// stands — but they only take effect on a tick the employer actually takes.
// Without a warrant the employer is woken only incidentally (e.g. the worker
// speaks again, firing the speech reactor), so a solicitation made into a lull
// goes unseen and expires at LaborLedgerTTLDefault (3 min). Live trace:
// hud-e8a494cc… — Anne Walker solicited twice, Prudence Ward never ticked
// during either window, accept_work never fired, the hire was wholly
// confabulated. events_labor.go documented this subscriber from the start
// ("a later subscriber stamps the employer's warrant"); it was never built.

// handleLaborOfferReceivedWarrants is the LaborOfferReceived subscriber. Stamps
// LaborOfferWarrantReason on the employer so their next reactor tick perceives
// the pending offer and decides accept_work / decline_work. The warrant's
// DedupDiscriminator is uint64(LaborID), so a duplicate registration (or a
// future restart re-stamp) dedupes cleanly against this stamp.
func handleLaborOfferReceivedWarrants(w *sim.World, evt sim.Event) {
	offer, ok := evt.(*sim.LaborOfferReceived)
	if !ok {
		return
	}
	if offer.EmployerID == "" {
		return
	}
	employer, ok := w.Actors[offer.EmployerID]
	if !ok || employer == nil {
		return
	}
	if employer.Kind != sim.KindNPCStateful && employer.Kind != sim.KindNPCShared {
		// PC or decorative employer. PCs don't deliberate via the reactor
		// (a player hiring acts through the UI surface), and a decorative
		// actor can't deliberate at all. Defensive skip, mirroring the pay
		// reactor's non-NPC-seller skip.
		return
	}

	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: offer.WorkerID,
		Force:          false,
		Reason: sim.LaborOfferWarrantReason{
			LaborID:     offer.LaborID,
			Worker:      offer.WorkerID,
			Reward:      offer.Reward,
			RewardItems: offer.RewardItems,
			DurationMin: offer.DurationMin,
			ExpiresAt:   offer.ExpiresAt,
		},
		SourceEventID: offer.EventID(),
		RootEventID:   offer.RootEventID(),
		SourceActorID: offer.WorkerID,
		HuddleID:      offer.HuddleID,
		SceneID:       offer.SceneID,
		OccurredAt:    offer.At,
	}
	if _, err := sim.StampWarrant(offer.EmployerID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: labor-reactor StampWarrant for employer %q (labor %d, event %d): %v",
			offer.EmployerID, offer.LaborID, offer.EventID(), err,
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
