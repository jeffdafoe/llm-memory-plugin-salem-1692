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
// The keeper sees what his business owes and the constable in front of him; the
// constable sees which of the keepers present are behind. Neither line is an
// imperative. The engine moves no coin here — the keeper hands it over with the
// ordinary pay command, and settleTownRate draws the debt down by what actually
// changed hands — so the scene has to be the argument, exactly as the farm-upkeep
// cue is the argument for buying shovels.
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

// TownRateView is the town-rate cue. Non-nil only when a keeper who owes and a
// constable are co-present — from whichever side is reading it.
type TownRateView struct {
	// Owed and ConstableName are the KEEPER's side: what his business owes in
	// arrears, and the constable standing with him. Owed is 0 on the collector's
	// side.
	Owed          int
	ConstableName string

	// BusinessName is the keeper's own business, so the line can name the shop the
	// rate is levied on rather than speaking of an abstract debt.
	BusinessName string

	// Debtors is the CONSTABLE's side: every co-present keeper currently behind on
	// the rate, sorted by name so the line is stable across snapshots. Empty on the
	// keeper's side.
	Debtors []TownRateDebtor
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

// buildTownRateKeeper builds the paying side: this actor owns a business in arrears
// and a constable is close enough that a pay resolves this tick.
func buildTownRateKeeper(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *TownRateView {
	business := sim.RateableBusinessOf(snap.VillageObjects, actorID)
	if business == nil || business.RateOwed <= 0 {
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

// buildTownRateCollector builds the collecting side: the constable, with co-present
// keepers who are behind on the rate.
func buildTownRateCollector(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *TownRateView {
	sc := buyerCoPresenceScope(snap, actorSnap)
	if sc.huddle == "" && sc.scope == "" {
		return nil
	}
	var debtors []TownRateDebtor
	for id, other := range snap.Actors {
		if other == nil || id == actorID || other.DisplayName == "" {
			continue
		}
		if !sc.sellerCoPresent(other) {
			continue
		}
		business := sim.RateableBusinessOf(snap.VillageObjects, id)
		if business == nil || business.RateOwed <= 0 {
			continue
		}
		debtors = append(debtors, TownRateDebtor{
			KeeperName:   other.DisplayName,
			BusinessName: business.DisplayName,
			Owed:         business.RateOwed,
		})
	}
	if len(debtors) == 0 {
		return nil
	}
	// Stable order regardless of map iteration — the same reason the vendor scans
	// tie-break deterministically.
	sort.Slice(debtors, func(i, j int) bool { return debtors[i].KeeperName < debtors[j].KeeperName })
	return &TownRateView{Debtors: debtors}
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
	if len(v.Debtors) > 0 {
		renderTownRateCollector(b, v)
		return
	}
	renderTownRateKeeper(b, v)
}

// renderTownRateKeeper writes the paying side. It names the constable verbatim and
// the exact coin, because both are arguments the pay call needs to resolve.
func renderTownRateKeeper(b *strings.Builder, v *TownRateView) {
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
func renderTownRateCollector(b *strings.Builder, v *TownRateView) {
	parts := make([]string, 0, len(v.Debtors))
	for _, d := range v.Debtors {
		if d.Owed > 1 {
			parts = append(parts, fmt.Sprintf("%s has let the rate on the %s run on — %s behind.", d.KeeperName, d.BusinessName, coinsOwedPhrase(d.Owed)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s owes the day's rate on the %s — %s.", d.KeeperName, d.BusinessName, coinsOwedPhrase(d.Owed)))
	}
	fmt.Fprintf(b, "The town rate keeps you, and it falls to you to collect it. %s\n", strings.Join(parts, " "))
}
