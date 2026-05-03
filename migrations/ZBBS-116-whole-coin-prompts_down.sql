-- ZBBS-116 down: strip the whole-coin rule from vendor prompts.

BEGIN;

UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'- Prices and coin amounts are whole numbers only. If a customer counters with a fractional price (e.g. "1.5 coins"), round to the nearest whole and re-quote ("1 coin" or "2 coins"). Never quote or accept fractional prices yourself — the pay tool rejects them and the customer would be stuck.\n',
           ''
       ),
       updated_at = NOW()
 WHERE slug IN ('blacksmith', 'herbalist', 'merchant', 'tavernkeeper');

COMMIT;
