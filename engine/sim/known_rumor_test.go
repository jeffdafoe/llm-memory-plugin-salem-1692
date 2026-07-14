package sim

import (
	"testing"
	"time"
)

func knownRumorTestNow() time.Time {
	return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
}

func TestKnownRumorClauseLadder(t *testing.T) {
	now := knownRumorTestNow()
	r := newShortOnCoinRumor("b1", "Ezekiel Crane", now)
	if got := r.Clause(); got != "Ezekiel Crane came up short of coin for a purchase" {
		t.Fatalf("rung 0 clause = %q", got)
	}
	r.Rung = MaxRumorRung
	if got := r.Clause(); got != "Ezekiel Crane is ruined — in debt to half the village" {
		t.Fatalf("top-rung clause = %q", got)
	}
}

func TestKnownRumorEscalatedClamps(t *testing.T) {
	now := knownRumorTestNow()
	r := newShortOnCoinRumor("b1", "Ezekiel Crane", now)
	if !r.FirstHand {
		t.Fatal("seed should be first-hand")
	}
	for i := 0; i < MaxRumorRung+3; i++ {
		r = r.escalated(now)
	}
	if r.Rung != MaxRumorRung {
		t.Fatalf("escalated rung = %d, want clamp at %d", r.Rung, MaxRumorRung)
	}
	if r.FirstHand {
		t.Fatal("escalated rumor should be hearsay, not first-hand")
	}
}

func TestLearnRumorDedupTakesHigherRung(t *testing.T) {
	now := knownRumorTestNow()
	a := &Actor{ID: "a1"}
	// Two hearsay rumors about the same subject; the higher rung must win (the
	// telephone-game escalation among non-witnesses).
	lowHearsay := newShortOnCoinRumor("b1", "Ezekiel Crane", now).escalated(now) // rung 1, hearsay
	if !a.learnRumor(lowHearsay, now) {
		t.Fatal("first learn should report a change")
	}
	highHearsay := lowHearsay.escalated(now) // rung 2, hearsay
	if !a.learnRumor(highHearsay, now) {
		t.Fatal("higher rung should report a change")
	}
	if len(a.Rumors) != 1 {
		t.Fatalf("dedup failed: %d rumors, want 1", len(a.Rumors))
	}
	if a.Rumors[0].Rung != 2 {
		t.Fatalf("kept rung = %d, want 2", a.Rumors[0].Rung)
	}
	if a.learnRumor(lowHearsay, now) { // re-hearing a lower rung: no material change
		t.Fatal("lower-rung re-hearing should not report a change")
	}
	if a.Rumors[0].Rung != 2 {
		t.Fatalf("rung regressed to %d", a.Rumors[0].Rung)
	}
}

// TestLearnRumorFirstHandIsAuthoritative pins the provenance rule: witnessing
// first-hand supersedes held hearsay (adopting the true rung-0 form), and inbound
// hearsay never overwrites a first-hand truth.
func TestLearnRumorFirstHandIsAuthoritative(t *testing.T) {
	now := knownRumorTestNow()

	// Held hearsay, then witnessed first-hand → promotes to first-hand rung 0.
	a := &Actor{ID: "a1"}
	a.learnRumor(newShortOnCoinRumor("b1", "Ezekiel", now).escalated(now), now) // rung 1 hearsay
	if !a.learnRumor(newShortOnCoinRumor("b1", "Ezekiel", now), now) {          // first-hand rung 0
		t.Fatal("witnessing first-hand should change a held hearsay entry")
	}
	if r := a.Rumors[0]; !r.FirstHand || r.Rung != 0 {
		t.Fatalf("witnessing should adopt first-hand rung 0, got %+v", r)
	}

	// First-hand held, then inflated hearsay arrives → witnessed truth is sticky.
	b := &Actor{ID: "a2"}
	b.learnRumor(newShortOnCoinRumor("b1", "Ezekiel", now), now)                        // first-hand rung 0
	inflated := newShortOnCoinRumor("b1", "Ezekiel", now).escalated(now).escalated(now) // rung 2 hearsay
	if b.learnRumor(inflated, now.Add(time.Minute)) {
		t.Fatal("hearsay must not overwrite a first-hand truth (no material change)")
	}
	if r := b.Rumors[0]; !r.FirstHand || r.Rung != 0 {
		t.Fatalf("first-hand truth should stay rung 0, got %+v", r)
	}
}

func TestLearnRumorDropsSelf(t *testing.T) {
	now := knownRumorTestNow()
	a := &Actor{ID: "a1"}
	self := newShortOnCoinRumor("a1", "Aaron", now)
	if a.learnRumor(self, now) || len(a.Rumors) != 0 {
		t.Fatal("actor should not carry a rumor about itself")
	}
}

func TestLearnRumorTTLPrune(t *testing.T) {
	now := knownRumorTestNow()
	a := &Actor{ID: "a1"}
	a.learnRumor(newShortOnCoinRumor("b1", "Ezekiel", now), now)
	later := now.Add(RumorTTL + time.Minute)
	// Learning anything at `later` prunes the expired b1 rumor first.
	a.learnRumor(newShortOnCoinRumor("b2", "Josiah", later), later)
	for _, r := range a.Rumors {
		if r.SubjectID == "b1" {
			t.Fatal("expired rumor not pruned")
		}
	}
}

func TestLearnRumorCapEvictsOldest(t *testing.T) {
	base := knownRumorTestNow()
	a := &Actor{ID: "a1"}
	// Learn MaxKnownRumors+2 distinct subjects, each newer than the last.
	for i := 0; i < MaxKnownRumors+2; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		subj := ActorID("b" + string(rune('0'+i)))
		a.learnRumor(newShortOnCoinRumor(subj, "Name", at), at)
	}
	if len(a.Rumors) != MaxKnownRumors {
		t.Fatalf("known-set = %d, want cap %d", len(a.Rumors), MaxKnownRumors)
	}
	for _, r := range a.Rumors {
		if r.SubjectID == "b0" || r.SubjectID == "b1" {
			t.Fatalf("oldest rumor %s not evicted", r.SubjectID)
		}
	}
}

func TestSeedShortOnCoinRumorSeedsWitnessesNotSubject(t *testing.T) {
	now := knownRumorTestNow()
	buyer := &Actor{ID: "buyer", DisplayName: "Ezekiel Crane"}
	seller := &Actor{ID: "seller", DisplayName: "Hannah Boggs"}
	bystander := &Actor{ID: "bystander", DisplayName: "John Ellis"}
	w := &World{
		Actors: map[ActorID]*Actor{
			buyer.ID:     buyer,
			seller.ID:    seller,
			bystander.ID: bystander,
		},
		Huddles: map[HuddleID]*Huddle{
			"h1": {Members: map[ActorID]struct{}{
				buyer.ID:     {},
				seller.ID:    {},
				bystander.ID: {},
			}},
		},
	}
	entry := &PayLedgerEntry{BuyerID: buyer.ID, SellerID: seller.ID, HuddleID: "h1"}
	seedShortOnCoinRumor(w, entry, now)

	if len(seller.Rumors) != 1 || seller.Rumors[0].SubjectID != buyer.ID {
		t.Fatalf("seller should carry a rumor about the buyer, got %+v", seller.Rumors)
	}
	if !seller.Rumors[0].FirstHand || seller.Rumors[0].Rung != 0 {
		t.Fatalf("seller's seed should be first-hand rung 0, got %+v", seller.Rumors[0])
	}
	if len(bystander.Rumors) != 1 {
		t.Fatalf("co-present bystander should also witness, got %d", len(bystander.Rumors))
	}
	if len(buyer.Rumors) != 0 {
		t.Fatal("buyer must not carry a rumor about themselves")
	}
}

// TestSeedShortOnCoinRumorSkipsPCSubject pins the resident-NPC-only subject guard:
// a PC coming up short does NOT seed gossip about the player (a deliberate v1
// scope line — the town gossips about townsfolk, not the player).
func TestSeedShortOnCoinRumorSkipsPCSubject(t *testing.T) {
	now := knownRumorTestNow()
	pcBuyer := &Actor{ID: "pc", DisplayName: "The Player", Kind: KindPC}
	seller := &Actor{ID: "seller", DisplayName: "Hannah Boggs", Kind: KindNPCShared}
	w := &World{Actors: map[ActorID]*Actor{pcBuyer.ID: pcBuyer, seller.ID: seller}}
	entry := &PayLedgerEntry{BuyerID: pcBuyer.ID, SellerID: seller.ID}
	seedShortOnCoinRumor(w, entry, now)
	if len(seller.Rumors) != 0 {
		t.Fatalf("no rumor should be seeded about a PC subject, got %+v", seller.Rumors)
	}
}

func TestSalientRumorToShareExcludesPresentSubject(t *testing.T) {
	now := knownRumorTestNow()
	a := &Actor{ID: "a"}
	a.learnRumor(newShortOnCoinRumor("present", "Present Fellow", now), now)
	h := &Huddle{Members: map[ActorID]struct{}{"a": {}, "present": {}}}
	if _, ok := a.salientRumorToShare(h, now); ok {
		t.Fatal("should not share a rumor about a present huddle member")
	}
	// A rumor about an absent subject IS shareable, even if fresher gossip about
	// a present member exists.
	a.learnRumor(newShortOnCoinRumor("absent", "Absent Fellow", now.Add(time.Second)), now.Add(time.Second))
	r, ok := a.salientRumorToShare(h, now)
	if !ok || r.SubjectID != "absent" {
		t.Fatalf("should share the absent-subject rumor, got %+v ok=%v", r, ok)
	}
}

// TestSnapshotActorCarriesRumors guards the live Actor -> ActorSnapshot copy of the
// carried rumor known-set (snapshotActor). Perception reads the "## Word about the
// village" line off ActorSnapshot.Rumors, so if the snapshot ever dropped the field
// a seeded/propagated rumor would live on the actor but never reach a turn. It also
// pins the no-alias invariant: the published snapshot must not share the live
// Actor's backing array (the plain append in snapshotActor is a full deep copy).
func TestSnapshotActorCarriesRumors(t *testing.T) {
	now := knownRumorTestNow()
	a := &Actor{
		ID:          "holder",
		DisplayName: "Hannah Boggs",
		Kind:        KindNPCShared,
		Needs:       map[NeedKey]int{},
		Inventory:   map[ItemKind]int{},
	}
	a.learnRumor(newShortOnCoinRumor("subj", "Ezekiel Crane", now), now)

	snap := snapshotActor(a, 0, false)
	if len(snap.Rumors) != 1 {
		t.Fatalf("snapshot carried %d rumors, want 1", len(snap.Rumors))
	}
	if snap.Rumors[0].SubjectID != "subj" || snap.Rumors[0].Clause() != "Ezekiel Crane came up short of coin for a purchase" {
		t.Fatalf("snapshot rumor = %+v", snap.Rumors[0])
	}
	// No-alias: mutating the snapshot copy must not reach back into the live actor.
	snap.Rumors[0].Rung = MaxRumorRung
	if a.Rumors[0].Rung != 0 {
		t.Fatal("snapshot Rumors aliases the live Actor's slice")
	}
}

func TestPropagateRumorOnSpeakActiveConversantsOnly(t *testing.T) {
	now := knownRumorTestNow()
	speaker := &Actor{ID: "spk", DisplayName: "Hannah"}
	speaker.learnRumor(newShortOnCoinRumor("subj", "Ezekiel Crane", now), now) // rung 0, absent subject
	active := &Actor{ID: "act", DisplayName: "John"}                           // has spoken → receives
	silent := &Actor{ID: "sil", DisplayName: "Mary"}                           // never spoke → does not
	w := &World{Actors: map[ActorID]*Actor{
		speaker.ID: speaker, active.ID: active, silent.ID: silent,
	}}
	h := &Huddle{Members: map[ActorID]struct{}{
		speaker.ID: {}, active.ID: {}, silent.ID: {},
	}}
	h.AppendUtterance(active.ID, "John", "good morrow", now.Add(-time.Minute))
	h.AppendUtterance(speaker.ID, "Hannah", "and to you", now)

	propagateRumorOnSpeak(w, h, speaker.ID, now)

	if len(active.Rumors) != 1 {
		t.Fatalf("active conversant should receive the rumor, got %d", len(active.Rumors))
	}
	if active.Rumors[0].Rung != 1 {
		t.Fatalf("received rumor should be escalated to rung 1, got %d", active.Rumors[0].Rung)
	}
	if active.Rumors[0].FirstHand {
		t.Fatal("received rumor should be hearsay, not first-hand")
	}
	if len(silent.Rumors) != 0 {
		t.Fatal("silent member must not receive the rumor")
	}
	if len(speaker.Rumors) != 1 || speaker.Rumors[0].Rung != 0 {
		t.Fatal("speaker's own rumor should stay first-hand at rung 0")
	}
}
