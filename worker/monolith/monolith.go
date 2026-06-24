// Package monolith is the Monolith+ implementation of the AD-2 Worker seam for
// M0–M2: a real LLM-backed turn executor that assembles a prompt, calls the
// broker, and proposes a Result. It satisfies worker.Worker structurally — it
// imports contracts + broker, not worker — so core never transitively pulls a
// provider SDK into its import graph (AD-1); only main wires this edge in.
//
// The worker is a plain struct. Its isolation (own goroutine, context timeout,
// recover, late-Result fence) comes from the arbiter's Submit (Story 2.6); this
// package adds no supervised edge. The worker holds no store/memory reference, so
// it structurally cannot write — it only proposes via Result.MemoryOps (AD-6).
// Memory context is read at turn time via an injected ContextSource (read-only
// seam, AD-6 intact); the worker never writes through that interface.
package monolith

import (
	"context"
	"log/slog"

	"github.com/elliotboney/shelldon_go/broker"
	"github.com/elliotboney/shelldon_go/contracts"
)

// systemPrompt is the minimal pet persona seeded ahead of the owner's input.
// DIRECTIVE/about/history assembly (formerly deferred to Epic 4) is now realized
// via the injected ContextSource (read-only): if a source is wired, its block is
// appended as a second system message after the persona and before the user turn.
const systemPrompt = "You are Shelldon, a small, curious digital pet. " +
	"Reply briefly and warmly, in character."

// Completer is the narrow broker dependency the worker needs: a single egress
// method. *broker.Broker satisfies it structurally, and tests inject a fake, so
// the worker stays agnostic to broker internals and testable without a network.
type Completer interface {
	Complete(ctx context.Context, req broker.Request) (broker.Response, error)
}

// ContextSource is the read-only memory seam injected into the worker. It
// returns the assembled memory context block (DIRECTIVE + about + recent window)
// for the given conversation, or "" when none is available. The worker reads it
// read-only (AD-6) and never writes through this interface.
type ContextSource interface {
	PromptContext(ctx context.Context, convoID string) (string, error)
}

// Option is a functional option for configuring a Worker at construction time.
type Option func(*Worker)

// WithContextSource wires a ContextSource into the Worker so AssembleAndPropose
// injects memory context on each turn (best-effort — a read error must not fail
// the reply).
func WithContextSource(src ContextSource) Option {
	return func(w *Worker) { w.src = src }
}

// Worker is the Monolith+ turn executor behind the seam. It holds only a
// Completer and an optional ContextSource — no store, no writeable memory — so
// AD-6 ("never writes") is structural.
type Worker struct {
	c   Completer
	src ContextSource
}

// New builds a Worker over the given completer (a *broker.Broker in production).
// Existing callers that pass only a Completer continue to work unchanged; opts
// are applied in order after the required completer is set.
func New(c Completer, opts ...Option) *Worker {
	w := &Worker{c: c}
	for _, o := range opts {
		o(w)
	}
	return w
}

// AssembleAndPropose assembles a prompt and calls the broker. The message order is:
//
//  1. system — the Shelldon persona (systemPrompt)
//  2. system — memory context block, if a ContextSource is wired and returns one
//  3. user   — the owner's raw input
//
// ctx is threaded straight into broker.Complete (AC2: a turn timeout or
// supersession cancels the in-flight LLM call). On broker failure the error flows
// out unwrapped so the arbiter degrades to a reflex ack (AD-8, Story 2.6).
// MemoryOps stays empty until Epic 4.
func (w *Worker) AssembleAndPropose(ctx context.Context, turn contracts.Job) (contracts.Result, error) {
	msgs := []broker.Message{
		{Role: "system", Content: systemPrompt},
	}

	if w.src != nil {
		s, err := w.src.PromptContext(ctx, turn.ConvoID)
		switch {
		case err != nil:
			// best-effort: memory context augments; a read error must not fail the
			// reply — log it and reply without context (AD-17).
			slog.Warn("monolith: prompt context unavailable; replying without memory", "err", err)
		case s != "":
			msgs = append(msgs, broker.Message{Role: "system", Content: s})
		}
	}

	msgs = append(msgs, broker.Message{Role: "user", Content: turn.Input})

	req := broker.Request{Messages: msgs}
	resp, err := w.c.Complete(ctx, req)
	if err != nil {
		return contracts.Result{}, err
	}
	return contracts.Result{Reply: resp.Text}, nil
}
