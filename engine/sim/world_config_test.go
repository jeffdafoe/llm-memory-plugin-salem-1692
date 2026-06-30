package sim_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// world_config_test.go — ZBBS-WORK-363 admin world-config write commands.

func newConfigWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	return sim.NewWorld(repo)
}

func TestSetZoomSettings_UpdatesBothAndEmits(t *testing.T) {
	w := newConfigWorld(t)
	var got sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) { got = e }))

	admin, regular := 0.15, 0.25
	res, err := sim.SetZoomSettings(&admin, &regular).Fn(w)
	if err != nil {
		t.Fatalf("SetZoomSettings: %v", err)
	}
	out, ok := res.(sim.SetZoomSettingsResult)
	if !ok {
		t.Fatalf("result %T, want SetZoomSettingsResult", res)
	}
	if out.ZoomMinAdmin != 0.15 || out.ZoomMinRegular != 0.25 {
		t.Errorf("result = %+v, want 0.15/0.25", out)
	}
	if w.Settings.ZoomMinAdmin != 0.15 || w.Settings.ZoomMinRegular != 0.25 {
		t.Errorf("settings = %v/%v, want 0.15/0.25", w.Settings.ZoomMinAdmin, w.Settings.ZoomMinRegular)
	}
	zc, ok := got.(*sim.ZoomSettingsChanged)
	if !ok {
		t.Fatalf("event %T, want *ZoomSettingsChanged", got)
	}
	if zc.ZoomMinAdmin != 0.15 || zc.ZoomMinRegular != 0.25 {
		t.Errorf("event = %v/%v, want 0.15/0.25", zc.ZoomMinAdmin, zc.ZoomMinRegular)
	}
}

func TestSetZoomSettings_OneLeavesOtherUnchanged(t *testing.T) {
	w := newConfigWorld(t)
	w.Settings.ZoomMinRegular = 0.30

	admin := 0.10
	if _, err := sim.SetZoomSettings(&admin, nil).Fn(w); err != nil {
		t.Fatalf("SetZoomSettings: %v", err)
	}
	if w.Settings.ZoomMinAdmin != 0.10 {
		t.Errorf("admin = %v, want 0.10", w.Settings.ZoomMinAdmin)
	}
	if w.Settings.ZoomMinRegular != 0.30 {
		t.Errorf("regular = %v, want 0.30 (unchanged)", w.Settings.ZoomMinRegular)
	}
}

func TestSetZoomSettings_NeitherProvidedIsError(t *testing.T) {
	w := newConfigWorld(t)
	if _, err := sim.SetZoomSettings(nil, nil).Fn(w); !errors.Is(err, sim.ErrInvalidZoomSetting) {
		t.Errorf("err = %v, want ErrInvalidZoomSetting", err)
	}
}

func TestSetZoomSettings_NonPositiveIsError(t *testing.T) {
	w := newConfigWorld(t)
	bad := -0.1
	if _, err := sim.SetZoomSettings(&bad, nil).Fn(w); !errors.Is(err, sim.ErrInvalidZoomSetting) {
		t.Errorf("err = %v, want ErrInvalidZoomSetting", err)
	}
	zero := 0.0
	if _, err := sim.SetZoomSettings(nil, &zero).Fn(w); !errors.Is(err, sim.ErrInvalidZoomSetting) {
		t.Errorf("zero floor: err = %v, want ErrInvalidZoomSetting", err)
	}
}

func TestSetAgentTicksPaused_MutatesAndEmits(t *testing.T) {
	w := newConfigWorld(t)
	var got sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) { got = e }))

	res, err := sim.SetAgentTicksPaused(true).Fn(w)
	if err != nil {
		t.Fatalf("SetAgentTicksPaused: %v", err)
	}
	if out, ok := res.(sim.SetAgentTicksPausedResult); !ok || !out.Paused {
		t.Errorf("result = %+v, want Paused=true", res)
	}
	if !w.Settings.AgentTicksPaused {
		t.Error("settings AgentTicksPaused = false, want true")
	}
	atc, ok := got.(*sim.AgentTicksPausedChanged)
	if !ok || !atc.Paused {
		t.Fatalf("event = %#v, want *AgentTicksPausedChanged{Paused:true}", got)
	}
}

// TestBuildCheckpointSnapshot_CarriesMutableSettings pins that the runtime-
// tunable subset rides the checkpoint (the durability path for the config
// writes) — and ONLY that subset (the rest of WorldSettings is load-once).
func TestBuildCheckpointSnapshot_CarriesMutableSettings(t *testing.T) {
	w := newConfigWorld(t)
	w.Settings.ZoomMinAdmin = 0.11
	w.Settings.ZoomMinRegular = 0.22
	w.Settings.AgentTicksPaused = true
	// Huddle loop-sweep knobs (LLM-183) ride the checkpoint too, in seconds — the
	// durability path that lets a live tune survive restart (SaveMutableSettings
	// writes these keys back to the setting table; the load path parses them back).
	w.Settings.HuddleLoopTimeout = 75 * time.Second
	w.Settings.HuddleLoopRepeatPercent = 65
	w.Settings.HuddleLoopSweepCadence = 25 * time.Second

	cp := w.BuildCheckpointSnapshot()
	if cp.MutableSettings.ZoomMinAdmin != 0.11 ||
		cp.MutableSettings.ZoomMinRegular != 0.22 ||
		!cp.MutableSettings.AgentTicksPaused {
		t.Errorf("MutableSettings = %+v, want 0.11/0.22/true", cp.MutableSettings)
	}
	if cp.MutableSettings.HuddleLoopTimeoutSeconds != 75 ||
		cp.MutableSettings.HuddleLoopRepeatPercent != 65 ||
		cp.MutableSettings.HuddleLoopSweepCadenceSeconds != 25 {
		t.Errorf("MutableSettings huddle-loop = %d/%d/%d, want 75/65/25",
			cp.MutableSettings.HuddleLoopTimeoutSeconds,
			cp.MutableSettings.HuddleLoopRepeatPercent,
			cp.MutableSettings.HuddleLoopSweepCadenceSeconds)
	}
}

// TestSetHuddleLoopSettings covers the LLM-183 loop-sweep tune command: per-knob
// apply (seconds -> Duration), partial update leaves others alone, the master
// off-switch (timeout 0 is valid), and the validation rejections.
func TestSetHuddleLoopSettings(t *testing.T) {
	ip := func(v int) *int { return &v }

	w := newConfigWorld(t)
	res, err := sim.SetHuddleLoopSettings(ip(90), ip(70), ip(20)).Fn(w)
	if err != nil {
		t.Fatalf("SetHuddleLoopSettings: %v", err)
	}
	out, ok := res.(sim.HuddleLoopSettingsResult)
	if !ok {
		t.Fatalf("result %T, want HuddleLoopSettingsResult", res)
	}
	if out.TimeoutSeconds != 90 || out.RepeatPercent != 70 || out.CadenceSeconds != 20 {
		t.Errorf("result = %+v, want 90/70/20", out)
	}
	if w.Settings.HuddleLoopTimeout != 90*time.Second ||
		w.Settings.HuddleLoopRepeatPercent != 70 ||
		w.Settings.HuddleLoopSweepCadence != 20*time.Second {
		t.Errorf("settings = %v/%d/%v, want 90s/70/20s",
			w.Settings.HuddleLoopTimeout, w.Settings.HuddleLoopRepeatPercent, w.Settings.HuddleLoopSweepCadence)
	}

	// Partial update: repeat_percent only; timeout + cadence unchanged.
	if _, err := sim.SetHuddleLoopSettings(nil, ip(55), nil).Fn(w); err != nil {
		t.Fatalf("partial tune: %v", err)
	}
	if w.Settings.HuddleLoopTimeout != 90*time.Second ||
		w.Settings.HuddleLoopRepeatPercent != 55 ||
		w.Settings.HuddleLoopSweepCadence != 20*time.Second {
		t.Errorf("after partial = %v/%d/%v, want 90s/55/20s",
			w.Settings.HuddleLoopTimeout, w.Settings.HuddleLoopRepeatPercent, w.Settings.HuddleLoopSweepCadence)
	}

	// Master off-switch: timeout 0 is valid (disables the sweep).
	if _, err := sim.SetHuddleLoopSettings(ip(0), nil, nil).Fn(w); err != nil {
		t.Fatalf("timeout 0 should be valid (disable): %v", err)
	}
	if w.Settings.HuddleLoopTimeout != 0 {
		t.Errorf("timeout after disable = %v, want 0", w.Settings.HuddleLoopTimeout)
	}

	bad := []struct {
		name                      string
		timeout, percent, cadence *int
	}{
		{"none provided", nil, nil, nil},
		{"negative timeout", ip(-1), nil, nil},
		{"percent zero", nil, ip(0), nil},
		{"percent over 100", nil, ip(101), nil},
		{"cadence zero", nil, nil, ip(0)},
		{"cadence negative", nil, nil, ip(-5)},
	}
	for _, c := range bad {
		if _, err := sim.SetHuddleLoopSettings(c.timeout, c.percent, c.cadence).Fn(newConfigWorld(t)); !errors.Is(err, sim.ErrInvalidHuddleLoopSetting) {
			t.Errorf("%s: err = %v, want ErrInvalidHuddleLoopSetting", c.name, err)
		}
	}
}
