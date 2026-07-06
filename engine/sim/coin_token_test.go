package sim

import "testing"

// coin_token_test.go — LLM-290. The coin-token predicate behind the
// pay_with_item coin-payment translation, the pay_items / scene_quote /
// offer_trade steers, and mintDiscoveredKind's never-mint-currency guard.

func TestIsCoinToken(t *testing.T) {
	yes := []string{"coin", "coins", "Coin", "COINS", " coins ", "a coin", "the coins", "The Coin"}
	for _, s := range yes {
		if !IsCoinToken(s) {
			t.Errorf("IsCoinToken(%q) = false, want true", s)
		}
	}
	// Closed list: anything beyond the bare currency tokens is NOT matched —
	// a compound ("coin purse") or lookalike could be a real authored good.
	no := []string{"", "coinage", "coin purse", "gold", "bread", "coins of the realm"}
	for _, s := range no {
		if IsCoinToken(s) {
			t.Errorf("IsCoinToken(%q) = true, want false", s)
		}
	}
}

// TestMintDiscoveredKind_NeverMintsCoins: the discovery-mint sites (consume,
// scene_quote, pay_items) can never (re-)create the phantom 'coin' catalog
// kind the LLM-290 migration removed — a coin token fails the mint instead.
// A normal unknown good still mints, proving the guard is coin-scoped.
func TestMintDiscoveredKind_NeverMintsCoins(t *testing.T) {
	w := &World{ItemKinds: map[ItemKind]*ItemKindDef{}}
	for _, s := range []string{"coin", "coins", "a coin"} {
		if kind, ok := resolveOrMintItemKind(w, s); ok {
			t.Errorf("resolveOrMintItemKind(%q) minted %q, want no mint", s, kind)
		}
	}
	if len(w.ItemKinds) != 0 {
		t.Errorf("catalog gained %d kinds from coin tokens, want 0", len(w.ItemKinds))
	}
	if kind, ok := resolveOrMintItemKind(w, "dried chamomile"); !ok || kind == "" {
		t.Error("a normal unknown good should still mint (guard must be coin-scoped)")
	}
}
