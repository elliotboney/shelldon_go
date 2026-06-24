package turntier

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/arbiter"
	"github.com/elliotboney/shelldon_go/core/scheduler"
)

// countingWorker records how many times the worker was actually invoked, so a
// test can prove a gated turn never reaches the worker (AC1). It satisfies
// worker.Worker structurally and is wired behind a real arbiter.
type countingWorker struct {
	calls atomic.Int32
}

func (w *countingWorker) AssembleAndPropose(_ context.Context, _ contracts.Job) (contracts.Result, error) {
	w.calls.Add(1)
	return contracts.Result{}, nil
}

// lowPower is a Power gate that always blocks — modelling the Epic 6 PiSugar2
// reader reporting battery/low charge (NFR14).
type lowPower struct{}

func (lowPower) AllowsTurn() bool { return false }

// replyWorker returns a fixed reply, so a test can observe the result delivered to
// OnResult.
type replyWorker struct{ reply string }

func (w *replyWorker) AssembleAndPropose(_ context.Context, _ contracts.Job) (contracts.Result, error) {
	return contracts.Result{Reply: w.reply}, nil
}

// runGatedJob registers a single turn job (built from cfg, with the given budget
// and power) into a real scheduler, runs it under synctest for elapsed fake time,
// and returns how many times the worker was actually invoked.
func runGatedJob(t *testing.T, cfg Config, budget *Budget, power Power, cadence, cooldown, elapsed time.Duration) int32 {
	t.Helper()
	var got int32
	synctest.Test(t, func(t *testing.T) {
		w := &countingWorker{}
		arb := arbiter.New(w, time.Minute)

		cfg.Cadence = func() time.Duration { return cadence }
		cfg.Cooldown = cooldown
		cfg.Build = func() contracts.Job { return contracts.Job{Input: "ping", ConvoID: "proactive"} }
		cfg.Arbiter = arb
		cfg.Budget = budget
		cfg.Power = power

		s := scheduler.New()
		s.Register(NewJob(cfg).Scheduler())

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx) }()

		time.Sleep(elapsed)
		synctest.Wait()
		cancel()
		<-done

		got = w.calls.Load()
	})
	return got
}

// TestBudgetExhausted_NoWorkerInvocation is AC1: a turn job with a zero daily
// budget is skipped on every cadence — the worker is never invoked.
func TestBudgetExhausted_NoWorkerInvocation(t *testing.T) {
	got := runGatedJob(t, Config{Name: "proactive"}, NewBudget(0), ACPower{},
		1*time.Second, 0, 5*time.Second)
	if got != 0 {
		t.Fatalf("worker invoked %d times with an exhausted budget, want 0 (NFR14/AD-8)", got)
	}
}

// TestCooldownNotElapsed_Skips is AC1: after the first fire, cadences inside the
// cooldown window are skipped — only the first fire reaches the worker.
func TestCooldownNotElapsed_Skips(t *testing.T) {
	got := runGatedJob(t, Config{Name: "proactive"}, NewBudget(100), ACPower{},
		1*time.Second, 10*time.Second, 5*time.Second)
	if got != 1 {
		t.Fatalf("worker invoked %d times, want 1 (cooldown should skip the 2nd–5th fires)", got)
	}
}

// TestBudgetCapsFires is AC1: the daily budget caps invocations regardless of how
// often the cadence comes due.
func TestBudgetCapsFires(t *testing.T) {
	got := runGatedJob(t, Config{Name: "proactive"}, NewBudget(2), ACPower{},
		1*time.Second, 0, 5*time.Second)
	if got != 2 {
		t.Fatalf("worker invoked %d times, want 2 (the daily budget should cap fires)", got)
	}
}

// TestBatteryGate_Skips proves the battery seam (NFR14): a Power gate that blocks
// skips the turn — the worker is never invoked, ahead of the Epic 6 real reader.
func TestBatteryGate_Skips(t *testing.T) {
	got := runGatedJob(t, Config{Name: "proactive"}, NewBudget(100), lowPower{},
		1*time.Second, 0, 5*time.Second)
	if got != 0 {
		t.Fatalf("worker invoked %d times on battery, want 0 (battery gate, NFR14)", got)
	}
}

// TestBudgetResetsDaily proves the daily budget reset: a 1/day budget fires once,
// then again after a day boundary is crossed.
func TestBudgetResetsDaily(t *testing.T) {
	got := runGatedJob(t, Config{Name: "proactive"}, NewBudget(1), ACPower{},
		1*time.Hour, 0, 25*time.Hour)
	if got != 2 {
		t.Fatalf("worker invoked %d times over 25h with a 1/day budget, want 2 (daily reset)", got)
	}
}

// TestNilPowerDefaultsToAC proves a nil Power defaults to ACPower (always allow),
// so a job built without an explicit power gate still fires.
func TestNilPowerDefaultsToAC(t *testing.T) {
	got := runGatedJob(t, Config{Name: "proactive"}, NewBudget(1), nil,
		1*time.Second, 0, 3*time.Second)
	if got != 1 {
		t.Fatalf("worker invoked %d times with nil Power, want 1 (nil should default to ACPower)", got)
	}
}

// TestOnResult_ReceivesResultOnSubmit proves the OnResult sink (Story 3.6): it
// fires with the worker's result when a turn is actually submitted, and is not
// called on a gated/skipped tick. A budget of 1 lets exactly one turn through.
func TestOnResult_ReceivesResultOnSubmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var replies []string
		w := &replyWorker{reply: "from worker"}
		arb := arbiter.New(w, time.Minute)

		s := scheduler.New()
		s.Register(NewJob(Config{
			Name:     "proactive",
			Cadence:  func() time.Duration { return time.Second },
			Cooldown: 0,
			Build:    func() contracts.Job { return contracts.Job{Input: "ping"} },
			Arbiter:  arb,
			Budget:   NewBudget(1),
			OnResult: func(_ context.Context, res contracts.Result, err error) {
				if err == nil {
					replies = append(replies, res.Reply)
				}
			},
		}).Scheduler())

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx) }()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		cancel()
		<-done

		// Budget 1 → exactly one submit → OnResult called once with the worker reply.
		if len(replies) != 1 || replies[0] != "from worker" {
			t.Fatalf("OnResult replies = %v, want exactly one %q (only the budgeted turn submits)", replies, "from worker")
		}
	})
}

// TestBudget_TryConsumeResetsOnDayBoundary unit-tests the budget reset directly,
// independent of the scheduler.
func TestBudget_TryConsumeResetsOnDayBoundary(t *testing.T) {
	b := NewBudget(1)
	day1 := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	if !b.tryConsume(day1) {
		t.Fatal("first consume of the day should succeed")
	}
	if b.tryConsume(day1.Add(2 * time.Hour)) {
		t.Fatal("second consume same day should fail (budget 1)")
	}
	if !b.tryConsume(day1.Add(24 * time.Hour)) {
		t.Fatal("consume next day should succeed after the daily reset")
	}
}
