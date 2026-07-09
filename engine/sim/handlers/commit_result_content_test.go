package handlers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestCommitResultContent_SpeakEchoesLine pins the speak tool result: a
// successful speak returns its own line back to the model, quoted, instead of
// the generic "[ok]" every other commit returns. This is a plain commit
// acknowledgment — speak is terminal-on-success (LLM-321), so the tick ends on
// it and the old ZBBS-WORK-375 "call done() now" continuation steer is gone
// (there is no second within-tick round for the model to read it).
func TestCommitResultContent_SpeakEchoesLine(t *testing.T) {
	cases := []struct {
		name string
		vc   ValidatedCall
		want string
	}{
		{
			name: "speak echoes the line",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "Welcome, friend"}},
			want: `[ok] You said: "Welcome, friend".`,
		},
		{
			name: "speak text is trimmed to match what was actually spoken",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "  good morrow  "}},
			want: `[ok] You said: "good morrow".`,
		},
		{
			// %q quotes + escapes, so an utterance containing a double quote
			// can't break out of the echo's "..." framing.
			name: "embedded quote is escaped, framing holds",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: `say "hi"`}},
			want: `[ok] You said: "say \"hi\"".`,
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
	const steer = "[ok] Your offer to buy 20 carrots is before Moses James — bide for their answer. Make no second offer; call done() and let them accept, decline, or counter."
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
			// A quote take closes instantly, but with a nil cmdResult there is
			// no FastPath evidence to voice a settle (ZBBS-HOME-436 mirrors the
			// scene_quote "don't assert state without evidence" rule) — generic
			// ok. The settled message is pinned in
			// TestCommitResultContent_SettledQuoteTake.
			name: "quote take with nil result returns generic ok",
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
			want: "[ok] Your offer to buy 1 those goods is before Moses James — bide for their answer. Make no second offer; call done() and let them accept, decline, or counter.",
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

// TestCommitResultContent_CoinTokenPaymentSettle (LLM-290): a coin-token
// pay_with_item was translated to sim.Pay, so the coins already moved — the
// narration must voice the settle, NOT the pending-offer "bide for their
// answer" echo (there is no offer to answer). The coin count mirrors the
// translation's amount-else-qty rule.
func TestCommitResultContent_CoinTokenPaymentSettle(t *testing.T) {
	cases := []struct {
		name  string
		vc    ValidatedCall
		coins int
	}{
		{
			name:  "amount carries the count",
			vc:    ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "coins", Qty: 1, Amount: 5}},
			coins: 5,
		},
		{
			name:  "qty fallback",
			vc:    ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "a coin", Qty: 3}},
			coins: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commitResultContent(&tc.vc, nil)
			if !strings.Contains(got, "settled as a plain payment") {
				t.Errorf("want settle narration, got %q", got)
			}
			if !strings.Contains(got, fmt.Sprintf("%d coins", tc.coins)) {
				t.Errorf("want %d coins voiced, got %q", tc.coins, got)
			}
			if strings.Contains(got, "bide for their answer") {
				t.Errorf("pending-offer echo leaked into a settled payment: %q", got)
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
// TestCommitResultContent_PayEatHereClampNote (ZBBS-WORK-405): when the
// command clamped a take-home request to eat-here (non-portable
// consumable), the feedback says so — on the pending-offer steer AND the
// generic-[ok] flows (quote take, counter-response). Unclamped results
// keep the existing texts byte-identical.
func TestCommitResultContent_PayEatHereClampNote(t *testing.T) {
	const note = " Mind: stew can't be carried away — this settles eat-here, taken on the spot."

	// Pending-offer steer carries the note.
	vc := ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "Stew", Qty: 1, Amount: 4}}
	got := commitResultContent(&vc, sim.PayWithItemResult{State: sim.PayLedgerStatePending, EatHereClamped: true})
	want := "[ok] Your offer to buy 1 stew is before Moses James — bide for their answer. Make no second offer; call done() and let them accept, decline, or counter." + note
	if got != want {
		t.Errorf("clamped steer:\n got %q\nwant %q", got, want)
	}

	// Quote take (settled flow, ZBBS-HOME-436) carries the note inside the
	// settled message.
	vc = ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "Stew", Qty: 1, Amount: 4, QuoteID: 7}}
	got = commitResultContent(&vc, sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, EatHereClamped: true})
	want = "[ok] Settled on the spot — you pay Moses James 4 coins for 1 stew." + note + " Call done() now unless something else needs you."
	if got != want {
		t.Errorf("clamped quote take:\n got %q\nwant %q", got, want)
	}

	// Unclamped result: steer unchanged.
	vc = ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses James", Item: "Stew", Qty: 1, Amount: 4}}
	if got := commitResultContent(&vc, sim.PayWithItemResult{State: sim.PayLedgerStatePending}); !strings.HasSuffix(got, "counter.") {
		t.Errorf("unclamped steer should end at the steer text, got %q", got)
	}
}

// TestCommitResultContent_SceneQuoteEatHereClampNote (ZBBS-WORK-405 +
// ZBBS-HOME-433): a scene_quote whose proposed take-home was clamped to
// eat-here tells the seller model so; every successful quote — clamped or
// not — carries the post-quote steer so the model stops re-posting it.
func TestCommitResultContent_SceneQuoteEatHereClampNote(t *testing.T) {
	vc := ValidatedCall{Name: "sell", DecodedArgs: SceneQuoteArgs{Lines: []SceneQuoteLineArg{{ItemKind: "Stew", Qty: 1}}, Amount: 4, ConsumeNow: false}}
	const steer = "The room has heard your offer — await an answer or call done(). Do not post the same offer again."

	got := commitResultContent(&vc, sim.SceneQuoteCreateResult{QuoteID: 3, EatHereClamped: true})
	want := "[ok] Mind: stew can't be carried away — your offer stands as eat-here, taken on the spot. " + steer
	if got != want {
		t.Errorf("clamped scene_quote:\n got %q\nwant %q", got, want)
	}

	if got := commitResultContent(&vc, sim.SceneQuoteCreateResult{QuoteID: 3}); got != "[ok] Your offer now stands. "+steer {
		t.Errorf("unclamped scene_quote = %q, want standing-offer steer", got)
	}
	// An unexpected result shape (nil / wrong type) still steers but must not
	// assert "now stands" without a SceneQuoteCreateResult as evidence
	// (code_review #415).
	if got := commitResultContent(&vc, nil); got != "[ok] "+steer {
		t.Errorf("nil result = %q, want soft steer without the standing claim", got)
	}
}

// TestCommitResultContent_SettledQuoteTake pins the ZBBS-HOME-436 settled
// quote-take feedback. An instant settle (HOME-424 fast path) used to return
// the bare "[ok]" — the model read "nothing happened", and with the
// within-tick perception body frozen at tick-start needs, it re-bought the
// same item to the iteration budget (the Ezekiel six-meat morning, live
// 2026-06-12). The settled message voices the payment, the meal or handover,
// and the buyer's post-meal felt state computed from live commit-time needs.
func TestCommitResultContent_SettledQuoteTake(t *testing.T) {
	const steer = " Call done() now unless something else needs you."
	args := func(item string, qty, amount int) PayWithItemArgs {
		return PayWithItemArgs{Seller: "John Ellis", Item: item, Qty: qty, Amount: amount, QuoteID: 1, ConsumeNow: true}
	}
	cases := []struct {
		name   string
		vc     ValidatedCall
		result sim.PayWithItemResult
		want   string
	}{
		{
			// The Ezekiel case: ate one, hunger met — explicit stop steer.
			name:   "eat now, sated",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Meat", 1, 4)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1, SatisfiesNeed: "hunger", FeltAfter: ""},
			want:   "[ok] Settled on the spot — you pay John Ellis 4 coins for 1 meat. You eat it now. Your hunger is met — buy no more food now." + steer,
		},
		{
			// Still hungry after the meal — honest state, no stop steer; a
			// second purchase is then a legitimate model choice.
			name:   "eat now, still hungry",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Meat", 1, 4)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1, SatisfiesNeed: "hunger", FeltAfter: "hungry"},
			want:   "[ok] Settled on the spot — you pay John Ellis 4 coins for 1 meat. You eat it now. You still feel hungry." + steer,
		},
		{
			name:   "thirst variant, sated",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Ale", 1, 2)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1, SatisfiesNeed: "thirst"},
			want:   "[ok] Settled on the spot — you pay John Ellis 2 coins for 1 ale. You eat it now. Your thirst is met — buy no more drink now." + steer,
		},
		{
			// ZBBS-WORK-409: an eat-here dwell meal (MealMinutes > 0) replaces the
			// "free to leave" closer with a stay-and-finish message naming the
			// time-to-finish and the cost of bolting, so NPCs stop walking off
			// mid-meal. Shares sim.DwellStayClause with the perception dwell line.
			name:   "eat-here dwell meal: stay-and-finish closer",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Porridge", 1, 10)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1, SatisfiesNeed: "hunger", MealMinutes: 12},
			want:   "[ok] Settled on the spot — you pay John Ellis 10 coins for 1 porridge. You start eating it now. It will take you 12 more minute(s) to finish eating it all. If you leave now you will waste the rest and the coins you paid, and you will remain hungry. Buy no more food now. Call done() to keep eating where you sit.",
		},
		{
			// Drink dwell: same shape, drink wording (food-agnostic via need).
			name:   "eat-here dwell drink: drink wording",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Ale", 1, 2)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1, SatisfiesNeed: "thirst", MealMinutes: 8},
			want:   "[ok] Settled on the spot — you pay John Ellis 2 coins for 1 ale. You start drinking it now. It will take you 8 more minute(s) to finish drinking it all. If you leave now you will waste the rest and the coins you paid, and you will remain thirsty. Buy no more drink now. Call done() to keep drinking where you sit.",
		},
		{
			// WORK-391 needs-clamp split on a multi-unit order.
			name:   "eat/kept split voiced",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Bread", 4, 8)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 2, KeptToInventory: 2, SatisfiesNeed: "hunger"},
			want:   "[ok] Settled on the spot — you pay John Ellis 8 coins for 4 bread. You eat 2 now; 2 goes into your pack — you can absorb no more. Your hunger is met — buy no more food now." + steer,
		},
		{
			// Take-home settle: no meal, goods handed over at accept.
			name:   "take-home settle",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "John Ellis", Item: "Bread", Qty: 2, Amount: 4, QuoteID: 1}},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, TookHome: true},
			want:   "[ok] Settled on the spot — you pay John Ellis 4 coins for 2 bread. The goods are in your pack." + steer,
		},
		{
			// Lodging settle: booking minted, check-in is the keeper's beat.
			name:   "lodging booking",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "John Ellis", Item: "nights_stay", Qty: 1, Amount: 4, QuoteID: 1}},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, Booked: true},
			want:   "[ok] Settled on the spot — you pay John Ellis 4 coins for 1 nights_stay. Your lodging is booked — the keeper will see you checked in." + steer,
		},
		{
			// A free quote (~0 coins) must not render "you pay 0 coins".
			name:   "zero-coin settle",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Water", 1, 0)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1, SatisfiesNeed: "thirst"},
			want:   "[ok] Settled on the spot — John Ellis hands over 1 water for nothing. You eat it now. Your thirst is met — buy no more drink now." + steer,
		},
		{
			name:   "single coin is singular",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Bread", 1, 1)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1, SatisfiesNeed: "hunger", FeltAfter: "peckish"},
			want:   "[ok] Settled on the spot — you pay John Ellis 1 coin for 1 bread. You eat it now. You still feel peckish." + steer,
		},
		{
			// Group order where the buyer ate nothing: surplus still voiced.
			name:   "buyer not a consumer, surplus pocketed",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Stew", 3, 6)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, KeptToInventory: 1},
			want:   "[ok] Settled on the spot — you pay John Ellis 6 coins for 3 stew. 1 uneaten goes into your pack." + steer,
		},
		{
			// Defensive: a FastPath result with wrong-typed args still must not
			// claim details it can't render.
			name:   "wrong args type degrades to generic ok",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: struct{ X int }{X: 1}},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true},
			want:   "[ok]",
		},
		{
			// Defensive: FastPath in any non-accepted state must not voice a
			// settle that didn't happen (code_review).
			name:   "fast path without accepted state degrades to generic ok",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Meat", 1, 4)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStatePending, FastPath: true, BuyerAte: 1},
			want:   "[ok]",
		},
		{
			// Defensive: nonsense quantities can't reach an accepted settle
			// (the command layer rejects qty < 1), but the display must not
			// voice "0 meat" if they ever do (code_review).
			name:   "non-positive qty degrades to generic ok",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Meat", 0, 4)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1},
			want:   "[ok]",
		},
		{
			// Defensive: a negative amount must not normalize into the
			// "hands over ... for nothing" free-purchase line (code_review).
			name:   "negative amount degrades to generic ok",
			vc:     ValidatedCall{Name: "pay_with_item", DecodedArgs: args("Meat", 1, -4)},
			result: sim.PayWithItemResult{State: sim.PayLedgerStateAccepted, FastPath: true, BuyerAte: 1},
			want:   "[ok]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commitResultContent(&tc.vc, tc.result)
			if got != tc.want {
				t.Errorf("commitResultContent\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}

func TestCommitResultContent_ConsumeClamp(t *testing.T) {
	vc := ValidatedCall{Name: "consume", DecodedArgs: ConsumeArgs{Item: "meat", Qty: 10}}

	got := commitResultContent(&vc, sim.ConsumeResult{Kind: "meat", Requested: 10, Consumed: 2, Kept: 8})
	want := "[ok] You consume 2 meat — that satisfies you; the remaining 8 stay in your pack. Do not consume more now."
	if got != want {
		t.Errorf("clamped consume content:\n got %q\nwant %q", got, want)
	}

	// No SatisfiesNeed on the result (a consume that moved no tracked need) →
	// bare [ok]; LLM-7 gates the felt-state steer on SatisfiesNeed != "".
	if got := commitResultContent(&vc, sim.ConsumeResult{Kind: "meat", Requested: 1, Consumed: 1, Kept: 0}); got != "[ok]" {
		t.Errorf("unclamped consume, no need info = %q, want [ok]", got)
	}
	if got := commitResultContent(&vc, nil); got != "[ok]" {
		t.Errorf("nil result = %q, want [ok]", got)
	}
}

// LLM-7: a need-moving consume with no surplus voices honest post-consume felt
// state (mirrors the pay_with_item eat feedback) so a sated NPC is steered to
// stop instead of a bare [ok] that the stale eat-affordance furniture overrides
// into a re-eat loop.
func TestCommitResultContent_ConsumeNeedFeedback(t *testing.T) {
	vc := ValidatedCall{Name: "consume", DecodedArgs: ConsumeArgs{Item: "bread", Qty: 1}}

	cases := []struct {
		name   string
		result sim.ConsumeResult
		want   string
	}{
		{
			name:   "hunger sated → stop steer",
			result: sim.ConsumeResult{Kind: "bread", Requested: 1, Consumed: 1, Kept: 0, SatisfiesNeed: "hunger", Verb: "eat"},
			want:   "[ok] You consume 1 bread. Your hunger is met — eat no more now.",
		},
		{
			name:   "still hungry → honest felt, no stop steer",
			result: sim.ConsumeResult{Kind: "bread", Requested: 1, Consumed: 1, Kept: 0, SatisfiesNeed: "hunger", FeltAfter: "peckish", Verb: "eat"},
			want:   "[ok] You consume 1 bread. You still feel peckish.",
		},
		{
			name:   "thirst sated → drink wording",
			result: sim.ConsumeResult{Kind: "water", Requested: 1, Consumed: 1, Kept: 0, SatisfiesNeed: "thirst", Verb: "drink"},
			want:   "[ok] You consume 1 water. Your thirst is met — drink no more now.",
		},
		{
			// LLM-318: a belly-filling ale eases hunger (SatisfiesNeed) but is a
			// drink (Verb) — the need clause names hunger, the verb follows the
			// category, so it reads "drink no more now", not "eat".
			name:   "drink easing hunger → drink verb, hunger clause",
			result: sim.ConsumeResult{Kind: "ale", Requested: 1, Consumed: 1, Kept: 0, SatisfiesNeed: "hunger", Verb: "drink"},
			want:   "[ok] You consume 1 ale. Your hunger is met — drink no more now.",
		},
		{
			// Empty Verb (legacy/zero-consume literal) falls back to the need-keyed
			// verb, preserving the pre-LLM-318 wording.
			name:   "empty verb → need-keyed fallback",
			result: sim.ConsumeResult{Kind: "bread", Requested: 1, Consumed: 1, Kept: 0, SatisfiesNeed: "hunger"},
			want:   "[ok] You consume 1 bread. Your hunger is met — eat no more now.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commitResultContent(&vc, tc.result); got != tc.want {
				t.Errorf("consume need feedback:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestCommitResultContent_GatherSteer pins the LLM-175 post-gather copy: gather is
// tick-terminal now, so the result is purely informational — it names the source
// when known and says the harvest lands next turn, with NO "do not gather again /
// call done() now" steer (the engine ends the turn for it). The "all of it / it's
// bare" beat lands on the next-tick completion perception, not here. A result that
// didn't start, a wrong-typed payload, or a nil result degrade to generic [ok].
func TestCommitResultContent_GatherSteer(t *testing.T) {
	vc := ValidatedCall{Name: "gather", DecodedArgs: GatherArgs{}}
	const want = "[ok] You start gathering. It finishes on its own in a few seconds; the harvest lands in your pack next turn."
	if got := commitResultContent(&vc, sim.SourceActivityStartResult{Started: true}); got != want {
		t.Errorf("started gather:\n got %q\nwant %q", got, want)
	}
	const wantNamed = "[ok] You start gathering at Raspberry Bush. It finishes on its own in a few seconds; the harvest lands in your pack next turn."
	if got := commitResultContent(&vc, sim.SourceActivityStartResult{Started: true, SourceName: "Raspberry Bush"}); got != wantNamed {
		t.Errorf("started gather (named source):\n got %q\nwant %q", got, wantNamed)
	}
	if got := commitResultContent(&vc, sim.SourceActivityStartResult{Started: false}); got != "[ok]" {
		t.Errorf("not-started gather = %q, want generic [ok]", got)
	}
	if got := commitResultContent(&vc, nil); got != "[ok]" {
		t.Errorf("nil result = %q, want generic [ok]", got)
	}
}

// TestCommitResultContent_ProduceBatchStart pins the LLM-319 post-produce copy:
// a started one-shot batch is confirmed by name with its size and humanized
// work duration, the consumed inputs when the recipe has any, and the "it
// finishes on its own" closer that stops the model from re-calling produce
// mid-cycle (the LLM-120 re-craft loop, one-shot edition). A missing noun
// falls back to the raw kind key; a wrong-typed payload or nil result degrade
// to generic [ok].
func TestCommitResultContent_ProduceBatchStart(t *testing.T) {
	vc := ValidatedCall{Name: "produce", DecodedArgs: CraftArgs{Item: "Nail"}}
	const closer = " It finishes on its own while you work here — no need to produce again until it lands."

	got := commitResultContent(&vc, sim.ProductionStartResult{Item: "nail", Noun: "nails", BatchQty: 24, DurationSeconds: 4500, InputsUsed: "3 iron"})
	want := "[ok] You start a batch of nails — 24 when it's done, about an hour and a quarter of work at your post. You use 3 iron." + closer
	if got != want {
		t.Errorf("produce batch start:\n got %q\nwant %q", got, want)
	}

	// An origin producer's recipe has no inputs — the "You use ..." clause is
	// omitted entirely, no dangling "You use ." fragment.
	got = commitResultContent(&vc, sim.ProductionStartResult{Item: "nail", Noun: "nails", BatchQty: 10, DurationSeconds: 1200})
	want = "[ok] You start a batch of nails — 10 when it's done, about 20 minutes of work at your post." + closer
	if got != want {
		t.Errorf("produce batch start (no inputs):\n got %q\nwant %q", got, want)
	}

	// No noun on the result (a discovery-minted kind with no catalog phrase) →
	// fall back to the raw kind key rather than render an empty good.
	got = commitResultContent(&vc, sim.ProductionStartResult{Item: "nail", BatchQty: 10, DurationSeconds: 1200})
	want = "[ok] You start a batch of nail — 10 when it's done, about 20 minutes of work at your post." + closer
	if got != want {
		t.Errorf("produce batch start noun fallback:\n got %q\nwant %q", got, want)
	}

	// Durable-tool wear (LLM-330): the world-side pre-phrased wear clause rides
	// after the consumed inputs, before the closer.
	got = commitResultContent(&vc, sim.ProductionStartResult{Item: "stew", Noun: "stew", BatchQty: 10, DurationSeconds: 4500,
		InputsUsed: "2 sage", ToolWear: "Your skillet has about 12 more uses in it."})
	want = "[ok] You start a batch of stew — 10 when it's done, about an hour and a quarter of work at your post. You use 2 sage. Your skillet has about 12 more uses in it." + closer
	if got != want {
		t.Errorf("produce batch start (tool wear):\n got %q\nwant %q", got, want)
	}

	if got := commitResultContent(&vc, struct{ X int }{X: 1}); got != "[ok]" {
		t.Errorf("wrong result type = %q, want generic [ok]", got)
	}
	if got := commitResultContent(&vc, nil); got != "[ok]" {
		t.Errorf("nil result = %q, want generic [ok]", got)
	}
}
