package perception

import (
	"fmt"
	"math"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// public_works.go — LLM-654 perception: the town's work on a broken well.
//
// HAND side ("## The town's works"): a worker (AttrWorker, not a visitor, not
// already on a job) is told a well is broken, what the town pays to mend it,
// and — standing at it — to call repair. The one signal (PublicWorksView with
// AtSite) also gates the repair tool, so the cue and the tool agree. Away from
// the well the cue is a walk-to with a bearing and names no second tool (the
// terminal-verb rule).
//
// CONSTABLE side (same header): the keeper of the peace knows the well is
// broken and that the town has posted the work — the standing fact he can speak
// of on his rounds. He hires no one; the bounty is open to any hand.
//
// Silence when every well is sound, and for a hand when the chest cannot pay
// (PublicWorksBountyOpen — the same predicate StartRepair gates on) or someone
// is already at the mending.

// PublicWorksView is the cue for a broken well.
type PublicWorksView struct {
	// Site is the broken object's display name; SiteID its id (the move_to
	// destination — two wells share the name "Well").
	Site   string
	SiteID sim.VillageObjectID
	// Bounty is what the chest pays; WorkMinutes how long the mending takes.
	Bounty      int
	WorkMinutes int
	// Constable is the constable's standing-fact view; BountyOpen tells him
	// whether the town can pay for the work just now. A hand's view is only
	// built when the bounty is open.
	Constable  bool
	BountyOpen bool
	// AtSite: the hand stands at the broken well — repair is theirs to call.
	// Walk is the bearing when they are not ("a short walk to the north").
	AtSite bool
	Walk   string
}

// damagedWell returns the broken well (lowest id when, against the guard, there
// are two), or nil.
func damagedWell(snap *sim.Snapshot) *sim.VillageObject {
	var best *sim.VillageObject
	for _, obj := range snap.VillageObjects {
		if !obj.IsWell() || !obj.Damaged() {
			continue
		}
		if best == nil || obj.ID < best.ID {
			best = obj
		}
	}
	return best
}

// wellUnderRepair reports whether another actor already has the mending in hand.
func wellUnderRepair(snap *sim.Snapshot, siteID sim.VillageObjectID, except sim.ActorID) bool {
	for id, a := range snap.Actors {
		if id == except || a == nil {
			continue
		}
		if a.SourceActivityKind == sim.SourceActivityRepair && a.SourceActivityObjectID == siteID {
			return true
		}
	}
	return false
}

// buildPublicWorks returns the broken-well cue for the subject, or nil.
// laboring is true when the subject is on (or walking to) a hired job.
func buildPublicWorks(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, laboring bool) *PublicWorksView {
	if snap == nil || actorSnap == nil || actorSnap.VisitorState != nil {
		return nil
	}
	site := damagedWell(snap)
	if site == nil {
		return nil
	}
	open := sim.PublicWorksBountyOpen(snap.Environment.TownChest, snap.PublicWorksBounty, snap.PublicWorksChestReserve)
	v := &PublicWorksView{
		Site:        sim.DamageSiteLabel(snap.VillageObjects, snap.Structures, snap.Assets, site),
		SiteID:      site.ID,
		Bounty:      snap.PublicWorksBounty,
		WorkMinutes: snap.PublicWorksRepairSeconds / 60,
		BountyOpen:  open,
	}
	if v.Site == "" {
		v.Site = "the well"
	}
	if isConstableSnapshot(actorSnap) {
		v.Constable = true
		return v
	}
	if !subjectIsWorker(actorSnap) || laboring || !open {
		return nil
	}
	// Already mending it: the in-flight line holds them there; a second "call
	// repair" would bounce as busy.
	if actorSnap.SourceActivityKind == sim.SourceActivityRepair && actorSnap.SourceActivityObjectID == site.ID {
		return nil
	}
	if wellUnderRepair(snap, site.ID, actorID) {
		return nil
	}
	if id, ok := sim.ResolveLoiteringObject(snap.VillageObjects, snap.Assets, actorSnap.Pos, sim.LoiterAttributionTiles); ok && id == site.ID {
		v.AtSite = true
		return v
	}
	toTile := site.Pos.Tile()
	dist := math.Max(
		math.Abs(float64(toTile.X-actorSnap.Pos.X)),
		math.Abs(float64(toTile.Y-actorSnap.Pos.Y)),
	)
	if dir := cardinalDirection(float64(actorSnap.Pos.X), float64(actorSnap.Pos.Y), float64(toTile.X), float64(toTile.Y)); dir != "" {
		v.Walk = qualitativeDistance(dist) + " " + dir
	}
	return v
}

// OffersRepair reports whether the cue hands the subject the repair tool: a
// hand standing at the broken well. The tool gate reads this, so the tool
// appears exactly when the cue says "call repair". Nil-safe.
func (v *PublicWorksView) OffersRepair() bool {
	return v != nil && !v.Constable && v.AtSite
}

// renderPublicWorks writes "## The town's works". Content-gated.
func renderPublicWorks(b *strings.Builder, v *PublicWorksView) {
	if v == nil {
		return
	}
	site := sanitizeInline(v.Site)
	b.WriteString("## The town's works\n")
	fmt.Fprintf(b, "The windlass at the %s is down — nobody can drink or draw water there until it is mended.", site)
	if v.Constable {
		if v.BountyOpen {
			fmt.Fprintf(b, " The town has posted %s for the mending; any hand seeking work may take it on.\n\n", coinsPhrase(v.Bounty))
		} else {
			b.WriteString(" The town chest cannot pay for the mending just now.\n\n")
		}
		return
	}
	work := "a while"
	if v.WorkMinutes > 0 {
		work = "about " + humanizeWorkMinutes(v.WorkMinutes)
	}
	fmt.Fprintf(b, " The town pays %s to whoever mends it — %s of work, no nails needed.\n", coinsPhrase(v.Bounty), work)
	if v.AtSite {
		b.WriteString("You are standing at it. Call repair to take the work, and stay put until it is done.\n\n")
		return
	}
	if v.Walk != "" {
		fmt.Fprintf(b, "It is %s. Head there (move_to \"%s\") if you want the work.\n\n", v.Walk, v.SiteID)
		return
	}
	fmt.Fprintf(b, "Head there (move_to \"%s\") if you want the work.\n\n", v.SiteID)
}
