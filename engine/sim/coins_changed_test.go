package sim_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// coinsEventRecorder collects NPCCoinsChanged events off the world's event bus.
// Same goroutine discipline as dormancyEventRecorder: appends land on the world
// goroutine during emit, reads happen on the test goroutine after w.Send returns
// (the reply channel is the happens-before edge); the mutex keeps -race quiet.
type coinsEventRecorder struct {
	mu     sync.Mutex
	events []*sim.NPCCoinsChanged
}

func (r *coinsEventRecorder) handle(_ *sim.World, evt sim.Event) {
	if e, ok := evt.(*sim.NPCCoinsChanged); ok {
		r.mu.Lock()
		r.events = append(r.events, e)
		r.mu.Unlock()
	}
}

func (r *coinsEventRecorder) byActor(id sim.ActorID) *sim.NPCCoinsChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.ActorID == id {
			return e
		}
	}
	return nil
}

func (r *coinsEventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *coinsEventRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// setActorCoins sends a command that sets one actor's purse balance, exercising
// the command-loop republish + coins diff without depending on a real transaction
// (the same direct-mutation posture TestEmitDormancyDeltas uses for state).
func setActorCoins(t *testing.T, w *sim.World, id sim.ActorID, coins int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].Coins = coins
		return nil, nil
	}}); err != nil {
		t.Fatalf("set %s coins=%d: %v", id, coins, err)
	}
}

// TestEmitCoinsDeltas verifies the command-loop coin change detection: a balance
// change emits one NPCCoinsChanged carrying the full post-change value, a no-op
// write of the same value emits nothing, a later change re-emits, and — unlike the
// dormancy frame — a PC's balance change DOES emit (the editor villager row shows
// coins for PCs too, so coins is not kind-gated).
func TestEmitCoinsDeltas(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc": {ID: "npc", Kind: sim.KindNPCStateful, LLMAgent: "salem-vendor", State: sim.StateIdle, Coins: 10},
		"pc":  {ID: "pc", Kind: sim.KindPC, State: sim.StateIdle, Coins: 100},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	rec := &coinsEventRecorder{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// 10 → 50: one delta carrying the full post-change balance.
	setActorCoins(t, w, "npc", 50)
	if got := rec.byActor("npc"); got == nil {
		t.Fatalf("no NPCCoinsChanged on 10→50")
	} else if got.Coins != 50 {
		t.Errorf("coins = %d, want 50", got.Coins)
	}

	// 50 → 50: balance unchanged → nothing emitted.
	rec.reset()
	setActorCoins(t, w, "npc", 50)
	if n := rec.count(); n != 0 {
		t.Errorf("no-op write emitted %d deltas, want 0", n)
	}

	// 50 → 25: a later change re-emits with the new value.
	rec.reset()
	setActorCoins(t, w, "npc", 25)
	if got := rec.byActor("npc"); got == nil || got.Coins != 25 {
		t.Fatalf("50→25 delta = %+v, want coins 25", got)
	}

	// A PC's balance change emits too — coins is not kind-gated (the dormancy frame
	// excludes PCs; this one must not).
	rec.reset()
	setActorCoins(t, w, "pc", 80)
	if got := rec.byActor("pc"); got == nil || got.Coins != 80 {
		t.Fatalf("PC coins delta = %+v, want coins 80", got)
	}
}
