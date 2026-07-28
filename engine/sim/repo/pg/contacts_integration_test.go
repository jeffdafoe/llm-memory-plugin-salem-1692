package pg

// Real-pg integration tests for the per-pair conversational recency trail
// (LLM-547). Run against embedded Postgres with the full prod-baseline schema +
// post-baseline migrations applied; skipped under `go test -short`.
//
// These prove what a spy repo cannot: that the trail survives a genuine
// SaveWorld → LoadWorld through the real timestamptz[] column, and that the
// recall horizon is applied on the way back in. That second property is the
// whole reason the table exists — LLM-546 was an innkeeper greeting a player as
// a stranger six hours after serving him, across three deploys — so a round trip
// that silently dropped or silently kept everything would look identical to a
// unit test working off an in-memory fake.

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// contactWorld sets up a checkpointable world holding one contact trail per
// named pair. Times are truncated to microseconds to match Postgres timestamptz
// precision on round-trip.
func contactWorld(repo sim.Repository, trails map[sim.ActorID]map[sim.ActorID][]time.Time) *sim.World {
	w := checkpointableWorld(repo)
	w.ContactLedger = make(map[sim.ActorID]map[sim.ActorID]*sim.ContactRecord)
	for subjectID, byPeer := range trails {
		w.ContactLedger[subjectID] = make(map[sim.ActorID]*sim.ContactRecord)
		for peerID, at := range byPeer {
			trunc := make([]time.Time, len(at))
			for i, t := range at {
				trunc[i] = t.UTC().Truncate(time.Microsecond)
			}
			w.ContactLedger[subjectID][peerID] = &sim.ContactRecord{At: trunc}
		}
	}
	return w
}

// TestIntegration_Contact_RoundTrip — DoD 7. A pair written before a checkpoint
// is present after a reload AND still tiers correctly, which is the assertion
// that matters: a trail that round-trips as bytes but lands outside its window
// would render nothing and look, from the village, exactly like the bug.
func TestIntegration_Contact_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	// Two calls inside the 2h brake window — the weighted tier, the one that says
	// there is nothing left to draw out of her.
	w := contactWorld(repo, map[sim.ActorID]map[sim.ActorID][]time.Time{
		"gideon": {"prudence": {now.Add(-75 * time.Minute), now.Add(-20 * time.Minute)}},
	})

	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	pairs, err := repo.Contacts.LoadAll(ctx)
	if err != nil {
		t.Fatalf("Contacts.LoadAll: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("loaded %d pair(s), want 1: %+v", len(pairs), pairs)
	}
	if pairs[0].SubjectID != "gideon" || pairs[0].PeerID != "prudence" {
		t.Errorf("pair identity = %s→%s, want gideon→prudence", pairs[0].SubjectID, pairs[0].PeerID)
	}
	if len(pairs[0].At) != 2 {
		t.Fatalf("trail length = %d, want 2 — the timestamptz[] column must carry every entry", len(pairs[0].At))
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	tier, count := loaded.ContactTierFor("gideon", "prudence", now)
	if tier != sim.ContactTierBrakeWeighted {
		t.Errorf("tier after reload = %v, want ContactTierBrakeWeighted — a trail that survives as "+
			"bytes but lands outside its window is indistinguishable from the bug", tier)
	}
	if count != 2 {
		t.Errorf("recent count after reload = %d, want 2", count)
	}
}

// TestIntegration_Contact_HorizonDropsStaleAtLoad — DoD 8. A trail older than the
// recall horizon is dropped when rehydrating rather than carried back into
// memory.
//
// Pruning at LOAD rather than by a sweep is the design: the rows are tiny and
// bounded by actor pairs, so there is nothing worth reclaiming between boots and
// a background sweep would be machinery for its own sake. The consequence — the
// stale ROW survives in Postgres until something overwrites it — is asserted
// here too, so the posture is pinned rather than assumed.
func TestIntegration_Contact_HorizonDropsStaleAtLoad(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	w := contactWorld(repo, map[sim.ActorID]map[sim.ActorID][]time.Time{
		"gideon": {
			"prudence": {now.Add(-40 * time.Minute)}, // inside the 8h horizon
			"josiah":   {now.Add(-20 * time.Hour)},   // long past it
		},
	})

	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	// Both rows are written — the checkpoint does not prune; memory already did,
	// and this fixture seeded the stale trail directly.
	pairs, err := repo.Contacts.LoadAll(ctx)
	if err != nil {
		t.Fatalf("Contacts.LoadAll: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("stored %d pair(s), want 2 — SaveSnapshot writes what memory holds without judging age", len(pairs))
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if tier, _ := loaded.ContactTierFor("gideon", "prudence", now); tier != sim.ContactTierBrakeQuiet {
		t.Errorf("recent pair after reload = %v, want ContactTierBrakeQuiet", tier)
	}
	if _, ok := loaded.ContactLedger["gideon"]["josiah"]; ok {
		t.Error("a pair whose only contact is past the recall horizon must be dropped at load, not rehydrated")
	}
}

// TestIntegration_Contact_UpsertReplacesTrail — the trail is REPLACED on
// checkpoint, never appended to.
//
// Memory is authoritative: it has already pruned to the horizon and enforced the
// per-pair cap, so an append would let the column grow past that cap and would
// resurrect entries memory had deliberately dropped. Worth its own test because
// an append-shaped upsert (array_cat) is the natural thing to reach for and
// would pass a round-trip test while quietly breaking pruning forever.
func TestIntegration_Contact_UpsertReplacesTrail(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	first := contactWorld(repo, map[sim.ActorID]map[sim.ActorID][]time.Time{
		"gideon": {"prudence": {now.Add(-90 * time.Minute), now.Add(-60 * time.Minute)}},
	})
	if err := SaveWorld(ctx, repo, first.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (first): %v", err)
	}

	// A later checkpoint where memory holds a SHORTER trail — the state after a
	// prune dropped the older entry.
	second := contactWorld(repo, map[sim.ActorID]map[sim.ActorID][]time.Time{
		"gideon": {"prudence": {now.Add(-10 * time.Minute)}},
	})
	if err := SaveWorld(ctx, repo, second.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (second): %v", err)
	}

	pairs, err := repo.Contacts.LoadAll(ctx)
	if err != nil {
		t.Fatalf("Contacts.LoadAll: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("loaded %d pair(s), want 1", len(pairs))
	}
	if got := len(pairs[0].At); got != 1 {
		t.Fatalf("trail length after re-checkpoint = %d, want 1 — the upsert must REPLACE the trail, "+
			"not append to it, or memory's pruning is silently undone on every checkpoint", got)
	}
	if !pairs[0].At[0].Equal(now.Add(-10 * time.Minute)) {
		t.Errorf("surviving entry = %v, want the one memory held (%v)", pairs[0].At[0], now.Add(-10*time.Minute))
	}
}

// TestIntegration_Contact_SurvivesPeerDeparture — the orphan-ref posture, and the
// reason there is deliberately no foreign key on either id.
//
// A transient visitor's actor row is DELETED at cleanup, and this ledger covers
// visitors on purpose (a traveller working a circuit of shops is exactly the
// actor a route-scoped design would miss). An FK would either block that delete
// or cascade the trail away; instead the row is left to age out. It is harmless
// because every read is keyed by a co-present peer, and a departed actor is
// never co-present.
func TestIntegration_Contact_SurvivesPeerDeparture(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	w := contactWorld(repo, map[sim.ActorID]map[sim.ActorID][]time.Time{
		"gideon": {"vstr-0000abcd": {now.Add(-30 * time.Minute)}},
	})
	// The traveller is not in w.Actors at all — he has already departed and been
	// swept. The contact with him must still persist and reload.
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if _, ok := loaded.ContactLedger["gideon"]["vstr-0000abcd"]; !ok {
		t.Error("a contact naming a departed actor must round-trip — there is no FK precisely so it can")
	}
}
