package sim

import (
	"context"
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
	return &Actor{
		ID:    id,
		Kind:  KindPC, // only PCs auto-rebook (LLM-37)
		Coins: coins,
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
