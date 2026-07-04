package sim

import (
	"testing"
	"time"
)

// return_to_post_backstop_commands_test.go — LLM-268. Substrate tests for the
// off-post-laboring-worker backstop sweep + its per-actor exponential backoff.
// Drives EvaluateReturnToPostBackstop(now).Fn(w) directly on an in-memory World
// (no goroutine) so the time-based backoff is deterministic. Reuses lrWorld /
// lrActor / placeInteriorShop (labor_relocate_internal_test.go) and warrantKinds /
// clearWarrant (same package).

var rtpNow = time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC)

func evalReturnToPost(t *testing.T, w *World, now time.Time) ReturnToPostBackstopTelemetry {
	t.Helper()
	v, err := EvaluateReturnToPostBackstop(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateReturnToPostBackstop: %v", err)
	}
	tm, ok := v.(ReturnToPostBackstopTelemetry)
	if !ok {
		t.Fatalf("EvaluateReturnToPostBackstop returned %T, want ReturnToPostBackstopTelemetry", v)
	}
	return tm
}

func hasReturnToPostWarrantActor(a *Actor) bool {
	for _, k := range warrantKinds(a) {
		if k == WarrantKindReturnToPost {
			return true
		}
	}
	return false
}

// rtpWorld builds a world with an employer keeping an interior post ("store") and
// a worker on a live Working job for them, green needs. The caller sets the
// worker's / employer's InsideStructureID to place them at/off the post. now
// anchors the work window.
func rtpWorld(now time.Time) (w *World, worker, employer *Actor) {
	w = lrWorld()
	w.Settings.Location = time.UTC
	w.Settings.NeedThresholds = DefaultNeedThresholds()
	placeInteriorShop(w, "store")

	employer = lrActor("josiah", "store", "store") // keeps the post
	worker = lrActor("silence", "", "")            // worker attr not required; the live job drives eligibility
	worker.Kind = KindNPCShared
	worker.State = StateLaboring
	until := now.Add(time.Hour)
	worker.LaboringUntil = &until
	worker.LaborID = 1
	worker.Needs = map[NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5}
	w.Actors[employer.ID] = employer
	w.Actors[worker.ID] = worker

	workingUntil := now.Add(time.Hour)
	w.LaborLedger[1] = &LaborOffer{
		ID: 1, WorkerID: worker.ID, EmployerID: employer.ID,
		Reward: 2, DurationMin: 120, State: LaborStateWorking,
		WorkingUntil: &workingUntil,
	}
	return w, worker, employer
}

// TestReturnToPostBackstop_StampsOffPostWorker: an off-post laboring worker (green
// needs, employer still holding the post) gets a return_to_post warrant, backoff
// initialized (level 0, base timer).
func TestReturnToPostBackstop_StampsOffPostWorker(t *testing.T) {
	w, worker, _ := rtpWorld(rtpNow)
	worker.InsideStructureID = "" // wandered off the post

	tm := evalReturnToPost(t, w, rtpNow)
	if tm.Stamped != 1 {
		t.Fatalf("Stamped=%d, want 1; telemetry=%+v", tm.Stamped, tm)
	}
	if !hasReturnToPostWarrantActor(worker) {
		t.Fatalf("warrant is not return_to_post; kinds=%v", warrantKinds(worker))
	}
	if worker.ReturnToPostBackoffLevel != 0 {
		t.Errorf("backoff level=%d, want 0 on first stamp", worker.ReturnToPostBackoffLevel)
	}
	if worker.ReturnToPostNextWarrantAt == nil || worker.ReturnToPostNextWarrantAt.Sub(rtpNow) != defaultReturnToPostBackstopBaseDelay {
		t.Errorf("first delay wrong; want base %v", defaultReturnToPostBackstopBaseDelay)
	}
}

// TestReturnToPostBackstop_SkipsWorkerAtPost: a worker standing at the post with
// the owner is committed (LLM-230) — no nudge.
func TestReturnToPostBackstop_SkipsWorkerAtPost(t *testing.T) {
	w, worker, _ := rtpWorld(rtpNow)
	worker.InsideStructureID = "store" // at the post

	// SkippedNotEligible counts BOTH the at-post worker and the employer (never a
	// worker on a job), so assert on Stamped — the worker got no nudge.
	tm := evalReturnToPost(t, w, rtpNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for an at-post worker; telemetry=%+v", tm.Stamped, tm)
	}
}

// TestReturnToPostBackstop_SkipsWhenEmployerAlsoAway: both off the post — she's
// likely following the employer (the accompany case, which rides the employer's
// speech), not marooned, so no spontaneous return is stamped.
func TestReturnToPostBackstop_SkipsWhenEmployerAlsoAway(t *testing.T) {
	w, worker, employer := rtpWorld(rtpNow)
	worker.InsideStructureID = ""
	employer.InsideStructureID = "" // employer left the post too

	tm := evalReturnToPost(t, w, rtpNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 when the employer is also away (following, not marooned); telemetry=%+v", tm.Stamped, tm)
	}
}

// TestReturnToPostBackstop_SkipsNonLaboring: no live Working job → nothing to
// return to.
func TestReturnToPostBackstop_SkipsNonLaboring(t *testing.T) {
	w, worker, _ := rtpWorld(rtpNow)
	worker.InsideStructureID = ""
	delete(w.LaborLedger, 1) // no live job

	tm := evalReturnToPost(t, w, rtpNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for a worker with no live job; telemetry=%+v", tm.Stamped, tm)
	}
}

// TestReturnToPostBackstop_SkipsNonLaboringStateMirror: a stale/corrupt mirror —
// a future LaboringUntil + a live Working offer on an actor NOT in StateLaboring —
// must not stamp a "you drifted from the job" warrant on someone the reactor no
// longer laboring-shelves.
func TestReturnToPostBackstop_SkipsNonLaboringStateMirror(t *testing.T) {
	w, worker, _ := rtpWorld(rtpNow)
	worker.InsideStructureID = ""
	worker.State = StateIdle // mirror drift: window + Working offer live, but not laboring

	tm := evalReturnToPost(t, w, rtpNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for a non-StateLaboring actor with a stale live window; telemetry=%+v", tm.Stamped, tm)
	}
}

// TestReturnToPostBackstop_SkipsInPlaceHire: an employer with no work structure
// (an in-place hire) has no post to be off — never eligible.
func TestReturnToPostBackstop_SkipsInPlaceHire(t *testing.T) {
	w, worker, employer := rtpWorld(rtpNow)
	worker.InsideStructureID = ""
	employer.WorkStructureID = ""

	tm := evalReturnToPost(t, w, rtpNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for an in-place hire (no post); telemetry=%+v", tm.Stamped, tm)
	}
}

// TestReturnToPostBackstop_YieldsToRedNeed: an off-post worker who is ALSO red on a
// need is left to the red-need path (she already keeps move_to + ticks via the
// red-need warrant); the return sweep steps aside without pacing the backoff.
func TestReturnToPostBackstop_YieldsToRedNeed(t *testing.T) {
	w, worker, _ := rtpWorld(rtpNow)
	worker.InsideStructureID = ""
	worker.Needs["hunger"] = 99 // well past the red threshold

	tm := evalReturnToPost(t, w, rtpNow)
	if tm.Stamped != 0 || tm.SkippedRedNeed != 1 {
		t.Errorf("Stamped=%d SkippedRedNeed=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedRedNeed, tm)
	}
	if worker.ReturnToPostNextWarrantAt != nil {
		t.Errorf("a red-need skip must not pace the backoff")
	}
}

// TestReturnToPostBackstop_EscalatesAndClears: a worker who stays off-post escalates
// the backoff each sweep; returning to the post makes her ineligible and clears it.
func TestReturnToPostBackstop_EscalatesAndClears(t *testing.T) {
	w, worker, _ := rtpWorld(rtpNow)
	worker.InsideStructureID = ""

	if tm := evalReturnToPost(t, w, rtpNow); tm.Stamped != 1 {
		t.Fatalf("first sweep Stamped=%d, want 1", tm.Stamped)
	}
	// Simulate the tick firing + the evaluator clearing the warrant cycle, then
	// advance to the end of the backoff window.
	clearWarrant(worker)
	next := *worker.ReturnToPostNextWarrantAt
	if tm := evalReturnToPost(t, w, next); tm.Stamped != 1 {
		t.Fatalf("second sweep Stamped=%d, want 1; telemetry=%+v", tm.Stamped, tm)
	}
	if worker.ReturnToPostBackoffLevel != 1 {
		t.Errorf("backoff level=%d, want 1 after the second stamp", worker.ReturnToPostBackoffLevel)
	}

	// Back at the post → ineligible → backoff clears so the next off-post spell
	// re-engages from base.
	clearWarrant(worker)
	worker.InsideStructureID = "store"
	if tm := evalReturnToPost(t, w, next.Add(time.Hour)); tm.Stamped != 0 {
		t.Fatalf("Stamped=%d after returning to the post, want 0", tm.Stamped)
	}
	if worker.ReturnToPostNextWarrantAt != nil || worker.ReturnToPostBackoffLevel != 0 {
		t.Errorf("backoff not cleared after returning to the post: nextAt=%v level=%d", worker.ReturnToPostNextWarrantAt, worker.ReturnToPostBackoffLevel)
	}
}
