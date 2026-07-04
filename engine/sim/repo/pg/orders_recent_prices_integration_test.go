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
//     the coin observation — the seed matches the subscriber's accepted
//     tradeoff exactly (mixed coin+goods accepts carry offered_amount > 0 and
//     are kept), it does not tighten it.
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
	insert := func(id int64, seller, item, buyer string, amount int) {
		t.Helper()
		// state='accepted' requires a non-null resolved_at (pay_ledger_check:
		// state='pending' iff resolved_at IS NULL). offered_amount has a
		// CHECK (>= 0), so 0 is a legal stored value — which is exactly why
		// the seed has to filter it rather than rely on the schema.
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO pay_ledger
			     (id, buyer_id, seller_id, item_kind, qty, offered_amount,
			      consumer_actor_ids, consume_now, state, fulfillment_status,
			      ready_by, created_at, resolved_at)
			 VALUES ($1, $2, $3, $4, 1, $5,
			         $6, false, 'accepted', 'delivered',
			         $7::date, $7, $7)`,
			id, buyer, seller, item, amount, []string{buyer}, now,
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

	// (Ezekiel, skillet): a zero-coin barter followed by a 5-coin sale. Only
	// the coin sale should seed a price — the mixed-window case.
	insert(1, ezekiel, "skillet", john, 0)
	insert(2, ezekiel, "skillet", john, 5)
	// (Hannah, porridge): a lone zero-coin barter. The whole key must yield
	// no observation — a barter-only key seeds nothing.
	insert(3, hannah, "porridge", prudence, 0)

	repo := NewOrdersRepo(f.Pool)
	since := now.Add(-24 * time.Hour)
	got, err := repo.LoadRecentPrices(ctx, since, 10)
	if err != nil {
		t.Fatalf("LoadRecentPrices: %v", err)
	}

	// No amount-0 observation may survive, and the porridge key (barter-only)
	// must be absent entirely.
	for _, r := range got {
		if r.Observation.Amount == 0 {
			t.Errorf("amount-0 barter leaked into seed: key=%+v obs=%+v", r.Key, r.Observation)
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
