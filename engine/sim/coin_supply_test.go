package sim

import "testing"

func TestComputeCoinSupply(t *testing.T) {
	// nil snapshot → zero value, no panic (the ConfigWarnings nil-safety posture).
	if got := ComputeCoinSupply(nil); got != (CoinSupply{}) {
		t.Errorf("nil snapshot = %+v, want zero value", got)
	}

	snap := &Snapshot{
		Actors: map[ActorID]*ActorSnapshot{
			"hannah": {Kind: KindNPCShared, Coins: 25},  // resident NPC
			"josiah": {Kind: KindNPCStateful, Coins: 3}, // resident NPC
			"bram":   {Kind: KindPC, Coins: 100},        // PC — resident (coin that stays)
			"factor": {Kind: KindNPCShared, Coins: 200, // transient visitor — passing through
				VisitorState: &VisitorState{Archetype: "peddler"}},
			"pauper": {Kind: KindNPCShared, Coins: 0, // broke visitor — still a holder, adds 0 to Visitor
				VisitorState: &VisitorState{Archetype: "pilgrim"}},
			"statue": {Kind: KindDecorative, Coins: 999}, // scenery — excluded (LLM-410 req)
			"ghost":  nil,                                // nil entry — skipped, no panic
		},
	}

	got := ComputeCoinSupply(snap)
	want := CoinSupply{
		Total:    25 + 3 + 100 + 200, // statue's 999 excluded; pauper adds 0
		Resident: 25 + 3 + 100,
		Visitor:  200, // pauper the broke visitor contributes 0
		Holders:  5,   // hannah, josiah, bram, factor, pauper — statue + nil ghost excluded
	}
	if got != want {
		t.Errorf("ComputeCoinSupply = %+v, want %+v", got, want)
	}
}

// A decorative actor holding coin (a data anomaly, or a decorative given a purse
// some day) never enters the supply — the explicit LLM-410 exclusion.
func TestComputeCoinSupply_ExcludesDecorativeWithCoins(t *testing.T) {
	snap := &Snapshot{Actors: map[ActorID]*ActorSnapshot{
		"statue": {Kind: KindDecorative, Coins: 500},
	}}
	if got := ComputeCoinSupply(snap); got != (CoinSupply{}) {
		t.Errorf("decorative-only supply = %+v, want zero value", got)
	}
}
