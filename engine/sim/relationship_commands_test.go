package sim_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildRelationshipTestWorld stands up a world with two NPCs (one
// shared, one stateful) and runs it on a dedicated goroutine. Caller
// gets the world plus a cancel func that stops the loop and waits.
func buildRelationshipTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()

	now := time.Now().UTC()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:               "hannah",
			DisplayName:      "Hannah",
			Kind:             sim.KindNPCShared,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		"ezekiel": {
			ID:               "ezekiel",
			DisplayName:      "Ezekiel Crane",
			Kind:             sim.KindNPCStateful,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() {
		cancel()
		<-done
	}
}

func TestRecordInteraction_SharedNPCAppendsFact(t *testing.T) {
	w, stop := buildRelationshipTestWorld(t)
	defer stop()

	at := time.Now().UTC()
	if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, "Said he wanted ale.", at)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	snap := w.Published()
	hannah := snap.Actors["hannah"]
	if hannah == nil {
		t.Fatal("hannah missing from snapshot")
	}
	rel := hannah.Relationships["ezekiel"]
	if rel == nil {
		t.Fatal("Relationships[ezekiel] not created")
	}
	if rel.InteractionCount != 1 {
		t.Errorf("InteractionCount = %d, want 1", rel.InteractionCount)
	}
	if len(rel.SalientFacts) != 1 {
		t.Fatalf("SalientFacts len = %d, want 1", len(rel.SalientFacts))
	}
	if got := rel.SalientFacts[0]; got.Kind != sim.InteractionHeard || got.Text != "Said he wanted ale." {
		t.Errorf("SalientFact = %+v", got)
	}
	if rel.LastInteractionAt == nil || !rel.LastInteractionAt.Equal(at) {
		t.Errorf("LastInteractionAt = %v, want %v", rel.LastInteractionAt, at)
	}
	if !rel.CreatedAt.Equal(at) || !rel.UpdatedAt.Equal(at) {
		t.Errorf("CreatedAt=%v UpdatedAt=%v, want both %v", rel.CreatedAt, rel.UpdatedAt, at)
	}
}

func TestRecordInteraction_AppendsToExistingRelationship(t *testing.T) {
	w, stop := buildRelationshipTestWorld(t)
	defer stop()

	first := time.Now().UTC()
	second := first.Add(2 * time.Minute)

	if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, "first", first)); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionPaidBy, "second", second)); err != nil {
		t.Fatalf("second Send: %v", err)
	}

	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.InteractionCount != 2 {
		t.Errorf("InteractionCount = %d, want 2", rel.InteractionCount)
	}
	if len(rel.SalientFacts) != 2 {
		t.Fatalf("SalientFacts len = %d, want 2", len(rel.SalientFacts))
	}
	// Ordering: oldest first (append-only). The consolidator reads
	// oldest-first per v1's buildConsolidationPrompt.
	if rel.SalientFacts[0].Text != "first" || rel.SalientFacts[1].Text != "second" {
		t.Errorf("SalientFacts ordering wrong: %+v", rel.SalientFacts)
	}
	if !rel.LastInteractionAt.Equal(second) {
		t.Errorf("LastInteractionAt = %v, want %v", rel.LastInteractionAt, second)
	}
	if !rel.CreatedAt.Equal(first) || !rel.UpdatedAt.Equal(second) {
		t.Errorf("CreatedAt=%v UpdatedAt=%v, want %v / %v", rel.CreatedAt, rel.UpdatedAt, first, second)
	}
}

func TestRecordInteraction_StatefulNPCSilentlySkipped(t *testing.T) {
	w, stop := buildRelationshipTestWorld(t)
	defer stop()

	// Ezekiel is stateful (own VA) — recording on him should be a no-op
	// with no error.
	at := time.Now().UTC()
	if _, err := w.Send(sim.RecordInteraction("ezekiel", "hannah", sim.InteractionSpoke, "anything", at)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	snap := w.Published()
	ezekiel := snap.Actors["ezekiel"]
	if ezekiel.Relationships != nil && len(ezekiel.Relationships) > 0 {
		t.Errorf("stateful NPC accumulated Relationships: %+v", ezekiel.Relationships)
	}
}

func TestRecordInteraction_SelfInteractionNoOp(t *testing.T) {
	w, stop := buildRelationshipTestWorld(t)
	defer stop()

	if _, err := w.Send(sim.RecordInteraction("hannah", "hannah", sim.InteractionSpoke, "talking to myself", time.Now().UTC())); err != nil {
		t.Fatalf("Send: %v", err)
	}
	snap := w.Published()
	if rels := snap.Actors["hannah"].Relationships; len(rels) > 0 {
		t.Errorf("self-interaction created a Relationship: %+v", rels)
	}
}

func TestRecordInteraction_MissingActorReturnsError(t *testing.T) {
	w, stop := buildRelationshipTestWorld(t)
	defer stop()

	_, err := w.Send(sim.RecordInteraction("ghost", "hannah", sim.InteractionSpoke, "x", time.Now().UTC()))
	if err == nil {
		t.Error("expected error for missing actor, got nil")
	}
	_, err = w.Send(sim.RecordInteraction("hannah", "ghost", sim.InteractionSpoke, "x", time.Now().UTC()))
	if err == nil {
		t.Error("expected error for missing other actor, got nil")
	}
}

func TestRecordInteraction_TextTruncatedAtWrite(t *testing.T) {
	w, stop := buildRelationshipTestWorld(t)
	defer stop()

	long := ""
	for i := 0; i < 500; i++ {
		long += "x"
	}
	if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, long, time.Now().UTC())); err != nil {
		t.Fatalf("Send: %v", err)
	}
	snap := w.Published()
	fact := snap.Actors["hannah"].Relationships["ezekiel"].SalientFacts[0]
	if got := len([]rune(fact.Text)); got != sim.MaxSalientFactTextLen {
		t.Errorf("fact Text len = %d runes, want %d", got, sim.MaxSalientFactTextLen)
	}
}

// TestRecordInteraction_SubCapNoDrop confirms appends below the cap
// don't trigger FIFO eviction and DroppedFactCount stays zero.
func TestRecordInteraction_SubCapNoDrop(t *testing.T) {
	w, stop := buildRelationshipTestWorld(t)
	defer stop()

	at := time.Now().UTC()
	for i := 0; i < sim.MaxSalientFactsPerRelationship; i++ {
		if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, "x", at.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}
	rel := w.Published().Actors["hannah"].Relationships["ezekiel"]
	if len(rel.SalientFacts) != sim.MaxSalientFactsPerRelationship {
		t.Errorf("SalientFacts len = %d, want %d (full but not over)", len(rel.SalientFacts), sim.MaxSalientFactsPerRelationship)
	}
	if rel.DroppedFactCount != 0 {
		t.Errorf("DroppedFactCount = %d, want 0", rel.DroppedFactCount)
	}
	if rel.InteractionCount != sim.MaxSalientFactsPerRelationship {
		t.Errorf("InteractionCount = %d, want %d", rel.InteractionCount, sim.MaxSalientFactsPerRelationship)
	}
}

// TestRecordInteraction_OverCapFIFOEviction confirms appends past the
// cap drop oldest first, increment DroppedFactCount per drop, and
// preserve InteractionCount (lifetime counter, not slice length).
func TestRecordInteraction_OverCapFIFOEviction(t *testing.T) {
	w, stop := buildRelationshipTestWorld(t)
	defer stop()

	at := time.Now().UTC()
	overflow := 5
	total := sim.MaxSalientFactsPerRelationship + overflow

	for i := 0; i < total; i++ {
		text := fmt.Sprintf("fact-%03d", i)
		if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, text, at.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}

	rel := w.Published().Actors["hannah"].Relationships["ezekiel"]
	if len(rel.SalientFacts) != sim.MaxSalientFactsPerRelationship {
		t.Fatalf("SalientFacts len = %d, want capped at %d", len(rel.SalientFacts), sim.MaxSalientFactsPerRelationship)
	}
	if rel.DroppedFactCount != overflow {
		t.Errorf("DroppedFactCount = %d, want %d (one per overflow append)", rel.DroppedFactCount, overflow)
	}
	if rel.InteractionCount != total {
		t.Errorf("InteractionCount = %d, want %d (lifetime, not slice length)", rel.InteractionCount, total)
	}
	// FIFO: oldest survivor is fact-<overflow> (indices 0..overflow-1 dropped).
	wantOldest := fmt.Sprintf("fact-%03d", overflow)
	if rel.SalientFacts[0].Text != wantOldest {
		t.Errorf("oldest survivor Text = %q, want %q (indices 0..%d should have dropped)", rel.SalientFacts[0].Text, wantOldest, overflow-1)
	}
	// And the newest is the last we wrote.
	wantNewest := fmt.Sprintf("fact-%03d", total-1)
	if rel.SalientFacts[len(rel.SalientFacts)-1].Text != wantNewest {
		t.Errorf("newest Text = %q, want %q", rel.SalientFacts[len(rel.SalientFacts)-1].Text, wantNewest)
	}
}
