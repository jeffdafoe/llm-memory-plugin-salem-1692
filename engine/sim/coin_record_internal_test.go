package sim

import (
	"context"
	"errors"
	"testing"
	"time"
)

// coin_record_internal_test.go — LLM-572. Unit coverage for the per-pair coin
// tally: the write path, the window, the cap, the read, the sweep, and the boot
// seed's recipient resolution.

func newCoinWorld(window time.Duration) *World {
	w := &World{}
	w.Settings.CoinRecordWindow = window
	return w
}

// A payment lands on BOTH sides of the ordered pair — the payer's Paid and the
// payee's Received — so a lookup never has to reach across into the other actor's
// map. This is the property the render depends on: each actor reads its own record.
func TestRecordCoinPaid_WritesBothDirections(t *testing.T) {
	w := newCoinWorld(0)
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	w.RecordCoinPaid("moses", "gideon", 1, at, CoinPaymentUnstated)

	payer := w.CoinRecord["moses"]["gideon"]
	if payer == nil || len(payer.Paid) != 1 || payer.Paid[0].Amount != 1 {
		t.Fatalf("payer record = %+v, want one 1-coin Paid entry", payer)
	}
	if len(payer.Received) != 0 {
		t.Errorf("payer Received = %v, want empty", payer.Received)
	}
	payee := w.CoinRecord["gideon"]["moses"]
	if payee == nil || len(payee.Received) != 1 || payee.Received[0].Amount != 1 {
		t.Fatalf("payee record = %+v, want one 1-coin Received entry", payee)
	}
	if len(payee.Paid) != 0 {
		t.Errorf("payee Paid = %v, want empty", payee.Paid)
	}
}

// The due marker rides both directions of the pair (LLM-607). The keeper reads it
// off his Paid trail and the constable off his Received trail, and a marker written
// to only one of them would leave whichever man consulted the other side reading a
// settled levy as an open debt — which is precisely the pair this exists for.
func TestRecordCoinPaid_MarksTheDueOnBothSides(t *testing.T) {
	w := newCoinWorld(0)
	at := time.Date(2026, 8, 6, 12, 11, 0, 0, time.UTC)
	w.RecordCoinPaid("moses", "gideon", 1, at, CoinPaymentForDue)

	if p := w.CoinRecord["moses"]["gideon"].Paid; len(p) != 1 || p[0].Kind != CoinPaymentForDue {
		t.Errorf("payer Paid = %+v, want the entry marked as a due", p)
	}
	if r := w.CoinRecord["gideon"]["moses"].Received; len(r) != 1 || r[0].Kind != CoinPaymentForDue {
		t.Errorf("payee Received = %+v, want the entry marked as a due", r)
	}
}

// The tally separates the due portion from the rest of a direction, so a mixed
// history can say which part of it was a levy. Marking the whole direction on the
// strength of one due would tell an NPC that goods he really is owed were never
// owed at all — the same failure as the original, inverted.
func TestCoinDealingsFor_CountsTheDuePortion(t *testing.T) {
	w := newCoinWorld(0)
	at := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	w.RecordCoinPaid("moses", "gideon", 1, at, CoinPaymentForDue)                     // the rate
	w.RecordCoinPaid("moses", "gideon", 1, at.Add(time.Minute), CoinPaymentForDue)    // the rate again
	w.RecordCoinPaid("moses", "gideon", 4, at.Add(2*time.Minute), CoinPaymentForGoods) // a purchase
	w.republish()

	d := w.Published().CoinDealingsFor("moses", "gideon", at.Add(3*time.Minute))
	if d.PaidCount != 3 || d.PaidTotal != 6 {
		t.Fatalf("paid = %d payments / %d coins, want 3 / 6", d.PaidCount, d.PaidTotal)
	}
	if d.PaidDueCount != 2 || d.PaidDueTotal != 2 {
		t.Errorf("due = %d payments / %d coins, want 2 / 2 — the 4-coin purchase is not a levy",
			d.PaidDueCount, d.PaidDueTotal)
	}
	// The mirror carries the same split, since both sides read their own record.
	m := w.Published().CoinDealingsFor("gideon", "moses", at.Add(3*time.Minute))
	if m.ReceivedDueCount != 2 || m.ReceivedDueTotal != 2 {
		t.Errorf("received due = %d / %d, want 2 / 2", m.ReceivedDueCount, m.ReceivedDueTotal)
	}
}

// Nothing that is not a coin transfer is recorded. A barter settles for 0 and still
// writes a `paid` action-log row, which is exactly the row the boot seed reads — so
// if this gate were missing, a pure goods swap would render as money changing hands.
func TestRecordCoinPaid_IgnoresNonTransfers(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		payer, payee ActorID
		amount       int
	}{
		{"zero amount (barter)", "moses", "gideon", 0},
		{"negative amount", "moses", "gideon", -5},
		{"self pay", "moses", "moses", 5},
		{"empty payer", "", "gideon", 5},
		{"empty payee", "moses", "", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCoinWorld(0)
			w.RecordCoinPaid(tc.payer, tc.payee, tc.amount, at, CoinPaymentUnstated)
			if n := countCoinPairs(w.CoinRecord); n != 0 {
				t.Errorf("recorded %d pair(s), want 0", n)
			}
		})
	}
}

// Payments older than the window are dropped on write, and the cutoff is measured
// from the NEWEST entry rather than from the incoming one — so a late-arriving old
// payment cannot push the cutoff backwards and revive entries already outlived.
func TestRecordCoinPaid_PrunesToWindow(t *testing.T) {
	w := newCoinWorld(48 * time.Hour)
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	w.RecordCoinPaid("moses", "gideon", 1, base.Add(-100*time.Hour), CoinPaymentUnstated) // well outside
	w.RecordCoinPaid("moses", "gideon", 1, base.Add(-1*time.Hour), CoinPaymentUnstated)   // inside
	rec := w.CoinRecord["moses"]["gideon"]
	if len(rec.Paid) != 1 || !rec.Paid[0].At.Equal(base.Add(-1*time.Hour)) {
		t.Fatalf("Paid = %+v, want only the in-window entry", rec.Paid)
	}
	// A late-arriving older payment: it is inside the window relative to the newest
	// entry, so it survives, and it must NOT revive the 100h-old one (already gone)
	// nor evict the newest.
	w.RecordCoinPaid("moses", "gideon", 1, base.Add(-3*time.Hour), CoinPaymentUnstated)
	if len(rec.Paid) != 2 {
		t.Fatalf("Paid = %+v, want 2 entries", rec.Paid)
	}
	if !rec.Paid[0].At.Before(rec.Paid[1].At) {
		t.Errorf("Paid not chronological after out-of-order write: %+v", rec.Paid)
	}
}

// The per-pair cap evicts oldest-first and COUNTS what it evicted. The count is what
// turns the render from an exact figure into "at least", so a silent undercount
// behind a sentence that reads as exact is the failure this guards.
func TestRecordCoinPaid_CapCountsEvictions(t *testing.T) {
	w := newCoinWorld(0)
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	const extra = 3
	for i := 0; i < MaxCoinPaymentsPerPairDirection+extra; i++ {
		w.RecordCoinPaid("moses", "gideon", 1, base.Add(time.Duration(i)*time.Minute), CoinPaymentUnstated)
	}
	rec := w.CoinRecord["moses"]["gideon"]
	if len(rec.Paid) != MaxCoinPaymentsPerPairDirection {
		t.Errorf("Paid length = %d, want %d", len(rec.Paid), MaxCoinPaymentsPerPairDirection)
	}
	if rec.DroppedPaid != extra {
		t.Errorf("DroppedPaid = %d, want %d", rec.DroppedPaid, extra)
	}
	// Oldest went, newest stayed.
	if !rec.Paid[len(rec.Paid)-1].At.Equal(base.Add(time.Duration(MaxCoinPaymentsPerPairDirection+extra-1) * time.Minute)) {
		t.Errorf("newest entry was evicted: %+v", rec.Paid[len(rec.Paid)-1])
	}
	d := (&Snapshot{CoinRecord: w.CoinRecord, CoinRecordWindow: DefaultCoinRecordWindow}).
		CoinDealingsFor("moses", "gideon", base.Add(time.Hour))
	if !d.PaidAtLeast {
		t.Errorf("PaidAtLeast = false after an eviction — the render would state an exact undercount")
	}
}

// The read applies the window without mutating: perception runs off a published
// snapshot and must never write back through it, so an aged-out payment has to read
// as absent while still sitting in the slice.
func TestCoinDealingsFor_WindowIsAppliedAtRead(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	rec := &CoinPairRecord{Received: []CoinPayment{
		{At: now.Add(-10 * 24 * time.Hour), Amount: 5}, // outside a 7-day window
		{At: now.Add(-2 * 24 * time.Hour), Amount: 1},
		{At: now.Add(-1 * 24 * time.Hour), Amount: 1},
	}}
	snap := &Snapshot{
		CoinRecord:       map[ActorID]map[ActorID]*CoinPairRecord{"gideon": {"moses": rec}},
		CoinRecordWindow: DefaultCoinRecordWindow,
	}
	d := snap.CoinDealingsFor("gideon", "moses", now)
	if d.ReceivedCount != 2 || d.ReceivedTotal != 2 {
		t.Errorf("ReceivedCount/Total = %d/%d, want 2/2 (the 10-day-old 5 coins are outside the window)", d.ReceivedCount, d.ReceivedTotal)
	}
	if !d.ReceivedAllSingle {
		t.Errorf("ReceivedAllSingle = false, want true — both in-window payments are single coins")
	}
	if len(rec.Received) != 3 {
		t.Errorf("read mutated the record: len = %d, want 3", len(rec.Received))
	}
	// This is the live shape the ticket turns on: two coins in, nothing back out.
	if d.PaidCount != 0 {
		t.Errorf("PaidCount = %d, want 0", d.PaidCount)
	}
	if !d.Any() {
		t.Errorf("Any() = false with two payments on record")
	}
}

// An unknown pair reads as empty rather than panicking — the common case, since most
// acquaintances have never exchanged coin and every one of them is looked up.
func TestCoinDealingsFor_UnknownPairIsEmpty(t *testing.T) {
	var nilSnap *Snapshot
	if nilSnap.CoinDealingsFor("a", "b", time.Now()).Any() {
		t.Errorf("nil snapshot should read empty")
	}
	snap := &Snapshot{CoinRecord: map[ActorID]map[ActorID]*CoinPairRecord{"a": {}}}
	if snap.CoinDealingsFor("a", "b", time.Now()).Any() {
		t.Errorf("unknown peer should read empty")
	}
	if snap.CoinDealingsFor("z", "b", time.Now()).Any() {
		t.Errorf("unknown subject should read empty")
	}
}

// The sweep is reclamation only — it drops pairs the read would already report as
// empty, and leaves everything the window still covers. Without it the map grows for
// the life of the process, because a transient visitor gets a fresh ActorID per
// visit and this ledger covers visitors.
func TestSweepCoinRecord_DropsOnlyDeadPairs(t *testing.T) {
	w := newCoinWorld(48 * time.Hour)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	w.CoinRecord = map[ActorID]map[ActorID]*CoinPairRecord{
		"moses": {
			"gideon":        {Paid: []CoinPayment{{At: now.Add(-time.Hour), Amount: 1}}},
			"vstr-deadbeef": {Paid: []CoinPayment{{At: now.Add(-100 * time.Hour), Amount: 3}}},
		},
		"hannah": {
			"vstr-cafebabe": {Received: []CoinPayment{{At: now.Add(-100 * time.Hour), Amount: 2}}},
		},
	}
	sweepCoinRecord(w, now)
	if _, ok := w.CoinRecord["moses"]["gideon"]; !ok {
		t.Errorf("live pair was swept")
	}
	if _, ok := w.CoinRecord["moses"]["vstr-deadbeef"]; ok {
		t.Errorf("dead pair survived the sweep")
	}
	if _, ok := w.CoinRecord["hannah"]; ok {
		t.Errorf("subject with no live pairs left behind an empty map")
	}
}

// stubCoinRecordsRepo serves canned rows to the boot seed.
type stubCoinRecordsRepo struct {
	rows  []CoinPaymentRow
	err   error
	since time.Time
}

func (s *stubCoinRecordsRepo) LoadPaymentsSince(_ context.Context, since time.Time) ([]CoinPaymentRow, error) {
	s.since = since
	return s.rows, s.err
}

// The seed's whole risk is understating: a dropped row means a "no coin has passed"
// line about money that did pass. This pins each way a row can be dropped, and that
// the good rows still land.
//
// The unresolvable case is not hypothetical — it is every visitor. Measured against
// production before this was built: of 2,609 `paid` rows, 2,580 resolved to exactly
// one actor, 0 were ambiguous, and all 29 that resolved to nobody were visitors
// whose actor rows are deleted at cleanup.
func TestRehydrateCoinRecordOnLoad_ResolvesAndSkips(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	w := newCoinWorld(0)
	w.Actors = map[ActorID]*Actor{
		"moses":  {ID: "moses", DisplayName: "Moses James"},
		"gideon": {ID: "gideon", DisplayName: "Constable Gideon Marsh"},
		"twin-a": {ID: "twin-a", DisplayName: "John Ellis"},
		"twin-b": {ID: "twin-b", DisplayName: "John Ellis"},
	}
	w.repo.CoinRecords = &stubCoinRecordsRepo{rows: []CoinPaymentRow{
		// Resolves by name (a historical row, written before the id was stamped).
		{PayerID: "moses", At: at, Amount: "1", RecipientName: "Constable Gideon Marsh"},
		// Resolves by the stamped id, which wins over the name.
		{PayerID: "moses", At: at.Add(time.Minute), Amount: "1", RecipientActorID: "gideon", RecipientName: "Somebody Else"},
		// A visitor: no live actor by that name. Dropped.
		{PayerID: "moses", At: at, Amount: "4", RecipientName: "Daniel Holcomb the factor"},
		// Ambiguous: two actors share the name, so it identifies neither. Dropped
		// rather than attributed to a coin flip.
		{PayerID: "moses", At: at, Amount: "9", RecipientName: "John Ellis"},
		// A stamped id for an actor that no longer exists, but whose display name
		// still names exactly one live actor. RESOLVES via the fallback — see
		// TestRehydrateCoinRecordOnLoad_StaleStampedIDFallsBackToName.
		{PayerID: "moses", At: at, Amount: "7", RecipientActorID: "vstr-deadbeef", RecipientName: "Constable Gideon Marsh"},
		// A barter — zero coin, and this is a record of coin.
		{PayerID: "moses", At: at, Amount: "0", RecipientName: "Constable Gideon Marsh"},
		// Malformed payload rather than a boot failure.
		{PayerID: "moses", At: at, Amount: "not-a-number", RecipientName: "Constable Gideon Marsh"},
		{PayerID: "moses", At: at, Amount: "", RecipientName: "Constable Gideon Marsh"},
	}}
	if err := w.rehydrateCoinRecordOnLoad(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := w.CoinRecord["moses"]["gideon"]
	if rec == nil || len(rec.Paid) != 3 {
		t.Fatalf("Paid = %+v, want the two 1-coin rows plus the stale-id row recovered by name", rec)
	}
	if n := countCoinPairs(w.CoinRecord); n != 2 {
		t.Errorf("pairs = %d, want 2 (moses→gideon and its mirror)", n)
	}
	// Nothing was attributed to either John Ellis.
	if _, ok := w.CoinRecord["moses"]["twin-a"]; ok {
		t.Errorf("ambiguous row was attributed to twin-a")
	}
	if _, ok := w.CoinRecord["moses"]["twin-b"]; ok {
		t.Errorf("ambiguous row was attributed to twin-b")
	}
}

// A payment's kind survives a restart (LLM-607, LLM-612). Neither marker is
// checkpointed anywhere — both are reconstructed from the durable payload, the due
// from rate_settled and goods from the presence of ledger_id — so a seed that
// ignored either field would leave every payment older than the current boot in the
// wrong register. Salem restarts several times a day, so "correct until the next
// deploy" is the same as wrong.
//
// A row carrying neither marker, or an unparseable rate_settled, reads as Unstated.
// That is the fallback the record wants: it degrades to the wording used before any
// classification existed rather than guessing, and no row is ever dropped for it.
func TestRehydrateCoinRecordOnLoad_RestoresThePaymentKind(t *testing.T) {
	at := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	w := newCoinWorld(0)
	w.Actors = map[ActorID]*Actor{
		"moses":  {ID: "moses", DisplayName: "Moses James"},
		"gideon": {ID: "gideon", DisplayName: "Constable Gideon Marsh"},
	}
	w.repo.CoinRecords = &stubCoinRecordsRepo{rows: []CoinPaymentRow{
		{PayerID: "moses", At: at, Amount: "1", RecipientActorID: "gideon", RateSettled: "1"},
		{PayerID: "moses", At: at.Add(time.Minute), Amount: "4", RecipientActorID: "gideon"},
		{PayerID: "moses", At: at.Add(2 * time.Minute), Amount: "2", RecipientActorID: "gideon", RateSettled: "nonsense"},
		{PayerID: "moses", At: at.Add(3 * time.Minute), Amount: "3", RecipientActorID: "gideon", LedgerID: "4127"},
		// rate_settled 0 is a bare pay that settled nothing, not a due.
		{PayerID: "moses", At: at.Add(4 * time.Minute), Amount: "5", RecipientActorID: "gideon", RateSettled: "0"},
	}}
	if err := w.rehydrateCoinRecordOnLoad(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	paid := w.CoinRecord["moses"]["gideon"].Paid
	if len(paid) != 5 {
		t.Fatalf("Paid = %+v, want all five rows — a malformed marker must not drop the payment", paid)
	}
	want := []CoinPaymentKind{
		CoinPaymentForDue,
		CoinPaymentUnstated,
		CoinPaymentUnstated,
		CoinPaymentForGoods,
		CoinPaymentUnstated,
	}
	for i, wantKind := range want {
		if paid[i].Kind != wantKind {
			t.Errorf("Paid[%d].Kind = %v, want %v", i, paid[i].Kind, wantKind)
		}
	}
}

// The goods marker is NOT forward-only, and that is the property worth pinning:
// ledger_id has been stamped by handlePayResolvedActionLog since LLM-105, so the
// seed reads a correct purchase history off rows written long before LLM-612
// existed. A row from that era carries no rate_settled at all — the due marker's
// own forward-only gap — and must still classify as goods rather than falling to
// Unstated on the strength of the missing sibling key.
func TestRehydrateCoinRecordOnLoad_ClassifiesHistoricalLedgerRowsAsGoods(t *testing.T) {
	at := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	w := newCoinWorld(0)
	w.Actors = map[ActorID]*Actor{
		"josiah":    {ID: "josiah", DisplayName: "Josiah Thorne"},
		"elizabeth": {ID: "elizabeth", DisplayName: "Elizabeth Ellis"},
	}
	w.repo.CoinRecords = &stubCoinRecordsRepo{rows: []CoinPaymentRow{
		// Pre-LLM-572 shape: no recipient_actor_id either, resolved by name.
		{PayerID: "josiah", At: at, Amount: "12", RecipientName: "Elizabeth Ellis", LedgerID: "881"},
	}}
	if err := w.rehydrateCoinRecordOnLoad(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	paid := w.CoinRecord["josiah"]["elizabeth"].Paid
	if len(paid) != 1 || paid[0].Kind != CoinPaymentForGoods {
		t.Fatalf("Paid = %+v, want one entry classified as goods", paid)
	}
}

// The kinds a row can present, at the boundaries coinPaymentKindFromRow decides on.
//
// The both-markers case cannot arise today — settleTownRate is reachable only from
// the bare-coin Pay command, which mints no ledger entry — so the precedence is
// pinned against a future path that produced both (code_review). The due wins: it is
// what stops a levy reading as an order placed and never filled, while a purchase
// left unnamed costs only the register.
//
// The lodging row is the third ActionTypePaid writer (sim/lodger_rebook.go). Since
// LLM-615 it carries lodging_grant and reads as goods: the rebook takes the night's
// rate and extends the room grant in one command, so the occupancy is delivered
// against the coin, and a stay bought by hand settles through the ledger as goods
// already. A lodging row written BEFORE that ticket carries no marker and must still
// read Unstated — the forward-only half, and the state of every such row in
// production.
func TestCoinPaymentKindFromRow(t *testing.T) {
	cases := []struct {
		name string
		row  CoinPaymentRow
		want CoinPaymentKind
	}{
		{"a ledger settlement", CoinPaymentRow{LedgerID: "881"}, CoinPaymentForGoods},
		{"a settled rate", CoinPaymentRow{RateSettled: "1"}, CoinPaymentForDue},
		{"both markers — the due wins", CoinPaymentRow{RateSettled: "1", LedgerID: "881"}, CoinPaymentForDue},
		{"a rate that settled nothing", CoinPaymentRow{RateSettled: "0"}, CoinPaymentUnstated},
		{"an unparseable rate", CoinPaymentRow{RateSettled: "nonsense"}, CoinPaymentUnstated},
		{"an unparseable rate on a ledger row", CoinPaymentRow{RateSettled: "nonsense", LedgerID: "881"}, CoinPaymentForGoods},
		{"a nightly lodging auto-charge", CoinPaymentRow{RecipientName: "John Ellis", LodgingGrant: "tavern"}, CoinPaymentForGoods},
		{"a lodging row from before the marker", CoinPaymentRow{RecipientName: "John Ellis"}, CoinPaymentUnstated},
		{"a due settled on a lodging row — the due still wins", CoinPaymentRow{RateSettled: "1", LodgingGrant: "tavern"}, CoinPaymentForDue},
		{"whitespace is not a ledger id", CoinPaymentRow{LedgerID: "  "}, CoinPaymentUnstated},
		{"whitespace is not a lodging grant", CoinPaymentRow{LodgingGrant: "  "}, CoinPaymentUnstated},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := coinPaymentKindFromRow(tc.row); got != tc.want {
				t.Errorf("coinPaymentKindFromRow(%+v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}

// The seed asks only for the window it will keep — a full-table scan of an
// append-only audit log that runs back months would grow without bound as the
// village ages.
func TestRehydrateCoinRecordOnLoad_ScopesQueryToWindow(t *testing.T) {
	w := newCoinWorld(48 * time.Hour)
	stub := &stubCoinRecordsRepo{}
	w.repo.CoinRecords = stub
	if err := w.rehydrateCoinRecordOnLoad(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Measure from AFTER the call, not before it. rehydrateCoinRecordOnLoad takes
	// its own time.Now() and subtracts the window, so that instant is strictly
	// later than any clock read before the call — measuring from before gives
	// 48h minus the elapsed time, i.e. always a hair UNDER the window, and the
	// lower bound can never hold. It survived local runs because Windows' coarse
	// clock often returns the same instant twice; Linux CI has nanosecond
	// resolution and failed every run (LLM-572, main red since 9a8d9c01).
	after := time.Now()
	if gap := after.Sub(stub.since); gap < 48*time.Hour || gap > 49*time.Hour {
		t.Errorf("queried since %v (%v before now), want ~48h", stub.since, gap)
	}
}

// A partially-wired repo is "no history", not a panic — the same nil-repo tolerance
// the rest of the loader has, and what keeps every catalog-only test working.
func TestRehydrateCoinRecordOnLoad_NilRepoIsEmpty(t *testing.T) {
	w := newCoinWorld(0)
	if err := w.rehydrateCoinRecordOnLoad(context.Background()); err != nil {
		t.Fatalf("nil repo: %v", err)
	}
	if w.CoinRecord == nil {
		t.Errorf("CoinRecord left nil — later writes would have to allocate defensively")
	}
}

// A repo error fails the boot rather than quietly starting with an empty record.
// Silently empty is the dangerous state: every pair would read "no coin has passed
// between you" and every NPC would deny debts it really owes.
func TestRehydrateCoinRecordOnLoad_PropagatesError(t *testing.T) {
	w := newCoinWorld(0)
	w.repo.CoinRecords = &stubCoinRecordsRepo{err: errors.New("boom")}
	if err := w.rehydrateCoinRecordOnLoad(context.Background()); err == nil {
		t.Fatalf("want error, got nil")
	}
}

// CloneCoinRecord must not share backing arrays: the snapshot is walked off the
// world goroutine while the world keeps appending.
func TestCloneCoinRecord_IsDeep(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	w := newCoinWorld(0)
	w.RecordCoinPaid("moses", "gideon", 1, at, CoinPaymentUnstated)
	clone := CloneCoinRecord(w.CoinRecord)
	w.RecordCoinPaid("moses", "gideon", 5, at.Add(time.Minute), CoinPaymentForGoods)
	if got := len(clone["moses"]["gideon"].Paid); got != 1 {
		t.Errorf("clone saw a later append: len = %d, want 1", got)
	}
	if CloneCoinRecord(nil) != nil {
		t.Errorf("CloneCoinRecord(nil) should stay nil")
	}
}

// The published snapshot has to CARRY the record, deep-cloned, or perception reads
// an empty one and every pair renders "no coin has passed between you". A field
// added to World and to Snapshot but never copied between them fails silently and
// in the dangerous direction, so this pins the wiring rather than the clone alone.
func TestRepublishCarriesTheCoinRecord(t *testing.T) {
	w := &World{}
	w.Actors = map[ActorID]*Actor{}
	w.RecordCoinPaid("moses", "gideon", 1, time.Now().Add(-time.Hour), CoinPaymentUnstated)
	w.republish()
	snap := w.Published()
	if snap == nil {
		t.Fatal("republish published nothing")
	}
	if snap.CoinRecordWindow != DefaultCoinRecordWindow {
		t.Errorf("CoinRecordWindow = %v, want the default — a zero window would make every read fall back", snap.CoinRecordWindow)
	}
	d := snap.CoinDealingsFor("gideon", "moses", time.Now())
	if d.ReceivedCount != 1 {
		t.Errorf("published snapshot reads %+v, want the one payment", d)
	}
}

// A STALE stamped recipient id falls through to the display name rather than
// dropping the row (code_review, LLM-572). Dropping there would produce "no coin has
// passed between you" about money the durable record plainly holds — the exact
// failure the mechanism exists to prevent — whenever the name still identifies one
// live actor.
func TestRehydrateCoinRecordOnLoad_StaleStampedIDFallsBackToName(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	w := newCoinWorld(0)
	w.Actors = map[ActorID]*Actor{
		"moses":  {ID: "moses", DisplayName: "Moses James"},
		"gideon": {ID: "gideon", DisplayName: "Constable Gideon Marsh"},
	}
	w.repo.CoinRecords = &stubCoinRecordsRepo{rows: []CoinPaymentRow{
		{PayerID: "moses", At: at, Amount: "2", RecipientActorID: "gone-forever", RecipientName: "Constable Gideon Marsh"},
	}}
	if err := w.rehydrateCoinRecordOnLoad(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := w.CoinRecord["moses"]["gideon"]
	if rec == nil || len(rec.Paid) != 1 || rec.Paid[0].Amount != 2 {
		t.Fatalf("Paid = %+v, want the row recovered by its display name", rec)
	}
}

// ...but the fallback keeps the uniqueness rule. A stale stamped id whose name is
// shared by two actors identifies neither, and mis-attributing money is worse than
// omitting it.
func TestRehydrateCoinRecordOnLoad_StaleStampedIDWithAmbiguousNameIsDropped(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	w := newCoinWorld(0)
	w.Actors = map[ActorID]*Actor{
		"moses":  {ID: "moses", DisplayName: "Moses James"},
		"twin-a": {ID: "twin-a", DisplayName: "John Ellis"},
		"twin-b": {ID: "twin-b", DisplayName: "John Ellis"},
	}
	w.repo.CoinRecords = &stubCoinRecordsRepo{rows: []CoinPaymentRow{
		{PayerID: "moses", At: at, Amount: "2", RecipientActorID: "gone-forever", RecipientName: "John Ellis"},
	}}
	if err := w.rehydrateCoinRecordOnLoad(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n := countCoinPairs(w.CoinRecord); n != 0 {
		t.Errorf("recorded %d pair(s), want 0 — an ambiguous name identifies nobody", n)
	}
}
