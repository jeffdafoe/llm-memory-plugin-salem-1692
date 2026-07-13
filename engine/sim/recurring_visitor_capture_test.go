package sim_test

// Episodic-memory capture tests (LLM-383). A returner→PC interaction beat routes
// through RecordInteraction into the pair's recurring_visitor_acquaintance instead
// of the actor-relationship skip. These drive the real chokepoint (sim.Speak /
// sim.Pay / … all funnel through RecordInteraction) and pin two properties:
//   - the self-talk guard (the returner's own utterance is excluded), and
//   - the STRUCTURAL cross-attribution guard (a co-present third party can't leak
//     into the pair's facts, because each beat is keyed to exactly one pair).

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// recordInteraction drives one beat through the real RecordInteraction chokepoint.
func recordInteraction(t *testing.T, w *sim.World, actor, other sim.ActorID, kind sim.InteractionKind, text string, at time.Time) {
	t.Helper()
	if _, err := w.Send(sim.RecordInteraction(actor, other, kind, text, at)); err != nil {
		t.Fatalf("RecordInteraction %s→%s: %v", actor, other, err)
	}
}

// returnerAcqFacts reads a copy of the (returner, pc) acquaintance's SalientFacts.
func returnerAcqFacts(t *testing.T, w *sim.World, rid string, pc sim.ActorID) []sim.SalientFact {
	t.Helper()
	var facts []sim.SalientFact
	withWorld(t, w, func(world *sim.World) {
		rv := world.RecurringVisitors[sim.RecurringVisitorID(rid)]
		if rv == nil {
			return
		}
		if acq := rv.Acquaintances[pc]; acq != nil {
			facts = append([]sim.SalientFact(nil), acq.SalientFacts...)
		}
	})
	return facts
}

// TestReturner_CaptureHeardAndTransactional — a returner→PC beat records episodic
// memory: what the returner HEARD the PC say, and factual transactional beats, are
// kept; the returner's OWN utterance (InteractionSpoke) is excluded (self-talk
// guard — the 2026-06-03 Item-A failure mode).
func TestReturner_CaptureHeardAndTransactional(t *testing.T) {
	w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", ""), nil)
	defer stop()

	at := time.Now().UTC()
	emitInCommand(t, w, &sim.ActorMet{A: "vstr-0000aaaa", B: "pc-jeff", At: at})

	// Heard (the PC's words) — kept.
	recordInteraction(t, w, "vstr-0000aaaa", "pc-jeff", sim.InteractionHeard, "The fence along the north field won't hold.", at.Add(1*time.Minute))
	// Spoke (the returner's own patter) — excluded.
	recordInteraction(t, w, "vstr-0000aaaa", "pc-jeff", sim.InteractionSpoke, "I've fine nails today.", at.Add(2*time.Minute))
	// Transactional (the PC paid the returner) — kept, attribution baked in the text.
	recordInteraction(t, w, "vstr-0000aaaa", "pc-jeff", sim.InteractionPaidBy, "Jeff paid me 6 coins for a bundle of nails.", at.Add(3*time.Minute))

	rid := w.Published().Actors["vstr-0000aaaa"].VisitorState.RecurringID
	facts := returnerAcqFacts(t, w, rid, "pc-jeff")
	if len(facts) != 2 {
		t.Fatalf("captured %d facts, want 2 (heard + paid_by; spoke excluded): %+v", len(facts), facts)
	}
	if facts[0].Kind != sim.InteractionHeard || facts[1].Kind != sim.InteractionPaidBy {
		t.Errorf("fact kinds = [%s %s], want [heard paid_by]", facts[0].Kind, facts[1].Kind)
	}
	for _, f := range facts {
		if f.Kind == sim.InteractionSpoke {
			t.Errorf("returner self-talk (spoke) leaked into episodic memory: %q", f.Text)
		}
	}
}

// TestReturner_CaptureScopedToPair — the structural cross-attribution guard: a
// co-present third party can't leak into the pair's facts. Each beat is keyed to
// exactly one (returner, PC) pair, so a second co-present PC's words land only in
// ITS acquaintance, and a co-present NPC's words are not captured at all (the arc
// is player-facing — other must be a PC).
func TestReturner_CaptureScopedToPair(t *testing.T) {
	w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", ""), nil)
	defer stop()

	// Add a second co-present PC (the helper seeds only pc-jeff + the NPC ezekiel).
	withWorld(t, w, func(world *sim.World) {
		world.Actors["pc-mary"] = &sim.Actor{
			ID: "pc-mary", DisplayName: "Mary", Kind: sim.KindPC, State: sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		}
	})

	at := time.Now().UTC()
	emitInCommand(t, w, &sim.ActorMet{A: "vstr-0000aaaa", B: "pc-jeff", At: at})
	emitInCommand(t, w, &sim.ActorMet{A: "vstr-0000aaaa", B: "pc-mary", At: at})

	// The returner hears each PC, and a co-present NPC (ezekiel) also speaks to it.
	recordInteraction(t, w, "vstr-0000aaaa", "pc-jeff", sim.InteractionHeard, "jeff-fact: the fence won't hold.", at.Add(time.Minute))
	recordInteraction(t, w, "vstr-0000aaaa", "pc-mary", sim.InteractionHeard, "mary-fact: the bread was stale.", at.Add(time.Minute))
	recordInteraction(t, w, "vstr-0000aaaa", "ezekiel", sim.InteractionHeard, "ezekiel-fact: idle NPC chatter.", at.Add(time.Minute))

	rid := w.Published().Actors["vstr-0000aaaa"].VisitorState.RecurringID

	jeff := returnerAcqFacts(t, w, rid, "pc-jeff")
	if len(jeff) != 1 || jeff[0].Text != "jeff-fact: the fence won't hold." {
		t.Errorf("pc-jeff facts = %+v, want exactly the jeff-fact (no leak from Mary/Ezekiel)", jeff)
	}
	mary := returnerAcqFacts(t, w, rid, "pc-mary")
	if len(mary) != 1 || mary[0].Text != "mary-fact: the bread was stale." {
		t.Errorf("pc-mary facts = %+v, want exactly the mary-fact", mary)
	}
	// The co-present NPC beat is not captured at all — no acquaintance minted for it.
	var ezekielAcqExists bool
	withWorld(t, w, func(world *sim.World) {
		if rv := world.RecurringVisitors[sim.RecurringVisitorID(rid)]; rv != nil {
			_, ezekielAcqExists = rv.Acquaintances["ezekiel"]
		}
	})
	if ezekielAcqExists {
		t.Error("a returner↔NPC beat created an acquaintance — the arc is player-facing (other must be a PC)")
	}
}
