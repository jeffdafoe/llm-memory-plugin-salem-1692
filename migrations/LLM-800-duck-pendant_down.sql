-- LLM-800 down: drop actor.wearing, take the `wear` capability off the
-- pendants, and turn the duck pendant back into the iron ward (labels and
-- description as LLM-410 seeded them). Refuses unless the catalog holds exactly
-- what the up left — a duck_pendant nobody holds and no iron_ward — so it is
-- the up's inverse. Same engine-stopped caveat as the up.
--
-- Rerun-safe: actor.wearing is dropped in this same transaction, so its
-- absence means the rename back already happened and the checks are skipped.

BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema = current_schema() AND table_name = 'actor' AND column_name = 'wearing') THEN
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM item_kind WHERE name = 'iron_ward') THEN
        RAISE EXCEPTION 'LLM-800 down: iron_ward already exists';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'duck_pendant') THEN
        RAISE EXCEPTION 'LLM-800 down: duck_pendant does not exist; nothing to turn back into the iron ward';
    END IF;
    IF EXISTS (SELECT 1 FROM actor_inventory WHERE item_kind = 'duck_pendant') THEN
        RAISE EXCEPTION 'LLM-800 down: an actor holds a duck pendant; take it back first';
    END IF;
    UPDATE item_kind SET name = 'iron_ward' WHERE name = 'duck_pendant';
END $$;

UPDATE item_kind
   SET display_label          = 'Iron ward',
       display_label_singular = 'iron ward',
       display_label_plural   = 'iron wards',
       description            = 'A twist of cold iron worn against the skin, to turn away ill wishes.'
 WHERE name = 'iron_ward';

UPDATE item_kind
   SET capabilities = array_remove(capabilities, 'wear')
 WHERE name IN ('iron_ward', 'silver_locket', 'whalebone_charm');

ALTER TABLE actor DROP COLUMN IF EXISTS wearing;

COMMIT;
