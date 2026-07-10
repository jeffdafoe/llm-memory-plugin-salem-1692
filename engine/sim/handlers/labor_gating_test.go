package handlers

import (
	"testing"
	"time"

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

// workOfferedToMePayload builds the LLM-346 worker-side decision view: an employer
// has offered the subject a job and is waiting on their answer. The subject is the
// WORKER, so the tools must be advertised off the standing offer alone — no
// affordance, no comfort ceiling, no coin threshold in the way.
func workOfferedToMePayload(ids ...sim.LaborID) perception.Payload {
	var offers []perception.LaborOfferView
	for _, id := range ids {
		offers = append(offers, perception.LaborOfferView{
			LaborID: id, Worker: "lewis", Employer: "prudence",
			EmployerInitiated: true, Reward: 4, DurationMin: 240,
		})
	}
	return perception.Payload{ActorID: "lewis", LaborOffersForMe: offers, Surroundings: speakAudience()}
}

// TestGateTools_WorkOfferedToWorker_AddsResponseTools is the LLM-346 gate. Lewis
// Walker's live deliberation prompt advertised no work-taking tool at all while
// Prudence Ward's request sat unanswered in front of him. The response tools ride
// the standing offer view, which is now direction-agnostic, so a worker who has
// been asked can answer.
func TestGateTools_WorkOfferedToWorker_AddsResponseTools(t *testing.T) {
	r := laborGatingRegistry(t)
	names := specNameSet(gateTools(r, workOfferedToMePayload(1), nil))
	for _, want := range []string{"accept_work", "decline_work"} {
		if names[want] != 1 {
			t.Errorf("%q should be advertised to a worker holding an offer of work; count %d", want, names[want])
		}
	}
	// He is not also handed the hiring verb — nobody here is his to hire.
	if names["offer_work"] != 0 {
		t.Errorf("offer_work advertised with no hireable worker present; count %d", names["offer_work"])
	}
}

// TestGateTools_HireableWorkers_AdvertisesOfferWork pins the cue/tool lockstep:
// offer_work rides the SAME HireableWorkers slice renderOfferWorkAffordance names
// its targets from, so a keeper is never handed a hiring verb with nobody in the
// room to hire (LLM-346).
func TestGateTools_HireableWorkers_AdvertisesOfferWork(t *testing.T) {
	r := laborGatingRegistry(t)

	on := perception.Payload{ActorID: "prudence", HireableWorkers: []sim.ActorID{"lewis"}, Surroundings: speakAudience()}
	if specNameSet(gateTools(r, on, nil))["offer_work"] != 1 {
		t.Error("offer_work should be advertised when a hireable worker is co-present")
	}

	off := perception.Payload{ActorID: "prudence", Surroundings: speakAudience()}
	if specNameSet(gateTools(r, off, nil))["offer_work"] != 0 {
		t.Error("offer_work should be dropped when no hireable worker is co-present")
	}
}

func TestGateTools_Moving_DropsOfferWork(t *testing.T) {
	r := laborGatingRegistry(t)
	// A hireable worker is present, but the keeper is mid-walk — OfferWork rejects
	// on MoveIntent, so the walk gate drops the tool rather than burn a doomed call.
	payload := perception.Payload{ActorID: "prudence", HireableWorkers: []sim.ActorID{"lewis"}, Surroundings: speakAudience()}
	if specNameSet(gateTools(r, payload, movingSnap("prudence", true)))["offer_work"] != 0 {
		t.Error("offer_work advertised while moving — want it gated out")
	}
}

// TestGateTools_LaboringWorker_DropsOfferWork: a hand mid-job does not subcontract
// the shelves to a passer-by. The offer_work gate only asks about the TARGET, so
// the laboring strip (laborAbandonTools) is what disqualifies the caller (LLM-346).
func TestGateTools_LaboringWorker_DropsOfferWork(t *testing.T) {
	r := laborGatingRegistry(t)
	payload := perception.Payload{
		ActorID:         "lewis",
		HireableWorkers: []sim.ActorID{"patience"},
		Laboring:        &perception.LaboringView{Employer: "prudence"},
		Surroundings:    speakAudience(),
	}
	if specNameSet(gateTools(r, payload, nil))["offer_work"] != 0 {
		t.Error("offer_work advertised to a worker mid-job — want it stripped with the other commerce tools")
	}
}

// laborSpeakOnlyRegistry adds the commerce + consume tools on top of the labor
// set so the LLM-230 speak-only gate can be observed stripping the abandon tools.
func laborSpeakOnlyRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	for name, fn := range map[string]func(*Registry) error{
		"speak":         RegisterSpeak,
		"move_to":       RegisterMoveTo,
		"consume":       RegisterConsume,
		"pay":           RegisterPay,
		"pay_with_item": RegisterPayWithItem,
		"offer_trade":   RegisterOfferTrade,
		"scene_quote":   RegisterSceneQuote,
	} {
		if err := fn(r); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("RegisterTerminal: %v", err)
	}
	// Precondition: the seller-quote tool advertises under the model-facing name
	// "sell" (RegisterSceneQuote, renamed in LLM-184), which is the exact name
	// laborAbandonTools strips. If the registry ever stops exposing "sell" the
	// gating tests below would pass/fail for the wrong reason — fail loudly here.
	if specNameSet(r.AdvertisedSpecs())["sell"] != 1 {
		t.Fatalf("RegisterSceneQuote did not advertise \"sell\"; laborAbandonTools gates a name the registry doesn't expose. names=%v", specNameSet(r.AdvertisedSpecs()))
	}
	return r
}

// laboringPayload builds a payload for a worker mid-job. redNeed controls whether
// a red-tier hunger need is present — the one case that keeps move_to (break off
// to eat), per the reactor's hunger/thirst carve-out.
func laboringPayload(redNeed bool) perception.Payload {
	hunger := 5
	if redNeed {
		hunger = 22 // >= the 20 threshold, below NeedMax (24): the NeedRed tier
	}
	return perception.Payload{
		ActorID:      "patience",
		Laboring:     &perception.LaboringView{Employer: "john", Until: time.Now().Add(time.Hour)},
		Surroundings: speakAudience(),
		Actor: perception.ActorView{
			Needs:          map[sim.NeedKey]int{"hunger": hunger, "thirst": 5, "tiredness": 5},
			NeedThresholds: sim.NeedThresholds{"hunger": 20, "thirst": 20, "tiredness": 20},
		},
	}
}

func TestGateTools_Laboring_SpeakOnlySurface(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	names := specNameSet(gateTools(r, laboringPayload(false), nil))

	for _, gated := range []string{"pay", "pay_with_item", "offer_trade", "sell", "move_to"} {
		if names[gated] != 0 {
			t.Errorf("%q advertised to a laboring worker; want it stripped (speak-only surface, LLM-230)", gated)
		}
	}
	for _, keep := range []string{"speak", "consume", "done"} {
		if names[keep] != 1 {
			t.Errorf("%q should stay advertised to a laboring worker; count %d", keep, names[keep])
		}
	}
}

func TestGateTools_LaboringWithRedNeed_KeepsMoveTo(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	names := specNameSet(gateTools(r, laboringPayload(true), nil))

	// A starving worker keeps move_to so she can break off to eat — the reactor's
	// hunger/thirst carve-out, mirrored at the tool surface.
	if names["move_to"] != 1 {
		t.Errorf("move_to should stay for a laboring worker with a red hunger need (break off to eat); count %d", names["move_to"])
	}
	// The commerce tools stay stripped even then — a starving worker eats, she
	// doesn't trade.
	for _, gated := range []string{"pay", "pay_with_item", "offer_trade", "sell"} {
		if names[gated] != 0 {
			t.Errorf("%q advertised to a laboring worker even with a red need; want it stripped", gated)
		}
	}
}

func TestGateTools_NotLaboring_KeepsCommerceTools(t *testing.T) {
	// Control: the gate is scoped to laboring, not a blanket strip — a non-laboring
	// actor with an audience keeps every commerce tool.
	r := laborSpeakOnlyRegistry(t)
	payload := perception.Payload{ActorID: "patience", Surroundings: speakAudience()}
	names := specNameSet(gateTools(r, payload, nil))
	for _, keep := range []string{"pay", "pay_with_item", "offer_trade", "sell", "move_to", "speak"} {
		if names[keep] != 1 {
			t.Errorf("%q should be advertised to a non-laboring actor; count %d", keep, names[keep])
		}
	}
}

// laboringPayloadFlags is laboringPayload with the LLM-268 off-post surface flags
// set on the LaboringView, so the move_to-gate widening can be exercised.
func laboringPayloadFlags(redNeed, offPost, employerAway bool) perception.Payload {
	p := laboringPayload(redNeed)
	p.Laboring.OffPost = offPost
	p.Laboring.EmployerAway = employerAway
	return p
}

func TestGateTools_LaboringOffPost_KeepsMoveTo(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	// Wandered off the post, needs green: move_to re-granted so she can walk back
	// (LLM-268 symptom 1), but the commerce tools stay stripped — she's returning
	// to the job, not trading.
	names := specNameSet(gateTools(r, laboringPayloadFlags(false, true, false), nil))
	if names["move_to"] != 1 {
		t.Errorf("move_to should be re-granted to an off-post laboring worker (walk back, LLM-268); count %d", names["move_to"])
	}
	for _, gated := range []string{"pay", "pay_with_item", "offer_trade", "sell"} {
		if names[gated] != 0 {
			t.Errorf("%q advertised to an off-post laboring worker; commerce stays stripped", gated)
		}
	}
}

func TestGateTools_LaboringEmployerAway_KeepsMoveTo(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	// Employer left the post: move_to re-granted so she can follow when asked
	// (LLM-268 symptom 2, the accompany case); commerce still stripped.
	names := specNameSet(gateTools(r, laboringPayloadFlags(false, false, true), nil))
	if names["move_to"] != 1 {
		t.Errorf("move_to should be re-granted so the worker can follow an away employer (LLM-268); count %d", names["move_to"])
	}
	for _, gated := range []string{"pay", "pay_with_item", "offer_trade", "sell"} {
		if names[gated] != 0 {
			t.Errorf("%q advertised to a laboring worker with an away employer; commerce stays stripped", gated)
		}
	}
}

// TestGateTools_Laboring_MoveToInvariant is the LLM-268 cross-condition invariant:
// for a laboring worker, move_to is advertised IFF at least one of {red
// hunger/thirst, off-post, employer-away} holds, and stripped when none do (the
// LLM-230 committed case). Commerce tools stay stripped in every laboring case.
func TestGateTools_Laboring_MoveToInvariant(t *testing.T) {
	r := laborSpeakOnlyRegistry(t)
	for _, tc := range []struct {
		name                           string
		redNeed, offPost, employerAway bool
	}{
		{"committed", false, false, false},
		{"red_need", true, false, false},
		{"off_post", false, true, false},
		{"employer_away", false, false, true},
		{"off_post_and_away", false, true, true},
		{"red_and_off_post", true, true, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			names := specNameSet(gateTools(r, laboringPayloadFlags(tc.redNeed, tc.offPost, tc.employerAway), nil))
			wantMove := tc.redNeed || tc.offPost || tc.employerAway
			got := names["move_to"] == 1
			if got != wantMove {
				t.Errorf("move_to advertised=%v, want %v (red=%v off=%v away=%v)", got, wantMove, tc.redNeed, tc.offPost, tc.employerAway)
			}
			for _, gated := range []string{"pay", "pay_with_item", "offer_trade", "sell"} {
				if names[gated] != 0 {
					t.Errorf("%q advertised to a laboring worker; commerce stays stripped in all cases", gated)
				}
			}
		})
	}
}
