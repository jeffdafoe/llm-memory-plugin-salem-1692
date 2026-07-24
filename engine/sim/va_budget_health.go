package sim

import (
	"sort"
	"sync"
	"time"
)

// va_budget_health.go — the live record of which virtual agents the engine's
// LLM boundary is currently refusing for cost-budget exhaustion (LLM-513).
//
// Why this exists: when a VA exhausts its configured daily/monthly cost budget,
// memory-api refuses every call to it (HTTP 402), so the NPC that VA drives can
// no longer think. Before LLM-513 that refusal only logged as text on the
// memory-api side and reached the engine misclassified as a malformed model
// response — a village-fatal condition sitting behind a door nobody opens, the
// same failure mode the checkpoint alarm was built to kill (see
// httpapi/alarms.go). VABudgetHealth is the health struct the umbilical alarm
// evaluator reads to raise a budget alarm, mirroring how CheckpointHealth backs
// the checkpoint_failure alarm.
//
// Live-only and in-memory by design, like CheckpointHealth: a VA is added to
// the capped set the moment a call to it is refused with
// llm.ErrorBudgetExceeded and removed on that same VA's next successful call, so
// the derived alarm reflects what is broken RIGHT NOW and self-clears when the
// rolling budget window resets or the limit is raised. There is no durable row
// and no restart survival — a still-capped VA re-refuses within a tick of the
// next umbilical read, so a reboot can only forget a cap that already healed
// (see shared GUIDELINES: "Postgres is for durable storage, not infrastructure
// substitute"). The memapi client feeds it through the memapi.BudgetObserver
// interface, which this type satisfies.

// VABudgetHealth tracks the set of virtual agents currently refused for budget
// exhaustion. Safe for concurrent use — Complete calls it from many tick
// goroutines at once. The zero value is an empty, ready recorder, and every
// method is nil-safe so an engine wired without one simply never fires the
// alarm.
type VABudgetHealth struct {
	mu     sync.Mutex
	capped map[string]vaBudgetEntry
}

// vaBudgetEntry is the per-agent cap detail: when the cap first went live (held
// across repeated refusals so the alarm's "since" does not creep forward every
// tick), when the MOST RECENT refusal was recorded (the guard that keeps a
// stale in-flight success from clearing a newer refusal's cap — see
// RecordBudgetOK), and the latest refusal reason for the operator.
type vaBudgetEntry struct {
	since         time.Time
	lastRefusalAt time.Time
	detail        string
}

// VABudgetHealthSnapshot is an immutable point-in-time copy of a
// VABudgetHealth, suitable for the umbilical alarm evaluator. Capped is sorted
// by agent slug so the derived alarm string is byte-stable across reads.
type VABudgetHealthSnapshot struct {
	Capped []VABudgetEntry `json:"capped"`
}

// VABudgetEntry is one capped VA in a snapshot.
type VABudgetEntry struct {
	Agent  string    `json:"agent"`
	Since  time.Time `json:"since"`
	Detail string    `json:"detail"`
}

// RecordBudgetExceeded marks agent as currently refused for budget exhaustion
// at now, stashing detail (the refusal reason) for the operator. Idempotent
// across a run of refusals: the first call sets the since timestamp and later
// calls only refresh the detail, so the alarm reports when the cap actually
// began, not when it was last observed. Nil-safe. Implements
// memapi.BudgetObserver.
func (h *VABudgetHealth) RecordBudgetExceeded(agent string, now time.Time, detail string) {
	if h == nil || agent == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.capped == nil {
		h.capped = make(map[string]vaBudgetEntry)
	}
	entry, ok := h.capped[agent]
	if !ok {
		entry.since = now
	}
	entry.lastRefusalAt = now
	entry.detail = detail
	h.capped[agent] = entry
}

// RecordBudgetOK clears the cap on agent when a call to it succeeds — but ONLY
// when that call STARTED after the most recent refusal was recorded (callStart
// is the successful Complete's start time). Complete runs concurrently, so a
// success that was already in flight when a NEWER request hit the 402 reflects
// stale budget state; clearing on it would let the alarm self-clear while the
// VA is still capped. Guarding on callStart vs lastRefusalAt keeps a stale
// in-flight success from clearing a newer refusal's cap — conservative toward
// keeping a fire alarm lit. A no-op when the agent is not capped. Nil-safe.
// Implements memapi.BudgetObserver.
func (h *VABudgetHealth) RecordBudgetOK(agent string, callStart time.Time) {
	if h == nil || agent == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.capped[agent]
	if !ok {
		return
	}
	if callStart.After(entry.lastRefusalAt) {
		delete(h.capped, agent)
	}
}

// Snapshot returns an immutable copy of the current capped set, sorted by agent
// slug. Nil-safe (returns the zero value with an empty slice), so the umbilical
// handler works even before any wiring.
func (h *VABudgetHealth) Snapshot() VABudgetHealthSnapshot {
	if h == nil {
		return VABudgetHealthSnapshot{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]VABudgetEntry, 0, len(h.capped))
	for agent, entry := range h.capped {
		out = append(out, VABudgetEntry{Agent: agent, Since: entry.since, Detail: entry.detail})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return VABudgetHealthSnapshot{Capped: out}
}
