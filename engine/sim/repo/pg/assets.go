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

// LoadAll assembles the asset catalog: load packs, load the asset rows
// (attaching the Pack pointer), then attach states (with light + tags)
// and slots to their parent. Four queries, no N+1.
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
