-- ZBBS-135: vendor TOOL DISCIPLINE — describe deliver_order handover.
--
-- ZBBS-129 step 2 split pay-accept from delivery: pay() now reserves
-- goods on a pay_ledger row and moves coins, but the goods don't
-- actually transfer until the seller calls deliver_order(ledger_id).
-- The four vendor role overlays (tavernkeeper, merchant, herbalist,
-- blacksmith) were not updated to match — they still describe the
-- old atomic-pay behavior ("pay decrements your stock and drops their
-- thirst or hunger"). Result: vendors don't realize they need to
-- deliver, so accepted pay rows pile up at fulfillment_status='ready'
-- and the buyer's needs never get eased.
--
-- This migration rewrites the offending sentence in each of the four
-- role overlays to describe the deliver_order handover. The
-- tavernkeeper's LODGING block already uses the right language; the
-- non-lodging TOOL DISCIPLINE block needs the same treatment.
--
-- Idempotency: each UPDATE matches on the OLD text (the pre-ZBBS-129
-- atomic-pay description). Re-running this migration on a row whose
-- TOOL DISCIPLINE has already been rewritten is a no-op because the
-- WHERE clause's LIKE pattern won't match.
--
-- Companion engine change (ZBBS-136) adds a "Customers awaiting
-- delivery" section to vendor perception so the LLM sees pending
-- ledger rows directly, not just a description of the mechanic.
-- A+B together; A alone may not be enough if the LLM is anchored
-- on its own needs.

BEGIN;

-- Tavernkeeper. The LODGING paragraph (ZBBS-134) is preserved
-- verbatim; only the TOOL DISCIPLINE bullet about pay() semantics
-- is rewritten.
UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() with consume_now=true atomically decrements your stock, drops their thirst or hunger, and moves coins to you. For groups, a customer can use pay''s `consumers` field to buy a round for everyone at the table — one pay covers all.',
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() reserves the goods on a pay_ledger row and moves coins to you, but the goods don''t transfer until you call deliver_order(ledger_id) — that''s the handover moment when they actually consume what they bought. After every accepted pay, deliver immediately if you can — keep the customer flow moving. A reactor tick fires you right after the pay so you can speak a brief acknowledgment ("here you are") then deliver_order in the same beat. For groups, a customer can use pay''s `consumers` field to buy a round for everyone at the table — one pay, one deliver_order covers all.'
       ),
       updated_at = NOW()
 WHERE slug = 'tavernkeeper'
   AND instructions LIKE '%atomically decrements your stock, drops their thirst or hunger%';

-- Merchant. Take-home (consume_now=false) is the common case.
UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() with consume_now=false atomically decrements your stock, transfers the goods to their inventory, and moves coins to you. DO NOT call serve to fulfill a sale.',
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() reserves the goods on a pay_ledger row and moves coins to you, but the goods don''t transfer until you call deliver_order(ledger_id) — that''s the handover moment when the items land in their inventory (consume_now=false) or they consume on the spot (consume_now=true). After every accepted pay, deliver immediately if you can — keep the customer flow moving. A reactor tick fires you right after the pay so you can speak a brief acknowledgment ("here you are") then deliver_order in the same beat. DO NOT call serve to fulfill a sale.'
       ),
       updated_at = NOW()
 WHERE slug = 'merchant'
   AND instructions LIKE '%atomically decrements your stock, transfers the goods to their inventory%';

-- Herbalist. Mixed take-home / at-source flow.
UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). For take-home (most herbalist purchases — they''ll brew the herbs at home, drink the tonic by their bedside), they use consume_now=false and the goods land in their inventory. For at-source (a sip of cordial right at your shop, water for someone collapsing inside), they use consume_now=true and their need drops on the spot. DO NOT call serve to fulfill a sale.',
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() reserves the goods on a pay_ledger row and moves coins to you, but the goods don''t transfer until you call deliver_order(ledger_id) — that''s the handover moment. For take-home (most herbalist purchases — consume_now=false), deliver_order adds the goods to their inventory. For at-source (a sip of cordial right at your shop, consume_now=true), deliver_order is when their need actually drops. After every accepted pay, deliver immediately if you can — keep the customer flow moving. A reactor tick fires you right after the pay so you can speak a brief acknowledgment ("here you are") then deliver_order in the same beat. DO NOT call serve to fulfill a sale.'
       ),
       updated_at = NOW()
 WHERE slug = 'herbalist'
   AND instructions LIKE '%goods land in their inventory. For at-source%';

-- Blacksmith. Take-home (tools) and at-source (rare) — note the
-- existing role text mixes both into one sentence.
UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. When a customer wants to buy, quote a price in speak() and let THEM call pay(). Their pay() does the whole transaction atomically — your stock decrements, the item lands in their inventory (or their need drops, for at-source consumption), and coins move to you. DO NOT call serve to fulfill a sale.',
           E'Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. When a customer wants to buy, quote a price in speak() and let THEM call pay(). Their pay() reserves the goods on a pay_ledger row and moves coins to you, but the goods don''t transfer until you call deliver_order(ledger_id) — that''s the handover moment when the item lands in their inventory (or their need drops, for at-source consumption). After every accepted pay, deliver immediately if you can — keep the customer flow moving. A reactor tick fires you right after the pay so you can speak a brief acknowledgment ("here you are") then deliver_order in the same beat. DO NOT call serve to fulfill a sale.'
       ),
       updated_at = NOW()
 WHERE slug = 'blacksmith'
   AND instructions LIKE '%does the whole transaction atomically — your stock decrements%';

COMMIT;
