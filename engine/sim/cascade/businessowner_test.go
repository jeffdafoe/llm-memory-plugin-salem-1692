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
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:                 "hannah",
			DisplayName:        "Hannah",
			Kind:               sim.KindNPCShared,
			State:              sim.StateIdle,
			CurrentHuddleID:    "h1",
			WorkStructureID:    "tavern",
			InsideStructureID:  "tavern",
			BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
			RecentActions:      sim.NewRingBuffer[sim.Action](4),
		},
		"jefferey": {
			ID:                "jefferey",
			DisplayName:       "Jefferey",
			Kind:              sim.KindPC,
			State:             sim.StateIdle,
			CurrentHuddleID:   "h1",
			InsideStructureID: "tavern",
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
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

// TestHandleHuddleJoined_FiresGreet — non-LLM-keeper path. A non-keeper joins
// a huddle the keeper is in at their work structure; the engine greet emits.
// The fixture keeper has no LLMAgent, so the ZBBS-HOME-461 gate does not apply
// here — this exercises the agent-less fallback that still engine-greets. The
// VA-backed gate is covered by TestHandleHuddleJoined_LLMKeeperSkipsEngineGreet.
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

// TestHandleHuddleJoined_LLMKeeperSkipsEngineGreet — ZBBS-HOME-461. A keeper
// backed by a VA greets in character on its own huddle-peer-joined tick, so
// the engine greet is suppressed to avoid the double-greet observed live
// (Prudence: engine "Greetings, Jefferey" then a model greet seconds later).
func TestHandleHuddleJoined_LLMKeeperSkipsEngineGreet(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	// Back the keeper with a VA — the production state for every keeper.
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Actors["hannah"].LLMAgent = "salem-vendor"
	})

	r := rand.New(rand.NewSource(42))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		evt := &sim.HuddleJoined{
			ActorID:      "jefferey",
			HuddleID:     "h1",
			StructureID:  "tavern",
			OtherMembers: []sim.ActorID{"hannah"},
			At:           time.Now().UTC(),
		}
		handleHuddleJoinedBusinessowner(world, evt, r)
	})
	if got := getSpokes(); len(got) != 0 {
		t.Errorf("got %d Spoke events, want 0 (VA-backed keeper greets via its own tick)", len(got))
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

// seedBusinessownerHuddle installs huddle h1 (keeper + customer) on the world
// so the LLM-535 farewell gate has a recent-conversation ring to read. The
// cascade fixture seeds actors and structures only, so w.Huddles["h1"] is
// otherwise nil — which the gate reads as "no model speech" and lets the
// farewell through. Returns nothing; call before the HuddleLeft invoke.
func seedBusinessownerHuddle(t *testing.T, w *sim.World, startedAt time.Time) {
	t.Helper()
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Huddles["h1"] = &sim.Huddle{
			ID:          "h1",
			StructureID: "tavern",
			Members: map[sim.ActorID]struct{}{
				"hannah":   {},
				"jefferey": {},
			},
			StartedAt: startedAt,
		}
	})
}

// TestHandleHuddleLeft_ModelSpokeRecently_SuppressesFarewell — LLM-535. The
// keeper's own model said goodbye; the engine must not add a second one.
//
// This is the live case verbatim: the constable announced his departure, the
// keeper answered in character, the constable walked off, and the engine spoke
// a farewell on top of the goodbye the keeper had already said.
func TestHandleHuddleLeft_ModelSpokeRecently_SuppressesFarewell(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-10*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Huddles["h1"].AppendUtterance(
			"hannah", "Hannah",
			"Safe travels to you, Constable. I'll be here if you need me.",
			now.Add(-30*time.Second), 0,
		)
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: now,
		}, r)
	})
	if got := getSpokes(); len(got) != 0 {
		t.Errorf("got %d Spoke events, want 0 (keeper's model already said goodbye): %+v", len(got), got)
	}
}

// TestHandleHuddleLeft_KeeperSilent_StillFiresFarewell — LLM-535 no-regression.
// A customer who leaves without the keeper having said a word is the beat the
// engine farewell exists for. The ring holds the customer's line only.
func TestHandleHuddleLeft_KeeperSilent_StillFiresFarewell(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-10*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Huddles["h1"].AppendUtterance(
			"jefferey", "Jefferey", "Good day to you.", now.Add(-20*time.Second), 0)
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: now,
		}, r)
	})
	spokes := getSpokes()
	if len(spokes) != 1 {
		t.Fatalf("got %d Spoke events, want 1 (silent keeper still bids farewell)", len(spokes))
	}
	if spokes[0].SpeakerID != "hannah" {
		t.Errorf("speaker = %q, want hannah", spokes[0].SpeakerID)
	}
}

// TestHandleHuddleLeft_ModelSpokeLongAgo_FiresFarewell — LLM-535 window edge.
// The keeper spoke, but far enough back that the conversation had lapsed into
// silence before the customer left. That departure gets its own farewell.
func TestHandleHuddleLeft_ModelSpokeLongAgo_FiresFarewell(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-30*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Huddles["h1"].AppendUtterance(
			"hannah", "Hannah", "Two coins and it's yours.", now.Add(-10*time.Minute), 0)
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: now,
		}, r)
	})
	if got := getSpokes(); len(got) != 1 {
		t.Fatalf("got %d Spoke events, want 1 (keeper's line is outside the window)", len(got))
	}
}

// TestHandleHuddleLeft_EngineLineDoesNotSuppressFarewell — LLM-535. The gate
// asks whether the keeper CHOSE to speak, so the engine's own hospitality lines
// must not satisfy it. The live shape is a handover ("There you go") seconds
// before the customer walks out: that is not the keeper saying goodbye, and
// suppressing on it would delete the farewell beat for every transaction.
func TestHandleHuddleLeft_EngineLineDoesNotSuppressFarewell(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-10*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Huddles["h1"].AppendEngineUtterance(
			"hannah", "Hannah", "There you are, Jefferey — enjoy.", now.Add(-10*time.Second), 0)
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: now,
		}, r)
	})
	if got := getSpokes(); len(got) != 1 {
		t.Fatalf("got %d Spoke events, want 1 (an engine line is not the keeper choosing to speak)", len(got))
	}
}

// TestHandleHuddleLeft_UnrelatedKeeperSpeech_AlsoSuppresses — LLM-535, the
// accepted cost of the heuristic, pinned so it can't be lost silently.
//
// The gate asks whether the keeper spoke, not whether it said goodbye. A keeper
// who quoted a price 90s ago and then watched a silent customer walk out loses
// the farewell it would have got before this change. That is deliberate: the
// regression is a missing pleasantry, and it buys off a keeper visibly saying
// goodbye twice. If a future change narrows the gate, this test should be
// re-stated, not deleted.
func TestHandleHuddleLeft_UnrelatedKeeperSpeech_AlsoSuppresses(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-10*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Huddles["h1"].AppendUtterance(
			"hannah", "Hannah", "Two coins for the room, and a penny for the ale.", now.Add(-90*time.Second), 0)
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: now,
		}, r)
	})
	if got := getSpokes(); len(got) != 0 {
		t.Errorf("got %d Spoke events, want 0 (any recent keeper speech suppresses — accepted heuristic cost)", len(got))
	}
}

// TestHandleHuddleLeft_LeaverSpeechDoesNotSuppress — attribution. The gate must
// read the KEEPER's speech, never the departing customer's. A customer who says
// goodbye on the way out is the strongest case for the keeper answering, so
// inspecting the wrong actor here would silence exactly the wrong beat.
func TestHandleHuddleLeft_LeaverSpeechDoesNotSuppress(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-10*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Huddles["h1"].AppendUtterance(
			"jefferey", "Jefferey", "I'll be on my way, then.", now.Add(-5*time.Second), 0)
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: now,
		}, r)
	})
	if got := getSpokes(); len(got) != 1 {
		t.Fatalf("got %d Spoke events, want 1 (the LEAVER spoke, not the keeper)", len(got))
	}
}

// TestHandleHuddleLeft_SuppressionIsPerKeeper — two keepers share the room and
// only one of them spoke. The silent one still bids the customer farewell.
func TestHandleHuddleLeft_SuppressionIsPerKeeper(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-10*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Actors["bram"] = &sim.Actor{
			ID: "bram", DisplayName: "Bram", Kind: sim.KindNPCShared, State: sim.StateIdle,
			CurrentHuddleID: "h1", WorkStructureID: "tavern", InsideStructureID: "tavern",
			BusinessownerState: &sim.BusinessownerState{Flavor: "reserved"},
			RecentActions:      sim.NewRingBuffer[sim.Action](4),
		}
		world.Huddles["h1"].Members["bram"] = struct{}{}
		world.Huddles["h1"].AppendUtterance(
			"hannah", "Hannah", "Safe travels to you.", now.Add(-15*time.Second), 0)
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah", "bram"}, At: now,
		}, r)
	})
	spokes := getSpokes()
	if len(spokes) != 1 {
		t.Fatalf("got %d Spoke events, want 1 (hannah suppressed, bram silent so bram speaks)", len(spokes))
	}
	if spokes[0].SpeakerID != "bram" {
		t.Errorf("speaker = %q, want bram (hannah already said her goodbye)", spokes[0].SpeakerID)
	}
}

// TestHandleHuddleLeft_EvictedGoodbye_FiresFarewell — LLM-535, documenting the
// known limit of using the prompt ring as the signal.
//
// The ring keeps MaxRecentUtterancesPerHuddle lines. A keeper's goodbye buried
// under that many later lines is invisible to the gate, and the duplicate
// farewell this change prevents becomes possible again. It needs a genuinely
// busy room to happen (eight turns inside the two-minute window, before the
// customer's departure lands), and it fails toward the pre-LLM-535 behavior
// rather than toward silencing a keeper. Pinned so the limit is a documented
// property rather than a surprise.
func TestHandleHuddleLeft_EvictedGoodbye_FiresFarewell(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-10*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		h := world.Huddles["h1"]
		h.AppendUtterance("hannah", "Hannah", "Safe travels to you.", now.Add(-60*time.Second), 0)
		for i := 0; i < sim.MaxRecentUtterancesPerHuddle; i++ {
			h.AppendUtterance("jefferey", "Jefferey", "and another thing", now.Add(-30*time.Second), 0)
		}
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "h1", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: now,
		}, r)
	})
	if got := getSpokes(); len(got) != 1 {
		t.Fatalf("got %d Spoke events, want 1 (goodbye evicted from the ring — gate fails open by design)", len(got))
	}
}

// TestHandleHuddleLeft_StaleHuddleID_FiresFarewell — a HuddleLeft naming a
// huddle the world doesn't hold reads as no-recent-speech, so the farewell
// emits. Fail-open, same direction as every other unknown in the gate.
func TestHandleHuddleLeft_StaleHuddleID_FiresFarewell(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()
	getSpokes := observeSpokes(t, w)

	now := time.Now().UTC()
	seedBusinessownerHuddle(t, w, now.Add(-10*time.Minute))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		world.Huddles["h1"].AppendUtterance(
			"hannah", "Hannah", "Safe travels to you.", now.Add(-15*time.Second), 0)
	})

	r := rand.New(rand.NewSource(1))
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		handleHuddleLeftBusinessowner(world, &sim.HuddleLeft{
			ActorID: "jefferey", HuddleID: "gone", StructureID: "tavern",
			RemainingMembers: []sim.ActorID{"hannah"}, At: now,
		}, r)
	})
	if got := getSpokes(); len(got) != 1 {
		t.Fatalf("got %d Spoke events, want 1 (unknown huddle reads as no model speech)", len(got))
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

// TestHuddlePeers_FiltersEmptyAndSpeakerIDs locks the defensive
// behavior code_review R1 requested: huddlePeers must never return ""
// or the speaker, even when a malformed empty-ID actor exists in the
// world. Matches the buildBusinessownerRecipients filter posture.
func TestHuddlePeers_FiltersEmptyAndSpeakerIDs(t *testing.T) {
	w, cleanup := buildBusinessownerCascadeWorld(t)
	defer cleanup()

	var got []sim.ActorID
	invokeBusinessownerOnWorld(t, w, func(world *sim.World) {
		// Seed a malformed empty-ID actor in the same huddle the seller
		// is in. Production invariants forbid this, but the filter
		// defends against the case anyway.
		world.Actors[""] = &sim.Actor{
			ID:              "",
			DisplayName:     "(malformed)",
			Kind:            sim.KindNPCShared,
			CurrentHuddleID: "h1",
		}
		got = huddlePeers(world, "h1", "hannah")
	})
	for _, id := range got {
		if id == "" {
			t.Errorf("huddlePeers returned empty ID: %v", got)
		}
		if id == "hannah" {
			t.Errorf("huddlePeers returned speaker ID: %v", got)
		}
	}
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
