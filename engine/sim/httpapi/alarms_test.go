package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// alarms_test.go — the alarm evaluator (threshold classification) and the
// response-injection middleware (body splice + header + healthy no-op).

// --- ticker_stale (LLM-395) ---

// staleEntry builds a registered ticker whose last beat was `silent` ago.
func staleEntry(name string, interval, silent time.Duration, now time.Time) sim.TickerHealthEntry {
	return sim.TickerHealthEntry{
		Name:         name,
		Count:        7,
		LastFire:     now.Add(-silent),
		Registered:   true,
		RegisteredAt: now.Add(-24 * time.Hour),
		Interval:     interval,
	}
}

func TestTickerStaleAlarm_SilentWhenCadencesAreMet(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	entries := []sim.TickerHealthEntry{
		staleEntry("needs", time.Minute, 20*time.Second, now),
		staleEntry("reactor", 250*time.Millisecond, time.Second, now),
		staleEntry("atmosphere", time.Hour, 90*time.Minute, now),
	}
	if a, ok := tickerStaleAlarm(entries, worldServing, now); ok {
		t.Errorf("alarm fired on healthy tickers: %+v", a)
	}
	// An empty registry (a world with no tickers wired) is silent, not a panic.
	if _, ok := tickerStaleAlarm(nil, worldServing, now); ok {
		t.Error("alarm fired on an empty registry")
	}
}

func TestTickerStaleAlarm_AggregatesAndNamesTheStaleTickers(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	entries := []sim.TickerHealthEntry{
		staleEntry("needs", time.Minute, 30*time.Minute, now), // stale
		staleEntry("shift", time.Minute, 10*time.Second, now), // healthy
		staleEntry("sleep", time.Minute, 20*time.Minute, now), // stale
		staleEntry("dwell", time.Minute, 5*time.Second, now),  // healthy
	}

	a, ok := tickerStaleAlarm(entries, worldServing, now)
	if !ok {
		t.Fatal("no alarm for two dead tickers")
	}
	if a.Kind != alarmKindTickerStale {
		t.Errorf("Kind=%q, want %q", a.Kind, alarmKindTickerStale)
	}
	// Assert against the rendered NAME LIST, not the whole sentence: the prose
	// legitimately mentions "needs decay, shift changes" as operator context, so a
	// bare substring check would match the healthy 'shift' ticker in that phrase.
	if !strings.Contains(a.Detail, "(needs, sleep)") {
		t.Errorf("Detail does not list exactly the two stale tickers: %s", a.Detail)
	}
	if !strings.Contains(a.Detail, "2 of the engine's") {
		t.Errorf("Detail does not report the stale count: %s", a.Detail)
	}

	// Since is the EARLIEST crossing — 'needs' went silent first, so its deadline
	// (lastFire + 3x1m) is the moment durability of the cadence broke.
	wantSince := now.Add(-30 * time.Minute).Add(3 * time.Minute)
	if !a.Since.Equal(wantSince) {
		t.Errorf("Since=%v, want the earliest staleSince %v", a.Since, wantSince)
	}
}

// The alarm evaluator holds no state, so re-evaluating the same registry must
// produce the same Since — otherwise the banner would appear to "reset" on every
// request an operator made mid-incident.
func TestTickerStaleAlarm_SinceIsStableAcrossEvaluations(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	entries := []sim.TickerHealthEntry{staleEntry("needs", time.Minute, 30*time.Minute, now)}

	first, ok := tickerStaleAlarm(entries, worldServing, now)
	if !ok {
		t.Fatal("no alarm")
	}
	later, ok := tickerStaleAlarm(entries, worldServing, now.Add(5*time.Minute))
	if !ok {
		t.Fatal("alarm cleared itself while the ticker was still dead")
	}
	if !first.Since.Equal(later.Since) {
		t.Errorf("Since moved between evaluations: %v -> %v", first.Since, later.Since)
	}
}

// Mass staleness has two shapes with OPPOSITE fixes, and the world-command probe
// is what tells them apart (LLM-402). With the probe healthy, "every ticker is
// stale" REMOVES the obvious suspect: the world loop is demonstrably serving, so
// the fault is the process or the ticker goroutines themselves.
func TestTickerStaleAlarm_AllStaleWithAHealthyWorldExoneratesTheWorldLoop(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var entries []sim.TickerHealthEntry
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		entries = append(entries, staleEntry(n, time.Minute, time.Hour, now))
	}

	a, ok := tickerStaleAlarm(entries, worldServing, now)
	if !ok {
		t.Fatal("no alarm with every ticker dead")
	}
	if !strings.Contains(a.Detail, "EVERY world-dependent ticker is stale") {
		t.Errorf("Detail does not call out the all-stale case: %s", a.Detail)
	}
	if !strings.Contains(a.Detail, "is NOT a wedged world COMMAND LOOP") {
		t.Errorf("Detail does not exonerate the world loop when the probe is landing: %s", a.Detail)
	}
	// Capped at tickerStaleNamesInDetail (8) of the 10, with the remainder summarised
	// — the wedge case must not paste every ticker in the engine into every response.
	if !strings.Contains(a.Detail, "(a, b, c, d, e, f, g, h, and 2 more)") {
		t.Errorf("Detail does not cap the name list at %d + a remainder: %s", tickerStaleNamesInDetail, a.Detail)
	}
}

// The other shape: the probe is timing out, so the cause is MEASURED. The alarm
// must name it and stand down rather than repeat the diagnosis.
func TestTickerStaleAlarm_DefersToTheWorldCommandAlarmWhenItIsFiring(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	entries := []sim.TickerHealthEntry{
		staleEntry("needs", time.Minute, time.Hour, now),
		staleEntry("sleep", time.Minute, time.Hour, now),
	}

	a, ok := tickerStaleAlarm(entries, worldStalled, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if !strings.Contains(a.Detail, "CONFIRMED STALLED") {
		t.Errorf("Detail does not defer to the measured cause: %s", a.Detail)
	}
	if strings.Contains(a.Detail, "is NOT a wedged world COMMAND LOOP") {
		t.Errorf("Detail exonerates the world loop while the probe is timing out: %s", a.Detail)
	}
}

// THE REGRESSION THIS FILE EXISTS TO PREVENT (LLM-402). The prober beats BEFORE its
// send, precisely so a wedged world cannot silence it — which means that in the
// exact incident the all-stale branch describes, the prober is the one ticker still
// beating. A headcount of len(stale) == len(entries) would therefore be false
// forever, and the branch would be dead code in the case it was written for. The
// probe must be excluded from the world-dependent population.
func TestTickerStaleAlarm_ProbeIsExcludedFromTheAllStaleHeadcount(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	entries := []sim.TickerHealthEntry{
		staleEntry("needs", time.Minute, time.Hour, now),
		staleEntry("sleep", time.Minute, time.Hour, now),
		// The prober: alive and beating, as it is BY DESIGN during a world wedge.
		staleEntry(sim.WorldCommandProbeTickerName, sim.WorldCommandProbeInterval, time.Second, now),
	}

	a, ok := tickerStaleAlarm(entries, worldServing, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if !strings.Contains(a.Detail, "EVERY world-dependent ticker is stale") {
		t.Errorf("a live prober suppressed the all-stale branch — the headcount still counts it: %s", a.Detail)
	}
	// And it is not named as a casualty, because it is not one.
	if strings.Contains(a.Detail, sim.WorldCommandProbeTickerName) {
		t.Errorf("the healthy prober was listed among the stale tickers: %s", a.Detail)
	}
}

// The fail-safe, at the alarm boundary: a ticker nobody declared a cadence for is
// never judged, however long it has been silent.
func TestTickerStaleAlarm_UnregisteredAndZeroIntervalNeverFire(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	entries := []sim.TickerHealthEntry{
		{Name: "unregistered", Count: 3, LastFire: now.Add(-30 * 24 * time.Hour)},
		{Name: "zero_interval", Registered: true, Interval: 0, LastFire: now.Add(-30 * 24 * time.Hour)},
	}
	if a, ok := tickerStaleAlarm(entries, worldServing, now); ok {
		t.Errorf("alarm fired on opted-out tickers — the fail-safe is broken: %+v", a)
	}
}

// The never-started goroutine: registered ahead of its `go`, never beat once.
func TestTickerStaleAlarm_FiresOnATickerThatNeverStarted(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	entries := []sim.TickerHealthEntry{{
		Name:         "needs",
		Registered:   true,
		Interval:     time.Minute,
		RegisteredAt: now.Add(-time.Hour),
		// No beat, ever.
	}}
	a, ok := tickerStaleAlarm(entries, worldServing, now)
	if !ok {
		t.Fatal("no alarm for a ticker that registered and never fired — this is the goroutine-never-started case")
	}
	if !a.Since.Equal(now.Add(-time.Hour).Add(3 * time.Minute)) {
		t.Errorf("Since=%v, want registeredAt+3m (the baseline for a never-fired ticker)", a.Since)
	}
}

// A worldless Server must not panic the entire umbilical surface: evaluateAlarms
// runs on EVERY response.
func TestEvaluateAlarms_NilWorldIsSilent(t *testing.T) {
	s := &Server{}
	if got := s.evaluateAlarms(time.Now().UTC()); len(got) != 0 {
		t.Errorf("evaluateAlarms on a worldless server = %+v, want none", got)
	}
}

func TestCheckpointAlarm_ThresholdBoundary(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		streak     int
		wantFiring bool
	}{
		{"healthy: no failures", 0, false},
		{"single transient failure does not cry wolf", 1, false},
		{"one below threshold stays quiet", checkpointFailureStreakThreshold - 1, false},
		{"at threshold fires", checkpointFailureStreakThreshold, true},
		{"a 17.5h outage most certainly fires", 1050, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := sim.CheckpointHealthSnapshot{
				ConsecutiveFailures: tc.streak,
				LastSuccessAt:       now.Add(-2 * time.Hour),
				LastError:           "pg SaveWorld: duplicate key",
			}
			got, firing := checkpointAlarm(h, now)
			if firing != tc.wantFiring {
				t.Fatalf("firing = %v, want %v (streak %d)", firing, tc.wantFiring, tc.streak)
			}
			if !firing {
				return
			}
			if got.Kind != alarmKindCheckpointFailure {
				t.Errorf("Kind = %q, want %q", got.Kind, alarmKindCheckpointFailure)
			}
			if got.Consecutive != tc.streak {
				t.Errorf("Consecutive = %d, want %d", got.Consecutive, tc.streak)
			}
			if got.LastError != "pg SaveWorld: duplicate key" {
				t.Errorf("LastError = %q, want the underlying pg error", got.LastError)
			}
			if got.Detail == "" {
				t.Error("Detail is empty; the alarm must carry a plain-English sentence")
			}
		})
	}
}

// Since bounds how much world state a restart would discard, so it must be the
// last time durability was known GOOD — not the last failure.
func TestCheckpointAlarm_SinceIsLastSuccess(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	lastGood := now.Add(-17*time.Hour - 30*time.Minute)
	got, firing := checkpointAlarm(sim.CheckpointHealthSnapshot{
		ConsecutiveFailures: 1050,
		LastSuccessAt:       lastGood,
		LastFailureAt:       now.Add(-time.Minute),
	}, now)
	if !firing {
		t.Fatal("expected the alarm to fire")
	}
	if !got.Since.Equal(lastGood) {
		t.Errorf("Since = %v, want last_success_at %v", got.Since, lastGood)
	}
}

// A fresh boot against a broken DB has never checkpointed successfully, so
// last_success_at is zero and "since" must fall back to the first failure rather
// than reporting the zero time (which would render as year 1).
func TestCheckpointAlarm_SinceFallsBackToFailureWhenNeverSucceeded(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	firstFail := now.Add(-5 * time.Minute)
	got, firing := checkpointAlarm(sim.CheckpointHealthSnapshot{
		ConsecutiveFailures: 5,
		LastFailureAt:       firstFail,
	}, now)
	if !firing {
		t.Fatal("expected the alarm to fire")
	}
	if !got.Since.Equal(firstFail) {
		t.Errorf("Since = %v, want the failure time %v when there is no success on record", got.Since, firstFail)
	}
}

// A nil recorder (engine wired without checkpoint health) must never fire and
// never panic.
func TestEvaluateAlarms_NilRecorderIsSilent(t *testing.T) {
	s := &Server{}
	if got := s.evaluateAlarms(time.Now()); len(got) != 0 {
		t.Fatalf("evaluateAlarms with no recorder = %v, want none", got)
	}
}

func TestInjectAlarms(t *testing.T) {
	encoded := []byte(`[{"kind":"checkpoint_failure"}]`)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "object with members gets the key spliced in first, with a comma",
			body: `{"contract_version":1,"total":2}`,
			want: `{"ALARMS":[{"kind":"checkpoint_failure"}],"contract_version":1,"total":2}`,
		},
		{
			name: "empty object takes the key with no trailing comma",
			body: `{}`,
			want: `{"ALARMS":[{"kind":"checkpoint_failure"}]}`,
		},
		{
			name: "leading whitespace is tolerated",
			body: "\n  {\"a\":1}",
			want: "\n  {\"ALARMS\":[{\"kind\":\"checkpoint_failure\"}],\"a\":1}",
		},
		{
			// /errors, /client-errors and /deadlocks dump a raw slice. You cannot add
			// a top-level key to an array — these ride the header instead.
			name: "array body is returned untouched",
			body: `[{"status":500}]`,
			want: `[{"status":500}]`,
		},
		{
			name: "empty body is returned untouched",
			body: ``,
			want: ``,
		},
		{
			name: "non-JSON body is returned untouched",
			body: `not json`,
			want: `not json`,
		},
		{
			// Opens with '{' but is not a valid object. Splicing would manufacture a
			// DIFFERENTLY malformed payload and stamp a fresh Content-Length on it.
			name: "truncated object is returned untouched",
			body: `{`,
			want: `{`,
		},
		{
			name: "brace-prefixed non-JSON is returned untouched",
			body: `{not json}`,
			want: `{not json}`,
		},
		{
			name: "object with trailing garbage is returned untouched",
			body: `{"x":1} trailing`,
			want: `{"x":1} trailing`,
		},
		{
			// A brace inside a string must not confuse the member-detection scan.
			name: "braces inside string values are handled",
			body: `{"msg":"{not a brace}"}`,
			want: `{"ALARMS":[{"kind":"checkpoint_failure"}],"msg":"{not a brace}"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(injectAlarms([]byte(tc.body), encoded))
			if got != tc.want {
				t.Errorf("injectAlarms()\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// The spliced body must still parse, and must carry BOTH the alarm and every
// original field — the payload underneath an alarm is exactly what the operator
// came for.
func TestInjectAlarms_ResultStaysValidJSON(t *testing.T) {
	encoded, err := json.Marshal([]Alarm{{Kind: alarmKindCheckpointFailure, Consecutive: 1050}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := injectAlarms([]byte(`{"contract_version":1,"actors":[{"id":"a1"}]}`), encoded)

	var got struct {
		Alarms          []Alarm `json:"ALARMS"`
		ContractVersion int     `json:"contract_version"`
		Actors          []struct {
			ID string `json:"id"`
		} `json:"actors"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("spliced body is not valid JSON: %v\nbody: %s", err, out)
	}
	if len(got.Alarms) != 1 || got.Alarms[0].Consecutive != 1050 {
		t.Errorf("ALARMS did not survive the splice: %+v", got.Alarms)
	}
	if got.ContractVersion != 1 || len(got.Actors) != 1 || got.Actors[0].ID != "a1" {
		t.Errorf("original payload was mangled: %+v", got)
	}
}

// serverWithStreak returns a Server whose checkpoint recorder has failed n times
// in a row.
func serverWithStreak(n int) *Server {
	h := &sim.CheckpointHealth{}
	h.RecordSuccess(time.Now().Add(-time.Hour), nil)
	for i := 0; i < n; i++ {
		h.RecordFailure(time.Now(), errAlarmTest)
	}
	return &Server{checkpointHealth: h}
}

var errAlarmTest = &alarmTestErr{}

type alarmTestErr struct{}

func (e *alarmTestErr) Error() string { return "pg SaveWorld: duplicate key" }

// Healthy = strict no-op. The response must come through byte-for-byte, with no
// alarm key and no header, so every existing consumer keeps working.
func TestWithAlarmBanner_HealthyIsStrictNoOp(t *testing.T) {
	s := serverWithStreak(0)
	h := s.withAlarmBanner(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"contract_version":1}`))
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/village/umbilical/state", nil))

	if got := rec.Body.String(); got != `{"contract_version":1}` {
		t.Errorf("healthy body = %s, want it untouched", got)
	}
	if got := rec.Header().Get(alarmHeader); got != "" {
		t.Errorf("healthy response carries %s = %q, want no header", alarmHeader, got)
	}
}

// Firing: an object response carries the alarm in the body AND the header.
func TestWithAlarmBanner_FiringSplicesObjectBodyAndSetsHeader(t *testing.T) {
	s := serverWithStreak(checkpointFailureStreakThreshold)
	h := s.withAlarmBanner(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"contract_version":1,"total":0}`))
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/village/umbilical/state", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(alarmHeader); got != alarmKindCheckpointFailure {
		t.Errorf("%s = %q, want %q", alarmHeader, got, alarmKindCheckpointFailure)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("handler's Content-Type was lost: %q", got)
	}

	var got struct {
		Alarms          []Alarm `json:"ALARMS"`
		ContractVersion int     `json:"contract_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if len(got.Alarms) != 1 || got.Alarms[0].Kind != alarmKindCheckpointFailure {
		t.Fatalf("ALARMS = %+v, want a checkpoint_failure", got.Alarms)
	}
	if got.Alarms[0].Consecutive != checkpointFailureStreakThreshold {
		t.Errorf("Consecutive = %d, want %d", got.Alarms[0].Consecutive, checkpointFailureStreakThreshold)
	}
	if got.ContractVersion != 1 {
		t.Errorf("original payload lost: contract_version = %d", got.ContractVersion)
	}

	// Content-Length must match the REWRITTEN body, or the response truncates.
	wantLen := strconv.Itoa(rec.Body.Len())
	if got := rec.Header().Get("Content-Length"); got != wantLen {
		t.Errorf("Content-Length = %q, want %q (the spliced length)", got, wantLen)
	}
}

// Firing on an array-bodied route (/errors, /client-errors, /deadlocks): the body
// is untouchable, so the header is the whole signal.
func TestWithAlarmBanner_FiringLeavesArrayBodyAndStillSetsHeader(t *testing.T) {
	s := serverWithStreak(10)
	h := s.withAlarmBanner(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"status":500}]`))
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/village/umbilical/errors", nil))

	if got := rec.Body.String(); got != `[{"status":500}]` {
		t.Errorf("array body = %s, want it untouched", got)
	}
	if got := rec.Header().Get(alarmHeader); got != alarmKindCheckpointFailure {
		t.Errorf("%s = %q, want the alarm to still ride the header", alarmHeader, got)
	}
}

// A handler's non-2xx status must survive the wrapper — an operator hitting a 400
// mid-incident should still see the alarm, and still get their 400.
func TestWithAlarmBanner_PreservesHandlerStatus(t *testing.T) {
	s := serverWithStreak(10)
	h := s.withAlarmBanner(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"missing id"}`))
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/village/umbilical/agent", nil))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if got := rec.Header().Get(alarmHeader); got != alarmKindCheckpointFailure {
		t.Errorf("%s = %q, want the alarm on the error response too", alarmHeader, got)
	}
	var got struct {
		Alarms []Alarm `json:"ALARMS"`
		Error  string  `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if got.Error != "missing id" || len(got.Alarms) != 1 {
		t.Errorf("want both the handler's error and the alarm, got %+v", got)
	}
}

// A no-body status must not gain one. 204/304 (and 1xx) carry no payload, so the
// alarm rides the header alone — writing a body here is a protocol violation.
func TestWithAlarmBanner_NoBodyStatusesGetHeaderOnly(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s := serverWithStreak(10)
			h := s.withAlarmBanner(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			})

			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodGet, "/api/village/umbilical/state", nil))

			if rec.Code != status {
				t.Errorf("status = %d, want %d", rec.Code, status)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty for a %d", rec.Body.String(), status)
			}
			if got := rec.Header().Get("Content-Length"); got != "" {
				t.Errorf("Content-Length = %q, want unset for a %d", got, status)
			}
			if got := rec.Header().Get(alarmHeader); got != alarmKindCheckpointFailure {
				t.Errorf("%s = %q, want the alarm to still ride the header", alarmHeader, got)
			}
		})
	}
}

// Go's ServeMux routes HEAD to a "GET <path>" pattern, so every umbilical GET
// handler is reachable by HEAD. A HEAD response carries no body.
func TestWithAlarmBanner_HeadRequestGetsNoBody(t *testing.T) {
	s := serverWithStreak(10)
	h := s.withAlarmBanner(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"contract_version":1}`))
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodHead, "/api/village/umbilical/state", nil))

	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty for a HEAD", rec.Body.String())
	}
	if got := rec.Header().Get(alarmHeader); got != alarmKindCheckpointFailure {
		t.Errorf("%s = %q, want the alarm on the header", alarmHeader, got)
	}
}

// A content-encoded body is opaque bytes: splicing JSON into it would corrupt it.
// It must pass through byte-for-byte, with the alarm on the header only.
func TestWithAlarmBanner_EncodedBodyIsNotSpliced(t *testing.T) {
	s := serverWithStreak(10)
	// Deliberately a body that WOULD be spliced if it were treated as plain JSON,
	// so the test fails loudly if the encoding guard regresses.
	payload := `{"contract_version":1}`
	h := s.withAlarmBanner(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte(payload))
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/village/umbilical/turns", nil))

	if got := rec.Body.String(); got != payload {
		t.Errorf("encoded body = %s, want it untouched", got)
	}
	if got := rec.Header().Get(alarmHeader); got != alarmKindCheckpointFailure {
		t.Errorf("%s = %q, want the alarm on the header", alarmHeader, got)
	}
}

// A recovered checkpoint clears the streak, so the alarm self-clears with no ack.
func TestWithAlarmBanner_SelfClearsOnRecovery(t *testing.T) {
	h := &sim.CheckpointHealth{}
	for i := 0; i < 10; i++ {
		h.RecordFailure(time.Now(), errAlarmTest)
	}
	s := &Server{checkpointHealth: h}
	if got := s.evaluateAlarms(time.Now()); len(got) != 1 {
		t.Fatalf("expected the alarm to be firing, got %v", got)
	}

	h.RecordSuccess(time.Now(), nil)

	if got := s.evaluateAlarms(time.Now()); len(got) != 0 {
		t.Fatalf("expected the alarm to self-clear after a successful checkpoint, got %v", got)
	}
}

// TestCheckpointClampAlarm_FiresOnASingleCorrection — unlike checkpoint_failure,
// which waits for a streak because a lone failure can be a transient pg hiccup, a
// single clamp fires immediately. There is no benign version of it: the world
// computed a value no rule of the world permits, and it did so before any of this
// ran.
func TestCheckpointClampAlarm_FiresOnASingleCorrection(t *testing.T) {
	written := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	h := sim.CheckpointHealthSnapshot{
		LastSuccessAt:  written,
		LastClampCount: 1,
		LastClamps:     []sim.Clamp{{Table: "actor_need", Field: "value", Key: "hannah/hunger", From: "25", To: "24"}},
	}

	got, firing := checkpointClampAlarm(h)
	if !firing {
		t.Fatal("a single clamp must fire the alarm — a clamped checkpoint that says nothing is a persistence layer quietly editing the world")
	}
	if got.Kind != alarmKindCheckpointClamped {
		t.Errorf("Kind = %q, want %q", got.Kind, alarmKindCheckpointClamped)
	}
	if !got.Since.Equal(written) {
		t.Errorf("Since = %v, want the moment the clamped checkpoint was written (%v)", got.Since, written)
	}
	// The prose has to carry the offending value, or the operator has to go
	// digging to learn anything actionable.
	for _, want := range []string{"actor_need", "hannah/hunger", "25", "24"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("Detail is missing %q: %s", want, got.Detail)
		}
	}
}

// TestCheckpointClampAlarm_QuietWhenClean — the common case. Every healthy
// checkpoint must leave this silent, or the alarm banner becomes noise and the
// operator learns to skim past the REAL one.
func TestCheckpointClampAlarm_QuietWhenClean(t *testing.T) {
	if _, firing := checkpointClampAlarm(sim.CheckpointHealthSnapshot{LastSuccessAt: time.Now()}); firing {
		t.Error("a clean checkpoint must not fire the clamp alarm")
	}
}

// TestCheckpointClampAlarm_SelfClearsOnACleanCheckpoint — the alarm reads
// LAST-checkpoint state, so fixing the world bug silences it on the next cadence
// with no ack and no restart. That is what keeps the evaluator stateless.
func TestCheckpointClampAlarm_SelfClearsOnACleanCheckpoint(t *testing.T) {
	h := &sim.CheckpointHealth{}
	dirty := &sim.CheckpointSnapshot{Actors: map[sim.ActorID]*sim.Actor{
		"a1": {ID: "a1", DisplayName: "A", State: sim.StateIdle, Needs: map[sim.NeedKey]int{"hunger": 25}},
	}}
	h.RecordSuccess(time.Now(), dirty.ClampToPersistable())

	s := &Server{checkpointHealth: h}
	if got := s.evaluateAlarms(time.Now()); len(got) != 1 || got[0].Kind != alarmKindCheckpointClamped {
		t.Fatalf("expected the clamp alarm to be firing, got %v", got)
	}

	// The world bug is fixed; the next checkpoint corrects nothing.
	clean := &sim.CheckpointSnapshot{Actors: map[sim.ActorID]*sim.Actor{
		"a1": {ID: "a1", DisplayName: "A", State: sim.StateIdle, Needs: map[sim.NeedKey]int{"hunger": 12}},
	}}
	h.RecordSuccess(time.Now(), clean.ClampToPersistable())

	if got := s.evaluateAlarms(time.Now()); len(got) != 0 {
		t.Fatalf("expected the clamp alarm to self-clear after a clean checkpoint, got %v", got)
	}
}

// TestCheckpointClampAlarm_CoexistsWithTheFailureAlarm — the two are independent
// conditions and both can be true at once (a checkpoint clamped, then a later one
// started failing). Neither must mask the other on the banner.
func TestCheckpointClampAlarm_CoexistsWithTheFailureAlarm(t *testing.T) {
	h := &sim.CheckpointHealth{}
	dirty := &sim.CheckpointSnapshot{Actors: map[sim.ActorID]*sim.Actor{
		"a1": {ID: "a1", DisplayName: "A", State: sim.StateIdle, Needs: map[sim.NeedKey]int{"hunger": 25}},
	}}
	h.RecordSuccess(time.Now(), dirty.ClampToPersistable())
	for i := 0; i < checkpointFailureStreakThreshold; i++ {
		h.RecordFailure(time.Now(), errAlarmTest)
	}

	s := &Server{checkpointHealth: h}
	kinds := map[string]bool{}
	for _, a := range s.evaluateAlarms(time.Now()) {
		kinds[a.Kind] = true
	}
	if !kinds[alarmKindCheckpointFailure] || !kinds[alarmKindCheckpointClamped] {
		t.Errorf("both alarms must fire together, got %v", kinds)
	}
}

// --- world_command_stalled (LLM-402) ---

// wcHealth builds a WorldCommandHealthSnapshot with a timeout streak of n whose
// first miss was `ago` before now.
func wcHealth(n int, phase sim.WorldCommandPhase, ago time.Duration, now time.Time) sim.WorldCommandHealthSnapshot {
	h := sim.WorldCommandHealthSnapshot{
		ConsecutiveTimeouts: n,
		LastTimeoutPhase:    phase,
		ProbeTimeoutSeconds: sim.WorldCommandProbeTimeout.Seconds(),
	}
	if n > 0 {
		h.TimeoutStreakStartedAt = now.Add(-ago)
		h.LastTimeoutAt = now
	}
	return h
}

func TestWorldCommandStalledAlarm_ThresholdBoundary(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	// A single missed deadline is a GC pause or a stolen CPU slice, not an
	// emergency. The whole worth of this surface is that it does not cry wolf.
	if a, ok := worldCommandStalledAlarm(wcHealth(1, sim.WorldCommandPhaseReply, 15*time.Second, now), false, now); ok {
		t.Errorf("alarm fired below the streak threshold: %+v", a)
	}
	// A healthy world — the zero snapshot an engine with no prober wired also
	// produces — is silent.
	if a, ok := worldCommandStalledAlarm(sim.WorldCommandHealthSnapshot{}, false, now); ok {
		t.Errorf("alarm fired on a healthy world: %+v", a)
	}
	a, ok := worldCommandStalledAlarm(wcHealth(worldCommandTimeoutStreakThreshold, sim.WorldCommandPhaseReply, 30*time.Second, now), false, now)
	if !ok {
		t.Fatalf("no alarm at the streak threshold (%d)", worldCommandTimeoutStreakThreshold)
	}
	if a.Kind != alarmKindWorldCommandStalled {
		t.Errorf("Kind=%q, want %q", a.Kind, alarmKindWorldCommandStalled)
	}
	if a.Consecutive != worldCommandTimeoutStreakThreshold {
		t.Errorf("Consecutive=%d, want %d", a.Consecutive, worldCommandTimeoutStreakThreshold)
	}
}

// Since is the start of the CURRENT timeout streak — the moment the world stopped
// serving. The evaluator is stateless and re-derives the alarm on every umbilical
// response, so a Since that moved between evaluations would make the banner appear
// to reset under an operator mid-incident.
func TestWorldCommandStalledAlarm_SinceIsTheStreakStartAndIsStable(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	h := wcHealth(3, sim.WorldCommandPhaseReply, 45*time.Second, now)

	first, ok := worldCommandStalledAlarm(h, false, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if !first.Since.Equal(now.Add(-45 * time.Second)) {
		t.Errorf("Since=%v, want the streak start %v", first.Since, now.Add(-45*time.Second))
	}
	later, ok := worldCommandStalledAlarm(h, false, now.Add(10*time.Minute))
	if !ok {
		t.Fatal("alarm cleared itself while the world was still stalled")
	}
	if !first.Since.Equal(later.Since) {
		t.Errorf("Since moved between evaluations: %v -> %v", first.Since, later.Since)
	}
}

// The two halves of the round-trip are different diseases with different first
// moves, so the prose must not collapse them into an undifferentiated timeout.
func TestWorldCommandStalledAlarm_NamesWhichHalfOfTheRoundTripExpired(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	reply, ok := worldCommandStalledAlarm(wcHealth(2, sim.WorldCommandPhaseReply, time.Minute, now), false, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if !strings.Contains(reply.Detail, "ACCEPTED the command and never completed it") {
		t.Errorf("reply-phase detail does not describe a wedged loop: %s", reply.Detail)
	}

	enq, ok := worldCommandStalledAlarm(wcHealth(2, sim.WorldCommandPhaseEnqueue, time.Minute, now), false, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if !strings.Contains(enq.Detail, "could not even ENQUEUE") {
		t.Errorf("enqueue-phase detail does not describe a saturated queue: %s", enq.Detail)
	}
	// It must NOT claim a wedged goroutine on the saturation path — the evidence
	// does not single that out, and a false certainty at 3am sends the operator
	// chasing the wrong thing.
	if strings.Contains(enq.Detail, "ACCEPTED the command") {
		t.Errorf("enqueue-phase detail overclaims a wedged handler: %s", enq.Detail)
	}
}

// A dead prober leaves its last streak behind, and this alarm reads recorded state
// — so it keeps firing off a FOSSIL. That is honest only if it says so; an operator
// must never mistake a frozen reading for a live one.
func TestWorldCommandStalledAlarm_FlagsAFrozenReadingWhenTheProberItselfIsDead(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	h := wcHealth(4, sim.WorldCommandPhaseReply, 20*time.Minute, now)

	live, ok := worldCommandStalledAlarm(h, false, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if strings.Contains(live.Detail, "FROZEN") {
		t.Errorf("a live reading was labelled frozen: %s", live.Detail)
	}

	fossil, ok := worldCommandStalledAlarm(h, true, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if !strings.Contains(fossil.Detail, "FROZEN") {
		t.Errorf("a reading from a dead prober is not flagged as stale: %s", fossil.Detail)
	}
}

// probeTickerStale reads the prober's own entry out of the registry — and must not
// mistake a registry that has no prober at all (a headless world) for a dead one.
func TestProbeTickerStale(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	if probeTickerStale(nil, now) {
		t.Error("an empty registry reported a dead prober")
	}
	alive := []sim.TickerHealthEntry{staleEntry(sim.WorldCommandProbeTickerName, sim.WorldCommandProbeInterval, time.Second, now)}
	if probeTickerStale(alive, now) {
		t.Error("a beating prober reported as stale")
	}
	dead := []sim.TickerHealthEntry{staleEntry(sim.WorldCommandProbeTickerName, sim.WorldCommandProbeInterval, time.Hour, now)}
	if !probeTickerStale(dead, now) {
		t.Error("a prober silent for an hour reported as alive")
	}
}

// A SATURATED queue is not a wedged loop, and ticker_stale must not say it is. The
// loop may be running perfectly and simply being out-produced — the tickers starve
// either way, but the operator's fix is upstream of the loop, so the prose has to
// keep them apart. (Caught by the code_review VA: a bool verdict collapsed these.)
func TestTickerStaleAlarm_SaturationIsNotReportedAsAWedgedLoop(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	entries := []sim.TickerHealthEntry{
		staleEntry("needs", time.Minute, time.Hour, now),
		staleEntry("sleep", time.Minute, time.Hour, now),
	}

	a, ok := tickerStaleAlarm(entries, worldSaturated, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if !strings.Contains(a.Detail, "QUEUE is CONFIRMED SATURATED") {
		t.Errorf("Detail does not name the saturation: %s", a.Detail)
	}
	if strings.Contains(a.Detail, "loop is CONFIRMED STALLED") {
		t.Errorf("Detail claims a wedged loop on a saturated queue: %s", a.Detail)
	}
}

// A dead prober means NO live measurement, so ticker_stale must neither confirm a
// stall nor exonerate the world off the fossil the recorder is still holding. The
// world_command_stalled alarm labels that reading FROZEN; an alarm next to it must
// not quietly launder it back into a confirmation.
func TestTickerStaleAlarm_ADeadProberConfirmsAndExoneratesNothing(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var entries []sim.TickerHealthEntry
	for _, n := range []string{"needs", "sleep", "dwell"} {
		entries = append(entries, staleEntry(n, time.Minute, time.Hour, now))
	}

	a, ok := tickerStaleAlarm(entries, worldUnknown, now)
	if !ok {
		t.Fatal("no alarm")
	}
	if strings.Contains(a.Detail, "CONFIRMED") {
		t.Errorf("Detail confirms a cause with a dead instrument: %s", a.Detail)
	}
	if strings.Contains(a.Detail, "is NOT a wedged world COMMAND LOOP") {
		t.Errorf("Detail exonerates the world loop off a frozen reading: %s", a.Detail)
	}
	if !strings.Contains(a.Detail, "liveness prober is dead") {
		t.Errorf("Detail does not admit it has no live measurement: %s", a.Detail)
	}
}

// The verdict mapping itself: four states, because a bool forced two different lies.
func TestWorldCommandVerdict(t *testing.T) {
	enqueue := sim.WorldCommandHealthSnapshot{LastTimeoutPhase: sim.WorldCommandPhaseEnqueue}
	reply := sim.WorldCommandHealthSnapshot{LastTimeoutPhase: sim.WorldCommandPhaseReply}

	cases := []struct {
		name       string
		h          sim.WorldCommandHealthSnapshot
		probeStale bool
		isStalled  bool
		want       worldVerdict
	}{
		{"probe landing", sim.WorldCommandHealthSnapshot{}, false, false, worldServing},
		{"loop wedged", reply, false, true, worldStalled},
		{"queue saturated", enqueue, false, true, worldSaturated},
		// A dead prober outranks BOTH readings: a fossil that says "stalled" is worth
		// exactly as much as one that says "serving".
		{"dead prober, fossil says stalled", reply, true, true, worldUnknown},
		{"dead prober, fossil says healthy", sim.WorldCommandHealthSnapshot{}, true, false, worldUnknown},
	}
	for _, tc := range cases {
		if got := worldCommandVerdict(tc.h, tc.probeStale, tc.isStalled); got != tc.want {
			t.Errorf("%s: verdict = %v, want %v", tc.name, got, tc.want)
		}
	}
}
