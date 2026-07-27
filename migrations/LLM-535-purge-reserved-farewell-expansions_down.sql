-- LLM-535 down: no-op.
--
-- The up migration deletes LLM-generated narration expansions. They are model
-- output, not derived state — nothing reconstructs them, and the whole point of
-- deleting them was that the deleted text had drifted out of register. Restoring
-- it would be restoring the defect.
--
-- The pool refills on its own: narrationDraw nudges the expansion cascade once
-- the pool has been drawn NarrationExpansionCycleFactor times its size, and the
-- new lines are written back here. Same posture as the LLM-511 down path, which
-- rolls back the schema and leaves the data alone.

BEGIN;

COMMIT;
