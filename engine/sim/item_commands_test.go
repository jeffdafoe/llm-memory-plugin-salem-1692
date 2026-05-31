package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// item_commands_test.go — sim-level coverage of the Consume Command's world-
// state validation, inventory decrement, need apply, dwell-credit stamp, and
// ItemConsumed event emission, plus direct unit tests on transferItem and
// resolveItemKind (exposed via export_test). The dwell-pin resolution is
// covered by loiter_resolve_test.go (ResolveLoiteringObject).
//
// Handler-level static validation (decode + control-char + trim) lives in
// handlers/consume_test.go.

// consumeActorSpec — minimal Actor seed for Consume tests. Adds Inventory +
// Needs + CurrentX/Y + InsideStructureID for the consume-related fields the
// pay test fixture doesn't need.
type consumeActorSpec struct {
	id           sim.ActorID
	displayName  string
	inventory    map[sim.ItemKind]int
	needs        map[sim.NeedKey]int
	x, y         int
	moveInFlight bool
}

// consumeObjectSpec — VillageObject seed used as a dwell pin. Position is in
// TILE coords (same frame as consumeActorSpec): the object's loiter pin lands
// exactly on this tile, so an actor standing on it (Chebyshev <= 1) pins.
type consumeObjectSpec struct {
	id   sim.VillageObjectID
	x, y int
}

func buildConsumeTestWorld(t *testing.T, actors []consumeActorSpec, objects []consumeObjectSpec) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())

	now := time.Now().UTC()
	actorSeed := make(map[sim.ActorID]*sim.Actor, len(actors))
	for _, s := range actors {
		a := &sim.Actor{
			ID:               s.id,
			DisplayName:      s.displayName,
			Kind:             sim.KindNPCShared,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			Inventory:        s.inventory,
			Needs:            s.needs,
			Pos:              sim.TilePos{X: s.x, Y: s.y},
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		}
		if s.moveInFlight {
			a.MoveIntent = &sim.MoveIntent{AttemptID: sim.MovementAttemptID(1)}
		}
		actorSeed[s.id] = a
	}
	handles.Actors.Seed(actorSeed)

	if len(objects) > 0 {
		// resolveLoiteringObject (the dwell-pin resolver) only considers NAMED
		// objects with a resolvable asset, and measures Chebyshev tiles to the
		// loiter pin. Seed a shared asset and give each object a name + a zero
		// loiter offset, so its pin lands on its anchor tile (TileToWorld
		// round-trips through WorldToTile). The actor pins by standing there.
		handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"consume-pin": {ID: "consume-pin"}})
		zero := 0
		objSeed := make(map[sim.VillageObjectID]*sim.VillageObject, len(objects))
		for _, o := range objects {
			objSeed[o.id] = &sim.VillageObject{
				ID:            o.id,
				DisplayName:   string(o.id),
				AssetID:       "consume-pin",
				Pos:           sim.TileToWorld(sim.GridPoint{X: o.x, Y: o.y}),
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			}
		}
		handles.VillageObjects.Seed(objSeed)
	}

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

// captureItemConsumed registers a subscriber recording every ItemConsumed
// event for inspection. Same pattern as capturePaid.
func captureItemConsumed(t *testing.T, w *sim.World) *[]sim.ItemConsumed {
	t.Helper()
	var out []sim.ItemConsumed
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if e, ok := evt.(*sim.ItemConsumed); ok {
				out = append(out, *e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("captureItemConsumed subscribe: %v", err)
	}
	return &out
}

// liveActorView is the per-actor projection unit tests read for fields the
// published Snapshot doesn't carry (Inventory raw counts, DwellCredits).
// Built on the world goroutine via readLiveActor so map access is safe.
type liveActorView struct {
	Inventory    map[sim.ItemKind]int
	Needs        map[sim.NeedKey]int
	DwellCredits map[sim.DwellCreditKey]sim.DwellCredit
}

// readLiveActor sends a Command that copies the live actor's inventory,
// needs, and dwell credits into a serializable view. Returns an empty view
// (zero-len maps) if the actor isn't in the world.
func readLiveActor(t *testing.T, w *sim.World, actorID sim.ActorID) liveActorView {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		view := liveActorView{
			Inventory:    map[sim.ItemKind]int{},
			Needs:        map[sim.NeedKey]int{},
			DwellCredits: map[sim.DwellCreditKey]sim.DwellCredit{},
		}
		actor := world.Actors[actorID]
		if actor == nil {
			return view, nil
		}
		for k, v := range actor.Inventory {
			view.Inventory[k] = v
		}
		for k, v := range actor.Needs {
			view.Needs[k] = v
		}
		for k, v := range actor.DwellCredits {
			if v == nil {
				continue
			}
			view.DwellCredits[k] = *v
		}
		return view, nil
	}})
	if err != nil {
		t.Fatalf("readLiveActor: %v", err)
	}
	return res.(liveActorView)
}

// ---- Consume Command tests ----

// TestConsume_HappyPath_Immediate: actor consumes 1 bread (Immediate=8 on
// hunger), hunger drops from 10 to 2, inventory decrements with delete-on-
// zero, ItemConsumed event emits with Applied[hunger]=8.
func TestConsume_HappyPath_Immediate(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"bread": 1},
			needs:       map[sim.NeedKey]int{"hunger": 10},
		}},
		nil,
	)
	defer stop()

	captured := captureItemConsumed(t, w)
	at := time.Now().UTC()
	if _, err := w.Send(sim.Consume("hannah", "bread", 1, at)); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	if len(*captured) != 1 {
		t.Fatalf("ItemConsumed events = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if got.ActorID != "hannah" {
		t.Errorf("ItemConsumed.ActorID = %q, want hannah", got.ActorID)
	}
	if got.Kind != "bread" {
		t.Errorf("ItemConsumed.Kind = %q, want bread", got.Kind)
	}
	if got.Qty != 1 {
		t.Errorf("ItemConsumed.Qty = %d, want 1", got.Qty)
	}
	if got, want := got.Applied["hunger"], 8; got != want {
		t.Errorf("ItemConsumed.Applied[hunger] = %d, want %d", got, want)
	}
	if !got.At.Equal(at) {
		t.Errorf("ItemConsumed.At = %v, want %v", got.At, at)
	}

	view := readLiveActor(t, w, "hannah")
	if got, want := view.Needs["hunger"], 2; got != want {
		t.Errorf("hannah.Needs[hunger] post-consume = %d, want %d", got, want)
	}
	// delete-on-zero invariant: bread entry should be gone, not 0.
	if v, present := view.Inventory["bread"]; present {
		t.Errorf("hannah.Inventory[bread] still present after consume-to-zero (value %d); want deleted", v)
	}
}

// TestConsume_QtyMultiplier: 3 ales consumes hunger by Immediate*qty = 2*3 = 6
// and thirst by 4*3 = 12, clamped at 0 by ClampNeed (thirst was 10 → 0).
func TestConsume_QtyMultiplier(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"ale": 5},
			needs:       map[sim.NeedKey]int{"hunger": 10, "thirst": 10},
		}},
		nil,
	)
	defer stop()

	captured := captureItemConsumed(t, w)
	if _, err := w.Send(sim.Consume("hannah", "ale", 3, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	got := (*captured)[0]
	if got, want := got.Applied["hunger"], 6; got != want {
		t.Errorf("Applied[hunger] = %d, want %d", got, want)
	}
	// Pre-clamp thirst drop would be 12; clamps at 0 → actual drop = 10.
	if got, want := got.Applied["thirst"], 10; got != want {
		t.Errorf("Applied[thirst] = %d, want %d (clamped)", got, want)
	}

	view := readLiveActor(t, w, "hannah")
	if got, want := view.Inventory["ale"], 2; got != want {
		t.Errorf("hannah.Inventory[ale] post-consume = %d, want %d", got, want)
	}
	if got := view.Needs["thirst"]; got != 0 {
		t.Errorf("hannah.Needs[thirst] post-consume = %d, want 0 (clamp)", got)
	}
}

// TestConsume_NotHungry_AppliedEmpty: consume an item where the actor's
// matching need is already 0. Applied is empty (no needs moved), inventory
// decrements regardless (the consume succeeded), event still emits for
// audit/replay.
func TestConsume_NotHungry_AppliedEmpty(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"bread": 1},
			needs:       map[sim.NeedKey]int{"hunger": 0},
		}},
		nil,
	)
	defer stop()

	captured := captureItemConsumed(t, w)
	if _, err := w.Send(sim.Consume("hannah", "bread", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	got := (*captured)[0]
	if len(got.Applied) != 0 {
		t.Errorf("Applied = %v, want empty (no needs moved)", got.Applied)
	}
	view := readLiveActor(t, w, "hannah")
	if v, present := view.Inventory["bread"]; present {
		t.Errorf("bread still in inventory: %d (delete-on-zero failed)", v)
	}
}

// TestConsume_CaseInsensitiveResolve: model emits "Ale" (capitalized),
// resolves to canonical "ale".
func TestConsume_CaseInsensitiveResolve(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"ale": 1},
			needs:       map[sim.NeedKey]int{"thirst": 10},
		}},
		nil,
	)
	defer stop()

	captured := captureItemConsumed(t, w)
	if _, err := w.Send(sim.Consume("hannah", "  ALE  ", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	got := (*captured)[0]
	if got.Kind != "ale" {
		t.Errorf("ItemConsumed.Kind = %q, want canonical 'ale'", got.Kind)
	}
}

// TestConsume_DwellPin_NearbyObject: actor consumes stew while standing at a
// named village object's loiter pin. Dwell credit stamps pinned to that
// object, source=item, RemainingTicks=8.
func TestConsume_DwellPin_NearbyObject(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"stew": 1},
			needs:       map[sim.NeedKey]int{"hunger": 20},
			x:           100, y: 100,
		}},
		[]consumeObjectSpec{{
			id: "tavern", x: 100, y: 100, // pin on the actor's tile → attributed
		}},
	)
	defer stop()

	at := time.Now().UTC()
	if _, err := w.Send(sim.Consume("hannah", "stew", 1, at)); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	view := readLiveActor(t, w, "hannah")
	if got, want := len(view.DwellCredits), 1; got != want {
		t.Fatalf("DwellCredits count = %d, want %d", got, want)
	}
	key := sim.DwellCreditKey{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}
	credit, ok := view.DwellCredits[key]
	if !ok {
		t.Fatalf("DwellCredits missing key %+v; have %v", key, view.DwellCredits)
	}
	if credit.DwellPeriodMinutes != 2 {
		t.Errorf("credit.DwellPeriodMinutes = %d, want 2", credit.DwellPeriodMinutes)
	}
	if credit.DwellDelta != -1 {
		t.Errorf("credit.DwellDelta = %d, want -1 (negated from +1 magnitude)", credit.DwellDelta)
	}
	if credit.RemainingTicks == nil || *credit.RemainingTicks != 8 {
		got := -1
		if credit.RemainingTicks != nil {
			got = *credit.RemainingTicks
		}
		t.Errorf("credit.RemainingTicks = %d, want 8", got)
	}
	if !credit.LastCreditedAt.Equal(at) {
		t.Errorf("credit.LastCreditedAt = %v, want %v", credit.LastCreditedAt, at)
	}
	// Immediate also applied: hunger 20 → 16.
	if got, want := view.Needs["hunger"], 16; got != want {
		t.Errorf("hunger post-immediate = %d, want %d", got, want)
	}
}

// TestConsume_DwellPin_NoNearbyObject: actor consumes stew with no village
// object within tolerance. Immediate satisfaction still applies; no dwell
// credit stamped (silent skip, matches v1 eat-while-walking behavior).
func TestConsume_DwellPin_NoNearbyObject(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"stew": 1},
			needs:       map[sim.NeedKey]int{"hunger": 20},
			x:           100, y: 100,
		}},
		[]consumeObjectSpec{{
			id: "far_tavern", x: 1000, y: 1000, // far outside the attribution radius
		}},
	)
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	view := readLiveActor(t, w, "hannah")
	if got := len(view.DwellCredits); got != 0 {
		t.Errorf("DwellCredits count = %d, want 0 (no nearby pin)", got)
	}
	// Immediate still applied.
	if got, want := view.Needs["hunger"], 16; got != want {
		t.Errorf("hunger post-immediate = %d, want %d", got, want)
	}
}

// TestConsume_UnknownKind: model emits an item name not in w.ItemKinds.
func TestConsume_UnknownKind(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"bread": 1},
		}},
		nil,
	)
	defer stop()

	captured := captureItemConsumed(t, w)
	_, err := w.Send(sim.Consume("hannah", "moonbeam", 1, time.Now().UTC()))
	if err == nil {
		t.Fatal("Consume: want error for unknown kind, got nil")
	}
	if !errors.Is(err, sim.ErrUnknownItemKind) {
		t.Errorf("want ErrUnknownItemKind; got %v", err)
	}
	if !strings.Contains(err.Error(), `"moonbeam"`) {
		t.Errorf("error should echo the unknown kind: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("ItemConsumed emitted on reject: %v", *captured)
	}
}

// TestConsume_NotConsumable: model tries to consume wheat (material with no
// satisfactions).
func TestConsume_NotConsumable(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"wheat": 5},
		}},
		nil,
	)
	defer stop()

	captured := captureItemConsumed(t, w)
	_, err := w.Send(sim.Consume("hannah", "wheat", 1, time.Now().UTC()))
	if err == nil {
		t.Fatal("Consume: want error for non-consumable, got nil")
	}
	if !errors.Is(err, sim.ErrNotConsumable) {
		t.Errorf("want ErrNotConsumable; got %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("ItemConsumed emitted on reject: %v", *captured)
	}
	// Inventory unchanged.
	view := readLiveActor(t, w, "hannah")
	if got, want := view.Inventory["wheat"], 5; got != want {
		t.Errorf("wheat inventory mutated after reject: %d, want %d", got, want)
	}
}

// TestConsume_InsufficientInventory: actor has 1 ale, asks to consume 2.
func TestConsume_InsufficientInventory(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"ale": 1},
		}},
		nil,
	)
	defer stop()

	captured := captureItemConsumed(t, w)
	_, err := w.Send(sim.Consume("hannah", "ale", 2, time.Now().UTC()))
	if err == nil {
		t.Fatal("Consume: want error for insufficient inventory, got nil")
	}
	if !errors.Is(err, sim.ErrInsufficientInventory) {
		t.Errorf("want ErrInsufficientInventory; got %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("ItemConsumed emitted on reject: %v", *captured)
	}
	// Inventory unchanged.
	view := readLiveActor(t, w, "hannah")
	if got, want := view.Inventory["ale"], 1; got != want {
		t.Errorf("ale inventory mutated after reject: %d, want %d", got, want)
	}
}

// TestConsume_InventoryMissingKind: actor doesn't have the item at all
// (different from "has some but not enough"). Same sentinel.
func TestConsume_InventoryMissingKind(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{}, // empty
		}},
		nil,
	)
	defer stop()

	_, err := w.Send(sim.Consume("hannah", "ale", 1, time.Now().UTC()))
	if err == nil {
		t.Fatal("Consume: want error for missing-kind, got nil")
	}
	if !errors.Is(err, sim.ErrInsufficientInventory) {
		t.Errorf("want ErrInsufficientInventory; got %v", err)
	}
}

// TestConsume_WalkInFlight: actor mid-walk rejects.
func TestConsume_WalkInFlight(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:           "hannah",
			displayName:  "Hannah",
			inventory:    map[sim.ItemKind]int{"ale": 1},
			moveInFlight: true,
		}},
		nil,
	)
	defer stop()

	captured := captureItemConsumed(t, w)
	_, err := w.Send(sim.Consume("hannah", "ale", 1, time.Now().UTC()))
	if err == nil {
		t.Fatal("Consume: want error for walk-in-flight, got nil")
	}
	if !strings.Contains(err.Error(), "walking") {
		t.Errorf("error lacks 'walking' guidance: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("ItemConsumed emitted on reject: %v", *captured)
	}
}

// TestConsume_QtyZeroRejects: Consume is exported, so non-handler callers
// could pass qty<=0. Re-validated inside Fn.
func TestConsume_QtyZeroRejects(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"ale": 1},
		}},
		nil,
	)
	defer stop()

	_, err := w.Send(sim.Consume("hannah", "ale", 0, time.Now().UTC()))
	if err == nil {
		t.Fatal("Consume: want error for qty=0, got nil")
	}
	if !strings.Contains(err.Error(), "qty must be at least 1") {
		t.Errorf("error lacks 'qty must be at least 1' guidance: %v", err)
	}
}

// TestConsume_UnknownActor: actorID isn't in w.Actors.
func TestConsume_UnknownActor(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"ale": 1},
		}},
		nil,
	)
	defer stop()

	_, err := w.Send(sim.Consume("ghost", "ale", 1, time.Now().UTC()))
	if err == nil {
		t.Fatal("Consume: want error for unknown actor, got nil")
	}
	if !strings.Contains(err.Error(), `actor "ghost"`) {
		t.Errorf("error should name the missing actor: %v", err)
	}
}

// TestConsume_RestackDwellCredit: consume stew twice at the same pin in
// quick succession. Dwell credit resets (LastCreditedAt anchored at the
// second consume, RemainingTicks back to 8). v1 explicitly does not stack;
// confirms the v2 substrate honors the same.
func TestConsume_RestackDwellCredit(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "hannah",
			displayName: "Hannah",
			inventory:   map[sim.ItemKind]int{"stew": 2},
			needs:       map[sim.NeedKey]int{"hunger": 24},
			x:           100, y: 100,
		}},
		[]consumeObjectSpec{{id: "tavern", x: 100, y: 100}},
	)
	defer stop()

	t1 := time.Now().UTC()
	if _, err := w.Send(sim.Consume("hannah", "stew", 1, t1)); err != nil {
		t.Fatalf("Consume 1: %v", err)
	}
	t2 := t1.Add(30 * time.Second) // second bowl 30s later
	if _, err := w.Send(sim.Consume("hannah", "stew", 1, t2)); err != nil {
		t.Fatalf("Consume 2: %v", err)
	}

	view := readLiveActor(t, w, "hannah")
	if got := len(view.DwellCredits); got != 1 {
		t.Fatalf("DwellCredits count = %d, want 1 (restack, not stack)", got)
	}
	credit := view.DwellCredits[sim.DwellCreditKey{
		ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem,
	}]
	if !credit.LastCreditedAt.Equal(t2) {
		t.Errorf("LastCreditedAt = %v, want %v (restacked at second consume)", credit.LastCreditedAt, t2)
	}
	if credit.RemainingTicks == nil || *credit.RemainingTicks != 8 {
		got := -1
		if credit.RemainingTicks != nil {
			got = *credit.RemainingTicks
		}
		t.Errorf("RemainingTicks = %d, want 8 (restack resets)", got)
	}
}

// ---- transferItem direct tests ----

// TestTransferItem_HappyPath: 3 ales from Hannah to Ezekiel. Seller decrements,
// buyer's map lazy-inits, buyer credits.
func TestTransferItem_HappyPath(t *testing.T) {
	from := &sim.Actor{ID: "hannah", Inventory: map[sim.ItemKind]int{"ale": 5}}
	to := &sim.Actor{ID: "ezekiel"} // Inventory nil — must lazy-init

	if err := sim.TransferItem(nil, from, to, "ale", 3); err != nil {
		t.Fatalf("transferItem: %v", err)
	}
	if got, want := from.Inventory["ale"], 2; got != want {
		t.Errorf("from[ale] = %d, want %d", got, want)
	}
	if to.Inventory == nil {
		t.Fatal("to.Inventory still nil after transfer")
	}
	if got, want := to.Inventory["ale"], 3; got != want {
		t.Errorf("to[ale] = %d, want %d", got, want)
	}
}

// TestTransferItem_DeleteOnZero: seller's remaining count hits zero → entry
// deleted, not left at 0.
func TestTransferItem_DeleteOnZero(t *testing.T) {
	from := &sim.Actor{ID: "hannah", Inventory: map[sim.ItemKind]int{"ale": 3}}
	to := &sim.Actor{ID: "ezekiel", Inventory: map[sim.ItemKind]int{}}

	if err := sim.TransferItem(nil, from, to, "ale", 3); err != nil {
		t.Fatalf("transferItem: %v", err)
	}
	if v, present := from.Inventory["ale"]; present {
		t.Errorf("from[ale] present with value %d; want deleted", v)
	}
	if got, want := to.Inventory["ale"], 3; got != want {
		t.Errorf("to[ale] = %d, want %d", got, want)
	}
}

// TestTransferItem_QtyZeroRejects: invalid qty rejected before any mutation.
func TestTransferItem_QtyZeroRejects(t *testing.T) {
	from := &sim.Actor{ID: "hannah", Inventory: map[sim.ItemKind]int{"ale": 5}}
	to := &sim.Actor{ID: "ezekiel"}

	if err := sim.TransferItem(nil, from, to, "ale", 0); err == nil {
		t.Fatal("transferItem: want error for qty=0, got nil")
	}
	if got, want := from.Inventory["ale"], 5; got != want {
		t.Errorf("from[ale] mutated on rejected transfer: %d, want %d", got, want)
	}
}

// TestTransferItem_QtyNegativeRejects: same — negative qty rejects before
// any mutation.
func TestTransferItem_QtyNegativeRejects(t *testing.T) {
	from := &sim.Actor{ID: "hannah", Inventory: map[sim.ItemKind]int{"ale": 5}}
	to := &sim.Actor{ID: "ezekiel"}

	if err := sim.TransferItem(nil, from, to, "ale", -1); err == nil {
		t.Fatal("transferItem: want error for qty=-1, got nil")
	}
	if got, want := from.Inventory["ale"], 5; got != want {
		t.Errorf("from[ale] mutated on rejected transfer: %d, want %d", got, want)
	}
}

// TestTransferItem_Insufficient: seller has 1, qty=2. Returns sentinel; no
// mutation on either side.
func TestTransferItem_Insufficient(t *testing.T) {
	from := &sim.Actor{ID: "hannah", Inventory: map[sim.ItemKind]int{"ale": 1}}
	to := &sim.Actor{ID: "ezekiel", Inventory: map[sim.ItemKind]int{"bread": 4}}

	err := sim.TransferItem(nil, from, to, "ale", 2)
	if err == nil {
		t.Fatal("transferItem: want error for insufficient, got nil")
	}
	if !errors.Is(err, sim.ErrInsufficientInventory) {
		t.Errorf("want ErrInsufficientInventory; got %v", err)
	}
	if got, want := from.Inventory["ale"], 1; got != want {
		t.Errorf("from[ale] mutated on rejected transfer: %d, want %d", got, want)
	}
	if got, want := to.Inventory["bread"], 4; got != want {
		t.Errorf("to[bread] mutated on rejected transfer: %d, want %d", got, want)
	}
}

// TestTransferItem_MissingKind: seller doesn't have the kind at all → same
// sentinel as "has some but not enough" (the LLM-facing signal is the same:
// you don't have that to give).
func TestTransferItem_MissingKind(t *testing.T) {
	from := &sim.Actor{ID: "hannah", Inventory: map[sim.ItemKind]int{"bread": 4}}
	to := &sim.Actor{ID: "ezekiel"}

	err := sim.TransferItem(nil, from, to, "ale", 1)
	if err == nil {
		t.Fatal("transferItem: want error for missing kind, got nil")
	}
	if !errors.Is(err, sim.ErrInsufficientInventory) {
		t.Errorf("want ErrInsufficientInventory; got %v", err)
	}
}

// TestTransferItem_AccumulatesOnExistingEntry: buyer already has 2 ales,
// transfer adds 3 → 5. No overwrite.
func TestTransferItem_AccumulatesOnExistingEntry(t *testing.T) {
	from := &sim.Actor{ID: "hannah", Inventory: map[sim.ItemKind]int{"ale": 5}}
	to := &sim.Actor{ID: "ezekiel", Inventory: map[sim.ItemKind]int{"ale": 2}}

	if err := sim.TransferItem(nil, from, to, "ale", 3); err != nil {
		t.Fatalf("transferItem: %v", err)
	}
	if got, want := to.Inventory["ale"], 5; got != want {
		t.Errorf("to[ale] = %d, want %d (accumulate, not overwrite)", got, want)
	}
}

// ---- resolveItemKind direct tests ----

func TestResolveItemKind_Cases(t *testing.T) {
	w, stop := buildConsumeTestWorld(t, nil, nil) // empty actors, populated ItemKinds
	defer stop()

	cases := []struct {
		name    string
		input   string
		wantOK  bool
		wantKey sim.ItemKind
	}{
		{name: "exact lowercase", input: "ale", wantOK: true, wantKey: "ale"},
		{name: "capitalized", input: "Ale", wantOK: true, wantKey: "ale"},
		{name: "upper", input: "ALE", wantOK: true, wantKey: "ale"},
		{name: "leading whitespace", input: "  ale", wantOK: true, wantKey: "ale"},
		{name: "trailing whitespace", input: "ale  ", wantOK: true, wantKey: "ale"},
		{name: "both-side whitespace + capitals", input: "  Ale  ", wantOK: true, wantKey: "ale"},
		{name: "miss", input: "moonbeam", wantOK: false},
		{name: "empty", input: "", wantOK: false},
		{name: "whitespace only", input: "   ", wantOK: false},
		{name: "partial match doesn't resolve", input: "al", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := sim.ResolveItemKind(w, c.input)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (got key %q)", ok, c.wantOK, got)
			}
			if ok && got != c.wantKey {
				t.Errorf("key = %q, want %q", got, c.wantKey)
			}
		})
	}
}

// TestResolveItemKind_DisplayLabel covers ZBBS-HOME-370: the deliberation
// prompt renders items by DisplayLabel ("Coca Tea" for key "coca_tea"), so the
// model passes the label back in its tool call. resolveItemKind must accept the
// label, not only the canonical key — otherwise consume/pay fail
// ErrUnknownItemKind on any item whose label differs from its key (spaces vs
// underscores being the live case). The canonical key still wins so a label
// can't shadow a different kind's id. resolveItemKind reads only w.ItemKinds,
// so a zero World with that field set exercises it without a running loop.
func TestResolveItemKind_DisplayLabel(t *testing.T) {
	w := &sim.World{
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"coca_tea": {Name: "coca_tea", DisplayLabel: "Coca Tea"},
			"ale":      {Name: "ale", DisplayLabel: "Ale"},
		},
	}

	cases := []struct {
		name    string
		input   string
		wantOK  bool
		wantKey sim.ItemKind
	}{
		{name: "label as rendered", input: "Coca Tea", wantOK: true, wantKey: "coca_tea"},
		{name: "label lowercased", input: "coca tea", wantOK: true, wantKey: "coca_tea"},
		{name: "label whitespace-padded", input: "  Coca Tea  ", wantOK: true, wantKey: "coca_tea"},
		{name: "canonical key still resolves", input: "coca_tea", wantOK: true, wantKey: "coca_tea"},
		{name: "single-word key path unaffected", input: "ale", wantOK: true, wantKey: "ale"},
		{name: "single-word label path", input: "Ale", wantOK: true, wantKey: "ale"},
		{name: "unknown still misses", input: "moonbeam", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := sim.ResolveItemKind(w, c.input)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (got key %q)", ok, c.wantOK, got)
			}
			if ok && got != c.wantKey {
				t.Errorf("key = %q, want %q", got, c.wantKey)
			}
		})
	}
}

// TestResolveItemKind_KeyBeatsLabel pins the two-pass precedence: when an input
// equals one kind's canonical key AND another kind's DisplayLabel, the key match
// must win regardless of Go map iteration order. Guards against a future
// refactor back to single-pass (key-or-label per entry) matching.
func TestResolveItemKind_KeyBeatsLabel(t *testing.T) {
	w := &sim.World{
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"coca_tea": {Name: "coca_tea", DisplayLabel: "Coca Tea"},
			"coca tea": {Name: "coca tea", DisplayLabel: "Other"},
		},
	}
	got, ok := sim.ResolveItemKind(w, "coca tea")
	if !ok {
		t.Fatal("expected a match for \"coca tea\"")
	}
	if got != "coca tea" {
		t.Errorf("key = %q, want %q (canonical key must beat another kind's label)", got, "coca tea")
	}
}

// The dwell-pin lookup (formerly findNearestVillageObject) is now
// resolveLoiteringObject, covered directly by loiter_resolve_test.go. Its
// end-to-end use in Consume is covered by TestConsume_DwellPin_* above.
