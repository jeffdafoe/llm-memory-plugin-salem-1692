package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// accept_work_no_hire_e2e_test.go — LLM-351. An employer accepts a work offer
// that has already lapsed, or that cannot be arranged — the worker walked away,
// or she can no longer cover the wage. Nobody is hired. The engine told her
// "[ok]", and she thanked a worker she never took on.
//
// The no-hire copy existed; the path that emits it did not. sim.AcceptWork's
// gate-driven flips returned a bare LaborLedgerState, so the harness's
// `cmdResult.(sim.LaborAcceptResult)` assertion failed and the switch fell
// through to the terminal bare "[ok]".
//
// The old tests missed it because they fed commitResultContent a hand-built
// LaborAcceptResult{State: Expired} — a shape AcceptWork never produced. So
// these drive the real command and hand the harness what it ACTUALLY returned:
// handler → world → commitResultContent, the same three steps a live tick runs.
// A copy assertion against a fixture the command cannot return proves nothing.

// mintOfferedJob has Prudence offer Lewis a day's work and returns the pending
// offer's id. Their huddle is bootstrapped by offer_work itself, so the accept
// gates below have a real co-presence to lose.
func mintOfferedJob(t *testing.T, w *sim.World) sim.LaborID {
	t.Helper()
	cmd, err := HandleOfferWork(HandlerInput{
		ActorID: "prudence", AttemptID: "tk-1",
		Args: OfferWorkArgs{Worker: "Lewis Walker", Reward: 4, DurationMinutes: 240},
	})
	if err != nil {
		t.Fatalf("HandleOfferWork: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("offer_work rejected: %v", err)
	}
	placed, ok := res.(sim.LaborOfferResult)
	if !ok || placed.State != sim.LaborStatePending {
		t.Fatalf("offer not placed: %+v (%T)", res, res)
	}
	return placed.ID
}

// lewisAccepts runs the accept the way a tick does — through the handler, into
// the world, and back out through the harness's result renderer — and returns
// both the command's value and the sentence the model reads.
func lewisAccepts(t *testing.T, w *sim.World, id sim.LaborID, say string) (any, string) {
	t.Helper()
	cmd, err := HandleAcceptWork(HandlerInput{
		ActorID: "lewis", AttemptID: "tk-2",
		Args: AcceptWorkArgs{LaborID: LenientID(id), Say: say},
	})
	if err != nil {
		t.Fatalf("HandleAcceptWork: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("accept_work errored; a gate-driven flip resolves the offer, it does not fail the call: %v", err)
	}
	vc := &ValidatedCall{Name: "accept_work", DecodedArgs: AcceptWorkArgs{LaborID: LenientID(id), Say: say}}
	return res, commitResultContent(vc, res)
}

// TestAcceptWork_ExpiredOffer_ReadsTheReasonNotOK — the offer lapsed before
// Lewis answered. He must read why, not a bare [ok] that reads as "hired".
func TestAcceptWork_ExpiredOffer_ReadsTheReasonNotOK(t *testing.T) {
	w, stop := buildApothecaryWorld(t)
	defer stop()
	id := mintOfferedJob(t, w)

	// Age the offer past its TTL. HandleAcceptWork stamps time.Now(), so the
	// clock has to be moved on the offer rather than on the call.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.LaborLedger[id].ExpiresAt = time.Now().UTC().Add(-time.Minute)
		return nil, nil
	}}); err != nil {
		t.Fatalf("expiring the offer: %v", err)
	}

	res, content := lewisAccepts(t, w, id, "It would be my pleasure.")

	accepted, ok := res.(sim.LaborAcceptResult)
	if !ok {
		t.Fatalf("accept result = %T, want sim.LaborAcceptResult — the harness reads the outcome off this type", res)
	}
	if accepted.State != sim.LaborStateExpired {
		t.Errorf("state = %q, want expired", accepted.State)
	}
	if !accepted.WorkingUntil.IsZero() {
		t.Errorf("WorkingUntil = %v on a lapsed offer; no work window opened", accepted.WorkingUntil)
	}
	if !strings.Contains(content, "That offer had already expired") {
		t.Errorf("accept_work result %q never told Lewis the offer had lapsed", content)
	}
	if content == "[ok]" || strings.Contains(content, "took on the job") || strings.Contains(content, "You hired") {
		t.Errorf("accept_work result %q reads as a hire; nobody was taken on", content)
	}
	// He spoke as he accepted. The gates resolved the offer before the line went
	// out, so he is told the room never heard him.
	if !strings.Contains(content, "Your words went unsaid") {
		t.Errorf("accept_work result %q left Lewis believing he had spoken", content)
	}

	if o := w.Published().LaborLedger[id]; o == nil || o.State != sim.LaborStateExpired {
		t.Errorf("ledger offer = %+v, want expired — the flip is the resolution", o)
	}
}

// TestAcceptWork_LostCoPresence_ReadsTheReasonNotOK — Prudence has walked out of
// the conversation by the time Lewis answers. Nothing can be arranged, and the
// engine says so.
func TestAcceptWork_LostCoPresence_ReadsTheReasonNotOK(t *testing.T) {
	w, stop := buildApothecaryWorld(t)
	defer stop()
	id := mintOfferedJob(t, w)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["prudence"].CurrentHuddleID = ""
		return nil, nil
	}}); err != nil {
		t.Fatalf("removing prudence from the huddle: %v", err)
	}

	res, content := lewisAccepts(t, w, id, "")

	accepted, ok := res.(sim.LaborAcceptResult)
	if !ok {
		t.Fatalf("accept result = %T, want sim.LaborAcceptResult", res)
	}
	if accepted.State != sim.LaborStateFailedUnavailable {
		t.Errorf("state = %q, want failed_unavailable", accepted.State)
	}
	if !strings.Contains(content, "That couldn't be arranged") {
		t.Errorf("accept_work result %q never told Lewis why no job was struck", content)
	}
	if content == "[ok]" || strings.Contains(content, "took on the job") {
		t.Errorf("accept_work result %q reads as a hire; nobody was taken on", content)
	}
	// A wordless accept invents no utterance to mourn.
	if strings.Contains(content, "unsaid") || strings.Contains(content, "You said") {
		t.Errorf("wordless accept_work result %q invented an utterance", content)
	}

	if got := w.Published().Actors["lewis"].State; got == sim.StateLaboring {
		t.Error("Lewis is laboring for a job that was never struck")
	}
}

// TestAcceptWork_EmployerCannotCoverWage_ReadsTheReasonNotOK — the wage is gone
// from Prudence's purse by the time Lewis answers. The other half of the
// failed_unavailable arm, reached through a different gate: the copy names all
// three causes, so both gates must land on it.
func TestAcceptWork_EmployerCannotCoverWage_ReadsTheReasonNotOK(t *testing.T) {
	w, stop := buildApothecaryWorld(t)
	defer stop()
	id := mintOfferedJob(t, w)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["prudence"].Coins = 0
		return nil, nil
	}}); err != nil {
		t.Fatalf("emptying prudence's purse: %v", err)
	}

	res, content := lewisAccepts(t, w, id, "Gladly, mistress.")

	accepted, ok := res.(sim.LaborAcceptResult)
	if !ok {
		t.Fatalf("accept result = %T, want sim.LaborAcceptResult", res)
	}
	if accepted.State != sim.LaborStateFailedUnavailable {
		t.Errorf("state = %q, want failed_unavailable", accepted.State)
	}
	if !strings.Contains(content, "couldn't cover the pay agreed") {
		t.Errorf("accept_work result %q never named the unpayable wage", content)
	}
	if !strings.Contains(content, "Your words went unsaid") {
		t.Errorf("accept_work result %q left Lewis believing he had spoken", content)
	}
}
