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

-- The pair, item-specific. The first cut matched on `worn_minutes_left IN (9720,
-- 12960)` without tying the value to the KIND, which was wrong twice over
-- (code_review): a shift holding 12960 would have matched and been driven to the
-- breeches target, and — the one that actually mattered — if only ONE row were
-- still at its seeded value the migration would have updated just that one.
--
-- A partial pair is worse than doing nothing. ResolveWorkGarmentTier takes the
-- BEST tier across kinds, so a threadbare breeches beside a sound shift grades
-- SOUND and the cue stays silent: the migration would have reported success
-- having achieved exactly nothing. Hence all-or-nothing below.
CREATE TEMP TABLE llm592_ezekiel_pair (item_kind text, seeded int, target int) ON COMMIT DROP;
INSERT INTO llm592_ezekiel_pair (item_kind, seeded, target) VALUES
    ('shift',     9720, 1188),   -- 11% of the 10800 budget (threadbare below 2160)
    ('breeches', 12960, 1584);   -- 11% of the 14400 budget (threadbare below 2880)

DO $$
DECLARE
    ezekiel constant uuid := '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4';  -- Ezekiel Crane
    eligible int;
    landed int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id = ezekiel) THEN
        -- Fresh schema-only database (the integration harness) — nothing to do.
        RETURN;
    END IF;

    -- Both rows must still be at their seeded values before either moves.
    SELECT count(*) INTO eligible
      FROM actor_inventory ai
      JOIN llm592_ezekiel_pair p ON p.item_kind = ai.item_kind
     WHERE ai.actor_id = ezekiel
       AND ai.worn_minutes_left = p.seeded;

    IF eligible <> 2 THEN
        -- The engine has worn one or both since the seed, or the rows are gone.
        -- Not fatal: the point of this migration is observability, and a smith who
        -- wore his own clothes down is the outcome it was faking. Say so rather
        -- than failing a deploy, and change NOTHING — a half-applied pair would
        -- leave him grading sound with the cue silent.
        RAISE NOTICE 'LLM-592: Ezekiel''s garments are not both at their seeded values (% of 2 eligible) — left as they are', eligible;
        RETURN;
    END IF;

    UPDATE actor_inventory ai
       SET worn_minutes_left = p.target
      FROM llm592_ezekiel_pair p
     WHERE ai.actor_id = ezekiel
       AND ai.item_kind = p.item_kind
       AND ai.worn_minutes_left = p.seeded;

    -- Assert the intended end state per kind, not merely a row count: the count
    -- cannot tell you the right value landed on the right garment.
    SELECT count(*) INTO landed
      FROM actor_inventory ai
      JOIN llm592_ezekiel_pair p ON p.item_kind = ai.item_kind
     WHERE ai.actor_id = ezekiel
       AND ai.worn_minutes_left = p.target;

    IF landed <> 2 THEN
        RAISE EXCEPTION 'LLM-592: Ezekiel''s garments did not land on their targets (% of 2)', landed;
    END IF;
END $$;

COMMIT;
