package sim

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Fence runs (LLM-637) — the authoring command behind the editor's drag-to-fence
// tool, and the canonical place a fence is placed for whatever later lets a
// player buy and lay fencing.
//
// A fence asset is an ordinary obstacle asset whose STATES are the pieces of a
// 16x16 fence sheet (one cell = one world tile at object render scale 2.0),
// each state tagged with the role it plays. Piece selection lives here, not in
// the client: the client sends the two corners of a drag in world pixels, the
// engine floors them to tiles, derives the shape, picks a tagged state per tile,
// validates every tile, and mints the segments as one world command. A closed
// ring of segments is a sealed pen for every walker with no new pathfinding
// mechanism — pathfind.go stamps each obstacle tile impassable and A* is
// 4-connected. Gates are separate placements (the pack's gate assets), dropped
// by hand into a gap the operator opens with a single-segment delete.
//
// THE TAG, NOT THE STATE NAME, IS THE CONTRACT (as with berry growth stages):
// pieces resolve through Asset.StateForTag. A sheet whose pen template is laid
// out differently authors its own cells against the same tags.

// Fence piece tags. A state may carry several where one cell serves several
// roles (the ranch sheet's bottom-left corner is also a line's left end).
const (
	TagFenceCornerTL = "fence-corner-tl"
	TagFenceCornerTR = "fence-corner-tr"
	TagFenceCornerBL = "fence-corner-bl"
	TagFenceCornerBR = "fence-corner-br"
	TagFenceH        = "fence-h"        // horizontal mid: post with rails both sides
	TagFenceV        = "fence-v"        // vertical mid: the side edges of a pen
	TagFenceVTop     = "fence-v-top"    // free-standing vertical line, top end
	TagFenceVBottom  = "fence-v-bottom" // free-standing vertical line, bottom end
	TagFenceEndLeft  = "fence-end-left" // free-standing horizontal line, left end
	TagFenceEndRight = "fence-end-right"
	TagFencePost     = "fence-post" // a 1x1 placement: a lone post
)

// FenceRunTagPrefix prefixes the per-instance object tag every segment of one
// run carries ("fence-run:<id>"), so a pen can be removed as a unit
// (DeleteFenceRun) and the editor can tell a fence segment from any other
// obstacle. The id is fresh per PlaceFenceRun.
const FenceRunTagPrefix = "fence-run:"

// MaxFenceRunSegments bounds one placement. A 60x60 pen is 236 segments; the cap
// exists so a runaway drag across the map cannot mint thousands of objects in a
// single command, not to size pens.
const MaxFenceRunSegments = 400

// ErrFenceAssetUnsupported: the asset has no state carrying a tag the requested
// shape needs — it is not a fence asset, or its sheet was authored incomplete.
var ErrFenceAssetUnsupported = errors.New("asset has no fence piece state for the requested shape")

// ErrFenceRunTooLarge: the drag would mint more than MaxFenceRunSegments.
var ErrFenceRunTooLarge = errors.New("fence run too large")

// ErrFenceTileBlocked: a ring tile is not fenceable — out of bounds, water,
// inside an obstacle footprint, or already fenced. Wrapped by FenceTileBlocked
// so the caller can name the tile; match with errors.Is.
var ErrFenceTileBlocked = errors.New("fence tile blocked")

// FenceTileBlocked carries the first offending tile of a refused run. Tile is the
// padded grid tile; X/Y are its world-pixel top-left, which is what the client
// understands.
type FenceTileBlocked struct {
	Tile TilePos
	X, Y float64
}

func (e *FenceTileBlocked) Error() string {
	return fmt.Sprintf("%v at tile (%d,%d)", ErrFenceTileBlocked, e.Tile.X, e.Tile.Y)
}

func (e *FenceTileBlocked) Unwrap() error { return ErrFenceTileBlocked }

// fenceSegment is one tile of a planned run and the piece tag it takes.
type fenceSegment struct {
	Tile TilePos
	Tag  string
}

// fenceRunSize is the number of segments fenceRunSegments would lay over the
// inclusive rectangle [a,b] — 1 for a post, w or h for a line, the ring's
// perimeter otherwise — computed from the dimensions so the cap can be applied
// without building the run.
func fenceRunSize(a, b TilePos) int {
	w := a.X - b.X
	if w < 0 {
		w = -w
	}
	h := a.Y - b.Y
	if h < 0 {
		h = -h
	}
	w++
	h++
	switch {
	case w == 1 && h == 1:
		return 1
	case h == 1:
		return w
	case w == 1:
		return h
	default:
		return 2*w + 2*h - 4
	}
}

// fenceRunSegments lays out a run over the inclusive tile rectangle [a,b]
// (either corner order) and returns the ring tiles in row-major order, each
// with its piece tag. The shape decides the vocabulary:
//
//	1x1  → a lone post
//	Nx1  → a horizontal line: end-left, h…, end-right
//	1xN  → a vertical line: v-top, v…, v-bottom
//	NxM  → the ring: corners, h along the top and bottom, v down the sides
//
// The interior of a rectangle is untouched. Pure: no world access, so the
// client's preview can mirror it exactly.
func fenceRunSegments(a, b TilePos) []fenceSegment {
	minX, maxX := a.X, b.X
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := a.Y, b.Y
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	w := maxX - minX + 1
	h := maxY - minY + 1

	var out []fenceSegment
	add := func(x, y int, tag string) {
		out = append(out, fenceSegment{Tile: TilePos{X: x, Y: y}, Tag: tag})
	}
	switch {
	case w == 1 && h == 1:
		add(minX, minY, TagFencePost)
	case h == 1:
		add(minX, minY, TagFenceEndLeft)
		for x := minX + 1; x < maxX; x++ {
			add(x, minY, TagFenceH)
		}
		add(maxX, minY, TagFenceEndRight)
	case w == 1:
		add(minX, minY, TagFenceVTop)
		for y := minY + 1; y < maxY; y++ {
			add(minX, y, TagFenceV)
		}
		add(minX, maxY, TagFenceVBottom)
	default:
		add(minX, minY, TagFenceCornerTL)
		for x := minX + 1; x < maxX; x++ {
			add(x, minY, TagFenceH)
		}
		add(maxX, minY, TagFenceCornerTR)
		for y := minY + 1; y < maxY; y++ {
			add(minX, y, TagFenceV)
			add(maxX, y, TagFenceV)
		}
		add(minX, maxY, TagFenceCornerBL)
		for x := minX + 1; x < maxX; x++ {
			add(x, maxY, TagFenceH)
		}
		add(maxX, maxY, TagFenceCornerBR)
	}
	return out
}

// fenceSegmentPos is where a segment's anchor goes so the 16x16 piece, drawn at
// render scale 2.0 with the asset's anchor, fills its tile exactly: the tile's
// world-pixel origin plus the anchor fraction of a tile. With the standing
// anchor (0.5, 0.85) that is (+16, +27.2), and WorldPos.Tile() floors back to
// the same tile — which is what the obstacle stamp keys on. An anchor_y of 1.0
// would land on the next tile's top edge, so the asset is authored below it.
func fenceSegmentPos(t TilePos, asset *Asset) WorldPos {
	origin := WorldPos{
		X: float64(t.X-PadX) * TileSize,
		Y: float64(t.Y-PadY) * TileSize,
	}
	return WorldPos{
		X: origin.X + asset.AnchorX*TileSize,
		Y: origin.Y + asset.AnchorY*TileSize,
	}
}

// fenceCornerTile converts one drag corner from world pixels to its padded
// grid tile, refusing a corner off the grid with *FenceTileBlocked. The
// pixel range is checked BEFORE the float→int conversion in WorldPos.Tile():
// a finite value beyond the int range converts implementation-specifically in
// Go, so a tile-side bounds check alone is not a reliable guard for a direct
// engine caller. Off-grid corners report the clamped pixel position rather
// than a tile, since there is no tile to name.
func fenceCornerTile(x, y float64) (TilePos, error) {
	minX := -float64(PadX) * TileSize
	maxX := float64(MapW-PadX) * TileSize
	minY := -float64(PadY) * TileSize
	maxY := float64(MapH-PadY) * TileSize
	if x < minX || x >= maxX || y < minY || y >= maxY {
		return TilePos{}, &FenceTileBlocked{
			Tile: TilePos{X: -1, Y: -1},
			X:    math.Max(minX, math.Min(x, maxX)),
			Y:    math.Max(minY, math.Min(y, maxY)),
		}
	}
	return WorldPos{X: x, Y: y}.Tile(), nil
}

// PlaceFenceRunResult is the outcome of a PlaceFenceRun command: the run's id
// (the value after FenceRunTagPrefix on every segment) and the segments in
// row-major order.
type PlaceFenceRunResult struct {
	RunID   string
	Objects []*VillageObject
}

// PlaceFenceRun returns a Command that lays a fence run of the given asset over
// the tile rectangle spanned by two world-pixel corners (x1,y1)-(x2,y2), any
// corner order. Validation is complete before any mutation, so a refused run
// places nothing: ErrUnknownAsset; ErrInvalidObjectPosition (non-finite
// coords); ErrFenceAssetUnsupported (the asset is not an obstacle, or lacks a
// tagged state the shape needs); ErrFenceRunTooLarge (sized from the corners
// before anything is laid out); or a *FenceTileBlocked (wrapping
// ErrFenceTileBlocked) naming the first tile that is not fenceable — a corner
// off the grid, or a ring tile that is not walkable on the standard walk grid
// (water, inside an obstacle footprint, already fenced). Every segment is minted through the shared placement path
// (placeVillageObject) with its piece state, tagged FenceRunTagPrefix+RunID,
// and announced with VillageObjectCreated then VillageObjectTagsUpdated so a
// live client holds the tag without a reload.
func PlaceFenceRun(assetID AssetID, x1, y1, x2, y2 float64, placedBy string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			for _, v := range []float64{x1, y1, x2, y2} {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return nil, ErrInvalidObjectPosition
				}
			}
			asset, ok := w.Assets[assetID]
			if !ok || asset == nil {
				return nil, ErrUnknownAsset
			}
			// A fence that does not block is not a fence: the sealed-pen property
			// rides on the obstacle stamp, so a tagged-but-passable asset is
			// refused rather than placed as decoration that looks like a fence.
			if !asset.IsObstacle {
				return nil, fmt.Errorf("%w: asset is not an obstacle", ErrFenceAssetUnsupported)
			}
			// Bound the corners and size the run BEFORE laying it out, so a
			// caller handing in huge finite coordinates (this command is the
			// validation boundary, not the HTTP route) cannot make
			// fenceRunSegments allocate an enormous slice or overflow the
			// width/height arithmetic. A corner off the grid is a blocked tile
			// — the same answer the per-tile walk check would give.
			a, err := fenceCornerTile(x1, y1)
			if err != nil {
				return nil, err
			}
			b, err := fenceCornerTile(x2, y2)
			if err != nil {
				return nil, err
			}
			if fenceRunSize(a, b) > MaxFenceRunSegments {
				return nil, ErrFenceRunTooLarge
			}
			segments := fenceRunSegments(a, b)
			states := make(map[string]string, 8)
			for _, seg := range segments {
				if _, seen := states[seg.Tag]; seen {
					continue
				}
				st := asset.StateForTag(seg.Tag)
				if st == nil {
					return nil, fmt.Errorf("%w: missing %s", ErrFenceAssetUnsupported, seg.Tag)
				}
				states[seg.Tag] = st.State
			}
			grid, err := buildWalkGrid(w)
			if err != nil {
				return nil, err
			}
			for _, seg := range segments {
				if !grid.CanWalk(seg.Tile.X, seg.Tile.Y) {
					return nil, &FenceTileBlocked{
						Tile: seg.Tile,
						X:    float64(seg.Tile.X-PadX) * TileSize,
						Y:    float64(seg.Tile.Y-PadY) * TileSize,
					}
				}
			}

			runID := newUUIDv4()
			runTag := FenceRunTagPrefix + runID
			now := time.Now().UTC()
			objects := make([]*VillageObject, 0, len(segments))
			for _, seg := range segments {
				obj := placeVillageObject(w, assetID, asset, fenceSegmentPos(seg.Tile, asset), "", placedBy, states[seg.Tag])
				obj.Tags = append(obj.Tags, runTag)
				w.emit(&VillageObjectTagsUpdated{
					ObjectID: obj.ID,
					Tags:     append([]string(nil), obj.Tags...),
					At:       now,
				})
				objects = append(objects, obj)
			}
			return PlaceFenceRunResult{RunID: runID, Objects: objects}, nil
		},
	}
}

// FenceRunIDOf returns the run id carried by a fence segment's tags ("" when
// the object carries no FenceRunTagPrefix tag). Nil-safe.
func FenceRunIDOf(o *VillageObject) string {
	if o == nil {
		return ""
	}
	for _, t := range o.Tags {
		if strings.HasPrefix(t, FenceRunTagPrefix) {
			return strings.TrimPrefix(t, FenceRunTagPrefix)
		}
	}
	return ""
}

// ErrFenceRunNotFound: no object carries the run tag.
var ErrFenceRunNotFound = errors.New("fence run not found")

// DeleteFenceRunResult lists every object removed, children before parents
// (deleteObjectCascade order), in a stable order across segments.
type DeleteFenceRunResult struct {
	DeletedIDs []VillageObjectID
}

// DeleteFenceRun returns a Command that removes every object tagged with the
// run (FenceRunTagPrefix+runID) plus anything attached to those segments, the
// same cascade DeleteVillageObject performs, emitting one VillageObjectDeleted
// per removed object. A segment that was promoted to a structure is refused as
// in DeleteVillageObject (ErrVillageObjectIsStructure) before anything is
// removed. ErrFenceRunNotFound when no object carries the tag — a run already
// deleted, or a bad id.
func DeleteFenceRun(runID string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			runTag := FenceRunTagPrefix + strings.TrimSpace(runID)
			if runTag == FenceRunTagPrefix {
				return nil, ErrFenceRunNotFound
			}
			var ids []VillageObjectID
			for id, obj := range w.VillageObjects {
				if obj != nil && obj.HasTag(runTag) {
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				return nil, ErrFenceRunNotFound
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, id := range ids {
				if _, ok := w.Structures[StructureID(id)]; ok {
					return nil, ErrVillageObjectIsStructure
				}
			}
			now := time.Now().UTC()
			var removed []VillageObjectID
			for _, id := range ids {
				// A segment may already be gone as an overlay of an earlier one.
				if _, ok := w.VillageObjects[id]; !ok {
					continue
				}
				for _, rid := range deleteObjectCascade(w, id) {
					w.emit(&VillageObjectDeleted{ObjectID: rid, At: now})
					removed = append(removed, rid)
				}
			}
			return DeleteFenceRunResult{DeletedIDs: removed}, nil
		},
	}
}
