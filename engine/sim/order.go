package sim

import "time"

// OrderID identifies one transactional order between buyer and seller.
type OrderID string

// OrderState is the macro-state of an order. Initial set covers the happy
// path; expand per pay/order subsystem port.
type OrderState string

const (
	OrderPending   OrderState = "pending"
	OrderPaid      OrderState = "paid"
	OrderDelivered OrderState = "delivered"
	OrderWithdrawn OrderState = "withdrawn"
)

// Order is the in-memory state of one buyer→seller transaction.
//
// In the rewrite, pay_ledger rows in Postgres are demoted to an append-only
// event log of payment attempts and outcomes; this Order struct is the live
// state machine inside the engine. Walker locomotion, deliberation results,
// and fulfillment progress all live as fields here (TODO during port).
type Order struct {
	ID        OrderID
	Buyer     ActorID
	Seller    ActorID
	Item      ItemKind
	Quantity  int
	Amount    int
	State     OrderState
	CreatedAt time.Time

	// TODO: fulfillment state machine fields ported with order_fulfillment.go.

	// Forward-compat for cross-realm orders. Empty in v1; populated by a
	// future orchestrator engine for cross-realm transactions (a Salem
	// actor buying from a Brunnfeld merchant, etc.).
	OriginRealmID      string
	DestinationRealmID string
}
