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

	// republish must also stamp the dawn/dusk window (ZBBS-HOME-352). The test
	// world loads default settings (dawn 07:00 / dusk 19:00), so both parse and
	// DawnDuskMinuteOK is true. Guards the green-but-broken gap where republish
	// drops the field and unscheduled-NPC return-to-post steering silently never
	// fires.
	if !snap.DawnDuskMinuteOK {
		t.Error("DawnDuskMinuteOK should be true on a published snapshot with default dawn/dusk")
	}
	if snap.DawnMinute != 420 || snap.DuskMinute != 1140 {
		t.Errorf("dawn/dusk = %d/%d, want 420/1140 (07:00/19:00 defaults)", snap.DawnMinute, snap.DuskMinute)
	}
}
