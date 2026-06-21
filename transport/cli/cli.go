// Package cli is the first chat-transport edge actor (AD-12): a bus client that
// reads lines from its input, publishes them as inbound-message envelopes, and
// renders outbound-message envelopes to its output. It speaks only the
// transport-agnostic message contract in contracts/ — no CLI type ever crosses
// into core.
//
// The adapter is run as a supervised edge Service (its Serve method) under the
// suture root (AD-5).
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/bus"
)

// Adapter bridges a line-oriented input/output to the bus.
type Adapter struct {
	hub      *bus.Hub
	outbound <-chan contracts.Envelope
	in       io.Reader
	out      io.Writer
	convoID  string
}

// New returns a CLI adapter. in/out are injected (io.Reader/io.Writer, not
// os.Stdin/os.Stdout) so tests can wire pipes. convoID is the fixed conversation
// id the CLI's single conversation maps to (AD-12).
func New(hub *bus.Hub, outbound <-chan contracts.Envelope, in io.Reader, out io.Writer, convoID string) *Adapter {
	return &Adapter{hub: hub, outbound: outbound, in: in, out: out, convoID: convoID}
}

// Serve renders outbound replies until ctx is cancelled. A background read loop
// publishes inbound messages from in. The blocking Scan on in is not
// ctx-cancelable; on shutdown the read loop ends at EOF / process exit (an M0
// CLI does not need a cancelable stdin).
func (a *Adapter) Serve(ctx context.Context) error {
	go a.readLoop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case env := <-a.outbound:
			if msg, ok := env.Payload.(contracts.OutboundMessage); ok {
				_, _ = fmt.Fprintln(a.out, msg.Text)
			}
		}
	}
}

// readLoop scans lines from in and publishes each as an inbound-message
// envelope. It returns when in reaches EOF.
func (a *Adapter) readLoop() {
	sc := bufio.NewScanner(a.in)
	for sc.Scan() {
		_ = a.hub.Publish(contracts.Envelope{
			Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "cli", Dst: "core"},
			Payload: contracts.InboundMessage{ConvoID: a.convoID, Text: sc.Text()},
		})
	}
}
