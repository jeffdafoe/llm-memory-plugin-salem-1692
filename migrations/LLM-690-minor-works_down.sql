-- LLM-690 down: remove the minor-work states. Refuses while any placement
-- shows one — mend them first (umbilical damage "repair", engine running), or
-- the placements would point at states that no longer exist.

BEGIN;

DO $$
DECLARE broken int;
BEGIN
    SELECT count(*) INTO broken
      FROM village_object o
      JOIN asset_state st ON st.asset_id = o.asset_id AND st.state = o.current_state
      JOIN asset_state_tag g ON g.state_id = st.id
     WHERE g.tag LIKE 'minor-of-%';

    IF broken > 0 THEN
        RAISE EXCEPTION
            'LLM-690 down: % placement(s) still show a minor-work state. Mend them first, then re-run.',
            broken;
    END IF;
END $$;

DO $$
DECLARE placed int;
BEGIN
    SELECT count(*) INTO placed
      FROM village_object
     WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000690001', '019e5f00-c401-7a10-9e00-000000690002');

    IF placed > 0 THEN
        RAISE EXCEPTION
            'LLM-690 down: % dark/light crate(s) still placed. Remove them first (engine stopped), then re-run.',
            placed;
    END IF;
END $$;

-- The two crate assets this migration added, whole.
DELETE FROM asset_state_tag
 WHERE state_id IN (
    SELECT id FROM asset_state
     WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000690001', '019e5f00-c401-7a10-9e00-000000690002'));
DELETE FROM asset_state
 WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000690001', '019e5f00-c401-7a10-9e00-000000690002');
DELETE FROM asset
 WHERE id IN ('019e5f00-c401-7a10-9e00-000000690001', '019e5f00-c401-7a10-9e00-000000690002');

DELETE FROM asset_state_tag
 WHERE state_id IN (
    SELECT st.id FROM asset_state st
     WHERE (st.asset_id = '019e5f00-c401-7a10-9e00-000000637001' AND st.state LIKE 'h-broken-%')
        OR (st.asset_id = '796d76b4-9a3b-4541-9852-98d389c1906a' AND st.state = 'crooked')
        OR (st.asset_id = '7bf7022c-c354-4333-a607-bb42650d2666' AND st.state = 'lid-loose'));

DELETE FROM asset_state st
 WHERE (st.asset_id = '019e5f00-c401-7a10-9e00-000000637001' AND st.state LIKE 'h-broken-%')
    OR (st.asset_id = '796d76b4-9a3b-4541-9852-98d389c1906a' AND st.state = 'crooked')
    OR (st.asset_id = '7bf7022c-c354-4333-a607-bb42650d2666' AND st.state = 'lid-loose');

COMMIT;
