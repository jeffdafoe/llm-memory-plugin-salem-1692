package sim

import (
	"testing"
	"time"
)

// hired_repair_backstop_commands_test.go — LLM-280. Substrate tests for the
// hired-worker repair backstop sweep + its per-actor exponential backoff. Drives
// EvaluateHiredRepairBackstop(now).Fn(w) directly on an in-memory World (no
// goroutine) so the time-based backoff is deterministic. Reuses lrWorld / lrActor /
// placeInteriorShop (labor_relocate_internal_test.go) and hasHiredRepairWarrant
// (same package). Sibling of return_to_post_backstop_commands_test.go.

var hrNow = time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC)

func evalHiredRepair(t *testing.T, w *World, now time.Time) HiredRepairBackstopTelemetry {
	t.Helper()
	v, err := EvaluateHiredRepairBackstop(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateHiredRepairBackstop: %v", err)
	}
	tm, ok := v.(HiredRepairBackstopTelemetry)
	if !ok {
		t.Fatalf("EvaluateHiredRepairBackstop returned %T, want HiredRepairBackstopTelemetry", v)
	}
	return tm
}

// hrWorld builds a world with an employer ("josiah") owning a worn, structure-backed
// business ("store") and a worker ("silence") on a live Working job for them, standing
// INSIDE the business (co-located via the AtBusiness inside branch), holding enough
// nails, green needs. This is the eligible baseline — each test mutates one axis off
// it. now anchors the work window.
func hrWorld(now time.Time) (w *World, worker, employer *Actor) {
	w = lrWorld()
	w.Settings.Location = time.UTC
	w.Settings.NeedThresholds = DefaultNeedThresholds()
	w.Settings.StallWearRepairThreshold = 400
	w.Settings.StallWearDegradeThreshold = 600
	w.Settings.StallNailsPerRepair = 5
	placeInteriorShop(w, "store")

	// Make "store" a worn business the employer owns (structures share the object id,
	// so the worker inside "store" is co-located with the business).
	store := w.VillageObjects["store"]
	store.OwnerActorID = "josiah"
	store.Tags = []string{TagBusiness}
	store.Wear = 500 // >= repair 400, < degrade 600 → worn, still trading

	employer = lrActor("josiah", "store", "store")
	worker = lrActor("silence", "", "store") // laboring AT the business
	worker.Kind = KindNPCShared
	worker.State = StateLaboring
	until := now.Add(time.Hour)
	worker.LaboringUntil = &until
	worker.LaborID = 1
	worker.Inventory = map[ItemKind]int{NailItemKind: 5} // enough to mend
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

// TestHiredRepairBackstop_StampsShelvedWorker: the eligible baseline — a laboring
// hired worker on-post at the worn business with nails — gets a re-stamped
// hired-repair warrant, backoff initialized (level 0, base timer).
func TestHiredRepairBackstop_StampsShelvedWorker(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 1 {
		t.Fatalf("Stamped=%d, want 1; telemetry=%+v", tm.Stamped, tm)
	}
	if !hasHiredRepairWarrant(worker.Warrants) {
		t.Fatalf("want a hired-repair warrant on the worker; warrants=%+v", worker.Warrants)
	}
	if worker.HiredRepairBackoffLevel != 0 {
		t.Errorf("backoff level=%d, want 0 on first stamp", worker.HiredRepairBackoffLevel)
	}
	if worker.HiredRepairNextWarrantAt == nil || worker.HiredRepairNextWarrantAt.Sub(hrNow) != defaultHiredRepairBackstopBaseDelay {
		t.Errorf("first delay wrong; want base %v", defaultHiredRepairBackstopBaseDelay)
	}
}

// TestHiredRepairBackstop_SkipsWhenNotHired: no live Working offer (the worker isn't
// on a hire) → WearableStallToMend resolves nothing → nothing to re-wake for.
func TestHiredRepairBackstop_SkipsWhenNotHired(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)
	delete(w.LaborLedger, 1) // no live hire

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for a worker not on a hire; telemetry=%+v", tm.Stamped, tm)
	}
	if hasHiredRepairWarrant(worker.Warrants) {
		t.Errorf("a non-hired worker must not be re-woken; warrants=%+v", worker.Warrants)
	}
}

// TestHiredRepairBackstop_SkipsEnRoute: an EnRoute worker hasn't reached the post yet
// (she gets the relocation cue, not the repair tool) → WearableStallToMend requires
// the Working state → skip.
func TestHiredRepairBackstop_SkipsEnRoute(t *testing.T) {
	w, _, _ := hrWorld(hrNow)
	w.LaborLedger[1].State = LaborStateEnRoute

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for an EnRoute worker; telemetry=%+v", tm.Stamped, tm)
	}
}

// TestHiredRepairBackstop_SkipsUnwornBusiness: the employer's business is below the
// repair threshold → nothing to mend → skip.
func TestHiredRepairBackstop_SkipsUnwornBusiness(t *testing.T) {
	w, _, _ := hrWorld(hrNow)
	w.VillageObjects["store"].Wear = 100 // < repair 400

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for an un-worn business; telemetry=%+v", tm.Stamped, tm)
	}
}

// TestHiredRepairBackstop_SkipsWithoutNails: a hired hand can't leave the job to shop,
// so a worker short of nails can't act — the backstop must not re-nag her (the one
// deliberate divergence from the nail-less one-shot awareness stamp).
func TestHiredRepairBackstop_SkipsWithoutNails(t *testing.T) {
	w, _, _ := hrWorld(hrNow)
	w.Actors["silence"].Inventory[NailItemKind] = 4 // < StallNailsPerRepair (5)

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for a worker short of nails; telemetry=%+v", tm.Stamped, tm)
	}
}

// TestHiredRepairBackstop_SkipsOffPost: a laboring worker who wandered off the post is
// the return-to-post backstop's case, not this one — she isn't co-located with the
// business, so she can't mend from where she stands. The two backstops partition by
// location and must never double-fire.
func TestHiredRepairBackstop_SkipsOffPost(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)
	worker.InsideStructureID = "" // wandered off the post (and off zero-tile, away from the pin)
	worker.Pos = TilePos{X: 9000, Y: 9000}

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for an off-post worker; telemetry=%+v", tm.Stamped, tm)
	}
}

// TestHiredRepairBackstop_SkipsMidRepair: a worker already mending has a SourceActivity
// in flight — don't re-stamp on top of the running repair window.
func TestHiredRepairBackstop_SkipsMidRepair(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)
	worker.SourceActivity = &SourceActivity{Kind: SourceActivityRepair}

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 {
		t.Errorf("Stamped=%d, want 0 for a worker mid-repair; telemetry=%+v", tm.Stamped, tm)
	}
}

// TestHiredRepairBackstop_YieldsToRedNeed: a worker with an actionable red need is left
// to the red-need path (which owns her wake and still surfaces repair on that tick); the
// sweep steps aside without pacing the backoff. Deliberately stamps NO need warrant — it
// locks in the "actionable red need but no break-interrupting warrant present yet" case,
// confirming we defer to the red-need backstop rather than waking her ourselves.
func TestHiredRepairBackstop_YieldsToRedNeed(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)
	worker.Needs["hunger"] = 99 // well past the red threshold

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 || tm.SkippedRedNeed != 1 {
		t.Errorf("Stamped=%d SkippedRedNeed=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedRedNeed, tm)
	}
	if worker.HiredRepairNextWarrantAt != nil {
		t.Errorf("a red-need skip must not pace the backoff")
	}
}

// TestHiredRepairBackstop_EscalatesAndClears: a worker who keeps declining escalates
// the backoff each sweep; mending (Wear reset to 0) makes her ineligible and clears it.
func TestHiredRepairBackstop_EscalatesAndClears(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)

	if tm := evalHiredRepair(t, w, hrNow); tm.Stamped != 1 {
		t.Fatalf("first sweep Stamped=%d, want 1", tm.Stamped)
	}
	// Simulate the tick firing + the evaluator clearing the warrant cycle, then
	// advance to the end of the backoff window.
	clearWarrant(worker)
	next := *worker.HiredRepairNextWarrantAt
	if tm := evalHiredRepair(t, w, next); tm.Stamped != 1 {
		t.Fatalf("second sweep Stamped=%d, want 1; telemetry=%+v", tm.Stamped, tm)
	}
	if worker.HiredRepairBackoffLevel != 1 {
		t.Errorf("backoff level=%d, want 1 after the second stamp", worker.HiredRepairBackoffLevel)
	}

	// Mended → Wear 0 → no longer repairable → ineligible → backoff clears so a later
	// worn spell re-engages from base.
	clearWarrant(worker)
	w.VillageObjects["store"].Wear = 0
	if tm := evalHiredRepair(t, w, next.Add(time.Hour)); tm.Stamped != 0 {
		t.Fatalf("Stamped=%d after mending, want 0", tm.Stamped)
	}
	if worker.HiredRepairNextWarrantAt != nil || worker.HiredRepairBackoffLevel != 0 {
		t.Errorf("backoff not cleared after mending: nextAt=%v level=%d", worker.HiredRepairNextWarrantAt, worker.HiredRepairBackoffLevel)
	}
}

// TestHiredRepairBackstop_SkipsBackoffWindow: inside the backoff window a still-eligible
// worker is not re-stamped (paced), so the cadence — not the 30 s sweep rate — bounds
// repeat cost.
func TestHiredRepairBackstop_SkipsBackoffWindow(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)

	if tm := evalHiredRepair(t, w, hrNow); tm.Stamped != 1 {
		t.Fatalf("first sweep Stamped=%d, want 1", tm.Stamped)
	}
	clearWarrant(worker)
	// A sweep one second later — still inside the base 90 s window.
	tm := evalHiredRepair(t, w, hrNow.Add(time.Second))
	if tm.Stamped != 0 || tm.SkippedBackoff != 1 {
		t.Errorf("Stamped=%d SkippedBackoff=%d, want 0/1 inside the backoff window; telemetry=%+v", tm.Stamped, tm.SkippedBackoff, tm)
	}
}

// TestHiredRepairBackstop_WarrantedSinceDoesNotPace: an eligible worker with an open
// warrant cycle is skipped WITHOUT advancing the backoff — the stamp itself is what
// paces, so a skip for an unrelated in-flight warrant must not consume cadence.
func TestHiredRepairBackstop_WarrantedSinceDoesNotPace(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)
	since := hrNow.Add(-time.Minute)
	worker.WarrantedSince = &since

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 || tm.SkippedWarranted != 1 {
		t.Errorf("Stamped=%d SkippedWarranted=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedWarranted, tm)
	}
	if worker.HiredRepairNextWarrantAt != nil || worker.HiredRepairBackoffLevel != 0 {
		t.Errorf("an open-warrant skip must not pace the backoff: nextAt=%v level=%d", worker.HiredRepairNextWarrantAt, worker.HiredRepairBackoffLevel)
	}
}

// TestHiredRepairBackstop_TickInFlightDoesNotPace: an actor mid-LLM-call is skipped
// without pacing — she doesn't need an injected warrant and the timer must not advance.
func TestHiredRepairBackstop_TickInFlightDoesNotPace(t *testing.T) {
	w, worker, _ := hrWorld(hrNow)
	worker.TickInFlight = true

	tm := evalHiredRepair(t, w, hrNow)
	if tm.Stamped != 0 || tm.SkippedTickInFlight != 1 {
		t.Errorf("Stamped=%d SkippedTickInFlight=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedTickInFlight, tm)
	}
	if worker.HiredRepairNextWarrantAt != nil || worker.HiredRepairBackoffLevel != 0 {
		t.Errorf("a tick-in-flight skip must not pace the backoff: nextAt=%v level=%d", worker.HiredRepairNextWarrantAt, worker.HiredRepairBackoffLevel)
	}
}
