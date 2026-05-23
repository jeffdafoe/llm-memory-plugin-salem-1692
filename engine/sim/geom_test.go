package sim

import "testing"

// geom_test.go — coordinate type conversions + distance metrics.

func TestWorldPosTile_FloorAndPad(t *testing.T) {
	cases := []struct {
		name string
		w    WorldPos
		want TilePos
	}{
		{"origin maps to pad", WorldPos{0, 0}, TilePos{PadX, PadY}},
		{"one tile over", WorldPos{TileSize, TileSize}, TilePos{PadX + 1, PadY + 1}},
		{"floors within a tile", WorldPos{TileSize - 1, TileSize - 1}, TilePos{PadX, PadY}},
		{"negative floors down", WorldPos{-1, -1}, TilePos{PadX - 1, PadY - 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.Tile(); !got.Equal(tc.want) {
				t.Errorf("%v.Tile() = %v, want %v", tc.w, got, tc.want)
			}
		})
	}
}

func TestTilePosCenter_AndRoundTrip(t *testing.T) {
	// Center of the pad-origin tile is half a tile in from world (0,0).
	if got := (TilePos{PadX, PadY}).Center(); got.X != TileSize/2 || got.Y != TileSize/2 {
		t.Errorf("Center() = %v, want {%g,%g}", got, TileSize/2, TileSize/2)
	}
	// Any tile's center floors back to that same tile.
	for _, tp := range []TilePos{{PadX, PadY}, {PadX + 5, PadY + 9}, {PadX - 3, PadY + 2}} {
		if got := tp.Center().Tile(); !got.Equal(tp) {
			t.Errorf("round trip %v -> Center %v -> Tile %v", tp, tp.Center(), got)
		}
	}
}

func TestTilePosOrigin(t *testing.T) {
	if got := (TilePos{PadX + 1, PadY + 2}).Origin(); got.X != TileSize || got.Y != 2*TileSize {
		t.Errorf("Origin() = %v, want {%g,%g}", got, TileSize, 2*TileSize)
	}
}

func TestChebyshevAndManhattan(t *testing.T) {
	a := TilePos{10, 10}
	cases := []struct {
		b              TilePos
		cheb, manhattn int
	}{
		{TilePos{12, 11}, 2, 3},
		{TilePos{11, 13}, 3, 4},
		{TilePos{10, 10}, 0, 0},
		{TilePos{7, 14}, 4, 7},
	}
	for _, tc := range cases {
		if got := a.Chebyshev(tc.b); got != tc.cheb {
			t.Errorf("%v.Chebyshev(%v) = %d, want %d", a, tc.b, got, tc.cheb)
		}
		if got := a.Manhattan(tc.b); got != tc.manhattn {
			t.Errorf("%v.Manhattan(%v) = %d, want %d", a, tc.b, got, tc.manhattn)
		}
	}
}

func TestTilePosAddOffset(t *testing.T) {
	got := (TilePos{60, 112}).Add(TileOffset{DX: 2, DY: -3})
	if !got.Equal(TilePos{62, 109}) {
		t.Errorf("Add = %v, want {62,109}", got)
	}
}

func TestWorldPosDist(t *testing.T) {
	if got := (WorldPos{0, 0}).Dist(WorldPos{3, 4}); got != 5 {
		t.Errorf("Dist = %g, want 5", got)
	}
}

// TestLegacyWrappersDelegate guards that WorldToTile / TileToWorld stay
// behaviorally identical to the methods they now delegate to.
func TestLegacyWrappersDelegate(t *testing.T) {
	w := WorldPos{X: 137, Y: 401}
	if got := WorldToTile(w.X, w.Y); !got.Equal(w.Tile()) {
		t.Errorf("WorldToTile = %v, want %v", got, w.Tile())
	}
	tp := TilePos{PadX + 4, PadY + 7}
	if got := TileToWorld(tp); got != tp.Center() {
		t.Errorf("TileToWorld = %v, want %v", got, tp.Center())
	}
}
