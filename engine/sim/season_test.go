package sim

import (
	"testing"
	"time"
)

func TestSeasonOf_WholeMonths(t *testing.T) {
	want := map[time.Month]Season{
		time.January: SeasonWinter, time.February: SeasonWinter, time.March: SeasonWinter,
		time.April: SeasonSpring, time.May: SeasonSpring, time.June: SeasonSummer,
		time.July: SeasonSummer, time.August: SeasonSummer, time.September: SeasonAutumn,
		time.October: SeasonAutumn, time.November: SeasonAutumn, time.December: SeasonWinter,
	}
	for m := time.January; m <= time.December; m++ {
		if got := SeasonOf(m); got != want[m] {
			t.Errorf("SeasonOf(%s) = %q, want %q", m, got, want[m])
		}
	}
	for _, m := range []time.Month{0, 13} {
		if got := SeasonOf(m); got != "" {
			t.Errorf("SeasonOf(%d) = %q, want empty", m, got)
		}
	}
}

func TestVillageMonthDay_UsesVillageTimeZone(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	w := &World{Settings: WorldSettings{Location: loc}}
	// 03:30 UTC on March 1 is still the evening of February 28 in the village:
	// the date, and so the season, follow the village clock.
	month, day := VillageMonthDay(w, time.Date(2026, 3, 1, 3, 30, 0, 0, time.UTC))
	if month != time.February || day != 28 {
		t.Errorf("date = %s %d, want February 28 (village local)", month, day)
	}
	if got := SeasonOf(month); got != SeasonWinter {
		t.Errorf("season = %q, want winter", got)
	}
}

func TestVillageMonthDay_NoLocationReadsTheInstant(t *testing.T) {
	w := &World{}
	month, day := VillageMonthDay(w, time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC))
	if month != time.December || day != 31 {
		t.Errorf("date = %s %d, want December 31", month, day)
	}
}

func TestOverrideVillageMonth(t *testing.T) {
	cases := []struct {
		name     string
		month    time.Month
		day      int
		override int
		wantM    time.Month
		wantDay  int
	}{
		{"off", time.October, 31, 0, time.October, 31},
		{"January", time.November, 2, 1, time.January, 2},
		{"day kept when it fits", time.October, 30, 12, time.December, 30},
		{"day cut to the month's end", time.October, 31, 11, time.November, 30},
		{"1692 is a leap year", time.October, 31, 2, time.February, 29},
		{"out of range reads as off", time.October, 22, 13, time.October, 22},
		{"negative reads as off", time.October, 22, -1, time.October, 22},
	}
	for _, tc := range cases {
		m, day := OverrideVillageMonth(tc.month, tc.day, tc.override)
		if m != tc.wantM || day != tc.wantDay {
			t.Errorf("%s: OverrideVillageMonth(%s %d, %d) = %s %d, want %s %d",
				tc.name, tc.month, tc.day, tc.override, m, day, tc.wantM, tc.wantDay)
		}
	}
}

// The override reaches every World-side reader of the village month: here the
// date the prose prompts are written with (LLM-889).
func TestVillageMonthDay_OverrideReachesTheProseDate(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	w := newAtmosphereTestWorld(t)
	w.Settings.Location = loc
	w.Settings.VillageMonthOverride = 1
	v := runAtmosphereCmd(t, w, FetchVillageContext(time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)))
	ctx, ok := v.(VillageContext)
	if !ok {
		t.Fatalf("FetchVillageContext returned %T, want VillageContext", v)
	}
	if ctx.Month != time.January || ctx.Day != 22 {
		t.Errorf("context date = %s %d, want January 22 (override month, village-local day)", ctx.Month, ctx.Day)
	}
}

// The snapshot carries the override beside the real date: the village month
// follows the override, LocalDateUTC (the order book's "today") does not.
func TestSnapshotVillageMonthDay(t *testing.T) {
	realDate := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		snap    Snapshot
		wantM   time.Month
		wantDay int
	}{
		{"no village clock", Snapshot{VillageMonthOverride: 1}, 0, 0},
		{"off", Snapshot{LocalDateUTC: realDate}, time.October, 31},
		{"February", Snapshot{LocalDateUTC: realDate, VillageMonthOverride: 2}, time.February, 29},
	}
	for _, tc := range cases {
		m, day := tc.snap.VillageMonthDay()
		if m != tc.wantM || day != tc.wantDay {
			t.Errorf("%s: VillageMonthDay = %s %d, want %s %d", tc.name, m, day, tc.wantM, tc.wantDay)
		}
	}

	w := newAtmosphereTestWorld(t)
	w.Settings.VillageMonthOverride = 1
	w.republish()
	snap := w.Published()
	if snap.VillageMonthOverride != 1 {
		t.Errorf("published VillageMonthOverride = %d, want 1", snap.VillageMonthOverride)
	}
	if m, _ := snap.VillageMonthDay(); m != time.January {
		t.Errorf("published village month = %s, want January", m)
	}
	if got, want := snap.LocalDateUTC, orderDateUTC(snap.PublishedAt, w.Settings.Location); !got.Equal(want) {
		t.Errorf("LocalDateUTC = %s, want the real date %s", got, want)
	}
}

func TestVillageDateScene(t *testing.T) {
	cases := []struct {
		month time.Month
		day   int
		want  string
	}{
		{time.October, 22, "It is October 22, 1692, in the autumn."},
		{time.January, 1, "It is January 1, 1692, in the winter."},
		{time.December, 31, "It is December 31, 1692, in the winter."},
		{time.June, 15, "It is June 15, 1692, in the summer."},
		{0, 0, ""},
		{time.October, 0, ""},
	}
	for _, tc := range cases {
		if got := VillageDateScene(tc.month, tc.day); got != tc.want {
			t.Errorf("VillageDateScene(%d, %d) = %q, want %q", tc.month, tc.day, got, tc.want)
		}
	}
}

func TestFetchVillageContext_CarriesTheVillageDate(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	w := newAtmosphereTestWorld(t)
	w.Settings.Location = loc
	v := runAtmosphereCmd(t, w, FetchVillageContext(time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)))
	ctx, ok := v.(VillageContext)
	if !ok {
		t.Fatalf("FetchVillageContext returned %T, want VillageContext", v)
	}
	if ctx.Month != time.October || ctx.Day != 22 {
		t.Errorf("context date = %s %d, want October 22 (village local)", ctx.Month, ctx.Day)
	}
}

func TestVillageDateEndsAt(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	// 02:00 UTC on October 23 is the evening of October 22 in the village; the
	// date ends at the village's midnight, 04:00 UTC (EDT).
	got := VillageDateEndsAt(time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC), loc)
	if want := time.Date(2026, 10, 23, 4, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("ends = %s, want %s", got.UTC(), want)
	}
	// Year end rolls into January 1.
	got = VillageDateEndsAt(time.Date(2026, 12, 31, 18, 0, 0, 0, loc), loc)
	if want := time.Date(2027, 1, 1, 0, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("year-end ends = %s, want %s", got, want)
	}
	// November 1, 2026 is the fall-back day: that village date lasts 25 hours.
	start := VillageDateEndsAt(time.Date(2026, 10, 31, 12, 0, 0, 0, loc), loc)
	end := VillageDateEndsAt(time.Date(2026, 11, 1, 12, 0, 0, 0, loc), loc)
	if d := end.Sub(start); d != 25*time.Hour {
		t.Errorf("November 1 lasts %s, want 25h", d)
	}
	// No location reads UTC.
	got = VillageDateEndsAt(time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC), nil)
	if want := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("no-location ends = %s, want %s", got, want)
	}
}

func TestVillageUTCOffset_FixedInstants(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	if off, ok := VillageUTCOffset(time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC), loc); !ok || off != -5*3600 {
		t.Errorf("January = %d ok=%v, want -18000 (EST)", off, ok)
	}
	if off, ok := VillageUTCOffset(time.Date(2027, 7, 15, 12, 0, 0, 0, time.UTC), loc); !ok || off != -4*3600 {
		t.Errorf("July = %d ok=%v, want -14400 (EDT)", off, ok)
	}
	if _, ok := VillageUTCOffset(time.Now(), nil); ok {
		t.Error("no Location: ok = true, want false")
	}
}

// TestVillageUTCOffsetChangesAt finds each DST transition to the second inside
// the village date it falls on, and nothing on an ordinary day.
func TestVillageUTCOffsetChangesAt(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		// Fall back: 2am EDT on November 1, 2026 is 06:00 UTC.
		{"fall back", time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)},
		// Spring forward: 2am EST on March 14, 2027 is 07:00 UTC.
		{"spring forward", time.Date(2027, 3, 14, 5, 0, 0, 0, time.UTC), time.Date(2027, 3, 14, 7, 0, 0, 0, time.UTC)},
		// A read 13 s before the change still lands on it.
		{"just before", time.Date(2026, 11, 1, 5, 59, 47, 500, time.UTC), time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)},
		{"ordinary day", time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC), time.Time{}},
		// After the change on the same date: nothing left to report.
		{"after the change", time.Date(2026, 11, 1, 6, 0, 1, 0, time.UTC), time.Time{}},
	}
	for _, c := range cases {
		got := VillageUTCOffsetChangesAt(c.now, VillageDateEndsAt(c.now, loc), loc)
		if !got.Equal(c.want) {
			t.Errorf("%s: changes at %s, want %s", c.name, got.UTC(), c.want)
		}
	}
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	if got := VillageUTCOffsetChangesAt(now, now.Add(-time.Hour), loc); !got.IsZero() {
		t.Errorf("window ending before now: %s, want zero", got)
	}
	if got := VillageUTCOffsetChangesAt(now, now.Add(24*time.Hour), nil); !got.IsZero() {
		t.Errorf("no Location: %s, want zero", got)
	}
}
