-- LLM-592 follow-up down: put Ezekiel's garments back to their seeded wear.
--
-- Guarded on the exact values the _up wrote, so a revert after the engine has
-- worn them further does not hand him back clothes he has since used up. Same
-- posture as the seed migration's down: undo what this migration DID, never
-- overwrite live state that has moved on.
--
-- actor_inventory is checkpoint-written; apply with the engine stopped.

BEGIN;

-- Item-specific and all-or-nothing, mirroring the _up (code_review). A value
-- matched without its kind could restore the wrong garment, and restoring only
-- ONE of the pair would leave a state neither migration ever created.
CREATE TEMP TABLE llm592_ezekiel_pair (item_kind text, applied int, restore int) ON COMMIT DROP;
INSERT INTO llm592_ezekiel_pair (item_kind, applied, restore) VALUES
    ('shift',    1188,  9720),
    ('breeches', 1584, 12960);

DO $$
DECLARE
    ezekiel constant uuid := '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4';  -- Ezekiel Crane
    eligible int;
BEGIN
    SELECT count(*) INTO eligible
      FROM actor_inventory ai
      JOIN llm592_ezekiel_pair p ON p.item_kind = ai.item_kind
     WHERE ai.actor_id = ezekiel
       AND ai.worn_minutes_left = p.applied;

    IF eligible <> 2 THEN
        -- The engine has worn one or both since the _up ran. Restoring now would
        -- hand him back clothes he has since used up, so leave both alone.
        RAISE NOTICE 'LLM-592 down: Ezekiel''s garments are not both at the applied values (% of 2) — left as they are', eligible;
        RETURN;
    END IF;

    UPDATE actor_inventory ai
       SET worn_minutes_left = p.restore
      FROM llm592_ezekiel_pair p
     WHERE ai.actor_id = ezekiel
       AND ai.item_kind = p.item_kind
       AND ai.worn_minutes_left = p.applied;
END $$;

COMMIT;
