package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// orders_recent_prices_integration_test.go — real-pg validation for the
// loadRecentPricesSQL barter guard (LLM-285). The pgxmock unit tests hand
// LoadRecentPrices canned rows and only assert the SQL shape + arg binding;
// they can NOT exercise the WHERE clause, so the `offered_amount > 0` filter
// that keeps zero-coin barter settlements out of the price-book seed needs a
// genuine round-trip against the migrated schema.
//
// Background: the price book has two ingestion paths that must agree. The
// runtime subscriber (handlePayWithItemResolvedPriceBook,
// engine/sim/cascade/price_book.go) skips Amount <= 0 (ZBBS-HOME-393) so a
// pure goods-for-goods barter never records a "free" coin price. The boot
// seed here previously lacked the mirror guard, so every engine restart
// re-imported accepted amount-0 rows and re-poisoned the book — surfacing as
// "~0 coins" restock cues. This test pins both halves of the fix:
//
//   - an accepted amount-0 row yields no observation (pure barter excluded);
//   - a (seller, item) key with a mix of barter and coin accepts keeps only
//     the coin observation.
//
// LLM-493 extended both paths to also drop MIXED coin+goods settlements. Such a
// row carries offered_amount > 0 and used to be KEPT, recorded at its coin leg
// against the full quantity — live, 5 nails bought for 2 coins PLUS 2 skillets
// and 2 wheat seeded nails at 0.4 coins each. Worse than the pure-barter gap:
// pure barter leaves the key silent, mixed leaves it WRONG, and the wrong rate
// propagates into every buy anchor and margin verdict derived from it. The seed
// identifies those rows by pay_ledger.pay_items, the column that migration added
// precisely because this query cannot otherwise see the goods legs.
func TestOrdersRepo_Integration_LoadRecentPrices_SkipsZeroCoinBarter(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// pay_ledger.item_kind carries a real FK to the item_kind reference table,
	// so the kinds these rows reference must exist first. Only name /
	// display_label / category are NOT NULL.
	for _, k := range []struct{ name, label, category string }{
		{"skillet", "Skillet", "tool"},
		{"porridge", "Porridge", "food"},
	} {
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO item_kind (name, display_label, category) VALUES ($1, $2, $3)`,
			k.name, k.label, k.category,
		); err != nil {
			t.Fatalf("seed item_kind %q: %v", k.name, err)
		}
	}

	// pay_ledger.buyer_id / seller_id are plain text (no actor FK), so no
	// actor seeding is needed. created_at is stamped `now` on every row and
	// the query's `since` floor sits a day back, so all rows fall inside the
	// window and the guard is the only thing that can drop one.
	now := time.Now().UTC()
	// payItems is the jsonb goods-leg column, nil for a pure-coin settlement
	// (LLM-493). NULL rather than '[]' is the pure-coin sentinel — payItemsJSON
	// writes it that way and the seed predicate keys on IS NULL.
	insert := func(id int64, seller, item, buyer string, amount int, payItems any) {
		t.Helper()
		// state='accepted' requires a non-null resolved_at (pay_ledger_check:
		// state='pending' iff resolved_at IS NULL). offered_amount has a
		// CHECK (>= 0), so 0 is a legal stored value — which is exactly why
		// the seed has to filter it rather than rely on the schema.
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO pay_ledger
			     (id, buyer_id, seller_id, item_kind, qty, offered_amount,
			      consumer_actor_ids, consume_now, state, fulfillment_status,
			      ready_by, created_at, resolved_at, pay_items)
			 VALUES ($1, $2, $3, $4, 1, $5,
			         $6, false, 'accepted', 'delivered',
			         $7::date, $7, $7, $8)`,
			id, buyer, seller, item, amount, []string{buyer}, now, payItems,
		); err != nil {
			t.Fatalf("insert pay_ledger id=%d: %v", id, err)
		}
	}

	const (
		ezekiel  = "ezekiel-crane"
		hannah   = "hannah-boggs"
		john     = "john-ellis"
		prudence = "prudence-ward"
	)

	// (Ezekiel, skillet): a zero-coin barter, a 5-coin sale, and a MIXED
	// coin+goods accept. Only the pure-coin sale should seed a price. The mixed
	// row is the LLM-493 case — it carries offered_amount > 0 and would pass the
	// old guard, so if the pay_items predicate is dropped this row leaks in at
	// 2 coins and the assertions below fail on both count and amount.
	insert(1, ezekiel, "skillet", john, 0, nil)
	insert(2, ezekiel, "skillet", john, 5, nil)
	insert(4, ezekiel, "skillet", john, 2, []byte(`[{"item":"wheat","qty":2}]`))
	// (Hannah, porridge): a lone zero-coin barter. The whole key must yield
	// no observation — a barter-only key seeds nothing.
	insert(3, hannah, "porridge", prudence, 0, nil)

	repo := NewOrdersRepo(f.Pool)
	since := now.Add(-24 * time.Hour)
	got, err := repo.LoadRecentPrices(ctx, since, 10)
	if err != nil {
		t.Fatalf("LoadRecentPrices: %v", err)
	}

	// No amount-0 observation may survive, no goods-bearing settlement may survive,
	// and the porridge key (barter-only) must be absent entirely.
	for _, r := range got {
		if r.Observation.Amount == 0 {
			t.Errorf("amount-0 barter leaked into seed: key=%+v obs=%+v", r.Key, r.Observation)
		}
		if r.Observation.Amount == 2 {
			t.Errorf("mixed coin+goods settlement leaked into seed at its coin leg (LLM-493): key=%+v obs=%+v", r.Key, r.Observation)
		}
		if r.Key == (sim.PriceBookKey{SellerID: hannah, Item: "porridge"}) {
			t.Errorf("barter-only (Hannah, porridge) key seeded an observation: %+v", r.Observation)
		}
	}

	// Exactly one observation survives: the 5-coin skillet sale.
	if len(got) != 1 {
		t.Fatalf("LoadRecentPrices returned %d observations, want 1 (only the coin sale): %+v", len(got), got)
	}
	only := got[0]
	if only.Key.SellerID != ezekiel || only.Key.Item != "skillet" {
		t.Errorf("surviving key = %+v, want {ezekiel-crane skillet}", only.Key)
	}
	if only.Observation.Amount != 5 {
		t.Errorf("surviving observation amount = %d, want 5", only.Observation.Amount)
	}
}

// TestOrdersRepo_Integration_BothIngestionPathsAgreeOnPayItems is the LLM-285
// invariant, re-pinned for LLM-493's extension of it: the boot seed and the live
// subscriber must classify the SAME settlement the same way.
//
// The two paths read different sources — the subscriber reads a PayWithItemResolved
// event in memory, the seed reads a pay_ledger row — so nothing structural keeps
// them aligned; only matching predicates do, and they have drifted before. When
// they disagree the failure is nasty and quiet: a settlement the live engine
// declined gets re-imported at the next boot, so a rate silently changes across a
// deploy and the village restarts several times a day.
//
// This walks the three payment shapes through BOTH classifiers and asserts they
// return the same verdict for each. The subscriber's rule is expressed here as the
// predicate it implements rather than by invoking it (that package is not
// importable from here without a cycle); the unit matrix
// TestHandlePayWithItemResolvedPriceBook_RecordsOnlyPureCoinSettlements pins that
// the subscriber really behaves this way, and this test pins that the SQL agrees.
func TestOrdersRepo_Integration_BothIngestionPathsAgreeOnPayItems(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO item_kind (name, display_label, category) VALUES ('nail', 'Nail', 'tool')`,
	); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}

	now := time.Now().UTC()
	cases := []struct {
		name     string
		id       int64
		seller   string
		amount   int
		payItems any
	}{
		{name: "pure coin", id: 1, seller: "seller-coin", amount: 5, payItems: nil},
		{name: "pure barter", id: 2, seller: "seller-barter", amount: 0,
			payItems: []byte(`[{"item":"wheat","qty":2}]`)},
		{name: "mixed coin+goods", id: 3, seller: "seller-mixed", amount: 2,
			payItems: []byte(`[{"item":"skillet","qty":2},{"item":"wheat","qty":2}]`)},
	}
	for _, c := range cases {
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO pay_ledger
			     (id, buyer_id, seller_id, item_kind, qty, offered_amount,
			      consumer_actor_ids, consume_now, state, fulfillment_status,
			      ready_by, created_at, resolved_at, pay_items)
			 VALUES ($1, 'buyer', $2, 'nail', 5, $3,
			         $4, false, 'accepted', 'delivered',
			         $5::date, $5, $5, $6)`,
			c.id, c.seller, c.amount, []string{"buyer"}, now, c.payItems,
		); err != nil {
			t.Fatalf("insert %s: %v", c.name, err)
		}
	}

	repo := NewOrdersRepo(f.Pool)
	got, err := repo.LoadRecentPrices(ctx, now.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("LoadRecentPrices: %v", err)
	}
	seeded := map[string]bool{}
	for _, r := range got {
		seeded[string(r.Key.SellerID)] = true
	}

	for _, c := range cases {
		// The subscriber's guard, verbatim in Go:
		//   if resolved.Amount <= 0 || len(resolved.PayItems) > 0 { return }
		hasGoods := c.payItems != nil
		subscriberWouldRecord := c.amount > 0 && !hasGoods
		seedRecorded := seeded[c.seller]

		if subscriberWouldRecord != seedRecorded {
			t.Errorf("%s: ingestion paths DISAGREE — subscriber would record=%v, seed recorded=%v. "+
				"A divergence here means a restart re-imports (or drops) what the live engine did not (LLM-285/LLM-493)",
				c.name, subscriberWouldRecord, seedRecorded)
		}
	}

	// Non-vacuity: if the seed dropped everything the loop above would pass while
	// proving nothing about the coin path.
	if !seeded["seller-coin"] {
		t.Error("the pure-coin settlement was not seeded — invariant is vacuous")
	}
}
