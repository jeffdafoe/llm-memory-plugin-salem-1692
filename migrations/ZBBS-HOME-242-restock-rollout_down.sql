-- ZBBS-HOME-242 down. Reverses the restock rollout migration.
--
-- Limitations of this revert:
--   * The deleted Abigail/Benjamin James actors and the duplicate
--     James Residence are NOT restored. They were placeholder rows
--     with no operational state; recreating them would require
--     reproducing their original UUIDs and there's nothing to gain
--     from that.
--   * The narrative seed_text rewrites are lost on revert — the
--     ON CONFLICT path overwrote whatever was there before (which
--     was nothing for Elizabeth and Moses).

BEGIN;

-- Restore Ellis Farm + James Farm entry_policy. Original values:
-- Ellis Farm = 'none', James Farm = 'anyone'.
UPDATE village_object SET entry_policy = 'none'
 WHERE id = '019e138d-724b-75d8-9374-9d931ebc93cd'::uuid;
UPDATE village_object SET entry_policy = 'anyone'
 WHERE id = '019e1390-0639-7bf6-8b66-08f95414079c'::uuid;

-- Drop restock from Josiah and John, restore '{}' default.
UPDATE actor_attribute SET params = '{}'::jsonb
 WHERE slug IN ('merchant', 'tavernkeeper')
   AND actor_id IN (
       (SELECT id FROM actor WHERE display_name = 'Josiah Thorne'),
       (SELECT id FROM actor WHERE display_name = 'John Ellis')
   );

-- Demote Elizabeth + Moses. Restore llm_memory_agent / work / hours
-- to NULL. Drop their actor_narrative_state and actor_attribute rows.
UPDATE actor SET llm_memory_agent = NULL,
                 work_structure_id = NULL,
                 active_start_hour = NULL,
                 active_end_hour   = NULL
 WHERE display_name IN ('Elizabeth Ellis', 'Moses James');

DELETE FROM actor_narrative_state
 WHERE actor_id IN (
     (SELECT id FROM actor WHERE display_name = 'Elizabeth Ellis'),
     (SELECT id FROM actor WHERE display_name = 'Moses James')
 );

DELETE FROM actor_attribute
 WHERE slug IN ('dairykeeper', 'farmer')
   AND actor_id IN (
       (SELECT id FROM actor WHERE display_name = 'Elizabeth Ellis'),
       (SELECT id FROM actor WHERE display_name = 'Moses James')
   );

-- Strip the appended discipline-copy paragraph from the keeper
-- attribute_definitions. Best-effort regex strip; idempotent.
UPDATE attribute_definition
   SET instructions = REGEXP_REPLACE(
           instructions,
           E'\\n\\nYour stock is replenished by your work and \\(later\\) by trips to your suppliers\\..*?Do not invent suppliers, prices, or transactions\\.',
           '',
           'gs'
       ),
       updated_at = now()
 WHERE slug IN ('blacksmith', 'herbalist', 'merchant');

-- Drop the new attribute definitions. Will fail if any actor_attribute
-- row still references them (shouldn't after the deletes above).
DELETE FROM attribute_definition WHERE slug IN ('farmer', 'dairykeeper');

COMMIT;
