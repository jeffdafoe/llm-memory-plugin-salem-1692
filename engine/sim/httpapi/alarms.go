package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// alarms.go — critical engine-health alarms, stamped onto EVERY umbilical
// response (LLM-394).
//
// Why this exists: during the 2026-07-12→13 checkpoint outage (LLM-392) the
// village ran 17.5 hours with ZERO durability while the umbilical was in active
// use and reported nothing wrong. The failure WAS visible — on
// /checkpoint-health — but that is a pull-only route nobody polls unless they
// already suspect checkpoint trouble, and no other route echoed it. /errors
// couldn't catch it either: that ring records non-2xx HTTP responses the engine
// RETURNED, and a checkpoint fails on a background goroutine that never produces
// one. So a fatal condition sat behind a door nobody had a reason to open.
//
// The fix is to remove the need to go looking: while a critical condition is
// firing, every umbilical response carries it. An operator (or agent) touching
// the surface for ANY unrelated reason trips over the alarm.
//
// Not a metrics feed — a fire alarm. The registry stays small and severe: a
// condition belongs here only if the right response is to drop what you are
// doing. Today that is exactly one condition (durability is broken); the
// evaluator is shaped so more slot in.
//
// TIED TO THE EXISTING MONITORS, NOT A NEW ONE. evaluateAlarms owns no state and
// tracks nothing: it reads the health structs the engine already maintains
// (today sim.CheckpointHealth, behind /checkpoint-health) and classifies
// severity. Live-only and in-memory by design — an alarm reflects what is broken
// RIGHT NOW and self-clears when the condition does, so there is no durable row,
// no operator ack, and no restart survival. That is deliberate (Jeff, 2026-07-14):
// a condition that is still live re-fires within a checkpoint cadence of the next
// umbilical call, so a reboot cannot hide an ONGOING outage — it can only forget
// one that already healed, which is not worth a table (see shared GUIDELINES:
// "Postgres is for durable storage, not infrastructure substitute").

// alarmKindCheckpointFailure fires when durable persistence is broken: the
// checkpointer has failed this many times in a row, so the world in memory is
// diverging from the last good checkpoint and a restart would roll back to it.
const alarmKindCheckpointFailure = "checkpoint_failure"

// checkpointFailureStreakThreshold is how many CONSECUTIVE failed checkpoints
// raise the alarm. Deliberately small: the checkpoint cadence is ~60s, so 3 is
// roughly three minutes of broken durability — long enough that a single
// transient blip (a lock timeout, a momentary pg hiccup) self-heals without
// crying wolf, short enough that a real outage is screaming within minutes
// rather than the 17.5 hours it took last time.
const checkpointFailureStreakThreshold = 3

// alarmHeader carries the firing alarm kinds on EVERY umbilical response,
// including the ones whose body cannot take a top-level key (the raw-array dumps
// and the /turns proxy relay — see injectAlarms). It is the uniform backstop; the
// body block is the one an operator piping through jq actually trips over.
const alarmHeader = "X-Umbilical-Alarms"

// alarmBodyKey is the top-level JSON key spliced into object-shaped responses.
// SHOUTED deliberately — it has to survive being skimmed past in a wall of
// unrelated diagnostic JSON.
const alarmBodyKey = `"ALARMS":`

// Alarm is one critical condition currently firing. Detail is a plain-English
// sentence because the reader may be an operator who has never seen this kind
// before and is holding it at 3am; the structured fields are for anything
// machine-driven.
type Alarm struct {
	Kind        string    `json:"kind"`
	Since       time.Time `json:"since"`
	Consecutive int       `json:"consecutive,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	Detail      string    `json:"detail"`
}

// UmbilicalAlarmsDTO is the GET /api/village/umbilical/alarms response — the
// pull view of the same alarms every other response is stamped with. Alarms is
// empty (not null) when healthy, so a consumer can range over it unconditionally.
type UmbilicalAlarmsDTO struct {
	ContractVersion int       `json:"contract_version"`
	Now             time.Time `json:"now"`
	Alarms          []Alarm   `json:"alarms"`
}

// evaluateAlarms reads the engine's existing health monitors and returns every
// critical condition currently firing, or nil when healthy. Cheap and lock-light
// (one mutex-guarded struct copy), so it is safe to run per-request.
//
// Nil-safe throughout: CheckpointHealth.Snapshot() returns the zero value for a
// nil recorder (an engine wired without one), and a zero snapshot has a zero
// streak, so it simply never fires.
func (s *Server) evaluateAlarms(now time.Time) []Alarm {
	var out []Alarm
	if a, ok := checkpointAlarm(s.checkpointHealth.Snapshot(), now); ok {
		out = append(out, a)
	}
	return out
}

// checkpointAlarm classifies a CheckpointHealthSnapshot: it fires once the
// consecutive-failure streak reaches the threshold. Split out from evaluateAlarms
// so the threshold logic is unit-testable without standing up a Server.
//
// Since is the moment durability was last known GOOD (last_success_at) — that is
// the number an operator actually needs, because it bounds how much world state a
// restart would throw away. It falls back to the first-observed failure time when
// the engine has never checkpointed successfully (a fresh boot against a broken
// DB), where "last success" is meaningless.
func checkpointAlarm(h sim.CheckpointHealthSnapshot, now time.Time) (Alarm, bool) {
	if h.ConsecutiveFailures < checkpointFailureStreakThreshold {
		return Alarm{}, false
	}
	since := h.LastSuccessAt
	if since.IsZero() {
		since = h.LastFailureAt
	}
	detail := "DURABILITY IS BROKEN: the last " + strconv.Itoa(h.ConsecutiveFailures) +
		" checkpoints all failed, so the running world is NOT being persisted and a restart would roll back to the last good checkpoint"
	if !since.IsZero() {
		detail += " (" + humanizeSince(now.Sub(since)) + " of world state at risk)"
	}
	detail += ". Investigate now — do not restart the engine until this is understood."
	return Alarm{
		Kind:        alarmKindCheckpointFailure,
		Since:       since,
		Consecutive: h.ConsecutiveFailures,
		LastError:   h.LastError,
		Detail:      detail,
	}, true
}

// humanizeSince renders a duration as a coarse human phrase for the alarm's
// prose. Deliberately coarse — the exact seconds are already on Since; what the
// reader needs is the ORDER OF MAGNITUDE of what a restart would discard.
// A negative delta (clock skew) reads as "some" rather than a nonsense negative.
func humanizeSince(d time.Duration) string {
	if d <= 0 {
		return "an unknown amount"
	}
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " minutes"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " hours"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + " days"
	}
}

// withAlarmBanner wraps an umbilical handler so a firing alarm rides out on the
// response. Applied INSIDE requireOperator (see Server.Handler), so engine health
// is never disclosed to an unauthenticated or non-operator caller.
//
// The healthy path is a STRICT no-op: when nothing is firing the handler's
// ResponseWriter is passed straight through un-wrapped, so there is zero buffering
// cost and zero change to any existing response — every jq pipeline in existence
// keeps working byte-for-byte. Only while an alarm is actually firing (rare, and
// by definition a moment when correctness beats throughput) does the response get
// captured and rewritten.
func (s *Server) withAlarmBanner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		alarms := s.evaluateAlarms(time.Now().UTC())
		if len(alarms) == 0 {
			next(w, r)
			return
		}
		encoded, err := json.Marshal(alarms)
		if err != nil {
			// Marshalling our own struct cannot realistically fail; if it somehow
			// does, serving the un-annotated response beats 500-ing an operator who
			// is mid-incident and needs the data underneath.
			next(w, r)
			return
		}

		rec := &alarmCapture{header: http.Header{}, status: http.StatusOK}
		next(rec, r)

		body := injectAlarms(rec.buf.Bytes(), encoded)

		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.Header().Set(alarmHeader, alarmKinds(alarms))
		// The splice changes the body length, and a handler may have copied an
		// upstream Content-Length (the /turns proxy relays upstream headers). Set it
		// from what we are actually about to write, or the response is truncated.
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(rec.status)
		_, _ = w.Write(body)
	}
}

// alarmKinds renders the header value: the firing kinds, comma-separated.
func alarmKinds(alarms []Alarm) string {
	kinds := make([]string, 0, len(alarms))
	for _, a := range alarms {
		kinds = append(kinds, a.Kind)
	}
	return strings.Join(kinds, ",")
}

// alarmCapture buffers a handler's response so the body can be rewritten. Only
// used on the firing path. No Hijack passthrough: the umbilical carries no
// WebSocket upgrade (the WS /events route is not an umbilical route and is never
// wrapped), so there is nothing to hijack.
type alarmCapture struct {
	header http.Header
	buf    bytes.Buffer
	status int
	wrote  bool
}

func (c *alarmCapture) Header() http.Header { return c.header }

func (c *alarmCapture) WriteHeader(code int) {
	if !c.wrote {
		c.status = code
		c.wrote = true
	}
}

func (c *alarmCapture) Write(b []byte) (int, error) {
	if !c.wrote {
		c.status = http.StatusOK
		c.wrote = true
	}
	return c.buf.Write(b)
}

// injectAlarms splices a top-level "ALARMS" key into a JSON OBJECT body and
// returns it. A body that is not a JSON object is returned UNCHANGED.
//
// That exception is not laziness, it is arithmetic: you cannot add a top-level
// key to a JSON array, and three umbilical reads (/errors, /client-errors,
// /deadlocks) return a raw entry slice with no DTO wrapper, while /turns relays
// memory-api's body verbatim. Those responses carry the alarm on the
// X-Umbilical-Alarms header instead. Wrapping them in an object to make room for
// the key would break their established contract for every existing consumer —
// a worse trade than a header on four routes.
//
// The splice is a byte insert rather than an unmarshal/remarshal so the payload
// underneath is preserved EXACTLY (key order, number formatting, unknown fields
// from the /turns upstream) — the operator is mid-incident and the response body
// must not be quietly reshaped on its way out.
func injectAlarms(body, encoded []byte) []byte {
	open := 0
	for open < len(body) && isJSONSpace(body[open]) {
		open++
	}
	if open >= len(body) || body[open] != '{' {
		return body
	}
	// First non-space byte after '{' tells us whether the object already has any
	// members — i.e. whether our key needs a trailing comma.
	rest := open + 1
	peek := rest
	for peek < len(body) && isJSONSpace(body[peek]) {
		peek++
	}
	out := make([]byte, 0, len(body)+len(encoded)+len(alarmBodyKey)+1)
	out = append(out, body[:rest]...)
	out = append(out, alarmBodyKey...)
	out = append(out, encoded...)
	if peek < len(body) && body[peek] != '}' {
		out = append(out, ',')
	}
	out = append(out, body[rest:]...)
	return out
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// handleUmbilicalAlarms serves the pull view of the firing alarms. Mostly for the
// Godot client's operator ticker (client/scripts/village_ticker.gd polls it when
// Auth.can_edit) — an operator reading the umbilical directly gets the same alarms
// stamped onto whatever route they were already calling, and does not need this
// one. Returns an empty list, never null, when healthy.
func (s *Server) handleUmbilicalAlarms(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	alarms := s.evaluateAlarms(now)
	if alarms == nil {
		alarms = []Alarm{}
	}
	writeJSON(w, UmbilicalAlarmsDTO{
		ContractVersion: ContractVersion,
		Now:             now,
		Alarms:          alarms,
	})
}
