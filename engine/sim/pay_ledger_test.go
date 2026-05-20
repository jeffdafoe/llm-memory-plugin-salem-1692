package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pay_ledger_test.go — Phase 3 PR S4 substrate coverage. Exercises the
// pay-ledger types and helpers that ship with pay_ledger.go:
//
//   - ClonePayLedgerEntry (deep-copy slice independence).
//   - World.nextLedgerSeq (counter monotonicity + sentinel).
//   - effectivePayLedgerTTL / effectivePayLedgerSweepCadence defaults.
//   - restartExpirePendingEntries state-machine flip + non-pending skip.
//   - Snapshot.PayLedger deep-clone isolation through republish.
//   - LoadWorld pay-ledger counter safety floor.
//
// Command Fn coverage (pay_with_item / accept_pay / decline_pay /
// counter_pay / withdraw_pay) lands in a later test file alongside
// the commands themselves; that's a separate ship step. These
// substrate-only tests bypass the command queue (no World.Run) by
// mutating world state directly via test-exported helpers — sim.NewWorld
// returns a *World whose unexported fields are reachable from inside
// the sim package's export_test.go shims.

// TestClonePayLedgerEntry_NilInputReturnsNil — defensive: clone of nil
// pointer is nil (matches CloneSceneQuote / CloneActor conventions).
func TestClonePayLedgerEntry_NilInputReturnsNil(t *testing.T) {
	if got := sim.ClonePayLedgerEntry(nil); got != nil {
		t.Errorf("ClonePayLedgerEntry(nil) = %+v, want nil", got)
	}
}

// TestClonePayLedgerEntry_DeepCopiesConsumerIDs — mutating the original's
// ConsumerIDs slice must not leak to the clone. This is the only
// pointer/slice field on PayLedgerEntry today, so it's the only one
// that needs explicit deep-copy verification.
func TestClonePayLedgerEntry_DeepCopiesConsumerIDs(t *testing.T) {
	orig := &sim.PayLedgerEntry{
		ID:          7,
		BuyerID:     "alice",
		SellerID:    "bob",
		ItemKind:    "stew",
		Qty:         2,
		Amount:      8,
		ConsumerIDs: []sim.ActorID{"alice", "carl"},
		State:       sim.PayLedgerStatePending,
	}
	clone := sim.ClonePayLedgerEntry(orig)
	if clone == nil {
		t.Fatalf("ClonePayLedgerEntry returned nil for non-nil input")
	}
	if clone.ID != 7 || clone.BuyerID != "alice" || clone.SellerID != "bob" ||
		clone.ItemKind != "stew" || clone.Qty != 2 || clone.Amount != 8 ||
		clone.State != sim.PayLedgerStatePending {
		t.Errorf("scalar fields not preserved on clone: %+v", clone)
	}
	if len(clone.ConsumerIDs) != 2 ||
		clone.ConsumerIDs[0] != "alice" || clone.ConsumerIDs[1] != "carl" {
		t.Fatalf("ConsumerIDs not preserved on clone: %+v", clone.ConsumerIDs)
	}
	orig.ConsumerIDs[0] = "mallory"
	if clone.ConsumerIDs[0] != "alice" {
		t.Errorf("ConsumerIDs aliased: mutating original leaked to clone")
	}
}

// TestClonePayLedgerEntry_NilConsumerIDsStaysNil — defensive: a nil
// ConsumerIDs slice on the input must produce a nil slice on the
// output, not an empty allocated slice. Matches CloneSceneQuote.
func TestClonePayLedgerEntry_NilConsumerIDsStaysNil(t *testing.T) {
	orig := &sim.PayLedgerEntry{ID: 1, ConsumerIDs: nil}
	clone := sim.ClonePayLedgerEntry(orig)
	if clone.ConsumerIDs != nil {
		t.Errorf("nil ConsumerIDs allocated on clone: %v", clone.ConsumerIDs)
	}
}

// TestWorld_NextLedgerSeqStartsAtOne — the first mint after NewWorld
// returns LedgerID(1); LedgerID(0) is the reserved unset sentinel
// (also used for ParentID="no parent" and QuoteID="no quote referenced").
func TestWorld_NextLedgerSeqStartsAtOne(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	got := sim.NextLedgerSeq(w)
	if got != 1 {
		t.Errorf("first nextLedgerSeq = %d, want 1 (LedgerID(0) is the unset sentinel)", got)
	}
}

// TestWorld_NextLedgerSeqMonotonic — successive mints monotonically
// increase. World-goroutine-only contract holds for production callers;
// this test pokes the unexported counter directly via the export hook.
func TestWorld_NextLedgerSeqMonotonic(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	const n = 5
	for i := 0; i < n; i++ {
		want := sim.LedgerID(i + 1)
		if got := sim.NextLedgerSeq(w); got != want {
			t.Errorf("nextLedgerSeq[%d] = %d, want %d", i, got, want)
		}
	}
}

// TestEffectivePayLedgerTTL_FallbackToDefault — zero settings returns
// PayLedgerTTLDefault; tests that don't seed an environment shouldn't
// need to set this manually.
func TestEffectivePayLedgerTTL_FallbackToDefault(t *testing.T) {
	got := sim.EffectivePayLedgerTTL(sim.WorldSettings{})
	if got != sim.PayLedgerTTLDefault {
		t.Errorf("EffectivePayLedgerTTL(zero) = %v, want %v", got, sim.PayLedgerTTLDefault)
	}
}

// TestEffectivePayLedgerTTL_HonorsConfigured — non-zero settings pass
// through unchanged.
func TestEffectivePayLedgerTTL_HonorsConfigured(t *testing.T) {
	want := 90 * time.Second
	got := sim.EffectivePayLedgerTTL(sim.WorldSettings{PayLedgerTTL: want})
	if got != want {
		t.Errorf("EffectivePayLedgerTTL(%v) = %v, want %v", want, got, want)
	}
}

// TestEffectivePayLedgerSweepCadence_FallbackToDefault — zero settings
// returns the 60s default.
func TestEffectivePayLedgerSweepCadence_FallbackToDefault(t *testing.T) {
	got := sim.EffectivePayLedgerSweepCadence(sim.WorldSettings{})
	if got != sim.PayLedgerSweepCadenceDefault {
		t.Errorf("EffectivePayLedgerSweepCadence(zero) = %v, want %v",
			got, sim.PayLedgerSweepCadenceDefault)
	}
}

// TestEffectivePayLedgerSweepCadence_HonorsConfigured — non-zero passes
// through.
func TestEffectivePayLedgerSweepCadence_HonorsConfigured(t *testing.T) {
	want := 15 * time.Second
	got := sim.EffectivePayLedgerSweepCadence(sim.WorldSettings{PayLedgerSweepCadence: want})
	if got != want {
		t.Errorf("EffectivePayLedgerSweepCadence(%v) = %v, want %v", want, got, want)
	}
}

// TestRestartExpirePendingEntries_FlipsExpiredPending — a pending entry
// whose ExpiresAt has already passed is flipped to Expired with
// ResolvedAt stamped at `now`. No event emitted (cascade root is gone
// post-restart — the original PayOfferReceived event no longer exists).
func TestRestartExpirePendingEntries_FlipsExpiredPending(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	now := time.Now().UTC()
	staleExpiry := now.Add(-time.Minute)
	w.PayLedger[42] = &sim.PayLedgerEntry{
		ID:        42,
		State:     sim.PayLedgerStatePending,
		ExpiresAt: staleExpiry,
	}
	sim.RestartExpirePendingEntries(w, now)
	got := w.PayLedger[42]
	if got.State != sim.PayLedgerStateExpired {
		t.Errorf("State = %q, want %q", got.State, sim.PayLedgerStateExpired)
	}
	if !got.ResolvedAt.Equal(now) {
		t.Errorf("ResolvedAt = %v, want %v", got.ResolvedAt, now)
	}
}

// TestRestartExpirePendingEntries_PreservesActivePending — a pending
// entry with ExpiresAt in the future is left alone for the aging sweep
// to pick up at its natural TTL boundary.
func TestRestartExpirePendingEntries_PreservesActivePending(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	now := time.Now().UTC()
	future := now.Add(2 * time.Minute)
	w.PayLedger[1] = &sim.PayLedgerEntry{
		ID:        1,
		State:     sim.PayLedgerStatePending,
		ExpiresAt: future,
	}
	sim.RestartExpirePendingEntries(w, now)
	got := w.PayLedger[1]
	if got.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending (future ExpiresAt should not flip)", got.State)
	}
	if !got.ResolvedAt.IsZero() {
		t.Errorf("ResolvedAt = %v, want zero (still pending)", got.ResolvedAt)
	}
}

// TestRestartExpirePendingEntries_SkipsTerminal — any non-pending entry
// is inert under restartExpirePendingEntries — even an accepted entry
// stamped with an ExpiresAt in the past stays accepted.
func TestRestartExpirePendingEntries_SkipsTerminal(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	now := time.Now().UTC()
	stale := now.Add(-time.Hour)
	w.PayLedger[1] = &sim.PayLedgerEntry{
		ID:         1,
		State:      sim.PayLedgerStateAccepted,
		ExpiresAt:  stale,
		ResolvedAt: stale,
	}
	sim.RestartExpirePendingEntries(w, now)
	got := w.PayLedger[1]
	if got.State != sim.PayLedgerStateAccepted {
		t.Errorf("State = %q, want accepted (terminal entries must be inert)", got.State)
	}
	if !got.ResolvedAt.Equal(stale) {
		t.Errorf("ResolvedAt = %v, want %v (terminal entries must not have their ResolvedAt touched)",
			got.ResolvedAt, stale)
	}
}

// TestRestartExpirePendingEntries_NilWorldNoPanic — defensive: a nil
// World pointer is a caller bug, but the helper guards against it
// rather than panicking. Mirrors restartExpireScannedQuotes' guard.
func TestRestartExpirePendingEntries_NilWorldNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("RestartExpirePendingEntries(nil, now) panicked: %v", r)
		}
	}()
	sim.RestartExpirePendingEntries(nil, time.Now())
}

// TestRestartExpirePendingEntries_ZeroExpiresAtLeftPending — defensive:
// an entry with a zero ExpiresAt is not aged out (zero is the unset
// sentinel; the contract is that pending entries always carry a
// non-zero ExpiresAt, but the helper is conservative).
func TestRestartExpirePendingEntries_ZeroExpiresAtLeftPending(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	now := time.Now().UTC()
	w.PayLedger[1] = &sim.PayLedgerEntry{
		ID:    1,
		State: sim.PayLedgerStatePending,
	}
	sim.RestartExpirePendingEntries(w, now)
	got := w.PayLedger[1]
	if got.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending (zero ExpiresAt should be treated as unset)", got.State)
	}
}

// TestEffectivePayLedgerTerminalRetention — default when unset, honors a
// custom value at/above the floor, and re-asserts the
// PayLedgerInResponseToWindow floor when configured shorter (so a
// countered parent can never be reaped while still referenceable).
func TestEffectivePayLedgerTerminalRetention(t *testing.T) {
	cases := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"unset uses default", 0, sim.PayLedgerTerminalRetentionDefault},
		{"negative uses default", -time.Minute, sim.PayLedgerTerminalRetentionDefault},
		{"custom above floor honored", 3 * time.Hour, 3 * time.Hour},
		{"too-short floored", 5 * time.Minute, sim.PayLedgerInResponseToWindow},
		{"exactly floor honored", sim.PayLedgerInResponseToWindow, sim.PayLedgerInResponseToWindow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sim.EffectivePayLedgerTerminalRetention(sim.WorldSettings{PayLedgerTerminalRetention: tc.set})
			if got != tc.want {
				t.Errorf("retention = %v, want %v", got, tc.want)
			}
		})
	}
	// The default itself must never be below the floor.
	if sim.PayLedgerTerminalRetentionDefault < sim.PayLedgerInResponseToWindow {
		t.Errorf("default %v < in_response_to floor %v", sim.PayLedgerTerminalRetentionDefault, sim.PayLedgerInResponseToWindow)
	}
}

// TestReapTerminalPayLedgerEntries — terminal entries older than the
// retention window are removed; pending entries, recently-resolved
// terminals, just-this-tick terminals, and zero-ResolvedAt terminals are
// all left in place. Nil entries are swept defensively.
func TestReapTerminalPayLedgerEntries(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	now := time.Now().UTC()
	retention := sim.EffectivePayLedgerTerminalRetention(w.Settings) // default 1h

	// 1: terminal, resolved well past retention → reaped.
	w.PayLedger[1] = &sim.PayLedgerEntry{
		ID: 1, State: sim.PayLedgerStateDeclined,
		ResolvedAt: now.Add(-retention - time.Minute),
	}
	// 2: terminal, resolved within retention → survives.
	w.PayLedger[2] = &sim.PayLedgerEntry{
		ID: 2, State: sim.PayLedgerStateExpired,
		ResolvedAt: now.Add(-retention + time.Minute),
	}
	// 3: pending → never reaped regardless of age.
	w.PayLedger[3] = &sim.PayLedgerEntry{
		ID: 3, State: sim.PayLedgerStatePending,
		CreatedAt: now.Add(-24 * time.Hour),
		ExpiresAt: now.Add(time.Minute),
	}
	// 4: terminal, resolved exactly now (just flipped this tick) → survives.
	w.PayLedger[4] = &sim.PayLedgerEntry{
		ID: 4, State: sim.PayLedgerStateAccepted,
		ResolvedAt: now,
	}
	// 5: terminal but zero ResolvedAt → skipped (defensive).
	w.PayLedger[5] = &sim.PayLedgerEntry{
		ID: 5, State: sim.PayLedgerStateWithdrawnByBuyer,
	}
	// 6: nil entry → deleted.
	w.PayLedger[6] = nil

	sim.ReapTerminalPayLedgerEntries(w, now)

	if _, ok := w.PayLedger[1]; ok {
		t.Error("entry 1 (old terminal) should have been reaped")
	}
	if _, ok := w.PayLedger[2]; !ok {
		t.Error("entry 2 (recent terminal) should survive")
	}
	if _, ok := w.PayLedger[3]; !ok {
		t.Error("entry 3 (pending) should survive")
	}
	if _, ok := w.PayLedger[4]; !ok {
		t.Error("entry 4 (resolved this tick) should survive")
	}
	if _, ok := w.PayLedger[5]; !ok {
		t.Error("entry 5 (zero ResolvedAt) should be skipped, not reaped")
	}
	if _, ok := w.PayLedger[6]; ok {
		t.Error("entry 6 (nil) should have been deleted")
	}
}

// TestReapTerminalPayLedgerEntries_CounteredFloorProtection — even with a
// misconfigured sub-floor retention setting, a countered parent resolved
// inside the in_response_to window is NOT reaped, because the effective
// retention is floored at PayLedgerInResponseToWindow. Reaping it would
// dangle a buyer's pending in_response_to follow-up.
func TestReapTerminalPayLedgerEntries_CounteredFloorProtection(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	w.Settings.PayLedgerTerminalRetention = time.Nanosecond // absurdly short
	now := time.Now().UTC()

	// Countered parent resolved 30 min ago — well inside the 1h
	// in_response_to window, so still referenceable.
	w.PayLedger[1] = &sim.PayLedgerEntry{
		ID: 1, State: sim.PayLedgerStateCountered,
		ResolvedAt: now.Add(-30 * time.Minute),
	}
	sim.ReapTerminalPayLedgerEntries(w, now)
	if _, ok := w.PayLedger[1]; !ok {
		t.Error("countered parent inside in_response_to window was reaped; the floor must protect it")
	}
}

// TestSnapshot_PayLedgerIsolation — mutating a PayLedger entry after
// republish must NOT leak to the published snapshot. The snapshot is
// the contract surface for admin readers / reconciliation jobs, so
// it has to be independent of subsequent world mutations.
func TestSnapshot_PayLedgerIsolation(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	w.PayLedger[5] = &sim.PayLedgerEntry{
		ID:          5,
		State:       sim.PayLedgerStatePending,
		Amount:      10,
		ConsumerIDs: []sim.ActorID{"alice", "carl"},
	}
	sim.RepublishForTest(w)
	snap := w.Published()
	snapEntry, ok := snap.PayLedger[5]
	if !ok {
		t.Fatalf("Snapshot.PayLedger missing ID 5")
	}
	if snapEntry.State != sim.PayLedgerStatePending || snapEntry.Amount != 10 {
		t.Errorf("Snapshot entry not preserved: %+v", snapEntry)
	}

	// Mutate the live world entry — snapshot must not change.
	live := w.PayLedger[5]
	live.State = sim.PayLedgerStateAccepted
	live.Amount = 999
	live.ConsumerIDs[0] = "mallory"
	if snapEntry.State != sim.PayLedgerStatePending {
		t.Errorf("snapshot State leaked: %q", snapEntry.State)
	}
	if snapEntry.Amount != 10 {
		t.Errorf("snapshot Amount leaked: %d", snapEntry.Amount)
	}
	if snapEntry.ConsumerIDs[0] != "alice" {
		t.Errorf("snapshot ConsumerIDs leaked: %q", snapEntry.ConsumerIDs[0])
	}
}

// TestLoadWorld_LedgerCounterSafetyFloor — if a repo somehow loaded
// PayLedger entries whose max ID exceeded the loaded payLedgerSeq, the
// counter must be bumped so the next mint doesn't collide. Idempotent:
// when the counter was already correct, the result is the same.
//
// No PayLedgerRepo exists yet, so this test seeds entries directly
// into World.PayLedger (simulating the future loaded-from-repo state)
// and invokes the floor loop via the exported test hook.
func TestLoadWorld_LedgerCounterSafetyFloor(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	w.PayLedger[42] = &sim.PayLedgerEntry{ID: 42, State: sim.PayLedgerStateAccepted}
	w.PayLedger[7] = &sim.PayLedgerEntry{ID: 7, State: sim.PayLedgerStateDeclined}
	// payLedgerSeq is still 0 from NewWorld; without the safety floor
	// the next nextLedgerSeq() would mint LedgerID(1) and ignore the
	// loaded high-water mark.
	sim.ApplyPayLedgerCounterSafetyFloor(w)
	if got := sim.PayLedgerSeqForTest(w); got != 42 {
		t.Errorf("payLedgerSeq after floor = %d, want 42 (max loaded ID)", got)
	}
	if got := sim.NextLedgerSeq(w); got != 43 {
		t.Errorf("nextLedgerSeq after floor = %d, want 43 (max(42, 7) + 1)", got)
	}
}

// TestLoadWorld_LedgerCounterSafetyFloor_NoEntries — empty PayLedger
// leaves the counter alone. Idempotent baseline for the floor loop.
func TestLoadWorld_LedgerCounterSafetyFloor_NoEntries(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	sim.ApplyPayLedgerCounterSafetyFloor(w)
	if got := sim.PayLedgerSeqForTest(w); got != 0 {
		t.Errorf("payLedgerSeq with empty PayLedger = %d, want 0", got)
	}
}

// ---- PayOfferWarrantReason — the load-bearing piece of the PR S4
// DedupDiscriminator interface migration. -----------------------------

// TestPayOfferWarrantReason_Kind — Kind() returns WarrantKindPayOffer
// regardless of the reason's payload values. Pins the discriminator
// at the type level — payload + kind can't drift apart.
func TestPayOfferWarrantReason_Kind(t *testing.T) {
	r := sim.PayOfferWarrantReason{LedgerID: 42}
	if got := r.Kind(); got != sim.WarrantKindPayOffer {
		t.Errorf("Kind() = %q, want %q", got, sim.WarrantKindPayOffer)
	}
}

// TestPayOfferWarrantReason_DedupDiscriminatorIsLedgerID — the reason's
// DedupDiscriminator returns uint64(LedgerID). This is the load-bearing
// case for the entire PR S4 WarrantReason interface migration: the
// LedgerID survives LoadWorld (it's checkpointed world state), so a
// restart re-stamp uses the same discriminator as the normal-flow
// stamp and the two dedupe against each other automatically.
func TestPayOfferWarrantReason_DedupDiscriminatorIsLedgerID(t *testing.T) {
	cases := []sim.LedgerID{1, 7, 42, 1_000_000}
	for _, id := range cases {
		r := sim.PayOfferWarrantReason{LedgerID: id}
		if got := r.DedupDiscriminator(); got != uint64(id) {
			t.Errorf("LedgerID %d: DedupDiscriminator() = %d, want %d", id, got, uint64(id))
		}
	}
}

// TestPayOfferWarrantReason_RestartStableSourceKey — two warrant metas
// for the same offer (same LedgerID, but ONE with a normal-flow source
// event ID and ONE with a zero "restart re-stamp" source event ID)
// produce identical WarrantSourceKeys. Without the per-Reason
// DedupDiscriminator override, the normal-flow key would be
// (PayOffer, EventID) and the restart-flow key would be (PayOffer, 0)
// — and they wouldn't dedupe, producing a duplicate warrant on
// restart. With the override, both paths produce (PayOffer, LedgerID).
//
// This is the corner case that motivated the whole interface
// migration. Lock it explicitly.
func TestPayOfferWarrantReason_RestartStableSourceKey(t *testing.T) {
	const sameLedgerID sim.LedgerID = 17
	reason := sim.PayOfferWarrantReason{LedgerID: sameLedgerID, Buyer: "alice"}

	// Normal-flow stamp: source event populated.
	normalFlow := sim.WarrantMeta{
		Reason:        reason,
		SourceEventID: sim.EventID(42),
		RootEventID:   sim.EventID(42),
	}
	// Restart re-stamp: source event gone (LoadWorld wipes it).
	restartFlow := sim.WarrantMeta{
		Reason: reason,
		// SourceEventID intentionally zero
	}

	normalKey := sim.SourceKey(normalFlow)
	restartKey := sim.SourceKey(restartFlow)
	if normalKey != restartKey {
		t.Errorf("normal-flow key %+v != restart-flow key %+v — restart re-stamp would NOT dedupe against the normal stamp, defeating the purpose of the DedupDiscriminator migration",
			normalKey, restartKey)
	}
	// Both should specifically be (PayOffer, 17).
	if normalKey.Kind != sim.WarrantKindPayOffer {
		t.Errorf("normal key Kind = %q, want %q", normalKey.Kind, sim.WarrantKindPayOffer)
	}
	if normalKey.Discriminator != uint64(sameLedgerID) {
		t.Errorf("normal key Discriminator = %d, want %d (uint64 of LedgerID)",
			normalKey.Discriminator, uint64(sameLedgerID))
	}

	// Both metas must also report EventSourced() == true (non-zero
	// discriminator means "participates in dedup"), even though the
	// restart-flow meta has SourceEventID == 0. Without the migration,
	// eventSourced() keyed on meta.SourceEventID and the restart-flow
	// meta would have bypassed dedup entirely.
	if !sim.EventSourced(normalFlow) {
		t.Error("normal-flow EventSourced() == false — PayOfferWarrantReason with non-zero LedgerID must participate in dedup")
	}
	if !sim.EventSourced(restartFlow) {
		t.Error("restart-flow EventSourced() == false — restart re-stamp must still participate in dedup")
	}
}
