package reflexes

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/state"
)

const epsilon = 1e-9

// TestMoodDrift_AccumulatesAndCheckpoints is AC2 (LLM-free in-core drift): N Run
// ticks move valence by step×N and the drift is persisted to the checkpoint. The
// "fires N times over a week" cadence assertion now lives in the scheduler test;
// this verifies the per-tick work the scheduler drives.
func TestMoodDrift_AccumulatesAndCheckpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := state.New(state.Personality{Mood: 0, Energy: 1}, path)
	md := NewMoodDrift(store)

	const ticks = 28 // a simulated week at four drifts per day
	want := 0.0
	for i := 0; i < ticks; i++ {
		md.Run(context.Background())
		want = clamp(want+moodDriftStep, moodValenceMin, moodValenceMax)
	}

	if got := store.Snapshot().Mood; math.Abs(got-want) > epsilon {
		t.Fatalf("RAM mood = %v, want %v (step %v × %d ticks)", got, want, moodDriftStep, ticks)
	}
	// The mood must have actually moved.
	if math.Abs(store.Snapshot().Mood) < epsilon {
		t.Fatal("mood did not drift over N ticks")
	}
	// The drift is persisted to the checkpoint, not just RAM.
	if got := state.Load(path).Mood; math.Abs(got-want) > epsilon {
		t.Fatalf("checkpointed mood = %v, want %v", got, want)
	}
}

// TestMoodDrift_ClampsValence proves valence never leaves [min, max] no matter how
// many ticks it drifts.
func TestMoodDrift_ClampsValence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// Start at the bound OPPOSITE the drift direction so the drift travels the
	// full range and slams into the far clamp (a negative step must start at
	// max to descend into the min clamp).
	start := moodValenceMin
	if moodDriftStep < 0 {
		start = moodValenceMax
	}
	store := state.New(state.Personality{Mood: start}, path)
	md := NewMoodDrift(store)

	for i := 0; i < 1000; i++ { // far more ticks than the range needs
		md.Run(context.Background())
	}

	got := store.Snapshot().Mood
	if got < moodValenceMin || got > moodValenceMax {
		t.Fatalf("mood %v escaped clamp [%v, %v]", got, moodValenceMin, moodValenceMax)
	}
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
