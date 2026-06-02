package sim_test

import (
	"errors"
	"testing"

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

	cp := w.BuildCheckpointSnapshot()
	if cp.MutableSettings.ZoomMinAdmin != 0.11 ||
		cp.MutableSettings.ZoomMinRegular != 0.22 ||
		!cp.MutableSettings.AgentTicksPaused {
		t.Errorf("MutableSettings = %+v, want 0.11/0.22/true", cp.MutableSettings)
	}
}
