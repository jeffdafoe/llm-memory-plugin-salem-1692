package pg

import (
	"context"
	"fmt"
	"log"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// AssetsRepo loads the asset catalog — assets plus their tileset packs,
// visual states (with optional light params + tags), and attachment
// slots — flattened into sim.Asset aggregates keyed by the asset.id
// UUID. Reference state: read-only, no checkpoint path. Admin edits
// write directly to the underlying tables through the editor port; the
// world rebuilds the map wholesale via LoadAll at startup and on SIGHUP.
//
// Port of v1's handleListAssets catalog assembly (engine/assets.go). The
// in-memory map is keyed by asset.id (UUID) to match VillageObject.AssetID
// and the asset_state / asset_slot child rows' asset_id FK shape.
type AssetsRepo struct {
	pool Pool
}

// NewAssetsRepo constructs an AssetsRepo against the given pool. Normal
// wiring path is pg.NewRepository.
func NewAssetsRepo(pool Pool) *AssetsRepo {
	return &AssetsRepo{pool: pool}
}

// loadAllPacksSQL reads the tileset packs that asset.pack_id points at.
// pack_group / pack_source are not modeled on sim.TilesetPack.
const loadAllPacksSQL = `
SELECT id, name, url
  FROM tileset_pack
 ORDER BY name`

// loadAllAssetsSQL reads the catalog rows. rotation_algo and
// transition_spread_seconds are selected (unlike v1's handleListAssets,
// which served the editor and didn't need them) because the v2 sim
// engine consumes both — world_rotation.go drives daily state rotation
// off RotationAlgo, world_phase.go staggers flips by TransitionSpreadSeconds,
// and cascade/npc_route.go gates route candidates on RotationAlgo. Without
// them those systems silently get zero-values. occupied_min_count /
// occupied_night_only feed the (still-stubbed) occupancy reactor.
//
// interior, source_file, and created_at are not modeled on sim.Asset.
const loadAllAssetsSQL = `
SELECT id, name, category, default_state, anchor_x, anchor_y, layer,
       pack_id, fits_slot, z_index, is_obstacle, is_passage,
       footprint_left, footprint_right, footprint_top, footprint_bottom,
       door_offset_x, door_offset_y, visible_when_inside,
       stand_offset_x, stand_offset_y,
       rotation_algo, transition_spread_seconds,
       occupied_min_count, occupied_night_only
  FROM asset`

// loadAllStatesSQL pulls every asset_state in one pass, LEFT JOINing
// asset_state_light so lit states carry their PointLight2D params inline
// (most rows come back with NULL light columns) and aggregating tags via
// array_agg so each state ships with its full tag set in one trip. The
// FILTER drops the NULL element array_agg would emit for states without
// tags. The inner ORDER BY t.tag makes the aggregated tag slice
// deterministic — without it array_agg order follows the query plan, so a
// SIGHUP hot-reload could reorder the slice and break anything that
// serializes / hashes / compares it. The outer ORDER BY asset_id, id
// gives a stable per-asset slice order keyed on the AssetStateID —
// StateForTag/RotatablePool both rely on ID order.
const loadAllStatesSQL = `
SELECT s.asset_id, s.id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h,
       s.frame_count, s.frame_rate,
       l.color, l.radius, l.energy, l.offset_x, l.offset_y,
       l.flicker_amplitude, l.flicker_period_ms,
       COALESCE(array_agg(t.tag ORDER BY t.tag) FILTER (WHERE t.tag IS NOT NULL), '{}')
  FROM asset_state s
  LEFT JOIN asset_state_light l ON l.state_id = s.id
  LEFT JOIN asset_state_tag t ON t.state_id = s.id
 GROUP BY s.id, l.state_id
 ORDER BY s.asset_id, s.id`

// loadAllSlotsSQL pulls every named attachment point.
const loadAllSlotsSQL = `
SELECT asset_id, slot_name, offset_x, offset_y
  FROM asset_slot
 ORDER BY asset_id, slot_name`

// loadAllRefreshDefaultsSQL pulls every asset_refresh_default row (LLM-363) — the
// per-asset TEMPLATE seeded onto new placements. No last_refresh_at (a template has
// no regen anchor) and no snapshot_gen (reference data, not checkpointed). Ordered
// by id so an asset's default rows load in a stable order.
const loadAllRefreshDefaultsSQL = `
SELECT asset_id, attribute, amount,
       max_quantity, available_quantity,
       refresh_mode, refresh_period_hours,
       dwell_amount, dwell_period_minutes, gather_item
  FROM asset_refresh_default
 ORDER BY asset_id, id`

// LoadAll assembles the asset catalog: load packs, load the asset rows
// (attaching the Pack pointer), then attach states (with light + tags),
// slots, and refresh-default templates to their parent. Five queries, no N+1.
//
// Runs against the pool directly (no Tx) — read-only restart path, same
// posture as the other read-side repos.
//
// Orphan handling differs from the item_kind/recipe repos: asset_state
// and asset_slot reference asset.id by a UNIQUE constraint only (no FK,
// so no ON DELETE CASCADE), and asset.pack_id has no FK to tileset_pack.
// An orphan child row or dangling pack ref is therefore reachable in prod
// data (e.g. an asset deleted without its states), so those are skipped
// with a logged warning rather than failing engine startup. The asset
// map itself keeps a loud duplicate-key guard (asset.id is the PK).
func (r *AssetsRepo) LoadAll(ctx context.Context) (map[sim.AssetID]*sim.Asset, error) {
	packs, err := r.loadPacks(ctx)
	if err != nil {
		return nil, err
	}
	assets, err := r.loadAssets(ctx, packs)
	if err != nil {
		return nil, err
	}
	if err := r.attachStates(ctx, assets); err != nil {
		return nil, err
	}
	if err := r.attachSlots(ctx, assets); err != nil {
		return nil, err
	}
	if err := r.attachRefreshDefaults(ctx, assets); err != nil {
		return nil, err
	}
	return assets, nil
}

// loadPacks reads tileset_pack into TilesetPack values keyed by id.
func (r *AssetsRepo) loadPacks(ctx context.Context) (map[string]*sim.TilesetPack, error) {
	rows, err := r.pool.Query(ctx, loadAllPacksSQL)
	if err != nil {
		return nil, fmt.Errorf("pg assets LoadAll: tileset_pack query: %w", err)
	}
	defer rows.Close()

	packs := make(map[string]*sim.TilesetPack)
	for rows.Next() {
		var p sim.TilesetPack
		if err := rows.Scan(&p.ID, &p.Name, &p.URL); err != nil {
			return nil, fmt.Errorf("pg assets LoadAll: tileset_pack scan: %w", err)
		}
		if _, exists := packs[p.ID]; exists {
			return nil, fmt.Errorf("pg assets LoadAll: duplicate tileset_pack %q", p.ID)
		}
		packs[p.ID] = &p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg assets LoadAll: tileset_pack iter: %w", err)
	}
	return packs, nil
}

// loadAssets reads the asset rows into the keyed map, attaching the Pack
// pointer when pack_id resolves. Nullable columns (pack_id, fits_slot,
// door/stand offsets) scan straight into the struct's pointer fields.
func (r *AssetsRepo) loadAssets(ctx context.Context, packs map[string]*sim.TilesetPack) (map[sim.AssetID]*sim.Asset, error) {
	rows, err := r.pool.Query(ctx, loadAllAssetsSQL)
	if err != nil {
		return nil, fmt.Errorf("pg assets LoadAll: asset query: %w", err)
	}
	defer rows.Close()

	assets := make(map[sim.AssetID]*sim.Asset)
	for rows.Next() {
		var (
			id string
			a  sim.Asset
		)
		if err := rows.Scan(
			&id, &a.Name, &a.Category, &a.DefaultState, &a.AnchorX, &a.AnchorY, &a.Layer,
			&a.PackID, &a.FitsSlot, &a.ZIndex, &a.IsObstacle, &a.IsPassage,
			&a.FootprintLeft, &a.FootprintRight, &a.FootprintTop, &a.FootprintBottom,
			&a.DoorOffsetX, &a.DoorOffsetY, &a.VisibleWhenInside,
			&a.StandOffsetX, &a.StandOffsetY,
			&a.RotationAlgo, &a.TransitionSpreadSeconds,
			&a.OccupiedMinCount, &a.OccupiedNightOnly,
		); err != nil {
			return nil, fmt.Errorf("pg assets LoadAll: asset scan: %w", err)
		}
		a.ID = sim.AssetID(id)
		a.States = []sim.AssetState{}
		a.Slots = []sim.AssetSlot{}
		if a.PackID != nil {
			if pack, ok := packs[*a.PackID]; ok {
				a.Pack = pack
			} else {
				// No FK on asset.pack_id; a dangling ref is reachable.
				// Leave Pack nil (the in-memory model tolerates it) and
				// keep PackID as stored, but log the drift.
				log.Printf("pg assets LoadAll: asset %s references unknown tileset_pack %q — leaving Pack unset", id, *a.PackID)
			}
		}
		// id is the asset PK so a duplicate is unreachable in valid data;
		// guard loudly against schema drift rather than letting a later
		// row silently win.
		if _, exists := assets[a.ID]; exists {
			return nil, fmt.Errorf("pg assets LoadAll: duplicate asset %q", id)
		}
		assets[a.ID] = &a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg assets LoadAll: asset iter: %w", err)
	}
	return assets, nil
}

// attachStates reads asset_state (+ light + tags) and appends each state
// to its parent asset. Orphan states (asset_id not in the map) are
// skipped with a warning — see LoadAll's orphan note.
func (r *AssetsRepo) attachStates(ctx context.Context, assets map[sim.AssetID]*sim.Asset) error {
	rows, err := r.pool.Query(ctx, loadAllStatesSQL)
	if err != nil {
		return fmt.Errorf("pg assets LoadAll: asset_state query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			assetID string
			stateID int
			s       sim.AssetState
			// Light columns are NULL for non-lit states (LEFT JOIN).
			lightColor                 *string
			lightRadius                *int
			lightEnergy                *float64
			lightOffsetX, lightOffsetY *int
			lightFlickerAmp            *float64
			lightFlickerPeriod         *int
		)
		if err := rows.Scan(
			&assetID, &stateID, &s.State, &s.Sheet,
			&s.SrcX, &s.SrcY, &s.SrcW, &s.SrcH, &s.FrameCount, &s.FrameRate,
			&lightColor, &lightRadius, &lightEnergy,
			&lightOffsetX, &lightOffsetY,
			&lightFlickerAmp, &lightFlickerPeriod,
			&s.Tags,
		); err != nil {
			return fmt.Errorf("pg assets LoadAll: asset_state scan: %w", err)
		}
		s.ID = sim.AssetStateID(stateID)
		if s.Tags == nil {
			s.Tags = []string{}
		}
		// Light presence is the discriminant: lit states have a row in
		// asset_state_light, so a non-NULL color means the whole tuple is
		// present (every light column is NOT NULL within that table). The
		// partial-row guard turns a schema-drift / partial-migration case
		// into a loud error rather than a nil-pointer panic on deref.
		if lightColor != nil {
			if lightRadius == nil || lightEnergy == nil || lightOffsetX == nil || lightOffsetY == nil ||
				lightFlickerAmp == nil || lightFlickerPeriod == nil {
				return fmt.Errorf("pg assets LoadAll: asset_state %d has a partial asset_state_light row (color set, other columns NULL)", stateID)
			}
			s.Light = &sim.AssetLight{
				Color:            *lightColor,
				Radius:           *lightRadius,
				Energy:           *lightEnergy,
				OffsetX:          *lightOffsetX,
				OffsetY:          *lightOffsetY,
				FlickerAmplitude: *lightFlickerAmp,
				FlickerPeriodMs:  *lightFlickerPeriod,
			}
		}
		a, ok := assets[sim.AssetID(assetID)]
		if !ok {
			log.Printf("pg assets LoadAll: asset_state %d references unknown asset %s — skipping", stateID, assetID)
			continue
		}
		a.States = append(a.States, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg assets LoadAll: asset_state iter: %w", err)
	}
	return nil
}

// attachSlots reads asset_slot and appends each slot to its parent asset.
// Orphan slots are skipped with a warning — see LoadAll's orphan note.
func (r *AssetsRepo) attachSlots(ctx context.Context, assets map[sim.AssetID]*sim.Asset) error {
	rows, err := r.pool.Query(ctx, loadAllSlotsSQL)
	if err != nil {
		return fmt.Errorf("pg assets LoadAll: asset_slot query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			assetID string
			slot    sim.AssetSlot
		)
		if err := rows.Scan(&assetID, &slot.SlotName, &slot.OffsetX, &slot.OffsetY); err != nil {
			return fmt.Errorf("pg assets LoadAll: asset_slot scan: %w", err)
		}
		a, ok := assets[sim.AssetID(assetID)]
		if !ok {
			log.Printf("pg assets LoadAll: asset_slot %q references unknown asset %s — skipping", slot.SlotName, assetID)
			continue
		}
		a.Slots = append(a.Slots, slot)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg assets LoadAll: asset_slot iter: %w", err)
	}
	return nil
}

// attachRefreshDefaults reads asset_refresh_default and appends each row to its
// parent asset's RefreshDefaults slice (LLM-363). Orphan rows (asset_id not in the
// map) are skipped with a warning — see LoadAll's orphan note; the FK to asset(id)
// ON DELETE CASCADE makes it unreachable from valid writes. Column mapping mirrors
// loadAllRefreshes (village_objects.go): a NULL attribute / gather_item maps to the
// empty in-memory value, and refresh_mode round-trips as stored ('continuous' on an
// infinite row, inert since regen ignores an untracked supply).
func (r *AssetsRepo) attachRefreshDefaults(ctx context.Context, assets map[sim.AssetID]*sim.Asset) error {
	rows, err := r.pool.Query(ctx, loadAllRefreshDefaultsSQL)
	if err != nil {
		return fmt.Errorf("pg assets LoadAll: asset_refresh_default query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			assetID        string
			attribute      *string // nullable: NULL on a yield-only row
			amount         int
			maxQty         *int
			availableQty   *int
			refreshMode    *string
			periodHours    *int
			dwellDelta     *int
			dwellPeriodMin *int
			gatherItem     *string
		)
		if err := rows.Scan(
			&assetID, &attribute, &amount,
			&maxQty, &availableQty,
			&refreshMode, &periodHours,
			&dwellDelta, &dwellPeriodMin, &gatherItem,
		); err != nil {
			return fmt.Errorf("pg assets LoadAll: asset_refresh_default scan: %w", err)
		}
		attr := ""
		if attribute != nil {
			attr = *attribute
		}
		mode := ""
		if refreshMode != nil {
			mode = *refreshMode
		}
		// An infinite (untracked-supply) row carries no regen schedule. object_refresh
		// stores 'continuous' on it to satisfy its NOT NULL column, but the canonical
		// in-memory shape ValidateObjectRefreshes accepts is an EMPTY mode, so
		// normalize it back (mirrors the editor's infinite-row mode normalization).
		// Without this the per-asset validation below would reject a valid template.
		if availableQty == nil {
			mode = ""
		}
		gather := ""
		if gatherItem != nil {
			gather = *gatherItem
		}
		a, ok := assets[sim.AssetID(assetID)]
		if !ok {
			log.Printf("pg assets LoadAll: asset_refresh_default references unknown asset %s — skipping", assetID)
			continue
		}
		a.RefreshDefaults = append(a.RefreshDefaults, &sim.ObjectRefresh{
			Attribute:          sim.NeedKey(attr),
			Amount:             amount,
			GatherItem:         sim.ItemKind(gather),
			AvailableQuantity:  availableQty,
			MaxQuantity:        maxQty,
			RefreshMode:        sim.RefreshMode(mode),
			RefreshPeriodHours: periodHours,
			DwellDelta:         dwellDelta,
			DwellPeriodMinutes: dwellPeriodMin,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg assets LoadAll: asset_refresh_default iter: %w", err)
	}
	// Validate each asset's assembled template against the authoring invariants
	// (ValidateObjectRefreshes) — the DB CHECKs cover structure but not e.g. an
	// unknown need name. A drifted / hand-edited template would otherwise seed
	// invalid rows onto every future placement. On failure, drop that asset's
	// defaults with a loud warning (skip-and-warn, this file's orphan posture) so
	// one bad template can't crash engine boot — those placements just fall back to
	// inert until the template is re-authored.
	for id, a := range assets {
		if len(a.RefreshDefaults) == 0 {
			continue
		}
		if err := sim.ValidateObjectRefreshes(a.RefreshDefaults); err != nil {
			log.Printf("pg assets LoadAll: dropping invalid asset_refresh_default template for asset %s: %v", id, err)
			a.RefreshDefaults = nil
		}
	}
	return nil
}

// --- Asset-geometry editor writes (LLM-263) ---------------------------------
//
// The durable half of the door / footprint / stand editor marker drags. Assets
// are otherwise load-only reference data (no checkpoint path), so these direct
// UPDATEs are the source of truth the catalog rebuilds from on restart — the
// same posture as item_kind / recipe / item_satisfies. The httpapi handler runs
// the in-memory SetAsset* command (mutate + WS broadcast) and then calls one of
// these to persist. Each targets one asset by id and touches only its own
// geometry columns.
//
// A zero-rows result means the id is absent from the asset table — a catalog/DB
// drift (the in-memory command already resolved the asset), surfaced as
// sim.ErrAssetNotFound rather than a silent no-op.

const updateAssetDoorSQL = `
UPDATE asset SET door_offset_x = $2, door_offset_y = $3 WHERE id = $1`

const updateAssetFootprintSQL = `
UPDATE asset
   SET footprint_left = $2, footprint_right = $3,
       footprint_top = $4, footprint_bottom = $5
 WHERE id = $1`

const updateAssetStandSQL = `
UPDATE asset SET stand_offset_x = $2, stand_offset_y = $3 WHERE id = $1`

// UpdateAssetDoorOffset persists the per-asset door tile offset. x/y are nil to
// clear the door (the columns are nullable). Returns sim.ErrAssetNotFound when
// no asset row has id.
func (r *AssetsRepo) UpdateAssetDoorOffset(ctx context.Context, id sim.AssetID, x, y *int) error {
	tag, err := r.pool.Exec(ctx, updateAssetDoorSQL, string(id), x, y)
	if err != nil {
		return fmt.Errorf("pg assets UpdateAssetDoorOffset: exec: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return sim.ErrAssetNotFound
	}
	return nil
}

// UpdateAssetFootprint persists the per-asset footprint tile counts. Callers
// pass non-negative sides (the asset table CHECKs footprint_* >= 0; the command
// validates before this runs). Returns sim.ErrAssetNotFound when no asset row
// has id.
func (r *AssetsRepo) UpdateAssetFootprint(ctx context.Context, id sim.AssetID, left, right, top, bottom int) error {
	tag, err := r.pool.Exec(ctx, updateAssetFootprintSQL, string(id), left, right, top, bottom)
	if err != nil {
		return fmt.Errorf("pg assets UpdateAssetFootprint: exec: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return sim.ErrAssetNotFound
	}
	return nil
}

// UpdateAssetStandOffset persists the per-asset inside-a-structure render offset.
// x/y are nil to clear it (the columns are nullable). Returns sim.ErrAssetNotFound
// when no asset row has id.
func (r *AssetsRepo) UpdateAssetStandOffset(ctx context.Context, id sim.AssetID, x, y *int) error {
	tag, err := r.pool.Exec(ctx, updateAssetStandSQL, string(id), x, y)
	if err != nil {
		return fmt.Errorf("pg assets UpdateAssetStandOffset: exec: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return sim.ErrAssetNotFound
	}
	return nil
}

// --- Asset refresh-default editor writes (LLM-363) ---------------------------
//
// The durable half of the set-refresh-default editor write. Like the geometry
// writes above, asset_refresh_default is reference data with no checkpoint path, so
// this direct write is the edit's source of truth on restart. The httpapi handler
// runs the in-memory SetAssetRefreshDefaults command (mutate World.Assets) and then
// calls this to persist. The set replaces the asset's template WHOLESALE in one
// transaction (DELETE all, then INSERT each row) so a partial failure can't leave a
// half-applied template.

const deleteAssetRefreshDefaultsSQL = `DELETE FROM asset_refresh_default WHERE asset_id = $1`

const insertAssetRefreshDefaultSQL = `
INSERT INTO asset_refresh_default (
    asset_id, attribute, amount, available_quantity, max_quantity,
    refresh_mode, refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

// UpdateAssetRefreshDefaults persists an asset's refresh-default template, replacing
// the whole set atomically. rows is the applied set from the SetAssetRefreshDefaults
// command (already validated + supply-normalized); a nil/empty rows clears the
// asset's defaults. Column mapping mirrors the object_refresh checkpoint upsert: an
// empty attribute / gather_item persists as NULL (LLM-264), and an empty
// (infinite-row) refresh_mode persists as 'continuous' to satisfy the NOT NULL +
// mode CHECK. Wrapped in a Tx so the delete + inserts land atomically — a mid-set
// failure rolls back rather than leaving a partial template.
func (r *AssetsRepo) UpdateAssetRefreshDefaults(ctx context.Context, id sim.AssetID, rows []*sim.ObjectRefresh) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg assets UpdateAssetRefreshDefaults: begin: %w", err)
	}
	// Rollback is a no-op after a successful Commit; safe to always defer.
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, deleteAssetRefreshDefaultsSQL, string(id)); err != nil {
		return fmt.Errorf("pg assets UpdateAssetRefreshDefaults: delete: %w", err)
	}
	for _, ref := range rows {
		if ref == nil {
			continue
		}
		// Mirror the object_refresh upsert's NULL/mode handling (village_objects.go).
		modeArg := string(ref.RefreshMode)
		if modeArg == "" {
			modeArg = string(sim.RefreshModeContinuous)
		}
		var attrArg any
		if ref.Attribute != "" {
			attrArg = string(ref.Attribute)
		}
		var gatherArg any
		if ref.GatherItem != "" {
			gatherArg = string(ref.GatherItem)
		}
		if _, err := tx.Exec(ctx, insertAssetRefreshDefaultSQL,
			string(id),             // $1 asset_id
			attrArg,                // $2 attribute (nullable)
			ref.Amount,             // $3 amount
			ref.AvailableQuantity,  // $4 available_quantity (nullable)
			ref.MaxQuantity,        // $5 max_quantity (nullable)
			modeArg,                // $6 refresh_mode
			ref.RefreshPeriodHours, // $7 refresh_period_hours (nullable)
			ref.DwellDelta,         // $8 dwell_amount (nullable; prod col name)
			ref.DwellPeriodMinutes, // $9 dwell_period_minutes (nullable)
			gatherArg,              // $10 gather_item (nullable)
		); err != nil {
			return fmt.Errorf("pg assets UpdateAssetRefreshDefaults: insert asset=%s: %w", id, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg assets UpdateAssetRefreshDefaults: commit: %w", err)
	}
	return nil
}
