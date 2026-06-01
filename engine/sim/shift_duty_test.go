package sim

import (
	"testing"
	"time"
)

// shift_duty_test.go — ZBBS-WORK-278, tick-driver producer #2. Covers the duty
// decision (shiftDutyTarget), the window helpers, and the ShiftTick dispatch
// (agent warrant path + idempotency). Reuses sleepTestWorld / intptr from
// npc_sleep_test.go (same package). The decision logic is driven with an
// explicit nowMinute, so these tests are deterministic (no wall-clock flake).

// shiftNPC builds an NPC for shift tests with all needs sated (0), so
// need-suppression doesn't fire unless a test sets a need. shiftDutyTarget keys
// on Kind, not LLMAgent, so the agent binding is omitted.
func shiftNPC(id ActorID, kind ActorKind, work, home, inside StructureID) *Actor {
	return &Actor{
		ID:                id,
		Kind:              kind,
		WorkStructureID:   work,
		HomeStructureID:   home,
		InsideStructureID: inside,
		Needs:             map[NeedKey]int{"hunger": 0, "thirst": 0, "tiredness": 0},
	}
}

func hasWarrantKind(a *Actor, want WarrantKind) bool {
	for _, k := range warrantKinds(a) {
		if k == want {
			return true
		}
	}
	return false
}

func TestMinuteInShiftWindow(t *testing.T) {
	// Day shift 07:00–16:00 (420..960), end exclusive.
	cases := []struct {
		start, end, min int
		want            bool
	}{
		{420, 960, 419, false}, {420, 960, 420, true}, {420, 960, 600, true},
		{420, 960, 959, true}, {420, 960, 960, false},
		// Wrap-midnight 16:00–03:00 (960..180).
		{960, 180, 1320, true}, {960, 180, 960, true}, {960, 180, 60, true},
		{960, 180, 179, true}, {960, 180, 180, false}, {960, 180, 600, false},
		// start == end is an EMPTY window (parity with sleep's isActorOnShift),
		// never on shift — NOT all-day.
		{600, 600, 600, false}, {600, 600, 300, false}, {0, 0, 0, false},
	}
	for _, c := range cases {
		if got := minuteInShiftWindow(c.start, c.end, c.min); got != c.want {
			t.Errorf("minuteInShiftWindow(%d,%d,%d) = %v, want %v", c.start, c.end, c.min, got, c.want)
		}
	}
}

func TestEffectiveShiftWindow_Schedule(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "work", "home", "home")
	a.ScheduleStartMin = intptr(960)
	a.ScheduleEndMin = intptr(180)
	w := sleepTestWorld(a)
	start, end, ok := effectiveShiftWindow(w, a)
	if !ok || start != 960 || end != 180 {
		t.Errorf("effectiveShiftWindow = (%d,%d,%v), want (960,180,true)", start, end, ok)
	}
}

func TestEffectiveShiftWindow_DawnDuskFallback(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "work", "home", "home") // nil schedule
	w := sleepTestWorld(a)
	w.Settings.DawnTime = "07:00"
	w.Settings.DuskTime = "19:00"
	start, end, ok := effectiveShiftWindow(w, a)
	if !ok || start != 420 || end != 1140 {
		t.Errorf("fallback window = (%d,%d,%v), want (420,1140,true)", start, end, ok)
	}
}

func TestShiftDutyTarget_OnShiftNotAtWork(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "home") // at home, on shift
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	w := sleepTestWorld(a)
	target, toWork, ok := shiftDutyTarget(w, a, 600, time.Now()) // 10:00, on shift
	if !ok || target != "shop" || !toWork {
		t.Errorf("got (%q,%v,%v), want (shop,true,true)", target, toWork, ok)
	}
}

func TestShiftDutyTarget_OffShiftAtWork(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop") // at work, off shift
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	w := sleepTestWorld(a)
	target, toWork, ok := shiftDutyTarget(w, a, 1300, time.Now()) // 21:40, off shift
	if !ok || target != "home" || toWork {
		t.Errorf("got (%q,%v,%v), want (home,false,true)", target, toWork, ok)
	}
}

func TestShiftDutyTarget_NoDutyWhenWhereItBelongs(t *testing.T) {
	w := sleepTestWorld()
	// On shift, already at work → no duty.
	atWork := shiftNPC("a", KindNPCStateful, "shop", "home", "shop")
	atWork.ScheduleStartMin = intptr(420)
	atWork.ScheduleEndMin = intptr(960)
	w.Actors["a"] = atWork
	if _, _, ok := shiftDutyTarget(w, atWork, 600, time.Now()); ok {
		t.Error("on-shift at-work NPC should have no duty")
	}
	// Off shift, already at home → no duty.
	atHome := shiftNPC("b", KindNPCStateful, "shop", "home", "home")
	atHome.ScheduleStartMin = intptr(420)
	atHome.ScheduleEndMin = intptr(960)
	w.Actors["b"] = atHome
	if _, _, ok := shiftDutyTarget(w, atHome, 1300, time.Now()); ok {
		t.Error("off-shift at-home NPC should have no duty")
	}
}

func TestShiftDutyTarget_NeedSuppressesToWorkButNotToHome(t *testing.T) {
	w := sleepTestWorld()
	// On-shift, not at work, but hungry (>= needSilentFloor) → to-work suppressed.
	hungry := shiftNPC("a", KindNPCStateful, "shop", "home", "home")
	hungry.ScheduleStartMin = intptr(420)
	hungry.ScheduleEndMin = intptr(960)
	hungry.Needs["hunger"] = 10 // mild tier (>= 8)
	w.Actors["a"] = hungry
	if _, _, ok := shiftDutyTarget(w, hungry, 600, time.Now()); ok {
		t.Error("on-shift to-work nudge should be suppressed by a mild+ need")
	}
	// Same need value, but off-shift at work → to-home is NOT suppressed.
	tiredAtWork := shiftNPC("b", KindNPCStateful, "shop", "home", "shop")
	tiredAtWork.ScheduleStartMin = intptr(420)
	tiredAtWork.ScheduleEndMin = intptr(960)
	tiredAtWork.Needs["tiredness"] = 22
	w.Actors["b"] = tiredAtWork
	target, toWork, ok := shiftDutyTarget(w, tiredAtWork, 1300, time.Now())
	if !ok || target != "home" || toWork {
		t.Errorf("to-home should NOT be need-suppressed; got (%q,%v,%v)", target, toWork, ok)
	}
}

func TestShiftDutyTarget_DecorativeNotNeedSuppressed(t *testing.T) {
	// Decoratives carry inert junk need values (the needs tick skips them).
	// They must NOT be need-suppressed — they always walk their shift.
	d := shiftNPC("d", KindDecorative, "shop", "home", "home")
	d.ScheduleStartMin = intptr(420)
	d.ScheduleEndMin = intptr(960)
	d.Needs["hunger"] = 24 // would suppress an agent
	w := sleepTestWorld(d)
	target, toWork, ok := shiftDutyTarget(w, d, 600, time.Now())
	if !ok || target != "shop" || !toWork {
		t.Errorf("decorative should walk regardless of needs; got (%q,%v,%v)", target, toWork, ok)
	}
}

func TestShiftDutyTarget_RestingSkipped(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)

	sleeping := shiftNPC("s", KindNPCStateful, "shop", "home", "home")
	sleeping.ScheduleStartMin = intptr(420)
	sleeping.ScheduleEndMin = intptr(960)
	sleeping.SleepingUntil = &future

	onBreak := shiftNPC("b", KindNPCStateful, "shop", "home", "home")
	onBreak.ScheduleStartMin = intptr(420)
	onBreak.ScheduleEndMin = intptr(960)
	onBreak.BreakUntil = &future

	w := sleepTestWorld(sleeping, onBreak)
	if _, _, ok := shiftDutyTarget(w, sleeping, 600, now); ok {
		t.Error("sleeping NPC should be skipped")
	}
	if _, _, ok := shiftDutyTarget(w, onBreak, 600, now); ok {
		t.Error("on-break NPC should be skipped")
	}
}

func TestShiftDutyTarget_ScopeExclusions(t *testing.T) {
	w := sleepTestWorld()
	// PC excluded.
	pc := shiftNPC("p", KindPC, "shop", "home", "home")
	pc.ScheduleStartMin = intptr(420)
	pc.ScheduleEndMin = intptr(960)
	w.Actors["p"] = pc
	if _, _, ok := shiftDutyTarget(w, pc, 600, time.Now()); ok {
		t.Error("PC should be out of scope")
	}
	// Transient visitor excluded.
	v := shiftNPC("v", KindNPCShared, "shop", "home", "home")
	v.ScheduleStartMin = intptr(420)
	v.ScheduleEndMin = intptr(960)
	v.VisitorState = &VisitorState{Archetype: "traveler", ExpiresAt: time.Now().Add(time.Hour)}
	w.Actors["v"] = v
	if _, _, ok := shiftDutyTarget(w, v, 600, time.Now()); ok {
		t.Error("transient visitor should be out of scope")
	}
	// A (hypothetical) decorative visitor is also excluded — the VisitorState
	// guard is unconditional, not agent-only.
	dv := shiftNPC("dv", KindDecorative, "shop", "home", "home")
	dv.ScheduleStartMin = intptr(420)
	dv.ScheduleEndMin = intptr(960)
	dv.VisitorState = &VisitorState{Archetype: "traveler", ExpiresAt: time.Now().Add(time.Hour)}
	w.Actors["dv"] = dv
	if _, _, ok := shiftDutyTarget(w, dv, 600, time.Now()); ok {
		t.Error("decorative visitor should also be out of scope (unconditional VisitorState guard)")
	}
}

// TestShiftTick_AgentTickInFlightSkipped: an agent mid-tick (TickInFlight) with
// no open warrant cycle and a standing duty still gets no shift_duty warrant —
// TickInFlight is part of the stamping gate (code_review, 2026-05-22).
func TestShiftTick_AgentTickInFlightSkipped(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "home")
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	a.TickInFlight = true
	w := sleepTestWorld(a)
	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC) // on shift, not at work

	if _, err := ShiftTick(now).Fn(w); err != nil {
		t.Fatalf("ShiftTick: %v", err)
	}
	if hasWarrantKind(a, WarrantKindShiftDuty) {
		t.Errorf("tick-in-flight agent should not get a shift_duty warrant; kinds = %v", warrantKinds(a))
	}
}

func TestShiftDutyTarget_UnscheduledDawnDuskFallback(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "home") // nil schedule
	w := sleepTestWorld(a)
	w.Settings.DawnTime = "07:00"
	w.Settings.DuskTime = "19:00"
	// 10:00 is within the dawn/dusk day window → on shift → to work.
	if target, toWork, ok := shiftDutyTarget(w, a, 600, time.Now()); !ok || target != "shop" || !toWork {
		t.Errorf("daytime: got (%q,%v,%v), want (shop,true,true)", target, toWork, ok)
	}
	// 23:00 is outside the day window → off shift → to home (move it to work first).
	a.InsideStructureID = "shop"
	if target, toWork, ok := shiftDutyTarget(w, a, 1380, time.Now()); !ok || target != "home" || toWork {
		t.Errorf("night: got (%q,%v,%v), want (home,false,true)", target, toWork, ok)
	}
}

func TestShiftTick_AgentStampsDutyWarrant(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "home")
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	w := sleepTestWorld(a)
	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC) // 10:00 UTC, minute 600, on shift

	if _, err := ShiftTick(now).Fn(w); err != nil {
		t.Fatalf("ShiftTick: %v", err)
	}
	if a.WarrantedSince == nil || !hasWarrantKind(a, WarrantKindShiftDuty) {
		t.Fatalf("expected a shift_duty warrant; kinds = %v", warrantKinds(a))
	}
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(ShiftDutyWarrantReason); ok {
			if !r.ToWork {
				t.Error("ToWork = false, want true (on-shift, heading to work)")
			}
			// 2b: the warrant must carry the target structure id (the value the
			// agent passes to move_to) — WorkStructureID when heading to work.
			if r.TargetStructureID != "shop" {
				t.Errorf("TargetStructureID = %q, want shop (WorkStructureID)", r.TargetStructureID)
			}
		}
	}
}

// TestShiftTick_AgentToHomeWarrantCarriesHomeID is the to-home companion of the
// to-work case above: an off-shift agent away from home gets a shift_duty
// warrant whose TargetStructureID is its HomeStructureID, so 2b's cue surfaces
// the right structure for move_to(home).
func TestShiftTick_AgentToHomeWarrantCarriesHomeID(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop") // at work, off shift
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	w := sleepTestWorld(a)
	now := time.Date(2026, 5, 22, 21, 40, 0, 0, time.UTC) // 21:40 UTC, minute 1300, off shift

	if _, err := ShiftTick(now).Fn(w); err != nil {
		t.Fatalf("ShiftTick: %v", err)
	}
	if a.WarrantedSince == nil || !hasWarrantKind(a, WarrantKindShiftDuty) {
		t.Fatalf("expected a shift_duty warrant; kinds = %v", warrantKinds(a))
	}
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(ShiftDutyWarrantReason); ok {
			if r.ToWork {
				t.Error("ToWork = true, want false (off-shift, heading home)")
			}
			if r.TargetStructureID != "home" {
				t.Errorf("TargetStructureID = %q, want home (HomeStructureID)", r.TargetStructureID)
			}
		}
	}
}

func TestShiftTick_AgentAlreadyWarrantedSkipped(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "home")
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	since := time.Now().Add(-time.Minute)
	a.WarrantedSince = &since
	a.Warrants = []WarrantMeta{{TriggerActorID: "n", Reason: BasicWarrantReason{K: WarrantKindNPCSpoke}}}
	w := sleepTestWorld(a)
	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)

	if _, err := ShiftTick(now).Fn(w); err != nil {
		t.Fatalf("ShiftTick: %v", err)
	}
	if hasWarrantKind(a, WarrantKindShiftDuty) {
		t.Errorf("already-warranted agent should not get a shift_duty warrant; kinds = %v", warrantKinds(a))
	}
}

func TestAlreadyEnRouteTo(t *testing.T) {
	a := shiftNPC("n", KindDecorative, "shop", "home", "home")
	if alreadyEnRouteTo(a, "shop") {
		t.Error("nil MoveIntent should not count as en route")
	}
	dest := NewStructureEnterDestination("shop")
	a.MoveIntent = &MoveIntent{Destination: dest}
	if !alreadyEnRouteTo(a, "shop") {
		t.Error("MoveIntent toward shop should count as en route to shop")
	}
	if alreadyEnRouteTo(a, "home") {
		t.Error("MoveIntent toward shop should not count as en route to home")
	}
}

// ZBBS-WORK-355 — the last-resort rest floor. classifyAgentDuty is the pure
// decision (march home vs warrant vs skip); these drive it directly on the light
// sleepTestWorld. The mechanical MoveActor march itself (which needs a fully
// placed structure + terrain) is left to live observation, the same way the
// decorative shift-walk dispatch is. tiredness == 24 is NeedPeak unconditionally;
// 23 is the just-below-peak boundary (Red, never Peak).

func TestClassifyAgentDuty_PeakOffShiftMarchesHome(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop") // off shift, away from home
	a.Needs["tiredness"] = 24                                   // peak — exhausted
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutyMarchHome {
		t.Errorf("peak off-shift agent: got %v, want agentDutyMarchHome", got)
	}
}

func TestClassifyAgentDuty_BelowPeakWarrants(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	a.Needs["tiredness"] = 23 // Red, one below peak — must still deliberate, not march
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutyWarrant {
		t.Errorf("below-peak off-shift agent: got %v, want agentDutyWarrant", got)
	}
}

func TestClassifyAgentDuty_PeakButWarrantedDefers(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	a.Needs["tiredness"] = 24
	since := time.Now().Add(-time.Minute)
	a.WarrantedSince = &since // a tick is pending — don't race the reactor
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutySkip {
		t.Errorf("peak but warranted: got %v, want agentDutySkip (deferred)", got)
	}
}

func TestClassifyAgentDuty_PeakButTickInFlightDefers(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	a.Needs["tiredness"] = 24
	a.TickInFlight = true
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutySkip {
		t.Errorf("peak but tick-in-flight: got %v, want agentDutySkip (deferred)", got)
	}
}

func TestClassifyAgentDuty_PeakAlreadyEnRouteHomeSkips(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	a.Needs["tiredness"] = 24
	a.MoveIntent = &MoveIntent{Destination: NewStructureEnterDestination("home")}
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutySkip {
		t.Errorf("peak already en route home: got %v, want agentDutySkip (idempotent)", got)
	}
}

func TestClassifyAgentDuty_PeakToWorkNeverMarched(t *testing.T) {
	// A to-work duty is never marched, even at peak — the march is home-only. (In
	// practice shiftDutyTarget need-suppresses an exhausted agent out of the
	// to-work nudge before this is reached, so this is the defensive guard.)
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "home")
	a.Needs["tiredness"] = 24
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "shop", true); got != agentDutyWarrant {
		t.Errorf("peak to-work duty: got %v, want agentDutyWarrant (never marched home)", got)
	}
}

func TestClassifyAgentDuty_PeakNonHomeTargetNotMarched(t *testing.T) {
	// The march is home-only, enforced in classifyAgentDuty itself: a peak,
	// off-shift agent whose duty target is NOT its home structure falls through to
	// the warrant path, so the helper can't be misused to mechanically relocate an
	// exhausted actor somewhere other than home.
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	a.Needs["tiredness"] = 24
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "tavern", false); got != agentDutyWarrant {
		t.Errorf("peak non-home target: got %v, want agentDutyWarrant (home-only march)", got)
	}
}

func TestClassifyAgentDuty_MissingTirednessNotMarched(t *testing.T) {
	// Missing need keys read as zero in this codebase, so an actor with no
	// tiredness entry is below peak and is NOT marched — it deliberates via the
	// warrant like any non-exhausted agent. (Pins the "missing reads below peak"
	// claim in atPeakTiredness's comment.)
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	delete(a.Needs, "tiredness")
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutyWarrant {
		t.Errorf("missing tiredness: got %v, want agentDutyWarrant (below peak)", got)
	}
}
