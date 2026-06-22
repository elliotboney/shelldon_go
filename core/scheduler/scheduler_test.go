package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestServe_IndependentCadences is AC1: two jobs at different fixed cadences each
// fire floor(elapsed / cadence) times, independently, under the fake clock.
func TestServe_IndependentCadences(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const fastCadence = 2 * time.Second
		const slowCadence = 5 * time.Second
		var fast, slow atomic.Int32

		s := New()
		s.Register(Job{
			Name:      "fast",
			NextDelay: func() time.Duration { return fastCadence },
			Run:       func(context.Context) { fast.Add(1) },
		})
		s.Register(Job{
			Name:      "slow",
			NextDelay: func() time.Duration { return slowCadence },
			Run:       func(context.Context) { slow.Add(1) },
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx) }()

		elapsed := 21 * time.Second
		time.Sleep(elapsed)
		synctest.Wait()
		cancel()
		<-done

		if want := int32(elapsed / fastCadence); fast.Load() != want {
			t.Errorf("fast job fired %d times, want %d", fast.Load(), want)
		}
		if want := int32(elapsed / slowCadence); slow.Load() != want {
			t.Errorf("slow job fired %d times, want %d", slow.Load(), want)
		}
	})
}

// TestServe_PanicIsolation proves per-job recover (AD-5): a job whose Run panics
// every tick does not stop the scheduler — a sibling job keeps firing on its own
// cadence.
func TestServe_PanicIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cadence = 2 * time.Second
		var survivor atomic.Int32

		s := New()
		s.Register(Job{
			Name:      "panicky",
			NextDelay: func() time.Duration { return cadence },
			Run:       func(context.Context) { panic("reflex blew up") },
		})
		s.Register(Job{
			Name:      "survivor",
			NextDelay: func() time.Duration { return cadence },
			Run:       func(context.Context) { survivor.Add(1) },
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx) }()

		elapsed := 10 * time.Second
		time.Sleep(elapsed)
		synctest.Wait()
		cancel()
		<-done

		if want := int32(elapsed / cadence); survivor.Load() != want {
			t.Errorf("survivor fired %d times despite sibling panicking, want %d", survivor.Load(), want)
		}
	})
}

// TestNextDelay_FlooredAtMinDelay proves a job whose NextDelay returns 0 (or
// negative) is clamped to minDelay, so it cannot busy-loop the scheduler.
func TestNextDelay_FlooredAtMinDelay(t *testing.T) {
	cases := []time.Duration{0, -time.Second}
	for _, d := range cases {
		j := Job{NextDelay: func() time.Duration { return d }}
		if got := nextDelay(j); got != minDelay {
			t.Errorf("nextDelay for NextDelay()==%v = %v, want floored to %v", d, got, minDelay)
		}
	}
	// A real cadence above the floor is returned unchanged.
	j := Job{NextDelay: func() time.Duration { return time.Hour }}
	if got := nextDelay(j); got != time.Hour {
		t.Errorf("nextDelay clamped a real cadence: got %v, want %v", got, time.Hour)
	}
}

// TestServe_ReturnsOnCancel proves Serve joins all job goroutines and returns the
// cancellation cause, so the supervised edge shuts down cleanly.
func TestServe_ReturnsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New()
		s.Register(Job{
			Name:      "noop",
			NextDelay: func() time.Duration { return time.Second },
			Run:       func(context.Context) {},
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx) }()

		cancel()
		synctest.Wait()
		if err := <-done; err != context.Canceled {
			t.Fatalf("Serve returned %v, want context.Canceled", err)
		}
	})
}
