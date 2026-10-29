package sim

import "testing"

// visitor_provenance_internal_test.go — LLM-875: recordVisitorProvenance, the
// per-visit record of which resident handed a traveler which goods. Goods move
// through transferItem so the clamps see real holdings.

func TestRecordVisitorProvenance(t *testing.T) {
	const wheat = ItemKind("wheat")
	newFactor := func() *Actor {
		return &Actor{ID: "vstr-1", Inventory: map[ItemKind]int{}, VisitorState: &VisitorState{}}
	}
	newResident := func(id ActorID, qty int) *Actor {
		return &Actor{ID: id, Inventory: map[ItemKind]int{wheat: qty}}
	}
	move := func(t *testing.T, from, to *Actor, qty int) {
		t.Helper()
		if err := transferItem(nil, from, to, wheat, qty); err != nil {
			t.Fatalf("transferItem %s→%s: %v", from.ID, to.ID, err)
		}
	}

	t.Run("resident to visitor credits his row", func(t *testing.T) {
		f, josiah := newFactor(), newResident("josiah", 10)
		move(t, josiah, f, 5)
		move(t, josiah, f, 2)
		if got := f.VisitorState.ReceivedFrom["josiah"][wheat]; got != 7 {
			t.Fatalf("ReceivedFrom[josiah][wheat] = %d, want 7", got)
		}
	})

	t.Run("visitor back to the same resident nets, delete-on-zero", func(t *testing.T) {
		f, josiah := newFactor(), newResident("josiah", 5)
		f.Inventory[wheat] = 10 // five of his own from Boston
		move(t, josiah, f, 5)
		move(t, f, josiah, 3)
		if got := f.VisitorState.ReceivedFrom["josiah"][wheat]; got != 2 {
			t.Fatalf("after 3 back: %d, want 2", got)
		}
		move(t, f, josiah, 4) // more than came from him: floor 0
		if _, ok := f.VisitorState.ReceivedFrom["josiah"]; ok {
			t.Fatalf("row survives at zero: %v", f.VisitorState.ReceivedFrom)
		}
	})

	t.Run("goods he parts with elsewhere shrink the row to what he holds", func(t *testing.T) {
		f, josiah, hannah := newFactor(), newResident("josiah", 5), newResident("hannah", 0)
		f.Inventory[wheat] = 3 // Boston wheat
		move(t, josiah, f, 5)
		move(t, f, hannah, 6) // holds 2 now
		if got := f.VisitorState.ReceivedFrom["josiah"][wheat]; got != 2 {
			t.Fatalf("ReceivedFrom[josiah][wheat] = %d after he sold 6 of 8 to hannah, want 2 (capped at holdings)", got)
		}
		move(t, f, hannah, 2) // holds none
		if f.VisitorState.ReceivedFrom != nil && len(f.VisitorState.ReceivedFrom) != 0 {
			t.Fatalf("row survives an empty pack: %v", f.VisitorState.ReceivedFrom)
		}
	})

	t.Run("a stale row never claims stock that arrives later", func(t *testing.T) {
		// code_review: josiah's wheat leaves the pack without a transfer (eaten,
		// worked up), then mary hands over wheat. Josiah's row must not claim it.
		f, josiah, mary := newFactor(), newResident("josiah", 5), newResident("mary", 5)
		move(t, josiah, f, 5)
		delete(f.Inventory, wheat)
		move(t, mary, f, 5)
		rows := f.VisitorState.ReceivedFrom
		if rows["josiah"][wheat] != 0 || rows["mary"][wheat] != 5 {
			t.Fatalf("ReceivedFrom = %v, want only mary:5 — josiah's wheat was gone before mary's arrived", rows)
		}
	})

	t.Run("visitor to visitor and resident to resident credit nothing", func(t *testing.T) {
		f, g := newFactor(), &Actor{ID: "vstr-2", Inventory: map[ItemKind]int{}, VisitorState: &VisitorState{}}
		f.Inventory[wheat] = 4
		josiah, hannah := newResident("josiah", 4), newResident("hannah", 0)
		move(t, f, g, 4)
		move(t, josiah, hannah, 4)
		if len(f.VisitorState.ReceivedFrom) != 0 || len(g.VisitorState.ReceivedFrom) != 0 {
			t.Fatalf("visitor-to-visitor move recorded provenance: %v / %v", f.VisitorState.ReceivedFrom, g.VisitorState.ReceivedFrom)
		}
	})
}

// TestCloneVisitorStateDeepCopiesReceivedFrom — a snapshot must not alias the
// world's provenance maps.
func TestCloneVisitorStateDeepCopiesReceivedFrom(t *testing.T) {
	src := &VisitorState{ReceivedFrom: map[ActorID]map[ItemKind]int{"josiah": {"wheat": 5}}}
	cp := cloneVisitorState(src)
	src.ReceivedFrom["josiah"]["wheat"] = 1
	src.ReceivedFrom["hannah"] = map[ItemKind]int{"cheese": 2}
	if cp.ReceivedFrom["josiah"]["wheat"] != 5 || len(cp.ReceivedFrom) != 1 {
		t.Fatalf("clone aliases the source: %v", cp.ReceivedFrom)
	}
}
