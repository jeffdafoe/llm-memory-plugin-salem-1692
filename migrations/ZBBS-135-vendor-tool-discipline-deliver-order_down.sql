-- ZBBS-135 down: revert TOOL DISCIPLINE rewrites to pre-ZBBS-129
-- atomic-pay language. Idempotent: each REPLACE runs only when the
-- new "deliver_order" handover language is present.

BEGIN;

UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() reserves the goods on a pay_ledger row and moves coins to you, but the goods don''t transfer until you call deliver_order(ledger_id) — that''s the handover moment when they actually consume what they bought. After every accepted pay, deliver immediately if you can — keep the customer flow moving. A reactor tick fires you right after the pay so you can speak a brief acknowledgment ("here you are") then deliver_order in the same beat. For groups, a customer can use pay''s `consumers` field to buy a round for everyone at the table — one pay, one deliver_order covers all.',
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() with consume_now=true atomically decrements your stock, drops their thirst or hunger, and moves coins to you. For groups, a customer can use pay''s `consumers` field to buy a round for everyone at the table — one pay covers all.'
       ),
       updated_at = NOW()
 WHERE slug = 'tavernkeeper'
   AND instructions LIKE '%deliver_order(ledger_id) — that''s the handover moment when they actually consume%';

UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() reserves the goods on a pay_ledger row and moves coins to you, but the goods don''t transfer until you call deliver_order(ledger_id) — that''s the handover moment when the items land in their inventory (consume_now=false) or they consume on the spot (consume_now=true). After every accepted pay, deliver immediately if you can — keep the customer flow moving. A reactor tick fires you right after the pay so you can speak a brief acknowledgment ("here you are") then deliver_order in the same beat. DO NOT call serve to fulfill a sale.',
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() with consume_now=false atomically decrements your stock, transfers the goods to their inventory, and moves coins to you. DO NOT call serve to fulfill a sale.'
       ),
       updated_at = NOW()
 WHERE slug = 'merchant'
   AND instructions LIKE '%deliver_order(ledger_id) — that''s the handover moment when the items land in their inventory%';

UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() reserves the goods on a pay_ledger row and moves coins to you, but the goods don''t transfer until you call deliver_order(ledger_id) — that''s the handover moment. For take-home (most herbalist purchases — consume_now=false), deliver_order adds the goods to their inventory. For at-source (a sip of cordial right at your shop, consume_now=true), deliver_order is when their need actually drops. After every accepted pay, deliver immediately if you can — keep the customer flow moving. A reactor tick fires you right after the pay so you can speak a brief acknowledgment ("here you are") then deliver_order in the same beat. DO NOT call serve to fulfill a sale.',
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). For take-home (most herbalist purchases — they''ll brew the herbs at home, drink the tonic by their bedside), they use consume_now=false and the goods land in their inventory. For at-source (a sip of cordial right at your shop, water for someone collapsing inside), they use consume_now=true and their need drops on the spot. DO NOT call serve to fulfill a sale.'
       ),
       updated_at = NOW()
 WHERE slug = 'herbalist'
   AND instructions LIKE '%For take-home (most herbalist purchases — consume_now=false), deliver_order%';

UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. When a customer wants to buy, quote a price in speak() and let THEM call pay(). Their pay() reserves the goods on a pay_ledger row and moves coins to you, but the goods don''t transfer until you call deliver_order(ledger_id) — that''s the handover moment when the item lands in their inventory (or their need drops, for at-source consumption). After every accepted pay, deliver immediately if you can — keep the customer flow moving. A reactor tick fires you right after the pay so you can speak a brief acknowledgment ("here you are") then deliver_order in the same beat. DO NOT call serve to fulfill a sale.',
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. When a customer wants to buy, quote a price in speak() and let THEM call pay(). Their pay() does the whole transaction atomically — your stock decrements, the item lands in their inventory (or their need drops, for at-source consumption), and coins move to you. DO NOT call serve to fulfill a sale.'
       ),
       updated_at = NOW()
 WHERE slug = 'blacksmith'
   AND instructions LIKE '%when the item lands in their inventory (or their need drops, for at-source consumption)%';

COMMIT;
