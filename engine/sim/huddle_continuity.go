package sim

import "time"

// huddle_continuity.go — LLM-170. Carry a conversation across huddle re-formation
// at the same structure.
//
// A Huddle is ephemeral: a clique that disperses and reconvenes at the same place
// mints a FRESH huddle each time (at most one active huddle per structure, so the
// old one concludes and a new one forms). Two things that should belong to the
// CONVERSATION were scoped to the huddle, so both reset on every re-formation:
//
//   1. RecentUtterances — the "## Recent conversation here" ring. Empty on a new
//      huddle, so the same peers read as strangers and re-greet ("Good morning",
//      in the afternoon) every churn cycle.
//   2. The loop-sweep clock (LoopingSince) plus the LastProgressAt guard. Every
//      membership change stamps LastProgressAt=now, which disarms the loop spell
//      before its persistence gate elapses — so a clique that churns huddles every
//      couple of minutes loops forever without the sweep (LLM-159) ever concluding
//      it, and the LLM-169 per-tick steer never arms either.
//
// The durable scope that already survives the churn is the structure (its Scene is
// reused across conversations — findOrCreateStructureScene). So when a huddle
// concludes we snapshot its conversation into a per-structure carry-over, and a
// newly-formed huddle at that structure adopts it — but only when the speaker who
// re-forms it was part of that conversation (a genuinely new arrival starts fresh).
// That single mechanism fixes the re-greeting (the ring is carried) AND lets the
// loop machinery span the churn (the spell + progress baseline are carried, and a
// same-clique re-join no longer counts as progress — see JoinHuddle / the dropped
// leave-stamp in leaveCurrentHuddle).
//
// The carry-over is transient world-goroutine state: keyed by StructureID (so it is
// bounded by the structure count, not unbounded growth), never checkpointed, and
// cleared at boot — chatter is restart-lossy by design, same posture as the ring
// and LastActivityAt it carries.

// HuddleContinuityWindowDefault is how long after a structure huddle concludes a
// re-formation still counts as the SAME conversation (so the ring + loop state are
// carried). 5 minutes comfortably spans the observed ~2-minute Walker churn cycle
// while staying short enough that a genuinely separate later visit starts fresh.
// Tunable via huddle_continuity_window_seconds.
const HuddleContinuityWindowDefault = 5 * time.Minute

// conversationCarryover is the snapshot of a just-concluded structure conversation,
// held per structure so the next huddle to form there can adopt it. members is the
// set of recent SPEAKERS (derived from the ring, not Huddle.Members — the latter is
// already empty on the last-member-leave conclude path), which is exactly "who was
// conversing here." All fields are deep copies isolated from the concluded huddle.
type conversationCarryover struct {
	utterances     []Utterance
	members        map[ActorID]struct{}
	loopingSince   *time.Time
	loopingReason  string
	lastProgressAt time.Time
	concludedAt    time.Time

	// turnsSinceProgress carries the LLM-333 endurance counter across the churn,
	// exactly as the ring carries the utterance arm's durable condition — a clique
	// that concludes and re-forms every couple of minutes must not reset its
	// spend-without-progress tally each cycle. The loop sweep's post-conclude
	// carry-over reset deliberately clears loopingSince (the CLOCK gets a fresh
	// chance) but NOT this counter (the CONDITION persists), matching how the
	// carried ring keeps huddleLoopContentPresent true across a sweep conclude.
	turnsSinceProgress int
}

// effectiveHuddleContinuityWindow returns the configured continuity window or the
// default when WorldSettings.HuddleContinuityWindow is zero/unset.
func effectiveHuddleContinuityWindow(s WorldSettings) time.Duration {
	if s.HuddleContinuityWindow > 0 {
		return s.HuddleContinuityWindow
	}
	return HuddleContinuityWindowDefault
}

// continuityFor returns the carry-over for structureID if one exists and is still
// within the continuity window at now; nil otherwise (no recent conversation, or it
// is old enough that a re-formation is a fresh conversation). World-goroutine only.
func (w *World) continuityFor(structureID StructureID, now time.Time) *conversationCarryover {
	cb := w.carryoverByStructure[structureID]
	if cb == nil {
		return nil
	}
	if now.Sub(cb.concludedAt) > effectiveHuddleContinuityWindow(w.Settings) {
		return nil
	}
	return cb
}

// joinContinuesClique reports whether this join is a same-clique re-formation that
// must NOT count as progress: the joiner was a speaker in the recent (within-window)
// conversation here AND every current member is too — the huddle is still the same
// clique reconvening. Once a GENUINELY-NEW participant has joined, the huddle has
// diverged from the old clique, so a later join even by a former member IS a
// composition change (progress) — otherwise a returning member sliding into an
// already-mixed conversation would wrongly carry the old loop baseline forward.
//
// MUST run AFTER the joiner is added to huddle.Members — the joiner is included in
// the all-members check (and trivially passes, since the joiner-is-a-member guard
// above already confirmed it is in the clique).
func (w *World) joinContinuesClique(huddle *Huddle, structureID StructureID, actorID ActorID, now time.Time) bool {
	cb := w.continuityFor(structureID, now)
	if cb == nil {
		return false
	}
	if _, ok := cb.members[actorID]; !ok {
		return false
	}
	for memberID := range huddle.Members {
		if _, ok := cb.members[memberID]; !ok {
			return false
		}
	}
	return true
}

// writeConversationCarryover snapshots a concluding structure huddle's conversation
// into the per-structure carry-over so the next huddle to form there can adopt it.
// Called from concludeHuddleInner BEFORE Members is cleared. No-op for outdoor /
// structureless huddles (their area scene is 1:1 with the huddle and is torn down,
// so there is nothing to continue). World-goroutine only.
func writeConversationCarryover(w *World, h *Huddle, now time.Time) {
	if h == nil || h.StructureID == "" {
		return
	}
	if w.carryoverByStructure == nil {
		w.carryoverByStructure = make(map[StructureID]*conversationCarryover)
	}
	if len(h.RecentUtterances) == 0 {
		// Nothing was said — no conversation to continue. Drop any stale carry-over
		// so a silent re-form doesn't inherit an unrelated older exchange.
		delete(w.carryoverByStructure, h.StructureID)
		return
	}
	members := make(map[ActorID]struct{})
	for _, u := range h.RecentUtterances {
		members[u.SpeakerID] = struct{}{}
	}
	var loopingSince *time.Time
	if h.LoopingSince != nil {
		t := *h.LoopingSince
		loopingSince = &t
	}
	w.carryoverByStructure[h.StructureID] = &conversationCarryover{
		utterances:         append([]Utterance(nil), h.RecentUtterances...),
		members:            members,
		loopingSince:       loopingSince,
		loopingReason:      h.LoopingReason,
		lastProgressAt:     h.LastProgressAt,
		concludedAt:        now,
		turnsSinceProgress: h.TurnsSinceProgress,
	}
}

// seedHuddleFromContinuity copies the recent conversation at this structure onto a
// freshly-formed huddle, when the speaker re-forming it (creatorID) was part of that
// conversation and it concluded within the continuity window. Carries the ring (so
// the reconvening peers see they were just talking — no re-greeting) and the loop
// spell + progress baseline (so the loop sweep's persistence gate and the LLM-169
// steer span the churn instead of resetting every cycle). A genuinely new arrival is
// not a continuity member, so the conversation starts fresh for them. World-
// goroutine only; called from JoinHuddle's new-huddle branch.
func seedHuddleFromContinuity(w *World, huddle *Huddle, structureID StructureID, creatorID ActorID, now time.Time) {
	cb := w.continuityFor(structureID, now)
	if cb == nil {
		return
	}
	if _, ok := cb.members[creatorID]; !ok {
		return
	}
	huddle.RecentUtterances = append([]Utterance(nil), cb.utterances...)
	if cb.loopingSince != nil {
		t := *cb.loopingSince
		huddle.LoopingSince = &t
		huddle.LoopingReason = cb.loopingReason
	}
	huddle.LastProgressAt = cb.lastProgressAt
	huddle.TurnsSinceProgress = cb.turnsSinceProgress
}
