package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// offer_work_commands_test.go — LLM-346 coverage of sim.OfferWork, the
// employer-side mint, and of the responder rules AcceptWork / DeclineWork grew
// to support it. Reuses buildLaborWorld from labor_commands_test.go.
//
// The gates worth pinning are the ones whose absence produces a WRONG VILLAGE
// rather than a Go error: hiring a villager who takes no work, hiring your own
// household, hiring a hand who is already on a job, and — the one that reaches
// furthest — letting the wrong party answer.

const (
	offerHuddle = sim.HuddleID("h-offer")
	offerScene  = sim.SceneID("s-offer")
)

// hireFixture is the standard two-actor room: Prudence keeps the apothecary and
// can pay; Lewis takes work for pay. Neither shares a household or a workplace.
func hireFixture(t *testing.T) (*sim.World, func()) {
	t.Helper()
	return buildLaborWorld(t, offerHuddle, offerScene, []laborActor{
		{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle, workStruct: "apothecary", insideStruct: "apothecary"},
		{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true, insideStruct: "apothecary"},
	})
}

func mustOffer(t *testing.T, w *sim.World, employer sim.ActorID, worker string, reward, minutes int) sim.LaborOfferResult {
	t.Helper()
	res, err := w.Send(sim.OfferWork(employer, worker, reward, nil, minutes, time.Now().UTC()))
	if err != nil {
		t.Fatalf("OfferWork(%s → %s): %v", employer, worker, err)
	}
	placed, ok := res.(sim.LaborOfferResult)
	if !ok {
		t.Fatalf("OfferWork result = %T, want sim.LaborOfferResult", res)
	}
	return placed
}

// TestOfferWork_MintsEmployerInitiatedPendingOffer is the happy path: the offer
// stands, it is the employer's, and the worker is the one who owes an answer.
func TestOfferWork_MintsEmployerInitiatedPendingOffer(t *testing.T) {
	w, stop := hireFixture(t)
	defer stop()

	placed := mustOffer(t, w, "prudence", "Lewis Walker", 4, 240)
	if placed.State != sim.LaborStatePending {
		t.Errorf("state = %q, want pending", placed.State)
	}
	if placed.WorkerName != "Lewis Walker" {
		t.Errorf("WorkerName = %q, want Lewis Walker", placed.WorkerName)
	}

	snap := w.Published()
	o := snap.LaborLedger[placed.ID]
	if o == nil {
		t.Fatal("offer absent from the ledger")
	}
	if !o.EmployerInitiated() {
		t.Errorf("EmployerInitiated() = false; InitiatedBy = %q", o.InitiatedBy)
	}
	if o.Initiator() != "prudence" || o.Responder() != "lewis" {
		t.Errorf("Initiator/Responder = %q/%q, want prudence/lewis", o.Initiator(), o.Responder())
	}
}

// TestOfferWork_Gates walks the refusals. Each names a village situation, not a
// type error: the message the model reads has to tell it what to do instead.
func TestOfferWork_Gates(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name      string
		actors    []laborActor
		employer  sim.ActorID
		worker    string
		reward    int
		minutes   int
		wantErrIn string
	}{
		{
			name: "target takes no work for pay",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle},
				{id: "josiah", displayName: "Josiah Thorne", coins: 5, huddleID: offerHuddle},
			},
			employer: "prudence", worker: "Josiah Thorne", reward: 4, minutes: 240,
			wantErrIn: "not taken on as a worker",
		},
		{
			name: "cannot hire your own household",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle, homeStruct: "walker-house"},
				{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true, homeStruct: "walker-house"},
			},
			employer: "prudence", worker: "Lewis Walker", reward: 4, minutes: 240,
			wantErrIn: "you live with",
		},
		{
			name: "cannot hire your own shop's crew",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle, workStruct: "apothecary"},
				{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true, workStruct: "apothecary"},
			},
			employer: "prudence", worker: "Lewis Walker", reward: 4, minutes: 240,
			wantErrIn: "keep the same workplace",
		},
		{
			name: "cannot offer a wage you do not hold",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 3, huddleID: offerHuddle},
				{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true},
			},
			employer: "prudence", worker: "Lewis Walker", reward: 40, minutes: 240,
			wantErrIn: "you have only 3 coins",
		},
		{
			// Naming yourself never reaches the not-self gate: the huddle-peer
			// resolver excludes the caller, so the refusal comes from resolution.
			// The gate remains as a backstop for non-handler callers that pass an
			// already-resolved id (the same posture SolicitWork's not-self check has).
			name: "cannot hire yourself",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle, worker: true},
				{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true},
			},
			employer: "prudence", worker: "Prudence Ward", reward: 4, minutes: 240,
			wantErrIn: `no one named "Prudence Ward" in this conversation`,
		},
		{
			name: "nobody here by that name",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle},
				{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true},
			},
			employer: "prudence", worker: "Goodwife Nurse", reward: 4, minutes: 240,
			wantErrIn: `no one named "Goodwife Nurse" in this conversation`,
		},
		{
			name: "the pay must be worth something",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle},
				{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true},
			},
			employer: "prudence", worker: "Lewis Walker", reward: 0, minutes: 240,
			wantErrIn: "the pay must be worth something",
		},
		{
			name: "a walking keeper offers when she arrives",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle, moveInFlight: true},
				{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true},
			},
			employer: "prudence", worker: "Lewis Walker", reward: 4, minutes: 240,
			wantErrIn: "you are walking",
		},
		{
			name: "not in a conversation",
			actors: []laborActor{
				{id: "prudence", displayName: "Prudence Ward", coins: 40},
				{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true},
			},
			employer: "prudence", worker: "Lewis Walker", reward: 4, minutes: 240,
			wantErrIn: "you're not in a conversation",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildLaborWorld(t, offerHuddle, offerScene, tc.actors)
			defer stop()
			_, err := w.Send(sim.OfferWork(tc.employer, tc.worker, tc.reward, nil, tc.minutes, now))
			if err == nil {
				t.Fatalf("OfferWork succeeded; want refusal containing %q", tc.wantErrIn)
			}
			if !strings.Contains(err.Error(), tc.wantErrIn) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErrIn)
			}
			if n := len(w.Published().LaborLedger); n != 0 {
				t.Errorf("a refused offer minted %d ledger entries; want none", n)
			}
		})
	}
}

// TestOfferWork_OneOfferOutPerEmployer mirrors SolicitWork's duplicate gate: a
// keeper hires one body at a time and waits for the answer, so a weak model
// cannot storm the room with offers.
func TestOfferWork_OneOfferOutPerEmployer(t *testing.T) {
	w, stop := buildLaborWorld(t, offerHuddle, offerScene, []laborActor{
		{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle},
		{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true},
		{id: "patience", displayName: "Patience Walker", coins: 2, huddleID: offerHuddle, worker: true},
	})
	defer stop()

	first := mustOffer(t, w, "prudence", "Lewis Walker", 4, 240)
	_, err := w.Send(sim.OfferWork("prudence", "Patience Walker", 4, nil, 240, time.Now().UTC()))
	if err == nil {
		t.Fatal("a second simultaneous offer of work succeeded; want the duplicate gate to refuse it")
	}
	if !strings.Contains(err.Error(), "Lewis Walker") {
		t.Errorf("the refusal should name who is already holding an offer; got %q", err.Error())
	}
	if n := len(w.Published().LaborLedger); n != 1 {
		t.Errorf("ledger holds %d offers, want 1 (offer %d)", n, first.ID)
	}
}

// TestOfferWork_WorkerAlreadyHoldingAnOfferIsNotOfferedAnother: one live pending
// offer per worker, in either direction. Two would leave the late acceptor hitting
// failed_unavailable, and a worker can only answer one thing at a time.
func TestOfferWork_WorkerAlreadyHoldingAnOfferIsNotOfferedAnother(t *testing.T) {
	w, stop := buildLaborWorld(t, offerHuddle, offerScene, []laborActor{
		{id: "prudence", displayName: "Prudence Ward", coins: 40, huddleID: offerHuddle},
		{id: "hannah", displayName: "Hannah Boggs", coins: 40, huddleID: offerHuddle},
		{id: "lewis", displayName: "Lewis Walker", coins: 26, huddleID: offerHuddle, worker: true},
	})
	defer stop()

	mustOffer(t, w, "prudence", "Lewis Walker", 4, 240)
	_, err := w.Send(sim.OfferWork("hannah", "Lewis Walker", 6, nil, 240, time.Now().UTC()))
	if err == nil {
		t.Fatal("a second employer's offer to the same worker succeeded; want it refused")
	}
	if !strings.Contains(err.Error(), "already has a work offer awaiting an answer") {
		t.Errorf("error = %q, want the already-holding-an-offer refusal", err.Error())
	}
}

// TestOfferWork_SolicitIsRedirectedToTheOfferOnTheTable: a worker reaching for
// solicit_work while an employer's offer stands is using the wrong verb. Naming
// the offer and the answer tools is what turns the loop into a hire.
func TestOfferWork_SolicitIsRedirectedToTheOfferOnTheTable(t *testing.T) {
	w, stop := hireFixture(t)
	defer stop()

	placed := mustOffer(t, w, "prudence", "Lewis Walker", 4, 240)
	_, err := w.Send(sim.SolicitWork("lewis", "Prudence Ward", 5, nil, 240, time.Now().UTC()))
	if err == nil {
		t.Fatal("solicit_work succeeded while an offer of work awaited an answer")
	}
	for _, want := range []string{"Prudence Ward has already offered you work", "accept_work", "decline_work"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q should contain %q — it must point at the offer already on the table", err.Error(), want)
		}
	}
	if o := w.Published().LaborLedger[placed.ID]; o == nil || o.State != sim.LaborStatePending {
		t.Error("the standing offer should be untouched by the refused solicit")
	}
}

// TestOfferWork_OnlyTheResponderMayAnswer is the auth gate in both directions. An
// employer who could accept her own offer would hire people who never agreed.
func TestOfferWork_OnlyTheResponderMayAnswer(t *testing.T) {
	now := time.Now().UTC()

	t.Run("employer cannot accept her own offer of work", func(t *testing.T) {
		w, stop := hireFixture(t)
		defer stop()
		placed := mustOffer(t, w, "prudence", "Lewis Walker", 4, 240)
		if _, err := w.Send(sim.AcceptWork("prudence", placed.ID, now)); err == nil {
			t.Fatal("the employer accepted her own offer; only Lewis may answer it")
		}
		if _, err := w.Send(sim.DeclineWork("prudence", placed.ID, now)); err == nil {
			t.Fatal("the employer declined her own offer; only Lewis may answer it")
		}
	})

	t.Run("worker cannot accept his own solicitation", func(t *testing.T) {
		w, stop := hireFixture(t)
		defer stop()
		res, err := w.Send(sim.SolicitWork("lewis", "Prudence Ward", 4, nil, 240, now))
		if err != nil {
			t.Fatalf("SolicitWork: %v", err)
		}
		id := res.(sim.LaborSolicitResult).ID
		if _, err := w.Send(sim.AcceptWork("lewis", id, now)); err == nil {
			t.Fatal("the worker accepted his own solicitation; only Prudence may answer it")
		}
	})
}

// TestOfferWork_WorkerAcceptStartsTheJobInPlace: both stand at the employer's own
// post, so the work window starts immediately — no relocation leg — and the
// result is written from the ACCEPTOR's side (he took a job on; he hired no one).
func TestOfferWork_WorkerAcceptStartsTheJobInPlace(t *testing.T) {
	w, stop := hireFixture(t)
	defer stop()

	placed := mustOffer(t, w, "prudence", "Lewis Walker", 4, 240)
	res, err := w.Send(sim.AcceptWork("lewis", placed.ID, time.Now().UTC()))
	if err != nil {
		t.Fatalf("AcceptWork by the worker: %v", err)
	}
	accepted := res.(sim.LaborAcceptResult)
	if accepted.State != sim.LaborStateWorking {
		t.Errorf("state = %q, want working (both are at her post)", accepted.State)
	}
	if !accepted.AcceptorIsWorker {
		t.Error("AcceptorIsWorker = false; Lewis accepted, so the feedback must address him as the one who took the job")
	}
	if accepted.WorkerName != "Lewis Walker" || accepted.EmployerName != "Prudence Ward" {
		t.Errorf("names = %q/%q, want Lewis Walker/Prudence Ward", accepted.WorkerName, accepted.EmployerName)
	}

	snap := w.Published()
	if got := snap.Actors["lewis"].State; got != sim.StateLaboring {
		t.Errorf("Lewis state = %q, want laboring", got)
	}
	// No coins move until the work is done.
	if got := snap.Actors["prudence"].Coins; got != 40 {
		t.Errorf("employer coins = %d, want 40 — the wage settles at completion, not at accept", got)
	}
}

// TestOfferWork_DeclinedOfferRemembersTheRightRefusal: the decline is the
// worker's, so the relationship facts must say so on both sides. Writing the
// solicit-shaped pair would leave Prudence remembering that she turned Lewis away
// when in fact he turned her down.
func TestOfferWork_DeclinedOfferRemembersTheRightRefusal(t *testing.T) {
	w, stop := hireFixture(t)
	defer stop()

	placed := mustOffer(t, w, "prudence", "Lewis Walker", 4, 240)
	if _, err := w.Send(sim.DeclineWork("lewis", placed.ID, time.Now().UTC())); err != nil {
		t.Fatalf("DeclineWork by the worker: %v", err)
	}

	snap := w.Published()
	lewisFacts := factTexts(snap.Actors["lewis"], "prudence")
	prudenceFacts := factTexts(snap.Actors["prudence"], "lewis")
	if !containsSubstring(lewisFacts, "I declined Prudence Ward's offer of work") {
		t.Errorf("Lewis's facts = %v; he declined HER offer and should remember doing so", lewisFacts)
	}
	if !containsSubstring(prudenceFacts, "Lewis Walker declined my offer of work") {
		t.Errorf("Prudence's facts = %v; her offer was declined, she did not decline his", prudenceFacts)
	}
}

func factTexts(a *sim.ActorSnapshot, peer sim.ActorID) []string {
	if a == nil {
		return nil
	}
	rel, ok := a.Relationships[peer]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rel.SalientFacts))
	for _, f := range rel.SalientFacts {
		out = append(out, f.Text)
	}
	return out
}

func containsSubstring(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}
