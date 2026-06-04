package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestTimeOfDayProse pins the wall-clock-minute → ambient-prose band mapping at
// every band boundary. ZBBS-HOME-351.
func TestTimeOfDayProse(t *testing.T) {
	cases := []struct {
		minute int
		want   string
	}{
		{0, "It is the dead of night."},
		{299, "It is the dead of night."},
		{300, "Dawn is breaking over the village."},
		{419, "Dawn is breaking over the village."},
		{420, "It is morning in the village."},
		{719, "It is morning in the village."},
		{720, "It is midday."},
		{839, "It is midday."},
		{840, "The afternoon wears on."},
		{1079, "The afternoon wears on."},
		{1080, "Evening settles over the village."},
		{1259, "Evening settles over the village."},
		{1260, "Night lies over the village."},
		{1439, "Night lies over the village."},
		{-1, ""},   // out of range — fail closed
		{1440, ""}, // out of range — fail closed
		{5000, ""},
	}
	for _, c := range cases {
		if got := timeOfDayProse(c.minute); got != c.want {
			t.Errorf("timeOfDayProse(%d) = %q, want %q", c.minute, got, c.want)
		}
	}
}

// TestRenderSurroundings_TimeOfDay confirms the time-of-day line renders when
// the clock is known, is omitted when nil, and precedes the atmosphere line.
// ZBBS-HOME-351.
func TestRenderSurroundings_TimeOfDay(t *testing.T) {
	render := func(s SurroundingsView) string {
		var b strings.Builder
		renderSurroundings(&b, s)
		return b.String()
	}
	evening := 1140

	t.Run("rendered as ambient prose when known", func(t *testing.T) {
		got := render(SurroundingsView{LocalMinuteOfDay: &evening})
		if !strings.Contains(got, "Evening settles over the village.") {
			t.Errorf("want evening time-of-day line, got: %q", got)
		}
	})

	t.Run("omitted when the clock is unknown", func(t *testing.T) {
		got := render(SurroundingsView{})
		// Assert absence of each exact band string rather than a broad
		// substring, so a future legitimate "village" line elsewhere in
		// renderSurroundings doesn't make this test a false negative.
		for _, band := range []string{
			"It is the dead of night.",
			"Dawn is breaking over the village.",
			"It is morning in the village.",
			"It is midday.",
			"The afternoon wears on.",
			"Evening settles over the village.",
			"Night lies over the village.",
		} {
			if strings.Contains(got, band) {
				t.Errorf("want no time-of-day line for nil clock, found %q in: %q", band, got)
			}
		}
	})

	// (The former "time line precedes the atmosphere line" subtest was removed in
	// ZBBS-WORK-374 — the literary atmosphere line is no longer rendered into the
	// decision prompt, so there is no longer an ordering relationship to assert.
	// TestRenderSurroundings_AtmosphereLine guards that atmosphere stays out.)
}

// TestBuildSurroundings_TimeOfDay confirms Build copies the snapshot clock into
// the view (and leaves it nil when the snapshot has none). ZBBS-HOME-351.
func TestBuildSurroundings_TimeOfDay(t *testing.T) {
	minute := 800
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &minute,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{"a1": {}},
	}
	st := buildSurroundings(snap, "a1", snap.Actors["a1"])
	if st.LocalMinuteOfDay == nil || *st.LocalMinuteOfDay != 800 {
		t.Errorf("LocalMinuteOfDay = %v, want pointer to 800", st.LocalMinuteOfDay)
	}

	bare := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"a1": {}}}
	stBare := buildSurroundings(bare, "a1", bare.Actors["a1"])
	if stBare.LocalMinuteOfDay != nil {
		t.Errorf("LocalMinuteOfDay = %v, want nil when the snapshot has no clock", stBare.LocalMinuteOfDay)
	}
}
