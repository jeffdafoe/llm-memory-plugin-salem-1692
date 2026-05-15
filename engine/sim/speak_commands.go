package sim

import (
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Speak returns a Command that commits a speech utterance from speakerID
// against the world. Phase 3 PR A — the port of v1's `case "speak":`
// commit arm from agent_tick.go to the v2 in-memory substrate.
//
// Scope: the structural shape of v1 — empty-text reject, walk-in-flight
// reject, vocative stale-addressee reject (in-memory), event emit, paired
// RecordInteraction writes per huddle peer (KindNPCShared-gated inside
// RecordInteraction). Deferred to follow-up PRs (port of dependent
// subsystems): mentions validation (ZBBS-WORK-223/227/230, needs
// inventory), state-claim gate (ZBBS-HOME-270, needs pay_ledger), price-
// quoting (ZBBS-124, needs scene_quote).
//
// Pre-conditions the caller (the speak handlers.CommitFn) is responsible
// for, NOT re-checked here:
//
//   - text is trimmed (no leading/trailing whitespace)
//   - text is non-empty after trim
//   - len(text) <= 1000 bytes
//   - text contains no control characters outside \n \r \t
//
// World-state pre-conditions checked here:
//
//   - speakerID resolves to a real actor in w.Actors
//   - actor.MoveIntent == nil (not walk-in-flight)
//   - no vocative-position name in text matches a non-peer actor's
//     first-name (vocative stale-addressee gate; see findVocativeAbsentees)
//
// On success:
//
//   - emits Spoke{SpeakerID, HuddleID, RecipientIDs (sorted), Text, At}
//   - for each peer in RecipientIDs: RecordInteraction(speaker, peer,
//     InteractionSpoke) AND RecordInteraction(peer, speaker,
//     InteractionHeard); the KindNPCShared gate inside RecordInteraction
//     filters which writes actually persist
//   - no warrants stamped here — the Spoke event subscriber
//     (handlers/speech_reactor.go) mints NPCSpeechWarrantReason warrants
//     on each recipient
//
// No-huddle case: a speaker with no current huddle (CurrentHuddleID == "")
// still commits — the event emits with empty RecipientIDs, no
// RecordInteraction calls fire, the subscriber stamps no warrants. By
// design (per the PR A design walkthrough): "speaking to no one" is a
// legitimate narrative beat we don't punish.
func Speak(speakerID ActorID, text string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[speakerID]
			if !ok {
				return nil, fmt.Errorf("Speak: speaker %q not in world", speakerID)
			}
			if actor.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — finish your move before speaking. " +
						"Either say what you need to say BEFORE the move_to, " +
						"or wait until you arrive.",
				)
			}

			// Build the peer set from the speaker's current huddle. Sorted
			// by ActorID for deterministic event content + downstream
			// reactor warrant ordering. Speaker is excluded.
			huddleID := actor.CurrentHuddleID
			peerSet, peerIDs := buildHuddlePeerSet(w, speakerID, huddleID)

			// Vocative stale-addressee gate. Scans text for sentence-position
			// vocative names (e.g. "Ezekiel, ...") that match a non-peer
			// actor — i.e. the model addressed someone who is NOT currently
			// in the speaker's huddle. Returns absent-actor display names.
			if absent := findVocativeAbsentees(text, w, speakerID, peerSet); len(absent) > 0 {
				return nil, fmt.Errorf(
					"%s is no longer in your conversation — don't address them by name. "+
						"Re-check who is present before speaking.",
					strings.Join(absent, " and "),
				)
			}

			// Emit the Spoke event. World.emit stamps EventID + RootEventID
			// and dispatches subscribers synchronously inside the world
			// goroutine.
			w.emit(&Spoke{
				SpeakerID:    speakerID,
				HuddleID:     huddleID,
				RecipientIDs: peerIDs,
				Text:         text,
				At:           at,
			})

			// Per-peer bidirectional relationship writes. The KindNPCShared
			// gate inside RecordInteraction filters which writes persist —
			// stateful-VA NPCs get their per-peer continuity from their VA's
			// own memory system, so the engine-side gate silently no-ops
			// for them. v1's recordSpeechInteractions did the same shape.
			//
			// RecordInteraction errors only on caller bugs (empty IDs,
			// missing actor). All callers here come from w.actorsByHuddle —
			// the actor existed at the start of this Fn and the world
			// goroutine is single-threaded, so they exist still. A failure
			// would indicate a deeper invariant breach; log and continue
			// rather than aborting the speech that already emitted.
			for _, peerID := range peerIDs {
				if _, err := RecordInteraction(speakerID, peerID, InteractionSpoke, text, at).Fn(w); err != nil {
					log.Printf("sim.Speak: RecordInteraction speaker→peer %q→%q: %v", speakerID, peerID, err)
				}
				if _, err := RecordInteraction(peerID, speakerID, InteractionHeard, text, at).Fn(w); err != nil {
					log.Printf("sim.Speak: RecordInteraction peer→speaker %q→%q: %v", peerID, speakerID, err)
				}
			}
			return nil, nil
		},
	}
}

// buildHuddlePeerSet returns the set of peer ActorIDs in huddleID
// excluding speakerID, as both a set (for O(1) peer-lookup in the
// vocative gate) and a sorted slice (for deterministic event content).
// Empty huddleID returns (nil, nil) — the no-huddle case the Speak
// Command handles as commit-with-empty-recipients.
func buildHuddlePeerSet(w *World, speakerID ActorID, huddleID HuddleID) (map[ActorID]struct{}, []ActorID) {
	if huddleID == "" {
		return nil, nil
	}
	members, ok := w.actorsByHuddle[huddleID]
	if !ok || len(members) <= 1 {
		return nil, nil
	}
	peerSet := make(map[ActorID]struct{}, len(members)-1)
	peerIDs := make([]ActorID, 0, len(members)-1)
	for pid := range members {
		if pid == speakerID {
			continue
		}
		peerSet[pid] = struct{}{}
		peerIDs = append(peerIDs, pid)
	}
	sort.Slice(peerIDs, func(i, j int) bool { return peerIDs[i] < peerIDs[j] })
	return peerSet, peerIDs
}

// vocativeCandidateRegex matches a sentence-position vocative candidate:
// either at start of text, or after a sentence-terminator (. ! ?)
// followed by whitespace, then a Capitalized word (Unicode letters +
// apostrophes), then a vocative punctuation marker (, . ? !). The
// captured group is the candidate name itself.
//
// The lowercase-tail count is {0,30}: allows single-letter first names
// or initials ("Q, wait." matches as candidate "Q") in addition to the
// typical multi-letter case. Single-letter false positives are still
// filtered downstream because findVocativeAbsentees only flags
// candidates that match an actor's first name — "I, ..." or "A, ..."
// only reject if an actor named exactly "I" / "A" exists in the world.
//
// Conservative on purpose. Catches the dominant model pattern
// ("Ezekiel, you look hungry") while avoiding mid-sentence proper-noun
// references ("I told Ezekiel to be careful") that v1's gate likewise
// passed through. Known limitations the PR A scope accepts:
//
//   - Misses leading-expression vocatives: "Yes, Ezekiel, take a seat"
//     — Ezekiel is not at strict sentence start, so the candidate set is
//     {"Yes"} and the gate finds no actor named Yes.
//   - Misses multi-word names addressed by surname: "Soames, your bill"
//     — only first-name matching is wired; surname patterns would need
//     extending findVocativeAbsentees.
//   - Misses non-ASCII-but-non-capital starts (rare in the corpus).
//
// All three are model-mistake-shapes we can refine the parser to catch in
// a follow-up. They produce false-NEGATIVES (we let through some bad
// speech), which is preferable to false-positives (rejecting legitimate
// speech). The other gates and reactor admission policy don't depend on
// this catching everything.
var vocativeCandidateRegex = regexp.MustCompile(`(?:^|[.!?]\s+)([A-Z][\p{Ll}\p{M}']{0,30})[,.!?]`)

// findVocativeAbsentees returns the display names of actors who are
// addressed in vocative position in text but are NOT currently in the
// speaker's huddle peer set (peerSet). Self-address is filtered out.
// Empty list when no candidates match a known actor.
//
// Pattern: extract sentence-position candidate names via
// vocativeCandidateRegex; for each non-speaker non-peer actor in the
// world, check whether their first-name (the leading word of
// DisplayName, before any space) is one of the candidates. Match is
// case-sensitive — the regex already requires a capital initial.
//
// Cost: O(matches + N_actors) per speak — one regex pass plus a linear
// scan of w.Actors with a map probe per actor. Village scale (~100
// actors) is microseconds.
//
// Returned slice is sorted by DisplayName for deterministic test output
// and reproducible error messages across runs.
func findVocativeAbsentees(text string, w *World, speakerID ActorID, peerSet map[ActorID]struct{}) []string {
	if text == "" {
		return nil
	}
	matches := vocativeCandidateRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	candidates := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		candidates[m[1]] = struct{}{}
	}

	var absent []string
	for aid, a := range w.Actors {
		if aid == speakerID {
			continue
		}
		if _, isPeer := peerSet[aid]; isPeer {
			continue
		}
		if a.DisplayName == "" {
			continue
		}
		// strings.Fields handles any Unicode whitespace shape — spaces,
		// tabs, runs of multiple spaces — and returns []string{} for an
		// all-whitespace name (already excluded above by the empty check
		// on DisplayName, but defensive).
		fields := strings.Fields(a.DisplayName)
		if len(fields) == 0 {
			continue
		}
		firstName := fields[0]
		if _, ok := candidates[firstName]; ok {
			absent = append(absent, a.DisplayName)
		}
	}
	sort.Strings(absent)
	return absent
}
