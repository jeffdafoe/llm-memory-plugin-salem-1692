package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_render_test.go — LLM-26. Coverage of the labor perception surface: the
// employer decision section (renderLaborOffers, carrying the load-bearing
// labor_id), the worker self-state line (renderLaborSelfState), the free-worker
// affordance (renderLaborAffordance), the shared gate predicate
// (PendingLaborOffers), and the Build-side scans (buildLaborOffersForMe /
// buildLaboring / subjectIsWorker).

func TestPendingLaborOffers_ReadsStandingView(t *testing.T) {
	p := Payload{
		ActorID: "josiah",
		LaborOffersForMe: []LaborOfferView{
			{LaborID: 3, Worker: "ezekiel", Reward: 10, DurationMin: 30},
			{LaborID: 5, Worker: "mary", Reward: 4, DurationMin: 15},
		},
	}
	offers := PendingLaborOffers(p)
	if len(offers) != 2 || offers[0].LaborID != 3 || offers[1].LaborID != 5 {
		t.Fatalf("PendingLaborOffers = %+v, want labor ids 3,5", offers)
	}
}

func TestRender_LaborOfferDecisionSection(t *testing.T) {
	p := Payload{
		ActorID: "josiah",
		// Coins cover the 10-coin reward, so this exercises the affordable path:
		// the accept_work/decline_work footer. The broke-employer decline steer
		// (LLM-158) is covered by TestRenderLaborOffers_AffordabilitySteer.
		Actor: ActorView{State: sim.StateIdle, Coins: 20},
		LaborOffersForMe: []LaborOfferView{
			{LaborID: 7, Worker: "ezekiel", Reward: 10, DurationMin: 120},
		},
		WarrantActorNames: map[sim.ActorID]string{"ezekiel": "Ezekiel"},
		Baseline:          BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))

	if !strings.Contains(out, "## Work offers awaiting your decision") {
		t.Errorf("labor decision section header missing\n%s", out)
	}
	// The labor_id MUST render — the model echoes it into accept_work.
	if !strings.Contains(out, "offer id 7") {
		t.Errorf("labor_id not rendered\n%s", out)
	}
	for _, want := range []string{"Ezekiel", "10 coins", "about 2 hours of work"} {
		if !strings.Contains(out, want) {
			t.Errorf("labor offer line missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "accept_work") || !strings.Contains(out, "decline_work") || !strings.Contains(out, "labor_id") {
		t.Errorf("labor respond instruction missing\n%s", out)
	}
}

func TestRender_LaborSelfState_Working(t *testing.T) {
	base := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	p := Payload{
		ActorID:           "ezekiel",
		Actor:             ActorView{State: sim.StateLaboring},
		Laboring:          &LaboringView{Employer: "josiah", Until: base.Add(25 * time.Minute)},
		WarrantActorNames: map[sim.ActorID]string{"josiah": "Josiah"},
		RenderedAt:        base,
		Baseline:          BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))

	if !strings.Contains(out, "working a job for Josiah") {
		t.Errorf("worker self-state line missing employer\n%s", out)
	}
	if !strings.Contains(out, "about 25 minutes of work left") {
		t.Errorf("worker self-state line missing time-remaining\n%s", out)
	}
	if !strings.Contains(out, "paid when you finish") {
		t.Errorf("worker self-state line missing payment nudge\n%s", out)
	}
}

func TestRender_LaborAffordance(t *testing.T) {
	p := Payload{
		ActorID:        "ezekiel",
		Actor:          ActorView{State: sim.StateIdle},
		CanSolicitWork: true,
		Baseline:       BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "solicit_work") || !strings.Contains(out, "take work for pay") {
		t.Errorf("free-worker affordance cue missing\n%s", out)
	}

	// Absent when the actor can't solicit.
	p.CanSolicitWork = false
	if out := combinedPrompt(Render(p, DefaultRenderConfig())); strings.Contains(out, "solicit_work") {
		t.Errorf("solicit_work affordance leaked when CanSolicitWork is false\n%s", out)
	}
}

func TestHumanizeWorkMinutes(t *testing.T) {
	cases := map[int]string{
		30:  "30 minutes",
		60:  "1 hour",
		120: "2 hours",
		90:  "1 hour 30 minutes",
		135: "2 hours 15 minutes",
	}
	for min, want := range cases {
		if got := humanizeWorkMinutes(min); got != want {
			t.Errorf("humanizeWorkMinutes(%d) = %q, want %q", min, got, want)
		}
	}
}

// TestBuildLaborViews_ScanShape — the Build-side scans project the snapshot
// LaborLedger into the employer decision view + the worker self-state, and read
// the worker marker off AttributeSlugs.
func TestBuildLaborViews_ScanShape(t *testing.T) {
	until := time.Date(2026, 6, 26, 12, 30, 0, 0, time.UTC)
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"josiah":  {DisplayName: "Josiah"},
			"ezekiel": {DisplayName: "Ezekiel", AttributeSlugs: []string{"worker"}},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "ezekiel", EmployerID: "josiah", Reward: 10, DurationMin: 30, State: sim.LaborStatePending},
			2: {ID: 2, WorkerID: "mary", EmployerID: "josiah", Reward: 4, DurationMin: 15, State: sim.LaborStateWorking, WorkingUntil: &until},
			3: {ID: 3, WorkerID: "ezekiel", EmployerID: "john", Reward: 6, DurationMin: 20, State: sim.LaborStatePending},
		},
	}

	// Employer view: Josiah sees the PENDING offer staked against him (id 1),
	// not the Working one (id 2) and not the one to another employer (id 3).
	offers := buildLaborOffersForMe(snap, "josiah")
	if len(offers) != 1 || offers[0].LaborID != 1 || offers[0].Worker != "ezekiel" {
		t.Fatalf("buildLaborOffersForMe(josiah) = %+v, want one offer id 1 from ezekiel", offers)
	}
	if got := buildLaborOffersForMe(snap, "nobody"); got != nil {
		t.Errorf("buildLaborOffersForMe(nobody) = %+v, want nil", got)
	}

	// Worker self-state: Mary is mid-job (Working offer id 2).
	if mary := buildLaboring(snap, "mary"); mary == nil || mary.Employer != "josiah" || !mary.Until.Equal(until) {
		t.Errorf("buildLaboring(mary) = %+v, want employer josiah until %v", mary, until)
	}
	// Ezekiel only has pending offers, no Working one → not laboring.
	if ez := buildLaboring(snap, "ezekiel"); ez != nil {
		t.Errorf("buildLaboring(ezekiel) = %+v, want nil (no working offer)", ez)
	}

	// Worker marker read off AttributeSlugs.
	if !subjectIsWorker(snap.Actors["ezekiel"]) {
		t.Errorf("subjectIsWorker(ezekiel) = false, want true")
	}
	if subjectIsWorker(snap.Actors["josiah"]) {
		t.Errorf("subjectIsWorker(josiah) = true, want false")
	}
}

// TestBuildLaborOffersForMe_HelpedBeforeKeyedByWorker — LLM-228: the
// returning-helper recall flag is keyed by the SOLICITING worker. An employer
// who remembers Anne must mark only Anne's offer HelpedBeforeRecently, never
// Lewis's — the (a)-class mismatch (surfacing the recall for the wrong worker)
// the flag exists to avoid. Pins that buildLaborOffersForMe reads the employer's
// Observed store by o.WorkerID.
func TestBuildLaborOffersForMe_HelpedBeforeKeyedByWorker(t *testing.T) {
	now := time.Date(2026, 7, 3, 11, 0, 0, 0, time.UTC)
	hannah := &sim.ActorSnapshot{
		DisplayName: "Hannah",
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{PeerID: "anne", Condition: sim.ObservedHelpedByWorker}: now.Add(-time.Hour),
		}),
	}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": hannah,
			"anne":   {DisplayName: "Anne"},
			"lewis":  {DisplayName: "Lewis"},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "anne", EmployerID: "hannah", Reward: 3, DurationMin: 60, State: sim.LaborStatePending},
			2: {ID: 2, WorkerID: "lewis", EmployerID: "hannah", Reward: 3, DurationMin: 60, State: sim.LaborStatePending},
		},
	}

	got := map[sim.ActorID]bool{}
	for _, o := range buildLaborOffersForMe(snap, "hannah") {
		got[o.Worker] = o.HelpedBeforeRecently
	}
	if !got["anne"] {
		t.Errorf("Anne completed a job for Hannah — her offer should be HelpedBeforeRecently=true, got %+v", got)
	}
	if got["lewis"] {
		t.Errorf("Hannah has no helped-by memory of Lewis — his offer must not be flagged, got %+v", got)
	}
}

// TestBuildLaboring_PrefersActiveJob — if two Working offers ever coexist for a
// worker (the sweep-lag overlap state; unreachable in normal flow but defended),
// buildLaboring picks the one with the LATEST WorkingUntil — the active job —
// not the lowest LaborID (which here is the stale one). code_review.
func TestBuildLaboring_PrefersActiveJob(t *testing.T) {
	stale := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	active := time.Date(2026, 6, 26, 12, 30, 0, 0, time.UTC)
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": {DisplayName: "Ezekiel"}},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "ezekiel", EmployerID: "josiah", State: sim.LaborStateWorking, WorkingUntil: &stale},
			2: {ID: 2, WorkerID: "ezekiel", EmployerID: "john", State: sim.LaborStateWorking, WorkingUntil: &active},
		},
	}
	v := buildLaboring(snap, "ezekiel")
	if v == nil || !v.Until.Equal(active) || v.Employer != "john" {
		t.Errorf("buildLaboring = %+v, want the active job (john, %v)", v, active)
	}
}

// TestSubjectHasPendingLaborOfferOut — the initiator-side pending-offer check that
// hides the solicit_work / offer_work affordance + tool while a bid is
// outstanding. The fixture carries no InitiatedBy, the legacy solicit shape, so it
// also pins that a zero value still reads worker-initiated (LLM-346).
func TestSubjectHasPendingLaborOfferOut(t *testing.T) {
	snap := &sim.Snapshot{
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "ezekiel", EmployerID: "josiah", State: sim.LaborStatePending},
		},
	}
	if !subjectHasPendingLaborOfferOut(snap, "ezekiel") {
		t.Error("subjectHasPendingLaborOfferOut(ezekiel) = false, want true (has a pending offer out)")
	}
	if subjectHasPendingLaborOfferOut(snap, "josiah") {
		t.Error("subjectHasPendingLaborOfferOut(josiah) = true, want false (the employer, not the initiator)")
	}
	// The mirror: the employer is the one who owes an answer on a solicited offer.
	if !subjectHasLaborOfferToAnswer(snap, "josiah") {
		t.Error("subjectHasLaborOfferToAnswer(josiah) = false, want true (the employer must answer)")
	}
	if subjectHasLaborOfferToAnswer(snap, "ezekiel") {
		t.Error("subjectHasLaborOfferToAnswer(ezekiel) = true, want false (the worker made the offer)")
	}
}

// TestLaborOfferLivePending_ExpiredPendingRowDoesNotSuppress is the code_review
// catch on LLM-346. A pending offer sits in the ledger for up to a full sweep
// cadence (60s) after its 3-minute TTL elapses — only the aging sweep flips it
// Expired. The substrate already skips those rows (workerPendingLaborOffer's
// `!now.Before(o.ExpiresAt)`), so perception must too, or for up to a minute a
// worker cannot solicit and a keeper cannot hire against an offer that is already
// dead: a cue that hides while the tool would happily mint.
func TestLaborOfferLivePending_ExpiredPendingRowDoesNotSuppress(t *testing.T) {
	published := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	snap := &sim.Snapshot{
		PublishedAt: published,
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			// Minted 4 minutes ago with the 3-minute TTL: dead, but unswept.
			1: {
				ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
				State:     sim.LaborStatePending,
				ExpiresAt: published.Add(-time.Minute),
			},
		},
	}
	if subjectHasPendingLaborOfferOut(snap, "ezekiel") {
		t.Error("an expired-but-unswept offer still suppresses the worker's solicit affordance; the substrate would mint past it")
	}
	if subjectHasLaborOfferToAnswer(snap, "josiah") {
		t.Error("an expired-but-unswept offer still suppresses the employer's affordances; there is nothing left to answer")
	}

	// A live one still suppresses both.
	snap.LaborLedger[1].ExpiresAt = published.Add(time.Minute)
	if !subjectHasPendingLaborOfferOut(snap, "ezekiel") {
		t.Error("a live pending offer must still suppress the worker's solicit affordance")
	}
	if !subjectHasLaborOfferToAnswer(snap, "josiah") {
		t.Error("a live pending offer must still name the employer as owing an answer")
	}

	// A clock-free fixture (the golden matrix) treats the row as live, matching
	// how the ledger reads a zero ExpiresAt.
	snap.PublishedAt = time.Time{}
	snap.LaborLedger[1].ExpiresAt = time.Time{}
	if !subjectHasPendingLaborOfferOut(snap, "ezekiel") {
		t.Error("a clock-free pending offer must read as live")
	}
}

// TestIsHireableWorker_ExpiredOfferDoesNotHideTheWorker is the same drift on the
// hiring side: a worker holding a dead-but-unswept offer must not vanish from the
// keeper's offer_work cue, because sim.OfferWork would take him.
func TestIsHireableWorker_ExpiredOfferDoesNotHideTheWorker(t *testing.T) {
	published := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	prudence := &sim.ActorSnapshot{
		DisplayName:   "Prudence Ward",
		Acquaintances: map[string]sim.Acquaintance{"Lewis Walker": {}},
	}
	lewis := &sim.ActorSnapshot{
		DisplayName:    "Lewis Walker",
		AttributeSlugs: []string{sim.AttrWorker},
	}
	snap := &sim.Snapshot{
		PublishedAt: published,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"prudence": prudence, "lewis": lewis},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID: 1, WorkerID: "lewis", EmployerID: "hannah",
				State:     sim.LaborStatePending,
				ExpiresAt: published.Add(-time.Minute), // dead, unswept
			},
		},
	}
	if !isHireableWorker(snap, "prudence", prudence, "lewis") {
		t.Error("Lewis is hidden from the offer_work cue by a dead offer the substrate would mint straight past")
	}

	// A live offer to someone else does occupy him — one answer at a time.
	snap.LaborLedger[1].ExpiresAt = published.Add(time.Minute)
	if isHireableWorker(snap, "prudence", prudence, "lewis") {
		t.Error("Lewis owes Hannah an answer; he must not be offered a second job")
	}

	// So does a job already under way.
	snap.LaborLedger[1].State = sim.LaborStateWorking
	if isHireableWorker(snap, "prudence", prudence, "lewis") {
		t.Error("Lewis is mid-job; he must not be offered another")
	}
}

// TestSubjectHasPendingLaborOfferOut_EmployerInitiated is the LLM-346 direction:
// the employer minted the offer, so the roles of the two predicates swap. Without
// this the worker would be told to go seek work while an offer of work stood in
// front of him, which is the Lewis Walker case the ticket exists to fix.
func TestSubjectHasPendingLaborOfferOut_EmployerInitiated(t *testing.T) {
	snap := &sim.Snapshot{
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "lewis", EmployerID: "prudence", InitiatedBy: "prudence", State: sim.LaborStatePending},
		},
	}
	if !subjectHasPendingLaborOfferOut(snap, "prudence") {
		t.Error("subjectHasPendingLaborOfferOut(prudence) = false, want true (she offered the work)")
	}
	if subjectHasPendingLaborOfferOut(snap, "lewis") {
		t.Error("subjectHasPendingLaborOfferOut(lewis) = true, want false (he did not mint it)")
	}
	if !subjectHasLaborOfferToAnswer(snap, "lewis") {
		t.Error("subjectHasLaborOfferToAnswer(lewis) = false, want true (the offer awaits his answer)")
	}
	if subjectHasLaborOfferToAnswer(snap, "prudence") {
		t.Error("subjectHasLaborOfferToAnswer(prudence) = true, want false (she is waiting on him)")
	}
}

// TestRender_PendingLaborOfferOut — the worker-side awaiting-acceptance self-state
// anchor (LLM-164): a worker who has solicited and is waiting sees a line naming
// the employer and terms and telling them to sit tight, so they don't flail into
// an unrelated tool under the quiet backstop / "choose one action" pressure.
func TestRender_PendingLaborOfferOut(t *testing.T) {
	p := Payload{
		ActorID:              "anne",
		Actor:                ActorView{State: sim.StateIdle},
		PendingLaborOfferOut: &PendingLaborOfferOutView{Employer: "john", Reward: 5, DurationMin: 30},
		WarrantActorNames:    map[sim.ActorID]string{"john": "John Ellis"},
		Baseline:             BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))

	if !strings.Contains(out, "offered to work for John Ellis") {
		t.Errorf("pending-offer self-state line missing employer\n%s", out)
	}
	if !strings.Contains(out, "5 coins") || !strings.Contains(out, "30 minutes") {
		t.Errorf("pending-offer self-state line missing the offered terms\n%s", out)
	}
	if !strings.Contains(out, "wait for their answer") {
		t.Errorf("pending-offer self-state line missing the wait nudge\n%s", out)
	}

	// Absent when there is no outgoing offer.
	p.PendingLaborOfferOut = nil
	if out := combinedPrompt(Render(p, DefaultRenderConfig())); strings.Contains(out, "offered to work for") {
		t.Errorf("pending-offer line leaked with no outgoing offer\n%s", out)
	}
}

// TestBuildPendingLaborOfferOut — the Build-side scan projects the worker's OWN
// pending offer into the awaiting-acceptance view, and ignores Working offers,
// the employer side, and other workers' offers.
func TestBuildPendingLaborOfferOut(t *testing.T) {
	until := time.Date(2026, 6, 26, 12, 30, 0, 0, time.UTC)
	snap := &sim.Snapshot{
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "anne", EmployerID: "john", Reward: 5, DurationMin: 30, State: sim.LaborStatePending},
			2: {ID: 2, WorkerID: "mary", EmployerID: "john", Reward: 4, DurationMin: 15, State: sim.LaborStatePending},
			3: {ID: 3, WorkerID: "ezekiel", EmployerID: "josiah", Reward: 6, DurationMin: 20, State: sim.LaborStateWorking, WorkingUntil: &until},
		},
	}

	// Anne (worker) has a pending offer out → the view carries employer + terms.
	v := buildPendingLaborOfferOut(snap, "anne")
	if v == nil || v.Employer != "john" || v.Reward != 5 || v.DurationMin != 30 {
		t.Fatalf("buildPendingLaborOfferOut(anne) = %+v, want employer john, 5 coins, 30 min", v)
	}
	// John is the EMPLOYER on Anne's offer, not the worker → no outgoing offer.
	if got := buildPendingLaborOfferOut(snap, "john"); got != nil {
		t.Errorf("buildPendingLaborOfferOut(john) = %+v, want nil (employer, not worker)", got)
	}
	// Ezekiel's offer is Working, not Pending → not an awaiting-acceptance offer.
	if got := buildPendingLaborOfferOut(snap, "ezekiel"); got != nil {
		t.Errorf("buildPendingLaborOfferOut(ezekiel) = %+v, want nil (working, not pending)", got)
	}
}

// TestBuild_PendingLaborOfferOut_ResolvesEmployerWithoutWarrant — the LLM-164
// name-resolution regression, end-to-end through Build+Render: a waiting worker on
// a tick with NO warrant referencing the employer (the idle/quiet-backstop wake
// this anchor exists for, whose only warrant triggers on the worker itself) still
// renders the employer's label off the standing PendingLaborOfferOut view rather
// than falling back to "someone". Mirrors TestBuild_StandingOfferSurvivesConsumedWarrant
// for the pay-offer side. The plain renderPendingLaborOfferOut unit test above hard-
// codes WarrantActorNames, so it cannot catch a missing build-side wire.
func TestBuild_PendingLaborOfferOut_ResolvesEmployerWithoutWarrant(t *testing.T) {
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"anne": {DisplayName: "Anne Walker", Role: "vendor", Kind: sim.KindNPCStateful,
				AttributeSlugs: []string{"worker"}, Needs: map[sim.NeedKey]int{}},
			"john": {DisplayName: "John Ellis", Role: "tavernkeeper", Kind: sim.KindNPCStateful, Needs: map[sim.NeedKey]int{}},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "anne", EmployerID: "john", Reward: 5, DurationMin: 30, State: sim.LaborStatePending},
		},
		Scenes:     map[sim.SceneID]*sim.Scene{},
		Huddles:    map[sim.HuddleID]*sim.Huddle{},
		Structures: map[sim.StructureID]*sim.Structure{},
	}
	p := Build(snap, "anne", nil) // empty warrant batch — the idle-backstop tick

	if p.PendingLaborOfferOut == nil || p.PendingLaborOfferOut.Employer != "john" {
		t.Fatalf("PendingLaborOfferOut = %+v, want the standing offer to john", p.PendingLaborOfferOut)
	}
	if got := p.WarrantActorNames["john"]; got == "" {
		t.Fatalf("employer name unresolved in WarrantActorNames — render will fall back to \"someone\"")
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	// Anne doesn't know John → role-gated label, NOT the raw "someone" fallback.
	if !strings.Contains(out, "offered to work for the tavernkeeper") {
		t.Errorf("pending-offer line missing the role-resolved employer\n%s", out)
	}
	if strings.Contains(out, "offered to work for someone") {
		t.Errorf("employer degraded to \"someone\" — build-side name wire missing\n%s", out)
	}
}
