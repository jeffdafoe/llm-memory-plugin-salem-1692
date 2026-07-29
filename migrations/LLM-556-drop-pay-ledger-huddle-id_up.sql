-- LLM-556: drop the vestigial pay_ledger.huddle_id column.
--
-- The column is NULL on every row the table has ever held (1,987 rows as of
-- 2026-07-29, huddle_id populated on 0 of them). Nothing writes it: all four
-- pay_ledger write statements in engine/sim/repo/pg/orders.go — upsertSQL, the
-- two terminal-flip UPDATEs, and insertOrderlessSettlementSQL — name explicit
-- column lists and none includes huddle_id. Nothing reads it either: no SELECT
-- in engine/sim/repo/pg names it (loadAllSQL and loadRecentPricesSQL list their
-- columns; there is no SELECT *), and GET /api/village/umbilical/pay-ledger does
-- not return it. llm-memory-api cannot read it at all — both of that service's
-- database connection sites point at the memory_api database, never at zbbs, and
-- the string pay_ledger does not appear anywhere in that repo.
--
-- The live in-memory World.PayLedger entry DOES carry a HuddleID and it is
-- load-bearing there (AcceptPay co-presence, callerSellsItemTo arm 2,
-- ledgerCommerceHuddles). But World.PayLedger is in-memory and restart-lossy by
-- accepted design, and the pay_ledger TABLE is an orders/settlement sink, not
-- that map's backing store. The column is a leftover from the table being read
-- as if it were one.
--
-- Why it is worth removing rather than leaving inert: an always-NULL column
-- reads as available data. The natural query for "did this settlement reversal
-- cross a conversation boundary" is a self-join on pay_ledger.huddle_id, which
-- silently reports same_huddle = true for EVERY row, because
-- NULL IS NOT DISTINCT FROM NULL. That is a wrong answer that looks like a right
-- one, and it cost LLM-555 a detour. The correct route is agent_action_log —
-- join pay_ledger.id to agent_action_log.ledger_id (LLM-494), then read
-- agent_action_log.huddle_id, which IS populated.
--
-- pay_ledger.scene_id is deliberately NOT touched here. It is also unwritten
-- since 2026-05-13, but its 99 surviving rows hold live cross-database keys: all
-- 66 distinct values resolve in memory_api.chat_message_texts (1,007 message
-- rows), and nothing else in the zbbs DB carries a scene id — agent_action_log
-- has no scene_id column. Dropping it would destroy the only settlement-to-
-- transcript link for that week. It stays, documented in
-- shared/notes/codebase/salem-engine-v2/pg/orders-pg rather than as a DB comment.
--
-- ENGINE-OWNED TABLE. pay_ledger is written by the running engine and the DROP
-- takes an ACCESS EXCLUSIVE lock, so this applies on the deploy's stop → migrate
-- → start boundary (deploy.sh already sequences it that way).

BEGIN;

ALTER TABLE public.pay_ledger DROP COLUMN IF EXISTS huddle_id;

COMMIT;
