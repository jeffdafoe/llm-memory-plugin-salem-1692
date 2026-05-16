package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// restart_restamp_test.go — Phase 3 PR S4 step 7. Coverage of
// restartReStampPayOfferWarrants and the DedupDiscriminator interlock
// against a normal-flow stamp emitted afterward.
//
// The substrate-level dedup contract (WarrantSourceKey{Kind: PayOffer,
// Discriminator: LedgerID} stable across normal-flow + restart flow)
// is pinned by pay_ledger_test.go's
// TestPayOfferWarrantReason_RestartStableSourceKey. This file goes one
// level deeper and exercises the end-to-end flow: re-stamp at restart
// + later normal-flow tryStampWarrant call must produce a SINGLE
// warrant on the seller.

// rstActor — minimal seed for restart-restamp tests.
type rstActor struct {
	id          sim.ActorID
	displayName string
	kind        sim.ActorKind
}

func buildRestampWorld(t *testing.T, actors []rstActor) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	seed := make(map[sim.ActorID]*sim.Actor, len(actors))
	for _, a := range actors {
		seed[a.id] = &sim.Actor{
			ID:               a.id,
			DisplayName:      a.displayName,
			Kind:             a.kind,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		}
	}
	handles.Actors.Seed(seed)
	w := sim.NewWorld(repo)
	// Mirror what LoadWorld does so the world is in a sensible
	// pre-Run state without actually starting the goroutine (these
	// tests prefer direct mutation over Send round-trips).
	for id, a := range seed {
		w.Actors[id] = a
		_ = id
	}
	return w
}

// TestRestartReStamp_StampsSellerWarrant — basic happy path. A pending
// entry seeded into World.PayLedger produces a PayOfferWarrant on the
// seller after the re-stamp pass.
func TestRestartReStamp_StampsSellerWarrant(t *testing.T) {
	w := buildRestampWorld(t, []rstActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared},
	})
	now := time.Now().UTC()
	w.PayLedger[17] = &sim.PayLedgerEntry{
		ID: 17, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		ExpiresAt: now.Add(3 * time.Minute),
		SceneID:   "sc1", HuddleID: "h1",
		CreatedAt: now.Add(-time.Minute),
	}
	sim.RestartReStampPayOfferWarrants(w, now)

	bob := w.Actors["bob"]
	if bob == nil || len(bob.Warrants) != 1 {
		t.Fatalf("bob warrants = %d, want 1", len(bob.Warrants))
	}
	reason, ok := bob.Warrants[0].Reason.(sim.PayOfferWarrantReason)
	if !ok {
		t.Fatalf("Reason = %T", bob.Warrants[0].Reason)
	}
	if reason.LedgerID != 17 {
		t.Errorf("LedgerID = %d, want 17", reason.LedgerID)
	}
	// Restart re-stamp uses SourceEventID=0 (the original event is gone),
	// but DedupDiscriminator returns uint64(LedgerID)=17 anyway —
	// that's the load-bearing property of the migration.
	if bob.Warrants[0].SourceEventID != 0 {
		t.Errorf("SourceEventID = %d, want 0 (restart re-stamp; original event is gone)", bob.Warrants[0].SourceEventID)
	}
}

// TestRestartReStamp_DedupesAgainstNormalFlow — the load-bearing
// interlock. After the re-stamp, a normal-flow stamp on the same
// PayOfferWarrantReason (LedgerID 17, this time WITH a non-zero
// SourceEventID) must be dropped at the open-cycle dedup gate. Bob
// ends with exactly ONE warrant.
//
// This is what makes "calling LoadWorld twice on the same checkpoint"
// safe — and what unblocks the eventual cutover where a tick cascade
// re-fires PayOfferReceived after restart for a pending entry that
// pre-existed.
func TestRestartReStamp_DedupesAgainstNormalFlow(t *testing.T) {
	w := buildRestampWorld(t, []rstActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared},
	})
	now := time.Now().UTC()
	w.PayLedger[17] = &sim.PayLedgerEntry{
		ID: 17, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		ExpiresAt: now.Add(3 * time.Minute),
		SceneID:   "sc1", HuddleID: "h1",
		CreatedAt: now.Add(-time.Minute),
	}
	sim.RestartReStampPayOfferWarrants(w, now)
	// Now a normal-flow PayOfferReceived subscriber would do
	// tryStampWarrant with a non-zero SourceEventID but the same
	// PayOfferWarrantReason (same LedgerID).
	normalMeta := sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason: sim.PayOfferWarrantReason{
			LedgerID: 17, Buyer: "alice", Item: "stew",
			Qty: 1, Amount: 4, ConsumeNow: false,
			ExpiresAt: now.Add(3 * time.Minute),
		},
		SourceEventID: 99,
		RootEventID:   99,
		SourceActorID: "alice",
		HuddleID:      "h1",
		SceneID:       "sc1",
		OccurredAt:    now,
	}
	sim.TryStampWarrant(w, w.Actors["bob"], normalMeta, now)
	bob := w.Actors["bob"]
	if len(bob.Warrants) != 1 {
		t.Fatalf("after both stamps: bob warrants = %d, want 1 (dedup must collapse on LedgerID)", len(bob.Warrants))
	}
	// The retained warrant is the first one (re-stamp). Open-cycle
	// dedup is "first wins" — the second stamp for the same source
	// key is dropped.
	if got := bob.Warrants[0].Reason.(sim.PayOfferWarrantReason); got.LedgerID != 17 {
		t.Errorf("retained warrant LedgerID = %d, want 17", got.LedgerID)
	}
}

// TestRestartReStamp_SkipsNonPending — terminal-state entries are
// inert under the re-stamp pass. Important because restartExpire runs
// FIRST and may have flipped already-stale pendings to Expired; the
// re-stamp must skip those (otherwise the seller would carry a
// warrant for an offer that's already terminal).
func TestRestartReStamp_SkipsNonPending(t *testing.T) {
	w := buildRestampWorld(t, []rstActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared},
	})
	now := time.Now().UTC()
	w.PayLedger[1] = &sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		State: sim.PayLedgerStateExpired, ResolvedAt: now,
	}
	w.PayLedger[2] = &sim.PayLedgerEntry{
		ID: 2, BuyerID: "alice", SellerID: "bob",
		State: sim.PayLedgerStateAccepted, ResolvedAt: now,
	}
	w.PayLedger[3] = &sim.PayLedgerEntry{
		ID: 3, BuyerID: "alice", SellerID: "bob",
		State: sim.PayLedgerStateDeclined, ResolvedAt: now,
	}
	sim.RestartReStampPayOfferWarrants(w, now)
	if got := len(w.Actors["bob"].Warrants); got != 0 {
		t.Errorf("terminal entries produced %d warrants, want 0", got)
	}
}

// TestRestartReStamp_SkipsMissingSeller — defensive: a pending entry
// whose seller no longer exists in the world (repo drift or partial
// load) does NOT panic — the re-stamp silently skips.
func TestRestartReStamp_SkipsMissingSeller(t *testing.T) {
	w := buildRestampWorld(t, []rstActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared},
	})
	now := time.Now().UTC()
	w.PayLedger[1] = &sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "ghost",
		State: sim.PayLedgerStatePending, ExpiresAt: now.Add(time.Minute),
	}
	// Should not panic.
	sim.RestartReStampPayOfferWarrants(w, now)
}

// TestRestartReStamp_NilWorldNoPanic — defensive nil guard, same shape
// as restartExpirePendingEntries.
func TestRestartReStamp_NilWorldNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil-world re-stamp panicked: %v", r)
		}
	}()
	sim.RestartReStampPayOfferWarrants(nil, time.Now())
}

// TestPayResolvedWarrantReason_KindAndDiscriminator — type-level pins
// for the new warrant kind: Kind reports WarrantKindPayResolved
// regardless of payload; DedupDiscriminator returns
// uint64(ResolvedEventID) so the (Kind, ResolvedEventID) key dedupes
// against itself when a subscriber gets re-invoked accidentally.
func TestPayResolvedWarrantReason_KindAndDiscriminator(t *testing.T) {
	r := sim.PayResolvedWarrantReason{ResolvedEventID: 42, LedgerID: 17}
	if r.Kind() != sim.WarrantKindPayResolved {
		t.Errorf("Kind() = %q, want %q", r.Kind(), sim.WarrantKindPayResolved)
	}
	if r.DedupDiscriminator() != 42 {
		t.Errorf("DedupDiscriminator() = %d, want 42 (ResolvedEventID)", r.DedupDiscriminator())
	}
}
