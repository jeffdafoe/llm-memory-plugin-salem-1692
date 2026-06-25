-- LLM-108: slow the wild berry bushes' free-forage regrow from 6h to 24h.
--
-- The 16 WILD eat-in-place berry bushes (owner_actor_id NULL, amount < 0 hunger,
-- finite supply 3, gather_item raspberries/blueberries) currently regen every 6h
-- -- free forage ~4x/day. This bumps their refresh_period_hours to 24h (once-
-- daily) to narrow free-forage abundance relative to vendor food and to Prudence
-- Ward's 168h forage-to-sell farm. Prudence's 48 owned farm bushes are
-- deliberately untouched (owner-gated, amount 0) and are excluded by the
-- owner NULL + amount < 0 shape below.
--
-- The set is 8 wild blueberry (LLM-58 far-SE "llm58_wild" clump) + 8 wild
-- raspberry (loose). The wild raspberry rows were editor-placed (not in any
-- checked-in migration); LLM-58 set the wild blueberries to 6h "mirroring the
-- loose raspberries", so all 16 are EXPECTED at 6h. The up-migration does not
-- depend on that (it sets 24h unconditionally for the matched rows), but the
-- down-migration reverts to 6h, so CONFIRM the live state before apply:
--   SELECT object_id, gather_item, refresh_period_hours
--     FROM object_refresh
--    WHERE object_id IN (... the 16 ids below ...)
--    ORDER BY gather_item;
-- Expect 16 rows, all refresh_period_hours = 6.
--
-- ENGINE-OWNED TABLE. object_refresh is checkpoint-written by the running engine.
-- Apply with the engine STOPPED (stop -> migrate -> start) or the old binary's
-- shutdown checkpoint clobbers it. snapshot_gen is left untouched; LoadAll has no
-- gen filter, so the edited rows enter memory at boot and the first checkpoint
-- re-stamps them.
--
-- Pinned by explicit id (LLM-58 convention) so it can never pull in a later-placed
-- bush by shape. Rerun-safe: the UPDATE is idempotent (24 -> 24); the guards fail
-- loud on a stale/missing id set or an unexpected matched-row count.

BEGIN;

CREATE TEMP TABLE llm108_wild (id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO llm108_wild (id) VALUES
    -- 8 wild blueberry (LLM-58 llm58_wild, far-SE clump)
    ('019d98b8-7a4b-757e-a70a-96076633d0db'),
    ('019d98b8-9c42-787e-8cbf-c91f99f8cbef'),
    ('019d98b8-bb9d-7185-b8a3-3c32435b6569'),
    ('019d98b8-d5f7-7c16-8c95-8648f0c4c54d'),
    ('019d98b8-e68f-7919-93e0-3a592797c73f'),
    ('019d98b8-f75a-7441-af9c-e8b8addae500'),
    ('019d98b9-0ad3-725a-9aaa-cb11ef87a152'),
    ('019d98b9-1bb9-77e9-ae7b-6b061aa64e1f'),
    -- 8 wild raspberry (loose; editor-placed, owner NULL)
    ('019d79ef-d9df-7efc-bc7f-36f2ce4f2deb'),
    ('019d79ef-d9de-7fca-8541-a947d1dbe40b'),
    ('019d98b3-84e4-73bb-8f13-d33c05638f0f'),
    ('019d98b3-a85d-72eb-9faf-ad3e6642c71b'),
    ('019d98b5-1e22-7aae-87be-d1afb0cb7241'),
    ('019d98b5-0313-731f-a0f2-93edb758b3dd'),
    ('019dc5f2-85c9-7851-86a3-6ac4e248e660'),
    ('019dc5f2-6281-7a0e-82c3-e66f10d04847');

-- Fail loud if an id was lost when editing this file.
DO $$
BEGIN
    IF (SELECT count(*) FROM llm108_wild) <> 16 THEN
        RAISE EXCEPTION 'LLM-108: wild id set has %, expected 16', (SELECT count(*) FROM llm108_wild);
    END IF;
END $$;

-- Confirm the targeted rows are the wild eat-in-place berry shape (owner NULL,
-- amount < 0 hunger, berry gather_item) before mutating -- this is what keeps the
-- migration off Prudence's owned (amount 0) farm rows. Allow the (0) unseeded case
-- (a fresh schema-only DB -- the test harness / a new env); the seeded case must
-- be exactly 16. A partial count is a stale-id or unexpected-shape failure.
DO $$
DECLARE
    n int;
BEGIN
    SELECT count(*) INTO n
      FROM object_refresh r
      JOIN village_object v ON v.id = r.object_id
     WHERE r.object_id IN (SELECT id FROM llm108_wild)
       AND r.attribute = 'hunger'
       AND r.amount < 0
       AND r.gather_item IN ('raspberries', 'blueberries')
       AND v.owner_actor_id IS NULL;
    IF n NOT IN (0, 16) THEN
        RAISE EXCEPTION 'LLM-108: expected 0 (unseeded) or 16 wild eat-in-place berry rows, found %', n;
    END IF;
END $$;

UPDATE object_refresh r
SET refresh_period_hours = 24
FROM village_object v
WHERE v.id = r.object_id
  AND r.object_id IN (SELECT id FROM llm108_wild)
  AND r.attribute = 'hunger'
  AND r.amount < 0
  AND r.gather_item IN ('raspberries', 'blueberries')
  AND v.owner_actor_id IS NULL;

COMMIT;
