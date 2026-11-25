package perception

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// commission_parity_test.go — LLM-1026. The offer line says "accept, and you
// take payment now and owe …" when it predicts the accept will mint a
// commission. This test checks the prediction against the real thing: build the
// perception payload from a running world's published snapshot, then run
// accept_pay on that world and see what it actually did. A drift between the
// preview (buildPayOfferShortfalls) and the accept path (acceptPendingOffer's
// stock gate, isCommissionOrder) fails here, including on reserved stock.

type parityCase struct {
	name      string
	nailsHeld int
	reserved  int          // nails an earlier Ready order already owes
	item      sim.ItemKind // what Alice asks for
	fundsGone bool         // Alice's purse is emptied between the offer and the accept
}

func TestCommissionPreviewMatchesAccept(t *testing.T) {
	cases := []parityCase{
		{name: "holds none", nailsHeld: 0, item: "nail"},
		{name: "holds enough but all reserved", nailsHeld: 3, reserved: 3, item: "nail"},
		{name: "holds some free", nailsHeld: 3, reserved: 2, item: "nail"},
		{name: "holds enough free", nailsHeld: 3, item: "nail"},
		{name: "not his to make", nailsHeld: 0, item: "widget"},
		// The line is conditional ("accept, and …"); a settle that fails a gate the
		// snapshot cannot see (here, the buyer's purse) mints nothing, and the
		// accept result already says the sale fell through.
		{name: "buyer can no longer pay", nailsHeld: 0, item: "nail", fundsGone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildParityWorld(t, tc.nailsHeld)
			defer stop()
			now := time.Now().UTC()
			if tc.reserved > 0 {
				send(t, w, func(world *sim.World) (any, error) {
					world.Orders[900] = &sim.Order{
						ID: 900, State: sim.OrderStateReady, SellerID: "bob", BuyerID: "carol",
						Item: "nail", Qty: tc.reserved, ConsumerIDs: []sim.ActorID{"carol"},
						CreatedAt: now, ExpiresAt: now.Add(time.Hour),
					}
					return nil, nil
				})
			}
			send(t, w, func(world *sim.World) (any, error) { return sim.EnsureColocatedHuddle("alice", now).Fn(world) })
			res := send(t, w, func(world *sim.World) (any, error) {
				return sim.PayWithItem("alice", "Bob", string(tc.item), 3, 18, false, nil, nil, 0, 0, "", now).Fn(world)
			})
			placed, ok := res.(sim.PayWithItemResult)
			if !ok || placed.State != sim.PayLedgerStatePending {
				t.Fatalf("offer = %+v, want pending", res)
			}

			// The preview, from the snapshot the seller's turn would read: the
			// shortfall data and the rendered line must agree with each other.
			p := Build(w.Published(), "bob", nil)
			sf, short := p.PayOfferShortfalls[placed.LedgerID]
			predictsCommission := short && sf.Owed
			out := combinedPrompt(Render(p, DefaultRenderConfig()))
			if says := strings.Contains(out, "accept, and you take payment now"); says != predictsCommission {
				t.Errorf("rendered commission clause = %v, shortfall predicts %v\n%s", says, predictsCommission, promptSection(out, "## Offers awaiting your decision"))
			}

			if tc.fundsGone {
				send(t, w, func(world *sim.World) (any, error) { world.Actors["alice"].Coins = 0; return nil, nil })
			}

			// The real accept, on the same world. The independent outcome is the
			// order the accept left behind: a Ready order still owed is a
			// commission; a delivered sale leaves none Ready; a refusal or a failed
			// settle mints none. CommissionTakenFor is then checked against it.
			type outcome struct {
				state      sim.PayLedgerState
				err        error
				readyOrder bool
				reported   bool
			}
			got := send(t, w, func(world *sim.World) (any, error) {
				st, err := sim.AcceptPay("bob", placed.LedgerID, now).Fn(world)
				out := outcome{err: err}
				if s, ok := st.(sim.PayLedgerState); ok {
					out.state = s
				}
				if o := world.Orders[sim.OrderID(placed.LedgerID)]; o != nil && o.State == sim.OrderStateReady {
					out.readyOrder = true
				}
				_, out.reported = sim.CommissionTakenFor(world, "bob", placed.LedgerID)
				return out, nil
			}).(outcome)

			if got.reported != got.readyOrder {
				t.Errorf("CommissionTakenFor = %v, but a Ready order owed after accept = %v", got.reported, got.readyOrder)
			}
			switch {
			case tc.fundsGone:
				if !predictsCommission || got.readyOrder || got.state != sim.PayLedgerStateFailedInsufficientFunds {
					t.Errorf("buyer short at settle: preview=%v, accept state=%q readyOrder=%v; want the preview, a failed settle and no order",
						predictsCommission, got.state, got.readyOrder)
				}
			case predictsCommission != got.readyOrder:
				t.Errorf("preview says commission=%v (short=%v %+v), accept gave state=%q err=%v readyOrder=%v",
					predictsCommission, short, sf, got.state, got.err, got.readyOrder)
			}
			// A short line that does not predict a commission must be one accept refuses.
			if short && !sf.Owed && got.err == nil {
				t.Errorf("short, non-commission offer accepted (state %q); the plain warning was wrong", got.state)
			}
			// No shortfall means accept hands the goods over from stock.
			if !short && (got.err != nil || got.state != sim.PayLedgerStateAccepted || got.readyOrder) {
				t.Errorf("no shortfall shown, but accept gave state=%q err=%v readyOrder=%v", got.state, got.err, got.readyOrder)
			}
		})
	}
}

func send(t *testing.T, w *sim.World, fn func(*sim.World) (any, error)) any {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: fn})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return res
}

// buildParityWorld runs a world with Alice (buyer) and Bob, a nail smith holding
// nailsHeld nails, both in the forge. "widget" is a real, makeable good Bob does
// not produce.
func buildParityWorld(t *testing.T, nailsHeld int) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"nail":   {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Category: sim.ItemCategoryMaterial},
		"widget": {Name: "widget", DisplayLabel: "Widget", DisplayLabelSingular: "widget", DisplayLabelPlural: "widgets", Category: sim.ItemCategoryMaterial},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"nail":   {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		"widget": {OutputItem: "widget", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 3, RetailPrice: 6},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{"forge": {ID: "forge", DisplayName: "The Forge"}})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"bldg-asset": {ID: "bldg-asset", Category: "structure"}})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"forge": {ID: "forge", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 160, Y: 160}},
	})
	inv := map[sim.ItemKind]int{}
	if nailsHeld > 0 {
		inv["nail"] = nailsHeld
	}
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {
			ID: "alice", DisplayName: "Alice", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Coins: 50, InsideStructureID: "forge",
			Acquaintances: map[string]sim.Acquaintance{"Bob": {}},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"bob": {
			ID: "bob", DisplayName: "Bob", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, InsideStructureID: "forge", WorkStructureID: "forge",
			Inventory:     inv,
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{{Item: "nail", Source: sim.RestockSourceProduce, Max: 20}}},
			Acquaintances: map[string]sim.Acquaintance{"Alice": {}},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
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
