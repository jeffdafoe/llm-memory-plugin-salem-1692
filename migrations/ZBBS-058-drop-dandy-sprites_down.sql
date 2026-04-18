-- Rollback ZBBS-058: restoration would require re-running the Dandy rows
-- from ZBBS-057. Left as a no-op — re-seed via a fresh migration if needed.

BEGIN;
COMMIT;
