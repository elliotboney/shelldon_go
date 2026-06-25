package privsep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/worker"
)

// closeGrace bounds how long Close waits for the child to exit on its own after
// the connection is closed before it is killed.
const closeGrace = 2 * time.Second

// Worker is the Privsep-lite parent: it owns a recycled worker subprocess and
// speaks the length-prefixed gob wire to it. It satisfies worker.Worker, so it
// drops into arbiter.New in place of the Monolith+ goroutine worker with no
// caller change (AD-2/AD-4). It holds no credentials: the child hosts the inner
// worker (injected by main via ChildMain).
type Worker struct {
	// command builds the child process command (a re-exec of this binary with the
	// sentinel env). Injectable so tests can re-exec the test binary.
	command func() (*exec.Cmd, error)
	uid     int // child uid; 0 = no drop (run same-uid)
	gid     int

	mu      sync.Mutex // serializes turns + lifecycle; the wire is one frame-pair at a time
	started bool
	cmd     *exec.Cmd
	conn    net.Conn
}

var _ worker.Worker = (*Worker)(nil)

// Option configures a Worker at construction.
type Option func(*Worker)

// WithUID sets the uid/gid the child subprocess drops to (Linux + root only; see
// applyCredential). Zero means no drop — the child runs same-uid, which still
// exercises the full transport (the OS-enforced isolation proof is Story 5.2).
func WithUID(uid, gid int) Option {
	return func(w *Worker) { w.uid, w.gid = uid, gid }
}

// New builds a privsep Worker. The subprocess is started lazily on the first
// AssembleAndPropose so a construction-time failure does not crash boot — a start
// error surfaces as a turn error the arbiter degrades from (AD-8).
func New(opts ...Option) *Worker {
	w := &Worker{command: defaultChildCommand}
	for _, o := range opts {
		o(w)
	}
	return w
}

// defaultChildCommand re-execs this binary with the sentinel env set so the child
// runs the worker loop (IsChild → ChildMain) instead of the normal process. The
// child inherits stdio so its logs surface in the parent's journal.
func defaultChildCommand() (*exec.Cmd, error) {
	path, err := execPath()
	if err != nil {
		return nil, fmt.Errorf("privsep: resolve exec path: %w", err)
	}
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), childSentinelEnv+"="+childSentinelVal)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd, nil
}

// AssembleAndPropose frames turn to the child, then awaits the Result while racing
// ctx cancellation. On any wire error or cancellation the child is torn down so a
// half-consumed frame cannot desync the recycled stream; the next turn lazily
// respawns it. A child-side inner error is rehydrated into the error return so the
// arbiter degrades to a reflex ack (AD-8). The mutex makes turns strictly
// sequential, matching ≤1-in-flight (AD-8) and the single-frame-pair wire.
func (w *Worker) AssembleAndPropose(ctx context.Context, turn contracts.Job) (contracts.Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureStartedLocked(); err != nil {
		return contracts.Result{}, err
	}

	if err := encodeFrame(w.conn, jobFrame{Job: turn}); err != nil {
		w.teardownLocked()
		return contracts.Result{}, err
	}

	type outcome struct {
		rf  resultFrame
		err error
	}
	done := make(chan outcome, 1) // buffered: a late decode after teardown is dropped, not leaked
	go func() {
		var rf resultFrame
		err := decodeFrame(w.conn, &rf)
		done <- outcome{rf, err}
	}()

	select {
	case <-ctx.Done():
		w.teardownLocked() // cancel/timeout mid-turn: kill the child so the next turn starts clean
		return contracts.Result{}, ctx.Err()
	case o := <-done:
		if o.err != nil {
			w.teardownLocked()
			return contracts.Result{}, fmt.Errorf("privsep: read result: %w", o.err)
		}
		if o.rf.Err != "" {
			return o.rf.Result, errors.New(o.rf.Err) // rehydrated child-side inner error
		}
		return o.rf.Result, nil
	}
}

// ensureStartedLocked starts the child if it is not running: it creates a
// connected UDS pair, hands one end to the child via ExtraFiles (fd 3), applies
// the uid-drop when configured, and keeps the other end. Caller holds w.mu.
func (w *Worker) ensureStartedLocked() error {
	if w.started {
		return nil
	}

	parentEnd, childEnd, err := socketpair()
	if err != nil {
		return fmt.Errorf("privsep: socketpair: %w", err)
	}
	// Both ends are dup-managed below; ensure no leak on any early return.
	defer func() { _ = childEnd.Close() }()

	cmd, err := w.command()
	if err != nil {
		_ = parentEnd.Close()
		return err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, childEnd) // → fd 3 in the child
	applyCredential(cmd, w.uid, w.gid)

	if err := cmd.Start(); err != nil {
		_ = parentEnd.Close()
		return fmt.Errorf("privsep: start child: %w", err)
	}

	conn, err := net.FileConn(parentEnd) // dups the fd; parentEnd is closed next
	_ = parentEnd.Close()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("privsep: adopt parent socket: %w", err)
	}

	w.cmd, w.conn, w.started = cmd, conn, true
	return nil
}

// teardownLocked force-kills the child and drops the connection so a desynced or
// cancelled turn leaves no half-open state. The next AssembleAndPropose respawns.
// Caller holds w.mu.
func (w *Worker) teardownLocked() {
	if w.conn != nil {
		_ = w.conn.Close()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
		_ = w.cmd.Wait()
	}
	w.conn, w.cmd, w.started = nil, nil, false
}

// Close shuts the child down gracefully for supervised drain (AD-5): closing the
// connection makes the child's loop see EOF and exit; it is killed only if it does
// not exit within closeGrace. Safe to call when no child is running.
func (w *Worker) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.started {
		return nil
	}
	if w.conn != nil {
		_ = w.conn.Close() // child sees EOF → returns nil → exits
	}

	waited := make(chan error, 1)
	go func() { waited <- w.cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(closeGrace):
		_ = w.cmd.Process.Kill()
		<-waited
	}

	w.conn, w.cmd, w.started = nil, nil, false
	return nil
}

// socketpair returns the two ends of a connected AF_UNIX stream socket as *os.File
// (available on Linux and darwin). One end is kept by the parent, the other is
// inherited by the child via ExtraFiles.
func socketpair() (parent, child *os.File, err error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	parent = os.NewFile(uintptr(fds[0]), "privsep-parent")
	child = os.NewFile(uintptr(fds[1]), "privsep-child")
	if parent == nil || child == nil {
		return nil, nil, fmt.Errorf("privsep: NewFile returned nil for socketpair fds")
	}
	return parent, child, nil
}

// logSameUID is used by the credential helpers to note a same-uid fallback once at
// start (AD-17), keeping the platform-specific files terse.
func logSameUID(reason string, uid int) {
	slog.Warn("privsep: child runs same-uid (transport-only)", "reason", reason, "requested_uid", uid)
}
