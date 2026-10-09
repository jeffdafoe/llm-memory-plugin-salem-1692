package sim_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pay_commands_test.go — sim-level coverage of the Pay Command's
// world-state validation, coin transfer, event emission, and bidirectional
// RecordInteraction matrix.
//
// Handler-level static validation (decode + bounds) lives in
// handlers/pay_test.go. Subscriber tests (warrant minting) live in
// handlers/pay_reactor_test.go.

// payActorSpec — mirrors actorSpec from speak_commands_test.go but adds
// Coins seeding for pay's balance gate.
type payActorSpec struct {
	id           sim.ActorID
	displayName  string
	kind         sim.ActorKind
	huddleID     sim.HuddleID
	coins        int
	moveInFlight bool
}

func buildPayTestWorld(t *testing.T, specs ...payActorSpec) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	seed := make(map[sim.ActorID]*sim.Actor, len(specs))
	for _, s := range specs {
		a := &sim.Actor{
			ID:              s.id,
			DisplayName:     s.displayName,
			Kind:            s.kind,
			State:           sim.StateIdle,
			CurrentHuddleID: s.huddleID,
			Coins:           s.coins,
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		}
		if s.moveInFlight {
			a.MoveIntent = &sim.MoveIntent{AttemptID: sim.MovementAttemptID(1)}
		}
		seed[s.id] = a
	}
	handles.Actors.Seed(seed)

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
	return w, func() { cancel(); <-done }
}

// playerPay runs sim.Pay with the payer made a player first. Only a PC may
// hand over bare coin (LLM-725), and sim.Pay's one caller is the pc/give
// route; these fixtures predate that and seed NPC payers, so each pay test
// models the real caller rather than every fixture growing a PC.
func playerPay(buyerID sim.ActorID, recipient string, amount int, forText string, at time.Time) sim.Command {
	return sim.Command{Fn: func(w *sim.World) (any, error) {
		if a := w.Actors[buyerID]; a != nil {
			a.Kind = sim.KindPC
		}
		return sim.Pay(buyerID, recipient, amount, forText, at).Fn(w)
	}}
}

// TestPay_NPCPayerRefused — LLM-725: an NPC cannot hand over bare coin, whoever
// the caller is. No coin moves and no Paid event fires.
func TestPay_NPCPayerRefused(t *testing.T) {
	for _, kind := range []sim.ActorKind{sim.KindNPCShared, sim.KindNPCStateful, sim.KindDecorative} {
		w, stop := buildPayTestWorld(t,
			payActorSpec{id: "hannah", displayName: "Hannah", kind: kind, huddleID: "h1", coins: 10},
			payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
		)
		captured := capturePaid(t, w)
		_, err := w.Send(sim.Pay("hannah", "Ezekiel Crane", 3, "for the ale", time.Now().UTC()))
		if err == nil || !strings.Contains(err.Error(), "only a player can give coins") {
			t.Errorf("kind %v: want the player-only refusal, got %v", kind, err)
		}
		snap := w.Published()
		if snap.Actors["hannah"].Coins != 10 || snap.Actors["ezekiel"].Coins != 0 {
			t.Errorf("kind %v: coins moved (hannah %d, ezekiel %d)", kind, snap.Actors["hannah"].Coins, snap.Actors["ezekiel"].Coins)
		}
		if len(*captured) != 0 {
			t.Errorf("kind %v: a Paid event fired on a refused pay", kind)
		}
		stop()
	}
}

// capturePaid registers a subscriber that records every emitted Paid event
// into the returned slice. Same pattern as captureSpoke — Subscribe routes
// through w.Send so it runs on the world goroutine.
func capturePaid(t *testing.T, w *sim.World) *[]sim.Paid {
	t.Helper()
	var out []sim.Paid
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if p, ok := evt.(*sim.Paid); ok {
				out = append(out, *p)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("capturePaid subscribe: %v", err)
	}
	return &out
}

// --- TestPay_HappyPath: same-huddle pair, sufficient coins → transfer,
// Paid event emits, 2 RecordInteraction writes (both directions).
func TestPay_HappyPath(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1", coins: 2},
	)
	defer stop()

	captured := capturePaid(t, w)
	at := time.Now().UTC()
	if _, err := w.Send(playerPay("hannah", "Ezekiel Crane", 3, "ale", at)); err != nil {
		t.Fatalf("Pay: %v", err)
	}

	// Event emitted.
	if len(*captured) != 1 {
		t.Fatalf("Paid events = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if got.BuyerID != "hannah" || got.SellerID != "ezekiel" {
		t.Errorf("Paid actors = %q→%q, want hannah→ezekiel", got.BuyerID, got.SellerID)
	}
	if got.Amount != 3 {
		t.Errorf("Paid.Amount = %d, want 3", got.Amount)
	}
	if got.ForText != "ale" {
		t.Errorf("Paid.ForText = %q, want %q", got.ForText, "ale")
	}
	if !got.At.Equal(at) {
		t.Errorf("Paid.At = %v, want %v", got.At, at)
	}

	// Coin balances updated.
	snap := w.Published()
	if got, want := snap.Actors["hannah"].Coins, 7; got != want {
		t.Errorf("hannah.Coins = %d, want %d", got, want)
	}
	if got, want := snap.Actors["ezekiel"].Coins, 5; got != want {
		t.Errorf("ezekiel.Coins = %d, want %d", got, want)
	}

	// Relationship facts: the payer is a player, and RecordInteraction keeps no
	// relationship row for a PC; the NPC recipient records being paid.
	if rel := snap.Actors["hannah"].Relationships["ezekiel"]; rel != nil {
		t.Errorf("player payer got a relationship row: %+v", rel)
	}
	ezekiel := snap.Actors["ezekiel"]
	if rel := ezekiel.Relationships["hannah"]; rel == nil {
		t.Fatal("ezekiel.Relationships[hannah] missing")
	} else if len(rel.SalientFacts) != 1 {
		t.Errorf("ezekiel→hannah facts = %d, want 1", len(rel.SalientFacts))
	} else {
		fact := rel.SalientFacts[0]
		if fact.Kind != sim.InteractionPaidBy {
			t.Errorf("ezekiel→hannah fact.Kind = %q, want PaidBy", fact.Kind)
		}
		if fact.Text != "Hannah paid me 3 coins for ale." {
			t.Errorf("ezekiel→hannah fact.Text = %q", fact.Text)
		}
	}
}

// --- TestPay_NoForText: same shape, ForText omitted produces the recipient's
// "X paid me N coins." form without the trailing "for ...".
func TestPay_NoForText(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(playerPay("hannah", "Ezekiel Crane", 5, "", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	snap := w.Published()
	fact := snap.Actors["ezekiel"].Relationships["hannah"].SalientFacts[0]
	if fact.Text != "Hannah paid me 5 coins." {
		t.Errorf("fact.Text = %q, want %q", fact.Text, "Hannah paid me 5 coins.")
	}
}

// --- TestPay_SingularCoin: amount=1 produces "1 coin" not "1 coins".
func TestPay_SingularCoin(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(playerPay("hannah", "Ezekiel Crane", 1, "", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	snap := w.Published()
	fact := snap.Actors["ezekiel"].Relationships["hannah"].SalientFacts[0]
	if !strings.Contains(fact.Text, "1 coin.") || strings.Contains(fact.Text, "1 coins") {
		t.Errorf("singular coin missing: %q", fact.Text)
	}
}

// --- TestPay_NoHuddle: buyer not in any huddle rejects.
func TestPay_NoHuddle(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, coins: 10},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared},
	)
	defer stop()

	captured := capturePaid(t, w)
	_, err := w.Send(playerPay("hannah", "Ezekiel Crane", 3, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay: want error for no-huddle, got nil")
	}
	if !strings.Contains(err.Error(), "not in a conversation") {
		t.Errorf("error lacks 'not in a conversation' guidance: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Paid emitted on rejected pay: %v", *captured)
	}
	// No coin change, no relationship write.
	snap := w.Published()
	if got := snap.Actors["hannah"].Coins; got != 10 {
		t.Errorf("hannah.Coins changed: %d", got)
	}
	if len(snap.Actors["hannah"].Relationships) != 0 {
		t.Errorf("hannah Relationships after reject: %v", snap.Actors["hannah"].Relationships)
	}
}

// --- TestPay_WalkInFlight: buyer with MoveIntent rejects.
func TestPay_WalkInFlight(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10, moveInFlight: true},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := capturePaid(t, w)
	_, err := w.Send(playerPay("hannah", "Ezekiel Crane", 3, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay: want error for walk-in-flight, got nil")
	}
	if !strings.Contains(err.Error(), "walking") {
		t.Errorf("error lacks 'walking' guidance: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Paid emitted on rejected pay: %v", *captured)
	}
}

// --- TestPay_RecipientNotInHuddle: recipient name doesn't match any peer
// in the buyer's huddle (the actor exists, just not in this conversation).
func TestPay_RecipientNotInHuddle(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared}, // NOT in huddle h1
	)
	defer stop()

	captured := capturePaid(t, w)
	_, err := w.Send(playerPay("hannah", "Ezekiel Crane", 3, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay: want error for recipient-not-in-huddle, got nil")
	}
	if !strings.Contains(err.Error(), `"Ezekiel Crane"`) {
		t.Errorf("error message should name the missing recipient; got: %v", err)
	}
	if !strings.Contains(err.Error(), "no one named") {
		t.Errorf("error lacks 'no one named' guidance: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Paid emitted on rejected pay: %v", *captured)
	}
}

// --- TestPay_RecipientNonExistent: name doesn't match any actor at all.
// Same error message as "exists but not in huddle" — the buyer can't tell
// the difference from inside the conversation, and the error matches the
// conversational frame ("no one HERE named X").
func TestPay_RecipientNonExistent(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	_, err := w.Send(playerPay("hannah", "Phantom", 3, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay: want error for nonexistent recipient, got nil")
	}
	if !strings.Contains(err.Error(), "no one named") {
		t.Errorf("error message = %v", err)
	}
}

// --- TestPay_SelfByName: paying via the buyer's own display name reads as
// "no one named X" (the buyer is excluded from the peer scan), not as a
// self-pay-specific error. This matches the conversational framing: from
// inside the huddle, "who is here besides me?" is the model's view.
func TestPay_SelfByName(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	_, err := w.Send(playerPay("hannah", "Hannah", 3, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay: want error for self-by-name, got nil")
	}
	if !strings.Contains(err.Error(), "no one named") {
		t.Errorf("error message = %v, want 'no one named' framing", err)
	}
}

// --- TestPay_InsufficientCoins: balance < amount rejects.
func TestPay_InsufficientCoins(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 2},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	captured := capturePaid(t, w)
	_, err := w.Send(playerPay("hannah", "Ezekiel Crane", 5, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay: want error for insufficient coins, got nil")
	}
	if !strings.Contains(err.Error(), "not enough to give") {
		t.Errorf("error lacks 'not enough to give': %v", err)
	}
	if !strings.Contains(err.Error(), "only 2 coins") || !strings.Contains(err.Error(), "give 5 coins") {
		t.Errorf("error should include exact balance and amount; got: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Paid emitted on rejected pay: %v", *captured)
	}
	// No coin movement.
	snap := w.Published()
	if got := snap.Actors["hannah"].Coins; got != 2 {
		t.Errorf("hannah.Coins moved: %d", got)
	}
	if got := snap.Actors["ezekiel"].Coins; got != 0 {
		t.Errorf("ezekiel.Coins moved: %d", got)
	}
}

// --- TestPay_ExactBalance: balance == amount succeeds (boundary case).
func TestPay_ExactBalance(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 5},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(playerPay("hannah", "Ezekiel Crane", 5, "", time.Now().UTC())); err != nil {
		t.Fatalf("Pay at exact balance: %v", err)
	}
	snap := w.Published()
	if got := snap.Actors["hannah"].Coins; got != 0 {
		t.Errorf("hannah.Coins = %d, want 0", got)
	}
	if got := snap.Actors["ezekiel"].Coins; got != 5 {
		t.Errorf("ezekiel.Coins = %d, want 5", got)
	}
}

// --- TestPay_CaseInsensitiveRecipient: "ezekiel crane" matches "Ezekiel Crane".
func TestPay_CaseInsensitiveRecipient(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	cases := []string{"ezekiel crane", "EZEKIEL CRANE", "EzEkIeL cRaNe"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			// Fresh receiver balance each subtest — reuse the same world but
			// re-test the lookup, not the transfer accumulating.
			if _, err := w.Send(playerPay("hannah", name, 1, "", time.Now().UTC())); err != nil {
				t.Errorf("Pay(%q): %v", name, err)
			}
		})
	}
}

// --- TestPay_UnknownBuyer: buyer ID not in w.Actors errors.
func TestPay_UnknownBuyer(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
	)
	defer stop()

	_, err := w.Send(playerPay("ghost", "Hannah", 3, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay: want error for unknown buyer, got nil")
	}
	if !strings.Contains(err.Error(), "not in world") {
		t.Errorf("error = %v, want 'not in world'", err)
	}
}

// --- TestPay_KindNPCSharedGate_Matrix: persistence matrix by recipient kind.
// The payer is always a player (LLM-725) and RecordInteraction keeps no row
// for a PC; only a shared-VA NPC recipient records being paid (a stateful
// NPC's VA keeps its own memory). Same shape as speak's gate matrix.
func TestPay_KindNPCSharedGate_Matrix(t *testing.T) {
	cases := []struct {
		name         string
		sellerKind   sim.ActorKind
		sellerWrites bool
	}{
		{"pc_to_shared", sim.KindNPCShared, true},
		{"pc_to_stateful", sim.KindNPCStateful, false},
		{"pc_to_pc", sim.KindPC, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildPayTestWorld(t,
				payActorSpec{id: "b", displayName: "Buyer", kind: sim.KindPC, huddleID: "h1", coins: 10},
				payActorSpec{id: "s", displayName: "Seller", kind: tc.sellerKind, huddleID: "h1"},
			)
			defer stop()

			if _, err := w.Send(sim.Pay("b", "Seller", 3, "", time.Now().UTC())); err != nil {
				t.Fatalf("Pay: %v", err)
			}
			snap := w.Published()
			if snap.Actors["b"].Relationships["s"] != nil {
				t.Errorf("player payer side wrote a relationship row")
			}
			sellerSideWrote := snap.Actors["s"].Relationships["b"] != nil
			if sellerSideWrote != tc.sellerWrites {
				t.Errorf("seller side wrote = %v, want %v", sellerSideWrote, tc.sellerWrites)
			}
			// Coin transfer ALWAYS happens (the KindNPCShared gate is on the
			// relationship writes only — the transfer itself is unconditional).
			if got := snap.Actors["b"].Coins; got != 7 {
				t.Errorf("buyer.Coins = %d, want 7 (transfer should always fire)", got)
			}
			if got := snap.Actors["s"].Coins; got != 3 {
				t.Errorf("seller.Coins = %d, want 3 (transfer should always fire)", got)
			}
		})
	}
}

// --- TestPay_RejectionEmitsNoPaid: every reject path leaves no Paid event
// and no coin movement.
func TestPay_RejectionEmitsNoPaid(t *testing.T) {
	cases := []struct {
		name string
		set  func(t *testing.T) (*sim.World, func(), sim.ActorID, string, int)
	}{
		{
			name: "no_huddle",
			set: func(t *testing.T) (*sim.World, func(), sim.ActorID, string, int) {
				w, stop := buildPayTestWorld(t,
					payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, coins: 10},
					payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared},
				)
				return w, stop, "hannah", "Ezekiel Crane", 3
			},
		},
		{
			name: "walk_in_flight",
			set: func(t *testing.T) (*sim.World, func(), sim.ActorID, string, int) {
				w, stop := buildPayTestWorld(t,
					payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10, moveInFlight: true},
					payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
				)
				return w, stop, "hannah", "Ezekiel Crane", 3
			},
		},
		{
			name: "recipient_absent",
			set: func(t *testing.T) (*sim.World, func(), sim.ActorID, string, int) {
				w, stop := buildPayTestWorld(t,
					payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
					payActorSpec{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
				)
				return w, stop, "hannah", "Phantom", 3
			},
		},
		{
			name: "insufficient_coins",
			set: func(t *testing.T) (*sim.World, func(), sim.ActorID, string, int) {
				w, stop := buildPayTestWorld(t,
					payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 1},
					payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
				)
				return w, stop, "hannah", "Ezekiel Crane", 5
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop, buyerID, recipient, amount := tc.set(t)
			defer stop()
			captured := capturePaid(t, w)
			beforeSnap := w.Published()
			beforeBuyer := beforeSnap.Actors[buyerID].Coins
			_, err := w.Send(playerPay(buyerID, recipient, amount, "", time.Now().UTC()))
			if err == nil {
				t.Fatal("Pay: want error, got nil")
			}
			if len(*captured) != 0 {
				t.Errorf("Paid emitted on reject: %v", *captured)
			}
			// Coin balance unchanged.
			afterSnap := w.Published()
			if got := afterSnap.Actors[buyerID].Coins; got != beforeBuyer {
				t.Errorf("buyer balance moved on reject: before=%d after=%d", beforeBuyer, got)
			}
		})
	}
}

// --- TestPay_RejectsNonPositiveAmount: amount < 1 rejected by the Command
// itself (defense in depth — Pay is exported, non-handler callers must not
// be able to mint coins via amount<=0). Verifies no event, no transfer,
// no relationship writes.
func TestPay_RejectsNonPositiveAmount(t *testing.T) {
	cases := []struct {
		name   string
		amount int
	}{
		{"zero", 0},
		{"negative_small", -1},
		{"negative_large", -1000},
		{"int_min", math.MinInt32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildPayTestWorld(t,
				payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
				payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1", coins: 5},
			)
			defer stop()
			captured := capturePaid(t, w)
			_, err := w.Send(playerPay("hannah", "Ezekiel Crane", tc.amount, "", time.Now().UTC()))
			if err == nil {
				t.Fatalf("Pay(amount=%d): want error, got nil", tc.amount)
			}
			if !strings.Contains(err.Error(), "at least 1") {
				t.Errorf("error lacks 'at least 1' guidance: %v", err)
			}
			if len(*captured) != 0 {
				t.Errorf("Paid emitted on amount=%d: %v", tc.amount, *captured)
			}
			snap := w.Published()
			if got := snap.Actors["hannah"].Coins; got != 10 {
				t.Errorf("hannah.Coins moved: %d (want 10)", got)
			}
			if got := snap.Actors["ezekiel"].Coins; got != 5 {
				t.Errorf("ezekiel.Coins moved: %d (want 5)", got)
			}
			if len(snap.Actors["hannah"].Relationships) != 0 {
				t.Errorf("hannah relationships after reject: %v", snap.Actors["hannah"].Relationships)
			}
		})
	}
}

// --- TestPay_RejectsAmountOverMax: amount > MaxPayAmount rejected.
func TestPay_RejectsAmountOverMax(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: math.MaxInt},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()
	captured := capturePaid(t, w)
	_, err := w.Send(playerPay("hannah", "Ezekiel Crane", sim.MaxPayAmount+1, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay(amount=MaxPayAmount+1): want error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("error lacks 'exceeds maximum': %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Paid emitted on over-max amount: %v", *captured)
	}
}

// --- TestPay_RejectsSellerBalanceOverflow: seller already at near-MaxInt
// + a legitimate amount would wrap negative. Theoretical at village scale
// but mint-path-adjacent.
func TestPay_RejectsSellerBalanceOverflow(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 1000},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1", coins: math.MaxInt - 100},
	)
	defer stop()
	captured := capturePaid(t, w)
	// 500 + (MaxInt - 100) overflows MaxInt.
	_, err := w.Send(playerPay("hannah", "Ezekiel Crane", 500, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay near seller overflow: want error, got nil")
	}
	if !strings.Contains(err.Error(), "overflow") {
		t.Errorf("error lacks 'overflow': %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Paid emitted on overflow: %v", *captured)
	}
	snap := w.Published()
	if got := snap.Actors["hannah"].Coins; got != 1000 {
		t.Errorf("hannah.Coins moved on overflow reject: %d", got)
	}
}

// --- TestPay_AmbiguousRecipientRejects: two huddle peers share a
// case-insensitive DisplayName. Pay must reject rather than pick one
// non-deterministically (money-transfer determinism).
func TestPay_AmbiguousRecipientRejects(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "john1", displayName: "John", kind: sim.KindNPCShared, huddleID: "h1"},
		payActorSpec{id: "john2", displayName: "john", kind: sim.KindNPCShared, huddleID: "h1"}, // case-insensitive duplicate
	)
	defer stop()
	captured := capturePaid(t, w)
	_, err := w.Send(playerPay("hannah", "John", 3, "", time.Now().UTC()))
	if err == nil {
		t.Fatal("Pay ambiguous: want error, got nil")
	}
	if !strings.Contains(err.Error(), "more than one") {
		t.Errorf("error lacks 'more than one' guidance: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("Paid emitted on ambiguous reject: %v", *captured)
	}
	// Neither John receives coins, buyer balance unchanged.
	snap := w.Published()
	if got := snap.Actors["hannah"].Coins; got != 10 {
		t.Errorf("hannah.Coins moved: %d", got)
	}
	if got := snap.Actors["john1"].Coins; got != 0 {
		t.Errorf("john1.Coins moved: %d", got)
	}
	if got := snap.Actors["john2"].Coins; got != 0 {
		t.Errorf("john2.Coins moved: %d", got)
	}
}

// --- TestPay_TwoPaysAccumulate: pay twice → two SalientFacts on each side,
// InteractionCount=2, LastInteractionAt advances.
func TestPay_TwoPaysAccumulate(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	first := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	if _, err := w.Send(playerPay("hannah", "Ezekiel Crane", 1, "", first)); err != nil {
		t.Fatalf("Pay 1: %v", err)
	}
	if _, err := w.Send(playerPay("hannah", "Ezekiel Crane", 2, "bread", second)); err != nil {
		t.Fatalf("Pay 2: %v", err)
	}
	snap := w.Published()
	rel := snap.Actors["ezekiel"].Relationships["hannah"] // the recipient records; a player payer keeps no row
	if rel == nil {
		t.Fatal("relationship missing")
	}
	if rel.InteractionCount != 2 {
		t.Errorf("InteractionCount = %d, want 2", rel.InteractionCount)
	}
	if len(rel.SalientFacts) != 2 {
		t.Fatalf("SalientFacts len = %d, want 2", len(rel.SalientFacts))
	}
	if !rel.LastInteractionAt.Equal(second) {
		t.Errorf("LastInteractionAt = %v, want %v", rel.LastInteractionAt, second)
	}
}

// --- TestPay_RedirectsToOpenQuoteSettlement: LLM-172. A bare pay naming a good
// the seller has an active quote for is rejected with a pointer to the quote's
// offer card, and no coins move — bare pay transfers coins but never delivers
// the good or settles the quote, so letting it through leaks coins for nothing
// (the live Ezekiel/John stew loop). buildFastPathFixture posts Bob's active
// stew quote (id 7, qty 1, 4 coins) to Alice in scene sc1.
func TestPay_RedirectsToOpenQuoteSettlement(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	_, err := w.Send(playerPay("alice", "Bob", 4, "stew", at))
	if err == nil {
		t.Fatal("bare pay for a quoted good should point at the offer card")
	}
	if !strings.Contains(err.Error(), "Bob is offering Stew for 4 coins") {
		t.Errorf("redirect missing the quoted good and price: %v", err)
	}
	if !strings.Contains(err.Error(), "offer card") || !strings.Contains(err.Error(), "does not buy it") {
		t.Errorf("redirect missing the offer-card steer: %v", err)
	}

	// The reject fires before any state change — no coins moved.
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 50 {
		t.Errorf("alice.Coins = %d, want 50 (no transfer on redirect)", got)
	}
	if got := snap.Actors["bob"].Coins; got != 0 {
		t.Errorf("bob.Coins = %d, want 0 (no transfer on redirect)", got)
	}
}

// --- TestPay_AllowsTipWhenForTextNamesNoQuotedGood: LLM-172 fall-through. A
// bare pay whose forText names no active quoted good (a tip / thanks) is not a
// botched purchase — it proceeds as a plain coin transfer. Same fixture (Bob
// has only a stew quote), but the pay is "for" something resolveItemKind can't
// canonicalize, so the open-quote guard doesn't fire.
func TestPay_AllowsTipWhenForTextNamesNoQuotedGood(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	if _, err := w.Send(playerPay("alice", "Bob", 3, "your kindness", at)); err != nil {
		t.Fatalf("a tip naming no quoted good should proceed: %v", err)
	}
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 47 {
		t.Errorf("alice.Coins = %d, want 47 (tip transferred)", got)
	}
	if got := snap.Actors["bob"].Coins; got != 3 {
		t.Errorf("bob.Coins = %d, want 3 (tip received)", got)
	}
}

// --- TestPay_CoinShortQuoteStillRefused: LLM-172. A payer short of the quote
// is refused the same way and moves no coins. The refusal names the price, so
// the player at the Pay box sees why the card is out of reach (the box itself
// says "You only have N" before it sends a take). It names no tool — sim.Pay's
// one caller is the player route (LLM-725). Alice is dropped below Bob's 4-coin
// stew quote.
func TestPay_CoinShortQuoteStillRefused(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	mustSend(t, w, func(world *sim.World) { world.Actors["alice"].Coins = 3 })

	_, err := w.Send(playerPay("alice", "Bob", 3, "stew", at))
	if err == nil {
		t.Fatal("coin-short bare pay for a quoted good should be rejected")
	}
	msg := err.Error()
	if strings.Contains(msg, "pay_with_item") || strings.Contains(msg, "offer_trade") {
		t.Errorf("refusal names an NPC tool: %v", err)
	}
	if !strings.Contains(msg, "for 4 coins") || !strings.Contains(msg, "offer card") {
		t.Errorf("refusal should name the quote's price and its offer card: %v", err)
	}

	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 3 {
		t.Errorf("alice.Coins = %d, want 3 (no transfer)", got)
	}
	if got := snap.Actors["bob"].Coins; got != 0 {
		t.Errorf("bob.Coins = %d, want 0 (no transfer)", got)
	}
}

// --- TestPay_RejectsBundleMemo: LLM-649. A bare pay whose memo names several
// goods is a purchase on the wrong tool — the LLM-172 guard resolves the whole
// memo as one item and sees nothing in a list, so the coins moved and no goods
// did (live 2026-08-31: a factor's "5x wheat, 3x flour, 2x firewood" at
// Josiah's counter). Refuse it with the pay_with_item steer and move nothing.
func TestPay_RejectsBundleMemo(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	// The last three are code_review's whitespace variants around "and": a
	// single-space split was bypassable by a second space, a tab, or a newline.
	for _, memo := range []string{
		"bread, ale", "5x wheat, 3x bread", "bread and ale", "bread; ale", "2 x bread & 1 ale",
		"5x wheat and  3x bread", "bread\tand\tale", "wheat\nand\nbread",
	} {
		_, err := w.Send(playerPay("alice", "Bob", 7, memo, at))
		if err == nil {
			t.Fatalf("bare pay for %q should be refused", memo)
		}
		if !strings.Contains(err.Error(), "giving coins buys none of") || !strings.Contains(err.Error(), "make Bob an offer") {
			t.Errorf("memo %q: steer missing: %v", memo, err)
		}
	}
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 50 {
		t.Errorf("alice.Coins = %d, want 50 (no transfer on refusal)", got)
	}
	if got := snap.Actors["bob"].Coins; got != 0 {
		t.Errorf("bob.Coins = %d, want 0 (no transfer on refusal)", got)
	}
}

// --- TestPay_RejectsCountedGoodMemo: LLM-649. One good with an explicit count
// ("5x wheat") is the same purchase shape as a bundle and is refused the same way.
func TestPay_RejectsCountedGoodMemo(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	_, err := w.Send(playerPay("alice", "Bob", 10, "5x wheat", at))
	if err == nil || !strings.Contains(err.Error(), "giving coins buys none of the Wheat") {
		t.Fatalf("counted single good should be refused as a purchase, got %v", err)
	}
	if got := w.Published().Actors["alice"].Coins; got != 50 {
		t.Errorf("alice.Coins = %d, want 50 (no transfer on refusal)", got)
	}
}

// --- TestPay_AllowsUncountedSingleGoodMemo: LLM-649 scope boundary. One good
// with no count and no open quote for it ("the ale") is the debt-memo shape and
// still transfers — Bob has a stew quote only, so the LLM-172 guard is silent too.
func TestPay_AllowsUncountedSingleGoodMemo(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	if _, err := w.Send(playerPay("alice", "Bob", 2, "the ale", at)); err != nil {
		t.Fatalf("a single uncounted good with no quote should transfer as a debt memo: %v", err)
	}
	if got := w.Published().Actors["bob"].Coins; got != 2 {
		t.Errorf("bob.Coins = %d, want 2", got)
	}
}

// --- LLM-659: a bare pay whose memo claims to be a refund is refused unless
// the coin record shows the recipient actually paid the payer inside the
// window. The live case: Lewis Walker, never paid by Josiah Thorne, "refunded"
// him 6 coins for Josiah's own undelivered whetstone.

func TestPay_RefundMemoWithNothingReceivedRejects(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "lewis", displayName: "Lewis Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
	)
	defer stop()
	paid := capturePaid(t, w)
	at := time.Now().UTC()

	_, err := w.Send(playerPay("lewis", "Josiah Thorne", 6, "refund for the whetstone that never arrived", at))
	if err == nil {
		t.Fatal("refund memo with no coin received from the recipient should be refused")
	}
	if !strings.Contains(err.Error(), "Josiah Thorne has paid you no coin these past 7 days") {
		t.Errorf("rejection = %q, want the record-driven line naming the recipient", err.Error())
	}
	snap := w.Published()
	if got := snap.Actors["lewis"].Coins; got != 10 {
		t.Errorf("lewis.Coins = %d, want 10 (unchanged)", got)
	}
	if got := snap.Actors["josiah"].Coins; got != 0 {
		t.Errorf("josiah.Coins = %d, want 0 (unchanged)", got)
	}
	if len(*paid) != 0 {
		t.Errorf("Paid events = %d, want 0", len(*paid))
	}
}

// The same memo transfers once the record shows the recipient paid the payer —
// the honest refund direction. Seeded straight onto the record: in production
// the cascade subscriber credits it beside the durable row.
func TestPay_RefundMemoAfterRecipientPaidAllows(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "lewis", displayName: "Lewis Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
	)
	defer stop()
	at := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.RecordCoinPaid("lewis", "josiah", 5, at.Add(-2*24*time.Hour), sim.CoinPaymentUnstated)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed coin record: %v", err)
	}

	if _, err := w.Send(playerPay("josiah", "Lewis Walker", 5, "refund for the whetstone I never delivered", at)); err != nil {
		t.Fatalf("a refund from the party who was paid should transfer: %v", err)
	}
	if got := w.Published().Actors["lewis"].Coins; got != 15 {
		t.Errorf("lewis.Coins = %d, want 15", got)
	}
}

// A payment the recipient made OUTSIDE the window does not license a refund —
// the guard asks the same week the coin cue renders.
func TestPay_RefundMemoWithReceiptAgedOutRejects(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "lewis", displayName: "Lewis Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
	)
	defer stop()
	at := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.RecordCoinPaid("lewis", "josiah", 5, at.Add(-(sim.DefaultCoinRecordWindow + time.Hour)), sim.CoinPaymentUnstated)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed coin record: %v", err)
	}

	if _, err := w.Send(playerPay("josiah", "Lewis Walker", 5, "refund for the whetstone", at)); err == nil {
		t.Fatal("a receipt older than the window should not license a refund")
	}
	if got := w.Published().Actors["lewis"].Coins; got != 10 {
		t.Errorf("lewis.Coins = %d, want 10 (unchanged)", got)
	}
}

// A memo that is not a repayment claim never consults the record: the debt
// memo where the PAYER owes ("what I owe you") is the ordinary shape and
// transfers with nothing on the record at all.
func TestPay_DebtMemoWithoutRepaymentClaimAllows(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "lewis", displayName: "Lewis Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
	)
	defer stop()
	at := time.Now().UTC()

	if _, err := w.Send(playerPay("lewis", "Josiah Thorne", 3, "what I owe you for the flour", at)); err != nil {
		t.Fatalf("a debt memo where the payer owes should transfer: %v", err)
	}
	if got := w.Published().Actors["josiah"].Coins; got != 3 {
		t.Errorf("josiah.Coins = %d, want 3", got)
	}
}

// Bare-pay coin licenses a refund only up to its total: 5 coins paid by hand do not
// let the seller "refund" 6. The same memo at 5 transfers.
func TestPay_RefundMemoCappedAtReceivedTotal(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "lewis", displayName: "Lewis Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
	)
	defer stop()
	at := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.RecordCoinPaid("lewis", "josiah", 5, at.Add(-2*24*time.Hour), sim.CoinPaymentUnstated)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed coin record: %v", err)
	}

	_, err := w.Send(playerPay("josiah", "Lewis Walker", 6, "refund for the whetstone", at))
	if err == nil {
		t.Fatal("a refund larger than the recipient's receipts should be refused")
	}
	if !strings.Contains(err.Error(), "has paid you only 5 coins these past 7 days not for goods, work or a due") {
		t.Errorf("rejection = %q, want the received total named", err.Error())
	}
	if got := w.Published().Actors["lewis"].Coins; got != 10 {
		t.Errorf("lewis.Coins = %d, want 10 (unchanged)", got)
	}
	if _, err := w.Send(playerPay("josiah", "Lewis Walker", 5, "refund for the whetstone", at)); err != nil {
		t.Fatalf("a refund within the recipient's receipts should transfer: %v", err)
	}
	if got := w.Published().Actors["lewis"].Coins; got != 15 {
		t.Errorf("lewis.Coins = %d, want 15", got)
	}
}

// LLM-660: coin that bought delivered goods is not refundable, so a purchase
// does not license the reversed refund. This is the live Lewis/Josiah shape:
// Josiah bought 7 coins of firewood and wheat from Lewis three days before
// Lewis "refunded" him 6 for Josiah's own undelivered whetstone.
func TestPay_RefundMemoAgainstGoodsReceiptRejects(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "lewis", displayName: "Lewis Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
	)
	defer stop()
	at := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.RecordCoinPaid("josiah", "lewis", 7, at.Add(-3*24*time.Hour), sim.CoinPaymentForGoods)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed coin record: %v", err)
	}

	_, err := w.Send(playerPay("lewis", "Josiah Thorne", 6, "refund for the whetstone that never arrived", at))
	if err == nil {
		t.Fatal("a delivered purchase must not license a refund to the buyer")
	}
	if !strings.Contains(err.Error(), "every coin Josiah Thorne has paid you these past 7 days bought goods") {
		t.Errorf("rejection = %q, want the goods-accounted line", err.Error())
	}
	if got := w.Published().Actors["lewis"].Coins; got != 10 {
		t.Errorf("lewis.Coins = %d, want 10 (unchanged)", got)
	}
}

// The live row that slipped LLM-660 (LLM-662): Silence Walker paid John
// Ellis 6 coins for ale, bread and cheese, and a minute later John "handed
// two back" for an overcharge that never happened. The memo carries no
// refund word, no giving verb and no coin word — only "two back" — and the
// receipt it claims against bought delivered goods, so the guard refuses.
func TestPay_NumberBackMemoAgainstGoodsReceiptRejects(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "john", displayName: "John Ellis", kind: sim.KindNPCStateful, huddleID: "h1", coins: 6},
		payActorSpec{id: "silence", displayName: "Silence Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
	)
	defer stop()
	at := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.RecordCoinPaid("silence", "john", 6, at.Add(-time.Minute), sim.CoinPaymentForGoods)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed coin record: %v", err)
	}

	_, err := w.Send(playerPay("john", "Silence Walker", 2,
		"You paid six but the ale and bread come to four — here's two back", at))
	if err == nil {
		t.Fatal("\"here's two back\" against a goods receipt must be refused")
	}
	if !strings.Contains(err.Error(), "every coin Silence Walker has paid you these past 7 days bought goods") {
		t.Errorf("rejection = %q, want the goods-accounted line", err.Error())
	}
	if got := w.Published().Actors["john"].Coins; got != 6 {
		t.Errorf("john.Coins = %d, want 6 (unchanged)", got)
	}
}

// Mixed receipts bound on the bare-pay part only: 7 coins for goods plus 3
// paid by hand license a 3-coin refund, not a 5-coin one. A wage counts as
// accounted the same way goods do.
func TestPay_RefundMemoBoundsOnUnaccountedReceiptsOnly(t *testing.T) {
	w, stop := buildPayTestWorld(t,
		payActorSpec{id: "lewis", displayName: "Lewis Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payActorSpec{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
	)
	defer stop()
	at := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.RecordCoinPaid("josiah", "lewis", 7, at.Add(-3*24*time.Hour), sim.CoinPaymentForGoods)
		world.RecordCoinPaid("josiah", "lewis", 4, at.Add(-2*24*time.Hour), sim.CoinPaymentForWork)
		world.RecordCoinPaid("josiah", "lewis", 3, at.Add(-24*time.Hour), sim.CoinPaymentUnstated)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed coin record: %v", err)
	}

	_, err := w.Send(playerPay("lewis", "Josiah Thorne", 5, "paying you back", at))
	if err == nil {
		t.Fatal("a refund above the bare-pay receipts should be refused")
	}
	if !strings.Contains(err.Error(), "has paid you only 3 coins these past 7 days not for goods, work or a due") {
		t.Errorf("rejection = %q, want the unaccounted total named", err.Error())
	}
	if _, err := w.Send(playerPay("lewis", "Josiah Thorne", 3, "paying you back", at)); err != nil {
		t.Fatalf("a refund within the bare-pay receipts should transfer: %v", err)
	}
	if got := w.Published().Actors["josiah"].Coins; got != 3 {
		t.Errorf("josiah.Coins = %d, want 3", got)
	}
}
