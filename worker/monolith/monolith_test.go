package monolith_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elliotboney/shelldon_go/broker"
	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/memory"
	"github.com/elliotboney/shelldon_go/worker"
	"github.com/elliotboney/shelldon_go/worker/monolith"
)

// fakeCompleter is a monolith.Completer test double: it captures the request it
// received and returns the configured response/error. blockUntilCancel makes it
// hang on ctx.Done() so cancellation propagation can be observed (AC2).
type fakeCompleter struct {
	resp             broker.Response
	err              error
	gotReq           broker.Request
	blockUntilCancel bool
	observedCancel   chan struct{}
}

func (f *fakeCompleter) Complete(ctx context.Context, req broker.Request) (broker.Response, error) {
	f.gotReq = req
	if f.blockUntilCancel {
		<-ctx.Done()
		if f.observedCancel != nil {
			close(f.observedCancel)
		}
		return broker.Response{}, ctx.Err()
	}
	return f.resp, f.err
}

// fakeContextSource is a monolith.ContextSource test double: it returns the
// configured block/error and never writes anything.
type fakeContextSource struct {
	block string
	err   error
}

func (f *fakeContextSource) PromptContext(_ context.Context, _ string) (string, error) {
	return f.block, f.err
}

// TestAssembleAndPropose_RealReply is AC1: the worker returns the model's reply,
// the request it built carries the owner's input, and it proposes no memory ops
// (it holds no store and writes nothing — AD-6).
func TestAssembleAndPropose_RealReply(t *testing.T) {
	fc := &fakeCompleter{resp: broker.Response{Text: "hi, I'm Shelldon"}}
	w := monolith.New(fc)

	res, err := w.AssembleAndPropose(context.Background(), contracts.Job{Input: "hello", ConvoID: "c1"})
	if err != nil {
		t.Fatalf("AssembleAndPropose errored on a healthy completer: %v", err)
	}
	if res.Reply != "hi, I'm Shelldon" {
		t.Fatalf("Reply = %q, want the completer's reply", res.Reply)
	}
	if len(res.MemoryOps) != 0 {
		t.Errorf("MemoryOps = %v, want empty — the worker proposes/writes nothing at M0 (AD-6)", res.MemoryOps)
	}

	// The assembled request must carry the owner's input as a user message.
	var sawUserInput bool
	for _, m := range fc.gotReq.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "hello") {
			sawUserInput = true
		}
	}
	if !sawUserInput {
		t.Errorf("assembled request %+v did not carry the owner's input as a user message", fc.gotReq.Messages)
	}
}

// TestAssembleAndPropose_BrokerErrorFlowsOut is AC1's degraded path: a broker
// failure returns from the worker as an error (so the arbiter degrades to a
// reflex ack, Story 2.6) rather than a fabricated reply.
func TestAssembleAndPropose_BrokerErrorFlowsOut(t *testing.T) {
	fc := &fakeCompleter{err: broker.ErrAllProvidersFailed}
	w := monolith.New(fc)

	res, err := w.AssembleAndPropose(context.Background(), contracts.Job{Input: "hello", ConvoID: "c1"})
	if !errors.Is(err, broker.ErrAllProvidersFailed) {
		t.Fatalf("err = %v, want ErrAllProvidersFailed to flow out for reflex degradation", err)
	}
	if res.Reply != "" {
		t.Errorf("Reply = %q, want empty on broker failure (no fabricated reply)", res.Reply)
	}
}

// TestAssembleAndPropose_CancellationPropagates is AC2: the worker threads ctx
// straight into broker.Complete, so cancelling the turn kills the in-flight call
// and AssembleAndPropose returns the context error. (The late-Result-discard
// half of AC2 is the arbiter fence, proven by TestArbiter_TimeoutClosesTurn,
// Story 2.6.)
func TestAssembleAndPropose_CancellationPropagates(t *testing.T) {
	fc := &fakeCompleter{blockUntilCancel: true, observedCancel: make(chan struct{})}
	w := monolith.New(fc)

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, err := w.AssembleAndPropose(ctx, contracts.Job{Input: "hello", ConvoID: "c1"})
		done <- outcome{err: err}
	}()

	cancel()

	select {
	case <-fc.observedCancel:
		// the completer observed the cancellation through the threaded ctx
	case <-time.After(2 * time.Second):
		t.Fatal("completer never observed ctx cancellation — worker did not thread ctx")
	}

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AssembleAndPropose did not return after cancellation")
	}
}

// TestSatisfiesWorkerSeam is the structural interface check: the Monolith+ worker
// must satisfy worker.Worker so the main wiring can't silently break.
func TestSatisfiesWorkerSeam(t *testing.T) {
	var _ worker.Worker = (*monolith.Worker)(nil)
}

// TestAssembleAndPropose_WithContextSource verifies that when a ContextSource is
// injected, AssembleAndPropose inserts a second system message carrying the memory
// block AFTER the persona and BEFORE the user message.
func TestAssembleAndPropose_WithContextSource(t *testing.T) {
	const memBlock = "### OWNER DIRECTIVE (authoritative)\nbe kind"
	fc := &fakeCompleter{resp: broker.Response{Text: "sure!"}}
	src := &fakeContextSource{block: memBlock}

	w := monolith.New(fc, monolith.WithContextSource(src))
	_, err := w.AssembleAndPropose(context.Background(), contracts.Job{Input: "hello", ConvoID: "c1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := fc.gotReq.Messages
	// Expect exactly 3 messages: persona system, memory system, user.
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (persona+memory+user); msgs=%+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" {
		t.Errorf("msgs[0].Role = %q, want system (persona)", msgs[0].Role)
	}
	if msgs[1].Role != "system" || msgs[1].Content != memBlock {
		t.Errorf("msgs[1] = %+v, want system message with memory block", msgs[1])
	}
	if msgs[2].Role != "user" || !strings.Contains(msgs[2].Content, "hello") {
		t.Errorf("msgs[2] = %+v, want user message with owner input", msgs[2])
	}
}

// TestAssembleAndPropose_NoContextSource verifies that without a ContextSource
// the worker sends exactly the persona system message and the user message —
// no extra messages injected.
func TestAssembleAndPropose_NoContextSource(t *testing.T) {
	fc := &fakeCompleter{resp: broker.Response{Text: "hi"}}
	w := monolith.New(fc) // no opts — nil source

	_, err := w.AssembleAndPropose(context.Background(), contracts.Job{Input: "hello", ConvoID: "c1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := fc.gotReq.Messages
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (persona+user); msgs=%+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" {
		t.Errorf("msgs[0].Role = %q, want system", msgs[0].Role)
	}
	if msgs[1].Role != "user" {
		t.Errorf("msgs[1].Role = %q, want user", msgs[1].Role)
	}
}

// TestAssembleAndPropose_ContextSourceError verifies the best-effort contract:
// when ContextSource.PromptContext returns an error, the worker proceeds without
// the memory block and still returns the normal reply (AD-17).
func TestAssembleAndPropose_ContextSourceError(t *testing.T) {
	const cannedReply = "hello there"
	fc := &fakeCompleter{resp: broker.Response{Text: cannedReply}}
	src := &fakeContextSource{err: errors.New("db unavailable")}

	w := monolith.New(fc, monolith.WithContextSource(src))
	res, err := w.AssembleAndPropose(context.Background(), contracts.Job{Input: "hi", ConvoID: "c2"})
	if err != nil {
		t.Fatalf("worker returned error on context-source failure, want best-effort proceed: %v", err)
	}
	if res.Reply != cannedReply {
		t.Errorf("Reply = %q, want %q", res.Reply, cannedReply)
	}

	// Should have sent only persona + user (no memory block).
	msgs := fc.gotReq.Messages
	if len(msgs) != 2 {
		t.Errorf("got %d messages on src error, want 2 (persona+user); msgs=%+v", len(msgs), msgs)
	}
}

// TestAssembleAndPropose_RealMemoryContext is the AC1 integration proof: a real
// memory.Context (sqlite store + curated tree) feeds the worker, so the prompt the
// worker sends includes the owner's DIRECTIVE (authoritative), about.md, and a
// prior recorded message — grounding the reply in durable memory. No network.
func TestAssembleAndPropose_RealMemoryContext(t *testing.T) {
	ctx := context.Background()

	store, err := memory.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Append(ctx, "c1", "owner", "what is your name"); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	root := t.TempDir()
	curated, err := memory.OpenCurated(root)
	if err != nil {
		t.Fatalf("open curated: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "DIRECTIVE.md"), []byte("always be honest"), 0o644); err != nil {
		t.Fatalf("owner DIRECTIVE: %v", err)
	}
	if err := curated.WriteAbout("a small curious shellfish"); err != nil {
		t.Fatalf("write about: %v", err)
	}

	fc := &fakeCompleter{resp: broker.Response{Text: "I'm Shelldon"}}
	w := monolith.New(fc, monolith.WithContextSource(memory.NewContext(store, curated, 10)))

	if _, err := w.AssembleAndPropose(ctx, contracts.Job{Input: "hello", ConvoID: "c1"}); err != nil {
		t.Fatalf("AssembleAndPropose: %v", err)
	}

	// The context system message must carry DIRECTIVE + about + the prior message.
	var ctxMsg string
	for _, m := range fc.gotReq.Messages {
		if m.Role == "system" && strings.Contains(m.Content, "always be honest") {
			ctxMsg = m.Content
		}
	}
	if ctxMsg == "" {
		t.Fatalf("no system message carried the DIRECTIVE; messages=%+v", fc.gotReq.Messages)
	}
	for _, want := range []string{"always be honest", "a small curious shellfish", "what is your name"} {
		if !strings.Contains(ctxMsg, want) {
			t.Errorf("assembled context missing %q; got %q", want, ctxMsg)
		}
	}
}
