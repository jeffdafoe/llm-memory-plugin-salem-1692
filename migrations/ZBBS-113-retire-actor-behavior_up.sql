-- ZBBS-113: retire the actor.behavior column and the npc_behavior
-- allowlist table.
--
-- The behavior column (added by ZBBS-049 on the legacy `npc` table,
-- carried into `actor` by ZBBS-084) was the single-slot behavior
-- slug for an NPC, dispatched via switches in npc_scheduler.go and
-- the various route starters. ZBBS-096 introduced the attribute
-- system (actor_attribute + attribute_definition) as a many-to-many
-- replacement; ZBBS-097 migrated the last legacy slug ('worker')
-- into the new system; ZBBS-106 dropped the standalone worker
-- attribute_definition once roles carried the worker hint via
-- behaviors JSONB. From ZBBS-097 onward, no engine code reads the
-- column for dispatch — it survives only as display drift on the
-- handleListNPCs response and as a nullable column on the schema.
--
-- Code paths that scanned actor.behavior or wrote into it (the
-- handleSetNPCBehavior endpoint, the legacy NPC.Behavior API field,
-- the npc_behavior_changed WS broadcast, the apply_npc_behavior_change
-- client handler and its container meta) are removed in the same
-- commit so the rollback shape is unambiguous: code without the
-- column on a schema with the column is fine; code without the
-- column on a schema that still has it just leaves the column
-- empty going forward.
--
-- The npc_behavior allowlist table (ZBBS-056) is dropped at the
-- same time. attribute_definition supplies the same shape (slug +
-- display_name) plus the behaviors JSONB for dispatch wiring;
-- handleListNPCBehaviors has been reading from attribute_definition
-- since ZBBS-096.

BEGIN;

-- Column drop cascades through the inline FK constraint to
-- npc_behavior that ZBBS-084 carried over from ZBBS-056.
ALTER TABLE actor DROP COLUMN IF EXISTS behavior;

DROP TABLE IF EXISTS npc_behavior;

COMMIT;
