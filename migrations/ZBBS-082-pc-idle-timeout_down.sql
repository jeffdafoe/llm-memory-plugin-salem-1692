BEGIN;

DELETE FROM setting WHERE key = 'pc_idle_timeout_seconds';

COMMIT;
