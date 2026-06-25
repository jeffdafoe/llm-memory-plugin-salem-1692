package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_objects.go — LLM-112. The /api/village/umbilical/objects operator
// read route: the placed-village-object roster off the published snapshot
// (lock-free + race-free, the /pay-ledger + /structures pattern). It is the READ
// side of the 11 object/* control routes (create / move / delete / set-display-
// name / set-state / set-owner / set-loiter-offset / set-entry-policy / add-tag /
// remove-tag / set-refresh): /state only reports an object COUNT, so there was no
// way to discover an object_id to feed the mutators, nor to read an object's
// current display-name / state / owner / tags / loiter-offset / entry-policy /
// refresh-policy / position before or after a change.
//
// Pure snapshot→DTO map: VillageObjects are deep-cloned at publish, so this needs
// no SendContext (unlike /agent). Optional filters narrow the list (AND-combined):
//   - id:        one object by id
//   - owner:     objects owned by an actor (OwnerActorID)
//   - tag:       objects carrying a per-instance tag
//   - structure: objects that are part of a structure's placement — the backing
//                object (whose id == the structure id, the shared-identity bridge)
//                plus any overlay objects attached to it (AttachedTo == that id).
// An unmatched filter yields an empty list (the /sell-through optional-filter
// posture), not a 404.

// UmbilicalObjectPositionDTO carries both the world-pixel anchor (the space the
// object/move control route writes) and the resolved tile (the space actor reads
// use), so an operator never has to convert between them.
type UmbilicalObjectPositionDTO struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	TileX int     `json:"tile_x"`
	TileY int     `json:"tile_y"`
}

// UmbilicalObjectLoiterOffsetDTO is the per-instance loiter-offset OVERRIDE in
// tile units (where visitors stand relative to the anchor). A nil axis means
// "use the catalog default for that axis"; the whole field is omitted when
// neither axis is overridden (set-loiter-offset hasn't touched this object).
type UmbilicalObjectLoiterOffsetDTO struct {
	X *int `json:"x,omitempty"`
	Y *int `json:"y,omitempty"`
}

// UmbilicalObjectDTO is one placed village object with its full mutable state —
// the read counterpart to the object/* control surface. Field omission mirrors
// the client ObjectDTO (empty display_name/owner/entry_policy/attached_to drop
// out); tags is always present (empty slice for none); refresh_policy reuses the
// adminObjectRefreshRow wire shape the set-refresh route accepts and echoes, so
// the read and write surfaces can't drift.
type UmbilicalObjectDTO struct {
	ID              string                          `json:"id"`
	AssetID         string                          `json:"asset_id"`
	Position        UmbilicalObjectPositionDTO      `json:"position"`
	DisplayName     string                          `json:"display_name,omitempty"`
	CurrentState    string                          `json:"current_state,omitempty"`
	OwnerActorID    string                          `json:"owner_actor_id,omitempty"`
	EntryPolicy     string                          `json:"entry_policy,omitempty"`
	LoiterOffset    *UmbilicalObjectLoiterOffsetDTO `json:"loiter_offset,omitempty"`
	Tags            []string                        `json:"tags"`
	RefreshPolicy   []adminObjectRefreshRow         `json:"refresh_policy,omitempty"`
	AttachedTo      string                          `json:"attached_to,omitempty"`
	StructureBacked bool                            `json:"structure_backed"`
}

// UmbilicalObjectsDTO is the GET /api/village/umbilical/objects response. Objects
// are sorted by id for a stable read. PublishedAt is the snapshot freshness stamp.
type UmbilicalObjectsDTO struct {
	ContractVersion int                  `json:"contract_version"`
	PublishedAt     time.Time            `json:"published_at"`
	Total           int                  `json:"total"`
	Objects         []UmbilicalObjectDTO `json:"objects"`
}

// objectsFilter holds the (already-extracted) query filters. An empty field is a
// wildcard; non-empty fields are AND-combined.
type objectsFilter struct {
	id        string
	owner     string
	tag       string
	structure string
}

// matches reports whether one snapshot object (and its computed structure-backed
// flag) passes every active filter. id is the map key; o is the live snapshot
// object (deep-cloned at publish, so HasTag etc. are race-free here).
func (f objectsFilter) matches(o *sim.VillageObject, id sim.VillageObjectID) bool {
	if f.id != "" && string(id) != f.id {
		return false
	}
	if f.owner != "" && string(o.OwnerActorID) != f.owner {
		return false
	}
	if f.tag != "" && !o.HasTag(f.tag) {
		return false
	}
	// structure: the backing object (id == structure id) OR an overlay attached
	// to it. The structure-backed object shares the structure's UUID, so an id
	// match is the building itself; AttachedTo == that id are its overlays.
	if f.structure != "" && string(id) != f.structure && string(o.AttachedTo) != f.structure {
		return false
	}
	return true
}

// handleUmbilicalObjects serves the placed-object roster off the published
// snapshot. All query filters (id / owner / tag / structure) are optional;
// read-only and lock-free, like the other umbilical snapshot reads.
func (s *Server) handleUmbilicalObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := objectsFilter{
		id:        q.Get("id"),
		owner:     q.Get("owner"),
		tag:       q.Get("tag"),
		structure: q.Get("structure"),
	}
	writeJSON(w, umbilicalObjectsFromSnapshot(s.world.Published(), filter))
}

// umbilicalObjectsFromSnapshot maps the published snapshot to the object roster
// for the given filters. Pure (no Server/world access) so it's unit-testable
// against a hand-built snapshot. A nil snapshot yields an empty roster, not a
// panic.
func umbilicalObjectsFromSnapshot(snap *sim.Snapshot, filter objectsFilter) UmbilicalObjectsDTO {
	out := UmbilicalObjectsDTO{
		ContractVersion: ContractVersion,
		Objects:         []UmbilicalObjectDTO{},
	}
	if snap == nil {
		return out
	}
	out.PublishedAt = snap.PublishedAt

	for id, o := range snap.VillageObjects {
		if o == nil {
			continue
		}
		if !filter.matches(o, id) {
			continue
		}
		// structure_backed: this placement also has a paired Structure via the
		// shared-identity bridge (same predicate as ObjectDTO.has_interior). A
		// tombstoned entry (key present, value nil) is not a real shell.
		shell, backed := snap.Structures[sim.StructureID(id)]
		backed = backed && shell != nil

		tile := o.Pos.Tile()
		dto := UmbilicalObjectDTO{
			ID:              string(id),
			AssetID:         string(o.AssetID),
			Position:        UmbilicalObjectPositionDTO{X: o.Pos.X, Y: o.Pos.Y, TileX: tile.X, TileY: tile.Y},
			DisplayName:     o.DisplayName,
			CurrentState:    o.CurrentState,
			OwnerActorID:    string(o.OwnerActorID),
			EntryPolicy:     string(o.EntryPolicy),
			Tags:            append([]string{}, o.Tags...),
			AttachedTo:      string(o.AttachedTo),
			StructureBacked: backed,
		}
		if o.LoiterOffsetX != nil || o.LoiterOffsetY != nil {
			dto.LoiterOffset = &UmbilicalObjectLoiterOffsetDTO{
				X: clonePtrInt(o.LoiterOffsetX),
				Y: clonePtrInt(o.LoiterOffsetY),
			}
		}
		if len(o.Refreshes) > 0 {
			dto.RefreshPolicy = refreshRowsToWire(o.Refreshes)
		}
		out.Objects = append(out.Objects, dto)
	}

	sort.Slice(out.Objects, func(i, j int) bool { return out.Objects[i].ID < out.Objects[j].ID })
	out.Total = len(out.Objects)
	return out
}
