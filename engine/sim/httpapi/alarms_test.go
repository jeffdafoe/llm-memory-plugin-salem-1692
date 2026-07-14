package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// alarms_test.go — the alarm evaluator (threshold classification) and the
// response-injection middleware (body splice + header + healthy no-op).

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
	h.RecordSuccess(time.Now().Add(-time.Hour))
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

	h.RecordSuccess(time.Now())

	if got := s.evaluateAlarms(time.Now()); len(got) != 0 {
		t.Fatalf("expected the alarm to self-clear after a successful checkpoint, got %v", got)
	}
}
