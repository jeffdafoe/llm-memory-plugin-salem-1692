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
// and escalated one rung per retelling. LLM-843 adds one topic per omen
// (omen.go), seeded where the omen happens. Propagation (speak-gated, active
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

	// Omen topics (LLM-843) — the subject's omen happened (omen.go). Seeded
	// first-hand into the witness and their conversation. Unlike every other
	// topic an omen is something people tell about THEMSELVES and to the
	// subject's face ("my milk turned", "I heard your milk turned"), so the
	// self-holding and present-subject rules relax for these topics alone
	// (RumorTopicSelfTellable).
	RumorTopicOmenSourMilk     RumorTopic = "omen_sour_milk"
	RumorTopicOmenColdHearth   RumorTopic = "omen_cold_hearth"
	RumorTopicOmenFlatBread    RumorTopic = "omen_flat_bread"
	RumorTopicOmenWitheredBush RumorTopic = "omen_withered_bush"
	RumorTopicOmenFoulWell     RumorTopic = "omen_foul_well"
)

// omenRumorTopic maps an omen to its rumor topic.
func omenRumorTopic(kind OmenKind) RumorTopic {
	return RumorTopic("omen_" + string(kind))
}

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

// The omen ladders (LLM-843) climb from the plain fact to dread: by the top
// rung the house is "ill-wished". None of them says witchcraft — the village may
// think it, the engine never writes it (TestOmenTextNeverSaysWitch). Each comes
// in two forms: about the subject (%s is their name) and to the subject, for the
// subject's own copy.
var (
	omenSourMilkLadder = [MaxRumorRung + 1]string{
		"%s's milk turned sour in the pail",
		"%s's milk spoils as soon as it is drawn",
		"the milk has gone bad at %s's for days now",
		"%s's household is ill-wished — the milk sours as it is drawn",
	}
	omenSourMilkSelfLadder = [MaxRumorRung + 1]string{
		"your milk turned sour in the pail",
		"your milk spoils as soon as it is drawn",
		"the milk has gone bad at your place for days now",
		"your household is ill-wished — the milk sours as it is drawn",
	}
	omenColdHearthLadder = [MaxRumorRung + 1]string{
		"%s's hearth fire would not stay lit",
		"the fire at %s's will not catch, however dry the wood",
		"%s's hearth has been cold for days",
		"%s's house is ill-wished — no fire will burn there",
	}
	omenColdHearthSelfLadder = [MaxRumorRung + 1]string{
		"your hearth fire would not stay lit",
		"the fire at your place will not catch, however dry the wood",
		"your hearth has been cold for days",
		"your house is ill-wished — no fire will burn there",
	}
	omenFlatBreadLadder = [MaxRumorRung + 1]string{
		"%s's bread would not rise",
		"%s's loaves come out flat and heavy as stone",
		"nothing will rise in %s's oven",
		"%s's house is ill-wished — no bread will rise there",
	}
	omenFlatBreadSelfLadder = [MaxRumorRung + 1]string{
		"your bread would not rise",
		"your loaves come out flat and heavy as stone",
		"nothing will rise in your oven",
		"your house is ill-wished — no bread will rise there",
	}
	omenWitheredBushLadder = [MaxRumorRung + 1]string{
		"the berries withered on the bush when %s went to pick them",
		"%s found a whole bush withered overnight",
		"the bushes wither wherever %s goes to pick",
		"%s is ill-wished — the fruit withers before it can be picked",
	}
	omenWitheredBushSelfLadder = [MaxRumorRung + 1]string{
		"the berries withered on the bush when you went to pick them",
		"you found a whole bush withered overnight",
		"the bushes wither wherever you go to pick",
		"you are ill-wished — the fruit withers before you can pick it",
	}
	omenFoulWellLadder = [MaxRumorRung + 1]string{
		"the well water tasted of iron to %s",
		"%s drew foul water from the well",
		"the well water turned bad for %s and has not come right",
		"%s is ill-wished — even the well water turns against them",
	}
	omenFoulWellSelfLadder = [MaxRumorRung + 1]string{
		"the well water tasted of iron to you",
		"you drew foul water from the well",
		"the well water turned bad for you and has not come right",
		"you are ill-wished — even the well water turns against you",
	}
)

// ladders maps each topic to its escalation ladder. A topic with no ladder
// renders no clause (defensive — every real topic has one).
var ladders = map[RumorTopic]*[MaxRumorRung + 1]string{
	RumorTopicShortOnCoin:      &shortOnCoinLadder,
	RumorTopicOmenSourMilk:     &omenSourMilkLadder,
	RumorTopicOmenColdHearth:   &omenColdHearthLadder,
	RumorTopicOmenFlatBread:    &omenFlatBreadLadder,
	RumorTopicOmenWitheredBush: &omenWitheredBushLadder,
	RumorTopicOmenFoulWell:     &omenFoulWellLadder,
}

// selfLadders holds the to-the-subject form of the topics a subject may carry
// about themselves. Membership IS the self-tellable rule (RumorTopicSelfTellable).
var selfLadders = map[RumorTopic]*[MaxRumorRung + 1]string{
	RumorTopicOmenSourMilk:     &omenSourMilkSelfLadder,
	RumorTopicOmenColdHearth:   &omenColdHearthSelfLadder,
	RumorTopicOmenFlatBread:    &omenFlatBreadSelfLadder,
	RumorTopicOmenWitheredBush: &omenWitheredBushSelfLadder,
	RumorTopicOmenFoulWell:     &omenFoulWellSelfLadder,
}

// RumorTopicSelfTellable reports whether a topic is one people tell about
// themselves and to the subject's face — the omens (LLM-843). For these the
// subject may hold its own rumor, and the rumor is shared and shown even with
// the subject present. Being short of coin keeps both rules: nobody gossips
// that to your face.
func RumorTopicSelfTellable(topic RumorTopic) bool {
	return selfLadders[topic] != nil
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

// ClauseFor renders the rumor as holder carries it: worded to the holder when
// the holder is its subject (a self-tellable topic, LLM-843), else Clause.
func (r KnownRumor) ClauseFor(holder ActorID) string {
	if holder != "" && r.SubjectID == holder {
		if self := selfLadders[r.Topic]; self != nil {
			return self[clampRung(r.Rung)]
		}
	}
	return r.Clause()
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
// holder) is dropped, except for a self-tellable topic (an omen, LLM-843).
// Returns true iff the held belief changed (gained, promoted to first-hand, or
// raised a rung); a pure recency refresh returns false.
func (a *Actor) learnRumor(r KnownRumor, now time.Time) bool {
	if a == nil || r.SubjectID == "" {
		return false
	}
	if r.SubjectID == a.ID && !RumorTopicSelfTellable(r.Topic) {
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

// seedOmenRumor plants a just-happened omen (LLM-843) as first-hand knowledge in
// the witness — who may be its subject — and in everyone in the witness's
// conversation, who saw it too. The subject must be a resident NPC (the roll
// only arms those). Social layer only. Runs on the world goroutine.
func seedOmenRumor(w *World, kind OmenKind, witness *Actor, subjectID ActorID, at time.Time) {
	if w == nil || witness == nil {
		return
	}
	if !omenResident(w.Actors[subjectID]) {
		return
	}
	subjectName := actorDisplayName(w, subjectID)
	if subjectName == "" {
		return
	}
	seed := KnownRumor{
		Topic:       omenRumorTopic(kind),
		SubjectID:   subjectID,
		SubjectName: subjectName,
		Rung:        0,
		HeardAt:     at,
		FirstHand:   true,
	}
	witness.learnRumor(seed, at)
	h := w.Huddles[witness.CurrentHuddleID]
	if h == nil {
		return
	}
	for id := range h.Members {
		if id == witness.ID {
			continue
		}
		if other := w.Actors[id]; other != nil && other.Kind != KindDecorative {
			other.learnRumor(seed, at)
		}
	}
}

// salientRumorToShare picks the one rumor this actor will pass along in a huddle:
// the freshest live rumor whose subject is NOT a current member of the huddle
// (nobody gossips about someone to their face) — except a self-tellable topic,
// an omen, which people tell about themselves and to the subject (LLM-843).
// Prunes expired entries first. ok is false when the actor holds nothing
// shareable here.
func (a *Actor) salientRumorToShare(h *Huddle, now time.Time) (KnownRumor, bool) {
	if a == nil || h == nil {
		return KnownRumor{}, false
	}
	a.pruneExpiredRumors(now)
	best := -1
	for i := range a.Rumors {
		if _, present := h.Members[a.Rumors[i].SubjectID]; present && !RumorTopicSelfTellable(a.Rumors[i].Topic) {
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
