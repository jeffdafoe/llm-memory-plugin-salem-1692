package llm

import "context"

// memory_search.go — the memory-search capability the recall observation
// tool needs. Mirrors the llm.Client split: the interface lives here (the
// shared low-level package both handlers and memapi depend on), the HTTP
// implementation lives in llm/memapi, and the consumer is
// handlers.RegisterRecall. ZBBS-WORK-321 (port of v1's recall tool).

// MemoryHit is one note-grouped result from a memory search: the
// best-matching chunk for a (namespace, source_file), plus its similarity
// and how many chunks of that note matched. Mirrors v1's searchMemoryHit
// (engine/agent_client.go).
type MemoryHit struct {
	SourceFile string
	Heading    string
	ChunkText  string
	Namespace  string
	Similarity float64
	ChunkCount int
}

// MemorySearcher is the narrow capability recall needs: a semantic search
// over a SINGLE namespace's notes/dreams/impressions. Scoping is the
// caller's responsibility — recall passes the acting NPC's own namespace,
// never "*", so an NPC can only remember its own memory.
//
// slugPrefix narrows the search below the namespace to source_files under a
// prefix (LLM-355/356): a shared-VA NPC's memory lives at "<name>/memory/…"
// inside one shared namespace (salem-vendor), so recall passes "<name>/" to
// keep each NPC from remembering another's. Empty = whole namespace (a
// dedicated-VA NPC owns its namespace outright, so its recall spans notes,
// dreams, and impressions). Passing it is mandatory at the type level so a
// caller can't silently forget the isolation.
//
// Implemented by llm/memapi.Client. An empty result is NOT an error.
type MemorySearcher interface {
	SearchMemory(ctx context.Context, namespace, query, slugPrefix string, limit int) ([]MemoryHit, error)
}
