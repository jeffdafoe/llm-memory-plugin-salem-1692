package perception

import (
	"fmt"
	"sort"
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

// TravelerRoundsView is the situational "## Your rounds" surface (LLM-379). The engine
// no longer chooses the traveler's stops — it renders his situation here and he
// navigates himself with move_to. Content-gated: a nil view (off his daytime rounds)
// writes nothing.
type TravelerRoundsView struct {
	// AtKeeperShop names the keeper-business he stands in co-present with its keeper,
	// "" when he is between legs / out in the open. Drives the trade-here line and is
	// excluded from OpenShops.
	AtKeeperShop string
	// Visited is the display names of the keeper-businesses he has already called at
	// this stay — rendered back so a stateless shared VA "remembers" and does not
	// repeat a shop.
	Visited []string
	// OpenShops is the keeper-businesses still tending (snapshotKeeperPresent, the twin
	// of the arrival-recording gate so the list can't outrun what a visit records),
	// unvisited, not the inn, not the one he stands in — each with a bearing so he can
	// choose a next stop. Nearest first. NEVER a single "go here" imperative: the list,
	// and he picks with move_to.
	OpenShops []RoundsShop
	// MinutesToDusk drives the escalating nightfall pressure; only meaningful when
	// HasClock. HasClock is false on an unusable dawn/dusk clock (suppress the line).
	MinutesToDusk int
	HasClock      bool
}

// RoundsShop is one still-open shop on the traveler's rounds: its name and a bearing
// from where he stands.
type RoundsShop struct {
	Name      string
	Direction string // "north" … "" when he is on top of it
	Steps     int    // Chebyshev tiles, for a rough near/far sense
}

// buildTravelerRounds returns the rounds surface when the subject is a traveler on his
// daytime rounds (arriving / making_rounds / a legacy 'present' row); nil in the evening
// (the seek-a-bed cue owns it) or off-visitor. Pure over the snapshot.
func buildTravelerRounds(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, members []HuddleMember) *TravelerRoundsView {
	if snap == nil || actorSnap == nil || actorSnap.VisitorState == nil {
		return nil
	}
	switch actorSnap.VisitorState.Phase {
	case sim.VisitorPhaseArriving, sim.VisitorPhaseMakingRounds, sim.VisitorPhasePresent:
		// on his daytime rounds
	default:
		return nil
	}
	vs := actorSnap.VisitorState

	// Where he stands: if co-present with the keeper of the shop he is in, name it (the
	// trade-here line) and exclude it from the still-open list.
	var atShopID sim.StructureID
	atShop := ""
	if sid := actorSnap.InsideStructureID; sid != "" {
		for _, m := range members {
			ks := snap.Actors[m.ID]
			if ks != nil && ks.BusinessownerState != nil && ks.WorkStructureID == sid {
				atShopID = sid
				if st := snap.Structures[sid]; st != nil {
					atShop = st.DisplayName
				}
				break
			}
		}
	}

	// Rounds so far.
	visitedSet := make(map[sim.StructureID]bool, len(vs.VisitedBusinesses))
	var visited []string
	for _, sid := range vs.VisitedBusinesses {
		visitedSet[sid] = true
		if st := snap.Structures[sid]; st != nil {
			visited = append(visited, st.DisplayName)
		}
	}

	// Shops still open: keeper tending now (the twin of the recording gate), unvisited,
	// not the inn, not where he stands. The engine lists them; the model chooses.
	var open []RoundsShop
	for id, vobj := range snap.VillageObjects {
		stID := sim.StructureID(id)
		if vobj == nil || stID == atShopID || visitedSet[stID] {
			continue
		}
		st, ok := snap.Structures[stID]
		if !ok || st == nil {
			continue
		}
		if structureSnapIsLodging(snap, stID) || !snapshotKeeperPresent(snap, stID) {
			continue
		}
		tile := vobj.Pos.Tile()
		open = append(open, RoundsShop{
			Name:      st.DisplayName,
			Direction: cardinalDirection(float64(actorSnap.Pos.X), float64(actorSnap.Pos.Y), float64(tile.X), float64(tile.Y)),
			Steps:     actorSnap.Pos.Chebyshev(tile),
		})
	}
	sort.Slice(open, func(i, j int) bool {
		if open[i].Steps != open[j].Steps {
			return open[i].Steps < open[j].Steps
		}
		return open[i].Name < open[j].Name
	})

	view := &TravelerRoundsView{AtKeeperShop: atShop, Visited: visited, OpenShops: open}
	if snap.LocalMinuteOfDay != nil && snap.DawnDuskMinuteOK {
		view.HasClock = true
		view.MinutesToDusk = snap.DuskMinute - *snap.LocalMinuteOfDay
	}
	return view
}

// renderTravelerRounds writes the "## Your rounds" surface — a scene (what he's called
// at, what's still open and where, the failing light), not a stat pile. Content-gated.
// It never names a single "go here next" target: it lays out the situation and the
// model chooses its next move with move_to (LLM-379).
func renderTravelerRounds(b *strings.Builder, v *TravelerRoundsView) {
	if v == nil {
		return
	}
	b.WriteString("## Your rounds\n")
	if v.AtKeeperShop != "" {
		fmt.Fprintf(b, "You're inside %s just now — greet whoever keeps it, share word from the road, and show what's in your pack if talk turns to trade.\n", sanitizeInline(v.AtKeeperShop))
	}
	if len(v.Visited) == 0 {
		b.WriteString("You've not called anywhere yet this visit.\n")
	} else {
		fmt.Fprintf(b, "So far you've called at %s.\n", joinNames(v.Visited))
	}
	if len(v.OpenShops) > 0 {
		b.WriteString("Still trading at this hour: ")
		for i, s := range v.OpenShops {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(b, "%s, %s", sanitizeInline(s.Name), roundsDistPhrase(s.Steps, s.Direction))
		}
		b.WriteString(". Make for whichever you please.\n")
	} else {
		b.WriteString("Nothing else is open just now.\n")
	}
	if v.HasClock {
		b.WriteString(roundsNightfallLine(v.MinutesToDusk))
	}
	b.WriteString("\n")
}

// roundsDistPhrase renders a shop's bearing as a diegetic phrase — "just to the west",
// "a short way to the north", "off to the east" — or "right here" when the traveler
// stands on it.
func roundsDistPhrase(steps int, dir string) string {
	if dir == "" {
		return "right here"
	}
	switch {
	case steps <= 8:
		return "just to the " + dir
	case steps <= 20:
		return "a short way to the " + dir
	default:
		return "off to the " + dir
	}
}

// roundsNightfallLine is the escalating pressure toward seeking a bed, keyed on minutes
// to dusk. Its wording lets the model decide when to break off trading.
func roundsNightfallLine(minsToDusk int) string {
	switch {
	case minsToDusk <= 0:
		return "The light has all but gone — best see about a bed for the night before long.\n"
	case minsToDusk <= 60:
		return "The light is going fast now; you'll want to see about a bed before it's dark.\n"
	case minsToDusk <= 180:
		return "The afternoon is wearing on and the light is starting to lengthen.\n"
	default:
		return "There's plenty of daylight left for your trade.\n"
	}
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
