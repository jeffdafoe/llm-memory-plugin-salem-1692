package httpapi

import (
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// umbilical.go — the read half of the out-of-band debug/control surface. These
// routes are NOT part of the player/client contract; they exist for an operator
// (work / home / jeff) to introspect a running engine over the standard HTTP
// server. Two gates stand between a caller and these handlers:
//
//   1. requireOperator (auth.go): a valid salem-realm token PLUS the llm-memory
//      plugins/administer capability — tighter than the normal read gate, since
//      every player is salem-realm but only operators hold plugins/administer.
//   2. Registration is conditional on SetTelemetry having been called, which
//      cmd/engine does only under UMBILICAL_ENABLED. Off by default → no route.
//
// The read surface is strictly additive and never a driver: it reads the same
// lock-free published snapshot the client routes read, plus the in-memory
// telemetry ring. It cannot influence the simulation (that's the control half,
// built separately and whitelisted). The invariant holds — the engine is fully
// correct with the umbilical disconnected.

// TelemetryRecordDTO is one buffered tick-lifecycle record on the wire. Mirrors
// sim.TickTelemetryRecord; Detail is the structured + REDACTED detail map (the
// sink contract guarantees no raw prompts / LLM responses / private text ever
// land in it), omitted when empty.
type TelemetryRecordDTO struct {
	At        time.Time         `json:"at"`
	ActorID   string            `json:"actor_id,omitempty"`
	AttemptID string            `json:"attempt_id,omitempty"`
	Kind      string            `json:"kind"`
	Detail    map[string]string `json:"detail,omitempty"`
}

// TelemetryStatsDTO is the ring-buffer accounting: how much history is retained
// and whether the buffer is saturating (dropped climbing → reader behind the
// retention window, not an error).
type TelemetryStatsDTO struct {
	Capacity int    `json:"capacity"`
	Size     int    `json:"size"`
	Written  uint64 `json:"written"`
	Dropped  uint64 `json:"dropped"`
}

// UmbilicalTelemetryDTO is the GET /api/village/umbilical/telemetry response:
// the ring's accounting plus the buffered records, oldest first.
type UmbilicalTelemetryDTO struct {
	ContractVersion int                  `json:"contract_version"`
	Stats           TelemetryStatsDTO    `json:"stats"`
	Records         []TelemetryRecordDTO `json:"records"`
}

// UmbilicalStateDTO is the GET /api/village/umbilical/state response: a coarse
// introspection of the running engine off the published snapshot. World embeds
// the same coarse world DTO the client /world route serves; the rest is
// operator-only debug detail (in-flight tick count, entity-table sizes,
// telemetry accounting).
type UmbilicalStateDTO struct {
	ContractVersion int                `json:"contract_version"`
	PublishedAt     time.Time          `json:"published_at"`
	World           WorldStateDTO      `json:"world"`
	TicksInFlight   int                `json:"ticks_in_flight"`
	Counts          UmbilicalCountsDTO `json:"counts"`
	Telemetry       TelemetryStatsDTO  `json:"telemetry"`
}

// UmbilicalCountsDTO is the size of each published entity table — a cheap
// "what's loaded right now" view for an operator, derived purely from the
// snapshot (no new plumbing).
type UmbilicalCountsDTO struct {
	Actors         int `json:"actors"`
	Huddles        int `json:"huddles"`
	Scenes         int `json:"scenes"`
	Structures     int `json:"structures"`
	Orders         int `json:"orders"`
	VillageObjects int `json:"village_objects"`
	Quotes         int `json:"quotes"`
	PayLedger      int `json:"pay_ledger"`
	ActionLog      int `json:"action_log"`
	PriceBook      int `json:"price_book"`
}

// handleUmbilicalTelemetry dumps the tick-telemetry ring (oldest first) with
// its accounting. Gated by requireOperator + registered only when the ring is
// attached, so s.telemetry is never nil here.
func (s *Server) handleUmbilicalTelemetry(w http.ResponseWriter, _ *http.Request) {
	recs := s.telemetry.Snapshot()
	out := UmbilicalTelemetryDTO{
		ContractVersion: ContractVersion,
		Stats:           telemetryStatsDTO(s.telemetry.Stats()),
		Records:         make([]TelemetryRecordDTO, 0, len(recs)),
	}
	for _, r := range recs {
		out.Records = append(out.Records, TelemetryRecordDTO{
			At:        r.At,
			ActorID:   string(r.ActorID),
			AttemptID: string(r.AttemptID),
			Kind:      r.Kind,
			Detail:    r.Detail,
		})
	}
	writeJSON(w, out)
}

// handleUmbilicalState serves a coarse introspection of the running engine off
// the published snapshot plus the telemetry ring's accounting.
func (s *Server) handleUmbilicalState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, umbilicalStateFromSnapshot(s.world.Published(), s.telemetry.Stats()))
}

// umbilicalStateFromSnapshot maps the published snapshot + ring stats to the
// state DTO. A nil snapshot (engine published nothing yet) yields a zero-valued
// world/counts view rather than panicking.
func umbilicalStateFromSnapshot(s *sim.Snapshot, st telemetry.Stats) UmbilicalStateDTO {
	out := UmbilicalStateDTO{
		ContractVersion: ContractVersion,
		Telemetry:       telemetryStatsDTO(st),
	}
	if s == nil {
		return out
	}
	out.PublishedAt = s.PublishedAt
	out.World = worldStateFromSnapshot(s)
	out.TicksInFlight = countTicksInFlight(s)
	out.Counts = UmbilicalCountsDTO{
		Actors:         len(s.Actors),
		Huddles:        len(s.Huddles),
		Scenes:         len(s.Scenes),
		Structures:     len(s.Structures),
		Orders:         len(s.Orders),
		VillageObjects: len(s.VillageObjects),
		Quotes:         len(s.Quotes),
		PayLedger:      len(s.PayLedger),
		ActionLog:      len(s.ActionLog),
		PriceBook:      len(s.PriceBook),
	}
	return out
}

// countTicksInFlight counts actors mid-tick (an LLM tick dispatched but not yet
// resolved) in the snapshot — the headline "is the engine busy" debug signal.
func countTicksInFlight(s *sim.Snapshot) int {
	n := 0
	for _, a := range s.Actors {
		if a != nil && a.TickInFlight {
			n++
		}
	}
	return n
}

func telemetryStatsDTO(st telemetry.Stats) TelemetryStatsDTO {
	return TelemetryStatsDTO{
		Capacity: st.Capacity,
		Size:     st.Size,
		Written:  st.Written,
		Dropped:  st.Dropped,
	}
}
