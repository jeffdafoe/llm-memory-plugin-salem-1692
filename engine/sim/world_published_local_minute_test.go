package sim_test

import (
	"testing"
)

// TestPublishedSnapshot_LocalMinuteOfDay confirms republish stamps the village
// wall-clock minute onto every published snapshot, so perception time-of-day
// prose (ZBBS-HOME-351) and schedule steering (ZBBS-HOME-352) have the local
// clock without the village *time.Location. Reuses the acquaintance test world
// (a running world that has published at least once). ZBBS-HOME-351.
func TestPublishedSnapshot_LocalMinuteOfDay(t *testing.T) {
	w, stop := buildAcquaintanceTestWorld(t)
	defer stop()

	snap := w.Published()
	if snap == nil {
		t.Fatal("Published() returned nil")
	}
	if snap.LocalMinuteOfDay == nil {
		t.Fatal("LocalMinuteOfDay should be set on a published snapshot, got nil")
	}
	if m := *snap.LocalMinuteOfDay; m < 0 || m > 1439 {
		t.Errorf("LocalMinuteOfDay = %d, want a valid minute-of-day (0..1439)", m)
	}
}
