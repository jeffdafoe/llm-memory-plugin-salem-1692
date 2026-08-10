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

-- The twenty Maple Trees of the 5x4 grove (world pixels x 3056-3913,
-- y -568 to -78) become apple trees owned by Prudence Ward.
--
-- Converted IN PLACE rather than deleted and recreated: positions are preserved
-- exactly, object ids survive, and nothing referencing them breaks. The cost is
-- that seedRefreshesFromDefaults does NOT run — it only fires through
-- CreateVillageObject — so the object_refresh rows are inserted explicitly
-- below, mirroring what that path would have produced.
--
-- THE TWENTY ARE PINNED BY ID, not selected by a bounding box. A box says "any
-- twenty maples currently in this area", which a count check cannot tell apart
-- from a substitution — remove one of the intended trees, place another maple
-- anywhere else in the box, and the count still reads 20 while the migration
-- converts the wrong tree. Pinning also gives the _down exact provenance: it
-- can revert precisely the rows this migration touched rather than whatever
-- twenty look right. Hardcoded production ids are already this file's idiom
-- (the maple asset, Prudence, Josiah), and on a village that lacks them the
-- guard below no-ops rather than guessing.
--
-- Safe to convert, all three verified against production before authoring AND
-- asserted below rather than merely asserted in this comment:
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
    grove_ids CONSTANT uuid[] := ARRAY[
        '019da68e-1ac4-75f4-9164-ad6bfb303a5d'::uuid,
        '019da68e-39fe-7cf2-bc72-39d0d02a323b'::uuid,
        '019da68d-f208-77af-b331-b64de9723109'::uuid,
        '019da68d-a3ce-723a-91c6-3381b169eee2'::uuid,
        '019da68b-e156-776e-a0e4-46c8c3b2f2f5'::uuid,
        '019da68e-6c2d-7c41-8ab5-8f39f4426f55'::uuid,
        '019da68e-dd93-7209-9614-ba403ade9640'::uuid,
        '019da68c-335c-770e-88d5-166179ad0f0f'::uuid,
        '019da68f-7458-7c2f-a67e-822964956bac'::uuid,
        '019da690-07a2-7a66-8d97-db76d77d5648'::uuid,
        '019da68f-0487-7574-a120-4161cc983632'::uuid,
        '019da68f-a92e-7120-8527-c5bc88ed56fa'::uuid,
        '019da68e-86dc-78b0-9125-ea4aa5ce392a'::uuid,
        '019da68c-94ff-71d8-be5d-efcbed2fc4ae'::uuid,
        '019da690-2863-7213-bb2e-76db22f5ddb1'::uuid,
        '019da68e-a71b-7878-b122-f938c7f1db4a'::uuid,
        '019da68f-2271-76e1-ac8c-88af8e53c194'::uuid,
        '019da68c-cb14-7f28-a7c3-ff971e40feb7'::uuid,
        '019da68f-dee7-74a3-bb0b-2da1226b906a'::uuid,
        '019da690-4df2-75ce-bc2a-30180d5a5132'::uuid];
    grove int;
    converted int;
    seeded int;
BEGIN
    -- Fresh schema-only DB (integration harness): no village at all, nothing to
    -- convert. The asset and item rows above still land, which is the point —
    -- they are reference data. This is the ONLY case that may skip quietly.
    IF NOT EXISTS (SELECT 1 FROM village_object) AND NOT EXISTS (SELECT 1 FROM actor) THEN
        RETURN;
    END IF;

    -- Past this point a village exists, so a missing grove is production drift,
    -- not an empty install — and it must NOT skip quietly. Parts 6 and 7 would
    -- otherwise still give Prudence and Josiah their apple restock entries
    -- against an orchard that does not exist, leaving her foraging a good with
    -- no source. Raising here aborts the whole transaction, which is what keeps
    -- those parts honest (code_review).
    SELECT count(*) INTO grove
      FROM village_object
     WHERE id = ANY(grove_ids) AND asset_id = maple_id;
    IF grove <> 20 THEN
        RAISE EXCEPTION
            'LLM-623: expected all 20 pinned grove objects to still be Maple Trees, found % — the grove has changed since this migration was authored', grove;
    END IF;

    -- The two preconditions the conversion relies on, asserted rather than
    -- assumed. Without the ownership check an owned maple would have its owner
    -- silently overwritten, and the _down would then null it out, losing the
    -- original owner with no record of it. The refresh check would otherwise
    -- surface only indirectly, as a confusing seeded-count mismatch below
    -- (code_review).
    IF EXISTS (SELECT 1 FROM village_object
                WHERE id = ANY(grove_ids) AND owner_actor_id IS NOT NULL) THEN
        RAISE EXCEPTION 'LLM-623: expected all 20 grove maples to be unowned — one or more now has an owner';
    END IF;
    IF EXISTS (SELECT 1 FROM object_refresh WHERE object_id = ANY(grove_ids)) THEN
        RAISE EXCEPTION 'LLM-623: expected the grove maples to carry no object_refresh rows — one or more now does';
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
     WHERE id = ANY(grove_ids);

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
     WHERE id = ANY(grove_ids);

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

    -- The postcondition asserts the COMPLETE entry, not just a matching
    -- item/source pair — that is the shape the _down looks for when it decides
    -- what it is allowed to remove, so the two have to agree exactly
    -- (code_review). jsonb equality ignores key order.
    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = prudence AND slug = 'herbalist'
       AND e = '{"max": 10, "item": "apples", "source": "forage"}'::jsonb;
    IF apple_entries <> 1 THEN
        RAISE EXCEPTION
            'LLM-623: Prudence apples forage entry did not land in its expected shape — found %', apple_entries;
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

    -- Complete-entry postcondition, same reasoning as Prudence's above.
    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = josiah AND slug = 'merchant'
       AND e = '{"max": 8, "item": "apples", "source": "buy"}'::jsonb;
    IF apple_entries <> 1 THEN
        RAISE EXCEPTION
            'LLM-623: Josiah apples buy entry did not land in its expected shape — found %', apple_entries;
    END IF;
END $$;

COMMIT;
