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

	// Needs tunables. NeedsTickAmount is the per-hour increment magnitude
	// applied to every eligible actor. NeedThresholds carries the per-need
	// "red" boundary; TirednessCriticalThreshold is the absolute (not pct)
	// threshold at which on-shift recovery gates lift.
	// MovementFatiguePerTileX100 is fatigue per tile of movement, stored ×100.
	NeedsTickAmount            int
	NeedThresholds             NeedThresholds
	TirednessCriticalThreshold int
	MovementFatiguePerTileX100 int
}

// SpeechHelper is the generic-dialogue pool. Pull(type, fromActor, toActor)
// returns a line for a typed scenario; both actors nullable. v1 ignores
// actors and selects randomly; future context-aware selection becomes a
// helper-internal change (callsites already wire both actors through).
//
// TODO: port from scattered hardcoded line arrays + per-tick LLM generic
// speech during speech subsystem port.
type SpeechHelper struct{}

// ReactorScheduler is the in-memory min-heap pacing reactor ticks
// (ZBBS-HOME-263).
//
// TODO: port from engine/reactor_scheduler.go.
type ReactorScheduler struct{}

// CascadeOrigin is a tagged event that opens a new cascade (PC speech,
// idle backstop, chronicler refresh, etc.).
//
// TODO: port from cascade-origin handling during cascade subsystem port.
type CascadeOrigin struct{}

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

	// Secondary indices — rebuildable from primary state at LoadWorld time
	// and kept consistent by command handlers thereafter.
	actorsByStructure map[StructureID]map[ActorID]struct{}
	actorsByHuddle    map[HuddleID]map[ActorID]struct{}

	Environment WorldEnvironment
	Phase       Phase
	Settings    WorldSettings
	TickCounter uint64

	Speech   *SpeechHelper
	Reactor  *ReactorScheduler
	Cascades chan CascadeOrigin

	cmds      chan Command
	published atomic.Pointer[Snapshot]

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
		Reactor:           &ReactorScheduler{},
		Cascades:          make(chan CascadeOrigin, 64),
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

	env, phase, settings, err := repo.Environment.Load(ctx)
	if err != nil {
		return nil, err
	}
	w.Environment = env
	w.Phase = phase
	w.Settings = settings

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

	villageObjects, err := repo.VillageObjects.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.VillageObjects = villageObjects

	w.rebuildIndices()
	w.republish()
	return w, nil
}

// Run owns the world goroutine. Processes commands until ctx is cancelled
// or the cmds channel is closed. Returns when the loop exits.
//
// Caller is responsible for starting this in a goroutine. After ctx
// cancel, in-flight commands complete; queued commands are dropped.
func (w *World) Run(ctx context.Context) {
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

// Send enqueues a command and waits for the reply. Returns the command's
// Value and Err.
//
// Caller MUST NOT call Send from inside a command Fn — that would deadlock
// the single world goroutine. Use direct mutation (you already hold the
// world goroutine) instead.
func (w *World) Send(cmd Command) (any, error) {
	reply := make(chan CommandResult, 1)
	cmd.Reply = reply
	w.cmds <- cmd
	r := <-reply
	return r.Value, r.Err
}

// Submit enqueues a fire-and-forget command. Returns immediately. Caller
// does not get to observe the outcome — use Send if you need the result.
func (w *World) Submit(fn func(*World) (any, error)) {
	w.cmds <- Command{Fn: fn}
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
		snap.Huddles[id] = h
	}
	for id, s := range w.Scenes {
		snap.Scenes[id] = s
	}
	for id, s := range w.Structures {
		snap.Structures[id] = s
	}
	for id, o := range w.Orders {
		snap.Orders[id] = o
	}
	for id, v := range w.VillageObjects {
		snap.VillageObjects[id] = v
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
