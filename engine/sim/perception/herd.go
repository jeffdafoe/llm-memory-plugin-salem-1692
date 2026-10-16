package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// herd.go — LLM-797. "## Your cows": the forage cue's third side, for an actor
// who gathers an item the cows give (Elizabeth's milk). Like her own bushes it is
// distance-independent — a dairykeeper knows her herd wherever it grazes — and it
// rides the ForageView, so the low-stock gate, the mid-sale deferral, the
// step-out reframe and the forage wake all apply unchanged. It steers move_to
// ONLY, by the cow's exact name; the at-animal cue (findGatherableSourceCue)
// advertises gather once she stands beside one (the LLM-59 posture).

// HerdItemView is one low item the herd can replenish.
type HerdItemView struct {
	ItemLabel string
	// Ready names the animals with something to give, sorted.
	Ready []string
	// AllFull: every animal that gives this item is full — the pressing tier.
	AllFull bool
	// MoveHandle is the nearest ready animal she is not already beside ("" when
	// she stands beside one, or none is ready). BesideReady names the ready
	// animal she stands beside.
	MoveHandle  string
	BesideReady string
	kind        sim.ItemKind
}

// buildHerdItem builds the herd view for item, or ok=false when no animal gives
// it. Pure over the snapshot.
func buildHerdItem(snap *sim.Snapshot, a *sim.ActorSnapshot, item sim.ItemKind) (HerdItemView, bool) {
	var animals []sim.LivestockCandidate
	for _, c := range sim.SnapshotLivestock(snap) {
		if c.Yield.Item() == item {
			animals = append(animals, c)
		}
	}
	if len(animals) == 0 {
		return HerdItemView{}, false
	}
	sort.Slice(animals, func(i, j int) bool {
		if animals[i].Name != animals[j].Name {
			return animals[i].Name < animals[j].Name
		}
		return animals[i].ID < animals[j].ID
	})
	v := HerdItemView{ItemLabel: itemDisplayLabel(snap, item), AllFull: true, kind: item}
	bestCheb := -1
	for _, c := range animals {
		row := &c.Yield.Row
		if row.AvailableQuantity == nil || row.MaxQuantity == nil || *row.AvailableQuantity < *row.MaxQuantity {
			v.AllFull = false
		}
		if !c.Yield.HasStock() {
			continue
		}
		v.Ready = append(v.Ready, c.Name)
		cheb := a.Pos.Chebyshev(c.Pos)
		if cheb <= sim.MilkingReach {
			if v.BesideReady == "" {
				v.BesideReady = c.Name
			}
			continue
		}
		if bestCheb == -1 || cheb < bestCheb {
			bestCheb, v.MoveHandle = cheb, c.Name
		}
	}
	if v.BesideReady != "" {
		v.MoveHandle = ""
	}
	return v, true
}

// renderHerd writes "## Your cows". Scene first; the only exact token is the
// cow's name, which move_to takes as written.
func renderHerd(b *strings.Builder, herd []HerdItemView) {
	if len(herd) == 0 {
		return
	}
	b.WriteString("## Your cows\n")
	for _, it := range herd {
		label := strings.ToLower(sanitizeInline(it.ItemLabel))
		names := it.Ready
		switch {
		case len(names) == 0:
			fmt.Fprintf(b, "Your %s is running low, but none of the cows has any to give just now — they fill again in time.\n", label)
			continue
		case it.AllFull:
			fmt.Fprintf(b, "Your %s is running low, and the cows are lowing in the pen, heavy with it: %s.\n", label, joinNames(names))
		default:
			fmt.Fprintf(b, "Your %s is running low. In the pen, %s %s ready for milking.\n", label, joinNames(names), isAre(len(names)))
		}
		switch {
		case it.BesideReady != "":
			fmt.Fprintf(b, "You are standing beside %s.\n", sanitizeInline(it.BesideReady))
		case it.MoveHandle != "":
			fmt.Fprintf(b, "Use move_to with destination \"%s\" to walk over to her.\n", sanitizeInline(it.MoveHandle))
		}
	}
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
