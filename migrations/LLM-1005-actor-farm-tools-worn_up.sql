-- LLM-1005: actor.farm_tools_worn_qty / farm_tools_worn_at — the shovels the last
-- farm-upkeep assessment wore out of a farm owner's pack (sim.Actor.FarmToolsWorn).
--
-- WHY. The daily assessment deletes the owner's shovels while she sleeps. The
-- `## Farm upkeep` cue now names that loss until the next assessment, so an owner
-- who bought shovels and holds none knows they wore out instead of blaming the
-- smith. It is checkpointed with the inventory it explains, so a restart cannot
-- leave the shovels gone and the explanation lost.
--
-- ENGINE-OWNED TABLE (actor). Apply with the engine STOPPED (stop -> migrate ->
-- start, the standard deploy order): the old binary's checkpoint does not write
-- these columns and the new binary's does.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS farm_tools_worn_qty integer;
ALTER TABLE actor ADD COLUMN IF NOT EXISTS farm_tools_worn_at timestamp with time zone;

COMMIT;
