package sim

import (
	"errors"
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
