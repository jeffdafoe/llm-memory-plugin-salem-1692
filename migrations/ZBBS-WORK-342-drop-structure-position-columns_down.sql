-- ZBBS-WORK-342 reverse: restore structure.position_x / position_y.
--
-- The down migration re-adds the columns NOT NULL DEFAULT 0 so the schema
-- shape matches pre-WORK-342 mechanically. It does NOT attempt to
-- reconstruct the original unpadded values — those were dead data the
-- engine never read again, and the live anchor lives on village_object.x
-- /y. A rollback on a deployment that has already lost the original
-- structure.position_x/y values therefore yields zeros, which is what
-- the engine had already stopped reading.

BEGIN;

ALTER TABLE public.structure
    ADD COLUMN position_x INT NOT NULL DEFAULT 0,
    ADD COLUMN position_y INT NOT NULL DEFAULT 0;

COMMIT;
