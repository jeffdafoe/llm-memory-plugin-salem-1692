package perception

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// public_works.go — LLM-654 perception: the town's work on a damaged site — a
// broken well, (LLM-675) a damaged business, or (LLM-677) a fallen tree across
// a road, which a hand clears standing at its loiter pin (sim.AtRoadObstacle).
//
// HAND side ("## The town's works"): a worker (AttrWorker, not a visitor, not
// already on a job) is told what is damaged, what the town pays to mend it,
// and — standing at it — to call repair. The one signal (PublicWorksView with
// a single AtSite site) also gates the repair tool, so the cue and the tool
// agree. Away from every site the cue is a walk-to per site with a bearing and
// names no second tool (the terminal-verb rule).
//
// CONSTABLE side (same header): the keeper of the peace knows what is damaged
// and whether the town has posted the work — the standing fact he can speak of
// on his rounds. He hires no one; the bounty is open to any hand.
//
// KEEPER side (same header, LLM-675): the owner of a damaged business is told
// what happened to it, what it stops, and that the mending is the town's — so
// the "## Restocking" silence reads as a cause, and he does not try to mend it
// himself. No tool: the town pays a hand, not him.
//
// Silence when nothing is damaged, and for a hand when the chest cannot pay
// (PublicWorksBountyOpen — the same predicate StartRepair gates on) or someone
// is already at the mending.

// PublicWorksSite is one damaged site as the subject sees it.
type PublicWorksSite struct {
	// Kind is sim.PublicWorksWell, sim.PublicWorksBusiness or
	// sim.PublicWorksRoad; Cause, for a business, is sim.DebrisStateStorm or
	// sim.DebrisStateWorn.
	Kind  string
	Cause string
	// Fact is the shared "what is broken" opening (sim.DamageFact); Site the
	// site's label; SiteID its id (the move_to destination — two wells share
	// the name "Well").
	Fact   string
	Site   string
	SiteID sim.VillageObjectID
	// Bounty is what the chest pays; WorkMinutes how long the mending takes;
	// BountyOpen whether the chest can pay it just now.
	Bounty      int
	WorkMinutes int
	BountyOpen  bool
	// AtSite: the hand stands at the site — repair is theirs to call. Walk is
	// the bearing when they are not ("a short walk to the north").
	AtSite bool
	Walk   string
}

// PublicWorksView is "## The town's works" for one subject. Exactly one of
// Constable / Keeper / neither (a hand) describes the audience.
type PublicWorksView struct {
	Constable bool
	Keeper    bool
	Sites     []PublicWorksSite
}

// damagedSites returns every damaged well and business, lowest id first.
func damagedSites(snap *sim.Snapshot) []*sim.VillageObject {
	var out []*sim.VillageObject
	for _, obj := range snap.VillageObjects {
		if sim.IsDamagedSite(obj) {
			out = append(out, obj)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// siteUnderRepair reports whether another actor already has the town's
// mending of siteID in hand. A keeper's own nail-mend at the same shop is not
// the town's work and does not count.
func siteUnderRepair(snap *sim.Snapshot, siteID sim.VillageObjectID, except sim.ActorID) bool {
	for id, a := range snap.Actors {
		if id == except || a == nil {
			continue
		}
		if a.SourceActivityKind == sim.SourceActivityRepair && a.SourceActivityPublicWorks && a.SourceActivityObjectID == siteID {
			return true
		}
	}
	return false
}

// publicWorksSite projects one damaged object into the view's terms.
func publicWorksSite(snap *sim.Snapshot, obj *sim.VillageObject) PublicWorksSite {
	kind := sim.PublicWorksKind(obj)
	bounty, seconds := snap.PublicWorksTerms(kind)
	s := PublicWorksSite{
		Kind:        kind,
		Fact:        sim.DamageFact(snap.VillageObjects, snap.Structures, snap.Assets, obj),
		Site:        sim.DamageSiteLabel(snap.VillageObjects, snap.Structures, snap.Assets, obj),
		SiteID:      obj.ID,
		Bounty:      bounty,
		WorkMinutes: seconds / 60,
		BountyOpen:  sim.PublicWorksBountyOpen(snap.Environment.TownChest, bounty, snap.PublicWorksChestReserve),
	}
	if kind == sim.PublicWorksBusiness {
		s.Cause = sim.BusinessDamageCause(snap.VillageObjects, obj.ID)
	}
	if s.Site == "" {
		s.Site = "the well"
	}
	return s
}

// atPublicWorksSite reports whether the subject stands at the damaged site: a
// well by the loitering resolution the drink path uses, a business inside or
// at its pin (sim.AtBusiness — the test StartRepair uses).
func atPublicWorksSite(snap *sim.Snapshot, a *sim.ActorSnapshot, obj *sim.VillageObject) bool {
	switch sim.PublicWorksKind(obj) {
	case sim.PublicWorksBusiness:
		return sim.AtBusiness(a.Pos, a.InsideStructureID, obj.ID, objectLoiterPin(obj), true)
	case sim.PublicWorksRoad:
		return sim.AtRoadObstacle(a.Pos, obj, snap.Assets[obj.AssetID])
	}
	id, ok := sim.ResolveLoiteringObject(snap.VillageObjects, snap.Assets, a.Pos, sim.LoiterAttributionTiles)
	return ok && id == obj.ID
}

// buildPublicWorks returns the town's-works cue for the subject, or nil.
// laboring is true when the subject is on (or walking to) a hired job.
func buildPublicWorks(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, laboring bool) *PublicWorksView {
	if snap == nil || actorSnap == nil || actorSnap.VisitorState != nil {
		return nil
	}
	damaged := damagedSites(snap)
	if len(damaged) == 0 {
		return nil
	}
	if isConstableSnapshot(actorSnap) {
		v := &PublicWorksView{Constable: true}
		for _, obj := range damaged {
			v.Sites = append(v.Sites, publicWorksSite(snap, obj))
		}
		return v
	}
	for _, obj := range damaged {
		if obj.OwnerActorID == actorID && sim.PublicWorksKind(obj) == sim.PublicWorksBusiness {
			return &PublicWorksView{Keeper: true, Sites: []PublicWorksSite{publicWorksSite(snap, obj)}}
		}
	}
	if !subjectIsWorker(actorSnap) || laboring {
		return nil
	}
	// Busy at anything — eating, gathering, already mending — the in-flight
	// line holds them there, and StartRepair would bounce a second window as
	// busy. The work is still on offer once they finish.
	if actorMidSourceActivity(actorSnap) {
		return nil
	}
	// Standing at a stall of their own, "repair" means that stall (StartRepair
	// takes the stall branch first), so no town's work is offered there.
	if own, _ := sim.WearableStallToMend(snap.VillageObjects, snap.LaborLedger, actorID); own != nil &&
		sim.AtBusiness(actorSnap.Pos, actorSnap.InsideStructureID, own.ID, objectLoiterPin(own), true) {
		return nil
	}
	v := &PublicWorksView{}
	for _, obj := range damaged {
		s := publicWorksSite(snap, obj)
		if !s.BountyOpen || siteUnderRepair(snap, obj.ID, actorID) {
			continue
		}
		if atPublicWorksSite(snap, actorSnap, obj) {
			// Standing at a site, that is the work in front of them — the only
			// site the cue names, so "call repair" has one meaning.
			s.AtSite = true
			v.Sites = []PublicWorksSite{s}
			return v
		}
		toTile := obj.Pos.Tile()
		dist := math.Max(
			math.Abs(float64(toTile.X-actorSnap.Pos.X)),
			math.Abs(float64(toTile.Y-actorSnap.Pos.Y)),
		)
		if dir := cardinalDirection(float64(actorSnap.Pos.X), float64(actorSnap.Pos.Y), float64(toTile.X), float64(toTile.Y)); dir != "" {
			s.Walk = qualitativeDistance(dist) + " " + dir
		}
		v.Sites = append(v.Sites, s)
	}
	if len(v.Sites) == 0 {
		return nil
	}
	return v
}

// OffersRepair reports whether the cue hands the subject the repair tool: a
// hand standing at a damaged site. The tool gate reads this, so the tool
// appears exactly when the cue says "call repair". Nil-safe.
func (v *PublicWorksView) OffersRepair() bool {
	return v != nil && !v.Constable && !v.Keeper && len(v.Sites) == 1 && v.Sites[0].AtSite
}

// publicWorksConsequence is what a damaged site stops, after the fact.
func publicWorksConsequence(s PublicWorksSite) string {
	switch s.Kind {
	case sim.PublicWorksBusiness:
		return " — it can take in no new stock, and its work goes slowly, until it is mended."
	case sim.PublicWorksRoad:
		return " — walkers must go around it until it is cleared."
	}
	return " — nobody can drink or draw water there until it is mended."
}

// renderPublicWorks writes "## The town's works". Content-gated.
func renderPublicWorks(b *strings.Builder, v *PublicWorksView) {
	if v == nil || len(v.Sites) == 0 {
		return
	}
	b.WriteString("## The town's works\n")
	switch {
	case v.Constable:
		for _, s := range v.Sites {
			b.WriteString(sanitizeInline(s.Fact) + publicWorksConsequence(s))
			noun := sim.PublicWorksMendNoun(s.Kind)
			if s.BountyOpen {
				fmt.Fprintf(b, " The town has posted %s for %s; any hand seeking work may take it on.\n", coinsPhrase(s.Bounty), noun)
			} else {
				fmt.Fprintf(b, " The town chest cannot pay for %s just now.\n", noun)
			}
		}
		b.WriteString("\n")
	case v.Keeper:
		renderKeeperPublicWorks(b, v.Sites[0])
	default:
		for _, s := range v.Sites {
			renderHandPublicWorks(b, s)
		}
		b.WriteString("\n")
	}
}

// renderKeeperPublicWorks is the keeper's own damaged business: what happened,
// what it stops, and that the mending is the town's.
func renderKeeperPublicWorks(b *strings.Builder, s PublicWorksSite) {
	name := sanitizeInline(s.Site)
	if s.Cause == sim.DebrisStateStorm {
		fmt.Fprintf(b, "The storm has torn at your %s — branches and broken boards lie by the door.", name)
	} else {
		fmt.Fprintf(b, "Boards have split and given way at your %s.", name)
	}
	b.WriteString(" Until it is mended you can take in no new shelf stock, and your work goes slowly; you can still sell what's on hand.")
	if s.BountyOpen {
		fmt.Fprintf(b, " The town pays a hand %s to mend it — the work is the town's, not yours.\n\n", coinsPhrase(s.Bounty))
		return
	}
	b.WriteString(" The town chest cannot pay for the mending just now, so it waits.\n\n")
}

// renderHandPublicWorks is one site on offer to a hand.
func renderHandPublicWorks(b *strings.Builder, s PublicWorksSite) {
	b.WriteString(sanitizeInline(s.Fact) + publicWorksConsequence(s))
	work := "a while"
	if s.WorkMinutes > 0 {
		work = "about " + humanizeWorkMinutes(s.WorkMinutes)
	}
	fmt.Fprintf(b, " The town pays %s to whoever %s it — %s of work, no nails needed.\n", coinsPhrase(s.Bounty), sim.PublicWorksMendVerb(s.Kind), work)
	switch {
	case s.AtSite:
		b.WriteString("You are standing at it. Call repair to take the work, and stay put until it is done.\n")
	case s.Walk != "":
		fmt.Fprintf(b, "It is %s. Head there (move_to \"%s\") if you want the work.\n", s.Walk, s.SiteID)
	default:
		fmt.Fprintf(b, "Head there (move_to \"%s\") if you want the work.\n", s.SiteID)
	}
}
