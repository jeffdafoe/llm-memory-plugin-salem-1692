// Package chatlog provides a non-blocking, bounded, in-memory store of the
// engine<->model CHAT EXCHANGE per scene — the rendered perception the engine
// SENT (tx) and the model's RESPONSES + tool calls it got back (rx) — keyed by
// the per-tick scene_id (llm.NewSceneID, the same id stamped on
// chat_message_texts.scene_id in llm-memory). ZBBS-HOME-382.
//
// Why it exists: to read what an NPC was told and what it said back for a given
// scene, an operator otherwise had to log into the llm-memory admin dashboard
// (Communications->Chat), which can't even filter by scene. This ring taps the
// exchange at the engine boundary and exposes it on the operator-gated umbilical
// (GET /api/village/umbilical/chat?scene=), so the conversation is visible from the umbilical
// with no llm-memory login.
//
// Why PER-SCENE (not per-actor like promptlog, not one global ring like
// telemetry): the debug question is "show me scene X's exchange." A scene is one
// actor's single deliberation tick (sceneID is minted once per tick), so a small
// per-scene ring holds that tick's perception + every response round together.
// Two bounds keep memory finite: perSceneCap records per scene, AND a cap on the
// number of distinct scenes retained (DefaultMaxScenes) — when a new scene would
// exceed it, the least-recently-active scene is evicted. Salem mints a fresh
// sceneID every tick, so without the scene cap the map would grow without bound.
//
// Non-blocking contract (same as promptlog/telemetry): WriteChat runs on the
// tick-worker goroutines and must never wait on a reader. The mutex is held only
// for a map lookup + slice append/trim, never across I/O. In-memory only; lost
// on restart by design — a debug surface, not durable state.
//
// REDACTION: like promptlog (and unlike the redacted telemetry ring), a
// ChatRecord carries raw prompt + response text. It is a debug-only surface and
// MUST reach ONLY the operator-gated umbilical — never telemetry, the action
// log, a player-facing path, or durable storage.
package chatlog

import (
	"sync"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// DefaultPerSceneCapacity is the per-scene record retention used when New is
// given a non-positive capacity. One tick = 1 perception (tx) + up to a handful
// of response rounds (rx, bounded by the harness iteration budget), so a small
// cap holds a full scene with headroom.
const DefaultPerSceneCapacity = 16

// DefaultMaxScenes bounds the number of distinct scenes retained at once. Salem
// mints a fresh sceneID every tick, so this is the real memory bound — a window
// of recent scenes across all actors. When exceeded, the least-recently-active
// scene is evicted (see WriteChat).
const DefaultMaxScenes = 256

// Stats is a point-in-time summary of a RingSink, surfaced so an operator can
// see how much exchange history is retained.
type Stats struct {
	// PerSceneCapacity is the fixed max records retained per scene.
	PerSceneCapacity int
	// MaxScenes is the fixed max number of distinct scenes retained at once —
	// the real memory bound (sceneID churns once per tick). Surfaced so the
	// startup log can report both bounds (operators otherwise can't tell total
	// chat retention from the per-scene cap alone).
	MaxScenes int
	// Scenes is how many distinct scenes currently have buffered records.
	Scenes int
	// Buffered is the total records currently held across all scenes.
	Buffered int
	// Written is the total records ever accepted (monotonic).
	Written uint64
	// Dropped is the total records evicted (per-scene overflow or whole-scene
	// eviction) (monotonic).
	Dropped uint64
}

// RingSink is a per-scene bounded store of ChatRecords, safe for concurrent
// writers (tick workers) and readers (umbilical HTTP goroutines). Implements
// sim.ChatSink.
type RingSink struct {
	mu        sync.Mutex
	cap       int
	maxScenes int
	byScene   map[string][]sim.ChatRecord
	written   uint64
	dropped   uint64
}

// New builds a RingSink retaining the last perSceneCap records PER SCENE, across
// at most DefaultMaxScenes distinct scenes. A non-positive capacity falls back
// to DefaultPerSceneCapacity.
func New(perSceneCap int) *RingSink {
	if perSceneCap <= 0 {
		perSceneCap = DefaultPerSceneCapacity
	}
	return &RingSink{
		cap:       perSceneCap,
		maxScenes: DefaultMaxScenes,
		byScene:   make(map[string][]sim.ChatRecord),
	}
}

// WriteChat records one exchange entry for rec.SceneID, evicting that scene's
// oldest record when the per-scene ring is full. Non-blocking: holds the mutex
// only for the append/trim. A record with an empty SceneID is dropped (nothing
// to key on). Implements sim.ChatSink.
func (r *RingSink) WriteChat(rec sim.ChatRecord) {
	if r == nil || rec.SceneID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buf, exists := r.byScene[rec.SceneID]
	// A NEW scene arriving at the scene cap evicts the least-recently-active
	// scene's whole ring, so the distinct-scene count stays bounded under the
	// per-tick sceneID churn. Existing scenes never trigger eviction.
	if !exists && len(r.byScene) >= r.maxScenes {
		r.evictStalestSceneLocked()
	}
	buf = append(buf, rec)
	if len(buf) > r.cap {
		over := len(buf) - r.cap
		buf = append(buf[:0:0], buf[over:]...)
		r.dropped += uint64(over)
	}
	r.byScene[rec.SceneID] = buf
	r.written++
}

// evictStalestSceneLocked deletes the scene whose most-recent record has the
// OLDEST timestamp (rec.At), counting its dropped records. In production At is
// the per-tick clock (distinct per tick), so this is effectively
// least-recently-active; with an injected/zero clock, timestamp ties are broken
// by map iteration order — acceptable for a bounded debug surface. Mirrors
// promptlog's evictStalestActorLocked. Caller holds r.mu. O(scenes), run only on
// overflow (a new scene arriving at the cap).
func (r *RingSink) evictStalestSceneLocked() {
	var victim string
	var stalest time.Time
	have := false
	for id, buf := range r.byScene {
		var last time.Time
		if len(buf) > 0 {
			last = buf[len(buf)-1].At
		}
		if !have || last.Before(stalest) {
			victim, stalest, have = id, last, true
		}
	}
	if have {
		r.dropped += uint64(len(r.byScene[victim]))
		delete(r.byScene, victim)
	}
}

// Recent returns up to limit of a scene's records, OLDEST FIRST (chronological,
// matching the prompt/telemetry read shape). limit <= 0 returns all retained.
// The returned slice is a fresh copy — the caller may keep it past the lock.
// Empty (non-nil) when the scene has no buffered records.
func (r *RingSink) Recent(sceneID string, limit int) []sim.ChatRecord {
	if r == nil {
		return []sim.ChatRecord{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buf := r.byScene[sceneID]
	if len(buf) == 0 {
		return []sim.ChatRecord{}
	}
	start := 0
	if limit > 0 && limit < len(buf) {
		start = len(buf) - limit // keep the most-recent `limit`, still oldest-first
	}
	out := make([]sim.ChatRecord, len(buf)-start)
	copy(out, buf[start:])
	return out
}

// Stats returns a point-in-time summary.
func (r *RingSink) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buffered := 0
	for _, buf := range r.byScene {
		buffered += len(buf)
	}
	return Stats{
		PerSceneCapacity: r.cap,
		MaxScenes:        r.maxScenes,
		Scenes:           len(r.byScene),
		Buffered:         buffered,
		Written:          r.written,
		Dropped:          r.dropped,
	}
}
