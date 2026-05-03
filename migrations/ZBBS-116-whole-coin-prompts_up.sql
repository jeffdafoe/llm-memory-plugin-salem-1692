-- ZBBS-116: vendor role prompts gain a whole-coin rule.
--
-- Background: vendor LLMs were observed bartering with fractional
-- prices ("I can do 1.5 coins for the hammer"). The engine's pay
-- dispatcher rejects fractional amounts ("amount must be a whole
-- number of coins"), so any customer trying to settle at the
-- bartered fractional price would hit the rejection. Same family
-- as the speak-fires-before-tool-rejects pattern: the LLM doesn't
-- know coins are integer-only.
--
-- The pay tool description (in agentToolSpec, code-side) gained
-- "WHOLE NUMBER ONLY" on the amount parameter in the same change.
-- This migration adds the parallel rule to the four vendor role
-- prompts so vendors don't quote fractional prices in speech in
-- the first place.
--
-- Idempotent via marker phrase "Prices and coin amounts are whole
-- numbers only".

BEGIN;

UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'TOOL DISCIPLINE (sales-and-gifts model):\n',
           E'TOOL DISCIPLINE (sales-and-gifts model):\n- Prices and coin amounts are whole numbers only. If a customer counters with a fractional price (e.g. "1.5 coins"), round to the nearest whole and re-quote ("1 coin" or "2 coins"). Never quote or accept fractional prices yourself — the pay tool rejects them and the customer would be stuck.\n'
       ),
       updated_at = NOW()
 WHERE slug IN ('blacksmith', 'herbalist', 'merchant', 'tavernkeeper')
   AND instructions LIKE E'%TOOL DISCIPLINE (sales-and-gifts model):\n%'
   AND instructions NOT LIKE '%Prices and coin amounts are whole numbers only%';

COMMIT;
