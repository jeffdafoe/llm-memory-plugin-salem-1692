package sim

// asset_refresh_default.go — the in-memory half of the asset-level refresh-default
// TEMPLATE (LLM-363). An asset's RefreshDefaults is the refresh-policy set copied
// onto every NEW placement of that asset (CreateVillageObject → seedRefreshesFrom-
// Defaults), so a forageable / eat-in-place source drops in working instead of
// inert — the admin no longer hand-enters the per-instance REFRESHES panel on each
// drop.
//
// Like the asset geometry writes (asset_admin.go), assets are reference data with
// no checkpoint path, so the durable half lives in the pg repo
// (UpdateAssetRefreshDefaults) and this command is the live-catalog half the httpapi
// set-refresh-default handler runs BEFORE the durable write. Unlike the geometry
// commands it emits NO event: refresh defaults are not client-rendered (the same
// posture as the per-object SetVillageObjectRefreshes command), so a co-editing
// admin needs no live broadcast.

// AssetRefreshDefaultsResult carries the applied template back to the handler so it
// can do the durable write-through and build the HTTP response. Rows is a deep copy
// read off the world goroutine — it never aliases the stored template.
type AssetRefreshDefaultsResult struct {
	ID   AssetID
	Rows []*ObjectRefresh
}

// SetAssetRefreshDefaults replaces an asset's default refresh-policy template in the
// live catalog. Validates the set against the same rules as the per-object route
// (ValidateObjectRefreshes — mirrors the DB CHECK constraints), then normalizes each
// finite row to a full supply so the stored template — and every placement seeded
// from it — represents a pristine source. Returns ErrAssetNotFound for an unknown id
// or ErrInvalidRefresh for a bad row set. Passing an empty set clears the asset's
// defaults (future drops fall back to inert, the pre-LLM-363 behavior).
func SetAssetRefreshDefaults(id AssetID, rows []*ObjectRefresh) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if err := ValidateObjectRefreshes(rows); err != nil {
				return nil, err
			}
			a, ok := w.Assets[id]
			if !ok || a == nil {
				return nil, ErrAssetNotFound
			}
			next := make([]*ObjectRefresh, 0, len(rows))
			for _, r := range rows {
				clone := cloneObjectRefresh(r)
				normalizeDefaultSupply(clone)
				clone.LastRefreshAt = nil // a template carries no regen anchor
				next = append(next, clone)
			}
			a.RefreshDefaults = next

			// Deep copy for the result: the handler reads it off the world goroutine
			// to build the durable write + response, and must not alias stored rows.
			snapshot := make([]*ObjectRefresh, len(next))
			for i, r := range next {
				snapshot[i] = cloneObjectRefresh(r)
			}
			return AssetRefreshDefaultsResult{ID: id, Rows: snapshot}, nil
		},
	}
}

// seedRefreshesFromDefaults returns a fresh Refreshes slice for a newly placed
// object, copied from its asset's default template. Each finite row starts at a FULL
// supply (available = max) with no regen anchor (LastRefreshAt nil — the object earns
// its own on the first regen tick). Returns nil for an asset with no defaults,
// preserving the pre-LLM-363 CreateVillageObject behavior (a refresh-less placement).
func seedRefreshesFromDefaults(defaults []*ObjectRefresh) []*ObjectRefresh {
	if len(defaults) == 0 {
		return nil
	}
	out := make([]*ObjectRefresh, 0, len(defaults))
	for _, d := range defaults {
		if d == nil {
			continue
		}
		clone := cloneObjectRefresh(d)
		normalizeDefaultSupply(clone)
		clone.LastRefreshAt = nil
		out = append(out, clone)
	}
	return out
}

// normalizeDefaultSupply sets a finite row's AvailableQuantity to its MaxQuantity via
// a fresh pointer, so a template — and every placement seeded from it — starts full.
// No-op for an infinite (untracked-supply) row, whose AvailableQuantity stays nil.
func normalizeDefaultSupply(r *ObjectRefresh) {
	if r == nil || r.MaxQuantity == nil {
		return
	}
	full := *r.MaxQuantity
	r.AvailableQuantity = &full
}
