-- ZBBS-137 down: narrow agent_action_log_result_check back to the
-- pre-deliberation enum.
--
-- Existing 'declined'/'countered' rows must be coerced to 'rejected'
-- (closest semantic) before the narrowed constraint will accept the
-- table. We pick 'rejected' because both decline and counter are
-- non-acceptance outcomes from the seller's side — closer to a
-- rejection than to a 'failed' execution error.

BEGIN;

UPDATE agent_action_log
   SET result = 'rejected'
 WHERE result IN ('declined', 'countered');

ALTER TABLE agent_action_log DROP CONSTRAINT agent_action_log_result_check;

ALTER TABLE agent_action_log
    ADD CONSTRAINT agent_action_log_result_check
    CHECK (result = ANY (ARRAY[
        'ok'::text,
        'rejected'::text,
        'failed'::text
    ]));

COMMIT;
