-- ZBBS-107 down: drop the summon_errand table and its indexes, and
-- restore the original village_event event_type CHECK constraint.

BEGIN;

DROP TABLE IF EXISTS summon_errand;

-- First drop any rows that would violate the original constraint —
-- the only new event_type was summon_ring.
DELETE FROM village_event WHERE event_type = 'summon_ring';

ALTER TABLE village_event DROP CONSTRAINT village_event_event_type_check;
ALTER TABLE village_event ADD CONSTRAINT village_event_event_type_check
    CHECK (event_type = ANY (ARRAY[
        'arrival'::text,
        'departure'::text,
        'phase_dawn'::text,
        'phase_midday'::text,
        'phase_dusk'::text
    ]));

COMMIT;
