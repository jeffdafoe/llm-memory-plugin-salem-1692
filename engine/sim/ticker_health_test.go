package sim

import (
	"sync"
	"testing"
)

// ticker_health_test.go — registry-internal coverage (package sim, so it can
// reach the unexported beat / newTickerHealth / w.beatTicker). The HTTP contract
// is covered separately in httpapi.

func TestTickerHealth_BeatAndSnapshot(t *testing.T) {
	h := newTickerHealth()
	h.beat("needs")
	h.beat("needs")
	h.beat("shift")

	got := h.Snapshot()
	if len(got) != 2 {
		t.Fatalf("snapshot len=%d, want 2", len(got))
	}
	// Sorted by name: needs before shift.
	if got[0].Name != "needs" || got[1].Name != "shift" {
		t.Errorf("snapshot not sorted by name: %+v", got)
	}
	if got[0].Count != 2 {
		t.Errorf("needs count=%d, want 2", got[0].Count)
	}
	if got[1].Count != 1 {
		t.Errorf("shift count=%d, want 1", got[1].Count)
	}
	if got[0].LastFire.IsZero() {
		t.Error("needs LastFire is zero after a beat")
	}
}

func TestTickerHealth_NilSafe(t *testing.T) {
	var h *TickerHealth
	// A nil registry must not panic on beat (a hand-built world that bypassed
	// NewWorld) and returns no entries.
	h.beat("anything")
	if got := h.Snapshot(); got != nil {
		t.Errorf("nil registry Snapshot = %v, want nil", got)
	}
}

func TestTickerHealth_ConcurrentBeats(t *testing.T) {
	h := newTickerHealth()
	const goroutines, perG = 8, 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				h.beat("needs")
			}
		}()
	}
	wg.Wait()

	got := h.Snapshot()
	if len(got) != 1 || got[0].Name != "needs" {
		t.Fatalf("snapshot=%+v, want one 'needs' entry", got)
	}
	if got[0].Count != goroutines*perG {
		t.Errorf("count=%d, want %d (lost beats under concurrency)", got[0].Count, goroutines*perG)
	}
}

func TestWorld_BeatTickerRoutesToRegistry(t *testing.T) {
	w := NewWorld(Repository{})
	w.beatTicker("phase")           // in-package sim ticker path
	w.BeatTicker("atmosphere")      // exported path (cascade-package tickers)
	w.BeatTicker("atmosphere")      //
	got := w.TickerHealthSnapshot() //
	if len(got) != 2 {
		t.Fatalf("snapshot len=%d, want 2 (phase + atmosphere)", len(got))
	}
	// Sorted by name: atmosphere before phase.
	if got[0].Name != "atmosphere" || got[0].Count != 2 {
		t.Errorf("atmosphere entry = %+v, want count=2 (exported BeatTicker)", got[0])
	}
	if got[1].Name != "phase" || got[1].Count != 1 {
		t.Errorf("phase entry = %+v, want count=1 (unexported beatTicker)", got[1])
	}
}
