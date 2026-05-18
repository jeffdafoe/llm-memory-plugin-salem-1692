package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// OrdersRepo is an in-memory implementation of sim.OrdersRepo, same
// shape as ActorsRepo / HuddlesRepo. Replaces the notImplOrders stub
// (Slice 5).
//
// Mirrors how mem repos behave for tests: Seed populates initial
// state, LoadAll deep-clones via CloneOrder, SaveSnapshot replaces
// the map wholesale so the in-memory "checkpoint" matches what a
// real pg SaveSnapshot would express semantically (a complete
// re-statement of the persisted set).
type OrdersRepo struct {
	orders map[sim.OrderID]*sim.Order
}

func NewOrdersRepo() *OrdersRepo {
	return &OrdersRepo{orders: make(map[sim.OrderID]*sim.Order)}
}

func (r *OrdersRepo) Seed(orders map[sim.OrderID]*sim.Order) {
	for id, o := range orders {
		r.orders[id] = sim.CloneOrder(o)
	}
}

func (r *OrdersRepo) LoadAll(_ context.Context) (map[sim.OrderID]*sim.Order, error) {
	out := make(map[sim.OrderID]*sim.Order, len(r.orders))
	for id, o := range r.orders {
		out[id] = sim.CloneOrder(o)
	}
	return out, nil
}

func (r *OrdersRepo) SaveSnapshot(_ context.Context, _ sim.Tx, orders map[sim.OrderID]*sim.Order) error {
	r.orders = make(map[sim.OrderID]*sim.Order, len(orders))
	for id, o := range orders {
		r.orders[id] = sim.CloneOrder(o)
	}
	return nil
}
