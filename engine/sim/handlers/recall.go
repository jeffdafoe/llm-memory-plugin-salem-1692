package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// recall.go — production recall observation tool, ZBBS-WORK-321 (port of v1's
// recall verb, engine/agent_tick.go).
//
// recall is the memory-grounded "try to remember" verb: it semantic-searches
// the acting NPC's OWN llm-memory namespace (notes, dreams, impressions) and
// returns the top hits as a tool-result text block the model reads on its next
// harness iteration. It is an OBSERVATION (ClassObservation) — it never ends
// the tick and never mutates the world; the only side effect is a read against
// llm-memory-api.
//
// Namespace scoping: recall searches in.LLMMemoryAgent (the acting actor's own
// namespace, resolved by the harness from the tick snapshot), never "*". An
// NPC can only remember its own memory.
//
// Failure handling mirrors v1's resolveRecall: every failure mode (empty
// query, no namespace, search error, no hits) returns an in-character text
// string as the tool RESULT, not a handler error — so the model reads a
// graceful "the memory wouldn't come" rather than a "[error: handler_failed]"
// label. The only error return is an args-type mismatch, which is a
// registration bug, not a runtime condition.

const (
	// recallResultLimit caps how many note-grouped hits recall returns.
	// v1 parity (engine/agent_tick.go).
	recallResultLimit = 5
	// recallQueryMaxChars caps the query length before search. v1 parity.
	// Truncation is by rune (v1 byte-sliced, which could split a UTF-8
	// rune); the cap is a cost guard, so trimming a few extra runes is fine.
	recallQueryMaxChars = 500
)

// recall in-character result strings — ported from v1 resolveRecall /
// formatRecallHits so the model sees the same prose it learned around.
const (
	recallNoQueryText  = "You tried to remember something but couldn't form the question."
	recallFailedText   = "You tried to recall but the memory wouldn't come."
	recallNoMemoryText = "Nothing comes to mind."
)

// RecallArgs is the decoded shape of the recall tool's single argument.
type RecallArgs struct {
	Query string `json:"query"`
}

// recallSchema is the JSON Schema bytes shipped to the LLM provider. Single
// required string `query`. Description ported verbatim from v1.
var recallSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "query": {
            "type": "string",
            "minLength": 1,
            "description": "What you're trying to remember."
        }
    },
    "required": ["query"],
    "additionalProperties": false
}`)

// recallDescription is the tool description advertised to the model. Framed as
// the counterpart to memorize (memapi read side of memorize's write) and told it
// won't invent unstored details, to curb models firing recall speculatively when
// they've stored nothing relevant.
const recallDescription = "Try to remember something — the counterpart to memorize. It searches what you've stored, plus your dreams and impressions, for anything relevant; it won't invent what you've never memorized."

// DecodeRecallArgs parses the raw tool-call arguments into a RecallArgs.
// Rejects non-object payloads, unknown fields, and trailing data — same
// pattern as DecodeTakeBreakArgs. An empty/whitespace query is NOT rejected
// here: the handler turns it into the in-character recallNoQueryText result,
// matching v1 (resolveRecall returns prose, not an error, for an empty query).
func DecodeRecallArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("recall: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args RecallArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("recall: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("recall: trailing data after JSON object")
		}
		return nil, fmt.Errorf("recall: malformed trailing data: %w", err)
	}
	return args, nil
}

// formatRecallHits renders the search hits as the tool-result text block.
// v2-simplified vs v1: recall searches a SINGLE namespace (the actor's own),
// so v1's per-hit "[namespace display name]" label (which required a
// slug→name cache refreshed every tick) is dropped — every hit is from the
// actor's own memory. Empty hits → recallNoMemoryText.
//
// The per-hit label is the note's Heading, not its SourceFile (LLM-356): a
// memorize slug is a path like "anne-walker/memory/2026-07-10-blacksmiths-name"
// — a raw identifier dropped into a scene — whereas the heading is the
// human-phrased topic ("## The blacksmith's name"). Fall back to SourceFile
// only when a hit carries no heading (raw ingest / pre-memorize note).
//
// A hit with a known CreatedAt is framed with its age (LLM-390): "From two
// days ago — <heading>:". Without it, a two-day-old "Day's end reflections"
// reads as if it happened tonight, so stale intentions ("offered to help
// Josiah tomorrow") pass for current ones. The age wraps the hit rather than
// being spliced into the heading — the heading is the NPC's own authored
// topic name, not a generic label. Zero CreatedAt or zero now (older API,
// raw ingest, hand-built test input) keeps the bare "— <heading> —" form.
func formatRecallHits(hits []llm.MemoryHit, now time.Time) string {
	if len(hits) == 0 {
		return recallNoMemoryText
	}
	var b strings.Builder
	b.WriteString("You remember:\n\n")
	for _, h := range hits {
		label := strings.TrimSpace(strings.TrimLeft(h.Heading, "#"))
		text := h.ChunkText
		if label != "" {
			// The heading is the label; drop it from the body so it isn't shown
			// twice (a memorize chunk_text leads with its own "## topic" line).
			text = dropLeadingHeadingLine(text)
		} else {
			label = h.SourceFile
		}
		// A CreatedAt after now (API/clock skew, bad data) would render as
		// AgoPhrase's "just now" — the negative-delta clamp is right for the
		// conversation stamps it was built for, but a memory framed "From
		// just now" makes bad data look current. Unknown-age (bare form) is
		// the honest render for a future timestamp (code_review, LLM-390).
		if h.CreatedAt.After(now) {
			fmt.Fprintf(&b, "— %s —\n%s\n\n", label, strings.TrimSpace(text))
			continue
		}
		if ago := perception.AgoPhrase(h.CreatedAt, now); ago != "" {
			fmt.Fprintf(&b, "From %s — %s:\n%s\n\n", ago, label, strings.TrimSpace(text))
		} else {
			fmt.Fprintf(&b, "— %s —\n%s\n\n", label, strings.TrimSpace(text))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// dropLeadingHeadingLine removes a leading markdown heading line (## …) and any
// blank line right after it, so recall doesn't print the topic both as the hit
// label and again atop the body.
func dropLeadingHeadingLine(text string) string {
	trimmed := strings.TrimLeft(text, " \t\n")
	if !strings.HasPrefix(trimmed, "#") {
		return text
	}
	nl := strings.IndexByte(trimmed, '\n')
	if nl < 0 {
		return ""
	}
	return strings.TrimLeft(trimmed[nl+1:], "\n")
}

// makeRecallHandler builds the recall ObservationFn closed over searcher. It
// runs OFF the world goroutine (worker pool); searcher.SearchMemory honors
// ctx. See the file-level doc for the in-character failure-handling contract.
func makeRecallHandler(searcher llm.MemorySearcher) ObservationFn {
	return func(ctx context.Context, in HandlerInput) (string, error) {
		args, ok := in.Args.(RecallArgs)
		if !ok {
			// Registration bug (decode produced the wrong type) — surface
			// as a handler error, not in-character prose.
			return "", fmt.Errorf("recall: handler received unexpected args type %T", in.Args)
		}
		query := strings.TrimSpace(args.Query)
		if query == "" {
			return recallNoQueryText, nil
		}
		if utf8.RuneCountInString(query) > recallQueryMaxChars {
			query = string([]rune(query)[:recallQueryMaxChars])
		}
		namespace := strings.TrimSpace(in.LLMMemoryAgent)
		if namespace == "" || !in.MemoryHasPartition {
			// No memory partition (decorative / PC, or a shared-VA actor whose
			// name won't slugify). Guarding on MemoryHasPartition — not just a
			// non-empty namespace — is a PRIVACY control: a partition-less shared-VA
			// actor has an empty MemorySlugPrefix, so an unguarded search of its
			// pooled namespace (salem-vendor) would leak the OTHER NPCs' private
			// memories into this actor's transcript. The perception gate already
			// drops recall for these, but gates are advertising-only, so the handler
			// is the real control (matches memorize).
			log.Printf("handlers: recall for actor %q: no memory partition (ns %q, hasPartition %v)", in.ActorID, namespace, in.MemoryHasPartition)
			return recallFailedText, nil
		}
		hits, err := searcher.SearchMemory(ctx, namespace, query, in.MemorySlugPrefix, recallResultLimit)
		if err != nil {
			// Detailed error to the log; in-character string to the model
			// (don't leak API/transport detail into the LLM transcript).
			log.Printf("handlers: recall for actor %q (ns %q): search: %v", in.ActorID, namespace, err)
			return recallFailedText, nil
		}
		// time.Now() rather than a snapshot clock: recall runs live during the
		// tick, and a memory's age against the actual present is exactly the
		// point. formatRecallHits stays pure for tests.
		return formatRecallHits(hits, time.Now()), nil
	}
}
