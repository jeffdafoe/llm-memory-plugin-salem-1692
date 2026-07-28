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

// An out-of-order append must not defeat pruning or the cap. `at` is the
// caller's clock, not RecordContact's — a replayed command or a delayed tick can
// land an older timestamp after a newer one — and both prune steps select by
// POSITION, so an unsorted trail would strand aged entries and evict the newest.
// (code_review, LLM-547.)
func TestRecordContactToleratesOutOfOrderWrites(t *testing.T) {
	w := &World{}
	base := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)

	// A fresh contact, THEN one from a day earlier. Position-based pruning on an
	// unsorted trail would keep the day-old entry (the scan stops at the first
	// non-old element, which is now at index 0).
	w.RecordContact("gideon", "prudence", base)
	w.RecordContact("gideon", "prudence", base.Add(-24*time.Hour))

	rec := w.contactRecord("gideon", "prudence")
	if len(rec.At) != 1 {
		t.Fatalf("trail = %v, want only the in-horizon entry — an out-of-order append must still prune", rec.At)
	}
	if !rec.At[0].Equal(base) {
		t.Errorf("surviving entry = %v, want %v", rec.At[0], base)
	}

	// The cap must also evict the OLDEST, not whatever sits at the front of an
	// unsorted slice. Write newest-first so an unsorted cap would keep the wrong end.
	w2 := &World{}
	for i := 0; i < MaxContactsPerPair*2; i++ {
		w2.RecordContact("a", "b", base.Add(-time.Duration(i)*time.Minute))
	}
	got := w2.contactRecord("a", "b").At
	if len(got) != MaxContactsPerPair {
		t.Fatalf("trail length = %d, want %d", len(got), MaxContactsPerPair)
	}
	if !got[len(got)-1].Equal(base) {
		t.Errorf("newest kept = %v, want %v — the cap must keep the most recent contacts", got[len(got)-1], base)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Before(got[i-1]) {
			t.Fatalf("trail is not chronologically ordered at index %d: %v", i, got)
		}
	}
}

// A persisted timestamp meaningfully in the FUTURE is a bad value, not a
// contact: only a clock correction between boots or an out-of-band edit produces
// one, and it would otherwise hold the pair in the brake tier until
// `future + horizon`, telling an actor it had already spoken with someone it had
// not. The trail is prompt-facing, so the loader validates rather than trusting
// the database. (code_review, LLM-547.)
func TestRehydrateContactLedgerDropsFutureStamps(t *testing.T) {
	now := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)
	pairs := []ContactPair{
		{SubjectID: "gideon", PeerID: "prudence", At: []time.Time{
			now.Add(-30 * time.Minute), // good
			now.Add(48 * time.Hour),    // impossible
		}},
		{SubjectID: "gideon", PeerID: "josiah", At: []time.Time{
			now.Add(24 * time.Hour), // nothing else to fall back on
		}},
	}
	got := RehydrateContactLedger(pairs, now, DefaultContactRecallHorizon)

	rec := got["gideon"]["prudence"]
	if rec == nil || len(rec.At) != 1 || !rec.At[0].Equal(now.Add(-30*time.Minute)) {
		t.Errorf("trail = %+v, want only the real contact — a future stamp must be dropped", rec)
	}
	if _, ok := got["gideon"]["josiah"]; ok {
		t.Error("a pair whose only stamp is in the future must be dropped entirely")
	}

	// Ordinary skew across a restart is absorbed rather than discarded — a
	// contact recorded moments before shutdown is real.
	within := RehydrateContactLedger([]ContactPair{
		{SubjectID: "a", PeerID: "b", At: []time.Time{now.Add(ContactFutureSkewTolerance / 2)}},
	}, now, DefaultContactRecallHorizon)
	if _, ok := within["a"]["b"]; !ok {
		t.Error("a stamp inside the skew tolerance must survive rehydrate")
	}
}

// The deliberate trust boundary, made explicit rather than left in comments: an
// in-process future timestamp is ACCEPTED on write (RecordContact validates
// nothing — the caller owns clock validity), and the persistence layer is what
// eventually removes it. (code_review, LLM-547 round 3.)
//
// This pins both halves. If someone later adds a write-path guard, the first
// assertion fails and they are made to think about whether they have a clock to
// check against — the answer today being no, since Environment.Now is never
// advanced.
func TestRecordContactAcceptsFutureStampsAndPersistenceReclaimsThem(t *testing.T) {
	w := &World{}
	now := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)

	// Accepted in-process, no error, no clamp.
	w.RecordContact("gideon", "ghost", future)
	if rec := w.contactRecord("gideon", "ghost"); rec == nil || len(rec.At) != 1 || !rec.At[0].Equal(future) {
		t.Fatalf("in-process future stamp = %+v, want it accepted verbatim", rec)
	}

	// The checkpoint's window is what judges it: outside [staleBefore,
	// validUntil] at both ends, so the row is reclaimed rather than living
	// forever behind a max-based predicate.
	cp := &CheckpointSnapshot{
		ContactPairs:         FlattenContactLedger(w.ContactLedger),
		ContactRecallHorizon: DefaultContactRecallHorizon,
	}
	cp.StampContactWindow(now)
	if cp.ContactStaleBefore.IsZero() || cp.ContactValidUntil.IsZero() {
		t.Fatal("StampContactWindow left the window unset")
	}
	if !future.After(cp.ContactValidUntil) {
		t.Errorf("future stamp %v is inside the valid window (ends %v) — the test proves nothing",
			future, cp.ContactValidUntil)
	}

	// And the loader refuses it on the way back in, so the two agree.
	restored := RehydrateContactLedger(cp.ContactPairs, now, DefaultContactRecallHorizon)
	if _, ok := restored["gideon"]["ghost"]; ok {
		t.Error("a future-only pair must not rehydrate")
	}
}

// StampContactWindow must not invent a horizon. A snapshot carrying none has not
// asked for reclamation, and defaulting would delete rows on a rule nobody chose.
func TestStampContactWindowRequiresAHorizon(t *testing.T) {
	cp := &CheckpointSnapshot{}
	cp.StampContactWindow(time.Now())
	if !cp.ContactStaleBefore.IsZero() || !cp.ContactValidUntil.IsZero() {
		t.Errorf("window = [%v, %v], want unset when the snapshot carries no horizon",
			cp.ContactStaleBefore, cp.ContactValidUntil)
	}

	// A zero clock is equally "I don't know", and must not stamp either.
	cp2 := &CheckpointSnapshot{ContactRecallHorizon: DefaultContactRecallHorizon}
	cp2.StampContactWindow(time.Time{})
	if !cp2.ContactStaleBefore.IsZero() || !cp2.ContactValidUntil.IsZero() {
		t.Errorf("window = [%v, %v], want unset when the clock is zero",
			cp2.ContactStaleBefore, cp2.ContactValidUntil)
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
