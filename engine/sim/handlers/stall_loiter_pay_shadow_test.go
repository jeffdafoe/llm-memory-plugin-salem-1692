package handlers_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// stall_loiter_pay_shadow_test.go — repro for the live "stuck at Ezekiel's
// stall" bug (2026-07-12): Prudence and Josiah both loitered at the Blacksmith's
// outdoor pin to buy nails from Ezekiel, who was inside and open. The perception
// render told them "Ezekiel Crane is here with you and sells nails — buy it now"
// (a structure-scope co-presence cue), but every pay_with_item was rejected, so
// Prudence looped to budget_forced each tick forever and Josiah confabulated
// "Ezekiel must be about his morning chores".
//
// Root cause reproduced here: an OUTDOOR peer huddle between two co-loiterers
// (the arrival-encounter cascade groups nearby outdoor actors via
// StartOutdoorHuddle — arrival_encounter.go) SHADOWS the stall. pay_with_item
// bootstraps a huddle via EnsureColocatedHuddle, which is a no-op when the actor
// is already huddled — so a peer-huddled buyer never migrates into the keeper's
// structure huddle and the seller can't be resolved.
//
// This is independent of LLM-359 (the "no talk through a closed door" gate): the
// shop is OPEN here (keeper present + awake, keeperPresentAt true), which is
// exactly why the lone-buyer control below succeeds. The failure is the peer
// huddle, not the shop-open gate.

// buildStallLoiterWorld seeds a Blacksmith structure with a resolvable loiter pin
// (a named vobj at the anchor tile, zero loiter offsets), a keeper working INSIDE
// it with stock, and two would-be buyers standing OUTSIDE at the pin.
func buildStallLoiterWorld(t *testing.T) (w *sim.World, pin sim.TilePos, stop func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"smithy": {ID: "smithy", DisplayName: "Blacksmith"},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg-asset": {ID: "bldg-asset", Category: "structure"},
	})
	// Shared-Identity Bridge (WORK-342): the structure needs a backing vobj so
	// the structure scene resolves; a DisplayName + zero loiter offsets make its
	// loiter pin == its anchor tile, so a customer standing there resolves to it.
	z := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"smithy": {
			ID: "smithy", AssetID: "bldg-asset", DisplayName: "Blacksmith",
			Pos:           sim.WorldPos{X: 160, Y: 160},
			LoiterOffsetX: &z, LoiterOffsetY: &z,
		},
	})
	pin = sim.WorldPos{X: 160, Y: 160}.Tile()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		// Ezekiel — keeper working INSIDE the open Blacksmith, with nails.
		"keeper": {
			ID: "keeper", DisplayName: "Ezekiel", Kind: sim.KindNPCStateful,
			State:             sim.StateIdle,
			Inventory:         map[sim.ItemKind]int{"bread": 5},
			InsideStructureID: "smithy",
			WorkStructureID:   "smithy",
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
		},
		// Prudence — OUTSIDE at the loiter pin, wanting to buy.
		"buyer1": {
			ID: "buyer1", DisplayName: "Prudence", Kind: sim.KindNPCStateful,
			State:         sim.StateIdle,
			Coins:         50,
			Pos:           pin,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		// Josiah — OUTSIDE at the same pin.
		"buyer2": {
			ID: "buyer2", DisplayName: "Josiah", Kind: sim.KindNPCStateful,
			State:         sim.StateIdle,
			Coins:         50,
			Pos:           pin,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
	})
	world, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { world.Run(ctx); close(done) }()
	if _, err := world.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		handlers.RegisterPayWithItemHandlers(wd)
		return nil, nil
	}}); err != nil {
		cancel()
		<-done
		t.Fatalf("setup: %v", err)
	}
	return world, pin, func() { cancel(); <-done }
}

func huddleOfActor(t *testing.T, w *sim.World, id sim.ActorID) sim.HuddleID {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].CurrentHuddleID, nil
	}})
	if err != nil {
		t.Fatalf("read huddle for %q: %v", id, err)
	}
	return v.(sim.HuddleID)
}

// TestStallLoiter_LonePayerReachesKeeper is the control: a single loiterer at the
// open stall's pin CAN pay the keeper inside. pay_with_item bootstraps the
// cross-threshold structure huddle (buyer1 was not previously huddled), the
// keeper is pulled in, the offer lands Pending. This is the behavior the buggy
// case below breaks — and it proves the shop-open scope (LLM-359) works here.
func TestStallLoiter_LonePayerReachesKeeper(t *testing.T) {
	w, _, stop := buildStallLoiterWorld(t)
	defer stop()

	cmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "buyer1", AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{
			Seller: "Ezekiel", Item: "bread", Qty: 1, Amount: 4, ConsumeNow: false,
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("lone loiterer's pay to the open stall was rejected: %v", err)
	}
	if got := res.(sim.PayWithItemResult).State; got != sim.PayLedgerStatePending {
		t.Errorf("offer State = %q, want pending", got)
	}
	if bh, kh := huddleOfActor(t, w, "buyer1"), huddleOfActor(t, w, "keeper"); bh == "" || bh != kh {
		t.Errorf("buyer1/keeper not co-huddled after pay: buyer1=%q keeper=%q", bh, kh)
	}
}

// TestStallLoiter_ForeignHuddleBlocksKeeperResolution is the invariant that made
// the shadow fatal: a buyer already in a huddle that does NOT contain the seller
// cannot resolve them. buyer1 and buyer2 are put in an OUTDOOR peer huddle (as the
// arrival-encounter cascade used to form at a stall — before LLM-375 stopped it),
// so buyer1's pay_with_item to the inside keeper is REJECTED — EnsureColocatedHuddle
// no-ops (already huddled), the keeper is never joined, and the seller can't be
// found. The shop is open throughout, isolating the cause to the huddle rather
// than the LLM-359 shop-open gate. LLM-375 prevents this huddle from forming at a
// stall in the first place; this test guards the resolution gate that backstops it.
func TestStallLoiter_ForeignHuddleBlocksKeeperResolution(t *testing.T) {
	w, pin, stop := buildStallLoiterWorld(t)
	defer stop()

	// Form the outdoor peer huddle between the two co-loiterers — the same
	// command arrival_encounter.go fires when the second buyer walks up.
	if _, err := w.Send(sim.StartOutdoorHuddle(
		[]sim.ActorID{"buyer1", "buyer2"},
		sim.Position{X: pin.X, Y: pin.Y}, 3, nil, time.Unix(0, 0).UTC(),
	)); err != nil {
		t.Fatalf("StartOutdoorHuddle (setup): %v", err)
	}
	peer1, peer2 := huddleOfActor(t, w, "buyer1"), huddleOfActor(t, w, "buyer2")
	if peer1 == "" || peer1 != peer2 {
		t.Fatalf("setup: buyers not in a shared peer huddle (buyer1=%q buyer2=%q)", peer1, peer2)
	}
	if kh := huddleOfActor(t, w, "keeper"); kh != "" {
		t.Fatalf("setup: keeper unexpectedly pulled into the outdoor peer huddle (%q)", kh)
	}

	cmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "buyer1", AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{
			Seller: "Ezekiel", Item: "bread", Qty: 1, Amount: 4, ConsumeNow: false,
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem: %v", err)
	}
	_, payErr := w.Send(cmd)
	if payErr == nil {
		t.Fatal("pay_with_item resolved a seller who is not in the buyer's huddle; the shared-huddle gate is gone")
	}
	if !strings.Contains(payErr.Error(), "in this conversation") {
		t.Errorf("want a seller-not-in-conversation reject, got: %v", payErr)
	}
	// The defining symptom: buyer1 is still in the peer huddle, keeper still out.
	if bh := huddleOfActor(t, w, "buyer1"); bh != peer1 {
		t.Errorf("buyer1 huddle changed from the peer huddle %q to %q", peer1, bh)
	}
	if kh := huddleOfActor(t, w, "keeper"); kh == peer1 {
		t.Errorf("keeper joined the peer huddle %q (unexpected)", kh)
	}
}

// TestStallLoiter_TwoBuyersBothReachKeeper is the fix's steady state (LLM-375):
// with no shadowing peer huddle, two customers arriving at the open stall both
// trade with the keeper. buyer1 buys (forming the keeper's structure huddle);
// buyer2 then buys and JOINS that same huddle via find-or-create rather than
// being stranded — all three end in one huddle. This is what the arrival-encounter
// fix enables (see arrival_encounter_stall_shadow_test.go, which stops the peer
// huddle that used to intercept them here).
func TestStallLoiter_TwoBuyersBothReachKeeper(t *testing.T) {
	w, _, stop := buildStallLoiterWorld(t)
	defer stop()

	buy := func(buyer sim.ActorID, attempt sim.TickAttemptID) {
		t.Helper()
		cmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
			ActorID: buyer, AttemptID: attempt,
			Args: handlers.PayWithItemArgs{
				Seller: "Ezekiel", Item: "bread", Qty: 1, Amount: 4, ConsumeNow: false,
			},
		})
		if err != nil {
			t.Fatalf("HandlePayWithItem(%s): %v", buyer, err)
		}
		if _, err := w.Send(cmd); err != nil {
			t.Fatalf("%s could not reach the keeper at the open stall: %v", buyer, err)
		}
	}
	buy("buyer1", "tk-1")
	buy("buyer2", "tk-2")

	kh := huddleOfActor(t, w, "keeper")
	if kh == "" {
		t.Fatal("keeper not huddled after two buyers traded")
	}
	if b1, b2 := huddleOfActor(t, w, "buyer1"), huddleOfActor(t, w, "buyer2"); b1 != kh || b2 != kh {
		t.Errorf("both customers should share the keeper's huddle %q, got buyer1=%q buyer2=%q", kh, b1, b2)
	}
}

// TestStallLoiter_KeeperSceneQuoteReachesCustomer proves the SELL side works at
// the stall too, not just the buy: once a customer has formed the structure
// huddle with the keeper, the keeper's own scene_quote resolves the co-huddled
// customer. scene_quote rides the same withHuddleBootstrap gate as pay_with_item,
// so this stands in for the whole seller-side surface (quote/accept/counter).
func TestStallLoiter_KeeperSceneQuoteReachesCustomer(t *testing.T) {
	w, _, stop := buildStallLoiterWorld(t)
	defer stop()

	// A customer buys first — forms the keeper's structure huddle across the pin.
	cmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "buyer1", AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{
			Seller: "Ezekiel", Item: "bread", Qty: 1, Amount: 4, ConsumeNow: false,
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem: %v", err)
	}
	if _, err := w.Send(cmd); err != nil {
		t.Fatalf("buyer1 pay rejected: %v", err)
	}

	quote, err := handlers.HandleSceneQuote(handlers.HandlerInput{
		ActorID: "keeper", AttemptID: "tk-2",
		Args: handlers.SceneQuoteArgs{
			Lines:      []handlers.SceneQuoteLineArg{{ItemKind: "bread", Qty: 1}},
			Amount:     3,
			ConsumeNow: false,
		},
	})
	if err != nil {
		t.Fatalf("HandleSceneQuote: %v", err)
	}
	if _, err := w.Send(quote); err != nil {
		t.Fatalf("keeper's scene_quote rejected at the open stall (sell side broken): %v", err)
	}
}
