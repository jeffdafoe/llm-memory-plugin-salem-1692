package sim

import (
	"testing"
	"time"
)

// labor_employer_away_settle_test.go — LLM-621. The employer's PRESENCE at the
// post is not a condition of a Working contract completing or paying.
//
// This is the safety property the LLM-621 perception change depends on. That
// change lets an employer's harvest cue survive while a worker he hired labors at
// his own post, so the engine may now steer him to step out to a gather source
// mid-contract. Everything about that is only sound if leaving does not strand
// the worker.
//
// The mechanism was already there — LLM-268 built the accompany case: gateTools
// re-grants the worker's move_to on LaboringView.EmployerAway, renderLaborSelfState
// tells her "carry on with the work. You are paid when the job is done", and the
// return-to-post backstop deliberately stamps nothing when the employer is also
// away (TestReturnToPostBackstop_SkipsWhenEmployerAlsoAway). What no test asserted
// is the settle itself: the unpaid terminal is reached on an employer who cannot
// COVER the reward (employerCanCoverLaborReward) or a missing actor, never on
// where he is standing. Pin it, so a later presence requirement cannot be added
// without this failing.
func TestSettleCompletedLabor_EmployerAwayFromPost_StillPays(t *testing.T) {
	now := time.Date(2026, 8, 8, 15, 13, 49, 0, time.UTC)
	w, worker, employer := rtpWorld(now)
	worker.InsideStructureID = "store" // stayed and worked
	employer.InsideStructureID = ""    // stepped out — the well trip LLM-621 permits
	employer.Coins = 10
	worker.Coins = 3

	offer := w.LaborLedger[1]
	settleCompletedLabor(w, offer, now)

	if offer.State != LaborLedgerState(LaborTerminalStateCompleted) {
		t.Errorf("offer state = %q, want completed — the employer's location must not decide the terminal", offer.State)
	}
	if worker.Coins != 5 {
		t.Errorf("worker coins = %d, want 5 (3 + the 2-coin reward)", worker.Coins)
	}
	if employer.Coins != 8 {
		t.Errorf("employer coins = %d, want 8 (10 - the 2-coin reward)", employer.Coins)
	}
	if worker.State != StateIdle || worker.LaborID != 0 {
		t.Errorf("worker not released: state=%q LaborID=%d, want idle/0", worker.State, worker.LaborID)
	}
}

// The control: a settle that DOES fail is failing on the reward, not the absence.
// Same away-from-post employer, purse too thin to cover the 2 coins — this is the
// one legitimate route to the unpaid terminal, and it must still be reachable.
func TestSettleCompletedLabor_EmployerAwayButBroke_ResolvesUnpaid(t *testing.T) {
	now := time.Date(2026, 8, 8, 15, 13, 49, 0, time.UTC)
	w, worker, employer := rtpWorld(now)
	worker.InsideStructureID = "store"
	employer.InsideStructureID = ""
	employer.Coins = 1 // short of the 2-coin reward
	worker.Coins = 3

	offer := w.LaborLedger[1]
	settleCompletedLabor(w, offer, now)

	if offer.State != LaborLedgerState(LaborTerminalStateFailedUnavailable) {
		t.Errorf("offer state = %q, want failed_unavailable for a reward the employer cannot cover", offer.State)
	}
	if worker.Coins != 3 {
		t.Errorf("worker coins = %d, want 3 — nothing moves on the unpaid path", worker.Coins)
	}
	if employer.Coins != 1 {
		t.Errorf("employer coins = %d, want 1 — nothing moves on the unpaid path", employer.Coins)
	}
}
