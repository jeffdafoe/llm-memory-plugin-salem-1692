-- LLM-606 down: wheat yield back to 2 grains per plant.
--
-- Restores the pre-migration LIVE tuning (2 per plant), which is the point of
-- a rollback against production. On a rebuilt-from-migrations database the
-- pre-LLM-606 template was the LLM-576 seed (3/120), so this down lands such
-- a DB on 2 rather than 3 — accepted: the down exists to unwind prod, and the
-- template value is a tuning, not a shape invariant.
--
-- available_quantity is clamped to the new max on placed rows so no plant is
-- left holding more stock than its maximum.
--
-- Same engine-stopped requirement as the up (object_refresh is
-- checkpoint-written).

BEGIN;

UPDATE asset_refresh_default
   SET available_quantity = 2, max_quantity = 2
 WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000576'
   AND gather_item = 'wheat'
   AND max_quantity = 3;

UPDATE object_refresh r
   SET max_quantity = 2,
       available_quantity = LEAST(r.available_quantity, 2)
  FROM village_object vo
 WHERE vo.id = r.object_id
   AND vo.asset_id = '019e5f00-c401-7a10-9e00-000000000576'
   AND r.gather_item = 'wheat'
   AND r.max_quantity = 3;

COMMIT;
