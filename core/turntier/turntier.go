// Package turntier is the Monolith+ turn tier of the one tier-shaped scheduler
// (AD-13): jobs that cost a worker invocation + LLM call (proactive pings in Story
// 3.6, dreaming in Epic 4), gated so background spend stays bounded (NFR14).
//
// The gating — a per-job cooldown, a shared daily credit/turn budget, and a
// battery-aware Power gate — lives inside a job's Run, so the reflex-tier
// scheduler loop is untouched (Yui's condition): a turn job is just an ordinary
// scheduler.Job. Turns are submitted through the arbiter (≤1 in flight, AD-8); the
// scheduler never invokes the worker directly. This package lives outside the
// reflex-tier fence (core/scheduler, core/reflexes are LLM-free) because a turn
// job must reach the worker via the arbiter.
package turntier

import (
	"context"
	"sync"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/scheduler"
)

// Power is the battery-aware gate seam (AD-13/NFR14). The default ACPower always
// allows; the PiSugar2 plugin (Epic 6, Story 6.3) supplies a real reader that
// throttles on battery/low charge. Until then the pet is treated as plugged in.
type Power interface {
	AllowsTurn() bool
}

// ACPower is the default Power gate: always allow (the pet is on wall power).
type ACPower struct{}

// AllowsTurn always permits a turn under AC power.
func (ACPower) AllowsTurn() bool { return true }

// Submitter is the arbiter seam a turn submits through (≤1 in flight, AD-8).
// *arbiter.Arbiter satisfies it structurally; tests inject a fake or a real
// arbiter over a fake worker.
type Submitter interface {
	Submit(ctx context.Context, turn contracts.Job) (contracts.Result, error)
}

// Budget is the shared daily credit/turn budget (NFR14/AD-8): at most perDay turn
// invocations across all turn jobs in a calendar day, reset at the day boundary.
// Safe for concurrent use — one Budget is shared across all turn jobs.
type Budget struct {
	mu     sync.Mutex
	perDay int
	used   int
	day    int // year*1000+yearDay of the current window
}

// NewBudget returns a Budget allowing perDay turn invocations per calendar day.
func NewBudget(perDay int) *Budget { return &Budget{perDay: perDay} }

// tryConsume reserves one turn for the day containing now: it resets the counter
// at a day boundary, then returns false if the daily budget is exhausted, or
// increments and returns true. now is injected so synctest drives it.
func (b *Budget) tryConsume(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := now.Year()*1000 + now.YearDay()
	if d != b.day {
		b.day = d
		b.used = 0
	}
	if b.used >= b.perDay {
		return false
	}
	b.used++
	return true
}

// Config builds a turn-tier Job. Cadence is how often to consider firing;
// Cooldown is the minimum interval between actual fires; Build produces the turn
// input (proactive/dream specifics are Story 3.6 / Epic 4). Arbiter, Budget, and
// Power are injected; a nil Power defaults to ACPower.
type Config struct {
	Name     string
	Cadence  func() time.Duration
	Cooldown time.Duration
	Build    func() contracts.Job
	Arbiter  Submitter
	Budget   *Budget
	Power    Power
	// OnResult, if set, receives the arbiter's result after a turn is actually
	// submitted (i.e. all gates passed). A reply turn leaves it nil (dispatch owns
	// the reply path); a proactive turn's OnResult publishes the reply as an
	// outbound message (Story 3.6). It is never called on a gated/skipped tick.
	OnResult func(ctx context.Context, res contracts.Result, err error)
}

// Job is a turn-tier scheduled unit. It gates on cooldown → battery → budget
// inside Run, then submits through the arbiter — so the reflex scheduler loop is
// untouched (Yui's condition, AD-13).
type Job struct {
	cfg       Config
	mu        sync.Mutex
	lastFired time.Time
}

// NewJob returns a turn-tier Job from cfg. A nil Power is replaced with ACPower.
func NewJob(cfg Config) *Job {
	if cfg.Power == nil {
		cfg.Power = ACPower{}
	}
	return &Job{cfg: cfg}
}

// Scheduler returns the registrable scheduler.Job: the same {name, next-delay,
// run} shape as a reflex job, so the turn tier layers onto the existing scheduler
// with no loop change.
func (j *Job) Scheduler() scheduler.Job {
	return scheduler.Job{Name: j.cfg.Name, NextDelay: j.cfg.Cadence, Run: j.run}
}

// run applies the gates in order — cooldown, battery, budget — and only if all
// pass submits the turn through the arbiter. Every skip path returns before
// Submit, so a gated turn never invokes the worker (AC1). The submit result flows
// to OnResult when set (a reply turn leaves it nil — dispatch owns that path; a
// proactive turn's OnResult publishes the outbound, Story 3.6).
func (j *Job) run(ctx context.Context) {
	now := time.Now()

	j.mu.Lock()
	if !j.lastFired.IsZero() && now.Sub(j.lastFired) < j.cfg.Cooldown {
		j.mu.Unlock()
		return // cooldown not elapsed
	}
	j.mu.Unlock()

	if !j.cfg.Power.AllowsTurn() {
		return // battery gate
	}
	if !j.cfg.Budget.tryConsume(now) {
		return // daily budget exhausted
	}

	j.mu.Lock()
	j.lastFired = now
	j.mu.Unlock()

	res, err := j.cfg.Arbiter.Submit(ctx, j.cfg.Build())
	if j.cfg.OnResult != nil {
		j.cfg.OnResult(ctx, res, err)
	}
}
