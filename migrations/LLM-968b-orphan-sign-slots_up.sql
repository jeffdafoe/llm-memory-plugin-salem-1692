-- LLM-968b: delete the two orphan 'sign' slot rows.
--
-- asset_slot has no foreign key to asset, so when two sign-post hosts were
-- deleted long ago their 'sign' slots stayed behind. The engine skips them at
-- boot ("pg assets LoadAll: asset_slot "sign" references unknown asset ...
-- skipping"). LLM-968 removed the 'sign' slot from every live host (an item is
-- one complete sprite, not a post with a sign attached); these two are the
-- last 'sign' rows.
--
-- All or nothing, so the down reverses it exactly: either both rows are here
-- as the down recreates them (slot 'sign' at offset 22,-78, hosts absent,
-- Hanging Sign here as the sign of the Mana Seed catalog), or neither is
-- (already migrated; a fresh database never had them). Anything else fails.
-- If a host with one of these ids is here, the slots are real ones and the
-- migration fails rather than touch them. Rerunnable.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start.

BEGIN;

LOCK TABLE asset, asset_slot IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    hosts CONSTANT uuid[] := ARRAY['e3658f1d-0b8c-4a24-9e0a-244399723a1e',
                                   '8344ac85-5a46-4b52-9d4b-35a22b200485']::uuid[];
    n integer;
BEGIN
    IF EXISTS (SELECT 1 FROM asset WHERE id = ANY (hosts)) THEN
        RAISE EXCEPTION 'LLM-968b: a host of these sign slots is here, so they are not orphans';
    END IF;
    SELECT count(*) INTO n FROM asset_slot WHERE asset_id = ANY (hosts);
    IF n = 0 THEN
        RETURN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = '01404aa0-8f3c-4e85-9d55-eb7099d6d89e') THEN
        RAISE EXCEPTION 'LLM-968b: Hanging Sign is not here, so the down would not bring these rows back';
    END IF;
    SELECT count(*) INTO n FROM asset_slot
     WHERE asset_id = ANY (hosts)
       AND slot_name IS NOT DISTINCT FROM 'sign' AND offset_x IS NOT DISTINCT FROM 22
       AND offset_y IS NOT DISTINCT FROM -78;
    IF n <> 2 OR EXISTS (SELECT 1 FROM asset_slot WHERE asset_id = ANY (hosts)
                          AND (slot_name IS DISTINCT FROM 'sign' OR offset_x IS DISTINCT FROM 22
                               OR offset_y IS DISTINCT FROM -78)) THEN
        RAISE EXCEPTION 'LLM-968b: the slot rows on these hosts are not the two the down recreates';
    END IF;
    DELETE FROM asset_slot WHERE asset_id = ANY (hosts);
END $$;

COMMIT;
