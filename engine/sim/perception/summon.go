package perception

import (
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// summon.go — the two summon-errand perception sections (ZBBS-HOME-311,
// reworked LLM-414), content-gated the same way recovery_options is: a nil
// view means render writes nothing. Both read transient cues mirrored onto
// the ActorSnapshot (PendingSummon / SummonRefusal).
//
//   - Target side ("## You have been summoned"): a messenger delivered a
//     summons asking the actor to come to a place — drives them to move_to.
//     While it stands (summonsActive) it is the SINGLE actionable movement
//     voice: buildDutySteer, buildEveningLeisure, and the walk-away errand
//     cues all yield to it, and shouldSkipNoop holds the gate open on it.
//     It persists through the target's speech and walk (the errand clears it
//     on the meeting / take_break / TTL — see sim handleSummonResponseFade).
//   - Summoner side ("## Your messenger returned"): the messenger came back
//     unable to find the target; fades on the summoner's next act.

// summonCueRenderTTL is the read-time age cap on the target-side cue —
// defense in depth behind the errand machine's own clears (the errand TTL is
// 10 minutes and its terminal paths drop the cue; this only matters if a
// stamped cue somehow outlives its errand's cleanup). Mirrors the
// businessRememberedShut read-time decay posture: stale state fades on read,
// no sweep needed.
const summonCueRenderTTL = 15 * time.Minute

// summonsActive reports whether the actor carries a live target-side summons
// — the single predicate behind the section, the steer suppressions, and the
// Build-level errand-cue nils, so they cannot drift. Age is measured against
// the snapshot's own publish instant (guarded against clock skew the same
// way businessRememberedShut is).
func summonsActive(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot) bool {
	if snap == nil || actorSnap == nil || actorSnap.PendingSummon == nil {
		return false
	}
	age := snap.PublishedAt.Sub(actorSnap.PendingSummon.At)
	if age < 0 {
		age = 0
	}
	return age <= summonCueRenderTTL
}

// SummonsForYouView is the content-gated target-side section. nil means the
// actor has no pending summons (render omits the section).
type SummonsForYouView struct {
	SummonerName string
	Place        string
	// PlaceStructureID is the meet place's structure id — the move_to token
	// the rendered line carries inline, duty-steer style. Empty when the
	// delivery could not resolve one (the render then names the label alone).
	PlaceStructureID sim.StructureID
	Reason           string // "" when the summoner gave none
}

// SummonRefusalView is the content-gated summoner-side section. nil means
// the actor's last summon did not hit the refusal branch (render omits it).
type SummonRefusalView struct {
	TargetName string
}

// buildSummonsForYou lifts the target-side cue off the snapshot, or nil when
// absent or aged out. Pure over the snapshot (no live-world reads).
func buildSummonsForYou(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot) *SummonsForYouView {
	if !summonsActive(snap, actorSnap) {
		return nil
	}
	p := actorSnap.PendingSummon
	return &SummonsForYouView{
		SummonerName:     p.SummonerName,
		Place:            p.Place,
		PlaceStructureID: p.PlaceStructureID,
		Reason:           p.Reason,
	}
}

// buildSummonRefusal lifts the summoner-side cue off the snapshot, or nil
// when absent.
func buildSummonRefusal(actorSnap *sim.ActorSnapshot) *SummonRefusalView {
	if actorSnap == nil || actorSnap.SummonRefusal == nil {
		return nil
	}
	return &SummonRefusalView{TargetName: actorSnap.SummonRefusal.TargetName}
}

// renderSummonsForYou writes the "## You have been summoned" section. The
// meet place's structure_id rides inline, duty-steer style, so the move_to is
// self-sufficient from anywhere in the village (a bare label from across the
// map may not resolve by name). Content-gated: nil view writes nothing.
func renderSummonsForYou(b *strings.Builder, v *SummonsForYouView) {
	if v == nil {
		return
	}
	summoner := v.SummonerName
	if summoner == "" {
		summoner = "someone"
	}
	place := v.Place
	if place == "" {
		place = "the summoning place"
	}
	b.WriteString("## You have been summoned\n")
	b.WriteString("A messenger has brought word: ")
	b.WriteString(sanitizeInline(summoner))
	b.WriteString(" asks you to come to ")
	b.WriteString(sanitizeInline(place))
	if v.PlaceStructureID != "" {
		b.WriteString(" (structure_id: ")
		b.WriteString(sanitizeInline(string(v.PlaceStructureID)))
		b.WriteString(")")
	}
	b.WriteString(".")
	if v.Reason != "" {
		b.WriteString(" ")
		b.WriteString(sanitizeInline(v.Reason))
	}
	b.WriteString("\nThey await you there now. Walk there (move_to) to answer the summons, or stay and let it go unanswered.\n\n")
}

// renderSummonRefusal writes the "## Your messenger returned" section.
// Content-gated: nil view writes nothing.
func renderSummonRefusal(b *strings.Builder, v *SummonRefusalView) {
	if v == nil {
		return
	}
	target := v.TargetName
	if target == "" {
		target = "the person you sent for"
	}
	b.WriteString("## Your messenger returned\n")
	b.WriteString(sanitizeInline(target))
	b.WriteString(" could not be found.\n\n")
}
