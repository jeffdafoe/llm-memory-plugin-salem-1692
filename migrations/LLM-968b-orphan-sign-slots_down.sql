-- LLM-968b down: put the two orphan 'sign' slot rows back (new ids; nothing
-- points at a slot row's id).
--
-- Only where the Mana Seed village catalog is here (Hanging Sign stands for
-- it; a fresh database never had these rows) and only while the hosts are
-- still absent, as they were when the up removed the rows.

BEGIN;

LOCK TABLE asset, asset_slot IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    hosts CONSTANT uuid[] := ARRAY['e3658f1d-0b8c-4a24-9e0a-244399723a1e',
                                   '8344ac85-5a46-4b52-9d4b-35a22b200485']::uuid[];
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = '01404aa0-8f3c-4e85-9d55-eb7099d6d89e') THEN
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM asset WHERE id = ANY (hosts)) THEN
        RAISE EXCEPTION 'LLM-968b down: a host of these sign slots is here, so they would not be orphans';
    END IF;
    INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
    SELECT h, 'sign', 22, -78 FROM unnest(hosts) h
    ON CONFLICT (asset_id, slot_name) DO NOTHING;
    IF EXISTS (SELECT 1 FROM asset_slot
                WHERE asset_id = ANY (hosts)
                  AND (slot_name <> 'sign' OR offset_x <> 22 OR offset_y <> -78)) THEN
        RAISE EXCEPTION 'LLM-968b down: a slot row on these hosts differs from the one recreated';
    END IF;
END $$;

COMMIT;
