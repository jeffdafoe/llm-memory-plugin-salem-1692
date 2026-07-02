package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// labor_perception_test.go — LLM-186. The labor Command tests
// (labor_commands_test.go) prove the engine state after accept_work; these drive
// the next link the live PW Apothecary loop exposed: that a hired worker's own
// perception reflects the in-progress job, so the seek-work directive stays
// suppressed and the worker doesn't re-solicit (and get re-hired) in a loop.
//
// Unlike the perception package's unit tests — which build a Snapshot by hand —
// this exercises the full production path end to end: SolicitWork -> AcceptWork
// -> World.Published() -> perception.Build, reusing buildLaborWorld. It is the
// in-process reproduction of the live trace where Anne Walker, hired by Prudence
// Ward for a 60-minute job, still perceived "no work — seek work" on her next
// turn (Payload.Laboring == nil) and re-solicited.

// TestHiredWorkerPerceivesOwnJob is the LLM-186 invariant: a worker with NO
// permanent WorkStructureID (the pooled-vendor case, like Anne) that has been
// hired via accept_work must perceive its in-progress job. Payload.Laboring is
// what the build gates SeekWorkPlaces on (build.go), so a non-nil Laboring is
// exactly what keeps a hired worker from being steered back to solicit_work.
func TestHiredWorkerPerceivesOwnJob(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne Walker", huddleID: "h1", worker: true},
		{id: "prudence", displayName: "Prudence Ward", huddleID: "h1", coins: 50},
	})
	defer stop()

	now := time.Now().UTC()

	// Anne solicits a 2-hour job; Prudence hires her (reward 2, 120 minutes —
	// the 2h floor is the minimum a worker may offer, LLM-190).
	res, err := w.Send(sim.SolicitWork("anne", "Prudence Ward", 2, nil, 120, now))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	laborID := res.(sim.LaborSolicitResult).ID

	if _, err := w.Send(sim.AcceptWork("prudence", laborID, now)); err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}

	// Engine state precondition: the accept stuck — Anne is laboring on the
	// accepted offer, and the ledger holds it Working with a live window. If this
	// fails, the bug is upstream of perception (accept/ledger), not the seek-work
	// gate.
	ws := readActor(t, w, "anne")
	if ws.State != sim.StateLaboring {
		t.Fatalf("worker State = %q, want laboring (accept_work did not stick)", ws.State)
	}
	if ws.LaborID != laborID {
		t.Fatalf("worker LaborID = %d, want %d", ws.LaborID, laborID)
	}
	o := readLaborLedger(t, w)[laborID]
	if o.State != sim.LaborStateWorking {
		t.Fatalf("ledger offer State = %q, want working", o.State)
	}
	if o.WorkerID != "anne" {
		t.Fatalf("ledger offer WorkerID = %q, want anne", o.WorkerID)
	}
	if o.WorkingUntil == nil {
		t.Fatal("ledger offer WorkingUntil = nil, want a live window")
	}

	// The LLM-186 assertion: build Anne's perception from the published snapshot
	// (the same Published() snapshot the deliberation worker reads) and require
	// her in-progress job to surface. A nil Laboring here is the bug — it leaves
	// SeekWorkPlaces / the seek-work directive live, so the hired worker is told
	// "you have no work — seek work" and re-solicits, getting re-hired in a loop.
	snap := w.Published()
	p := perception.Build(snap, "anne", nil)
	if p.Laboring == nil {
		t.Fatal("hired worker perceives no job (Payload.Laboring == nil) — would be steered to seek work and re-solicit (LLM-186)")
	}
	if p.Laboring.Employer != "prudence" {
		t.Errorf("Laboring.Employer = %q, want prudence", p.Laboring.Employer)
	}
	if len(p.SeekWorkPlaces) != 0 {
		t.Errorf("SeekWorkPlaces = %d, want 0 — a hired worker must not be steered to seek work", len(p.SeekWorkPlaces))
	}
}
