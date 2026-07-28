package mem

import (
	"context"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ContactsRepo is an in-memory implementation of sim.ContactsRepo (LLM-547).
// Seed populates initial state; LoadAll deep-clones; SaveSnapshot re-states the
// trail from whatever the checkpoint hands it.
//
// SaveSnapshot REPLACES rather than merges, mirroring the pg tier's
// whole-trail upsert: the in-memory ledger is authoritative and has already
// pruned to the recall horizon, so a merge here would resurrect entries the
// engine had dropped and let the fake diverge from production behaviour.
type ContactsRepo struct {
	pairs []sim.ContactPair
}

func NewContactsRepo() *ContactsRepo {
	return &ContactsRepo{}
}

func (r *ContactsRepo) Seed(pairs []sim.ContactPair) {
	r.pairs = cloneContactPairs(pairs)
}

func (r *ContactsRepo) LoadAll(_ context.Context) ([]sim.ContactPair, error) {
	return cloneContactPairs(r.pairs), nil
}

// SaveSnapshot mirrors the pg tier: upsert what memory holds, then drop pairs
// whose whole trail is older than staleBefore. Because this fake REPLACES its
// contents wholesale, the delete only shows up for a pair that memory still
// holds but whose trail has aged — which is exactly the case worth keeping
// faithful, since it is the one a merge-shaped implementation would get wrong.
func (r *ContactsRepo) SaveSnapshot(_ context.Context, _ sim.Tx, pairs []sim.ContactPair, staleBefore time.Time) error {
	kept := make([]sim.ContactPair, 0, len(pairs))
	for _, p := range pairs {
		if p.SubjectID == "" || p.PeerID == "" || p.SubjectID == p.PeerID || len(p.At) == 0 {
			continue
		}
		if !staleBefore.IsZero() && !newestContact(p.At).Before(staleBefore) {
			kept = append(kept, p)
			continue
		}
		if staleBefore.IsZero() {
			kept = append(kept, p)
		}
	}
	r.pairs = cloneContactPairs(kept)
	return nil
}

// newestContact returns the latest timestamp in a trail without assuming order,
// matching the pg tier's max(unnest(...)).
func newestContact(at []time.Time) time.Time {
	var newest time.Time
	for _, t := range at {
		if t.After(newest) {
			newest = t
		}
	}
	return newest
}

// cloneContactPairs deep-copies the trail slices so Seed / LoadAll /
// SaveSnapshot don't alias the caller's slices across the fake-repo boundary.
func cloneContactPairs(src []sim.ContactPair) []sim.ContactPair {
	if src == nil {
		return nil
	}
	out := make([]sim.ContactPair, 0, len(src))
	for _, p := range src {
		at := make([]time.Time, len(p.At))
		copy(at, p.At)
		out = append(out, sim.ContactPair{SubjectID: p.SubjectID, PeerID: p.PeerID, At: at})
	}
	return out
}
