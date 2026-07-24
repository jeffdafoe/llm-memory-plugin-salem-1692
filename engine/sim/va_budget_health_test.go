package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestVABudgetHealth_SetClearSnapshot(t *testing.T) {
	var h sim.VABudgetHealth
	t0 := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	if got := h.Snapshot(); len(got.Capped) != 0 {
		t.Fatalf("fresh recorder should be empty, got %v", got.Capped)
	}

	h.RecordBudgetExceeded("zbbs-ezekiel-crane", t0, "Daily cost limit exceeded")
	h.RecordBudgetExceeded("salem-generic", t0.Add(time.Minute), "Monthly cost limit exceeded")

	snap := h.Snapshot()
	if len(snap.Capped) != 2 {
		t.Fatalf("want 2 capped, got %d: %v", len(snap.Capped), snap.Capped)
	}
	// Snapshot is sorted by slug for byte-stable alarm prose: salem-generic < zbbs-.
	if snap.Capped[0].Agent != "salem-generic" || snap.Capped[1].Agent != "zbbs-ezekiel-crane" {
		t.Errorf("capped not sorted by slug: %v", snap.Capped)
	}
	if snap.Capped[1].Detail != "Daily cost limit exceeded" {
		t.Errorf("ezekiel detail = %q", snap.Capped[1].Detail)
	}

	// A repeat refusal for the same VA holds the original since (the cap has been
	// live since the first one) and only refreshes the detail.
	h.RecordBudgetExceeded("salem-generic", t0.Add(time.Hour), "Monthly cost limit exceeded (still)")
	snap = h.Snapshot()
	if !snap.Capped[0].Since.Equal(t0.Add(time.Minute)) {
		t.Errorf("since should hold at first refusal %v, got %v", t0.Add(time.Minute), snap.Capped[0].Since)
	}
	if snap.Capped[0].Detail != "Monthly cost limit exceeded (still)" {
		t.Errorf("detail should refresh, got %q", snap.Capped[0].Detail)
	}

	// A success whose call started after the last refusal clears just that VA.
	h.RecordBudgetOK("salem-generic", t0.Add(2*time.Hour))
	snap = h.Snapshot()
	if len(snap.Capped) != 1 || snap.Capped[0].Agent != "zbbs-ezekiel-crane" {
		t.Errorf("after clearing salem-generic, want only ezekiel, got %v", snap.Capped)
	}

	// Clearing an uncapped VA is a no-op.
	h.RecordBudgetOK("nobody", t0.Add(3*time.Hour))
	if got := h.Snapshot(); len(got.Capped) != 1 {
		t.Errorf("no-op clear changed the set: %v", got.Capped)
	}
}

// A success whose call STARTED before the most recent refusal must not clear the
// cap — the concurrent-Complete race code_review flagged: a slow in-flight
// success landing after a newer request's 402 would otherwise self-clear a live
// cap. Deterministic (ordering encoded as timestamps) rather than goroutine-
// flaky, which is a stronger guarantee than a racing test.
func TestVABudgetHealth_StaleSuccessDoesNotClearNewerCap(t *testing.T) {
	var h sim.VABudgetHealth
	t0 := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	callStart := t0                    // a Complete that began before the budget ran out
	refusalAt := t0.Add(5 * time.Second) // a newer request records the cap afterward

	h.RecordBudgetExceeded("zbbs-ezekiel-crane", refusalAt, "Daily cost limit exceeded")

	// The stale in-flight success (started at callStart, before refusalAt) lands
	// last and must NOT clear the cap.
	h.RecordBudgetOK("zbbs-ezekiel-crane", callStart)
	if got := h.Snapshot(); len(got.Capped) != 1 {
		t.Fatalf("stale success cleared a newer cap: %v", got.Capped)
	}

	// A genuinely newer success (started after the refusal) does clear it.
	h.RecordBudgetOK("zbbs-ezekiel-crane", refusalAt.Add(time.Second))
	if got := h.Snapshot(); len(got.Capped) != 0 {
		t.Fatalf("a post-refusal success should clear the cap, got %v", got.Capped)
	}
}

// Repeated-refusal case: a success that started after the FIRST refusal but
// before a SECOND refusal landed must not clear the cap, because lastRefusalAt
// is refreshed on every refusal. `since` stays at the first refusal so the
// alarm's "dark for" duration is unaffected.
func TestVABudgetHealth_SuccessBeforeLatestRefusalDoesNotClear(t *testing.T) {
	var h sim.VABudgetHealth
	t0 := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	firstRefusal := t0
	successStart := t0.Add(10 * time.Second) // began after the first refusal...
	secondRefusal := t0.Add(20 * time.Second) // ...but a second refusal lands before it records

	h.RecordBudgetExceeded("zbbs-ezekiel-crane", firstRefusal, "Daily cost limit exceeded")
	h.RecordBudgetExceeded("zbbs-ezekiel-crane", secondRefusal, "Daily cost limit exceeded")

	h.RecordBudgetOK("zbbs-ezekiel-crane", successStart)
	snap := h.Snapshot()
	if len(snap.Capped) != 1 {
		t.Fatalf("a success predating the latest refusal cleared the cap: %v", snap.Capped)
	}
	if !snap.Capped[0].Since.Equal(firstRefusal) {
		t.Errorf("since should hold at the first refusal %v, got %v", firstRefusal, snap.Capped[0].Since)
	}
}

func TestVABudgetHealth_NilSafe(t *testing.T) {
	var h *sim.VABudgetHealth // never wired
	h.RecordBudgetExceeded("x", time.Unix(0, 0), "d")
	h.RecordBudgetOK("x", time.Unix(0, 0))
	if got := h.Snapshot(); len(got.Capped) != 0 {
		t.Errorf("nil snapshot should be empty, got %v", got.Capped)
	}
}
