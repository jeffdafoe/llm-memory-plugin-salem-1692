package sim_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// dormancyEventRecorder collects NPCDormancyChanged events off the world's event
// bus. Same goroutine discipline as needsEventRecorder: appends land on the world
// goroutine during emit, reads happen on the test goroutine after w.Send returns
// (the reply channel is the happens-before edge); the mutex keeps -race quiet.
type dormancyEventRecorder struct {
	mu     sync.Mutex
	events []*sim.NPCDormancyChanged
}

func (r *dormancyEventRecorder) handle(_ *sim.World, evt sim.Event) {
	if e, ok := evt.(*sim.NPCDormancyChanged); ok {
		r.mu.Lock()
		r.events = append(r.events, e)
		r.mu.Unlock()
	}
}

func (r *dormancyEventRecorder) byActor(id sim.ActorID) *sim.NPCDormancyChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.ActorID == id {
			return e
		}
	}
	return nil
}

func (r *dormancyEventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *dormancyEventRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// setActorState sends a command that flips one actor's macro-state, exercising
// the command-loop republish + dormancy diff without depending on the sleep
// machine (the same direct-mutation posture TestEmitNeedsDeltas uses for needs).
func setActorState(t *testing.T, w *sim.World, id sim.ActorID, st sim.ActorState) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].State = st
		return nil, nil
	}}); err != nil {
		t.Fatalf("set %s state=%s: %v", id, st, err)
	}
}

// TestEmitDormancyDeltas verifies the command-loop dormancy change detection:
// crossing into a dormant state emits one NPCDormancyChanged carrying the dormant
// token, switching between the two dormant states re-emits (token changed), waking
// emits the clearing "" token, a non-dormant transition (idle↔walking) emits
// nothing, and a PC is excluded (PCs ride the pc_sleep frames).
func TestEmitDormancyDeltas(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc": {ID: "npc", Kind: sim.KindNPCStateful, LLMAgent: "salem-vendor", State: sim.StateIdle},
		"pc":  {ID: "pc", Kind: sim.KindPC, State: sim.StateIdle},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	rec := &dormancyEventRecorder{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// idle → sleeping: one delta carrying "sleeping".
	setActorState(t, w, "npc", sim.StateSleeping)
	if got := rec.byActor("npc"); got == nil {
		t.Fatalf("no NPCDormancyChanged on idle→sleeping")
	} else if got.State != "sleeping" {
		t.Errorf("state = %q, want sleeping", got.State)
	}

	// sleeping → resting: still dormant, but the token changed, so it re-emits.
	rec.reset()
	setActorState(t, w, "npc", sim.StateResting)
	if got := rec.byActor("npc"); got == nil || got.State != "resting" {
		t.Fatalf("sleeping→resting delta = %+v, want resting", got)
	}

	// resting → idle (wake): emits the clearing "" token.
	rec.reset()
	setActorState(t, w, "npc", sim.StateIdle)
	if got := rec.byActor("npc"); got == nil {
		t.Fatalf("no NPCDormancyChanged on resting→idle wake")
	} else if got.State != "" {
		t.Errorf("wake state = %q, want empty", got.State)
	}

	// idle → walking: both awake, dormancy unchanged → nothing emitted.
	rec.reset()
	setActorState(t, w, "npc", sim.StateWalking)
	if n := rec.count(); n != 0 {
		t.Errorf("idle→walking emitted %d deltas, want 0", n)
	}

	// A PC entering sleep emits nothing here — PCs ride pc_sleep_started/ended and
	// a distinct client render path; the dormancy frame is NPC-only.
	rec.reset()
	setActorState(t, w, "pc", sim.StateSleeping)
	if got := rec.byActor("pc"); got != nil {
		t.Errorf("PC sleep should not emit NPCDormancyChanged, got %+v", got)
	}
}
