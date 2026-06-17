package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_structures.go — ZBBS-WORK-433. The /api/village/umbilical/structures
// operator read route: the village establishment map off the published snapshot
// (lock-free + race-free, the /pay-ledger pattern). Answers "who runs the Inn /
// the Blacksmith, on what schedule, on shift right now?" — which previously meant
// SSHing the zbbs DB and joining actor.work_structure_id to structure. Neither
// /actors (per-actor needs) nor /state (table counts) maps a structure to its
// keeper; this is the missing structure-keyed view.
//
// scope (query param, default "keepered"):
//   - keepered: only structures with at least one keeper (the "who runs X" case)
//   - all:      every structure, keepered ones first (surfaces a structure that
//     SHOULD have a keeper but doesn't)
//
// An unknown scope is a 400, consistent with the other umbilical params.

const (
	structuresScopeKeepered = "keepered"
	structuresScopeAll      = "all"
)

// UmbilicalStructureKeeperDTO is one keeper — an actor whose WorkStructureID is
// this structure — on the roster. on_shift is computed against the snapshot's
// village clock (LocalMinuteOfDay) with the engine's isActorOnShift semantics
// (unscheduled = always off); it is false when the snapshot carries no clock.
type UmbilicalStructureKeeperDTO struct {
	ActorID       string `json:"actor_id"`
	DisplayName   string `json:"display_name"`
	LLMAgent      string `json:"llm_memory_agent,omitempty"`
	ScheduleStart *int   `json:"schedule_start_minute,omitempty"`
	ScheduleEnd   *int   `json:"schedule_end_minute,omitempty"`
	OnShift       bool   `json:"on_shift"`
	State         string `json:"state"`
}

// UmbilicalStructureRoomsDTO is the per-structure room tally by kind plus the
// count of private rooms currently held by an active lodging ledger grant.
// PrivateOccupied uses the same IsActiveLedgerGrant predicate as the keeper
// lodging perception (buildKeeperLodgingView), so the umbilical and the in-world
// vacancy cue can't disagree.
type UmbilicalStructureRoomsDTO struct {
	Common          int `json:"common"`
	Private         int `json:"private"`
	Staff           int `json:"staff"`
	PrivateOccupied int `json:"private_occupied"`
}

// UmbilicalStructureRowDTO is one structure on the roster.
type UmbilicalStructureRowDTO struct {
	ID          string                        `json:"id"`
	DisplayName string                        `json:"display_name"`
	Tags        []string                      `json:"tags"`
	Keepers     []UmbilicalStructureKeeperDTO `json:"keepers"`
	Rooms       UmbilicalStructureRoomsDTO    `json:"rooms"`
}

// UmbilicalStructuresDTO is the GET /api/village/umbilical/structures response.
// LocalMinuteOfDay is the snapshot's village-clock minute (nil before settings
// load a timezone); it is the basis for every keeper's on_shift, surfaced so the
// operator can see the clock the shift flags were computed against. PublishedAt
// is the snapshot freshness stamp.
type UmbilicalStructuresDTO struct {
	ContractVersion  int                        `json:"contract_version"`
	Scope            string                     `json:"scope"`
	PublishedAt      time.Time                  `json:"published_at"`
	LocalMinuteOfDay *int                       `json:"local_minute_of_day,omitempty"`
	Total            int                        `json:"total"`
	Structures       []UmbilicalStructureRowDTO `json:"structures"`
}

// handleUmbilicalStructures serves the establishment roster off the published
// snapshot. Query param scope (keepered|all, default keepered); an unknown value
// is a 400. Read-only and lock-free, like the other umbilical read routes.
func (s *Server) handleUmbilicalStructures(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = structuresScopeKeepered
	}
	if scope != structuresScopeKeepered && scope != structuresScopeAll {
		writeError(w, http.StatusBadRequest, "scope must be keepered or all")
		return
	}
	writeJSON(w, umbilicalStructuresFromSnapshot(s.world.Published(), scope))
}

// umbilicalStructuresFromSnapshot maps the published snapshot to the structures
// roster for the given (already-validated) scope. Pure (no Server/world access)
// so it's unit-testable against a hand-built snapshot. A nil snapshot yields an
// empty roster, not a panic.
func umbilicalStructuresFromSnapshot(snap *sim.Snapshot, scope string) UmbilicalStructuresDTO {
	out := UmbilicalStructuresDTO{
		ContractVersion: ContractVersion,
		Scope:           scope,
		Structures:      []UmbilicalStructureRowDTO{},
	}
	if snap == nil {
		return out
	}
	out.PublishedAt = snap.PublishedAt
	out.LocalMinuteOfDay = clonePtrInt(snap.LocalMinuteOfDay)

	// Keepers grouped by their work structure. ActorSnapshot has no ID field (the
	// id is the map key), so the keeper DTO is built here where the key is in
	// hand. on_shift is computed against the snapshot's village clock.
	keepers := map[sim.StructureID][]UmbilicalStructureKeeperDTO{}
	for id, a := range snap.Actors {
		if a == nil || a.WorkStructureID == "" {
			continue
		}
		k := UmbilicalStructureKeeperDTO{
			ActorID:       string(id),
			DisplayName:   a.DisplayName,
			LLMAgent:      a.LLMAgent,
			ScheduleStart: clonePtrInt(a.ScheduleStartMin),
			ScheduleEnd:   clonePtrInt(a.ScheduleEndMin),
			State:         string(a.State),
		}
		if snap.LocalMinuteOfDay != nil {
			k.OnShift = sim.OnShiftAtMinute(a.ScheduleStartMin, a.ScheduleEndMin, *snap.LocalMinuteOfDay)
		}
		keepers[a.WorkStructureID] = append(keepers[a.WorkStructureID], k)
	}

	// Private rooms currently held by an active lodging ledger grant, across all
	// actors — the same scan + predicate the keeper lodging perception uses.
	// RoomIDs are globally unique, so a structure's private_occupied is just how
	// many of its private rooms fall in this set.
	occupied := map[sim.RoomID]bool{}
	for _, a := range snap.Actors {
		if a == nil {
			continue
		}
		for _, ra := range a.RoomAccess {
			if sim.IsActiveLedgerGrant(ra, snap.PublishedAt) {
				occupied[ra.RoomID] = true
			}
		}
	}

	for id, st := range snap.Structures {
		if st == nil {
			continue
		}
		ks := keepers[id]
		if scope == structuresScopeKeepered && len(ks) == 0 {
			continue
		}
		sort.Slice(ks, func(i, j int) bool { return ks[i].ActorID < ks[j].ActorID })

		rooms := UmbilicalStructureRoomsDTO{}
		for _, rm := range st.Rooms {
			if rm == nil {
				continue
			}
			switch rm.Kind {
			case sim.RoomKindCommon:
				rooms.Common++
			case sim.RoomKindPrivate:
				rooms.Private++
				if occupied[rm.ID] {
					rooms.PrivateOccupied++
				}
			case sim.RoomKindStaff:
				rooms.Staff++
			}
		}

		out.Structures = append(out.Structures, UmbilicalStructureRowDTO{
			ID:          string(id),
			DisplayName: st.DisplayName,
			Tags:        append([]string{}, st.Tags...),
			Keepers:     ks,
			Rooms:       rooms,
		})
	}

	out.Total = len(out.Structures)
	// Deterministic order: keepered structures first (the headline "who runs X"),
	// then by id. For scope=keepered every row has a keeper, so this is just id
	// order; for scope=all it floats the keepered establishments to the top.
	sort.Slice(out.Structures, func(i, j int) bool {
		ki := len(out.Structures[i].Keepers) > 0
		kj := len(out.Structures[j].Keepers) > 0
		if ki != kj {
			return ki
		}
		return out.Structures[i].ID < out.Structures[j].ID
	})
	return out
}

// clonePtrInt copies a *int so a returned DTO never aliases the snapshot's
// pointer (the *int counterpart to clonePtrTime). nil stays nil → the field
// omits.
func clonePtrInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
