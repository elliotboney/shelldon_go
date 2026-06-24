package proactive_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/arbiter"
	"github.com/elliotboney/shelldon_go/core/bus"
	"github.com/elliotboney/shelldon_go/core/proactive"
	"github.com/elliotboney/shelldon_go/core/scheduler"
	"github.com/elliotboney/shelldon_go/core/turntier"
)

// replyWorker returns a fixed reply (or error), standing in for the real LLM
// worker so no credit burns.
type replyWorker struct {
	reply string
	err   error
}

func (w *replyWorker) AssembleAndPropose(_ context.Context, _ contracts.Job) (contracts.Result, error) {
	return contracts.Result{Reply: w.reply}, w.err
}

// runProactive wires the real bus + arbiter (over the given worker) + scheduler +
// the proactive job, runs it under synctest for elapsed fake time, and returns
// every OutboundMessage published.
func runProactive(t *testing.T, w *replyWorker, budget *turntier.Budget, cadence, cooldown, elapsed time.Duration) []contracts.OutboundMessage {
	t.Helper()
	var got []contracts.OutboundMessage
	synctest.Test(t, func(t *testing.T) {
		hub := bus.New()
		outbound := make(chan contracts.Envelope, 64)
		if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
			t.Fatalf("register outbound: %v", err)
		}
		arb := arbiter.New(w, time.Minute)

		s := scheduler.New()
		s.Register(proactive.NewJob(hub, arb, budget, turntier.ACPower{}, "owner-1",
			func() time.Duration { return cadence }, cooldown))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx) }()

		time.Sleep(elapsed)
		synctest.Wait()
		cancel()
		<-done

		for {
			select {
			case env := <-outbound:
				if msg, ok := env.Payload.(contracts.OutboundMessage); ok {
					got = append(got, msg)
				}
			default:
				return
			}
		}
	})
	return got
}

// TestProactivePing_InitiatesOutbound is AC1: a proactive job that fires within
// cooldown + budget publishes an outbound message carrying the worker's reply for
// the owner's conversation — the pet messages first, with no inbound.
func TestProactivePing_InitiatesOutbound(t *testing.T) {
	got := runProactive(t, &replyWorker{reply: "hi there"}, turntier.NewBudget(100),
		1*time.Second, 0, 1500*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("published %d outbound messages, want 1 proactive ping", len(got))
	}
	if got[0].Text != "hi there" {
		t.Fatalf("ping text = %q, want the worker's reply %q", got[0].Text, "hi there")
	}
	if got[0].ConvoID != "owner-1" {
		t.Fatalf("ping ConvoID = %q, want the owner's conversation %q", got[0].ConvoID, "owner-1")
	}
}

// TestProactivePing_CooldownSuppresses is AC2: after one ping, cadences inside the
// cooldown window publish nothing — exactly one outbound over the window.
func TestProactivePing_CooldownSuppresses(t *testing.T) {
	got := runProactive(t, &replyWorker{reply: "hi there"}, turntier.NewBudget(100),
		1*time.Second, 10*time.Second, 5*time.Second)
	if len(got) != 1 {
		t.Fatalf("published %d outbound messages over the cooldown window, want exactly 1 (cooldown should suppress the rest)", len(got))
	}
}

// TestProactivePing_QuietOnFailure is the AD-8 degrade: a failed proactive turn
// publishes nothing — the pet stays quiet rather than spamming error placeholders.
func TestProactivePing_QuietOnFailure(t *testing.T) {
	got := runProactive(t, &replyWorker{err: errors.New("provider chain exhausted")}, turntier.NewBudget(100),
		1*time.Second, 0, 3*time.Second)
	if len(got) != 0 {
		t.Fatalf("published %d outbound messages on a failed turn, want 0 (stay quiet, AD-8)", len(got))
	}
}

// TestProactivePing_QuietOnEmptyReply proves an empty reply also publishes nothing.
func TestProactivePing_QuietOnEmptyReply(t *testing.T) {
	got := runProactive(t, &replyWorker{reply: ""}, turntier.NewBudget(100),
		1*time.Second, 0, 3*time.Second)
	if len(got) != 0 {
		t.Fatalf("published %d outbound messages on an empty reply, want 0", len(got))
	}
}
