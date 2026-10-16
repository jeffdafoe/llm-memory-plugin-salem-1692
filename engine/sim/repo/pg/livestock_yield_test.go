package pg

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-797: the actor.livestock_yield column round-trips a cow's stock, regen
// anchor and pending feed bonus, and a NULL column is no yield.
func TestLivestockYieldRoundTrip(t *testing.T) {
	avail, max, period := 2, 4, 12
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	in := &sim.LivestockYield{
		Row: sim.ObjectRefresh{
			GatherItem: "milk", AvailableQuantity: &avail, MaxQuantity: &max,
			RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: &period, LastRefreshAt: &at,
		},
		FedBonus: 4,
	}
	blob, ok := encodeLivestockYield(in).([]byte)
	if !ok {
		t.Fatal("encode returned no blob")
	}
	out, err := decodeLivestockYield(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if *out.Row.AvailableQuantity != 2 || *out.Row.MaxQuantity != 4 || *out.Row.RefreshPeriodHours != 12 ||
		out.Row.RefreshMode != sim.RefreshModePeriodic || out.Row.GatherItem != "milk" ||
		!out.Row.LastRefreshAt.Equal(at) || out.FedBonus != 4 {
		t.Errorf("round trip = %+v / %+v", out.Row, out)
	}
	if encodeLivestockYield(nil) != nil {
		t.Error("nil yield should encode as NULL")
	}
	if y, err := decodeLivestockYield(nil); y != nil || err != nil {
		t.Errorf("NULL decode = %v, %v", y, err)
	}
	if _, err := decodeLivestockYield([]byte(`{"gather_item":"milk","available_quantity":1,"max_quantity":0,"refresh_mode":"periodic","refresh_period_hours":12}`)); err == nil {
		t.Error("max 0 should be rejected")
	}
}
