// Package dispatch is the core-resident turn-dispatch loop. It consumes
// inbound-message envelopes from the bus, runs each turn through the arbiter
// (≤1 in flight, AD-8), and publishes the worker's reply as an outbound-message
// envelope. It is the in-core glue between the bus and the arbiter.
//
// It imports contracts, core/bus, core/arbiter, core/state, and worker — never a
// transport adapter (AD-12; enforced by imports_test.go). Core sees only the
// transport-agnostic message contract.
package dispatch

import (
	"context"
	"log/slog"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/arbiter"
	"github.com/elliotboney/shelldon_go/core/bus"
	"github.com/elliotboney/shelldon_go/core/state"
)

// Recorder is the narrow write interface that dispatch uses to persist each
// conversation turn. *memory.Store satisfies it. Defined here (consumer-defined
// interface, AD-6) so dispatch.go does not import core/memory.
type Recorder interface {
	Append(ctx context.Context, convoID, role, content string) (int64, error)
}

// Option is a functional option for Dispatcher, used to extend New without
// breaking existing callers.
type Option func(*Dispatcher)

// WithRecorder wires an optional memory recorder into the dispatcher. When set,
// each completed turn is persisted (owner message then pet reply) after the reply
// is published. Errors are ignored — recording is best-effort and must not stall
// the loop.
func WithRecorder(r Recorder) Option {
	return func(d *Dispatcher) { d.recorder = r }
}

// Dispatcher routes inbound messages to turns and emits the replies.
type Dispatcher struct {
	hub      *bus.Hub
	arb      *arbiter.Arbiter
	inbound  <-chan contracts.Envelope
	store    *state.Store
	recorder Recorder // nil when unset; best-effort memory writer (AD-6)
}

// New returns a Dispatcher consuming inbound from the given channel and
// publishing outbound replies through hub. store is stamped on each inbound
// message so the idle reflex (Story 2.3) knows when the owner last interacted.
// Existing callers that pass no opts continue to work unchanged.
func New(hub *bus.Hub, arb *arbiter.Arbiter, inbound <-chan contracts.Envelope, store *state.Store, opts ...Option) *Dispatcher {
	d := &Dispatcher{hub: hub, arb: arb, inbound: inbound, store: store}
	for _, o := range opts {
		o(d)
	}
	return d
}

// reflexAck is the canned, in-core acknowledgement published when the brain
// cannot answer a message — busy (ErrTurnInFlight), timed out (ErrTurnTimeout),
// or (Epic 3) provider-exhausted. It uses no worker and no LLM, so the pet stays
// responsive offline (NFR13). Tunable story-time config, not an invariant.
const reflexAck = "…"

// Serve runs the dispatch loop until ctx is cancelled. For each inbound message
// it submits a turn to the arbiter and publishes the reply. A turn the brain
// cannot complete — busy, timed out, or worker error — degrades to a canned
// reflex acknowledgement (NFR13/AD-8) instead of being dropped, and the loop
// keeps consuming inbound: the inbound path never blocks. Only a parent-context
// cancellation (shutdown) ends the loop.
//
// Header.ID/TurnID are left zero for M0: the hub routes by Kind, and envelope-id
// minting + turn_id fencing arrive with the turn lifecycle (AD-11).
func (d *Dispatcher) Serve(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case env := <-d.inbound:
			msg, ok := env.Payload.(contracts.InboundMessage)
			if !ok {
				continue // defensive: wrong payload for this kind
			}
			d.store.Touch() // reset idleness so ambient blinking pauses (Story 2.3)
			res, err := d.arb.Submit(ctx, contracts.Job{Input: msg.Text, ConvoID: msg.ConvoID})

			var reply string
			switch {
			case err == nil:
				reply = res.Reply
				d.publishReply(ctx, msg.ConvoID, reply)
			case ctx.Err() != nil:
				return ctx.Err() // shutdown, not a brain failure — do not ack or record
			default:
				reply = reflexAck
				d.publishReply(ctx, msg.ConvoID, reply) // busy / timeout / brain absent: stay alive
			}

			// Record the turn after publishing so the next turn's recent window
			// includes it. Best-effort: a record failure is logged (AD-17) but never
			// stalls the loop (AD-6). The reflex ack ("…") is NOT recorded — it would
			// pollute the recent window the next prompt reads; only a real worker
			// reply (err == nil) is durable conversation history.
			if d.recorder != nil {
				if _, aerr := d.recorder.Append(ctx, msg.ConvoID, "owner", msg.Text); aerr != nil {
					slog.Warn("dispatch: record owner message failed", "convo_id", msg.ConvoID, "err", aerr)
				}
				if err == nil {
					if _, aerr := d.recorder.Append(ctx, msg.ConvoID, "pet", reply); aerr != nil {
						slog.Warn("dispatch: record reply failed", "convo_id", msg.ConvoID, "err", aerr)
					}
				}
			}
		}
	}
}

// publishReply sends one outbound message for convoID. Both the worker reply and
// the reflex acknowledgement share it.
func (d *Dispatcher) publishReply(ctx context.Context, convoID, text string) {
	_ = d.hub.PublishContext(ctx, contracts.Envelope{
		Header:  contracts.Header{Kind: contracts.KindOutboundMessage, Src: "core", Dst: "cli"},
		Payload: contracts.OutboundMessage{ConvoID: convoID, Text: text},
	})
}
