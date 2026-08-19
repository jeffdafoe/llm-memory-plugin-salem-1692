package sim

import (
	"reflect"
	"testing"
)

// fenceTestAssetID is the fence asset every fence test places.
const fenceTestAssetID AssetID = "ranch-fence"

// fenceTestAsset mirrors the LLM-637 catalog row: one asset, one state per
// piece, the role carried by the TAG (a cell that serves two roles carries
// both). IDs are distinct so StateForTag is deterministic.
func fenceTestAsset() *Asset {
	return &Asset{
		ID: fenceTestAssetID, Name: "Ranch Fence", Category: "fence", DefaultState: "h",
		AnchorX: 0.5, AnchorY: 0.85, IsObstacle: true,
		States: []AssetState{
			{ID: 1, State: "corner-tl", Tags: []string{TagFenceCornerTL}},
			{ID: 2, State: "h", Tags: []string{TagFenceH}},
			{ID: 3, State: "corner-tr", Tags: []string{TagFenceCornerTR}},
			{ID: 4, State: "v-top", Tags: []string{TagFenceVTop}},
			{ID: 5, State: "v", Tags: []string{TagFenceV}},
			{ID: 6, State: "v-bottom", Tags: []string{TagFenceVBottom, TagFencePost}},
			{ID: 7, State: "corner-bl", Tags: []string{TagFenceCornerBL, TagFenceEndLeft}},
			{ID: 8, State: "corner-br", Tags: []string{TagFenceCornerBR, TagFenceEndRight}},
		},
	}
}

// TestFenceRunSegments_Shapes pins the shape → piece vocabulary for all four
// shapes: lone post, horizontal line, vertical line, ring. Row-major order, and
// the corner order of the drag does not matter.
func TestFenceRunSegments_Shapes(t *testing.T) {
	seg := func(x, y int, tag string) fenceSegment { return fenceSegment{Tile: TilePos{X: x, Y: y}, Tag: tag} }
	cases := []struct {
		name string
		a, b TilePos
		want []fenceSegment
	}{
		{"1x1 post", TilePos{5, 5}, TilePos{5, 5}, []fenceSegment{seg(5, 5, TagFencePost)}},
		{"4x1 horizontal line", TilePos{5, 5}, TilePos{8, 5}, []fenceSegment{
			seg(5, 5, TagFenceEndLeft), seg(6, 5, TagFenceH), seg(7, 5, TagFenceH), seg(8, 5, TagFenceEndRight)}},
		{"1x4 vertical line, dragged bottom-up", TilePos{5, 8}, TilePos{5, 5}, []fenceSegment{
			seg(5, 5, TagFenceVTop), seg(5, 6, TagFenceV), seg(5, 7, TagFenceV), seg(5, 8, TagFenceVBottom)}},
		{"4x3 ring, dragged from bottom-right", TilePos{8, 7}, TilePos{5, 5}, []fenceSegment{
			seg(5, 5, TagFenceCornerTL), seg(6, 5, TagFenceH), seg(7, 5, TagFenceH), seg(8, 5, TagFenceCornerTR),
			seg(5, 6, TagFenceV), seg(8, 6, TagFenceV),
			seg(5, 7, TagFenceCornerBL), seg(6, 7, TagFenceH), seg(7, 7, TagFenceH), seg(8, 7, TagFenceCornerBR)}},
		{"2x2 ring is four corners", TilePos{5, 5}, TilePos{6, 6}, []fenceSegment{
			seg(5, 5, TagFenceCornerTL), seg(6, 5, TagFenceCornerTR),
			seg(5, 6, TagFenceCornerBL), seg(6, 6, TagFenceCornerBR)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fenceRunSegments(tc.a, tc.b)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("segments = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFenceSegmentPos: a segment's anchor lands inside its own tile (so the
// obstacle stamp keys on the drawn tile) and the sprite's top-left — anchor
// minus anchor fraction × tile — is the tile origin exactly.
func TestFenceSegmentPos(t *testing.T) {
	asset := fenceTestAsset()
	tile := TilePos{X: PadX + 3, Y: PadY + 7}
	pos := fenceSegmentPos(tile, asset)
	if got := pos.Tile(); got != tile {
		t.Fatalf("Tile() of segment pos = %v, want %v", got, tile)
	}
	wantX, wantY := 3*TileSize, 7*TileSize
	if gotX, gotY := pos.X-asset.AnchorX*TileSize, pos.Y-asset.AnchorY*TileSize; gotX != wantX || gotY != wantY {
		t.Errorf("sprite top-left = (%v,%v), want tile origin (%v,%v)", gotX, gotY, wantX, wantY)
	}
}

// TestFenceRunIDOf reads the run id off a segment's tags and "" off anything else.
func TestFenceRunIDOf(t *testing.T) {
	if got := FenceRunIDOf(&VillageObject{Tags: []string{"shop", FenceRunTagPrefix + "abc"}}); got != "abc" {
		t.Errorf("got %q, want abc", got)
	}
	if got := FenceRunIDOf(&VillageObject{Tags: []string{"shop"}}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := FenceRunIDOf(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
}
