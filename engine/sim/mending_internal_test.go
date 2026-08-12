package sim

import (
	"strings"
	"testing"
	"time"
)

// mendingCatalog is the minimal catalog for the LLM-625 tests: a work garment,
// a warms garment, the imported thread, and the mending service itself.
func mendingCatalog() map[ItemKind]*ItemKindDef {
	return map[ItemKind]*ItemKindDef{
		"linens":       {Name: "linens", WearMinutes: 10800},
		"coat":         {Name: "coat", WearMinutes: 18000, Capabilities: []string{string(CapabilityWarms)}},
		MendThreadKind: {Name: MendThreadKind},
		"mending":      {Name: "mending", Capabilities: []string{"service", CapabilityMending}},
	}
}

// TestWornGarmentKinds — a kind is mendable exactly when the actor still holds
// it and its in-use unit is partially worn (an entry inside (0, budget)).
// Fresh units (no entry), stale unbacked entries, and non-garment kinds never
// list; both garment classes (work and warms) do.
func TestWornGarmentKinds(t *testing.T) {
	kinds := mendingCatalog()
	cases := []struct {
		name      string
		inventory map[ItemKind]int
		wear      map[ItemKind]int
		want      int
	}{
		{"fresh wardrobe has nothing to mend", map[ItemKind]int{"linens": 1}, nil, 0},
		{"a worn work garment lists", map[ItemKind]int{"linens": 1}, map[ItemKind]int{"linens": 1200}, 1},
		{"a worn warms garment lists too", map[ItemKind]int{"coat": 1}, map[ItemKind]int{"coat": 900}, 1},
		{"both classes list together", map[ItemKind]int{"linens": 1, "coat": 1}, map[ItemKind]int{"linens": 1200, "coat": 900}, 2},
		{"an unbacked stale entry does not list", map[ItemKind]int{}, map[ItemKind]int{"linens": 1200}, 0},
		{"an entry at full budget reads fresh", map[ItemKind]int{"linens": 1}, map[ItemKind]int{"linens": 10800}, 0},
		{"a non-garment wear entry does not list", map[ItemKind]int{MendThreadKind: 1}, map[ItemKind]int{MendThreadKind: 5}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(WornGarmentKinds(kinds, tc.inventory, tc.wear)); got != tc.want {
				t.Errorf("WornGarmentKinds = %d kinds, want %d", got, tc.want)
			}
		})
	}
}

// TestMendGarments — a mend deletes every worn entry (work and warms alike), so
// the in-use units read fresh to the wear sweep and both clothing tiers, and
// leaves everything else untouched.
func TestMendGarments(t *testing.T) {
	kinds := mendingCatalog()
	a := &Actor{
		Inventory:   map[ItemKind]int{"linens": 1, "coat": 1},
		GarmentWear: map[ItemKind]int{"linens": 1200, "coat": 900},
	}
	mended := MendGarments(kinds, a)
	if len(mended) != 2 {
		t.Fatalf("mended %d kinds, want 2 (linens and coat): %v", len(mended), mended)
	}
	if len(a.GarmentWear) != 0 {
		t.Errorf("wear entries survived the mend: %v", a.GarmentWear)
	}
	if a.Inventory["linens"] != 1 || a.Inventory["coat"] != 1 {
		t.Errorf("a mend must not touch inventory: %v", a.Inventory)
	}
	if again := MendGarments(kinds, a); len(again) != 0 {
		t.Errorf("a second mend on a fresh wardrobe mended %v, want nothing", again)
	}
	if MendGarments(kinds, nil) != nil {
		t.Error("nil actor must mend nothing")
	}
}

// mendingWorld builds the minimal World for the transferOrderGoods mending arm:
// a mender working a TagMending structure with `thread` spools, and a buyer with
// a worn linens unit.
func mendingWorld(thread int) (*World, *Actor, *Actor) {
	seller := &Actor{
		ID: "hannah", DisplayName: "Hannah Boggs", WorkStructureID: "inn",
		Inventory: map[ItemKind]int{},
	}
	if thread > 0 {
		seller.Inventory[MendThreadKind] = thread
	}
	buyer := &Actor{
		ID: "ezekiel", DisplayName: "Ezekiel Crane",
		Inventory:   map[ItemKind]int{"linens": 1},
		GarmentWear: map[ItemKind]int{"linens": 1200},
	}
	w := &World{
		ItemKinds: mendingCatalog(),
		Actors:    map[ActorID]*Actor{seller.ID: seller, buyer.ID: buyer},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"inn": {ID: "inn", Tags: []string{TagMending}},
		},
	}
	return w, seller, buyer
}

func mendingOrder(buyer ActorID) *Order {
	return &Order{ID: 1, Item: "mending", Qty: 1, BuyerID: buyer, ConsumerIDs: []ActorID{buyer}}
}

// TestTransferOrderGoods_Mending — the happy path restores the buyer's worn
// unit and draws the seller's thread; the gate arms reject a non-mender
// workplace, a threadless mender, and an unworn buyer without mutating state.
func TestTransferOrderGoods_Mending(t *testing.T) {
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

	t.Run("mend restores wear and draws thread", func(t *testing.T) {
		w, seller, buyer := mendingWorld(2)
		if err := transferOrderGoods(w, mendingOrder(buyer.ID), seller, []*Actor{buyer}, at); err != nil {
			t.Fatalf("transferOrderGoods: %v", err)
		}
		if len(buyer.GarmentWear) != 0 {
			t.Errorf("wear survived the mend: %v", buyer.GarmentWear)
		}
		if got := seller.Inventory[MendThreadKind]; got != 2-MendThreadPerMend {
			t.Errorf("seller thread = %d, want %d", got, 2-MendThreadPerMend)
		}
		if buyer.Inventory["mending"] != 0 {
			t.Error("a mend must transfer no goods to the buyer")
		}
	})

	t.Run("the last spool is drawn and deleted on zero", func(t *testing.T) {
		w, seller, buyer := mendingWorld(MendThreadPerMend)
		if err := transferOrderGoods(w, mendingOrder(buyer.ID), seller, []*Actor{buyer}, at); err != nil {
			t.Fatalf("transferOrderGoods: %v", err)
		}
		if _, held := seller.Inventory[MendThreadKind]; held {
			t.Errorf("zeroed thread must be deleted from inventory (delete-on-zero): %v", seller.Inventory)
		}
	})

	t.Run("a non-mender workplace rejects", func(t *testing.T) {
		w, seller, buyer := mendingWorld(2)
		w.VillageObjects["inn"].Tags = nil
		err := transferOrderGoods(w, mendingOrder(buyer.ID), seller, []*Actor{buyer}, at)
		if err == nil || !strings.Contains(err.Error(), "mending shop") {
			t.Fatalf("want a mending-shop rejection, got %v", err)
		}
		if len(buyer.GarmentWear) == 0 {
			t.Error("a rejected mend must not touch the buyer's wear")
		}
	})

	t.Run("a threadless mender rejects", func(t *testing.T) {
		w, seller, buyer := mendingWorld(0)
		err := transferOrderGoods(w, mendingOrder(buyer.ID), seller, []*Actor{buyer}, at)
		if err == nil || !strings.Contains(err.Error(), "thread") {
			t.Fatalf("want a no-thread rejection, got %v", err)
		}
		if len(buyer.GarmentWear) == 0 {
			t.Error("a rejected mend must not touch the buyer's wear")
		}
	})

	t.Run("an unworn buyer rejects without drawing thread", func(t *testing.T) {
		w, seller, buyer := mendingWorld(2)
		buyer.GarmentWear = nil
		err := transferOrderGoods(w, mendingOrder(buyer.ID), seller, []*Actor{buyer}, at)
		if err == nil || !strings.Contains(err.Error(), "nothing worn") {
			t.Fatalf("want a nothing-to-mend rejection, got %v", err)
		}
		if got := seller.Inventory[MendThreadKind]; got != 2 {
			t.Errorf("a rejected mend must not draw thread: %d", got)
		}
	})

	t.Run("a non-self consumer rejects", func(t *testing.T) {
		w, seller, buyer := mendingWorld(2)
		o := mendingOrder(buyer.ID)
		o.ConsumerIDs = []ActorID{seller.ID}
		if err := transferOrderGoods(w, o, seller, []*Actor{seller}, at); err == nil {
			t.Fatal("want a sole-self-consumer rejection, got nil")
		}
	})

	t.Run("mending without service is a misconfigured catalog", func(t *testing.T) {
		w, seller, buyer := mendingWorld(2)
		w.ItemKinds["mending"].Capabilities = []string{CapabilityMending}
		err := transferOrderGoods(w, mendingOrder(buyer.ID), seller, []*Actor{buyer}, at)
		if err == nil || !strings.Contains(err.Error(), "misconfigured") {
			t.Fatalf("want the misconfigured-catalog rejection, got %v", err)
		}
	})

	t.Run("a resolved consumer other than the buyer rejects", func(t *testing.T) {
		w, seller, buyer := mendingWorld(2)
		other := &Actor{ID: "other", DisplayName: "Someone Else",
			Inventory: map[ItemKind]int{"linens": 1}, GarmentWear: map[ItemKind]int{"linens": 500}}
		w.Actors[other.ID] = other
		// ConsumerIDs pass the id check, but the resolved actor is someone else.
		err := transferOrderGoods(w, mendingOrder(buyer.ID), seller, []*Actor{other}, at)
		if err == nil || !strings.Contains(err.Error(), "other than buyer") {
			t.Fatalf("want the resolved-consumer identity rejection, got %v", err)
		}
		if len(other.GarmentWear) == 0 {
			t.Error("the mismatched consumer must not be mended")
		}
	})
}

// TestPreflightMendingEntry — the commit-time invariant boundary (code_review):
// coins move before the delivery branch in commitPayTransfer, so every way a
// mend can fail must reject in the preflight, and an entry with no mending
// involvement must pass through untouched.
func TestPreflightMendingEntry(t *testing.T) {
	w, seller, buyer := mendingWorld(2)

	t.Run("no mending involvement passes", func(t *testing.T) {
		if err := preflightMendingEntry(w, buyer, seller, &PayLedgerEntry{ItemKind: "linens"}); err != nil {
			t.Fatalf("a plain goods entry must pass the preflight: %v", err)
		}
	})
	t.Run("a deliverable mend passes", func(t *testing.T) {
		entry := &PayLedgerEntry{ID: 7, ItemKind: "mending", BuyerID: buyer.ID, SellerID: seller.ID}
		if err := preflightMendingEntry(w, buyer, seller, entry); err != nil {
			t.Fatalf("a deliverable mend must pass the preflight: %v", err)
		}
	})
	t.Run("mending inside a bundle rejects", func(t *testing.T) {
		entry := &PayLedgerEntry{ID: 7, Lines: []QuoteLine{{ItemKind: "linens"}, {ItemKind: "mending"}}}
		if err := preflightMendingEntry(w, buyer, seller, entry); err == nil || !strings.Contains(err.Error(), "bundle") {
			t.Fatalf("want the bundle rejection, got %v", err)
		}
	})
	t.Run("a gifted mend rejects", func(t *testing.T) {
		entry := &PayLedgerEntry{ID: 7, ItemKind: "mending", IsGift: true, BuyerID: buyer.ID}
		if err := preflightMendingEntry(w, buyer, seller, entry); err == nil || !strings.Contains(err.Error(), "gift") {
			t.Fatalf("want the gift rejection, got %v", err)
		}
	})
	t.Run("a non-buyer consumer rejects", func(t *testing.T) {
		entry := &PayLedgerEntry{ID: 7, ItemKind: "mending", BuyerID: buyer.ID, ConsumerIDs: []ActorID{seller.ID}}
		if err := preflightMendingEntry(w, buyer, seller, entry); err == nil || !strings.Contains(err.Error(), "non-buyer") {
			t.Fatalf("want the non-buyer consumer rejection, got %v", err)
		}
	})
	t.Run("an undeliverable mend rejects without mutating", func(t *testing.T) {
		wDry, sellerDry, buyerDry := mendingWorld(0)
		entry := &PayLedgerEntry{ID: 7, ItemKind: "mending", BuyerID: buyerDry.ID}
		if err := preflightMendingEntry(wDry, buyerDry, sellerDry, entry); err == nil || !strings.Contains(err.Error(), "thread") {
			t.Fatalf("want the no-thread rejection, got %v", err)
		}
		if len(buyerDry.GarmentWear) == 0 {
			t.Error("the preflight must not mutate the buyer")
		}
	})
}
