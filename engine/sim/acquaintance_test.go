package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildAcquaintanceTestWorld stands up a world with three actors:
// hannah (shared NPC), ezekiel (stateful NPC), pc-jeff (PC). Registers
// the acquaintance subscriber. Used by the ActorMet handler tests.
func buildAcquaintanceTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID: "hannah", DisplayName: "Hannah",
			Kind: sim.KindNPCShared, State: sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"ezekiel": {
			ID: "ezekiel", DisplayName: "Ezekiel Crane",
			Kind: sim.KindNPCStateful, State: sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"pc-jeff": {
			ID: "pc-jeff", DisplayName: "Jeff",
			Kind: sim.KindPC, State: sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	sim.RegisterAcquaintanceSubscriber(w)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// emitInCommand fires evt from inside a Command, so subscribers run on
// the world goroutine (matching production where emits happen inside
// command bodies) and the post-Command republish captures the mutation.
func emitInCommand(t *testing.T, w *sim.World, evt sim.Event) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, evt)
		return nil, nil
	}}); err != nil {
		t.Fatalf("emitInCommand Send: %v", err)
	}
}

func TestAcquaintanceSubscriber_NPCPairAddsBothWays(t *testing.T) {
	w, stop := buildAcquaintanceTestWorld(t)
	defer stop()

	at := time.Now().UTC()
	emitInCommand(t, w, &sim.ActorMet{A: "hannah", B: "ezekiel", At: at})

	snap := w.Published()
	if _, ok := snap.Actors["hannah"].Acquaintances["Ezekiel Crane"]; !ok {
		t.Error("hannah should know Ezekiel Crane")
	}
	if _, ok := snap.Actors["ezekiel"].Acquaintances["Hannah"]; !ok {
		t.Error("ezekiel should know Hannah")
	}
}

func TestAcquaintanceSubscriber_PCDoesNotTrackOwn(t *testing.T) {
	w, stop := buildAcquaintanceTestWorld(t)
	defer stop()

	emitInCommand(t, w, &sim.ActorMet{A: "pc-jeff", B: "hannah", At: time.Now().UTC()})

	snap := w.Published()
	// PC doesn't get a row; NPC does.
	if len(snap.Actors["pc-jeff"].Acquaintances) != 0 {
		t.Errorf("PC populated Acquaintances: %+v", snap.Actors["pc-jeff"].Acquaintances)
	}
	if _, ok := snap.Actors["hannah"].Acquaintances["Jeff"]; !ok {
		t.Error("hannah should know Jeff")
	}
}

func TestAcquaintanceSubscriber_FirstMetSemanticsRepeated(t *testing.T) {
	w, stop := buildAcquaintanceTestWorld(t)
	defer stop()

	first := time.Now().UTC()
	second := first.Add(1 * time.Hour)
	emitInCommand(t, w, &sim.ActorMet{A: "hannah", B: "ezekiel", At: first})
	emitInCommand(t, w, &sim.ActorMet{A: "hannah", B: "ezekiel", At: second})

	snap := w.Published()
	got := snap.Actors["hannah"].Acquaintances["Ezekiel Crane"].FirstInteractedAt
	if !got.Equal(first) {
		t.Errorf("FirstInteractedAt = %v, want first %v (second emit should not overwrite)", got, first)
	}
}

func TestAcquaintanceSubscriber_NonActorMetEventIgnored(t *testing.T) {
	w, stop := buildAcquaintanceTestWorld(t)
	defer stop()

	// A non-ActorMet event must not panic or mutate.
	emitInCommand(t, w, &sim.HuddleConcluded{HuddleID: "h-empty", At: time.Now().UTC()})

	snap := w.Published()
	if len(snap.Actors["hannah"].Acquaintances) != 0 {
		t.Errorf("non-ActorMet event mutated Acquaintances: %+v", snap.Actors["hannah"].Acquaintances)
	}
}
