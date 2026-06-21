package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/chatlog"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/promptlog"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// Server serves the read surface for one world. It holds the *sim.World and
// reads world.Published() per request — lock-free, no command channel. Safe
// for concurrent requests: every handler only reads the immutable snapshot.
// Every route requires a valid salem-realm session token (auth); an optional
// event hub (SetEventsHub) adds the authenticated WS /events push channel.
type Server struct {
	world            *sim.World
	auth             Authenticator
	hub              *Hub
	telemetry        *telemetry.RingSink
	prompts          *promptlog.RingSink
	chat             *chatlog.RingSink
	checkpointHealth *sim.CheckpointHealth
	controlEnabled   bool
	errorLog         *errorRing
	clientLog        *clientErrorRing
	clientLogLimiter *clientLogRateLimiter
	// memoryAPIBaseURL is the llm-memory-api root the /umbilical/turns route
	// proxies raw-LLM-turn queries to (the full turn — system_prompt, token
	// counts, cost, provider status — lives only in memory-api's
	// virtual_agent_calls; the engine never sees the composed system prompt). It
	// forwards the operator's own bearer token there. Empty when unset → the
	// /turns route answers 503. Wired by SetMemoryAPIBaseURL under
	// UMBILICAL_ENABLED, same posture as the rings.
	memoryAPIBaseURL string
	// turnsClient is the HTTP client the /umbilical/turns proxy uses to reach
	// memory-api. Owned by the Server (initialized in NewServer) so it has a
	// bounded timeout and tests can reach an httptest upstream via the same path.
	turnsClient *http.Client
	// transcript reads the durable, complete agent_action_log transcript for one
	// huddle, backing the operator-gated GET /umbilical/transcript route (LLM-35).
	// nil → that route answers 503 (store not wired), the same posture as an unset
	// /turns upstream. Wired by SetTranscriptStore under UMBILICAL_ENABLED.
	transcript HuddleTranscriptStore
	// routeForcer backs the operator-gated POST /umbilical/route control route.
	// It returns a sim.Command that dispatches a schedule-driven NPC route
	// (crier / washerwoman) immediately, bypassing the schedule-window gate. Held
	// as an injected func (set by cmd/engine via SetRouteForcer) so httpapi does
	// not import the cascade package that owns the route builders. Nil when
	// unwired → the /route handler answers 503.
	routeForcer func(attrSlug string, start bool) sim.Command
}

// NewServer builds a Server for w, authenticating every route via auth. Panics
// on a nil world or nil Authenticator — both are wiring bugs, and a nil auth
// would silently leave the read surface open.
func NewServer(w *sim.World, auth Authenticator) *Server {
	if w == nil {
		panic("httpapi: NewServer requires a non-nil world")
	}
	if auth == nil {
		panic("httpapi: NewServer requires a non-nil Authenticator")
	}
	return &Server{
		world:            w,
		auth:             auth,
		errorLog:         newErrorRing(0),
		clientLog:        newClientErrorRing(0),
		clientLogLimiter: newClientLogRateLimiter(clientLogRateMax, clientLogRateWindow),
		turnsClient:      &http.Client{Timeout: turnsUpstreamTimeout},
	}
}

// SetEventsHub attaches the WS event hub. When set, Handler exposes the
// GET /api/village/events WebSocket route. The hub must already be Subscribed
// to the world and have its Run goroutine started (both happen at wiring time,
// before world.Run). Wired separately from NewServer so the read-only REST
// surface can stand up without a hub (e.g. existing tests).
//
// MUST be called before Handler and before serving requests — it mutates s
// without synchronization, so calling it concurrently with Handler or a live
// handler races. The intended wiring sets it once during startup.
func (s *Server) SetEventsHub(h *Hub) {
	s.hub = h
}

// SetTelemetry attaches the tick-telemetry ring buffer and, by doing so, ENABLES
// the umbilical debug/control surface. When set (non-nil), Handler registers the
// operator-gated umbilical routes (/api/village/umbilical/*). When NOT set, those
// routes are never registered — the surface does not exist on the wire at all.
// This is the off-by-default posture: cmd/engine only calls SetTelemetry when
// UMBILICAL_ENABLED is on, so a default deployment exposes no umbilical surface
// even to a caller holding plugins/administer.
//
// The same RingSink must also be wired as repo.TickTelemetry so the engine's
// writers feed the buffer this surface reads (see cmd/engine/main.go).
//
// MUST be called before Handler and before serving requests — it mutates s
// without synchronization, identical to SetEventsHub. The intended wiring sets
// it once during startup.
func (s *Server) SetTelemetry(ring *telemetry.RingSink) {
	s.telemetry = ring
}

// SetPrompts attaches the per-actor rendered-prompt ring (ZBBS-HOME-360),
// backing the operator-gated GET /umbilical/agent/prompts route. Optional and
// independent of SetTelemetry: when nil, that route still registers (it's in
// the umbilical table, gated on SetTelemetry like the rest) but reports no
// prompts. cmd/engine wires the same ring it passes to the harness as the
// PromptSink. Same wiring-time-only contract as SetTelemetry — call before
// Handler, never concurrently with serving.
func (s *Server) SetPrompts(ring *promptlog.RingSink) {
	s.prompts = ring
}

// SetChat attaches the per-scene chat-exchange ring (ZBBS-HOME-382), backing the
// operator-gated GET /umbilical/chat route. Optional and independent of
// SetTelemetry: when nil, that route still registers (it's in the umbilical
// table, gated on SetTelemetry like the rest) but reports an empty list. Like
// SetPrompts, MUST be called before Handler and before serving — it mutates s
// without synchronization.
func (s *Server) SetChat(ring *chatlog.RingSink) {
	s.chat = ring
}

// SetMemoryAPIBaseURL configures the upstream llm-memory-api root for the
// operator-gated GET /umbilical/turns route, which proxies raw-LLM-turn queries
// (forwarding the operator's bearer token) to memory-api — the only place the
// full turn (system_prompt, token counts, cost, provider status) is logged. The
// engine never sees the composed system prompt, so this route can't read a local
// ring like the others. Optional and independent of SetTelemetry: when unset,
// the /turns route still registers (it's in the umbilical table, gated on
// SetTelemetry like the rest) but answers 503. cmd/engine wires the same
// LLM_MEMORY_URL the LLM client uses. Same wiring-time-only contract as
// SetTelemetry — call before Handler, never concurrently with serving.
func (s *Server) SetMemoryAPIBaseURL(baseURL string) {
	s.memoryAPIBaseURL = strings.TrimRight(baseURL, "/")
}

// SetTranscriptStore attaches the durable huddle-transcript reader backing the
// operator-gated GET /umbilical/transcript route (LLM-35) — the complete
// committed-action trail of one conversation from agent_action_log, the durable
// companion to the retention-bounded /huddle ring. Optional and independent of
// SetTelemetry: when nil, that route still registers (it's in the umbilical
// table, gated on SetTelemetry like the rest) but answers 503. cmd/engine wires
// the same *pg.ActionLogRepo it installed as the durable action-log sink. Same
// wiring-time-only contract as SetTelemetry — call before Handler, never
// concurrently with serving.
func (s *Server) SetTranscriptStore(store HuddleTranscriptStore) {
	s.transcript = store
}

// SetCheckpointHealth attaches the durable-checkpoint health recorder so the
// umbilical /checkpoint-health route (and the checkpoint summary in /state) can
// surface it. Optional: when nil, those views report the zero value (the
// CheckpointHealth methods are nil-safe). cmd/engine wires the same recorder
// the periodic checkpointer writes to. Same wiring-time-only contract as
// SetTelemetry — call before Handler, never concurrently with serving.
func (s *Server) SetCheckpointHealth(h *sim.CheckpointHealth) {
	s.checkpointHealth = h
}

// SetControlEnabled arms the world-MUTATING umbilical control routes
// (/umbilical/nudge, /umbilical/phase). This is a second, independent opt-in on
// top of SetTelemetry: an operator can run the read-only introspection surface
// (telemetry + state) without exposing any route that can mutate the running
// world. cmd/engine calls this only under UMBILICAL_CONTROL_ENABLED, and only
// when the umbilical itself is enabled — control routes register only when BOTH
// a telemetry ring is attached AND control is enabled. The routes are still
// requireOperator-gated and every invocation is audited (see umbilical_control.go).
//
// Same wiring-time-only contract as SetTelemetry/SetEventsHub: call before
// Handler, never concurrently with serving.
func (s *Server) SetControlEnabled(enabled bool) {
	s.controlEnabled = enabled
}

// SetRouteForcer wires the cascade-backed forcer behind the operator-gated POST
// /umbilical/route control route. The func returns a sim.Command that dispatches
// a schedule-driven NPC route (town_crier / washerwoman) immediately, bypassing
// the schedule-window gate — letting an operator reproduce a tour on demand
// instead of waiting for a boundary or restarting. Injected (rather than
// imported) so httpapi stays free of the cascade dependency that owns the route
// builders. Optional: when unset the /route handler answers 503. Same
// wiring-time-only contract as SetTelemetry — call before Handler, never
// concurrently with serving.
func (s *Server) SetRouteForcer(f func(attrSlug string, start bool) sim.Command) {
	s.routeForcer = f
}

// Handler returns the read-surface routes: the static-render read set
// (world / agents / objects off the published snapshot; terrain / assets /
// sprites off *sim.World reference state), plus the WS /events push channel
// when an event hub is attached via SetEventsHub. Every route requires a valid
// salem-realm session token — REST via requireAuth (Bearer header), WS via its
// own ?token= verify. Write routes (POST) run the mutation through the world
// command channel — see write_handlers.go.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Every REST read is wrapped in requireAuth (Bearer token → verify →
	// salem-realm gate). The WS /events handler does its own ?token= verify
	// (browsers can't set WS handshake headers), before the upgrade.
	mux.HandleFunc("GET /api/village/world", s.requireAuth(s.handleWorld))
	mux.HandleFunc("GET /api/village/agents", s.requireAuth(s.handleAgents))
	mux.HandleFunc("GET /api/village/objects", s.requireAuth(s.handleObjects))
	mux.HandleFunc("GET /api/village/object/gather", s.requireAuth(s.handleObjectGather)) // LLM-52: hover berry count
	mux.HandleFunc("GET /api/village/terrain", s.requireAuth(s.handleTerrain))
	mux.HandleFunc("GET /api/village/assets", s.requireAuth(s.handleAssets))
	mux.HandleFunc("GET /api/village/sprites", s.requireAuth(s.handleSprites))
	mux.HandleFunc("GET /api/village/npc-behaviors", s.requireAuth(s.handleNPCBehaviors))
	mux.HandleFunc("GET /api/village/refresh-attributes", s.requireAuth(s.handleRefreshAttributes))
	// Item-kind catalog for the Pay modal's compose-an-offer dropdown
	// (ZBBS-HOME-423) — lean reference data, see items.go. No longer
	// boot-immutable: ZBBS-WORK-412 mints discovered kinds at runtime, so the
	// handler reads the published snapshot (and filters discoveries out).
	mux.HandleFunc("GET /api/village/items", s.requireAuth(s.handleItems))
	// Rich admin items catalog for the Village Config panel: full defs + a live
	// in-world stock rollup, admin-gated. Revives the v1 ZBBS-114 catalog
	// (ZBBS-WORK-412). Separate from the lean route above so the hot Pay-modal
	// path doesn't pay for the all-actor inventory scan.
	mux.HandleFunc("GET /api/village/items/catalog", s.requireAuth(s.handleItemCatalog))
	// Static editor allowlists (vocabulary the editor's tag dropdowns render).
	// Hardcoded reference data — no World map, no DB; see catalog_tags.go.
	mux.HandleFunc("GET /api/village/object-tags", s.requireAuth(s.handleObjectTags))
	mux.HandleFunc("GET /api/assets/state-tags", s.requireAuth(s.handleStateTags))
	// Client-reported error feed (clientlog.go). Authed write; records browser-
	// runtime failures the engine/nginx can't see into a pull-only ring surfaced
	// via the umbilical. Untrusted — kept separate from the server-observed ring.
	mux.HandleFunc("POST /api/village/client-log", s.requireAuth(s.handleClientLog))
	// PC bootstrap read. POST to match the v1 verb + the client, but it's a
	// pure snapshot read (no command channel) — see pc_me.go.
	mux.HandleFunc("POST /api/village/pc/me", s.requireAuth(s.handlePCMe))
	// Live take-able scene quotes for the Pay modal (ZBBS-HOME-426) — pure
	// snapshot read over Snapshot.Quotes; see pc_quotes.go.
	mux.HandleFunc("GET /api/village/pc/quotes", s.requireAuth(s.handlePCQuotes))
	// Write routes — same requireAuth gate; the mutation runs through the
	// world command channel (see write_handlers.go).
	mux.HandleFunc("POST /api/village/pc/move", s.requireAuth(s.handlePCMove))
	mux.HandleFunc("POST /api/village/pc/speak", s.requireAuth(s.handlePCSpeak))
	mux.HandleFunc("POST /api/village/pc/pay", s.requireAuth(s.handlePCPay))
	mux.HandleFunc("POST /api/village/pc/sprite", s.requireAuth(s.handlePCSprite))
	mux.HandleFunc("POST /api/village/pc/create", s.requireAuth(s.handlePCCreate))
	mux.HandleFunc("POST /api/village/pc/sleep", s.requireAuth(s.handlePCSleep))
	mux.HandleFunc("POST /api/village/pc/wake", s.requireAuth(s.handlePCWake))
	mux.HandleFunc("POST /api/village/pc/gather", s.requireAuth(s.handlePCGather)) // ZBBS-WORK-328
	// Admin world-config read (ZBBS-WORK-363) — the config panel's populate
	// fetch. Admin-only despite being a GET: requireAuth + an in-command
	// IsAdmin gate (adminCommand inside handleConfig), so it stays off the
	// public /world poll and reads live settings. See config_handlers.go.
	mux.HandleFunc("GET /api/village/config", s.requireAuth(s.handleConfig))
	// Village-activity feed for the talk panel's admin-only Village tab
	// (ZBBS-WORK-399). requireOperator (plugins/administer) rather than the
	// in-command IsAdmin gate: it's a snapshot read that never touches the
	// world goroutine, and the capability is the same one pc/me's can_edit
	// mirrors — the flag the client uses to show the tab. See
	// village_activity.go.
	mux.HandleFunc("GET /api/village/activity/recent", s.requireOperator(s.handleVillageActivity))
	// Admin write routes — requireAuth PLUS an in-command admin gate (the
	// caller's actor must have admin = true; see adminCommand in
	// write_handlers.go). Distinct from pc/* whose ownership is structural.
	mux.HandleFunc("POST /api/village/admin/phase", s.requireAuth(s.handleAdminPhase))
	// World-config write routes (ZBBS-WORK-363) — the config panel's saves.
	// Mutate the runtime-tunable WorldSettings subset; durability rides the
	// checkpoint, live updates ride the WS hub. See config_handlers.go.
	mux.HandleFunc("POST /api/village/admin/zoom-settings", s.requireAuth(s.handleAdminZoomSettings))
	mux.HandleFunc("POST /api/village/admin/agent-ticks", s.requireAuth(s.handleAdminAgentTicks))
	mux.HandleFunc("POST /api/village/admin/force-rotate", s.requireAuth(s.handleAdminForceRotate))
	mux.HandleFunc("POST /api/village/admin/object/create", s.requireAuth(s.handleAdminObjectCreate))
	mux.HandleFunc("POST /api/village/admin/object/move", s.requireAuth(s.handleAdminObjectMove))
	mux.HandleFunc("POST /api/village/admin/object/delete", s.requireAuth(s.handleAdminObjectDelete))
	mux.HandleFunc("POST /api/village/admin/object/set-state", s.requireAuth(s.handleAdminObjectSetState))
	mux.HandleFunc("POST /api/village/admin/object/set-owner", s.requireAuth(s.handleAdminObjectSetOwner))
	mux.HandleFunc("POST /api/village/admin/object/set-loiter-offset", s.requireAuth(s.handleAdminObjectSetLoiterOffset))
	mux.HandleFunc("POST /api/village/admin/object/set-entry-policy", s.requireAuth(s.handleAdminObjectSetEntryPolicy))
	mux.HandleFunc("POST /api/village/admin/object/set-display-name", s.requireAuth(s.handleAdminObjectSetDisplayName))
	mux.HandleFunc("POST /api/village/admin/object/add-tag", s.requireAuth(s.handleAdminObjectAddTag))
	mux.HandleFunc("POST /api/village/admin/object/remove-tag", s.requireAuth(s.handleAdminObjectRemoveTag))
	mux.HandleFunc("POST /api/village/admin/object/set-refresh", s.requireAuth(s.handleAdminObjectSetRefresh))
	// NPC editor write routes (ZBBS-HOME-309) — author/edit NPC metadata; the
	// write half of AgentDTO's read surface. Same admin gate as object/*.
	mux.HandleFunc("POST /api/village/admin/npc/set-display-name", s.requireAuth(s.handleAdminNPCSetDisplayName))
	mux.HandleFunc("POST /api/village/admin/npc/set-agent", s.requireAuth(s.handleAdminNPCSetAgent))
	mux.HandleFunc("POST /api/village/admin/npc/set-home-structure", s.requireAuth(s.handleAdminNPCSetHomeStructure))
	mux.HandleFunc("POST /api/village/admin/npc/set-work-structure", s.requireAuth(s.handleAdminNPCSetWorkStructure))
	mux.HandleFunc("POST /api/village/admin/npc/set-schedule", s.requireAuth(s.handleAdminNPCSetSchedule))
	mux.HandleFunc("POST /api/village/admin/npc/set-social", s.requireAuth(s.handleAdminNPCSetSocial))
	mux.HandleFunc("POST /api/village/admin/npc/add-attribute", s.requireAuth(s.handleAdminNPCAddAttribute))
	mux.HandleFunc("POST /api/village/admin/npc/remove-attribute", s.requireAuth(s.handleAdminNPCRemoveAttribute))
	mux.HandleFunc("POST /api/village/admin/npc/create", s.requireAuth(s.handleAdminNPCCreate))
	mux.HandleFunc("POST /api/village/admin/npc/delete", s.requireAuth(s.handleAdminNPCDelete))
	mux.HandleFunc("POST /api/village/admin/npc/set-sprite", s.requireAuth(s.handleAdminNPCSetSprite))
	mux.HandleFunc("POST /api/village/admin/npc/inventory", s.requireAuth(s.handleAdminNPCInventory))
	mux.HandleFunc("POST /api/village/admin/npc/set-inventory", s.requireAuth(s.handleAdminNPCSetInventory))
	if s.hub != nil {
		mux.HandleFunc("GET /api/village/events", s.handleEvents)
	}
	// Umbilical debug/control surface — operator-gated (requireOperator =
	// requireAuth + plugins/administer). Registered ONLY when a telemetry ring
	// is attached (SetTelemetry), which cmd/engine does only under
	// UMBILICAL_ENABLED. Off by default → the routes don't exist.
	//
	// Registration is driven by the umbilicalRoutes() descriptor table (the
	// single source of truth that also backs the self-describing manifest at
	// GET /api/village/umbilical — see umbilical.go). Read routes are always
	// armed when the umbilical is on; control (world-mutating) routes only when
	// control is ALSO enabled (UMBILICAL_CONTROL_ENABLED), so read-only is the
	// default even with the umbilical on. Every route is requireOperator-gated;
	// the control routes are additionally audited (see umbilical_control.go).
	if s.telemetry != nil {
		for _, rt := range s.umbilicalRoutes() {
			if rt.control && !s.controlEnabled {
				continue
			}
			mux.HandleFunc(rt.method+" "+rt.path, s.requireOperator(rt.handler))
		}
	}
	// Wrap the whole mux so every non-2xx response (incl. no-route 404s and auth
	// rejections, which sit outside requireAuth) is recorded + logged. See
	// errorlog.go.
	return s.withErrorCapture(mux)
}

func (s *Server) handleWorld(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, worldStateFromSnapshot(snap))
}

func (s *Server) handleAgents(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, agentsFromSnapshot(snap, s.world.Sprites))
}

func (s *Server) handleObjects(w http.ResponseWriter, _ *http.Request) {
	snap := s.world.Published()
	writeJSON(w, objectsFromSnapshot(snap, s.world.Assets))
}

// handleTerrain serves the terrain grid. Unlike the per-tick handlers above it
// reads world.Terrain directly — reference state loaded once at startup and
// never written by the engine loop, so the read is lock-free with no snapshot
// or command channel. The DTO aliases Terrain.Data (no defensive copy) because
// that immutability holds.
//
// INVARIANT for the future SIGHUP hot-reload (not yet wired — cmd/engine
// registers only SIGINT/SIGTERM): a reload MUST publish a NEW *sim.Terrain /
// asset map and swap it atomically (e.g. atomic.Pointer, the way world.Publish
// works for per-tick state). It must NOT mutate the existing Terrain.Data slice
// or world.Assets map in place — these handlers read them concurrently without
// synchronization, so an in-place mutation would race.
func (s *Server) handleTerrain(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, terrainDTO(s.world.Terrain))
}

// handleAssets serves the asset catalog. Reads world.Assets directly — same
// lock-free reference-state posture and same SIGHUP invariant as handleTerrain.
func (s *Server) handleAssets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, assetsFromCatalog(s.world.Assets))
}

// handleSprites serves the raw character-sprite catalog (the editor's sprite
// picker source). Reads world.Sprites directly — same lock-free reference-
// state posture and same SIGHUP invariant as handleTerrain/handleAssets.
func (s *Server) handleSprites(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, spritesFromCatalog(s.world.Sprites))
}

// handleNPCBehaviors serves the actor-assignable attribute catalog (the
// editor's "add attribute" dropdown source). Reads world.AttributeDefinitions
// directly — same lock-free reference-state posture and same SIGHUP invariant
// as handleSprites/handleAssets.
func (s *Server) handleNPCBehaviors(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, npcBehaviorsFromCatalog(s.world.AttributeDefinitions))
}

// handleRefreshAttributes serves the need catalog (the refresh editor's
// attribute dropdown source) — the v2 replacement for v1's /api/refresh-attributes.
// Reads the frozen sim.Needs registry (reference state, no World map / DB),
// same lock-free posture as handleObjectTags. The set-refresh route validates a
// row's attribute against this same registry via sim.FindNeed.
func (s *Server) handleRefreshAttributes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, refreshAttributesFromNeeds())
}

// worldStateFromSnapshot maps the snapshot's world-level state to the wire DTO.
func worldStateFromSnapshot(s *sim.Snapshot) WorldStateDTO {
	return WorldStateDTO{
		ContractVersion: ContractVersion,
		Phase:           string(s.Phase),
		Tick:            s.AtTick,
		// Real world clock: the snapshot's publish wall-time, normalized to UTC
		// for an unambiguous wire instant (was the never-assigned Environment.Now).
		Now:            s.PublishedAt.UTC(),
		Weather:        s.Environment.Weather,
		Atmosphere:     s.Environment.Atmosphere,
		ZoomMinAdmin:   s.ZoomMinAdmin,
		ZoomMinRegular: s.ZoomMinRegular,
	}
}

// agentsFromSnapshot maps every actor to an AgentDTO, sorted by ID so the
// response is deterministic (stable client diffs + testable). The sprite
// catalog (reference state off *sim.World) is passed in so each agent's
// SpriteID can be resolved + inlined; a nil/absent catalog or a dangling
// SpriteID simply leaves Sprite unset (the client renders a placeholder).
func agentsFromSnapshot(s *sim.Snapshot, sprites map[sim.SpriteID]*sim.Sprite) []AgentDTO {
	out := make([]AgentDTO, 0, len(s.Actors))
	for id, a := range s.Actors {
		hunger, thirst, tiredness := sim.DisplayNeeds(a.Needs)
		out = append(out, AgentDTO{
			ID:                string(id),
			DisplayName:       a.DisplayName,
			Kind:              actorKindString(a.Kind),
			State:             string(a.State),
			Role:              a.Role,
			LLMAgent:          a.LLMAgent,
			X:                 a.Pos.X,
			Y:                 a.Pos.Y,
			Facing:            normalizeFacing(a.Facing),
			InsideStructureID: string(a.InsideStructureID),
			CurrentHuddleID:   string(a.CurrentHuddleID),
			Sprite:            resolveAgentSprite(a.SpriteID, sprites),
			Hunger:            hunger,
			Thirst:            thirst,
			Tiredness:         tiredness,
			Attributes:        a.AttributeSlugs,
			HomeStructureID:   string(a.HomeStructureID),
			WorkStructureID:   string(a.WorkStructureID),
			ScheduleStartMin:  a.ScheduleStartMin,
			ScheduleEndMin:    a.ScheduleEndMin,
			SocialTag:         a.SocialTag,
			SocialStartMin:    a.SocialStartMin,
			SocialEndMin:      a.SocialEndMin,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// normalizeFacing coalesces an unset Facing to "south" so every agent emits a
// valid facing regardless of origin. A pg-loaded actor's facing column is NOT
// NULL (default 'south'); an in-memory-spawned actor may have an empty Facing.
// Without this the wire would carry "south" for the former and omit it for the
// latter — an origin-dependent inconsistency. Non-empty values pass through
// unchanged (the write path validates the enum; this read path is display-only
// and never the source of a bad stored value).
func normalizeFacing(facing string) string {
	if facing == "" {
		return "south"
	}
	return facing
}

// resolveAgentSprite looks up spriteID in the catalog and maps it to the
// inline render DTO. Returns nil when the actor has no sprite (empty ID) or
// the ID doesn't resolve (dangling ref) — Sprite is omitempty, so the field
// is simply absent on the wire and the client falls back to a placeholder.
func resolveAgentSprite(spriteID sim.SpriteID, sprites map[sim.SpriteID]*sim.Sprite) *AgentSpriteDTO {
	if spriteID == "" || sprites == nil {
		return nil
	}
	return agentSpriteDTOFromSprite(sprites[spriteID])
}

// agentSpriteDTOFromSprite maps a resolved catalog sprite to the inline render
// DTO. Returns nil for a nil sprite. Split out from resolveAgentSprite so the
// npc_created translate path (which carries the *sim.Sprite on the event) can
// build the same DTO without a catalog map.
func agentSpriteDTOFromSprite(sp *sim.Sprite) *AgentSpriteDTO {
	if sp == nil {
		return nil
	}
	return &AgentSpriteDTO{
		ID:          string(sp.ID),
		Name:        sp.Name,
		Sheet:       sp.Sheet,
		FrameWidth:  sp.FrameWidth,
		FrameHeight: sp.FrameHeight,
		Animations:  spriteAnimationsDTO(sp.Animations),
	}
}

// objectsFromSnapshot maps every village object to an ObjectDTO, sorted by ID.
// The asset catalog (immutable reference state off *sim.World, same posture as
// agentsFromSnapshot's sprite map) is passed in to resolve each object's
// effective loiter offset; sim.EffectiveLoiterOffset is nil-asset-safe, so a
// dangling asset_id falls back to the raw override (or zero) without breaking
// the build (ZBBS-HOME-289).
func objectsFromSnapshot(s *sim.Snapshot, assets map[sim.AssetID]*sim.Asset) []ObjectDTO {
	out := make([]ObjectDTO, 0, len(s.VillageObjects))
	for id, o := range s.VillageObjects {
		if o == nil {
			continue
		}
		effX, effY := sim.EffectiveLoiterOffset(o, assets[o.AssetID])
		// has_interior == "this placement is also a Structure" via the
		// shared-identity bridge: any building (or legacy shell-backed prop
		// like a noticeboard) has a Structure row whose id matches the
		// VillageObject's. Bare placements (wells, lamps, gather piles)
		// don't. Drives client dispatch between structure_enter (walk inside)
		// and object_visit (walk to loiter slot) — ZBBS-WORK-351.
		//
		// Tombstoned entries (key present, value nil) don't count as a real
		// shell — match MoveActor's `!ok || vobj == nil` shape so a stale
		// nil row can't mark a bare placement as has_interior=true and
		// route a click into a structure_enter 404 (code_review round 1).
		shell, hasInterior := s.Structures[sim.StructureID(id)]
		hasInterior = hasInterior && shell != nil
		dto := ObjectDTO{
			ID:                     string(id),
			AssetID:                string(o.AssetID),
			X:                      o.Pos.X,
			Y:                      o.Pos.Y,
			CurrentState:           o.CurrentState,
			DisplayName:            o.DisplayName,
			Tags:                   o.Tags,
			Owner:                  string(o.OwnerActorID),
			PlacedBy:               o.PlacedBy,
			EntryPolicy:            string(o.EntryPolicy),
			LoiterOffsetX:          o.LoiterOffsetX,
			LoiterOffsetY:          o.LoiterOffsetY,
			EffectiveLoiterOffsetX: effX,
			EffectiveLoiterOffsetY: effY,
			HasInterior:            hasInterior,
		}
		// Noticeboard content (ZBBS-HOME-291): a board with authored prose
		// carries its current text + posted-at; everything else omits both.
		if nc := s.NoticeboardContent[id]; nc != nil {
			dto.ContentText = nc.Text
			postedAt := nc.PostedAt
			dto.ContentPostedAt = &postedAt
		}
		// Refresh policies for the editor panel (no standalone GET in v2).
		// refreshRowsToWire returns a non-nil empty slice for no refreshes,
		// which `omitempty` then drops from the wire.
		if len(o.Refreshes) > 0 {
			dto.Refreshes = refreshRowsToWire(o.Refreshes)
		}
		out = append(out, dto)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// terrainDTO maps the terrain reference state to the wire DTO. Grid dimensions
// come from the canonical sim constants (the loaded blob is validated to be
// exactly MapW*MapH at load, so they always agree). A nil Terrain (terrain
// absent / not loaded) yields the metadata header with an empty Data string —
// the client decodes that to a zero-length grid and renders nothing.
func terrainDTO(t *sim.Terrain) TerrainDTO {
	dto := TerrainDTO{
		ContractVersion: ContractVersion,
		MapW:            sim.MapW,
		MapH:            sim.MapH,
		PadX:            sim.PadX,
		PadY:            sim.PadY,
		TileSize:        int(sim.TileSize),
	}
	if t != nil {
		dto.Data = base64.StdEncoding.EncodeToString(t.Data)
	}
	return dto
}

// assetsFromCatalog maps the asset catalog to a DTO slice, sorted by ID so the
// response is deterministic (stable client diffs + testable).
func assetsFromCatalog(assets map[sim.AssetID]*sim.Asset) []AssetDTO {
	out := make([]AssetDTO, 0, len(assets))
	for id, a := range assets {
		if a == nil {
			continue
		}
		out = append(out, assetDTO(id, a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// assetDTO maps one catalog asset to its render-graph DTO.
func assetDTO(id sim.AssetID, a *sim.Asset) AssetDTO {
	dto := AssetDTO{
		ID:                string(id),
		Name:              a.Name,
		Category:          a.Category,
		DefaultState:      a.DefaultState,
		AnchorX:           a.AnchorX,
		AnchorY:           a.AnchorY,
		Layer:             a.Layer,
		ZIndex:            a.ZIndex,
		VisibleWhenInside: a.VisibleWhenInside,
		StandOffsetX:      a.StandOffsetX,
		StandOffsetY:      a.StandOffsetY,
		DoorOffsetX:       a.DoorOffsetX,
		DoorOffsetY:       a.DoorOffsetY,
		Footprint: FootprintDTO{
			Left:   a.FootprintLeft,
			Right:  a.FootprintRight,
			Top:    a.FootprintTop,
			Bottom: a.FootprintBottom,
		},
		FitsSlot: a.FitsSlot,
		States:   assetStatesDTO(a.States),
		Slots:    assetSlotsDTO(a.Slots),
	}
	if a.Pack != nil {
		dto.Pack = &TilesetPackDTO{
			ID:   a.Pack.ID,
			Name: a.Pack.Name,
			URL:  a.Pack.URL,
		}
	}
	return dto
}

// assetStatesDTO maps an asset's states. Always returns a non-nil slice — the
// states field is required on the wire (no omitempty), so an asset with no
// states serializes as [] rather than null.
func assetStatesDTO(states []sim.AssetState) []AssetStateDTO {
	out := make([]AssetStateDTO, 0, len(states))
	for i := range states {
		st := &states[i]
		d := AssetStateDTO{
			State:      st.State,
			Sheet:      st.Sheet,
			SrcX:       st.SrcX,
			SrcY:       st.SrcY,
			SrcW:       st.SrcW,
			SrcH:       st.SrcH,
			FrameCount: st.FrameCount,
			FrameRate:  st.FrameRate,
			Tags:       st.Tags,
		}
		if st.Light != nil {
			d.Light = &AssetLightDTO{
				Color:            st.Light.Color,
				Radius:           st.Light.Radius,
				Energy:           st.Light.Energy,
				OffsetX:          st.Light.OffsetX,
				OffsetY:          st.Light.OffsetY,
				FlickerAmplitude: st.Light.FlickerAmplitude,
				FlickerPeriodMs:  st.Light.FlickerPeriodMs,
			}
		}
		out = append(out, d)
	}
	return out
}

// assetSlotsDTO maps an asset's attachment slots. Returns nil when there are
// none — slots carries omitempty, so it's absent on the wire for slot-less
// assets (the common case).
func assetSlotsDTO(slots []sim.AssetSlot) []AssetSlotDTO {
	if len(slots) == 0 {
		return nil
	}
	out := make([]AssetSlotDTO, 0, len(slots))
	for i := range slots {
		out = append(out, AssetSlotDTO{
			SlotName: slots[i].SlotName,
			OffsetX:  slots[i].OffsetX,
			OffsetY:  slots[i].OffsetY,
		})
	}
	return out
}

// spritesFromCatalog maps the sprite catalog to a DTO slice, sorted by ID so
// the response is deterministic (stable client diffs + testable).
func spritesFromCatalog(sprites map[sim.SpriteID]*sim.Sprite) []SpriteDTO {
	out := make([]SpriteDTO, 0, len(sprites))
	for id, sp := range sprites {
		if sp == nil {
			continue
		}
		out = append(out, spriteDTO(id, sp))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// npcBehaviorsFromCatalog maps the attribute-definition catalog to a DTO slice,
// sorted by display name so the editor dropdown is predictable (mirrors v1's
// ORDER BY display_name) and the response is deterministic for tests. Ties on
// display name fall back to slug for a total order.
func npcBehaviorsFromCatalog(defs map[string]*sim.AttributeDefinition) []NPCBehaviorDTO {
	out := make([]NPCBehaviorDTO, 0, len(defs))
	for _, d := range defs {
		if d == nil {
			continue
		}
		out = append(out, NPCBehaviorDTO{Slug: d.Slug, DisplayName: d.DisplayName})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// refreshAttributesFromNeeds maps the canonical sim.Needs registry to the
// refresh-attribute DTO slice for the editor dropdown. Registry order is stable
// (hunger/thirst/tiredness), so no sort is needed; the DisplayLabel is the key
// with an upper-cased first rune ("hunger" -> "Hunger") since needs carry no
// dedicated label field.
func refreshAttributesFromNeeds() []RefreshAttributeDTO {
	out := make([]RefreshAttributeDTO, 0, len(sim.Needs))
	for _, n := range sim.Needs {
		key := string(n.Key)
		label := key
		if key != "" {
			label = strings.ToUpper(key[:1]) + key[1:]
		}
		out = append(out, RefreshAttributeDTO{Name: key, DisplayLabel: label})
	}
	return out
}

// spriteDTO maps one catalog sprite to its render DTO.
func spriteDTO(id sim.SpriteID, sp *sim.Sprite) SpriteDTO {
	dto := SpriteDTO{
		ID:          string(id),
		Name:        sp.Name,
		Sheet:       sp.Sheet,
		FrameWidth:  sp.FrameWidth,
		FrameHeight: sp.FrameHeight,
		Animations:  spriteAnimationsDTO(sp.Animations),
	}
	if sp.Pack != nil {
		dto.Pack = &TilesetPackDTO{
			ID:   sp.Pack.ID,
			Name: sp.Pack.Name,
			URL:  sp.Pack.URL,
		}
	}
	return dto
}

// spriteAnimationsDTO maps a sprite's animation rows. Always returns a
// non-nil slice — animations is required on the wire (no omitempty), so a
// sprite with no animation rows serializes as [] rather than null.
func spriteAnimationsDTO(anims []sim.SpriteAnimation) []SpriteAnimationDTO {
	out := make([]SpriteAnimationDTO, 0, len(anims))
	for i := range anims {
		out = append(out, SpriteAnimationDTO{
			Direction:  anims[i].Direction,
			Animation:  anims[i].Animation,
			RowIndex:   anims[i].RowIndex,
			FrameCount: anims[i].FrameCount,
			FrameRate:  anims[i].FrameRate,
		})
	}
	return out
}

// writeJSON encodes v as the JSON response body. A late encode error (after
// the 200 header is sent) can't be recovered into a status code, so it's
// logged — the client sees a truncated body and re-syncs via its next fetch.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}
