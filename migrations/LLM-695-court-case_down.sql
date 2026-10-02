-- Revert LLM-695: drop the magistrates' docket.
--
-- Manual-rollback only (the runner never applies _down.sql). DESTROYS the
-- court's record of every ruling and every case still waiting for a sitting —
-- read the table first. Coin already moved by a pay order stays where it went;
-- the payer's durable `paid` row (court_case_id) and the `ruled` rows in
-- agent_action_log are unaffected.
BEGIN;

DROP TABLE IF EXISTS public.court_case;

COMMIT;
