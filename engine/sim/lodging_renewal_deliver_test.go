package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// orderReadyBy reads an order's ReadyBy on the world goroutine.
func orderReadyBy(t *testing.T, w *sim.World, id sim.OrderID) time.Time {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		o := world.Orders[id]
		if o == nil {
			return time.Time{}, nil
		}
		return o.ReadyBy, nil
	}})
	if err != nil {
		t.Fatalf("orderReadyBy: %v", err)
	}
	return res.(time.Time)
}

// LLM-47 deliver-time backstop: delivering a second nights_stay for a night the
// buyer already holds (delivered) advances the second order to the next night, so
// two delivered (buyer, seller, ready_by) rows never collide on
// pay_ledger_lodging_active_once. The accept-time advance normally prevents this;
// this proves the engine never PERSISTS the duplicate even if a same-night order
// reaches delivery by another path.
func TestDeliverOrder_LodgingBackstopAdvancesSameNight(t *testing.T) {
	rooms := []*sim.Room{
		{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
		{ID: 3, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_2"},
	}
	w, stop := buildLodgingDeliverWorld(t, rooms)
	defer stop()

	at := time.Now().UTC()
	first := mintLodgingOrder(t, w, 42, 1, at)
	if _, err := w.Send(sim.DeliverOrder("hannah", first, at)); err != nil {
		t.Fatalf("deliver first: %v", err)
	}
	firstNight := orderReadyBy(t, w, first)

	// Same night, same buyer/seller — must be advanced one night at delivery.
	second := mintLodgingOrder(t, w, 43, 1, at)
	if _, err := w.Send(sim.DeliverOrder("hannah", second, at)); err != nil {
		t.Fatalf("deliver second: %v", err)
	}
	if got := orderState(t, w, second); got != sim.OrderStateDelivered {
		t.Fatalf("second order state = %q, want delivered", got)
	}
	secondNight := orderReadyBy(t, w, second)
	if !secondNight.Equal(firstNight.AddDate(0, 0, 1)) {
		t.Errorf("backstop: second order ready_by = %s, want %s (first night + 1)",
			secondNight.Format("2006-01-02"), firstNight.AddDate(0, 0, 1).Format("2006-01-02"))
	}
}
