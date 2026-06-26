package sim

import (
	"testing"
	"time"
)

// oscillationTick is a scored tick whose successful move_to changed the actor's
// position — individually "productive" per degeneracyTickWasFutile (StateChanged
// short-circuits it), which is exactly why the zero-yield arms miss a shuttle.
func oscillationTick() TickResult {
	return TickResult{
		TerminalStatus:  TickStatusSuccess,
		BaselinePresent: true,
		StateChanged:    true,
		ToolsRequested:  []string{"move_to"},
		ToolsSucceeded:  []string{"move_to"},
	}
}

func TestCountRedNeeds(t *testing.T) {
	// Default thresholds: hunger >= 18, thirst >= 12, tiredness >= 16.
	s := WorldSettings{NeedThresholds: DefaultNeedThresholds()}
	cases := []struct {
		name  string
		needs map[NeedKey]int
		want  int
	}{
		{"no needs", nil, 0},
		{"below red", map[NeedKey]int{"hunger": 17}, 0},
		{"at red", map[NeedKey]int{"hunger": 18}, 1},
		{"two red, one green", map[NeedKey]int{"hunger": 24, "thirst": 12, "tiredness": 5}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Actor{Needs: tc.needs}
			if got := countRedNeeds(s, a); got != tc.want {
				t.Errorf("countRedNeeds = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRecordDegenVisit_TrimsToWindow(t *testing.T) {
	s := WorldSettings{DegeneracyOscillationWindow: 3, NeedThresholds: DefaultNeedThresholds()}
	a := &Actor{}
	for _, st := range []StructureID{"a", "b", "c", "d", "e"} {
		a.InsideStructureID = st
		recordDegenVisit(s, a)
	}
	if len(a.DegenVisits) != 3 {
		t.Fatalf("window len = %d, want 3 (trimmed)", len(a.DegenVisits))
	}
	if a.DegenVisits[0].Structure != "c" || a.DegenVisits[2].Structure != "e" {
		t.Errorf("window = %+v, want trailing c,d,e", a.DegenVisits)
	}
}

func TestDegeneracyOscillationFutile(t *testing.T) {
	// Defaults via the resolvers: window 8, min transitions 3, max distinct 2.
	s := WorldSettings{NeedThresholds: DefaultNeedThresholds()}
	v := func(st StructureID, red int) DegenVisit { return DegenVisit{Structure: st, RedNeeds: red} }
	bl, tv, sh := StructureID("blacksmith"), StructureID("tavern"), StructureID("shop")

	cases := []struct {
		name   string
		visits []DegenVisit
		want   bool
	}{
		{
			name:   "tight shuttle, reds flat → futile",
			visits: []DegenVisit{v(bl, 1), v(tv, 1), v(bl, 1), v(tv, 1), v(bl, 1), v(tv, 1), v(bl, 1), v(tv, 1)},
			want:   true,
		},
		{
			name:   "window not yet full → not futile",
			visits: []DegenVisit{v(bl, 1), v(tv, 1), v(bl, 1)},
			want:   false,
		},
		{
			name:   "resolved a red need across the window → exempt",
			visits: []DegenVisit{v(bl, 2), v(tv, 2), v(bl, 2), v(tv, 1), v(bl, 1), v(tv, 1), v(bl, 1), v(tv, 1)},
			want:   false,
		},
		{
			name:   "three distinct structures (a route) → not futile",
			visits: []DegenVisit{v(bl, 1), v(tv, 1), v(sh, 1), v(bl, 1), v(tv, 1), v(sh, 1), v(bl, 1), v(tv, 1)},
			want:   false,
		},
		{
			name:   "dwelling in one structure → not futile",
			visits: []DegenVisit{v(bl, 1), v(bl, 1), v(bl, 1), v(bl, 1), v(bl, 1), v(bl, 1), v(bl, 1), v(bl, 1)},
			want:   false,
		},
		{
			name:   "in-transit blanks collapse out, shuttle still caught",
			visits: []DegenVisit{v(bl, 1), v("", 1), v(tv, 1), v("", 1), v(bl, 1), v(tv, 1), v(bl, 1), v(tv, 1)},
			want:   true,
		},
		{
			name:   "reds rising while shuttling → futile (no progress)",
			visits: []DegenVisit{v(bl, 0), v(tv, 1), v(bl, 1), v(tv, 2), v(bl, 2), v(tv, 2), v(bl, 3), v(tv, 3)},
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Actor{DegenVisits: tc.visits}
			if got := degeneracyOscillationFutile(s, a); got != tc.want {
				t.Errorf("degeneracyOscillationFutile = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUpdateDegeneracy_OscillationFlags is the LLM-124 regression: a sustained
// Blacksmith<->Tavern shuttle with no goal progress (every leg a successful
// move_to) must build the futility streak and flag the actor — the loop the
// zero-yield observer missed live (Ezekiel Crane, 2026-06-25).
func TestUpdateDegeneracy_OscillationFlags(t *testing.T) {
	w, sink := newDegenWorld(WorldSettings{
		DegeneracyThinAfterTicks: 3,
		NeedThresholds:           DefaultNeedThresholds(),
	})
	// A standing, never-resolved red hunger (he can't afford food).
	a := &Actor{ID: "ezekiel", Needs: map[NeedKey]int{"hunger": 20}}
	t0 := time.Unix(1000, 0).UTC()
	structs := []StructureID{"blacksmith", "tavern"}
	// Window (8) fills, then 3 more oscillating ticks reach the Stage-1 threshold.
	for i := 0; i < 12; i++ {
		a.InsideStructureID = structs[i%2]
		updateDegeneracy(w, a, oscillationTick(), t0.Add(time.Duration(i)*time.Second))
	}
	if a.DegenStage != DegeneracyFlagged {
		t.Fatalf("oscillation did not flag: stage=%v streak=%d", a.DegenStage, a.DegenStreak)
	}
	if a.DegenStreak != 5 {
		t.Errorf("streak = %d, want 5 (12 ticks - 8-tick window + the fill tick = 5 scored futile)", a.DegenStreak)
	}
	var sawStuck bool
	for _, r := range sink.records {
		if r.Kind == "stuck" {
			sawStuck = true
		}
	}
	if !sawStuck {
		t.Errorf("expected a `stuck` telemetry record, got %+v", sink.records)
	}
}

// A shuttle that actually resolves a red need each pass is meeting a goal, not
// thrashing — it must NOT flag.
func TestUpdateDegeneracy_OscillationProgressExempt(t *testing.T) {
	w, _ := newDegenWorld(WorldSettings{
		DegeneracyThinAfterTicks: 3,
		NeedThresholds:           DefaultNeedThresholds(),
	})
	a := &Actor{ID: "fed", Needs: map[NeedKey]int{"hunger": 20}}
	t0 := time.Unix(2000, 0).UTC()
	structs := []StructureID{"home", "well"}
	// Each full window contains a fresh red->resolved transition, so the
	// goal-completion guard exempts every evaluated tick.
	for i := 0; i < 12; i++ {
		a.InsideStructureID = structs[i%2]
		// Oscillate the hunger across red so the window always shows a drop.
		if i%2 == 1 {
			a.Needs["hunger"] = 5 // resolved on the well visit
		} else {
			a.Needs["hunger"] = 20
		}
		updateDegeneracy(w, a, oscillationTick(), t0.Add(time.Duration(i)*time.Second))
	}
	if a.DegenStage != DegeneracyNone || a.DegenStreak != 0 {
		t.Errorf("a progress-making shuttle was flagged: stage=%v streak=%d", a.DegenStage, a.DegenStreak)
	}
}

// countRedNeeds must use registry defaults even when WorldSettings carries no
// NeedThresholds map — NeedThresholds.Get falls back to each need's default, so
// a zero-value settings value is not a footgun.
func TestCountRedNeeds_ZeroValueSettingsUsesDefaults(t *testing.T) {
	var s WorldSettings // NeedThresholds is nil
	a := &Actor{Needs: map[NeedKey]int{"hunger": 18, "thirst": 5}}
	if got := countRedNeeds(s, a); got != 1 {
		t.Errorf("countRedNeeds with zero-value settings = %d, want 1 (hunger red at default 18, thirst green)", got)
	}
}

// A short shuttle broken by a genuine detour to a third structure must NOT
// flag: the detour's visits stay in the rolling window (raising the distinct
// count above the tight-loop bound) and the productive ticks reset the streak,
// so re-flagging requires a fresh sustained pure shuttle. This is the
// cross-productive-window case raised in review — the rolling window plus the
// streak/distinct gates handle it without nil-ing the window (which would break
// window fill for a genuine sustained loop).
func TestUpdateDegeneracy_OscillationDetourDoesNotFlag(t *testing.T) {
	w, _ := newDegenWorld(WorldSettings{
		DegeneracyThinAfterTicks: 5,
		NeedThresholds:           DefaultNeedThresholds(),
	})
	a := &Actor{ID: "worker", Needs: map[NeedKey]int{"hunger": 20}}
	t0 := time.Unix(5000, 0).UTC()
	tick := 0
	feed := func(st StructureID) {
		a.InsideStructureID = st
		updateDegeneracy(w, a, oscillationTick(), t0.Add(time.Duration(tick)*time.Second))
		tick++
	}
	shuttle := []StructureID{"forge", "store"}
	for i := 0; i < 4; i++ {
		feed(shuttle[i%2])
	}
	for i := 0; i < 3; i++ {
		feed("market") // genuine detour elsewhere
	}
	for i := 0; i < 6; i++ {
		feed(shuttle[i%2])
	}
	if a.DegenStage != DegeneracyNone {
		t.Errorf("a shuttle broken by a real detour flagged too eagerly: stage=%v streak=%d", a.DegenStage, a.DegenStreak)
	}
}

func TestCloneActor_DegenVisitsIndependent(t *testing.T) {
	a := &Actor{DegenVisits: []DegenVisit{{Structure: "a", RedNeeds: 1}}}
	cp := CloneActor(a)
	cp.DegenVisits[0].Structure = "b"
	if a.DegenVisits[0].Structure != "a" {
		t.Errorf("clone aliased DegenVisits: mutating the copy changed the original")
	}
}
