package sim

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// coin_record.go — LLM-572. What coin has actually passed between two actors,
// available in the scene where a money claim is made.
//
// The trigger: at the James Farm, 2026-07-30 19:00:22 UTC, Constable Gideon Marsh
// paid Moses James five coins to return "your coin — I did not earn it". Moses had
// never paid him five coins. Across the whole of agent_action_log — which is not
// pruned, and runs back to 2026-04-25 — the only money Moses ever handed the
// constable is two single coins of town rate. Five real coins moved on a history
// that never happened.
//
// Neither man could check. A shared-VA NPC sees only the consolidated per-peer
// SUMMARY in "## What you remember of those here" — a judgment, with the facts it
// was distilled from already consumed (consolidation prunes the trail it reads).
// A stateful NPC like Marsh sees less than that: buildRelationships is gated to
// KindNPCShared, so his Relationships are nil and he had no per-peer view at all.
// The record existed, in Postgres, in front of neither of them. The more confident
// account won, which is what happens in a vacuum.
//
// # What this is
//
// A per-ordered-pair tally of coin, both directions, over a one-week window. It is
// deliberately NOT a relationship, a judgment, or a narrative: no summary, no free
// text, no stated purpose. Two numbers per direction — how many payments and how
// much — which is exactly enough to refute "I paid you five coins" and nothing
// more. An NPC may still feel wronged, and should; it just can't invent the money.
//
// LLM-607 added one bit beside those numbers: whether the coin discharged a due
// (CoinPayment.Due). That is not the payer's purpose creeping back in — it is the
// engine's own record of what it did with the coin, and the distinction matters
// because the numbers alone turned out to be refutable in one direction only. They
// stopped Moses James inventing five coins he never paid; they did not stop him
// reading nine coins he really did pay as nine debts owing, and he collected eight
// of them back in refunds. A record that cannot say "this one was never going to
// come back" is a record that reads every levy as an unfilled order.
//
// # Durability by derivation, not by a table
//
// The live tally is in memory and there is no coin_record table. It is SEEDED AT
// BOOT from agent_action_log, which the engine already writes a durable `paid` row
// to on every settlement, and incremented from the same two events that write those
// rows. That is the whole durability story: the record survives restart because the
// table it is derived from does.
//
// Chosen over a new checkpointed aggregate (the contact_ledger.go shape, LLM-547)
// because the requirement here is genuinely weaker. The contact ledger had to be
// its own table — nothing else recorded who had spoken with whom. Coin already has
// a complete, unpruned, append-only durable record; a second copy would be a
// migration, a repo, a checkpoint writer and a reclamation sweep, all to store what
// one boot query can reconstruct. Precedent for the pattern is the price book,
// which seeds its ring from pay_ledger at LoadWorld the same way.
//
// # The one thing that can make this lie, and why it doesn't
//
// The seed resolves each row's recipient — the durable payload stores a display
// NAME (payload.recipient), not an id, for rows written before this ticket. A row
// whose recipient resolves to no actor, or to more than one, is DROPPED, and a
// dropped row means an understated tally. Understating is the dangerous direction,
// because "no coin has passed between you" is a claim.
//
// Measured against production before building it: of 2,609 `paid` rows, 2,580
// resolve to exactly one actor and ZERO are ambiguous. All 29 that resolve to
// nobody are visitors — Daniel Holcomb the factor, Ephraim Pollard the wool-buyer,
// and three others — whose actor rows are deleted at cleanup. That is the complete
// unresolvable set, and it is harmless in both directions:
//
//   - resident pays a visitor: the row drops, but the visitor is gone and a
//     transient actor id is minted fresh on the next visit, so that pair is never
//     read again.
//   - visitor pays a resident: the row exists but names no payer. LLM-573 stopped
//     discarding visitor-authored rows and now BLANKS their actor_id instead (the
//     column FKs to actor(id) and a visitor lives in a separate table), so the row
//     persists for the dream pipeline while carrying no id to key a pair on. The
//     seed's `actor_id IS NOT NULL` filter skips it.
//
// Both cases collapse to the same rule, which the render enforces rather than
// assumes: a pair is only read when both sides are resident actors the engine can
// still resolve. RecordCoinPaid itself covers everyone, so within a single boot a
// visitor's payments do count.
//
// Going forward the payload also carries recipient_actor_id, so seeds after this
// ticket resolve by id and the name lookup is only the historical fallback.

// CoinPayment is one settled coin transfer between a pair. Amount, time, and
// whether the coin discharged a due — no purpose text, no counterparty text.
//
// The omission of the payer's stated `for` is deliberate and is the point. That
// text is model-authored, it is re-read on every co-present turn once it is in the
// scene, and it is the specific thing that misled the reader in the trigger case:
// "Day's rate on the James Farm" is a payment with a stated purpose and no delivery
// against it, which is indistinguishable from an order placed and never filled.
// Carrying it here would re-import the ambiguity into the very cue built to settle
// it, and would put untrusted free text into a section rendered every tick.
//
// Due is the answer to that same ambiguity from the other side (LLM-607). Omitting
// the purpose stopped the cue REPEATING a misleading claim, but it did not let the
// cue REFUTE one: nine single coins with nothing coming back reads as nine unfilled
// orders whether or not the reason is quoted, and Moses James read it exactly that
// way and was refunded eight of the nine. Due is not the payer's account of the
// payment — it is settleTownRate's, i.e. what the engine itself did with the coin.
// That distinction is the whole reason it is allowed in a section this file
// otherwise keeps free of purpose: an engine-computed classification cannot be
// authored by a villager with a grievance.
type CoinPayment struct {
	At     time.Time
	Amount int
	// Due marks coin that discharged an obligation rather than buying anything —
	// today only the town rate, since that is the only levy a villager pays by
	// hand. A due is settled the moment it is handed over and owes no delivery,
	// which is the fact the record could not state before.
	Due bool
}

// CoinPairRecord is one ORDERED pair's coin history. Paid is what the subject
// handed the peer; Received is what came back. Both are held on the subject's own
// record rather than read off the peer's, so a lookup never has to reach across to
// another actor's map — the same reason the contact ledger keeps both directions.
type CoinPairRecord struct {
	// Paid / Received are oldest-first, capped at MaxCoinPaymentsPerPairDirection
	// and pruned to the recall window on write.
	Paid     []CoinPayment
	Received []CoinPayment

	// DroppedPaid / DroppedReceived count entries lost to the cap (never to the
	// window — an aged-out payment is not "dropped", it is outside the question
	// being asked). Non-zero softens the render from an exact count to "at least",
	// so the cue understates visibly instead of silently. Mirrors
	// Relationship.DroppedFactCount.
	DroppedPaid     int
	DroppedReceived int
}

// MaxCoinPaymentsPerPairDirection caps one direction of one pair. The whole village
// settles on the order of 27 payments a day across every pair; sixty-four in one
// direction between two actors inside a week is far outside anything observed, so
// this is a bound on pathology, not a working limit. When it does bite, the
// Dropped counters keep the render honest.
const MaxCoinPaymentsPerPairDirection = 64

// DefaultCoinRecordWindow is how far back coin is remembered for this cue.
//
// One week, matching how the levy that prompted the ticket accrues: the town rate
// is assessed once per game-day, so a week is the natural unit for "he has paid me
// twice" to mean something. Long enough that a dispute about a few days ago is
// answerable, short enough that the record stays a recollection rather than an
// audit — an NPC should not be reciting a season of accounts at a neighbour.
const DefaultCoinRecordWindow = 7 * 24 * time.Hour

// CoinRecordWindow reads the tunable off settings, falling back to the default when
// unset so a world built without the environment loader (every unit test) behaves
// sensibly. Same shape as ContactBrakeWindow / ContactRecallHorizon.
func (w *World) CoinRecordWindow() time.Duration {
	if w.Settings.CoinRecordWindow > 0 {
		return w.Settings.CoinRecordWindow
	}
	return DefaultCoinRecordWindow
}

// RecordCoinPaid credits one settled coin transfer to both sides' records.
//
// Called from the cascade action-log subscribers, on the same two events and under
// the same conditions that write the durable `paid` rows this ledger is seeded
// from. That co-location settles the question the seed cannot answer for itself —
// WHAT COUNTS as a payment — by making one place decide it for both. A future
// settlement path that logs a `paid` row must call this too, or the tally will be
// right until the next restart and then quietly change.
//
// It is NOT atomic with the durable append, and does not try to be (code_review,
// LLM-572). AppendActionLogDurable is a deliberately non-blocking enqueue onto an
// async writer goroutine, so the world goroutine never waits on Postgres; gating
// this call on a confirmed write would either block that goroutine or need a
// completion callback, which is a large change for a soft recollection cue. The two
// exposures, both accepted:
//
//   - a durable append that is rejected, fails to enqueue, or errors on the writer
//     goroutine leaves the tally holding a payment the next restart will not see, so
//     that pair understates by one. This is the restart-lossy class the GUIDELINES
//     permit — the coin itself moved in the Pay command and is checkpointed, so
//     persistent state stays consistent; what degrades is a recollection.
//
//     The due marker shares that fate EXACTLY and adds no new exposure (code_review,
//     LLM-607). It is one key of the same payload of the same row as the amount, so
//     a lost append loses the payment and its classification together; the divergence
//     is always a pair that understates, never one that misclassifies. Pinned by
//     cascade.TestHandlePaidActionLog_RejectedAppendLosesThePaymentNotItsMeaning.
//     The one path that yields a payment WITHOUT a marker is a row written before
//     this ticket, which is deliberate and documented on CoinPaymentRow.RateSettled.
//
//   - a visitor's payment credits the tally but reaches the durable log with a
//     BLANKED actor id (LLM-573), so the seed cannot key it to a pair. That one is
//     closed rather than accepted: perception excludes travelers on both sides
//     precisely because their durable record cannot be attributed, so no pair that
//     renders is affected.
//
// Zero and negative amounts are dropped — a barter settles for 0 coin and writes a
// `paid` row, but no coin passed and this is a record of coin. Self-pairs and empty
// ids are dropped rather than erroring; this runs after the transfer has already
// committed, so there is nothing useful a caller could do with an error.
//
// CLOCK CONTRACT: `at` is the engine's tick time and the caller owns its validity,
// the same contract RecordContact documents and for the same reasons.
//
// `due` marks coin that discharged an obligation (LLM-607). It is the CALLER's
// classification and must come from what the engine did — settleTownRate's return
// — never from the payer's stated purpose, which is the untrusted half.
func (w *World) RecordCoinPaid(payerID, payeeID ActorID, amount int, at time.Time, due bool) {
	if w == nil || amount <= 0 || payerID == "" || payeeID == "" || payerID == payeeID {
		return
	}
	payment := CoinPayment{At: at, Amount: amount, Due: due}
	w.coinPairRecord(payerID, payeeID).appendPaid(payment, w.CoinRecordWindow())
	w.coinPairRecord(payeeID, payerID).appendReceived(payment, w.CoinRecordWindow())
}

// coinPairRecord returns the record for an ordered pair, creating it if absent.
func (w *World) coinPairRecord(subjectID, peerID ActorID) *CoinPairRecord {
	if w.CoinRecord == nil {
		w.CoinRecord = make(map[ActorID]map[ActorID]*CoinPairRecord)
	}
	byPeer, ok := w.CoinRecord[subjectID]
	if !ok {
		byPeer = make(map[ActorID]*CoinPairRecord)
		w.CoinRecord[subjectID] = byPeer
	}
	rec, ok := byPeer[peerID]
	if !ok {
		rec = &CoinPairRecord{}
		byPeer[peerID] = rec
	}
	return rec
}

func (r *CoinPairRecord) appendPaid(p CoinPayment, window time.Duration) {
	r.Paid, r.DroppedPaid = appendCoinPayment(r.Paid, r.DroppedPaid, p, window)
}

func (r *CoinPairRecord) appendReceived(p CoinPayment, window time.Duration) {
	r.Received, r.DroppedReceived = appendCoinPayment(r.Received, r.DroppedReceived, p, window)
}

// appendCoinPayment adds one payment to a direction, restoring chronological order,
// pruning to the window and enforcing the cap. Returns the new slice and dropped
// count.
//
// Sorts before pruning for the reason RecordContact does: both prune steps select
// by POSITION, so on an unsorted trail the window scan stops early and the cap
// discards the newest entries instead of the oldest. The seed feeds rows in
// occurred_at order and the live path is in order, so the sort is a walk of an
// already-sorted slice in every real case.
//
// The window is measured from the NEWEST entry rather than from the incoming one:
// for a late-arriving older payment the incoming `at` is not "now", and using it
// would push the cutoff backwards and revive entries the record had already
// outlived.
func appendCoinPayment(trail []CoinPayment, dropped int, p CoinPayment, window time.Duration) ([]CoinPayment, int) {
	trail = append(trail, p)
	if len(trail) > 1 && trail[len(trail)-1].At.Before(trail[len(trail)-2].At) {
		sort.SliceStable(trail, func(i, j int) bool { return trail[i].At.Before(trail[j].At) })
	}
	cutoff := trail[len(trail)-1].At.Add(-window)
	first := 0
	for first < len(trail) && trail[first].At.Before(cutoff) {
		first++
	}
	if first > 0 {
		// Aged out of the window, not lost to the cap — the question only ever
		// asks about the window, so these are not counted as dropped.
		trail = append(trail[:0], trail[first:]...)
	}
	if len(trail) > MaxCoinPaymentsPerPairDirection {
		excess := len(trail) - MaxCoinPaymentsPerPairDirection
		dropped += excess
		trail = append(trail[:0], trail[excess:]...)
	}
	return trail, dropped
}

// CoinDealings is the engine-computed answer the scene is rendered from: what
// passed between one ordered pair inside the window. Derived world-side so render
// only selects phrasing — the felt-needs division of labour.
//
// AtLeast marks a direction whose count understates because the cap evicted
// entries. It is never set by the window.
//
// DueCount / DueTotal are the subset of a direction that discharged a due rather
// than buying anything (LLM-607) — the difference between coin owed to the town and
// coin handed over for goods that never came.
type CoinDealings struct {
	PaidCount     int
	PaidTotal     int
	PaidAtLeast   bool
	PaidAllSingle bool
	PaidDueCount  int
	PaidDueTotal  int

	ReceivedCount     int
	ReceivedTotal     int
	ReceivedAtLeast   bool
	ReceivedAllSingle bool
	ReceivedDueCount  int
	ReceivedDueTotal  int
}

// Any reports whether any coin passed either way inside the window.
func (d CoinDealings) Any() bool { return d.PaidCount > 0 || d.ReceivedCount > 0 }

// CoinDealingsFor derives the pair's dealings off a published snapshot as of `now`
// — the form perception uses.
//
// Reads without mutating: perception builds off a published snapshot and must never
// write back through it, so the window is applied here rather than by pruning. A
// record whose payments have all aged out reads as empty until the next write (or
// the daily sweep) prunes it.
func (s *Snapshot) CoinDealingsFor(subjectID, peerID ActorID, now time.Time) CoinDealings {
	if s == nil || s.CoinRecord == nil {
		return CoinDealings{}
	}
	byPeer, ok := s.CoinRecord[subjectID]
	if !ok {
		return CoinDealings{}
	}
	rec := byPeer[peerID]
	if rec == nil {
		return CoinDealings{}
	}
	window := s.CoinRecordWindow
	if window <= 0 {
		window = DefaultCoinRecordWindow
	}
	cutoff := now.Add(-window)
	var d CoinDealings
	paid := tallyCoinPayments(rec.Paid, cutoff)
	received := tallyCoinPayments(rec.Received, cutoff)
	d.PaidCount, d.PaidTotal, d.PaidAllSingle = paid.count, paid.total, paid.allSingle
	d.PaidDueCount, d.PaidDueTotal = paid.dueCount, paid.dueTotal
	d.ReceivedCount, d.ReceivedTotal, d.ReceivedAllSingle = received.count, received.total, received.allSingle
	d.ReceivedDueCount, d.ReceivedDueTotal = received.dueCount, received.dueTotal
	d.PaidAtLeast = rec.DroppedPaid > 0
	d.ReceivedAtLeast = rec.DroppedReceived > 0
	return d
}

// coinTally is one direction's totals inside the window. A struct rather than a
// widening return list — five positional results is where a caller starts
// transposing two of them silently.
type coinTally struct {
	count int
	total int
	// allSingle reports whether every counted payment was a single coin — the
	// difference between "a coin each time" and "seven coins in all", which is the
	// phrasing a person would actually use about a recurring due.
	allSingle bool
	// dueCount / dueTotal are the subset that discharged an obligation.
	dueCount int
	dueTotal int
}

// tallyCoinPayments sums the entries at or after cutoff.
func tallyCoinPayments(trail []CoinPayment, cutoff time.Time) coinTally {
	t := coinTally{allSingle: true}
	for _, p := range trail {
		if p.At.Before(cutoff) {
			continue
		}
		t.count++
		t.total += p.Amount
		if p.Amount != 1 {
			t.allSingle = false
		}
		if p.Due {
			t.dueCount++
			t.dueTotal += p.Amount
		}
	}
	if t.count == 0 {
		t.allSingle = false
	}
	return t
}

// CloneCoinRecord deep-copies the ledger for the published snapshot. Every slice is
// copied — a reader running off the world goroutine must not walk a backing array a
// later append can mutate. Same contract as CloneContactLedger.
func CloneCoinRecord(src map[ActorID]map[ActorID]*CoinPairRecord) map[ActorID]map[ActorID]*CoinPairRecord {
	if src == nil {
		return nil
	}
	out := make(map[ActorID]map[ActorID]*CoinPairRecord, len(src))
	for subjectID, byPeer := range src {
		cloned := make(map[ActorID]*CoinPairRecord, len(byPeer))
		for peerID, rec := range byPeer {
			if rec == nil {
				continue
			}
			cp := &CoinPairRecord{
				Paid:            append([]CoinPayment(nil), rec.Paid...),
				Received:        append([]CoinPayment(nil), rec.Received...),
				DroppedPaid:     rec.DroppedPaid,
				DroppedReceived: rec.DroppedReceived,
			}
			cloned[peerID] = cp
		}
		out[subjectID] = cloned
	}
	return out
}

// sweepCoinRecord drops pairs with nothing left inside the window.
//
// The map is otherwise unbounded over a long uptime, and not because of residents:
// a transient visitor gets a FRESH actor id every visit and this ledger covers
// visitors on purpose, so the set of pairs that have ever traded grows even though
// the set of live actors does not. The contact ledger solves the same problem with
// a horizon-predicate DELETE inside its checkpoint; this one has no table to sweep,
// so it sweeps itself.
//
// Called once per game-day from checkAndRotate, beside assessTownRate — the same
// durable rotation boundary, and far more often than the bound needs.
func sweepCoinRecord(w *World, now time.Time) {
	if w == nil || w.CoinRecord == nil {
		return
	}
	cutoff := now.Add(-w.CoinRecordWindow())
	for subjectID, byPeer := range w.CoinRecord {
		for peerID, rec := range byPeer {
			if rec == nil || !rec.aliveAt(cutoff) {
				delete(byPeer, peerID)
			}
		}
		if len(byPeer) == 0 {
			delete(w.CoinRecord, subjectID)
		}
	}
}

// SweepCoinRecord wraps the reclamation pass as a Command so the rotation driver
// can run it on the world goroutine. Mirrors ApplyTownRate / ApplyFarmUpkeep.
func SweepCoinRecord(now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		sweepCoinRecord(w, now)
		return nil, nil
	}}
}

// aliveAt reports whether either direction still holds a payment inside the window.
// Both trails are chronological, so the last entry is the newest.
func (r *CoinPairRecord) aliveAt(cutoff time.Time) bool {
	if n := len(r.Paid); n > 0 && !r.Paid[n-1].At.Before(cutoff) {
		return true
	}
	n := len(r.Received)
	return n > 0 && !r.Received[n-1].At.Before(cutoff)
}

// CoinPaymentRow is one durable `paid` action-log row as the seed reads it, before
// the recipient is resolved to an actor. The repo layer returns this shape; the
// resolution is Go's, not SQL's.
type CoinPaymentRow struct {
	PayerID ActorID
	At      time.Time
	// Amount is the raw payload text, parsed here rather than cast in SQL. The
	// column is jsonb and a single malformed value would otherwise fail the cast
	// and take the whole boot query with it.
	Amount string
	// RecipientActorID is set on rows written since LLM-572; empty on historical
	// rows, which resolve by RecipientName instead.
	RecipientActorID string
	RecipientName    string
	// RateSettled is the raw payload text for how much of the payment discharged
	// town-rate arrears (LLM-607), parsed here for the reason Amount is. Empty on
	// every purchase and on every row written before that ticket; both read back as
	// an ordinary payment, which is the safe direction — a due mistaken for a
	// purchase is the state the village was already in, while a purchase mistaken
	// for a due would tell an NPC nothing was owed him when something was.
	RateSettled string
}

// rehydrateCoinRecordOnLoad rebuilds World.CoinRecord at boot from the durable
// action log (FinalizeLoad).
//
// A partially-wired repo (catalog-only loads, tests that hand-build a
// sim.Repository without this tier) leaves CoinRecords nil — treated as "no
// history" rather than panicking, matching the loader's nil-repo tolerance
// elsewhere.
func (w *World) rehydrateCoinRecordOnLoad(ctx context.Context) error {
	if w.repo.CoinRecords == nil {
		if w.CoinRecord == nil {
			w.CoinRecord = make(map[ActorID]map[ActorID]*CoinPairRecord)
		}
		return nil
	}
	now := time.Now()
	rows, err := w.repo.CoinRecords.LoadPaymentsSince(ctx, now.Add(-w.CoinRecordWindow()))
	if err != nil {
		return err
	}
	w.CoinRecord = make(map[ActorID]map[ActorID]*CoinPairRecord)
	byName := actorIDsByDisplayName(w.Actors)
	var unresolved, ambiguous, malformed int
	for _, row := range rows {
		amount, convErr := strconv.Atoi(strings.TrimSpace(row.Amount))
		if convErr != nil || amount <= 0 {
			malformed++
			continue
		}
		payeeID, ok := resolveCoinRecipient(row, w.Actors, byName)
		if !ok {
			// Overwhelmingly a visitor whose actor row was deleted at cleanup —
			// see the file header. Counted, not logged per row.
			if row.RecipientName != "" && len(byName[strings.TrimSpace(row.RecipientName)]) > 1 {
				ambiguous++
			} else {
				unresolved++
			}
			continue
		}
		// A malformed rate_settled reads as "not a due" rather than failing the row
		// (LLM-607). The payment itself is still true and still belongs in the
		// tally; only its classification is lost, which degrades to exactly the
		// pre-ticket behaviour.
		settled, _ := strconv.Atoi(strings.TrimSpace(row.RateSettled))
		w.RecordCoinPaid(row.PayerID, payeeID, amount, row.At, settled > 0)
	}
	if pairs := countCoinPairs(w.CoinRecord); pairs > 0 || unresolved > 0 || ambiguous > 0 {
		log.Printf("sim: seeded coin record for %d pair(s) from %d durable payment row(s) (%d recipient(s) unresolved, %d ambiguous, %d malformed)",
			pairs, len(rows), unresolved, ambiguous, malformed)
	}
	return nil
}

// resolveCoinRecipient maps a durable row's recipient onto a live actor. Prefers
// the stamped id (rows written since LLM-572), then falls back to an exact
// display-name match, which must be UNIQUE — a name shared by two actors identifies
// neither, and mis-attributing money is worse than omitting it.
//
// The fallback runs for a STALE stamped id too, not only for an absent one. A
// stamped id naming an actor that no longer exists carries no information, and
// refusing there would drop a row whose display name still identifies exactly one
// live actor — producing "no coin has passed between you" about money the durable
// record plainly holds, which is the failure this whole mechanism exists to prevent
// (code_review, LLM-572). Falling through costs nothing: the name path applies the
// same uniqueness rule, so an ambiguous name is still refused.
func resolveCoinRecipient(row CoinPaymentRow, actors map[ActorID]*Actor, byName map[string][]ActorID) (ActorID, bool) {
	if id := ActorID(strings.TrimSpace(row.RecipientActorID)); id != "" {
		if _, ok := actors[id]; ok {
			return id, true
		}
	}
	ids := byName[strings.TrimSpace(row.RecipientName)]
	if len(ids) != 1 {
		return "", false
	}
	return ids[0], true
}

// actorIDsByDisplayName indexes live actors by display name for the seed's
// historical fallback. Returns every id per name so the caller can refuse an
// ambiguous match rather than pick one.
func actorIDsByDisplayName(actors map[ActorID]*Actor) map[string][]ActorID {
	out := make(map[string][]ActorID, len(actors))
	for id, a := range actors {
		if a == nil {
			continue
		}
		name := strings.TrimSpace(a.DisplayName)
		if name == "" {
			continue
		}
		out[name] = append(out[name], id)
	}
	return out
}

// countCoinPairs totals the ordered pairs held, for the seed's log line.
func countCoinPairs(rec map[ActorID]map[ActorID]*CoinPairRecord) int {
	n := 0
	for _, byPeer := range rec {
		n += len(byPeer)
	}
	return n
}
