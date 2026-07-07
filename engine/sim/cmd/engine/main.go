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

	// Embed the timezone database so America/New_York always resolves, even on a
	// stripped image with no system zoneinfo. Without it, localMinuteOfDay and
	// the phase ticker silently fall back to UTC, shifting the whole village
	// clock — wrong day phases and wrong schedule-aware steering (ZBBS-HOME-351,
	// HOME-352). ~450KB in the binary; correctness over size here.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/chatlog"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/httpapi"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm/memapi"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/promptlog"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/pg"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/simpush"
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
	// PromptRing captures rendered deliberation prompts for the umbilical's
	// /agent/prompts route (ZBBS-HOME-360). Non-nil only when the umbilical is
	// enabled; run wires it to BOTH the harness (PromptSink) and the server
	// (SetPrompts). Nil = no prompt capture.
	PromptRing *promptlog.RingSink
	// ChatRing captures the engine<->model chat exchange per scene for the
	// umbilical's /chat route (ZBBS-HOME-382). Non-nil only when the umbilical is
	// enabled; run wires it to BOTH the harness (ChatSink) and the server
	// (SetChat). Nil = no chat capture.
	ChatRing *chatlog.RingSink
	// MemoryAPIBaseURL is the llm-memory-api root the umbilical /turns route
	// (ZBBS-HOME-396) proxies raw-LLM-turn queries to, forwarding the operator's
	// bearer token. The full turn (system_prompt, tokens, cost, provider status)
	// lives only in memory-api; the engine never sees it. Wired by run via
	// SetMemoryAPIBaseURL when the umbilical is enabled.
	MemoryAPIBaseURL string
	// ActionLog is the durable agent_action_log writer (ZBBS-WORK-376). Its
	// Append is already installed on the World (SetActionLogSink in main); run
	// owns the writer-goroutine lifecycle — started before world.Run, drained
	// AFTER the world goroutine stops (no more Appends) and BEFORE main closes
	// the pool. Nil in the headless lifecycle test (mem-backed, no pg).
	ActionLog *pg.ActionLogRepo
	// SimPush is the daily sim-conversation push (ZBBS-WORK-376 piece 3): once
	// per UTC day it POSTs each agentized actor's completed-day action rows to
	// llm-memory-api so the stateful NPCs' dream cron has input. run owns its
	// goroutine (bound to worldCtx, waited at shutdown before the pool closes).
	// Nil in the headless lifecycle test (mem-backed, no pg).
	SimPush *simpush.Dispatcher
	// RecipeWriter is the durable item_recipe upsert behind the operator-gated
	// /umbilical/recipe/set route (LLM-97). run wires it via SetRecipeWriter when
	// the umbilical control surface is enabled. Nil in the headless lifecycle
	// test (mem-backed, no pg) → the route answers 503.
	RecipeWriter *pg.RecipesRepo
	// SatisfiesWriter is the durable item_satisfies upsert behind the operator-
	// gated /umbilical/item/set-satisfies route (LLM-119). run wires it via
	// SetSatisfiesWriter when the umbilical control surface is enabled. Nil in the
	// headless lifecycle test (mem-backed, no pg) → the route answers 503.
	SatisfiesWriter *pg.ItemKindsRepo
	// ItemKindWriter is the durable item_kind upsert behind the operator-gated
	// /umbilical/item/set route (LLM-200). Same repo type as SatisfiesWriter —
	// both writes live on pg.ItemKindsRepo — but wired separately so each route's
	// nil-guard stays independent. run wires it via SetItemKindWriter when the
	// umbilical control surface is enabled. Nil in the headless lifecycle test
	// (mem-backed, no pg) → the route answers 503.
	ItemKindWriter *pg.ItemKindsRepo
	// AssetGeometryWriter is the durable asset-geometry UPDATE behind the admin
	// PATCH /api/assets/{id}/door | /footprint | /stand editor routes (LLM-263).
	// Unlike the item/recipe writers this is wired whenever pg is present (not
	// only under the umbilical control flag) — it's a player-admin editor route.
	// run wires it via SetAssetGeometryWriter. Nil in the headless lifecycle test
	// (mem-backed, no pg) → those routes answer 503.
	AssetGeometryWriter *pg.AssetsRepo
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
	var promptRing *promptlog.RingSink
	var chatRing *chatlog.RingSink
	umbilicalControl := false
	if envBool("UMBILICAL_ENABLED") {
		umbilical = telemetry.New(getEnvInt("UMBILICAL_TELEMETRY_BUFFER", telemetry.DefaultCapacity))
		repo.TickTelemetry = umbilical
		// Per-actor rendered-prompt ring for the /agent/prompts debug route
		// (ZBBS-HOME-360). Separate from the telemetry ring because it carries raw
		// prompts the telemetry contract forbids. Wired to the harness + server in run().
		promptRing = promptlog.New(getEnvInt("UMBILICAL_PROMPT_BUFFER", promptlog.DefaultPerActorCapacity))
		// Per-scene chat-exchange ring for the /chat debug route (ZBBS-HOME-382):
		// the rendered perception (tx) + the model's responses (rx) per scene, so
		// an operator can read an NPC's conversation off the umbilical without an
		// llm-memory login. Wired to the harness (ChatSink) + server (SetChat) in run().
		chatRing = chatlog.New(getEnvInt("UMBILICAL_CHAT_BUFFER", chatlog.DefaultPerSceneCapacity))
		// Control (world-mutating) routes are a second, independent opt-in — only
		// honored when the umbilical itself is on, so read-only is the default.
		umbilicalControl = envBool("UMBILICAL_CONTROL_ENABLED")
		log.Printf("engine: umbilical ENABLED (telemetry buffer=%d, prompt buffer=%d/actor, chat buffer=%d/scene x %d scenes, control=%v)",
			umbilical.Stats().Capacity, promptRing.Stats().PerActorCapacity, chatRing.Stats().PerSceneCapacity, chatRing.Stats().MaxScenes, umbilicalControl)
	}

	// requireAllImpl=true is the production gate: LoadWorld hard-fails if any
	// LOAD sub-repo is still a notImpl stub. ActionLog + TickTelemetry remain
	// notImpl, but they're write-only sinks LoadWorld never reads, so the gate
	// passes. A cold-loaded World is fully finalized (FinalizeLoad ran inside).
	world, err := pg.LoadWorld(context.Background(), repo, true)
	if err != nil {
		log.Fatalf("engine: load world: %v", err)
	}

	// LLM-60: non-fatal config audit of the loaded world. A refresh-bearing
	// (gather/eat) object with no display_name is silently unreachable — the
	// command resolver (resolveLoiteringObject) skips nameless objects, so neither
	// the gather verb nor eat-on-arrival can resolve it, though the perception cue
	// still advertises it. Logged here so a boot-time defect (e.g. a migration that
	// forgot a name) lands in journalctl, and also surfaced live on /umbilical/state.
	// NEVER fatal — a mislabeled bush must not stop the village from booting.
	for _, warning := range sim.ConfigWarnings(world.VillageObjects) {
		log.Printf("engine: config warning: %s", warning)
	}

	// ZBBS-HOME-417: drop every checkpoint-reloaded huddle before the world
	// goroutine starts. A conversation is transient state — a standing huddle
	// "resuming" hours later across a restart is meaningless, and reloading them
	// is what let a structure's huddle live for days. Durable scenes are kept.
	// Safe to mutate directly here: World.Run hasn't started, so the world is
	// single-threaded in this boot window.
	sim.ClearConversationalHuddlesOnBoot(world)

	// Install the durable terminal-order sink. Without it, finalizeOrderTerminal
	// runs its legacy no-prune path: terminal Orders are never written through to
	// pg at transition time NOR pruned from w.Orders, so they accumulate in memory
	// and in every published snapshot for the life of the process (checkpoint still
	// reconciles pg, so it's bloat, not data loss). repo.Orders satisfies the narrow
	// TerminalOrderSink via its WriteTerminal method. pg.LoadWorld doesn't run the
	// restartExpirePendingOrders pass, so there's no before-load ordering to honor.
	world.SetTerminalOrderSink(repo.Orders)

	// Install the durable order-less settlement sink (LLM-246). consume_now
	// eat-here settlements and bundle takes mint no Order, so without this
	// write-through they never reach pay_ledger and the price-book restart
	// seed above every boot re-loses the village's highest-frequency trades
	// (tavern meals, stew, porridge). repo.Orders satisfies the narrow
	// OrderlessSettlementSink via its WriteOrderlessSettlement method.
	world.SetOrderlessSettlementSink(repo.Orders)

	// Install the durable agent_action_log sink (ZBBS-WORK-376). It feeds the
	// four stateful NPCs' nightly dream memory via llm-memory-api's daily sim
	// push. Async: Append enqueues on the world goroutine; the writer goroutine
	// (started in run, drained at shutdown before pool.Close) does the INSERT
	// off the world goroutine.
	actionLogSink := pg.NewActionLogRepo(pool)
	world.SetActionLogSink(actionLogSink)

	// Narration pool expansion (ZBBS-WORK-399): merge previously generated
	// narration lines into the seed pools, and install the durable sink the
	// expansion cascade writes new lines through. Merge failure is non-fatal
	// — the engine runs fine on seed-only pools; it just loses accumulated
	// variety until the table is reachable again.
	narrationRepo := pg.NewNarrationExpansionRepo(pool)
	if expansions, err := narrationRepo.LoadAll(context.Background()); err != nil {
		log.Printf("main: narration expansion load failed (continuing with seed pools): %v", err)
	} else {
		world.MergeNarrationExpansions(expansions)
	}
	world.SetNarrationExpansionSink(narrationRepo)

	// Daily sim-conversation push (ZBBS-WORK-376 piece 3). Reads the durable
	// agent_action_log the sink above writes, plus the actor roster + push
	// cursor, and POSTs each agentized actor's completed-day rows to
	// llm-memory-api's /v1/sim/conversation-day. Authenticated as salem-engine
	// with the same key the LLM client uses.
	simPush := simpush.NewDispatcher(
		pg.NewSimPushStore(pool),
		simpush.NewHTTPPoster(llmMemoryURL, engineKey),
	)

	// LLM-156: one memapi client, shared by the runtime's LLMClient slot and the
	// startup per-agent rate-limit query (installAgentRateLimits below).
	llmClient := memapi.NewClient(llmMemoryURL, engineKey)

	rt := runtime{
		World:     world,
		LLMClient: llmClient,
		Save: func(ctx context.Context, cp *sim.CheckpointSnapshot) error {
			return pg.SaveWorld(ctx, repo, cp)
		},
		// TickSink is the ring when the umbilical is enabled (set on repo above),
		// otherwise the notImpl drop sink. The slot is always occupied.
		TickSink: repo.TickTelemetry,
		HTTPAddr: ":" + port,
		// Read-surface auth: verifies session tokens against llm-memory-api's
		// /v1/auth/verify + the salem-realm gate, caching positive results.
		Auth:                httpapi.NewTokenVerifier(llmMemoryURL, 0),
		Umbilical:           umbilical,
		UmbilicalControl:    umbilicalControl,
		RecipeWriter:        pg.NewRecipesRepo(pool),
		SatisfiesWriter:     pg.NewItemKindsRepo(pool),
		ItemKindWriter:      pg.NewItemKindsRepo(pool),
		AssetGeometryWriter: pg.NewAssetsRepo(pool),
		PromptRing:          promptRing,
		ChatRing:            chatRing,
		MemoryAPIBaseURL:    llmMemoryURL,
		ActionLog:           actionLogSink,
		SimPush:             simPush,
	}

	// Shutdown on SIGINT/SIGTERM.
	stop := make(chan struct{})
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		close(stop)
	}()

	// LLM-156: install per-agent tick caps before run() starts the world loop,
	// so the reactor paces a shared VA's pool under its memory-api rate limit
	// instead of bursting into the cooldown. Startup-only; a fetch failure
	// installs conservative fallback pacing for the shared VAs (not unthrottled).
	rlCtx, rlCancel := context.WithTimeout(context.Background(), 10*time.Second)
	installAgentRateLimits(rlCtx, world, llmClient)
	rlCancel()

	if err := run(rt, stop); err != nil {
		log.Fatalf("engine: %v", err)
	}
	log.Println("engine: stopped")
}

// agentRateCapHeadroom scales memory-api's effective per-agent limit down to
// the cap the reactor paces against (LLM-156). The engine emits at most Cap
// ticks per window for a shared VA; memory-api trips one tick PAST its limit.
// Pacing strictly under the limit (rather than at it) absorbs the slack between
// the engine's emit-time window and memory-api's call-time window — a tick is
// counted server-side a beat after it's emitted, and the worker pool's small
// buffer lets a few bunch — so the server cooldown effectively never fires for
// engine traffic.
const agentRateCapHeadroom = 0.8

// fallbackAgentLimit / fallbackAgentWindow are the conservative pacing applied
// to the shared VAs when the startup rate-limit query FAILS — matching
// memory-api's documented global default (10 calls / 60s). Pacing the shared
// slugs at headroom under this beats reverting to the un-paced behavior that
// drops the whole pool into a cooldown; a drifted value here only bites on the
// rare boot where the query itself fails.
const (
	fallbackAgentLimit  = 10
	fallbackAgentWindow = time.Minute
)

// pacedCap scales a memory-api limit down to the cap the reactor paces against,
// never below 1.
func pacedCap(limit int) int {
	c := int(float64(limit) * agentRateCapHeadroom)
	if c < 1 {
		c = 1
	}
	return c
}

// sharedVASlugs are the switchboard VAs that aggregate many NPCs under one
// agent-name — the only slugs the per-agent pacing actually protects (a
// dedicated 1:1 VA can't burst a shared limit). Always queried, even when no
// actor of the slug exists at boot (visitors spawn on demand), so the slug is
// already gated the moment its first NPC ticks.
func sharedVASlugs() []string {
	return []string{sim.VendorAgentName, sim.VisitorAgentName, sim.GenericAgentName}
}

// installAgentRateLimits queries memory-api for the effective rate limit of the
// VA slugs the engine drives and installs per-agent tick caps on the world
// (LLM-156). MUST be called before the world loop starts (the write is
// unsynchronized — see World.SetAgentRateLimits). On a fetch failure it falls
// back to conservative pacing for the shared VAs rather than leaving the whole
// process un-paced for one transient boot-time API blip.
func installAgentRateLimits(ctx context.Context, world *sim.World, client *memapi.Client) {
	slugs := agentSlugsToQuery(world)
	fetched, err := client.FetchRateLimits(ctx, slugs)
	if err != nil {
		limits := make(map[string]sim.AgentRateLimit, len(sharedVASlugs()))
		for _, slug := range sharedVASlugs() {
			limits[slug] = sim.AgentRateLimit{Cap: pacedCap(fallbackAgentLimit), Window: fallbackAgentWindow}
		}
		world.SetAgentRateLimits(limits)
		log.Printf("engine: agent rate-limit fetch failed (%v); pacing shared VAs at conservative fallback %d/%s",
			err, pacedCap(fallbackAgentLimit), fallbackAgentWindow)
		return
	}
	limits := make(map[string]sim.AgentRateLimit, len(fetched))
	for slug, rl := range fetched {
		if rl.Limit <= 0 {
			continue
		}
		window := time.Duration(rl.WindowMS) * time.Millisecond
		if window <= 0 {
			window = fallbackAgentWindow
		}
		limits[slug] = sim.AgentRateLimit{Cap: pacedCap(rl.Limit), Window: window}
	}
	world.SetAgentRateLimits(limits)
	log.Printf("engine: installed per-agent tick caps for %d of %d VA(s) (%.0f%% headroom under the memory-api limit)",
		len(limits), len(slugs), agentRateCapHeadroom*100)
}

// agentSlugsToQuery is the union of the shared VAs and every distinct non-empty
// Actor.LLMAgent present at boot — the set the startup rate-limit query resolves.
func agentSlugsToQuery(world *sim.World) []string {
	seen := make(map[string]struct{})
	var slugs []string
	add := func(slug string) {
		if slug == "" {
			return
		}
		if _, ok := seen[slug]; ok {
			return
		}
		seen[slug] = struct{}{}
		slugs = append(slugs, slug)
	}
	for _, slug := range sharedVASlugs() {
		add(slug)
	}
	for _, a := range world.Actors {
		add(a.LLMAgent)
	}
	return slugs
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
	// actionLogWriterCtx drives the durable action-log writer goroutine
	// (ZBBS-WORK-376). Separate from worldCtx so shutdown can drain it AFTER the
	// world goroutine has stopped emitting (no more Appends) and BEFORE main
	// closes the pool.
	actionLogWriterCtx, cancelActionLogWriter := context.WithCancel(context.Background())
	defer cancelActionLogWriter()

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
	harnessCfg := handlers.HarnessConfig{
		Client:   rt.LLMClient,
		Registry: registry,
	}
	// Capture rendered prompts only when the umbilical (and its ring) is wired —
	// leaving PromptSink as a nil interface otherwise, so the harness skips the
	// capture entirely rather than calling a typed-nil sink each tick.
	if rt.PromptRing != nil {
		harnessCfg.PromptSink = rt.PromptRing
	}
	// Capture the chat exchange only when the umbilical (and its ring) is wired,
	// leaving ChatSink a nil interface otherwise (ZBBS-HOME-382).
	if rt.ChatRing != nil {
		harnessCfg.ChatSink = rt.ChatRing
	}
	harness, err := handlers.NewHarness(harnessCfg)
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
	handlers.RegisterSourceActivityHandlers(rt.World)  // LLM-69: NPC completion beat for timed eat/drink/harvest
	handlers.RegisterProductionCycleHandlers(rt.World) // LLM-319: NPC completion beat for a landed production batch — the wake to decide about the next one
	handlers.RegisterSceneQuoteHandlers(rt.World)
	handlers.RegisterPayWithItemHandlers(rt.World)
	handlers.RegisterLaborHandlers(rt.World) // LLM-187: wake the employer when a worker solicits work (accept_work/decline_work)
	sim.RegisterAcquaintanceSubscriber(rt.World)
	sim.RegisterSummonSubscriber(rt.World)                // ZBBS-HOME-311: advance summon errands on arrival + arrival-warrant suppression hook
	sim.RegisterSleepSubscriber(rt.World)                 // ZBBS-HOME-284 #2: auto-sleep NPCs on arrival home
	sim.RegisterClosedBusinessSubscriber(rt.World)        // ZBBS-HOME-353: remember a business found shut on arrival
	sim.RegisterOutOfStockSubscriber(rt.World)            // ZBBS-HOME-363: remember a vendor-item found out of stock on a failed buy
	sim.RegisterDeclinedWorkSubscriber(rt.World)          // LLM-198: remember an employer that declined a labor offer; drop it from the worker's seek-work directory for 12h
	sim.RegisterNoHiringSubscriber(rt.World)              // LLM-210: remember a business whose keeper was on break (present but not hireable); drop it from the seek-work directory
	sim.RegisterHelpedByWorkerSubscriber(rt.World)        // LLM-228: employer remembers a worker who completed a paid job; recall it at the decision section when they solicit again (36h)
	sim.RegisterKnownPlaceSubscriber(rt.World)            // LLM-77: remember a place's affordance on gather/purchase (durable world-memory)
	sim.RegisterGatherTargetSubscriber(rt.World)          // LLM-93: remember the bush an NPC walked to, so gather prefers it over the nearest
	sim.RegisterLaborArrivalSubscriber(rt.World)          // LLM-229: start a hired worker's job when they (and the owner) reach the employer's workplace
	sim.RegisterLodgingMorningDescentSubscriber(rt.World) // ZBBS-HOME-312 #2: walk a naturally-woken lodger PC down to the common room
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

	// Durable action-log writer (ZBBS-WORK-376): drains the agent_action_log
	// queue to pg off the world goroutine. Nil sink in the headless lifecycle
	// test, so guard. Drained in the shutdown sequence below, after worldDone.
	actionLogWriterDone := make(chan struct{})
	if rt.ActionLog != nil {
		go func() {
			rt.ActionLog.Run(actionLogWriterCtx)
			close(actionLogWriterDone)
		}()
	} else {
		close(actionLogWriterDone)
	}

	// Resume walks the previous process had in flight at shutdown
	// (ZBBS-HOME-449): each checkpointed move_destination re-dispatches
	// through the normal MoveActor, so a deploy restart no longer strands
	// mid-walk actors wherever the final checkpoint caught them. Runs
	// before the tickers start so the resumed walks win any race with
	// producers that would re-target an apparently-idle actor.
	if _, err := rt.World.SendContext(worldCtx, sim.ResumeCheckpointedWalks(time.Now().UTC())); err != nil {
		log.Printf("engine: resume checkpointed walks: %v", err)
	}

	// Launch the worker pool (workers complete ticks via SendContext to world)
	// and every periodic ticker/sweep, all bound to worldCtx.
	tickPool.Start(worldCtx)
	startTickers(worldCtx, rt.World)

	// Schedule-window trigger for the washerwoman / town-crier routes
	// (ZBBS-HOME-446): once a minute, fire a route at the carrier's
	// window start and end boundaries (laundry out at start / in at end;
	// crier tours the boards at both). Lives in cascade but is wired here
	// with the other tickers so RegisterNPCRoutes stays pure subscriber
	// wiring.
	go cascade.RunRouteScheduleTicker(worldCtx, rt.World)

	// Daily sim-conversation push (ZBBS-WORK-376 piece 3). Bound to worldCtx so
	// it stops with the world; it reads pg + POSTs to the API, independent of
	// the world goroutine. Nil in the headless lifecycle test (no pg). Waited at
	// shutdown (below) before main closes the pool, so an in-flight query/POST
	// isn't cut mid-flight.
	simPushDone := make(chan struct{})
	if rt.SimPush != nil {
		go func() {
			rt.SimPush.Run(worldCtx)
			close(simPushDone)
		}()
	} else {
		close(simPushDone)
	}

	// Periodic checkpointer. checkpointerDone closes when the loop has fully
	// stopped — the shutdown path waits on it before forcing the final
	// checkpoint, so the two never overlap. checkpointHealth records each
	// attempt's outcome so the umbilical can surface checkpoint health
	// remotely (ZBBS-HOME-334) — wired to the HTTP server below.
	checkpointHealth := &sim.CheckpointHealth{}
	checkpointerDone := make(chan struct{})
	go func() {
		sim.RunCheckpointer(checkpointerCtx, rt.World, rt.Save, checkpointHealth)
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
		// Backs the admin PATCH /api/assets/{id}/door|footprint|stand editor routes
		// (LLM-263). Wired whenever pg is present — a player-admin editor route,
		// independent of the umbilical. Guarded like the ActionLog store below: a
		// typed-nil *pg.AssetsRepo boxed into the interface would read as non-nil and
		// defeat the handler's nil-writer 503 guard, so only wire a real one.
		if rt.AssetGeometryWriter != nil {
			server.SetAssetGeometryWriter(rt.AssetGeometryWriter)
		}
		// Enables the operator-gated umbilical routes. Nil when UMBILICAL_ENABLED
		// is unset → SetTelemetry not called → routes never registered.
		if rt.Umbilical != nil {
			server.SetTelemetry(rt.Umbilical)
			server.SetPrompts(rt.PromptRing)                // backs /umbilical/agent/prompts (ZBBS-HOME-360)
			server.SetChat(rt.ChatRing)                     // backs /umbilical/chat (ZBBS-HOME-382)
			server.SetMemoryAPIBaseURL(rt.MemoryAPIBaseURL) // backs /umbilical/turns (ZBBS-HOME-396)
			// Backs /umbilical/transcript (LLM-35). Guarded: a typed-nil
			// *pg.ActionLogRepo stored into the interface would read as non-nil and
			// defeat the handler's nil-store 503 guard, so only wire a real one.
			if rt.ActionLog != nil {
				server.SetTranscriptStore(rt.ActionLog)
				server.SetSettlementStore(rt.ActionLog) // LLM-105: durable settlements audit read
			}
			server.SetCheckpointHealth(checkpointHealth)
			if rt.UmbilicalControl {
				server.SetControlEnabled(true)
				server.SetRouteForcer(cascade.ForceRouteCommand) // backs /umbilical/route (force a crier/washerwoman tour on demand)
				if rt.RecipeWriter != nil {
					server.SetRecipeWriter(rt.RecipeWriter) // backs /umbilical/recipe/set (LLM-97) — durable item_recipe upsert
				}
				if rt.SatisfiesWriter != nil {
					server.SetSatisfiesWriter(rt.SatisfiesWriter) // backs /umbilical/item/set-satisfies (LLM-119) — durable item_satisfies upsert
				}
				if rt.ItemKindWriter != nil {
					server.SetItemKindWriter(rt.ItemKindWriter) // backs /umbilical/item/set (LLM-200) — durable item_kind upsert
				}
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
		//
		// Unlike the periodic loop (which skips recording a checkpoint the
		// SHUTDOWN cancelled — that's a race, not a failure), finalCtx is a
		// fresh Background-derived timeout, never the shutdown context. An
		// error here means the final write genuinely failed or exceeded
		// finalCheckpointTimeout — a real durability failure worth surfacing to
		// the operator, so it is recorded unconditionally.
		checkpointHealth.RecordFailure(time.Now(), err)
		log.Printf("engine: final checkpoint failed: %v", err)
	} else {
		checkpointHealth.RecordSuccess(time.Now())
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

	// World is stopped — no more action-log Appends will arrive. Drain the
	// durable writer (ZBBS-WORK-376) before the caller closes the pool, so the
	// day's tail of audit rows lands rather than being lost on exit.
	cancelActionLogWriter()
	<-actionLogWriterDone

	// The daily push is bound to worldCtx (cancelled above). Wait for it to
	// return before the caller closes the pool, so an in-flight cursor write or
	// day-events query isn't racing pool teardown. A partial catch-up is safe —
	// the cursor only advances per fully-pushed day and the API upsert is
	// idempotent, so the next run resumes cleanly (ZBBS-WORK-376 piece 3).
	<-simPushDone

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
		// `pay` (bare-coin transfer) — wages, tips, gifts (LLM-99). Pulled in
		// ZBBS-HOME-430 because it was then the only coin tool, so NPCs reached
		// for it to settle purchases and double-charged on buy-then-pay.
		// pay_with_item is now the registered purchase path, so bare pay is back
		// for the non-purchase coin movement it was always meant for — the
		// John→Ezekiel wages-for-chores case that otherwise lands as empty
		// speech. PCs pay via /pc/pay.
		{"pay", handlers.RegisterPay},
		{"consume", handlers.RegisterConsume},
		{"sell", handlers.RegisterSceneQuote},
		{"deliver_order", handlers.RegisterDeliverOrder},
		{"pay_with_item_family", handlers.RegisterPayWithItemFamily},
		{"labor_family", handlers.RegisterLaborFamily}, // LLM-26: solicit_work / accept_work / decline_work
		{"offer_trade", handlers.RegisterOfferTrade},   // ZBBS-HOME-407
		{"give_family", handlers.RegisterGiveFamily},   // LLM-138: give / accept_gift / decline_gift
		{"take_break", handlers.RegisterTakeBreak},     // ZBBS-HOME-284 #4
		{"stay_open", handlers.RegisterStayOpen},       // ZBBS-WORK-387
		{"move_to", handlers.RegisterMoveTo},           // ZBBS-HOME-285
		{"summon", handlers.RegisterSummon},            // ZBBS-HOME-311
		{"gather", handlers.RegisterGather},            // ZBBS-WORK-328
		{"produce", handlers.RegisterCraft},            // LLM-116: producer picks what to produce next
		{"repair", handlers.RegisterRepair},            // LLM-118: owner mends their worn market stall
		{"stop", handlers.RegisterStop},                // ZBBS-HOME-338
		// `done` — the universal terminal tool. The NPC's instructions tell it
		// to end its turn with done, and the v2 harness ends the tick on a
		// ClassTerminal dispatch (sim.TickStatusDone, see sim/reactor_commands.go).
		// Every handler test and the register_*.go composition docs wire this,
		// but production registration omitted it — so a `done` call errored with
		// unknown_tool and the NPC was forced into another tool (typically a
		// walk-off), manufacturing goal-thrash. ZBBS-HOME-369.
		{"done", func(r *handlers.Registry) error { return r.RegisterTerminal("done") }},
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
	go sim.RunShiftTicker(ctx, w) // ZBBS-WORK-278: shift/duty producer

	go sim.RunDwellTicker(ctx, w)
	go sim.RunProduceTicker(ctx, w)
	go sim.RunRestockTicker(ctx, w)          // ZBBS-WORK-322: buy-side restock producer
	go sim.RunProductionChoiceTicker(ctx, w) // LLM-116: wake an idle multi-output crafter to choose what to forge
	go sim.RunObjectRefreshRegen(ctx, w)
	go sim.RunSourceActivityTicker(ctx, w) // LLM-54: completes timed eat/drink/harvest
	go sim.RunOrderSweep(ctx, w)
	go sim.RunPayLedgerSweep(ctx, w)
	go sim.RunLaborLedgerSweep(ctx, w)   // LLM-26: expire pending + settle completed labor offers
	go sim.RunHuddleSilenceSweep(ctx, w) // ZBBS-HOME-417: conclude dormant huddles
	go sim.RunHuddleLoopSweep(ctx, w)    // LLM-159: conclude conversational-livelock huddles (OFF unless huddle_loop_timeout_seconds > 0)
	go sim.RunRoomSweep(ctx, w)
	go sim.RunPCPresenceSweep(ctx, w) // ZBBS-WORK-326: reclaim ghost (closed-tab) PCs
	go sim.RunSceneQuoteSweep(ctx, w)
	// ZBBS-HOME-443/446: carve laundry + notice boards out of the bulk
	// rotation — those domains are mutated exclusively by the washerwoman /
	// town-crier routes, which since ZBBS-HOME-446 fire on the carriers' own
	// schedule-window boundaries (see RunRouteScheduleTicker above), not on
	// RotationApplied. The exclusion no longer doubles as the route trigger;
	// it only keeps the midnight bulk pass from re-flipping route-owned
	// objects.
	go sim.RunRotationTicker(ctx, w, sim.RotationScope{
		ExcludeTags: []string{sim.TagLaundry, sim.TagNoticeBoard},
	})
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
