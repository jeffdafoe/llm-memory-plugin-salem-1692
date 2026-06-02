package pg

import (
	"context"
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// SaveWorld orchestrates a full pg-backed checkpoint of a
// sim.CheckpointSnapshot — the write-side mirror of LoadWorld. It opens one
// transaction via repo.Begin,
// calls every checkpointed aggregate's SaveSnapshot inside that single Tx,
// plus SaveMutableSettings for the runtime-tunable settings subset
// (ZBBS-WORK-363 — zoom floors + agent-ticks pause, the only setting rows the
// checkpoint writes), and commits atomically. Any sub-repo error aborts the whole checkpoint:
// the deferred Rollback discards every write, so a crash or failure mid-
// checkpoint can never leave persistent state half-updated (the GUIDELINES
// consistency line — transient state may be lossy on crash, persistent
// state must stay consistent).
//
// # Aggregates checkpointed
//
// The seven aggregates that own mutable world state and expose SaveSnapshot:
// VillageObjects, Structures, Huddles, Scenes, Actors, Orders, Environment
// (env + phase only). Each SaveSnapshot is a full-snapshot replace via the
// generation-marker pattern — bump a per-table gen sequence, UPSERT every
// live row at the new gen, then DELETE rows still bearing an older gen.
// Rows absent from the in-memory map (an actor who left, a closed scene)
// are swept by that delete-stale step, so the checkpoint is a true mirror
// of the World, not an append.
//
// # Not checkpointed (deliberately)
//
//   - Assets / Recipes / ItemKinds / Terrain — reference data, loaded at
//     startup and hot-reloaded on SIGHUP, never written by the engine loop.
//     They have no SaveSnapshot.
//   - ActionLog / TickTelemetry — append-only sinks, not snapshot state.
//   - Quotes / PayLedger — intentionally restart-lossy: no repo in the
//     sim.Repository facade and none planned. Pending entries lock no
//     coins/stock/presence and carry short TTLs, so losing them on restart
//     is materially harmless (decided 2026-05-20).
//
// # No notImpl tolerance (unlike LoadWorld)
//
// LoadWorld tolerates notImpl sub-repos during cutover-prep because a
// missing read just leaves an empty default. A missing WRITE is different:
// silently skipping an aggregate's checkpoint would drop live state on the
// next restart. So SaveWorld has no requireAllImpl flag — every SaveSnapshot
// error (including errNotImpl from a stub) is a hard failure that rolls the
// whole Tx back. All seven writers are real pg-impls today (pg.NewRepository
// wires only ActionLog + TickTelemetry as notImpl, and neither is a
// checkpoint writer), so this is the all-or-nothing contract, not a
// limitation.
//
// # Write order is NOT FK-load-bearing
//
// The order below mirrors LoadWorld's dependency narrative (roots first)
// for readability, but it is not required for referential safety. The v2
// rewrite deliberately DROPPED every cross-aggregate FK among these tables
// (Slices 11-13: actor->huddle, scene_huddle->village_object, actor->
// structure/room were all dropped, and the new scene table's structure /
// huddle refs were created as TEXT soft-refs with no FK), moving that
// consistency to Go-side validation at LoadWorld. The only FKs left on
// checkpoint tables are within-aggregate child->parent ON DELETE CASCADE
// (e.g. actor_need->actor, structure_room->structure, scene_huddle_ref->
// scene), which each SaveSnapshot already orders parent-before-children
// internally. So the Tx is order-independent across aggregates; no
// DEFERRABLE FK is needed.
//
// # Concurrency
//
// SaveWorld reads a sim.CheckpointSnapshot — a full-fidelity, immutable
// deep-clone of the seven aggregates built by World.BuildCheckpointSnapshot
// on the world goroutine. Because the snapshot is frozen and disconnected
// from live world state, the slow Tx here runs safely OFF the world
// goroutine while the world keeps processing commands. The quiescence point
// is the in-memory clone, not this multi-second write. sim.RunCheckpointer
// (the periodic driver) and the entrypoint's shutdown path compose the
// clone-then-write; see engine/sim/checkpoint.go.
func SaveWorld(ctx context.Context, repo sim.Repository, cp *sim.CheckpointSnapshot) error {
	if cp == nil {
		return fmt.Errorf("pg SaveWorld: nil checkpoint snapshot")
	}

	tx, err := repo.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg SaveWorld: begin tx: %w", err)
	}
	// Roll back unless we reach a clean Commit. After a successful Commit
	// the tx is already closed, so the deferred Rollback is a no-op and its
	// error is intentionally discarded; on any early return below it's what
	// actually unwinds the partial checkpoint.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := repo.VillageObjects.SaveSnapshot(ctx, tx, cp.VillageObjects); err != nil {
		return fmt.Errorf("pg SaveWorld: VillageObjects.SaveSnapshot: %w", err)
	}
	if err := repo.Structures.SaveSnapshot(ctx, tx, cp.Structures); err != nil {
		return fmt.Errorf("pg SaveWorld: Structures.SaveSnapshot: %w", err)
	}
	if err := repo.Huddles.SaveSnapshot(ctx, tx, cp.Huddles); err != nil {
		return fmt.Errorf("pg SaveWorld: Huddles.SaveSnapshot: %w", err)
	}
	if err := repo.Scenes.SaveSnapshot(ctx, tx, cp.Scenes); err != nil {
		return fmt.Errorf("pg SaveWorld: Scenes.SaveSnapshot: %w", err)
	}
	if err := repo.Actors.SaveSnapshot(ctx, tx, cp.Actors); err != nil {
		return fmt.Errorf("pg SaveWorld: Actors.SaveSnapshot: %w", err)
	}
	if err := repo.Orders.SaveSnapshot(ctx, tx, cp.Orders); err != nil {
		return fmt.Errorf("pg SaveWorld: Orders.SaveSnapshot: %w", err)
	}
	if err := repo.Environment.SaveSnapshot(ctx, tx, cp.Environment, cp.Phase); err != nil {
		return fmt.Errorf("pg SaveWorld: Environment.SaveSnapshot: %w", err)
	}
	// Runtime-tunable settings (ZBBS-WORK-363) — the 3-key mutable subset, in the
	// same Tx so a crash can't split a config save from the rest of the checkpoint.
	if err := repo.Environment.SaveMutableSettings(ctx, tx, cp.MutableSettings); err != nil {
		return fmt.Errorf("pg SaveWorld: Environment.SaveMutableSettings: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg SaveWorld: commit: %w", err)
	}
	committed = true
	return nil
}
