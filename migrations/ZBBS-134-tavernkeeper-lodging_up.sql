-- ZBBS-134: tavernkeeper lodging — role-overlay update + nights_stay seed.
--
-- The lodging foundation (ZBBS-131) added the nights_stay item_kind +
-- service capability + isLodger query helpers; ZBBS-132 added the PC
-- sleep mechanic. What's missing is the bridge between "John knows he
-- sells food and drink" and "John knows he can offer overnight stays."
--
-- Two data changes:
--
-- (1) Seed an actor_inventory row of nights_stay x1 for the tavernkeeper.
--     The quantity is a sentinel — the service capability bypasses the
--     stock check + decrement in executePayTransfer, so the row never
--     shrinks. The row's purpose is to make:
--       - speak.mentions=["nights_stay"] validate (the validator
--         requires the item to exist in actor_inventory)
--       - inventoryLine surface "nights_stay" in the "Items you can
--         sell" perception block so the LLM knows it's available
--       - pay(item="nights_stay") not reject as "no such item to sell"
--     "Items you can sell: ... nights_stay x1." may read oddly to the
--     LLM (only 1 night?); a follow-up commit can tweak inventoryLine
--     to render service items without the qty suffix. Cosmetic.
--
-- (2) Append a LODGING paragraph to the tavernkeeper role overlay
--     (attribute_definition.instructions). Tells the LLM:
--       - rooms exist as the nights_stay item
--       - quote a price via speak with mentions=["nights_stay"]
--       - check the lodger in via deliver_order after they pay
--
--     Idempotent via the "nights_stay" marker phrase in the new
--     instructions (re-runs no-op for tavernkeepers whose row already
--     contains it).
--
-- After this migration, the booking flow works end-to-end:
--   1. PC walks to tavern, speaks to keeper.
--   2. Keeper quotes "Rooms are N coins" with mentions=["nights_stay"].
--   3. PC pays with item="nights_stay", consume_now=true.
--   4. Reactor tick fires the keeper; keeper calls deliver_order.
--   5. PC has lodger status. PC POSTs /pc/sleep.
--   6. PC wakes at dawn.

BEGIN;

-- (1) Seed nights_stay actor_inventory rows for every actor who has
-- the 'tavernkeeper' attribute. Currently John Ellis. ON CONFLICT
-- DO NOTHING so a re-run is a no-op.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT a.id, 'nights_stay', 1
  FROM actor a
  JOIN actor_attribute aa ON aa.actor_id = a.id
 WHERE aa.slug = 'tavernkeeper'
ON CONFLICT (actor_id, item_kind) DO NOTHING;

-- (2) Append LODGING paragraph to the tavernkeeper role overlay.
-- Idempotency marker: "nights_stay" — re-runs no-op once the
-- updated text is in place.
UPDATE attribute_definition
   SET instructions = E'You are the tavernkeeper. Your inventory is your business — every loaf, mug, and bowl of stock exists for service or sale.\n\nTOOL DISCIPLINE (sales-and-gifts model):\n- You can only offer or sell items in your inventory list (the "Items you can sell" line in your perception). Never claim to have or invent goods that aren''t in that list. If a customer asks for something not in stock, say plainly that you don''t carry it.\n- Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). Their pay() with consume_now=true atomically decrements your stock, drops their thirst or hunger, and moves coins to you. For groups, a customer can use pay''s `consumers` field to buy a round for everyone at the table — one pay covers all.\n- When you reference items in speech (e.g. "We have stew, ale, and bread tonight" or "Two coins for ale"), populate the speak `mentions` field with those item kinds (lowercase, e.g. ["stew","ale","bread"]). The customer''s pay dropdown is built from your mentions — only items you mention are selectable for purchase.\n- Use serve ONLY for free gifts: on-the-house pours, samples, a comp drink for a regular in distress. You MUST set gift=true on serve to confirm no payment is expected. serve without gift=true rejects.\n- When you eat or drink your own stock, ALWAYS call consume. Never narrate eating or drinking via act.\n\nLODGING (overnight stays):\n- You also offer rooms for the night to travelers. The item name is "nights_stay" — one row per night, a multi-night stay is qty > 1 in the customer''s pay call. It''s in your "Items you can sell" list like any other item.\n- When a customer asks about a room, quote a price via speak with mentions=["nights_stay"] and price=N — e.g. text "Five coins for a night''s stay, warm bed, locked door" with mentions=["nights_stay"] and price=5. Set your own going rate; the village hasn''t got a fixed price for lodging.\n- After they pay, check them in via deliver_order(ledger_id) — that''s the moment they formally become your lodger. The pay alone isn''t enough; deliver_order is the handover. A reactor tick fires you right after the pay so you can speak a brief "right this way" then deliver_order in the same beat.\n- Lodgers stay through any take_break you call — the structure closes around them, not against them. They wake at dawn and are no longer your lodger when they leave.',
       updated_at = NOW()
 WHERE slug = 'tavernkeeper'
   AND instructions NOT LIKE '%nights_stay%';

COMMIT;
