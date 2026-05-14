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
	Now              time.Time
	Weather          string
	Atmosphere       string
	LastRefreshed    time.Time
	LastTransitionAt time.Time // last day↔night transition (UTC)
	LastRotationAt   time.Time // last daily asset rotation (UTC)
	LastNeedsTickAt  time.Time // last hourly needs increment (UTC, hour-truncated)
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
	ReactorJitterMin                 time.Duration
	ReactorJitterMax                 time.Duration
	ReactorEvaluatorCadence          time.Duration
	MaxWarrantAge                    time.Duration
	MaxReactorTicksPerActorPerMinute int
	MaxWarrantsPerActor              int

	// DefaultOutdoorSceneRadius is the conversational radius used by
	// SceneBoundArea when callers don't specify one explicitly. Measured
	// in king's-move (Chebyshev) tiles around the bound's Anchor.
	// normalizeOutdoorSceneRadius applies the default and the bounds
	// clamp at LoadWorld:
	//   - 0 / unset / negative → DefaultOutdoorSceneRadiusValue (3 tiles)
	//   - above DefaultOutdoorSceneRadiusMax (10) → clamped to max
	DefaultOutdoorSceneRadius int
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

	// Asset catalog — reference state, loaded at startup. Looked up by
	// VillageObject.AssetID for state resolution, footprint, anchor, etc.
	Assets map[AssetID]*Asset

	// Recipe catalog — reference state. Keyed by OutputItem. Used by
	// produce_tick (rate + inputs + output_qty) and pay-deliberation
	// (wholesale/retail prices).
	Recipes map[ItemKind]*ItemRecipe

	// Terrain — reference state, loaded once at startup. MapW * MapH
	// bytes of per-tile terrain type. Hot-reload on SIGHUP if needed.
	Terrain *Terrain

	// Secondary indices — rebuildable from primary state at LoadWorld time
	// and kept consistent by command handlers thereafter.
	actorsByStructure map[StructureID]map[ActorID]struct{}
	actorsByHuddle    map[HuddleID]map[ActorID]struct{}

	Environment WorldEnvironment
	Phase       Phase
	Settings    WorldSettings
	TickCounter uint64

	Speech      *SpeechHelper
	reactorEval reactorEvaluatorState

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

	repo Repository
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
		Assets:            make(map[AssetID]*Asset),
		Recipes:           make(map[ItemKind]*ItemRecipe),
		actorsByStructure: make(map[StructureID]map[ActorID]struct{}),
		actorsByHuddle:    make(map[HuddleID]map[ActorID]struct{}),
		Speech:            &SpeechHelper{},
		cmds:              make(chan Command, 256),
		repo:              repo,
	}
	w.republish()
	return w
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
			value, err := cmd.Fn(w)
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

// emit dispatches event evt to every registered subscriber. Called from
// command Fn implementations after the underlying state mutation lands.
// Inline dispatch keeps subscriber side effects atomic with the mutation
// — readers of the next Snapshot see the post-mutation, post-subscriber
// state.
func (w *World) emit(evt Event) {
	for _, s := range w.subscribers {
		s.Handle(w, evt)
	}
}

// Published returns the most recently published Snapshot. Safe to call
// from any goroutine — atomic load, no coordination.
func (w *World) Published() *Snapshot {
	return w.published.Load()
}

// rebuildIndices populates the actorsByStructure / actorsByHuddle
// secondary indices from primary state. Called by LoadWorld and as a
// defensive recovery path if drift is ever detected.
func (w *World) rebuildIndices() {
	w.actorsByStructure = make(map[StructureID]map[ActorID]struct{})
	w.actorsByHuddle = make(map[HuddleID]map[ActorID]struct{})
	for id, a := range w.Actors {
		if a.InsideStructureID != "" {
			if w.actorsByStructure[a.InsideStructureID] == nil {
				w.actorsByStructure[a.InsideStructureID] = make(map[ActorID]struct{})
			}
			w.actorsByStructure[a.InsideStructureID][id] = struct{}{}
		}
		if a.CurrentHuddleID != "" {
			if w.actorsByHuddle[a.CurrentHuddleID] == nil {
				w.actorsByHuddle[a.CurrentHuddleID] = make(map[ActorID]struct{})
			}
			w.actorsByHuddle[a.CurrentHuddleID][id] = struct{}{}
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
		State:             a.State,
		InsideStructureID: a.InsideStructureID,
		CurrentX:          a.CurrentX,
		CurrentY:          a.CurrentY,
		CurrentHuddleID:   a.CurrentHuddleID,
		Needs:             needsCopy,
		InventoryHash:     hash,
		Coins:             a.Coins,
	}
}
