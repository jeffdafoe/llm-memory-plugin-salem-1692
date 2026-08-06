package sim

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// lodger_rebook_test.go — ZBBS-HOME-296 PR2. Exercises the engine-auto
// rebook Command directly on a hand-built World (no goroutine / repo).

var rebookNow = time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

func rebookTestWorld(weeklyRate, checkOut int, actors ...*Actor) *World {
	m := make(map[ActorID]*Actor, len(actors))
	for _, a := range actors {
		m[a.ID] = a
	}
	return &World{
		Actors: m,
		Structures: map[StructureID]*Structure{
			"inn": {ID: "inn", DisplayName: "Hannah's Inn", Rooms: []*Room{
				{ID: 1, StructureID: "inn", Kind: RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: "inn", Kind: RoomKindPrivate, Name: "bedroom_1"},
			}},
		},
		Settings: WorldSettings{
			Location:                 time.UTC,
			LodgingCheckOutHour:      checkOut,
			LodgingDefaultWeeklyRate: weeklyRate,
		},
	}
}

func rebookLodger(id ActorID, coins int, roomID RoomID, expiresAt time.Time) *Actor {
	exp := expiresAt
	// Present by default: LLM-450's free-hold only diverts an OFFLINE (presence-
	// stale) lodger off the paid path, so the paid-rebook tests seed a live
	// client. The offline case sets LastPCSeenAt stale explicitly.
	seen := rebookNow
	return &Actor{
		ID:           id,
		Kind:         KindPC, // only PCs auto-rebook (LLM-37)
		Coins:        coins,
		LastPCSeenAt: &seen,
		RoomAccess: map[RoomAccessKey]*RoomAccess{
			{RoomID: roomID, Source: AccessSourceLedger}: {
				RoomID: roomID, Source: AccessSourceLedger, Active: true, ExpiresAt: &exp, LedgerID: 1,
			},
		},
	}
}

func rebookKeeper(id ActorID) *Actor {
	return &Actor{ID: id, DisplayName: "Hannah", WorkStructureID: "inn"}
}

func runRebook(t *testing.T, w *World) RebookLodgersResult {
	t.Helper()
	res, err := RebookLodgersDue(rebookNow).Fn(w)
	if err != nil {
		t.Fatalf("RebookLodgersDue: %v", err)
	}
	return res.(RebookLodgersResult)
}

func TestRebook_RenewsWhenAffordable(t *testing.T) {
	lodger := rebookLodger("ezekiel", 10, 2, rebookNow.Add(3*time.Hour)) // in the 6h window
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper) // nightly = 4

	res := runRebook(t, w)

	if lodger.Coins != 6 {
		t.Errorf("lodger coins = %d, want 6 (10 - 4 nightly)", lodger.Coins)
	}
	if keeper.Coins != 4 {
		t.Errorf("keeper coins = %d, want 4", keeper.Coins)
	}
	wantExpiry := ComputeLodgerUntil(rebookNow.Add(3*time.Hour), 1, 11, time.UTC)
	got := *lodger.RoomAccess[RoomAccessKey{RoomID: 2, Source: AccessSourceLedger}].ExpiresAt
	if !got.Equal(wantExpiry) {
		t.Errorf("extended ExpiresAt = %v, want %v", got, wantExpiry)
	}
	if len(res.Renewals) != 1 {
		t.Fatalf("renewals = %d, want 1", len(res.Renewals))
	}
	if len(w.ActionLog) != 1 || w.ActionLog[0].ActionType != ActionTypePaid {
		t.Errorf("want one ActionTypePaid audit entry, got %+v", w.ActionLog)
	}
}

func TestRebook_LapsesWhenBroke(t *testing.T) {
	lodger := rebookLodger("ezekiel", 2, 2, rebookNow.Add(3*time.Hour)) // 2 < 4 nightly
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper)

	res := runRebook(t, w)

	if lodger.Coins != 2 {
		t.Errorf("lodger coins = %d, want 2 (unchanged — can't afford)", lodger.Coins)
	}
	if keeper.Coins != 0 {
		t.Errorf("keeper coins = %d, want 0", keeper.Coins)
	}
	orig := rebookNow.Add(3 * time.Hour)
	if got := *lodger.RoomAccess[RoomAccessKey{RoomID: 2, Source: AccessSourceLedger}].ExpiresAt; !got.Equal(orig) {
		t.Errorf("ExpiresAt = %v, want unchanged %v", got, orig)
	}
	if len(res.Renewals) != 0 || len(w.ActionLog) != 0 {
		t.Errorf("broke lodger must not renew: renewals=%d actionlog=%d", len(res.Renewals), len(w.ActionLog))
	}
}

func TestRebook_OfflineLodgerHeldFreeNotBilled(t *testing.T) {
	// LLM-450: an offline (presence-stale) lodger's room is FROZEN, not billed —
	// the grant is extended for free (no coin debit, no keeper credit, no audit)
	// so it never lapses while the player is away.
	lodger := rebookLodger("jefferey", 10, 2, rebookNow.Add(3*time.Hour)) // in the 6h window
	stale := rebookNow.Add(-time.Hour)                                    // > 40s stale threshold => offline
	lodger.LastPCSeenAt = &stale
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper) // nightly = 4

	res := runRebook(t, w)

	if lodger.Coins != 10 {
		t.Errorf("offline lodger coins = %d, want 10 (frozen — not billed)", lodger.Coins)
	}
	if keeper.Coins != 0 {
		t.Errorf("keeper coins = %d, want 0 (no charge for a held room)", keeper.Coins)
	}
	wantExpiry := ComputeLodgerUntil(rebookNow.Add(3*time.Hour), 1, 11, time.UTC)
	got := *lodger.RoomAccess[RoomAccessKey{RoomID: 2, Source: AccessSourceLedger}].ExpiresAt
	if !got.Equal(wantExpiry) {
		t.Errorf("held ExpiresAt = %v, want extended to %v (grant frozen, not lapsed)", got, wantExpiry)
	}
	if res.Holds != 1 {
		t.Errorf("Holds = %d, want 1 (one free hold)", res.Holds)
	}
	if len(res.Renewals) != 0 || len(w.ActionLog) != 0 {
		t.Errorf("a held (unbilled) room must not renew or audit: renewals=%d actionlog=%d", len(res.Renewals), len(w.ActionLog))
	}
}

func TestRebook_OutsideWindowUntouched(t *testing.T) {
	lodger := rebookLodger("ezekiel", 100, 2, rebookNow.Add(10*time.Hour)) // > 6h lead
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper)

	res := runRebook(t, w)

	if lodger.Coins != 100 || len(res.Renewals) != 0 {
		t.Errorf("grant outside the 6h window must be untouched: coins=%d renewals=%d", lodger.Coins, len(res.Renewals))
	}
}

func TestRebook_NoKeeperSkips(t *testing.T) {
	lodger := rebookLodger("ezekiel", 100, 2, rebookNow.Add(3*time.Hour))
	w := rebookTestWorld(28, 11, lodger) // no keeper actor

	res := runRebook(t, w)

	if lodger.Coins != 100 || len(res.Renewals) != 0 {
		t.Errorf("no keeper must skip: coins=%d renewals=%d", lodger.Coins, len(res.Renewals))
	}
}

func TestRebook_RateDisabledNoop(t *testing.T) {
	lodger := rebookLodger("ezekiel", 100, 2, rebookNow.Add(3*time.Hour))
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(6, 11, lodger, keeper) // weekly 6 → nightly 0 (disabled)

	res := runRebook(t, w)

	if lodger.Coins != 100 || keeper.Coins != 0 || len(res.Renewals) != 0 {
		t.Errorf("sub-7 weekly rate disables rebook: lodger=%d keeper=%d renewals=%d",
			lodger.Coins, keeper.Coins, len(res.Renewals))
	}
}

func TestRebook_Idempotent(t *testing.T) {
	lodger := rebookLodger("ezekiel", 100, 2, rebookNow.Add(3*time.Hour))
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper)

	runRebook(t, w) // first renews → pushes ExpiresAt to next day 11:00 (well past the 6h window)
	coinsAfterFirst := lodger.Coins
	res := runRebook(t, w) // second should no-op

	if lodger.Coins != coinsAfterFirst || len(res.Renewals) != 0 {
		t.Errorf("second sweep must no-op: coins %d->%d renewals=%d",
			coinsAfterFirst, lodger.Coins, len(res.Renewals))
	}
}

func TestRebook_ZeroNowRejected(t *testing.T) {
	lodger := rebookLodger("ezekiel", 100, 2, rebookNow.Add(3*time.Hour))
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper)

	_, err := RebookLodgersDue(time.Time{}).Fn(w)
	if err == nil {
		t.Fatal("zero now must be rejected before any work")
	}
	if lodger.Coins != 100 || keeper.Coins != 0 || len(w.ActionLog) != 0 {
		t.Errorf("zero-now must not mutate state: lodger=%d keeper=%d log=%d",
			lodger.Coins, keeper.Coins, len(w.ActionLog))
	}
}

func TestRebook_NilActorSkipped(t *testing.T) {
	lodger := rebookLodger("ezekiel", 100, 2, rebookNow.Add(3*time.Hour))
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper)
	w.Actors["ghost"] = nil // must not panic

	res := runRebook(t, w)
	if len(res.Renewals) != 1 {
		t.Errorf("a nil actor must be skipped, the real lodger still renews: renewals=%d", len(res.Renewals))
	}
}

func TestRebook_KeeperNotChargedAsOwnLodger(t *testing.T) {
	// The keeper holds a ledger grant for a room in their own structure. Mark the
	// keeper a PC so it clears the LLM-37 kind gate — this keeps the self-keeper
	// guard (keeperID == lodgerID) the reason it's skipped, not the kind gate.
	keeper := rebookKeeper("hannah")
	keeper.Kind = KindPC
	keeper.Coins = 100
	exp := rebookNow.Add(3 * time.Hour)
	keeper.RoomAccess = map[RoomAccessKey]*RoomAccess{
		{RoomID: 2, Source: AccessSourceLedger}: {RoomID: 2, Source: AccessSourceLedger, Active: true, ExpiresAt: &exp, LedgerID: 1},
	}
	w := rebookTestWorld(28, 11, keeper)

	res := runRebook(t, w)
	if keeper.Coins != 100 || len(res.Renewals) != 0 || len(w.ActionLog) != 0 {
		t.Errorf("keeper must not be auto-rebooked as their own lodger: coins=%d renewals=%d log=%d",
			keeper.Coins, len(res.Renewals), len(w.ActionLog))
	}
}

func TestRebook_NPCLodgerNotRenewed(t *testing.T) {
	// An NPC lodger in the renewal window with coins to spare must NOT be
	// auto-rebooked (LLM-37) — its grant lapses and relies on the keeper-LLM
	// renewal path. Ezekiel Crane is the live case.
	lodger := rebookLodger("ezekiel", 100, 2, rebookNow.Add(3*time.Hour))
	lodger.Kind = KindNPCStateful
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper)

	res := runRebook(t, w)

	if lodger.Coins != 100 || keeper.Coins != 0 || len(res.Renewals) != 0 || len(w.ActionLog) != 0 {
		t.Errorf("NPC lodger must not auto-rebook: lodger=%d keeper=%d renewals=%d log=%d",
			lodger.Coins, keeper.Coins, len(res.Renewals), len(w.ActionLog))
	}
}

// recordingActionLogSink captures durable rows so a test can assert the rebook
// wrote a visible audit record (the production sink is async PG; this is sync).
type recordingActionLogSink struct{ rows []DurableActionLogRow }

func (s *recordingActionLogSink) Append(_ context.Context, row DurableActionLogRow) error {
	s.rows = append(s.rows, row)
	return nil
}

// failingActionLogSink rejects every durable row, standing in for the async writer
// erroring or the enqueue being dropped.
type failingActionLogSink struct{}

func (failingActionLogSink) Append(context.Context, DurableActionLogRow) error {
	return errors.New("sink rejected")
}

func TestRebook_WritesDurableAudit(t *testing.T) {
	lodger := rebookLodger("jefferey", 10, 2, rebookNow.Add(3*time.Hour))
	lodger.CurrentHuddleID = "huddle-1" // the audit shape carries the lodger's huddle
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper) // nightly = 4
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)

	runRebook(t, w)

	// Lean ring entry carries the counterparty + amount so the talk panel /
	// umbilical narrate "<lodger> pays Hannah 4 coins for a night's lodging".
	if len(w.ActionLog) != 1 {
		t.Fatalf("want 1 lean ring entry, got %d", len(w.ActionLog))
	}
	e := w.ActionLog[0]
	if e.ActionType != ActionTypePaid || e.CounterpartyName != "Hannah" || e.Amount != 4 || e.Text != "a night's lodging" {
		t.Errorf("lean entry = %+v, want paid / Hannah / 4 / 'a night's lodging'", e)
	}
	if e.HuddleID != "huddle-1" {
		t.Errorf("lean HuddleID = %q, want huddle-1", e.HuddleID)
	}

	// Durable mirror to agent_action_log — the persistent, restart-surviving audit.
	if len(sink.rows) != 1 {
		t.Fatalf("want 1 durable row, got %d", len(sink.rows))
	}
	r := sink.rows[0]
	if r.ActorID != "jefferey" || r.ActionType != ActionTypePaid || r.Source != "engine" {
		t.Errorf("durable row = %+v, want actor jefferey / paid / source engine", r)
	}
	if r.HuddleID != "huddle-1" {
		t.Errorf("durable HuddleID = %q, want huddle-1", r.HuddleID)
	}
	// No DisplayName on the test lodger → speaker_name falls back to the id (the
	// NOT NULL column must never be blank).
	if r.SpeakerName != "jefferey" {
		t.Errorf("SpeakerName = %q, want id fallback 'jefferey'", r.SpeakerName)
	}
	if r.Payload["recipient"] != "Hannah" || r.Payload["amount"] != 4 || r.Payload["for"] != "a night's lodging" {
		t.Errorf("durable payload = %+v, want recipient Hannah / amount 4 / for 'a night's lodging'", r.Payload)
	}
	// The coin-record seed prefers the id over an exact-name match it has to drop
	// when ambiguous (LLM-572), and reads lodging_grant as the goods marker
	// (LLM-615). Both are on the row, so a restart rebuilds this pair the way the
	// live tally holds it.
	if r.Payload["recipient_actor_id"] != "hannah" {
		t.Errorf("recipient_actor_id = %v, want hannah", r.Payload["recipient_actor_id"])
	}
	if r.Payload["lodging_grant"] != "inn" {
		t.Errorf("lodging_grant = %v, want the room's structure id 'inn'", r.Payload["lodging_grant"])
	}
}

// TestRebook_CreditsTheCoinRecordAsGoods is the replacement for the LLM-615
// divergence test, which asserted the opposite and was written to go red here.
//
// The rebook writes a durable `paid` row the coin-record boot seed selects
// (action_type='paid' AND result='ok' AND actor_id IS NOT NULL). Before LLM-615 it
// did not call RecordCoinPaid, so the pair's tally MISSED the charge until the next
// restart and HELD it afterwards — "## Coin between you and those here" answered
// differently depending on uptime.
//
// ForGoods, not Unstated: the debit and the grant extension commit in one command,
// so a night's occupancy is delivered against the coin. The durable row carries
// lodging_grant to say so, which is what keeps this live classification and a
// post-restart seed from disagreeing.
func TestRebook_CreditsTheCoinRecordAsGoods(t *testing.T) {
	lodger := rebookLodger("jefferey", 10, 2, rebookNow.Add(3*time.Hour))
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper) // nightly = 4

	runRebook(t, w)

	// Both sides of the pair, and nothing else.
	if n := countCoinPairs(w.CoinRecord); n != 2 {
		t.Fatalf("coin record holds %d pair-direction(s), want 2 (lodger's and keeper's)", n)
	}
	paid := w.CoinRecord["jefferey"]["hannah"]
	if paid == nil || len(paid.Paid) != 1 || len(paid.Received) != 0 {
		t.Fatalf("lodger's record = %+v, want exactly one Paid and no Received", paid)
	}
	if paid.Paid[0].Amount != 4 || paid.Paid[0].Kind != CoinPaymentForGoods {
		t.Errorf("lodger's payment = %+v, want amount 4 kind ForGoods", paid.Paid[0])
	}
	if !paid.Paid[0].At.Equal(rebookNow) {
		t.Errorf("payment At = %v, want the sweep's now %v", paid.Paid[0].At, rebookNow)
	}
	received := w.CoinRecord["hannah"]["jefferey"]
	if received == nil || len(received.Received) != 1 || len(received.Paid) != 0 {
		t.Fatalf("keeper's record = %+v, want exactly one Received and no Paid", received)
	}
	if received.Received[0].Amount != 4 || received.Received[0].Kind != CoinPaymentForGoods {
		t.Errorf("keeper's receipt = %+v, want amount 4 kind ForGoods", received.Received[0])
	}
}

// TestRebook_CoinRecordAgreesWithASeedOfItsOwnRow pins the property the marker
// exists for: what the live call credits and what a boot seed rebuilds FROM THE ROW
// THE SAME SWEEP WROTE must be the same payment, on the same pair, with the same
// classification.
//
// This is the actual LLM-615 defect. It was not that a number was wrong — it was
// that two readers of one event disagreed, so the answer depended on uptime. A
// future change that stamps the marker without crediting the tally, or credits it
// without stamping, leaves this red where either half asserted alone stays green.
//
// The seed half runs through rehydrateCoinRecordOnLoad rather than calling the
// classifier directly (code_review), so recipient resolution and the pair keying are
// exercised too — the classification is only one of the ways the two can diverge.
func TestRebook_CoinRecordAgreesWithASeedOfItsOwnRow(t *testing.T) {
	lodger := rebookLodger("jefferey", 10, 2, rebookNow.Add(3*time.Hour))
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper) // nightly = 4
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)

	runRebook(t, w)

	live := w.CoinRecord
	if len(sink.rows) != 1 {
		t.Fatalf("want 1 durable row to seed from, got %d", len(sink.rows))
	}
	row := sink.rows[0]

	// Rebuild the world's record the way FinalizeLoad does at boot, off nothing but
	// the durable row. The repo hands back what the pg loader would read from that
	// payload, so the keys the sweep wrote are what the seed sees.
	seeded := rebookTestWorld(28, 11, lodger, keeper)
	seeded.repo.CoinRecords = &stubCoinRecordsRepo{rows: []CoinPaymentRow{{
		PayerID:          row.ActorID,
		At:               row.OccurredAt,
		Amount:           fmt.Sprint(row.Payload["amount"]),
		RecipientActorID: fmt.Sprint(row.Payload["recipient_actor_id"]),
		RecipientName:    fmt.Sprint(row.Payload["recipient"]),
		RateSettled:      fmt.Sprint(row.Payload["rate_settled"]),
		LodgingGrant:     fmt.Sprint(row.Payload["lodging_grant"]),
	}}}
	if err := seeded.rehydrateCoinRecordOnLoad(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got, want := countCoinPairs(seeded.CoinRecord), countCoinPairs(live); got != want {
		t.Fatalf("seed rebuilt %d pair-direction(s), live holds %d — the record changes at boot", got, want)
	}
	for _, pair := range []struct{ subject, peer ActorID }{{"jefferey", "hannah"}, {"hannah", "jefferey"}} {
		liveRec, seededRec := live[pair.subject][pair.peer], seeded.CoinRecord[pair.subject][pair.peer]
		if seededRec == nil {
			t.Fatalf("seed has no record for %s->%s", pair.subject, pair.peer)
		}
		if !reflect.DeepEqual(liveRec.Paid, seededRec.Paid) || !reflect.DeepEqual(liveRec.Received, seededRec.Received) {
			t.Errorf("%s->%s: live %+v / %+v, seed %+v / %+v — the pair's record would change at boot",
				pair.subject, pair.peer, liveRec.Paid, liveRec.Received, seededRec.Paid, seededRec.Received)
		}
	}
}

// The live credit is NOT atomic with the durable enqueue, and this pins the
// direction of that gap rather than closing it.
//
// RecordCoinPaid documents the exposure and the decision behind it (LLM-572):
// AppendActionLogDurable is a deliberately non-blocking enqueue onto an async
// writer, so the world goroutine never waits on Postgres. Gating the credit on a
// confirmed write would block that goroutine or need a completion callback — a
// large change for a soft recollection cue. This path inherits that posture rather
// than inventing a new one, which is the point: all three `paid` writers behave
// alike here. The mechanism is intentionally restart-lossy for the tally; a
// successful RecordCoinPaid establishes NOTHING about the durable write.
//
// What must stay true is the DIRECTION. A rejected sink leaves the tally holding a
// payment the next boot will not rebuild, so the pair UNDERSTATES after a restart.
// It must never be the reverse — a durable row the live tally lacks is the LLM-615
// defect itself, and it is the reverse that made the record depend on uptime.
//
// SCOPE, precisely (code_review): this covers the SYNCHRONOUS enqueue-rejection
// path only. The other two failure modes — the async writer erroring after a
// successful enqueue, and a crash between the credit and the write — are documented
// and UNTESTED. They are believed to fail in the same direction, but nothing here
// proves it; that would need failure injection at the writer goroutine. Do not read
// this test as the broader guarantee.
func TestRebook_RejectedEnqueueUnderstatesRatherThanDiverging(t *testing.T) {
	lodger := rebookLodger("jefferey", 10, 2, rebookNow.Add(3*time.Hour))
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper) // nightly = 4
	w.SetActionLogSink(&failingActionLogSink{})

	runRebook(t, w)

	// The lodger is still housed and the coins still moved — the renewal does not
	// roll back on an audit failure, by the same reasoning the lean-ring append uses.
	if lodger.Coins != 6 || keeper.Coins != 4 {
		t.Errorf("coins = lodger %d / keeper %d, want 6 / 4 — the transfer must not depend on the sink", lodger.Coins, keeper.Coins)
	}
	// The live tally holds the payment. A boot seeded from a log that never got the
	// row would hold nothing, so the pair understates after a restart.
	paid := w.CoinRecord["jefferey"]["hannah"]
	if paid == nil || len(paid.Paid) != 1 {
		t.Fatalf("live record = %+v, want the payment held despite the failed append", paid)
	}
	if paid.Paid[0].Kind != CoinPaymentForGoods {
		t.Errorf("kind = %v, want ForGoods — a lost append loses the payment and its meaning together, never one without the other", paid.Paid[0].Kind)
	}
}

func TestLodgingNightlyRate(t *testing.T) {
	cases := []struct {
		weekly, want int
	}{
		{28, 4}, {35, 5}, {7, 1}, {6, 0}, {0, 0}, {-7, 0}, {29, 4}, // 29/7 = 4 (truncates)
	}
	for _, c := range cases {
		if got := LodgingNightlyRate(c.weekly); got != c.want {
			t.Errorf("LodgingNightlyRate(%d) = %d, want %d", c.weekly, got, c.want)
		}
	}
}
