// Package supervisor makes core the suture/v4 supervisor root (AD-5). Every edge
// (broker, transport, display, plugin-host, …) runs as a supervised Service with
// its own defer recover() + backoff restart, so a single edge panic degrades
// gracefully instead of killing the pet — the soul survives any single edge
// failure. Graceful shutdown drains edges in reverse start order.
//
// recover() does not cross goroutines, so each edge must recover its own panic:
// Guard wraps an edge's work with that mandatory defer recover(). suture's own
// per-goroutine panic recovery is the backstop behind it.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thejerf/suture/v4"
)

// drainTimeout bounds how long shutdown waits for a single stuck edge to stop
// before moving on. Not a tunable invariant.
const drainTimeout = 5 * time.Second

// Root is core's suture supervisor root. It holds edge service tokens in start
// order so shutdown can drain them in reverse.
type Root struct {
	sup    *suture.Supervisor
	tokens []suture.ServiceToken
}

// New builds a supervisor root. Panics and backoffs are logged via slog (AD-17);
// PassThroughPanics is left false so suture recovers and restarts a panicking
// edge as the backstop behind each edge's own recover().
func New(name string) *Root {
	return &Root{sup: suture.New(name, suture.Spec{EventHook: logEvent})}
}

// Add registers an edge Service, recording its token in start order. Edges are
// added before Serve (a fixed M0 startup set; no dynamic add-after-start).
func (r *Root) Add(svc suture.Service) {
	r.tokens = append(r.tokens, r.sup.Add(svc))
}

// Serve runs the supervisor until ctx is cancelled (the signal.NotifyContext
// shutdown signal at main), then drains edges in reverse start order and exits
// cleanly. The supervisor runs under its own context so the reverse drain is
// deterministic: cancelling the external ctx must not let suture stop every edge
// concurrently before the ordered drain runs.
func (r *Root) Serve(ctx context.Context) error {
	supCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := r.sup.ServeBackground(supCtx)

	select {
	case err := <-errCh:
		// Supervisor stopped on its own before the external shutdown signal.
		return supErr(err)
	case <-ctx.Done():
	}

	for i := len(r.tokens) - 1; i >= 0; i-- { // reverse start order
		_ = r.sup.RemoveAndWait(r.tokens[i], drainTimeout)
	}

	cancel()
	return supErr(<-errCh)
}

// supErr normalizes the supervisor's exit: a context-cancelled shutdown is the
// clean path and reports no error.
func supErr(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Guard adapts an edge's work function into a supervised suture.Service whose
// Serve carries the per-edge defer recover() AD-5 mandates. A recovered panic is
// logged (AD-17) and returned as an error, which suture restarts with backoff.
func Guard(name string, serve func(ctx context.Context) error) suture.Service {
	return &guarded{name: name, serve: serve}
}

type guarded struct {
	name  string
	serve func(ctx context.Context) error
}

func (g *guarded) Serve(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("edge recovered from panic", "edge", g.name, "panic", fmt.Sprint(r))
			err = fmt.Errorf("edge %q panicked: %v", g.name, r)
		}
	}()
	return g.serve(ctx)
}

// String names the service for suture's event logging.
func (g *guarded) String() string { return g.name }

// logEvent logs edge panics and supervisor backoffs (AD-17). It is the backstop
// logger for panics suture recovers in a service goroutine.
func logEvent(e suture.Event) {
	switch ev := e.(type) {
	case suture.EventServicePanic:
		slog.Error("edge panicked",
			"service", ev.ServiceName,
			"restarting", ev.Restarting,
			"panic", ev.PanicMsg)
	case suture.EventBackoff:
		slog.Warn("supervisor entering backoff", "supervisor", ev.SupervisorName)
	}
}
