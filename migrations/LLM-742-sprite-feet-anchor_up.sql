-- LLM-742: per-sprite feet line. The client anchored every one-sheet
-- character sprite at 0.9 of its frame height — right for the 32x32 villager
-- sheets, whose feet sit there, but the livestock sheets leave empty rows
-- under the hooves. Drawn at 0.9, a cow's hooves land 23 px north of where it
-- stands (a sheep 14 px, a hen or rooster 13 px), so an animal on the top row
-- of a pen draws on top of the north fence. anchor_y is the fraction of the
-- frame height where the art's feet sit; like render_scale it is a client
-- draw hint the engine never reads.
--
-- Values measured off the sheets (lowest opaque row + 1, every colourway):
--   cattle (cow, bull, heifer)  46 / 64 = 0.71875  (south/north rows; east/west end a row higher)
--   hen, rooster                45 / 64 = 0.703125 (standing rows; the south peck dips lower)
--   sheep                       21 / 32 = 0.65625

BEGIN;

ALTER TABLE public.npc_sprite
    ADD COLUMN IF NOT EXISTS anchor_y double precision NOT NULL DEFAULT 0.9;

ALTER TABLE public.npc_sprite
    DROP CONSTRAINT IF EXISTS npc_sprite_anchor_y_check;
ALTER TABLE public.npc_sprite
    ADD CONSTRAINT npc_sprite_anchor_y_check CHECK (anchor_y > 0 AND anchor_y <= 1);

UPDATE public.npc_sprite
   SET anchor_y = 0.71875
 WHERE id IN ('639d0c01-0000-4000-8000-000000000001', '639d0c02-0000-4000-8000-000000000002',
              '639d0c03-0000-4000-8000-000000000003', '639d0c04-0000-4000-8000-000000000004',
              '639d0c05-0000-4000-8000-000000000005', '639d0c06-0000-4000-8000-000000000006',
              '639d0c07-0000-4000-8000-000000000007', '639d0c08-0000-4000-8000-000000000008');

UPDATE public.npc_sprite
   SET anchor_y = 0.703125
 WHERE id IN ('641c0001-0000-4000-8000-000000000001', '641c0002-0000-4000-8000-000000000002',
              '641c0003-0000-4000-8000-000000000003', '641c0004-0000-4000-8000-000000000004',
              '641c0005-0000-4000-8000-000000000005', '641c0006-0000-4000-8000-000000000006');

UPDATE public.npc_sprite
   SET anchor_y = 0.65625
 WHERE id = '689d0c01-0000-4000-8000-000000000001';

COMMIT;
