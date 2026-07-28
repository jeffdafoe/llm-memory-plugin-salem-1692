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

func (r *ContactsRepo) SaveSnapshot(_ context.Context, _ sim.Tx, pairs []sim.ContactPair) error {
	kept := make([]sim.ContactPair, 0, len(pairs))
	for _, p := range pairs {
		if p.SubjectID == "" || p.PeerID == "" || p.SubjectID == p.PeerID || len(p.At) == 0 {
			continue
		}
		kept = append(kept, p)
	}
	r.pairs = cloneContactPairs(kept)
	return nil
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
