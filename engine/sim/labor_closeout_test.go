package sim

import (
	"strings"
	"testing"
	"time"
)

// labor_closeout_test.go — LLM-190. The shop-close bound on a labor job: a job's
// completion deadline is clamped at accept to the employer's closing time, and a
// job that runs up to a keeper's shift end completes with the keeper's spoken
// close-out. White-box: the helpers are unexported and pure against a bare world
// (effectiveShiftWindow reads the employer's own schedule; localMinuteOfDay reads
// the wall clock via the world timezone, UTC when no Location is configured).

func schedActor(id ActorID, startMin, endMin int) *Actor {
	s, e := startMin, endMin
	return &Actor{ID: id, ScheduleStartMin: &s, ScheduleEndMin: &e}
}

func TestClampWorkingUntilToEmployerClose(t *testing.T) {
	w := &World{}
	emp := schedActor("ezekiel", 480, 1080)             // keeps 08:00–18:00
	at := time.Date(2026, 6, 30, 14, 0, 0, 0, time.UTC) // on shift, 4h until close
	wantClose := time.Date(2026, 6, 30, 18, 0, 0, 0, time.UTC)

	// An 8h job is clamped back to the 18:00 close.
	if got := clampWorkingUntilToEmployerClose(w, emp, at.Add(8*time.Hour), at); !got.Equal(wantClose) {
		t.Errorf("8h job clamp = %v, want %v (shop close)", got, wantClose)
	}
	// A 2h job finishes before close — unchanged.
	short := at.Add(2 * time.Hour)
	if got := clampWorkingUntilToEmployerClose(w, emp, short, at); !got.Equal(short) {
		t.Errorf("2h job clamp = %v, want %v (unchanged, ends before close)", got, short)
	}
}

func TestClampWorkingUntilToEmployerClose_OffShiftAndUnscheduled(t *testing.T) {
	w := &World{}
	emp := schedActor("ezekiel", 480, 1080)              // 08:00–18:00
	off := time.Date(2026, 6, 30, 20, 0, 0, 0, time.UTC) // 20:00, off shift
	wu := off.Add(3 * time.Hour)
	if got := clampWorkingUntilToEmployerClose(w, emp, wu, off); !got.Equal(wu) {
		t.Errorf("off-shift employer: clamp = %v, want unchanged %v", got, wu)
	}
	// Unscheduled employer with no dawn/dusk configured → no window to clamp to.
	bare := &Actor{ID: "nobody"}
	at := time.Date(2026, 6, 30, 14, 0, 0, 0, time.UTC)
	wu2 := at.Add(8 * time.Hour)
	if got := clampWorkingUntilToEmployerClose(w, bare, wu2, at); !got.Equal(wu2) {
		t.Errorf("unscheduled employer: clamp = %v, want unchanged %v", got, wu2)
	}
}

func TestClampWorkingUntilToEmployerClose_WrapMidnight(t *testing.T) {
	w := &World{}
	emp := schedActor("john", 960, 180)                 // tavernkeeper 16:00–03:00, wraps midnight
	at := time.Date(2026, 6, 30, 22, 0, 0, 0, time.UTC) // on shift; close is 03:00 next day
	wantClose := time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC)
	if got := clampWorkingUntilToEmployerClose(w, emp, at.Add(8*time.Hour), at); !got.Equal(wantClose) {
		t.Errorf("wrap-midnight clamp = %v, want %v (03:00 next day)", got, wantClose)
	}
}

func TestShopClosedForCloseout(t *testing.T) {
	w := &World{}
	keeper := schedActor("ezekiel", 480, 1080)
	keeper.BusinessownerState = &BusinessownerState{Flavor: "reserved"}
	offShift := time.Date(2026, 6, 30, 19, 0, 0, 0, time.UTC) // after the 18:00 close
	onShift := time.Date(2026, 6, 30, 14, 0, 0, 0, time.UTC)

	if !shopClosedForCloseout(w, keeper, offShift) {
		t.Errorf("keeper off shift: want close-out=true")
	}
	if shopClosedForCloseout(w, keeper, onShift) {
		t.Errorf("keeper on shift: want close-out=false (job finished mid-day, stays silent)")
	}
	// A non-keeper employer never triggers a shop close-out, even off shift.
	plain := schedActor("anne", 480, 1080)
	if shopClosedForCloseout(w, plain, offShift) {
		t.Errorf("non-keeper employer: want close-out=false")
	}
}

func TestLaborCloseoutLine(t *testing.T) {
	line := strings.ToLower(laborCloseoutLine(5))
	if !strings.Contains(line, "5 coins") {
		t.Errorf("close-out line %q should name the 5-coin pay", line)
	}
	if !strings.Contains(line, "shop shut") || !strings.Contains(line, "work's done") {
		t.Errorf("close-out line %q should announce closing + work done", line)
	}
}
