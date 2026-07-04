package sim

import (
	"testing"
	"time"
)

// seek_work_backstop_commands_test.go — LLM-141/168. Substrate tests for the
// idle-workless-worker backstop sweep + its per-actor exponential backoff. Drives
// EvaluateSeekWorkBackstop(now).Fn(w) directly on an in-memory World (no
// goroutine) so the time-based backoff is deterministic. Reuses
// workerShiftWorld / homedWorker (LLM-137 sleep-shift tests) and warrantKinds
// (needs tests) — same package. The day window is [dawn 07:00, dusk 19:00).

// seekNoon / seekNight are fixed moments inside / outside workerShiftWorld's
// day window — eligibility is on-shift-gated, so now must be controlled.
var (
	seekNoon  = time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC) // minute 720, on-shift
	seekNight = time.Date(2026, 6, 27, 23, 0, 0, 0, time.UTC) // minute 1380, off-shift
)

func evalSeekWork(t *testing.T, w *World, now time.Time) SeekWorkBackstopTelemetry {
	t.Helper()
	v, err := EvaluateSeekWorkBackstop(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateSeekWorkBackstop: %v", err)
	}
	tm, ok := v.(SeekWorkBackstopTelemetry)
	if !ok {
		t.Fatalf("EvaluateSeekWorkBackstop returned %T, want SeekWorkBackstopTelemetry", v)
	}
	return tm
}

func seekWorkNextDelay(t *testing.T, a *Actor, stampedAt time.Time) time.Duration {
	t.Helper()
	if a.SeekWorkNextWarrantAt == nil {
		t.Fatal("SeekWorkNextWarrantAt is nil — no backoff timer set")
	}
	return a.SeekWorkNextWarrantAt.Sub(stampedAt)
}

func hasSeekWorkWarrant(a *Actor) bool {
	for _, k := range warrantKinds(a) {
		if k == WarrantKindSeekWork {
			return true
		}
	}
	return false
}

// TestSeekWorkBackstop_StampsWorklessIdleWorker: a workless, on-shift, idle
// worker gets a seek_work warrant, and the backoff initializes (level 0, base timer).
func TestSeekWorkBackstop_StampsWorklessIdleWorker(t *testing.T) {
	a := homedWorker("w") // KindNPCShared, worker attr, no work_structure_id
	w := workerShiftWorld(a)

	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 {
		t.Fatalf("Stamped = %d, want 1; telemetry=%+v", tm.Stamped, tm)
	}
	if a.WarrantedSince == nil {
		t.Fatal("no WarrantedSince after seek-work backstop")
	}
	if !hasSeekWorkWarrant(a) {
		t.Fatalf("warrant is not seek_work; kinds=%v", warrantKinds(a))
	}
	if a.SeekWorkBackoffLevel != 0 {
		t.Errorf("backoff level = %d, want 0 on first stamp", a.SeekWorkBackoffLevel)
	}
	if d := seekWorkNextDelay(t, a, seekNoon); d != defaultSeekWorkBackstopBaseDelay {
		t.Errorf("first delay = %v, want base %v", d, defaultSeekWorkBackstopBaseDelay)
	}
}

// TestSeekWorkBackstop_SkipsNonWorker: an NPC without the worker attribute is
// never nudged to seek work.
func TestSeekWorkBackstop_SkipsNonWorker(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 5) // no worker attribute
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 1 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedNotEligible, tm)
	}
}

// TestSeekWorkBackstop_SkipsWorkerWithWorkplace: a worker that has its own post
// (work_structure_id set) is driven there by the duty steer — seek-work is for
// the workless, so it's left alone (LLM-168).
func TestSeekWorkBackstop_SkipsWorkerWithWorkplace(t *testing.T) {
	a := homedWorker("w")
	a.WorkStructureID = "shop1"
	w := workerShiftWorld(a)
	w.Structures = map[StructureID]*Structure{"shop1": {}} // resolvable post → has a workplace
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 1 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/1 (has a workplace)", tm.Stamped, tm.SkippedNotEligible)
	}
}

// TestSeekWorkBackstop_StampsWorkerWithDanglingWorkplace: a worker whose
// WorkStructureID is set but names no structure in the world (a stale/dangling
// reference the duty steer can't route to) reads as WORKLESS and IS nudged to seek
// work — otherwise it would dead-zone between an unroutable duty steer and a
// suppressed seek-work cue (LLM-168, raised in code review).
func TestSeekWorkBackstop_StampsWorkerWithDanglingWorkplace(t *testing.T) {
	a := homedWorker("w")
	a.WorkStructureID = "ghost" // set, but no such structure exists in the world
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 {
		t.Fatalf("Stamped = %d, want 1 (dangling workplace reads as workless); telemetry=%+v", tm.Stamped, tm)
	}
}

// TestSeekWorkBackstop_StampsWorklessWorkerWithCoins is the LLM-168 regression: a
// workless worker holding coin is STILL nudged to seek work — eligibility is
// workless, not broke. The brand-new Walker family (a few coins each, no
// workplace) idled all shift because the old Coins==0 gate skipped them here.
func TestSeekWorkBackstop_StampsWorklessWorkerWithCoins(t *testing.T) {
	a := homedWorker("w")
	a.Coins = 15 // holds coin, but workless → still eligible
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 {
		t.Fatalf("Stamped = %d, want 1 (workless worker with coin still seeks work); telemetry=%+v", tm.Stamped, tm)
	}
	if !hasSeekWorkWarrant(a) {
		t.Fatalf("warrant is not seek_work; kinds=%v", warrantKinds(a))
	}
}

// TestSeekWorkBackstop_SkipsComfortableWorker is the LLM-194 gate: a workless worker
// holding coin AT OR ABOVE the seek-work ceiling is NOT nudged to seek work — it's
// comfortable, so it drains its purse via consumption rather than hustling for odd jobs
// it doesn't need. The complement of TestSeekWorkBackstop_StampsWorklessWorkerWithCoins
// (a few coins, under the ceiling, still seeks).
func TestSeekWorkBackstop_SkipsComfortableWorker(t *testing.T) {
	a := homedWorker("w")
	a.Coins = SeekWorkCoinCeilingDefault // 25 — at the ceiling → comfortable
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 1 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/1 (comfortable worker at the ceiling)", tm.Stamped, tm.SkippedNotEligible)
	}
	if hasSeekWorkWarrant(a) {
		t.Errorf("comfortable worker got a seek_work warrant; kinds=%v", warrantKinds(a))
	}
}

// TestSeekWorkBackstop_StampsWorkerJustUnderCeiling: one coin under the ceiling the
// worker is still hustling — the boundary is >= ceiling, so ceiling-1 seeks. Pins the
// exact edge so a future off-by-one (> vs >=) is caught.
func TestSeekWorkBackstop_StampsWorkerJustUnderCeiling(t *testing.T) {
	a := homedWorker("w")
	a.Coins = SeekWorkCoinCeilingDefault - 1 // 24 — under the ceiling → still seeks
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 {
		t.Fatalf("Stamped = %d, want 1 (one coin under the ceiling still seeks); telemetry=%+v", tm.Stamped, tm)
	}
}

// TestSeekWorkBackstop_RespectsTunedCeiling: the gate reads the LIVE WorldSettings
// ceiling, not just the compiled default. With a raised ceiling a worker that would be
// comfortable at the default (25) is nudged again, so the live-tune actually takes
// effect on the warrant side.
func TestSeekWorkBackstop_RespectsTunedCeiling(t *testing.T) {
	a := homedWorker("w")
	a.Coins = 40 // above the default 25, below the tuned 60
	w := workerShiftWorld(a)
	w.Settings.SeekWorkCoinCeiling = 60
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 {
		t.Fatalf("Stamped = %d, want 1 (40 coins under the tuned 60 ceiling still seeks); telemetry=%+v", tm.Stamped, tm)
	}
}

// TestSeekWorkBackstop_SkipsOffShift: a broke worker outside its day window is
// not nudged (don't send it to find work at night).
func TestSeekWorkBackstop_SkipsOffShift(t *testing.T) {
	a := homedWorker("w")
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNight)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 1 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/1 (off-shift)", tm.Stamped, tm.SkippedNotEligible)
	}
}

// TestSeekWorkBackstop_SkipsSleeper: a broke worker that is asleep is not nudged
// — the seek-work warrant can't wake a sleeper anyway.
func TestSeekWorkBackstop_SkipsSleeper(t *testing.T) {
	a := homedWorker("w")
	until := seekNoon.Add(time.Hour)
	a.SleepingUntil = &until
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 1 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/1 (asleep)", tm.Stamped, tm.SkippedNotEligible)
	}
}

// TestSeekWorkBackstop_SkipsRester: a broke worker on a scheduled break is
// recovering — don't nudge it off to find work mid-rest.
func TestSeekWorkBackstop_SkipsRester(t *testing.T) {
	byState := homedWorker("rs")
	byState.State = StateResting
	until := homedWorker("bu")
	bu := seekNoon.Add(10 * time.Minute)
	until.BreakUntil = &bu
	w := workerShiftWorld(byState, until)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 2 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/2 (on break)", tm.Stamped, tm.SkippedNotEligible)
	}
}

// TestSeekWorkBackstop_SkipsSourceActivity: a broke worker mid eat/drink/harvest
// is occupied — don't yank it off the activity.
func TestSeekWorkBackstop_SkipsSourceActivity(t *testing.T) {
	a := homedWorker("w")
	a.SourceActivity = &SourceActivity{}
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 1 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/1 (source activity)", tm.Stamped, tm.SkippedNotEligible)
	}
}

// TestSeekWorkBackstop_PastTTLPendingOfferDoesNotBlock: a pending offer past its
// TTL is NOT a live block — it resolves on the labor sweep — so the worker is
// still eligible. Locks parity with SolicitWork's duplicate-offer gate (both use
// workerPendingLaborOffer).
func TestSeekWorkBackstop_PastTTLPendingOfferDoesNotBlock(t *testing.T) {
	a := homedWorker("w")
	w := workerShiftWorld(a)
	w.LaborLedger = map[LaborID]*LaborOffer{
		1: {ID: 1, WorkerID: "w", State: LaborStatePending, ExpiresAt: seekNoon.Add(-time.Minute)},
	}
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 {
		t.Errorf("Stamped = %d, want 1 (past-TTL pending offer is not a live block)", tm.Stamped)
	}
}

func hasTendNeedWarrant(a *Actor) bool {
	for _, k := range warrantKinds(a) {
		if k == WarrantKindTendNeed {
			return true
		}
	}
	return false
}

// foodVendorWorld is workerShiftWorld with a stationed keeper selling porridge at
// retail 5 — the coin arm of LLM-276 resolvability requires a real affordable vendor,
// not just a positive purse (code_review). The keeper carries no worker attribute, so
// the sweep skips it and only the subject can stamp.
func foodVendorWorld(subject *Actor) *World {
	keeper := &Actor{
		ID:                "keeper",
		Kind:              KindNPCShared,
		WorkStructureID:   "store",
		InsideStructureID: "store",
		Inventory:         map[ItemKind]int{"porridge": 5},
	}
	w := workerShiftWorld(subject, keeper)
	w.Structures = map[StructureID]*Structure{"store": {}}
	w.ItemKinds = map[ItemKind]*ItemKindDef{
		"porridge": {Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 8}}},
	}
	w.Recipes = map[ItemKind]*ItemRecipe{
		"porridge": {OutputItem: "porridge", RetailPrice: 5},
	}
	return w
}

// TestSeekWorkBackstop_RedirectsResolvableHungerToEat is the LLM-276 core: a workless
// idle worker whose hunger sits in the upper felt band (13..17, below the red-line 18)
// AND who can resolve it now (holds coin, an affordable porridge vendor is stationed)
// is woken with a TEND-NEED impulse to go eat, NOT the go-earn seek-work one. The
// redirect rides the same stamp/backoff path, so Stamped is still 1 — only the felt
// impulse differs.
func TestSeekWorkBackstop_RedirectsResolvableHungerToEat(t *testing.T) {
	a := homedWorker("w")
	a.Needs = map[NeedKey]int{"hunger": 15} // in [18-5, 18) — upper felt, sub-red
	a.Coins = 5                             // covers the porridge retail (5)
	w := foodVendorWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 || tm.Redirected != 1 {
		t.Fatalf("Stamped=%d Redirected=%d, want 1/1; telemetry=%+v", tm.Stamped, tm.Redirected, tm)
	}
	if !hasTendNeedWarrant(a) {
		t.Errorf("warrant is not tend_need; kinds=%v", warrantKinds(a))
	}
	if hasSeekWorkWarrant(a) {
		t.Errorf("stamped a seek_work warrant when it should redirect to eating; kinds=%v", warrantKinds(a))
	}
}

// TestSeekWorkBackstop_CoinButUnaffordableSeeksWork: coin in the band is NOT enough on
// its own — if every sold satisfier costs more than the purse and there is no free
// source or carried food, the need is unresolvable, so the worker stays on seek-work
// (goes to earn more). Guards against the coins>0 false-positive code_review flagged.
func TestSeekWorkBackstop_CoinButUnaffordableSeeksWork(t *testing.T) {
	a := homedWorker("w")
	a.Needs = map[NeedKey]int{"hunger": 15}
	a.Coins = 3 // below the porridge retail (5), no free source, carries nothing
	w := foodVendorWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 || tm.Redirected != 0 {
		t.Fatalf("Stamped=%d Redirected=%d, want 1/0; telemetry=%+v", tm.Stamped, tm.Redirected, tm)
	}
	if !hasSeekWorkWarrant(a) {
		t.Errorf("unaffordable coin should fall back to seek_work; kinds=%v", warrantKinds(a))
	}
}

// TestSeekWorkBackstop_BelowBandSeeksWork: a hunger below the redirect band
// (threshold-margin) is not pressing enough to interrupt earning — the worker still
// gets the ordinary seek-work impulse.
func TestSeekWorkBackstop_BelowBandSeeksWork(t *testing.T) {
	a := homedWorker("w")
	a.Needs = map[NeedKey]int{"hunger": 10} // below 18-5=13 → not in the band
	a.Coins = 5
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 || tm.Redirected != 0 {
		t.Fatalf("Stamped=%d Redirected=%d, want 1/0; telemetry=%+v", tm.Stamped, tm.Redirected, tm)
	}
	if !hasSeekWorkWarrant(a) {
		t.Errorf("warrant is not seek_work; kinds=%v", warrantKinds(a))
	}
}

// TestSeekWorkBackstop_UnresolvableHungerSeeksWork: a worker hungry in the band but
// with NO way to eat now — no coin, no carried food, no free source — stays on
// seek-work, so it goes and earns meal money. The resolvable gate is what splits the
// redirect from the fallback.
func TestSeekWorkBackstop_UnresolvableHungerSeeksWork(t *testing.T) {
	a := homedWorker("w")
	a.Needs = map[NeedKey]int{"hunger": 15}
	a.Coins = 0 // broke; no inventory, no free source in this world → unresolvable
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 1 || tm.Redirected != 0 {
		t.Fatalf("Stamped=%d Redirected=%d, want 1/0; telemetry=%+v", tm.Stamped, tm.Redirected, tm)
	}
	if !hasSeekWorkWarrant(a) {
		t.Errorf("unresolvable hunger should fall back to seek_work; kinds=%v", warrantKinds(a))
	}
}

// TestSeekWorkBackstop_RedirectsViaOwnStock: a broke worker that CARRIES a hunger
// satisfier is resolvable (own stock) and redirected to eat — no coin needed.
func TestSeekWorkBackstop_RedirectsViaOwnStock(t *testing.T) {
	a := homedWorker("w")
	a.Needs = map[NeedKey]int{"hunger": 15}
	a.Coins = 0
	a.Inventory = map[ItemKind]int{"bread": 1}
	w := workerShiftWorld(a)
	w.ItemKinds = map[ItemKind]*ItemKindDef{
		"bread": {Satisfies: []ItemSatisfaction{{Attribute: "hunger", Immediate: 4}}},
	}
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Redirected != 1 || !hasTendNeedWarrant(a) {
		t.Fatalf("carried food should redirect to eating; Redirected=%d kinds=%v", tm.Redirected, warrantKinds(a))
	}
}

// TestSeekWorkBackstop_RedirectsViaFreeSource: a broke worker with a free public
// hunger source (an unowned bush) in the world is resolvable and redirected to eat.
func TestSeekWorkBackstop_RedirectsViaFreeSource(t *testing.T) {
	a := homedWorker("w")
	a.Needs = map[NeedKey]int{"hunger": 15}
	a.Coins = 0
	w := workerShiftWorld(a)
	w.VillageObjects = map[VillageObjectID]*VillageObject{
		"bush": {Refreshes: []*ObjectRefresh{{Attribute: "hunger", Amount: -5}}}, // unowned, eases hunger
	}
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Redirected != 1 || !hasTendNeedWarrant(a) {
		t.Fatalf("free source should redirect to eating; Redirected=%d kinds=%v", tm.Redirected, warrantKinds(a))
	}
}

// TestSeekWorkBackstop_RespectsTunedNeedMargin: the band reads the LIVE
// WorldSettings margin. Widened to 10, a hunger of 10 (in [18-10, 18)) now redirects
// where it wouldn't at the default 5 — proving the live-tune takes effect.
func TestSeekWorkBackstop_RespectsTunedNeedMargin(t *testing.T) {
	a := homedWorker("w")
	a.Needs = map[NeedKey]int{"hunger": 10}
	a.Coins = 5
	w := foodVendorWorld(a)
	w.Settings.SeekWorkNeedYieldMargin = 10 // band becomes [8, 18) → 10 qualifies
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Redirected != 1 || !hasTendNeedWarrant(a) {
		t.Fatalf("tuned margin should redirect hunger 10; Redirected=%d kinds=%v", tm.Redirected, warrantKinds(a))
	}
}

// TestSeekWorkBackstop_YieldsToRedNeed: a workless worker that is ALSO red on a
// need is left to the red-need backstop (eat before work) — no seek-work warrant.
func TestSeekWorkBackstop_YieldsToRedNeed(t *testing.T) {
	a := homedWorker("w")
	a.Needs = map[NeedKey]int{"hunger": 24} // ≥ red 18
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedRedNeed != 1 {
		t.Errorf("Stamped=%d SkippedRedNeed=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedRedNeed, tm)
	}
	if a.WarrantedSince != nil {
		t.Error("stamped a seek-work warrant while a red need pressed")
	}
}

// TestSeekWorkBackstop_SkipsWorkingWorker: a worker mid-job (live ledger entry)
// is already engaged.
func TestSeekWorkBackstop_SkipsWorkingWorker(t *testing.T) {
	a := homedWorker("w")
	w := workerShiftWorld(a)
	w.LaborLedger = map[LaborID]*LaborOffer{1: {ID: 1, WorkerID: "w", State: LaborStateWorking}}
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 1 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/1 (working)", tm.Stamped, tm.SkippedNotEligible)
	}
}

// TestSeekWorkBackstop_SkipsPendingOffer: a worker with a live pending outgoing
// offer is awaiting an answer — don't re-nudge it to solicit again.
func TestSeekWorkBackstop_SkipsPendingOffer(t *testing.T) {
	a := homedWorker("w")
	w := workerShiftWorld(a)
	w.LaborLedger = map[LaborID]*LaborOffer{
		1: {ID: 1, WorkerID: "w", State: LaborStatePending, ExpiresAt: seekNoon.Add(time.Minute)},
	}
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedNotEligible != 1 {
		t.Errorf("Stamped=%d SkippedNotEligible=%d, want 0/1 (pending offer)", tm.Stamped, tm.SkippedNotEligible)
	}
}

// TestSeekWorkBackstop_SkipsWarrantedAndInFlight: an actor already pending a
// tick or mid-LLM-call doesn't need an injected warrant.
func TestSeekWorkBackstop_SkipsWarrantedAndInFlight(t *testing.T) {
	warranted := homedWorker("warranted")
	since := seekNoon.Add(-time.Minute)
	warranted.WarrantedSince = &since
	inflight := homedWorker("inflight")
	inflight.TickInFlight = true
	w := workerShiftWorld(warranted, inflight)

	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0", tm.Stamped)
	}
	if tm.SkippedWarranted != 1 || tm.SkippedTickInFlight != 1 {
		t.Errorf("SkippedWarranted=%d SkippedTickInFlight=%d, want 1/1; telemetry=%+v",
			tm.SkippedWarranted, tm.SkippedTickInFlight, tm)
	}
}

// TestSeekWorkBackstop_SkipsScope: a transient visitor (KindNPCShared but
// VisitorState set) is out of scope, like the red-need backstop.
func TestSeekWorkBackstop_SkipsScope(t *testing.T) {
	a := homedWorker("v")
	a.VisitorState = &VisitorState{}
	w := workerShiftWorld(a)
	tm := evalSeekWork(t, w, seekNoon)
	if tm.Stamped != 0 || tm.SkippedScope != 1 {
		t.Errorf("Stamped=%d SkippedScope=%d, want 0/1 (visitor)", tm.Stamped, tm.SkippedScope)
	}
}

// TestSeekWorkBackstop_RespectsBackoffWindow: a still-broke worker inside its
// backoff window is not re-warranted.
func TestSeekWorkBackstop_RespectsBackoffWindow(t *testing.T) {
	a := homedWorker("w")
	w := workerShiftWorld(a)

	evalSeekWork(t, w, seekNoon) // first stamp; next = seekNoon + 90s
	clearWarrant(a)              // simulate the tick firing + evaluator clearing it

	tm := evalSeekWork(t, w, seekNoon.Add(30*time.Second)) // inside the 90 s window
	if tm.Stamped != 0 || tm.SkippedBackoff != 1 {
		t.Fatalf("Stamped=%d SkippedBackoff=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedBackoff, tm)
	}
	if a.WarrantedSince != nil {
		t.Error("re-warranted inside the backoff window")
	}
}

// TestSeekWorkBackstop_EscalatesWhileWorkless: a worker that stays workless
// doubles its backoff each time the window elapses — 90 s → 180 s → 360 s.
func TestSeekWorkBackstop_EscalatesWhileWorkless(t *testing.T) {
	a := homedWorker("w")
	w := workerShiftWorld(a)
	base := defaultSeekWorkBackstopBaseDelay

	now := seekNoon
	evalSeekWork(t, w, now)
	if d := seekWorkNextDelay(t, a, now); d != base {
		t.Fatalf("delay[0] = %v, want %v", d, base)
	}

	for level, wantMult := range []int{2, 4, 8} { // levels 1,2,3
		clearWarrant(a)
		now = *a.SeekWorkNextWarrantAt // advance exactly to the due moment
		tm := evalSeekWork(t, w, now)
		if tm.Stamped != 1 {
			t.Fatalf("level %d: Stamped = %d, want 1; telemetry=%+v", level+1, tm.Stamped, tm)
		}
		if a.SeekWorkBackoffLevel != level+1 {
			t.Errorf("level = %d, want %d", a.SeekWorkBackoffLevel, level+1)
		}
		if d := seekWorkNextDelay(t, a, now); d != time.Duration(wantMult)*base {
			t.Errorf("delay at level %d = %v, want %v", level+1, d, time.Duration(wantMult)*base)
		}
	}
}

// TestSeekWorkBackstop_ClearsBackoffWhenGainsWorkplace: once the worker gains a
// post of its own (work_structure_id set) it is ineligible, and its backoff
// state is cleared so the next workless spell re-engages from base (LLM-168).
func TestSeekWorkBackstop_ClearsBackoffWhenGainsWorkplace(t *testing.T) {
	a := homedWorker("w")
	w := workerShiftWorld(a)

	evalSeekWork(t, w, seekNoon) // stamp; sets backoff state
	clearWarrant(a)
	if a.SeekWorkNextWarrantAt == nil {
		t.Fatal("expected backoff state after first stamp")
	}

	a.WorkStructureID = "shop1" // got a workplace — no longer eligible
	w.Structures = map[StructureID]*Structure{"shop1": {}}
	tm := evalSeekWork(t, w, seekNoon.Add(2*time.Minute))
	if tm.SkippedNotEligible != 1 {
		t.Errorf("SkippedNotEligible = %d, want 1", tm.SkippedNotEligible)
	}
	if a.SeekWorkNextWarrantAt != nil || a.SeekWorkBackoffLevel != 0 {
		t.Errorf("backoff not cleared on ineligibility: next=%v level=%d", a.SeekWorkNextWarrantAt, a.SeekWorkBackoffLevel)
	}
}
