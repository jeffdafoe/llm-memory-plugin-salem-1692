package sim

import (
	"testing"
	"time"
)

// stay_open_test.go — ZBBS-WORK-387. Covers resolveOpenUntil (next-occurrence
// hour math), the StayOpen command (stamp + already-committed reject), and the
// shiftDutyTarget OpenUntil suppression (which yields to peak exhaustion) plus
// the housing-aware lodger wind-down target. Same hand-built-world style as
// shift_duty_test.go / lodger_rebook_test.go.

func TestResolveOpenUntil(t *testing.T) {
	at := time.Date(2026, 6, 9, 21, 30, 0, 0, time.UTC) // 21:30 local (UTC test loc)
	cases := []struct {
		name      string
		untilHour int
		want      time.Time
		wantErr   bool
	}{
		{"later today", 23, time.Date(2026, 6, 9, 23, 0, 0, 0, time.UTC), false},
		{"past hour rolls to tomorrow", 21, time.Date(2026, 6, 10, 21, 0, 0, 0, time.UTC), false},
		{"after midnight", 1, time.Date(2026, 6, 10, 1, 0, 0, 0, time.UTC), false},
		{"midnight is valid", 0, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), false},
		{"out of range high", 24, time.Time{}, true},
		{"out of range low", -1, time.Time{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveOpenUntil(time.UTC, c.untilHour, at)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (result %v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(c.want) {
				t.Errorf("resolveOpenUntil(%d) = %v, want %v", c.untilHour, got, c.want)
			}
		})
	}
}

func TestStayOpen_StampsAndRejectsRepeat(t *testing.T) {
	at := time.Date(2026, 6, 9, 21, 30, 0, 0, time.UTC)
	a := shiftNPC("ezekiel", KindNPCStateful, "smithy", "home", "smithy")
	w := sleepTestWorld(a)

	if _, err := StayOpen("ezekiel", "an order I still owe", 23, at).Fn(w); err != nil {
		t.Fatalf("StayOpen: %v", err)
	}
	want := time.Date(2026, 6, 9, 23, 0, 0, 0, time.UTC)
	if a.OpenUntil == nil || !a.OpenUntil.Equal(want) {
		t.Fatalf("OpenUntil = %v, want %v", a.OpenUntil, want)
	}

	// Second call while still committed → reject (no silent re-stamp), mirroring
	// take_break's already-on-break gate.
	if _, err := StayOpen("ezekiel", "again", 22, at).Fn(w); err == nil {
		t.Error("second stay_open while committed should be rejected")
	}
}

// TestShiftDutyTarget_OpenUntilSuppressesWindDown — the core ZBBS-WORK-387
// behavior: a stay-open commitment suppresses the routine off-shift wind-down,
// but yields to peak exhaustion (the needs floor wins) and is inert once expired.
func TestShiftDutyTarget_OpenUntilSuppressesWindDown(t *testing.T) {
	now := time.Date(2026, 6, 9, 22, 0, 0, 0, time.UTC)
	future := now.Add(2 * time.Hour)

	// Homed keeper, off-shift (07:00–16:00), away from home, OpenUntil set, NOT
	// exhausted → routine wind-down suppressed.
	a := shiftNPC("k", KindNPCStateful, "shop", "home", "shop")
	a.ScheduleStartMin = intptr(420)
	a.ScheduleEndMin = intptr(960)
	a.OpenUntil = &future
	w := sleepTestWorld(a)
	if _, _, ok := shiftDutyTarget(w, a, 1320, now); ok { // 22:00 == minute 1320, off shift
		t.Error("OpenUntil (not peak) should suppress the wind-down duty")
	}

	// Same keeper at peak exhaustion → the commitment yields; wind-down resumes.
	a.Needs["tiredness"] = 24 // peak — exhausted
	target, toWork, ok := shiftDutyTarget(w, a, 1320, now)
	if !ok || target != "home" || toWork {
		t.Errorf("at peak the wind-down must override OpenUntil; got (%q,%v,%v)", target, toWork, ok)
	}

	// Expired OpenUntil → no suppression (wind-down fires).
	past := now.Add(-time.Hour)
	a.Needs["tiredness"] = 0
	a.OpenUntil = &past
	if _, _, ok := shiftDutyTarget(w, a, 1320, now); !ok {
		t.Error("expired OpenUntil should not suppress the wind-down")
	}
}

// TestShiftDutyTarget_LodgerWindsDownToInn — the housing-aware target: a lodger
// (no home, active ledger room grant) winds down to the inn it rents, not nil.
func TestShiftDutyTarget_LodgerWindsDownToInn(t *testing.T) {
	now := time.Date(2026, 6, 9, 22, 0, 0, 0, time.UTC)
	exp := now.Add(24 * time.Hour)

	lodger := shiftNPC("ezekiel", KindNPCStateful, "smithy", "", "smithy") // no home
	lodger.ScheduleStartMin = intptr(420)
	lodger.ScheduleEndMin = intptr(960)
	lodger.RoomAccess = map[RoomAccessKey]*RoomAccess{
		{RoomID: 2, Source: AccessSourceLedger}: {
			RoomID: 2, Source: AccessSourceLedger, Active: true, ExpiresAt: &exp, LedgerID: 1,
		},
	}
	w := sleepTestWorld(lodger)
	w.Structures = map[StructureID]*Structure{
		"inn": {ID: "inn", DisplayName: "Hannah's Inn", Rooms: []*Room{
			{ID: 2, StructureID: "inn", Kind: RoomKindPrivate, Name: "bedroom_1"},
		}},
	}

	// Off-shift, away from the inn → wind-down target is the rented inn.
	target, toWork, ok := shiftDutyTarget(w, lodger, 1320, now)
	if !ok || target != "inn" || toWork {
		t.Errorf("lodger wind-down should target the inn; got (%q,%v,%v)", target, toWork, ok)
	}

	// Already at the inn → no duty.
	lodger.InsideStructureID = "inn"
	if _, _, ok := shiftDutyTarget(w, lodger, 1320, now); ok {
		t.Error("lodger already at the inn should have no wind-down duty")
	}

	// Homeless (no home, no active grant) → no engine target (cue-only).
	homeless := shiftNPC("vagrant", KindNPCStateful, "smithy", "", "smithy")
	homeless.ScheduleStartMin = intptr(420)
	homeless.ScheduleEndMin = intptr(960)
	w.Actors["vagrant"] = homeless
	if _, _, ok := shiftDutyTarget(w, homeless, 1320, now); ok {
		t.Error("homeless NPC should have no engine wind-down target")
	}
}
