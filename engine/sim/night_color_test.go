package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// night_color_test.go — LLM-842.

func TestNormalizeNightColorUpperCasesAndTrims(t *testing.T) {
	got, err := sim.NormalizeNightColor("  #1a2b3c ")
	if err != nil {
		t.Fatalf("NormalizeNightColor: %v", err)
	}
	if got != "#1A2B3C" {
		t.Errorf("got %q, want #1A2B3C", got)
	}
}

func TestApplyNightColorSettingThenSettingWrittenEmits(t *testing.T) {
	w := newConfigWorld(t)
	var got sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) { got = e }))

	spec, err := sim.ApplySetting(&w.Settings, sim.NightColorSettingKey, "#101820")
	if err != nil {
		t.Fatalf("ApplySetting: %v", err)
	}
	sim.SettingWritten(w, spec.Key)

	if w.Settings.NightColor != "#101820" {
		t.Errorf("settings = %q, want #101820", w.Settings.NightColor)
	}
	nc, ok := got.(*sim.NightColorChanged)
	if !ok {
		t.Fatalf("event %T, want *NightColorChanged", got)
	}
	if nc.NightColor != "#101820" {
		t.Errorf("event = %q, want #101820", nc.NightColor)
	}
}

func TestSettingWrittenIgnoresOtherKeys(t *testing.T) {
	w := newConfigWorld(t)
	var got sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) { got = e }))

	sim.SettingWritten(w, "world_dusk_time")
	if got != nil {
		t.Errorf("event %T emitted for a key the client does not render live", got)
	}
}
