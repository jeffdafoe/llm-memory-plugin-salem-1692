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

// TestFencePieceFor pins the full 16-case neighbour matrix (LLM-638). The
// client preview mirrors this table in editor.gd _fence_piece_for, pinned by
// tests/fence_run_test.gd — change both or neither.
func TestFencePieceFor(t *testing.T) {
	cases := []struct {
		n, e, s, w bool
		want       string
	}{
		{false, false, false, false, TagFencePost},
		{false, true, false, false, TagFenceEndLeft},
		{false, false, false, true, TagFenceEndRight},
		{false, false, true, false, TagFenceVTop},
		{true, false, false, false, TagFenceVBottom},
		{false, true, false, true, TagFenceH},
		{true, false, true, false, TagFenceV},
		{false, true, true, false, TagFenceCornerTL},
		{false, false, true, true, TagFenceCornerTR},
		{true, true, false, false, TagFenceCornerBL},
		{true, false, false, true, TagFenceCornerBR},
		// No T / cross art: E+W wins as h, then N+S as v.
		{true, true, false, true, TagFenceH},
		{false, true, true, true, TagFenceH},
		{true, true, true, true, TagFenceH},
		{true, true, true, false, TagFenceV},
		{true, false, true, true, TagFenceV},
	}
	for _, tc := range cases {
		if got := fencePieceFor(tc.n, tc.e, tc.s, tc.w); got != tc.want {
			t.Errorf("fencePieceFor(n=%v e=%v s=%v w=%v) = %s, want %s", tc.n, tc.e, tc.s, tc.w, got, tc.want)
		}
	}
}

// TestFenceRunTiles pins the geometry — ring tiles in row-major order, corner
// order irrelevant — and TestFenceRunShapesResolveAsDrawn that an ISOLATED run
// resolves through the neighbour predicate to exactly the hand-drawn vocabulary
// (post / capped line / capped vertical / cornered ring): nothing visible
// changed for a lone run when pieces went neighbour-aware.
func TestFenceRunTiles(t *testing.T) {
	tp := func(x, y int) TilePos { return TilePos{X: x, Y: y} }
	cases := []struct {
		name string
		a, b TilePos
		want []TilePos
	}{
		{"1x1", tp(5, 5), tp(5, 5), []TilePos{tp(5, 5)}},
		{"4x1", tp(5, 5), tp(8, 5), []TilePos{tp(5, 5), tp(6, 5), tp(7, 5), tp(8, 5)}},
		{"1x4 bottom-up", tp(5, 8), tp(5, 5), []TilePos{tp(5, 5), tp(5, 6), tp(5, 7), tp(5, 8)}},
		{"4x3 from bottom-right", tp(8, 7), tp(5, 5), []TilePos{
			tp(5, 5), tp(6, 5), tp(7, 5), tp(8, 5),
			tp(5, 6), tp(8, 6),
			tp(5, 7), tp(6, 7), tp(7, 7), tp(8, 7)}},
		{"2x2", tp(5, 5), tp(6, 6), []TilePos{tp(5, 5), tp(6, 5), tp(5, 6), tp(6, 6)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fenceRunTiles(tc.a, tc.b); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("tiles = %v, want %v", got, tc.want)
			}
			if got, want := fenceRunSize(tc.a, tc.b), len(tc.want); got != want {
				t.Errorf("fenceRunSize = %d, want %d", got, want)
			}
		})
	}
}

func TestFenceRunShapesResolveAsDrawn(t *testing.T) {
	tp := func(x, y int) TilePos { return TilePos{X: x, Y: y} }
	resolve := func(a, b TilePos) []string {
		occupied := make(map[TilePos]*VillageObject)
		tiles := fenceRunTiles(a, b)
		for _, tile := range tiles {
			occupied[tile] = nil
		}
		var out []string
		for _, tile := range tiles {
			out = append(out, fencePieceAt(occupied, tile))
		}
		return out
	}
	cases := []struct {
		name string
		a, b TilePos
		want []string
	}{
		{"post", tp(5, 5), tp(5, 5), []string{TagFencePost}},
		{"line", tp(5, 5), tp(8, 5), []string{TagFenceEndLeft, TagFenceH, TagFenceH, TagFenceEndRight}},
		{"vertical", tp(5, 8), tp(5, 5), []string{TagFenceVTop, TagFenceV, TagFenceV, TagFenceVBottom}},
		{"ring", tp(8, 7), tp(5, 5), []string{
			TagFenceCornerTL, TagFenceH, TagFenceH, TagFenceCornerTR,
			TagFenceV, TagFenceV,
			TagFenceCornerBL, TagFenceH, TagFenceH, TagFenceCornerBR}},
		{"2x2", tp(5, 5), tp(6, 6), []string{TagFenceCornerTL, TagFenceCornerTR, TagFenceCornerBL, TagFenceCornerBR}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolve(tc.a, tc.b); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pieces = %v, want %v", got, tc.want)
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

// TestFenceRunIDOf reads run ids off a segment's tags and "" off anything else.
func TestFenceRunIDOf(t *testing.T) {
	shared := &VillageObject{Tags: []string{"shop", FenceRunTagPrefix + "abc", FenceRunTagPrefix + "def"}}
	if got := FenceRunIDOf(shared); got != "abc" {
		t.Errorf("got %q, want abc", got)
	}
	if got := FenceRunIDsOf(shared); !reflect.DeepEqual(got, []string{"abc", "def"}) {
		t.Errorf("ids = %v, want [abc def]", got)
	}
	if got := FenceRunIDOf(&VillageObject{Tags: []string{"shop"}}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := FenceRunIDOf(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
}
