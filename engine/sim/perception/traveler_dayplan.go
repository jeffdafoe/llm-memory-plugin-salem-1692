package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// traveler_dayplan.go — the salem-visitor traveler's day-plan cues (LLM-373).
// The engine walks the traveler its circuit and, of an evening, to the tavern
// (engine/sim/visitor.go dispatchVisitorCircuit); these render surfaces give the
// shared salem-visitor VA the SITUATION at each stage so it trades and books with
// purpose rather than idling. Two turn-fresh sections here:
//
//   - "## On your rounds"     — the daytime business circuit: frame the visit as a
//                               peddler's round so the VA greets + trades + passes
//                               news instead of standing mute.
//   - "## A bed for the night" — the evening booking: a homeless traveler at the inn
//                               is told to buy a night's lodging with pay_with_item,
//                               the same buyer-initiated flow a PC uses.
//
// The evening-leisure "the tavern's open of an evening" cue is ALSO extended to the
// traveler (buildVisitorEveningLeisure below, dispatched from buildEveningLeisure) —
// the social-hours pull that draws it to the tavern with the rest of the village.

// TravelerRoundsView is the content-gated "## On your rounds" section — presence is
// the whole signal (a nil view renders nothing). It is built only when the traveler
// is on its daytime circuit AND co-present with the keeper of the shop it is in, so
// the cue never fires in an empty room.
type TravelerRoundsView struct{}

// buildTravelerRounds returns the rounds view when the subject is a traveler on its
// daytime circuit standing inside a business with that business's keeper co-present,
// or nil otherwise. Pure over the snapshot.
func buildTravelerRounds(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, members []HuddleMember) *TravelerRoundsView {
	if snap == nil || actorSnap == nil || actorSnap.VisitorState == nil {
		return nil
	}
	switch actorSnap.VisitorState.Phase {
	case sim.VisitorPhaseArriving, sim.VisitorPhaseMakingRounds, sim.VisitorPhasePresent:
		// on the daytime circuit
	default:
		return nil
	}
	sid := actorSnap.InsideStructureID
	if sid == "" {
		return nil // not inside a shop — nothing to frame yet
	}
	for _, m := range members {
		ks := snap.Actors[m.ID]
		if ks != nil && ks.BusinessownerState != nil && ks.WorkStructureID == sid {
			return &TravelerRoundsView{}
		}
	}
	return nil
}

// renderTravelerRounds writes the "## On your rounds" framing. Content-gated: a nil
// view writes nothing. Deliberately does not re-name the shop or keeper — "## Around
// you" already places the traveler and names who is present; this adds only the
// purpose (why it is here, what to do), keeping the register a scene, not a stat.
func renderTravelerRounds(b *strings.Builder, v *TravelerRoundsView) {
	if v == nil {
		return
	}
	b.WriteString("## On your rounds\n")
	b.WriteString("You are making your rounds of the village — calling shop to shop, trading and passing the news you carry from the road. Greet whoever keeps this place, share word from your travels, and show what is in your pack if talk turns to trade.\n\n")
}

// TravelerSeekBedView is the content-gated "## A bed for the night" section: the
// evening booking cue for a homeless traveler co-present with an innkeeper. It names
// the innkeeper (for the pay_with_item seller arg, which resolves by display name)
// and the inn. nil skips the section.
type TravelerSeekBedView struct {
	// KeeperName is the innkeeper's display name — used verbatim as the
	// pay_with_item seller argument (findHuddlePeerByDisplayName resolves it) and in
	// the prose, mirroring how the co-present buy cue names its seller.
	KeeperName string
	// InnLabel is the inn's display name for the prose ("Hannah's Inn").
	InnLabel string
}

// buildTravelerSeekBed returns the seek-a-bed view when the subject is a homeless
// traveler (no home, no active room grant), it is the civil evening, and the keeper
// of a lodging structure is co-present in its huddle — the moment a booking is both
// wanted and actionable (pay_with_item needs a co-present seller). nil otherwise.
// Pure over the snapshot.
func buildTravelerSeekBed(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, members []HuddleMember) *TravelerSeekBedView {
	if snap == nil || actorSnap == nil || actorSnap.VisitorState == nil {
		return nil
	}
	if !actorSnapIsLodgingSeeker(actorSnap, snap.PublishedAt) {
		return nil // has a home or already holds a room — not seeking a bed
	}
	if !visitorCivilEvening(snap, actorSnap) {
		return nil // still daytime — the rounds cue owns the day
	}
	for _, m := range members {
		ks := snap.Actors[m.ID]
		if ks == nil || ks.BusinessownerState == nil || ks.WorkStructureID == "" {
			continue
		}
		if !structureSnapIsLodging(snap, ks.WorkStructureID) {
			continue
		}
		inn := "the inn"
		if st := snap.Structures[ks.WorkStructureID]; st != nil {
			inn = innLabel(st)
		}
		return &TravelerSeekBedView{KeeperName: m.DisplayName, InnLabel: inn}
	}
	return nil
}

// renderTravelerSeekBed writes the "## A bed for the night" booking cue: it names the
// innkeeper and inn and spells out the pay_with_item call for a nights_stay, with the
// barter-first payment (a ware from the pack) named ahead of coins (LLM-353). The
// keeper's side is already cued to accept (renderPayOffers). Content-gated.
func renderTravelerSeekBed(b *strings.Builder, v *TravelerSeekBedView) {
	if v == nil {
		return
	}
	keeper := sanitizeInline(v.KeeperName)
	inn := sanitizeInline(v.InnLabel)
	b.WriteString("## A bed for the night\n")
	fmt.Fprintf(b, "The light is going and the day's trade is behind you. You have no bed of your own in this town, but %s lets rooms here at %s. Offer them something from your pack for a night's lodging — a ware, or a few coins. Call pay_with_item with seller \"%s\", item \"nights_stay\", qty 1, consume_now false, and your payment: a ware you carry in pay_items, coins in amount, or both, with your words in say. They will take your offer or name their price.\n\n",
		keeper, inn, keeper)
}

// buildVisitorEveningLeisure is the transient-traveler arm of the evening-leisure
// cue (LLM-373), dispatched from buildEveningLeisure. A homeless traveler has no
// night-place of its own, so the resident subjectNightPlace / inEveningLeisure gates
// exclude it — but of an evening it should be drawn to the tavern like anyone, for
// company and to seek its bed. This returns the same EveningLeisureView the resident
// path does, with the tavern as the venue and NO home destination (HomeID ""), so
// renderEveningLeisure takes its no-home branch. Scoped to a traveler still seeking a
// bed — once booked it is a lodger and the standard lodger cues take over. Pure over
// the snapshot.
func buildVisitorEveningLeisure(snap *sim.Snapshot, a *sim.ActorSnapshot) *EveningLeisureView {
	if !visitorCivilEvening(snap, a) {
		return nil
	}
	if !actorSnapIsLodgingSeeker(a, snap.PublishedAt) {
		return nil // booked a room already — the lodger evening path covers it
	}
	venueID, venueLabel, ok := nearestTaggedVenue(snap, a, sim.VisitorTagTavern)
	if !ok {
		return nil // no tavern placed — nothing to steer to
	}
	// Settled tier is gated on being inside the SELECTED venue, not just any leisure
	// venue — with two taverns, insideLeisureVenue would mislabel the traveler as
	// settled at the nearest one while standing in the other (the LLM-345 precision).
	if a.InsideStructureID == venueID {
		if leavingLeisureVenue(a) {
			return nil // already walking out — don't argue at its back
		}
		return &EveningLeisureView{SettledIn: true, VenueLabel: venueLabel}
	}
	if a.MoveDestKind == sim.MoveDestinationStructureEnter && a.MoveDestStructureID == venueID {
		return nil // already walking in — the invitation was acted on
	}
	return &EveningLeisureView{VenueID: venueID, VenueLabel: venueLabel}
}

// visitorCivilEvening reports whether now is in the village's civil evening
// [dusk, lodger bedtime) — the window in which a traveler is drawn to the tavern and
// seeks its bed. Requires a usable dawn/dusk clock; false otherwise (the cues degrade
// to silence rather than firing on a bad clock). Mirrors the lodger night-window
// posture in lodging.go.
func visitorCivilEvening(snap *sim.Snapshot, a *sim.ActorSnapshot) bool {
	if snap == nil || a == nil || snap.LocalMinuteOfDay == nil || !snap.DawnDuskMinuteOK {
		return false
	}
	return minuteInWindow(snap.DuskMinute, snap.LodgingBedtimeMinute, *snap.LocalMinuteOfDay)
}

// structureSnapIsLodging reports whether a structure is the village inn (its backing
// VillageObject carries the "lodging" tag) over the published snapshot — the
// snapshot-side twin of sim.structureIsLodging.
func structureSnapIsLodging(snap *sim.Snapshot, sid sim.StructureID) bool {
	if snap == nil {
		return false
	}
	vobj := snap.VillageObjects[sim.VillageObjectID(sid)]
	return vobj != nil && vobj.HasTag("lodging")
}
