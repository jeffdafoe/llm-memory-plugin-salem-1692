package sim

import (
	"strings"
	"time"
	"unicode/utf8"
)

// ProvisionWorkerResult reports an actor's driver state after it was minted
// into a live Worker: the backing VA link, the reclassified Kind, and the full
// post-mutation attribute set (sorted).
type ProvisionWorkerResult struct {
	ID         ActorID
	LLMAgent   string
	Kind       ActorKind
	Attributes []string
}

// ProvisionWorker mints an actor into a live Worker in one atomic step:
//
//  1. link the backing VA (llm_memory_agent),
//  2. reclassify the actor's Kind from that link (ClassifyActorKind), and
//  3. grant the `worker` attribute.
//
// The point is to bring a sprite-only decorative ONLINE — ticking and taking
// solicit_work jobs — WITHOUT an engine restart. The live Kind flip is the
// load-time ClassifyActorKind replayed in memory, so the tick-eligibility
// gates (which read a.Kind) see the actor on its next backstop tick; the
// checkpoint then persists the agent link + attribute, so a later DB reload
// reclassifies it the same way.
//
// This is the operator/umbilical analog of the editor's SetActorAgentLink +
// AddActorAttribute, with the one piece those omit: the live Kind reclassify
// (they leave Kind to the next DB load, which is exactly why minting a worker
// used to need a coordinated stop → write → start). editableNPC rejects PCs;
// an empty agent is rejected here (a worker with no VA can never tick — the
// HTTP layer defaults an omitted agent to salem-vendor before calling).
//
// Idempotent: re-provisioning an actor already linked / already carrying the
// attribute is a no-op for that leg and emits no spurious frame. Emits
// NPCAgentChanged and NPCAttributesChanged (only on an actual change) so a live
// editor stays consistent — Kind itself is derived on load and has no frame.
//
// Errors: ErrInvalidAgentLink (empty / too long / control chars),
// ErrUnknownAttribute (the `worker` definition is unseeded), ErrActorNotFound
// (missing actor or a PC).
func ProvisionWorker(id ActorID, agent string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(agent)
			if trimmed == "" || utf8.RuneCountInString(trimmed) > MaxActorDisplayNameLen || containsControlChar(trimmed) {
				return nil, ErrInvalidAgentLink
			}
			if _, ok := w.AttributeDefinitions[AttrWorker]; !ok {
				return nil, ErrUnknownAttribute
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}

			// VA link + live Kind reclassify. The reclassify is the whole
			// reason this command exists over a bare SetActorAgentLink.
			if a.LLMAgent != trimmed {
				a.LLMAgent = trimmed
				w.emit(&NPCAgentChanged{ActorID: id, LLMAgent: trimmed, At: time.Now().UTC()})
			}
			a.Kind = ClassifyActorKind(a.LoginUsername, trimmed)

			// Grant the worker attribute (idempotent).
			if a.Attributes == nil {
				a.Attributes = map[string][]byte{}
			}
			if _, present := a.Attributes[AttrWorker]; !present {
				a.Attributes[AttrWorker] = []byte{}
				w.emit(&NPCAttributesChanged{ActorID: id, Attributes: sortedAttributeSlugs(a), At: time.Now().UTC()})
			}

			return ProvisionWorkerResult{
				ID:         id,
				LLMAgent:   a.LLMAgent,
				Kind:       a.Kind,
				Attributes: sortedAttributeSlugs(a),
			}, nil
		},
	}
}
