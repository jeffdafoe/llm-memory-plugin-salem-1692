package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestCommitResultContent_SpeakEchoesLine pins the speak tool result: a
// successful speak returns its own line back to the model (quoted, the
// ZBBS-WORK-368 within-tick salience echo) plus the ZBBS-WORK-375 post-speak
// continuation steer (bias to done(), forbid re-greet/re-pitch/rephrase),
// instead of the generic "[ok]" every other commit returns. With HOME-381's
// hard cap gone, this tool result is the recency-dominant message the model
// reads before deciding whether to speak again or end the turn.
func TestCommitResultContent_SpeakEchoesLine(t *testing.T) {
	cases := []struct {
		name string
		vc   ValidatedCall
		want string
	}{
		{
			name: "speak echoes the line + continuation steer",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "Welcome, friend"}},
			want: `[ok] You said: "Welcome, friend". You have spoken — call done() now unless a new event has arrived or someone asked you something distinct you have not yet answered. Do not greet again, re-pitch, or rephrase what you just said.`,
		},
		{
			name: "speak text is trimmed to match what was actually spoken",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "  good morrow  "}},
			want: `[ok] You said: "good morrow". You have spoken — call done() now unless a new event has arrived or someone asked you something distinct you have not yet answered. Do not greet again, re-pitch, or rephrase what you just said.`,
		},
		{
			// %q quotes + escapes, so an utterance containing a double quote
			// can't break out of the echo's "..." framing.
			name: "embedded quote is escaped, framing holds",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: `say "hi"`}},
			want: `[ok] You said: "say \"hi\"". You have spoken — call done() now unless a new event has arrived or someone asked you something distinct you have not yet answered. Do not greet again, re-pitch, or rephrase what you just said.`,
		},
		{
			// Defensive: can't happen on the success path (sim.Speak rejects
			// empty text), but the guard must not echo `You said: ""`.
			name: "whitespace-only text falls back to generic ok",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "   "}},
			want: "[ok]",
		},
		{
			// Defensive: a future refactor that hands the wrong decoded type
			// must degrade to the generic ok, not panic.
			name: "wrong args type falls back to generic ok",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: struct{ X int }{X: 1}},
			want: "[ok]",
		},
		{
			name: "non-speak commit returns the generic ok unchanged",
			vc:   ValidatedCall{Name: "move_to", DecodedArgs: nil},
			want: "[ok]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commitResultContent(&tc.vc, nil)
			if got != tc.want {
				t.Errorf("commitResultContent\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}

// TestCommitResultContent_PayWithItemSteer pins the pay_with_item tool result
// (ZBBS-HOME-395): a placed plain offer echoes the pending offer back to the
// model plus an await-the-seller / done() steer, instead of the bare "[ok]"
// that pre-395 read as "nothing happened" and drove the re-offer storm. The
// quote-take and counter-response paths keep the generic "[ok]" (they don't
// storm), and any non-offer or wrong-typed call degrades to "[ok]".
func TestCommitResultContent_PayWithItemSteer(t *testing.T) {
	const steer = "[ok] Your offer to buy 20 carrots from Moses James is now before them, awaiting their answer. Do not offer again — call done() and let them accept, decline, or counter. Offer again only after they have responded."
	cases := []struct {
		name string
		vc   ValidatedCall
		want string
	}{
		{
			name: "plain offer echoes pending offer + steer",
			vc:   ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 20, Amount: 10}},
			want: steer,
		},
		{
			// Item is lowercased + whitespace-collapsed, seller is trimmed, so
			// trivial drift renders the same steer the dedup key matches on.
			name: "item normalized, seller trimmed",
			vc:   ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "  Moses James  ", Item: "  Carrots  ", Qty: 20, Amount: 10}},
			want: steer,
		},
		{
			// A quote take closes instantly — not a pending offer — so it keeps
			// the generic ok and is exempt from the dedup key.
			name: "quote take returns generic ok",
			vc:   ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 20, Amount: 10, QuoteID: 7}},
			want: "[ok]",
		},
		{
			// A counter-response is a deliberate distinct move — generic ok.
			name: "counter-response returns generic ok",
			vc:   ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 20, Amount: 10, InResponseTo: 12}},
			want: "[ok]",
		},
		{
			// Defensive: empty item can't reach the success path (decode rejects
			// it), but the steer must not render "buy 1 " with a gap.
			name: "empty item falls back to those goods",
			vc:   ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "", Qty: 1}},
			want: "[ok] Your offer to buy 1 those goods from Moses James is now before them, awaiting their answer. Do not offer again — call done() and let them accept, decline, or counter. Offer again only after they have responded.",
		},
		{
			name: "wrong args type falls back to generic ok",
			vc:   ValidatedCall{Name: "pay_with_item", DecodedArgs: struct{ X int }{X: 1}},
			want: "[ok]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commitResultContent(&tc.vc, nil)
			if got != tc.want {
				t.Errorf("commitResultContent\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}

// TestPayOfferKey pins the same-tick repeat-offer dedup key (ZBBS-HOME-395):
// keyed on (seller, item, disposition) with price EXCLUDED so a re-offer at a
// drifting amount still matches, normalized so spacing/case drift matches, and
// returning (_, false) for the quote-take / counter-response / non-offer paths
// that are exempt from the guard.
func TestPayOfferKey(t *testing.T) {
	keyOf := func(args PayWithItemArgs) (string, bool) {
		return payOfferKey(&ValidatedCall{Name: "pay_with_item", DecodedArgs: args})
	}

	base := PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 20, Amount: 5}
	k1, ok1 := keyOf(base)
	if !ok1 {
		t.Fatal("plain offer should produce a key")
	}

	// Price drift (the actual storm: 5 → 10, qty held) must collide — amount is
	// excluded from the key.
	if k2, ok2 := keyOf(PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 20, Amount: 10}); !ok2 || k2 != k1 {
		t.Errorf("price drift should collide: k1=%q k2=%q", k1, k2)
	}

	// Qty drift (20 → 5, price held) must ALSO collide — qty is excluded too, by
	// design: one pending offer per (seller, item, disposition) per tick, the
	// buyer reconsiders quantity next tick after the seller responds (not by
	// stacking a second pending offer the seller has not yet seen).
	if kq, okq := keyOf(PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 5, Amount: 5}); !okq || kq != k1 {
		t.Errorf("qty drift should collide: k1=%q kq=%q", k1, kq)
	}

	// Spacing/case drift normalizes to the same key — qty/price held constant
	// here to isolate normalization from the term-exclusion above.
	if k3, ok3 := keyOf(PayWithItemArgs{Seller: "  moses  james ", Item: "CARROTS", Qty: 20, Amount: 5}); !ok3 || k3 != k1 {
		t.Errorf("case/space drift should collide: k1=%q k3=%q", k1, k3)
	}

	// A different item to the same seller is a distinct, allowed offer.
	if k4, _ := keyOf(PayWithItemArgs{Seller: "Moses James", Item: "wheat", Qty: 1, Amount: 1}); k4 == k1 {
		t.Error("different item should not collide")
	}

	// consume-now vs keep are kept distinct (buy one to eat, one to stock).
	if k5, _ := keyOf(PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 1, Amount: 1, ConsumeNow: true}); k5 == k1 {
		t.Error("disposition should keep keep/consume distinct")
	}

	// Exempt paths: quote take, counter-response, and any non-pay tool.
	if _, ok := keyOf(PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 1, Amount: 1, QuoteID: 7}); ok {
		t.Error("quote take must be exempt")
	}
	if _, ok := keyOf(PayWithItemArgs{Seller: "Moses James", Item: "carrots", Qty: 1, Amount: 1, InResponseTo: 9}); ok {
		t.Error("counter-response must be exempt")
	}
	if _, ok := payOfferKey(&ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "hi"}}); ok {
		t.Error("non-pay tool must return false")
	}
}

// TestCommitResultContent_ConsumeClamp pins the ZBBS-WORK-391 clamped-consume
// result: when the needs-clamp held units back (Kept > 0) the model is told
// the eaten/kept split — a bare [ok] after "consume 10" reads as ten eaten
// and drives a re-consume of the surplus. Unclamped consumes and non-result
// payloads keep the generic [ok].
func TestCommitResultContent_ConsumeClamp(t *testing.T) {
	vc := ValidatedCall{Name: "consume", DecodedArgs: ConsumeArgs{Item: "meat", Qty: 10}}

	got := commitResultContent(&vc, sim.ConsumeResult{Kind: "meat", Requested: 10, Consumed: 2, Kept: 8})
	want := "[ok] You consume 2 meat — that satisfies you; the remaining 8 stay in your pack. Do not consume more now."
	if got != want {
		t.Errorf("clamped consume content:\n got %q\nwant %q", got, want)
	}

	if got := commitResultContent(&vc, sim.ConsumeResult{Kind: "meat", Requested: 1, Consumed: 1, Kept: 0}); got != "[ok]" {
		t.Errorf("unclamped consume = %q, want [ok]", got)
	}
	if got := commitResultContent(&vc, nil); got != "[ok]" {
		t.Errorf("nil result = %q, want [ok]", got)
	}
}
