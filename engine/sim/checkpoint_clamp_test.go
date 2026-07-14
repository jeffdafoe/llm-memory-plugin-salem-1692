package sim_test

// Unit tests for the checkpoint clamp pass (LLM-392). The projection arithmetic
// and the report; the real-Postgres tests in repo/pg prove the bounds match the
// actual columns.

import (
	"context"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// snapshotWith builds a CheckpointSnapshot around a single actor, the way
// BuildCheckpointSnapshot would. Callers mutate the actor before clamping.
func snapshotWith(a *sim.Actor) *sim.CheckpointSnapshot {
	return &sim.CheckpointSnapshot{Actors: map[sim.ActorID]*sim.Actor{a.ID: a}}
}

func baseActor() *sim.Actor {
	return &sim.Actor{ID: "a1", DisplayName: "Ann", State: sim.StateIdle}
}

// TestClamp_CleanSnapshotIsUntouched — the overwhelmingly common case. A healthy
// snapshot must produce an empty report and change nothing, or every checkpoint
// would raise the alarm and the alarm would mean nothing.
func TestClamp_CleanSnapshotIsUntouched(t *testing.T) {
	start, end := 480, 1080
	a := baseActor()
	a.Facing = "north"
	a.ScheduleStartMin, a.ScheduleEndMin = &start, &end
	a.Needs = map[sim.NeedKey]int{"hunger": 0, "thirst": 24}
	a.Inventory = map[sim.ItemKind]int{"bread": 1}
	a.ToolWear = map[sim.ItemKind]int{"axe": 1}

	report := snapshotWith(a).ClampToPersistable()

	if !report.Clean() {
		t.Fatalf("clean snapshot produced clamps: %s", report.Summary())
	}
	if report.Summary() != "" {
		t.Errorf("Summary = %q, want empty on a clean report", report.Summary())
	}
	// The boundary values must survive: 0 and 24 are IN range, not out of it.
	if a.Needs["hunger"] != 0 || a.Needs["thirst"] != 24 {
		t.Errorf("boundary needs were altered: %v", a.Needs)
	}
	if *a.ScheduleStartMin != 480 || *a.ScheduleEndMin != 1080 {
		t.Errorf("schedule was altered: %d/%d", *a.ScheduleStartMin, *a.ScheduleEndMin)
	}
}

// TestClamp_ProjectsEachRangedField walks every clamped field, one case each,
// checking both the corrected value and that the correction is REPORTED. A clamp
// that fires silently would be the thing this ticket rejected.
func TestClamp_ProjectsEachRangedField(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*sim.Actor)
		check      func(*testing.T, *sim.Actor)
		wantTable  string
		wantField  string
		wantFromTo [2]string
	}{
		{
			name:   "need above ceiling",
			mutate: func(a *sim.Actor) { a.Needs = map[sim.NeedKey]int{"hunger": 25} },
			check: func(t *testing.T, a *sim.Actor) {
				if a.Needs["hunger"] != 24 {
					t.Errorf("hunger = %d, want 24", a.Needs["hunger"])
				}
			},
			wantTable: "actor_need", wantField: "value", wantFromTo: [2]string{"25", "24"},
		},
		{
			name:   "need below floor",
			mutate: func(a *sim.Actor) { a.Needs = map[sim.NeedKey]int{"thirst": -1} },
			check: func(t *testing.T, a *sim.Actor) {
				if a.Needs["thirst"] != 0 {
					t.Errorf("thirst = %d, want 0", a.Needs["thirst"])
				}
			},
			wantTable: "actor_need", wantField: "value", wantFromTo: [2]string{"-1", "0"},
		},
		{
			name:   "negative inventory clamps to the no-row case",
			mutate: func(a *sim.Actor) { a.Inventory = map[sim.ItemKind]int{"bread": -3} },
			check: func(t *testing.T, a *sim.Actor) {
				if a.Inventory["bread"] != 0 {
					t.Errorf("bread = %d, want 0", a.Inventory["bread"])
				}
			},
			wantTable: "actor_inventory", wantField: "quantity", wantFromTo: [2]string{"-3", "0"},
		},
		{
			name:   "inventory above SMALLINT",
			mutate: func(a *sim.Actor) { a.Inventory = map[sim.ItemKind]int{"bread": 40000} },
			check: func(t *testing.T, a *sim.Actor) {
				if a.Inventory["bread"] != 32767 {
					t.Errorf("bread = %d, want 32767 (the column is SMALLINT; pgx refuses to encode 40000)", a.Inventory["bread"])
				}
			},
			wantTable: "actor_inventory", wantField: "quantity", wantFromTo: [2]string{"40000", "32767"},
		},
		{
			name:   "spent tool wear",
			mutate: func(a *sim.Actor) { a.ToolWear = map[sim.ItemKind]int{"axe": 0} },
			check: func(t *testing.T, a *sim.Actor) {
				if a.ToolWear["axe"] != 1 {
					t.Errorf("axe wear = %d, want 1", a.ToolWear["axe"])
				}
			},
			wantTable: "actor_inventory", wantField: "uses_left", wantFromTo: [2]string{"0", "1"},
		},
		{
			name: "negative interaction count",
			mutate: func(a *sim.Actor) {
				a.Relationships = map[sim.ActorID]*sim.Relationship{"b": {InteractionCount: -7}}
			},
			check: func(t *testing.T, a *sim.Actor) {
				if a.Relationships["b"].InteractionCount != 0 {
					t.Errorf("InteractionCount = %d, want 0", a.Relationships["b"].InteractionCount)
				}
			},
			wantTable: "actor_relationship", wantField: "interaction_count", wantFromTo: [2]string{"-7", "0"},
		},
		{
			name:   "invalid facing falls back to the column default",
			mutate: func(a *sim.Actor) { a.Facing = "up" },
			check: func(t *testing.T, a *sim.Actor) {
				if a.Facing != "south" {
					t.Errorf("Facing = %q, want south", a.Facing)
				}
			},
			wantTable: "actor", wantField: "facing", wantFromTo: [2]string{"up", "south"},
		},
		{
			name: "non-positive production cycle",
			mutate: func(a *sim.Actor) {
				a.ProductionActivity = &sim.ProductionActivity{Item: "stew", BatchQty: 5, RemainingSeconds: -12}
			},
			check: func(t *testing.T, a *sim.Actor) {
				if a.ProductionActivity.RemainingSeconds != 1 {
					t.Errorf("RemainingSeconds = %d, want 1", a.ProductionActivity.RemainingSeconds)
				}
			},
			wantTable: "actor", wantField: "production_remaining_seconds", wantFromTo: [2]string{"-12", "1"},
		},
		{
			name: "non-negative dwell delta",
			mutate: func(a *sim.Actor) {
				a.DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
					{ObjectID: "o1", Attribute: "warmth", Source: sim.DwellSourceObject}: {
						ObjectID: "o1", Attribute: "warmth", Source: sim.DwellSourceObject,
						DwellDelta: 0, DwellPeriodMinutes: 10,
					},
				}
			},
			check: func(t *testing.T, a *sim.Actor) {
				for _, dc := range a.DwellCredits {
					if dc.DwellDelta != -1 {
						t.Errorf("DwellDelta = %d, want -1 (the column CHECKs < 0)", dc.DwellDelta)
					}
				}
			},
			wantTable: "actor_dwell_credit", wantField: "dwell_delta", wantFromTo: [2]string{"0", "-1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := baseActor()
			tc.mutate(a)
			report := snapshotWith(a).ClampToPersistable()

			if report.Total() != 1 {
				t.Fatalf("Total = %d, want exactly 1 correction: %s", report.Total(), report.Summary())
			}
			got := report.Clamps()[0]
			if got.Table != tc.wantTable || got.Field != tc.wantField {
				t.Errorf("clamp = %s.%s, want %s.%s", got.Table, got.Field, tc.wantTable, tc.wantField)
			}
			if got.From != tc.wantFromTo[0] || got.To != tc.wantFromTo[1] {
				t.Errorf("clamp = %s→%s, want %s→%s", got.From, got.To, tc.wantFromTo[0], tc.wantFromTo[1])
			}
			tc.check(t, a)
		})
	}
}

// TestClamp_ScheduleBoundsAreIndependent — the two schedule minutes clamp against
// their own bound, not each other's. A start of 5000 and an end of -30 are two
// separate corrections, and the half-set case (one nil) is NOT repaired: which
// bound is missing is unknowable, so it stays a hard failure in the writer.
func TestClamp_ScheduleBoundsAreIndependent(t *testing.T) {
	start, end := 5000, -30
	a := baseActor()
	a.ScheduleStartMin, a.ScheduleEndMin = &start, &end

	report := snapshotWith(a).ClampToPersistable()

	if report.Total() != 2 {
		t.Fatalf("Total = %d, want 2: %s", report.Total(), report.Summary())
	}
	if *a.ScheduleStartMin != 1439 {
		t.Errorf("ScheduleStartMin = %d, want 1439", *a.ScheduleStartMin)
	}
	if *a.ScheduleEndMin != 0 {
		t.Errorf("ScheduleEndMin = %d, want 0", *a.ScheduleEndMin)
	}

	// Half-set: left exactly as-is for the writer to refuse.
	bad := 700
	h := baseActor()
	h.ScheduleStartMin = &bad
	if r := snapshotWith(h).ClampToPersistable(); !r.Clean() {
		t.Errorf("half-set schedule produced clamps (%s) — the missing bound cannot be invented, so it must stay a hard failure", r.Summary())
	}
	if h.ScheduleEndMin != nil {
		t.Error("clamp invented a schedule end bound")
	}
}

// TestClamp_DepositClampsAgainstTheCorrectedAmount — deposit_amount's ceiling is
// offered_amount, so the two clamps are ORDERED. A negative amount and an
// over-large deposit in the same order must end up with the deposit projected
// onto the CORRECTED amount (0), not the original (-5), or the pair still
// violates the CHECK and the checkpoint still aborts.
func TestClamp_DepositClampsAgainstTheCorrectedAmount(t *testing.T) {
	cp := &sim.CheckpointSnapshot{
		Orders: map[sim.OrderID]*sim.Order{
			7: {ID: 7, Qty: 0, Amount: -5, Deposit: 12},
		},
	}
	report := cp.ClampToPersistable()

	o := cp.Orders[7]
	if o.Qty != 1 {
		t.Errorf("Qty = %d, want 1 (CHECK qty > 0)", o.Qty)
	}
	if o.Amount != 0 {
		t.Errorf("Amount = %d, want 0", o.Amount)
	}
	if o.Deposit != 0 {
		t.Errorf("Deposit = %d, want 0 — it must clamp against the CORRECTED amount, or deposit > offered still violates the CHECK", o.Deposit)
	}
	if report.Total() != 3 {
		t.Errorf("Total = %d, want 3: %s", report.Total(), report.Summary())
	}
}

// TestClamp_LeavesUnclampableStateAlone — the line the design draws. None of
// these have a nearest legal value, so the pass must NOT touch them: they belong
// to the writer, which fails the checkpoint atomically and loudly.
func TestClamp_LeavesUnclampableStateAlone(t *testing.T) {
	a := baseActor()
	a.State = ""       // no default FSM state to guess
	a.DisplayName = "" // load-bearing for prompts
	a.Relationships = map[sim.ActorID]*sim.Relationship{
		a.ID: {InteractionCount: 3}, // self-relationship: violates a CHECK, unrepairable
	}

	report := snapshotWith(a).ClampToPersistable()

	if !report.Clean() {
		t.Fatalf("clamp report = %s, want CLEAN — repairing these would fabricate world state, which is exactly what this design rejects", report.Summary())
	}
	if a.State != "" || a.DisplayName != "" {
		t.Error("clamp invented identity/FSM state")
	}
	if _, self := a.Relationships[a.ID]; !self {
		t.Error("clamp removed the self-relationship — dropping rows is not this pass's job")
	}
}

// TestClamp_ReportCapsDetailButNotCount — a world bug that overshoots a need
// tends to do it to EVERY actor at once, and this report is pasted into operator
// responses. The detail is capped; the count must stay exact, or the operator
// reads "64 corrections" during an event that corrupted thousands.
func TestClamp_ReportCapsDetailButNotCount(t *testing.T) {
	const n = 200
	cp := &sim.CheckpointSnapshot{Actors: make(map[sim.ActorID]*sim.Actor, n)}
	for i := 0; i < n; i++ {
		id := sim.ActorID("actor-" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('0'+i/26)))
		cp.Actors[id] = &sim.Actor{
			ID: id, DisplayName: "A", State: sim.StateIdle,
			Needs: map[sim.NeedKey]int{"hunger": 99},
		}
	}

	report := cp.ClampToPersistable()

	if report.Total() != len(cp.Actors) {
		t.Errorf("Total = %d, want %d — the COUNT must be exact even when the detail is capped", report.Total(), len(cp.Actors))
	}
	if got := len(report.Clamps()); got != 64 {
		t.Errorf("retained detail = %d, want the 64 cap", got)
	}
	if !strings.Contains(report.Summary(), "more") {
		t.Errorf("Summary must say how many corrections it omitted: %s", report.Summary())
	}
}

// TestClamp_NeverTouchesTheLiveWorld is the load-bearing safety test.
//
// The clamp mutates the snapshot, and it runs OFF the world goroutine while the
// world keeps ticking. That is only safe because BuildCheckpointSnapshot returns
// a genuine deep clone. It did not, quite: CloneActor copied ScheduleStartMin /
// ScheduleEndMin (both *int) by value out of `cp := *a`, so the clone shared those
// two ints with the live actor and a clamp would have written 1439 straight into
// the running world, concurrently with the world goroutine reading it.
//
// This test fails against that bug. Do not delete it.
func TestClamp_NeverTouchesTheLiveWorld(t *testing.T) {
	start, end := 5000, -30
	live := &sim.Actor{
		ID: "hannah", DisplayName: "Hannah", State: sim.StateIdle,
		Facing:           "up",
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Needs:            map[sim.NeedKey]int{"hunger": 99},
		Inventory:        map[sim.ItemKind]int{"bread": -3},
		Relationships:    map[sim.ActorID]*sim.Relationship{"other": {InteractionCount: -7}},
		ProductionActivity: &sim.ProductionActivity{
			Item: "stew", BatchQty: 0, RemainingSeconds: -12,
		},
	}
	w, cancel := runningWorld(t, map[sim.ActorID]*sim.Actor{live.ID: live})
	defer cancel()

	res, err := w.SendContext(context.Background(), sim.CheckpointSnapshotCommand())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	cp := res.(*sim.CheckpointSnapshot)

	report := cp.ClampToPersistable()
	if report.Clean() {
		t.Fatal("expected clamps on the snapshot")
	}
	// The snapshot is corrected...
	if got := cp.Actors["hannah"]; *got.ScheduleStartMin != 1439 || got.Needs["hunger"] != 24 {
		t.Fatalf("snapshot was not clamped: start=%d hunger=%d", *got.ScheduleStartMin, got.Needs["hunger"])
	}

	// ...and the LIVE actor still holds every original value. Read it back through
	// the world goroutine, which is the only safe way to look at him.
	got, err := w.SendContext(context.Background(), sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CloneActor(world.Actors["hannah"]), nil
		},
	})
	if err != nil {
		t.Fatalf("read live actor: %v", err)
	}
	liveNow := got.(*sim.Actor)

	if *liveNow.ScheduleStartMin != 5000 || *liveNow.ScheduleEndMin != -30 {
		t.Errorf("THE CLAMP WROTE THROUGH INTO THE LIVE WORLD: schedule = %d/%d, want the original 5000/-30. CloneActor must copyIntPtr both schedule fields",
			*liveNow.ScheduleStartMin, *liveNow.ScheduleEndMin)
	}
	if liveNow.Needs["hunger"] != 99 {
		t.Errorf("live hunger = %d, want the original 99 — the clamp must not reach live state", liveNow.Needs["hunger"])
	}
	if liveNow.Inventory["bread"] != -3 {
		t.Errorf("live bread = %d, want the original -3", liveNow.Inventory["bread"])
	}
	if liveNow.Relationships["other"].InteractionCount != -7 {
		t.Errorf("live InteractionCount = %d, want the original -7", liveNow.Relationships["other"].InteractionCount)
	}
	if liveNow.ProductionActivity.RemainingSeconds != -12 {
		t.Errorf("live RemainingSeconds = %d, want the original -12", liveNow.ProductionActivity.RemainingSeconds)
	}
	if liveNow.Facing != "up" {
		t.Errorf("live Facing = %q, want the original %q", liveNow.Facing, "up")
	}
}

// TestClamp_NilSafety — a nil report and a nil snapshot are no-ops. The report is
// handed to CheckpointHealth.RecordSuccess, which tests and non-checkpoint callers
// pass nil.
func TestClamp_NilSafety(t *testing.T) {
	var nilReport *sim.ClampReport
	if !nilReport.Clean() || nilReport.Total() != 0 || nilReport.Clamps() != nil || nilReport.Summary() != "" {
		t.Error("a nil *ClampReport must be a no-op on every method")
	}
	var nilSnapshot *sim.CheckpointSnapshot
	if r := nilSnapshot.ClampToPersistable(); !r.Clean() {
		t.Error("a nil snapshot must produce a clean report, not panic")
	}
}
