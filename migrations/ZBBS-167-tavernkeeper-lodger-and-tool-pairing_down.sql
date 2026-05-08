-- ZBBS-167 down: strip the appended ALREADY-CHECKED-IN LODGERS and
-- SPEECH AND TOOL PAIRING paragraphs from the tavernkeeper role
-- overlay. Targets the exact text the up-migration appended; if the
-- text has been hand-edited since, the regex won't match and the
-- column is left as-is rather than corrupted.

BEGIN;

UPDATE attribute_definition
   SET instructions = REGEXP_REPLACE(
           instructions,
           E'\n\nALREADY-CHECKED-IN LODGERS:.*?dialogue level alone \\(e\\.g\\. "right this way" without an unfulfilled handoff\\)\\.',
           '',
           'sg'
       ),
       updated_at = NOW()
 WHERE slug = 'tavernkeeper'
   AND instructions LIKE '%ALREADY-CHECKED-IN LODGERS%';

COMMIT;
