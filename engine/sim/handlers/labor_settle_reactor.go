package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_settle_reactor.go — LLM-498. The LaborResolved(Completed) subscriber
// that surfaces a PAID labor settle to BOTH parties' next perception, the labor
// analog of production_cycle_reactor.go's completion beat.
//
// Why it exists: settleCompletedLabor transfers the reward at the completion
// sweep — mid-shift, with neither party's turn in flight — and until LLM-498
// nothing carried the outcome into perception. The salient facts it writes feed
// the consolidation layer, not the next turn; the only narration was the LLM-190
// shop-closed close-out, which fires solely when the employer is a keeper now
// off shift. Live trace (2026-07-19, Ellis Farm): the settle paid Abraham Warren
// in full, he then asked to "settle up", and Elizabeth Ellis — whose perception
// held ONLY his settle-up line — paid a second wage for the same job. Each side
// now gets a pre-rendered self-perception line (the ProductionDone posture):
// the worker reads the wage as received, the employer as already paid.
//
// The stiffed completion (FailedUnavailable with WorkPerformed=true) is NOT
// stamped here — that path already narrates through the LLM-165 aggrieved
// relationship facts, and "you've been paid, as agreed" must never render over
// an unpaid settle. Declined/Expired terminals never had a payment to report.

// handleLaborSettledWarrants is the LaborResolved subscriber. Fires only on the
// Completed terminal — the one terminal where the reward actually transferred —
// and stamps LaborSettledWarrantReason on worker and employer, each with its
// own side's narration (names + exact quantities verbatim). The warrant's
// DedupDiscriminator is uint64(LaborID), so each party gets at most one settle
// beat per job even under a double registration.
func handleLaborSettledWarrants(w *sim.World, evt sim.Event) {
	res, ok := evt.(*sim.LaborResolved)
	if !ok || res.TerminalState != sim.LaborTerminalStateCompleted {
		return
	}
	nameOf := func(id sim.ActorID) string {
		if a := w.Actors[id]; a != nil {
			return a.DisplayName
		}
		return ""
	}
	now := time.Now().UTC()
	stamp := func(partyID, counterpartyID sim.ActorID, narration string) {
		party, ok := w.Actors[partyID]
		if !ok || party == nil {
			return
		}
		if party.Kind != sim.KindNPCStateful && party.Kind != sim.KindNPCShared {
			// A PC carries its continuity through the player and a decorative
			// actor never takes a turn — the labor-offer reactor's skip.
			return
		}
		meta := sim.WarrantMeta{
			TriggerActorID: counterpartyID,
			Force:          false,
			Reason: sim.LaborSettledWarrantReason{
				LaborID:       res.LaborID,
				Counterparty:  counterpartyID,
				NarrationText: narration,
			},
			SourceEventID: res.EventID(),
			RootEventID:   res.RootEventID(),
			SourceActorID: counterpartyID,
			HuddleID:      res.HuddleID,
			SceneID:       res.SceneID,
			OccurredAt:    res.At,
		}
		if _, err := sim.StampWarrant(partyID, meta, now).Fn(w); err != nil {
			log.Printf(
				"handlers: labor-settle-reactor StampWarrant for %q (labor %d, event %d): %v",
				partyID, res.LaborID, res.EventID(), err,
			)
		}
	}
	stamp(res.WorkerID, res.EmployerID,
		sim.LaborSettledWorkerNarration(nameOf(res.EmployerID), res.Reward, res.RewardItems))
	stamp(res.EmployerID, res.WorkerID,
		sim.LaborSettledEmployerNarration(nameOf(res.WorkerID), res.Reward, res.RewardItems))
}
