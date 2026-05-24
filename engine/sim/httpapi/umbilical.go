package httpapi

import (
	"net/http"
	"strconv"
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

// Action-log view bounds. The action log is retention-bounded in the world
// (hours of history); the umbilical returns a tail of it, capped so a careless
// request can't serialize the whole thing.
const (
	defaultActionsLimit = 200
	maxActionsLimit     = 1000
)

// ActionLogEntryDTO is one committed agent/engine action on the wire. Unlike
// the tick telemetry (which is redacted to mechanics), this is the
// what-actually-happened trail — ActionType + the engine-authored Text + the
// HuddleID context. That content is the point: it's what surfaces an NPC that's
// ticking fine but behaving nonsensically (double-talking, speaking after
// leaving — `HuddleID` empty on a `spoke` is the tell — or oscillating between
// anchors, visible as a repeated `walked` pattern for one actor).
type ActionLogEntryDTO struct {
	ActorID    string    `json:"actor_id"`
	OccurredAt time.Time `json:"occurred_at"`
	ActionType string    `json:"action_type"`
	Text       string    `json:"text,omitempty"`
	HuddleID   string    `json:"huddle_id,omitempty"`
}

// UmbilicalActionsDTO is the GET /api/village/umbilical/actions response: a tail
// of the committed-action log (chronological, oldest-first within the window).
// Total is the full log size before filtering; Returned is how many entries
// this response carries after the optional actor filter + limit.
type UmbilicalActionsDTO struct {
	ContractVersion int                 `json:"contract_version"`
	Total           int                 `json:"total"`
	Returned        int                 `json:"returned"`
	Actions         []ActionLogEntryDTO `json:"actions"`
}

// handleUmbilicalActions serves a tail of the world's committed action log off
// the published snapshot. Query params: `actor` (optional — filter to one
// ActorID, e.g. to inspect a single NPC's recent behavior for an oscillation
// pattern), `limit` (optional — max entries, default 200, capped at 1000).
// Read-only and lock-free over the snapshot, like the other read routes.
func (s *Server) handleUmbilicalActions(w http.ResponseWriter, r *http.Request) {
	var log []sim.ActionLogEntry
	if snap := s.world.Published(); snap != nil {
		log = snap.ActionLog
	}
	total := len(log)

	q := r.URL.Query()
	if actor := q.Get("actor"); actor != "" {
		filtered := make([]sim.ActionLogEntry, 0, len(log))
		for _, e := range log {
			if string(e.ActorID) == actor {
				filtered = append(filtered, e)
			}
		}
		log = filtered
	}

	limit := parseActionsLimit(q.Get("limit"))
	// Tail: keep the most recent `limit`, preserving chronological order so a
	// per-actor scan reads left-to-right in time (the way an A→B→A oscillation
	// or a leave-then-speak sequence is easiest to spot).
	if len(log) > limit {
		log = log[len(log)-limit:]
	}

	out := UmbilicalActionsDTO{
		ContractVersion: ContractVersion,
		Total:           total,
		Returned:        len(log),
		Actions:         make([]ActionLogEntryDTO, 0, len(log)),
	}
	for _, e := range log {
		out.Actions = append(out.Actions, ActionLogEntryDTO{
			ActorID:    string(e.ActorID),
			OccurredAt: e.OccurredAt,
			ActionType: string(e.ActionType),
			Text:       e.Text,
			HuddleID:   string(e.HuddleID),
		})
	}
	writeJSON(w, out)
}

// parseActionsLimit reads the `limit` query value, clamping to (0, maxActionsLimit]
// and falling back to defaultActionsLimit when absent, unparseable, or <= 0.
func parseActionsLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultActionsLimit
	}
	if n > maxActionsLimit {
		return maxActionsLimit
	}
	return n
}
