// Package scheduler is the core-resident reflex-tier scheduler (AD-13): it runs
// named jobs at independent cadences, in-core, with no worker and no LLM. It is
// tier-shaped — the turn tier (Story 3.5) layers on by registering more jobs
// with no change to this loop; turn jobs encode their arbiter/budget gating
// inside their own Run. The scheduler knows only {name, next-delay, run}.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// minDelay floors every cadence so a job whose NextDelay returns 0 or negative
// cannot busy-loop the scheduler. M1 reflex cadences are seconds-to-hours, so this
// never clamps them; it is insurance for the turn tier (Story 3.5), whose cadences
// are computed from cooldown/budget logic that could evaluate to "fire now".
const minDelay = time.Millisecond

// Job is a tier-agnostic scheduled unit. NextDelay returns the gap to the next
// fire (fixed or jittered); Run does the work. Reflex jobs run in-core; future
// turn jobs (Story 3.5) keep this same shape and gate inside Run.
type Job struct {
	Name      string
	NextDelay func() time.Duration
	Run       func(ctx context.Context)
}

// Scheduler holds the registered jobs and runs each on its own cadence.
type Scheduler struct {
	jobs []Job
}

// New returns an empty scheduler.
func New() *Scheduler { return &Scheduler{} }

// Register appends a job. Jobs are registered before Serve (a fixed startup set,
// like the supervisor's edges); there is no dynamic add-after-start.
func (s *Scheduler) Register(j Job) { s.jobs = append(s.jobs, j) }

// Serve runs every registered job, each in its own goroutine on its own cadence,
// until ctx is cancelled. Independent goroutines give independent cadences. It
// waits for all job goroutines to exit before returning ctx.Err(), so shutdown
// is clean. It is wrapped by supervisor.Guard (AD-5); the per-job recover below
// is the in-goroutine layer Guard cannot provide (recover does not cross
// goroutines).
func (s *Scheduler) Serve(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, j := range s.jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runJob(ctx, j)
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// runJob drives one job's cadence: wait NextDelay, run it, reset, repeat until
// ctx is cancelled.
func (s *Scheduler) runJob(ctx context.Context, j Job) {
	timer := time.NewTimer(nextDelay(j))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.fire(ctx, j)
			timer.Reset(nextDelay(j))
		}
	}
}

// nextDelay returns the job's cadence floored at minDelay, so a 0/negative
// NextDelay can never produce a zero-delay busy-loop.
func nextDelay(j Job) time.Duration {
	if d := j.NextDelay(); d > minDelay {
		return d
	}
	return minDelay
}

// fire runs a single job tick under its own recover so one panicking reflex does
// not kill the scheduler edge or its sibling jobs (AD-5). The panic is logged
// (AD-17); the job continues on its next cadence.
func (s *Scheduler) fire(ctx context.Context, j Job) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("reflex job recovered from panic", "job", j.Name, "panic", fmt.Sprint(r))
		}
	}()
	j.Run(ctx)
}
