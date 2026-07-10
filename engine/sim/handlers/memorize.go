package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// memorize.go — the memory-writing observation tool (LLM-356), companion to
// recall. Where recall reads an NPC's memory, memorize lets the NPC deliberately
// commit something to it — a person's name, a place, a promise — so it survives
// past the current conversation and can be recalled later.
//
// It is an OBSERVATION (ClassObservation): non-terminal, no world mutation. Its
// only side effect is a write against llm-memory-api (save one note, then prune
// the NPC's memory back to a cap). It runs OFF the world goroutine, so "who am
// I" (namespace, partition prefix) and "what day is it" (date stamp) are
// threaded in via HandlerInput, exactly as recall's namespace is.
//
// Storage: one note per memory, at
//
//	<partition><memory/><date>-<topic-slug>
//
// e.g. "anne-walker/memory/2026-07-10-the-blacksmith-s-name" for a shared-VA
// NPC, or "memory/2026-07-10-…" for a dedicated-VA NPC (its whole namespace is
// its own). The (date, topic) slug is deterministic, so re-memorizing the same
// topic the same day upserts the SAME note — a revision, not a duplicate. The
// note is one markdown heading + body, which chunkByHeading indexes as a single
// chunk, so recall returns the whole memory (see recall.formatRecallHits).
//
// Why the documents route, not memory/ingest: a note saved through
// documents/save is auto-indexed for search AND browsable in the admin UI; a
// raw ingest is searchable chunks with no note.
//
// Failure handling mirrors recall: every runtime failure (empty input, no
// namespace, save error) returns an in-character string as the tool RESULT, not
// a handler error, so the model reads graceful prose rather than a transport
// leak. The only error return is an args-type mismatch (a registration bug).

const (
	// memorizeTopicMaxChars / memorizeBodyMaxChars cap the two fields before
	// storage — cost guards, trimmed by rune so a multibyte rune is never split.
	memorizeTopicMaxChars = 120
	memorizeBodyMaxChars  = 1000

	// memoryNoteCap is the most memories one NPC keeps. Recall surfaces up to
	// recallResultLimit (5), so the cap leaves the search a real field to choose
	// from while bounding the admin tree and the per-save prune cost. Anchored
	// here as a named constant so it tunes in one place once live journals show
	// their real size.
	memoryNoteCap = 40

	// memorySubfolder is the slug segment memories live under, within the actor's
	// partition. Keeping memories foldered means the prune step (which lists this
	// prefix) never touches a dedicated-VA NPC's dreams or impressions.
	memorySubfolder = "memory/"

	// memoryCognitiveType selects the note's search-decay half-life. "episodic"
	// (a season, ~90 days) means a memory fades unless recall reinforces it — a
	// recall stamps last_accessed, which resets the decay clock (LLM-355). The
	// blacksmith's name an NPC keeps asking for stays sharp; a one-off fades.
	memoryCognitiveType = "episodic"
)

// memorize in-character result strings.
const (
	memorizeNoInputText = "You tried to fix something in your memory but couldn't form the thought."
	memorizeFailedText  = "You tried to hold onto it, but the thought slipped away."
	memorizeNoAgentText = "You have nowhere to keep such a thing in mind."
	memorizeSavedFormat = "You fix it in your memory: %q. You'll be able to recall it later."
)

// MemorizeArgs is the decoded shape of the memorize tool's arguments: a short
// topic (what the memory is about — becomes the recall label and the slug) and
// the body (what to remember, in the NPC's own words).
type MemorizeArgs struct {
	Topic string `json:"topic"`
	Body  string `json:"body"`
}

// memorizeSchema is the JSON Schema shipped to the LLM provider. Both fields
// required, no additional properties.
var memorizeSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "topic": {
            "type": "string",
            "minLength": 1,
            "description": "A short label for what this memory is about — a name, a place, a promise."
        },
        "body": {
            "type": "string",
            "minLength": 1,
            "description": "What you want to remember, in your own words."
        }
    },
    "required": ["topic", "body"],
    "additionalProperties": false
}`)

// memorizeDescription is advertised to the model. Phrased to pair with recall's
// "in your own words" so the two read as one mechanism (write / read).
const memorizeDescription = "Store something in your memory so you can recall it later. Use this when you learn something worth keeping — a person's name, where something is, a promise you made. Write it in your own words."

// DecodeMemorizeArgs parses the raw tool-call arguments into a MemorizeArgs.
// Same strict shape as DecodeRecallArgs: object only, no unknown fields, no
// trailing data. Empty/whitespace fields are NOT rejected here — the handler
// turns them into the in-character memorizeNoInputText result.
func DecodeMemorizeArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("memorize: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args MemorizeArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("memorize: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("memorize: trailing data after JSON object")
		}
		return nil, fmt.Errorf("memorize: malformed trailing data: %w", err)
	}
	return args, nil
}

// memorizeSlug builds the note slug for a memory from the actor's partition
// prefix, the date stamp, and the topic. Returns "" when it can't form a valid
// slug — an empty date (would yield a malformed "…/memory/-topic") or a topic
// that slugifies to nothing (all punctuation). Encoding both invariants here,
// not only in the caller, keeps the helper from ever producing a bad slug.
func memorizeSlug(partitionPrefix, dateStamp, topic string) string {
	dateStamp = strings.TrimSpace(dateStamp)
	if dateStamp == "" {
		return ""
	}
	topicSlug := sim.Slugify(topic)
	if topicSlug == "" {
		return ""
	}
	return partitionPrefix + memorySubfolder + dateStamp + "-" + topicSlug
}

// makeMemorizeHandler builds the memorize ObservationFn closed over writer. It
// runs OFF the world goroutine; writer honors ctx. See the file-level doc for
// the in-character failure contract.
func makeMemorizeHandler(writer llm.MemoryWriter) ObservationFn {
	return func(ctx context.Context, in HandlerInput) (string, error) {
		args, ok := in.Args.(MemorizeArgs)
		if !ok {
			return "", fmt.Errorf("memorize: handler received unexpected args type %T", in.Args)
		}
		topic := strings.TrimSpace(args.Topic)
		body := strings.TrimSpace(args.Body)
		if topic == "" || body == "" {
			return memorizeNoInputText, nil
		}
		if utf8.RuneCountInString(topic) > memorizeTopicMaxChars {
			topic = strings.TrimSpace(string([]rune(topic)[:memorizeTopicMaxChars]))
		}
		if utf8.RuneCountInString(body) > memorizeBodyMaxChars {
			body = string([]rune(body)[:memorizeBodyMaxChars])
		}

		namespace := strings.TrimSpace(in.LLMMemoryAgent)
		if namespace == "" || !in.MemoryHasPartition {
			// No memory partition (decorative / PC, or a shared-VA actor whose
			// name won't slugify). The perception gate already drops memorize for
			// these, but gates are advertising-only — a stray dispatch must NOT
			// fall through to a write, or a partition-less shared-VA actor would
			// pool its memory into the shared namespace root. Refuse in-character.
			log.Printf("handlers: memorize for actor %q: no memory partition (ns %q, hasPartition %v)", in.ActorID, namespace, in.MemoryHasPartition)
			return memorizeNoAgentText, nil
		}
		if in.MemoryDateStamp == "" {
			// Only a clockless (hand-built) snapshot reaches here; a live tick
			// always carries the village date. Without it the slug can't be dated.
			log.Printf("handlers: memorize for actor %q: no date stamp", in.ActorID)
			return memorizeFailedText, nil
		}

		slug := memorizeSlug(in.MemorySlugPrefix, in.MemoryDateStamp, topic)
		if slug == "" {
			return memorizeNoInputText, nil
		}
		// One heading + body → one chunk (chunkByHeading), so recall returns the
		// whole memory and labels it with the topic.
		content := "## " + topic + "\n\n" + body
		if err := writer.SaveNote(ctx, namespace, slug, topic, content, memoryCognitiveType); err != nil {
			log.Printf("handlers: memorize for actor %q (ns %q slug %q): save: %v", in.ActorID, namespace, slug, err)
			return memorizeFailedText, nil
		}

		// Prune to the cap. Best-effort: the memory is already saved, so a prune
		// failure must not fail the tool — log and return success.
		pruneMemories(ctx, writer, namespace, in.MemorySlugPrefix, in.ActorID)

		return fmt.Sprintf(memorizeSavedFormat, topic), nil
	}
}

// pruneMemories keeps an NPC's memory notes at or below memoryNoteCap by
// soft-deleting the stalest. It lists only the actor's "<partition>memory/"
// prefix (never its dreams/impressions), sorts by NoteMeta.Freshness ascending
// — the same recency basis the search decay uses, so a memory kept alive by
// recall resists eviction — and deletes the oldest excess. Every step is
// best-effort and logs on failure; memorize's contract is that the save
// succeeded, not that the prune did.
func pruneMemories(ctx context.Context, writer llm.MemoryWriter, namespace, partitionPrefix string, actorID sim.ActorID) {
	notes, err := writer.ListNotes(ctx, namespace, partitionPrefix+memorySubfolder)
	if err != nil {
		log.Printf("handlers: memorize prune for actor %q (ns %q): list: %v", actorID, namespace, err)
		return
	}
	if len(notes) <= memoryNoteCap {
		return
	}
	sort.Slice(notes, func(i, j int) bool {
		return notes[i].Freshness().Before(notes[j].Freshness())
	})
	for _, n := range notes[:len(notes)-memoryNoteCap] {
		if err := writer.DeleteNote(ctx, namespace, n.Slug); err != nil {
			log.Printf("handlers: memorize prune for actor %q (ns %q slug %q): delete: %v", actorID, namespace, n.Slug, err)
		}
	}
}
