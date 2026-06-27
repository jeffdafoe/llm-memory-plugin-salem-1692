package sim

import "time"

// HuddleID identifies one co-located conversational pocket.
type HuddleID string

// Huddle is the set of actors who are conversationally co-present at one
// structure. A huddle is the persistent co-location pocket — independent
// of any single Scene (one huddle may be observed across many scenes over
// its lifetime; one scene may observe many huddles).
//
// Single-goroutine ownership of huddle state means at most one active
// huddle per structure by construction — the legacy parallel-huddle
// consolidator (ZBBS-WORK-232) has no analog here because the race that
// motivated it cannot occur.
//
// Membership is canonical on Members; Actor.CurrentHuddleID is a
// denormalized back-reference kept in sync atomically by JoinHuddle /
// LeaveHuddle / ConcludeHuddle. The invariant
// "Huddle.Members[a] iff Actor[a].CurrentHuddleID == h.ID" holds across
// every command-handler return.
type Huddle struct {
	ID          HuddleID
	Members     map[ActorID]struct{}
	StructureID StructureID
	StartedAt   time.Time
	ConcludedAt *time.Time

	// LastActivityAt is the wall-clock time of the last conversational
	// activity in this huddle — a spoken line, a member joining, or a
	// completed transaction. The silence sweep (RunHuddleSilenceSweep,
	// ZBBS-HOME-417) concludes a huddle once now-LastActivityAt exceeds
	// HuddleSilenceTimeout, which is the ONLY routine conclusion path at a
	// staffed structure: leaveCurrentHuddle's last-member-leave path never
	// fires while the keeper stands present, so without this a structure's
	// huddle accreted every exchange for days. Zero-valued is treated as
	// StartedAt by the sweep, so a creation site that forgets to stamp still
	// gets a sane dormancy baseline. In-memory only: NOT checkpointed (no
	// column) and reset on restart by design — the boot-clear drops every
	// huddle anyway, so a conversation never spans a restart. Same
	// transient-state posture as RecentUtterances.
	LastActivityAt time.Time

	// RecentUtterances is a transient, capped ring of the last few spoken lines
	// in this huddle — the cross-tick "## Recent conversation here" perception
	// source (ZBBS-HOME-412). Populated by SpeakTo for EVERY speaker, NPC and PC
	// alike (both flow through SpeakTo in v2), so a re-engaging NPC sees that it
	// already spoke and what the player said on earlier ticks — the cross-tick
	// half of the re-pitch fix that the within-tick HOME-411 swap can't reach.
	// In-memory only: NOT persisted (no checkpoint column) and lost on restart by
	// design — a conversation is ephemeral, and restart-loss of chatter is fine
	// per shared/GUIDELINES (transient state stays in-process; Postgres is for
	// durable data). Oldest-first; trimmed to MaxRecentUtterancesPerHuddle.
	RecentUtterances []Utterance

	// LastProgressAt is the wall-clock time of the last NON-conversational
	// progress in this huddle — a completed transaction (coin pay, pay-with-
	// item, labor accept) or a membership change (join/leave). The loop sweep
	// (RunHuddleLoopSweep, LLM-159) treats a huddle whose progress is newer than
	// the current repetition spell as productive even when its speech looks
	// repetitive, so a busy vendor huddle or a closing negotiation is never
	// concluded as a livelock. Distinct from LastActivityAt, which a plain
	// spoken line also bumps — speech is exactly what a livelock is made of, so
	// it cannot itself be the progress signal. Zero until the first such event.
	// In-memory only, not checkpointed — same transient posture as LastActivityAt.
	LastProgressAt time.Time

	// LoopingSince marks when the loop sweep (LLM-159) first observed this huddle
	// in a sustained high-repetition, progress-free conversation — the onset of a
	// candidate conversational livelock. nil whenever the huddle is not currently
	// looping; a turn that breaks the repetition, a progress event, or the
	// conversation going quiet clears it. The sweep concludes the huddle once the
	// spell has persisted HuddleLoopTimeout. In-memory only, not checkpointed.
	LoopingSince *time.Time
}

// Utterance is one spoken line recorded in a Huddle's RecentUtterances ring.
// Speech only — pay/deliver/order events are not conversation and are not
// recorded here. SpeakerName is denormalized at write time so the perception
// render needs no actor lookup.
type Utterance struct {
	SpeakerID   ActorID
	SpeakerName string
	Text        string
	At          time.Time
}

// MaxRecentUtterancesPerHuddle caps the recent-conversation ring. Small on
// purpose: this is per-tick decision context ("have I already said this / what
// was just asked"), not history — the last several turns are what a re-engaging
// NPC needs. The consolidation cascade carries anything durable.
const MaxRecentUtterancesPerHuddle = 8

// CloneHuddle returns a deep copy suitable for publication via Snapshot or
// for serialization-equivalent boundaries (mem repo Seed / LoadAll /
// SaveSnapshot). World goroutine mutations to the live Huddle (Members
// add/remove, ConcludedAt rebind) won't leak into the returned copy.
func CloneHuddle(h *Huddle) *Huddle {
	if h == nil {
		return nil
	}
	cp := *h
	if h.Members != nil {
		cp.Members = make(map[ActorID]struct{}, len(h.Members))
		for k := range h.Members {
			cp.Members[k] = struct{}{}
		}
	}
	if h.ConcludedAt != nil {
		t := *h.ConcludedAt
		cp.ConcludedAt = &t
	}
	if h.LoopingSince != nil {
		t := *h.LoopingSince
		cp.LoopingSince = &t
	}
	// Utterance is a pure value type (no pointers/maps), so a slice copy fully
	// isolates the published snapshot from later world-goroutine appends.
	if len(h.RecentUtterances) > 0 {
		cp.RecentUtterances = append([]Utterance(nil), h.RecentUtterances...)
	}
	return &cp
}

// AppendUtterance records a spoken line in the huddle's recent-conversation
// ring, trimming the oldest when over MaxRecentUtterancesPerHuddle. Oldest-first
// so a reader sees the turns in order. No-ops on a nil huddle or empty text (the
// speak command already rejects empty utterances; this is defensive).
func (h *Huddle) AppendUtterance(speakerID ActorID, speakerName, text string, at time.Time) {
	if h == nil || text == "" {
		return
	}
	h.RecentUtterances = append(h.RecentUtterances, Utterance{
		SpeakerID:   speakerID,
		SpeakerName: speakerName,
		Text:        text,
		At:          at,
	})
	if len(h.RecentUtterances) > MaxRecentUtterancesPerHuddle {
		// Re-home into a fresh slice so the dropped head isn't pinned by the
		// backing array across the huddle's lifetime.
		trimmed := make([]Utterance, MaxRecentUtterancesPerHuddle)
		copy(trimmed, h.RecentUtterances[len(h.RecentUtterances)-MaxRecentUtterancesPerHuddle:])
		h.RecentUtterances = trimmed
	}
}
