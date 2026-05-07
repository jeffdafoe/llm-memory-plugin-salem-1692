-- ZBBS-163: DB-level "one active occupant per private room" invariant.
--
-- Problem: room_access PK is (room_id, actor_id). Multi-actor active
-- access to the same private room is allowed by the schema; "one lodger
-- per bedroom" is enforced only by application code in
-- assignBedroomForLodger's pick query (UNION ALL with FOR UPDATE
-- SKIP LOCKED). code_review (74281399) flagged this as a missing DB
-- invariant.
--
-- Fix: add `kind room_kind NOT NULL` (denormalized from structure_room;
-- required because partial unique index predicates can't reference
-- other tables) and `active BOOLEAN NOT NULL DEFAULT true` (required
-- because NOW() isn't IMMUTABLE and can't appear in an index predicate).
-- Then partial unique on (room_id) WHERE kind='private' AND active=true.
--
-- The `active` flag is maintained by a new minute-cadence sweep
-- (expireRoomAccess) that flips active=false on expired rows. Runtime
-- queries (canEnterRoom, autoBedIdleLodgers, assignBedroomForLodger,
-- handlePCSleep gate) switch from `(expires_at IS NULL OR expires_at >
-- NOW())` to `active = true`. The wake-on-expired sweep
-- (wakeExpiredSleepers) keeps its expires_at check — independent of
-- active, gives tight wake timing immune to sweep cadence.

BEGIN;

ALTER TABLE room_access
    ADD COLUMN kind   room_kind,
    ADD COLUMN active BOOLEAN NOT NULL DEFAULT true;

-- Backfill kind from structure_room. Single UPDATE; small table.
UPDATE room_access ra
   SET kind = sr.kind
  FROM structure_room sr
 WHERE sr.id = ra.room_id;

-- Mark already-expired rows inactive so the validation step below sees
-- accurate occupancy and the new index isn't immediately violated by
-- stale data. Rows with NULL expires_at (admin-granted permanent
-- access) stay active=true.
UPDATE room_access
   SET active = false
 WHERE expires_at IS NOT NULL AND expires_at <= NOW();

-- Validate: fail loudly with offending IDs if any room_access row
-- has no matching structure_room (kind backfill left it NULL).
-- ALTER COLUMN ... SET NOT NULL below would catch this but with a
-- generic "column contains null" error; this surfaces the offender(s)
-- so a human can decide whether to clean up or roll back.
DO $$
DECLARE missing TEXT;
BEGIN
    SELECT string_agg(room_id::text || '/' || actor_id::text, ', ')
      INTO missing
      FROM room_access
     WHERE kind IS NULL;
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'ZBBS-163 backfill incomplete: room_access rows with no matching structure_room (room_id/actor_id): %', missing;
    END IF;
END $$;

-- Validate: fail loudly if any private room already has more than one
-- active access row. The ZBBS-149 backfill deduped per (structure,
-- buyer) and ranked, so violations are not expected — but if they
-- exist (legacy bad data, manual inserts), the index creation below
-- would fail unhelpfully. Better to surface the offender(s) by id.
DO $$
DECLARE violations TEXT;
BEGIN
    SELECT string_agg(room_id::text || ' (' || cnt || ' active rows)', ', ')
      INTO violations
      FROM (
        SELECT room_id, count(*) AS cnt
          FROM room_access
         WHERE kind = 'private' AND active = true
         GROUP BY room_id
        HAVING count(*) > 1
      ) t;
    IF violations IS NOT NULL THEN
        RAISE EXCEPTION 'ZBBS-163 invariant violation: private rooms with multiple active access rows: %', violations;
    END IF;
END $$;

ALTER TABLE room_access ALTER COLUMN kind SET NOT NULL;

CREATE UNIQUE INDEX ux_room_access_one_private_active
    ON room_access(room_id)
    WHERE kind = 'private' AND active = true;

COMMIT;
