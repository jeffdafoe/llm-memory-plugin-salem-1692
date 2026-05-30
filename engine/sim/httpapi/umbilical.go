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
	// Checkpoint is the durable-checkpoint health summary — surfaced here too
	// (not just on /checkpoint-health) because /state is the daily check-in
	// route, and consecutive_failures is the at-a-glance durability signal.
	Checkpoint sim.CheckpointHealthSnapshot `json:"checkpoint"`
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
	out := umbilicalStateFromSnapshot(s.world.Published(), s.telemetry.Stats())
	out.Checkpoint = s.checkpointHealth.Snapshot()
	writeJSON(w, out)
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

// TickerHealthEntryDTO is one interval goroutine's liveness on the wire.
type TickerHealthEntryDTO struct {
	Name     string    `json:"name"`
	Count    uint64    `json:"count"`
	LastFire time.Time `json:"last_fire"`
}

// UmbilicalTickerHealthDTO is the GET /api/village/umbilical/ticker-health
// response: per-interval-goroutine last-fire + cumulative fire count, sorted by
// name. The signal: a ticker goroutine that died or wedged stops beating, so a
// LastFire that's stale relative to that ticker's known cadence (or a Count that
// stops advancing across two polls) flags a silently-stopped cadence driver.
// `now` is the server's wall-clock at response time so the operator computes
// staleness without assuming clock alignment. The reactor evaluator is included
// for a complete view even though its liveness is also inferable from the
// telemetry-ring flow; the cascade-package internal tickers (atmosphere,
// consolidation, …) are NOT here — they fold into the separate cascade-health
// work.
type UmbilicalTickerHealthDTO struct {
	ContractVersion int                    `json:"contract_version"`
	Now             time.Time              `json:"now"`
	Tickers         []TickerHealthEntryDTO `json:"tickers"`
}

// handleUmbilicalTickerHealth serves the per-ticker liveness view off the
// world's TickerHealth registry (its own mutex — safe to read off the world
// goroutine). Read-only, like the other umbilical read routes.
func (s *Server) handleUmbilicalTickerHealth(w http.ResponseWriter, _ *http.Request) {
	entries := s.world.TickerHealthSnapshot()
	out := UmbilicalTickerHealthDTO{
		ContractVersion: ContractVersion,
		Now:             time.Now().UTC(),
		Tickers:         make([]TickerHealthEntryDTO, 0, len(entries)),
	}
	for _, e := range entries {
		out.Tickers = append(out.Tickers, TickerHealthEntryDTO{
			Name:     e.Name,
			Count:    e.Count,
			LastFire: e.LastFire,
		})
	}
	writeJSON(w, out)
}

// UmbilicalCheckpointHealthDTO is the GET /api/village/umbilical/checkpoint-health
// response: the durable-checkpoint health snapshot plus the contract version.
type UmbilicalCheckpointHealthDTO struct {
	ContractVersion int                          `json:"contract_version"`
	Health          sim.CheckpointHealthSnapshot `json:"health"`
}

// handleUmbilicalCheckpointHealth serves the durable-checkpoint health view.
// Read-only, like the other umbilical read routes. s.checkpointHealth may be
// nil if the recorder wasn't wired (Snapshot is nil-safe and returns the zero
// value), so the route never panics.
func (s *Server) handleUmbilicalCheckpointHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, UmbilicalCheckpointHealthDTO{
		ContractVersion: ContractVersion,
		Health:          s.checkpointHealth.Snapshot(),
	})
}

// umbilicalBasePath is the umbilical route prefix. The manifest lives at exactly
// this path; every other umbilical route hangs off it (basePath + "/telemetry"
// etc.).
const umbilicalBasePath = "/api/village/umbilical"

// umbilicalRoute describes one umbilical route. It is the single source of truth
// for the surface: Server.Handler iterates the table to register handlers, and
// handleUmbilicalManifest renders the same table — so a route cannot be added
// without it appearing in the manifest, and the manifest cannot claim a route
// that isn't registered. (This is deliberately unlike the old hand-written help
// blobs — e.g. the WarrantKind const list — which silently drifted.)
type umbilicalRoute struct {
	method  string
	path    string
	summary string
	control bool // true = world-mutating; armed only when controlEnabled
	handler http.HandlerFunc
}

// umbilicalRoutes returns the umbilical route table. The handler fields are bound
// method values on s, so this must be called on the live Server. Order here is
// the order routes register and the order the manifest lists them: the manifest
// itself first, then the read surface, then the control whitelist.
func (s *Server) umbilicalRoutes() []umbilicalRoute {
	return []umbilicalRoute{
		{http.MethodGet, umbilicalBasePath, "Self-describing manifest of the currently-armed umbilical routes (this endpoint).", false, s.handleUmbilicalManifest},

		// Read surface — always armed when the umbilical is enabled.
		{http.MethodGet, umbilicalBasePath + "/telemetry", "Dump the tick-telemetry ring buffer (redacted per-tick lifecycle records, oldest first) with retention accounting.", false, s.handleUmbilicalTelemetry},
		{http.MethodGet, umbilicalBasePath + "/telemetry/summary", "Rolled-up telemetry rates: counts by kind / terminal status / LLM error class, plus mean and p95 tick duration.", false, s.handleUmbilicalTelemetrySummary},
		{http.MethodGet, umbilicalBasePath + "/state", "Coarse engine introspection: phase/tick, in-flight tick count, and per-table entity counts off the published snapshot.", false, s.handleUmbilicalState},
		{http.MethodGet, umbilicalBasePath + "/actions", "Tail of the committed action log (behavioral trail). Query params: actor, limit.", false, s.handleUmbilicalActions},
		{http.MethodGet, umbilicalBasePath + "/agent", "One actor's full live picture: needs, position, inventory, rest windows, reactor/warrant state, in-flight move target, recent ticks and actions. Query param: id (required).", false, s.handleUmbilicalAgent},
		{http.MethodGet, umbilicalBasePath + "/agent/prompts", "One actor's recent RENDERED DELIBERATION PROMPTS (what it actually perceived per tick), raw text, oldest first. Query params: id (required), limit (optional, default all retained). Empty when prompt capture is off.", false, s.handleUmbilicalAgentPrompts},
		{http.MethodGet, umbilicalBasePath + "/reactor", "Tick-eligibility across all actors: warranted / due-now / in-flight / idle counts plus the queued-actor list.", false, s.handleUmbilicalReactor},
		{http.MethodGet, umbilicalBasePath + "/ticker-health", "Per-interval-goroutine liveness: last-fire time and cumulative fire count for each cadence driver.", false, s.handleUmbilicalTickerHealth},
		{http.MethodGet, umbilicalBasePath + "/checkpoint-health", "Durable-checkpoint health: last success/failure/attempt times, consecutive-failure streak, totals, and last error. A non-zero consecutive_failures or a stale last_success_at means durability is broken.", false, s.handleUmbilicalCheckpointHealth},
		{http.MethodGet, umbilicalBasePath + "/errors", "Recent non-2xx responses the engine returned (server-observed) for remote visibility into client-facing failures.", false, s.handleUmbilicalErrors},
		{http.MethodGet, umbilicalBasePath + "/client-errors", "Client-reported (untrusted) runtime-error feed beaconed by the Godot client.", false, s.handleUmbilicalClientErrors},
		{http.MethodGet, umbilicalBasePath + "/deadlocks", "Recent locomotion soft-block deadlock hard-stops (mover + occupant + whether re-plan found no detour) for remote visibility into live freeze frequency.", false, s.handleUmbilicalDeadlocks},
		{http.MethodGet, umbilicalBasePath + "/actors", "Full actor roster with live needs (who's starving/exhausted) — the companion read for picking reset-needs targets.", false, s.handleUmbilicalActors},

		// Control whitelist — world-mutating; armed only when control is also enabled.
		{http.MethodPost, umbilicalBasePath + "/nudge", "Force a reactor tick for one actor, optionally injecting an in-world felt-impulse directive. Body: {actor_id, message?}.", true, s.handleUmbilicalNudge},
		{http.MethodPost, umbilicalBasePath + "/phase", "Force a day/night phase transition. Body: {phase}.", true, s.handleUmbilicalPhase},
		{http.MethodPost, umbilicalBasePath + "/settle", "Clear one actor's pending warrant cycle (stop a spiraling NPC). Body: {actor_id}.", true, s.handleUmbilicalSettle},
		{http.MethodPost, umbilicalBasePath + "/rotate", "Force a daily-rotation pass. Body: {tag?}.", true, s.handleUmbilicalRotate},
		{http.MethodPost, umbilicalBasePath + "/settings/need-threshold", "Live-tune one need's red-line threshold (ephemeral; resets on restart). Body: {need, value}.", true, s.handleUmbilicalNeedThreshold},
		{http.MethodPost, umbilicalBasePath + "/grant", "Give or claw back coins/items to/from any actor. Body: {actor_id, coins?, items?}.", true, s.handleUmbilicalGrant},
		{http.MethodPost, umbilicalBasePath + "/reset-needs", "Reset an actor's needs (hunger/thirst/tiredness) to 0 — fully satisfied. Body: {actor_id} for one, or {all:true} for every actor.", true, s.handleUmbilicalResetNeeds},
	}
}

// UmbilicalRouteDTO is one route on the manifest wire.
type UmbilicalRouteDTO struct {
	Path    string `json:"path"`
	Method  string `json:"method"`
	Summary string `json:"summary"`
	Control bool   `json:"control"`
}

// UmbilicalManifestDTO is the GET /api/village/umbilical response: the in-band,
// runtime-aware description of the surface. The thing a static codebase note
// can't report is exactly what this carries — whether control is armed on THIS
// deploy and which routes are therefore actually live. `enabled` is always true
// in a served response (the route only registers when the umbilical is on; when
// off the operator gets a 404, which is itself the answer).
type UmbilicalManifestDTO struct {
	ContractVersion int                 `json:"contract_version"`
	Enabled         bool                `json:"enabled"`
	ControlEnabled  bool                `json:"control_enabled"`
	Routes          []UmbilicalRouteDTO `json:"routes"`
}

// handleUmbilicalManifest renders the route table, filtered to what is actually
// armed right now: read routes always (the umbilical is on or this handler
// wouldn't be registered), control routes only when controlEnabled — the same
// filter Server.Handler applies at registration, so the manifest matches the
// live mux exactly.
func (s *Server) handleUmbilicalManifest(w http.ResponseWriter, _ *http.Request) {
	routes := s.umbilicalRoutes()
	out := UmbilicalManifestDTO{
		ContractVersion: ContractVersion,
		Enabled:         true,
		ControlEnabled:  s.controlEnabled,
		Routes:          make([]UmbilicalRouteDTO, 0, len(routes)),
	}
	for _, rt := range routes {
		if rt.control && !s.controlEnabled {
			continue
		}
		out.Routes = append(out.Routes, UmbilicalRouteDTO{
			Path:    rt.path,
			Method:  rt.method,
			Summary: rt.summary,
			Control: rt.control,
		})
	}
	writeJSON(w, out)
}
