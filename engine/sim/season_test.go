package sim

import (
	"testing"
	"time"
)

func TestSeasonOf_WholeMonths(t *testing.T) {
	want := map[time.Month]Season{
		time.January: SeasonWinter, time.February: SeasonWinter, time.March: SeasonSpring,
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
