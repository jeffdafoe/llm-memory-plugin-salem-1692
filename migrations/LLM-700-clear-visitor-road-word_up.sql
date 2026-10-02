-- LLM-700: only the messenger carries word now, and it is outside news written
-- after he spawns. Every payload saved before this deploy is an old village road
-- word (a resident's own trade framed as news "from the road"), including any on a
-- messenger, which would also stop his news call. Clear them all once at deploy;
-- the engine is stopped while migrations run, so no new news is lost.

BEGIN;
UPDATE visitor SET payload = '' WHERE payload <> '';
COMMIT;
