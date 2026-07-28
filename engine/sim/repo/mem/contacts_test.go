package mem

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// The reclamation window predicate exists in two places — this fake and the pg
// tier's NOT EXISTS — and they must not drift. The pg version was WRONG for two
// rounds precisely because the shape looked obviously equivalent to "is the
// newest entry old", so the cases below are chosen to separate those two
// readings rather than to cover the happy path.
//
// The pg side has the same matrix under real Postgres in
// repo/pg/contacts_integration_test.go; this one runs in milliseconds and is
// where a drift shows up first.
func TestContactsSaveSnapshotWindowPredicate(t *testing.T) {
	now := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)
	staleBefore := now.Add(-8 * time.Hour)
	validUntil := now.Add(5 * time.Minute)

	cases := []struct {
		name string
		at   []time.Time
		keep bool
	}{
		{
			name: "a recent contact is kept",
			at:   []time.Time{now.Add(-10 * time.Minute)},
			keep: true,
		},
		{
			name: "an old-only trail is dropped",
			at:   []time.Time{now.Add(-20 * time.Hour)},
			keep: false,
		},
		{
			// The case a max-based predicate can never reach: the newest entry is
			// ahead of every cutoff, forever.
			name: "a future-only trail is dropped",
			at:   []time.Time{now.Add(72 * time.Hour)},
			keep: false,
		},
		{
			// Also unreachable by max: max is in the future, so "is it old" says
			// no, yet nothing in the trail is a real contact.
			name: "old plus future, with nothing valid, is dropped",
			at:   []time.Time{now.Add(-20 * time.Hour), now.Add(72 * time.Hour)},
			keep: false,
		},
		{
			// The mirror case: one bad value must not condemn a live pair.
			name: "one valid contact beside an invalid one is kept",
			at:   []time.Time{now.Add(72 * time.Hour), now.Add(-10 * time.Minute)},
			keep: true,
		},
		{
			name: "an empty trail is dropped",
			at:   nil,
			keep: false,
		},
		{
			name: "a contact exactly at the stale boundary is kept",
			at:   []time.Time{staleBefore},
			keep: true,
		},
		{
			// Inclusive at BOTH ends, matching the loader's
			// `t.Before(cutoff) || t.After(future)` rejection. If either side
			// changes its inclusivity, the sweep and the loader disagree about a
			// contact sitting exactly on the boundary.
			name: "a contact exactly at the future boundary is kept",
			at:   []time.Time{validUntil},
			keep: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewContactsRepo()
			if err := r.SaveSnapshot(context.Background(), nil,
				[]sim.ContactPair{{SubjectID: "a", PeerID: "b", At: tc.at}},
				staleBefore, validUntil); err != nil {
				t.Fatalf("SaveSnapshot: %v", err)
			}
			got, err := r.LoadAll(context.Background())
			if err != nil {
				t.Fatalf("LoadAll: %v", err)
			}
			if kept := len(got) == 1; kept != tc.keep {
				t.Errorf("row kept = %v, want %v (trail %v)", kept, tc.keep, tc.at)
			}
		})
	}
}

// An unset window must reclaim NOTHING. A caller that supplied no horizon — or a
// snapshot built outside CheckpointNow — has not asked for deletion, and
// substituting a default would delete rows on a rule nobody chose.
func TestContactsSaveSnapshotZeroWindowDeletesNothing(t *testing.T) {
	now := time.Now().UTC()
	ancient := []sim.ContactPair{{SubjectID: "a", PeerID: "b", At: []time.Time{now.Add(-1000 * time.Hour)}}}

	for _, tc := range []struct {
		name                    string
		staleBefore, validUntil time.Time
	}{
		{"both bounds unset", time.Time{}, time.Time{}},
		{"only the lower bound set", now.Add(-8 * time.Hour), time.Time{}},
		{"only the upper bound set", time.Time{}, now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewContactsRepo()
			if err := r.SaveSnapshot(context.Background(), nil, ancient, tc.staleBefore, tc.validUntil); err != nil {
				t.Fatalf("SaveSnapshot: %v", err)
			}
			got, err := r.LoadAll(context.Background())
			if err != nil {
				t.Fatalf("LoadAll: %v", err)
			}
			if len(got) != 1 {
				t.Errorf("rows = %d, want 1 — an incomplete window must delete nothing", len(got))
			}
		})
	}
}
