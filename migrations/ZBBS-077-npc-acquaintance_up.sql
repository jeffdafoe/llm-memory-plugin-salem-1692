-- ZBBS-077 (M6.4.5): npc_acquaintance — who knows whom by name.
--
-- Without acquaintance, every NPC's perception would list other actors
-- by their full display name even when they've never met. In a small
-- 1692 village most folks know each other on sight, but strangers
-- (travelers, new arrivals, distant neighbors) shouldn't be greeted by
-- name. This table tracks first-meeting per pair so the perception's
-- "Here:" block can swap in a generic descriptor ("the blacksmith,"
-- "a stranger") for unknown others.
--
-- Acquaintance is symmetric in concept but stored as directed pairs to
-- keep query plans simple — each NPC's "do I know X?" is one row check.
-- The application layer writes both directions when two parties meet
-- so the symmetry holds without a CHECK constraint or trigger.
--
-- other_name is TEXT rather than a typed FK so the table can hold both
-- NPC↔NPC pairs (other_name = NPC's display_name) and NPC↔PC pairs
-- (other_name = PC's actor name) without cross-database joins.
-- Application layer enforces validity by always writing the canonical
-- name string from the source NPC/actor row.

BEGIN;

CREATE TABLE npc_acquaintance (
    npc_id UUID NOT NULL REFERENCES npc(id) ON DELETE CASCADE,
    other_name VARCHAR(100) NOT NULL,
    first_interacted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (npc_id, other_name)
);

CREATE INDEX idx_npc_acquaintance_other ON npc_acquaintance(other_name);

COMMIT;
