-- ZBBS-114: tool item kinds + blacksmith starter inventory.
--
-- Adds four blacksmith goods (hammer, axe, horseshoe, nail) to item_kind
-- so Ezekiel Crane has actual schema-backed wares to sell. Phase A of the
-- sales/transaction redesign documented in
-- shared/notes/codebase/salem/sales-and-gifts.
--
-- Background: Ezekiel's role description is "blacksmith" but item_kind
-- previously contained only food/drink/raw materials — nothing tool-
-- shaped. The vendor LLM extrapolated from the role concept and offered
-- hammers and horseshoes that didn't exist, then his serve() calls were
-- rejected with "no such item." The fix here is the minimal one:
-- introduce tool items as portable goods with no satisfies_attribute
-- (you can't eat a hammer), seed Ezekiel with starting stock, and let
-- the existing pay/serve flows operate on them.
--
-- Tools as items here is a stopgap, not the full Phase 3 design from
-- inventory-and-trade. No production, no durability, no per-instance
-- state — a hammer is a stackable portable. Phase 3 (recipes + tool
-- durability) remains deferred.
--
-- New category 'tool' is introduced. No engine code switches on
-- item_kind.category today (verified via grep), so it's a free string.
--
-- Idempotent: each insert guards on NOT EXISTS so a re-run is safe.

BEGIN;

INSERT INTO item_kind (name, display_label, category, satisfies_attribute, satisfies_amount, sort_order, capabilities)
SELECT 'hammer', 'Hammer', 'tool', NULL, NULL, 310, ARRAY['portable']::varchar[]
 WHERE NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'hammer');

INSERT INTO item_kind (name, display_label, category, satisfies_attribute, satisfies_amount, sort_order, capabilities)
SELECT 'axe', 'Axe', 'tool', NULL, NULL, 320, ARRAY['portable']::varchar[]
 WHERE NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'axe');

INSERT INTO item_kind (name, display_label, category, satisfies_attribute, satisfies_amount, sort_order, capabilities)
SELECT 'horseshoe', 'Horseshoe', 'tool', NULL, NULL, 330, ARRAY['portable']::varchar[]
 WHERE NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'horseshoe');

INSERT INTO item_kind (name, display_label, category, satisfies_attribute, satisfies_amount, sort_order, capabilities)
SELECT 'nail', 'Nail', 'tool', NULL, NULL, 340, ARRAY['portable']::varchar[]
 WHERE NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'nail');

-- Seed Ezekiel Crane (the blacksmith) with starter stock so the new
-- item kinds have something to sell. Lookup by display_name; idempotent
-- on (actor_id, item_kind) primary key.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT a.id, s.kind, s.qty
  FROM actor a,
       (VALUES ('hammer'::varchar, 3::smallint),
               ('axe',             2::smallint),
               ('horseshoe',       8::smallint),
               ('nail',           40::smallint)) AS s(kind, qty)
 WHERE a.display_name = 'Ezekiel Crane'
   AND NOT EXISTS (
       SELECT 1 FROM actor_inventory ai
        WHERE ai.actor_id = a.id AND ai.item_kind = s.kind
   );

-- Vendor-role grounding rule. Each vendor role's TOOL DISCIPLINE
-- section gets a new top bullet pinning offers to actual stock. This
-- pairs with the agent_tick perception change that relabels vendors'
-- inventory line as "Items you can sell:" — the rule references the
-- relabeled perception line so the LLM has both data and constraint
-- in adjacent context.
--
-- Idempotent via a NOT LIKE guard on the marker phrase. Re-running the
-- migration after the rule is in place is a no-op for these rows.
UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'TOOL DISCIPLINE:\n',
           E'TOOL DISCIPLINE:\n- You can only offer or sell items in your inventory list (the "Items you can sell" line in your perception). Never claim to have or invent goods that aren''t in that list. If a customer asks for something not in stock, say plainly that you don''t carry it.\n'
       ),
       updated_at = NOW()
 WHERE slug IN ('blacksmith', 'herbalist', 'merchant', 'tavernkeeper')
   AND instructions LIKE E'%TOOL DISCIPLINE:\n%'
   AND instructions NOT LIKE '%You can only offer or sell items in your inventory list%';

COMMIT;
