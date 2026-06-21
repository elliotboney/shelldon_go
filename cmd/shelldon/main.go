// Command shelldon is the single supervised process: it wires the bus, arbiter,
// worker stub, core dispatch loop, and CLI transport adapter, then runs them as
// supervised edges under the core suture root until a shutdown signal arrives,
// draining edges in reverse start order (AD-5).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/arbiter"
	"github.com/elliotboney/shelldon_go/core/bus"
	"github.com/elliotboney/shelldon_go/core/dispatch"
	"github.com/elliotboney/shelldon_go/core/supervisor"
	"github.com/elliotboney/shelldon_go/transport/cli"
	"github.com/elliotboney/shelldon_go/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := bus.New()
	arb := arbiter.New(worker.Stub{})

	inbound := make(chan contracts.Envelope, 16)
	outbound := make(chan contracts.Envelope, 16)
	if err := hub.Register(contracts.KindInboundMessage, inbound); err != nil {
		slog.Error("register inbound route", "err", err)
		os.Exit(1)
	}
	if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
		slog.Error("register outbound route", "err", err)
		os.Exit(1)
	}

	disp := dispatch.New(hub, arb, inbound)
	adapter := cli.New(hub, outbound, os.Stdin, os.Stdout, "cli")

	root := supervisor.New("shelldon")
	// Start order: dispatch first, CLI second → reverse drain stops CLI, then dispatch.
	root.Add(supervisor.Guard("core-dispatch", disp.Serve))
	root.Add(supervisor.Guard("cli-transport", adapter.Serve))

	if err := root.Serve(ctx); err != nil {
		slog.Error("supervisor exited with error", "err", err)
		os.Exit(1)
	}
}
