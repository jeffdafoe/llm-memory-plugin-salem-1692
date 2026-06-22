-- LLM-58 follow-up: tag the Blueberry Bush's berries/bare states in
-- asset_state_tag.
--
-- LLM-58 gave the Blueberry asset (630909ca) its 'berries' and 'bare'
-- asset_state rows but NOT their asset_state_tag entries. The berries/bare
-- reactor resolves states via Asset.StateForTag (engine/sim/asset.go), which
-- matches on AssetState.Tags -- loaded from asset_state_tag
-- (engine/sim/repo/pg/assets.go) -- NOT on the state name. Without the tags
-- StateForTag returns nil, refreshObjectBerryState short-circuits
-- ("not berry-state-tracked"), and the blueberry bushes never flip to 'bare'
-- when picked clean. The Raspberry Bush works because its states are tagged.
--
-- Tag value == state name, matching the Raspberry Bush ('berries' state tagged
-- 'berries', 'bare' tagged 'bare'). Looked up by (asset_id, state) so no
-- environment-specific serial state_id is hard-coded.
--
-- asset_state_tag is catalog data (loaded at boot, NOT engine-checkpoint-
-- written), so this is a normal migration -- it does not require the engine
-- stopped. The engine just needs its usual deploy restart to reload the catalog.

BEGIN;

-- Fail loud if the Blueberry asset isn't in the expected 2-state shape (LLM-58),
-- so a bad/partial catalog can't silently tag only one state. Gated on the
-- Blueberry `asset` row EXISTING — seeded content that neither LLM-58 nor this
-- migration creates (both only touch asset_state). On a fresh schema-only DB the
-- asset isn't seeded, so skip the assertion (the INSERT below no-ops). On a
-- seeded DB, require exactly the 2 states — this also catches a bad bare-only
-- partial (asset present but missing 'berries'), which a 'berries'-gated check
-- would have skipped.
DO $$
DECLARE n integer;
BEGIN
    IF EXISTS (SELECT 1 FROM asset
               WHERE id = '630909ca-df4f-43ac-9fc4-5192ca44da73') THEN
        SELECT count(*) INTO n FROM asset_state
        WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73' AND state IN ('berries', 'bare');
        IF n <> 2 THEN
            RAISE EXCEPTION 'LLM-58b: expected 2 Blueberry Bush states (berries, bare), found %', n;
        END IF;
    END IF;
END $$;

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, s.state
FROM asset_state s
WHERE s.asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND s.state IN ('berries', 'bare')
ON CONFLICT (state_id, tag) DO NOTHING;

COMMIT;
