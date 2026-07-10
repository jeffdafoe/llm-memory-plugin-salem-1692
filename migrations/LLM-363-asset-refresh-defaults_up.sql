-- LLM-363: asset-level refresh defaults, copied onto new village_objects at placement.
--
-- Adds asset_refresh_default: a per-asset TEMPLATE of object_refresh policy rows.
-- When the editor drops a new village_object of an asset that carries defaults,
-- CreateVillageObject seeds the object's refresh set from these rows
-- (available_quantity = max_quantity — a fresh, full source) so a forageable lands
-- working instead of inert. Authored via the admin set-refresh-default route.
--
-- REFERENCE DATA, not engine-owned. Unlike object_refresh / village_object (which
-- the engine checkpoint-writes and prunes by snapshot_gen), asset_refresh_default
-- is load-only reference state written through directly by the editor route — the
-- same posture as the asset geometry columns (door/footprint/stand). It carries no
-- snapshot_gen and the engine never clobbers it. The deploy still applies
-- migrations with the engine stopped (stop -> migrate -> start); the backfill below
-- only READS the engine-owned object_refresh / village_object tables, so even a
-- concurrent engine could not delete-stale it.
--
-- Column shape mirrors the LIVE object_refresh (post LLM-24's relaxed amount check
-- + LLM-264's nullable attribute), minus last_refresh_at (an engine-managed regen
-- anchor, meaningless on a template) and snapshot_gen (not checkpointed).

BEGIN;

CREATE TABLE asset_refresh_default (
    id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    asset_id             uuid NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
    attribute            varchar(32),
    amount               smallint NOT NULL,
    available_quantity   smallint,
    max_quantity         smallint,
    refresh_mode         varchar(16) NOT NULL DEFAULT 'continuous',
    refresh_period_hours integer,
    dwell_amount         smallint,
    dwell_period_minutes integer,
    gather_item          varchar(32),

    -- amount < 0 = need-bearing (eat/drink in place); amount = 0 = yield-only
    -- forage-to-sell, legal only with a gather_item (LLM-24 relaxed shape).
    CONSTRAINT asset_refresh_default_amount_negative
        CHECK (amount < 0 OR (amount = 0 AND NULLIF(btrim(gather_item::text), ''::text) IS NOT NULL)),
    -- A need-bearing row must name its need; a yield-only row may omit it (LLM-264).
    CONSTRAINT asset_refresh_default_attribute_required_for_need
        CHECK (attribute IS NOT NULL OR amount = 0),
    CONSTRAINT asset_refresh_default_available_le_max
        CHECK (available_quantity IS NULL OR available_quantity <= max_quantity),
    CONSTRAINT asset_refresh_default_quantity_nonneg
        CHECK (available_quantity IS NULL OR available_quantity >= 0),
    -- available/max are both set (finite supply) or both null (infinite).
    CONSTRAINT asset_refresh_default_quantity_pair
        CHECK ((available_quantity IS NULL AND max_quantity IS NULL)
            OR (available_quantity IS NOT NULL AND max_quantity IS NOT NULL)),
    CONSTRAINT asset_refresh_default_max_positive
        CHECK (max_quantity IS NULL OR max_quantity > 0),
    CONSTRAINT asset_refresh_default_mode_check
        CHECK (refresh_mode::text = ANY (ARRAY['continuous'::text, 'periodic'::text])),
    CONSTRAINT asset_refresh_default_period_positive
        CHECK (refresh_period_hours IS NULL OR refresh_period_hours > 0),
    CONSTRAINT asset_refresh_default_dwell_amount_negative
        CHECK (dwell_amount IS NULL OR dwell_amount < 0),
    CONSTRAINT asset_refresh_default_dwell_pair
        CHECK ((dwell_amount IS NULL) = (dwell_period_minutes IS NULL)),
    CONSTRAINT asset_refresh_default_dwell_period_positive
        CHECK (dwell_period_minutes IS NULL OR dwell_period_minutes > 0),
    -- Enforce the "empty means absent" convention at the DB level too. The Go
    -- writer and the backfill both NULL out a blank attribute / gather_item; these
    -- keep a direct SQL insert of '' from bypassing that — a blank would evade the
    -- partial-index uniqueness semantics below and read oddly against the Go model.
    CONSTRAINT asset_refresh_default_attribute_nonblank
        CHECK (attribute IS NULL OR btrim(attribute) <> ''),
    CONSTRAINT asset_refresh_default_gather_item_nonblank
        CHECK (gather_item IS NULL OR btrim(gather_item) <> '')
);

-- Need rows (non-null attribute) unique per (asset, attribute); yield-only rows
-- (null attribute) unique per (asset, gather_item). BOTH indexes are PARTIAL so the
-- uniqueness they enforce is explicit and non-overlapping: NULL attributes compare
-- distinct, so a non-partial (asset_id, attribute) index would silently fail to
-- constrain yield rows — the partial predicates make that split intentional. The
-- yield predicate also excludes a null gather_item defensively. Mirrors
-- object_refresh_object_attribute_key + object_refresh_yield_key.
CREATE UNIQUE INDEX asset_refresh_default_attribute_key
    ON asset_refresh_default (asset_id, attribute) WHERE attribute IS NOT NULL;
CREATE UNIQUE INDEX asset_refresh_default_yield_key
    ON asset_refresh_default (asset_id, gather_item) WHERE attribute IS NULL AND gather_item IS NOT NULL;
CREATE INDEX idx_asset_refresh_default_asset ON asset_refresh_default (asset_id);

-- Backfill defaults for assets that already have configured forageables, so the
-- feature works retroactively (existing Sage Bush / berry drops seed correctly)
-- without re-authoring each asset. For each asset with at least one object_refresh
-- row, take one representative instance (lowest object id, deterministic) and copy
-- its full row-set as the asset default, seeding available_quantity to
-- max_quantity (a pristine full source). Empty-string attributes are normalized to
-- NULL to match the LLM-264 nullable convention. On a fresh database object_refresh
-- is empty, so this is a natural no-op. The copied set is one object's rows, already
-- unique per object_refresh's own (object_id, attribute) + yield-key constraints, so
-- it cannot violate the new partial unique indexes above (verified against prod:
-- zero collisions, zero blank attributes/gather_items).
WITH representative AS (
    SELECT DISTINCT ON (vo.asset_id) vo.asset_id, orf.object_id
      FROM object_refresh orf
      JOIN village_object vo ON vo.id = orf.object_id
     ORDER BY vo.asset_id, orf.object_id
)
INSERT INTO asset_refresh_default (
    asset_id, attribute, amount, available_quantity, max_quantity,
    refresh_mode, refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item)
SELECT vo.asset_id,
       NULLIF(btrim(orf.attribute), ''),
       orf.amount,
       orf.max_quantity,          -- available seeds to full
       orf.max_quantity,
       orf.refresh_mode, orf.refresh_period_hours,
       orf.dwell_amount, orf.dwell_period_minutes,
       NULLIF(btrim(orf.gather_item), '')
  FROM representative rep
  JOIN object_refresh orf ON orf.object_id = rep.object_id
  JOIN village_object vo  ON vo.id = orf.object_id;

COMMIT;
