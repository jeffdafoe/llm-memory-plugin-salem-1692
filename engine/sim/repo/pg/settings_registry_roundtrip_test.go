package pg

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// settings_registry_roundtrip_test.go — LLM-577. The registry↔loader round-trip.
//
// The registry test in package sim can only prove Read and Apply agree with EACH
// OTHER, because both derive a duration's unit from the same key suffix. A spec
// pointed at a field the loader measures differently would satisfy that test and
// still be wrong by a factor of 60.
//
// This closes it from the other side: seed a known time.Duration, assert the
// exact scalar the registry stores, then feed that scalar through the real
// loader and require the original Duration back. Registry → setting table →
// buildSettings is the actual path a live tune takes across a restart, so this
// is the round-trip that matters.

func TestRegistryDurationsSurviveTheLoader(t *testing.T) {
	cases := []struct {
		key        string
		live       time.Duration
		wantStored string
		read       func(sim.WorldSettings) time.Duration
		write      func(*sim.WorldSettings, time.Duration)
	}{
		{
			key: "admission_backoff_ms", live: 250 * time.Millisecond, wantStored: "250",
			read:  func(s sim.WorldSettings) time.Duration { return s.AdmissionBackoff },
			write: func(s *sim.WorldSettings, d time.Duration) { s.AdmissionBackoff = d },
		},
		{
			key: "eco_social_gap_seconds", live: 45 * time.Second, wantStored: "45",
			read:  func(s sim.WorldSettings) time.Duration { return s.EcoSocialGap },
			write: func(s *sim.WorldSettings, d time.Duration) { s.EcoSocialGap = d },
		},
		{
			key: "storm_interval_minutes", live: 3 * time.Hour, wantStored: "180",
			read:  func(s sim.WorldSettings) time.Duration { return s.StormInterval },
			write: func(s *sim.WorldSettings, d time.Duration) { s.StormInterval = d },
		},
		{
			key: "action_log_retention_hours", live: 48 * time.Hour, wantStored: "48",
			read:  func(s sim.WorldSettings) time.Duration { return s.ActionLogRetention },
			write: func(s *sim.WorldSettings, d time.Duration) { s.ActionLogRetention = d },
		},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			spec, ok := sim.SettingSpecByKey(tc.key)
			if !ok {
				t.Fatalf("%q is not registered", tc.key)
			}

			// Start from a LIVE duration written straight onto the field — the
			// direction the checkpoint actually runs. Going through Apply first
			// would only re-prove that Apply and Read agree with each other,
			// which is what the sim-side test already covers and is exactly the
			// pair that cannot detect a unit mismatch.
			ws := sim.WorldSettings{}
			tc.write(&ws, tc.live)
			if got := spec.Read(&ws); got != tc.wantStored {
				t.Fatalf("registry formatted %v as %q, want %q — the spec's field is measured in a different unit than its key advertises",
					tc.live, got, tc.wantStored)
			}

			// And through the real persistence projection, since that (not
			// Read alone) is what lands in the setting table.
			if got := sim.PersistableSettingRows(&ws)[tc.key]; got != tc.wantStored {
				t.Fatalf("checkpoint would persist %s=%q, want %q", tc.key, got, tc.wantStored)
			}

			// The reverse leg: the stored scalar parses back to the same live
			// duration.
			back := sim.WorldSettings{}
			if err := spec.Apply(&back, tc.wantStored); err != nil {
				t.Fatalf("Apply(%q): %v", tc.wantStored, err)
			}
			if got := tc.read(back); got != tc.live {
				t.Fatalf("Apply(%q) produced %v, want %v", tc.wantStored, got, tc.live)
			}

			// The loader must reconstruct the same duration from that scalar.
			loaded := buildSettings(map[string]string{tc.key: tc.wantStored})
			if got := tc.read(loaded); got != tc.live {
				t.Errorf("loader read %q=%q as %v, want %v — registry and loader disagree on this key's unit",
					tc.key, tc.wantStored, got, tc.live)
			}
		})
	}
}

// TestEcoAudienceIdleZeroSurvivesToTheEffectiveDefault covers the one behaviour
// change LLM-577 makes to what gets STORED: the old checkpoint builder wrote the
// RESOLVED horizon (PCAudienceIdleAfter, which turns a stored 0 into the
// default), the registry writes the raw 0.
//
// The claim being pinned is that this is behaviourally inert — a stored 0 must
// come back out of the loader as 0 and still resolve to the default at read
// time. If the loader ever stopped seeding the default, or the resolver stopped
// treating 0 as unset, this would catch it.
func TestEcoAudienceIdleZeroSurvivesToTheEffectiveDefault(t *testing.T) {
	const key = "eco_audience_idle_seconds"

	ws := sim.WorldSettings{}
	spec, ok := sim.SettingSpecByKey(key)
	if !ok {
		t.Fatalf("%q is not registered", key)
	}
	if err := spec.Apply(&ws, "0"); err != nil {
		t.Fatalf("Apply(0): %v", err)
	}

	// Checkpoint would store exactly this.
	rows := sim.PersistableSettingRows(&ws)
	if rows[key] != "0" {
		t.Fatalf("checkpoint would store %q=%q, want \"0\" (the raw value, not the resolved default)", key, rows[key])
	}

	// Reload: the raw 0 comes back as 0, NOT silently promoted to the default.
	loaded := buildSettings(map[string]string{key: "0"})
	if loaded.PCAudienceIdleAfter != 0 {
		t.Errorf("loader read a stored 0 as %v, want 0 — the raw/effective split this change relies on is gone",
			loaded.PCAudienceIdleAfter)
	}

	// And the resolver still turns that 0 into the default at read time, which
	// is what makes storing the raw value harmless.
	w := &sim.World{Settings: loaded}
	if got := sim.PCAudienceIdleAfter(w); got != sim.DefaultPCAudienceIdleAfter {
		t.Errorf("PCAudienceIdleAfter with a stored 0 = %v, want the %v default — storing the raw 0 is only safe while the resolver does this",
			got, sim.DefaultPCAudienceIdleAfter)
	}
}
