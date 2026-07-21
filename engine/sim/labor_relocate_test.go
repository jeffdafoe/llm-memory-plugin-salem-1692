package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_relocate_test.go — LLM-229. AcceptWork's branch selection driven through
// the full command: a deal struck at the employer's own post with the owner
// present (or a workless employer) starts the work window in place; a deal
// struck off-site flips the offer to EnRoute and leaves the worker's laboring
// mirror UNSET until the arrival subscriber starts the work (that arrival flip
// is covered white-box in labor_relocate_internal_test.go). buildLaborWorld
// seeds no structure placement, so the EnRoute relocation walk can't resolve and
// simply no-ops — which is fine here: these tests assert the accept-time state,
// not the walk.

// TestAcceptWork_ImmediateStartWhenAtEmployerPost — deal struck at the
// employer's own post with the owner present: the work window starts
// immediately in place, exactly as before LLM-229 (the seek-work-at-shop case).
// actorAtWorkpost is satisfied by both parties being InsideStructureID == the
// employer's work structure.
func TestAcceptWork_ImmediateStartWhenAtEmployerPost(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true, insideStruct: "store"},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50, workStruct: "store", insideStruct: "store"},
	})
	defer stop()

	now := time.Date(2026, 6, 24, 13, 0, 0, 0, time.UTC)
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30,
		State:    sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(2 * time.Minute),
	})

	res, err := w.Send(sim.AcceptWork("josiah", 1, now))
	if err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}
	if out := res.(sim.LaborAcceptResult); out.State != sim.LaborStateWorking {
		t.Errorf("result State = %q, want working (on-site hire starts immediately)", out.State)
	}
	wantUntil := now.Add(30 * time.Minute)
	o := readLaborLedger(t, w)[1]
	if o.State != sim.LaborStateWorking {
		t.Errorf("offer State = %q, want working", o.State)
	}
	if o.WorkStartedAt == nil || !o.WorkStartedAt.Equal(now) {
		t.Errorf("offer WorkStartedAt = %v, want %v (starts at accept for an on-site hire)", o.WorkStartedAt, now)
	}
	if o.WorkingUntil == nil || !o.WorkingUntil.Equal(wantUntil) {
		t.Errorf("offer WorkingUntil = %v, want %v", o.WorkingUntil, wantUntil)
	}
	if ws := readActor(t, w, "ezekiel"); ws.State != sim.StateLaboring {
		t.Errorf("worker State = %q, want laboring", ws.State)
	}
}

// TestAcceptWork_RelocatesWhenStruckOffSite — deal struck away from the
// employer's workplace: the offer flips to EnRoute with a bounded-wait deadline,
// and the worker's laboring mirror stays UNSET (they're relocating, not yet
// working). This is the core LLM-229 change — no more paying for presence in the
// wrong building.
func TestAcceptWork_RelocatesWhenStruckOffSite(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		// Deal struck in the tavern huddle; neither party is at Josiah's store.
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50, workStruct: "store"},
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Date(2026, 6, 24, 13, 0, 0, 0, time.UTC)
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30,
		State:    sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(2 * time.Minute),
	})

	res, err := w.Send(sim.AcceptWork("josiah", 1, now))
	if err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}
	out := res.(sim.LaborAcceptResult)
	if out.State != sim.LaborStateEnRoute {
		t.Fatalf("result State = %q, want en_route (off-site hire relocates)", out.State)
	}
	if !out.WorkingUntil.IsZero() {
		t.Errorf("result WorkingUntil = %v, want zero (window unknown until arrival)", out.WorkingUntil)
	}

	o := readLaborLedger(t, w)[1]
	if o.State != sim.LaborStateEnRoute {
		t.Errorf("offer State = %q, want en_route", o.State)
	}
	if o.AcceptedAt == nil || !o.AcceptedAt.Equal(now) {
		t.Errorf("offer AcceptedAt = %v, want %v", o.AcceptedAt, now)
	}
	if o.EnRouteDeadline.IsZero() {
		t.Errorf("offer EnRouteDeadline is zero, want a bounded-wait deadline")
	}
	if o.WorkStartedAt != nil || o.WorkingUntil != nil {
		t.Errorf("work window must be unset while EnRoute (WorkStartedAt=%v WorkingUntil=%v)", o.WorkStartedAt, o.WorkingUntil)
	}

	// The worker's laboring mirror stays clear — they're relocating, not working.
	ws := readActor(t, w, "ezekiel")
	if ws.State == sim.StateLaboring {
		t.Errorf("worker State = laboring, want NOT laboring while en_route")
	}
	if ws.LaborID != 0 || ws.LaboringUntil != nil {
		t.Errorf("worker mirror set while en_route: LaborID=%d LaboringUntil=%v", ws.LaborID, ws.LaboringUntil)
	}

	// The hire itself still happened — the action-log "hired" beat keys off this.
	if len(events.Accepted) != 1 || events.Accepted[0].LaborID != 1 {
		t.Errorf("LaborOfferAccepted = %+v, want one for labor 1", events.Accepted)
	}
}

// TestAcceptWork_SecondJobBlockedWhileEnRoute — a worker relocating to an
// accepted job is already committed: workerHasLiveJob (extended to count
// EnRoute) blocks them soliciting a second job mid-walk, which would strand the
// first.
func TestAcceptWork_SecondJobBlockedWhileEnRoute(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50, workStruct: "store"},
	})
	defer stop()

	now := time.Date(2026, 6, 24, 13, 0, 0, 0, time.UTC)
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30,
		State:    sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(2 * time.Minute),
	})
	if _, err := w.Send(sim.AcceptWork("josiah", 1, now)); err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}

	// Ezekiel is now EnRoute to Josiah's store. Any fresh solicit is refused.
	_, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 5, nil, 240, now))
	if err == nil {
		t.Fatalf("SolicitWork while en_route: got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "already on a job") {
		t.Errorf("SolicitWork rejection = %q, want the already-on-a-job message", err.Error())
	}
}
