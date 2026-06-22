package reflexes

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/state"
)

const epsilon = 1e-9

// TestMoodDrift_AccumulatesAndCheckpoints is AC1+AC2: over a simulated week the
// valence drifts by step×cadences and the drift is persisted to the checkpoint.
func TestMoodDrift_AccumulatesAndCheckpoints(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		store := state.New(state.Personality{Mood: 0, Energy: 1, LastInteraction: time.Now()}, path)
		md := NewMoodDrift(store)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- md.Serve(ctx) }()

		sleepDur := 7*24*time.Hour + time.Hour
		time.Sleep(sleepDur)
		synctest.Wait()
		cancel()
		<-done

		ticks := int(sleepDur / moodDriftInterval)
		want := 0.0
		for i := 0; i < ticks; i++ {
			want = clamp(want+moodDriftStep, moodValenceMin, moodValenceMax)
		}

		if got := store.Snapshot().Mood; math.Abs(got-want) > epsilon {
			t.Fatalf("RAM mood = %v, want %v (step %v × %d cadences)", got, want, moodDriftStep, ticks)
		}
		// AC2 before/after: the mood must have actually moved.
		if math.Abs(store.Snapshot().Mood) < epsilon {
			t.Fatal("mood did not drift over a simulated week")
		}
		// AC1: the drift is persisted to the checkpoint, not just RAM.
		if got := state.Load(path).Mood; math.Abs(got-want) > epsilon {
			t.Fatalf("checkpointed mood = %v, want %v", got, want)
		}
	})
}

// TestMoodDrift_ClampsValence proves valence never leaves [min, max] no matter how
// long it drifts.
func TestMoodDrift_ClampsValence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		// Start at the bound OPPOSITE the drift direction so the drift travels the
		// full range and slams into the far clamp (a negative step must start at
		// max to descend into the min clamp).
		start := moodValenceMin
		if moodDriftStep < 0 {
			start = moodValenceMax
		}
		store := state.New(state.Personality{Mood: start, LastInteraction: time.Now()}, path)
		md := NewMoodDrift(store)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- md.Serve(ctx) }()
		time.Sleep(30 * 24 * time.Hour) // a month of drift
		synctest.Wait()
		cancel()
		<-done

		got := store.Snapshot().Mood
		if got < moodValenceMin || got > moodValenceMax {
			t.Fatalf("mood %v escaped clamp [%v, %v]", got, moodValenceMin, moodValenceMax)
		}
	})
}

// TestExpressionFor maps valence bands to the right face expression.
func TestExpressionFor(t *testing.T) {
	cases := []struct {
		mood float64
		want contracts.Expression
	}{
		{-1.0, contracts.ExpressionSad},
		{moodSadThreshold, contracts.ExpressionSad},
		{0.0, contracts.ExpressionNeutral},
		{moodHappyThreshold, contracts.ExpressionHappy},
		{1.0, contracts.ExpressionHappy},
	}
	for _, c := range cases {
		if got := expressionFor(c.mood); got != c.want {
			t.Errorf("expressionFor(%v) = %q, want %q", c.mood, got, c.want)
		}
	}
}
