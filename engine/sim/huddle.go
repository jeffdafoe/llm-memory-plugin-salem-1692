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

	// LoopingReason latches WHICH arm stamped LoopingSince ("huddle_loop" /
	// "huddle_loop_ledger" / "huddle_loop_endurance") so the conclusion
	// telemetry reports the onset cause rather than re-diagnosing at conclude
	// time — the arms can drift apart over a spell (a lexically-armed ring can
	// churn into varied-but-over-budget lines) and the reason is used to
	// validate which detector caught an incident (LLM-333 code_review). Empty
	// whenever LoopingSince is nil; cleared and carried everywhere LoopingSince
	// is. In-memory only, not checkpointed.
	LoopingReason string

	// LastPCUtteranceAt is the wall-clock time a PLAYER (KindPC) member last spoke
	// in this huddle. The loop sweep + the per-tick ConversationLooping steer
	// (LLM-185) treat the huddle as player-attended while now-LastPCUtteranceAt is
	// within huddlePCAttentionWindow and skip concluding/steering it, so an active
	// player conversation is never broken. Gating on a PC's recent SPEECH rather
	// than mere PC membership is deliberate: a PC parked-and-silent at a hub (the
	// tavern) must NOT permanently exempt that huddle, or NPC loops there would
	// never be swept. Zero until a PC speaks. In-memory only, not checkpointed —
	// same transient posture as LastActivityAt.
	LastPCUtteranceAt time.Time

	// TurnsSinceProgress counts spoken lines since the huddle's last progress
	// event (a completed transaction, a genuine membership change) or a player
	// line — the loop sweep's endurance arm (LLM-333). The two 2026-07-08 live
	// loops proved the utterance arm's word-overlap metric cannot see paraphrase
	// repetition (the farewell loop measured 0.00 against the 0.60 threshold —
	// the model never words the same farewell twice), so this arm is content-
	// blind: a conversation where nothing has HAPPENED for HuddleLoopMaxTurns
	// spoken turns is stuck no matter how varied its wording. Incremented by
	// AppendUtterance; reset wherever LastProgressAt is stamped and on a PC
	// line (a player's words genuinely redirect a conversation, and the ring
	// cannot serve as the counter — it caps at 8, below any sane threshold).
	// In-memory only, not checkpointed — same transient posture as the ring;
	// carried across same-clique re-formation by the LLM-170 carry-over so
	// churn can't evade the arm.
	TurnsSinceProgress int

	// ConversationSince is when this CONVERSATION began — not when this huddle
	// object was minted. The two differ because a clique that disperses and
	// reconvenes at a structure mints a fresh Huddle each time (LLM-170), so
	// StartedAt restarts on every churn cycle while the talk continues
	// unbroken; the live 2026-07-14 inn conversation ran 100 minutes across ten
	// huddle ids. Stamped at creation and carried across same-clique
	// re-formation by the carry-over, so it measures the thing an observer would
	// call "how long have these people been talking" — the clock the loop
	// sweep's lingering arm (LLM-397) reads. A conversation that genuinely lapses
	// (no re-formation within HuddleContinuityWindow) leaves no carry-over to
	// adopt, so the next huddle there starts a fresh clock. In-memory only, not
	// checkpointed — same transient posture as the ring it travels with.
	ConversationSince time.Time
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

// HuddleLiveWindowDefault is the fallback for WorldSettings.HuddleLiveWindow —
// how recently a huddle must have seen activity to still read as a live
// conversation (LLM-467). 5 minutes is chosen against the two cadences that
// bracket it: the idle backstop fires every 30 minutes
// (defaultIdleBackstopThreshold), so any window shorter than that reliably
// classifies a backstop landing in a quiet room as dormant; and it sits well
// above DefaultNPCAwaitReplyWindow (60s), so a conversation whose participants
// are still trading turns at NPC speed never reads as dormant mid-exchange.
const HuddleLiveWindowDefault = 5 * time.Minute

// HuddleIsLive reports whether h has seen conversational activity — a spoken
// line, a member joining, a completed transaction — within `window` of `now`.
//
// This is a LIVENESS question, not a lifecycle one: a huddle stays open for
// HuddleSilenceTimeout (2h) after its last word so a returning patron resumes
// the same conversation, but for most of that span nobody is actually talking.
// The noop-skip preflight uses this to tell "someone is here to talk to" apart
// from "someone is standing here" (LLM-467) — the distinction that stopped a
// finished conversation from billing every member a full LLM call per backstop
// for the rest of the two hours.
//
// Baselines match RunHuddleSilenceSweep's, so the two agree on what a
// never-stamped huddle means: an unset LastActivityAt falls back to StartedAt,
// which covers a creation site that forgets to stamp. A huddle with NEITHER
// stamp (a hand-built test snapshot) reads live — the safe direction, since a
// false "live" only costs the pre-LLM-467 tick, while a false "dormant" would
// silently strand a real conversation. window <= 0 reads live for the same
// reason; a loaded world always resolves a positive window.
// EffectiveHuddleLiveWindow returns the configured huddle liveness window,
// falling back to HuddleLiveWindowDefault when unset/zero. Same lazy-default
// posture as effectiveHuddleSilenceTimeout; exported because the snapshot
// publish mirrors the resolved value for off-world readers (perception).
func EffectiveHuddleLiveWindow(s WorldSettings) time.Duration {
	if s.HuddleLiveWindow > 0 {
		return s.HuddleLiveWindow
	}
	return HuddleLiveWindowDefault
}

func HuddleIsLive(h *Huddle, now time.Time, window time.Duration) bool {
	if h == nil {
		return false
	}
	if window <= 0 {
		return true
	}
	last := h.LastActivityAt
	if last.IsZero() {
		last = h.StartedAt
	}
	if last.IsZero() {
		return true
	}
	return now.Sub(last) <= window
}

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
//
// Every recorded line also advances TurnsSinceProgress (the LLM-333 endurance
// counter) — including filler-only lines the repetition metric excludes: each
// spoken turn burned an LLM call, and spend-without-progress is exactly what
// the counter measures. A PC line increments here and is reset to zero at the
// speak site (the PC branch runs after this call).
func (h *Huddle) AppendUtterance(speakerID ActorID, speakerName, text string, at time.Time) {
	if h == nil || text == "" {
		return
	}
	h.TurnsSinceProgress++
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

// LastUtteranceAtBy returns the time of the most recent utterance by `id` in
// this huddle's recent-conversation ring, or the zero time if the ring holds
// none. The ring is oldest-first (AppendUtterance), so a forward scan keeping
// the last match yields the latest. Nil-safe. Consumed by the LLM-232 re-ask
// suppression — the sim.SpeakTo backstop (soleAwaitedPeerForReask) and the
// perception turn-state anchor (solePeerReaskAnchor) — to tell "I spoke and they
// went quiet" from "they answered": the subject's own last line newer than the
// peer's means the peer has said nothing since.
func (h *Huddle) LastUtteranceAtBy(id ActorID) time.Time {
	if h == nil {
		return time.Time{}
	}
	var last time.Time
	for _, u := range h.RecentUtterances {
		if u.SpeakerID == id {
			last = u.At
		}
	}
	return last
}
