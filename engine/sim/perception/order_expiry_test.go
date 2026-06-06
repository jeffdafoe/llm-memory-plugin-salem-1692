package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// order_expiry_test.go — ZBBS-HOME-357. A legacy v1 order row has a NULL
// expires_at, which the PG loader substitutes with the 9999-12-31 sentinel
// (repo/pg/orders.go). Fed to humanizeDurationUntil, time.Time.Sub saturates at
// MaxInt64 ns → "expires in 153722867 minutes" (~292 years) in a live NPC's
// prompt. expiryClause must omit the clause for that sentinel (and any
// implausibly-far / overflowing deadline) while still rendering a real TTL.

// theSentinel mirrors the PG loader's NULL-expires_at substitute.
var theSentinel = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

func TestExpiryClause(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		deadline time.Time
		wantOK   bool
		wantSub  string
	}{
		{"real ttl renders", now.Add(5 * time.Minute), true, "expires in 5 minutes"},
		{"zero omitted", time.Time{}, false, ""},
		{"far-future sentinel omitted", theSentinel, false, ""},
		{"at horizon renders", now.Add(maxRenderableExpiryHorizon), true, "expires in"},
		{"just past horizon omitted", now.Add(maxRenderableExpiryHorizon + time.Nanosecond), false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clause, ok := expiryClause(c.deadline, now)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (clause=%q)", ok, c.wantOK, clause)
			}
			if c.wantSub != "" && !strings.Contains(clause, c.wantSub) {
				t.Errorf("clause = %q, want substring %q", clause, c.wantSub)
			}
			if strings.Contains(clause, "153722867") {
				t.Errorf("garbage duration leaked: %q", clause)
			}
			if !c.wantOK && clause != "" {
				t.Errorf("omitted expiry returned non-empty clause: %q", clause)
			}
		})
	}
}

// TestRenderPendingDeliveries_SentinelOmitsExpiry is the end-to-end guard: a
// rendered delivery line for a sentinel-expiry order carries the order, but NO
// "expires in" clause and NONE of the garbage duration. ZBBS-HOME-357.
func TestRenderPendingDeliveries_SentinelOmitsExpiry(t *testing.T) {
	view := []OrderView{{
		ID: 102, Item: "milk", Qty: 4,
		BuyerName: "John Ellis", SellerName: "Hannah",
		ExpiresAt: theSentinel,
	}}

	var from strings.Builder
	renderPendingDeliveriesFromMe(&from, view, startOfUTCDay(time.Now()))
	var to strings.Builder
	renderPendingDeliveriesToMe(&to, view, startOfUTCDay(time.Now()))

	for _, out := range []string{from.String(), to.String()} {
		if !strings.Contains(out, "#102") || !strings.Contains(out, "milk") {
			t.Errorf("order line missing:\n%s", out)
		}
		if strings.Contains(out, "expires in") {
			t.Errorf("sentinel expiry must omit the 'expires in' clause:\n%s", out)
		}
		if strings.Contains(out, "153722867") {
			t.Errorf("the garbage duration leaked into the prompt:\n%s", out)
		}
	}
}

// TestRenderPendingDeliveries_RealTTLStillRenders guards against over-correction
// — a normal order TTL must still show its expiry. ZBBS-HOME-357.
func TestRenderPendingDeliveries_RealTTLStillRenders(t *testing.T) {
	view := []OrderView{{
		ID: 7, Item: "stew", SellerName: "Hannah", BuyerName: "Jeff",
		ExpiresAt: time.Now().Add(8 * time.Minute),
	}}
	var b strings.Builder
	renderPendingDeliveriesToMe(&b, view, startOfUTCDay(time.Now()))
	if !strings.Contains(b.String(), "expires in") {
		t.Errorf("a real TTL must still render its expiry:\n%s", b.String())
	}
	_ = sim.OrderID(7) // keep the sim import meaningful if the literal type changes
}
