package sim

import "time"

// price_book.go — in-memory per-(seller, item) ring buffer of recent
// transactions, the v2 substrate for v1's price-history perception cues.
//
// v1 reference: engine/pay_history.go. The buyer-side perception
// surface ("you paid X coins last time, three days ago" / "ask the
// keeper") is the load-bearing render path that established this
// substrate's contract. v1 documents the gameplay framing directly:
//
//   "Cost is never surfaced as ground truth from a vendor's price
//    config — NPCs only know what they've personally paid for. No
//    history → 'ask the keeper.' Knowledge of price is earned by
//    patronage."
//
// v2 extends the model on the seller side: a shopkeeper has a wider
// perspective than any individual buyer and naturally knows what they've
// been charging. Both views are read paths over the same store; the
// asymmetry lives in the render, not the substrate.
//
// Storage shape. World.PriceBook is keyed by (SellerID, ItemKind);
// each entry is a ring buffer of recent PriceObservation rows. The
// BuyerID lives ON each entry, not in the key — so the buyer-side
// lookup filters the buffer for entries where BuyerID == subject, and
// the seller-side lookup reads the buffer directly. One store, two
// read paths.
//
// Maintenance. Subscribes to PayWithItemResolved{TerminalState=Accepted}:
// every accepted offer (ConsumeNow AND take-home) appends one entry
// to the buyer's view of the seller's pricing for that item kind.
// Matches v1's `state='accepted'` filter on pay_ledger — knowledge
// lands at acceptance, not at delivery.
//
// Why not OrderDelivered. ConsumeNow offers never mint an Order; they
// transfer goods at accept-time. Subscribing to OrderDelivered would
// miss every eat-on-the-spot transaction. PayWithItemResolved is the
// architecture-§-10 canonical "commerce ended" event and is the right
// hook for both flows.
//
// Startup seed. LoadWorld pulls top-K-per-(seller, item) accepted rows
// from pay_ledger via OrdersRepo.LoadRecentPrices, populating the
// ring buffers oldest-first so the most-recent slot holds the most
// recent observation. Restart-loss without a seed would cause a
// thundering herd of "ask the keeper" turns; the seed window
// (PriceBookSeedWindow, default 90 days) caps the cost.
//
// Durability. None. The substrate is purely a perception cache —
// pay_ledger remains the source of truth. No checkpoint pass; no
// projection sink. Same posture as ActionLog. Knowledge naturally
// degrades across restarts as old observations age out beyond the
// seed window; that matches the gameplay framing ("knowledge earned
// by patronage" — knowledge from too long ago is allowed to fade).

// PriceBookKey identifies one (seller, item) bucket in the price book.
// Buyers DO NOT appear in the key — they live on each PriceObservation
// so the same buffer supports both per-buyer filtered reads and
// per-seller aggregated reads.
type PriceBookKey struct {
	SellerID ActorID
	Item     ItemKind
}

// PriceObservation is one accepted transaction's worth of pricing
// signal: who paid, how much, what shape, when. Carried in the ring
// buffer associated with the (SellerID, Item) key.
//
// Fields are sized for the v2 perception render paths:
//
//   - Amount is the total coin price for the transaction (matches
//     pay_ledger.offered_amount). The buyer-side render emits this
//     directly: "you paid 5 coins last time."
//   - Qty is the per-consumer quantity (matches pay_ledger.qty). For
//     a solo buyer Qty is the count of units bought; for a group
//     order it's the count of units PER consumer.
//   - Consumers is the number of recipients sharing the order
//     (= len(ConsumerIDs); minimum 1 for solo orders). Total units
//     sold = Qty * Consumers; per-unit price = Amount / (Qty * Consumers).
//     Stored explicitly so future seller-side aggregates ("you sold
//     N units this week") don't have to reconstruct it.
//   - At is the moment of acceptance — used for the "N days ago"
//     wording and for any time-windowed seller aggregation.
type PriceObservation struct {
	BuyerID   ActorID
	Amount    int
	Qty       int
	Consumers int
	At        time.Time
}

// PriceBookSeedRecord is one row returned by OrdersRepo.LoadRecentPrices
// at LoadWorld time. Pairs a PriceBookKey with the PriceObservation
// that goes into the keyed ring buffer. The repo returns records
// oldest-first per (seller, item) so SeedPriceBook can push them
// directly into ring buffers without re-sorting.
type PriceBookSeedRecord struct {
	Key         PriceBookKey
	Observation PriceObservation
}

// PriceBookRingCapacity bounds the per-(seller, item) ring buffer
// size. 20 entries covers ~2 weeks of activity for a 1-2 transactions/
// day item and ~16 hours for a busy 30/day vendor. Tune up if seller
// perception surfaces a longer aggregation window; memory cost is
// ~80 bytes/entry × 20 × per-key, trivial at v2 scale.
const PriceBookRingCapacity = 20

// PriceBookSeedWindow bounds how far back LoadWorld pulls historical
// pay_ledger rows when seeding the price book. 90 days caps the seed
// cost while preserving "I paid Ezekiel a coin for ale three weeks
// ago" recall across restarts. Older observations age out and the
// next acceptance refreshes them.
const PriceBookSeedWindow = 90 * 24 * time.Hour

// SeedPriceBook populates World.PriceBook from a list of seed records.
// Records MUST be supplied oldest-first per (SellerID, Item) so the
// most-recent slot in each ring buffer ends up holding the most recent
// observation (RingBuffer.Push appends at head; the contract is
// chronological order). OrdersRepo.LoadRecentPrices enforces the
// ordering on the repo side.
//
// MUST be called before World.Run (LoadWorld is the canonical caller).
// Direct map writes — no Command channel — because the world goroutine
// hasn't started yet at LoadWorld time. Lazy-allocates w.PriceBook on
// first call.
//
// Idempotency: pushes append. Calling twice with the same records
// double-loads each ring buffer. Wiring guards live at LoadWorld;
// don't call this twice.
func (w *World) SeedPriceBook(records []PriceBookSeedRecord) {
	if len(records) == 0 {
		return
	}
	if w.PriceBook == nil {
		w.PriceBook = make(map[PriceBookKey]*RingBuffer[PriceObservation])
	}
	for _, r := range records {
		buf, ok := w.PriceBook[r.Key]
		if !ok {
			buf = NewRingBuffer[PriceObservation](PriceBookRingCapacity)
			w.PriceBook[r.Key] = buf
		}
		buf.Push(r.Observation)
	}
}

// RecordPriceObservation is the Command builder the cascade subscriber
// uses to append one entry to the (SellerID, Item) ring buffer at
// commit time. Lazy-allocates w.PriceBook and the per-key ring buffer.
// Returns the number of entries currently in the keyed buffer (for
// telemetry / test assertions); always nil error today — no validation
// can fail at this layer (the cascade has already filtered to accepted
// terminals, and the substrate trusts its caller).
func RecordPriceObservation(key PriceBookKey, obs PriceObservation) Command {
	return Command{Fn: func(w *World) (any, error) {
		if w.PriceBook == nil {
			w.PriceBook = make(map[PriceBookKey]*RingBuffer[PriceObservation])
		}
		buf, ok := w.PriceBook[key]
		if !ok {
			buf = NewRingBuffer[PriceObservation](PriceBookRingCapacity)
			w.PriceBook[key] = buf
		}
		buf.Push(obs)
		return buf.Len(), nil
	}}
}

// LookupBuyerLastPaid returns the buyer's most recent accepted price
// observation against the given (seller, item) pair, or ok=false when
// no history exists. Scans the keyed ring buffer most-recent-first and
// returns the first entry whose BuyerID matches.
//
// MUST be called from inside a Command.Fn (world goroutine). Perception
// builders running off-goroutine should read from Snapshot.PriceBook
// instead — same data, no synchronization needed.
//
// The "no history → ask the keeper" framing is implemented here as
// ok=false. The render path emits "ask the keeper" only when this
// returns ok=false; with an observation in hand, render emits the
// v1 "you paid X coins last time, N days ago" line.
func (w *World) LookupBuyerLastPaid(buyer ActorID, seller ActorID, item ItemKind) (PriceObservation, bool) {
	if w.PriceBook == nil {
		return PriceObservation{}, false
	}
	buf, ok := w.PriceBook[PriceBookKey{SellerID: seller, Item: item}]
	if !ok || buf == nil || buf.Len() == 0 {
		return PriceObservation{}, false
	}
	// Snapshot returns oldest-first; scan from the end for newest-first.
	entries := buf.Snapshot()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].BuyerID == buyer {
			return entries[i], true
		}
	}
	return PriceObservation{}, false
}

// LookupSellerRecent returns the seller's full recent-transaction
// history for the given item kind — chronological order, oldest first.
// Returns nil (not an empty slice) when no history exists, so callers
// can branch on `len(...) == 0` cleanly.
//
// The full history is returned; seller-side perception is responsible
// for computing whatever aggregation it needs (last-N, time-windowed
// average, distinct-buyer count, etc.). Computing here would foreclose
// future aggregations; the ring buffer is small enough that returning
// the whole slice is cheap.
//
// MUST be called from inside a Command.Fn (world goroutine). Off-
// goroutine readers use Snapshot.PriceBook.
func (w *World) LookupSellerRecent(seller ActorID, item ItemKind) []PriceObservation {
	if w.PriceBook == nil {
		return nil
	}
	buf, ok := w.PriceBook[PriceBookKey{SellerID: seller, Item: item}]
	if !ok || buf == nil || buf.Len() == 0 {
		return nil
	}
	return buf.Snapshot()
}

// ClonePriceBook deep-copies the entire price book map for snapshot
// republish. Returns nil for an empty input so Snapshot.PriceBook's
// field semantics match an unset map exactly. Each RingBuffer is
// cloned via RingBuffer.Clone so snapshot readers can't reach back
// into world-goroutine-owned storage.
//
// Allocation: O(K * capacity) per snapshot where K is the number of
// active (seller, item) keys. At v2 scale (~500 keys × 20 entries ×
// ~80 bytes) this is ~800KB per republish, comparable to the rest of
// Snapshot's clone cost.
func ClonePriceBook(in map[PriceBookKey]*RingBuffer[PriceObservation]) map[PriceBookKey]*RingBuffer[PriceObservation] {
	if len(in) == 0 {
		return nil
	}
	out := make(map[PriceBookKey]*RingBuffer[PriceObservation], len(in))
	for k, buf := range in {
		out[k] = buf.Clone()
	}
	return out
}
