package sim

import "testing"

func TestClockHourProse(t *testing.T) {
	cases := []struct {
		minute int
		want   string
	}{
		{0, "midnight"},
		{720, "noon"},
		{1260, "9 in the evening"},      // 21:00 — the LLM-40 close-time example
		{1080, "6 in the evening"},      // 18:00
		{1020, "5 in the evening"},      // 17:00
		{1320, "10 at night"},           // 22:00
		{1439, "11:59 at night"},        // 23:59
		{360, "6 in the morning"},       // 06:00
		{780, "1 in the afternoon"},     // 13:00
		{1290, "9:30 in the evening"},   // 21:30 — non-zero minutes
		{30, "12:30 in the morning"},    // 00:30 — past-midnight, not the "midnight" word
		{750, "12:30 in the afternoon"}, // 12:30 — past-noon, not the "noon" word
		{-1, ""},                        // out of range
		{1440, ""},                      // out of range
	}
	for _, c := range cases {
		if got := ClockHourProse(c.minute); got != c.want {
			t.Errorf("ClockHourProse(%d) = %q, want %q", c.minute, got, c.want)
		}
	}
}
