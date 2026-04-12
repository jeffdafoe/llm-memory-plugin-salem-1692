-- Add animation support to asset_state.
-- frame_count: number of horizontal frames starting at src_x (1 = static, default)
-- frame_rate: frames per second (0 = static, default)
-- Animated frames are consecutive in the tileset, each src_w wide, running right from src_x.
ALTER TABLE asset_state ADD COLUMN frame_count INT NOT NULL DEFAULT 1;
ALTER TABLE asset_state ADD COLUMN frame_rate DOUBLE PRECISION NOT NULL DEFAULT 0;

-- Campfire lit animation: 4 frames at 8 FPS
-- Frames run right from (32,0): (32,0), (64,0), (96,0), (128,0)
UPDATE asset_state SET frame_count = 4, frame_rate = 8
WHERE asset_id = 'campfire' AND state = 'default';
