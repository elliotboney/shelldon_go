package monolith_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/elliotboney/shelldon_go/broker"
	"github.com/elliotboney/shelldon_go/contracts"
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
