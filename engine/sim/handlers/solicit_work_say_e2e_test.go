package handlers

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// solicit_work_say_e2e_test.go — LLM-564. The worker-side ask never fired live
// (26 calls against 1,427 cue renders in a week): a worker who voiced the offer
// first never reached the tool, because speak and solicit_work are both
// terminal. The fix is offer_work's, as register_labor.go predicted — fold the
// utterance into the tool — so these tests mirror offer_work_e2e_test.go with
// the roles reversed, on the same live fixture (Lewis the worker, Prudence the
// keeper).
//
//  1. TestSolicitWork_SayRidesTheOffer — the ask is spoken as the offer mints,
//     Announced reports it, and the words are actually in the huddle.
//  2. TestSolicitWork_AutoDeclineSpeaksNothing — the LLM-193 invariant the say
//     leg must not break: the destitute-employer auto-decline exists so the
//     employer is never woken, and a SpeakTo would wake them through the speech
//     event. A say-carrying solicit on that path must leave the room silent.

// TestSolicitWork_SayRidesTheOffer drives the fold end to end: one tool call
// carries the offer AND the words, exactly the pair the live Patience/Hannah
// hire needed two actors' independent ticks to assemble.
func TestSolicitWork_SayRidesTheOffer(t *testing.T) {
	w, stop := buildApothecaryWorld(t)
	defer stop()

	const ask = "I could see to the shelves and the herbs — four coins for the afternoon, if you'll have me."
	cmd, err := HandleSolicitWork(HandlerInput{
		ActorID: "lewis", AttemptID: "tk-1",
		Args: SolicitWorkArgs{
			Employer:        "Prudence Ward",
			Reward:          4,
			DurationMinutes: 240,
			Say:             ask,
		},
	})
	if err != nil {
		t.Fatalf("HandleSolicitWork: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("solicit_work rejected: %v", err)
	}
	placed, ok := res.(sim.LaborSolicitResult)
	if !ok {
		t.Fatalf("result = %T, want sim.LaborSolicitResult", res)
	}
	if placed.ID == 0 || placed.State != sim.LaborStatePending {
		t.Fatalf("offer not placed: id=%d state=%q", placed.ID, placed.State)
	}
	if !placed.Announced {
		t.Errorf("Announced = false (SayRefused=%q); his ask never reached the room", placed.SayRefused)
	}

	// The ask must actually be IN the world, not merely reported by the result
	// — the failure mode LLM-343 pinned for sell.
	snap := w.Published()
	huddleID := snap.Actors["lewis"].CurrentHuddleID
	if huddleID == "" {
		t.Fatal("lewis not huddled after solicit_work — the huddle bootstrap did not fire")
	}
	var heard bool
	if h := snap.Huddles[huddleID]; h != nil {
		for _, u := range h.RecentUtterances {
			if u.SpeakerID == "lewis" && strings.Contains(u.Text, "shelves and the herbs") {
				heard = true
			}
		}
	}
	if !heard {
		t.Error("lewis's ask never entered the conversation")
	}

	// The offer is the worker's, and it is Prudence who owes the answer.
	offer := snap.LaborLedger[placed.ID]
	if offer == nil {
		t.Fatalf("offer %d absent from the published ledger", placed.ID)
	}
	if offer.EmployerInitiated() {
		t.Errorf("offer.InitiatedBy = %q, want lewis — a worker-initiated offer", offer.InitiatedBy)
	}
	if got := offer.Responder(); got != "prudence" {
		t.Errorf("offer.Responder() = %q, want prudence", got)
	}
}

// TestSolicitWork_AutoDeclineSpeaksNothing pins the say leg against LLM-193: a
// destitute employer (no coin, no goods) auto-declines at mint WITHOUT emitting
// LaborOfferReceived, precisely so no tick is burned waking them for a refusal.
// The say must respect that path — a spoken ask would wake the employer through
// the speech event and undo the gate — so the room stays silent and Announced
// stays false.
func TestSolicitWork_AutoDeclineSpeaksNothing(t *testing.T) {
	w, stop := buildApothecaryWorld(t)
	defer stop()

	// Strip Prudence to destitution on the world goroutine: no coins and no
	// inventory means employerCanHireInKind is false and the LLM-193 branch runs.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		p := world.Actors["prudence"]
		p.Coins = 0
		p.Inventory = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("strip prudence: %v", err)
	}

	cmd, err := HandleSolicitWork(HandlerInput{
		ActorID: "lewis", AttemptID: "tk-1",
		Args: SolicitWorkArgs{
			Employer:        "Prudence Ward",
			Reward:          4,
			DurationMinutes: 240,
			Say:             "Any work going? Four coins for the afternoon and I'm yours.",
		},
	})
	if err != nil {
		t.Fatalf("HandleSolicitWork: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("solicit_work rejected: %v", err)
	}
	placed, ok := res.(sim.LaborSolicitResult)
	if !ok {
		t.Fatalf("result = %T, want sim.LaborSolicitResult", res)
	}
	if placed.State != sim.LaborStateDeclined {
		t.Fatalf("state = %q, want declined (the LLM-193 auto-decline)", placed.State)
	}
	if placed.Announced {
		t.Error("Announced = true on the auto-decline path — the say went out and woke the employer LLM-193 exists to protect")
	}

	// Nothing was spoken: no huddle utterance from lewis anywhere.
	snap := w.Published()
	for _, h := range snap.Huddles {
		for _, u := range h.RecentUtterances {
			if u.SpeakerID == "lewis" {
				t.Errorf("lewis spoke %q on the auto-decline path — the room must stay silent", u.Text)
			}
		}
	}
}
