package handlers

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// shouldSkipNoop reports whether the harness should short-circuit the
// LLM call as a noop. Returns true when ALL of these hold:
//
//  1. No Force warrant in the consumed batch. Force always ticks — it's
//     the same exemption the evaluator gives Force at the pacing /
//     rate-gate / min-tick-gap level (an admin override or emergency
//     reason must fire regardless).
//
//  2. No co-present peer in the actor's huddle. A peer in
//     SurroundingsView.HuddleMembers is the cheapest "someone is here to
//     talk to" signal. Cascade origins (PC speak, NPC arrival, peer-
//     joined) naturally pass via this check — the speaker / arriver is
//     in the huddle by the time perception runs.
//
//  3. No actor need at or past its red-tier threshold. Mirrors v1's
//     `needLabelTier(value, threshold) >= 1` check at agent_tick.go:
//     213-215. Thresholds ride on the snapshot (snap.NeedThresholds) so
//     the gate is race-free against admin tuning.
//
//  4. Every warrant in the consumed batch is a low-information kind —
//     i.e. carries no fresh stimulus on its own. The set:
//
//     - WarrantKindIdleBackstop:   engine-injected liveness; the whole
//     point of the gate is that "give them
//     a chance to act" is empty when
//     perception is empty.
//     - WarrantKindHuddleConcluded: the actor's conversation just
//     ended; there's no peer left to
//     respond to (also caught by 2).
//     - WarrantKindHuddleLeft:      the actor's own past departure; no
//     external stimulus.
//     - WarrantKindHuddlePeerLeft:  a peer left the actor's conversation.
//     When a peer still remains, check 2 keeps the gate open so the
//     actor can react to the changed group; when none remain (the
//     alone case) there's nothing to respond to — same shape as
//     HuddleConcluded. (ZBBS-WORK-367)
//
//     Any other warrant kind in the batch — speech, pay, arrival,
//     peer-joined, need-threshold, scene-quote, admin — counts as fresh
//     news worth one LLM call, and the gate steps aside.
//
//  5. No duty steer in the payload, AND no duty pending. A non-nil
//     DutySteer is a standing, actionable signal even with no peers and
//     sub-red needs — the actor is off-post inside its shift window
//     ("head to work") or away from home/lodging off-shift ("head
//     home"). The tick must run so the steer can be read: the only
//     warrants a solo idle NPC receives are idle-backstops, so eating
//     them here skip-locks the actor until a need crosses red — hours
//     (ZBBS-HOME-441: Josiah stood at his doorstep all morning; v2 has
//     no recurring shift warrant because HOME-352 made duty a perception
//     steer, which only renders if a tick actually runs).
//
//     DutyPending covers the band the steer alone missed (ZBBS-HOME-442):
//     Option B (HOME-400) suppresses the to-work steer for a MILD need,
//     so an off-post on-shift keeper with hunger in [mild, red) had nil
//     DutySteer and still skip-locked — the suppression assumed the tick
//     would run so the model could address the need first, but the gate
//     meant it never ran at all. DutyPending is the pre-suppression
//     "to-work duty applies" predicate; the cue stays suppressed, the
//     tick voices the mild need, the model addresses it, and once needs
//     drop below mild the steer renders and walks the actor to post.
//     The extra ticks are bounded to the off-post-on-shift case and
//     self-extinguish on arrival.
//
// Replaces v1's salem-vendor-only skip in engine/agent_tick.go (lines
// 211-221, ZBBS-WORK-235). v1 narrowed by agent slug because the
// "shared VA with cache_prompts=false / learning_enabled=false" pattern
// was inferred from one specific slug. v2 applies universally: the
// criterion captures "no information for the LLM to act on" regardless
// of kind. Stateful NPCs benefit too — a stateful tick on empty
// perception builds VA-memory noise, not signal.
//
// Caller (Harness.RunTick): runs this after perception.Build, before
// perception.Render — the rendered prompt is wasted work when the gate
// triggers. On a skip, returns TickStatusSkipped; CompleteReactorTick's
// terminal-status policy treats Skipped as "addressed" so the consumed
// warrants land in recently-consumed and don't re-fire next scan.
func shouldSkipNoop(payload perception.Payload, thresholds sim.NeedThresholds, warrants []sim.WarrantMeta) bool {
	if len(warrants) == 0 {
		// Empty batch means "no signal was the trigger" — the evaluator
		// shouldn't emit this (a tick is gated on WarrantedSince != nil,
		// which implies at least one stamped warrant), but if it ever does
		// the safer call is to let the LLM tick run rather than silently
		// suppress. Skipping a no-warrant tick would silently consume an
		// attempt that someone upstream thought worth emitting.
		return false
	}
	for _, m := range warrants {
		if m.Force {
			return false
		}
	}
	if len(payload.Surroundings.HuddleMembers) > 0 {
		return false
	}
	for key, value := range payload.Actor.Needs {
		if value >= thresholds.Get(key) {
			return false
		}
	}
	for _, m := range warrants {
		if !isLowInfoWarrantKind(m.Kind()) {
			return false
		}
	}
	// An AtPost steer (ZBBS-WORK-431) is render-only: it stabilizes an at-post
	// NPC's prompt on ticks that already run, but is NOT a reason to force a tick
	// — an idle owner standing at its post with no stimulus should still skip its
	// idle-backstops (HOME-441), or this gate would re-open and the constant
	// deliberation it caused is exactly what made the actor wander. The to-work /
	// wind-down arms remain standing actionable signals that hold the gate open.
	//
	// EveningLeisure (LLM-149) holds the gate open in the off-shift go-home
	// steer's place: on the evening window [shift-end, 22:00) buildDutySteer
	// suppresses the go-home steer so the evening cue is the single voice, but
	// that steer is exactly what kept an idle off-shift homed agent ticking
	// (HOME-441). Without this an evening agent with only idle-backstop warrants
	// would skip-lock and never see the invitation. It self-extinguishes when the
	// agent commits (the cue clears at the tavern or home).
	//
	// LLM-335: the BatchHold variant is EXEMPT — like the at-post steer above, it is
	// render-only, not a reason to force a tick. A keeper mid-batch at its post is
	// pinned there and has nothing to act on, so its idle-backstop no-op ticks should
	// still skip (that constant deliberation is the pester the ticket removes). The
	// batch's own landing wake is a high-info ProductionDone warrant, so it keeps the
	// gate open on its own the tick the batch finishes — this exemption can't strand it.
	if (payload.DutySteer != nil && !payload.DutySteer.AtPost) || payload.DutyPending ||
		(payload.EveningLeisure != nil && !payload.EveningLeisure.BatchHold) {
		return false
	}
	return true
}

// batchHasNewNews reports whether the consumed warrant batch carries any
// fresh stimulus — a Force warrant or any high-information kind. It is the
// turn-state gate's new-news signal (ZBBS-WORK-370): the speak backstop
// (sim.SpeakTo) exempts a tick that carries new news so a legitimate
// event-driven follow-up commits while only idle re-pitches are suppressed.
// The inverse of the noop-skip gate's low-info-only test: a batch that is NOT
// new news is exactly a batch of only low-info kinds with no Force. An empty
// batch carries no news (false) — the safe default (no exemption granted).
func batchHasNewNews(warrants []sim.WarrantMeta) bool {
	for _, m := range warrants {
		if m.Force || !isLowInfoWarrantKind(m.Kind()) {
			return true
		}
	}
	return false
}

// isLowInfoWarrantKind reports whether a warrant kind carries no fresh
// external stimulus on its own — so a batch consisting solely of these
// kinds, with no other perception signal, has nothing for the LLM to
// react to. The default branch is "high-info" (return false): adding a
// new warrant kind without classifying it here keeps the gate from
// over-suppressing the new kind by accident.
func isLowInfoWarrantKind(k sim.WarrantKind) bool {
	switch k {
	case sim.WarrantKindIdleBackstop,
		sim.WarrantKindHuddleConcluded,
		sim.WarrantKindHuddleLeft,
		sim.WarrantKindHuddlePeerLeft:
		return true
	default:
		return false
	}
}
