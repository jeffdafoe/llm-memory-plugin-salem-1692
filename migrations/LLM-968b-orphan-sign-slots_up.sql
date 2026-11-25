-- LLM-968b: delete the two orphan 'sign' slot rows.
--
-- asset_slot has no foreign key to asset, so when two sign-post hosts were
-- deleted long ago their 'sign' slots stayed behind. The engine skips them at
-- boot ("pg assets LoadAll: asset_slot "sign" references unknown asset ...
-- skipping"). LLM-968 removed the 'sign' slot from every live host (an item is
-- one complete sprite, not a post with a sign attached); these two are the
-- last 'sign' rows.
--
-- Only rows that are still orphans go, and only as the down recreates them
-- (slot 'sign' at offset 22,-78). If a host with one of these ids is here,
-- the slot is a real one and the migration fails rather than touch it. A
-- fresh database has neither row; then this does nothing. Rerunnable.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start.

BEGIN;

LOCK TABLE asset, asset_slot IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    hosts CONSTANT uuid[] := ARRAY['e3658f1d-0b8c-4a24-9e0a-244399723a1e',
                                   '8344ac85-5a46-4b52-9d4b-35a22b200485']::uuid[];
BEGIN
    IF EXISTS (SELECT 1 FROM asset WHERE id = ANY (hosts)) THEN
        RAISE EXCEPTION 'LLM-968b: a host of these sign slots is here, so they are not orphans';
    END IF;
    IF EXISTS (SELECT 1 FROM asset_slot
                WHERE asset_id = ANY (hosts)
                  AND (slot_name <> 'sign' OR offset_x <> 22 OR offset_y <> -78)) THEN
        RAISE EXCEPTION 'LLM-968b: a slot row on these hosts differs from the one the down recreates';
    END IF;
    DELETE FROM asset_slot WHERE asset_id = ANY (hosts);
END $$;

COMMIT;
