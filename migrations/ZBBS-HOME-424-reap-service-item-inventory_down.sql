-- ZBBS-HOME-424 down: no-op by design.
--
-- The up deletes vestigial service-item inventory rows that nothing in the
-- engine reads (service kinds skip every inventory gate), so there is no
-- state to restore. Re-seeding the rows would only re-create the carrying-
-- line noise the up exists to remove.

SELECT 1;
