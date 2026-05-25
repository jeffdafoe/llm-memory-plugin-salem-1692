package perception

import (
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// summon.go — the two summon-errand perception sections (ZBBS-HOME-311),
// content-gated the same way recovery_options is: a nil view means render
// writes nothing. Both read transient cues mirrored onto the ActorSnapshot
// (PendingSummon / SummonRefusal); the cue is set by the errand machine and
// fades after the actor next acts (cleared on its reactor tick), so the
// section appears for exactly the tick(s) between delivery and the actor's
// next action.
//
//   - Target side ("## You have been summoned"): a messenger delivered a
//     summons asking the actor to come to a place — drives them to move_to.
//   - Summoner side ("## Your messenger returned"): the messenger came back
//     unable to find the target.

// SummonsForYouView is the content-gated target-side section. nil means the
// actor has no pending summons (render omits the section).
type SummonsForYouView struct {
	SummonerName string
	Place        string
	Reason       string // "" when the summoner gave none
}

// SummonRefusalView is the content-gated summoner-side section. nil means
// the actor's last summon did not hit the refusal branch (render omits it).
type SummonRefusalView struct {
	TargetName string
}

// buildSummonsForYou lifts the target-side cue off the snapshot, or nil when
// absent. Pure over the snapshot (no live-world reads).
func buildSummonsForYou(actorSnap *sim.ActorSnapshot) *SummonsForYouView {
	if actorSnap == nil || actorSnap.PendingSummon == nil {
		return nil
	}
	p := actorSnap.PendingSummon
	return &SummonsForYouView{
		SummonerName: p.SummonerName,
		Place:        p.Place,
		Reason:       p.Reason,
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

// renderSummonsForYou writes the "## You have been summoned" section.
// Content-gated: nil view writes nothing.
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
	b.WriteString(sanitizeInline(summoner))
	b.WriteString(" asks you to come to ")
	b.WriteString(sanitizeInline(place))
	b.WriteString(".")
	if v.Reason != "" {
		b.WriteString(" ")
		b.WriteString(sanitizeInline(v.Reason))
	}
	b.WriteString("\nWalk there (move_to) if you choose to answer.\n\n")
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
