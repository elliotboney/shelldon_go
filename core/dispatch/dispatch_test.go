package dispatch_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/arbiter"
	"github.com/elliotboney/shelldon_go/core/bus"
	"github.com/elliotboney/shelldon_go/core/dispatch"
	"github.com/elliotboney/shelldon_go/core/memory"
	"github.com/elliotboney/shelldon_go/core/state"
	"github.com/elliotboney/shelldon_go/worker"
)

// wantAck is the canned no-LLM acknowledgement published when the brain cannot
// answer (AC1), linked to the real const so the value can't drift (export_test.go).
var wantAck = dispatch.ReflexAckForTest

// errWorker models an absent brain: every turn fails immediately (the shape of
// Epic 3's provider-chain exhaustion).
type errWorker struct{}

func (errWorker) AssembleAndPropose(_ context.Context, _ contracts.Job) (contracts.Result, error) {
	return contracts.Result{}, errors.New("no brain available")
}

// hangingWorker models a brain that cannot complete: it blocks until its context
// is cancelled, so the arbiter timeout must abandon the turn.
type hangingWorker struct{}

func (hangingWorker) AssembleAndPropose(ctx context.Context, _ contracts.Job) (contracts.Result, error) {
	<-ctx.Done()
	return contracts.Result{}, ctx.Err()
}

// TestServe_TouchesStateOnInbound verifies an inbound message stamps
// LastInteraction (the idle reset the blink reflex depends on, Story 2.3).
func TestServe_TouchesStateOnInbound(t *testing.T) {
	hub := bus.New()
	inbound := make(chan contracts.Envelope, 1)
	outbound := make(chan contracts.Envelope, 1)
	if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
		t.Fatalf("register outbound: %v", err)
	}

	old := time.Now().Add(-time.Hour)
	store := state.New(state.Personality{LastInteraction: old}, filepath.Join(t.TempDir(), "state.json"))
	d := dispatch.New(hub, arbiter.New(worker.Stub{}, time.Minute), inbound, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Serve(ctx) }()

	inbound <- contracts.Envelope{
		Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "cli", Dst: "core"},
		Payload: contracts.InboundMessage{ConvoID: "c1", Text: "hi"},
	}

	// The reply proves the turn was processed; Touch ran before the submit.
	<-outbound
	if !store.Snapshot().LastInteraction.After(old) {
		t.Fatal("LastInteraction was not stamped on inbound message")
	}
}

// TestServe_AcksWhenBrainAbsent is AC1: when the worker cannot answer (it errors),
// the message is acknowledged with the canned reflex ack, not dropped.
func TestServe_AcksWhenBrainAbsent(t *testing.T) {
	hub := bus.New()
	inbound := make(chan contracts.Envelope, 1)
	outbound := make(chan contracts.Envelope, 1)
	if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
		t.Fatalf("register outbound: %v", err)
	}
	store := state.New(state.Default(), filepath.Join(t.TempDir(), "state.json"))
	d := dispatch.New(hub, arbiter.New(errWorker{}, time.Minute), inbound, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Serve(ctx) }()

	inbound <- contracts.Envelope{
		Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "cli", Dst: "core"},
		Payload: contracts.InboundMessage{ConvoID: "c1", Text: "hi"},
	}

	env := <-outbound
	msg := env.Payload.(contracts.OutboundMessage)
	if msg.Text != wantAck {
		t.Fatalf("brain-absent reply = %q, want the reflex ack %q", msg.Text, wantAck)
	}
}

// TestServe_DoesNotRecordReflexAck is the post-review fix: when the brain is
// absent and the turn degrades to a reflex ack, the owner message is recorded but
// the ack is NOT — recording "…" as a pet reply would pollute the recent window
// the next prompt reads.
func TestServe_DoesNotRecordReflexAck(t *testing.T) {
	memStore, err := memory.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	defer func() { _ = memStore.Close() }()

	hub := bus.New()
	inbound := make(chan contracts.Envelope, 1)
	outbound := make(chan contracts.Envelope, 1)
	if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
		t.Fatalf("register outbound: %v", err)
	}
	stateStore := state.New(state.Default(), filepath.Join(t.TempDir(), "state.json"))
	d := dispatch.New(hub, arbiter.New(errWorker{}, time.Minute), inbound, stateStore, dispatch.WithRecorder(memStore))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = d.Serve(ctx) }()

	inbound <- contracts.Envelope{
		Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "cli", Dst: "core"},
		Payload: contracts.InboundMessage{ConvoID: "c1", Text: "hi"},
	}
	<-outbound // the reflex ack was published

	// Poll until the owner record lands; then assert the ack was NOT recorded.
	for {
		msgs, err := memStore.Recent(ctx, "c1", 10)
		if err != nil {
			t.Fatalf("Recent: %v", err)
		}
		if len(msgs) >= 1 {
			if len(msgs) != 1 || msgs[0].Role != "owner" || msgs[0].Content != "hi" {
				t.Fatalf("recorded %+v, want only the owner message (no reflex ack)", msgs)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("owner message was never recorded")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestServe_NeverBlocksUnderHungBrain is AC1's never-block property: with a brain
// that hangs every turn, the dispatch loop still drains the queue — both inbound
// messages are acknowledged (each via the arbiter timeout), so one stuck turn
// never wedges the loop. Deterministic under the synctest fake clock (AD-10).
func TestServe_NeverBlocksUnderHungBrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 5 * time.Second
		hub := bus.New()
		inbound := make(chan contracts.Envelope, 2)
		outbound := make(chan contracts.Envelope, 2)
		if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
			t.Fatalf("register outbound: %v", err)
		}
		store := state.New(state.Default(), filepath.Join(t.TempDir(), "state.json"))
		d := dispatch.New(hub, arbiter.New(hangingWorker{}, timeout), inbound, store)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = d.Serve(ctx) }()

		for _, id := range []string{"c1", "c2"} {
			inbound <- contracts.Envelope{
				Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "cli", Dst: "core"},
				Payload: contracts.InboundMessage{ConvoID: id, Text: "hi"},
			}
		}

		// Turns run sequentially (≤1 in flight); advance past both deadlines.
		time.Sleep(2*timeout + time.Second)
		synctest.Wait()

		got := map[string]string{}
		for i := 0; i < 2; i++ {
			env := <-outbound
			msg := env.Payload.(contracts.OutboundMessage)
			got[msg.ConvoID] = msg.Text
		}
		for _, id := range []string{"c1", "c2"} {
			if got[id] != wantAck {
				t.Errorf("convo %s reply = %q, want the reflex ack %q (loop wedged or message dropped)", id, got[id], wantAck)
			}
		}
	})
}

// TestServe_RecordsConversationTurn is AC2: after a successful turn, both the
// owner message and the pet reply are written to the memory recorder. The next
// call to Recent returns both, most-recent first (pet then owner).
func TestServe_RecordsConversationTurn(t *testing.T) {
	const convoID = "test-convo"

	// Open a real memory store so we exercise the actual Append path.
	memStore, err := memory.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	defer func() { _ = memStore.Close() }()

	hub := bus.New()
	inbound := make(chan contracts.Envelope, 1)
	outbound := make(chan contracts.Envelope, 1)
	if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
		t.Fatalf("register outbound: %v", err)
	}
	stateStore := state.New(state.Default(), filepath.Join(t.TempDir(), "state.json"))
	d := dispatch.New(
		hub,
		arbiter.New(worker.Stub{}, time.Minute),
		inbound,
		stateStore,
		dispatch.WithRecorder(memStore),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = d.Serve(ctx) }()

	inbound <- contracts.Envelope{
		Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "cli", Dst: "core"},
		Payload: contracts.InboundMessage{ConvoID: convoID, Text: "hello pet"},
	}

	// Wait for the reply to be published — guarantees the turn (and recording) is done.
	select {
	case env := <-outbound:
		msg := env.Payload.(contracts.OutboundMessage)
		if msg.ConvoID != convoID {
			t.Fatalf("reply convoID = %q, want %q", msg.ConvoID, convoID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for reply")
	}

	// Recording happens after publishReply, so the reply signal does not guarantee
	// the Appends have landed — poll until both turn messages are recorded.
	var msgs []memory.Message
	for {
		msgs, err = memStore.Recent(ctx, convoID, 10)
		if err != nil {
			t.Fatalf("Recent: %v", err)
		}
		if len(msgs) == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("recorded %d messages, want 2 (timed out)", len(msgs))
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Recent returns most-recent-first: pet reply then owner message.
	if msgs[0].Role != "pet" {
		t.Errorf("msgs[0].Role = %q, want %q", msgs[0].Role, "pet")
	}
	if msgs[1].Role != "owner" {
		t.Errorf("msgs[1].Role = %q, want %q", msgs[1].Role, "owner")
	}
	if msgs[1].Content != "hello pet" {
		t.Errorf("owner content = %q, want %q", msgs[1].Content, "hello pet")
	}
}

// TestServe_NoRecorder_NoPanic confirms that a Dispatcher wired without
// WithRecorder behaves identically to before — the reply is published and no
// panic occurs from a nil recorder dereference.
func TestServe_NoRecorder_NoPanic(t *testing.T) {
	hub := bus.New()
	inbound := make(chan contracts.Envelope, 1)
	outbound := make(chan contracts.Envelope, 1)
	if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
		t.Fatalf("register outbound: %v", err)
	}
	store := state.New(state.Default(), filepath.Join(t.TempDir(), "state.json"))
	// No WithRecorder — matches the pre-existing New call signature.
	d := dispatch.New(hub, arbiter.New(worker.Stub{}, time.Minute), inbound, store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = d.Serve(ctx) }()

	inbound <- contracts.Envelope{
		Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "cli", Dst: "core"},
		Payload: contracts.InboundMessage{ConvoID: "c1", Text: "hi"},
	}

	select {
	case <-outbound:
		// reply received — loop alive, no panic
	case <-ctx.Done():
		t.Fatal("timed out waiting for reply without recorder")
	}
}
