package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestValidateFacing covers the facing write-path coalesce + enum guard:
// empty -> 'south' (schema default), valid enum members pass through, and a
// non-empty bad value is rejected before SQL so a CHECK violation can't fail
// the whole checkpoint Tx late.
func TestValidateFacing(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "south", false},
		{"north", "north", false},
		{"south", "south", false},
		{"east", "east", false},
		{"west", "west", false},
		{"South", "", true},     // wrong case — CHECK is exact
		{"northeast", "", true}, // not a member
		{"garbage", "", true},
	} {
		got, err := validateFacing(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("validateFacing(%q): want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("validateFacing(%q): unexpected error %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("validateFacing(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// pgxmock-based tests for ActorsRepo (Slice 1, ZBBS-WORK-243). Asserts
// SQL shape + arg bindings + nullable scan mapping. Real-pg behaviors
// (CHECK constraints, FK CASCADE, advisory lock blocking, UNIQUE
// constraints) land with the testcontainers smoke slice (parked
// pending migrations/baseline.sql).
//
// Actor IDs follow v1-UUID-as-text shape (same posture as Structures /
// Huddles slices). The `*::text` cast in loadAllSQLA lets pgxmock
// return bare strings.

// Predictable actor IDs.
const (
	actA = "00000000-0000-0000-0000-aaaaaaaaaaa1"
	actB = "00000000-0000-0000-0000-bbbbbbbbbbb2"
	actC = "00000000-0000-0000-0000-ccccccccccc3"
	actV = "00000000-0000-0000-0000-deadbeef0001" // visitor actor
	objX = "11111111-1111-1111-1111-111111111111" // a village object (dwell credit)
)

// Predictable timestamps. Use a fixed point so test expectations stay
// stable across runs.
var (
	tsTickedAt = time.Date(2026, 5, 19, 14, 30, 0, 0, time.UTC)
	tsBreak    = time.Date(2026, 5, 19, 15, 0, 0, 0, time.UTC)
	tsNextSelf = time.Date(2026, 5, 19, 15, 5, 0, 0, time.UTC)
	tsSleep    = time.Date(2026, 5, 19, 22, 0, 0, 0, time.UTC)
	tsEntered  = time.Date(2026, 5, 19, 8, 0, 0, 0, time.UTC)
)

func newMockPoolA(t *testing.T) (pgxmock.PgxPoolIface, *ActorsRepo) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock, NewActorsRepo(mock)
}

// actorParentColumns is the SELECT column ordering for the parent
// LoadAll query. Centralized so per-test row builders stay in sync if
// the column list changes.
func actorParentColumns() []string {
	return []string{
		"id", "display_name", "current_x", "current_y",
		"inside_structure_id", "current_huddle_id", "inside_room_id",
		"home_structure_id", "work_structure_id",
		"coins", "llm_memory_agent", "role", "login_username",
		"schedule_start_minute", "schedule_end_minute",
		"last_agent_tick_at", "break_until", "next_self_tick_at",
		"next_self_tick_reason", "sleeping_until",
		"move_attempt_counter", "sim_state", "sim_state_entered_at",
		"sprite_id", "facing", "admin",
	}
}

// emptyNeedRows / emptyInvRows: no-row sets for the child-table
// LoadAll queries. Used in tests that only exercise the parent path.
func emptyNeedRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"actor_id", "key", "value"})
}

func emptyInvRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"actor_id", "item_kind", "quantity"})
}

// --- Slice 2 continuity-tier column lists + empty-row builders -----------

func relationshipColumns() []string {
	return []string{
		"actor_id", "other_actor_id", "summary_text", "salient_facts",
		"interaction_count", "last_interaction_at", "last_consolidated_at",
		"created_at", "updated_at", "dropped_fact_count",
	}
}

func narrativeColumns() []string {
	return []string{
		"actor_id", "seed_text", "evolving_summary",
		"last_consolidated_at", "created_at", "updated_at",
	}
}

func acquaintanceColumns() []string {
	return []string{"actor_id", "other_name", "first_interacted_at"}
}

func emptyRelRows() *pgxmock.Rows  { return pgxmock.NewRows(relationshipColumns()) }
func emptyNarrRows() *pgxmock.Rows { return pgxmock.NewRows(narrativeColumns()) }
func emptyAcqRows() *pgxmock.Rows  { return pgxmock.NewRows(acquaintanceColumns()) }

// oneBareActorRows returns a parent result set with a single all-nullable
// actA row, for continuity child-loader tests that just need a valid
// parent to attach to.
func oneBareActorRows() *pgxmock.Rows {
	return pgxmock.NewRows(actorParentColumns()).AddRow(
		actA, "Hannah", 0, 0,
		(*string)(nil), (*string)(nil), (*int64)(nil),
		(*string)(nil), (*string)(nil),
		20, (*string)(nil), (*string)(nil), (*string)(nil),
		(*int16)(nil), (*int16)(nil),
		(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
		(*string)(nil), (*time.Time)(nil),
		int64(0), "idle", tsEntered,
		(*string)(nil), "south", false,
	)
}

// expectLoadAllContinuityEmpty programs empty result sets for the three
// continuity child queries (relationship / narrative / acquaintance) so
// parent-focused LoadAll tests don't have to spell them out.
func expectLoadAllContinuityEmpty(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`FROM actor_relationship\b`).WillReturnRows(emptyRelRows())
	mock.ExpectQuery(`FROM actor_narrative_state\b`).WillReturnRows(emptyNarrRows())
	mock.ExpectQuery(`FROM npc_acquaintance\b`).WillReturnRows(emptyAcqRows())
}

// expectActorContinuityTailsEmpty programs the relationship + narrative +
// acquaintance nextval/delete tails with no UPSERTs (the standard suffix
// when the snapshot has no continuity rows). Mirrors
// expectActorSaveSnapshotChildTails for the Slice 2 tiers.
func expectActorContinuityTailsEmpty(mock pgxmock.PgxPoolIface, relGen, narrGen, acqGen int64) {
	mock.ExpectQuery(`SELECT nextval\('actor_relationship_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(relGen))
	mock.ExpectExec(`DELETE FROM actor_relationship .*WHERE snapshot_gen < \$1`).
		WithArgs(relGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('actor_narrative_state_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(narrGen))
	mock.ExpectExec(`DELETE FROM actor_narrative_state .*WHERE snapshot_gen < \$1`).
		WithArgs(narrGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('npc_acquaintance_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(acqGen))
	mock.ExpectExec(`DELETE FROM npc_acquaintance .*WHERE snapshot_gen < \$1`).
		WithArgs(acqGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
}

// expectActorSaveSnapshotPrelude programs advisory lock + parent
// nextval. Called once per SaveSnapshot test.
func expectActorSaveSnapshotPrelude(mock pgxmock.PgxPoolIface, actorGen int64) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\('actor_snapshot'`).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	mock.ExpectQuery(`SELECT nextval\('actor_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(actorGen))
}

// expectActorSaveSnapshotChildTails programs the need + inventory
// nextval/delete tails (the standard suffix once parent UPSERTs are
// declared). Need + inventory UPSERTs are programmed per-test.
func expectActorSaveSnapshotChildTails(mock pgxmock.PgxPoolIface, needGen, invGen int64) {
	mock.ExpectQuery(`SELECT nextval\('actor_need_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(needGen))
	// Need UPSERTs (if any) are programmed by the test before this.
	mock.ExpectExec(`DELETE FROM actor_need .*WHERE snapshot_gen < \$1`).
		WithArgs(needGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('actor_inventory_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(invGen))
	// Inventory UPSERTs (if any) are programmed by the test before this.
	mock.ExpectExec(`DELETE FROM actor_inventory .*WHERE snapshot_gen < \$1`).
		WithArgs(invGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
}

// --- Slice 3 final-tier column lists + empty-row / tail builders ----------

func dwellCreditColumns() []string {
	return []string{
		"actor_id", "object_id", "attribute", "source",
		"last_credited_at", "remaining_ticks", "dwell_delta", "dwell_period_minutes",
	}
}

func produceStateColumns() []string {
	return []string{"actor_id", "item_kind", "last_produced_at"}
}

func roomAccessColumns() []string {
	return []string{
		"actor_id", "room_id", "granted_via_ledger_id",
		"granted_at", "expires_at", "active",
	}
}

func attributeColumns() []string {
	return []string{"actor_id", "slug", "params"}
}

func emptyDwellRows() *pgxmock.Rows      { return pgxmock.NewRows(dwellCreditColumns()) }
func emptyProduceRows() *pgxmock.Rows    { return pgxmock.NewRows(produceStateColumns()) }
func emptyRoomAccessRows() *pgxmock.Rows { return pgxmock.NewRows(roomAccessColumns()) }
func emptyAttrRows() *pgxmock.Rows       { return pgxmock.NewRows(attributeColumns()) }

// expectLoadAllSlice3Empty programs empty result sets for the four Slice 3
// child queries (dwell credit / produce state / room access / attribute),
// in LoadAll order. Pairs with expectLoadAllContinuityEmpty for the full
// post-inventory child suffix.
func expectLoadAllSlice3Empty(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`FROM actor_dwell_credit\b`).WillReturnRows(emptyDwellRows())
	mock.ExpectQuery(`FROM actor_produce_state\b`).WillReturnRows(emptyProduceRows())
	mock.ExpectQuery(`FROM room_access\b`).WillReturnRows(emptyRoomAccessRows())
	mock.ExpectQuery(`FROM actor_attribute\b`).WillReturnRows(emptyAttrRows())
}

// expectActorSlice3TailsEmpty programs the dwell-credit + produce-state +
// room-access + attribute nextval/delete tails with no UPSERTs (the
// standard suffix when the snapshot has no Slice 3 rows). Mirrors
// expectActorContinuityTailsEmpty for the Slice 3 tiers.
func expectActorSlice3TailsEmpty(mock pgxmock.PgxPoolIface, dwellGen, produceGen, roomGen, attrGen int64) {
	mock.ExpectQuery(`SELECT nextval\('actor_dwell_credit_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(dwellGen))
	mock.ExpectExec(`DELETE FROM actor_dwell_credit .*WHERE snapshot_gen < \$1`).
		WithArgs(dwellGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('actor_produce_state_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(produceGen))
	mock.ExpectExec(`DELETE FROM actor_produce_state .*WHERE snapshot_gen < \$1`).
		WithArgs(produceGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('room_access_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(roomGen))
	mock.ExpectExec(`DELETE FROM room_access .*WHERE snapshot_gen < \$1`).
		WithArgs(roomGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('actor_attribute_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(attrGen))
	mock.ExpectExec(`DELETE FROM actor_attribute .*WHERE snapshot_gen < \$1`).
		WithArgs(attrGen).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
}

// --- LoadAll happy path ---------------------------------------------------

// TestActorsRepo_LoadAll_HappyPath — full-shape actor with all nullable
// fields populated. Also covers the cross-aggregate carry-forward
// columns (Slice 11 current_huddle_id, Slice 12 home/work/inside
// structure refs + inside_room_id) — they arrive as `*string` /
// `*int64` scans and land on the v2 sim.Actor fields with the
// empty-string / zero-int sentinels.
func TestActorsRepo_LoadAll_HappyPath(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)

	insideStr := "00000000-0000-0000-0000-1111aaaaaaaa"
	huddleID := "00000000-0000-0000-0000-2222bbbbbbbb"
	homeStr := "00000000-0000-0000-0000-3333cccccccc"
	workStr := "00000000-0000-0000-0000-4444dddddddd"
	roomID := int64(42)
	startMin := int16(540) // 09:00
	endMin := int16(1020)  // 17:00
	reason := "low_tiredness"

	mock.ExpectQuery(`FROM actor\b`).
		WillReturnRows(pgxmock.NewRows(actorParentColumns()).
			AddRow(
				actA, "Mira", 5, 10,
				&insideStr, &huddleID, &roomID,
				&homeStr, &workStr,
				20, ptrStr("mira-agent"), ptrStr("tavernkeeper"), (*string)(nil),
				&startMin, &endMin,
				&tsTickedAt, &tsBreak, &tsNextSelf, ptrStr(reason), &tsSleep,
				int64(7), "working", tsEntered,
				ptrStr("00000000-0000-0000-0000-5555eeeeeeee"), "east", true,
			).
			AddRow(
				actB, "Bare", 0, 0,
				(*string)(nil), (*string)(nil), (*int64)(nil),
				(*string)(nil), (*string)(nil),
				20, (*string)(nil), (*string)(nil), (*string)(nil),
				(*int16)(nil), (*int16)(nil),
				(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
				(*string)(nil), (*time.Time)(nil),
				int64(0), "idle", tsEntered,
				(*string)(nil), "south", false,
			))

	mock.ExpectQuery(`FROM actor_need\b`).
		WillReturnRows(pgxmock.NewRows([]string{"actor_id", "key", "value"}).
			AddRow(actA, "hunger", 4).
			AddRow(actA, "tiredness", 18))

	mock.ExpectQuery(`FROM actor_inventory\b`).
		WillReturnRows(pgxmock.NewRows([]string{"actor_id", "item_kind", "quantity"}).
			AddRow(actA, "ale", 3).
			AddRow(actA, "coin_purse", 1))

	expectLoadAllContinuityEmpty(mock)
	expectLoadAllSlice3Empty(mock)

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	a := got[actA]
	if a == nil {
		t.Fatal("actA missing")
	}
	if a.DisplayName != "Mira" {
		t.Errorf("actA DisplayName = %q", a.DisplayName)
	}
	if a.CurrentX != 5 || a.CurrentY != 10 {
		t.Errorf("actA pos = (%d,%d)", a.CurrentX, a.CurrentY)
	}
	if string(a.InsideStructureID) != insideStr {
		t.Errorf("InsideStructureID = %q want %q", a.InsideStructureID, insideStr)
	}
	if string(a.CurrentHuddleID) != huddleID {
		t.Errorf("CurrentHuddleID = %q want %q", a.CurrentHuddleID, huddleID)
	}
	if int64(a.InsideRoomID) != roomID {
		t.Errorf("InsideRoomID = %d want %d", a.InsideRoomID, roomID)
	}
	if string(a.HomeStructureID) != homeStr || string(a.WorkStructureID) != workStr {
		t.Errorf("Home/Work = %q/%q", a.HomeStructureID, a.WorkStructureID)
	}
	if a.Coins != 20 {
		t.Errorf("Coins = %d", a.Coins)
	}
	if a.LLMAgent != "mira-agent" || a.Role != "tavernkeeper" {
		t.Errorf("LLMAgent=%q Role=%q", a.LLMAgent, a.Role)
	}
	if a.LoginUsername != "" {
		t.Errorf("LoginUsername = %q want empty", a.LoginUsername)
	}
	if a.ScheduleStartMin == nil || *a.ScheduleStartMin != int(startMin) {
		t.Errorf("ScheduleStartMin = %v", a.ScheduleStartMin)
	}
	if a.ScheduleEndMin == nil || *a.ScheduleEndMin != int(endMin) {
		t.Errorf("ScheduleEndMin = %v", a.ScheduleEndMin)
	}
	if a.LastTickedAt == nil || !a.LastTickedAt.Equal(tsTickedAt) {
		t.Errorf("LastTickedAt = %v", a.LastTickedAt)
	}
	if a.BreakUntil == nil || !a.BreakUntil.Equal(tsBreak) {
		t.Errorf("BreakUntil = %v", a.BreakUntil)
	}
	if a.NextSelfTickAt == nil || !a.NextSelfTickAt.Equal(tsNextSelf) {
		t.Errorf("NextSelfTickAt = %v", a.NextSelfTickAt)
	}
	if a.NextSelfTickReason != reason {
		t.Errorf("NextSelfTickReason = %q", a.NextSelfTickReason)
	}
	if a.SleepingUntil == nil || !a.SleepingUntil.Equal(tsSleep) {
		t.Errorf("SleepingUntil = %v", a.SleepingUntil)
	}
	if int64(a.MoveAttemptCounter) != 7 {
		t.Errorf("MoveAttemptCounter = %d", a.MoveAttemptCounter)
	}
	if string(a.State) != "working" {
		t.Errorf("State = %q", a.State)
	}
	if !a.StateEnteredAt.Equal(tsEntered) {
		t.Errorf("StateEnteredAt = %v", a.StateEnteredAt)
	}
	if string(a.SpriteID) != "00000000-0000-0000-0000-5555eeeeeeee" {
		t.Errorf("SpriteID = %q", a.SpriteID)
	}
	if a.Facing != "east" {
		t.Errorf("Facing = %q want east", a.Facing)
	}
	if !a.IsAdmin {
		t.Errorf("IsAdmin = false, want true (admin column loaded)")
	}
	if len(a.Needs) != 2 || a.Needs["hunger"] != 4 || a.Needs["tiredness"] != 18 {
		t.Errorf("Needs = %v", a.Needs)
	}
	if len(a.Inventory) != 2 || a.Inventory["ale"] != 3 {
		t.Errorf("Inventory = %v", a.Inventory)
	}

	// Bare actor with all-NULL nullable fields.
	b := got[actB]
	if b == nil {
		t.Fatal("actB missing")
	}
	if b.InsideStructureID != "" || b.CurrentHuddleID != "" || b.HomeStructureID != "" || b.WorkStructureID != "" {
		t.Errorf("actB IDs not empty: in=%q hud=%q home=%q work=%q",
			b.InsideStructureID, b.CurrentHuddleID, b.HomeStructureID, b.WorkStructureID)
	}
	if int64(b.InsideRoomID) != 0 {
		t.Errorf("actB InsideRoomID = %d, want 0", b.InsideRoomID)
	}
	if b.LLMAgent != "" || b.Role != "" || b.LoginUsername != "" {
		t.Errorf("actB strings not empty")
	}
	if b.ScheduleStartMin != nil || b.ScheduleEndMin != nil {
		t.Errorf("actB schedule not nil: %v/%v", b.ScheduleStartMin, b.ScheduleEndMin)
	}
	if b.LastTickedAt != nil || b.BreakUntil != nil || b.NextSelfTickAt != nil || b.SleepingUntil != nil {
		t.Errorf("actB time ptrs not nil")
	}
	if b.NextSelfTickReason != "" {
		t.Errorf("actB NextSelfTickReason = %q", b.NextSelfTickReason)
	}
	if b.SpriteID != "" {
		t.Errorf("actB SpriteID = %q, want empty (NULL sprite_id)", b.SpriteID)
	}
	if b.Facing != "south" {
		t.Errorf("actB Facing = %q, want south", b.Facing)
	}
	if b.IsAdmin {
		t.Errorf("actB IsAdmin = true, want false")
	}
	if int64(b.MoveAttemptCounter) != 0 {
		t.Errorf("actB MoveAttemptCounter = %d", b.MoveAttemptCounter)
	}
	if len(b.Needs) != 0 || len(b.Inventory) != 0 {
		t.Errorf("actB Needs=%v Inventory=%v", b.Needs, b.Inventory)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestActorsRepo_LoadAll_OrphanNeed — child need row whose parent actor
// isn't in the loaded set surfaces as a clean error (schema drift
// signal). FK CASCADE makes this unreachable from valid writes.
func TestActorsRepo_LoadAll_OrphanNeed(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)

	mock.ExpectQuery(`FROM actor\b`).
		WillReturnRows(pgxmock.NewRows(actorParentColumns())) // empty

	mock.ExpectQuery(`FROM actor_need\b`).
		WillReturnRows(pgxmock.NewRows([]string{"actor_id", "key", "value"}).
			AddRow(actA, "hunger", 4))

	// inventory not reached due to error.

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("LoadAll: want orphan-need error, got nil")
	}
	if !strings.Contains(err.Error(), "orphan need row") {
		t.Errorf("err = %v, want substring 'orphan need row'", err)
	}
}

// TestActorsRepo_LoadAll_OrphanInventory — same shape but on inventory.
func TestActorsRepo_LoadAll_OrphanInventory(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)

	mock.ExpectQuery(`FROM actor\b`).
		WillReturnRows(pgxmock.NewRows(actorParentColumns()))

	mock.ExpectQuery(`FROM actor_need\b`).
		WillReturnRows(emptyNeedRows())

	mock.ExpectQuery(`FROM actor_inventory\b`).
		WillReturnRows(pgxmock.NewRows([]string{"actor_id", "item_kind", "quantity"}).
			AddRow(actA, "ale", 3))

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("LoadAll: want orphan-inventory error, got nil")
	}
	if !strings.Contains(err.Error(), "orphan inventory row") {
		t.Errorf("err = %v, want substring 'orphan inventory row'", err)
	}
}

// TestActorsRepo_LoadAll_ParentQueryError — top-level query error
// surfaces wrapped.
func TestActorsRepo_LoadAll_ParentQueryError(t *testing.T) {
	mock, repo := newMockPoolA(t)
	sentinel := errors.New("connection lost")
	mock.ExpectQuery(`FROM actor\b`).WillReturnError(sentinel)

	_, err := repo.LoadAll(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want wrapping %v", err, sentinel)
	}
}

// --- SaveSnapshot happy path ----------------------------------------------

// TestActorsRepo_SaveSnapshot_FullActor — single fully-populated actor
// with needs + inventory. Asserts the parent UPSERT carries all 24
// positional args correctly, including the nullable conversions
// (*string → SQL NULL when empty, *int → SQL NULL when nil,
// InsideRoomID 0 → SQL NULL).
func TestActorsRepo_SaveSnapshot_FullActor(t *testing.T) {
	mock, repo := newMockPoolA(t)

	expectActorSaveSnapshotPrelude(mock, 101)

	startMin := 540
	endMin := 1020

	mock.ExpectExec(`INSERT INTO actor `).
		WithArgs(
			actA, "Mira", 5, 10,
			"00000000-0000-0000-0000-1111aaaaaaaa",
			"00000000-0000-0000-0000-2222bbbbbbbb",
			int64(42),
			"00000000-0000-0000-0000-3333cccccccc",
			"00000000-0000-0000-0000-4444dddddddd",
			20, "mira-agent", "tavernkeeper", nil,
			int16(540), int16(1020),
			&tsTickedAt, &tsBreak, &tsNextSelf, "low_tiredness", &tsSleep,
			int64(7), "working", tsEntered,
			"00000000-0000-0000-0000-5555eeeeeeee", "east",
			int64(101),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(101)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	mock.ExpectQuery(`SELECT nextval\('actor_need_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(201)))
	mock.ExpectExec(`INSERT INTO actor_need `).
		WithArgs(actA, "hunger", 4, int64(201)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_need .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(201)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	mock.ExpectQuery(`SELECT nextval\('actor_inventory_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(301)))
	mock.ExpectExec(`INSERT INTO actor_inventory `).
		WithArgs(actA, "ale", 3, int64(301)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_inventory .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(301)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	expectActorContinuityTailsEmpty(mock, 401, 501, 601)
	expectActorSlice3TailsEmpty(mock, 4001, 5001, 6001, 7001)

	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID:                 actA,
			DisplayName:        "Mira",
			CurrentX:           5,
			CurrentY:           10,
			InsideStructureID:  "00000000-0000-0000-0000-1111aaaaaaaa",
			CurrentHuddleID:    "00000000-0000-0000-0000-2222bbbbbbbb",
			InsideRoomID:       42,
			HomeStructureID:    "00000000-0000-0000-0000-3333cccccccc",
			WorkStructureID:    "00000000-0000-0000-0000-4444dddddddd",
			Coins:              20,
			LLMAgent:           "mira-agent",
			Role:               "tavernkeeper",
			LoginUsername:      "",
			ScheduleStartMin:   &startMin,
			ScheduleEndMin:     &endMin,
			LastTickedAt:       &tsTickedAt,
			BreakUntil:         &tsBreak,
			NextSelfTickAt:     &tsNextSelf,
			NextSelfTickReason: "low_tiredness",
			SleepingUntil:      &tsSleep,
			MoveAttemptCounter: 7,
			State:              "working",
			StateEnteredAt:     tsEntered,
			SpriteID:           "00000000-0000-0000-0000-5555eeeeeeee",
			Facing:             "east",
			Needs:              map[sim.NeedKey]int{"hunger": 4},
			Inventory:          map[sim.ItemKind]int{"ale": 3},
		},
	}

	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestActorsRepo_SaveSnapshot_BareActor — empty-string / nil-pointer /
// zero-RoomID fields all round-trip through nil → SQL NULL. Crucial
// for the empty-string ↔ NULL convention this slice establishes.
func TestActorsRepo_SaveSnapshot_BareActor(t *testing.T) {
	mock, repo := newMockPoolA(t)

	expectActorSaveSnapshotPrelude(mock, 102)

	mock.ExpectExec(`INSERT INTO actor `).
		WithArgs(
			actB, "Bare", 0, 0,
			nil, nil, nil, // InsideStructureID, CurrentHuddleID, InsideRoomID
			nil, nil, // Home/Work StructureID
			20, nil, nil, nil, // Coins, LLMAgent, Role, LoginUsername
			nil, nil, // schedule
			(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil), // time pointers
			nil, (*time.Time)(nil), // NextSelfTickReason, SleepingUntil
			int64(0), "idle", tsEntered, // counter, state, entered
			nil, "south", // sprite_id (empty→NULL), facing (empty→default 'south')
			int64(102),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(102)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	expectActorSaveSnapshotChildTails(mock, 202, 302)
	expectActorContinuityTailsEmpty(mock, 402, 502, 602)
	expectActorSlice3TailsEmpty(mock, 4002, 5002, 6002, 7002)

	actors := map[sim.ActorID]*sim.Actor{
		actB: {
			ID:             actB,
			DisplayName:    "Bare",
			Coins:          20,
			State:          "idle",
			StateEnteredAt: tsEntered,
			// All other fields are zero values — empty strings, nil pointers,
			// zero RoomID, zero MoveAttemptCounter.
		},
	}

	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestActorsRepo_SaveSnapshot_VisitorFiltered — actors with non-nil
// VisitorState are filtered out of SaveSnapshot entirely (no UPSERT
// fires for them). Per visitor codebase note "No durable visitor row
// persistence." All three gen-marker tiers still bump + DELETE-stale
// run to prune absent rows.
func TestActorsRepo_SaveSnapshot_VisitorFiltered(t *testing.T) {
	mock, repo := newMockPoolA(t)

	expectActorSaveSnapshotPrelude(mock, 103)
	// Note: ZERO actor UPSERTs expected — the visitor is filtered,
	// no non-visitor actors in the snapshot.
	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(103)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 1"))
	expectActorSaveSnapshotChildTails(mock, 203, 303)
	expectActorContinuityTailsEmpty(mock, 403, 503, 603)
	expectActorSlice3TailsEmpty(mock, 4003, 5003, 6003, 7003)

	actors := map[sim.ActorID]*sim.Actor{
		actV: {
			ID:             actV,
			DisplayName:    "Visitor Vince",
			State:          "idle",
			StateEnteredAt: tsEntered,
			VisitorState:   &sim.VisitorState{Archetype: "scholar"},
		},
	}

	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestActorsRepo_SaveSnapshot_EmptyMap — empty actors map. All three
// gens bump, no UPSERTs run, all three deletes sweep the tables.
func TestActorsRepo_SaveSnapshot_EmptyMap(t *testing.T) {
	mock, repo := newMockPoolA(t)

	expectActorSaveSnapshotPrelude(mock, 104)
	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(104)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectActorSaveSnapshotChildTails(mock, 204, 304)
	expectActorContinuityTailsEmpty(mock, 404, 504, 604)
	expectActorSlice3TailsEmpty(mock, 4004, 5004, 6004, 7004)

	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, map[sim.ActorID]*sim.Actor{}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestActorsRepo_SaveSnapshot_ZeroQtyInventoryDropped — inventory
// entries with quantity <= 0 are NOT UPSERTed. The trailing DELETE
// sweep eventually prunes the row from the table; in this test we
// just confirm no INSERT into actor_inventory fires for the zero
// entry.
func TestActorsRepo_SaveSnapshot_ZeroQtyInventoryDropped(t *testing.T) {
	mock, repo := newMockPoolA(t)

	expectActorSaveSnapshotPrelude(mock, 105)

	mock.ExpectExec(`INSERT INTO actor `).
		WithArgs(
			actA, "Mira", 0, 0,
			nil, nil, nil,
			nil, nil,
			20, nil, nil, nil,
			nil, nil,
			(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
			nil, (*time.Time)(nil),
			int64(0), "idle", tsEntered,
			nil, "south",
			int64(105),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(105)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	mock.ExpectQuery(`SELECT nextval\('actor_need_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(205)))
	mock.ExpectExec(`DELETE FROM actor_need .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(205)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Inventory tier: ale gets UPSERTed (qty=3), bread is SKIPPED (qty=0).
	mock.ExpectQuery(`SELECT nextval\('actor_inventory_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(305)))
	mock.ExpectExec(`INSERT INTO actor_inventory `).
		WithArgs(actA, "ale", 3, int64(305)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_inventory .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(305)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 1")) // bread row swept

	expectActorContinuityTailsEmpty(mock, 405, 505, 605)
	expectActorSlice3TailsEmpty(mock, 4005, 5005, 6005, 7005)

	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID:             actA,
			DisplayName:    "Mira",
			Coins:          20,
			State:          "idle",
			StateEnteredAt: tsEntered,
			Inventory: map[sim.ItemKind]int{
				"ale":   3,
				"bread": 0, // zero qty → skip UPSERT, swept by DELETE
			},
		},
	}
	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// --- SaveSnapshot validation ----------------------------------------------

// All validation errors abort BEFORE any SQL fires (advisory lock
// included — Slice 1 R1 moved validation in front of the lock). We
// assert that by programming NO expectations on the mock and confirming
// ExpectationsWereMet after the error returns.

func assertValidationOnly(t *testing.T, mock pgxmock.PgxPoolIface, err error, wantSubstr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("err = %v, want substring %q", err, wantSubstr)
	}
	if exErr := mock.ExpectationsWereMet(); exErr != nil {
		t.Errorf("validation path fired unexpected SQL: %v", exErr)
	}
}

func TestActorsRepo_SaveSnapshot_NilEntry(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{actA: nil}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "nil entry")
}

func TestActorsRepo_SaveSnapshot_EmptyID(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		"   ": {ID: "   ", DisplayName: "X", State: "idle", StateEnteredAt: tsEntered},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "empty ActorID")
}

func TestActorsRepo_SaveSnapshot_KeyMismatch(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {ID: actB, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "does not match a.ID")
}

func TestActorsRepo_SaveSnapshot_EmptyDisplayName(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {ID: actA, DisplayName: "  ", State: "idle", StateEnteredAt: tsEntered},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "empty DisplayName")
}

func TestActorsRepo_SaveSnapshot_EmptyState(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {ID: actA, DisplayName: "X", State: "", StateEnteredAt: tsEntered},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "empty State")
}

func TestActorsRepo_SaveSnapshot_ZeroStateEnteredAt(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {ID: actA, DisplayName: "X", State: "idle"}, // zero StateEnteredAt
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "zero StateEnteredAt")
}

func TestActorsRepo_SaveSnapshot_HalfSetSchedule(t *testing.T) {
	mock, repo := newMockPoolA(t)
	start := 540
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			ScheduleStartMin: &start, // ScheduleEndMin: nil
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "half-set schedule")
}

// TestActorsRepo_SaveSnapshot_ScheduleOutOfRange — guards intPtrToSQL's
// int16 narrowing. A 40000 value would wrap silently if validation
// didn't catch it.
func TestActorsRepo_SaveSnapshot_ScheduleOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*sim.Actor)
		want string
	}{
		{
			name: "start_above",
			mut: func(a *sim.Actor) {
				v := 40000
				e := 100
				a.ScheduleStartMin, a.ScheduleEndMin = &v, &e
			},
			want: "ScheduleStartMin=40000",
		},
		{
			name: "start_below",
			mut: func(a *sim.Actor) {
				v := -1
				e := 100
				a.ScheduleStartMin, a.ScheduleEndMin = &v, &e
			},
			want: "ScheduleStartMin=-1",
		},
		{
			name: "end_above",
			mut: func(a *sim.Actor) {
				s := 100
				v := 1440
				a.ScheduleStartMin, a.ScheduleEndMin = &s, &v
			},
			want: "ScheduleEndMin=1440",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, repo := newMockPoolA(t)
			a := &sim.Actor{ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered}
			tc.mut(a)
			err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, map[sim.ActorID]*sim.Actor{actA: a})
			assertValidationOnly(t, mock, err, tc.want)
		})
	}
}

func TestActorsRepo_SaveSnapshot_NeedOutOfRange(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Needs: map[sim.NeedKey]int{"hunger": 99}, // > 24
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "out of range")
}

// TestActorsRepo_SaveSnapshot_EmptyNeedKey — guards against whitespace
// or empty need keys (would trip btrim CHECK mid-Tx otherwise).
func TestActorsRepo_SaveSnapshot_EmptyNeedKey(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Needs: map[sim.NeedKey]int{"  ": 4},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "empty need key")
}

// TestActorsRepo_SaveSnapshot_NegativeInventoryQuantity — negative
// quantities are almost certainly command-handler bugs; reject rather
// than silently treat as deletion.
func TestActorsRepo_SaveSnapshot_NegativeInventoryQuantity(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Inventory: map[sim.ItemKind]int{"ale": -1},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "quantity=-1")
}

// TestActorsRepo_SaveSnapshot_EmptyInventoryKind — guards whitespace /
// empty item_kind keys.
func TestActorsRepo_SaveSnapshot_EmptyInventoryKind(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Inventory: map[sim.ItemKind]int{"   ": 3},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "empty inventory item kind")
}

// TestActorsRepo_SaveSnapshot_NilTx — defensive guard.
func TestActorsRepo_SaveSnapshot_NilTx(t *testing.T) {
	_, repo := newMockPoolA(t)
	err := repo.SaveSnapshot(context.Background(), nil, map[sim.ActorID]*sim.Actor{})
	if err == nil || !strings.Contains(err.Error(), "nil tx") {
		t.Fatalf("err = %v", err)
	}
}

// --- Slice 2: salient-fact JSONB marshal/unmarshal ------------------------

// TestSalientFacts_MarshalRoundTrip pins the lowercase {at, kind, text}
// key shape (matches v1's stored JSONB) and the empty-slice ↔ `[]` ↔ nil
// round-trip.
func TestSalientFacts_MarshalRoundTrip(t *testing.T) {
	facts := []sim.SalientFact{
		{At: time.Date(2026, 5, 19, 14, 30, 0, 0, time.UTC), Kind: "spoke", Text: "Good morrow"},
		{At: time.Date(2026, 5, 19, 15, 0, 0, 0, time.UTC), Kind: "paid", Text: "bought ale"},
	}

	got, err := marshalSalientFacts(facts)
	if err != nil {
		t.Fatalf("marshalSalientFacts: %v", err)
	}
	want := `[{"at":"2026-05-19T14:30:00Z","kind":"spoke","text":"Good morrow"},` +
		`{"at":"2026-05-19T15:00:00Z","kind":"paid","text":"bought ale"}]`
	if got != want {
		t.Errorf("marshal =\n %s\nwant\n %s", got, want)
	}

	back, err := unmarshalSalientFacts([]byte(got))
	if err != nil {
		t.Fatalf("unmarshalSalientFacts: %v", err)
	}
	if len(back) != len(facts) {
		t.Fatalf("round-trip len = %d, want %d", len(back), len(facts))
	}
	for i := range facts {
		if !back[i].At.Equal(facts[i].At) || back[i].Kind != facts[i].Kind || back[i].Text != facts[i].Text {
			t.Errorf("fact[%d] = %+v, want %+v", i, back[i], facts[i])
		}
	}
}

func TestSalientFacts_EmptyAndNil(t *testing.T) {
	got, err := marshalSalientFacts(nil)
	if err != nil {
		t.Fatalf("marshal nil: %v", err)
	}
	if got != "[]" {
		t.Errorf("marshal(nil) = %q, want %q", got, "[]")
	}
	if f, err := unmarshalSalientFacts(nil); err != nil || f != nil {
		t.Errorf("unmarshal(nil) = %v, %v; want nil, nil", f, err)
	}
	if f, err := unmarshalSalientFacts([]byte("[]")); err != nil || f != nil {
		t.Errorf("unmarshal([]) = %v, %v; want nil, nil", f, err)
	}
}

// --- Slice 2: LoadAll continuity tiers ------------------------------------

// TestActorsRepo_LoadAll_Continuity — relationships (multi-fact JSONB +
// nil-time variant + lenient peer-not-in-world), narrative, and
// acquaintance all attach to the owning actor.
func TestActorsRepo_LoadAll_Continuity(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)

	actZ := "00000000-0000-0000-0000-zzzz00000009" // peer NOT loaded as an actor

	// One parent actor.
	mock.ExpectQuery(`FROM actor\b`).
		WillReturnRows(pgxmock.NewRows(actorParentColumns()).
			AddRow(
				actA, "Hannah", 0, 0,
				(*string)(nil), (*string)(nil), (*int64)(nil),
				(*string)(nil), (*string)(nil),
				20, (*string)(nil), (*string)(nil), (*string)(nil),
				(*int16)(nil), (*int16)(nil),
				(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
				(*string)(nil), (*time.Time)(nil),
				int64(0), "idle", tsEntered,
				(*string)(nil), "south", false,
			))
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())

	factsJSON := []byte(`[{"at":"2026-05-19T14:30:00Z","kind":"spoke","text":"Good morrow"},` +
		`{"at":"2026-05-19T15:00:00Z","kind":"paid","text":"bought ale"}]`)
	mock.ExpectQuery(`FROM actor_relationship\b`).
		WillReturnRows(pgxmock.NewRows(relationshipColumns()).
			// Full relationship with facts + both timestamps set.
			AddRow(actA, actB, "John runs the tavern", factsJSON, 5, &tsTickedAt, &tsBreak, tsEntered, tsTickedAt, 2).
			// Bare relationship: empty facts, nil timestamps. Peer (actZ)
			// is NOT a loaded actor — must still load (lenient).
			AddRow(actA, actZ, "", []byte("[]"), 0, (*time.Time)(nil), (*time.Time)(nil), tsEntered, tsEntered, 0))

	mock.ExpectQuery(`FROM actor_narrative_state\b`).
		WillReturnRows(pgxmock.NewRows(narrativeColumns()).
			AddRow(actA, "seed frame", "evolving impression", &tsBreak, tsEntered, tsTickedAt))

	mock.ExpectQuery(`FROM npc_acquaintance\b`).
		WillReturnRows(pgxmock.NewRows(acquaintanceColumns()).
			AddRow(actA, "Goodwife Smith", tsTickedAt))
	expectLoadAllSlice3Empty(mock)

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	a := got[actA]
	if a == nil {
		t.Fatal("actA missing")
	}

	rel := a.Relationships[actB]
	if rel == nil {
		t.Fatal("relationship actA→actB missing")
	}
	if rel.SummaryText != "John runs the tavern" {
		t.Errorf("SummaryText = %q", rel.SummaryText)
	}
	if len(rel.SalientFacts) != 2 {
		t.Fatalf("SalientFacts len = %d, want 2", len(rel.SalientFacts))
	}
	if rel.SalientFacts[0].Kind != "spoke" || rel.SalientFacts[0].Text != "Good morrow" {
		t.Errorf("fact[0] = %+v", rel.SalientFacts[0])
	}
	if rel.SalientFacts[1].Kind != "paid" {
		t.Errorf("fact[1].Kind = %q", rel.SalientFacts[1].Kind)
	}
	if rel.InteractionCount != 5 || rel.DroppedFactCount != 2 {
		t.Errorf("counts = %d/%d", rel.InteractionCount, rel.DroppedFactCount)
	}
	if rel.LastInteractionAt == nil || !rel.LastInteractionAt.Equal(tsTickedAt) {
		t.Errorf("LastInteractionAt = %v", rel.LastInteractionAt)
	}
	if rel.LastConsolidatedAt == nil || !rel.LastConsolidatedAt.Equal(tsBreak) {
		t.Errorf("LastConsolidatedAt = %v", rel.LastConsolidatedAt)
	}

	bare := a.Relationships[sim.ActorID(actZ)]
	if bare == nil {
		t.Fatal("lenient relationship actA→actZ (peer not loaded) should still load")
	}
	if len(bare.SalientFacts) != 0 {
		t.Errorf("bare SalientFacts = %v, want empty", bare.SalientFacts)
	}
	if bare.LastInteractionAt != nil || bare.LastConsolidatedAt != nil {
		t.Errorf("bare timestamps not nil: %v/%v", bare.LastInteractionAt, bare.LastConsolidatedAt)
	}

	if a.Narrative == nil {
		t.Fatal("narrative missing")
	}
	if a.Narrative.SeedText != "seed frame" || a.Narrative.EvolvingSummary != "evolving impression" {
		t.Errorf("narrative = %+v", a.Narrative)
	}
	if a.Narrative.LastConsolidatedAt == nil || !a.Narrative.LastConsolidatedAt.Equal(tsBreak) {
		t.Errorf("narrative LastConsolidatedAt = %v", a.Narrative.LastConsolidatedAt)
	}

	acq, ok := a.Acquaintances["Goodwife Smith"]
	if !ok {
		t.Fatal("acquaintance 'Goodwife Smith' missing")
	}
	if !acq.FirstInteractedAt.Equal(tsTickedAt) {
		t.Errorf("acquaintance FirstInteractedAt = %v", acq.FirstInteractedAt)
	}
}

func TestActorsRepo_LoadAll_OrphanRelationship(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(pgxmock.NewRows(actorParentColumns()))
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	mock.ExpectQuery(`FROM actor_relationship\b`).
		WillReturnRows(pgxmock.NewRows(relationshipColumns()).
			AddRow(actA, actB, "", []byte("[]"), 0, (*time.Time)(nil), (*time.Time)(nil), tsEntered, tsEntered, 0))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "orphan relationship row") {
		t.Fatalf("err = %v, want 'orphan relationship row'", err)
	}
}

func TestActorsRepo_LoadAll_OrphanNarrative(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(pgxmock.NewRows(actorParentColumns()))
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	mock.ExpectQuery(`FROM actor_relationship\b`).WillReturnRows(emptyRelRows())
	mock.ExpectQuery(`FROM actor_narrative_state\b`).
		WillReturnRows(pgxmock.NewRows(narrativeColumns()).
			AddRow(actA, "seed", "", (*time.Time)(nil), tsEntered, tsEntered))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "orphan narrative row") {
		t.Fatalf("err = %v, want 'orphan narrative row'", err)
	}
}

func TestActorsRepo_LoadAll_OrphanAcquaintance(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(pgxmock.NewRows(actorParentColumns()))
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	mock.ExpectQuery(`FROM actor_relationship\b`).WillReturnRows(emptyRelRows())
	mock.ExpectQuery(`FROM actor_narrative_state\b`).WillReturnRows(emptyNarrRows())
	mock.ExpectQuery(`FROM npc_acquaintance\b`).
		WillReturnRows(pgxmock.NewRows(acquaintanceColumns()).
			AddRow(actA, "Goodwife Smith", tsTickedAt))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "orphan acquaintance row") {
		t.Fatalf("err = %v, want 'orphan acquaintance row'", err)
	}
}

// TestActorsRepo_LoadAll_RelationshipSelfRejected — Load enforces the
// same shape invariants as Save (Go owns the invariants; the schema
// deliberately omits some CHECKs). A self-relationship row from
// out-of-band data / legacy state hard-errors at load.
func TestActorsRepo_LoadAll_RelationshipSelfRejected(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(oneBareActorRows())
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	mock.ExpectQuery(`FROM actor_relationship\b`).
		WillReturnRows(pgxmock.NewRows(relationshipColumns()).
			AddRow(actA, actA, "", []byte("[]"), 0, (*time.Time)(nil), (*time.Time)(nil), tsEntered, tsEntered, 0))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "self-relationship") {
		t.Fatalf("err = %v, want 'self-relationship'", err)
	}
}

// TestActorsRepo_LoadAll_RelationshipNegativeCount — negative
// interaction_count (no DB CHECK in v1) hard-errors at load.
func TestActorsRepo_LoadAll_RelationshipNegativeCount(t *testing.T) {
	mock, repo := newMockPoolA(t)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(oneBareActorRows())
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	mock.ExpectQuery(`FROM actor_relationship\b`).
		WillReturnRows(pgxmock.NewRows(relationshipColumns()).
			AddRow(actA, actB, "", []byte("[]"), -1, (*time.Time)(nil), (*time.Time)(nil), tsEntered, tsEntered, 0))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "interaction_count=-1") {
		t.Fatalf("err = %v, want 'interaction_count=-1'", err)
	}
}

// --- Slice 2: SaveSnapshot continuity tiers -------------------------------

// TestActorsRepo_SaveSnapshot_Continuity — one actor with a relationship
// (incl. salient-fact JSONB), narrative, and acquaintance. Asserts the
// UPSERT args for each new tier, including the marshalled JSON string.
func TestActorsRepo_SaveSnapshot_Continuity(t *testing.T) {
	mock, repo := newMockPoolA(t)

	expectActorSaveSnapshotPrelude(mock, 701)
	mock.ExpectExec(`INSERT INTO actor `).
		WithArgs(
			actA, "Hannah", 0, 0,
			nil, nil, nil,
			nil, nil,
			20, nil, nil, nil,
			nil, nil,
			(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
			nil, (*time.Time)(nil),
			int64(0), "idle", tsEntered,
			nil, "south",
			int64(701),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(701)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectActorSaveSnapshotChildTails(mock, 801, 901)

	// Relationship tier.
	wantFacts := []sim.SalientFact{{At: tsTickedAt, Kind: "spoke", Text: "Good morrow"}}
	wantFactsJSON, err := marshalSalientFacts(wantFacts)
	if err != nil {
		t.Fatalf("marshalSalientFacts: %v", err)
	}
	mock.ExpectQuery(`SELECT nextval\('actor_relationship_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1001)))
	mock.ExpectExec(`INSERT INTO actor_relationship `).
		WithArgs(
			actA, actB, "John runs the tavern", wantFactsJSON,
			5, &tsTickedAt, &tsBreak,
			tsEntered, tsTickedAt, 2, int64(1001),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_relationship .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1001)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Narrative tier.
	mock.ExpectQuery(`SELECT nextval\('actor_narrative_state_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1101)))
	mock.ExpectExec(`INSERT INTO actor_narrative_state `).
		WithArgs(actA, "seed frame", "evolving impression", &tsBreak, tsEntered, tsTickedAt, int64(1101)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_narrative_state .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1101)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Acquaintance tier.
	mock.ExpectQuery(`SELECT nextval\('npc_acquaintance_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1201)))
	mock.ExpectExec(`INSERT INTO npc_acquaintance `).
		WithArgs(actA, "Goodwife Smith", tsTickedAt, int64(1201)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM npc_acquaintance .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1201)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectActorSlice3TailsEmpty(mock, 1301, 1401, 1501, 1601)

	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "Hannah", Coins: 20, State: "idle", StateEnteredAt: tsEntered,
			Relationships: map[sim.ActorID]*sim.Relationship{
				actB: {
					SummaryText:        "John runs the tavern",
					SalientFacts:       wantFacts,
					InteractionCount:   5,
					LastInteractionAt:  &tsTickedAt,
					LastConsolidatedAt: &tsBreak,
					CreatedAt:          tsEntered,
					UpdatedAt:          tsTickedAt,
					DroppedFactCount:   2,
				},
			},
			Narrative: &sim.NarrativeState{
				SeedText:           "seed frame",
				EvolvingSummary:    "evolving impression",
				LastConsolidatedAt: &tsBreak,
				CreatedAt:          tsEntered,
				UpdatedAt:          tsTickedAt,
			},
			Acquaintances: map[string]sim.Acquaintance{
				"Goodwife Smith": {FirstInteractedAt: tsTickedAt},
			},
		},
	}

	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestActorsRepo_SaveSnapshot_EmptySalientFacts — a relationship with no
// facts is persisted with salient_facts = `[]` (not NULL), matching the
// column DEFAULT and v1's stored shape.
func TestActorsRepo_SaveSnapshot_EmptySalientFacts(t *testing.T) {
	mock, repo := newMockPoolA(t)

	expectActorSaveSnapshotPrelude(mock, 702)
	mock.ExpectExec(`INSERT INTO actor `).
		WithArgs(
			actA, "Hannah", 0, 0,
			nil, nil, nil, nil, nil,
			20, nil, nil, nil,
			nil, nil,
			(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
			nil, (*time.Time)(nil),
			int64(0), "idle", tsEntered,
			nil, "south",
			int64(702),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(702)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectActorSaveSnapshotChildTails(mock, 802, 902)

	mock.ExpectQuery(`SELECT nextval\('actor_relationship_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1002)))
	mock.ExpectExec(`INSERT INTO actor_relationship `).
		WithArgs(
			actA, actB, "", "[]",
			0, (*time.Time)(nil), (*time.Time)(nil),
			tsEntered, tsEntered, 0, int64(1002),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_relationship .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1002)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Narrative + acquaintance tiers empty (this actor has neither).
	mock.ExpectQuery(`SELECT nextval\('actor_narrative_state_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1102)))
	mock.ExpectExec(`DELETE FROM actor_narrative_state .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1102)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('npc_acquaintance_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1202)))
	mock.ExpectExec(`DELETE FROM npc_acquaintance .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1202)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectActorSlice3TailsEmpty(mock, 1302, 1402, 1502, 1602)

	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "Hannah", Coins: 20, State: "idle", StateEnteredAt: tsEntered,
			Relationships: map[sim.ActorID]*sim.Relationship{
				actB: {CreatedAt: tsEntered, UpdatedAt: tsEntered}, // no facts
			},
		},
	}
	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// --- Slice 2: SaveSnapshot continuity validation --------------------------

func TestActorsRepo_SaveSnapshot_SelfRelationship(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Relationships: map[sim.ActorID]*sim.Relationship{actA: {}},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "self-relationship")
}

func TestActorsRepo_SaveSnapshot_EmptyRelationshipPeer(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Relationships: map[sim.ActorID]*sim.Relationship{"  ": {}},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "empty relationship peer key")
}

func TestActorsRepo_SaveSnapshot_NegativeRelationshipCounts(t *testing.T) {
	cases := []struct {
		name string
		rel  *sim.Relationship
		want string
	}{
		{"interaction", &sim.Relationship{InteractionCount: -1}, "InteractionCount=-1"},
		{"dropped", &sim.Relationship{DroppedFactCount: -1}, "DroppedFactCount=-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, repo := newMockPoolA(t)
			actors := map[sim.ActorID]*sim.Actor{
				actA: {
					ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
					Relationships: map[sim.ActorID]*sim.Relationship{actB: tc.rel},
				},
			}
			err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
			assertValidationOnly(t, mock, err, tc.want)
		})
	}
}

func TestActorsRepo_SaveSnapshot_EmptyAcquaintanceName(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Acquaintances: map[string]sim.Acquaintance{"   ": {FirstInteractedAt: tsEntered}},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "empty acquaintance name")
}

func TestActorsRepo_SaveSnapshot_OverLongAcquaintanceName(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Acquaintances: map[string]sim.Acquaintance{strings.Repeat("x", 101): {FirstInteractedAt: tsEntered}},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "exceeds 100 chars")
}

// TestActorsRepo_SaveSnapshot_AcquaintanceMultibyteWithinLimit — a name
// of 100 multibyte runes (200 bytes) is WITHIN VARCHAR(100) and must be
// accepted. Regression for the byte-vs-rune length bug (round 1).
func TestActorsRepo_SaveSnapshot_AcquaintanceMultibyteWithinLimit(t *testing.T) {
	mock, repo := newMockPoolA(t)
	name := strings.Repeat("é", 100) // 100 runes, 200 bytes

	expectActorSaveSnapshotPrelude(mock, 706)
	mock.ExpectExec(`INSERT INTO actor `).
		WithArgs(
			actA, "Hannah", 0, 0,
			nil, nil, nil, nil, nil,
			20, nil, nil, nil,
			nil, nil,
			(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
			nil, (*time.Time)(nil),
			int64(0), "idle", tsEntered,
			nil, "south",
			int64(706),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(706)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectActorSaveSnapshotChildTails(mock, 806, 906)

	// Relationship + narrative tiers empty.
	mock.ExpectQuery(`SELECT nextval\('actor_relationship_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1006)))
	mock.ExpectExec(`DELETE FROM actor_relationship .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1006)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	mock.ExpectQuery(`SELECT nextval\('actor_narrative_state_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1106)))
	mock.ExpectExec(`DELETE FROM actor_narrative_state .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1106)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Acquaintance tier with the multibyte name.
	mock.ExpectQuery(`SELECT nextval\('npc_acquaintance_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1206)))
	mock.ExpectExec(`INSERT INTO npc_acquaintance `).
		WithArgs(actA, name, tsTickedAt, int64(1206)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM npc_acquaintance .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1206)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectActorSlice3TailsEmpty(mock, 1306, 1406, 1506, 1606)

	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "Hannah", Coins: 20, State: "idle", StateEnteredAt: tsEntered,
			Acquaintances: map[string]sim.Acquaintance{name: {FirstInteractedAt: tsTickedAt}},
		},
	}
	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestActorsRepo_SaveSnapshot_AcquaintanceOverLongRunes — 101 multibyte
// runes exceeds VARCHAR(100) and is rejected (rune-counted, not byte).
func TestActorsRepo_SaveSnapshot_AcquaintanceOverLongRunes(t *testing.T) {
	mock, repo := newMockPoolA(t)
	name := strings.Repeat("é", 101) // 101 runes
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Acquaintances: map[string]sim.Acquaintance{name: {FirstInteractedAt: tsEntered}},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "exceeds 100 chars")
}

// --- Slice 3 (ZBBS-WORK-245): dwell / produce / room_access / attribute ---

// TestActorsRepo_LoadAll_Slice3 — round-trips one of each Slice 3 child
// onto a bare actor: an object-source and an item-source dwell credit
// (covering the remaining_ticks NULL/non-NULL pairing + the Kind-not-
// persisted gap), a produce-state anchor, a ledger room-access grant and
// a staff one (covering the Source derivation from granted_via_ledger_id),
// and a raw attribute blob.
func TestActorsRepo_LoadAll_Slice3(t *testing.T) {
	mock, repo := newMockPoolA(t)

	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(oneBareActorRows())
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	expectLoadAllContinuityEmpty(mock)

	rem := 3
	mock.ExpectQuery(`FROM actor_dwell_credit\b`).
		WillReturnRows(pgxmock.NewRows(dwellCreditColumns()).
			AddRow(actA, objX, "tiredness", "object", tsTickedAt, (*int)(nil), -2, 10).
			AddRow(actA, objX, "hunger", "item", tsBreak, &rem, -1, 5))
	mock.ExpectQuery(`FROM actor_produce_state\b`).
		WillReturnRows(pgxmock.NewRows(produceStateColumns()).
			AddRow(actA, "bread", &tsTickedAt))
	mock.ExpectQuery(`FROM room_access\b`).
		WillReturnRows(pgxmock.NewRows(roomAccessColumns()).
			AddRow(actA, int64(42), ptrInt64(77), tsEntered, &tsSleep, true).
			AddRow(actA, int64(7), (*int64)(nil), tsEntered, (*time.Time)(nil), true))
	mock.ExpectQuery(`FROM actor_attribute\b`).
		WillReturnRows(pgxmock.NewRows(attributeColumns()).
			AddRow(actA, "businessowner", []byte(`{"flavor":"flamboyant"}`)))

	actors, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	a := actors[actA]
	if a == nil {
		t.Fatal("actA missing")
	}

	// Dwell credits.
	objKey := sim.DwellCreditKey{ObjectID: objX, Attribute: "tiredness", Source: sim.DwellSourceObject}
	itemKey := sim.DwellCreditKey{ObjectID: objX, Attribute: "hunger", Source: sim.DwellSourceItem}
	if dc := a.DwellCredits[objKey]; dc == nil {
		t.Fatal("object-source dwell credit missing")
	} else if dc.RemainingTicks != nil || dc.DwellDelta != -2 || dc.DwellPeriodMinutes != 10 {
		t.Errorf("object dwell = %+v", dc)
	}
	if dc := a.DwellCredits[itemKey]; dc == nil {
		t.Fatal("item-source dwell credit missing")
	} else {
		if dc.RemainingTicks == nil || *dc.RemainingTicks != 3 {
			t.Errorf("item dwell remaining = %v, want 3", dc.RemainingTicks)
		}
		if dc.Kind != "" {
			t.Errorf("item dwell Kind = %q, want empty (not persisted)", dc.Kind)
		}
	}

	// Produce state.
	if ps := a.ProduceState["bread"]; ps == nil || !ps.LastProducedAt.Equal(tsTickedAt) {
		t.Errorf("produce state bread = %+v", ps)
	}

	// Room access — ledger Source derived from non-NULL ledger id, staff
	// from NULL.
	ledgerKey := sim.RoomAccessKey{RoomID: 42, Source: sim.AccessSourceLedger}
	staffKey := sim.RoomAccessKey{RoomID: 7, Source: sim.AccessSourceStaff}
	if ra := a.RoomAccess[ledgerKey]; ra == nil || ra.LedgerID != 77 || !ra.Active {
		t.Errorf("ledger room access = %+v", ra)
	}
	if ra := a.RoomAccess[staffKey]; ra == nil || ra.LedgerID != 0 {
		t.Errorf("staff room access = %+v", ra)
	}

	// Attribute raw bytes carried verbatim.
	if got := string(a.Attributes["businessowner"]); got != `{"flavor":"flamboyant"}` {
		t.Errorf("attribute params = %q", got)
	}
}

// TestActorsRepo_LoadAll_OrphanDwellCredit — a dwell credit whose actor is
// absent from the parent set is a hard error (schema drift / out-of-band).
func TestActorsRepo_LoadAll_OrphanDwellCredit(t *testing.T) {
	mock, repo := newMockPoolA(t)

	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(pgxmock.NewRows(actorParentColumns()))
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	expectLoadAllContinuityEmpty(mock)
	mock.ExpectQuery(`FROM actor_dwell_credit\b`).
		WillReturnRows(pgxmock.NewRows(dwellCreditColumns()).
			AddRow(actA, objX, "hunger", "object", tsTickedAt, (*int)(nil), -1, 5))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "orphan dwell credit") {
		t.Fatalf("err = %v, want orphan dwell credit", err)
	}
}

// TestActorsRepo_LoadAll_DwellCreditShapeRejected — load enforces the same
// remaining↔source pairing the baseline CHECK does: an object-source row
// with a non-NULL remaining_ticks is rejected.
func TestActorsRepo_LoadAll_DwellCreditShapeRejected(t *testing.T) {
	mock, repo := newMockPoolA(t)

	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(oneBareActorRows())
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	expectLoadAllContinuityEmpty(mock)
	bad := 4
	mock.ExpectQuery(`FROM actor_dwell_credit\b`).
		WillReturnRows(pgxmock.NewRows(dwellCreditColumns()).
			AddRow(actA, objX, "hunger", "object", tsTickedAt, &bad, -1, 5))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "non-nil remaining_ticks") {
		t.Fatalf("err = %v, want non-nil remaining_ticks", err)
	}
}

// TestActorsRepo_LoadAll_RoomAccessNonPositiveLedger — a row with a
// non-NULL but non-positive granted_via_ledger_id derives source=ledger
// yet stores an invalid LedgerID; load rejects it symmetrically with Save
// (R1 finding 1).
func TestActorsRepo_LoadAll_RoomAccessNonPositiveLedger(t *testing.T) {
	mock, repo := newMockPoolA(t)

	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(oneBareActorRows())
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	expectLoadAllContinuityEmpty(mock)
	mock.ExpectQuery(`FROM actor_dwell_credit\b`).WillReturnRows(emptyDwellRows())
	mock.ExpectQuery(`FROM actor_produce_state\b`).WillReturnRows(emptyProduceRows())
	mock.ExpectQuery(`FROM room_access\b`).
		WillReturnRows(pgxmock.NewRows(roomAccessColumns()).
			AddRow(actA, int64(42), ptrInt64(0), tsEntered, (*time.Time)(nil), true))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "non-positive ledger id") {
		t.Fatalf("err = %v, want non-positive ledger id", err)
	}
}

// TestActorsRepo_LoadAll_AttributeInvalidJSON — load enforces JSON validity
// on params symmetrically with Save (R1 finding 2).
func TestActorsRepo_LoadAll_AttributeInvalidJSON(t *testing.T) {
	mock, repo := newMockPoolA(t)

	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(oneBareActorRows())
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	expectLoadAllContinuityEmpty(mock)
	mock.ExpectQuery(`FROM actor_dwell_credit\b`).WillReturnRows(emptyDwellRows())
	mock.ExpectQuery(`FROM actor_produce_state\b`).WillReturnRows(emptyProduceRows())
	mock.ExpectQuery(`FROM room_access\b`).WillReturnRows(emptyRoomAccessRows())
	mock.ExpectQuery(`FROM actor_attribute\b`).
		WillReturnRows(pgxmock.NewRows(attributeColumns()).
			AddRow(actA, "businessowner", []byte("{not json")))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid JSON params") {
		t.Fatalf("err = %v, want invalid JSON params", err)
	}
}

// TestActorsRepo_LoadAll_OrphanAttribute — an attribute row whose actor is
// absent is a hard error. attribute is the last child loader, so all prior
// child queries are programmed empty first.
func TestActorsRepo_LoadAll_OrphanAttribute(t *testing.T) {
	mock, repo := newMockPoolA(t)

	mock.ExpectQuery(`FROM actor\b`).WillReturnRows(pgxmock.NewRows(actorParentColumns()))
	mock.ExpectQuery(`FROM actor_need\b`).WillReturnRows(emptyNeedRows())
	mock.ExpectQuery(`FROM actor_inventory\b`).WillReturnRows(emptyInvRows())
	expectLoadAllContinuityEmpty(mock)
	mock.ExpectQuery(`FROM actor_dwell_credit\b`).WillReturnRows(emptyDwellRows())
	mock.ExpectQuery(`FROM actor_produce_state\b`).WillReturnRows(emptyProduceRows())
	mock.ExpectQuery(`FROM room_access\b`).WillReturnRows(emptyRoomAccessRows())
	mock.ExpectQuery(`FROM actor_attribute\b`).
		WillReturnRows(pgxmock.NewRows(attributeColumns()).
			AddRow(actA, "businessowner", []byte(`{}`)))

	_, err := repo.LoadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "orphan attribute") {
		t.Fatalf("err = %v, want orphan attribute", err)
	}
}

// TestActorsRepo_SaveSnapshot_Slice3 — full write path for one of each
// Slice 3 child (single-entry maps keep the UPSERT order deterministic
// under pgxmock's ordered matching). Asserts SQL shape + arg bindings
// including kind synthesis (ledger→private) and the jsonb params cast.
func TestActorsRepo_SaveSnapshot_Slice3(t *testing.T) {
	mock, repo := newMockPoolA(t)

	expectActorSaveSnapshotPrelude(mock, 710)
	mock.ExpectExec(`INSERT INTO actor `).
		WithArgs(
			actA, "Hannah", 0, 0,
			nil, nil, nil,
			nil, nil,
			20, nil, nil, nil,
			nil, nil,
			(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
			nil, (*time.Time)(nil),
			int64(0), "idle", tsEntered,
			nil, "south",
			int64(710),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(710)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	expectActorSaveSnapshotChildTails(mock, 810, 910)
	expectActorContinuityTailsEmpty(mock, 410, 510, 610)

	// Dwell credit tier (one item-source credit).
	rem := 3
	mock.ExpectQuery(`SELECT nextval\('actor_dwell_credit_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1110)))
	mock.ExpectExec(`INSERT INTO actor_dwell_credit `).
		WithArgs(actA, objX, "hunger", "item", tsTickedAt, &rem, -1, 5, int64(1110)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_dwell_credit .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1110)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Produce state tier.
	mock.ExpectQuery(`SELECT nextval\('actor_produce_state_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1210)))
	mock.ExpectExec(`INSERT INTO actor_produce_state `).
		WithArgs(actA, "bread", tsTickedAt, int64(1210)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_produce_state .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1210)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Room access tier (ledger grant → kind synthesized as private).
	mock.ExpectQuery(`SELECT nextval\('room_access_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1310)))
	mock.ExpectExec(`INSERT INTO room_access `).
		WithArgs(int64(42), actA, int64(77), tsEntered, &tsSleep, "private", true, int64(1310)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM room_access .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1310)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	// Attribute tier (raw params written verbatim with the ::jsonb cast).
	mock.ExpectQuery(`SELECT nextval\('actor_attribute_snapshot_gen_seq`).
		WillReturnRows(pgxmock.NewRows([]string{"nextval"}).AddRow(int64(1410)))
	mock.ExpectExec(`INSERT INTO actor_attribute `).
		WithArgs(actA, "businessowner", `{"flavor":"flamboyant"}`, int64(1410)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`DELETE FROM actor_attribute .*WHERE snapshot_gen < \$1`).
		WithArgs(int64(1410)).
		WillReturnResult(pgconn.NewCommandTag("DELETE 0"))

	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "Hannah", Coins: 20, State: "idle", StateEnteredAt: tsEntered,
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: objX, Attribute: "hunger", Source: sim.DwellSourceItem}: {
					ObjectID: objX, Attribute: "hunger", Source: sim.DwellSourceItem,
					LastCreditedAt: tsTickedAt, RemainingTicks: &rem, DwellDelta: -1, DwellPeriodMinutes: 5,
				},
			},
			ProduceState: map[sim.ItemKind]*sim.ProduceState{
				"bread": {Item: "bread", LastProducedAt: tsTickedAt},
			},
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 42, Source: sim.AccessSourceLedger}: {
					RoomID: 42, Source: sim.AccessSourceLedger, LedgerID: 77,
					ExpiresAt: &tsSleep, Active: true, CreatedAt: tsEntered,
				},
			},
			Attributes: map[string][]byte{
				"businessowner": []byte(`{"flavor":"flamboyant"}`),
			},
		},
	}

	if err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestActorsRepo_SaveSnapshot_DwellCreditItemNilRemaining — an item-source
// credit must carry a remaining_ticks countdown (pre-pass rejection,
// mirrors the baseline pairing CHECK).
func TestActorsRepo_SaveSnapshot_DwellCreditItemNilRemaining(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: objX, Attribute: "hunger", Source: sim.DwellSourceItem}: {
					ObjectID: objX, Attribute: "hunger", Source: sim.DwellSourceItem,
					LastCreditedAt: tsTickedAt, RemainingTicks: nil, DwellDelta: -1, DwellPeriodMinutes: 5,
				},
			},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "nil remaining_ticks")
}

// TestActorsRepo_SaveSnapshot_RoomAccessNonPositiveRoom — a room-access
// key with room_id <= 0 is rejected (RoomID 0 is the "not in a room"
// sentinel; a grant for it is corruption).
func TestActorsRepo_SaveSnapshot_RoomAccessNonPositiveRoom(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 0, Source: sim.AccessSourceLedger}: {
					RoomID: 0, Source: sim.AccessSourceLedger, LedgerID: 1, CreatedAt: tsEntered,
				},
			},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "non-positive room_id")
}

// TestActorsRepo_SaveSnapshot_RoomAccessLedgerMissingLedgerID — a ledger
// grant must carry a positive LedgerID so the load-side Source derivation
// (non-NULL ledger id ⇒ ledger) round-trips.
func TestActorsRepo_SaveSnapshot_RoomAccessLedgerMissingLedgerID(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 42, Source: sim.AccessSourceLedger}: {
					RoomID: 42, Source: sim.AccessSourceLedger, LedgerID: 0, CreatedAt: tsEntered,
				},
			},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "non-positive ledger id")
}

// TestActorsRepo_SaveSnapshot_RoomAccessDuplicateActivePrivate — two actors
// each holding an active ledger (private) grant for the same room violates
// ux_room_access_one_private_active; the cross-actor pre-pass guard rejects
// it before any UPSERT (R1 finding 5).
func TestActorsRepo_SaveSnapshot_RoomAccessDuplicateActivePrivate(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "A", State: "idle", StateEnteredAt: tsEntered,
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 42, Source: sim.AccessSourceLedger}: {
					RoomID: 42, Source: sim.AccessSourceLedger, LedgerID: 77, Active: true, CreatedAt: tsEntered,
				},
			},
		},
		actB: {
			ID: actB, DisplayName: "B", State: "idle", StateEnteredAt: tsEntered,
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 42, Source: sim.AccessSourceLedger}: {
					RoomID: 42, Source: sim.AccessSourceLedger, LedgerID: 88, Active: true, CreatedAt: tsEntered,
				},
			},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "active ledger (private) grant for room")
}

// TestActorsRepo_SaveSnapshot_RoomAccessDuplicateRoom — two in-memory
// grants for the same room under different sources would collide on the
// (room_id, actor_id) PK; the pre-pass rejects it.
func TestActorsRepo_SaveSnapshot_RoomAccessDuplicateRoom(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 42, Source: sim.AccessSourceLedger}: {
					RoomID: 42, Source: sim.AccessSourceLedger, LedgerID: 77, CreatedAt: tsEntered,
				},
				{RoomID: 42, Source: sim.AccessSourceStaff}: {
					RoomID: 42, Source: sim.AccessSourceStaff, LedgerID: 0, CreatedAt: tsEntered,
				},
			},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "two room-access entries for room")
}

// TestActorsRepo_SaveSnapshot_AttributeInvalidJSON — a params blob that
// isn't valid JSON would trip the ::jsonb cast mid-Tx; reject in the
// pre-pass for a clean error.
func TestActorsRepo_SaveSnapshot_AttributeInvalidJSON(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Attributes: map[string][]byte{"businessowner": []byte("{not json")},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "invalid JSON params")
}

// TestActorsRepo_SaveSnapshot_AttributeOverLongSlug — slug exceeds
// VARCHAR(64).
func TestActorsRepo_SaveSnapshot_AttributeOverLongSlug(t *testing.T) {
	mock, repo := newMockPoolA(t)
	actors := map[sim.ActorID]*sim.Actor{
		actA: {
			ID: actA, DisplayName: "X", State: "idle", StateEnteredAt: tsEntered,
			Attributes: map[string][]byte{strings.Repeat("a", 65): []byte(`{}`)},
		},
	}
	err := repo.SaveSnapshot(context.Background(), fakeTx{mock: mock}, actors)
	assertValidationOnly(t, mock, err, "exceeds 64 chars")
}

// --- helpers --------------------------------------------------------------

// ptrStr returns &s as *string for AddRow nullable-column fixtures.
func ptrStr(s string) *string { return &s }

// ptrInt64 returns &v as *int64 for AddRow nullable-column fixtures.
func ptrInt64(v int64) *int64 { return &v }
