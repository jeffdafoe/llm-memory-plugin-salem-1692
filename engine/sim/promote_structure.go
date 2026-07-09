package sim

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// promote_structure.go — LLM-249 structure authoring. Promotes an
// already-placed VillageObject into a first-class Structure that shares its id
// (the Shared-Identity Bridge, structure_anchors.go), turning a dead decorative
// placement into a navigable named building: move_to("<name>") resolves it, it
// can be assigned as an NPC home/work anchor, and it can host an interior.
//
// Before this the only structure-creation path was DB seed data loaded at
// engine boot (StructuresRepo.LoadAll). A structure-category asset dropped in
// the editor created only a village_object and stayed invisible to move_to's
// name resolver (which scans w.Structures). The editor now calls this right
// after placing a structure-category asset (Option B — auto-promote on drop).
//
// The new Structure is registered live in w.Structures, so it is navigable on
// the very next tick with NO engine restart, and it persists on the next
// checkpoint through the normal generation-marker snapshot — SaveSnapshot
// UPSERTs every w.Structures entry, so an in-memory add is durable without a
// direct pg write. (A crash before that checkpoint loses it — the same
// restart-loss posture as every other in-memory mutation.) This is why the old
// manual "stop → INSERT → start" workaround needed a restart: a raw DB insert
// with no in-memory row was delete-staled by the next gen-marker sweep; adding
// it to w.Structures instead makes the sweep keep it.

// ErrVillageObjectIsAlreadyStructure is returned by PromoteObjectToStructure
// when the target object already backs a Structure (its id is already a
// w.Structures key). Promoting twice would clobber the live aggregate
// (occupants, rooms, ownership), so the command refuses (→ 409 at the HTTP
// layer).
var ErrVillageObjectIsAlreadyStructure = errors.New("village object already backs a structure")

// PromoteStructureResult reports the promoted structure's id, resolved display
// name, and tags.
type PromoteStructureResult struct {
	ID          StructureID
	DisplayName string
	Tags        []string
}

// PromoteObjectToStructure returns a Command that mints a Structure sharing
// objectID (the Shared-Identity Bridge) and registers it live in w.Structures.
//
// displayName is optional: when blank it defaults to the object's current
// display name, falling back to the asset's catalog name (asset.Name is NOT
// NULL, so the resolved name is guaranteed non-empty — the structure's
// display_name is a non-empty invariant). A supplied name is validated the same
// way as SetVillageObjectDisplayName (length + control chars) so promote can't
// introduce a name the rename route would reject.
//
// tags land on the Structure (forward-compat; no engine consumer today — the
// load-bearing 'business' tag lives on the VillageObject, read there by
// move_to's open-business logic). nil/empty is fine; each element is trimmed
// and validated like an object tag.
//
// The Structure is created with NO Rooms: a plain navigable workplace needs
// none (an entered actor sits at InsideRoomID 0 = common/public scope). Rooms
// (bedrooms / staff quarters) are a separate authoring concern for buildings
// that host lodging.
func PromoteObjectToStructure(objectID VillageObjectID, displayName string, tags []string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			obj, ok := w.VillageObjects[objectID]
			if !ok {
				return nil, ErrVillageObjectNotFound
			}
			sid := StructureID(objectID)
			if _, exists := w.Structures[sid]; exists {
				return nil, ErrVillageObjectIsAlreadyStructure
			}

			name := strings.TrimSpace(displayName)
			if name == "" {
				name = strings.TrimSpace(obj.DisplayName)
			}
			if name == "" {
				if asset, ok := w.Assets[obj.AssetID]; ok && asset != nil {
					name = strings.TrimSpace(asset.Name)
				}
			}
			if name == "" {
				return nil, ErrInvalidDisplayName
			}
			if utf8.RuneCountInString(name) > MaxVillageObjectDisplayNameLen || containsControlChar(name) {
				return nil, ErrInvalidDisplayName
			}

			// Copy tags defensively and keep the slice non-nil: the checkpoint's
			// tags TEXT[] NOT NULL column rejects a nil slice (see
			// CloneStructure), and validating each element here matches the
			// object-tag route rather than letting a bad tag reach the DB.
			cleanTags := make([]string, 0, len(tags))
			for _, t := range tags {
				t = strings.TrimSpace(t)
				if t == "" || utf8.RuneCountInString(t) > MaxVillageObjectTagLen || containsControlChar(t) {
					return nil, ErrInvalidTag
				}
				cleanTags = append(cleanTags, t)
			}

			st := &Structure{
				ID:          sid,
				DisplayName: name,
				Tags:        cleanTags,
			}
			w.Structures[sid] = st
			return PromoteStructureResult{ID: sid, DisplayName: name, Tags: cleanTags}, nil
		},
	}
}
