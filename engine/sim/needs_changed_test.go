package sim_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// needsEventRecorder collects NPCNeedsChanged events off the world's event bus.
// Appends land on the world goroutine (during emit); reads happen on the test
// goroutine after w.Send returns (the command reply channel is the happens-
// before edge). The mutex keeps -race quiet regardless.
type needsEventRecorder struct {
	mu     sync.Mutex
	events []*sim.NPCNeedsChanged
}

func (r *needsEventRecorder) handle(_ *sim.World, evt sim.Event) {
	if e, ok := evt.(*sim.NPCNeedsChanged); ok {
		r.mu.Lock()
		r.events = append(r.events, e)
		r.mu.Unlock()
	}
}

func (r *needsEventRecorder) byActor(id sim.ActorID) *sim.NPCNeedsChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.ActorID == id {
			return e
		}
	}
	return nil
}

func (r *needsEventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *needsEventRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// TestEmitNeedsDeltas verifies the command-loop change detection: a command that
// moves an actor's needs broadcasts exactly one NPCNeedsChanged for the touched
// actor (carrying the full post-change triple), a tick-ineligible actor whose
// needs don't move emits nothing, and a command that changes no needs at all
// emits nothing.
func TestEmitNeedsDeltas(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc": {
			ID:       "npc",
			LLMAgent: "salem-vendor",
			Needs:    map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5},
		},
		"decorative": {
			ID:    "decorative",
			Needs: map[sim.NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	rec := &needsEventRecorder{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// A command that raises needs emits one delta for the touched actor.
	if _, err := w.Send(sim.IncrementNeedsTick(2)); err != nil { // +2 @ 1/h
		t.Fatalf("increment: %v", err)
	}
	got := rec.byActor("npc")
	if got == nil {
		t.Fatalf("no NPCNeedsChanged emitted for npc")
	}
	if got.Hunger != 7 || got.Thirst != 7 || got.Tiredness != 7 {
		t.Errorf("npc delta = %d/%d/%d, want 7/7/7", got.Hunger, got.Thirst, got.Tiredness)
	}
	// The decorative is tick-ineligible (no agent / login): needs unchanged, so
	// the diff produces no delta for it.
	if e := rec.byActor("decorative"); e != nil {
		t.Errorf("decorative needs unchanged — unexpected NPCNeedsChanged %+v", e)
	}

	// A command that changes no needs emits nothing further.
	rec.reset()
	if _, err := w.Send(sim.IncrementNeedsTick(0)); err != nil { // no-op (capped <= 0)
		t.Fatalf("noop increment: %v", err)
	}
	if n := rec.count(); n != 0 {
		t.Errorf("no-op command emitted %d deltas, want 0", n)
	}

	// A newly inserted actor that spawns with non-zero needs gets a correcting
	// delta even though it's absent from the prior snapshot (npc_created only
	// delivers 0/0/0); one that spawns at 0/0/0 gets no redundant frame.
	rec.reset()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["newcomer"] = &sim.Actor{
			ID: "newcomer", LLMAgent: "salem-visitor",
			Needs: map[sim.NeedKey]int{"hunger": 10, "thirst": 0, "tiredness": 0},
		}
		world.Actors["ghost"] = &sim.Actor{
			ID: "ghost", LLMAgent: "salem-visitor",
			Needs: map[sim.NeedKey]int{"hunger": 0, "thirst": 0, "tiredness": 0},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("insert newcomer: %v", err)
	}
	if got := rec.byActor("newcomer"); got == nil {
		t.Fatalf("no NPCNeedsChanged for newcomer spawned with non-zero needs")
	} else if got.Hunger != 10 || got.Thirst != 0 || got.Tiredness != 0 {
		t.Errorf("newcomer delta = %d/%d/%d, want 10/0/0", got.Hunger, got.Thirst, got.Tiredness)
	}
	if got := rec.byActor("ghost"); got != nil {
		t.Errorf("ghost spawned at 0/0/0 — unexpected redundant NPCNeedsChanged %+v", got)
	}
}
