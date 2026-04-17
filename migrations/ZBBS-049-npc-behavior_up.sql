-- ZBBS-049: Per-NPC behavior identity.
--
-- Adds npc.behavior (nullable). Current values:
--   'lamplighter' — at dusk walks the route lighting night-active objects;
--                   at dawn walks it again extinguishing them.
--
-- Extensible to 'washerwoman' (rotatable laundry) and 'crier' (rotatable
-- notice boards) in milestone 5. One row per behavior for now — the village
-- has exactly one lamplighter, one washerwoman, etc. If we want multiple of
-- a role later, the engine will just walk them in parallel routes over
-- partitioned targets.

ALTER TABLE npc ADD COLUMN behavior VARCHAR(32);

-- Ezekiel Crane: the village lamplighter (blacksmith-by-day is fine, he
-- runs the lamp route at dawn + dusk).
UPDATE npc SET behavior = 'lamplighter' WHERE display_name = 'Ezekiel Crane';
