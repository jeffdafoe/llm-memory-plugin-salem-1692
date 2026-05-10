BEGIN;

UPDATE asset
   SET visible_when_inside = false,
       stand_offset_x = NULL,
       stand_offset_y = NULL
 WHERE name = 'Market Stall (Fancy)';

COMMIT;
