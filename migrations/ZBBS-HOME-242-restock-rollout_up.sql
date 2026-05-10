-- ZBBS-HOME-242 — Restock rollout (Phase 1b-1).
--
-- Promotes two existing decorative actors into shared-VA producer
-- NPCs (Elizabeth Ellis at Ellis Farm, Moses James at James Farm),
-- adds restock policies on John/Josiah/Elizabeth/Moses so the
-- ZBBS-HOME-241 produce + buy mechanisms have something to act on,
-- normalizes farm entry_policy, appends supply-chain discipline copy
-- to the remaining keeper attributes, and cleans up a duplicate
-- residence + its placeholder occupants.
--
-- This is the data half of Phase 1b. The buy walk dispatcher (the
-- engine code that ACTS on `buy` restock entries — walk to seller,
-- haggle, walk home) lands separately as 1b-2. With this migration
-- alone:
--
--   * John Ellis auto-replenishes water / ale / bread (terminator
--     produce, no inputs) — fixes the recurring "John ran out"
--     problem for those three items immediately.
--   * Elizabeth Ellis produces cheese / milk / meat at her stall
--     during 6am-7pm; Moses James produces carrots. Their inventories
--     accumulate to max while no one buys from them.
--   * John's stew (transformation) does NOT yet produce — needs
--     meat/water/milk/carrots. Water comes from John's own produce;
--     the others need the buy chain to deliver, which waits on 1b-2.
--   * Josiah's cheese/meat/milk/carrots `buy` entries exist but no
--     executor walks him to Elizabeth/Moses yet. Same wait on 1b-2.
--
-- Decisions resolved 2026-05-10 in conversation:
--   * Sprites: keep existing (no visual change to Elizabeth/Moses).
--   * Active hours: 6am-7pm for the producers (daylight only).
--   * actor.role: leave NULL (matches Hannah, attribute system is
--     the source of truth going forward).
--   * Producer NPCs: existing actors promoted (no new actors created).
--   * Property surnames match producer surnames (Elizabeth Ellis at
--     Ellis Farm; the James Family at James Farm).
--   * Both farms become entry_policy='owner' (matches General Store
--     / Blacksmith / PW Apothecary stall convention).

BEGIN;

-- 1. New role attributes for the two producer trades.

INSERT INTO attribute_definition (slug, display_name, description, instructions) VALUES (
    'farmer',
    'Farmer',
    'Smallholder who works a vegetable patch or grain field at the village edge. Sells the harvest at a stall.',
    E'You are a farmer working a small plot at the edge of the village. You grow what the village needs — root crops, grain when in season — and sell what you have at your stall.\n\nTOOL DISCIPLINE (sales-and-gifts model):\n- You can only offer or sell items in your inventory list (the "Items you can sell" line in your perception). Never claim to have or invent goods that aren''t in that list. If a customer asks for something not in stock, say plainly that you don''t carry it.\n- Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). After every accepted pay, deliver immediately via deliver_order if you can.\n- Prices and coin amounts are whole numbers only. Never quote or accept fractional prices.\n- When you reference goods in speech, populate speak `mentions` with those item kinds (lowercase). When you state a per-unit price, set speak.price.\n- Use serve ONLY for free gifts; you MUST set gift=true.\n- When you eat from your own stock, ALWAYS call consume.\n\nYou are a farmer, not a talker. Greet customers civilly but don''t fill the air with chatter — your hands are usually full of work, and you save your words for what needs saying.\n\nYour stock is replenished by your work and (later) by trips to your suppliers. The engine handles these replenishments and tells you when they happen via your perception. Do not narrate purchases, deliveries, or supply-chain events that the engine has not told you about. If asked where an item came from, you may reference your most recent successful buy (when the engine surfaces it) or "from my own work" for things you produce. Do not invent suppliers, prices, or transactions.'
);

INSERT INTO attribute_definition (slug, display_name, description, instructions) VALUES (
    'dairykeeper',
    'Dairykeeper',
    'Goodwife who keeps a cow and small herd, churns butter, and ages cheese. Sells dairy and the occasional cut of meat at her stall.',
    E'You are a dairykeeper at the edge of the village. You milk the cow morning and evening, churn butter, age cheese, and butcher the occasional pig or fowl when needed. Your stall sells what comes from the work — milk, cheese, meat.\n\nTOOL DISCIPLINE (sales-and-gifts model):\n- You can only offer or sell items in your inventory list (the "Items you can sell" line in your perception). Never claim to have or invent goods that aren''t in that list. If a customer asks for something not in stock, say plainly that you don''t carry it.\n- Sales are initiated by the CUSTOMER via pay(), NOT by you serving them. Quote a price in speak() and let THEM call pay(). After every accepted pay, deliver immediately via deliver_order if you can.\n- Prices and coin amounts are whole numbers only. Never quote or accept fractional prices.\n- When you reference goods in speech, populate speak `mentions` with those item kinds (lowercase). When you state a per-unit price, set speak.price.\n- Use serve ONLY for free gifts; you MUST set gift=true.\n- When you eat from your own stock, ALWAYS call consume.\n\nYou take pride in the quality of your dairy. Customers who treat your cheese as ordinary get a frostier reception than those who notice the difference. You''re not flowery, but you''re not cold either — friendly to regulars, professional to strangers.\n\nYour stock is replenished by your work and (later) by trips to your suppliers. The engine handles these replenishments and tells you when they happen via your perception. Do not narrate purchases, deliveries, or supply-chain events that the engine has not told you about. If asked where an item came from, you may reference your most recent successful buy (when the engine surfaces it) or "from my own work" for things you produce. Do not invent suppliers, prices, or transactions.'
);

-- 2. Append the supply-chain discipline copy to the remaining keeper
--    roles that didn't get it in 1a. Same exact paragraph for parity.

UPDATE attribute_definition
   SET instructions = instructions ||
       E'\n\nYour stock is replenished by your work and (later) by trips to your suppliers. The engine handles these replenishments and tells you when they happen via your perception. Do not narrate purchases, deliveries, or supply-chain events that the engine has not told you about. If asked where an item came from, you may reference your most recent successful buy (when the engine surfaces it) or "from my own work" for things you produce. Do not invent suppliers, prices, or transactions.',
       updated_at = now()
 WHERE slug IN ('blacksmith', 'herbalist')
   AND instructions NOT LIKE '%Do not invent suppliers%';

-- merchant already has its own (more detailed) tool discipline block
-- — append the no-fabrication paragraph but only if not already there.
UPDATE attribute_definition
   SET instructions = instructions ||
       E'\n\nYour stock is replenished by your work and (later) by trips to your suppliers. The engine handles these replenishments and tells you when they happen via your perception. Do not narrate purchases, deliveries, or supply-chain events that the engine has not told you about. If asked where an item came from, you may reference your most recent successful buy (when the engine surfaces it) or "from my own work" for things you produce. Do not invent suppliers, prices, or transactions.',
       updated_at = now()
 WHERE slug = 'merchant'
   AND instructions NOT LIKE '%Do not invent suppliers%';

-- 3. Promote Elizabeth Ellis to dairykeeper.
--
-- Sprites and home_structure_id stay as they are. We set:
--   * llm_memory_agent='salem-vendor' (shared multiplexed VA, matches Hannah)
--   * work_structure_id = Ellis Farm
--   * active_start_hour=6, active_end_hour=19 (daylight production)

UPDATE actor
   SET llm_memory_agent   = 'salem-vendor',
       work_structure_id  = '019e138d-724b-75d8-9374-9d931ebc93cd'::uuid,  -- Ellis Farm
       active_start_hour  = 6,
       active_end_hour    = 19,
       -- Required by actor_schedule_all_or_none CHECK: when active_*_hour
       -- are set, schedule_interval_hours must also be non-NULL. Producer
       -- NPCs don't use a scheduled behavior_handler (the produce_tick
       -- fires per-minute on its own gating), so the value is effectively
       -- inert here — set to 24 to convey "daily, no extra mid-day firing".
       schedule_interval_hours = 24
 WHERE display_name = 'Elizabeth Ellis';

INSERT INTO actor_attribute (actor_id, slug, params)
SELECT id, 'dairykeeper', jsonb_build_object('restock', jsonb_build_array(
    jsonb_build_object('item', 'cheese', 'source', 'produce', 'max', 50),
    jsonb_build_object('item', 'milk',   'source', 'produce', 'max', 30),
    jsonb_build_object('item', 'meat',   'source', 'produce', 'max', 20)
))
  FROM actor WHERE display_name = 'Elizabeth Ellis'
ON CONFLICT (actor_id, slug) DO UPDATE SET params = EXCLUDED.params;

-- Per-actor character via narrative state.
INSERT INTO actor_narrative_state (actor_id, seed_text)
SELECT id, E'Elizabeth Ellis, dairykeeper at the Ellis family stall on the village edge. Mid-30s, the household''s eldest daughter who never married — kept the farm running while her brothers went off. Practical, plain-spoken, exact about weights and prices. Has opinions about who keeps a clean dairy and who doesn''t and isn''t shy about expressing them when asked. Loyal to regulars; reserved with strangers until they''ve made a few honest trades.'
  FROM actor WHERE display_name = 'Elizabeth Ellis'
ON CONFLICT (actor_id) DO UPDATE SET seed_text = EXCLUDED.seed_text;

-- 4. Promote Moses James to farmer.
UPDATE actor
   SET llm_memory_agent   = 'salem-vendor',
       work_structure_id  = '019e1390-0639-7bf6-8b66-08f95414079c'::uuid,  -- James Farm
       active_start_hour  = 6,
       active_end_hour    = 19,
       -- Required by actor_schedule_all_or_none CHECK: when active_*_hour
       -- are set, schedule_interval_hours must also be non-NULL. Producer
       -- NPCs don't use a scheduled behavior_handler (the produce_tick
       -- fires per-minute on its own gating), so the value is effectively
       -- inert here — set to 24 to convey "daily, no extra mid-day firing".
       schedule_interval_hours = 24
 WHERE display_name = 'Moses James';

INSERT INTO actor_attribute (actor_id, slug, params)
SELECT id, 'farmer', jsonb_build_object('restock', jsonb_build_array(
    jsonb_build_object('item', 'carrots', 'source', 'produce', 'max', 30)
))
  FROM actor WHERE display_name = 'Moses James'
ON CONFLICT (actor_id, slug) DO UPDATE SET params = EXCLUDED.params;

INSERT INTO actor_narrative_state (actor_id, seed_text)
SELECT id, E'Moses James, smallholder farmer at the James family stall on the village edge. Late 40s, weathered, hands ingrained with soil. Husband to Hope James; the household has been on this land three generations. Taciturn — answers what''s asked, doesn''t volunteer. Cares about his crops, his fences, and the weather. Quotes prices with a flat tone and won''t haggle far below them — if you don''t want to pay, walk on.'
  FROM actor WHERE display_name = 'Moses James'
ON CONFLICT (actor_id) DO UPDATE SET seed_text = EXCLUDED.seed_text;

-- 5. Restock policy on John Ellis (tavernkeeper). Stew is a
--    transformation (will not produce until meat/milk/carrots arrive
--    via buy chain — waits on 1b-2). Water/ale/bread are terminator
--    produce — fire immediately, fix the "John ran out" symptom for
--    those three items the next produce_tick after deploy.
UPDATE actor_attribute
   SET params = jsonb_build_object('restock', jsonb_build_array(
       jsonb_build_object('item', 'stew',    'source', 'produce', 'max', 10),
       jsonb_build_object('item', 'water',   'source', 'produce', 'max', 25),
       jsonb_build_object('item', 'ale',     'source', 'produce', 'max', 20),
       jsonb_build_object('item', 'bread',   'source', 'produce', 'max', 15),
       jsonb_build_object('item', 'cheese',  'source', 'buy',     'target', 8),
       jsonb_build_object('item', 'meat',    'source', 'buy',     'target', 5),
       jsonb_build_object('item', 'milk',    'source', 'buy',     'target', 5),
       jsonb_build_object('item', 'carrots', 'source', 'buy',     'target', 5)
   ))
 WHERE slug = 'tavernkeeper'
   AND actor_id = (SELECT id FROM actor WHERE display_name = 'John Ellis');

-- 6. Restock policy on Josiah Thorne (merchant). Pure buy chain;
--    sources from Elizabeth (cheese/milk/meat) and Moses (carrots).
--    Bread/flour/wheat not yet wired — when a baker NPC ships, those
--    join here.
UPDATE actor_attribute
   SET params = jsonb_build_object('restock', jsonb_build_array(
       jsonb_build_object('item', 'cheese',  'source', 'buy', 'target', 30),
       jsonb_build_object('item', 'milk',    'source', 'buy', 'target', 20),
       jsonb_build_object('item', 'meat',    'source', 'buy', 'target', 15),
       jsonb_build_object('item', 'carrots', 'source', 'buy', 'target', 20)
   ))
 WHERE slug = 'merchant'
   AND actor_id = (SELECT id FROM actor WHERE display_name = 'Josiah Thorne');

-- 7. Both farm stalls follow the entry_policy='owner' convention
--    used by General Store / Blacksmith / PW Apothecary. Customers
--    approach from outside; only the keeper enters / stands at the
--    stall. Ellis Farm is currently 'none', James Farm is 'anyone' —
--    flip both to 'owner' for stall consistency.
UPDATE village_object
   SET entry_policy = 'owner'
 WHERE id IN (
    '019e138d-724b-75d8-9374-9d931ebc93cd'::uuid,  -- Ellis Farm
    '019e1390-0639-7bf6-8b66-08f95414079c'::uuid   -- James Farm
 );

-- 8. Cleanup: remove the duplicate James Residence and its two
--    placeholder occupants (Abigail James + Benjamin James — neither
--    has llm_memory_agent or role; pure decorative).
--
--    Order matters: delete actor rows first (CASCADE clears their
--    inventory + attribute assignments + action_log), then the
--    village_object once nothing else references it.

DELETE FROM actor WHERE id IN (
    '019dcaf3-bc1d-740b-9bbc-b4c7d31c1a8a'::uuid,  -- Abigail James
    '019dcaf4-abc7-74bf-bcfd-7693e1980dd3'::uuid   -- Benjamin James
);

-- The duplicate "James Residence" (Tiny). Will fail if anything
-- still references it — that surfaces an unexpected dependency
-- rather than silently orphaning.
DELETE FROM village_object
 WHERE id = '019d9dd4-6540-7a73-a6f5-8673ca905394'::uuid;

COMMIT;
