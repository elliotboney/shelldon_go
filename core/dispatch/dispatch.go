// Package dispatch is the core-resident turn-dispatch loop. It consumes
// inbound-message envelopes from the bus, runs each turn through the arbiter
// (≤1 in flight, AD-8), and publishes the worker's reply as an outbound-message
// envelope. It is the in-core glue between the bus and the arbiter.
//
// It imports contracts, core/bus, core/arbiter, and worker — never a transport
// adapter (AD-12; enforced by imports_test.go). Core sees only the transport-
// agnostic message contract.
package dispatch

import (
	"context"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/arbiter"
	"github.com/elliotboney/shelldon_go/core/bus"
)

// Dispatcher routes inbound messages to turns and emits the replies.
type Dispatcher struct {
	hub     *bus.Hub
	arb     *arbiter.Arbiter
	inbound <-chan contracts.Envelope
}

// New returns a Dispatcher consuming inbound from the given channel and
// publishing outbound replies through hub.
func New(hub *bus.Hub, arb *arbiter.Arbiter, inbound <-chan contracts.Envelope) *Dispatcher {
	return &Dispatcher{hub: hub, arb: arb, inbound: inbound}
}

// Serve runs the dispatch loop until ctx is cancelled. For each inbound message
// it submits a turn to the arbiter and publishes the reply. A turn rejected
// (ErrTurnInFlight) or a cancelled submit is skipped — the busy/offline
// acknowledgement is Story 2.6 and turn_id fencing is AD-11 (both deferred).
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
			res, err := d.arb.Submit(ctx, contracts.Job{Input: msg.Text, ConvoID: msg.ConvoID})
			if err != nil {
				continue // ErrTurnInFlight / cancelled: busy-ack is Story 2.6
			}
			_ = d.hub.Publish(contracts.Envelope{
				Header:  contracts.Header{Kind: contracts.KindOutboundMessage, Src: "core", Dst: "cli"},
				Payload: contracts.OutboundMessage{ConvoID: msg.ConvoID, Text: res.Reply},
			})
		}
	}
}
