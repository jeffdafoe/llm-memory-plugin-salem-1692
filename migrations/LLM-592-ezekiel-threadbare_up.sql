-- LLM-592 follow-up: wear Ezekiel Crane's clothes down so the cue is OBSERVABLE.
--
-- This is a deliberate observability tweak, not a simulation outcome, and it is
-- worth saying so plainly rather than dressing it up. The seeding migration put
-- its three threadbare actors on Anne Walker, Gideon Marsh and Joseph Scott —
-- chosen as the poorest by coin, which read right in-world and was wrong
-- operationally:
--
--   * Anne and Joseph are backed by the SHARED va (salem-vendor). The engine walks
--     and schedules them; an LLM tick only happens when interaction warrants a
--     voice, so they can go a long time without a turn.
--   * Gideon is stateful and ticks regularly, but a constable walking his rounds
--     is StateIdle, and the cue's audience is the working posture only — so he is
--     outside it and always was.
--
-- Net effect: the release was correct but its first visible firing depended on
-- actors that rarely tick or never qualify. (Joseph did eventually tick and the
-- cue rendered correctly, so the mechanism is proven — this is about being able
-- to watch it on demand rather than about whether it works.)
--
-- Ezekiel is the right subject on both counts. Operationally he is stateful, ticks
-- regularly, and is genuinely at his forge. Diegetically he is the cue's own
-- motivating case — garment wear is charged by worked MINUTE, so the smith burns
-- through clothes faster than anyone in the village, which is exactly the story
-- work_clothes.go opens with. A wealthy man in worn clothes is not a contradiction
-- when the wear is a consequence of the work.
--
-- Both his garments go into the threadbare band together because
-- ResolveWorkGarmentTier takes the BEST tier across kinds: a sound pair of
-- breeches would make a threadbare shift a non-problem and the cue would stay
-- silent. Threadbare is the last 20% of the budget, so shift below 2160 and
-- breeches below 2880; 11% of each leaves him about two working days in either.
--
-- UPDATE, not INSERT: LLM-592-seed-working-garments already gave him both rows.
-- Guarded on the seeded values so a re-apply after the engine has worn them
-- further does not RESET him to a higher remaining count than he has earned.
--
-- KNOWN, and not fixed here: he will be steered to buy a SHIFT. He owns shift and
-- breeches, the distributor stocks shifts and gowns and no breeches, so
-- like-for-like resolves to the shift. Historically a shift is a woman's garment
-- and a man's equivalent is a shirt, which the catalog does not carry at all —
-- its `shift` row is authored neutrally ("worn next to the skin") while `gown` and
-- `breeches` are explicitly gendered. Adding a `shirt` kind is the real fix and is
-- a separate change; this migration does not pretend to address it.
--
-- actor_inventory is CHECKPOINT-WRITTEN. The deploy runs migrations with the
-- engine stopped, so this applies cleanly and the post-deploy boot loads it. An
-- ad-hoc apply against a running engine would simply be overwritten from memory.

BEGIN;

DO $$
DECLARE
    ezekiel constant uuid := '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4';  -- Ezekiel Crane
    n int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id = ezekiel) THEN
        -- Fresh schema-only database (the integration harness) — nothing to do.
        RETURN;
    END IF;

    UPDATE actor_inventory
       SET worn_minutes_left = CASE item_kind
                                   WHEN 'shift'    THEN 1188   -- 11% of 10800
                                   WHEN 'breeches' THEN 1584   -- 11% of 14400
                               END
     WHERE actor_id = ezekiel
       AND item_kind IN ('shift', 'breeches')
       -- Only from the values the seed wrote. If the engine has already worn them
       -- past this, leave them: they are lower than what we would set anyway.
       AND worn_minutes_left IN (9720, 12960);

    GET DIAGNOSTICS n = ROW_COUNT;

    -- 0 rows means the seed rows are gone or already moved on. Not fatal — the
    -- point of this migration is observability, and a smith who has worn his
    -- clothes down on his own is the outcome it was faking. Say so in the log
    -- rather than failing a deploy over it.
    IF n = 0 THEN
        RAISE NOTICE 'LLM-592: Ezekiel''s garments were not at their seeded values — left as they are';
    END IF;
END $$;

COMMIT;
