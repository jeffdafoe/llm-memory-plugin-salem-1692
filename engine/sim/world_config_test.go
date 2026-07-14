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
	res, err := sim.SetHuddleLoopSettings(ip(90), ip(70), ip(20), ip(24), ip(600)).Fn(w)
	if err != nil {
		t.Fatalf("SetHuddleLoopSettings: %v", err)
	}
	out, ok := res.(sim.HuddleLoopSettingsResult)
	if !ok {
		t.Fatalf("result %T, want HuddleLoopSettingsResult", res)
	}
	if out.TimeoutSeconds != 90 || out.RepeatPercent != 70 || out.CadenceSeconds != 20 || out.MaxTurns != 24 {
		t.Errorf("result = %+v, want 90/70/20/24", out)
	}
	if w.Settings.HuddleLoopTimeout != 90*time.Second ||
		w.Settings.HuddleLoopRepeatPercent != 70 ||
		w.Settings.HuddleLoopSweepCadence != 20*time.Second ||
		w.Settings.HuddleLoopMaxTurns != 24 {
		t.Errorf("settings = %v/%d/%v/%d, want 90s/70/20s/24",
			w.Settings.HuddleLoopTimeout, w.Settings.HuddleLoopRepeatPercent, w.Settings.HuddleLoopSweepCadence, w.Settings.HuddleLoopMaxTurns)
	}

	// Partial update: repeat_percent only; timeout + cadence + max_turns unchanged.
	if _, err := sim.SetHuddleLoopSettings(nil, ip(55), nil, nil, nil).Fn(w); err != nil {
		t.Fatalf("partial tune: %v", err)
	}
	if w.Settings.HuddleLoopTimeout != 90*time.Second ||
		w.Settings.HuddleLoopRepeatPercent != 55 ||
		w.Settings.HuddleLoopSweepCadence != 20*time.Second ||
		w.Settings.HuddleLoopMaxTurns != 24 {
		t.Errorf("after partial = %v/%d/%v/%d, want 90s/55/20s/24",
			w.Settings.HuddleLoopTimeout, w.Settings.HuddleLoopRepeatPercent, w.Settings.HuddleLoopSweepCadence, w.Settings.HuddleLoopMaxTurns)
	}

	// A max_turns-only tune with an unset stored value echoes the effective
	// default resolution path: result reports the concrete value just set.
	if res, err := sim.SetHuddleLoopSettings(nil, nil, nil, ip(10), nil).Fn(w); err != nil {
		t.Fatalf("max_turns tune: %v", err)
	} else if out := res.(sim.HuddleLoopSettingsResult); out.MaxTurns != 10 {
		t.Errorf("MaxTurns after tune = %d, want 10", out.MaxTurns)
	}

	// Master off-switch: timeout 0 is valid (disables the sweep).
	if _, err := sim.SetHuddleLoopSettings(ip(0), nil, nil, nil, nil).Fn(w); err != nil {
		t.Fatalf("timeout 0 should be valid (disable): %v", err)
	}
	if w.Settings.HuddleLoopTimeout != 0 {
		t.Errorf("timeout after disable = %v, want 0", w.Settings.HuddleLoopTimeout)
	}

	bad := []struct {
		name                                          string
		timeout, percent, cadence, maxTurns, windDown *int
	}{
		{"none provided", nil, nil, nil, nil, nil},
		{"negative timeout", ip(-1), nil, nil, nil, nil},
		{"percent zero", nil, ip(0), nil, nil, nil},
		{"percent over 100", nil, ip(101), nil, nil, nil},
		{"cadence zero", nil, nil, ip(0), nil, nil},
		{"cadence negative", nil, nil, ip(-5), nil, nil},
		{"max_turns zero", nil, nil, nil, ip(0), nil},
		{"max_turns negative", nil, nil, nil, ip(-3), nil},
		{"wind_down zero", nil, nil, nil, nil, ip(0)},
		{"wind_down negative", nil, nil, nil, nil, ip(-60)},
	}
	for _, c := range bad {
		if _, err := sim.SetHuddleLoopSettings(c.timeout, c.percent, c.cadence, c.maxTurns, c.windDown).Fn(newConfigWorld(t)); !errors.Is(err, sim.ErrInvalidHuddleLoopSetting) {
			t.Errorf("%s: err = %v, want ErrInvalidHuddleLoopSetting", c.name, err)
		}
	}
}

// TestSetSeekWorkCoinCeiling covers the LLM-194 live-tune command: a valid set lands on
// WorldSettings and echoes in the result; a missing/zero/negative ceiling is rejected
// (a zero would suppress seek-work for everyone) and leaves the prior value intact.
func TestSetSeekWorkCoinCeiling(t *testing.T) {
	ip := func(v int) *int { return &v }

	w := newConfigWorld(t)
	res, err := sim.SetSeekWorkCoinCeiling(ip(30)).Fn(w)
	if err != nil {
		t.Fatalf("SetSeekWorkCoinCeiling: %v", err)
	}
	out, ok := res.(sim.SeekWorkCeilingSettingResult)
	if !ok {
		t.Fatalf("result %T, want SeekWorkCeilingSettingResult", res)
	}
	if out.CoinCeiling != 30 {
		t.Errorf("result = %+v, want CoinCeiling 30", out)
	}
	if w.Settings.SeekWorkCoinCeiling != 30 {
		t.Errorf("settings ceiling = %d, want 30", w.Settings.SeekWorkCoinCeiling)
	}

	bad := []struct {
		name    string
		ceiling *int
	}{
		{"missing", nil},
		{"zero", ip(0)},
		{"negative", ip(-5)},
	}
	for _, c := range bad {
		if _, err := sim.SetSeekWorkCoinCeiling(c.ceiling).Fn(w); !errors.Is(err, sim.ErrInvalidSeekWorkCeilingSetting) {
			t.Errorf("%s: err = %v, want ErrInvalidSeekWorkCeilingSetting", c.name, err)
		}
	}
	// A rejected set must not mutate the prior value.
	if w.Settings.SeekWorkCoinCeiling != 30 {
		t.Errorf("ceiling after rejected sets = %d, want 30 (unchanged)", w.Settings.SeekWorkCoinCeiling)
	}
}

// TestSetSeekWorkNeedYieldMargin covers the LLM-276 live-tune command: a valid set
// lands on WorldSettings and echoes in the result; a missing/zero/negative margin is
// rejected (a zero would collapse the redirect band) and leaves the prior value intact.
func TestSetSeekWorkNeedYieldMargin(t *testing.T) {
	ip := func(v int) *int { return &v }

	w := newConfigWorld(t)
	res, err := sim.SetSeekWorkNeedYieldMargin(ip(7)).Fn(w)
	if err != nil {
		t.Fatalf("SetSeekWorkNeedYieldMargin: %v", err)
	}
	out, ok := res.(sim.SeekWorkNeedMarginSettingResult)
	if !ok {
		t.Fatalf("result %T, want SeekWorkNeedMarginSettingResult", res)
	}
	if out.Margin != 7 {
		t.Errorf("result = %+v, want Margin 7", out)
	}
	if w.Settings.SeekWorkNeedYieldMargin != 7 {
		t.Errorf("settings margin = %d, want 7", w.Settings.SeekWorkNeedYieldMargin)
	}

	bad := []struct {
		name   string
		margin *int
	}{
		{"missing", nil},
		{"zero", ip(0)},
		{"negative", ip(-3)},
	}
	for _, c := range bad {
		if _, err := sim.SetSeekWorkNeedYieldMargin(c.margin).Fn(w); !errors.Is(err, sim.ErrInvalidSeekWorkNeedMarginSetting) {
			t.Errorf("%s: err = %v, want ErrInvalidSeekWorkNeedMarginSetting", c.name, err)
		}
	}
	if w.Settings.SeekWorkNeedYieldMargin != 7 {
		t.Errorf("margin after rejected sets = %d, want 7 (unchanged)", w.Settings.SeekWorkNeedYieldMargin)
	}
}

// TestSetMerchantCoinFloor covers the LLM-294 live-tune command: a valid set lands on
// WorldSettings and echoes in the result; 0 is VALID (the off-switch, unlike the
// seek-work ceiling); a missing/negative value is rejected and leaves the prior value
// intact.
func TestSetMerchantCoinFloor(t *testing.T) {
	ip := func(v int) *int { return &v }

	w := newConfigWorld(t)
	res, err := sim.SetMerchantCoinFloor(ip(15)).Fn(w)
	if err != nil {
		t.Fatalf("SetMerchantCoinFloor: %v", err)
	}
	out, ok := res.(sim.MerchantCoinFloorSettingResult)
	if !ok {
		t.Fatalf("result %T, want MerchantCoinFloorSettingResult", res)
	}
	if out.CoinFloor != 15 {
		t.Errorf("result = %+v, want CoinFloor 15", out)
	}
	if w.Settings.MerchantCoinFloor != 15 {
		t.Errorf("settings floor = %d, want 15", w.Settings.MerchantCoinFloor)
	}

	// 0 is the explicit off-switch and must be accepted (unlike SeekWorkCoinCeiling).
	if _, err := sim.SetMerchantCoinFloor(ip(0)).Fn(w); err != nil {
		t.Errorf("SetMerchantCoinFloor(0) = %v, want nil (0 is the off-switch)", err)
	}
	if w.Settings.MerchantCoinFloor != 0 {
		t.Errorf("settings floor = %d, want 0 after off-switch set", w.Settings.MerchantCoinFloor)
	}

	bad := []struct {
		name  string
		floor *int
	}{
		{"missing", nil},
		{"negative", ip(-5)},
	}
	for _, c := range bad {
		if _, err := sim.SetMerchantCoinFloor(c.floor).Fn(w); !errors.Is(err, sim.ErrInvalidMerchantCoinFloorSetting) {
			t.Errorf("%s: err = %v, want ErrInvalidMerchantCoinFloorSetting", c.name, err)
		}
	}
	// A rejected set must not mutate the prior value (still 0 from the off-switch set).
	if w.Settings.MerchantCoinFloor != 0 {
		t.Errorf("floor after rejected sets = %d, want 0 (unchanged)", w.Settings.MerchantCoinFloor)
	}
}
