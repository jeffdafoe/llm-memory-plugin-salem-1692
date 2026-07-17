package sim

import (
	"fmt"
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

// TestShiftDutyTarget_StaggerOnlyAppliesAtHome pins LLM-451: the arrival stagger
// (ZBBS-HOME-309) desyncs the dawn home->work departure, so it may gate ONLY an
// NPC currently at home. An NPC already out in the world — an early-shift
// errand-runner, or one that wandered off-post, whether standing outdoors or
// inside some OTHER structure — must get its to-work duty immediately, not stand
// stranded off-post until the offset elapses. Covered across a scheduled day
// shift, a wrap-midnight shift, and an unscheduled dawn/dusk-fallback shift so the
// gate composes with the existing wrap-aware / fallback window logic.
func TestShiftDutyTarget_StaggerOnlyAppliesAtHome(t *testing.T) {
	// Fixed timestamp: nowMinute is passed explicitly, and these actors carry no
	// rest windows, so `now` is never consulted for the outcome — pin it anyway so
	// the test can't go non-deterministic if that ever changes.
	fixedNow := time.Unix(1_700_000_000, 0)

	const window = 30
	// nonzeroOffsetID finds a deterministic id whose stagger offset for this shift
	// start is non-zero, so "inside the window" is a real gate rather than a vacuous
	// pass (the hash can land on 0 for a given id+start).
	nonzeroOffsetID := func(start int) ActorID {
		for i := 0; i < 10000; i++ {
			id := ActorID(fmt.Sprintf("errand-runner-%d", i))
			if shiftLatenessOffset(id, start, window) > 0 {
				return id
			}
		}
		t.Fatalf("no non-zero stagger offset found for start=%d window=%d", start, window)
		return ""
	}

	cases := []struct {
		name        string
		start, end  int
		unscheduled bool // drive the shift window off the dawn/dusk fallback instead
	}{
		{"day_shift", 420, 960, false},             // 07:00–16:00
		{"wrap_midnight_shift", 1380, 120, false},  // 23:00–02:00
		{"unscheduled_dawn_dusk", 420, 1140, true}, // fallback window 07:00–19:00
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := nonzeroOffsetID(c.start)
			offset := shiftLatenessOffset(id, c.start, window)
			nowMinute := (c.start + offset - 1 + 1440) % 1440 // inside the window, wrap-safe

			build := func(inside StructureID) (*World, *Actor) {
				a := shiftNPC(id, KindNPCStateful, "shop", "home", inside)
				if !c.unscheduled {
					a.ScheduleStartMin = intptr(c.start)
					a.ScheduleEndMin = intptr(c.end)
				}
				w := sleepTestWorld(a)
				w.Settings.ShiftLatenessWindowMinutes = window
				if c.unscheduled {
					w.Settings.DawnTime = "07:00" // 420
					w.Settings.DuskTime = "19:00" // 1140
				}
				return w, a
			}

			// At home, inside the stagger window → still gated (the case the stagger
			// exists for). Assert the full suppressed contract, not just ok.
			w, atHome := build("home")
			if target, toWork, ok := shiftDutyTarget(w, atHome, nowMinute, fixedNow); ok || target != "" || toWork {
				t.Errorf("at-home: got (%q,%v,%v), want suppressed (\"\",false,false)", target, toWork, ok)
			}

			// Off-post inside a DIFFERENT structure (the direct farm-loiter-shaped
			// case) → NOT staggered.
			w, insideOther := build("tavern")
			if target, toWork, ok := shiftDutyTarget(w, insideOther, nowMinute, fixedNow); !ok || target != "shop" || !toWork {
				t.Errorf("inside-other-structure: got (%q,%v,%v), want (shop,true,true)", target, toWork, ok)
			}

			// Off-post outdoors → NOT staggered.
			w, outdoors := build("")
			if target, toWork, ok := shiftDutyTarget(w, outdoors, nowMinute, fixedNow); !ok || target != "shop" || !toWork {
				t.Errorf("outdoors: got (%q,%v,%v), want (shop,true,true)", target, toWork, ok)
			}
		})
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

// TestShiftDutyTarget_SuppressedDuringActiveRoute: an off-shift NPC with a
// standing go-home duty is left alone while it has an in-flight scheduled route
// (lamplighter / washerwoman / town_crier). The route owns the actor until it
// walks home and clears itself; without this the once-a-minute go-home nudge
// supersedes the route's walk to a far stop (stranding it) and marches a homed
// route NPC into its sprite-hiding house mid-round.
func TestShiftDutyTarget_SuppressedDuringActiveRoute(t *testing.T) {
	a := shiftNPC("lamp", KindDecorative, "", "home", "") // off-shift, not at home
	w := sleepTestWorld(a)
	w.Settings.DawnTime = "07:00"
	w.Settings.DuskTime = "19:00"

	// Precondition: at night (21:40) the unscheduled NPC has a go-home duty.
	if _, _, ok := shiftDutyTarget(w, a, 1300, time.Now()); !ok {
		t.Fatal("precondition: expected a go-home duty off-shift with no route")
	}

	// With an in-flight route the duty is suppressed.
	if w.ActiveRoutes == nil {
		w.ActiveRoutes = map[ActorID]*NPCRoute{}
	}
	w.ActiveRoutes["lamp"] = &NPCRoute{NPCID: "lamp", Label: AttrLamplighter, Phase: RoutePhaseActive}
	if _, _, ok := shiftDutyTarget(w, a, 1300, time.Now()); ok {
		t.Error("shiftDutyTarget returned a duty while a route was active; want suppressed")
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
	// On-shift, not at work, with a RED need → to-work suppressed (resolve the
	// pressing need first). ZBBS-HOME-463: only red suppresses now, not mild.
	redNeed := shiftNPC("a", KindNPCStateful, "shop", "home", "home")
	redNeed.ScheduleStartMin = intptr(420)
	redNeed.ScheduleEndMin = intptr(960)
	redNeed.Needs["hunger"] = 22 // red tier (>= the default red threshold)
	w.Actors["a"] = redNeed
	if _, _, ok := shiftDutyTarget(w, redNeed, 600, time.Now()); ok {
		t.Error("on-shift to-work nudge should be suppressed by a RED need")
	}
	// A merely MILD need no longer suppresses the to-work commute (ZBBS-HOME-463) —
	// the old mild gate stranded chronically-needy NPCs short of their post.
	mild := shiftNPC("c", KindNPCStateful, "shop", "home", "home")
	mild.ScheduleStartMin = intptr(420)
	mild.ScheduleEndMin = intptr(960)
	mild.Needs["hunger"] = 10 // mild tier ([8, 18))
	w.Actors["c"] = mild
	if target, toWork, ok := shiftDutyTarget(w, mild, 600, time.Now()); !ok || target != "shop" || !toWork {
		t.Errorf("mild need must NOT suppress to-work; got (%q,%v,%v)", target, toWork, ok)
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

// TestShiftDutyTarget_OffShiftBreakStillWindsDown: LLM-62 — the break suppressor is
// shift-aware. An OFF-shift on-break actor away from home still gets the wind-down
// go-home duty, so a self-renewing break can no longer shield an exhausted vendor
// from being routed home to bed. (On-shift on-break stays skipped — see
// TestShiftDutyTarget_RestingSkipped; sleep stays skipped regardless of shift.)
func TestShiftDutyTarget_OffShiftBreakStillWindsDown(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	a := shiftNPC("b", KindNPCStateful, "shop", "home", "shop") // off-shift, at work → away from home
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	a.BreakUntil = &future
	w := sleepTestWorld(a)
	target, toWork, ok := shiftDutyTarget(w, a, 1300, now) // 21:40 → off shift
	if !ok || target != "home" || toWork {
		t.Errorf("off-shift on-break NPC should get the wind-down go-home duty; got target=%q toWork=%v ok=%v", target, toWork, ok)
	}
}

// TestShiftDutyTarget_ExpiredBreakOnShiftNotSuppressed: only a LIVE break (After
// now) suppresses the duty — the shift-aware guard checks .After(now), not just
// non-nil. An on-shift actor with an EXPIRED break still gets its normal to-work
// duty. LLM-62.
func TestShiftDutyTarget_ExpiredBreakOnShiftNotSuppressed(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "home") // on shift, at home (not at work)
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	a.BreakUntil = &past // expired — must not suppress
	w := sleepTestWorld(a)
	target, toWork, ok := shiftDutyTarget(w, a, 600, now) // 10:00, on shift
	if !ok || target != "shop" || !toWork {
		t.Errorf("on-shift actor with expired break should get to-work duty; got (%q,%v,%v), want (shop,true,true)", target, toWork, ok)
	}
}

// TestShiftDutyTarget_OvernightShiftBreakSuppression: the shift-aware break guard
// runs through minuteInShiftWindow, so it must honor wraparound (overnight)
// schedules. For a 22:00–06:00 keeper, a live break at 23:00 (inside the window →
// on shift) suppresses the duty; the same break at 07:00 (daytime gap → off shift)
// does not, so the keeper winds down home. LLM-62.
func TestShiftDutyTarget_OvernightShiftBreakSuppression(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	mk := func(inside StructureID) *Actor {
		a := shiftNPC("n", KindNPCStateful, "shop", "home", inside)
		a.ScheduleStartMin = intptr(1320) // 22:00
		a.ScheduleEndMin = intptr(360)    // 06:00 — wraps midnight
		a.BreakUntil = &future
		return a
	}
	// 23:00 (minute 1380), not at work → would get a to-work duty, but the on-shift
	// break suppresses it.
	onShift := mk("home")
	if _, _, ok := shiftDutyTarget(sleepTestWorld(onShift), onShift, 1380, now); ok {
		t.Error("overnight on-shift (23:00) on-break keeper should be suppressed")
	}
	// 07:00 (minute 420), at work and away from home → off-shift wind-down; the
	// break no longer shields it.
	offShift := mk("shop")
	target, toWork, ok := shiftDutyTarget(sleepTestWorld(offShift), offShift, 420, now)
	if !ok || target != "home" || toWork {
		t.Errorf("overnight off-shift (07:00) on-break keeper should wind down home; got (%q,%v,%v), want (home,false,true)", target, toWork, ok)
	}
}

// TestShiftDutyTarget_MidMealSuppressesGoHome — ZBBS-WORK-386. The off-shift
// go-home duty yields while the NPC holds a live item-source dwell credit
// (mid-meal), so the engine doesn't drive it home off its meal. Object-source
// dwell (resting) is out of scope and does not suppress.
func TestShiftDutyTarget_MidMealSuppressesGoHome(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop") // at work, off shift
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	w := sleepTestWorld(a)

	// Precondition: off-shift away from home → go-home duty.
	if target, toWork, ok := shiftDutyTarget(w, a, 1300, time.Now()); !ok || target != "home" || toWork {
		t.Fatalf("precondition: want (home,false,true); got (%q,%v,%v)", target, toWork, ok)
	}

	// A live item-source dwell credit (mid-meal) suppresses the go-home duty.
	a.DwellCredits = map[DwellCreditKey]*DwellCredit{
		{ObjectID: "shop", Attribute: "hunger", Source: DwellSourceItem}: {
			ObjectID: "shop", Attribute: "hunger", Source: DwellSourceItem,
		},
	}
	if _, _, ok := shiftDutyTarget(w, a, 1300, time.Now()); ok {
		t.Error("mid-meal NPC should not be sent home; want go-home duty suppressed")
	}

	// An object-source dwell (resting) is out of scope — go-home duty still fires.
	a.DwellCredits = map[DwellCreditKey]*DwellCredit{
		{ObjectID: "well", Attribute: "thirst", Source: DwellSourceObject}: {
			ObjectID: "well", Attribute: "thirst", Source: DwellSourceObject,
		},
	}
	if _, _, ok := shiftDutyTarget(w, a, 1300, time.Now()); !ok {
		t.Error("object-source dwell should NOT suppress the go-home duty")
	}
}

// TestShiftDutyTarget_MidBatchAtPostSuppressesGoHome (LLM-335): a keeper standing
// at its post with a batch in the works is pinned there (the LLM-319 pause model),
// so the routine off-shift go-home duty is suppressed rather than re-litigated every
// minute of the shift/batch overlap. The suppression lifts the moment the batch lands
// (ProductionActivity cleared), so the keeper is then free to wind down.
func TestShiftDutyTarget_MidBatchAtPostSuppressesGoHome(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop") // at post, off shift
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	w := sleepTestWorld(a)

	// Precondition: off-shift at post with nothing in the works → go-home duty.
	if target, toWork, ok := shiftDutyTarget(w, a, 1300, time.Now()); !ok || target != "home" || toWork {
		t.Fatalf("precondition: want (home,false,true); got (%q,%v,%v)", target, toWork, ok)
	}

	// A batch in the works at the post suppresses the go-home duty.
	a.ProductionActivity = &ProductionActivity{Item: "cheese", BatchQty: 1, RemainingSeconds: 600}
	if _, _, ok := shiftDutyTarget(w, a, 1300, time.Now()); ok {
		t.Error("mid-batch keeper at its post should not be sent home; want go-home duty suppressed")
	}

	// Batch lands → the pin lifts and the wind-down duty resumes on the next scan.
	a.ProductionActivity = nil
	if _, _, ok := shiftDutyTarget(w, a, 1300, time.Now()); !ok {
		t.Error("with the batch landed the go-home duty should resume")
	}
}

// TestShiftDutyTarget_MidBatchNotAtPostStillWindsDown (LLM-335): the batch pin is
// AT-POST only. A keeper that wandered off mid-batch has paused it (it advances only
// at the post), so the go-home nag must stay live to bring it home — a paused batch
// waits indefinitely by design.
func TestShiftDutyTarget_MidBatchNotAtPostStillWindsDown(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "") // off shift, wandered off post
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	a.ProductionActivity = &ProductionActivity{Item: "cheese", BatchQty: 1, RemainingSeconds: 600}
	w := sleepTestWorld(a)
	if target, toWork, ok := shiftDutyTarget(w, a, 1300, time.Now()); !ok || target != "home" || toWork {
		t.Errorf("off-post mid-batch keeper: got (%q,%v,%v), want (home,false,true)", target, toWork, ok)
	}
}

// TestShiftDutyTarget_MidBatchRedTiredStillMarchesHome (LLM-335): the batch pin
// yields to the LLM-62 red-tiredness rest floor. An exhausted keeper mid-batch at
// its post is NOT suppressed here — shiftDutyTarget returns the go-home duty so
// classifyAgentDuty can march it home mechanically (pausing the batch to resume
// after sleep), the same "needs trump duty" layering the OpenUntil window honors.
func TestShiftDutyTarget_MidBatchRedTiredStillMarchesHome(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop") // at post, off shift
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	a.ProductionActivity = &ProductionActivity{Item: "cheese", BatchQty: 1, RemainingSeconds: 600}
	a.Needs["tiredness"] = 23 // red, one below peak — the LLM-62 dead zone
	w := sleepTestWorld(a)
	target, toWork, ok := shiftDutyTarget(w, a, 1300, time.Now())
	if !ok || target != "home" || toWork {
		t.Errorf("red-tired mid-batch keeper: got (%q,%v,%v), want (home,false,true)", target, toWork, ok)
	}
	if got := classifyAgentDuty(w, a, target, toWork); got != agentDutyMarchHome {
		t.Errorf("red-tired mid-batch keeper: classifyAgentDuty = %v, want agentDutyMarchHome", got)
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
// decorative shift-walk dispatch is. LLM-62 lowered the march trigger to RED: a
// red-or-worse off-shift agent (tiredness >= 20, peak 24 included) is marched home;
// a below-red one still warrants. 24 is NeedPeak; 23 is Red (never Peak); 13 is mild.

func TestClassifyAgentDuty_PeakOffShiftMarchesHome(t *testing.T) {
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop") // off shift, away from home
	a.Needs["tiredness"] = 24                                   // peak — exhausted
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutyMarchHome {
		t.Errorf("peak off-shift agent: got %v, want agentDutyMarchHome", got)
	}
}

func TestClassifyAgentDuty_RedOffShiftMarchesHome(t *testing.T) {
	// LLM-62 dead-zone fix: a red-but-not-peak (20–23) off-shift agent away from home
	// is now marched, not merely warranted — the band Elizabeth Ellis sat in, where
	// the LLM ignored the go-home cue and tiredness never climbed to peak.
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	a.Needs["tiredness"] = 23 // Red, one below peak
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutyMarchHome {
		t.Errorf("red off-shift agent: got %v, want agentDutyMarchHome (LLM-62)", got)
	}
}

func TestClassifyAgentDuty_BelowRedWarrants(t *testing.T) {
	// Below red the agent still deliberates (warrant + recovery_options), preserving
	// the normal wind-down beat: a rested keeper that ends its shift mild heads home
	// on its own rather than being mechanically marched.
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	a.Needs["tiredness"] = 13 // mild, below the red 20 threshold
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutyWarrant {
		t.Errorf("below-red off-shift agent: got %v, want agentDutyWarrant", got)
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
	// tiredness entry is below red and is NOT marched — it deliberates via the
	// warrant like any non-exhausted agent. (Pins the "missing reads below red"
	// claim in atRedTiredness's comment.)
	a := shiftNPC("n", KindNPCStateful, "shop", "home", "shop")
	delete(a.Needs, "tiredness")
	w := sleepTestWorld(a)
	if got := classifyAgentDuty(w, a, "home", false); got != agentDutyWarrant {
		t.Errorf("missing tiredness: got %v, want agentDutyWarrant (below peak)", got)
	}
}
