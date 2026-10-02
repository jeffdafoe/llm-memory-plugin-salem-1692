package sim_test

import (
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedDepartVisitor places one shared-VA visitor in the world with the given phase
// and stay deadline, for the DepartVisitor tests (LLM-701).
func seedDepartVisitor(t *testing.T, w *sim.World, id sim.ActorID, phase sim.VisitorPhase, expiresAt time.Time) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id] = &sim.Actor{
			ID:          id,
			DisplayName: "Roger Standish the messenger",
			Kind:        sim.KindNPCShared,
			LLMAgent:    sim.VisitorAgentName,
			Pos:         sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			Needs:       sim.SeedVisitorNeedsForTest(),
			Inventory:   map[sim.ItemKind]int{},
			VisitorState: &sim.VisitorState{
				Archetype: "messenger",
				Origin:    "Boston",
				ExpiresAt: expiresAt,
				Phase:     phase,
			},
			State: sim.StateIdle,
		}
		sim.RebuildIndicesForTest(world)
		return nil, nil
	}})
}

// stampSpeechWarrant stamps an NPC-speech warrant on id — the producer the
// 2026-10-02 messenger kept being woken by — and reports whether the funnel took it.
func stampSpeechWarrant(t *testing.T, w *sim.World, id sim.ActorID, speechID uint64, now time.Time) bool {
	t.Helper()
	res := sendT(t, w, sim.StampWarrant(id, sim.WarrantMeta{
		TriggerActorID: "lewis",
		Reason:         sim.NPCSpeechWarrantReason{SpeechID: sim.SpeechID(speechID), Speaker: "lewis", Excerpt: "Good day."},
		SourceEventID:  sim.EventID(speechID),
		OccurredAt:     now,
	}, now))
	return res.(sim.StampWarrantResult).Stamped
}

// TestDepartVisitor_SilencesAndWalksOut pins the operator lever: a visitor
// mid-stay, with an open warrant cycle and an LLM call in flight, is sent away —
// his stay ends now, the walk out starts, the open cycle and the in-flight attempt
// are gone, and no later stimulus can open a turn for him.
func TestDepartVisitor_SilencesAndWalksOut(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := time.Now().UTC()
	seedDepartVisitor(t, w, "vstr-roger", sim.VisitorPhaseMakingRounds, now.Add(12*time.Hour))
	if !stampSpeechWarrant(t, w, "vstr-roger", 1, now) {
		t.Fatal("precondition: a visitor making rounds should take a speech warrant")
	}
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["vstr-roger"]
		a.TickInFlight = true
		a.TickAttemptID = "attempt-before-depart"
		return nil, nil
	}})

	res := sendT(t, w, sim.DepartVisitor("vstr-roger", now))
	out := res.(sim.DepartVisitorResult)
	if out.Walk != sim.VisitorDepartWalkStarted {
		t.Errorf("walk = %q, want %q", out.Walk, sim.VisitorDepartWalkStarted)
	}
	if !out.ExpiresAt.Equal(now) {
		t.Errorf("ExpiresAt = %v, want now %v (stay ends at the call)", out.ExpiresAt, now)
	}

	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["vstr-roger"]
		if a.VisitorState.Phase != sim.VisitorPhaseDeparting {
			t.Errorf("phase = %q, want departing", a.VisitorState.Phase)
		}
		if a.VisitorState.DepartCause != sim.VisitorDepartCauseOperator {
			t.Errorf("DepartCause = %q, want operator", a.VisitorState.DepartCause)
		}
		if a.MoveIntent == nil {
			t.Error("no MoveIntent — the walk to the edge was not issued")
		}
		if a.WarrantedSince != nil || len(a.Warrants) != 0 {
			t.Errorf("open warrant cycle survived the depart: since=%v warrants=%d", a.WarrantedSince, len(a.Warrants))
		}
		if a.TickInFlight || a.TickAttemptID != "" {
			t.Errorf("in-flight attempt survived the depart: inFlight=%v attempt=%q", a.TickInFlight, a.TickAttemptID)
		}
		return nil, nil
	}})

	if stampSpeechWarrant(t, w, "vstr-roger", 2, now.Add(time.Second)) {
		t.Error("an operator-departed visitor took a new warrant — he could still speak on his way out")
	}
}

// TestDepartVisitor_NaturalDepartureKeepsTurns pins the scope Jeff chose: only an
// operator-departed visitor is silenced. A traveler leaving because his stay ran
// out may still be woken (to answer a farewell) — until the operator sends him
// away, which keeps his walk and only adds the silencing.
func TestDepartVisitor_NaturalDepartureKeepsTurns(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := time.Now().UTC()
	seedDepartVisitor(t, w, "vstr-elias", sim.VisitorPhasePresent, now.Add(-time.Minute))
	tm := sendT(t, w, sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(7))})).(sim.VisitorCascadeTelemetry)
	if tm.DespawnsStarted != 1 {
		t.Fatalf("precondition: DespawnsStarted = %d, want 1", tm.DespawnsStarted)
	}
	if !stampSpeechWarrant(t, w, "vstr-elias", 1, now) {
		t.Error("a naturally departing visitor was refused a warrant — only an operator depart silences")
	}

	expiresBefore := now.Add(-time.Minute)
	out := sendT(t, w, sim.DepartVisitor("vstr-elias", now)).(sim.DepartVisitorResult)
	if out.Walk != sim.VisitorDepartWalkAlreadyDeparting {
		t.Errorf("walk = %q, want %q", out.Walk, sim.VisitorDepartWalkAlreadyDeparting)
	}
	if !out.ExpiresAt.Equal(expiresBefore) {
		t.Errorf("ExpiresAt = %v, want the original %v (already past; left alone)", out.ExpiresAt, expiresBefore)
	}
	if stampSpeechWarrant(t, w, "vstr-elias", 2, now.Add(time.Second)) {
		t.Error("visitor still warranted after the operator sent him away")
	}
}

// TestDepartVisitor_CleanupRemovesAfterGrace pins that the operator path ends on
// the normal cleanup: VisitorCleanupGraceMinutes after the call the visitor is
// gone and ActorDeparted fired.
func TestDepartVisitor_CleanupRemovesAfterGrace(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := time.Now().UTC()
	seedDepartVisitor(t, w, "vstr-roger", sim.VisitorPhaseMakingRounds, now.Add(12*time.Hour))
	sendT(t, w, sim.DepartVisitor("vstr-roger", now))

	var departed bool
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if d, ok := evt.(*sim.ActorDeparted); ok && d.ActorID == "vstr-roger" {
			departed = true
		}
	}))
	later := now.Add(time.Duration(sim.VisitorCleanupGraceMinutes+1) * time.Minute)
	tm := sendT(t, w, sim.TickVisitorCascade(sim.VisitorTickInputs{Now: later, Rand: rand.New(rand.NewSource(1))})).(sim.VisitorCascadeTelemetry)
	if tm.CleanedUp != 1 {
		t.Errorf("CleanedUp = %d, want 1", tm.CleanedUp)
	}
	if _, ok := w.Published().Actors["vstr-roger"]; ok {
		t.Error("operator-departed visitor still present past the grace window")
	}
	if !departed {
		t.Error("ActorDeparted not emitted for the operator-departed visitor")
	}
}

// TestDepartVisitor_Refusals: an unknown id is ErrActorNotFound; a resident is
// ErrNotAVisitor and is left untouched.
func TestDepartVisitor_Refusals(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.DepartVisitor("nobody", now)); !errors.Is(err, sim.ErrActorNotFound) {
		t.Errorf("unknown id: err = %v, want ErrActorNotFound", err)
	}
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["john"] = &sim.Actor{
			ID: "john", DisplayName: "John Ellis", Kind: sim.KindNPCStateful,
			Pos: sim.TilePos{X: sim.PadX + 4, Y: sim.PadY + 4}, Inventory: map[sim.ItemKind]int{}, State: sim.StateIdle,
		}
		return nil, nil
	}})
	if _, err := w.Send(sim.DepartVisitor("john", now)); !errors.Is(err, sim.ErrNotAVisitor) {
		t.Errorf("resident: err = %v, want ErrNotAVisitor", err)
	}
	if _, ok := w.Published().Actors["john"]; !ok {
		t.Error("resident removed by a refused depart")
	}
}
