package supervisor

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// serviceFunc is a test-only raw suture.Service with NO defer recover(), used
// to prove the supervisor isolates a panic the edge does not handle itself
// (recover() does not cross goroutines). Production edges use Guard instead.
type serviceFunc func(ctx context.Context) error

func (f serviceFunc) Serve(ctx context.Context) error { return f(ctx) }

// steadyEdge starts once and blocks until its context is cancelled. It records
// how many times Serve was entered so a test can prove it was never disturbed.
type steadyEdge struct {
	starts  atomic.Int32
	started chan int32
}

func (e *steadyEdge) Serve(ctx context.Context) error {
	e.started <- e.starts.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

// TestRoot_SoulSurvivesSingleEdgePanic is the required M0 test (AC1): inject a
// panic into one edge Service and assert core and every other edge keep running
// and the panicked edge is restarted. The panic propagates raw to the
// supervisor (no local recover on this path) to prove recover() does not cross
// goroutines — suture must isolate the panic in the service goroutine.
func TestRoot_SoulSurvivesSingleEdgePanic(t *testing.T) {
	steady := &steadyEdge{started: make(chan int32, 4)}

	var flakyStarts atomic.Int32
	flakyStarted := make(chan int32, 8)
	var panicOnce atomic.Bool
	panicOnce.Store(true)
	flaky := serviceFunc(func(ctx context.Context) error {
		flakyStarted <- flakyStarts.Add(1)
		if panicOnce.CompareAndSwap(true, false) {
			panic("injected edge panic")
		}
		<-ctx.Done()
		return ctx.Err()
	})

	root := New("panic-test")
	root.Add(steady)
	root.Add(flaky)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.Serve(ctx) }()

	if n := <-steady.started; n != 1 {
		t.Fatalf("steady first start = %d, want 1", n)
	}
	if n := <-flakyStarted; n != 1 {
		t.Fatalf("flaky first start = %d, want 1", n)
	}
	// Observing the second start proves the supervisor survived the panic and
	// restarted the edge — the soul survived a single edge panic.
	if n := <-flakyStarted; n != 2 {
		t.Fatalf("flaky restart start = %d, want 2 (edge not restarted → soul did not survive)", n)
	}

	// The sibling must not have been disturbed by the flaky edge's panic.
	select {
	case n := <-steady.started:
		t.Fatalf("steady restarted unexpectedly (start %d) — sibling disturbed by flaky panic", n)
	default:
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error on clean shutdown: %v", err)
	}
	if got := flakyStarts.Load(); got < 2 {
		t.Fatalf("flaky starts = %d, want >= 2 (restarted)", got)
	}
	if got := steady.starts.Load(); got != 1 {
		t.Fatalf("steady starts = %d, want 1 (undisturbed)", got)
	}
}

// TestRoot_EdgeRecoversOwnPanicViaGuard exercises AC2: every edge built with
// Guard carries the mandatory per-edge defer recover(). A panic is converted to
// an error inside the edge and suture restarts it on that error.
func TestRoot_EdgeRecoversOwnPanicViaGuard(t *testing.T) {
	var starts atomic.Int32
	started := make(chan int32, 8)
	var panicOnce atomic.Bool
	panicOnce.Store(true)

	edge := Guard("flaky", func(ctx context.Context) error {
		started <- starts.Add(1)
		if panicOnce.CompareAndSwap(true, false) {
			panic("injected panic recovered by Guard")
		}
		<-ctx.Done()
		return ctx.Err()
	})

	root := New("guard-test")
	root.Add(edge)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.Serve(ctx) }()

	if n := <-started; n != 1 {
		t.Fatalf("first start = %d, want 1", n)
	}
	if n := <-started; n != 2 {
		t.Fatalf("restart start = %d, want 2 (Guard did not recover + restart)", n)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error on clean shutdown: %v", err)
	}
}

// stopLog records the order in which edges stop, under a mutex.
type stopLog struct {
	mu    sync.Mutex
	order []string
}

func (s *stopLog) record(name string) {
	s.mu.Lock()
	s.order = append(s.order, name)
	s.mu.Unlock()
}

func (s *stopLog) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

// recorderEdge signals when it starts and records its name when it stops.
type recorderEdge struct {
	name    string
	up      chan struct{}
	stopped *stopLog
}

func (e *recorderEdge) Serve(ctx context.Context) error {
	e.up <- struct{}{}
	<-ctx.Done()
	e.stopped.record(e.name)
	return ctx.Err()
}

// TestRoot_DrainsInReverseStartOrder covers AC3: on shutdown, edges drain in
// reverse start order and Serve exits cleanly.
func TestRoot_DrainsInReverseStartOrder(t *testing.T) {
	log := &stopLog{}
	root := New("drain-test")

	edges := []*recorderEdge{
		{name: "e1", up: make(chan struct{}, 1), stopped: log},
		{name: "e2", up: make(chan struct{}, 1), stopped: log},
		{name: "e3", up: make(chan struct{}, 1), stopped: log},
	}
	for _, e := range edges {
		root.Add(e)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.Serve(ctx) }()

	for _, e := range edges { // wait until all three are running
		<-e.up
	}

	cancel() // trigger graceful drain
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error on drain: %v", err)
	}

	got := log.snapshot()
	want := []string{"e3", "e2", "e1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drain order = %v, want %v (reverse start order)", got, want)
	}
}

// TestRoot_ServeNoEdges covers the composition-root path where no edges are
// wired yet (Story 1.5 adds them): Serve must start, await shutdown, and exit
// cleanly with no edges registered.
func TestRoot_ServeNoEdges(t *testing.T) {
	root := New("empty")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.Serve(ctx) }()

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve with no edges returned error: %v", err)
	}
}
