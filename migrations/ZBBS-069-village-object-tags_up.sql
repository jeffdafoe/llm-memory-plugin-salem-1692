-- ZBBS-069: Per-instance tags on village_object.
--
-- asset_state_tag (ZBBS-025 etc.) tags asset STATES — it expresses
-- "instances of this asset are laundry lines / notice boards / rotatable"
-- i.e. identity baked into the asset template. That model doesn't fit
-- role tags like 'tavern': the same Blue House asset might have one
-- instance that's the tavern and others that are plain houses.
--
-- village_object_tag is the per-instance analog. Keyed on the placed
-- object, so the role travels with THIS building, not all buildings of
-- the same type. ON DELETE CASCADE drops tags when the object is removed.

BEGIN;

CREATE TABLE village_object_tag (
    object_id UUID NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    tag VARCHAR(64) NOT NULL,
    PRIMARY KEY (object_id, tag)
);

-- Looked up frequently by tag in the social scheduler's nearest-match
-- query. Reverse index keeps it fast even as the villages grow.
CREATE INDEX idx_village_object_tag_tag ON village_object_tag(tag);

COMMIT;
