// Command engine is the v2 sim-engine entrypoint: it boots a sim.World from
// Postgres, wires the full runtime (tickers, cascades, the agent-tick
// pipeline, the periodic checkpointer), runs the world's command loop, and on
// SIGINT/SIGTERM takes a final checkpoint before exiting.
//
// The client-facing surface is served when PORT (→ HTTPAddr) is set: the REST
// read endpoints plus the WS /events push channel (movement events today). An
// empty HTTPAddr runs headless (used by the lifecycle test).
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/httpapi"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm/memapi"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/pg"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// finalCheckpointTimeout bounds the shutdown checkpoint so a wedged DB can't
// hang the process forever on the way out.
const finalCheckpointTimeout = 30 * time.Second

// worldStopTimeout bounds how long shutdown waits for World.Run to exit after
// the world context is cancelled, so a stuck command loop can't hang exit.
const worldStopTimeout = 10 * time.Second

// httpShutdownTimeout bounds the graceful HTTP server drain on shutdown.
const httpShutdownTimeout = 5 * time.Second

// runtime bundles the dependencies run needs. World is already loaded +
// finalized by the caller (so the choice of repo / load orchestrator stays in
// main, and tests can supply a mem-backed world). Save adapts the durable
// checkpoint writer. TickSink may be nil — the worker pool null-checks it.
// HTTPAddr is the read-surface listen address (e.g. ":8080"); empty disables
// the HTTP server (headless-only, used by the lifecycle test). Auth verifies
// session tokens for the read surface — required when HTTPAddr is set, may be
// nil when headless.
//
// Umbilical, when non-nil, is the tick-telemetry ring buffer that both backs
// the TickSink AND enables the operator-gated umbilical debug surface (run
// hands it to the Server via SetTelemetry). Nil = umbilical disabled (the
// default): no surface, and TickSink falls back to the notImpl drop sink.
type runtime struct {
	World     *sim.World
	LLMClient llm.Client
	Save      sim.CheckpointFunc
	TickSink  sim.TickTelemetrySink
	HTTPAddr  string
	Auth      httpapi.Authenticator
	Umbilical *telemetry.RingSink
	// UmbilicalControl arms the world-mutating umbilical control routes. Only
	// meaningful when Umbilical is non-nil; run wires it via SetControlEnabled.
	UmbilicalControl bool
}

func main() {
	databaseURL := requireEnv("DATABASE_URL")
	llmMemoryURL := getEnv("LLM_MEMORY_URL", "http://127.0.0.1:3100")
	port := getEnv("PORT", "8080")
	// Every NPC tick's LLM call is authenticated as salem-engine on
	// llm-memory-api; the cascades that author prose need the same client.
	engineKey := requireEnv("LLM_MEMORY_ENGINE_KEY")

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("engine: connect database: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("engine: ping database: %v", err)
	}

	repo := pg.NewRepository(pool)

	// Umbilical (off by default). When UMBILICAL_ENABLED is set, build the
	// tick-telemetry ring and install it as repo.TickTelemetry BEFORE LoadWorld
	// — the loaded world copies the repo by value, so the ring must be in place
	// first to reach BOTH telemetry writers: the reactor evaluator (w.repo.
	// TickTelemetry) and the worker pool (rt.TickSink, read from repo.TickTelemetry
	// below). The same ring is handed to the HTTP server (run → SetTelemetry),
	// which is what registers the umbilical routes. When disabled, TickTelemetry
	// stays the notImpl drop sink and no umbilical surface exists.
	var umbilical *telemetry.RingSink
	umbilicalControl := false
	if envBool("UMBILICAL_ENABLED") {
		umbilical = telemetry.New(getEnvInt("UMBILICAL_TELEMETRY_BUFFER", telemetry.DefaultCapacity))
		repo.TickTelemetry = umbilical
		// Control (world-mutating) routes are a second, independent opt-in — only
		// honored when the umbilical itself is on, so read-only is the default.
		umbilicalControl = envBool("UMBILICAL_CONTROL_ENABLED")
		log.Printf("engine: umbilical ENABLED (telemetry buffer=%d, control=%v)", umbilical.Stats().Capacity, umbilicalControl)
	}

	// requireAllImpl=true is the production gate: LoadWorld hard-fails if any
	// LOAD sub-repo is still a notImpl stub. ActionLog + TickTelemetry remain
	// notImpl, but they're write-only sinks LoadWorld never reads, so the gate
	// passes. A cold-loaded World is fully finalized (FinalizeLoad ran inside).
	world, err := pg.LoadWorld(context.Background(), repo, true)
	if err != nil {
		log.Fatalf("engine: load world: %v", err)
	}

	// Install the durable terminal-order sink. Without it, finalizeOrderTerminal
	// runs its legacy no-prune path: terminal Orders are never written through to
	// pg at transition time NOR pruned from w.Orders, so they accumulate in memory
	// and in every published snapshot for the life of the process (checkpoint still
	// reconciles pg, so it's bloat, not data loss). repo.Orders satisfies the narrow
	// TerminalOrderSink via its WriteTerminal method. pg.LoadWorld doesn't run the
	// restartExpirePendingOrders pass, so there's no before-load ordering to honor.
	world.SetTerminalOrderSink(repo.Orders)

	rt := runtime{
		World:     world,
		LLMClient: memapi.NewClient(llmMemoryURL, engineKey),
		Save: func(ctx context.Context, cp *sim.CheckpointSnapshot) error {
			return pg.SaveWorld(ctx, repo, cp)
		},
		// TickSink is the ring when the umbilical is enabled (set on repo above),
		// otherwise the notImpl drop sink. The slot is always occupied.
		TickSink: repo.TickTelemetry,
		HTTPAddr: ":" + port,
		// Read-surface auth: verifies session tokens against llm-memory-api's
		// /v1/auth/verify + the salem-realm gate, caching positive results.
		Auth:             httpapi.NewTokenVerifier(llmMemoryURL, 0),
		Umbilical:        umbilical,
		UmbilicalControl: umbilicalControl,
	}

	// Shutdown on SIGINT/SIGTERM.
	stop := make(chan struct{})
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		close(stop)
	}()

	if err := run(rt, stop); err != nil {
		log.Fatalf("engine: %v", err)
	}
	log.Println("engine: stopped")
}

// run wires the full runtime onto an already-loaded world, operates it until
// stop is closed, then performs the graceful shutdown sequence (final
// checkpoint) and returns once the world goroutine has stopped.
func run(rt runtime, stop <-chan struct{}) error {
	// Two independent contexts. worldCtx drives the world goroutine plus every
	// ticker, cascade sweep, and the tick-worker pool — everything that must
	// stay alive to build the final checkpoint. checkpointerCtx drives only
	// the periodic checkpointer, so shutdown can stop the periodic writes
	// FIRST (and force one authoritative final write) while the world is still
	// running. See the shutdown sequence below.
	worldCtx, cancelWorld := context.WithCancel(context.Background())
	defer cancelWorld()
	checkpointerCtx, cancelCheckpointer := context.WithCancel(context.Background())
	defer cancelCheckpointer()

	// Agent-tick execution pipeline: tool registry → harness → worker pool.
	// The registry is the set of tools an NPC's LLM may call during a tick.
	registry := handlers.NewRegistry()
	// recall (observation) reaches llm-memory-api for memory search; the
	// production LLM client (memapi) implements llm.MemorySearcher. Assert it
	// here so a client that can't search fails loudly at startup rather than
	// at the first recall call.
	searcher, ok := rt.LLMClient.(llm.MemorySearcher)
	if !ok {
		return fmt.Errorf("engine: LLM client %T does not implement llm.MemorySearcher (recall needs it)", rt.LLMClient)
	}
	if err := registerTools(registry, searcher); err != nil {
		return err
	}
	harness, err := handlers.NewHarness(handlers.HarnessConfig{
		Client:   rt.LLMClient,
		Registry: registry,
	})
	if err != nil {
		return fmt.Errorf("build tick harness: %w", err)
	}
	tickPool := handlers.NewTickWorkerPoolWithHarness(rt.World, rt.TickSink, harness)

	// Wire everything that subscribes to world events or installs world-level
	// controllers. All of this must happen BEFORE world.Run starts processing
	// commands (subscriber registration mutates world state directly). Cascade
	// sweep goroutines spawned here block on their initial settings read until
	// Run starts — harmless.
	handlers.RegisterTickHandlers(rt.World, tickPool) // admission controller + ReactorTickDue subscriber (one unit)
	handlers.RegisterSpeechHandlers(rt.World)
	handlers.RegisterPayHandlers(rt.World)
	handlers.RegisterDwellHandlers(rt.World)
	handlers.RegisterConsumeHandlers(rt.World)
	handlers.RegisterSceneQuoteHandlers(rt.World)
	handlers.RegisterPayWithItemHandlers(rt.World)
	sim.RegisterAcquaintanceSubscriber(rt.World)
	sim.RegisterSleepSubscriber(rt.World) // ZBBS-HOME-284 #2: auto-sleep NPCs on arrival home
	cascade.RegisterProductionCascades(worldCtx, rt.World, rt.LLMClient)

	// WebSocket event hub (Slice 2 WS /events). Subscribed before world.Run,
	// like every other subscriber; its Run goroutine owns the client fan-out.
	// Only wired when the HTTP surface is enabled (it serves the /events route).
	var eventsHub *httpapi.Hub
	if rt.HTTPAddr != "" {
		eventsHub = httpapi.NewHub(httpapi.TranslateEvent)
		rt.World.Subscribe(eventsHub)
		go eventsHub.Run(worldCtx)
	}

	// Start the world command loop. This stamps world.LifecycleContext, which
	// the sweep goroutines' AfterFunc re-arm chains key off. worldDone closes
	// when Run returns — shutdown waits on it so the process doesn't tear down
	// dependencies (the pgxpool) while the world goroutine is still unwinding.
	worldDone := make(chan struct{})
	go func() {
		rt.World.Run(worldCtx)
		close(worldDone)
	}()

	// Launch the worker pool (workers complete ticks via SendContext to world)
	// and every periodic ticker/sweep, all bound to worldCtx.
	tickPool.Start(worldCtx)
	startTickers(worldCtx, rt.World)

	// Periodic checkpointer. checkpointerDone closes when the loop has fully
	// stopped — the shutdown path waits on it before forcing the final
	// checkpoint, so the two never overlap.
	checkpointerDone := make(chan struct{})
	go func() {
		sim.RunCheckpointer(checkpointerCtx, rt.World, rt.Save)
		close(checkpointerDone)
	}()

	// Read surface (Slice 2). Handlers read world.Published() lock-free, so the
	// HTTP server is independent of the world goroutine's liveness — it's shut
	// down first on the way out (stop accepting reads before anything else
	// unwinds). Skipped when HTTPAddr is empty (headless-only).
	var httpServer *http.Server
	if rt.HTTPAddr != "" {
		server := httpapi.NewServer(rt.World, rt.Auth)
		server.SetEventsHub(eventsHub)
		// Enables the operator-gated umbilical routes. Nil when UMBILICAL_ENABLED
		// is unset → SetTelemetry not called → routes never registered.
		if rt.Umbilical != nil {
			server.SetTelemetry(rt.Umbilical)
			if rt.UmbilicalControl {
				server.SetControlEnabled(true)
			}
		}
		httpServer = &http.Server{
			Addr:    rt.HTTPAddr,
			Handler: server.Handler(),
		}
		go func() {
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("engine: http server: %v", err)
			}
		}()
		log.Printf("engine: read surface listening on %s", rt.HTTPAddr)
	}

	log.Printf("engine: v2 sim engine running")

	<-stop
	log.Println("engine: shutting down...")

	// Stop accepting HTTP reads first, before any runtime teardown.
	if httpServer != nil {
		httpCtx, cancelHTTP := context.WithTimeout(context.Background(), httpShutdownTimeout)
		if err := httpServer.Shutdown(httpCtx); err != nil {
			log.Printf("engine: http shutdown: %v", err)
		}
		cancelHTTP()
	}

	// Shutdown order is load-bearing:
	//
	//  1. Stop the periodic checkpointer and wait for it to fully exit. Any
	//     in-flight periodic write is cancelled and rolls back atomically,
	//     leaving the prior checkpoint intact; the final write below
	//     supersedes it.
	//  2. Stop the tick pool and drain workers. Their in-flight commits
	//     complete against the still-running world goroutine.
	//  3. Force ONE final checkpoint with a fresh (uncancelled) context while
	//     the world goroutine is still alive — this is the authoritative
	//     persisted state.
	//  4. Cancel worldCtx and WAIT for World.Run to exit: only then is it safe
	//     for the caller to tear down the repo/pool.
	cancelCheckpointer()
	<-checkpointerDone

	tickPool.Stop()
	tickPool.Wait()

	finalCtx, cancelFinal := context.WithTimeout(context.Background(), finalCheckpointTimeout)
	if err := sim.CheckpointNow(finalCtx, rt.World, rt.Save); err != nil {
		// Don't fail the whole shutdown on a final-checkpoint error — the
		// prior checkpoint is still intact. Log and proceed to stop the world.
		log.Printf("engine: final checkpoint failed: %v", err)
	} else {
		log.Println("engine: final checkpoint written")
	}
	cancelFinal()

	// Stop the world and block until Run has actually returned. cancelWorld is
	// also deferred (cleanup for early returns before the world starts);
	// calling it explicitly here makes the normal-path wait unambiguous. The
	// tickers/cascades are keyed to worldCtx and exit alongside Run; none of
	// them touch the repo directly (only the now-stopped checkpointer did), so
	// waiting on Run is sufficient before the caller closes the pool.
	cancelWorld()
	select {
	case <-worldDone:
	case <-time.After(worldStopTimeout):
		return fmt.Errorf("world did not stop within %s of cancellation", worldStopTimeout)
	}

	return nil
}

// registerTools installs every production tick tool into the registry. There
// is deliberately no handlers.RegisterAllProductionTools helper — the tool
// surface is a composition choice the entrypoint owns. A registration failure
// is a wiring bug, surfaced to the caller to fail loudly at startup.
func registerTools(r *handlers.Registry, searcher llm.MemorySearcher) error {
	register := []struct {
		name string
		fn   func(*handlers.Registry) error
	}{
		{"speak", handlers.RegisterSpeak},
		{"pay", handlers.RegisterPay},
		{"consume", handlers.RegisterConsume},
		{"scene_quote", handlers.RegisterSceneQuote},
		{"deliver_order", handlers.RegisterDeliverOrder},
		{"pay_with_item_family", handlers.RegisterPayWithItemFamily},
		{"take_break", handlers.RegisterTakeBreak}, // ZBBS-HOME-284 #4
		{"move_to", handlers.RegisterMoveTo},       // ZBBS-HOME-285
	}
	for _, t := range register {
		if err := t.fn(r); err != nil {
			return fmt.Errorf("register tool %s: %w", t.name, err)
		}
	}
	// recall (ZBBS-WORK-321) — observation tool; registered separately
	// because it needs the memory searcher (the others take only *Registry).
	if err := handlers.RegisterRecall(r, searcher); err != nil {
		return fmt.Errorf("register tool recall: %w", err)
	}
	return nil
}

// startTickers launches every periodic substrate ticker/sweep in its own
// goroutine, all bound to ctx (= worldCtx). Cascade-owned sweeps are started
// separately by RegisterProductionCascades; these are the core sim tickers
// that live in the sim package itself.
func startTickers(ctx context.Context, w *sim.World) {
	go sim.RunReactorEvaluator(ctx, w)
	go sim.RunLocomotionTicker(ctx, w)
	go sim.RunPhaseTicker(ctx, w)
	go sim.RunNeedsTicker(ctx, w)
	go sim.RunTirednessRecoveryTicker(ctx, w)
	go sim.RunSleepTicker(ctx, w)
	go sim.RunShiftTicker(ctx, w)  // ZBBS-WORK-278: shift/duty producer
	go sim.RunSocialTicker(ctx, w) // ZBBS-WORK-279 (4b): social scheduler (decorative mover)

	go sim.RunDwellTicker(ctx, w)
	go sim.RunProduceTicker(ctx, w)
	go sim.RunObjectRefreshRegen(ctx, w)
	go sim.RunOrderSweep(ctx, w)
	go sim.RunPayLedgerSweep(ctx, w)
	go sim.RunRoomSweep(ctx, w)
	go sim.RunSceneQuoteSweep(ctx, w)
	go sim.RunRotationTicker(ctx, w, sim.RotationScope{}) // empty scope = bulk-rotate everything
}

// requireEnv reads an environment variable or exits if missing.
func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("engine: required environment variable %s is not set", key)
	}
	return val
}

// getEnv reads an environment variable with a fallback default.
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// envBool reports whether key is set to a truthy value (1/t/true/yes/on, any
// case). Unset or any other value is false — so the umbilical fails closed
// (disabled) on a missing or malformed flag.
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "t", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// getEnvInt reads key as an int, falling back to fallback when unset or
// unparseable (or non-positive — the buffer size must be positive).
func getEnvInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return v
	}
	return fallback
}
