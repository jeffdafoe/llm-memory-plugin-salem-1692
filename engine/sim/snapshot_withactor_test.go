package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestSnapshotWithActor pins the LLM-88 mid-tick self-state patch helper: it
// overrides exactly one actor's entry, keeps every other actor aliased (cheap
// pointer copy), shallow-copies scalar fields, and — critically — does NOT
// mutate the source snapshot's Actors map. The published snapshot is shared
// lock-free, so an in-place edit there would be a data race.
func TestSnapshotWithActor(t *testing.T) {
	alice := &sim.ActorSnapshot{DisplayName: "alice", Coins: 1}
	bob := &sim.ActorSnapshot{DisplayName: "bob", Coins: 2}
	orig := &sim.Snapshot{
		AtTick: 7,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": alice, "bob": bob},
	}

	newAlice := &sim.ActorSnapshot{DisplayName: "alice", Coins: 99}
	patched := orig.WithActor("alice", newAlice)

	if patched.Actors["alice"] != newAlice {
		t.Error("WithActor must override the target actor's entry")
	}
	if patched.Actors["bob"] != bob {
		t.Error("WithActor must keep other actors aliased")
	}
	if patched.AtTick != orig.AtTick {
		t.Errorf("WithActor must shallow-copy scalar fields: AtTick got %d, want %d", patched.AtTick, orig.AtTick)
	}

	// The source snapshot must be untouched — it is shared lock-free.
	if orig.Actors["alice"] != alice {
		t.Error("WithActor must not mutate the source snapshot's Actors map")
	}
	if len(orig.Actors) != 2 {
		t.Errorf("source Actors map size changed: got %d, want 2", len(orig.Actors))
	}

	// The patched map is a distinct object — adding to it must not leak back.
	patched.Actors["carol"] = &sim.ActorSnapshot{DisplayName: "carol"}
	if _, leaked := orig.Actors["carol"]; leaked {
		t.Error("adding to the patched map must not leak into the source map")
	}
}

// TestSnapshotWithoutOrders pins the deliver_order companion to WithActor
// (LLM-887): the named orders are gone from the copy, the rest stay aliased, and
// the shared source snapshot is untouched. An empty set returns the source.
func TestSnapshotWithoutOrders(t *testing.T) {
	kept := &sim.Order{ID: 1}
	gone := &sim.Order{ID: 2}
	orig := &sim.Snapshot{
		AtTick: 7,
		Orders: map[sim.OrderID]*sim.Order{1: kept, 2: gone},
	}

	if orig.WithoutOrders(nil) != orig {
		t.Error("WithoutOrders(nil) must return the source snapshot")
	}

	pruned := orig.WithoutOrders(map[sim.OrderID]struct{}{2: {}, 99: {}})
	if _, ok := pruned.Orders[2]; ok {
		t.Error("WithoutOrders must drop the named order")
	}
	if pruned.Orders[1] != kept {
		t.Error("WithoutOrders must keep other orders aliased")
	}
	if pruned.AtTick != orig.AtTick {
		t.Errorf("WithoutOrders must shallow-copy scalar fields: AtTick got %d, want %d", pruned.AtTick, orig.AtTick)
	}
	if orig.Orders[2] != gone || len(orig.Orders) != 2 {
		t.Error("WithoutOrders must not mutate the source snapshot's Orders map")
	}
}
