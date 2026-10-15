package sim

import (
	"errors"
	"math/rand"
	"time"
)

// visitor_spawn_force.go — the operator's lever to bring a visitor in now
// (LLM-790), the arrival side of DepartVisitor. A traveler otherwise arrives
// only on the cascade's afternoon rolls, so testing anything a visitor does
// meant waiting for one.

// Visitor kinds ForceVisitorSpawn takes.
const (
	ForceVisitorPasser = "passer"
	ForceVisitorBuyer  = "buyer"
	ForceVisitorSeller = "seller"
)

var (
	// ErrUnknownVisitorKind — the kind is not passer, buyer or seller.
	ErrUnknownVisitorKind = errors.New(`kind must be "passer", "buyer" or "seller"`)
	// ErrVisitorNotSpawned — the arrival bailed out: every persona of the class
	// is already in the village, or no destination or edge tile could be had.
	// The engine log says which.
	ErrVisitorNotSpawned = errors.New("no visitor could arrive now (see the engine log)")
)

// ForcedVisitor is the visitor ForceVisitorSpawn brought in.
type ForcedVisitor struct {
	ID          ActorID
	DisplayName string
	Archetype   string
	// Merchant is whether he came on a bound trade errand. A buyer or seller
	// whose errand the economy cannot service arrives a passer-through.
	Merchant bool
}

// ForceVisitorSpawn brings a visitor in now: a passer-through, or a merchant
// buying or selling. No afternoon window, no concurrent cap, no roll, and
// no carter or shortage peddler in his place; the arrival itself — persona
// (a due returner first, as on the cascade), errand, edge tile, broadcast and
// walk in — is the cascade's own spawnVisitor. kind "" is a passer-through.
func ForceVisitorSpawn(kind string, now time.Time, r *rand.Rand) Command {
	return Command{Fn: func(w *World) (any, error) {
		roll := visitorSpawnRoll{Class: visitorSpawnPasser}
		switch kind {
		case "", ForceVisitorPasser:
		case ForceVisitorBuyer:
			roll = visitorSpawnRoll{Class: visitorSpawnMerchant, Direction: TradeDirectionBuy}
		case ForceVisitorSeller:
			roll = visitorSpawnRoll{Class: visitorSpawnMerchant, Direction: TradeDirectionSell}
		default:
			return nil, ErrUnknownVisitorKind
		}
		before := make(map[ActorID]bool)
		for id, a := range w.Actors {
			if a != nil && a.VisitorState != nil {
				before[id] = true
			}
		}
		var t VisitorCascadeTelemetry
		dispatchVisitorSpawn(w, VisitorTickInputs{Now: now, Rand: r, force: &roll}, &t)
		for id, a := range w.Actors {
			if a == nil || a.VisitorState == nil || before[id] {
				continue
			}
			return ForcedVisitor{
				ID:          id,
				DisplayName: a.DisplayName,
				Archetype:   a.VisitorState.Archetype,
				Merchant:    a.VisitorState.Trade != nil,
			}, nil
		}
		return nil, ErrVisitorNotSpawned
	}}
}
