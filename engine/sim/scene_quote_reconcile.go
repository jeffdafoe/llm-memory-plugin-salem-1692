package sim

import (
	"math"
	"sort"
	"time"
)

// scene_quote_reconcile.go — LLM-409.
//
// A standing SceneQuote is a passive advertisement: it reserves no goods (see
// scene_quote.go — reservation was rejected because the flat inventory map has
// no provenance, so escrow would block the seller from a legitimate emergency
// spend). That means a seller can spend, eat, or pay the quoted goods away out
// from under their own open lot through any of the ~nine inventory-drain paths
// (Consume, PayWithItem, production inputs, tool wear, farm upkeep, order
// delivery, …), and nothing tied to those paths knows a lot depended on the
// stock. The lot keeps standing, so the seller advertises goods he no longer
// holds and the "## Offers you've put out" cue pins him waiting on a promise he
// can't keep (the LLM-406 family of absorbing state).
//
// reconcileQuoteCoverage closes that gap centrally rather than by hooking every
// drain site: once per command, immediately BEFORE republish, it flips any
// active lot the seller can no longer cover to the terminal SceneQuoteStateShortfall.
// Running pre-publish means no published snapshot ever advertises an uncoverable
// lot, whichever path drained the goods and with zero sweep latency. It is
// O(active quotes) — a handful village-wide, hard-capped at
// SceneQuoteMaxPerSellerScene per (seller, scene) — and content-gates on an empty
// quote map, so the per-command cost on the hot path is a nil check in the
// common case.
//
// The flip is whole-lot expire, not shrink: the seller announced the lot's price
// aloud, so shrinking would have the engine silently re-price a deal to a
// quantity nobody agreed to (and a multi-line bundle has no coherent partial),
// while expire-and-narrate lets the seller re-post what he still holds. The
// seller learns of the broken promise through the perception beat
// (buildRecentlyShortfallQuotesFromMe → "## An offer you couldn't keep").

// reconcileQuoteCoverage flips every active SceneQuote whose seller can no longer
// cover it to the terminal SceneQuoteStateShortfall. MUST be called from the
// world goroutine (Run's post-command phase) before republish — it mutates quote
// state, the per-scene quote index, and emits SceneQuoteExpired via
// flipQuoteTerminal.
func (w *World) reconcileQuoteCoverage(now time.Time) {
	if len(w.Quotes) == 0 {
		return
	}
	// Collect first, mutate after — flipQuoteTerminal emits SceneQuoteExpired,
	// and though that event has no subscribers today, dispatching synchronously
	// while iterating w.Quotes would be unsafe if one were ever added. Same
	// posture as EvaluateSceneQuoteSweep.
	var shortfall []QuoteID
	for id, q := range w.Quotes {
		if q == nil || q.State != SceneQuoteStateActive {
			continue
		}
		if quoteStillCoverable(w, q) {
			continue
		}
		shortfall = append(shortfall, id)
	}
	if len(shortfall) == 0 {
		return
	}
	// Sorted so SceneQuoteExpired events emit in a stable order when several lots
	// fall through on the same command — matches EvaluateSceneQuoteSweep.
	sort.Slice(shortfall, func(i, j int) bool { return shortfall[i] < shortfall[j] })
	for _, id := range shortfall {
		q, ok := w.Quotes[id]
		if !ok || q == nil || q.State != SceneQuoteStateActive {
			continue
		}
		flipQuoteTerminal(w, w.Scenes[q.SceneID], q, SceneQuoteStateShortfall, SceneQuoteExpiredReasonShortfall, now)
	}
}

// quoteStillCoverable reports whether the seller can still deliver every line of
// the lot: for each non-service line, coverable stock (on-hand minus goods
// earmarked for a Ready order — the same sellerCoverableStock predicate quote
// create and the accept fast path use) must meet the line's need. A departed
// seller (gone from w.Actors) reads as uncoverable so the dangling lot is
// cleared. Service-capability lines carry no inventory (the grant is a capacity,
// not stock) and are skipped, exactly as at create — a lodging/service lot is
// never shortfall'd for want of stock it never had.
func quoteStillCoverable(w *World, q *SceneQuote) bool {
	seller := w.Actors[q.SellerID]
	if seller == nil {
		return false
	}
	effConsumers := effectivePayConsumerCount(q.ConsumerIDs)
	// effectivePayConsumerCount normalizes an empty set to 1, but the reconcile
	// trusts no upstream gate: a 0 here would divide-by-zero the overflow guard
	// below and panic the world loop, so fail closed on malformed consumer data.
	if effConsumers <= 0 {
		return false
	}
	for _, ln := range q.Lines {
		if itemHasCapability(w, ln.ItemKind, "service") {
			continue
		}
		// Overflow guard mirrors create/accept: a wrapped Qty*consumers product
		// would go negative and read as trivially coverable, so fail closed. A
		// lot this large can't have passed create, but the reconcile trusts no
		// upstream gate (a future in-engine minter could bypass it).
		if ln.Qty > math.MaxInt/effConsumers {
			return false
		}
		if coverable, _ := sellerCoverableStock(w, seller, ln.ItemKind); coverable < ln.Qty*effConsumers {
			return false
		}
	}
	return true
}
