-- ZBBS-WORK-218 — actor_relationship consolidation tracking.
--
-- Phase 3 of the engine-side continuity layer for shared-VA NPCs.
-- Phase 2A/B/C wrote salient_facts append-only on speech / pay /
-- serve / deliver_order events. Phase 3 adds a periodic consolidation
-- pass that calls the actor's own VA (salem-vendor for Hannah) to
-- compress the salient_facts trail into a rewritten summary_text,
-- then prunes the consolidated entries from the trail.
--
-- This migration only adds the consolidation marker column; the sweep
-- + LLM-call code lands in the engine commit. The marker tracks when
-- a row was last consolidated so the sweep skips stable pairs and
-- prioritizes the longest-untouched ones.
--
--   last_consolidated_at  — set to NOW() on each successful
--                           consolidation. NULL on rows never
--                           consolidated. Sweep selects pairs where
--                           this is NULL or older than the daily
--                           floor (24h).
--
-- updated_at already exists but covers BOTH event-driven appends and
-- consolidation writes — can't use it to gate sweep selection (a busy
-- relationship would never qualify because event hooks bump updated_at
-- continuously). Hence the dedicated column.

BEGIN;

ALTER TABLE actor_relationship
    ADD COLUMN last_consolidated_at TIMESTAMPTZ;

-- Index to support the sweep's "find oldest-consolidated pairs"
-- ordering. Partial because we only care about non-empty trails;
-- empty rows have nothing to consolidate.
CREATE INDEX idx_actor_relationship_consolidation
    ON actor_relationship (last_consolidated_at NULLS FIRST)
 WHERE jsonb_array_length(salient_facts) > 0;

COMMIT;
