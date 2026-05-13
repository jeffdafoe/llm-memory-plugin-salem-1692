package sim_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestSubscriber_ReceivesEventsInEmissionOrder covers the dispatch
// contract: every event emitted by a command Fn is delivered to every
// subscriber in registration order, synchronously inside the world
// goroutine. This is the seam the acquaintance reactor, action-log
// sink, future greet/farewell reactors, and the warrant-watcher all
// hang off of.
func TestSubscriber_ReceivesEventsInEmissionOrder(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", DisplayName: "Alice"},
		"bob":   {ID: "bob", DisplayName: "Bob"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	// Subscribers MUST be registered BEFORE Run starts.
	var (
		mu     sync.Mutex
		events []sim.Event
	)
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()
	if _, err := w.Send(sim.JoinHuddle("alice", "tavern", "", now)); err != nil {
		t.Fatalf("alice join: %v", err)
	}
	if _, err := w.Send(sim.JoinHuddle("bob", "tavern", "", now.Add(10*time.Second))); err != nil {
		t.Fatalf("bob join: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Expected sequence:
	//   HuddleJoined{alice, no others}
	//   HuddleJoined{bob, [alice]}
	//   ActorMet{bob, alice}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %#v", len(events), events)
	}
	first, ok := events[0].(sim.HuddleJoined)
	if !ok || first.ActorID != "alice" || !first.HuddleNew || len(first.OtherMembers) != 0 {
		t.Errorf("event[0] = %#v, want HuddleJoined{alice, new=true, no others}", events[0])
	}
	second, ok := events[1].(sim.HuddleJoined)
	if !ok || second.ActorID != "bob" || second.HuddleNew || len(second.OtherMembers) != 1 || second.OtherMembers[0] != "alice" {
		t.Errorf("event[1] = %#v, want HuddleJoined{bob, new=false, others=[alice]}", events[1])
	}
	third, ok := events[2].(sim.ActorMet)
	if !ok || third.A != "bob" || third.B != "alice" {
		t.Errorf("event[2] = %#v, want ActorMet{bob, alice}", events[2])
	}
}

// TestSubscriber_LeaveAndConcludeOrdering locks in that HuddleLeft
// always precedes HuddleConcluded for the last departing member —
// subscribers can rely on the ordering for "did the leave conclude the
// huddle" derivations without re-querying state.
func TestSubscriber_LeaveAndConcludeOrdering(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	var (
		mu     sync.Mutex
		events []sim.Event
	)
	// Subscribe via a command to dodge the "before Run starts" rule —
	// the world is already running from buildHuddleTestWorld, but a
	// command-Fn append into Subscribe is safe because we're inside the
	// single world goroutine here.
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
				mu.Lock()
				events = append(events, evt)
				mu.Unlock()
			}))
			return nil, nil
		},
	})

	now := time.Now().UTC()
	res := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult)
	_ = res
	if _, err := w.Send(sim.LeaveHuddle("alice", now.Add(time.Minute))); err != nil {
		t.Fatalf("LeaveHuddle: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Subscribe was registered before the join, so we see the full sequence:
	//   HuddleJoined{alice}                — alice joins
	//   HuddleLeft{alice, no remaining}    — alice leaves
	//   HuddleConcluded                    — solo-leave conclusion
	//
	// The load-bearing assertion is the [HuddleLeft, HuddleConcluded]
	// ordering: subscribers can rely on HuddleLeft preceding the
	// concluding event for the last departing member.
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %#v", len(events), events)
	}
	if _, ok := events[0].(sim.HuddleJoined); !ok {
		t.Errorf("event[0] = %#v, want HuddleJoined", events[0])
	}
	if _, ok := events[1].(sim.HuddleLeft); !ok {
		t.Errorf("event[1] = %#v, want HuddleLeft", events[1])
	}
	if _, ok := events[2].(sim.HuddleConcluded); !ok {
		t.Errorf("event[2] = %#v, want HuddleConcluded", events[2])
	}
}
