package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// production_cycle_say_test.go — LLM-468. StartProductionCycle carries an
// optional `say`, spoken to the room as the batch goes on, so a producer can
// voice its beat on the acting call instead of buying a whole extra LLM round
// for `speak`. Best-effort by design: the batch is the point, the word is the
// garnish, and a refused or unheard utterance never fails the start.

// huddleTheCook puts the cook in a conversation WITH a listener, so an
// announcement has somewhere to land. Both halves matter: produce (unlike bake)
// never creates or leaves a huddle, and SpeakTo refuses an utterance with no one
// to hear it ("there is no one here to hear you"), so a lone member is not an
// audience.
func huddleTheCook(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		// buildCookWorld seeds no structures (its tests never needed one); the
		// huddle machinery resolves membership by structure, so add the tavern.
		world.Structures["tavern"] = &sim.Structure{ID: "tavern", DisplayName: "The Tavern"}
		world.Actors["patron"] = &sim.Actor{
			ID:                "patron",
			DisplayName:       "Patron",
			Kind:              sim.KindNPCShared,
			InsideStructureID: "tavern",
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed patron: %v", err)
	}
	// Join through the command, not by writing Huddles directly: the speak
	// audience is resolved off the world's actorsByHuddle index, which only
	// JoinHuddle maintains. A hand-built Huddle.Members leaves that index empty
	// and every utterance is refused as "no one here to hear you".
	now := time.Unix(0, 0).UTC()
	for _, id := range []sim.ActorID{"cook", "patron"} {
		if _, err := w.Send(sim.JoinHuddle(id, "tavern", "", now)); err != nil {
			t.Fatalf("JoinHuddle(%s): %v", id, err)
		}
	}
}

func TestStartProductionCycle_SayReachesTheRoom(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()
	huddleTheCook(t, w)

	res, err := w.Send(sim.StartProductionCycle("cook", "stew", "I'll get a pot of stew on", false))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	r := res.(sim.ProductionStartResult)
	if !r.Spoke {
		t.Errorf("a say delivered to a huddle must report Spoke=true — the harness reads it to end the tick")
	}
	// The batch is still real: the say rides the start, it does not replace it.
	if r.Item != "stew" || r.BatchQty != 1 {
		t.Errorf("result = %+v, want the stew batch still opened", r)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		h := world.Huddles[world.Actors["cook"].CurrentHuddleID]
		if len(h.RecentUtterances) != 1 {
			t.Errorf("huddle utterances = %d, want the announcement recorded", len(h.RecentUtterances))
		} else if h.RecentUtterances[0].Text != "I'll get a pot of stew on" {
			t.Errorf("utterance = %q, want the announcement verbatim", h.RecentUtterances[0].Text)
		}
		if world.Actors["cook"].ProductionActivity == nil {
			t.Errorf("the production cycle must be open after a spoken start")
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("inspect world: %v", err)
	}
}

func TestStartProductionCycle_SilentStartReportsNoSpeech(t *testing.T) {
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()
	huddleTheCook(t, w)

	res, err := w.Send(sim.StartProductionCycle("cook", "stew", "", false))
	if err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if res.(sim.ProductionStartResult).Spoke {
		t.Errorf("an empty say must report Spoke=false so the tick stays open")
	}
}

func TestStartProductionCycle_SayToEmptyRoomIsDropped(t *testing.T) {
	// An unhuddled producer has no one to announce to. The utterance is dropped
	// rather than failing the start — and Spoke stays false, so the tick stays
	// open and the actor can still go find someone.
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()

	res, err := w.Send(sim.StartProductionCycle("cook", "stew", "I'll get a pot of stew on", false))
	if err != nil {
		t.Fatalf("a say with no audience must not fail the start: %v", err)
	}
	r := res.(sim.ProductionStartResult)
	if r.Spoke {
		t.Errorf("an announcement to an empty room must report Spoke=false")
	}
	if r.Item != "stew" {
		t.Errorf("result = %+v, want the stew batch opened anyway", r)
	}
}

func TestStartProductionCycle_RefusedSayLeavesTheCycleOpen(t *testing.T) {
	// The refusal path that survives having a huddle: the actor IS in a
	// conversation, but is its only member, so SpeakTo rejects the utterance
	// ("there is no one here to hear you"). Distinct from the no-huddle case
	// above, and the one that proves the error is swallowed rather than unwinding
	// the batch (code_review). The cycle must stand and Spoke must be false, so
	// the tick stays open.
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 3})
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Structures["tavern"] = &sim.Structure{ID: "tavern", DisplayName: "The Tavern"}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed structure: %v", err)
	}
	if _, err := w.Send(sim.JoinHuddle("cook", "tavern", "", time.Unix(0, 0).UTC())); err != nil {
		t.Fatalf("JoinHuddle: %v", err)
	}

	res, err := w.Send(sim.StartProductionCycle("cook", "stew", "I'll get a pot of stew on", false))
	if err != nil {
		t.Fatalf("a refused say must not fail the start: %v", err)
	}
	r := res.(sim.ProductionStartResult)
	if r.Spoke {
		t.Errorf("a refused utterance must report Spoke=false so the tick stays open")
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if world.Actors["cook"].ProductionActivity == nil {
			t.Errorf("the production cycle must remain open after a refused say")
		}
		if world.Actors["cook"].Inventory["sage"] != 1 {
			t.Errorf("sage = %d, want 1 — the inputs stay spent, the batch is real",
				world.Actors["cook"].Inventory["sage"])
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("inspect world: %v", err)
	}
}

func TestStartProductionCycle_SayDoesNotRescueARejectedStart(t *testing.T) {
	// The say must not be spoken when the batch never opens — otherwise the room
	// hears "I'll get a pot of stew on" from someone who did not.
	w, cancel := buildCookWorld(t, stewWaterRecipes(), stewWaterRestock(), map[sim.ItemKind]int{"sage": 0})
	defer cancel()
	huddleTheCook(t, w)

	if _, err := w.Send(sim.StartProductionCycle("cook", "stew", "I'll get a pot of stew on", false)); err == nil {
		t.Fatalf("expected a rejection with no sage on hand")
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if n := len(world.Huddles[world.Actors["cook"].CurrentHuddleID].RecentUtterances); n != 0 {
			t.Errorf("a rejected start must speak nothing, got %d utterances", n)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("inspect world: %v", err)
	}
}
