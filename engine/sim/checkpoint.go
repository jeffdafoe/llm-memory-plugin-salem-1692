package sim

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// checkpoint.go — full-fidelity checkpoint snapshot + the periodic
// checkpoint driver.
//
// The durable checkpoint writer (pg.SaveWorld) runs a multi-second
// Postgres transaction. Running that on the world goroutine (the only
// goroutine allowed to touch live world state) would stall all command
// processing for the duration of the write. Instead the world goroutine
// does a fast in-memory deep-clone of the seven checkpoint aggregates into
// an immutable CheckpointSnapshot, and the slow Tx runs OFF the world
// goroutine against that frozen copy. The deep-clone is the quiescence
// point, not the Tx.
//
// Why not the slim Snapshot (snapshot.go): Snapshot.Actors holds
// *ActorSnapshot, the perception/admin view, which deliberately drops
// fields a durable checkpoint needs (Inventory — only an InventoryHash —
// RoomAccess, Attributes, ProduceState/RestockPolicy, Home/WorkStructureID,
// Schedule*, InsideRoomID, warrants). Checkpointing from it would silently
// lose persisted actor state on restart. CheckpointSnapshot carries full
// *Actor (and full clones of the other aggregates) so the write is lossless.

// CheckpointSnapshot is a full-fidelity, immutable deep-clone of exactly the
// aggregates pg.SaveWorld persists. Every field is a complete deep copy, so
// the durable write can run off the world goroutine without racing world
// mutations. Built by World.BuildCheckpointSnapshot on the world goroutine.
type CheckpointSnapshot struct {
	Actors          map[ActorID]*Actor
	Structures      map[StructureID]*Structure
	Huddles         map[HuddleID]*Huddle
	Scenes          map[SceneID]*Scene
	Orders          map[OrderID]*Order
	VillageObjects  map[VillageObjectID]*VillageObject
	Environment     WorldEnvironment
	Phase           Phase
	MutableSettings MutableWorldSettings
	// DiscoveredKinds — engine-minted item kinds (ZBBS-WORK-412), upserted into
	// item_kind by SaveWorld so an agent's invented good survives restart.
	DiscoveredKinds []DiscoveredKind
}

// MutableWorldSettings is the runtime-tunable subset of WorldSettings the admin
// config write routes mutate (ZBBS-WORK-363) — the ONLY settings the checkpoint
// persists. The full settings table holds ~20 operator-tuned-out-of-band keys
// that are load-once by design; writing the whole map back at every checkpoint
// would clobber any direct DB edit with the startup-loaded value. So the
// checkpoint carries (and SaveWorld upserts) only these three keys. Value type —
// plain-copied into the CheckpointSnapshot, no clone needed.
type MutableWorldSettings struct {
	ZoomMinAdmin     float64
	ZoomMinRegular   float64
	AgentTicksPaused bool
}

// DiscoveredKind is the minimal persist-tuple for an engine-minted item kind
// (ZBBS-WORK-412). SaveWorld upserts these into item_kind so a good an agent
// NPC invented survives restart and shows in the Village Config items table.
// Only name/display_label/category are written — discovered kinds carry no
// satisfies/recipe/price (hand-wired when an operator sources the kind) — and
// the upsert is INSERT ... ON CONFLICT (name) DO NOTHING, so it never clobbers
// an authored row or an operator's later edit.
type DiscoveredKind struct {
	Name         ItemKind
	DisplayLabel string
	Category     ItemCategory
}

// BuildCheckpointSnapshot deep-clones the seven checkpoint aggregates into an
// immutable CheckpointSnapshot.
//
// MUST run on the world goroutine — it reads the live maps directly. Off-
// goroutine callers (the checkpointer) reach it via CheckpointSnapshotCommand
// / CheckpointNow, never by calling this method against a running world from
// another goroutine. (Tests that build a world without starting Run may call
// it directly, since nothing else is mutating the maps.)
//
// WorldEnvironment and Phase are value types — a plain assignment copies them.
func (w *World) BuildCheckpointSnapshot() *CheckpointSnapshot {
	cp := &CheckpointSnapshot{
		Actors:         make(map[ActorID]*Actor, len(w.Actors)),
		Structures:     make(map[StructureID]*Structure, len(w.Structures)),
		Huddles:        make(map[HuddleID]*Huddle, len(w.Huddles)),
		Scenes:         make(map[SceneID]*Scene, len(w.Scenes)),
		Orders:         make(map[OrderID]*Order, len(w.Orders)),
		VillageObjects: make(map[VillageObjectID]*VillageObject, len(w.VillageObjects)),
		Environment:    w.Environment,
		Phase:          w.Phase,
		MutableSettings: MutableWorldSettings{
			ZoomMinAdmin:     w.Settings.ZoomMinAdmin,
			ZoomMinRegular:   w.Settings.ZoomMinRegular,
			AgentTicksPaused: w.Settings.AgentTicksPaused,
		},
	}
	// ZBBS-WORK-412: carry the engine-minted (unknown-category) item kinds so
	// the checkpoint persists them. Authored kinds (food/drink/material/craft)
	// stay reference data, never written by the loop — only discoveries are.
	for kind, def := range w.ItemKinds {
		if def == nil || def.Category != ItemCategoryUnknown {
			continue
		}
		cp.DiscoveredKinds = append(cp.DiscoveredKinds, DiscoveredKind{
			Name:         kind,
			DisplayLabel: def.DisplayLabel,
			Category:     def.Category,
		})
	}
	for id, a := range w.Actors {
		cp.Actors[id] = CloneActor(a)
	}
	for id, s := range w.Structures {
		cp.Structures[id] = CloneStructure(s)
	}
	for id, h := range w.Huddles {
		cp.Huddles[id] = CloneHuddle(h)
	}
	for id, s := range w.Scenes {
		cp.Scenes[id] = CloneScene(s)
	}
	for id, o := range w.Orders {
		cp.Orders[id] = CloneOrder(o)
	}
	for id, v := range w.VillageObjects {
		cp.VillageObjects[id] = CloneVillageObject(v)
	}
	return cp
}

// CheckpointSnapshotCommand returns a Command whose Fn builds a
// CheckpointSnapshot on the world goroutine. The checkpointer SendContexts
// this, then runs the durable write off-goroutine against the result.
func CheckpointSnapshotCommand() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			return w.BuildCheckpointSnapshot(), nil
		},
	}
}

// CheckpointFunc persists a CheckpointSnapshot durably. The production impl
// adapts pg.SaveWorld (the entrypoint wraps it: func(ctx, cp) error {
// return pg.SaveWorld(ctx, repo, cp) }); tests pass a fake. It is a func
// rather than an interface so the durable-write package (pg) stays out of
// sim's import graph — sim owns the cadence + clone, pg owns the SQL, the
// entrypoint composes them.
type CheckpointFunc func(ctx context.Context, cp *CheckpointSnapshot) error

// defaultCheckpointInterval is the fallback cadence when
// WorldSettings.CheckpointInterval is unset (<= 0). Matches the locked-plan
// default; environment config tunes it via the checkpoint_interval_seconds
// setting that EnvironmentRepo loads.
const defaultCheckpointInterval = 60 * time.Second

// CheckpointHealth is the operator-visible health of the durable checkpoint
// loop. The checkpointer records every periodic attempt's outcome here; the
// umbilical reads a Snapshot. In-memory and lossy-on-restart — transient
// diagnostics, no durability need (see shared GUIDELINES) — and safe for
// concurrent use: the checkpointer writes, umbilical request goroutines read.
//
// Why this exists (ZBBS-HOME-334): a failed checkpoint was previously
// log-and-continue with NO operator-visible signal. A duplicate-key abort
// (ZBBS-HOME-333) wedged every world checkpoint for ~an hour, observable only
// by SSHing in and grepping the journal — the umbilical's /errors ring is
// HTTP-response-only and the checkpointer isn't a tracked /ticker-health
// driver, so nothing surfaced it. A non-zero ConsecutiveFailures or a stale
// LastSuccessAt is the at-a-glance "durability is broken" signal that was
// missing. A nil *CheckpointHealth is a no-op on every method, so callers
// (and tests) that don't care can pass nil.
type CheckpointHealth struct {
	mu                  sync.Mutex
	lastAttemptAt       time.Time
	lastSuccessAt       time.Time
	lastFailureAt       time.Time
	consecutiveFailures int
	totalSuccesses      uint64
	totalFailures       uint64
	lastError           string
}

// CheckpointHealthSnapshot is an immutable point-in-time copy of a
// CheckpointHealth, suitable for serialization to the umbilical.
type CheckpointHealthSnapshot struct {
	LastAttemptAt       time.Time `json:"last_attempt_at"`
	LastSuccessAt       time.Time `json:"last_success_at"`
	LastFailureAt       time.Time `json:"last_failure_at"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	TotalSuccesses      uint64    `json:"total_successes"`
	TotalFailures       uint64    `json:"total_failures"`
	LastError           string    `json:"last_error"`
}

// RecordSuccess marks a successful checkpoint at now: clears the consecutive-
// failure streak and the last-error string. Nil-safe.
func (h *CheckpointHealth) RecordSuccess(now time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAttemptAt = now
	h.lastSuccessAt = now
	h.consecutiveFailures = 0
	h.totalSuccesses++
	h.lastError = ""
}

// RecordFailure marks a failed checkpoint at now, advancing the consecutive-
// failure streak and stashing the error string for the operator. Nil-safe.
func (h *CheckpointHealth) RecordFailure(now time.Time, err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAttemptAt = now
	h.lastFailureAt = now
	h.consecutiveFailures++
	h.totalFailures++
	if err != nil {
		h.lastError = err.Error()
	}
}

// Snapshot returns an immutable copy of the current health. Nil-safe (returns
// the zero value), so the umbilical handler works even before any wiring.
func (h *CheckpointHealth) Snapshot() CheckpointHealthSnapshot {
	if h == nil {
		return CheckpointHealthSnapshot{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return CheckpointHealthSnapshot{
		LastAttemptAt:       h.lastAttemptAt,
		LastSuccessAt:       h.lastSuccessAt,
		LastFailureAt:       h.lastFailureAt,
		ConsecutiveFailures: h.consecutiveFailures,
		TotalSuccesses:      h.totalSuccesses,
		TotalFailures:       h.totalFailures,
		LastError:           h.lastError,
	}
}

// RunCheckpointer drives periodic durable checkpoints. The caller starts it
// in a goroutine alongside World.Run; it returns when ctx is cancelled.
//
// Each tick performs one CheckpointNow: a deep-clone on the world goroutine
// (fast) followed by the durable write off the world goroutine (slow). The
// loop is sequential — at most one checkpoint write is in flight at a time.
// Each completed attempt's outcome is recorded on health (nil-safe) so the
// umbilical can surface checkpoint health remotely (ZBBS-HOME-334).
//
// There is NO immediate first checkpoint: a freshly-loaded world is identical
// to what is already on disk, so the first write waits a full interval. The
// final, authoritative checkpoint at shutdown is the caller's responsibility
// (cancel ctx, wait for this to return, then call CheckpointNow with a fresh
// context while the world goroutine is still alive).
func RunCheckpointer(ctx context.Context, w *World, save CheckpointFunc, health *CheckpointHealth) {
	interval := readCheckpointInterval(ctx, w)
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("sim/checkpoint: periodic checkpointer started (interval %s)", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			err := CheckpointNow(ctx, w, save)
			// A shutdown racing this tick cancels the in-flight write; that's
			// not a real failure, so don't record or log it — the <-ctx.Done()
			// case returns on the next loop iteration.
			if ctx.Err() != nil {
				continue
			}
			if err != nil {
				health.RecordFailure(time.Now(), err)
				log.Printf("sim/checkpoint: %v", err)
			} else {
				finished := time.Now()
				health.RecordSuccess(finished)
				// ZBBS-HOME-399: log every successful periodic checkpoint so an
				// operator can confirm checkpointing is alive — and spot duration
				// creep — from journalctl alone, instead of polling the DB's
				// snapshot_gen. Each line IS one gen advance. One line per
				// interval (default 60s) is negligible under default journald
				// rotation. Failures already log above; shutdown's final
				// checkpoint logs separately (cmd/engine).
				log.Printf("sim/checkpoint: written ok (%s)", finished.Sub(start).Round(time.Millisecond))
			}
		}
	}
}

// CheckpointNow performs one checkpoint: build the immutable clone on the
// world goroutine (via SendContext), then run the durable write off the world
// goroutine against that clone. Exposed so the shutdown path can force a final
// checkpoint synchronously after stopping the periodic loop — call it with a
// FRESH context (not the cancelled run/checkpointer context) so the build send
// and the durable write both complete.
func CheckpointNow(ctx context.Context, w *World, save CheckpointFunc) error {
	res, err := w.SendContext(ctx, CheckpointSnapshotCommand())
	if err != nil {
		return fmt.Errorf("build checkpoint snapshot: %w", err)
	}
	cp, ok := res.(*CheckpointSnapshot)
	if !ok {
		return fmt.Errorf("checkpoint snapshot command returned %T, want *CheckpointSnapshot", res)
	}
	return save(ctx, cp)
}

// readCheckpointInterval reads WorldSettings.CheckpointInterval via a
// context-aware Command (settings live on world-goroutine-owned state), with
// a non-zero fallback. Read once at checkpointer startup; a Settings change
// mid-run takes effect on the next process start (production tuning is
// config + restart, not hot-reload).
//
// SendContext (not Send) so a shutdown racing startup unblocks instead of
// deadlocking on a send to a not-yet-running or already-dead cmds channel; the
// caller checks ctx.Err() after this returns and bails before installing the
// ticker. The non-zero fallback also keeps time.NewTicker from panicking on a
// zero interval. Mirrors cascade/idle_backstop.go readSweepInterval.
func readCheckpointInterval(ctx context.Context, w *World) time.Duration {
	res, err := w.SendContext(ctx, Command{Fn: func(world *World) (any, error) {
		interval := world.Settings.CheckpointInterval
		if interval <= 0 {
			interval = defaultCheckpointInterval
		}
		return interval, nil
	}})
	if err != nil {
		return defaultCheckpointInterval
	}
	interval, ok := res.(time.Duration)
	if !ok || interval <= 0 {
		return defaultCheckpointInterval
	}
	return interval
}
