// Command shelldon is the single supervised process: it stands up the core
// suture supervisor root and runs until a shutdown signal arrives, draining
// edges in reverse start order (AD-5).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/elliotboney/shelldon_go/core/supervisor"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := supervisor.New("shelldon")
	// Edges (CLI transport, broker, display, …) are wired here starting in Story 1.5.

	if err := root.Serve(ctx); err != nil {
		slog.Error("supervisor exited with error", "err", err)
		os.Exit(1)
	}
}
