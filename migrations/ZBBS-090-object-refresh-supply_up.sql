-- ZBBS-090: Finite supply + regeneration on object_refresh, plus a
-- refresh_attribute lookup table replacing the hard-coded CHECK.
--
-- Motivation: ZBBS-085 modeled refresh as a fixed decrement on arrival
-- with no notion of supply — a well always refreshed thirst no matter
-- how often it was visited. This migration adds optional finite
-- quantity (available_quantity / max_quantity) with two replenishment
-- modes:
--
--   continuous  — water/well/berry-bush style. Engine ticks
--                 max_quantity / refresh_period_hours per hour up to
--                 max_quantity. Smooth, anchored on last_refresh_at.
--   periodic    — crop/harvest style. Engine jumps available_quantity
--                 to max_quantity once (now - last_refresh_at) >=
--                 refresh_period_hours. Sits at zero between refills.
--
-- available_quantity IS NULL means infinite supply (no regen, no clamp).
-- This preserves ZBBS-085 behavior: existing rows untouched. A well that
-- ought to run dry is migrated by the operator setting both available
-- and max to non-NULL via the editor.
--
-- Lookup table refresh_attribute replaces the (hunger,thirst,tiredness)
-- CHECK so the editor can populate its attribute picker from data
-- without code changes for new attribute names. Adding a new attribute
-- still requires engine work (consumption switch in object_refresh.go
-- and applyConsumption, plus an actor column and the attribute_tick
-- UPDATE in attributes.go) — see notes/codebase/salem/refresh-attributes
-- for the runbook.

BEGIN;

CREATE TABLE refresh_attribute (
    name          VARCHAR(32) PRIMARY KEY,
    display_label VARCHAR(64) NOT NULL,
    sort_order    SMALLINT NOT NULL DEFAULT 0
);

INSERT INTO refresh_attribute (name, display_label, sort_order) VALUES
    ('hunger',    'Hunger',    10),
    ('thirst',    'Thirst',    20),
    ('tiredness', 'Tiredness', 30);

-- Replace the hard-coded CHECK with an FK to the lookup table. ON UPDATE
-- CASCADE so a future rename of an attribute propagates; no ON DELETE
-- because deleting an attribute that's still referenced should fail
-- loudly rather than orphaning rows.
ALTER TABLE object_refresh DROP CONSTRAINT object_refresh_attribute_check;
ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_attribute_fk
    FOREIGN KEY (attribute) REFERENCES refresh_attribute(name)
    ON UPDATE CASCADE;

ALTER TABLE object_refresh
    ADD COLUMN available_quantity   SMALLINT NULL,
    ADD COLUMN max_quantity         SMALLINT NULL,
    ADD COLUMN refresh_mode         VARCHAR(16) NOT NULL DEFAULT 'continuous',
    ADD COLUMN refresh_period_hours INTEGER NULL,
    ADD COLUMN last_refresh_at      TIMESTAMPTZ NULL;

ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_mode_check
    CHECK (refresh_mode IN ('continuous','periodic'));

-- available + max travel together. NULL/NULL is the infinite case;
-- non-NULL/non-NULL has a tracked supply with a cap.
ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_quantity_pair
    CHECK (
        (available_quantity IS NULL AND max_quantity IS NULL)
        OR (available_quantity IS NOT NULL AND max_quantity IS NOT NULL)
    );

ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_quantity_nonneg
    CHECK (available_quantity IS NULL OR available_quantity >= 0);

ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_max_positive
    CHECK (max_quantity IS NULL OR max_quantity > 0);

ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_available_le_max
    CHECK (available_quantity IS NULL OR available_quantity <= max_quantity);

ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_period_positive
    CHECK (refresh_period_hours IS NULL OR refresh_period_hours > 0);

COMMIT;
