-- LLM-592 down: strip the distributor's clothing restock lines.
--
-- Removes exactly the five garment entries the _up appended, by item name, and
-- leaves every other entry in the array alone — the food and fuel lines were never
-- ours to touch. Rebuilding the array by filtering is the only safe shape here: a
-- positional splice would break the moment an operator edits the policy through
-- the umbilical, and replacing the whole blob with a remembered literal would
-- silently discard any such edit.
--
-- actor_attribute is checkpoint-written; apply with the engine stopped.

BEGIN;

DO $$
DECLARE distributor_id uuid;
BEGIN
    SELECT vo.owner_actor_id::uuid INTO distributor_id
      FROM village_object vo
     WHERE 'distributor' = ANY(vo.tags)
       AND vo.owner_actor_id IS NOT NULL
     LIMIT 1;

    IF distributor_id IS NULL THEN
        RETURN;
    END IF;

    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(
                   (SELECT jsonb_agg(entry)
                      FROM jsonb_array_elements(aa.params->'restock') AS entry
                     WHERE entry->>'item' NOT IN ('shift', 'breeches', 'gown', 'coat', 'cloak')),
                   '[]'::jsonb))
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant'
       AND aa.params->'restock' IS NOT NULL;
END $$;

COMMIT;
