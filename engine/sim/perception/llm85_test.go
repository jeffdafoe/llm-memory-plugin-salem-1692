package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// llm85_test.go — LLM-85. Tiredness renders on its own situated line: a tier
// phrase (a little tired / weary / exhausted) anchored to real hours awake, with
// NO imperative at any tier (the "address this" cue that drove the re-take_break
// loop is gone — see llm67_test.go). The awareness-floor and weary-threshold
// moves (8→10, 20→16) are exercised in the sim package's needs_test.go. LLM-179
// raised tiredness's own awareness floor to 13 (DefaultTirednessAwarenessFloor)
// so the mild "a little tired" band is [13, 16); the cases below track that.

func intPtr(v int) *int { return &v }

func TestRenderTiredness_Tiers(t *testing.T) {
	threshold := sim.DefaultTirednessRedThreshold // 16
	hours := intPtr(13)
	cases := []struct {
		name    string
		value   int
		wantPre string // "" means the line must be empty
	}{
		{"below floor is silent", 9, ""},
		{"plateau band stays silent (LLM-179)", 12, ""},
		{"at floor is mild", 13, "You're starting to feel a little tired"},
		{"mid mild band", 15, "You're starting to feel a little tired"},
		{"at weary threshold", 16, "You're weary"},
		{"upper weary band", 23, "You're weary"},
		{"peak is exhausted", 24, "You're exhausted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := renderTiredness(c.value, threshold, hours)
			if c.wantPre == "" {
				if line != "" {
					t.Errorf("value %d: want empty, got %q", c.value, line)
				}
				return
			}
			if !strings.HasPrefix(line, c.wantPre) {
				t.Errorf("value %d: want prefix %q, got %q", c.value, c.wantPre, line)
			}
			if strings.Contains(line, "Address now") {
				t.Errorf("value %d: tiredness must carry no imperative: %q", c.value, line)
			}
		})
	}
}

func TestRenderTiredness_HoursAwakeTail(t *testing.T) {
	threshold := sim.DefaultTirednessRedThreshold

	t.Run("plural hours", func(t *testing.T) {
		if line := renderTiredness(16, threshold, intPtr(14)); line != "You're weary — you've been awake for 14 hours." {
			t.Errorf("unexpected: %q", line)
		}
	})
	t.Run("singular hour", func(t *testing.T) {
		if line := renderTiredness(24, threshold, intPtr(1)); line != "You're exhausted — you've been awake for 1 hour." {
			t.Errorf("unexpected: %q", line)
		}
	})
	t.Run("nil hours drops the tail (unscheduled NPC)", func(t *testing.T) {
		if line := renderTiredness(14, threshold, nil); line != "You're starting to feel a little tired." {
			t.Errorf("unexpected: %q", line)
		}
	})
	t.Run("zero hours drops the tail", func(t *testing.T) {
		// A mild but real fatigue (14, just into the [13,16) band): the
		// "awake for 0 hours" wording is guarded out regardless of the value.
		if line := renderTiredness(14, threshold, intPtr(0)); line != "You're starting to feel a little tired." {
			t.Errorf("unexpected: %q", line)
		}
	})
}

func TestComputeHoursAwake(t *testing.T) {
	// Shifts: day = [08:00, 18:00); wrap = [16:00, 03:00).
	cases := []struct {
		name            string
		now, start, end *int
		want            *int
	}{
		{"on-shift day, 8h in", intPtr(16 * 60), intPtr(8 * 60), intPtr(18 * 60), intPtr(8)},
		{"on-shift wrap past midnight, 10h in", intPtr(2 * 60), intPtr(16 * 60), intPtr(3 * 60), intPtr(10)},
		{"just woke at shift-start", intPtr(8 * 60), intPtr(8 * 60), intPtr(18 * 60), intPtr(0)},
		{"off-shift evening drops tail", intPtr(20 * 60), intPtr(8 * 60), intPtr(18 * 60), nil},
		{"off-shift pre-dawn drops tail (the wrap bug)", intPtr(6 * 60), intPtr(8 * 60), intPtr(18 * 60), nil},
		{"nil clock", nil, intPtr(8 * 60), intPtr(18 * 60), nil},
		{"unscheduled actor", intPtr(8 * 60), nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeHoursAwake(c.now, c.start, c.end)
			switch {
			case c.want == nil && got != nil:
				t.Errorf("want nil, got %d", *got)
			case c.want != nil && got == nil:
				t.Errorf("want %d, got nil", *c.want)
			case c.want != nil && got != nil && *got != *c.want:
				t.Errorf("want %d, got %d", *c.want, *got)
			}
		})
	}
}
