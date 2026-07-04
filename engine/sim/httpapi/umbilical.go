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
	// WS is the event-hub delivery accounting (WORK-434) — frame-drop /
	// slow-consumer / connected-client health. Surfaced here because /state is
	// the daily check-in route and a silent live-frame drop (the suspected cause
	// of stale-on-client noticeboards) is otherwise invisible off the box. Zero
	// when no event hub is attached (headless/test).
	WS WSDeliveryStatsDTO `json:"ws"`
	// ConfigWarnings (LLM-60) is the live data-config audit: one line per village
	// object that is misconfigured in a tolerated-but-wrong way (today: a gather/
	// eat source with no display_name, which the resolver silently can't reach).
	// Computed off the snapshot on every read via sim.ConfigWarnings, so it
	// reflects live edits, not just boot state. Omitted when clean.
	ConfigWarnings []string `json:"config_warnings,omitempty"`
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
	if s.hub != nil {
		out.WS = s.hub.Stats()
	}
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
	out.ConfigWarnings = sim.ConfigWarnings(s.VillageObjects)
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
		{http.MethodGet, umbilicalBasePath + "/chat", "One scene's engine<->model exchange: the rendered perception (tx) and the model's responses + tool calls (rx) for that scene_id, oldest first. Query params: scene (required), limit (optional, default all retained). Empty when chat capture is off.", false, s.handleUmbilicalChat},
		{http.MethodGet, umbilicalBasePath + "/reactor", "Tick-eligibility across all actors: warranted / due-now / in-flight / idle counts plus the queued-actor list.", false, s.handleUmbilicalReactor},
		{http.MethodGet, umbilicalBasePath + "/ticker-health", "Per-interval-goroutine liveness: last-fire time and cumulative fire count for each cadence driver.", false, s.handleUmbilicalTickerHealth},
		{http.MethodGet, umbilicalBasePath + "/checkpoint-health", "Durable-checkpoint health: last success/failure/attempt times, consecutive-failure streak, totals, and last error. A non-zero consecutive_failures or a stale last_success_at means durability is broken.", false, s.handleUmbilicalCheckpointHealth},
		{http.MethodGet, umbilicalBasePath + "/errors", "Recent non-2xx responses the engine returned (server-observed) for remote visibility into client-facing failures.", false, s.handleUmbilicalErrors},
		{http.MethodGet, umbilicalBasePath + "/client-errors", "Client-reported (untrusted) runtime-error feed beaconed by the Godot client.", false, s.handleUmbilicalClientErrors},
		{http.MethodGet, umbilicalBasePath + "/deadlocks", "Recent locomotion soft-block deadlock hard-stops (mover + occupant + whether re-plan found no detour) for remote visibility into live freeze frequency.", false, s.handleUmbilicalDeadlocks},
		{http.MethodGet, umbilicalBasePath + "/actors", "Full actor roster with live needs (who's starving/exhausted) — the companion read for picking set-needs targets.", false, s.handleUmbilicalActors},
		{http.MethodGet, umbilicalBasePath + "/structures", "Establishment roster off the published snapshot: per structure its keeper(s) (VA, schedule, on-shift, state) and room tally (common/private/staff + private-occupied). Query param: scope=keepered (default) | all.", false, s.handleUmbilicalStructures},
		{http.MethodGet, umbilicalBasePath + "/objects", "Placed village objects off the published snapshot (read side of the object/* control routes): per object its asset, position (world-pixel + tile), display-name, state, owner, entry-policy, loiter-offset, tags, refresh-policy, attached-to, and whether it backs a structure. Query filters (all optional): id, owner, tag, structure.", false, s.handleUmbilicalObjects},
		{http.MethodGet, umbilicalBasePath + "/pay-ledger", "Live pay-ledger off the published snapshot (most-recent first): per-entry buyer/seller/CONSUMER split, item, qty, coins offered, consume_now, state, timestamps. Reads in-memory state the checkpointed DB lags. Query param: limit (optional).", false, s.handleUmbilicalPayLedger},
		{http.MethodGet, umbilicalBasePath + "/sell-through", "Per-(seller, item) recent sell-through off the published snapshot's price book — the demand + weekly-P&L signal the reseller restock cue reasons against: units sold, sale count, sales coins, buy cost (restocking spend), distinct buyers over a trailing window, highest-throughput first. Query params: actor (filter to one seller), item, window_hours (default 168).", false, s.handleUmbilicalSellThrough},
		{http.MethodGet, umbilicalBasePath + "/turns", "Raw LLM turn(s) for an NPC straight off memory-api: the composed system_prompt, the perception sent, the model's response, token counts, cost, and provider status/error. Proxied with the operator's own token (the full turn lives only in memory-api, never in the engine). Query params: scene, agent, conversation (a hud-<hex> huddle id), since, status, limit (all optional).", false, s.handleUmbilicalTurns},
		{http.MethodGet, umbilicalBasePath + "/huddles", "List ACTIVE huddles (live conversations): members with per-member recent-utterance counts, structure, started/last-activity times — spot a stuck or one-sided huddle at a glance. In-memory, so only the currently-running engine's huddles.", false, s.handleUmbilicalHuddles},
		{http.MethodGet, umbilicalBasePath + "/huddle", "One huddle's detail: members (with per-member recent-utterance counts) + the recent-conversation ring (oldest first). conversation_id pivots to /turns?conversation= for the full raw turns. Query param: id (required). In-memory: a just-concluded huddle is still fetchable by id while retained (until the next boot clears it); for a durable past-huddle lookup use /turns?conversation= (raw LLM turns) or /transcript?huddle= (committed both-speaker transcript).", false, s.handleUmbilicalHuddle},
		{http.MethodGet, umbilicalBasePath + "/transcript", "Complete durable committed-action transcript of one huddle (every participant — agent, player, engine — oldest-first) read from agent_action_log: the durable companion to the retention-bounded /huddle ring. Query param: huddle (required).", false, s.handleUmbilicalTranscript},
		{http.MethodGet, umbilicalBasePath + "/settlements", "Durable accepted pay-with-item settlements off the agent_action_log 'paid' beat, most-recent first — the audit lens for 'did a free-food settlement happen' (each row carries coins + barter goods + a `free` flag, so a give-away is unambiguous). Reaches settlements that happened outside a huddle, unlike /transcript. Optional query params: actor (buyer id), since, until (RFC3339), ledger (a ledger id), limit. Rows from before LLM-105 carry has_legacy=true (no goods leg recorded).", false, s.handleUmbilicalSettlements},
		{http.MethodGet, umbilicalBasePath + "/recipes", "Live item-recipe catalog (read side of recipe/set): per recipe its output batch, production rate, inputs, optional boost_inputs, and wholesale/retail price. Query param: item (filter to one, case-insensitive against the canonical catalog key).", false, s.handleUmbilicalRecipes},
		{http.MethodGet, umbilicalBasePath + "/items", "Live item catalog (read side of item/set-satisfies): per item kind its label, category, capabilities, eat-here-only flag, and per-need satiation entries (immediate amount + dwell triple where authored). Production rate/inputs/price live on /recipes. Query param: item (filter to one, case-insensitive against the canonical catalog key).", false, s.handleUmbilicalItems},
		{http.MethodGet, umbilicalBasePath + "/settings", "Live operator-tunable world settings: per-need red-line thresholds (read side of settings/need-threshold; ephemeral) + the huddle loop-sweep knobs (read side of settings/huddle-loop; persisted) with an enabled flag + the seek-work coin ceiling (read side of settings/seek-work-ceiling; persisted) + the farm wealth-tax knobs (read side of farm-upkeep/set; persisted) + the labor produce boost (read side of settings/labor-produce-boost; persisted).", false, s.handleUmbilicalSettings},

		// Control whitelist — world-mutating; armed only when control is also enabled.
		{http.MethodPost, umbilicalBasePath + "/nudge", "Force a reactor tick for one actor, optionally injecting an in-world felt-impulse directive. Body: {actor_id, message?}.", true, s.handleUmbilicalNudge},
		{http.MethodPost, umbilicalBasePath + "/phase", "Force a day/night phase transition. Body: {phase}.", true, s.handleUmbilicalPhase},
		{http.MethodPost, umbilicalBasePath + "/weather", "Force the world weather to storm or clear on demand — ungated by PC presence, so it works on an empty village for demo/testing (LLM-117). Body: {weather}.", true, s.handleUmbilicalWeather},
		{http.MethodPost, umbilicalBasePath + "/settle", "Clear one actor's pending warrant cycle (stop a spiraling NPC). Body: {actor_id}.", true, s.handleUmbilicalSettle},
		{http.MethodPost, umbilicalBasePath + "/rotate", "Force a daily-rotation pass. Body: {tag?}.", true, s.handleUmbilicalRotate},
		{http.MethodPost, umbilicalBasePath + "/settings/need-threshold", "Live-tune one need's red-line threshold (ephemeral; resets on restart). Body: {need, value}.", true, s.handleUmbilicalNeedThreshold},
		{http.MethodPost, umbilicalBasePath + "/settings/huddle-loop", "Live-tune the huddle conversational-loop sweep (LLM-159): master enable + thresholds, PERSISTED across restart. All fields optional, at least one required. Body: {timeout_seconds? (0 disables the sweep), repeat_percent? (1-100), cadence_seconds?}.", true, s.handleUmbilicalHuddleLoop},
		{http.MethodPost, umbilicalBasePath + "/settings/seek-work-ceiling", "Live-tune the seek-work coin ceiling (LLM-194): a workless worker stops seeking/soliciting work at/above this coin balance and drains its purse via ordinary consumption. PERSISTED across restart. Body: {coin_ceiling (>=1)}.", true, s.handleUmbilicalSeekWorkCeiling},
		{http.MethodPost, umbilicalBasePath + "/settings/seek-work-need-margin", "Live-tune the seek-work→eat redirect margin (LLM-276): a workless idle worker whose hunger/thirst sits within this many points below its red-line, and who can resolve it now (carries food, holds coin, or a free source is nearby), is woken to eat/drink instead of to seek work. PERSISTED across restart. Body: {margin (>=1)}.", true, s.handleUmbilicalSeekWorkNeedMargin},
		{http.MethodPost, umbilicalBasePath + "/settings/labor-produce-boost", "Live-tune the per-worker produce boost (LLM-224): each worker laboring at their employer's establishment adds this percent of the keeper's base rate to the produce tick, so a wage buys real output. PERSISTED across restart. Body: {boost_pct (>=0; 0 disables)}.", true, s.handleUmbilicalLaborBoost},
		{http.MethodPost, umbilicalBasePath + "/grant", "Give or claw back coins/items to/from any actor. Body: {actor_id, coins?, items?}.", true, s.handleUmbilicalGrant},
		{http.MethodPost, umbilicalBasePath + "/set-needs", "Set an actor's needs to ABSOLUTE values [0..24]. Body: {actor_id} or {all:true}, plus {needs:{\"hunger\":20,\"tiredness\":0}} (unlisted needs untouched). Omit needs to set every need to 0 (back-to-0 shortcut). Setting tiredness to 0 also clears the actor's rest window.", true, s.handleUmbilicalSetNeeds},
		{http.MethodPost, umbilicalBasePath + "/set-position", "Teleport an actor to a walkable TILE coordinate (the units /actors reports). Cancels any in-flight walk, reconciles inside-structure attribution, and removes the actor from a huddle it was displaced away from. Unwalkable/out-of-bounds targets are refused. Body: {actor_id, x, y}.", true, s.handleUmbilicalSetPosition},
		{http.MethodPost, umbilicalBasePath + "/route", "Force a schedule-driven NPC route (town crier / washerwoman) to dispatch NOW, bypassing the schedule-window gate — reproduce a crier tour on demand instead of waiting for a boundary or restart. Does NOT consume the real schedule boundary. Body: {attr, start?}.", true, s.handleUmbilicalRoute},
		{http.MethodPost, umbilicalBasePath + "/worker/provision", "Mint a sprite-only DECORATIVE into a live Worker: assign a backing VA (default salem-vendor), grant the `worker` attribute, and reclassify its Kind in memory so it comes online and takes solicit_work jobs WITHOUT a restart (the checkpoint persists the link+attribute for the next reload). Refuses an already-live NPC (409). An unscheduled worker is then day-active on the world dawn/dusk window automatically (LLM-137). Body: {actor_id, agent?}.", true, s.handleUmbilicalProvisionWorker},
		{http.MethodPost, umbilicalBasePath + "/worker/retire", "Retire an actor from Worker duty (inverse of worker/provision): remove the `worker` attribute so the seek-work backstop + solicit_work no longer engage it — live, no restart (removing an attribute doesn't change Kind, so no reclassify and no race). With {to_decorative:true} it ALSO unlinks the VA, reclassifies to decorative, and resets reactor state so the actor goes fully inert (zero LLM cost; re-provision to bring it back). Body: {actor_id, to_decorative?}.", true, s.handleUmbilicalRetireWorker},

		// Object lifecycle (LLM-61) — live add/edit/remove of placed village objects.
		// The operator-gated counterparts to /admin/object/* (same sim Commands,
		// without the in-world admin-actor gate operators can't pass).
		{http.MethodPost, umbilicalBasePath + "/object/create", "Place a new village object (operator live-ops). World-pixel position. Body: {asset_id, x, y, attached_to?}.", true, s.handleUmbilicalObjectCreate},
		{http.MethodPost, umbilicalBasePath + "/object/move", "Reposition a placed village object to a new world-pixel anchor. Body: {object_id, x, y}.", true, s.handleUmbilicalObjectMove},
		{http.MethodPost, umbilicalBasePath + "/object/delete", "Remove a placed village object and its attached overlays (refused for a structure-backed object). Body: {object_id}.", true, s.handleUmbilicalObjectDelete},
		{http.MethodPost, umbilicalBasePath + "/object/set-display-name", "Set or clear a placed object's display-name override (e.g. name a nameless gather/eat source live). Body: {object_id, display_name}.", true, s.handleUmbilicalObjectSetDisplayName},
		{http.MethodPost, umbilicalBasePath + "/object/set-state", "Set a placed object's current_state (free-form catalog state; unknown renders as the asset fallback). Body: {object_id, state}.", true, s.handleUmbilicalObjectSetState},
		{http.MethodPost, umbilicalBasePath + "/object/set-owner", "Set or clear a placed object's owning actor (empty owner_actor_id clears). Body: {object_id, owner_actor_id}.", true, s.handleUmbilicalObjectSetOwner},
		{http.MethodPost, umbilicalBasePath + "/object/set-loiter-offset", "Set or clear a placed object's loiter offset (both x,y or neither). Body: {object_id, x?, y?}.", true, s.handleUmbilicalObjectSetLoiterOffset},
		{http.MethodPost, umbilicalBasePath + "/object/set-entry-policy", "Set a placed object's entry policy (\"\", open, owner-only, closed). Body: {object_id, entry_policy}.", true, s.handleUmbilicalObjectSetEntryPolicy},
		{http.MethodPost, umbilicalBasePath + "/object/add-tag", "Add a per-instance tag to a placed object (idempotent). Body: {object_id, tag}.", true, s.handleUmbilicalObjectAddTag},
		{http.MethodPost, umbilicalBasePath + "/object/remove-tag", "Remove a per-instance tag from a placed object (idempotent). Body: {object_id, tag}.", true, s.handleUmbilicalObjectRemoveTag},
		{http.MethodPost, umbilicalBasePath + "/object/set-refresh", "Replace a placed object's refresh-policy set wholesale (empty rows clears; the partner to set-display-name for fixing a gather/eat source live). Body: {object_id, rows}.", true, s.handleUmbilicalObjectSetRefresh},

		// Restock policy (LLM-95) — live per-entry edit of what an NPC
		// produces / restocks / forages at work. Mutates the actor's attribute
		// params and re-projects RestockPolicy; durability rides the attribute
		// checkpoint.
		{http.MethodPost, umbilicalBasePath + "/restock/set", "Add or update one restock entry on an actor (produce/buy/forage). Validates the item exists; a produce entry requires a recipe. Body: {actor_id, item, source, cap}.", true, s.handleUmbilicalRestockSet},
		{http.MethodPost, umbilicalBasePath + "/restock/remove", "Remove one restock entry (by item) from an actor. Body: {actor_id, item}.", true, s.handleUmbilicalRestockRemove},

		// Stall wear (LLM-118) — live-tune the market-stall wear/repair knobs
		// without a restart; applied in memory and persisted on the next checkpoint.
		{http.MethodPost, umbilicalBasePath + "/stall-wear/set", "Live-tune the stall wear knobs (LLM-118) without a restart. All fields optional (at least one required), non-negative ints. Body: {stall_wear_per_coin?, stall_wear_repair_threshold?, stall_wear_degrade_threshold?, stall_nails_per_repair?, stall_repair_duration_seconds?}.", true, s.handleUmbilicalStallWearSet},

		// Farm upkeep wealth tax (LLM-215) — live-tune the per-farm shovel levy
		// without a restart; applied in memory and persisted on the next checkpoint.
		{http.MethodPost, umbilicalBasePath + "/farm-upkeep/set", "Live-tune the farm wealth-tax knobs (LLM-215) without a restart. Both fields optional (at least one required), non-negative ints; farm_upkeep_coins_per_shovel=0 disables the feature. Body: {farm_upkeep_floor?, farm_upkeep_coins_per_shovel?}.", true, s.handleUmbilicalFarmUpkeepSet},

		// Item definition (LLM-200) — live create/edit of one item_kind row (the
		// definition: label, category, sort order, capabilities, counting nouns,
		// dwell narration). The create leg of the all-live new-good flow
		// (item/set → recipe/set → restock/set), so unlike the other two it can
		// introduce a brand-new good. Durable write to item_kind + in-memory
		// catalog update; needs SetItemKindWriter wired (else 503).
		{http.MethodPost, umbilicalBasePath + "/item/set", "Add or update one item-kind definition (upsert keyed on name; creates a brand-new good or edits an existing one, leaving its satiation rows intact). Body: {name, display_label, category, sort_order, capabilities:[...], display_label_singular, display_label_plural, consume_dwell_narration}.", true, s.handleUmbilicalItemSet},

		// Recipe (LLM-97) — live edit/add of an item recipe (existing items
		// only). Durable write to item_recipe + in-memory catalog update; needs
		// SetRecipeWriter wired (else 503).
		{http.MethodPost, umbilicalBasePath + "/recipe/set", "Add or update one item recipe (upsert; output + input items must already exist). Body: {output_item, output_qty, rate_qty, rate_per_hours, inputs:[{item,qty}], boost_inputs:[{item,qty,bonus_qty}] (optional per-execution yield boosters, LLM-248), wholesale_price, retail_price}.", true, s.handleUmbilicalRecipeSet},

		// Item satiation (LLM-119) — live edit of how much consuming one unit of
		// an item eases a need (the item_satisfies immediate amount). Durable
		// write to item_satisfies + in-memory catalog update; needs
		// SetSatisfiesWriter wired (else 503).
		{http.MethodPost, umbilicalBasePath + "/item/set-satisfies", "Add or update one item's immediate per-unit need-ease magnitude (upsert keyed item+attribute; the item must already exist and the attribute must be a tracked need). Edits preserve any authored dwell triple. Body: {item, attribute, amount}.", true, s.handleUmbilicalSetSatisfies},
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
