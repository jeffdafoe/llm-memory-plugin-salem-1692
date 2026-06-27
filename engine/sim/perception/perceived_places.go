package perception

import (
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// perceived_places.go — ZBBS-HOME-389. The set of walkable OBJECT destinations a
// Payload surfaced to the actor THIS tick: every object_id a move-target cue
// named (eat/drink free sources — wells, fruit trees — and rest props). move_to
// resolves a structure_name that matches no village structure against this set,
// so the model can walk by NAME to a free source it was actually shown — distant
// or not — without the engine pretending the actor knows every wild bush.
//
// STRUCTURES ARE NOT TRACKED HERE (LLM-142). Village geography is common
// knowledge — a resident knows where every building is — so move_to resolves a
// structure_name against every named structure directly, with no shown-this-tick
// set. Only objects (a wild bush, a free source) stay discovered, because their
// existence is not common knowledge the way a building's is.
//
// Why this exists: the recurring "NPC emits the place NAME, move_to rejects it,
// starves in place" regression for FREE SOURCES. The cues always carried the
// object id; name-resolution just never consulted them. Collecting the shown
// object ids once and feeding them to the resolver closes that for every cue.

// PerceivedPlaces is the deduped set of object ids a Payload surfaced as move
// targets this tick. Threaded to the move_to name-resolver as its
// discovered-objects source.
type PerceivedPlaces struct {
	ObjectIDs []sim.VillageObjectID
}

// CollectPerceivedPlaces walks a Payload's move-target cues and returns the
// deduped, deterministically-ordered set of OBJECT ids they named. Pure over the
// Payload; the harness calls it once per tick (off the world goroutine) and
// threads the result to the move_to handler. nil slice when the tick surfaced no
// object move targets. Structures are deliberately not collected — they resolve
// by village geography (LLM-142).
func CollectPerceivedPlaces(p Payload) PerceivedPlaces {
	objects := map[sim.VillageObjectID]struct{}{}

	addObject := func(id sim.VillageObjectID) {
		if id != "" {
			objects[id] = struct{}{}
		}
	}

	// Eat/drink: free public sources (wells, fruit trees). Vendor workplaces are
	// structures — common-knowledge geography (LLM-142), so they are not tracked.
	if p.Satiation != nil {
		for _, n := range p.Satiation.Needs {
			for _, fs := range n.FreeSources {
				addObject(fs.ObjectID)
			}
		}
	}

	// Rest: free object spots (shade tree, picnic area). Structure-backed spots
	// (inn, home) and remedy vendors are structures — not tracked here.
	if p.RecoveryOptions != nil {
		for _, o := range p.RecoveryOptions.Options {
			addObject(o.ObjectID)
		}
	}

	return PerceivedPlaces{ObjectIDs: sortedObjectIDs(objects)}
}

func sortedObjectIDs(set map[sim.VillageObjectID]struct{}) []sim.VillageObjectID {
	if len(set) == 0 {
		return nil
	}
	out := make([]sim.VillageObjectID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
