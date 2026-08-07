package perception

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// buyer_last_paid_test.go — LLM-620. buyerLastPaidText used to emit
// PriceObservation.Amount bare ("~6 coins"), which is the TOTAL of one past
// transaction with no quantity attached. In the restock cue that total renders
// directly beneath a per-unit sentence, and the model read it as the unit price:
// Josiah Thorne, who had bought 38 wheat for 48 coins, refused to restock for hours
// on "a poor trade at six coins from James Farm when I sell at one".
//
// The cases below are chosen so the old behaviour and the new one give DIFFERENT
// answers wherever the fix matters — a fixture where total and unit coincide would
// pass against the bug.

func lastPaidSnap(obs sim.PriceObservation) *sim.Snapshot {
	buf := sim.NewRingBuffer[sim.PriceObservation](8)
	buf.Push(obs)
	return &sim.Snapshot{
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "moses", Item: "wheat"}: buf,
		},
	}
}

func TestBuyerLastPaidText(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		obs  sim.PriceObservation
		want string
		why  string
	}{
		{
			name: "single unit keeps the bare total",
			obs:  sim.PriceObservation{BuyerID: "josiah", Amount: 3, Qty: 1, Consumers: 1, At: at},
			want: "~3 coins",
			why:  "one unit — total and unit price coincide, so there is nothing to disambiguate",
		},
		{
			name: "the live Josiah shape reads per unit, not as a unit price of six",
			obs:  sim.PriceObservation{BuyerID: "josiah", Amount: 6, Qty: 6, Consumers: 1, At: at},
			want: "about 1 coin each",
			why:  "6 coins bought 6 sheaves; the old wording said '~6 coins' beside a sell price of 1",
		},
		{
			name: "a fraction is spoken, not rounded away",
			obs:  sim.PriceObservation{BuyerID: "josiah", Amount: 48, Qty: 38, Consumers: 1, At: at},
			want: "a little over 1 coin each",
			why:  "Josiah's real week: 48 coins for 38 units is 1.26, a genuine loss against a sell price of 1 — flattening it to 'about 1 coin' would hide exactly what the cue exists to show",
		},
		{
			name: "half and up rounds the phrase upward",
			obs:  sim.PriceObservation{BuyerID: "josiah", Amount: 9, Qty: 6, Consumers: 1, At: at},
			want: "nearly 2 coins each",
			why:  "1.5 must never understate cost — the failure mode is buying above the sell price without noticing",
		},
		{
			name: "a group order divides by Qty x Consumers",
			obs:  sim.PriceObservation{BuyerID: "josiah", Amount: 6, Qty: 2, Consumers: 3, At: at},
			want: "about 1 coin each",
			why:  "Qty is PER CONSUMER: 2 each for 3 recipients moved 6 units. Dividing by Qty alone gives 3 coins each and trebles the apparent price",
		},
		{
			name: "a malformed row falls back to the bare total",
			obs:  sim.PriceObservation{BuyerID: "josiah", Amount: 5, Qty: 0, Consumers: 0, At: at},
			want: "~5 coins",
			why:  "no usable unit count — say what is known rather than divide by zero",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buyerLastPaidText(lastPaidSnap(tc.obs), "josiah", "moses", "wheat", "ask the seller")
			if got != tc.want {
				t.Errorf("buyerLastPaidText() = %q, want %q\n%s", got, tc.want, tc.why)
			}
		})
	}
}

// A non-positive amount is not a price. Both ingestion paths reject one already
// (cascade/price_book.go on resolved.Amount <= 0; loadRecentPricesSQL on
// `offered_amount > 0`), but a fixture, migration or hand-seeded row reaches the
// book without passing either — so the render guards (code_review). The important
// half is that it SKIPS rather than returns: a bad newest row must not hide an
// older genuine price. Note the old bare-total code rendered "~0 coins", which at
// least looked suspect; per-unit prose would have said "about 0 coins each", which
// reads like a real free lunch.
func TestBuyerLastPaidTextSkipsNonPositiveAmounts(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	buf := sim.NewRingBuffer[sim.PriceObservation](8)
	// Oldest-first: a genuine purchase, then a junk row that arrived after it.
	buf.Push(sim.PriceObservation{BuyerID: "josiah", Amount: 6, Qty: 6, Consumers: 1, At: at})
	buf.Push(sim.PriceObservation{BuyerID: "josiah", Amount: 0, Qty: 4, Consumers: 1, At: at.Add(time.Hour)})
	snap := &sim.Snapshot{PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
		{SellerID: "moses", Item: "wheat"}: buf,
	}}
	if got := buyerLastPaidText(snap, "josiah", "moses", "wheat", "ask the seller"); got != "about 1 coin each" {
		t.Errorf("buyerLastPaidText() = %q, want %q — a zero-amount row must be skipped, not rendered as a free purchase, and must not hide the real price behind it", got, "about 1 coin each")
	}
	// Nothing but junk → the fallback, never "about 0 coins each".
	only := sim.NewRingBuffer[sim.PriceObservation](8)
	only.Push(sim.PriceObservation{BuyerID: "josiah", Amount: 0, Qty: 4, Consumers: 1, At: at})
	onlySnap := &sim.Snapshot{PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
		{SellerID: "moses", Item: "wheat"}: only,
	}}
	if got := buyerLastPaidText(onlySnap, "josiah", "moses", "wheat", "ask the seller"); got != "ask the seller" {
		t.Errorf("buyerLastPaidText() = %q, want the fallback — with no valid observation the buyer has no remembered price", got)
	}
}

// Price knowledge is per-buyer and per-(seller, item): patronage earns the number.
// These arms were unwritten before LLM-620 touched the function, and pin the
// fallback so the per-unit change cannot quietly start answering for a buyer who
// never traded here.
func TestBuyerLastPaidTextFallbacks(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	snap := lastPaidSnap(sim.PriceObservation{BuyerID: "josiah", Amount: 6, Qty: 6, Consumers: 1, At: at})
	const fallback = "ask the seller"
	t.Run("a different buyer has not earned the price", func(t *testing.T) {
		if got := buyerLastPaidText(snap, "hannah", "moses", "wheat", fallback); got != fallback {
			t.Errorf("got %q, want the fallback %q — price knowledge is per-buyer", got, fallback)
		}
	})
	t.Run("a seller never bought from", func(t *testing.T) {
		if got := buyerLastPaidText(snap, "josiah", "elizabeth", "wheat", fallback); got != fallback {
			t.Errorf("got %q, want the fallback %q", got, fallback)
		}
	})
	t.Run("an item never bought here", func(t *testing.T) {
		if got := buyerLastPaidText(snap, "josiah", "moses", "flour", fallback); got != fallback {
			t.Errorf("got %q, want the fallback %q", got, fallback)
		}
	})
	t.Run("no seller resolved", func(t *testing.T) {
		if got := buyerLastPaidText(snap, "josiah", "", "wheat", fallback); got != fallback {
			t.Errorf("got %q, want the fallback %q", got, fallback)
		}
	})
	t.Run("no price book at all", func(t *testing.T) {
		if got := buyerLastPaidText(&sim.Snapshot{}, "josiah", "moses", "wheat", fallback); got != fallback {
			t.Errorf("got %q, want the fallback %q", got, fallback)
		}
	})
}
