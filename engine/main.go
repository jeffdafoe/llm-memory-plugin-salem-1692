package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// App holds shared dependencies for all handlers.
type App struct {
	DB           *pgxpool.Pool
	LLMMemoryURL string // base URL for llm-memory auth verification
	Hub          *EventHub

	// WorldEventGen increments on every applyTransition / applyRotation call.
	// Each pendingFlip scheduled by those calls captures the gen in effect at
	// schedule time; applyFlip bails if a newer event has fired before its
	// timer elapsed. Prevents zombie flips from an earlier Force Night landing
	// after a subsequent Force Day.
	WorldEventGen atomic.Uint64

	// NPCMovement tracks active NPC walks. Lookup by NPC id; mutex-guarded.
	NPCMovement *NPCMovement

	// NPCBehaviors tracks active scheduled NPC routines (lamplighter ...).
	// One per NPC; advanced from applyArrival so each waypoint chains into
	// the next state-machine step.
	NPCBehaviors *NPCBehaviors

	// npcChatClient drives /v1/chat/send?wait=true calls for LLM-controlled
	// NPCs, authenticated as `salem-engine`. Created once at startup and
	// shared across server-tick dispatches.
	npcChatClient *npcChatClient

	// NPCDisplayNames maps an agent slug (= namespace) to the NPC's
	// human-readable display_name. Refreshed by refreshNPCDisplayNames
	// every server tick so newly added agents are visible to the recall
	// result formatter without engine restart. Misses fall through to
	// a one-shot DB lookup in namespaceDisplayName.
	//
	// Guarded by NPCDisplayNamesMu — written once per server tick by
	// the refresh function; read on every recall result formatter call,
	// which can happen from any goroutine (NPC ticks, chronicler fires).
	NPCDisplayNames   map[string]string
	NPCDisplayNamesMu sync.RWMutex

	// ChroniclerSem caps the number of concurrent cascade-origin
	// chronicler fires in flight. Cascade origins (PC speech, NPC
	// arrival) can bunch — this prevents a slow / hung chat API from
	// piling up unbounded goroutines. Cascade fires arriving while the
	// slot is full are skipped (logged) rather than queued. Capacity 2
	// is enough for the realistic event rate; bump if drops are
	// observed in practice.
	ChroniclerSem chan struct{}

	// OverseerAttendSem caps the number of concurrent attend_to-spawned
	// agent ticks in flight (ZBBS-083). Each chronicler fire can dispatch
	// up to chronicler_dispatch_ceiling NPCs (default 12); without an
	// app-level cap, two overlapping fires could spawn ~24 concurrent
	// agent ticks against the upstream LLM provider. Per-NPC cost guards
	// in triggerImmediateTick prevent same-NPC storms but don't bound
	// aggregate concurrency. Capacity 4 is conservative for the current
	// 4-NPC village; raise as population grows.
	OverseerAttendSem chan struct{}

	// SceneTickedActors deduplicates reactor-tick fan-out within a single
	// scene. The cascade machinery (PC speak → reactor ticks → reactor's
	// own speak/act → next reactor ticks → ...) propagates a sceneID from
	// the cascade origin through every downstream tick.
	//
	// Two policies layer on top of this map:
	//   (a) Same-trigger-actor dedup: an actor doesn't tick twice in a
	//       scene in response to the same triggering actor. Different
	//       triggering actor (different speaker, different arriver)
	//       passes through. This models natural conversation — you
	//       respond to John once, but Ezekiel saying something new is
	//       its own thing to react to.
	//   (b) Hard per-(scene, actor) reaction cap as a backstop on cost.
	//       maxReactionsPerSceneActor below.
	//
	// Empty triggerActorID (e.g. chronicler-attendance, which has no
	// salient speaker) doesn't match any other empty against (a), so a
	// chronicler-dispatched scene-opening tick doesn't lock the actor
	// out of subsequent heard-speech reactions. (b) still applies.
	//
	// Key: sceneID + "|" + actorID. Value: a sceneTickEntry tracking the
	// last tick's salient actor and the count of ticks so far. Stale
	// entries (>30 min) are evicted by a periodic cleanup goroutine.
	SceneTickedActors   map[string]sceneTickEntry
	SceneTickedActorsMu sync.Mutex

	// AgentTickInFlight prevents a second tick from firing on an NPC
	// while their current tick is still running. Two cascades aimed at
	// the same actor (e.g. a PC-speak cascade running John's LLM loop
	// while a chronicler overseer-attend-to fires a fresh tick on him)
	// previously produced duplicate LLM output — both calls completed,
	// both wrote action_log rows, both ran tool side effects. This map
	// gates the second one out via tryClaimAgentTick.
	//
	// Set is keyed by actor_id. Values are intentionally `bool` so the
	// presence of the key is the gate; lookups don't read the value.
	// Released by releaseAgentTick at the end of runAgentTick (defer)
	// so a panic in the LLM loop doesn't strand the gate.
	AgentTickInFlight   map[string]bool
	AgentTickInFlightMu sync.Mutex

	// ChroniclerDispatchQueue buffers agent-NPC shift boundary events
	// between the worker scheduler (enqueue site) and the chronicler
	// (drain site). Drained at perception build time so any chronicler
	// fire — phase, cascade, or the dedicated shift-boundary dispatcher
	// — picks up pending events. See dispatch_queue.go for semantics.
	ChroniclerDispatchQueue *chroniclerDispatchQueue
}

// sceneTickEntry is the per-(scene, actor) dedup record.
type sceneTickEntry struct {
	lastAt           time.Time
	lastTriggerActor string // actor_id of who caused the prior tick; "" for no-speaker triggers (chronicler dispatch)
	count            int
}

// sceneTickKey is the dedup key used by SceneTickedActors.
func sceneTickKey(sceneID, actorID string) string {
	return sceneID + "|" + actorID
}

// sceneTickStaleness is how long a (scene, actor) entry remains in
// SceneTickedActors before being treated as stale. A real cascade
// completes in seconds — entries this old indicate the cascade
// finished long ago and a same-sceneID re-trigger (vanishingly rare
// since sceneIDs are UUIDs) should be allowed to proceed.
const sceneTickStaleness = 30 * time.Minute

// maxReactionsPerSceneActor is the hard backstop on how many times a
// single actor can react inside a single scene. Generous enough that
// a healthy back-and-forth (responses to multiple speakers, follow-up
// reactions) doesn't bump it; tight enough that a runaway cascade or
// a chatty PC can't burn unbounded budget.
const maxReactionsPerSceneActor = 4

// claimSceneTick reserves the next (sceneID, actorID) tick slot using
// the policy described in the SceneTickedActors comment.
//
// triggerActorID is the actor_id of who caused this tick (the speaker
// for heard-speech, the actor for saw-action, the arriver for
// arrival, the PC's actor_id for pc-spoke, "" for chronicler dispatch
// or any trigger without a salient speaker).
//
// Returns (allowed, reason). reason is empty on allow and a short
// label on skip ("same trigger actor" or "reaction cap reached") so
// the caller can log specifically what happened.
//
// Single mutex acquisition gates check-and-update so concurrent
// triggers from a fan-out don't both observe "ok" and both proceed.
func (app *App) claimSceneTick(sceneID, actorID, triggerActorID string) (bool, string) {
	key := sceneTickKey(sceneID, actorID)
	now := time.Now()
	app.SceneTickedActorsMu.Lock()
	defer app.SceneTickedActorsMu.Unlock()
	entry, exists := app.SceneTickedActors[key]
	if exists && now.Sub(entry.lastAt) < sceneTickStaleness {
		// (b) backstop first — once the cap is hit no further reactions
		// fire regardless of who triggered them.
		if entry.count >= maxReactionsPerSceneActor {
			return false, fmt.Sprintf("reaction cap reached (%d)", entry.count)
		}
		// (a) same-trigger-actor dedup. Empty triggerActorID never matches
		// (chronicler-style triggers don't lock the actor against later
		// heard-speech reactions, and two empty triggers in a row are
		// vanishingly rare in practice).
		if triggerActorID != "" && triggerActorID == entry.lastTriggerActor {
			return false, "same trigger actor"
		}
		entry.lastAt = now
		entry.lastTriggerActor = triggerActorID
		entry.count++
		app.SceneTickedActors[key] = entry
		return true, ""
	}
	app.SceneTickedActors[key] = sceneTickEntry{
		lastAt:           now,
		lastTriggerActor: triggerActorID,
		count:            1,
	}
	return true, ""
}

// tryClaimAgentTick reserves the in-flight gate for actorID. Returns
// true when the caller may proceed with a tick, false when another
// goroutine is already running one for this actor and the caller
// should drop. Always paired with releaseAgentTick on the success
// path (typically via defer in runAgentTick).
//
// Why: two cascades aimed at the same NPC (PC-speak + chronicler
// attend-to in close succession, or two separate cascades within the
// dedup-bypass window) previously produced concurrent LLM calls,
// duplicate action_log rows, and double tool side effects (e.g. two
// serve calls back-to-back, decrementing inventory twice for one
// observed event). The scene-level dedup at claimSceneTick gates
// per-(sceneID, actorID) and so doesn't catch cross-scene collisions.
// This gate is per-actorID across all scenes.
func (app *App) tryClaimAgentTick(actorID string) bool {
	app.AgentTickInFlightMu.Lock()
	defer app.AgentTickInFlightMu.Unlock()
	if app.AgentTickInFlight[actorID] {
		return false
	}
	app.AgentTickInFlight[actorID] = true
	return true
}

// releaseAgentTick clears the in-flight gate for actorID. Idempotent
// against accidental double-release (delete on a missing key is a
// no-op). Always called from the goroutine that successfully claimed
// the gate, typically via defer at the top of runAgentTick.
func (app *App) releaseAgentTick(actorID string) {
	app.AgentTickInFlightMu.Lock()
	defer app.AgentTickInFlightMu.Unlock()
	delete(app.AgentTickInFlight, actorID)
}

// runSceneTickCleanup evicts stale entries from SceneTickedActors so
// the map doesn't grow unbounded. Runs every 5 minutes; entries older
// than sceneTickStaleness are dropped. Cheap — bounded by the number
// of unique (scene, actor) pairs in the recent past.
func (app *App) runSceneTickCleanup(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-sceneTickStaleness)
			app.SceneTickedActorsMu.Lock()
			for k, v := range app.SceneTickedActors {
				if v.lastAt.Before(cutoff) {
					delete(app.SceneTickedActors, k)
				}
			}
			app.SceneTickedActorsMu.Unlock()
		}
	}
}

func main() {
	// Required environment variables
	databaseURL := requireEnv("DATABASE_URL")
	port := getEnv("PORT", "8080")
	llmMemoryURL := getEnv("LLM_MEMORY_URL", "http://127.0.0.1:3100")
	// Engine API key for the salem-engine actor on llm-memory-api. Every chat
	// to an NPC originates from salem-engine; realm overlap on the API side
	// grants it access to all NPCs in the salem realm.
	engineKey := requireEnv("LLM_MEMORY_ENGINE_KEY")

	// Connect to database
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	app := &App{
		DB:            pool,
		LLMMemoryURL:  llmMemoryURL,
		Hub:           NewEventHub(),
		NPCMovement:   newNPCMovement(),
		NPCBehaviors:  newNPCBehaviors(),
		npcChatClient: newNPCChatClient(llmMemoryURL, engineKey),
		// Capacity 2 — concurrent cascade chronicler fires. PC speech +
		// NPC arrival can briefly overlap; more than that gets skipped.
		ChroniclerSem: make(chan struct{}, 2),
		// Capacity 4 — concurrent attend_to-spawned agent ticks. Bounds
		// burst when the overseer dispatches multiple villagers at once.
		OverseerAttendSem: make(chan struct{}, 4),
		// Per-(sceneID, actorID) dedup map. See SceneTickedActors comment
		// on the App struct for the why.
		SceneTickedActors: make(map[string]sceneTickEntry),
		// Per-actor in-flight tick gate. See AgentTickInFlight comment
		// on the App struct for the why.
		AgentTickInFlight: make(map[string]bool),
		// Queue for agent-NPC shift boundary events (chronicler-dispatch
		// redesign). Empty at startup; populated by the worker scheduler
		// and drained by chronicler fires.
		ChroniclerDispatchQueue: newChroniclerDispatchQueue(),
	}
	// Prime the display-name map so reactive ticks before the first
	// server-tick refresh have data. Cheap; bounded by NPC count.
	app.refreshNPCDisplayNames(context.Background())

	// Periodic cleanup of stale scene-tick entries. Without this the map
	// grows unbounded as the world runs. 5-minute interval, 30-minute
	// staleness threshold — well past any realistic cascade duration.
	go app.runSceneTickCleanup(context.Background())

	// Build router. Routes are registered via two helpers: authed() wraps
	// the handler in requireLLMMemory; public() leaves it unwrapped. Default
	// is authed — anything public must explicitly opt out, so forgetting to
	// authenticate a new route is caught by review, not shipped to prod.
	mux := http.NewServeMux()
	authed := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, app.requireLLMMemory(handler))
	}
	public := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, handler)
	}

	// Public routes — explicitly unauthenticated. Keep this list short and
	// justify each entry in a comment.
	//
	// /api/assets: the client loads the asset catalog on boot, before the
	// user logs in, so the initial scene has textures to render.
	//
	// /api/village/events: WebSocket. Browsers can't attach Authorization
	// headers to WS handshakes, so the endpoint auths via ?token= query
	// param inside handleVillageEvents — effectively authed, just not via
	// middleware.
	public("GET /api/assets", app.handleListAssets)
	public("GET /api/village/events", app.handleVillageEvents)

	// Identity / catalog edits
	authed("GET /api/me", app.handleVillageMe)
	authed("PATCH /api/assets/{id}/footprint", app.handlePatchAssetFootprint)
	authed("PATCH /api/assets/{id}/door", app.handlePatchAssetDoor)
	authed("PATCH /api/assets/{id}/enterable", app.handlePatchAssetEnterable)
	authed("PATCH /api/assets/{id}/visible-when-inside", app.handlePatchAssetVisibleWhenInside)
	authed("PATCH /api/assets/{id}/stand", app.handlePatchAssetStand)
	authed("GET /api/assets/state-tags", app.handleListStateTags)
	authed("POST /api/assets/{id}/states/{state}/tags", app.handleAddStateTag)
	authed("DELETE /api/assets/{id}/states/{state}/tags/{tag}", app.handleRemoveStateTag)

	// Agents
	authed("GET /api/village/agents", app.handleListVillageAgents)
	authed("POST /api/village/agents/{id}/move", app.handleMoveAgent)

	// Placed objects
	authed("GET /api/village/objects", app.handleListVillageObjects)
	authed("POST /api/village/objects", app.handleCreateVillageObject)
	authed("POST /api/village/objects/bulk", app.handleBulkCreateVillageObjects)
	authed("DELETE /api/village/objects/{id}", app.handleDeleteVillageObject)
	authed("PATCH /api/village/objects/{id}/owner", app.handleSetVillageObjectOwner)
	authed("PATCH /api/village/objects/{id}/name", app.handleSetVillageObjectDisplayName)
	authed("PATCH /api/village/objects/{id}/state", app.handleSetVillageObjectState)
	authed("PATCH /api/village/objects/{id}/position", app.handleMoveVillageObject)
	authed("PATCH /api/village/objects/{id}/loiter-offset", app.handleSetVillageObjectLoiterOffset)

	// Object refresh — finite-supply attribute restoration on arrival
	// (ZBBS-090). Lookup table for the attribute picker; per-object set
	// for editing the configured rows.
	authed("GET /api/refresh-attributes", app.handleListRefreshAttributes)
	authed("GET /api/village/objects/{id}/refresh", app.handleGetObjectRefresh)
	authed("PUT /api/village/objects/{id}/refresh", app.handlePutObjectRefresh)

	// Inventory + items (ZBBS-091). Lookup table for the item picker;
	// per-actor inventory for editing.
	authed("GET /api/items", app.handleListItems)
	authed("GET /api/village/npcs/{id}/inventory", app.handleGetActorInventory)
	authed("PUT /api/village/npcs/{id}/inventory", app.handlePutActorInventory)

	// Player character endpoints (M6.7)
	authed("POST /api/village/pc/me", app.handlePCMe)
	authed("POST /api/village/pc/create", app.handlePCCreate)
	authed("POST /api/village/pc/say", app.handlePCSay)
	authed("POST /api/village/pc/speak", app.handlePCSpeak)
	authed("GET /api/village/object-tags", app.handleListObjectTags)
	authed("POST /api/village/objects/{id}/tags", app.handleAddObjectTag)
	authed("DELETE /api/village/objects/{id}/tags/{tag}", app.handleRemoveObjectTag)

	// Village log (ZBBS-087) — backload for the Village tab and the
	// top-bar marquee ticker's initial state.
	authed("POST /api/village/log/recent", app.handleVillageLogRecent)
	authed("POST /api/village/environment/recent", app.handleVillageEnvironmentRecent)

	// Terrain grid
	authed("GET /api/village/terrain", app.handleGetTerrain)
	authed("PUT /api/village/terrain", app.handleSaveTerrain)

	// NPCs — placed villagers with sprite catalog info inlined
	authed("GET /api/village/npcs", app.handleListNPCs)
	authed("POST /api/village/npcs", app.handleCreateNPC)
	authed("DELETE /api/village/npcs/{id}", app.handleDeleteNPC)
	authed("POST /api/village/npcs/{id}/walk-to", app.handleWalkTo)
	authed("PATCH /api/village/npcs/{id}/display-name", app.handleSetNPCDisplayName)
	authed("PATCH /api/village/npcs/{id}/sprite", app.handleSetNPCSprite)
	authed("PATCH /api/village/npcs/{id}/behavior", app.handleSetNPCBehavior)
	authed("PATCH /api/village/npcs/{id}/agent", app.handleSetNPCAgent)
	authed("PATCH /api/village/npcs/{id}/home-structure", app.handleSetNPCHomeStructure)
	authed("PATCH /api/village/npcs/{id}/work-structure", app.handleSetNPCWorkStructure)
	authed("PATCH /api/village/npcs/{id}/schedule", app.handleSetNPCSchedule)
	authed("PATCH /api/village/npcs/{id}/social", app.handleSetNPCSocial)
	authed("POST /api/village/npcs/{id}/run-cycle", app.handleRunNPCCycle)
	authed("POST /api/village/npcs/{id}/go-home", app.handleGoHome)
	authed("POST /api/village/npcs/{id}/go-to-work", app.handleGoToWork)
	authed("POST /api/village/npcs/{id}/reset-needs", app.handleResetNPCNeeds)
	authed("GET /api/village/npc-sprites", app.handleListNPCSprites)
	authed("GET /api/village/npc-behaviors", app.handleListNPCBehaviors)

	// World day/night cycle + daily rotation
	authed("GET /api/village/world", app.handleGetWorldState)
	authed("POST /api/village/world/force-phase", app.handleForcePhase)
	authed("POST /api/village/world/force-rotate", app.handleForceRotate)
	authed("POST /api/village/world/zoom-settings", app.handleSetZoomSettings)
	authed("POST /api/village/world/agent-ticks", app.handleSetAgentTicksPaused)

	// CORS middleware for Godot web client
	handler := corsMiddleware(mux)

	server := &http.Server{
		Addr:        ":" + port,
		Handler:     handler,
		ReadTimeout: 10 * time.Second,
		// No WriteTimeout — WebSocket connections are long-lived.
		// Individual write deadlines are set per-message in the WS handler.
		IdleTimeout: 120 * time.Second,
	}

	// Graceful shutdown — signals cancel the ticker context and trigger
	// server.Shutdown. The ticker goroutine exits on ctx.Done().
	tickerCtx, cancelTicker := context.WithCancel(context.Background())
	go app.runServerTick(tickerCtx)

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		log.Println("Shutting down...")
		cancelTicker()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	log.Printf("Salem 1692 engine listening on :%s", port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

// CORS middleware — allows the Angular client to make cross-origin requests.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// requireEnv reads an environment variable or exits if missing.
func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("Required environment variable %s is not set", key)
	}
	return val
}

// getEnv reads an environment variable with a fallback default.
func getEnv(key, fallback string) string {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	return val
}

