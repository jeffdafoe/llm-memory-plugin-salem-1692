package sim

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Fence runs (LLM-637, LLM-638) — the authoring command behind the editor's
// drag-to-fence tool, and the canonical place a fence is placed for whatever
// later lets a player buy and lay fencing.
//
// A fence asset is an ordinary obstacle asset whose STATES are the pieces of a
// 16x16 fence sheet (one cell = one world tile at object render scale 2.0),
// each state tagged with the role it plays. Piece selection lives here, not in
// the client: the client sends the two corners of a drag in world pixels, the
// engine floors them to tiles, lays the ring, picks each tile's piece from its
// NEIGHBOURS (LLM-638 — so runs connect: a line drawn onto the end of another
// turns that end into a corner), validates every tile, and mints the segments
// as one world command. A closed ring of segments is a sealed pen for every
// walker with no new pathfinding mechanism — pathfind.go stamps each obstacle
// tile impassable and A* is 4-connected. Gates are separate placements (the
// pack's gate assets), dropped by hand into a gap the operator opens with a
// single-segment delete.
//
// THE TAG, NOT THE STATE NAME, IS THE CONTRACT (as with berry growth stages):
// pieces resolve through Asset.StateForTag. A sheet whose pen template is laid
// out differently authors its own cells against the same tags.
//
// NEIGHBOURS MEAN SAME-ASSET SEGMENTS. A ranch fence does not join a picket
// fence or a stone wall — different art — so the neighbour set for a tile is
// the placed segments of the SAME asset on its four edge-adjacent tiles.

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
	TagFencePost     = "fence-post" // a lone post: no neighbours
)

// fencePieceTags is the full vocabulary a fence asset must carry. With pieces
// chosen by neighbourhood any tag can be needed by any run, so the asset is
// checked for all of them up front rather than per shape.
var fencePieceTags = []string{
	TagFenceCornerTL, TagFenceCornerTR, TagFenceCornerBL, TagFenceCornerBR,
	TagFenceH, TagFenceV, TagFenceVTop, TagFenceVBottom,
	TagFenceEndLeft, TagFenceEndRight, TagFencePost,
}

// FenceRunTagPrefix prefixes the per-instance object tag every segment of one
// run carries ("fence-run:<id>"), so a pen can be removed as a unit
// (DeleteFenceRun) and the editor can tell a fence segment from any other
// obstacle. The id is fresh per PlaceFenceRun. A segment SHARED by several
// runs — the junction post where one line was drawn onto another — carries
// every run's tag, and survives until the last of them is deleted.
const FenceRunTagPrefix = "fence-run:"

// MaxFenceRunSegments bounds one placement. A 60x60 pen is 236 segments; the cap
// exists so a runaway drag across the map cannot mint thousands of objects in a
// single command, not to size pens.
const MaxFenceRunSegments = 400

// ErrFenceAssetUnsupported: the asset is not an obstacle, or lacks a state for
// one of the fence piece tags — it is not a fence asset, or its sheet was
// authored incomplete.
var ErrFenceAssetUnsupported = errors.New("asset is not a complete fence asset")

// ErrFenceRunTooLarge: the drag would mint more than MaxFenceRunSegments.
var ErrFenceRunTooLarge = errors.New("fence run too large")

// ErrFenceRunNothingNew: every tile of the run already holds a segment of this
// asset — the drag retraces existing fence and would mint nothing.
var ErrFenceRunNothingNew = errors.New("fence run adds no segments")

// ErrFenceTileBlocked: a ring tile is not fenceable — off the grid, water,
// inside an obstacle footprint, or held by some other obstacle (a different
// fence asset included). Wrapped by FenceTileBlocked so the caller can name the
// tile; match with errors.Is.
var ErrFenceTileBlocked = errors.New("fence tile blocked")

// ErrFenceRunNotFound: no object carries the run tag.
var ErrFenceRunNotFound = errors.New("fence run not found")

// FenceTileBlocked carries the first offending tile of a refused run. Tile is the
// padded grid tile ((-1,-1) when a corner is off the grid and there is no tile
// to name); X/Y are world pixels — the tile's top-left, or the clamped corner.
type FenceTileBlocked struct {
	Tile TilePos
	X, Y float64
}

func (e *FenceTileBlocked) Error() string {
	return fmt.Sprintf("%v at tile (%d,%d)", ErrFenceTileBlocked, e.Tile.X, e.Tile.Y)
}

func (e *FenceTileBlocked) Unwrap() error { return ErrFenceTileBlocked }

// fencePieceFor is THE piece predicate: which tag a fence tile takes given
// which of its four edge neighbours are fence (same asset). Corners, ends and
// mids all derive from it; a lone run yields exactly the vocabulary a
// rectangle or line would be drawn with by hand. The sheet has no T or cross
// art, so three or four neighbours fall back to the straight piece — E+W
// present reads as a post with rails both sides (`h`), N+S with one side as a
// plain post (`v`); the rails of the side neighbour run into the post, which is
// what a top-down junction looks like anyway. Pure; the client preview mirrors
// it (editor.gd _fence_piece_for, pinned by tests/fence_run_test.gd).
func fencePieceFor(n, e, s, w bool) string {
	switch {
	case e && w:
		return TagFenceH
	case n && s:
		return TagFenceV
	case e && s:
		return TagFenceCornerTL
	case w && s:
		return TagFenceCornerTR
	case e && n:
		return TagFenceCornerBL
	case w && n:
		return TagFenceCornerBR
	case e:
		return TagFenceEndLeft
	case w:
		return TagFenceEndRight
	case s:
		return TagFenceVTop
	case n:
		return TagFenceVBottom
	default:
		return TagFencePost
	}
}

// fenceRunSize is the number of tiles fenceRunTiles would lay over the
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

// fenceRunTiles is the GEOMETRY of a run: the ring tiles of the inclusive
// rectangle [a,b] (either corner order) in row-major order — a single tile, a
// line, or the perimeter with the interior untouched. Pieces are not decided
// here; fencePieceFor does that once the neighbourhood is known. Pure, so the
// client's preview mirrors it exactly.
func fenceRunTiles(a, b TilePos) []TilePos {
	minX, maxX := a.X, b.X
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := a.Y, b.Y
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	var out []TilePos
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			if x == minX || x == maxX || y == minY || y == maxY {
				out = append(out, TilePos{X: x, Y: y})
			}
		}
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

// isFenceAsset reports whether an asset is fence-capable: an obstacle carrying
// the horizontal mid piece. The cheap membership test the delete path and the
// client use; fenceAssetCheck is the full one a placement needs.
func isFenceAsset(asset *Asset) bool {
	return asset != nil && asset.IsObstacle && asset.StateForTag(TagFenceH) != nil
}

// fenceAssetCheck refuses an asset that is not an obstacle (a fence that does
// not block would render as a fence and not seal a pen) or lacks a state for
// any piece tag (with pieces chosen by neighbourhood, any of them can be needed
// by any run).
func fenceAssetCheck(asset *Asset) error {
	if !asset.IsObstacle {
		return fmt.Errorf("%w: asset is not an obstacle", ErrFenceAssetUnsupported)
	}
	for _, tag := range fencePieceTags {
		if asset.StateForTag(tag) == nil {
			return fmt.Errorf("%w: missing %s", ErrFenceAssetUnsupported, tag)
		}
	}
	return nil
}

// fenceAssetTiles indexes the placed segments of one fence asset by tile — the
// neighbour set every piece decision reads. Built per command; a few hundred
// segments against a few hundred objects is nothing.
func fenceAssetTiles(w *World, assetID AssetID) map[TilePos]*VillageObject {
	tiles := make(map[TilePos]*VillageObject)
	for _, obj := range w.VillageObjects {
		if obj == nil || obj.AssetID != assetID {
			continue
		}
		t := obj.Pos.Tile()
		// Two segments on one tile cannot happen through this file (a shared
		// tile is reused, never re-minted); if an out-of-band write managed it,
		// keep the lower id so the choice is stable.
		if prev, dup := tiles[t]; dup && prev.ID < obj.ID {
			continue
		}
		tiles[t] = obj
	}
	return tiles
}

// fencePieceAt resolves the piece for tile t against the occupied set.
func fencePieceAt(occupied map[TilePos]*VillageObject, t TilePos) string {
	_, n := occupied[TilePos{X: t.X, Y: t.Y - 1}]
	_, e := occupied[TilePos{X: t.X + 1, Y: t.Y}]
	_, s := occupied[TilePos{X: t.X, Y: t.Y + 1}]
	_, w := occupied[TilePos{X: t.X - 1, Y: t.Y}]
	return fencePieceFor(n, e, s, w)
}

// fenceNeighbourTiles lists the four edge-adjacent tiles of t.
func fenceNeighbourTiles(t TilePos) [4]TilePos {
	return [4]TilePos{
		{X: t.X, Y: t.Y - 1}, {X: t.X + 1, Y: t.Y}, {X: t.X, Y: t.Y + 1}, {X: t.X - 1, Y: t.Y},
	}
}

// refenceTiles re-resolves the piece of every placed segment on the given
// tiles against the occupied set, flipping state (setVillageObjectStateInline →
// VillageObjectStateChanged) only where it changed. A tag with no state on the
// asset leaves the segment as it is (cannot happen for an asset that passed
// fenceAssetCheck). Tiles with no segment are skipped. Returns the ids flipped.
func refenceTiles(w *World, asset *Asset, occupied map[TilePos]*VillageObject, tiles []TilePos) []VillageObjectID {
	var flipped []VillageObjectID
	seen := make(map[TilePos]bool, len(tiles))
	for _, t := range tiles {
		if seen[t] {
			continue
		}
		seen[t] = true
		obj, ok := occupied[t]
		if !ok || obj == nil {
			continue
		}
		st := asset.StateForTag(fencePieceAt(occupied, t))
		if st == nil || obj.CurrentState == st.State {
			continue
		}
		setVillageObjectStateInline(w, obj, st.State)
		flipped = append(flipped, obj.ID)
	}
	return flipped
}

// refenceAroundRemoved re-resolves the surviving same-asset neighbours of tiles
// whose segments were just removed — a detached line gets its end cap back, a
// broken ring its corners. Called by DeleteFenceRun and DeleteVillageObject
// after the removal.
func refenceAroundRemoved(w *World, asset *Asset, removed []TilePos) {
	if asset == nil || !isFenceAsset(asset) {
		return
	}
	occupied := fenceAssetTiles(w, asset.ID)
	var around []TilePos
	for _, t := range removed {
		for _, n := range fenceNeighbourTiles(t) {
			around = append(around, n)
		}
	}
	refenceTiles(w, asset, occupied, around)
}

// PlaceFenceRunResult is the outcome of a PlaceFenceRun command: the run's id
// (the value after FenceRunTagPrefix on every segment), the NEW segments in
// row-major order, the existing segments the run shares (junction posts, now
// also tagged with this run), and the existing segments whose piece changed
// because the run attached to them.
type PlaceFenceRunResult struct {
	RunID    string
	Objects  []*VillageObject
	Shared   []VillageObjectID
	Restated []VillageObjectID
}

// PlaceFenceRun returns a Command that lays a fence run of the given asset over
// the tile rectangle spanned by two world-pixel corners (x1,y1)-(x2,y2), any
// corner order. Validation is complete before any mutation, so a refused run
// places nothing: ErrUnknownAsset; ErrInvalidObjectPosition (non-finite
// coords); ErrFenceAssetUnsupported (not an obstacle, or a piece state missing);
// ErrFenceRunTooLarge (sized from the corners before anything is laid out);
// a *FenceTileBlocked (wrapping ErrFenceTileBlocked) naming the first tile that
// is not fenceable — a corner off the grid, or a ring tile that is not walkable
// on the standard walk grid (water, an obstacle footprint, another obstacle);
// or ErrFenceRunNothingNew when every ring tile already holds a segment of this
// asset.
//
// A ring tile already holding a segment of the SAME asset is shared, not
// blocked: the run reuses it as a junction, adds its run tag to it, and the
// segment's piece is re-resolved with the new neighbours (the end cap of a line
// becomes a corner when a second line is drawn onto it). Every new segment is
// minted through the shared placement path (placeVillageObject) with the piece
// its neighbourhood — new tiles plus existing same-asset segments — dictates,
// tagged FenceRunTagPrefix+RunID, and announced with VillageObjectCreated then
// VillageObjectTagsUpdated so a live client holds the tag without a reload.
// Existing neighbours of the new tiles are re-resolved too, each change
// announced with VillageObjectStateChanged.
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
			if err := fenceAssetCheck(asset); err != nil {
				return nil, err
			}
			// Bound the corners and size the run BEFORE laying it out, so a
			// caller handing in huge finite coordinates (this command is the
			// validation boundary, not the HTTP route) cannot make
			// fenceRunTiles allocate an enormous slice or overflow the
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
			tiles := fenceRunTiles(a, b)

			occupied := fenceAssetTiles(w, assetID)
			grid, err := buildWalkGrid(w)
			if err != nil {
				return nil, err
			}
			var fresh []TilePos
			var shared []*VillageObject
			for _, t := range tiles {
				if seg, ok := occupied[t]; ok {
					shared = append(shared, seg)
					continue
				}
				if !grid.CanWalk(t.X, t.Y) {
					return nil, &FenceTileBlocked{
						Tile: t,
						X:    float64(t.X-PadX) * TileSize,
						Y:    float64(t.Y-PadY) * TileSize,
					}
				}
				fresh = append(fresh, t)
			}
			if len(fresh) == 0 {
				return nil, ErrFenceRunNothingNew
			}

			runID := newUUIDv4()
			runTag := FenceRunTagPrefix + runID
			now := time.Now().UTC()

			// Mint the new segments first with a placeholder state so the
			// neighbour set is complete, then resolve every piece against it —
			// a new tile's piece depends on the other new tiles as much as on
			// the existing ones. The created event carries the resolved state:
			// pieces are resolved before the loop that emits.
			for _, t := range fresh {
				occupied[t] = nil // reserve the tile; the object lands below
			}
			pieces := make(map[TilePos]string, len(fresh))
			for _, t := range fresh {
				pieces[t] = asset.StateForTag(fencePieceAt(occupied, t)).State
			}
			objects := make([]*VillageObject, 0, len(fresh))
			for _, t := range fresh {
				obj := placeVillageObject(w, assetID, asset, fenceSegmentPos(t, asset), "", placedBy, pieces[t])
				obj.Tags = append(obj.Tags, runTag)
				w.emit(&VillageObjectTagsUpdated{
					ObjectID: obj.ID,
					Tags:     append([]string(nil), obj.Tags...),
					At:       now,
				})
				occupied[t] = obj
				objects = append(objects, obj)
			}

			// Shared junction posts join this run too.
			sharedIDs := make([]VillageObjectID, 0, len(shared))
			for _, seg := range shared {
				if !seg.HasTag(runTag) {
					seg.Tags = append(seg.Tags, runTag)
					w.emit(&VillageObjectTagsUpdated{
						ObjectID: seg.ID,
						Tags:     append([]string(nil), seg.Tags...),
						At:       now,
					})
				}
				sharedIDs = append(sharedIDs, seg.ID)
			}

			// Re-resolve the existing segments the run touches: the shared
			// tiles themselves and every existing neighbour of a new tile.
			var touched []TilePos
			for _, seg := range shared {
				touched = append(touched, seg.Pos.Tile())
			}
			for _, t := range fresh {
				for _, n := range fenceNeighbourTiles(t) {
					if _, isNew := pieces[n]; isNew {
						continue
					}
					touched = append(touched, n)
				}
			}
			restated := refenceTiles(w, asset, occupied, touched)

			return PlaceFenceRunResult{RunID: runID, Objects: objects, Shared: sharedIDs, Restated: restated}, nil
		},
	}
}

// FenceRunIDOf returns the first run id carried by a fence segment's tags (""
// when the object carries no FenceRunTagPrefix tag). Nil-safe. A shared
// junction post carries several; FenceRunIDsOf lists them all.
func FenceRunIDOf(o *VillageObject) string {
	ids := FenceRunIDsOf(o)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// FenceRunIDsOf returns every run id carried by a segment's tags, in tag order.
func FenceRunIDsOf(o *VillageObject) []string {
	if o == nil {
		return nil
	}
	var ids []string
	for _, t := range o.Tags {
		if strings.HasPrefix(t, FenceRunTagPrefix) {
			ids = append(ids, strings.TrimPrefix(t, FenceRunTagPrefix))
		}
	}
	return ids
}

// DeleteFenceRunResult lists every object removed (children before parents,
// deleteObjectCascade order) and every shared segment that was only released —
// still standing for another run, with this run's tag removed.
type DeleteFenceRunResult struct {
	DeletedIDs  []VillageObjectID
	ReleasedIDs []VillageObjectID
}

// DeleteFenceRun returns a Command that removes a run: every object tagged
// FenceRunTagPrefix+runID, plus anything attached to those segments (the same
// cascade DeleteVillageObject performs), emitting one VillageObjectDeleted per
// removed object. A segment that also carries ANOTHER run's tag is a junction
// post shared with that run: it is released (this run's tag removed,
// VillageObjectTagsUpdated emitted) rather than deleted, and survives until the
// last run using it goes. Afterwards the surviving neighbours of every removed
// tile are re-resolved so a detached line gets its end cap back. A segment that
// was promoted to a structure is refused as in DeleteVillageObject
// (ErrVillageObjectIsStructure) before anything is removed. ErrFenceRunNotFound
// when no object carries the tag — a run already deleted, or a bad id.
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
			var removed, released []VillageObjectID
			// Removed tiles per asset, for the re-resolution afterwards.
			removedTiles := make(map[AssetID][]TilePos)
			for _, id := range ids {
				// A segment may already be gone as an overlay of an earlier one.
				obj, ok := w.VillageObjects[id]
				if !ok || obj == nil {
					continue
				}
				if len(FenceRunIDsOf(obj)) > 1 {
					kept := obj.Tags[:0]
					for _, t := range obj.Tags {
						if t != runTag {
							kept = append(kept, t)
						}
					}
					obj.Tags = kept
					w.emit(&VillageObjectTagsUpdated{
						ObjectID: obj.ID,
						Tags:     append([]string(nil), obj.Tags...),
						At:       now,
					})
					released = append(released, obj.ID)
					continue
				}
				removedTiles[obj.AssetID] = append(removedTiles[obj.AssetID], obj.Pos.Tile())
				for _, rid := range deleteObjectCascade(w, id) {
					w.emit(&VillageObjectDeleted{ObjectID: rid, At: now})
					removed = append(removed, rid)
				}
			}
			for assetID, tiles := range removedTiles {
				refenceAroundRemoved(w, w.Assets[assetID], tiles)
			}
			return DeleteFenceRunResult{DeletedIDs: removed, ReleasedIDs: released}, nil
		},
	}
}
