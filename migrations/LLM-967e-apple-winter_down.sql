-- LLM-967e down: the apple trees lose their winter states (and those states'
-- tags), so in winter they fall back to their autumn look, and apples go back
-- to regrowing at 125% in winter instead of none. A placed tree showing a winter
-- state moves to its picked autumn state first, so no object points at a state
-- that is gone (the engine's next sweep would pick autumn-bare anyway).
-- Run this BEFORE reverting the engine: an engine without LLM-967e reads
-- apples=0 as out of range and falls back to its own default for every item.

BEGIN;

DO $$
DECLARE
    apple CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    apple2 CONSTANT uuid := '48f8b1e5-a9e6-45d7-87f9-acbbf83fcc61';
    apple3 CONSTANT uuid := 'e8d24766-7ef7-4380-8ff4-0e4de2d9cb05';
    winter CONSTANT text[] := ARRAY['winter-clean', 'winter-leaves', 'winter-snowy-top', 'winter-snowy'];
BEGIN
    UPDATE setting
       SET value = regexp_replace(value, '(^|,)\s*apples\s*=\s*0\s*(,|$)', '\1apples=125\2', 'i')
     WHERE key = 'winter_regrow_factors';

    UPDATE village_object SET current_state = 'autumn-bare'
     WHERE asset_id IN (apple, apple2, apple3) AND current_state = ANY (winter);

    DELETE FROM asset_state_tag
     WHERE state_id IN (SELECT id FROM asset_state WHERE asset_id IN (apple, apple2, apple3) AND state = ANY (winter));
    DELETE FROM asset_state WHERE asset_id IN (apple, apple2, apple3) AND state = ANY (winter);

    IF EXISTS (SELECT 1 FROM asset_state WHERE asset_id IN (apple, apple2, apple3) AND state = ANY (winter)) THEN
        RAISE EXCEPTION 'LLM-967e down: an apple tree winter state is left behind';
    END IF;
END $$;

COMMIT;
