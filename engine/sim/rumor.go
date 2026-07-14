package sim

import (
	"fmt"
	"time"
)

// rumor.go — the village rumor layer (LLM-387).
//
// A rumor is a DELIBERATELY-fallible social belief one actor carries about
// another. Unlike a perception fact — a faithful readout of real system state —
// a rumor may be exaggerated or outright false, and it is NOT re-synced from the
// world: it is a frozen claim that spreads and DISTORTS through conversation,
// then decays. The only engine-wide fabrication constraint is commerce integrity
// (a seller can't advertise stock it lacks); the social layer is unconstrained,
// so a rumor is free to outlive and outgrow the fact that seeded it — that gap
// is the point, not a bug.
//
// v1 carries a single topic — short_on_coin — seeded from a witnessed
// insufficient-funds settlement (the seller is told the buyer couldn't cover),
// and escalated one rung per retelling. Propagation (speak-gated, active
// conversants) and perception live alongside. The known-set is in-memory only:
// carried through CloneActor and the published snapshot, but deliberately NOT
// checkpointed — the same posture as other decaying per-actor state (Observed,
// RecentProduce). A restart clears in-flight gossip; it re-seeds from live
// commerce within the TTL window.

// RumorTopic is the closed vocabulary of rumor kinds.
type RumorTopic string

const (
	// RumorTopicShortOnCoin — the subject was seen unable to cover a purchase.
	// Seeded from a PayLedgerStateFailedInsufficientFunds settlement into the
	// witnessing seller's known-set.
	RumorTopicShortOnCoin RumorTopic = "short_on_coin"
)

// MaxRumorRung is the top of every ladder — the most embellished, least true
// form of a rumor. Escalation clamps here.
const MaxRumorRung = 3

// MaxKnownRumors caps one actor's known-set; on overflow the oldest (by HeardAt)
// is evicted. Small on purpose — the known-set is cost-bounded, not a log.
const MaxKnownRumors = 8

// RumorTTL is how long a carried rumor stays live before it decays out of the
// known-set: long enough to travel a few hops, short enough that stale gossip
// clears itself without any real event to contradict it.
const RumorTTL = 6 * time.Hour

// KnownRumor is one rumor an actor carries about a subject. Ids + a render-time
// clause, never stored prose. The subject name is resolved once at seed time —
// a rumor never re-resolves its subject, mirroring how it never re-syncs its
// truth.
type KnownRumor struct {
	Topic       RumorTopic
	SubjectID   ActorID
	SubjectName string
	Rung        int       // embellishment level — index into the topic's ladder
	HeardAt     time.Time // when THIS holder came to carry it; drives recency + TTL
	FirstHand   bool      // true for the original witness; false once relayed as hearsay
}

// Key identifies a rumor for de-dup within a known-set: at most one live rumor
// per (topic, subject) per holder. A re-seed or re-hearing updates the existing
// entry (freshening HeardAt, taking the higher Rung) rather than piling up.
func (r KnownRumor) Key() string {
	return string(r.Topic) + "|" + string(r.SubjectID)
}

// shortOnCoinLadder escalates a short-on-coin rumor from the true, witnessed
// fact (rung 0) to pure fiction (rung MaxRumorRung). The seed is true, the top
// is invented, and no coin ever moved past rung 0. %s is the subject's name.
var shortOnCoinLadder = [MaxRumorRung + 1]string{
	"%s came up short of coin for a purchase",
	"%s couldn't pay what they owed",
	"%s has fallen behind on their debts",
	"%s is ruined — in debt to half the village",
}

// ladders maps each topic to its escalation ladder. A topic with no ladder
// renders no clause (defensive — every real topic has one).
var ladders = map[RumorTopic]*[MaxRumorRung + 1]string{
	RumorTopicShortOnCoin: &shortOnCoinLadder,
}

// clampRung keeps a rung inside [0, MaxRumorRung].
func clampRung(rung int) int {
	if rung < 0 {
		return 0
	}
	if rung > MaxRumorRung {
		return MaxRumorRung
	}
	return rung
}

// Clause renders the rumor to the diegetic clause a carrier would pass along, at
// its current rung. Empty for an unknown topic or a missing subject name.
func (r KnownRumor) Clause() string {
	if r.SubjectName == "" {
		return ""
	}
	ladder := ladders[r.Topic]
	if ladder == nil {
		return ""
	}
	return fmt.Sprintf(ladder[clampRung(r.Rung)], r.SubjectName)
}

// Expired reports whether the rumor has aged past RumorTTL as of now.
func (r KnownRumor) Expired(now time.Time) bool {
	return now.Sub(r.HeardAt) > RumorTTL
}

// escalated returns a copy of the rumor as it is carried onward by a new holder:
// one rung higher (clamped), no longer first-hand, stamped as heard now. This is
// the telephone-game distortion applied on each propagation hop.
func (r KnownRumor) escalated(now time.Time) KnownRumor {
	r.Rung = clampRung(r.Rung + 1)
	r.FirstHand = false
	r.HeardAt = now
	return r
}

// newShortOnCoinRumor builds the seed rumor a witness forms when they see the
// subject come up short at a purchase: first-hand, rung 0 (the true fact),
// stamped now.
func newShortOnCoinRumor(subjectID ActorID, subjectName string, now time.Time) KnownRumor {
	return KnownRumor{
		Topic:       RumorTopicShortOnCoin,
		SubjectID:   subjectID,
		SubjectName: subjectName,
		Rung:        0,
		HeardAt:     now,
		FirstHand:   true,
	}
}

// learnRumor folds r into the actor's known-set: at most one entry per
// (topic, subject). FIRST-HAND KNOWLEDGE IS AUTHORITATIVE — witnessing the event
// yourself gives you the truth, so (a) an incoming first-hand rumor supersedes a
// held hearsay entry, adopting its true (rung-0) form, and (b) inbound hearsay
// NEVER overwrites a first-hand entry, only refreshing its recency. This keeps
// the perception first-hand/hearsay split trustworthy — a witness is never
// silently reframed as "word has it" about something they saw. Between two
// same-provenance entries the higher rung wins (the telephone-game escalation,
// which still climbs freely among the many non-witnesses). Prunes expired first,
// then evicts the oldest over MaxKnownRumors. A self-rumor (subject is the
// holder) is dropped. Returns true iff the held belief changed (gained, promoted
// to first-hand, or raised a rung); a pure recency refresh returns false.
func (a *Actor) learnRumor(r KnownRumor, now time.Time) bool {
	if a == nil || r.SubjectID == "" || r.SubjectID == a.ID {
		return false
	}
	a.pruneExpiredRumors(now)
	key := r.Key()
	for i := range a.Rumors {
		if a.Rumors[i].Key() != key {
			continue
		}
		existing := &a.Rumors[i]
		changed := false
		switch {
		case r.FirstHand && !existing.FirstHand:
			// Direct witnessing supersedes hearsay: adopt the true first-hand
			// form, overriding any inflated relayed rung.
			existing.Rung = r.Rung
			existing.FirstHand = true
			changed = true
		case r.FirstHand == existing.FirstHand && r.Rung > existing.Rung:
			// Same provenance tier — hearsay escalates by rung. (Two first-hand
			// seeds are both rung 0, so this is a no-op for them.)
			existing.Rung = r.Rung
			changed = true
		}
		// A first-hand entry meeting inbound hearsay matches neither case above,
		// so its belief is untouched — only recency refreshes.
		if r.HeardAt.After(existing.HeardAt) {
			existing.HeardAt = r.HeardAt
		}
		return changed
	}
	a.Rumors = append(a.Rumors, r)
	a.evictOldestRumorsOverCap()
	return true
}

// pruneExpiredRumors drops rumors aged past RumorTTL, in place, preserving order.
func (a *Actor) pruneExpiredRumors(now time.Time) {
	if len(a.Rumors) == 0 {
		return
	}
	kept := a.Rumors[:0]
	for _, r := range a.Rumors {
		if !r.Expired(now) {
			kept = append(kept, r)
		}
	}
	a.Rumors = kept
}

// evictOldestRumorsOverCap trims the known-set to MaxKnownRumors by dropping the
// oldest entry (by HeardAt) until within cap. The set is tiny, so a linear
// min-scan per eviction is fine.
func (a *Actor) evictOldestRumorsOverCap() {
	for len(a.Rumors) > MaxKnownRumors {
		oldest := 0
		for i := 1; i < len(a.Rumors); i++ {
			if a.Rumors[i].HeardAt.Before(a.Rumors[oldest].HeardAt) {
				oldest = i
			}
		}
		a.Rumors = append(a.Rumors[:oldest], a.Rumors[oldest+1:]...)
	}
}

// seedShortOnCoinRumor plants the witnessed coin-short rumor from a settlement
// that fell through for insufficient funds: the subject is the buyer who
// couldn't cover; the carriers are the seller (first-hand — the engine just told
// them the buyer couldn't cover) plus any other co-present members of the
// settling huddle. learnRumor filters the subject out, so a self-seed can't
// happen. Social layer only — no coin or state moves. Runs on the world
// goroutine (called from finalizePayLedgerTerminal).
func seedShortOnCoinRumor(w *World, entry *PayLedgerEntry, at time.Time) {
	if w == nil || entry == nil || entry.BuyerID == "" {
		return
	}
	// Subject must be a resident NPC. Rumors are townsfolk gossiping about
	// townsfolk (LLM-387); seeding one about a PC buyer would have the village
	// gossip about the player being broke — a separate, deliberate feature, not a
	// fallout of the pay path. Decorative actors never transact, so this also
	// no-ops them.
	subject := w.Actors[entry.BuyerID]
	if subject == nil || (subject.Kind != KindNPCStateful && subject.Kind != KindNPCShared) {
		return
	}
	subjectName := actorDisplayName(w, entry.BuyerID)
	if subjectName == "" {
		return
	}
	seed := newShortOnCoinRumor(entry.BuyerID, subjectName, at)

	if seller := w.Actors[entry.SellerID]; seller != nil {
		seller.learnRumor(seed, at)
	}
	if entry.HuddleID == "" {
		return
	}
	h := w.Huddles[entry.HuddleID]
	if h == nil {
		return
	}
	for id := range h.Members {
		if id == entry.SellerID {
			continue // already seeded above
		}
		if witness := w.Actors[id]; witness != nil {
			witness.learnRumor(seed, at) // subject (buyer) filtered inside learnRumor
		}
	}
}

// salientRumorToShare picks the one rumor this actor will pass along in a huddle:
// the freshest live rumor whose subject is NOT a current member of the huddle
// (nobody gossips about someone to their face). Prunes expired entries first.
// ok is false when the actor holds nothing shareable here.
func (a *Actor) salientRumorToShare(h *Huddle, now time.Time) (KnownRumor, bool) {
	if a == nil || h == nil {
		return KnownRumor{}, false
	}
	a.pruneExpiredRumors(now)
	best := -1
	for i := range a.Rumors {
		if _, present := h.Members[a.Rumors[i].SubjectID]; present {
			continue // don't spread gossip about someone standing right here
		}
		if best < 0 || a.Rumors[i].HeardAt.After(a.Rumors[best].HeardAt) {
			best = i
		}
	}
	if best < 0 {
		return KnownRumor{}, false
	}
	return a.Rumors[best], true
}

// propagateRumorOnSpeak spreads one rumor from the speaker to the huddle's other
// ACTIVE conversants — members who have themselves spoken (non-zero
// LastUtteranceAtBy) — escalated one rung on the hop. Speak-gated +
// active-conversants-only is the crux: a silent bystander neither gives nor
// gets, so a rumor travels only through actual conversation, not mere
// co-presence. Cap-1 per speak; the utterance text is never read (we move the
// token the speaker holds, not what they said). Runs on the world goroutine
// (called from SpeakTo once the utterance is recorded).
func propagateRumorOnSpeak(w *World, h *Huddle, speakerID ActorID, at time.Time) {
	if w == nil || h == nil {
		return
	}
	speaker := w.Actors[speakerID]
	if speaker == nil || len(speaker.Rumors) == 0 {
		return
	}
	rumor, ok := speaker.salientRumorToShare(h, at)
	if !ok {
		return
	}
	spread := rumor.escalated(at)
	for id := range h.Members {
		if id == speakerID {
			continue
		}
		if h.LastUtteranceAtBy(id).IsZero() {
			continue // silent bystander — active conversants only
		}
		if listener := w.Actors[id]; listener != nil {
			listener.learnRumor(spread, at) // subject filtered inside learnRumor
		}
	}
}
