package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// response_say_test.go — LLM-350. Every transactional RESPONSE verb is
// terminal-on-success, and so is speak (LLM-321). The decision cues told the NPC
// to call both: "Respond first with accept_pay… Then also use speak." Whichever
// landed first ended the tick and the harness skipped the rest of the batch, so
// obeying the cue in the order written settled the sale in silence, and obeying
// it literally — speaking first — cost the sale outright, because the offer went
// unanswered and expired.
//
// The words are folded into each response tool's `say`, the shape LLM-343 gave
// sell and LLM-346 gave offer_work. These tests pin the properties that make it
// correct across the handler → world boundary, where a handler-only unit test
// passes while the village stays mute:
//
//  1. accept_work by a RELOCATING worker both hires and is heard. This is the
//     case that dictates the whole design: sim.AcceptWork sends the worker
//     walking to the employer's post and drops them from the huddle, and SpeakTo
//     refuses a walker AND refuses a speaker with no audience. A handler-level
//     "accept, then speak" composite — the shape sell and offer_work use — is
//     therefore silent here, always. Verified: reverting AcceptWorkSaying's speak
//     site to after the relocation fails this test with the live symptom.
//  2. accept_pay settles AND is heard, in one call.
//  3. A response that falls through to a failed terminal speaks nothing — the
//     LLM-343 ordering rule: no words against a deal that did not happen.
//  4. decline_pay's `reason` and counter_pay's `message` still decode, as
//     aliases for the spoken line.

// buildOffPostHireWorld stands Prudence Ward and Lewis Walker together in the
// tavern, far from Prudence's own apothecary. A hire struck here is exactly the
// LLM-229 relocate branch: the worker must walk to the employer's post, so the
// accept sets his MoveIntent and pulls him out of the huddle.
func buildOffPostHireWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"apothecary": {ID: "apothecary", DisplayName: "PW Apothecary"},
		"tavern":     {ID: "tavern", DisplayName: "The Tavern"},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg-asset": {ID: "bldg-asset", Category: "structure"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"apothecary": {ID: "apothecary", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 1600, Y: 1600}},
		"tavern":     {ID: "tavern", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 160, Y: 160}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"prudence": {
			ID: "prudence", DisplayName: "Prudence Ward", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, Coins: 40,
			WorkStructureID:   "apothecary",
			InsideStructureID: "tavern",
			Acquaintances:     map[string]sim.Acquaintance{"Lewis Walker": {}},
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
		},
		"lewis": {
			ID: "lewis", DisplayName: "Lewis Walker", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Coins: 26,
			InsideStructureID: "tavern",
			Attributes:        map[string][]byte{sim.AttrWorker: nil},
			Acquaintances:     map[string]sim.Acquaintance{"Prudence Ward": {}},
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w
}

// heardInHuddle reports whether speakerID's utterance containing fragment sits in
// the huddle ring the LISTENER can still see. Read off the listener because the
// speaker may have left the huddle by the time we look — which is precisely what
// a relocating accept does to the worker.
func heardInHuddle(w *sim.World, listener sim.ActorID, speaker sim.ActorID, fragment string) bool {
	actor, ok := w.Actors[listener]
	if !ok || actor == nil || actor.CurrentHuddleID == "" {
		return false
	}
	h := w.Huddles[actor.CurrentHuddleID]
	if h == nil {
		return false
	}
	for _, u := range h.RecentUtterances {
		if u.SpeakerID == speaker && strings.Contains(u.Text, fragment) {
			return true
		}
	}
	return false
}

// TestAcceptWorkSay_RelocatingWorkerIsHiredAndHeard is the repro. Lewis takes a
// job offered away from the shop: he must end the tick both hired (walking to the
// apothecary) and heard saying yes.
//
// This is the test that arbitrates the design. Move the speak in
// sim.AcceptWorkSaying to after sendWorkerToWorkplace and it fails with
// SayRefused = "you are walking — finish your move before speaking."
func TestAcceptWorkSay_RelocatingWorkerIsHiredAndHeard(t *testing.T) {
	w := buildOffPostHireWorld(t)
	now := time.Now().UTC()

	if _, err := sim.EnsureColocatedHuddle("prudence", now).Fn(w); err != nil {
		t.Fatalf("EnsureColocatedHuddle: %v", err)
	}
	offered, err := sim.OfferWork("prudence", "Lewis Walker", 4, nil, 240, now).Fn(w)
	if err != nil {
		t.Fatalf("OfferWork: %v", err)
	}
	offer := offered.(sim.LaborOfferResult)

	const line = "It would be my pleasure — lead on."
	cmd, err := HandleAcceptWork(HandlerInput{
		ActorID: "lewis", AttemptID: "tk-1", HasNewNews: true,
		Args: AcceptWorkArgs{LaborID: LenientID(offer.ID), Say: line},
	})
	if err != nil {
		t.Fatalf("HandleAcceptWork: %v", err)
	}
	res, err := cmd.Fn(w)
	if err != nil {
		t.Fatalf("accept_work with say rejected: %v", err)
	}
	accepted, ok := res.(sim.LaborAcceptResult)
	if !ok {
		t.Fatalf("result = %T, want sim.LaborAcceptResult", res)
	}

	// The hire must stand — this is the relocate branch, so he is en route.
	if accepted.State != sim.LaborStateEnRoute {
		t.Fatalf("State = %q, want en_route (the fixture is deliberately off-post)", accepted.State)
	}
	if !accepted.AcceptorIsWorker {
		t.Fatal("AcceptorIsWorker = false; the fixture has the WORKER answering an employer's offer")
	}
	// And he must have been heard. Announced is the tool's claim...
	if !accepted.Announced {
		t.Errorf("Announced = false, SayRefused = %q; Lewis took the job without a word — "+
			"the words must go out before sendWorkerToWorkplace sets him walking", accepted.SayRefused)
	}
	// ...the huddle ring is the world's. Prudence stays behind in it.
	if !heardInHuddle(w, "prudence", "lewis", "It would be my pleasure") {
		t.Error("Lewis's acceptance never entered the conversation Prudence can see")
	}
	// Post-conditions that make this the hard case: he really is walking, and
	// really has left the huddle. If either stops holding, the test has silently
	// stopped covering the relocate branch.
	lewis := w.Actors["lewis"]
	if lewis.MoveIntent == nil {
		t.Error("Lewis is not walking — fixture no longer exercises the relocate branch")
	}
	if lewis.CurrentHuddleID != "" {
		t.Error("Lewis is still huddled — fixture no longer exercises the relocate branch")
	}
}

// TestDeclineWorkSay_RefusalIsHeard covers the sibling that CAN use the
// handler-level composite: a decline moves no one, so the words go out after the
// Command commits.
func TestDeclineWorkSay_RefusalIsHeard(t *testing.T) {
	w := buildOffPostHireWorld(t)
	now := time.Now().UTC()

	if _, err := sim.EnsureColocatedHuddle("prudence", now).Fn(w); err != nil {
		t.Fatalf("EnsureColocatedHuddle: %v", err)
	}
	offered, err := sim.OfferWork("prudence", "Lewis Walker", 4, nil, 240, now).Fn(w)
	if err != nil {
		t.Fatalf("OfferWork: %v", err)
	}
	offer := offered.(sim.LaborOfferResult)

	cmd, err := HandleDeclineWork(HandlerInput{
		ActorID: "lewis", AttemptID: "tk-1", HasNewNews: true,
		Args: DeclineWorkArgs{LaborID: LenientID(offer.ID), Say: "Not today — my back is not what it was."},
	})
	if err != nil {
		t.Fatalf("HandleDeclineWork: %v", err)
	}
	res, err := cmd.Fn(w)
	if err != nil {
		t.Fatalf("decline_work with say rejected: %v", err)
	}
	declined, ok := res.(sim.LaborDeclineResult)
	if !ok {
		t.Fatalf("result = %T, want sim.LaborDeclineResult", res)
	}
	if declined.State != sim.LaborStateDeclined {
		t.Errorf("State = %q, want declined", declined.State)
	}
	if !declined.Announced {
		t.Errorf("Announced = false, SayRefused = %q; Lewis refused in silence", declined.SayRefused)
	}
	if !heardInHuddle(w, "prudence", "lewis", "my back is not what it was") {
		t.Error("Lewis's refusal never entered the conversation")
	}
}

// TestDeclineWorkSay_RefusedDeclineSpeaksNothing is the ordering invariant for
// the composite: an offer that is not the caller's to answer errors out, and the
// words must not have gone anywhere first.
func TestDeclineWorkSay_RefusedDeclineSpeaksNothing(t *testing.T) {
	w := buildOffPostHireWorld(t)
	now := time.Now().UTC()

	if _, err := sim.EnsureColocatedHuddle("prudence", now).Fn(w); err != nil {
		t.Fatalf("EnsureColocatedHuddle: %v", err)
	}
	offered, err := sim.OfferWork("prudence", "Lewis Walker", 4, nil, 240, now).Fn(w)
	if err != nil {
		t.Fatalf("OfferWork: %v", err)
	}
	offer := offered.(sim.LaborOfferResult)

	// Prudence made the offer; it is not hers to decline.
	cmd, err := HandleDeclineWork(HandlerInput{
		ActorID: "prudence", AttemptID: "tk-1", HasNewNews: true,
		Args: DeclineWorkArgs{LaborID: LenientID(offer.ID), Say: "On reflection, no."},
	})
	if err != nil {
		t.Fatalf("HandleDeclineWork build: %v", err)
	}
	if _, err := cmd.Fn(w); err == nil {
		t.Fatal("expected the initiator's own decline to be refused")
	}
	if heardInHuddle(w, "lewis", "prudence", "On reflection") {
		t.Error("the refused decline still spoke — words went out against an act that did not happen")
	}
}

// buildPayResponseWorld stands a buyer and a seller together in the tavern, the
// seller holding bread. Mirrors the live shape of an NPC-to-NPC counter sale.
func buildPayResponseWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "The Tavern"},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"bldg-asset": {ID: "bldg-asset", Category: "structure"}})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {ID: "tavern", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 160, Y: 160}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {
			ID: "alice", DisplayName: "Alice", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Coins: 50, InsideStructureID: "tavern",
			Acquaintances: map[string]sim.Acquaintance{"Bob": {}},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"bob": {
			ID: "bob", DisplayName: "Bob", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, InsideStructureID: "tavern",
			Inventory:     map[sim.ItemKind]int{"bread": 5},
			Acquaintances: map[string]sim.Acquaintance{"Alice": {}},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w
}

// placePendingBreadOffer has Alice offer `amount` coins for one loaf, returning the
// pending ledger id Bob must answer.
func placePendingBreadOffer(t *testing.T, w *sim.World, amount int, now time.Time) sim.LedgerID {
	t.Helper()
	if _, err := sim.EnsureColocatedHuddle("alice", now).Fn(w); err != nil {
		t.Fatalf("EnsureColocatedHuddle: %v", err)
	}
	res, err := sim.PayWithItem("alice", "Bob", "bread", 1, amount, false, nil, nil, 0, 0, "", now, 0).Fn(w)
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	placed, ok := res.(sim.PayWithItemResult)
	if !ok {
		t.Fatalf("PayWithItem result = %T", res)
	}
	if placed.State != sim.PayLedgerStatePending {
		t.Fatalf("offer state = %q, want pending", placed.State)
	}
	return placed.LedgerID
}

// TestAcceptPaySay_SettlesAndIsHeardInOneCall is the pay-side repro: one
// accept_pay call must leave both a settled sale and a heard utterance. Before
// LLM-350 the cue asked for an accept AND a speak; the speak was skipped as
// post_terminal and the transaction landed in silence.
func TestAcceptPaySay_SettlesAndIsHeardInOneCall(t *testing.T) {
	w := buildPayResponseWorld(t)
	now := time.Now().UTC()
	ledgerID := placePendingBreadOffer(t, w, 4, now)

	const line = "Four coins it is — here's your loaf."
	cmd, err := HandleAcceptPay(HandlerInput{
		ActorID: "bob", AttemptID: "tk-1", HasNewNews: true,
		Args: AcceptPayArgs{LedgerID: LenientID(ledgerID), Say: line},
	})
	if err != nil {
		t.Fatalf("HandleAcceptPay: %v", err)
	}
	res, err := cmd.Fn(w)
	if err != nil {
		t.Fatalf("accept_pay with say rejected: %v", err)
	}
	out, ok := res.(payResponseResult)
	if !ok {
		t.Fatalf("result = %T, want payResponseResult", res)
	}
	if out.State != sim.PayLedgerStateAccepted {
		t.Fatalf("State = %q, want accepted", out.State)
	}
	if !out.Announced {
		t.Errorf("Announced = false, SayRefused = %q; the sale settled in silence", out.SayRefused)
	}
	// The world's own record, not the tool's claim.
	if !heardInHuddle(w, "alice", "bob", "here's your loaf") {
		t.Error("Bob's line never entered the conversation Alice can hear")
	}
	// And the sale really settled.
	if got := w.Actors["bob"].Coins; got != 4 {
		t.Errorf("Bob's coins = %d, want 4 — the accept did not settle", got)
	}
}

// TestAcceptPaySay_FellThroughSettlementSpeaksNothing pins the LLM-343 ordering
// rule on the pay responses: an accept that flips to a FAILED terminal settled
// nothing, so the seller must not thank a buyer for a sale that never happened.
// Here Alice's purse is emptied between the offer and the accept.
func TestAcceptPaySay_FellThroughSettlementSpeaksNothing(t *testing.T) {
	w := buildPayResponseWorld(t)
	now := time.Now().UTC()
	ledgerID := placePendingBreadOffer(t, w, 4, now)
	w.Actors["alice"].Coins = 0 // she can no longer cover it

	cmd, err := HandleAcceptPay(HandlerInput{
		ActorID: "bob", AttemptID: "tk-1", HasNewNews: true,
		Args: AcceptPayArgs{LedgerID: LenientID(ledgerID), Say: "Four coins it is — here's your loaf."},
	})
	if err != nil {
		t.Fatalf("HandleAcceptPay: %v", err)
	}
	res, err := cmd.Fn(w)
	if err != nil {
		t.Fatalf("accept_pay: %v", err)
	}
	out, ok := res.(payResponseResult)
	if !ok {
		t.Fatalf("result = %T, want payResponseResult", res)
	}
	if out.State != sim.PayLedgerStateFailedInsufficientFunds {
		t.Fatalf("State = %q, want failed_insufficient_funds", out.State)
	}
	if out.Announced {
		t.Error("the seller spoke over a sale that fell through")
	}
	if heardInHuddle(w, "alice", "bob", "here's your loaf") {
		t.Error("words went out against a settlement that did not happen (LLM-343 ordering rule)")
	}
}

// TestDecodeDeclinePayArgs_ReasonAliasesToSay pins the LLM-350 fold: `reason` was
// never spoken and never reached the buyer, so it is now a decode-only alias for
// the spoken line. A model still reaching for the old name lands its words.
func TestDecodeDeclinePayArgs_ReasonAliasesToSay(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"say wins", `{"ledger_id":5,"say":"no room today"}`, "no room today"},
		{"reason alias", `{"ledger_id":5,"reason":"no room today"}`, "no room today"},
		{"say beats alias", `{"ledger_id":5,"say":"spoken","reason":"silent"}`, "spoken"},
		{"neither", `{"ledger_id":5}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeDeclinePayArgs([]byte(tc.raw))
			if err != nil {
				t.Fatalf("DecodeDeclinePayArgs: %v", err)
			}
			if args := got.(DeclinePayArgs); args.Say != tc.want {
				t.Errorf("Say = %q, want %q", args.Say, tc.want)
			}
		})
	}
}

// TestDecodeCounterPayArgs_MessageAliasesToSay is the counter_pay half of the fold.
func TestDecodeCounterPayArgs_MessageAliasesToSay(t *testing.T) {
	got, err := DecodeCounterPayArgs([]byte(`{"ledger_id":5,"amount":6,"message":"six, and it's yours"}`))
	if err != nil {
		t.Fatalf("DecodeCounterPayArgs: %v", err)
	}
	if args := got.(CounterPayArgs); args.Say != "six, and it's yours" {
		t.Errorf("Say = %q, want the `message` alias to have landed", args.Say)
	}
}

// TestResponseSaySchemas_CapMatchesSubstrate pins each advertised say maxLength to
// the cap the decoder actually enforces. The schema bytes are static, so a change
// to MaxSpeakTextChars would otherwise silently let the model send a line the
// decoder then rejects. Mirrors TestSceneQuoteSchema_CapsMatchSubstrate (LLM-343).
func TestResponseSaySchemas_CapMatchesSubstrate(t *testing.T) {
	if MaxSpeakTextChars != 1000 {
		t.Fatalf("MaxSpeakTextChars = %d, but the say schemas advertise maxLength 1000 — "+
			"update the literal in every schema listed below", MaxSpeakTextChars)
	}
	r := NewRegistry()
	for _, reg := range []func(*Registry) error{
		RegisterAcceptPay, RegisterDeclinePay, RegisterCounterPay,
		RegisterPayWithItem, RegisterAcceptWork, RegisterDeclineWork,
	} {
		if err := reg(r); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	for _, name := range []string{
		"accept_pay", "decline_pay", "counter_pay", "pay_with_item", "accept_work", "decline_work",
	} {
		entry, ok := r.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		schema := string(entry.Schema)
		if !strings.Contains(schema, `"say"`) {
			t.Errorf("%s: schema advertises no `say` argument — its words have nowhere to ride (LLM-350)", name)
		}
		if !strings.Contains(schema, `"maxLength": 1000`) {
			t.Errorf("%s: say maxLength drifted from MaxSpeakTextChars", name)
		}
	}
}

// TestPayWithItem_ReturnsOnlyLiveStatesOnSuccess pins the assumption
// HandlePayWithItem's speak wrapper makes: whenever sim.PayWithItem returns a nil
// error, the offer it reports actually stands — Pending on the slow path, Accepted
// on a quote take. Its failure modes REJECT (a tool error) rather than resolving to
// a failed terminal the way accept_pay's gates do.
//
// That distinction is what lets the buyer speak on a nil error at all. The wrapper
// guards on the state anyway, so this test is what keeps that guard a belt rather
// than a suspender: if a future change starts returning a failed terminal with a
// nil error, the guard silently starts doing real work and this test says so
// (code_review).
//
// Note a seller who holds none of the good still mints a PENDING offer — stock is
// checked at accept, not at offer — so that is a live offer, and speaking over it
// is correct.
func TestPayWithItem_ReturnsOnlyLiveStatesOnSuccess(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		seller string
		item   string
		amount int
	}{
		{"slow path, seller holds the good", "Bob", "bread", 4},
		{"seller holds none of it — still a live pending offer", "Bob", "ale", 4},
		{"unknown seller", "Nobody At All", "bread", 4},
		{"zero payment", "Bob", "bread", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := buildPayResponseWorld(t)
			if _, err := sim.EnsureColocatedHuddle("alice", now).Fn(w); err != nil {
				t.Fatalf("EnsureColocatedHuddle: %v", err)
			}
			res, err := sim.PayWithItem("alice", tc.seller, tc.item, 1, tc.amount, false, nil, nil, 0, 0, "", now, 0).Fn(w)
			if err != nil {
				return // a rejection is fine — nothing was minted, so nothing is said
			}
			placed, ok := res.(sim.PayWithItemResult)
			if !ok {
				t.Fatalf("result = %T, want sim.PayWithItemResult", res)
			}
			switch placed.State {
			case sim.PayLedgerStatePending, sim.PayLedgerStateAccepted:
			default:
				t.Errorf("PayWithItem returned state %q with a nil error. HandlePayWithItem's "+
					"wrapper only speaks for pending/accepted; a failed terminal arriving as a "+
					"SUCCESS would have the buyer haggling over an offer that never landed", placed.State)
			}
		})
	}
}

// TestSelectSayAlias_WhitespaceSayDoesNotSwallowTheAlias is the code_review
// regression: a whitespace-only canonical `say` is semantically empty, and must not
// beat a legacy `reason` / `message` that actually carries the refusal. Selecting on
// exact emptiness (sell's item/item_kind rule) would drop the words silently, since
// the handler trims `say` to nothing and speaks it.
func TestSelectSayAlias_WhitespaceSayDoesNotSwallowTheAlias(t *testing.T) {
	got, err := DecodeDeclinePayArgs([]byte(`{"ledger_id":5,"say":"   ","reason":"No bread today."}`))
	if err != nil {
		t.Fatalf("DecodeDeclinePayArgs: %v", err)
	}
	if args := got.(DeclinePayArgs); args.Say != "No bread today." {
		t.Errorf("Say = %q; a whitespace-only `say` swallowed the meaningful `reason` alias", args.Say)
	}

	got, err = DecodeCounterPayArgs([]byte(`{"ledger_id":5,"amount":6,"say":"\n\t","message":"Six, and it's yours."}`))
	if err != nil {
		t.Fatalf("DecodeCounterPayArgs: %v", err)
	}
	if args := got.(CounterPayArgs); args.Say != "Six, and it's yours." {
		t.Errorf("Say = %q; a whitespace-only `say` swallowed the meaningful `message` alias", args.Say)
	}
}

// TestPayWithItemCoinTranslation_EchoesSaid is the code_review regression for the
// one folded-say path that reported nothing back. A buyer who names "coins" as the
// good is translated to a plain sim.Pay (LLM-290), but the CALL is still
// pay_with_item — terminal — so its `say` rides along. sim.Pay has no result shape
// to carry the outcome, so payCoinTranslationResult wraps it, and the harness echoes
// from that. Without it, the model was told nothing about whether the room heard it.
func TestPayWithItemCoinTranslation_EchoesSaid(t *testing.T) {
	vc := &ValidatedCall{
		Name:        "pay_with_item",
		DecodedArgs: PayWithItemArgs{Seller: "Bob", Item: "coins", Qty: 3, Say: "Here, for your trouble."},
	}
	got := commitResultContent(vc, payCoinTranslationResult{Announced: true})
	if !strings.Contains(got, `You said: "Here, for your trouble."`) {
		t.Errorf("coin-translation result %q does not echo the buyer's spoken line", got)
	}
	if !strings.Contains(got, "settled as a plain payment") {
		t.Errorf("coin-translation result %q lost its own explanation", got)
	}
	if strings.Contains(got, "done()") {
		t.Errorf("coin-translation result %q asks for done() after a terminal pay_with_item (LLM-350)", got)
	}

	got = commitResultContent(vc, payCoinTranslationResult{SayRefused: "you are walking"})
	if !strings.Contains(got, "Your words went unsaid: you are walking") {
		t.Errorf("coin-translation result %q does not report the refused line", got)
	}
}
