package sim

import (
	"strings"
	"testing"
	"time"
)

// stay_open_test.go — ZBBS-WORK-387. Covers resolveOpenUntil (next-occurrence
// hour math), the StayOpen command (stamp + already-committed reject), and the
// shiftDutyTarget OpenUntil suppression (which yields to peak exhaustion) plus
// the housing-aware lodger wind-down target. Same hand-built-world style as
// shift_duty_test.go / lodger_rebook_test.go.

func TestResolveOpenUntil(t *testing.T) {
	base := time.Date(2026, 6, 9, 21, 30, 0, 0, time.UTC) // 21:30 local (UTC test loc)
	cases := []struct {
		name      string
		at        time.Time
		untilHour int
		want      time.Time
		wantErr   bool
	}{
		{"later today", base, 23, time.Date(2026, 6, 9, 23, 0, 0, 0, time.UTC), false},
		{"after midnight (within window)", base, 1, time.Date(2026, 6, 10, 1, 0, 0, 0, time.UTC), false},
		{"midnight is valid", base, 0, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), false},
		// LLM-39: naming an hour that has just passed rolls a full ~24h forward.
		// That degenerate window is now rejected, not honored as an all-nighter.
		{"just-past hour (~24h roll) rejected", base, 21, time.Time{}, true},
		// Window boundary at maxStayOpenWindow (8h): 05:00 is +7.5h (ok), 06:00 is +8.5h (reject).
		{"window edge +7.5h ok", base, 5, time.Date(2026, 6, 10, 5, 0, 0, 0, time.UTC), false},
		{"beyond window +8.5h rejected", base, 6, time.Time{}, true},
		// Exact maxStayOpenWindow boundary: from 21:00, hour 5 is precisely +8h —
		// allowed (the bound is `> 8h`). Guards against a future flip to `>=`.
		{"window exact +8h ok", time.Date(2026, 6, 9, 21, 0, 0, 0, time.UTC), 5, time.Date(2026, 6, 10, 5, 0, 0, 0, time.UTC), false},
		// The exact LLM-39 specimen: until_hour=20 named at 20:01 → rejected.
		{"LLM-39 current hour at HH:01 rejected", time.Date(2026, 6, 18, 20, 1, 0, 0, time.UTC), 20, time.Time{}, true},
		// A genuine cross-midnight commit ("until 1am" at 9pm = +4h) still resolves.
		{"legit cross-midnight from 21:00", time.Date(2026, 6, 18, 21, 0, 0, 0, time.UTC), 1, time.Date(2026, 6, 19, 1, 0, 0, 0, time.UTC), false},
		{"out of range high", base, 24, time.Time{}, true},
		{"out of range low", base, -1, time.Time{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveOpenUntil(time.UTC, c.untilHour, c.at)
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

// TestStayOpen_AwayFromPostRejected — LLM-66. stay_open is incoherent away from
// the keeper's own work post (presence is the open/closed signal), so a call
// that arrives while the keeper is elsewhere is rejected at dispatch — the
// backstop behind the advertising gate (handlers/tool_gating.go), which keeps
// the tool off the off-post menu in the first place. Specimen: a blacksmith
// calling stay_open while resting under a Shade Tree (InsideStructureID != WorkStructureID).
func TestStayOpen_AwayFromPostRejected(t *testing.T) {
	at := time.Date(2026, 6, 9, 21, 30, 0, 0, time.UTC)
	a := shiftNPC("ezekiel", KindNPCStateful, "smithy", "home", "shade_tree") // away from the smithy
	w := sleepTestWorld(a)

	_, err := StayOpen("ezekiel", "weariness", 23, at).Fn(w)
	if err == nil {
		t.Fatal("stay_open while away from the work post should be rejected")
	}
	if a.OpenUntil != nil {
		t.Errorf("OpenUntil should be unset on an away-from-post reject, got %v", a.OpenUntil)
	}
}

// TestStayOpen_NoOpReject — LLM-40. A stay_open that does not reach past the
// keeper's regular close is the diligence-reflex misfire (commit to "open until
// 9" when you already close at 9). It is rejected while still before close, but
// a genuine extension is honored, an after-close commitment is honored (close
// already passed), and a keeper with no explicit schedule is unaffected.
func TestStayOpen_NoOpReject(t *testing.T) {
	const closeMin = 1260 // 21:00 — "9 in the evening"

	newKeeper := func(schedEnd *int) (*Actor, *World) {
		a := shiftNPC("ezekiel", KindNPCStateful, "smithy", "home", "smithy")
		a.ScheduleStartMin = intptr(540) // 09:00
		a.ScheduleEndMin = schedEnd
		return a, sleepTestWorld(a)
	}

	t.Run("until_hour == close while on shift is rejected", func(t *testing.T) {
		a, w := newKeeper(intptr(closeMin))
		at := time.Date(2026, 6, 18, 20, 1, 0, 0, time.UTC) // an hour before close
		_, err := StayOpen("ezekiel", "ensure customers can still buy", 21, at).Fn(w)
		if err == nil {
			t.Fatal("staying open until the regular close hour should be rejected as a no-op")
		}
		if !strings.Contains(err.Error(), "9 in the evening") {
			t.Errorf("reject should voice the close time, got: %v", err)
		}
		if a.OpenUntil != nil {
			t.Errorf("OpenUntil should be unset on a no-op reject, got %v", a.OpenUntil)
		}
	})

	t.Run("genuine extension past close is honored", func(t *testing.T) {
		a, w := newKeeper(intptr(closeMin))
		at := time.Date(2026, 6, 18, 20, 1, 0, 0, time.UTC)
		if _, err := StayOpen("ezekiel", "an order I still owe", 22, at).Fn(w); err != nil {
			t.Fatalf("staying open an hour past close should succeed: %v", err)
		}
		want := time.Date(2026, 6, 18, 22, 0, 0, 0, time.UTC)
		if a.OpenUntil == nil || !a.OpenUntil.Equal(want) {
			t.Errorf("OpenUntil = %v, want %v", a.OpenUntil, want)
		}
	})

	t.Run("commitment after close has passed is honored", func(t *testing.T) {
		a, w := newKeeper(intptr(closeMin))
		at := time.Date(2026, 6, 18, 22, 0, 0, 0, time.UTC) // already past close
		if _, err := StayOpen("ezekiel", "a customer is still here", 23, at).Fn(w); err != nil {
			t.Fatalf("staying open past an already-passed close should succeed: %v", err)
		}
		want := time.Date(2026, 6, 18, 23, 0, 0, 0, time.UTC)
		if a.OpenUntil == nil || !a.OpenUntil.Equal(want) {
			t.Errorf("OpenUntil = %v, want %v", a.OpenUntil, want)
		}
	})

	t.Run("overnight schedule rejects no-op on the pre-midnight side", func(t *testing.T) {
		// Shift 18:00–03:00 (ScheduleEndMin < ScheduleStartMin). At 22:00 the active
		// close is 03:00 TOMORROW, not today's already-past 03:00 — so committing to
		// the regular 03:00 close is still a no-op (code_review edge case).
		a := shiftNPC("ezekiel", KindNPCStateful, "smithy", "home", "smithy")
		a.ScheduleStartMin = intptr(1080) // 18:00
		a.ScheduleEndMin = intptr(180)    // 03:00 (wraps midnight)
		w := sleepTestWorld(a)
		at := time.Date(2026, 6, 18, 22, 0, 0, 0, time.UTC)

		_, err := StayOpen("ezekiel", "ensure customers can still buy", 3, at).Fn(w)
		if err == nil {
			t.Fatal("until the regular overnight close (03:00) should be rejected as a no-op")
		}
		if !strings.Contains(err.Error(), "3 in the morning") {
			t.Errorf("reject should voice the overnight close, got: %v", err)
		}
		if a.OpenUntil != nil {
			t.Errorf("OpenUntil should be unset on a no-op reject, got %v", a.OpenUntil)
		}

		// One hour past the overnight close is a genuine extension.
		if _, err := StayOpen("ezekiel", "an order I still owe", 4, at).Fn(w); err != nil {
			t.Fatalf("staying open until 04:00 (past the 03:00 close) should succeed: %v", err)
		}
		want := time.Date(2026, 6, 19, 4, 0, 0, 0, time.UTC) // tomorrow 04:00
		if a.OpenUntil == nil || !a.OpenUntil.Equal(want) {
			t.Errorf("OpenUntil = %v, want %v", a.OpenUntil, want)
		}
	})

	t.Run("no explicit schedule skips the no-op check", func(t *testing.T) {
		a, w := newKeeper(nil) // dawn/dusk-window keeper, no precise close to compare
		at := time.Date(2026, 6, 18, 20, 1, 0, 0, time.UTC)
		if _, err := StayOpen("ezekiel", "keeping the lamp lit", 21, at).Fn(w); err != nil {
			t.Fatalf("unscheduled keeper should not hit the no-op reject: %v", err)
		}
		want := time.Date(2026, 6, 18, 21, 0, 0, 0, time.UTC)
		if a.OpenUntil == nil || !a.OpenUntil.Equal(want) {
			t.Errorf("OpenUntil = %v, want %v", a.OpenUntil, want)
		}
	})
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
