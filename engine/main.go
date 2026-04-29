package main

import (
	"context"
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
	}
	// Prime the display-name map so reactive ticks before the first
	// server-tick refresh have data. Cheap; bounded by NPC count.
	app.refreshNPCDisplayNames(context.Background())

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

	// Player character endpoints (M6.7)
	authed("POST /api/village/pc/me", app.handlePCMe)
	authed("POST /api/village/pc/create", app.handlePCCreate)
	authed("POST /api/village/pc/say", app.handlePCSay)
	authed("POST /api/village/pc/speak", app.handlePCSpeak)
	authed("GET /api/village/object-tags", app.handleListObjectTags)
	authed("POST /api/village/objects/{id}/tags", app.handleAddObjectTag)
	authed("DELETE /api/village/objects/{id}/tags/{tag}", app.handleRemoveObjectTag)

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

