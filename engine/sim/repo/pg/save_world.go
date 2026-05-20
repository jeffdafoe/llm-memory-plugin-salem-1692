package pg

import (
	"context"
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// SaveWorld orchestrates a full pg-backed checkpoint of a sim.World — the
// write-side mirror of LoadWorld. It opens one transaction via repo.Begin,
// calls every checkpointed aggregate's SaveSnapshot inside that single Tx,
// and commits atomically. Any sub-repo error aborts the whole checkpoint:
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
//   - Quotes / PayLedger — no repo in the sim.Repository facade yet; their
//     checkpoint/reload is its own future slice. (PayLedger in particular
//     carries a real reconcile concern — see notes/active-work.)
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
// SaveWorld reads the World's aggregate maps directly. The caller must
// ensure the World is quiesced for the duration of the call — i.e. invoke
// it from the world goroutine (between command applications) or under
// whatever mutex the entrypoint uses to serialize writes against the
// checkpoint. Wiring that into a periodic checkpoint timer is the engine-
// entrypoint slice's job, not this orchestrator's.
func SaveWorld(ctx context.Context, repo sim.Repository, w *sim.World) error {
	if w == nil {
		return fmt.Errorf("pg SaveWorld: nil world")
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

	if err := repo.VillageObjects.SaveSnapshot(ctx, tx, w.VillageObjects); err != nil {
		return fmt.Errorf("pg SaveWorld: VillageObjects.SaveSnapshot: %w", err)
	}
	if err := repo.Structures.SaveSnapshot(ctx, tx, w.Structures); err != nil {
		return fmt.Errorf("pg SaveWorld: Structures.SaveSnapshot: %w", err)
	}
	if err := repo.Huddles.SaveSnapshot(ctx, tx, w.Huddles); err != nil {
		return fmt.Errorf("pg SaveWorld: Huddles.SaveSnapshot: %w", err)
	}
	if err := repo.Scenes.SaveSnapshot(ctx, tx, w.Scenes); err != nil {
		return fmt.Errorf("pg SaveWorld: Scenes.SaveSnapshot: %w", err)
	}
	if err := repo.Actors.SaveSnapshot(ctx, tx, w.Actors); err != nil {
		return fmt.Errorf("pg SaveWorld: Actors.SaveSnapshot: %w", err)
	}
	if err := repo.Orders.SaveSnapshot(ctx, tx, w.Orders); err != nil {
		return fmt.Errorf("pg SaveWorld: Orders.SaveSnapshot: %w", err)
	}
	if err := repo.Environment.SaveSnapshot(ctx, tx, w.Environment, w.Phase); err != nil {
		return fmt.Errorf("pg SaveWorld: Environment.SaveSnapshot: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg SaveWorld: commit: %w", err)
	}
	committed = true
	return nil
}
