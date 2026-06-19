package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestWorldSmoke is the skeleton-PR acceptance smoke test:
//  1. Build a world with mem repo.
//  2. Send a no-op command.
//  3. Assert TickCounter advanced.
//  4. Assert a fresh Snapshot was published with the new tick.
func TestWorldSmoke(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	initial := w.Published()
	if initial == nil {
		t.Fatal("expected initial snapshot to be published by NewWorld")
	}
	if initial.AtTick != 0 {
		t.Fatalf("initial snapshot AtTick = %d, want 0", initial.AtTick)
	}

	value, err := w.Send(sim.Command{
		Fn: func(_ *sim.World) (any, error) {
			return "no-op result", nil
		},
	})
	if err != nil {
		t.Fatalf("no-op command returned error: %v", err)
	}
	if value != "no-op result" {
		t.Fatalf("no-op command value = %v, want \"no-op result\"", value)
	}

	snap := w.Published()
	if snap == nil {
		t.Fatal("expected snapshot to be published after command")
	}
	if snap.AtTick != 1 {
		t.Fatalf("post-command AtTick = %d, want 1", snap.AtTick)
	}
	if snap.PublishedAt.Before(initial.PublishedAt) {
		t.Fatalf("PublishedAt went backwards: initial=%v, post=%v", initial.PublishedAt, snap.PublishedAt)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("world goroutine did not exit on ctx cancel")
	}
}

// TestLoadWorldAndActorSnapshot exercises LoadWorld + the snapshotActor
// path: seed an actor with a macro-state, load the world, send a no-op
// command to trigger republish, confirm the published ActorSnapshot
// reflects the state.
func TestLoadWorldAndActorSnapshot(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"elizabeth": {
			ID:            "elizabeth",
			DisplayName:   "Elizabeth Ellis",
			Kind:          sim.KindNPCStateful,
			State:         sim.StateWalking,
			RecentActions: sim.NewRingBuffer[sim.Action](16),
			Needs:         map[sim.NeedKey]int{},
			Inventory:     map[sim.ItemKind]int{},
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// LoadWorld already published; assert the loaded state surfaced.
	snap := w.Published()
	actor, ok := snap.Actors["elizabeth"]
	if !ok {
		t.Fatal("expected elizabeth in snapshot after LoadWorld")
	}
	if actor.State != sim.StateWalking {
		t.Fatalf("snapshot actor State = %q, want %q", actor.State, sim.StateWalking)
	}

	// Send a no-op to confirm republish keeps the state intact and
	// advances the tick.
	_, err = w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }})
	if err != nil {
		t.Fatalf("no-op send: %v", err)
	}
	snap = w.Published()
	if snap.AtTick != 1 {
		t.Fatalf("post-noop AtTick = %d, want 1", snap.AtTick)
	}
	if snap.Actors["elizabeth"].State != sim.StateWalking {
		t.Fatalf("state lost after republish: %q", snap.Actors["elizabeth"].State)
	}
}

// TestRingBuffer covers the ring-buffer primitive directly: under-cap,
// at-cap, and wrapped behavior.
func TestRingBuffer(t *testing.T) {
	rb := sim.NewRingBuffer[int](3)
	if rb.Len() != 0 || rb.Cap() != 3 {
		t.Fatalf("fresh buffer: Len=%d Cap=%d, want 0,3", rb.Len(), rb.Cap())
	}
	rb.Push(1)
	rb.Push(2)
	if got := rb.Snapshot(); !equalInt(got, []int{1, 2}) {
		t.Fatalf("partial: %v, want [1 2]", got)
	}
	rb.Push(3)
	rb.Push(4) // wraps; 1 falls off
	if got := rb.Snapshot(); !equalInt(got, []int{2, 3, 4}) {
		t.Fatalf("wrapped: %v, want [2 3 4]", got)
	}
	rb.Push(5)
	rb.Push(6)
	if got := rb.Snapshot(); !equalInt(got, []int{4, 5, 6}) {
		t.Fatalf("double-wrapped: %v, want [4 5 6]", got)
	}
}

func equalInt(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
