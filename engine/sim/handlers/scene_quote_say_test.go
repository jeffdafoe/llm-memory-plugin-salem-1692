package handlers_test

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
)

// scene_quote_say_test.go — LLM-343. speak and sell are both tick-terminal, so
// a keeper told to "say your price, then call sell" only ever got the speech
// out: the tick ended on the speak and no payable offer was ever posted. Live
// specimen (2026-07-09, tavern): a player ordered "a bowl of soup and a loaf of
// bread", John Ellis answered "six coins for the both of them together. I'll
// fetch 'em for you", and his pay screen stayed empty.
//
// The fix folds the utterance into sell's `say`. These tests pin the two
// properties that make it correct end-to-end, across the handler → world
// boundary (a unit test on the handler alone would pass while the world stayed
// silent):
//
//  1. ONE sell call both posts the offer and speaks the line.
//  2. The quote is minted BEFORE the words go out, so a rejected quote leaves
//     the keeper silent rather than quoting a price against nothing.

// TestSceneQuoteSay_PostsOfferAndSpeaksInOneCall is the repro: the exact shape
// of the tavern order (two goods, one total price, one spoken line) must leave
// both a live quote and a heard utterance behind after a single tool call.
func TestSceneQuoteSay_PostsOfferAndSpeaksInOneCall(t *testing.T) {
	w, stop := buildArrivalWorld(t)
	defer stop()

	const line = "A bowl of stew runs four coins, and a loaf of bread two — six coins for the both of them together."
	cmd, err := handlers.HandleSceneQuote(handlers.HandlerInput{
		ActorID: "seller", AttemptID: "tk-1",
		Args: handlers.SceneQuoteArgs{
			Lines:      []handlers.SceneQuoteLineArg{{ItemKind: "stew", Qty: 1}},
			Amount:     6,
			ConsumeNow: true,
			Say:        line,
		},
	})
	if err != nil {
		t.Fatalf("HandleSceneQuote: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("sell with say rejected: %v", err)
	}

	created, ok := res.(sim.SceneQuoteCreateResult)
	if !ok {
		t.Fatalf("result = %T, want sim.SceneQuoteCreateResult", res)
	}
	if created.QuoteID == 0 {
		t.Error("no quote minted — the buyer would have nothing to pay against")
	}
	if !created.Announced {
		t.Error("Announced = false; the seller's words never reached the room")
	}

	// The utterance must actually be in the world, not merely reported by the
	// result. This is the assertion that would have caught the live bug.
	snap := w.Published()
	huddleID := snap.Actors["seller"].CurrentHuddleID
	if huddleID == "" {
		t.Fatal("seller not huddled after sell")
	}
	h := snap.Huddles[huddleID]
	if h == nil {
		t.Fatalf("huddle %q missing from snapshot", huddleID)
	}
	var heard bool
	for _, u := range h.RecentUtterances {
		if u.SpeakerID == "seller" && strings.Contains(u.Text, "six coins for the both of them") {
			heard = true
		}
	}
	if !heard {
		t.Errorf("seller's say never entered the conversation; ring = %+v", h.RecentUtterances)
	}
}

// TestSceneQuoteSay_QuoteFailsSoNothingIsSpoken pins the ordering invariant. A
// quote the world refuses (here: an item the seller holds none of) must abort
// the whole tool — a keeper who names a price for goods he cannot offer is the
// original bug wearing different clothes.
func TestSceneQuoteSay_QuoteFailsSoNothingIsSpoken(t *testing.T) {
	w, stop := buildArrivalWorld(t)
	defer stop()

	cmd, err := handlers.HandleSceneQuote(handlers.HandlerInput{
		ActorID: "seller", AttemptID: "tk-1",
		Args: handlers.SceneQuoteArgs{
			Lines:      []handlers.SceneQuoteLineArg{{ItemKind: "stew", Qty: 999}},
			Amount:     6,
			ConsumeNow: true,
			Say:        "Six coins for the lot, friend.",
		},
	})
	if err != nil {
		t.Fatalf("HandleSceneQuote (static validation): %v", err)
	}
	if _, err := w.Send(cmd); err == nil {
		t.Fatal("sell for 999 stew succeeded; expected an out-of-stock rejection")
	}

	snap := w.Published()
	if huddleID := snap.Actors["seller"].CurrentHuddleID; huddleID != "" {
		if h := snap.Huddles[huddleID]; h != nil {
			for _, u := range h.RecentUtterances {
				if u.SpeakerID == "seller" {
					t.Errorf("seller spoke %q despite the quote being refused — "+
						"a price was named against an offer that does not exist", u.Text)
				}
			}
		}
	}
}

// TestSceneQuoteSay_OmittedLeavesAColdOffer guards the common shape: most sells
// carry no words at all (24 of 26 live sells on 2026-07-09), and folding `say`
// into the tool must not make speech mandatory.
func TestSceneQuoteSay_OmittedLeavesAColdOffer(t *testing.T) {
	w, stop := buildArrivalWorld(t)
	defer stop()

	cmd, err := handlers.HandleSceneQuote(handlers.HandlerInput{
		ActorID: "seller", AttemptID: "tk-1",
		Args: handlers.SceneQuoteArgs{
			Lines:      []handlers.SceneQuoteLineArg{{ItemKind: "stew", Qty: 1}},
			Amount:     4,
			ConsumeNow: false,
		},
	})
	if err != nil {
		t.Fatalf("HandleSceneQuote: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("cold sell rejected: %v", err)
	}
	created, ok := res.(sim.SceneQuoteCreateResult)
	if !ok {
		t.Fatalf("result = %T, want sim.SceneQuoteCreateResult", res)
	}
	if created.QuoteID == 0 {
		t.Error("cold sell minted no quote")
	}
	if created.Announced {
		t.Error("Announced = true for a sell with no say")
	}
}

// TestSceneQuoteSay_RefusedSayStillPostsTheOfferAndReportsWhy pins the
// best-effort contract. SpeakTo carries gates the quote does not — here the
// turn-state gate: the seller has already spoken and is owed a reply, so a
// second unprompted line is refused. The offer must still stand (losing a sale
// to a conversational-discipline rule would be the original bug inverted), and
// the seller must be handed SpeakTo's own reason rather than a guess, since its
// reachable refusals call for different corrections.
func TestSceneQuoteSay_RefusedSayStillPostsTheOfferAndReportsWhy(t *testing.T) {
	w, stop := buildArrivalWorld(t)
	defer stop()

	// The seller speaks, and the buyer never answers.
	speakCmd, err := handlers.HandleSpeak(handlers.HandlerInput{
		ActorID: "seller", AttemptID: "tk-0",
		Args: handlers.SpeakArgs{Text: "Good evening to you."},
	})
	if err != nil {
		t.Fatalf("HandleSpeak: %v", err)
	}
	if _, err := w.Send(speakCmd); err != nil {
		t.Fatalf("opening speak rejected: %v", err)
	}

	// With no reply and no new news, the say is refused — but the sell is not.
	cmd, err := handlers.HandleSceneQuote(handlers.HandlerInput{
		ActorID: "seller", AttemptID: "tk-1", HasNewNews: false,
		Args: handlers.SceneQuoteArgs{
			Lines:      []handlers.SceneQuoteLineArg{{ItemKind: "stew", Qty: 1}},
			Amount:     4,
			ConsumeNow: true,
			Say:        "Four coins the bowl.",
		},
	})
	if err != nil {
		t.Fatalf("HandleSceneQuote: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("sell rejected outright; the offer must stand even when the say is refused: %v", err)
	}
	created := res.(sim.SceneQuoteCreateResult)
	if created.QuoteID == 0 {
		t.Fatal("no quote minted — a refused say must not cost the seller the offer")
	}
	if created.Announced {
		t.Error("Announced = true though the turn-state gate refused the line")
	}
	if created.SayRefused == "" {
		t.Fatal("SayRefused is empty; the seller is left with no reason its words went unsaid")
	}
	if !strings.Contains(created.SayRefused, "already spoke") {
		t.Errorf("SayRefused = %q, want SpeakTo's own turn-state reason", created.SayRefused)
	}
}
