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
// The eight aggregates that own mutable world state and expose SaveSnapshot:
// VillageObjects, Structures, Huddles, Scenes, Actors, LaborContracts, Orders,
// Environment (env + phase only). Each SaveSnapshot is a full-snapshot replace via the
// generation-marker pattern — bump a per-table gen sequence, UPSERT every
// live row at the new gen, then DELETE rows still bearing an older gen.
// Rows absent from the in-memory map (an actor who left, a closed scene)
// are swept by that delete-stale step, so the checkpoint is a true mirror
// of the World, not an append.
//
// # Not checkpointed (deliberately)
//
//   - Assets / Recipes / Terrain — reference data, loaded at startup and
//     hot-reloaded on SIGHUP, never written by the engine loop. They have no
//     SaveSnapshot. ItemKinds is reference data too, with ONE exception:
//     engine-minted DISCOVERED kinds (ZBBS-WORK-412, category "unknown") are
//     upserted via saveDiscoveredKinds below so an agent's invented good
//     survives restart; authored kinds are still never written.
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
// whole Tx back. All eight writers are real pg-impls today (pg.NewRepository
// wires only ActionLog + TickTelemetry as notImpl, and neither is a
// checkpoint writer), so this is the all-or-nothing contract, not a
// limitation.
//
// # Write order IS FK-load-bearing: Orders before Actors
//
// The v2 rewrite dropped nearly every cross-aggregate FK among these tables
// (Slices 11-13: actor->huddle, scene_huddle->village_object, actor->
// structure/room were all dropped, and the new scene table's structure /
// huddle refs were created as TEXT soft-refs with no FK), moving that
// consistency to Go-side validation at LoadWorld. ONE cross-aggregate FK
// survived the purge: room_access.granted_via_ledger_id -> pay_ledger(id)
// (subspace_access_granted_via_ledger_id_fkey, NOT deferred — checked at
// statement time). room_access is written by Actors.SaveSnapshot;
// pay_ledger by Orders.SaveSnapshot. So Orders MUST run before Actors:
// when an order is minted, accepted, and delivered with a room grant all
// inside one checkpoint window (instant-settle lodging does this in ~3s),
// both rows are new to the same SaveWorld, and saving the grant before its
// ledger row aborts the checkpoint — permanently, since the same snapshot
// state re-fails every cycle (ZBBS-HOME-451, live wedge 2026-06-12).
// pay_ledger's only outbound FKs are item_kind (startup reference data)
// and its parent_id self-ref, so running Orders early is safe. The
// remaining FKs on checkpoint tables are within-aggregate child->parent
// ON DELETE CASCADE (e.g. actor_need->actor, structure_room->structure,
// scene_huddle_ref->scene), which each SaveSnapshot already orders
// parent-before-children internally — those impose no cross-aggregate
// ordering.
//
// # Concurrency
//
// SaveWorld reads a sim.CheckpointSnapshot — a full-fidelity, immutable
// deep-clone of the eight aggregates built by World.BuildCheckpointSnapshot
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
	// Orders before Actors — room_access (actors aggregate) carries a real,
	// non-deferred FK to pay_ledger (orders aggregate); see the write-order
	// section in the doc comment above (ZBBS-HOME-451).
	if err := repo.Orders.SaveSnapshot(ctx, tx, cp.Orders); err != nil {
		return fmt.Errorf("pg SaveWorld: Orders.SaveSnapshot: %w", err)
	}
	if err := repo.Actors.SaveSnapshot(ctx, tx, cp.Actors); err != nil {
		return fmt.Errorf("pg SaveWorld: Actors.SaveSnapshot: %w", err)
	}
	// LLM-369: the in-flight visitor mirror — the COMPLEMENT of Actors.SaveSnapshot
	// (which skips VisitorState != nil). Handed the same cp.Actors map, it persists
	// exactly the visitor subset the actor aggregate drops. Same Tx, so a crash
	// can't split a visitor's persistence from the rest of the checkpoint.
	if err := repo.Visitors.SaveSnapshot(ctx, tx, cp.Actors); err != nil {
		return fmt.Errorf("pg SaveWorld: Visitors.SaveSnapshot: %w", err)
	}
	// LLM-372: the durable returner set (recurring_visitor + acquaintance children).
	// Same Tx as Visitors so a visitor's recurring_visitor_id link and the row it
	// points at can never split across a crash. Upsert-only (no sweep) — these rows
	// outlive the visit. No cross-aggregate FK (pc_actor_id is a soft ref), so order
	// relative to the other aggregates is free.
	if err := repo.RecurringVisitors.SaveSnapshot(ctx, tx, cp.RecurringVisitors); err != nil {
		return fmt.Errorf("pg SaveWorld: RecurringVisitors.SaveSnapshot: %w", err)
	}
	// LLM-259: the accepted-labor-contract mirror (en_route + working). No cross-
	// aggregate FK (worker_id/employer_id are soft TEXT refs to actor, Go-side
	// validated at LoadWorld), so order is free — placed after Actors for
	// readability. Same Tx as everything else, so the DURABLE snapshot is atomic:
	// Postgres never holds paid actor coins while retaining the active
	// labor_contract row, or vice versa. A crash before the next checkpoint rolls
	// both back to the previous checkpoint and the in-memory settlement replays on
	// reload — the standard checkpoint-replay property, so durable coins still
	// settle exactly once; a write-through side effect of the replayed settle
	// (e.g. the agent_action_log `labored` audit row) can duplicate, same as every
	// other write-through action in the engine.
	if err := repo.LaborContracts.SaveSnapshot(ctx, tx, cp.LaborContracts); err != nil {
		return fmt.Errorf("pg SaveWorld: LaborContracts.SaveSnapshot: %w", err)
	}
	if err := repo.Environment.SaveSnapshot(ctx, tx, cp.Environment, cp.Phase); err != nil {
		return fmt.Errorf("pg SaveWorld: Environment.SaveSnapshot: %w", err)
	}
	// Runtime-tunable settings (ZBBS-WORK-363) — the 3-key mutable subset, in the
	// same Tx so a crash can't split a config save from the rest of the checkpoint.
	if err := repo.Environment.SaveMutableSettings(ctx, tx, cp.MutableSettings); err != nil {
		return fmt.Errorf("pg SaveWorld: Environment.SaveMutableSettings: %w", err)
	}
	// Discovered (engine-minted) item kinds (ZBBS-WORK-412) — upserted in the
	// same Tx so a crash can't split a discovery from the rest of the checkpoint.
	// INSERT ... ON CONFLICT DO NOTHING: only unknown-category discoveries are
	// written; authored item_kind rows stay reference data (SIGHUP hot-reload).
	if err := saveDiscoveredKinds(ctx, tx, cp.DiscoveredKinds); err != nil {
		return fmt.Errorf("pg SaveWorld: saveDiscoveredKinds: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg SaveWorld: commit: %w", err)
	}
	committed = true
	return nil
}
