BEGIN;

DELETE FROM village_object_tag
 WHERE tag = 'noticeboard_content';

DELETE FROM asset_state_tag
 WHERE tag IN (
    'content-capacity-1',
    'content-capacity-2',
    'content-capacity-3',
    'content-capacity-4'
 )
   AND state_id IN (
    SELECT s.id FROM asset_state s
    JOIN asset a ON a.id = s.asset_id
    WHERE a.name = 'Notice Board' AND a.pack_id = 'mana-seed'
 );

ALTER TABLE village_object
    DROP COLUMN IF EXISTS content_posted_at,
    DROP COLUMN IF EXISTS content_text;

COMMIT;
