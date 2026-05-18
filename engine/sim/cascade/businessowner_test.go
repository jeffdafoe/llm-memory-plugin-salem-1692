package cascade

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildBusinessownerCascadeWorld stands up a world with a keeper + a
// customer, runs it, and returns handles. Caller seeds further state
// via invokeBusinessownerOnWorld.
func buildBusinessownerCascadeWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:                 "hannah",
			DisplayName:        "Hannah",
			Kind:               sim.KindNPCShared,
			State:              sim.StateIdle,
			StateEnteredAt:     now,
			CurrentHuddleID:    "h1",
			WorkStructureID:    "tavern",
			InsideStructureID:  "tavern",
			BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
			RecentActions:      sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans:   sim.NewRingBuffer[sim.StateTransition](4),
		},
		"jefferey": {
			ID:                "jefferey",
			DisplayName:       "Jefferey",
			Kind:              sim.KindPC,
			State:             sim.StateIdle,
			StateEnteredAt:    now,
			CurrentHuddleID:   "h1",
			InsideStructureID: "tavern",
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans:  sim.NewRingBuffer[sim.StateTransition](4),
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "the tavern"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// invokeBusinessownerOnWorld runs fn on the world goroutine inside a
// Command. Used to call subscriber handlers under their real concurrency
// model.
func invokeBusinessownerOnWorld(t *testing.T, w *sim.World, fn func(*sim.World)) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		fn(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("invokeBusinessownerOnWorld: %v", err)
	}
}

// observeSpokes subscribes a Spoke-collector to the world and returns
// a getter that pulls the slice off the goroutine.
func observeSpokes(t *testing.T, w *sim.World) func() []*sim.Spoke {
	t.Helper()
	var collected []*sim.Spoke
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if s, ok := evt.(*sim.Spoke); ok {
				collected = append(collected, s)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe spokes: %v", err)
	}
	return func() []*sim.Spoke {
		var out []*sim.Spoke
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			out = append(out, collected...)
			return nil, nil
		}}); err != nil {
			t.Fatalf("read spokes: %v", err)
		}
		return out
	}
}

// TestRegisterBusinessowner_NilWorldPanics is the wiring guard.
func TestRegisterBusinessowner_NilWorldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("RegisterBusinessowner(nil) did not panic")
		}
	}()
	RegisterBusinessowner(context.Background(), nil)
}

// TestHandleHuddleJoined_FiresGreet — happy path. A non-keeper joins a
// huddle the keeper is in at their work structure; greet emits.
func TestHandleHuddleJoined_FiresGreet(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	r := rand.New(rand.NewSource(42))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleJoined{
			ActorID:      "jefferey",
			HuddleID:     "h1",
			StructureID:  "tavern",
			OtherMembers: []sim.ActorID{"hannah"},
			At:           now,
		}
		handleHuddleJoinedBusinessowner(world, evt, r)
	})
	spokes := getSpokes()
	if len(spokes) != 1 {
		t.Fatalf("got %d Spoke events, want 1", len(spokes))
	}
	if spokes[0].SpeakerID != "hannah" {
		t.Errorf("speaker = %q, want hannah", spokes[0].SpeakerID)
	}
}

// TestHandleHuddleJoined_JoinerIsBusinessowner_Skips — gate 1: don't
// have keepers greet each other.
func TestHandleHuddleJoined_JoinerIsBusinessowner_Skips(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	// Flip jefferey to a keeper for this test.
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Actors["jefferey"].BusinessownerState = &sim.BusinessownerState{Flavor: "reserved"}
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleJoined{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			OtherMembers: []sim.ActorID{"hannah"},
			At:           time.Now().UTC(),
		}
		handleHuddleJoinedBusinessowner(world, evt, r)
	})
	if got := getSpokes(); len(got) != 0 {
		t.Errorf("got %d Spoke events, want 0 (keeper joining doesn't trigger)", len(got))
	}
}

// TestHandleHuddleJoined_KeeperWrongStructure_Skips — at-post check.
// Keeper at a structure other than the event's StructureID gets no greet.
func TestHandleHuddleJoined_KeeperWrongStructure_Skips(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleJoined{
			ActorID: "jefferey", HuddleID: "h2",
			StructureID:  "wrong-structure", // hannah's WorkStructureID is "tavern"
			OtherMembers: []sim.ActorID{"hannah"},
			At:           time.Now().UTC(),
		}
		handleHuddleJoinedBusinessowner(world, evt, r)
	})
	if got := getSpokes(); len(got) != 0 {
		t.Errorf("got %d Spoke events, want 0 (off-post keeper)", len(got))
	}
}

// TestHandleHuddleJoined_KeeperSleeping_Skips — sleeping/resting gate.
func TestHandleHuddleJoined_KeeperSleeping_Skips(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Actors["hannah"].State = sim.StateSleeping
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleJoined{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			OtherMembers: []sim.ActorID{"hannah"},
			At:           time.Now().UTC(),
		}
		handleHuddleJoinedBusinessowner(world, evt, r)
	})
	if got := getSpokes(); len(got) != 0 {
		t.Errorf("got %d Spoke events, want 0 (keeper sleeping)", len(got))
	}
}

// TestHandleHuddleJoined_Cooldown — second greet inside the cooldown
// window is suppressed.
func TestHandleHuddleJoined_Cooldown(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	r := rand.New(rand.NewSource(1))
	now := time.Now().UTC()
	// First greet fires.
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleJoined{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			OtherMembers: []sim.ActorID{"hannah"}, At: now,
		}
		handleHuddleJoinedBusinessowner(world, evt, r)
	})
	// Second greet 1 minute later — inside the 30-min default cooldown.
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleJoined{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			OtherMembers: []sim.ActorID{"hannah"}, At: now.Add(1 * time.Minute),
		}
		handleHuddleJoinedBusinessowner(world, evt, r)
	})
	if got := getSpokes(); len(got) != 1 {
		t.Errorf("got %d Spoke events, want 1 (second cooldown'd)", len(got))
	}
}

// TestHandleOrderDelivered_FiresHandover — happy path. Seller is a
// keeper; handover emits.
func TestHandleOrderDelivered_FiresHandover(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.OrderDelivered{
			OrderID: 1, SellerID: "hannah", BuyerID: "jefferey",
			Item: "ale", Qty: 1, ConsumerIDs: []sim.ActorID{"jefferey"},
			LedgerID: 1, At: time.Now().UTC(),
		}
		handleOrderDeliveredBusinessowner(world, evt, r)
	})
	spokes := getSpokes()
	if len(spokes) != 1 {
		t.Fatalf("got %d Spoke events, want 1", len(spokes))
	}
	if spokes[0].SpeakerID != "hannah" {
		t.Errorf("speaker = %q, want hannah", spokes[0].SpeakerID)
	}
	// No cooldown on handover — fire again and verify it emits again.
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.OrderDelivered{
			OrderID: 2, SellerID: "hannah", BuyerID: "jefferey",
			Item: "ale", Qty: 1, ConsumerIDs: []sim.ActorID{"jefferey"},
			LedgerID: 2, At: time.Now().UTC().Add(1 * time.Second),
		}
		handleOrderDeliveredBusinessowner(world, evt, r)
	})
	if got := getSpokes(); len(got) != 2 {
		t.Errorf("got %d Spoke events after second handover, want 2", len(got))
	}
}

// TestHandleOrderDelivered_SellerNotBusinessowner_Skips — non-keeper
// seller produces no engine speech.
func TestHandleOrderDelivered_SellerNotBusinessowner_Skips(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Actors["hannah"].BusinessownerState = nil
	})
	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.OrderDelivered{
			OrderID: 1, SellerID: "hannah", BuyerID: "jefferey",
			Item: "ale", Qty: 1, ConsumerIDs: []sim.ActorID{"jefferey"},
			LedgerID: 1, At: time.Now().UTC(),
		}
		handleOrderDeliveredBusinessowner(world, evt, r)
	})
	if got := getSpokes(); len(got) != 0 {
		t.Errorf("got %d Spoke events, want 0 (non-keeper seller)", len(got))
	}
}

// TestHandleHuddleLeft_FiresFarewell — happy path. Non-keeper leaves a
// huddle a keeper remains in at their work structure.
func TestHandleHuddleLeft_FiresFarewell(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleLeft{
			ActorID:          "jefferey",
			HuddleID:         "h1",
			StructureID:      "tavern",
			RemainingMembers: []sim.ActorID{"hannah"},
			At:               time.Now().UTC(),
		}
		handleHuddleLeftBusinessowner(world, evt, r)
	})
	spokes := getSpokes()
	if len(spokes) != 1 {
		t.Fatalf("got %d Spoke events, want 1", len(spokes))
	}
	if spokes[0].SpeakerID != "hannah" {
		t.Errorf("speaker = %q, want hannah", spokes[0].SpeakerID)
	}
}

// TestHandleHuddleLeft_LeaverIsBusinessowner_Skips — keepers don't bid
// each other farewell.
func TestHandleHuddleLeft_LeaverIsBusinessowner_Skips(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Actors["jefferey"].BusinessownerState = &sim.BusinessownerState{Flavor: "reserved"}
	})
	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: time.Now().UTC(),
		}
		handleHuddleLeftBusinessowner(world, evt, r)
	})
	if got := getSpokes(); len(got) != 0 {
		t.Errorf("got %d Spoke events, want 0 (keeper leaving)", len(got))
	}
}

// TestBuildBusinessownerRecipients covers the slice helper's branches:
// dedup, exclude, extra, empty.
func TestBuildBusinessownerRecipients(t *testing.T) {
	t.Run("excludes speaker, keeps order", func(t *testing.T) {
		got := buildBusinessownerRecipients(
			[]sim.ActorID{"a", "keeper", "b"}, "", "keeper",
		)
		want := []sim.ActorID{"a", "b"}
		if !equalActorIDs(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("appends extra when not present", func(t *testing.T) {
		got := buildBusinessownerRecipients(
			[]sim.ActorID{"a"}, "joiner", "keeper",
		)
		want := []sim.ActorID{"a", "joiner"}
		if !equalActorIDs(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("dedups extra if already present", func(t *testing.T) {
		got := buildBusinessownerRecipients(
			[]sim.ActorID{"a", "joiner"}, "joiner", "keeper",
		)
		want := []sim.ActorID{"a", "joiner"}
		if !equalActorIDs(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("dedups duplicates in input", func(t *testing.T) {
		got := buildBusinessownerRecipients(
			[]sim.ActorID{"a", "a", "b"}, "", "keeper",
		)
		want := []sim.ActorID{"a", "b"}
		if !equalActorIDs(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("ignores empty IDs", func(t *testing.T) {
		got := buildBusinessownerRecipients(
			[]sim.ActorID{"", "a", ""}, "", "keeper",
		)
		want := []sim.ActorID{"a"}
		if !equalActorIDs(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

func equalActorIDs(a, b []sim.ActorID) bool {
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
