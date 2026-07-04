-- Revert LLM-264: restore NOT NULL attribute + the composite (object_id, attribute)
-- primary key on object_refresh.
--
-- The up migration nulled the attribute on yield-only rows; the original placeholder
-- is not recoverable and NOT NULL / the composite PK can't return while NULLs exist.
-- Restore the seed convention's placeholder (`hunger`, used by the LLM-50 / LLM-58
-- berry bushes) on every null-attribute row so the pre-migration shape can be
-- rebuilt. Best-effort, matching the LLM-24 down precedent: a down that cannot
-- perfectly reconstruct pre-migration data restores a valid canonical shape.
--
-- The up migration deliberately allows MULTIPLE null-attribute rows per object, so
-- a blanket NULL -> 'hunger' fill could produce two 'hunger' rows on one object (or
-- a 'hunger' row colliding with a pre-existing one) and violate the restored PK. The
-- preflight below fails loud with a clear reason rather than a cryptic mid-ALTER PK
-- violation; the operator resolves the duplicates (delete or re-attribute extras)
-- before re-running.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM object_refresh
        WHERE attribute IS NULL
        GROUP BY object_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'LLM-264 down: an object has multiple NULL-attribute rows; filling them all to ''hunger'' would violate the composite PK. Resolve the duplicates before reverting.';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM object_refresh nul
        JOIN object_refresh existing
          ON existing.object_id = nul.object_id
         AND existing.attribute = 'hunger'
        WHERE nul.attribute IS NULL
    ) THEN
        RAISE EXCEPTION 'LLM-264 down: an object has both a NULL-attribute row and a ''hunger'' row; filling the NULL to ''hunger'' would violate the composite PK. Resolve the collision before reverting.';
    END IF;
END $$;

-- Re-fill nulled attributes so NOT NULL + the composite PK can be restored.
UPDATE object_refresh SET attribute = 'hunger' WHERE attribute IS NULL;

ALTER TABLE object_refresh DROP CONSTRAINT object_refresh_attribute_required_for_need;
DROP INDEX object_refresh_yield_key;
ALTER TABLE object_refresh DROP CONSTRAINT object_refresh_object_attribute_key;
ALTER TABLE object_refresh ALTER COLUMN attribute SET NOT NULL;
ALTER TABLE object_refresh DROP CONSTRAINT object_refresh_pkey;
ALTER TABLE object_refresh DROP COLUMN id;
ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_pkey PRIMARY KEY (object_id, attribute);

COMMIT;
