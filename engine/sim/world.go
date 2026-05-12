package sim

import (
	"context"
	"sync/atomic"
	"time"
)

// Phase is the current daypart in the world (dawn / morning / noon / dusk /
// night / etc.). Placeholder typed string; concrete enum + transition rules
// ported with world_phase subsystem.
type Phase string

// WorldEnvironment carries world-level transient state: time-of-day,
// weather, and atmosphere prose (the chronicler-replacement single-string
// mood line, refreshed every ~4h by the atmosphere goroutine).
type WorldEnvironment struct {
	Now           time.Time
	Weather       string
	Atmosphere    string
	LastRefreshed time.Time
}

// WorldSettings carries world-level config (checkpoint cadence, world phase
// thresholds, etc.). Fields expand per subsystem port.
type WorldSettings struct {
	CheckpointInterval time.Duration
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
	Actors     map[ActorID]*Actor
	Structures map[StructureID]*Structure
	Huddles    map[HuddleID]*Huddle
	Scenes     map[SceneID]*Scene
	Orders     map[OrderID]*Order

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
// Only the sub-repos implemented in v1 (Actors, Huddles) are loaded;
// the rest land as subsystems get ported. Indices are rebuilt from
// primary state, snapshot is published, ready to Run.
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
		AtTick:      w.TickCounter,
		PublishedAt: time.Now(),
		Actors:      make(map[ActorID]*ActorSnapshot, len(w.Actors)),
		Huddles:     make(map[HuddleID]*Huddle, len(w.Huddles)),
		Scenes:      make(map[SceneID]*Scene, len(w.Scenes)),
		Structures:  make(map[StructureID]*Structure, len(w.Structures)),
		Orders:      make(map[OrderID]*Order, len(w.Orders)),
		Environment: w.Environment,
		Phase:       w.Phase,
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
