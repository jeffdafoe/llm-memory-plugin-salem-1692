-- LLM-800: the duck pendant, the `wear` capability, and what an actor wears.
--
-- WHY. Jeff (10-16): one of the game's pendants becomes a duck pendant; the
-- one who wears it gets a duck that follows them. The Your Pack list gets its
-- first item actions — Wear and Remove on a pendant.
--
-- WHAT THIS DOES:
--   1. Renames item_kind `iron_ward` to `duck_pendant` with a new label and
--      description. Every FK to item_kind.name is ON UPDATE CASCADE, so the
--      price anchor follows. iron_ward had no holder, no factor source and no
--      restock line (LLM-592 left it out), so nothing in the economy moves.
--      Refuses unless the catalog holds exactly the starting state — an
--      iron_ward nobody holds and no duck_pendant (an engine-minted discovery
--      would be one) — so the down is always its inverse.
--   2. Adds the `wear` capability to the three pendants: duck_pendant,
--      silver_locket, whalebone_charm (sim.CapabilityWear).
--   3. actor.wearing — the item kinds an actor has on (sim.Actor.Wearing),
--      written by the checkpoint.
--
-- ENGINE-OWNED TABLE (actor). Apply with the engine STOPPED (stop -> migrate
-- -> start, the standard deploy order): the old binary's checkpoint does not
-- write wearing and the new binary's does.
--
-- Rerun-safe: actor.wearing is added in this same transaction, so its
-- presence means the rename already happened and the checks are skipped.

BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = current_schema() AND table_name = 'actor' AND column_name = 'wearing') THEN
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM item_kind WHERE name = 'duck_pendant') THEN
        RAISE EXCEPTION 'LLM-800: duck_pendant already exists; remove that discovery row first';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'iron_ward') THEN
        RAISE EXCEPTION 'LLM-800: iron_ward does not exist; nothing to turn into the duck pendant';
    END IF;
    IF EXISTS (SELECT 1 FROM actor_inventory WHERE item_kind = 'iron_ward') THEN
        RAISE EXCEPTION 'LLM-800: an actor holds an iron ward; renaming it to the duck pendant would hand them a duck';
    END IF;
    UPDATE item_kind SET name = 'duck_pendant' WHERE name = 'iron_ward';
END $$;

UPDATE item_kind
   SET display_label          = 'Duck pendant',
       display_label_singular = 'duck pendant',
       display_label_plural   = 'duck pendants',
       description            = 'A little duck carved from pale wood and hung on a cord, to be worn about the neck.'
 WHERE name = 'duck_pendant';

UPDATE item_kind
   SET capabilities = array_append(capabilities, 'wear')
 WHERE name IN ('duck_pendant', 'silver_locket', 'whalebone_charm')
   AND NOT ('wear' = ANY(capabilities));

ALTER TABLE actor ADD COLUMN IF NOT EXISTS wearing text[] NOT NULL DEFAULT '{}';

DO $$
BEGIN
    IF (SELECT count(*) FROM item_kind
         WHERE name IN ('duck_pendant', 'silver_locket', 'whalebone_charm')
           AND 'wear' = ANY(capabilities)) <> 3 THEN
        RAISE EXCEPTION 'LLM-800: the three pendants do not all carry the wear capability';
    END IF;
END $$;

COMMIT;
