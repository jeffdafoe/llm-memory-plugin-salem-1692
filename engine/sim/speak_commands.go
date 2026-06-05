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

// SpeakTo returns a Command that commits a speech utterance from speakerID
// against the world, directed at the addressee named by `to` (empty = no
// explicit addressee; the to-less Speak wrapper passes ""). Phase 3 PR A —
// the port of v1's `case "speak":` commit arm from agent_tick.go to the v2
// in-memory substrate; the `to` addressee channel is ZBBS-WORK-369.
//
// `to` is the speaker's declared addressee — an actor name (full display
// name or first name), matched case-insensitively against present huddle
// peers. It seeds the WORK-369 addressee-resolution chain `to` →
// sentence-position vocative in text → whole-huddle; the resolved peer (or
// empty for whole-huddle) rides on Spoke.AddressedID for the turn-state
// core (WORK-370) to consume. A `to` that names no present peer falls
// through to the vocative / whole-huddle steps rather than rejecting.
//
// Scope: the structural shape of v1 — empty-text reject, walk-in-flight
// reject, vocative stale-addressee reject (in-memory), the WORK-323 prose-
// validation gates (item-presence + transfer-verb + state-claim, see
// validateSpeechClaims in speak_validation.go), event emit, paired
// RecordInteraction writes per huddle peer (KindNPCShared-gated inside
// RecordInteraction). Still deferred: the structured `mentions[]` schema field
// (ZBBS-WORK-223 — only needed for the PC sellable dropdown + price-quoting; the
// WORK-323 item gates use an implicit text scan instead, so no prompt cache-bust)
// and price-quoting (ZBBS-124, needs scene_quote).
//
// Pre-conditions the caller (the speak handlers.CommitFn) is responsible
// for, NOT re-checked here:
//
//   - text is trimmed (no leading/trailing whitespace)
//   - text is non-empty after trim
//   - len(text) <= 1000 bytes
//   - text contains no control characters outside \n \r \t
//
// hasNewNews is the turn-state gate's new-news signal (ZBBS-WORK-370): true
// when the reactor tick driving this speak consumed a fresh-stimulus warrant
// (Force, or any high-information kind). It exempts a legitimate event-driven
// follow-up from the idle-re-pitch backstop below. The harness computes it per
// tick (batchHasNewNews); the to-less Speak wrapper passes true so the PC path
// and every internal caller ride the exemption unconditionally.
//
// World-state pre-conditions checked here:
//
//   - speakerID resolves to a real actor in w.Actors
//   - actor.MoveIntent == nil (not walk-in-flight)
//   - no vocative-position name in text matches a non-peer actor's
//     first-name (vocative stale-addressee gate; see findVocativeAbsentees)
//   - (NPC speakers only) the WORK-323 prose gates pass: no item mentioned that
//     the speaker doesn't hold, no transfer-verb-narrated handover, no unbacked
//     booking/payment state-claim (see validateSpeechClaims)
//   - (NPC speakers only) the turn-state backstop passes: not an idle re-pitch
//     of a peer already addressed and not yet replied (see below)
//
// On success:
//
//   - emits Spoke{SpeakerID, HuddleID, RecipientIDs (sorted), AddressedID, Text, At}
//   - for each peer in RecipientIDs: RecordInteraction(speaker, peer,
//     InteractionSpoke) AND RecordInteraction(peer, speaker,
//     InteractionHeard); the KindNPCShared gate inside RecordInteraction
//     filters which writes actually persist
//   - no warrants stamped here — the Spoke event subscriber
//     (handlers/speech_reactor.go) mints NPCSpeechWarrantReason warrants
//     on each recipient
//
// No-audience case (ZBBS-HOME-402): an NPC speaker with no huddle peers is
// REJECTED — speaking to no one reaches no one (the event would emit with empty
// RecipientIDs, fire no RecordInteraction, stamp no warrants), so committing it
// only littered the action log and, at scale, drove the empty-room re-pitch
// storm the exact-dedup can't catch (live: Josiah greeting his own empty store
// ~13x in 35s). A PC speaker with no huddle STILL commits (empty RecipientIDs,
// no writes) — the kind exemption mirrors the walk / vocative / prose /
// turn-state gates: a human may speak to anyone, anytime. This supersedes the
// old "speaking to no one is a legitimate narrative beat" allowance, which only
// ever produced inert void lines.
func SpeakTo(speakerID ActorID, text, to string, hasNewNews bool, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[speakerID]
			if !ok {
				return nil, fmt.Errorf("Speak: speaker %q not in world", speakerID)
			}
			// The walk-in-flight gate disciplines an NPC LLM that would "speak"
			// mid-move; it does not apply to a human player, who may legitimately
			// type while their avatar walks (ZBBS-WORK-360). Same PC exemption the
			// prose gates below already carry — every PC-speak rejection is a
			// client-visible malfunction, so PCs are held to none of the
			// NPC-discipline gates.
			if actor.Kind != KindPC && actor.MoveIntent != nil {
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

			// ZBBS-HOME-402: no-audience gate. An NPC with no huddle peers is
			// speaking to the void — the Spoke would reach no one, so reject it
			// model-facing instead, steering the model to call done() or move to
			// someone. PCs are exempt (a human may speak to no one), same posture
			// as the walk / vocative / prose / turn-state gates below. The speak
			// handler's EnsureColocatedHuddle already formed an indoor huddle with
			// any co-located actors before this command ran, so an empty peer set
			// here means genuinely no one is present to hear.
			if actor.Kind != KindPC && len(peerIDs) == 0 {
				return nil, errors.New(
					"there is no one here to hear you — call done(), or move_to someone before speaking.")
			}

			// Vocative stale-addressee gate. Scans text for sentence-position
			// vocative names (e.g. "Ezekiel, ...") that match a non-peer
			// actor — i.e. the model addressed someone who is NOT currently
			// in the speaker's huddle. Returns absent-actor display names.
			// Vocative stale-addressee gate — also NPC-LLM discipline (stop a
			// model addressing someone who isn't present). A human player may
			// legitimately call out to someone they can see even if that actor
			// isn't a huddle peer (e.g. an asleep keeper), so PCs are exempt
			// (ZBBS-WORK-360); the utterance simply reaches whoever is actually
			// in the huddle, possibly no one.
			if actor.Kind != KindPC {
				if absent := findVocativeAbsentees(text, w, speakerID, peerSet); len(absent) > 0 {
					return nil, fmt.Errorf(
						"%s is no longer in your conversation — don't address them by name. "+
							"Re-check who is present before speaking.",
						strings.Join(absent, " and "),
					)
				}
			}

			// Prose-validation gates (ZBBS-WORK-323): item-presence, transfer-verb,
			// and transactional state-claim — the defense against speaking a
			// service/transaction into apparent existence that no tool performed.
			// PC speech is exempt (players may roleplay assertions); only NPC LLM
			// hallucination is gated. See speak_validation.go.
			if actor.Kind != KindPC {
				if reject := validateSpeechClaims(w, actor, text, at); reject != "" {
					return nil, errors.New(reject)
				}
			}

			// Resolve the single addressee (ZBBS-WORK-369): explicit `to` →
			// vocative in text → whole-huddle (empty). Computed here, after the
			// gates, against the committed peer set; carried on the event for
			// the turn-state core (WORK-370). No-huddle / no-peer speaks resolve
			// to empty (whole-huddle).
			addressedID := resolveAddressee(to, text, w, peerIDs)

			// ZBBS-WORK-370 (2/2) turn-state backstop. Suppress an NPC's idle
			// re-pitch: a speaker with a still-live, unanswered outgoing edge to
			// the addressee it is initiating to — on a tick with no fresh event
			// behind it — is talking over a peer who hasn't answered yet (the
			// "welcome, then two more pitches" cadence the live trace caught).
			// Reject it model-facing, like the vocative gate. Carve-outs:
			//   - PC speakers are never gated (they reach Speak with
			//     hasNewNews=true anyway; a human may say whatever, whenever —
			//     same posture as the walk / vocative / prose gates above).
			//   - hasNewNews exempts a tick driven by a real event (paid, order
			//     ready, arrival, a distinct utterance heard) so a legitimate
			//     follow-up ("here is your bread") still commits.
			//   - a whole-huddle utterance (addressedID == "") opens no edge, so
			//     it is never gated.
			//   - the outgoing edge must still be LIVE within the addressee-kind
			//     window; once it lapses the conversation may re-open (anti-lockup).
			//
			// No explicit "responding" carve-out is needed. The gate fires only
			// when the speaker holds a LIVE outgoing edge to the addressee, and the
			// per-pair edge invariant makes the two directions mutually exclusive:
			// any speak clears every INCOMING edge against the speaker
			// (satisfyAwaitedReplyFrom below), so if the addressee were awaiting the
			// speaker — i.e. the speaker were RESPONDING to it — the speaker's
			// outgoing edge to that addressee would already have been cleared and
			// this check could not fire. A genuine reply is therefore always
			// allowed implicitly. (An earlier draft exempted "any peer awaits me",
			// which wrongly let an idle re-pitch of X through whenever an unrelated
			// peer Y was owed a reply — code_review caught it.)
			//
			// Runs BEFORE emit + before the edge mutations below, so it reads the
			// pre-speak turn-state.
			if actor.Kind != KindPC && !hasNewNews && addressedID != "" {
				if addressee := w.Actors[addressedID]; addressee != nil {
					window := w.awaitReplyWindow(addressee.Kind)
					if actor.hasLiveAwaitEdge(addressedID, at, window) {
						name := addressee.DisplayName
						if name == "" {
							name = "them"
						}
						return nil, fmt.Errorf(
							"you already spoke to %s and are awaiting their reply — do not "+
								"repeat yourself or address them again; attend to your own work, "+
								"or wait until they answer.",
							name,
						)
					}
				}
			}

			// Emit the Spoke event. World.emit stamps EventID + RootEventID
			// and dispatches subscribers synchronously inside the world
			// goroutine.
			w.emit(&Spoke{
				SpeakerID:    speakerID,
				HuddleID:     huddleID,
				RecipientIDs: peerIDs,
				AddressedID:  addressedID,
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

			// ZBBS-HOME-331: speaking INTO the huddle is the productive signal the
			// heard-speech circuit breaker keys on — clear this actor's "ignoring
			// a speaker" suppression against the peers it just addressed, so a
			// stalled exchange resumes the moment one side says something.
			// Per-recipient (not a blanket clear) so a solo / no-one utterance
			// (empty peerIDs) doesn't wrongly reopen an unrelated suppressed pair.
			// Only successful speaks reach here; a rejected speak returns above,
			// before emit, and does not reset.
			actor.resetHeardSpeechMissesAgainst(peerIDs)

			// ZBBS-WORK-370 turn-state edges. The speaker now awaits a reply
			// from its resolved addressee (no-op when it addressed the whole
			// huddle, addressedID == ""), AND its utterance satisfies any peer
			// that was awaiting a reply from it — any speak by the awaited party
			// IS the reply that takes the turn. Mirror of
			// resetHeardSpeechMissesAgainst one layer up; the gate that reads
			// these edges lands in the follow-up slice.
			actor.awaitReply(addressedID, at)
			for _, peerID := range peerIDs {
				if peer, ok := w.Actors[peerID]; ok {
					peer.satisfyAwaitedReplyFrom(speakerID)
				}
			}
			return nil, nil
		},
	}
}

// Speak is the addressee-less form of SpeakTo: it commits a speech utterance
// with no explicit `to`, so the addressee resolves from a sentence-position
// vocative in the text or, failing that, the whole huddle. Every pre-WORK-369
// caller (the PC speak path, tests) reaches speech through this wrapper
// unchanged; only the NPC speak tool passes an explicit `to` via SpeakTo.
func Speak(speakerID ActorID, text string, at time.Time) Command {
	// hasNewNews=true: the to-less wrapper is the PC speak path plus every
	// pre-WORK-369 caller. None should ever be turn-gated — PCs are exempt via
	// the Kind check anyway, and internal/test callers declare "fresh news"
	// unconditionally so the backstop only ever fires for an NPC speak tool call
	// that threads the harness-computed flag through SpeakTo.
	return SpeakTo(speakerID, text, "", true, at)
}

// resolveAddressee picks the single huddle peer a speak is directed at,
// following the WORK-369 chain: explicit `to` (the NPC model's declared
// addressee; empty for the PC / to-less path) → sentence-position vocative
// in text → no one specific (whole-huddle). Returns "" for the whole-huddle
// case. peerIDs is the sorted peer slice, so a result is deterministic when
// more than one peer could match.
//
// Only a PRESENT peer resolves: a `to` (or vocative) naming someone not in
// the huddle falls through rather than resolving to an absentee. The
// separate vocative-absentee gate already rejects an NPC addressing an
// absent actor by name in the text; a `to` naming an absentee is simply
// ignored here, leaving the utterance addressed to the whole huddle.
func resolveAddressee(to, text string, w *World, peerIDs []ActorID) ActorID {
	if len(peerIDs) == 0 {
		return ""
	}
	// 1. Explicit `to` — match against each peer's full display name or
	//    leading (first) name, case-insensitively. The model addresses by
	//    the name it sees in perception, which may be either form.
	if to = strings.TrimSpace(to); to != "" {
		want := strings.ToLower(to)
		for _, pid := range peerIDs {
			a := w.Actors[pid]
			if a == nil || a.DisplayName == "" {
				continue
			}
			if strings.ToLower(a.DisplayName) == want {
				return pid
			}
			if fields := strings.Fields(a.DisplayName); len(fields) > 0 && strings.ToLower(fields[0]) == want {
				return pid
			}
		}
	}
	// 2. Sentence-position vocative in the text — the present-peer
	//    counterpart of the absentee gate (same candidate extraction,
	//    opposite membership test).
	if id := vocativePeer(text, w, peerIDs); id != "" {
		return id
	}
	// 3. No one specific → whole huddle.
	return ""
}

// vocativePeer returns the first huddle peer (in sorted peerIDs order)
// whose first name appears in sentence-position vocative in text, or ""
// if none. Reuses vocativeCandidateRegex; the first-name match is
// case-sensitive (the regex already requires a capital initial), matching
// findVocativeAbsentees.
func vocativePeer(text string, w *World, peerIDs []ActorID) ActorID {
	if text == "" {
		return ""
	}
	matches := vocativeCandidateRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return ""
	}
	candidates := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		candidates[m[1]] = struct{}{}
	}
	for _, pid := range peerIDs {
		a := w.Actors[pid]
		if a == nil || a.DisplayName == "" {
			continue
		}
		fields := strings.Fields(a.DisplayName)
		if len(fields) == 0 {
			continue
		}
		if _, ok := candidates[fields[0]]; ok {
			return pid
		}
	}
	return ""
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
