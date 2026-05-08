-- ZBBS-179 down — remove the business tag rows.
--
-- This deletes ALL 'business'-tagged rows, including any that were
-- manually added post-up via the editor. That's appropriate: rolling
-- back means the tag concept goes away, so any rows referencing it
-- become invalid.

BEGIN;

DELETE FROM village_object_tag WHERE tag = 'business';

COMMIT;
