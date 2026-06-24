package sim

import (
	"testing"
	"time"
)

// degenRecordingTelemetry captures the records the observer writes so tests can
// assert on stage-transition telemetry.
type degenRecordingTelemetry struct {
	records []TickTelemetryRecord
}

func (r *degenRecordingTelemetry) WriteTickTelemetry(rec TickTelemetryRecord) {
	r.records = append(r.records, rec)
}

// futileResult is a scored tick that accomplished nothing (arm A: requested a
// tool, every requested call failed, present baseline, no change).
func futileResult() TickResult {
	return TickResult{
		TerminalStatus:      TickStatusBudgetForced,
		BaselinePresent:     true,
		StateChanged:        false,
		ToolsRequested:      []string{"move_to"},
		ToolsFailedRejected: []string{"move_to"},
	}
}

func TestDegeneracyTickWasFutile(t *testing.T) {
	cases := []struct {
		name string
		r    TickResult
		want bool
	}{
		{
			name: "arm A — requested, all failed, no change",
			r:    TickResult{BaselinePresent: true, ToolsRequested: []string{"move_to"}, ToolsFailedRejected: []string{"move_to"}},
			want: true,
		},
		{
			name: "not futile — only SOME requested calls failed (one succeeded)",
			r: TickResult{
				BaselinePresent:     true,
				HadAudience:         true, // exclude arm B so this isolates arm A
				ToolsRequested:      []string{"speak", "move_to"},
				ToolsSucceeded:      []string{"speak"},
				ToolsFailedRejected: []string{"move_to"},
			},
			want: false,
		},
		{
			name: "arm B — no audience, only speech succeeded, no change",
			r:    TickResult{BaselinePresent: true, HadAudience: false, ToolsSucceeded: []string{"speak"}},
			want: true,
		},
		{
			name: "arm B — woke, did nothing, no one around",
			r:    TickResult{BaselinePresent: true, HadAudience: false},
			want: true,
		},
		{
			name: "not futile — missing baseline is inconclusive",
			r:    TickResult{BaselinePresent: false, ToolsRequested: []string{"move_to"}},
			want: false,
		},
		{
			name: "not futile — something changed",
			r:    TickResult{BaselinePresent: true, StateChanged: true, ToolsRequested: []string{"move_to"}},
			want: false,
		},
		{
			name: "not futile — a material commit succeeded",
			r:    TickResult{BaselinePresent: true, ToolsRequested: []string{"consume"}, ToolsSucceeded: []string{"consume"}},
			want: false,
		},
		{
			name: "not futile — spoke to a present peer",
			r:    TickResult{BaselinePresent: true, HadAudience: true, ToolsRequested: []string{"speak"}, ToolsSucceeded: []string{"speak"}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := degeneracyTickWasFutile(tc.r); got != tc.want {
				t.Errorf("degeneracyTickWasFutile = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDegeneracyTickScored(t *testing.T) {
	scored := []TickTerminalStatus{TickStatusSuccess, TickStatusDone, TickStatusBudgetForced, TickStatusFailedAfterRender}
	for _, s := range scored {
		if !degeneracyTickScored(s) {
			t.Errorf("status %v should be scored", s)
		}
	}
	neutral := []TickTerminalStatus{TickStatusSkipped, TickStatusStale, TickStatusFailedBeforeRender, TickStatusShutdown, TickStatusUnknown}
	for _, s := range neutral {
		if degeneracyTickScored(s) {
			t.Errorf("status %v should be neutral (not scored)", s)
		}
	}
}

func TestHasMaterialSuccess(t *testing.T) {
	cases := []struct {
		in   []string
		want bool
	}{
		{nil, false},
		{[]string{"speak"}, false},
		{[]string{"speak", "recall"}, false},
		{[]string{"consume"}, true},
		{[]string{"speak", "move_to"}, true},
	}
	for _, tc := range cases {
		if got := hasMaterialSuccess(tc.in); got != tc.want {
			t.Errorf("hasMaterialSuccess(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDegeneracySettings(t *testing.T) {
	if (WorldSettings{}).degeneracyEnabled() {
		t.Error("observer should be OFF by default")
	}
	if !(WorldSettings{DegeneracyThinAfterTicks: 1}).degeneracyEnabled() {
		t.Error("a positive thin threshold should enable the observer")
	}
	// Stage-2 sub-knobs fall back to defaults when unset.
	s := WorldSettings{DegeneracyThinAfterTicks: 5}
	if s.degeneracyThrottleAfterTicks() != defaultDegeneracyThrottleAfterTicks {
		t.Errorf("throttle-after default = %d, want %d", s.degeneracyThrottleAfterTicks(), defaultDegeneracyThrottleAfterTicks)
	}
	if s.degeneracyThrottleMinDuration() != defaultDegeneracyThrottleMinDuration {
		t.Errorf("throttle min-duration default = %v, want %v", s.degeneracyThrottleMinDuration(), defaultDegeneracyThrottleMinDuration)
	}
	if s.degeneracyThrottleBackoff() != defaultDegeneracyThrottleBackoff {
		t.Errorf("throttle backoff default = %v, want %v", s.degeneracyThrottleBackoff(), defaultDegeneracyThrottleBackoff)
	}
}

// newDegenWorld builds a minimal world wired with a recording telemetry sink.
func newDegenWorld(s WorldSettings) (*World, *degenRecordingTelemetry) {
	sink := &degenRecordingTelemetry{}
	w := &World{Settings: s}
	w.repo.TickTelemetry = sink
	return w, sink
}

func TestUpdateDegeneracy_DisabledNoop(t *testing.T) {
	w, sink := newDegenWorld(WorldSettings{}) // observer OFF
	a := &Actor{ID: "a1"}
	for i := 0; i < 50; i++ {
		updateDegeneracy(w, a, futileResult(), time.Unix(int64(i), 0).UTC())
	}
	if a.DegenStreak != 0 || a.DegenStage != DegeneracyNone {
		t.Errorf("disabled observer mutated state: streak=%d stage=%v", a.DegenStreak, a.DegenStage)
	}
	if len(sink.records) != 0 {
		t.Errorf("disabled observer emitted %d telemetry records", len(sink.records))
	}
}

func TestUpdateDegeneracy_StreakAndStages(t *testing.T) {
	w, sink := newDegenWorld(WorldSettings{
		DegeneracyThinAfterTicks:      3,
		DegeneracyThrottleAfterTicks:  5,
		DegeneracyThrottleMinDuration: 10 * time.Second,
	})
	a := &Actor{ID: "a1"}
	t0 := time.Unix(1000, 0).UTC()

	step := func(n int) time.Time { return t0.Add(time.Duration(n) * 3 * time.Second) }

	for i := 0; i < 5; i++ {
		updateDegeneracy(w, a, futileResult(), step(i))
	}
	// 5 futile ticks at +0,+3,+6,+9,+12s: flagged at tick 3 (streak 3),
	// throttled at tick 5 (streak 5, span 12s >= 10s).
	if a.DegenStreak != 5 {
		t.Errorf("streak = %d, want 5", a.DegenStreak)
	}
	if a.DegenStage != DegeneracyThrottled {
		t.Errorf("stage = %v, want throttled", a.DegenStage)
	}
	// Two transitions → two `stuck` records.
	if len(sink.records) != 2 {
		t.Fatalf("telemetry records = %d, want 2: %+v", len(sink.records), sink.records)
	}
	if sink.records[0].Kind != "stuck" || sink.records[0].Detail["stage"] != "flagged" {
		t.Errorf("first record = %+v, want stuck/flagged", sink.records[0])
	}
	if sink.records[1].Kind != "stuck" || sink.records[1].Detail["stage"] != "throttled" {
		t.Errorf("second record = %+v, want stuck/throttled", sink.records[1])
	}
}

func TestUpdateDegeneracy_ThrottleNeedsDuration(t *testing.T) {
	w, _ := newDegenWorld(WorldSettings{
		DegeneracyThinAfterTicks:      3,
		DegeneracyThrottleAfterTicks:  5,
		DegeneracyThrottleMinDuration: 10 * time.Second,
	})
	a := &Actor{ID: "a1"}
	t0 := time.Unix(2000, 0).UTC()
	// 5 futile ticks 1s apart — streak hits 5 but spans only 4s (< 10s),
	// so it must stay flagged, not throttle.
	for i := 0; i < 5; i++ {
		updateDegeneracy(w, a, futileResult(), t0.Add(time.Duration(i)*time.Second))
	}
	if a.DegenStreak != 5 {
		t.Errorf("streak = %d, want 5", a.DegenStreak)
	}
	if a.DegenStage != DegeneracyFlagged {
		t.Errorf("stage = %v, want flagged (duration gate not met)", a.DegenStage)
	}
}

func TestUpdateDegeneracy_ProductiveResets(t *testing.T) {
	w, sink := newDegenWorld(WorldSettings{DegeneracyThinAfterTicks: 3})
	a := &Actor{ID: "a1"}
	t0 := time.Unix(3000, 0).UTC()
	for i := 0; i < 3; i++ {
		updateDegeneracy(w, a, futileResult(), t0.Add(time.Duration(i)*time.Second))
	}
	if a.DegenStage != DegeneracyFlagged {
		t.Fatalf("setup: stage = %v, want flagged", a.DegenStage)
	}
	// A productive tick breaks the streak and emits a recovery record.
	productive := TickResult{TerminalStatus: TickStatusSuccess, BaselinePresent: true, StateChanged: true}
	updateDegeneracy(w, a, productive, t0.Add(10*time.Second))
	if a.DegenStreak != 0 || a.DegenStreakSince != nil || a.DegenStage != DegeneracyNone {
		t.Errorf("productive tick did not reset: streak=%d since=%v stage=%v", a.DegenStreak, a.DegenStreakSince, a.DegenStage)
	}
	if len(sink.records) == 0 || sink.records[len(sink.records)-1].Kind != "recovered" {
		t.Errorf("expected a final `recovered` record, got %+v", sink.records)
	}
}

func TestUpdateDegeneracy_NeutralTickIgnored(t *testing.T) {
	w, _ := newDegenWorld(WorldSettings{DegeneracyThinAfterTicks: 3})
	a := &Actor{ID: "a1"}
	t0 := time.Unix(4000, 0).UTC()
	updateDegeneracy(w, a, futileResult(), t0)
	updateDegeneracy(w, a, futileResult(), t0.Add(time.Second))
	// A cheap skipped tick in the middle must not increment OR reset the streak.
	skipped := TickResult{TerminalStatus: TickStatusSkipped, BaselinePresent: true}
	updateDegeneracy(w, a, skipped, t0.Add(2*time.Second))
	if a.DegenStreak != 2 {
		t.Errorf("streak = %d after neutral tick, want 2 (unchanged)", a.DegenStreak)
	}
}
