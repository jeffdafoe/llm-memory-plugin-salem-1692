package sim_test

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildReturnerTestWorld stands up a running world with a transient visitor
// (in-window, so FinalizeLoad rehydrates it into w.Actors), a PC, and a stateful
// NPC, and registers the returner subscriber. seedRecurring is optionally seeded
// into the durable returner tier before load. Returns the world + a stop func.
func buildReturnerTestWorld(
	t *testing.T,
	visitor *sim.LoadedVisitor,
	seedRecurring map[sim.RecurringVisitorID]*sim.RecurringVisitor,
) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"pc-jeff": {
			ID: "pc-jeff", DisplayName: "Jeff",
			Kind: sim.KindPC, State: sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"ezekiel": {
			ID: "ezekiel", DisplayName: "Ezekiel Crane",
			Kind: sim.KindNPCStateful, State: sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
	})
	if visitor != nil {
		handles.Visitors.Seed(map[sim.ActorID]*sim.LoadedVisitor{visitor.ID: visitor})
	}
	if seedRecurring != nil {
		handles.RecurringVisitors.Seed(seedRecurring)
	}

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	sim.RegisterVisitorReturnerSubscriber(w)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	return w, func() { cancel(); <-done }
}

// inWindowVisitor builds a LoadedVisitor whose stay is still open (rehydrated on
// load), with the given RecurringID link.
func inWindowVisitor(id sim.ActorID, recurringID string) *sim.LoadedVisitor {
	return &sim.LoadedVisitor{
		ID:          id,
		DisplayName: "Elias Drum the peddler",
		Pos:         sim.TilePos{X: sim.PadX + 3, Y: sim.PadY + 3},
		VisitorState: &sim.VisitorState{
			Archetype: "peddler", Origin: "Boston", Disposition: "weary",
			ExpiresAt: time.Now().UTC().Add(2 * time.Hour), Phase: sim.VisitorPhasePresent,
			RecurringID: recurringID,
		},
	}
}

// withWorld runs fn synchronously on the world goroutine so a test can read/mutate
// live world state without racing Run. w.Send blocks until fn returns, so values
// fn captures into outer vars are safely visible afterward.
func withWorld(t *testing.T, w *sim.World, fn func(*sim.World)) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		fn(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("withWorld Send: %v", err)
	}
}

func TestReturner_PromotesOnPCMeet(t *testing.T) {
	w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", ""), nil)
	defer stop()

	at := time.Now().UTC()
	emitInCommand(t, w, &sim.ActorMet{A: "vstr-0000aaaa", B: "pc-jeff", At: at})

	// The traveler is now linked to a durable returner identity.
	rid := w.Published().Actors["vstr-0000aaaa"].VisitorState.RecurringID
	if rid == "" {
		t.Fatal("meeting a PC did not promote the visitor (RecurringID still empty)")
	}

	var count int
	var name, archetype string
	var acqName string
	var firstMet, lastMet time.Time
	withWorld(t, w, func(world *sim.World) {
		count = len(world.RecurringVisitors)
		rv := world.RecurringVisitors[sim.RecurringVisitorID(rid)]
		if rv != nil {
			name, archetype = rv.Name, rv.Archetype
			if acq := rv.Acquaintances["pc-jeff"]; acq != nil {
				acqName = acq.PCDisplayName
				firstMet, lastMet = acq.FirstMetAt, acq.LastMetAt
			}
		}
	})
	if count != 1 {
		t.Fatalf("recurring_visitor count = %d, want 1", count)
	}
	if name != "Elias Drum" || archetype != "peddler" {
		t.Errorf("returner persona = %q the %q, want Elias Drum the peddler", name, archetype)
	}
	if acqName != "Jeff" {
		t.Errorf("acquaintance pc name = %q, want Jeff", acqName)
	}
	if !firstMet.Equal(at) || !lastMet.Equal(at) {
		t.Errorf("acquaintance times = first %v last %v, want both %v", firstMet, lastMet, at)
	}
}

func TestReturner_NoPromotionOnNPCMeet(t *testing.T) {
	w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", ""), nil)
	defer stop()

	emitInCommand(t, w, &sim.ActorMet{A: "vstr-0000aaaa", B: "ezekiel", At: time.Now().UTC()})

	if rid := w.Published().Actors["vstr-0000aaaa"].VisitorState.RecurringID; rid != "" {
		t.Errorf("visitor↔NPC meeting promoted the visitor (RecurringID=%q); only a PC meeting should", rid)
	}
	var count int
	withWorld(t, w, func(world *sim.World) { count = len(world.RecurringVisitors) })
	if count != 0 {
		t.Errorf("recurring_visitor count = %d, want 0 (no PC meeting)", count)
	}
}

func TestReturner_SecondMeetBumpsLastMetNotFirst(t *testing.T) {
	w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", ""), nil)
	defer stop()

	first := time.Now().UTC()
	second := first.Add(90 * time.Minute)
	emitInCommand(t, w, &sim.ActorMet{A: "vstr-0000aaaa", B: "pc-jeff", At: first})
	emitInCommand(t, w, &sim.ActorMet{A: "pc-jeff", B: "vstr-0000aaaa", At: second})

	rid := w.Published().Actors["vstr-0000aaaa"].VisitorState.RecurringID
	var count int
	var firstMet, lastMet time.Time
	withWorld(t, w, func(world *sim.World) {
		count = len(world.RecurringVisitors)
		if rv := world.RecurringVisitors[sim.RecurringVisitorID(rid)]; rv != nil {
			if acq := rv.Acquaintances["pc-jeff"]; acq != nil {
				firstMet, lastMet = acq.FirstMetAt, acq.LastMetAt
			}
		}
	})
	if count != 1 {
		t.Fatalf("recurring_visitor count = %d, want 1 (no duplicate on second meet)", count)
	}
	if !firstMet.Equal(first) {
		t.Errorf("FirstMetAt = %v, want %v (first meet preserved)", firstMet, first)
	}
	if !lastMet.Equal(second) {
		t.Errorf("LastMetAt = %v, want %v (bumped to second meet)", lastMet, second)
	}
}

func TestReturner_DepartureSchedulesReturn(t *testing.T) {
	rid := "rvis-0000abcd"
	seed := map[sim.RecurringVisitorID]*sim.RecurringVisitor{
		sim.RecurringVisitorID(rid): {
			ID: sim.RecurringVisitorID(rid), Name: "Elias Drum", Archetype: "peddler",
			Origin: "Boston", Disposition: "weary", VisitCount: 1,
			FirstSeenAt: time.Now().UTC().Add(-time.Hour), LastSeenAt: time.Now().UTC().Add(-time.Hour),
			Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{},
		},
	}
	w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", rid), seed)
	defer stop()

	// Drive the visitor cascade with a clock past ExpiresAt + grace so cleanup
	// removes the traveler and schedules its comeback. Force a min/max window so
	// the return interval is deterministic.
	future := time.Now().UTC().Add(6 * time.Hour)
	withWorld(t, w, func(world *sim.World) {
		world.Settings.VisitorReturnMinDays = 10
		world.Settings.VisitorReturnMaxDays = 10
	})
	if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{
		Now: future, Rand: rand.New(rand.NewSource(1)),
	})); err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}

	if a := w.Published().Actors["vstr-0000aaaa"]; a != nil {
		t.Error("departed visitor should have been cleaned up from Actors")
	}
	var next, lastSeen time.Time
	var visitCount int
	withWorld(t, w, func(world *sim.World) {
		if rv := world.RecurringVisitors[sim.RecurringVisitorID(rid)]; rv != nil {
			next, lastSeen, visitCount = rv.NextReturnAt, rv.LastSeenAt, rv.VisitCount
		}
	})
	wantNext := future.Add(10 * 24 * time.Hour)
	if !next.Equal(wantNext) {
		t.Errorf("NextReturnAt = %v, want %v (10d after departure)", next, wantNext)
	}
	if !lastSeen.Equal(future) {
		t.Errorf("LastSeenAt = %v, want departure time %v", lastSeen, future)
	}
	if visitCount != 1 {
		t.Errorf("VisitCount = %d, want 1 (departure doesn't bump; the return spawn does)", visitCount)
	}
}

func TestReturner_SnapshotGatedOnPriorVisit(t *testing.T) {
	// A returner on a repeat visit (VisitCount >= 2) projects continuity; a
	// first-visit traveler (VisitCount 1) does not.
	twentyDaysAgo := time.Now().UTC().Add(-20 * 24 * time.Hour)
	build := func(visitCount int) *sim.ReturnerSnapshot {
		rid := "rvis-0000beef"
		seed := map[sim.RecurringVisitorID]*sim.RecurringVisitor{
			sim.RecurringVisitorID(rid): {
				ID: sim.RecurringVisitorID(rid), Name: "Elias Drum", Archetype: "peddler",
				Origin: "Boston", Disposition: "weary", VisitCount: visitCount,
				FirstSeenAt: twentyDaysAgo, LastSeenAt: twentyDaysAgo,
				Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{
					"pc-jeff": {PCActorID: "pc-jeff", PCDisplayName: "Jeff", FirstMetAt: twentyDaysAgo, LastMetAt: twentyDaysAgo},
				},
			},
		}
		w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", rid), seed)
		defer stop()
		return w.Published().Actors["vstr-0000aaaa"].Returner
	}

	if r := build(1); r != nil {
		t.Errorf("first-visit traveler projected a Returner snapshot: %+v (want nil)", r)
	}
	r := build(3)
	if r == nil {
		t.Fatal("repeat-visit returner projected no Returner snapshot")
	}
	if r.VisitCount != 3 {
		t.Errorf("Returner.VisitCount = %d, want 3", r.VisitCount)
	}
	if len(r.KnownHere) != 1 || r.KnownHere[0].DisplayName != "Jeff" {
		t.Fatalf("Returner.KnownHere = %+v, want one entry for Jeff", r.KnownHere)
	}
	if r.KnownHere[0].Recency != sim.RecencyWeeks {
		t.Errorf("KnownHere recency = %v, want RecencyWeeks (~20 days)", r.KnownHere[0].Recency)
	}
}

func TestPickDueReturner(t *testing.T) {
	// A non-Run world so pickDueReturner can be exercised single-threaded.
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	now := time.Now().UTC()
	due := &sim.RecurringVisitor{ID: "rvis-00000001", Name: "A", NextReturnAt: now.Add(-time.Hour)}
	notYet := &sim.RecurringVisitor{ID: "rvis-00000002", Name: "B", NextReturnAt: now.Add(48 * time.Hour)}
	unscheduled := &sim.RecurringVisitor{ID: "rvis-00000003", Name: "C"} // NextReturnAt zero
	w.RecurringVisitors = map[sim.RecurringVisitorID]*sim.RecurringVisitor{
		due.ID: due, notYet.ID: notYet, unscheduled.ID: unscheduled,
	}

	got, ok := sim.PickDueReturnerForTest(w, now)
	if !ok || got == nil || got.ID != due.ID {
		t.Fatalf("pickDueReturner = %v (ok=%v), want the due returner %s", got, ok, due.ID)
	}

	// With an in-flight visitor already linked to the due returner, it is not
	// picked again (no double-spawn).
	w.Actors["vstr-0000cccc"] = &sim.Actor{
		ID: "vstr-0000cccc", Kind: sim.KindNPCShared,
		VisitorState: &sim.VisitorState{Phase: sim.VisitorPhasePresent, RecurringID: string(due.ID)},
	}
	if got, ok := sim.PickDueReturnerForTest(w, now); ok {
		t.Errorf("pickDueReturner returned %s while it is already present", got.ID)
	}
}
