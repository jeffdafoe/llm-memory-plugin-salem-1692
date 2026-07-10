package llm

import (
	"context"
	"time"
)

// memory_write.go — the note-writing capability the memorize observation tool
// needs (LLM-356). Companion to memory_search.go's MemorySearcher: the
// interface lives here (the shared low-level package both handlers and memapi
// depend on), the HTTP implementation lives in llm/memapi, and the consumer is
// handlers.RegisterMemorize.

// NoteMeta is one note's identity and freshness, as returned by a prefix list.
// The memorize tool uses it to prune an NPC's memory to a fixed cap: it sorts
// by the most recent of the three timestamps (the same "freshness" the search
// decay uses — GREATEST(created_at, updated_at, last_accessed)) and drops the
// stalest. LastAccessed is the zero time when the note has never been recalled;
// callers must fold it into the max, not read it alone (a just-saved note has a
// null last_accessed and would otherwise look infinitely stale).
type NoteMeta struct {
	Slug         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastAccessed time.Time
}

// Freshness is the note's effective recency — the most recent of its three
// timestamps. Prune order is ascending by this value, matching the search
// decay's age basis so a note that keeps being recalled resists eviction.
func (n NoteMeta) Freshness() time.Time {
	newest := n.CreatedAt
	if n.UpdatedAt.After(newest) {
		newest = n.UpdatedAt
	}
	if n.LastAccessed.After(newest) {
		newest = n.LastAccessed
	}
	return newest
}

// MemoryWriter is the capability memorize needs to make an NPC's chosen memory
// durable: write one note, list the NPC's memory notes to enforce the cap, and
// soft-delete the stalest over it. Implemented by llm/memapi.Client against the
// /v1/documents/* routes (NOT /v1/memory/ingest — a note written through
// documents/save is auto-indexed for search AND browsable in the admin UI,
// whereas a raw ingest is searchable chunks with no note).
//
// SaveNote always upserts: memorize resolves the same (date, topic) to the same
// slug, so re-memorizing a topic the same day revises the note in place instead
// of duplicating it. cognitiveType is stamped into the note metadata to select
// its search-decay half-life (e.g. "episodic" — memories fade over a season
// unless recall reinforces them).
//
// Errors carry no llm.Error classification (like SearchMemory): memorize turns
// any failure into an in-character tool result, so it only needs
// success-vs-failure.
type MemoryWriter interface {
	SaveNote(ctx context.Context, namespace, slug, title, content, cognitiveType string) error
	ListNotes(ctx context.Context, namespace, slugPrefix string) ([]NoteMeta, error)
	DeleteNote(ctx context.Context, namespace, slug string) error
}
