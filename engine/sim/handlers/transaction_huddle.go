package handlers

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// transaction_huddle.go — ZBBS-HOME-400.
//
// The transaction-initiating tools — pay and pay_with_item (buyer-side) and
// scene_quote (seller-side) — gate their world-goroutine validation on the
// actor already being in a conversation (CurrentHuddleID != ""), rejecting with
// "you're not in a conversation — start one with the person … first." But an
// NPC that walks up to a stall to trade has NO huddle yet: the indoor / loiter
// encounter path forms none (arrival-encounter is outdoor-only; EnterOrKnock
// only forms one on an owner-only knock). Until now the ONLY tool that
// bootstrapped the co-located huddle was speak (HandleSpeak, ZBBS-HOME-363) — so
// a buyer had to speak first and pay on a later tick, and in practice was pulled
// away in between (the live Josiah restock-thrash: pay-on-arrival rejected →
// speak → wander off before the offer could be placed).
//
// withHuddleBootstrap wraps a transaction Command so the same
// EnsureColocatedHuddle that speak runs fires on the transaction tool call
// itself, forming/joining the structure (or stall-loiter) huddle with
// co-located actors before the gate runs. This makes "offer to someone present"
// a conversation-initiating act with the same standing as speaking to them,
// rather than requiring a separate prior speak to bootstrap the huddle.
//
// EnsureColocatedHuddle is idempotent and a no-op when the actor is already
// huddled, alone, or out of stall scope, so this preserves the real invariant —
// you still can't transact with an absent counterparty, because the helper
// forms no huddle and the downstream gate then rejects exactly as before — and
// it cannot churn or mint a second huddle.
//
// NOT applied to withdraw_pay (unilateral — its description says it works even
// after you've left the conversation) or to the seller-response tools
// (accept_pay / decline_pay / counter_pay), which act on an offer that was
// placed while the parties were already huddled.
func withHuddleBootstrap(actorID sim.ActorID, now time.Time, inner sim.Command) sim.Command {
	return sim.Command{Fn: func(w *sim.World) (any, error) {
		if _, err := sim.EnsureColocatedHuddle(actorID, now).Fn(w); err != nil {
			return nil, err
		}
		return inner.Fn(w)
	}}
}
