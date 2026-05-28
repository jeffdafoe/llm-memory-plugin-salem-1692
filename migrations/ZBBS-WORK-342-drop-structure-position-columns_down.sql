-- ZBBS-WORK-342 reverse: restore structure.position_x / position_y.
--
-- The down migration re-adds the columns AND backfills them from the
-- backing village_object (matching the Shared-Identity Bridge:
-- structure.id::text = village_object.id). Backfill mirrors the original
-- v1 / ZBBS-WORK-240 convention — UNPADDED tiles, FLOOR(pixel / 32) — so
-- a code-rollback to the pre-WORK-342 readers gets approximately the
-- shape they used to read (the bad shape they always read, but
-- consistent with old expectations rather than uniform zeros).
--
-- Adopted from code_review's R1 suggestion (2026-05-28): defaulting all
-- rows to (0,0) would put every structure at world origin for the old
-- consumers (lodging PC spawn, scene origins), which is worse than
-- restoring the original unpadded-tile shape. The pre-WORK-342 readers
-- were already wrong in two ways (pad-drop + staleness) — a backfilled
-- rollback at least reproduces the value those readers had been
-- silently consuming.
--
-- Add nullable → backfill → set NOT NULL DEFAULT 0, in three steps so
-- the constraint never blocks the backfill on a partially-bridged row.

BEGIN;

ALTER TABLE public.structure
    ADD COLUMN position_x INT,
    ADD COLUMN position_y INT;

UPDATE public.structure s
   SET position_x = FLOOR(vo.x / 32)::INT,
       position_y = FLOOR(vo.y / 32)::INT
  FROM public.village_object vo
 WHERE vo.id::text = s.id;

-- Defensive: any structure without a backing village_object (Shared-
-- Identity Bridge violation, supposed to be impossible per deploy-time
-- validation) gets zeros, matching the old "no anchor" shape.
UPDATE public.structure SET position_x = 0 WHERE position_x IS NULL;
UPDATE public.structure SET position_y = 0 WHERE position_y IS NULL;

ALTER TABLE public.structure
    ALTER COLUMN position_x SET NOT NULL,
    ALTER COLUMN position_y SET NOT NULL,
    ALTER COLUMN position_x SET DEFAULT 0,
    ALTER COLUMN position_y SET DEFAULT 0;

COMMIT;
