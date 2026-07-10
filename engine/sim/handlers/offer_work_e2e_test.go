package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// offer_work_e2e_test.go — LLM-346. Labor was worker-initiated only: a keeper
// had no tool with which to ask someone to work for her. Live on 2026-07-10,
// Lewis Walker offered to help at the PW Apothecary and Prudence Ward accepted
// in as many words — "Would you be so kind as to lend a hand with the shelves
// and the herbs?" — and nothing happened. Neither could act on the agreement
// they had just reached. Lewis stood outside her door for 45 minutes.
//
// These tests drive the WHOLE path the live failure ran through, across the
// handler → world → perception → tool-gate boundaries. A unit test on any one
// layer passes while the village stays broken: the substrate can mint an offer
// no perception renders, perception can render an offer no gate arms, and a gate
// can arm a tool no substrate accepts. Only the round trip proves a keeper can
// hire and a worker can take the job.
//
//  1. TestOfferWork_KeeperHiresWorkerEndToEnd — Prudence asks, Lewis is offered
//     the answer tools, Lewis accepts, Lewis is at work.
//  2. TestOfferWork_RejectedOfferSpeaksNothing — the LLM-343 ordering invariant
//     for the new tool: an offer the world refuses leaves the keeper silent.
//  3. TestOfferWork_WorkerDeclineDoesNotTeachTheWorkerToAvoidTheShop — the
//     decline-direction trap.
//
// Internal (package handlers) because the advertising assertion has to run
// through gateTools, which is where the live prompt actually went wrong.

// buildApothecaryWorld stands up the live fixture: Prudence Ward keeping the PW
// Apothecary, Lewis Walker (a `worker`) co-located inside it with no huddle yet,
// so the tool's own huddle bootstrap has to form the conversation. Lewis carries
// 26 coins as he did live — above the seek-work comfort ceiling, which is exactly
// what silenced his solicit affordance. His ANSWER must not depend on it.
func buildApothecaryWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"apothecary": {ID: "apothecary", DisplayName: "PW Apothecary"},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg-asset": {ID: "bldg-asset", Category: "structure"},
	})
	// Shared-Identity Bridge (ZBBS-WORK-342): the structure needs a backing
	// VillageObject so the structure-bound scene's origin tile resolves.
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"apothecary": {ID: "apothecary", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 160, Y: 160}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"prudence": {
			ID: "prudence", DisplayName: "Prudence Ward", Kind: sim.KindNPCStateful,
			State:             sim.StateIdle,
			Coins:             40,
			InsideStructureID: "apothecary",
			WorkStructureID:   "apothecary",
			Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
		},
		"lewis": {
			ID: "lewis", DisplayName: "Lewis Walker", Kind: sim.KindNPCShared,
			State:             sim.StateIdle,
			Coins:             26,
			InsideStructureID: "apothecary",
			Attributes:        map[string][]byte{sim.AttrWorker: nil}, // presence-only marker
			Acquaintances:     map[string]sim.Acquaintance{"Prudence Ward": {}},
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	return w, func() { cancel(); <-done }
}

// laborToolRegistry registers the tools whose advertising these tests inspect.
// The gate is what the live prompt got wrong — Lewis's deliberation advertised no
// work tool at all — so the assertion runs through gateTools rather than merely
// checking that the ledger holds a row.
func laborToolRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := RegisterLaborFamily(r); err != nil {
		t.Fatalf("RegisterLaborFamily: %v", err)
	}
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("RegisterTerminal: %v", err)
	}
	return r
}

// advertisedTo renders the actor's real perception off the live world and returns
// the tool names gateTools would put in front of the model.
func advertisedTo(t *testing.T, r *Registry, w *sim.World, actorID sim.ActorID) map[string]bool {
	t.Helper()
	snap := w.Published()
	payload := perception.Build(snap, actorID, nil)
	names := map[string]bool{}
	for _, spec := range gateTools(r, payload, snap) {
		names[spec.Name] = true
	}
	return names
}

// TestOfferWork_KeeperHiresWorkerEndToEnd is the repro. Prudence asks Lewis to
// lend a hand — one tool call, carrying her words — and the promise becomes
// something the world can keep: the offer stands in the ledger, Lewis's next
// perception hands him accept_work, he takes it, and he is at the work.
func TestOfferWork_KeeperHiresWorkerEndToEnd(t *testing.T) {
	w, stop := buildApothecaryWorld(t)
	defer stop()
	r := laborToolRegistry(t)

	const request = "Would you be so kind as to lend a hand with the shelves and the herbs?"
	cmd, err := HandleOfferWork(HandlerInput{
		ActorID: "prudence", AttemptID: "tk-1",
		Args: OfferWorkArgs{
			Worker:          "Lewis Walker",
			Reward:          4,
			DurationMinutes: 240,
			Say:             request,
		},
	})
	if err != nil {
		t.Fatalf("HandleOfferWork: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("offer_work rejected: %v", err)
	}
	placed, ok := res.(sim.LaborOfferResult)
	if !ok {
		t.Fatalf("result = %T, want sim.LaborOfferResult", res)
	}
	if placed.ID == 0 || placed.State != sim.LaborStatePending {
		t.Fatalf("offer not placed: id=%d state=%q", placed.ID, placed.State)
	}
	if !placed.Announced {
		t.Errorf("Announced = false (SayRefused=%q); her words never reached the room", placed.SayRefused)
	}

	// The request must actually be IN the world, not merely reported by the
	// result — the failure mode LLM-343 pinned for sell.
	snap := w.Published()
	huddleID := snap.Actors["prudence"].CurrentHuddleID
	if huddleID == "" {
		t.Fatal("prudence not huddled after offer_work — the huddle bootstrap did not fire")
	}
	var heard bool
	if h := snap.Huddles[huddleID]; h != nil {
		for _, u := range h.RecentUtterances {
			if u.SpeakerID == "prudence" && strings.Contains(u.Text, "lend a hand with the shelves") {
				heard = true
			}
		}
	}
	if !heard {
		t.Error("prudence's request never entered the conversation")
	}

	// The offer is the employer's, and it is Lewis who owes the answer.
	offer := snap.LaborLedger[placed.ID]
	if offer == nil {
		t.Fatalf("offer %d absent from the published ledger", placed.ID)
	}
	if !offer.EmployerInitiated() {
		t.Errorf("offer.InitiatedBy = %q, want prudence — an employer-initiated offer", offer.InitiatedBy)
	}
	if got := offer.Responder(); got != "lewis" {
		t.Errorf("offer.Responder() = %q, want lewis", got)
	}

	// THE assertion the live bug would have failed. Lewis's rendered deliberation
	// advertised no work-taking tool; his perception must now carry both answers.
	lewisTools := advertisedTo(t, r, w, "lewis")
	for _, tool := range []string{"accept_work", "decline_work"} {
		if !lewisTools[tool] {
			t.Errorf("%s not advertised to Lewis — he holds an unanswered offer and cannot answer it", tool)
		}
	}
	// And he is not simultaneously told to go hunting for work he already has.
	if lewisTools["solicit_work"] {
		t.Error("solicit_work advertised to Lewis while an offer of work awaits his answer")
	}

	// He takes the job. Prudence is at her own post and Lewis is inside it with
	// her, so the work window starts in place — no relocation leg.
	accept, err := HandleAcceptWork(HandlerInput{
		ActorID: "lewis", AttemptID: "tk-2",
		Args: AcceptWorkArgs{LaborID: LenientID(placed.ID)},
	})
	if err != nil {
		t.Fatalf("HandleAcceptWork: %v", err)
	}
	ares, err := w.Send(accept)
	if err != nil {
		t.Fatalf("lewis could not accept the offer made to him: %v", err)
	}
	accepted, ok := ares.(sim.LaborAcceptResult)
	if !ok {
		t.Fatalf("accept result = %T, want sim.LaborAcceptResult", ares)
	}
	if accepted.State != sim.LaborStateWorking {
		t.Errorf("state after accept = %q, want working (both stand at her post)", accepted.State)
	}
	if !accepted.AcceptorIsWorker {
		t.Error("AcceptorIsWorker = false; the worker accepted, so the tool feedback would tell Lewis he hired himself")
	}
	if accepted.EmployerName != "Prudence Ward" {
		t.Errorf("EmployerName = %q, want Prudence Ward", accepted.EmployerName)
	}

	snap = w.Published()
	if got := snap.Actors["lewis"].State; got != sim.StateLaboring {
		t.Errorf("Lewis state = %q, want laboring — he agreed to the work and should be doing it", got)
	}
	if o := snap.LaborLedger[placed.ID]; o == nil || o.State != sim.LaborStateWorking {
		t.Errorf("offer did not reach working; got %+v", o)
	}
}

// TestOfferWork_RejectedOfferSpeaksNothing pins the ordering invariant for the
// new tool (LLM-343's, restated): the offer is minted BEFORE the words go out, so
// an offer the world refuses leaves the keeper silent. Here she names a wage she
// cannot cover — asking aloud for help she could never pay for would be the
// original bug wearing different clothes.
func TestOfferWork_RejectedOfferSpeaksNothing(t *testing.T) {
	w, stop := buildApothecaryWorld(t)
	defer stop()

	cmd, err := HandleOfferWork(HandlerInput{
		ActorID: "prudence", AttemptID: "tk-1",
		Args: OfferWorkArgs{
			Worker:          "Lewis Walker",
			Reward:          400, // she holds 40
			DurationMinutes: 240,
			Say:             "Four hundred coins for an afternoon's work, Lewis.",
		},
	})
	if err != nil {
		t.Fatalf("HandleOfferWork (static validation): %v", err)
	}
	if _, err := w.Send(cmd); err == nil {
		t.Fatal("offer_work for a wage she cannot cover succeeded; expected rejection")
	}

	snap := w.Published()
	if len(snap.LaborLedger) != 0 {
		t.Errorf("a refused offer left %d ledger entries; nothing should have been minted", len(snap.LaborLedger))
	}
	if huddleID := snap.Actors["prudence"].CurrentHuddleID; huddleID != "" {
		if h := snap.Huddles[huddleID]; h != nil {
			for _, u := range h.RecentUtterances {
				if u.SpeakerID == "prudence" {
					t.Errorf("prudence spoke %q against an offer that was never made", u.Text)
				}
			}
		}
	}
}

// TestOfferWork_WorkerDeclineDoesNotTeachTheWorkerToAvoidTheShop guards the
// decline-direction trap. A decline is the RESPONDER's refusal. On a solicited
// offer that is the employer turning the worker away, and the worker earns a
// 12-hour "don't come back" memory of that shop (ObservedDeclinedWork, LLM-198).
// On an OFFERED job it is the worker saying no — and stamping the same memory
// would drop the shop that just tried to hire him from his own seek-work
// directory, punishing him for his own refusal.
func TestOfferWork_WorkerDeclineDoesNotTeachTheWorkerToAvoidTheShop(t *testing.T) {
	w, stop := buildApothecaryWorld(t)
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RegisterDeclinedWorkSubscriber(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("registering the declined-work subscriber: %v", err)
	}

	cmd, err := HandleOfferWork(HandlerInput{
		ActorID: "prudence", AttemptID: "tk-1",
		Args: OfferWorkArgs{Worker: "Lewis Walker", Reward: 4, DurationMinutes: 240},
	})
	if err != nil {
		t.Fatalf("HandleOfferWork: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("offer_work rejected: %v", err)
	}
	placed := res.(sim.LaborOfferResult)
	now := time.Now().UTC()

	// The employer may not answer her own offer.
	if _, err := w.Send(sim.DeclineWork("prudence", placed.ID, now)); err == nil {
		t.Error("prudence declined her own offer of work; only the responder may answer")
	}
	if _, err := w.Send(sim.DeclineWork("lewis", placed.ID, now)); err != nil {
		t.Fatalf("lewis could not decline the offer made to him: %v", err)
	}

	snap := w.Published()
	if snap.Actors["lewis"].Observed.Active(
		sim.ObservedStateKey{StructureID: "apothecary", Condition: sim.ObservedDeclinedWork},
		snap.PublishedAt,
	) {
		t.Error("Lewis remembers the PW Apothecary as having turned him down — but HE declined HER offer; that memory would drop the shop from his own seek-work directory for 12 hours")
	}
}
