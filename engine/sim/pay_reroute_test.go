package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pay_reroute_test.go — ZBBS-HOME-460. When the model names a vendor's
// WORKPLACE (a building) instead of the vendor — the buy cues model "where to
// buy" as a structure ("buy from Ellis Farm (structure_id: …)"), so the weak
// shared-NPC model passes the place name where a co-present person is wanted —
// Pay and PayWithItem reroute to the worker rather than rejecting. These tests
// cover the shared findHuddlePeerByWorkplaceName resolver in all its branches
// (single match, no match, ambiguous, owner tiebreak) via the bare Pay
// command, plus the PayWithItem wiring that also surfaces the resolved name
// for the harness echo.

type rerouteSpec struct {
	id          sim.ActorID
	displayName string
	huddleID    sim.HuddleID
	coins       int
	work        sim.StructureID
	owner       bool
}

// buildRerouteWorld seeds the actors and the structures they work at. When
// sceneID != "", it also attaches a scene observing the huddle so
// PayWithItem's scene gate passes (bare Pay needs no scene). Structures take
// their DisplayName from structNames.
func buildRerouteWorld(t *testing.T, huddleID sim.HuddleID, sceneID sim.SceneID, structNames map[sim.StructureID]string, specs ...rerouteSpec) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())

	structures := make(map[sim.StructureID]*sim.Structure, len(structNames))
	for id, name := range structNames {
		structures[id] = &sim.Structure{ID: id, DisplayName: name}
	}
	handles.Structures.Seed(structures)

	now := time.Now().UTC()
	seed := make(map[sim.ActorID]*sim.Actor, len(specs))
	members := make(map[sim.ActorID]struct{}, len(specs))
	for _, s := range specs {
		a := &sim.Actor{
			ID:              s.id,
			DisplayName:     s.displayName,
			Kind:            sim.KindNPCShared,
			State:           sim.StateIdle,
			CurrentHuddleID: s.huddleID,
			Coins:           s.coins,
			WorkStructureID: s.work,
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		}
		if s.owner {
			a.BusinessownerState = &sim.BusinessownerState{}
		}
		seed[s.id] = a
		if s.huddleID == huddleID && huddleID != "" {
			members[s.id] = struct{}{}
		}
	}
	handles.Actors.Seed(seed)

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

	if sceneID != "" && huddleID != "" {
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Huddles[huddleID] = &sim.Huddle{ID: huddleID, Members: members, StartedAt: now}
			world.Scenes[sceneID] = &sim.Scene{
				ID:       sceneID,
				OriginAt: now,
				Bound:    sim.NewUnboundedBound(),
				Huddles:  map[sim.HuddleID]struct{}{huddleID: {}},
			}
			sim.RebuildIndicesForTest(world)
			return nil, nil
		}}); err != nil {
			cancel()
			<-done
			t.Fatalf("seed scene+huddle: %v", err)
		}
	}
	return w, func() { cancel(); <-done }
}

// TestPay_RerouteWorkplaceNameToWorker: the canonical "pay Ellis Farm" case.
// John names the farm; Elizabeth works it and is co-present; the payment
// routes to her instead of rejecting.
func TestPay_RerouteWorkplaceNameToWorker(t *testing.T) {
	w, stop := buildRerouteWorld(t, "h1", "",
		map[sim.StructureID]string{"farm": "Ellis Farm"},
		rerouteSpec{id: "john", displayName: "John Ellis", huddleID: "h1", coins: 50},
		rerouteSpec{id: "liz", displayName: "Elizabeth Ellis", huddleID: "h1", work: "farm"},
	)
	defer stop()

	captured := capturePaid(t, w)
	at := time.Now().UTC()
	if _, err := w.Send(sim.Pay("john", "Ellis Farm", 10, "meat", at)); err != nil {
		t.Fatalf("rerouted Pay should succeed, got: %v", err)
	}

	if len(*captured) != 1 {
		t.Fatalf("Paid events = %d, want 1", len(*captured))
	}
	if got := (*captured)[0]; got.SellerID != "liz" {
		t.Errorf("rerouted Paid.SellerID = %q, want liz (the farm's worker)", got.SellerID)
	}
	snap := w.Published()
	if got, want := snap.Actors["john"].Coins, 40; got != want {
		t.Errorf("john.Coins = %d, want %d", got, want)
	}
	if got, want := snap.Actors["liz"].Coins, 10; got != want {
		t.Errorf("liz.Coins = %d, want %d", got, want)
	}
}

// TestPay_RerouteNoWorkerPresentRejects: a building is named but nobody
// present works there (the keeper drifted off) — there is no one to route to,
// so the original person-not-found reject stands. This is the safety property:
// the reroute never targets a bystander just because a place was named.
func TestPay_RerouteNoWorkerPresentRejects(t *testing.T) {
	w, stop := buildRerouteWorld(t, "h1", "",
		map[sim.StructureID]string{"farm": "Ellis Farm"},
		rerouteSpec{id: "john", displayName: "John Ellis", huddleID: "h1", coins: 50},
		// Hannah is co-present but works nowhere — she must NOT receive the pay.
		rerouteSpec{id: "hannah", displayName: "Hannah Boggs", huddleID: "h1"},
	)
	defer stop()

	_, err := w.Send(sim.Pay("john", "Ellis Farm", 10, "meat", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay to a building with no co-present worker should reject, got nil")
	}
	if !strings.Contains(err.Error(), "no one named") {
		t.Errorf("want person-not-found reject, got %v", err)
	}
	if snap := w.Published(); snap.Actors["hannah"].Coins != 0 {
		t.Errorf("bystander hannah was paid (%d coins) — reroute must not target a non-worker", snap.Actors["hannah"].Coins)
	}
}

// TestPay_RerouteAmbiguousWorkersReject: two co-present peers work at
// different structures that share the name "Ellis Farm", and neither owns one
// — a money transfer must not pick a recipient non-deterministically.
func TestPay_RerouteAmbiguousWorkersReject(t *testing.T) {
	w, stop := buildRerouteWorld(t, "h1", "",
		map[sim.StructureID]string{"farm_a": "Ellis Farm", "farm_b": "Ellis Farm"},
		rerouteSpec{id: "john", displayName: "John Ellis", huddleID: "h1", coins: 50},
		rerouteSpec{id: "liz", displayName: "Elizabeth Ellis", huddleID: "h1", work: "farm_a"},
		rerouteSpec{id: "amos", displayName: "Amos Ellis", huddleID: "h1", work: "farm_b"},
	)
	defer stop()

	_, err := w.Send(sim.Pay("john", "Ellis Farm", 10, "meat", time.Now().UTC()))
	if err == nil {
		t.Fatal("ambiguous workplace reroute should reject, got nil")
	}
	if !strings.Contains(err.Error(), "works at") {
		t.Errorf("want ambiguous-workplace reject, got %v", err)
	}
}

// TestPay_RerouteOwnerTiebreak: an owner and a hired hand work the SAME shop
// and are both present — the proprietor is the seller, so the tie resolves to
// the owner rather than rejecting as ambiguous.
func TestPay_RerouteOwnerTiebreak(t *testing.T) {
	w, stop := buildRerouteWorld(t, "h1", "",
		map[sim.StructureID]string{"farm": "Ellis Farm"},
		rerouteSpec{id: "john", displayName: "John Ellis", huddleID: "h1", coins: 50},
		rerouteSpec{id: "liz", displayName: "Elizabeth Ellis", huddleID: "h1", work: "farm", owner: true},
		rerouteSpec{id: "amos", displayName: "Amos Ellis", huddleID: "h1", work: "farm"},
	)
	defer stop()

	captured := capturePaid(t, w)
	if _, err := w.Send(sim.Pay("john", "Ellis Farm", 10, "meat", time.Now().UTC())); err != nil {
		t.Fatalf("owner-tiebreak reroute should succeed, got: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("Paid events = %d, want 1", len(*captured))
	}
	if got := (*captured)[0]; got.SellerID != "liz" {
		t.Errorf("rerouted to %q, want liz (the owner)", got.SellerID)
	}
}

// TestPay_RerouteDuplicateStructureNamesReject: two co-present peers work at
// DIFFERENT structures that share the display name "Ellis Farm" — and one of
// them even owns their building. A shared name is not a positive id of one
// place, so ownership must NOT break the tie; the reroute rejects.
func TestPay_RerouteDuplicateStructureNamesReject(t *testing.T) {
	w, stop := buildRerouteWorld(t, "h1", "",
		map[sim.StructureID]string{"farm_a": "Ellis Farm", "farm_b": "Ellis Farm"},
		rerouteSpec{id: "john", displayName: "John Ellis", huddleID: "h1", coins: 50},
		rerouteSpec{id: "liz", displayName: "Elizabeth Ellis", huddleID: "h1", work: "farm_a", owner: true},
		rerouteSpec{id: "amos", displayName: "Amos Ellis", huddleID: "h1", work: "farm_b"},
	)
	defer stop()

	_, err := w.Send(sim.Pay("john", "Ellis Farm", 10, "meat", time.Now().UTC()))
	if err == nil {
		t.Fatal("duplicate-structure-name reroute should reject even with an owner present, got nil")
	}
	if !strings.Contains(err.Error(), "works at") {
		t.Errorf("want ambiguous-workplace reject, got %v", err)
	}
}

// TestPayWithItem_RerouteWorkplaceNameToWorker: the dominant live case — a
// restock buy where the cue named the farm. The offer mints against the
// co-present worker, and the result carries her name so the harness echo can
// say the offer stands before Elizabeth Ellis, not "before Ellis Farm".
func TestPayWithItem_RerouteWorkplaceNameToWorker(t *testing.T) {
	w, stop := buildRerouteWorld(t, "h1", "sc1",
		map[sim.StructureID]string{"farm": "Ellis Farm"},
		rerouteSpec{id: "john", displayName: "John Ellis", huddleID: "h1", coins: 50},
		rerouteSpec{id: "liz", displayName: "Elizabeth Ellis", huddleID: "h1", work: "farm"},
	)
	defer stop()

	res, err := w.Send(sim.PayWithItem("john", "Ellis Farm", "bread", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("rerouted PayWithItem should succeed, got: %v", err)
	}
	result, ok := res.(sim.PayWithItemResult)
	if !ok {
		t.Fatalf("result type = %T, want PayWithItemResult", res)
	}
	if result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
	if result.ReroutedSellerName != "Elizabeth Ellis" {
		t.Errorf("ReroutedSellerName = %q, want %q", result.ReroutedSellerName, "Elizabeth Ellis")
	}
	// The pending entry is staked against the worker, not the building.
	ledger := readPayLedger(t, w)
	if len(ledger) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(ledger))
	}
	for _, e := range ledger {
		if e.SellerID != "liz" {
			t.Errorf("offer SellerID = %q, want liz", e.SellerID)
		}
	}
}
