package sim

import "sort"

// stall_wares.go — LLM-753. What a business's shelf holds: the goods its keepers
// SELL, and how much of each is spare to sell. Read by the player's stall window
// (httpapi pc/stall) so the goods drawn behind the counter are exactly the goods
// the keeper deals in — the same supplier rule the buy directory routes buyers
// by, and the same reservation the trade gate holds back.

// FirstHandSupplier reports whether a seller stationed at workStructureID, with
// restock policy p, supplies kind at first hand: it makes or gathers it (a
// produce or forage line), or it is the village distributor, whose every held
// good is sale stock. A reseller's buy lines and a maker's bought-in makings do
// not qualify (LLM-252). The buy directory (perception findItemVendors) and the
// stall shelf (StallWares) share this one definition.
func FirstHandSupplier(objects map[VillageObjectID]*VillageObject, workStructureID StructureID, p *RestockPolicy, kind ItemKind) bool {
	return p.ProducesOrForages(kind) || ActorIsDistributor(objects, workStructureID)
}

// StallWare is one good on a business's shelf: Spare is how many units its
// keepers would sell, summed over everyone who works there.
type StallWare struct {
	Kind  ItemKind
	Spare int
}

// StallWares lists the goods on structureID's shelf, sorted by kind. A good is
// on the shelf when someone who works there (a non-PC actor whose
// WorkStructureID is the structure) holds it, supplies it at first hand
// (FirstHandSupplier), and it is a good that can change hands at all
// (KindBarterable — not a service, not eaten where it is served). Spare is the
// held units less what the keeper keeps to work with: the larger of the
// spoken-for claim (SpokenFor — bought-in makings, the worn garment) and the
// recipe-input floor (ReorderFloors — the LLM-609 floor the wares cue and the
// restock warrant read), so a smith's water above his forge's floor is a ware
// and the floor itself is not. A good with nothing spare is left off.
func StallWares(snap *Snapshot, structureID StructureID) []StallWare {
	if snap == nil || structureID == "" {
		return nil
	}
	spare := map[ItemKind]int{}
	for _, a := range snap.Actors {
		if a == nil || a.Kind == KindPC || a.WorkStructureID != structureID {
			continue
		}
		spokenFor := SpokenFor(snap.ItemKinds, snap.Recipes, SnapshotBarterHolder(snap, a))
		floors := ReorderFloors(snap.Recipes, a.RestockPolicy)
		for kind, held := range a.Inventory {
			if held <= 0 || !FirstHandSupplier(snap.VillageObjects, a.WorkStructureID, a.RestockPolicy, kind) {
				continue
			}
			if !KindBarterable(snap.ItemKinds[kind]) {
				continue
			}
			keep := spokenFor[kind].Qty
			if floors[kind] > keep {
				keep = floors[kind]
			}
			if held > keep {
				spare[kind] += held - keep
			}
		}
	}
	out := make([]StallWare, 0, len(spare))
	for kind, n := range spare {
		out = append(out, StallWare{Kind: kind, Spare: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}
