package sim

import (
	"context"
	"fmt"
	"log"
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
	Actors         map[ActorID]*Actor
	Structures     map[StructureID]*Structure
	Huddles        map[HuddleID]*Huddle
	Scenes         map[SceneID]*Scene
	Orders         map[OrderID]*Order
	VillageObjects map[VillageObjectID]*VillageObject
	Environment    WorldEnvironment
	Phase          Phase
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

// RunCheckpointer drives periodic durable checkpoints. The caller starts it
// in a goroutine alongside World.Run; it returns when ctx is cancelled.
//
// Each tick performs one CheckpointNow: a deep-clone on the world goroutine
// (fast) followed by the durable write off the world goroutine (slow). The
// loop is sequential — at most one checkpoint write is in flight at a time.
//
// There is NO immediate first checkpoint: a freshly-loaded world is identical
// to what is already on disk, so the first write waits a full interval. The
// final, authoritative checkpoint at shutdown is the caller's responsibility
// (cancel ctx, wait for this to return, then call CheckpointNow with a fresh
// context while the world goroutine is still alive).
func RunCheckpointer(ctx context.Context, w *World, save CheckpointFunc) {
	interval := readCheckpointInterval(ctx, w)
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := CheckpointNow(ctx, w, save); err != nil && ctx.Err() == nil {
				log.Printf("sim/checkpoint: %v", err)
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
