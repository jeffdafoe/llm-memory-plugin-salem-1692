-- LLM-623: a growable apple orchard — the third staged crop on the LLM-576
-- mechanism, planted by converting the existing 5x4 maple grove.
--
-- Migration only, no engine code: berry_state.go's crop path is stage-count
-- agnostic and keys on the 'growth-N' state tags.
--
-- WHY THE STAGES ARE NOT SHAPED LIKE WHEAT OR CARROTS. Those are annuals —
-- reaped to nothing and regrown from seed, so their growth-1 is bare ground. A
-- tree is perennial: after you pick it, it is still a tree. Mirroring the crop
-- pattern literally would shrink the tree to a sapling on every harvest and
-- regrow it in a week. So the four stages here are the FRUITING YEAR, not the
-- tree's life. Every frame is a mature tree; only the canopy changes, blossom
-- through leaf to fruit. The sheet's sapling and stump cells are deliberately
-- unused.
--
-- The tag ordinal is only an ORDER. berry_state.go renders the HIGHEST ordinal
-- whenever the plant has stock and dates the lower ones off the regrow clock,
-- so growth-4 must be the fruited frame and nothing else reads the rest.
--
-- PREREQUISITE, and the migration cannot check it: the pack PNGs must already
-- be on the VPS at /var/www/llm-memory-salem-1692/tilesets/mana-seed/
-- growable-fruit-trees/. The tiles repo has no deploy path to the server and
-- the salem deploy protects that directory (no --delete) rather than
-- populating it, so new packs land there by hand. Without them the rows below
-- are all valid and the client renders nothing.
--
-- asset / asset_state / asset_state_tag / asset_refresh_default / item_kind /
-- item_satisfies / item_recipe are REFERENCE data: load-only, no snapshot_gen,
-- never checkpoint-clobbered. actor_attribute AND village_object/object_refresh
-- ARE checkpoint-written, so parts 5-7 need the engine stopped — which the
-- deploy already does (stop -> migrate -> start). An ad-hoc apply must stop it
-- first, or the shutdown checkpoint will clobber the grove conversion.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Pack
-- ---------------------------------------------------------------------------

INSERT INTO tileset_pack (id, name, url)
VALUES ('mana-seed-fruit-trees', 'Growable Fruit Trees',
        'https://seliel-the-shaper.itch.io/growable-fruit-trees')
ON CONFLICT (id) DO NOTHING;

-- DO NOTHING is silent about WHY it did nothing, so assert the row we ended up
-- with is the row we meant. A PRESENCE check rather than a mismatch check so it
-- covers absence too — a mismatch-only test passes vacuously when there is no
-- row at all, deferring the failure to a murkier FK violation below.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM tileset_pack
         WHERE id = 'mana-seed-fruit-trees'
           AND name = 'Growable Fruit Trees'
           AND url = 'https://seliel-the-shaper.itch.io/growable-fruit-trees'
    ) THEN
        RAISE EXCEPTION
            'LLM-623: expected mana-seed-fruit-trees tileset_pack row is missing or has incompatible metadata';
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 2. Asset + states
-- ---------------------------------------------------------------------------

-- Fixed UUID rather than gen_random_uuid(): the asset_state, refresh-default
-- and village_object statements below reference it, and the _down needs to find
-- it again.
INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, z_index, is_obstacle)
VALUES (
    '019e5f00-c401-7a10-9e00-000000000623',
    'Apple Tree', 'crops',
    -- Placed trees come down ripe: asset_refresh_default seeds them full, so
    -- default_state matching the ripe stage means what the editor drops is what
    -- the engine derives a moment later.
    'growth-4',
    0.5, 0.85, 'objects', 'mana-seed-fruit-trees', 10,
    -- An obstacle, UNLIKE wheat and carrots, which are deliberately not one so
    -- the farmer can stand in his own field. Two reasons: it matches the maples
    -- being converted, so village pathing is unchanged by this migration; and
    -- it matches every other tree asset. Being an obstacle does not block
    -- gathering — both Wells are obstacles and are drawn from daily.
    true);

-- Four 48x64 cells off the red-apple sheet. The sheet is 7 columns x 8 rows;
-- each ROW is a colour/season variant and each COLUMN a stage, so these four
-- frames cross two rows: src_y 0 is the pale blossom canopy, src_y 64 the green
-- one. Column 144 is the mature tree with a full crown, 192 the same tree
-- bearing red apples, 240 a mature tree with a slimmer trunk.
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT '019e5f00-c401-7a10-9e00-000000000623', v.state,
       '/tilesets/mana-seed/growable-fruit-trees/fruit trees (apple, red) 48x64.png',
       v.src_x, v.src_y, 48, 64, 1, 0
  FROM (VALUES
        ('growth-1', 240,  0),   -- blossom, slim trunk — just picked
        ('growth-2', 144,  0),   -- blossom, full crown
        ('growth-3', 144, 64),   -- green leaf, no fruit
        ('growth-4', 192, 64)    -- ripe; renders whenever the tree has stock
       ) AS v(state, src_x, src_y);

-- The tag, not the state name, is what berry_state.go reads. They match here
-- for legibility, but the tag is the contract.
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, s.state
  FROM asset_state s
 WHERE s.asset_id = '019e5f00-c401-7a10-9e00-000000000623';

-- ---------------------------------------------------------------------------
-- 3. Placement template
-- ---------------------------------------------------------------------------

-- Copied onto every new tree by CreateVillageObject, seeded to
-- available_quantity = max_quantity (seedRefreshesFromDefaults ->
-- normalizeDefaultSupply), so a tree placed in the editor drops in ripe.
--
-- PERIODIC, not continuous, and that choice is load-bearing: periodic holds the
-- tree at 0 for the whole period and then jumps to full, so an unripe tree has
-- no stock and every existing gate already refuses it — the Gather command,
-- ResolveGatherSource, the at-source cue, and the forage move handle. No
-- separate ripeness gate exists or is needed. Continuous would trickle units in
-- and make half-grown fruit pickable.
--
-- Yield-only (amount = 0, no attribute): the fruit is forage-to-sell, so
-- visiting the tree drops no need. A need-bearing row would instead make the
-- orchard free food for its owner, which is not the chain being built here —
-- the hunger drop lives on the ITEM (part 4), so a BOUGHT apple feeds you.
--
-- 3 units / 168h across the 20 trees of part 5 is ~8.6 apples/day, between
-- carrots (~12/day) and the ~8/day of demand wheat was sized against.
INSERT INTO asset_refresh_default (
    asset_id, attribute, amount, available_quantity, max_quantity,
    refresh_mode, refresh_period_hours, gather_item)
VALUES (
    '019e5f00-c401-7a10-9e00-000000000623', NULL, 0, 3, 3, 'periodic', 168, 'apples');

-- ---------------------------------------------------------------------------
-- 4. The item
-- ---------------------------------------------------------------------------

INSERT INTO item_kind (
    name, display_label, category, sort_order, capabilities,
    display_label_singular, display_label_plural)
VALUES (
    'apples', 'Apples', 'food', 143, '{portable}', 'apple', 'apples');

-- Hunger 2 — between a berry at 1 and a loaf of bread at 4. This is what makes
-- a bought apple worth eating, and so what makes Josiah's counter (part 7) a
-- real trade rather than a shelf of decoration.
INSERT INTO item_satisfies (item_kind, attribute, amount)
VALUES ('apples', 'hunger', 2);

-- The recipe row exists ONLY to carry the prices, which perception reads
-- (trade_value.go, restock.go) for any traded good. It stays dormant: the
-- produce path is gated on an actor's 'produce' restock entry, not on the
-- recipe (StartProductionCycle checks produceEntry BEFORE makeableRecipe), and
-- nobody gets a produce entry for apples. rate_qty/rate_per_hours are NOT NULL
-- with CHECK (> 0), so they are filled with wheat's values and never read.
--
-- RETAIL 2 IS DELIBERATE. Coins are integers, so retail 1 is the cheapest
-- non-zero price and wholesale cannot go below it without paying the forager
-- nothing — which means a reseller of a retail-1 good nets exactly zero. Five
-- goods already sit at that floor (water, carrots, blueberries, ale,
-- raspberries) and Josiah is stuck on it with water. At 1/2 he clears 1 coin a
-- unit and the orchard has a working retail leg.
INSERT INTO item_recipe (
    output_item, output_qty, rate_qty, rate_per_hours, inputs, wholesale_price, retail_price)
VALUES ('apples', 1, 3, 1, '[]'::jsonb, 1, 2);

-- ---------------------------------------------------------------------------
-- 5. The grove becomes the orchard
-- ---------------------------------------------------------------------------

-- The twenty Maple Trees in the 5x4 grove (world pixels x 3056-3913,
-- y -568 to -78) become apple trees owned by Prudence Ward.
--
-- Converted IN PLACE rather than deleted and recreated: positions are preserved
-- exactly, object ids survive, and nothing referencing them breaks. The cost is
-- that seedRefreshesFromDefaults does NOT run — it only fires through
-- CreateVillageObject — so the object_refresh rows are inserted explicitly
-- below, mirroring what that path would have produced.
--
-- Safe to convert, all three checked against production before authoring:
--   * The grove twenty carry no object_refresh rows at all. Only 2 of the 44
--     maples village-wide do, both elsewhere, and both are continuous shade
--     rows with no gather_item.
--   * All twenty are unowned.
--   * Firewood does not come from maples — all 3 firewood sources are
--     Wood Pile (Large) — so this removes no supply line.
DO $$
DECLARE
    maple_id  CONSTANT uuid := '2d91c8a9-6501-4e16-873a-4d18bdc6f63e';
    apple_id  CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    prudence  CONSTANT text := '019dbcec-1149-7149-8a49-2cdb54680b86';
    grove int;
    converted int;
    seeded int;
BEGIN
    -- Fresh schema-only DB (integration harness): no village, nothing to
    -- convert. The asset and item rows above still land, which is the point —
    -- they are reference data.
    SELECT count(*) INTO grove
      FROM village_object
     WHERE asset_id = maple_id
       AND x BETWEEN 3000 AND 4000
       AND y BETWEEN -600 AND -50;
    IF grove = 0 THEN
        RETURN;
    END IF;

    -- Assert the shape BEFORE mutating it. village_object is checkpoint-written
    -- and editable live through the editor, so the grove can drift between
    -- authoring and deploy — fail loud rather than convert whatever happens to
    -- be in the box.
    IF grove <> 20 THEN
        RAISE EXCEPTION
            'LLM-623: expected exactly 20 Maple Trees in the grove bounding box, found %', grove;
    END IF;

    -- Prudence has to exist for the orchard to have an owner. If the village is
    -- present but she is not, that is drift this migration must not paper over.
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id::text = prudence) THEN
        RAISE EXCEPTION 'LLM-623: the grove is present but Prudence Ward (%) was not found', prudence;
    END IF;

    UPDATE village_object
       SET asset_id = apple_id,
           -- Ripe, matching the full supply seeded below. berry_state.go
           -- recomputes this on its next sweep anyway; setting it means the
           -- first frame drawn after boot is already correct.
           current_state = 'growth-4',
           owner_actor_id = prudence
     WHERE asset_id = maple_id
       AND x BETWEEN 3000 AND 4000
       AND y BETWEEN -600 AND -50;

    GET DIAGNOSTICS converted = ROW_COUNT;
    IF converted <> 20 THEN
        RAISE EXCEPTION 'LLM-623: grove conversion updated % rows, expected 20', converted;
    END IF;

    -- What seedRefreshesFromDefaults would have produced: full supply
    -- (available = max) and NO regen anchor — last_refresh_at stays NULL so
    -- each tree earns its own on the first regen tick, exactly as a freshly
    -- placed object does.
    INSERT INTO object_refresh (
        object_id, attribute, amount, available_quantity, max_quantity,
        refresh_mode, refresh_period_hours, last_refresh_at, gather_item)
    SELECT id, NULL, 0, 3, 3, 'periodic', 168, NULL, 'apples'
      FROM village_object
     WHERE asset_id = apple_id;

    GET DIAGNOSTICS seeded = ROW_COUNT;
    IF seeded <> 20 THEN
        RAISE EXCEPTION 'LLM-623: seeded % object_refresh rows, expected 20', seeded;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 6. Prudence forages the orchard
-- ---------------------------------------------------------------------------

-- Her herbalist restock array already carries raspberries, blueberries and
-- sage, all forage. Apples join it.
--
-- The forage_range attribute she also holds is NOT what makes this work — that
-- is the unowned-wild fallback for an actor who owns no source for the item.
-- The owner forage cue (perception/forage.go) is already ranged and
-- unconditional for a source the actor owns, which is what part 5 gave her.
DO $$
DECLARE
    prudence CONSTANT text := '019dbcec-1149-7149-8a49-2cdb54680b86';
    apple_entries int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id::text = prudence) THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM actor_attribute
         WHERE actor_id::text = prudence AND slug = 'herbalist'
           AND jsonb_typeof(params->'restock') = 'array'
    ) THEN
        RAISE EXCEPTION 'LLM-623: Prudence Ward exists but her herbalist restock array was not found';
    END IF;

    -- Assert absence before adding. actor_attribute is live-tunable through the
    -- umbilical (/restock/*), so an apples entry appearing between authoring and
    -- deploy means someone did this by hand — refuse rather than create a
    -- duplicate the restock reader would then have to arbitrate.
    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = prudence AND slug = 'herbalist'
       AND e->>'item' = 'apples';
    IF apple_entries <> 0 THEN
        RAISE EXCEPTION
            'LLM-623: Prudence already has % apples restock entr(y/ies) — refusing to add another', apple_entries;
    END IF;

    -- Appended rather than rebuilt element-wise: this adds an entry instead of
    -- transforming one, so the existing elements are untouched by construction.
    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}',
               (params->'restock') || '[{"max": 10, "item": "apples", "source": "forage"}]'::jsonb)
     WHERE actor_id::text = prudence
       AND slug = 'herbalist'
       AND jsonb_typeof(params->'restock') = 'array';

    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = prudence AND slug = 'herbalist'
       AND e->>'item' = 'apples' AND e->>'source' = 'forage';
    IF apple_entries <> 1 THEN
        RAISE EXCEPTION
            'LLM-623: Prudence apples forage entry did not land — found %', apple_entries;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 7. Josiah retails them
-- ---------------------------------------------------------------------------

-- Without a buyer the orchard is decoration: apples would pile up against
-- Prudence's cap of 10 and stop. This is the LLM-324 rule — a good with no
-- restock entry anywhere downstream silently goes nowhere.
DO $$
DECLARE
    josiah CONSTANT text := '019dcac2-e78a-715e-91b7-101f339b0891';
    apple_entries int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id::text = josiah) THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM actor_attribute
         WHERE actor_id::text = josiah AND slug = 'merchant'
           AND jsonb_typeof(params->'restock') = 'array'
    ) THEN
        RAISE EXCEPTION 'LLM-623: Josiah Thorne exists but his merchant restock array was not found';
    END IF;

    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = josiah AND slug = 'merchant'
       AND e->>'item' = 'apples';
    IF apple_entries <> 0 THEN
        RAISE EXCEPTION
            'LLM-623: Josiah already has % apples restock entr(y/ies) — refusing to add another', apple_entries;
    END IF;

    -- Cap 8, in line with his other perishable food lines (cheese, milk, meat
    -- and carrots are all 6) and below Prudence's 10, so the orchard's output
    -- has somewhere to go without the store hoarding a week of fruit.
    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}',
               (params->'restock') || '[{"max": 8, "item": "apples", "source": "buy"}]'::jsonb)
     WHERE actor_id::text = josiah
       AND slug = 'merchant'
       AND jsonb_typeof(params->'restock') = 'array';

    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = josiah AND slug = 'merchant'
       AND e->>'item' = 'apples' AND e->>'source' = 'buy';
    IF apple_entries <> 1 THEN
        RAISE EXCEPTION
            'LLM-623: Josiah apples buy entry did not land — found %', apple_entries;
    END IF;
END $$;

COMMIT;
