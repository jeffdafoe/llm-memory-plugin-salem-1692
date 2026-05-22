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
type runtime struct {
	World     *sim.World
	LLMClient llm.Client
	Save      sim.CheckpointFunc
	TickSink  sim.TickTelemetrySink
	HTTPAddr  string
	Auth      httpapi.Authenticator
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

	// requireAllImpl=true is the production gate: LoadWorld hard-fails if any
	// LOAD sub-repo is still a notImpl stub. ActionLog + TickTelemetry remain
	// notImpl, but they're write-only sinks LoadWorld never reads, so the gate
	// passes. A cold-loaded World is fully finalized (FinalizeLoad ran inside).
	world, err := pg.LoadWorld(context.Background(), repo, true)
	if err != nil {
		log.Fatalf("engine: load world: %v", err)
	}

	rt := runtime{
		World:     world,
		LLMClient: memapi.NewClient(llmMemoryURL, engineKey),
		Save: func(ctx context.Context, cp *sim.CheckpointSnapshot) error {
			return pg.SaveWorld(ctx, repo, cp)
		},
		// notImpl sink today (silently drops); the slot is occupied so a real
		// sink later is a drop-in.
		TickSink: repo.TickTelemetry,
		HTTPAddr: ":" + port,
		// Read-surface auth: verifies session tokens against llm-memory-api's
		// /v1/auth/verify + the salem-realm gate, caching positive results.
		Auth: httpapi.NewTokenVerifier(llmMemoryURL, 0),
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
	if err := registerTools(registry); err != nil {
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
	handlers.RegisterSceneQuoteHandlers(rt.World)
	handlers.RegisterPayWithItemHandlers(rt.World)
	sim.RegisterAcquaintanceSubscriber(rt.World)
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
func registerTools(r *handlers.Registry) error {
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
	}
	for _, t := range register {
		if err := t.fn(r); err != nil {
			return fmt.Errorf("register tool %s: %w", t.name, err)
		}
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
