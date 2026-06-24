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
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/elliotboney/shelldon_go/broker"
	"github.com/elliotboney/shelldon_go/contracts"
)

// systemPrompt is the minimal pet persona seeded ahead of the owner's input.
// DIRECTIVE/about/history assembly (formerly deferred to Epic 4) is now realized
// via the injected ContextSource (read-only): if a source is wired, its block is
// appended as a second system message after the persona and before the user turn.
const systemPrompt = "You are Shelldon, a small, curious digital pet. " +
	"Reply briefly and warmly, in character."

// dreamPrompt instructs the model to review candidate learnings and decide which
// to promote as durable knowledge and which to prune. The response must be a
// strict JSON array — no prose — so the worker can parse it deterministically.
const dreamPrompt = "You are dreaming. Review these candidate learnings and decide which to keep " +
	"(promote) as durable knowledge and which to forget (prune). " +
	`Respond ONLY with a JSON array of objects, each {"pattern_key":"...","action":"promote"|"prune","observation":"..."}. ` +
	"No prose."

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

// AssembleAndPropose assembles a prompt and calls the broker. For a JobDream turn
// it runs the dream flow (introspective review of candidate learnings, proposing
// promote/prune MemoryOps). For any other kind (JobReply / "") it runs the normal
// reply flow.
//
// Reply flow message order:
//  1. system — the Shelldon persona (systemPrompt)
//  2. system — memory context block, if a ContextSource is wired and returns one
//  3. user   — the owner's raw input
//
// Dream flow message order:
//  1. system — dreamPrompt
//  2. user   — turn.Input (the caller formats candidate learnings into it)
//
// ctx is threaded straight into broker.Complete (AC2: a turn timeout or
// supersession cancels the in-flight LLM call). On broker failure the error flows
// out unwrapped so the arbiter degrades to a reflex ack (AD-8, Story 2.6).
func (w *Worker) AssembleAndPropose(ctx context.Context, turn contracts.Job) (contracts.Result, error) {
	if turn.Kind == contracts.JobDream {
		return w.dream(ctx, turn)
	}
	return w.reply(ctx, turn)
}

// reply is the existing normal-turn flow, extracted verbatim from AssembleAndPropose.
func (w *Worker) reply(ctx context.Context, turn contracts.Job) (contracts.Result, error) {
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

// dream runs the introspective dream flow: it asks the model to review candidate
// learnings and decide which to promote (durable knowledge) or prune (forget).
// On any parse failure it logs a warning and returns an empty no-op Result (nil
// error) — a malformed dream must never break the scheduler (AD-17).
func (w *Worker) dream(ctx context.Context, turn contracts.Job) (contracts.Result, error) {
	msgs := []broker.Message{
		{Role: "system", Content: dreamPrompt},
		{Role: "user", Content: turn.Input},
	}

	resp, err := w.c.Complete(ctx, broker.Request{Messages: msgs})
	if err != nil {
		return contracts.Result{}, err
	}

	raw, ok := extractJSONArray(resp.Text)
	if !ok {
		slog.Warn("monolith: AD-17 no-op dream — could not locate JSON array in response",
			"response_len", len(resp.Text))
		return contracts.Result{}, nil
	}

	var decisions []struct {
		PatternKey  string `json:"pattern_key"`
		Action      string `json:"action"`
		Observation string `json:"observation"`
	}
	if err := json.Unmarshal([]byte(raw), &decisions); err != nil {
		slog.Warn("monolith: AD-17 no-op dream — JSON unmarshal failed", "err", err)
		return contracts.Result{}, nil
	}

	ops := make([]contracts.MemoryOp, 0, len(decisions))
	for _, d := range decisions {
		if d.PatternKey == "" {
			continue
		}
		switch d.Action {
		case "promote":
			ops = append(ops, contracts.MemoryOp{
				Kind:        contracts.MemoryOpPromoteLearning,
				PatternKey:  d.PatternKey,
				Observation: d.Observation,
			})
		case "prune":
			ops = append(ops, contracts.MemoryOp{
				Kind:       contracts.MemoryOpPruneLearning,
				PatternKey: d.PatternKey,
			})
		default:
			// unrecognized action — skip silently
		}
	}

	return contracts.Result{MemoryOps: ops}, nil
}

// extractJSONArray locates the first '[' and last ']' in s, returning the
// substring between them (inclusive). It strips markdown code fences before
// searching. Returns ("", false) if no array boundaries are found.
func extractJSONArray(s string) (string, bool) {
	// Strip markdown code fences: ```json ... ``` or ``` ... ```
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "```"); idx != -1 {
		// find content after the opening fence line
		after := s[idx+3:]
		if nl := strings.Index(after, "\n"); nl != -1 {
			after = after[nl+1:]
		}
		// strip closing fence if present
		if end := strings.LastIndex(after, "```"); end != -1 {
			after = after[:end]
		}
		s = strings.TrimSpace(after)
	}

	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start == -1 || end == -1 || end < start {
		return "", false
	}
	return s[start : end+1], true
}
