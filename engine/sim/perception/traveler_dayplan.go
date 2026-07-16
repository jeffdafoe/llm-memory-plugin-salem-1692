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
	if vs.DistributorOnly {
		// A wholesale factor (LLM-410) doesn't make the ordinary rounds — he deals only with
		// the distributor. The distributor-only "## Your dealings here" cue (buildFactorTrade)
		// replaces this surface, so it never nudges him to trade at another shop.
		return nil
	}

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

// FactorTradeView is the wholesale factor's "## Your dealings here" cue (LLM-410) — the
// distributor-only replacement for the ordinary "## Your rounds" surface. It points him at
// the one keeper he deals with and, when he stands with that keeper, cues the two-way trade
// (sell his cloth, buy the surplus); it never nudges him toward any other shop, so he isn't
// steered into a trade the PayWithItem gate would reject. nil for a non-factor or off his
// daytime rounds.
type FactorTradeView struct {
	// StorekeeperName is the distributor keeper's display name — used verbatim as the
	// pay_with_item seller arg when the factor buys the surplus, and named in the prose. ""
	// when the keeper is not co-present (then only the bearing renders).
	StorekeeperName string
	// ShopLabel is the distributor structure's display name for the prose.
	ShopLabel string
	// AtDistributor is true when the factor stands co-present with the distributor's keeper —
	// the moment to lay out his wares and buy the surplus.
	AtDistributor bool
	// Direction / Steps give the bearing to the distributor's shop when he is not there yet;
	// HasBearing is false when he is on top of it (keeper away) so the line drops the bearing.
	Direction  string
	Steps      int
	HasBearing bool
}

// buildFactorTrade returns the factor's distributor-only trade cue when the subject is a
// wholesale factor on his daytime rounds (LLM-410). nil for a non-factor, off his rounds, or
// when no distributor structure is placed (then he has no business to cue — his lifecycle
// carries him to the tavern and out at daybreak, a clean no-op). Pure over the snapshot.
func buildFactorTrade(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, members []HuddleMember) *FactorTradeView {
	if snap == nil || actorSnap == nil || actorSnap.VisitorState == nil || !actorSnap.VisitorState.DistributorOnly {
		return nil
	}
	switch actorSnap.VisitorState.Phase {
	case sim.VisitorPhaseArriving, sim.VisitorPhaseMakingRounds, sim.VisitorPhasePresent:
		// on his daytime rounds — the evening seek-a-bed / leisure cues take over after dusk
	default:
		return nil
	}
	distID := snapDistributorStructure(snap)
	if distID == "" {
		return nil // no distributor placed — no factor business to cue
	}
	shop := string(distID)
	if st := snap.Structures[distID]; st != nil && st.DisplayName != "" {
		shop = st.DisplayName
	}
	view := &FactorTradeView{ShopLabel: shop}
	// Co-present with the distributor's keeper? (the businessowner working at the distributor
	// structure, resolved off the huddle — the same shape buildTravelerRounds uses for AtKeeperShop.)
	for _, m := range members {
		ks := snap.Actors[m.ID]
		if ks == nil || ks.BusinessownerState == nil || ks.WorkStructureID != distID {
			continue
		}
		view.AtDistributor = true
		view.StorekeeperName = m.DisplayName
		break
	}
	if !view.AtDistributor {
		if vobj := snap.VillageObjects[sim.VillageObjectID(distID)]; vobj != nil {
			tile := vobj.Pos.Tile()
			view.Direction = cardinalDirection(float64(actorSnap.Pos.X), float64(actorSnap.Pos.Y), float64(tile.X), float64(tile.Y))
			view.Steps = actorSnap.Pos.Chebyshev(tile)
			view.HasBearing = view.Direction != ""
		}
	}
	return view
}

// renderFactorTrade writes the "## Your dealings here" surface — the factor's one-keeper trade
// steer. Co-present with the distributor it spells out the two-way deal and names pay_with_item
// for the buy leg; otherwise it points him at the distributor's shop with a bearing and tells
// him the other shops are no concern of his. Content-gated (LLM-410).
func renderFactorTrade(b *strings.Builder, v *FactorTradeView) {
	if v == nil {
		return
	}
	shop := sanitizeInline(v.ShopLabel)
	b.WriteString("## Your dealings here\n")
	if v.AtDistributor && v.StorekeeperName != "" {
		keeper := sanitizeInline(v.StorekeeperName)
		fmt.Fprintf(b, "You're here to deal with %s, who keeps %s — the one person in this village you trade with. Show them the cloth and charms in your pack and let them buy what they want for the village; a warm coat or cloak is worth most with the cold weather in. In return, buy the goods they have a surplus of to carry back to the city: call pay_with_item with seller \"%s\", the item you want, qty, consume_now false, coins in amount, and your words in say. The other shops here are no concern of yours.\n\n", keeper, shop, keeper)
		return
	}
	if v.HasBearing {
		fmt.Fprintf(b, "You deal only with the keeper of %s, %s. Make your way there — the other shops in this village are no concern of yours.\n\n", shop, roundsDistPhrase(v.Steps, v.Direction))
		return
	}
	fmt.Fprintf(b, "You deal only with the keeper of %s. Seek them out — the other shops in this village are no concern of yours.\n\n", shop)
}

// snapDistributorStructure returns the smallest-ID distributor-tagged structure over the
// snapshot (the perception twin of sim.pickDistributorDestination), or "" when none is placed.
// Lexicographic tie-break for determinism; one distributor by data convention.
func snapDistributorStructure(snap *sim.Snapshot) sim.StructureID {
	var pick sim.VillageObjectID
	for id, vobj := range snap.VillageObjects {
		if !sim.IsDistributorStructure(vobj) {
			continue
		}
		if _, ok := snap.Structures[sim.StructureID(id)]; !ok {
			continue
		}
		if pick == "" || id < pick {
			pick = id
		}
	}
	return sim.StructureID(pick)
}

// FactorVisitView is the distributor's "## A factor's come to trade" cue (LLM-410): when a
// wholesale factor is co-present with the distributor keeper, tell the keeper who he is and
// that he deals both ways, so the keeper buys the factor's cloth and sells him the surplus.
// nil unless the subject is the distributor with a factor co-present.
type FactorVisitView struct {
	// FactorName is the factor's display name — used verbatim as the pay_with_item seller arg
	// when the keeper buys the factor's cloth, and named in the prose.
	FactorName string
	// Origin colors the prose ("a factor out of Boston"); "" drops the clause.
	Origin string
}

// buildFactorVisit returns the distributor-facing cue when the subject is the village
// distributor and a wholesale factor is co-present in his huddle (LLM-410). nil for anyone
// else, or when no factor is present. Pure over the snapshot.
func buildFactorVisit(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, members []HuddleMember) *FactorVisitView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	if actorSnap.VisitorState != nil {
		return nil // the distributor is a resident keeper, never a visitor
	}
	if !sim.ActorIsDistributor(snap.VillageObjects, actorSnap.WorkStructureID) {
		return nil
	}
	for _, m := range members {
		fs := snap.Actors[m.ID]
		if fs == nil || fs.VisitorState == nil || !fs.VisitorState.DistributorOnly {
			continue
		}
		return &FactorVisitView{FactorName: m.DisplayName, Origin: fs.VisitorState.Origin}
	}
	return nil
}

// renderFactorVisit writes the "## A factor's come to trade" cue for the distributor: names
// the factor, frames the two-way deal, and names pay_with_item for the leg the keeper drives
// (buying the factor's cloth). Content-gated (LLM-410).
func renderFactorVisit(b *strings.Builder, v *FactorVisitView) {
	if v == nil {
		return
	}
	name := sanitizeInline(v.FactorName)
	b.WriteString("## A factor's come to trade\n")
	if v.Origin != "" {
		fmt.Fprintf(b, "%s, a factor out of %s, is here to deal with you — and only you. ", name, sanitizeInline(v.Origin))
	} else {
		fmt.Fprintf(b, "%s, a traveling factor, is here to deal with you — and only you. ", name)
	}
	fmt.Fprintf(b, "He carries cloth and charms from the city to sell, and he'll buy the surplus stacking up in your store to carry off. Buy what the village needs from his pack with pay_with_item (seller \"%s\", the item, qty, consume_now false, coins in amount, your words in say), and let him buy your surplus in turn.\n\n", name)
}
