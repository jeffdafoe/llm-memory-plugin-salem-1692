package sim

import (
	"errors"
	"sort"
	"time"
)

// asset_admin.go — the in-memory half of the asset-geometry editor writes
// (LLM-263): door / footprint / stand marker drags from the Godot editor.
//
// Assets are reference data loaded read-only at startup (repo/pg/assets.go
// LoadAll) with no checkpoint path, so — like item_kind / recipe / item_satisfies
// — the durable write lives in the pg repo (UpdateAsset*, the source of truth the
// catalog rebuilds from on restart) and these commands are the live-catalog half
// the httpapi /api/assets/{id}/{door,footprint,stand} handlers run BEFORE the
// durable write (apply-then-persist — see the handler comment). Each mutates
// World.Assets[id] in place and emits its Asset*Changed event, which the hub
// translates to the asset_* WS frame the editor already consumes
// (event_client.gd) so a co-editing admin's marker refreshes live.
//
// The emitted event and the returned result carry their own copies of the offset
// pointers (copyIntPtr), not the asset's stored pointers — the same
// serialization-boundary discipline translate.go uses for slices, so an event
// consumer (the hub marshals asynchronously) can never observe a pointer that a
// later catalog write might replace.
//
// The mutate-then-emit-then-persist ordering matches the rest of the editor-write
// family (npc/object admin edits broadcast before their persistence lands); the
// only difference is assets persist via an immediate direct write rather than the
// deferred checkpoint sweep.

// ErrAssetNotFound is returned by the SetAsset* commands when no catalog asset has
// the given id. The handler maps it to 404 (the id is a URL path segment).
var ErrAssetNotFound = errors.New("asset not found")

// ErrInvalidFootprint is returned when a footprint side is negative. The asset
// table CHECKs footprint_* >= 0, so validating here turns it into a 400 at the
// command rather than a 500 from the pg write.
var ErrInvalidFootprint = errors.New("footprint sides must be non-negative")

// ErrInvalidDoorOffset / ErrInvalidStandOffset are returned when exactly one of
// x/y is set: a door / stand offset is a coordinate pair, so it is either both
// tiles (a position) or both nil (cleared) — never half. The handler maps them
// to 400.
var (
	ErrInvalidDoorOffset  = errors.New("door offset requires both x and y, or neither")
	ErrInvalidStandOffset = errors.New("stand offset requires both x and y, or neither")
)

// ErrAssetStateNotFound is returned by the asset-state-tag commands when the asset
// exists but has no state with the given name. The handler maps it to 404 (asset id
// and state name are both URL path segments).
var ErrAssetStateNotFound = errors.New("asset state not found")

// AssetDoorOffsetResult / AssetFootprintResult / AssetStandOffsetResult carry the
// post-mutation values back to the handler so it can do the durable pg write and
// build the HTTP response. Pointer fields mirror the nullable columns (nil =
// cleared / unset).
type AssetDoorOffsetResult struct {
	ID AssetID
	X  *int
	Y  *int
}

type AssetFootprintResult struct {
	ID     AssetID
	Left   int
	Right  int
	Top    int
	Bottom int
}

type AssetStandOffsetResult struct {
	ID AssetID
	X  *int
	Y  *int
}

// SetAssetDoorOffset sets (or clears, when x and y are nil) the per-asset door
// tile offset in the live catalog and emits AssetDoorOffsetChanged. x and y must
// both be set or both be nil. Returns ErrAssetNotFound for an unknown id,
// ErrInvalidDoorOffset for a half-set pair.
func SetAssetDoorOffset(id AssetID, x, y *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if (x == nil) != (y == nil) {
				return nil, ErrInvalidDoorOffset
			}
			a, ok := w.Assets[id]
			if !ok || a == nil {
				return nil, ErrAssetNotFound
			}
			a.DoorOffsetX = copyIntPtr(x)
			a.DoorOffsetY = copyIntPtr(y)
			w.emit(&AssetDoorOffsetChanged{
				AssetID: id,
				X:       copyIntPtr(a.DoorOffsetX),
				Y:       copyIntPtr(a.DoorOffsetY),
				At:      time.Now().UTC(),
			})
			return AssetDoorOffsetResult{ID: id, X: copyIntPtr(a.DoorOffsetX), Y: copyIntPtr(a.DoorOffsetY)}, nil
		},
	}
}

// SetAssetFootprint sets the per-asset footprint tile counts in the live catalog
// and emits AssetFootprintChanged. Each side must be non-negative. Returns
// ErrAssetNotFound for an unknown id, ErrInvalidFootprint for a negative side.
func SetAssetFootprint(id AssetID, left, right, top, bottom int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if left < 0 || right < 0 || top < 0 || bottom < 0 {
				return nil, ErrInvalidFootprint
			}
			a, ok := w.Assets[id]
			if !ok || a == nil {
				return nil, ErrAssetNotFound
			}
			a.FootprintLeft = left
			a.FootprintRight = right
			a.FootprintTop = top
			a.FootprintBottom = bottom
			w.emit(&AssetFootprintChanged{
				AssetID: id,
				Left:    left,
				Right:   right,
				Top:     top,
				Bottom:  bottom,
				At:      time.Now().UTC(),
			})
			return AssetFootprintResult{ID: id, Left: left, Right: right, Top: top, Bottom: bottom}, nil
		},
	}
}

// SetAssetStandOffset sets (or clears, when x and y are nil) the per-asset
// inside-a-visible-structure render offset in the live catalog and emits
// AssetStandOffsetChanged. x and y must both be set or both be nil. Returns
// ErrAssetNotFound for an unknown id, ErrInvalidStandOffset for a half-set pair.
func SetAssetStandOffset(id AssetID, x, y *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if (x == nil) != (y == nil) {
				return nil, ErrInvalidStandOffset
			}
			a, ok := w.Assets[id]
			if !ok || a == nil {
				return nil, ErrAssetNotFound
			}
			a.StandOffsetX = copyIntPtr(x)
			a.StandOffsetY = copyIntPtr(y)
			w.emit(&AssetStandOffsetChanged{
				AssetID: id,
				X:       copyIntPtr(a.StandOffsetX),
				Y:       copyIntPtr(a.StandOffsetY),
				At:      time.Now().UTC(),
			})
			return AssetStandOffsetResult{ID: id, X: copyIntPtr(a.StandOffsetX), Y: copyIntPtr(a.StandOffsetY)}, nil
		},
	}
}

// AssetVisibleWhenInsideResult carries the post-mutation flag back to the handler
// for the durable pg write and the HTTP response.
type AssetVisibleWhenInsideResult struct {
	ID                AssetID
	VisibleWhenInside bool
}

// AssetStateTagsResult carries the affected asset id, state name, and the full
// post-mutation tag set (sorted, never nil) back to the handler — the durable write
// keys on (id, state) and the response/WS frame echo the resulting set.
type AssetStateTagsResult struct {
	ID    AssetID
	State string
	Tags  []string
}

// SetAssetVisibleWhenInside sets the per-asset "keep the villager sprite visible
// while inside a structure of this asset" flag in the live catalog (LLM-516) and
// emits AssetVisibleWhenInsideChanged. Returns ErrAssetNotFound for an unknown id.
func SetAssetVisibleWhenInside(id AssetID, visible bool) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Assets[id]
			if !ok || a == nil {
				return nil, ErrAssetNotFound
			}
			a.VisibleWhenInside = visible
			w.emit(&AssetVisibleWhenInsideChanged{
				AssetID:           id,
				VisibleWhenInside: visible,
				At:                time.Now().UTC(),
			})
			return AssetVisibleWhenInsideResult{ID: id, VisibleWhenInside: visible}, nil
		},
	}
}

// AddAssetStateTag adds tag to the named state's tag set in the live catalog
// (LLM-517), idempotent — a tag already present is a no-op — and emits
// AssetStateTagsChanged with the full post-mutation set. Returns ErrAssetNotFound
// for an unknown asset, ErrAssetStateNotFound when the asset has no state with that
// name. Tag-vocabulary validation is the handler's job (the allowed-tag set lives in
// the http layer). The stored slice is kept sorted so the live catalog matches the
// order a reload rebuilds from asset_state_tag.
func AddAssetStateTag(id AssetID, state, tag string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			st, err := findAssetState(w, id, state)
			if err != nil {
				return nil, err
			}
			if !st.HasTag(tag) {
				st.Tags = append(st.Tags, tag)
				sort.Strings(st.Tags)
			}
			return emitStateTags(w, id, state, st), nil
		},
	}
}

// RemoveAssetStateTag removes tag from the named state's tag set in the live catalog
// (LLM-517), a no-op when the pair is absent, and emits AssetStateTagsChanged with
// the full post-mutation set. Returns ErrAssetNotFound / ErrAssetStateNotFound like
// AddAssetStateTag. Remove does NOT validate tag against the vocabulary — a tag since
// dropped from the allowlist must still be removable.
func RemoveAssetStateTag(id AssetID, state, tag string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			st, err := findAssetState(w, id, state)
			if err != nil {
				return nil, err
			}
			if st.HasTag(tag) {
				kept := make([]string, 0, len(st.Tags))
				for _, t := range st.Tags {
					if t != tag {
						kept = append(kept, t)
					}
				}
				st.Tags = kept
			}
			return emitStateTags(w, id, state, st), nil
		},
	}
}

// findAssetState resolves the asset + named state, returning the shared error
// sentinels the two state-tag commands map to 404s.
func findAssetState(w *World, id AssetID, state string) (*AssetState, error) {
	a, ok := w.Assets[id]
	if !ok || a == nil {
		return nil, ErrAssetNotFound
	}
	st := a.FindState(state)
	if st == nil {
		return nil, ErrAssetStateNotFound
	}
	return st, nil
}

// emitStateTags emits AssetStateTagsChanged and builds the command result, each
// with its own non-nil copy of the post-mutation tag set (independent copies so the
// hub's async marshal can never observe a slice a later edit mutates, matching the
// pointer-copy discipline the geometry commands use). A non-nil empty slice marshals
// as [] not null.
func emitStateTags(w *World, id AssetID, state string, st *AssetState) AssetStateTagsResult {
	w.emit(&AssetStateTagsChanged{
		AssetID: id,
		State:   state,
		Tags:    append([]string{}, st.Tags...),
		At:      time.Now().UTC(),
	})
	return AssetStateTagsResult{ID: id, State: state, Tags: append([]string{}, st.Tags...)}
}
