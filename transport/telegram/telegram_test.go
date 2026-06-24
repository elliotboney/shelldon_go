package telegram_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/arbiter"
	"github.com/elliotboney/shelldon_go/core/bus"
	"github.com/elliotboney/shelldon_go/core/dispatch"
	"github.com/elliotboney/shelldon_go/core/state"
	"github.com/elliotboney/shelldon_go/core/supervisor"
	"github.com/elliotboney/shelldon_go/transport/telegram"
	"github.com/elliotboney/shelldon_go/worker"
)

// sentMsg captures a Send the adapter made back to Telegram.
type sentMsg struct {
	chatID int64
	text   string
}

// fakeClient is a telegram.Client test double: it feeds updates the test pushes
// and captures Send calls — no real bot, no network (mirrors broker.fakeProvider
// and the CLI test's pipes). Note it uses only telegram.Update (simple int64/
// string) — no telego type crosses into the test, proving the edge mapping.
type fakeClient struct {
	updates chan telegram.Update
	sent    chan sentMsg
}

func newFakeClient() *fakeClient {
	return &fakeClient{updates: make(chan telegram.Update, 1), sent: make(chan sentMsg, 1)}
}

func (f *fakeClient) Updates(_ context.Context) (<-chan telegram.Update, error) {
	return f.updates, nil
}

func (f *fakeClient) Send(_ context.Context, chatID int64, text string) error {
	f.sent <- sentMsg{chatID, text}
	return nil
}

// TestEndToEndRoundTrip is the AC1 spine proof: a Telegram update round-trips
// inbound → core → worker seam → stub → outbound → Telegram, wiring the REAL bus,
// arbiter, dispatch, and stub (no mocks) with a fake Telegram client. The stub
// echoes the input, so the reply Sent back to the chat equals the text received,
// and the chat id received outbound equals the one that arrived inbound — proving
// the native id is mapped to ConvoID at the edge and reversed for the reply.
func TestEndToEndRoundTrip(t *testing.T) {
	hub := bus.New()
	inbound := make(chan contracts.Envelope, 1)
	outbound := make(chan contracts.Envelope, 1)
	if err := hub.Register(contracts.KindInboundMessage, inbound); err != nil {
		t.Fatalf("register inbound: %v", err)
	}
	if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
		t.Fatalf("register outbound: %v", err)
	}

	arb := arbiter.New(worker.Stub{}, time.Minute)
	store := state.New(state.Default(), filepath.Join(t.TempDir(), "state.json"))
	disp := dispatch.New(hub, arb, inbound, store)

	fc := newFakeClient()
	adapter := telegram.New(hub, outbound, fc, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = disp.Serve(ctx) }()
	go func() { _ = adapter.Serve(ctx) }()

	fc.updates <- telegram.Update{ChatID: 42, Text: "hello"}

	select {
	case s := <-fc.sent:
		if s.chatID != 42 {
			t.Fatalf("reply sent to chat %d, want 42 (ConvoID did not map back to the originating chat)", s.chatID)
		}
		if s.text != "hello" {
			t.Fatalf("reply text = %q, want %q (the stub echo did not round-trip)", s.text, "hello")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the round-trip reply — the Telegram spine did not deliver")
	}
}

// TestEdgeMapsChatIDToConvoID is AC1's edge-mapping half: the inbound update's
// native chat id becomes core's ConvoID at the edge (no telego type crosses in).
func TestEdgeMapsChatIDToConvoID(t *testing.T) {
	hub := bus.New()
	inbound := make(chan contracts.Envelope, 1)
	outbound := make(chan contracts.Envelope, 1)
	if err := hub.Register(contracts.KindInboundMessage, inbound); err != nil {
		t.Fatalf("register inbound: %v", err)
	}

	fc := newFakeClient()
	adapter := telegram.New(hub, outbound, fc, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = adapter.Serve(ctx) }()

	fc.updates <- telegram.Update{ChatID: 42, Text: "hello"}

	select {
	case env := <-inbound:
		msg, ok := env.Payload.(contracts.InboundMessage)
		if !ok {
			t.Fatalf("inbound payload = %T, want contracts.InboundMessage", env.Payload)
		}
		if msg.ConvoID != "42" {
			t.Fatalf("ConvoID = %q, want %q (chat id not mapped at the edge)", msg.ConvoID, "42")
		}
		if msg.Text != "hello" {
			t.Fatalf("Text = %q, want %q", msg.Text, "hello")
		}
		if env.Src != "telegram" {
			t.Fatalf("Src = %q, want %q", env.Src, "telegram")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message published from the Telegram update")
	}
}

// TestOwnerGuardDropsNonOwner is the minimal single-owner guard (AD-12): with an
// owner id configured, an update from a different chat is dropped; the owner's is
// published.
func TestOwnerGuardDropsNonOwner(t *testing.T) {
	hub := bus.New()
	inbound := make(chan contracts.Envelope, 2)
	outbound := make(chan contracts.Envelope, 1)
	if err := hub.Register(contracts.KindInboundMessage, inbound); err != nil {
		t.Fatalf("register inbound: %v", err)
	}

	fc := newFakeClient()
	const owner int64 = 100
	adapter := telegram.New(hub, outbound, fc, owner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = adapter.Serve(ctx) }()

	// A non-owner chat must be dropped: nothing reaches inbound.
	fc.updates <- telegram.Update{ChatID: 42, Text: "stranger"}
	select {
	case <-inbound:
		t.Fatal("a non-owner chat message was published — the owner guard did not drop it")
	case <-time.After(200 * time.Millisecond):
		// dropped as expected
	}

	// The owner's chat passes through.
	fc.updates <- telegram.Update{ChatID: owner, Text: "hi"}
	select {
	case env := <-inbound:
		msg := env.Payload.(contracts.InboundMessage)
		if msg.ConvoID != "100" || msg.Text != "hi" {
			t.Fatalf("owner message = %+v, want ConvoID 100 / text hi", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner message was dropped")
	}
}

// TestNATWatchdogTimeoutUnderWindow is AC3: the long-poll timeout must stay under
// the NAT-idle window so the connection refreshes before a NAT mapping expires. A
// future bump that lets the timeout meet/exceed the window fails the suite.
func TestNATWatchdogTimeoutUnderWindow(t *testing.T) {
	if telegram.LongPollTimeout >= telegram.NATIdleWindow {
		t.Fatalf("longPollTimeout %d must be < natIdleWindow %d (NAT-idle watchdog, AD-12)",
			telegram.LongPollTimeout, telegram.NATIdleWindow)
	}
}

// TestServeIsWireableAsEdge guards the supervised-edge wiring: Serve must be
// accepted by supervisor.Guard (the exact call main makes), so a signature drift
// can't silently break the transport wiring.
func TestServeIsWireableAsEdge(t *testing.T) {
	_ = supervisor.Guard("telegram-transport", (&telegram.Adapter{}).Serve)
}
