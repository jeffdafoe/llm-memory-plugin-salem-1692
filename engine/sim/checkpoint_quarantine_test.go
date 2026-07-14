package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// checkpoint_quarantine_test.go — the Quarantine collector and its interaction
// with CheckpointHealth (LLM-392).
//
// The load-bearing property here is the one that is easy to get wrong: a
// quarantined checkpoint COMMITS, so it is a success by every existing measure
// — and if that were the whole story, isolating a bad row would silently switch
// OFF the durability alarm LLM-394 had just built. The degraded state has to be
// tracked separately, and it must not clear just because the checkpoint stopped
// crashing.

func TestQuarantine_NilIsSafeAndClean(t *testing.T) {
	var q *sim.Quarantine // the non-checkpoint write paths pass nil

	q.Drop("actor", "a", "reason")
	q.Clamp("actor", "a", "reason")
	q.BlockSweep("actor_need")

	if !q.Clean() {
		t.Error("a nil Quarantine must report Clean — no quarantine attached means nothing went wrong")
	}
	if q.Dropped("actor", "a") {
		t.Error("nil Quarantine reported a drop")
	}
	if q.SweepBlocked("actor") {
		t.Error("nil Quarantine reported a blocked sweep")
	}
	if q.Rows() != nil || q.SkippedSweeps() != nil {
		t.Error("nil Quarantine returned non-nil rows/sweeps")
	}
}

// TestQuarantine_DropBlocksItsOwnSweep — the core sweep-guard rule. A dropped
// row was not re-upserted, so its durable row still carries the OLD gen; if the
// sweep ran, it would DELETE the row we merely declined to update.
func TestQuarantine_DropBlocksItsOwnSweep(t *testing.T) {
	q := &sim.Quarantine{}
	q.Drop("pay_ledger", "1448", "double-booked lodging")

	if !q.SweepBlocked("pay_ledger") {
		t.Error("a table with a dropped row must not sweep — sweeping would delete the row we kept")
	}
	if q.SweepBlocked("actor") {
		t.Error("a drop in pay_ledger must not block an unrelated table's sweep")
	}
	if q.Clean() {
		t.Error("a checkpoint that dropped a row is not clean")
	}
}

// TestQuarantine_ClampDoesNotBlockSweep — a clamped row IS written, current and
// complete, so nothing downstream should skip it and its table still sweeps.
// (Getting this wrong would stop sweeping on any table that ever clamped a
// value, quietly accumulating departed rows forever.)
func TestQuarantine_ClampDoesNotBlockSweep(t *testing.T) {
	q := &sim.Quarantine{}
	q.Clamp("actor_need", "hannah/hunger", "value=99 clamped to 24")

	if q.SweepBlocked("actor_need") {
		t.Error("a clamped row is still written — its table must keep sweeping")
	}
	if q.Dropped("actor_need", "hannah/hunger") {
		t.Error("a clamped row must not be reported as dropped — downstream writers would skip it")
	}
	if q.Clean() {
		t.Error("a checkpoint that had to clamp a value is not clean — it must still alarm")
	}
}

// TestQuarantine_BlockSweepWithoutDrop — a dropped PARENT blocks its child
// tables even though no row of theirs was dropped. Without this, dropping an
// actor keeps her `actor` row (its sweep is blocked) while her needs and
// inventory get swept out from under her: a half-erased actor, worse than
// either keeping or dropping her whole.
func TestQuarantine_BlockSweepWithoutDrop(t *testing.T) {
	q := &sim.Quarantine{}
	q.Drop("actor", "hannah", "empty State")
	q.BlockSweep("actor_inventory")

	if !q.SweepBlocked("actor_inventory") {
		t.Error("a dropped parent must block its child table's sweep")
	}
	if q.Dropped("actor_inventory", "hannah/porridge") {
		t.Error("blocking a sweep must not fabricate row drops")
	}
}

// TestCheckpointHealth_QuarantinedSuccessKeepsAlarming is the regression test
// for the trap this whole ticket walks into: quarantine turns a FAILING
// checkpoint into a SUCCEEDING one. If degraded state rode on the failure
// counters, a village dropping a row every 60 seconds would report perfect
// health — re-creating the exact blind spot that cost 17.5 hours.
func TestCheckpointHealth_QuarantinedSuccessKeepsAlarming(t *testing.T) {
	h := &sim.CheckpointHealth{}
	t0 := time.Date(2026, 7, 12, 23, 0, 0, 0, time.UTC)

	dirty := &sim.Quarantine{}
	dirty.Drop("pay_ledger", "1448", "double-booked lodging")

	h.RecordSuccess(t0, dirty)
	got := h.Snapshot()

	// It committed — so by the failure metrics it looks perfectly healthy.
	if got.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0 — a quarantined checkpoint DID commit", got.ConsecutiveFailures)
	}
	if got.TotalSuccesses != 1 {
		t.Errorf("TotalSuccesses = %d, want 1", got.TotalSuccesses)
	}
	// ...which is exactly why the degraded state has to be tracked separately.
	if got.ConsecutiveDegraded != 1 {
		t.Fatalf("ConsecutiveDegraded = %d, want 1 — a lossy checkpoint MUST stay visible", got.ConsecutiveDegraded)
	}
	if !got.QuarantineSince.Equal(t0) {
		t.Errorf("QuarantineSince = %v, want %v", got.QuarantineSince, t0)
	}
	if len(got.LastQuarantinedRows) != 1 {
		t.Errorf("LastQuarantinedRows = %+v, want the dropped ledger row", got.LastQuarantinedRows)
	}

	// A second degraded checkpoint advances the streak but does NOT reset the
	// since-timestamp — the operator needs "this has been lossy for 4 hours",
	// not "for 60 seconds", every cycle.
	t1 := t0.Add(time.Hour)
	h.RecordSuccess(t1, dirty)
	got = h.Snapshot()
	if got.ConsecutiveDegraded != 2 {
		t.Errorf("ConsecutiveDegraded = %d, want 2", got.ConsecutiveDegraded)
	}
	if !got.QuarantineSince.Equal(t0) {
		t.Errorf("QuarantineSince = %v, want it pinned to %v (the start of the degraded run, not the latest checkpoint)", got.QuarantineSince, t0)
	}

	// Only a genuinely CLEAN checkpoint clears it.
	h.RecordSuccess(t1.Add(time.Minute), &sim.Quarantine{})
	got = h.Snapshot()
	if got.ConsecutiveDegraded != 0 || !got.QuarantineSince.IsZero() {
		t.Errorf("a clean checkpoint must clear the degraded state, got degraded=%d since=%v",
			got.ConsecutiveDegraded, got.QuarantineSince)
	}
	if got.TotalDegraded != 2 {
		t.Errorf("TotalDegraded = %d, want 2 (the lifetime count must NOT be cleared)", got.TotalDegraded)
	}
}

// TestCheckpointHealth_SkippedSweepsReported — an operator has to be able to
// see WHICH tables stopped sweeping, because those are the ones now accumulating
// rows for entities that have left the world.
func TestCheckpointHealth_SkippedSweepsReported(t *testing.T) {
	h := &sim.CheckpointHealth{}
	q := &sim.Quarantine{}
	q.Drop("actor", "hannah", "empty State")
	q.SweepSkipped("actor")
	q.SweepSkipped("actor_need")

	h.RecordSuccess(time.Now(), q)
	got := h.Snapshot()

	if len(got.LastSkippedSweeps) != 2 {
		t.Fatalf("LastSkippedSweeps = %v, want both actor and actor_need", got.LastSkippedSweeps)
	}
	if got.LastSkippedSweeps[0] != "actor" || got.LastSkippedSweeps[1] != "actor_need" {
		t.Errorf("LastSkippedSweeps = %v, want sorted [actor actor_need]", got.LastSkippedSweeps)
	}
}
