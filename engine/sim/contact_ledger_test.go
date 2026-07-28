package sim

import (
	"testing"
	"time"
)

// The tier boundaries are the whole feature: every rendered line is chosen by
// them, and they are the part a later tuning change is most likely to move by
// accident. Each case states the situation in the units the design is written in
// (ages before "now") rather than absolute timestamps.
func TestContactTierBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)
	const (
		brake   = 2 * time.Hour
		horizon = 8 * time.Hour
	)
	cases := []struct {
		name      string
		ages      []time.Duration
		wantTier  ContactTier
		wantCount int
	}{
		{
			name:     "no history at all is silent",
			ages:     nil,
			wantTier: ContactTierNone,
		},
		{
			name:     "a contact past the recall horizon is forgotten",
			ages:     []time.Duration{10 * time.Hour},
			wantTier: ContactTierNone,
		},
		{
			name:     "inside the horizon but past the brake reads as continuity",
			ages:     []time.Duration{5 * time.Hour},
			wantTier: ContactTierContinuity,
		},
		{
			name:      "one contact inside the brake window brakes quietly",
			ages:      []time.Duration{40 * time.Minute},
			wantTier:  ContactTierBrakeQuiet,
			wantCount: 1,
		},
		{
			name:      "two inside the brake window carry weight",
			ages:      []time.Duration{75 * time.Minute, 20 * time.Minute},
			wantTier:  ContactTierBrakeWeighted,
			wantCount: 2,
		},
		{
			// The live shape: Ward at 20:44, 21:09 and 21:19. By the third approach
			// the scene should already have been arguing against it.
			name:      "three inside the brake window still weigh, and the count follows",
			ages:      []time.Duration{35 * time.Minute, 10 * time.Minute, time.Minute},
			wantTier:  ContactTierBrakeWeighted,
			wantCount: 3,
		},
		{
			// An old contact must not dilute a recent one into the quieter tier —
			// the brake counts only what is inside its own window.
			name:      "an aged contact does not soften a recent pair",
			ages:      []time.Duration{7 * time.Hour, 90 * time.Minute, 30 * time.Minute},
			wantTier:  ContactTierBrakeWeighted,
			wantCount: 2,
		},
		{
			// Nor may aged contacts ADD UP into a brake: three old calls are still
			// only history, however many there were.
			name:     "several aged contacts remain mere continuity",
			ages:     []time.Duration{7 * time.Hour, 6 * time.Hour, 5 * time.Hour},
			wantTier: ContactTierContinuity,
		},
		{
			// Exactly at the brake edge counts as inside it. The boundary has to
			// fall somewhere; inclusive means a contact never slips tiers between
			// two ticks a millisecond apart.
			name:      "a contact exactly at the brake edge is still recent",
			ages:      []time.Duration{brake},
			wantTier:  ContactTierBrakeQuiet,
			wantCount: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &ContactRecord{}
			for _, age := range tc.ages {
				rec.At = append(rec.At, now.Add(-age))
			}
			gotTier, gotCount := rec.tier(now, brake, horizon)
			if gotTier != tc.wantTier {
				t.Errorf("tier = %v, want %v", gotTier, tc.wantTier)
			}
			if gotCount != tc.wantCount {
				t.Errorf("recent count = %d, want %d", gotCount, tc.wantCount)
			}
		})
	}
}

// A nil record must read as "no history" rather than panicking: perception looks
// up pairs that have never spoken on every tick, so this is the common path, not
// an edge case.
func TestContactTierNilRecordIsSilent(t *testing.T) {
	var rec *ContactRecord
	if tier, count := rec.tier(time.Now(), time.Hour, 8*time.Hour); tier != ContactTierNone || count != 0 {
		t.Fatalf("nil record: got (%v, %d), want (ContactTierNone, 0)", tier, count)
	}
}

// RecordContact is on the speech commit path, so its guards have to hold without
// erroring — the utterance has already emitted by the time it runs.
func TestRecordContactWritesBothWaysAndGuards(t *testing.T) {
	w := &World{}
	now := time.Now()

	w.RecordContact("gideon", "prudence", now)
	if tier, _ := w.ContactTierFor("gideon", "prudence", now); tier != ContactTierBrakeQuiet {
		t.Errorf("subject→peer: tier = %v, want ContactTierBrakeQuiet", tier)
	}
	// The reverse direction is a separate row and is NOT written implicitly — the
	// Speak path writes it explicitly. Pinned so a future "optimization" that
	// makes RecordContact symmetric doesn't silently double-count every contact.
	if tier, _ := w.ContactTierFor("prudence", "gideon", now); tier != ContactTierNone {
		t.Errorf("peer→subject: tier = %v, want ContactTierNone (the caller writes this direction itself)", tier)
	}

	// Guards: a self-pair and empty ids are dropped silently.
	w.RecordContact("gideon", "gideon", now)
	if tier, _ := w.ContactTierFor("gideon", "gideon", now); tier != ContactTierNone {
		t.Error("a self-pair must never be recorded")
	}
	w.RecordContact("", "prudence", now)
	w.RecordContact("gideon", "", now)
	if _, ok := w.ContactLedger[""]; ok {
		t.Error("an empty subject id must never be recorded")
	}
	if _, ok := w.ContactLedger["gideon"][""]; ok {
		t.Error("an empty peer id must never be recorded")
	}
}

// The trail prunes on write, so a long-running pair cannot grow without bound
// and entries that have aged out cannot linger to be counted later.
func TestRecordContactPrunesOnWrite(t *testing.T) {
	w := &World{}
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	// A contact a full day back, then one now. The old one is past the 8h default
	// horizon and must be gone from the stored trail, not merely ignored at read.
	w.RecordContact("gideon", "prudence", base.Add(-24*time.Hour))
	w.RecordContact("gideon", "prudence", base)
	rec := w.contactRecord("gideon", "prudence")
	if got := len(rec.At); got != 1 {
		t.Fatalf("trail length = %d, want 1 (the day-old entry should be pruned on write)", got)
	}
	if !rec.At[0].Equal(base) {
		t.Errorf("surviving entry = %v, want %v", rec.At[0], base)
	}

	// The per-pair cap holds even for a burst well inside the horizon.
	for i := 0; i < MaxContactsPerPair*3; i++ {
		w.RecordContact("gideon", "prudence", base.Add(time.Duration(i)*time.Minute))
	}
	if got := len(w.contactRecord("gideon", "prudence").At); got > MaxContactsPerPair {
		t.Errorf("trail length = %d, want <= %d", got, MaxContactsPerPair)
	}
}

// The clone must not alias: the checkpoint write and the published snapshot both
// read off the world goroutine, so a shared backing array would let a later
// Speak mutate what a reader is walking.
func TestCloneContactLedgerDoesNotAlias(t *testing.T) {
	w := &World{}
	now := time.Now()
	w.RecordContact("gideon", "prudence", now)

	clone := CloneContactLedger(w.ContactLedger)
	w.RecordContact("gideon", "prudence", now.Add(time.Minute))

	if got := len(clone["gideon"]["prudence"].At); got != 1 {
		t.Errorf("clone trail length = %d, want 1 — the clone must not see writes made after it", got)
	}
}

// Flatten → Rehydrate is the durability round trip, and the horizon is applied
// on the way back in. This is the LLM-546 requirement in miniature: a trail
// written before a restart has to still mean something after one.
func TestContactLedgerRoundTripAppliesHorizon(t *testing.T) {
	w := &World{}
	now := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)

	// Two pairs: one spoken with recently, one only long ago.
	w.RecordContact("gideon", "prudence", now.Add(-40*time.Minute))
	w.ContactLedger["gideon"]["josiah"] = &ContactRecord{At: []time.Time{now.Add(-20 * time.Hour)}}

	pairs := FlattenContactLedger(w.ContactLedger)
	if len(pairs) != 2 {
		t.Fatalf("flattened %d pair(s), want 2", len(pairs))
	}
	// Deterministic order, so a checkpoint write is stable run to run.
	if pairs[0].PeerID != "josiah" || pairs[1].PeerID != "prudence" {
		t.Errorf("flatten order = [%s %s], want [josiah prudence] (sorted by subject then peer)",
			pairs[0].PeerID, pairs[1].PeerID)
	}

	restored := RehydrateContactLedger(pairs, now, DefaultContactRecallHorizon)
	if tier, _ := (&World{ContactLedger: restored}).ContactTierFor("gideon", "prudence", now); tier != ContactTierBrakeQuiet {
		t.Errorf("after reload, the recent pair reads %v, want ContactTierBrakeQuiet", tier)
	}
	if _, ok := restored["gideon"]["josiah"]; ok {
		t.Error("a pair whose only contact is past the recall horizon must be dropped at load, not rehydrated")
	}
}

// A stored row naming a departed actor is tolerated rather than rejected: a
// visitor's ActorID is deleted at cleanup, and the row is harmless because every
// read is keyed by a co-present peer. Pinned so a later "cleanup" pass doesn't
// add validation that would also drop a returner legitimately re-minted under a
// known id.
func TestRehydrateContactLedgerKeepsOrphanRefsAndDropsJunk(t *testing.T) {
	now := time.Now()
	pairs := []ContactPair{
		{SubjectID: "gideon", PeerID: "vstr-departed", At: []time.Time{now.Add(-time.Hour)}},
		{SubjectID: "", PeerID: "prudence", At: []time.Time{now}},
		{SubjectID: "gideon", PeerID: "", At: []time.Time{now}},
		{SubjectID: "gideon", PeerID: "gideon", At: []time.Time{now}},
		{SubjectID: "gideon", PeerID: "empty-trail"},
	}
	got := RehydrateContactLedger(pairs, now, DefaultContactRecallHorizon)

	if _, ok := got["gideon"]["vstr-departed"]; !ok {
		t.Error("a contact with a departed actor must survive rehydrate — it is harmless and ages out on its own")
	}
	if _, ok := got[""]; ok {
		t.Error("an empty subject id must be dropped")
	}
	for _, bad := range []ActorID{"", "gideon", "empty-trail"} {
		if _, ok := got["gideon"][bad]; ok {
			t.Errorf("pair gideon→%q must be dropped at rehydrate", bad)
		}
	}
}

// The trail is stored oldest-first and the tier walk assumes nothing about
// order, but rehydrate sorts anyway so the cap keeps the MOST RECENT entries
// rather than whatever order the database happened to return.
func TestRehydrateContactLedgerKeepsMostRecentUnderCap(t *testing.T) {
	now := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)
	var at []time.Time
	// Deliberately out of order, newest first, to prove the sort runs.
	for i := 0; i < MaxContactsPerPair*2; i++ {
		at = append(at, now.Add(-time.Duration(i)*time.Minute))
	}
	got := RehydrateContactLedger([]ContactPair{{SubjectID: "a", PeerID: "b", At: at}}, now, DefaultContactRecallHorizon)

	rec := got["a"]["b"]
	if len(rec.At) != MaxContactsPerPair {
		t.Fatalf("trail length = %d, want %d", len(rec.At), MaxContactsPerPair)
	}
	if !rec.At[len(rec.At)-1].Equal(now) {
		t.Errorf("newest kept entry = %v, want %v — the cap must keep the most recent contacts", rec.At[len(rec.At)-1], now)
	}
	for i := 1; i < len(rec.At); i++ {
		if rec.At[i].Before(rec.At[i-1]) {
			t.Fatalf("trail is not oldest-first at index %d", i)
		}
	}
}

// The tunables fall back to their defaults when unset, so a world built without
// the environment loader — every unit test, and any partially-seeded fixture —
// still tiers sensibly rather than treating every window as zero.
func TestContactWindowsFallBackToDefaults(t *testing.T) {
	w := &World{}
	if got := w.ContactBrakeWindow(); got != DefaultContactBrakeWindow {
		t.Errorf("brake window = %v, want %v", got, DefaultContactBrakeWindow)
	}
	if got := w.ContactRecallHorizon(); got != DefaultContactRecallHorizon {
		t.Errorf("recall horizon = %v, want %v", got, DefaultContactRecallHorizon)
	}
	w.Settings.ContactBrakeWindow = 15 * time.Minute
	w.Settings.ContactRecallHorizon = time.Hour
	if got := w.ContactBrakeWindow(); got != 15*time.Minute {
		t.Errorf("configured brake window = %v, want 15m", got)
	}
	if got := w.ContactRecallHorizon(); got != time.Hour {
		t.Errorf("configured recall horizon = %v, want 1h", got)
	}
}
