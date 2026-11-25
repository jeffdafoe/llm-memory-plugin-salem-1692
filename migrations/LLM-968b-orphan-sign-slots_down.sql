-- LLM-968b down: put the two orphan 'sign' slot rows back (new ids; nothing
-- points at a slot row's id).
--
-- All or nothing, like the up. Without Hanging Sign (the sign of the Mana
-- Seed village catalog; a fresh database never had these rows) neither row
-- may be here, and nothing happens. With it, either neither row is here
-- (migrated: both are recreated) or both are, exactly (not migrated: a
-- rerun, nothing to do). The hosts must still be absent.

BEGIN;

LOCK TABLE asset, asset_slot IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    hosts CONSTANT uuid[] := ARRAY['e3658f1d-0b8c-4a24-9e0a-244399723a1e',
                                   '8344ac85-5a46-4b52-9d4b-35a22b200485']::uuid[];
    n integer;
BEGIN
    IF EXISTS (SELECT 1 FROM asset WHERE id = ANY (hosts)) THEN
        RAISE EXCEPTION 'LLM-968b down: a host of these sign slots is here, so they would not be orphans';
    END IF;
    SELECT count(*) INTO n FROM asset_slot WHERE asset_id = ANY (hosts);
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = '01404aa0-8f3c-4e85-9d55-eb7099d6d89e') THEN
        IF n > 0 THEN
            RAISE EXCEPTION 'LLM-968b down: slot rows on these hosts are here without the catalog';
        END IF;
        RETURN;
    END IF;
    IF n = 0 THEN
        INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
        SELECT h, 'sign', 22, -78 FROM unnest(hosts) h;
    END IF;
    SELECT count(*) INTO n FROM asset_slot
     WHERE asset_id = ANY (hosts)
       AND slot_name IS NOT DISTINCT FROM 'sign' AND offset_x IS NOT DISTINCT FROM 22
       AND offset_y IS NOT DISTINCT FROM -78;
    IF n <> 2 OR EXISTS (SELECT 1 FROM asset_slot WHERE asset_id = ANY (hosts)
                          AND (slot_name IS DISTINCT FROM 'sign' OR offset_x IS DISTINCT FROM 22
                               OR offset_y IS DISTINCT FROM -78)) THEN
        RAISE EXCEPTION 'LLM-968b down: the slot rows on these hosts are not the two it recreates';
    END IF;
END $$;

COMMIT;
