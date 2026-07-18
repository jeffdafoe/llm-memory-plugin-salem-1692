package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// turn_in.go — LLM-447. The voluntary bed-down affordance: the cue + tool-gate
// signal for an agent NPC that could end its own evening.
//
// The presence of TurnInChoiceView is the SINGLE gate both the turn_in tool
// (handlers/tool_gating.go) and the cue (renderTurnInChoice) read, so they cannot
// drift (tool-cue lockstep, discussion-109). It mirrors sim.npcMayTurnIn — the
// auto-bed's residency/off-shift predicate with the night window widened to open
// at dusk — so the affordance appears exactly when the substrate would accept it.

// TurnInChoiceView is the evening bed-down affordance. nil when turning in isn't
// on the table (not where the actor sleeps, not yet evening, on shift, asleep, or
// mid-activity).
type TurnInChoiceView struct {
	// PlaceName is the display name of the place the actor would bed down in —
	// its own home, or the inn where it rents a room. Names the bed so the scene
	// is concrete rather than an abstract "go to sleep".
	PlaceName string
	// Lodging is true when the actor is a lodger at a rented room rather than
	// home, so the line can say "your room at the Ordinary" rather than "your
	// own bed".
	Lodging bool
	// HasCompany is true when a co-present huddle companion is there to bid
	// goodnight to. It shapes the line only — a lone actor may still turn in,
	// it just has no one to say it to.
	HasCompany bool
}

// buildTurnInChoice returns the evening bed-down affordance, or nil.
//
// Conditions mirror sim.npcMayTurnIn: an awake agent NPC, not mid-activity,
// settled either inside its own home or inside the inn where it holds an active
// lodging grant, NOT on a scheduled shift, and the village clock at or past dusk
// (the night window [dusk, dawn), widened from the auto-bed's civil bedtime hour
// — that widening is the feature: the evening finally has an exit).
//
// Deliberately NOT tiredness-gated: the Walkers who produced the live loop were
// at tiredness 12, below even the awareness floor, so their prompts carried no
// tiredness line at all. Bedtime here is clock-and-social, not a meter read.
// Pure over the snapshot.
func buildTurnInChoice(snap *sim.Snapshot, a *sim.ActorSnapshot, members []HuddleMember) *TurnInChoiceView {
	if snap == nil || a == nil || snap.LocalMinuteOfDay == nil {
		return nil
	}
	if a.Kind != sim.KindNPCStateful && a.Kind != sim.KindNPCShared {
		return nil // PCs have their own pc_sleep_* surface; decoratives have none
	}
	if a.State == sim.StateSleeping {
		return nil // already abed
	}
	if actorMidSourceActivity(a) {
		return nil // mid-bake / mid-harvest — finish it first, same as the substrate
	}
	if !snap.DawnDuskMinuteOK {
		return nil // no usable dawn/dusk boundary — can't bound the night window
	}
	if a.InsideStructureID == "" {
		return nil // out in the open — no bed here
	}
	// The night window [dusk, dawn), wrap-aware for the crossing past midnight.
	if !minuteInWindow(snap.DuskMinute, snap.DawnMinute, *snap.LocalMinuteOfDay) {
		return nil // still daytime — the evening hasn't come
	}
	if subjectOnShift(snap, a) {
		return nil
	}
	hasCompany := len(members) > 0
	// Home arm first — a homed NPC inside its home beds there, whatever else it holds.
	if a.HomeStructureID != "" && a.InsideStructureID == a.HomeStructureID {
		label, ok := resolveStructureLabel(snap, a.HomeStructureID)
		if !ok {
			return nil // home must resolve in the snapshot
		}
		return &TurnInChoiceView{PlaceName: label, HasCompany: hasCompany}
	}
	// Lodger arm — inside the inn it holds an active grant on (mirrors
	// buildLodgingView's selection: the soonest-expiring active grant).
	now := snap.PublishedAt
	var best *sim.RoomAccess
	for _, ra := range a.RoomAccess {
		if !sim.IsActiveLedgerGrant(ra, now) {
			continue
		}
		if best == nil || ra.ExpiresAt.Before(*best.ExpiresAt) {
			best = ra
		}
	}
	if best == nil {
		return nil // neither home nor a lodger — no bed here
	}
	s := structureForRoom(snap, best.RoomID)
	if s == nil || a.InsideStructureID != s.ID {
		return nil // its room is elsewhere
	}
	return &TurnInChoiceView{PlaceName: innLabel(s), Lodging: true, HasCompany: hasCompany}
}

// subjectOnShift is the snapshot-side mirror of sim.actorOnShift — the shift
// notion the turn_in substrate gate uses — so the cue and the tool agree on who
// is at work. Both arms matter:
//
//   - A SCHEDULED actor uses its own half-open [start, end) window, wrap-aware
//     (sim.isActorOnShift). This is load-bearing for a night-shift home==work
//     keeper (a tavernkeeper 16:00–03:00) standing inside her own home at 22:00,
//     and for a lodger whose shift straddles the evening.
//   - An UNSCHEDULED WORKER falls back to the world's dawn/dusk day window
//     (sim.effectiveShiftWindow), so it is day-active rather than always-off.
//
// The second arm is currently redundant for turn_in — the day window [dawn, dusk)
// is the exact complement of the turn_in window [dusk, dawn), so an unscheduled
// worker inside one is never inside the other. It is written out anyway rather
// than leaned on: that complement is a property of today's two settings, not an
// invariant anyone declared, and a cue that silently depends on it would break
// quietly if the windows were ever decoupled.
func subjectOnShift(snap *sim.Snapshot, a *sim.ActorSnapshot) bool {
	if snap.LocalMinuteOfDay == nil {
		return false
	}
	now := *snap.LocalMinuteOfDay
	inWindow := func(start, end int) bool {
		if start <= end {
			return now >= start && now < end
		}
		return now >= start || now < end
	}
	if a.ScheduleStartMin != nil && a.ScheduleEndMin != nil {
		return inWindow(*a.ScheduleStartMin, *a.ScheduleEndMin)
	}
	if subjectIsWorker(a) && snap.DawnDuskMinuteOK {
		return inWindow(snap.DawnMinute, snap.DuskMinute)
	}
	return false
}

// renderTurnInChoice writes the evening bed-down affordance as a scene (LLM-447).
//
// Register per scenes-not-stats: the line is soft and diegetic — the evening
// winding down, the fire burning low — and never an imperative. It offers the
// end of the day; the model decides whether the day is over. The goodnight is
// named as riding turn_in's own say rather than a separate speak call, because
// both verbs are terminal: a cue that asked for both could never be obeyed.
func renderTurnInChoice(b *strings.Builder, v *TurnInChoiceView) {
	if v == nil {
		return
	}
	bed := "your own bed"
	if v.Lodging {
		bed = fmt.Sprintf("your room at %s", sanitizeInline(v.PlaceName))
	}
	b.WriteString("## The evening draws in\n")
	if v.HasCompany {
		fmt.Fprintf(b, "The hour has grown late and the day's work is behind you. When you have had your fill of the company, you can call turn_in to say your goodnight and take yourself off to %s — put the parting word in its say, and it will be the last thing you speak before you sleep.\n\n", bed)
		return
	}
	fmt.Fprintf(b, "The hour has grown late and the day's work is behind you. The house is quiet. When you are ready, you can call turn_in and take yourself off to %s for the night.\n\n", bed)
}
