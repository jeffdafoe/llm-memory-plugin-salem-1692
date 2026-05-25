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
// Implemented by llm/memapi.Client. An empty result is NOT an error.
type MemorySearcher interface {
	SearchMemory(ctx context.Context, namespace, query string, limit int) ([]MemoryHit, error)
}
