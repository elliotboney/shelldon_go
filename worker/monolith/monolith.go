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
package monolith

import (
	"context"

	"github.com/elliotboney/shelldon_go/broker"
	"github.com/elliotboney/shelldon_go/contracts"
)

// systemPrompt is the minimal pet persona seeded ahead of the owner's input.
// DIRECTIVE/about/history assembly is Epic 4; this is tunable story-time config,
// not a spine invariant.
const systemPrompt = "You are Shelldon, a small, curious digital pet. " +
	"Reply briefly and warmly, in character."

// Completer is the narrow broker dependency the worker needs: a single egress
// method. *broker.Broker satisfies it structurally, and tests inject a fake, so
// the worker stays agnostic to broker internals and testable without a network.
type Completer interface {
	Complete(ctx context.Context, req broker.Request) (broker.Response, error)
}

// Worker is the Monolith+ turn executor behind the seam. It holds only a
// Completer — no store, no memory — so AD-6 ("never writes") is structural.
type Worker struct {
	c Completer
}

// New builds a Worker over the given completer (a *broker.Broker in production).
func New(c Completer) *Worker { return &Worker{c: c} }

// AssembleAndPropose assembles a minimal prompt (system persona + the owner's
// input), calls the broker threading ctx straight through (AC2: a turn timeout or
// supersession cancels the in-flight LLM call), and proposes the reply. On broker
// failure the error flows out unwrapped so the arbiter degrades to a reflex ack
// (AD-8, Story 2.6). MemoryOps stays empty until Epic 4.
func (w *Worker) AssembleAndPropose(ctx context.Context, turn contracts.Job) (contracts.Result, error) {
	req := broker.Request{
		Messages: []broker.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: turn.Input},
		},
	}
	resp, err := w.c.Complete(ctx, req)
	if err != nil {
		return contracts.Result{}, err
	}
	return contracts.Result{Reply: resp.Text}, nil
}
