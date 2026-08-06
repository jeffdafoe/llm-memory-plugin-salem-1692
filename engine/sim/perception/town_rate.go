package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// town_rate.go — LLM-557. The "## Town rate" section, written for both sides of the
// levy and rendered only when the two are standing together.
//
// The keeper sees where his business stands on the levy — behind, or settled — and
// the constable in front of him; the constable sees which of the keepers present are
// behind. Neither line is an imperative. The engine moves no coin here — the keeper
// hands it over with the ordinary pay command, and settleTownRate draws the debt
// down by what actually changed hands — so the scene has to be the argument, exactly
// as the farm-upkeep cue is the argument for buying shovels.
//
// CO-LOCATION-GATED, unlike the farm-upkeep cue. A farm owner's obligation sends him
// on an errand (walk to the smith, buy shovels), so his cue rides every tick
// wherever he is. A keeper owes the constable nothing but a coin handed over when
// the man is in front of him: there is nowhere to go and nothing to do until then,
// so off-scene the cue would be pure nagging. This is also why the daily assessment
// stamps no wake — see assessTownRate.
//
// The two sides are mutually exclusive in practice (the constable keeps no shop, so
// he never owes), which is why one view serves both rather than two parallel cues.

// TownRateView is the town-rate cue. Non-nil when a keeper and a constable are
// co-present — from whichever side is reading it.
type TownRateView struct {
	// Owed and ConstableName are the KEEPER's side: what his business owes in
	// arrears, and the constable standing with him.
	//
	// Owed is 0 on the collector's side, and also on a keeper who is square — the
	// two are told apart by Collector, never by this field. Since LLM-607 a
	// paid-up keeper gets a view rather than nothing at all, so 0 here means
	// "settled" and not "no cue".
	Owed          int
	ConstableName string

	// BusinessName is the keeper's own business, so the line can name the shop the
	// rate is levied on rather than speaking of an abstract debt.
	BusinessName string

	// Collector marks this as the CONSTABLE's side. It is the render's dispatch,
	// not len(Debtors): since LLM-572 the collecting side renders with an empty
	// Debtors list too, and without an explicit flag that view would fall through
	// to the keeper's branch and tell the constable he owes a rate he collects.
	Collector bool

	// Debtors is every co-present keeper currently behind on the rate, sorted by
	// name so the line is stable across snapshots. Collector side only.
	Debtors []TownRateDebtor

	// PaidUpBusinesses names the co-present keepers' businesses that owe nothing,
	// sorted. Collector side only, and read only when Debtors is empty — when
	// somebody is behind, the debtor sentences already carry the direction of the
	// levy and naming the settled shops as well would only pad the scene.
	PaidUpBusinesses []string
}

// TownRateDebtor is one keeper the constable could collect from right now.
type TownRateDebtor struct {
	KeeperName   string
	BusinessName string
	Owed         int
}

// buildTownRate returns the town-rate cue for either side, or nil. Pure over the
// snapshot.
func buildTownRate(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *TownRateView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	if isConstableSnapshot(actorSnap) {
		return buildTownRateCollector(snap, actorID, actorSnap)
	}
	return buildTownRateKeeper(snap, actorID, actorSnap)
}

// buildTownRateKeeper builds the paying side: this actor owns a rateable business
// and a constable is close enough that a pay would resolve this tick.
//
// LLM-607 widened the gate from "he OWES" to "he is rateable at all", the same
// widening LLM-572 made on the collecting side and for the same reason. The old gate
// meant the levy vanished from the keeper's scene the instant he paid it, so the one
// state the scene never described was the settled one — and settled is the state a
// keeper who pays every day is in almost all the time. What filled the silence was
// the coin record: nine single coins to the constable with nothing coming back,
// which is the shape of nine unfilled orders. Moses James read it that way and
// collected eight coins of refunds on it.
func buildTownRateKeeper(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *TownRateView {
	business := sim.RateableBusinessOf(snap.VillageObjects, actorID)
	if business == nil {
		return nil
	}
	sc := buyerCoPresenceScope(snap, actorSnap)
	if sc.huddle == "" && sc.scope == "" {
		return nil // neither conversing nor standing in a shop scope — nobody to pay
	}
	// Lowest ActorID among co-present constables, so the named man is stable across
	// snapshots (the same determinism rule coPresentSellerForItem follows). The
	// village has one constable today; the tie-break costs nothing and keeps the
	// line from flickering if a second is ever appointed.
	var bestID sim.ActorID
	var bestName string
	for id, other := range snap.Actors {
		if other == nil || id == actorID || other.DisplayName == "" {
			continue
		}
		if !isConstableSnapshot(other) || !sc.sellerCoPresent(other) {
			continue
		}
		if bestID == "" || id < bestID {
			bestID = id
			bestName = other.DisplayName
		}
	}
	if bestID == "" {
		return nil
	}
	return &TownRateView{
		Owed:          business.RateOwed,
		ConstableName: bestName,
		BusinessName:  business.DisplayName,
	}
}

// buildTownRateCollector builds the collecting side: the constable, with the
// co-present keepers who are behind on the rate and those who are square.
//
// LLM-572 widened the gate from "a co-present keeper OWES" to "a co-present keeper
// is rateable at all". The old gate meant the levy appeared in the constable's scene
// ONLY while somebody was in arrears, so the direction of the rate — that it runs
// toward him — was invisible for most of his rounds. Live consequence, 2026-07-30
// 19:03:43 UTC: three minutes after refunding Moses James, Gideon Marsh paid John
// Ellis a coin "for" a `Town rate — Tavern day's fee`. The constable paid the town
// rate to a tavern keeper. Nothing settled (settleTownRate requires the PAYEE be a
// constable); a coin simply ran backwards. His scene had never once told him which
// way the levy flows, because the Tavern was square at the time.
func buildTownRateCollector(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *TownRateView {
	sc := buyerCoPresenceScope(snap, actorSnap)
	if sc.huddle == "" && sc.scope == "" {
		return nil
	}
	var debtors []TownRateDebtor
	var paidUp []string
	for id, other := range snap.Actors {
		if other == nil || id == actorID || other.DisplayName == "" {
			continue
		}
		if !sc.sellerCoPresent(other) {
			continue
		}
		business := sim.RateableBusinessOf(snap.VillageObjects, id)
		if business == nil {
			continue // not a keeper — no rate is levied on them either way
		}
		if business.RateOwed <= 0 {
			paidUp = append(paidUp, business.DisplayName)
			continue
		}
		debtors = append(debtors, TownRateDebtor{
			KeeperName:   other.DisplayName,
			BusinessName: business.DisplayName,
			Owed:         business.RateOwed,
		})
	}
	if len(debtors) == 0 && len(paidUp) == 0 {
		return nil // no keeper in front of him; the levy is not part of this scene
	}
	// Stable order regardless of map iteration — the same reason the vendor scans
	// tie-break deterministically.
	sort.Slice(debtors, func(i, j int) bool { return debtors[i].KeeperName < debtors[j].KeeperName })
	sort.Strings(paidUp)
	return &TownRateView{Collector: true, Debtors: debtors, PaidUpBusinesses: paidUp}
}

// isConstableSnapshot reports whether an actor snapshot carries the constable
// attribute. AttributeSlugs is the sorted projection of the live Actor.Attributes
// keys, and the engine keys behaviour on presence only — the same test
// sim.ActorIsConstable makes against the live Actor.
func isConstableSnapshot(a *sim.ActorSnapshot) bool {
	if a == nil {
		return false
	}
	for _, slug := range a.AttributeSlugs {
		if slug == sim.AttrConstable {
			return true
		}
	}
	return false
}

// coinsOwedPhrase voices a rate balance as coin rather than a bare number —
// "a coin" reads as money owed, "1" reads as a counter.
func coinsOwedPhrase(n int) string {
	if n == 1 {
		return "a coin"
	}
	return fmt.Sprintf("%d coins", n)
}

// renderTownRate writes the "## Town rate" section. Content-gated: a nil view writes
// nothing.
//
// Tiered by arrears rather than stating a number flatly, the felt-needs register
// applied to a debt: one day's rate is a passing courtesy, several days is something
// the keeper ought to feel behind on. Neither tier issues an imperative — the scene
// is the argument.
func renderTownRate(b *strings.Builder, v *TownRateView) {
	if v == nil {
		return
	}
	b.WriteString("## Town rate\n")
	if v.Collector {
		renderTownRateCollector(b, v)
		return
	}
	renderTownRateKeeper(b, v)
}

// renderTownRateKeeper writes the paying side. It names the constable verbatim and
// the exact coin, because both are arguments the pay call needs to resolve.
func renderTownRateKeeper(b *strings.Builder, v *TownRateView) {
	if v.Owed <= 0 {
		// Nothing owing. The sentence has no tool and no imperative because there
		// is nothing to do — it exists so the settled state is SAID rather than
		// left to be inferred from a record of coin going one way (LLM-607). A due
		// is discharged when it is handed over, and a keeper who reads his own rate
		// payments as debts still owing him will ask for them back.
		//
		// "Stands settled" rather than "is paid": the engine knows the balance is
		// clear, not that this keeper ever paid anything. A business assessed today
		// and collected from an hour later is square by the same field as one whose
		// levy has never yet accrued, and the line must not assert a payment for
		// which there may be no record.
		fmt.Fprintf(b,
			"%s keeps the watch. The rate on the %s stands settled — nothing is owing on it, and nothing is owed back.\n",
			v.ConstableName, v.BusinessName)
		return
	}
	owed := coinsOwedPhrase(v.Owed)
	if v.Owed > 1 {
		// Several days behind — the keeper should feel it as arrears, not a courtesy.
		fmt.Fprintf(b,
			"%s keeps the watch, and the town rate on the %s has gone unpaid these several days — %s owing. Settle it with pay (recipient: %s, amount: %d), saying your piece as you hand it over.\n",
			v.ConstableName, v.BusinessName, owed, v.ConstableName, v.Owed)
		return
	}
	// A single day's rate — a passing courtesy, voiced quietly.
	fmt.Fprintf(b,
		"%s keeps the watch, and the day's rate on the %s is owing — %s. Settle it with pay (recipient: %s, amount: %d), saying your piece as you hand it over.\n",
		v.ConstableName, v.BusinessName, owed, v.ConstableName, v.Owed)
}

// renderTownRateCollector writes the collecting side. Deliberately NO imperative and
// no tool: the constable has nothing to call — the coin is the keeper's to hand over
// — so the line states who is behind and leaves the asking to him. An imperative
// here would also risk instructing a second terminal verb alongside speak.
//
// The opening sentence is the load-bearing one and it renders on EVERY collector
// view, arrears or none (LLM-572). It is the only place the constable is ever told
// which way the levy runs, and the settled case — a keeper in front of him owing
// nothing — is the one that used to render no section at all.
// The second sentence is LLM-607. Gideon Marsh handed back eight of the nine coins
// of rate he had collected from Moses James, one refund at a time, because a keeper
// pressed him about coin he had paid and received no goods for and nothing in the
// constable's scene said a levy is not a purchase. He is a stateful NPC, so
// buildRelationships is gated off and he holds no stored view of any villager — this
// line is the only thing he has to answer with.
func renderTownRateCollector(b *strings.Builder, v *TownRateView) {
	b.WriteString("The town rate keeps you, and it falls to you to collect it. What you collect is the town's, not yours to hand back.")
	if len(v.Debtors) == 0 {
		// Everyone here is square. Say so plainly and close the door on the rate
		// being asked for again in this scene — the constable reading "it falls to
		// you to collect it" with no debtor named would otherwise be left to guess
		// whether something is owing.
		fmt.Fprintf(b, " The rate on the %s is paid up — nothing is owing you here.\n", joinNames(v.PaidUpBusinesses))
		return
	}
	for _, d := range v.Debtors {
		if d.Owed > 1 {
			fmt.Fprintf(b, " %s has let the rate on the %s run on — %s behind.", d.KeeperName, d.BusinessName, coinsOwedPhrase(d.Owed))
			continue
		}
		fmt.Fprintf(b, " %s owes the day's rate on the %s — %s.", d.KeeperName, d.BusinessName, coinsOwedPhrase(d.Owed))
	}
	b.WriteString("\n")
}
