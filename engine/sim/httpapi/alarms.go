package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
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

// alarmKindCheckpointClamped fires when the last checkpoint could only be
// persisted by correcting a value the world had no business producing — a need
// above its ceiling, a negative quantity, a zero-length period (LLM-392).
//
// Durability is INTACT when this fires; that is what separates it from
// checkpoint_failure, and it is why the checkpoint clamps at all. But it earns a
// place in a registry this severe because a clamp is proof of a live bug that has
// ALREADY corrupted world state in memory — the durable row is the repaired
// value, not what the actor actually holds — and because the alternative to
// making this loud is a persistence layer that quietly edits the world on its way
// to disk. The clamp buys durability; the alarm is the price, and it is not
// optional.
const alarmKindCheckpointClamped = "checkpoint_clamped"

// alarmKindTickerStale fires when a cadence goroutine has stopped beating on its
// declared interval (LLM-395). This is the OTHER way the engine keeps serving
// while quietly ceasing to function: the HTTP surface answers, the world holds
// state, but needs stop decaying, shifts stop firing, sweeps stop expiring. The
// staleness judgement itself lives on sim.TickerHealthEntry, which owns the
// cadence contract; this file only classifies the result and writes the prose.
const alarmKindTickerStale = "ticker_stale"

// alarmKindWorldCommandStalled fires when the engine's single world command
// goroutine has missed its liveness deadline (LLM-402). Every mutation in the
// engine goes through that one goroutine and every ticker drives its work by
// sending to it, so its wedging is the loudest failure the engine has: the HTTP
// surface still answers, and NOTHING in the world changes.
//
// This is a MEASUREMENT, and it is the reason this kind exists next to
// ticker_stale rather than being folded into it. Stale tickers can only INFER an
// upstream cause from silence, and silence cannot distinguish a wedged world loop
// from scheduler starvation, a GC pause, a lock convoy, or a handful of unlucky
// ticker panics. The probe enqueues a no-op command and requires the round-trip
// (sim.RunWorldCommandProbe) — if it does not land, the command path is not
// serving. It is also FAST where staleness is slowest: seconds, against a
// ticker_stale floor of two minutes.
const alarmKindWorldCommandStalled = "world_command_stalled"

// worldCommandTimeoutStreakThreshold is how many CONSECUTIVE probe timeouts raise
// the alarm.
//
// Two, at a 15s cadence and a 5s deadline: ~20-35s to detect, requiring ~10s of
// continuous unavailability. One missed 5s deadline is survivable as a GC pause or
// a noisy neighbour stealing the CPU; two in a row across a 15s cadence is not a
// hiccup, it is an engine that has stopped serving. Lower than that cries wolf on
// a surface whose entire worth is that it only screams about real emergencies;
// higher buys nothing, because a wedged world loop does not un-wedge itself.
const worldCommandTimeoutStreakThreshold = 2

// tickerStaleNamesInDetail caps how many stale ticker names the alarm's prose
// lists before summarising the remainder.
//
// The cap exists because of a specific and expected failure shape: if the world
// COMMAND GOROUTINE wedges, every ticker starves at once (they all block sending
// to it), so ~36 names would otherwise be pasted into every umbilical response
// body the operator reads while trying to diagnose it. The count always tells the
// true scale; /umbilical/ticker-health carries the full per-ticker detail.
const tickerStaleNamesInDetail = 8

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
	health := s.checkpointHealth.Snapshot()
	if a, ok := checkpointAlarm(health, now); ok {
		out = append(out, a)
	}
	if a, ok := checkpointClampAlarm(health); ok {
		out = append(out, a)
	}
	// Nil-guarded: unlike the health recorders, s.world is a bare pointer whose
	// methods are not nil-safe, and this runs on EVERY umbilical response — a
	// worldless test server must not panic the whole surface.
	if s.world != nil {
		entries := s.world.TickerHealthSnapshot()
		// CAUSE BEFORE SYMPTOM, in both the ordering and the prose. A wedged world
		// command loop starves every ticker that depends on it, so these two alarms
		// fire together in the headline incident — and world_command_stalled is the
		// one that says what is actually wrong. It is evaluated first so its verdict
		// can be handed to the ticker_stale classifier, which is what lets that alarm
		// name the cause instead of hedging at a 3am operator.
		stalled, worldStalled := worldCommandStalledAlarm(
			s.world.WorldCommandHealthSnapshot(),
			probeTickerStale(entries, now),
			now,
		)
		if worldStalled {
			out = append(out, stalled)
		}
		if a, ok := tickerStaleAlarm(entries, worldStalled, now); ok {
			out = append(out, a)
		}
	}
	return out
}

// worldCommandStalledAlarm classifies the world-command liveness recorder: it
// fires once the no-op probe has missed its round-trip deadline this many times in
// a row.
//
// WHAT IT CLAIMS IS EXACTLY WHAT IT MEASURED. A missed deadline proves the command
// path missed its SERVICE deadline — which covers a wedged handler, a wedge inside
// republish or an event subscriber, AND a command queue so saturated the probe
// could not get in. Those are different diseases, so the prose says "stalled or
// saturated" and then names which half of the round-trip actually expired, rather
// than asserting a wedged goroutine the evidence does not single out.
//
// probeStale is the ticker registry's verdict on the PROBER ITSELF. It matters
// because this alarm reads recorded state: if the prober goroutine is dead, the
// streak it left behind keeps this alarm firing off a frozen reading, which would
// otherwise be indistinguishable from a live stall. When that happens the alarm
// says so rather than pretending to a currency it does not have.
//
// Since is the start of the CURRENT timeout streak — the moment the command path
// stopped serving, as closely as the prober can date it. Not last_success_at
// ("last known good" is a different and staler question, and there is no such
// moment on an engine whose world loop never came up at all), and never "now": the
// evaluator is stateless and re-derives this on every umbilical response, so a
// request-time Since would walk forward on every poll.
func worldCommandStalledAlarm(h sim.WorldCommandHealthSnapshot, probeStale bool, now time.Time) (Alarm, bool) {
	if h.ConsecutiveTimeouts < worldCommandTimeoutStreakThreshold {
		return Alarm{}, false
	}

	detail := "THE WORLD IS NOT PROCESSING COMMANDS: " + strconv.Itoa(h.ConsecutiveTimeouts) +
		" consecutive no-op liveness probes have missed their " + formatSeconds(h.ProbeTimeoutSeconds) +
		" round-trip deadline, so the single world command goroutine is stalled or saturated. " +
		"The engine is still answering HTTP, but NOTHING in the village is changing — no deliberation, " +
		"no movement, no trade, no needs decay"
	if !h.TimeoutStreakStartedAt.IsZero() {
		detail += " (since " + humanizeSince(now.Sub(h.TimeoutStreakStartedAt)) + " ago)"
	}
	detail += ". " + describeWorldCommandPhase(h.LastTimeoutPhase)
	if probeStale {
		// The instrument is dead, so this reading is a fossil. Say it plainly — an
		// operator must not act on a stale measurement believing it is a live one.
		detail += " WARNING: the prober itself has stopped beating (see the ticker_stale alarm), " +
			"so this reading is FROZEN at its last observation, not live."
	}
	detail += " See /umbilical/world-command-health."

	return Alarm{
		Kind:        alarmKindWorldCommandStalled,
		Since:       h.TimeoutStreakStartedAt,
		Consecutive: h.ConsecutiveTimeouts,
		LastError:   h.LastError,
		Detail:      detail,
	}, true
}

// describeWorldCommandPhase renders the operator's actual next move, which differs
// by which half of the round-trip expired — the whole reason the probe records the
// phase instead of reporting an undifferentiated timeout.
func describeWorldCommandPhase(phase sim.WorldCommandPhase) string {
	switch phase {
	case sim.WorldCommandPhaseEnqueue:
		return "The probe could not even ENQUEUE its command — the command queue has been full for the " +
			"whole deadline, so the loop is being out-produced or has backed up behind something stuck. " +
			"Look at what is flooding it."
	case sim.WorldCommandPhaseReply:
		return "The world goroutine ACCEPTED the command and never completed it — a wedged command handler, " +
			"a wedge in republish or an event subscriber, or a process starved so hard the goroutine is not " +
			"being scheduled. Get a goroutine dump."
	default:
		return "The round-trip did not complete."
	}
}

// probeTickerStale reports whether the liveness prober's OWN ticker has gone
// stale — i.e. the goroutine that produces the world-command measurement has
// itself stopped.
func probeTickerStale(entries []sim.TickerHealthEntry, now time.Time) bool {
	for _, e := range entries {
		if e.Name == sim.WorldCommandProbeTickerName {
			return e.IsStale(now)
		}
	}
	return false
}

// formatSeconds renders a whole-second duration for the alarm prose ("5s"),
// falling back to a decimal for a sub-second one so a retuned constant can never
// render as a bare "0s".
func formatSeconds(secs float64) string {
	if secs == math.Trunc(secs) {
		return strconv.Itoa(int(secs)) + "s"
	}
	return strconv.FormatFloat(secs, 'g', -1, 64) + "s"
}

// tickerStaleAlarm classifies the ticker-health registry: it fires when one or
// more registered tickers have missed their declared cadence.
//
// ONE AGGREGATE ALARM, NOT ONE PER TICKER. The tickers are not independent — they
// all drive their work by sending to the single world command goroutine, so the
// headline failure (that goroutine wedging, or the process starving) takes every
// one of them out simultaneously. Per-ticker alarms would stamp ~36 entries onto
// every response in exactly the incident where the operator most needs the
// surface to stay readable. The kind is the grouping key the alarm framework
// already provides; the names go in the prose.
//
// Since is the EARLIEST moment any of the stale tickers crossed its deadline —
// derived from recorded beat/registration state (see TickerHealthEntry.StaleSince),
// never from when an HTTP request happened to notice. That keeps the alarm stable
// across requests and self-clearing the moment the cadence resumes, which is what
// lets the evaluator stay stateless.
//
// worldStalled is the world_command_stalled verdict, and it turns this alarm's
// worst case from a shrug into a DIAGNOSIS (LLM-402). Mass staleness has exactly
// two shapes and they have opposite fixes: the tickers are starving on a wedged
// world command loop, or the world loop is fine and the tickers themselves (or the
// process scheduling them) are dying. Silence alone cannot tell those apart —
// which is why this alarm's prose used to hedge — but the liveness probe can, so
// the answer is stated instead of guessed.
//
// THE PROBER IS EXCLUDED FROM THE "EVERY TICKER" HEADCOUNT, and that exclusion is
// load-bearing rather than cosmetic. The prober beats BEFORE each send precisely so
// a wedged world cannot silence it (see sim.WorldCommandProbeTickerName) — so in
// the exact incident the all-stale branch was written for, the prober is the one
// ticker still beating, and a naive len(stale) == len(entries) would be false
// forever. The branch would have quietly become unreachable in the case it exists
// to describe.
func tickerStaleAlarm(entries []sim.TickerHealthEntry, worldStalled bool, now time.Time) (Alarm, bool) {
	var (
		stale []sim.TickerHealthEntry
		since time.Time
		// The world-dependent population: every ticker that drives its work through
		// the world command loop, i.e. all of them except the loop's own prober.
		worldDependent      int
		worldDependentStale int
	)
	for _, e := range entries {
		isProbe := e.Name == sim.WorldCommandProbeTickerName
		if !isProbe {
			worldDependent++
		}
		if !e.IsStale(now) {
			continue
		}
		if !isProbe {
			worldDependentStale++
		}
		stale = append(stale, e)
		if at := e.StaleSince(); since.IsZero() || at.Before(since) {
			since = at
		}
	}
	if len(stale) == 0 {
		return Alarm{}, false
	}

	names := make([]string, 0, len(stale))
	for _, e := range stale {
		names = append(names, e.Name)
	}
	listed := names
	suffix := ""
	if len(listed) > tickerStaleNamesInDetail {
		listed = listed[:tickerStaleNamesInDetail]
		suffix = ", and " + strconv.Itoa(len(names)-tickerStaleNamesInDetail) + " more"
	}

	detail := "CADENCE DRIVERS HAVE STOPPED: " + strconv.Itoa(len(stale)) +
		" of the engine's interval goroutines (" + strings.Join(listed, ", ") + suffix +
		") have missed their expected cadence. The engine is still serving requests, but the work those " +
		"tickers drive — needs decay, shift changes, sweeps — is NOT happening"
	if !since.IsZero() {
		detail += " (first went stale " + humanizeSince(now.Sub(since)) + " ago)"
	}
	switch {
	case worldStalled:
		// The cause is MEASURED, not inferred. Point at it and get out of the way:
		// this alarm is the shadow of the other one, and an operator who fixes the
		// world command loop fixes this too.
		detail += ". The world command loop is CONFIRMED STALLED (see the world_command_stalled alarm) — " +
			"these tickers are starving on it, not dying independently. Fix that first; this alarm is its shadow"
	case worldDependent > 1 && worldDependentStale == worldDependent:
		// Every ticker that depends on the world is stale, and yet the world is
		// answering its liveness probe. That REMOVES the obvious suspect, which is
		// the single most useful thing this sentence can do at 3am.
		detail += ". EVERY world-dependent ticker is stale, and yet the world command loop IS completing " +
			"its liveness probes — so this is NOT a wedged world. Suspect the process itself (scheduler " +
			"starvation, a GC death spiral) or the ticker goroutines, and check /umbilical/world-command-health"
	}
	detail += ". See /umbilical/ticker-health for per-ticker detail."

	return Alarm{
		Kind:   alarmKindTickerStale,
		Since:  since,
		Detail: detail,
	}, true
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

// checkpointClampAlarm classifies the clamp side of a CheckpointHealthSnapshot: it
// fires whenever the most recent SUCCESSFUL checkpoint had to correct at least one
// out-of-range value to be persistable at all (LLM-392).
//
// Threshold of one, unlike checkpoint_failure's three. A failed checkpoint can be a
// transient blip that self-heals (a lock timeout, a pg hiccup), which is why that
// alarm waits for a streak. A clamp cannot: it means an engine code path computed a
// value that no rule of the world permits, and it computed it BEFORE any of this
// ran. There is no benign version to wait out.
//
// It reads last-checkpoint state, so it self-clears as soon as one checkpoint
// completes without a correction — which keeps the evaluator stateless and means a
// fixed bug stops the alarm on the next cadence, with no ack and no restart.
//
// Since is the moment the clamped checkpoint was WRITTEN (last_success_at), not the
// moment a request noticed — so the alarm reads identically on every response until
// the next checkpoint moves it, per this file's stability rule.
func checkpointClampAlarm(h sim.CheckpointHealthSnapshot) (Alarm, bool) {
	if h.LastClampCount <= 0 {
		return Alarm{}, false
	}
	detail := "THE WORLD IS PRODUCING IMPOSSIBLE VALUES: the last checkpoint could only be saved by correcting " +
		strconv.Itoa(h.LastClampCount) +
		" value(s) that no valid world state can hold — a need past its ceiling, a negative quantity, a zero-length period. " +
		"Durability is INTACT (the correction is what kept it), but a live engine bug has already corrupted this state in " +
		"memory, and what is now on disk is the repaired value, NOT what the world believed"
	if len(h.LastClamps) > 0 {
		detail += ". First: " + describeClamp(h.LastClamps[0])
	}
	detail += ". Find the code path that wrote it; see /umbilical/checkpoint-health for the full list."
	return Alarm{
		Kind:        alarmKindCheckpointClamped,
		Since:       h.LastSuccessAt,
		Consecutive: h.LastClampCount,
		Detail:      detail,
	}, true
}

// describeClamp renders one correction for the alarm's prose. The operator reading
// it may never have seen this alarm before, so it spells out the transition rather
// than emitting a struct.
func describeClamp(c sim.Clamp) string {
	return c.Table + "." + c.Field + " for " + c.Key + " held " + c.From + ", written as " + c.To
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

		// Copy the handler's headers out. The values are cloned rather than aliased
		// so the outbound header map can never share backing arrays with the capture.
		for k, v := range rec.header {
			w.Header()[k] = append([]string(nil), v...)
		}
		// The header is the one signal that reaches EVERY response, including the
		// ones we must not or cannot touch the body of. Always set it.
		w.Header().Set(alarmHeader, alarmKinds(alarms))

		body := rec.buf.Bytes()
		if responseAllowsBody(r.Method, rec.status) && bodyAcceptsAlarmSplice(rec.header) {
			body = injectAlarms(body, encoded)
			// The splice changes the body length, so Content-Length must be restated
			// from what we are actually about to write or the response truncates.
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(rec.status)
			_, _ = w.Write(body)
			return
		}

		// A no-body status (204/304/1xx) or a HEAD request must not carry a body, and
		// an encoded body must not be spliced. Emit the handler's response as it was,
		// carrying the alarm on the header alone. Content-Length is dropped rather
		// than restated: for a no-body response we write nothing, and net/http will
		// set the right thing for what we do write.
		w.Header().Del("Content-Length")
		w.WriteHeader(rec.status)
		if responseAllowsBody(r.Method, rec.status) {
			_, _ = w.Write(body)
		}
	}
}

// responseAllowsBody reports whether a response with this method + status is
// permitted to carry a body at all. Writing one anyway is a protocol violation
// that net/http will complain about ("request method or response status code
// does not allow body").
//
// HEAD is reachable here: Go's ServeMux routes a HEAD request to a "GET <path>"
// pattern, so every umbilical GET handler can be entered by a HEAD.
func responseAllowsBody(method string, status int) bool {
	if method == http.MethodHead {
		return false
	}
	if status >= 100 && status < 200 {
		return false
	}
	return status != http.StatusNoContent && status != http.StatusNotModified
}

// bodyAcceptsAlarmSplice reports whether the captured body is raw bytes we may
// safely splice a key into — i.e. it is not content-encoded.
//
// No umbilical route sets Content-Encoding today (the /turns proxy relays only
// Content-Type and io.Copy's the body through), so this is a guard against the
// future: a route that ever relays a gzip'd upstream body would otherwise have
// compressed bytes handed to a JSON byte-splice. Compressed bytes almost never
// begin with '{' so injectAlarms would decline anyway, but "almost never" is not
// an invariant to hang a live operator surface on. Encoded → header-only.
func bodyAcceptsAlarmSplice(h http.Header) bool {
	enc := h.Get("Content-Encoding")
	return enc == "" || strings.EqualFold(enc, "identity")
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
// used on the firing path.
//
// It implements ResponseWriter and NOTHING ELSE, deliberately:
//
//   - No Hijack. The umbilical carries no WebSocket upgrade (the WS /events route
//     is not an umbilical route and is never wrapped), so there is nothing to
//     hijack, and claiming to support it would be a lie.
//   - No Flush. A no-op Flush() would be worse than its absence: it makes
//     w.(http.Flusher) succeed, so a streaming handler would believe it had
//     flushed bytes to the client when they are in fact sitting in this buffer.
//     Leaving it unimplemented makes the type assertion fail, which is the honest
//     answer for a buffered writer and the safe branch for any handler that tests
//     for it.
//
// The constraint this places on the surface: AN UMBILICAL HANDLER MUST NOT DEPEND
// ON OPTIONAL ResponseWriter INTERFACES. Every one today just writes a JSON body,
// so this holds; a future streaming umbilical route would have to bypass or teach
// this wrapper.
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
//
// The invariant is strict: VALID JSON OBJECT gets the key, everything else is
// returned byte-for-byte. Starting with '{' is necessary but not sufficient — a
// truncated body ("{"), a text/plain diagnostic that happens to open with a
// brace, or any malformed object would otherwise be spliced into a DIFFERENTLY
// malformed payload, with a freshly-computed Content-Length lending it false
// authority. json.Valid buys that guarantee for one linear scan, and only ever on
// the firing path.
func injectAlarms(body, encoded []byte) []byte {
	open := 0
	for open < len(body) && isJSONSpace(body[open]) {
		open++
	}
	if open >= len(body) || body[open] != '{' {
		return body
	}
	if !json.Valid(body) {
		return body
	}
	// First non-space byte after '{' tells us whether the object already has any
	// members — i.e. whether our key needs a trailing comma.
	rest := open + 1
	peek := rest
	for peek < len(body) && isJSONSpace(body[peek]) {
		peek++
	}
	// Grown by append rather than pre-sized. Summing the three lengths to
	// pre-allocate is arithmetic on a value derived from a proxied upstream body
	// (/turns), which is exactly the shape CodeQL's go/allocation-size-overflow
	// flags. The saving was a micro-optimization on a path that only runs WHILE AN
	// ALARM IS FIRING; it is not worth carrying an overflow-prone size computation
	// on an operator surface, and it is certainly not worth a suppression.
	var out []byte
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
