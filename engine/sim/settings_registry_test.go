package sim

import (
	"strings"
	"testing"
	"time"
)

// settings_registry_test.go — LLM-577.

// TestSettingRegistryRoundTripsEveryKey is the load-bearing invariant: for every
// registered key, formatting the live value and parsing it back must be the
// identity. It has to hold because the SAME encoding crosses three boundaries —
// the checkpoint writes Read() into the setting table, the loader parses that
// row back at boot, and the operator API echoes Read() after an Apply(). A key
// whose format and parse disagree loses its value on the next restart, silently.
//
// Duration keys are the ones this actually catches: they store a scalar whose
// unit comes from the key suffix, so a spec whose key lacks a recognized suffix,
// or whose field is set in the wrong unit, round-trips wrong.
func TestSettingRegistryRoundTripsEveryKey(t *testing.T) {
	for _, spec := range SettingSpecs() {
		t.Run(spec.Key, func(t *testing.T) {
			ws := seedRoundTripSettings(t, spec)
			first := spec.Read(&ws)
			if err := spec.Apply(&ws, first); err != nil {
				t.Fatalf("Apply(%q) rejected the value Read produced: %v", first, err)
			}
			if second := spec.Read(&ws); second != first {
				t.Errorf("round-trip changed the value: Read=%q, Apply+Read=%q", first, second)
			}
		})
	}
}

// seedRoundTripSettings returns a WorldSettings with a non-zero value written
// into spec's field, so the round-trip exercises a real value rather than the
// zero one (a duration bug in the wrong unit still round-trips 0 → 0).
func seedRoundTripSettings(t *testing.T, spec SettingSpec) WorldSettings {
	t.Helper()
	ws := WorldSettings{}
	var seed string
	switch spec.Kind {
	case SettingKindInt:
		seed = "37"
	case SettingKindDuration:
		seed = "90"
	case SettingKindBool:
		seed = "true"
	case SettingKindFloat:
		seed = "0.375"
	case SettingKindString:
		// The two clock keys parse as HH:MM downstream and world_timezone must
		// name a loadable zone, so seed something each will accept rather than
		// an arbitrary token.
		seed = "07:30"
		if spec.Key == "world_timezone" {
			seed = "America/New_York"
		}
	default:
		t.Fatalf("unhandled setting kind %q", spec.Kind)
	}
	if err := spec.Apply(&ws, seed); err != nil {
		t.Fatalf("seeding %q with %q: %v", spec.Key, seed, err)
	}
	return ws
}

// TestSettingRegistryDurationKeysCarryAUnitSuffix pins that every duration spec's
// key ends in one of the four recognized suffixes.
//
// Without a suffix, DurationUnitForKey fails and the spec's formatter falls back
// to "0" — so the key would read as zero forever and its checkpoint row would
// zero the setting on the next boot. Nothing else catches that: the round-trip
// test above is satisfied by a consistent "0" → "0".
//
// It does NOT verify that the field behind the spec is measured in that unit —
// nothing in the registry can, since get and set both derive the unit from the
// same key. That correctness comes from the spec pointing at the field the
// loader assigns with the same key, which is what the pg coverage test pins.
func TestSettingRegistryDurationKeysCarryAUnitSuffix(t *testing.T) {
	for _, spec := range SettingSpecs() {
		if spec.Kind != SettingKindDuration {
			continue
		}
		if _, ok := DurationUnitForKey(spec.Key); !ok {
			t.Errorf("duration setting %q has no recognized _ms / _seconds / _minutes / _hours suffix — it would read as 0 and zero the stored row at the next checkpoint", spec.Key)
		}
	}
}

// TestSettingRegistryRejectsBadValues pins that a malformed value is refused
// rather than silently coerced — an operator typo must not land as a zero.
func TestSettingRegistryRejectsBadValues(t *testing.T) {
	cases := []struct {
		key  string
		bad  string
		want string
	}{
		{"seek_work_coin_ceiling", "lots", "whole number"},
		{"eco_enabled", "yes-please", "true or false"},
		{"world_zoom_min_admin", "big", "must be a number"},
		// Non-finite floats: ParseFloat accepts all three by design and
		// FormatFloat would persist them. NaN is the dangerous one — every
		// comparison against it is false, so a NaN zoom floor disables the
		// clamp it exists to enforce instead of failing visibly.
		{"world_zoom_min_admin", "NaN", "finite number"},
		{"world_zoom_min_admin", "Inf", "finite number"},
		{"world_zoom_min_admin", "-Inf", "finite number"},
		{"world_zoom_min_regular", "-0.5", "0 or greater"},
		// The clamp class: the loader reads these through clampNonNegSetting, so
		// a negative accepted live would read back as written and resolve to 0
		// on the next restart.
		{"cold_night_multiplier_x100", "-1", "0 or greater"},
		{"cold_warm_recovery_per_minute_x100", "-250", "0 or greater"},
		// Counts, permilles and thresholds: negative is meaningless.
		{"visitor_spawn_chance_permille", "-5", "0 or greater"},
		{"tick_worker_count", "-2", "0 or greater"},
		{"hunger_red_threshold", "-1", "0 or greater"},
		{"world_dusk_time", "   ", "cannot be empty"},
		{"world_timezone", "Mars/Olympus_Mons", "not a known timezone"},
		// Negative durations would produce tight loops or immediate expiry, the
		// same reason the loader refuses them.
		{"eco_social_gap_seconds", "-5", "0 or greater"},
		{"eco_social_gap_seconds", "not-a-number", "whole number of seconds"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"/"+tc.bad, func(t *testing.T) {
			spec, ok := SettingSpecByKey(tc.key)
			if !ok {
				t.Fatalf("%q is not registered", tc.key)
			}
			ws := WorldSettings{}
			err := spec.Apply(&ws, tc.bad)
			if err == nil {
				t.Fatalf("Apply(%q) accepted a malformed value", tc.bad)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the problem (want it to mention %q)", err, tc.want)
			}
		})
	}
}

// TestApplySettingRejectsUnknownKey pins that a typo'd key is an error, not a
// silent no-op that returns 200 and changes nothing.
func TestApplySettingRejectsUnknownKey(t *testing.T) {
	ws := WorldSettings{}
	if _, err := ApplySetting(&ws, "world_dusk_tiem", "19:00"); err == nil {
		t.Fatal("ApplySetting accepted an unknown key")
	}
}

// TestApplySettingTimezoneKeepsLocationInStep pins the coupling that makes
// world_timezone a bespoke spec: Timezone names the zone, Location is what every
// world-instant conversion actually uses, and writing one without the other
// leaves the engine reporting one timezone and computing in another.
func TestApplySettingTimezoneKeepsLocationInStep(t *testing.T) {
	ws := WorldSettings{}
	if _, err := ApplySetting(&ws, "world_timezone", "Europe/London"); err != nil {
		t.Fatalf("ApplySetting: %v", err)
	}
	if ws.Timezone != "Europe/London" {
		t.Errorf("Timezone = %q, want Europe/London", ws.Timezone)
	}
	if ws.Location == nil || ws.Location.String() != "Europe/London" {
		t.Errorf("Location = %v, want Europe/London — the name moved without the zone", ws.Location)
	}

	// A rejected write must leave BOTH fields untouched, so a typo cannot
	// half-apply and desync the pair.
	if _, err := ApplySetting(&ws, "world_timezone", "Nowhere/Fictional"); err == nil {
		t.Fatal("ApplySetting accepted an unloadable timezone")
	}
	if ws.Timezone != "Europe/London" || ws.Location.String() != "Europe/London" {
		t.Errorf("a rejected timezone write mutated state: %q / %v", ws.Timezone, ws.Location)
	}
}

// TestPersistableSettingRowsCoversThePreRegistryKeys pins that the registry-driven
// checkpoint still persists everything the old hand-listed row set did. This is
// the regression that would be invisible otherwise: dropping a key here does not
// fail any other test, it just means that knob silently reverts on the next
// restart — which is exactly the class of bug LLM-577 exists to end.
func TestPersistableSettingRowsCoversThePreRegistryKeys(t *testing.T) {
	// The literal row list SaveMutableSettings carried before LLM-577.
	legacy := []string{
		"world_zoom_min_admin", "world_zoom_min_regular", "agent_ticks_paused",
		"stall_wear_per_coin", "stall_wear_repair_threshold", "stall_wear_degrade_threshold",
		"stall_nails_per_repair", "stall_repair_duration_seconds", "stall_degraded_produce_pct",
		"farm_upkeep_floor", "farm_upkeep_coins_per_shovel",
		"town_rate_coins_per_day", "town_rate_max_owed",
		"huddle_loop_timeout_seconds", "huddle_loop_repeat_percent",
		"huddle_loop_sweep_cadence_seconds", "huddle_loop_max_turns",
		"huddle_conversation_wind_down_seconds",
		"seek_work_coin_ceiling", "seek_work_need_yield_margin",
		"labor_produce_boost_pct", "merchant_coin_floor",
		"eco_enabled", "eco_social_gap_seconds", "eco_economy_gap_seconds",
		"eco_audience_idle_seconds", "constable_rounds_interval_seconds",
	}
	ws := WorldSettings{Timezone: "UTC", Location: time.UTC}
	rows := PersistableSettingRows(&ws)
	for _, key := range legacy {
		if _, ok := rows[key]; !ok {
			t.Errorf("%q was persisted before LLM-577 and is not in the registry's row set — a live tune of it would now silently revert on restart", key)
		}
	}
}
