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
--      Refuses if a duck_pendant row already exists (an engine-minted
--      discovery), so the down can always turn it back into the iron ward.
--   2. Adds the `wear` capability to the three pendants: duck_pendant,
--      silver_locket, whalebone_charm (sim.CapabilityWear).
--   3. actor.wearing — the item kinds an actor has on (sim.Actor.Wearing),
--      written by the checkpoint.
--
-- ENGINE-OWNED TABLE (actor). Apply with the engine STOPPED (stop -> migrate
-- -> start, the standard deploy order): the old binary's checkpoint does not
-- write wearing and the new binary's does.
--
-- Rerun-safe.

BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM actor_inventory WHERE item_kind = 'iron_ward') THEN
        RAISE EXCEPTION 'LLM-800: an actor holds an iron ward; renaming it to the duck pendant would hand them a duck';
    END IF;
    IF EXISTS (SELECT 1 FROM item_kind WHERE name = 'iron_ward') THEN
        IF EXISTS (SELECT 1 FROM item_kind WHERE name = 'duck_pendant') THEN
            RAISE EXCEPTION 'LLM-800: both iron_ward and duck_pendant exist; remove the duck_pendant discovery row first';
        END IF;
        UPDATE item_kind SET name = 'duck_pendant' WHERE name = 'iron_ward';
    END IF;
END $$;

INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('duck_pendant', 'Duck pendant', 'duck pendant', 'duck pendants',
     'charm', 330, '{}'::text[],
     'A little duck carved from pale wood and hung on a cord, to be worn about the neck.')
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    description            = EXCLUDED.description;

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
