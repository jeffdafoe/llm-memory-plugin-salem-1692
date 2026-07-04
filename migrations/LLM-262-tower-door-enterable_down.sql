-- Revert LLM-262: restore the tower assets to doorless (unenterable). Symmetric with
-- the up migration — matches by stable seed name and asserts exactly 4 rows so seed
-- drift fails loudly instead of silently partial-applying.

BEGIN;

DO $$
DECLARE
    updated_count integer;
BEGIN
    UPDATE asset
    SET door_offset_x = NULL,
        door_offset_y = NULL
    WHERE name IN ('Black Tower', 'Blue Tower', 'Red Tower', 'Yellow Tower');

    GET DIAGNOSTICS updated_count = ROW_COUNT;

    IF updated_count <> 4 THEN
        RAISE EXCEPTION 'LLM-262: expected to reset door_offset on 4 tower assets, updated %', updated_count;
    END IF;
END $$;

COMMIT;
