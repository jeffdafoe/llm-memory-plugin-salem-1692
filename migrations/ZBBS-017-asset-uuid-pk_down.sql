-- ZBBS-017 rollback
-- This is a destructive rollback — the old string IDs are lost.
-- Would need to regenerate from asset names.
-- Not practical to reverse automatically.
RAISE EXCEPTION 'ZBBS-017 rollback not supported — string IDs are gone';
