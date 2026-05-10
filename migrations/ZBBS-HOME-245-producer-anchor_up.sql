-- ZBBS-HOME-245 — Anchor Elizabeth + Moses at their stalls and seed
-- starter inventory.
--
-- HOME-242 promoted Elizabeth and Moses to producer NPCs but didn't
-- physically move them to their work_structures, and didn't seed
-- them with any inventory. Result: their salem-vendor LLM saw they
-- were hungry, found Josiah listed as a merchant, and literally
-- walked into his house asking for food. Plus produce_tick gates on
-- inside_structure_id == work_structure_id, which never matches
-- because they were standing at home.
--
-- This migration:
--   1. Snaps each producer to their stall coordinates so they're
--      already at work the moment the migration applies. Sets
--      inside_structure_id so produce_tick gates them in.
--   2. Seeds starter inventory: a stash of their own products plus
--      a little bread/water/etc so they have food to consume from
--      their own pouch when hungry instead of wandering off.
--
-- The LLM-side discipline is a separate, slower fix (stronger
-- "stay at your stall" instructions, or a behavior_handler that
-- walks them back if they leave). For v1 the seeded inventory is
-- enough to keep them anchored — they consume from their own stock
-- and produce_tick replenishes it.

BEGIN;

-- 1. Snap Elizabeth to Ellis Farm.
UPDATE actor
   SET current_x = -550.99,
       current_y = -2205.76,
       inside_structure_id = '019e138d-724b-75d8-9374-9d931ebc93cd'::uuid,  -- Ellis Farm
       inside = TRUE,
       facing = 'south'
 WHERE display_name = 'Elizabeth Ellis';

-- 2. Snap Moses to James Farm.
UPDATE actor
   SET current_x = -940.86,
       current_y = -1836.72,
       inside_structure_id = '019e1390-0639-7bf6-8b66-08f95414079c'::uuid,  -- James Farm
       inside = TRUE,
       facing = 'south'
 WHERE display_name = 'Moses James';

-- 3. Seed Elizabeth's inventory.
--    She needs her own products (so produce_tick has a starting
--    state to add to, and so customers can buy immediately) plus
--    food to consume herself when hungry.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT a.id, item, qty FROM actor a, (VALUES
    ('cheese', 5),
    ('milk',   3),
    ('meat',   2),
    ('bread',  3),
    ('water', 10)
) AS seed(item, qty)
WHERE a.display_name = 'Elizabeth Ellis'
ON CONFLICT (actor_id, item_kind) DO UPDATE
    SET quantity = GREATEST(actor_inventory.quantity, EXCLUDED.quantity);

-- 4. Seed Moses's inventory.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT a.id, item, qty FROM actor a, (VALUES
    ('carrots', 8),
    ('bread',   3),
    ('water',  10),
    ('cheese',  2)
) AS seed(item, qty)
WHERE a.display_name = 'Moses James'
ON CONFLICT (actor_id, item_kind) DO UPDATE
    SET quantity = GREATEST(actor_inventory.quantity, EXCLUDED.quantity);

-- 5. Lock all private residences to entry_policy='owner'. Today
--    almost every residence is set to 'anyone', meaning ANY actor
--    can walk in. That's how Elizabeth + Moses literally walked
--    into Josiah's house when their LLM got hungry and picked
--    Thorne Residence as a target.
--
--    entry_policy='owner' is enforced via home_structure_id /
--    work_structure_id match (agent_tick.go::isAgentMoveOwner),
--    so residents continue to enter their own homes.
--    Non-residents stand at the loiter slot at the door instead.
--
--    Ward Residence is already set correctly (kept as-is).
UPDATE village_object
   SET entry_policy = 'owner'
 WHERE display_name LIKE '%Residence%'
   AND entry_policy = 'anyone';

COMMIT;
