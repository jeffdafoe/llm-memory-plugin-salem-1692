-- ZBBS-HOME-241 — Recipes + restock policies foundation.
--
-- Origin: live observation 2026-05-10 — John Ellis ran out of stew
-- overnight (no production loop in the world) AND fabricated a cheese
-- supply chain ("I pay Josiah 4 coins for the cheese" — verified
-- false against pay_ledger, zero matching transactions). Both are
-- the same root gap: NPCs have no real way to refill stock, so
-- inventory monotonically depletes and the LLM invents narration the
-- engine doesn't back.
--
-- Design: shared/tasks/pending/zbbs-home-203-recipes-and-restock
-- (filed under the design-discussion ticket number 203; this
-- migration uses the next available sequential number 241).
--
-- This migration is the FOUNDATION layer (Phase 1a in the task note):
-- additive schema, zero behavior change. No actor opts in until a
-- follow-up migration seeds restock policies on actor_attribute.params.
-- Engine code is also additive — the produce tick scans an empty
-- actor_produce_state until a policy lands; the buy dispatcher only
-- runs for actors with restock policies.
--
-- The discipline-copy update on tavernkeeper.instructions IS active
-- immediately — it just makes the LLM stop narrating supply-chain
-- events that didn't happen. No mechanism dependency.
--
-- Decisions resolved in 2026-05-10 design discussion (recorded in the
-- task note): hybrid pay_ledger + game-state candidate lookup; stew is
-- a transformation with output_qty=10 (no fractional inputs); produce
-- gated to work_structure + active hours; Euclidean distance to
-- seller's work_structure walk-target; reuse take_break for buy
-- trips; random final tiebreak; skip-if-any-input-short for recipes;
-- no_stock pay_ledger row on empty-arrival to close the cycle gap.

BEGIN;

-- 1. item_recipe — global recipe table. One row per output item.
--    output_qty + inputs together let us model "10 stew per batch
--    consuming 1 each of meat/water/milk/carrots" without fractional
--    quantities (effective 0.1 of each input per bowl).
--    Empty inputs array = terminator producer (magic source — the
--    farmer's grain, the goodwife's cheese — chains that don't
--    chase upstream forever).
CREATE TABLE item_recipe (
    output_item     VARCHAR(32) PRIMARY KEY REFERENCES item_kind(name) ON UPDATE CASCADE ON DELETE CASCADE,
    output_qty      SMALLINT NOT NULL DEFAULT 1 CHECK (output_qty > 0),
    rate_qty        SMALLINT NOT NULL CHECK (rate_qty > 0),
    rate_per_hours  SMALLINT NOT NULL CHECK (rate_per_hours > 0),
    inputs          JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- inputs shape: [{"item": "<item_kind>", "qty": <smallint>}, ...]
    -- engine validates qty is a positive integer when parsing; this
    -- check just ensures the column is a JSON array.
    CONSTRAINT item_recipe_inputs_array CHECK (jsonb_typeof(inputs) = 'array'),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2. actor_produce_state — per-actor anchor for produce-tick regen.
--    Mirrors object_refresh.last_refresh_at: the produce tick uses
--    last_produced_at to compute how many units have accrued since the
--    anchor, advances the anchor by exact unit-second multiples so
--    sub-unit residue carries forward. NULL on first observation =
--    stamp anchor without filling, matches object_refresh first-pass.
CREATE TABLE actor_produce_state (
    actor_id          UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    item_kind         VARCHAR(32) NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE ON DELETE CASCADE,
    last_produced_at  TIMESTAMPTZ,
    PRIMARY KEY (actor_id, item_kind)
);

-- 3. actor_buy_state — per-actor sticky-supplier preference and
--    backoff anchor. Used as:
--      * Tiebreak preference when multiple equidistant candidates tie
--        (prefer last_bought_from in the tied set).
--      * Backoff anchor — last_buy_failed_at suppresses retries until
--        restock.buy_failure_backoff_minutes has elapsed.
--      * Source for failure-narration speech ("I tried Josiah, came
--        home empty"). Engine reads this when the seller's customers
--        arrive and asks for X.
--    No restock policy lives here — that's on actor_attribute.params.
CREATE TABLE actor_buy_state (
    actor_id                UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    item_kind               VARCHAR(32) NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE ON DELETE CASCADE,
    last_bought_from        UUID REFERENCES actor(id) ON DELETE SET NULL,
    last_buy_succeeded_at   TIMESTAMPTZ,
    last_buy_failed_at      TIMESTAMPTZ,
    last_buy_failed_reason  TEXT,
    PRIMARY KEY (actor_id, item_kind)
);

-- 4. Allow 'no_stock' as a pay_ledger.state value. When a buyer
--    arrives at a seller and finds them empty, today no pay tool
--    fires so no row is created; that lets the cycle filter miss
--    the relationship. Inserting a no_stock row stamps "I was your
--    customer for this item" even when no goods moved, so future
--    cycle-filter passes catch it.
ALTER TABLE pay_ledger DROP CONSTRAINT IF EXISTS pay_ledger_state_check;
ALTER TABLE pay_ledger ADD CONSTRAINT pay_ledger_state_check
    CHECK (state IN ('pending','accepted','declined','countered','withdrawn','failed','no_stock'));

-- 5. New item_kind: carrots. Needed for the stew transformation
--    recipe per the design (stew = meat + water + milk + carrots).
--    Portable like all other vegetables.
INSERT INTO item_kind (name, display_label, category, sort_order, capabilities)
    VALUES ('carrots', 'Carrots', 'food', 150, ARRAY['portable'])
    ON CONFLICT (name) DO NOTHING;

INSERT INTO item_satisfies (item_kind, attribute, amount)
    VALUES ('carrots', 'hunger', 3)
    ON CONFLICT (item_kind, attribute) DO NOTHING;

-- 6. Settings. Use dotted-prefix style to match existing conventions
--    (take_break.tiredness_recovery_per_minute, etc.).
INSERT INTO setting (key, value, description) VALUES
    ('restock.cycle_lookback_hours', '24',
     'Buy-resolver cycle filter window: exclude any candidate seller who has bought this item from me within the last N hours (per pay_ledger, including no_stock attempts).'),
    ('restock.buy_failure_backoff_minutes', '60',
     'After a failed buy attempt for an item, suppress retries for this many minutes. Prevents busy-waiting on a depleted upstream chain.')
    ON CONFLICT (key) DO NOTHING;

-- 7. Seed recipes. Terminators (no inputs) for items that don't have
--    a real production chain modeled yet. Stew is the lone
--    transformation — uses output_qty=10 to model "1 batch of 10 bowls
--    consumes 1 each of meat/water/milk/carrots".
--
--    Rate values are first-pass tuning and will need play-test
--    adjustment. The pattern: producers should ramp inventory fast
--    enough to cover demand peaks during their active window.
--
--    Phase 1a does NOT seed any actor_attribute.params restock
--    policies. The recipes exist but no actor uses them yet. A
--    follow-up migration will turn on John/Josiah/outskirts-producer
--    policies after Jeff reviews placement decisions.

INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, inputs) VALUES
    -- John Ellis terminators (he produces these from his work — well
    -- abstraction for water, brewing for ale, baking for bread).
    ('water',    1, 12, 1, '[]'::jsonb),  -- 12/h, abstract well draw
    ('ale',      1, 4,  1, '[]'::jsonb),  -- 4/h, brewing
    ('bread',    1, 4,  1, '[]'::jsonb),  -- 4/h, baking (placeholder until a baker NPC ships)

    -- Outskirts producer terminators (will be wired to a Goodwife /
    -- farmer NPC in a follow-up). Recipes exist now so the engine
    -- has something to look up when those policies land.
    ('cheese',   1, 2,  1, '[]'::jsonb),  -- 2/h
    ('milk',     1, 4,  1, '[]'::jsonb),  -- 4/h
    ('meat',     1, 1,  1, '[]'::jsonb),  -- 1/h
    ('carrots',  1, 3,  1, '[]'::jsonb),  -- 3/h
    ('berries',  1, 2,  1, '[]'::jsonb),  -- 2/h, currently Prudence

    -- Prudence's tea (currently produced from nothing — could become
    -- a transformation with leaves+water inputs in a future phase).
    ('coca_tea', 1, 4,  1, '[]'::jsonb),

    -- Stew — the one transformation in v1. 1 batch every 2 hours
    -- yields 10 bowls, consuming 1 each of meat/water/milk/carrots.
    -- Effective ratio 0.1 of each input per bowl.
    ('stew',    10, 10, 2,
     '[{"item":"meat","qty":1},{"item":"water","qty":1},{"item":"milk","qty":1},{"item":"carrots","qty":1}]'::jsonb)
ON CONFLICT (output_item) DO NOTHING;

-- 8. Discipline copy: append the no-fabrication guidance to roles
--    that get drawn into supply-chain narration. This is the only
--    behavior-active piece of this migration — it stops John from
--    inventing transactions like "I pay Josiah 4 coins for the
--    cheese" when no pay tool actually fired. Same shape as the
--    ZBBS-098 consume/act/serve discipline.

UPDATE attribute_definition
   SET instructions = instructions ||
       E'\n\nYour stock is replenished by your work and (later) by trips to your suppliers. The engine handles these replenishments and tells you when they happen via your perception. Do not narrate purchases, deliveries, or supply-chain events that the engine has not told you about. If asked where an item came from, you may reference your most recent successful buy (when the engine surfaces it) or "from my own work" for things you produce. Do not invent suppliers, prices, or transactions.',
       updated_at = now()
 WHERE slug IN ('tavernkeeper', 'innkeeper');

-- Note: 'merchant' / 'shopkeeper' / 'baker' attributes don't exist
-- yet (Josiah currently has actor.role='merchant' but no matching
-- attribute_definition row). When those attribute_definitions get
-- created in a follow-up, the same discipline copy should be appended
-- there.

COMMIT;
