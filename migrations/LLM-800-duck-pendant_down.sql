-- LLM-800 down: drop actor.wearing, take the `wear` capability off the
-- pendants, and turn the duck pendant back into the iron ward (description as
-- LLM-410 seeded it). Refuses while anyone holds a duck pendant — the rename
-- would quietly turn their pendant into an iron ward. Same engine-stopped
-- caveat as the up.
--
-- Rerun-safe.

BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM actor_inventory WHERE item_kind = 'duck_pendant') THEN
        RAISE EXCEPTION 'LLM-800 down: an actor holds a duck pendant; take it back first';
    END IF;
    IF EXISTS (SELECT 1 FROM item_kind WHERE name = 'duck_pendant') THEN
        IF EXISTS (SELECT 1 FROM item_kind WHERE name = 'iron_ward') THEN
            RAISE EXCEPTION 'LLM-800 down: both duck_pendant and iron_ward exist; nothing to rename';
        END IF;
        UPDATE item_kind SET name = 'iron_ward' WHERE name = 'duck_pendant';
    END IF;
END $$;

UPDATE item_kind
   SET display_label          = 'Iron ward',
       display_label_singular = 'iron ward',
       display_label_plural   = 'iron wards',
       description            = 'A twist of cold iron worn against the skin, to turn away ill wishes.'
 WHERE name = 'iron_ward';

UPDATE item_kind
   SET capabilities = array_remove(capabilities, 'wear')
 WHERE name IN ('iron_ward', 'duck_pendant', 'silver_locket', 'whalebone_charm');

ALTER TABLE actor DROP COLUMN IF EXISTS wearing;

COMMIT;
