package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// ZBBS-HOME-296 PR1 — item capabilities + the service stock-skip + the
// deliver_order lodging branch (RoomAccess-is-the-lodger-relationship).

// --- capability model ---

func TestItemKindDef_HasCapability(t *testing.T) {
	def := &sim.ItemKindDef{Name: "nights_stay", Capabilities: []string{"service", "lodging"}}
	for _, tok := range []string{"service", "lodging"} {
		if !def.HasCapability(tok) {
			t.Errorf("HasCapability(%q) = false, want true", tok)
		}
	}
	if def.HasCapability("portable") {
		t.Error("HasCapability(portable) = true, want false")
	}
	if (&sim.ItemKindDef{Name: "stew"}).HasCapability("service") {
		t.Error("empty capabilities: HasCapability(service) = true, want false")
	}
}

// --- T2: service items skip the stock gate ---

// TestAcceptPay_ServiceItem_SkipsStockGate — a "service"-capability item
// (no inventory backing) accepts even though the seller holds zero stock.
// Without the gate-10 skip the accept would flip to
// failed_insufficient_stock.
func TestAcceptPay_ServiceItem_SkipsStockGate(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"}, // no inventory
	})
	defer stop()

	at := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["nights_stay"] = &sim.ItemKindDef{
			Name: "nights_stay", DisplayLabel: "a night's stay",
			Capabilities: []string{"service", "lodging"},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("inject service item: %v", err)
	}

	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob", ItemKind: "nights_stay",
		Qty: 1, Amount: 4, ConsumeNow: false,
		State: sim.PayLedgerStatePending, HuddleID: "h1", SceneID: "sc1",
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStateAccepted {
		t.Fatalf("State = %q, want %q (a service item must skip the stock gate despite zero seller inventory)",
			got, sim.PayLedgerStateAccepted)
	}
}

// --- T3: deliver_order lodging branch ---

// buildLodgingDeliverWorld stands up an inn (rooms supplied by the caller),
// a keeper (hannah, work_structure=inn) and a lodger-buyer (jefferey)
// co-present in huddle h1, plus a nights_stay service+lodging item.
func buildLodgingDeliverWorld(t *testing.T, innRooms []*sim.Room) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()

	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"inn": {ID: "inn", DisplayName: "Hannah's Inn", Rooms: innRooms},
	})
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"nights_stay": {
			Name: "nights_stay", DisplayLabel: "a night's stay",
			Capabilities: []string{"service", "lodging"},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID: "hannah", DisplayName: "Hannah", Kind: sim.KindNPCShared,
			State: sim.StateIdle, StateEnteredAt: now,
			WorkStructureID:  "inn",
			CurrentHuddleID:  "h1",
			Inventory:        map[sim.ItemKind]int{},
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		"jefferey": {
			ID: "jefferey", DisplayName: "Jefferey", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, StateEnteredAt: now,
			CurrentHuddleID:  "h1",
			Inventory:        map[sim.ItemKind]int{},
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		// A second co-present actor, for the multi-consumer guard test.
		"mary": {
			ID: "mary", DisplayName: "Mary", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, StateEnteredAt: now,
			CurrentHuddleID:  "h1",
			Inventory:        map[sim.ItemKind]int{},
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	return w, func() { cancel(); <-done }
}

func innWithBedroom() []*sim.Room {
	return []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	}
}

// mintLodgingOrder mints a Ready nights_stay Order hannah→jefferey (qty
// nights, jefferey the lone consumer) carrying the given ledger id.
func mintLodgingOrder(t *testing.T, w *sim.World, ledgerID sim.LedgerID, qty int, at time.Time) sim.OrderID {
	t.Helper()
	entry := &sim.PayLedgerEntry{
		ID: ledgerID, BuyerID: "jefferey", SellerID: "hannah",
		ItemKind: "nights_stay", Qty: qty, Amount: 28,
		ConsumerIDs: []sim.ActorID{"jefferey"}, ConsumeNow: false,
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CreateOrderForPayWithItem(world, entry, at), nil
	}})
	if err != nil {
		t.Fatalf("mintLodgingOrder: %v", err)
	}
	return res.(sim.OrderID)
}

// activeLedgerRoomAccess returns the actor's active ledger RoomAccess
// (read on the world goroutine). ok=false when none is active.
func activeLedgerRoomAccess(t *testing.T, w *sim.World, actorID sim.ActorID) (sim.RoomAccess, bool) {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[actorID]
		if a == nil {
			return nil, nil
		}
		for _, ra := range a.RoomAccess {
			if ra != nil && ra.Source == sim.AccessSourceLedger && ra.Active {
				return *ra, nil
			}
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("activeLedgerRoomAccess: %v", err)
	}
	if res == nil {
		return sim.RoomAccess{}, false
	}
	return res.(sim.RoomAccess), true
}

func orderState(t *testing.T, w *sim.World, id sim.OrderID) sim.OrderState {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		o := world.Orders[id]
		if o == nil {
			return sim.OrderState(""), nil
		}
		return o.State, nil
	}})
	if err != nil {
		t.Fatalf("orderState: %v", err)
	}
	return res.(sim.OrderState)
}

// Delivering a lodging order grants a private-bedroom RoomAccess to the
// lodger (the buyer) and transfers no goods.
func TestDeliverOrder_LodgingGrantsRoomAccess(t *testing.T) {
	w, stop := buildLodgingDeliverWorld(t, innWithBedroom())
	defer stop()

	at := time.Now().UTC()
	id := mintLodgingOrder(t, w, 42, 7, at)

	if _, err := w.Send(sim.DeliverOrder("hannah", id, at)); err != nil {
		t.Fatalf("DeliverOrder: %v", err)
	}

	ra, ok := activeLedgerRoomAccess(t, w, "jefferey")
	if !ok {
		t.Fatal("jefferey has no active ledger RoomAccess after lodging delivery")
	}
	if ra.RoomID != 2 {
		t.Errorf("RoomAccess.RoomID = %d, want 2 (bedroom_1)", ra.RoomID)
	}
	if ra.LedgerID != 42 {
		t.Errorf("RoomAccess.LedgerID = %d, want 42 (the order's ledger id)", ra.LedgerID)
	}
	if ra.ExpiresAt == nil || !ra.ExpiresAt.After(at) {
		t.Errorf("RoomAccess.ExpiresAt = %v, want a future instant", ra.ExpiresAt)
	}
	// Lodging moves no goods — the keeper's inventory is untouched.
	if got := readInventory(t, w, "hannah", "nights_stay"); got != 0 {
		t.Errorf("hannah nights_stay inventory = %d, want 0 (no goods transferred for lodging)", got)
	}
	if st := orderState(t, w, id); st != sim.OrderStateDelivered {
		t.Errorf("order state = %q, want %q", st, sim.OrderStateDelivered)
	}
}

// A second lodging delivery extends the lodger's existing access (same
// room, later expiry) rather than hopping rooms — AssignBedroomForLodger
// branch 1.
func TestDeliverOrder_LodgingExtendsExistingAccess(t *testing.T) {
	w, stop := buildLodgingDeliverWorld(t, innWithBedroom())
	defer stop()

	at := time.Now().UTC()
	id1 := mintLodgingOrder(t, w, 42, 7, at)
	if _, err := w.Send(sim.DeliverOrder("hannah", id1, at)); err != nil {
		t.Fatalf("first DeliverOrder: %v", err)
	}
	ra1, ok := activeLedgerRoomAccess(t, w, "jefferey")
	if !ok {
		t.Fatal("no access after first delivery")
	}

	at2 := at.Add(7 * 24 * time.Hour)
	id2 := mintLodgingOrder(t, w, 43, 7, at2)
	if _, err := w.Send(sim.DeliverOrder("hannah", id2, at2)); err != nil {
		t.Fatalf("second DeliverOrder: %v", err)
	}
	ra2, ok := activeLedgerRoomAccess(t, w, "jefferey")
	if !ok {
		t.Fatal("no access after second delivery")
	}

	if ra2.RoomID != ra1.RoomID {
		t.Errorf("room hopped on renewal: %d -> %d, want same room", ra1.RoomID, ra2.RoomID)
	}
	if ra1.ExpiresAt == nil || ra2.ExpiresAt == nil || !ra2.ExpiresAt.After(*ra1.ExpiresAt) {
		t.Errorf("renewal did not extend expiry: %v -> %v", ra1.ExpiresAt, ra2.ExpiresAt)
	}
	if ra2.LedgerID != 43 {
		t.Errorf("RoomAccess.LedgerID = %d, want 43 (the renewing order)", ra2.LedgerID)
	}
}

// A lodging-capability item that lacks the service capability is a catalog
// misconfiguration (it would pass the gate-5 stock check yet consume no
// stock down the lodging branch). DeliverOrder rejects it loudly.
// (code_review finding 2.)
func TestDeliverOrder_LodgingWithoutService_Rejected(t *testing.T) {
	w, stop := buildLodgingDeliverWorld(t, innWithBedroom())
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["nights_stay"].Capabilities = []string{"lodging"} // drop "service"
		// Give the keeper stock so gate 5 passes and execution reaches the
		// lodging-implies-service guard (the point of the test).
		world.Actors["hannah"].Inventory["nights_stay"] = 7
		return nil, nil
	}}); err != nil {
		t.Fatalf("reconfigure item: %v", err)
	}

	at := time.Now().UTC()
	id := mintLodgingOrder(t, w, 42, 7, at)
	_, err := w.Send(sim.DeliverOrder("hannah", id, at))
	if err == nil {
		t.Fatal("DeliverOrder succeeded for a lodging-without-service item, want an error")
	}
	if !strings.Contains(err.Error(), "without service") {
		t.Errorf("error = %q, want it to mention 'without service'", err.Error())
	}
}

// Redelivering an already-delivered lodging order is rejected by the state
// gate — the room grant is not applied (or extended) twice. (code_review
// finding 1: retry must not double-extend durable access.)
func TestDeliverOrder_LodgingRedeliverRejected(t *testing.T) {
	w, stop := buildLodgingDeliverWorld(t, innWithBedroom())
	defer stop()

	at := time.Now().UTC()
	id := mintLodgingOrder(t, w, 42, 7, at)
	if _, err := w.Send(sim.DeliverOrder("hannah", id, at)); err != nil {
		t.Fatalf("first DeliverOrder: %v", err)
	}
	ra1, ok := activeLedgerRoomAccess(t, w, "jefferey")
	if !ok {
		t.Fatal("no access after first delivery")
	}

	if _, err := w.Send(sim.DeliverOrder("hannah", id, at)); err == nil {
		t.Fatal("redelivering a delivered order succeeded, want a gate-3 rejection")
	}
	ra2, ok := activeLedgerRoomAccess(t, w, "jefferey")
	if !ok {
		t.Fatal("access vanished after rejected redelivery")
	}
	if ra1.ExpiresAt == nil || ra2.ExpiresAt == nil || !ra2.ExpiresAt.Equal(*ra1.ExpiresAt) {
		t.Errorf("rejected redelivery changed the access expiry: %v -> %v (double-extend)", ra1.ExpiresAt, ra2.ExpiresAt)
	}
}

// lodger_until is anchored on the Order's CreatedAt, not the delivery
// time. Decouple the two (creation, then next-day delivery, with a bumped
// OrderTTL so gate-4 doesn't reject) and assert the expiry tracks CreatedAt.
// (code_review finding 5.)
func TestDeliverOrder_LodgingUntilAnchoredOnCreatedAt(t *testing.T) {
	w, stop := buildLodgingDeliverWorld(t, innWithBedroom())
	defer stop()

	createdAt := time.Date(2026, 5, 23, 9, 0, 0, 0, time.UTC)
	deliveredAt := createdAt.Add(26 * time.Hour) // next day — distinct from creation

	var checkOutHour int
	var loc *time.Location
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.OrderTTL = 30 * 24 * time.Hour // so the next-day delivery isn't TTL-expired (gate 4)
		checkOutHour = world.Settings.LodgingCheckOutHour
		loc = world.Settings.Location
		return nil, nil
	}}); err != nil {
		t.Fatalf("settings: %v", err)
	}

	id := mintLodgingOrder(t, w, 42, 7, createdAt)
	if _, err := w.Send(sim.DeliverOrder("hannah", id, deliveredAt)); err != nil {
		t.Fatalf("DeliverOrder: %v", err)
	}
	ra, ok := activeLedgerRoomAccess(t, w, "jefferey")
	if !ok {
		t.Fatal("no access after delivery")
	}

	wantCreated := sim.ComputeLodgerUntil(createdAt, 7, checkOutHour, loc)
	wantDelivered := sim.ComputeLodgerUntil(deliveredAt, 7, checkOutHour, loc)
	if wantCreated.Equal(wantDelivered) {
		t.Fatal("test setup bug: created/delivered anchors coincide — widen the gap")
	}
	if ra.ExpiresAt == nil || !ra.ExpiresAt.Equal(wantCreated) {
		t.Errorf("ExpiresAt = %v, want %v (anchored on Order.CreatedAt, not delivery time %v)",
			ra.ExpiresAt, wantCreated, wantDelivered)
	}
}

// A lodging order whose consumer set isn't exactly [buyer] is rejected —
// the grant/teleport must not land on an actor gate 6 never validated, nor
// strand listed consumers. (work review finding 1.)
func TestDeliverOrder_LodgingRejectsNonSelfConsumer(t *testing.T) {
	w, stop := buildLodgingDeliverWorld(t, innWithBedroom())
	defer stop()

	at := time.Now().UTC()
	// Two consumers (jefferey + mary), both co-present so gate 6 passes and
	// execution reaches the single-self-consumer guard.
	entry := &sim.PayLedgerEntry{
		ID: 42, BuyerID: "jefferey", SellerID: "hannah",
		ItemKind: "nights_stay", Qty: 7, Amount: 28,
		ConsumerIDs: []sim.ActorID{"jefferey", "mary"}, ConsumeNow: false,
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CreateOrderForPayWithItem(world, entry, at), nil
	}})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	id := res.(sim.OrderID)

	if _, err := w.Send(sim.DeliverOrder("hannah", id, at)); err == nil {
		t.Fatal("DeliverOrder succeeded for a multi-consumer lodging order, want rejection")
	} else if !strings.Contains(err.Error(), "sole consumer") {
		t.Errorf("error = %q, want it to mention 'sole consumer'", err.Error())
	}
	// No room granted to anyone — the guard rejects before assignment.
	if _, ok := activeLedgerRoomAccess(t, w, "jefferey"); ok {
		t.Error("jefferey got a room despite the rejected multi-consumer order")
	}
	if _, ok := activeLedgerRoomAccess(t, w, "mary"); ok {
		t.Error("mary got a room despite the rejected multi-consumer order")
	}
}

// An inn with no private bedrooms surfaces a distinct operator-data error.
func TestDeliverOrder_LodgingNoPrivateRooms(t *testing.T) {
	w, stop := buildLodgingDeliverWorld(t, []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
	})
	defer stop()

	at := time.Now().UTC()
	id := mintLodgingOrder(t, w, 42, 7, at)
	_, err := w.Send(sim.DeliverOrder("hannah", id, at))
	if err == nil {
		t.Fatal("DeliverOrder succeeded, want an error (no private bedrooms)")
	}
	if !strings.Contains(err.Error(), "no bedrooms") {
		t.Errorf("error = %q, want it to mention 'no bedrooms'", err.Error())
	}
}
