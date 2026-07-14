package sim

import (
	"testing"
	"time"
)

// price_book_test.go — substrate-layer tests for the in-memory
// per-(seller, item) price book. World is constructed via NewWorld
// + direct method calls; the cascade subscriber wiring is exercised
// separately in engine/sim/cascade/price_book_test.go.

// stubRepo gives NewWorld a non-nil Repository — none of the price
// book substrate paths exercise sub-repo methods, so any Repository
// works. We pick a zero value for simplicity.
func priceBookStubRepo() Repository { return Repository{} }

// --- SeedPriceBook ---------------------------------------------------

func TestSeedPriceBook_LazyAllocates(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	if w.PriceBook != nil {
		t.Fatalf("PriceBook should be nil on fresh world, got %v", w.PriceBook)
	}
	at := time.Now().UTC()
	w.SeedPriceBook([]PriceBookSeedRecord{
		{
			Key: PriceBookKey{SellerID: "bob", Item: "ale"},
			Observation: PriceObservation{
				BuyerID: "alice", Amount: 3, Qty: 1, Consumers: 1, At: at,
			},
		},
	})
	if w.PriceBook == nil {
		t.Fatal("PriceBook should be allocated after Seed")
	}
	buf, ok := w.PriceBook[PriceBookKey{SellerID: "bob", Item: "ale"}]
	if !ok || buf.Len() != 1 {
		t.Fatalf("expected one entry for (bob, ale); got ok=%v len=%d", ok, buf.Len())
	}
}

func TestSeedPriceBook_EmptyIsNoop(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	w.SeedPriceBook(nil)
	if w.PriceBook != nil {
		t.Fatalf("nil seed should leave PriceBook unallocated, got %v", w.PriceBook)
	}
	w.SeedPriceBook([]PriceBookSeedRecord{})
	if w.PriceBook != nil {
		t.Fatalf("empty seed should leave PriceBook unallocated, got %v", w.PriceBook)
	}
}

func TestSeedPriceBook_OldestFirstChronological(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	// Seed in chronological order — the contract.
	seed := []PriceBookSeedRecord{
		{Key: PriceBookKey{SellerID: "bob", Item: "ale"}, Observation: PriceObservation{BuyerID: "alice", Amount: 2, Qty: 1, Consumers: 1, At: base}},
		{Key: PriceBookKey{SellerID: "bob", Item: "ale"}, Observation: PriceObservation{BuyerID: "carol", Amount: 3, Qty: 1, Consumers: 1, At: base.Add(2 * time.Hour)}},
		{Key: PriceBookKey{SellerID: "bob", Item: "ale"}, Observation: PriceObservation{BuyerID: "alice", Amount: 2, Qty: 1, Consumers: 1, At: base.Add(5 * time.Hour)}},
	}
	w.SeedPriceBook(seed)

	buf := w.PriceBook[PriceBookKey{SellerID: "bob", Item: "ale"}]
	entries := buf.Snapshot()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if !entries[0].At.Equal(base) || !entries[2].At.Equal(base.Add(5*time.Hour)) {
		t.Errorf("entries not in oldest-first order: %+v", entries)
	}
}

// --- RecordPriceObservation ------------------------------------------

func TestRecordPriceObservation_AppendsEntry(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	at := time.Now().UTC()
	key := PriceBookKey{SellerID: "bob", Item: "ale"}
	obs := PriceObservation{BuyerID: "alice", Amount: 3, Qty: 1, Consumers: 1, At: at}

	n, err := RecordPriceObservation(key, obs).Fn(w)
	if err != nil {
		t.Fatalf("RecordPriceObservation: %v", err)
	}
	if n.(int) != 1 {
		t.Errorf("expected len=1, got %v", n)
	}
	if w.PriceBook[key].Len() != 1 {
		t.Errorf("expected one entry in buffer, got %d", w.PriceBook[key].Len())
	}
}

func TestRecordPriceObservation_RingBufferCapsAtCapacity(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	key := PriceBookKey{SellerID: "bob", Item: "ale"}
	base := time.Now().UTC()

	// Push PriceBookRingCapacity + 5 entries.
	for i := 0; i < PriceBookRingCapacity+5; i++ {
		obs := PriceObservation{
			BuyerID: "alice", Amount: i, Qty: 1, Consumers: 1, At: base.Add(time.Duration(i) * time.Minute),
		}
		if _, err := RecordPriceObservation(key, obs).Fn(w); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	if got := w.PriceBook[key].Len(); got != PriceBookRingCapacity {
		t.Errorf("buffer length = %d, want %d (capped)", got, PriceBookRingCapacity)
	}
	// Oldest 5 entries should have been dropped — first surviving entry
	// is Amount=5.
	entries := w.PriceBook[key].Snapshot()
	if entries[0].Amount != 5 {
		t.Errorf("oldest survivor Amount = %d, want 5 (entries 0-4 should have been dropped)", entries[0].Amount)
	}
	if entries[len(entries)-1].Amount != PriceBookRingCapacity+4 {
		t.Errorf("newest Amount = %d, want %d", entries[len(entries)-1].Amount, PriceBookRingCapacity+4)
	}
}

// --- LookupBuyerLastPaid ---------------------------------------------

func TestLookupBuyerLastPaid_NoBookReturnsAskTheKeeper(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	_, ok := w.LookupBuyerLastPaid("alice", "bob", "ale")
	if ok {
		t.Fatal("LookupBuyerLastPaid on empty book should return ok=false (ask the keeper)")
	}
}

func TestLookupBuyerLastPaid_NoBuyerMatchReturnsAskTheKeeper(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	key := PriceBookKey{SellerID: "bob", Item: "ale"}
	w.SeedPriceBook([]PriceBookSeedRecord{
		{Key: key, Observation: PriceObservation{BuyerID: "carol", Amount: 3, Qty: 1, Consumers: 1, At: time.Now().UTC()}},
	})
	// Buffer is non-empty but no entry for alice.
	if _, ok := w.LookupBuyerLastPaid("alice", "bob", "ale"); ok {
		t.Fatal("LookupBuyerLastPaid with no buyer match should return ok=false")
	}
}

func TestLookupBuyerLastPaid_ReturnsMostRecentForBuyer(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	key := PriceBookKey{SellerID: "bob", Item: "ale"}
	base := time.Now().UTC().Add(-10 * time.Hour)
	w.SeedPriceBook([]PriceBookSeedRecord{
		{Key: key, Observation: PriceObservation{BuyerID: "alice", Amount: 2, At: base, Qty: 1, Consumers: 1}},
		{Key: key, Observation: PriceObservation{BuyerID: "carol", Amount: 5, At: base.Add(2 * time.Hour), Qty: 1, Consumers: 1}},
		{Key: key, Observation: PriceObservation{BuyerID: "alice", Amount: 3, At: base.Add(4 * time.Hour), Qty: 1, Consumers: 1}}, // most recent alice
		{Key: key, Observation: PriceObservation{BuyerID: "dave", Amount: 4, At: base.Add(5 * time.Hour), Qty: 1, Consumers: 1}},
	})

	obs, ok := w.LookupBuyerLastPaid("alice", "bob", "ale")
	if !ok {
		t.Fatal("LookupBuyerLastPaid returned ok=false; expected match")
	}
	if obs.Amount != 3 {
		t.Errorf("returned Amount = %d, want 3 (most recent alice entry)", obs.Amount)
	}
	if !obs.At.Equal(base.Add(4 * time.Hour)) {
		t.Errorf("returned At = %v, want %v", obs.At, base.Add(4*time.Hour))
	}
}

// --- LookupSellerRecent ----------------------------------------------

func TestLookupSellerRecent_EmptyReturnsNil(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	if got := w.LookupSellerRecent("bob", "ale"); got != nil {
		t.Fatalf("LookupSellerRecent on empty book should return nil, got %v", got)
	}
}

func TestLookupSellerRecent_ReturnsAllBuyersChronological(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	key := PriceBookKey{SellerID: "bob", Item: "ale"}
	base := time.Now().UTC().Add(-10 * time.Hour)
	w.SeedPriceBook([]PriceBookSeedRecord{
		{Key: key, Observation: PriceObservation{BuyerID: "alice", Amount: 2, At: base, Qty: 1, Consumers: 1}},
		{Key: key, Observation: PriceObservation{BuyerID: "carol", Amount: 3, At: base.Add(time.Hour), Qty: 1, Consumers: 1}},
		{Key: key, Observation: PriceObservation{BuyerID: "alice", Amount: 2, At: base.Add(2 * time.Hour), Qty: 1, Consumers: 1}},
	})

	history := w.LookupSellerRecent("bob", "ale")
	if len(history) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(history))
	}
	if history[0].BuyerID != "alice" || history[1].BuyerID != "carol" || history[2].BuyerID != "alice" {
		t.Errorf("buyer order wrong: %+v", history)
	}
}

// --- ClonePriceBook --------------------------------------------------

func TestClonePriceBook_EmptyReturnsNil(t *testing.T) {
	if got := ClonePriceBook(nil); got != nil {
		t.Errorf("ClonePriceBook(nil) should return nil, got %v", got)
	}
	if got := ClonePriceBook(map[PriceBookKey]*RingBuffer[PriceObservation]{}); got != nil {
		t.Errorf("ClonePriceBook(empty) should return nil, got %v", got)
	}
}

func TestClonePriceBook_IsIndependent(t *testing.T) {
	src := map[PriceBookKey]*RingBuffer[PriceObservation]{
		{SellerID: "bob", Item: "ale"}: NewRingBuffer[PriceObservation](PriceBookRingCapacity),
	}
	src[PriceBookKey{SellerID: "bob", Item: "ale"}].Push(PriceObservation{
		BuyerID: "alice", Amount: 3, Qty: 1, Consumers: 1, At: time.Now().UTC(),
	})

	cp := ClonePriceBook(src)
	if cp == nil || cp[PriceBookKey{SellerID: "bob", Item: "ale"}].Len() != 1 {
		t.Fatal("clone missing seeded entry")
	}
	// Mutate source — clone must not change.
	src[PriceBookKey{SellerID: "bob", Item: "ale"}].Push(PriceObservation{
		BuyerID: "carol", Amount: 5, Qty: 1, Consumers: 1, At: time.Now().UTC(),
	})
	if cp[PriceBookKey{SellerID: "bob", Item: "ale"}].Len() != 1 {
		t.Errorf("clone affected by source mutation: clone len=%d, want 1",
			cp[PriceBookKey{SellerID: "bob", Item: "ale"}].Len())
	}
}

// --- Snapshot integration --------------------------------------------

func TestRepublish_IncludesPriceBookClone(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	key := PriceBookKey{SellerID: "bob", Item: "ale"}
	w.SeedPriceBook([]PriceBookSeedRecord{
		{Key: key, Observation: PriceObservation{BuyerID: "alice", Amount: 3, Qty: 1, Consumers: 1, At: time.Now().UTC()}},
	})
	w.republish()
	snap := w.Published()
	if snap.PriceBook == nil {
		t.Fatal("Snapshot.PriceBook should be populated after republish")
	}
	if snap.PriceBook[key].Len() != 1 {
		t.Errorf("Snapshot.PriceBook[key].Len() = %d, want 1", snap.PriceBook[key].Len())
	}
	// Mutating live world should not affect the snapshot.
	if _, err := RecordPriceObservation(key, PriceObservation{
		BuyerID: "carol", Amount: 5, Qty: 1, Consumers: 1, At: time.Now().UTC(),
	}).Fn(w); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if snap.PriceBook[key].Len() != 1 {
		t.Errorf("snapshot clone mutated after live world mutation: %d", snap.PriceBook[key].Len())
	}
}

func TestRepublish_EmptyPriceBookProducesNilSnapshotField(t *testing.T) {
	w := NewWorld(priceBookStubRepo())
	w.republish()
	snap := w.Published()
	if snap.PriceBook != nil {
		t.Errorf("empty PriceBook should snapshot as nil, got %v", snap.PriceBook)
	}
}

// TestBuyerCostBasis is the LLM-411 cost-basis lookup: what a seller actually paid for
// the goods on their shelf, averaged over their own buy history across every seller
// they restocked from. The zero cases are load-bearing — no purchase history means no
// cost basis, which is what leaves producers' wear untouched by the net-margin accrual.
func TestBuyerCostBasis(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	book := map[PriceBookKey]*RingBuffer[PriceObservation]{}
	push := func(seller ActorID, item ItemKind, obs PriceObservation) {
		key := PriceBookKey{SellerID: seller, Item: item}
		if book[key] == nil {
			book[key] = NewRingBuffer[PriceObservation](PriceBookRingCapacity)
		}
		book[key].Push(obs)
	}
	// Josiah's milk buys, from two different farms: 6 units for 6 coins, then 4 for 6.
	// Blended: 10 units, 12 coins → 1.2 coins/unit.
	push("elizabeth", "milk", PriceObservation{BuyerID: "josiah", Amount: 6, Qty: 6, Consumers: 1, At: at})
	push("moses", "milk", PriceObservation{BuyerID: "josiah", Amount: 6, Qty: 2, Consumers: 2, At: at})
	// Noise the lookup must exclude: another buyer's milk, Josiah's cheese, and Josiah's
	// own milk SALES (he is the seller on those, so they are not purchases).
	push("elizabeth", "milk", PriceObservation{BuyerID: "hannah", Amount: 40, Qty: 4, Consumers: 1, At: at})
	push("moses", "cheese", PriceObservation{BuyerID: "josiah", Amount: 40, Qty: 4, Consumers: 1, At: at})
	push("josiah", "milk", PriceObservation{BuyerID: "villager", Amount: 30, Qty: 3, Consumers: 1, At: at})

	cases := []struct {
		name  string
		buyer ActorID
		item  ItemKind
		units int64
		want  int64
	}{
		{"blended average over both suppliers", "josiah", "milk", 5, 6},  // 5 × 1.2
		{"rounds half-up on the whole quantity", "josiah", "milk", 3, 4}, // 3 × 1.2 = 3.6
		{"never bought it — no cost basis", "josiah", "porridge", 5, 0},  // producer/forager case
		{"never bought anything", "ezekiel", "milk", 5, 0},
		{"non-positive units", "josiah", "milk", 0, 0},
	}
	for _, c := range cases {
		if got := BuyerCostBasis(book, c.buyer, c.item, c.units); got != c.want {
			t.Errorf("%s: BuyerCostBasis(%s, %s, %d) = %d, want %d", c.name, c.buyer, c.item, c.units, got, c.want)
		}
	}

	if units, coins := BuyerPurchaseTotals(book, "josiah", "milk", at.Add(time.Hour)); units != 0 || coins != 0 {
		t.Errorf("BuyerPurchaseTotals past the cutoff = (%d, %d), want (0, 0)", units, coins)
	}
	if got := BuyerCostBasis(nil, "josiah", "milk", 5); got != 0 {
		t.Errorf("BuyerCostBasis over a nil book = %d, want 0", got)
	}
}
