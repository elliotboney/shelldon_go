package privsep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/elliotboney/shelldon_go/worker"
)

// Wire/handshake constants shared by parent and child.
const (
	// childFD is the inherited socket fd in the child: stdin/out/err are 0/1/2,
	// so the first ExtraFiles entry the parent passes lands at fd 3.
	childFD = 3
	// childSentinelEnv/Val route main into the child worker loop instead of the
	// normal supervised process. The parent sets this on the re-exec'd command.
	childSentinelEnv = "SHELLDON_WORKER_CHILD"
	childSentinelVal = "1"
)

// IsChild reports whether this process was launched as a privsep worker child
// (the sentinel env is set). main checks this first and, if true, runs ChildMain
// and exits — the child never builds the bus, scheduler, or transport.
func IsChild() bool { return os.Getenv(childSentinelEnv) == childSentinelVal }

// ChildMain is the subprocess entry point. It adopts the inherited socket at
// fd 3, then serves the parent by running inner against each Job it receives.
// inner is injected by main (the brain that runs behind the wall); this package
// holds no worker of its own and no credentials. It returns nil on an orderly
// parent shutdown (the parent closed the connection).
func ChildMain(ctx context.Context, inner worker.Worker) error {
	f := os.NewFile(childFD, "privsep-conn")
	if f == nil {
		return fmt.Errorf("privsep child: no inherited socket at fd %d", childFD)
	}
	defer func() { _ = f.Close() }()

	conn, err := net.FileConn(f)
	if err != nil {
		return fmt.Errorf("privsep child: adopt inherited socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	return runChild(ctx, conn, inner)
}

// runChild is the sequential serve loop: decode a Job frame, run the inner
// worker, encode the Result frame back (with any inner error flattened to a
// string). One turn at a time, matching the arbiter's ≤1-in-flight bound (AD-8).
// A clean EOF (parent closed the connection) ends the loop without error.
func runChild(ctx context.Context, conn net.Conn, inner worker.Worker) error {
	for {
		var jf jobFrame
		if err := decodeFrame(conn, &jf); err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
				return nil // parent closed: orderly shutdown
			}
			return fmt.Errorf("privsep child: decode job: %w", err)
		}

		res, aerr := inner.AssembleAndPropose(ctx, jf.Job)
		rf := resultFrame{Result: res}
		if aerr != nil {
			rf.Err = aerr.Error()
		}
		if err := encodeFrame(conn, rf); err != nil {
			return fmt.Errorf("privsep child: encode result: %w", err)
		}

		if err := ctx.Err(); err != nil {
			return err // parent ctx cancelled (shutdown)
		}
	}
}
