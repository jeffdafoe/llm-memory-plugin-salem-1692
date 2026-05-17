package sim

import (
	"context"
	"sync/atomic"
	"time"
)

// Phase is the current daypart in the world. Salem operates on a simple
// two-phase day/night cycle driven by configurable dawn/dusk boundaries.
type Phase string

const (
	PhaseDay   Phase = "day"
	PhaseNight Phase = "night"
)

// WorldEnvironment carries world-level transient state: time-of-day,
// weather, atmosphere prose (the chronicler-replacement single-string mood
// line refreshed every ~4h), and timestamps the various tickers use to
// avoid re-firing a boundary they have already processed.
type WorldEnvironment struct {
	Now                     time.Time
	Weather                 string
	Atmosphere              string
	LastRefreshed           time.Time
	LastAtmosphereRefreshAt time.Time // last successful atmosphere refresh (UTC); see engine/sim/atmosphere.go
	LastTransitionAt        time.Time // last day↔night transition (UTC)
	LastRotationAt          time.Time // last daily asset rotation (UTC)
	LastNeedsTickAt         time.Time // last hourly needs increment (UTC, hour-truncated)
}

// WorldSettings carries world-level config — checkpoint cadence, phase
// boundary times, admin-tunable thresholds. Fields expand per subsystem
// port; nothing here is hot-path on the tick.
type WorldSettings struct {
	CheckpointInterval time.Duration

	// Phase boundary times in HH:MM, interpreted in Timezone.
	DawnTime     string
	DuskTime     string
	RotationTime string
	Timezone     string
	Location     *time.Location

	// Client-side zoom floors — different for admins vs regular users.
	// Pure UI config; the sim package carries the values so admin endpoints
	// have one place to read/write.
	ZoomMinAdmin   float64
	ZoomMinRegular float64

	// AgentTicksPaused, when true, suppresses LLM agent activity globally —
	// reactive NPC ticks and chronicler fires both gated. Worker schedulers,
	// social hours, lamplighter, and rotation continue running. Used to halt
	// agent activity mid-session when a bad loop is being investigated.
	AgentTicksPaused bool

	// Lodging hour-of-day tunables (legacy lodging_check_in_hour /
	// lodging_check_out_hour). Interpreted in WorldSettings.Location.
	LodgingCheckInHour  int
	LodgingCheckOutHour int

	// Needs tunables. NeedsTickAmount is the per-hour increment magnitude
	// applied to every eligible actor. NeedThresholds carries the per-need
	// "red" boundary; TirednessCriticalThreshold is the absolute (not pct)
	// threshold at which on-shift recovery gates lift.
	// MovementFatiguePerTileX100 is fatigue per tile of movement, stored ×100.
	NeedsTickAmount            int
	NeedThresholds             NeedThresholds
	TirednessCriticalThreshold int
	MovementFatiguePerTileX100 int

	// Reactor evaluator tunables (Phase 2 PR 2). Settings-driven gross
	// gates — no per-call cost calculation; llm-memory-api's per-VA dollar
	// budgets (MEM-052) own the hard $ ceiling.
	//
	// ReactorJitterMin/Max: stamped at warrant time as now+jitter. Provides
	// conversational pacing (1-4s default — fires feel like turn-taking,
	// not LLM-speed turbo).
	//
	// ReactorEvaluatorCadence: how often the evaluator runs. 250ms gives
	// ±250ms timing precision around the jitter floor, which is fine for
	// conversational scale.
	//
	// MaxWarrantAge: cleared on LoadWorld; not currently used at runtime
	// (warrants are ephemeral). Kept for future use if persistence lands.
	//
	// MaxReactorTicksPerActorPerMinute: per-actor rate floor. Drops to 0
	// (disabled) by default; turn on if a noisy environment produces sub-
	// jitter ping-pong loops in practice. Capped actors get their
	// WarrantDueAt pushed to the next allowed time rather than silently
	// skipped each scan.
	//
	// MaxWarrantsPerActor: cap on the per-actor Warrants list size. When
	// exceeded, oldest entries drop (freshest signals are most relevant).
	// 0 = uncapped.
	//
	// MinReactorTickGap: per-actor minimum wall-clock gap between reactor
	// ticks — an always-on pacing floor independent of the optional per-
	// minute rate cap above. Default 5s (defaultMinReactorTickGap). A
	// warrant coming due inside the gap has its WarrantDueAt pushed to the
	// gap boundary; a Force warrant bypasses it.
	//
	// AdmissionBackoff: how far the evaluator pushes an actor's
	// WarrantDueAt when tick admission control turns it away (downstream
	// worker pool at capacity). Default 250ms (defaultAdmissionBackoff) ≈
	// the evaluator cadence, so a deferred warrant is re-examined on
	// roughly the next scan. The warrants stay OPEN — a deferral consumes
	// nothing.
	//
	// TickWorkerCount: number of off-world goroutines in PR 3's tick worker
	// pool. Defaults to 1 (handlers.defaultTickWorkerCount) — a pool >1
	// gives nondeterministic cross-actor commit order, so the default must
	// not imply an ordering guarantee the system lacks. The pool derives
	// its bounded job-buffer size from this; backpressure is a feature.
	ReactorJitterMin                 time.Duration
	ReactorJitterMax                 time.Duration
	ReactorEvaluatorCadence          time.Duration
	MaxWarrantAge                    time.Duration
	MaxReactorTicksPerActorPerMinute int
	MaxWarrantsPerActor              int
	MinReactorTickGap                time.Duration
	AdmissionBackoff                 time.Duration
	TickWorkerCount                  int

	// Idle-backstop tunables (engine/sim/cascade/idle_backstop.go). Both
	// fall back to defaults when zero, so tests that bypass the
	// environment loader get sensible behavior without seeding them.
	//
	// IdleBackstopThreshold: how long an actor must go without a reactor
	// tick before the idle-backstop sweep stamps a WarrantKindIdleBackstop
	// warrant. Default 30 min (defaultIdleBackstopThreshold in
	// reactor.go) — engine-injected liveness for actors no other warrant
	// has engaged. Production can tune up; sandbox / dev keeps the
	// default for visible behavior.
	//
	// IdleBackstopSweepInterval: how often the idle-backstop sweep walks
	// the actor list. Default 5 min (defaultIdleBackstopSweepInterval in
	// engine/sim/cascade/idle_backstop.go — owned by cascade since cascade
	// owns the goroutine driver). Detection latency ≤ this interval
	// against the threshold; oversample cost is trivial (per-actor field
	// reads on the world goroutine, no allocations).
	IdleBackstopThreshold     time.Duration
	IdleBackstopSweepInterval time.Duration

	// AtmosphereRefreshInterval is the cadence at which the atmosphere
	// refresh cascade slice fires a salem-generic LLM call to rewrite
	// World.Environment.Atmosphere. Default 4h
	// (defaultAtmosphereRefreshInterval in
	// engine/sim/cascade/atmosphere.go — owned by cascade since cascade
	// owns the goroutine driver). Settings-driven from day one so dev /
	// staging can tune it down for testing without rebuilding.
	AtmosphereRefreshInterval time.Duration

	// DefaultOutdoorSceneRadius is the conversational radius used by
	// SceneBoundArea when callers don't specify one explicitly. Measured
	// in king's-move (Chebyshev) tiles around the bound's Anchor.
	// normalizeOutdoorSceneRadius applies the default and the bounds
	// clamp at LoadWorld:
	//   - 0 / unset / negative → DefaultOutdoorSceneRadiusValue (3 tiles)
	//   - above DefaultOutdoorSceneRadiusMax (10) → clamped to max
	DefaultOutdoorSceneRadius int

	// Scene-quote substrate tunables (Phase 3 PR S3). Both fall back to
	// scene_quote.go's *Default constants when zero, so tests that
	// bypass the environment loader get sensible behavior without
	// seeding them.
	//
	// SceneQuoteTTL: how long a freshly minted quote stays Active before
	// the aging sweep flips it Expired. Default 10 min — asymmetric
	// (longer) with the pay-ledger pending TTL (2-5 min) since a
	// quote is a passive ad rather than a staked offer.
	//
	// SceneQuoteSweepCadence: how often the aging sweep scans
	// World.Quotes for expired entries. Default 60s — gives ±60s expiry
	// latency against the 10-min TTL, invisible at gameplay scale.
	SceneQuoteTTL          time.Duration
	SceneQuoteSweepCadence time.Duration

	// Pay-ledger substrate tunables (Phase 3 PR S4). Both fall back to
	// pay_ledger.go's *Default constants when zero. Shorter TTL than
	// SceneQuoteTTL — a pending pay offer has the buyer staked into a
	// social moment, which decays faster than a passive quote ad does.
	//
	// PayLedgerTTL: how long a freshly minted pending entry stays
	// Pending before the aging sweep flips it Expired. Default 3 min —
	// middle of architecture § 3's 2-5 minute range.
	//
	// PayLedgerSweepCadence: how often the aging sweep scans
	// World.PayLedger for expired pending entries. Default 60s —
	// matches the scene-quote sweep cadence so admin tuning sees one
	// mental model.
	PayLedgerTTL          time.Duration
	PayLedgerSweepCadence time.Duration

	// Order substrate tunables (Phase 3 PR S6). Both fall back to
	// order.go's *Default constants when zero. The order TTL is the
	// post-acceptance fulfillment window — longer than PayLedgerTTL
	// since at this stage the buyer has already committed (coins
	// debited) and we want plenty of room for the seller's reactor
	// to fire and deliver.
	//
	// OrderTTL: how long an Order at OrderStateReady sits before
	// the aging sweep flips it OrderStateExpired. Default 10 min.
	//
	// OrderSweepCadence: how often the aging sweep scans World.Orders
	// for expired entries. Default 60s — matches the PayLedger and
	// SceneQuote sweep cadences.
	OrderTTL          time.Duration
	OrderSweepCadence time.Duration
}

// DefaultOutdoorSceneRadiusValue is the fallback radius used when
// callers don't specify one. 3 tiles is a reasonable "stop-and-chat"
// distance on the village grid.
const DefaultOutdoorSceneRadiusValue = 3

// DefaultOutdoorSceneRadiusMax caps the configured radius. Larger
// values are clamped down at LoadWorld. Conversational radii beyond
// 10 tiles are unlikely to reflect "people standing close enough to
// chat" — the cap is a sanity floor, not a hard physics constraint.
const DefaultOutdoorSceneRadiusMax = 10

// normalizeOutdoorSceneRadius applies the default + clamp to the
// settings at load time. Called from LoadWorld after the environment
// loader returns. Unexported by design.
func normalizeOutdoorSceneRadius(s *WorldSettings) {
	if s == nil {
		return
	}
	switch {
	case s.DefaultOutdoorSceneRadius <= 0:
		s.DefaultOutdoorSceneRadius = DefaultOutdoorSceneRadiusValue
	case s.DefaultOutdoorSceneRadius > DefaultOutdoorSceneRadiusMax:
		s.DefaultOutdoorSceneRadius = DefaultOutdoorSceneRadiusMax
	}
}

// SpeechHelper is the generic-dialogue pool. Pull(type, fromActor, toActor)
// returns a line for a typed scenario; both actors nullable. v1 ignores
// actors and selects randomly; future context-aware selection becomes a
// helper-internal change (callsites already wire both actors through).
//
// TODO: port from scattered hardcoded line arrays + per-tick LLM generic
// speech during speech subsystem port.
type SpeechHelper struct{}

// reactorEvaluatorState carries the coalescing flag that gates the
// AfterFunc self-rearm chain. Owned by the world (mutated only from the
// world goroutine), exposed to the timer callback that drives the next
// evaluation. No mutex needed — the flag is read/written exclusively from
// inside Command.Fn.
type reactorEvaluatorState struct {
	scheduled bool
}

// locomotionTickerState carries the coalescing flag for the locomotion
// ticker's AfterFunc self-rearm chain (Phase 2 PR 4). Same shape and
// rules as reactorEvaluatorState — read/written exclusively from inside
// Command.Fn, so no mutex.
type locomotionTickerState struct {
	scheduled bool
}

// sceneQuoteSweepState carries the coalescing flag for the scene-quote
// aging sweep's AfterFunc self-rearm chain (Phase 3 PR S3). Same shape
// and rules as locomotionTickerState — read/written exclusively from
// inside Command.Fn.
type sceneQuoteSweepState struct {
	scheduled bool
}

// payLedgerSweepState carries the coalescing flag for the pay-ledger
// aging sweep's AfterFunc self-rearm chain (Phase 3 PR S4 step 8).
// Same shape and rules as sceneQuoteSweepState.
type payLedgerSweepState struct {
	scheduled bool
}

// orderSweepState carries the coalescing flag for the Order aging
// sweep's AfterFunc self-rearm chain (Phase 3 PR S6). Same shape
// and rules as payLedgerSweepState.
type orderSweepState struct {
	scheduled bool
}

// World is the in-memory state of one realm's simulation. A single
// goroutine (started by World.Run) owns all mutable fields below — every
// mutation must go through the cmds channel. Readers consume the published
// Snapshot via atomic.Pointer (World.Published).
//
// Per design: zero locks, zero races by construction.
type World struct {
	// Primary state — source of truth.
	Actors         map[ActorID]*Actor
	Structures     map[StructureID]*Structure
	Huddles        map[HuddleID]*Huddle
	Scenes         map[SceneID]*Scene
	Orders         map[OrderID]*Order
	VillageObjects map[VillageObjectID]*VillageObject

	// Quotes is the world-level flat map of all SceneQuotes (active and
	// terminal). Keyed by QuoteID — the LLM-visible uint64 the buyer
	// references in pay_with_item(quote_id=N, ...) at fast-path time.
	// Mirrored by a per-scene reverse index at Scene.QuoteIDs (rebuilt
	// at LoadWorld from this map; the canonical entries live here).
	//
	// Phase 3 PR S3 substrate. No checkpoint persistence layer yet —
	// QuotesRepo lands at the pg-impl cutover. For now NewWorld /
	// LoadWorld both start with an empty Quotes map, which is also
	// the correct restart behavior: a pending quote crossing a
	// restart should re-emit fresh via the next scene_quote tool
	// call, not be resurrected with stale ExpiresAt.
	Quotes map[QuoteID]*SceneQuote

	// PayLedger is the world-level flat map of all PayLedgerEntries
	// (pending and terminal). Keyed by LedgerID — the LLM-visible
	// uint64 the seller references in accept_pay / decline_pay /
	// counter_pay, and the buyer references in withdraw_pay /
	// pay_with_item(in_response_to=N).
	//
	// Phase 3 PR S4 substrate. Source of truth for the offer-side
	// state machine; Postgres pay_ledger table is the best-effort
	// downstream projection via PayLedgerSink (drift-recovered by
	// admin reconciliation, not by command logic — see
	// ledger-substrate-design § 10). No PayLedgerRepo yet — the
	// pg-impl checkpoint layer lands at cutover. For now NewWorld /
	// LoadWorld both start with an empty PayLedger map; restart
	// re-engagement happens via the warrant re-stamp pass during
	// LoadWorld.
	PayLedger map[LedgerID]*PayLedgerEntry

	// Asset catalog — reference state, loaded at startup. Looked up by
	// VillageObject.AssetID for state resolution, footprint, anchor, etc.
	Assets map[AssetID]*Asset

	// Recipe catalog — reference state. Keyed by OutputItem. Used by
	// produce_tick (rate + inputs + output_qty) and pay-deliberation
	// (wholesale/retail prices).
	Recipes map[ItemKind]*ItemRecipe

	// ItemKind catalog — reference state. Keyed by Name (== ItemKind). The
	// definitional source for an item's display label, category, default
	// price, sort order, and per-need satisfies entries (port of v1's
	// item_kind + item_satisfies tables). Loaded at startup; hot-reloaded
	// on SIGHUP when admin edits land. See item_kind.go.
	ItemKinds map[ItemKind]*ItemKindDef

	// Terrain — reference state, loaded once at startup. MapW * MapH
	// bytes of per-tile terrain type. Hot-reload on SIGHUP if needed.
	Terrain *Terrain

	// Secondary indices — rebuildable from primary state at LoadWorld time
	// and kept consistent by command handlers thereafter.
	actorsByStructure map[StructureID]map[ActorID]struct{}
	actorsByHuddle    map[HuddleID]map[ActorID]struct{}
	// outdoorActors tracks every actor with InsideStructureID == "". Hot-
	// path optimization for the encounter subscribers (handleArrival-
	// Encounter, handleMovedEncounter): at 200+ actors, scanning w.Actors
	// linearly on every ActorMoved is the wrong shape. Most actors are
	// indoor at any moment (sleeping, working, dining), so the outdoor set
	// is a small fraction of the population and the scan stays bounded by
	// outdoor density rather than total population.
	//
	// Maintained in lockstep with InsideStructureID by setActorInside-
	// Structure (the single mutation chokepoint) and rebuilt from primary
	// state by rebuildIndices. Iterated read-only via ForEachOutdoorActor.
	outdoorActors map[ActorID]struct{}

	Environment WorldEnvironment
	Phase       Phase
	Settings    WorldSettings
	TickCounter uint64

	// LoadedAt is the wall-clock moment LoadWorld populated this world
	// from the repository. Set once by LoadWorld; never modified
	// afterward. Read by the idle-backstop cascade slice as the cold-
	// start anchor for actors with no RecentReactorTicks history (a
	// fresh-loaded actor is "active at LoadedAt," not "idle forever").
	// Other consumers don't need this — lastReactorTickAt is the
	// authoritative source for per-actor tick history, and its
	// nil-RecentReactorTicks "never ticked" semantics is what the
	// MinReactorTickGap pacing floor and rate gate both rely on.
	LoadedAt time.Time

	Speech          *SpeechHelper
	reactorEval     reactorEvaluatorState
	locomotionTick  locomotionTickerState
	sceneQuoteSweep sceneQuoteSweepState
	payLedgerSweep  payLedgerSweepState
	orderSweep      orderSweepState

	// quoteSeq is the monotonic per-run QuoteID counter — same shape
	// and rules as eventSeq. Incremented before assignment; first
	// minted QuoteID is 1 (QuoteID(0) reserved as the unset sentinel).
	// World-goroutine-only (touched exclusively from inside Command.Fn).
	quoteSeq uint64

	// payLedgerSeq is the monotonic per-run LedgerID counter — same
	// shape and rules as quoteSeq. Incremented before assignment;
	// first minted LedgerID is 1 (LedgerID(0) reserved as the unset
	// sentinel / "no parent" / "no quote referenced").
	// World-goroutine-only (touched exclusively from inside Command.Fn).
	payLedgerSeq uint64

	// orderSeq is the monotonic per-run OrderID counter — same shape
	// and rules as payLedgerSeq. Incremented before assignment; first
	// minted OrderID is 1 (OrderID(0) reserved as the unset sentinel).
	// World-goroutine-only (touched exclusively from inside Command.Fn).
	orderSeq uint64

	// payLedgerSink is the best-effort projection target for PayLedger
	// state transitions. Never nil: NewWorld installs nullPayLedgerSink,
	// SetPayLedgerSink(nil) restores it. The world goroutine invokes
	// payLedgerSink.Project(entry) after every state transition; the
	// impl is required not to block the goroutine (typical impl pushes
	// onto a buffered channel and drains in a side goroutine). Sink
	// failures never propagate to commands.
	payLedgerSink PayLedgerSink

	cmds      chan Command
	published atomic.Pointer[Snapshot]

	// runCtx is the lifecycle context the world goroutine is running
	// under. Set by Run on entry and INTENTIONALLY RETAINED after Run
	// exits, so callbacks firing post-shutdown observe the cancelled
	// ctx (rather than a fresh background ctx) and abort cleanly via
	// ctx.Err() instead of parking on a dead cmds channel.
	//
	// Used by long-lived goroutines launched outside the ticker loop
	// (notably time.AfterFunc-driven scheduled flips) via
	// World.LifecycleContext.
	//
	// Atomic so non-world-goroutine readers (the flip timer callbacks)
	// can pick it up without going through the command channel.
	runCtx atomic.Pointer[context.Context]

	// WorldEventGen is bumped after any world-level state change that could
	// invalidate scheduled follow-ups (phase transitions, occupancy refresh,
	// asset rotation). Long-running scheduled work (e.g. spread-out object
	// flips fired via time.AfterFunc) captures the generation at schedule
	// time and skips itself when the world has moved on.
	//
	// Atomic so the goroutine-launched scheduler can read it without
	// going through the command channel. Writers (inside the world
	// goroutine) use Add to make the bump observable.
	WorldEventGen atomic.Uint64

	// subscribers receive in-world Events emitted from command handlers.
	// Registered via Subscribe before Run starts; each event is dispatched
	// to every subscriber in registration order, synchronously inside the
	// world goroutine. See events.go for the contract.
	subscribers []EventSubscriber

	// eventSeq is the monotonic per-run event counter. emit increments it
	// and assigns the value as the new event's EventID. World-goroutine-
	// only — emit runs exclusively inside Command.Fn, so no atomic is
	// needed. Starts at 0; the first emitted event gets ID 1, leaving
	// EventID(0) as the unset sentinel.
	eventSeq uint64

	// currentRootEventID is the ambient causal root for events emitted
	// within the current cascade. 0 means no cascade is active — the next
	// emit becomes a fresh root. Set and restored by withRoot (defer-
	// scoped, panic-safe). World-goroutine-only.
	currentRootEventID EventID

	// tickAdmission gates the reactor evaluator — consulted before an
	// actor's warrants are consumed (Option A — admit before consume).
	// Never nil: NewWorld sets alwaysAdmit, and PR 3's worker pool installs
	// a real one via SetTickAdmissionController.
	tickAdmission TickAdmissionController

	repo Repository
}

// nextEventSeq increments the per-run event counter and returns the new
// EventID. World-goroutine-only (called from emit). The counter starts at
// 0, so the first event is EventID(1) — EventID(0) is never assigned.
func (w *World) nextEventSeq() EventID {
	w.eventSeq++
	return EventID(w.eventSeq)
}

// withRoot runs fn with currentRootEventID set to root, restoring the
// previous value on return — including on panic, via defer. World-
// goroutine-only; no atomic. Used by emit (to establish a fresh cascade
// root) and by Run (to continue an inherited root across the worker seam).
func (w *World) withRoot(root EventID, fn func()) {
	prev := w.currentRootEventID
	w.currentRootEventID = root
	defer func() { w.currentRootEventID = prev }()
	fn()
}

// NewWorld constructs an empty World bound to the given Repository.
//
// Call LoadWorld for production startup (populates primary state from
// persistence); tests typically use NewWorld + direct map seeding so they
// can control the initial state precisely.
//
// The cmds channel is buffered to absorb bursts without blocking
// producers; the world goroutine drains it.
func NewWorld(repo Repository) *World {
	w := &World{
		Actors:            make(map[ActorID]*Actor),
		Structures:        make(map[StructureID]*Structure),
		Huddles:           make(map[HuddleID]*Huddle),
		Scenes:            make(map[SceneID]*Scene),
		Orders:            make(map[OrderID]*Order),
		VillageObjects:    make(map[VillageObjectID]*VillageObject),
		Quotes:            make(map[QuoteID]*SceneQuote),
		PayLedger:         make(map[LedgerID]*PayLedgerEntry),
		Assets:            make(map[AssetID]*Asset),
		Recipes:           make(map[ItemKind]*ItemRecipe),
		ItemKinds:         make(map[ItemKind]*ItemKindDef),
		actorsByStructure: make(map[StructureID]map[ActorID]struct{}),
		actorsByHuddle:    make(map[HuddleID]map[ActorID]struct{}),
		outdoorActors:     make(map[ActorID]struct{}),
		Speech:            &SpeechHelper{},
		cmds:              make(chan Command, 256),
		tickAdmission:     alwaysAdmit{},
		payLedgerSink:     nullPayLedgerSink{},
		repo:              repo,
	}
	w.republish()
	return w
}

// SetPayLedgerSink installs the projection target the world invokes on
// every PayLedger state transition (pending creation + every terminal
// flip). Nil restores nullPayLedgerSink so the field is never nil at
// call sites. Mirrors SetTickAdmissionController's contract — safe to
// call before Run, or from inside a Command.Fn. The impl is required
// not to block the world goroutine; see PayLedgerSink doc.
func (w *World) SetPayLedgerSink(s PayLedgerSink) {
	if s == nil {
		s = nullPayLedgerSink{}
	}
	w.payLedgerSink = s
}

// SetTickAdmissionController installs the controller the reactor evaluator
// consults before consuming an actor's warrants. PR 3's worker pool calls
// this at bootstrap (as one half of RegisterTickHandlers). A nil argument
// resets to the alwaysAdmit default.
//
// Safe to call before Run, or from inside a Command.Fn (the world
// goroutine). Calling it from an arbitrary goroutine while Run is
// processing races the evaluator — route it through a Command instead.
func (w *World) SetTickAdmissionController(c TickAdmissionController) {
	if c == nil {
		c = alwaysAdmit{}
	}
	w.tickAdmission = c
}

// LoadWorld constructs a World and populates primary state from the
// repository. Use this for production startup.
//
// Sub-repos implemented at this stage (Actors, Huddles, Environment)
// are loaded; remaining sub-repos land as subsystems get ported.
// Indices are rebuilt from primary state, snapshot is published, ready
// to Run.
func LoadWorld(ctx context.Context, repo Repository) (*World, error) {
	w := NewWorld(repo)

	actors, err := repo.Actors.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Actors = actors

	huddles, err := repo.Huddles.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Huddles = huddles

	scenes, err := repo.Scenes.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Scenes = scenes

	env, phase, settings, err := repo.Environment.Load(ctx)
	if err != nil {
		return nil, err
	}
	w.Environment = env
	w.Phase = phase
	w.Settings = settings
	normalizeOutdoorSceneRadius(&w.Settings)

	assets, err := repo.Assets.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Assets = assets

	recipes, err := repo.Recipes.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Recipes = recipes

	itemKinds, err := repo.ItemKinds.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.ItemKinds = itemKinds

	terrain, err := repo.Terrain.Load(ctx)
	if err != nil {
		return nil, err
	}
	w.Terrain = terrain

	structures, err := repo.Structures.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Structures = structures

	villageObjects, err := repo.VillageObjects.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.VillageObjects = villageObjects

	w.rebuildIndices()
	// Reactor state (warrants + in-flight + attempt-id + recent-tick ring)
	// is ephemeral by design — payloads are interface-typed and weren't
	// designed to cross the checkpoint serialization boundary. Cascade
	// origins re-engage actors via fresh events post-restart; the warrant
	// list from before the crash isn't meaningful anymore (the
	// conversational moment passed).
	for _, a := range w.Actors {
		resetReactorStateOnLoad(a)
	}
	// LoadedAt is the wall-clock moment this world woke up (not
	// w.Environment.Now, which can lag arbitrarily on a long-crash
	// recovery). Read by the idle-backstop sweep so fresh-loaded actors
	// — who have no RecentReactorTicks history yet — are treated as
	// "active at world wake-up" rather than "never ticked, idle by
	// maximum duration." Without that, the first sweep after restart
	// would stamp idle warrants on every actor simultaneously. See
	// engine/sim/idle_backstop_commands.go.
	w.LoadedAt = time.Now().UTC()
	// Scene-quote restart housekeeping. No QuotesRepo exists yet
	// (pg-impl deferred to cutover), so this loop iterates an empty
	// map today. Implementation is correct for the future case where
	// quotes do load from a repo: any quote already past its
	// ExpiresAt at restart is flipped to expired with ResolvedAt
	// stamped, no event emitted (the original SceneQuoteCreated event
	// is gone, so a re-stamped expired event would have nothing to
	// reference causally — restart-noncritical per the design pass).
	// Active non-expired quotes survive with their absolute ExpiresAt
	// intact; the sweep picks them up on its next pass.
	restartExpireScannedQuotes(w, time.Now())
	// QuoteIDs reverse index is rebuilt from the canonical World.Quotes
	// map so any drift loaded from a repo can't persist past startup.
	rebuildSceneQuoteIndex(w)
	// Quote sequence counter safety floor: if the loaded counter is
	// somehow below the max QuoteID actually present, bump it so the
	// next mint doesn't collide. Idempotent — both paths produce the
	// same result when the counter was correct.
	for id := range w.Quotes {
		if uint64(id) > w.quoteSeq {
			w.quoteSeq = uint64(id)
		}
	}
	// Pay-ledger restart housekeeping. No PayLedgerRepo exists yet
	// (pg-impl deferred to cutover), so this loop iterates an empty
	// map today. Implementation is correct for the future case where
	// entries load from a repo: any pending entry already past its
	// ExpiresAt at restart is flipped to expired with ResolvedAt
	// stamped, no event emitted (the original PayOfferReceived event
	// is gone — restart re-engagement happens via the warrant
	// re-stamp pass which lands later in PR S4 alongside
	// PayOfferWarrantReason). Active pending entries with ExpiresAt
	// in the future survive the load with absolute ExpiresAt intact;
	// the aging sweep picks them up on its first pass.
	restartExpirePendingEntries(w, time.Now())
	// Ledger sequence counter safety floor: same posture as quoteSeq.
	for id := range w.PayLedger {
		if uint64(id) > w.payLedgerSeq {
			w.payLedgerSeq = uint64(id)
		}
	}
	// Pay-offer warrant restart re-stamp (Phase 3 PR S4 step 7 — the
	// load-bearing use case for the DedupDiscriminator interface
	// migration). Walks pending ledger entries and stamps
	// PayOfferWarrantReason on each seller so post-restart the seller's
	// next reactor tick still perceives the offer. Discriminator =
	// uint64(LedgerID), so a normal-flow PayOfferReceived emit that
	// fires AFTER this stamp dedupes against it cleanly. Done after
	// restartExpirePendingEntries so already-expired pendings are
	// skipped. Subscribers haven't registered yet at LoadWorld time;
	// that's fine — the re-stamp doesn't go through a subscriber, it
	// reaches into the actor's warrant slice directly via
	// tryStampWarrant.
	restartReStampPayOfferWarrants(w, time.Now())

	// Order restart housekeeping (Phase 3 PR S6). No OrdersRepo exists
	// yet (pg-impl deferred to cutover), so this loop iterates an empty
	// map today. Implementation is correct for the future case where
	// orders load from a repo: any Ready Order already past its
	// ExpiresAt at restart is flipped to Expired in-band, mirroring
	// restartExpirePendingEntries' pay-ledger pattern. Active Ready
	// orders survive the load with absolute ExpiresAt intact; the
	// aging sweep picks them up on its first pass.
	restartExpirePendingOrders(w, time.Now())
	// Order sequence counter safety floor: same posture as quoteSeq /
	// payLedgerSeq.
	for id := range w.Orders {
		if uint64(id) > w.orderSeq {
			w.orderSeq = uint64(id)
		}
	}

	w.republish()
	return w, nil
}

// Run owns the world goroutine. Processes commands until ctx is cancelled
// or the cmds channel is closed. Returns when the loop exits.
//
// Caller is responsible for starting this in a goroutine. After ctx
// cancel, in-flight commands complete; queued commands are dropped.
//
// Stamps w.runCtx so callbacks scheduled inside commands (e.g. phase-
// transition flip timers) can ride the same shutdown signal — see
// World.LifecycleContext. Deliberately does NOT clear runCtx on exit:
// if the timer fires after Run has returned, the stored ctx is already
// cancelled, so the callback's SendContext sees ctx.Err() != nil and
// returns immediately instead of parking forever on the cmds channel.
func (w *World) Run(ctx context.Context) {
	w.runCtx.Store(&ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-w.cmds:
			if !ok {
				return
			}
			var value any
			var err error
			if cmd.inheritedRoot != 0 {
				// Cross-boundary command (PR 3's worker tool-call): run the
				// whole handler under the inherited cascade root so events
				// it emits continue that root and it cannot bleed into the
				// next command. See newRootedCommand.
				w.withRoot(cmd.inheritedRoot, func() {
					value, err = cmd.Fn(w)
				})
			} else {
				value, err = cmd.Fn(w)
			}
			w.TickCounter++
			w.republish()
			if cmd.Reply != nil {
				cmd.Reply <- CommandResult{Value: value, Err: err}
			}
		}
	}
}

// SendContext enqueues a command and waits for the reply, honoring ctx
// cancellation on both the send and receive halves. Returns ctx.Err() if
// the context expires before the world goroutine accepts the command or
// before the reply comes back.
//
// Use this from tickers / long-lived goroutines that need to unblock when
// the world is shutting down — Send (no context) deadlocks if Run has
// already exited.
//
// Caller MUST NOT call SendContext from inside a command Fn — that would
// deadlock the single world goroutine. Use direct mutation instead.
func (w *World) SendContext(ctx context.Context, cmd Command) (any, error) {
	reply := make(chan CommandResult, 1)
	cmd.Reply = reply
	select {
	case w.cmds <- cmd:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-reply:
		return r.Value, r.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Send enqueues a command and waits for the reply. Returns the command's
// Value and Err.
//
// SAFETY CONTRACT: only call Send when the caller knows the world is
// running. There is no context plumbed in — if the world goroutine has
// already exited (or hasn't started), Send blocks on the cmds channel
// forever. Tickers, long-lived background goroutines, and anything
// launched via time.AfterFunc MUST use SendContext with a context that
// gets cancelled on shutdown (see World.LifecycleContext for the
// world's own ctx).
//
// Caller MUST NOT call Send from inside a command Fn — that would
// deadlock the single world goroutine. Use direct mutation (you already
// hold the world goroutine) instead.
func (w *World) Send(cmd Command) (any, error) {
	return w.SendContext(context.Background(), cmd)
}

// LifecycleContext returns the context Run is currently using, or a
// background context if Run has never been called. Goroutines launched
// from inside a command (notably time.AfterFunc-driven scheduled flips)
// call this to get a ctx that unblocks on world shutdown.
//
// After Run exits the cancelled ctx remains in place, so a callback
// firing post-shutdown sees ctx.Err() != nil and aborts cleanly instead
// of deadlocking on a send to a dead cmds channel.
//
// Pulled fresh each time — the schedule-to-fire window can be many
// seconds, and an admin force-phase mid-window could in principle
// re-enter Run with a new ctx in the future (not today; Run is run-once).
func (w *World) LifecycleContext() context.Context {
	if p := w.runCtx.Load(); p != nil {
		return *p
	}
	return context.Background()
}

// Submit enqueues a fire-and-forget command. Returns immediately. Caller
// does not get to observe the outcome — use Send if you need the result.
func (w *World) Submit(fn func(*World) (any, error)) {
	w.cmds <- Command{Fn: fn}
}

// Subscribe registers an EventSubscriber to receive in-world Events emitted
// by command handlers. Subscribers run synchronously inside the world
// goroutine after each event is emitted; they may mutate world state
// freely (atomic with the emitting command) but MUST NOT block on I/O or
// call Send/SendContext (would deadlock the single goroutine).
//
// Safe to call (a) before Run has started, or (b) from inside a Command.Fn
// (which runs on the world goroutine). Calling from an arbitrary goroutine
// while Run is processing commands races against the dispatch loop in
// emit — surface those registrations through a Command instead.
//
// Subscribers fire in registration order; later subscribers see any state
// changes earlier subscribers made.
func (w *World) Subscribe(s EventSubscriber) {
	w.subscribers = append(w.subscribers, s)
}

// emit assigns the event its per-run identity and dispatches it to every
// registered subscriber. Called from command Fn implementations after the
// underlying state mutation lands. Inline dispatch keeps subscriber side
// effects atomic with the mutation — readers of the next Snapshot see the
// post-mutation, post-subscriber state.
//
// Identity: every event gets a fresh monotonic EventID. The RootEventID
// depends on whether a cascade is already active:
//
//   - No ambient root (currentRootEventID == 0): this is a fresh-origin
//     event and is its own causal root. Subscriber dispatch runs under
//     withRoot(id, ...) so events emitted by subscribers (the cascade)
//     inherit this root, and the ambient root restores to 0 on unwind —
//     even if a subscriber panics.
//
//   - Ambient root set: this is a consequent event; it inherits the
//     ambient cascade root. Dispatch needs no extra withRoot — the
//     ambient value is already correct for any nested emits.
func (w *World) emit(evt Event) {
	id := w.nextEventSeq()
	root := w.currentRootEventID
	if root == 0 {
		root = id
		evt.setEventBase(id, root)
		w.withRoot(root, func() {
			for _, s := range w.subscribers {
				s.Handle(w, evt)
			}
		})
		return
	}
	evt.setEventBase(id, root)
	for _, s := range w.subscribers {
		s.Handle(w, evt)
	}
}

// Published returns the most recently published Snapshot. Safe to call
// from any goroutine — atomic load, no coordination.
func (w *World) Published() *Snapshot {
	return w.published.Load()
}

// rebuildIndices populates the actorsByStructure / actorsByHuddle /
// outdoorActors secondary indices from primary state. Called by
// LoadWorld and as a defensive recovery path if drift is ever detected.
func (w *World) rebuildIndices() {
	w.actorsByStructure = make(map[StructureID]map[ActorID]struct{})
	w.actorsByHuddle = make(map[HuddleID]map[ActorID]struct{})
	w.outdoorActors = make(map[ActorID]struct{})
	for id, a := range w.Actors {
		if a.InsideStructureID != "" {
			if w.actorsByStructure[a.InsideStructureID] == nil {
				w.actorsByStructure[a.InsideStructureID] = make(map[ActorID]struct{})
			}
			w.actorsByStructure[a.InsideStructureID][id] = struct{}{}
		} else {
			w.outdoorActors[id] = struct{}{}
		}
		if a.CurrentHuddleID != "" {
			if w.actorsByHuddle[a.CurrentHuddleID] == nil {
				w.actorsByHuddle[a.CurrentHuddleID] = make(map[ActorID]struct{})
			}
			w.actorsByHuddle[a.CurrentHuddleID][id] = struct{}{}
		}
	}
}

// ForEachOutdoorActor invokes fn for every actor currently outdoors
// (InsideStructureID == ""). Iteration stops if fn returns false. Order
// is undefined; callers needing a deterministic order must sort the
// IDs they collect.
//
// Backed by the outdoorActors secondary index — O(K) where K is the
// outdoor population, not O(N) where N is total actor count. Intended
// for hot-path subscribers (encounter detection on ActorMoved /
// ActorArrived) at 200+ actor scale.
//
// MUST be called from inside a Command.Fn or a subscriber dispatched
// from emit (both run on the world goroutine).
//
// SNAPSHOT SEMANTICS. Iteration is over a snapshot of outdoor IDs taken
// at entry, then each ID is re-checked against w.outdoorActors and
// w.Actors before fn is invoked. So fn MAY safely mutate world state —
// including calls that flow through setActorInsideStructure — without
// breaking iteration: an actor moved indoor mid-iteration is skipped
// on its re-check, and newly-outdoor actors after entry are not seen
// by this call (they will be by the next ForEachOutdoorActor on the
// next event). Allocation is O(K) per call; this is intentional to
// avoid exposing range-while-mutating map semantics to callbacks.
func (w *World) ForEachOutdoorActor(fn func(*Actor) bool) {
	ids := make([]ActorID, 0, len(w.outdoorActors))
	for id := range w.outdoorActors {
		ids = append(ids, id)
	}
	for _, id := range ids {
		// Re-check membership: fn from a prior iteration may have moved
		// this actor indoor (e.g. by calling setActorInsideStructure via
		// a command). Skip rather than visit a now-indoor actor.
		if _, ok := w.outdoorActors[id]; !ok {
			continue
		}
		a, ok := w.Actors[id]
		if !ok {
			// Defensive: index drift would only happen if a caller
			// bypassed setActorInsideStructure or removed an actor
			// without unhooking the index. Skip rather than panic.
			continue
		}
		if !fn(a) {
			return
		}
	}
}

// republish builds and atomically swaps a fresh Snapshot. Called from the
// world goroutine after every command.
//
// Per-aggregate snapshot helpers deep-copy each entity so the published
// Snapshot is genuinely immutable from a reader's perspective — readers
// can't reach into world state through a Snapshot pointer to race against
// the world goroutine.
//
// v1 publishes a fresh map per command (cheap allocations). If snapshot
// allocation becomes hot on profiling, the contained replacement is a
// copy-on-write per-entity scheme — same external Snapshot type, lower
// allocation pressure.
func (w *World) republish() {
	snap := &Snapshot{
		AtTick:         w.TickCounter,
		PublishedAt:    time.Now(),
		Actors:         make(map[ActorID]*ActorSnapshot, len(w.Actors)),
		Huddles:        make(map[HuddleID]*Huddle, len(w.Huddles)),
		Scenes:         make(map[SceneID]*Scene, len(w.Scenes)),
		Structures:     make(map[StructureID]*Structure, len(w.Structures)),
		Orders:         make(map[OrderID]*Order, len(w.Orders)),
		VillageObjects: make(map[VillageObjectID]*VillageObject, len(w.VillageObjects)),
		Quotes:         make(map[QuoteID]*SceneQuote, len(w.Quotes)),
		PayLedger:      make(map[LedgerID]*PayLedgerEntry, len(w.PayLedger)),
		Environment:    w.Environment,
		Phase:          w.Phase,
	}
	for id, a := range w.Actors {
		snap.Actors[id] = snapshotActor(a, w.TickCounter)
	}
	for id, h := range w.Huddles {
		snap.Huddles[id] = CloneHuddle(h)
	}
	for id, s := range w.Scenes {
		snap.Scenes[id] = CloneScene(s)
	}
	for id, s := range w.Structures {
		snap.Structures[id] = CloneStructure(s)
	}
	for id, o := range w.Orders {
		snap.Orders[id] = CloneOrder(o)
	}
	for id, v := range w.VillageObjects {
		snap.VillageObjects[id] = CloneVillageObject(v)
	}
	for id, q := range w.Quotes {
		snap.Quotes[id] = CloneSceneQuote(q)
	}
	for id, e := range w.PayLedger {
		snap.PayLedger[id] = ClonePayLedgerEntry(e)
	}
	w.published.Store(snap)
}

// snapshotActor produces an ActorSnapshot — the slim immutable view of an
// actor for consumers.
//
// InventoryHash is a v1 stub (sum of quantities). Future change to a real
// hash (xxhash over sorted kind+qty) is a contained change behind the same
// type.
func snapshotActor(a *Actor, atTick uint64) *ActorSnapshot {
	var hash uint64
	for _, q := range a.Inventory {
		hash += uint64(q)
	}
	needsCopy := make(map[NeedKey]int, len(a.Needs))
	for k, v := range a.Needs {
		needsCopy[k] = v
	}
	return &ActorSnapshot{
		AtTick:            atTick,
		DisplayName:       a.DisplayName,
		Kind:              a.Kind,
		State:             a.State,
		Role:              a.Role,
		InsideStructureID: a.InsideStructureID,
		CurrentX:          a.CurrentX,
		CurrentY:          a.CurrentY,
		CurrentHuddleID:   a.CurrentHuddleID,
		Needs:             needsCopy,
		InventoryHash:     hash,
		Coins:             a.Coins,
		Acquaintances:     cloneAcquaintances(a.Acquaintances),
		Relationships:     cloneRelationships(a.Relationships),
		Narrative:         cloneNarrativeState(a.Narrative),
		DwellCredits:      cloneDwellCredits(a.DwellCredits),
		TickInFlight:      a.TickInFlight,
		TickAttemptID:     a.TickAttemptID,
	}
}
