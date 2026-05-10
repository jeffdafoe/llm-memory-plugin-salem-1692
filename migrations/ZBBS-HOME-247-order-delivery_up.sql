-- ZBBS-HOME-247 — Order/delivery model + tiered pricing.
--
-- Replaces the v1 fast-food buy_walker with a real merchant-economy
-- flow:
--   * Buyer walks to seller's stall.
--   * Has stock → transfer at retail price (seller's tier) + dialogue.
--   * Empty → seller takes an ORDER (pay_ledger row,
--     fulfillment_status='pending'). Buyer walks home empty.
--   * Seller's restock walker (existing) fetches goods from upstream.
--   * NEW: fulfill_orders walker dispatches the seller to the buyer's
--     work_structure to deliver pending orders (deliver_order semantic
--     — goods + retail price exchange at the door).
--
-- Pricing tiers: each item_recipe has wholesale_price (charged by
-- the producer to a merchant buying upstream) + retail_price (charged
-- by a merchant to the customer downstream). Engine picks the tier
-- based on the SELLER's role: producer → wholesale; merchant → retail.
-- This is what gives Josiah a profit margin.
--
-- Plus carryforward fixes from the closed PR #181:
--   * One-shot footprint-based restore for keepers stuck outside their
--     work_structure post-HOME-244.
--   * (engine-side) timezone fix for produce_tick — separate code
--     change.

BEGIN;

-- 1. Tiered pricing on item_recipe.
ALTER TABLE item_recipe
    ADD COLUMN wholesale_price SMALLINT,
    ADD COLUMN retail_price    SMALLINT;

-- Seed prices. Pattern: retail = wholesale × 2 (or close to). Tunable
-- per item later. Same items as the v1 hardcoded buyDeterministicPrice
-- map but split into tiers.
UPDATE item_recipe SET wholesale_price = 2, retail_price = 4 WHERE output_item = 'cheese';
UPDATE item_recipe SET wholesale_price = 1, retail_price = 2 WHERE output_item = 'milk';
UPDATE item_recipe SET wholesale_price = 2, retail_price = 4 WHERE output_item = 'meat';
UPDATE item_recipe SET wholesale_price = 1, retail_price = 1 WHERE output_item = 'carrots';
UPDATE item_recipe SET wholesale_price = 1, retail_price = 2 WHERE output_item = 'bread';
UPDATE item_recipe SET wholesale_price = 1, retail_price = 1 WHERE output_item = 'ale';
UPDATE item_recipe SET wholesale_price = 1, retail_price = 1 WHERE output_item = 'water';
UPDATE item_recipe SET wholesale_price = 1, retail_price = 1 WHERE output_item = 'berries';
UPDATE item_recipe SET wholesale_price = 1, retail_price = 2 WHERE output_item = 'coca_tea';
UPDATE item_recipe SET wholesale_price = 3, retail_price = 5 WHERE output_item = 'stew';

-- 2. Delivery trip state table. Mirror of actor_restock_in_progress
--    but for the seller-walks-to-buyer direction. Tracks pending
--    deliveries so a crash mid-trip doesn't strand the seller.
CREATE TABLE actor_delivery_in_progress (
    actor_id            UUID PRIMARY KEY REFERENCES actor(id) ON DELETE CASCADE,  -- the seller doing the delivery
    customer_id         UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    item_kind           VARCHAR(32) NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE,
    qty                 SMALLINT NOT NULL CHECK (qty > 0),
    pay_ledger_id       BIGINT NOT NULL REFERENCES pay_ledger(id) ON DELETE CASCADE,
    customer_structure_id UUID NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    home_x              DOUBLE PRECISION NOT NULL,
    home_y              DOUBLE PRECISION NOT NULL,
    phase               VARCHAR(16) NOT NULL CHECK (phase IN ('outbound','inbound')),
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_actor_delivery_in_progress_customer_structure
    ON actor_delivery_in_progress (customer_structure_id);

-- 3. One-shot restore for keepers stuck outside post-HOME-244.
--    Footprint-based filter: actor must be physically within their
--    work_structure asset's footprint to be restored. Avoids the
--    bad case where a keeper at the visitor loiter slot OUTSIDE
--    the building gets flipped to inside.
UPDATE actor a
   SET inside_structure_id = a.work_structure_id,
       inside = TRUE
  FROM village_object vo
  JOIN asset s ON s.id = vo.asset_id
 WHERE a.work_structure_id IS NOT NULL
   AND a.inside_structure_id IS NULL
   AND vo.id = a.work_structure_id
   AND a.current_x BETWEEN vo.x - s.footprint_left * 32 AND vo.x + s.footprint_right * 32
   AND a.current_y BETWEEN vo.y - s.footprint_top  * 32 AND vo.y + s.footprint_bottom * 32;

COMMIT;
