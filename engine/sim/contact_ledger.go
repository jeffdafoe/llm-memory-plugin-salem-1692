package sim

import (
	"context"
	"log"
	"sort"
	"time"
)

// contact_ledger.go — per-pair conversational recency (LLM-547).
//
// The engine has always known who an actor has walked past and what its round
// has left; it has never told the actor who it has already SPOKEN WITH. The
// trigger case: Constable Marsh opened a fresh conversation with Prudence Ward
// three times inside one round, and she was the one who noticed ("Three times
// is thrice too many").
//
// This is deliberately NOT the relationship system. actor.Relationships
// (relationship_commands.go) is the shared-VA continuity substrate — salient
// facts, an LLM-folded summary, nightly consolidation — and it is gated to
// KindNPCShared with visitors skipped on both sides. That gate is correct for
// what it does and must not be widened: a stateful NPC gets its per-peer memory
// from its own VA, and a visitor's ActorID is deleted at cleanup (widening it
// would mint orphan refs).
//
// The contact ledger answers a much smaller question — "have I had my word with
// this person, and how lately" — for EVERY actor kind, PCs and visitors
// included. It is a bare timestamp trail per ordered pair. Cheap enough that
// covering everyone costs nothing, which is the whole reason it is separate.
//
// DURABILITY. Unlike PayLedger / LaborLedger, this is genuinely persisted: it
// rides the checkpoint aggregate set and is rehydrated at boot. The reason is
// LLM-546 — an innkeeper greeted a player as a stranger six hours after selling
// him porridge, with three deploys in between. On our deploy cadence a purely
// in-memory ledger would leave the continuity tier empty most of the working
// day, and it is a human player who notices. The write itself stays in memory;
// durability rides the existing 1-minute checkpointer, so nothing is added to
// the speech hot path.

// ContactRecord is one ordered pair's conversational history: the times the
// subject and the peer were in the same conversation while one of them spoke.
//
// Ordered, not symmetric — the ledger holds both directions and each is written
// independently. They will normally agree, but nothing depends on that, and
// keeping them separate means a read never has to canonicalize the key.
type ContactRecord struct {
	// At is the contact trail, oldest-first, capped at
	// MaxContactsPerPair and pruned to the recall horizon on write.
	//
	// A trail rather than a bare last-seen timestamp because the tiers count:
	// "twice already this round" is a different line from "already this round",
	// and that distinction is the one that carries her state rather than just
	// his history.
	At []time.Time
}

// MaxContactsPerPair caps a pair's trail. The tiers never count past two inside
// the brake window, so a handful of entries is already more than the render can
// use; the cap exists so a long conversation between two actors cannot grow the
// slice without bound between prunes.
const MaxContactsPerPair = 8

// DefaultContactBrakeWindow is how recently a contact must have happened for the
// scene to read as "you have already had your word with them" rather than as
// history to build on. Comfortably longer than one constable round (the round
// interval is 4h live, but a round WALKS in well under an hour) so a repeat
// inside a single circuit always trips the brake.
const DefaultContactBrakeWindow = 2 * time.Hour

// ContactFutureSkewTolerance is how far ahead of the load clock a persisted
// contact may be stamped and still be believed. Beyond it the entry is treated
// as a bad value and dropped at rehydrate.
//
// Only applied on the way IN from the database. The write path has no second
// clock to validate against — `at` IS the engine's tick time — so there is
// nothing to compare it with that would not itself be a determinism hazard.
const ContactFutureSkewTolerance = 5 * time.Minute

// DefaultContactRecallHorizon is how far back a contact is remembered at all.
// Past it the pair reads as strangers again and the record is dropped. Eight
// hours is about a working day in the village: it comfortably covers "earlier
// today" without letting yesterday's conversation surface as though it were
// this morning's.
const DefaultContactRecallHorizon = 8 * time.Hour

// ContactBrakeWindow / ContactRecallHorizon read the tunables off settings,
// falling back to the defaults when unset so a world built without the
// environment loader (every unit test) behaves sensibly.
func (w *World) ContactBrakeWindow() time.Duration {
	if w.Settings.ContactBrakeWindow > 0 {
		return w.Settings.ContactBrakeWindow
	}
	return DefaultContactBrakeWindow
}

func (w *World) ContactRecallHorizon() time.Duration {
	if w.Settings.ContactRecallHorizon > 0 {
		return w.Settings.ContactRecallHorizon
	}
	return DefaultContactRecallHorizon
}

// RecordContact credits one ordered pair with a conversational contact at `at`.
//
// Called from the Speak commit path for every huddle peer, in both directions:
// having your word with someone means being in the conversation, not being
// named in it. Ward addressed the constable and he addressed her; either counts.
//
// Self-pairs and empty ids are dropped rather than erroring — this runs after
// the utterance has already emitted, so there is nothing useful a caller could
// do with an error and nothing worth aborting.
func (w *World) RecordContact(subjectID, peerID ActorID, at time.Time) {
	if subjectID == "" || peerID == "" || subjectID == peerID {
		return
	}
	if w.ContactLedger == nil {
		w.ContactLedger = make(map[ActorID]map[ActorID]*ContactRecord)
	}
	byPeer, ok := w.ContactLedger[subjectID]
	if !ok {
		byPeer = make(map[ActorID]*ContactRecord)
		w.ContactLedger[subjectID] = byPeer
	}
	rec, ok := byPeer[peerID]
	if !ok {
		rec = &ContactRecord{}
		byPeer[peerID] = rec
	}
	rec.At = append(rec.At, at)
	// Restore chronological order before pruning. `at` is the caller's clock, not
	// this function's, so an out-of-order append is possible — a replayed command,
	// a delayed tick, an operator-injected time. Both prune steps below select by
	// POSITION (drop a prefix, keep a suffix), so on an unsorted trail the horizon
	// scan would stop early and leave an aged entry behind, and the cap would
	// discard the newest contacts instead of the oldest. Sorting first makes both
	// correct by construction rather than by an assumption about callers.
	//
	// Free in practice: the trail is capped at MaxContactsPerPair, and the common
	// case is an append that is already in order, which sort.SliceStable walks
	// once.
	if len(rec.At) > 1 && rec.At[len(rec.At)-1].Before(rec.At[len(rec.At)-2]) {
		sort.SliceStable(rec.At, func(i, j int) bool { return rec.At[i].Before(rec.At[j]) })
	}
	// Measure the horizon from the NEWEST entry in the trail, not from `at`.
	// They are the same for an in-order write, but for a late-arriving older one
	// `at` is not "now" — using it would push the cutoff backwards and let
	// entries the trail had already outlived survive. The newest contact known is
	// the best estimate of the present this function has.
	rec.prune(rec.At[len(rec.At)-1].Add(-w.ContactRecallHorizon()))
}

// prune drops entries older than `cutoff` and enforces the per-pair cap, keeping
// the most recent. Requires a chronologically ordered trail — every caller
// establishes that first (RecordContact sorts, RehydrateContactLedger sorts).
func (r *ContactRecord) prune(cutoff time.Time) {
	first := 0
	for first < len(r.At) && r.At[first].Before(cutoff) {
		first++
	}
	if first > 0 {
		r.At = append(r.At[:0], r.At[first:]...)
	}
	if len(r.At) > MaxContactsPerPair {
		r.At = append(r.At[:0], r.At[len(r.At)-MaxContactsPerPair:]...)
	}
}

// ContactTier is what the scene says about a pair, derived world-side so render
// only selects phrasing (the felt-needs shape: the engine computes the
// judgment, the render picks the words).
type ContactTier int

const (
	// ContactTierNone — no contact inside the recall horizon. The scene says
	// nothing; they read as strangers, which is the truth.
	ContactTierNone ContactTier = iota

	// ContactTierContinuity — they have spoken, but not lately. "You had your
	// word with Prudence Ward earlier today." History to build on.
	ContactTierContinuity

	// ContactTierBrakeQuiet — one contact inside the brake window. "You have
	// already had your word with her." A reason not to re-open, stated once.
	ContactTierBrakeQuiet

	// ContactTierBrakeWeighted — two or more inside the brake window. This is
	// the tier that introduces HER state, not just his history: she has said her
	// piece. Ward's actual rebuke arrived at the third approach; the scene
	// should let him feel it coming before she has to deliver it.
	ContactTierBrakeWeighted
)

// ContactTierFor derives the tier for one ordered pair as of `now`.
//
// Reads without mutating: perception builds off a published snapshot and must
// never write back through it, so the horizon filter is applied here rather
// than by pruning. A record whose entries have all aged out reads as
// ContactTierNone until the next write prunes it (or the next boot drops it).
func (w *World) ContactTierFor(subjectID, peerID ActorID, now time.Time) (ContactTier, int) {
	rec := w.contactRecord(subjectID, peerID)
	if rec == nil {
		return ContactTierNone, 0
	}
	return rec.tier(now, w.ContactBrakeWindow(), w.ContactRecallHorizon())
}

// ContactTierFor derives the tier off a published snapshot — the form
// perception actually uses. Same classifier as the World method; the windows
// were resolved (defaults applied) at publish time.
func (s *Snapshot) ContactTierFor(subjectID, peerID ActorID, now time.Time) (ContactTier, int) {
	if s == nil || s.ContactLedger == nil {
		return ContactTierNone, 0
	}
	byPeer, ok := s.ContactLedger[subjectID]
	if !ok {
		return ContactTierNone, 0
	}
	brake, horizon := s.ContactBrakeWindow, s.ContactRecallHorizon
	if brake <= 0 {
		brake = DefaultContactBrakeWindow
	}
	if horizon <= 0 {
		horizon = DefaultContactRecallHorizon
	}
	return byPeer[peerID].tier(now, brake, horizon)
}

func (w *World) contactRecord(subjectID, peerID ActorID) *ContactRecord {
	if w.ContactLedger == nil {
		return nil
	}
	byPeer, ok := w.ContactLedger[subjectID]
	if !ok {
		return nil
	}
	return byPeer[peerID]
}

// tier classifies a trail. recentCount is the number of contacts inside the
// brake window — returned alongside the tier because the weighted line wants to
// say "twice", and a caller that re-derived it could drift from the tier.
func (r *ContactRecord) tier(now time.Time, brake, horizon time.Duration) (ContactTier, int) {
	if r == nil {
		return ContactTierNone, 0
	}
	horizonCutoff := now.Add(-horizon)
	brakeCutoff := now.Add(-brake)

	within, recent := 0, 0
	for _, t := range r.At {
		if t.Before(horizonCutoff) {
			continue
		}
		within++
		if !t.Before(brakeCutoff) {
			recent++
		}
	}
	switch {
	case within == 0:
		return ContactTierNone, 0
	case recent == 0:
		return ContactTierContinuity, 0
	case recent == 1:
		return ContactTierBrakeQuiet, recent
	default:
		return ContactTierBrakeWeighted, recent
	}
}

// CloneContactLedger deep-copies the ledger for the published snapshot and the
// checkpoint aggregate. Both readers run off the world goroutine, so every
// slice has to be copied — sharing the backing array would let a later append
// mutate what a reader is walking.
func CloneContactLedger(src map[ActorID]map[ActorID]*ContactRecord) map[ActorID]map[ActorID]*ContactRecord {
	if src == nil {
		return nil
	}
	out := make(map[ActorID]map[ActorID]*ContactRecord, len(src))
	for subjectID, byPeer := range src {
		clonedPeers := make(map[ActorID]*ContactRecord, len(byPeer))
		for peerID, rec := range byPeer {
			if rec == nil {
				continue
			}
			at := make([]time.Time, len(rec.At))
			copy(at, rec.At)
			clonedPeers[peerID] = &ContactRecord{At: at}
		}
		out[subjectID] = clonedPeers
	}
	return out
}

// rehydrateContactLedgerOnLoad loads the persisted trail into
// World.ContactLedger at boot (FinalizeLoad), applying the recall horizon as it
// goes so a restart after a long quiet stretch comes back with the pairs that
// still mean something and nothing else.
//
// A partially-wired repo (catalog-only loads, tests that hand-build a
// sim.Repository without this tier) leaves Contacts nil — treated as "no
// history" rather than panicking, matching the loader's nil-repo tolerance
// elsewhere.
func (w *World) rehydrateContactLedgerOnLoad(ctx context.Context) error {
	if w.repo.Contacts == nil {
		if w.ContactLedger == nil {
			w.ContactLedger = make(map[ActorID]map[ActorID]*ContactRecord)
		}
		return nil
	}
	pairs, err := w.repo.Contacts.LoadAll(ctx)
	if err != nil {
		return err
	}
	w.ContactLedger = RehydrateContactLedger(pairs, time.Now(), w.ContactRecallHorizon())
	if kept := len(w.ContactLedger); kept > 0 {
		log.Printf("sim: rehydrated conversational contacts for %d actor(s) (%d of %d stored pair(s) still inside the recall horizon)",
			kept, countContactPairs(w.ContactLedger), len(pairs))
	}
	return nil
}

// countContactPairs totals the ordered pairs held in the ledger — the log line's
// "still inside the horizon" figure, against the stored row count.
func countContactPairs(ledger map[ActorID]map[ActorID]*ContactRecord) int {
	n := 0
	for _, byPeer := range ledger {
		n += len(byPeer)
	}
	return n
}

// ContactPair is one flattened ordered-pair row, for the durable write and for
// operator reads. The repo layer persists this shape directly.
type ContactPair struct {
	SubjectID ActorID
	PeerID    ActorID
	At        []time.Time
}

// FlattenContactLedger renders the ledger as a deterministic ordered-pair slice
// (subject, then peer) so the checkpoint write and any operator dump are stable
// run to run. Empty trails are dropped — a pair with nothing inside the horizon
// is not worth a row.
func FlattenContactLedger(src map[ActorID]map[ActorID]*ContactRecord) []ContactPair {
	out := make([]ContactPair, 0, len(src))
	for subjectID, byPeer := range src {
		for peerID, rec := range byPeer {
			if rec == nil || len(rec.At) == 0 {
				continue
			}
			at := make([]time.Time, len(rec.At))
			copy(at, rec.At)
			out = append(out, ContactPair{SubjectID: subjectID, PeerID: peerID, At: at})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubjectID != out[j].SubjectID {
			return out[i].SubjectID < out[j].SubjectID
		}
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

// RehydrateContactLedger rebuilds the in-memory ledger from persisted pairs,
// dropping anything older than the recall horizon as of `now`.
//
// Pruning at load rather than by a sweep, matching the RecurringVisitors
// posture: the rows are tiny and bounded by actor pairs, so there is nothing to
// reclaim between boots and a background sweep would be pure machinery. A pair
// left with no surviving entries is dropped entirely rather than kept empty.
//
// Orphan refs are tolerated by design and NOT validated against the actor set
// here. A visitor's ActorID is deleted at cleanup, so a contact with a departed
// traveller will point at an actor that no longer exists — harmless, because
// every read is keyed by a co-present peer's id, and an actor who is gone is
// never co-present. Filtering them out would need the actor map, which the
// caller has, but it would also silently drop a returner whose row is
// legitimately re-minted under the same id.
func RehydrateContactLedger(pairs []ContactPair, now time.Time, horizon time.Duration) map[ActorID]map[ActorID]*ContactRecord {
	if horizon <= 0 {
		horizon = DefaultContactRecallHorizon
	}
	cutoff := now.Add(-horizon)
	// Anything stamped meaningfully in the future is a bad value, not a contact:
	// only a clock correction between boots or an out-of-band edit can produce
	// one, and it would otherwise sit in the brake tier until `future + horizon`,
	// telling an actor it had already spoken with someone it had not. The trail is
	// prompt-facing, so the loader validates rather than trusting the database.
	// The tolerance absorbs ordinary skew across a restart without discarding a
	// contact recorded moments before shutdown.
	future := now.Add(ContactFutureSkewTolerance)
	out := make(map[ActorID]map[ActorID]*ContactRecord)
	for _, p := range pairs {
		if p.SubjectID == "" || p.PeerID == "" || p.SubjectID == p.PeerID {
			continue
		}
		kept := make([]time.Time, 0, len(p.At))
		for _, t := range p.At {
			if t.Before(cutoff) || t.After(future) {
				continue
			}
			kept = append(kept, t)
		}
		if len(kept) == 0 {
			continue
		}
		sort.Slice(kept, func(i, j int) bool { return kept[i].Before(kept[j]) })
		if len(kept) > MaxContactsPerPair {
			kept = kept[len(kept)-MaxContactsPerPair:]
		}
		byPeer, ok := out[p.SubjectID]
		if !ok {
			byPeer = make(map[ActorID]*ContactRecord)
			out[p.SubjectID] = byPeer
		}
		byPeer[p.PeerID] = &ContactRecord{At: kept}
	}
	return out
}
