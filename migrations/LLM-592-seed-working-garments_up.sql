-- LLM-592: give the village working clothes to wear out.
--
-- WHY. LLM-422 wears garments by worked MINUTE and LLM-589 gave those budgets a
-- realistic scale, but 24 of the 27 actors own no working garment at all — the
-- world was seeded without them, and LLM-410 made clothing import-only, so the
-- only garments that have ever existed are the ones on the distributor's shelf.
-- The working-clothes cue lands in the same release; without this seed its first
-- act would be to tell essentially every working NPC at once that they have
-- nothing fit to work in, point all of them at the General Store, and then fall
-- silent village-wide the moment his five garments sold. That is a seeding gap
-- being reported as a simulation outcome.
--
-- WHAT. One shift plus one second garment for each of the 15 agent-backed actors,
-- each with PARTIAL wear already on the clock so they do not all fall due
-- together. The `worn_minutes_left` column is REMAINING worked minutes, not
-- consumed (engine/sim/garment_wear.go), and NULL means a fresh unit — so every
-- value below is deliberately short of its budget.
--
-- The wear phases are staggered by station: the keepers and businessowners start
-- better dressed than the day-labourers, which is both diegetically right for
-- 1692 and puts the cue's first voices where they belong. Three actors (Anne
-- Walker, Gideon Marsh, Joseph Scott) start inside the threadbare band on BOTH
-- garments and so hear the cue at first boot; everyone else is silent until wear
-- accrues. Threadbare is the last 20% of the budget
-- (garment_threadbare_fraction_x100 = 20), so: shift below 2160, breeches/gown
-- below 2880.
--
-- Both garments per actor share a phase deliberately. ResolveWorkGarmentTier
-- returns the BEST tier across kinds — a sound gown makes a threadbare shift a
-- non-problem, because he would wear the good one — so independent phases would
-- have silenced the cue for everyone until both happened to run down at once.
--
-- QUANTITY IS 1 PER KIND, also deliberately: qty >= 2 reads as a fresh spare on
-- the shelf and grades sound whatever the in-use unit's wear.
--
-- WHICH KIND IS DATA, NOT A MODEL. Breeches for the men and gowns for the women
-- follows the catalog's own descriptions ("cut to the knee for a working man" /
-- "a woman's gown of good cloth"). The ENGINE still models no sex and treats every
-- working garment as interchangeable — this is seeded flavour, not a mechanic, and
-- nothing reads it back.
--
-- WHO IS EXCLUDED, and why each:
--   * Josiah Thorne, the distributor — inventory carries no personal-vs-stock
--     split, so a personal garment would be indistinguishable from his sale stock.
--     He is excluded from the wear sweep for exactly this reason
--     (sim.actorWearsGarments), so seeded clothes would never wear anyway.
--   * The 8 decorative actors and the ducks — no LLM agent, so they never
--     deliberate and could not act on the cue; their garments would burn silently.
--   * PCs — a player's clothes are not ours to invent.
--
-- ON CONFLICT DO NOTHING preserves live state: an actor already wearing a kind
-- keeps the wear the engine has put on it. Two rows are in that position today
-- (Joseph Scott's breeches at 302 and Moses James's at 14399), both left alone.
--
-- actor_inventory is CHECKPOINT-WRITTEN by the engine. The deploy runs migrations
-- with the engine stopped (down -> migrate -> up), so this applies cleanly and the
-- post-deploy boot loads the seeded rows. loadAllInventorySQLA reads every row
-- with no snapshot_gen filter, so the default gen 0 is re-stamped by the first
-- checkpoint rather than swept. An ad-hoc apply outside a deploy must stop the
-- engine first, or the running world will checkpoint over it.

BEGIN;

-- The seed, named once and read by both the INSERT and the assertion, so the two
-- can never disagree about who was supposed to get clothes. ON COMMIT DROP rolls
-- back cleanly under the deploy's dry run, which swaps the terminating COMMIT for
-- ROLLBACK.
CREATE TEMP TABLE llm592_seed (actor_id text, item_kind text, worn_minutes_left int) ON COMMIT DROP;

INSERT INTO llm592_seed (actor_id, item_kind, worn_minutes_left)
SELECT * FROM (VALUES
    -- keepers and businessowners: well found, late to need replacing
    ('019da6f9-1b4c-7dda-bb6b-3248cdafb2c4', 'shift',     9720),  -- Ezekiel Crane
    ('019da6f9-1b4c-7dda-bb6b-3248cdafb2c4', 'breeches', 12960),
    ('70419d0c-3668-428c-8bd8-633993c3aa60', 'shift',     9180),  -- Hannah Boggs
    ('70419d0c-3668-428c-8bd8-633993c3aa60', 'gown',     12240),
    ('019dbcec-1149-7149-8a49-2cdb54680b86', 'shift',     8640),  -- Prudence Ward
    ('019dbcec-1149-7149-8a49-2cdb54680b86', 'gown',     11520),
    ('019da6b2-7074-7b19-ab19-89b6fc3a29a1', 'shift',     8100),  -- John Ellis
    ('019da6b2-7074-7b19-ab19-89b6fc3a29a1', 'breeches', 10800),
    ('019da6af-c8c9-7eb8-aead-759142785789', 'shift',     7560),  -- Elizabeth Ellis
    ('019da6af-c8c9-7eb8-aead-759142785789', 'gown',     10080),
    ('019da6ae-3376-73fc-8872-1cbb3ada1c78', 'shift',     7020),  -- Moses James
    ('019da6ae-3376-73fc-8872-1cbb3ada1c78', 'breeches',  9360),  -- (kept at 14399 — pre-existing)

    -- day-labourers and hands: middling wear
    ('019dcaf9-1d10-73b8-a4a5-1debc3f2992e', 'shift',     6480),  -- Nathaniel Cole
    ('019dcaf9-1d10-73b8-a4a5-1debc3f2992e', 'breeches',  8640),
    ('019da6be-d36d-789e-9bf1-580f9982ecb9', 'shift',     5940),  -- Constance Scott
    ('019da6be-d36d-789e-9bf1-580f9982ecb9', 'gown',      7920),
    ('019da6d3-5038-79cc-a09a-1a3356bda342', 'shift',     5400),  -- Patience Walker
    ('019da6d3-5038-79cc-a09a-1a3356bda342', 'gown',      7200),
    ('019da6d0-ef1b-7e27-9163-37a3f2ce5bb0', 'shift',     4860),  -- Silence Walker
    ('019da6d0-ef1b-7e27-9163-37a3f2ce5bb0', 'gown',      6480),
    ('019da6b5-3143-71e0-9f47-6bf3af456524', 'shift',     4320),  -- Abraham Warren
    ('019da6b5-3143-71e0-9f47-6bf3af456524', 'breeches',  5760),
    ('019da6d4-24d2-7461-88b0-72b2b288bd5c', 'shift',     3780),  -- Lewis Walker
    ('019da6d4-24d2-7461-88b0-72b2b288bd5c', 'breeches',  5040),

    -- in rags: threadbare on BOTH, so these three hear the cue at first boot
    ('019da6d7-98fc-738d-859e-5614bae1b2d0', 'shift',     1512),  -- Anne Walker
    ('019da6d7-98fc-738d-859e-5614bae1b2d0', 'gown',      2016),
    ('4561da54-eb08-46c8-8f05-ddc0aadaebff', 'shift',     1296),  -- Constable Gideon Marsh
    ('4561da54-eb08-46c8-8f05-ddc0aadaebff', 'breeches',  1728),
    ('019da6b7-a853-79fb-91eb-645e5d9915c1', 'shift',     1080),  -- Joseph Scott
    ('019da6b7-a853-79fb-91eb-645e5d9915c1', 'breeches',  1440)   -- (kept at 302 — pre-existing)
  ) AS v(actor_id, item_kind, worn_minutes_left);

INSERT INTO actor_inventory (actor_id, item_kind, quantity, worn_minutes_left)
SELECT s.actor_id::uuid, s.item_kind, 1, s.worn_minutes_left
  FROM llm592_seed s
 WHERE EXISTS (SELECT 1 FROM actor a WHERE a.id = s.actor_id::uuid)
ON CONFLICT (actor_id, item_kind) DO NOTHING;

-- Completeness assertion, mirroring LLM-422's own guard and LLM-589's. A silent
-- partial seed is the dangerous failure here: the cue would speak for whoever was
-- missed and nobody would know the seed was the reason.
--
-- Asserted over the INTENDED SET — the actors named in the seed above — rather
-- than over "every agent-backed actor except Josiah by display name", which the
-- first cut did (code_review). That derived set was wrong in both directions: it
-- leaned on a display name for the exclusion, and it would have failed a future
-- deploy for any NEW agent-backed NPC this migration was never written to cover.
-- Reading the seed table means Josiah and the PCs are out of scope by simply not
-- being in it, with no exception clause to keep true.
--
-- Actors in the seed that do not exist are skipped, not failed: that is the
-- fresh schema-only database (the integration harness), where the whole seed is
-- correctly a no-op.
DO $$
DECLARE missing text;
BEGIN
    SELECT string_agg(DISTINCT a.display_name, ', ' ORDER BY a.display_name) INTO missing
      FROM llm592_seed s
      JOIN actor a ON a.id = s.actor_id::uuid
     WHERE NOT EXISTS (
           SELECT 1 FROM actor_inventory i
            WHERE i.actor_id = a.id
              AND i.item_kind IN ('shift', 'breeches', 'gown'));
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-592: seeded actors left with no working garment: %', missing;
    END IF;
END $$;

COMMIT;
