package cascade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// messenger_news.go — writes the messenger's news from outside (LLM-700).
//
// When a messenger spawns, an NPCCreated subscriber kicks one off-world call to the
// salem-news VA for an item of real 1692 news dated to the village's calendar day,
// then hands the reply to sim.SetMessengerNews, which cleans it, refuses anything
// naming a villager or touching witchcraft, and installs it as his Payload. He is
// still walking in from the road edge while the call runs, so the news is in place
// for his first turns; if it lands late, those turns simply have no news.
//
// One call per messenger visit — he is rare (one calling of five passers, one
// persona name), so a pre-authored daily batch would mostly go unused. A failed or
// refused call leaves him without news, which the preface handles by dropping the
// clause. A restart mid-call loses the call; the persisted Payload stays empty for
// that visit. Nothing retries — the next messenger visit makes a fresh call.

// messengerNewsLLMModel is the VA the news call routes to: Opus 5.5 with no system
// prompt and no dreams. Chosen over salem-generic (gpt-5.4-mini) after a live probe
// on 2026-10-02: the small model returned the same three famous 1692 headlines for
// any date, months stale and with a factual error; Opus returned events timely for
// the date and correct.
const messengerNewsLLMModel = "salem-news"

// messengerNewsLLMTimeout caps the off-world call so a wedged provider cannot hold
// the goroutine. Matches the noticeboard and atmosphere cascades.
const messengerNewsLLMTimeout = 90 * time.Second

// RegisterMessengerNews wires the NPCCreated subscriber that starts a messenger's
// news call. ctx bounds the off-world goroutines it starts (engine shutdown cancels
// them). Must run on the world goroutine — call before World.Run.
func RegisterMessengerNews(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterMessengerNews requires a non-nil world")
	}
	if client == nil {
		panic("cascade: RegisterMessengerNews requires a non-nil LLM client")
	}
	w.Subscribe(sim.SubscriberFunc(func(w *sim.World, evt sim.Event) {
		startMessengerNews(ctx, w, client, evt)
	}))
}

// startMessengerNews is the NPCCreated subscriber body: for a newly spawned
// messenger with no news yet, start the off-world news call. Reports whether a call
// was started. Runs on the world goroutine.
func startMessengerNews(ctx context.Context, w *sim.World, client llm.Client, evt sim.Event) bool {
	created, ok := evt.(*sim.NPCCreated)
	if !ok {
		return false
	}
	a := w.Actors[created.ActorID]
	if a == nil || !a.VisitorState.CarriesOutsideNews() || a.VisitorState.Payload != "" {
		return false
	}
	month, day := sim.MessengerNewsDate(w, created.At)
	go authorMessengerNews(ctx, w, client, created.ActorID, created.DisplayName, month, day)
	return true
}

// authorMessengerNews runs the news call off-world and sends the reply back to be
// installed. Logs the outcome either way.
func authorMessengerNews(ctx context.Context, w *sim.World, client llm.Client, id sim.ActorID, name string, month time.Month, day int) {
	callCtx, cancel := context.WithTimeout(ctx, messengerNewsLLMTimeout)
	defer cancel()

	resp, err := client.Complete(callCtx, llm.Request{
		Messages: buildMessengerNewsPrompt(month, day),
		Model:    messengerNewsLLMModel,
		// Fresh scene per call so the VA never sees a prior day's news as history.
		SceneID: llm.NewSceneID(),
	})
	if err != nil {
		if callCtx.Err() == nil {
			log.Printf("cascade/messenger_news: Complete for %s (%s): %v", name, id, err)
		}
		return
	}
	res, err := w.SendContext(callCtx, sim.SetMessengerNews(id, resp.Content))
	if err != nil {
		if callCtx.Err() == nil {
			log.Printf("cascade/messenger_news: install for %s (%s): %v", name, id, err)
		}
		return
	}
	outcome, _ := res.(sim.MessengerNewsOutcome)
	// The text is logged so a refusal can be read, truncated to the install cap so
	// a runaway reply cannot flood the log.
	text := sim.CleanMessengerNews(resp.Content)
	if r := []rune(text); len(r) > sim.MaxMessengerNewsLen {
		text = string(r[:sim.MaxMessengerNewsLen]) + "…"
	}
	log.Printf("cascade/messenger_news: %s (%s) news for %s %d: %s — %q", name, id, month, day, outcome, text)
}

// buildMessengerNewsPrompt is the full instruction for one item of news. The
// salem-news VA has no persona and no system prompt; the engine pushes everything.
// The reply must complete "Word reached you on the road that …", which is how the
// traveler preface frames it.
func buildMessengerNewsPrompt(month time.Month, day int) []llm.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "You supply the news a messenger carries into a small farming village in the Massachusetts Bay Colony. The date is %s %d, 1692.\n\n", month, day)
	b.WriteString("Give ONE item of real news from elsewhere in New England or from overseas, as a traveler would have heard it on the road around that date: Boston and the ports, the Maine and New Hampshire frontier, the Governor and the General Court, ships from England and the West Indies, the war with France, the harvest and the weather. Use a real event of 1692 whose news would plausibly have reached Massachusetts by that date; prefer recent events over old ones.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Do NOT mention witchcraft, witches, the trials, accusations, the special court, jails or hangings, and do not mention Salem in any form.\n")
	b.WriteString("- Do NOT name anyone who lives in Salem Village.\n")
	b.WriteString("- Write it as the end of this sentence: \"Word reached you on the road that ...\". Give only the part after \"that\", in the past tense, under 35 words, in plain words a farmer would use.\n")
	b.WriteString("- Output that one clause and nothing else: no quotes, no preface, no final period.")
	return []llm.Message{{Role: llm.RoleUser, Content: b.String()}}
}
