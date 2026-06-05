package perception

import (
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// perceived_places.go — ZBBS-HOME-389. The set of walkable destinations a
// Payload surfaced to the actor THIS tick: every structure_id and object_id a
// move-target cue named (own anchors, eat/drink vendors + free sources, rest
// spots + remedy vendors, restock suppliers). move_to resolves a structure_name
// against this set IN ADDITION to the spatial scene-radius scan, so the model
// can walk by NAME to anything it was actually shown — distant or not — without
// the engine pretending the actor knows every place in the village (HOME-356's
// no-omniscience guard holds: only what was shown this tick resolves).
//
// Why this exists: the recurring "NPC emits the place NAME, move_to rejects it,
// starves in place" regression. HOME-326/356/359 + WORK-365 each patched ONE
// cue to carry its id — and they all DO carry ids now. The hole was never the
// cues; it was that name-resolution only ever consulted anchors + scene-radius,
// never the cue-shown places. A distant shop named in a cue (the General Store,
// a far rest spot) therefore stayed unreachable by name even though its id was
// right there in the prompt. Collecting the shown ids once and feeding them to
// the resolver closes that for EVERY cue, present and future, in one place
// instead of per-cue.

// PerceivedPlaces is the deduped sets of structure and object ids a Payload
// surfaced as move targets this tick. Threaded to the move_to name-resolver.
type PerceivedPlaces struct {
	StructureIDs []sim.StructureID
	ObjectIDs    []sim.VillageObjectID
}

// CollectPerceivedPlaces walks a Payload's move-target cues and returns the
// deduped, deterministically-ordered sets of structure and object ids they
// named. Pure over the Payload; the harness calls it once per tick (off the
// world goroutine) and threads the result to the move_to handler. nil slices
// when the tick surfaced no move targets.
func CollectPerceivedPlaces(p Payload) PerceivedPlaces {
	structures := map[sim.StructureID]struct{}{}
	objects := map[sim.VillageObjectID]struct{}{}

	addStructure := func(id sim.StructureID) {
		if id != "" {
			structures[id] = struct{}{}
		}
	}
	addObject := func(id sim.VillageObjectID) {
		if id != "" {
			objects[id] = struct{}{}
		}
	}

	// Own anchors — home/work. The resolver already always-considers these, but
	// recording them keeps the set a complete picture of what the actor was shown.
	if p.Anchors != nil {
		addStructure(p.Anchors.WorkID)
		addStructure(p.Anchors.HomeID)
	}

	// Eat/drink: vendor workplaces (paid) + free public sources (wells, fruit
	// trees). Co-present peers carry no walkable id (already here) — skipped.
	if p.Satiation != nil {
		for _, n := range p.Satiation.Needs {
			for _, v := range n.Vendors {
				addStructure(v.StructureID)
			}
			for _, fs := range n.FreeSources {
				addObject(fs.ObjectID)
			}
		}
	}

	// Rest: structure-backed spots (inn, home) carry StructureID; free objects
	// (shade tree, picnic area) carry ObjectID; remedy vendors carry StructureID.
	// Both fields are populated per-kind, so add whichever the option used.
	if p.RecoveryOptions != nil {
		for _, o := range p.RecoveryOptions.Options {
			addStructure(o.StructureID)
			addObject(o.ObjectID)
		}
	}

	// Restock suppliers (reseller replenishment).
	if p.Restocking != nil {
		for _, it := range p.Restocking.Items {
			for _, v := range it.Vendors {
				addStructure(v.StructureID)
			}
		}
	}

	return PerceivedPlaces{
		StructureIDs: sortedStructureIDs(structures),
		ObjectIDs:    sortedObjectIDs(objects),
	}
}

func sortedStructureIDs(set map[sim.StructureID]struct{}) []sim.StructureID {
	if len(set) == 0 {
		return nil
	}
	out := make([]sim.StructureID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
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
