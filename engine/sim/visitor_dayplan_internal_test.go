package sim

import (
	"math/rand"
	"testing"
	"time"
)

// visitor_dayplan_internal_test.go — LLM-373 unit coverage for the day-plan
// helpers that are unexported (an internal test, package sim). Behavioral spawn /
// circuit coverage lives in the external visitor_test.go.

func dayplanWorld(t *testing.T) *World {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	w := &World{}
	w.Settings.DawnTime = "06:00"
	w.Settings.DuskTime = "18:00"
	w.Settings.Location = loc
	return w
}

func TestNextDaybreak(t *testing.T) {
	w := dayplanWorld(t)
	loc := w.Settings.Location

	// Afternoon (15:00) → dawn has passed today, so the next daybreak is tomorrow.
	afternoon := time.Date(2026, 7, 12, 15, 0, 0, 0, loc)
	if got, want := nextDaybreak(w, afternoon), time.Date(2026, 7, 13, 6, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("nextDaybreak(afternoon) = %v; want %v", got, want)
	}
	// Pre-dawn (05:00) → today's dawn is still ahead.
	predawn := time.Date(2026, 7, 12, 5, 0, 0, 0, loc)
	if got, want := nextDaybreak(w, predawn), time.Date(2026, 7, 12, 6, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("nextDaybreak(pre-dawn) = %v; want %v", got, want)
	}
	// Bad clock → one-day fallback (never a never-expiring visitor).
	bad := &World{}
	if got := nextDaybreak(bad, afternoon); !got.Equal(afternoon.Add(24 * time.Hour)) {
		t.Errorf("nextDaybreak(bad clock) = %v; want +24h fallback", got)
	}
}

func TestVisitorDaytime(t *testing.T) {
	w := dayplanWorld(t)
	loc := w.Settings.Location
	cases := []struct {
		hour int
		want bool
	}{
		{5, false},  // before dawn
		{6, true},   // dawn boundary (inclusive)
		{13, true},  // midday
		{17, true},  // late afternoon
		{18, false}, // dusk boundary (exclusive)
		{21, false}, // night
	}
	for _, c := range cases {
		now := time.Date(2026, 7, 12, c.hour, 0, 0, 0, loc)
		if got := visitorDaytime(w, now); got != c.want {
			t.Errorf("visitorDaytime(%02d:00) = %v; want %v", c.hour, got, c.want)
		}
	}
	// Bad clock fails open to daytime (the circuit keeps running; ExpiresAt bounds the stay).
	if !visitorDaytime(&World{}, time.Date(2026, 7, 12, 23, 0, 0, 0, loc)) {
		t.Error("visitorDaytime with a bad clock should fail open to daytime")
	}
}

func TestSeedVisitorPack(t *testing.T) {
	valid := map[ItemKind]bool{}
	for _, k := range visitorWareKinds {
		valid[k] = true
	}
	for seed := int64(0); seed < 50; seed++ {
		pack, purse := seedVisitorPack(rand.New(rand.NewSource(seed)))
		if len(pack) != 2 {
			t.Fatalf("seed %d: pack has %d ware kinds, want 2 distinct", seed, len(pack))
		}
		for kind, qty := range pack {
			if !valid[kind] {
				t.Errorf("seed %d: pack carries %q, not in visitorWareKinds", seed, kind)
			}
			if qty < 2 || qty > 6 {
				t.Errorf("seed %d: ware %q qty %d out of [2,6]", seed, kind, qty)
			}
		}
		if purse < 30 || purse > 50 {
			t.Errorf("seed %d: purse %d out of [30,50]", seed, purse)
		}
	}
}
