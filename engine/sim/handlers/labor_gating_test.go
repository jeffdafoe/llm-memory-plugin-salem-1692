package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// labor_gating_test.go — LLM-26. solicit_work is advertised only to a free
// worker with an audience (CanSolicitWork); accept_work/decline_work only to an
// employer with a pending labor offer (PendingLaborOffers). Same advertising-
// only posture + cue/tool-lockstep as the pay gates.

func laborGatingRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	for name, fn := range map[string]func(*Registry) error{
		"speak":   RegisterSpeak,
		"labor":   RegisterLaborFamily,
		"move_to": RegisterMoveTo,
		"stop":    RegisterStop,
	} {
		if err := fn(r); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("RegisterTerminal: %v", err)
	}
	return r
}

// laborOfferPayload builds a payload whose standing labor view carries one
// pending offer per supplied labor id (the employer-side decision view).
func laborOfferPayload(ids ...sim.LaborID) perception.Payload {
	var offers []perception.LaborOfferView
	for _, id := range ids {
		offers = append(offers, perception.LaborOfferView{
			LaborID: id, Worker: "ezekiel", Reward: 10, DurationMin: 30,
		})
	}
	return perception.Payload{ActorID: "josiah", LaborOffersForMe: offers, Surroundings: speakAudience()}
}

func TestGateTools_NoLaborOffer_DropsResponseTools(t *testing.T) {
	r := laborGatingRegistry(t)
	specs := gateTools(r, perception.Payload{ActorID: "josiah", Surroundings: speakAudience()}, nil)
	names := specNameSet(specs)

	for _, gated := range []string{"accept_work", "decline_work"} {
		if names[gated] != 0 {
			t.Errorf("%q advertised with no pending labor offer (count %d)", gated, names[gated])
		}
	}
	// speak/done stay; solicit_work absent (not a worker here).
	if names["speak"] != 1 || names["done"] != 1 {
		t.Errorf("speak/done should always be advertised; got speak=%d done=%d", names["speak"], names["done"])
	}
	if names["solicit_work"] != 0 {
		t.Errorf("solicit_work advertised to a non-worker (count %d)", names["solicit_work"])
	}
}

func TestGateTools_PendingLaborOffer_AddsResponseTools(t *testing.T) {
	r := laborGatingRegistry(t)
	specs := gateTools(r, laborOfferPayload(1), nil)
	names := specNameSet(specs)

	for _, want := range []string{"accept_work", "decline_work"} {
		if names[want] != 1 {
			t.Errorf("%q should be advertised with a pending labor offer; count %d", want, names[want])
		}
	}
}

func TestGateTools_CanSolicitWork_AdvertisesSolicitWork(t *testing.T) {
	r := laborGatingRegistry(t)

	// Free worker with an audience: solicit_work advertised.
	on := perception.Payload{ActorID: "ezekiel", CanSolicitWork: true, Surroundings: speakAudience()}
	if specNameSet(gateTools(r, on, nil))["solicit_work"] != 1 {
		t.Errorf("solicit_work should be advertised when CanSolicitWork is true")
	}

	// Not a free worker (or no audience): solicit_work dropped.
	off := perception.Payload{ActorID: "ezekiel", CanSolicitWork: false, Surroundings: speakAudience()}
	if specNameSet(gateTools(r, off, nil))["solicit_work"] != 0 {
		t.Errorf("solicit_work should be dropped when CanSolicitWork is false")
	}
}

func TestGateTools_Moving_DropsSolicitWork(t *testing.T) {
	r := laborGatingRegistry(t)
	// CanSolicitWork is true, but the actor is mid-walk — SolicitWork rejects on
	// MoveIntent, so the walk gate drops solicit_work.
	payload := perception.Payload{ActorID: "ezekiel", CanSolicitWork: true, Surroundings: speakAudience()}
	specs := gateTools(r, payload, movingSnap("ezekiel", true))
	if specNameSet(specs)["solicit_work"] != 0 {
		t.Errorf("solicit_work advertised while moving — want it gated out")
	}
}
