package reflexes

import (
	"context"
	"log/slog"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/state"
)

// Tunable mood-drift cadence and step (story-time config, not invariants —
// architecture Deferred). The drift is a fixed signed step per cadence so the
// accumulated drift over time is linear (AD: step × cadences), clamped to the
// valence range.
const (
	moodDriftInterval = 6 * time.Hour // four drifts per day
	moodDriftStep     = -0.02         // sign/magnitude tunable
	moodValenceMin    = -1.0
	moodValenceMax    = 1.0
)

// Expression thresholds: the valence bands that map to a face expression.
const (
	moodHappyThreshold = 0.3
	moodSadThreshold   = -0.3
)

// MoodDrift is the reflex that slowly drifts personality valence on a cadence and
// checkpoints it, so the pet's mood shifts believably across days with no LLM
// (AD-13). It is state-only — the expression-aware blink renders the drifted mood.
type MoodDrift struct {
	store *state.Store
}

// NewMoodDrift returns a mood-drift reflex over store.
func NewMoodDrift(store *state.Store) *MoodDrift {
	return &MoodDrift{store: store}
}

// Serve drifts the mood every moodDriftInterval until ctx is cancelled. Each tick
// moves valence by moodDriftStep (clamped) and checkpoints the drift (AD-16). It
// is wrapped by supervisor.Guard (AD-5) and shaped so the reflex-tier scheduler
// (Story 2.5) can own its cadence with no rewrite.
func (m *MoodDrift) Serve(ctx context.Context) error {
	ticker := time.NewTicker(moodDriftInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			next := clamp(m.store.Snapshot().Mood+moodDriftStep, moodValenceMin, moodValenceMax)
			m.store.SetMood(next)
			if err := m.store.Checkpoint(); err != nil {
				slog.Error("mood-drift checkpoint failed", "err", err)
			}
		}
	}
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// expressionFor maps a valence to the face expression a renderer should show.
func expressionFor(mood float64) contracts.Expression {
	switch {
	case mood >= moodHappyThreshold:
		return contracts.ExpressionHappy
	case mood <= moodSadThreshold:
		return contracts.ExpressionSad
	default:
		return contracts.ExpressionNeutral
	}
}
