package sim

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrActorNotProvisionable is returned when the target actor exists and is not a
// PC but is already a live NPC (KindNPCStateful / KindNPCShared). ProvisionWorker
// mints only a never-ticked KindDecorative; relinking a ticking actor's VA could
// race an in-flight reaction (see ProvisionWorker).
var ErrActorNotProvisionable = errors.New("actor is already a live NPC, not a provisionable decorative")

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
// The command provisions ONLY a sprite-only KindDecorative actor — the case
// that genuinely needs the live Kind flip. A decorative has never ticked, so it
// carries no warrant, no in-flight LLM tick, and no reactor state a relink could
// race; the flip is therefore provably safe. An already-live NPC
// (KindNPCStateful / KindNPCShared) is refused with ErrActorNotProvisionable:
// re-linking a ticking actor's VA could let a reaction dispatched outside this
// command loop apply against the newly linked actor, and merely granting the
// worker attribute to an actor that already deliberates is the editor's
// AddActorAttribute job, not this route's. A PC or missing id is ErrActorNotFound.
//
// Emits NPCAgentChanged + NPCAttributesChanged (the same frames the editor's
// SetActorAgentLink / AddActorAttribute emit) so a live editor stays consistent;
// Kind itself is derived on load and has no frame. The per-leg change guards are
// defensive — a fresh decorative trips both, but a decorative pre-granted the
// worker attribute via the editor won't re-emit the attribute frame.
//
// Errors: ErrInvalidAgentLink (empty / too long / control chars),
// ErrUnknownAttribute (the `worker` definition is unseeded), ErrActorNotFound
// (missing actor or a PC), ErrActorNotProvisionable (already a live NPC).
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
			// Mint only a never-ticked decorative — see the doc comment: an
			// already-live NPC may have a warrant or an in-flight LLM tick
			// (dispatched outside this command loop), and relinking its VA
			// could let that stale reaction apply against the new link.
			if a.Kind != KindDecorative {
				return nil, ErrActorNotProvisionable
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
