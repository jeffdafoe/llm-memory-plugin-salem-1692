-- Revert LLM-108: return the 16 wild berry bushes' free-forage regrow to 6h.
--
-- Same pinned id set + wild shape as the up-migration. Assumes the pre-LLM-108
-- value was 6h for all 16 (the up-migration header documents the one-line SELECT
-- to confirm this at apply time). Engine STOPPED, same as the up-migration.

BEGIN;

CREATE TEMP TABLE llm108_wild (id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO llm108_wild (id) VALUES
    -- 8 wild blueberry (LLM-58 llm58_wild, far-SE clump)
    ('019d98b8-7a4b-757e-a70a-96076633d0db'),
    ('019d98b8-9c42-787e-8cbf-c91f99f8cbef'),
    ('019d98b8-bb9d-7185-b8a3-3c32435b6569'),
    ('019d98b8-d5f7-7c16-8c95-8648f0c4c54d'),
    ('019d98b8-e68f-7919-93e0-3a592797c73f'),
    ('019d98b8-f75a-7441-af9c-e8b8addae500'),
    ('019d98b9-0ad3-725a-9aaa-cb11ef87a152'),
    ('019d98b9-1bb9-77e9-ae7b-6b061aa64e1f'),
    -- 8 wild raspberry (loose; editor-placed, owner NULL)
    ('019d79ef-d9df-7efc-bc7f-36f2ce4f2deb'),
    ('019d79ef-d9de-7fca-8541-a947d1dbe40b'),
    ('019d98b3-84e4-73bb-8f13-d33c05638f0f'),
    ('019d98b3-a85d-72eb-9faf-ad3e6642c71b'),
    ('019d98b5-1e22-7aae-87be-d1afb0cb7241'),
    ('019d98b5-0313-731f-a0f2-93edb758b3dd'),
    ('019dc5f2-85c9-7851-86a3-6ac4e248e660'),
    ('019dc5f2-6281-7a0e-82c3-e66f10d04847');

DO $$
BEGIN
    IF (SELECT count(*) FROM llm108_wild) <> 16 THEN
        RAISE EXCEPTION 'LLM-108 down: wild id set has %, expected 16', (SELECT count(*) FROM llm108_wild);
    END IF;
END $$;

UPDATE object_refresh r
SET refresh_period_hours = 6
FROM village_object v
WHERE v.id = r.object_id
  AND r.object_id IN (SELECT id FROM llm108_wild)
  AND r.attribute = 'hunger'
  AND r.amount < 0
  AND r.gather_item IN ('raspberries', 'blueberries')
  AND v.owner_actor_id IS NULL;

COMMIT;
