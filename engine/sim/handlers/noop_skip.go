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
	return true
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
