package sim

import (
	"testing"
	"time"
)

// turn_in_gate_test.go — LLM-447. The voluntary bed-down predicate (npcMayTurnIn)
// and the shared-arm refactor it rides on.
//
// npcMayTurnIn is npcSleepHere with the night window's OPEN widened from the
// civil bedtime hour (22:00) to dusk (19:00). Everything else — the residency
// arms, the off-shift requirement — is literally the same code path
// (npcSleepArmFor), which is the invariant the last test in this file pins: the
// two gates may differ in WHEN and in nothing else.

// turnInWorld is lodgerSleepWorld's clock (dawn 07:00, dusk 19:00, lodger bedtime
// 22:00) with the inn, so both the home and lodger arms resolve. The window under
// test is [19:00, 07:00); the auto-bed's is [22:00, 07:00).
func turnInWorld(actors ...*Actor) *World { return lodgerSleepWorld(actors...) }

// turnInHomed is the Walker shape: an UNSCHEDULED worker, homed, standing at
// home. Unscheduled workers are day-active on the dawn/dusk window (LLM-137), so
// their evening opens at dusk — exactly the population that produced the loop.
func turnInHomed(id ActorID) *Actor {
	return &Actor{
		ID:                id,
		Kind:              KindNPCShared,
		HomeStructureID:   "home1",
		InsideStructureID: "home1",
		// 12 — below the awareness floor. The gate must not consult this.
		Needs:      map[NeedKey]int{"tiredness": 12},
		Attributes: map[string][]byte{AttrWorker: nil},
	}
}

func turnInAt(hour, min int) time.Time {
	return time.Date(2026, 7, 16, hour, min, 0, 0, time.UTC)
}

// TestNpcMayTurnIn_HomeWindowBoundaries walks the clock across both windows. The
// stretch that matters is 19:00–21:59: turn_in is open there and the auto-bed is
// not, which is the "evening with no exit" this ticket closes.
func TestNpcMayTurnIn_HomeWindowBoundaries(t *testing.T) {
	a := turnInHomed("silence")
	w := turnInWorld(a)

	cases := []struct {
		hour, min       int
		turnIn, autoBed bool
		why             string
	}{
		{12, 0, false, false, "midday — day-active, neither gate open"},
		{18, 59, false, false, "one minute before dusk — the evening hasn't come"},
		{19, 0, true, false, "dusk exactly — turn_in opens; the auto-bed will not for 3 hours"},
		{20, 30, true, false, "mid-evening — the Long Goodnight window, now escapable"},
		{21, 59, true, false, "one minute before the auto-bed hour"},
		{22, 0, true, true, "the auto-bed hour — both open"},
		{2, 0, true, true, "small hours (window wraps midnight) — both open"},
		{6, 59, true, true, "one minute before dawn — both still open"},
		{7, 0, false, false, "dawn — both windows close"},
	}
	for _, c := range cases {
		when := turnInAt(c.hour, c.min)
		if got := npcMayTurnIn(w, a, when); got != c.turnIn {
			t.Errorf("%02d:%02d (%s): npcMayTurnIn = %v, want %v", c.hour, c.min, c.why, got, c.turnIn)
		}
		// The auto-bed is pinned alongside so the WIDENING is visible in this
		// table rather than asserted in prose — if a change collapsed the two
		// windows back together, the 19:00–21:59 rows fail here.
		if got := npcSleepHere(w, a, when); got != c.autoBed {
			t.Errorf("%02d:%02d (%s): npcSleepHere = %v, want %v", c.hour, c.min, c.why, got, c.autoBed)
		}
	}
}

// TestNpcMayTurnIn_NotTirednessGated is the explicit anti-regression for the
// design decision Jeff approved: the window opens on the CLOCK, not the meter.
//
// The three Walkers were at tiredness 12 — below the awareness floor of 13, so
// their prompts carried no tiredness line at all, and the red-tiredness march
// (≥16) was six points away. A tiredness-gated bedtime verb would have been
// invisible in precisely the situation it was built for.
func TestNpcMayTurnIn_NotTirednessGated(t *testing.T) {
	for _, tiredness := range []int{0, 5, 12, 16, 20} {
		a := turnInHomed("silence")
		a.Needs["tiredness"] = tiredness
		w := turnInWorld(a)
		if !npcMayTurnIn(w, a, turnInAt(20, 30)) {
			t.Errorf("tiredness %d: npcMayTurnIn = false, want true — bedtime is clock-and-social, not a meter read", tiredness)
		}
	}
}

// TestNpcMayTurnIn_OffShiftArm covers the on-shift refusals, including the
// night-shift home==work keeper the auto-bed's off-shift arm exists for. A
// tavernkeeper working 16:00–03:00 and living above the shop is inside her own
// home, in the night window, at 22:00 — every arm satisfied but the shift. Bedding
// her would shut the tavern mid-evening.
func TestNpcMayTurnIn_OffShiftArm(t *testing.T) {
	t.Run("night-shift keeper at home mid-shift refused", func(t *testing.T) {
		keeper := scheduledHomed("hannah", 16*60, 3*60) // 16:00–03:00, wrapping midnight
		w := turnInWorld(keeper)
		if npcMayTurnIn(w, keeper, turnInAt(22, 0)) {
			t.Error("22:00 mid-shift: npcMayTurnIn = true, want false — she is at work, not at leisure")
		}
		if npcMayTurnIn(w, keeper, turnInAt(1, 0)) {
			t.Error("01:00 mid-shift: npcMayTurnIn = true, want false")
		}
		if !npcMayTurnIn(w, keeper, turnInAt(4, 0)) {
			t.Error("04:00 off-shift, pre-dawn: npcMayTurnIn = false, want true")
		}
	})

	t.Run("day worker mid-shift refused, off-shift allowed", func(t *testing.T) {
		worker := scheduledHomed("ezekiel", 9*60, 19*60) // 09:00–19:00
		w := turnInWorld(worker)
		if npcMayTurnIn(w, worker, turnInAt(18, 0)) {
			t.Error("18:00 mid-shift: npcMayTurnIn = true, want false")
		}
		if !npcMayTurnIn(w, worker, turnInAt(19, 30)) {
			t.Error("19:30 off-shift past dusk: npcMayTurnIn = false, want true")
		}
	})
}

// TestNpcMayTurnIn_LodgerArm covers the boarder path — same widening, and the
// same refusals when the actor isn't where its bed is.
func TestNpcMayTurnIn_LodgerArm(t *testing.T) {
	future := turnInAt(22, 0).Add(72 * time.Hour)

	t.Run("lodger at its inn in the widened window", func(t *testing.T) {
		l := lodgerNPC("ezekiel", future)
		w := turnInWorld(l)
		if !npcMayTurnIn(w, l, turnInAt(20, 0)) {
			t.Error("20:00 at its inn: npcMayTurnIn = false, want true — before the lodger bedtime, but past dusk")
		}
		if npcSleepHere(w, l, turnInAt(20, 0)) {
			t.Error("20:00: npcSleepHere = true, want false — the auto-bed hour is 22:00 (fixture drift)")
		}
	})

	t.Run("lodger elsewhere refused", func(t *testing.T) {
		l := lodgerNPC("ezekiel", future)
		l.InsideStructureID = "tavern"
		w := turnInWorld(l)
		if npcMayTurnIn(w, l, turnInAt(20, 0)) {
			t.Error("lodger standing away from its rented inn: npcMayTurnIn = true, want false")
		}
	})

	t.Run("homeless non-lodger refused", func(t *testing.T) {
		// Ezekiel-when-unhoused. Homeless NPCs fall out naturally — no home, no
		// grant, no tool. This is by design; never "heal" it into a home.
		homeless := lodgerNPC("ezekiel", future)
		homeless.RoomAccess = nil
		w := turnInWorld(homeless)
		if npcMayTurnIn(w, homeless, turnInAt(20, 0)) {
			t.Error("homeless non-lodger: npcMayTurnIn = true, want false")
		}
	})
}

// TestNpcMayTurnIn_RejectsNonEligibleSubjects covers the cheap refusals.
func TestNpcMayTurnIn_RejectsNonEligibleSubjects(t *testing.T) {
	t.Run("already asleep", func(t *testing.T) {
		a := turnInHomed("silence")
		wake := turnInAt(23, 0)
		a.SleepingUntil = &wake
		if npcMayTurnIn(turnInWorld(a), a, turnInAt(20, 0)) {
			t.Error("already abed: npcMayTurnIn = true, want false")
		}
	})

	t.Run("a PC", func(t *testing.T) {
		a := turnInHomed("player")
		a.Kind = KindPC
		if npcMayTurnIn(turnInWorld(a), a, turnInAt(20, 0)) {
			t.Error("a PC: npcMayTurnIn = true, want false — players use the pc_sleep_* surface")
		}
	})

	t.Run("outdoors", func(t *testing.T) {
		a := turnInHomed("silence")
		a.InsideStructureID = ""
		if npcMayTurnIn(turnInWorld(a), a, turnInAt(20, 0)) {
			t.Error("standing outdoors: npcMayTurnIn = true, want false")
		}
	})

	t.Run("unusable dusk clock", func(t *testing.T) {
		a := turnInHomed("silence")
		w := turnInWorld(a)
		w.Settings.DuskTime = "" // unparseable
		if npcMayTurnIn(w, a, turnInAt(20, 0)) {
			t.Error("no usable dusk: npcMayTurnIn = true, want false — no window rather than an unbounded one")
		}
	})
}

// TestTurnInAndAutoBedShareTheirArms is the anti-drift invariant the refactor
// exists to make possible, and the one most worth keeping.
//
// npcMayTurnIn and npcSleepHere must differ in the window's OPEN and nothing
// else. So: anywhere inside the auto-bed's own window [22:00, dawn), the two
// predicates must agree exactly, for every residency and shift shape. If someone
// later adds a residency rule or a shift carve-out to one gate and forgets the
// other, the fixtures below diverge inside the shared window and this fails.
func TestTurnInAndAutoBedShareTheirArms(t *testing.T) {
	future := turnInAt(22, 0).Add(72 * time.Hour)
	fixtures := []struct {
		name string
		make func() *Actor
	}{
		{"unscheduled worker at home", func() *Actor { return turnInHomed("silence") }},
		{"scheduled day worker at home", func() *Actor { return scheduledHomed("ezekiel", 9*60, 19*60) }},
		{"night-shift keeper at home", func() *Actor { return scheduledHomed("hannah", 16*60, 3*60) }},
		{"lodger at its inn", func() *Actor { return lodgerNPC("boarder", future) }},
		{"scheduled lodger at its inn", func() *Actor { return scheduledLodgerNPC("smith", future) }},
		{"lodger away from its inn", func() *Actor {
			l := lodgerNPC("boarder", future)
			l.InsideStructureID = "tavern"
			return l
		}},
		{"homeless non-lodger", func() *Actor {
			h := lodgerNPC("crane", future)
			h.RoomAccess = nil
			return h
		}},
	}
	// Inside the auto-bed window only — [22:00, 07:00). Outside it the two are
	// SUPPOSED to differ, and that difference is pinned by the boundary table above.
	sharedWindow := []struct{ hour, min int }{
		{22, 0}, {23, 30}, {0, 15}, {3, 0}, {6, 59},
	}
	for _, f := range fixtures {
		for _, c := range sharedWindow {
			a := f.make()
			w := turnInWorld(a)
			when := turnInAt(c.hour, c.min)
			turnIn, autoBed := npcMayTurnIn(w, a, when), npcSleepHere(w, a, when)
			if turnIn != autoBed {
				t.Errorf("%s at %02d:%02d: npcMayTurnIn = %v but npcSleepHere = %v — inside the shared window "+
					"the two gates must agree; they may differ only in when the window OPENS",
					f.name, c.hour, c.min, turnIn, autoBed)
			}
		}
	}
}

// TestNpcMayTurnIn_NightShiftLodgerMidShift is the regression for the bug the
// LLM-447 code review surfaced indirectly.
//
// The auto-bed's LODGER arm deliberately does NOT consult the shift (LLM-14: a
// scheduled lodger beds at the civil night hour, not at its forge-close — reading
// its work shift was the force-sleep bug that ticket fixed). That is safe for the
// auto-bed because its window opens at 22:00. It is NOT safe for turn_in, whose
// window opens at dusk — which a night shift straddles.
//
// Concretely: a blacksmith boarding at the inn on a 16:00–03:00 shift, standing in
// the inn at 20:00. Sharing the arms alone would let him put himself to bed
// mid-shift and shut his own trade for the night. npcMayTurnIn requires off-shift
// on every arm for exactly this reason; npcSleepHere's arm semantics are untouched.
func TestNpcMayTurnIn_NightShiftLodgerMidShift(t *testing.T) {
	future := turnInAt(22, 0).Add(72 * time.Hour)
	lodger := lodgerNPC("smith", future)
	start, end := 16*60, 3*60 // 16:00–03:00, straddling dusk
	lodger.ScheduleStartMin, lodger.ScheduleEndMin = &start, &end
	w := turnInWorld(lodger)

	if npcMayTurnIn(w, lodger, turnInAt(20, 0)) {
		t.Error("20:00 mid-shift at its inn: npcMayTurnIn = true, want false — a lodger on a night shift " +
			"must not be able to bed himself and shut his trade for the evening")
	}
	if npcMayTurnIn(w, lodger, turnInAt(23, 30)) {
		t.Error("23:30 mid-shift at its inn: npcMayTurnIn = true, want false")
	}
	// 04:00 — shift ended at 03:00, still before dawn: now he may.
	if !npcMayTurnIn(w, lodger, turnInAt(4, 0)) {
		t.Error("04:00 off-shift at its inn: npcMayTurnIn = false, want true")
	}
}
